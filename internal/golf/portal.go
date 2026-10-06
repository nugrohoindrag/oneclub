package golf

// Customer channels: Member Portal (EP-13) and the public website booking
// flow for non-members (EP-14).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/integration"
)

func billingSummary(ctx context.Context, q dbtx.Querier, f uuid.UUID) (billing.Summary, error) {
	return billing.FolioSummary(ctx, q, f)
}

// ── Member Portal ─────────────────────────────────────────────────────────

type memberCtx struct {
	memberID   uuid.UUID
	customerID uuid.UUID
	property   uuid.UUID
	standing   membership.Info
}

// asMember resolves the logged-in member and narrows the request to the
// member's property.
func (m *Module) asMember(ctx context.Context, tx pgx.Tx) (context.Context, memberCtx, error) {
	var mc memberCtx
	p := authzFrom(ctx)
	if p == nil {
		return ctx, mc, errs.Unauthorized("authentication required")
	}
	c, err := crm.CustomerByUser(ctx, tx, p.UserID)
	if err != nil {
		return ctx, mc, err
	}
	mid, err := membership.MemberByUser(ctx, tx, p.UserID)
	if err != nil {
		return ctx, mc, err
	}
	if mid == nil {
		return ctx, mc, errs.Forbidden("no membership is linked to this account")
	}
	mc.memberID, mc.customerID, mc.property = *mid, c.ID, c.PropertyID
	ctx = reqctx.WithProperty(ctx, c.PropertyID)
	mc.standing, err = membership.Standing(ctx, tx, *mid, clock.Now())
	return ctx, mc, err
}

func (m *Module) memberRead(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error)) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		ctx, mc, err := m.asMember(ctx, tx)
		if err != nil {
			return nil, err
		}
		return fn(ctx, tx, mc)
	})
}

func (m *Module) memberWrite(w http.ResponseWriter, r *http.Request, status int, fn func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error)) {
	m.write(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		ctx, mc, err := m.asMember(ctx, tx)
		if err != nil {
			return nil, err
		}
		return fn(ctx, tx, mc)
	})
}

// CourseInfo is a course for customer channels.
type CourseInfo struct {
	ID           uuid.UUID    `json:"id"`
	Code         string       `json:"code"`
	Name         string       `json:"name"`
	Holes        int          `json:"holes"`
	LengthMeters *int         `json:"lengthMeters"`
	Par          *int         `json:"par"`
	Description  *string      `json:"description"`
	Guide        *string      `json:"guide"`
	Routes       []RouteHoles `json:"routes"`
	TeeSets      []TeeSetInfo `json:"teeSets"`
	Assets       []AssetInfo  `json:"assets"`
}

type TeeSetInfo struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Color        *string `json:"color"`
	CourseRating *string `json:"courseRating"`
	Slope        *int    `json:"slope"`
}

type AssetInfo struct {
	Code      string     `json:"code"`
	AssetType string     `json:"assetType"`
	Name      string     `json:"name"`
	HoleID    *uuid.UUID `json:"holeId"`
	FileURL   *string    `json:"fileUrl"`
}

func courseInfos(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]CourseInfo, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, holes, length_meters, par, description, guide FROM golf.courses
		WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, property)
	if err != nil {
		return nil, err
	}
	out := []CourseInfo{}
	for rows.Next() {
		var c CourseInfo
		if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.Holes, &c.LengthMeters, &c.Par, &c.Description, &c.Guide); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	for i := range out {
		c := &out[i]
		c.Routes, c.TeeSets, c.Assets = []RouteHoles{}, []TeeSetInfo{}, []AssetInfo{}
		rr, err := q.Query(ctx, `SELECT id FROM golf.playing_routes WHERE course_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY is_default DESC, code`, c.ID)
		if err != nil {
			return nil, err
		}
		var ids []uuid.UUID
		for rr.Next() {
			var x uuid.UUID
			if err := rr.Scan(&x); err != nil {
				rr.Close()
				return nil, err
			}
			ids = append(ids, x)
		}
		rr.Close()
		for _, x := range ids {
			rh, err := LoadRouteHoles(ctx, q, x)
			if err != nil {
				return nil, err
			}
			c.Routes = append(c.Routes, rh)
		}
		tr, err := q.Query(ctx, `SELECT code, name, color, course_rating::text, slope FROM golf.tee_sets WHERE course_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY sequence`, c.ID)
		if err != nil {
			return nil, err
		}
		for tr.Next() {
			var t TeeSetInfo
			if err := tr.Scan(&t.Code, &t.Name, &t.Color, &t.CourseRating, &t.Slope); err != nil {
				tr.Close()
				return nil, err
			}
			c.TeeSets = append(c.TeeSets, t)
		}
		tr.Close()
		ar, err := q.Query(ctx, `SELECT code, asset_type, name, hole_id, file_id FROM golf.course_assets WHERE course_id = $1 AND status = 'active' AND archived_at IS NULL
			ORDER BY asset_type, code`, c.ID)
		if err != nil {
			return nil, err
		}
		for ar.Next() {
			var a AssetInfo
			var fid *uuid.UUID
			if err := ar.Scan(&a.Code, &a.AssetType, &a.Name, &a.HoleID, &fid); err != nil {
				ar.Close()
				return nil, err
			}
			if fid != nil {
				u := "/api/v1/files/" + fid.String()
				a.FileURL = &u
			}
			c.Assets = append(c.Assets, a)
		}
		ar.Close()
	}
	return out, nil
}

