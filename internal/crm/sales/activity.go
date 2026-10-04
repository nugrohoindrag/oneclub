package sales

// Activities & follow-ups (FR-LEAD-08, FR-PIPE-05): calls, WhatsApp,
// e-mail, meetings, site visits, food tastings, notes and tasks on a lead,
// an opportunity or a customer. An activity with a due date is a follow-up
// (open until completed, "Follow-ups" menu) and is reminded to its
// assignee. A completed outbound contact is the lead's first response
// (FR-LEAD-06); communication is mirrored into the customer's Interaction
// History (P2 FR-CRM-02).

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// ActivityTypes of a sales activity.
var ActivityTypes = []string{"call", "whatsapp", "email", "meeting", "site_visit", "food_tasting", "note", "task"}

// contactTypes count as a response to the customer.
var contactTypes = []string{"call", "whatsapp", "email", "meeting", "site_visit", "food_tasting"}

// Activity is one sales activity or follow-up.
type Activity struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	LeadID         *uuid.UUID `json:"leadId" db:"lead_id"`
	LeadNumber     *string    `json:"leadNumber" db:"lead_number"`
	OpportunityID  *uuid.UUID `json:"opportunityId" db:"opportunity_id"`
	OpportunityNo  *string    `json:"opportunityNumber" db:"opportunity_number"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	ContactName    *string    `json:"contactName" db:"contact_name"`
	Type           string     `json:"type" db:"activity_type" enum:"call,whatsapp,email,meeting,site_visit,food_tasting,note,task"`
	Direction      string     `json:"direction" db:"direction" enum:"inbound,outbound,internal"`
	Subject        string     `json:"subject" db:"subject"`
	Notes          *string    `json:"notes" db:"notes"`
	DueAt          *time.Time `json:"dueAt" db:"due_at"`
	AssignedTo     *uuid.UUID `json:"assignedTo" db:"assigned_to"`
	AssignedToName *string    `json:"assignedToName" db:"assigned_to_name"`
	Status         string     `json:"status" db:"status" enum:"open,completed,cancelled"`
	Overdue        bool       `json:"overdue" db:"overdue"`
	OccurredAt     *time.Time `json:"occurredAt" db:"occurred_at"`
	CompletedAt    *time.Time `json:"completedAt" db:"completed_at"`
	Outcome        *string    `json:"outcome" db:"outcome"`
	Source         string     `json:"source" db:"source" enum:"manual,whatsapp,email,website,system"`
	CreatedBy      *uuid.UUID `json:"createdBy" db:"created_by"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
}

const activitySelect = `SELECT a.id, a.lead_id, l.number AS lead_number, a.opportunity_id, o.number AS opportunity_number, a.customer_id,
	coalesce(l.name, c.name, o.title) AS contact_name, a.activity_type, a.direction, a.subject, a.notes, a.due_at, a.assigned_to,
	u.full_name AS assigned_to_name, a.status, (a.status = 'open' AND a.due_at < now()) AS overdue, a.occurred_at, a.completed_at, a.outcome,
	a.source, a.created_by, a.created_at
	FROM crm.sales_activities a LEFT JOIN crm.sales_leads l ON l.id = a.lead_id LEFT JOIN crm.sales_opportunities o ON o.id = a.opportunity_id
	LEFT JOIN crm.customers c ON c.id = a.customer_id LEFT JOIN platform.users u ON u.id = a.assigned_to`

func listActivities(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]Activity, error) {
	return handle.List[Activity](q.Query(ctx, activitySelect+` WHERE `+where+` ORDER BY coalesce(a.occurred_at, a.due_at, a.created_at) DESC, a.id`, args...))
}

