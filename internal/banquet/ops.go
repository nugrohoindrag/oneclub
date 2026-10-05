package banquet

// Event operations (PRD P3 EP-12, EP-21 Event Operations): run-of-show,
// checklist with PIC and due date, guest registration with capacity,
// waitlist and QR check-in, food tasting / technical meeting, vendors,
// other resources, incident notes, the event detail, venue calendar and
// availability, and Today's Events.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/sync"
	"oneclub/internal/reservation"
)

// ── run-of-show (FR-EVT-02) ───────────────────────────────────────────────

// ScheduleItem is one line of the run-of-show.
type RundownItem struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	Seq         int        `json:"seq" db:"seq"`
	Start       time.Time  `json:"start" db:"start_at"`
	End         *time.Time `json:"end" db:"end_at"`
	Title       string     `json:"title" db:"title"`
	VenueID     *uuid.UUID `json:"venueId" db:"venue_id"`
	VenueName   *string    `json:"venueName" db:"venue_name"`
	OwnerUserID *uuid.UUID `json:"ownerUserId" db:"owner_user_id"`
	OwnerName   *string    `json:"ownerName" db:"owner_name"`
	Department  *string    `json:"department" db:"department"`
	Notes       *string    `json:"notes" db:"notes"`
}

func (m *Module) schedule(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]RundownItem, error) {
	return handle.List[RundownItem](q.Query(ctx, `SELECT s.id, s.seq, s.start_at, s.end_at, s.title, s.venue_id, v.name AS venue_name, s.owner_user_id,
		coalesce(s.owner_name, u.full_name) AS owner_name, s.department, s.notes FROM banquet.event_schedule s LEFT JOIN banquet.venues v ON v.id = s.venue_id
		LEFT JOIN platform.users u ON u.id = s.owner_user_id WHERE s.event_id = $1 ORDER BY s.seq`, eid))
}

// ScheduleItemInput is one rundown line.
type RundownItemInput struct {
	Start       time.Time  `json:"start"`
	End         *time.Time `json:"end,omitempty"`
	Title       string     `json:"title"`
	VenueID     *uuid.UUID `json:"venueId,omitempty"`
	OwnerUserID *uuid.UUID `json:"ownerUserId,omitempty" doc:"Person in charge"`
	OwnerName   string     `json:"ownerName,omitempty"`
	Department  string     `json:"department,omitempty" enum:"banquet,sales,kitchen,fnb_service,venue,engineering,front_desk,golf,finance,security,housekeeping,other"`
	Notes       string     `json:"notes,omitempty"`
}

// RundownInput replaces the run-of-show.
type RundownInput struct {
	Items []RundownItemInput `json:"items"`
}

// SetSchedule replaces the run-of-show of an event.
func (m *Module) SetSchedule(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in RundownInput) ([]RundownItem, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return nil, err
	}
	if !e.open() {
		return nil, errs.Conflict("event_closed", "a "+e.Status+" event cannot change its schedule")
	}
	before, err := m.schedule(ctx, tx, eid)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM banquet.event_schedule WHERE event_id = $1`, eid); err != nil {
		return nil, err
	}
	for i, it := range in.Items {
		f := fmt.Sprintf("items[%d]", i)
		if strings.TrimSpace(it.Title) == "" {
			return nil, handle.Invalid(f+".title", "required", "title is required")
		}
		if it.Start.IsZero() || (it.End != nil && !it.End.After(it.Start)) {
			return nil, handle.Invalid(f+".end", "invalid_period", "start is required and end must be after start")
		}
		if it.Department != "" && !contains(Departments, it.Department) {
			return nil, handle.Invalid(f+".department", "invalid", "unknown department")
		}
		if it.VenueID != nil {
			if _, err := venueByID(ctx, tx, e.PropertyID, *it.VenueID); err != nil {
				return nil, handle.Invalid(f+".venueId", "not_found", "venue not found")
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_schedule (id, property_id, event_id, seq, start_at, end_at, title, venue_id, owner_user_id,
			owner_name, department, notes, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id.New(), e.PropertyID, eid, i+1, it.Start, it.End,
			strings.TrimSpace(it.Title), it.VenueID, it.OwnerUserID, nzs(it.OwnerName), nzs(it.Department), nzs(it.Notes), actor(ctx)); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return nil, handle.Invalid(f+".ownerUserId", "not_found", "user not found")
			}
			return nil, err
		}
	}
	if err := m.touch(ctx, tx, eid); err != nil {
		return nil, err
	}
	after, err := m.schedule(ctx, tx, eid)
	if err != nil {
		return nil, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "schedule", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number + " · run-of-show", PropertyID: &e.PropertyID, Before: before, After: after})
}

// ── checklist (FR-EVT-07) ─────────────────────────────────────────────────

// ChecklistItem is a task of the event checklist.
type EventChecklistItem struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	EventID        uuid.UUID  `json:"eventId" db:"event_id"`
	TemplateItemID *uuid.UUID `json:"templateItemId" db:"template_item_id"`
	Task           string     `json:"task" db:"task"`
	Department     string     `json:"department" db:"department"`
	OwnerUserID    *uuid.UUID `json:"ownerUserId" db:"owner_user_id"`
	OwnerName      *string    `json:"ownerName" db:"owner_name"`
	DueDate        *string    `json:"dueDate" db:"due_date"`
	Status         string     `json:"status" db:"status" enum:"open,done"`
	DoneAt         *time.Time `json:"doneAt" db:"done_at"`
	DoneBy         *string    `json:"doneBy" db:"done_by"`
	Notes          *string    `json:"notes" db:"notes"`
	Overdue        bool       `json:"overdue" db:"overdue"`
}

const checklistSelect = `SELECT c.id, c.event_id, c.template_item_id, c.task, c.department, c.owner_user_id, coalesce(c.owner_name, u.full_name) AS owner_name,
	to_char(c.due_date, 'YYYY-MM-DD') AS due_date, c.status, c.done_at, d.full_name AS done_by, c.notes,
	(c.status = 'open' AND c.due_date < $2::date) AS overdue
	FROM banquet.event_checklist_items c LEFT JOIN platform.users u ON u.id = c.owner_user_id LEFT JOIN platform.users d ON d.id = c.done_by`

func today(ctx context.Context, q dbtx.Querier) string {
	return clock.Now().In(calendar.Location(ctx, q)).Format("2006-01-02")
}

// Checklist returns the checklist of an event.
func (m *Module) Checklist(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventChecklistItem, error) {
	return handle.List[EventChecklistItem](q.Query(ctx, checklistSelect+` WHERE c.event_id = $1 ORDER BY c.due_date NULLS LAST, c.sort_order, c.task`, eid, today(ctx, q)))
}

func (m *Module) checklistItem(ctx context.Context, q dbtx.Querier, cid uuid.UUID) (EventChecklistItem, error) {
	rows, err := q.Query(ctx, checklistSelect+` WHERE c.id = $1`, cid, today(ctx, q))
	return handle.One[EventChecklistItem](rows, err, "checklist item")
}

// applyTemplates copies template tasks (one template, or every active
// template of the event type) with due dates before the event.
func (m *Module) applyTemplates(ctx context.Context, tx pgx.Tx, eid uuid.UUID, template *uuid.UUID) error {
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	loc := calendar.Location(ctx, tx)
	day := localDay(e.Start, loc)
	_, err = tx.Exec(ctx, `INSERT INTO banquet.event_checklist_items (id, property_id, event_id, template_item_id, task, department, due_date, sort_order, created_by)
		SELECT gen_random_uuid(), $1, $2, i.id, i.task, i.department, $3::date - i.days_before, i.sort_order, $5
		FROM banquet.checklist_template_items i JOIN banquet.checklist_templates t ON t.id = i.template_id
		WHERE t.property_id = $1 AND t.status = 'active' AND t.archived_at IS NULL AND (($4::uuid IS NOT NULL AND t.id = $4) OR ($4::uuid IS NULL AND t.event_type_id = $6))
		ON CONFLICT (event_id, template_item_id) DO NOTHING`, e.PropertyID, eid, day, template, actor(ctx), e.EventTypeID)
	return err
}

// ApplyTemplateInput applies a checklist template.
type ChecklistTemplateApply struct {
	TemplateID uuid.UUID `json:"templateId"`
}

// ApplyTemplate adds the tasks of a checklist template to an event.
func (m *Module) ApplyTemplate(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in ChecklistTemplateApply) ([]EventChecklistItem, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM banquet.checklist_templates WHERE id = $1 AND property_id = $2 AND status = 'active')`,
		in.TemplateID, e.PropertyID).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, handle.Invalid("templateId", "not_found", "checklist template not found")
	}
	if err := m.applyTemplates(ctx, tx, eid, &in.TemplateID); err != nil {
		return nil, err
	}
	list, err := m.Checklist(ctx, tx, eid)
	if err != nil {
		return nil, err
	}
	return list, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "apply_checklist_template", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number, PropertyID: &e.PropertyID, Metadata: map[string]any{"templateId": in.TemplateID, "tasks": len(list)}})
}

