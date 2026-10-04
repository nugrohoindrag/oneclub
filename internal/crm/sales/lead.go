package sales

// EP-01 Lead Management: capture with source (FR-LEAD-01/02), duplicate
// detection against customers and open leads (FR-LEAD-03), member referral
// (FR-LEAD-04), assignment manual / round-robin / fixed per line
// (FR-LEAD-05), first-response SLA within business hours (FR-LEAD-06),
// qualify / disqualify and conversion into customer, corporate account and
// opportunity (FR-LEAD-07), consent (FR-LEAD-09) and transfer of a sales'
// leads with history (FR-LEAD-10).

import (
	"context"
	"regexp"
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
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// Lead is a sales lead.
type Lead struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Number             string     `json:"number" db:"number"`
	Name               string     `json:"name" db:"name"`
	CompanyName        *string    `json:"companyName" db:"company_name"`
	Phone              *string    `json:"phone" db:"phone"`
	Email              *string    `json:"email" db:"email"`
	Source             string     `json:"source" db:"source" enum:"whatsapp,instagram,facebook,tiktok,website_form,walk_in,member_referral,email,phone,event,import,other"`
	Channel            *string    `json:"channel" db:"channel" doc:"Business number, department mailbox or campaign"`
	Line               string     `json:"line" db:"line" enum:"wedding,banquet,mice,event,tournament,stay,golf,package,membership,other"`
	EventType          *string    `json:"eventType" db:"event_type"`
	EventDate          *string    `json:"eventDate" db:"event_date"`
	Pax                *int       `json:"pax" db:"pax"`
	Budget             *string    `json:"budget" db:"budget"`
	Currency           string     `json:"currency" db:"currency"`
	Notes              *string    `json:"notes" db:"notes"`
	Message            *string    `json:"message" db:"message" doc:"First inbound message"`
	Status             string     `json:"status" db:"status" enum:"new,contacted,qualified,unqualified,converted"`
	OwnerUserID        *uuid.UUID `json:"ownerUserId" db:"owner_user_id"`
	OwnerName          *string    `json:"ownerName" db:"owner_name"`
	AssignedAt         *time.Time `json:"assignedAt" db:"assigned_at"`
	AssignmentMethod   *string    `json:"assignmentMethod" db:"assignment_method"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id" doc:"Matched (duplicate) or converted customer — Customer 360"`
	CustomerName       *string    `json:"customerName" db:"customer_name"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	ReferrerCustomerID *uuid.UUID `json:"referrerCustomerId" db:"referrer_customer_id" doc:"Referring member (Member Referral)"`
	ReferrerName       *string    `json:"referrerName" db:"referrer_name"`
	MarketingConsent   bool       `json:"marketingConsent" db:"marketing_consent"`
	ConsentAt          *time.Time `json:"consentAt" db:"consent_at"`
	FirstResponseDueAt *time.Time `json:"firstResponseDueAt" db:"first_response_due_at"`
	FirstRespondedAt   *time.Time `json:"firstRespondedAt" db:"first_responded_at"`
	SLAStatus          string     `json:"slaStatus" db:"sla_status" enum:"pending,met,breached,overdue,none"`
	SLAEscalatedAt     *time.Time `json:"slaEscalatedAt" db:"sla_escalated_at"`
	QualifiedAt        *time.Time `json:"qualifiedAt" db:"qualified_at"`
	UnqualifiedAt      *time.Time `json:"unqualifiedAt" db:"unqualified_at"`
	UnqualifiedReason  *string    `json:"unqualifiedReason" db:"unqualified_reason"`
	UnqualifiedNote    *string    `json:"unqualifiedNote" db:"unqualified_note"`
	ConvertedAt        *time.Time `json:"convertedAt" db:"converted_at"`
	OpportunityID      *uuid.UUID `json:"opportunityId" db:"opportunity_id"`
	LastActivityAt     *time.Time `json:"lastActivityAt" db:"last_activity_at"`
	ExternalRef        *string    `json:"externalRef" db:"external_ref"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt          time.Time  `json:"updatedAt" db:"updated_at"`
}

const leadSelect = `SELECT l.id, l.number, l.name, l.company_name, l.phone, l.email, l.source, l.channel, l.line, l.event_type,
	to_char(l.event_date, 'YYYY-MM-DD') AS event_date, l.pax, trim_scale(l.budget)::text AS budget, l.currency, l.notes, l.message, l.status,
	l.owner_user_id, u.full_name AS owner_name, l.assigned_at, l.assignment_method, l.customer_id, c.name AS customer_name, l.corporate_account_id,
	l.referrer_customer_id, rc.name AS referrer_name, l.marketing_consent, l.consent_at, l.first_response_due_at, l.first_responded_at,
	CASE WHEN l.first_responded_at IS NOT NULL AND l.first_response_due_at IS NOT NULL
	       THEN CASE WHEN l.first_responded_at <= l.first_response_due_at THEN 'met' ELSE 'breached' END
	     WHEN l.first_response_due_at IS NULL THEN 'none'
	     WHEN l.first_response_due_at < now() THEN 'overdue' ELSE 'pending' END AS sla_status,
	l.sla_escalated_at, l.qualified_at, l.unqualified_at, l.unqualified_reason, l.unqualified_note, l.converted_at, l.opportunity_id,
	l.last_activity_at, l.external_ref, l.created_at, l.updated_at
	FROM crm.sales_leads l LEFT JOIN platform.users u ON u.id = l.owner_user_id LEFT JOIN crm.customers c ON c.id = l.customer_id
	LEFT JOIN crm.customers rc ON rc.id = l.referrer_customer_id`

// GetLead loads a lead.
func GetLead(ctx context.Context, q dbtx.Querier, lid uuid.UUID) (Lead, error) {
	return getOne[Lead]("lead")(q.Query(ctx, leadSelect+` WHERE l.id = $1`, lid))
}

