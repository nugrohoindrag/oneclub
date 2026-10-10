package sportclub

// Operations of the courts (docs/requirement-booking-sportclub-mgcc.md):
// the slot grid of the website, Member App and the desk (FR-14..16, FR-61),
// the court board of the front desk (FR-52..60), the staff on duty from the
// HRIS roster (FR-101), the court staff's ready / problem reports (FR-119)
// and the blocked slots (FR-74).

import (
	"context"
	"encoding/json"
	"fmt"
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
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/realtime"
	"oneclub/internal/reservation"
)

// ── slot grid ────────────────────────────────────────────────────────────────

// GridSlot is one hour of a court.
type GridSlot struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Status    string    `json:"status" enum:"available,booked,blocked,past"`
	Price     *string   `json:"price" doc:"Price charged for this hour (after promotion, before tax)"`
	ListPrice *string   `json:"listPrice" doc:"Brochure price when a promotion lowers it (struck through)"`
}

// GridCourt is a court with its hours.
type GridCourt struct {
	Court CourtInfo  `json:"court"`
	Slots []GridSlot `json:"slots"`
	Free  int        `json:"free"`
}

// Grid is the slot grid of a sport on a date.
type Grid struct {
	Date        string      `json:"date"`
	FacilityID  uuid.UUID   `json:"facilityId"`
	Courts      []GridCourt `json:"courts"`
	TaxIncluded bool        `json:"taxIncluded"`
	WindowDays  int         `json:"windowDays"`
}

// grid builds the hours of the courts of a sport on a local date, with the
// price of the quote function per free hour.
func (m *Module) grid(ctx context.Context, q dbtx.Querier, property, facility uuid.UUID, day time.Time, online bool, courtIDs []uuid.UUID) (Grid, error) {
	pol, err := m.courtPolicy(ctx, q, property)
	if err != nil {
		return Grid{}, err
	}
	loc := calendar.Location(ctx, q)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	out := Grid{Date: day.Format(time.DateOnly), FacilityID: facility, Courts: []GridCourt{}, TaxIncluded: pol.TaxIncluded, WindowDays: pol.WindowDays}
	where := ` AND c.facility_id = $2 AND c.status = 'active' AND f.status = 'active' AND c.resource_id IS NOT NULL`
	if online {
		where += ` AND c.online_booking AND f.online_booking`
	}
	courts, err := m.courts(ctx, q, property, where, facility)
	if err != nil {
		return out, err
	}
	now := clock.Now()
	type key struct {
		item  string
		start int64
	}
	prices := map[key][2]*string{}
	for _, c := range courts {
		if len(courtIDs) > 0 && !containsID(courtIDs, c.ID) {
			continue
		}
		av, err := m.Res.Availability(ctx, q, property, reservation.AvailabilityQuery{ResourceType: "sport_court", Date: day, Days: 1, ResourceID: c.ResourceID})
		if err != nil {
			return out, err
		}
		gc := GridCourt{Court: c, Slots: []GridSlot{}}
		if len(av.Resources) > 0 {
			for _, s := range av.Resources[0].Slots {
				g := GridSlot{Start: s.Start, End: s.End}
				switch {
				case s.Status == "blocked":
					g.Status = "blocked"
				case s.Status == "reserved" || s.Status == "full":
					g.Status = "booked"
				case !s.End.After(now):
					g.Status = "past"
				default:
					g.Status = "available"
				}
				if g.Status == "available" {
					k := key{c.PriceItem, s.Start.Unix()}
					p, ok := prices[k]
					if !ok {
						qt, err := m.quote(ctx, q, property, quoteRequest{Slots: []courtSlot{{Court: c, Start: s.Start, End: s.End}}, Channel: "website"}, pol)
						if err == nil && len(qt.Lines) == 1 {
							net := qt.Lines[0].Net
							p[0] = &net
							if qt.Lines[0].Discount != "0" {
								lst := qt.Lines[0].ListPrice
								p[1] = &lst
							}
						}
						prices[k] = p
					}
					g.Price, g.ListPrice = p[0], p[1]
					gc.Free++
				}
				gc.Slots = append(gc.Slots, g)
			}
		}
		out.Courts = append(out.Courts, gc)
	}
	return out, nil
}

// ── staff on duty (FR-101) ───────────────────────────────────────────────────

