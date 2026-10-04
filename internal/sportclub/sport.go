package sportclub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/reservation"
)

// Facility is the runtime view of a facility.
type Facility struct {
	ID           uuid.UUID      `db:"id"`
	PropertyID   uuid.UUID      `db:"property_id"`
	Code         string         `db:"code"`
	Name         string         `db:"name"`
	FacilityType *string        `db:"facility_type"`
	Capacity     *int           `db:"capacity"`
	UsageMode    string         `db:"usage_mode"`
	MinAge       *int           `db:"min_age"`
	MaxAge       *int           `db:"max_age"`
	OpeningHours map[string]any `db:"opening_hours"`
	PriceItem    *string        `db:"price_item"`
	ResourceID   *uuid.UUID     `db:"resource_id"`
	Status       string         `db:"status"`
	Attributes   map[string]any `db:"attributes"`
}

func (m *Module) facility(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (Facility, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, code, name, facility_type, capacity, usage_mode, min_age, max_age, opening_hours, price_item,
		resource_id, status, attributes FROM sportclub.facilities WHERE id = $1`, fid)
	return handle.One[Facility](rows, err, "facility")
}

func (f Facility) priceItem() string {
	if f.PriceItem != nil && *f.PriceItem != "" {
		return *f.PriceItem
	}
	return f.Code
}

func (f Facility) kind() string { return deref(f.FacilityType) }

// openAt checks the opening hours of the facility (per day type) at local t.
func (m *Module) openAt(ctx context.Context, q dbtx.Querier, f Facility, pol Policy, t time.Time) (bool, string, error) {
	dk, err := dayKind(ctx, q, f.PropertyID, t)
	if err != nil {
		return false, "", err
	}
	hours, ok := pol.OpeningHours[dk]
	if v, ok2 := f.OpeningHours[dk].([]any); ok2 && len(v) == 2 {
		hours, ok = [2]string{str(v[0]), str(v[1])}, true
	}
	if !ok {
		return true, "", nil
	}
	hm := t.Format("15:04")
	if hm < hours[0] || hm >= hours[1] {
		return false, fmt.Sprintf("%s is open %s–%s on %s", f.Name, hours[0], hours[1], dk), nil
	}
	return true, "", nil
}

func qr(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ── entry tickets (FR-SPT-04) ─────────────────────────────────────────────

// PaymentInput settles a sale immediately (reception).
type PaymentInput struct {
	MethodType string         `json:"methodType" enum:"cash,bank_transfer,qris,card,member_account,voucher_prepaid,payment_gateway"`
	Amount     string         `json:"amount,omitempty" doc:"Default: the total"`
	Reference  string         `json:"reference,omitempty"`
	Tender     map[string]any `json:"tender,omitempty"`
}

type GuestInput struct {
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

type EntryInput struct {
	FacilityID     uuid.UUID     `json:"facilityId"`
	EntryType      string        `json:"entryType" enum:"walk_in_guest,guest_of_member,child,family_package,member,voucher,staying_guest"`
	CustomerID     *uuid.UUID    `json:"customerId,omitempty"`
	Guest          *GuestInput   `json:"guest,omitempty"`
	HostCustomerID *uuid.UUID    `json:"hostCustomerId,omitempty" doc:"Guest of Member: the member who brings the guest"`
	BirthDate      string        `json:"birthDate,omitempty" doc:"Child Entry: YYYY-MM-DD when the customer has none"`
	Adults         int           `json:"adults,omitempty"`
	Children       int           `json:"children,omitempty"`
	VoucherCode    string        `json:"voucherCode,omitempty"`
	VisitDate      string        `json:"visitDate,omitempty" doc:"YYYY-MM-DD; default today"`
	Channel        string        `json:"channel,omitempty" enum:"ops,back_office,member_app,website"`
	Payment        *PaymentInput `json:"payment,omitempty"`
}

// Entry is an entry ticket.
type Entry struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	TicketNo       string     `json:"ticketNo" db:"ticket_no"`
	FacilityID     uuid.UUID  `json:"facilityId" db:"facility_id"`
	FacilityName   string     `json:"facilityName" db:"facility_name"`
	EntryType      string     `json:"entryType" db:"entry_type"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName   *string    `json:"customerName" db:"customer_name"`
	HostCustomerID *uuid.UUID `json:"hostCustomerId" db:"host_customer_id"`
	GuestName      *string    `json:"guestName" db:"guest_name"`
	Adults         int        `json:"adults" db:"adults"`
	Children       int        `json:"children" db:"children"`
	VisitDate      time.Time  `json:"visitDate" db:"visit_date"`
	Amount         string     `json:"amount" db:"amount"`
	FolioID        *uuid.UUID `json:"folioId" db:"folio_id"`
	VoucherID      *uuid.UUID `json:"voucherId" db:"voucher_id"`
	ReservationID  *uuid.UUID `json:"reservationId" db:"reservation_id"`
	QRToken        string     `json:"qrToken" db:"qr_token"`
	Status         string     `json:"status" db:"status" enum:"issued,used,cancelled,expired"`
	UsedAt         *time.Time `json:"usedAt" db:"used_at"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
}

const entrySelect = `SELECT e.id, e.ticket_no, e.facility_id, f.name AS facility_name, e.entry_type, e.customer_id, c.name AS customer_name, e.host_customer_id,
	e.guest_name, e.adults, e.children, e.visit_date, trim_scale(e.amount)::text AS amount, e.folio_id, e.voucher_id, e.reservation_id, e.qr_token,
	e.status, e.used_at, e.created_at
	FROM sportclub.entries e JOIN sportclub.facilities f ON f.id = e.facility_id LEFT JOIN reporting.customer_directory c ON c.id = e.customer_id`

func (m *Module) entry(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (Entry, error) {
	rows, err := q.Query(ctx, entrySelect+` WHERE e.id = $1`, eid)
	return handle.One[Entry](rows, err, "entry ticket")
}

var entrySegment = map[string]string{"walk_in_guest": "walk_in", "guest_of_member": "guest_of_member", "child": "child", "family_package": "family"}

// resolveCustomer finds or creates the customer of a walk-in.
func resolveCustomer(ctx context.Context, tx pgx.Tx, property uuid.UUID, cid *uuid.UUID, g *GuestInput) (*uuid.UUID, string, error) {
	if cid != nil {
		p, err := crm.GetCustomer(ctx, tx, *cid)
		if err != nil {
			return nil, "", err
		}
		return &p.ID, p.Name, nil
	}
	if g == nil || strings.TrimSpace(g.Name) == "" {
		return nil, "", nil
	}
	if g.Phone == "" && g.Email == "" {
		return nil, g.Name, nil
	}
	p, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: g.Name, Phone: g.Phone, Email: g.Email})
	if err != nil {
		return nil, "", err
	}
	return &p.ID, p.Name, nil
}

// EntryResult is the sale of an entry ticket.
type EntryResult struct {
	Entry Entry                `json:"entry"`
	Folio *billing.FolioDetail `json:"folio"`
}

// CreateEntry sells or issues an entry ticket with the right rate and rights.
func (m *Module) CreateEntry(ctx context.Context, tx pgx.Tx, property uuid.UUID, in EntryInput, key string) (EntryResult, error) {
	f, err := m.facility(ctx, tx, in.FacilityID)
	if err != nil {
		return EntryResult{}, err
	}
	if f.PropertyID != property || f.Status != "active" {
		return EntryResult{}, errs.Validation("facility_unavailable", "facility not available")
	}
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return EntryResult{}, err
	}
	now := localNow(ctx, tx)
	visit := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if in.VisitDate != "" {
		d, err := time.ParseInLocation("2006-01-02", in.VisitDate, now.Location())
		if err != nil {
			return EntryResult{}, handle.Invalid("visitDate", "invalid_date", "visitDate must be YYYY-MM-DD")
		}
		if d.Before(visit) {
			return EntryResult{}, handle.Invalid("visitDate", "in_the_past", "visit date cannot be in the past")
		}
		visit = d
	}
	at := now
	if !visit.Equal(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) {
		at = visit.Add(10 * time.Hour)
	}
	if in.Adults <= 0 && in.EntryType != "child" {
		in.Adults = 1
	}
	if in.Channel == "" {
		in.Channel = "ops"
	}
	customerID, customerName, err := resolveCustomer(ctx, tx, property, in.CustomerID, in.Guest)
	if err != nil {
		return EntryResult{}, err
	}
	segment := entrySegment[in.EntryType]
	quantity := in.Adults
	free := false
	var voucherID *uuid.UUID
	switch in.EntryType {
	case "walk_in_guest":
		quantity = in.Adults + in.Children
	case "guest_of_member":
		if in.HostCustomerID == nil {
			return EntryResult{}, handle.Invalid("hostCustomerId", "required", "Guest of Member needs the member who brings the guest")
		}
		ok, err := membership.IsActive(ctx, tx, property, *in.HostCustomerID, "")
		if err != nil {
			return EntryResult{}, err
		}
		if !ok {
			return EntryResult{}, errs.Conflict("host_not_member", "the host is not an active member")
		}
		if pol.GuestOfMemberMustBePresent {
			var present bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sportclub.access_events WHERE customer_id = $1 AND result = 'granted'
				AND direction = 'in' AND occurred_at >= $2)`, *in.HostCustomerID, visit).Scan(&present); err != nil {
				return EntryResult{}, err
			}
			if !present {
				return EntryResult{}, errs.Conflict("host_not_present", "Guest Policy: the member must check in before a Guest of Member entry")
			}
		}
		var today int
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(adults + children), 0) FROM sportclub.entries WHERE host_customer_id = $1 AND visit_date = $2
			AND status <> 'cancelled'`, *in.HostCustomerID, visit).Scan(&today); err != nil {
			return EntryResult{}, err
		}
		if pol.MaxGuestsPerMember > 0 && today+in.Adults > pol.MaxGuestsPerMember {
			return EntryResult{}, errs.Conflict("guest_limit", fmt.Sprintf("Guest Policy: at most %d guests per member per day", pol.MaxGuestsPerMember))
		}
	case "child":
		quantity = max(in.Children, 1)
		in.Children, in.Adults = quantity, 0
		var bd *time.Time
		if customerID != nil {
			p, err := crm.GetCustomer(ctx, tx, *customerID)
			if err != nil {
				return EntryResult{}, err
			}
			bd = p.BirthDate
		}
		if in.BirthDate != "" {
			d, err := time.Parse("2006-01-02", in.BirthDate)
			if err != nil {
				return EntryResult{}, handle.Invalid("birthDate", "invalid_date", "birthDate must be YYYY-MM-DD")
			}
			bd = &d
		}
		if bd == nil {
			return EntryResult{}, handle.Invalid("birthDate", "required", "the child's date of birth is required")
		}
		age := crm.Customer{BirthDate: bd}
		a, _ := crm.AgeOn(age, visit)
		if a > pol.ChildMaxAge {
			return EntryResult{}, errs.Validation("child_too_old", fmt.Sprintf("Child Entry is for children under %d (this child is %d); use the adult rate", pol.ChildMaxAge+1, a))
		}
	case "family_package":
		if in.Adults > 2 || in.Children > 3 {
			return EntryResult{}, handle.Invalid("children", "family_composition", "Family Package covers 2 adults and 3 children")
		}
		quantity = 1
	case "member":
		if customerID == nil {
			return EntryResult{}, handle.Invalid("customerId", "required", "Member Entry needs the member")
		}
		allowed, isFree, err := membership.FacilityAccess(ctx, tx, property, *customerID, f.kind())
		if err != nil {
			return EntryResult{}, err
		}
		if !allowed {
			return EntryResult{}, errs.Conflict("no_entitlement", "the membership does not include "+f.Name)
		}
		segment, free = "member", isFree
	case "staying_guest":
		if customerID == nil {
			return EntryResult{}, handle.Invalid("customerId", "required", "staying guest is required")
		}
		stay, err := m.Res.ActiveStay(ctx, tx, property, *customerID, now)
		if err != nil {
			return EntryResult{}, err
		}
		if stay == nil {
			return EntryResult{}, errs.Conflict("not_staying", "the guest has no checked-in stay")
		}
		access, _ := stay.Attributes["facilityAccess"].([]any)
		ok := false
		for _, a := range access {
			ok = ok || str(a) == f.kind() || str(a) == "*"
		}
		if !ok {
			return EntryResult{}, errs.Conflict("no_entitlement", "the stay rate plan does not include "+f.Name)
		}
		free = true
	case "voucher":
		if in.VoucherCode == "" {
			return EntryResult{}, handle.Invalid("voucherCode", "required", "voucher code is required")
		}
		vk := ""
		if key != "" {
			vk = "entry-" + key
		}
		res, err := m.Vouchers.Redeem(ctx, tx, commercial.RedeemRequest{PropertyID: property, Code: in.VoucherCode, Quantity: decimal.NewFromInt(int64(in.Adults + in.Children)),
			ServiceType: "facility_entry", ItemRef: f.priceItem(), CustomerID: customerID, Terminal: in.Channel, SourceType: "sportclub.entry", IdempotencyKey: vk})
		if err != nil {
			return EntryResult{}, err
		}
		voucherID, free = &res.Voucher.ID, true
	default:
		return EntryResult{}, handle.Invalid("entryType", "invalid_entry_type", "unknown entry type")
	}
	// capacity (pool, gym …)
	var resID *uuid.UUID
	if f.ResourceID != nil {
		r, err := m.Res.Book(ctx, tx, property, reservation.BookRequest{BusinessLine: "sportclub", Channel: in.Channel, CustomerID: customerID,
			GuestName: guestName(in.Guest), Confirm: true, SourceType: "sportclub.entry",
			Lines: []reservation.LineRequest{{ResourceID: *f.ResourceID, Start: visit, End: visit.AddDate(0, 0, 1), Quantity: max(in.Adults+in.Children, 1),
				Description: "Entry " + f.Name}}})
		if err != nil {
			return EntryResult{}, err
		}
		resID = &r.ID
	}
	amount := decimal.Zero
	var snapshot *uuid.UUID
	var folioID *uuid.UUID
	if !free {
		pr, err := commercial.Pricer{}.Price(ctx, tx, property, commercial.PriceRequest{ServiceType: "facility_entry", ItemRef: f.priceItem(),
			Segment: segment, Start: at, Quantity: quantity, Channel: in.Channel})
		if err != nil {
			return EntryResult{}, err
		}
		amount, snapshot = pr.Total(), pr.SnapshotID
		fo, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: customerID, HolderName: customerName, SourceType: "sport_entry"}, BusinessLine: billing.LineSport})
		if err != nil {
			return EntryResult{}, err
		}
		folioID = &fo.ID
	}
	no, err := numbering.Next(ctx, tx, property, "ENT", now)
	if err != nil {
		return EntryResult{}, err
	}
	eid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.entries (id, property_id, ticket_no, facility_id, entry_type, customer_id, host_customer_id, guest_name,
		adults, children, visit_date, amount, pricing_snapshot_id, folio_id, voucher_id, reservation_id, qr_token, channel, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::numeric,$13,$14,$15,$16,$17,$18,$19)`,
		eid, property, no, f.ID, in.EntryType, customerID, in.HostCustomerID, nzs(customerName), in.Adults, in.Children, visit, amount.String(),
		snapshot, folioID, voucherID, resID, qr("oce_"), in.Channel, actor(ctx)); err != nil {
		return EntryResult{}, err
	}
	if resID != nil {
		if err := m.Res.SetSource(ctx, tx, *resID, eid); err != nil {
			return EntryResult{}, err
		}
	}
	out := EntryResult{}
	if folioID != nil {
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *folioID, ReferenceType: "sportclub.entry", ReferenceID: &eid, Description: "Entry " + f.Name + " (" + strings.ReplaceAll(in.EntryType, "_", " ") + ")", Quantity: decimal.NewFromInt(int64(quantity)), Net: amount, SnapshotID: snapshot}, BusinessLine: billing.LineSport, RevenueComponent: "sport_entry"}); err != nil {
			return EntryResult{}, err
		}
		if err := m.settle(ctx, tx, *folioID, in.Payment, key); err != nil {
			return EntryResult{}, err
		}
		d, err := billing.GetFolio(ctx, tx, *folioID)
		if err != nil {
			return out, err
		}
		out.Folio = &d
	}
	e, err := m.entry(ctx, tx, eid)
	if err != nil {
		return out, err
	}
	out.Entry = e
	return out, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionCreate, EntityType: "sportclub.entry",
		EntityID: eid.String(), EntityLabel: no + " · " + f.Name, PropertyID: &property, After: e})
}

