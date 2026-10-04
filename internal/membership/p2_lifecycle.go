package membership

// PRD P2 EP-04 Complete Membership Lifecycle on P1's membership model.
// Lifecycle actions work on the principal membership; family members and
// corporate nominees (memberships with principal_id) follow its status.
// Changes that need a decision (pause, postpone, reactivation, upgrade /
// downgrade, nominee replacement) are requests through the approval engine;
// fees of the lifecycle are folio charges of their own (source "membership").

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/rules"
)

// Lifecycle domain events (PRD P2 §11).
const (
	EventPaused       = "membership.paused"
	EventResumed      = "membership.resumed"
	EventSuspended    = "membership.suspended"
	EventReactivated  = "membership.reactivated"
	EventUpgraded     = "membership.upgraded"
	EventCancelled    = "membership.cancelled"
	EventAnnualFeeDue = "membership.annual_fee_due"
)

// Approval document types of the lifecycle.
var (
	PauseType        = provision.DocumentType{Code: "membership_pause", Module: "membership", Name: "Membership Pause", Attributes: []provision.DocumentAttribute{{Key: "months", Label: "Months", Type: "number"}}}
	PostponeType     = provision.DocumentType{Code: "membership_postpone", Module: "membership", Name: "Annual Fee Postponement"}
	ReactivationType = provision.DocumentType{Code: "membership_reactivation", Module: "membership", Name: "Membership Reactivation"}
	ChangeType       = provision.DocumentType{Code: "membership_change", Module: "membership", Name: "Membership Upgrade / Downgrade", Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Fee difference", Type: "number"}}}
	NomineeType      = provision.DocumentType{Code: "membership_nominee_change", Module: "membership", Name: "Corporate Nominee Replacement"}
)

// LifecycleDocumentTypes are the P2 document types decided by Decision.
func LifecycleDocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{PauseType, PostponeType, ReactivationType, ChangeType, NomineeType}
}

// LifecyclePolicy is the Member Policies extension (FR-POL-P2-03).
type LifecyclePolicy struct {
	PauseMinMembershipMonths int  `json:"pauseMinMembershipMonths"`
	PauseMaxMonths           int  `json:"pauseMaxMonths"`
	PausesPerYear            int  `json:"pausesPerYear"`
	SuspendAfterGrace        bool `json:"suspendAfterGrace"`
	ExpireAfterSuspendedDays int  `json:"expireAfterSuspendedDays"`
	AutoLiftOnPayment        bool `json:"autoLiftOnPayment"`
	ChildAgeNoticeDays       int  `json:"childAgeNoticeDays"`
}

var defaultLifecycle = LifecyclePolicy{PauseMinMembershipMonths: 6, PauseMaxMonths: 6, PausesPerYear: 1, SuspendAfterGrace: true,
	ExpireAfterSuspendedDays: 90, AutoLiftOnPayment: true, ChildAgeNoticeDays: 30}

func (m *Module) policy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (LifecyclePolicy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, "membership.lifecycle", property, defaultLifecycle)
}

// lc is the principal membership a lifecycle action works on.
type lc struct {
	ID, PropertyID, MemberID, TypeID uuid.UUID
	CustomerID                       *uuid.UUID
	MemberNo, MemberName             string
	Role, Status                     string
	StartsOn                         time.Time
	EndsOn, NextFeeDue, PausedUntil  *time.Time
}

// loadLC loads and locks a principal membership for a lifecycle change.
func loadLC(ctx context.Context, tx pgx.Tx, mid uuid.UUID) (lc, error) { return queryLC(ctx, tx, mid, " FOR UPDATE OF ms") }

func queryLC(ctx context.Context, q dbtx.Querier, mid uuid.UUID, lock string) (lc, error) {
	var l lc
	err := q.QueryRow(ctx, `SELECT ms.id, ms.property_id, ms.member_id, ms.type_id, mb.customer_id, mb.code, mb.name, ms.role, ms.status,
		ms.starts_on, ms.ends_on, ms.next_fee_due, ms.paused_until
		FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id WHERE ms.id = $1`+lock, mid).
		Scan(&l.ID, &l.PropertyID, &l.MemberID, &l.TypeID, &l.CustomerID, &l.MemberNo, &l.MemberName, &l.Role, &l.Status, &l.StartsOn, &l.EndsOn,
			&l.NextFeeDue, &l.PausedUntil)
	if dbtx.IsNoRows(err) {
		return l, errs.NotFound("membership")
	}
	if err != nil {
		return l, err
	}
	if l.Role != "principal" {
		return l, errs.Conflict("principal_only", "lifecycle changes apply to the principal membership; family members and nominees follow it")
	}
	return l, nil
}

// GetMembership returns the API view of a membership.
func GetMembership(ctx context.Context, q dbtx.Querier, mid uuid.UUID) (Membership, error) {
	list, err := listMemberships(ctx, q, "ms.id = $1", mid)
	if err != nil {
		return Membership{}, err
	}
	if len(list) == 0 {
		return Membership{}, errs.NotFound("membership")
	}
	return list[0], nil
}

// MembershipDetail is P1's membership with its P2 lifecycle state
// (status paused / suspended / cancelled, annual fee due date, pause,
// suspension and cancellation).
type MembershipDetail struct {
	Membership
	TypeCode         string     `json:"typeCode"`
	NextFeeDue       *string    `json:"nextFeeDue"`
	PausedFrom       *string    `json:"pausedFrom"`
	PausedUntil      *string    `json:"pausedUntil"`
	SuspensionKind   *string    `json:"suspensionKind" enum:"arrears,discipline"`
	SuspensionReason *string    `json:"suspensionReason"`
	SuspendedAt      *time.Time `json:"suspendedAt"`
	CancelledAt      *time.Time `json:"cancelledAt"`
	CancelReason     *string    `json:"cancelReason"`
}

// GetMembershipDetail returns a membership with its lifecycle state.
func GetMembershipDetail(ctx context.Context, q dbtx.Querier, mid uuid.UUID) (MembershipDetail, error) {
	ms, err := GetMembership(ctx, q, mid)
	if err != nil {
		return MembershipDetail{}, err
	}
	d := MembershipDetail{Membership: ms}
	err = q.QueryRow(ctx, `SELECT t.code, ms.next_fee_due::text, ms.paused_from::text, ms.paused_until::text, ms.suspension_kind, ms.suspension_reason,
		ms.suspended_at, ms.cancelled_at, ms.cancel_reason FROM membership.memberships ms JOIN membership.types t ON t.id = ms.type_id WHERE ms.id = $1`, mid).
		Scan(&d.TypeCode, &d.NextFeeDue, &d.PausedFrom, &d.PausedUntil, &d.SuspensionKind, &d.SuspensionReason, &d.SuspendedAt, &d.CancelledAt, &d.CancelReason)
	return d, err
}