func (m *Module) memberCourses(w http.ResponseWriter, r *http.Request) {
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		out, err := courseInfos(ctx, tx, mc.property)
		return httpx.Page[CourseInfo]{Items: out}, err
	})
}

func (m *Module) memberAvailability(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		var course *uuid.UUID
		if v := q.Get("courseId"); v != "" {
			u, err := uuid.Parse(v)
			if err != nil {
				return nil, errs.BadRequest("invalid_course", "courseId must be a UUID")
			}
			course = &u
		}
		segs := []string{"member", "guest_of_member"}
		if !mc.standing.Privileges.MemberRate {
			segs = []string{"guest", "guest_of_member"}
		}
		out, err := m.availability(ctx, tx, mc.property, day, course, q.Get("session"), 0, segs, "member_app")
		return httpx.Page[AvailableSlot]{Items: out}, err
	})
}

func (m *Module) memberHold(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[HoldRequest](w, r)
	if !ok {
		return
	}
	req.Channel = "member_app"
	m.memberWrite(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if !mc.standing.Active() {
			return nil, errs.Conflict("member_not_active", "your membership is "+mc.standing.Status+"; renew it to book with your member privileges")
		}
		h, err := m.PlaceHold(withTierCustomer(ctx, mc.customerID), tx, mc.property, req, []string{"member"}, false)
		h.HoldToken = ""
		if err == nil {
			if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET member_id = $2, customer_id = $3 WHERE id = $1`, h.ID, mc.memberID, mc.customerID); err != nil {
				return nil, err
			}
		}
		return h, err
	})
}

func (m *Module) memberOwnsBooking(ctx context.Context, tx pgx.Tx, mc memberCtx, bid uuid.UUID) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.bookings b WHERE b.id = $1 AND (b.member_id = $2 OR b.customer_id = $3
		OR EXISTS (SELECT 1 FROM golf.booking_players p WHERE p.booking_id = b.id AND (p.member_id = $2 OR p.customer_id = $3))))`, bid, mc.memberID, mc.customerID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errs.NotFound("booking")
	}
	return nil
}

func (m *Module) memberReleaseHold(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.memberWrite(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if err := m.memberOwnsBooking(ctx, tx, mc, bid); err != nil {
			return nil, err
		}
		if err := m.ReleaseHold(ctx, tx, mc.property, bid, "released by member"); err != nil {
			return nil, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "release_hold", EntityType: "golf.booking", EntityID: bid.String(), PropertyID: &mc.property}); err != nil {
			return nil, err
		}
		return GetBooking(ctx, tx, bid)
	})
}

