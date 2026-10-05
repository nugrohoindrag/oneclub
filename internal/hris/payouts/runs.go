package payouts

// Payout runs of the non-employee workforce (EP-13 caddies, semi-monthly on
// the 15th and at month end including non-cash tips; EP-14 partner
// instructors, monthly — PRD P5 §16 #4): Draft → :calculate → Calculated →
// :approve (approval engine; no workflow ⇒ approved at once) → Approved,
// posted to the ledger (hris.payout_posted: caddy fee / instructor fee
// liability against PPh 21 non-employee, BPJS BPU, deductions and the
// partner payout payable) → :mark-paid → Paid (hris.payout_paid: payable
// against bank / cash; the settlements and fees turn Paid in golf / sport
// club). A run claims the approved sources it pays, keeps the policy
// versions it used (FR-POL-P5-07) and produces the bank file and the
// statements (FR-CDY-03/04, FR-INS-HR-04).

import (
	"context"
	"encoding/csv"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/rules"
)

// Run statuses (PRD P5 §7.6: Calculated, Approved, Posted, Paid).
var RunStatuses = []string{"draft", "calculated", "pending_approval", "approved", "paid", "cancelled"}

// PayoutRun is a payout run with its totals.
type PayoutRun struct {
	ID                uuid.UUID         `json:"id" db:"id"`
	PropertyID        uuid.UUID         `json:"propertyId" db:"property_id"`
	Number            string            `json:"number" db:"number"`
	Kind              string            `json:"kind" db:"kind" enum:"caddy,instructor"`
	PeriodStart       time.Time         `json:"periodStart" db:"period_start"`
	PeriodEnd         time.Time         `json:"periodEnd" db:"period_end"`
	PayDate           time.Time         `json:"payDate" db:"pay_date"`
	Status            string            `json:"status" db:"status" enum:"draft,calculated,pending_approval,approved,paid,cancelled"`
	PolicyVersions    []rules.PolicyRef `json:"policyVersions" db:"policy_versions" doc:"Policy versions used by the calculation (FR-POL-P5-07)"`
	TaxNote           *string           `json:"taxNote" db:"tax_note"`
	Partners          int               `json:"partners" db:"partners"`
	HeldPartners      int               `json:"heldPartners" db:"held_partners" doc:"Partners below the minimum net, left for the next run"`
	Gross             string            `json:"gross" db:"gross"`
	SourceDeductions  string            `json:"sourceDeductions" db:"source_deductions"`
	PPh21             string            `json:"pph21" db:"pph21"`
	BPU               string            `json:"bpu" db:"bpu"`
	OtherDeductions   string            `json:"otherDeductions" db:"other_deductions"`
	Net               string            `json:"net" db:"net"`
	BankAmount        string            `json:"bankAmount" db:"bank_amount"`
	CashAmount        string            `json:"cashAmount" db:"cash_amount"`
	ApprovalRequestID *uuid.UUID        `json:"approvalRequestId" db:"approval_request_id"`
	CalculatedAt      *time.Time        `json:"calculatedAt" db:"calculated_at"`
	ApprovedAt        *time.Time        `json:"approvedAt" db:"approved_at"`
	PostedAt          *time.Time        `json:"postedAt" db:"posted_at"`
	PaidAt            *time.Time        `json:"paidAt" db:"paid_at"`
	PaidOn            *time.Time        `json:"paidOn" db:"paid_on"`
	PaidReference     *string           `json:"paidReference" db:"paid_reference"`
	BankFileAt        *time.Time        `json:"bankFileAt" db:"bank_file_at"`
	CancelledAt       *time.Time        `json:"cancelledAt" db:"cancelled_at"`
	CancelReason      *string           `json:"cancelReason" db:"cancel_reason"`
	DecisionNote      *string           `json:"decisionNote" db:"decision_note"`
	Notes             *string           `json:"notes" db:"notes"`
	CreatedAt         time.Time         `json:"createdAt" db:"created_at"`
	UpdatedAt         time.Time         `json:"updatedAt" db:"updated_at"`
}

const runSelect = `SELECT id, property_id, number, kind, period_start, period_end, pay_date, status, policy_versions, tax_note, partners, held_partners,
	trim_scale(gross)::text AS gross, trim_scale(source_deductions)::text AS source_deductions, trim_scale(pph21)::text AS pph21,
	trim_scale(bpu)::text AS bpu, trim_scale(other_deductions)::text AS other_deductions, trim_scale(net)::text AS net,
	trim_scale(bank_amount)::text AS bank_amount, trim_scale(cash_amount)::text AS cash_amount, approval_request_id, calculated_at, approved_at,
	posted_at, paid_at, paid_on, paid_reference, bank_file_at, cancelled_at, cancel_reason, decision_note, notes, created_at, updated_at
	FROM hris.payout_runs`

