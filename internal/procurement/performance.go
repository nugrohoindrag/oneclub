package procurement

// PRD P4 FR-SUP-03 Vendor Performance: on-time delivery (GR date vs the PO
// delivery schedule), fill rate, quality (rejected quantity at goods
// receipt), return rate, price variance (invoice vs PO price) and RFQ
// response time per supplier for a period, with a weighted score and grade
// (Procurement Configuration weights); a monthly scorecard is stored by a
// job (and on demand).

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
)

// VendorPerformance is one supplier's performance for a period.
type VendorPerformance struct {
	SupplierID           uuid.UUID `json:"supplierId" db:"supplier_id"`
	SupplierCode         string    `json:"supplierCode" db:"supplier_code"`
	SupplierName         string    `json:"supplierName" db:"supplier_name"`
	Orders               int       `json:"orders" db:"orders"`
	PurchaseValue        string    `json:"purchaseValue" db:"purchase_value"`
	Receipts             int       `json:"receipts" db:"receipts"`
	OnTimeRate           *string   `json:"onTimeRate" db:"on_time_rate" doc:"0–1: receipts on or before the delivery date"`
	FillRate             *string   `json:"fillRate" db:"fill_rate" doc:"0–1: accepted ÷ ordered (orders of the period)"`
	QualityRate          *string   `json:"qualityRate" db:"quality_rate" doc:"0–1: 1 − rejected ÷ delivered at goods receipt"`
	ReturnRate           *string   `json:"returnRate" db:"return_rate" doc:"0–1: returned ÷ accepted"`
	PriceVariancePercent *string   `json:"priceVariancePercent" db:"price_variance_percent" doc:"Average invoice price vs PO price (%)"`
	ResponseHours        *string   `json:"responseHours" db:"response_hours" doc:"Average RFQ response time"`
	Score                string    `json:"score" db:"score" doc:"0–100, weighted (Procurement Configuration)"`
	Grade                string    `json:"grade" db:"grade" enum:"A,B,C,D,n/a"`
}

// VendorScorecard is a stored monthly scorecard.
type VendorScorecard struct {
	VendorPerformance
	ID         uuid.UUID `json:"id" db:"id"`
	Period     string    `json:"period" db:"period"`
	ComputedAt time.Time `json:"computedAt" db:"computed_at"`
}

// VendorScorecardComputeInput selects the scorecard period.
type VendorScorecardComputeInput struct {
	Period string `json:"period,omitempty" doc:"YYYY-MM; default: previous month"`
}

