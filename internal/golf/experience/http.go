package experience

// Routes of PRD P2 Complete Golf Experience (§11 API): caddy lifecycle,
// Caddy Tablet and rounds, golf cart inspections & maintenance, scoring &
// handicap, pace of play & course maps, Hole-in-One, Hall of Fame, Driving
// Range and Reciprocal Club. P1's tee sheet, booking, flight, starter,
// caddy and golf cart assignment routes stay as they are.

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
	syncsvc "oneclub/internal/platform/sync"
)

type ReasonInput struct {
	Reason string `json:"reason"`
}

type FavoriteInput struct {
	CustomerID uuid.UUID `json:"customerId"`
	Favorite   bool      `json:"favorite"`
}

// CaddyRatingInput rates the caddy of a finished assignment (staff entry).
type CaddyRatingInput struct {
	AssignmentID uuid.UUID `json:"assignmentId"`
	RatingInput
}

// CaddyRating is a recorded rating.
type CaddyRating struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	AssignmentID uuid.UUID  `json:"assignmentId" db:"assignment_id"`
	CaddyID      uuid.UUID  `json:"caddyId" db:"caddy_id"`
	CaddyName    string     `json:"caddyName" db:"caddy_name"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	Rating       int        `json:"rating" db:"rating"`
	Comment      *string    `json:"comment" db:"comment"`
	Channel      string     `json:"channel" db:"channel"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
}

// GolfCartInspectionInput is an inspection of one golf cart.
type GolfCartInspectionInput struct {
	GolfCartID uuid.UUID `json:"golfCartId"`
	InspectionInput
}

type IngestResult struct {
	Updated int `json:"updated"`
}

type CountResult struct {
	Count int `json:"count"`
}

// CartHistory is the Assignment / Replacement / Maintenance history of a cart.
type CartHistory struct {
	Assignments []CartAssignment `json:"assignments"`
	Inspections []Inspection     `json:"inspections"`
	Maintenance []Maintenance    `json:"maintenance"`
	Incidents   []Incident       `json:"incidents"`
}

// CourseMap is the course map of a hole (FR-PLX-02): P1 course assets plus
// the distances from the player's position.
type CourseMap struct {
	HoleID    uuid.UUID   `json:"holeId"`
	Assets    []AssetInfo `json:"assets"`
	Distances []Distance  `json:"distances" doc:"With lat & lng: distance to the green, hazards and POIs"`
	// the geo-referenced course map (Cart View): where the green of the hole
	// and the device are on it, as fractions of its width and height
	OverviewURL *string  `json:"overviewUrl"`
	GreenX      *float64 `json:"greenX"`
	GreenY      *float64 `json:"greenY"`
	HereX       *float64 `json:"hereX" doc:"With lat & lng inside the course map"`
	HereY       *float64 `json:"hereY"`
}

// HallOfFameConsentInput records the player's opt-in for one entry.
type HallOfFameConsentInput struct {
	EntryID uuid.UUID `json:"entryId"`
	Consent string    `json:"consent" enum:"granted,withdrawn"`
}

// Kiosk is the Clubhouse Screen feed (read-only, rotating).
type Kiosk struct {
	RotateSeconds int           `json:"rotateSeconds"`
	Entries       []PublicEntry `json:"entries"`
}

type PublicClub struct {
	Code    string  `json:"code" db:"code"`
	Name    string  `json:"name" db:"name"`
	Country string  `json:"country" db:"country"`
	City    *string `json:"city" db:"city"`
}

// LinkPlayerInput ties a verified visit to the reciprocal booking player.
type LinkPlayerInput struct {
	PlayerID uuid.UUID `json:"playerId"`
}

var publicLimiter = &handle.Limiter{N: 120, Period: time.Minute}

func dateRange(ctx context.Context, q dbtx.Querier, r *http.Request, defDays int) (time.Time, time.Time, error) {
	now := localNow(ctx, q)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from, err := handle.QueryDate(r, "from", today.AddDate(0, 0, -defDays))
	if err != nil {
		return from, from, err
	}
	to, err := handle.QueryDate(r, "to", today)
	if err != nil {
		return from, to, err
	}
	f, _ := dayBounds(ctx, q, from)
	_, t := dayBounds(ctx, q, to)
	return f, t, nil
}

func queryPosition(r *http.Request) (float64, float64, bool) {
	lat, err1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	return lat, lng, err1 == nil && err2 == nil
}

// RegisterSync registers the offline round queue of the Caddy Tablet.
func (m *Module) RegisterSync(s *syncsvc.Service) {
	s.Handle("golf.round", m.SyncHandler)
}

// PaceTargetInput sets the pace target of a hole.
type PaceTargetInput struct {
	TargetMinutes int `json:"targetMinutes" doc:"5–40 minutes"`
}

// PaceToleranceInput sets the slow-play tolerance of a playing route.
// CancelOrderInput is the reason an on-course order is cancelled.
type CancelOrderInput struct {
	Reason string `json:"reason,omitempty"`
}

type PaceToleranceInput struct {
	ToleranceMinutes int `json:"toleranceMinutes"`
}