// DutyStaff is an employee on shift at the Sport Club.
type DutyStaff struct {
	EmployeeID uuid.UUID  `json:"employeeId" db:"employee_id"`
	Name       string     `json:"name" db:"name"`
	Role       string     `json:"role" db:"role"`
	From       time.Time  `json:"from" db:"starts_at"`
	To         time.Time  `json:"to" db:"ends_at"`
	UserID     *uuid.UUID `json:"userId" db:"user_id"`
}

// onDuty lists the Sport Club staff whose shift overlaps [from, to) (roster).
func (m *Module) onDuty(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]DutyStaff, error) {
	return handle.List[DutyStaff](q.Query(ctx, `SELECT e.id AS employee_id, e.full_name AS name,
		coalesce(a.workforce_role, p.workforce_role, 'sport_staff') AS role, a.starts_at, a.ends_at, u.id AS user_id
		FROM hris.shift_assignments a JOIN hris.employees e ON e.id = a.employee_id LEFT JOIN hris.positions p ON p.id = e.position_id
		LEFT JOIN platform.users u ON u.employee_id = e.id
		WHERE a.property_id = $1 AND a.kind = 'shift' AND a.status = 'scheduled' AND a.starts_at < $3 AND a.ends_at > $2
		AND (coalesce(a.workforce_role, p.workforce_role, '') IN ('sport_staff', 'lifeguard', 'instructor') OR 'sportclub' = ANY(e.work_areas))
		ORDER BY a.starts_at, e.full_name`, property, from, to))
}

// ── court board (FR-52..60) ──────────────────────────────────────────────────

// BoardBlock is a blocked period of a court.
type BoardBlock struct {
	ID      uuid.UUID  `json:"id" db:"id"`
	CourtID uuid.UUID  `json:"courtId" db:"court_id"`
	Start   time.Time  `json:"start" db:"start_at"`
	End     time.Time  `json:"end" db:"end_at"`
	Reason  string     `json:"reason" db:"reason"`
	Notes   *string    `json:"notes" db:"notes"`
	Code    string     `json:"code" db:"code"`
	IssueID *uuid.UUID `json:"issueId" db:"issue_id" doc:"Problem reported by the court staff"`
}

// SportRevenue is the revenue of a sport.
type SportRevenue struct {
	Facility string `json:"facility" db:"facility"`
	Amount   string `json:"amount" db:"amount"`
}

// BoardSummary are the figures above the board (FR-57).
type BoardSummary struct {
	Bookings        int            `json:"bookings" doc:"Bookings with a line today (not expired / void)"`
	InUse           int            `json:"inUse" doc:"Courts with a line in play now"`
	Courts          int            `json:"courts"`
	AwaitingPayment int            `json:"awaitingPayment" doc:"Held online, or not paid at the desk"`
	Late            int            `json:"late" doc:"Lines started without check-in"`
	Revenue         string         `json:"revenue" doc:"Rent and extras of today's lines before tax (per day of play)"`
	RevenueBySport  []SportRevenue `json:"revenueBySport"`
}

// BoardAlert is a warning of the board.
type BoardAlert struct {
	Kind      string     `json:"kind" enum:"issue,late,unpaid_soon,reminder,outside_hours"`
	Message   string     `json:"message"`
	CourtID   *uuid.UUID `json:"courtId,omitempty"`
	BookingID *uuid.UUID `json:"bookingId,omitempty"`
}

// Board is the court board of a day.
type Board struct {
	Date     string         `json:"date"`
	From     time.Time      `json:"from" doc:"First hour shown"`
	To       time.Time      `json:"to" doc:"Last hour shown"`
	Courts   []CourtInfo    `json:"courts"`
	Bookings []CourtBooking `json:"bookings"`
	Blocks   []BoardBlock   `json:"blocks"`
	Summary  BoardSummary   `json:"summary"`
	OnDuty   []DutyStaff    `json:"onDuty"`
	Alerts   []BoardAlert   `json:"alerts"`
	Issues   []CourtReport  `json:"issues"`
}

