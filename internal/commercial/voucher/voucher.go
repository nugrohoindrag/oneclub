package voucher

// Voucher & Prepaid (PRD P2 EP-19). Vouchers are value (Rp) or quota
// (entries, sessions, balls) instruments with an append-only ledger.
// Financial treatment (FR-VCH-08): the sale price is deferred revenue; each
// redemption recognises quantity × unit value; expiry books the remaining
// liability as breakage. Redemption is idempotent per key and serialised per
// voucher (row lock), so one voucher can never be used twice concurrently.

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// VoucherTypes master (FR-VCH-01).
var VoucherTypes = &resource.Def{
	Key: "commercial.voucher_type", Module: "commercial", Perm: "commercial.voucher_type", Path: "/api/v1/commercial/voucher-types",
	Table: "commercial.voucher_types", Name: "Voucher Type", Plural: "Voucher Types", Tag: "Voucher & Prepaid", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"value", "quota", "promo"}, Required: true, CreateOnly: true, Filter: true},
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Filter: true, Default: "other",
			Enum: []string{"driving_range_balls", "sport_entry", "class_package", "court_package", "fnb", "hole_in_one", "gift", "promo", "other"}},
		{Name: "unit", Column: "unit", Label: "Unit", Kind: resource.Enum, Enum: []string{"rupiah", "entry", "session", "ball", "round", "use"}, Default: "rupiah"},
		{Name: "faceValue", Column: "face_value", Label: "Face Value / Quota", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "price", Column: "price", Label: "Sale Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "memberPrice", Column: "member_price", Label: "Member Price", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "validityDays", Column: "validity_days", Label: "Validity (days)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "validityMonths", Column: "validity_months", Label: "Validity (months)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "applicableServices", Column: "applicable_services", Label: "Applicable Services (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "applicableResourceTypes", Column: "applicable_resource_types", Label: "Applicable Resource Types", Kind: resource.StringList, Default: []string{}},
		{Name: "applicableItems", Column: "applicable_items", Label: "Applicable Items", Kind: resource.StringList, Default: []string{}},
		{Name: "transferable", Column: "transferable", Label: "Transferable", Kind: resource.Bool, Default: false},
		{Name: "prepaid", Column: "prepaid", Label: "Prepaid Balance", Kind: resource.Bool, Default: false},
		{Name: "discountPercent", Column: "discount_percent", Label: "Discount (%)", Kind: resource.Decimal, Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "discountAmount", Column: "discount_amount", Label: "Discount Amount", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "maxUses", Column: "max_uses", Label: "Maximum Uses (promo)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component on Redemption", Kind: resource.Enum, Enum: billing.RevenueComponents, Default: "other"},
		resource.Status("active", "inactive")},
}

// Approval document types (FR-VCH-02, FR-VCH-05, FR-VCH-07).
var (
	VoucherIssueType  = provision.DocumentType{Code: "voucher_issue", Module: "commercial", Name: "Voucher Issue (complimentary / promo)", Attributes: []provision.DocumentAttribute{{Key: "value", Label: "Value", Type: "number"}}}
	VoucherExtendType = provision.DocumentType{Code: "voucher_extend", Module: "commercial", Name: "Voucher Expiry Extension", Attributes: []provision.DocumentAttribute{{Key: "days", Label: "Days", Type: "number"}}}
	VoucherAdjustType = provision.DocumentType{Code: "voucher_adjust", Module: "commercial", Name: "Voucher Balance Adjustment", Attributes: []provision.DocumentAttribute{{Key: "value", Label: "Value", Type: "number"}}}
)

// LedgerEntry is one voucher usage history row (FR-VCH-04).
type LedgerEntry struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	VoucherID    uuid.UUID  `json:"voucherId" db:"voucher_id"`
	VoucherCode  string     `json:"voucherCode" db:"voucher_code"`
	EntryType    string     `json:"entryType" db:"entry_type"`
	Quantity     string     `json:"quantity" db:"quantity"`
	Amount       string     `json:"amount" db:"amount"`
	BalanceAfter string     `json:"balanceAfter" db:"balance_after"`
	ServiceType  *string    `json:"serviceType" db:"service_type"`
	Terminal     *string    `json:"terminal" db:"terminal"`
	Reference    *string    `json:"reference" db:"reference"`
	SourceType   *string    `json:"sourceType" db:"source_type"`
	SourceID     *uuid.UUID `json:"sourceId" db:"source_id"`
	Reason       *string    `json:"reason" db:"reason"`
	ActorName    *string    `json:"actorName" db:"actor_name"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
}

const voucherSelect = `SELECT v.id, v.code, v.voucher_type_id, t.code AS type_code, t.name AS type_name, t.kind, t.category, t.unit, t.prepaid, t.transferable,
	v.customer_id, c.name AS customer_name, v.status, trim_scale(v.original_quantity)::text AS original_quantity, trim_scale(v.remaining_quantity)::text AS remaining_quantity, trim_scale(v.unit_value)::text AS unit_value, trim_scale(v.price_paid)::text AS price_paid,
	v.uses_count, v.issued_via, v.folio_id, v.issued_at, v.expires_at, v.void_reason
	FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id LEFT JOIN reporting.customer_directory c ON c.id = v.customer_id`

const ledgerSelect = `SELECT l.id, l.voucher_id, v.code AS voucher_code, l.entry_type, trim_scale(l.quantity)::text AS quantity, trim_scale(l.amount)::text AS amount, trim_scale(l.balance_after)::text AS balance_after, l.service_type,
	l.terminal, l.reference, l.source_type, l.source_id, l.reason, l.actor_name, l.created_at
	FROM commercial.voucher_ledger l JOIN commercial.vouchers v ON v.id = l.voucher_id`

type voucherType struct {
	ID               uuid.UUID   `db:"id"`
	PropertyID       uuid.UUID   `db:"property_id"`
	Code             string      `db:"code"`
	Name             string      `db:"name"`
	Kind             string      `db:"kind"`
	Category         string      `db:"category"`
	Unit             string      `db:"unit"`
	FaceValue        string      `db:"face_value"`
	Price            string      `db:"price"`
	MemberPrice      *string     `db:"member_price"`
	ValidityDays     *int        `db:"validity_days"`
	ValidityMonths   *int        `db:"validity_months"`
	Services         []string    `db:"applicable_services"`
	ResourceTypes    []string    `db:"applicable_resource_types"`
	Items            []string    `db:"applicable_items"`
	Outlets          []uuid.UUID `db:"applicable_outlets"`
	Transferable     bool        `db:"transferable"`
	Prepaid          bool        `db:"prepaid"`
	DiscountPercent  *string     `db:"discount_percent"`
	DiscountAmount   *string     `db:"discount_amount"`
	MaxUses          *int        `db:"max_uses"`
	RevenueComponent string      `db:"revenue_component"`
	Status           string      `db:"status"`
}

func loadVoucherType(ctx context.Context, q dbtx.Querier, where string, arg ...any) (voucherType, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, code, name, kind, category, unit, trim_scale(face_value)::text AS face_value, trim_scale(price)::text AS price, trim_scale(member_price)::text AS member_price,
		validity_days, validity_months, applicable_services, applicable_resource_types, applicable_items, applicable_outlets, transferable,
		prepaid, trim_scale(discount_percent)::text AS discount_percent, trim_scale(discount_amount)::text AS discount_amount, max_uses, revenue_component, status FROM commercial.voucher_types WHERE `+where, arg...)
	return handle.One[voucherType](rows, err, "voucher type")
}