func guestName(g *GuestInput) string {
	if g == nil {
		return ""
	}
	return g.Name
}

// settle pays a walk-in folio and closes it when fully paid.
func (m *Module) settle(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, p *PaymentInput, key string) error {
	if p == nil {
		return nil
	}
	f, err := billing.GetFolio(ctx, tx, folioID)
	if err != nil {
		return err
	}
	bal, _ := decimal.NewFromString(f.Balance)
	if !bal.IsPositive() {
		return nil
	}
	amt, err := handle.Decimal("payment.amount", p.Amount, bal)
	if err != nil {
		return err
	}
	pk := ""
	if key != "" {
		pk = key + "-pay"
	}
	if _, err := m.Billing.TakeTender(ctx, tx, billing.TenderPaymentInput{PaymentInput: billing.PaymentInput{FolioID: &folioID, MethodType: p.MethodType, Amount: amt, Reference: p.Reference}, Tender: p.Tender, IdempotencyKey: pk}); err != nil {
		return err
	}
	f, err = billing.GetFolio(ctx, tx, folioID)
	if err != nil {
		return err
	}
	if b, _ := decimal.NewFromString(f.Balance); b.IsZero() && f.SourceType == "sport_entry" {
		err = m.Billing.CloseFolio(ctx, tx, folioID)
	}
	return err
}

