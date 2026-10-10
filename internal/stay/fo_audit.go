package stay

// Front office night audit and the reports of the bungalows (docs/
// requirement-booking-hotel-mgcc.md FR-H74, FR-H75, FR-H79, FR-H81): the
// checklist of a business day — arrivals that did not come (no-show of the
// policy), room nights to post, guests past their check-out, folios with an
// odd balance, the payments of the day — run with the night audit of
// Billing (the same posting and no-show step) and freezing the Manager
// Flash of the day; the hotel tax (PB1) report, the deposits and city
// ledger, the settlement of the online payments and their CSV export.
// KPI follow §12.2: occupancy = room nights sold ÷ bungalows available
// (0–100 %), ADR = room revenue before tax ÷ sold, RevPAR = room revenue ÷
// available (≤ ADR).

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// FlashReport is the Manager Flash of a business day.
type FlashReport struct {
	Date            string      `json:"date"`
	Frozen          bool        `json:"frozen" doc:"The night audit closed the day: the figures are those of the close"`
	Units           int         `json:"units"`
	OutOfOrder      int         `json:"outOfOrder"`
	Available       int         `json:"available"`
	Sold            int         `json:"sold"`
	Occupancy       string      `json:"occupancy"`
	ADR             string      `json:"adr"`
	RevPAR          string      `json:"revpar"`
	RoomRevenue     string      `json:"roomRevenue" doc:"Before tax and service"`
	AddonRevenue    string      `json:"addonRevenue"`
	FnbChargeToRoom string      `json:"fnbChargeToRoom" doc:"POS orders charged to the rooms"`
	OtherRevenue    string      `json:"otherRevenue"`
	Arrivals        int         `json:"arrivals"`
	Departures      int         `json:"departures"`
	InHouse         int         `json:"inHouse"`
	InHouseGuests   int         `json:"inHouseGuests"`
	NoShows         int         `json:"noShows"`
	Cancellations   int         `json:"cancellations"`
	Expired         int         `json:"expired" doc:"Online holds that ran out unpaid"`
	MTD             NightStat   `json:"mtd" doc:"Month to date"`
	BySource        []Breakdown `json:"bySource" doc:"Bookings made today per channel (no OTA)"`
}