func lockLead(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID) (Lead, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_leads WHERE id = $1 AND property_id = $2 FOR UPDATE`, lid, property).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Lead{}, errs.NotFound("lead")
		}
		return Lead{}, err
	}
	return GetLead(ctx, tx, lid)
}

// LeadDuplicate is a customer or open lead with the same phone or e-mail.
type LeadDuplicate struct {
	Kind      string    `json:"kind" enum:"customer,lead"`
	ID        uuid.UUID `json:"id"`
	Reference string    `json:"reference" doc:"Customer code or lead number"`
	Name      string    `json:"name"`
	MatchedOn string    `json:"matchedOn" enum:"phone,email"`
	Status    string    `json:"status"`
}

// LeadAssignment is one assignment of a lead.
type LeadAssignment struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	FromUserID *uuid.UUID `json:"fromUserId" db:"from_user_id"`
	FromName   *string    `json:"fromName" db:"from_name"`
	ToUserID   *uuid.UUID `json:"toUserId" db:"to_user_id"`
	ToName     *string    `json:"toName" db:"to_name"`
	Method     string     `json:"method" db:"method" enum:"manual,round_robin,fixed,transfer,import"`
	Reason     *string    `json:"reason" db:"reason"`
	AssignedAt time.Time  `json:"assignedAt" db:"assigned_at"`
}

// LeadDetail is a lead with its history.
type LeadDetail struct {
	Lead
	Duplicates  []LeadDuplicate  `json:"duplicates" doc:"Possible duplicates (warning, FR-LEAD-03)"`
	Activities  []Activity       `json:"activities"`
	Assignments []LeadAssignment `json:"assignments"`
}

// LeadDetailOf loads a lead with duplicates, activities and assignments.
func LeadDetailOf(ctx context.Context, q dbtx.Querier, lid uuid.UUID) (LeadDetail, error) {
	l, err := GetLead(ctx, q, lid)
	if err != nil {
		return LeadDetail{}, err
	}
	d := LeadDetail{Lead: l}
	var property uuid.UUID
	if err := q.QueryRow(ctx, `SELECT property_id FROM crm.sales_leads WHERE id = $1`, lid).Scan(&property); err != nil {
		return d, err
	}
	if d.Duplicates, err = findDuplicates(ctx, q, property, deref(l.Phone), deref(l.Email), &lid); err != nil {
		return d, err
	}
	if d.Activities, err = listActivities(ctx, q, `a.lead_id = $1`, lid); err != nil {
		return d, err
	}
	d.Assignments, err = handle.List[LeadAssignment](q.Query(ctx, `SELECT a.id, a.from_user_id, fu.full_name AS from_name, a.to_user_id,
		tu.full_name AS to_name, a.method, a.reason, a.assigned_at FROM crm.sales_lead_assignments a
		LEFT JOIN platform.users fu ON fu.id = a.from_user_id LEFT JOIN platform.users tu ON tu.id = a.to_user_id
		WHERE a.lead_id = $1 ORDER BY a.assigned_at, a.id`, lid))
	return d, err
}

// findDuplicates lists customers and open leads with the same phone or
// e-mail (FR-LEAD-03, P1 FR-CUS-03 rules).
func findDuplicates(ctx context.Context, q dbtx.Querier, property uuid.UUID, phone, email string, self *uuid.UUID) ([]LeadDuplicate, error) {
	phone = crm.NormalizePhone(phone)
	email = strings.ToLower(strings.TrimSpace(email))
	out := []LeadDuplicate{}
	if phone == "" && email == "" {
		return out, nil
	}
	custs, err := crm.FindDuplicates(ctx, q, property, phone, email, "")
	if err != nil {
		return nil, err
	}
	for _, c := range custs {
		out = append(out, LeadDuplicate{Kind: "customer", ID: c.ID, Reference: c.Code, Name: c.Name, MatchedOn: c.MatchedOn, Status: "active"})
	}
	ex := uuid.Nil
	if self != nil {
		ex = *self
	}
	rows, err := q.Query(ctx, `SELECT id, number, name, CASE WHEN $2 <> '' AND phone = $2 THEN 'phone' ELSE 'email' END, status
		FROM crm.sales_leads WHERE property_id = $1 AND status IN ('new', 'contacted', 'qualified') AND id <> $4
		  AND (($2 <> '' AND phone = $2) OR ($3 <> '' AND lower(email) = $3)) ORDER BY created_at LIMIT 5`, property, phone, email, ex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d := LeadDuplicate{Kind: "lead"}
		if err := rows.Scan(&d.ID, &d.Reference, &d.Name, &d.MatchedOn, &d.Status); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// openLeadByContact returns an open lead with the same phone or e-mail
// (automatic captures append to it instead of creating a duplicate).
func openLeadByContact(ctx context.Context, q dbtx.Querier, property uuid.UUID, phone, email string) (*uuid.UUID, error) {
	phone = crm.NormalizePhone(phone)
	email = strings.ToLower(strings.TrimSpace(email))
	if phone == "" && email == "" {
		return nil, nil
	}
	var lid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM crm.sales_leads WHERE property_id = $1 AND status IN ('new', 'contacted', 'qualified')
		AND (($2 <> '' AND phone = $2) OR ($3 <> '' AND lower(email) = $3)) ORDER BY created_at DESC LIMIT 1`, property, phone, email).Scan(&lid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lid, nil
}

// ── create ────────────────────────────────────────────────────────────────