const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ" // no 0/O/1/I

// newVoucherCode returns an unguessable 15-character code (75 bits) shown as
// XXXXX-XXXXX-XXXXX (FR-VCH-10).
func newVoucherCode() string {
	var b strings.Builder
	for i := 0; i < 15; i++ {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String()
}

func normalizeCode(c string) string { return strings.ToUpper(strings.TrimSpace(c)) }

// IssueRequest creates a voucher (sale, complimentary issue, auto issue,
// migration).
type IssueRequest struct {
	PropertyID uuid.UUID
	TypeID     *uuid.UUID
	TypeCode   string
	CustomerID *uuid.UUID
	Via        string // sale | issue | auto | migration
	PricePaid  decimal.Decimal
	Quantity   *decimal.Decimal // override of the type face value (migration of partly used vouchers)
	Original   *decimal.Decimal
	ExpiresAt  *time.Time
	SourceType string
	SourceID   *uuid.UUID
	FolioID    *uuid.UUID
	Status     string // default active
	Notes      string
	Code       string // migration keeps the legacy code
}

// Issue creates a voucher and its ledger entry; sold vouchers defer revenue.
func (m *Module) Issue(ctx context.Context, tx pgx.Tx, r IssueRequest) (Voucher, error) {
	var vt voucherType
	var err error
	if r.TypeID != nil {
		vt, err = loadVoucherType(ctx, tx, `id = $1`, *r.TypeID)
	} else {
		vt, err = loadVoucherType(ctx, tx, `property_id = $1 AND code = $2`, r.PropertyID, r.TypeCode)
	}
	if err != nil {
		return Voucher{}, err
	}
	if vt.PropertyID != r.PropertyID {
		return Voucher{}, errs.NotFound("voucher type")
	}
	if vt.Status != "active" && r.Via != "migration" {
		return Voucher{}, errs.Conflict("voucher_type_inactive", "voucher type "+vt.Name+" is inactive")
	}
	face, _ := decimal.NewFromString(vt.FaceValue)
	orig := face
	if vt.Kind == "promo" {
		orig = decimal.NewFromInt(int64(max(1, ptrInt(vt.MaxUses))))
	}
	if r.Original != nil {
		orig = *r.Original
	}
	remaining := orig
	if r.Quantity != nil {
		remaining = *r.Quantity
	}
	if !orig.IsPositive() {
		return Voucher{}, errs.Validation("invalid_voucher_type", "voucher type "+vt.Name+" has no face value / quota")
	}
	unitValue := decimal.Zero
	if r.PricePaid.IsPositive() {
		unitValue = r.PricePaid.Div(orig)
	}
	now := clock.Now()
	exp := r.ExpiresAt
	if exp == nil {
		loc := calendar.Location(ctx, tx)
		l := now.In(loc)
		var t time.Time
		switch {
		case vt.ValidityMonths != nil:
			t = time.Date(l.Year(), l.Month()+time.Month(*vt.ValidityMonths), l.Day(), 23, 59, 59, 0, loc)
			exp = &t
		case vt.ValidityDays != nil:
			t = time.Date(l.Year(), l.Month(), l.Day()+*vt.ValidityDays, 23, 59, 59, 0, loc)
			exp = &t
		}
	}
	status := r.Status
	if status == "" {
		status = "active"
		if remaining.LessThan(orig) {
			status = "partially_redeemed"
		}
	}
	code := normalizeCode(r.Code)
	if code == "" {
		code = newVoucherCode()
	}
	vid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.vouchers (id, property_id, code, voucher_type_id, customer_id, status, original_quantity,
		remaining_quantity, unit_value, price_paid, issued_via, source_type, source_id, folio_id, expires_at, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11,$12,$13,$14,$15,$16,$17)`,
		vid, r.PropertyID, code, vt.ID, r.CustomerID, status, orig.String(), remaining.String(), unitValue.String(), r.PricePaid.String(),
		r.Via, nullStr(r.SourceType), r.SourceID, r.FolioID, exp, nullStr(r.Notes), actorID(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return Voucher{}, errs.Validation("invalid_customer", "customer not found")
		}
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Voucher{}, errs.Conflict("duplicate_code", "voucher code "+code+" already exists")
		}
		return Voucher{}, err
	}
	entry := "issue"
	if r.Via == "sale" {
		entry = "sale"
	}
	if err := m.ledger(ctx, tx, r.PropertyID, vid, entry, remaining, decimal.Zero, remaining, ledgerOpts{sourceType: r.SourceType, sourceID: r.SourceID}); err != nil {
		return Voucher{}, err
	}
	if r.PricePaid.IsPositive() {
		liab := r.PricePaid
		if r.Quantity != nil && remaining.LessThan(orig) {
			liab = remaining.Mul(unitValue).Round(0) // migrated partly used voucher: only the remaining liability
		}
		if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: r.PropertyID, LiabilityType: liabilityType(vt), RefType: "commercial.voucher",
			RefID: vid, EntryType: "deferral", Amount: liab, RevenueComponent: vt.RevenueComponent, Description: "Voucher " + code + " sold",
			IdempotencyKey: "voucher-deferral-" + vid.String()}); err != nil {
			return Voucher{}, err
		}
	}
	v, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return v, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionCreate, EntityType: "commercial.voucher",
		EntityID: vid.String(), EntityLabel: code + " · " + vt.Name, PropertyID: &r.PropertyID, After: v}); err != nil {
		return v, err
	}
	if r.Via == "sale" {
		_, err = m.Events.Publish(ctx, tx, "commercial.voucher_sold", "commercial.voucher", &vid, &r.PropertyID, map[string]any{
			"voucherId": vid, "code": code, "type": vt.Code, "customerId": r.CustomerID, "price": r.PricePaid.String()})
	}
	return v, err
}

func ptrInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func liabilityType(vt voucherType) string {
	if vt.Prepaid {
		return "prepaid"
	}
	return "voucher"
}

type ledgerOpts struct {
	serviceType, terminal, reference, sourceType, reason, idem string
	sourceID                                                   *uuid.UUID
	outletID                                                   *uuid.UUID
	details                                                    map[string]any
}

func (m *Module) ledger(ctx context.Context, tx pgx.Tx, property, vid uuid.UUID, typ string, qty, amount, balance decimal.Decimal, o ledgerOpts) error {
	name := "system"
	if p := authz.From(ctx); p != nil {
		name = p.Name
	}
	det := o.details
	if det == nil {
		det = map[string]any{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO commercial.voucher_ledger (id, property_id, voucher_id, entry_type, quantity, amount, balance_after, service_type,
		outlet_id, terminal, reference, source_type, source_id, reason, details, idempotency_key, actor_id, actor_name)
		VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		id.New(), property, vid, typ, qty.String(), amount.String(), balance.String(), nullStr(o.serviceType), o.outletID, nullStr(o.terminal),
		nullStr(o.reference), nullStr(o.sourceType), o.sourceID, nullStr(o.reason), det, nullStr(o.idem), actorID(ctx), name)
	return err
}

// Voucher returns a voucher by id.
func (m *Module) Voucher(ctx context.Context, q dbtx.Querier, vid uuid.UUID) (Voucher, error) {
	rows, err := q.Query(ctx, voucherSelect+` WHERE v.id = $1`, vid)
	return handle.One[Voucher](rows, err, "voucher")
}

// VoucherByCode returns a voucher by code at a property.
func (m *Module) VoucherByCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (Voucher, error) {
	rows, err := q.Query(ctx, voucherSelect+` WHERE v.property_id = $1 AND v.code = $2`, property, normalizeCode(code))
	return handle.One[Voucher](rows, err, "voucher")
}

// liabilityOf is the remaining deferred liability of a voucher.
func (m *Module) liabilityOf(ctx context.Context, q dbtx.Querier, vid uuid.UUID) (decimal.Decimal, error) {
	var raw string
	err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(amount), 0))::text FROM billing.deferred_revenue_entries WHERE ref_type = 'commercial.voucher' AND ref_id = $1`, vid).Scan(&raw)
	d, _ := decimal.NewFromString(raw)
	return d, err
}

