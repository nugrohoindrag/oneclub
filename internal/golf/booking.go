package golf

// EP-04 Golf Booking and EP-05 Flight & Player:
//
//	Availability → Tee Hold → Player Validation → Pricing Resolution → Pricing Snapshot
//	→ Payment Policy → Booking Confirmation → Tee Sheet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/docno"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/membership"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
	"oneclub/internal/reservation"
)

// Domain events (PRD §10).
const (
	EventBookingConfirmed   = "golf.booking_confirmed"
	EventBookingCancelled   = "golf.booking_cancelled"
	EventBookingRescheduled = "golf.booking_rescheduled"
	EventBookingNoShow      = "golf.booking_no_show"
	EventPlayerCheckedIn    = "golf.player_checked_in"
	EventFlightReady        = "golf.flight_ready"
	EventFlightTeedOff      = "golf.flight_teed_off"
	EventRoundFinished      = "golf.round_finished"
)

// Approval document types owned by golf.
var (
	PriceOverrideType = provision.DocumentType{Code: "price_override", Module: "golf", Name: "Price Override",
		Attributes: []provision.DocumentAttribute{{Key: "discountPercent", Label: "Discount (%)", Type: "number"}, {Key: "amount", Label: "Discount amount", Type: "number"}}}
	CancellationWaiverType = provision.DocumentType{Code: "cancellation_waiver", Module: "golf", Name: "Cancellation Fee Waiver",
		Attributes: []provision.DocumentAttribute{{Key: "fee", Label: "Fee", Type: "number"}}}
)

var hundred = decimal.NewFromInt(100)

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func decPositive(s string) bool { return dec(s).IsPositive() }

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// ── inputs ────────────────────────────────────────────────────────────────

// PlayerInput is one player of a booking.
type PlayerInput struct {
	PlayerType             string     `json:"playerType" enum:"member,guest_of_member,reciprocal,non_member"`
	MemberID               *uuid.UUID `json:"memberId,omitempty"`
	MemberNo               string     `json:"memberNo,omitempty"`
	CustomerID             *uuid.UUID `json:"customerId,omitempty"`
	GuestID                *uuid.UUID `json:"guestId,omitempty"`
	Name                   string     `json:"name,omitempty"`
	Phone                  string     `json:"phone,omitempty"`
	Email                  string     `json:"email,omitempty"`
	HostIndex              *int       `json:"hostIndex,omitempty" doc:"Guest of Member: index of the host member in players (default: first member)"`
	TBA                    bool       `json:"tba,omitempty" doc:"To be announced; identity completed before check-in"`
	ReciprocalClub         string     `json:"reciprocalClub,omitempty"`
	ReciprocalLetterFileID *uuid.UUID `json:"reciprocalLetterFileId,omitempty"`
	PriceOverride          string     `json:"priceOverride,omitempty" doc:"Manual unit price (permission + approval above the Pricing Policy limit)"`
	OverrideReason         string     `json:"overrideReason,omitempty"`
	FlightIndex            int        `json:"flightIndex,omitempty" doc:"Group bookings: flight (0-based) of this player"`
}

// BookingRequest creates a booking (FR-BKG-03).
type BookingRequest struct {
	HoldID             *uuid.UUID    `json:"holdId,omitempty" doc:"Draft booking created by a tee hold"`
	TeeTimeID          *uuid.UUID    `json:"teeTimeId,omitempty" doc:"Slot when booking without a hold (Back Office, walk-in)"`
	GroupTeeTimeIDs    []uuid.UUID   `json:"groupTeeTimeIds,omitempty" doc:"Group booking: consecutive slots, one flight each"`
	BookingType        string        `json:"bookingType" enum:"member,guest,non_member,group,corporate,walk_in"`
	Channel            string        `json:"channel,omitempty" enum:"back_office,walk_in,member_app,website,import"`
	CustomerID         *uuid.UUID    `json:"customerId,omitempty" doc:"Person responsible (penanggung jawab)"`
	MemberID           *uuid.UUID    `json:"memberId,omitempty"`
	CorporateAccountID *uuid.UUID    `json:"corporateAccountId,omitempty"`
	ContactName        string        `json:"contactName,omitempty"`
	ContactPhone       string        `json:"contactPhone,omitempty"`
	ContactEmail       string        `json:"contactEmail,omitempty"`
	Players            []PlayerInput `json:"players"`
	PlayingRouteID     *uuid.UUID    `json:"playingRouteId,omitempty"`
	CartRequest        *int          `json:"golfCartRequest,omitempty" doc:"Requested golf carts (above the sharing rule adds a surcharge)"`
	CaddyRequest       string        `json:"caddyRequest,omitempty"`
	PaymentMode        string        `json:"paymentMode,omitempty" enum:"prepaid,deposit,pay_at_venue,member_charge" doc:"Default: Payment Policy"`
	PaymentMethod      string        `json:"paymentMethod,omitempty" enum:"qris,virtual_account,card,payment_gateway" doc:"Online method for prepaid / deposit"`
	RainCheckIDs       []uuid.UUID   `json:"rainCheckIds,omitempty"`
	Notes              string        `json:"notes,omitempty"`
	Consent            bool          `json:"consent,omitempty"`
}

// ── booking view ──────────────────────────────────────────────────────────

// Player is the API view of a booking player.
type Player struct {
	ID                 uuid.UUID       `json:"id"`
	FlightID           uuid.UUID       `json:"flightId"`
	Seq                int             `json:"seq"`
	PlayerType         string          `json:"playerType"`
	Segment            string          `json:"segment"`
	CustomerID         *uuid.UUID      `json:"customerId"`
	GuestID            *uuid.UUID      `json:"guestId"`
	MemberID           *uuid.UUID      `json:"memberId"`
	HostPlayerID       *uuid.UUID      `json:"hostPlayerId"`
	Name               string          `json:"name"`
	Phone              *string         `json:"phone"`
	TBA                bool            `json:"tba"`
	ReciprocalClub     *string         `json:"reciprocalClub"`
	ReciprocalVerified bool            `json:"reciprocalVerified"`
	Eligibility        json.RawMessage `json:"eligibility"`
	Entitlement        json.RawMessage `json:"entitlement"`
	PricingSnapshotID  *uuid.UUID      `json:"pricingSnapshotId"`
	PriceTotal         *string         `json:"priceTotal"`
	HandicapIndex      *string         `json:"handicapIndex"`
	Status             string          `json:"status" enum:"booked,checked_in,no_show,cancelled,removed"`
	CheckedInAt        *time.Time      `json:"checkedInAt"`
	CheckInMethod      *string         `json:"checkInMethod"`
}

// BookingFlight is a flight of a booking.
type BookingFlight struct {
	ID        uuid.UUID  `json:"id"`
	TeeTimeID uuid.UUID  `json:"teeTimeId"`
	StartAt   time.Time  `json:"startAt"`
	StartTee  int        `json:"startTee"`
	FlightNo  int        `json:"flightNo"`
	Status    string     `json:"status"`
	ReadyAt   *time.Time `json:"readyAt"`
	TeeOffAt  *time.Time `json:"teeOffAt"`
	FinishAt  *time.Time `json:"roundFinishAt"`
}

// Booking is the API view of a booking.
type Booking struct {
	ID                 uuid.UUID        `json:"id"`
	Code               string           `json:"code"`
	BookingType        string           `json:"bookingType" enum:"member,guest,non_member,group,corporate,walk_in"`
	Channel            string           `json:"channel" enum:"member_app,website,back_office,walk_in,import"`
	Status             string           `json:"status" enum:"draft,pending,confirmed,checked_in,completed,cancelled,no_show"`
	CourseID           uuid.UUID        `json:"courseId"`
	CourseName         string           `json:"courseName"`
	TeeTimeID          uuid.UUID        `json:"teeTimeId"`
	PlayDate           string           `json:"playDate"`
	StartAt            time.Time        `json:"startAt"`
	LocalTime          string           `json:"localTime"`
	PlayingRouteID     *uuid.UUID       `json:"playingRouteId"`
	PlayerCount        int              `json:"playerCount"`
	CustomerID         *uuid.UUID       `json:"customerId"`
	GuestID            *uuid.UUID       `json:"guestId"`
	MemberID           *uuid.UUID       `json:"memberId"`
	CorporateAccountID *uuid.UUID       `json:"corporateAccountId"`
	ContactName        string           `json:"contactName"`
	ContactPhone       *string          `json:"contactPhone"`
	ContactEmail       *string          `json:"contactEmail"`
	HoldExpiresAt      *time.Time       `json:"holdExpiresAt"`
	PaymentMode        *string          `json:"paymentMode"`
	PaymentDueAt       *time.Time       `json:"paymentDueAt"`
	DepositAmount      *string          `json:"depositAmount"`
	FolioID            *uuid.UUID       `json:"folioId"`
	Folio              *billing.Summary `json:"folio"`
	PolicyVersions     json.RawMessage  `json:"policyVersions"`
	QRToken            string           `json:"qrToken" doc:"Encode as oneclub:booking:<token> in the confirmation QR"`
	RescheduleCount    int              `json:"rescheduleCount"`
	CartRequest        *int             `json:"golfCartRequest"`
	CaddyRequest       *string          `json:"caddyRequest"`
	Notes              *string          `json:"notes"`
	ConfirmedAt        *time.Time       `json:"confirmedAt"`
	CheckedInAt        *time.Time       `json:"checkedInAt"`
	CompletedAt        *time.Time       `json:"completedAt"`
	CheckedOutAt       *time.Time       `json:"checkedOutAt" doc:"Golfer Check-out: settled and left the club"`
	CancelledAt        *time.Time       `json:"cancelledAt"`
	CancelReason       *string          `json:"cancelReason"`
	NoShowAt           *time.Time       `json:"noShowAt"`
	Flights            []BookingFlight  `json:"flights"`
	Players            []Player         `json:"players"`
	Payment            *billing.Payment `json:"payment" doc:"Latest pending online payment (checkout)"`
	ManageToken        string           `json:"manageToken,omitempty" doc:"Returned once to website guests (manage link)"`
	CreatedAt          time.Time        `json:"createdAt"`
	PackageBookingID   *uuid.UUID       `json:"packageBookingId" doc:"Commercial package booking this tee time fulfils (PRD P3 FR-PKG-04)"`
	PackageComponentID *uuid.UUID       `json:"packageComponentId"`
}