// PayoutRunLine is one partner of a run (statement).
type PayoutRunLine struct {
	ID               uuid.UUID                     `json:"id" db:"id"`
	RunID            uuid.UUID                     `json:"runId" db:"run_id"`
	PartnerID        uuid.UUID                     `json:"partnerId" db:"partner_id"`
	PartnerCode      *string                       `json:"partnerCode" db:"partner_code"`
	PartnerName      string                        `json:"partnerName" db:"partner_name"`
	PaymentMethod    string                        `json:"paymentMethod" db:"payment_method" enum:"bank_transfer,cash"`
	BankName         *string                       `json:"bankName" db:"bank_name"`
	BankAccountNo    *string                       `json:"bankAccountNo" db:"bank_account_no" doc:"Masked without hris.partner_profile.view_sensitive"`
	BankAccountName  *string                       `json:"bankAccountName" db:"bank_account_name"`
	HasTaxID         bool                          `json:"hasTaxId" db:"has_tax_id" doc:"NPWP / NIK registered (else the non-NPWP surcharge)"`
	BPUEnrolled      bool                          `json:"bpuEnrolled" db:"bpu_enrolled"`
	MonthlyDue       bool                          `json:"monthlyDue" db:"monthly_due" doc:"BPU and monthly deductions taken in this run"`
	Units            int                           `json:"units" db:"units" doc:"Rounds (caddy) / sessions (instructor)"`
	Fee              string                        `json:"fee" db:"fee"`
	Tips             string                        `json:"tips" db:"tips"`
	Gross            string                        `json:"gross" db:"gross"`
	SourceDeductions string                        `json:"sourceDeductions" db:"source_deductions"`
	YTDTaxBase       string                        `json:"ytdTaxBase" db:"ytd_tax_base"`
	TaxBase          string                        `json:"taxBase" db:"tax_base"`
	PPh21Rate        string                        `json:"pph21Rate" db:"pph21_rate"`
	PPh21            string                        `json:"pph21" db:"pph21"`
	BPUJKK           string                        `json:"bpuJkk" db:"bpu_jkk"`
	BPUJKM           string                        `json:"bpuJkm" db:"bpu_jkm"`
	OtherDeductions  string                        `json:"otherDeductions" db:"other_deductions"`
	Deductions       []hris.PartnerDeductionAmount `json:"deductions" db:"deductions"`
	Net              string                        `json:"net" db:"net"`
	Sources          []hris.PayoutSourceRef        `json:"sources" db:"sources"`
	Status           string                        `json:"status" db:"status" enum:"payable,paid"`
}

const lineSelect = `SELECT id, run_id, partner_id, partner_code, partner_name, payment_method, bank_name, bank_account_no, bank_account_name, has_tax_id,
	bpu_enrolled, monthly_due, units, trim_scale(fee)::text AS fee, trim_scale(tips)::text AS tips, trim_scale(gross)::text AS gross,
	trim_scale(source_deductions)::text AS source_deductions, trim_scale(ytd_tax_base)::text AS ytd_tax_base, trim_scale(tax_base)::text AS tax_base,
	trim_scale(pph21_rate)::text AS pph21_rate, trim_scale(pph21)::text AS pph21, trim_scale(bpu_jkk)::text AS bpu_jkk,
	trim_scale(bpu_jkm)::text AS bpu_jkm, trim_scale(other_deductions)::text AS other_deductions, deductions, trim_scale(net)::text AS net, sources,
	status FROM hris.payout_lines`

// PayoutRunDetail is a run with its lines.
type PayoutRunDetail struct {
	PayoutRun
	Lines []PayoutRunLine `json:"lines"`
}

// PayoutRunInput creates a run.
type PayoutRunInput struct {
	Kind        string `json:"kind" enum:"caddy,instructor"`
	PeriodStart string `json:"periodStart,omitempty" doc:"Default: the last completed period of the schedule (Payroll Configuration partnerPayout)"`
	PeriodEnd   string `json:"periodEnd,omitempty"`
	PayDate     string `json:"payDate,omitempty" doc:"Default: the period end"`
	Notes       string `json:"notes,omitempty"`
}

// PayoutRunAction carries a reason / note.
type PayoutRunAction struct {
	Reason string `json:"reason,omitempty"`
}

// PayoutRunPayment confirms the payment of a run.
type PayoutRunPayment struct {
	PaidOn    string `json:"paidOn,omitempty" doc:"Default: today (club date)"`
	Reference string `json:"reference,omitempty" doc:"Bank batch / transfer reference"`
}

func (m *Module) registerRuns(reg *route.Registry) {
	tag := "HRIS Payouts"
	base := "/api/v1/hris/payout-runs"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Payout runs (caddies, partner instructors)", Permission: PermRunView,
		Response: PayoutRun{}, List: true, Query: []route.Param{{Name: "kind", Enum: hris.PayoutKinds}, {Name: "status", Enum: RunStatuses}},
		Handler: listRead(m.DB, m.listRunsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Create a payout run", Permission: PermRunCreate, Request: PayoutRunInput{},
		Response: PayoutRunDetail{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createRunHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Payout run with its lines", Permission: PermRunView,
		Response: PayoutRunDetail{}, Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PayoutRunDetail, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return PayoutRunDetail{}, err
			}
			return m.Run(ctx, tx, rid)
		})})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:calculate",
		Summary: "Calculate (or recalculate) the run: approved settlements / fees, PPh 21 non-employee, BPJS BPU, deductions", Permission: PermRunCalculate,
		Request: handle.Empty{}, Response: PayoutRunDetail{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.calculateRunHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:approve",
		Summary:    "Approve the run (submits it to the approval engine, or approves the current step); approval posts it to the ledger",
		Permission: PermRunApprove, Request: PayoutRunAction{}, Response: PayoutRunDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideRunHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reject", Summary: "Reject the run at the current approval step",
		Permission: PermRunApprove, Request: PayoutRunAction{}, Response: PayoutRunDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideRunHTTP(false))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:mark-paid",
		Summary: "Confirm the payment (bank file transferred, cash paid): statements Paid, ledger cleared", Permission: PermRunPay,
		Request: PayoutRunPayment{}, Response: PayoutRunDetail{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.markPaidHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel a run not yet approved (sources released)",
		Permission: PermRunCreate, Request: PayoutRunAction{}, Response: PayoutRunDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelRunHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}/bank-file", Summary: "Bulk transfer file of the run (CSV)",
		Permission: PermRunExport, RawContent: "text/csv", Query: []route.Param{{Name: "format", Enum: []string{"generic_csv", "bca_csv"}}},
		Handler: m.bankFileHTTP})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}/statements/{lineId}", Summary: "Statement of one partner (PDF)",
		Permission: PermRunView, RawContent: "application/pdf", Handler: m.statementHTTP(false)})
	m.registerSources(reg, tag)
}

