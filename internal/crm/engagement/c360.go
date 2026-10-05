package engagement

// Advanced Customer 360 (FR-C360-01, FR-C360-04): the Campaign, Complaint
// and engagement sections of the Customer 360 (Loyalty comes from
// crm/loyalty; Banquet / Event, Lead & Opportunity, Quotation and Tournament
// sections are contributed by their modules through internal/app) and the
// Corporate 360 of a company.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// CampaignSection is the Campaign section of the Customer 360.
type CampaignSection struct {
	Preferences CommunicationPreferences `json:"preferences"`
	Recent      []CampaignTouch          `json:"recent"`
	Facts       RFMFacts                 `json:"facts" doc:"Recency, frequency, monetary (90 days) and RFM segment"`
	Segments    []SegmentRef             `json:"segments"`
	Tags        []string                 `json:"tags" doc:"Tags from other modules (tournament_participant, wedding …)"`
}

// CampaignTouch is one campaign message to the customer.
type CampaignTouch struct {
	CampaignID  uuid.UUID  `json:"campaignId" db:"campaign_id"`
	Code        string     `json:"code" db:"code"`
	Name        string     `json:"name" db:"name"`
	Channel     string     `json:"channel" db:"channel"`
	Status      string     `json:"status" db:"status"`
	SentAt      *time.Time `json:"sentAt" db:"sent_at"`
	ClickedAt   *time.Time `json:"clickedAt" db:"clicked_at"`
	ConvertedAt *time.Time `json:"convertedAt" db:"converted_at"`
}

// SegmentRef is a segment the customer belongs to.
type SegmentRef struct {
	ID   uuid.UUID `json:"id" db:"id"`
	Code string    `json:"code" db:"code"`
	Name string    `json:"name" db:"name"`
}

// CampaignSectionOf loads the Campaign section of a customer.
func (s *Service) CampaignSectionOf(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	var out CampaignSection
	var err error
	if out.Preferences, err = Preferences(ctx, q, customer); err != nil {
		return out, err
	}
	if out.Recent, err = handle.List[CampaignTouch](q.Query(ctx, `SELECT r.campaign_id, c.code, c.name, r.channel, r.status, r.sent_at, r.clicked_at,
		r.converted_at FROM crm.campaign_recipients r JOIN crm.campaigns c ON c.id = r.campaign_id WHERE r.customer_id = $1
		ORDER BY r.created_at DESC LIMIT 10`, customer)); err != nil {
		return out, err
	}
	if out.Facts, err = CustomerFacts(ctx, q, property, customer); err != nil {
		return out, err
	}
	if out.Segments, err = handle.List[SegmentRef](q.Query(ctx, `SELECT s.id, s.code, s.name FROM crm.segment_members m JOIN crm.segments s ON s.id = m.segment_id
		WHERE m.customer_id = $1 AND s.archived_at IS NULL ORDER BY s.name`, customer)); err != nil {
		return out, err
	}
	out.Tags, err = CustomerTags(ctx, q, customer)
	return out, err
}

// ComplaintSection is the Complaint section of the Customer 360.
type ComplaintSection struct {
	Open    int               `json:"open"`
	Total   int               `json:"total"`
	Recent  []ComplaintTicket `json:"recent"`
	LastNPS *int              `json:"lastNps"`
}

