package banquet

// CRM quotations of the wedding, banquet, MICE and event lines (PRD P3
// FR-QUO-04/06/07, EP-15, docs/p3-p4-contracts.md). CRM never calls
// banquet: the events are decoded by name.
//
//   - crm.quotation_sent places a Tentative hold on the quoted venue until
//     the option date (an inquiry event carries it); a revision (same
//     number, new version) moves the hold; a taken venue is waitlisted.
//   - crm.quotation_rejected / crm.quotation_expired release the hold (the
//     carrying event is cancelled) and the waitlist moves up.
//   - crm.quotation_accepted converts the quotation into the event — the one
//     holding the venue when there is one — with the quoted lines as charges
//     and the payment terms as the payment schedule (the DP first). It is
//     idempotent per quotation number: the DP is issued once even when the
//     acceptance is delivered twice (FR-QUO-07).

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/outbox"
)

// QuotationLine is one line of an accepted quotation.
type QuotationLine struct {
	ItemType    string  `json:"itemType"`
	ItemRef     *string `json:"itemRef"`
	Description string  `json:"description"`
	Quantity    string  `json:"quantity"`
	UnitPrice   string  `json:"unitPrice"`
	Discount    string  `json:"discount"`
	Total       string  `json:"total"`
}

// PaymentTerm is one payment term of an accepted quotation.
type PaymentTerm struct {
	Label   string `json:"label"`
	Percent string `json:"percent"`
	Amount  string `json:"amount"`
	DueDate string `json:"dueDate"`
}

// QuotationPayload is crm.quotation_accepted (docs/p3-p4-contracts.md).
type QuotationPayload struct {
	QuotationID        uuid.UUID       `json:"quotationId"`
	Number             string          `json:"number"`
	Version            int             `json:"version"`
	PropertyID         uuid.UUID       `json:"propertyId"`
	OpportunityID      *uuid.UUID      `json:"opportunityId"`
	LeadID             *uuid.UUID      `json:"leadId"`
	CustomerID         *uuid.UUID      `json:"customerId"`
	CorporateAccountID *uuid.UUID      `json:"corporateAccountId"`
	Line               string          `json:"line"`
	EventType          *string         `json:"eventType"`
	EventDate          *string         `json:"eventDate"`
	EndDate            *string         `json:"endDate"`
	Pax                *int            `json:"pax"`
	VenueResourceID    *uuid.UUID      `json:"venueResourceId"`
	PackageRef         *string         `json:"packageRef"`
	Title              string          `json:"title"`
	Currency           string          `json:"currency"`
	Subtotal           string          `json:"subtotal"`
	Discount           string          `json:"discount"`
	Service            string          `json:"service"`
	Tax                string          `json:"tax"`
	Total              string          `json:"total"`
	Lines              []QuotationLine `json:"lines"`
	PaymentTerms       []PaymentTerm   `json:"paymentTerms"`
	AcceptedAt         *time.Time      `json:"acceptedAt"`
	AcceptedVia        string          `json:"acceptedVia"`
}