const bookingCols = `b.id, b.code, b.booking_type, b.channel, b.status, b.course_id, c.name, b.tee_time_id, b.play_date, b.start_at, b.playing_route_id,
	b.player_count, b.customer_id, b.guest_id, b.member_id, b.corporate_account_id, b.contact_name, b.contact_phone, b.contact_email, b.hold_expires_at,
	b.payment_mode, b.payment_due_at, b.deposit_amount::text, b.folio_id, b.policy_versions, b.qr_token, b.reschedule_count, b.cart_request,
	b.caddy_request, b.notes, b.confirmed_at, b.checked_in_at, b.completed_at, b.checked_out_at, b.cancelled_at, b.cancel_reason, b.no_show_at, b.created_at,
	b.package_booking_id, b.package_component_id
	FROM golf.bookings b JOIN golf.courses c ON c.id = b.course_id`

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	var day time.Time
	err := row.Scan(&b.ID, &b.Code, &b.BookingType, &b.Channel, &b.Status, &b.CourseID, &b.CourseName, &b.TeeTimeID, &day, &b.StartAt, &b.PlayingRouteID,
		&b.PlayerCount, &b.CustomerID, &b.GuestID, &b.MemberID, &b.CorporateAccountID, &b.ContactName, &b.ContactPhone, &b.ContactEmail, &b.HoldExpiresAt,
		&b.PaymentMode, &b.PaymentDueAt, &b.DepositAmount, &b.FolioID, &b.PolicyVersions, &b.QRToken, &b.RescheduleCount, &b.CartRequest,
		&b.CaddyRequest, &b.Notes, &b.ConfirmedAt, &b.CheckedInAt, &b.CompletedAt, &b.CheckedOutAt, &b.CancelledAt, &b.CancelReason, &b.NoShowAt, &b.CreatedAt,
		&b.PackageBookingID, &b.PackageComponentID)
	b.PlayDate = day.Format("2006-01-02")
	return b, err
}

// GetBooking loads a booking with flights, players and folio summary.
func GetBooking(ctx context.Context, q dbtx.Querier, bid uuid.UUID) (Booking, error) {
	b, err := scanBooking(q.QueryRow(ctx, `SELECT `+bookingCols+` WHERE b.id = $1`, bid))
	if dbtx.IsNoRows(err) {
		return b, errs.NotFound("booking")
	}
	if err != nil {
		return b, err
	}
	var pid uuid.UUID
	_ = q.QueryRow(ctx, `SELECT property_id FROM golf.bookings WHERE id = $1`, bid).Scan(&pid)
	b.LocalTime = b.StartAt.In(location(ctx, q, pid)).Format("15:04")
	rows, err := q.Query(ctx, `SELECT f.id, f.tee_time_id, t.start_at, t.start_tee, f.flight_no, f.status, f.ready_at, f.tee_off_at, f.round_finish_at
		FROM golf.flights f JOIN golf.tee_times t ON t.id = f.tee_time_id WHERE f.booking_id = $1 ORDER BY t.start_at, f.flight_no`, bid)
	if err != nil {
		return b, err
	}
	b.Flights = []BookingFlight{}
	for rows.Next() {
		var f BookingFlight
		if err := rows.Scan(&f.ID, &f.TeeTimeID, &f.StartAt, &f.StartTee, &f.FlightNo, &f.Status, &f.ReadyAt, &f.TeeOffAt, &f.FinishAt); err != nil {
			rows.Close()
			return b, err
		}
		b.Flights = append(b.Flights, f)
	}
	rows.Close()
	pr, err := q.Query(ctx, `SELECT id, flight_id, seq, player_type, segment, customer_id, guest_id, member_id, host_player_id, name, phone, tba, reciprocal_club,
		reciprocal_verified, eligibility, entitlement, pricing_snapshot_id, price_total::text, handicap_index::text, status, checked_in_at, check_in_method
		FROM golf.booking_players WHERE booking_id = $1 ORDER BY seq`, bid)
	if err != nil {
		return b, err
	}
	b.Players = []Player{}
	for pr.Next() {
		var p Player
		if err := pr.Scan(&p.ID, &p.FlightID, &p.Seq, &p.PlayerType, &p.Segment, &p.CustomerID, &p.GuestID, &p.MemberID, &p.HostPlayerID, &p.Name, &p.Phone,
			&p.TBA, &p.ReciprocalClub, &p.ReciprocalVerified, &p.Eligibility, &p.Entitlement, &p.PricingSnapshotID, &p.PriceTotal, &p.HandicapIndex,
			&p.Status, &p.CheckedInAt, &p.CheckInMethod); err != nil {
			pr.Close()
			return b, err
		}
		b.Players = append(b.Players, p)
	}
	pr.Close()
	if b.FolioID != nil {
		s, err := billing.FolioSummary(ctx, q, *b.FolioID)
		if err != nil {
			return b, err
		}
		b.Folio = &s
		if p, err := billing.PendingOnline(ctx, q, *b.FolioID); err == nil {
			b.Payment = p
		}
	}
	return b, nil
}