func getActivity(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (Activity, error) {
	return getOne[Activity]("activity")(q.Query(ctx, activitySelect+` WHERE a.id = $1`, aid))
}

// ActivityInput logs an activity or schedules a follow-up.
type ActivityInput struct {
	Type       string     `json:"type" enum:"call,whatsapp,email,meeting,site_visit,food_tasting,note,task"`
	Direction  string     `json:"direction,omitempty" enum:"inbound,outbound,internal"`
	Subject    string     `json:"subject"`
	Notes      string     `json:"notes,omitempty"`
	DueAt      *time.Time `json:"dueAt,omitempty" doc:"Schedules a follow-up (open until completed)"`
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
	AssignedTo *uuid.UUID `json:"assignedTo,omitempty" doc:"Default: the lead / opportunity owner, else me"`
}

// FollowUpInput schedules a follow-up from the Follow-ups list.
type FollowUpInput struct {
	ActivityInput
	LeadID        *uuid.UUID `json:"leadId,omitempty"`
	OpportunityID *uuid.UUID `json:"opportunityId,omitempty"`
	CustomerID    *uuid.UUID `json:"customerId,omitempty"`
}

// CompleteInput completes a follow-up.
type CompleteInput struct {
	Outcome string `json:"outcome,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

// FollowUpCancelInput cancels a follow-up.
type FollowUpCancelInput struct {
	Reason string `json:"reason"`
}

type activityTarget struct {
	lead, opportunity, customer *uuid.UUID
}

// addActivity records an activity on a lead / opportunity / customer.
func (m *Module) addActivity(ctx context.Context, tx pgx.Tx, property uuid.UUID, t activityTarget, in ActivityInput, source, externalRef string) (Activity, error) {
	if !oneOf(ActivityTypes, in.Type) {
		return Activity{}, enumErr("type", ActivityTypes)
	}
	if err := handle.Required("subject", in.Subject); err != nil {
		return Activity{}, err
	}
	if in.Direction == "" {
		in.Direction = "outbound"
		if in.Type == "note" || in.Type == "task" {
			in.Direction = "internal"
		}
	}
	if !oneOf([]string{"inbound", "outbound", "internal"}, in.Direction) {
		return Activity{}, enumErr("direction", []string{"inbound", "outbound", "internal"})
	}
	var owner, customer *uuid.UUID
	var leadNumber string
	customer = t.customer
	switch {
	case t.lead != nil:
		var cust *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT owner_user_id, customer_id, number FROM crm.sales_leads WHERE id = $1 AND property_id = $2`,
			*t.lead, property).Scan(&owner, &cust, &leadNumber); err != nil {
			if dbtx.IsNoRows(err) {
				return Activity{}, errs.NotFound("lead")
			}
			return Activity{}, err
		}
		if customer == nil {
			customer = cust
		}
	case t.opportunity != nil:
		var cust *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT owner_user_id, customer_id FROM crm.sales_opportunities WHERE id = $1 AND property_id = $2`,
			*t.opportunity, property).Scan(&owner, &cust); err != nil {
			if dbtx.IsNoRows(err) {
				return Activity{}, errs.NotFound("opportunity")
			}
			return Activity{}, err
		}
		if customer == nil {
			customer = cust
		}
	case t.customer != nil:
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *t.customer); err != nil || !ok {
			if err != nil {
				return Activity{}, err
			}
			return Activity{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
	default:
		return Activity{}, handle.Invalid("leadId", "required", "a lead, opportunity or customer is required")
	}
	assignee := in.AssignedTo
	if assignee != nil {
		if err := ensureUser(ctx, tx, "assignedTo", *assignee); err != nil {
			return Activity{}, err
		}
	} else if owner != nil {
		assignee = owner
	} else {
		assignee = actor(ctx)
	}
	status := "completed"
	var occurred, completed *time.Time
	now := clock.Now()
	if in.DueAt != nil {
		status = "open"
	} else {
		at := now
		if in.OccurredAt != nil {
			at = *in.OccurredAt
		}
		occurred, completed = &at, &at
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_activities (id, property_id, lead_id, opportunity_id, customer_id, activity_type, direction,
		subject, notes, due_at, assigned_to, status, occurred_at, completed_at, source, external_ref, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)`,
		aid, property, t.lead, t.opportunity, customer, in.Type, in.Direction, strings.TrimSpace(in.Subject), nullStr(in.Notes), in.DueAt, assignee,
		status, occurred, completed, source, nullStr(externalRef), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Activity{}, errs.Conflict("duplicate_activity", "this message is already recorded")
		}
		return Activity{}, err
	}
	if status == "completed" {
		if err := m.afterContact(ctx, tx, property, t.lead, customer, aid, in, *occurred); err != nil {
			return Activity{}, err
		}
	} else if t.lead != nil {
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET last_activity_at = greatest(coalesce(last_activity_at, now()), now()) WHERE id = $1`,
			*t.lead); err != nil {
			return Activity{}, err
		}
	}
	a, err := getActivity(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	label := in.Subject
	if leadNumber != "" {
		label = leadNumber + " · " + in.Subject
	}
	return a, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.sales_activity", EntityID: aid.String(),
		EntityLabel: label, PropertyID: &property, After: a})
}

// afterContact records the first response of a lead and mirrors a
// communication into the customer's Interaction History.
func (m *Module) afterContact(ctx context.Context, tx pgx.Tx, property uuid.UUID, lead, customer *uuid.UUID, aid uuid.UUID, in ActivityInput, at time.Time) error {
	contact := oneOf(contactTypes, in.Type)
	if lead != nil {
		if contact && in.Direction == "outbound" {
			if err := markResponded(ctx, tx, *lead, at, true); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET last_activity_at = greatest(coalesce(last_activity_at, $2), $2) WHERE id = $1`,
			*lead, at); err != nil {
			return err
		}
	}
	if customer == nil || !contact {
		return nil
	}
	channel := map[string]string{"call": "phone", "whatsapp": "whatsapp", "email": "email", "meeting": "visit", "site_visit": "visit",
		"food_tasting": "visit"}[in.Type]
	dir := in.Direction
	if dir != "inbound" {
		dir = "outbound"
	}
	_, err := crm.LogInteraction(ctx, tx, property, *customer, crm.InteractionInput{Channel: channel, Direction: dir, Subject: in.Subject,
		Body: in.Notes, OccurredAt: &at}, "manual", "crm.sales_activity", &aid)
	return err
}