// Flash computes the Manager Flash of a local day (or the frozen one).
func (m *Module) Flash(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (FlashReport, error) {
	loc := calendar.Location(ctx, q)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	var raw []byte
	err := q.QueryRow(ctx, `SELECT figures FROM stay.front_office_audits WHERE property_id = $1 AND business_date = $2::date`, property,
		from.Format(time.DateOnly)).Scan(&raw)
	if err == nil && len(raw) > 2 {
		var out FlashReport
		if json.Unmarshal(raw, &out) == nil {
			out.Frozen = true
			return out, nil
		}
	}
	if err != nil && !dbtx.IsNoRows(err) {
		return FlashReport{}, err
	}
	to := from.AddDate(0, 0, 1)
	out := FlashReport{Date: from.Format(time.DateOnly), BySource: []Breakdown{}}
	nights, err := m.nightStats(ctx, q, property, from, 1)
	if err != nil {
		return out, err
	}
	n := nights[0]
	out.Units, out.OutOfOrder, out.Available, out.Sold, out.RoomRevenue = n.Units, n.OutOfOrder, n.Available, n.Occupied, n.RoomRevenue
	out.Occupancy, out.ADR, out.RevPAR = n.Occupancy, n.ADR, n.RevPAR
	var addon, fnb, other string
	if err := q.QueryRow(ctx, `SELECT
		coalesce(sum(l.net_amount) FILTER (WHERE l.reference_type = 'stay.addon'), 0)::text,
		coalesce(sum(l.net_amount) FILTER (WHERE l.business_line = 'pos'), 0)::text,
		coalesce(sum(l.net_amount) FILTER (WHERE coalesce(l.reference_type, '') NOT IN ('reservation.line', 'stay.addon') AND l.business_line <> 'pos'), 0)::text
		FROM reporting.eng_folio_lines l JOIN stay.stays s ON s.folio_id = l.folio_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND l.posted_at >= $2 AND l.posted_at < $3`, property, from, to).Scan(&addon, &fnb, &other); err != nil {
		return out, err
	}
	out.AddonRevenue, out.FnbChargeToRoom, out.OtherRevenue = decOf(addon).Round(0).String(), decOf(fnb).Round(0).String(), decOf(other).Round(0).String()
	fo, err := m.FrontOfficeDay(ctx, q, property, from)
	if err != nil {
		return out, err
	}
	out.Arrivals, out.Departures, out.InHouse, out.InHouseGuests = len(fo.Arrivals), len(fo.Departures), len(fo.InHouse), fo.Counts["inHouseGuests"]
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'no_show' AND start_at >= $2::timestamptz - interval '1 day' AND start_at < $3)::int,
		count(*) FILTER (WHERE status = 'cancelled' AND cancelled_at >= $2 AND cancelled_at < $3)::int,
		count(*) FILTER (WHERE status = 'expired' AND updated_at >= $2 AND updated_at < $3)::int
		FROM stay.stays WHERE property_id = $1 AND kind = 'bungalow'`, property, from, to).Scan(&out.NoShows, &out.Cancellations, &out.Expired); err != nil {
		return out, err
	}
	first := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, loc)
	mtd, err := m.nightStats(ctx, q, property, first, from.Day())
	if err != nil {
		return out, err
	}
	rev := decimal.Zero
	out.MTD = NightStat{Date: first.Format(time.DateOnly)}
	for _, x := range mtd {
		out.MTD.Units, out.MTD.OutOfOrder = out.MTD.Units+x.Units, out.MTD.OutOfOrder+x.OutOfOrder
		out.MTD.Available, out.MTD.Occupied = out.MTD.Available+x.Available, out.MTD.Occupied+x.Occupied
		rev = rev.Add(decOf(x.RoomRevenue))
	}
	out.MTD.RoomRevenue = rev.String()
	out.MTD.Occupancy, out.MTD.ADR, out.MTD.RevPAR = kpis(rev, out.MTD.Occupied, out.MTD.Available)
	out.BySource, err = handle.List[Breakdown](q.Query(ctx, `SELECT coalesce(s.booking_source, s.channel) AS key, count(*)::int AS count,
		sum(greatest(1, round(extract(epoch FROM s.end_at - s.start_at) / 86400)))::int AS nights, '0' AS revenue FROM stay.stays s
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.created_at >= $2 AND s.created_at < $3 AND s.status NOT IN ('expired', 'void')
		GROUP BY 1 ORDER BY 2 DESC`, property, from, to))
	return out, err
}

// AuditItem is a stay on the night audit checklist.
type AuditItem struct {
	StayID uuid.UUID `json:"stayId"`
	StayNo string    `json:"stayNo"`
	Guest  string    `json:"guest"`
	Unit   string    `json:"unit"`
	Detail string    `json:"detail"`
}

// AuditCheck is one step of the front office night audit.
type AuditCheck struct {
	Key      string      `json:"key" enum:"no_shows,room_postings,overdue_departures,folio_balance,payments"`
	Label    string      `json:"label"`
	Severity string      `json:"severity" enum:"info,warning"`
	Count    int         `json:"count"`
	Items    []AuditItem `json:"items"`
	Detail   *string     `json:"detail"`
}

// FrontOfficeAudit is the night audit of a business day.
type FrontOfficeAudit struct {
	BusinessDate string       `json:"businessDate"`
	Closed       bool         `json:"closed"`
	ClosedAt     *time.Time   `json:"closedAt"`
	Checklist    []AuditCheck `json:"checklist"`
	Flash        FlashReport  `json:"flash"`
	Notes        *string      `json:"notes"`
}

// AuditRunInput closes the business day.
type AuditRunInput struct {
	Date  string `json:"date,omitempty" doc:"Business date YYYY-MM-DD; default today"`
	Notes string `json:"notes,omitempty"`
}

// FOAudit builds the checklist of a business day (FR-H74).
func (m *Module) FOAudit(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (FrontOfficeAudit, error) {
	loc := calendar.Location(ctx, q)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	next := from.AddDate(0, 0, 1)
	out := FrontOfficeAudit{BusinessDate: from.Format(time.DateOnly), Checklist: []AuditCheck{}}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT checklist, closed_at, notes FROM stay.front_office_audits WHERE property_id = $1 AND business_date = $2::date`, property,
		out.BusinessDate).Scan(&raw, &out.ClosedAt, &out.Notes)
	if err == nil {
		out.Closed = true
		_ = json.Unmarshal(raw, &out.Checklist)
		out.Flash, err = m.Flash(ctx, q, property, from)
		return out, err
	}
	if !dbtx.IsNoRows(err) {
		return out, err
	}
	pol, _, err := m.policy(ctx, q, property)
	if err != nil {
		return out, err
	}
	item := func(s Stay, detail string) AuditItem {
		return AuditItem{StayID: s.ID, StayNo: s.StayNo, Guest: guestLabel(s), Unit: nonEmpty(deref(s.UnitCode), s.UnitName), Detail: detail}
	}
	// 1. arrivals that did not check in
	due, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'reserved'
		AND r.status IN ('pending', 'confirmed') AND s.start_at < $2 AND s.package_booking_id IS NULL ORDER BY s.start_at`, property, next))
	if err != nil {
		return out, err
	}
	c := AuditCheck{Key: "no_shows", Label: "Kedatangan belum check-in → No-show", Severity: "warning", Items: []AuditItem{}}
	if !pol.AutoNoShow {
		d := "Stay Policies: no-show otomatis tidak aktif — tandai manual"
		c.Detail = &d
	}
	for _, s := range due {
		c.Items = append(c.Items, item(s, "datang "+s.Start.In(loc).Format("02 Jan")+" · "+s.PaymentStatus))
	}
	c.Count = len(c.Items)
	out.Checklist = append(out.Checklist, c)
	// 2. room nights to post
	inHouse, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'checked_in'
		ORDER BY b.code`, property))
	if err != nil {
		return out, err
	}
	c = AuditCheck{Key: "room_postings", Label: "Posting room & tax per malam", Severity: "info", Items: []AuditItem{}}
	for _, s := range inHouse {
		if s.RoomPosting != "nightly" {
			continue
		}
		var posted int
		if err := q.QueryRow(ctx, `SELECT count(*)::int FROM stay.night_postings WHERE stay_id = $1`, s.ID).Scan(&posted); err != nil {
			return out, err
		}
		a := s.Start.In(loc)
		arrival := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc)
		want := min(int(next.Sub(arrival).Hours()/24+0.5), s.Nights)
		if want > posted {
			c.Items = append(c.Items, item(s, fmt.Sprintf("%d malam belum diposting", want-posted)))
		}
	}
	c.Count = len(c.Items)
	out.Checklist = append(out.Checklist, c)
	// 3. guests past their check-out
	c = AuditCheck{Key: "overdue_departures", Label: "Seharusnya check-out tapi masih in-house", Severity: "warning", Items: []AuditItem{}}
	for _, s := range inHouse {
		end := s.End
		if s.LateCheckOutUntil != nil && s.LateCheckOutUntil.After(end) {
			end = *s.LateCheckOutUntil
		}
		if end.Before(next) {
			c.Items = append(c.Items, item(s, "check-out "+end.In(loc).Format("02 Jan 15:04")))
		}
	}
	c.Count = len(c.Items)
	out.Checklist = append(out.Checklist, c)
	// 4. folios with a credit (paid more than due) or a checked-out debit
	odd, err := handle.List[Stay](q.Query(ctx, `SELECT x.* FROM (`+staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow'
		AND s.status IN ('checked_in', 'checked_out', 'cancelled', 'no_show') AND s.updated_at > $2::timestamptz - interval '30 days') x
		WHERE (x.paid::numeric > x.total_due::numeric) OR (x.status = 'checked_out' AND x.total_due::numeric > x.paid::numeric) ORDER BY x.stay_no`, property, next))
	if err != nil {
		return out, err
	}
	c = AuditCheck{Key: "folio_balance", Label: "Folio dengan saldo kredit/debit tidak wajar", Severity: "warning", Items: []AuditItem{}}
	for _, s := range odd {
		bal := decOf(s.TotalDue).Sub(decOf(s.Paid))
		c.Items = append(c.Items, item(s, "saldo "+rupiah(bal.String())+" · "+s.Status))
	}
	c.Count = len(c.Items)
	out.Checklist = append(out.Checklist, c)
	// 5. the payments of the day (shift recap)
	sum, err := billing.DailyPaymentSummary(ctx, q, property, from)
	if err != nil {
		return out, err
	}
	c = AuditCheck{Key: "payments", Label: "Rekap pembayaran hari ini (tutup shift kasir)", Severity: "info", Items: []AuditItem{}}
	parts := []string{}
	for _, r := range sum.ByMethod {
		parts = append(parts, fmt.Sprintf("%s %s", strings.ReplaceAll(r.MethodType, "_", " "), rupiah(r.Net)))
	}
	d := "Total " + rupiah(sum.Total) + " · " + strings.Join(parts, " · ")
	c.Detail, c.Count = &d, len(sum.ByMethod)
	out.Checklist = append(out.Checklist, c)
	out.Flash, err = m.Flash(ctx, q, property, from)
	return out, err
}

// RunFOAudit closes a business day: the room nights are posted and the
// no-shows marked (the Stay step of the Billing night audit), then the
// checklist and the Manager Flash are frozen.
func (m *Module) RunFOAudit(ctx context.Context, tx pgx.Tx, property uuid.UUID, in AuditRunInput) (FrontOfficeAudit, error) {
	loc := calendar.Location(ctx, tx)
	day := localToday(ctx, tx)
	if in.Date != "" {
		d, err := time.ParseInLocation(time.DateOnly, in.Date, loc)
		if err != nil {
			return FrontOfficeAudit{}, handle.Invalid("date", "invalid_date", "date must be YYYY-MM-DD")
		}
		day = d
	}
	if day.After(localToday(ctx, tx)) {
		return FrontOfficeAudit{}, handle.Invalid("date", "in_the_future", "a future business day cannot be closed")
	}
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stay.front_office_audits WHERE property_id = $1 AND business_date = $2::date)`, property,
		day.Format(time.DateOnly)).Scan(&closed); err != nil {
		return FrontOfficeAudit{}, err
	}
	if closed {
		return FrontOfficeAudit{}, errs.Conflict("closed", "the night audit of "+day.Format("02 Jan 2006")+" is already closed")
	}
	if _, err := m.NightAudit(ctx, tx, property, day); err != nil {
		return FrontOfficeAudit{}, err
	}
	a, err := m.FOAudit(ctx, tx, property, day)
	if err != nil {
		return a, err
	}
	checklist, _ := json.Marshal(a.Checklist)
	figures, _ := json.Marshal(a.Flash)
	if _, err := tx.Exec(ctx, `INSERT INTO stay.front_office_audits (id, property_id, business_date, checklist, figures, notes, closed_by)
		VALUES (gen_random_uuid(), $1, $2::date, $3, $4, $5, $6)`, property, day.Format(time.DateOnly), checklist, figures, nzs(in.Notes), actor(ctx)); err != nil {
		return a, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "night_audit", EntityType: "stay.front_office_audit", EntityID: day.Format(time.DateOnly),
		PropertyID: &property, After: map[string]any{"occupancy": a.Flash.Occupancy, "roomRevenue": a.Flash.RoomRevenue, "noShows": a.Flash.NoShows}}); err != nil {
		return a, err
	}
	return m.FOAudit(ctx, tx, property, day)
}

