package journey

// Journeys and their steps: create, replace (draft / paused only), activate
// through the approval engine, pause, resume, complete and archive.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

const journeySelect = `SELECT j.id, j.code, j.name, j.description, j.template, j.category, j.trigger_type, j.trigger_event, j.trigger_segment_id,
	j.trigger_date, j.trigger_days, j.exit_events, j.exit_segment_id, j.goal_event, j.goal_days, j.re_entry_days, j.control_percent, j.status,
	j.approval_request_id, j.activated_at, j.paused_at, j.completed_at, j.last_run_at, j.created_at, j.updated_at, j.property_id,
	(SELECT count(*) FROM crm.journey_enrollments e WHERE e.journey_id = j.id)::int AS enrolled,
	(SELECT count(*) FROM crm.journey_enrollments e WHERE e.journey_id = j.id AND e.status = 'active')::int AS active_enrollments
	FROM crm.journeys j`

const stepSelect = `SELECT key, position, step_type, name, channel, subject, body, subject_b, body_b, split_percent, offer_title, promo_code,
	offer_valid_days, wait_days, wait_hours, until_days_before, condition_kind, condition_value, on_true, on_false, next_key, points, voucher_type_ref,
	reward_id, task_subject, task_due_days, tag FROM crm.journey_steps`

// Steps lists the steps of a journey in order.
func Steps(ctx context.Context, q dbtx.Querier, jid uuid.UUID) ([]JourneyStep, error) {
	return handle.List[JourneyStep](q.Query(ctx, stepSelect+` WHERE journey_id = $1 ORDER BY position, key`, jid))
}

// Get loads a journey of a property with its steps.
func Get(ctx context.Context, q dbtx.Querier, property, jid uuid.UUID, lock bool) (Journey, error) {
	sql := journeySelect + ` WHERE j.id = $1 AND j.property_id = $2 AND j.archived_at IS NULL`
	if lock {
		sql += ` FOR UPDATE OF j`
	}
	rows, err := q.Query(ctx, sql, jid, property)
	j, err := handle.One[Journey](rows, err, "journey")
	if err != nil {
		return j, err
	}
	if j.ExitEvents == nil {
		j.ExitEvents = []string{}
	}
	j.Steps, err = Steps(ctx, q, jid)
	return j, err
}

// List lists the journeys of a property.
func List(ctx context.Context, q dbtx.Querier, property uuid.UUID, status, text string, limit int) ([]Journey, error) {
	if limit <= 0 {
		limit = 100
	}
	out, err := handle.List[Journey](q.Query(ctx, journeySelect+` WHERE j.property_id = $1 AND j.archived_at IS NULL AND ($2 = '' OR j.status = $2)
		AND ($3 = '' OR j.code ILIKE '%' || $3 || '%' OR j.name ILIKE '%' || $3 || '%') ORDER BY j.code, j.id LIMIT $4`, property, status, text, limit))
	for i := range out {
		if out[i].ExitEvents == nil {
			out[i].ExitEvents = []string{}
		}
		out[i].Steps = []JourneyStep{}
	}
	return out, err
}

// checkRefs verifies the segments and rewards a journey refers to.
func checkRefs(ctx context.Context, tx pgx.Tx, property uuid.UUID, in JourneyInput) error {
	exists := func(table string, v uuid.UUID) (bool, error) {
		var ok bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`, v, property).Scan(&ok)
		return ok, err
	}
	for _, r := range []struct {
		field, table string
		v            *uuid.UUID
	}{{"triggerSegmentId", "crm.segments", in.TriggerSegmentID}, {"exitSegmentId", "crm.segments", in.ExitSegmentID}} {
		if r.v == nil {
			continue
		}
		ok, err := exists(r.table, *r.v)
		if err != nil {
			return err
		}
		if !ok {
			return handle.Invalid(r.field, "not_found", "segment not found in this property")
		}
	}
	for i, st := range in.Steps {
		if st.RewardID != nil && st.StepType == "reward" {
			ok, err := exists("crm.loyalty_rewards", *st.RewardID)
			if err != nil {
				return err
			}
			if !ok {
				return handle.Invalid(stepField(i, "rewardId"), "not_found", "reward not found in this property")
			}
		}
		if st.StepType == "condition" && strp(st.ConditionKind) == "in_segment" {
			sid, _ := uuid.Parse(strp(st.ConditionValue))
			ok, err := exists("crm.segments", sid)
			if err != nil {
				return err
			}
			if !ok {
				return handle.Invalid(stepField(i, "conditionValue"), "not_found", "segment not found in this property")
			}
		}
	}
	return nil
}

func writeSteps(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID, steps []JourneyStep) error {
	if _, err := tx.Exec(ctx, `DELETE FROM crm.journey_steps WHERE journey_id = $1`, jid); err != nil {
		return err
	}
	for _, s := range steps {
		var ch *string
		if s.StepType == "message" {
			ch = s.Channel
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.journey_steps (id, property_id, journey_id, key, position, step_type, name, channel, subject, body,
			subject_b, body_b, split_percent, offer_title, promo_code, offer_valid_days, wait_days, wait_hours, until_days_before, condition_kind,
			condition_value, on_true, on_false, next_key, points, voucher_type_ref, reward_id, task_subject, task_due_days, tag)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`,
			id.New(), property, jid, s.Key, s.Position, s.StepType, s.Name, ch, blank(s.Subject), blank(s.Body), blank(s.SubjectB), blank(s.BodyB),
			s.SplitPercent, blank(s.OfferTitle), upper(s.PromoCode), s.OfferValidDays, s.WaitDays, s.WaitHours, s.UntilDaysBefore, blank(s.ConditionKind),
			blank(s.ConditionValue), blank(s.OnTrue), blank(s.OnFalse), blank(s.NextKey), s.Points, blank(s.VoucherTypeRef), s.RewardID,
			blank(s.TaskSubject), s.TaskDueDays, blank(s.Tag)); err != nil {
			return err
		}
	}
	return nil
}

