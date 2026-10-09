package stay

// Housekeeping (requirements §20, §21). A check-out makes the bungalow
// Dirty and gives housekeeping a check-out cleaning; the attendant starts
// the task (Cleaning) and completes it with the checklist (Cleaned); the
// inspector passes it (Inspected, then Ready — Stay Policies) or fails it
// (back to Cleaning). Stayover cleaning of the occupied bungalows, deep
// cleaning, turndown and inspections are planned the same way.

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// HK task types and priorities.
var (
	HKTaskTypes  = []string{"checkout_cleaning", "stayover_cleaning", "deep_cleaning", "turndown", "inspection", "other"}
	HKPriorities = []string{"low", "normal", "high", "urgent"}
)

// ChecklistItem is one line of a cleaning or inspection checklist.
type ChecklistItem struct {
	Area string `json:"area"`
	Item string `json:"item"`
	Done bool   `json:"done"`
	Note string `json:"note,omitempty"`
}

// defaultChecklist is the checklist of a task type (§20: bedroom, bathroom,
// general area, amenities, AC, lighting and final inspection).
func defaultChecklist(taskType string) []ChecklistItem {
	items := [][2]string{
		{"bedroom", "Bed linen changed and bed made"}, {"bedroom", "Floor swept and mopped"}, {"bedroom", "Furniture dusted"},
		{"bathroom", "Toilet, shower and sink cleaned"}, {"bathroom", "Towels replaced"}, {"bathroom", "Toiletries refilled"},
		{"general", "Terrace and living area tidy"}, {"general", "Rubbish removed"},
		{"amenities", "Minibar / water and coffee set refilled"}, {"amenities", "Amenities complete"},
		{"ac", "AC working and filter clean"}, {"lighting", "All lights working"}, {"final", "Final inspection: room ready for the guest"},
	}
	switch taskType {
	case "turndown":
		items = [][2]string{{"bedroom", "Bed turned down"}, {"bedroom", "Curtains closed"}, {"bathroom", "Towels refreshed"}, {"amenities", "Water refilled"}}
	case "stayover_cleaning":
		items = [][2]string{{"bedroom", "Bed made"}, {"bedroom", "Floor swept"}, {"bathroom", "Bathroom cleaned"}, {"bathroom", "Towels replaced on request"},
			{"general", "Rubbish removed"}, {"amenities", "Water and coffee refilled"}}
	case "deep_cleaning":
		items = append(items, [2]string{"general", "Windows, curtains and under the furniture"}, [2]string{"ac", "AC filter washed"},
			[2]string{"bathroom", "Grout and drains descaled"})
	}
	out := make([]ChecklistItem, len(items))
	for i, x := range items {
		out[i] = ChecklistItem{Area: x[0], Item: x[1]}
	}
	return out
}

// HKTask is a housekeeping task.
type HKTask struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	TaskNo         string          `json:"taskNo" db:"task_no"`
	BungalowID     uuid.UUID       `json:"bungalowId" db:"bungalow_id"`
	BungalowCode   string          `json:"bungalowCode" db:"bungalow_code"`
	BungalowName   string          `json:"bungalowName" db:"bungalow_name"`
	HKStatus       string          `json:"hkStatus" db:"hk_status"`
	StayID         *uuid.UUID      `json:"stayId" db:"stay_id"`
	StayNo         *string         `json:"stayNo" db:"stay_no"`
	TaskType       string          `json:"taskType" db:"task_type" enum:"checkout_cleaning,stayover_cleaning,deep_cleaning,turndown,inspection,other"`
	Priority       string          `json:"priority" db:"priority" enum:"low,normal,high,urgent"`
	TaskDate       string          `json:"taskDate" db:"task_date"`
	AssignedTo     *string         `json:"assignedTo" db:"assigned_to"`
	AssigneeUserID *uuid.UUID      `json:"assigneeUserId" db:"assignee_user_id"`
	Status         string          `json:"status" db:"status" enum:"open,in_progress,completed,inspected,cancelled"`
	StartedAt      *time.Time      `json:"startedAt" db:"started_at"`
	CompletedAt    *time.Time      `json:"completedAt" db:"completed_at"`
	InspectedAt    *time.Time      `json:"inspectedAt" db:"inspected_at"`
	Reworks        int             `json:"reworks" db:"reworks"`
	Checklist      []ChecklistItem `json:"checklist" db:"checklist"`
	Notes          *string         `json:"notes" db:"notes"`
	Source         string          `json:"source" db:"source"`
	GuestRequestID *uuid.UUID      `json:"guestRequestId" db:"guest_request_id"`
	CreatedAt      time.Time       `json:"createdAt" db:"created_at"`
	MinutesTaken   *int            `json:"minutesTaken" db:"minutes_taken"`
}

