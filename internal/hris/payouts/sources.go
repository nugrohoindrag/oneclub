package payouts

// Event-maintained sources of the payouts (hris never reads or writes the
// golf, sport club or CRM tables): approved caddy settlements with their
// non-cash tips (H1, golf.caddy_settlement_approved), approved instructor
// fees (H2, sportclub.instructor_fee_approved) and approved commission
// statements (H3, crm.commission_approved). Payloads are decoded by name
// into local structs; a redelivered event is ignored (unique source).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
)

// Consumed events.
const (
	EventCaddySettlementApproved = "golf.caddy_settlement_approved"
	EventInstructorFeeApproved   = "sportclub.instructor_fee_approved"
	EventCommissionApproved      = "crm.commission_approved"
)

// Subscriptions returns the outbox subscribers of the area (subscriber
// name → handler per event type).
func (m *Module) Subscriptions() map[string]outbox.Handler {
	return map[string]outbox.Handler{
		EventCaddySettlementApproved: m.OnCaddySettlementApproved,
		EventInstructorFeeApproved:   m.OnInstructorFeeApproved,
		EventCommissionApproved:      m.OnCommissionApproved,
	}
}

// PayoutSource is an approved settlement / fee waiting for (or paid by) a
// payout run or payroll.
type PayoutSource struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	Kind        string     `json:"kind" db:"kind" enum:"caddy,instructor"`
	SourceType  string     `json:"sourceType" db:"source_type" enum:"golf.caddy_settlement,sportclub.instructor_fee"`
	SourceID    uuid.UUID  `json:"sourceId" db:"source_id"`
	Number      string     `json:"number" db:"number"`
	PartnerID   uuid.UUID  `json:"partnerId" db:"partner_id"`
	PartnerName string     `json:"partnerName" db:"partner_name"`
	Channel     string     `json:"channel" db:"channel" enum:"payout,payroll" doc:"payroll: an employee instructor, paid with payroll"`
	EmployeeID  *uuid.UUID `json:"employeeId" db:"employee_id"`
	PeriodStart time.Time  `json:"periodStart" db:"period_start"`
	PeriodEnd   time.Time  `json:"periodEnd" db:"period_end"`
	Units       int        `json:"units" db:"units" doc:"Rounds (caddy) or sessions (instructor)"`
	Fee         string     `json:"fee" db:"fee"`
	Tips        string     `json:"tips" db:"tips" doc:"Non-cash tips"`
	Deductions  string     `json:"deductions" db:"deductions" doc:"Deductions of the settlement (golf Caddy Policies)"`
	Gross       string     `json:"gross" db:"gross"`
	Total       string     `json:"total" db:"total"`
	Status      string     `json:"status" db:"status" enum:"open,claimed,paid,cancelled"`
	PayoutRunID *uuid.UUID `json:"payoutRunId" db:"payout_run_id"`
	ReceivedAt  time.Time  `json:"receivedAt" db:"received_at"`
}

const sourceSelect = `SELECT id, kind, source_type, source_id, number, partner_id, partner_name, channel, employee_id, period_start, period_end, units,
	trim_scale(fee)::text AS fee, trim_scale(tips)::text AS tips, trim_scale(deductions)::text AS deductions, trim_scale(gross)::text AS gross,
	trim_scale(total)::text AS total, status, payout_run_id, received_at FROM hris.payout_sources`

func eventCtx(ctx context.Context, e outbox.Event) (context.Context, uuid.UUID, bool) {
	if e.PropertyID == nil {
		return ctx, uuid.Nil, false
	}
	return reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID), *e.PropertyID, true
}

