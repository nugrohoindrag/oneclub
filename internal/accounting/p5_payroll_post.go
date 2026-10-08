package accounting

// Payroll posting (PRD P5 EP-15 FR-PPY-01/02, contract H5): hris publishes
// hris.payroll_posted with the journal lines of an approved payroll run
// (gross pay per component and department, BPJS employer contributions,
// BPJS employee shares and PPh 21 withheld, after-tax deductions) and
// hris.payroll_paid with the net pay transferred. Accounting books them with
// the DEF-PAYROLL-* posting rules (generated with the chart; editable): Dr
// salary expense (the service charge distribution payable and the
// commission payable for components already expensed / accrued) /
// Cr salaries payable; Dr BPJS expense / Cr BPJS payable; Dr salaries
// payable / Cr PPh 21 payable, BPJS payable, employee receivables; and on
// payment Dr salaries payable / Cr bank. The payloads are decoded into
// accounting's own structs (accounting does not import hris).

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/id"
)

// Payroll events consumed.
const (
	evPayrollPosted = "hris.payroll_posted"
	evPayrollPaid   = "hris.payroll_paid"
)

// payrollAccounts are the chart accounts of payroll (added to the OneClub
// template; created on the next chart load or the first payroll posting).
var payrollAccounts = []tplAccount{
	a("1145", "1100", "Employee Receivables (Loans & Advances)", "Piutang Karyawan (Pinjaman & Kasbon)", "asset", "receivable", "operating"),
	a("2134", "2100", "PPh 21 Payable", "Utang PPh 21", "liability", "tax", "operating"),
	a("2126", "2100", "Service Charge Distribution Payable", "Utang Distribusi Service Charge", "liability", "accrued", "operating"),
	a("2176", "2100", "Salaries Payable", "Utang Gaji", "liability", "accrued", "operating"),
	a("2175", "2100", "BPJS Payable", "Utang BPJS", "liability", "accrued", "operating"),
	a("6111", "6000", "Employee Benefits – BPJS", "Tunjangan BPJS Pemberi Kerja", "expense", "expense", "operating"),
	a("6112", "6000", "Severance & Termination Pay", "Pesangon & Kompensasi", "expense", "expense", "operating"),
	// HRIS phase C: employee contributions to benefit plans withheld by payroll, due to the provider
	a("2177", "2100", "Benefit Contributions Payable", "Utang Iuran Benefit Karyawan", "liability", "accrued", "operating"),
}

// payrollRoles are the default-account roles of payroll.
var payrollRoles = map[string]string{
	"employee_receivable": "1145", "pph21_payable": "2134", "salaries_payable": "2176", "bpjs_payable": "2175", "salary_expense": "6110",
	"employee_benefit_expense": "6111", "severance_expense": "6112", "service_charge_distribution_payable": "2126",
	"benefit_payable": "2177",
}

func init() {
	// the payouts area (p5_payout_post.go) may add 2126 too: once in the template
	have := map[string]bool{}
	for _, t := range Template {
		have[t.Code] = true
	}
	for _, t := range payrollAccounts {
		if !have[t.Code] {
			Template = append(Template, t)
		}
	}
	for k, v := range payrollRoles {
		if DefaultAccounts[k] == "" {
			DefaultAccounts[k] = v
		}
	}
	ConsumedEvents = append(ConsumedEvents, evPayrollPosted, evPayrollPaid)
	extraHandlers[evPayrollPosted] = func(m *Module) evHandler { return m.onPayrollPosted }
	extraHandlers[evPayrollPaid] = func(m *Module) evHandler { return m.onPayrollPaid }
	extraRuleSpecs = append(extraRuleSpecs, payrollRuleSpecs()...)
}

