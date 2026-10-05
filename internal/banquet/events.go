package banquet

// Event Management (PRD P3 EP-12) and venue holds (EP-15): event creation,
// the tentative hold with option date and waitlist on the Reservation
// Engine, Definite on the down payment (or manually with approval),
// cancellation tiers and completion with the final pax.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/rules"
	"oneclub/internal/reservation"
)

// Event is the event header (FR-EVT-01).
type BanquetEvent struct {
	ID                   uuid.UUID  `json:"id" db:"id"`
	PropertyID           uuid.UUID  `json:"propertyId" db:"property_id"`
	Number               string     `json:"number" db:"number"`
	Title                string     `json:"title" db:"title"`
	EventTypeID          uuid.UUID  `json:"eventTypeId" db:"event_type_id"`
	EventTypeCode        string     `json:"eventTypeCode" db:"event_type_code"`
	EventTypeName        string     `json:"eventTypeName" db:"event_type_name"`
	Category             string     `json:"category" db:"category" enum:"wedding,banquet,mice,social,sport,tournament,other"`
	BanquetFlow          bool       `json:"banquetFlow" db:"banquet_flow"`
	Status               string     `json:"status" db:"status" enum:"inquiry,tentative,definite,completed,cancelled"`
	CustomerID           *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName         *string    `json:"customerName" db:"customer_name"`
	CorporateAccountID   *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	CorporateName        *string    `json:"corporateName" db:"corporate_name"`
	ContactName          *string    `json:"contactName" db:"contact_name"`
	ContactPhone         *string    `json:"contactPhone" db:"contact_phone"`
	ContactEmail         *string    `json:"contactEmail" db:"contact_email"`
	SalesOwnerID         *uuid.UUID `json:"salesOwnerId" db:"sales_owner_id"`
	SalesOwnerName       *string    `json:"salesOwnerName" db:"sales_owner_name"`
	Start                time.Time  `json:"start" db:"start_at"`
	End                  time.Time  `json:"end" db:"end_at"`
	ExpectedPax          int        `json:"expectedPax" db:"expected_pax"`
	GuaranteedPax        *int       `json:"guaranteedPax" db:"guaranteed_pax"`
	FinalPax             *int       `json:"finalPax" db:"final_pax"`
	PaxDeadline          *string    `json:"paxDeadline" db:"pax_deadline" doc:"Guaranteed (final) pax cut-off, YYYY-MM-DD"`
	ChargedPax           int        `json:"chargedPax" db:"charged_pax" doc:"Pax covered by the posted package charges"`
	PackageID            *uuid.UUID `json:"packageId" db:"package_id"`
	PackageName          *string    `json:"packageName" db:"package_name"`
	Layout               *string    `json:"layout" db:"layout"`
	PowerWatt            int        `json:"powerWatt" db:"power_watt"`
	QuotationID          *uuid.UUID `json:"quotationId" db:"quotation_id"`
	QuotationNumber      *string    `json:"quotationNumber" db:"quotation_number"`
	OpportunityID        *uuid.UUID `json:"opportunityId" db:"opportunity_id"`
	LeadID               *uuid.UUID `json:"leadId" db:"lead_id"`
	TournamentID         *uuid.UUID `json:"tournamentId" db:"tournament_id"`
	Public               bool       `json:"public" db:"public"`
	RegistrationOpen     bool       `json:"registrationOpen" db:"registration_open"`
	Capacity             *int       `json:"capacity" db:"capacity"`
	RegistrationFee      string     `json:"registrationFee" db:"registration_fee"`
	MembersOnly          bool       `json:"membersOnly" db:"members_only"`
	RegistrationClosesAt *time.Time `json:"registrationClosesAt" db:"registration_closes_at"`
	Description          *string    `json:"description" db:"description"`
	FolioID              *uuid.UUID `json:"folioId" db:"folio_id"`
	ScheduleID           *uuid.UUID `json:"scheduleId" db:"schedule_id"`
	Currency             string     `json:"currency" db:"currency"`
	ContractTotal        string     `json:"contractTotal" db:"contract_total"`
	OptionDate           *time.Time `json:"optionDate" db:"option_date" doc:"Tentative holds expire at the option date"`
	Source               string     `json:"source" db:"source" enum:"back_office,quotation,website,member_app,import"`
	SpecialRequests      *string    `json:"specialRequests" db:"special_requests"`
	Notes                *string    `json:"notes" db:"notes"`
	DefiniteAt           *time.Time `json:"definiteAt" db:"definite_at"`
	DefiniteReason       *string    `json:"definiteReason" db:"definite_reason"`
	CompletedAt          *time.Time `json:"completedAt" db:"completed_at"`
	CancelledAt          *time.Time `json:"cancelledAt" db:"cancelled_at"`
	CancelReason         *string    `json:"cancelReason" db:"cancel_reason"`
	CancellationFee      *string    `json:"cancellationFee" db:"cancellation_fee"`
	FinalInvoiceID       *uuid.UUID `json:"finalInvoiceId" db:"final_invoice_id"`
	FinalBilledAt        *time.Time `json:"finalBilledAt" db:"final_billed_at"`
	SettledAt            *time.Time `json:"settledAt" db:"settled_at"`
	Version              int        `json:"version" db:"version"`
	CreatedAt            time.Time  `json:"createdAt" db:"created_at"`
}

const eventSelect = `SELECT e.id, e.property_id, e.number, e.title, e.event_type_id, t.code AS event_type_code, t.name AS event_type_name, e.category,
	t.banquet_flow, e.status, e.customer_id, c.name AS customer_name, e.corporate_account_id, ca.name AS corporate_name, e.contact_name, e.contact_phone,
	e.contact_email, e.sales_owner_id, u.full_name AS sales_owner_name, e.start_at, e.end_at, e.expected_pax, e.guaranteed_pax, e.final_pax,
	to_char(e.pax_deadline, 'YYYY-MM-DD') AS pax_deadline, e.charged_pax, e.package_id, p.name AS package_name, e.layout, e.power_watt, e.quotation_id,
	e.quotation_number, e.opportunity_id, e.lead_id, e.tournament_id, e.public, e.registration_open, e.capacity,
	trim_scale(e.registration_fee)::text AS registration_fee, e.members_only, e.registration_closes_at, e.description, e.folio_id, e.schedule_id,
	e.currency, trim_scale(e.contract_total)::text AS contract_total, e.option_date, e.source, e.special_requests, e.notes, e.definite_at,
	e.definite_reason, e.completed_at, e.cancelled_at, e.cancel_reason, trim_scale(e.cancellation_fee)::text AS cancellation_fee, e.final_invoice_id,
	e.final_billed_at, e.settled_at, e.version, e.created_at
	FROM banquet.events e JOIN banquet.event_types t ON t.id = e.event_type_id
	LEFT JOIN reporting.customer_directory c ON c.id = e.customer_id LEFT JOIN crm.corporate_accounts ca ON ca.id = e.corporate_account_id
	LEFT JOIN platform.users u ON u.id = e.sales_owner_id LEFT JOIN banquet.packages p ON p.id = e.package_id`