// board builds the court board of a local day.
func (m *Module) board(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, facility *uuid.UUID) (Board, error) {
	loc := calendar.Location(ctx, q)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	next := day.AddDate(0, 0, 1)
	out := Board{Date: day.Format(time.DateOnly), Bookings: []CourtBooking{}, Blocks: []BoardBlock{}, Alerts: []BoardAlert{}, Issues: []CourtReport{}}
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
	out.Courts = courts
	if out.Courts == nil {
		out.Courts = []CourtInfo{}
	}
	// hours shown: the widest opening of the courts that day
	rt, err := m.Res.Type(ctx, q, "sport_court")
	if err != nil {
		return out, err
	}
	out.From, out.To = next, day
	for _, c := range courts {
		res, err := m.Res.Resource(ctx, q, *c.ResourceID)
		if err != nil {
			return out, err
		}
		o, cl, closed, err := m.Res.OpenHours(ctx, q, property, res, rt, day)
		if err != nil {
			return out, err
		}
		if closed {
			continue
		}
		if o.Before(out.From) {
			out.From = o
		}
		if cl.After(out.To) {
			out.To = cl
		}
	}
	if !out.From.Before(out.To) {
		out.From, out.To = day.Add(6*time.Hour), day.Add(22*time.Hour)
	}
	all, err := m.loadBookings(ctx, q, property, `AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id
		AND l.period && tstzrange($2, $3, '[)')) ORDER BY r.created_at`, day, next)
	if err != nil {
		return out, err
	}
	now := clock.Now()
	inUse := map[uuid.UUID]bool{}
	pkgRevenue := decimal.Zero
	for _, b := range all {
		if b.State == StateExpired || b.Void {
			continue // admin only (FR-125)
		}
		if facility != nil && !bookingHasFacility(b, *facility) {
			continue
		}
		out.Bookings = append(out.Bookings, b)
		out.Summary.Bookings++
		if b.State == StateAwaitingPayment || (b.PayStatus != "paid" && b.PayStatus != "overpaid" && b.State != StateNoShow) {
			out.Summary.AwaitingPayment++
		}
		for _, l := range b.Lines {
			if l.Start.Before(day) || !l.Start.Before(next) {
				continue
			}
			if l.State == StatePlaying && !now.Before(l.Start) && now.Before(l.End) && l.CourtID != nil {
				inUse[*l.CourtID] = true
			}
			if l.State == StateLate {
				out.Summary.Late++
				bid := b.ID
				out.Alerts = append(out.Alerts, BoardAlert{Kind: "late", BookingID: &bid, CourtID: l.CourtID,
					Message: fmt.Sprintf("%s %s: %s belum datang", l.CourtName, l.Start.In(loc).Format("15:04"), b.Name)})
			}
			if l.State == StateScheduled && b.PayStatus != "paid" && b.PayStatus != "overpaid" && l.Start.Sub(now) < 2*time.Hour && l.Start.After(now) {
				bid := b.ID
				out.Alerts = append(out.Alerts, BoardAlert{Kind: "unpaid_soon", BookingID: &bid, CourtID: l.CourtID,
					Message: fmt.Sprintf("%s %s: %s belum lunas", l.CourtName, l.Start.In(loc).Format("15:04"), b.Name)})
			}
			if l.State == StateScheduled && l.Start.After(now) && l.Start.Sub(now) <= 15*time.Minute {
				bid := b.ID
				out.Alerts = append(out.Alerts, BoardAlert{Kind: "reminder", BookingID: &bid, CourtID: l.CourtID,
					Message: fmt.Sprintf("%s %s: siapkan lapangan dan nyalakan lampu (%s)", l.CourtName, l.Start.In(loc).Format("15:04"), b.Name)})
			}
		}
		if b.Package != nil && b.State != StateNoShow {
			v, _ := decimal.NewFromString(fmt.Sprint(b.Package["value"]))
			pkgRevenue = pkgRevenue.Add(v)
		}
	}
	out.Summary.Courts = len(courts)
	out.Summary.InUse = len(inUse)
	if out.Summary.RevenueBySport, err = m.revenueBySport(ctx, q, property, day, next); err != nil {
		return out, err
	}
	total := pkgRevenue
	for _, r := range out.Summary.RevenueBySport {
		v, _ := decimal.NewFromString(r.Amount)
		total = total.Add(v)
	}
	out.Summary.Revenue = total.StringFixed(0)
	if out.Blocks, err = m.blocks(ctx, q, property, day, next); err != nil {
		return out, err
	}
	if out.OnDuty, err = m.onDuty(ctx, q, property, day, next); err != nil {
		return out, err
	}
	if out.Issues, err = m.openIssues(ctx, q, property); err != nil {
		return out, err
	}
	for _, i := range out.Issues {
		cid := i.CourtID
		out.Alerts = append([]BoardAlert{{Kind: "issue", CourtID: &cid, Message: i.CourtName + ": " + deref(i.Note)}}, out.Alerts...)
	}
	return out, nil
}

