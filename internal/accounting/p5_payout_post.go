package accounting

// PRD P5 payouts & distributions (EP-11, EP-13, EP-14; contract H5),
// additive: the accounts, default posting rules and subscribers of the HRIS
// payout events. Payloads are decoded into local structs (no import of
// hris).
//
//   hris.service_charge_distributed  Dr service charge payable (the month's pool)
//                                    Cr service charge distribution payable (paid by payroll: the payroll
//                                       journal debits it for the SERVICE_CHARGE component)
//                                    Cr service charge reserve (breakage & loss + undistributed)
//                                    Cr rounding (Service Charge Policy account, else the rounding role)
//   hris.payout_posted               Dr caddy fee liability (caddy) / instructor fee payable (instructor)
//                                    Cr partner payout payable (net), PPh 21 non-employee payable,
//                                       BPJS BPU payable, deduction income
//   hris.payout_paid                 Dr partner payout payable, Cr bank / cash
//
// Commission (crm.commission_approved, P4) stays accrued in the commission
// payable until the payroll journal pays it.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// P5 payout events consumed by accounting.
const (
	EventServiceChargeDistributed = "hris.service_charge_distributed"
	EventPayoutPosted             = "hris.payout_posted"
	EventPayoutPaid               = "hris.payout_paid"
)

func init() {
	// accounts of the club & hospitality template (added when the chart is
	// loaded again: Accounting → Setup → Load template is idempotent); the
	// payroll area (p5_payroll_post.go) adds 2126 too, so a code is added once
	have := map[string]bool{}
	for _, t := range Template {
		have[t.Code] = true
	}
	for _, t := range []tplAccount{
		a("2126", "2100", "Service Charge Distribution Payable", "Utang Distribusi Service Charge", "liability", "accrued", "operating"),
		a("2127", "2100", "Service Charge Reserve (Breakage & Loss)", "Cadangan Service Charge (Breakage & Loss)", "liability", "accrued", "operating"),
		a("2135", "2100", "PPh 21 Payable – Non-employees", "Utang PPh 21 Bukan Pegawai", "liability", "tax", "operating"),
		a("2136", "2100", "BPJS Ketenagakerjaan BPU Payable", "Utang Iuran BPJS Ketenagakerjaan BPU", "liability", "accrued", "operating"),
		a("2174", "2100", "Partner Payouts Payable", "Utang Payout Mitra (Caddy & Instruktur)", "liability", "payable", "operating"),
	} {
		if !have[t.Code] {
			have[t.Code] = true
			Template = append(Template, t)
		}
	}
	for role, code := range map[string]string{"service_charge_distribution_payable": "2126", "service_charge_reserve": "2127",
		"pph21_partner_payable": "2135", "bpu_payable": "2136", "partner_payout_payable": "2174"} {
		if DefaultAccounts[role] == "" {
			DefaultAccounts[role] = code
		}
	}
	ConsumedEvents = append(ConsumedEvents, EventServiceChargeDistributed, EventPayoutPosted, EventPayoutPaid)
	extraHandlers[EventServiceChargeDistributed] = func(m *Module) evHandler { return m.onServiceChargeDistributed }
	extraHandlers[EventPayoutPosted] = func(m *Module) evHandler { return m.onPayoutPosted }
	extraHandlers[EventPayoutPaid] = func(m *Module) evHandler { return m.onPayoutPaid }
	add := func(code, name, source string, cond map[string]any, debit, credit string) {
		extraRuleSpecs = append(extraRuleSpecs, ruleSpec{Code: code, Name: name, Source: source, Cond: cond, Debit: debit, Credit: credit, Priority: 100})
	}
	sc := EventServiceChargeDistributed
	add("DEF-SVC-DIST", "Service charge distributed to employees (payroll)", sc, map[string]any{"part": "distributed"}, "@service_charge_payable",
		"@service_charge_distribution_payable")
	add("DEF-SVC-RESERVE", "Service charge reserve (breakage & loss)", sc, map[string]any{"part": "reserve"}, "@service_charge_payable",
		"@service_charge_reserve")
	add("DEF-SVC-ROUNDING", "Service charge distribution rounding", sc, map[string]any{"part": "rounding"}, "@service_charge_payable", "@rounding")
	for _, k := range []struct{ kind, up, liability, deduction string }{
		{"caddy", "CADDY", "@caddy_fee_payable", "@caddy_deduction_income"},
		{"instructor", "INSTR", "@instructor_fee_payable", "@revenue_other"},
	} {
		add("DEF-PAYOUT-RUN-"+k.up+"-NET", "Payout run "+k.kind+" – net payable", EventPayoutPosted, map[string]any{"kind": k.kind, "part": "net"},
			k.liability, "@partner_payout_payable")
		add("DEF-PAYOUT-RUN-"+k.up+"-PPH21", "Payout run "+k.kind+" – PPh 21 non-employee", EventPayoutPosted, map[string]any{"kind": k.kind,
			"part": "pph21"}, k.liability, "@pph21_partner_payable")
		add("DEF-PAYOUT-RUN-"+k.up+"-BPU", "Payout run "+k.kind+" – BPJS Ketenagakerjaan BPU", EventPayoutPosted, map[string]any{"kind": k.kind,
			"part": "bpu"}, k.liability, "@bpu_payable")
		add("DEF-PAYOUT-RUN-"+k.up+"-DEDUCT", "Payout run "+k.kind+" – deductions", EventPayoutPosted, map[string]any{"kind": k.kind, "part": "deduction"},
			k.liability, k.deduction)
	}
	add("DEF-PAYOUT-RUN-PAID-BANK", "Payout run paid by transfer", EventPayoutPaid, map[string]any{"methodType": "bank_transfer"}, "@partner_payout_payable",
		"@bank")
	add("DEF-PAYOUT-RUN-PAID-CASH", "Payout run paid in cash", EventPayoutPaid, map[string]any{"methodType": "cash"}, "@partner_payout_payable", "@cash")
}

