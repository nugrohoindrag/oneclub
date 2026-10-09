package experience

// Course maintenance (demo feedback 9 Oct 2026, replaces Smartscore Golf
// O&M): the course is maintained every day. The plan of a day lists the
// work per hole or area — mowing greens and fairways, bunkers, irrigation,
// fertiliser, pin positions … — and who of the groundstaff does it; the
// work goes planned → in progress → done with a photo and notes. A task
// that closes its hole closes it in Course Status while it is worked and
// opens it again when done. The holes being worked show on the Course
// Monitor and the tee sheet, and every hole keeps its maintenance history.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// Maintenance areas and kinds of work.
var (
	MaintenanceAreas = []string{"green", "fairway", "tee", "bunker", "rough", "irrigation", "driving_range", "whole_course", "other"}
	MaintenanceTypes = []string{"mowing_green", "mowing_fairway", "mowing_rough", "bunker", "irrigation", "fertilizer", "pin_position", "aeration",
		"top_dressing", "repair", "other"}
)

// MaintenanceTasks is the master of the work (Back Office edits, photo
// upload); the status moves through the actions below.
var MaintenanceTasks = &resource.Def{
	Key: "golf.maintenance_task", Module: "golf", Perm: "golf.maintenance_task", Path: "/api/v1/golf/maintenance-tasks", Table: "golf.maintenance_tasks",
	Name: "Maintenance Task", Plural: "Course Maintenance", Tag: "Course Maintenance", PropertyScoped: true, OrderBy: "work_date DESC, planned_start NULLS LAST, id",
	SchemaName: "MaintenanceTaskRecord",
	Fields: []resource.Field{
		{Name: "courseId", Column: "course_id", Label: "Course", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "golf.courses", SameProperty: true, Label: "course"}},
		{Name: "holeId", Column: "hole_id", Label: "Hole", Kind: resource.UUID, Filter: true, Ref: &resource.Ref{Table: "golf.holes", SameProperty: true, Label: "hole"}},
		{Name: "area", Column: "area", Label: "Area", Kind: resource.Enum, Enum: MaintenanceAreas, Required: true, Filter: true},
		{Name: "taskType", Column: "task_type", Label: "Work", Kind: resource.Enum, Enum: MaintenanceTypes, Required: true, Filter: true},
		{Name: "workDate", Column: "work_date", Label: "Date", Kind: resource.Date, Required: true, Filter: true},
		{Name: "plannedStart", Column: "planned_start", Label: "Start", Kind: resource.Time},
		{Name: "assigneeName", Column: "assignee_name", Label: "Groundstaff", Kind: resource.String, Max: 120, Search: true},
		{Name: "closesHole", Column: "closes_hole", Label: "Close the hole while worked", Kind: resource.Bool, Default: false},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"planned", "in_progress", "done", "cancelled"}, ReadOnly: true, Filter: true},
		{Name: "startedAt", Column: "started_at", Label: "Started", Kind: resource.Timestamp, ReadOnly: true},
		{Name: "completedAt", Column: "completed_at", Label: "Done", Kind: resource.Timestamp, ReadOnly: true},
		{Name: "doneNotes", Column: "done_notes", Label: "Work notes", Kind: resource.Text, Max: 1000},
		{Name: "photoUrl", Column: "photo_url", Label: "Photo", Kind: resource.Image, Max: 500},
	},
	Hooks: resource.Hooks{BeforeWrite: func(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
		course, hole := v["courseId"], v["holeId"]
		if before != nil {
			if course == nil {
				course = before["courseId"]
			}
			if _, set := v["holeId"]; !set {
				hole = before["holeId"]
			}
		}
		if hole == nil || fmt.Sprint(hole) == "" {
			return nil
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.holes WHERE id::text = $1 AND course_id::text = $2)`,
			fmt.Sprint(hole), fmt.Sprint(course)).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid("holeId", "invalid", "a hole of the course")
		}
		return nil
	}},
}

// MaintenanceTask is a task with its course and hole.
type MaintenanceTask struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	CourseID     uuid.UUID  `json:"courseId" db:"course_id"`
	CourseName   string     `json:"courseName" db:"course_name"`
	HoleID       *uuid.UUID `json:"holeId" db:"hole_id"`
	HoleNumber   *int       `json:"holeNumber" db:"hole_number"`
	HoleLabel    *string    `json:"holeLabel" db:"hole_label"`
	Area         string     `json:"area" db:"area"`
	TaskType     string     `json:"taskType" db:"task_type"`
	WorkDate     string     `json:"workDate" db:"work_date"`
	PlannedStart *string    `json:"plannedStart" db:"planned_start"`
	AssigneeName *string    `json:"assigneeName" db:"assignee_name"`
	ClosesHole   bool       `json:"closesHole" db:"closes_hole"`
	Notes        *string    `json:"notes" db:"notes"`
	Status       string     `json:"status" db:"status" enum:"planned,in_progress,done,cancelled"`
	StartedAt    *time.Time `json:"startedAt" db:"started_at"`
	CompletedAt  *time.Time `json:"completedAt" db:"completed_at"`
	DoneNotes    *string    `json:"doneNotes" db:"done_notes"`
	PhotoURL     *string    `json:"photoUrl" db:"photo_url"`
}

const taskSelect = `SELECT t.id, t.course_id, c.name AS course_name, t.hole_id, h.number AS hole_number,
	CASE WHEN h.id IS NULL THEN NULL ELSE s.code || '-' || h.number END AS hole_label, t.area, t.task_type, to_char(t.work_date, 'YYYY-MM-DD') AS work_date,
	to_char(t.planned_start, 'HH24:MI') AS planned_start, t.assignee_name, t.closes_hole, t.notes, t.status, t.started_at, t.completed_at, t.done_notes, t.photo_url
	FROM golf.maintenance_tasks t JOIN golf.courses c ON c.id = t.course_id LEFT JOIN golf.holes h ON h.id = t.hole_id
	LEFT JOIN golf.course_sections s ON s.id = h.section_id`

func (m *Module) task(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (MaintenanceTask, error) {
	rows, err := q.Query(ctx, taskSelect+` WHERE t.id = $1 AND t.property_id = $2`, tid, property)
	return handle.One[MaintenanceTask](rows, err, "maintenance task")
}

// MaintenanceBoard is the plan of a day.
type MaintenanceBoard struct {
	Date       string            `json:"date"`
	Tasks      []MaintenanceTask `json:"tasks"`
	Planned    int               `json:"planned"`
	InProgress int               `json:"inProgress"`
	Done       int               `json:"done"`
}

// Board lists the work of a day (optionally one course).
func (m *Module) Board(ctx context.Context, q dbtx.Querier, property uuid.UUID, course *uuid.UUID, day string) (MaintenanceBoard, error) {
	out := MaintenanceBoard{Date: day}
	var err error
	out.Tasks, err = handle.List[MaintenanceTask](q.Query(ctx, taskSelect+` WHERE t.property_id = $1 AND t.work_date = $2::date AND ($3::uuid IS NULL OR t.course_id = $3)
		ORDER BY t.planned_start NULLS LAST, h.number NULLS FIRST, t.task_type`, property, day, course))
	for _, t := range out.Tasks {
		switch t.Status {
		case "planned":
			out.Planned++
		case "in_progress":
			out.InProgress++
		case "done":
			out.Done++
		}
	}
	return out, err
}

// MaintenancePlanInput plans one kind of work on several holes at once.
type MaintenancePlanInput struct {
	CourseID     uuid.UUID   `json:"courseId"`
	WorkDate     string      `json:"workDate"`
	HoleIDs      []uuid.UUID `json:"holeIds,omitempty" doc:"Empty: the area as a whole (no hole)"`
	Area         string      `json:"area"`
	TaskType     string      `json:"taskType"`
	PlannedStart string      `json:"plannedStart,omitempty" doc:"HH:MM"`
	AssigneeName string      `json:"assigneeName,omitempty"`
	ClosesHole   bool        `json:"closesHole,omitempty"`
	Notes        string      `json:"notes,omitempty"`
}

// Plan creates the tasks of a plan.
func (m *Module) Plan(ctx context.Context, tx pgx.Tx, property uuid.UUID, in MaintenancePlanInput) ([]MaintenanceTask, error) {
	if !slices.Contains(MaintenanceAreas, in.Area) {
		return nil, handle.Invalid("area", "invalid", strings.Join(MaintenanceAreas, ", "))
	}
	if !slices.Contains(MaintenanceTypes, in.TaskType) {
		return nil, handle.Invalid("taskType", "invalid", strings.Join(MaintenanceTypes, ", "))
	}
	if _, err := time.Parse("2006-01-02", in.WorkDate); err != nil {
		return nil, handle.Invalid("workDate", "invalid", "YYYY-MM-DD")
	}
	var start *string
	if s := strings.TrimSpace(in.PlannedStart); s != "" {
		if _, err := time.Parse("15:04", s); err != nil {
			return nil, handle.Invalid("plannedStart", "invalid", "HH:MM")
		}
		start = &s
	}
	var owner uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.courses WHERE id = $1`, in.CourseID).Scan(&owner); err != nil || owner != property {
		return nil, errs.NotFound("course")
	}
	holes := []*uuid.UUID{nil}
	if len(in.HoleIDs) > 0 {
		holes = nil
		for i := range in.HoleIDs {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.holes WHERE id = $1 AND course_id = $2)`, in.HoleIDs[i], in.CourseID).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, handle.Invalid("holeIds", "invalid", "holes of the course")
			}
			holes = append(holes, &in.HoleIDs[i])
		}
	}
	ids := make([]uuid.UUID, 0, len(holes))
	for _, h := range holes {
		tid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO golf.maintenance_tasks (id, property_id, course_id, hole_id, area, task_type, work_date, planned_start, assignee_name,
			closes_hole, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7::date,$8::time,$9,$10,$11,$12,$12)`, tid, property, in.CourseID, h, in.Area,
			in.TaskType, in.WorkDate, start, nullStr(strings.TrimSpace(in.AssigneeName)), in.ClosesHole && h != nil, nullStr(strings.TrimSpace(in.Notes)), actorPtr(ctx)); err != nil {
			return nil, err
		}
		ids = append(ids, tid)
	}
	if err := record(ctx, tx, "golf.maintenance_task", ids[0], in.TaskType+" "+in.WorkDate, audit.ActionCreate, property, nil, map[string]any{"tasks": len(ids), "plan": in}, ""); err != nil {
		return nil, err
	}
	return handle.List[MaintenanceTask](tx.Query(ctx, taskSelect+` WHERE t.id = ANY($1) ORDER BY h.number NULLS FIRST`, ids))
}

