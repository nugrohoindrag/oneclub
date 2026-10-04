package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notification"
	"oneclub/internal/reservation"
)

func integrationID(t testing.TB, in *Instance, code string) string {
	var v string
	sysQueryRow(t, in, `SELECT id::text FROM platform.integrations WHERE code = $1`, []any{code}, &v)
	return v
}

func deliveryStatus(t testing.TB, in *Instance, id string) (status string, attempts int) {
	sysQueryRow(t, in, `SELECT status, attempts FROM platform.notification_deliveries WHERE id = $1`, []any{id}, &status, &attempts)
	return
}

// EP-05 AC: a test event sends e-mail and in-app notifications in the
// recipient's language; temporary SMTP failures are retried automatically.
func TestNotificationsDeliveryAndRetry(t *testing.T) {
	integration.ResetMockEmail()
	pa := platformAdmin(t, inst2)
	mock := integrationID(t, inst2, "mock-email")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mock, map[string]any{"enabled": true, "settings": map[string]any{"failTimes": 2}})
	// Recipient with Indonesian locale.
	gm := login(t, inst2, "gm@demo.oneclub.id", demoPassword)
	var gmID string
	sysQueryRow(t, inst2, `SELECT id::text FROM platform.users WHERE email = 'gm@demo.oneclub.id'`, nil, &gmID)
	pa.Must(202, "POST", "/api/v1/platform/notifications:send-test", map[string]any{"userId": gmID})
	var did string
	sysQueryRow(t, inst2, `SELECT id::text FROM platform.notification_deliveries WHERE event_code = 'system.test' AND channel = 'email' AND user_id = $1
		ORDER BY created_at DESC LIMIT 1`, []any{gmID}, &did)
	waitFor(t, 45*time.Second, "e-mail sent after retries", func() bool {
		st, _ := deliveryStatus(t, inst2, did)
		return st == "sent"
	})
	if _, attempts := deliveryStatus(t, inst2, did); attempts != 3 {
		t.Fatalf("expected 2 failures + 1 success = 3 attempts, got %d", attempts)
	}
	var subject string
	sysQueryRow(t, inst2, `SELECT subject FROM platform.notification_deliveries WHERE id = $1`, []any{did}, &subject)
	if subject != "Notifikasi uji" {
		t.Fatalf("expected Indonesian subject, got %q", subject)
	}
	// In-app notification center (FR-NOT-05).
	unread := gm.Must(200, "GET", "/api/v1/platform/notifications/unread-count", nil).JSON()["unread"].(float64)
	if unread < 1 {
		t.Fatal("expected an unread in-app notification")
	}
	items := gm.Must(200, "GET", "/api/v1/platform/notifications?filter[unread]=true", nil).Items()
	gm.Must(204, "POST", "/api/v1/platform/notifications/"+str(items[0]["id"])+":read", nil)
	gm.Must(204, "POST", "/api/v1/platform/notifications:read-all", nil)
	if gm.Must(200, "GET", "/api/v1/platform/notifications/unread-count", nil).JSON()["unread"].(float64) != 0 {
		t.Fatal("read-all")
	}
	// Delivery history (FR-NOT-04).
	hist := pa.Must(200, "GET", "/api/v1/platform/notification-deliveries?filter[eventCode]=system.test", nil).Items()
	if len(hist) < 2 {
		t.Fatalf("history: %v", hist)
	}
	// Templates (FR-NOT-02).
	ts := pa.Must(200, "GET", "/api/v1/platform/notification-templates?filter[eventCode]=system.test", nil).Items()
	if len(ts) != 4 {
		t.Fatalf("expected 4 templates (2 channels × 2 languages), got %d", len(ts))
	}
	pa.Must(422, "PATCH", "/api/v1/platform/notification-templates/"+str(ts[0]["id"]), map[string]any{"body": "{{.broken"})
	pa.Must(200, "PATCH", "/api/v1/platform/notification-templates/"+str(ts[0]["id"]), map[string]any{"subject": str(ts[0]["subject"])})
	// Preferences (FR-NOT-06): opt-out of system e-mails; security stays mandatory.
	prefs := gm.Must(200, "PUT", "/api/v1/platform/notification-preferences", map[string]any{"preferences": []map[string]any{
		{"category": "system", "inApp": true, "email": false, "whatsapp": false},
		{"category": "security", "inApp": false, "email": false, "whatsapp": false},
	}}).Items()
	for _, p := range prefs {
		if p["category"] == "security" && p["email"] != true {
			t.Fatal("mandatory category was opted out")
		}
	}
	gm.Must(200, "GET", "/api/v1/platform/notification-preferences", nil)
	before := countDeliveries(t, inst2, gmID, "email")
	pa.Must(202, "POST", "/api/v1/platform/notifications:send-test", map[string]any{"userId": gmID})
	if countDeliveries(t, inst2, gmID, "email") != before {
		t.Fatal("opted-out e-mail was queued")
	}
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mock, map[string]any{"enabled": false, "settings": map[string]any{}})
}