func addHistory(ctx context.Context, tx pgx.Tx, property, bookingID uuid.UUID, event string, from, to any, reason string) error {
	var f, t []byte
	if from != nil {
		f, _ = json.Marshal(from)
	}
	if to != nil {
		t, _ = json.Marshal(to)
	}
	_, err := tx.Exec(ctx, `INSERT INTO golf.booking_history (id, property_id, booking_id, event, from_value, to_value, reason, actor_id, actor_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, bookingID, event, f, t, nullStr(reason), id.Ptr(actor(ctx)), actorName(ctx))
	return err
}

// ── slot locking & seats ──────────────────────────────────────────────────

type slotRow struct {
	ID          uuid.UUID
	CourseID    uuid.UUID
	CourseCode  string
	VenueID     uuid.UUID
	PlayDate    time.Time
	StartAt     time.Time
	Tee         int
	Session     string
	DayType     string
	Interval    int
	Flights     int
	MinP, MaxP  int
	Capacity    int
	RouteID     *uuid.UUID
	Peak        bool
	MemberOnly  bool
	Status      string
	CourseState string
	Weather     string
}

// lockSlot locks the tee time row: concurrent holds on the same slot are
// serialised; the EXCLUDE constraint on allocations is the final guard.
func lockSlot(ctx context.Context, tx pgx.Tx, property, teeTimeID uuid.UUID) (slotRow, error) {
	var s slotRow
	err := tx.QueryRow(ctx, `SELECT t.id, t.course_id, c.code, c.venue_id, t.play_date, t.start_at, t.start_tee, t.session, t.day_type_code, t.interval_minutes,
		t.flights, t.min_players, t.max_players, t.capacity, t.playing_route_id, t.peak, t.member_only, t.status,
		coalesce(cs.course_state, 'open'), coalesce(cs.weather, 'normal')
		FROM golf.tee_times t JOIN golf.courses c ON c.id = t.course_id LEFT JOIN golf.course_status cs ON cs.course_id = t.course_id
		WHERE t.id = $1 AND t.property_id = $2 FOR UPDATE OF t`, teeTimeID, property).
		Scan(&s.ID, &s.CourseID, &s.CourseCode, &s.VenueID, &s.PlayDate, &s.StartAt, &s.Tee, &s.Session, &s.DayType, &s.Interval, &s.Flights, &s.MinP,
			&s.MaxP, &s.Capacity, &s.RouteID, &s.Peak, &s.MemberOnly, &s.Status, &s.CourseState, &s.Weather)
	if dbtx.IsNoRows(err) {
		return s, errs.NotFound("tee time")
	}
	return s, err
}

func (s slotRow) period() (time.Time, time.Time) {
	return s.StartAt, s.StartAt.Add(time.Duration(s.Interval) * time.Minute)
}

func reservationEnsureSeat(ctx context.Context, tx pgx.Tx, property uuid.UUID, courseCode string, venueID uuid.UUID, tee, flight, seat int) (uuid.UUID, error) {
	return reservation.EnsureResource(ctx, tx, property, SeatCode(courseCode, tee, flight, seat), fmt.Sprintf("%s tee %d flight %d seat %d", courseCode, tee, flight, seat),
		"golf_seat", &venueID, map[string]any{"courseCode": courseCode, "tee": tee, "flight": flight, "seat": seat})
}

func (m *Module) seatIDs(ctx context.Context, tx pgx.Tx, property uuid.UUID, s slotRow) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for f := 1; f <= s.Flights; f++ {
		for seat := 1; seat <= s.MaxP; seat++ {
			rid, err := reservationEnsureSeat(ctx, tx, property, s.CourseCode, s.VenueID, s.Tee, f, seat)
			if err != nil {
				return nil, err
			}
			out = append(out, rid)
		}
	}
	return out, nil
}

// ErrSlotFull is returned when the slot has no capacity left (FR-BKG-02).
var ErrSlotFull = errs.Conflict("slot_full", "this tee time does not have enough free places")

// allocateSeats locks n seats of a slot for a booking (held with expiry or
// confirmed).
func (m *Module) allocateSeats(ctx context.Context, tx pgx.Tx, property uuid.UUID, s slotRow, bookingID uuid.UUID, n int, holdUntil *time.Time) ([]uuid.UUID, error) {
	seats, err := m.seatIDs(ctx, tx, property, s)
	if err != nil {
		return nil, err
	}
	if err := reservation.ReleaseExpiredOn(ctx, tx, seats); err != nil {
		return nil, err
	}
	start, end := s.period()
	busy, err := reservation.Busy(ctx, tx, seats, start, end)
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, seat := range seats {
		if len(out) == n {
			break
		}
		if busy[seat] {
			continue
		}
		var a reservation.Allocation
		if holdUntil != nil {
			a, err = reservation.Hold(ctx, tx, property, seat, bookingID, start, end, time.Until(*holdUntil))
		} else {
			a, err = reservation.Confirm(ctx, tx, property, seat, bookingID, start, end)
		}
		if err == reservation.ErrSlotTaken {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, a.ID)
	}
	if len(out) < n {
		return nil, ErrSlotFull
	}
	return out, nil
}

// ── holds (FR-BKG-01) ─────────────────────────────────────────────────────

// HoldRequest places a tee hold.
type HoldRequest struct {
	TeeTimeID    uuid.UUID `json:"teeTimeId"`
	Players      int       `json:"players"`
	Channel      string    `json:"channel,omitempty" enum:"back_office,member_app,website,walk_in"`
	CaptchaToken string    `json:"captchaToken,omitempty" doc:"Website: CAPTCHA token (FR-WEB-07)"`
}

// Hold is a placed tee hold.
type Hold struct {
	ID        uuid.UUID `json:"id" doc:"Draft booking id; pass as holdId when booking"`
	Code      string    `json:"code"`
	TeeTimeID uuid.UUID `json:"teeTimeId"`
	StartAt   time.Time `json:"startAt"`
	Players   int       `json:"players"`
	ExpiresAt time.Time `json:"expiresAt"`
	HoldToken string    `json:"holdToken,omitempty" doc:"Website: secret to continue this hold"`
}

func (m *Module) bookingCode(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) (string, error) {
	return docno.Daily(ctx, tx, "golf.sequences", property, "BK", day)
}

// checkSlotRules enforces slot status, course state, cut-off, booking window
// and member-only / night rules for a channel and player types.
func checkSlotRules(s slotRow, pol Policies, channel string, playerTypes []string, now time.Time, staffOverride bool) error {
	if s.Status == "blocked" {
		return errs.Conflict("slot_blocked", "this tee time is blocked")
	}
	if s.Status == "closed" {
		return errs.Conflict("slot_closed", "this tee time is no longer offered")
	}
	if s.CourseState == "closed" {
		return errs.Conflict("course_closed", "the course is closed")
	}
	cut := pol.Golf.BookingCutoffMinutes[channel]
	if !staffOverride && s.StartAt.Add(-time.Duration(cut)*time.Minute).Before(now) {
		return errs.Conflict("booking_cutoff", fmt.Sprintf("online booking closes %d minutes before the tee time", cut))
	}
	if staffOverride && s.StartAt.Add(time.Duration(s.Interval)*time.Minute).Before(now) {
		return errs.Conflict("tee_time_past", "this tee time has passed")
	}
	if !staffOverride {
		days := int(s.PlayDate.Sub(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
		window := 0
		for _, t := range playerTypes {
			w := pol.Golf.BookingWindowDays[t]
			if window == 0 || w < window {
				window = w // the most restrictive player decides (Member Priority)
			}
		}
		if window > 0 && days > window {
			return errs.Conflict("outside_booking_window", fmt.Sprintf("this date opens for booking %d days in advance", window))
		}
	}
	if s.MemberOnly {
		for _, t := range playerTypes {
			if t != "member" && t != "guest_of_member" {
				return errs.Conflict("member_only", "this tee time is reserved for members and their guests")
			}
		}
	}
	return nil
}

// PlaceHold creates a draft booking holding n seats until the hold expires.
func (m *Module) PlaceHold(ctx context.Context, tx pgx.Tx, property uuid.UUID, req HoldRequest, playerTypes []string, staff bool) (Hold, error) {
	if req.Players <= 0 {
		return Hold{}, errs.Validation("invalid_players", "players must be at least 1", errs.Field("players", "invalid", "1 or more"))
	}
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return Hold{}, err
	}
	pol = withTierWindow(pol, tierBonus(ctx, tx, tierCustomer(ctx))) // PRD P5 tier benefit
	if req.Channel == "" {
		req.Channel = "back_office"
	}
	s, err := lockSlot(ctx, tx, property, req.TeeTimeID)
	if err != nil {
		return Hold{}, err
	}
	if len(playerTypes) == 0 {
		playerTypes = []string{"non_member"}
	}
	if err := checkSlotRules(s, pol, req.Channel, playerTypes, clock.Now(), staff); err != nil {
		return Hold{}, err
	}
	maxP := min(pol.Golf.MaxPlayers, s.MaxP)
	if req.Players > maxP {
		return Hold{}, errs.Validation("too_many_players", fmt.Sprintf("at most %d players per booking", maxP), errs.Field("players", "too_large", fmt.Sprintf("≤ %d", maxP)))
	}
	bid := id.New()
	expires := clock.Now().Add(time.Duration(pol.Golf.HoldMinutes) * time.Minute)
	if _, err := m.allocateSeats(ctx, tx, property, s, bid, req.Players, &expires); err != nil {
		return Hold{}, err
	}
	code, err := m.bookingCode(ctx, tx, property, s.PlayDate)
	if err != nil {
		return Hold{}, err
	}
	token := secret.RandomToken(24)
	bt := "non_member"
	if req.Channel == "member_app" {
		bt = "member"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.bookings (id, property_id, code, booking_type, channel, status, course_id, tee_time_id, play_date, start_at,
		playing_route_id, player_count, contact_name, hold_expires_at, qr_token, manage_token_hash, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,'draft',$6,$7,$8,$9,$10,$11,'Tee Hold',$12,$13,$14,$15,$15)`,
		bid, property, code, bt, req.Channel, s.CourseID, s.ID, s.PlayDate.Format("2006-01-02"), s.StartAt, s.RouteID, req.Players, expires,
		secret.RandomToken(18), secret.HashToken(token), id.Ptr(actor(ctx))); err != nil {
		return Hold{}, err
	}
	if err := addHistory(ctx, tx, property, bid, "hold", nil, map[string]any{"teeTimeId": s.ID, "players": req.Players, "expiresAt": expires}, ""); err != nil {
		return Hold{}, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "hold", s.CourseID, s.PlayDate, map[string]any{"teeTimeId": s.ID.String()}); err != nil {
		return Hold{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "hold", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: code,
		PropertyID: &property, After: map[string]any{"teeTimeId": s.ID, "players": req.Players, "expiresAt": expires, "channel": req.Channel}}); err != nil {
		return Hold{}, err
	}
	return Hold{ID: bid, Code: code, TeeTimeID: s.ID, StartAt: s.StartAt, Players: req.Players, ExpiresAt: expires, HoldToken: token}, nil
}