// MaintenanceCopyInput copies the plan of a day to another day.
type MaintenanceCopyInput struct {
	CourseID uuid.UUID `json:"courseId"`
	From     string    `json:"from" doc:"YYYY-MM-DD"`
	To       string    `json:"to" doc:"YYYY-MM-DD"`
}

// CopyDay plans a day like another one (the daily routine).
func (m *Module) CopyDay(ctx context.Context, tx pgx.Tx, property uuid.UUID, in MaintenanceCopyInput) ([]MaintenanceTask, error) {
	for f, v := range map[string]string{"from": in.From, "to": in.To} {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return nil, handle.Invalid(f, "invalid", "YYYY-MM-DD")
		}
	}
	if in.From == in.To {
		return nil, handle.Invalid("to", "invalid", "another day")
	}
	ids, err := collectIDs(tx.Query(ctx, `INSERT INTO golf.maintenance_tasks (id, property_id, course_id, hole_id, area, task_type, work_date, planned_start,
		assignee_name, closes_hole, notes, created_by, updated_by)
		SELECT gen_random_uuid(), property_id, course_id, hole_id, area, task_type, $4::date, planned_start, assignee_name, closes_hole, notes, $5, $5
		FROM golf.maintenance_tasks WHERE property_id = $1 AND course_id = $2 AND work_date = $3::date AND status <> 'cancelled' RETURNING id`,
		property, in.CourseID, in.From, in.To, actorPtr(ctx)))
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errs.Conflict("nothing_to_copy", "no maintenance is planned on "+in.From)
	}
	if err := record(ctx, tx, "golf.maintenance_task", ids[0], "copy "+in.From+" → "+in.To, audit.ActionCreate, property, nil, map[string]any{"tasks": len(ids), "copy": in}, ""); err != nil {
		return nil, err
	}
	return handle.List[MaintenanceTask](tx.Query(ctx, taskSelect+` WHERE t.id = ANY($1) ORDER BY t.planned_start NULLS LAST, h.number NULLS FIRST`, ids))
}