func (m *Module) memberBook(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[BookingRequest](w, r)
	if !ok {
		return
	}
	m.memberWrite(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if req.HoldID == nil {
			return nil, errs.Validation("hold_required", "choose a tee time first (tee hold)", errs.Field("holdId", "required", "tee hold"))
		}
		if err := m.memberOwnsBooking(ctx, tx, mc, *req.HoldID); err != nil {
			return nil, err
		}
		req.Channel, req.BookingType, req.MemberID, req.CustomerID = "member_app", "member", &mc.memberID, &mc.customerID
		req.GroupTeeTimeIDs, req.TeeTimeID, req.CorporateAccountID = nil, nil, nil
		if len(req.Players) == 0 {
			req.Players = []PlayerInput{{PlayerType: "member", MemberID: &mc.memberID}}
		}
		// the member must be in the booking (FR-APP-03: diri sendiri, family, other members, guests)
		ownNo, _ := membership.MemberNo(ctx, tx, mc.memberID)
		self := false
		for i, p := range req.Players {
			if p.PlayerType == "member" && p.MemberID == nil && ownNo != "" && strings.EqualFold(strings.TrimSpace(p.MemberNo), ownNo) {
				req.Players[i].MemberID, req.Players[i].MemberNo = &mc.memberID, ""
				p = req.Players[i]
			}
			if p.PriceOverride != "" {
				return nil, errs.Forbidden("price overrides are staff-only")
			}
			if p.PlayerType == "member" && p.MemberID != nil && *p.MemberID == mc.memberID {
				self = true
			}
			if p.PlayerType == "member" && p.MemberID == nil && p.MemberNo == "" && p.CustomerID == nil {
				req.Players[i].MemberID = &mc.memberID
				self = true
			}
		}
		if !self {
			return nil, errs.Validation("member_must_play", "add yourself as a player", errs.Field("players", "member_required", "you must play"))
		}
		return m.CreateBooking(ctx, tx, mc.property, req, false)
	})
}

func (m *Module) memberBookings(w http.ResponseWriter, r *http.Request) {
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		return listBookings(ctx, tx, mc.property, r, `(b.member_id = $2 OR b.customer_id = $3 OR EXISTS (SELECT 1 FROM golf.booking_players x WHERE x.booking_id = b.id
			AND (x.member_id = $2 OR x.customer_id = $3)))`, mc.memberID, mc.customerID)
	})
}

func (m *Module) memberBooking(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if err := m.memberOwnsBooking(ctx, tx, mc, bid); err != nil {
			return nil, err
		}
		return GetBooking(ctx, tx, bid)
	})
}

func (m *Module) memberCancel(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[CancelRequest](w, r)
	if !ok {
		return
	}
	req.WaiveFee = false
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = "cancelled by member"
	}
	m.memberWrite(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if err := m.memberOwnsBooking(ctx, tx, mc, bid); err != nil {
			return nil, err
		}
		return m.Cancel(ctx, tx, mc.property, bid, req)
	})
}

func (m *Module) memberReschedule(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[RescheduleRequest](w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = "rescheduled by member"
	}
	m.memberWrite(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if err := m.memberOwnsBooking(ctx, tx, mc, bid); err != nil {
			return nil, err
		}
		return m.Reschedule(ctx, tx, mc.property, bid, req, false)
	})
}

// MyFlight is today's flight for a member (My Flights, My Caddy, My Golf Cart).
type MyFlight struct {
	BookingID   uuid.UUID     `json:"bookingId"`
	BookingCode string        `json:"bookingCode"`
	CourseName  string        `json:"courseName"`
	StartAt     time.Time     `json:"startAt"`
	LocalTime   string        `json:"localTime"`
	Status      string        `json:"status"`
	Players     []SheetPlayer `json:"players"`
	GolfCarts   []string      `json:"golfCarts"`
	QueueStatus *string       `json:"queueStatus"`
}

func (m *Module) memberFlights(w http.ResponseWriter, r *http.Request) {
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		pol, err := LoadPolicies(ctx, tx, mc.property, clock.Now())
		if err != nil {
			return nil, err
		}
		day := localDay(clock.Now(), location(ctx, tx, mc.property))
		fl, err := m.loadFlights(ctx, tx, pol, `f.play_date >= $1::date AND f.status <> 'cancelled' AND EXISTS (SELECT 1 FROM golf.booking_players x
			WHERE x.flight_id = f.id AND (x.member_id = $2 OR x.customer_id = $3) AND x.status IN ('booked','checked_in'))`, day.Format("2006-01-02"), mc.memberID, mc.customerID)
		if err != nil {
			return nil, err
		}
		loc := location(ctx, tx, mc.property)
		out := []MyFlight{}
		for _, f := range fl {
			if f.BookingID == nil {
				continue
			}
			b, err := GetBooking(ctx, tx, *f.BookingID)
			if err != nil {
				return nil, err
			}
			var start time.Time
			_ = tx.QueryRow(ctx, `SELECT start_at FROM golf.tee_times WHERE id = $1`, f.teeTimeID).Scan(&start)
			out = append(out, MyFlight{BookingID: b.ID, BookingCode: b.Code, CourseName: b.CourseName, StartAt: start, LocalTime: start.In(loc).Format("15:04"),
				Status: f.Status, Players: f.Players, GolfCarts: f.Carts, QueueStatus: f.QueueStatus})
		}
		return httpx.Page[MyFlight]{Items: out}, nil
	})
}

