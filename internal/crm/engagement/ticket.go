package engagement

// Complaint Ticket (FR-TKT-01..05): tickets from staff, the Member App
// (Support), the website and low feedback scores; SLA per priority (and
// category) counted in business hours; escalation line manager → General
// Manager; Open → In Progress → Resolved → Closed with reopen; customer
// communication logged; compensation through approval.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
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
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Ticket statuses (PRD P3 §7.6) and priorities.
var (
	TicketStatuses = []string{"open", "in_progress", "escalated", "resolved", "closed"}
	Priorities     = []string{"low", "medium", "high", "urgent"}
	BusinessLines  = []string{"golf", "sportclub", "stay", "pos", "membership", "banquet", "other"}
)

// TicketCategories is the Complaint category master (default line,
// priority, department and SLA override).
var TicketCategories = &resource.Def{
	Key: "crm.ticket_category", Module: "crm", Perm: "crm.ticket_category", Path: "/api/v1/crm/ticket-categories", Table: "crm.ticket_categories",
	Name: "Complaint Category", Plural: "Complaint Categories", Tag: "Complaints", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "businessLine", Column: "business_line", Label: "Business Line", Kind: resource.Enum, Enum: BusinessLines, Filter: true},
		{Name: "defaultPriority", Column: "default_priority", Label: "Default Priority", Kind: resource.Enum, Enum: Priorities, Default: "medium"},
		{Name: "departmentId", Column: "department_id", Label: "Department", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "platform.departments", SameProperty: true, Label: "department"}},
		{Name: "firstResponseMinutes", Column: "first_response_minutes", Label: "First Response SLA (minutes, default: policy)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "resolutionMinutes", Column: "resolution_minutes", Label: "Resolution SLA (minutes, default: policy)", Kind: resource.Int, Min: resource.Min(1)},
		resource.Status("active", "inactive")},
}

// CompensationDocumentType is the approval of a ticket compensation.
var CompensationDocumentType = provision.DocumentType{Code: "ticket_compensation", Module: "crm", Name: "Complaint Compensation",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}, {Key: "points", Label: "Points", Type: "number"},
		{Key: "type", Label: "Type", Type: "string"}}}

// ComplaintTicket is a complaint ticket.
type ComplaintTicket struct {
	ID                    uuid.UUID   `json:"id" db:"id"`
	Number                string      `json:"number" db:"number"`
	CustomerID            *uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName          *string     `json:"customerName" db:"customer_name"`
	ContactName           *string     `json:"contactName" db:"contact_name"`
	ContactEmail          *string     `json:"contactEmail" db:"contact_email"`
	ContactPhone          *string     `json:"contactPhone" db:"contact_phone"`
	CategoryID            *uuid.UUID  `json:"categoryId" db:"category_id"`
	CategoryName          *string     `json:"categoryName" db:"category_name"`
	BusinessLine          string      `json:"businessLine" db:"business_line"`
	Priority              string      `json:"priority" db:"priority" enum:"low,medium,high,urgent"`
	Channel               string      `json:"channel" db:"channel" enum:"staff,member_app,website,feedback,whatsapp,email,phone"`
	Subject               string      `json:"subject" db:"subject"`
	Description           string      `json:"description" db:"description"`
	AttachmentFileIDs     []uuid.UUID `json:"attachmentFileIds" db:"attachment_file_ids"`
	Status                string      `json:"status" db:"status" enum:"open,in_progress,escalated,resolved,closed"`
	FeedbackID            *uuid.UUID  `json:"feedbackId" db:"feedback_id"`
	DepartmentID          *uuid.UUID  `json:"departmentId" db:"department_id"`
	DepartmentName        *string     `json:"departmentName" db:"department_name"`
	AssignedTo            *uuid.UUID  `json:"assignedTo" db:"assigned_to"`
	AssigneeName          *string     `json:"assigneeName" db:"assignee_name"`
	EscalationLevel       int         `json:"escalationLevel" db:"escalation_level" doc:"0 none, 1 line manager, 2 General Manager"`
	EscalatedAt           *time.Time  `json:"escalatedAt" db:"escalated_at"`
	FirstResponseDueAt    time.Time   `json:"firstResponseDueAt" db:"first_response_due_at"`
	ResolutionDueAt       time.Time   `json:"resolutionDueAt" db:"resolution_due_at"`
	FirstRespondedAt      *time.Time  `json:"firstRespondedAt" db:"first_responded_at"`
	ResolvedAt            *time.Time  `json:"resolvedAt" db:"resolved_at"`
	Resolution            *string     `json:"resolution" db:"resolution"`
	ClosedAt              *time.Time  `json:"closedAt" db:"closed_at"`
	ReopenedCount         int         `json:"reopenedCount" db:"reopened_count"`
	FirstResponseBreached bool        `json:"firstResponseBreached" db:"first_response_breached"`
	ResolutionBreached    bool        `json:"resolutionBreached" db:"resolution_breached"`
	SLAPolicyVersion      int         `json:"slaPolicyVersion" db:"sla_policy_version"`
	CreatedAt             time.Time   `json:"createdAt" db:"created_at"`
	UpdatedAt             time.Time   `json:"updatedAt" db:"updated_at"`
	PropertyID            uuid.UUID   `json:"-" db:"property_id"`
	Overdue               bool        `json:"overdue" db:"-" doc:"An open SLA timer has passed"`
}

const ticketSelect = `SELECT t.id, t.number, t.customer_id, c.name AS customer_name, t.contact_name, t.contact_email, t.contact_phone, t.category_id,
	k.name AS category_name, t.business_line, t.priority, t.channel, t.subject, t.description, t.attachment_file_ids, t.status, t.feedback_id,
	t.department_id, d.name AS department_name, t.assigned_to, u.full_name AS assignee_name, t.escalation_level, t.escalated_at,
	t.first_response_due_at, t.resolution_due_at, t.first_responded_at, t.resolved_at, t.resolution, t.closed_at, t.reopened_count,
	t.first_response_breached, t.resolution_breached, t.sla_policy_version, t.created_at, t.updated_at, t.property_id
	FROM crm.tickets t LEFT JOIN crm.customers c ON c.id = t.customer_id LEFT JOIN crm.ticket_categories k ON k.id = t.category_id
	LEFT JOIN platform.departments d ON d.id = t.department_id LEFT JOIN platform.users u ON u.id = t.assigned_to`