// Get returns an event.
func (m *Module) Get(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (BanquetEvent, error) {
	rows, err := q.Query(ctx, eventSelect+` WHERE e.id = $1`, eid)
	return handle.One[BanquetEvent](rows, err, "event")
}

func (m *Module) lock(ctx context.Context, tx pgx.Tx, eid uuid.UUID) (BanquetEvent, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM banquet.events WHERE id = $1 FOR UPDATE`, eid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return BanquetEvent{}, errs.NotFound("event")
		}
		return BanquetEvent{}, err
	}
	return m.Get(ctx, tx, eid)
}

func (e BanquetEvent) holder() string {
	for _, s := range []*string{e.CorporateName, e.CustomerName, e.ContactName} {
		if s != nil && *s != "" {
			return *s
		}
	}
	return e.Title
}

// paxBasis is the pax the kitchen produces for: final, guaranteed, expected.
func (e BanquetEvent) paxBasis() int {
	switch {
	case e.FinalPax != nil && *e.FinalPax > 0:
		return *e.FinalPax
	case e.GuaranteedPax != nil && *e.GuaranteedPax > 0:
		return *e.GuaranteedPax
	}
	return e.ExpectedPax
}

func (e BanquetEvent) open() bool {
	return e.Status == StatusInquiry || e.Status == StatusTentative || e.Status == StatusDefinite
}

func (e BanquetEvent) channel() string {
	switch e.Source {
	case "website", "member_app", "import":
		return e.Source
	}
	return "back_office"
}

// touch bumps the version (ETag) of an event.
func (m *Module) touch(ctx context.Context, tx pgx.Tx, eid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE banquet.events SET version = version + 1, updated_by = $2 WHERE id = $1`, eid, actor(ctx))
	return err
}

// setStatus moves an event to a status, audited with before / after.
func (m *Module) setStatus(ctx context.Context, tx pgx.Tx, before BanquetEvent, status, action, reason string, extra string, args ...any) (BanquetEvent, error) {
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET status = $2, updated_by = $3, version = version + 1`+extra+` WHERE id = $1`,
		append([]any{before.ID, status, actor(ctx)}, args...)...); err != nil {
		return before, err
	}
	after, err := m.Get(ctx, tx, before.ID)
	if err != nil {
		return after, err
	}
	if action == "" {
		action = audit.ActionStatusChange
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: action, EntityType: "banquet.event", EntityID: before.ID.String(),
		EntityLabel: before.Number + " · " + before.Title, PropertyID: &before.PropertyID, Before: before, After: after, Reason: reason,
		ActorName: sysActor(ctx)})
}

func sysActor(ctx context.Context) string {
	if actor(ctx) == nil {
		return "system:banquet"
	}
	return ""
}

func localDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}

func daysUntil(ctx context.Context, q dbtx.Querier, t time.Time) int {
	loc := calendar.Location(ctx, q)
	return int(localDay(t, loc).Sub(localDay(clock.Now(), loc)).Hours() / 24)
}

// ── create & update ───────────────────────────────────────────────────────

// GuestInput is a new contact (found or created by phone / e-mail).
type EventGuestInput struct {
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

// HoldInput holds a venue for the event (FR-VEN-03).
type VenueHoldInput struct {
	VenueID      uuid.UUID  `json:"venueId"`
	FunctionName string     `json:"functionName,omitempty" doc:"Function / session, e.g. Akad, Reception"`
	Layout       string     `json:"layout,omitempty" enum:"round_table,classroom,u_shape,theater,boardroom,standing,banquet,cocktail"`
	Pax          int        `json:"pax,omitempty" doc:"Default: the expected pax of the event"`
	Start        *time.Time `json:"start,omitempty" doc:"Default: the event start"`
	End          *time.Time `json:"end,omitempty" doc:"Default: the event end"`
	Waitlist     bool       `json:"waitlist,omitempty" doc:"Join the waitlist when another event holds the venue (second hold)"`
}

// EventInput creates an event.
type EventInput struct {
	Title              string           `json:"title"`
	EventTypeID        uuid.UUID        `json:"eventTypeId"`
	CustomerID         *uuid.UUID       `json:"customerId,omitempty"`
	Guest              *EventGuestInput `json:"guest,omitempty" doc:"Contact without a customer id (found or created by phone / e-mail)"`
	CorporateAccountID *uuid.UUID       `json:"corporateAccountId,omitempty" doc:"Company event: billed to the corporate account"`
	ContactName        string           `json:"contactName,omitempty"`
	ContactPhone       string           `json:"contactPhone,omitempty"`
	ContactEmail       string           `json:"contactEmail,omitempty"`
	SalesOwnerID       *uuid.UUID       `json:"salesOwnerId,omitempty" doc:"Default: the creating user"`
	Start              time.Time        `json:"start"`
	End                time.Time        `json:"end"`
	ExpectedPax        int              `json:"expectedPax"`
	Layout             string           `json:"layout,omitempty" enum:"round_table,classroom,u_shape,theater,boardroom,standing,banquet,cocktail"`
	PowerWatt          int              `json:"powerWatt,omitempty" doc:"Electricity needed (watt)"`
	PackageID          *uuid.UUID       `json:"packageId,omitempty" doc:"Banquet package priced for the expected pax"`
	Venues             []VenueHoldInput `json:"venues,omitempty"`
	TournamentID       *uuid.UUID       `json:"tournamentId,omitempty" doc:"Golf tournament of the event"`
	Public             bool             `json:"public,omitempty" doc:"Listed on the website and in the Member App"`
	RegistrationOpen   bool             `json:"registrationOpen,omitempty"`
	Capacity           *int             `json:"capacity,omitempty" doc:"Registration capacity (seats)"`
	RegistrationFee    string           `json:"registrationFee,omitempty"`
	MembersOnly        bool             `json:"membersOnly,omitempty"`
	Description        string           `json:"description,omitempty"`
	SpecialRequests    string           `json:"specialRequests,omitempty"`
	Notes              string           `json:"notes,omitempty"`
}

// EventPatch edits an event (If-Match: the event version).
type EventPatch struct {
	Title                *string    `json:"title,omitempty"`
	ContactName          *string    `json:"contactName,omitempty"`
	ContactPhone         *string    `json:"contactPhone,omitempty"`
	ContactEmail         *string    `json:"contactEmail,omitempty"`
	SalesOwnerID         *uuid.UUID `json:"salesOwnerId,omitempty"`
	ExpectedPax          *int       `json:"expectedPax,omitempty"`
	Layout               *string    `json:"layout,omitempty"`
	PowerWatt            *int       `json:"powerWatt,omitempty"`
	TournamentID         *uuid.UUID `json:"tournamentId,omitempty"`
	Public               *bool      `json:"public,omitempty"`
	RegistrationOpen     *bool      `json:"registrationOpen,omitempty"`
	Capacity             *int       `json:"capacity,omitempty"`
	RegistrationFee      *string    `json:"registrationFee,omitempty"`
	MembersOnly          *bool      `json:"membersOnly,omitempty"`
	RegistrationClosesAt *time.Time `json:"registrationClosesAt,omitempty"`
	Description          *string    `json:"description,omitempty"`
	SpecialRequests      *string    `json:"specialRequests,omitempty"`
	Notes                *string    `json:"notes,omitempty"`
}

func validLayout(l string) bool {
	if l == "" {
		return true
	}
	for _, x := range Layouts {
		if x == l {
			return true
		}
	}
	return false
}

type eventSeed struct {
	EventInput
	Source          string
	QuotationID     *uuid.UUID
	QuotationNumber string
	OpportunityID   *uuid.UUID
	LeadID          *uuid.UUID
}

// CreateEvent creates an event (inquiry), holds its venues and prices its
// package.
func (m *Module) CreateEvent(ctx context.Context, tx pgx.Tx, property uuid.UUID, in EventInput) (EventDetail, error) {
	e, err := m.createEvent(ctx, tx, property, eventSeed{EventInput: in, Source: "back_office"})
	if err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, e.ID)
}

func (m *Module) createEvent(ctx context.Context, tx pgx.Tx, property uuid.UUID, in eventSeed) (BanquetEvent, error) {
	if err := handle.Required("title", in.Title); err != nil {
		return BanquetEvent{}, err
	}
	if in.Start.IsZero() || !in.End.After(in.Start) {
		return BanquetEvent{}, handle.Invalid("end", "invalid_period", "end must be after start")
	}
	if in.ExpectedPax < 0 || in.PowerWatt < 0 {
		return BanquetEvent{}, handle.Invalid("expectedPax", "invalid", "pax cannot be negative")
	}
	if !validLayout(in.Layout) {
		return BanquetEvent{}, handle.Invalid("layout", "invalid", "unknown layout")
	}
	var category, typeStatus string
	if err := tx.QueryRow(ctx, `SELECT category, status FROM banquet.event_types WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`,
		in.EventTypeID, property).Scan(&category, &typeStatus); err != nil {
		if dbtx.IsNoRows(err) {
			return BanquetEvent{}, handle.Invalid("eventTypeId", "not_found", "event type not found")
		}
		return BanquetEvent{}, err
	}
	if typeStatus != "active" {
		return BanquetEvent{}, handle.Invalid("eventTypeId", "inactive", "the event type is inactive")
	}
	cid := in.CustomerID
	contactName, contactPhone, contactEmail := in.ContactName, in.ContactPhone, in.ContactEmail
	switch {
	case cid != nil:
		c, err := crm.GetCustomer(ctx, tx, *cid)
		if err != nil {
			return BanquetEvent{}, handle.Invalid("customerId", "not_found", "customer not found")
		}
		if contactName == "" {
			contactName, contactPhone, contactEmail = c.Name, c.Phone, c.Email
		}
	case in.Guest != nil && strings.TrimSpace(in.Guest.Name) != "":
		if contactName == "" {
			contactName, contactPhone, contactEmail = in.Guest.Name, in.Guest.Phone, in.Guest.Email
		}
		if in.Guest.Phone != "" || in.Guest.Email != "" {
			c, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: in.Guest.Name, Phone: in.Guest.Phone, Email: in.Guest.Email})
			if err != nil {
				return BanquetEvent{}, err
			}
			cid = &c.ID
		}
	}
	if in.CorporateAccountID != nil {
		if _, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID); err != nil {
			return BanquetEvent{}, err
		}
	}
	if cid == nil && in.CorporateAccountID == nil && strings.TrimSpace(contactName) == "" {
		return BanquetEvent{}, handle.Invalid("customerId", "required", "a customer, a company or a contact is required")
	}
	owner := in.SalesOwnerID
	if owner == nil {
		owner = actor(ctx)
	} else {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.users WHERE id = $1)`, *owner).Scan(&ok); err != nil {
			return BanquetEvent{}, err
		}
		if !ok {
			return BanquetEvent{}, handle.Invalid("salesOwnerId", "not_found", "user not found")
		}
	}
	fee, err := handle.Decimal("registrationFee", in.RegistrationFee, decimal.Zero)
	if err != nil {
		return BanquetEvent{}, err
	}
	if fee.IsNegative() {
		return BanquetEvent{}, handle.Invalid("registrationFee", "invalid", "fee cannot be negative")
	}
	if in.Capacity != nil && *in.Capacity <= 0 {
		return BanquetEvent{}, handle.Invalid("capacity", "invalid", "capacity must be positive")
	}
	pol, ref, err := bookingPolicy(ctx, tx, property)
	if err != nil {
		return BanquetEvent{}, err
	}
	loc := calendar.Location(ctx, tx)
	no, err := numbering.Next(ctx, tx, property, "EVT", clock.Now().In(loc))
	if err != nil {
		return BanquetEvent{}, err
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return BanquetEvent{}, err
	}
	source := in.Source
	if source == "" {
		source = "back_office"
	}
	deadline := localDay(in.Start, loc).AddDate(0, 0, -pol.GuaranteedPaxDaysBefore)
	eid := id.New()
	refs := []rules.PolicyRef{ref}
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.events (id, property_id, number, title, event_type_id, category, status, customer_id, corporate_account_id,
		contact_name, contact_phone, contact_email, sales_owner_id, start_at, end_at, expected_pax, pax_deadline, layout, power_watt, quotation_id,
		quotation_number, opportunity_id, lead_id, tournament_id, public, registration_open, capacity, registration_fee, members_only, description,
		currency, source, special_requests, notes, policy_refs, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,'inquiry',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27::numeric,$28,$29,$30,$31,$32,$33,$34,$35,$35)`,
		eid, property, no, strings.TrimSpace(in.Title), in.EventTypeID, category, cid, in.CorporateAccountID, nzs(contactName), nzs(contactPhone),
		nzs(contactEmail), owner, in.Start, in.End, in.ExpectedPax, deadline, nzs(in.Layout), in.PowerWatt, in.QuotationID, nzs(in.QuotationNumber),
		in.OpportunityID, in.LeadID, in.TournamentID, in.Public, in.RegistrationOpen, in.Capacity, fee.String(), in.MembersOnly, nzs(in.Description),
		cur, source, nzs(in.SpecialRequests), nzs(in.Notes), refs, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok && in.QuotationID != nil {
			return BanquetEvent{}, errs.Conflict("already_converted", "the quotation is already an event")
		}
		return BanquetEvent{}, err
	}
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return e, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionCreate, EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: no + " · " + e.Title, PropertyID: &property, After: e, ActorName: sysActor(ctx)}); err != nil {
		return e, err
	}
	for i, h := range in.Venues {
		if _, err := m.placeHold(ctx, tx, e.ID, h); err != nil {
			if de, ok := errs.As(err); ok {
				de.Message = fmt.Sprintf("venues[%d]: %s", i, de.Message)
			}
			return e, err
		}
	}
	if in.PackageID != nil {
		if e, err = m.Get(ctx, tx, eid); err != nil {
			return e, err
		}
		if _, err := m.applyPackage(ctx, tx, e, *in.PackageID, in.ExpectedPax, 0); err != nil {
			return e, err
		}
	}
	if err := m.applyTemplates(ctx, tx, eid, nil); err != nil {
		return e, err
	}
	return m.Get(ctx, tx, eid)
}

// checkIfMatch enforces optimistic concurrency (Technical Doc §8.1).
func checkIfMatch(ifMatch string, version int, what string) error {
	im := strings.Trim(strings.TrimPrefix(strings.TrimSpace(ifMatch), "W/"), `"`)
	if im == "" || im == "*" {
		return nil
	}
	if im != fmt.Sprint(version) {
		return errs.Precondition("the " + what + " changed since you opened it; reload and try again")
	}
	return nil
}