// ── reads ─────────────────────────────────────────────────────────────────

func (m *Module) listRunsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayoutRun, error) {
	kind, status := filterParam(r, "kind"), filterParam(r, "status")
	if kind != "" && !oneOf(hris.PayoutKinds, kind) {
		return nil, enumErr("kind", hris.PayoutKinds)
	}
	if status != "" && !oneOf(RunStatuses, status) {
		return nil, enumErr("status", RunStatuses)
	}
	return handle.List[PayoutRun](tx.Query(ctx, runSelect+` WHERE property_id = $1 AND ($2 = '' OR kind = $2) AND ($3 = '' OR status = $3)
		ORDER BY period_end DESC, created_at DESC LIMIT 200`, handle.Property(ctx), kind, status))
}

func maskLine(ctx context.Context, property uuid.UUID, l *PayoutRunLine) {
	if l.BankAccountNo != nil && *l.BankAccountNo != "" && !can(ctx, PermProfileSensitive, property) {
		v := mask.Text(*l.BankAccountNo)
		l.BankAccountNo = &v
	}
	if l.Deductions == nil {
		l.Deductions = []hris.PartnerDeductionAmount{}
	}
	if l.Sources == nil {
		l.Sources = []hris.PayoutSourceRef{}
	}
}

// Run loads a run of the request's property with its lines (bank accounts
// masked without the sensitive permission).
func (m *Module) Run(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (PayoutRunDetail, error) {
	r, err := getOne[PayoutRun]("payout run")(tx.Query(ctx, runSelect+` WHERE id = $1 AND property_id = $2`, rid, handle.Property(ctx)))
	if err != nil {
		return PayoutRunDetail{}, err
	}
	lines, err := handle.List[PayoutRunLine](tx.Query(ctx, lineSelect+` WHERE run_id = $1 ORDER BY partner_name, partner_id`, rid))
	if err != nil {
		return PayoutRunDetail{}, err
	}
	for i := range lines {
		maskLine(ctx, r.PropertyID, &lines[i])
	}
	if r.PolicyVersions == nil {
		r.PolicyVersions = []rules.PolicyRef{}
	}
	return PayoutRunDetail{PayoutRun: r, Lines: lines}, nil
}

func lockRun(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (PayoutRun, error) {
	return getOne[PayoutRun]("payout run")(tx.Query(ctx, runSelect+` WHERE id = $1 AND property_id = $2 FOR UPDATE`, rid, handle.Property(ctx)))
}

func runAudit(r PayoutRun) map[string]any {
	return map[string]any{"status": r.Status, "partners": r.Partners, "gross": r.Gross, "pph21": r.PPh21, "bpu": r.BPU, "net": r.Net}
}

// ── create ────────────────────────────────────────────────────────────────

func (m *Module) createRunHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, in PayoutRunInput) (PayoutRunDetail, error) {
	property := handle.Property(ctx)
	if !oneOf(hris.PayoutKinds, in.Kind) {
		return PayoutRunDetail{}, enumErr("kind", hris.PayoutKinds)
	}
	now := today(ctx, tx, property)
	cfg, _, err := hris.LoadPayrollConfiguration(ctx, tx, property, hris.PolicyTime(now))
	if err != nil {
		return PayoutRunDetail{}, err
	}
	schedule := cfg.PartnerPayout.CaddySchedule
	if in.Kind == hris.PayoutKindInstructor {
		schedule = cfg.PartnerPayout.InstructorSchedule
	}
	from, to := hris.PreviousPartnerPeriod(schedule, now)
	if in.PeriodStart != "" || in.PeriodEnd != "" {
		f, err := parseDate("periodStart", in.PeriodStart)
		if err != nil {
			return PayoutRunDetail{}, err
		}
		t, err := parseDate("periodEnd", in.PeriodEnd)
		if err != nil {
			return PayoutRunDetail{}, err
		}
		if f == nil || t == nil {
			return PayoutRunDetail{}, handle.Invalid("periodEnd", "required", "give both periodStart and periodEnd")
		}
		if !hris.ValidPartnerPeriod(schedule, *f, *t) {
			return PayoutRunDetail{}, handle.Invalid("periodStart", "period_not_in_schedule",
				"the period must be one "+strings.ReplaceAll(schedule, "_", "-")+" payout period of the Payroll Configuration")
		}
		from, to = *f, *t
	}
	pay := to
	if p, err := parseDate("payDate", in.PayDate); err != nil {
		return PayoutRunDetail{}, err
	} else if p != nil {
		if p.Before(from) {
			return PayoutRunDetail{}, handle.Invalid("payDate", "before_period", "the pay date is before the period")
		}
		pay = *p
	}
	no, err := yearlyNumber(ctx, tx, property, "PYO", to.Year())
	if err != nil {
		return PayoutRunDetail{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.payout_runs (id, property_id, number, kind, period_start, period_end, pay_date, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, rid, property, no, in.Kind, from, to, pay, nullStr(in.Notes), actor(ctx)); err != nil {
		return PayoutRunDetail{}, err
	}
	out, err := m.Run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.payout_run", EntityID: rid.String(),
		EntityLabel: no + " · " + in.Kind + " " + ymd(from) + " – " + ymd(to), PropertyID: &property, After: runAudit(out.PayoutRun)})
}

// ── calculate ─────────────────────────────────────────────────────────────

type profileRow struct {
	ID              uuid.UUID
	PaymentMethod   string
	BankCode        *string
	BankName        *string
	BankAccountNo   *string
	BankAccountName *string
	HasTaxID        bool
	BPUEnrolled     bool
	BPUDeclared     decimal.NullDecimal
}

func partnerProfile(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind string, partner uuid.UUID) (*profileRow, error) {
	var p profileRow
	err := tx.QueryRow(ctx, `SELECT id, payment_method, bank_code, bank_name, bank_account_no, bank_account_name,
		coalesce(nullif(trim(npwp), ''), nullif(trim(nik), '')) IS NOT NULL, bpu_enrolled, bpu_declared_income
		FROM hris.partner_profiles WHERE property_id = $1 AND partner_kind = $2 AND partner_id = $3 AND archived_at IS NULL`, property, kind, partner).
		Scan(&p.ID, &p.PaymentMethod, &p.BankCode, &p.BankName, &p.BankAccountNo, &p.BankAccountName, &p.HasTaxID, &p.BPUEnrolled, &p.BPUDeclared)
	if err != nil {
		if dbtx.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// releaseRun puts the sources claimed by a run back to open and removes its
// lines (recalculation, cancellation).
func releaseRun(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE hris.payout_sources SET status = 'open', payout_run_id = NULL, payout_line_id = NULL
		WHERE payout_run_id = $1 AND status = 'claimed'`, rid); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM hris.payout_lines WHERE run_id = $1`, rid)
	return err
}

func (m *Module) calculateRunHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PayoutRunDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PayoutRunDetail{}, err
	}
	before, err := lockRun(ctx, tx, rid)
	if err != nil {
		return PayoutRunDetail{}, err
	}
	if before.Status != "draft" && before.Status != "calculated" {
		return PayoutRunDetail{}, errs.Conflict("run_not_open", "only a draft or calculated run can be calculated (the run is "+before.Status+")")
	}
	if err := m.CalculateRun(ctx, tx, before); err != nil {
		return PayoutRunDetail{}, err
	}
	out, err := m.Run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "calculate", EntityType: "hris.payout_run", EntityID: rid.String(),
		EntityLabel: out.Number, PropertyID: &out.PropertyID, Before: runAudit(before), After: runAudit(out.PayoutRun)})
}

