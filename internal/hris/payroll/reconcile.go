package payroll

// Payroll totals of the HR Migration Reconciliation (PRD P5 EP-28
// FR-MIG-P5-05/06): the gross and net pay of the last period paid by the
// legacy payroll against the OneClub parallel run of that period, signed off
// by the HR Manager and the Finance Manager with headcount and leave
// balances. Key TOTAL = the last period on or before the cutover with
// imported legacy payroll (`oneclub import hris --legacy-payroll`); a period
// code YYYY-MM picks another period. The OneClub value sums the regular
// runs of the period that are calculated or later (a parallel run is
// usually calculated, not approved).

import (
	"context"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
)

// Reconciliation metric codes of payroll.
const (
	ReconPayrollGross = "payroll_gross"
	ReconPayrollNet   = "payroll_net"
)

var periodKey = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// reconPeriod resolves the period of a key: a period code, or for TOTAL the
// last legacy payroll period on or before the cutover ("" = none).
func reconPeriod(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time, key string) (string, error) {
	if periodKey.MatchString(key) {
		return key, nil
	}
	if key != hris.ReconciliationTotal {
		return "", nil
	}
	var p *string
	err := q.QueryRow(ctx, `SELECT max(period_code) FROM hris.legacy_payroll_lines WHERE property_id = $1 AND period_code <= $2`,
		property, cutover.Format("2006-01")).Scan(&p)
	if err != nil || p == nil {
		return "", err
	}
	return *p, nil
}

// payrollTotal sums a slip column of the regular runs of a period.
func payrollTotal(column string) func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time, key string) (decimal.Decimal, error) {
	return func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time, key string) (decimal.Decimal, error) {
		period, err := reconPeriod(ctx, q, property, cutover, key)
		if err != nil || period == "" {
			return decimal.Zero, err
		}
		var v decimal.Decimal
		err = q.QueryRow(ctx, `SELECT coalesce(sum(s.`+column+`), 0) FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id
			WHERE r.property_id = $1 AND r.period_code = $2 AND r.run_type = 'regular' AND r.status IN ('calculated', 'submitted', 'approved', 'posted', 'paid')`,
			property, period).Scan(&v)
		return v, err
	}
}

func init() {
	help := "TOTAL (the last period with imported legacy payroll on or before the cutover: the parallel run) or a period YYYY-MM"
	hris.RegisterReconciliationMetric(hris.ReconciliationMetric{Code: ReconPayrollGross, Label: "Payroll Gross", KeyHelp: help, Value: payrollTotal("gross")})
	hris.RegisterReconciliationMetric(hris.ReconciliationMetric{Code: ReconPayrollNet, Label: "Payroll Net Pay", KeyHelp: help, Value: payrollTotal("net")})
}