// ChecklistInput adds or edits a task.
type EventChecklistInput struct {
	Task        string     `json:"task,omitempty"`
	Department  string     `json:"department,omitempty" enum:"banquet,sales,kitchen,fnb_service,venue,engineering,front_desk,golf,finance,security,housekeeping,other"`
	OwnerUserID *uuid.UUID `json:"ownerUserId,omitempty"`
	OwnerName   string     `json:"ownerName,omitempty"`
	DueDate     string     `json:"dueDate,omitempty" doc:"YYYY-MM-DD"`
	Notes       string     `json:"notes,omitempty"`
}

func parseDay(field, v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, handle.Invalid(field, "invalid_date", "YYYY-MM-DD")
	}
	return &d, nil
}

// AddChecklistItem adds a task to the checklist.
func (m *Module) AddChecklistItem(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventChecklistInput) (EventChecklistItem, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventChecklistItem{}, err
	}
	if err := handle.Required("task", in.Task); err != nil {
		return EventChecklistItem{}, err
	}
	dept := in.Department
	if dept == "" {
		dept = "banquet"
	}
	if !contains(Departments, dept) {
		return EventChecklistItem{}, handle.Invalid("department", "invalid", "unknown department")
	}
	due, err := parseDay("dueDate", in.DueDate)
	if err != nil {
		return EventChecklistItem{}, err
	}
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_checklist_items (id, property_id, event_id, task, department, owner_user_id, owner_name, due_date, notes,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, cid, e.PropertyID, eid, strings.TrimSpace(in.Task), dept, in.OwnerUserID,
		nzs(in.OwnerName), due, nzs(in.Notes), actor(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return EventChecklistItem{}, handle.Invalid("ownerUserId", "not_found", "user not found")
		}
		return EventChecklistItem{}, err
	}
	it, err := m.checklistItem(ctx, tx, cid)
	if err != nil {
		return it, err
	}
	return it, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionCreate, EntityType: "banquet.checklist_item", EntityID: cid.String(),
		EntityLabel: e.Number + " · " + it.Task, PropertyID: &e.PropertyID, After: it})
}

// UpdateChecklistItem changes the task, PIC or due date.
func (m *Module) UpdateChecklistItem(ctx context.Context, tx pgx.Tx, cid uuid.UUID, in EventChecklistInput) (EventChecklistItem, error) {
	before, err := m.checklistItem(ctx, tx, cid)
	if err != nil {
		return before, err
	}
	if in.Department != "" && !contains(Departments, in.Department) {
		return before, handle.Invalid("department", "invalid", "unknown department")
	}
	due, err := parseDay("dueDate", in.DueDate)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_checklist_items SET task = coalesce($2, task), department = coalesce($3, department),
		owner_user_id = coalesce($4, owner_user_id), owner_name = coalesce($5, owner_name), due_date = coalesce($6, due_date), notes = coalesce($7, notes),
		reminded_at = CASE WHEN $6::date IS NULL THEN reminded_at END, updated_by = $8 WHERE id = $1`, cid, nzs(strings.TrimSpace(in.Task)), nzs(in.Department),
		in.OwnerUserID, nzs(in.OwnerName), due, nzs(in.Notes), actor(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return before, handle.Invalid("ownerUserId", "not_found", "user not found")
		}
		return before, err
	}
	after, err := m.checklistItem(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionUpdate, EntityType: "banquet.checklist_item", EntityID: cid.String(),
		EntityLabel: after.Task, Before: before, After: after})
}

// ToggleInput marks a task done or open.
type ChecklistToggleInput struct {
	Done  bool   `json:"done"`
	Notes string `json:"notes,omitempty"`
}

// ToggleChecklistItem completes or reopens a task.
func (m *Module) ToggleChecklistItem(ctx context.Context, tx pgx.Tx, cid uuid.UUID, in ChecklistToggleInput) (EventChecklistItem, error) {
	before, err := m.checklistItem(ctx, tx, cid)
	if err != nil {
		return before, err
	}
	status := "open"
	if in.Done {
		status = "done"
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_checklist_items SET status = $2, done_at = CASE WHEN $2 = 'done' THEN now() END,
		done_by = CASE WHEN $2 = 'done' THEN $3::uuid END, notes = coalesce($4, notes), updated_by = $3 WHERE id = $1`, cid, status, actor(ctx), nzs(in.Notes)); err != nil {
		return before, err
	}
	after, err := m.checklistItem(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionStatusChange, EntityType: "banquet.checklist_item",
		EntityID: cid.String(), EntityLabel: after.Task, Before: before, After: after})
}

// ── participants (FR-EVT-04) ──────────────────────────────────────────────

// Participant is a registered guest of an event.
type EventParticipant struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	EventID       uuid.UUID  `json:"eventId" db:"event_id"`
	EventNumber   string     `json:"eventNumber" db:"event_number"`
	EventTitle    string     `json:"eventTitle" db:"event_title"`
	EventStart    time.Time  `json:"eventStart" db:"event_start"`
	CustomerID    *uuid.UUID `json:"customerId" db:"customer_id"`
	Name          string     `json:"name" db:"name"`
	Email         *string    `json:"email" db:"email"`
	Phone         *string    `json:"phone" db:"phone"`
	Company       *string    `json:"company" db:"company"`
	PartySize     int        `json:"partySize" db:"party_size"`
	TicketCode    string     `json:"ticketCode" db:"ticket_code" doc:"QR content for the check-in"`
	Status        string     `json:"status" db:"status" enum:"registered,waitlisted,withdrawn,checked_in"`
	WaitlistRank  *int       `json:"waitlistRank" db:"waitlist_rank"`
	Source        string     `json:"source" db:"source" enum:"staff,import,website,member_app"`
	Fee           string     `json:"fee" db:"fee"`
	PaymentStatus string     `json:"paymentStatus" db:"payment_status" enum:"none,unpaid,paid,refunded"`
	FolioID       *uuid.UUID `json:"folioId" db:"folio_id"`
	TableNo       *string    `json:"tableNo" db:"table_no"`
	VIP           bool       `json:"vip" db:"vip"`
	DietaryNotes  *string    `json:"dietaryNotes" db:"dietary_notes"`
	RegisteredAt  time.Time  `json:"registeredAt" db:"registered_at"`
	PromotedAt    *time.Time `json:"promotedAt" db:"promoted_at"`
	WithdrawnAt   *time.Time `json:"withdrawnAt" db:"withdrawn_at"`
	CheckedInAt   *time.Time `json:"checkedInAt" db:"checked_in_at"`
}

const participantSelect = `SELECT p.id, p.event_id, e.number AS event_number, e.title AS event_title, e.start_at AS event_start, p.customer_id, p.name, p.email,
	p.phone, p.company, p.party_size, p.ticket_code, p.status, p.waitlist_rank, p.source, trim_scale(p.fee)::text AS fee, p.payment_status, p.folio_id,
	p.table_no, p.vip, p.dietary_notes, p.registered_at, p.promoted_at, p.withdrawn_at, p.checked_in_at
	FROM banquet.participants p JOIN banquet.events e ON e.id = p.event_id`