// CancelEntry cancels an unused ticket; paid amounts are refunded.
func (m *Module) CancelEntry(ctx context.Context, tx pgx.Tx, eid uuid.UUID, reason string) (Entry, error) {
	e, err := m.entry(ctx, tx, eid)
	if err != nil {
		return e, err
	}
	if e.Status != "issued" {
		return e, errs.Conflict("ticket_"+e.Status, "only unused tickets can be cancelled")
	}
	if e.ReservationID != nil {
		if _, err := m.Res.Cancel(ctx, tx, *e.ReservationID, "entry cancelled: "+reason, true); err != nil {
			return e, err
		}
	}
	if e.FolioID != nil {
		f, err := billing.GetFolio(ctx, tx, *e.FolioID)
		if err != nil {
			return e, err
		}
		if f.Status == "closed" {
			if err := m.Billing.ReopenFolio(ctx, tx, f.ID, "entry cancelled"); err != nil {
				return e, err
			}
		}
		if _, err := m.Billing.SettleCancellation(ctx, tx, *e.FolioID, decimal.Zero, billing.LineSport, reason); err != nil {
			return e, err
		}
	}
	if e.VoucherID != nil {
		qty := decimal.NewFromInt(int64(e.Adults + e.Children))
		if err := m.Vouchers.Restore(ctx, tx, *e.VoucherID, qty, "entry cancelled", "entry-cancel-"+eid.String()); err != nil {
			return e, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.entries SET status = 'cancelled', updated_by = $2 WHERE id = $1`, eid, actor(ctx)); err != nil {
		return e, err
	}
	after, err := m.entry(ctx, tx, eid)
	if err != nil {
		return after, err
	}
	var pid uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM sportclub.entries WHERE id = $1`, eid).Scan(&pid)
	return after, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionVoid, EntityType: "sportclub.entry",
		EntityID: eid.String(), EntityLabel: e.TicketNo, PropertyID: &pid, Before: e, After: after, Reason: reason})
}