func countDeliveries(t testing.TB, in *Instance, user, channel string) int {
	var n int
	sysQueryRow(t, in, `SELECT count(*) FROM platform.notification_deliveries WHERE user_id = $1 AND channel = $2`, []any{user, channel}, &n)
	return n
}

// EP-09 AC: a job that fails 3 times appears in Background Jobs and then
// succeeds after Retry; the delivery becomes Failed, then Sent.
func TestFailedJobRetry(t *testing.T) {
	integration.ResetMockEmail()
	pa := platformAdmin(t, inst2)
	mock := integrationID(t, inst2, "mock-email")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mock, map[string]any{"enabled": true, "settings": map[string]any{"failTimes": 3}})
	defer pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mock, map[string]any{"enabled": false, "settings": map[string]any{}})
	ctx := dbtx.System(context.Background())
	did := id.New()
	var jobID int64
	err := inst2.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.notification_deliveries (id, event_code, category, channel, locale, recipient, subject, body, status)
			VALUES ($1, 'system.test', 'system', 'email', 'en', 'ops@demo.test', 'Probe', 'Probe', 'pending')`, did); err != nil {
			return err
		}
		var err error
		jobID, err = inst2.App.Jobs.Insert(ctx, tx, notification.DeliverArgs{DeliveryID: did}, &river.InsertOpts{Queue: jobs.QueueNotifications, MaxAttempts: 3})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 45*time.Second, "job discarded after 3 attempts", func() bool {
		for _, j := range pa.Must(200, "GET", "/api/v1/platform/jobs", nil).Items() {
			if int64(j["id"].(float64)) == jobID && j["state"] == "discarded" {
				return len(j["errors"].([]any)) == 3
			}
		}
		return false
	})
	if st, _ := deliveryStatus(t, inst2, did.String()); st != "failed" {
		t.Fatalf("delivery should be failed, got %s", st)
	}
	pa.Must(422, "POST", "/api/v1/platform/jobs/"+itoa64(jobID)+":discard", map[string]any{"reason": ""})
	pa.Must(200, "POST", "/api/v1/platform/jobs/"+itoa64(jobID)+":retry", map[string]any{"reason": "SMTP restored"})
	waitFor(t, 20*time.Second, "retried job completed", func() bool {
		st, _ := deliveryStatus(t, inst2, did.String())
		return st == "sent"
	})
	// Discard with reason on another failing job.
	integration.ResetMockEmail()
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mock, map[string]any{"settings": map[string]any{"alwaysFail": true}})
	did2 := id.New()
	var job2 int64
	_ = inst2.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `INSERT INTO platform.notification_deliveries (id, event_code, category, channel, locale, recipient, subject, body, status)
			VALUES ($1, 'system.test', 'system', 'email', 'en', 'ops@demo.test', 'Probe', 'Probe', 'pending')`, did2)
		job2, err = inst2.App.Jobs.Insert(ctx, tx, notification.DeliverArgs{DeliveryID: did2}, &river.InsertOpts{Queue: jobs.QueueNotifications, MaxAttempts: 10,
			ScheduledAt: time.Now().Add(time.Hour)})
		return err
	})
	pa.Must(200, "POST", "/api/v1/platform/jobs/"+itoa64(job2)+":discard", map[string]any{"reason": "duplicate message"})
	all := pa.Must(200, "GET", "/api/v1/platform/jobs?filter[state]=cancelled", nil).Items()
	if len(all) == 0 {
		t.Fatal("cancelled job not listed")
	}
	// A job that exhausts its attempts stays Failed (discarded).
	did3 := id.New()
	var job3 int64
	_ = inst2.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `INSERT INTO platform.notification_deliveries (id, event_code, category, channel, locale, recipient, subject, body, status)
			VALUES ($1, 'system.test', 'system', 'email', 'en', 'ops@demo.test', 'Probe', 'Probe', 'pending')`, did3)
		job3, err = inst2.App.Jobs.Insert(ctx, tx, notification.DeliverArgs{DeliveryID: did3}, &river.InsertOpts{Queue: jobs.QueueNotifications, MaxAttempts: 1})
		return err
	})
	waitFor(t, 30*time.Second, "job discarded", func() bool {
		for _, j := range pa.Must(200, "GET", "/api/v1/platform/jobs", nil).Items() {
			if int64(j["id"].(float64)) == job3 && j["state"] == "discarded" {
				return true
			}
		}
		return false
	})
	// FR-JOB-05: the health check alerts Platform Admins.
	var paID string
	sysQueryRow(t, inst2, `SELECT id::text FROM platform.users WHERE email = 'role.platform_admin@matrix.test'`, nil, &paID)
	h, err := maintenanceCheck(inst2)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) == 0 {
		t.Fatal("health check should report the failed job")
	}
	var alerts int
	sysQueryRow(t, inst2, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND event_code = 'system.job_alert'`, []any{paID}, &alerts)
	if alerts == 0 {
		t.Fatal("Platform Admin did not receive a job alert")
	}
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }

// EP-06 AC: a 2-step workflow with an amount condition runs end-to-end on
// the "Test Approval" document type; every decision is audited with actor
// and reason; approvers and requester are notified.
func TestApprovalTwoStepWorkflow(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	sa := superAdmin(t, inst)
	sa.Must(200, "GET", "/api/v1/platform/approval-document-types", nil)
	wfs := sa.Must(200, "GET", "/api/v1/platform/approval-workflows?filter[documentType]=test_approval", nil).Items()
	if len(wfs) != 1 || len(wfs[0]["steps"].([]any)) != 2 {
		t.Fatalf("seeded 2-step workflow expected: %v", wfs)
	}
	sa.Must(200, "GET", "/api/v1/platform/approval-workflows/"+str(wfs[0]["id"]), nil)

	// Large amount: both steps.
	big := pa.Must(201, "POST", "/api/v1/platform/approvals:test", map[string]any{"title": "Buy new golf carts", "amount": "15000000"}).JSON()
	if big["status"] != "pending" || big["currentStepNo"].(float64) != 1 {
		t.Fatalf("submit: %v", big)
	}
	pa.Must(403, "POST", "/api/v1/platform/approvals/"+str(big["id"])+":approve", map[string]any{})  // requester cannot approve
	fin.Must(403, "POST", "/api/v1/platform/approvals/"+str(big["id"])+":approve", map[string]any{}) // not step 1 approver
	inbox := gm.Must(200, "GET", "/api/v1/platform/approvals?box=inbox", nil).Items()
	if !containsID(inbox, str(big["id"])) {
		t.Fatal("request missing from GM inbox")
	}
	r1 := gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(big["id"])+":approve", map[string]any{"reason": "Budget ok"}).JSON()
	if r1["status"] != "pending" || r1["currentStepNo"].(float64) != 2 {
		t.Fatalf("after step 1: %v", r1)
	}
	if !containsID(fin.Must(200, "GET", "/api/v1/platform/approvals?box=inbox", nil).Items(), str(big["id"])) {
		t.Fatal("request missing from Finance inbox")
	}
	r2 := fin.Must(200, "POST", "/api/v1/platform/approvals/"+str(big["id"])+":approve", map[string]any{}).JSON()
	if r2["status"] != "approved" {
		t.Fatalf("final: %v", r2)
	}
	// Small amount: step 2 condition does not match → skipped.
	small := pa.Must(201, "POST", "/api/v1/platform/approvals:test", map[string]any{"title": "Buy tees", "amount": "5000000"}).JSON()
	r := gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(small["id"])+":approve", map[string]any{}).JSON()
	if r["status"] != "approved" {
		t.Fatalf("small amount should finish after step 1: %v", r)
	}
	steps := r["steps"].([]any)
	if steps[1].(map[string]any)["status"] != "skipped" {
		t.Fatalf("step 2 should be skipped: %v", steps)
	}
	// Reject needs a reason.
	rej := pa.Must(201, "POST", "/api/v1/platform/approvals:test", map[string]any{"title": "Buy yacht", "amount": "900000000"}).JSON()
	gm.Must(422, "POST", "/api/v1/platform/approvals/"+str(rej["id"])+":reject", map[string]any{})
	gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(rej["id"])+":reject", map[string]any{"reason": "Out of scope"})
	gm.Must(409, "POST", "/api/v1/platform/approvals/"+str(rej["id"])+":approve", map[string]any{})
	// Requester cancels while pending; drafts can be submitted.
	can := pa.Must(201, "POST", "/api/v1/platform/approvals:test", map[string]any{"title": "Changed mind", "amount": "1"}).JSON()
	gm.Must(403, "POST", "/api/v1/platform/approvals/"+str(can["id"])+":cancel", map[string]any{})
	pa.Must(200, "POST", "/api/v1/platform/approvals/"+str(can["id"])+":cancel", map[string]any{"reason": "no longer needed"})
	draft := pa.Must(201, "POST", "/api/v1/platform/approvals:test", map[string]any{"title": "Draft request", "amount": "2", "draft": true}).JSON()
	if draft["status"] != "draft" {
		t.Fatalf("draft: %v", draft)
	}
	pa.Must(200, "POST", "/api/v1/platform/approvals/"+str(draft["id"])+":submit", map[string]any{})
	mine := pa.Must(200, "GET", "/api/v1/platform/approvals?box=mine", nil).Items()
	if len(mine) < 5 {
		t.Fatalf("my requests: %d", len(mine))
	}
	sa.Must(200, "GET", "/api/v1/platform/approvals?box=all&filter[status]=approved", nil)
	login(t, inst, "golf.manager@demo.oneclub.id", demoPassword).Must(403, "GET", "/api/v1/platform/approvals?box=all", nil)
	pa.Must(200, "GET", "/api/v1/platform/approvals/"+str(big["id"]), nil)

	// Audit trail with actor and reason.
	logs := sa.Must(200, "GET", "/api/v1/audit/logs?filter[entityType]=platform.approval_request&filter[entityId]="+str(big["id"]), nil).Items()
	var approvals int
	for _, l := range logs {
		if l["action"] == "approval_approved" {
			approvals++
			if l["actorName"] == nil || len(l["actorRoles"].([]any)) == 0 {
				t.Fatalf("decision without actor: %v", l)
			}
		}
	}
	if approvals != 2 {
		t.Fatalf("expected 2 audited approvals, got %d", approvals)
	}
	rl := sa.Must(200, "GET", "/api/v1/audit/logs?filter[action]=approval_rejected&filter[entityId]="+str(rej["id"]), nil).Items()
	if len(rl) != 1 || rl[0]["reason"] != "Out of scope" {
		t.Fatalf("rejection audit: %v", rl)
	}
	// Notifications to approver and requester (FR-APR-05).
	var gmPending, paDecided int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications n JOIN platform.users u ON u.id = n.user_id
		WHERE u.email = 'gm@demo.oneclub.id' AND n.event_code = 'approval.pending'`, nil, &gmPending)
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications n JOIN platform.users u ON u.id = n.user_id
		WHERE u.email = 'property.admin@demo.oneclub.id' AND n.event_code = 'approval.decided'`, nil, &paDecided)
	if gmPending == 0 || paDecided == 0 {
		t.Fatalf("notifications: approver=%d requester=%d", gmPending, paDecided)
	}

	// Workflow management.
	nw := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "test_approval", "name": "MDR special",
		"propertyId": inst.MDR, "priority": 10, "steps": []map[string]any{{"stepNo": 1, "name": "Finance", "approverType": "role",
			"approverRoleId": roleID(t, sa, "finance_manager"), "conditions": []map[string]any{{"attribute": "amount", "operator": "gte", "value": 1}}, "slaHours": 4}}}).JSON()
	sa.Must(422, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "test_approval", "name": "Bad",
		"steps": []map[string]any{{"stepNo": 1, "name": "x", "approverType": "role", "conditions": []map[string]any{{"attribute": "colour", "operator": "eq", "value": 1}}}}})
	sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+str(nw["id"]), map[string]any{"status": "inactive"})
}