// UpdateEvent edits the event header.
func (m *Module) UpdateEvent(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventPatch, ifMatch string) (EventDetail, error) {
	before, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if err := checkIfMatch(ifMatch, before.Version, "event"); err != nil {
		return EventDetail{}, err
	}
	if !before.open() {
		return EventDetail{}, errs.Conflict("event_closed", "a "+before.Status+" event cannot be changed")
	}
	sets := []string{}
	args := []any{eid}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.Title != nil {
		if strings.TrimSpace(*in.Title) == "" {
			return EventDetail{}, handle.Invalid("title", "required", "title is required")
		}
		set("title", strings.TrimSpace(*in.Title))
	}
	if in.ContactName != nil {
		set("contact_name", nzs(*in.ContactName))
	}
	if in.ContactPhone != nil {
		set("contact_phone", nzs(*in.ContactPhone))
	}
	if in.ContactEmail != nil {
		set("contact_email", nzs(*in.ContactEmail))
	}
	if in.SalesOwnerID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.users WHERE id = $1)`, *in.SalesOwnerID).Scan(&ok); err != nil {
			return EventDetail{}, err
		}
		if !ok {
			return EventDetail{}, handle.Invalid("salesOwnerId", "not_found", "user not found")
		}
		set("sales_owner_id", *in.SalesOwnerID)
	}
	if in.ExpectedPax != nil {
		if *in.ExpectedPax < 0 {
			return EventDetail{}, handle.Invalid("expectedPax", "invalid", "pax cannot be negative")
		}
		set("expected_pax", *in.ExpectedPax)
	}
	if in.Layout != nil {
		if !validLayout(*in.Layout) {
			return EventDetail{}, handle.Invalid("layout", "invalid", "unknown layout")
		}
		set("layout", nzs(*in.Layout))
	}
	if in.PowerWatt != nil {
		if *in.PowerWatt < 0 {
			return EventDetail{}, handle.Invalid("powerWatt", "invalid", "watt cannot be negative")
		}
		set("power_watt", *in.PowerWatt)
	}
	if in.TournamentID != nil {
		set("tournament_id", *in.TournamentID)
	}
	if in.Public != nil {
		set("public", *in.Public)
	}
	if in.RegistrationOpen != nil {
		set("registration_open", *in.RegistrationOpen)
	}
	if in.Capacity != nil {
		if *in.Capacity <= 0 {
			return EventDetail{}, handle.Invalid("capacity", "invalid", "capacity must be positive")
		}
		set("capacity", *in.Capacity)
	}
	if in.RegistrationFee != nil {
		fee, err := handle.Decimal("registrationFee", *in.RegistrationFee, decimal.Zero)
		if err != nil || fee.IsNegative() {
			return EventDetail{}, handle.Invalid("registrationFee", "invalid", "a non-negative amount")
		}
		set("registration_fee", fee.String())
		sets[len(sets)-1] += "::numeric"
	}
	if in.MembersOnly != nil {
		set("members_only", *in.MembersOnly)
	}
	if in.RegistrationClosesAt != nil {
		set("registration_closes_at", *in.RegistrationClosesAt)
	}
	if in.Description != nil {
		set("description", nzs(*in.Description))
	}
	if in.SpecialRequests != nil {
		set("special_requests", nzs(*in.SpecialRequests))
	}
	if in.Notes != nil {
		set("notes", nzs(*in.Notes))
	}
	if len(sets) > 0 {
		args = append(args, actor(ctx))
		if _, err := tx.Exec(ctx, `UPDATE banquet.events SET `+strings.Join(sets, ", ")+fmt.Sprintf(`, version = version + 1, updated_by = $%d WHERE id = $1`, len(args)),
			args...); err != nil {
			return EventDetail{}, err
		}
	}
	after, err := m.Get(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionUpdate, EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: before.Number + " · " + after.Title, PropertyID: &before.PropertyID, Before: before, After: after}); err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, eid)
}

// ── venue holds (EP-15) ───────────────────────────────────────────────────

// VenueHold is a venue held for an event.
type VenueHold struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	EventID         uuid.UUID  `json:"eventId" db:"event_id"`
	VenueID         uuid.UUID  `json:"venueId" db:"venue_id"`
	VenueCode       string     `json:"venueCode" db:"venue_code"`
	VenueName       string     `json:"venueName" db:"venue_name"`
	FunctionName    *string    `json:"functionName" db:"function_name"`
	Layout          *string    `json:"layout" db:"layout"`
	Pax             *int       `json:"pax" db:"pax"`
	Start           time.Time  `json:"start" db:"start_at"`
	End             time.Time  `json:"end" db:"end_at"`
	Status          string     `json:"status" db:"status" enum:"tentative,definite,waitlisted,released,expired,cancelled,completed"`
	ReservationID   *uuid.UUID `json:"reservationId" db:"reservation_id"`
	ReservationCode *string    `json:"reservationCode" db:"reservation_code"`
	WaitlistRank    *int       `json:"waitlistRank" db:"waitlist_rank"`
	OptionDate      *time.Time `json:"optionDate" db:"option_date"`
	PromotedAt      *time.Time `json:"promotedAt" db:"promoted_at"`
	ReleasedAt      *time.Time `json:"releasedAt" db:"released_at"`
	ReleaseReason   *string    `json:"releaseReason" db:"release_reason"`
}

const holdSelect = `SELECT h.id, h.event_id, h.venue_id, v.code AS venue_code, v.name AS venue_name, h.function_name, h.layout, h.pax, h.start_at,
	h.end_at, h.status, h.reservation_id, r.code AS reservation_code, h.waitlist_rank, h.option_date, h.promoted_at, h.released_at, h.release_reason
	FROM banquet.event_venues h JOIN banquet.venues v ON v.id = h.venue_id LEFT JOIN reporting.reservations r ON r.reservation_id = h.reservation_id`

func (m *Module) holds(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]VenueHold, error) {
	return handle.List[VenueHold](q.Query(ctx, holdSelect+` WHERE h.event_id = $1 ORDER BY h.start_at, h.created_at`, eid))
}

func (m *Module) hold(ctx context.Context, q dbtx.Querier, hid uuid.UUID) (VenueHold, error) {
	rows, err := q.Query(ctx, holdSelect+` WHERE h.id = $1`, hid)
	return handle.One[VenueHold](rows, err, "venue hold")
}

func activeHold(s string) bool { return s == "tentative" || s == "definite" }

type venueRow struct {
	ID              uuid.UUID
	Code, Name      string
	VenueType       string
	ResourceID      *uuid.UUID
	MinPax          int
	MaxCapacity     *int
	AddonPrice      decimal.Decimal
	ElectricityWatt int
	Status          string
}

func venueByID(ctx context.Context, q dbtx.Querier, property, vid uuid.UUID) (venueRow, error) {
	var v venueRow
	var addon string
	err := q.QueryRow(ctx, `SELECT id, code, name, venue_type, resource_id, min_pax, max_capacity, addon_price::text, electricity_watt, status
		FROM banquet.venues WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, vid, property).
		Scan(&v.ID, &v.Code, &v.Name, &v.VenueType, &v.ResourceID, &v.MinPax, &v.MaxCapacity, &addon, &v.ElectricityWatt, &v.Status)
	if dbtx.IsNoRows(err) {
		return v, handle.Invalid("venueId", "not_found", "venue not found")
	}
	v.AddonPrice = dec(addon)
	return v, err
}