// transition changes the status of the principal and its family / nominee
// memberships; set adds SQL assignments for the principal ($3 …).
func (m *Module) transition(ctx context.Context, tx pgx.Tx, l lc, to, event, reason string, details map[string]any, set string, args ...any) (Membership, error) {
	before, err := GetMembership(ctx, tx, l.ID)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET status = $2`+set+` WHERE id = $1`, append([]any{l.ID, to}, args...)...); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET status = $2 WHERE principal_id = $1 AND status NOT IN ('inactive', 'cancelled')`, l.ID, to); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.members mb SET status = $2 FROM membership.memberships ms
		WHERE ms.member_id = mb.id AND (ms.id = $1 OR ms.principal_id = $1) AND ms.status = $2`, l.ID, to); err != nil {
		return before, err
	}
	if details == nil {
		details = map[string]any{}
	}
	if reason != "" {
		details["reason"] = reason
	}
	if err := history(ctx, tx, l.PropertyID, l.MemberID, &l.ID, event, l.Status, to, details); err != nil {
		return before, err
	}
	after, err := GetMembership(ctx, tx, l.ID)
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionStatusChange, EntityType: "membership.membership",
		EntityID: l.ID.String(), EntityLabel: l.MemberNo, PropertyID: &l.PropertyID, Reason: reason, Before: before, After: after}); err != nil {
		return after, err
	}
	return after, m.publish(ctx, tx, "membership."+event, l, details)
}

func (m *Module) publish(ctx context.Context, tx pgx.Tx, event string, l lc, extra map[string]any) error {
	payload := map[string]any{"membershipId": l.ID, "memberId": l.MemberID, "memberNo": l.MemberNo, "customerId": l.CustomerID}
	for k, v := range extra {
		payload[k] = v
	}
	_, err := m.Events.Publish(ctx, tx, event, "membership.membership", &l.ID, &l.PropertyID, payload)
	return err
}

// ── lifecycle fees (FR-MBL-04/08/12/13) ───────────────────────────────────

// Fee is a lifecycle fee (Membership Fee History).
type Fee struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	MembershipID  uuid.UUID  `json:"membershipId" db:"membership_id"`
	MemberNo      string     `json:"memberNo" db:"member_no"`
	MemberName    string     `json:"memberName" db:"member_name"`
	FeeType       string     `json:"feeType" db:"fee_type" enum:"annual,upgrade,reactivation,card_replacement,nominee_change"`
	PeriodStart   *time.Time `json:"periodStart" db:"period_start"`
	PeriodEnd     *time.Time `json:"periodEnd" db:"period_end"`
	DueDate       time.Time  `json:"dueDate" db:"due_date"`
	Amount        string     `json:"amount" db:"amount"`
	Status        string     `json:"status" db:"status" enum:"scheduled,due,paid,waived,postponed,cancelled"`
	FolioID       *uuid.UUID `json:"folioId" db:"folio_id"`
	PostponedFrom *time.Time `json:"postponedFrom" db:"postponed_from"`
	PaidAt        *time.Time `json:"paidAt" db:"paid_at"`
}

const feeSelect = `SELECT f.id, f.membership_id, mb.code AS member_no, mb.name AS member_name, f.fee_type, f.period_start, f.period_end, f.due_date,
	f.amount::text AS amount, f.status, f.folio_id, f.postponed_from, f.paid_at
	FROM membership.fees f JOIN membership.memberships ms ON ms.id = f.membership_id JOIN membership.members mb ON mb.id = ms.member_id`

var feeComponent = map[string]string{"annual": "membership_annual_fee", "upgrade": "membership_fee", "reactivation": "reactivation_fee",
	"card_replacement": "card_replacement_fee", "nominee_change": "nominee_fee"}

var feeLabel = map[string]string{"annual": "Annual Fee", "upgrade": "Upgrade Fee", "reactivation": "Reactivation Fee",
	"card_replacement": "Card Replacement Fee", "nominee_change": "Nominee Replacement Fee"}

// chargeFee records a lifecycle fee and posts it to a folio of its own;
// a fee of zero is recorded as paid.
func (m *Module) chargeFee(ctx context.Context, tx pgx.Tx, l lc, feeType string, amount decimal.Decimal, due time.Time, ps, pe *time.Time) (uuid.UUID, error) {
	fid := id.New()
	status := "scheduled"
	if !due.After(today(ctx, tx, l.PropertyID)) {
		status = "due"
	}
	if !amount.IsPositive() {
		status = "paid"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO membership.fees (id, property_id, membership_id, fee_type, period_start, period_end, due_date, amount, status,
		paid_at, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,CASE WHEN $9 = 'paid' THEN now() END,$10,$10)`,
		fid, l.PropertyID, l.ID, feeType, ps, pe, due, amount.String(), status, id.Ptr(actor(ctx))); err != nil {
		return fid, err
	}
	if amount.IsPositive() {
		if err := m.postFee(ctx, tx, l, fid, feeType, amount); err != nil {
			return fid, err
		}
	}
	return fid, history(ctx, tx, l.PropertyID, l.MemberID, &l.ID, "fee_charged", l.Status, l.Status,
		map[string]any{"feeId": fid, "feeType": feeType, "amount": amount.String(), "dueDate": due.Format("2006-01-02")})
}

