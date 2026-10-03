package reporting

// P1 reports (FR-RPT-03..06) and the golf operational dashboard
// (FR-RPT-01/02). Everything reads the reporting read models on the replica.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
)

type q struct {
	where []string
	args  []any
}

func (x *q) add(cond string, v any) {
	x.args = append(x.args, v)
	x.where = append(x.where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(x.args))))
}

func (x *q) sql() string {
	if len(x.where) == 0 {
		return "true"
	}
	return strings.Join(x.where, " AND ")
}

func dateRange(x *q, col string, p map[string]string) {
	if v := p["from"]; v != "" {
		x.add(col+" >= ?::date", v)
	}
	if v := p["to"]; v != "" {
		x.add(col+" <= ?::date", v)
	}
	if v := p["date"]; v != "" {
		x.add(col+" = ?::date", v)
	}
}

func propertyFilter(x *q, p map[string]string) {
	if v := p["propertyId"]; v != "" {
		x.add("property_id::text = ?", v)
	}
}

var dateParams = []Param{{Key: "from", Label: "From", Type: "date"}, {Key: "to", Label: "To", Type: "date"}, {Key: "propertyId", Label: "Property", Type: "uuid"}}
var dayParams = []Param{{Key: "date", Label: "Date", Type: "date"}, {Key: "propertyId", Label: "Property", Type: "uuid"}}