var errVenueTaken = errs.Conflict("venue_taken", "the venue is held by another event for an overlapping period")

// bookVenue allocates the venue (and the parts of a combined venue) on the
// Reservation Engine: pending while tentative, confirmed when definite.
func (m *Module) bookVenue(ctx context.Context, tx pgx.Tx, e BanquetEvent, v venueRow, holdID uuid.UUID, start, end time.Time, confirm bool) (*reservation.Reservation, error) {
	if v.ResourceID == nil {
		return nil, errs.Conflict("not_bookable", v.Name+" has no bookable resource")
	}
	lines := []reservation.LineRequest{{ResourceID: *v.ResourceID, Start: start, End: end, Description: v.Name}}
	rows, err := tx.Query(ctx, `SELECT resource_id, name FROM banquet.venues WHERE parent_venue_id = $1 AND status = 'active' AND archived_at IS NULL
		AND resource_id IS NOT NULL ORDER BY code`, v.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rid uuid.UUID
		var name string
		if err := rows.Scan(&rid, &name); err != nil {
			rows.Close()
			return nil, err
		}
		lines = append(lines, reservation.LineRequest{ResourceID: rid, Start: start, End: end, Description: name + " (part of " + v.Name + ")"})
	}
	rows.Close()
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := m.Res.Book(ctx, sp, e.PropertyID, reservation.BookRequest{Kind: "block", BusinessLine: "banquet", Channel: e.channel(), CustomerID: e.CustomerID,
		GuestName: deref(e.ContactName), GuestPhone: deref(e.ContactPhone), CorporateName: deref(e.CorporateName), Confirm: confirm,
		SourceType: "banquet.event_venue", SourceID: &holdID, Notes: e.Number + " · " + e.Title, Lines: lines,
		Attributes: map[string]any{"eventId": e.ID.String(), "eventNumber": e.Number}})
	if err != nil {
		_ = sp.Rollback(ctx)
		if de, ok := errs.As(err); ok && de.Code == "slot_taken" {
			return nil, errVenueTaken
		}
		return nil, err
	}
	return &r, sp.Commit(ctx)
}