// CompleteFollowUp completes an open follow-up.
func (m *Module) CompleteFollowUp(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in CompleteInput) (Activity, error) {
	before, err := lockActivity(ctx, tx, property, aid)
	if err != nil {
		return before, err
	}
	if before.Status != "open" {
		return before, conflict("follow_up_closed", "the follow-up is "+before.Status)
	}
	now := clock.Now()
	notes := deref(before.Notes)
	if strings.TrimSpace(in.Notes) != "" {
		notes = strings.TrimSpace(notes + "\n" + in.Notes)
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_activities SET status = 'completed', completed_at = $2, occurred_at = $2, outcome = $3, notes = $4,
		updated_by = $5 WHERE id = $1`, aid, now, nullStr(in.Outcome), nullStr(notes), actor(ctx)); err != nil {
		return before, err
	}
	if err := m.afterContact(ctx, tx, property, before.LeadID, before.CustomerID, aid, ActivityInput{Type: before.Type, Direction: before.Direction,
		Subject: before.Subject, Notes: in.Outcome}, now); err != nil {
		return before, err
	}
	after, err := getActivity(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "complete", EntityType: "crm.sales_activity", EntityID: aid.String(),
		EntityLabel: before.Subject, PropertyID: &property, Before: map[string]any{"status": before.Status}, After: after})
}

// CancelFollowUp cancels an open follow-up.
func (m *Module) CancelFollowUp(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in FollowUpCancelInput) (Activity, error) {
	before, err := lockActivity(ctx, tx, property, aid)
	if err != nil {
		return before, err
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return before, err
	}
	if before.Status != "open" {
		return before, conflict("follow_up_closed", "the follow-up is "+before.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_activities SET status = 'cancelled', cancel_reason = $2, updated_by = $3 WHERE id = $1`,
		aid, in.Reason, actor(ctx)); err != nil {
		return before, err
	}
	after, err := getActivity(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "cancel", EntityType: "crm.sales_activity", EntityID: aid.String(),
		EntityLabel: before.Subject, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": before.Status},
		After: map[string]any{"status": after.Status}})
}

func lockActivity(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID) (Activity, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_activities WHERE id = $1 AND property_id = $2 FOR UPDATE`, aid, property).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Activity{}, errs.NotFound("follow-up")
		}
		return Activity{}, err
	}
	return getActivity(ctx, tx, aid)
}