func containsID(items []map[string]any, idv string) bool {
	for _, x := range items {
		if x["id"] == idv {
			return true
		}
	}
	return false
}

// FR-APR-07/08: delegation and SLA reminders.
func TestApprovalDelegationAndReminder(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	golf := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	var golfID string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'golf.manager@demo.oneclub.id'`, nil, &golfID)
	d := gm.Must(201, "POST", "/api/v1/platform/approval-delegations", map[string]any{"delegateUserId": golfID,
		"startsAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "endsAt": time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339), "reason": "Annual leave"}).JSON()
	req := pa.Must(201, "POST", "/api/v1/platform/approvals:test", map[string]any{"title": "Delegated", "amount": "100"}).JSON()
	if !containsID(golf.Must(200, "GET", "/api/v1/platform/approvals?box=inbox", nil).Items(), str(req["id"])) {
		t.Fatal("delegate inbox misses the request")
	}
	res := golf.Must(200, "POST", "/api/v1/platform/approvals/"+str(req["id"])+":approve", map[string]any{}).JSON()
	if res["status"] != "approved" || res["steps"].([]any)[0].(map[string]any)["onBehalfOfName"] == nil {
		t.Fatalf("delegated approval: %v", res)
	}
	gm.Must(200, "GET", "/api/v1/platform/approval-delegations", nil)
	golf.Must(403, "POST", "/api/v1/platform/approval-delegations/"+str(d["id"])+":revoke", nil)
	gm.Must(204, "POST", "/api/v1/platform/approval-delegations/"+str(d["id"])+":revoke", nil)

	// SLA reminder.
	late := pa.Must(201, "POST", "/api/v1/platform/approvals:test", map[string]any{"title": "Overdue", "amount": "100"}).JSON()
	ctx := dbtx.System(context.Background())
	_ = inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.approval_request_steps SET due_at = now() - interval '1 hour' WHERE request_id = $1`, late["id"])
		return err
	})
	n, err := inst.App.Approvals.SendReminders(context.Background())
	if err != nil || n == 0 {
		t.Fatalf("reminders: %d %v", n, err)
	}
	var rem int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications n JOIN platform.users u ON u.id = n.user_id
		WHERE u.email = 'gm@demo.oneclub.id' AND n.event_code = 'approval.reminder'`, nil, &rem)
	if rem == 0 {
		t.Fatal("no reminder notification")
	}
}

// PRD §8 vertical slice: Venue → authz → RLS → audit → Venue Activation
// approval → notifications → outbox event → mock integration → report on
// the read replica.
func TestVerticalSliceVenueActivation(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	v := pa.Must(201, "POST", "/api/v1/platform/venues", map[string]any{"code": "LAKE", "name": "Lakeside Pavilion", "venueType": "banquet"}).JSON()
	if v["status"] != "pending" {
		t.Fatalf("venue: %v", v)
	}
	var reqID string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.approval_requests WHERE document_type = 'venue_activation' AND document_id = $1`, []any{v["id"]}, &reqID)
	// Approver notified (in-app + e-mail queued).
	var notified int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries d JOIN platform.users u ON u.id = d.user_id
		WHERE u.email = 'gm@demo.oneclub.id' AND d.event_code = 'approval.pending' AND d.body LIKE '%Lakeside%'`, nil, &notified)
	if notified < 2 {
		t.Fatalf("approver should get in-app + e-mail, got %d", notified)
	}
	logsBefore := countLogs(t, inst, "integration_code = 'mock-whatsapp' AND operation = 'send_message'")
	gm.Must(200, "POST", "/api/v1/platform/approvals/"+reqID+":approve", map[string]any{"reason": "Ready for events"})
	got := pa.Must(200, "GET", "/api/v1/platform/venues/"+str(v["id"]), nil).JSON()
	if got["status"] != "active" {
		t.Fatalf("venue not activated: %v", got)
	}
	// Outbox event dispatched → subscriber called the mock messaging adapter.
	waitFor(t, 30*time.Second, "outbox dispatch", func() bool {
		var pending int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'platform.venue_activated' AND aggregate_id = $1 AND dispatched_at IS NULL`,
			[]any{v["id"]}, &pending)
		return pending == 0 && countLogs(t, inst, "integration_code = 'mock-whatsapp' AND operation = 'send_message'") > logsBefore
	})
	// Report on the read replica shows the venue as Active.
	rep := gm.Must(200, "GET", "/api/v1/reporting/reports/platform.venue_directory?params[status]=active", nil).JSON()
	if rep["source"] != "replica" {
		t.Fatalf("report must run on the replica connection: %v", rep["source"])
	}
	found := false
	for _, row := range rep["rows"].([]any) {
		if row.(map[string]any)["code"] == "LAKE" {
			found = true
		}
	}
	if !found {
		t.Fatal("activated venue not in the Venue Directory Report")
	}
	// Full audit trail.
	sa := superAdmin(t, inst)
	trail := sa.Must(200, "GET", "/api/v1/audit/logs?filter[entityId]="+str(v["id"]), nil).Items()
	actions := map[string]bool{}
	for _, l := range trail {
		actions[str(l["action"])] = true
	}
	if !actions["create"] || !actions["status_change"] {
		t.Fatalf("venue audit trail incomplete: %v", actions)
	}
}

