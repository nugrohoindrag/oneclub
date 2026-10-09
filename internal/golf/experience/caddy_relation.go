package experience

// Member ↔ caddy relationship and caddy wage (demo feedback 9 Oct 2026):
// how often a caddy carried for a member, the ratings the member gave,
// the favourites; the front desk asks the player for a 1–5 rating at the
// end of the session (check-out); the wage of a caddy per month — the base
// salary plus the caddy fee earned per assignment (so it grows with the
// number of assignments), tips and the deductions of the Caddy Policies.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// MemberCaddy is a caddy who carried for a customer.
type MemberCaddy struct {
	CaddyID     uuid.UUID `json:"caddyId" db:"caddy_id"`
	Code        string    `json:"code" db:"code"`
	Name        string    `json:"name" db:"name"`
	Rounds      int       `json:"rounds" db:"rounds" doc:"Times the caddy accompanied the customer"`
	FirstRound  time.Time `json:"firstRound" db:"first_round"`
	LastRound   time.Time `json:"lastRound" db:"last_round"`
	Favourite   bool      `json:"favourite" db:"favourite"`
	AvgRating   *string   `json:"avgRating" db:"avg_rating" doc:"Average rating the customer gave this caddy"`
	LastRating  *int      `json:"lastRating" db:"last_rating"`
	Requested   int       `json:"requested" db:"requested" doc:"Times the customer asked for this caddy"`
	CaddyRating *string   `json:"caddyRating" db:"caddy_rating" doc:"Overall average rating of the caddy"`
}

// MemberCaddies lists the caddies of a customer, most rounds first, with
// the favourites they never played with yet.
func (m *Module) MemberCaddies(ctx context.Context, q dbtx.Querier, customer uuid.UUID) ([]MemberCaddy, error) {
	return handle.List[MemberCaddy](q.Query(ctx, `WITH played AS (
		  SELECT a.caddy_id, count(DISTINCT a.id)::int AS rounds, min(lower(a.period)) AS first_round, max(lower(a.period)) AS last_round,
		    count(DISTINCT a.id) FILTER (WHERE a.requested)::int AS requested
		  FROM golf.caddy_assignments a JOIN golf.booking_players bp ON bp.id = ANY(a.player_ids)
		  WHERE bp.customer_id = $1 AND a.status IN ('in_play', 'completed', 'replaced') GROUP BY a.caddy_id),
		ids AS (SELECT caddy_id FROM played UNION SELECT caddy_id FROM golf.caddy_favorites WHERE customer_id = $1)
		SELECT c.id AS caddy_id, c.code, c.name, coalesce(p.rounds, 0) AS rounds, coalesce(p.first_round, f.created_at) AS first_round,
		  coalesce(p.last_round, f.created_at) AS last_round, f.caddy_id IS NOT NULL AS favourite,
		  (SELECT trim_scale(round(avg(r.rating), 1))::text FROM golf.caddy_ratings r WHERE r.caddy_id = c.id AND r.customer_id = $1) AS avg_rating,
		  (SELECT r.rating FROM golf.caddy_ratings r WHERE r.caddy_id = c.id AND r.customer_id = $1 ORDER BY r.created_at DESC LIMIT 1) AS last_rating,
		  coalesce(p.requested, 0) AS requested,
		  (SELECT trim_scale(round(avg(r.rating), 1))::text FROM golf.caddy_ratings r WHERE r.caddy_id = c.id) AS caddy_rating
		FROM ids JOIN golf.caddies c ON c.id = ids.caddy_id LEFT JOIN played p ON p.caddy_id = c.id
		LEFT JOIN golf.caddy_favorites f ON f.caddy_id = c.id AND f.customer_id = $1
		ORDER BY coalesce(p.rounds, 0) DESC, f.caddy_id IS NOT NULL DESC, c.code`, customer))
}

// FeeBand is the caddy fees of one fee amount (e.g. 18 or 9 holes).
type FeeBand struct {
	Amount      string `json:"amount" db:"amount"`
	Assignments int    `json:"assignments" db:"assignments"`
	Total       string `json:"total" db:"total"`
}