// postFee posts a fee to a folio of its own (source "membership", settled
// through OnFeePaid).
func (m *Module) postFee(ctx context.Context, tx pgx.Tx, l lc, fid uuid.UUID, feeType string, amount decimal.Decimal) error {
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: l.PropertyID, CustomerID: l.CustomerID, HolderName: l.MemberName, SourceType: "membership", SourceID: &fid, SourceRef: l.MemberNo}, BusinessLine: billing.LineMembership})
	if err != nil {
		return err
	}
	if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: f.ID, Description: feeLabel[feeType] + " — " + l.MemberNo, Net: amount, ReferenceType: "membership.fee", ReferenceID: &fid}, BusinessLine: billing.LineMembership, RevenueComponent: feeComponent[feeType]}); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE membership.fees SET folio_id = $2 WHERE id = $1`, fid, f.ID)
	return err
}

// OnFeePaid marks a lifecycle fee paid once its folio is settled; a paid
// annual fee moves the next due date a year on and lifts an arrears
// suspension (subscriber of billing.payment_settled).
func (m *Module) OnFeePaid(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p billing.SettledPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.SourceType == nil || *p.SourceType != "membership" || p.SourceID == nil || p.FolioID == nil || e.PropertyID == nil {
		return nil
	}
	fid, err := uuid.Parse(*p.SourceID)
	if err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	ctx = withProperty(dbtx.System(ctx), *e.PropertyID)
	sum, err := billing.FolioSummary(ctx, tx, *p.FolioID)
	if err != nil {
		return err
	}
	if decimal.RequireFromString(sum.Balance).IsPositive() {
		return nil
	}
	var mid uuid.UUID
	var feeType string
	var periodEnd *time.Time
	err = tx.QueryRow(ctx, `UPDATE membership.fees SET status = 'paid', paid_at = now() WHERE id = $1 AND status NOT IN ('paid', 'waived', 'cancelled')
		RETURNING membership_id, fee_type, period_end`, fid).Scan(&mid, &feeType, &periodEnd)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if feeType == "annual" {
		// the annual fee is recognised month by month over its period (FR-BIL-P2-06)
		var amt string
		if err := tx.QueryRow(ctx, `SELECT amount::text FROM membership.fees WHERE id = $1`, fid).Scan(&amt); err != nil {
			return err
		}
		if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: *e.PropertyID, LiabilityType: "annual_fee", RefType: "membership.fee",
			RefID: fid, EntryType: "deferral", Amount: decimal.RequireFromString(amt), RevenueComponent: "membership_annual_fee",
			Description: "Annual fee paid", IdempotencyKey: "annual-fee-paid-" + fid.String()}); err != nil {
			return err
		}
	}
	return m.afterFeeSettled(ctx, tx, mid, feeType, periodEnd)
}

func (m *Module) afterFeeSettled(ctx context.Context, tx pgx.Tx, mid uuid.UUID, feeType string, periodEnd *time.Time) error {
	if feeType != "annual" {
		return nil
	}
	if periodEnd != nil {
		if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET next_fee_due = $2::date + 1 WHERE id = $1`, mid, *periodEnd); err != nil {
			return err
		}
	}
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return err
	}
	pol, _, err := m.policy(ctx, tx, l.PropertyID)
	if err != nil {
		return err
	}
	var kind *string
	if err := tx.QueryRow(ctx, `SELECT suspension_kind FROM membership.memberships WHERE id = $1`, mid).Scan(&kind); err != nil {
		return err
	}
	if l.Status != "suspended" || kind == nil || *kind != "arrears" || !pol.AutoLiftOnPayment {
		return nil
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM membership.fees WHERE membership_id = $1 AND fee_type = 'annual' AND status = 'due'`, mid).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return nil
	}
	_, err = m.lift(ctx, tx, l, "annual fee paid")
	return err
}

// WaiveFee waives an open lifecycle fee (charge voided, reason recorded).
func (m *Module) WaiveFee(ctx context.Context, tx pgx.Tx, fid uuid.UUID, reason string) (Fee, error) {
	if strings.TrimSpace(reason) == "" {
		return Fee{}, handle.Invalid("reason", "required", "a reason is required")
	}
	var mid uuid.UUID
	var folio *uuid.UUID
	var feeType string
	var periodEnd *time.Time
	if err := tx.QueryRow(ctx, `UPDATE membership.fees SET status = 'waived' WHERE id = $1 AND status IN ('scheduled', 'due', 'postponed')
		RETURNING membership_id, folio_id, fee_type, period_end`, fid).Scan(&mid, &folio, &feeType, &periodEnd); err != nil {
		if dbtx.IsNoRows(err) {
			return Fee{}, errs.Conflict("fee_not_open", "only an open fee can be waived")
		}
		return Fee{}, err
	}
	if folio != nil {
		if _, err := m.Billing.VoidSourceCharges(ctx, tx, "membership.fee", fid, "waived: "+reason); err != nil {
			return Fee{}, err
		}
	}
	f, err := handle.Get[Fee](tx.Query(ctx, feeSelect+` WHERE f.id = $1`, fid))
	if err != nil {
		return f, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "waive", EntityType: "membership.fee", EntityID: fid.String(),
		EntityLabel: f.MemberNo + " " + f.FeeType, PropertyID: id.Ptr(propOf(ctx)), Reason: reason, After: f}); err != nil {
		return f, err
	}
	return f, m.afterFeeSettled(ctx, tx, mid, feeType, periodEnd)
}

// ── requests through approval ─────────────────────────────────────────────

// LifecycleRequest is a lifecycle change waiting for approval.
type LifecycleRequest struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	MembershipID uuid.UUID      `json:"membershipId" db:"membership_id"`
	MemberNo     string         `json:"memberNo" db:"member_no"`
	RequestType  string         `json:"requestType" db:"request_type" enum:"pause,postpone,reactivate,upgrade,downgrade,cancel,nominee_change"`
	Payload      map[string]any `json:"payload" db:"payload"`
	Reason       *string        `json:"reason" db:"reason"`
	Status       string         `json:"status" db:"status" enum:"pending,approved,rejected,cancelled,applied"`
	Channel      string         `json:"channel" db:"channel"`
	ApprovalID   *uuid.UUID     `json:"approvalId" db:"approval_id"`
	CreatedAt    time.Time      `json:"createdAt" db:"created_at"`
}

const requestSelect = `SELECT r.id, r.membership_id, mb.code AS member_no, r.request_type, r.payload, r.reason, r.status, r.channel, r.approval_id, r.created_at
	FROM membership.requests r JOIN membership.memberships ms ON ms.id = r.membership_id JOIN membership.members mb ON mb.id = ms.member_id`

func (m *Module) submitRequest(ctx context.Context, tx pgx.Tx, l lc, typ string, dt provision.DocumentType, payload map[string]any, reason, channel, title string,
	attrs map[string]any) (LifecycleRequest, error) {
	if channel == "" {
		channel = "back_office"
	}
	rid := id.New()
	raw, _ := json.Marshal(payload)
	if _, err := tx.Exec(ctx, `INSERT INTO membership.requests (id, property_id, membership_id, request_type, payload, reason, channel, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, rid, l.PropertyID, l.ID, typ, raw, nullStr(reason), channel, id.Ptr(actor(ctx))); err != nil {
		return LifecycleRequest{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionCreate, EntityType: "membership.request",
		EntityID: rid.String(), EntityLabel: typ + " " + l.MemberNo, PropertyID: &l.PropertyID, After: payload, Reason: reason}); err != nil {
		return LifecycleRequest{}, err
	}
	aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: dt.Code, DocumentID: rid, DocumentRef: l.MemberNo,
		Title: title, PropertyID: l.PropertyID, Attributes: attrs})
	if err != nil {
		return LifecycleRequest{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.requests SET approval_id = $2 WHERE id = $1`, rid, aid); err != nil {
		return LifecycleRequest{}, err
	}
	return handle.Get[LifecycleRequest](tx.Query(ctx, requestSelect+` WHERE r.id = $1`, rid))
}

// Decision applies approval outcomes of the lifecycle document types.
func (m *Module) Decision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	if st == "" {
		return nil
	}
	var r LifecycleRequest
	var err error
	r, err = handle.Get[LifecycleRequest](tx.Query(ctx, requestSelect+` WHERE r.id = $1 AND r.status = 'pending'`, d.DocumentID))
	if errs.Is(err, errs.KindNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.requests SET status = $2, decided_at = now() WHERE id = $1`, r.ID, st); err != nil {
		return err
	}
	if d.Status != approval.StatusApproved {
		return nil
	}
	l, err := loadLC(ctx, tx, r.MembershipID)
	if err != nil {
		return err
	}
	reason := ""
	if r.Reason != nil {
		reason = *r.Reason
	}
	switch r.RequestType {
	case "pause":
		err = m.applyPause(ctx, tx, l, dateOf(r.Payload["from"]), dateOf(r.Payload["until"]), reason)
	case "postpone":
		err = m.applyPostpone(ctx, tx, l, dateOf(r.Payload["dueDate"]), reason)
	case "reactivate":
		err = m.applyReactivation(ctx, tx, l, reason)
	case "upgrade", "downgrade":
		to, perr := uuid.Parse(fmt.Sprint(r.Payload["toTypeId"]))
		if perr != nil {
			return perr
		}
		err = m.applyChange(ctx, tx, l, to, reason)
	case "nominee_change":
		err = m.applyNomineeChange(ctx, tx, l, r.Payload, reason)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE membership.requests SET status = 'applied' WHERE id = $1`, r.ID)
	return err
}

func dateOf(v any) time.Time {
	s, _ := v.(string)
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// ── pause / resume (FR-MBL-07) ────────────────────────────────────────────

// RequestPause asks for a pause; eligibility per the Member Policies.
func (m *Module) RequestPause(ctx context.Context, tx pgx.Tx, mid uuid.UUID, from, until time.Time, reason, channel string) (LifecycleRequest, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return LifecycleRequest{}, err
	}
	if l.Status != "active" {
		return LifecycleRequest{}, errs.Conflict("invalid_status", "only an Active membership can be paused")
	}
	if strings.TrimSpace(reason) == "" {
		return LifecycleRequest{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if !until.After(from) {
		return LifecycleRequest{}, handle.Invalid("until", "invalid_period", "the pause must end after it starts")
	}
	pol, _, err := m.policy(ctx, tx, l.PropertyID)
	if err != nil {
		return LifecycleRequest{}, err
	}
	if l.StartsOn.AddDate(0, pol.PauseMinMembershipMonths, 0).After(from) {
		return LifecycleRequest{}, errs.Validation("pause_not_eligible", fmt.Sprintf("a pause is possible after %d months of membership", pol.PauseMinMembershipMonths))
	}
	if from.AddDate(0, pol.PauseMaxMonths, 1).Before(until) {
		return LifecycleRequest{}, errs.Validation("pause_too_long", fmt.Sprintf("a pause may last at most %d months", pol.PauseMaxMonths))
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM membership.history WHERE membership_id = $1 AND event = 'paused'
		AND occurred_at > now() - interval '1 year'`, mid).Scan(&recent); err != nil {
		return LifecycleRequest{}, err
	}
	if recent >= pol.PausesPerYear {
		return LifecycleRequest{}, errs.Validation("pause_limit", fmt.Sprintf("at most %d pause(s) per year", pol.PausesPerYear))
	}
	months := int(until.Sub(from).Hours()/24/30 + 0.5)
	return m.submitRequest(ctx, tx, l, "pause", PauseType, map[string]any{"from": from.Format("2006-01-02"), "until": until.Format("2006-01-02")},
		reason, channel, "Pause "+l.MemberNo+" "+from.Format("2 Jan")+" – "+until.Format("2 Jan 2006"), map[string]any{"months": months})
}

