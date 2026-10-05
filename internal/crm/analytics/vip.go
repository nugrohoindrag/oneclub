package analytics

// VIP segmentation (FR-SEG-03): automatic VIPs from the Top Spender rank
// and the loyalty tier, manual VIPs by staff, benefits and handling notes,
// and the VIP marker read at check-in and POS.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
)

func location(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	l, err := org.Location(ctx, q, property)
	if err != nil || l == nil {
		return time.UTC
	}
	return l
}

// VIPCustomer is a VIP of the property.
type VIPCustomer struct {
	ID           uuid.UUID `json:"id" db:"id"`
	CustomerID   uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerCode string    `json:"customerCode" db:"customer_code"`
	CustomerName string    `json:"customerName" db:"customer_name"`
	Level        string    `json:"level" db:"level" enum:"vip,vvip"`
	Source       string    `json:"source" db:"source" enum:"top_spender,tier,manual"`
	Reason       *string   `json:"reason" db:"reason"`
	Benefits     *string   `json:"benefits" db:"benefits"`
	HandlingNote *string   `json:"handlingNote" db:"handling_note"`
	ValidUntil   *string   `json:"validUntil" db:"valid_until"`
	Status       string    `json:"status" db:"status" enum:"active,inactive"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt    time.Time `json:"updatedAt" db:"updated_at"`
}

const vipSelect = `SELECT v.id, v.customer_id, c.code AS customer_code, c.name AS customer_name, v.level, v.source, v.reason, v.benefits, v.handling_note,
	to_char(v.valid_until, 'YYYY-MM-DD') AS valid_until, v.status, v.created_at, v.updated_at FROM crm.vip_customers v JOIN crm.customers c ON c.id = v.customer_id`

// VIPs lists the VIPs of a property.
func VIPs(ctx context.Context, q dbtx.Querier, property uuid.UUID, status, level, source, text string, limit int) ([]VIPCustomer, error) {
	if limit <= 0 {
		limit = 200
	}
	return handle.List[VIPCustomer](q.Query(ctx, vipSelect+` WHERE v.property_id = $1 AND ($2 = '' OR v.status = $2) AND ($3 = '' OR v.level = $3)
		AND ($4 = '' OR v.source = $4) AND ($5 = '' OR c.name ILIKE '%' || $5 || '%' OR c.code ILIKE '%' || $5 || '%')
		ORDER BY v.level DESC, c.name, v.id LIMIT $6`, property, status, level, source, text, limit))
}

// GetVIP loads a VIP row of a property.
func GetVIP(ctx context.Context, q dbtx.Querier, property, vid uuid.UUID) (VIPCustomer, error) {
	rows, err := q.Query(ctx, vipSelect+` WHERE v.id = $1 AND v.property_id = $2`, vid, property)
	return handle.One[VIPCustomer](rows, err, "VIP")
}

// VIPInput marks a customer as VIP manually.
type VIPInput struct {
	CustomerID   uuid.UUID `json:"customerId"`
	Level        string    `json:"level,omitempty" enum:"vip,vvip"`
	Reason       string    `json:"reason"`
	Benefits     string    `json:"benefits,omitempty"`
	HandlingNote string    `json:"handlingNote,omitempty"`
	ValidUntil   string    `json:"validUntil,omitempty" doc:"YYYY-MM-DD"`
}

// VIPDeactivateInput ends a VIP status.
type VIPDeactivateInput struct {
	Reason string `json:"reason"`
}

// SetVIP marks a customer as VIP (manual source; replaces an automatic row).
func SetVIP(ctx context.Context, tx pgx.Tx, property uuid.UUID, in VIPInput) (VIPCustomer, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return VIPCustomer{}, err
	}
	if in.Level == "" {
		in.Level = "vip"
	}
	if in.Level != "vip" && in.Level != "vvip" {
		return VIPCustomer{}, handle.Invalid("level", "invalid", "vip or vvip")
	}
	var until *time.Time
	if in.ValidUntil != "" {
		d, err := time.Parse("2006-01-02", in.ValidUntil)
		if err != nil {
			return VIPCustomer{}, handle.Invalid("validUntil", "invalid_date", "YYYY-MM-DD")
		}
		until = &d
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.customers WHERE id = $1 AND property_id = $2 AND status = 'active')`, in.CustomerID, property).
		Scan(&ok); err != nil {
		return VIPCustomer{}, err
	}
	if !ok {
		return VIPCustomer{}, handle.Invalid("customerId", "not_found", "active customer not found in this property")
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return VIPCustomer{}, err
	}
	benefits, handling := in.Benefits, in.HandlingNote
	if benefits == "" {
		benefits = pol.VIPBenefits
	}
	if handling == "" {
		handling = pol.VIPHandling
	}
	var vid uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO crm.vip_customers (id, property_id, customer_id, level, source, reason, benefits, handling_note, valid_until,
		created_by, updated_by) VALUES ($1,$2,$3,$4,'manual',$5,$6,$7,$8,$9,$9) ON CONFLICT (customer_id) DO UPDATE SET level = EXCLUDED.level,
		source = 'manual', reason = EXCLUDED.reason, benefits = EXCLUDED.benefits, handling_note = EXCLUDED.handling_note, valid_until = EXCLUDED.valid_until,
		status = 'active', updated_by = EXCLUDED.updated_by RETURNING id`, id.New(), property, in.CustomerID, in.Level, in.Reason, benefits, handling, until,
		actor(ctx)).Scan(&vid); err != nil {
		return VIPCustomer{}, err
	}
	return GetVIP(ctx, tx, property, vid)
}

// DeactivateVIP ends a VIP status.
func DeactivateVIP(ctx context.Context, tx pgx.Tx, property, vid uuid.UUID, reason string) (VIPCustomer, VIPCustomer, error) {
	before, err := GetVIP(ctx, tx, property, vid)
	if err != nil {
		return before, before, err
	}
	if err := handle.Required("reason", reason); err != nil {
		return before, before, err
	}
	if before.Status != "active" {
		return before, before, errs.Conflict("vip_inactive", "the VIP status is already inactive")
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.vip_customers SET status = 'inactive', reason = $2, source = 'manual', updated_by = $3 WHERE id = $1`, vid,
		"ended: "+reason, actor(ctx)); err != nil {
		return before, before, err
	}
	after, err := GetVIP(ctx, tx, property, vid)
	return before, after, err
}