// SentPayload is crm.quotation_sent.
type SentPayload struct {
	QuotationID        uuid.UUID  `json:"quotationId"`
	Number             string     `json:"number"`
	Version            int        `json:"version"`
	CustomerID         *uuid.UUID `json:"customerId"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	OpportunityID      *uuid.UUID `json:"opportunityId"`
	Line               string     `json:"line"`
	Title              string     `json:"title"`
	EventType          *string    `json:"eventType"`
	EventDate          *string    `json:"eventDate"`
	EndDate            *string    `json:"endDate"`
	Pax                *int       `json:"pax"`
	VenueResourceID    *uuid.UUID `json:"venueResourceId"`
	OptionDate         *string    `json:"optionDate"`
	PackageRef         *string    `json:"packageRef"`
}

// defaultTypes name the event types created on first use.
var defaultTypes = map[string][2]string{
	"WEDDING": {"Wedding", "wedding"}, "BANQUET": {"Banquet", "banquet"}, "MEETING": {"Meeting", "mice"}, "CONFERENCE": {"Conference", "mice"},
	"GATHERING": {"Corporate Gathering", "social"}, "BIRTHDAY": {"Birthday", "social"}, "TOURNAMENT": {"Golf Tournament", "tournament"},
	"OTHER": {"Other Event", "other"},
}

func typeCode(eventType *string, line string) string {
	if eventType != nil && *eventType != "" {
		c := strings.ToUpper(*eventType)
		if _, ok := defaultTypes[c]; ok {
			return c
		}
		return "OTHER"
	}
	switch line {
	case "wedding":
		return "WEDDING"
	case "banquet":
		return "BANQUET"
	case "mice":
		return "MEETING"
	}
	return "GATHERING"
}

// ensureEventType returns the event type of a code, created with the
// default name and category when the club has not configured it.
func ensureEventType(ctx context.Context, tx pgx.Tx, property uuid.UUID, code string) (uuid.UUID, error) {
	var tid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM banquet.event_types WHERE property_id = $1 AND upper(code) = $2 AND archived_at IS NULL AND status = 'active'
		ORDER BY created_at LIMIT 1`, property, code).Scan(&tid)
	if err == nil || !dbtx.IsNoRows(err) {
		return tid, err
	}
	d, ok := defaultTypes[code]
	if !ok {
		d = defaultTypes["OTHER"]
	}
	err = tx.QueryRow(ctx, `INSERT INTO banquet.event_types (id, property_id, code, name, category) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (property_id, code) DO UPDATE SET status = 'active', archived_at = NULL RETURNING id`, id.New(), property, code, d[0], d[1]).Scan(&tid)
	return tid, err
}

func componentOfItem(t string) string {
	switch t {
	case "banquet_package", "package":
		return "banquet_package"
	case "venue":
		return "venue_rental"
	case "product", "banquet_menu":
		return "banquet_fnb"
	case "service":
		return "event_fee"
	}
	return "other"
}

// quotationEvent is the live event of a quotation number (locked).
func quotationEvent(ctx context.Context, tx pgx.Tx, property uuid.UUID, number string) (*uuid.UUID, *time.Time, error) {
	var eid uuid.UUID
	var converted *time.Time
	err := tx.QueryRow(ctx, `SELECT id, converted_at FROM banquet.events WHERE property_id = $1 AND quotation_number = $2 AND status <> 'cancelled'
		FOR UPDATE`, property, number).Scan(&eid, &converted)
	if dbtx.IsNoRows(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &eid, converted, nil
}

// quotationOwner is the sales owner of a quotation (CRM read model).
func quotationOwner(ctx context.Context, q dbtx.Querier, qid uuid.UUID) *uuid.UUID {
	var owner *uuid.UUID
	_ = q.QueryRow(ctx, `SELECT owner_user_id FROM reporting.sales_quotations WHERE quotation_id = $1`, qid).Scan(&owner)
	return owner
}

// quotationPeriod is the event period of a quotation: the default start
// time of Banquet Policies on the event date for the package hours.
func quotationPeriod(ctx context.Context, q dbtx.Querier, pol BookingPolicy, eventDate, endDate *string, hours int) (time.Time, time.Time, bool) {
	loc := calendar.Location(ctx, q)
	day := localDay(clock.Now(), loc).AddDate(0, 0, 30)
	dated := false
	if eventDate != nil {
		if d, err := time.ParseInLocation("2006-01-02", *eventDate, loc); err == nil {
			day, dated = d, true
		}
	}
	at, err := time.Parse("15:04", pol.DefaultStartTime)
	if err != nil {
		at = time.Date(0, 1, 1, 10, 0, 0, 0, time.UTC)
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), at.Hour(), at.Minute(), 0, 0, loc)
	endDay := day
	if endDate != nil {
		if d, err := time.ParseInLocation("2006-01-02", *endDate, loc); err == nil && !d.Before(day) {
			endDay = d
		}
	}
	if hours <= 0 {
		hours = 5
	}
	end := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), at.Hour(), at.Minute(), 0, 0, loc).Add(time.Duration(hours) * time.Hour)
	return start, end, dated
}