// applyPause pauses (now or when the pause starts) and extends the validity
// by the pause (Validity Adjustment).
func (m *Module) applyPause(ctx context.Context, tx pgx.Tx, l lc, from, until time.Time, reason string) error {
	days := int(until.Sub(from).Hours()/24) + 1
	if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET paused_from = $2, paused_until = $3, ends_on = ends_on + $4::int,
		next_fee_due = next_fee_due + $4::int WHERE id = $1 OR principal_id = $1`, l.ID, from, until, days); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.cards c SET valid_until = ms.ends_on FROM membership.memberships ms
		WHERE c.membership_id = ms.id AND (ms.id = $1 OR ms.principal_id = $1) AND c.status = 'active'`, l.ID); err != nil {
		return err
	}
	details := map[string]any{"from": from.Format("2006-01-02"), "until": until.Format("2006-01-02"), "validityExtendedDays": days}
	if from.After(today(ctx, tx, l.PropertyID)) {
		return history(ctx, tx, l.PropertyID, l.MemberID, &l.ID, "pause_scheduled", l.Status, l.Status, details)
	}
	_, err := m.transition(ctx, tx, l, "paused", "paused", reason, details, "")
	return err
}

// Resume ends a pause early; unused pause days are taken back from the validity.
func (m *Module) Resume(ctx context.Context, tx pgx.Tx, mid uuid.UUID, reason string) (Membership, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return Membership{}, err
	}
	if l.Status != "paused" {
		return Membership{}, errs.Conflict("invalid_status", "the membership is not paused")
	}
	return m.resume(ctx, tx, l, reason)
}

