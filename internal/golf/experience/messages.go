package experience

// Course messenger (demo feedback 9 Oct 2026, OneClub replaces Smartscore's
// cart messenger): two-way messages between a flight's caddy tablet and
// course control — the Marshal (Operational › Starter / Marshal) and the
// back office (Golf › Course Monitor). Course control writes to one flight
// or to every flight on the course; each tablet confirms what it read, and
// course control sees the replies with the unread ones marked. The Marshal's
// pace interventions (reminder, warning …) stay as they are.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// CourseMessage is a message between a caddy tablet and course control.
type CourseMessage struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	FlightID       uuid.UUID  `json:"flightId" db:"flight_id"`
	FlightLabel    string     `json:"flightLabel" db:"flight_label"`
	Sender         string     `json:"sender" db:"sender" enum:"tablet,marshal,office"`
	SenderName     *string    `json:"senderName" db:"sender_name"`
	Body           string     `json:"body" db:"body"`
	Broadcast      bool       `json:"broadcast" db:"broadcast" doc:"Sent to every flight on the course"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
	ReadByTabletAt *time.Time `json:"readByTabletAt" db:"read_by_tablet_at"`
	ReadByCourseAt *time.Time `json:"readByCourseAt" db:"read_by_course_at"`
}

const messageSelect = `SELECT m.id, m.flight_id, coalesce(b.code, 'Flight ' || f.flight_no) AS flight_label, m.sender, m.sender_name, m.body, m.broadcast,
	m.created_at, m.read_by_tablet_at, m.read_by_course_at
	FROM golf.course_messages m JOIN golf.flights f ON f.id = m.flight_id LEFT JOIN golf.bookings b ON b.id = f.booking_id`

// TabletMessageInput is a message from the caddy tablet.
type TabletMessageInput struct {
	Body string `json:"body"`
}

// CourseMessageInput is a message from course control.
type CourseMessageInput struct {
	CourseID uuid.UUID  `json:"courseId"`
	FlightID *uuid.UUID `json:"flightId,omitempty" doc:"Empty: every flight playing on the course"`
	Body     string     `json:"body"`
	Desk     string     `json:"desk" enum:"marshal,office" doc:"Who writes: the Marshal (Operational) or the back office"`
}

// MessageReadInput marks a flight's messages read by course control.
type MessageReadInput struct {
	FlightID uuid.UUID `json:"flightId"`
}

func messageBody(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > 500 {
		return "", handle.Invalid("body", "invalid", "1 – 500 characters")
	}
	return s, nil
}

func userName(ctx context.Context, q dbtx.Querier) *string {
	var n *string
	_ = q.QueryRow(ctx, `SELECT full_name FROM platform.users WHERE id = $1`, handle.UserID(ctx)).Scan(&n)
	return n
}

func (m *Module) flightMessages(ctx context.Context, q dbtx.Querier, fid uuid.UUID) ([]CourseMessage, error) {
	return handle.List[CourseMessage](q.Query(ctx, messageSelect+` WHERE m.flight_id = $1 ORDER BY m.created_at`, fid))
}

func (m *Module) insertMessage(ctx context.Context, tx pgx.Tx, property, course, fid uuid.UUID, sender string, name *string, body string, broadcast bool) (uuid.UUID, error) {
	mid := id.New()
	_, err := tx.Exec(ctx, `INSERT INTO golf.course_messages (id, property_id, course_id, flight_id, sender, sender_name, body, broadcast, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, mid, property, course, fid, sender, name, body, broadcast, actorPtr(ctx))
	return mid, err
}