// CalculateRun (re)builds the lines of a run from the open approved sources
// of its kind whose settlement period ends in the run period (a late
// approval of an earlier period is paid by a supplementary run of that
// period).
func (m *Module) CalculateRun(ctx context.Context, tx pgx.Tx, run PayoutRun) error {
	property := run.PropertyID
	if err := releaseRun(ctx, tx, run.ID); err != nil {
		return err
	}
	at := hris.PolicyTime(run.PeriodEnd)
	cfg, cfgRef, err := hris.LoadPayrollConfiguration(ctx, tx, property, at)
	if err != nil {
		return err
	}
	pol, polRef, err := hris.LoadPartnerPayoutPolicy(ctx, tx, property, at)
	if err != nil {
		return err
	}
	rule := pol.Rule(run.Kind)
	sources, err := listSources(ctx, tx, `WHERE property_id = $1 AND kind = $2 AND channel = 'payout' AND status = 'open'
		AND period_end BETWEEN $3 AND $4 ORDER BY partner_id, period_end, number FOR UPDATE`, property, run.Kind, run.PeriodStart, run.PeriodEnd)
	if err != nil {
		return err
	}
	srcType := SourceCaddySettlement
	if run.Kind == hris.PayoutKindInstructor {
		srcType = SourceInstructorFee
	}
	if m.Directory != nil && len(sources) > 0 {
		ids := make([]uuid.UUID, len(sources))
		for i, s := range sources {
			ids[i] = s.SourceID
		}
		paid, err := m.Directory.PaidElsewhere(ctx, tx, srcType, ids)
		if err != nil {
			return err
		}
		kept := sources[:0]
		for _, s := range sources {
			if paid[s.SourceID] {
				if _, err := tx.Exec(ctx, `UPDATE hris.payout_sources SET status = 'paid', consumed_at = now() WHERE id = $1`, s.ID); err != nil {
					return err
				}
				continue
			}
			kept = append(kept, s)
		}
		sources = kept
	}
	byPartner := map[uuid.UUID][]PayoutSource{}
	var order []uuid.UUID
	for _, s := range sources {
		if _, ok := byPartner[s.PartnerID]; !ok {
			order = append(order, s.PartnerID)
		}
		byPartner[s.PartnerID] = append(byPartner[s.PartnerID], s)
	}
	minNet := dec(rule.MinimumNet)
	monthStart := time.Date(run.PeriodEnd.Year(), run.PeriodEnd.Month(), 1, 0, 0, 0, 0, time.UTC)
	yearStart := time.Date(run.PeriodEnd.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	var tot struct{ gross, srcDed, pph, bpu, other, net, bank, cash decimal.Decimal }
	partners, held := 0, 0
	for _, pid := range order {
		srcs := byPartner[pid]
		var fee, tips, gross, srcDed decimal.Decimal
		units := 0
		refs := make([]hris.PayoutSourceRef, 0, len(srcs))
		for _, s := range srcs {
			fee, tips = fee.Add(dec(s.Fee)), tips.Add(dec(s.Tips))
			gross, srcDed = gross.Add(dec(s.Gross)), srcDed.Add(dec(s.Deductions))
			units += s.Units
			refs = append(refs, hris.PayoutSourceRef{SourceType: s.SourceType, SourceID: s.SourceID, Number: s.Number, Gross: s.Gross, Deductions: s.Deductions})
		}
		prof, err := partnerProfile(ctx, tx, property, run.Kind, pid)
		if err != nil {
			return err
		}
		var monthlyTaken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.payout_lines l JOIN hris.payout_runs r ON r.id = l.run_id
			WHERE l.partner_id = $1 AND l.monthly_due AND r.id <> $2 AND r.status <> 'cancelled' AND r.kind = $3
			  AND r.period_end BETWEEN $4 AND $5)`, pid, run.ID, run.Kind, monthStart, monthStart.AddDate(0, 1, -1)).Scan(&monthlyTaken); err != nil {
			return err
		}
		var ytd decimal.Decimal
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.tax_base), 0) FROM hris.payout_lines l JOIN hris.payout_runs r ON r.id = l.run_id
			WHERE l.partner_id = $1 AND r.id <> $2 AND r.status <> 'cancelled' AND r.kind = $3 AND r.period_end BETWEEN $4 AND $5`,
			pid, run.ID, run.Kind, yearStart, run.PeriodEnd).Scan(&ytd); err != nil {
			return err
		}
		in := hris.PartnerPayoutInput{Gross: gross, SourceDeductions: srcDed, YTDTaxBase: ytd, MonthlyDue: !monthlyTaken}
		method := rule.PaymentMethod
		var profileID *uuid.UUID
		var bankCode, bankName, acctNo, acctName *string
		if prof != nil {
			profileID = &prof.ID
			in.HasTaxID, in.BPUEnrolled = prof.HasTaxID, prof.BPUEnrolled
			if prof.BPUDeclared.Valid {
				in.DeclaredIncome = prof.BPUDeclared.Decimal
			}
			method, bankCode, bankName, acctNo, acctName = prof.PaymentMethod, prof.BankCode, prof.BankName, prof.BankAccountNo, prof.BankAccountName
		}
		if method == "bank_transfer" && deref(acctNo) == "" {
			method = "cash"
		}
		a := hris.CalculatePartnerPayout(cfg, rule, in)
		if minNet.IsPositive() && a.Net.LessThan(minNet) {
			held++
			continue
		}
		lid := id.New()
		name, code := srcs[0].PartnerName, ""
		if m.Directory != nil {
			if p, err := m.Directory.Partner(ctx, tx, property, run.Kind, pid); err != nil {
				return err
			} else if p != nil {
				name, code = p.Name, p.Code
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.payout_lines (id, run_id, property_id, partner_id, partner_code, partner_name, profile_id, payment_method,
			bank_code, bank_name, bank_account_no, bank_account_name, has_tax_id, bpu_enrolled, monthly_due, units, fee, tips, gross, source_deductions,
			ytd_tax_base, tax_base, pph21_rate, pph21, bpu_jkk, bpu_jkm, other_deductions, deductions, net, sources)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::numeric,$18::numeric,$19::numeric,$20::numeric,$21::numeric,$22::numeric,
			$23::numeric,$24::numeric,$25::numeric,$26::numeric,$27::numeric,$28,$29::numeric,$30)`,
			lid, run.ID, property, pid, nullStr(code), name, profileID, method, bankCode, bankName, acctNo, acctName, in.HasTaxID, in.BPUEnrolled,
			in.MonthlyDue, units, fee.String(), tips.String(), gross.String(), srcDed.String(), ytd.String(), a.TaxBase.String(), a.PPh21Rate.String(),
			a.PPh21.String(), a.BPUJKK.String(), a.BPUJKM.String(), a.OtherDeductions.String(), a.Deductions, a.Net.String(), refs); err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(srcs))
		for i, s := range srcs {
			ids[i] = s.ID
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.payout_sources SET status = 'claimed', payout_run_id = $2, payout_line_id = $3 WHERE id = ANY($1)`,
			ids, run.ID, lid); err != nil {
			return err
		}
		partners++
		tot.gross, tot.srcDed, tot.pph = tot.gross.Add(gross), tot.srcDed.Add(srcDed), tot.pph.Add(a.PPh21)
		tot.bpu, tot.other, tot.net = tot.bpu.Add(a.BPU()), tot.other.Add(a.OtherDeductions), tot.net.Add(a.Net)
		if method == "cash" {
			tot.cash = tot.cash.Add(a.Net)
		} else {
			tot.bank = tot.bank.Add(a.Net)
		}
	}
	refs := []rules.PolicyRef{cfgRef, polRef}
	_, err = tx.Exec(ctx, `UPDATE hris.payout_runs SET status = 'calculated', policy_versions = $2, tax_note = $3, partners = $4, held_partners = $5,
		gross = $6::numeric, source_deductions = $7::numeric, pph21 = $8::numeric, bpu = $9::numeric, other_deductions = $10::numeric, net = $11::numeric,
		bank_amount = $12::numeric, cash_amount = $13::numeric, calculated_at = now(), calculated_by = $14, decision_note = NULL, updated_by = $14
		WHERE id = $1`, run.ID, refs, nullStr(pol.TaxNote), partners, held, tot.gross.String(), tot.srcDed.String(), tot.pph.String(), tot.bpu.String(),
		tot.other.String(), tot.net.String(), tot.bank.String(), tot.cash.String(), actor(ctx))
	return err
}

