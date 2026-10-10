package sportclub

// Sport Club admin (docs/requirement-booking-sportclub-mgcc.md §6, §7):
// Overview KPIs (FR-71) and the reports (FR-79..82) on one definition of
// every KPI (§13.2: utilisation against the open hours less the blocked
// hours, revenue per day of play before tax, no-show rate on the lines
// already started), recurring bookings (FR-64, FR-75, FR-128), incidents
// (FR-107), the rate card shown like the brochure (FR-09, FR-78) and the
// membership prospects among frequent players (FR-89).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// ── KPIs and reports ─────────────────────────────────────────────────────────

// UtilRow is the utilisation of a sport or a court.
type UtilRow struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Facility     string `json:"facility,omitempty"`
	OpenHours    string `json:"openHours" doc:"Open hours less the blocked hours"`
	NormalHours  string `json:"normalHours" doc:"Normal opening hours (without the 24-hour demo) less the blocked hours"`
	BookedHours  string `json:"bookedHours"`
	Utilization  string `json:"utilization" doc:"Booked ÷ open hours, 0–100 %"`
	UtilNormal   string `json:"utilizationNormal" doc:"Booked within the normal hours ÷ normal hours, 0–100 %"`
	Revenue      string `json:"revenue" doc:"Rent and extras before tax, per day of play"`
	RevenuePerHr string `json:"revenuePerHour" doc:"Revenue per open court hour"`
	Bookings     int    `json:"bookings"`
	NoShows      int    `json:"noShows"`
}

// HeatCell is one weekday × hour of the utilisation heatmap.
type HeatCell struct {
	Weekday int    `json:"weekday" doc:"1 = Monday … 7 = Sunday"`
	Hour    int    `json:"hour"`
	Booked  int    `json:"booked"`
	Open    int    `json:"open"`
	Rate    string `json:"rate" doc:"0–100 %"`
}

// Breakdown is a count and amount per key.
type Breakdown struct {
	Key    string `json:"key"`
	Label  string `json:"label,omitempty"`
	Count  int    `json:"count"`
	Hours  string `json:"hours,omitempty"`
	Amount string `json:"amount"`
}

// TopCustomer is a frequent player.
type TopCustomer struct {
	CustomerID *uuid.UUID `json:"customerId" db:"customer_id"`
	Name       string     `json:"name" db:"name"`
	Phone      *string    `json:"phone" db:"phone"`
	Bookings   int        `json:"bookings" db:"bookings"`
	Hours      string     `json:"hours" db:"hours"`
	Spend      string     `json:"spend" db:"spend"`
	NoShows    int        `json:"noShows" db:"no_shows"`
	Member     bool       `json:"member" db:"member"`
	Favorite   *string    `json:"favorite" db:"favorite"`
	Prospect   bool       `json:"prospect" db:"prospect" doc:"Non-member who plays often: membership lead"`
}

// CourtReport is the Sport Club report of a period.
type CourtReportData struct {
	From          string         `json:"from"`
	To            string         `json:"to"`
	Bookings      int            `json:"bookings"`
	BookedHours   string         `json:"bookedHours"`
	Utilization   string         `json:"utilization"`
	UtilNormal    string         `json:"utilizationNormal"`
	Revenue       string         `json:"revenue"`
	ServiceFees   string         `json:"serviceFees"`
	Extras        string         `json:"extras"`
	PackageUse    string         `json:"packageUse" doc:"Court package value recognised by the hours played"`
	PackageSales  string         `json:"packageSales" doc:"Court packages sold (deferred until used)"`
	NoShows       int            `json:"noShows"`
	NoShowRate    string         `json:"noShowRate" doc:"No-show lines ÷ lines whose start has passed, 0–100 %"`
	AwaitingPay   int            `json:"awaitingPayment"`
	BySport       []UtilRow      `json:"bySport"`
	ByCourt       []UtilRow      `json:"byCourt"`
	Heatmap       []HeatCell     `json:"heatmap"`
	ByChannel     []Breakdown    `json:"byChannel"`
	ByMethod      []Breakdown    `json:"byMethod"`
	NoShowBySport []Breakdown    `json:"noShowBySport"`
	TopCustomers  []TopCustomer  `json:"topCustomers"`
	Upcoming      []CourtBooking `json:"upcoming"`
	Alerts        []BoardAlert   `json:"alerts"`
	OutsideNormal []CourtBooking `json:"outsideNormalHours" doc:"Bookings outside the normal hours (made during the 24-hour demo, FR-132)"`
}

func pct(a, b decimal.Decimal) string {
	if !b.IsPositive() {
		return "0"
	}
	return decimal.Min(a.Mul(decimal.NewFromInt(100)).Div(b), decimal.NewFromInt(100)).StringFixed(1)
}

type lineFact struct {
	LineID     uuid.UUID  `db:"line_id"`
	Code       string     `db:"code"`
	CourtID    uuid.UUID  `db:"court_id"`
	FacilityID uuid.UUID  `db:"facility_id"`
	Start      time.Time  `db:"start_at"`
	End        time.Time  `db:"end_at"`
	Status     string     `db:"line_status"`
	Channel    string     `db:"channel"`
	Customer   *uuid.UUID `db:"customer_id"`
	Revenue    string     `db:"revenue"`
}