// MemberLookup is a member found to add as a player.
type MemberLookup struct {
	MemberID uuid.UUID `json:"memberId"`
	MemberNo string    `json:"memberNo"`
	Name     string    `json:"name"`
	Family   bool      `json:"family"`
}

func (m *Module) memberDirectory(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		out := []MemberLookup{}
		fam, err := membership.FamilyMemberIDs(ctx, tx, mc.memberID)
		if err != nil {
			return nil, err
		}
		famSet := map[uuid.UUID]bool{}
		for _, f := range fam {
			famSet[f] = true
		}
		// family members always; other members by exact member number or name (≥ 3 letters)
		found, err := membership.FindMembers(ctx, tx, mc.property, mc.memberID, fam, q)
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			out = append(out, MemberLookup{MemberID: f.ID, MemberNo: f.No, Name: f.Name, Family: famSet[f.ID]})
		}
		return httpx.Page[MemberLookup]{Items: out}, nil
	})
}

// MyProfile is the member's profile (Profile page).
type MyProfile struct {
	CustomerID     uuid.UUID  `json:"customerId"`
	Name           string     `json:"name"`
	Email          string     `json:"email"`
	Phone          string     `json:"phone"`
	Gender         string     `json:"gender"`
	BirthDate      *time.Time `json:"birthDate"`
	HandicapIndex  *string    `json:"handicapIndex" doc:"Read-only"`
	MemberNo       string     `json:"memberNo"`
	ConsentAt      *time.Time `json:"consentAt"`
	MarketingOptIn bool       `json:"marketingOptIn"`
}

type ProfilePatch struct {
	Phone          *string `json:"phone,omitempty"`
	MarketingOptIn *bool   `json:"marketingOptIn,omitempty"`
	Consent        *bool   `json:"consent,omitempty"`
}

func (m *Module) loadProfile(ctx context.Context, tx pgx.Tx, mc memberCtx) (MyProfile, error) {
	c, err := crm.GetCustomer(ctx, tx, mc.customerID)
	if err != nil {
		return MyProfile{}, err
	}
	p := MyProfile{CustomerID: c.ID, Name: c.Name, Email: c.Email, Phone: c.Phone, Gender: c.Gender, BirthDate: c.BirthDate, MemberNo: mc.standing.MemberNo}
	_ = tx.QueryRow(ctx, `SELECT handicap_index::text FROM golf.handicaps WHERE customer_id = $1 ORDER BY effective_at DESC LIMIT 1`, c.ID).Scan(&p.HandicapIndex)
	var consent *time.Time
	var opt bool
	if err := crm.Consent(ctx, tx, c.ID, &consent, &opt); err != nil {
		return p, err
	}
	p.ConsentAt, p.MarketingOptIn = consent, opt
	return p, nil
}

func (m *Module) memberProfile(w http.ResponseWriter, r *http.Request) {
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) { return m.loadProfile(ctx, tx, mc) })
}

func (m *Module) memberProfilePatch(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[ProfilePatch](w, r)
	if !ok {
		return
	}
	m.memberWrite(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if err := crm.UpdateSelf(ctx, tx, mc.customerID, crm.SelfUpdate{Phone: req.Phone, MarketingOptIn: req.MarketingOptIn, Consent: req.Consent}); err != nil {
			return nil, err
		}
		return m.loadProfile(ctx, tx, mc)
	})
}

// ── public website (EP-14) ────────────────────────────────────────────────

// publicProperty picks the property of the public website (query
// parameter property = code; default the first active property with golf).
func publicProperty(ctx context.Context, tx pgx.Tx, r *http.Request) (uuid.UUID, error) {
	var pid uuid.UUID
	code := r.URL.Query().Get("property")
	err := tx.QueryRow(ctx, `SELECT p.id FROM platform.properties p WHERE p.status = 'active' AND p.archived_at IS NULL AND ($1 = '' OR p.code = upper($1))
		AND EXISTS (SELECT 1 FROM golf.courses c WHERE c.property_id = p.id AND c.status = 'active') ORDER BY p.created_at LIMIT 1`, code).Scan(&pid)
	if dbtx.IsNoRows(err) {
		return pid, errs.NotFound("golf club")
	}
	return pid, err
}