func (m *Module) participant(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (EventParticipant, error) {
	rows, err := q.Query(ctx, participantSelect+` WHERE p.id = $1`, pid)
	return handle.One[EventParticipant](rows, err, "participant")
}

// Participants lists the guests of an event.
func (m *Module) Participants(ctx context.Context, q dbtx.Querier, eid uuid.UUID, status, text string) ([]EventParticipant, error) {
	return handle.List[EventParticipant](q.Query(ctx, participantSelect+` WHERE p.event_id = $1 AND ($2 = '' OR p.status = ANY(string_to_array($2, ',')))
		AND ($3 = '' OR p.name ILIKE '%' || $3 || '%' OR p.ticket_code = upper($3) OR p.company ILIKE '%' || $3 || '%')
		ORDER BY p.status = 'waitlisted', p.waitlist_rank NULLS FIRST, p.registered_at`, eid, status, text))
}

// ParticipantInput registers a guest.
type EventParticipantInput struct {
	CustomerID   *uuid.UUID         `json:"customerId,omitempty"`
	Name         string             `json:"name,omitempty"`
	Email        string             `json:"email,omitempty"`
	Phone        string             `json:"phone,omitempty"`
	Company      string             `json:"company,omitempty"`
	PartySize    int                `json:"partySize,omitempty" doc:"Seats; default 1"`
	TableNo      string             `json:"tableNo,omitempty"`
	VIP          bool               `json:"vip,omitempty"`
	DietaryNotes string             `json:"dietaryNotes,omitempty"`
	Payment      *EventPaymentInput `json:"payment,omitempty" doc:"Paid events: take the fee now"`
}

// ParticipantStats summarise the registration of an event.
type EventParticipantStats struct {
	Capacity   *int `json:"capacity"`
	Seats      int  `json:"seats" doc:"Seats taken (registered + checked in)"`
	Registered int  `json:"registered"`
	Waitlisted int  `json:"waitlisted"`
	CheckedIn  int  `json:"checkedIn"`
	Withdrawn  int  `json:"withdrawn"`
}

func (m *Module) participantStats(ctx context.Context, q dbtx.Querier, e BanquetEvent) (EventParticipantStats, error) {
	s := EventParticipantStats{Capacity: e.Capacity}
	err := q.QueryRow(ctx, `SELECT coalesce(sum(party_size) FILTER (WHERE status IN ('registered', 'checked_in')), 0),
		count(*) FILTER (WHERE status = 'registered'), count(*) FILTER (WHERE status = 'waitlisted'), count(*) FILTER (WHERE status = 'checked_in'),
		count(*) FILTER (WHERE status = 'withdrawn') FROM banquet.participants WHERE event_id = $1`, e.ID).
		Scan(&s.Seats, &s.Registered, &s.Waitlisted, &s.CheckedIn, &s.Withdrawn)
	return s, err
}

func (m *Module) seatsTaken(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT coalesce(sum(party_size), 0) FROM banquet.participants WHERE event_id = $1 AND status IN ('registered', 'checked_in')`, eid).Scan(&n)
	return n, err
}

// register adds a participant: registered while seats are left, otherwise
// waitlisted (Event Policies); paid events get a folio with the fee.
func (m *Module) register(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventParticipantInput, source string) (EventParticipant, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventParticipant{}, err
	}
	if !e.open() || !e.End.After(clock.Now()) {
		return EventParticipant{}, errs.Conflict("registration_closed", "registration for this event is closed")
	}
	pol, _, err := registrationPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return EventParticipant{}, err
	}
	if source == "website" || source == "member_app" {
		closes := e.Start.Add(-time.Duration(pol.RegistrationClosesHoursBefore) * time.Hour)
		if e.RegistrationClosesAt != nil {
			closes = *e.RegistrationClosesAt
		}
		if !e.Public || !e.RegistrationOpen || !clock.Now().Before(closes) {
			return EventParticipant{}, errs.Conflict("registration_closed", "registration for this event is not open")
		}
		if e.MembersOnly {
			member := false
			if in.CustomerID != nil {
				if member, err = membership.IsActive(ctx, tx, e.PropertyID, *in.CustomerID, ""); err != nil {
					return EventParticipant{}, err
				}
			}
			if !member {
				return EventParticipant{}, errs.Forbidden("this event is for members only")
			}
		}
	}
	party := in.PartySize
	if party <= 0 {
		party = 1
	}
	if pol.MaxPartySize > 0 && party > pol.MaxPartySize {
		return EventParticipant{}, handle.Invalid("partySize", "too_large", fmt.Sprintf("at most %d seats per registration", pol.MaxPartySize))
	}
	name, email, phone := strings.TrimSpace(in.Name), in.Email, in.Phone
	if in.CustomerID != nil {
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return EventParticipant{}, handle.Invalid("customerId", "not_found", "customer not found")
		}
		if name == "" {
			name = c.Name
		}
		if email == "" {
			email = c.Email
		}
		if phone == "" {
			phone = c.Phone
		}
	}
	if name == "" {
		return EventParticipant{}, handle.Invalid("name", "required", "the guest name is required")
	}
	var dup bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM banquet.participants WHERE event_id = $1 AND status IN ('registered', 'waitlisted', 'checked_in')
		AND (($2::uuid IS NOT NULL AND customer_id = $2) OR ($3 <> '' AND lower(email) = lower($3))))`, eid, in.CustomerID, email).Scan(&dup); err != nil {
		return EventParticipant{}, err
	}
	if dup {
		return EventParticipant{}, errs.Conflict("already_registered", name+" is already registered for this event")
	}
	status := "registered"
	var rank *int
	if e.Capacity != nil {
		taken, err := m.seatsTaken(ctx, tx, eid)
		if err != nil {
			return EventParticipant{}, err
		}
		if taken+party > *e.Capacity {
			if !pol.WaitlistEnabled {
				return EventParticipant{}, errs.Conflict("event_full", "the event is full")
			}
			status = "waitlisted"
			var n int
			if err := tx.QueryRow(ctx, `SELECT coalesce(max(waitlist_rank), 0) + 1 FROM banquet.participants WHERE event_id = $1 AND status = 'waitlisted'`, eid).Scan(&n); err != nil {
				return EventParticipant{}, err
			}
			rank = &n
		}
	}
	pid := id.New()
	fee := dec(e.RegistrationFee).Mul(decimal.NewFromInt(int64(party)))
	payStatus := "none"
	if fee.IsPositive() {
		payStatus = "unpaid"
	}
	code := ticketCode()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.participants (id, property_id, event_id, customer_id, name, email, phone, company, party_size, ticket_code,
		status, waitlist_rank, source, fee, payment_status, table_no, vip, dietary_notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::numeric,$15,$16,$17,$18,$19,$19)`, pid, e.PropertyID, eid, in.CustomerID, name, nzs(email),
		nzs(phone), nzs(in.Company), party, code, status, rank, source, fee.String(), payStatus, nzs(in.TableNo), in.VIP, nzs(in.DietaryNotes), actor(ctx)); err != nil {
		return EventParticipant{}, err
	}
	if status == "registered" && fee.IsPositive() {
		if err := m.chargeRegistration(ctx, tx, e, pid, in.Payment); err != nil {
			return EventParticipant{}, err
		}
	}
	p, err := m.participant(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	if err := m.notifyParticipant(ctx, tx, e, p, "banquet.registration_confirmed"); err != nil {
		return p, err
	}
	return p, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "register", EntityType: "banquet.participant", EntityID: pid.String(),
		EntityLabel: e.Number + " · " + name, PropertyID: &e.PropertyID, After: p, ActorName: sysActor(ctx)})
}

// chargeRegistration opens the guest's folio with the fee (and takes the
// payment when given).
func (m *Module) chargeRegistration(ctx context.Context, tx pgx.Tx, e BanquetEvent, pid uuid.UUID, pay *EventPaymentInput) error {
	p, err := m.participant(ctx, tx, pid)
	if err != nil {
		return err
	}
	fee := dec(p.Fee)
	if !fee.IsPositive() {
		return nil
	}
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: e.PropertyID, CustomerID: p.CustomerID,
		HolderName: p.Name, SourceType: "banquet_event", SourceID: &pid, SourceRef: e.Number + " registration " + p.TicketCode}, BusinessLine: billing.LineBanquet})
	if err != nil {
		return err
	}
	if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: f.ID, Description: fmt.Sprintf("%s · registration × %d",
		e.Title, p.PartySize), Quantity: decimal.NewFromInt(int64(p.PartySize)), Net: fee, Total: fee, ReferenceType: "banquet.participant", ReferenceID: &pid},
		BusinessLine: billing.LineBanquet, RevenueComponent: "event_fee"}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.participants SET folio_id = $2 WHERE id = $1`, pid, f.ID); err != nil {
		return err
	}
	if pay != nil {
		method := pay.MethodType
		if method == "" {
			method = "cash"
		}
		pm, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: &f.ID, MethodType: method, Amount: fee, Reference: pay.Reference,
			Description: "Registration " + p.TicketCode})
		if err != nil {
			return err
		}
		if pm.Status == "completed" {
			return m.refreshPayment(ctx, tx, pid)
		}
	}
	return nil
}

// refreshPayment marks a participant paid once the folio is settled.
func (m *Module) refreshPayment(ctx context.Context, tx pgx.Tx, pid uuid.UUID) error {
	p, err := m.participant(ctx, tx, pid)
	if err != nil || p.FolioID == nil || p.PaymentStatus != "unpaid" {
		return err
	}
	sum, err := billing.FolioSummary(ctx, tx, *p.FolioID)
	if err != nil {
		return err
	}
	if dec(sum.Balance).IsPositive() {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.participants SET payment_status = 'paid' WHERE id = $1`, pid); err != nil {
		return err
	}
	return m.tryClose(ctx, tx, *p.FolioID)
}

// RegisterParticipant registers a guest by staff (Guest Registration).
func (m *Module) RegisterParticipant(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventParticipantInput) (EventParticipant, error) {
	return m.register(ctx, tx, eid, in, "staff")
}

// ImportInput registers a guest list.
type GuestListImport struct {
	Participants []EventParticipantInput `json:"participants"`
}

// ImportResult reports an import.
type GuestListImportResult struct {
	Registered int                `json:"registered"`
	Waitlisted int                `json:"waitlisted"`
	Skipped    []string           `json:"skipped" doc:"Rows not imported with the reason"`
	Items      []EventParticipant `json:"items"`
}

// ImportParticipants registers a guest list (duplicates are skipped).
func (m *Module) ImportParticipants(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in GuestListImport) (GuestListImportResult, error) {
	out := GuestListImportResult{Skipped: []string{}, Items: []EventParticipant{}}
	if len(in.Participants) == 0 {
		return out, handle.Invalid("participants", "required", "the guest list is empty")
	}
	if len(in.Participants) > 2000 {
		return out, handle.Invalid("participants", "too_many", "at most 2000 guests per import")
	}
	for i, p := range in.Participants {
		p.Payment = nil
		sp, err := tx.Begin(ctx)
		if err != nil {
			return out, err
		}
		r, err := m.register(ctx, sp, eid, p, "import")
		if err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal && de.Kind != errs.KindNotFound {
				out.Skipped = append(out.Skipped, fmt.Sprintf("row %d (%s): %s", i+1, p.Name, de.Message))
				continue
			}
			return out, err
		}
		if err := sp.Commit(ctx); err != nil {
			return out, err
		}
		if r.Status == "waitlisted" {
			out.Waitlisted++
		} else {
			out.Registered++
		}
		out.Items = append(out.Items, r)
	}
	var property uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM banquet.events WHERE id = $1`, eid).Scan(&property)
	return out, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionImport, EntityType: "banquet.participant", EntityID: eid.String(),
		EntityLabel: "guest list", PropertyID: &property, Metadata: map[string]any{"registered": out.Registered, "waitlisted": out.Waitlisted,
			"skipped": len(out.Skipped)}})
}