func overdue(t *ComplaintTicket, now time.Time) {
	if t.Status == "resolved" || t.Status == "closed" {
		return
	}
	t.Overdue = now.After(t.ResolutionDueAt) || (t.FirstRespondedAt == nil && now.After(t.FirstResponseDueAt))
}

// GetTicket loads a ticket.
func GetTicket(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (ComplaintTicket, error) {
	rows, err := q.Query(ctx, ticketSelect+` WHERE t.id = $1`, tid)
	t, err := handle.One[ComplaintTicket](rows, err, "ticket")
	if err == nil {
		overdue(&t, clock.Now())
	}
	return t, err
}

func lockTicket(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (ComplaintTicket, error) {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT true FROM crm.tickets WHERE id = $1 AND property_id = $2 FOR UPDATE`, tid, property).Scan(&ok); err != nil {
		if dbtx.IsNoRows(err) {
			return ComplaintTicket{}, errs.NotFound("ticket")
		}
		return ComplaintTicket{}, err
	}
	return GetTicket(ctx, tx, tid)
}

// TicketEvent is one entry of the ticket history.
type TicketEvent struct {
	ID         uuid.UUID      `json:"id" db:"id"`
	Kind       string         `json:"kind" db:"kind" enum:"created,comment,customer_reply,status,assigned,escalated,compensation,reopened"`
	Body       *string        `json:"body" db:"body"`
	Internal   bool           `json:"internal" db:"internal" doc:"Internal note, not shown to the customer"`
	FromStatus *string        `json:"fromStatus" db:"from_status"`
	ToStatus   *string        `json:"toStatus" db:"to_status"`
	Details    map[string]any `json:"details" db:"details"`
	ActorType  string         `json:"actorType" db:"actor_type" enum:"staff,customer,system"`
	ActorName  *string        `json:"actorName" db:"actor_name"`
	CreatedAt  time.Time      `json:"createdAt" db:"created_at"`
}

func ticketEvents(ctx context.Context, q dbtx.Querier, tid uuid.UUID, public bool) ([]TicketEvent, error) {
	return handle.List[TicketEvent](q.Query(ctx, `SELECT e.id, e.kind, e.body, e.internal, e.from_status, e.to_status, e.details, e.actor_type,
		CASE WHEN e.actor_type = 'customer' THEN NULL ELSE u.full_name END AS actor_name, e.created_at FROM crm.ticket_events e
		LEFT JOIN platform.users u ON u.id = e.created_by WHERE e.ticket_id = $1 AND (NOT $2 OR NOT e.internal) ORDER BY e.created_at, e.id`, tid, public))
}

func addEvent(ctx context.Context, tx pgx.Tx, t ComplaintTicket, kind, body string, internal bool, from, to string, details map[string]any, actorType string) error {
	if details == nil {
		details = map[string]any{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO crm.ticket_events (id, property_id, ticket_id, kind, body, internal, from_status, to_status, details, actor_type, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id.New(), t.PropertyID, t.ID, kind, nullStr(body), internal, nullStr(from), nullStr(to), details,
		actorType, actor(ctx))
	return err
}

// TicketDetail is a ticket with its history and compensations.
type TicketDetail struct {
	ComplaintTicket
	Events        []TicketEvent        `json:"events"`
	Compensations []TicketCompensation `json:"compensations"`
}

func ticketDetail(ctx context.Context, q dbtx.Querier, tid uuid.UUID, public bool) (TicketDetail, error) {
	t, err := GetTicket(ctx, q, tid)
	if err != nil {
		return TicketDetail{}, err
	}
	out := TicketDetail{ComplaintTicket: t, Compensations: []TicketCompensation{}}
	if out.Events, err = ticketEvents(ctx, q, tid, public); err != nil {
		return out, err
	}
	if !public {
		out.Compensations, err = handle.List[TicketCompensation](q.Query(ctx, compensationSelect+` WHERE ticket_id = $1 ORDER BY created_at`, tid))
	}
	return out, err
}

// ── SLA in business hours (FR-TKT-02) ─────────────────────────────────────

func hm(s string, def int) int {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return def
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return def
	}
	return h*60 + m
}

// addBusiness adds minutes counted in the business hours of the policy.
func addBusiness(start time.Time, minutes int, pol ComplaintPolicy, loc *time.Location) time.Time {
	open, closeAt := hm(pol.BusinessHoursStart, 8*60), hm(pol.BusinessHoursEnd, 20*60)
	if minutes <= 0 {
		return start
	}
	if closeAt <= open || len(pol.BusinessDays) == 0 {
		return start.Add(time.Duration(minutes) * time.Minute)
	}
	t := start.In(loc)
	remaining := minutes
	for i := 0; i < 1000; i++ {
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		wd := int(day.Weekday())
		if wd == 0 {
			wd = 7
		}
		next := day.AddDate(0, 0, 1)
		if !slices.Contains(pol.BusinessDays, wd) {
			t = next
			continue
		}
		o, c := day.Add(time.Duration(open)*time.Minute), day.Add(time.Duration(closeAt)*time.Minute)
		if t.Before(o) {
			t = o
		}
		if !t.Before(c) {
			t = next
			continue
		}
		avail := int(c.Sub(t) / time.Minute)
		if remaining <= avail {
			return t.Add(time.Duration(remaining) * time.Minute)
		}
		remaining -= avail
		t = next
	}
	return t
}

func target(pol ComplaintPolicy, priority string) SLATarget {
	for _, t := range pol.Targets {
		if t.Priority == priority {
			return t
		}
	}
	return SLATarget{Priority: priority, FirstResponseMinutes: 240, ResolutionMinutes: 1440}
}

// dueDates computes the SLA due times of a ticket opened at start.
func dueDates(ctx context.Context, q dbtx.Querier, property uuid.UUID, priority string, category *uuid.UUID, start time.Time) (time.Time, time.Time, int, error) {
	pol, version, err := complaintPolicy(ctx, q, property)
	if err != nil {
		return start, start, 0, err
	}
	tg := target(pol, priority)
	if category != nil {
		var fr, res *int
		if err := q.QueryRow(ctx, `SELECT first_response_minutes, resolution_minutes FROM crm.ticket_categories WHERE id = $1`, *category).Scan(&fr, &res); err != nil &&
			!dbtx.IsNoRows(err) {
			return start, start, 0, err
		}
		if fr != nil {
			tg.FirstResponseMinutes = *fr
		}
		if res != nil {
			tg.ResolutionMinutes = *res
		}
	}
	loc := location(ctx, q, property)
	return addBusiness(start, tg.FirstResponseMinutes, pol, loc), addBusiness(start, tg.ResolutionMinutes, pol, loc), version, nil
}

// ── create ────────────────────────────────────────────────────────────────

// TicketInput opens a ticket (staff).
type TicketInput struct {
	CustomerID        *uuid.UUID  `json:"customerId,omitempty"`
	ContactName       string      `json:"contactName,omitempty" doc:"Contact of a complainant without a customer profile"`
	ContactEmail      string      `json:"contactEmail,omitempty"`
	ContactPhone      string      `json:"contactPhone,omitempty"`
	CategoryID        *uuid.UUID  `json:"categoryId,omitempty"`
	BusinessLine      string      `json:"businessLine,omitempty" enum:"golf,sportclub,stay,pos,membership,banquet,other" doc:"Default: from the category, else other"`
	Priority          string      `json:"priority,omitempty" enum:"low,medium,high,urgent" doc:"Default: from the category, else medium"`
	Channel           string      `json:"channel,omitempty" enum:"staff,whatsapp,email,phone" doc:"How the complaint came in (default staff)"`
	Subject           string      `json:"subject"`
	Description       string      `json:"description"`
	AttachmentFileIDs []uuid.UUID `json:"attachmentFileIds,omitempty" doc:"Uploaded files (platform files)"`
	DepartmentID      *uuid.UUID  `json:"departmentId,omitempty"`
	AssignedTo        *uuid.UUID  `json:"assignedTo,omitempty"`
}

func lineOfContext(ctxType string) string {
	switch ctxType {
	case "round":
		return "golf"
	case "stay", "meeting":
		return "stay"
	case "class", "sport":
		return "sportclub"
	case "fnb":
		return "pos"
	}
	return "other"
}

// CreateTicket opens a ticket with its SLA timers.
func (s *Service) CreateTicket(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TicketInput, channel, actorType string, feedback *uuid.UUID) (ComplaintTicket, error) {
	in.Subject, in.Description = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Description)
	if err := handle.Required("subject", in.Subject); err != nil {
		return ComplaintTicket{}, err
	}
	if err := handle.Required("description", in.Description); err != nil {
		return ComplaintTicket{}, err
	}
	if in.CustomerID != nil {
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return ComplaintTicket{}, err
		}
		if c.PropertyID != property {
			return ComplaintTicket{}, errs.NotFound("customer")
		}
		in.CustomerID = &c.ID
		if in.ContactName == "" {
			in.ContactName = c.Name
		}
		if in.ContactEmail == "" {
			in.ContactEmail = c.Email
		}
		if in.ContactPhone == "" {
			in.ContactPhone = c.Phone
		}
	} else if strings.TrimSpace(in.ContactName) == "" {
		return ComplaintTicket{}, errs.Validation("complainant_required", "a customer or the contact name is required",
			errs.Field("customerId", "required", "customer or contactName"))
	}
	if in.CategoryID != nil {
		var line *string
		var prio string
		var dept *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT business_line, default_priority, department_id FROM crm.ticket_categories WHERE id = $1 AND property_id = $2
			AND archived_at IS NULL AND status = 'active'`, *in.CategoryID, property).Scan(&line, &prio, &dept); err != nil {
			if dbtx.IsNoRows(err) {
				return ComplaintTicket{}, handle.Invalid("categoryId", "not_found", "category not found in this property")
			}
			return ComplaintTicket{}, err
		}
		if in.BusinessLine == "" && line != nil {
			in.BusinessLine = *line
		}
		if in.Priority == "" {
			in.Priority = prio
		}
		if in.DepartmentID == nil {
			in.DepartmentID = dept
		}
	}
	if in.BusinessLine == "" {
		in.BusinessLine = "other"
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	if !slices.Contains(BusinessLines, in.BusinessLine) {
		return ComplaintTicket{}, handle.Invalid("businessLine", "invalid_line", "unknown business line")
	}
	if !slices.Contains(Priorities, in.Priority) {
		return ComplaintTicket{}, handle.Invalid("priority", "invalid_priority", "priority must be low, medium, high or urgent")
	}
	if err := s.checkAssignee(ctx, tx, property, in.DepartmentID, in.AssignedTo); err != nil {
		return ComplaintTicket{}, err
	}
	if len(in.AttachmentFileIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.files WHERE id = ANY($1::uuid[])`, in.AttachmentFileIDs).Scan(&n); err != nil {
			return ComplaintTicket{}, err
		}
		if n != len(in.AttachmentFileIDs) {
			return ComplaintTicket{}, handle.Invalid("attachmentFileIds", "not_found", "an attachment was not found")
		}
	}
	if in.AttachmentFileIDs == nil {
		in.AttachmentFileIDs = []uuid.UUID{}
	}
	now := clock.Now()
	frDue, resDue, version, err := dueDates(ctx, tx, property, in.Priority, in.CategoryID, now)
	if err != nil {
		return ComplaintTicket{}, err
	}
	num, err := numbering.Next(ctx, tx, property, "TKT", localToday(ctx, tx, property))
	if err != nil {
		return ComplaintTicket{}, err
	}
	tid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.tickets (id, property_id, number, customer_id, contact_name, contact_email, contact_phone, category_id,
		business_line, priority, channel, subject, description, attachment_file_ids, feedback_id, department_id, assigned_to, first_response_due_at,
		resolution_due_at, sla_policy_version, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$21)`,
		tid, property, num, in.CustomerID, nullStr(in.ContactName), nullStr(strings.ToLower(in.ContactEmail)), nullStr(crm.NormalizePhone(in.ContactPhone)),
		in.CategoryID, in.BusinessLine, in.Priority, channel, in.Subject, in.Description, in.AttachmentFileIDs, feedback, in.DepartmentID, in.AssignedTo,
		frDue, resDue, version, actor(ctx)); err != nil {
		return ComplaintTicket{}, err
	}
	t, err := GetTicket(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if err := addEvent(ctx, tx, t, "created", in.Description, false, "", "open", map[string]any{"channel": channel, "priority": in.Priority}, actorType); err != nil {
		return t, err
	}
	if in.CustomerID != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, body, source, ref_type, ref_id)
			VALUES ($1,$2,$3,$4,'inbound',$5,$6,'ticket','crm.ticket',$7)`, id.New(), property, *in.CustomerID, interactionChannel(channel),
			"Complaint "+num+" · "+in.Subject, in.Description, tid); err != nil {
			return t, err
		}
	}
	if err := s.notifyCustomer(ctx, tx, t, "crm.ticket_created", map[string]any{"message": "We received your complaint and will respond by " +
		t.FirstResponseDueAt.In(location(ctx, tx, property)).Format("02 Jan 15:04") + "."}); err != nil {
		return t, err
	}
	if in.AssignedTo != nil {
		if err := s.notifyStaff(ctx, tx, t, "crm.ticket_assigned", []uuid.UUID{*in.AssignedTo}, ""); err != nil {
			return t, err
		}
	}
	if s.Events != nil {
		if _, err := s.Events.Publish(ctx, tx, EventTicketCreated, "crm.ticket", &tid, &property, map[string]any{"ticketId": tid, "number": num,
			"customerId": in.CustomerID, "businessLine": in.BusinessLine, "priority": in.Priority, "channel": channel}); err != nil {
			return t, err
		}
	}
	return t, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.ticket", EntityID: tid.String(),
		EntityLabel: num, PropertyID: &property, After: t})
}

func interactionChannel(ch string) string {
	switch ch {
	case "whatsapp", "email", "phone":
		return ch
	case "member_app":
		return "in_app"
	}
	return "other"
}

func (s *Service) checkAssignee(ctx context.Context, q dbtx.Querier, property uuid.UUID, dept, user *uuid.UUID) error {
	if dept != nil {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.departments WHERE id = $1 AND property_id = $2)`, *dept, property).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid("departmentId", "not_found", "department not found in this property")
		}
	}
	if user != nil {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.users WHERE id = $1 AND status = 'active')`, *user).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid("assignedTo", "not_found", "user not found")
		}
	}
	return nil
}

// notifyCustomer sends a ticket update to the complainant (portal user,
// e-mail or WhatsApp of the contact).
func (s *Service) notifyCustomer(ctx context.Context, tx pgx.Tx, t ComplaintTicket, event string, data map[string]any) error {
	if s.Notify == nil {
		return nil
	}
	data["number"], data["subject"], data["status"] = t.Number, t.Subject, strings.ReplaceAll(t.Status, "_", " ")
	data["name"] = deref(t.ContactName)
	msg := notify.Message{Event: event, Category: "support", PropertyID: &t.PropertyID, Data: data, Link: "/support/tickets/" + t.ID.String(), Mandatory: true}
	ok := false
	if t.CustomerID != nil {
		var err error
		if ok, err = crm.Recipient(ctx, tx, *t.CustomerID, &msg); err != nil {
			return err
		}
	}
	if !ok {
		switch {
		case t.ContactEmail != nil:
			msg.Email, msg.Name, msg.Channels, ok = *t.ContactEmail, deref(t.ContactName), []string{notify.ChannelEmail}, true
		case t.ContactPhone != nil:
			msg.Phone, msg.Name, msg.Channels, ok = *t.ContactPhone, deref(t.ContactName), []string{notify.ChannelWhatsApp}, true
		}
	}
	if !ok {
		return nil
	}
	return s.Notify.Send(ctx, tx, msg)
}

// notifyStaff alerts staff about a ticket (assignment, escalation, reply).
func (s *Service) notifyStaff(ctx context.Context, tx pgx.Tx, t ComplaintTicket, event string, users []uuid.UUID, reason string) error {
	if s.Notify == nil || len(users) == 0 {
		return nil
	}
	return s.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "support", UserIDs: users, PropertyID: &t.PropertyID, Mandatory: true,
		Link: "/crm/complaints/" + t.ID.String(), Data: map[string]any{"number": t.Number, "subject": t.Subject, "priority": t.Priority,
			"businessLine": t.BusinessLine, "level": t.EscalationLevel, "reason": reason, "customer": deref(t.CustomerName)}})
}

// ── workflow ──────────────────────────────────────────────────────────────

// TicketAssignInput routes a ticket to a department and / or a user.
type TicketAssignInput struct {
	DepartmentID *uuid.UUID `json:"departmentId,omitempty"`
	AssignedTo   *uuid.UUID `json:"assignedTo,omitempty"`
}

// Assign routes a ticket.
func (s *Service) Assign(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TicketAssignInput) (ComplaintTicket, error) {
	if in.DepartmentID == nil && in.AssignedTo == nil {
		return ComplaintTicket{}, handle.Invalid("assignedTo", "required", "choose a department or a user")
	}
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status == "closed" {
		return t, errs.Conflict("ticket_closed", "ticket "+t.Number+" is closed")
	}
	if err := s.checkAssignee(ctx, tx, property, in.DepartmentID, in.AssignedTo); err != nil {
		return t, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.tickets SET department_id = coalesce($2, department_id), assigned_to = coalesce($3, assigned_to), updated_by = $4
		WHERE id = $1`, tid, in.DepartmentID, in.AssignedTo, actor(ctx)); err != nil {
		return t, err
	}
	after, err := GetTicket(ctx, tx, tid)
	if err != nil {
		return after, err
	}
	if err := addEvent(ctx, tx, after, "assigned", "", true, "", "", map[string]any{"departmentId": in.DepartmentID, "assignedTo": in.AssignedTo}, "staff"); err != nil {
		return after, err
	}
	if in.AssignedTo != nil {
		if err := s.notifyStaff(ctx, tx, after, "crm.ticket_assigned", []uuid.UUID{*in.AssignedTo}, ""); err != nil {
			return after, err
		}
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "assign", EntityType: "crm.ticket", EntityID: tid.String(),
		EntityLabel: t.Number, PropertyID: &property, Before: map[string]any{"departmentId": t.DepartmentID, "assignedTo": t.AssignedTo},
		After: map[string]any{"departmentId": after.DepartmentID, "assignedTo": after.AssignedTo}})
}