func bookingHasFacility(b CourtBooking, f uuid.UUID) bool {
	for _, l := range b.Lines {
		if l.FacilityID != nil && *l.FacilityID == f {
			return true
		}
	}
	return false
}

// revenueBySport sums the rent and extras of the lines played in [from, to)
// before tax (per day of play, not of payment); the service fee is not
// revenue of the sport.
func (m *Module) revenueBySport(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]SportRevenue, error) {
	return handle.List[SportRevenue](q.Query(ctx, `WITH b AS (
		  SELECT r.id, r.folio_id, (SELECT l.id FROM reservation.reservation_lines l WHERE l.reservation_id = r.id ORDER BY lower(l.period), l.line_no LIMIT 1) AS first_line
		  FROM reservation.reservations r WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND r.folio_id IS NOT NULL
		  AND r.status NOT IN ('expired', 'cancelled', 'draft'))
		SELECT f.name AS facility, trim_scale(sum(x.net_amount))::text AS amount
		FROM b JOIN billing.folio_lines x ON x.folio_id = b.folio_id AND x.voided_at IS NULL AND coalesce(x.revenue_component, '') <> 'gateway_fee'
		JOIN reservation.reservation_lines l ON l.id = CASE WHEN x.reference_type = 'reservation.line' THEN x.reference_id ELSE b.first_line END
		JOIN sportclub.courts c ON c.resource_id = l.resource_id JOIN sportclub.facilities f ON f.id = c.facility_id
		WHERE lower(l.period) >= $2 AND lower(l.period) < $3 AND l.status <> 'cancelled'
		GROUP BY f.name, f.sort_order ORDER BY f.sort_order, f.name`, property, from, to))
}

// ── blocked slots (FR-74) ────────────────────────────────────────────────────

func (m *Module) blocks(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]BoardBlock, error) {
	return handle.List[BoardBlock](q.Query(ctx, `SELECT r.id, c.id AS court_id, lower(l.period) AS start_at, upper(l.period) AS end_at,
		coalesce(r.attributes ->> 'blockReason', 'other') AS reason, r.notes, r.code, (SELECT cr.id FROM sportclub.court_reports cr WHERE cr.block_id = r.id LIMIT 1) AS issue_id
		FROM reservation.reservations r JOIN reservation.reservation_lines l ON l.reservation_id = r.id
		JOIN sportclub.courts c ON c.resource_id = l.resource_id
		WHERE r.property_id = $1 AND r.kind = 'block' AND r.status = 'confirmed' AND l.period && tstzrange($2, $3, '[)')
		ORDER BY lower(l.period), c.name`, property, from, to))
}

// BlockInput blocks courts for maintenance, a tournament or an event, on a
// date range and hours (optionally on some weekdays).
type BlockInput struct {
	CourtIDs  []uuid.UUID `json:"courtIds"`
	From      string      `json:"from" doc:"First date YYYY-MM-DD"`
	To        string      `json:"to,omitempty" doc:"Last date (default: from)"`
	StartTime string      `json:"startTime" doc:"HH:MM"`
	EndTime   string      `json:"endTime" doc:"HH:MM (24:00 = midnight)"`
	Weekdays  []int       `json:"weekdays,omitempty" doc:"1 = Monday … 7 = Sunday (default every day)"`
	Reason    string      `json:"reason" enum:"maintenance,tournament,private_event,management_hold,weather_closure,other"`
	Notes     string      `json:"notes,omitempty"`
	DryRun    bool        `json:"dryRun,omitempty" doc:"Only list the bookings in the way"`
}

// BlockResult lists the blocks made and the bookings in the way.
type BlockResult struct {
	Blocks    []uuid.UUID    `json:"blocks"`
	Conflicts []CourtBooking `json:"conflicts" doc:"Bookings in the period: move them first (the block is refused while they stay)"`
	Periods   int            `json:"periods"`
}

func parseHM(day time.Time, hm string) (time.Time, error) {
	if hm == "24:00" {
		return day.AddDate(0, 0, 1), nil
	}
	t, err := time.Parse("15:04", hm)
	if err != nil {
		return day, handle.Invalid("startTime", "invalid_time", "time must be HH:MM")
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, day.Location()), nil
}