// payrollRuleSpecs are the default posting rules of payroll.
func payrollRuleSpecs() []ruleSpec {
	r := func(code, name, source string, cond map[string]any, debit, credit string, prio int) ruleSpec {
		return ruleSpec{Code: code, Name: name, Source: source, Cond: cond, Debit: debit, Credit: credit, Priority: prio}
	}
	return []ruleSpec{
		r("DEF-PAYROLL-SVC", "Payroll – service charge distribution paid (clears the distribution payable)", evPayrollPosted, map[string]any{"part": "earning",
			"category": "service_charge"}, "@service_charge_distribution_payable", "@salaries_payable", 50),
		r("DEF-PAYROLL-COMM", "Payroll – sales commission paid (clears the payable)", evPayrollPosted, map[string]any{"part": "earning",
			"category": "commission"}, "@commission_payable", "@salaries_payable", 50),
		r("DEF-PAYROLL-SEV", "Payroll – severance, service award, PKWT compensation", evPayrollPosted, map[string]any{"part": "earning",
			"category": "severance"}, "@severance_expense", "@salaries_payable", 60),
		r("DEF-PAYROLL-EARN", "Payroll – gross pay", evPayrollPosted, map[string]any{"part": "earning"}, "@salary_expense", "@salaries_payable", 100),
		r("DEF-PAYROLL-BPJS-ER", "Payroll – BPJS employer contributions", evPayrollPosted, map[string]any{"part": "employer_contribution"},
			"@employee_benefit_expense", "@bpjs_payable", 100),
		r("DEF-PAYROLL-BPJS-EE", "Payroll – BPJS employee contributions withheld", evPayrollPosted, map[string]any{"part": "bpjs_employee"},
			"@salaries_payable", "@bpjs_payable", 100),
		r("DEF-PAYROLL-PPH21", "Payroll – PPh 21 withheld", evPayrollPosted, map[string]any{"part": "pph21"}, "@salaries_payable", "@pph21_payable", 100),
		r("DEF-PAYROLL-BENEFIT", "Payroll – employee benefit contributions withheld", evPayrollPosted, map[string]any{"part": "deduction",
			"component": "BENEFIT_EE"}, "@salaries_payable", "@benefit_payable", 50),
		r("DEF-PAYROLL-DEDUCT", "Payroll – loan installments and other deductions", evPayrollPosted, map[string]any{"part": "deduction"},
			"@salaries_payable", "@employee_receivable", 100),
		r("DEF-PAYROLL-PAID", "Payroll – net pay transferred", evPayrollPaid, map[string]any{"part": "net_pay"}, "@salaries_payable", "@bank", 100),
	}
}

// payrollJournalLine is accounting's view of a line of the payroll payloads.
type payrollJournalLine struct {
	Part          string     `json:"part"`
	ComponentCode string     `json:"componentCode"`
	ComponentName string     `json:"componentName"`
	Category      string     `json:"category"`
	Programme     string     `json:"programme"`
	OrgUnitID     *uuid.UUID `json:"orgUnitId"`
	CostCenter    string     `json:"costCenter"`
	Amount        string     `json:"amount"`
	DebitRole     string     `json:"debitRole"`
	CreditRole    string     `json:"creditRole"`
	Description   string     `json:"description"`
}

// ensurePayrollSetup creates the payroll accounts and DEF-PAYROLL rules a
// chart loaded before payroll existed lacks (idempotent; never re-creates
// rules of other areas).
func ensurePayrollSetup(ctx context.Context, tx pgx.Tx) error {
	ids := map[string]uuid.UUID{}
	rows, err := tx.Query(ctx, `SELECT code, id FROM accounting.accounts`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c string
		var aid uuid.UUID
		if err := rows.Scan(&c, &aid); err != nil {
			rows.Close()
			return err
		}
		ids[c] = aid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range payrollAccounts {
		parent, ok := ids[t.Parent]
		if _, exists := ids[t.Code]; exists || !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.accounts (id, code, name, name_id, account_type, subtype, parent_id, is_posting, normal_balance,
			cash_flow) VALUES ($1,$2,$3,$4,$5,$6,$7,true,$8,$9) ON CONFLICT (code) DO NOTHING`,
			id.New(), t.Code, t.Name, t.NameID, t.Type, t.Subtype, parent, normalBalance(t.Type, t.Credit), t.CashFlow); err != nil {
			return err
		}
	}
	return insertPayrollRules(ctx, tx)
}

// insertPayrollRules creates the DEF-PAYROLL-* rules missing (roles; the
// accounts of the Accounting Configuration resolve them at posting).
func insertPayrollRules(ctx context.Context, tx pgx.Tx) error {
	for _, s := range payrollRuleSpecs() {
		raw, _ := json.Marshal(s.Cond)
		dr, cr := strings.TrimPrefix(s.Debit, "@"), strings.TrimPrefix(s.Credit, "@")
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.posting_rules (id, code, name, source, conditions, debit_role, credit_role, priority, description)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,'Generated with the payroll posting' WHERE NOT EXISTS (SELECT 1 FROM accounting.posting_rules WHERE code = $2)`,
			id.New(), s.Code, s.Name, s.Source, raw, dr, cr, s.Priority); err != nil {
			return err
		}
	}
	return nil
}

