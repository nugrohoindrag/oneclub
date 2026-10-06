package payouts

// Payroll input sources of the area (hris.RegisterPayrollInputSource,
// docs/p5-contracts.md "Payroll inputs"): the payroll engine (EP-09)
// collects these lines when it calculates a run and marks them consumed in
// the transaction that posts the run. A line belongs to the payroll period
// whose end is on or after the first day of its pay period (late approvals
// catch up with the next payroll); a consumed line is never returned again.
//
//   service_charge  SERVICE_CHARGE earning, approved distribution lines (EP-11)
//   commission      COMMISSION earning + COMMISSION_CLAWBACK deduction of
//                   approved commission statements (EP-12); CRM Paid on posting
//   bonus           BONUS earning (irregular) of approved bonus programmes
//   instructor_fee  INSTRUCTOR_FEE earning of employee instructors (EP-14,
//                   FR-INS-HR-03); the sport club fee turns Paid on posting

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
)

// Payroll input source codes, components and source types.
const (
	InputServiceCharge = "service_charge"
	InputCommission    = "commission"
	InputBonus         = "bonus"
	InputInstructorFee = "instructor_fee"

	ComponentServiceCharge     = "SERVICE_CHARGE"
	ComponentCommission        = "COMMISSION"
	ComponentCommissionClawbak = "COMMISSION_CLAWBACK"
	ComponentBonus             = "BONUS"
	ComponentInstructorFee     = "INSTRUCTOR_FEE"

	SourceServiceChargeLine = "hris.service_charge_line"
	SourceCommissionPayout  = "hris.commission_payout"
	SourceBonusLine         = "hris.bonus_line"
	SourcePayoutSource      = "hris.payout_source"
)

// RegisterPayrollInputs registers the payroll input sources of the area.
func (m *Module) RegisterPayrollInputs() {
	hris.RegisterPayrollInputSource(hris.PayrollInputSource{Code: InputServiceCharge, Lines: serviceChargeInputs, Consumed: serviceChargeConsumed})
	hris.RegisterPayrollInputSource(hris.PayrollInputSource{Code: InputCommission, Lines: commissionInputs, Consumed: m.commissionConsumed})
	hris.RegisterPayrollInputSource(hris.PayrollInputSource{Code: InputBonus, Lines: bonusInputs, Consumed: bonusConsumed})
	hris.RegisterPayrollInputSource(hris.PayrollInputSource{Code: InputInstructorFee, Lines: instructorFeeInputs, Consumed: m.instructorFeeConsumed})
}

func nonNilIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// inputRow is a source row: id, employee, amounts (earning, deduction),
// description and the irregular flag.
type inputRow struct {
	id, employee uuid.UUID
	a, b         decimal.Decimal
	desc         string
	irregular    bool
}

// collect runs a query returning input rows.
func collect(ctx context.Context, q dbtx.Querier, sql string, args ...any) ([]inputRow, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inputRow
	for rows.Next() {
		var r inputRow
		if err := rows.Scan(&r.id, &r.employee, &r.a, &r.b, &r.desc, &r.irregular); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func serviceChargeInputs(ctx context.Context, q dbtx.Querier, property uuid.UUID, _, to time.Time, employees []uuid.UUID) ([]hris.PayrollInputLine, error) {
	rows, err := collect(ctx, q, `SELECT l.id, l.employee_id, l.amount, 0::numeric, 'Service charge ' || to_char(make_date(d.year, d.month, 1), 'YYYY-MM'), false
		FROM hris.service_charge_lines l JOIN hris.service_charge_distributions d ON d.id = l.distribution_id
		WHERE d.property_id = $1 AND d.status IN ('approved', 'paid') AND l.eligible AND l.amount > 0 AND l.consumed_at IS NULL
		  AND to_date(d.pay_period || '-01', 'YYYY-MM-DD') <= $2::date AND (cardinality($3::uuid[]) = 0 OR l.employee_id = ANY($3))
		ORDER BY d.year, d.month, l.employee_id`, property, to.Format(time.DateOnly), nonNilIDs(employees))
	if err != nil {
		return nil, err
	}
	out := make([]hris.PayrollInputLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, hris.PayrollInputLine{EmployeeID: r.employee, ComponentCode: ComponentServiceCharge, Description: r.desc,
			Kind: hris.PayrollInputEarning, Amount: r.a, SourceType: SourceServiceChargeLine, SourceID: r.id})
	}
	return out, nil
}

func idsOf(lines []hris.PayrollInputLine) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.SourceID)
	}
	return out
}