// MaintenanceDoneInput finishes a task.
type MaintenanceDoneInput struct {
	DoneNotes string `json:"doneNotes,omitempty"`
	PhotoURL  string `json:"photoUrl,omitempty" doc:"An uploaded photo (/api/v1/platform/images?resource=golf.maintenance_task)"`
}

// MaintenanceCancelInput cancels a task.
type MaintenanceCancelInput struct {
	Reason string `json:"reason,omitempty"`
}

// setHoleClosed adds or removes a hole number in the course's closed holes.
func (m *Module) setHoleClosed(ctx context.Context, tx pgx.Tx, property uuid.UUID, t MaintenanceTask, closed bool) error {
	if !t.ClosesHole || t.HoleNumber == nil || m.Golf == nil {
		return nil
	}
	if !closed {
		// another task still working the hole keeps it closed
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.maintenance_tasks WHERE hole_id = $1 AND id <> $2 AND status = 'in_progress' AND closes_hole)`,
			t.HoleID, t.ID).Scan(&busy); err != nil || busy {
			return err
		}
	}
	var current string
	if err := tx.QueryRow(ctx, `SELECT coalesce(closed_holes, '') FROM golf.course_status WHERE course_id = $1`, t.CourseID).Scan(&current); err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	no := strconv.Itoa(*t.HoleNumber)
	var holes []string
	for _, h := range strings.Split(current, ",") {
		if h = strings.TrimSpace(h); h != "" && h != no {
			holes = append(holes, h)
		}
	}
	if closed {
		holes = append(holes, no)
	}
	sort.Slice(holes, func(i, j int) bool { a, _ := strconv.Atoi(holes[i]); b, _ := strconv.Atoi(holes[j]); return a < b })
	list := strings.Join(holes, ",")
	if list == current {
		return nil
	}
	_, err := m.Golf.UpdateCourseStatus(ctx, tx, property, t.CourseID, golf.CourseStatusRequest{ClosedHoles: &list})
	return err
}

// MoveTask starts, finishes or cancels a task.
func (m *Module) MoveTask(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, action string, done MaintenanceDoneInput, reason string) (MaintenanceTask, error) {
	before, err := m.task(ctx, tx, property, tid)
	if err != nil {
		return before, err
	}
	now := clock.Now()
	switch action {
	case "start":
		if before.Status != "planned" {
			return before, errs.Conflict("invalid_status", "the task is "+before.Status)
		}
		_, err = tx.Exec(ctx, `UPDATE golf.maintenance_tasks SET status = 'in_progress', started_at = $2, updated_by = $3 WHERE id = $1`, tid, now, actorPtr(ctx))
	case "complete":
		if before.Status != "planned" && before.Status != "in_progress" {
			return before, errs.Conflict("invalid_status", "the task is "+before.Status)
		}
		photo := strings.TrimSpace(done.PhotoURL)
		if photo != "" && !strings.HasPrefix(photo, "/api/v1/files/") && !strings.HasPrefix(photo, "https://") {
			return before, handle.Invalid("photoUrl", "invalid", "an uploaded photo")
		}
		_, err = tx.Exec(ctx, `UPDATE golf.maintenance_tasks SET status = 'done', started_at = coalesce(started_at, $2), completed_at = $2,
			done_notes = coalesce($3, done_notes), photo_url = coalesce($4, photo_url), updated_by = $5 WHERE id = $1`,
			tid, now, nullStr(strings.TrimSpace(done.DoneNotes)), nullStr(photo), actorPtr(ctx))
	case "cancel":
		if before.Status != "planned" && before.Status != "in_progress" {
			return before, errs.Conflict("invalid_status", "the task is "+before.Status)
		}
		_, err = tx.Exec(ctx, `UPDATE golf.maintenance_tasks SET status = 'cancelled', done_notes = coalesce($2, done_notes), updated_by = $3 WHERE id = $1`,
			tid, nullStr(strings.TrimSpace(reason)), actorPtr(ctx))
	}
	if err != nil {
		return before, err
	}
	after, err := m.task(ctx, tx, property, tid)
	if err != nil {
		return after, err
	}
	if err := m.setHoleClosed(ctx, tx, property, after, action == "start"); err != nil {
		return after, err
	}
	if err := record(ctx, tx, "golf.maintenance_task", tid, after.TaskType+" "+after.WorkDate, audit.ActionStatusChange, property, before, after, reason); err != nil {
		return after, err
	}
	return after, m.live(ctx, tx, "golf.pace", property, "maintenance", tid.String(), map[string]any{"status": after.Status})
}

// workingHoles are the tasks in progress on the holes of a course.
func workingHoles(ctx context.Context, q dbtx.Querier, course uuid.UUID) (map[uuid.UUID][]string, error) {
	type row struct {
		HoleID   uuid.UUID `db:"hole_id"`
		TaskType string    `db:"task_type"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT hole_id, task_type FROM golf.maintenance_tasks WHERE course_id = $1 AND status = 'in_progress' AND hole_id IS NOT NULL
		ORDER BY started_at`, course))
	out := map[uuid.UUID][]string{}
	for _, r := range rows {
		out[r.HoleID] = append(out[r.HoleID], r.TaskType)
	}
	return out, err
}

// MaintenanceHole is a hole with the last day of each kind of work done.
type MaintenanceHole struct {
	HoleID   uuid.UUID         `json:"holeId" db:"id"`
	Number   int               `json:"number" db:"number"`
	Label    string            `json:"label" db:"label"`
	Par      int               `json:"par" db:"par"`
	LastDone map[string]string `json:"lastDone" db:"-" doc:"Work kind → last date done"`
	Working  []string          `json:"working" db:"-" doc:"Work in progress now"`
}

// MaintenanceCourse is a course with its holes for the maintenance plan.
type MaintenanceCourse struct {
	CourseID uuid.UUID         `json:"courseId" db:"id"`
	Name     string            `json:"name" db:"name"`
	Holes    []MaintenanceHole `json:"holes" db:"-"`
}

// MaintenanceCourses lists the active courses with their holes, the last
// day of every kind of work per hole, and the work in progress.
func (m *Module) MaintenanceCourses(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]MaintenanceCourse, error) {
	courses, err := handle.List[MaintenanceCourse](q.Query(ctx, `SELECT id, name FROM golf.courses WHERE property_id = $1 AND status = 'active'
		AND archived_at IS NULL ORDER BY name`, property))
	if err != nil {
		return nil, err
	}
	for i := range courses {
		c := &courses[i]
		if c.Holes, err = handle.List[MaintenanceHole](q.Query(ctx, `SELECT h.id, h.number, s.code || '-' || h.number AS label, h.par
			FROM golf.holes h JOIN golf.course_sections s ON s.id = h.section_id WHERE h.course_id = $1 AND h.status = 'active'
			ORDER BY s.sequence, h.number`, c.CourseID)); err != nil {
			return nil, err
		}
		type last struct {
			HoleID   uuid.UUID `db:"hole_id"`
			TaskType string    `db:"task_type"`
			Day      string    `db:"day"`
		}
		done, err := handle.List[last](q.Query(ctx, `SELECT hole_id, task_type, to_char(max(work_date), 'YYYY-MM-DD') AS day FROM golf.maintenance_tasks
			WHERE course_id = $1 AND status = 'done' AND hole_id IS NOT NULL GROUP BY hole_id, task_type`, c.CourseID))
		if err != nil {
			return nil, err
		}
		working, err := workingHoles(ctx, q, c.CourseID)
		if err != nil {
			return nil, err
		}
		for j := range c.Holes {
			h := &c.Holes[j]
			h.LastDone, h.Working = map[string]string{}, append([]string{}, working[h.HoleID]...)
			for _, d := range done {
				if d.HoleID == h.HoleID {
					h.LastDone[d.TaskType] = d.Day
				}
			}
		}
	}
	return courses, nil
}

func (m *Module) registerMaintenance(add func(tag string, rt route.Route)) {
	db := m.DB
	const tag = "Course Maintenance"
	add(tag, route.Route{Method: http.MethodGet, Path: "/api/v1/golf/maintenance-courses", Summary: "Courses and holes with the last maintenance done and the work in progress",
		Permission: "golf.maintenance_task.view", Response: MaintenanceCourse{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MaintenanceCourse], error) {
			return handle.Page(m.MaintenanceCourses(ctx, tx, handle.Property(ctx)))
		})})
	add(tag, route.Route{Method: http.MethodGet, Path: "/api/v1/golf/maintenance-board", Summary: "Course maintenance of a day (plan, in progress, done)",
		Permission: "golf.maintenance_task.view", Response: MaintenanceBoard{}, Query: []route.Param{{Name: "date"}, {Name: "courseId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MaintenanceBoard, error) {
			p := handle.Property(ctx)
			day := r.URL.Query().Get("date")
			if day == "" {
				day = localDay(clock.Now(), location(ctx, tx, p)).Format("2006-01-02")
			}
			course, err := handle.QueryUUID(r, "courseId")
			if err != nil {
				return MaintenanceBoard{}, err
			}
			return m.Board(ctx, tx, p, course, day)
		})})
	add(tag, route.Route{Method: http.MethodGet, Path: "/api/v1/golf/maintenance-history", Summary: "Maintenance history of the holes (work done)",
		Permission: "golf.maintenance_task.view", Response: MaintenanceTask{}, List: true,
		Query: []route.Param{{Name: "courseId"}, {Name: "holeId"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MaintenanceTask], error) {
			q := r.URL.Query()
			course, err := handle.QueryUUID(r, "courseId")
			if err != nil {
				return httpx.Page[MaintenanceTask]{}, err
			}
			hole, err := handle.QueryUUID(r, "holeId")
			if err != nil {
				return httpx.Page[MaintenanceTask]{}, err
			}
			return handle.Page(handle.List[MaintenanceTask](tx.Query(ctx, taskSelect+` WHERE t.property_id = $1 AND t.status = 'done'
				AND ($2::uuid IS NULL OR t.course_id = $2) AND ($3::uuid IS NULL OR t.hole_id = $3)
				AND ($4 = '' OR t.work_date >= $4::date) AND ($5 = '' OR t.work_date <= $5::date)
				ORDER BY t.work_date DESC, t.completed_at DESC LIMIT 500`, handle.Property(ctx), course, hole, q.Get("from"), q.Get("to"))))
		})})
	add(tag, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/maintenance-tasks:plan", Summary: "Plan one kind of work on several holes",
		Permission: "golf.maintenance_task.create", Request: MaintenancePlanInput{}, Response: MaintenanceTask{}, List: true, Status: http.StatusCreated,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MaintenancePlanInput) (httpx.Page[MaintenanceTask], error) {
			return handle.Page(m.Plan(ctx, tx, handle.Property(ctx), in))
		})})
	add(tag, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/maintenance-tasks:copy-day", Summary: "Plan a day like another one (the daily routine)",
		Permission: "golf.maintenance_task.create", Request: MaintenanceCopyInput{}, Response: MaintenanceTask{}, List: true, Status: http.StatusCreated,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MaintenanceCopyInput) (httpx.Page[MaintenanceTask], error) {
			return handle.Page(m.CopyDay(ctx, tx, handle.Property(ctx), in))
		})})
	add(tag, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/maintenance-tasks/{id}:start", Summary: "Groundstaff: start the work (closes the hole when set)",
		Permission: "golf.maintenance_task.work", Response: MaintenanceTask{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (MaintenanceTask, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return MaintenanceTask{}, err
			}
			return m.MoveTask(ctx, tx, handle.Property(ctx), tid, "start", MaintenanceDoneInput{}, "")
		})})
	add(tag, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/maintenance-tasks/{id}:complete", Summary: "Groundstaff: work done, with notes and a photo",
		Permission: "golf.maintenance_task.work", Request: MaintenanceDoneInput{}, Response: MaintenanceTask{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MaintenanceDoneInput) (MaintenanceTask, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return MaintenanceTask{}, err
			}
			return m.MoveTask(ctx, tx, handle.Property(ctx), tid, "complete", in, "")
		})})
	add(tag, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/maintenance-tasks/{id}:cancel", Summary: "Cancel a task (opens its hole again)",
		Permission: "golf.maintenance_task.update", Request: MaintenanceCancelInput{}, Response: MaintenanceTask{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MaintenanceCancelInput) (MaintenanceTask, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return MaintenanceTask{}, err
			}
			return m.MoveTask(ctx, tx, handle.Property(ctx), tid, "cancel", MaintenanceDoneInput{}, in.Reason)
		})})
}