func countLogs(t testing.TB, in *Instance, where string) int {
	var n int
	sysQueryRow(t, in, `SELECT count(*) FROM platform.integration_logs WHERE `+where, nil, &n)
	return n
}

// EP-09 AC: an outbox event is still delivered after the worker restarts.
func TestOutboxSurvivesWorkerRestart(t *testing.T) {
	ctx := context.Background()
	stopCtx, c := context.WithTimeout(ctx, 15*time.Second)
	if err := inst2.App.Jobs.River.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	c()
	var eid uuid.UUID
	sctx := dbtx.System(ctx)
	err := inst2.DB.WithTx(sctx, func(tx pgx.Tx) error {
		var err error
		eid, err = inst2.App.Bus.Publish(sctx, tx, "platform.venue_activated", "platform.venue", nil, nil, map[string]any{"venueCode": "RESTART", "venueName": "Restart"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	var dispatched *time.Time
	sysQueryRow(t, inst2, `SELECT dispatched_at FROM platform.outbox WHERE id = $1`, []any{eid}, &dispatched)
	if dispatched != nil {
		t.Fatal("event dispatched while the worker was down")
	}
	// A new worker process starts (new River client on the same database).
	w, err := jobs.New(inst2.DB.Primary, inst2.App.Registrar, jobs.Options{Process: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.River.Start(ctx); err != nil {
		t.Fatal(err)
	}
	inst2.App.Jobs = w
	waitFor(t, 30*time.Second, "event dispatched after restart", func() bool {
		sysQueryRow(t, inst2, `SELECT dispatched_at FROM platform.outbox WHERE id = $1`, []any{eid}, &dispatched)
		return dispatched != nil
	})
	var processed int
	sysQueryRow(t, inst2, `SELECT count(*) FROM platform.outbox_processed WHERE event_id = $1`, []any{eid}, &processed)
	if processed != 1 {
		t.Fatalf("subscriber should have processed the event exactly once, got %d", processed)
	}
	// Re-dispatching is idempotent.
	if _, err := inst2.App.Dispatcher.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
}

// EP-08 AC: the mock payment adapter accepts correctly signed webhooks,
// rejects invalid ones, and processes duplicates only once.
func TestWebhooks(t *testing.T) {
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	sec := pa.Must(200, "POST", "/api/v1/platform/integrations/"+mp+":rotate-webhook-secret", nil).JSON()
	secret := str(sec["webhookSecret"])
	body := []byte(`{"id":"evt_001","type":"payment.paid","data":{"reference":"INV-1","amount":"150000"}}`)
	c := anon(t, inst)
	c.Property = uuid.Nil
	c.Must(401, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign("wrong", body, time.Now()))
	c.Must(401, "POST", "/api/v1/webhooks/mock-payment", body)
	c.Must(401, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now().Add(-time.Hour)))
	first := c.Must(200, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now())).JSON()
	second := c.Must(200, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now())).JSON()
	if first["status"] != "processed" || second["status"] != "duplicate" {
		t.Fatalf("idempotency: %v / %v", first, second)
	}
	var events, outbox int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.webhook_events WHERE external_id = 'evt_001'`, nil, &events)
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'integration.webhook_received' AND payload->>'externalId' = 'evt_001'`, nil, &outbox)
	if events != 1 || outbox != 1 {
		t.Fatalf("processed more than once: events=%d outbox=%d", events, outbox)
	}
	logs := pa.Must(200, "GET", "/api/v1/platform/integration-logs?filter[integrationCode]=mock-payment&filter[direction]=inbound", nil).Items()
	if len(logs) < 4 {
		t.Fatalf("inbound logs: %d", len(logs))
	}
	c.Must(404, "POST", "/api/v1/webhooks/unknown-integration", body)
}

// FR-INT-01/02/04/05: integrations with encrypted credentials, test
// connection and masked logs.
func TestIntegrations(t *testing.T) {
	pa := platformAdmin(t, inst)
	ads := pa.Must(200, "GET", "/api/v1/platform/integrations/adapters", nil).Items()
	caps := map[string]bool{}
	for _, a := range ads {
		caps[str(a["capability"])] = true
	}
	for _, c := range []string{"payment", "messaging", "email", "tax_invoice", "resident_data", "hardware"} {
		if !caps[c] {
			t.Fatalf("no adapter for capability %s", c)
		}
	}
	pa.Must(422, "POST", "/api/v1/platform/integrations", map[string]any{"code": "smtp-main", "adapter": "smtp", "name": "SMTP", "enabled": true})
	smtp := pa.Must(201, "POST", "/api/v1/platform/integrations", map[string]any{"code": "smtp-main", "adapter": "smtp", "name": "SMTP",
		"mode": "production", "credentials": map[string]string{"host": "127.0.0.1", "port": "1", "from": "no-reply@club.test", "password": "s3cret!"}}).JSON()
	if strings.Contains(string(pa.Must(200, "GET", "/api/v1/platform/integrations/"+str(smtp["id"]), nil).Body), "s3cret") {
		t.Fatal("credentials leaked in API response")
	}
	var raw []byte
	sysQueryRow(t, inst, `SELECT credentials_enc FROM platform.integrations WHERE code = 'smtp-main'`, nil, &raw)
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("credentials stored in clear text")
	}
	res := pa.Must(200, "POST", "/api/v1/platform/integrations/"+str(smtp["id"])+":test", nil).JSON()
	if res["ok"] != false {
		t.Fatalf("connection to closed port must fail: %v", res)
	}
	ef := pa.Must(201, "POST", "/api/v1/platform/integrations", map[string]any{"code": "efaktur", "adapter": "mock-efaktur", "name": "e-Faktur Sandbox", "enabled": true}).JSON()
	if ok := pa.Must(200, "POST", "/api/v1/platform/integrations/"+str(ef["id"])+":test", nil).JSON(); ok["ok"] != true {
		t.Fatalf("mock test connection: %v", ok)
	}
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+str(smtp["id"]), map[string]any{"credentials": map[string]string{"password": ""}, "name": "SMTP (disabled)"})
	pa.Must(200, "GET", "/api/v1/platform/integrations", nil)
	logs := pa.Must(200, "GET", "/api/v1/platform/integration-logs?q=test_connection", nil).Items()
	if len(logs) < 2 {
		t.Fatalf("integration logs: %d", len(logs))
	}
	// Payment and messaging sandbox adapters work for P1 development.
	p, err := inst.App.Integrations.Payment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pr, err := p.CreatePayment(context.Background(), integration.PaymentRequest{Reference: "INV-9", Amount: "250000", Currency: "IDR", Method: "qris"})
	if err != nil || pr.QRString == "" {
		t.Fatalf("mock payment: %v %v", pr, err)
	}
}