func serviceChargeConsumed(ctx context.Context, q dbtx.Querier, property, runID uuid.UUID, lines []hris.PayrollInputLine) error {
	if _, err := q.Exec(ctx, `UPDATE hris.service_charge_lines SET payroll_run_id = $3, consumed_at = now() WHERE property_id = $1 AND id = ANY($2)
		AND consumed_at IS NULL`, property, idsOf(lines), runID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `UPDATE hris.service_charge_distributions d SET status = 'paid', paid_at = now()
		WHERE d.property_id = $1 AND d.status = 'approved' AND d.id IN (SELECT distribution_id FROM hris.service_charge_lines WHERE id = ANY($2))
		  AND NOT EXISTS (SELECT 1 FROM hris.service_charge_lines x WHERE x.distribution_id = d.id AND x.eligible AND x.amount > 0 AND x.consumed_at IS NULL)`,
		property, idsOf(lines))
	return err
}

func commissionInputs(ctx context.Context, q dbtx.Querier, property uuid.UUID, _, to time.Time, employees []uuid.UUID) ([]hris.PayrollInputLine, error) {
	rows, err := collect(ctx, q, `SELECT id, employee_id, earning, deduction, 'Commission ' || number || ' (' || period || ')', irregular
		FROM hris.commission_payouts WHERE property_id = $1 AND status = 'ready' AND employee_id IS NOT NULL AND consumed_at IS NULL
		  AND period <= to_char($2::date, 'YYYY-MM') AND (cardinality($3::uuid[]) = 0 OR employee_id = ANY($3))
		ORDER BY period, number`, property, to.Format(time.DateOnly), nonNilIDs(employees))
	if err != nil {
		return nil, err
	}
	var out []hris.PayrollInputLine
	for _, r := range rows {
		if r.a.IsPositive() {
			out = append(out, hris.PayrollInputLine{EmployeeID: r.employee, ComponentCode: ComponentCommission, Description: r.desc,
				Kind: hris.PayrollInputEarning, Amount: r.a, SourceType: SourceCommissionPayout, SourceID: r.id, Irregular: r.irregular})
		}
		if r.b.IsPositive() {
			out = append(out, hris.PayrollInputLine{EmployeeID: r.employee, ComponentCode: ComponentCommissionClawbak, Description: r.desc + " clawback",
				Kind: hris.PayrollInputDeduction, Amount: r.b, SourceType: SourceCommissionPayout, SourceID: r.id})
		}
	}
	if out == nil {
		out = []hris.PayrollInputLine{}
	}
	return out, nil
}