const performanceSQL = `SELECT s.id AS supplier_id, s.code AS supplier_code, s.name AS supplier_name,
	(SELECT count(*) FROM procurement.purchase_orders o WHERE o.supplier_id = s.id AND o.order_date BETWEEN $2::date AND $3::date
	  AND o.status NOT IN ('draft', 'pending_approval', 'cancelled'))::int AS orders,
	trim_scale(coalesce((SELECT sum(o.total) FROM procurement.purchase_orders o WHERE o.supplier_id = s.id AND o.order_date BETWEEN $2::date AND $3::date
	  AND o.status NOT IN ('draft', 'pending_approval', 'cancelled')), 0))::text AS purchase_value,
	gr.receipts::int AS receipts,
	CASE WHEN gr.po_receipts > 0 THEN round(gr.on_time::numeric / gr.po_receipts, 4)::text END AS on_time_rate,
	(SELECT CASE WHEN sum(l.quantity - l.cancelled_quantity) > 0 THEN round(least(sum(l.received_quantity) / sum(l.quantity - l.cancelled_quantity), 1), 4)::text END
	  FROM procurement.purchase_order_lines l JOIN procurement.purchase_orders o ON o.id = l.purchase_order_id WHERE o.supplier_id = s.id
	  AND o.order_type = 'goods' AND o.order_date BETWEEN $2::date AND $3::date AND o.status NOT IN ('draft', 'pending_approval', 'cancelled')) AS fill_rate,
	CASE WHEN gr.delivered > 0 THEN round(1 - gr.rejected / gr.delivered, 4)::text END AS quality_rate,
	CASE WHEN gr.accepted > 0 THEN round(gr.returned / gr.accepted, 4)::text END AS return_rate,
	(SELECT round(avg((il.unit_price - pl.unit_price * (1 - pl.discount_percent / 100)) / nullif(pl.unit_price * (1 - pl.discount_percent / 100), 0) * 100), 4)::text
	  FROM procurement.vendor_invoice_lines il JOIN procurement.vendor_invoices v ON v.id = il.vendor_invoice_id
	  JOIN procurement.purchase_order_lines pl ON pl.id = il.purchase_order_line_id
	  WHERE v.supplier_id = s.id AND v.status NOT IN ('draft', 'cancelled') AND v.invoice_date BETWEEN $2::date AND $3::date) AS price_variance_percent,
	(SELECT round(avg(extract(epoch FROM x.responded_at - x.sent_at) / 3600)::numeric, 2)::text FROM procurement.rfq_suppliers x
	  WHERE x.supplier_id = s.id AND x.responded_at IS NOT NULL AND x.sent_at IS NOT NULL AND (x.sent_at AT TIME ZONE 'UTC')::date BETWEEN $2::date AND $3::date) AS response_hours,
	'0' AS score, 'n/a' AS grade
	FROM procurement.suppliers s
	LEFT JOIN LATERAL (SELECT count(*) AS receipts, count(*) FILTER (WHERE g.purchase_order_id IS NOT NULL) AS po_receipts,
	  count(*) FILTER (WHERE g.purchase_order_id IS NOT NULL AND g.received_date <= coalesce((SELECT max(pl.expected_date)
	    FROM procurement.goods_receipt_lines gl JOIN procurement.purchase_order_lines pl ON pl.id = gl.purchase_order_line_id
	    WHERE gl.goods_receipt_id = g.id), (SELECT o.expected_date FROM procurement.purchase_orders o WHERE o.id = g.purchase_order_id), g.received_date)) AS on_time,
	  coalesce(sum((SELECT sum(delivered_quantity) FROM procurement.goods_receipt_lines WHERE goods_receipt_id = g.id)), 0) AS delivered,
	  coalesce(sum((SELECT sum(rejected_quantity) FROM procurement.goods_receipt_lines WHERE goods_receipt_id = g.id)), 0) AS rejected,
	  coalesce(sum((SELECT sum(accepted_quantity) FROM procurement.goods_receipt_lines WHERE goods_receipt_id = g.id)), 0) AS accepted,
	  coalesce(sum((SELECT sum(returned_quantity) FROM procurement.goods_receipt_lines WHERE goods_receipt_id = g.id)), 0) AS returned
	  FROM procurement.goods_receipts g WHERE g.supplier_id = s.id AND g.status = 'posted' AND g.received_date BETWEEN $2::date AND $3::date) gr ON true
	WHERE s.property_id = $1 AND s.archived_at IS NULL AND ($4::uuid IS NULL OR s.id = $4)`

// Performance computes vendor performance for [from, to].
func Performance(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, supplier *uuid.UUID) ([]VendorPerformance, error) {
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return nil, err
	}
	rows, err := handle.List[VendorPerformance](q.Query(ctx, performanceSQL+` ORDER BY s.name`, property, from.Format("2006-01-02"), to.Format("2006-01-02"),
		supplier))
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		if r.Orders == 0 && r.Receipts == 0 && r.ResponseHours == nil && r.PriceVariancePercent == nil && supplier == nil {
			continue // no activity in the period
		}
		for _, p := range []*string{r.OnTimeRate, r.FillRate, r.QualityRate, r.ReturnRate, r.PriceVariancePercent, r.ResponseHours} {
			if p != nil {
				*p = dec(*p).String()
			}
		}
		r.Score, r.Grade = score(cfg.ScoreWeights, r)
		out = append(out, r)
	}
	return out, nil
}