// FR-INT-07: bridge agent registration and heartbeat.
func TestBridgeAgent(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	a := pa.Must(201, "POST", "/api/v1/platform/bridge-agents", map[string]any{"name": "Locker Room Bridge"}).JSON()
	agent := anon(t, inst)
	agent.Property = uuid.Nil
	agent.Must(401, "POST", "/api/v1/bridge/heartbeat", map[string]any{"agentVersion": "0.1.0"}, "Authorization", "Bearer ocb_wrong")
	agent.Must(200, "POST", "/api/v1/bridge/heartbeat", map[string]any{"agentVersion": "0.1.0", "hardware": []map[string]any{{"type": "locker", "id": "L-01"}}},
		"Authorization", "Bearer "+str(a["token"]))
	list := pa.Must(200, "GET", "/api/v1/platform/bridge-agents", nil).Items()
	if len(list) != 1 || list[0]["online"] != true {
		t.Fatalf("agent should be online: %v", list)
	}
	pa.Must(200, "PATCH", "/api/v1/platform/bridge-agents/"+str(a["id"]), map[string]any{"status": "inactive"})
	agent.Must(401, "POST", "/api/v1/bridge/heartbeat", map[string]any{"agentVersion": "0.1.0"}, "Authorization", "Bearer "+str(a["token"]))
}