const hkSelect = `SELECT h.id, h.task_no, h.bungalow_id, b.code AS bungalow_code, b.name AS bungalow_name, b.hk_status, h.stay_id, s.stay_no, h.task_type, h.priority,
	to_char(h.task_date, 'YYYY-MM-DD') AS task_date, h.assigned_to, h.assignee_user_id, h.status, h.started_at, h.completed_at, h.inspected_at, h.reworks,
	h.checklist, h.notes, h.source, h.guest_request_id, h.created_at,
	CASE WHEN h.started_at IS NOT NULL AND h.completed_at IS NOT NULL THEN (extract(epoch FROM h.completed_at - h.started_at) / 60)::int END AS minutes_taken
	FROM stay.housekeeping_tasks h JOIN stay.bungalows b ON b.id = h.bungalow_id LEFT JOIN stay.stays s ON s.id = h.stay_id`

func (m *Module) hkTask(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (HKTask, error) {
	rows, err := q.Query(ctx, hkSelect+` WHERE h.id = $1`, tid)
	return handle.One[HKTask](rows, err, "housekeeping task")
}

// hkTaskInput creates a housekeeping task.
type hkTaskInput struct {
	BungalowID     uuid.UUID       `json:"bungalowId"`
	StayID         *uuid.UUID      `json:"stayId,omitempty"`
	TaskType       string          `json:"taskType" enum:"checkout_cleaning,stayover_cleaning,deep_cleaning,turndown,inspection,other"`
	Priority       string          `json:"priority,omitempty" enum:"low,normal,high,urgent"`
	TaskDate       string          `json:"taskDate,omitempty" doc:"YYYY-MM-DD; default today"`
	AssignedTo     string          `json:"assignedTo,omitempty"`
	AssigneeUserID *uuid.UUID      `json:"assigneeUserId,omitempty"`
	Notes          string          `json:"notes,omitempty"`
	Checklist      []ChecklistItem `json:"checklist,omitempty" doc:"Default: the checklist of the task type"`
	Source         string          `json:"-"`
	GuestRequestID *uuid.UUID      `json:"-"`
}

// HKTaskInput is the API body of a new housekeeping task.
type HKTaskInput = hkTaskInput

func (m *Module) createHKTask(ctx context.Context, tx pgx.Tx, property uuid.UUID, in hkTaskInput) (HKTask, error) {
	if !slices.Contains(HKTaskTypes, in.TaskType) {
		return HKTask{}, handle.Invalid("taskType", "invalid", "unknown task type")
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if !slices.Contains(HKPriorities, in.Priority) {
		return HKTask{}, handle.Invalid("priority", "invalid", "priority must be low, normal, high or urgent")
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	loc := calendar.Location(ctx, tx)
	day := clock.Now().In(loc).Format(time.DateOnly)
	if in.TaskDate != "" {
		if _, err := time.Parse(time.DateOnly, in.TaskDate); err != nil {
			return HKTask{}, handle.Invalid("taskDate", "invalid_date", "taskDate must be YYYY-MM-DD")
		}
		day = in.TaskDate
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stay.bungalows WHERE id = $1 AND property_id = $2)`, in.BungalowID, property).Scan(&ok); err != nil {
		return HKTask{}, err
	}
	if !ok {
		return HKTask{}, errs.NotFound("bungalow")
	}
	cl := in.Checklist
	if len(cl) == 0 {
		cl = defaultChecklist(in.TaskType)
	}
	raw, _ := json.Marshal(cl)
	no, err := numbering.Next(ctx, tx, property, "HK", clock.Now().In(loc))
	if err != nil {
		return HKTask{}, err
	}
	tid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.housekeeping_tasks (id, property_id, task_no, bungalow_id, stay_id, task_type, priority, task_date, assigned_to,
		assignee_user_id, checklist, notes, source, guest_request_id, status, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::date,$9,$10,$11,$12,$13,$14,$15,$16)`,
		tid, property, no, in.BungalowID, in.StayID, in.TaskType, in.Priority, day, nzs(in.AssignedTo), in.AssigneeUserID, raw, nzs(in.Notes), in.Source,
		in.GuestRequestID, "open", actor(ctx)); err != nil {
		return HKTask{}, err
	}
	t, err := m.hkTask(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.housekeeping_task", EntityID: tid.String(),
		EntityLabel: no + " · " + t.BungalowCode, PropertyID: &property, After: t}); err != nil {
		return t, err
	}
	return t, m.notifyStaff(ctx, tx, property, "stay.housekeeping.work", "stay.ops_housekeeping", map[string]any{"title": "Housekeeping " + t.BungalowCode,
		"body": label(t.TaskType) + " · " + t.Priority, "taskNo": no}, "/ops/housekeeping")
}

func (m *Module) lockHK(ctx context.Context, tx pgx.Tx, tid uuid.UUID) (HKTask, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM stay.housekeeping_tasks WHERE id = $1 FOR UPDATE`, tid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return HKTask{}, errs.NotFound("housekeeping task")
		}
		return HKTask{}, err
	}
	return m.hkTask(ctx, tx, tid)
}

func (m *Module) hkUpdate(ctx context.Context, tx pgx.Tx, before HKTask, action, set string, args ...any) (HKTask, error) {
	if _, err := tx.Exec(ctx, `UPDATE stay.housekeeping_tasks SET updated_by = $2`+set+` WHERE id = $1`, append([]any{before.ID, actor(ctx)}, args...)...); err != nil {
		return HKTask{}, err
	}
	after, err := m.hkTask(ctx, tx, before.ID)
	if err != nil {
		return after, err
	}
	pid := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: action, EntityType: "stay.housekeeping_task", EntityID: before.ID.String(),
		EntityLabel: before.TaskNo + " · " + before.BungalowCode, PropertyID: &pid, Before: before, After: after})
}

// cleaningType is a task that cleans the bungalow (moves its status).
func cleaningType(t string) bool {
	return t == "checkout_cleaning" || t == "deep_cleaning" || t == "other"
}

// HKAssignInput assigns a task.
type HKAssignInput struct {
	AssignedTo     string     `json:"assignedTo"`
	AssigneeUserID *uuid.UUID `json:"assigneeUserId,omitempty"`
	Priority       string     `json:"priority,omitempty" enum:"low,normal,high,urgent"`
}

func (m *Module) assignHK(ctx context.Context, tx pgx.Tx, tid uuid.UUID, in HKAssignInput) (HKTask, error) {
	t, err := m.lockHK(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "open" && t.Status != "in_progress" {
		return t, errs.Conflict("invalid_status", "the task is "+t.Status)
	}
	if err := handle.Required("assignedTo", in.AssignedTo); err != nil {
		return t, err
	}
	prio := t.Priority
	if in.Priority != "" {
		if !slices.Contains(HKPriorities, in.Priority) {
			return t, handle.Invalid("priority", "invalid", "invalid priority")
		}
		prio = in.Priority
	}
	return m.hkUpdate(ctx, tx, t, "assign", `, assigned_to = $3, assignee_user_id = $4, priority = $5`, in.AssignedTo, in.AssigneeUserID, prio)
}

func (m *Module) startHK(ctx context.Context, tx pgx.Tx, tid uuid.UUID) (HKTask, error) {
	t, err := m.lockHK(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "open" {
		return t, errs.Conflict("invalid_status", "only open tasks can be started (is "+t.Status+")")
	}
	if cleaningType(t.TaskType) {
		if err := m.setHKStatus(ctx, tx, t.BungalowID, "cleaning", t.TaskNo); err != nil {
			return t, err
		}
	}
	return m.hkUpdate(ctx, tx, t, "start", `, status = 'in_progress', started_at = now(), assigned_to = coalesce(assigned_to, $3)`, actorName(ctx, tx))
}

// HKCompleteInput completes a task with its checklist.
type HKCompleteInput struct {
	Checklist []ChecklistItem `json:"checklist,omitempty"`
	Notes     string          `json:"notes,omitempty"`
}

func (m *Module) completeHK(ctx context.Context, tx pgx.Tx, tid uuid.UUID, in HKCompleteInput) (HKTask, error) {
	t, err := m.lockHK(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "open" && t.Status != "in_progress" {
		return t, errs.Conflict("invalid_status", "the task is "+t.Status)
	}
	pol, _, err := m.policy(ctx, tx, handle.Property(ctx))
	if err != nil {
		return t, err
	}
	cl := in.Checklist
	if len(cl) == 0 {
		cl = t.Checklist
	}
	raw, _ := json.Marshal(cl)
	if cleaningType(t.TaskType) {
		next := "cleaned"
		if !pol.InspectionRequired {
			next = "ready"
		}
		if err := m.setHKStatus(ctx, tx, t.BungalowID, next, t.TaskNo); err != nil {
			return t, err
		}
	}
	status := "completed"
	if !cleaningType(t.TaskType) || !pol.InspectionRequired {
		status = "inspected" // nothing left to inspect
	}
	out, err := m.hkUpdate(ctx, tx, t, "complete", `, status = $3, started_at = coalesce(started_at, now()), completed_at = now(), checklist = $4,
		notes = coalesce($5, notes)`, status, raw, nzs(in.Notes))
	if err != nil {
		return out, err
	}
	if t.GuestRequestID != nil {
		if err := m.completeRequestFromTask(ctx, tx, *t.GuestRequestID); err != nil {
			return out, err
		}
	}
	return out, nil
}

// InspectInput is the result of a room inspection (§21).
type InspectInput struct {
	Result    string          `json:"result" enum:"passed,failed"`
	Checklist []ChecklistItem `json:"checklist,omitempty"`
	Notes     string          `json:"notes,omitempty"`
	// A failed inspection with a technical issue opens a work order.
	WorkOrderTitle    string `json:"workOrderTitle,omitempty"`
	WorkOrderCategory string `json:"workOrderCategory,omitempty" enum:"ac,plumbing,electrical,water_heater,furniture,appliance,structure,pest,other"`
}

// RoomInspection is a recorded inspection.
type RoomInspection struct {
	ID            uuid.UUID       `json:"id" db:"id"`
	BungalowID    uuid.UUID       `json:"bungalowId" db:"bungalow_id"`
	BungalowCode  string          `json:"bungalowCode" db:"bungalow_code"`
	TaskID        *uuid.UUID      `json:"taskId" db:"task_id"`
	TaskNo        *string         `json:"taskNo" db:"task_no"`
	InspectorName *string         `json:"inspectorName" db:"inspector_name"`
	Result        string          `json:"result" db:"result" enum:"passed,failed"`
	Checklist     []ChecklistItem `json:"checklist" db:"checklist"`
	Notes         *string         `json:"notes" db:"notes"`
	InspectedAt   time.Time       `json:"inspectedAt" db:"inspected_at"`
}

// inspect records an inspection of a bungalow (of a task or on its own):
// passed → Inspected (Ready per Stay Policies); failed → back to Cleaning.
func (m *Module) inspect(ctx context.Context, tx pgx.Tx, property, unitID uuid.UUID, task *HKTask, in InspectInput) (RoomInspection, error) {
	if in.Result != "passed" && in.Result != "failed" {
		return RoomInspection{}, handle.Invalid("result", "invalid", "result must be passed or failed")
	}
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return RoomInspection{}, err
	}
	cl := in.Checklist
	if len(cl) == 0 && task != nil {
		cl = task.Checklist
	}
	if cl == nil {
		cl = []ChecklistItem{}
	}
	raw, _ := json.Marshal(cl)
	iid := id.New()
	var taskID *uuid.UUID
	if task != nil {
		taskID = &task.ID
	}
	name := actorName(ctx, tx)
	if _, err := tx.Exec(ctx, `INSERT INTO stay.room_inspections (id, property_id, bungalow_id, task_id, inspector_id, inspector_name, result, checklist, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, iid, property, unitID, taskID, actor(ctx), name, in.Result, raw, nzs(in.Notes)); err != nil {
		return RoomInspection{}, err
	}
	if in.Result == "passed" {
		next := "inspected"
		if pol.ReadyAfterInspection {
			next = "ready"
		}
		if err := m.setHKStatus(ctx, tx, unitID, next, "inspection passed"); err != nil {
			return RoomInspection{}, err
		}
		if task != nil {
			if _, err := m.hkUpdate(ctx, tx, *task, "inspect", `, status = 'inspected', inspected_at = now()`); err != nil {
				return RoomInspection{}, err
			}
		}
	} else {
		if err := m.setHKStatus(ctx, tx, unitID, "cleaning", "inspection failed"); err != nil {
			return RoomInspection{}, err
		}
		if task != nil {
			if _, err := m.hkUpdate(ctx, tx, *task, "inspection_failed", `, status = 'in_progress', reworks = reworks + 1, completed_at = NULL,
				notes = coalesce($3, notes)`, nzs(in.Notes)); err != nil {
				return RoomInspection{}, err
			}
		}
		if in.WorkOrderTitle != "" {
			if _, err := m.createWorkOrder(ctx, tx, property, WorkOrderInput{BungalowID: &unitID, Category: nonEmpty(in.WorkOrderCategory, "other"),
				Title: in.WorkOrderTitle, Description: in.Notes, Priority: "high", ReportedBy: name}, "inspection", nil); err != nil {
				return RoomInspection{}, err
			}
		}
	}
	rows, err := tx.Query(ctx, inspectionSelect+` WHERE i.id = $1`, iid)
	out, err := handle.One[RoomInspection](rows, err, "inspection")
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "inspect", EntityType: "stay.room_inspection", EntityID: iid.String(),
		EntityLabel: out.BungalowCode + " · " + in.Result, PropertyID: &property, After: out, Reason: in.Notes})
}