func (m *Module) resume(ctx context.Context, tx pgx.Tx, l lc, reason string) (Membership, error) {
	t := today(ctx, tx, l.PropertyID)
	unused := 0
	if l.PausedUntil != nil && l.PausedUntil.After(t) {
		unused = int(l.PausedUntil.Sub(t).Hours() / 24)
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET paused_from = NULL, paused_until = NULL, ends_on = ends_on - $2::int,
		next_fee_due = next_fee_due - $2::int WHERE id = $1 OR principal_id = $1`, l.ID, unused); err != nil {
		return Membership{}, err
	}
	return m.transition(ctx, tx, l, "active", "resumed", reason, map[string]any{"unusedPauseDays": unused}, "")
}

// ── postpone & reactivation (FR-MBL-08) ───────────────────────────────────

// RequestPostpone asks to move the next annual fee due date.
func (m *Module) RequestPostpone(ctx context.Context, tx pgx.Tx, mid uuid.UUID, newDue time.Time, reason string) (LifecycleRequest, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return LifecycleRequest{}, err
	}
	if l.NextFeeDue == nil {
		return LifecycleRequest{}, errs.Conflict("no_fee_due", "this membership has no annual fee due date")
	}
	if !newDue.After(*l.NextFeeDue) {
		return LifecycleRequest{}, handle.Invalid("dueDate", "invalid_date", "the new due date must be later than "+l.NextFeeDue.Format("2006-01-02"))
	}
	return m.submitRequest(ctx, tx, l, "postpone", PostponeType, map[string]any{"dueDate": newDue.Format("2006-01-02")}, reason, "",
		"Postpone annual fee "+l.MemberNo+" to "+newDue.Format("2 Jan 2006"), nil)
}

func (m *Module) applyPostpone(ctx context.Context, tx pgx.Tx, l lc, newDue time.Time, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET next_fee_due = $2 WHERE id = $1`, l.ID, newDue); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.fees SET postponed_from = due_date, due_date = $2,
		status = CASE WHEN status = 'due' THEN 'postponed' ELSE status END
		WHERE membership_id = $1 AND fee_type = 'annual' AND status IN ('scheduled', 'due')`, l.ID, newDue); err != nil {
		return err
	}
	return history(ctx, tx, l.PropertyID, l.MemberID, &l.ID, "postponed", l.Status, l.Status,
		map[string]any{"from": l.NextFeeDue, "dueDate": newDue.Format("2006-01-02"), "reason": reason})
}

// RequestReactivation asks to reactivate an Expired or Suspended membership.
func (m *Module) RequestReactivation(ctx context.Context, tx pgx.Tx, mid uuid.UUID, reason string) (LifecycleRequest, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return LifecycleRequest{}, err
	}
	if l.Status != "expired" && l.Status != "suspended" {
		return LifecycleRequest{}, errs.Conflict("invalid_status", "only Expired or Suspended memberships can be reactivated")
	}
	return m.submitRequest(ctx, tx, l, "reactivate", ReactivationType, map[string]any{}, reason, "", "Reactivate "+l.MemberNo, nil)
}

type lifecycleType struct {
	ProgramID                                       uuid.UUID
	Name                                            string
	Rank, GraceDays                                 int
	AnnualFee, CardFee, ReactivationFee, NomineeFee decimal.Decimal
	PeriodUnit                                      string
	PeriodCount                                     int
}

func loadLifecycleType(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (lifecycleType, error) {
	var t lifecycleType
	var annual, card, react, nominee string
	err := q.QueryRow(ctx, `SELECT t.program_id, t.name, t.rank, t.grace_days, t.annual_fee::text, t.card_replacement_fee::text, t.reactivation_fee::text,
		t.nominee_change_fee::text, coalesce(p.period_unit, 'year'), coalesce(p.period_count, 1)
		FROM membership.types t LEFT JOIN LATERAL (SELECT period_unit, period_count FROM membership.packages WHERE type_id = t.id AND status = 'active'
		ORDER BY created_at LIMIT 1) p ON true WHERE t.id = $1`, tid).
		Scan(&t.ProgramID, &t.Name, &t.Rank, &t.GraceDays, &annual, &card, &react, &nominee, &t.PeriodUnit, &t.PeriodCount)
	if dbtx.IsNoRows(err) {
		return t, errs.Validation("invalid_type", "membership type not found", errs.Field("typeId", "not_found", "membership type not found"))
	}
	t.AnnualFee, t.CardFee, t.ReactivationFee, t.NomineeFee = decimal.RequireFromString(annual), decimal.RequireFromString(card),
		decimal.RequireFromString(react), decimal.RequireFromString(nominee)
	return t, err
}

func (m *Module) applyReactivation(ctx context.Context, tx pgx.Tx, l lc, reason string) error {
	t, err := loadLifecycleType(ctx, tx, l.TypeID)
	if err != nil {
		return err
	}
	day := today(ctx, tx, l.PropertyID)
	if _, err := m.chargeFee(ctx, tx, l, "reactivation", t.ReactivationFee, day, nil, nil); err != nil {
		return err
	}
	set := `, suspension_kind = NULL, suspension_reason = NULL, suspended_at = NULL`
	var args []any
	if l.Status == "expired" {
		end := PeriodEnd(day, t.PeriodUnit, t.PeriodCount)
		set += `, starts_on = $3, ends_on = $4`
		args = append(args, day, end)
		if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET ends_on = $2 WHERE principal_id = $1`, l.ID, end); err != nil {
			return err
		}
	}
	if _, err := m.transition(ctx, tx, l, "active", "reactivated", reason, nil, set, args...); err != nil {
		return err
	}
	return m.restoreAccount(ctx, tx, l)
}

// ── suspension (FR-MBL-10) ────────────────────────────────────────────────

// Suspend suspends a membership: manual (discipline) or automatic (arrears).
// Member rate and member charge are blocked while suspended.
func (m *Module) Suspend(ctx context.Context, tx pgx.Tx, mid uuid.UUID, kind, reason string) (Membership, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return Membership{}, err
	}
	return m.suspend(ctx, tx, l, kind, reason)
}