// Redeem uses a voucher. The row lock makes concurrent use from two
// terminals sequential; the idempotency key makes retries harmless.
func (m *Module) Redeem(ctx context.Context, tx pgx.Tx, r RedeemRequest) (RedeemResult, error) {
	var vid uuid.UUID
	var err error
	if r.VoucherID != nil {
		err = tx.QueryRow(ctx, `SELECT id FROM commercial.vouchers WHERE id = $1 AND property_id = $2 FOR UPDATE`, *r.VoucherID, r.PropertyID).Scan(&vid)
	} else {
		err = tx.QueryRow(ctx, `SELECT id FROM commercial.vouchers WHERE code = $1 AND property_id = $2 FOR UPDATE`, normalizeCode(r.Code), r.PropertyID).Scan(&vid)
	}
	if dbtx.IsNoRows(err) {
		return RedeemResult{}, errs.NotFound("voucher")
	}
	if err != nil {
		return RedeemResult{}, err
	}
	if r.IdempotencyKey != "" {
		var qty, amt string
		err := tx.QueryRow(ctx, `SELECT trim_scale(-quantity)::text, trim_scale(amount)::text AS amount FROM commercial.voucher_ledger WHERE voucher_id = $1 AND idempotency_key = $2`,
			vid, r.IdempotencyKey).Scan(&qty, &amt)
		if err == nil {
			v, err := m.Voucher(ctx, tx, vid)
			if err != nil {
				return RedeemResult{}, err
			}
			pid := r.PropertyID
			return RedeemResult{Voucher: v, Quantity: qty, Recognized: amt, Discount: "0", Duplicate: true},
				audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "redeem_duplicate", EntityType: "commercial.voucher", EntityID: vid.String(),
					EntityLabel: v.Code, PropertyID: &pid, Metadata: map[string]any{"idempotencyKey": r.IdempotencyKey}})
		}
		if !dbtx.IsNoRows(err) {
			return RedeemResult{}, err
		}
	}
	v, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return RedeemResult{}, err
	}
	vt, err := loadVoucherType(ctx, tx, `id = $1`, v.VoucherTypeID)
	if err != nil {
		return RedeemResult{}, err
	}
	switch v.Status {
	case "active", "partially_redeemed":
	case "pending":
		return RedeemResult{}, errs.Conflict("voucher_pending", "voucher "+v.Code+" is waiting for approval")
	default:
		return RedeemResult{}, errs.Conflict("voucher_"+v.Status, "voucher "+v.Code+" is "+v.Status)
	}
	if v.ExpiresAt != nil && !v.ExpiresAt.After(clock.Now()) {
		return RedeemResult{}, errs.Conflict("voucher_expired", "voucher "+v.Code+" expired on "+v.ExpiresAt.Format("2006-01-02"))
	}
	if len(vt.Services) > 0 && r.ServiceType != "" && !slices.Contains(vt.Services, r.ServiceType) {
		return RedeemResult{}, errs.Conflict("voucher_not_applicable", "voucher "+v.Code+" cannot be used for "+r.ServiceType)
	}
	if len(vt.ResourceTypes) > 0 && r.ResourceType != "" && !slices.Contains(vt.ResourceTypes, r.ResourceType) {
		return RedeemResult{}, errs.Conflict("voucher_not_applicable", "voucher "+v.Code+" cannot be used for "+r.ResourceType)
	}
	if len(vt.Items) > 0 && r.ItemRef != "" && !slices.Contains(vt.Items, r.ItemRef) {
		return RedeemResult{}, errs.Conflict("voucher_not_applicable", "voucher "+v.Code+" cannot be used for this item")
	}
	if len(vt.Outlets) > 0 && r.OutletID != nil && !slices.Contains(vt.Outlets, *r.OutletID) {
		return RedeemResult{}, errs.Conflict("voucher_not_applicable", "voucher "+v.Code+" is not valid at this outlet")
	}
	if v.CustomerID != nil && r.CustomerID != nil && *v.CustomerID != *r.CustomerID && !vt.Transferable {
		return RedeemResult{}, errs.Conflict("voucher_other_customer", "voucher "+v.Code+" belongs to another customer")
	}
	remaining, _ := decimal.NewFromString(v.RemainingQuantity)
	qty := r.Quantity
	discount := decimal.Zero
	if vt.Kind == "promo" {
		qty = decimal.NewFromInt(1)
		switch {
		case vt.DiscountPercent != nil:
			p, _ := decimal.NewFromString(*vt.DiscountPercent)
			discount = r.BaseAmount.Mul(p).Div(decimal.NewFromInt(100)).Round(0)
		case vt.DiscountAmount != nil:
			discount, _ = decimal.NewFromString(*vt.DiscountAmount)
			if r.BaseAmount.IsPositive() && discount.GreaterThan(r.BaseAmount) {
				discount = r.BaseAmount
			}
		}
	}
	if qty.IsZero() {
		qty = decimal.NewFromInt(1)
	}
	if !qty.IsPositive() {
		return RedeemResult{}, handle.Invalid("quantity", "invalid_quantity", "quantity must be positive")
	}
	if qty.GreaterThan(remaining) {
		return RedeemResult{}, errs.Conflict("insufficient_balance", fmt.Sprintf("voucher %s has only %s %s left", v.Code, remaining.String(), vt.Unit))
	}
	after := remaining.Sub(qty)
	unitValue, _ := decimal.NewFromString(v.UnitValue)
	recognized := qty.Mul(unitValue).Round(0)
	liab, err := m.liabilityOf(ctx, tx, vid)
	if err != nil {
		return RedeemResult{}, err
	}
	if after.IsZero() || recognized.GreaterThan(liab) {
		recognized = liab // last use takes the residue so the liability ends at exactly zero
	}
	status := "partially_redeemed"
	if after.IsZero() {
		status = "redeemed"
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET remaining_quantity = $2::numeric, status = $3, uses_count = uses_count + 1, updated_by = $4 WHERE id = $1`,
		vid, after.String(), status, actorID(ctx)); err != nil {
		return RedeemResult{}, err
	}
	if err := m.ledger(ctx, tx, r.PropertyID, vid, "redemption", qty.Neg(), recognized, after, ledgerOpts{serviceType: r.ServiceType, terminal: r.Terminal,
		reference: r.Reference, sourceType: r.SourceType, sourceID: r.SourceID, outletID: r.OutletID, idem: r.IdempotencyKey,
		details: map[string]any{"discount": discount.String()}}); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return RedeemResult{}, errs.Conflict("duplicate_redemption", "this redemption was already processed")
		}
		return RedeemResult{}, err
	}
	if recognized.IsPositive() {
		if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: r.PropertyID, LiabilityType: liabilityType(vt), RefType: "commercial.voucher",
			RefID: vid, EntryType: "recognition", Amount: recognized, RevenueComponent: vt.RevenueComponent,
			Description: fmt.Sprintf("Voucher %s used: %s %s", v.Code, qty.String(), vt.Unit)}); err != nil {
			return RedeemResult{}, err
		}
	}
	after2, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return RedeemResult{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "redeem", EntityType: "commercial.voucher", EntityID: vid.String(),
		EntityLabel: v.Code, PropertyID: &r.PropertyID, Before: v, After: after2,
		Metadata: map[string]any{"quantity": qty.String(), "serviceType": r.ServiceType, "terminal": r.Terminal}}); err != nil {
		return RedeemResult{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, "commercial.voucher_redeemed", "commercial.voucher", &vid, &r.PropertyID, map[string]any{
		"voucherId": vid, "code": v.Code, "type": vt.Code, "quantity": qty.String(), "recognized": recognized.String(), "customerId": v.CustomerID,
		"serviceType": r.ServiceType}); err != nil {
		return RedeemResult{}, err
	}
	return RedeemResult{Voucher: after2, Quantity: qty.String(), Recognized: recognized.String(), Discount: discount.String()}, nil
}

// RedeemBalance uses a customer's prepaid balance of a voucher category
// (e.g. balls), oldest expiry first (FR-RNG-04).
func (m *Module) RedeemBalance(ctx context.Context, tx pgx.Tx, r RedeemRequest, category string) ([]RedeemResult, error) {
	if r.CustomerID == nil {
		return nil, handle.Invalid("customerId", "required", "customer is required to use a prepaid balance")
	}
	rows, err := tx.Query(ctx, `SELECT v.id, trim_scale(v.remaining_quantity)::text AS remaining_quantity FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
		WHERE v.property_id = $1 AND v.customer_id = $2 AND t.category = $3 AND t.prepaid AND v.status IN ('active', 'partially_redeemed')
		AND (v.expires_at IS NULL OR v.expires_at > now()) ORDER BY v.expires_at NULLS LAST, v.issued_at`, r.PropertyID, *r.CustomerID, category)
	if err != nil {
		return nil, err
	}
	type bal struct {
		id  uuid.UUID
		rem decimal.Decimal
	}
	var bals []bal
	total := decimal.Zero
	for rows.Next() {
		var b bal
		var s string
		if err := rows.Scan(&b.id, &s); err != nil {
			rows.Close()
			return nil, err
		}
		b.rem, _ = decimal.NewFromString(s)
		total = total.Add(b.rem)
		bals = append(bals, b)
	}
	rows.Close()
	if total.LessThan(r.Quantity) {
		return nil, errs.Conflict("insufficient_balance", "prepaid balance is "+total.String()+", "+r.Quantity.String()+" needed")
	}
	need := r.Quantity
	var out []RedeemResult
	for i, b := range bals {
		if !need.IsPositive() {
			break
		}
		take := decimal.Min(need, b.rem)
		rr := r
		vid := b.id
		rr.VoucherID, rr.Quantity = &vid, take
		if r.IdempotencyKey != "" {
			rr.IdempotencyKey = fmt.Sprintf("%s#%d", r.IdempotencyKey, i)
		}
		res, err := m.Redeem(ctx, tx, rr)
		if err != nil {
			return out, err
		}
		out = append(out, res)
		need = need.Sub(take)
	}
	return out, nil
}

// ExpireDue expires vouchers past their expiry and books breakage for the
// remaining liability (FR-VCH-05, FR-VCH-08). Runs daily.
func (m *Module) ExpireDue(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id, property_id FROM commercial.vouchers WHERE status IN ('active', 'partially_redeemed') AND expires_at <= now() FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	type x struct{ id, prop uuid.UUID }
	var list []x
	for rows.Next() {
		var a x
		if err := rows.Scan(&a.id, &a.prop); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, a)
	}
	rows.Close()
	for _, a := range list {
		v, err := m.Voucher(ctx, tx, a.id)
		if err != nil {
			return 0, err
		}
		vt, err := loadVoucherType(ctx, tx, `id = $1`, v.VoucherTypeID)
		if err != nil {
			return 0, err
		}
		liab, err := m.liabilityOf(ctx, tx, a.id)
		if err != nil {
			return 0, err
		}
		rem, _ := decimal.NewFromString(v.RemainingQuantity)
		if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET status = 'expired', remaining_quantity = 0 WHERE id = $1`, a.id); err != nil {
			return 0, err
		}
		if err := m.ledger(ctx, tx, a.prop, a.id, "expiry", rem.Neg(), liab, decimal.Zero, ledgerOpts{reason: "expired"}); err != nil {
			return 0, err
		}
		if liab.IsPositive() {
			if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: a.prop, LiabilityType: liabilityType(vt), RefType: "commercial.voucher",
				RefID: a.id, EntryType: "breakage", Amount: liab, RevenueComponent: "breakage", Description: "Voucher " + v.Code + " expired",
				IdempotencyKey: "voucher-breakage-" + a.id.String()}); err != nil {
				return 0, err
			}
		}
		prop := a.prop
		vid := a.id
		if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionStatusChange, EntityType: "commercial.voucher",
			EntityID: a.id.String(), EntityLabel: v.Code, PropertyID: &prop, Before: v, Reason: "expired", Metadata: map[string]any{"breakage": liab.String()}}); err != nil {
			return 0, err
		}
		if _, err := m.Events.Publish(ctx, tx, "commercial.voucher_expired", "commercial.voucher", &vid, &prop, map[string]any{
			"voucherId": vid, "code": v.Code, "remaining": rem.String(), "breakage": liab.String(), "customerId": v.CustomerID}); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// TenderHandler is the C3 voucher tender: a value voucher pays (part of) a