// CourtReport computes the Sport Club report of [from, to] (local dates).
func (m *Module) CourtReport(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, facility *uuid.UUID) (CourtReportData, error) {
	loc := calendar.Location(ctx, q)
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	end := to.AddDate(0, 0, 1)
	out := CourtReportData{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), BySport: []UtilRow{}, ByCourt: []UtilRow{}, Heatmap: []HeatCell{},
		ByChannel: []Breakdown{}, ByMethod: []Breakdown{}, NoShowBySport: []Breakdown{}, TopCustomers: []TopCustomer{}, Upcoming: []CourtBooking{},
		Alerts: []BoardAlert{}, OutsideNormal: []CourtBooking{}}
	where := ` AND c.status = 'active' AND c.resource_id IS NOT NULL AND f.usage_mode = 'slot_booking'`
	args := []any{}
	if facility != nil {
		where += ` AND c.facility_id = $2`
		args = append(args, *facility)
	}
	courts, err := m.courts(ctx, q, property, where, args...)
	if err != nil {
		return out, err
	}
	rt, err := m.Res.Type(ctx, q, "sport_court")
	if err != nil {
		return out, err
	}
	// facts: booked lines (status other than cancelled / expired) with their revenue before tax
	facts, err := handle.List[lineFact](q.Query(ctx, `WITH b AS (
		  SELECT r.id, r.folio_id, (SELECT l.id FROM reservation.reservation_lines l WHERE l.reservation_id = r.id ORDER BY lower(l.period), l.line_no LIMIT 1) AS first_line
		  FROM reservation.reservations r WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking')
		SELECT l.id AS line_id, r.code, c.id AS court_id, c.facility_id, lower(l.period) AS start_at, upper(l.period) AS end_at, l.status AS line_status,
		  CASE WHEN r.recurring_group_id IS NOT NULL THEN 'recurring' WHEN r.attributes ->> 'via' = 'phone' THEN 'phone'
		       WHEN r.channel IN ('website', 'member_app') THEN r.channel ELSE 'walk_in' END AS channel, r.customer_id,
		  (coalesce((SELECT sum(x.net_amount) FROM billing.folio_lines x WHERE x.folio_id = r.folio_id AND x.voided_at IS NULL
		     AND coalesce(x.revenue_component, '') <> 'gateway_fee'
		     AND ((x.reference_type = 'reservation.line' AND x.reference_id = l.id) OR (coalesce(x.reference_type, '') <> 'reservation.line' AND l.id = b.first_line))), 0)
		   + CASE WHEN l.id = b.first_line THEN coalesce((r.attributes -> 'package' ->> 'value')::numeric, 0) ELSE 0 END)::text AS revenue
		FROM b JOIN reservation.reservations r ON r.id = b.id JOIN reservation.reservation_lines l ON l.reservation_id = r.id
		JOIN sportclub.courts c ON c.resource_id = l.resource_id
		WHERE l.period && tstzrange($2, $3, '[)') AND l.status IN ('held', 'confirmed', 'checked_in', 'completed', 'no_show')
		AND NOT coalesce((r.attributes ->> 'void')::boolean, false) AND r.status NOT IN ('expired', 'cancelled')`, property, from, end))
	if err != nil {
		return out, err
	}
	blocks, err := m.blocks(ctx, q, property, from, end)
	if err != nil {
		return out, err
	}
	now := clock.Now()
	type acc struct {
		open, normal, booked, bookedNormal, revenue decimal.Decimal
		bookings                                    map[string]bool
		noShows                                     int
	}
	byCourt := map[uuid.UUID]*acc{}
	bySport := map[uuid.UUID]*acc{}
	heat := map[[2]int]*HeatCell{}
	cell := func(t time.Time) *HeatCell {
		wd := int(t.Weekday())
		if wd == 0 {
			wd = 7
		}
		k := [2]int{wd, t.Hour()}
		if heat[k] == nil {
			heat[k] = &HeatCell{Weekday: wd, Hour: t.Hour()}
		}
		return heat[k]
	}
	type span struct{ s, e time.Time }
	hoursCache := map[string][2]span{}
	courtOpen := map[uuid.UUID][]span{}   // open spans per court
	courtNormal := map[uuid.UUID][]span{} // normal spans per court
	for _, c := range courts {
		byCourt[c.ID] = &acc{bookings: map[string]bool{}}
		if bySport[c.FacilityID] == nil {
			bySport[c.FacilityID] = &acc{bookings: map[string]bool{}}
		}
		res, err := m.Res.Resource(ctx, q, *c.ResourceID)
		if err != nil {
			return out, err
		}
		for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
			k := c.FacilityID.String() + d.Format(time.DateOnly)
			sp, ok := hoursCache[k]
			if !ok {
				o, cl, closed, err := m.Res.OpenHours(ctx, q, property, res, rt, d)
				if err != nil {
					return out, err
				}
				no, nc, err := m.Res.NormalHours(ctx, q, property, res, rt, d)
				if err != nil {
					return out, err
				}
				if closed {
					o, cl, no, nc = d, d, d, d
				}
				sp = [2]span{{o, cl}, {no, nc}}
				hoursCache[k] = sp
			}
			courtOpen[c.ID] = append(courtOpen[c.ID], sp[0])
			courtNormal[c.ID] = append(courtNormal[c.ID], sp[1])
		}
	}
	overlap := func(a, b span) time.Duration {
		s, e := a.s, a.e
		if b.s.After(s) {
			s = b.s
		}
		if b.e.Before(e) {
			e = b.e
		}
		if e.After(s) {
			return e.Sub(s)
		}
		return 0
	}
	hrs := func(d time.Duration) decimal.Decimal { return decimal.NewFromFloat(d.Hours()) }
	for _, c := range courts {
		a := byCourt[c.ID]
		var blocked, blockedNormal time.Duration
		for _, b := range blocks {
			if b.CourtID != c.ID {
				continue
			}
			for _, sp := range courtOpen[c.ID] {
				blocked += overlap(span{b.Start, b.End}, sp)
			}
			for _, sp := range courtNormal[c.ID] {
				blockedNormal += overlap(span{b.Start, b.End}, sp)
			}
		}
		var open, normal time.Duration
		for _, sp := range courtOpen[c.ID] {
			open += sp.e.Sub(sp.s)
			for t := sp.s; t.Before(sp.e); t = t.Add(time.Hour) {
				cell(t.In(loc)).Open++
			}
		}
		for _, sp := range courtNormal[c.ID] {
			normal += sp.e.Sub(sp.s)
		}
		a.open, a.normal = hrs(open-blocked), hrs(normal-blockedNormal)
	}
	courtByID := map[uuid.UUID]CourtInfo{}
	for _, c := range courts {
		courtByID[c.ID] = c
	}
	channels := map[string]*Breakdown{}
	started, noShows := 0, 0
	for _, f := range facts {
		c, ok := courtByID[f.CourtID]
		if !ok {
			continue
		}
		a, s := byCourt[c.ID], bySport[c.FacilityID]
		h := hrs(f.End.Sub(f.Start))
		rev, _ := decimal.NewFromString(f.Revenue)
		var inNormal time.Duration
		for _, sp := range courtNormal[c.ID] {
			inNormal += overlap(span{f.Start, f.End}, sp)
		}
		for _, x := range []*acc{a, s} {
			x.booked = x.booked.Add(h)
			x.bookedNormal = x.bookedNormal.Add(hrs(inNormal))
			x.revenue = x.revenue.Add(rev)
			x.bookings[f.Code] = true
			if f.Status == "no_show" {
				x.noShows++
			}
		}
		for t := f.Start; t.Before(f.End); t = t.Add(time.Hour) {
			cell(t.In(loc)).Booked++
		}
		if f.Start.Before(now) {
			started++
			if f.Status == "no_show" {
				noShows++
			}
		}
		ch := channels[f.Channel]
		if ch == nil {
			ch = &Breakdown{Key: f.Channel, Hours: "0", Amount: "0"}
			channels[f.Channel] = ch
		}
		ch.Hours = decimal.RequireFromString(ch.Hours).Add(h).String()
		ch.Amount = decimal.RequireFromString(ch.Amount).Add(rev).StringFixed(0)
	}
	codesPerChannel := map[string]map[string]bool{}
	for _, f := range facts {
		if codesPerChannel[f.Channel] == nil {
			codesPerChannel[f.Channel] = map[string]bool{}
		}
		codesPerChannel[f.Channel][f.Code] = true
	}
	for k, ch := range channels {
		ch.Count = len(codesPerChannel[k])
		out.ByChannel = append(out.ByChannel, *ch)
	}
	sort.Slice(out.ByChannel, func(i, j int) bool { return out.ByChannel[i].Count > out.ByChannel[j].Count })
	totOpen, totNormal, totBooked, totBookedNormal, totRev := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	allCodes := map[string]bool{}
	row := func(key, label, fac string, a *acc) UtilRow {
		perHr := "0"
		if a.open.IsPositive() {
			perHr = a.revenue.Div(a.open).StringFixed(0)
		}
		return UtilRow{Key: key, Label: label, Facility: fac, OpenHours: a.open.StringFixed(1), NormalHours: a.normal.StringFixed(1), BookedHours: a.booked.StringFixed(1),
			Utilization: pct(a.booked, a.open), UtilNormal: pct(a.bookedNormal, a.normal), Revenue: a.revenue.StringFixed(0), RevenuePerHr: perHr,
			Bookings: len(a.bookings), NoShows: a.noShows}
	}
	seenSport := map[uuid.UUID]bool{}
	for _, c := range courts {
		a := byCourt[c.ID]
		out.ByCourt = append(out.ByCourt, row(c.ID.String(), c.Name, c.FacilityName, a))
		if !seenSport[c.FacilityID] {
			seenSport[c.FacilityID] = true
			s := bySport[c.FacilityID]
			// the sport's open hours are the sum of its courts
			s.open, s.normal = decimal.Zero, decimal.Zero
			for _, x := range courts {
				if x.FacilityID == c.FacilityID {
					s.open, s.normal = s.open.Add(byCourt[x.ID].open), s.normal.Add(byCourt[x.ID].normal)
				}
			}
			out.BySport = append(out.BySport, row(c.FacilityID.String(), c.FacilityName, "", s))
			out.NoShowBySport = append(out.NoShowBySport, Breakdown{Key: c.FacilityID.String(), Label: c.FacilityName, Count: s.noShows, Amount: "0"})
			totOpen, totNormal = totOpen.Add(s.open), totNormal.Add(s.normal)
			totBooked, totBookedNormal, totRev = totBooked.Add(s.booked), totBookedNormal.Add(s.bookedNormal), totRev.Add(s.revenue)
			for k := range s.bookings {
				allCodes[k] = true
			}
		}
	}
	out.Bookings, out.BookedHours, out.Revenue = len(allCodes), totBooked.StringFixed(1), totRev.StringFixed(0)
	out.Utilization, out.UtilNormal = pct(totBooked, totOpen), pct(totBookedNormal, totNormal)
	out.NoShows, out.NoShowRate = noShows, pct(decimal.NewFromInt(int64(noShows)), decimal.NewFromInt(int64(started)))
	for _, c := range heat {
		c.Rate = pct(decimal.NewFromInt(int64(c.Booked)), decimal.NewFromInt(int64(c.Open)))
		out.Heatmap = append(out.Heatmap, *c)
	}
	sort.Slice(out.Heatmap, func(i, j int) bool {
		if out.Heatmap[i].Weekday != out.Heatmap[j].Weekday {
			return out.Heatmap[i].Weekday < out.Heatmap[j].Weekday
		}
		return out.Heatmap[i].Hour < out.Heatmap[j].Hour
	})
	// money of the period: service fees, extras, packages, payment methods
	var fees, extras, pkgSales string
	if err := q.QueryRow(ctx, `SELECT
		coalesce((SELECT sum(x.total) FROM billing.folio_lines x JOIN reservation.reservations r ON r.folio_id = x.folio_id
		  WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND x.voided_at IS NULL AND x.revenue_component = 'gateway_fee'
		  AND x.posted_at >= $2 AND x.posted_at < $3), 0)::text,
		coalesce((SELECT sum(x.net_amount) FROM billing.folio_lines x JOIN reservation.reservations r ON r.folio_id = x.folio_id
		  WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND x.voided_at IS NULL AND x.reference_type = 'sportclub.extra'
		  AND x.posted_at >= $2 AND x.posted_at < $3), 0)::text,
		coalesce((SELECT sum(v.price_paid) FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
		  WHERE v.property_id = $1 AND t.category = 'court_package' AND v.issued_at >= $2 AND v.issued_at < $3 AND v.status <> 'void'), 0)::text`,
		property, from, end).Scan(&fees, &extras, &pkgSales); err != nil {
		return out, err
	}
	out.ServiceFees, out.Extras, out.PackageSales = trimDec(fees), trimDec(extras), trimDec(pkgSales)
	var pkgUse string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum((r.attributes -> 'package' ->> 'value')::numeric), 0)::text FROM reservation.reservations r
		WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND r.attributes ? 'package' AND r.status NOT IN ('cancelled', 'expired')
		AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.period && tstzrange($2, $3, '[)'))`, property, from, end).Scan(&pkgUse); err != nil {
		return out, err
	}
	out.PackageUse = trimDec(pkgUse)
	type methodRow struct {
		Method string `db:"method_type"`
		N      int    `db:"n"`
		Amount string `db:"amount"`
	}
	ms, err := handle.List[methodRow](q.Query(ctx, `SELECT p.method_type, count(*)::int AS n, trim_scale(sum(p.amount - p.refunded_amount))::text AS amount
		FROM billing.payments p JOIN reservation.reservations r ON r.folio_id = p.folio_id
		WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND p.status IN ('completed', 'refunded') AND coalesce(p.paid_at, p.created_at) >= $2
		AND coalesce(p.paid_at, p.created_at) < $3 GROUP BY p.method_type ORDER BY 3 DESC`, property, from, end))
	if err != nil {
		return out, err
	}
	for _, x := range ms {
		out.ByMethod = append(out.ByMethod, Breakdown{Key: x.Method, Count: x.N, Amount: x.Amount})
	}
	pol, err := m.courtPolicy(ctx, q, property)
	if err != nil {
		return out, err
	}
	tops, err := handle.List[TopCustomer](q.Query(ctx, `SELECT r.customer_id, coalesce(c.name, r.guest_name, '—') AS name, c.phone,
		count(DISTINCT r.id)::int AS bookings, trim_scale(round(sum(extract(epoch FROM upper(l.period) - lower(l.period)) / 3600)::numeric, 1))::text AS hours,
		trim_scale(coalesce(sum(l.amount), 0))::text AS spend, count(*) FILTER (WHERE l.status = 'no_show')::int AS no_shows,
		EXISTS (SELECT 1 FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id WHERE mb.customer_id = r.customer_id AND ms.status = 'active') AS member,
		(SELECT f.name FROM reservation.reservation_lines l2 JOIN reservation.reservations r2 ON r2.id = l2.reservation_id
		   JOIN sportclub.courts c2 ON c2.resource_id = l2.resource_id JOIN sportclub.facilities f ON f.id = c2.facility_id
		   WHERE r2.customer_id = r.customer_id AND r2.source_type = 'sportclub.court_booking' GROUP BY f.name ORDER BY count(*) DESC LIMIT 1) AS favorite,
		false AS prospect
		FROM reservation.reservations r JOIN reservation.reservation_lines l ON l.reservation_id = r.id LEFT JOIN crm.customers c ON c.id = r.customer_id
		WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND r.status NOT IN ('expired', 'cancelled') AND l.period && tstzrange($2, $3, '[)')
		GROUP BY r.customer_id, coalesce(c.name, r.guest_name, '—'), c.phone ORDER BY 4 DESC, 6 DESC LIMIT 20`, property, from, end))
	if err != nil {
		return out, err
	}
	for i := range tops {
		tops[i].Prospect = !tops[i].Member && pol.ProspectVisits > 0 && tops[i].Bookings >= pol.ProspectVisits && tops[i].CustomerID != nil
	}
	out.TopCustomers = tops
	// operation of today: next bookings, unpaid soon, blocked courts, outside the normal hours
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	up, err := m.loadBookings(ctx, q, property, `AND r.status IN ('draft', 'confirmed', 'pending') AND EXISTS (SELECT 1 FROM reservation.reservation_lines l
		WHERE l.reservation_id = r.id AND lower(l.period) >= $2 AND lower(l.period) < $3) ORDER BY (SELECT min(lower(l.period)) FROM reservation.reservation_lines l
		WHERE l.reservation_id = r.id) LIMIT 10`, now, today.AddDate(0, 0, 2))
	if err != nil {
		return out, err
	}
	for _, b := range up {
		if b.State == StateExpired || b.Void {
			continue
		}
		out.Upcoming = append(out.Upcoming, b)
		if b.PayStatus != "paid" && b.PayStatus != "overpaid" && b.Start != nil && b.Start.Sub(now) < 3*time.Hour {
			bid := b.ID
			out.Alerts = append(out.Alerts, BoardAlert{Kind: "unpaid_soon", BookingID: &bid, Message: fmt.Sprintf("%s %s belum lunas, main %s", b.Code, b.Name,
				b.Start.In(loc).Format("15:04"))})
		}
	}
	todayBlocks, err := m.blocks(ctx, q, property, today, today.AddDate(0, 0, 1))
	if err != nil {
		return out, err
	}
	for _, b := range todayBlocks {
		cid := b.CourtID
		out.Alerts = append(out.Alerts, BoardAlert{Kind: "issue", CourtID: &cid, Message: fmt.Sprintf("%s diblokir %s–%s (%s)", courtByID[b.CourtID].Name,
			b.Start.In(loc).Format("15:04"), b.End.In(loc).Format("15:04"), strings.ReplaceAll(b.Reason, "_", " "))})
	}
	future, err := m.loadBookings(ctx, q, property, `AND r.status IN ('draft', 'confirmed', 'pending') AND EXISTS (SELECT 1 FROM reservation.reservation_lines l
		WHERE l.reservation_id = r.id AND lower(l.period) >= $2) ORDER BY r.created_at LIMIT 500`, now)
	if err != nil {
		return out, err
	}
	for _, b := range future {
		if b.Void || b.State == StateExpired {
			continue
		}
		for _, l := range b.Lines {
			if l.CourtID == nil {
				continue
			}
			c, ok := courtByID[*l.CourtID]
			if !ok {
				continue
			}
			res, err := m.Res.Resource(ctx, q, *c.ResourceID)
			if err != nil {
				return out, err
			}
			no, nc, err := m.Res.NormalHours(ctx, q, property, res, rt, l.Start.In(loc))
			if err != nil {
				return out, err
			}
			if l.Start.Before(no) || l.End.After(nc) {
				out.OutsideNormal = append(out.OutsideNormal, b)
				break
			}
		}
	}
	return out, nil
}

func trimDec(s string) string {
	d, _ := decimal.NewFromString(s)
	return d.StringFixed(0)
}

// ── recurring bookings (FR-64, FR-75, FR-128) ───────────────────────────────

// RecurringInput is a weekly booking of a community, school or company.
type RecurringInput struct {
	CourtID       uuid.UUID     `json:"courtId"`
	Weekdays      []int         `json:"weekdays" doc:"1 = Monday … 7 = Sunday"`
	StartTime     string        `json:"startTime" doc:"HH:MM"`
	Hours         int           `json:"hours"`
	StartDate     string        `json:"startDate"`
	EndDate       string        `json:"endDate"`
	CustomerID    *uuid.UUID    `json:"customerId,omitempty"`
	Guest         *GuestInput   `json:"guest,omitempty"`
	CorporateName string        `json:"corporateName,omitempty"`
	PaymentMode   string        `json:"paymentMode" enum:"prepaid,package,pay_per_visit,invoice"`
	PackageCode   string        `json:"packageCode,omitempty"`
	Notes         string        `json:"notes,omitempty"`
	SkipConflicts bool          `json:"skipConflicts,omitempty" doc:"Create the free dates and skip the clashing ones"`
	Payment       *PaymentInput `json:"payment,omitempty" doc:"Prepaid: pay all meetings now"`
}

// RecurringDate is one meeting of a recurring booking.
type RecurringDate struct {
	Date      string     `json:"date"`
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	Status    string     `json:"status" enum:"ok,conflict,created"`
	Reason    string     `json:"reason,omitempty"`
	Price     string     `json:"price,omitempty"`
	BookingID *uuid.UUID `json:"bookingId,omitempty"`
}

// RecurringPreview lists the meetings and the clashes before saving.
type RecurringPreview struct {
	Dates     []RecurringDate `json:"dates"`
	Count     int             `json:"count"`
	Conflicts int             `json:"conflicts"`
	Total     string          `json:"total"`
}

func (m *Module) recurringDates(ctx context.Context, tx pgx.Tx, property uuid.UUID, in RecurringInput) ([]RecurringDate, CourtInfo, error) {
	loc := calendar.Location(ctx, tx)
	c, err := m.court(ctx, tx, property, in.CourtID)
	if err != nil {
		return nil, c, err
	}
	sd, err := time.ParseInLocation(time.DateOnly, in.StartDate, loc)
	if err != nil {
		return nil, c, handle.Invalid("startDate", "invalid_date", "startDate must be YYYY-MM-DD")
	}
	ed, err := time.ParseInLocation(time.DateOnly, in.EndDate, loc)
	if err != nil || ed.Before(sd) {
		return nil, c, handle.Invalid("endDate", "invalid_date", "endDate must be on or after the start date")
	}
	if ed.Sub(sd) > 366*24*time.Hour {
		return nil, c, handle.Invalid("endDate", "too_long", "at most one year")
	}
	if len(in.Weekdays) == 0 {
		return nil, c, handle.Invalid("weekdays", "required", "choose the weekday(s)")
	}
	if in.Hours <= 0 {
		in.Hours = 1
	}
	var out []RecurringDate
	for d := sd; !d.After(ed); d = d.AddDate(0, 0, 1) {
		wd := int(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		if !containsInt(in.Weekdays, wd) {
			continue
		}
		s, err := parseHM(d, in.StartTime)
		if err != nil {
			return nil, c, err
		}
		out = append(out, RecurringDate{Date: d.Format(time.DateOnly), Start: s, End: s.Add(time.Duration(in.Hours) * time.Hour), Status: "ok"})
	}
	if len(out) == 0 {
		return nil, c, errs.Validation("no_dates", "no meeting falls in this period")
	}
	if len(out) > 120 {
		return nil, c, errs.Validation("too_many_dates", "at most 120 meetings")
	}
	return out, c, nil
}

// PreviewRecurring lists the meetings with their clashes and prices.
func (m *Module) PreviewRecurring(ctx context.Context, tx pgx.Tx, property uuid.UUID, in RecurringInput) (RecurringPreview, error) {
	out := RecurringPreview{Dates: []RecurringDate{}}
	dates, _, err := m.recurringDates(ctx, tx, property, in)
	if err != nil {
		return out, err
	}
	pol, err := m.courtPolicy(ctx, tx, property)
	if err != nil {
		return out, err
	}
	total := decimal.Zero
	for _, d := range dates {
		slots, err := m.normalizeLines(ctx, tx, property, []CourtLine{{CourtID: in.CourtID, Start: d.Start, End: d.End}}, pol, slotOptions{})
		if err != nil {
			d.Status, d.Reason = "conflict", errMessage(err)
			out.Conflicts++
			out.Dates = append(out.Dates, d)
			continue
		}
		taken, err := m.takenSlots(ctx, tx, slots)
		if err != nil {
			return out, err
		}
		if len(taken) > 0 {
			d.Status, d.Reason = "conflict", taken[0].Message
			out.Conflicts++
		} else if qt, err := m.quote(ctx, tx, property, quoteRequest{Slots: slots, Channel: "ops"}, pol); err == nil {
			d.Price = qt.Total
			total = total.Add(decimal.RequireFromString(qt.Total))
		} else {
			d.Status, d.Reason = "conflict", errMessage(err)
			out.Conflicts++
		}
		out.Dates = append(out.Dates, d)
	}
	out.Count, out.Total = len(out.Dates)-out.Conflicts, total.StringFixed(0)
	return out, nil
}

// Recurring is a recurring booking with its meetings.
type Recurring struct {
	ID            uuid.UUID      `json:"id" db:"id"`
	Code          string         `json:"code" db:"code"`
	GroupID       uuid.UUID      `json:"groupId" db:"group_id"`
	CourtID       uuid.UUID      `json:"courtId" db:"court_id"`
	CourtName     string         `json:"courtName" db:"court_name"`
	FacilityName  string         `json:"facilityName" db:"facility_name"`
	CustomerID    *uuid.UUID     `json:"customerId" db:"customer_id"`
	HolderName    string         `json:"holderName" db:"holder_name"`
	Phone         *string        `json:"phone" db:"phone"`
	CorporateName *string        `json:"corporateName" db:"corporate_name"`
	Weekdays      []int32        `json:"weekdays" db:"weekdays"`
	StartTime     string         `json:"startTime" db:"start_time"`
	Hours         int            `json:"hours" db:"hours"`
	StartDate     time.Time      `json:"startDate" db:"start_date"`
	EndDate       time.Time      `json:"endDate" db:"end_date"`
	PaymentMode   string         `json:"paymentMode" db:"payment_mode"`
	PackageCode   *string        `json:"packageCode" db:"package_code"`
	Status        string         `json:"status" db:"status" enum:"active,paused,stopped,ended"`
	PausedFrom    *time.Time     `json:"pausedFrom" db:"paused_from"`
	PausedUntil   *time.Time     `json:"pausedUntil" db:"paused_until"`
	Notes         *string        `json:"notes" db:"notes"`
	CreatedAt     time.Time      `json:"createdAt" db:"created_at"`
	Meetings      int            `json:"meetings" db:"meetings"`
	Played        int            `json:"played" db:"played"`
	Upcoming      int            `json:"upcoming" db:"upcoming"`
	Unpaid        string         `json:"unpaid" db:"unpaid" doc:"Open balance of the meetings"`
	Bookings      []CourtBooking `json:"bookings,omitempty" db:"-"`
}

const recurringSelect = `SELECT g.id, g.code, g.group_id, g.court_id, c.name AS court_name, f.name AS facility_name, g.customer_id, g.holder_name, g.phone,
	g.corporate_name, g.weekdays, to_char(g.start_time, 'HH24:MI') AS start_time, g.hours, g.start_date, g.end_date, g.payment_mode, g.package_code, g.status,
	g.paused_from, g.paused_until, g.notes, g.created_at,
	(SELECT count(*) FROM reservation.reservations r WHERE r.recurring_group_id = g.group_id AND r.status <> 'cancelled')::int AS meetings,
	(SELECT count(*) FROM reservation.reservations r WHERE r.recurring_group_id = g.group_id AND r.status IN ('completed', 'checked_in'))::int AS played,
	(SELECT count(*) FROM reservation.reservations r WHERE r.recurring_group_id = g.group_id AND r.status = 'confirmed'
	  AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND lower(l.period) > now()))::int AS upcoming,
	trim_scale(coalesce((SELECT sum(fl.total) FROM billing.folio_lines fl JOIN reservation.reservations r ON r.folio_id = fl.folio_id
	  WHERE r.recurring_group_id = g.group_id AND r.status <> 'cancelled' AND fl.voided_at IS NULL), 0)
	- coalesce((SELECT sum(p.amount - p.refunded_amount) FROM billing.payments p JOIN reservation.reservations r ON r.folio_id = p.folio_id
	  WHERE r.recurring_group_id = g.group_id AND r.status <> 'cancelled' AND p.status IN ('completed', 'refunded') AND p.purpose = 'settlement'), 0))::text AS unpaid
	FROM sportclub.recurring_bookings g JOIN sportclub.courts c ON c.id = g.court_id JOIN sportclub.facilities f ON f.id = c.facility_id`

func (m *Module) recurring(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]Recurring, error) {
	return handle.List[Recurring](q.Query(ctx, recurringSelect+where, args...))
}

// CreateRecurring books every free meeting (one booking per meeting) and
// stores the recurring booking; clashes are refused unless skipped.
func (m *Module) CreateRecurring(ctx context.Context, tx pgx.Tx, property uuid.UUID, in RecurringInput, key string) (Recurring, error) {
	if in.PaymentMode == "" {
		in.PaymentMode = "pay_per_visit"
	}
	if in.PaymentMode == "package" && strings.TrimSpace(in.PackageCode) == "" {
		return Recurring{}, handle.Invalid("packageCode", "required", "the package code is required")
	}
	pre, err := m.PreviewRecurring(ctx, tx, property, in)
	if err != nil {
		return Recurring{}, err
	}
	if pre.Conflicts > 0 && !in.SkipConflicts {
		return Recurring{}, errs.Conflict("recurring_conflicts", fmt.Sprintf("%d meeting(s) clash; skip them or change the time", pre.Conflicts))
	}
	cid, name, err := resolveCustomer(ctx, tx, property, in.CustomerID, in.Guest)
	if err != nil {
		return Recurring{}, err
	}
	if name == "" {
		name = in.CorporateName
	}
	if name == "" {
		return Recurring{}, handle.Invalid("guest", "required", "the customer, a name or the company is required")
	}
	phone := ""
	if in.Guest != nil {
		phone = in.Guest.Phone
	}
	gid, rid := uuid.New(), id.New()
	no, err := numbering.Next(ctx, tx, property, "RUT", clock.Now().In(calendar.Location(ctx, tx)))
	if err != nil {
		return Recurring{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.recurring_bookings (id, property_id, code, group_id, court_id, customer_id, holder_name, phone, corporate_name,
		weekdays, start_time, hours, start_date, end_date, payment_mode, package_code, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::time,$12,$13::date,$14::date,$15,$16,$17,$18)`,
		rid, property, no, gid, in.CourtID, cid, name, nzs(phone), nzs(in.CorporateName), in.Weekdays, in.StartTime, max(in.Hours, 1), in.StartDate, in.EndDate,
		in.PaymentMode, nzs(strings.ToUpper(in.PackageCode)), nzs(in.Notes), actor(ctx)); err != nil {
		return Recurring{}, err
	}
	if err := m.bookMeetings(ctx, tx, property, rid, gid, cid, name, phone, in, pre.Dates, key); err != nil {
		return Recurring{}, err
	}
	list, err := m.recurring(ctx, tx, ` WHERE g.id = $1`, rid)
	if err != nil || len(list) == 0 {
		return Recurring{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionCreate, EntityType: "sportclub.recurring_booking",
		EntityID: rid.String(), EntityLabel: no + " · " + name, PropertyID: &property, After: list[0]})
}