func (m *Module) suspend(ctx context.Context, tx pgx.Tx, l lc, kind, reason string) (Membership, error) {
	if l.Status != "active" && l.Status != "paused" {
		return Membership{}, errs.Conflict("invalid_status", "only Active or Paused memberships can be suspended")
	}
	if strings.TrimSpace(reason) == "" {
		return Membership{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if kind == "" {
		kind = "discipline"
	}
	after, err := m.transition(ctx, tx, l, "suspended", "suspended", reason, map[string]any{"kind": kind},
		`, suspension_kind = $3, suspension_reason = $4, suspended_at = now()`, kind, reason)
	if err != nil {
		return after, err
	}
	if l.CustomerID != nil {
		if err := m.Billing.SetAccountStatus(ctx, tx, l.PropertyID, *l.CustomerID, "suspended", "membership "+l.MemberNo+" suspended"); err != nil {
			return after, err
		}
		if c, err := crm.GetCustomer(ctx, tx, *l.CustomerID); err == nil {
			if err := m.notifyCustomer(ctx, tx, c, "membership.suspended", map[string]any{"name": c.Name, "memberNo": l.MemberNo, "reason": reason}); err != nil {
				return after, err
			}
		}
	}
	return after, nil
}

// LiftSuspension lifts a suspension (recorded with the reason).
func (m *Module) LiftSuspension(ctx context.Context, tx pgx.Tx, mid uuid.UUID, reason string) (Membership, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return Membership{}, err
	}
	if l.Status != "suspended" {
		return Membership{}, errs.Conflict("invalid_status", "the membership is not suspended")
	}
	return m.lift(ctx, tx, l, reason)
}

func (m *Module) lift(ctx context.Context, tx pgx.Tx, l lc, reason string) (Membership, error) {
	after, err := m.transition(ctx, tx, l, "active", "suspension_lifted", reason, nil, `, suspension_kind = NULL, suspension_reason = NULL, suspended_at = NULL`)
	if err != nil {
		return after, err
	}
	return after, m.restoreAccount(ctx, tx, l)
}

func (m *Module) restoreAccount(ctx context.Context, tx pgx.Tx, l lc) error {
	if l.CustomerID == nil {
		return nil
	}
	var other int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id
		WHERE mb.customer_id = $1 AND ms.status = 'suspended'`, *l.CustomerID).Scan(&other); err != nil {
		return err
	}
	if other > 0 {
		return nil
	}
	return m.Billing.SetAccountStatus(ctx, tx, l.PropertyID, *l.CustomerID, "active", "membership "+l.MemberNo+" active again")
}

// ── upgrade / downgrade (FR-MBL-06) ───────────────────────────────────────

// ChangePreview is the prorated fee difference of an upgrade / downgrade.
type ChangePreview struct {
	FromType      string `json:"fromType"`
	ToType        string `json:"toType"`
	Direction     string `json:"direction" enum:"upgrade,downgrade"`
	RemainingDays int    `json:"remainingDays"`
	PeriodDays    int    `json:"periodDays"`
	Difference    string `json:"difference" doc:"Prorated annual fee difference (positive = charged)"`
}

// PreviewChange computes the prorated difference:
// (new annual fee − current annual fee) × remaining days ÷ period days.
func (m *Module) PreviewChange(ctx context.Context, tx pgx.Tx, mid, toType uuid.UUID) (ChangePreview, error) {
	l, err := queryLC(ctx, tx, mid, "") // read-only preview
	if err != nil {
		return ChangePreview{}, err
	}
	return m.previewChange(ctx, tx, l, toType)
}

func (m *Module) previewChange(ctx context.Context, tx pgx.Tx, l lc, toType uuid.UUID) (ChangePreview, error) {
	from, err := loadLifecycleType(ctx, tx, l.TypeID)
	if err != nil {
		return ChangePreview{}, err
	}
	to, err := loadLifecycleType(ctx, tx, toType)
	if err != nil {
		return ChangePreview{}, err
	}
	if to.ProgramID != from.ProgramID {
		return ChangePreview{}, errs.Validation("other_program", "upgrade / downgrade must stay within the same program")
	}
	if toType == l.TypeID {
		return ChangePreview{}, errs.Validation("same_type", "the membership already has this type")
	}
	if l.EndsOn == nil {
		return ChangePreview{}, errs.Conflict("no_period", "the membership has no end date")
	}
	t := today(ctx, tx, l.PropertyID)
	remaining := max(int(l.EndsOn.Sub(t).Hours()/24)+1, 0)
	period := max(int(l.EndsOn.Sub(l.StartsOn).Hours()/24)+1, 1)
	diff := to.AnnualFee.Sub(from.AnnualFee).Mul(decimal.NewFromInt(int64(remaining))).Div(decimal.NewFromInt(int64(period))).Round(0)
	dir := "upgrade"
	if to.Rank < from.Rank || (to.Rank == from.Rank && diff.IsNegative()) {
		dir = "downgrade"
	}
	return ChangePreview{FromType: from.Name, ToType: to.Name, Direction: dir, RemainingDays: remaining, PeriodDays: period, Difference: diff.String()}, nil
}

// RequestChange asks for an upgrade or downgrade.
func (m *Module) RequestChange(ctx context.Context, tx pgx.Tx, mid, toType uuid.UUID, direction, reason string) (LifecycleRequest, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return LifecycleRequest{}, err
	}
	if l.Status != "active" {
		return LifecycleRequest{}, errs.Conflict("invalid_status", "only an Active membership can change type")
	}
	p, err := m.previewChange(ctx, tx, l, toType)
	if err != nil {
		return LifecycleRequest{}, err
	}
	if direction != "" && p.Direction != direction {
		return LifecycleRequest{}, errs.Validation("wrong_direction", "this change is a "+p.Direction)
	}
	return m.submitRequest(ctx, tx, l, p.Direction, ChangeType, map[string]any{"toTypeId": toType.String(), "difference": p.Difference,
		"fromType": p.FromType, "toType": p.ToType}, reason, "", strings.ToUpper(p.Direction[:1])+p.Direction[1:]+" "+l.MemberNo+": "+p.FromType+" → "+p.ToType,
		map[string]any{"amount": p.Difference})
}

func (m *Module) applyChange(ctx context.Context, tx pgx.Tx, l lc, toType uuid.UUID, reason string) error {
	p, err := m.previewChange(ctx, tx, l, toType)
	if err != nil {
		return err
	}
	if d := decimal.RequireFromString(p.Difference); d.IsPositive() {
		if _, err := m.chargeFee(ctx, tx, l, "upgrade", d, today(ctx, tx, l.PropertyID), nil, l.EndsOn); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET type_id = $2, package_id = (SELECT id FROM membership.packages WHERE type_id = $2
		AND status = 'active' ORDER BY created_at LIMIT 1) WHERE id = $1 OR principal_id = $1`, l.ID, toType); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.members SET membership_type = $2 WHERE id = $1`, l.MemberID, p.ToType); err != nil {
		return err
	}
	ev := "upgraded"
	if p.Direction == "downgrade" {
		ev = "downgraded"
	}
	details := map[string]any{"direction": p.Direction, "fromType": p.FromType, "toType": p.ToType, "difference": p.Difference, "reason": reason}
	if err := history(ctx, tx, l.PropertyID, l.MemberID, &l.ID, ev, l.Status, l.Status, details); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionUpdate, EntityType: "membership.membership",
		EntityID: l.ID.String(), EntityLabel: l.MemberNo, PropertyID: &l.PropertyID, Reason: reason, After: details}); err != nil {
		return err
	}
	return m.publish(ctx, tx, EventUpgraded, l, details)
}

// ── cancellation (FR-MBL-11) ──────────────────────────────────────────────

// Cancel cancels a membership on request of the member or the club: open
// fees are cancelled and the cards of the covered members are blocked.
func (m *Module) Cancel(ctx context.Context, tx pgx.Tx, mid uuid.UUID, reason string) (Membership, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return Membership{}, err
	}
	if l.Status == "cancelled" {
		return Membership{}, errs.Conflict("invalid_status", "already cancelled")
	}
	if strings.TrimSpace(reason) == "" {
		return Membership{}, handle.Invalid("reason", "required", "a reason is required")
	}
	after, err := m.transition(ctx, tx, l, "cancelled", "cancelled", reason, nil, `, cancelled_at = now(), cancel_reason = $3`, reason)
	if err != nil {
		return after, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.fees SET status = 'cancelled' WHERE membership_id = $1 AND status IN ('scheduled', 'postponed')`, mid); err != nil {
		return after, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.cards c SET status = 'blocked', blocked_at = now(), block_reason = 'membership cancelled'
		FROM membership.memberships ms WHERE c.membership_id = ms.id AND (ms.id = $1 OR ms.principal_id = $1) AND c.status = 'active'`, mid); err != nil {
		return after, err
	}
	return after, m.restoreAccount(ctx, tx, l)
}

// ── card replacement (FR-MBL-12) ──────────────────────────────────────────

// ReplaceCard blocks a card and issues a new one; the Card Replacement Fee
// of the type is charged unless waived.
func (m *Module) ReplaceCard(ctx context.Context, tx pgx.Tx, cardID uuid.UUID, reason string, waiveFee bool) (Card, error) {
	if strings.TrimSpace(reason) == "" {
		return Card{}, handle.Invalid("reason", "required", "a reason is required")
	}
	var property, memberID uuid.UUID
	var membershipID *uuid.UUID
	var cardType, number, status string
	if err := tx.QueryRow(ctx, `SELECT property_id, member_id, membership_id, card_type, card_number, status FROM membership.cards WHERE id = $1 FOR UPDATE`, cardID).
		Scan(&property, &memberID, &membershipID, &cardType, &number, &status); err != nil {
		if dbtx.IsNoRows(err) {
			return Card{}, errs.NotFound("member card")
		}
		return Card{}, err
	}
	if status != "active" {
		return Card{}, errs.Conflict("card_not_active", "only an active card can be replaced")
	}
	var validUntil *time.Time
	if membershipID != nil {
		_ = tx.QueryRow(ctx, `SELECT ends_on FROM membership.memberships WHERE id = $1`, *membershipID).Scan(&validUntil)
	}
	newID, err := IssueCard(ctx, tx, property, memberID, membershipID, cardType, "", "", validUntil)
	if err != nil {
		return Card{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.cards SET status = 'replaced', blocked_at = now(), block_reason = $2, replaced_by = $3 WHERE id = $1`,
		cardID, reason, newID); err != nil {
		return Card{}, err
	}
	if !waiveFee && membershipID != nil {
		principal := *membershipID
		_ = tx.QueryRow(ctx, `SELECT coalesce(principal_id, id) FROM membership.memberships WHERE id = $1`, *membershipID).Scan(&principal)
		if l, err := loadLC(ctx, tx, principal); err == nil {
			t, err := loadLifecycleType(ctx, tx, l.TypeID)
			if err != nil {
				return Card{}, err
			}
			if _, err := m.chargeFee(ctx, tx, l, "card_replacement", t.CardFee, today(ctx, tx, property), nil, nil); err != nil {
				return Card{}, err
			}
		}
	}
	if err := history(ctx, tx, property, memberID, membershipID, "card_replaced", "", "", map[string]any{"oldCard": number, "reason": reason}); err != nil {
		return Card{}, err
	}
	c, err := getCard(ctx, tx, newID)
	if err != nil {
		return c, err
	}
	return c, audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "replace", EntityType: "membership.card", EntityID: cardID.String(),
		EntityLabel: number + " → " + c.CardNumber, PropertyID: &property, Reason: reason, After: c})
}