// EP-07: audit log filters, before/after detail, masking (FR-AUD-06) and
// audited export (FR-AUD-05).
func TestAuditLogs(t *testing.T) {
	sa := superAdmin(t, inst)
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	e := pa.Must(201, "POST", "/api/v1/platform/employees", map[string]any{"employeeNo": "AUD-1", "fullName": "Audit Person", "email": "audit.person@club.test"}).JSON()
	pa.Must(200, "PATCH", "/api/v1/platform/employees/"+str(e["id"]), map[string]any{"jobTitle": "Marshal"})
	logs := sa.Must(200, "GET", "/api/v1/audit/logs?filter[entityId]="+str(e["id"]), nil).Items()
	if len(logs) != 2 {
		t.Fatalf("expected create + update, got %d", len(logs))
	}
	upd := logs[0]
	if upd["action"] != "update" || !strings.Contains(str(upd["changed"]), "jobTitle") || upd["ip"] == nil {
		t.Fatalf("update entry: %v", upd)
	}
	detail := sa.Must(200, "GET", "/api/v1/audit/logs/"+str(upd["id"]), nil).JSON()
	if detail["after"].(map[string]any)["email"] != "audit.person@club.test" || detail["masked"] != false {
		t.Fatalf("super admin must see unmasked data: %v", detail["after"])
	}
	// Property admin lacks audit.log.view_sensitive → personal data masked.
	pd := pa.Must(200, "GET", "/api/v1/audit/logs/"+str(upd["id"]), nil).JSON()
	if pd["after"].(map[string]any)["email"] == "audit.person@club.test" || pd["masked"] != true {
		t.Fatalf("personal data not masked: %v", pd["after"])
	}
	// Security events are recorded.
	sec := sa.Must(200, "GET", "/api/v1/audit/logs?filter[category]=security&filter[action]=login_failed,login_succeeded,mfa_verified", nil).Items()
	if len(sec) == 0 {
		t.Fatal("no security events")
	}
	// Filters by time and module, cursor pagination.
	page := sa.Must(200, "GET", "/api/v1/audit/logs?limit=5&filter[module]=platform&from="+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), nil).JSON()
	if page["nextCursor"] == nil {
		t.Fatal("expected a next page")
	}
	sa.Must(200, "GET", "/api/v1/audit/logs?limit=5&cursor="+str(page["nextCursor"]), nil)
	sa.Must(400, "GET", "/api/v1/audit/logs?filter[password]=x", nil)
	// Export (CSV) is itself audited.
	csv := sa.Must(200, "POST", "/api/v1/audit/exports", map[string]any{"filters": map[string]string{"entityType": "platform.employee"}})
	if !strings.HasPrefix(string(csv.Body), "occurredAt,") {
		t.Fatalf("csv: %s", csv.Body[:50])
	}
	ex := sa.Must(200, "GET", "/api/v1/audit/logs?filter[action]=export&filter[module]=audit", nil).Items()
	if len(ex) == 0 {
		t.Fatal("audit export not audited")
	}
	pa.Must(403, "POST", "/api/v1/audit/exports", map[string]any{})
}