func (m *Module) bookMeetings(ctx context.Context, tx pgx.Tx, property, rid, gid uuid.UUID, cid *uuid.UUID, name, phone string, in RecurringInput,
	dates []RecurringDate, key string) error {
	for i, d := range dates {
		if d.Status != "ok" {
			continue
		}
		bi := CourtBookingInput{Lines: []CourtLine{{CourtID: in.CourtID, Start: d.Start, End: d.End}}, CustomerID: cid, CorporateName: in.CorporateName,
			Channel: "back_office", Notes: in.Notes, RecurringGroupID: &gid}
		if cid == nil {
			bi.Guest = &GuestInput{Name: name, Phone: phone}
		}
		switch in.PaymentMode {
		case "package":
			bi.PackageCode = in.PackageCode
		case "prepaid":
			bi.Payment = in.Payment
		}
		k := ""
		if key != "" {
			k = fmt.Sprintf("%s-%d", key, i)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := m.BookCourt(ctx, sp, property, bi, k); err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal && in.SkipConflicts {
				continue
			}
			return fmt.Errorf("%s: %w", d.Date, err)
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE sportclub.recurring_bookings SET updated_by = $2 WHERE id = $1`, rid, actor(ctx))
	return err
}

// RecurringAction pauses, resumes, stops or extends a recurring booking.
type RecurringAction struct {
	From    string `json:"from,omitempty" doc:"Pause: first date"`
	Until   string `json:"until,omitempty" doc:"Pause: last date"`
	EndDate string `json:"endDate,omitempty" doc:"Extend: new last date"`
	Reason  string `json:"reason,omitempty"`
}

// ActRecurring changes a recurring booking: pause / stop void the future
// meetings not paid yet (paid meetings stay, no refund); extend books the
// new dates.
func (m *Module) ActRecurring(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, action string, in RecurringAction, key string) (Recurring, error) {
	list, err := m.recurring(ctx, tx, ` WHERE g.id = $1 AND g.property_id = $2`, rid, property)
	if err != nil {
		return Recurring{}, err
	}
	if len(list) == 0 {
		return Recurring{}, errNotFound("recurring booking")
	}
	g := list[0]
	loc := calendar.Location(ctx, tx)
	voidFuture := func(from, until *time.Time, reason string) error {
		bs, err := m.loadBookings(ctx, tx, property, `AND r.recurring_group_id = $2 AND r.status IN ('confirmed', 'pending')`, g.GroupID)
		if err != nil {
			return err
		}
		now := clock.Now()
		for _, b := range bs {
			if b.Start == nil || !b.Start.After(now) || b.PayStatus == "paid" || b.PackageCode != "" {
				continue
			}
			if from != nil && b.Start.Before(*from) {
				continue
			}
			if until != nil && !b.Start.Before(until.AddDate(0, 0, 1)) {
				continue
			}
			if _, err := m.VoidBooking(ctx, tx, property, b.ID, reason); err != nil {
				return err
			}
		}
		return nil
	}
	switch action {
	case "pause":
		f, err1 := time.ParseInLocation(time.DateOnly, in.From, loc)
		u, err2 := time.ParseInLocation(time.DateOnly, in.Until, loc)
		if err1 != nil || err2 != nil || u.Before(f) {
			return g, handle.Invalid("until", "invalid_date", "from and until must be dates, until on or after from")
		}
		if err := voidFuture(&f, &u, "recurring paused: "+in.Reason); err != nil {
			return g, err
		}
		if _, err := tx.Exec(ctx, `UPDATE sportclub.recurring_bookings SET status = 'paused', paused_from = $2, paused_until = $3, updated_by = $4 WHERE id = $1`,
			rid, f, u, actor(ctx)); err != nil {
			return g, err
		}
	case "resume":
		if g.Status != "paused" || g.PausedFrom == nil || g.PausedUntil == nil {
			return g, errs.Conflict("not_paused", "the recurring booking is not paused")
		}
		in2 := m.recurringInput(g)
		in2.StartDate, in2.EndDate, in2.SkipConflicts = maxDate(*g.PausedFrom, clock.Now().In(loc)).Format(time.DateOnly), g.PausedUntil.Format(time.DateOnly), true
		if !g.PausedUntil.Before(time.Date(clock.Now().In(loc).Year(), clock.Now().In(loc).Month(), clock.Now().In(loc).Day(), 0, 0, 0, 0, loc)) {
			pre, err := m.PreviewRecurring(ctx, tx, property, in2)
			if err == nil {
				if err := m.bookMeetings(ctx, tx, property, g.ID, g.GroupID, g.CustomerID, g.HolderName, deref(g.Phone), in2, pre.Dates, key); err != nil {
					return g, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sportclub.recurring_bookings SET status = 'active', paused_from = NULL, paused_until = NULL, updated_by = $2 WHERE id = $1`,
			rid, actor(ctx)); err != nil {
			return g, err
		}
	case "stop":
		if err := voidFuture(nil, nil, "recurring stopped: "+in.Reason); err != nil {
			return g, err
		}
		if _, err := tx.Exec(ctx, `UPDATE sportclub.recurring_bookings SET status = 'stopped', updated_by = $2 WHERE id = $1`, rid, actor(ctx)); err != nil {
			return g, err
		}
	case "extend":
		ne, err := time.ParseInLocation(time.DateOnly, in.EndDate, loc)
		if err != nil || !ne.After(g.EndDate) {
			return g, handle.Invalid("endDate", "invalid_date", "the new end date must be after "+g.EndDate.Format(time.DateOnly))
		}
		in2 := m.recurringInput(g)
		in2.StartDate, in2.EndDate, in2.SkipConflicts = g.EndDate.AddDate(0, 0, 1).Format(time.DateOnly), in.EndDate, true
		pre, err := m.PreviewRecurring(ctx, tx, property, in2)
		if err != nil {
			return g, err
		}
		if err := m.bookMeetings(ctx, tx, property, g.ID, g.GroupID, g.CustomerID, g.HolderName, deref(g.Phone), in2, pre.Dates, key); err != nil {
			return g, err
		}
		if _, err := tx.Exec(ctx, `UPDATE sportclub.recurring_bookings SET end_date = $2, status = CASE WHEN status = 'ended' THEN 'active' ELSE status END,
			updated_by = $3 WHERE id = $1`, rid, ne, actor(ctx)); err != nil {
			return g, err
		}
	default:
		return g, errs.BadRequest("invalid_action", "unknown action")
	}
	list, err = m.recurring(ctx, tx, ` WHERE g.id = $1`, rid)
	if err != nil || len(list) == 0 {
		return g, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: "recurring_" + action, EntityType: "sportclub.recurring_booking",
		EntityID: rid.String(), EntityLabel: g.Code, PropertyID: &property, Before: g, After: list[0], Reason: in.Reason})
}