// TicketCommentInput adds a reply to the customer or an internal note.
type TicketCommentInput struct {
	Body     string `json:"body"`
	Internal bool   `json:"internal,omitempty" doc:"Internal note (not sent to the customer)"`
}

func (s *Service) setStatus(ctx context.Context, tx pgx.Tx, t ComplaintTicket, to string) error {
	if t.Status == to {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE crm.tickets SET status = $2, updated_by = $3 WHERE id = $1`, t.ID, to, actor(ctx))
	return err
}

// Comment records a staff reply (the first one stops the first-response
// timer and starts the work: Open → In Progress) or an internal note.
func (s *Service) Comment(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TicketCommentInput) (TicketDetail, error) {
	if err := handle.Required("body", in.Body); err != nil {
		return TicketDetail{}, err
	}
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return TicketDetail{}, err
	}
	if t.Status == "closed" {
		return TicketDetail{}, errs.Conflict("ticket_closed", "ticket "+t.Number+" is closed; reopen it first")
	}
	if !in.Internal {
		if t.FirstRespondedAt == nil {
			now := clock.Now()
			if _, err := tx.Exec(ctx, `UPDATE crm.tickets SET first_responded_at = $2, first_response_breached = $2 > first_response_due_at WHERE id = $1`, tid, now); err != nil {
				return TicketDetail{}, err
			}
		}
		if t.Status == "open" {
			if err := s.setStatus(ctx, tx, t, "in_progress"); err != nil {
				return TicketDetail{}, err
			}
		}
	}
	after, err := GetTicket(ctx, tx, tid)
	if err != nil {
		return TicketDetail{}, err
	}
	if err := addEvent(ctx, tx, after, "comment", in.Body, in.Internal, t.Status, after.Status, nil, "staff"); err != nil {
		return TicketDetail{}, err
	}
	if !in.Internal {
		if err := s.notifyCustomer(ctx, tx, after, "crm.ticket_update", map[string]any{"message": in.Body}); err != nil {
			return TicketDetail{}, err
		}
		if after.CustomerID != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, body, source, ref_type, ref_id)
				VALUES ($1,$2,$3,'other','outbound',$4,$5,'ticket','crm.ticket',$6)`, id.New(), property, *after.CustomerID, "Complaint "+after.Number+" · reply",
				in.Body, tid); err != nil {
				return TicketDetail{}, err
			}
		}
	}
	d, err := ticketDetail(ctx, tx, tid, false)
	if err != nil {
		return d, err
	}
	return d, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "comment", EntityType: "crm.ticket", EntityID: tid.String(), EntityLabel: t.Number,
		PropertyID: &property, After: map[string]any{"internal": in.Internal, "status": after.Status}})
}