// Register adds the P2 golf routes (wired by internal/app next to P1's).
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	m.rangeHooks()
	m.registerMe(reg)
	for _, d := range Defs {
		eng.Register(reg, d)
	}
	if m.GPS == nil {
		m.GPS = TabletGPS{}
	}
	if m.Weather == nil {
		m.Weather = NewOpenMeteo()
	}
	db := m.DB
	add := func(tag string, rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "golf", route.ScopeProperty, tag
		reg.Add(rt)
	}
	id := func(r *http.Request) (uuid.UUID, error) { return handle.ID(r) }
	prop := handle.Property
	m.registerGuide(reg, add)
	m.registerRangeBookings(reg, add)
	m.registerRangeAreas(reg, add)
	m.registerScorecardSheets(reg, add)
	m.registerCorrectionRequests(reg, add)
	m.registerCourseStops(add)
	m.registerRouteTeeHouses(add)
	m.registerSelfCheckIn(reg, add)
	m.registerTabletGPS(add)
	m.registerMessages(add)
	m.registerWeather(add)
	m.registerMaintenance(add)
	m.registerCaddyRelation(add)
	m.registerLeaders(reg, add)

	// ── P2 profiles of P1 master data ──
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddies/{id}/profile", Summary: "Caddy profile: level, tablet login, joined date",
		Permission: "golf.caddy.view", Response: CaddyProfile{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CaddyProfile, error) {
			cid, err := id(r)
			if err != nil {
				return CaddyProfile{}, err
			}
			return m.CaddyProfile(ctx, tx, prop(ctx), cid)
		})})
	add("Caddies", route.Route{Method: http.MethodPut, Path: "/api/v1/golf/caddies/{id}/profile", Summary: "Set the tablet login, joined date and initial level",
		Permission: "golf.caddy.update", Request: CaddyProfileInput{}, Response: CaddyProfile{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CaddyProfileInput) (CaddyProfile, error) {
			cid, err := id(r)
			if err != nil {
				return CaddyProfile{}, err
			}
			return m.SetCaddyProfile(ctx, tx, prop(ctx), cid, in)
		})})
	add("Golf Carts", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-carts/{id}/profile", Summary: "Golf cart service hours, battery and GPS device",
		Permission: "golf.golf_cart.view", Response: CartProfile{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CartProfile, error) {
			cid, err := id(r)
			if err != nil {
				return CartProfile{}, err
			}
			return m.CartProfile(ctx, tx, prop(ctx), cid)
		})})
	add("Golf Carts", route.Route{Method: http.MethodPut, Path: "/api/v1/golf/golf-carts/{id}/profile", Summary: "Set the service threshold and GPS device",
		Permission: "golf.golf_cart.update", Request: CartProfileInput{}, Response: CartProfile{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CartProfileInput) (CartProfile, error) {
			cid, err := id(r)
			if err != nil {
				return CartProfile{}, err
			}
			return m.SetCartProfile(ctx, tx, prop(ctx), cid, in)
		})})
	add("Pace of Play", route.Route{Method: http.MethodPut, Path: "/api/v1/golf/holes/{id}/pace-target", Summary: "Pace target of a hole (minutes)",
		Permission: "golf.course.update", Request: PaceTargetInput{},
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PaceTargetInput) (handle.Empty, error) {
			hid, err := id(r)
			if err != nil {
				return handle.Empty{}, err
			}
			if in.TargetMinutes < 5 || in.TargetMinutes > 40 {
				return handle.Empty{}, handle.Invalid("targetMinutes", "invalid", "5–40 minutes")
			}
			tag, err := tx.Exec(ctx, `INSERT INTO golf.hole_pace_targets (hole_id, property_id, target_minutes, updated_by)
				SELECT id, property_id, $3, $4 FROM golf.holes WHERE id = $1 AND property_id = $2
				ON CONFLICT (hole_id) DO UPDATE SET target_minutes = EXCLUDED.target_minutes, updated_at = now(), updated_by = EXCLUDED.updated_by`,
				hid, prop(ctx), in.TargetMinutes, actorPtr(ctx))
			if err != nil {
				return handle.Empty{}, err
			}
			if tag.RowsAffected() == 0 {
				return handle.Empty{}, errs.NotFound("hole")
			}
			return handle.Empty{}, record(ctx, tx, "golf.hole_pace_target", hid, "pace target", audit.ActionUpdate, prop(ctx), nil, in, "")
		})})
	add("Pace of Play", route.Route{Method: http.MethodPut, Path: "/api/v1/golf/playing-routes/{id}/pace-tolerance", Summary: "Slow-play tolerance of a playing route (minutes)",
		Permission: "golf.course.update", Request: PaceToleranceInput{},
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PaceToleranceInput) (handle.Empty, error) {
			rid, err := id(r)
			if err != nil {
				return handle.Empty{}, err
			}
			if in.ToleranceMinutes < 0 {
				return handle.Empty{}, handle.Invalid("toleranceMinutes", "invalid", "0 or more minutes")
			}
			tag, err := tx.Exec(ctx, `INSERT INTO golf.route_pace_tolerances (playing_route_id, property_id, tolerance_minutes, updated_by)
				SELECT id, property_id, $3, $4 FROM golf.playing_routes WHERE id = $1 AND property_id = $2
				ON CONFLICT (playing_route_id) DO UPDATE SET tolerance_minutes = EXCLUDED.tolerance_minutes, updated_at = now(), updated_by = EXCLUDED.updated_by`,
				rid, prop(ctx), in.ToleranceMinutes, actorPtr(ctx))
			if err != nil {
				return handle.Empty{}, err
			}
			if tag.RowsAffected() == 0 {
				return handle.Empty{}, errs.NotFound("playing route")
			}
			return handle.Empty{}, record(ctx, tx, "golf.route_pace_tolerance", rid, "pace tolerance", audit.ActionUpdate, prop(ctx), nil, in, "")
		})})

	// ── Caddy Tablet & rounds (EP-06, EP-09) ──
	add("Caddy Tablet", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/my-assignments", Summary: "My Assignments (current and next) of the signed-in caddy",
		Permission: "golf.tablet.use", Response: MyAssignments{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MyAssignments, error) {
			return m.MyAssignments(ctx, tx, prop(ctx))
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-assignments/{id}:accept", Summary: "Accept Assignment (tablet)",
		Permission: "golf.caddy_assignment.accept", Response: CaddyAssignment{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (CaddyAssignment, error) {
			aid, err := id(r)
			if err != nil {
				return CaddyAssignment{}, err
			}
			return m.AcceptAssignment(ctx, tx, aid)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/rounds/{id}", Summary: "Round information (tablet): flight, holes, players' context, scorecards",
		Permission: "golf.round.view", Response: RoundInfo{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RoundInfo, error) {
			fid, err := id(r)
			if err != nil {
				return RoundInfo{}, err
			}
			return m.RoundInfo(ctx, tx, fid)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}:start", Summary: "Start Round (tee-off through the starter)",
		Permission: "golf.round.operate", Request: RoundEvent{}, Response: Round{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RoundEvent) (Round, error) {
			fid, err := m.seeFlight(ctx, tx, r)
			if err != nil {
				return Round{}, err
			}
			return m.StartRound(ctx, tx, fid, in)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}:hole-progress", Summary: "Update Round Status (hole progress, pace)",
		Permission: "golf.round.operate", Request: HoleInput{}, Response: Round{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in HoleInput) (Round, error) {
			fid, err := m.seeFlight(ctx, tx, r)
			if err != nil {
				return Round{}, err
			}
			return m.RecordHole(ctx, tx, fid, in)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}:complete", Summary: "Complete Round (Round Finish)",
		Permission: "golf.round.operate", Request: RoundEvent{}, Response: Round{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RoundEvent) (Round, error) {
			fid, err := m.seeFlight(ctx, tx, r)
			if err != nil {
				return Round{}, err
			}
			return m.FinishRound(ctx, tx, fid, in)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}:handover", Summary: "Continue the round on a replacement tablet",
		Permission: "golf.round.operate", Request: HandoverInput{}, Response: RoundInfo{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in HandoverInput) (RoundInfo, error) {
			fid, err := id(r)
			if err != nil {
				return RoundInfo{}, err
			}
			return m.Handover(ctx, tx, fid, in)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/rounds/{id}/times", Summary: "Hole Progress, Hole Duration, Round Duration",
		Permission: "golf.round.view", Response: RoundTimes{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RoundTimes, error) {
			fid, err := m.seeFlight(ctx, tx, r)
			if err != nil {
				return RoundTimes{}, err
			}
			return m.RoundTimes(ctx, tx, fid)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/my-earnings", Summary: "Caddy fee, tips, settlements, attendance and assignment history",
		Permission: "golf.tablet.use", Response: Earnings{}, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Earnings, error) {
			c, err := m.myCaddy(ctx, tx, prop(ctx))
			if err != nil {
				return Earnings{}, err
			}
			from, to, err := dateRange(ctx, tx, r, 30)
			if err != nil {
				return Earnings{}, err
			}
			return m.Earnings(ctx, tx, prop(ctx), c.ID, from, to)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/on-course-orders", Summary: "On-course F&B order charged to the booking folio",
		Permission: "golf.tablet.use", Request: CourseOrderInput{}, Response: commercial.Order{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CourseOrderInput) (commercial.Order, error) {
			return m.CourseOrder(ctx, tx, in)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/rounds/{id}/orders", Summary: "On-course orders of the flight with their service status",
		Permission: "golf.tablet.use", Response: commercial.Order{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[commercial.Order], error) {
			fid, err := id(r)
			if err != nil {
				return httpx.Page[commercial.Order]{}, err
			}
			return handle.Page(m.FlightOrders(ctx, tx, fid))
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}/orders/{orderId}:cancel", Summary: "Cancel an on-course order the tee house has not started",
		Permission: "golf.tablet.use", Request: CancelOrderInput{}, Response: commercial.Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CancelOrderInput) (commercial.Order, error) {
			fid, err := id(r)
			if err != nil {
				return commercial.Order{}, err
			}
			oid, err := uuid.Parse(chi.URLParam(r, "orderId"))
			if err != nil {
				return commercial.Order{}, errs.NotFound("order")
			}
			return m.CancelCourseOrder(ctx, tx, fid, oid, in.Reason)
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/customers/{id}/preferences", Summary: "Record Customer Preference (caddy)",
		Permission: "golf.tablet.use", Request: crm.PreferenceInput{}, Response: crm.Preference{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in crm.PreferenceInput) (crm.Preference, error) {
			cid, err := id(r)
			if err != nil {
				return crm.Preference{}, err
			}
			return m.RecordPreference(ctx, tx, prop(ctx), cid, in)
		})})

	// ── pace of play & course maps (EP-09) ──
	add("Pace of Play", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/pace-of-play", Summary: "Pace of Play (Starter / Marshal)", Permission: "golf.pace.view",
		Response: PaceFlight{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PaceFlight], error) {
			return handle.Page(m.Pace(ctx, tx, prop(ctx)))
		})})
	add("Pace of Play", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/course-maps/{holeId}", Summary: "Course map of a hole with GPS distances",
		Permission: "golf.course.view", Response: CourseMap{}, Query: []route.Param{{Name: "lat"}, {Name: "lng"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CourseMap, error) {
			hid, err := handle.ID(r, "holeId")
			if err != nil {
				return CourseMap{}, err
			}
			out := CourseMap{HoleID: hid, Distances: []Distance{}}
			if out.Assets, err = holeAssets(ctx, tx, hid); err != nil {
				return out, err
			}
			cm, err := courseOverview(ctx, tx, hid, &out)
			if err != nil {
				return out, err
			}
			if lat, lng, ok := queryPosition(r); ok {
				out.HereX, out.HereY = cm.project(lat, lng)
				out.Distances, err = m.HoleDistances(ctx, tx, hid, lat, lng)
			}
			return out, err
		})})

	// ── caddies (EP-05) ──
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-attendance:clock-in", Summary: "Caddy Attendance: clock in (joins the rotation)",
		Permission: "golf.caddy_assignment.manage", Request: ClockInput{}, Response: Attendance{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ClockInput) (Attendance, error) {
			return m.ClockIn(ctx, tx, prop(ctx), in)
		})})
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-attendance:clock-out", Summary: "Caddy Attendance: clock out",
		Permission: "golf.caddy_assignment.manage", Request: ClockInput{}, Response: Attendance{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ClockInput) (Attendance, error) {
			return m.ClockOut(ctx, tx, prop(ctx), in)
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-attendance", Summary: "Caddy Attendance of a day (arrival, departure, shift)",
		Permission: "golf.caddy.view", Response: Attendance{}, List: true, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Attendance], error) {
			d, err := handle.QueryDate(r, "date", localDay(localNow(ctx, tx), time.UTC))
			if err != nil {
				return httpx.Page[Attendance]{}, err
			}
			return handle.Page(handle.List[Attendance](tx.Query(ctx, attendanceSelect+` ORDER BY a.queue_no NULLS LAST, c.code`,
				prop(ctx), d.Format("2006-01-02"))))
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-rotation", Summary: "Caddy rotation & Next Assignment (Caddy Policies order)",
		Permission: "golf.caddy.view", Response: RotationEntry{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "levelId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RotationEntry], error) {
			d, err := handle.QueryDate(r, "date", localDay(localNow(ctx, tx), time.UTC))
			if err != nil {
				return httpx.Page[RotationEntry]{}, err
			}
			lvl, err := handle.QueryUUID(r, "levelId")
			if err != nil {
				return httpx.Page[RotationEntry]{}, err
			}
			return handle.Page(m.Rotation(ctx, tx, prop(ctx), d, lvl))
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddies/{id}/indicators", Summary: "Promotion eligibility indicators (rounds, rating, incidents)",
		Permission: "golf.caddy_promotion.view", Response: Indicators{}, Query: []route.Param{{Name: "toLevelId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Indicators, error) {
			cid, err := id(r)
			if err != nil {
				return Indicators{}, err
			}
			lvl, err := handle.QueryUUID(r, "toLevelId")
			if err != nil {
				return Indicators{}, err
			}
			return m.indicators(ctx, tx, cid, lvl)
		})})
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddies/{id}:promote", Summary: "Request a level change (Promotion Approval)",
		Permission: "golf.caddy_promotion.request", Request: PromotionInput{}, Response: LevelChange{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PromotionInput) (LevelChange, error) {
			cid, err := id(r)
			if err != nil {
				return LevelChange{}, err
			}
			return m.RequestPromotion(ctx, tx, prop(ctx), cid, in)
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddies/{id}/history", Summary: "Round, Assignment, Customer & Level History; repeat customers; favourite count",
		Permission: "golf.caddy.view", Response: CaddyHistory{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CaddyHistory, error) {
			cid, err := id(r)
			if err != nil {
				return CaddyHistory{}, err
			}
			return m.CaddyHistory(ctx, tx, prop(ctx), cid, httpx.ParseList(r).Limit)
		})})
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddies/{id}:favorite", Summary: "Mark / unmark a customer's favourite caddy",
		Permission: "golf.caddy.update", Request: FavoriteInput{},
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FavoriteInput) (handle.Empty, error) {
			cid, err := id(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, m.Favorite(ctx, tx, prop(ctx), cid, in.CustomerID, in.Favorite)
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-utilization", Summary: "Caddy Utilization (rounds per day present, duty hours)",
		Permission: "golf.caddy.view", Response: Utilization{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Utilization], error) {
			now := localDay(localNow(ctx, tx), time.UTC)
			from, err := handle.QueryDate(r, "from", now.AddDate(0, 0, -30))
			if err != nil {
				return httpx.Page[Utilization]{}, err
			}
			to, err := handle.QueryDate(r, "to", now)
			if err != nil {
				return httpx.Page[Utilization]{}, err
			}
			return handle.Page(m.Utilization(ctx, tx, prop(ctx), from, to))
		})})
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-ratings", Summary: "Rate the caddy after the round (1–5 + comment)",
		Permission: "golf.caddy_rating.create", Request: CaddyRatingInput{},
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CaddyRatingInput) (handle.Empty, error) {
			return handle.Empty{}, m.RateCaddy(ctx, tx, prop(ctx), in.AssignmentID, in.CustomerID, in.Rating, in.Comment, "staff")
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-ratings", Summary: "Caddy ratings", Permission: "golf.caddy_rating.view",
		Response: CaddyRating{}, List: true, Query: []route.Param{{Name: "filter[caddyId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CaddyRating], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[CaddyRating](tx.Query(ctx, `SELECT r.id, r.assignment_id, r.caddy_id, c.name AS caddy_name, r.customer_id, r.rating,
				r.comment, r.channel, r.created_at FROM golf.caddy_ratings r JOIN golf.caddies c ON c.id = r.caddy_id
				WHERE r.property_id = $1 AND ($2 = '' OR r.caddy_id::text = $2) ORDER BY r.created_at DESC LIMIT $3`, prop(ctx), lp.Filters["caddyId"], lp.Limit)))
		})})
	m.incidentRoutes(add, "caddy-incidents", "caddy", "golf.caddy_incident")
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-settlements", Summary: "Caddy Fee Settlement statement (goes to approval)",
		Permission: "golf.caddy_settlement.manage", Request: SettlementInput{}, Response: Settlement{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SettlementInput) (Settlement, error) {
			return m.CreateSettlement(ctx, tx, prop(ctx), in)
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-settlements", Summary: "Caddy fee settlements", Permission: "golf.caddy_settlement.view",
		Response: Settlement{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[caddyId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Settlement], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Settlement](tx.Query(ctx, settlementSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.status = $2)
				AND ($3 = '' OR s.caddy_id::text = $3) ORDER BY s.created_at DESC LIMIT $4`, prop(ctx), lp.Filters["status"], lp.Filters["caddyId"], lp.Limit)))
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-settlements/{id}", Summary: "Caddy fee settlement", Permission: "golf.caddy_settlement.view",
		Response: Settlement{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Settlement, error) {
			sid, err := id(r)
			if err != nil {
				return Settlement{}, err
			}
			return m.settlement(ctx, tx, sid)
		})})
	add("Caddies", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-settlements/{id}:pay", Summary: "Record the settlement payout (cash / transfer)",
		Permission: "golf.caddy_settlement.pay", Request: PayInput{}, Response: Settlement{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayInput) (Settlement, error) {
			sid, err := id(r)
			if err != nil {
				return Settlement{}, err
			}
			return m.PaySettlement(ctx, tx, prop(ctx), sid, in)
		})})
	add("Caddies", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-liabilities", Summary: "Caddy fee & tip liability not yet paid", Permission: "golf.caddy_settlement.view",
		Response: CaddyLiability{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CaddyLiability], error) {
			return handle.Page(m.Liabilities(ctx, tx, prop(ctx)))
		})})

	// ── golf carts (EP-07) ──
	add("Golf Carts", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-cart-inspections", Summary: "Golf Cart Inspection (pre-op, post-op, release)",
		Permission: "golf.cart_inspection.create", Request: GolfCartInspectionInput{}, Response: Inspection{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GolfCartInspectionInput) (Inspection, error) {
			return m.Inspect(ctx, tx, prop(ctx), in.GolfCartID, in.InspectionInput)
		})})
	add("Golf Carts", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-cart-inspections", Summary: "Golf cart inspections", Permission: "golf.cart_inspection.view",
		Response: Inspection{}, List: true, Query: []route.Param{{Name: "filter[golfCartId]"}, {Name: "filter[kind]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Inspection], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Inspection](tx.Query(ctx, inspectionSelect+` WHERE property_id = $1 AND ($2 = '' OR golf_cart_id::text = $2)
				AND ($3 = '' OR inspection_kind = $3) ORDER BY inspected_at DESC LIMIT $4`, prop(ctx), lp.Filters["golfCartId"], lp.Filters["kind"], lp.Limit)))
		})})
	add("Golf Carts", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-cart-maintenance", Summary: "Golf Cart Maintenance records", Permission: "golf.cart_maintenance.view",
		Response: Maintenance{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[golfCartId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Maintenance], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Maintenance](tx.Query(ctx, maintenanceSelect+` WHERE m.property_id = $1 AND ($2 = '' OR m.status = $2)
				AND ($3 = '' OR m.golf_cart_id::text = $3) ORDER BY m.opened_at DESC LIMIT $4`, prop(ctx), lp.Filters["status"], lp.Filters["golfCartId"], lp.Limit)))
		})})
	add("Golf Carts", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-cart-maintenance", Summary: "Open maintenance (cart goes to Maintenance)",
		Permission: "golf.cart_maintenance.manage", Request: MaintenanceInput{}, Response: Maintenance{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MaintenanceInput) (Maintenance, error) {
			return m.OpenMaintenance(ctx, tx, prop(ctx), in)
		})})
	add("Golf Carts", route.Route{Method: http.MethodPatch, Path: "/api/v1/golf/golf-cart-maintenance/{id}", Summary: "Record maintenance cost / notes",
		Permission: "golf.cart_maintenance.manage", Request: MaintenanceUpdate{}, Response: Maintenance{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MaintenanceUpdate) (Maintenance, error) {
			mid, err := id(r)
			if err != nil {
				return Maintenance{}, err
			}
			return m.UpdateMaintenance(ctx, tx, prop(ctx), mid, in)
		})})
	add("Golf Carts", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-cart-maintenance/{id}:release", Summary: "Maintenance release (release inspection; Ready when passed)",
		Permission: "golf.cart_inspection.create", Request: InspectionInput{}, Response: Inspection{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in InspectionInput) (Inspection, error) {
			mid, err := id(r)
			if err != nil {
				return Inspection{}, err
			}
			var cart uuid.UUID
			var status string
			if err := tx.QueryRow(ctx, `SELECT golf_cart_id, status FROM golf.cart_maintenance WHERE id = $1 AND property_id = $2`, mid, prop(ctx)).Scan(&cart, &status); err != nil {
				if dbtx.IsNoRows(err) {
					return Inspection{}, errs.NotFound("maintenance")
				}
				return Inspection{}, err
			}
			if status != "open" {
				return Inspection{}, errs.Conflict("maintenance_closed", "the maintenance is already closed")
			}
			in.Kind = "release"
			return m.Inspect(ctx, tx, prop(ctx), cart, in)
		})})
	m.incidentRoutes(add, "golf-cart-incidents", "golf_cart", "golf.cart_incident")
	add("Golf Carts", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-cart-assignments/{id}:replace", Summary: "Golf Cart Replacement mid-round (replaced cart to inspection)",
		Permission: "golf.golf_cart_assignment.manage", Request: CartReplaceInput{}, Response: Round{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CartReplaceInput) (Round, error) {
			aid, err := id(r)
			if err != nil {
				return Round{}, err
			}
			return m.ReplaceCart(ctx, tx, prop(ctx), aid, in)
		})})
	add("Golf Carts", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-cart-readiness", Summary: "Golf cart readiness board (battery, service hours, position)",
		Permission: "golf.golf_cart.view", Response: Board{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Board, error) {
			return m.ReadinessBoard(ctx, tx, prop(ctx))
		})})
	add("Golf Carts", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-carts/{id}/history", Summary: "Assignment, Inspection, Maintenance & Incident History",
		Permission: "golf.golf_cart.view", Response: CartHistory{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CartHistory, error) {
			cid, err := id(r)
			if err != nil {
				return CartHistory{}, err
			}
			var h CartHistory
			if h.Assignments, err = golf.ListCartAssignments(ctx, tx, "a.golf_cart_id = $1", cid); err != nil {
				return h, err
			}
			if h.Inspections, err = handle.List[Inspection](tx.Query(ctx, inspectionSelect+` WHERE golf_cart_id = $1 ORDER BY inspected_at DESC LIMIT 200`, cid)); err != nil {
				return h, err
			}
			if h.Maintenance, err = handle.List[Maintenance](tx.Query(ctx, maintenanceSelect+` WHERE m.golf_cart_id = $1 ORDER BY m.opened_at DESC`, cid)); err != nil {
				return h, err
			}
			h.Incidents, err = handle.List[Incident](tx.Query(ctx, incidentSelect+` WHERE golf_cart_id = $1 ORDER BY occurred_at DESC`, cid))
			return h, err
		})})
	add("Golf Carts", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-cart-positions", Summary: "Golf cart positions (the caddy tablets' GPS, else estimated)",
		Permission: "golf.golf_cart.view", Response: Position{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Position], error) {
			return handle.Page(m.GPS.Positions(ctx, tx, prop(ctx)))
		})})
	add("Golf Carts", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-cart-positions", Summary: "Ingest golf cart GPS fixes (vendor adapter / bridge agent)",
		Permission: "golf.golf_cart.update", Request: []Position{}, Response: IngestResult{}, Status: http.StatusOK,
		NoAudit: "high-frequency telemetry; stored as the cart's last position",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in []Position) (IngestResult, error) {
			n, err := m.IngestPositions(ctx, tx, prop(ctx), in)
			return IngestResult{Updated: n}, err
		})})

	// ── scoring & handicap (EP-08) ──
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/scorecards", Summary: "Scorecards (staff)", Permission: "golf.scorecard.view_all",
		Response: Scorecard{}, List: true, Query: []route.Param{{Name: "filter[customerId]"}, {Name: "filter[status]"}, {Name: "filter[flightId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Scorecard], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Scorecard](tx.Query(ctx, scorecardSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.customer_id::text = $2)
				AND ($3 = '' OR s.status = $3) AND ($4 = '' OR s.flight_id::text = $4) ORDER BY s.played_on DESC, s.created_at DESC LIMIT $5`,
				prop(ctx), lp.Filters["customerId"], lp.Filters["status"], lp.Filters["flightId"], lp.Limit)))
		})})
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/scorecards/{id}", Summary: "Scorecard (player privacy: staff with view_all or the flight's caddy)",
		Permission: "golf.scorecard.view", Response: Scorecard{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Scorecard, error) {
			sid, err := m.seeCard(ctx, tx, r)
			if err != nil {
				return Scorecard{}, err
			}
			return m.GetScorecard(ctx, tx, sid)
		})})
	add("Scoring", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/scorecards/{id}/scores", Summary: "Score Entry per hole (last write per hole wins)",
		Permission: "golf.scorecard.enter", Request: ScoreInput{}, Response: Scorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ScoreInput) (Scorecard, error) {
			sid, err := m.seeCard(ctx, tx, r)
			if err != nil {
				return Scorecard{}, err
			}
			if err := m.mayScore(ctx, tx, sid); err != nil {
				return Scorecard{}, err
			}
			return m.EnterScores(ctx, tx, sid, in)
		})})
	add("Scoring", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/scorecards/{id}:validate", Summary: "Validate the scorecard (completeness check, marker)",
		Permission: "golf.scorecard.enter", Request: SubmitInput{}, Response: Scorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SubmitInput) (Scorecard, error) {
			sid, err := m.seeCard(ctx, tx, r)
			if err != nil {
				return Scorecard{}, err
			}
			return m.SubmitScorecard(ctx, tx, sid, in)
		})})
	add("Scoring", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/scorecards/{id}:finalize", Summary: "Score Finalization (immutable; handicap, HIO, Hall of Fame)",
		Permission: "golf.scorecard.finalize", Response: Scorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (Scorecard, error) {
			sid, err := id(r)
			if err != nil {
				return Scorecard{}, err
			}
			return m.FinalizeScorecard(ctx, tx, sid)
		})})
	add("Scoring", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/scorecards/{id}/corrections", Summary: "Score Correction of a finalized card (reason required)",
		Permission: "golf.scorecard.correct", Request: CorrectionInput{}, Response: Scorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CorrectionInput) (Scorecard, error) {
			sid, err := id(r)
			if err != nil {
				return Scorecard{}, err
			}
			return m.CorrectScorecard(ctx, tx, sid, in)
		})})
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/scorecards/{id}/corrections", Summary: "Score Audit History (entries and corrections)",
		Permission: "golf.scorecard.view_all", Response: ScoreAudit{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ScoreAudit], error) {
			sid, err := id(r)
			if err != nil {
				return httpx.Page[ScoreAudit]{}, err
			}
			return handle.Page(m.ScoreAuditHistory(ctx, tx, sid))
		})})
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/players/{id}/statistics", Summary: "Round History, Score History, Round Statistics & Handicap of a customer",
		Permission: "golf.scorecard.view_all", Response: RoundStats{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RoundStats, error) {
			cid, err := id(r)
			if err != nil {
				return RoundStats{}, err
			}
			return m.Stats(ctx, tx, cid, httpx.ParseList(r).Limit)
		})})
	add("Scoring", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/players/{id}/official-handicap", Summary: "Record the official (federation) handicap index",
		Permission: "golf.handicap.manage", Request: OfficialHandicapInput{},
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OfficialHandicapInput) (handle.Empty, error) {
			cid, err := id(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, m.SetOfficialHandicap(ctx, tx, prop(ctx), cid, in)
		})})

	// ── Hole-in-One (EP-10) ──
	add("Hole-in-One", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/hole-in-ones", Summary: "Hole-in-One records", Permission: "golf.hio.view",
		Response: HIO{}, List: true, Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[HIO], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[HIO](tx.Query(ctx, hioSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2) ORDER BY r.achieved_on DESC LIMIT $3`,
				prop(ctx), lp.Filters["status"], lp.Limit)))
		})})
	add("Hole-in-One", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/hole-in-ones", Summary: "Record a Hole-in-One (manual)", Permission: "golf.hio.manage",
		Request: HIOInput{}, Response: HIO{}, Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in HIOInput) (HIO, error) {
			return m.CreateHIO(ctx, tx, prop(ctx), in)
		})})
	add("Hole-in-One", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/hole-in-ones/{id}", Summary: "Hole-in-One record", Permission: "golf.hio.view", Response: HIO{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (HIO, error) {
			hid, err := id(r)
			if err != nil {
				return HIO{}, err
			}
			return m.hio(ctx, tx, hid)
		})})
	add("Hole-in-One", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/hole-in-ones/{id}:verify", Summary: "Submit for verification (witnesses, attachments; approval)",
		Permission: "golf.hio.manage", Request: HIOVerifyInput{}, Response: HIO{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in HIOVerifyInput) (HIO, error) {
			hid, err := id(r)
			if err != nil {
				return HIO{}, err
			}
			return m.SubmitHIO(ctx, tx, prop(ctx), hid, in)
		})})
	add("Hole-in-One", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/hole-in-ones/{id}:claim", Summary: "Insurance Claim (claim package PDF)",
		Permission: "golf.hio.claim", Request: ClaimInput{}, Response: HIO{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ClaimInput) (HIO, error) {
			hid, err := id(r)
			if err != nil {
				return HIO{}, err
			}
			return m.ClaimHIO(ctx, tx, prop(ctx), hid, in)
		})})
	add("Hole-in-One", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/hole-in-ones/{id}:update-claim", Summary: "Update claim status / payout",
		Permission: "golf.hio.claim", Request: ClaimUpdateInput{}, Response: HIO{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ClaimUpdateInput) (HIO, error) {
			hid, err := id(r)
			if err != nil {
				return HIO{}, err
			}
			return m.UpdateClaim(ctx, tx, prop(ctx), hid, in)
		})})

	// ── Hall of Fame (EP-11) ──
	for _, pub := range []bool{true, false} {
		publish := pub
		path, summary := "publish", "Publish a Hall of Fame entry (curation)"
		if !publish {
			path, summary = "unpublish", "Unpublish a Hall of Fame entry"
		}
		add("Hall of Fame", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/hall-of-fame/{id}:" + path, Summary: summary,
			Permission: "golf.hall_of_fame.publish",
			Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (handle.Empty, error) {
				eid, err := id(r)
				if err != nil {
					return handle.Empty{}, err
				}
				return handle.Empty{}, m.PublishEntry(ctx, tx, prop(ctx), eid, publish)
			})})
	}
	add("Hall of Fame", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/hall-of-fame/consents", Summary: "Record the player's opt-in / withdrawal for an entry",
		Permission: "golf.hall_of_fame.update", Request: HallOfFameConsentInput{},
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in HallOfFameConsentInput) (handle.Empty, error) {
			return handle.Empty{}, m.SetEntryConsent(ctx, tx, prop(ctx), in.EntryID, in.Consent, nil)
		})})
	pub := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Auth = "golf", "Website", route.AuthPublic
		reg.Add(rt)
	}
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/hall-of-fame", Summary: "Hall of Fame (website; consented & published entries only)",
		Response: PublicEntry{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "category"}},
		Handler: publicLimiter.Wrap(m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			return handle.Page(m.PublicEntries(ctx, tx, pid, r.URL.Query().Get("category")))
		}))})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/hall-of-fame/kiosk", Summary: "Clubhouse Screen feed (kiosk, read-only, rotating)",
		Response: Kiosk{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: publicLimiter.Wrap(m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			pol, err := m.hofPolicy(ctx, tx, pid)
			if err != nil {
				return nil, err
			}
			es, err := m.PublicEntries(ctx, tx, pid, "")
			return Kiosk{RotateSeconds: pol.KioskRotateSecs, Entries: es}, err
		}))})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/reciprocal-clubs", Summary: "Reciprocal Clubs (website)",
		Response: PublicClub{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: publicLimiter.Wrap(m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			return handle.Page(handle.List[PublicClub](tx.Query(ctx, `SELECT code, name, country, city FROM golf.reciprocal_clubs WHERE property_id = $1
				AND status = 'active' AND archived_at IS NULL AND (agreement_to IS NULL OR agreement_to >= billing.local_date($1)) ORDER BY country, name`, pid)))
		}))})

	// ── Driving Range (EP-12) ──
	add("Driving Range", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/range-sessions", Summary: "Range sessions & queue", Permission: "golf.range.view",
		Response: RangeSession{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[area]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RangeSession], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[RangeSession](tx.Query(ctx, sessionSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.status = $2) AND ($3 = '' OR s.area = $3)
				AND (s.status IN ('waiting', 'active') OR s.queued_at > now() - interval '1 day') ORDER BY s.queued_at LIMIT $4`,
				prop(ctx), lp.Filters["status"], lp.Filters["area"], lp.Limit)))
		})})
	add("Driving Range", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/range-sessions", Summary: "Walk-in: assign a bay or join the queue",
		Permission: "golf.range.operate", Request: RangeSessionInput{}, Response: RangeSession{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RangeSessionInput) (RangeSession, error) {
			return m.StartSession(ctx, tx, prop(ctx), in)
		})})
	add("Driving Range", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/range-sessions/{id}:assign-bay", Summary: "Bay Assignment for a waiting guest",
		Permission: "golf.range.operate", Request: AssignBayInput{}, Response: RangeSession{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AssignBayInput) (RangeSession, error) {
			sid, err := id(r)
			if err != nil {
				return RangeSession{}, err
			}
			return m.AssignBay(ctx, tx, prop(ctx), sid, in)
		})})
	for _, c := range []bool{false, true} {
		cancel := c
		path := "end"
		if cancel {
			path = "cancel"
		}
		add("Driving Range", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/range-sessions/{id}:" + path, Summary: "End / cancel a range session (frees the bay)",
			Permission: "golf.range.operate", Response: RangeSession{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (RangeSession, error) {
				sid, err := id(r)
				if err != nil {
					return RangeSession{}, err
				}
				return m.EndSession(ctx, tx, prop(ctx), sid, cancel)
			})})
	}
	add("Driving Range", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/range-buckets", Summary: "Issue a bucket (POS sale, prepaid ball balance, complimentary)",
		Permission: "golf.range.operate", Request: BucketInput{}, Response: Bucket{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BucketInput) (Bucket, error) {
			return m.IssueBucket(ctx, tx, prop(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	add("Driving Range", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/range-usage", Summary: "Range Usage: buckets, balls, bay utilisation, revenue",
		Permission: "golf.range.view", Response: RangeUsage{}, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RangeUsage, error) {
			from, to, err := dateRange(ctx, tx, r, 0)
			if err != nil {
				return RangeUsage{}, err
			}
			return m.RangeUsage(ctx, tx, prop(ctx), from, to)
		})})

	// ── Reciprocal Club (EP-13) ──
	add("Reciprocal Club", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/reciprocal-visits", Summary: "Member Verification of a reciprocal guest",
		Permission: "golf.reciprocal_visit.verify", Request: InboundInput{}, Response: Visit{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in InboundInput) (Visit, error) {
			return m.VerifyInbound(ctx, tx, prop(ctx), in)
		})})
	add("Reciprocal Club", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/reciprocal-visits", Summary: "Reciprocal Visits (inbound & outbound)",
		Permission: "golf.reciprocal_visit.view", Response: Visit{}, List: true,
		Query: []route.Param{{Name: "filter[direction]"}, {Name: "filter[clubId]"}, {Name: "filter[settlementStatus]"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Visit], error) {
			lp := httpx.ParseList(r)
			from, to, err := dateRange(ctx, tx, r, 365)
			if err != nil {
				return httpx.Page[Visit]{}, err
			}
			return handle.Page(m.Visits(ctx, tx, prop(ctx), lp.Filters["direction"], lp.Filters["clubId"], lp.Filters["settlementStatus"],
				from, to.AddDate(0, 0, 1), lp.Limit))
		})})
	add("Reciprocal Club", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/reciprocal-visits/{id}:link-player", Summary: "Link the verified visit to the reciprocal booking player",
		Permission: "golf.reciprocal_visit.verify", Request: LinkPlayerInput{}, Response: Visit{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LinkPlayerInput) (Visit, error) {
			vid, err := id(r)
			if err != nil {
				return Visit{}, err
			}
			return m.LinkPlayer(ctx, tx, prop(ctx), vid, in.PlayerID)
		})})
	add("Reciprocal Club", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/reciprocal-visits:settle", Summary: "Club Settlement: mark visits invoiced / settled",
		Permission: "golf.reciprocal_club.update", Request: SettlementMarkInput{}, Response: CountResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SettlementMarkInput) (CountResult, error) {
			n, err := m.MarkSettlement(ctx, tx, prop(ctx), in)
			return CountResult{Count: n}, err
		})})
	add("Reciprocal Club", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/introduction-letters", Summary: "Request an Introduction Letter (approval)",
		Permission: "golf.introduction_letter.request", Request: LetterInput{}, Response: Letter{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LetterInput) (Letter, error) {
			return m.RequestLetter(ctx, tx, prop(ctx), in)
		})})
	add("Reciprocal Club", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/introduction-letters", Summary: "Introduction letters",
		Permission: "golf.introduction_letter.view", Response: Letter{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Letter], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Letter](tx.Query(ctx, letterSelect+` WHERE l.property_id = $1 AND ($2 = '' OR l.status = $2)
				AND ($3 = '' OR l.customer_id::text = $3) ORDER BY l.created_at DESC LIMIT $4`, prop(ctx), lp.Filters["status"], lp.Filters["customerId"], lp.Limit)))
		})})
	add("Reciprocal Club", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/introduction-letters/{id}:issue", Summary: "Issue an approved letter (PDF)",
		Permission: "golf.introduction_letter.issue", Response: Letter{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (Letter, error) {
			lid, err := id(r)
			if err != nil {
				return Letter{}, err
			}
			return m.IssueLetter(ctx, tx, prop(ctx), lid)
		})})
}

