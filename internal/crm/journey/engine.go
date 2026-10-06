package journey

// The journey engine: enrollment (trigger, re-entry, control group, start
// step of anchored journeys), step execution (message with consent,
// suppression, frequency cap, quiet hours and A/B variant; wait; condition;
// voucher; points; reward; sales task; tag; exit) and the runs.

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"oneclub/internal/kernel/clock"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/crm/engagement"
	"oneclub/internal/crm/loyalty"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
)

// ── pure helpers (unit tested) ────────────────────────────────────────────

// bucket maps a key to 0–99 deterministically.
func bucket(parts ...string) int {
	h := fnv.New32a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return int(h.Sum32() % 100)
}

// InControl reports whether a customer falls in the control group of a
// journey (stable per journey and customer).
func InControl(journey, customer uuid.UUID, percent int) bool {
	return percent > 0 && bucket(journey.String(), customer.String()) < percent
}

// Variant is the A/B variant of a message step for an enrollment.
func Variant(enrollment uuid.UUID, key string, split int) string {
	if split > 0 && bucket(enrollment.String(), key) < split {
		return "B"
	}
	return "A"
}

// StartKey is the first step of an enrollment: for anchored journeys
// (membership end, birthday) the step after the last "wait until N days
// before" whose date has been reached, so a late entrant joins at the
// current stage instead of receiving the earlier messages at once.
func StartKey(steps []JourneyStep, anchor *time.Time, today time.Time) string {
	if len(steps) == 0 {
		return ""
	}
	start := 0
	if anchor != nil {
		for i, s := range steps {
			if s.StepType == "wait" && s.UntilDaysBefore != nil && !anchor.AddDate(0, 0, -*s.UntilDaysBefore).After(today) && i+1 < len(steps) {
				start = i + 1
			}
		}
	}
	return steps[start].Key
}

// nextKey is the step after steps[i]: its next key, else the following position.
func nextKey(steps []JourneyStep, i int) string {
	if k := strp(steps[i].NextKey); k != "" {
		return k
	}
	if i+1 < len(steps) {
		return steps[i+1].Key
	}
	return TargetEnd
}