// EP-10: report registry, replica routing, async export with notification,
// Executive Overview.
func TestReporting(t *testing.T) {
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	reports := fin.Must(200, "GET", "/api/v1/reporting/reports", nil).Items()
	codes := map[string]bool{}
	for _, r := range reports {
		codes[str(r["code"])] = true
	}
	if !codes["platform.user_access"] || !codes["platform.venue_directory"] || !codes["billing.golf_revenue"] || codes["membership.active_members"] {
		t.Fatalf("finance report set wrong: %v", codes)
	}
	ua := fin.Must(200, "GET", "/api/v1/reporting/reports/platform.user_access", nil).JSON()
	if len(ua["rows"].([]any)) == 0 {
		t.Fatal("user access report empty")
	}
	// Property scope: MDR users are not visible to a MAIN finance manager.
	for _, r := range ua["rows"].([]any) {
		if r.(map[string]any)["email"] == "property.admin2@demo.oneclub.id" {
			t.Fatal("report leaks another property's access")
		}
	}
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	gm.Must(403, "GET", "/api/v1/reporting/reports/platform.user_access", nil)
	ex := fin.Must(202, "POST", "/api/v1/reporting/exports", map[string]any{"reportCode": "platform.user_access", "format": "xlsx"}).JSON()
	var url string
	waitFor(t, 30*time.Second, "export completed", func() bool {
		for _, e := range fin.Must(200, "GET", "/api/v1/reporting/exports", nil).Items() {
			if e["id"] == ex["id"] && e["status"] == "completed" {
				url = str(e["fileUrl"])
				return true
			}
		}
		return false
	})
	file := fin.Must(200, "GET", strings.TrimPrefix(url, ""), nil)
	if string(file.Body[:2]) != "PK" {
		t.Fatal("xlsx export file invalid")
	}
	gm.Must(404, "GET", url, nil) // private file
	var ready int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications n JOIN platform.users u ON u.id = n.user_id
		WHERE u.email = 'finance@demo.oneclub.id' AND n.event_code = 'report.export_ready'`, nil, &ready)
	if ready == 0 {
		t.Fatal("export-ready notification missing")
	}
	ov := gm.Must(200, "GET", "/api/v1/reporting/dashboards/executive-overview", nil).JSON()
	if len(ov["widgets"].([]any)) < 4 {
		t.Fatalf("overview: %v", ov)
	}
	login(t, inst, "starter@demo.oneclub.id", demoPassword).Must(403, "GET", "/api/v1/reporting/dashboards/executive-overview", nil)
}

// Business Rules / Club Policies framework (PRD §6.1).
func TestRulesFramework(t *testing.T) {
	sa := superAdmin(t, inst)
	v1 := sa.Must(201, "POST", "/api/v1/platform/business-rules", map[string]any{"category": "Booking", "code": "booking.max_advance_days",
		"name": "Maximum advance booking", "value": 14}).JSON()
	sa.Must(422, "POST", "/api/v1/platform/business-rules", map[string]any{"category": "Booking", "code": "booking.max_advance_days",
		"name": "Maximum advance booking", "value": 21, "effectiveFrom": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)})
	sa.Must(201, "POST", "/api/v1/platform/business-rules", map[string]any{"category": "Booking", "code": "booking.max_advance_days",
		"name": "Maximum advance booking", "value": 21, "effectiveFrom": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)})
	list := sa.Must(200, "GET", "/api/v1/platform/business-rules?filter[code]=booking.max_advance_days", nil).Items()
	if len(list) != 2 || list[1]["inEffect"] != true {
		t.Fatalf("rules: %v", list)
	}
	val, ver, ok, err := rulesResolve(inst, "business_rule", "booking.max_advance_days")
	if err != nil || !ok || ver != 1 || string(val) != "14" {
		t.Fatalf("resolve: %s v%d %v %v", val, ver, ok, err)
	}
	sa.Must(200, "POST", "/api/v1/platform/business-rules/"+str(v1["id"])+":set-status", map[string]any{"status": "inactive", "reason": "replaced"})
	sa.Must(422, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Space Policies", "code": "x.y", "name": "x", "value": true})
	cp := sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Cancellation Policies", "code": "golf.cancel_hours",
		"name": "Golf cancellation window", "value": map[string]any{"hours": 24}, "propertyId": inst.Main}).JSON()
	sa.Must(200, "GET", "/api/v1/platform/club-policies?history=true", nil)
	sa.Must(200, "POST", "/api/v1/platform/club-policies/"+str(cp["id"])+":set-status", map[string]any{"status": "active"})
}

// FR-TEC-06: the EXCLUDE constraint prevents double booking.
func TestAllocationExcludeConstraint(t *testing.T) {
	ctx := dbtx.System(context.Background())
	res := id.New()
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO reservation.resources (id, property_id, code, name) VALUES ($1, $2, 'TEE-1', 'Tee 1')`, res, inst.Main)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(24 * time.Hour).Truncate(time.Minute)
	err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := reservation.Confirm(ctx, tx, inst.Main, res, id.New(), start, start.Add(10*time.Minute)); err != nil {
			return err
		}
		if _, err := reservation.Hold(ctx, tx, inst.Main, res, id.New(), start.Add(5*time.Minute), start.Add(15*time.Minute), time.Minute); err != reservation.ErrSlotTaken {
			t.Fatalf("overlap must be rejected, got %v", err)
		}
		// Adjacent slot is fine ([) ranges).
		if _, err := reservation.Hold(ctx, tx, inst.Main, res, id.New(), start.Add(10*time.Minute), start.Add(20*time.Minute), time.Millisecond); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	n, err := reservation.ReleaseExpired(context.Background(), inst.DB)
	if err != nil || n != 1 {
		t.Fatalf("release expired: %d %v", n, err)
	}
}

// FR-SH-05 / Technical Doc §6.4: the offline queue is processed in order and
// idempotently; re-sending an item returns the stored result.
func TestOfflineSync(t *testing.T) {
	c := anon(t, inst)
	c.Must(200, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": inst.Device, "email": "cashier@demo.oneclub.id", "pin": "246810"})
	a, b, bad := id.New(), id.New(), id.New()
	items := []map[string]any{
		{"id": a, "action": "ops.shift_note", "payload": map[string]any{"text": "Opened register while offline"}},
		{"id": b, "action": "ops.shift_note", "payload": map[string]any{"text": ""}},
		{"id": bad, "action": "pos.unknown", "payload": map[string]any{}},
	}
	res := c.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()["results"].([]any)
	if res[0].(map[string]any)["status"] != "accepted" || res[1].(map[string]any)["status"] != "rejected" || res[2].(map[string]any)["status"] != "rejected" {
		t.Fatalf("results: %v", res)
	}
	again := c.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items[:1]}).JSON()["results"].([]any)
	if again[0].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("resend: %v", again)
	}
	login(t, inst, "gm@demo.oneclub.id", demoPassword).Must(403, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{}})
}
