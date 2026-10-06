package payroll

// Payroll exception queue (HRIS improvement phase B, spec §35): the
// exceptions of the last calculation of a run, one per employee and
// problem, so HR works them as a queue — View → Fix (employee data,
// attendance, contract) → Revalidate (:calculate). Errors (negative net pay,
// the employee already in another run of the period and type) block the
// submission for approval; warnings do not.

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// PayrollException is one exception of a run.
type PayrollException struct {
	Code         string    `json:"code" enum:"negative_net,duplicate_payroll,missing_salary,missing_contract,missing_tax_profile,missing_tax_id,missing_bpjs,missing_bank,attendance_exception,employee_suspended,other"`
	Severity     string    `json:"severity" enum:"error,warning"`
	EmployeeID   uuid.UUID `json:"employeeId"`
	EmployeeNo   string    `json:"employeeNo"`
	EmployeeName string    `json:"employeeName"`
	SlipID       uuid.UUID `json:"slipId"`
	Message      string    `json:"message"`
}

func (m *Module) registerExceptions(reg *route.Registry) {
	add(reg, "HRIS Payroll", route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs/{id}/exceptions",
		Summary: "Exception queue of the last calculation (errors block the submission for approval)", Permission: PermRunView,
		Response: PayrollException{}, List: true, Query: []route.Param{{Name: "severity", Enum: []string{"error", "warning"}}},
		Handler: listRead(m.DB, m.exceptionsHTTP)})
}

func (m *Module) exceptionsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollException, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	if _, err := m.inProperty(ctx, tx, rid, handle.Property(ctx)); err != nil {
		return nil, err
	}
	out, err := runExceptions(ctx, tx, rid)
	if sev := filterParam(r, "severity"); sev != "" && err == nil {
		kept := out[:0]
		for _, x := range out {
			if x.Severity == sev {
				kept = append(kept, x)
			}
		}
		out = kept
	}
	return out, err
}

// blockingExceptions refuses the submission of a run with errors.
func (m *Module) blockingExceptions(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	xs, err := runExceptions(ctx, tx, rid)
	if err != nil {
		return err
	}
	n := 0
	for _, x := range xs {
		if x.Severity == "error" {
			n++
		}
	}
	if n > 0 {
		return errs.Conflict("payroll_exceptions", itoa(n)+" payroll exception(s) must be fixed and the run recalculated before approval")
	}
	return nil
}

// messages of the calculation already covered by a structured exception
var knownMessages = []string{"No PTKP status", "No active bank account", "attendance exception(s) open", "No contract in force", "Negative net pay"}

func runExceptions(ctx context.Context, tx pgx.Tx, rid uuid.UUID) ([]PayrollException, error) {
	rows, err := tx.Query(ctx, `SELECT s.id, s.employee_id, s.employee_no, s.full_name, s.ptkp_status IS NOT NULL,
		(coalesce(s.npwp, '') <> '' OR coalesce(s.nik, '') <> ''), s.account_no IS NOT NULL, s.gross, s.net, s.messages,
		(coalesce(e.bpjs_kesehatan_no, '') <> '' AND coalesce(e.bpjs_ketenagakerjaan_no, '') <> ''),
		(e.suspended_from IS NOT NULL AND e.suspended_from <= r.period_end AND (e.suspended_until IS NULL OR e.suspended_until >= r.period_start)),
		(SELECT o.number FROM hris.payroll_slips os JOIN hris.payroll_runs o ON o.id = os.run_id
		  WHERE os.employee_id = s.employee_id AND o.id <> r.id AND o.period_code = r.period_code AND o.run_type = r.run_type AND o.status <> 'cancelled'
		  ORDER BY o.created_at LIMIT 1)
		FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id JOIN hris.employees e ON e.id = s.employee_id
		WHERE s.run_id = $1 ORDER BY s.full_name, s.employee_no`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PayrollException{}
	for rows.Next() {
		var slip, emp uuid.UUID
		var no, name string
		var ptkp, taxID, bank, bpjs, suspended bool
		var gross, net decimal.Decimal
		var msgs []string
		var dup *string
		if err := rows.Scan(&slip, &emp, &no, &name, &ptkp, &taxID, &bank, &gross, &net, &msgs, &bpjs, &suspended, &dup); err != nil {
			return nil, err
		}
		x := func(code, sev, msg string) {
			out = append(out, PayrollException{Code: code, Severity: sev, EmployeeID: emp, EmployeeNo: no, EmployeeName: name, SlipID: slip, Message: msg})
		}
		if net.IsNegative() {
			x("negative_net", "error", "Net pay is negative ("+money(net)+"): deductions exceed the pay")
		}
		if dup != nil {
			x("duplicate_payroll", "error", "Already in payroll run "+*dup+" of the same period and type")
		}
		if gross.IsZero() {
			x("missing_salary", "warning", "No pay calculated: check the contract and the salary structure")
		}
		for _, msg := range msgs {
			switch {
			case strings.HasPrefix(msg, "No contract in force"):
				x("missing_contract", "warning", msg)
			case strings.Contains(msg, "attendance exception(s) open"):
				x("attendance_exception", "warning", msg)
			}
		}
		if !ptkp {
			x("missing_tax_profile", "warning", "No PTKP status: TK/0 applied")
		}
		if !taxID {
			x("missing_tax_id", "warning", "No NPWP or NIK: PPh 21 cannot be reported to the employee")
		}
		if !bpjs {
			x("missing_bpjs", "warning", "BPJS Kesehatan or Ketenagakerjaan number missing")
		}
		if !bank {
			x("missing_bank", "warning", "No active bank account: excluded from the bank file")
		}
		if suspended {
			x("employee_suspended", "warning", "Suspended during the period: check whether the pay applies")
		}
		for _, msg := range msgs {
			known := false
			for _, k := range knownMessages {
				known = known || strings.Contains(msg, k)
			}
			if !known {
				x("other", "warning", msg)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == "error" && out[j].Severity != "error" })
	return out, rows.Err()
}