// VIPFlag is the VIP marker shown at check-in and POS.
type VIPFlag struct {
	CustomerID   uuid.UUID `json:"customerId"`
	VIP          bool      `json:"vip"`
	Level        *string   `json:"level" enum:"vip,vvip"`
	Source       *string   `json:"source"`
	Benefits     *string   `json:"benefits"`
	HandlingNote *string   `json:"handlingNote"`
	TierName     *string   `json:"tierName" doc:"Loyalty tier"`
	RFMGroup     *string   `json:"rfmGroup"`
}

// Flag returns the VIP marker of a customer.
func Flag(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (VIPFlag, error) {
	out := VIPFlag{CustomerID: customer}
	err := q.QueryRow(ctx, `SELECT level, source, benefits, handling_note FROM crm.vip_customers WHERE customer_id = $1 AND property_id = $2 AND status = 'active'
		AND (valid_until IS NULL OR valid_until >= $3::date)`, customer, property, localToday(ctx, q, property)).Scan(&out.Level, &out.Source, &out.Benefits, &out.HandlingNote)
	if err != nil && !dbtx.IsNoRows(err) {
		return out, err
	}
	out.VIP = err == nil
	if err := q.QueryRow(ctx, `SELECT t.name FROM crm.loyalty_accounts a JOIN crm.loyalty_tiers t ON t.id = a.tier_id WHERE a.customer_id = $1
		AND a.status = 'active'`, customer).Scan(&out.TierName); err != nil && !dbtx.IsNoRows(err) {
		return out, err
	}
	if err := q.QueryRow(ctx, `SELECT rfm_group FROM crm.rfm_scores WHERE customer_id = $1 AND property_id = $2 ORDER BY as_of DESC LIMIT 1`, customer,
		property).Scan(&out.RFMGroup); err != nil && !dbtx.IsNoRows(err) {
		return out, err
	}
	return out, nil
}

// Section is the "VIP & RFM" section of the Customer 360.
func Section(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	return Behavior(ctx, q, property, customer)
}