// WithdrawInput withdraws a registration.
type RegistrationWithdrawInput struct {
	Reason string `json:"reason,omitempty"`
}

// withdraw cancels a registration: the paid fee is refunded per Event
// Policies and the waitlist moves up automatically.
func (m *Module) withdraw(ctx context.Context, tx pgx.Tx, pid uuid.UUID, reason string, byGuest bool) (EventParticipant, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM banquet.participants WHERE id = $1 FOR UPDATE`, pid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return EventParticipant{}, errs.NotFound("participant")
		}
		return EventParticipant{}, err
	}
	p, err := m.participant(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	if p.Status != "registered" && p.Status != "waitlisted" {
		return p, errs.Conflict("invalid_status", "a "+p.Status+" registration cannot be withdrawn")
	}
	e, err := m.Get(ctx, tx, p.EventID)
	if err != nil {
		return p, err
	}
	pol, _, err := registrationPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return p, err
	}
	if byGuest && clock.Now().After(e.Start.Add(-time.Duration(pol.WithdrawDeadlineHours)*time.Hour)) {
		return p, errs.Conflict("withdraw_closed", fmt.Sprintf("registrations can be withdrawn until %d hours before the event", pol.WithdrawDeadlineHours))
	}
	if reason == "" {
		reason = "withdrawn"
	}
	payStatus := p.PaymentStatus
	if p.FolioID != nil {
		if st, err := billing.FolioStatus(ctx, tx, *p.FolioID); err != nil {
			return p, err
		} else if st == "closed" {
			if err := m.Billing.ReopenFolio(ctx, tx, *p.FolioID, "registration withdrawn"); err != nil {
				return p, err
			}
		}
		kept := decimal.Zero
		if p.PaymentStatus == "paid" {
			kept = dec(p.Fee).Mul(decimal.NewFromInt(100).Sub(dec(pol.WithdrawRefundPercent))).Div(decimal.NewFromInt(100)).Round(0)
		}
		refunded, err := m.Billing.SettleCancellation(ctx, tx, *p.FolioID, kept, billing.LineBanquet, "registration withdrawn: "+reason)
		if err != nil {
			return p, err
		}
		if refunded.IsPositive() {
			payStatus = "refunded"
		}
		if err := m.tryClose(ctx, tx, *p.FolioID); err != nil {
			return p, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.participants SET status = 'withdrawn', withdrawn_at = now(), waitlist_rank = NULL, payment_status = $2,
		updated_by = $3 WHERE id = $1`, pid, payStatus, actor(ctx)); err != nil {
		return p, err
	}
	if p.Status == "registered" {
		if err := m.promoteParticipants(ctx, tx, e); err != nil {
			return p, err
		}
	}
	after, err := m.participant(ctx, tx, pid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "withdraw", EntityType: "banquet.participant", EntityID: pid.String(),
		EntityLabel: e.Number + " · " + p.Name, PropertyID: &e.PropertyID, Before: p, After: after, Reason: reason})
}

// Withdraw withdraws a registration (staff).
func (m *Module) Withdraw(ctx context.Context, tx pgx.Tx, pid uuid.UUID, in RegistrationWithdrawInput) (EventParticipant, error) {
	return m.withdraw(ctx, tx, pid, in.Reason, false)
}

// promoteParticipants registers waitlisted guests while seats are left.
func (m *Module) promoteParticipants(ctx context.Context, tx pgx.Tx, e BanquetEvent) error {
	pol, _, err := registrationPolicy(ctx, tx, e.PropertyID)
	if err != nil || !pol.AutoPromoteWaitlist || e.Capacity == nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM banquet.participants WHERE event_id = $1 AND status = 'waitlisted' ORDER BY waitlist_rank NULLS LAST, registered_at`, e.ID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, wid := range ids {
		w, err := m.participant(ctx, tx, wid)
		if err != nil {
			return err
		}
		taken, err := m.seatsTaken(ctx, tx, e.ID)
		if err != nil {
			return err
		}
		if taken+w.PartySize > *e.Capacity {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.participants SET status = 'registered', waitlist_rank = NULL, promoted_at = now() WHERE id = $1`, wid); err != nil {
			return err
		}
		if dec(w.Fee).IsPositive() && w.FolioID == nil {
			if err := m.chargeRegistration(ctx, tx, e, wid, nil); err != nil {
				return err
			}
		}
		after, err := m.participant(ctx, tx, wid)
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "waitlist_promoted", EntityType: "banquet.participant", EntityID: wid.String(),
			EntityLabel: e.Number + " · " + w.Name, PropertyID: &e.PropertyID, Before: w, After: after, ActorName: sysActor(ctx)}); err != nil {
			return err
		}
		if err := m.notifyParticipant(ctx, tx, e, after, "banquet.registration_promoted"); err != nil {
			return err
		}
	}
	return nil
}

// withdrawAll withdraws every registration of a cancelled event (fees
// refunded in full).
func (m *Module) withdrawAll(ctx context.Context, tx pgx.Tx, e BanquetEvent, reason string) error {
	rows, err := tx.Query(ctx, `SELECT id, folio_id FROM banquet.participants WHERE event_id = $1 AND status IN ('registered', 'waitlisted')`, e.ID)
	if err != nil {
		return err
	}
	type row struct {
		id    uuid.UUID
		folio *uuid.UUID
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.folio); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		ps := "none"
		if r.folio != nil {
			if st, err := billing.FolioStatus(ctx, tx, *r.folio); err != nil {
				return err
			} else if st == "closed" {
				if err := m.Billing.ReopenFolio(ctx, tx, *r.folio, reason); err != nil {
					return err
				}
			}
			refunded, err := m.Billing.SettleCancellation(ctx, tx, *r.folio, decimal.Zero, billing.LineBanquet, reason)
			if err != nil {
				return err
			}
			ps = "unpaid"
			if refunded.IsPositive() {
				ps = "refunded"
			}
			if err := m.tryClose(ctx, tx, *r.folio); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.participants SET status = 'withdrawn', withdrawn_at = now(), waitlist_rank = NULL,
			payment_status = CASE WHEN payment_status = 'none' THEN 'none' ELSE $2 END WHERE id = $1`, r.id, ps); err != nil {
			return err
		}
	}
	return nil
}

// CheckInInput checks a guest in (QR code or participant id).
type EventCheckInInput struct {
	Code    string             `json:"code,omitempty" doc:"Ticket code (QR)"`
	Payment *EventPaymentInput `json:"payment,omitempty" doc:"Collect an unpaid fee at the door"`
}

// CheckInResult is the check-in answer for the door.
type EventCheckInResult struct {
	Participant      EventParticipant `json:"participant"`
	AlreadyCheckedIn bool             `json:"alreadyCheckedIn"`
}

func (m *Module) checkIn(ctx context.Context, tx pgx.Tx, p EventParticipant, pay *EventPaymentInput) (EventCheckInResult, error) {
	e, err := m.Get(ctx, tx, p.EventID)
	if err != nil {
		return EventCheckInResult{}, err
	}
	if p.Status == "checked_in" {
		return EventCheckInResult{Participant: p, AlreadyCheckedIn: true}, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "check_in_repeat",
			EntityType: "banquet.participant", EntityID: p.ID.String(), EntityLabel: e.Number + " · " + p.Name, PropertyID: &e.PropertyID})
	}
	if p.Status != "registered" {
		return EventCheckInResult{}, errs.Conflict("not_registered", p.Name+" is "+p.Status+", not registered")
	}
	if e.Status == StatusCancelled || e.Status == StatusCompleted {
		return EventCheckInResult{}, errs.Conflict("event_closed", "the event is "+e.Status)
	}
	pol, _, err := registrationPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return EventCheckInResult{}, err
	}
	if clock.Now().Before(e.Start.Add(-time.Duration(pol.CheckInOpensMinutesBefore)*time.Minute)) || clock.Now().After(e.End) {
		return EventCheckInResult{}, errs.Conflict("check_in_closed", "check-in opens "+fmt.Sprint(pol.CheckInOpensMinutesBefore)+" minutes before the event")
	}
	if pay != nil && p.PaymentStatus == "unpaid" && p.FolioID != nil {
		method := pay.MethodType
		if method == "" {
			method = "cash"
		}
		if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: p.FolioID, MethodType: method, Amount: dec(p.Fee), Reference: pay.Reference,
			Description: "Registration " + p.TicketCode}); err != nil {
			return EventCheckInResult{}, err
		}
		if err := m.refreshPayment(ctx, tx, p.ID); err != nil {
			return EventCheckInResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.participants SET status = 'checked_in', checked_in_at = now(), checked_in_by = $2 WHERE id = $1`, p.ID, actor(ctx)); err != nil {
		return EventCheckInResult{}, err
	}
	after, err := m.participant(ctx, tx, p.ID)
	if err != nil {
		return EventCheckInResult{}, err
	}
	return EventCheckInResult{Participant: after}, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "check_in", EntityType: "banquet.participant",
		EntityID: p.ID.String(), EntityLabel: e.Number + " · " + p.Name, PropertyID: &e.PropertyID, Before: p, After: after})
}