// TaxRow is a day of the hotel tax report (PB1).
type TaxRow struct {
	Date      string `json:"date" db:"date"`
	Component string `json:"component" db:"component" doc:"room, addon, fnb, other"`
	Net       string `json:"net" db:"net"`
	Service   string `json:"service" db:"service"`
	Tax       string `json:"tax" db:"tax"`
	Total     string `json:"total" db:"total"`
}

func (m *Module) taxReport(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]TaxRow, error) {
	loc := calendar.Location(ctx, q)
	return handle.List[TaxRow](q.Query(ctx, `SELECT to_char(l.posted_at AT TIME ZONE $4, 'YYYY-MM-DD') AS date,
		CASE WHEN l.reference_type = 'reservation.line' OR l.revenue_component IN ('bungalow', 'late_checkout_fee', 'no_show_fee') THEN 'room'
		  WHEN l.reference_type = 'stay.addon' THEN 'addon' WHEN l.business_line = 'pos' THEN 'fnb' ELSE 'other' END AS component,
		trim_scale(sum(l.net_amount))::text AS net, trim_scale(sum(l.service_amount))::text AS service, trim_scale(sum(l.tax_amount))::text AS tax,
		trim_scale(sum(l.total))::text AS total
		FROM reporting.eng_folio_lines l JOIN stay.stays s ON s.folio_id = l.folio_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND l.posted_at >= $2 AND l.posted_at < $3 GROUP BY 1, 2 ORDER BY 1, 2`,
		property, from, to, loc.String()))
}