// cancelReservation frees a reservation of the event (no fee: the event
// settles its own cancellation).
func (m *Module) cancelReservation(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) error {
	r, err := m.Res.Get(ctx, tx, rid, false)
	if err != nil {
		return err
	}
	switch r.Status {
	case reservation.StatusDraft, reservation.StatusPending, reservation.StatusConfirmed:
		_, err = m.Res.Cancel(ctx, tx, rid, reason, true)
	}
	return err
}

func (m *Module) confirmReservation(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	r, err := m.Res.Get(ctx, tx, rid, false)
	if err != nil {
		return err
	}
	if r.Status == reservation.StatusDraft || r.Status == reservation.StatusPending {
		_, err = m.Res.Confirm(ctx, tx, rid, true)
	}
	return err
}

func (m *Module) completeReservation(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	r, err := m.Res.Get(ctx, tx, rid, false)
	if err != nil {
		return err
	}
	if r.Status == reservation.StatusConfirmed || r.Status == reservation.StatusCheckedIn {
		_, err = m.Res.Complete(ctx, tx, rid)
	}
	return err
}

// checkVenueFit checks the layout capacity and the minimum pax of a venue
// (outdoor add-on minimum 100 pax, FR-BQT-05).
func checkVenueFit(ctx context.Context, q dbtx.Querier, v venueRow, layout string, pax int) error {
	if v.Status != "active" {
		return errs.Conflict("venue_inactive", v.Name+" is not active")
	}
	if v.MinPax > 0 && pax < v.MinPax {
		return errs.Validation("below_minimum_pax", fmt.Sprintf("%s requires at least %d pax (event has %d)", v.Name, v.MinPax, pax),
			errs.Field("pax", "below_minimum", fmt.Sprintf("minimum %d pax", v.MinPax)))
	}
	if layout != "" {
		var capacity int
		err := q.QueryRow(ctx, `SELECT capacity FROM banquet.venue_layouts WHERE venue_id = $1 AND layout = $2`, v.ID, layout).Scan(&capacity)
		if dbtx.IsNoRows(err) {
			return errs.Validation("layout_unavailable", v.Name+" has no "+strings.ReplaceAll(layout, "_", " ")+" layout")
		}
		if err != nil {
			return err
		}
		if pax > capacity {
			return errs.Validation("over_capacity", fmt.Sprintf("%d pax exceeds the %s capacity of %s (%d)", pax, strings.ReplaceAll(layout, "_", " "), v.Name, capacity),
				errs.Field("pax", "over_capacity", fmt.Sprintf("maximum %d pax", capacity)))
		}
		return nil
	}
	if v.MaxCapacity != nil && pax > *v.MaxCapacity {
		return errs.Validation("over_capacity", fmt.Sprintf("%d pax exceeds the capacity of %s (%d)", pax, v.Name, *v.MaxCapacity),
			errs.Field("pax", "over_capacity", fmt.Sprintf("maximum %d pax", *v.MaxCapacity)))
	}
	return nil
}

// optionUntil is the option date of a new tentative hold.
func optionUntil(pol BookingPolicy, start time.Time) time.Time {
	t := clock.Now().Add(time.Duration(max(pol.TentativeHoldDays, 1)) * 24 * time.Hour)
	if t.After(start) {
		t = start
	}
	return t
}

// placeHold holds a venue: tentative (option date) or definite, or
// waitlisted when another event holds it and the waitlist is requested.
func (m *Module) placeHold(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in VenueHoldInput) (VenueHold, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return VenueHold{}, err
	}
	if !e.open() {
		return VenueHold{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot hold venues")
	}
	pol, _, err := bookingPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return VenueHold{}, err
	}
	v, err := venueByID(ctx, tx, e.PropertyID, in.VenueID)
	if err != nil {
		return VenueHold{}, err
	}
	start, end := e.Start, e.End
	if in.Start != nil {
		start = *in.Start
	}
	if in.End != nil {
		end = *in.End
	}
	if !end.After(start) {
		return VenueHold{}, handle.Invalid("end", "invalid_period", "end must be after start")
	}
	if !validLayout(in.Layout) {
		return VenueHold{}, handle.Invalid("layout", "invalid", "unknown layout")
	}
	layout := in.Layout
	if layout == "" {
		layout = deref(e.Layout)
	}
	pax := in.Pax
	if pax <= 0 {
		pax = e.paxBasis()
	}
	if err := checkVenueFit(ctx, tx, v, layout, pax); err != nil {
		return VenueHold{}, err
	}
	hid := id.New()
	status := StatusTentative
	if e.Status == StatusDefinite {
		status = StatusDefinite
	}
	var rid *uuid.UUID
	var rank *int
	var option *time.Time
	r, err := m.bookVenue(ctx, tx, e, v, hid, start, end, status == StatusDefinite)
	switch {
	case err == errVenueTaken && in.Waitlist && pol.WaitlistEnabled:
		status = "waitlisted"
		var n int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(waitlist_rank), 0) + 1 FROM banquet.event_venues WHERE venue_id = $1 AND status = 'waitlisted'
			AND start_at < $3 AND end_at > $2`, v.ID, start, end).Scan(&n); err != nil {
			return VenueHold{}, err
		}
		rank = &n
	case err == errVenueTaken:
		return VenueHold{}, errs.Conflict("venue_taken", v.Name+" is held by another event for an overlapping period; hold it as waitlist")
	case err != nil:
		return VenueHold{}, err
	default:
		rid = &r.ID
	}
	if status == StatusTentative {
		o := optionUntil(pol, e.Start)
		if e.OptionDate != nil {
			o = *e.OptionDate
		}
		option = &o
	}
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_venues (id, property_id, event_id, venue_id, function_name, layout, pax, start_at, end_at, status,
		reservation_id, waitlist_rank, option_date, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`,
		hid, e.PropertyID, e.ID, v.ID, nzs(in.FunctionName), nzs(layout), pax, start, end, status, rid, rank, option, actor(ctx)); err != nil {
		return VenueHold{}, err
	}
	if status == StatusTentative && e.Status == StatusInquiry {
		if _, err := m.setStatus(ctx, tx, e, StatusTentative, "", "venue held", `, option_date = $4`, option); err != nil {
			return VenueHold{}, err
		}
	} else if err := m.touch(ctx, tx, e.ID); err != nil {
		return VenueHold{}, err
	}
	if activeHold(status) && v.AddonPrice.IsPositive() {
		if err := m.venueAddon(ctx, tx, e.ID, hid, v); err != nil {
			return VenueHold{}, err
		}
	}
	h, err := m.hold(ctx, tx, hid)
	if err != nil {
		return h, err
	}
	return h, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "hold_venue", EntityType: "banquet.event_venue", EntityID: hid.String(),
		EntityLabel: e.Number + " · " + v.Name, PropertyID: &e.PropertyID, After: h, ActorName: sysActor(ctx)})
}

// HoldVenue is the route use case of placeHold.
func (m *Module) HoldVenue(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in VenueHoldInput) (VenueHold, error) {
	return m.placeHold(ctx, tx, eid, in)
}

