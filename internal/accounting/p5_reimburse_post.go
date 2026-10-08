package accounting

// Reimbursement payment (HRIS improvement phase C, spec §24 and §30): hris
// publishes hris.reimbursement_paid when Finance pays an approved claim.
// Dr the expense account of the reimbursement category (else 6113 Employee
// Reimbursements, role reimbursement_expense) with the employee's
// department and cost center / Cr cash or bank (or the GL account named
// with the payment).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const evReimbursementPaid = "hris.reimbursement_paid"

func init() {
	acct := a("6113", "6000", "Employee Reimbursements", "Penggantian Biaya Karyawan", "expense", "expense", "operating")
	Template = append(Template, acct)
	payrollAccounts = append(payrollAccounts, acct) // created by ensurePayrollSetup on charts loaded earlier
	if DefaultAccounts["reimbursement_expense"] == "" {
		DefaultAccounts["reimbursement_expense"] = "6113"
	}
	ConsumedEvents = append(ConsumedEvents, evReimbursementPaid)
	extraHandlers[evReimbursementPaid] = func(m *Module) evHandler { return m.onReimbursementPaid }
	extraRuleSpecs = append(extraRuleSpecs,
		ruleSpec{Code: "DEF-REIMB-PAY-CASH", Name: "Employee reimbursement paid in cash", Source: evReimbursementPaid, Cond: map[string]any{"method": "cash"},
			Debit: "@reimbursement_expense", Credit: "@cash", Priority: 50},
		ruleSpec{Code: "DEF-REIMB-PAY", Name: "Employee reimbursement paid", Source: evReimbursementPaid, Cond: map[string]any{},
			Debit: "@reimbursement_expense", Credit: "@bank", Priority: 100})
}

func (m *Module) onReimbursementPaid(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		ReimbursementID    uuid.UUID  `json:"reimbursementId"`
		Number             string     `json:"number"`
		EmployeeName       string     `json:"employeeName"`
		CategoryCode       string     `json:"categoryCode"`
		ExpenseAccountCode string     `json:"expenseAccountCode"`
		CostCenter         string     `json:"costCenter"`
		OrgUnitID          *uuid.UUID `json:"orgUnitId"`
		PaidOn             string     `json:"paidOn"`
		Amount             string     `json:"amount"`
		Method             string     `json:"method"`
		Reference          string     `json:"reference"`
		BankAccountCode    string     `json:"bankAccountCode"`
	}
	if err := ev.decode(&p); err != nil || p.ReimbursementID == uuid.Nil {
		return outcome{}, badPayload("reimbursement_paid payload")
	}
	amt := dec(p.Amount)
	if !amt.IsPositive() {
		return outcome{Status: "no_posting", Note: "reimbursement " + p.Number + " without amount"}, nil
	}
	d, err := time.Parse("2006-01-02", p.PaidOn)
	if err != nil {
		d = eventDate(ctx, tx, ev)
	}
	if err := ensurePayrollSetup(ctx, tx); err != nil {
		return outcome{}, err
	}
	money := "bank"
	if p.Method == "cash" {
		money = "cash"
	}
	desc := "Reimbursement " + p.Number + " " + p.CategoryCode + " · " + p.EmployeeName + " " + p.Reference
	it := Item{Source: ev.Type, Attrs: map[string]string{"method": p.Method, "category": p.CategoryCode}, Amount: amt, Description: desc,
		Dims: Dims{CostCenter: p.CostCenter, DepartmentID: p.OrgUnitID}, DimSide: "debit", SourceType: "hris.reimbursement",
		SourceID: p.ReimbursementID.String(), FallbackDebit: "reimbursement_expense", FallbackCredit: money, DebitCode: p.ExpenseAccountCode,
		CreditCode: p.BankAccountCode}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.ReimbursementID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: desc},
		Sources: []sourceMark{{Type: "hris.reimbursement", ID: p.ReimbursementID, Part: 1, BusinessDate: &d, Key1: p.Number, Amount: amt, Items: []Item{it}}}})
	out.add(j, missing)
	return out, err
}