// score weighs the available components (missing data is left out).
func score(w VendorScoreWeights, r VendorPerformance) (string, string) {
	total, weight := decimal.Zero, 0
	add := func(v *string, wt int, f func(decimal.Decimal) decimal.Decimal) {
		if v == nil || wt <= 0 {
			return
		}
		c := f(dec(*v))
		if c.IsNegative() {
			c = decimal.Zero
		}
		if c.GreaterThan(hundred) {
			c = hundred
		}
		total = total.Add(c.Mul(decimal.NewFromInt(int64(wt))))
		weight += wt
	}
	pct := func(d decimal.Decimal) decimal.Decimal { return d.Mul(hundred) }
	add(r.OnTimeRate, w.OnTime, pct)
	add(r.QualityRate, w.Quality, pct)
	add(r.FillRate, w.Fill, pct)
	add(r.PriceVariancePercent, w.Price, func(d decimal.Decimal) decimal.Decimal { return hundred.Sub(d.Abs().Mul(decimal.NewFromInt(10))) })
	add(r.ResponseHours, w.Response, func(d decimal.Decimal) decimal.Decimal {
		switch {
		case d.LessThanOrEqual(decimal.NewFromInt(24)):
			return hundred
		case d.LessThanOrEqual(decimal.NewFromInt(72)):
			return decimal.NewFromInt(70)
		}
		return decimal.NewFromInt(40)
	})
	if weight == 0 {
		return "0", "n/a"
	}
	s := total.Div(decimal.NewFromInt(int64(weight))).Round(2)
	grade := "D"
	switch {
	case s.GreaterThanOrEqual(decimal.NewFromInt(90)):
		grade = "A"
	case s.GreaterThanOrEqual(decimal.NewFromInt(75)):
		grade = "B"
	case s.GreaterThanOrEqual(decimal.NewFromInt(60)):
		grade = "C"
	}
	return s.String(), grade
}

var periodRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ComputeScorecards stores the scorecards of a month for a property.
func ComputeScorecards(ctx context.Context, tx pgx.Tx, property uuid.UUID, period string) ([]VendorScorecard, error) {
	if period == "" {
		t := localToday(ctx, tx, property)
		period = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")
	}
	if !periodRe.MatchString(period) {
		return nil, handle.Invalid("period", "invalid", "YYYY-MM")
	}
	from, _ := time.Parse("2006-01", period)
	to := from.AddDate(0, 1, -1)
	rows, err := Performance(ctx, tx, property, from, to, nil)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.vendor_scorecards (id, property_id, supplier_id, period, orders, receipts, on_time_rate, fill_rate,
			quality_rate, return_rate, price_variance_percent, response_hours, purchase_value, score, grade, computed_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15,$16)
			ON CONFLICT (property_id, supplier_id, period) DO UPDATE SET orders = EXCLUDED.orders, receipts = EXCLUDED.receipts,
			on_time_rate = EXCLUDED.on_time_rate, fill_rate = EXCLUDED.fill_rate, quality_rate = EXCLUDED.quality_rate, return_rate = EXCLUDED.return_rate,
			price_variance_percent = EXCLUDED.price_variance_percent, response_hours = EXCLUDED.response_hours, purchase_value = EXCLUDED.purchase_value,
			score = EXCLUDED.score, grade = EXCLUDED.grade, computed_at = now(), computed_by = EXCLUDED.computed_by`,
			id.New(), property, r.SupplierID, period, r.Orders, r.Receipts, r.OnTimeRate, r.FillRate, r.QualityRate, r.ReturnRate, r.PriceVariancePercent,
			r.ResponseHours, r.PurchaseValue, r.Score, r.Grade, actor(ctx)); err != nil {
			return nil, err
		}
	}
	return listScorecards(ctx, tx, property, period, "")
}

const scorecardSelect = `SELECT c.id, c.period, c.computed_at, c.supplier_id, s.code AS supplier_code, s.name AS supplier_name, c.orders,
	trim_scale(c.purchase_value)::text AS purchase_value, c.receipts, trim_scale(c.on_time_rate)::text AS on_time_rate, trim_scale(c.fill_rate)::text AS fill_rate,
	trim_scale(c.quality_rate)::text AS quality_rate, trim_scale(c.return_rate)::text AS return_rate,
	trim_scale(c.price_variance_percent)::text AS price_variance_percent, trim_scale(c.response_hours)::text AS response_hours,
	trim_scale(c.score)::text AS score, c.grade FROM procurement.vendor_scorecards c JOIN procurement.suppliers s ON s.id = c.supplier_id`

func listScorecards(ctx context.Context, q dbtx.Querier, property uuid.UUID, period, supplier string) ([]VendorScorecard, error) {
	return handle.List[VendorScorecard](q.Query(ctx, scorecardSelect+` WHERE c.property_id = $1 AND ($2 = '' OR c.period = $2)
		AND ($3 = '' OR c.supplier_id::text = $3) ORDER BY c.period DESC, c.score DESC LIMIT 500`, property, period, supplier))
}

// ScorecardArgs computes last month's scorecards (monthly job).
type ScorecardArgs struct{}

func (ScorecardArgs) Kind() string { return "procurement_vendor_scorecard" }
func (ScorecardArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// ScorecardWorker runs the monthly vendor scorecard for every property.
type ScorecardWorker struct {
	river.WorkerDefaults[ScorecardArgs]
	DB *dbtx.DB
}

func (w *ScorecardWorker) Work(ctx context.Context, _ *river.Job[ScorecardArgs]) error {
	ctx = dbtx.System(ctx)
	var props []uuid.UUID
	if err := w.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties ORDER BY created_at`)
		if err != nil {
			return err
		}
		props, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return err
	}
	for _, p := range props {
		if err := w.DB.WithTx(ctx, func(tx pgx.Tx) error {
			if localNow(ctx, tx, p).Day() != 1 {
				return nil
			}
			cards, err := ComputeScorecards(ctx, tx, p, "")
			if err != nil || len(cards) == 0 {
				return err
			}
			pp := p
			return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "compute", Category: audit.CategorySystem, ActorName: "oneclub worker",
				EntityType: "procurement.vendor_scorecard", EntityLabel: cards[0].Period, PropertyID: &pp, Metadata: map[string]any{"suppliers": len(cards)}})
		}); err != nil {
			return fmt.Errorf("vendor scorecard for %s: %w", p, err)
		}
	}
	return nil
}