// optionUntilDate is the end of the option date (the hold expires when the
// day is over).
func optionUntilDate(ctx context.Context, q dbtx.Querier, d *string) *time.Time {
	if d == nil {
		return nil
	}
	loc := calendar.Location(ctx, q)
	day, err := time.ParseInLocation("2006-01-02", *d, loc)
	if err != nil {
		return nil
	}
	t := day.AddDate(0, 0, 1)
	return &t
}

func packageByRef(ctx context.Context, tx pgx.Tx, property uuid.UUID, ref *string) (*packageRow, error) {
	if ref == nil || strings.TrimSpace(*ref) == "" {
		return nil, nil
	}
	var pid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM banquet.packages WHERE property_id = $1 AND (upper(code) = upper($2) OR id::text = $2) AND archived_at IS NULL`,
		property, strings.TrimSpace(*ref)).Scan(&pid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := packageByID(ctx, tx, property, pid)
	return &p, err
}

func venueOfResource(ctx context.Context, tx pgx.Tx, property uuid.UUID, rid *uuid.UUID) (*uuid.UUID, error) {
	if rid == nil {
		return nil, nil
	}
	var vid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM banquet.venues WHERE property_id = $1 AND (resource_id = $2 OR id = $2) AND archived_at IS NULL`, property,
		*rid).Scan(&vid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &vid, nil
}