// ── facility access (FR-SPT-05) ───────────────────────────────────────────

type AccessInput struct {
	Code       string    `json:"code" doc:"Scanned QR: member card, entry ticket or booking code"`
	FacilityID uuid.UUID `json:"facilityId"`
	Direction  string    `json:"direction,omitempty" enum:"in,out"`
	Terminal   string    `json:"terminal,omitempty"`
}

// AccessResult tells reception whether to let the person in.
type AccessResult struct {
	EventID        uuid.UUID           `json:"eventId"`
	Result         string              `json:"result" enum:"granted,denied"`
	Reason         string              `json:"reason,omitempty"`
	CredentialType string              `json:"credentialType" enum:"member_card,ticket,booking,stay,unknown"`
	CustomerID     *uuid.UUID          `json:"customerId"`
	CustomerName   string              `json:"customerName,omitempty"`
	Persons        int                 `json:"persons"`
	Memberships    []membership.Active `json:"memberships,omitempty"`
	Entry          *Entry              `json:"entry,omitempty"`
}

// ValidateAccess checks entitlement, validity, opening hours and age limits,
// then records the access event (granted or denied).
func (m *Module) ValidateAccess(ctx context.Context, tx pgx.Tx, property uuid.UUID, in AccessInput, offline bool) (AccessResult, error) {
	f, err := m.facility(ctx, tx, in.FacilityID)
	if err != nil {
		return AccessResult{}, err
	}
	if f.PropertyID != property {
		return AccessResult{}, errNotFound("facility")
	}
	if in.Direction == "" {
		in.Direction = "in"
	}
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return AccessResult{}, err
	}
	now := localNow(ctx, tx)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	code := strings.TrimSpace(in.Code)
	res := AccessResult{Result: "denied", CredentialType: "unknown", Persons: 1}
	var entryID, resID *uuid.UUID
	deny := func(reason string) { res.Result, res.Reason = "denied", reason }
	grant := func() { res.Result, res.Reason = "granted", "" }
	checkAge := func(cid uuid.UUID) (bool, error) {
		if f.MinAge == nil && f.MaxAge == nil {
			return true, nil
		}
		p, err := crm.GetCustomer(ctx, tx, cid)
		if err != nil {
			return false, err
		}
		age, ok := crm.AgeOn(p, today)
		if !ok {
			return true, nil
		}
		if (f.MinAge != nil && age < *f.MinAge) || (f.MaxAge != nil && age > *f.MaxAge) {
			deny(fmt.Sprintf("age %d is outside the limits of %s", age, f.Name))
			return false, nil
		}
		return true, nil
	}

	// 1. entry ticket
	var e Entry
	rows, err := tx.Query(ctx, entrySelect+` WHERE e.property_id = $1 AND (e.qr_token = $2 OR e.ticket_no = upper($2)) FOR UPDATE OF e`, property, code)
	e, err = handle.One[Entry](rows, err, "entry")
	switch {
	case err == nil:
		res.CredentialType, res.CustomerID, entryID = "ticket", e.CustomerID, &e.ID
		res.Persons = max(e.Adults+e.Children, 1)
		res.CustomerName = deref(e.CustomerName)
		if e.CustomerName == nil {
			res.CustomerName = deref(e.GuestName)
		}
		switch {
		case e.FacilityID != f.ID:
			deny("this ticket is for " + e.FacilityName)
		case !sameDay(e.VisitDate, today):
			deny("this ticket is valid on " + e.VisitDate.Format("2006-01-02"))
		case e.Status == "used" && in.Direction == "in":
			deny("this ticket has already been used")
		case e.Status != "issued" && e.Status != "used":
			deny("ticket is " + e.Status)
		default:
			grant()
			if e.Status == "issued" && in.Direction == "in" {
				if _, err := tx.Exec(ctx, `UPDATE sportclub.entries SET status = 'used', used_at = now() WHERE id = $1`, e.ID); err != nil {
					return res, err
				}
			}
		}
		res.Entry = &e
	case !errs.Is(err, errs.KindNotFound):
		return res, err
	default:
		// 2. member card
		card, cerr := membership.LookupCard(ctx, tx, property, code)
		switch {
		case cerr == nil:
			res.CredentialType, res.CustomerID, res.CustomerName, res.Memberships = "member_card", card.CustomerID, card.CustomerName, card.Memberships
			cust := uuid.Nil
			if card.CustomerID != nil {
				cust = *card.CustomerID
			}
			allowed, _, err := membership.FacilityAccess(ctx, tx, property, cust, f.kind())
			if err != nil {
				return res, err
			}
			switch {
			case card.Status != "active":
				deny("member card is " + card.Status)
			case !allowed:
				deny("no active membership with access to " + f.Name)
			default:
				if ok, err := checkAge(cust); err != nil {
					return res, err
				} else if ok {
					grant()
				}
			}
		case !errs.Is(cerr, errs.KindNotFound):
			return res, cerr
		default:
			// 3. booking or stay code
			r, err := m.Res.ByCode(ctx, tx, property, code)
			if err != nil {
				return res, err
			}
			if r == nil {
				deny("unknown code")
				break
			}
			resID, res.CustomerID = &r.ID, r.CustomerID
			res.CustomerName = deref(r.CustomerName)
			if res.CustomerName == "" {
				res.CustomerName = deref(r.GuestName)
			}
			switch r.BusinessLine {
			case "stay":
				res.CredentialType = "stay"
				access, _ := r.Attributes["facilityAccess"].([]any)
				ok := false
				for _, a := range access {
					ok = ok || str(a) == f.kind() || str(a) == "*"
				}
				switch {
				case r.Status != "checked_in":
					deny("the stay is not checked in")
				case !ok:
					deny("the stay rate plan does not include " + f.Name)
				default:
					grant()
				}
			default:
				res.CredentialType = "booking"
				fac := ""
				for _, l := range r.Lines {
					if v, ok := l.Attributes["facilityId"]; ok {
						fac = str(v)
					}
				}
				if fac == "" {
					resources, err := m.Res.LineResources(ctx, tx, r.ID)
					if err != nil {
						return res, err
					}
					if err := tx.QueryRow(ctx, `SELECT coalesce(facility_id::text, '') FROM sportclub.courts WHERE resource_id = ANY($1) LIMIT 1`, resources).
						Scan(&fac); err != nil && !dbtx.IsNoRows(err) {
						return res, err
					}
				}
				switch {
				case r.Status != reservation.StatusConfirmed && r.Status != reservation.StatusCheckedIn:
					deny("booking is " + r.Status)
				case fac != "" && fac != f.ID.String():
					deny("this booking is for another facility")
				case r.Start == nil || now.Before(r.Start.Add(-30*time.Minute)) || (r.End != nil && now.After(*r.End)):
					deny("the booking is not for now")
				default:
					grant()
					if r.Status == reservation.StatusConfirmed && in.Direction == "in" {
						if _, err := m.Res.CheckIn(ctx, tx, r.ID); err != nil {
							return res, err
						}
					}
				}
			}
		}
	}
	if res.Result == "granted" && in.Direction == "in" {
		if open, why, err := m.openAt(ctx, tx, f, pol, now); err != nil {
			return res, err
		} else if !open {
			deny(why)
		}
	}
	res.EventID = id.New()
	hint := code
	if len(hint) > 8 {
		hint = "…" + hint[len(hint)-6:]
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.access_events (id, property_id, facility_id, credential_type, code_hint, customer_id, entry_id, reservation_id,
		direction, result, reason, terminal, offline, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		res.EventID, property, f.ID, res.CredentialType, hint, res.CustomerID, entryID, resID, in.Direction, res.Result, nzs(res.Reason),
		nzs(in.Terminal), offline, actor(ctx)); err != nil {
		return res, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: "access_" + res.Result, EntityType: "sportclub.access_event",
		EntityID: res.EventID.String(), EntityLabel: f.Name + " · " + res.CredentialType, PropertyID: &property,
		After: map[string]any{"result": res.Result, "reason": res.Reason, "customerId": res.CustomerID, "direction": in.Direction}}); err != nil {
		return res, err
	}
	if res.Result == "granted" {
		if _, err := m.Events.Publish(ctx, tx, "sportclub.access_granted", "sportclub.facility", &f.ID, &property, map[string]any{
			"facilityId": f.ID, "facility": f.Name, "facilityType": f.kind(), "customerId": res.CustomerID, "credential": res.CredentialType,
			"direction": in.Direction, "persons": res.Persons}); err != nil {
			return res, err
		}
	}
	return res, nil
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// ── lockers (FR-SPT-07) ───────────────────────────────────────────────────