func parseHM(s string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// QuietUntil reports whether t falls in the quiet hours [start, end) of the
// property's local time (the window may cross midnight) and returns when
// they end.
func QuietUntil(t time.Time, loc *time.Location, start, end string) (time.Time, bool) {
	s, ok1 := parseHM(start)
	e, ok2 := parseHM(end)
	if !ok1 || !ok2 || s == e {
		return t, false
	}
	l := t.In(loc)
	cur := l.Hour()*60 + l.Minute()
	in := (s < e && cur >= s && cur < e) || (s > e && (cur >= s || cur < e))
	if !in {
		return t, false
	}
	endAt := time.Date(l.Year(), l.Month(), l.Day(), e/60, e%60, 0, 0, loc)
	if !endAt.After(l) {
		endAt = endAt.AddDate(0, 0, 1)
	}
	return endAt.UTC(), true
}

// Capped reports whether a marketing message would exceed the cap.
func Capped(weekly, monthly int, pol Policy) bool {
	return (pol.WeeklyCap > 0 && weekly >= pol.WeeklyCap) || (pol.MonthlyCap > 0 && monthly >= pol.MonthlyCap)
}

// NextBirthday is the next birthday on or after today (29 February → 28
// February in other years).
func NextBirthday(birth, today time.Time) time.Time {
	for _, y := range []int{today.Year(), today.Year() + 1} {
		d := time.Date(y, birth.Month(), birth.Day(), 0, 0, 0, 0, time.UTC)
		if birth.Month() == time.February && birth.Day() == 29 && d.Month() != time.February {
			d = time.Date(y, time.February, 28, 0, 0, 0, 0, time.UTC)
		}
		if !d.Before(today) {
			return d
		}
	}
	return today
}

// ── enrollment ────────────────────────────────────────────────────────────

// EnrollRequest enrolls one customer.
type EnrollRequest struct {
	Customer   uuid.UUID
	Occurrence string
	Anchor     *time.Time
	Context    map[string]any
}

// enroll adds a customer to an active journey once per occurrence (and not
// while another enrollment of the journey is active or within the re-entry
// days). created is false when the customer was not enrolled.
func (s *Service) enroll(ctx context.Context, tx pgx.Tx, j Journey, r EnrollRequest) (uuid.UUID, bool, error) {
	if j.Status != "active" || len(j.Steps) == 0 {
		return uuid.Nil, false, nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.customers WHERE id = $1 AND property_id = $2 AND status = 'active' AND erased_at IS NULL)`,
		r.Customer, j.PropertyID).Scan(&ok); err != nil || !ok {
		return uuid.Nil, false, err
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.journey_enrollments WHERE journey_id = $1 AND customer_id = $2
		AND (occurrence = $3 OR status = 'active' OR ($4 > 0 AND entered_at > now() - make_interval(days => $4))))`, j.ID, r.Customer, r.Occurrence,
		j.ReEntryDays).Scan(&blocked); err != nil || blocked {
		return uuid.Nil, false, err
	}
	loc := location(ctx, tx, j.PropertyID)
	key := StartKey(j.Steps, r.Anchor, localDay(now(), loc))
	cohort := "treatment"
	if InControl(j.ID, r.Customer, j.ControlPercent) {
		cohort = "control"
	}
	data := r.Context
	if data == nil {
		data = map[string]any{}
	}
	raw, _ := json.Marshal(data)
	eid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.journey_enrollments (id, property_id, journey_id, customer_id, occurrence, cohort, current_key, next_run_at,
		anchor_date, context, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (journey_id, customer_id, occurrence) DO NOTHING`,
		eid, j.PropertyID, j.ID, r.Customer, r.Occurrence, cohort, key, now(), r.Anchor, raw, actor(ctx)); err != nil {
		return uuid.Nil, false, err
	}
	return eid, true, nil
}

// ── execution ─────────────────────────────────────────────────────────────

type enrollment struct {
	ID         uuid.UUID      `db:"id"`
	PropertyID uuid.UUID      `db:"property_id"`
	JourneyID  uuid.UUID      `db:"journey_id"`
	CustomerID uuid.UUID      `db:"customer_id"`
	Cohort     string         `db:"cohort"`
	Status     string         `db:"status"`
	CurrentKey *string        `db:"current_key"`
	AnchorDate *time.Time     `db:"anchor_date"`
	Context    map[string]any `db:"context"`
	EnteredAt  time.Time      `db:"entered_at"`
}

// RunResult reports a run.
type RunResult struct {
	Journeys  int `json:"journeys"`
	Enrolled  int `json:"enrolled"`
	Processed int `json:"processed" doc:"Enrollments processed"`
	Steps     int `json:"steps" doc:"Steps executed"`
	Sent      int `json:"sent" doc:"Messages sent"`
	Skipped   int `json:"skipped" doc:"Messages skipped (consent, suppression, contact, frequency cap)"`
	Deferred  int `json:"deferred" doc:"Messages waiting for the end of the quiet hours"`
	Completed int `json:"completed"`
	Exited    int `json:"exited"`
}

func (r *RunResult) add(o RunResult) {
	r.Journeys += o.Journeys
	r.Enrolled += o.Enrolled
	r.Processed += o.Processed
	r.Steps += o.Steps
	r.Sent += o.Sent
	r.Skipped += o.Skipped
	r.Deferred += o.Deferred
	r.Completed += o.Completed
	r.Exited += o.Exited
}

type eventExtra struct {
	Channel, Variant, Token, Subject, Body, OfferTitle, PromoCode string
	OfferExpires                                                  *time.Time
	Details                                                       map[string]any
}

// logEvent records an executed step and publishes crm.journey_step_executed.
func (s *Service) logEvent(ctx context.Context, tx pgx.Tx, j Journey, e enrollment, st JourneyStep, outcome string, x eventExtra) (uuid.UUID, error) {
	evID := id.New()
	if x.Details == nil {
		x.Details = map[string]any{}
	}
	raw, _ := json.Marshal(x.Details)
	if _, err := tx.Exec(ctx, `INSERT INTO crm.journey_events (id, property_id, journey_id, enrollment_id, customer_id, step_key, step_type, outcome, category,
		channel, variant, token, subject, body, offer_title, promo_code, offer_expires_on, details, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, evID, e.PropertyID, j.ID, e.ID, e.CustomerID, st.Key, st.StepType,
		outcome, j.Category, nullStr(x.Channel), nullStr(x.Variant), nullStr(x.Token), nullStr(x.Subject), nullStr(x.Body), nullStr(x.OfferTitle),
		nullStr(x.PromoCode), x.OfferExpires, raw, now()); err != nil {
		return evID, err
	}
	if s.Events != nil {
		pid := e.PropertyID
		if _, err := s.Events.Publish(ctx, tx, EventStepExecuted, "crm.journey_enrollment", &e.ID, &pid, map[string]any{"journeyId": j.ID,
			"journeyCode": j.Code, "enrollmentId": e.ID, "customerId": e.CustomerID, "cohort": e.Cohort, "stepKey": st.Key, "stepType": st.StepType,
			"outcome": outcome, "channel": nullStr(x.Channel), "variant": nullStr(x.Variant), "eventId": evID, "category": j.Category}); err != nil {
			return evID, err
		}
	}
	return evID, nil
}

func (s *Service) finish(ctx context.Context, tx pgx.Tx, e enrollment, status, reason string) error {
	col := "completed_at"
	if status == "exited" {
		col = "exited_at"
	}
	_, err := tx.Exec(ctx, `UPDATE crm.journey_enrollments SET status = $2, `+col+` = now(), exit_reason = $3, current_key = NULL, next_run_at = NULL
		WHERE id = $1`, e.ID, status, nullStr(reason))
	return err
}

// marketingCounts are the marketing messages a customer received in the
// last 7 and 30 days (journeys, and campaigns when the policy counts them).
func marketingCounts(ctx context.Context, q dbtx.Querier, customer uuid.UUID, at time.Time, campaigns bool) (int, int, error) {
	var w, m int
	err := q.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM crm.journey_events WHERE customer_id = $1 AND outcome = 'sent' AND category = 'marketing' AND created_at > $2::timestamptz - interval '7 days')
		+ CASE WHEN $3 THEN (SELECT count(*) FROM crm.campaign_recipients WHERE customer_id = $1 AND status = 'sent' AND sent_at > $2::timestamptz - interval '7 days') ELSE 0 END,
		(SELECT count(*) FROM crm.journey_events WHERE customer_id = $1 AND outcome = 'sent' AND category = 'marketing' AND created_at > $2::timestamptz - interval '30 days')
		+ CASE WHEN $3 THEN (SELECT count(*) FROM crm.campaign_recipients WHERE customer_id = $1 AND status = 'sent' AND sent_at > $2::timestamptz - interval '30 days') ELSE 0 END`,
		customer, at, campaigns).Scan(&w, &m)
	return w, m, err
}

// condition evaluates a condition step for an enrollment.
func (s *Service) condition(ctx context.Context, tx pgx.Tx, e enrollment, st JourneyStep) (bool, error) {
	var ok bool
	since := e.EnteredAt
	switch strp(st.ConditionKind) {
	case "booked":
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reporting.golf_bookings WHERE customer_id = $1 AND created_at >= $2 AND status <> 'cancelled')
			OR EXISTS (SELECT 1 FROM reporting.eng_payments WHERE customer_id = $1 AND paid_at >= $2 AND status IN ('completed', 'refunded'))`,
			e.CustomerID, since).Scan(&ok)
		return ok, err
	case "paid":
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reporting.eng_payments WHERE customer_id = $1 AND paid_at >= $2 AND status IN ('completed', 'refunded'))`,
			e.CustomerID, since).Scan(&ok)
		return ok, err
	case "renewed":
		anchor := e.AnchorDate
		var ms *uuid.UUID
		if v, _ := e.Context["membershipId"].(string); v != "" {
			if u, err := uuid.Parse(v); err == nil {
				ms = &u
			}
		}
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reporting.membership_lifecycle WHERE customer_id = $1 AND ($2::uuid IS NULL OR membership_id = $2)
			AND status = 'active' AND ends_on > coalesce($3::date, $4::date))`, e.CustomerID, ms, anchor,
			localDay(clock.Now(), location(ctx, tx, e.PropertyID))).Scan(&ok)
		return ok, err
	case "in_segment":
		sid, _ := uuid.Parse(strp(st.ConditionValue))
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.segment_members WHERE segment_id = $1 AND customer_id = $2)`, sid, e.CustomerID).Scan(&ok)
		return ok, err
	case "tier":
		var code *string
		if err := tx.QueryRow(ctx, `SELECT t.code FROM crm.loyalty_accounts a JOIN crm.loyalty_tiers t ON t.id = a.tier_id WHERE a.customer_id = $1
			AND a.status = 'active'`, e.CustomerID).Scan(&code); err != nil && !dbtx.IsNoRows(err) {
			return false, err
		}
		if code == nil {
			return false, nil
		}
		for _, c := range strings.Split(strp(st.ConditionValue), ",") {
			if strings.EqualFold(strings.TrimSpace(c), *code) {
				return true, nil
			}
		}
		return false, nil
	case "clicked":
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.journey_events WHERE enrollment_id = $1 AND clicked_at IS NOT NULL)`, e.ID).Scan(&ok)
		return ok, err
	case "opted_in":
		a, err := engagement.Addressee(ctx, tx, e.PropertyID, e.CustomerID, strp(st.ConditionValue), false)
		return a.Status == "queued", err
	}
	return false, nil
}