// LeadInput creates a lead (quick manual entry for social media DMs and
// walk-ins: the source is mandatory, FR-LEAD-02).
type LeadInput struct {
	Name                  string      `json:"name"`
	CompanyName           string      `json:"companyName,omitempty"`
	Phone                 string      `json:"phone,omitempty"`
	Email                 string      `json:"email,omitempty"`
	Source                string      `json:"source" enum:"whatsapp,instagram,facebook,tiktok,website_form,walk_in,member_referral,email,phone,event,other"`
	Channel               string      `json:"channel,omitempty"`
	Line                  string      `json:"line,omitempty" enum:"wedding,banquet,mice,event,tournament,stay,golf,package,membership,other"`
	EventType             string      `json:"eventType,omitempty" enum:"wedding,meeting,conference,gathering,birthday,tournament,other"`
	EventDate             *route.Date `json:"eventDate,omitempty"`
	Pax                   *int        `json:"pax,omitempty"`
	Budget                string      `json:"budget,omitempty"`
	Notes                 string      `json:"notes,omitempty"`
	Message               string      `json:"message,omitempty"`
	OwnerUserID           *uuid.UUID  `json:"ownerUserId,omitempty" doc:"Assign manually (otherwise Sales Policies assignment)"`
	CustomerID            *uuid.UUID  `json:"customerId,omitempty" doc:"Existing customer (Customer 360)"`
	ReferrerCustomerID    *uuid.UUID  `json:"referrerCustomerId,omitempty" doc:"Referring member (Member Referral)"`
	MarketingConsent      bool        `json:"marketingConsent,omitempty" doc:"Marketing consent given (UU PDP)"`
	DuplicateAcknowledged bool        `json:"duplicateAcknowledged,omitempty" doc:"Save although an open lead with the same phone / e-mail exists"`
}

// leadSpec is the internal lead creation request of every capture path.
type leadSpec struct {
	LeadInput
	sourceRef, externalRef string
	assignMethod           string     // manual | import (with OwnerUserID)
	createdAt              *time.Time // migrated leads keep their date
	status                 string     // migrated status
	noSLA                  bool
	consentSource          string
	activityType           string // inbound message activity: whatsapp | email | note
	activitySource         string // whatsapp | email | website | manual
	activityRef            string
}

var phoneRe = regexp.MustCompile(`^\+?[0-9 ()-]{6,24}$`)
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (s *leadSpec) validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return handle.Invalid("name", "required", "name is required")
	}
	if !oneOf(Sources, s.Source) {
		return enumErr("source", Sources)
	}
	if s.Line == "" {
		s.Line = "other"
	}
	if !oneOf(Lines, s.Line) {
		return enumErr("line", Lines)
	}
	if s.EventType != "" && !oneOf(EventTypes, s.EventType) {
		return enumErr("eventType", EventTypes)
	}
	s.Phone = strings.TrimSpace(s.Phone)
	s.Email = strings.ToLower(strings.TrimSpace(s.Email))
	if s.Phone == "" && s.Email == "" {
		return handle.Invalid("phone", "required", "phone or e-mail is required")
	}
	if s.Phone != "" {
		if !phoneRe.MatchString(s.Phone) {
			return handle.Invalid("phone", "invalid_phone", "digits, spaces, + ( ) - only")
		}
		s.Phone = crm.NormalizePhone(s.Phone)
	}
	if s.Email != "" && !emailRe.MatchString(s.Email) {
		return handle.Invalid("email", "invalid_email", "invalid e-mail address")
	}
	if s.Pax != nil && *s.Pax <= 0 {
		return handle.Invalid("pax", "invalid", "a positive number of guests")
	}
	if b := strings.TrimSpace(s.Budget); b != "" {
		v, err := handle.Decimal("budget", b, decimal.Zero)
		if err != nil {
			return err
		}
		if v.IsNegative() {
			return handle.Invalid("budget", "invalid", "a positive amount")
		}
	}
	return nil
}

// CreateLead creates a lead entered by staff. An open lead with the same
// phone / e-mail blocks the save until acknowledged; a matching customer is
// linked and reported as a warning (FR-LEAD-03).
func (m *Module) CreateLead(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LeadInput) (LeadDetail, error) {
	spec := leadSpec{LeadInput: in, assignMethod: "manual", activityType: "note", activitySource: "manual"}
	if err := spec.validate(); err != nil {
		return LeadDetail{}, err
	}
	if !in.DuplicateAcknowledged {
		dups, err := findDuplicates(ctx, tx, property, spec.Phone, spec.Email, nil)
		if err != nil {
			return LeadDetail{}, err
		}
		for _, d := range dups {
			if d.Kind == "lead" {
				e := errs.Conflict("duplicate_lead", "an open lead with the same "+d.MatchedOn+" exists: "+d.Name+" ("+d.Reference+")")
				e.Fields = []errs.FieldError{errs.Field(d.MatchedOn, "duplicate", d.ID.String())}
				return LeadDetail{}, e
			}
		}
	}
	if in.OwnerUserID != nil {
		if uid := actor(ctx); (uid == nil || *uid != *in.OwnerUserID) && !can(ctx, "crm.lead.assign", property) {
			return LeadDetail{}, errs.Forbidden("assigning a lead to another sales needs crm.lead.assign")
		}
	}
	l, err := m.createLead(ctx, tx, property, spec)
	if err != nil {
		return LeadDetail{}, err
	}
	return LeadDetailOf(ctx, tx, l.ID)
}

