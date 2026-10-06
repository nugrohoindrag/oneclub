package engagement

// NPS (FR-TKT-06): Net Promoter Score per business line and period from P2's
// surveys and the relationship NPS of the Member App, with the trend and the
// latest comments.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// NPSScore is the score of a group of answers.
type NPSScore struct {
	Label      string `json:"label" db:"label"`
	Responses  int    `json:"responses" db:"responses"`
	Promoters  int    `json:"promoters" db:"promoters"`
	Passives   int    `json:"passives" db:"passives"`
	Detractors int    `json:"detractors" db:"detractors"`
	NPS        string `json:"nps" db:"-" doc:"% promoters − % detractors (−100 … 100)"`
}

func (s *NPSScore) compute() {
	if s.Responses == 0 {
		s.NPS = "0"
		return
	}
	s.NPS = fmt.Sprintf("%.1f", float64(s.Promoters-s.Detractors)*100/float64(s.Responses))
}

// NPSComment is a recent answer with a comment.
type NPSComment struct {
	Score        int       `json:"score" db:"score"`
	Comment      string    `json:"comment" db:"comment"`
	BusinessLine string    `json:"businessLine" db:"business_line"`
	Source       string    `json:"source" db:"source" enum:"survey,relationship"`
	CustomerName *string   `json:"customerName" db:"customer_name"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
}

// NPSReport is the NPS of a period.
type NPSReport struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Overall  NPSScore     `json:"overall"`
	ByLine   []NPSScore   `json:"byLine"`
	Trend    []NPSScore   `json:"trend" doc:"Per month (label YYYY-MM)"`
	Comments []NPSComment `json:"comments"`
}

const npsAgg = `count(*)::int AS responses, count(*) FILTER (WHERE score >= 9)::int AS promoters,
	count(*) FILTER (WHERE score BETWEEN 7 AND 8)::int AS passives, count(*) FILTER (WHERE score <= 6)::int AS detractors`

// NPS computes the NPS of a property for local dates from–to.
func NPS(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, line string) (NPSReport, error) {
	tz := location(ctx, q, property).String()
	out := NPSReport{From: from.Format("2006-01-02"), To: to.Format("2006-01-02")}
	args := []any{property, out.From, out.To, tz, line}
	where := ` FROM reporting.eng_nps WHERE property_id = $1 AND (created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date AND ($5 = '' OR business_line = $5)`
	all, err := handle.List[NPSScore](q.Query(ctx, `SELECT 'all' AS label, `+npsAgg+where, args...))
	if err != nil {
		return out, err
	}
	if len(all) == 1 {
		out.Overall = all[0]
	}
	out.Overall.compute()
	if out.ByLine, err = handle.List[NPSScore](q.Query(ctx, `SELECT business_line AS label, `+npsAgg+where+` GROUP BY 1 ORDER BY 1`, args...)); err != nil {
		return out, err
	}
	if out.Trend, err = handle.List[NPSScore](q.Query(ctx, `SELECT to_char((created_at AT TIME ZONE $4)::date, 'YYYY-MM') AS label, `+npsAgg+where+
		` GROUP BY 1 ORDER BY 1`, args...)); err != nil {
		return out, err
	}
	for i := range out.ByLine {
		out.ByLine[i].compute()
	}
	for i := range out.Trend {
		out.Trend[i].compute()
	}
	out.Comments, err = handle.List[NPSComment](q.Query(ctx, `SELECT n.score, n.comment, n.business_line, n.source, c.name AS customer_name, n.created_at
		FROM reporting.eng_nps n LEFT JOIN crm.customers c ON c.id = n.customer_id WHERE n.property_id = $1
		AND (n.created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date AND ($5 = '' OR n.business_line = $5)
		AND coalesce(n.comment, '') <> '' ORDER BY n.created_at DESC LIMIT 20`, args...))
	return out, err
}

// NPSInput is a relationship NPS answer (Member App).
type NPSInput struct {
	Score        int    `json:"score" doc:"0–10: how likely are you to recommend the club?"`
	Comment      string `json:"comment,omitempty"`
	BusinessLine string `json:"businessLine,omitempty" enum:"golf,sportclub,stay,pos,membership,banquet,other"`
}

// NPSResponse is a stored answer.
type NPSResponse struct {
	ID           uuid.UUID `json:"id" db:"id"`
	Score        int       `json:"score" db:"score"`
	Comment      *string   `json:"comment" db:"comment"`
	BusinessLine string    `json:"businessLine" db:"business_line"`
	Channel      string    `json:"channel" db:"channel"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
}

// RecordNPS stores an NPS answer; a detractor with a comment can open a
// ticket through the Complaint Policies (AutoTicketOnDetractor).
func RecordNPS(ctx context.Context, tx pgx.Tx, property uuid.UUID, customer *uuid.UUID, in NPSInput, channel string) (NPSResponse, error) {
	if in.Score < 0 || in.Score > 10 {
		return NPSResponse{}, handle.Invalid("score", "invalid_score", "score must be between 0 and 10")
	}
	if in.BusinessLine == "" {
		in.BusinessLine = "other"
	}
	if !slices.Contains(BusinessLines, in.BusinessLine) {
		return NPSResponse{}, handle.Invalid("businessLine", "invalid_line", "unknown business line")
	}
	nid := id.New()
	rows, err := tx.Query(ctx, `INSERT INTO crm.nps_responses (id, property_id, customer_id, score, comment, business_line, channel, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id, score, comment, business_line, channel, created_at`, nid, property, customer, in.Score,
		nullStr(in.Comment), in.BusinessLine, channel, actor(ctx))
	out, err := handle.One[NPSResponse](rows, err, "nps response")
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.nps_response", EntityID: nid.String(),
		EntityLabel: fmt.Sprintf("NPS %d", in.Score), PropertyID: &property, After: out})
}