// message executes a message step; deferred reports the quiet hours.
func (s *Service) message(ctx context.Context, tx pgx.Tx, j Journey, e enrollment, st JourneyStep, pol Policy, at time.Time, loc *time.Location, res *RunResult) (bool, error) {
	marketing := j.Category == "marketing"
	if marketing {
		if until, quiet := QuietUntil(at, loc, pol.QuietStart, pol.QuietEnd); quiet {
			_, err := tx.Exec(ctx, `UPDATE crm.journey_enrollments SET current_key = $2, next_run_at = $3 WHERE id = $1`, e.ID, st.Key, until)
			res.Deferred++
			return true, err
		}
	}
	ch := strp(st.Channel)
	if e.Cohort == "control" {
		_, err := s.logEvent(ctx, tx, j, e, st, "control", eventExtra{Channel: ch})
		return false, err
	}
	a, err := engagement.Addressee(ctx, tx, e.PropertyID, e.CustomerID, ch, !marketing)
	if err != nil {
		return false, err
	}
	if a.Status == "queued" && ch == "in_app" && a.UserID == nil {
		a.Status = "skipped_no_contact"
	}
	if a.Status == "queued" && marketing {
		w, m, err := marketingCounts(ctx, tx, e.CustomerID, at, pol.CountCampaigns)
		if err != nil {
			return false, err
		}
		if Capped(w, m, pol) {
			a.Status = "skipped_frequency_cap"
		}
	}
	if a.Status != "queued" {
		res.Skipped++
		_, err := s.logEvent(ctx, tx, j, e, st, a.Status, eventExtra{Channel: ch})
		return false, err
	}
	variant := Variant(e.ID, st.Key, st.SplitPercent)
	subject, body := strp(st.Subject), strp(st.Body)
	if variant == "B" {
		if v := strp(st.SubjectB); v != "" {
			subject = v
		}
		body = strp(st.BodyB)
	}
	token := randomToken()
	var expires *time.Time
	if st.OfferValidDays != nil && *st.OfferValidDays > 0 {
		x := localDay(at, loc).AddDate(0, 0, *st.OfferValidDays)
		expires = &x
	}
	data := map[string]any{"name": a.Name, "promoCode": strp(st.PromoCode), "offer": strp(st.OfferTitle), "trackingToken": token,
		"link": s.website() + "/api/v1/public/journey-links/" + token, "preferencesLink": s.portal() + "/profile/communication-preferences"}
	if e.AnchorDate != nil {
		data["date"] = e.AnchorDate.Format("02 Jan 2006")
	}
	if d, _ := e.Context["detail"].(string); d != "" {
		data["detail"] = d
	}
	subject, body = engagement.Render(subject, data), engagement.Render(body, data)
	data["subject"], data["message"] = subject, body
	if !marketing {
		data["preferencesLink"] = ""
	}
	if s.Notify != nil {
		msg := notify.Message{Event: MessageTemplate, Category: "marketing", PropertyID: &e.PropertyID, Data: data, Channels: []string{ch}, Locale: a.Locale}
		if !marketing {
			msg.Category = "crm"
		}
		switch ch {
		case "email":
			msg.Email, msg.Name = a.Address, a.Name
		case "whatsapp":
			msg.Phone, msg.Name = a.Address, a.Name
		case "in_app":
			msg.UserIDs, msg.Link = []uuid.UUID{*a.UserID}, "/loyalty/offers/"+token
		}
		if err := s.Notify.Send(ctx, tx, msg); err != nil {
			return false, err
		}
	}
	res.Sent++
	_, err = s.logEvent(ctx, tx, j, e, st, "sent", eventExtra{Channel: ch, Variant: variant, Token: token, Subject: subject, Body: body,
		OfferTitle: strp(st.OfferTitle), PromoCode: strp(st.PromoCode), OfferExpires: expires})
	return false, err
}