// createLead inserts a lead, links a matching customer, computes the SLA,
// assigns it and publishes crm.lead_created.
func (m *Module) createLead(ctx context.Context, tx pgx.Tx, property uuid.UUID, s leadSpec) (Lead, error) {
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return Lead{}, err
	}
	loc := location(ctx, tx, property)
	now := clock.Now()
	created := now
	if s.createdAt != nil {
		created = *s.createdAt
	}
	customer := s.CustomerID
	if customer != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *customer); err != nil || !ok {
			if err != nil {
				return Lead{}, err
			}
			return Lead{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
	} else {
		custs, err := crm.FindDuplicates(ctx, tx, property, s.Phone, s.Email, "")
		if err != nil {
			return Lead{}, err
		}
		if len(custs) > 0 {
			c := custs[0].ID
			customer = &c
		}
	}
	if s.ReferrerCustomerID != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *s.ReferrerCustomerID); err != nil || !ok {
			if err != nil {
				return Lead{}, err
			}
			return Lead{}, handle.Invalid("referrerCustomerId", "not_found", "referring member not found in this property")
		}
		if s.Source == "" || s.Source == "other" {
			s.Source = "member_referral"
		}
	}
	var eventDate *time.Time
	if s.EventDate != nil {
		if eventDate, err = parseDate("eventDate", string(*s.EventDate)); err != nil {
			return Lead{}, err
		}
	}
	var budget *string
	if b := strings.TrimSpace(s.Budget); b != "" {
		budget = &b
	}
	var due *time.Time
	if !s.noSLA {
		d := slaDue(created, loc, pol, s.Line)
		due = &d
	}
	status := s.status
	if status == "" {
		status = "new"
	}
	var consentAt *time.Time
	consentSource := s.consentSource
	if s.MarketingConsent {
		consentAt = &created
		if consentSource == "" {
			consentSource = s.Source
		}
	}
	number, err := numbering.Next(ctx, tx, property, "LEAD", now.In(loc))
	if err != nil {
		return Lead{}, err
	}
	cur := "IDR"
	_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur)
	lid := id.New()
	var responded *time.Time
	if status != "new" {
		responded = &created
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_leads (id, property_id, number, name, company_name, phone, email, source, channel, line,
		event_type, event_date, pax, budget, currency, notes, message, status, customer_id, referrer_customer_id, marketing_consent, consent_at,
		consent_source, first_response_due_at, first_responded_at, external_ref, source_ref, last_activity_at, created_at, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::numeric,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28,$29,$29)`,
		lid, property, number, s.Name, nullStr(s.CompanyName), nullStr(s.Phone), nullStr(s.Email), s.Source, nullStr(s.Channel), s.Line,
		nullStr(s.EventType), eventDate, s.Pax, budget, cur, nullStr(s.Notes), nullStr(s.Message), status, customer, s.ReferrerCustomerID,
		s.MarketingConsent, consentAt, nullStr(consentSource), due, responded, nullStr(s.externalRef), nullStr(s.sourceRef), created, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Lead{}, errs.Conflict("duplicate_lead", "a lead with the same reference already exists")
		}
		return Lead{}, err
	}
	l, err := GetLead(ctx, tx, lid)
	if err != nil {
		return l, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.lead", EntityID: lid.String(),
		EntityLabel: number + " · " + s.Name, PropertyID: &property, After: l}); err != nil {
		return l, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventLeadCreated, "crm.lead", &lid, &property, map[string]any{"leadId": lid, "number": number,
		"source": s.Source, "line": s.Line, "customerId": customer, "referrerCustomerId": s.ReferrerCustomerID}); err != nil {
		return l, err
	}
	if strings.TrimSpace(s.Message) != "" && s.activityType != "" {
		subject := "Inquiry via " + strings.ReplaceAll(s.Source, "_", " ")
		if _, err := m.addActivity(ctx, tx, property, activityTarget{lead: &lid}, ActivityInput{Type: s.activityType, Direction: "inbound",
			Subject: subject, Notes: s.Message, OccurredAt: &created}, s.activitySource, s.activityRef); err != nil {
			return l, err
		}
	}
	// assignment
	switch {
	case s.OwnerUserID != nil:
		if err := ensureUser(ctx, tx, "ownerUserId", *s.OwnerUserID); err != nil {
			return l, err
		}
		method := s.assignMethod
		if method == "" {
			method = "manual"
		}
		if err := m.setOwner(ctx, tx, property, l, s.OwnerUserID, method, ""); err != nil {
			return l, err
		}
	case status == "new" || status == "contacted" || status == "qualified":
		uid, method, err := m.autoAssign(ctx, tx, property, s.Line, pol)
		if err != nil {
			return l, err
		}
		if uid != nil {
			if err := m.setOwner(ctx, tx, property, l, uid, method, ""); err != nil {
				return l, err
			}
		}
	}
	return GetLead(ctx, tx, lid)
}

// slaDue is the first-response deadline of a lead created at t: within
// business hours t + the line's SLA; before opening and after closing (or
// past the closing) the lead is due at AfterHoursDueTime of the next
// morning (PRD P3 §16 #3: ≤ 1 hour 08.00–20.00, else 09.00 next day).
func slaDue(t time.Time, loc *time.Location, pol SalesPolicy, line string) time.Time {
	minutes := pol.FirstResponseMinutes
	if v, ok := pol.LineResponseMinutes[line]; ok && v > 0 {
		minutes = v
	}
	if minutes <= 0 {
		minutes = 60
	}
	l := t.In(loc)
	at := func(day time.Time, hhmm, def string) time.Time {
		hm, err := time.Parse("15:04", hhmm)
		if err != nil {
			hm, _ = time.Parse("15:04", def)
		}
		return time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
	}
	open := at(l, pol.BusinessHoursStart, "08:00")
	closing := at(l, pol.BusinessHoursEnd, "20:00")
	switch {
	case l.Before(open):
		return at(l, pol.AfterHoursDueTime, "09:00")
	case !l.Before(closing):
		return at(l.AddDate(0, 0, 1), pol.AfterHoursDueTime, "09:00")
	}
	due := l.Add(time.Duration(minutes) * time.Minute)
	if due.After(closing) {
		return at(l.AddDate(0, 0, 1), pol.AfterHoursDueTime, "09:00")
	}
	return due
}

// autoAssign picks the sales for a new lead: the fixed sales of the line
// (fixed mode), else round-robin over the active members of the sales
// teams handling the line (the member assigned longest ago first).
func (m *Module) autoAssign(ctx context.Context, tx pgx.Tx, property uuid.UUID, line string, pol SalesPolicy) (*uuid.UUID, string, error) {
	switch pol.AssignmentMode {
	case "manual":
		return nil, "", nil
	case "fixed":
		if raw := pol.FixedAssignees[line]; raw != "" {
			if uid, err := uuid.Parse(raw); err == nil {
				if ensureUser(ctx, tx, "fixedAssignees", uid) == nil {
					return &uid, "fixed", nil
				}
			}
		}
	}
	var mid, uid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT m.id, m.user_id FROM crm.sales_team_members m JOIN crm.sales_teams t ON t.id = m.team_id
		JOIN platform.users u ON u.id = m.user_id AND u.status = 'active'
		WHERE m.property_id = $1 AND m.status = 'active' AND t.status = 'active' AND t.archived_at IS NULL
		  AND CASE WHEN cardinality(m.lines) > 0 THEN $2 = ANY (m.lines) ELSE cardinality(t.lines) = 0 OR $2 = ANY (t.lines) END
		ORDER BY m.last_assigned_at NULLS FIRST, m.created_at, m.id LIMIT 1 FOR UPDATE OF m SKIP LOCKED`, property, line).Scan(&mid, &uid)
	if dbtx.IsNoRows(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_team_members SET last_assigned_at = clock_timestamp() WHERE id = $1`, mid); err != nil {
		return nil, "", err
	}
	return &uid, "round_robin", nil
}

// setOwner (re)assigns a lead, keeps the history, notifies the sales and
// publishes crm.lead_assigned.
func (m *Module) setOwner(ctx context.Context, tx pgx.Tx, property uuid.UUID, l Lead, to *uuid.UUID, method, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET owner_user_id = $2, assigned_at = now(), assignment_method = $3, updated_by = $4 WHERE id = $1`,
		l.ID, to, method, actor(ctx)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_lead_assignments (id, property_id, lead_id, from_user_id, to_user_id, method, reason, assigned_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id.New(), property, l.ID, l.OwnerUserID, to, method, nullStr(reason), actor(ctx)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "assign", EntityType: "crm.lead", EntityID: l.ID.String(),
		EntityLabel: l.Number + " · " + l.Name, PropertyID: &property, Reason: reason,
		Before: map[string]any{"ownerUserId": l.OwnerUserID}, After: map[string]any{"ownerUserId": to, "method": method}}); err != nil {
		return err
	}
	if _, err := m.Events.Publish(ctx, tx, EventLeadAssigned, "crm.lead", &l.ID, &property, map[string]any{"leadId": l.ID, "number": l.Number,
		"ownerUserId": to, "previousOwnerUserId": l.OwnerUserID, "method": method}); err != nil {
		return err
	}
	if to == nil {
		return nil
	}
	due := ""
	if l.FirstResponseDueAt != nil && l.FirstRespondedAt == nil {
		due = l.FirstResponseDueAt.In(location(ctx, tx, property)).Format("02 Jan 15:04")
	}
	return m.notifyUsers(ctx, tx, property, []uuid.UUID{*to}, "crm.sales_lead_assigned", m.staffLink("/crm/leads/"+l.ID.String()),
		map[string]any{"number": l.Number, "name": l.Name, "line": l.Line, "source": l.Source, "dueAt": due})
}