func (m *Module) public(w http.ResponseWriter, r *http.Request, write bool, status int, fn func(ctx context.Context, tx pgx.Tx, property uuid.UUID) (any, error)) {
	ctx := dbtx.System(r.Context())
	run := m.DB.WithReadTx
	if write {
		run = m.DB.WithTx
	}
	var out any
	err := run(ctx, func(tx pgx.Tx) error {
		pid, err := publicProperty(ctx, tx, r)
		if err != nil {
			return err
		}
		pctx := reqctx.WithProperty(ctx, pid)
		out, err = fn(pctx, tx, pid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, status, out)
}

func (m *Module) limit(r *http.Request, key string, n int) error {
	if m.Cfg != nil && m.Cfg.Env == "test" {
		return nil
	}
	if !m.Limiter.Allow(key+":"+reqctx.GetMeta(r.Context()).IP, n, time.Minute) {
		return errs.RateLimited()
	}
	return nil
}

func (m *Module) checkCaptcha(ctx context.Context, r *http.Request, token string) error {
	if m.Integrations == nil {
		return nil
	}
	cv, err := m.Integrations.Captcha(ctx)
	if errors.Is(err, integration.ErrNotConfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	ok, err := cv.VerifyCaptcha(ctx, token, reqctx.GetMeta(ctx).IP)
	if err != nil || !ok {
		return errs.Forbidden("CAPTCHA verification failed; please try again")
	}
	return nil
}

// PublicInfo describes the club for the website (Golf Course, Course Guide,
// Hole-by-Hole, Facilities, Handicap).
type PublicInfo struct {
	PropertyID     uuid.UUID    `json:"propertyId"`
	ClubName       string       `json:"clubName"`
	Courses        []CourseInfo `json:"courses"`
	DressCode      string       `json:"dressCode"`
	ClubRules      string       `json:"clubRules"`
	MaxPlayers     int          `json:"maxPlayers"`
	HoldMinutes    int          `json:"holdMinutes"`
	BookingWindow  int          `json:"bookingWindowDays"`
	CaptchaSiteKey *string      `json:"captchaSiteKey"`
	PaymentMethods []string     `json:"paymentMethods"`
}

func (m *Module) publicInfo(w http.ResponseWriter, r *http.Request) {
	m.public(w, r, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, p uuid.UUID) (any, error) {
		pol, err := LoadPolicies(ctx, tx, p, clock.Now())
		if err != nil {
			return nil, err
		}
		out := PublicInfo{PropertyID: p, DressCode: pol.Golf.DressCode, ClubRules: pol.Golf.ClubRules, MaxPlayers: pol.Golf.MaxPlayers,
			HoldMinutes: pol.Golf.HoldMinutes, BookingWindow: pol.Golf.BookingWindowDays["non_member"], PaymentMethods: []string{"qris", "virtual_account", "card"}}
		_ = tx.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&out.ClubName)
		if out.Courses, err = courseInfos(ctx, tx, p); err != nil {
			return nil, err
		}
		if m.Integrations != nil {
			if cv, err := m.Integrations.Captcha(ctx); err == nil {
				k := cv.SiteKey()
				out.CaptchaSiteKey = &k
			}
		}
		return out, nil
	})
}

// RateRow is one line of the structured rate table (FR-WEB-03).
type RateRow struct {
	Segment  string `json:"segment"`
	DayType  string `json:"dayType"`
	TimeBand string `json:"timeBand"`
	Price    string `json:"price"`
	Currency string `json:"currency"`
	Mode     string `json:"pricingMode"`
}

func (m *Module) publicRates(w http.ResponseWriter, r *http.Request) {
	m.public(w, r, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, p uuid.UUID) (any, error) {
		day := localDay(clock.Now(), location(ctx, tx, p))
		if v := r.URL.Query().Get("date"); v != "" {
			d, err := parseDate(v)
			if err != nil {
				return nil, err
			}
			day = d
		}
		rows, err := commercial.RateTable(ctx, tx, p, day, "golf_round")
		if err != nil {
			return nil, err
		}
		out := []RateRow{}
		for _, x := range rows {
			out = append(out, RateRow{Segment: x.Segment, DayType: x.DayType, TimeBand: x.TimeBand, Price: x.Price, Currency: x.Currency, Mode: x.Mode})
		}
		return httpx.Page[RateRow]{Items: out}, nil
	})
}