// folio. Quota vouchers are redeemed against their service instead.
func (m *Module) TenderHandler() billing.TenderHandler {
	return func(ctx context.Context, tx pgx.Tx, r billing.TenderRequest) (billing.TenderResult, error) {
		code, _ := r.Data["code"].(string)
		if code == "" {
			return billing.TenderResult{}, handle.Invalid("tender.code", "required", "voucher code is required")
		}
		v, err := m.VoucherByCode(ctx, tx, r.PropertyID, code)
		if err != nil {
			return billing.TenderResult{}, err
		}
		if v.Kind != "value" {
			return billing.TenderResult{}, errs.Conflict("voucher_not_value", "voucher "+v.Code+" is a "+v.Kind+" voucher and cannot pay an amount")
		}
		rem, _ := decimal.NewFromString(v.RemainingQuantity)
		amt := decimal.Min(r.Amount, rem)
		if !amt.IsPositive() {
			return billing.TenderResult{}, errs.Conflict("insufficient_balance", "voucher "+v.Code+" has no balance left")
		}
		svc := ""
		switch r.BusinessLine {
		case billing.LinePOS:
			svc = "pos"
		}
		res, err := m.Redeem(ctx, tx, RedeemRequest{PropertyID: r.PropertyID, VoucherID: &v.ID, Quantity: amt, ServiceType: svc,
			CustomerID: r.CustomerID, SourceType: "billing.folio", SourceID: &r.FolioID, IdempotencyKey: "tender-" + r.IdempotencyKey})
		if err != nil {
			return billing.TenderResult{}, err
		}
		return billing.TenderResult{Amount: amt, Ref: map[string]any{"voucherId": v.ID, "code": v.Code, "remaining": res.Voucher.RemainingQuantity}}, nil
	}
}