// CheckInParticipant checks a guest in by id.
func (m *Module) CheckInParticipant(ctx context.Context, tx pgx.Tx, pid uuid.UUID, in EventCheckInInput) (EventCheckInResult, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM banquet.participants WHERE id = $1 FOR UPDATE`, pid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return EventCheckInResult{}, errs.NotFound("participant")
		}
		return EventCheckInResult{}, err
	}
	p, err := m.participant(ctx, tx, pid)
	if err != nil {
		return EventCheckInResult{}, err
	}
	return m.checkIn(ctx, tx, p, in.Payment)
}

// CheckInByCode checks a guest in by the ticket code (QR scan).
func (m *Module) CheckInByCode(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventCheckInInput) (EventCheckInResult, error) {
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" {
		return EventCheckInResult{}, handle.Invalid("code", "required", "scan the ticket code")
	}
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM banquet.participants WHERE event_id = $1 AND ticket_code = $2 FOR UPDATE`, eid, code).Scan(&pid); err != nil {
		if dbtx.IsNoRows(err) {
			return EventCheckInResult{}, errs.NotFound("ticket")
		}
		return EventCheckInResult{}, err
	}
	p, err := m.participant(ctx, tx, pid)
	if err != nil {
		return EventCheckInResult{}, err
	}
	return m.checkIn(ctx, tx, p, in.Payment)
}

// SyncCheckIn is the offline Event Check-in action (FR-OPS-P3-05): a
// queued scan is applied once; a withdrawn ticket becomes a conflict.
func (m *Module) SyncCheckIn(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in struct {
		EventID uuid.UUID `json:"eventId"`
		Code    string    `json:"code"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, errs.Validation("invalid_payload", "eventId and code are required")
	}
	property := handle.Property(ctx)
	if p := authz.From(ctx); p == nil || !p.Can("banquet.participant.check_in", &property) {
		return nil, errs.Forbidden("Event Check-in is not allowed for this user")
	}
	r, err := m.CheckInByCode(ctx, tx, in.EventID, EventCheckInInput{Code: in.Code})
	if de, ok := errs.As(err); ok && de.Kind == errs.KindConflict {
		return nil, &sync.ConflictError{Message: de.Message}
	}
	return r, err
}

// ── food tasting & technical meeting (FR-BQT-09) ──────────────────────────

// Meeting is a food tasting, technical meeting or site visit.
type EventMeeting struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	EventID     uuid.UUID  `json:"eventId" db:"event_id"`
	Kind        string     `json:"kind" db:"kind" enum:"food_tasting,technical_meeting,site_visit"`
	ScheduledAt time.Time  `json:"scheduledAt" db:"scheduled_at"`
	Location    *string    `json:"location" db:"location"`
	Attendees   []string   `json:"attendees" db:"attendees"`
	Status      string     `json:"status" db:"status" enum:"scheduled,done,cancelled"`
	Outcome     *string    `json:"outcome" db:"outcome"`
	Changes     []string   `json:"changes" db:"changes" doc:"Changes carried into the next BEO"`
	RecordedAt  *time.Time `json:"recordedAt" db:"recorded_at"`
}

const meetingSelect = `SELECT id, event_id, kind, scheduled_at, location, attendees, status, outcome, changes, recorded_at FROM banquet.event_meetings`

func (m *Module) meetings(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventMeeting, error) {
	return handle.List[EventMeeting](q.Query(ctx, meetingSelect+` WHERE event_id = $1 ORDER BY scheduled_at`, eid))
}

// MeetingInput schedules a meeting.
type EventMeetingInput struct {
	Kind        string    `json:"kind" enum:"food_tasting,technical_meeting,site_visit"`
	ScheduledAt time.Time `json:"scheduledAt"`
	Location    string    `json:"location,omitempty"`
	Attendees   []string  `json:"attendees,omitempty"`
}

// ScheduleMeeting schedules a food tasting / technical meeting.
func (m *Module) ScheduleMeeting(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventMeetingInput) (EventMeeting, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventMeeting{}, err
	}
	if !e.open() {
		return EventMeeting{}, errs.Conflict("event_closed", "a "+e.Status+" event has no meetings")
	}
	if !contains([]string{"food_tasting", "technical_meeting", "site_visit"}, in.Kind) {
		return EventMeeting{}, handle.Invalid("kind", "invalid", "food_tasting, technical_meeting or site_visit")
	}
	if in.ScheduledAt.IsZero() {
		return EventMeeting{}, handle.Invalid("scheduledAt", "required", "the date and time are required")
	}
	att := in.Attendees
	if att == nil {
		att = []string{}
	}
	mid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_meetings (id, property_id, event_id, kind, scheduled_at, location, attendees, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, mid, e.PropertyID, eid, in.Kind, in.ScheduledAt, nzs(in.Location), att, actor(ctx)); err != nil {
		return EventMeeting{}, err
	}
	rows, err := tx.Query(ctx, meetingSelect+` WHERE id = $1`, mid)
	mt, err := handle.One[EventMeeting](rows, err, "meeting")
	if err != nil {
		return mt, err
	}
	return mt, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionCreate, EntityType: "banquet.event_meeting", EntityID: mid.String(),
		EntityLabel: e.Number + " · " + strings.ReplaceAll(in.Kind, "_", " "), PropertyID: &e.PropertyID, After: mt})
}

// MeetingRecordInput records the outcome of a meeting.
type EventMeetingRecord struct {
	Status  string   `json:"status,omitempty" enum:"done,cancelled" doc:"Default done"`
	Outcome string   `json:"outcome,omitempty"`
	Changes []string `json:"changes,omitempty" doc:"Agreed changes (menu, setup …) forwarded to the BEO"`
}

// RecordMeeting records the outcome and the changes for the BEO.
func (m *Module) RecordMeeting(ctx context.Context, tx pgx.Tx, mid uuid.UUID, in EventMeetingRecord) (EventMeeting, error) {
	rows, err := tx.Query(ctx, meetingSelect+` WHERE id = $1 FOR UPDATE`, mid)
	before, err := handle.One[EventMeeting](rows, err, "meeting")
	if err != nil {
		return before, err
	}
	status := in.Status
	if status == "" {
		status = "done"
	}
	if status != "done" && status != "cancelled" {
		return before, handle.Invalid("status", "invalid", "done or cancelled")
	}
	ch := in.Changes
	if ch == nil {
		ch = []string{}
	}
	raw, _ := json.Marshal(ch)
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_meetings SET status = $2, outcome = $3, changes = $4, recorded_at = now(), recorded_by = $5, updated_by = $5
		WHERE id = $1`, mid, status, nzs(in.Outcome), raw, actor(ctx)); err != nil {
		return before, err
	}
	rows, err = tx.Query(ctx, meetingSelect+` WHERE id = $1`, mid)
	after, err := handle.One[EventMeeting](rows, err, "meeting")
	if err != nil {
		return after, err
	}
	if err := m.touch(ctx, tx, before.EventID); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "record_meeting", EntityType: "banquet.event_meeting", EntityID: mid.String(),
		EntityLabel: strings.ReplaceAll(before.Kind, "_", " "), Before: before, After: after})
}

// ── vendors (FR-EVT-06) ───────────────────────────────────────────────────

// EventVendor is a vendor booked for an event.
type EventVendor struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	EventID          uuid.UUID  `json:"eventId" db:"event_id"`
	VendorID         uuid.UUID  `json:"vendorId" db:"vendor_id"`
	VendorName       string     `json:"vendorName" db:"vendor_name"`
	VendorType       string     `json:"vendorType" db:"vendor_type"`
	Partner          bool       `json:"partner" db:"partner"`
	ContactName      *string    `json:"contactName" db:"contact_name"`
	Phone            *string    `json:"phone" db:"phone"`
	Service          string     `json:"service" db:"service"`
	Fee              string     `json:"fee" db:"fee"`
	ChargeToCustomer bool       `json:"chargeToCustomer" db:"charge_to_customer"`
	ChargeID         *uuid.UUID `json:"chargeId" db:"charge_id"`
	ArrivalAt        *time.Time `json:"arrivalAt" db:"arrival_at"`
	Notes            *string    `json:"notes" db:"notes"`
	Status           string     `json:"status" db:"status" enum:"confirmed,cancelled"`
}

const eventVendorSelect = `SELECT x.id, x.event_id, x.vendor_id, v.name AS vendor_name, v.vendor_type, v.partner, v.contact_name, v.phone, x.service,
	trim_scale(x.fee)::text AS fee, x.charge_to_customer, x.charge_id, x.arrival_at, x.notes, x.status
	FROM banquet.event_vendors x JOIN banquet.vendors v ON v.id = x.vendor_id`

func (m *Module) eventVendors(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventVendor, error) {
	return handle.List[EventVendor](q.Query(ctx, eventVendorSelect+` WHERE x.event_id = $1 ORDER BY x.arrival_at NULLS LAST, v.name`, eid))
}