// DepositRow is a deposit held or a city ledger charge of a stay (FR-H79).
type DepositRow struct {
	StayNo   string     `json:"stayNo" db:"stay_no"`
	Guest    string     `json:"guest" db:"guest"`
	Status   string     `json:"status" db:"status"`
	Kind     string     `json:"kind" db:"kind" enum:"deposit,city_ledger"`
	Number   string     `json:"number" db:"number"`
	Amount   string     `json:"amount" db:"amount"`
	Applied  string     `json:"applied" db:"applied"`
	Company  *string    `json:"company" db:"company"`
	PaidAt   *time.Time `json:"paidAt" db:"paid_at"`
	Arrival  time.Time  `json:"arrival" db:"arrival"`
	Departed *time.Time `json:"departed" db:"departed"`
}

func (m *Module) depositReport(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]DepositRow, error) {
	return handle.List[DepositRow](q.Query(ctx, `SELECT s.stay_no, coalesce(c.name, s.guest_name, s.corporate_name, '') AS guest, s.status, 'deposit' AS kind, d.number,
		trim_scale(d.amount)::text AS amount, trim_scale(d.applied_amount)::text AS applied, s.corporate_name AS company, d.created_at AS paid_at,
		s.start_at AS arrival, s.checked_out_at AS departed
		FROM billing.deposits d JOIN stay.stays s ON s.folio_id = d.folio_id LEFT JOIN reporting.customer_directory c ON c.id = s.customer_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND d.amount > d.applied_amount AND d.status NOT IN ('refunded', 'cancelled')
		UNION ALL
		SELECT s.stay_no, coalesce(c.name, s.guest_name, s.corporate_name, ''), s.status, 'city_ledger', p.number, trim_scale(p.amount)::text, '0',
		  coalesce(s.corporate_name, ca.corporate_account_id::text), p.paid_at, s.start_at, s.checked_out_at
		FROM billing.payments p JOIN stay.stays s ON s.folio_id = p.folio_id JOIN billing.customer_accounts ca ON ca.id = p.account_id
		LEFT JOIN reporting.customer_directory c ON c.id = s.customer_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND p.method_type = 'member_account' AND ca.corporate_account_id IS NOT NULL AND p.status = 'completed'
		ORDER BY 10 DESC LIMIT 500`, property))
}