// holdOrNote places a hold (waitlisted when the venue is taken); a business
// refusal (capacity, minimum pax …) is noted on the event instead.
func (m *Module) holdOrNote(ctx context.Context, tx pgx.Tx, eid, vid uuid.UUID) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := m.placeHold(ctx, sp, eid, VenueHoldInput{VenueID: vid, Waitlist: true}); err != nil {
		_ = sp.Rollback(ctx)
		de, ok := errs.As(err)
		if !ok || de.Kind == errs.KindInternal {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE banquet.events SET notes = concat_ws('; ', notes, $2::text) WHERE id = $1`, eid, "venue not held: "+de.Message)
		return err
	}
	return sp.Commit(ctx)
}

func eventProperty(p uuid.UUID, ev outbox.Event) uuid.UUID {
	if p == uuid.Nil && ev.PropertyID != nil {
		return *ev.PropertyID
	}
	return p
}

// OnQuotationSent places (or moves) the tentative venue hold of a quotation
// until its option date.
func (m *Module) OnQuotationSent(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p SentPayload
	if err := ev.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	if !contains(QuotationLines, p.Line) || p.EventDate == nil || p.VenueResourceID == nil {
		return nil
	}
	property := eventProperty(uuid.Nil, ev)
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	vid, err := venueOfResource(ctx, tx, property, p.VenueResourceID)
	if err != nil || vid == nil {
		return err
	}
	existing, converted, err := quotationEvent(ctx, tx, property, p.Number)
	if err != nil || converted != nil {
		return err
	}
	pol, _, err := bookingPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	pkg, err := packageByRef(ctx, tx, property, p.PackageRef)
	if err != nil {
		return err
	}
	hours := 0
	if pkg != nil {
		hours = pkg.DurationHours
	}
	start, end, _ := quotationPeriod(ctx, tx, pol, p.EventDate, p.EndDate, hours)
	option := optionUntilDate(ctx, tx, p.OptionDate)
	pax := 0
	if p.Pax != nil {
		pax = max(*p.Pax, 0)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "Event " + p.Number
	}
	var eid uuid.UUID
	action := "quotation_hold"
	if existing == nil {
		tid, err := ensureEventType(ctx, tx, property, typeCode(p.EventType, p.Line))
		if err != nil {
			return err
		}
		e, err := m.createEvent(ctx, tx, property, eventSeed{EventInput: EventInput{Title: title, EventTypeID: tid, CustomerID: p.CustomerID,
			CorporateAccountID: p.CorporateAccountID, SalesOwnerID: quotationOwner(ctx, tx, p.QuotationID), Start: start, End: end, ExpectedPax: pax,
			Notes: "Tentative hold of quotation " + p.Number}, Source: "quotation", QuotationID: &p.QuotationID, QuotationNumber: p.Number,
			OpportunityID: p.OpportunityID})
		if err != nil {
			return err
		}
		eid = e.ID
	} else {
		eid, action = *existing, "quotation_hold_moved"
		e, err := m.Get(ctx, tx, eid)
		if err != nil {
			return err
		}
		same := false
		hs, err := m.holds(ctx, tx, eid)
		if err != nil {
			return err
		}
		for _, h := range hs {
			if (activeHold(h.Status) || h.Status == "waitlisted") && h.VenueID == *vid && h.Start.Equal(start) && h.End.Equal(end) {
				same = true
			}
		}
		if !same {
			for _, h := range hs {
				if activeHold(h.Status) || h.Status == "waitlisted" {
					if err := m.releaseHold(ctx, tx, h, "released", "quotation "+p.Number+" revised"); err != nil {
						return err
					}
				}
			}
			if err := m.afterHoldsChanged(ctx, tx, eid, "quotation revised"); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.events SET title = $2, expected_pax = $3, start_at = $4, end_at = $5, quotation_id = $6, version = version + 1
			WHERE id = $1`, eid, title, pax, start, end, p.QuotationID); err != nil {
			return err
		}
		if same && option != nil {
			if _, err := tx.Exec(ctx, `UPDATE banquet.event_venues SET option_date = $2 WHERE event_id = $1 AND status = 'tentative'`, eid, *option); err != nil {
				return err
			}
			if e.Status == StatusTentative {
				if _, err := tx.Exec(ctx, `UPDATE banquet.events SET option_date = $2 WHERE id = $1`, eid, *option); err != nil {
					return err
				}
			}
		}
		if same {
			option = nil // nothing to place
			vid = nil
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET quotation_version = $2 WHERE id = $1`, eid, p.Version); err != nil {
		return err
	}
	if vid != nil {
		if option != nil {
			if _, err := tx.Exec(ctx, `UPDATE banquet.events SET option_date = $2 WHERE id = $1`, eid, *option); err != nil {
				return err
			}
		}
		if err := m.holdOrNote(ctx, tx, eid, *vid); err != nil {
			return err
		}
	}
	after, err := m.Detail(ctx, tx, eid)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: action, EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: after.Number + " · " + after.Title, PropertyID: &property, After: after.Venues, ActorName: "event:" + QuotationSent,
		Metadata: map[string]any{"quotationId": p.QuotationID, "quotationNumber": p.Number, "version": p.Version, "optionDate": p.OptionDate}})
}

// OnQuotationClosed releases the hold of a rejected or expired quotation:
// the event that only carried the hold is cancelled.
func (m *Module) OnQuotationClosed(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		QuotationID uuid.UUID `json:"quotationId"`
		Number      string    `json:"number"`
	}
	if err := ev.Decode(&p); err != nil || p.Number == "" {
		return nil //nolint:nilerr // foreign payload
	}
	property := eventProperty(uuid.Nil, ev)
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	existing, converted, err := quotationEvent(ctx, tx, property, p.Number)
	if err != nil || existing == nil || converted != nil {
		return err
	}
	e, err := m.Get(ctx, tx, *existing)
	if err != nil {
		return err
	}
	if (e.QuotationID != nil && *e.QuotationID != p.QuotationID) || (e.Status != StatusInquiry && e.Status != StatusTentative) {
		return nil // a newer version holds the venue, or staff moved the event on
	}
	what := "rejected"
	if ev.Type == QuotationExpired {
		what = "expired"
	}
	_, err = m.CancelEvent(ctx, tx, e.ID, EventCancelInput{Reason: "quotation " + p.Number + " " + what})
	return err
}

// OnQuotationAccepted converts an accepted quotation of the wedding,
// banquet, MICE and event lines into an event: the quoted lines become the
// charges (header service & tax spread per line), the payment terms the
// payment schedule, the venue is held, and the event is Tentative until the
// DP is paid (Definite at once without payment terms). Idempotent per
// quotation.
func (m *Module) OnQuotationAccepted(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p QuotationPayload
	if err := ev.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	if !contains(QuotationLines, p.Line) {
		return nil
	}
	property := eventProperty(p.PropertyID, ev)
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	existing, converted, err := quotationEvent(ctx, tx, property, p.Number)
	if err != nil || converted != nil {
		return err
	}
	pol, _, err := bookingPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	pkg, err := packageByRef(ctx, tx, property, p.PackageRef)
	if err != nil {
		return err
	}
	pax := 0
	if p.Pax != nil {
		pax = max(*p.Pax, 0)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "Event " + p.Number
	}
	var eid uuid.UUID
	if existing != nil {
		eid = *existing
		if _, err := tx.Exec(ctx, `UPDATE banquet.events SET title = $2, expected_pax = $3, customer_id = coalesce($4, customer_id),
			corporate_account_id = coalesce($5, corporate_account_id), opportunity_id = coalesce($6, opportunity_id), lead_id = coalesce($7, lead_id),
			quotation_id = $8, version = version + 1 WHERE id = $1`, eid, title, pax, p.CustomerID, p.CorporateAccountID, p.OpportunityID, p.LeadID,
			p.QuotationID); err != nil {
			return err
		}
	} else {
		hours := 0
		if pkg != nil {
			hours = pkg.DurationHours
		}
		start, end, dated := quotationPeriod(ctx, tx, pol, p.EventDate, p.EndDate, hours)
		notes := "Converted from quotation " + p.Number
		if !dated {
			notes += "; the event date is to be confirmed"
		}
		tid, err := ensureEventType(ctx, tx, property, typeCode(p.EventType, p.Line))
		if err != nil {
			return err
		}
		e, err := m.createEvent(ctx, tx, property, eventSeed{EventInput: EventInput{Title: title, EventTypeID: tid, CustomerID: p.CustomerID,
			CorporateAccountID: p.CorporateAccountID, SalesOwnerID: quotationOwner(ctx, tx, p.QuotationID), Start: start, End: end, ExpectedPax: pax,
			Notes: notes}, Source: "quotation", QuotationID: &p.QuotationID, QuotationNumber: p.Number, OpportunityID: p.OpportunityID, LeadID: p.LeadID})
		if err != nil {
			return err
		}
		eid = e.ID
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET converted_at = now(), quotation_version = $2 WHERE id = $1`, eid, p.Version); err != nil {
		return err
	}
	var held int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM banquet.event_venues WHERE event_id = $1 AND status IN ('tentative', 'definite', 'waitlisted')`,
		eid).Scan(&held); err != nil {
		return err
	}
	if held == 0 && p.EventDate != nil {
		vid, err := venueOfResource(ctx, tx, property, p.VenueResourceID)
		if err != nil {
			return err
		}
		if vid != nil {
			if err := m.holdOrNote(ctx, tx, eid, *vid); err != nil {
				return err
			}
		}
	}
	if pkg != nil {
		if _, err := tx.Exec(ctx, `UPDATE banquet.events SET package_id = $2, charged_pax = $3 WHERE id = $1`, eid, pkg.ID, max(pax, pkg.IncludedPax)); err != nil {
			return err
		}
		if err := m.applyInclusions(ctx, tx, eid, pkg.ID); err != nil {
			return err
		}
	}
	if err := m.quotationCharges(ctx, tx, eid, p); err != nil {
		return err
	}
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	if len(p.PaymentTerms) > 0 && dec(e.ContractTotal).IsPositive() {
		lines := termsLines(p.PaymentTerms, dec(e.ContractTotal), places(e.Currency))
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if err := m.createSchedule(ctx, sp, e, lines); err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); !ok || de.Kind == errs.KindInternal {
				return err
			}
			if err := m.createSchedule(ctx, tx, e, nil); err != nil { // Banquet Policies terms
				return err
			}
		} else if err := sp.Commit(ctx); err != nil {
			return err
		}
	} else {
		if _, err := m.makeDefinite(ctx, tx, eid, "quotation "+p.Number+" accepted without down payment"); err != nil {
			if de, ok := errs.As(err); !ok || de.Code != "venue_waitlisted" {
				return err
			}
		}
	}
	after, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "converted", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: after.Number + " · " + after.Title, PropertyID: &property, After: after, ActorName: "event:" + QuotationAccepted,
		Metadata: map[string]any{"quotationId": p.QuotationID, "quotationNumber": p.Number, "version": p.Version}})
}

// PackageBookedPayload is commercial.package_booked (docs/p3-p4-contracts.md).
type PackageBookedPayload struct {
	BookingID   uuid.UUID  `json:"bookingId"`
	Number      string     `json:"number"`
	PackageCode string     `json:"packageCode"`
	CustomerID  *uuid.UUID `json:"customerId"`
	StartDate   string     `json:"startDate"`
	EndDate     string     `json:"endDate"`
	Pax         int        `json:"pax"`
	Components  []struct {
		BookingComponentID uuid.UUID `json:"bookingComponentId"`
		ComponentType      string    `json:"componentType"`
		AllocationRef      *string   `json:"allocationRef"`
		ServiceDate        string    `json:"serviceDate"`
		Quantity           string    `json:"quantity"`
	} `json:"components"`
}

// OnPackageBooked turns the banquet components of a cross-line package
// booking (EP-11) into Definite events — the package booking holds the
// money, the event carries the venue, menu, BEO and operations. Idempotent
// per booking component.
func (m *Module) OnPackageBooked(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p PackageBookedPayload
	if err := ev.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := eventProperty(uuid.Nil, ev)
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	for _, c := range p.Components {
		if c.ComponentType != "banquet" {
			continue
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM banquet.events WHERE package_component_id = $1)`, c.BookingComponentID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		if p.CustomerID == nil {
			return nil
		}
		pol, _, err := bookingPolicy(ctx, tx, property)
		if err != nil {
			return err
		}
		day := c.ServiceDate
		if day == "" {
			day = p.StartDate
		}
		start, end, _ := quotationPeriod(ctx, tx, pol, &day, nil, 0)
		tid, err := ensureEventType(ctx, tx, property, "BANQUET")
		if err != nil {
			return err
		}
		pax := p.Pax
		if q := dec(c.Quantity); q.IsPositive() && q.IntPart() > 1 {
			pax = int(q.IntPart())
		}
		e, err := m.createEvent(ctx, tx, property, eventSeed{EventInput: EventInput{Title: "Package " + p.PackageCode + " · " + p.Number, EventTypeID: tid,
			CustomerID: p.CustomerID, Start: start, End: end, ExpectedPax: pax, Notes: "Banquet of package booking " + p.Number}, Source: "package"})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.events SET package_booking_id = $2, package_component_id = $3 WHERE id = $1`, e.ID, p.BookingID,
			c.BookingComponentID); err != nil {
			return err
		}
		if vid, err := venueOfResource(ctx, tx, property, uuidFromRef(c.AllocationRef)); err != nil {
			return err
		} else if vid != nil {
			if err := m.holdOrNote(ctx, tx, e.ID, *vid); err != nil {
				return err
			}
		}
		if _, err := m.makeDefinite(ctx, tx, e.ID, "package booking "+p.Number); err != nil {
			if de, ok := errs.As(err); !ok || de.Code != "venue_waitlisted" {
				return err
			}
		}
		after, err := m.Get(ctx, tx, e.ID)
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "package_event", EntityType: "banquet.event", EntityID: e.ID.String(),
			EntityLabel: after.Number + " · " + after.Title, PropertyID: &property, After: after, ActorName: "event:" + PackageBooked,
			Metadata: map[string]any{"bookingId": p.BookingID, "bookingComponentId": c.BookingComponentID}}); err != nil {
			return err
		}
	}
	return nil
}

func uuidFromRef(ref *string) *uuid.UUID {
	if ref == nil {
		return nil
	}
	if u, err := uuid.Parse(*ref); err == nil {
		return &u
	}
	return nil
}

// quotationCharges posts the quoted lines with the header discount, service
// and tax spread in proportion (the last line takes the rounding).
func (m *Module) quotationCharges(ctx context.Context, tx pgx.Tx, eid uuid.UUID, p QuotationPayload) error {
	lines := p.Lines
	if len(lines) == 0 {
		lines = []QuotationLine{{ItemType: "other", Description: p.Title, Quantity: "1", Total: p.Subtotal}}
	}
	pl := places(p.Currency)
	sum := decimal.Zero
	for _, l := range lines {
		sum = sum.Add(dec(l.Total))
	}
	total := dec(p.Total)
	if !sum.IsPositive() || !total.IsPositive() {
		return nil
	}
	svc, tax := dec(p.Service), dec(p.Tax)
	net := total.Sub(svc).Sub(tax)
	var accNet, accSvc, accTax decimal.Decimal
	for i, l := range lines {
		share := dec(l.Total).Div(sum)
		n, s, t := net.Mul(share).Round(pl), svc.Mul(share).Round(pl), tax.Mul(share).Round(pl)
		if i == len(lines)-1 {
			n, s, t = net.Sub(accNet), svc.Sub(accSvc), tax.Sub(accTax)
		}
		accNet, accSvc, accTax = accNet.Add(n), accSvc.Add(s), accTax.Add(t)
		qty := dec(l.Quantity)
		if !qty.IsPositive() {
			qty = decimal.NewFromInt(1)
		}
		desc := strings.TrimSpace(l.Description)
		if desc == "" {
			desc = strings.ReplaceAll(l.ItemType, "_", " ")
		}
		if _, err := m.postCharge(ctx, tx, eid, chargeSpec{Source: "quotation", Kind: l.ItemType, Description: desc, Quantity: qty, UnitPrice: dec(l.UnitPrice),
			Mode: "nett", Component: componentOfItem(l.ItemType), Preset: &presetAmounts{Net: n, Service: s, Tax: t, Total: n.Add(s).Add(t)}}); err != nil {
			return err
		}
	}
	return nil
}

// termsLines turns quotation payment terms into schedule lines summing to
// the total (the last term takes the remainder).
func termsLines(terms []PaymentTerm, total decimal.Decimal, pl int32) []billing.ScheduleLineInput {
	out := make([]billing.ScheduleLineInput, 0, len(terms))
	acc := decimal.Zero
	for i, t := range terms {
		amt := dec(t.Amount)
		if !amt.IsPositive() && dec(t.Percent).IsPositive() {
			amt = total.Mul(dec(t.Percent)).Div(decimal.NewFromInt(100)).Round(pl)
		}
		if i == len(terms)-1 {
			amt = total.Sub(acc)
		}
		acc = acc.Add(amt)
		kind := "installment"
		switch {
		case i == 0 && len(terms) > 1:
			kind = "down_payment"
		case i == len(terms)-1:
			kind = "final"
		}
		out = append(out, billing.ScheduleLineInput{Label: t.Label, Kind: kind, Amount: amt.String(), DueDate: t.DueDate})
	}
	return out
}