// incidentRoutes registers the caddy or golf cart incident routes.
func (m *Module) incidentRoutes(add func(string, route.Route), path, subject, perm string) {
	db := m.DB
	tag := "Caddies"
	if subject == "golf_cart" {
		tag = "Golf Carts"
	}
	add(tag, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/" + path, Summary: "Report a " + map[string]string{"caddy": "caddy", "golf_cart": "golf cart"}[subject] + " incident",
		Permission: perm + ".create", Request: IncidentInput{}, Response: Incident{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in IncidentInput) (Incident, error) {
			in.SubjectType = subject
			return m.ReportIncident(ctx, tx, handle.Property(ctx), in)
		})})
	add(tag, route.Route{Method: http.MethodGet, Path: "/api/v1/golf/" + path, Summary: "Incidents", Permission: perm + ".view",
		Response: Incident{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[caddyId]"}, {Name: "filter[golfCartId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Incident], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Incident](tx.Query(ctx, incidentSelect+` WHERE property_id = $1 AND subject_type = $2 AND ($3 = '' OR status = $3)
				AND ($4 = '' OR caddy_id::text = $4) AND ($5 = '' OR golf_cart_id::text = $5) ORDER BY occurred_at DESC LIMIT $6`, handle.Property(ctx),
				subject, lp.Filters["status"], lp.Filters["caddyId"], lp.Filters["golfCartId"], lp.Limit)))
		})})
	add(tag, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/" + path + "/{id}:close", Summary: "Close an incident with the action taken",
		Permission: perm + ".manage", Request: ReasonInput{}, Response: Incident{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Incident, error) {
			iid, err := handle.ID(r)
			if err != nil {
				return Incident{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return Incident{}, err
			}
			pid := handle.Property(ctx)
			tag, err := tx.Exec(ctx, `UPDATE golf.incidents SET status = 'closed', action_taken = coalesce(action_taken || E'\n', '') || $4
				WHERE id = $1 AND property_id = $2 AND subject_type = $3`, iid, pid, subject, in.Reason)
			if err != nil {
				return Incident{}, err
			}
			if tag.RowsAffected() == 0 {
				return Incident{}, errs.NotFound("incident")
			}
			inc, err := handle.Get[Incident](tx.Query(ctx, incidentSelect+` WHERE id = $1`, iid))
			if err != nil {
				return inc, err
			}
			return inc, record(ctx, tx, "golf.incident", iid, inc.Number, audit.ActionStatusChange, pid, nil, inc, in.Reason)
		})})
}