// TicketReasonInput carries a reason (escalate, reopen, close).
type TicketReasonInput struct {
	Reason string `json:"reason"`
}

// escalate raises the escalation level and alerts the line manager (level
// 1) or the top role (level 2).
func (s *Service) escalate(ctx context.Context, tx pgx.Tx, t ComplaintTicket, reason, actorType string) (ComplaintTicket, error) {
	if t.EscalationLevel >= 2 {
		return t, errs.Conflict("max_escalation", "ticket "+t.Number+" is already escalated to the highest level")
	}
	pol, _, err := complaintPolicy(ctx, tx, t.PropertyID)
	if err != nil {
		return t, err
	}
	level := t.EscalationLevel + 1
	role := pol.TopEscalationRole
	if level == 1 {
		role = pol.LineManagers[t.BusinessLine]
		if role == "" {
			role = pol.LineManagers["other"]
		}
	}
	if role == "" {
		role = "general_manager"
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.tickets SET escalation_level = $2, escalated_at = now(), status = 'escalated', updated_by = $3 WHERE id = $1`,
		t.ID, level, actor(ctx)); err != nil {
		return t, err
	}
	after, err := GetTicket(ctx, tx, t.ID)
	if err != nil {
		return after, err
	}
	users, err := roleHolders(ctx, tx, t.PropertyID, role)
	if err != nil {
		return after, err
	}
	if err := addEvent(ctx, tx, after, "escalated", reason, true, t.Status, "escalated", map[string]any{"level": level, "role": role,
		"notified": len(users)}, actorType); err != nil {
		return after, err
	}
	if err := s.notifyStaff(ctx, tx, after, "crm.ticket_escalated", users, reason); err != nil {
		return after, err
	}
	if s.Events != nil {
		pid := t.PropertyID
		if _, err := s.Events.Publish(ctx, tx, EventTicketEscalate, "crm.ticket", &t.ID, &pid, map[string]any{"ticketId": t.ID, "number": t.Number,
			"level": level, "role": role, "reason": reason, "priority": t.Priority, "businessLine": t.BusinessLine}); err != nil {
			return after, err
		}
	}
	return after, nil
}

// Escalate escalates a ticket manually.
func (s *Service) Escalate(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TicketReasonInput) (ComplaintTicket, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return ComplaintTicket{}, err
	}
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status == "resolved" || t.Status == "closed" {
		return t, errs.Conflict("ticket_done", "ticket "+t.Number+" is "+t.Status)
	}
	after, err := s.escalate(ctx, tx, t, in.Reason, "staff")
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "escalate", EntityType: "crm.ticket", EntityID: tid.String(), EntityLabel: t.Number,
		PropertyID: &property, Reason: in.Reason, After: map[string]any{"level": after.EscalationLevel}})
}

// TicketResolveInput resolves a ticket.
type TicketResolveInput struct {
	Resolution string `json:"resolution"`
}

// Resolve closes the work on a ticket and informs the customer.
func (s *Service) Resolve(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TicketResolveInput) (ComplaintTicket, error) {
	if err := handle.Required("resolution", in.Resolution); err != nil {
		return ComplaintTicket{}, err
	}
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status == "resolved" || t.Status == "closed" {
		return t, errs.Conflict("ticket_done", "ticket "+t.Number+" is "+t.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.tickets SET status = 'resolved', resolved_at = now(), resolution = $2, first_responded_at = coalesce(first_responded_at, now()),
		first_response_breached = coalesce(first_responded_at, now()) > first_response_due_at, resolution_breached = now() > resolution_due_at, updated_by = $3
		WHERE id = $1`, tid, in.Resolution, actor(ctx)); err != nil {
		return t, err
	}
	after, err := GetTicket(ctx, tx, tid)
	if err != nil {
		return after, err
	}
	if err := addEvent(ctx, tx, after, "status", in.Resolution, false, t.Status, "resolved", nil, "staff"); err != nil {
		return after, err
	}
	if err := s.notifyCustomer(ctx, tx, after, "crm.ticket_update", map[string]any{"message": in.Resolution}); err != nil {
		return after, err
	}
	if s.Events != nil {
		if _, err := s.Events.Publish(ctx, tx, EventTicketResolved, "crm.ticket", &tid, &property, map[string]any{"ticketId": tid, "number": t.Number,
			"customerId": t.CustomerID, "resolutionBreached": after.ResolutionBreached, "firstResponseBreached": after.FirstResponseBreached}); err != nil {
			return after, err
		}
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.ticket", EntityID: tid.String(),
		EntityLabel: t.Number, PropertyID: &property, Before: map[string]any{"status": t.Status}, After: map[string]any{"status": "resolved"}})
}