// EventVendorInput books a vendor for an event.
type EventVendorInput struct {
	VendorID         uuid.UUID  `json:"vendorId"`
	Service          string     `json:"service"`
	Fee              string     `json:"fee,omitempty"`
	ChargeToCustomer bool       `json:"chargeToCustomer,omitempty" doc:"Post the vendor fee to the event folio"`
	ArrivalAt        *time.Time `json:"arrivalAt,omitempty"`
	Notes            string     `json:"notes,omitempty"`
}

// AddVendor books a vendor (decoration, MC, band, photographer …).
func (m *Module) AddVendor(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventVendorInput) (EventVendor, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventVendor{}, err
	}
	if !e.open() {
		return EventVendor{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot book vendors")
	}
	if err := handle.Required("service", in.Service); err != nil {
		return EventVendor{}, err
	}
	var name, status string
	if err := tx.QueryRow(ctx, `SELECT name, status FROM banquet.vendors WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, in.VendorID, e.PropertyID).
		Scan(&name, &status); err != nil {
		if dbtx.IsNoRows(err) {
			return EventVendor{}, handle.Invalid("vendorId", "not_found", "vendor not found")
		}
		return EventVendor{}, err
	}
	if status != "active" {
		return EventVendor{}, handle.Invalid("vendorId", "inactive", "the vendor is inactive")
	}
	fee, err := handle.Decimal("fee", in.Fee, decimal.Zero)
	if err != nil || fee.IsNegative() {
		return EventVendor{}, handle.Invalid("fee", "invalid", "a non-negative amount")
	}
	xid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_vendors (id, property_id, event_id, vendor_id, service, fee, charge_to_customer, arrival_at, notes,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8,$9,$10,$10)`, xid, e.PropertyID, eid, in.VendorID, strings.TrimSpace(in.Service),
		fee.String(), in.ChargeToCustomer, in.ArrivalAt, nzs(in.Notes), actor(ctx)); err != nil {
		return EventVendor{}, err
	}
	if in.ChargeToCustomer && fee.IsPositive() {
		pol, _, err := chargesPolicy(ctx, tx, e.PropertyID)
		if err != nil {
			return EventVendor{}, err
		}
		c, err := m.postCharge(ctx, tx, eid, chargeSpec{Source: "vendor", Kind: "vendor_fee", RefID: &xid, Description: "Vendor · " + name + " · " + in.Service,
			Quantity: decimal.NewFromInt(1), UnitPrice: fee, Mode: pol.PricingMode, TaxCodes: pol.TaxCodes, Component: "event_fee"})
		if err != nil {
			return EventVendor{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.event_vendors SET charge_id = $2 WHERE id = $1`, xid, c.ID); err != nil {
			return EventVendor{}, err
		}
	} else if err := m.touch(ctx, tx, eid); err != nil {
		return EventVendor{}, err
	}
	rows, err := tx.Query(ctx, eventVendorSelect+` WHERE x.id = $1`, xid)
	v, err := handle.One[EventVendor](rows, err, "event vendor")
	if err != nil {
		return v, err
	}
	return v, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionCreate, EntityType: "banquet.event_vendor", EntityID: xid.String(),
		EntityLabel: e.Number + " · " + name, PropertyID: &e.PropertyID, After: v})
}

// CancelVendor cancels a vendor booking (its fee charge is voided).
func (m *Module) CancelVendor(ctx context.Context, tx pgx.Tx, xid uuid.UUID, reason string) (EventVendor, error) {
	rows, err := tx.Query(ctx, eventVendorSelect+` WHERE x.id = $1`, xid)
	before, err := handle.One[EventVendor](rows, err, "event vendor")
	if err != nil {
		return before, err
	}
	if before.Status != "confirmed" {
		return before, errs.Conflict("already_cancelled", "the vendor booking is cancelled")
	}
	if reason == "" {
		reason = "vendor cancelled"
	}
	if err := m.voidCharges(ctx, tx, before.EventID, "vendor", &xid, reason); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_vendors SET status = 'cancelled', updated_by = $2 WHERE id = $1`, xid, actor(ctx)); err != nil {
		return before, err
	}
	rows, err = tx.Query(ctx, eventVendorSelect+` WHERE x.id = $1`, xid)
	after, err := handle.One[EventVendor](rows, err, "event vendor")
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "cancel", EntityType: "banquet.event_vendor", EntityID: xid.String(),
		EntityLabel: before.VendorName, Before: before, After: after, Reason: reason})
}

// ── other resources (FR-EVT-03, FR-BQT-06) ────────────────────────────────

// EventResource is another resource of the event (bungalow, meeting room,
// golf cart, equipment) on the Reservation Engine.
type EventResource struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	EventID         uuid.UUID  `json:"eventId" db:"event_id"`
	ResourceID      uuid.UUID  `json:"resourceId" db:"resource_id"`
	ResourceName    string     `json:"resourceName" db:"resource_name"`
	ResourceType    string     `json:"resourceType" db:"resource_type"`
	ReservationID   *uuid.UUID `json:"reservationId" db:"reservation_id"`
	ReservationCode *string    `json:"reservationCode" db:"reservation_code"`
	InclusionID     *uuid.UUID `json:"inclusionId" db:"inclusion_id" doc:"Held for a package inclusion (no charge)"`
	Description     *string    `json:"description" db:"description"`
	Quantity        int        `json:"quantity" db:"quantity"`
	Start           time.Time  `json:"start" db:"start_at"`
	End             time.Time  `json:"end" db:"end_at"`
	Status          string     `json:"status" db:"status" enum:"held,confirmed,released,completed"`
}

const resourceSelect = `SELECT x.id, x.event_id, x.resource_id, r.name AS resource_name, r.resource_type, x.reservation_id, rv.code AS reservation_code,
	x.inclusion_id, x.description,
	x.quantity, x.start_at, x.end_at, x.status FROM banquet.event_resources x JOIN reservation.resources r ON r.id = x.resource_id
	LEFT JOIN reporting.reservations rv ON rv.reservation_id = x.reservation_id`

func (m *Module) resources(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventResource, error) {
	return handle.List[EventResource](q.Query(ctx, resourceSelect+` WHERE x.event_id = $1 ORDER BY x.start_at, r.name`, eid))
}

// ResourceInput books another resource for the event.
type EventResourceInput struct {
	ResourceID       uuid.UUID  `json:"resourceId" doc:"Reservation resource: bungalow, meeting room, golf cart, equipment …"`
	Start            *time.Time `json:"start,omitempty" doc:"Default: the event start"`
	End              *time.Time `json:"end,omitempty" doc:"Default: the event end"`
	Quantity         int        `json:"quantity,omitempty"`
	Description      string     `json:"description,omitempty"`
	Amount           string     `json:"amount,omitempty" doc:"Charge to the event folio (not included in the package)"`
	RevenueComponent string     `json:"revenueComponent,omitempty" doc:"Billing revenue component of the charge (default other)"`
}

// AddResource books a resource (all or nothing) for the event: pending while
// tentative, confirmed with the event.
func (m *Module) AddResource(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventResourceInput) (EventResource, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventResource{}, err
	}
	if !e.open() {
		return EventResource{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot book resources")
	}
	res, err := m.Res.Resource(ctx, tx, in.ResourceID)
	if err != nil || res.PropertyID != e.PropertyID {
		return EventResource{}, handle.Invalid("resourceId", "not_found", "resource not found")
	}
	if res.ResourceType == "banquet_venue" {
		return EventResource{}, handle.Invalid("resourceId", "venue", "hold banquet venues through the venue holds")
	}
	start, end := e.Start, e.End
	if in.Start != nil {
		start = *in.Start
	}
	if in.End != nil {
		end = *in.End
	}
	qty := max(in.Quantity, 1)
	amount, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil || amount.IsNegative() {
		return EventResource{}, handle.Invalid("amount", "invalid", "a non-negative amount")
	}
	comp := in.RevenueComponent
	if comp == "" {
		comp = "other"
	}
	if !contains(billing.RevenueComponents, comp) {
		return EventResource{}, handle.Invalid("revenueComponent", "invalid", "unknown revenue component")
	}
	xid := id.New()
	r, err := m.Res.Book(ctx, tx, e.PropertyID, reservation.BookRequest{Kind: "block", BusinessLine: "banquet", Channel: e.channel(), CustomerID: e.CustomerID,
		GuestName: deref(e.ContactName), CorporateName: deref(e.CorporateName), Confirm: e.Status == StatusDefinite, SourceType: "banquet.event_resource",
		SourceID: &xid, Notes: e.Number + " · " + e.Title, Lines: []reservation.LineRequest{{ResourceID: res.ID, Start: start, End: end, Quantity: qty,
			Description: in.Description}}, Attributes: map[string]any{"eventId": e.ID.String(), "eventNumber": e.Number}})
	if err != nil {
		return EventResource{}, err
	}
	status := "held"
	if e.Status == StatusDefinite {
		status = "confirmed"
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		desc = res.Name
	}
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_resources (id, property_id, event_id, resource_id, reservation_id, description, quantity, start_at, end_at,
		status, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, xid, e.PropertyID, eid, res.ID, r.ID, desc, qty, start, end, status,
		actor(ctx)); err != nil {
		return EventResource{}, err
	}
	if amount.IsPositive() {
		pol, _, err := chargesPolicy(ctx, tx, e.PropertyID)
		if err != nil {
			return EventResource{}, err
		}
		if _, err := m.postCharge(ctx, tx, eid, chargeSpec{Source: "resource", Kind: res.ResourceType, RefID: &xid, Description: desc,
			Quantity: decimal.NewFromInt(int64(qty)), UnitPrice: amount.Div(decimal.NewFromInt(int64(qty))), Mode: pol.PricingMode, TaxCodes: pol.TaxCodes,
			Component: comp}); err != nil {
			return EventResource{}, err
		}
	} else if err := m.touch(ctx, tx, eid); err != nil {
		return EventResource{}, err
	}
	rows, err := tx.Query(ctx, resourceSelect+` WHERE x.id = $1`, xid)
	out, err := handle.One[EventResource](rows, err, "event resource")
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionCreate, EntityType: "banquet.event_resource", EntityID: xid.String(),
		EntityLabel: e.Number + " · " + res.Name, PropertyID: &e.PropertyID, After: out})
}