// onServiceChargeDistributed moves an approved distribution out of the
// service charge liability (the pool of the month becomes nil there):
// distributed → distribution payable (payroll), reserve + undistributed →
// reserve, rounding → the configured account (FR-SVC-04).
func (m *Module) onServiceChargeDistributed(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		DistributionID      uuid.UUID `json:"distributionId"`
		Number              string    `json:"number"`
		Year                int       `json:"year"`
		Month               int       `json:"month"`
		Collected           string    `json:"collected"`
		Reserve             string    `json:"reserve"`
		Distributed         string    `json:"distributed"`
		Undistributed       string    `json:"undistributed"`
		RoundingDifference  string    `json:"roundingDifference"`
		RoundingAccountCode string    `json:"roundingAccountCode"`
	}
	if err := ev.decode(&p); err != nil || p.DistributionID == uuid.Nil {
		return outcome{}, badPayload("service_charge_distributed payload")
	}
	dist, reserve, rounding := dec(p.Distributed), dec(p.Reserve).Add(dec(p.Undistributed)), dec(p.RoundingDifference)
	if dist.IsZero() && reserve.IsZero() && rounding.IsZero() {
		return outcome{Status: "no_posting", Note: "nothing distributed"}, nil
	}
	d := eventDate(ctx, tx, ev)
	desc := "Service charge distribution " + p.Number
	dims := Dims{PartnerType: "service_charge_payout"} // excluded from the next pool computation
	item := func(part string, amt decimal.Decimal) Item {
		return Item{Source: ev.Type, Attrs: map[string]string{"part": part}, Amount: amt, Description: desc + " – " + part, Dims: dims,
			SourceType: "hris.service_charge_distribution", SourceID: p.DistributionID.String()}
	}
	var items []Item
	if dist.IsPositive() {
		items = append(items, item("distributed", dist))
	}
	if reserve.IsPositive() {
		items = append(items, item("reserve", reserve))
	}
	if rounding.IsPositive() {
		it := item("rounding", rounding)
		it.CreditCode = strings.TrimSpace(p.RoundingAccountCode)
		items = append(items, it)
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.DistributionID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: desc},
		Sources: []sourceMark{{Type: "hris.service_charge_distribution", ID: p.DistributionID, BusinessDate: &d, Amount: dec(p.Collected), Items: items}}})
	out.add(j, missing)
	return out, err
}