// ReleaseHold cancels a draft booking and frees its seats.
func (m *Module) ReleaseHold(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, reason string) error {
	var status string
	var course uuid.UUID
	var day time.Time
	err := tx.QueryRow(ctx, `SELECT status, course_id, play_date FROM golf.bookings WHERE id = $1 AND property_id = $2 FOR UPDATE`, bid, property).Scan(&status, &course, &day)
	if dbtx.IsNoRows(err) {
		return errs.NotFound("hold")
	}
	if err != nil {
		return err
	}
	if status != "draft" {
		return errs.Conflict("not_a_hold", "only a tee hold (draft booking) can be released")
	}
	if err := reservation.Release(ctx, tx, bid, "released"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2 WHERE id = $1`, bid, reason); err != nil {
		return err
	}
	if err := addHistory(ctx, tx, property, bid, "hold_released", map[string]any{"status": "draft"}, map[string]any{"status": "cancelled"}, reason); err != nil {
		return err
	}
	return notifyRealtime(ctx, tx, property, "golf.tee_sheet", "hold_released", course, day, map[string]any{"bookingId": bid.String()})
}

// ── player validation, eligibility, entitlement (EP-05) ───────────────────

type resolvedPlayer struct {
	in          PlayerInput
	playerType  string
	customerID  *uuid.UUID
	guestID     *uuid.UUID
	memberID    *uuid.UUID
	membership  *uuid.UUID
	name        string
	phone       string
	segments    []string
	eligibility map[string]any
	entitlement map[string]any
	hostIndex   int
	handicap    *string
	accountCust *uuid.UUID
}

func (m *Module) resolvePlayers(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, s slotRow, pol Policies, players []PlayerInput, staff bool) ([]resolvedPlayer, error) {
	out := make([]resolvedPlayer, len(players))
	guestsByHost := map[int]int{}
	for i, in := range players {
		rp := resolvedPlayer{in: in, playerType: in.PlayerType, hostIndex: -1, eligibility: map[string]any{}, entitlement: map[string]any{}}
		field := fmt.Sprintf("players[%d]", i)
		switch in.PlayerType {
		case "member":
			mid := in.MemberID
			var err error
			if mid == nil && in.MemberNo != "" {
				mid, err = membership.MemberByNumber(ctx, tx, property, in.MemberNo)
			} else if mid == nil && in.CustomerID != nil {
				mid, err = membership.MemberByCustomer(ctx, tx, *in.CustomerID)
			}
			if err != nil {
				return nil, err
			}
			if mid == nil {
				return nil, errs.Validation("member_not_found", "member not found", errs.Field(field, "member_not_found", "member not found"))
			}
			st, err := membership.Standing(ctx, tx, *mid, day)
			if err != nil {
				return nil, err
			}
			if !st.Active() {
				e := errs.Validation("member_not_active", fmt.Sprintf("membership of %s (%s) is %s on %s: book as a guest / non-member or renew the membership",
					st.Name, st.MemberNo, titleCase(st.Status), day.Format("02 Jan 2006")),
					errs.Field(field, "member_not_active", "membership "+st.Status))
				return nil, e
			}
			if !st.Privileges.GolfAccess {
				return nil, errs.Validation("no_golf_access", st.Name+"'s membership type has no golf access", errs.Field(field, "no_golf_access", "no golf access"))
			}
			if !staff && st.Privileges.BookingWindowDays > 0 {
				now := clock.Now()
				days := int(day.Sub(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
				window := st.Privileges.BookingWindowDays + tierBonus(ctx, tx, st.CustomerID) // PRD P5 tier benefit
				if days > window {
					return nil, errs.Conflict("outside_booking_window", fmt.Sprintf("members of this type book up to %d days ahead", window))
				}
			}
			rp.memberID, rp.customerID, rp.membership, rp.name = mid, st.CustomerID, st.MembershipID, st.Name
			if st.Privileges.MemberRate {
				rp.segments = []string{"member"}
			} else {
				rp.segments = []string{"guest"}
			}
			rp.entitlement = map[string]any{"memberNo": st.MemberNo, "membershipType": st.TypeName, "memberRate": st.Privileges.MemberRate,
				"maxGuests": st.Privileges.MaxGuests, "bookingWindowDays": st.Privileges.BookingWindowDays, "validUntil": st.EndsOn}
			if holder, err := membership.AccountHolder(ctx, tx, *mid); err == nil {
				rp.accountCust = holder
			}
		case "guest_of_member", "reciprocal", "non_member":
			if in.PlayerType == "reciprocal" && strings.TrimSpace(in.ReciprocalClub) == "" && !in.TBA {
				return nil, errs.Validation("reciprocal_club_required", "home club of the reciprocal member is required", errs.Field(field, "required", "reciprocal club"))
			}
			switch {
			case in.CustomerID != nil:
				c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
				if err != nil {
					return nil, errs.Validation("customer_not_found", "customer not found", errs.Field(field, "not_found", "customer not found"))
				}
				rp.customerID, rp.name, rp.phone = &c.ID, c.Name, c.Phone
			case in.GuestID != nil:
				n, _, ph, err := crm.Contact(ctx, tx, nil, in.GuestID)
				if err != nil || n == "" {
					return nil, errs.Validation("guest_not_found", "guest not found", errs.Field(field, "not_found", "guest not found"))
				}
				rp.guestID, rp.name, rp.phone = in.GuestID, n, ph
			case in.TBA:
				if !pol.Golf.AllowTBA {
					return nil, errs.Validation("tba_not_allowed", "players must be named at booking", errs.Field(field, "required", "player name"))
				}
				rp.name = "TBA"
				if in.Name != "" {
					rp.name = in.Name
				}
			default:
				ref, err := crm.ResolveGuest(ctx, tx, property, crm.GuestInput{Name: in.Name, Phone: in.Phone, Email: in.Email})
				if err != nil {
					return nil, err
				}
				rp.customerID, rp.guestID, rp.name, rp.phone = ref.CustomerID, ref.GuestID, in.Name, ref.Phone
			}
			base := map[string][]string{"guest_of_member": {"guest_of_member", "guest"}, "reciprocal": {"reciprocal", "guest"}, "non_member": {"non_member", "guest"}}[in.PlayerType]
			rp.segments = append([]string{}, base...)
			// FR-FLT-04 eligibility for Senior / Ladies / Junior.
			if rp.customerID != nil {
				c, err := crm.GetCustomer(ctx, tx, *rp.customerID)
				if err == nil {
					age := c.Age(day)
					rp.eligibility["age"] = age
					rp.eligibility["gender"] = c.Gender
					rp.eligibility["resident"] = c.Resident
					okRes := !pol.Eligibility.ResidentOnly || c.Resident
					if okRes && age >= 0 && pol.Eligibility.SeniorMinAge > 0 && age >= pol.Eligibility.SeniorMinAge {
						rp.segments = append(rp.segments, "senior")
					}
					if okRes && age >= 0 && pol.Eligibility.JuniorMaxAge > 0 && age <= pol.Eligibility.JuniorMaxAge {
						rp.segments = append(rp.segments, "junior")
					}
					if okRes && c.Gender != "" && c.Gender == pol.Eligibility.LadiesGender {
						rp.segments = append(rp.segments, "ladies")
					}
				}
			}
			rp.eligibility["segments"] = rp.segments
		default:
			return nil, errs.Validation("invalid_player_type", "invalid player type", errs.Field(field, "invalid", "member, guest_of_member, reciprocal or non_member"))
		}
		if rp.customerID != nil && !in.TBA {
			var hcp *string
			_ = tx.QueryRow(ctx, `SELECT handicap_index::text FROM golf.handicaps WHERE customer_id = $1 ORDER BY effective_at DESC LIMIT 1`, *rp.customerID).Scan(&hcp)
			rp.handicap = hcp
		}
		out[i] = rp
	}
	// Guest of Member links and limits (FR-FLT-03, Guest Policy FR-POL-02).
	firstMember := -1
	for i, rp := range out {
		if rp.playerType == "member" {
			firstMember = i
			break
		}
	}
	for i := range out {
		if out[i].playerType != "guest_of_member" {
			continue
		}
		host := firstMember
		if out[i].in.HostIndex != nil {
			host = *out[i].in.HostIndex
		}
		if host < 0 || host >= len(out) || out[host].playerType != "member" {
			if pol.Guest.MemberMustPlay {
				return nil, errs.Validation("host_required", "a Guest of Member must play with the hosting member in the same booking",
					errs.Field(fmt.Sprintf("players[%d]", i), "host_required", "add the hosting member"))
			}
			host = -1
		}
		out[i].hostIndex = host
		if host >= 0 {
			guestsByHost[host]++
			maxG := pol.Guest.MaxGuestsPerMember
			if mg, ok := out[host].entitlement["maxGuests"].(int); ok && (maxG == 0 || mg < maxG) {
				maxG = mg
			}
			if maxG >= 0 && guestsByHost[host] > maxG {
				return nil, errs.Validation("too_many_guests", fmt.Sprintf("%s may bring at most %d guests", out[host].name, maxG),
					errs.Field(fmt.Sprintf("players[%d]", i), "too_many_guests", "guest limit"))
			}
			out[i].entitlement = map[string]any{"hostMember": out[host].entitlement["memberNo"], "guestOfMemberRate": true}
		}
		if len(pol.Guest.AllowedWeekdays) > 0 {
			wd := int(day.Weekday())
			if wd == 0 {
				wd = 7
			}
			ok := false
			for _, d := range pol.Guest.AllowedWeekdays {
				ok = ok || d == wd
			}
			if !ok {
				return nil, errs.Conflict("guests_not_allowed_day", "guests are not allowed on this day (Guest Policy)")
			}
		}
		lt := s.StartAt.In(location(ctx, tx, property)).Format("15:04")
		if (pol.Guest.AllowedFrom != "" && lt < pol.Guest.AllowedFrom) || (pol.Guest.AllowedTo != "" && lt > pol.Guest.AllowedTo) {
			return nil, errs.Conflict("guests_not_allowed_time", "guests are not allowed at this time (Guest Policy)")
		}
	}
	return out, nil
}

// ── pricing per player (EP-07, FR-FLT-05) ─────────────────────────────────

type pricedPlayer struct {
	result     commercial.PriceResult
	snapshotID uuid.UUID
	override   *commercial.Override
}

func (m *Module) pricePlayer(ctx context.Context, tx pgx.Tx, property uuid.UUID, s slotRow, routeID *uuid.UUID, channel string, rp resolvedPlayer,
	bookingID uuid.UUID, loc *time.Location) (pricedPlayer, *approvalNeed, error) {
	peak := s.Peak
	q := commercial.PriceQuery{Property: property, Segments: rp.segments, PlayAt: s.StartAt, PlayDate: s.PlayDate, LocalTime: s.StartAt.In(loc).Format("15:04"),
		Session: s.Session, DayTypeCode: s.DayType, PlayingRouteID: routeID, Channel: channel, Peak: &peak}
	res, err := commercial.Resolve(ctx, tx, q)
	if err != nil {
		return pricedPlayer{}, nil, err
	}
	var need *approvalNeed
	var ov *commercial.Override
	if strings.TrimSpace(rp.in.PriceOverride) != "" {
		unit, err := decimal.NewFromString(rp.in.PriceOverride)
		if err != nil || unit.IsNegative() {
			return pricedPlayer{}, nil, errs.Validation("invalid_override", "invalid override price", errs.Field("priceOverride", "invalid", "non-negative decimal"))
		}
		if strings.TrimSpace(rp.in.OverrideReason) == "" {
			return pricedPlayer{}, nil, errs.Validation("override_reason_required", "a reason is required for a price override", errs.Field("overrideReason", "required", "reason"))
		}
		p := property
		pr := authzFrom(ctx)
		if pr == nil || !pr.Can("commercial.price_override.apply", &p) {
			return pricedPlayer{}, nil, errs.Forbidden("missing permission commercial.price_override.apply")
		}
		list := dec(res.UnitPrice)
		pol, err := LoadPolicies(ctx, tx, property, clock.Now())
		if err != nil {
			return pricedPlayer{}, nil, err
		}
		discount := decimal.Zero
		if list.IsPositive() {
			discount = list.Sub(unit).Div(list).Mul(hundred)
		}
		ov = &commercial.Override{UnitPrice: unit, Reason: rp.in.OverrideReason, ListPrice: res.UnitPrice}
		if discount.GreaterThan(dec(pol.Override.MaxDiscountPercent)) && !pr.Can("commercial.price_override.approve_any", &p) {
			// above the limit: list price now, override after approval
			need = &approvalNeed{unit: unit, discount: discount, list: list, reason: rp.in.OverrideReason}
			ov = nil
		} else {
			uid := actor(ctx)
			ov.ApprovedBy = &uid
			if res, err = commercial.ApplyOverride(ctx, tx, property, res, unit, s.StartAt); err != nil {
				return pricedPlayer{}, nil, err
			}
		}
	}
	sid, err := commercial.Snapshot(ctx, tx, commercial.SnapshotInput{Query: q, Result: res, Override: ov,
		Context: map[string]any{"bookingId": bookingID, "playerType": rp.playerType, "eligibility": rp.eligibility}})
	if err != nil {
		return pricedPlayer{}, nil, err
	}
	return pricedPlayer{result: res, snapshotID: sid, override: ov}, need, nil
}

type approvalNeed struct {
	unit, discount, list decimal.Decimal
	reason               string
}

// postRound posts a player's round charge from the snapshot.
func (m *Module) postRound(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, playerID uuid.UUID, name string, pp pricedPlayer) (uuid.UUID, error) {
	r := pp.result
	return m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: folioID, ChargeType: "golf_round",
		Description: fmt.Sprintf("Golf round — %s (%s)", name, strings.ReplaceAll(r.Segment, "_", " ")), Quantity: decimal.NewFromInt(int64(r.Quantity)),
		UnitPrice: dec(r.UnitPrice), Net: dec(r.NetAmount), Tax: dec(r.TaxAmount), Service: dec(r.ServiceAmt), Total: dec(r.Total), Components: r.Components,
		SnapshotID: &pp.snapshotID, ReferenceType: "golf_player", ReferenceID: &playerID})
}

// optionalCharge posts caddy / cart fees priced separately (when the club's
// rate is not all-in) — FR-PRC-08.
func (m *Module) optionalCharge(ctx context.Context, tx pgx.Tx, property uuid.UUID, s slotRow, loc *time.Location, chargeType string, segments []string,
	qty int, folioID uuid.UUID, ref uuid.UUID, refType, desc string, channel string) (*commercial.PriceResult, error) {
	peak := s.Peak
	q := commercial.PriceQuery{Property: property, ChargeType: chargeType, Segments: segments, PlayAt: s.StartAt, PlayDate: s.PlayDate,
		LocalTime: s.StartAt.In(loc).Format("15:04"), Session: s.Session, DayTypeCode: s.DayType, Channel: channel, Peak: &peak, Quantity: qty}
	res, err := commercial.Resolve(ctx, tx, q)
	if err == commercial.ErrNoRate {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sid, err := commercial.Snapshot(ctx, tx, commercial.SnapshotInput{Query: q, Result: res, Context: map[string]any{"reference": ref}})
	if err != nil {
		return nil, err
	}
	if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: folioID, ChargeType: chargeType, Description: desc,
		Quantity: decimal.NewFromInt(int64(qty)), UnitPrice: dec(res.UnitPrice), Net: dec(res.NetAmount), Tax: dec(res.TaxAmount), Service: dec(res.ServiceAmt),
		Total: dec(res.Total), Components: res.Components, SnapshotID: &sid, ReferenceType: refType, ReferenceID: &ref, Liability: chargeType == "caddy_fee"}); err != nil {
		return nil, err
	}
	return &res, nil
}

// ── create booking ────────────────────────────────────────────────────────

type flightPlan struct {
	slot    slotRow
	players []int // indexes into resolved players
}

// CreateBooking runs the whole booking model in one transaction.
func (m *Module) CreateBooking(ctx context.Context, tx pgx.Tx, property uuid.UUID, req BookingRequest, staff bool) (Booking, error) {
	now := clock.Now()
	pol, err := LoadPolicies(ctx, tx, property, now)
	if err != nil {
		return Booking{}, err
	}
	pol = withTierWindow(pol, requestTierBonus(ctx, tx, req)) // PRD P5 tier benefit
	loc := location(ctx, tx, property)
	if req.Channel == "" {
		req.Channel = "back_office"
	}
	if req.BookingType == "" {
		req.BookingType = "non_member"
	}
	switch req.BookingType {
	case "member", "guest", "non_member", "group", "corporate", "walk_in":
	default:
		return Booking{}, errs.Validation("invalid_booking_type", "invalid booking type", errs.Field("bookingType", "invalid", "member, guest, non_member, group, corporate or walk_in"))
	}
	if req.BookingType == "walk_in" {
		req.Channel = "walk_in"
	}
	if len(req.Players) == 0 {
		return Booking{}, errs.Validation("players_required", "add at least one player", errs.Field("players", "required", "one or more players"))
	}
	var types []string
	for _, p := range req.Players {
		types = append(types, p.PlayerType)
	}
	// slots: hold, single slot or group slots
	var bookingID uuid.UUID
	var heldPlayers int
	var slotIDs []uuid.UUID
	fromHold := false
	if req.HoldID != nil {
		var status, channel string
		var tt uuid.UUID
		var exp *time.Time
		err := tx.QueryRow(ctx, `SELECT status, tee_time_id, player_count, hold_expires_at, channel FROM golf.bookings WHERE id = $1 AND property_id = $2 FOR UPDATE`,
			*req.HoldID, property).Scan(&status, &tt, &heldPlayers, &exp, &channel)
		if dbtx.IsNoRows(err) {
			return Booking{}, errs.NotFound("tee hold")
		}
		if err != nil {
			return Booking{}, err
		}
		if status != "draft" || exp == nil || exp.Before(now) {
			return Booking{}, errs.Conflict("hold_expired", "the tee hold has expired; choose the tee time again")
		}
		bookingID, slotIDs, fromHold = *req.HoldID, []uuid.UUID{tt}, true
	} else {
		bookingID = id.New()
		switch {
		case len(req.GroupTeeTimeIDs) > 0:
			slotIDs = req.GroupTeeTimeIDs
			req.BookingType = map[bool]string{true: req.BookingType, false: "group"}[req.BookingType == "corporate"]
		case req.TeeTimeID != nil:
			slotIDs = []uuid.UUID{*req.TeeTimeID}
		default:
			return Booking{}, errs.Validation("tee_time_required", "choose a tee time", errs.Field("teeTimeId", "required", "tee time"))
		}
	}
	var slots []slotRow
	for i, sid := range slotIDs {
		s, err := lockSlot(ctx, tx, property, sid)
		if err != nil {
			return Booking{}, err
		}
		if !fromHold {
			if err := checkSlotRules(s, pol, req.Channel, types, now, staff); err != nil {
				return Booking{}, err
			}
		}
		if i > 0 && s.CourseID != slots[0].CourseID {
			return Booking{}, errs.Validation("group_course", "group flights must be on the same course")
		}
		slots = append(slots, s)
	}
	// distribute players to flights
	plans := make([]flightPlan, len(slots))
	for i := range slots {
		plans[i].slot = slots[i]
	}
	maxP := pol.Golf.MaxPlayers
	for i, p := range req.Players {
		fi := p.FlightIndex
		if len(slots) == 1 {
			fi = 0
		} else if fi == 0 && i >= maxP {
			fi = i / maxP
		}
		if fi < 0 || fi >= len(plans) {
			return Booking{}, errs.Validation("invalid_flight_index", "flight index out of range", errs.Field(fmt.Sprintf("players[%d].flightIndex", i), "invalid", "flight index"))
		}
		plans[fi].players = append(plans[fi].players, i)
	}
	for i, fp := range plans {
		limit := min(maxP, fp.slot.MaxP)
		if len(fp.players) > limit {
			return Booking{}, errs.Validation("too_many_players", fmt.Sprintf("at most %d players per flight", limit), errs.Field("players", "too_large", fmt.Sprintf("≤ %d per flight", limit)))
		}
		minP := fp.slot.MinP
		if fp.slot.Session == "night" && pol.Golf.NightMinPlayers > minP {
			minP = pol.Golf.NightMinPlayers
		}
		if len(fp.players) < minP {
			return Booking{}, errs.Validation("too_few_players", fmt.Sprintf("flight %d needs at least %d players", i+1, minP),
				errs.Field("players", "too_few", fmt.Sprintf("≥ %d players", minP)))
		}
	}
	first := slots[0]
	// no-show block for online channels (No-show Policy, FR-BKG-10)
	resolved, err := m.resolvePlayers(ctx, tx, property, first.PlayDate, first, pol, req.Players, staff)
	if err != nil {
		return Booking{}, err
	}
	// booker / person responsible
	customerID, memberID := req.CustomerID, req.MemberID
	if memberID == nil {
		for _, rp := range resolved {
			if rp.playerType == "member" {
				memberID = rp.memberID
				if customerID == nil {
					customerID = rp.customerID
				}
				break
			}
		}
	}
	var guestID *uuid.UUID
	if customerID == nil {
		for _, rp := range resolved {
			if rp.customerID != nil || rp.guestID != nil {
				customerID, guestID = rp.customerID, rp.guestID
				break
			}
		}
	}
	if !staff && customerID != nil && pol.Cancellation.NoShowBlockAfter > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.bookings WHERE customer_id = $1 AND status = 'no_show' AND no_show_at > now() - make_interval(days => $2)`,
			*customerID, pol.Cancellation.NoShowBlockWindowDays).Scan(&n); err != nil {
			return Booking{}, err
		}
		if n >= pol.Cancellation.NoShowBlockAfter {
			return Booking{}, errs.Conflict("online_booking_blocked", "online booking is blocked after repeated no-shows; please contact the club")
		}
	}
	if req.BookingType == "corporate" {
		if req.CorporateAccountID == nil {
			return Booking{}, errs.Validation("corporate_required", "a corporate account is required", errs.Field("corporateAccountId", "required", "corporate account"))
		}
		if _, err := crm.CorporateName(ctx, tx, *req.CorporateAccountID); err != nil {
			return Booking{}, err
		}
	}
	contactName, contactPhone, contactEmail := req.ContactName, req.ContactPhone, req.ContactEmail
	if contactName == "" || (contactPhone == "" && contactEmail == "") {
		n, e, ph, _ := crm.Contact(ctx, tx, customerID, guestID)
		if contactName == "" {
			contactName = n
		}
		if contactPhone == "" {
			contactPhone = ph
		}
		if contactEmail == "" {
			contactEmail = e
		}
	}
	if contactName == "" {
		contactName = resolved[0].name
	}
	routeID := req.PlayingRouteID
	if routeID == nil {
		routeID = first.RouteID
	}
	policyRaw, _ := json.Marshal(pol.Versions)
	code := ""
	if fromHold {
		if err := tx.QueryRow(ctx, `SELECT code FROM golf.bookings WHERE id = $1`, bookingID).Scan(&code); err != nil {
			return Booking{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET booking_type = $2, customer_id = $3, guest_id = $4, member_id = $5, corporate_account_id = $6,
			contact_name = $7, contact_phone = $8, contact_email = $9, player_count = $10, playing_route_id = $11, policy_versions = $12, cart_request = $13,
			caddy_request = $14, notes = $15, updated_by = $16 WHERE id = $1`, bookingID, req.BookingType, customerID, guestID, memberID, req.CorporateAccountID,
			contactName, nullStr(contactPhone), nullStr(contactEmail), len(resolved), routeID, policyRaw, req.CartRequest, nullStr(req.CaddyRequest),
			nullStr(req.Notes), id.Ptr(actor(ctx))); err != nil {
			return Booking{}, err
		}
	} else {
		if code, err = m.bookingCode(ctx, tx, property, first.PlayDate); err != nil {
			return Booking{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.bookings (id, property_id, code, booking_type, channel, status, course_id, tee_time_id, play_date, start_at,
			playing_route_id, player_count, customer_id, guest_id, member_id, corporate_account_id, contact_name, contact_phone, contact_email, policy_versions,
			qr_token, cart_request, caddy_request, notes, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,'pending',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$24)`,
			bookingID, property, code, req.BookingType, req.Channel, first.CourseID, first.ID, first.PlayDate.Format("2006-01-02"), first.StartAt, routeID,
			len(resolved), customerID, guestID, memberID, req.CorporateAccountID, contactName, nullStr(contactPhone), nullStr(contactEmail), policyRaw,
			secret.RandomToken(18), req.CartRequest, nullStr(req.CaddyRequest), nullStr(req.Notes), id.Ptr(actor(ctx))); err != nil {
			return Booking{}, err
		}
	}
	// seats: holds keep their allocations; extra players get more seats
	var holdAllocations []uuid.UUID
	if fromHold {
		var err error
		if holdAllocations, err = reservation.HeldAllocations(ctx, tx, bookingID); err != nil {
			return Booking{}, err
		}
		if len(resolved) < len(holdAllocations) {
			if err := reservation.ReleaseAllocations(ctx, tx, holdAllocations[len(resolved):]); err != nil {
				return Booking{}, err
			}
			holdAllocations = holdAllocations[:len(resolved)]
		}
	}
	// folio (FR-PAY-01)
	folio, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: property, CustomerID: customerID, GuestID: guestID, HolderName: contactName,
		SourceType: "golf_booking", SourceID: &bookingID, SourceRef: code})
	if err != nil {
		return Booking{}, err
	}
	// flights, players, seats, prices
	var approvals []approvalNeed
	var approvalPlayers []uuid.UUID
	seq := 0
	playerIDs := make([]uuid.UUID, len(resolved))
	for fi, fp := range plans {
		fid := id.New()
		var fno int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(flight_no), 0) + 1 FROM golf.flights WHERE tee_time_id = $1`, fp.slot.ID).Scan(&fno); err != nil {
			return Booking{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.flights (id, property_id, tee_time_id, booking_id, course_id, play_date, flight_no, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'confirmed')`, fid, property, fp.slot.ID, bookingID, fp.slot.CourseID, fp.slot.PlayDate.Format("2006-01-02"), fno); err != nil {
			return Booking{}, err
		}
		var allocs []uuid.UUID
		if fromHold && fi == 0 {
			allocs = holdAllocations
		}
		if missing := len(fp.players) - len(allocs); missing > 0 {
			more, err := m.allocateSeats(ctx, tx, property, fp.slot, bookingID, missing, ptrTime(now.Add(time.Duration(pol.Golf.HoldMinutes)*time.Minute)))
			if err != nil {
				return Booking{}, err
			}
			allocs = append(allocs, more...)
		}
		for k, pi := range fp.players {
			rp := resolved[pi]
			seq++
			pid := id.New()
			playerIDs[pi] = pid
			elig, _ := json.Marshal(rp.eligibility)
			ent, _ := json.Marshal(rp.entitlement)
			var host *uuid.UUID
			if rp.hostIndex >= 0 && rp.hostIndex < pi {
				host = &playerIDs[rp.hostIndex]
			}
			if _, err := tx.Exec(ctx, `INSERT INTO golf.booking_players (id, property_id, booking_id, flight_id, seq, player_type, segment, customer_id, guest_id,
				member_id, membership_id, host_player_id, name, phone, tba, reciprocal_club, reciprocal_letter_file_id, eligibility, entitlement, allocation_id,
				handicap_index, status, created_by, updated_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21::numeric,'booked',$22,$22)`,
				pid, property, bookingID, fid, seq, rp.playerType, rp.segments[0], rp.customerID, rp.guestID, rp.memberID, rp.membership, host, rp.name,
				nullStr(rp.phone), rp.in.TBA, nullStr(rp.in.ReciprocalClub), rp.in.ReciprocalLetterFileID, elig, ent, allocs[k], rp.handicap, id.Ptr(actor(ctx))); err != nil {
				return Booking{}, err
			}
			pp, need, err := m.pricePlayer(ctx, tx, property, fp.slot, routeID, req.Channel, rp, bookingID, loc)
			if err != nil {
				return Booking{}, err
			}
			lid, err := m.postRound(ctx, tx, folio.ID, pid, rp.name, pp)
			if err != nil {
				return Booking{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET segment = $2, pricing_snapshot_id = $3, folio_line_id = $4, price_total = $5::numeric WHERE id = $1`,
				pid, pp.result.Segment, pp.snapshotID, lid, pp.result.Total); err != nil {
				return Booking{}, err
			}
			if need != nil {
				approvals = append(approvals, *need)
				approvalPlayers = append(approvalPlayers, pid)
			}
			// caddy fee priced separately when the club's rate is not all-in
			if _, err := m.optionalCharge(ctx, tx, property, fp.slot, loc, "caddy_fee", resolved[pi].segments, 1, folio.ID, pid, "golf_player",
				"Caddy fee — "+rp.name, req.Channel); err != nil {
				return Booking{}, err
			}
		}
		// golf carts: sharing rule and surcharge (FR-CRT-04/05)
		need := ceilDiv(len(fp.players), pol.Cart.PlayersPerCart)
		requested := need
		if req.CartRequest != nil && len(plans) == 1 {
			requested = *req.CartRequest
		}
		if requested > need {
			extra := requested - need
			if !pol.Cart.SingleRiderAllowed {
				return Booking{}, errs.Validation("single_rider_not_allowed", fmt.Sprintf("buggy sharing is mandatory: %d players per golf cart", pol.Cart.PlayersPerCart),
					errs.Field("golfCartRequest", "invalid", fmt.Sprintf("≤ %d golf carts", need)))
			}
			segs := resolved[fp.players[0]].segments
			if _, err := m.optionalCharge(ctx, tx, property, fp.slot, loc, "extra_cart", segs, extra, folio.ID, fid, "golf_flight",
				fmt.Sprintf("Golf cart surcharge × %d", extra), req.Channel); err != nil {
				return Booking{}, err
			}
		}
		if _, err := m.optionalCharge(ctx, tx, property, fp.slot, loc, "cart_fee", resolved[fp.players[0]].segments, need, folio.ID, fid, "golf_flight",
			fmt.Sprintf("Golf cart fee × %d", need), req.Channel); err != nil {
			return Booking{}, err
		}
	}
	// rain check redemption (FR-BKG-11)
	for _, rc := range req.RainCheckIDs {
		if err := m.redeemRainCheck(ctx, tx, property, rc, bookingID, folio.ID, customerID); err != nil {
			return Booking{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET folio_id = $2 WHERE id = $1`, bookingID, folio.ID); err != nil {
		return Booking{}, err
	}
	for i, n := range approvals {
		if err := m.submitOverride(ctx, tx, property, bookingID, code, approvalPlayers[i], n); err != nil {
			return Booking{}, err
		}
	}
	if err := addHistory(ctx, tx, property, bookingID, "created", nil, map[string]any{"code": code, "type": req.BookingType, "channel": req.Channel,
		"players": len(resolved), "flights": len(plans)}, ""); err != nil {
		return Booking{}, err
	}
	// payment policy → confirm or wait for payment
	if err := m.applyPaymentPolicy(ctx, tx, property, bookingID, folio.ID, pol, req, resolved, staff); err != nil {
		return Booking{}, err
	}
	b, err := GetBooking(ctx, tx, bookingID)
	if err != nil {
		return b, err
	}
	for _, fp := range plans {
		if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "booking", fp.slot.CourseID, fp.slot.PlayDate, map[string]any{"bookingId": bookingID.String(),
			"teeTimeId": fp.slot.ID.String()}); err != nil {
			return b, err
		}
	}
	return b, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionCreate, EntityType: "golf.booking", EntityID: bookingID.String(),
		EntityLabel: code, PropertyID: &property, After: map[string]any{"code": code, "status": b.Status, "type": b.BookingType, "channel": b.Channel,
			"players": b.PlayerCount, "startAt": b.StartAt, "total": b.Folio.Charges}})
}