// process runs the due steps of one enrollment until it waits or ends.
func (s *Service) process(ctx context.Context, tx pgx.Tx, j Journey, e enrollment, pol Policy, at time.Time, res *RunResult) error {
	idx := map[string]int{}
	for i, st := range j.Steps {
		idx[st.Key] = i
	}
	loc := location(ctx, tx, e.PropertyID)
	if j.ExitSegmentID != nil {
		var in bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.segment_members WHERE segment_id = $1 AND customer_id = $2)`, *j.ExitSegmentID,
			e.CustomerID).Scan(&in); err != nil {
			return err
		}
		if in {
			res.Exited++
			return s.finish(ctx, tx, e, "exited", "exit segment")
		}
	}
	key := strp(e.CurrentKey)
	for guard := 0; guard < 50; guard++ {
		switch key {
		case "", TargetEnd:
			res.Completed++
			return s.finish(ctx, tx, e, "completed", "")
		case TargetExit:
			res.Exited++
			return s.finish(ctx, tx, e, "exited", "exit step")
		}
		i, ok := idx[key]
		if !ok {
			res.Completed++
			return s.finish(ctx, tx, e, "completed", "step "+key+" removed")
		}
		st := j.Steps[i]
		next := nextKey(j.Steps, i)
		res.Steps++
		incentive := e.Cohort == "control" && (st.StepType == "voucher" || st.StepType == "points" || st.StepType == "reward" || st.StepType == "sales_task")
		switch {
		case st.StepType == "message":
			deferred, err := s.message(ctx, tx, j, e, st, pol, at, loc, res)
			if err != nil || deferred {
				return err
			}
		case st.StepType == "wait":
			var until time.Time
			if st.UntilDaysBefore != nil && e.AnchorDate != nil {
				d := e.AnchorDate.AddDate(0, 0, -*st.UntilDaysBefore)
				until = time.Date(d.Year(), d.Month(), d.Day(), 8, 0, 0, 0, loc).UTC()
			} else {
				until = at.Add(time.Duration(intp(st.WaitDays))*24*time.Hour + time.Duration(intp(st.WaitHours))*time.Hour)
			}
			if until.After(at) {
				if _, err := s.logEvent(ctx, tx, j, e, st, "waiting", eventExtra{Details: map[string]any{"until": until}}); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE crm.journey_enrollments SET current_key = $2, next_run_at = $3 WHERE id = $1`, e.ID, next, until)
				return err
			}
		case st.StepType == "condition":
			ok, err := s.condition(ctx, tx, e, st)
			if err != nil {
				return err
			}
			outcome, target := "condition_false", strp(st.OnFalse)
			if ok {
				outcome, target = "condition_true", strp(st.OnTrue)
			}
			if target != "" {
				next = target
			}
			if _, err := s.logEvent(ctx, tx, j, e, st, outcome, eventExtra{Details: map[string]any{"condition": strp(st.ConditionKind), "next": next}}); err != nil {
				return err
			}
		case incentive:
			if _, err := s.logEvent(ctx, tx, j, e, st, "control", eventExtra{}); err != nil {
				return err
			}
		case st.StepType == "voucher":
			if err := s.voucher(ctx, tx, j, e, st); err != nil {
				return err
			}
		case st.StepType == "points":
			if err := s.points(ctx, tx, j, e, st); err != nil {
				return err
			}
		case st.StepType == "reward":
			if err := s.reward(ctx, tx, j, e, st); err != nil {
				return err
			}
		case st.StepType == "sales_task":
			if err := s.salesTask(ctx, tx, j, e, st, at, loc); err != nil {
				return err
			}
		case st.StepType == "tag":
			if err := engagement.TagCustomer(ctx, tx, e.PropertyID, e.CustomerID, strp(st.Tag), "crm.journey", &j.ID); err != nil {
				return err
			}
			if _, err := s.logEvent(ctx, tx, j, e, st, "tagged", eventExtra{Details: map[string]any{"tag": strp(st.Tag)}}); err != nil {
				return err
			}
		case st.StepType == "exit":
			if _, err := s.logEvent(ctx, tx, j, e, st, "exited", eventExtra{}); err != nil {
				return err
			}
			next = TargetExit
		}
		key = next
		if _, err := tx.Exec(ctx, `UPDATE crm.journey_enrollments SET current_key = $2, next_run_at = $3 WHERE id = $1`, e.ID, key, at); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) voucher(ctx context.Context, tx pgx.Tx, j Journey, e enrollment, st JourneyStep) error {
	if s.IssueVoucher == nil {
		_, err := s.logEvent(ctx, tx, j, e, st, "failed", eventExtra{Details: map[string]any{"reason": "vouchers are not wired"}})
		return err
	}
	src := uuid.NewSHA1(e.ID, []byte(st.Key))
	codes, err := s.IssueVoucher(ctx, tx, VoucherRequest{PropertyID: e.PropertyID, TypeCode: strp(st.VoucherTypeRef), CustomerID: e.CustomerID,
		SourceID: src, Reference: j.Code + " · " + st.Name})
	if err != nil {
		return err
	}
	code := ""
	if len(codes) > 0 {
		code = codes[0]
	}
	_, err = s.logEvent(ctx, tx, j, e, st, "issued", eventExtra{PromoCode: code, OfferTitle: st.Name, Details: map[string]any{"voucherCodes": codes,
		"voucherType": strp(st.VoucherTypeRef)}})
	return err
}