// CaddyWage is the wage of a caddy for a month.
type CaddyWage struct {
	CaddyID       uuid.UUID `json:"caddyId"`
	Month         string    `json:"month" doc:"YYYY-MM"`
	BaseSalary    string    `json:"baseSalary" doc:"Monthly base salary (gaji pokok)"`
	Assignments   int       `json:"assignments" doc:"Finished assignments in the month"`
	Members       int       `json:"members" doc:"Different customers accompanied"`
	CaddyFees     string    `json:"caddyFees" doc:"Caddy fee per assignment, summed: grows with the number of assignments"`
	FeeBands      []FeeBand `json:"feeBands"`
	TipsNonCash   string    `json:"tipsNonCash" doc:"Paid through the settlement"`
	TipsCash      string    `json:"tipsCash" doc:"Received directly by the caddy"`
	Deductions    string    `json:"deductions" doc:"Caddy Policies deductions on the caddy fees"`
	Total         string    `json:"total" doc:"Base salary + caddy fees + non-cash tips − deductions"`
	Settled       string    `json:"settled" doc:"Already in caddy fee settlements"`
	AverageRating *string   `json:"averageRating"`
}

// Wage computes a caddy's wage for a month.
func (m *Module) Wage(ctx context.Context, q dbtx.Querier, property, caddy uuid.UUID, month string) (CaddyWage, error) {
	loc := location(ctx, q, property)
	start, err := time.ParseInLocation("2006-01", month, loc)
	if err != nil {
		return CaddyWage{}, handle.Invalid("month", "invalid", "YYYY-MM")
	}
	from, to := start.Format("2006-01-02"), start.AddDate(0, 1, -1).Format("2006-01-02")
	w := CaddyWage{CaddyID: caddy, Month: month, FeeBands: []FeeBand{}}
	var base *string
	if err := q.QueryRow(ctx, `SELECT trim_scale(p.base_salary)::text FROM golf.caddies c LEFT JOIN golf.caddy_profiles p ON p.caddy_id = c.id
		WHERE c.id = $1 AND c.property_id = $2`, caddy, property).Scan(&base); err != nil {
		if dbtx.IsNoRows(err) {
			return w, errs.NotFound("caddy")
		}
		return w, err
	}
	w.BaseSalary = "0"
	if base != nil {
		w.BaseSalary = *base
	}
	const fees = `FROM golf.caddy_assignments a LEFT JOIN golf.caddy_fee_shares fs ON fs.assignment_id = a.id
		WHERE a.caddy_id = $1 AND a.status IN ('completed', 'replaced') AND a.play_date BETWEEN $2::date AND $3::date`
	if err := q.QueryRow(ctx, `SELECT count(*)::int, trim_scale(coalesce(sum(coalesce(fs.share_amount, a.fee_amount)), 0))::text `+fees, caddy, from, to).
		Scan(&w.Assignments, &w.CaddyFees); err != nil {
		return w, err
	}
	if w.FeeBands, err = handle.List[FeeBand](q.Query(ctx, `SELECT trim_scale(coalesce(fs.share_amount, a.fee_amount))::text AS amount, count(*)::int AS assignments,
		trim_scale(sum(coalesce(fs.share_amount, a.fee_amount)))::text AS total `+fees+` GROUP BY 1 ORDER BY 1 DESC`, caddy, from, to)); err != nil {
		return w, err
	}
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT bp.customer_id)::int FROM golf.caddy_assignments a JOIN golf.booking_players bp ON bp.id = ANY(a.player_ids)
		WHERE a.caddy_id = $1 AND a.status IN ('completed', 'replaced') AND a.play_date BETWEEN $2::date AND $3::date`, caddy, from, to).Scan(&w.Members); err != nil {
		return w, err
	}
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(amount) FILTER (WHERE method = 'non_cash'), 0))::text,
		trim_scale(coalesce(sum(amount) FILTER (WHERE method <> 'non_cash'), 0))::text
		FROM golf.caddy_tips WHERE caddy_id = $1 AND tip_date BETWEEN $2::date AND $3::date`, caddy, from, to).Scan(&w.TipsNonCash, &w.TipsCash); err != nil {
		return w, err
	}
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(i.amount), 0))::text FROM golf.caddy_settlement_items i JOIN golf.caddy_settlements s ON s.id = i.settlement_id
		WHERE s.caddy_id = $1 AND s.status <> 'rejected' AND s.period_start <= $3::date AND s.period_end >= $2::date`, caddy, from, to).Scan(&w.Settled); err != nil {
		return w, err
	}
	if err := q.QueryRow(ctx, `SELECT trim_scale(round(avg(rating), 2))::text FROM golf.caddy_ratings WHERE caddy_id = $1 AND created_at >= $2
		AND created_at < $3`, caddy, start, start.AddDate(0, 1, 0)).Scan(&w.AverageRating); err != nil {
		return w, err
	}
	pol, err := m.caddyPolicy(ctx, q, property)
	if err != nil {
		return w, err
	}
	fee, _ := decimal.NewFromString(w.CaddyFees)
	pct, _ := decimal.NewFromString(pol.DeductionPercent)
	per, _ := decimal.NewFromString(pol.DeductionPerRound)
	ded := fee.Mul(pct).Div(decimal.NewFromInt(100)).Add(per.Mul(decimal.NewFromInt(int64(w.Assignments)))).Round(2)
	b, _ := decimal.NewFromString(w.BaseSalary)
	tips, _ := decimal.NewFromString(w.TipsNonCash)
	w.Deductions, w.Total = ded.String(), b.Add(fee).Add(tips).Sub(ded).String()
	return w, nil
}

// DeskRatingInput is the player's rating taken by the front desk.
type DeskRatingInput struct {
	PlayerID uuid.UUID `json:"playerId" doc:"The player of the flight who rates"`
	Rating   int       `json:"rating" doc:"1–5"`
	Comment  string    `json:"comment,omitempty"`
}

// RateAtDesk records the 1–5 rating a player gives at the end of the
// session (front desk check-out).
func (m *Module) RateAtDesk(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in DeskRatingInput) error {
	var cust *uuid.UUID
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT bp.customer_id, bp.id = ANY(a.player_ids) FROM golf.caddy_assignments a JOIN golf.booking_players bp ON bp.flight_id = a.flight_id
		WHERE a.id = $1 AND bp.id = $2`, aid, in.PlayerID).Scan(&cust, &ok); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("player of the flight")
		}
		return err
	}
	if !ok {
		return errs.Validation("not_this_caddy", "the player rates their own caddy", errs.Field("playerId", "invalid", "a player of this caddy"))
	}
	return m.RateCaddy(ctx, tx, property, aid, cust, in.Rating, in.Comment, "front_desk")
}