// ComplaintSectionOf loads the Complaint section of a customer.
func (s *Service) ComplaintSectionOf(ctx context.Context, q dbtx.Querier, _, customer uuid.UUID) (any, error) {
	out := ComplaintSection{}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('open', 'in_progress', 'escalated'))::int, count(*)::int FROM crm.tickets
		WHERE customer_id = $1`, customer).Scan(&out.Open, &out.Total); err != nil {
		return out, err
	}
	var err error
	if out.Recent, err = handle.List[ComplaintTicket](q.Query(ctx, ticketSelect+` WHERE t.customer_id = $1 ORDER BY t.created_at DESC LIMIT 10`, customer)); err != nil {
		return out, err
	}
	var nps *int
	err = q.QueryRow(ctx, `SELECT score FROM reporting.eng_nps WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 1`, customer).Scan(&nps)
	if err != nil && !dbtx.IsNoRows(err) {
		return out, err
	}
	out.LastNPS = nps
	return out, nil
}

// ── Corporate 360 (FR-C360-04) ────────────────────────────────────────────

// Corporate360 summarises a company: nominees, billing (invoices and
// receivables), golf activity of the nominees, spend and the sections of
// other modules (events …).
type Corporate360 struct {
	Account      Corporate360Account   `json:"account"`
	Nominees     []Corporate360Nominee `json:"nominees"`
	Billing      CorporateBilling      `json:"billing"`
	GolfActivity CorporateGolf         `json:"golfActivity"`
	Spend        CorporateSpend        `json:"spend"`
	Tickets      []ComplaintTicket     `json:"tickets" doc:"Complaints of the nominees"`
	Sections     map[string]any        `json:"sections" doc:"Contributed by other modules (banquet events, quotations …)"`
	Generated    time.Time             `json:"generatedAt"`
	Invoices     []CorporateInvoice    `json:"invoices" doc:"Open invoices"`
}

// Corporate360Account is the company.
type Corporate360Account struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Code        string    `json:"code" db:"code"`
	Name        string    `json:"name" db:"name"`
	NPWP        *string   `json:"npwp" db:"npwp"`
	ContactName *string   `json:"contactName" db:"contact_name"`
	Email       *string   `json:"email" db:"email"`
	Phone       *string   `json:"phone" db:"phone"`
	Status      string    `json:"status" db:"status"`
}

// Corporate360Nominee is a nominee of the company.
type Corporate360Nominee struct {
	CustomerID uuid.UUID `json:"customerId" db:"customer_id"`
	Code       string    `json:"code" db:"code"`
	Name       string    `json:"name" db:"name"`
	Title      *string   `json:"title" db:"title"`
	Status     string    `json:"status" db:"status"`
	Rounds90   int       `json:"rounds90" db:"rounds90" doc:"Golf rounds in the last 90 days"`
}

// CorporateBilling is the receivable of the company.
type CorporateBilling struct {
	OpenInvoices int    `json:"openInvoices" db:"open_invoices"`
	Outstanding  string `json:"outstanding" db:"outstanding"`
	Overdue      string `json:"overdue" db:"overdue"`
	Invoiced12m  string `json:"invoiced12m" db:"invoiced12m" doc:"Invoiced in the last 12 months"`
}

// CorporateInvoice is an open invoice.
type CorporateInvoice struct {
	ID          uuid.UUID `json:"id" db:"invoice_id"`
	Number      *string   `json:"number" db:"number"`
	IssueDate   *string   `json:"issueDate" db:"issue_date"`
	DueDate     *string   `json:"dueDate" db:"due_date"`
	Total       string    `json:"total" db:"total"`
	Outstanding string    `json:"outstanding" db:"outstanding"`
	Status      string    `json:"status" db:"status"`
}

// CorporateGolf is the golf activity of the nominees.
type CorporateGolf struct {
	Rounds90 int `json:"rounds90"`
	Players  int `json:"players"`
}

// CorporateSpend is the spend of the company and its nominees.
type CorporateSpend struct {
	Last12Months string `json:"last12Months"`
}

// CorporateView assembles the Corporate 360.
func (s *Service) CorporateView(ctx context.Context, q dbtx.Querier, property, cid uuid.UUID) (Corporate360, error) {
	out := Corporate360{Sections: map[string]any{}, Generated: time.Now().UTC()}
	rows, err := q.Query(ctx, `SELECT id, code, name, npwp, contact_name, email, phone, status FROM crm.corporate_accounts WHERE id = $1 AND property_id = $2`, cid, property)
	if out.Account, err = handle.One[Corporate360Account](rows, err, "corporate account"); err != nil {
		return out, err
	}
	since := time.Now().AddDate(0, 0, -90).Format("2006-01-02")
	if out.Nominees, err = handle.List[Corporate360Nominee](q.Query(ctx, `SELECT n.customer_id, c.code, c.name, n.title, n.status,
		(SELECT count(*) FROM reporting.golf_rounds g WHERE g.customer_id = n.customer_id AND g.play_date >= $2::date)::int AS rounds90
		FROM crm.corporate_nominees n JOIN crm.customers c ON c.id = n.customer_id WHERE n.corporate_account_id = $1 ORDER BY n.status, c.name`, cid, since)); err != nil {
		return out, err
	}
	for _, n := range out.Nominees {
		out.GolfActivity.Rounds90 += n.Rounds90
		if n.Rounds90 > 0 {
			out.GolfActivity.Players++
		}
	}
	rows, err = q.Query(ctx, `SELECT count(*) FILTER (WHERE status IN ('issued', 'partially_paid', 'overdue'))::int AS open_invoices,
		trim_scale(coalesce(sum(outstanding) FILTER (WHERE status IN ('issued', 'partially_paid', 'overdue')), 0))::text AS outstanding,
		trim_scale(coalesce(sum(outstanding) FILTER (WHERE status IN ('issued', 'partially_paid', 'overdue') AND due_date < current_date), 0))::text AS overdue,
		trim_scale(coalesce(sum(total) FILTER (WHERE status <> 'void' AND issue_date >= current_date - 365), 0))::text AS invoiced12m
		FROM reporting.eng_invoices WHERE property_id = $1 AND corporate_account_id = $2`, property, cid)
	if out.Billing, err = handle.One[CorporateBilling](rows, err, "billing"); err != nil {
		return out, err
	}
	if out.Invoices, err = handle.List[CorporateInvoice](q.Query(ctx, `SELECT invoice_id, number, to_char(issue_date, 'YYYY-MM-DD') AS issue_date,
		to_char(due_date, 'YYYY-MM-DD') AS due_date, trim_scale(total)::text AS total, trim_scale(outstanding)::text AS outstanding, status
		FROM reporting.eng_invoices WHERE property_id = $1 AND corporate_account_id = $2 AND status IN ('issued', 'partially_paid', 'overdue')
		ORDER BY due_date NULLS LAST LIMIT 20`, property, cid)); err != nil {
		return out, err
	}
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(l.net_amount), 0))::text FROM reporting.eng_folio_lines l WHERE l.property_id = $1 AND NOT l.liability
		AND l.posted_at >= now() - interval '12 months' AND (l.corporate_account_id = $2 OR l.customer_id IN
		(SELECT customer_id FROM crm.corporate_nominees WHERE corporate_account_id = $2))`, property, cid).Scan(&out.Spend.Last12Months); err != nil {
		return out, err
	}
	if out.Tickets, err = handle.List[ComplaintTicket](q.Query(ctx, ticketSelect+` WHERE t.customer_id IN (SELECT customer_id FROM crm.corporate_nominees
		WHERE corporate_account_id = $1) ORDER BY t.created_at DESC LIMIT 10`, cid)); err != nil {
		return out, err
	}
	for k, f := range s.CorporateSections {
		v, err := f(ctx, q, property, cid)
		if err != nil {
			return out, err
		}
		out.Sections[k] = v
	}
	return out, nil
}

var _ = errs.NotFound