func maxDate(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (m *Module) recurringInput(g Recurring) RecurringInput {
	wd := make([]int, 0, len(g.Weekdays))
	for _, w := range g.Weekdays {
		wd = append(wd, int(w))
	}
	return RecurringInput{CourtID: g.CourtID, Weekdays: wd, StartTime: g.StartTime, Hours: g.Hours, CustomerID: g.CustomerID,
		CorporateName: deref(g.CorporateName), PaymentMode: g.PaymentMode, PackageCode: deref(g.PackageCode), Notes: deref(g.Notes)}
}

// ── incidents (FR-107) ───────────────────────────────────────────────────────

// Incident is a Sport Club incident.
type Incident struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	Number        string     `json:"number" db:"number"`
	Category      string     `json:"category" db:"category" enum:"injury,damage,complaint,lost_item,other"`
	Severity      string     `json:"severity" db:"severity" enum:"low,medium,high,critical"`
	Status        string     `json:"status" db:"status" enum:"open,closed"`
	FacilityID    *uuid.UUID `json:"facilityId" db:"facility_id"`
	CourtID       *uuid.UUID `json:"courtId" db:"court_id"`
	Place         *string    `json:"place" db:"place"`
	ReservationID *uuid.UUID `json:"reservationId" db:"reservation_id"`
	BookingCode   *string    `json:"bookingCode" db:"booking_code"`
	CustomerID    *uuid.UUID `json:"customerId" db:"customer_id"`
	PersonName    *string    `json:"personName" db:"person_name"`
	Description   string     `json:"description" db:"description"`
	ActionTaken   *string    `json:"actionTaken" db:"action_taken"`
	DamageAmount  *string    `json:"damageAmount" db:"damage_amount"`
	OccurredAt    time.Time  `json:"occurredAt" db:"occurred_at"`
	ClosedAt      *time.Time `json:"closedAt" db:"closed_at"`
	ReportedBy    *string    `json:"reportedBy" db:"reported_by"`
}