// ── family members & corporate nominees (FR-MBL-13) ───────────────────────

// MemberInput adds a person to a membership.
type MemberInput struct {
	CustomerID   uuid.UUID `json:"customerId"`
	Relationship string    `json:"relationship,omitempty" enum:"spouse,child,parent,sibling,other" doc:"Family member; empty for a corporate nominee"`
}

// AddMember covers a family member or a corporate nominee (eligibility and
// the limits of the type re-checked).
func (m *Module) AddMember(ctx context.Context, tx pgx.Tx, mid uuid.UUID, in MemberInput) (Membership, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return Membership{}, err
	}
	if l.Status != "active" {
		return Membership{}, errs.Conflict("invalid_status", "members are added to an Active membership")
	}
	t, err := loadType(ctx, tx, l.TypeID)
	if err != nil {
		return Membership{}, err
	}
	role := "family"
	if t.Category == "corporate" {
		role = "nominee"
	} else if !slices.Contains([]string{"spouse", "child", "parent", "sibling", "other"}, in.Relationship) {
		return Membership{}, handle.Invalid("relationship", "invalid", "spouse, child, parent, sibling or other")
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM membership.memberships WHERE principal_id = $1 AND role = $2 AND status = 'active'`, mid, role).Scan(&count); err != nil {
		return Membership{}, err
	}
	limit := t.MaxFamily
	if role == "nominee" {
		limit = t.MaxNominees
	}
	if count >= limit {
		return Membership{}, errs.Conflict("limit_reached", fmt.Sprintf("the type covers at most %d %s member(s)", limit, role))
	}
	if role == "family" && l.CustomerID != nil {
		applicant, err := crm.GetCustomerProfile(ctx, tx, *l.CustomerID)
		if err != nil {
			return Membership{}, err
		}
		dep, err := crm.GetCustomerProfile(ctx, tx, in.CustomerID)
		if err != nil {
			return Membership{}, err
		}
		deps := []Dependent{{CustomerID: in.CustomerID, Relationship: in.Relationship, Student: dep.Student}}
		res := Evaluate(t.Eligibility, applicant.Customer, deps, map[uuid.UUID]crm.Customer{in.CustomerID: dep.Customer}, applicant.Student, false, today(ctx, tx, l.PropertyID))
		if !res.Eligible {
			fields := []errs.FieldError{}
			for _, c := range res.Checks {
				if !c.Passed {
					fields = append(fields, errs.Field("customerId", c.Rule, c.Message))
				}
			}
			return Membership{}, errs.Validation("not_eligible", "the member does not meet the eligibility rules", fields...)
		}
		if err := crm.EnsureRelationship(ctx, tx, l.PropertyID, *l.CustomerID, in.CustomerID, in.Relationship); err != nil {
			return Membership{}, err
		}
	}
	return m.coverMember(ctx, tx, l, in.CustomerID, role, in.Relationship)
}

func (m *Module) coverMember(ctx context.Context, tx pgx.Tx, l lc, customer uuid.UUID, role, relationship string) (Membership, error) {
	c, err := crm.GetCustomer(ctx, tx, customer)
	if err != nil {
		return Membership{}, err
	}
	var typeName string
	if err := tx.QueryRow(ctx, `SELECT name FROM membership.types WHERE id = $1`, l.TypeID).Scan(&typeName); err != nil {
		return Membership{}, err
	}
	day := today(ctx, tx, l.PropertyID)
	memberID, _, _, err := ensureMember(ctx, tx, l.PropertyID, c, typeName, day)
	if err != nil {
		return Membership{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.memberships WHERE principal_id = $1 AND member_id = $2 AND status = 'active')`,
		l.ID, memberID).Scan(&exists); err != nil {
		return Membership{}, err
	}
	if exists || memberID == l.MemberID {
		return Membership{}, errs.Conflict("already_member", "this person is already covered")
	}
	msID := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO membership.memberships (id, property_id, member_id, type_id, package_id, principal_id, role, relationship,
		corporate_account_id, starts_on, ends_on, status, activated_at, created_by, updated_by)
		SELECT $1, property_id, $2, type_id, package_id, id, $3, $4, corporate_account_id, $5::date, ends_on, 'active', now(), $6, $6
		FROM membership.memberships WHERE id = $7`, msID, memberID, role, nullStr(relationship), day.Format("2006-01-02"), id.Ptr(actor(ctx)), l.ID); err != nil {
		return Membership{}, err
	}
	if _, err := IssueCard(ctx, tx, l.PropertyID, memberID, &msID, "digital", "", "", l.EndsOn); err != nil {
		return Membership{}, err
	}
	if err := history(ctx, tx, l.PropertyID, l.MemberID, &l.ID, "member_added", l.Status, l.Status,
		map[string]any{"customerId": customer, "role": role, "relationship": relationship}); err != nil {
		return Membership{}, err
	}
	after, err := GetMembership(ctx, tx, msID)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionCreate, EntityType: "membership.membership",
		EntityID: msID.String(), EntityLabel: l.MemberNo + " · " + c.Name, PropertyID: &l.PropertyID, After: after})
}

// RemoveMember ends the coverage of a family member or nominee.
func (m *Module) RemoveMember(ctx context.Context, tx pgx.Tx, mid, coveredID uuid.UUID, reason string) (Membership, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return Membership{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Membership{}, handle.Invalid("reason", "required", "a reason is required")
	}
	var memberID uuid.UUID
	var role string
	if err := tx.QueryRow(ctx, `UPDATE membership.memberships SET status = 'inactive', ends_on = least(coalesce(ends_on, current_date), current_date)
		WHERE id = $1 AND principal_id = $2 AND status <> 'inactive' RETURNING member_id, role`, coveredID, mid).Scan(&memberID, &role); err != nil {
		if dbtx.IsNoRows(err) {
			return Membership{}, errs.NotFound("covered member")
		}
		return Membership{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.cards SET status = 'blocked', blocked_at = now(), block_reason = $2 WHERE membership_id = $1 AND status = 'active'`,
		coveredID, "removed: "+reason); err != nil {
		return Membership{}, err
	}
	if err := history(ctx, tx, l.PropertyID, memberID, &coveredID, "member_removed", "active", "inactive", map[string]any{"role": role, "reason": reason}); err != nil {
		return Membership{}, err
	}
	after, err := GetMembership(ctx, tx, coveredID)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionStatusChange, EntityType: "membership.membership",
		EntityID: coveredID.String(), EntityLabel: l.MemberNo, PropertyID: &l.PropertyID, Reason: reason, After: after})
}