// commissionConsumed marks the payouts paid and the CRM statements Paid
// (FR-CMS-HR-03).
func (m *Module) commissionConsumed(ctx context.Context, q dbtx.Querier, property, runID uuid.UUID, lines []hris.PayrollInputLine) error {
	rows, err := q.Query(ctx, `UPDATE hris.commission_payouts SET status = 'paid', payroll_run_id = $3, consumed_at = now()
		WHERE property_id = $1 AND id = ANY($2) AND status = 'ready' RETURNING statement_id`, property, idsOf(lines), runID)
	if err != nil {
		return err
	}
	var stmts []uuid.UUID
	for rows.Next() {
		var s uuid.UUID
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return err
		}
		stmts = append(stmts, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(stmts) == 0 || m.Directory == nil {
		return nil
	}
	return m.Directory.MarkPaid(ctx, q, property, SourceCommissionStatement, stmts, "Payroll run "+runID.String())
}

func bonusInputs(ctx context.Context, q dbtx.Querier, property uuid.UUID, _, to time.Time, employees []uuid.UUID) ([]hris.PayrollInputLine, error) {
	rows, err := collect(ctx, q, `SELECT l.id, l.employee_id, l.amount, 0::numeric, 'Bonus ' || b.name || ' (' || b.number || ')', true
		FROM hris.bonus_lines l JOIN hris.bonus_programmes b ON b.id = l.programme_id
		WHERE b.property_id = $1 AND b.status IN ('approved', 'paid') AND l.consumed_at IS NULL
		  AND to_date(b.pay_period || '-01', 'YYYY-MM-DD') <= $2::date AND (cardinality($3::uuid[]) = 0 OR l.employee_id = ANY($3))
		ORDER BY b.pay_period, b.number, l.employee_id`, property, to.Format(time.DateOnly), nonNilIDs(employees))
	if err != nil {
		return nil, err
	}
	out := make([]hris.PayrollInputLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, hris.PayrollInputLine{EmployeeID: r.employee, ComponentCode: ComponentBonus, Description: r.desc, Kind: hris.PayrollInputEarning,
			Amount: r.a, SourceType: SourceBonusLine, SourceID: r.id, Irregular: true})
	}
	return out, nil
}

func bonusConsumed(ctx context.Context, q dbtx.Querier, property, runID uuid.UUID, lines []hris.PayrollInputLine) error {
	if _, err := q.Exec(ctx, `UPDATE hris.bonus_lines SET payroll_run_id = $3, consumed_at = now() WHERE property_id = $1 AND id = ANY($2)
		AND consumed_at IS NULL`, property, idsOf(lines), runID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `UPDATE hris.bonus_programmes b SET status = 'paid' WHERE b.property_id = $1 AND b.status = 'approved'
		AND b.id IN (SELECT programme_id FROM hris.bonus_lines WHERE id = ANY($2))
		AND NOT EXISTS (SELECT 1 FROM hris.bonus_lines x WHERE x.programme_id = b.id AND x.consumed_at IS NULL)`, property, idsOf(lines))
	return err
}

func instructorFeeInputs(ctx context.Context, q dbtx.Querier, property uuid.UUID, _, to time.Time, employees []uuid.UUID) ([]hris.PayrollInputLine, error) {
	rows, err := collect(ctx, q, `SELECT id, employee_id, gross, 0::numeric, 'Instructor fee ' || number || ' (' || to_char(period_start, 'YYYY-MM-DD') || ' – ' ||
		to_char(period_end, 'YYYY-MM-DD') || ')', false
		FROM hris.payout_sources WHERE property_id = $1 AND channel = 'payroll' AND status = 'open' AND period_end <= $2::date
		  AND (cardinality($3::uuid[]) = 0 OR employee_id = ANY($3)) ORDER BY period_end, number`, property, to.Format(time.DateOnly), nonNilIDs(employees))
	if err != nil {
		return nil, err
	}
	out := make([]hris.PayrollInputLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, hris.PayrollInputLine{EmployeeID: r.employee, ComponentCode: ComponentInstructorFee, Description: r.desc,
			Kind: hris.PayrollInputEarning, Amount: r.a, SourceType: SourcePayoutSource, SourceID: r.id})
	}
	return out, nil
}

func (m *Module) instructorFeeConsumed(ctx context.Context, q dbtx.Querier, property, runID uuid.UUID, lines []hris.PayrollInputLine) error {
	rows, err := q.Query(ctx, `UPDATE hris.payout_sources SET status = 'paid', payroll_run_id = $3, consumed_at = now()
		WHERE property_id = $1 AND id = ANY($2) AND channel = 'payroll' AND status = 'open' RETURNING source_id`, property, idsOf(lines), runID)
	if err != nil {
		return err
	}
	var fees []uuid.UUID
	for rows.Next() {
		var s uuid.UUID
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return err
		}
		fees = append(fees, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(fees) == 0 || m.Directory == nil {
		return nil
	}
	return m.Directory.MarkPaid(ctx, q, property, SourceInstructorFee, fees, fmt.Sprintf("Payroll run %s", runID))
}