// Block blocks courts; when bookings are in the way it lists them and
// blocks nothing (move them first, FR-74).
func (m *Module) Block(ctx context.Context, tx pgx.Tx, property uuid.UUID, in BlockInput) (BlockResult, error) {
	out := BlockResult{Blocks: []uuid.UUID{}, Conflicts: []CourtBooking{}}
	if len(in.CourtIDs) == 0 {
		return out, handle.Invalid("courtIds", "required", "choose at least one court")
	}
	if in.Reason == "" {
		return out, handle.Invalid("reason", "required", "the reason is required")
	}
	loc := calendar.Location(ctx, tx)
	from, err := time.ParseInLocation(time.DateOnly, in.From, loc)
	if err != nil {
		return out, handle.Invalid("from", "invalid_date", "from must be YYYY-MM-DD")
	}
	to := from
	if in.To != "" {
		if to, err = time.ParseInLocation(time.DateOnly, in.To, loc); err != nil || to.Before(from) {
			return out, handle.Invalid("to", "invalid_date", "to must be a date on or after from")
		}
	}
	if to.Sub(from) > 366*24*time.Hour {
		return out, handle.Invalid("to", "too_long", "at most one year")
	}
	type period struct {
		court      CourtInfo
		start, end time.Time
	}
	var periods []period
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		wd := int(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		if len(in.Weekdays) > 0 && !containsInt(in.Weekdays, wd) {
			continue
		}
		s, err := parseHM(d, in.StartTime)
		if err != nil {
			return out, err
		}
		e, err := parseHM(d, in.EndTime)
		if err != nil {
			return out, err
		}
		if !e.After(s) {
			return out, handle.Invalid("endTime", "invalid_period", "the end must be after the start")
		}
		for _, cid := range in.CourtIDs {
			c, err := m.court(ctx, tx, property, cid)
			if err != nil {
				return out, err
			}
			if c.ResourceID == nil {
				return out, errs.Conflict("court_not_bookable", c.Name+" has no bookable resource")
			}
			periods = append(periods, period{c, s, e})
		}
	}
	out.Periods = len(periods)
	seen := map[uuid.UUID]bool{}
	for _, p := range periods {
		list, err := m.loadBookings(ctx, tx, property, `AND r.status IN ('draft', 'pending', 'confirmed', 'checked_in') AND EXISTS (SELECT 1 FROM reservation.reservation_lines l
			WHERE l.reservation_id = r.id AND l.resource_id = $2 AND l.status IN ('held', 'confirmed', 'checked_in') AND l.period && tstzrange($3, $4, '[)'))`,
			*p.court.ResourceID, p.start, p.end)
		if err != nil {
			return out, err
		}
		for _, b := range list {
			if !seen[b.ID] {
				seen[b.ID] = true
				out.Conflicts = append(out.Conflicts, b)
			}
		}
	}
	if in.DryRun || len(out.Conflicts) > 0 {
		return out, nil
	}
	for _, p := range periods {
		r, err := m.Res.Book(ctx, tx, property, reservation.BookRequest{Kind: "block", BusinessLine: "sportclub", Confirm: true, Notes: in.Notes,
			SourceType: "sportclub.block", Attributes: map[string]any{"blockReason": in.Reason},
			Lines: []reservation.LineRequest{{ResourceID: *p.court.ResourceID, Start: p.start, End: p.end, Description: "Blocked: " + in.Reason}}})
		if err != nil {
			return out, err
		}
		out.Blocks = append(out.Blocks, r.ID)
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: "block", EntityType: "sportclub.block", EntityID: property.String(),
		EntityLabel: fmt.Sprintf("%d block(s) · %s", len(out.Blocks), in.Reason), PropertyID: &property, After: in}); err != nil {
		return out, err
	}
	return out, realtime.Publish(ctx, tx, "sportclub.board", "blocked", &property, map[string]any{"blocks": len(out.Blocks)})
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Unblock removes a block (supervisor).
func (m *Module) Unblock(ctx context.Context, tx pgx.Tx, property, blockID uuid.UUID, reason string) error {
	var kind, source string
	if err := tx.QueryRow(ctx, `SELECT kind, coalesce(source_type, '') FROM reservation.reservations WHERE id = $1 AND property_id = $2`, blockID, property).
		Scan(&kind, &source); err != nil {
		return errNotFound("block")
	}
	if kind != "block" {
		return errs.Conflict("not_a_block", "this is not a blocked slot")
	}
	if reason == "" {
		reason = "unblocked"
	}
	if _, err := m.Res.Cancel(ctx, tx, blockID, reason, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.court_reports SET status = 'resolved', resolved_at = now(), resolved_by = $2 WHERE block_id = $1 AND status = 'open'`,
		blockID, actor(ctx)); err != nil {
		return err
	}
	return realtime.Publish(ctx, tx, "sportclub.board", "unblocked", &property, map[string]any{"blockId": blockID})
}

// ── court staff (FR-119) ─────────────────────────────────────────────────────

// CourtReport is a ready or problem report of the court staff.
type CourtReport struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	CourtID       uuid.UUID  `json:"courtId" db:"court_id"`
	CourtName     string     `json:"courtName" db:"court_name"`
	Kind          string     `json:"kind" db:"kind" enum:"ready,issue"`
	Note          *string    `json:"note" db:"note"`
	ReservationID *uuid.UUID `json:"reservationId" db:"reservation_id"`
	BlockID       *uuid.UUID `json:"blockId" db:"block_id"`
	Until         *time.Time `json:"until" db:"until"`
	Status        string     `json:"status" db:"status" enum:"open,resolved"`
	CreatedAt     time.Time  `json:"createdAt" db:"created_at"`
	CreatedBy     *string    `json:"createdBy" db:"created_by_name"`
}

const reportSelect = `SELECT r.id, r.court_id, c.name AS court_name, r.kind, r.note, r.reservation_id, r.block_id, r.until, r.status, r.created_at,
	u.full_name AS created_by_name FROM sportclub.court_reports r JOIN sportclub.courts c ON c.id = r.court_id LEFT JOIN platform.users u ON u.id = r.created_by`

func (m *Module) openIssues(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]CourtReport, error) {
	return handle.List[CourtReport](q.Query(ctx, reportSelect+` WHERE r.property_id = $1 AND r.kind = 'issue' AND r.status = 'open'
		AND (r.until IS NULL OR r.until > now()) ORDER BY r.created_at DESC`, property))
}

// CourtReportInput reports a court ready or a problem.
type CourtReportInput struct {
	CourtID       uuid.UUID  `json:"courtId"`
	Kind          string     `json:"kind" enum:"ready,issue"`
	Note          string     `json:"note,omitempty"`
	ReservationID *uuid.UUID `json:"reservationId,omitempty" doc:"Ready: the booking prepared"`
	Hours         int        `json:"hours,omitempty" doc:"Issue: hours the court is closed from now (default 1)"`
}

// CourtReportResult is a report with the bookings an issue affects.
type CourtReportResult struct {
	Report   CourtReport    `json:"report"`
	Affected []CourtBooking `json:"affected" doc:"Bookings in the closed period: move them (front desk)"`
}

// ReportCourt records a ready court, or a problem that closes the court for
// a while (a temporary block where no booking is in the way) and alerts the
// front desk.
func (m *Module) ReportCourt(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CourtReportInput) (CourtReportResult, error) {
	out := CourtReportResult{Affected: []CourtBooking{}}
	c, err := m.court(ctx, tx, property, in.CourtID)
	if err != nil {
		return out, err
	}
	if in.Kind != "ready" && in.Kind != "issue" {
		return out, handle.Invalid("kind", "invalid_kind", "ready or issue")
	}
	rid := id.New()
	var blockID *uuid.UUID
	var until *time.Time
	if in.Kind == "issue" {
		if strings.TrimSpace(in.Note) == "" {
			return out, handle.Invalid("note", "required", "describe the problem")
		}
		h := max(in.Hours, 1)
		now := clock.Now().In(calendar.Location(ctx, tx))
		start := now.Truncate(time.Hour)
		end := start.Add(time.Duration(h+1) * time.Hour)
		until = &end
		list, err := m.loadBookings(ctx, tx, property, `AND r.status IN ('draft', 'pending', 'confirmed', 'checked_in') AND EXISTS (SELECT 1 FROM reservation.reservation_lines l
			WHERE l.reservation_id = r.id AND l.resource_id = $2 AND l.status IN ('held', 'confirmed', 'checked_in') AND l.period && tstzrange($3, $4, '[)'))`,
			*c.ResourceID, now, end)
		if err != nil {
			return out, err
		}
		out.Affected = list
		// block the free hours from the next full hour; the bookings stay for the desk to move
		if len(list) == 0 {
			b, err := m.Res.Book(ctx, tx, property, reservation.BookRequest{Kind: "block", BusinessLine: "sportclub", Confirm: true, Notes: in.Note,
				SourceType: "sportclub.block", Attributes: map[string]any{"blockReason": "maintenance", "courtIssue": true},
				Lines: []reservation.LineRequest{{ResourceID: *c.ResourceID, Start: start.Add(time.Hour), End: end, Description: "Blocked: court problem"}}})
			if err == nil {
				blockID = &b.ID
			} else if de, ok := errs.As(err); !ok || de.Kind == errs.KindInternal {
				return out, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.court_reports (id, property_id, court_id, kind, note, reservation_id, block_id, until, status, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, rid, property, c.ID, in.Kind, nzs(in.Note), in.ReservationID, blockID, until,
		map[bool]string{true: "resolved", false: "open"}[in.Kind == "ready"], actor(ctx)); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, reportSelect+` WHERE r.id = $1`, rid)
	if out.Report, err = handle.One[CourtReport](rows, err, "court report"); err != nil {
		return out, err
	}
	if in.Kind == "issue" {
		if err := m.notifyDesk(ctx, tx, property, "sportclub.court_issue", map[string]any{"court": c.Name, "note": in.Note,
			"affected": len(out.Affected)}, "/ops/sport"); err != nil {
			return out, err
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: "court_" + in.Kind, EntityType: "sportclub.court", EntityID: c.ID.String(),
		EntityLabel: c.Name, PropertyID: &property, After: out.Report}); err != nil {
		return out, err
	}
	return out, realtime.Publish(ctx, tx, "sportclub.board", "court_"+in.Kind, &property, map[string]any{"courtId": c.ID})
}

// ResolveReport closes a problem report and removes its temporary block.
func (m *Module) ResolveReport(ctx context.Context, tx pgx.Tx, property, reportID uuid.UUID) (CourtReport, error) {
	var block *uuid.UUID
	if err := tx.QueryRow(ctx, `UPDATE sportclub.court_reports SET status = 'resolved', resolved_at = now(), resolved_by = $3
		WHERE id = $1 AND property_id = $2 AND status = 'open' RETURNING block_id`, reportID, property, actor(ctx)).Scan(&block); err != nil {
		if dbtx.IsNoRows(err) {
			return CourtReport{}, errs.Conflict("not_open", "the report is not open")
		}
		return CourtReport{}, err
	}
	if block != nil {
		if _, err := m.Res.Cancel(ctx, tx, *block, "court problem resolved", true); err != nil {
			if de, ok := errs.As(err); !ok || de.Kind == errs.KindInternal {
				return CourtReport{}, err
			}
		}
	}
	rows, err := tx.Query(ctx, reportSelect+` WHERE r.id = $1`, reportID)
	rep, err := handle.One[CourtReport](rows, err, "court report")
	if err != nil {
		return rep, err
	}
	return rep, realtime.Publish(ctx, tx, "sportclub.board", "court_resolved", &property, map[string]any{"courtId": rep.CourtID})
}

// CourtStaffView is the phone screen of the court staff: the next two hours
// per court and the open problems.
type CourtStaffView struct {
	Now      time.Time      `json:"now"`
	Courts   []CourtInfo    `json:"courts"`
	Bookings []CourtBooking `json:"bookings"`
	Issues   []CourtReport  `json:"issues"`
	Ready    []CourtReport  `json:"ready" doc:"Courts marked ready today"`
}

func (m *Module) courtStaff(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CourtStaffView, error) {
	now := clock.Now()
	out := CourtStaffView{Now: now, Bookings: []CourtBooking{}}
	var err error
	if out.Courts, err = m.courts(ctx, q, property, ` AND c.status = 'active' AND c.resource_id IS NOT NULL`); err != nil {
		return out, err
	}
	list, err := m.loadBookings(ctx, q, property, `AND r.status IN ('confirmed', 'checked_in') AND EXISTS (SELECT 1 FROM reservation.reservation_lines l
		WHERE l.reservation_id = r.id AND l.status IN ('confirmed', 'checked_in') AND l.period && tstzrange($2, $3, '[)')) ORDER BY r.created_at`, now, now.Add(2*time.Hour))
	if err != nil {
		return out, err
	}
	out.Bookings = list
	if out.Issues, err = m.openIssues(ctx, q, property); err != nil {
		return out, err
	}
	loc := calendar.Location(ctx, q)
	n := now.In(loc)
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	out.Ready, err = handle.List[CourtReport](q.Query(ctx, reportSelect+` WHERE r.property_id = $1 AND r.kind = 'ready' AND r.created_at >= $2 ORDER BY r.created_at DESC`,
		property, day))
	return out, err
}

// ── notifications, reminders, realtime ───────────────────────────────────────

// notifyDesk alerts the Sport Club staff on duty (roster), else the holders
// of the front desk permission.
func (m *Module) notifyDesk(ctx context.Context, tx pgx.Tx, property uuid.UUID, event string, data map[string]any, link string) error {
	if m.Notify == nil {
		return nil
	}
	now := clock.Now()
	staff, err := m.onDuty(ctx, tx, property, now, now.Add(time.Minute))
	if err != nil {
		return err
	}
	var users []uuid.UUID
	for _, s := range staff {
		if s.UserID != nil {
			users = append(users, *s.UserID)
		}
	}
	if len(users) == 0 {
		if users, err = notify.Holders(ctx, tx, property, "sportclub.booking.operate"); err != nil {
			return err
		}
	}
	if len(users) == 0 {
		return nil
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "general", UserIDs: users, PropertyID: &property, Link: link, Data: data,
		Channels: []string{notify.ChannelInApp}})
}

// SendReminders sends the in-app reminder before each booked line to the
// staff on duty: prepare the court and switch the lights on (FR-59). Each
// line once.
func (m *Module) SendReminders(ctx context.Context) (int, error) {
	n := 0
	err := m.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		type due struct {
			LineID   uuid.UUID `db:"line_id"`
			Property uuid.UUID `db:"property_id"`
			Court    string    `db:"court"`
			Start    time.Time `db:"start_at"`
			Name     string    `db:"name"`
			Code     string    `db:"code"`
			Minutes  int       `db:"minutes"`
		}
		list, err := handle.List[due](tx.Query(ctx, `SELECT l.id AS line_id, l.property_id, c.name AS court, lower(l.period) AS start_at,
			coalesce(cu.name, r.guest_name, r.corporate_name, '') AS name, r.code, 0 AS minutes
			FROM reservation.reservation_lines l JOIN reservation.reservations r ON r.id = l.reservation_id
			JOIN sportclub.courts c ON c.resource_id = l.resource_id LEFT JOIN crm.customers cu ON cu.id = r.customer_id
			WHERE r.source_type = 'sportclub.court_booking' AND l.status = 'confirmed' AND lower(l.period) > now()
			AND lower(l.period) <= now() + interval '60 minutes'
			AND NOT EXISTS (SELECT 1 FROM sportclub.booking_reminders x WHERE x.line_id = l.id)`))
		if err != nil {
			return err
		}
		pols := map[uuid.UUID]CourtPolicy{}
		for _, d := range list {
			pol, ok := pols[d.Property]
			if !ok {
				if pol, err = m.courtPolicy(ctx, tx, d.Property); err != nil {
					return err
				}
				pols[d.Property] = pol
			}
			if pol.ReminderMinutes <= 0 || d.Start.Sub(clock.Now()) > time.Duration(pol.ReminderMinutes)*time.Minute {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO sportclub.booking_reminders (line_id, property_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, d.LineID, d.Property); err != nil {
				return err
			}
			loc := calendar.Location(ctx, tx)
			if err := m.notifyDesk(ctx, tx, d.Property, "sportclub.court_reminder", map[string]any{"court": d.Court, "time": d.Start.In(loc).Format("15:04"),
				"name": d.Name, "code": d.Code}, "/ops/sport/court-staff"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// OnReservationEvent pushes booking changes made elsewhere (online payment,
// hold expiry, back office) to the open court boards (FR-58, FR-134).
func (m *Module) OnReservationEvent(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		ReservationID uuid.UUID `json:"reservationId"`
		BusinessLine  string    `json:"businessLine"`
		Status        string    `json:"status"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.BusinessLine != "sportclub" {
		return nil //nolint:nilerr // not a Sport Club booking
	}
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM reservation.reservations WHERE id = $1`, p.ReservationID).Scan(&property); err != nil {
		return nil //nolint:nilerr // gone
	}
	return realtime.Publish(ctx, tx, "sportclub.board", ev.Type, &property, map[string]any{"reservationId": p.ReservationID, "status": p.Status})
}