func (s *Service) points(ctx context.Context, tx pgx.Tx, j Journey, e enrollment, st JourneyStep) error {
	if s.Loyalty == nil {
		_, err := s.logEvent(ctx, tx, j, e, st, "failed", eventExtra{Details: map[string]any{"reason": "loyalty is not wired"}})
		return err
	}
	pol, _, err := loyalty.LoadPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return err
	}
	ok, err := loyalty.WithinBudget(ctx, tx, e.PropertyID, pol.Value().Mul(decimal.NewFromInt(*st.Points)))
	if err != nil {
		return err
	}
	if !ok {
		_, err := s.logEvent(ctx, tx, j, e, st, "skipped_budget", eventExtra{Details: map[string]any{"points": *st.Points}})
		return err
	}
	entry, err := s.Loyalty.AwardPoints(ctx, tx, e.CustomerID, *st.Points, "crm.journey", e.ID, "journey:"+e.ID.String()+":"+st.Key,
		"Bonus points: "+j.Name)
	if err != nil {
		return err
	}
	if entry == nil {
		_, err := s.logEvent(ctx, tx, j, e, st, "failed", eventExtra{Details: map[string]any{"reason": "no_loyalty_account"}})
		return err
	}
	_, err = s.logEvent(ctx, tx, j, e, st, "issued", eventExtra{Details: map[string]any{"points": *st.Points, "ledgerEntryId": entry.ID}})
	return err
}