// ── approve / reject ──────────────────────────────────────────────────────

func (m *Module) decideRunHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayoutRunAction) (PayoutRunDetail, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayoutRunAction) (PayoutRunDetail, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return PayoutRunDetail{}, err
		}
		run, err := lockRun(ctx, tx, rid)
		if err != nil {
			return PayoutRunDetail{}, err
		}
		switch {
		case run.Status == "pending_approval" && run.ApprovalRequestID != nil:
			if err := m.Approvals.Decide(ctx, tx, *run.ApprovalRequestID, approve, in.Reason); err != nil {
				return PayoutRunDetail{}, err
			}
		case run.Status == "calculated" && approve:
			if run.Partners == 0 {
				return PayoutRunDetail{}, errs.Conflict("empty_run", "the run pays nobody: calculate it with approved settlements / fees first")
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET status = 'pending_approval', decision_note = NULL, updated_by = $2 WHERE id = $1`,
				rid, actor(ctx)); err != nil {
				return PayoutRunDetail{}, err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit_approval", EntityType: "hris.payout_run", EntityID: rid.String(),
				EntityLabel: run.Number, PropertyID: &run.PropertyID, Reason: in.Reason, Before: map[string]any{"status": run.Status},
				After: map[string]any{"status": "pending_approval"}}); err != nil {
				return PayoutRunDetail{}, err
			}
			aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: PayoutRunDocumentType.Code, DocumentID: rid, DocumentRef: run.Number,
				Title:      "Payout run " + run.Number + " · " + run.Kind + " " + ymd(run.PeriodStart) + " – " + ymd(run.PeriodEnd) + " · net " + money(dec(run.Net)),
				PropertyID: run.PropertyID, Attributes: map[string]any{"kind": run.Kind, "amount": dec(run.Net).InexactFloat64(), "partners": float64(run.Partners)}})
			if err != nil {
				return PayoutRunDetail{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET approval_request_id = $2 WHERE id = $1`, rid, aid); err != nil {
				return PayoutRunDetail{}, err
			}
		default:
			return PayoutRunDetail{}, errs.Conflict("run_not_submittable", "the run is "+run.Status)
		}
		return m.Run(ctx, tx, rid)
	}
}