// Close closes a resolved ticket.
func (s *Service) Close(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TicketReasonInput, actorType string) (ComplaintTicket, error) {
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "resolved" {
		return t, errs.Conflict("not_resolved", "only resolved tickets can be closed")
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.tickets SET status = 'closed', closed_at = now(), updated_by = $2 WHERE id = $1`, tid, actor(ctx)); err != nil {
		return t, err
	}
	after, err := GetTicket(ctx, tx, tid)
	if err != nil {
		return after, err
	}
	if err := addEvent(ctx, tx, after, "status", in.Reason, true, "resolved", "closed", nil, actorType); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.ticket", EntityID: tid.String(),
		EntityLabel: t.Number, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": "resolved"}, After: map[string]any{"status": "closed"}})
}

// Reopen reopens a resolved or closed ticket within the reopen window of the
// Complaint Policies (new SLA timers).
func (s *Service) Reopen(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TicketReasonInput, actorType string) (ComplaintTicket, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return ComplaintTicket{}, err
	}
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "resolved" && t.Status != "closed" {
		return t, errs.Conflict("not_resolved", "ticket "+t.Number+" is still "+strings.ReplaceAll(t.Status, "_", " "))
	}
	pol, _, err := complaintPolicy(ctx, tx, property)
	if err != nil {
		return t, err
	}
	if t.ResolvedAt != nil && pol.ReopenDays > 0 && clock.Now().After(t.ResolvedAt.AddDate(0, 0, pol.ReopenDays)) {
		return t, errs.Conflict("reopen_window_passed", fmt.Sprintf("tickets can be reopened within %d days of resolution; open a new ticket", pol.ReopenDays))
	}
	frDue, resDue, version, err := dueDates(ctx, tx, property, t.Priority, t.CategoryID, clock.Now())
	if err != nil {
		return t, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.tickets SET status = 'open', reopened_count = reopened_count + 1, resolved_at = NULL, closed_at = NULL,
		first_responded_at = NULL, escalation_level = 0, escalated_at = NULL, first_response_due_at = $2, resolution_due_at = $3, sla_policy_version = $4,
		first_response_breached = false, resolution_breached = false, updated_by = $5 WHERE id = $1`, tid, frDue, resDue, version, actor(ctx)); err != nil {
		return t, err
	}
	after, err := GetTicket(ctx, tx, tid)
	if err != nil {
		return after, err
	}
	if err := addEvent(ctx, tx, after, "reopened", in.Reason, false, t.Status, "open", nil, actorType); err != nil {
		return after, err
	}
	if after.AssignedTo != nil && actorType == "customer" {
		if err := s.notifyStaff(ctx, tx, after, "crm.ticket_assigned", []uuid.UUID{*after.AssignedTo}, "reopened by the customer: "+in.Reason); err != nil {
			return after, err
		}
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "reopen", EntityType: "crm.ticket", EntityID: tid.String(), EntityLabel: t.Number,
		PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": t.Status}, After: map[string]any{"status": "open"}})
}

// ── compensation (FR-TKT-05) ──────────────────────────────────────────────

// TicketCompensationInput proposes a compensation for a ticket.
type TicketCompensationInput struct {
	Type        string `json:"type" enum:"points,voucher,refund,other"`
	Points      int64  `json:"points,omitempty" doc:"Loyalty points (type points)"`
	Amount      string `json:"amount,omitempty" doc:"Value of a voucher / refund (issued in Commercial / Billing once approved)"`
	Description string `json:"description"`
}

// TicketCompensation is a compensation of a ticket.
type TicketCompensation struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Number            string     `json:"number" db:"number"`
	TicketID          uuid.UUID  `json:"ticketId" db:"ticket_id"`
	Type              string     `json:"type" db:"comp_type" enum:"points,voucher,refund,other"`
	Points            *int64     `json:"points" db:"points"`
	Amount            *string    `json:"amount" db:"amount"`
	Description       string     `json:"description" db:"description"`
	Status            string     `json:"status" db:"status" enum:"pending,approved,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	LedgerID          *uuid.UUID `json:"ledgerId" db:"ledger_id"`
	DecidedAt         *time.Time `json:"decidedAt" db:"decided_at"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

const compensationSelect = `SELECT id, number, ticket_id, comp_type, points, trim_scale(amount)::text AS amount, description, status, approval_request_id,
	ledger_id, decided_at, created_at FROM crm.ticket_compensations`

// Compensate records a compensation and submits it for approval; approved
// points are posted to the customer's loyalty account.
func (s *Service) Compensate(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TicketCompensationInput) (TicketCompensation, error) {
	if !slices.Contains([]string{"points", "voucher", "refund", "other"}, in.Type) {
		return TicketCompensation{}, handle.Invalid("type", "invalid_type", "type must be points, voucher, refund or other")
	}
	if err := handle.Required("description", in.Description); err != nil {
		return TicketCompensation{}, err
	}
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return TicketCompensation{}, err
	}
	var points *int64
	var amount *string
	if in.Type == "points" {
		if in.Points <= 0 {
			return TicketCompensation{}, handle.Invalid("points", "invalid_points", "points above 0 are required")
		}
		if t.CustomerID == nil {
			return TicketCompensation{}, errs.Conflict("no_customer", "the complainant has no customer profile for loyalty points")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_accounts WHERE customer_id = $1 AND status = 'active')`, *t.CustomerID).Scan(&ok); err != nil {
			return TicketCompensation{}, err
		}
		if !ok {
			return TicketCompensation{}, errs.Conflict("no_loyalty_account", "the customer has no active loyalty account")
		}
		points = &in.Points
	} else {
		a, err := handle.Decimal("amount", in.Amount, decimal.Zero)
		if err != nil {
			return TicketCompensation{}, err
		}
		if a.IsPositive() {
			s := a.String()
			amount = &s
		} else if in.Type != "other" {
			return TicketCompensation{}, handle.Invalid("amount", "required", "the amount is required")
		}
	}
	num, err := numbering.Next(ctx, tx, property, "TCP", localToday(ctx, tx, property))
	if err != nil {
		return TicketCompensation{}, err
	}
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.ticket_compensations (id, property_id, number, ticket_id, comp_type, points, amount, description, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$9)`, cid, property, num, tid, in.Type, points, amount, in.Description, actor(ctx)); err != nil {
		return TicketCompensation{}, err
	}
	if err := addEvent(ctx, tx, t, "compensation", in.Description, true, "", "", map[string]any{"type": in.Type, "points": points, "amount": amount,
		"number": num}, "staff"); err != nil {
		return TicketCompensation{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.ticket_compensation", EntityID: cid.String(),
		EntityLabel: num, PropertyID: &property, After: map[string]any{"ticket": t.Number, "type": in.Type, "points": points, "amount": amount}}); err != nil {
		return TicketCompensation{}, err
	}
	if s.Approvals == nil {
		return TicketCompensation{}, errs.Unavailable("approvals are not wired")
	}
	attrs := map[string]any{"type": in.Type, "points": in.Points, "amount": 0}
	if amount != nil {
		f, _ := strconv.ParseFloat(*amount, 64)
		attrs["amount"] = f
	}
	rid, _, err := s.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: CompensationDocumentType.Code, DocumentID: cid, DocumentRef: num,
		Title: "Compensation " + in.Type + " for " + t.Number, PropertyID: property, Attributes: attrs})
	if err != nil {
		return TicketCompensation{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.ticket_compensations SET approval_request_id = $2 WHERE id = $1`, cid, rid); err != nil {
		return TicketCompensation{}, err
	}
	rows, err := tx.Query(ctx, compensationSelect+` WHERE id = $1`, cid)
	return handle.One[TicketCompensation](rows, err, "compensation")
}

// CompensationDecision applies the approval outcome of a compensation.
func (s *Service) CompensationDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var tid uuid.UUID
	var typ, num, desc, status string
	var points *int64
	err := tx.QueryRow(ctx, `SELECT ticket_id, comp_type, number, description, status, points FROM crm.ticket_compensations WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&tid, &typ, &num, &desc, &status, &points)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil || status != "pending" {
		return err
	}
	newStatus := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	if newStatus == "" {
		return nil
	}
	t, err := GetTicket(ctx, tx, tid)
	if err != nil {
		return err
	}
	var ledger *uuid.UUID
	if newStatus == "approved" && typ == "points" && points != nil && t.CustomerID != nil && s.Loyalty != nil {
		var aid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1`, *t.CustomerID).Scan(&aid); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.Conflict("no_loyalty_account", "the customer has no loyalty account")
			}
			return err
		}
		did := d.DocumentID
		e, err := s.Loyalty.PostAdjustment(ctx, tx, aid, *points, "crm.ticket_compensation", &did, num, "compensation:"+did.String(),
			"Compensation "+num+" for complaint "+t.Number+": "+desc, nil)
		if err != nil {
			return err
		}
		ledger = &e.ID
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.ticket_compensations SET status = $2, ledger_id = $3, decided_at = now() WHERE id = $1`, d.DocumentID, newStatus, ledger); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, t, "compensation", "Compensation "+num+" "+newStatus, true, "", "", map[string]any{"status": newStatus}, "system"); err != nil {
		return err
	}
	pid := d.PropertyID
	if newStatus == "approved" && s.Events != nil {
		var amount *string
		if err := tx.QueryRow(ctx, `SELECT trim_scale(amount)::text FROM crm.ticket_compensations WHERE id = $1`, d.DocumentID).Scan(&amount); err != nil {
			return err
		}
		did := d.DocumentID
		if _, err := s.Events.Publish(ctx, tx, EventCompensationApproved, "crm.ticket_compensation", &did, &pid, map[string]any{"compensationId": did,
			"number": num, "ticketId": t.ID, "ticketNumber": t.Number, "customerId": t.CustomerID, "type": typ, "amount": amount, "points": points,
			"description": desc}); err != nil {
			return err
		}
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.ticket_compensation", EntityID: d.DocumentID.String(),
		EntityLabel: num, PropertyID: &pid, Reason: d.Reason, After: map[string]any{"status": newStatus}})
}

