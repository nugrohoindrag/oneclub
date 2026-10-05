package engagement

// Member App Support (FR-APP-P3-06): the customer's replies on a complaint
// and general feedback without a survey link; Customer 360 Lead &
// Opportunity and Quotation section (FR-C360-01) from CRM Sales.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
)

func conflictSent(c CampaignDetail) error {
	return errs.Conflict("already_sent", "campaign "+c.Code+" is "+c.Status)
}

// CustomerReply records a reply of the customer on a ticket: the assignee
// (or the ticket holders) is told, a resolved ticket goes back to work.
func (s *Service) CustomerReply(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, body string) (TicketDetail, error) {
	body = strings.TrimSpace(body)
	if err := handle.Required("body", body); err != nil {
		return TicketDetail{}, err
	}
	t, err := lockTicket(ctx, tx, property, tid)
	if err != nil {
		return TicketDetail{}, err
	}
	if t.Status == "closed" {
		return TicketDetail{}, errs.Conflict("ticket_closed", "complaint "+t.Number+" is closed; reopen it or send a new complaint")
	}
	if err := addEvent(ctx, tx, t, "customer_reply", body, false, t.Status, t.Status, nil, "customer"); err != nil {
		return TicketDetail{}, err
	}
	if t.CustomerID != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, body, source, ref_type, ref_id)
			VALUES ($1,$2,$3,'in_app','inbound',$4,$5,'ticket','crm.ticket',$6)`, id.New(), property, *t.CustomerID, "Complaint "+t.Number+" · customer reply",
			body, tid); err != nil {
			return TicketDetail{}, err
		}
	}
	users := []uuid.UUID{}
	if t.AssignedTo != nil {
		users = append(users, *t.AssignedTo)
	} else if users, err = notify.Holders(ctx, tx, property, "crm.ticket.manage"); err != nil {
		return TicketDetail{}, err
	}
	if err := s.notifyStaff(ctx, tx, t, "crm.ticket_customer_reply", users, body); err != nil {
		return TicketDetail{}, err
	}
	d, err := ticketDetail(ctx, tx, tid, true)
	if err != nil {
		return d, err
	}
	return d, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "customer_reply", EntityType: "crm.ticket", EntityID: tid.String(),
		EntityLabel: t.Number, PropertyID: &property, After: map[string]any{"status": d.Status}})
}

var contextTypes = []string{"round", "stay", "class", "fnb", "sport", "meeting", "other"}

// MemberFeedback stores feedback the member gives from Support (no survey
// link). A low score follows P2's rule (rating ≤ 2 or NPS ≤ 6): the
// follow-up opens and the Complaint Policies may turn it into a ticket.
func (s *Service) MemberFeedback(ctx context.Context, tx pgx.Tx, c crm.Customer, in MemberFeedbackInput) (crm.Feedback, error) {
	if in.Rating < 1 || in.Rating > 5 {
		return crm.Feedback{}, handle.Invalid("rating", "invalid_rating", "rating must be between 1 and 5")
	}
	if in.NPS != nil && (*in.NPS < 0 || *in.NPS > 10) {
		return crm.Feedback{}, handle.Invalid("nps", "invalid_nps", "NPS must be between 0 and 10")
	}
	if in.ContextType == "" {
		in.ContextType = "other"
	}
	if !slices.Contains(contextTypes, in.ContextType) {
		return crm.Feedback{}, handle.Invalid("contextType", "invalid", "one of: "+strings.Join(contextTypes, ", "))
	}
	low := in.Rating <= 2 || (in.NPS != nil && *in.NPS <= 6)
	follow := "none"
	if low {
		follow = "open"
	}
	fid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.feedback (id, property_id, customer_id, context_type, context_label, rating, nps, comment, channel,
		low_score, follow_up) VALUES ($1,$2,$3,$4,'Member App',$5,$6,$7,'member_app',$8,$9)`, fid, c.PropertyID, c.ID, in.ContextType, in.Rating, in.NPS,
		nullStr(in.Comment), low, follow); err != nil {
		return crm.Feedback{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, body, source, ref_type, ref_id)
		VALUES ($1,$2,$3,'in_app','inbound',$4,$5,'feedback','crm.feedback',$6)`, id.New(), c.PropertyID, c.ID,
		fmt.Sprintf("Feedback %s · %s", in.ContextType, strings.Repeat("★", in.Rating)), nullStr(in.Comment), fid); err != nil {
		return crm.Feedback{}, err
	}
	if low && s.Notify != nil {
		users, err := notify.Holders(ctx, tx, c.PropertyID, "crm.feedback.follow_up")
		if err != nil {
			return crm.Feedback{}, err
		}
		if len(users) > 0 {
			pid := c.PropertyID
			if err := s.Notify.Send(ctx, tx, notify.Message{Event: "crm.feedback_low_score", Category: "feedback", UserIDs: users, PropertyID: &pid,
				Data: map[string]any{"context": "Member App", "rating": in.Rating, "comment": in.Comment}, Link: "/crm/feedback"}); err != nil {
				return crm.Feedback{}, err
			}
		}
	}
	var f crm.Feedback
	err := tx.QueryRow(ctx, `SELECT f.id, f.customer_id, c.name, f.context_type, f.context_id, f.context_label, f.rating, f.nps, f.comment, f.channel,
		f.low_score, f.follow_up, f.follow_up_note, f.created_at FROM crm.feedback f LEFT JOIN crm.customers c ON c.id = f.customer_id WHERE f.id = $1`, fid).
		Scan(&f.ID, &f.CustomerID, &f.CustomerName, &f.ContextType, &f.ContextID, &f.ContextLabel, &f.Rating, &f.NPS, &f.Comment, &f.Channel, &f.LowScore,
			&f.FollowUp, &f.FollowUpNote, &f.CreatedAt)
	if err != nil {
		return f, err
	}
	pid := c.PropertyID
	return f, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.feedback", EntityID: fid.String(),
		EntityLabel: in.ContextType, PropertyID: &pid, After: f})
}

// ── Customer 360: Lead & Opportunity, Quotation (FR-C360-01) ─────────────

// SalesSection is the Lead & Opportunity and Quotation section.
type SalesSection struct {
	Leads         []SalesLeadRef        `json:"leads"`
	Opportunities []SalesOpportunityRef `json:"opportunities"`
	Quotations    []SalesQuotationRef   `json:"quotations"`
	Referrals     int                   `json:"referrals" doc:"Leads the customer referred (member referral)"`
}

// SalesLeadRef is a lead of the customer.
type SalesLeadRef struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Number    string    `json:"number" db:"number"`
	Line      string    `json:"line" db:"line"`
	Source    string    `json:"source" db:"source"`
	Status    string    `json:"status" db:"status"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// SalesOpportunityRef is an opportunity of the customer.
type SalesOpportunityRef struct {
	ID            uuid.UUID `json:"id" db:"id"`
	Number        string    `json:"number" db:"number"`
	Title         string    `json:"title" db:"title"`
	Line          string    `json:"line" db:"line"`
	Status        string    `json:"status" db:"status"`
	ExpectedValue string    `json:"expectedValue" db:"expected_value"`
}

// SalesQuotationRef is a quotation of the customer.
type SalesQuotationRef struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Number     string     `json:"number" db:"number"`
	Version    int        `json:"version" db:"version"`
	Title      string     `json:"title" db:"title"`
	Status     string     `json:"status" db:"status"`
	Total      string     `json:"total" db:"total"`
	ValidUntil *time.Time `json:"validUntil" db:"valid_until"`
}