// ── update & status ───────────────────────────────────────────────────────

// LeadUpdate edits a lead (omitted fields keep their value).
type LeadUpdate struct {
	Name               *string     `json:"name,omitempty"`
	CompanyName        *string     `json:"companyName,omitempty"`
	Phone              *string     `json:"phone,omitempty"`
	Email              *string     `json:"email,omitempty"`
	Channel            *string     `json:"channel,omitempty"`
	Line               *string     `json:"line,omitempty" enum:"wedding,banquet,mice,event,tournament,stay,golf,package,membership,other"`
	EventType          *string     `json:"eventType,omitempty"`
	EventDate          *route.Date `json:"eventDate,omitempty"`
	Pax                *int        `json:"pax,omitempty"`
	Budget             *string     `json:"budget,omitempty"`
	Notes              *string     `json:"notes,omitempty"`
	CustomerID         *uuid.UUID  `json:"customerId,omitempty"`
	ReferrerCustomerID *uuid.UUID  `json:"referrerCustomerId,omitempty"`
	MarketingConsent   *bool       `json:"marketingConsent,omitempty"`
}

// UpdateLead edits an open lead.
func (m *Module) UpdateLead(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID, in LeadUpdate) (LeadDetail, error) {
	before, err := lockLead(ctx, tx, property, lid)
	if err != nil {
		return LeadDetail{}, err
	}
	if before.Status == "converted" && (in.Name != nil || in.Phone != nil || in.Email != nil || in.Line != nil || in.CustomerID != nil) {
		return LeadDetail{}, conflict("lead_converted", "a converted lead keeps its contact; edit the customer instead")
	}
	spec := leadSpec{LeadInput: LeadInput{Name: before.Name, CompanyName: deref(before.CompanyName), Phone: deref(before.Phone),
		Email: deref(before.Email), Source: before.Source, Channel: deref(before.Channel), Line: before.Line, EventType: deref(before.EventType),
		Budget: deref(before.Budget), Notes: deref(before.Notes), Pax: before.Pax}}
	if before.EventDate != nil {
		d := route.Date(*before.EventDate)
		spec.EventDate = &d
	}
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&spec.Name, in.Name)
	set(&spec.CompanyName, in.CompanyName)
	set(&spec.Phone, in.Phone)
	set(&spec.Email, in.Email)
	set(&spec.Channel, in.Channel)
	set(&spec.Line, in.Line)
	set(&spec.EventType, in.EventType)
	set(&spec.Budget, in.Budget)
	set(&spec.Notes, in.Notes)
	if in.EventDate != nil {
		spec.EventDate = in.EventDate
	}
	if in.Pax != nil {
		spec.Pax = in.Pax
	}
	if err := spec.validate(); err != nil {
		return LeadDetail{}, err
	}
	var eventDate *time.Time
	if spec.EventDate != nil {
		if eventDate, err = parseDate("eventDate", string(*spec.EventDate)); err != nil {
			return LeadDetail{}, err
		}
	}
	customer := before.CustomerID
	if in.CustomerID != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *in.CustomerID); err != nil || !ok {
			if err != nil {
				return LeadDetail{}, err
			}
			return LeadDetail{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
		customer = in.CustomerID
	}
	referrer := before.ReferrerCustomerID
	if in.ReferrerCustomerID != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *in.ReferrerCustomerID); err != nil || !ok {
			if err != nil {
				return LeadDetail{}, err
			}
			return LeadDetail{}, handle.Invalid("referrerCustomerId", "not_found", "referring member not found in this property")
		}
		referrer = in.ReferrerCustomerID
	}
	consent := before.MarketingConsent
	if in.MarketingConsent != nil {
		consent = *in.MarketingConsent
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET name = $2, company_name = $3, phone = $4, email = $5, channel = $6, line = $7,
		event_type = $8, event_date = $9, pax = $10, budget = $11::numeric, notes = $12, customer_id = $13, referrer_customer_id = $14,
		marketing_consent = $15, consent_at = CASE WHEN $15 THEN coalesce(consent_at, now()) ELSE NULL END,
		consent_source = CASE WHEN $15 THEN coalesce(consent_source, 'staff') ELSE NULL END, updated_by = $16 WHERE id = $1`,
		lid, spec.Name, nullStr(spec.CompanyName), nullStr(spec.Phone), nullStr(spec.Email), nullStr(spec.Channel), spec.Line, nullStr(spec.EventType),
		eventDate, spec.Pax, nullStr(spec.Budget), nullStr(spec.Notes), customer, referrer, consent, actor(ctx)); err != nil {
		return LeadDetail{}, err
	}
	after, err := GetLead(ctx, tx, lid)
	if err != nil {
		return LeadDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionUpdate, EntityType: "crm.lead", EntityID: lid.String(),
		EntityLabel: after.Number + " · " + after.Name, PropertyID: &property, Before: before, After: after}); err != nil {
		return LeadDetail{}, err
	}
	return LeadDetailOf(ctx, tx, lid)
}

// LeadAssignInput assigns or reassigns a lead.
type LeadAssignInput struct {
	UserID uuid.UUID `json:"userId"`
	Reason string    `json:"reason,omitempty"`
}

// AssignLead assigns a lead manually (Sales Manager reassign).
func (m *Module) AssignLead(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID, in LeadAssignInput) (LeadDetail, error) {
	l, err := lockLead(ctx, tx, property, lid)
	if err != nil {
		return LeadDetail{}, err
	}
	if l.Status == "converted" || l.Status == "unqualified" {
		return LeadDetail{}, conflict("lead_closed", "the lead is "+l.Status)
	}
	if err := ensureUser(ctx, tx, "userId", in.UserID); err != nil {
		return LeadDetail{}, err
	}
	if l.OwnerUserID != nil && *l.OwnerUserID == in.UserID {
		return LeadDetail{}, conflict("already_assigned", "the lead is already assigned to this user")
	}
	if err := m.setOwner(ctx, tx, property, l, &in.UserID, "manual", in.Reason); err != nil {
		return LeadDetail{}, err
	}
	return LeadDetailOf(ctx, tx, lid)
}

// markResponded records the first response (status new → contacted).
func markResponded(ctx context.Context, tx pgx.Tx, lid uuid.UUID, at time.Time, contacted bool) error {
	status := "status"
	if contacted {
		status = "CASE WHEN status = 'new' THEN 'contacted' ELSE status END"
	}
	_, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET first_responded_at = coalesce(first_responded_at, $2), status = `+status+`,
		last_activity_at = greatest(coalesce(last_activity_at, $2), $2) WHERE id = $1`, lid, at)
	return err
}