func (s *Service) reward(ctx context.Context, tx pgx.Tx, j Journey, e enrollment, st JourneyStep) error {
	if s.Loyalty == nil {
		_, err := s.logEvent(ctx, tx, j, e, st, "failed", eventExtra{Details: map[string]any{"reason": "loyalty is not wired"}})
		return err
	}
	jid := j.ID
	issue, status, err := s.Loyalty.IssueReward(ctx, tx, loyalty.IssueRequest{PropertyID: e.PropertyID, CustomerID: e.CustomerID, RewardID: *st.RewardID,
		Source: "journey", SourceID: &jid, SourceRef: j.Code + " · " + st.Key, Key: "jrn:" + e.ID.String() + ":" + st.Key, EnforceBudget: true})
	if err != nil {
		return err
	}
	if status == loyalty.IssueSkippedBudget {
		_, err := s.logEvent(ctx, tx, j, e, st, "skipped_budget", eventExtra{})
		return err
	}
	code := ""
	if issue.FulfilmentCode != nil {
		code = *issue.FulfilmentCode
	}
	_, err = s.logEvent(ctx, tx, j, e, st, "issued", eventExtra{OfferTitle: issue.RewardName, PromoCode: code, Details: map[string]any{"issueId": issue.ID,
		"number": issue.Number}})
	return err
}