const incidentSelect = `SELECT i.id, i.number, i.category, i.severity, i.status, i.facility_id, i.court_id, coalesce(c.name, f.name) AS place, i.reservation_id,
	r.code AS booking_code, i.customer_id, i.person_name, i.description, i.action_taken, trim_scale(i.damage_amount)::text AS damage_amount, i.occurred_at,
	i.closed_at, u.full_name AS reported_by
	FROM sportclub.incidents i LEFT JOIN sportclub.courts c ON c.id = i.court_id LEFT JOIN sportclub.facilities f ON f.id = coalesce(i.facility_id, c.facility_id)
	LEFT JOIN reservation.reservations r ON r.id = i.reservation_id LEFT JOIN platform.users u ON u.id = i.created_by`

// IncidentInput records an incident.
type IncidentInput struct {
	Category      string     `json:"category" enum:"injury,damage,complaint,lost_item,other"`
	Severity      string     `json:"severity,omitempty" enum:"low,medium,high,critical"`
	FacilityID    *uuid.UUID `json:"facilityId,omitempty"`
	CourtID       *uuid.UUID `json:"courtId,omitempty"`
	ReservationID *uuid.UUID `json:"reservationId,omitempty"`
	PersonName    string     `json:"personName,omitempty"`
	Description   string     `json:"description"`
	ActionTaken   string     `json:"actionTaken,omitempty"`
	DamageAmount  string     `json:"damageAmount,omitempty"`
}