// TabletMessage sends a message from the flight's tablet to course control.
func (m *Module) TabletMessage(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in TabletMessageInput) ([]CourseMessage, error) {
	r, err := m.GetRound(ctx, tx, fid)
	if err != nil {
		return nil, err
	}
	if err := m.canSeeFlight(ctx, tx, r); err != nil {
		return nil, err
	}
	body, err := messageBody(in.Body)
	if err != nil {
		return nil, err
	}
	name := userName(ctx, tx)
	if c, err := m.myCaddy(ctx, tx, r.PropertyID); err == nil {
		n := "Caddy " + c.Name
		name = &n
	}
	mid, err := m.insertMessage(ctx, tx, r.PropertyID, r.CourseID, fid, "tablet", name, body, false)
	if err != nil {
		return nil, err
	}
	if err := record(ctx, tx, "golf.course_message", mid, body, "send", r.PropertyID, nil, map[string]any{"flightId": fid, "from": "tablet"}, ""); err != nil {
		return nil, err
	}
	if err := m.live(ctx, tx, "golf.pace", r.PropertyID, "message", fid.String(), map[string]any{"from": "tablet"}); err != nil {
		return nil, err
	}
	return m.flightMessages(ctx, tx, fid)
}

// TabletRead marks course control's messages read on the tablet.
func (m *Module) TabletRead(ctx context.Context, tx pgx.Tx, fid uuid.UUID) ([]CourseMessage, error) {
	r, err := m.GetRound(ctx, tx, fid)
	if err != nil {
		return nil, err
	}
	if err := m.canSeeFlight(ctx, tx, r); err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE golf.course_messages SET read_by_tablet_at = $2 WHERE flight_id = $1 AND sender <> 'tablet' AND read_by_tablet_at IS NULL`,
		fid, clock.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() > 0 {
		if err := m.live(ctx, tx, "golf.pace", r.PropertyID, "message_read", fid.String(), nil); err != nil {
			return nil, err
		}
	}
	return m.flightMessages(ctx, tx, fid)
}

// SendCourseMessage sends course control's message to a flight or to every
// flight playing on the course.
func (m *Module) SendCourseMessage(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CourseMessageInput) ([]CourseMessage, error) {
	if in.Desk != "marshal" && in.Desk != "office" {
		return nil, handle.Invalid("desk", "invalid", "marshal or office")
	}
	body, err := messageBody(in.Body)
	if err != nil {
		return nil, err
	}
	var flights []uuid.UUID
	if in.FlightID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.flights WHERE id = $1 AND course_id = $2 AND property_id = $3)`,
			*in.FlightID, in.CourseID, property).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.NotFound("flight")
		}
		flights = []uuid.UUID{*in.FlightID}
	} else {
		// the flights playing now: teed off within half a day and not finished
		if flights, err = collectIDs(tx.Query(ctx, `SELECT id FROM golf.flights WHERE course_id = $1 AND property_id = $2 AND tee_off_at > $3
			AND round_finish_at IS NULL AND status <> 'cancelled' ORDER BY tee_off_at`, in.CourseID, property, clock.Now().Add(-12*time.Hour))); err != nil {
			return nil, err
		}
		if len(flights) == 0 {
			return nil, errs.Conflict("no_flight_in_play", "no flight is playing on this course")
		}
	}
	name := userName(ctx, tx)
	var ids []uuid.UUID
	for _, fid := range flights {
		mid, err := m.insertMessage(ctx, tx, property, in.CourseID, fid, in.Desk, name, body, in.FlightID == nil)
		if err != nil {
			return nil, err
		}
		ids = append(ids, mid)
	}
	if err := record(ctx, tx, "golf.course_message", ids[0], body, "send", property, nil, map[string]any{"flights": flights, "from": in.Desk}, ""); err != nil {
		return nil, err
	}
	if err := m.live(ctx, tx, "golf.pace", property, "message", "", map[string]any{"from": in.Desk, "flights": len(flights)}); err != nil {
		return nil, err
	}
	return handle.List[CourseMessage](tx.Query(ctx, messageSelect+` WHERE m.id = ANY($1) ORDER BY m.created_at`, ids))
}