const inspectionSelect = `SELECT i.id, i.bungalow_id, b.code AS bungalow_code, i.task_id, h.task_no, i.inspector_name, i.result, i.checklist, i.notes, i.inspected_at
	FROM stay.room_inspections i JOIN stay.bungalows b ON b.id = i.bungalow_id LEFT JOIN stay.housekeeping_tasks h ON h.id = i.task_id`

func (m *Module) inspectTask(ctx context.Context, tx pgx.Tx, tid uuid.UUID, in InspectInput) (RoomInspection, error) {
	t, err := m.lockHK(ctx, tx, tid)
	if err != nil {
		return RoomInspection{}, err
	}
	if t.Status != "completed" && t.Status != "in_progress" {
		return RoomInspection{}, errs.Conflict("invalid_status", "inspect a completed task (is "+t.Status+")")
	}
	return m.inspect(ctx, tx, handle.Property(ctx), t.BungalowID, &t, in)
}

func (m *Module) cancelHK(ctx context.Context, tx pgx.Tx, tid uuid.UUID, reason string) (HKTask, error) {
	t, err := m.lockHK(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if t.Status == "inspected" || t.Status == "cancelled" {
		return t, errs.Conflict("invalid_status", "the task is "+t.Status)
	}
	return m.hkUpdate(ctx, tx, t, "cancel", `, status = 'cancelled', notes = coalesce($3, notes)`, nzs(reason))
}

// HKBoard is the housekeeping plan of a day.
type HKBoard struct {
	Date      string         `json:"date"`
	Tasks     []HKTask       `json:"tasks"`
	Counts    map[string]int `json:"counts"`
	Rooms     []RoomState    `json:"rooms"`
	RoomCount map[string]int `json:"roomCounts" doc:"Bungalows per housekeeping / operational status"`
}

// HKBoardFor returns the tasks of a day (and the open tasks of earlier
// days) with the status of every bungalow.
func (m *Module) HKBoardFor(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, assignee string) (HKBoard, error) {
	ds := day.Format(time.DateOnly)
	out := HKBoard{Date: ds, Counts: map[string]int{}, RoomCount: map[string]int{}}
	var err error
	if out.Tasks, err = handle.List[HKTask](q.Query(ctx, hkSelect+` WHERE h.property_id = $1 AND (h.task_date = $2::date
		OR (h.task_date < $2::date AND h.status IN ('open', 'in_progress', 'completed'))) AND ($3 = '' OR h.assigned_to ILIKE '%' || $3 || '%')
		ORDER BY CASE h.status WHEN 'in_progress' THEN 0 WHEN 'open' THEN 1 WHEN 'completed' THEN 2 ELSE 3 END,
		CASE h.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, b.code`, property, ds, assignee)); err != nil {
		return out, err
	}
	for _, t := range out.Tasks {
		out.Counts[t.Status]++
	}
	if out.Rooms, err = m.RoomStates(ctx, q, property, day, nil); err != nil {
		return out, err
	}
	for _, r := range out.Rooms {
		out.RoomCount[r.OperationalStatus]++
	}
	return out, nil
}

// PlanDay adds the stayover cleaning of every occupied bungalow for a day
// (once per bungalow and day). Returns the tasks created.
func (m *Module) PlanDay(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]HKTask, error) {
	ds := day.Format(time.DateOnly)
	loc := calendar.Location(ctx, tx)
	next := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc)
	// in-house guests who stay the coming night (not departing today)
	rows, err := tx.Query(ctx, `SELECT s.unit_id, s.id FROM stay.stays s WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'checked_in'
		AND s.end_at >= $3 AND NOT EXISTS (SELECT 1 FROM stay.housekeeping_tasks h WHERE h.bungalow_id = s.unit_id AND h.task_date = $2::date
		AND h.task_type = 'stayover_cleaning')`, property, ds, next)
	if err != nil {
		return nil, err
	}
	type pair struct{ unit, stay uuid.UUID }
	var list []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.unit, &p.stay); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	rows.Close()
	out := []HKTask{}
	for _, p := range list {
		stay := p.stay
		t, err := m.createHKTask(ctx, tx, property, hkTaskInput{BungalowID: p.unit, StayID: &stay, TaskType: "stayover_cleaning", TaskDate: ds, Source: "schedule"})
		if err != nil {
			return out, err
		}
		out = append(out, t)
	}
	return out, nil
}

// actorName is the display name of the signed-in user.
func actorName(ctx context.Context, q dbtx.Querier) string {
	a := actor(ctx)
	if a == nil {
		return "system"
	}
	var name string
	if err := q.QueryRow(ctx, `SELECT full_name FROM platform.users WHERE id = $1`, *a).Scan(&name); err != nil {
		return "staff"
	}
	return name
}

func label(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == '_' {
			b[i] = ' '
		}
	}
	if len(b) > 0 && b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 32
	}
	return string(b)
}