// releaseHold frees a hold (released by staff, expired option, cancelled
// event) and promotes the waitlist of the venue.
func (m *Module) releaseHold(ctx context.Context, tx pgx.Tx, h VenueHold, status, reason string) error {
	was := h.Status
	if h.ReservationID != nil && activeHold(was) {
		if err := m.cancelReservation(ctx, tx, *h.ReservationID, reason); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_venues SET status = $2, released_at = now(), release_reason = $3, waitlist_rank = NULL, updated_by = $4
		WHERE id = $1`, h.ID, status, nzs(reason), actor(ctx)); err != nil {
		return err
	}
	if err := m.voidCharges(ctx, tx, h.EventID, "venue_addon", &h.ID, reason); err != nil {
		return err
	}
	if activeHold(was) {
		return m.promoteWaitlist(ctx, tx, h.VenueID, h.Start, h.End)
	}
	return nil
}

// ReleaseHold releases a hold of an event.
func (m *Module) ReleaseHold(ctx context.Context, tx pgx.Tx, eid, hid uuid.UUID, reason string) (EventDetail, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	h, err := m.hold(ctx, tx, hid)
	if err != nil {
		return EventDetail{}, err
	}
	if h.EventID != e.ID {
		return EventDetail{}, errs.NotFound("venue hold")
	}
	if !activeHold(h.Status) && h.Status != "waitlisted" {
		return EventDetail{}, errs.Conflict("not_held", "the hold is "+h.Status)
	}
	if !e.open() {
		return EventDetail{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot change its venues")
	}
	if reason == "" {
		reason = "released"
	}
	if err := m.releaseHold(ctx, tx, h, "released", reason); err != nil {
		return EventDetail{}, err
	}
	if err := m.afterHoldsChanged(ctx, tx, e.ID, "venue released"); err != nil {
		return EventDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "release_venue", EntityType: "banquet.event_venue", EntityID: hid.String(),
		EntityLabel: e.Number + " · " + h.VenueName, PropertyID: &e.PropertyID, Before: h, Reason: reason}); err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, e.ID)
}

// afterHoldsChanged returns a tentative event without held venues to
// inquiry.
func (m *Module) afterHoldsChanged(ctx context.Context, tx pgx.Tx, eid uuid.UUID, reason string) error {
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	if e.Status != StatusTentative {
		return m.touch(ctx, tx, eid)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM banquet.event_venues WHERE event_id = $1 AND status IN ('tentative', 'definite')`, eid).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return m.touch(ctx, tx, eid)
	}
	_, err = m.setStatus(ctx, tx, e, StatusInquiry, "", reason, `, option_date = NULL`)
	return err
}

// promoteWaitlist moves the first waitlisted hold of a venue whose period is
// free again into a tentative hold and tells its sales owner (EP-15 AC).
func (m *Module) promoteWaitlist(ctx context.Context, tx pgx.Tx, venueID uuid.UUID, start, end time.Time) error {
	rows, err := tx.Query(ctx, `SELECT h.id FROM banquet.event_venues h JOIN banquet.events e ON e.id = h.event_id WHERE h.venue_id = $1
		AND h.status = 'waitlisted' AND h.start_at < $3 AND h.end_at > $2 AND e.status IN ('inquiry', 'tentative', 'definite')
		ORDER BY h.waitlist_rank NULLS LAST, h.created_at`, venueID, start, end)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, hid := range ids {
		h, err := m.hold(ctx, tx, hid)
		if err != nil {
			return err
		}
		e, err := m.lock(ctx, tx, h.EventID)
		if err != nil {
			return err
		}
		v, err := venueByID(ctx, tx, e.PropertyID, h.VenueID)
		if err != nil {
			return err
		}
		pol, _, err := bookingPolicy(ctx, tx, e.PropertyID)
		if err != nil {
			return err
		}
		status := StatusTentative
		if e.Status == StatusDefinite {
			status = StatusDefinite
		}
		r, err := m.bookVenue(ctx, tx, e, v, h.ID, h.Start, h.End, status == StatusDefinite)
		if err == errVenueTaken {
			continue
		}
		if err != nil {
			return err
		}
		var option *time.Time
		if status == StatusTentative {
			o := optionUntil(pol, e.Start)
			option = &o
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.event_venues SET status = $2, reservation_id = $3, promoted_at = now(), waitlist_rank = NULL,
			option_date = $4, updated_by = $5 WHERE id = $1`, h.ID, status, r.ID, option, actor(ctx)); err != nil {
			return err
		}
		if e.Status == StatusInquiry {
			if e, err = m.setStatus(ctx, tx, e, StatusTentative, "waitlist_promoted", "waitlisted hold promoted", `, option_date = $4`, option); err != nil {
				return err
			}
		} else if err := m.touch(ctx, tx, e.ID); err != nil {
			return err
		}
		if v.AddonPrice.IsPositive() {
			if err := m.venueAddon(ctx, tx, e.ID, h.ID, v); err != nil {
				return err
			}
		}
		after, _ := m.hold(ctx, tx, h.ID)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "waitlist_promoted", EntityType: "banquet.event_venue", EntityID: h.ID.String(),
			EntityLabel: e.Number + " · " + v.Name, PropertyID: &e.PropertyID, Before: h, After: after, ActorName: sysActor(ctx)}); err != nil {
			return err
		}
		if err := m.notifyOwner(ctx, tx, e, "banquet.waitlist_promoted", map[string]any{"venue": v.Name,
			"date": h.Start.In(calendar.Location(ctx, tx)).Format("02 Jan 2006 15:04")}); err != nil {
			return err
		}
	}
	return nil
}

// ExtendOptionInput moves the option date of the tentative holds.
type ExtendOptionInput struct {
	OptionDate time.Time `json:"optionDate"`
	Reason     string    `json:"reason,omitempty"`
}

// ExtendOption extends (or shortens) the option date of a tentative event.
func (m *Module) ExtendOption(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in ExtendOptionInput) (EventDetail, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if e.Status != StatusTentative {
		return EventDetail{}, errs.Conflict("not_tentative", "only tentative events have an option date")
	}
	if !in.OptionDate.After(clock.Now()) || in.OptionDate.After(e.Start) {
		return EventDetail{}, handle.Invalid("optionDate", "invalid", "the option date must be in the future and before the event")
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_venues SET option_date = $2 WHERE event_id = $1 AND status = 'tentative'`, eid, in.OptionDate); err != nil {
		return EventDetail{}, err
	}
	if _, err := m.setStatus(ctx, tx, e, StatusTentative, "extend_option", in.Reason, `, option_date = $4`, in.OptionDate); err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, eid)
}