func (s *Service) salesTask(ctx context.Context, tx pgx.Tx, j Journey, e enrollment, st JourneyStep, at time.Time, loc *time.Location) error {
	var owner *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT owner_user_id FROM crm.sales_opportunities WHERE customer_id = $1 AND owner_user_id IS NOT NULL
		ORDER BY created_at DESC LIMIT 1`, e.CustomerID).Scan(&owner); err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	name := ""
	if c, err := crm.GetCustomer(ctx, tx, e.CustomerID); err == nil {
		name = c.Name
	}
	data := map[string]any{"name": name}
	if e.AnchorDate != nil {
		data["date"] = e.AnchorDate.Format("02 Jan 2006")
	}
	subject := engagement.Render(strp(st.TaskSubject), data)
	due := localDay(at, loc).AddDate(0, 0, intp(st.TaskDueDays))
	dueAt := time.Date(due.Year(), due.Month(), due.Day(), 17, 0, 0, 0, loc)
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_activities (id, property_id, customer_id, activity_type, direction, subject, notes, due_at, assigned_to,
		status, source, created_by, updated_by) VALUES ($1,$2,$3,'task','internal',$4,$5,$6,$7,'open','system',$8,$8)`, aid, e.PropertyID, e.CustomerID,
		subject, "Journey "+j.Code+" · "+j.Name, dueAt, owner, actor(ctx)); err != nil {
		return err
	}
	_, err := s.logEvent(ctx, tx, j, e, st, "created", eventExtra{Details: map[string]any{"activityId": aid, "subject": subject, "assignedTo": owner}})
	return err
}

// ── triggers & runs ───────────────────────────────────────────────────────

// dateTargets finds the customers a date trigger enrolls today.
func (s *Service) dateTargets(ctx context.Context, tx pgx.Tx, j Journey, today time.Time) ([]EnrollRequest, error) {
	var out []EnrollRequest
	switch strp(j.TriggerDate) {
	case "birthday":
		type row struct {
			ID    uuid.UUID `db:"id"`
			Birth time.Time `db:"birth_date"`
		}
		rs, err := handle.List[row](tx.Query(ctx, `SELECT id, birth_date FROM crm.customers WHERE property_id = $1 AND status = 'active' AND erased_at IS NULL
			AND birth_date IS NOT NULL`, j.PropertyID))
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			next := NextBirthday(r.Birth, today)
			if next.After(today.AddDate(0, 0, j.TriggerDays)) {
				continue
			}
			a := next
			out = append(out, EnrollRequest{Customer: r.ID, Occurrence: fmt.Sprintf("bday:%d", next.Year()), Anchor: &a})
		}
	case "membership_expiry":
		type row struct {
			Customer   uuid.UUID `db:"customer_id"`
			Membership uuid.UUID `db:"membership_id"`
			EndsOn     time.Time `db:"ends_on"`
			Type       string    `db:"type_name"`
		}
		rs, err := handle.List[row](tx.Query(ctx, `SELECT DISTINCT ON (customer_id) customer_id, membership_id, ends_on, type_name
			FROM reporting.membership_lifecycle WHERE property_id = $1 AND role = 'principal' AND status = 'active' AND customer_id IS NOT NULL
			AND ends_on BETWEEN $2::date AND $2::date + $3::int ORDER BY customer_id, ends_on`, j.PropertyID, today, j.TriggerDays))
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			a := r.EndsOn
			out = append(out, EnrollRequest{Customer: r.Customer, Occurrence: "ms:" + r.Membership.String() + ":" + r.EndsOn.Format("2006-01-02"), Anchor: &a,
				Context: map[string]any{"membershipId": r.Membership.String(), "detail": r.Type, "endsOn": r.EndsOn.Format("2006-01-02")}})
		}
	case "last_visit":
		type row struct {
			Customer uuid.UUID `db:"customer_id"`
			Last     time.Time `db:"last_day"`
		}
		rs, err := handle.List[row](tx.Query(ctx, `SELECT customer_id, last_day FROM (SELECT customer_id, max(business_date) AS last_day
			FROM reporting.eng_folio_lines WHERE property_id = $1 AND customer_id IS NOT NULL AND NOT liability GROUP BY customer_id) x
			WHERE last_day BETWEEN $2::date - ($3::int + 30) AND $2::date - $3::int`, j.PropertyID, today, j.TriggerDays))
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			out = append(out, EnrollRequest{Customer: r.Customer, Occurrence: "lv:" + r.Last.Format("2006-01-02"),
				Context: map[string]any{"lastVisit": r.Last.Format("2006-01-02")}})
		}
	}
	return out, nil
}