func payrollItems(source, runID, number string, lines []payrollJournalLine) []Item {
	var items []Item
	for _, l := range lines {
		amt := dec(l.Amount)
		if amt.IsZero() {
			continue
		}
		attrs := map[string]string{"part": l.Part, "component": l.ComponentCode, "category": l.Category}
		if l.Programme != "" {
			attrs["programme"] = l.Programme
		}
		if l.CostCenter != "" {
			attrs["costCenter"] = l.CostCenter
		}
		it := Item{Source: source, Attrs: attrs, Amount: amt, Description: strings.TrimSpace(number + " " + l.Description), SourceType: "hris.payroll_run",
			SourceID: runID, FallbackDebit: l.DebitRole, FallbackCredit: l.CreditRole}
		// the expense side carries the department and cost center
		if l.Part == "earning" || l.Part == "employer_contribution" {
			it.Dims, it.DimSide = Dims{CostCenter: l.CostCenter, DepartmentID: l.OrgUnitID, Component: l.ComponentCode}, "debit"
		}
		items = append(items, it)
	}
	return items
}

// onPayrollPosted books the payroll journal of a posted run (dated the end
// of the payroll period).
func (m *Module) onPayrollPosted(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		RunID        uuid.UUID            `json:"runId"`
		Number       string               `json:"number"`
		RunType      string               `json:"runType"`
		PeriodCode   string               `json:"periodCode"`
		PostingDate  string               `json:"postingDate"`
		JournalLines []payrollJournalLine `json:"journalLines"`
	}
	if err := ev.decode(&p); err != nil || p.RunID == uuid.Nil {
		return outcome{}, badPayload("payroll_posted payload")
	}
	d, err := time.Parse("2006-01-02", p.PostingDate)
	if err != nil {
		d = eventDate(ctx, tx, ev)
	}
	if err := ensurePayrollSetup(ctx, tx); err != nil {
		return outcome{}, err
	}
	items := payrollItems(ev.Type, p.RunID.String(), p.Number, p.JournalLines)
	if len(items) == 0 {
		return outcome{Status: "no_posting", Note: "payroll run " + p.Number + " without amounts"}, nil
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.RunID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Payroll " + p.Number + " (" + p.PeriodCode + ")", Merge: true},
		Sources: []sourceMark{{Type: "hris.payroll_run", ID: p.RunID, Part: 1, BusinessDate: &d, Key1: p.PeriodCode, Items: items}}})
	out.add(j, missing)
	return out, err
}

// onPayrollPaid clears the salaries payable against the bank (an explicit
// GL bank account code of the payment, else the default bank account).
func (m *Module) onPayrollPaid(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		RunID           uuid.UUID            `json:"runId"`
		Number          string               `json:"number"`
		PeriodCode      string               `json:"periodCode"`
		PaidOn          string               `json:"paidOn"`
		Reference       string               `json:"reference"`
		BankAccountCode string               `json:"bankAccountCode"`
		JournalLines    []payrollJournalLine `json:"journalLines"`
	}
	if err := ev.decode(&p); err != nil || p.RunID == uuid.Nil {
		return outcome{}, badPayload("payroll_paid payload")
	}
	d, err := time.Parse("2006-01-02", p.PaidOn)
	if err != nil {
		d = eventDate(ctx, tx, ev)
	}
	if err := ensurePayrollSetup(ctx, tx); err != nil {
		return outcome{}, err
	}
	items := payrollItems(ev.Type, p.RunID.String(), p.Number, p.JournalLines)
	if p.BankAccountCode != "" {
		for i := range items {
			items[i].CreditCode = p.BankAccountCode
		}
	}
	if len(items) == 0 {
		return outcome{Status: "no_posting", Note: "payroll run " + p.Number + " without net pay"}, nil
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.RunID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Payroll payment " + p.Number + " " + p.Reference},
		Sources: []sourceMark{{Type: "hris.payroll_run", ID: p.RunID, Part: 2, BusinessDate: &d, Key1: p.PeriodCode, Items: items}}})
	out.add(j, missing)
	return out, err
}