type payoutLinePayload struct {
	PartnerID        uuid.UUID `json:"partnerId"`
	PartnerName      string    `json:"partnerName"`
	Gross            string    `json:"gross"`
	SourceDeductions string    `json:"sourceDeductions"`
	PPh21            string    `json:"pph21"`
	BPU              string    `json:"bpu"`
	OtherDeductions  string    `json:"otherDeductions"`
	Net              string    `json:"net"`
}

// onPayoutPosted clears the caddy fee / instructor fee liability of an
// approved payout run per partner.
func (m *Module) onPayoutPosted(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		RunID  uuid.UUID           `json:"runId"`
		Number string              `json:"number"`
		Kind   string              `json:"kind"`
		Gross  string              `json:"gross"`
		Lines  []payoutLinePayload `json:"lines"`
	}
	if err := ev.decode(&p); err != nil || p.RunID == uuid.Nil || (p.Kind != "caddy" && p.Kind != "instructor") {
		return outcome{}, badPayload("payout_posted payload")
	}
	d := eventDate(ctx, tx, ev)
	var items []Item
	for _, l := range p.Lines {
		pid := l.PartnerID
		dims := Dims{PartnerType: p.Kind, PartnerID: &pid, PartnerName: l.PartnerName}
		for _, part := range []struct {
			name string
			amt  decimal.Decimal
		}{{"net", dec(l.Net)}, {"pph21", dec(l.PPh21)}, {"bpu", dec(l.BPU)}, {"deduction", dec(l.SourceDeductions).Add(dec(l.OtherDeductions))}} {
			if !part.amt.IsPositive() {
				continue
			}
			items = append(items, Item{Source: ev.Type, Attrs: map[string]string{"kind": p.Kind, "part": part.name}, Amount: part.amt,
				Description: "Payout " + p.Number + " " + l.PartnerName + " – " + part.name, Dims: dims, SourceType: "hris.payout_run",
				SourceID: p.RunID.String()})
		}
	}
	if len(items) == 0 {
		return outcome{Status: "no_posting", Note: "payout run " + p.Number + " without amount"}, nil
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.RunID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Payout run " + p.Number + " (" + p.Kind + ")"},
		Sources: []sourceMark{{Type: "hris.payout_run", ID: p.RunID, BusinessDate: &d, Key1: "posted", Amount: dec(p.Gross), Items: items}}})
	out.add(j, missing)
	return out, err
}

// onPayoutPaid clears the partner payout payable against the bank (bank
// file) and the cash paid out.
func (m *Module) onPayoutPaid(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		RunID      uuid.UUID `json:"runId"`
		Number     string    `json:"number"`
		Kind       string    `json:"kind"`
		Net        string    `json:"net"`
		BankAmount string    `json:"bankAmount"`
		CashAmount string    `json:"cashAmount"`
	}
	if err := ev.decode(&p); err != nil || p.RunID == uuid.Nil {
		return outcome{}, badPayload("payout_paid payload")
	}
	d := eventDate(ctx, tx, ev)
	var items []Item
	for _, x := range []struct {
		method string
		amt    decimal.Decimal
	}{{"bank_transfer", dec(p.BankAmount)}, {"cash", dec(p.CashAmount)}} {
		if x.amt.IsPositive() {
			items = append(items, Item{Source: ev.Type, Attrs: map[string]string{"methodType": x.method, "kind": p.Kind}, Amount: x.amt,
				Description: "Payout " + p.Number + " paid (" + x.method + ")", Dims: Dims{PartnerType: p.Kind}, SourceType: "hris.payout_run",
				SourceID: p.RunID.String()})
		}
	}
	if len(items) == 0 {
		return outcome{Status: "no_posting", Note: "payout run " + p.Number + " without amount"}, nil
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.RunID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Payout run " + p.Number + " paid"},
		Sources: []sourceMark{{Type: "hris.payout_run", ID: p.RunID, Part: 1, BusinessDate: &d, Key1: "paid", Amount: dec(p.Net), Items: items}}})
	out.add(j, missing)
	return out, err
}