// RunDecision applies the approval decision (approval engine hook): an
// approved run is posted to the ledger (hris.payout_posted) and the
// partners see their statement; a rejected run returns to Calculated.
func (m *Module) RunDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	run, err := getOne[PayoutRun]("payout run")(tx.Query(ctx, runSelect+` WHERE id = $1 FOR UPDATE`, d.DocumentID))
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil
		}
		return err
	}
	if run.Status != "pending_approval" {
		return nil
	}
	next := "calculated"
	if d.Status == approval.StatusApproved {
		next = "approved"
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET status = $2, decision_note = $3,
		approved_at = CASE WHEN $2 = 'approved' THEN now() END, posted_at = CASE WHEN $2 = 'approved' THEN now() END WHERE id = $1`,
		run.ID, next, nullStr(d.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.payout_run", EntityID: run.ID.String(),
		EntityLabel: run.Number, PropertyID: &run.PropertyID, Reason: d.Reason, Before: map[string]any{"status": run.Status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	period := ymd(run.PeriodStart) + " – " + ymd(run.PeriodEnd)
	creator := []uuid.UUID{}
	var cb *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT created_by FROM hris.payout_runs WHERE id = $1`, run.ID).Scan(&cb); err == nil && cb != nil {
		creator = append(creator, *cb)
	}
	decision := map[string]string{"approved": "approved", "calculated": "rejected"}[next]
	if d.Status == approval.StatusCancelled {
		decision = "withdrawn"
	}
	if err := m.notifyUsers(ctx, tx, run.PropertyID, creator, "hris.payout_run_decided", "/hris/"+kindPath(run.Kind)+"/runs/"+run.ID.String(),
		map[string]any{"number": run.Number, "kind": run.Kind, "period": period, "amount": money(dec(run.Net)), "partners": run.Partners,
			"decision": decision, "reason": d.Reason}); err != nil {
		return err
	}
	if next != "approved" {
		return nil
	}
	lines, err := handle.List[PayoutRunLine](tx.Query(ctx, lineSelect+` WHERE run_id = $1 ORDER BY partner_name, partner_id`, run.ID))
	if err != nil {
		return err
	}
	ev := hris.PayoutPosted{RunID: run.ID, Number: run.Number, PropertyID: run.PropertyID, Kind: run.Kind, PeriodStart: ymd(run.PeriodStart),
		PeriodEnd: ymd(run.PeriodEnd), PayDate: ymd(run.PayDate), Gross: run.Gross, SourceDeductions: run.SourceDeductions, PPh21: run.PPh21, BPU: run.BPU,
		OtherDeductions: run.OtherDeductions, Net: run.Net, Lines: make([]hris.PayoutPostedLine, 0, len(lines))}
	for _, l := range lines {
		ev.Lines = append(ev.Lines, hris.PayoutPostedLine{LineID: l.ID, PartnerID: l.PartnerID, PartnerName: l.PartnerName, Gross: l.Gross,
			SourceDeductions: l.SourceDeductions, PPh21: l.PPh21, BPU: dec(l.BPUJKK).Add(dec(l.BPUJKM)).String(), OtherDeductions: l.OtherDeductions,
			Net: l.Net, PaymentMethod: l.PaymentMethod, Sources: l.Sources})
	}
	if _, err := m.Events.Publish(ctx, tx, hris.EventPayoutPosted, "hris.payout_run", &run.ID, &run.PropertyID, ev); err != nil {
		return err
	}
	return m.notifyPartners(ctx, tx, run, lines, "Approved — paid on "+ymd(run.PayDate)+".")
}