// day parses a YYYY-MM-DD or RFC 3339 date of a payload.
func day(s string) (time.Time, bool) {
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// OnCaddySettlementApproved keeps an approved caddy settlement (caddy fee
// + non-cash tips − settlement deductions) for the next caddy payout run.
func (m *Module) OnCaddySettlementApproved(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		SettlementID uuid.UUID `json:"settlementId"`
		Number       string    `json:"number"`
		CaddyID      uuid.UUID `json:"caddyId"`
		CaddyName    string    `json:"caddyName"`
		PeriodStart  string    `json:"periodStart"`
		PeriodEnd    string    `json:"periodEnd"`
		Rounds       int       `json:"rounds"`
		CaddyFee     string    `json:"caddyFee"`
		Tips         string    `json:"tips"`
		Deductions   string    `json:"deductions"`
		Total        string    `json:"total"`
	}
	if err := e.Decode(&p); err != nil || p.SettlementID == uuid.Nil || p.CaddyID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload without the H1 fields
	}
	ctx, property, ok := eventCtx(ctx, e)
	from, ok1 := day(p.PeriodStart)
	to, ok2 := day(p.PeriodEnd)
	if !ok || !ok1 || !ok2 {
		return nil
	}
	fee, tips, ded := dec(p.CaddyFee), dec(p.Tips), dec(p.Deductions)
	_, err := tx.Exec(ctx, `INSERT INTO hris.payout_sources (id, property_id, kind, source_type, source_id, number, partner_id, partner_name, period_start,
		period_end, units, fee, tips, deductions, gross, total, event_id) VALUES ($1,$2,'caddy',$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12::numeric,$13::numeric,
		$14::numeric,$15::numeric,$16) ON CONFLICT (source_type, source_id) DO NOTHING`,
		id.New(), property, SourceCaddySettlement, p.SettlementID, p.Number, p.CaddyID, p.CaddyName, from, to, p.Rounds, fee.String(), tips.String(),
		ded.String(), fee.Add(tips).String(), dec(p.Total).String(), e.ID)
	return err
}

// OnInstructorFeeApproved keeps an approved instructor fee: partner
// instructors are paid by the monthly instructor payout run, employee
// instructors with payroll (FR-INS-HR-03).
func (m *Module) OnInstructorFeeApproved(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		FeeID        uuid.UUID `json:"feeId"`
		FeeNo        string    `json:"feeNo"`
		InstructorID uuid.UUID `json:"instructorId"`
		Amount       string    `json:"amount"`
		PeriodStart  string    `json:"periodStart"`
		PeriodEnd    string    `json:"periodEnd"`
	}
	if err := e.Decode(&p); err != nil || p.FeeID == uuid.Nil || p.InstructorID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload without the H2 fields
	}
	ctx, property, ok := eventCtx(ctx, e)
	from, ok1 := day(p.PeriodStart)
	to, ok2 := day(p.PeriodEnd)
	if !ok || !ok1 || !ok2 {
		return nil
	}
	name, channel, units := "Instructor", "payout", 0
	var employee *uuid.UUID
	if m.Directory != nil {
		pt, err := m.Directory.Partner(ctx, tx, property, hris.PayoutKindInstructor, p.InstructorID)
		if err != nil {
			return err
		}
		if pt != nil {
			name = pt.Name
			if pt.Partnership == "employee" && pt.EmployeeID != nil {
				channel, employee = "payroll", pt.EmployeeID
			}
		}
		if units, err = m.Directory.Units(ctx, tx, SourceInstructorFee, p.FeeID); err != nil {
			return err
		}
	}
	amt := dec(p.Amount)
	_, err := tx.Exec(ctx, `INSERT INTO hris.payout_sources (id, property_id, kind, source_type, source_id, number, partner_id, partner_name, channel,
		employee_id, period_start, period_end, units, fee, gross, total, event_id) VALUES ($1,$2,'instructor',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::numeric,
		$13::numeric,$13::numeric,$14) ON CONFLICT (source_type, source_id) DO NOTHING`,
		id.New(), property, SourceInstructorFee, p.FeeID, p.FeeNo, p.InstructorID, name, channel, employee, from, to, units, amt.String(), e.ID)
	return err
}