// trigger enrolls the customers of the date and segment-entry triggers.
func (s *Service) trigger(ctx context.Context, tx pgx.Tx, j Journey, today time.Time) (int, error) {
	var reqs []EnrollRequest
	switch j.TriggerType {
	case "date":
		var err error
		if reqs, err = s.dateTargets(ctx, tx, j, today); err != nil {
			return 0, err
		}
	case "segment_entry":
		if j.TriggerSegmentID == nil {
			return 0, nil
		}
		rows, err := tx.Query(ctx, `SELECT m.customer_id FROM crm.segment_members m WHERE m.segment_id = $1 AND NOT EXISTS
			(SELECT 1 FROM crm.journey_enrollments e WHERE e.journey_id = $2 AND e.customer_id = m.customer_id AND e.occurrence = $3) ORDER BY m.customer_id`,
			*j.TriggerSegmentID, j.ID, "seg:"+j.TriggerSegmentID.String())
		if err != nil {
			return 0, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return 0, err
		}
		for _, c := range ids {
			reqs = append(reqs, EnrollRequest{Customer: c, Occurrence: "seg:" + j.TriggerSegmentID.String()})
		}
	}
	n := 0
	for _, r := range reqs {
		_, created, err := s.enroll(ctx, tx, j, r)
		if err != nil {
			return n, err
		}
		if created {
			n++
		}
	}
	return n, nil
}

// RunJourney enrolls the customers of a journey's date / segment trigger
// and processes its due enrollments (at most the batch size).
func (s *Service) RunJourney(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID) (RunResult, error) {
	res := RunResult{Journeys: 1}
	j, err := Get(ctx, tx, property, jid, true)
	if err != nil {
		return res, err
	}
	if j.Status != "active" {
		return res, nil
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return res, err
	}
	at := now()
	loc := location(ctx, tx, property)
	if res.Enrolled, err = s.trigger(ctx, tx, j, localDay(at, loc)); err != nil {
		return res, err
	}
	at = now() // the enrollments of the trigger are due at once
	es, err := handle.List[enrollment](tx.Query(ctx, `SELECT id, property_id, journey_id, customer_id, cohort, status, current_key, anchor_date, context,
		entered_at FROM crm.journey_enrollments WHERE journey_id = $1 AND status = 'active' AND next_run_at <= $2 ORDER BY next_run_at, id LIMIT $3
		FOR UPDATE SKIP LOCKED`, jid, at, pol.BatchSize))
	if err != nil {
		return res, err
	}
	for _, e := range es {
		res.Processed++
		if err := s.process(ctx, tx, j, e, pol, at, &res); err != nil {
			return res, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE crm.journeys SET last_run_at = $2 WHERE id = $1`, jid, at)
	return res, err
}

// RunProperty runs every active journey of a property.
func (s *Service) RunProperty(ctx context.Context, tx pgx.Tx, property uuid.UUID) (RunResult, error) {
	var res RunResult
	rows, err := tx.Query(ctx, `SELECT id FROM crm.journeys WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY code`, property)
	if err != nil {
		return res, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return res, err
	}
	for _, jid := range ids {
		r, err := s.RunJourney(ctx, tx, property, jid)
		if err != nil {
			return res, err
		}
		res.add(r)
	}
	return res, nil
}

// RunDue runs every active journey of every property, one transaction per
// journey (job).
func (s *Service) RunDue(ctx context.Context) (RunResult, error) {
	ctx = dbtx.System(ctx)
	var res RunResult
	type jr struct {
		ID       uuid.UUID `db:"id"`
		Property uuid.UUID `db:"property_id"`
	}
	var list []jr
	if err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		list, err = handle.List[jr](tx.Query(ctx, `SELECT id, property_id FROM crm.journeys WHERE status = 'active' AND archived_at IS NULL ORDER BY id`))
		return err
	}); err != nil {
		return res, err
	}
	for _, j := range list {
		if err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			r, err := s.RunJourney(ctx, tx, j.Property, j.ID)
			res.add(r)
			return err
		}); err != nil {
			return res, fmt.Errorf("journey %s: %w", j.ID, err)
		}
	}
	return res, nil
}