type LockerAssignInput struct {
	LockerID       uuid.UUID     `json:"lockerId"`
	CustomerID     *uuid.UUID    `json:"customerId,omitempty"`
	GuestName      string        `json:"guestName,omitempty"`
	AssignmentType string        `json:"assignmentType,omitempty" enum:"daily,rental"`
	EndAt          *time.Time    `json:"endAt,omitempty" doc:"Rental end; daily assignments end at closing"`
	EntryID        *uuid.UUID    `json:"entryId,omitempty"`
	Fee            string        `json:"fee,omitempty"`
	Payment        *PaymentInput `json:"payment,omitempty"`
}

// LockerAssignment is one assignment (Locker History).
type LockerAssignment struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	LockerID       uuid.UUID  `json:"lockerId" db:"locker_id"`
	LockerCode     string     `json:"lockerCode" db:"locker_code"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName   *string    `json:"customerName" db:"customer_name"`
	GuestName      *string    `json:"guestName" db:"guest_name"`
	AssignmentType string     `json:"assignmentType" db:"assignment_type"`
	StartAt        time.Time  `json:"startAt" db:"start_at"`
	EndAt          *time.Time `json:"endAt" db:"end_at"`
	ReturnedAt     *time.Time `json:"returnedAt" db:"returned_at"`
	Fee            string     `json:"fee" db:"fee"`
	Status         string     `json:"status" db:"status" enum:"active,returned,cancelled"`
}

const lockerAssignSelect = `SELECT a.id, a.locker_id, l.code AS locker_code, a.customer_id, c.name AS customer_name, a.guest_name, a.assignment_type,
	a.start_at, a.end_at, a.returned_at, trim_scale(a.fee)::text AS fee, a.status
	FROM sportclub.locker_assignments a JOIN sportclub.lockers l ON l.id = a.locker_id LEFT JOIN reporting.customer_directory c ON c.id = a.customer_id`

// AssignLocker assigns an available locker (one active assignment per locker).
func (m *Module) AssignLocker(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LockerAssignInput, key string) (LockerAssignment, error) {
	var status, code string
	if err := tx.QueryRow(ctx, `SELECT status, code FROM sportclub.lockers WHERE id = $1 AND property_id = $2 FOR UPDATE`, in.LockerID, property).Scan(&status, &code); err != nil {
		if dbtx.IsNoRows(err) {
			return LockerAssignment{}, errNotFound("locker")
		}
		return LockerAssignment{}, err
	}
	if status != "available" {
		return LockerAssignment{}, errs.Conflict("locker_"+status, "locker "+code+" is "+status)
	}
	if in.AssignmentType == "" {
		in.AssignmentType = "daily"
	}
	if in.CustomerID == nil && strings.TrimSpace(in.GuestName) == "" {
		return LockerAssignment{}, handle.Invalid("guestName", "required", "customer or guest name is required")
	}
	fee, err := handle.Decimal("fee", in.Fee, decimal.Zero)
	if err != nil {
		return LockerAssignment{}, err
	}
	aid := id.New()
	var folio *uuid.UUID
	if fee.IsPositive() {
		f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: in.CustomerID, HolderName: in.GuestName, SourceType: "locker", SourceID: &aid}, BusinessLine: billing.LineSport})
		if err != nil {
			return LockerAssignment{}, err
		}
		folio = &f.ID
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: f.ID, ReferenceType: "sportclub.locker_assignment", ReferenceID: &aid, Description: "Locker " + code + " (" + in.AssignmentType + ")", Net: fee}, BusinessLine: billing.LineSport, RevenueComponent: "locker"}); err != nil {
			return LockerAssignment{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.locker_assignments (id, property_id, locker_id, customer_id, guest_name, assignment_type, entry_id,
		end_at, fee, folio_id, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11)`,
		aid, property, in.LockerID, in.CustomerID, nzs(in.GuestName), in.AssignmentType, in.EntryID, in.EndAt, fee.String(), folio, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return LockerAssignment{}, errs.Conflict("locker_occupied", "locker "+code+" is already assigned")
		}
		return LockerAssignment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.lockers SET status = 'occupied' WHERE id = $1`, in.LockerID); err != nil {
		return LockerAssignment{}, err
	}
	if folio != nil {
		if err := m.settle(ctx, tx, *folio, in.Payment, key); err != nil {
			return LockerAssignment{}, err
		}
	}
	rows, err := tx.Query(ctx, lockerAssignSelect+` WHERE a.id = $1`, aid)
	a, err := handle.One[LockerAssignment](rows, err, "locker assignment")
	if err != nil {
		return a, err
	}
	return a, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionCreate, EntityType: "sportclub.locker_assignment",
		EntityID: aid.String(), EntityLabel: "Locker " + code, PropertyID: &property, After: a})
}

// ReturnLocker ends an assignment; the locker is Available again.
func (m *Module) ReturnLocker(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (LockerAssignment, error) {
	var lockerID, property uuid.UUID
	if err := tx.QueryRow(ctx, `UPDATE sportclub.locker_assignments SET status = 'returned', returned_at = now() WHERE id = $1 AND status = 'active'
		RETURNING locker_id, property_id`, aid).Scan(&lockerID, &property); err != nil {
		if dbtx.IsNoRows(err) {
			return LockerAssignment{}, errs.Conflict("not_active", "the assignment is not active")
		}
		return LockerAssignment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.lockers SET status = 'available' WHERE id = $1 AND status = 'occupied'`, lockerID); err != nil {
		return LockerAssignment{}, err
	}
	rows, err := tx.Query(ctx, lockerAssignSelect+` WHERE a.id = $1`, aid)
	a, err := handle.One[LockerAssignment](rows, err, "locker assignment")
	if err != nil {
		return a, err
	}
	return a, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionStatusChange, EntityType: "sportclub.locker_assignment",
		EntityID: aid.String(), EntityLabel: "Locker " + a.LockerCode, PropertyID: &property, After: a})
}