// ── feedback → ticket (FR-TKT-01) ─────────────────────────────────────────

// FeedbackTicketInput opens a ticket from a feedback.
type FeedbackTicketInput struct {
	Priority   string     `json:"priority,omitempty" enum:"low,medium,high,urgent"`
	CategoryID *uuid.UUID `json:"categoryId,omitempty"`
	AssignedTo *uuid.UUID `json:"assignedTo,omitempty"`
}

// TicketFromFeedback opens the ticket of a (low score) feedback, once.
func (s *Service) TicketFromFeedback(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID, in FeedbackTicketInput, actorType string) (ComplaintTicket, bool, error) {
	var existing uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.tickets WHERE feedback_id = $1`, fid).Scan(&existing)
	if err == nil {
		t, err := GetTicket(ctx, tx, existing)
		return t, false, err
	}
	if !dbtx.IsNoRows(err) {
		return ComplaintTicket{}, false, err
	}
	var f struct {
		Customer *uuid.UUID
		Context  string
		Label    *string
		Rating   int
		NPS      *int
		Comment  *string
	}
	if err := tx.QueryRow(ctx, `SELECT customer_id, context_type, context_label, rating, nps, comment FROM crm.feedback WHERE id = $1 AND property_id = $2`,
		fid, property).Scan(&f.Customer, &f.Context, &f.Label, &f.Rating, &f.NPS, &f.Comment); err != nil {
		if dbtx.IsNoRows(err) {
			return ComplaintTicket{}, false, errs.NotFound("feedback")
		}
		return ComplaintTicket{}, false, err
	}
	prio := in.Priority
	if prio == "" {
		prio = "medium"
		if f.Rating <= 1 {
			prio = "high"
		}
	}
	label := f.Context
	if f.Label != nil {
		label = *f.Label
	}
	desc := fmt.Sprintf("Feedback %d/5", f.Rating)
	if f.NPS != nil {
		desc += fmt.Sprintf(", NPS %d", *f.NPS)
	}
	if f.Comment != nil && *f.Comment != "" {
		desc += ": " + *f.Comment
	}
	ti := TicketInput{CustomerID: f.Customer, CategoryID: in.CategoryID, BusinessLine: lineOfContext(f.Context), Priority: prio,
		Subject: "Low feedback score · " + label, Description: desc, AssignedTo: in.AssignedTo}
	if f.Customer == nil {
		ti.ContactName = "Anonymous feedback"
	}
	t, err := s.CreateTicket(ctx, tx, property, ti, "feedback", actorType, &fid)
	if err != nil {
		return t, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.feedback SET follow_up = 'open', follow_up_note = coalesce(follow_up_note, $2) WHERE id = $1 AND follow_up = 'none'`,
		fid, "Complaint ticket "+t.Number); err != nil {
		return t, false, err
	}
	return t, true, nil
}