// QualifyInput qualifies a lead.
type QualifyInput struct {
	Note string `json:"note,omitempty"`
}

// DisqualifyInput disqualifies a lead with a reason.
type DisqualifyInput struct {
	Reason string `json:"reason" enum:"not_interested,budget,date_unavailable,duplicate,spam,no_response,competitor,other"`
	Note   string `json:"note,omitempty"`
}

var disqualifyReasons = []string{"not_interested", "budget", "date_unavailable", "duplicate", "spam", "no_response", "competitor", "other"}

// QualifyLead marks a lead qualified (FR-LEAD-07).
func (m *Module) QualifyLead(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID, in QualifyInput) (LeadDetail, error) {
	l, err := lockLead(ctx, tx, property, lid)
	if err != nil {
		return LeadDetail{}, err
	}
	if l.Status == "converted" || l.Status == "qualified" {
		return LeadDetail{}, conflict("invalid_status", "the lead is already "+l.Status)
	}
	if err := markResponded(ctx, tx, lid, clock.Now(), false); err != nil {
		return LeadDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET status = 'qualified', qualified_at = now(), unqualified_at = NULL, unqualified_reason = NULL,
		unqualified_note = NULL, updated_by = $2 WHERE id = $1`, lid, actor(ctx)); err != nil {
		return LeadDetail{}, err
	}
	if err := m.statusAudit(ctx, tx, property, l, "qualify", in.Note); err != nil {
		return LeadDetail{}, err
	}
	return LeadDetailOf(ctx, tx, lid)
}

// DisqualifyLead marks a lead unqualified with a reason (FR-LEAD-07).
func (m *Module) DisqualifyLead(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID, in DisqualifyInput) (LeadDetail, error) {
	l, err := lockLead(ctx, tx, property, lid)
	if err != nil {
		return LeadDetail{}, err
	}
	if !oneOf(disqualifyReasons, in.Reason) {
		return LeadDetail{}, enumErr("reason", disqualifyReasons)
	}
	if l.Status == "converted" || l.Status == "unqualified" {
		return LeadDetail{}, conflict("invalid_status", "the lead is already "+l.Status)
	}
	if err := markResponded(ctx, tx, lid, clock.Now(), false); err != nil {
		return LeadDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET status = 'unqualified', unqualified_at = now(), unqualified_reason = $2,
		unqualified_note = $3, updated_by = $4 WHERE id = $1`, lid, in.Reason, nullStr(in.Note), actor(ctx)); err != nil {
		return LeadDetail{}, err
	}
	if err := m.statusAudit(ctx, tx, property, l, "disqualify", in.Reason+" "+in.Note); err != nil {
		return LeadDetail{}, err
	}
	return LeadDetailOf(ctx, tx, lid)
}