func blank(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	return p
}

func upper(p *string) *string {
	if p = blank(p); p == nil {
		return nil
	}
	u := strings.ToUpper(strings.TrimSpace(*p))
	return &u
}

func emptyIfNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Create stores a new draft journey.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, property uuid.UUID, in JourneyInput, template string) (Journey, error) {
	if err := Validate(&in); err != nil {
		return Journey{}, err
	}
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.journeys WHERE property_id = $1 AND code = $2)`, property, in.Code).Scan(&taken); err != nil {
		return Journey{}, err
	}
	if taken {
		e := errs.Conflict("journey_code_taken", "a journey with code "+in.Code+" already exists")
		e.Fields = []errs.FieldError{errs.Field("code", "taken", "code is taken")}
		return Journey{}, e
	}
	if err := checkRefs(ctx, tx, property, in); err != nil {
		return Journey{}, err
	}
	if template == "" {
		template = "custom"
	}
	jid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.journeys (id, property_id, code, name, description, template, category, trigger_type, trigger_event,
		trigger_segment_id, trigger_date, trigger_days, exit_events, exit_segment_id, goal_event, goal_days, re_entry_days, control_percent, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$19)`, jid, property, in.Code, strings.TrimSpace(in.Name),
		nullStr(in.Description), template, in.Category, in.TriggerType, nullStr(in.TriggerEvent), in.TriggerSegmentID, nullStr(in.TriggerDate),
		in.TriggerDays, emptyIfNil(in.ExitEvents), in.ExitSegmentID, nullStr(in.GoalEvent), in.GoalDays, in.ReEntryDays, in.ControlPercent,
		actor(ctx)); err != nil {
		return Journey{}, err
	}
	if err := writeSteps(ctx, tx, property, jid, in.Steps); err != nil {
		return Journey{}, err
	}
	return Get(ctx, tx, property, jid, false)
}

// Update replaces the definition of a draft or paused journey (enrollments
// continue from their current step when it still exists).
func (s *Service) Update(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID, in JourneyInput) (Journey, Journey, error) {
	before, err := Get(ctx, tx, property, jid, true)
	if err != nil {
		return before, before, err
	}
	if before.Status != "draft" && before.Status != "paused" {
		return before, before, errs.Conflict("journey_not_editable", "pause the journey before changing it (status "+before.Status+")")
	}
	if err := Validate(&in); err != nil {
		return before, before, err
	}
	if !strings.EqualFold(strings.TrimSpace(in.Code), before.Code) {
		return before, before, handle.Invalid("code", "immutable", "the code of a journey cannot change")
	}
	if err := checkRefs(ctx, tx, property, in); err != nil {
		return before, before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET name = $2, description = $3, category = $4, trigger_type = $5, trigger_event = $6,
		trigger_segment_id = $7, trigger_date = $8, trigger_days = $9, exit_events = $10, exit_segment_id = $11, goal_event = $12, goal_days = $13,
		re_entry_days = $14, control_percent = $15, updated_by = $16 WHERE id = $1`, jid, strings.TrimSpace(in.Name), nullStr(in.Description), in.Category,
		in.TriggerType, nullStr(in.TriggerEvent), in.TriggerSegmentID, nullStr(in.TriggerDate), in.TriggerDays, emptyIfNil(in.ExitEvents),
		in.ExitSegmentID, nullStr(in.GoalEvent), in.GoalDays, in.ReEntryDays, in.ControlPercent, actor(ctx)); err != nil {
		return before, before, err
	}
	if err := writeSteps(ctx, tx, property, jid, in.Steps); err != nil {
		return before, before, err
	}
	after, err := Get(ctx, tx, property, jid, false)
	return before, after, err
}

func messageSteps(steps []JourneyStep) int {
	n := 0
	for _, st := range steps {
		if st.StepType == "message" {
			n++
		}
	}
	return n
}

// Activate starts a draft journey through the Journey Activation approval
// (approved at once without a workflow) or resumes a paused one.
func (s *Service) Activate(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID) (Journey, Journey, error) {
	before, err := Get(ctx, tx, property, jid, true)
	if err != nil {
		return before, before, err
	}
	switch before.Status {
	case "paused":
		if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET status = 'active', paused_at = NULL, updated_by = $2 WHERE id = $1`, jid, actor(ctx)); err != nil {
			return before, before, err
		}
	case "draft":
		if len(before.Steps) == 0 {
			return before, before, errs.Conflict("journey_without_steps", "add steps before activating the journey")
		}
		pol, err := LoadPolicy(ctx, tx, property)
		if err != nil {
			return before, before, err
		}
		if !pol.ActivationReview || s.Approvals == nil {
			if err := s.setActive(ctx, tx, jid); err != nil {
				return before, before, err
			}
			break
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET status = 'pending', updated_by = $2 WHERE id = $1`, jid, actor(ctx)); err != nil {
			return before, before, err
		}
		rid, _, err := s.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: ActivationDocumentType.Code, DocumentID: jid, DocumentRef: before.Code,
			Title: "Activate journey " + before.Code + " · " + before.Name, PropertyID: property,
			Attributes: map[string]any{"category": before.Category, "messages": messageSteps(before.Steps)}})
		if err != nil {
			return before, before, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET approval_request_id = $2 WHERE id = $1`, jid, rid); err != nil {
			return before, before, err
		}
	default:
		return before, before, errs.Conflict("journey_not_startable", "journey "+before.Code+" is "+before.Status)
	}
	after, err := Get(ctx, tx, property, jid, false)
	return before, after, err
}