func (m *Module) releaseResource(ctx context.Context, tx pgx.Tx, x EventResource, reason string) error {
	if x.ReservationID != nil && (x.Status == "held" || x.Status == "confirmed") {
		if err := m.cancelReservation(ctx, tx, *x.ReservationID, reason); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_resources SET status = 'released', updated_by = $2 WHERE id = $1`, x.ID, actor(ctx)); err != nil {
		return err
	}
	return m.voidCharges(ctx, tx, x.EventID, "resource", &x.ID, reason)
}

// ReleaseResource frees a resource of the event.
func (m *Module) ReleaseResource(ctx context.Context, tx pgx.Tx, xid uuid.UUID, reason string) (EventResource, error) {
	rows, err := tx.Query(ctx, resourceSelect+` WHERE x.id = $1`, xid)
	before, err := handle.One[EventResource](rows, err, "event resource")
	if err != nil {
		return before, err
	}
	if before.Status != "held" && before.Status != "confirmed" {
		return before, errs.Conflict("not_held", "the resource is "+before.Status)
	}
	if reason == "" {
		reason = "resource released"
	}
	if err := m.releaseResource(ctx, tx, before, reason); err != nil {
		return before, err
	}
	rows, err = tx.Query(ctx, resourceSelect+` WHERE x.id = $1`, xid)
	after, err := handle.One[EventResource](rows, err, "event resource")
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "release", EntityType: "banquet.event_resource", EntityID: xid.String(),
		EntityLabel: before.ResourceName, Before: before, After: after, Reason: reason})
}

func (m *Module) releaseResources(ctx context.Context, tx pgx.Tx, eid uuid.UUID, reason string) error {
	list, err := m.resources(ctx, tx, eid)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.Status == "held" || x.Status == "confirmed" {
			if err := m.releaseResource(ctx, tx, x, reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── incidents (FR-OPS-P3-01) ──────────────────────────────────────────────

// Incident is an incident note of the event day.
type EventIncident struct {
	ID        uuid.UUID `json:"id" db:"id"`
	EventID   uuid.UUID `json:"eventId" db:"event_id"`
	Severity  string    `json:"severity" db:"severity" enum:"low,medium,high"`
	Note      string    `json:"note" db:"note"`
	Reporter  *string   `json:"reporter" db:"reporter"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// IncidentInput records an incident.
type EventIncidentInput struct {
	Severity string `json:"severity,omitempty" enum:"low,medium,high"`
	Note     string `json:"note"`
}

func (m *Module) incidents(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventIncident, error) {
	return handle.List[EventIncident](q.Query(ctx, `SELECT id, event_id, severity, note, reporter, created_at FROM banquet.event_incidents WHERE event_id = $1
		ORDER BY created_at`, eid))
}

// AddIncident records an incident note on the event day.
func (m *Module) AddIncident(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventIncidentInput) (EventIncident, error) {
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return EventIncident{}, err
	}
	if err := handle.Required("note", in.Note); err != nil {
		return EventIncident{}, err
	}
	sev := in.Severity
	if sev == "" {
		sev = "low"
	}
	if !contains([]string{"low", "medium", "high"}, sev) {
		return EventIncident{}, handle.Invalid("severity", "invalid", "low, medium or high")
	}
	iid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_incidents (id, property_id, event_id, severity, note, reported_by, reporter) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		iid, e.PropertyID, eid, sev, strings.TrimSpace(in.Note), actor(ctx), actorName(ctx)); err != nil {
		return EventIncident{}, err
	}
	out := EventIncident{ID: iid, EventID: eid, Severity: sev, Note: strings.TrimSpace(in.Note), CreatedAt: clock.Now()}
	n := actorName(ctx)
	out.Reporter = &n
	return out, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "incident", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number, PropertyID: &e.PropertyID, After: out})
}

// ── event detail, lists, calendar, today ──────────────────────────────────

// ChecklistProgress counts the checklist tasks.
type EventChecklistProgress struct {
	Total   int `json:"total"`
	Done    int `json:"done"`
	Overdue int `json:"overdue"`
}

// BEOSummary is the current BEO of an event.
type BEOSummary struct {
	ID                 uuid.UUID  `json:"id"`
	Number             string     `json:"number"`
	Version            int        `json:"version"`
	Status             string     `json:"status"`
	IssuedAt           *time.Time `json:"issuedAt"`
	PendingDepartments []string   `json:"pendingDepartments" doc:"Departments that have not confirmed this version"`
}

// BillingSnapshot is the money position of an event.
type EventBillingSnapshot struct {
	FolioID             *uuid.UUID `json:"folioId"`
	ScheduleID          *uuid.UUID `json:"scheduleId"`
	ContractTotal       string     `json:"contractTotal"`
	Received            string     `json:"received"`
	Balance             string     `json:"balance"`
	DownPaymentRequired string     `json:"downPaymentRequired"`
	DownPaymentReceived bool       `json:"downPaymentReceived"`
}

// EventDetail is the event with everything attached.
type EventDetail struct {
	BanquetEvent
	Venues       []VenueHold            `json:"venues"`
	Schedule     []RundownItem          `json:"schedule"`
	Menus        []EventMenuSelection   `json:"menus"`
	Resources    []EventResource        `json:"resources"`
	Inclusions   []EventInclusion       `json:"inclusions" doc:"Package inclusions (FR-BQT-06)"`
	Vendors      []EventVendor          `json:"vendors"`
	Meetings     []EventMeeting         `json:"meetings"`
	Incidents    []EventIncident        `json:"incidents"`
	Checklist    EventChecklistProgress `json:"checklist"`
	Participants EventParticipantStats  `json:"participants"`
	Billing      EventBillingSnapshot   `json:"billing"`
	BEO          *BEOSummary            `json:"beo"`
}

func (m *Module) checklistProgress(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (EventChecklistProgress, error) {
	var p EventChecklistProgress
	err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'done'), count(*) FILTER (WHERE status = 'open' AND due_date < $2::date)
		FROM banquet.event_checklist_items WHERE event_id = $1`, eid, today(ctx, q)).Scan(&p.Total, &p.Done, &p.Overdue)
	return p, err
}

func (m *Module) beoSummary(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (*BEOSummary, error) {
	var s BEOSummary
	err := q.QueryRow(ctx, `SELECT id, number, version, status, issued_at FROM banquet.beos WHERE event_id = $1 AND status <> 'superseded'
		ORDER BY version DESC LIMIT 1`, eid).Scan(&s.ID, &s.Number, &s.Version, &s.Status, &s.IssuedAt)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT department FROM banquet.beo_departments WHERE beo_id = $1 AND acknowledged_at IS NULL ORDER BY department`, s.ID)
	if err != nil {
		return nil, err
	}
	if s.PendingDepartments, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	if s.PendingDepartments == nil {
		s.PendingDepartments = []string{}
	}
	return &s, nil
}