// holeAssets lists the P1 course assets of a hole.
func holeAssets(ctx context.Context, q dbtx.Querier, hole uuid.UUID) ([]AssetInfo, error) {
	var course uuid.UUID
	if err := q.QueryRow(ctx, `SELECT course_id FROM golf.holes WHERE id = $1`, hole).Scan(&course); err != nil {
		if dbtx.IsNoRows(err) {
			return nil, errs.NotFound("hole")
		}
		return nil, err
	}
	// the hole's own pictures before the course map
	rows, err := q.Query(ctx, `SELECT code, asset_type, name, hole_id, `+assetImage+` FROM golf.course_assets WHERE course_id = $1 AND (hole_id = $2 OR (hole_id IS NULL AND asset_type = 'course_map'))
		AND status = 'active' AND archived_at IS NULL ORDER BY asset_type, hole_id IS NULL, code`, course, hole)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetInfo{}
	for rows.Next() {
		var a AssetInfo
		if err := rows.Scan(&a.Code, &a.AssetType, &a.Name, &a.HoleID, &a.FileURL); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// seeFlight parses {id} and checks the caller may operate the flight.
func (m *Module) seeFlight(ctx context.Context, tx pgx.Tx, r *http.Request) (uuid.UUID, error) {
	fid, err := handle.ID(r)
	if err != nil {
		return fid, err
	}
	f, err := m.GetRound(ctx, tx, fid)
	if err != nil {
		return fid, err
	}
	return fid, m.canSeeFlight(ctx, tx, f)
}

// seeCard enforces score privacy: staff with view_all, or the caddy of the
// card's flight (FR-SCR-09). Members use their own /member routes.
func (m *Module) seeCard(ctx context.Context, tx pgx.Tx, r *http.Request) (uuid.UUID, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return sid, err
	}
	var pid uuid.UUID
	var flight *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id, flight_id FROM golf.scorecards WHERE id = $1`, sid).Scan(&pid, &flight); err != nil {
		if dbtx.IsNoRows(err) {
			return sid, errs.NotFound("scorecard")
		}
		return sid, err
	}
	if can(ctx, "golf.scorecard.view_all", pid) {
		return sid, nil
	}
	if flight != nil {
		f, err := m.GetRound(ctx, tx, *flight)
		if err != nil {
			return sid, err
		}
		if err := m.canSeeFlight(ctx, tx, f); err == nil {
			return sid, nil
		}
	}
	return sid, errs.Forbidden("scores are private to the player")
}

// publicRead runs a read-only public handler for the propertyId query.
func (m *Module) publicRead(fn func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
			return
		}
		ctx := dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{pid}})
		var out any
		err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			var err error
			out, err = fn(ctx, tx, pid, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}