// SettlementRow is an online payment of a stay folio (FR-H81).
type SettlementRow struct {
	Number  string     `json:"number" db:"number"`
	StayNo  string     `json:"stayNo" db:"stay_no"`
	Method  string     `json:"method" db:"method"`
	Amount  string     `json:"amount" db:"amount"`
	Status  string     `json:"status" db:"status"`
	Created time.Time  `json:"created" db:"created_at"`
	PaidAt  *time.Time `json:"paidAt" db:"paid_at"`
	Gateway *string    `json:"gateway" db:"gateway"`
}

func (m *Module) settlementReport(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]SettlementRow, error) {
	return handle.List[SettlementRow](q.Query(ctx, `SELECT p.number, s.stay_no, p.method_type AS method, trim_scale(p.amount)::text AS amount, p.status, p.created_at,
		p.paid_at, p.integration_code AS gateway FROM billing.payments p JOIN stay.stays s ON s.folio_id = p.folio_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND p.channel = 'online' AND p.created_at >= $2 AND p.created_at < $3 ORDER BY p.created_at DESC`,
		property, from, to))
}

// writeCSV streams rows as CSV (Excel opens it).
func writeCSV(w http.ResponseWriter, name string, head []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	_ = cw.Write(head)
	for _, r := range rows {
		_ = cw.Write(r)
	}
	cw.Flush()
}