func kindPath(kind string) string {
	if kind == hris.PayoutKindInstructor {
		return "instructors"
	}
	return "caddy"
}

// notifyPartners tells each partner with a login that the statement is
// ready (Caddy App / Instructor → Payout History).
func (m *Module) notifyPartners(ctx context.Context, tx pgx.Tx, run PayoutRun, lines []PayoutRunLine, status string) error {
	if m.Directory == nil {
		return nil
	}
	link := "/tablet/payouts"
	if run.Kind == hris.PayoutKindInstructor {
		link = "/ops/instructor/honor"
	}
	for _, l := range lines {
		p, err := m.Directory.Partner(ctx, tx, run.PropertyID, run.Kind, l.PartnerID)
		if err != nil {
			return err
		}
		if p == nil || p.UserID == nil {
			continue
		}
		if err := m.notifyUsers(ctx, tx, run.PropertyID, []uuid.UUID{*p.UserID}, "hris.payout_statement_ready", link+"/"+l.ID.String(),
			map[string]any{"number": run.Number, "period": ymd(run.PeriodStart) + " – " + ymd(run.PeriodEnd), "gross": money(dec(l.Gross)),
				"pph21": money(dec(l.PPh21)), "bpu": money(dec(l.BPUJKK).Add(dec(l.BPUJKM))),
				"deductions": money(dec(l.SourceDeductions).Add(dec(l.OtherDeductions))), "net": money(dec(l.Net)), "status": status}); err != nil {
			return err
		}
	}
	return nil
}

// ── mark paid / cancel ───────────────────────────────────────────────────