// ExpireOptions releases the holds of tentative events whose option date
// passed (FR-VEN-03): the event returns to inquiry, the waitlist moves up
// and the sales owner is told. Returns the number of expired events.
func (m *Module) ExpireOptions(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	var ids []uuid.UUID
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM banquet.events WHERE status = 'tentative' AND option_date <= now() ORDER BY option_date`)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, eid := range ids {
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			e, err := m.lock(ctx, tx, eid)
			if err != nil {
				return err
			}
			if e.Status != StatusTentative || e.OptionDate == nil || e.OptionDate.After(clock.Now()) {
				return nil
			}
			hs, err := m.holds(ctx, tx, eid)
			if err != nil {
				return err
			}
			var venues []string
			for _, h := range hs {
				if h.Status == StatusTentative {
					if err := m.releaseHold(ctx, tx, h, "expired", "option date passed"); err != nil {
						return err
					}
					venues = append(venues, h.VenueName)
				}
			}
			if e, err = m.Get(ctx, tx, eid); err != nil {
				return err
			}
			if _, err := m.setStatus(ctx, tx, e, StatusInquiry, "option_expired", "option date passed", `, option_date = NULL`); err != nil {
				return err
			}
			n++
			return m.notifyOwner(ctx, tx, e, "banquet.hold_expired", map[string]any{"venues": strings.Join(venues, ", ")})
		})
		if err != nil {
			return n, fmt.Errorf("expire option of event %s: %w", eid, err)
		}
	}
	return n, nil
}

// ── definite ──────────────────────────────────────────────────────────────

// depositStatus returns the down payment required and what was received
// (payments and deposits on the event folio).
func (m *Module) depositStatus(ctx context.Context, q dbtx.Querier, e BanquetEvent) (required, received decimal.Decimal, err error) {
	if e.ScheduleID != nil {
		var amt *string
		if err := q.QueryRow(ctx, `SELECT amount::text FROM billing.payment_schedule_lines WHERE schedule_id = $1 AND status <> 'cancelled'
			ORDER BY seq LIMIT 1`, *e.ScheduleID).Scan(&amt); err != nil && !dbtx.IsNoRows(err) {
			return required, received, err
		}
		if amt != nil {
			required = dec(*amt)
		}
	} else {
		pol, _, err := bookingPolicy(ctx, q, e.PropertyID)
		if err != nil {
			return required, received, err
		}
		required = dec(e.ContractTotal).Mul(dec(pol.MinDownPaymentPercent)).Div(decimal.NewFromInt(100)).Round(0)
	}
	if e.FolioID != nil {
		paid, held, err := billingPaid(ctx, q, *e.FolioID)
		if err != nil {
			return required, received, err
		}
		received = paid.Add(held)
	}
	return required, received, nil
}

// makeDefinite confirms the event: the venue and resource reservations are
// confirmed, banquet.event_confirmed is published and the customer told.
func (m *Module) makeDefinite(ctx context.Context, tx pgx.Tx, eid uuid.UUID, reason string) (BanquetEvent, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return e, err
	}
	if e.Status == StatusDefinite {
		return e, nil
	}
	if e.Status != StatusInquiry && e.Status != StatusTentative {
		return e, errs.Conflict("invalid_status", "a "+e.Status+" event cannot become definite")
	}
	hs, err := m.holds(ctx, tx, eid)
	if err != nil {
		return e, err
	}
	held, waiting := 0, 0
	for _, h := range hs {
		switch h.Status {
		case StatusTentative:
			held++
		case "waitlisted":
			waiting++
		}
	}
	if held == 0 && waiting > 0 {
		return e, errs.Conflict("venue_waitlisted", "the venue is still waitlisted; it becomes definite once the hold is promoted")
	}
	for _, h := range hs {
		if h.Status != StatusTentative || h.ReservationID == nil {
			continue
		}
		if err := m.confirmReservation(ctx, tx, *h.ReservationID); err != nil {
			return e, err
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.event_venues SET status = 'definite', option_date = NULL, updated_by = $2 WHERE id = $1`, h.ID, actor(ctx)); err != nil {
			return e, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT reservation_id FROM banquet.event_resources WHERE event_id = $1 AND status = 'held' AND reservation_id IS NOT NULL`, eid)
	if err != nil {
		return e, err
	}
	rids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return e, err
	}
	for _, rid := range rids {
		if err := m.confirmReservation(ctx, tx, rid); err != nil {
			return e, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_resources SET status = 'confirmed' WHERE event_id = $1 AND status = 'held'`, eid); err != nil {
		return e, err
	}
	after, err := m.setStatus(ctx, tx, e, StatusDefinite, "definite", reason, `, definite_at = now(), definite_reason = $4, option_date = NULL`, nzs(reason))
	if err != nil {
		return after, err
	}
	if err := m.issueVouchers(ctx, tx, after); err != nil {
		return after, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventConfirmed, "banquet.event", &after.ID, &after.PropertyID, statusPayload(ctx, tx, after)); err != nil {
		return after, err
	}
	loc := calendar.Location(ctx, tx)
	return after, m.notifyCustomer(ctx, tx, after, "banquet.event_confirmed", map[string]any{"date": after.Start.In(loc).Format("02 Jan 2006 15:04")})
}

// statusPayload is the payload of banquet.event_confirmed / event_cancelled.
func statusPayload(ctx context.Context, q dbtx.Querier, e BanquetEvent) map[string]any {
	loc := calendar.Location(ctx, q)
	return map[string]any{"eventId": e.ID, "number": e.Number, "eventType": e.EventTypeCode, "title": e.Title, "customerId": e.CustomerID,
		"startDate": e.Start.In(loc).Format("2006-01-02"), "endDate": e.End.In(loc).Format("2006-01-02"), "pax": e.paxBasis(), "status": e.Status}
}

// DefiniteInput makes an event Definite by hand.
type DefiniteInput struct {
	Reason   string `json:"reason,omitempty"`
	Override bool   `json:"override,omitempty" doc:"Definite before the down payment: needs the Definite without Deposit approval"`
}

// DefiniteResult is the event after the request (or the pending approval).
type DefiniteResult struct {
	Event             EventDetail `json:"event"`
	ApprovalRequestID *uuid.UUID  `json:"approvalRequestId" doc:"Set when an approval was requested"`
	ApprovalStatus    string      `json:"approvalStatus,omitempty" enum:"pending,approved"`
}

// MakeDefinite confirms an event by hand: allowed when the down payment is
// received (or none is due); otherwise only with override and approval.
func (m *Module) MakeDefinite(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in DefiniteInput) (DefiniteResult, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return DefiniteResult{}, err
	}
	if e.Status != StatusInquiry && e.Status != StatusTentative {
		return DefiniteResult{}, errs.Conflict("invalid_status", "a "+e.Status+" event cannot become definite")
	}
	pol, _, err := bookingPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return DefiniteResult{}, err
	}
	required, received, err := m.depositStatus(ctx, tx, e)
	if err != nil {
		return DefiniteResult{}, err
	}
	out := DefiniteResult{}
	if !pol.DefiniteRequiresDeposit || received.GreaterThanOrEqual(required) {
		reason := in.Reason
		if reason == "" {
			reason = "confirmed by staff"
		}
		if _, err := m.makeDefinite(ctx, tx, eid, reason); err != nil {
			return out, err
		}
	} else {
		if !in.Override {
			return out, errs.Conflict("deposit_required", fmt.Sprintf("the down payment of %s is not received (%s paid); request an override",
				required.StringFixed(0), received.StringFixed(0)))
		}
		if err := handle.Required("reason", in.Reason); err != nil {
			return out, err
		}
		rid, st, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: DefiniteOverrideType.Code, DocumentID: eid, DocumentRef: e.Number,
			Title: "Definite without deposit · " + e.Number + " · " + e.Title, PropertyID: e.PropertyID,
			Attributes: map[string]any{"amount": dec(e.ContractTotal).InexactFloat64(), "reason": in.Reason}})
		if err != nil {
			return out, err
		}
		out.ApprovalRequestID, out.ApprovalStatus = &rid, st
		if st != approval.StatusApproved {
			if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "definite_requested", EntityType: "banquet.event", EntityID: eid.String(),
				EntityLabel: e.Number, PropertyID: &e.PropertyID, Reason: in.Reason, Metadata: map[string]any{"approvalRequestId": rid}}); err != nil {
				return out, err
			}
		}
	}
	d, err := m.Detail(ctx, tx, eid)
	out.Event = d
	return out, err
}

// DefiniteDecision applies the Definite without Deposit approval.
func (m *Module) DefiniteDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	if d.Status != approval.StatusApproved {
		return nil
	}
	e, err := m.Get(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	if e.Status != StatusInquiry && e.Status != StatusTentative {
		return nil
	}
	reason := "definite without deposit approved"
	if d.Reason != "" {
		reason += ": " + d.Reason
	}
	_, err = m.makeDefinite(ctx, tx, d.DocumentID, reason)
	return err
}