func (m *Module) publicAvailability(w http.ResponseWriter, r *http.Request) {
	if err := m.limit(r, "public-availability", 120); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := r.URL.Query()
	m.public(w, r, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, p uuid.UUID) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		var course *uuid.UUID
		if v := q.Get("courseId"); v != "" {
			u, err := uuid.Parse(v)
			if err != nil {
				return nil, errs.BadRequest("invalid_course", "courseId must be a UUID")
			}
			course = &u
		}
		players := 0
		if v := q.Get("players"); v != "" {
			for _, c := range v {
				if c >= '0' && c <= '9' {
					players = players*10 + int(c-'0')
				}
			}
		}
		all, err := m.availability(ctx, tx, p, day, course, q.Get("session"), players, []string{"non_member", "guest"}, "website")
		if err != nil {
			return nil, err
		}
		pol, err := LoadPolicies(ctx, tx, p, clock.Now())
		if err != nil {
			return nil, err
		}
		out := []AvailableSlot{}
		cut := clock.Now().Add(time.Duration(pol.Golf.BookingCutoffMinutes["website"]) * time.Minute)
		for _, s := range all {
			if s.MemberOnly || s.StartAt.Before(cut) {
				continue
			}
			out = append(out, s)
		}
		return httpx.Page[AvailableSlot]{Items: out}, nil
	})
}

func (m *Module) publicHold(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[HoldRequest](w, r)
	if !ok {
		return
	}
	if err := m.limit(r, "public-hold", 10); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req.Channel = "website"
	m.public(w, r, true, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, p uuid.UUID) (any, error) {
		if err := m.checkCaptcha(ctx, r, req.CaptchaToken); err != nil {
			return nil, err
		}
		return m.PlaceHold(ctx, tx, p, req, []string{"non_member"}, false)
	})
}

// PublicContact is the person booking on the website.
type PublicContact struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	Email string `json:"email"`
}

// PublicPlayer is a player entered on the website.
type PublicPlayer struct {
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
}

// PublicBookingRequest completes a website booking (FR-WEB-04/05).
type PublicBookingRequest struct {
	HoldID        uuid.UUID      `json:"holdId"`
	HoldToken     string         `json:"holdToken"`
	Contact       PublicContact  `json:"contact"`
	Players       []PublicPlayer `json:"players,omitempty" doc:"Other players (the contact plays as player 1)"`
	Consent       bool           `json:"consent" doc:"UU PDP consent (required)"`
	CartRequest   *int           `json:"golfCartRequest,omitempty"`
	CaddyRequest  string         `json:"caddyRequest,omitempty"`
	PaymentMethod string         `json:"paymentMethod" enum:"qris,virtual_account,card"`
	CaptchaToken  string         `json:"captchaToken,omitempty"`
}

// PublicBooking is the website view of a booking.
type PublicBooking struct {
	Code        string           `json:"code"`
	Status      string           `json:"status"`
	CourseName  string           `json:"courseName"`
	StartAt     time.Time        `json:"startAt"`
	LocalTime   string           `json:"localTime"`
	PlayDate    string           `json:"playDate"`
	PlayerCount int              `json:"playerCount"`
	ContactName string           `json:"contactName"`
	Folio       *billing.Summary `json:"folio"`
	Payment     *PublicPayment   `json:"payment"`
	QRToken     *string          `json:"qrToken" doc:"Present once confirmed: encode as oneclub:booking:<token>"`
	ManageToken string           `json:"manageToken,omitempty"`
	CanCancel   bool             `json:"canCancel"`
}