// ── court booking (FR-SPT-02/03) ──────────────────────────────────────────

type CourtBookingInput struct {
	CourtID     uuid.UUID     `json:"courtId"`
	Start       time.Time     `json:"start"`
	End         time.Time     `json:"end"`
	CustomerID  *uuid.UUID    `json:"customerId,omitempty"`
	Guest       *GuestInput   `json:"guest,omitempty"`
	Segment     string        `json:"segment,omitempty" doc:"Default: member when the customer has an active membership, else walk_in"`
	Channel     string        `json:"channel,omitempty" enum:"ops,back_office,member_app,website"`
	PackageCode string        `json:"packageCode,omitempty" doc:"Session Package (4x/8x court voucher) to redeem instead of paying"`
	Hold        bool          `json:"hold,omitempty" doc:"Create as Draft (online checkout)"`
	Notes       string        `json:"notes,omitempty"`
	Payment     *PaymentInput `json:"payment,omitempty"`
}

// CourtBookingResult is the booking with its folio.
type CourtBookingResult struct {
	Reservation reservation.Reservation `json:"reservation"`
	Folio       *billing.FolioDetail    `json:"folio"`
	Total       string                  `json:"total"`
}

// BookCourt books a court slot (exclusive) with the right rate or package.
func (m *Module) BookCourt(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CourtBookingInput, key string) (CourtBookingResult, error) {
	var resID *uuid.UUID
	var courtName, item, facilityID string
	if err := tx.QueryRow(ctx, `SELECT c.resource_id, c.name, coalesce(c.price_item, f.price_item, f.code), c.facility_id::text FROM sportclub.courts c
		JOIN sportclub.facilities f ON f.id = c.facility_id WHERE c.id = $1 AND c.property_id = $2 AND c.status = 'active'`, in.CourtID, property).
		Scan(&resID, &courtName, &item, &facilityID); err != nil {
		if dbtx.IsNoRows(err) {
			return CourtBookingResult{}, errNotFound("court")
		}
		return CourtBookingResult{}, err
	}
	if resID == nil {
		return CourtBookingResult{}, errs.Conflict("court_not_bookable", courtName+" has no bookable resource")
	}
	if in.Channel == "" {
		in.Channel = "back_office"
	}
	cid, name, err := resolveCustomer(ctx, tx, property, in.CustomerID, in.Guest)
	if err != nil {
		return CourtBookingResult{}, err
	}
	segment := in.Segment
	if segment == "" {
		segment = "walk_in"
		if cid != nil {
			if s, err := membership.Segment(ctx, tx, property, *cid, "sportclub"); err != nil {
				return CourtBookingResult{}, err
			} else if s != "" {
				segment = s
			}
		}
	}
	r, err := m.Res.Book(ctx, tx, property, reservation.BookRequest{BusinessLine: "sportclub", Channel: in.Channel, CustomerID: cid, GuestName: name,
		Hold: in.Hold, Confirm: !in.Hold, SourceType: "sportclub.court_booking", Notes: in.Notes,
		Lines: []reservation.LineRequest{{ResourceID: *resID, Start: in.Start, End: in.End, Description: courtName,
			Attributes: map[string]any{"courtId": in.CourtID.String(), "facilityId": facilityID}}}})
	if err != nil {
		return CourtBookingResult{}, err
	}
	out := CourtBookingResult{Total: "0"}
	if in.PackageCode != "" {
		vk := ""
		if key != "" {
			vk = "court-" + key
		}
		if _, err := m.Vouchers.Redeem(ctx, tx, commercial.RedeemRequest{PropertyID: property, Code: in.PackageCode, Quantity: decimal.NewFromInt(1),
			ServiceType: "sport_court", ItemRef: item, CustomerID: cid, SourceType: "reservation.reservation", SourceID: &r.ID, IdempotencyKey: vk}); err != nil {
			return out, err
		}
		if err := m.Res.SetAttribute(ctx, tx, r.ID, "packageCode", strings.ToUpper(in.PackageCode)); err != nil {
			return out, err
		}
	} else {
		var total decimal.Decimal
		r, total, err = m.Res.ChargeLines(ctx, tx, r, segment, 0)
		if err != nil {
			return out, err
		}
		out.Total = total.String()
		if in.Payment != nil && r.FolioID != nil {
			if err := m.settle(ctx, tx, *r.FolioID, in.Payment, key); err != nil {
				return out, err
			}
			if r.Status == reservation.StatusDraft {
				if r, err = m.Res.Confirm(ctx, tx, r.ID, false); err != nil {
					return out, err
				}
			}
		}
		if r.FolioID != nil {
			d, err := billing.GetFolio(ctx, tx, *r.FolioID)
			if err != nil {
				return out, err
			}
			out.Folio = &d
		}
	}
	if err := m.Res.SetSource(ctx, tx, r.ID, r.ID); err != nil {
		return out, err
	}
	out.Reservation, err = m.Res.Get(ctx, tx, r.ID, false)
	return out, err
}