// SalesSectionOf loads the CRM Sales history of a customer.
func SalesSectionOf(ctx context.Context, q dbtx.Querier, _, customer uuid.UUID) (any, error) {
	out := SalesSection{}
	var err error
	if out.Leads, err = handle.List[SalesLeadRef](q.Query(ctx, `SELECT id, number, line, source, status, created_at FROM crm.sales_leads
		WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 10`, customer)); err != nil {
		return out, err
	}
	if out.Opportunities, err = handle.List[SalesOpportunityRef](q.Query(ctx, `SELECT id, number, title, line, status,
		trim_scale(expected_value)::text AS expected_value FROM crm.sales_opportunities WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 10`, customer)); err != nil {
		return out, err
	}
	if out.Quotations, err = handle.List[SalesQuotationRef](q.Query(ctx, `SELECT id, number, version, title, status, trim_scale(total)::text AS total,
		valid_until FROM crm.sales_quotations WHERE customer_id = $1 AND status <> 'revised' ORDER BY created_at DESC LIMIT 10`, customer)); err != nil {
		return out, err
	}
	err = q.QueryRow(ctx, `SELECT count(*)::int FROM crm.sales_leads WHERE referrer_customer_id = $1`, customer).Scan(&out.Referrals)
	return out, err
}

// SeedDefaults creates the demo complaint categories and reminder rules of a
// property (idempotent by code; reminder rules start inactive).
func SeedDefaults(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	for _, c := range []struct{ code, name, line, prio string }{
		{"SERVICE", "Staff service", "other", "medium"}, {"FACILITY", "Facility & cleanliness", "other", "medium"},
		{"FNB", "Food & beverage quality", "pos", "medium"}, {"COURSE", "Course condition", "golf", "medium"},
		{"BILLING", "Billing & payment", "other", "high"}, {"SAFETY", "Safety incident", "other", "urgent"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.ticket_categories (id, property_id, code, name, business_line, default_priority)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, c.code, c.name, c.line, c.prio); err != nil {
			return err
		}
	}
	for _, r := range []struct {
		code, name, kind string
		days             int
	}{{"BIRTHDAY", "Birthday greeting", "birthday", 0}, {"RENEWAL-14", "Membership renewal offer (H-14)", "renewal", 14},
		{"WINBACK-60", "We miss you (60 days)", "follow_up", 60}} {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.reminder_rules (id, property_id, code, name, kind, days_offset, channel, template_event, status)
			VALUES ($1,$2,$3,$4,$5,$6,'email',$7,'inactive') ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, r.code, r.name, r.kind,
			r.days, defaultTemplates[r.kind]); err != nil {
			return err
		}
	}
	return nil
}