func ptrTime(t time.Time) *time.Time { return &t }

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// applyPaymentPolicy decides the payment mode (FR-PAY-02) and confirms the
// booking when nothing must be paid first.
func (m *Module) applyPaymentPolicy(ctx context.Context, tx pgx.Tx, property, bookingID, folioID uuid.UUID, pol Policies, req BookingRequest,
	resolved []resolvedPlayer, staff bool) error {
	rule := pol.PaymentFor(req.Channel, req.BookingType)
	mode := rule.Mode
	if req.PaymentMode != "" {
		allowed := staff || req.PaymentMode == mode || (req.Channel == "member_app" && (req.PaymentMode == "prepaid" || req.PaymentMode == "member_charge"))
		if !allowed {
			return errs.Validation("payment_mode_not_allowed", "this payment option is not allowed by the Payment Policy", errs.Field("paymentMode", "invalid", mode))
		}
		mode = req.PaymentMode
	}
	sum, err := billing.FolioSummary(ctx, tx, folioID)
	if err != nil {
		return err
	}
	balance := dec(sum.Balance)
	due := clock.Now().Add(time.Duration(max(rule.DueMinutes, 15)) * time.Minute)
	switch mode {
	case "member_charge":
		var holder *uuid.UUID
		for _, rp := range resolved {
			if rp.playerType == "member" && rp.accountCust != nil {
				holder = rp.accountCust
				break
			}
		}
		if holder == nil {
			return errs.Validation("member_account_required", "member charge needs a member in the booking", errs.Field("paymentMode", "invalid", "member charge"))
		}
		acct, err := billing.AccountFor(ctx, tx, property, *holder, "member")
		if err != nil {
			return err
		}
		if acct == nil {
			a, err := m.Billing.EnsureAccount(ctx, tx, property, *holder, "member", nil)
			if err != nil {
				return err
			}
			acct = &a
		}
		if balance.IsPositive() {
			if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: &folioID, AccountID: &acct.ID, MethodType: "member_account",
				Amount: balance, Description: "Member charge"}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET payment_mode = 'member_charge' WHERE id = $1`, bookingID); err != nil {
			return err
		}
		return m.confirm(ctx, tx, property, bookingID, "member charge")
	case "prepaid", "deposit":
		amount := balance
		purpose := "settlement"
		if mode == "deposit" {
			pct := dec(rule.DepositPercent)
			if pct.IsZero() {
				pct = decimal.NewFromInt(30)
			}
			amount = balance.Mul(pct).Div(hundred).Round(0)
			purpose = "deposit"
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET payment_mode = $2, payment_due_at = $3, deposit_amount = CASE WHEN $2 = 'deposit' THEN $4::numeric END,
			status = 'pending' WHERE id = $1`, bookingID, mode, due, amount.String()); err != nil {
			return err
		}
		if err := reservation.ExtendHold(ctx, tx, bookingID, due); err != nil {
			return err
		}
		if !amount.IsPositive() {
			return m.confirm(ctx, tx, property, bookingID, "nothing to pay")
		}
		if req.Channel == "website" || req.Channel == "member_app" || req.PaymentMethod != "" {
			method := req.PaymentMethod
			if method == "" {
				method = "payment_gateway"
			}
			if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: &folioID, MethodType: method, Channel: "online", Purpose: purpose,
				Amount: amount, ExpiresAt: &due, Description: "Golf booking"}); err != nil {
				return err
			}
			// a sandbox gateway may complete immediately
			var st string
			if err := tx.QueryRow(ctx, `SELECT status FROM golf.bookings WHERE id = $1`, bookingID).Scan(&st); err != nil {
				return err
			}
			paid, held, err := billing.PaidTowards(ctx, tx, folioID)
			if err != nil {
				return err
			}
			if st == "pending" && (paid.GreaterThanOrEqual(balance) || (mode == "deposit" && held.GreaterThanOrEqual(amount))) {
				return m.confirm(ctx, tx, property, bookingID, "paid")
			}
		}
		b, err := GetBooking(ctx, tx, bookingID)
		if err != nil {
			return err
		}
		return m.notifyBooking(ctx, tx, property, b, "golf.booking_payment_pending", map[string]any{"dueAt": due.In(location(ctx, tx, property)).Format("02 Jan 15:04"),
			"payLink": m.payLink(b)})
	default:
		if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET payment_mode = 'pay_at_venue' WHERE id = $1`, bookingID); err != nil {
			return err
		}
		return m.confirm(ctx, tx, property, bookingID, "pay at venue")
	}
}

func (m *Module) payLink(b Booking) string {
	if b.Payment != nil && b.Payment.CheckoutURL != nil {
		return *b.Payment.CheckoutURL
	}
	return m.Cfg.WebsiteURL + "/en/book-golf/manage"
}

// confirm turns held seats into confirmed ones and confirms the booking
// (Booking Confirmation, FR-BKG-07).
func (m *Module) confirm(ctx context.Context, tx pgx.Tx, property, bookingID uuid.UUID, reason string) error {
	if _, err := reservation.ConfirmReservation(ctx, tx, bookingID); err != nil {
		return err
	}
	var code string
	err := tx.QueryRow(ctx, `UPDATE golf.bookings SET status = 'confirmed', confirmed_at = now(), hold_expires_at = NULL, payment_due_at = NULL
		WHERE id = $1 AND status IN ('draft', 'pending') RETURNING code`, bookingID).Scan(&code)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := addHistory(ctx, tx, property, bookingID, "confirmed", map[string]any{"status": "pending"}, map[string]any{"status": "confirmed"}, reason); err != nil {
		return err
	}
	b, err := GetBooking(ctx, tx, bookingID)
	if err != nil {
		return err
	}
	if _, err := m.Events.Publish(ctx, tx, EventBookingConfirmed, "golf.booking", &bookingID, &property, map[string]any{"bookingId": bookingID, "code": code,
		"startAt": b.StartAt, "players": b.PlayerCount, "customerId": b.CustomerID, "channel": b.Channel}); err != nil {
		return err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "booking_confirmed", b.CourseID, mustDay(b.PlayDate), map[string]any{"bookingId": bookingID.String()}); err != nil {
		return err
	}
	return m.notifyBooking(ctx, tx, property, b, "golf.booking_confirmed", nil)
}

func mustDay(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// notifyBooking sends a booking message to the booker (e-mail / WhatsApp,
// in-app for portal users).
func (m *Module) notifyBooking(ctx context.Context, tx pgx.Tx, property uuid.UUID, b Booking, event string, extra map[string]any) error {
	if m.Notify == nil {
		return nil
	}
	loc := location(ctx, tx, property)
	link := m.Cfg.MemberPortalURL + "/bookings/" + b.ID.String()
	if b.Channel == "website" {
		link = m.Cfg.WebsiteURL + "/id/book-golf/manage"
	}
	total := ""
	if b.Folio != nil {
		total = b.Folio.Currency + " " + b.Folio.Charges
	}
	data := map[string]any{"name": b.ContactName, "bookingCode": b.Code, "teeTime": b.StartAt.In(loc).Format("Mon 02 Jan 2006 15:04"), "course": b.CourseName,
		"players": b.PlayerCount, "total": total, "link": link, "qr": "oneclub:booking:" + b.QRToken}
	for k, v := range extra {
		data[k] = v
	}
	msg := notify.Message{Event: event, Category: "general", Data: data, Link: link, PropertyID: &property}
	var userID *uuid.UUID
	if b.CustomerID != nil {
		if c, err := crm.GetCustomer(ctx, tx, *b.CustomerID); err == nil {
			userID = c.UserID
		}
	}
	if userID != nil {
		msg.UserIDs = []uuid.UUID{*userID}
		msg.Channels = []string{notify.ChannelInApp, notify.ChannelEmail, notify.ChannelWhatsApp}
	} else {
		if b.ContactEmail != nil {
			msg.Email = *b.ContactEmail
			msg.Channels = append(msg.Channels, notify.ChannelEmail)
		}
		if b.ContactPhone != nil {
			msg.Phone = *b.ContactPhone
			msg.Channels = append(msg.Channels, notify.ChannelWhatsApp)
		}
		msg.Name = b.ContactName
		if len(msg.Channels) == 0 {
			return nil
		}
	}
	return m.Notify.Send(ctx, tx, msg)
}

// submitOverride sends a price override above the limit to approval.
func (m *Module) submitOverride(ctx context.Context, tx pgx.Tx, property, bookingID uuid.UUID, code string, playerID uuid.UUID, n approvalNeed) error {
	disc, _ := n.discount.Round(2).Float64()
	amt, _ := n.list.Sub(n.unit).Float64()
	payload, _ := json.Marshal(map[string]any{"playerId": playerID, "unitPrice": n.unit.String(), "reason": n.reason, "listPrice": n.list.String()})
	oid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.booking_history (id, property_id, booking_id, event, to_value, reason, actor_id, actor_name)
		VALUES ($1,$2,$3,'price_override_requested',$4,$5,$6,$7)`, oid, property, bookingID, payload, n.reason, id.Ptr(actor(ctx)), actorName(ctx)); err != nil {
		return err
	}
	_, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: PriceOverrideType.Code, DocumentID: oid, DocumentRef: code,
		Title: fmt.Sprintf("Price override %s — %.1f%% off", code, disc), PropertyID: property, Attributes: map[string]any{"discountPercent": disc, "amount": amt}})
	return err
}