// Detail returns the event with holds, schedule, menu, resources, vendors,
// meetings, checklist, registrations, billing and BEO.
func (m *Module) Detail(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (EventDetail, error) {
	e, err := m.Get(ctx, q, eid)
	if err != nil {
		return EventDetail{}, err
	}
	d := EventDetail{BanquetEvent: e}
	if d.Venues, err = m.holds(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Schedule, err = m.schedule(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Menus, err = m.menuSelections(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Resources, err = m.resources(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Inclusions, err = m.inclusions(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Vendors, err = m.eventVendors(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Meetings, err = m.meetings(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Incidents, err = m.incidents(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Checklist, err = m.checklistProgress(ctx, q, eid); err != nil {
		return d, err
	}
	if d.Participants, err = m.participantStats(ctx, q, e); err != nil {
		return d, err
	}
	if d.BEO, err = m.beoSummary(ctx, q, eid); err != nil {
		return d, err
	}
	required, received, err := m.depositStatus(ctx, q, e)
	if err != nil {
		return d, err
	}
	d.Billing = EventBillingSnapshot{FolioID: e.FolioID, ScheduleID: e.ScheduleID, ContractTotal: e.ContractTotal, Received: received.String(),
		DownPaymentRequired: required.String(), DownPaymentReceived: required.IsPositive() && received.GreaterThanOrEqual(required), Balance: "0"}
	if e.FolioID != nil {
		sum, err := billing.FolioSummary(ctx, q, *e.FolioID)
		if err != nil {
			return d, err
		}
		d.Billing.Balance = dec(sum.Charges).Sub(received).String()
	}
	return d, nil
}

// EventFilter filters the event list.
type EventFilter struct {
	Status, Category, Text string
	EventTypeID, OwnerID   *uuid.UUID
	From, To               time.Time
	Limit                  int
}

// List lists events overlapping a period.
func (m *Module) List(ctx context.Context, q dbtx.Querier, property uuid.UUID, f EventFilter) ([]BanquetEvent, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	return handle.List[BanquetEvent](q.Query(ctx, eventSelect+` WHERE e.property_id = $1 AND ($2 = '' OR e.status = ANY(string_to_array($2, ',')))
		AND ($3 = '' OR e.category = ANY(string_to_array($3, ','))) AND ($4::uuid IS NULL OR e.event_type_id = $4) AND ($5::uuid IS NULL OR e.sales_owner_id = $5)
		AND e.end_at > $6 AND e.start_at < $7
		AND ($8 = '' OR e.number ILIKE '%' || $8 || '%' OR e.title ILIKE '%' || $8 || '%' OR c.name ILIKE '%' || $8 || '%' OR ca.name ILIKE '%' || $8 || '%')
		ORDER BY e.start_at, e.number LIMIT $9`, property, f.Status, f.Category, f.EventTypeID, f.OwnerID, f.From, f.To, f.Text, f.Limit))
}

// CalendarEntry is one event hold on the venue calendar.
type VenueCalendarEntry struct {
	HoldID       uuid.UUID `json:"holdId" db:"hold_id"`
	EventID      uuid.UUID `json:"eventId" db:"event_id"`
	EventNumber  string    `json:"eventNumber" db:"event_number"`
	Title        string    `json:"title" db:"title"`
	EventStatus  string    `json:"eventStatus" db:"event_status"`
	HoldStatus   string    `json:"holdStatus" db:"hold_status" enum:"tentative,definite,waitlisted,completed"`
	Function     *string   `json:"function" db:"function_name"`
	Start        time.Time `json:"start" db:"start_at"`
	End          time.Time `json:"end" db:"end_at"`
	WaitlistRank *int      `json:"waitlistRank" db:"waitlist_rank"`
}

// CalendarRow is one venue on the Venue Calendar (FR-VEN-05, FR-EVT-10).
type VenueCalendarRow struct {
	VenueID   uuid.UUID                   `json:"venueId"`
	Code      string                      `json:"code"`
	Name      string                      `json:"name"`
	VenueType string                      `json:"venueType"`
	ParentID  *uuid.UUID                  `json:"parentVenueId"`
	Events    []VenueCalendarEntry        `json:"events"`
	Bookings  []reservation.CalendarEntry `json:"bookings" doc:"Reservation Engine allocations of the venue resource (Booking Calendar)"`
}

// VenueCalendar lists the holds and bookings of every venue in a period.
func (m *Module) VenueCalendar(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]VenueCalendarRow, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, venue_type, parent_venue_id, resource_id FROM banquet.venues WHERE property_id = $1 AND status = 'active'
		AND archived_at IS NULL ORDER BY coalesce(parent_venue_id, id), parent_venue_id NULLS FIRST, name`, property)
	if err != nil {
		return nil, err
	}
	type v struct {
		row VenueCalendarRow
		res *uuid.UUID
	}
	var venues []v
	for rows.Next() {
		var x v
		if err := rows.Scan(&x.row.VenueID, &x.row.Code, &x.row.Name, &x.row.VenueType, &x.row.ParentID, &x.res); err != nil {
			rows.Close()
			return nil, err
		}
		venues = append(venues, x)
	}
	rows.Close()
	cal, err := m.Res.Calendar(ctx, q, property, from, to, []string{"banquet_venue"}, nil)
	if err != nil {
		return nil, err
	}
	byRes := map[uuid.UUID][]reservation.CalendarEntry{}
	for _, c := range cal {
		byRes[c.Resource.ID] = c.Entries
	}
	out := make([]VenueCalendarRow, 0, len(venues))
	for _, x := range venues {
		x.row.Events, err = handle.List[VenueCalendarEntry](q.Query(ctx, `SELECT h.id AS hold_id, h.event_id, e.number AS event_number, e.title, e.status AS event_status,
			h.status AS hold_status, h.function_name, h.start_at, h.end_at, h.waitlist_rank FROM banquet.event_venues h JOIN banquet.events e ON e.id = h.event_id
			WHERE h.venue_id = $1 AND h.status IN ('tentative', 'definite', 'waitlisted', 'completed') AND h.end_at > $2 AND h.start_at < $3
			ORDER BY h.start_at, h.status = 'waitlisted', h.waitlist_rank`, x.row.VenueID, from, to))
		if err != nil {
			return nil, err
		}
		x.row.Bookings = []reservation.CalendarEntry{}
		if x.res != nil {
			if b, ok := byRes[*x.res]; ok {
				x.row.Bookings = b
			}
		}
		out = append(out, x.row)
	}
	return out, nil
}

// VenueAvailability is a venue free or busy for a period and pax.
type BanquetVenueAvailability struct {
	VenueID    uuid.UUID `json:"venueId"`
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	VenueType  string    `json:"venueType"`
	Capacity   *int      `json:"capacity" doc:"For the layout when given, else the maximum"`
	MinPax     int       `json:"minPax"`
	AddonPrice string    `json:"addonPrice"`
	Fits       bool      `json:"fits" doc:"Pax within the capacity and above the minimum"`
	Available  bool      `json:"available" doc:"No hold or booking overlaps the period (with setup / teardown buffers)"`
	Waitlist   int       `json:"waitlist" doc:"Waitlisted holds on the period"`
}

// Availability checks every venue for a period, pax and layout.
func (m *Module) Availability(ctx context.Context, q dbtx.Querier, property uuid.UUID, start, end time.Time, pax int, layout string) ([]BanquetVenueAvailability, error) {
	rt, err := m.Res.Type(ctx, q, "banquet_venue")
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT v.id, v.code, v.name, v.venue_type, v.resource_id, v.min_pax, trim_scale(v.addon_price)::text,
		coalesce((SELECT capacity FROM banquet.venue_layouts l WHERE l.venue_id = v.id AND l.layout = $2), CASE WHEN $2 = '' THEN v.max_capacity END)
		FROM banquet.venues v WHERE v.property_id = $1 AND v.status = 'active' AND v.archived_at IS NULL ORDER BY v.name`, property, layout)
	if err != nil {
		return nil, err
	}
	type row struct {
		a   BanquetVenueAvailability
		res *uuid.UUID
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.a.VenueID, &r.a.Code, &r.a.Name, &r.a.VenueType, &r.res, &r.a.MinPax, &r.a.AddonPrice, &r.a.Capacity); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, r)
	}
	rows.Close()
	from := start.Add(-time.Duration(rt.BufferBefore) * time.Minute)
	to := end.Add(time.Duration(rt.BufferAfter) * time.Minute)
	out := make([]BanquetVenueAvailability, 0, len(list))
	for _, r := range list {
		a := r.a
		a.Fits = (a.Capacity == nil && layout == "") || (a.Capacity != nil && pax <= *a.Capacity)
		if a.MinPax > 0 && pax < a.MinPax {
			a.Fits = false
		}
		if r.res != nil {
			busy, err := reservation.Busy(ctx, q, []uuid.UUID{*r.res}, from, to)
			if err != nil {
				return nil, err
			}
			a.Available = !busy[*r.res]
		}
		if err := q.QueryRow(ctx, `SELECT count(*) FROM banquet.event_venues WHERE venue_id = $1 AND status = 'waitlisted' AND start_at < $3 AND end_at > $2`,
			a.VenueID, start, end).Scan(&a.Waitlist); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// TodayEvent is one event of Today's Events (Event Operations).
type TodayEvent struct {
	Event        BanquetEvent           `json:"event"`
	Venues       []VenueHold            `json:"venues"`
	Schedule     []RundownItem          `json:"schedule"`
	BEO          *BEOSummary            `json:"beo"`
	Checklist    EventChecklistProgress `json:"checklist"`
	Participants EventParticipantStats  `json:"participants"`
	Incidents    int                    `json:"incidents"`
}

// Today lists the events running on a local day.
func (m *Module) Today(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) ([]TodayEvent, error) {
	loc := calendar.Location(ctx, q)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	evs, err := m.List(ctx, q, property, EventFilter{Status: "tentative,definite,completed", From: from, To: from.AddDate(0, 0, 1), Limit: 200})
	if err != nil {
		return nil, err
	}
	out := make([]TodayEvent, 0, len(evs))
	for _, e := range evs {
		t := TodayEvent{Event: e}
		if t.Venues, err = m.holds(ctx, q, e.ID); err != nil {
			return nil, err
		}
		if t.Schedule, err = m.schedule(ctx, q, e.ID); err != nil {
			return nil, err
		}
		if t.BEO, err = m.beoSummary(ctx, q, e.ID); err != nil {
			return nil, err
		}
		if t.Checklist, err = m.checklistProgress(ctx, q, e.ID); err != nil {
			return nil, err
		}
		if t.Participants, err = m.participantStats(ctx, q, e); err != nil {
			return nil, err
		}
		if err := q.QueryRow(ctx, `SELECT count(*) FROM banquet.event_incidents WHERE event_id = $1`, e.ID).Scan(&t.Incidents); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
