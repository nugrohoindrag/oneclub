package experience

// Course Guide, Course Handicap and the Marshal's Course Monitor (PRD P2
// FR-PLX-01 course map per hole in the Member App and the caddy tablet,
// FR-PLX-04 pace of play on the Starter / Marshal screen, FR-PLX-05 golf
// carts on the map; roadmap §70 Hole by Hole and Handicap Index).
//
// The course map is a P1 course asset: the overview map (course_map without
// a hole) is geo-referenced by its geometry (a GeoJSON Polygon of the image
// corners) and every hole has a green_center point, so holes, flights and
// GPS golf carts land on the image without a map vendor.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// CourseHandicap is the WHS course handicap: index × slope / 113 + (course
// rating − par), half the index and the rating for a nine-hole round.
func CourseHandicap(index decimal.Decimal, rating *string, slope *int, par, holes int) int {
	r := decimal.Zero
	if rating != nil {
		r = dec(*rating)
	}
	if holes <= 9 {
		index = index.Div(decimal.NewFromInt(2))
		r = r.Div(decimal.NewFromInt(2))
	}
	ch := index
	if slope != nil && rating != nil && *slope > 0 {
		ch = index.Mul(decimal.NewFromInt(int64(*slope))).Div(decimal.NewFromInt(113)).Add(r.Sub(decimal.NewFromInt(int64(par))))
	}
	return int(ch.Round(0).IntPart())
}