// OnCommissionApproved keeps an approved commission statement for payroll
// (FR-CMS-HR-01): earning = earned + positive adjustments, deduction =
// clawbacks (FR-CMS-HR-02). A statement whose user has no employee profile
// waits as Unmatched (HR links the login, then matches it).
func (m *Module) OnCommissionApproved(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		StatementID uuid.UUID `json:"statementId"`
		Number      string    `json:"number"`
		UserID      uuid.UUID `json:"userId"`
		Period      string    `json:"period"`
		Total       string    `json:"total"`
		Currency    string    `json:"currency"`
	}
	if err := e.Decode(&p); err != nil || p.StatementID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload without the H3 fields
	}
	ctx, property, ok := eventCtx(ctx, e)
	if !ok {
		return nil
	}
	total := dec(p.Total)
	b := CommissionBreakdown{Earned: decimal.Max(total, decimal.Zero), Clawback: decimal.Min(total, decimal.Zero), Adjustments: decimal.Zero}
	if m.Directory != nil {
		got, found, err := m.Directory.Commission(ctx, tx, p.StatementID)
		if err != nil {
			return err
		}
		if found && got.Earned.Add(got.Clawback).Add(got.Adjustments).Equal(total) {
			b = got
		}
	}
	earning, deduction := hris.CommissionSplit(b.Earned, b.Clawback, b.Adjustments)
	var employee *uuid.UUID
	status := "unmatched"
	if p.UserID != uuid.Nil {
		emp, err := hris.EmployeeByUser(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if emp != nil && emp.PropertyID == property {
			employee, status = &emp.ID, "ready"
		}
	}
	cur := p.Currency
	if cur == "" {
		cur = "IDR"
	}
	var user *uuid.UUID
	if p.UserID != uuid.Nil {
		user = &p.UserID
	}
	tag, err := tx.Exec(ctx, `INSERT INTO hris.commission_payouts (id, property_id, statement_id, number, user_id, user_name, employee_id, period, earned,
		clawback, adjustments, total, earning, deduction, currency, status, event_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11::numeric,
		$12::numeric,$13::numeric,$14::numeric,$15,$16,$17) ON CONFLICT (statement_id) DO NOTHING`,
		id.New(), property, p.StatementID, p.Number, user, nullStr(b.UserName), employee, p.Period, b.Earned.String(), b.Clawback.String(),
		b.Adjustments.String(), total.String(), earning.String(), deduction.String(), cur, status, e.ID)
	if err != nil || tag.RowsAffected() == 0 || status == "ready" {
		return err
	}
	return m.notifyUsers(ctx, tx, property, holders(ctx, tx, property, PermCommissionManage), "hris.commission_unmatched", "/hris/commissions",
		map[string]any{"number": p.Number, "period": p.Period, "user": b.UserName})
}

// ── sources list (HRIS → Caddy / Instructors) ─────────────────────────────

func (m *Module) listSourcesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayoutSource, error) {
	kind, status := filterParam(r, "kind"), filterParam(r, "status")
	if kind != "" && !oneOf(hris.PayoutKinds, kind) {
		return nil, enumErr("kind", hris.PayoutKinds)
	}
	return listSources(ctx, tx, `WHERE property_id = $1 AND ($2 = '' OR kind = $2) AND ($3 = '' OR status = $3)
		ORDER BY period_end DESC, partner_name, number LIMIT 500`, handle.Property(ctx), kind, status)
}

func listSources(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]PayoutSource, error) {
	return handle.List[PayoutSource](q.Query(ctx, sourceSelect+" "+where, args...))
}

func (m *Module) registerSources(reg *route.Registry, tag string) {
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payout-sources",
		Summary: "Approved caddy settlements and instructor fees (paid by payout runs or payroll)", Permission: PermRunView, Response: PayoutSource{},
		List: true, Query: []route.Param{{Name: "kind", Enum: hris.PayoutKinds}, {Name: "status", Enum: []string{"open", "claimed", "paid", "cancelled"}}},
		Handler: listRead(m.DB, m.listSourcesHTTP)})
}