// VoucherDecision applies approval outcomes of issue / extend / adjust.
func (m *Module) VoucherDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.DocumentType {
	case VoucherIssueType.Code:
		v, err := m.Voucher(ctx, tx, d.DocumentID)
		if err != nil {
			return err
		}
		if v.Status != "pending" {
			return nil
		}
		status := "void"
		if d.Status == approval.StatusApproved {
			status = "active"
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET status = $2, void_reason = CASE WHEN $2 = 'void' THEN $3 END WHERE id = $1`,
			d.DocumentID, status, "issue "+d.Status+": "+d.Reason); err != nil {
			return err
		}
		after, _ := m.Voucher(ctx, tx, d.DocumentID)
		pid := d.PropertyID
		return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionStatusChange, EntityType: "commercial.voucher",
			EntityID: d.DocumentID.String(), EntityLabel: v.Code, PropertyID: &pid, Before: v, After: after, Reason: d.Reason})
	case VoucherExtendType.Code, VoucherAdjustType.Code:
		var vid uuid.UUID
		var typ, reason string
		var newExp *time.Time
		var qty *string
		err := tx.QueryRow(ctx, `UPDATE commercial.voucher_requests SET status = $2, decided_at = now() WHERE id = $1 AND status = 'pending'
			RETURNING voucher_id, request_type, new_expires_at, trim_scale(quantity)::text AS quantity, reason`, d.DocumentID,
			map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]).
			Scan(&vid, &typ, &newExp, &qty, &reason)
		if dbtx.IsNoRows(err) {
			return nil
		}
		if err != nil || d.Status != approval.StatusApproved {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM commercial.vouchers WHERE id = $1 FOR UPDATE`, vid); err != nil {
			return err
		}
		v, err := m.Voucher(ctx, tx, vid)
		if err != nil {
			return err
		}
		pid := d.PropertyID
		if typ == "extend" {
			if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET expires_at = $2, status = CASE WHEN status = 'expired' THEN status ELSE status END WHERE id = $1`, vid, newExp); err != nil {
				return err
			}
			rem, _ := decimal.NewFromString(v.RemainingQuantity)
			if err := m.ledger(ctx, tx, pid, vid, "extend", decimal.Zero, decimal.Zero, rem, ledgerOpts{reason: reason,
				details: map[string]any{"from": v.ExpiresAt, "to": newExp}}); err != nil {
				return err
			}
		} else {
			delta, _ := decimal.NewFromString(deref(qty))
			rem, _ := decimal.NewFromString(v.RemainingQuantity)
			orig, _ := decimal.NewFromString(v.OriginalQuantity)
			after := rem.Add(delta)
			if after.IsNegative() || after.GreaterThan(orig) {
				return errs.Conflict("invalid_adjustment", "adjustment would put the balance outside 0 – original quantity")
			}
			status := "partially_redeemed"
			if after.IsZero() {
				status = "redeemed"
			} else if after.Equal(orig) {
				status = "active"
			}
			if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET remaining_quantity = $2::numeric, status = $3 WHERE id = $1`, vid, after.String(), status); err != nil {
				return err
			}
			unit, _ := decimal.NewFromString(v.UnitValue)
			amt := delta.Mul(unit).Round(0)
			if err := m.ledger(ctx, tx, pid, vid, "adjustment", delta, amt.Neg(), after, ledgerOpts{reason: reason}); err != nil {
				return err
			}
			if !amt.IsZero() {
				vt, _ := loadVoucherType(ctx, tx, `id = $1`, v.VoucherTypeID)
				if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: pid, LiabilityType: liabilityType(vt), RefType: "commercial.voucher",
					RefID: vid, EntryType: "adjustment", Amount: amt, RevenueComponent: vt.RevenueComponent, Description: "Voucher " + v.Code + " adjusted: " + reason}); err != nil {
					return err
				}
			}
		}
		after, _ := m.Voucher(ctx, tx, vid)
		return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionUpdate, EntityType: "commercial.voucher",
			EntityID: vid.String(), EntityLabel: v.Code, PropertyID: &pid, Before: v, After: after, Reason: reason})
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ── HTTP ──────────────────────────────────────────────────────────────────

type SellInput struct {
	VoucherTypeID uuid.UUID    `json:"voucherTypeId"`
	CustomerID    *uuid.UUID   `json:"customerId,omitempty"`
	GuestName     string       `json:"guestName,omitempty"`
	Count         int          `json:"count,omitempty" doc:"Number of vouchers; default 1"`
	MemberPrice   bool         `json:"memberPrice,omitempty" doc:"Use the type's member price when no pricing rule applies"`
	Segment       string       `json:"segment,omitempty" doc:"Pricing segment (voucher_sale rules with item = voucher type code win over the type price)"`
	Channel       string       `json:"channel,omitempty" enum:"back_office,ops,member_app,website"`
	FolioID       *uuid.UUID   `json:"folioId,omitempty" doc:"Add the sale to an existing open folio"`
	Payment       *SalePayment `json:"payment,omitempty" doc:"Pay immediately (e.g. at reception)"`
}

type SalePayment struct {
	MethodType string         `json:"methodType"`
	Amount     string         `json:"amount,omitempty" doc:"Default: the sale total"`
	Reference  string         `json:"reference,omitempty"`
	Tender     map[string]any `json:"tender,omitempty"`
}

// SaleResult is a voucher sale.
type SaleResult struct {
	Vouchers []Voucher           `json:"vouchers"`
	Folio    billing.FolioDetail `json:"folio"`
}

type IssueInput struct {
	VoucherTypeID uuid.UUID  `json:"voucherTypeId"`
	CustomerID    *uuid.UUID `json:"customerId,omitempty"`
	Reason        string     `json:"reason"`
}

type RedeemInput struct {
	Code         string     `json:"code,omitempty"`
	VoucherID    *uuid.UUID `json:"voucherId,omitempty"`
	Quantity     string     `json:"quantity,omitempty" doc:"Units for quota vouchers, rupiah for value vouchers; default 1"`
	ServiceType  string     `json:"serviceType,omitempty"`
	ResourceType string     `json:"resourceType,omitempty"`
	ItemRef      string     `json:"itemRef,omitempty"`
	OutletID     *uuid.UUID `json:"outletId,omitempty"`
	CustomerID   *uuid.UUID `json:"customerId,omitempty"`
	Terminal     string     `json:"terminal,omitempty"`
	Reference    string     `json:"reference,omitempty"`
	BaseAmount   string     `json:"baseAmount,omitempty" doc:"Promo vouchers: the amount the discount applies to"`
}

type TransferInput struct {
	CustomerID uuid.UUID `json:"customerId"`
	Reason     string    `json:"reason,omitempty"`
}

type ExtendInput struct {
	ExpiresAt time.Time `json:"expiresAt"`
	Reason    string    `json:"reason"`
}