func (m *Module) registerCaddyRelation(add func(tag string, rt route.Route)) {
	db := m.DB
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/customers/{id}/caddies", Summary: "The caddies of a member: rounds together, ratings given, favourites",
		Permission: "golf.caddy.view", Response: MemberCaddy{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MemberCaddy], error) {
			cid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[MemberCaddy]{}, err
			}
			out, err := m.MemberCaddies(ctx, tx, cid)
			return httpx.Page[MemberCaddy]{Items: out}, err
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddies/{id}/wage", Summary: "Caddy wage of a month: base salary, caddy fees per assignment, tips, deductions",
		Permission: "golf.caddy_settlement.view", Response: CaddyWage{}, Query: []route.Param{{Name: "month", Description: "YYYY-MM (default this month)"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CaddyWage, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CaddyWage{}, err
			}
			month := r.URL.Query().Get("month")
			if month == "" {
				month = localNow(ctx, tx).Format("2006-01")
			}
			return m.Wage(ctx, tx, handle.Property(ctx), cid, month)
		})})
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-assignments/{id}:rate", Summary: "The player's 1–5 caddy rating at the end of the session (front desk)",
		Permission: "golf.caddy_assignment.manage", Request: DeskRatingInput{},
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in DeskRatingInput) (handle.Empty, error) {
			aid, err := handle.ID(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, m.RateAtDesk(ctx, tx, handle.Property(ctx), aid, in)
		})})
}