func (s *Service) setActive(ctx context.Context, tx pgx.Tx, jid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE crm.journeys SET status = 'active', activated_at = coalesce(activated_at, now()), paused_at = NULL WHERE id = $1`, jid)
	return err
}

// Decision applies the Journey Activation approval.
func (s *Service) Decision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM crm.journeys WHERE id = $1 FOR UPDATE`, d.DocumentID).Scan(&status)
	if dbtx.IsNoRows(err) || (err == nil && status != "pending") {
		return nil
	}
	if err != nil {
		return err
	}
	to := "draft"
	if d.Status == approval.StatusApproved {
		to = "active"
		if err := s.setActive(ctx, tx, d.DocumentID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET status = 'draft' WHERE id = $1`, d.DocumentID); err != nil {
		return err
	}
	pid := d.PropertyID
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.journey", EntityID: d.DocumentID.String(),
		EntityLabel: "journey activation", PropertyID: &pid, Reason: d.Reason, Before: map[string]any{"status": "pending"}, After: map[string]any{"status": to}})
}

// Pause stops processing (enrollments wait where they are; no new ones).
func (s *Service) Pause(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID) (Journey, Journey, error) {
	before, err := Get(ctx, tx, property, jid, true)
	if err != nil {
		return before, before, err
	}
	if before.Status != "active" {
		return before, before, errs.Conflict("journey_not_active", "journey "+before.Code+" is "+before.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET status = 'paused', paused_at = now(), updated_by = $2 WHERE id = $1`, jid, actor(ctx)); err != nil {
		return before, before, err
	}
	after, err := Get(ctx, tx, property, jid, false)
	return before, after, err
}

// Complete ends a journey: no new enrollments, open ones exit.
func (s *Service) Complete(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID, reason string) (Journey, Journey, error) {
	before, err := Get(ctx, tx, property, jid, true)
	if err != nil {
		return before, before, err
	}
	if before.Status != "active" && before.Status != "paused" {
		return before, before, errs.Conflict("journey_not_running", "journey "+before.Code+" is "+before.Status)
	}
	if err := handle.Required("reason", reason); err != nil {
		return before, before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET status = 'completed', completed_at = now(), updated_by = $2 WHERE id = $1`, jid, actor(ctx)); err != nil {
		return before, before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.journey_enrollments SET status = 'exited', exited_at = now(), exit_reason = 'journey completed', next_run_at = NULL
		WHERE journey_id = $1 AND status = 'active'`, jid); err != nil {
		return before, before, err
	}
	after, err := Get(ctx, tx, property, jid, false)
	return before, after, err
}

// Archive removes a draft or completed journey from the lists.
func (s *Service) Archive(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID) (Journey, error) {
	before, err := Get(ctx, tx, property, jid, true)
	if err != nil {
		return before, err
	}
	if before.Status != "draft" && before.Status != "completed" {
		return before, errs.Conflict("journey_running", "complete the journey before archiving it")
	}
	_, err = tx.Exec(ctx, `UPDATE crm.journeys SET archived_at = now(), updated_by = $2 WHERE id = $1`, jid, actor(ctx))
	return before, err
}