type AdjustInput struct {
	Quantity string `json:"quantity" doc:"Signed change of the remaining quantity"`
	Reason   string `json:"reason"`
}

type VoidInput struct {
	Reason string `json:"reason"`
}

// ChangeRequest is a pending Extend / Adjust request.
type ChangeRequest struct {
	ID          uuid.UUID  `json:"id"`
	VoucherID   uuid.UUID  `json:"voucherId"`
	RequestType string     `json:"requestType"`
	Status      string     `json:"status"`
	ApprovalID  *uuid.UUID `json:"approvalId"`
}

// Balance is a voucher balance check (FR-VCH-04).
type Balance struct {
	Code      string     `json:"code"`
	TypeName  string     `json:"typeName"`
	Kind      string     `json:"kind"`
	Unit      string     `json:"unit"`
	Status    string     `json:"status"`
	Remaining string     `json:"remaining"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// PrepaidBalance is a customer's prepaid balance per voucher type.
type PrepaidBalance struct {
	CustomerID   uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName string     `json:"customerName" db:"customer_name"`
	TypeCode     string     `json:"typeCode" db:"type_code"`
	TypeName     string     `json:"typeName" db:"type_name"`
	Category     string     `json:"category" db:"category"`
	Unit         string     `json:"unit" db:"unit"`
	Remaining    string     `json:"remaining" db:"remaining"`
	Vouchers     int        `json:"vouchers" db:"vouchers"`
	NextExpiry   *time.Time `json:"nextExpiry" db:"next_expiry"`
}

// PublicCheck is the anonymous code check (rate limited, no customer data).
type PublicCheck struct {
	Code       string    `json:"code"`
	PropertyID uuid.UUID `json:"propertyId"`
}

var publicCheckLimiter = &handle.Limiter{N: 10, Period: time.Minute}

func (m *Module) registerVouchers(reg *route.Registry, eng *resource.Engine) {
	eng.Register(reg, VoucherTypes)
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag = "commercial", "Voucher & Prepaid"
		if rt.Auth == route.AuthRequired {
			rt.Scope = route.ScopeProperty
		}
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/vouchers", Summary: "List vouchers", Permission: "commercial.voucher.view",
		Response: Voucher{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[customerId]"}, {Name: "filter[category]"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Voucher], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Voucher](tx.Query(ctx, voucherSelect+` WHERE v.property_id = $1 AND ($2 = '' OR v.status = ANY(string_to_array($2, ',')))
				AND ($3 = '' OR v.customer_id::text = $3) AND ($4 = '' OR t.category = $4) AND ($5 = '' OR v.code ILIKE '%' || $5 || '%' OR c.name ILIKE '%' || $5 || '%')
				ORDER BY v.issued_at DESC LIMIT $6`, handle.Property(ctx), lp.Filters["status"], lp.Filters["customerId"], lp.Filters["category"], lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/vouchers/{id}", Summary: "View voucher", Permission: "commercial.voucher.view",
		Response: Voucher{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Voucher, error) {
			vid, err := handle.ID(r)
			if err != nil {
				return Voucher{}, err
			}
			return m.Voucher(ctx, tx, vid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/vouchers/{id}/ledger", Summary: "Voucher usage history", Permission: "commercial.voucher.view",
		Response: LedgerEntry{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LedgerEntry], error) {
			vid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[LedgerEntry]{}, err
			}
			return handle.Page(handle.List[LedgerEntry](tx.Query(ctx, ledgerSelect+` WHERE l.voucher_id = $1 ORDER BY l.created_at, l.id`, vid)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/voucher-ledger", Summary: "Redemption history across vouchers", Permission: "commercial.voucher.view",
		Response: LedgerEntry{}, List: true, Query: []route.Param{{Name: "filter[entryType]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LedgerEntry], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LedgerEntry](tx.Query(ctx, ledgerSelect+` WHERE l.property_id = $1 AND ($2 = '' OR l.entry_type = $2)
				AND ($3 = '' OR v.customer_id::text = $3) ORDER BY l.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["entryType"], lp.Filters["customerId"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/vouchers/{code}/balance", Summary: "Check voucher balance by code", Permission: "commercial.voucher.view",
		Response: Balance{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Balance, error) {
			v, err := m.VoucherByCode(ctx, tx, handle.Property(ctx), chiParam(r, "code"))
			if err != nil {
				return Balance{}, err
			}
			return Balance{Code: v.Code, TypeName: v.TypeName, Kind: v.Kind, Unit: v.Unit, Status: v.Status, Remaining: v.RemainingQuantity, ExpiresAt: v.ExpiresAt}, nil
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/prepaid-balances", Summary: "Prepaid balances per customer and voucher type",
		Permission: "commercial.voucher.view", Response: PrepaidBalance{}, List: true, Query: []route.Param{{Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PrepaidBalance], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[PrepaidBalance](tx.Query(ctx, `SELECT v.customer_id, c.name AS customer_name, t.code AS type_code, t.name AS type_name,
				t.category, t.unit, trim_scale(sum(v.remaining_quantity))::text AS remaining, count(*)::int AS vouchers, min(v.expires_at) AS next_expiry
				FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id JOIN reporting.customer_directory c ON c.id = v.customer_id
				WHERE v.property_id = $1 AND t.prepaid AND v.status IN ('active', 'partially_redeemed') AND ($2 = '' OR v.customer_id::text = $2)
				GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 2, 4`, handle.Property(ctx), lp.Filters["customerId"])))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/vouchers:sell", Summary: "Sell voucher / package (deferred revenue)",
		Permission: "commercial.voucher.sell", Request: SellInput{}, Response: SaleResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SellInput) (SaleResult, error) {
			return m.Sell(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/vouchers:issue", Summary: "Issue complimentary / promo voucher (approval)",
		Permission: "commercial.voucher.issue", Request: IssueInput{}, Response: Voucher{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in IssueInput) (Voucher, error) {
			if err := handle.Required("reason", in.Reason); err != nil {
				return Voucher{}, err
			}
			pid := handle.Property(ctx)
			v, err := m.Issue(ctx, tx, IssueRequest{PropertyID: pid, TypeID: &in.VoucherTypeID, CustomerID: in.CustomerID, Via: "issue", Status: "pending", Notes: in.Reason})
			if err != nil {
				return v, err
			}
			if _, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: VoucherIssueType.Code, DocumentID: v.ID, DocumentRef: v.Code,
				Title: "Issue " + v.TypeName + " " + v.Code, PropertyID: pid, Attributes: map[string]any{"value": v.OriginalQuantity}}); err != nil {
				return v, err
			}
			return m.Voucher(ctx, tx, v.ID)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/vouchers:redeem", Summary: "Redeem voucher by code (idempotent per Idempotency-Key)",
		Permission: "commercial.voucher.redeem", Request: RedeemInput{}, Response: RedeemResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RedeemInput) (RedeemResult, error) {
			return m.redeemInput(ctx, tx, in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/prepaid-balances:redeem", Summary: "Use a customer's prepaid balance (oldest expiry first)",
		Permission: "commercial.voucher.redeem", Request: RedeemInput{}, Response: RedeemResult{}, List: true, Status: http.StatusOK,
		Query: []route.Param{{Name: "category", Required: true}},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RedeemInput) (httpx.Page[RedeemResult], error) {
			qty, err := handle.Decimal("quantity", in.Quantity, decimal.NewFromInt(1))
			if err != nil {
				return httpx.Page[RedeemResult]{}, err
			}
			return handle.Page(m.RedeemBalance(ctx, tx, RedeemRequest{PropertyID: handle.Property(ctx), Quantity: qty, ServiceType: in.ServiceType,
				ResourceType: in.ResourceType, ItemRef: in.ItemRef, OutletID: in.OutletID, CustomerID: in.CustomerID, Terminal: in.Terminal,
				Reference: in.Reference, IdempotencyKey: r.Header.Get("Idempotency-Key")}, r.URL.Query().Get("category")))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/vouchers/{id}:transfer", Summary: "Transfer voucher to another customer",
		Permission: "commercial.voucher.transfer", Request: TransferInput{}, Response: Voucher{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TransferInput) (Voucher, error) {
			vid, err := handle.ID(r)
			if err != nil {
				return Voucher{}, err
			}
			return m.Transfer(ctx, tx, vid, in.CustomerID, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/vouchers/{id}:extend", Summary: "Request expiry extension (approval)",
		Permission: "commercial.voucher.extend", Request: ExtendInput{}, Response: ChangeRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ExtendInput) (ChangeRequest, error) {
			vid, err := handle.ID(r)
			if err != nil {
				return ChangeRequest{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return ChangeRequest{}, err
			}
			return m.request(ctx, tx, vid, "extend", &in.ExpiresAt, nil, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/vouchers/{id}:adjust", Summary: "Request balance adjustment (approval)",
		Permission: "commercial.voucher.adjust", Request: AdjustInput{}, Response: ChangeRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AdjustInput) (ChangeRequest, error) {
			vid, err := handle.ID(r)
			if err != nil {
				return ChangeRequest{}, err
			}
			q, err := handle.Decimal("quantity", in.Quantity, decimal.Zero)
			if err != nil || q.IsZero() {
				return ChangeRequest{}, handle.Invalid("quantity", "invalid_quantity", "quantity must be a non-zero decimal")
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return ChangeRequest{}, err
			}
			return m.request(ctx, tx, vid, "adjust", nil, &q, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/vouchers/{id}:void", Summary: "Void voucher (liability reversed)",
		Permission: "commercial.voucher.void", Request: VoidInput{}, Response: Voucher{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in VoidInput) (Voucher, error) {
			vid, err := handle.ID(r)
			if err != nil {
				return Voucher{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return Voucher{}, err
			}
			return m.Void(ctx, tx, vid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/vouchers:check", Summary: "Check a voucher code (website; rate limited)", Auth: route.AuthPublic,
		Request: PublicCheck{}, Response: Balance{}, Status: http.StatusOK, NoAudit: "read-only check",
		Handler: publicCheckLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var in PublicCheck
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{in.PropertyID}})
			var out Balance
			err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				v, err := m.VoucherByCode(ctx, tx, in.PropertyID, in.Code)
				if err != nil {
					return err
				}
				out = Balance{Code: v.Code, TypeName: v.TypeName, Kind: v.Kind, Unit: v.Unit, Status: v.Status, Remaining: v.RemainingQuantity, ExpiresAt: v.ExpiresAt}
				return nil
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
}

func (m *Module) redeemInput(ctx context.Context, tx pgx.Tx, in RedeemInput, key string) (RedeemResult, error) {
	qty, err := handle.Decimal("quantity", in.Quantity, decimal.NewFromInt(1))
	if err != nil {
		return RedeemResult{}, err
	}
	base, err := handle.Decimal("baseAmount", in.BaseAmount, decimal.Zero)
	if err != nil {
		return RedeemResult{}, err
	}
	if in.Code == "" && in.VoucherID == nil {
		return RedeemResult{}, handle.Invalid("code", "required", "voucher code is required")
	}
	return m.Redeem(ctx, tx, RedeemRequest{PropertyID: handle.Property(ctx), VoucherID: in.VoucherID, Code: in.Code, Quantity: qty,
		ServiceType: in.ServiceType, ResourceType: in.ResourceType, ItemRef: in.ItemRef, OutletID: in.OutletID, CustomerID: in.CustomerID,
		Terminal: in.Terminal, Reference: in.Reference, IdempotencyKey: key, BaseAmount: base})
}

// Sell sells vouchers: walk-in (or given) folio, a liability charge per
// voucher (revenue component voucher_deferred) and optional payment.
func (m *Module) Sell(ctx context.Context, tx pgx.Tx, property uuid.UUID, in SellInput, key string) (SaleResult, error) {
	vt, err := loadVoucherType(ctx, tx, `id = $1 AND property_id = $2`, in.VoucherTypeID, property)
	if err != nil {
		return SaleResult{}, err
	}
	if vt.Kind == "promo" {
		return SaleResult{}, errs.Conflict("promo_not_for_sale", "promo vouchers are issued, not sold")
	}
	if in.Count <= 0 {
		in.Count = 1
	}
	if in.Count > 50 {
		return SaleResult{}, handle.Invalid("count", "too_many", "sell at most 50 vouchers at once")
	}
	price, _ := decimal.NewFromString(vt.Price)
	if in.MemberPrice && vt.MemberPrice != nil {
		price, _ = decimal.NewFromString(*vt.MemberPrice)
	}
	var snapshot *uuid.UUID
	preq := commercial.PriceRequest{ServiceType: "voucher_sale", ItemRef: vt.Code, Segment: in.Segment, Start: clock.Now()}
	if pr, err := (commercial.Pricer{}).Resolve(ctx, tx, property, preq); err == nil {
		if _, err := (commercial.Pricer{}).Snapshot(ctx, tx, property, preq, &pr); err != nil {
			return SaleResult{}, err
		}
		price, snapshot = pr.Total(), pr.SnapshotID
	} else if !errs.Is(err, errs.KindValidation) {
		return SaleResult{}, err
	}
	channel := in.Channel
	if channel == "" {
		channel = "back_office"
	}
	var folioID uuid.UUID
	if in.FolioID != nil {
		folioID = *in.FolioID
	} else {
		f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: in.CustomerID, HolderName: in.GuestName, SourceType: "voucher_sale"}, BusinessLine: billing.LineVoucher})
		if err != nil {
			return SaleResult{}, err
		}
		folioID = f.ID
	}
	out := SaleResult{Vouchers: []Voucher{}}
	total := decimal.Zero
	for i := 0; i < in.Count; i++ {
		v, err := m.Issue(ctx, tx, IssueRequest{PropertyID: property, TypeID: &vt.ID, CustomerID: in.CustomerID, Via: "sale", PricePaid: price,
			FolioID: &folioID, SourceType: "billing.folio", SourceID: &folioID})
		if err != nil {
			return out, err
		}
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folioID, ReferenceType: "commercial.voucher", ReferenceID: &v.ID, Description: vt.Name + " " + v.Code, Net: price, Liability: true, SnapshotID: snapshot}, BusinessLine: billing.LineVoucher, RevenueComponent: "voucher_deferred"}); err != nil {
			return out, err
		}
		out.Vouchers = append(out.Vouchers, v)
		total = total.Add(price)
	}
	if in.Payment != nil && total.IsPositive() {
		amt, err := handle.Decimal("payment.amount", in.Payment.Amount, total)
		if err != nil {
			return out, err
		}
		pkey := ""
		if key != "" {
			pkey = key + "-payment"
		}
		if _, err := m.Billing.TakeTender(ctx, tx, billing.TenderPaymentInput{PaymentInput: billing.PaymentInput{FolioID: &folioID, MethodType: in.Payment.MethodType, Amount: amt, Reference: in.Payment.Reference, Channel: channel}, Tender: in.Payment.Tender, IdempotencyKey: pkey}); err != nil {
			return out, err
		}
		f, err := billing.GetFolio(ctx, tx, folioID)
		if err != nil {
			return out, err
		}
		if bal, _ := decimal.NewFromString(f.Balance); bal.IsZero() && in.FolioID == nil {
			if err := m.Billing.CloseFolio(ctx, tx, folioID); err != nil {
				return out, err
			}
		}
	}
	out.Folio, err = billing.GetFolio(ctx, tx, folioID)
	return out, err
}

// Transfer moves a transferable voucher to another customer (FR-VCH-06).
func (m *Module) Transfer(ctx context.Context, tx pgx.Tx, vid, to uuid.UUID, reason string) (Voucher, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM commercial.vouchers WHERE id = $1 FOR UPDATE`, vid); err != nil {
		return Voucher{}, err
	}
	v, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return v, err
	}
	if !v.Transferable {
		return v, errs.Conflict("not_transferable", "voucher type "+v.TypeName+" is not transferable")
	}
	var vp uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.vouchers WHERE id = $1`, vid).Scan(&vp); err != nil {
		return v, err
	}
	if pol, err := m.voucherPolicy(ctx, tx, vp); err != nil {
		return v, err
	} else if !pol.TransferAllowed {
		return v, errs.Conflict("transfer_disabled", "voucher transfer is disabled by the Voucher Policies")
	}
	if v.Status != "active" && v.Status != "partially_redeemed" {
		return v, errs.Conflict("voucher_"+v.Status, "voucher is "+v.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET customer_id = $2, updated_by = $3 WHERE id = $1`, vid, to, actorID(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return v, errs.Validation("invalid_customer", "customer not found")
		}
		return v, err
	}
	rem, _ := decimal.NewFromString(v.RemainingQuantity)
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.vouchers WHERE id = $1`, vid).Scan(&pid); err != nil {
		return v, err
	}
	if err := m.ledger(ctx, tx, pid, vid, "transfer", decimal.Zero, decimal.Zero, rem, ledgerOpts{reason: reason,
		details: map[string]any{"from": v.CustomerID, "to": to}}); err != nil {
		return v, err
	}
	after, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "transfer", EntityType: "commercial.voucher", EntityID: vid.String(),
		EntityLabel: v.Code, PropertyID: &pid, Before: v, After: after, Reason: reason})
}

// Void cancels a voucher; any remaining liability is reversed (refund case).
func (m *Module) Void(ctx context.Context, tx pgx.Tx, vid uuid.UUID, reason string) (Voucher, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM commercial.vouchers WHERE id = $1 FOR UPDATE`, vid); err != nil {
		return Voucher{}, err
	}
	v, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return v, err
	}
	if v.Status == "void" || v.Status == "expired" || v.Status == "redeemed" {
		return v, errs.Conflict("voucher_"+v.Status, "a "+v.Status+" voucher cannot be voided")
	}
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.vouchers WHERE id = $1`, vid).Scan(&pid); err != nil {
		return v, err
	}
	rem, _ := decimal.NewFromString(v.RemainingQuantity)
	if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET status = 'void', remaining_quantity = 0, voided_at = now(), void_reason = $2 WHERE id = $1`, vid, reason); err != nil {
		return v, err
	}
	liab, err := m.liabilityOf(ctx, tx, vid)
	if err != nil {
		return v, err
	}
	if err := m.ledger(ctx, tx, pid, vid, "void", rem.Neg(), decimal.Zero, decimal.Zero, ledgerOpts{reason: reason}); err != nil {
		return v, err
	}
	if liab.IsPositive() {
		vt, _ := loadVoucherType(ctx, tx, `id = $1`, v.VoucherTypeID)
		if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: pid, LiabilityType: liabilityType(vt), RefType: "commercial.voucher",
			RefID: vid, EntryType: "reversal", Amount: liab, RevenueComponent: "voucher_deferred", Description: "Voucher " + v.Code + " voided: " + reason,
			IdempotencyKey: "voucher-void-" + vid.String()}); err != nil {
			return v, err
		}
	}
	after, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionVoid, EntityType: "commercial.voucher", EntityID: vid.String(),
		EntityLabel: v.Code, PropertyID: &pid, Before: v, After: after, Reason: reason})
}

func (m *Module) request(ctx context.Context, tx pgx.Tx, vid uuid.UUID, typ string, exp *time.Time, qty *decimal.Decimal, reason string) (ChangeRequest, error) {
	v, err := m.Voucher(ctx, tx, vid)
	if err != nil {
		return ChangeRequest{}, err
	}
	if v.Status == "void" {
		return ChangeRequest{}, errs.Conflict("voucher_void", "voucher is void")
	}
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.vouchers WHERE id = $1`, vid).Scan(&pid); err != nil {
		return ChangeRequest{}, err
	}
	if typ == "extend" && exp != nil {
		pol, err := m.voucherPolicy(ctx, tx, pid)
		if err != nil {
			return ChangeRequest{}, err
		}
		base := time.Now()
		if v.ExpiresAt != nil && v.ExpiresAt.After(base) {
			base = *v.ExpiresAt
		}
		if exp.After(base.AddDate(0, pol.MaxExtensionMonths, 1)) {
			return ChangeRequest{}, handle.Invalid("expiresAt", "extension_too_long", "the Voucher Policies allow at most "+strconv.Itoa(pol.MaxExtensionMonths)+" months of extension")
		}
	}
	rid := id.New()
	var q *string
	if qty != nil {
		s := qty.String()
		q = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.voucher_requests (id, property_id, voucher_id, request_type, new_expires_at, quantity, reason, created_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8)`, rid, pid, vid, typ, exp, q, reason, actorID(ctx)); err != nil {
		return ChangeRequest{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionCreate, EntityType: "commercial.voucher_request",
		EntityID: rid.String(), EntityLabel: typ + " " + v.Code, PropertyID: &pid, Reason: reason,
		After: map[string]any{"voucherId": vid, "requestType": typ, "expiresAt": exp, "quantity": q}}); err != nil {
		return ChangeRequest{}, err
	}
	dt, title, attrs := VoucherExtendType.Code, "Extend "+v.Code, map[string]any{}
	if typ == "adjust" {
		dt, title = VoucherAdjustType.Code, "Adjust "+v.Code+" by "+deref(q)
		attrs["value"] = deref(q)
	} else if v.ExpiresAt != nil {
		attrs["days"] = int(exp.Sub(*v.ExpiresAt).Hours() / 24)
	}
	aid, status, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: dt, DocumentID: rid, DocumentRef: v.Code, Title: title,
		PropertyID: pid, Attributes: attrs})
	if err != nil {
		return ChangeRequest{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.voucher_requests SET approval_id = $2 WHERE id = $1`, rid, aid); err != nil {
		return ChangeRequest{}, err
	}
	st := "pending"
	if status == approval.StatusApproved {
		st = "approved"
	}
	return ChangeRequest{ID: rid, VoucherID: vid, RequestType: typ, Status: st, ApprovalID: &aid}, nil
}
