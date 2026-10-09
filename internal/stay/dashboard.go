package stay

// Accommodation dashboard and reports (requirements §3, §32). A night D is
// counted at 18:00 local: a bungalow is occupied on night D when a stay
// (reserved, in-house or checked out) covers that instant, out of order
// when a Maintenance / Out of Order block covers it. Room revenue of a
// night is the room net of the stay divided by its nights (accommodation
// quote, else the pricing snapshot of the reservation line).
//
//	Occupancy  = occupied room nights / available room nights × 100
//	ADR        = room revenue / rooms sold
//	RevPAR     = room revenue / available room nights
//	ALOS       = total room nights / number of reservations

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// roomNetSQL is the room net of a stay s (before tax and service).
const roomNetSQL = `coalesce((s.room_quote->>'net')::numeric, (SELECT ps.net_amount FROM reservation.reservation_lines l
	JOIN commercial.pricing_snapshots ps ON ps.id = l.pricing_snapshot_id WHERE l.reservation_id = s.reservation_id ORDER BY l.line_no LIMIT 1), 0)
	/ greatest(1, round(extract(epoch FROM s.end_at - s.start_at) / 86400))`

// NightStat is the inventory and revenue of one night.
type NightStat struct {
	Date        string `json:"date"`
	Units       int    `json:"units"`
	OutOfOrder  int    `json:"outOfOrder"`
	Available   int    `json:"available" doc:"Available room nights: units − out of order"`
	Occupied    int    `json:"occupied"`
	RoomRevenue string `json:"roomRevenue"`
	Occupancy   string `json:"occupancy"`
	ADR         string `json:"adr"`
	RevPAR      string `json:"revpar"`
}