// Occupancy is the live occupancy of capacity facilities (FR-SPT-08).
type Occupancy struct {
	FacilityID   uuid.UUID  `json:"facilityId" db:"facility_id"`
	FacilityName string     `json:"facilityName" db:"facility_name"`
	FacilityType *string    `json:"facilityType" db:"facility_type"`
	Capacity     *int       `json:"capacity" db:"capacity"`
	Inside       int        `json:"inside" db:"inside"`
	EntriesToday int        `json:"entriesToday" db:"entries_today"`
	Booked       int        `json:"booked" db:"booked" doc:"Places reserved for today (capacity slot)"`
	ResourceID   *uuid.UUID `json:"-" db:"resource_id"`
}

func (m *Module) occupancy(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]Occupancy, error) {
	now := localNow(ctx, q)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	list, err := handle.List[Occupancy](q.Query(ctx, `SELECT f.id AS facility_id, f.name AS facility_name, f.facility_type, f.capacity,
		greatest(0, (SELECT count(*) FILTER (WHERE direction = 'in') - count(*) FILTER (WHERE direction = 'out') FROM sportclub.access_events a
		  WHERE a.facility_id = f.id AND a.result = 'granted' AND a.occurred_at >= $2))::int AS inside,
		(SELECT coalesce(sum(adults + children), 0) FROM sportclub.entries e WHERE e.facility_id = f.id AND e.visit_date = $3 AND e.status <> 'cancelled')::int AS entries_today,
		0 AS booked, f.resource_id
		FROM sportclub.facilities f WHERE f.property_id = $1 AND f.status = 'active' AND f.archived_at IS NULL AND f.usage_mode = 'entry' ORDER BY f.name`,
		property, today, today.Format("2006-01-02")))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ResourceID != nil {
			if list[i].Booked, err = m.Res.BookedAt(ctx, q, *list[i].ResourceID, today); err != nil {
				return nil, err
			}
		}
	}
	return list, nil
}