// RegisterJobs adds the monthly vendor scorecard (runs daily, acts on the 1st).
func RegisterJobs(reg *jobs.Registrar, db *dbtx.DB, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &ScorecardWorker{DB: db})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 6, Minute: 20, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return ScorecardArgs{}, nil }, nil))
}

func (m *Module) registerPerformance(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", "Vendor Performance", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/procurement/vendor-performance",
		Summary:    "Vendor Performance per supplier (on-time, fill, quality, returns, price variance, response; score & grade)",
		Permission: "procurement.vendor_performance.view", Response: VendorPerformance{}, List: true,
		Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD (default: 90 days ago)"}, {Name: "to"}, {Name: "supplierId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VendorPerformance], error) {
			p := handle.Property(ctx)
			today := localToday(ctx, tx, p)
			from, err := handle.QueryDate(r, "from", today.AddDate(0, 0, -90))
			if err != nil {
				return httpx.Page[VendorPerformance]{}, err
			}
			to, err := handle.QueryDate(r, "to", today)
			if err != nil {
				return httpx.Page[VendorPerformance]{}, err
			}
			if to.Before(from) {
				return httpx.Page[VendorPerformance]{}, errs.BadRequest("invalid_period", "to must not be before from")
			}
			sup, err := handle.QueryUUID(r, "supplierId")
			if err != nil {
				return httpx.Page[VendorPerformance]{}, err
			}
			return handle.Page(Performance(ctx, tx, p, from, to, sup))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/procurement/vendor-performance:compute", Summary: "Compute and store the monthly vendor scorecards",
		Permission: "procurement.vendor_performance.compute", Request: VendorScorecardComputeInput{}, Response: VendorScorecard{}, List: true, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in VendorScorecardComputeInput) (httpx.Page[VendorScorecard], error) {
			p := handle.Property(ctx)
			cards, err := ComputeScorecards(ctx, tx, p, in.Period)
			if err != nil {
				return httpx.Page[VendorScorecard]{}, err
			}
			period := in.Period
			if len(cards) > 0 {
				period = cards[0].Period
			}
			return handle.Page(cards, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "compute", EntityType: "procurement.vendor_scorecard",
				EntityLabel: period, PropertyID: &p, Metadata: map[string]any{"suppliers": len(cards)}}))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/procurement/vendor-scorecards", Summary: "Stored monthly vendor scorecards",
		Permission: "procurement.vendor_performance.view", Response: VendorScorecard{}, List: true,
		Query: []route.Param{{Name: "filter[period]"}, {Name: "filter[supplierId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VendorScorecard], error) {
			lp := httpx.ParseList(r)
			return handle.Page(listScorecards(ctx, tx, handle.Property(ctx), lp.Filters["period"], lp.Filters["supplierId"]))
		})})
}