// PublicPayment is the checkout of a website booking.
type PublicPayment struct {
	Number      string     `json:"number"`
	Status      string     `json:"status"`
	Amount      string     `json:"amount"`
	MethodType  string     `json:"methodType"`
	CheckoutURL *string    `json:"checkoutUrl"`
	QRString    *string    `json:"qrString"`
	VANumber    *string    `json:"vaNumber"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

func toPublic(b Booking, token string) PublicBooking {
	out := PublicBooking{Code: b.Code, Status: b.Status, CourseName: b.CourseName, StartAt: b.StartAt, LocalTime: b.LocalTime, PlayDate: b.PlayDate,
		PlayerCount: b.PlayerCount, ContactName: b.ContactName, Folio: b.Folio, ManageToken: token,
		CanCancel: b.Status == "pending" || b.Status == "confirmed"}
	if b.Status == "confirmed" || b.Status == "checked_in" || b.Status == "completed" {
		q := b.QRToken
		out.QRToken = &q
	}
	if b.Payment != nil {
		out.Payment = &PublicPayment{Number: b.Payment.Number, Status: b.Payment.Status, Amount: b.Payment.Amount, MethodType: b.Payment.MethodType,
			CheckoutURL: b.Payment.CheckoutURL, QRString: b.Payment.QRString, VANumber: b.Payment.VANumber, ExpiresAt: b.Payment.ExpiresAt}
	}
	return out
}

func (m *Module) publicBook(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[PublicBookingRequest](w, r)
	if !ok {
		return
	}
	if err := m.limit(r, "public-book", 10); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !req.Consent {
		httpx.WriteError(w, r, errs.Validation("consent_required", "please agree to the privacy notice", errs.Field("consent", "required", "consent is required")))
		return
	}
	if strings.TrimSpace(req.Contact.Name) == "" || strings.TrimSpace(req.Contact.Phone) == "" || !strings.Contains(req.Contact.Email, "@") {
		httpx.WriteError(w, r, errs.Validation("contact_required", "name, phone and e-mail are required", errs.Field("contact", "required", "name, phone, e-mail")))
		return
	}
	switch req.PaymentMethod {
	case "qris", "virtual_account", "card":
	default:
		httpx.WriteError(w, r, errs.Validation("invalid_payment_method", "choose QRIS, Virtual Account or Card", errs.Field("paymentMethod", "invalid", "qris, virtual_account or card")))
		return
	}
	m.public(w, r, true, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, p uuid.UUID) (any, error) {
		if err := m.checkCaptcha(ctx, r, req.CaptchaToken); err != nil {
			return nil, err
		}
		var hash string
		if err := tx.QueryRow(ctx, `SELECT coalesce(manage_token_hash, '') FROM golf.bookings WHERE id = $1 AND property_id = $2 AND channel = 'website'`,
			req.HoldID, p).Scan(&hash); err != nil || hash == "" || !secret.Equal(hash, secret.HashToken(req.HoldToken)) {
			return nil, errs.NotFound("tee hold")
		}
		ref, err := crm.ResolveGuest(ctx, tx, p, crm.GuestInput{Name: req.Contact.Name, Phone: req.Contact.Phone, Email: req.Contact.Email, Consent: true})
		if err != nil {
			return nil, err
		}
		players := []PlayerInput{{PlayerType: "non_member", CustomerID: ref.CustomerID, GuestID: ref.GuestID, Name: req.Contact.Name, Phone: req.Contact.Phone}}
		if ref.CustomerID != nil {
			players[0].GuestID = nil
		}
		for _, pl := range req.Players {
			if strings.TrimSpace(pl.Name) == "" {
				players = append(players, PlayerInput{PlayerType: "non_member", TBA: true, Name: "Guest"})
				continue
			}
			players = append(players, PlayerInput{PlayerType: "non_member", Name: pl.Name, Phone: pl.Phone, TBA: pl.Phone == ""})
		}
		b, err := m.CreateBooking(ctx, tx, p, BookingRequest{HoldID: &req.HoldID, BookingType: "non_member", Channel: "website", CustomerID: ref.CustomerID,
			ContactName: req.Contact.Name, ContactPhone: req.Contact.Phone, ContactEmail: req.Contact.Email, Players: players, CartRequest: req.CartRequest,
			CaddyRequest: req.CaddyRequest, PaymentMethod: req.PaymentMethod, Consent: true}, false)
		if err != nil {
			return nil, err
		}
		if b.CustomerID == nil && ref.GuestID != nil {
			if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET guest_id = $2 WHERE id = $1`, b.ID, *ref.GuestID); err != nil {
				return nil, err
			}
		}
		return toPublic(b, req.HoldToken), nil
	})
}