// OnDepositPaid makes a tentative event Definite once the down payment is
// received on its folio (billing.payment_settled).
func (m *Module) onDepositPaid(ctx context.Context, tx pgx.Tx, eid uuid.UUID) error {
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	if e.Status != StatusTentative && e.Status != StatusInquiry {
		return nil
	}
	required, received, err := m.depositStatus(ctx, tx, e)
	if err != nil {
		return err
	}
	if !required.IsPositive() || received.LessThan(required) {
		return nil
	}
	_, err = m.makeDefinite(ctx, tx, eid, "down payment received")
	if de, ok := errs.As(err); ok && de.Code == "venue_waitlisted" {
		return nil // stays tentative until the hold is promoted
	}
	return err
}

// ── cancel ────────────────────────────────────────────────────────────────

// CancelInput cancels an event.
type EventCancelInput struct {
	Reason   string `json:"reason"`
	WaiveFee bool   `json:"waiveFee,omitempty" doc:"Keep nothing (needs banquet.billing.manage)"`
}

// CancelResult reports the cancellation settlement.
type EventCancelResult struct {
	Event    EventDetail             `json:"event"`
	Fee      string                  `json:"fee" doc:"Forfeited deposit / cancellation fee kept by the club"`
	Refunded string                  `json:"refunded"`
	Tier     BanquetCancellationTier `json:"tier"`
	Policy   rules.PolicyRef         `json:"policy"`
}

// CancelEvent cancels an event under the cancellation tiers of Banquet
// Policies (PRD P3 §16 #11): charges are voided, the forfeited share of the
// deposit becomes the cancellation fee and the rest is refunded; venues and
// resources are released and the waitlist moves up.
func (m *Module) CancelEvent(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventCancelInput) (EventCancelResult, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventCancelResult{}, err
	}
	if !e.open() {
		return EventCancelResult{}, errs.Conflict("invalid_status", "a "+e.Status+" event cannot be cancelled")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return EventCancelResult{}, err
	}
	pol, ref, err := cancellationPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return EventCancelResult{}, err
	}
	out := EventCancelResult{Fee: "0", Refunded: "0", Policy: ref}
	fee := decimal.Zero
	if e.FolioID != nil {
		required, received, err := m.depositStatus(ctx, tx, e)
		if err != nil {
			return out, err
		}
		if t, ok := pol.tier(daysUntil(ctx, tx, e.Start)); ok {
			out.Tier = t
			basis := received
			switch t.Basis {
			case "down_payment":
				basis = decimal.Min(received, required)
			case "contract":
				basis = dec(e.ContractTotal)
			}
			fee = basis.Mul(dec(t.ForfeitPercent)).Div(decimal.NewFromInt(100)).Round(0)
			if t.Basis != "contract" && fee.GreaterThan(received) {
				fee = received
			}
		}
		if in.WaiveFee {
			fee = decimal.Zero
		}
		refunded, err := m.settleCancellation(ctx, tx, e, fee, in.Reason)
		if err != nil {
			return out, err
		}
		out.Refunded = refunded.String()
		if e.ScheduleID != nil {
			if _, err := m.Billing.CancelSchedule(ctx, tx, *e.ScheduleID, "event cancelled: "+in.Reason); err != nil {
				return out, err
			}
		}
	}
	out.Fee = fee.String()
	hs, err := m.holds(ctx, tx, eid)
	if err != nil {
		return out, err
	}
	for _, h := range hs {
		if activeHold(h.Status) || h.Status == "waitlisted" {
			if err := m.releaseHold(ctx, tx, h, StatusCancelled, "event cancelled"); err != nil {
				return out, err
			}
		}
	}
	if err := m.releaseResources(ctx, tx, eid, "event cancelled"); err != nil {
		return out, err
	}
	if err := m.cancelInclusions(ctx, tx, eid, "event cancelled"); err != nil {
		return out, err
	}
	if err := m.withdrawAll(ctx, tx, e, "event cancelled"); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.production_items SET status = 'cancelled' WHERE event_id = $1 AND status <> 'served'`, eid); err != nil {
		return out, err
	}
	after, err := m.setStatus(ctx, tx, e, StatusCancelled, "cancel", in.Reason, `, cancelled_at = now(), cancel_reason = $4, cancellation_fee = $5::numeric,
		option_date = NULL`, in.Reason, fee.String())
	if err != nil {
		return out, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventCancelled, "banquet.event", &after.ID, &after.PropertyID, statusPayload(ctx, tx, after)); err != nil {
		return out, err
	}
	out.Event, err = m.Detail(ctx, tx, eid)
	return out, err
}

// ── complete ──────────────────────────────────────────────────────────────

// CompleteInput completes an event with the final pax.
type EventCompleteInput struct {
	FinalPax int    `json:"finalPax" doc:"Actual pax served; above the charged pax the difference is charged"`
	Notes    string `json:"notes,omitempty"`
}

// CompleteEvent closes a definite event: the pax above the charged pax are
// charged (FR-BQT-10), the BEO is locked (FR-BEO-06), the reservations are
// completed and banquet.event_completed carries the consumption (K6).
func (m *Module) CompleteEvent(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventCompleteInput) (EventDetail, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if e.Status != StatusDefinite {
		return EventDetail{}, errs.Conflict("invalid_status", "only definite events can be completed (is "+e.Status+")")
	}
	if in.FinalPax <= 0 {
		return EventDetail{}, handle.Invalid("finalPax", "required", "the final pax is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET final_pax = $2 WHERE id = $1`, eid, in.FinalPax); err != nil {
		return EventDetail{}, err
	}
	if e, err = m.Get(ctx, tx, eid); err != nil {
		return EventDetail{}, err
	}
	if e.PackageID != nil && in.FinalPax > e.ChargedPax {
		if err := m.chargeExtraPax(ctx, tx, e, in.FinalPax); err != nil {
			return EventDetail{}, err
		}
	}
	hs, err := m.holds(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	for _, h := range hs {
		switch {
		case h.Status == StatusDefinite && h.ReservationID != nil:
			if err := m.completeReservation(ctx, tx, *h.ReservationID); err != nil {
				return EventDetail{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE banquet.event_venues SET status = 'completed' WHERE id = $1`, h.ID); err != nil {
				return EventDetail{}, err
			}
		case h.Status == "waitlisted":
			if err := m.releaseHold(ctx, tx, h, "released", "event completed"); err != nil {
				return EventDetail{}, err
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT reservation_id FROM banquet.event_resources WHERE event_id = $1 AND status = 'confirmed' AND reservation_id IS NOT NULL`, eid)
	if err != nil {
		return EventDetail{}, err
	}
	rids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return EventDetail{}, err
	}
	for _, rid := range rids {
		if err := m.completeReservation(ctx, tx, rid); err != nil {
			return EventDetail{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_resources SET status = 'completed' WHERE event_id = $1 AND status = 'confirmed'`, eid); err != nil {
		return EventDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.beos SET locked_at = now() WHERE event_id = $1 AND locked_at IS NULL`, eid); err != nil {
		return EventDetail{}, err
	}
	notes := in.Notes
	after, err := m.setStatus(ctx, tx, e, StatusCompleted, "complete", notes, `, completed_at = now()`)
	if err != nil {
		return EventDetail{}, err
	}
	payload, err := m.completedPayload(ctx, tx, after)
	if err != nil {
		return EventDetail{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventCompleted, "banquet.event", &after.ID, &after.PropertyID, payload); err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, eid)
}