// OverrideDecision re-prices the player when the override is approved.
func (m *Module) OverrideDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	if d.Status != approval.StatusApproved {
		return nil
	}
	var bookingID uuid.UUID
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT booking_id, to_value FROM golf.booking_history WHERE id = $1`, d.DocumentID).Scan(&bookingID, &raw); err != nil {
		return err
	}
	var p struct {
		PlayerID  uuid.UUID `json:"playerId"`
		UnitPrice string    `json:"unitPrice"`
		Reason    string    `json:"reason"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	return m.repricePlayer(ctx, tx, d.PropertyID, bookingID, p.PlayerID, &commercial.Override{UnitPrice: dec(p.UnitPrice), Reason: p.Reason,
		ApprovedBy: &d.DecidedBy, ApprovalID: &d.RequestID}, "price override approved")
}

// repricePlayer voids a player's round line and posts a new one (new
// snapshot; the old snapshot stays for history).
func (m *Module) repricePlayer(ctx context.Context, tx pgx.Tx, property, bookingID, playerID uuid.UUID, ov *commercial.Override, reason string) error {
	ctx = withProperty(ctx, property)
	var lineID *uuid.UUID
	var flight uuid.UUID
	var segment, name, channel string
	var folio *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT bp.folio_line_id, bp.flight_id, bp.segment, bp.name, b.channel, b.folio_id FROM golf.booking_players bp
		JOIN golf.bookings b ON b.id = bp.booking_id WHERE bp.id = $1 AND bp.booking_id = $2`, playerID, bookingID).Scan(&lineID, &flight, &segment, &name, &channel, &folio); err != nil {
		return err
	}
	var tt uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT tee_time_id FROM golf.flights WHERE id = $1`, flight).Scan(&tt); err != nil {
		return err
	}
	s, err := lockSlot(ctx, tx, property, tt)
	if err != nil {
		return err
	}
	loc := location(ctx, tx, property)
	peak := s.Peak
	q := commercial.PriceQuery{Property: property, Segments: []string{segment}, PlayAt: s.StartAt, PlayDate: s.PlayDate, LocalTime: s.StartAt.In(loc).Format("15:04"),
		Session: s.Session, DayTypeCode: s.DayType, PlayingRouteID: s.RouteID, Channel: channel, Peak: &peak}
	res, err := commercial.Resolve(ctx, tx, q)
	if err != nil {
		return err
	}
	if ov != nil {
		ov.ListPrice = res.UnitPrice
		if res, err = commercial.ApplyOverride(ctx, tx, property, res, ov.UnitPrice, s.StartAt); err != nil {
			return err
		}
	}
	sid, err := commercial.Snapshot(ctx, tx, commercial.SnapshotInput{Query: q, Result: res, Override: ov, Context: map[string]any{"bookingId": bookingID, "reason": reason}})
	if err != nil {
		return err
	}
	if lineID != nil {
		if err := m.Billing.VoidCharge(ctx, tx, *lineID, reason); err != nil {
			return err
		}
	}
	if folio == nil {
		return nil
	}
	lid, err := m.postRound(ctx, tx, *folio, playerID, name, pricedPlayer{result: res, snapshotID: sid})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET pricing_snapshot_id = $2, folio_line_id = $3, price_total = $4::numeric WHERE id = $1`,
		playerID, sid, lid, res.Total); err != nil {
		return err
	}
	return addHistory(ctx, tx, property, bookingID, "price_changed", nil, map[string]any{"playerId": playerID, "total": res.Total, "snapshotId": sid}, reason)
}

// SetLegacyRef stores the Rhapsody booking reference of a migrated booking
// (EP-18 FR-MIG-07) so the import can be re-run and reconciled.
func SetLegacyRef(ctx context.Context, tx pgx.Tx, bookingID uuid.UUID, ref string) error {
	_, err := tx.Exec(ctx, `UPDATE golf.bookings SET legacy_ref = $2 WHERE id = $1`, bookingID, ref)
	return err
}

// BookingByLegacyRef returns the booking migrated with ref, if any.
func BookingByLegacyRef(ctx context.Context, q dbtx.Querier, property uuid.UUID, ref string) (*uuid.UUID, error) {
	var out uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM golf.bookings WHERE property_id = $1 AND legacy_ref = $2`, property, ref).Scan(&out)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