func (m *Module) bookingByToken(ctx context.Context, tx pgx.Tx, property uuid.UUID, token string) (Booking, error) {
	var bid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM golf.bookings WHERE manage_token_hash = $1 AND property_id = $2 AND channel = 'website' AND status <> 'draft'`,
		secret.HashToken(token), property).Scan(&bid); err != nil {
		return Booking{}, errs.NotFound("booking")
	}
	return GetBooking(ctx, tx, bid)
}

func (m *Module) publicManage(w http.ResponseWriter, r *http.Request) {
	token := chiParam(r, "token")
	if err := m.limit(r, "public-manage", 60); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.public(w, r, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, p uuid.UUID) (any, error) {
		b, err := m.bookingByToken(ctx, tx, p, token)
		if err != nil {
			return nil, err
		}
		return toPublic(b, ""), nil
	})
}

func (m *Module) publicCancel(w http.ResponseWriter, r *http.Request) {
	token := chiParam(r, "token")
	req, ok := decode[ReasonBody](w, r)
	if !ok {
		return
	}
	if err := m.limit(r, "public-cancel", 10); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.public(w, r, true, http.StatusOK, func(ctx context.Context, tx pgx.Tx, p uuid.UUID) (any, error) {
		b, err := m.bookingByToken(ctx, tx, p, token)
		if err != nil {
			return nil, err
		}
		reason := req.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "cancelled by guest (website)"
		}
		if _, err := m.Cancel(ctx, tx, p, b.ID, CancelRequest{Reason: reason}); err != nil {
			return nil, err
		}
		nb, err := GetBooking(ctx, tx, b.ID)
		return toPublic(nb, ""), err
	})
}

func (m *Module) registerPortal(reg *route.Registry) {
	mem := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Permission = "golf", "Member Portal", catalog.ShellMemberPortal
		reg.Add(rt)
	}
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/courses", Summary: "Courses (Course Guide)", Response: CourseInfo{}, List: true, Handler: m.memberCourses})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/availability", Summary: "Tee Time availability with Member and Guest of Member prices",
		Response: AvailableSlot{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "courseId"}, {Name: "session"}}, Handler: m.memberAvailability})
	mem(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/holds", Summary: "Hold a tee time", Request: HoldRequest{}, Response: Hold{},
		Idempotent: true, Handler: m.memberHold})
	mem(route.Route{Method: http.MethodDelete, Path: "/api/v1/member/golf/holds/{id}", Summary: "Release my tee hold", Response: Booking{}, Status: http.StatusOK,
		Handler: m.memberReleaseHold})
	mem(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/bookings", Summary: "Book Golf (players, caddy / golf cart request, payment)",
		Request: BookingRequest{}, Response: Booking{}, Idempotent: true, Handler: m.memberBook})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/bookings", Summary: "My bookings (upcoming and history)", Response: BookingSummary{}, List: true,
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "filter[status]"}}, Handler: m.memberBookings})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/bookings/{id}", Summary: "My booking", Response: Booking{}, Handler: m.memberBooking})
	mem(route.Route{Method: http.MethodPost, Path: "/api/v1/member/bookings/{id}:cancel", Summary: "Cancel my booking (Cancellation Policy)",
		Request: CancelRequest{}, Response: CancelResult{}, Status: http.StatusOK, Handler: m.memberCancel})
	mem(route.Route{Method: http.MethodPost, Path: "/api/v1/member/bookings/{id}:reschedule", Summary: "Reschedule my booking",
		Request: RescheduleRequest{}, Response: Booking{}, Status: http.StatusOK, Handler: m.memberReschedule})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/my-flights", Summary: "My Flights with My Caddy and My Golf Cart",
		Response: MyFlight{}, List: true, Handler: m.memberFlights})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/members", Summary: "Find family members and other members to add as players",
		Response: MemberLookup{}, List: true, Query: []route.Param{{Name: "q"}}, Handler: m.memberDirectory})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/profile", Summary: "My profile (handicap index read-only)", Response: MyProfile{}, Handler: m.memberProfile})
	mem(route.Route{Method: http.MethodPatch, Path: "/api/v1/member/profile", Summary: "Update my phone, consent and marketing preference",
		Request: ProfilePatch{}, Response: MyProfile{}, Handler: m.memberProfilePatch})

	pub := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Auth = "golf", "Website", route.AuthPublic
		rt.Query = append(rt.Query, route.Param{Name: "property", Description: "Property code (default: the golf club)"})
		reg.Add(rt)
	}
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/golf/info", Summary: "Golf Course, Course Guide, Hole-by-Hole and booking rules",
		Response: PublicInfo{}, Handler: m.publicInfo})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/golf/rates", Summary: "Golf rate table from the Pricing Engine", Response: RateRow{}, List: true,
		Query: []route.Param{{Name: "date"}}, Handler: m.publicRates})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/golf/availability", Summary: "Public tee time availability with guest prices",
		Response: AvailableSlot{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "courseId"}, {Name: "players", Type: "integer"}, {Name: "session"}}, Handler: m.publicAvailability})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/golf/holds", Summary: "Hold a tee time (CAPTCHA, rate limited)", Request: HoldRequest{}, Response: Hold{},
		Handler: m.publicHold})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/golf/bookings", Summary: "Book and pay online (prepaid, Payment Policy)",
		Request: PublicBookingRequest{}, Response: PublicBooking{}, Handler: m.publicBook})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/bookings/{token}", Summary: "Manage booking (secure link)", Response: PublicBooking{}, Handler: m.publicManage})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/bookings/{token}:cancel", Summary: "Cancel from the secure link (Cancellation Policy)",
		Request: ReasonBody{}, Response: PublicBooking{}, Status: http.StatusOK, Handler: m.publicCancel})
}