func (m *Module) registerMessages(add func(tag string, rt route.Route)) {
	db := m.DB
	flight := func(fn func(ctx context.Context, tx pgx.Tx, fid uuid.UUID) ([]CourseMessage, error)) func(context.Context, pgx.Tx, *http.Request) (httpx.Page[CourseMessage], error) {
		return func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CourseMessage], error) {
			fid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[CourseMessage]{}, err
			}
			return handle.Page(fn(ctx, tx, fid))
		}
	}
	add("Caddy Tablet", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/rounds/{id}/messages", Summary: "Messages between the flight's tablet and course control",
		Permission: "golf.round.operate", Response: CourseMessage{}, List: true,
		Handler: handle.Read(db, flight(func(ctx context.Context, tx pgx.Tx, fid uuid.UUID) ([]CourseMessage, error) {
			r, err := m.GetRound(ctx, tx, fid)
			if err != nil {
				return nil, err
			}
			if err := m.canSeeFlight(ctx, tx, r); err != nil {
				return nil, err
			}
			return m.flightMessages(ctx, tx, fid)
		}))})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}/messages", Summary: "Caddy tablet: message to the Marshal and the back office",
		Permission: "golf.round.operate", Request: TabletMessageInput{}, Response: CourseMessage{}, List: true, Status: http.StatusCreated,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TabletMessageInput) (httpx.Page[CourseMessage], error) {
			fid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[CourseMessage]{}, err
			}
			return handle.Page(m.TabletMessage(ctx, tx, fid, in))
		})})
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}/messages:read", Summary: "Caddy tablet: the course control messages were read",
		Permission: "golf.round.operate", Response: CourseMessage{}, List: true, Status: http.StatusOK, NoAudit: "read receipt",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (httpx.Page[CourseMessage], error) {
			fid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[CourseMessage]{}, err
			}
			return handle.Page(m.TabletRead(ctx, tx, fid))
		})})
	add("Pace of Play", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/course-messages", Summary: "Course control: today's messages with the caddy tablets of a course",
		Permission: "golf.pace.view", Response: CourseMessage{}, List: true, Query: []route.Param{{Name: "courseId", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CourseMessage], error) {
			cid, err := handle.QueryUUID(r, "courseId")
			if err != nil || cid == nil {
				return httpx.Page[CourseMessage]{}, handle.Invalid("courseId", "required", "course")
			}
			p := handle.Property(ctx)
			loc := location(ctx, tx, p)
			d := localDay(clock.Now(), loc)
			start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			return handle.Page(handle.List[CourseMessage](tx.Query(ctx, messageSelect+` WHERE m.course_id = $1 AND m.property_id = $2 AND m.created_at >= $3
				ORDER BY m.created_at`, *cid, p, start)))
		})})
	add("Pace of Play", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/course-messages", Summary: "Course control: message to a flight's caddy tablet or to every flight on the course",
		Permission: "golf.pace.manage", Request: CourseMessageInput{}, Response: CourseMessage{}, List: true, Status: http.StatusCreated,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CourseMessageInput) (httpx.Page[CourseMessage], error) {
			return handle.Page(m.SendCourseMessage(ctx, tx, handle.Property(ctx), in))
		})})
	add("Pace of Play", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/course-messages:read", Summary: "Course control: a flight's tablet messages were read",
		Permission: "golf.pace.view", Request: MessageReadInput{}, Response: CourseMessage{}, List: true, Status: http.StatusOK, NoAudit: "read receipt",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MessageReadInput) (httpx.Page[CourseMessage], error) {
			p := handle.Property(ctx)
			tag, err := tx.Exec(ctx, `UPDATE golf.course_messages SET read_by_course_at = $3 WHERE flight_id = $1 AND property_id = $2 AND sender = 'tablet'
				AND read_by_course_at IS NULL`, in.FlightID, p, clock.Now())
			if err != nil {
				return httpx.Page[CourseMessage]{}, err
			}
			if tag.RowsAffected() > 0 {
				if err := m.live(ctx, tx, "golf.pace", p, "message_read", in.FlightID.String(), nil); err != nil {
					return httpx.Page[CourseMessage]{}, err
				}
			}
			return handle.Page(m.flightMessages(ctx, tx, in.FlightID))
		})})
}