func (m *Module) statusAudit(ctx context.Context, tx pgx.Tx, property uuid.UUID, before Lead, action, reason string) error {
	after, err := GetLead(ctx, tx, before.ID)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: action, EntityType: "crm.lead", EntityID: before.ID.String(),
		EntityLabel: before.Number + " · " + before.Name, PropertyID: &property, Reason: strings.TrimSpace(reason),
		Before: map[string]any{"status": before.Status}, After: map[string]any{"status": after.Status}})
}

// ── conversion (FR-LEAD-07) ───────────────────────────────────────────────

// ConvertInput converts a lead into a customer (+ corporate account) and an
// opportunity.
type ConvertInput struct {
	CustomerID             *uuid.UUID  `json:"customerId,omitempty" doc:"Use this customer (default: the matched customer, else a new one)"`
	CorporateAccountID     *uuid.UUID  `json:"corporateAccountId,omitempty"`
	CreateCorporateAccount *bool       `json:"createCorporateAccount,omitempty" doc:"Default: true when the lead has a company name"`
	CreateOpportunity      *bool       `json:"createOpportunity,omitempty" doc:"Default true"`
	PipelineID             *uuid.UUID  `json:"pipelineId,omitempty" doc:"Default: the pipeline of the lead's line"`
	Title                  string      `json:"title,omitempty"`
	ExpectedValue          string      `json:"expectedValue,omitempty" doc:"Default: the lead budget"`
	ExpectedCloseDate      *route.Date `json:"expectedCloseDate,omitempty"`
	OwnerUserID            *uuid.UUID  `json:"ownerUserId,omitempty" doc:"Default: the lead owner"`
}

// ConvertResult is the outcome of a conversion.
type ConvertResult struct {
	Lead               Lead         `json:"lead"`
	CustomerID         uuid.UUID    `json:"customerId"`
	CorporateAccountID *uuid.UUID   `json:"corporateAccountId"`
	Opportunity        *Opportunity `json:"opportunity"`
}

// ConvertLead converts a lead and publishes crm.lead_converted.
func (m *Module) ConvertLead(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID, in ConvertInput) (ConvertResult, error) {
	l, err := lockLead(ctx, tx, property, lid)
	if err != nil {
		return ConvertResult{}, err
	}
	if l.Status == "converted" || l.Status == "unqualified" {
		return ConvertResult{}, conflict("invalid_status", "a "+l.Status+" lead cannot be converted")
	}
	now := clock.Now()
	// customer
	var cid uuid.UUID
	switch {
	case in.CustomerID != nil:
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *in.CustomerID); err != nil || !ok {
			if err != nil {
				return ConvertResult{}, err
			}
			return ConvertResult{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
		cid = *in.CustomerID
	case l.CustomerID != nil:
		cid = *l.CustomerID
	default:
		dups, err := crm.FindDuplicates(ctx, tx, property, deref(l.Phone), deref(l.Email), "")
		if err != nil {
			return ConvertResult{}, err
		}
		if len(dups) > 0 {
			cid = dups[0].ID
		} else {
			consent := ""
			if l.MarketingConsent {
				consent = l.Source
			}
			c, err := crm.CreateCustomer(ctx, tx, property, crm.NewCustomer{Name: l.Name, Email: deref(l.Email), Phone: deref(l.Phone), ConsentChannel: consent})
			if err != nil {
				return ConvertResult{}, err
			}
			cid = c.ID
		}
	}
	if l.MarketingConsent {
		if _, err := tx.Exec(ctx, `UPDATE crm.customers SET marketing_opt_in = true, consent_at = coalesce(consent_at, $2),
			consent_channel = coalesce(consent_channel, $3) WHERE id = $1`, cid, deref2(l.ConsentAt, now), l.Source); err != nil {
			return ConvertResult{}, err
		}
	}
	// corporate account
	corp := l.CorporateAccountID
	if in.CorporateAccountID != nil {
		if _, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID); err != nil {
			return ConvertResult{}, err
		}
		corp = in.CorporateAccountID
	}
	createCorp := l.CompanyName != nil && strings.TrimSpace(*l.CompanyName) != ""
	if in.CreateCorporateAccount != nil {
		createCorp = *in.CreateCorporateAccount && createCorp
	}
	if corp == nil && createCorp {
		c, err := ensureCorporateAccount(ctx, tx, property, strings.TrimSpace(*l.CompanyName), l)
		if err != nil {
			return ConvertResult{}, err
		}
		corp = &c
	}
	if corp != nil {
		if err := crm.LinkNominee(ctx, tx, property, *corp, cid, "Contact person"); err != nil {
			return ConvertResult{}, err
		}
	}
	// opportunity
	var opp *Opportunity
	if in.CreateOpportunity == nil || *in.CreateOpportunity {
		owner := l.OwnerUserID
		if in.OwnerUserID != nil {
			owner = in.OwnerUserID
		}
		if owner == nil {
			owner = actor(ctx)
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			who := l.Name
			if l.CompanyName != nil && *l.CompanyName != "" {
				who = *l.CompanyName
			}
			title = lineLabel(l.Line) + " · " + who
		}
		value := in.ExpectedValue
		if value == "" {
			value = deref(l.Budget)
		}
		closeDate := in.ExpectedCloseDate
		if closeDate == nil && l.EventDate != nil {
			d := route.Date(*l.EventDate)
			closeDate = &d
		}
		var evDate *route.Date
		if l.EventDate != nil {
			d := route.Date(*l.EventDate)
			evDate = &d
		}
		lead := l.ID
		o, err := m.createOpportunity(ctx, tx, property, OpportunityInput{Title: title, PipelineID: in.PipelineID, Line: l.Line, LeadID: &lead,
			CustomerID: &cid, CorporateAccountID: corp, OwnerUserID: owner, ExpectedValue: value, ExpectedCloseDate: closeDate,
			EventType: deref(l.EventType), EventDate: evDate, Pax: l.Pax, Notes: deref(l.Notes)})
		if err != nil {
			return ConvertResult{}, err
		}
		opp = &o
	}
	var oppID *uuid.UUID
	if opp != nil {
		oppID = &opp.ID
	}
	if err := markResponded(ctx, tx, lid, now, false); err != nil {
		return ConvertResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET status = 'converted', converted_at = $2, customer_id = $3, corporate_account_id = $4,
		opportunity_id = $5, qualified_at = coalesce(qualified_at, $2), updated_by = $6 WHERE id = $1`, lid, now, cid, corp, oppID, actor(ctx)); err != nil {
		return ConvertResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_activities SET customer_id = coalesce(customer_id, $2), opportunity_id = coalesce(opportunity_id, $3)
		WHERE lead_id = $1`, lid, cid, oppID); err != nil {
		return ConvertResult{}, err
	}
	after, err := GetLead(ctx, tx, lid)
	if err != nil {
		return ConvertResult{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "convert", EntityType: "crm.lead", EntityID: lid.String(),
		EntityLabel: l.Number + " · " + l.Name, PropertyID: &property, Before: map[string]any{"status": l.Status},
		After: map[string]any{"status": "converted", "customerId": cid, "corporateAccountId": corp, "opportunityId": oppID}}); err != nil {
		return ConvertResult{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventLeadConverted, "crm.lead", &lid, &property, map[string]any{"leadId": lid, "number": l.Number,
		"customerId": cid, "corporateAccountId": corp, "opportunityId": oppID, "convertedAt": now.UTC().Format(time.RFC3339),
		"line": l.Line, "source": l.Source, "referrerCustomerId": l.ReferrerCustomerID}); err != nil {
		return ConvertResult{}, err
	}
	return ConvertResult{Lead: after, CustomerID: cid, CorporateAccountID: corp, Opportunity: opp}, nil
}

func deref2(t *time.Time, def time.Time) time.Time {
	if t == nil {
		return def
	}
	return *t
}

// ensureCorporateAccount returns the active corporate account with the
// company name, creating it (code CA-…) when missing.
func ensureCorporateAccount(ctx context.Context, tx pgx.Tx, property uuid.UUID, company string, l Lead) (uuid.UUID, error) {
	var cid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.corporate_accounts WHERE property_id = $1 AND lower(name) = lower($2) AND status = 'active'
		AND archived_at IS NULL ORDER BY created_at LIMIT 1`, property, company).Scan(&cid)
	if err == nil {
		return cid, nil
	}
	if !dbtx.IsNoRows(err) {
		return cid, err
	}
	cid = id.New()
	code := "CA-" + strings.ToUpper(cid.String()[24:])
	if _, err := tx.Exec(ctx, `INSERT INTO crm.corporate_accounts (id, property_id, code, name, contact_name, phone, email, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, cid, property, code, company, l.Name, l.Phone, l.Email, actor(ctx)); err != nil {
		return cid, err
	}
	return cid, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.corporate_account", EntityID: cid.String(),
		EntityLabel: code + " · " + company, PropertyID: &property, After: map[string]any{"code": code, "name": company, "fromLead": l.Number}})
}