// HoleStrokes spreads a course handicap over the holes by stroke index
// (lowest index first; within a nine-hole route the indexes are ranked).
// A plus handicap gives strokes back from the easiest holes.
func HoleStrokes(ch int, strokeIndex []*int) []int {
	n := len(strokeIndex)
	out := make([]int, n)
	if n == 0 || ch == 0 {
		return out
	}
	order := make([]int, n) // order[k] = position of the k-th hardest hole
	for i := range order {
		order[i] = i
	}
	si := func(i int) int {
		if strokeIndex[i] == nil {
			return 99 + i
		}
		return *strokeIndex[i]
	}
	for i := 1; i < n; i++ {
		for j := i; j > 0 && si(order[j]) < si(order[j-1]); j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	abs, sign := ch, 1
	if ch < 0 {
		abs, sign = -ch, -1
	}
	for k := 0; k < abs; k++ {
		pos := k % n
		if sign < 0 {
			pos = n - 1 - pos
		}
		out[order[pos]] += sign
	}
	return out
}

// ── course guide ──────────────────────────────────────────────────────────

// GuideImage is a picture of a hole or of the course (P1 course asset).
type GuideImage struct {
	Code string `json:"code"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// GuideTee is a tee set with its length and the viewer's course handicap.
type GuideTee struct {
	ID              uuid.UUID `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Color           *string   `json:"color"`
	Gender          string    `json:"gender" enum:"male,female,any"`
	PlayerCategory  *string   `json:"playerCategory" enum:"women,general,professional,senior,junior" doc:"Red women, blue / white general, black professional"`
	CourseRating    *string   `json:"courseRating"`
	Slope           *int      `json:"slope"`
	LengthMeters    int       `json:"lengthMeters"`
	CourseHandicap  *int      `json:"courseHandicap" doc:"Course handicap of the signed-in member from this tee (18 holes)"`
	CourseHandicap9 *int      `json:"courseHandicap9" doc:"Same for a nine-hole round"`
}

// GuideHole is one hole of the Hole-by-Hole guide.
type GuideHole struct {
	HoleID      uuid.UUID      `json:"holeId"`
	Number      int            `json:"number"`
	SectionCode string         `json:"sectionCode"`
	Par         int            `json:"par"`
	StrokeIndex *int           `json:"strokeIndex"`
	Distances   map[string]int `json:"distances" doc:"Meters per tee set code"`
	Description *string        `json:"description"`
	Images      []GuideImage   `json:"images"`
	MapX        *float64       `json:"mapX" doc:"Position on the course map, 0–1 from the left"`
	MapY        *float64       `json:"mapY" doc:"Position on the course map, 0–1 from the top"`
}

// CourseGuide is the Hole-by-Hole guide of a course.
type CourseGuide struct {
	CourseID      uuid.UUID   `json:"courseId"`
	Code          string      `json:"code"`
	Name          string      `json:"name"`
	Description   *string     `json:"description"`
	Guide         *string     `json:"guide"`
	Par           int         `json:"par"`
	LengthMeters  *int        `json:"lengthMeters"`
	MapURL        *string     `json:"mapUrl" doc:"Overview course map"`
	HandicapIndex *string     `json:"handicapIndex" doc:"Of the signed-in member"`
	TeeSets       []GuideTee  `json:"teeSets" doc:"Longest first"`
	Holes         []GuideHole `json:"holes"`
}

// courseMap is the geo-referenced overview map of a course.
type courseMap struct {
	url                      *string
	west, east, north, south float64
	ok                       bool
}

func (c courseMap) project(lat, lng float64) (*float64, *float64) {
	if !c.ok {
		return nil, nil
	}
	x, y := (lng-c.west)/(c.east-c.west), (c.north-lat)/(c.north-c.south)
	if x < 0 || x > 1 || y < 0 || y > 1 {
		return nil, nil
	}
	return &x, &y
}

func loadCourseMap(ctx context.Context, q dbtx.Querier, course uuid.UUID) (courseMap, error) {
	var out courseMap
	var geom map[string]any
	err := q.QueryRow(ctx, `SELECT `+assetImage+`, geometry FROM golf.course_assets WHERE course_id = $1 AND hole_id IS NULL AND asset_type = 'course_map'
		AND status = 'active' AND archived_at IS NULL ORDER BY code LIMIT 1`, course).Scan(&out.url, &geom)
	if dbtx.IsNoRows(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if geom["type"] != "Polygon" {
		return out, nil
	}
	rings, _ := geom["coordinates"].([]any)
	if len(rings) == 0 {
		return out, nil
	}
	ring, _ := rings[0].([]any)
	first := true
	for _, v := range ring {
		p, _ := v.([]any)
		if len(p) < 2 {
			continue
		}
		lng, ok1 := p[0].(float64)
		lat, ok2 := p[1].(float64)
		if !ok1 || !ok2 {
			continue
		}
		if first {
			out.west, out.east, out.north, out.south, first = lng, lng, lat, lat, false
			continue
		}
		out.west, out.east = min(out.west, lng), max(out.east, lng)
		out.north, out.south = max(out.north, lat), min(out.south, lat)
	}
	out.ok = !first && out.east > out.west && out.north > out.south
	return out, nil
}

// assetImage is the picture of a course asset: the uploaded photo, else
// P1's file.
const assetImage = `coalesce(image_url, '/api/v1/files/' || file_id::text)`

// courseAssetImage lets the Back Office upload course maps and hole layouts
// like product photos (golf 00009).
var courseAssetImage = resource.Field{Name: "imageUrl", Column: "image_url", Label: "Picture (course map, hole layout)", Kind: resource.Image, Max: 500}

func init() {
	fs := golf.CourseAssets.Fields
	for i, f := range fs {
		if f.Name == "status" {
			golf.CourseAssets.Fields = append(append(append([]resource.Field{}, fs[:i]...), courseAssetImage), fs[i:]...)
			return
		}
	}
	golf.CourseAssets.Fields = append(fs, courseAssetImage)
}

// defaultCourse is the course asked for, else the first active course with
// holes.
func defaultCourse(ctx context.Context, q dbtx.Querier, property uuid.UUID, want string) (uuid.UUID, error) {
	var cid uuid.UUID
	err := q.QueryRow(ctx, `SELECT c.id FROM golf.courses c WHERE c.property_id = $1 AND c.status = 'active' AND c.archived_at IS NULL
		AND ($2 = '' OR c.id::text = $2)
		ORDER BY EXISTS (SELECT 1 FROM golf.holes h WHERE h.course_id = c.id) DESC, c.name LIMIT 1`, property, want).Scan(&cid)
	if dbtx.IsNoRows(err) {
		return cid, errs.NotFound("course")
	}
	return cid, err
}

// Guide loads the Hole-by-Hole guide; with a handicap index every tee gets
// the course handicap.
func (m *Module) Guide(ctx context.Context, q dbtx.Querier, course uuid.UUID, index *string) (CourseGuide, error) {
	g := CourseGuide{CourseID: course, HandicapIndex: index, TeeSets: []GuideTee{}, Holes: []GuideHole{}}
	if err := q.QueryRow(ctx, `SELECT code, name, description, guide, length_meters FROM golf.courses WHERE id = $1`, course).
		Scan(&g.Code, &g.Name, &g.Description, &g.Guide, &g.LengthMeters); err != nil {
		if dbtx.IsNoRows(err) {
			return g, errs.NotFound("course")
		}
		return g, err
	}
	cm, err := loadCourseMap(ctx, q, course)
	if err != nil {
		return g, err
	}
	g.MapURL = cm.url
	rows, err := q.Query(ctx, `SELECT h.id, h.number, s.code, h.par, h.stroke_index, h.distances, h.description,
		(SELECT ca.geometry FROM golf.course_assets ca WHERE ca.hole_id = h.id AND ca.asset_type = 'green_center' AND ca.status = 'active' AND ca.archived_at IS NULL LIMIT 1)
		FROM golf.holes h JOIN golf.course_sections s ON s.id = h.section_id
		WHERE h.course_id = $1 AND h.status = 'active' AND h.archived_at IS NULL ORDER BY h.number`, course)
	if err != nil {
		return g, err
	}
	byID := map[uuid.UUID]int{}
	for rows.Next() {
		var h GuideHole
		var dist json.RawMessage
		var green map[string]any
		if err := rows.Scan(&h.HoleID, &h.Number, &h.SectionCode, &h.Par, &h.StrokeIndex, &dist, &h.Description, &green); err != nil {
			rows.Close()
			return g, err
		}
		h.Distances, h.Images = map[string]int{}, []GuideImage{}
		_ = json.Unmarshal(dist, &h.Distances)
		if lat, lng, ok := geoPoint(green); ok {
			h.MapX, h.MapY = cm.project(lat, lng)
		}
		g.Par += h.Par
		byID[h.HoleID] = len(g.Holes)
		g.Holes = append(g.Holes, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return g, err
	}
	type img struct {
		HoleID uuid.UUID `db:"hole_id"`
		Code   string    `db:"code"`
		Name   string    `db:"name"`
		URL    string    `db:"url"`
	}
	imgs, err := handle.List[img](q.Query(ctx, `SELECT hole_id, code, name, `+assetImage+` AS url FROM golf.course_assets WHERE course_id = $1 AND hole_id IS NOT NULL
		AND (image_url IS NOT NULL OR file_id IS NOT NULL) AND asset_type IN ('course_map', 'panorama') AND status = 'active' AND archived_at IS NULL ORDER BY code`, course))
	if err != nil {
		return g, err
	}
	for _, a := range imgs {
		if i, ok := byID[a.HoleID]; ok {
			g.Holes[i].Images = append(g.Holes[i].Images, GuideImage{Code: a.Code, Name: a.Name, URL: a.URL})
		}
	}
	tees, err := q.Query(ctx, `SELECT id, code, name, color, coalesce(gender, 'any'), player_category, course_rating::text, slope FROM golf.tee_sets
		WHERE course_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY sequence, code`, course)
	if err != nil {
		return g, err
	}
	for tees.Next() {
		var t GuideTee
		if err := tees.Scan(&t.ID, &t.Code, &t.Name, &t.Color, &t.Gender, &t.PlayerCategory, &t.CourseRating, &t.Slope); err != nil {
			tees.Close()
			return g, err
		}
		for _, h := range g.Holes {
			t.LengthMeters += h.Distances[t.Code]
		}
		if index != nil {
			hi := dec(*index)
			ch, ch9 := CourseHandicap(hi, t.CourseRating, t.Slope, g.Par, len(g.Holes)), CourseHandicap(hi, t.CourseRating, t.Slope, g.Par/2, 9)
			t.CourseHandicap, t.CourseHandicap9 = &ch, &ch9
		}
		g.TeeSets = append(g.TeeSets, t)
	}
	tees.Close()
	if err := tees.Err(); err != nil {
		return g, err
	}
	for i := 1; i < len(g.TeeSets); i++ { // longest first: Black, Blue, White, Red
		for j := i; j > 0 && g.TeeSets[j].LengthMeters > g.TeeSets[j-1].LengthMeters; j-- {
			g.TeeSets[j], g.TeeSets[j-1] = g.TeeSets[j-1], g.TeeSets[j]
		}
	}
	return g, nil
}

// ── marshal: interventions & course monitor ───────────────────────────────

// Intervention kinds with the message the caddy tablet shows by default.
var interventionMessages = map[string]string{
	"reminder":      "Friendly reminder from the Marshal: please keep up with the flight ahead.",
	"warning":       "Marshal warning: your flight is behind the pace of play. Please speed up.",
	"final_warning": "Final warning from the Marshal: please pick up and move on to the next hole.",
	"skip_hole":     "Marshal: please skip to the next hole to restore the pace of play.",
	"play_through":  "Marshal: please let the flight behind play through.",
	"note":          "",
}

// PaceIntervention is what the Starter / Marshal did about a flight.
type PaceIntervention struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	FlightID       uuid.UUID  `json:"flightId" db:"flight_id"`
	FlightLabel    string     `json:"flightLabel" db:"flight_label"`
	Kind           string     `json:"kind" db:"kind" enum:"reminder,warning,final_warning,skip_hole,play_through,note"`
	Message        string     `json:"message" db:"message"`
	CurrentSeq     *int       `json:"currentSeq" db:"current_seq"`
	Hole           *string    `json:"hole" db:"hole"`
	BehindMinutes  *int       `json:"behindMinutes" db:"behind_minutes"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
	CreatedByName  *string    `json:"createdByName" db:"created_by_name"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt" db:"acknowledged_at"`
}

// InterventionInput records an intervention; the message defaults per kind.
type InterventionInput struct {
	Kind    string `json:"kind" enum:"reminder,warning,final_warning,skip_hole,play_through,note"`
	Message string `json:"message,omitempty" doc:"Required for a note; default text for the other kinds"`
}

const interventionSelect = `SELECT i.id, i.flight_id, coalesce(b.code, 'Flight ' || f.flight_no) AS flight_label, i.kind, i.message, i.current_seq, i.hole,
	i.behind_minutes, i.created_at, i.created_by_name, i.acknowledged_at
	FROM golf.pace_interventions i JOIN golf.flights f ON f.id = i.flight_id LEFT JOIN golf.bookings b ON b.id = f.booking_id`

func listInterventions(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]PaceIntervention, error) {
	return handle.List[PaceIntervention](q.Query(ctx, interventionSelect+` WHERE `+where+` ORDER BY i.created_at DESC`, args...))
}

// Intervene records an intervention on a flight in play and alerts its
// caddy tablet and the other Starter / Marshal screens.
func (m *Module) Intervene(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in InterventionInput) (PaceIntervention, error) {
	def, ok := interventionMessages[in.Kind]
	if !ok {
		return PaceIntervention{}, handle.Invalid("kind", "invalid", "unknown intervention kind")
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		msg = def
	}
	if msg == "" {
		return PaceIntervention{}, handle.Invalid("message", "required", "a note needs a message")
	}
	if len(msg) > 500 {
		return PaceIntervention{}, handle.Invalid("message", "too_long", "at most 500 characters")
	}
	r, err := m.lockRound(ctx, tx, fid)
	if err != nil {
		return PaceIntervention{}, err
	}
	if r.Status != "in_play" {
		return PaceIntervention{}, errs.Conflict("not_in_play", "the flight is not on the course")
	}
	var hole *string
	if holes, err := m.roundHoles(ctx, tx, r); err != nil {
		return PaceIntervention{}, err
	} else if r.CurrentSeq >= 1 && r.CurrentSeq <= len(holes) {
		h := "Hole " + itoa(holes[r.CurrentSeq-1].Number)
		hole = &h
	}
	behind := r.BehindMinutes // the pace job's last value; the live pace when the flight is on it
	pace, err := m.Pace(ctx, tx, r.PropertyID)
	if err != nil {
		return PaceIntervention{}, err
	}
	for _, p := range pace {
		if p.FlightID == fid {
			behind = p.BehindMinutes
		}
	}
	var name *string
	if uid := actorPtr(ctx); uid != nil {
		_ = tx.QueryRow(ctx, `SELECT full_name FROM platform.users WHERE id = $1`, *uid).Scan(&name)
	}
	iid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.pace_interventions (id, property_id, flight_id, kind, message, current_seq, hole, behind_minutes, created_at,
		created_by, created_by_name) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, iid, r.PropertyID, fid, in.Kind, msg, r.CurrentSeq, hole, behind,
		clock.Now(), actorPtr(ctx), name); err != nil {
		return PaceIntervention{}, err
	}
	list, err := listInterventions(ctx, tx, "i.id = $1", iid)
	if err != nil || len(list) == 0 {
		return PaceIntervention{}, err
	}
	out := list[0]
	if err := m.live(ctx, tx, "golf.pace", r.PropertyID, "intervention", fid.String(), map[string]any{"label": out.FlightLabel, "kind": out.Kind}); err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.pace_intervention", iid, out.FlightLabel, audit.ActionCreate, r.PropertyID, nil, out, "")
}