func (m *Module) registerFOAudit(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "stay", tagAccommodation, route.ScopeProperty
		reg.Add(rt)
	}
	day := func(ctx context.Context, tx pgx.Tx, r *http.Request) (time.Time, error) {
		loc := calendar.Location(ctx, tx)
		d, err := handle.QueryDate(r, "date", localToday(ctx, tx))
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), err
	}
	period := func(ctx context.Context, tx pgx.Tx, r *http.Request) (time.Time, time.Time, error) {
		loc := calendar.Location(ctx, tx)
		t := localToday(ctx, tx)
		from, err := handle.QueryDate(r, "from", time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc))
		if err != nil {
			return t, t, err
		}
		to, err := handle.QueryDate(r, "to", t)
		if err != nil {
			return t, t, err
		}
		return time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc), time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1), nil
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/night-audit", Summary: "Front office night audit of a business day: checklist and Manager Flash",
		Permission: "stay.night_audit.view", Response: FrontOfficeAudit{}, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FrontOfficeAudit, error) {
			d, err := day(ctx, tx, r)
			if err != nil {
				return FrontOfficeAudit{}, err
			}
			return m.FOAudit(ctx, tx, handle.Property(ctx), d)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/night-audit", Summary: "Close the business day: post the nights, mark the no-shows, freeze the figures",
		Permission: "stay.night_audit.run", Request: AuditRunInput{}, Response: FrontOfficeAudit{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AuditRunInput) (FrontOfficeAudit, error) {
			return m.RunFOAudit(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/flash", Summary: "Manager Flash Report of a day (frozen after the night audit)",
		Permission: "stay.dashboard.view", Response: FlashReport{}, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FlashReport, error) {
			d, err := day(ctx, tx, r)
			if err != nil {
				return FlashReport{}, err
			}
			return m.Flash(ctx, tx, handle.Property(ctx), d)
		})})
	csvOr := func(path, summary, perm string, resp any, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, string, []string, [][]string, error)) {
		add(route.Route{Method: http.MethodGet, Path: path, Summary: summary + " (format=csv for Excel)", Permission: perm, Response: resp, List: true,
			Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "format", Enum: []string{"json", "csv"}}},
			Handler: func(w http.ResponseWriter, r *http.Request) {
				ctx := r.Context()
				var out any
				var name string
				var head []string
				var rows [][]string
				err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
					var err error
					out, name, head, rows, err = fn(ctx, tx, r)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				if r.URL.Query().Get("format") == "csv" {
					writeCSV(w, name, head, rows)
					return
				}
				httpx.JSON(w, http.StatusOK, out)
			}})
	}
	csvOr("/api/v1/stay/reports/tax", "Hotel tax report (PB1) per day and component", "stay.dashboard.view", TaxRow{},
		func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, string, []string, [][]string, error) {
			from, to, err := period(ctx, tx, r)
			if err != nil {
				return nil, "", nil, nil, err
			}
			list, err := m.taxReport(ctx, tx, handle.Property(ctx), from, to)
			rows := [][]string{}
			for _, x := range list {
				rows = append(rows, []string{x.Date, x.Component, x.Net, x.Service, x.Tax, x.Total})
			}
			return httpx.Page[TaxRow]{Items: list}, "pb1-" + from.Format("2006-01-02") + ".csv", []string{"Tanggal", "Komponen", "Net", "Service", "Pajak (PB1)", "Total"}, rows, err
		})
	csvOr("/api/v1/stay/reports/deposits", "Deposits held and city ledger of the stays", "stay.dashboard.view", DepositRow{},
		func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, string, []string, [][]string, error) {
			list, err := m.depositReport(ctx, tx, handle.Property(ctx))
			rows := [][]string{}
			for _, x := range list {
				rows = append(rows, []string{x.Kind, x.StayNo, x.Guest, x.Status, x.Number, x.Amount, x.Applied, deref(x.Company)})
			}
			return httpx.Page[DepositRow]{Items: list}, "deposit-city-ledger.csv", []string{"Jenis", "Reservasi", "Tamu", "Status", "No.", "Jumlah", "Diterapkan", "Perusahaan"}, rows, err
		})
	csvOr("/api/v1/stay/reports/settlement", "Settlement of the online payments of the stays (mock gateway)", "stay.dashboard.view", SettlementRow{},
		func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, string, []string, [][]string, error) {
			from, to, err := period(ctx, tx, r)
			if err != nil {
				return nil, "", nil, nil, err
			}
			list, err := m.settlementReport(ctx, tx, handle.Property(ctx), from, to)
			rows := [][]string{}
			for _, x := range list {
				rows = append(rows, []string{x.Created.Format(time.RFC3339), x.Number, x.StayNo, x.Method, x.Amount, x.Status, deref(x.Gateway)})
			}
			return httpx.Page[SettlementRow]{Items: list}, "settlement.csv", []string{"Dibuat", "Pembayaran", "Reservasi", "Metode", "Jumlah", "Status", "Gateway"}, rows, err
		})
	csvOr("/api/v1/stay/reports/reservations", "Reservations of a period (export)", "stay.stay.view", Stay{},
		func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, string, []string, [][]string, error) {
			from, to, err := period(ctx, tx, r)
			if err != nil {
				return nil, "", nil, nil, err
			}
			list, err := handle.List[Stay](tx.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.end_at > $2 AND s.start_at < $3
				ORDER BY s.start_at`, handle.Property(ctx), from, to))
			loc := calendar.Location(ctx, tx)
			rows := [][]string{}
			for _, s := range list {
				rows = append(rows, []string{s.StayNo, s.ReservationCode, deref(s.GroupNo), guestLabel(s), deref(s.TypeName), deref(s.UnitCode),
					s.Start.In(loc).Format("2006-01-02"), s.End.In(loc).Format("2006-01-02"), fmt.Sprint(s.Nights), deref(s.RatePlan), sourceLabelID(deref(s.BookingSource)),
					s.BookingStatus, s.PaymentStatus, s.TotalDue, s.Paid})
			}
			return httpx.Page[Stay]{Items: list}, "reservations.csv", []string{"Stay", "Reservasi", "Grup", "Tamu", "Tipe", "Bungalow", "Check-in", "Check-out",
				"Malam", "Rate", "Kanal", "Status", "Bayar", "Total", "Dibayar"}, rows, err
		})
}