// lineLabel is the UI label of a business line.
func lineLabel(line string) string {
	switch line {
	case "mice":
		return "MICE / Meeting"
	case "stay":
		return "Stay"
	}
	if line == "" {
		return "Other"
	}
	return strings.ToUpper(line[:1]) + line[1:]
}

// ── transfer (FR-LEAD-10) ─────────────────────────────────────────────────

// TransferInput moves the open leads, opportunities and follow-ups of a
// sales to another (leave, resignation).
type TransferInput struct {
	FromUserID    uuid.UUID `json:"fromUserId"`
	ToUserID      uuid.UUID `json:"toUserId"`
	Reason        string    `json:"reason"`
	Opportunities *bool     `json:"opportunities,omitempty" doc:"Also move open opportunities and follow-ups (default true)"`
}

// TransferResult counts what moved.
type TransferResult struct {
	Leads         int `json:"leads"`
	Opportunities int `json:"opportunities"`
	FollowUps     int `json:"followUps"`
}

// TransferLeads reassigns every open lead of a sales with history.
func (m *Module) TransferLeads(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TransferInput) (TransferResult, error) {
	var res TransferResult
	if err := handle.Required("reason", in.Reason); err != nil {
		return res, err
	}
	if in.FromUserID == in.ToUserID {
		return res, handle.Invalid("toUserId", "same_user", "choose another sales")
	}
	if err := ensureUser(ctx, tx, "toUserId", in.ToUserID); err != nil {
		return res, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM crm.sales_leads WHERE property_id = $1 AND owner_user_id = $2 AND status IN ('new', 'contacted', 'qualified')
		ORDER BY created_at FOR UPDATE`, property, in.FromUserID)
	if err != nil {
		return res, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return res, err
	}
	to := in.ToUserID
	for _, lid := range ids {
		l, err := GetLead(ctx, tx, lid)
		if err != nil {
			return res, err
		}
		if err := m.setOwner(ctx, tx, property, l, &to, "transfer", in.Reason); err != nil {
			return res, err
		}
		res.Leads++
	}
	if in.Opportunities == nil || *in.Opportunities {
		tag, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET owner_user_id = $3, updated_by = $4 WHERE property_id = $1 AND owner_user_id = $2
			AND status = 'open'`, property, in.FromUserID, in.ToUserID, actor(ctx))
		if err != nil {
			return res, err
		}
		res.Opportunities = int(tag.RowsAffected())
		tag, err = tx.Exec(ctx, `UPDATE crm.sales_activities SET assigned_to = $3, updated_by = $4 WHERE property_id = $1 AND assigned_to = $2
			AND status = 'open'`, property, in.FromUserID, in.ToUserID, actor(ctx))
		if err != nil {
			return res, err
		}
		res.FollowUps = int(tag.RowsAffected())
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "transfer", EntityType: "crm.lead", EntityID: in.FromUserID.String(),
		EntityLabel: "Lead transfer", PropertyID: &property, Reason: in.Reason,
		After: map[string]any{"fromUserId": in.FromUserID, "toUserId": in.ToUserID, "leads": res.Leads, "opportunities": res.Opportunities,
			"followUps": res.FollowUps}})
}

// ── list ──────────────────────────────────────────────────────────────────