// CreateIncident records an incident from the front desk.
func (m *Module) CreateIncident(ctx context.Context, tx pgx.Tx, property uuid.UUID, in IncidentInput) (Incident, error) {
	if strings.TrimSpace(in.Description) == "" {
		return Incident{}, handle.Invalid("description", "required", "describe what happened")
	}
	if in.Severity == "" {
		in.Severity = "medium"
	}
	var dmg *string
	if in.DamageAmount != "" {
		d, err := handle.Decimal("damageAmount", in.DamageAmount, decimal.Zero)
		if err != nil {
			return Incident{}, err
		}
		s := d.String()
		dmg = &s
	}
	var cust *uuid.UUID
	if in.ReservationID != nil {
		_ = tx.QueryRow(ctx, `SELECT customer_id FROM reservation.reservations WHERE id = $1 AND property_id = $2`, *in.ReservationID, property).Scan(&cust)
	}
	no, err := numbering.Next(ctx, tx, property, "INC", clock.Now().In(calendar.Location(ctx, tx)))
	if err != nil {
		return Incident{}, err
	}
	iid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.incidents (id, property_id, number, category, severity, facility_id, court_id, reservation_id, customer_id,
		person_name, description, action_taken, damage_amount, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::numeric,$14)`,
		iid, property, "SC-"+no, in.Category, in.Severity, in.FacilityID, in.CourtID, in.ReservationID, cust, nzs(in.PersonName), in.Description,
		nzs(in.ActionTaken), dmg, actor(ctx)); err != nil {
		return Incident{}, err
	}
	rows, err := tx.Query(ctx, incidentSelect+` WHERE i.id = $1`, iid)
	inc, err := handle.One[Incident](rows, err, "incident")
	if err != nil {
		return inc, err
	}
	return inc, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionCreate, EntityType: "sportclub.incident", EntityID: iid.String(),
		EntityLabel: inc.Number, PropertyID: &property, After: inc})
}

// CloseIncident closes an incident with the action taken.
func (m *Module) CloseIncident(ctx context.Context, tx pgx.Tx, property, iid uuid.UUID, action string) (Incident, error) {
	if strings.TrimSpace(action) == "" {
		return Incident{}, handle.Invalid("reason", "required", "the action taken is required")
	}
	tag, err := tx.Exec(ctx, `UPDATE sportclub.incidents SET status = 'closed', closed_at = now(), action_taken = $3, updated_by = $4
		WHERE id = $1 AND property_id = $2 AND status = 'open'`, iid, property, action, actor(ctx))
	if err != nil {
		return Incident{}, err
	}
	if tag.RowsAffected() == 0 {
		return Incident{}, errs.Conflict("not_open", "the incident is not open")
	}
	rows, err := tx.Query(ctx, incidentSelect+` WHERE i.id = $1`, iid)
	return handle.One[Incident](rows, err, "incident")
}

// ── rate card (FR-09) ────────────────────────────────────────────────────────

// RateRow is one rate of the brochure.
type RateRow struct {
	Item     string  `json:"item" db:"item"`
	Name     string  `json:"name" db:"name"`
	DayType  *string `json:"dayType" db:"day_type"`
	DayOrder int     `json:"-" db:"day_order"`
	Band     *string `json:"band" db:"band"`
	From     *string `json:"from" db:"band_from"`
	To       *string `json:"to" db:"band_to"`
	Price    string  `json:"price" db:"price" doc:"Per hour, before tax"`
	MinHours int     `json:"minHours" db:"min_quantity" doc:"Price per hour from this many hours (Basket Indoor 2 hours)"`
}

// PackageRow is a court package of the brochure.
type PackageRow struct {
	Code  string   `json:"code" db:"code"`
	Name  string   `json:"name" db:"name"`
	Uses  string   `json:"uses" db:"uses"`
	Price string   `json:"price" db:"price"`
	Items []string `json:"items" db:"items"`
}

// RateCard is the rate card of the sports.
type RateCard struct {
	Rates    []RateRow    `json:"rates"`
	Packages []PackageRow `json:"packages"`
}

func (m *Module) rateCard(ctx context.Context, q dbtx.Querier, property uuid.UUID) (RateCard, error) {
	out := RateCard{}
	var err error
	if out.Rates, err = handle.List[RateRow](q.Query(ctx, `SELECT DISTINCT ON (r.code) r.item_ref AS item, r.name, dt.name AS day_type,
		coalesce(dt.priority, 0) * 10 + coalesce(substr(dt.weekdays, 1, 1)::int, 9) AS day_order, tb.name AS band,
		left(tb.start_time::text, 5) AS band_from, left(tb.end_time::text, 5) AS band_to, trim_scale(r.price)::text AS price, r.min_quantity
		FROM commercial.pricing_rules r LEFT JOIN commercial.line_day_types dt ON dt.id = r.line_day_type_id LEFT JOIN commercial.time_bands tb ON tb.id = r.time_band_id
		WHERE r.property_id = $1 AND r.service_type = 'sport_court' AND r.status = 'active' AND r.effective_from <= billing.local_date(r.property_id)
		AND (r.effective_to IS NULL OR r.effective_to >= billing.local_date(r.property_id)) ORDER BY r.code, r.version DESC`, property)); err != nil {
		return out, err
	}
	sort.SliceStable(out.Rates, func(i, j int) bool {
		a, b := out.Rates[i], out.Rates[j]
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		if a.DayOrder != b.DayOrder {
			return a.DayOrder < b.DayOrder
		}
		return deref(a.From) < deref(b.From)
	})
	out.Packages, err = handle.List[PackageRow](q.Query(ctx, `SELECT code, name, trim_scale(face_value)::text AS uses, trim_scale(price)::text AS price,
		applicable_items AS items FROM commercial.voucher_types WHERE property_id = $1 AND category = 'court_package' AND status = 'active' ORDER BY code`, property))
	return out, err
}

// ── membership prospects (FR-89) ────────────────────────────────────────────

// FlagProspects marks non-members with at least ProspectVisits court
// bookings in 30 days and hands each one once to the lead hook (Sales).
func (m *Module) FlagProspects(ctx context.Context) (int, error) {
	n := 0
	err := m.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		props, err := collect(tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active'`))
		if err != nil {
			return err
		}
		for _, p := range props {
			pol, err := m.courtPolicy(ctx, tx, p)
			if err != nil || pol.ProspectVisits <= 0 {
				continue
			}
			type cand struct {
				CustomerID uuid.UUID `db:"customer_id"`
				Name       string    `db:"name"`
				Phone      *string   `db:"phone"`
				Email      *string   `db:"email"`
				Bookings   int       `db:"bookings"`
				Favorite   *string   `db:"favorite"`
			}
			list, err := handle.List[cand](tx.Query(ctx, `SELECT r.customer_id, c.name, c.phone, c.email, count(DISTINCT r.id)::int AS bookings,
				(SELECT f.name FROM reservation.reservation_lines l2 JOIN reservation.reservations r2 ON r2.id = l2.reservation_id
				  JOIN sportclub.courts c2 ON c2.resource_id = l2.resource_id JOIN sportclub.facilities f ON f.id = c2.facility_id
				  WHERE r2.customer_id = r.customer_id AND r2.source_type = 'sportclub.court_booking' GROUP BY f.name ORDER BY count(*) DESC LIMIT 1) AS favorite
				FROM reservation.reservations r JOIN crm.customers c ON c.id = r.customer_id
				WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND r.status IN ('confirmed', 'checked_in', 'completed')
				AND r.created_at > now() - interval '30 days'
				AND NOT EXISTS (SELECT 1 FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id WHERE mb.customer_id = r.customer_id AND ms.status = 'active')
				AND NOT EXISTS (SELECT 1 FROM sportclub.membership_prospects x WHERE x.customer_id = r.customer_id)
				GROUP BY r.customer_id, c.name, c.phone, c.email HAVING count(DISTINCT r.id) >= $2`, p, pol.ProspectVisits))
			if err != nil {
				return err
			}
			for _, c := range list {
				ref := ""
				if m.Leads != nil {
					note := fmt.Sprintf("Bermain %d kali dalam 30 hari (favorit: %s). Prospek Membership Sport Club.", c.Bookings, deref(c.Favorite))
					if ref, err = m.Leads(ctx, tx, p, c.CustomerID, c.Name, deref(c.Phone), deref(c.Email), note); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(ctx, `INSERT INTO sportclub.membership_prospects (customer_id, property_id, bookings, lead_ref) VALUES ($1,$2,$3,$4)
					ON CONFLICT DO NOTHING`, c.CustomerID, p, c.Bookings, nzs(ref)); err != nil {
					return err
				}
				n++
			}
		}
		return nil
	})
	return n, err
}