// AcknowledgeIntervention is the caddy confirming the flight got the message.
func (m *Module) AcknowledgeIntervention(ctx context.Context, tx pgx.Tx, iid uuid.UUID) (PaceIntervention, error) {
	list, err := listInterventions(ctx, tx, "i.id = $1", iid)
	if err != nil {
		return PaceIntervention{}, err
	}
	if len(list) == 0 {
		return PaceIntervention{}, errs.NotFound("intervention")
	}
	r, err := m.GetRound(ctx, tx, list[0].FlightID)
	if err != nil {
		return PaceIntervention{}, err
	}
	if err := m.canSeeFlight(ctx, tx, r); err != nil {
		return PaceIntervention{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.pace_interventions SET acknowledged_at = $2, acknowledged_by = $3 WHERE id = $1 AND acknowledged_at IS NULL`,
		iid, clock.Now(), actorPtr(ctx)); err != nil {
		return PaceIntervention{}, err
	}
	before := list[0]
	if list, err = listInterventions(ctx, tx, "i.id = $1", iid); err != nil {
		return PaceIntervention{}, err
	}
	if err := record(ctx, tx, "golf.pace_intervention", iid, list[0].FlightLabel, audit.ActionStatusChange, r.PropertyID, before, list[0], ""); err != nil {
		return list[0], err
	}
	return list[0], m.live(ctx, tx, "golf.pace", r.PropertyID, "acknowledged", r.FlightID.String(), map[string]any{"label": list[0].FlightLabel})
}

// MonitorFlight is a flight on the course monitor.
type MonitorFlight struct {
	PaceFlight
	HoleNumber    *int               `json:"holeNumber"`
	MapX          *float64           `json:"mapX"`
	MapY          *float64           `json:"mapY"`
	Live          bool               `json:"live" doc:"Placed by the caddy tablet's GPS (else on the hole being played)"`
	Interventions []PaceIntervention `json:"interventions" doc:"Today's interventions on this flight, latest first"`
}

// MonitorCart is a golf cart position on the course monitor.
type MonitorCart struct {
	GolfCartID uuid.UUID `json:"golfCartId"`
	Code       string    `json:"code"`
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	MapX       *float64  `json:"mapX"`
	MapY       *float64  `json:"mapY"`
	Battery    *int      `json:"batteryPercent"`
	At         time.Time `json:"at"`
	Source     string    `json:"source" enum:"tablet,estimated"`
}

// MonitorHole is a hole marker of the course monitor.
type MonitorHole struct {
	HoleID      uuid.UUID `json:"holeId"`
	Number      int       `json:"number"`
	Par         int       `json:"par"`
	SectionCode string    `json:"sectionCode"`
	MapX        *float64  `json:"mapX"`
	MapY        *float64  `json:"mapY"`
	Flights     int       `json:"flights" doc:"Flights playing this hole now"`
}

// CourseMonitor is the Marshal's live view of a course (FR-PLX-04/05).
type CourseMonitor struct {
	CourseID      uuid.UUID          `json:"courseId"`
	CourseName    string             `json:"courseName"`
	MapURL        *string            `json:"mapUrl"`
	Holes         []MonitorHole      `json:"holes"`
	Flights       []MonitorFlight    `json:"flights" doc:"In play, most behind first"`
	Carts         []MonitorCart      `json:"golfCarts"`
	InPlay        int                `json:"inPlay"`
	Slow          int                `json:"slow"`
	Interventions []PaceIntervention `json:"interventions" doc:"Today's log, latest first"`
	GeneratedAt   time.Time          `json:"generatedAt"`
}

// Monitor builds the course monitor of one course.
func (m *Module) Monitor(ctx context.Context, q dbtx.Querier, property, course uuid.UUID) (CourseMonitor, error) {
	g, err := m.Guide(ctx, q, course, nil)
	if err != nil {
		return CourseMonitor{}, err
	}
	cm, err := loadCourseMap(ctx, q, course)
	if err != nil {
		return CourseMonitor{}, err
	}
	out := CourseMonitor{CourseID: course, CourseName: g.Name, MapURL: g.MapURL, Holes: []MonitorHole{}, Flights: []MonitorFlight{}, Carts: []MonitorCart{},
		GeneratedAt: clock.Now()}
	holeIdx := map[uuid.UUID]int{}
	for _, h := range g.Holes {
		holeIdx[h.HoleID] = len(out.Holes)
		out.Holes = append(out.Holes, MonitorHole{HoleID: h.HoleID, Number: h.Number, Par: h.Par, SectionCode: h.SectionCode, MapX: h.MapX, MapY: h.MapY})
	}
	pace, err := m.Pace(ctx, q, property)
	if err != nil {
		return out, err
	}
	loc := location(ctx, q, property)
	from := localDay(clock.Now(), loc)
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	if out.Interventions, err = listInterventions(ctx, q, "i.property_id = $1 AND i.created_at >= $2", property, start); err != nil {
		return out, err
	}
	for _, p := range pace {
		if p.CourseID != course {
			continue
		}
		f := MonitorFlight{PaceFlight: p, Interventions: []PaceIntervention{}}
		if p.HoleID != nil {
			if i, ok := holeIdx[*p.HoleID]; ok {
				n := out.Holes[i].Number
				f.HoleNumber, f.MapX, f.MapY = &n, out.Holes[i].MapX, out.Holes[i].MapY
				out.Holes[i].Flights++
			}
		}
		for _, iv := range out.Interventions {
			if iv.FlightID == p.FlightID {
				f.Interventions = append(f.Interventions, iv)
			}
		}
		if p.Slow {
			out.Slow++
		}
		out.Flights = append(out.Flights, f)
	}
	out.InPlay = len(out.Flights)
	for i := 1; i < len(out.Flights); i++ {
		for j := i; j > 0 && out.Flights[j].BehindMinutes > out.Flights[j-1].BehindMinutes; j-- {
			out.Flights[j], out.Flights[j-1] = out.Flights[j-1], out.Flights[j]
		}
	}
	if m.GPS != nil {
		ps, err := m.GPS.Positions(ctx, q, property)
		if err != nil {
			return out, err
		}
		codes := map[uuid.UUID]string{}
		ids := make([]uuid.UUID, 0, len(ps))
		for _, p := range ps {
			ids = append(ids, p.GolfCartID)
		}
		rows, err := q.Query(ctx, `SELECT id, code FROM golf.golf_carts WHERE id = ANY($1)`, ids)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var cid uuid.UUID
			var code string
			if err := rows.Scan(&cid, &code); err != nil {
				rows.Close()
				return out, err
			}
			codes[cid] = code
		}
		rows.Close()
		for _, p := range ps {
			c := MonitorCart{GolfCartID: p.GolfCartID, Code: codes[p.GolfCartID], Lat: p.Lat, Lng: p.Lng, Battery: p.Battery, At: p.At, Source: p.Source}
			c.MapX, c.MapY = cm.project(p.Lat, p.Lng)
			if c.MapX == nil {
				continue
			}
			out.Carts = append(out.Carts, c)
			// the flight is where its caddy's tablet is
			if p.Source == "tablet" && p.FlightID != nil {
				for i := range out.Flights {
					if out.Flights[i].FlightID == *p.FlightID && !out.Flights[i].Live {
						out.Flights[i].MapX, out.Flights[i].MapY, out.Flights[i].Live = c.MapX, c.MapY, true
					}
				}
			}
		}
	}
	return out, nil
}

// openInterventions are the messages a caddy tablet still has to confirm.
func openInterventions(ctx context.Context, q dbtx.Querier, fid uuid.UUID) ([]PaceIntervention, error) {
	return listInterventions(ctx, q, "i.flight_id = $1 AND i.acknowledged_at IS NULL", fid)
}

func (m *Module) registerGuide(reg *route.Registry, add func(string, route.Route)) {
	db := m.DB
	crm.MeRoute(reg, "golf", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/course-guide",
		Summary: "Course Guide: Hole by Hole and my course handicap per tee", Response: CourseGuide{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CourseGuide], error) {
			me, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[CourseGuide]{}, err
			}
			ids, err := collectIDs(tx.Query(ctx, `SELECT c.id FROM golf.courses c WHERE c.property_id = $1 AND c.status = 'active' AND c.archived_at IS NULL
				AND EXISTS (SELECT 1 FROM golf.holes h WHERE h.course_id = c.id AND h.status = 'active') ORDER BY c.name`, me.PropertyID))
			if err != nil {
				return httpx.Page[CourseGuide]{}, err
			}
			index := currentHandicap(ctx, tx, me.ID)
			out := []CourseGuide{}
			for _, cid := range ids {
				g, err := m.Guide(ctx, tx, cid, index)
				if err != nil {
					return httpx.Page[CourseGuide]{}, err
				}
				out = append(out, g)
			}
			return httpx.Page[CourseGuide]{Items: out}, nil
		})})
	add("Pace of Play", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/course-monitor", Summary: "Course Monitor (Starter / Marshal): course map, flights in play, golf carts",
		Permission: "golf.pace.view", Response: CourseMonitor{}, Query: []route.Param{{Name: "courseId", Description: "Default: the first course with holes"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CourseMonitor, error) {
			p := handle.Property(ctx)
			cid, err := defaultCourse(ctx, tx, p, r.URL.Query().Get("courseId"))
			if err != nil {
				return CourseMonitor{}, err
			}
			return m.Monitor(ctx, tx, p, cid)
		})})
	add("Pace of Play", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/flights/{id}/pace-interventions",
		Summary:    "Marshal: remind, warn, ask to skip a hole or let the flight behind play through (shown on the caddy tablet)",
		Permission: "golf.pace.manage", Request: InterventionInput{}, Response: PaceIntervention{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in InterventionInput) (PaceIntervention, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return PaceIntervention{}, err
			}
			return m.Intervene(ctx, tx, fid, in)
		})})
	add("Pace of Play", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/pace-interventions/{id}:acknowledge",
		Summary: "Caddy tablet: the flight got the Marshal's message", Permission: "golf.round.operate", Response: PaceIntervention{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PaceIntervention, error) {
			iid, err := handle.ID(r)
			if err != nil {
				return PaceIntervention{}, err
			}
			return m.AcknowledgeIntervention(ctx, tx, iid)
		})})
}