// nightStats computes the nights [from, from+days).
func (m *Module) nightStats(ctx context.Context, q dbtx.Querier, property uuid.UUID, from time.Time, days int) ([]NightStat, error) {
	loc := calendar.Location(ctx, q)
	out := make([]NightStat, 0, days)
	for i := 0; i < days; i++ {
		d := time.Date(from.Year(), from.Month(), from.Day()+i, 0, 0, 0, 0, loc)
		at := d.Add(18 * time.Hour)
		var n NightStat
		var rev string
		if err := q.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM stay.bungalows b WHERE b.property_id = $1 AND b.status = 'active' AND b.archived_at IS NULL AND b.created_at <= $2)::int,
			(SELECT count(DISTINCT k.bungalow_id) FROM stay.room_blocks k WHERE k.property_id = $1 AND k.kind IN ('maintenance', 'out_of_order')
			  AND k.start_at <= $2 AND k.end_at > $2 AND (k.status = 'active' OR k.released_at > $2))::int,
			(SELECT count(*) FROM stay.stays s WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status IN ('reserved', 'checked_in', 'checked_out')
			  AND s.start_at <= $2 AND coalesce(s.actual_end_at, s.end_at) > $2)::int,
			(SELECT coalesce(sum(`+roomNetSQL+`), 0) FROM stay.stays s WHERE s.property_id = $1 AND s.kind = 'bungalow'
			  AND s.status IN ('reserved', 'checked_in', 'checked_out') AND s.start_at <= $2 AND coalesce(s.actual_end_at, s.end_at) > $2)::text`,
			property, at).Scan(&n.Units, &n.OutOfOrder, &n.Occupied, &rev); err != nil {
			return nil, err
		}
		n.Date = d.Format(time.DateOnly)
		n.Available = max(n.Units-n.OutOfOrder, 0)
		r := decOf(rev).Round(0)
		n.RoomRevenue = r.String()
		n.Occupancy, n.ADR, n.RevPAR = kpis(r, n.Occupied, n.Available)
		out = append(out, n)
	}
	return out, nil
}

func kpis(revenue decimal.Decimal, sold, available int) (occ, adr, revpar string) {
	occ, adr, revpar = "0", "0", "0"
	if available > 0 {
		occ = decimal.NewFromInt(int64(sold)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(available))).Round(1).String()
		revpar = revenue.Div(decimal.NewFromInt(int64(available))).Round(0).String()
	}
	if sold > 0 {
		adr = revenue.Div(decimal.NewFromInt(int64(sold))).Round(0).String()
	}
	return
}

// Dashboard is the accommodation overview of a day (§3).
type Dashboard struct {
	Date  string    `json:"date"`
	Night NightStat `json:"night"`
	// KPI
	Occupancy          string         `json:"occupancy"`
	AvailableBungalows int            `json:"availableBungalows"`
	OccupiedBungalows  int            `json:"occupiedBungalows"`
	TodaysArrivals     int            `json:"todaysArrivals"`
	TodaysDepartures   int            `json:"todaysDepartures"`
	InHouseGuests      int            `json:"inHouseGuests"`
	Revenue            string         `json:"revenue" doc:"Room revenue of the night plus the other charges posted on stay folios today"`
	ADR                string         `json:"adr"`
	RevPAR             string         `json:"revpar"`
	Cancelled          int            `json:"cancelled" doc:"Reservations cancelled today"`
	NoShows            int            `json:"noShows"`
	RoomStatus         map[string]int `json:"roomStatus" doc:"Bungalows per operational status (ready, dirty, cleaning, maintenance …)"`
	// Today's operation
	Arrivals           []Stay         `json:"arrivals"`
	Departures         []Stay         `json:"departures"`
	ExpectedCheckIns   int            `json:"expectedCheckIns"`
	ExpectedCheckOuts  int            `json:"expectedCheckOuts"`
	PendingPayment     []Stay         `json:"pendingPayment"`
	PendingPreparation []Stay         `json:"pendingPreparation"`
	VIP                []Stay         `json:"vip"`
	Requests           []GuestRequest `json:"requests"`
	HousekeepingTasks  []HKTask       `json:"housekeepingTasks"`
	MaintenanceIssues  []WorkOrder    `json:"maintenanceIssues"`
	Week               []NightStat    `json:"week" doc:"The next seven nights"`
}

// DashboardFor builds the overview of a local day.
func (m *Module) DashboardFor(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (Dashboard, error) {
	loc := calendar.Location(ctx, q)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	out := Dashboard{Date: from.Format(time.DateOnly), RoomStatus: map[string]int{}, PendingPayment: []Stay{}, PendingPreparation: []Stay{}, VIP: []Stay{}}
	fo, err := m.FrontOfficeDay(ctx, q, property, from)
	if err != nil {
		return out, err
	}
	out.Arrivals, out.Departures = fo.Arrivals, fo.Departures
	out.TodaysArrivals, out.TodaysDepartures, out.InHouseGuests = len(fo.Arrivals), len(fo.Departures), fo.Counts["inHouseGuests"]
	out.ExpectedCheckIns, out.ExpectedCheckOuts = len(fo.Arrivals), len(fo.Departures)
	out.RoomStatus = fo.Rooms
	week, err := m.nightStats(ctx, q, property, from, 7)
	if err != nil {
		return out, err
	}
	out.Week, out.Night = week, week[0]
	out.Occupancy, out.ADR, out.RevPAR = out.Night.Occupancy, out.Night.ADR, out.Night.RevPAR
	out.OccupiedBungalows = len(fo.InHouse)
	out.AvailableBungalows = fo.Rooms["ready"] + fo.Rooms["dirty"] + fo.Rooms["cleaning"] + fo.Rooms["cleaned"] + fo.Rooms["inspected"] - out.OccupiedBungalows
	if out.AvailableBungalows < 0 {
		out.AvailableBungalows = 0
	}
	var other string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(l.net_amount), 0)::text FROM reporting.eng_folio_lines l JOIN stay.stays s ON s.folio_id = l.folio_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND l.posted_at >= $2 AND l.posted_at < $3 AND coalesce(l.reference_type, '') <> 'reservation.line'
		AND l.revenue_component NOT IN ('bungalow')`, property, from, to).Scan(&other); err != nil {
		return out, err
	}
	out.Revenue = decOf(out.Night.RoomRevenue).Add(decOf(other)).Round(0).String()
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'cancelled' AND cancelled_at >= $2 AND cancelled_at < $3)::int,
		count(*) FILTER (WHERE status = 'no_show' AND start_at >= $2 - interval '1 day' AND start_at < $3)::int
		FROM stay.stays WHERE property_id = $1 AND kind = 'bungalow'`, property, from, to).Scan(&out.Cancelled, &out.NoShows); err != nil {
		return out, err
	}
	for _, s := range append(append([]Stay{}, fo.Arrivals...), fo.InHouse...) {
		if s.PaymentStatus != "paid" {
			out.PendingPayment = append(out.PendingPayment, s)
		}
		if s.VIP {
			out.VIP = append(out.VIP, s)
		}
	}
	ready := map[uuid.UUID]bool{}
	states, err := m.RoomStates(ctx, q, property, from, nil)
	if err != nil {
		return out, err
	}
	for _, r := range states {
		ready[r.ID] = r.OperationalStatus == "ready"
	}
	for _, s := range fo.Arrivals {
		if !s.UnitAssigned || !ready[s.UnitID] {
			out.PendingPreparation = append(out.PendingPreparation, s)
		}
	}
	if out.Requests, err = handle.List[GuestRequest](q.Query(ctx, requestSelect+` WHERE g.property_id = $1 AND g.status IN ('requested', 'assigned', 'in_progress')
		ORDER BY g.created_at`, property)); err != nil {
		return out, err
	}
	hk, err := m.HKBoardFor(ctx, q, property, from, "")
	if err != nil {
		return out, err
	}
	out.HousekeepingTasks = []HKTask{}
	for _, t := range hk.Tasks {
		if t.Status == "open" || t.Status == "in_progress" || t.Status == "completed" {
			out.HousekeepingTasks = append(out.HousekeepingTasks, t)
		}
	}
	out.MaintenanceIssues, err = handle.List[WorkOrder](q.Query(ctx, woSelect+` WHERE w.property_id = $1 AND w.status IN ('open', 'assigned', 'in_progress')
		ORDER BY CASE w.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 ELSE 2 END, w.created_at`, property))
	return out, err
}

// Breakdown is a count and revenue per key.
type Breakdown struct {
	Key     string `json:"key" db:"key"`
	Count   int    `json:"count" db:"count"`
	Nights  int    `json:"nights" db:"nights"`
	Revenue string `json:"revenue" db:"revenue"`
}

// Report is the accommodation report of a period (§32).
type Report struct {
	From                  string      `json:"from"`
	To                    string      `json:"to"`
	AvailableRoomNights   int         `json:"availableRoomNights"`
	OccupiedRoomNights    int         `json:"occupiedRoomNights"`
	OutOfOrderRoomNights  int         `json:"outOfOrderRoomNights"`
	Occupancy             string      `json:"occupancy"`
	ADR                   string      `json:"adr"`
	RevPAR                string      `json:"revpar"`
	RoomRevenue           string      `json:"roomRevenue"`
	AddonRevenue          string      `json:"addonRevenue"`
	OtherRevenue          string      `json:"otherRevenue"`
	TotalRevenue          string      `json:"totalRevenue"`
	BookingVolume         int         `json:"bookingVolume" doc:"Reservations made in the period"`
	ByRoomType            []Breakdown `json:"byRoomType"`
	BySource              []Breakdown `json:"bySource"`
	Cancellations         int         `json:"cancellations"`
	NoShows               int         `json:"noShows"`
	ALOS                  string      `json:"alos" doc:"Average length of stay (nights)"`
	NewGuests             int         `json:"newGuests"`
	ReturningGuests       int         `json:"returningGuests"`
	AvgGuestSpending      string      `json:"avgGuestSpending" doc:"Average folio charges per checked-out stay"`
	RoomTurnaroundMinutes *int        `json:"roomTurnaroundMinutes" doc:"Average check-out cleaning: task created → room ready"`
	HKCompletion          string      `json:"hkCompletion" doc:"Housekeeping tasks done / tasks (%)"`
	HKTasks               int         `json:"hkTasks"`
	MaintenanceIssues     int         `json:"maintenanceIssues"`
	MaintenanceOpen       int         `json:"maintenanceOpen"`
	MaintenanceHours      *string     `json:"maintenanceHours" doc:"Average hours to resolve"`
	MaintenanceByCategory []Breakdown `json:"maintenanceByCategory"`
	RequestCompletion     string      `json:"requestCompletion" doc:"Guest requests completed / requests (%)"`
	Requests              int         `json:"requests"`
	RequestMinutes        *int        `json:"requestMinutes" doc:"Average minutes to complete a request"`
	Nights                []NightStat `json:"nights"`
}

// ReportFor builds the report of the local dates [from, to].
func (m *Module) ReportFor(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) (Report, error) {
	loc := calendar.Location(ctx, q)
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	endDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	days := int(endDay.Sub(start).Hours()/24+0.5) + 1
	if days < 1 {
		days = 1
	}
	if days > 366 {
		days = 366
	}
	end := start.AddDate(0, 0, days)
	out := Report{From: start.Format(time.DateOnly), To: end.AddDate(0, 0, -1).Format(time.DateOnly)}
	nights, err := m.nightStats(ctx, q, property, start, days)
	if err != nil {
		return out, err
	}
	out.Nights = nights
	rev := decimal.Zero
	for _, n := range nights {
		out.AvailableRoomNights += n.Available
		out.OccupiedRoomNights += n.Occupied
		out.OutOfOrderRoomNights += n.OutOfOrder
		rev = rev.Add(decOf(n.RoomRevenue))
	}
	out.RoomRevenue = rev.String()
	out.Occupancy, out.ADR, out.RevPAR = kpis(rev, out.OccupiedRoomNights, out.AvailableRoomNights)
	var addon, other string
	if err := q.QueryRow(ctx, `SELECT
		coalesce((SELECT sum(sa.total) FROM stay.stay_addons sa WHERE sa.property_id = $1 AND sa.voided_at IS NULL AND NOT sa.included
		  AND sa.created_at >= $2 AND sa.created_at < $3), 0)::text,
		coalesce((SELECT sum(l.net_amount) FROM reporting.eng_folio_lines l JOIN stay.stays s ON s.folio_id = l.folio_id WHERE s.property_id = $1
		  AND s.kind = 'bungalow' AND l.posted_at >= $2 AND l.posted_at < $3 AND coalesce(l.reference_type, '') NOT IN ('reservation.line', 'stay.addon')), 0)::text`,
		property, start, end).Scan(&addon, &other); err != nil {
		return out, err
	}
	out.AddonRevenue, out.OtherRevenue = decOf(addon).Round(0).String(), decOf(other).Round(0).String()
	out.TotalRevenue = rev.Add(decOf(addon)).Add(decOf(other)).Round(0).String()
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE created_at >= $2 AND created_at < $3)::int,
		count(*) FILTER (WHERE status = 'cancelled' AND cancelled_at >= $2 AND cancelled_at < $3)::int,
		count(*) FILTER (WHERE status = 'no_show' AND start_at >= $2 AND start_at < $3)::int
		FROM stay.stays WHERE property_id = $1 AND kind = 'bungalow'`, property, start, end).Scan(&out.BookingVolume, &out.Cancellations, &out.NoShows); err != nil {
		return out, err
	}
	nightsExpr := `greatest(1, round(extract(epoch FROM coalesce(s.actual_end_at, s.end_at) - s.start_at) / 86400))`
	if out.ByRoomType, err = handle.List[Breakdown](q.Query(ctx, `SELECT coalesce(t.name, '—') AS key, count(*)::int AS count, sum(`+nightsExpr+`)::int AS nights,
		trim_scale(round(sum(`+roomNetSQL+` * `+nightsExpr+`), 0))::text AS revenue FROM stay.stays s LEFT JOIN stay.bungalow_types t ON t.id = s.unit_type_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.created_at >= $2 AND s.created_at < $3 AND s.status <> 'requested'
		GROUP BY t.name ORDER BY count(*) DESC`, property, start, end)); err != nil {
		return out, err
	}
	if out.BySource, err = handle.List[Breakdown](q.Query(ctx, `SELECT coalesce(s.booking_source, s.channel) AS key, count(*)::int AS count,
		sum(`+nightsExpr+`)::int AS nights, trim_scale(round(sum(`+roomNetSQL+` * `+nightsExpr+`), 0))::text AS revenue FROM stay.stays s
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.created_at >= $2 AND s.created_at < $3 GROUP BY 1 ORDER BY count(*) DESC`, property, start, end)); err != nil {
		return out, err
	}
	var alos *string
	var spend *string
	if err := q.QueryRow(ctx, `SELECT trim_scale(round(avg(`+nightsExpr+`), 1))::text,
		count(*) FILTER (WHERE s.customer_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM stay.stays p WHERE p.customer_id = s.customer_id AND p.kind = 'bungalow'
		  AND p.status IN ('checked_in', 'checked_out') AND p.start_at < s.start_at))::int,
		count(*) FILTER (WHERE s.customer_id IS NOT NULL AND EXISTS (SELECT 1 FROM stay.stays p WHERE p.customer_id = s.customer_id AND p.kind = 'bungalow'
		  AND p.status IN ('checked_in', 'checked_out') AND p.start_at < s.start_at))::int,
		trim_scale(round(avg(ef.charges) FILTER (WHERE s.status = 'checked_out'), 0))::text
		FROM stay.stays s LEFT JOIN reporting.eng_folios ef ON ef.folio_id = s.folio_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status IN ('checked_in', 'checked_out') AND s.start_at >= $2 AND s.start_at < $3`,
		property, start, end).Scan(&alos, &out.NewGuests, &out.ReturningGuests, &spend); err != nil {
		return out, err
	}
	out.ALOS, out.AvgGuestSpending = nonEmpty(deref(alos), "0"), nonEmpty(deref(spend), "0")
	var hkDone int
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status <> 'cancelled')::int, count(*) FILTER (WHERE status IN ('completed', 'inspected'))::int,
		(avg(extract(epoch FROM coalesce(inspected_at, completed_at) - created_at) / 60) FILTER (WHERE task_type = 'checkout_cleaning'
		  AND coalesce(inspected_at, completed_at) IS NOT NULL))::int
		FROM stay.housekeeping_tasks WHERE property_id = $1 AND task_date >= $2::date AND task_date < $3::date`,
		property, out.From, end.Format(time.DateOnly)).Scan(&out.HKTasks, &hkDone, &out.RoomTurnaroundMinutes); err != nil {
		return out, err
	}
	out.HKCompletion = pct(hkDone, out.HKTasks)
	var hours *string
	if err := q.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE status IN ('open', 'assigned', 'in_progress'))::int,
		trim_scale(round(avg(extract(epoch FROM resolved_at - created_at) / 3600) FILTER (WHERE resolved_at IS NOT NULL), 1))::text
		FROM stay.work_orders WHERE property_id = $1 AND created_at >= $2 AND created_at < $3`, property, start, end).
		Scan(&out.MaintenanceIssues, &out.MaintenanceOpen, &hours); err != nil {
		return out, err
	}
	out.MaintenanceHours = hours
	if out.MaintenanceByCategory, err = handle.List[Breakdown](q.Query(ctx, `SELECT category AS key, count(*)::int AS count, 0 AS nights,
		trim_scale(coalesce(sum(cost), 0))::text AS revenue FROM stay.work_orders WHERE property_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY category ORDER BY count(*) DESC`, property, start, end)); err != nil {
		return out, err
	}
	var reqDone int
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status <> 'cancelled')::int, count(*) FILTER (WHERE status = 'completed')::int,
		(avg(extract(epoch FROM completed_at - created_at) / 60) FILTER (WHERE completed_at IS NOT NULL))::int
		FROM stay.guest_requests WHERE property_id = $1 AND created_at >= $2 AND created_at < $3`, property, start, end).
		Scan(&out.Requests, &reqDone, &out.RequestMinutes); err != nil {
		return out, err
	}
	out.RequestCompletion = pct(reqDone, out.Requests)
	return out, nil
}

func pct(a, b int) string {
	if b == 0 {
		return "0"
	}
	return decimal.NewFromInt(int64(a)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(b))).Round(1).String()
}

// today is the local day of now.
func today(ctx context.Context, q dbtx.Querier) time.Time {
	loc := calendar.Location(ctx, q)
	n := clock.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
}