// DailyTeeSheetReport lists every player of a day (FR-RPT-03).
var DailyTeeSheetReport = &Report{
	Code: "golf.daily_tee_sheet", Name: "Daily Tee Sheet Report", Module: "golf", Permission: "reporting.golf_daily_tee_sheet.view",
	Description: "Every player of the day with tee time, flight, check-in, caddy and status.",
	Columns: []Column{{Key: "teeTime", Label: "Tee Time", Type: "datetime"}, {Key: "courseName", Label: "Course", Type: "string"},
		{Key: "startTee", Label: "Tee", Type: "number"}, {Key: "flightNo", Label: "Flight", Type: "number"}, {Key: "bookingCode", Label: "Booking", Type: "string"},
		{Key: "playerName", Label: "Player", Type: "string"}, {Key: "playerType", Label: "Player Type", Type: "string"}, {Key: "segment", Label: "Segment", Type: "string"},
		{Key: "playerStatus", Label: "Status", Type: "string"}, {Key: "checkedInAt", Label: "Check-in", Type: "datetime"}, {Key: "caddy", Label: "Caddy", Type: "string"},
		{Key: "teeOffAt", Label: "Tee-Off", Type: "datetime"}},
	Params: dayParams,
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{}
		if p["date"] == "" {
			p["date"] = time.Now().Format("2006-01-02")
		}
		dateRange(x, "play_date", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT start_at AS "teeTime", course_name AS "courseName", start_tee AS "startTee", flight_no AS "flightNo",
			booking_code AS "bookingCode", player_name AS "playerName", player_type AS "playerType", segment, player_status AS "playerStatus",
			checked_in_at AS "checkedInAt", caddy, tee_off_at AS "teeOffAt" FROM reporting.golf_players WHERE `+x.sql()+
			` ORDER BY start_at, start_tee, flight_no, seq`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// GolfBookingReport lists bookings of a period.
var GolfBookingReport = &Report{
	Code: "golf.bookings", Name: "Golf Booking Report", Module: "golf", Permission: "reporting.golf_bookings.view",
	Description: "Bookings per tee time with type, channel, players and charges.",
	Columns: []Column{{Key: "code", Label: "Booking", Type: "string"}, {Key: "startAt", Label: "Tee Time", Type: "datetime"}, {Key: "courseName", Label: "Course", Type: "string"},
		{Key: "bookingType", Label: "Type", Type: "string"}, {Key: "channel", Label: "Channel", Type: "string"}, {Key: "contactName", Label: "Booked by", Type: "string"},
		{Key: "playerCount", Label: "Players", Type: "number"}, {Key: "status", Label: "Status", Type: "string"}, {Key: "paymentMode", Label: "Payment", Type: "string"},
		{Key: "charges", Label: "Charges", Type: "number"}},
	Params: append([]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"pending", "confirmed", "checked_in", "completed", "cancelled", "no_show"}},
		{Key: "channel", Label: "Channel", Type: "enum", Enum: []string{"member_app", "website", "back_office", "walk_in", "import"}}}, dateParams...),
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{}
		dateRange(x, "play_date", p)
		propertyFilter(x, p)
		if v := p["status"]; v != "" {
			x.add("status = ?", v)
		}
		if v := p["channel"]; v != "" {
			x.add("channel = ?", v)
		}
		rows, err := tx.Query(ctx, `SELECT code, start_at AS "startAt", course_name AS "courseName", booking_type AS "bookingType", channel, contact_name AS "contactName",
			player_count AS "playerCount", status, payment_mode AS "paymentMode", charges::float8 AS charges FROM reporting.golf_bookings WHERE `+x.sql()+
			` ORDER BY start_at, code`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// NoShowCancellationReport lists cancelled and no-show bookings with fees.
var NoShowCancellationReport = &Report{
	Code: "golf.no_show_cancellation", Name: "No-show & Cancellation Report", Module: "golf", Permission: "reporting.golf_no_show_cancellation.view",
	Description: "Cancelled and No-show bookings with reason and fees.",
	Columns: []Column{{Key: "code", Label: "Booking", Type: "string"}, {Key: "startAt", Label: "Tee Time", Type: "datetime"}, {Key: "contactName", Label: "Booked by", Type: "string"},
		{Key: "status", Label: "Status", Type: "string"}, {Key: "reason", Label: "Reason", Type: "string"}, {Key: "fees", Label: "Fees", Type: "number"},
		{Key: "at", Label: "Cancelled / No-show", Type: "datetime"}},
	Params: dateParams,
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{where: []string{"status IN ('cancelled', 'no_show')"}}
		dateRange(x, "play_date", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT code, start_at AS "startAt", contact_name AS "contactName", status, cancel_reason AS reason, fees::float8 AS fees,
			coalesce(cancelled_at, no_show_at) AS at FROM reporting.golf_bookings WHERE `+x.sql()+` ORDER BY start_at`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// CaddyAssignmentReport lists assignments with fees and tips per caddy.
var CaddyAssignmentReport = &Report{
	Code: "golf.caddy_assignments", Name: "Caddy Assignment Report", Module: "golf", Permission: "reporting.golf_caddy_assignments.view",
	Description: "Caddy assignments per day with caddy fee (held for the caddy) and tips.",
	Columns: []Column{{Key: "playDate", Label: "Date", Type: "string"}, {Key: "caddyCode", Label: "Caddy No.", Type: "string"}, {Key: "caddyName", Label: "Caddy", Type: "string"},
		{Key: "bookingCode", Label: "Booking", Type: "string"}, {Key: "players", Label: "Players", Type: "number"}, {Key: "status", Label: "Status", Type: "string"},
		{Key: "feeAmount", Label: "Caddy Fee", Type: "number"}, {Key: "tips", Label: "Tips", Type: "number"}},
	Params: append([]Param{{Key: "date", Label: "Date", Type: "date"}}, dateParams...),
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{where: []string{"status <> 'cancelled'"}}
		dateRange(x, "play_date", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT to_char(play_date, 'YYYY-MM-DD') AS "playDate", caddy_code AS "caddyCode", caddy_name AS "caddyName", booking_code AS "bookingCode",
			players, status, fee_amount::float8 AS "feeAmount", tips::float8 AS tips FROM reporting.golf_caddy_assignments WHERE `+x.sql()+
			` ORDER BY play_date, caddy_code, window_start`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// GolfCartUsageReport lists golf cart usage.
var GolfCartUsageReport = &Report{
	Code: "golf.golf_cart_usage", Name: "Golf Cart Usage Report", Module: "golf", Permission: "reporting.golf_cart_usage.view",
	Description: "Golf cart assignments with time out and back.",
	Columns: []Column{{Key: "playDate", Label: "Date", Type: "string"}, {Key: "golfCartCode", Label: "Golf Cart", Type: "string"}, {Key: "bookingCode", Label: "Booking", Type: "string"},
		{Key: "status", Label: "Status", Type: "string"}, {Key: "extra", Label: "Surcharge", Type: "boolean"}, {Key: "outAt", Label: "Out", Type: "datetime"},
		{Key: "returnedAt", Label: "Back", Type: "datetime"}, {Key: "minutesOut", Label: "Minutes", Type: "number"}},
	Params: append([]Param{{Key: "date", Label: "Date", Type: "date"}}, dateParams...),
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{where: []string{"status <> 'cancelled'"}}
		dateRange(x, "play_date", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT to_char(play_date, 'YYYY-MM-DD') AS "playDate", golf_cart_code AS "golfCartCode", booking_code AS "bookingCode", status, extra,
			out_at AS "outAt", returned_at AS "returnedAt", round(minutes_out)::float8 AS "minutesOut" FROM reporting.golf_cart_usage WHERE `+x.sql()+
			` ORDER BY play_date, golf_cart_code`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// RainCheckReport lists rain checks issued and used.
var RainCheckReport = &Report{
	Code: "golf.rain_checks", Name: "Rain Check Report", Module: "golf", Permission: "reporting.golf_rain_checks.view",
	Description: "Rain checks issued, redeemed and expired.",
	Columns: []Column{{Key: "number", Label: "Rain Check", Type: "string"}, {Key: "issuedOn", Label: "Issued", Type: "string"}, {Key: "bookingCode", Label: "Booking", Type: "string"},
		{Key: "playerName", Label: "Player", Type: "string"}, {Key: "holesPlayed", Label: "Holes Played", Type: "number"}, {Key: "creditAmount", Label: "Credit", Type: "number"},
		{Key: "expiresOn", Label: "Expires", Type: "string"}, {Key: "status", Label: "Status", Type: "string"}},
	Params: dateParams,
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{}
		dateRange(x, "issued_on", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT number, to_char(issued_on, 'YYYY-MM-DD') AS "issuedOn", booking_code AS "bookingCode", player_name AS "playerName",
			holes_played AS "holesPlayed", credit_amount::float8 AS "creditAmount", to_char(expires_on, 'YYYY-MM-DD') AS "expiresOn", status
			FROM reporting.golf_rain_checks WHERE `+x.sql()+` ORDER BY issued_on, number`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// GolfRevenueReport sums revenue per all-in component and segment.
var GolfRevenueReport = &Report{
	Code: "billing.golf_revenue", Name: "Golf Revenue Report", Module: "billing", Permission: "reporting.golf_revenue.view",
	Description: "Golf revenue per all-in component (caddy fee held as liability) and player segment.",
	Columns: []Column{{Key: "componentName", Label: "Component", Type: "string"}, {Key: "segment", Label: "Segment", Type: "string"},
		{Key: "liability", Label: "Liability", Type: "boolean"}, {Key: "lines", Label: "Lines", Type: "number"}, {Key: "amount", Label: "Net Amount", Type: "number"}},
	Params: dateParams,
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{where: []string{"source_type = 'golf_booking'"}}
		dateRange(x, "posted_at::date", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT component_name AS "componentName", coalesce(segment, '-') AS segment, liability, count(*)::int8 AS lines, sum(amount)::float8 AS amount
			FROM reporting.folio_components WHERE `+x.sql()+` GROUP BY 1, 2, 3 ORDER BY 1, 2`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// DailyPaymentReport sums payments per method for a day.
var DailyPaymentReport = &Report{
	Code: "billing.daily_payments", Name: "Daily Payment Report", Module: "billing", Permission: "reporting.daily_payments.view",
	Description: "Completed payments per method and staff.",
	Columns: []Column{{Key: "methodType", Label: "Method", Type: "string"}, {Key: "receivedBy", Label: "Staff", Type: "string"}, {Key: "count", Label: "Payments", Type: "number"},
		{Key: "amount", Label: "Amount", Type: "number"}, {Key: "refunded", Label: "Refunded", Type: "number"}},
	Params: append([]Param{{Key: "date", Label: "Date", Type: "date"}}, dateParams...),
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{where: []string{"status IN ('completed', 'refunded')"}}
		dateRange(x, "paid_at::date", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT method_type AS "methodType", coalesce(received_by, CASE channel WHEN 'online' THEN 'Online (gateway)' ELSE 'Member charge' END) AS "receivedBy",
			count(*)::int8 AS count, sum(amount)::float8 AS amount, sum(refunded_amount)::float8 AS refunded FROM reporting.payments WHERE `+x.sql()+
			` GROUP BY 1, 2 ORDER BY 1, 2`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// RefundReport lists refunds.
var RefundReport = &Report{
	Code: "billing.refunds", Name: "Refund Report", Module: "billing", Permission: "reporting.refunds.view",
	Description: "Refunds with destination, approval status and reason.",
	Columns: []Column{{Key: "number", Label: "Refund", Type: "string"}, {Key: "createdAt", Label: "Requested", Type: "datetime"}, {Key: "paymentNumber", Label: "Payment", Type: "string"},
		{Key: "amount", Label: "Amount", Type: "number"}, {Key: "destination", Label: "Destination", Type: "string"}, {Key: "status", Label: "Status", Type: "string"},
		{Key: "reason", Label: "Reason", Type: "string"}},
	Params: dateParams,
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{}
		dateRange(x, "created_at::date", p)
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT number, created_at AS "createdAt", payment_number AS "paymentNumber", amount::float8 AS amount, destination, status, reason
			FROM reporting.refunds WHERE `+x.sql()+` ORDER BY created_at`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// OutstandingMemberChargeReport lists member accounts with a balance.
var OutstandingMemberChargeReport = &Report{
	Code: "billing.outstanding_member_charges", Name: "Outstanding Member Charge Report", Module: "billing", Permission: "reporting.outstanding_member_charges.view",
	Description: "Member accounts with an outstanding balance (signing bill).",
	Columns: []Column{{Key: "number", Label: "Account", Type: "string"}, {Key: "holderName", Label: "Member", Type: "string"}, {Key: "balance", Label: "Balance", Type: "number"},
		{Key: "creditLimit", Label: "Credit Limit", Type: "number"}, {Key: "lastActivity", Label: "Last Activity", Type: "datetime"}},
	Params: []Param{{Key: "propertyId", Label: "Property", Type: "uuid"}},
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		x := &q{where: []string{"account_type = 'member'", "balance > 0"}}
		propertyFilter(x, p)
		rows, err := tx.Query(ctx, `SELECT number, holder_name AS "holderName", balance::float8 AS balance, credit_limit::float8 AS "creditLimit", last_activity AS "lastActivity"
			FROM reporting.member_accounts WHERE `+x.sql()+` ORDER BY balance DESC`+limitClause(limit), x.args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

func membershipReport(code, name, perm, desc string, where string, params []Param, extra func(x *q, p map[string]string)) *Report {
	return &Report{
		Code: code, Name: name, Module: "membership", Permission: perm, Description: desc,
		Columns: []Column{{Key: "memberNo", Label: "Member No.", Type: "string"}, {Key: "memberName", Label: "Member", Type: "string"}, {Key: "typeName", Label: "Type", Type: "string"},
			{Key: "role", Label: "Role", Type: "string"}, {Key: "status", Label: "Status", Type: "string"}, {Key: "startsOn", Label: "Starts", Type: "string"},
			{Key: "endsOn", Label: "Ends", Type: "string"}},
		Params: params,
		Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
			x := &q{where: []string{where}}
			propertyFilter(x, p)
			if extra != nil {
				extra(x, p)
			}
			rows, err := tx.Query(ctx, `SELECT member_no AS "memberNo", member_name AS "memberName", type_name AS "typeName", role, status,
				to_char(starts_on, 'YYYY-MM-DD') AS "startsOn", to_char(ends_on, 'YYYY-MM-DD') AS "endsOn" FROM reporting.memberships WHERE `+x.sql()+
				` ORDER BY ends_on NULLS LAST, member_name`+limitClause(limit), x.args...)
			if err != nil {
				return nil, err
			}
			return collect(rows)
		},
	}
}

var (
	ActiveMembersReport = membershipReport("membership.active_members", "Active Members Report", "reporting.active_members.view",
		"Active memberships with type and validity.", "status = 'active'", []Param{{Key: "propertyId", Label: "Property", Type: "uuid"}}, nil)
	NewMembersReport = membershipReport("membership.new_members", "New Members Report", "reporting.new_members.view",
		"Memberships activated in a period.", "activated_at IS NOT NULL", dateParams, func(x *q, p map[string]string) { dateRange(x, "activated_at::date", p) })
	ExpiringMembershipReport = membershipReport("membership.expiring", "Expiring Membership Report", "reporting.expiring_memberships.view",
		"Active principal memberships ending within N days (renewal list).", "status = 'active' AND role = 'principal'",
		[]Param{{Key: "days", Label: "Within days", Type: "string"}, {Key: "propertyId", Label: "Property", Type: "uuid"}},
		func(x *q, p map[string]string) {
			days := 30
			if v, err := strconv.Atoi(p["days"]); err == nil && v > 0 {
				days = v
			}
			x.add("ends_on <= current_date + ?::int", days)
		})
)

// P1Reports are the reports added by the Golf Core MVP.
var P1Reports = []*Report{DailyTeeSheetReport, GolfBookingReport, NoShowCancellationReport, CaddyAssignmentReport, GolfCartUsageReport, RainCheckReport,
	GolfRevenueReport, DailyPaymentReport, RefundReport, OutstandingMemberChargeReport, ActiveMembersReport, NewMembersReport, ExpiringMembershipReport}

// ── golf operational dashboard (FR-RPT-01) ────────────────────────────────

// GolfExecutive returns the golf KPIs of the Executive Overview: utilization,
// revenue of the day and the member vs guest split.
func GolfExecutive(ctx context.Context, tx pgx.Tx, day time.Time) ([]Widget, error) {
	d := day.Format("2006-01-02")
	var booked, capacity, members, guests int64
	var revenue float64
	err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM reporting.golf_players WHERE play_date = $1::date),
		(SELECT coalesce(sum(capacity), 0) FROM reporting.golf_tee_times WHERE play_date = $1::date AND status = 'open'),
		(SELECT count(*) FROM reporting.golf_players WHERE play_date = $1::date AND player_type = 'member'),
		(SELECT count(*) FROM reporting.golf_players WHERE play_date = $1::date AND player_type <> 'member'),
		(SELECT coalesce(sum(amount), 0)::float8 FROM reporting.folio_components WHERE source_type = 'golf_booking' AND NOT liability AND posted_at::date = $1::date)`, d).
		Scan(&booked, &capacity, &members, &guests, &revenue)
	if err != nil {
		return nil, err
	}
	var util int64
	if capacity > 0 {
		util = booked * 100 / capacity
	}
	rev := int64(revenue)
	var memberPct int64
	if members+guests > 0 {
		memberPct = members * 100 / (members + guests)
	}
	return []Widget{
		{Key: "tee_time_utilization", Label: "Tee Time Utilization", Module: "golf", Status: "available", Value: &util, Phase: "P1"},
		{Key: "golf_revenue", Label: "Golf Revenue", Module: "golf", Status: "available", Value: &rev, Phase: "P1"},
		{Key: "member_vs_guest", Label: "Member vs Guest", Module: "golf", Status: "available", Value: &memberPct, Phase: "P1"},
	}, nil
}

// GolfToday is the golf widget set of the Executive Overview.
type GolfToday struct {
	Date        string    `json:"date"`
	GeneratedAt time.Time `json:"generatedAt"`
	Widgets     []Widget  `json:"widgets"`
}

// GolfTodayValues computes the KPI values (Naming Convention §23 labels).
func GolfTodayValues(ctx context.Context, tx pgx.Tx, property *uuid.UUID, day time.Time, course *uuid.UUID) ([]Widget, error) {
	d := day.Format("2006-01-02")
	var bookings, players, queue, onCourse, pending, availCaddies, caddiesOnRound, cartsReady, cartsInUse, cartsMaint int64
	var avgMin *float64
	err := tx.QueryRow(ctx, `SELECT
		(SELECT count(DISTINCT booking_id) FROM reporting.golf_players WHERE play_date = $1::date AND ($2::uuid IS NULL OR property_id = $2) AND ($3::uuid IS NULL OR course_id = $3)),
		(SELECT count(*) FROM reporting.golf_players WHERE play_date = $1::date AND ($2::uuid IS NULL OR property_id = $2) AND ($3::uuid IS NULL OR course_id = $3)),
		(SELECT count(*) FROM reporting.golf_starter_queue WHERE play_date = $1::date AND status = 'waiting' AND ($2::uuid IS NULL OR property_id = $2) AND ($3::uuid IS NULL OR course_id = $3)),
		(SELECT count(*) FROM reporting.golf_players WHERE play_date = $1::date AND flight_status = 'in_play' AND ($2::uuid IS NULL OR property_id = $2) AND ($3::uuid IS NULL OR course_id = $3)),
		(SELECT count(*) FROM reporting.golf_players WHERE play_date = $1::date AND player_status = 'booked' AND booking_status = 'confirmed' AND ($2::uuid IS NULL OR property_id = $2) AND ($3::uuid IS NULL OR course_id = $3)),
		(SELECT count(*) FROM reporting.golf_caddy_attendance WHERE work_date = $1::date AND status = 'present' AND NOT engaged AND ($2::uuid IS NULL OR property_id = $2)),
		(SELECT count(DISTINCT caddy_id) FROM reporting.golf_caddy_assignments WHERE play_date = $1::date AND status = 'in_play' AND ($2::uuid IS NULL OR property_id = $2)),
		(SELECT count(*) FROM reporting.golf_carts WHERE status = 'active' AND readiness = 'ready' AND ($2::uuid IS NULL OR property_id = $2)),
		(SELECT count(*) FROM reporting.golf_carts WHERE status = 'active' AND readiness = 'in_use' AND ($2::uuid IS NULL OR property_id = $2)),
		(SELECT count(*) FROM reporting.golf_carts WHERE status = 'active' AND readiness IN ('maintenance', 'out_of_service') AND ($2::uuid IS NULL OR property_id = $2)),
		(SELECT avg(extract(epoch FROM tee_off_at - first_in) / 60)::float8 FROM (SELECT tee_off_at, min(checked_in_at) AS first_in FROM reporting.golf_players
			WHERE play_date = $1::date AND tee_off_at IS NOT NULL AND checked_in_at IS NOT NULL AND ($2::uuid IS NULL OR property_id = $2) AND ($3::uuid IS NULL OR course_id = $3)
			GROUP BY flight_id, tee_off_at) f)`, d, property, course).
		Scan(&bookings, &players, &queue, &onCourse, &pending, &availCaddies, &caddiesOnRound, &cartsReady, &cartsInUse, &cartsMaint, &avgMin)
	if err != nil {
		return nil, err
	}
	w := func(key, label string, v int64) Widget {
		val := v
		return Widget{Key: key, Label: label, Module: "golf", Status: "available", Value: &val, Phase: "P1"}
	}
	var avg int64
	if avgMin != nil {
		avg = int64(*avgMin + 0.5)
	}
	return []Widget{
		w("todays_bookings", "Today's Bookings", bookings), w("todays_players", "Today's Players", players), w("current_queue", "Current Queue", queue),
		w("players_on_course", "Players on Course", onCourse), w("pending_check_in", "Pending Check-in", pending),
		w("available_caddies", "Available Caddies", availCaddies), w("caddies_on_round", "Caddies on Round", caddiesOnRound),
		w("golf_carts_ready", "Golf Carts Ready", cartsReady), w("golf_carts_in_use", "Golf Carts in Use", cartsInUse),
		w("golf_carts_in_maintenance", "Golf Carts in Maintenance", cartsMaint), w("avg_check_in_to_tee_off", "Average Check-in to Tee-Off Time", avg),
	}, nil
}

func (s *Service) golfToday(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var prop *uuid.UUID
	if p, ok := reqctx.Property(ctx); ok {
		prop = &p
	}
	day := clock.Now()
	loc := time.UTC
	if s.Location != nil {
		loc = s.Location()
	}
	day = day.In(loc)
	if v := r.URL.Query().Get("date"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("invalid_date", "date must be YYYY-MM-DD"))
			return
		}
		day = t
	}
	var course *uuid.UUID
	if v := r.URL.Query().Get("courseId"); v != "" {
		u, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("invalid_course", "courseId must be a UUID"))
			return
		}
		course = &u
	}
	out := GolfToday{Date: day.Format("2006-01-02"), GeneratedAt: time.Now().UTC()}
	err := s.DB.WithReportTx(ctx, func(tx pgx.Tx) error {
		var err error
		out.Widgets, err = GolfTodayValues(ctx, tx, prop, day, course)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Service) registerP1(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/dashboards/golf-today", Module: "reporting", Tag: "Reports",
		Summary: "Golf operational dashboard of today (Executive Overview widgets)", Permission: "reporting.golf_dashboard.view",
		Response: GolfToday{}, Query: []route.Param{{Name: "date"}, {Name: "courseId"}}, Handler: s.golfToday})
}

// P1Permissions are the report permissions of P1.
func P1Permissions() ([]catalog.Permission, map[string][]string) {
	perms := []catalog.Permission{{Code: "reporting.golf_dashboard.view", Description: "Golf operational dashboard"}}
	golfReports, finReports, memReports := []string{}, []string{}, []string{}
	for _, r := range P1Reports {
		perms = append(perms, catalog.Permission{Code: r.Permission, Description: r.Name})
		switch r.Module {
		case "golf":
			golfReports = append(golfReports, r.Permission)
		case "billing":
			finReports = append(finReports, r.Permission)
		default:
			memReports = append(memReports, r.Permission)
		}
	}
	all := append(append(append([]string{"reporting.golf_dashboard.view"}, golfReports...), finReports...), memReports...)
	golf := append([]string{"reporting.golf_dashboard.view", "reporting.report.view"}, golfReports...)
	fin := append([]string{"reporting.report.view", "reporting.export.create"}, finReports...)
	mem := append([]string{"reporting.report.view", "reporting.export.create"}, memReports...)
	return perms, map[string][]string{
		"property_admin": all, "general_manager": all, "club_manager": all,
		"finance_manager":    append(append([]string{"reporting.golf_dashboard.view"}, finReports...), "reporting.golf_bookings.view", "reporting.golf_no_show_cancellation.view"),
		"accountant":         fin,
		"golf_manager":       append(golf, "reporting.golf_revenue.view", "reporting.export.create"),
		"golf_admin":         golf,
		"starter_marshal":    {"reporting.golf_dashboard.view"},
		"caddy_manager":      {"reporting.golf_dashboard.view", "reporting.report.view", "reporting.golf_caddy_assignments.view"},
		"reservation_staff":  {"reporting.report.view", "reporting.golf_daily_tee_sheet.view", "reporting.golf_bookings.view"},
		"front_desk":         {"reporting.report.view", "reporting.golf_daily_tee_sheet.view", "reporting.daily_payments.view"},
		"membership_manager": mem,
		"membership_admin":   {"reporting.report.view", "reporting.active_members.view", "reporting.expiring_memberships.view"},
	}
}