// NomineeChangeInput replaces a corporate nominee.
type NomineeChangeInput struct {
	CoveredID  uuid.UUID `json:"coveredMembershipId" doc:"Membership of the nominee who leaves"`
	CustomerID uuid.UUID `json:"customerId" doc:"New nominee"`
	Reason     string    `json:"reason"`
}

// RequestNomineeChange asks to replace a corporate nominee (fee per policy).
func (m *Module) RequestNomineeChange(ctx context.Context, tx pgx.Tx, mid uuid.UUID, in NomineeChangeInput) (LifecycleRequest, error) {
	l, err := loadLC(ctx, tx, mid)
	if err != nil {
		return LifecycleRequest{}, err
	}
	var role string
	if err := tx.QueryRow(ctx, `SELECT role FROM membership.memberships WHERE id = $1 AND principal_id = $2 AND status = 'active'`, in.CoveredID, mid).Scan(&role); err != nil || role != "nominee" {
		return LifecycleRequest{}, errs.NotFound("nominee")
	}
	if _, err := crm.GetCustomer(ctx, tx, in.CustomerID); err != nil {
		return LifecycleRequest{}, err
	}
	return m.submitRequest(ctx, tx, l, "nominee_change", NomineeType, map[string]any{"coveredMembershipId": in.CoveredID.String(),
		"customerId": in.CustomerID.String()}, in.Reason, "", "Replace nominee of "+l.MemberNo, nil)
}

func (m *Module) applyNomineeChange(ctx context.Context, tx pgx.Tx, l lc, payload map[string]any, reason string) error {
	covered, err := uuid.Parse(fmt.Sprint(payload["coveredMembershipId"]))
	if err != nil {
		return err
	}
	cust, err := uuid.Parse(fmt.Sprint(payload["customerId"]))
	if err != nil {
		return err
	}
	if _, err := m.RemoveMember(ctx, tx, l.ID, covered, "nominee replaced: "+reason); err != nil {
		return err
	}
	if _, err := m.coverMember(ctx, tx, l, cust, "nominee", ""); err != nil {
		return err
	}
	t, err := loadLifecycleType(ctx, tx, l.TypeID)
	if err != nil {
		return err
	}
	_, err = m.chargeFee(ctx, tx, l, "nominee_change", t.NomineeFee, today(ctx, tx, l.PropertyID), nil, nil)
	return err
}

func getCard(ctx context.Context, q dbtx.Querier, cid uuid.UUID) (Card, error) {
	cards, err := listCards(ctx, q, "c.id = $1", cid)
	if err != nil {
		return Card{}, err
	}
	if len(cards) == 0 {
		return Card{}, errs.NotFound("member card")
	}
	return cards[0], nil
}

func decimalOf(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}