func (m *Module) markPaidHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, in PayoutRunPayment) (PayoutRunDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PayoutRunDetail{}, err
	}
	run, err := lockRun(ctx, tx, rid)
	if err != nil {
		return PayoutRunDetail{}, err
	}
	if run.Status != "approved" {
		return PayoutRunDetail{}, errs.Conflict("run_not_approved", "only an approved run can be marked paid (the run is "+run.Status+")")
	}
	paidOn := today(ctx, tx, run.PropertyID)
	if p, err := parseDate("paidOn", in.PaidOn); err != nil {
		return PayoutRunDetail{}, err
	} else if p != nil {
		paidOn = *p
	}
	srcType := SourceCaddySettlement
	if run.Kind == hris.PayoutKindInstructor {
		srcType = SourceInstructorFee
	}
	var ids []uuid.UUID
	rows, err := tx.Query(ctx, `SELECT source_id FROM hris.payout_sources WHERE payout_run_id = $1 AND status = 'claimed'`, rid)
	if err != nil {
		return PayoutRunDetail{}, err
	}
	if ids, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return PayoutRunDetail{}, err
	}
	if m.Directory != nil && len(ids) > 0 {
		paid, err := m.Directory.PaidElsewhere(ctx, tx, srcType, ids)
		if err != nil {
			return PayoutRunDetail{}, err
		}
		for _, s := range ids {
			if paid[s] {
				return PayoutRunDetail{}, errs.Conflict("source_paid_elsewhere", "a settlement / fee of the run was paid outside the run meanwhile")
			}
		}
		if err := m.Directory.MarkPaid(ctx, tx, run.PropertyID, srcType, ids, run.Number); err != nil {
			return PayoutRunDetail{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payout_sources SET status = 'paid', consumed_at = now() WHERE payout_run_id = $1 AND status = 'claimed'`,
		rid); err != nil {
		return PayoutRunDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payout_lines SET status = 'paid' WHERE run_id = $1`, rid); err != nil {
		return PayoutRunDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET status = 'paid', paid_at = now(), paid_on = $2, paid_reference = $3, paid_by = $4, updated_by = $4
		WHERE id = $1`, rid, paidOn, nullStr(in.Reference), actor(ctx)); err != nil {
		return PayoutRunDetail{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, hris.EventPayoutPaid, "hris.payout_run", &rid, &run.PropertyID, hris.PayoutPaid{RunID: rid, Number: run.Number,
		PropertyID: run.PropertyID, Kind: run.Kind, PaidOn: ymd(paidOn), Reference: in.Reference, Net: run.Net, BankAmount: run.BankAmount,
		CashAmount: run.CashAmount}); err != nil {
		return PayoutRunDetail{}, err
	}
	out, err := m.Run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "mark_paid", EntityType: "hris.payout_run", EntityID: rid.String(),
		EntityLabel: run.Number, PropertyID: &run.PropertyID, Before: map[string]any{"status": run.Status},
		After: map[string]any{"status": "paid", "paidOn": ymd(paidOn), "reference": in.Reference, "net": run.Net}}); err != nil {
		return out, err
	}
	return out, m.notifyPartners(ctx, tx, out.PayoutRun, out.Lines, "Paid on "+ymd(paidOn)+".")
}

func (m *Module) cancelRunHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, in PayoutRunAction) (PayoutRunDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PayoutRunDetail{}, err
	}
	run, err := lockRun(ctx, tx, rid)
	if err != nil {
		return PayoutRunDetail{}, err
	}
	if !slices.Contains([]string{"draft", "calculated", "pending_approval"}, run.Status) {
		return PayoutRunDetail{}, errs.Conflict("run_posted", "an approved or paid run cannot be cancelled (the run is "+run.Status+")")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return PayoutRunDetail{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if run.Status == "pending_approval" && run.ApprovalRequestID != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET status = 'calculated' WHERE id = $1`, rid); err != nil {
			return PayoutRunDetail{}, err
		}
		if err := m.Approvals.Cancel(ctx, tx, *run.ApprovalRequestID, "payout run cancelled"); err != nil {
			return PayoutRunDetail{}, err
		}
	}
	if err := releaseRun(ctx, tx, rid); err != nil {
		return PayoutRunDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, partners = 0, held_partners = 0,
		gross = 0, source_deductions = 0, pph21 = 0, bpu = 0, other_deductions = 0, net = 0, bank_amount = 0, cash_amount = 0, updated_by = $3 WHERE id = $1`,
		rid, in.Reason, actor(ctx)); err != nil {
		return PayoutRunDetail{}, err
	}
	out, err := m.Run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.payout_run", EntityID: rid.String(),
		EntityLabel: run.Number, PropertyID: &run.PropertyID, Reason: in.Reason, Before: runAudit(run), After: map[string]any{"status": "cancelled"}})
}

// ── bank file (FR-CDY-04, FR-INT-P5-04) ──────────────────────────────────

// BankFile renders the bulk transfer file of an approved / paid run:
// generic_csv (one row per transfer) or bca_csv (header with the debit
// account, total and count, then the transfers). Cash lines are listed by
// the statements, not in the bank file.
func (m *Module) BankFile(ctx context.Context, tx pgx.Tx, rid uuid.UUID, format string) (string, string, error) {
	run, err := getOne[PayoutRun]("payout run")(tx.Query(ctx, runSelect+` WHERE id = $1 AND property_id = $2`, rid, handle.Property(ctx)))
	if err != nil {
		return "", "", err
	}
	if run.Status != "approved" && run.Status != "paid" {
		return "", "", errs.Conflict("run_not_approved", "the bank file is available once the run is approved")
	}
	pol, _, err := hris.LoadPartnerPayoutPolicy(ctx, tx, run.PropertyID, hris.PolicyTime(run.PeriodEnd))
	if err != nil {
		return "", "", err
	}
	if format == "" {
		format = pol.BankFile
	}
	if format != "generic_csv" && format != "bca_csv" {
		return "", "", enumErr("format", []string{"generic_csv", "bca_csv"})
	}
	lines, err := handle.List[PayoutRunLine](tx.Query(ctx, lineSelect+` WHERE run_id = $1 AND payment_method = 'bank_transfer' AND net > 0
		ORDER BY partner_name, partner_id`, rid))
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	total := decimal.Zero
	for _, l := range lines {
		total = total.Add(dec(l.Net))
	}
	ref := func(l PayoutRunLine) string { return strings.TrimSpace(run.Number + " " + deref(l.PartnerCode)) }
	if format == "bca_csv" {
		_ = w.Write([]string{"H", deref(nullStr(pol.DebitAccount)), ymd(run.PayDate), itoa(len(lines)), total.StringFixed(2), run.Number})
		for _, l := range lines {
			_ = w.Write([]string{"D", deref(l.BankAccountNo), deref(l.BankAccountName), dec(l.Net).StringFixed(2), ref(l),
				"Payout " + run.Kind + " " + ymd(run.PeriodStart) + "-" + ymd(run.PeriodEnd)})
		}
	} else {
		_ = w.Write([]string{"pay_date", "run", "partner_code", "partner_name", "bank_code", "bank_name", "account_no", "account_name", "amount", "currency",
			"reference"})
		for _, l := range lines {
			var bankCode *string
			_ = tx.QueryRow(ctx, `SELECT bank_code FROM hris.payout_lines WHERE id = $1`, l.ID).Scan(&bankCode)
			_ = w.Write([]string{ymd(run.PayDate), run.Number, deref(l.PartnerCode), l.PartnerName, deref(bankCode), deref(l.BankName),
				deref(l.BankAccountNo), deref(l.BankAccountName), dec(l.Net).StringFixed(2), "IDR", ref(l)})
		}
	}
	w.Flush()
	return b.String(), strings.ToLower(run.Number) + "-" + format + ".csv", w.Error()
}

func itoa(n int) string { return decimal.NewFromInt(int64(n)).String() }

func (m *Module) bankFileHTTP(w http.ResponseWriter, r *http.Request) {
	rid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out, name string
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, name, err = m.BankFile(ctx, tx, rid, filterParam(r, "format")); err != nil {
			return err
		}
		pid := handle.Property(ctx)
		if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET bank_file_at = now() WHERE id = $1`, rid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "export", EntityType: "hris.payout_run", EntityID: rid.String(),
			EntityLabel: name, PropertyID: &pid, Metadata: map[string]any{"file": "bank_file"}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write([]byte(out))
}