// ── SLA job (FR-TKT-02/03) ────────────────────────────────────────────────

// SLAResult reports a run of the SLA job.
type SLAResult struct {
	Escalated     int `json:"escalated"`
	Closed        int `json:"closed"`
	FromFeedback  int `json:"fromFeedback"`
	EscalationErr int `json:"-"`
}

// RunSLA escalates breached tickets, closes resolved tickets after the
// auto-close window and opens tickets from low feedback, for every property.
func (s *Service) RunSLA(ctx context.Context) (SLAResult, error) {
	ctx = dbtx.System(ctx)
	var res SLAResult
	props, err := s.properties(ctx)
	if err != nil {
		return res, err
	}
	for _, p := range props {
		err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			pol, _, err := complaintPolicy(ctx, tx, p)
			if err != nil {
				return err
			}
			type due struct {
				ID     uuid.UUID `db:"id"`
				Level  int       `db:"escalation_level"`
				Reason string    `db:"reason"`
			}
			list, err := handle.List[due](tx.Query(ctx, `SELECT id, escalation_level, CASE
				  WHEN escalation_level = 0 AND first_responded_at IS NULL AND first_response_due_at < now() THEN 'First response SLA breached'
				  WHEN escalation_level = 0 AND resolution_due_at < now() THEN 'Resolution SLA breached'
				  ELSE 'Still unresolved after escalation' END AS reason
				FROM crm.tickets WHERE property_id = $1 AND status IN ('open', 'in_progress', 'escalated') AND (
				  (escalation_level = 0 AND ((first_responded_at IS NULL AND first_response_due_at < now()) OR resolution_due_at < now()))
				  OR (escalation_level = 1 AND escalated_at < now() - make_interval(mins => $2)))
				ORDER BY resolution_due_at FOR UPDATE SKIP LOCKED`, p, max(pol.SecondEscalationMinutes, 1)))
			if err != nil {
				return err
			}
			for _, d := range list {
				t, err := GetTicket(ctx, tx, d.ID)
				if err != nil {
					return err
				}
				if _, err := s.escalate(ctx, tx, t, d.Reason, "system"); err != nil {
					return err
				}
				res.Escalated++
			}
			if pol.AutoCloseDays > 0 {
				ids, err := idList(ctx, tx, `SELECT id FROM crm.tickets WHERE property_id = $1 AND status = 'resolved' AND resolved_at < now() - make_interval(days => $2)`,
					p, pol.AutoCloseDays)
				if err != nil {
					return err
				}
				for _, tid := range ids {
					if _, err := s.Close(ctx, tx, p, tid, TicketReasonInput{Reason: "closed automatically"}, "system"); err != nil {
						return err
					}
					res.Closed++
				}
			}
			if pol.AutoTicketFromFeedback {
				ids, err := idList(ctx, tx, `SELECT f.id FROM crm.feedback f WHERE f.property_id = $1 AND f.created_at >= now() - interval '30 days'
					AND (f.rating <= $2 OR ($3 AND f.nps IS NOT NULL AND f.nps <= 6)) AND f.follow_up <> 'closed'
					AND NOT EXISTS (SELECT 1 FROM crm.tickets t WHERE t.feedback_id = f.id)`, p, pol.AutoTicketMaxRating, pol.AutoTicketOnDetractor)
				if err != nil {
					return err
				}
				for _, fid := range ids {
					if _, created, err := s.TicketFromFeedback(ctx, tx, p, fid, FeedbackTicketInput{}, "system"); err != nil {
						return err
					} else if created {
						res.FromFeedback++
					}
				}
			}
			return nil
		})
		if err != nil {
			return res, fmt.Errorf("complaint SLA for %s: %w", p, err)
		}
	}
	return res, nil
}

func idList(ctx context.Context, q dbtx.Querier, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}
