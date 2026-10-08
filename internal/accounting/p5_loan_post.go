package accounting

// Employee loans and cash advances (HRIS improvement phase B, spec §24 and
// §30 Finance Integration Boundary): hris publishes hris.loan_disbursed when
// Finance pays an approved loan / cash advance to the employee and
// hris.loan_repaid when the employee repays outside payroll (cash returned,
// transfer). Dr employee receivables / Cr cash or bank, and back. Payroll
// installments are booked by DEF-PAYROLL-DEDUCT (p5_payroll_post.go).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Loan events consumed.
const (
	evLoanDisbursed = "hris.loan_disbursed"
	evLoanRepaid    = "hris.loan_repaid"
)

func init() {
	ConsumedEvents = append(ConsumedEvents, evLoanDisbursed, evLoanRepaid)
	extraHandlers[evLoanDisbursed] = func(m *Module) evHandler { return m.onLoanMoney(true) }
	extraHandlers[evLoanRepaid] = func(m *Module) evHandler { return m.onLoanMoney(false) }
	r := func(code, name, source, method, debit, credit string, prio int) ruleSpec {
		cond := map[string]any{}
		if method != "" {
			cond["method"] = method
		}
		return ruleSpec{Code: code, Name: name, Source: source, Cond: cond, Debit: debit, Credit: credit, Priority: prio}
	}
	extraRuleSpecs = append(extraRuleSpecs,
		r("DEF-LOAN-PAY-CASH", "Employee loan / cash advance paid in cash", evLoanDisbursed, "cash", "@employee_receivable", "@cash", 50),
		r("DEF-LOAN-PAY", "Employee loan / cash advance paid", evLoanDisbursed, "", "@employee_receivable", "@bank", 100),
		r("DEF-LOAN-REPAY-CASH", "Employee loan / cash advance repaid in cash", evLoanRepaid, "cash", "@cash", "@employee_receivable", 50),
		r("DEF-LOAN-REPAY", "Employee loan / cash advance repaid", evLoanRepaid, "", "@bank", "@employee_receivable", 100))
}

// onLoanMoney books a disbursement (paid) or a repayment outside payroll.
func (m *Module) onLoanMoney(paid bool) evHandler {
	return func(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
		var p struct {
			LoanID          uuid.UUID `json:"loanId"`
			MovementID      uuid.UUID `json:"movementId"`
			Number          string    `json:"number"`
			LoanType        string    `json:"loanType"`
			EmployeeName    string    `json:"employeeName"`
			Date            string    `json:"date"`
			Amount          string    `json:"amount"`
			Method          string    `json:"method"`
			Reference       string    `json:"reference"`
			BankAccountCode string    `json:"bankAccountCode"`
		}
		if err := ev.decode(&p); err != nil || p.LoanID == uuid.Nil || p.MovementID == uuid.Nil {
			return outcome{}, badPayload(ev.Type + " payload")
		}
		amt := dec(p.Amount)
		if !amt.IsPositive() {
			return outcome{Status: "no_posting", Note: "loan " + p.Number + " without amount"}, nil
		}
		d, err := time.Parse("2006-01-02", p.Date)
		if err != nil {
			d = eventDate(ctx, tx, ev)
		}
		if err := ensurePayrollSetup(ctx, tx); err != nil { // the employee receivable account
			return outcome{}, err
		}
		money := "bank"
		if p.Method == "cash" {
			money = "cash"
		}
		what, part := "Loan", 1
		if p.LoanType == "cash_advance" {
			what = "Cash advance"
		}
		it := Item{Source: ev.Type, Attrs: map[string]string{"method": p.Method, "loanType": p.LoanType}, Amount: amt, SourceType: "hris.employee_loan",
			SourceID: p.LoanID.String()}
		if paid {
			it.Description = what + " " + p.Number + " paid · " + p.EmployeeName + " " + p.Reference
			it.FallbackDebit, it.FallbackCredit, it.CreditCode = "employee_receivable", money, p.BankAccountCode
		} else {
			part = 2
			it.Description = what + " " + p.Number + " repaid · " + p.EmployeeName + " " + p.Reference
			it.FallbackDebit, it.FallbackCredit, it.DebitCode = money, "employee_receivable", p.BankAccountCode
		}
		var out outcome
		j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.MovementID.String(),
			SourceRef: p.Number, EventID: &ev.ID, Description: it.Description},
			Sources: []sourceMark{{Type: "hris.loan_movement", ID: p.MovementID, Part: part, BusinessDate: &d, Key1: p.Number, Amount: amt, Items: []Item{it}}}})
		out.add(j, missing)
		return out, err
	}
}
