package accounting

// EP-17 Posting Rules (FR-PST-01/06/08): a rule maps a posting source (an
// event or a source document type) and conditions on its attributes
// (business line, revenue component, payment method, purpose, movement type,
// property …) to a debit and a credit account. Accounts are concrete or a
// default-account role of the Accounting Configuration (@role), so the same
// rules serve every property. Rules are versioned by effective date; a
// journal keeps the rule it was posted with. Two-dimensional postings (e.g.
// payment method × purpose) are split into two items through the posting
// clearing account, which nets to zero inside the journal.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/id"
)

type postingRule struct {
	ID              uuid.UUID
	Code            string
	Source          string
	Conditions      map[string]any
	DebitAccountID  *uuid.UUID
	DebitRole       *string
	CreditAccountID *uuid.UUID
	CreditRole      *string
	Priority        int
}

// matches reports whether the rule applies to attrs and how specific it is.
func (r postingRule) matches(attrs map[string]string) (bool, int) {
	n := 0
	for k, v := range r.Conditions {
		got := attrs[k]
		switch c := v.(type) {
		case string:
			if c == "*" {
				continue
			}
			if !strings.EqualFold(got, c) {
				return false, 0
			}
		case bool:
			if got != strconv.FormatBool(c) {
				return false, 0
			}
		case []any:
			ok := false
			for _, x := range c {
				if strings.EqualFold(fmt.Sprint(x), got) {
					ok = true
					break
				}
			}
			if !ok {
				return false, 0
			}
		case nil:
			if got != "" {
				return false, 0
			}
		default:
			if fmt.Sprint(c) != got {
				return false, 0
			}
		}
		n++
	}
	return true, n
}

// Item is one posting fact: Amount is debited to the rule's debit account
// and credited to its credit account (a negative amount swaps the sides).
type Item struct {
	Source         string
	Attrs          map[string]string
	Amount         decimal.Decimal
	Description    string
	Dims           Dims
	DimSide        string // debit | credit | "" (both)
	SourceType     string
	SourceID       string
	FallbackDebit  string // role used when no rule matches (else suspense)
	FallbackCredit string
	DebitAccount   *uuid.UUID // explicit account (bypasses the rule for that side)
	CreditAccount  *uuid.UUID
	// DebitCode / CreditCode name an explicit account by code (the ledger
	// accounts of an item category, FR-INV-01/02): they take precedence over
	// the rule for that side; an unusable code is a missing_account.
	DebitCode  string
	CreditCode string
}

// Missing is an item without a usable mapping.
type Missing struct {
	Source string            `json:"source"`
	Attrs  map[string]string `json:"attrs"`
	Reason string            `json:"reason" enum:"missing_rule,missing_account"`
	Detail string            `json:"detail,omitempty"`
	Amount string            `json:"amount"`
}

type acctInfo struct {
	ID   uuid.UUID
	Type string
	Err  string
}

// resolver turns items into journal lines with the rules and the default
// accounts of one property at one date.
type resolver struct {
	tx       pgx.Tx
	ctx      context.Context
	property uuid.UUID
	date     time.Time
	cfg      AccountingConfiguration
	rules    map[string][]postingRule
	byCode   map[string]acctInfo
	byID     map[uuid.UUID]acctInfo
	taxCodes map[string]uuid.UUID // tax & service rule code → account
	missing  []Missing
	held     map[int]bool
}

func newResolver(ctx context.Context, tx pgx.Tx, property uuid.UUID, date time.Time) (*resolver, error) {
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	return &resolver{tx: tx, ctx: ctx, property: property, date: date, cfg: cfg, rules: map[string][]postingRule{}, byCode: map[string]acctInfo{},
		byID: map[uuid.UUID]acctInfo{}, held: map[int]bool{}}, nil
}

func (r *resolver) loadRules(source string) ([]postingRule, error) {
	if rs, ok := r.rules[source]; ok {
		return rs, nil
	}
	rows, err := r.tx.Query(r.ctx, `SELECT id, code, source, conditions, debit_account_id, debit_role, credit_account_id, credit_role, priority
		FROM accounting.posting_rules WHERE status = 'active' AND source = $1 AND effective_from <= $2 AND (effective_to IS NULL OR effective_to >= $2)
		ORDER BY priority, code`, source, r.date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []postingRule
	for rows.Next() {
		var p postingRule
		var raw []byte
		if err := rows.Scan(&p.ID, &p.Code, &p.Source, &raw, &p.DebitAccountID, &p.DebitRole, &p.CreditAccountID, &p.CreditRole, &p.Priority); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &p.Conditions)
		out = append(out, p)
	}
	r.rules[source] = out
	return out, rows.Err()
}

func (r *resolver) rule(source string, attrs map[string]string) (*postingRule, error) {
	rs, err := r.loadRules(source)
	if err != nil {
		return nil, err
	}
	var best *postingRule
	bestSpec := -1
	for i := range rs {
		ok, spec := rs[i].matches(attrs)
		if !ok {
			continue
		}
		if best == nil || rs[i].Priority < best.Priority || (rs[i].Priority == best.Priority && spec > bestSpec) {
			best, bestSpec = &rs[i], spec
		}
	}
	return best, nil
}

func (r *resolver) accountByCode(code string) (acctInfo, error) {
	if a, ok := r.byCode[code]; ok {
		return a, nil
	}
	var a acctInfo
	var status string
	var posting bool
	var props []uuid.UUID
	err := r.tx.QueryRow(r.ctx, `SELECT id, account_type, status, is_posting, properties FROM accounting.accounts WHERE code = $1`, code).
		Scan(&a.ID, &a.Type, &status, &posting, &props)
	if err != nil && err != pgx.ErrNoRows {
		return a, err
	}
	a = checkAccount(a, err == pgx.ErrNoRows, status, posting, props, r.property, code)
	r.byCode[code] = a
	return a, nil
}

func (r *resolver) accountByID(aid uuid.UUID) (acctInfo, error) {
	if a, ok := r.byID[aid]; ok {
		return a, nil
	}
	var a acctInfo
	var status, code string
	var posting bool
	var props []uuid.UUID
	err := r.tx.QueryRow(r.ctx, `SELECT id, code, account_type, status, is_posting, properties FROM accounting.accounts WHERE id = $1`, aid).
		Scan(&a.ID, &code, &a.Type, &status, &posting, &props)
	if err != nil && err != pgx.ErrNoRows {
		return a, err
	}
	a = checkAccount(a, err == pgx.ErrNoRows, status, posting, props, r.property, aid.String())
	r.byID[aid] = a
	return a, nil
}

func checkAccount(a acctInfo, missing bool, status string, posting bool, props []uuid.UUID, property uuid.UUID, ref string) acctInfo {
	switch {
	case missing:
		a.Err = "account " + ref + " does not exist"
	case status != "active":
		a.Err = "account " + ref + " is inactive"
	case !posting:
		a.Err = "account " + ref + " is a header account"
	case len(props) > 0 && !containsUUID(props, property):
		a.Err = "account " + ref + " is not used by this property"
	}
	return a
}

func containsUUID(list []uuid.UUID, v uuid.UUID) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// role resolves a default-account role of the Accounting Configuration.
func (r *resolver) role(role string) (acctInfo, error) {
	code := r.cfg.Accounts[strings.TrimPrefix(role, "@")]
	if code == "" {
		return acctInfo{Err: "no default account for role " + role}, nil
	}
	return r.accountByCode(code)
}

// side resolves one side of an item.
func (r *resolver) side(explicit *uuid.UUID, ruleAcct *uuid.UUID, ruleRole *string) (acctInfo, error) {
	switch {
	case explicit != nil:
		return r.accountByID(*explicit)
	case ruleAcct != nil:
		return r.accountByID(*ruleAcct)
	case ruleRole != nil && *ruleRole != "":
		return r.role(*ruleRole)
	}
	return acctInfo{Err: "no account"}, nil
}

// taxAccount maps a tax & service rule code to the account of its tax code.
func (r *resolver) taxAccount(ruleCode string) (*uuid.UUID, error) {
	if ruleCode == "" {
		return nil, nil
	}
	if r.taxCodes == nil {
		r.taxCodes = map[string]uuid.UUID{}
		rows, err := r.tx.Query(r.ctx, `SELECT code, rule_codes, account_id FROM accounting.tax_codes WHERE status = 'active' ORDER BY code`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var code string
			var rc []string
			var acct uuid.UUID
			if err := rows.Scan(&code, &rc, &acct); err != nil {
				rows.Close()
				return nil, err
			}
			for _, c := range append(rc, code) {
				if _, ok := r.taxCodes[strings.ToUpper(c)]; !ok {
					r.taxCodes[strings.ToUpper(c)] = acct
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if a, ok := r.taxCodes[strings.ToUpper(ruleCode)]; ok {
		return &a, nil
	}
	return nil, nil
}

// resolve turns items into lines. Items without a rule or account go to
// the suspense account (unmappedPosting = suspense) or are held back
// (hold: r.held[i]); both are reported in r.missing.
func (r *resolver) resolve(items []Item) ([]Line, error) {
	var out []Line
	for i, it := range items {
		if it.Amount.IsZero() {
			continue
		}
		// Tax Configuration (FR-REV-04): a tax or service line of a tax &
		// service rule mapped to a tax code goes to the tax code's account.
		if tc := it.Attrs["taxCode"]; tc != "" && it.CreditAccount == nil && (it.Attrs["part"] == "tax" || it.Attrs["part"] == "service") {
			acct, err := r.taxAccount(tc)
			if err != nil {
				return nil, err
			}
			it.CreditAccount = acct
		}
		// Explicit account codes (category accounts) come before the rules.
		var codeErr string
		for _, x := range []struct {
			code string
			acct **uuid.UUID
		}{{it.DebitCode, &it.DebitAccount}, {it.CreditCode, &it.CreditAccount}} {
			if x.code == "" || *x.acct != nil {
				continue
			}
			a, err := r.accountByCode(x.code)
			if err != nil {
				return nil, err
			}
			if a.Err != "" {
				codeErr = a.Err
				continue
			}
			id := a.ID
			*x.acct = &id
		}
		var rl *postingRule
		var err error
		if it.DebitAccount == nil || it.CreditAccount == nil {
			if rl, err = r.rule(it.Source, it.Attrs); err != nil {
				return nil, err
			}
		}
		var dr, cr acctInfo
		var reason, detail string
		if rl == nil && (it.DebitAccount == nil || it.CreditAccount == nil) {
			reason, detail = "missing_rule", "no posting rule for "+it.Source
			if dr, err = r.fallback(it.DebitAccount, it.FallbackDebit); err != nil {
				return nil, err
			}
			if cr, err = r.fallback(it.CreditAccount, it.FallbackCredit); err != nil {
				return nil, err
			}
		} else {
			var da, ca *uuid.UUID
			var drole, crole *string
			if rl != nil {
				da, drole, ca, crole = rl.DebitAccountID, rl.DebitRole, rl.CreditAccountID, rl.CreditRole
			}
			if dr, err = r.side(it.DebitAccount, da, drole); err != nil {
				return nil, err
			}
			if cr, err = r.side(it.CreditAccount, ca, crole); err != nil {
				return nil, err
			}
			for _, x := range []acctInfo{dr, cr} {
				if x.Err != "" {
					reason, detail = "missing_account", x.Err
				}
			}
		}
		if codeErr != "" {
			// the configured category account is unusable: the side it names
			// goes to suspense (or is held), never silently to the default
			reason, detail = "missing_account", codeErr
			if it.DebitCode != "" && it.DebitAccount == nil {
				dr = acctInfo{Err: codeErr}
			}
			if it.CreditCode != "" && it.CreditAccount == nil {
				cr = acctInfo{Err: codeErr}
			}
		}
		if reason != "" {
			r.missing = append(r.missing, Missing{Source: it.Source, Attrs: it.Attrs, Reason: reason, Detail: detail, Amount: money(it.Amount)})
			if r.cfg.UnmappedPosting == "hold" {
				r.held[i] = true
				continue
			}
			susp, err := r.role("suspense")
			if err != nil {
				return nil, err
			}
			if susp.Err != "" {
				return nil, errf("suspense account unavailable: %s", susp.Err)
			}
			if dr.Err != "" || dr.ID == uuid.Nil {
				dr = susp
			}
			if cr.Err != "" || cr.ID == uuid.Nil {
				cr = susp
			}
		}
		if dr.ID == cr.ID {
			continue // no effect (e.g. folio transfer, transfer between locations of one account)
		}
		amt := it.Amount
		if amt.IsNegative() {
			dr, cr, amt = cr, dr, amt.Neg()
		}
		var ruleID *uuid.UUID
		if rl != nil {
			ruleID = &rl.ID
		}
		dl := Line{AccountID: dr.ID, Debit: amt, Description: it.Description, RuleID: ruleID, SourceType: it.SourceType, SourceID: it.SourceID}
		cl := Line{AccountID: cr.ID, Credit: amt, Description: it.Description, RuleID: ruleID, SourceType: it.SourceType, SourceID: it.SourceID}
		switch it.DimSide {
		case "debit":
			dl.Dims = it.Dims
			cl.Dims = Dims{PartnerType: it.Dims.PartnerType, PartnerID: it.Dims.PartnerID, PartnerName: it.Dims.PartnerName}
		case "credit":
			cl.Dims = it.Dims
		default:
			dl.Dims, cl.Dims = it.Dims, it.Dims
		}
		if it.Amount.IsNegative() && it.DimSide != "" {
			dl.Dims, cl.Dims = cl.Dims, dl.Dims
		}
		out = append(out, dl, cl)
	}
	return out, nil
}

func (r *resolver) fallback(explicit *uuid.UUID, role string) (acctInfo, error) {
	if explicit != nil {
		return r.accountByID(*explicit)
	}
	if role == "" {
		return acctInfo{Err: "no rule"}, nil
	}
	return r.role(role)
}

// ── default rules (FR-PST-08) ─────────────────────────────────────────────

// componentAccounts maps the revenue components of the Accounting Export
// (billing RevenueComponents, P1 charge types and the all-in components of
// the rate card) to the template accounts; @role entries are liabilities.
var componentAccounts = map[string]string{
	"green_fee": "4110", "golf_round": "4110", "caddy_fee": "@caddy_fee_payable", "caddy_tip": "@caddy_fee_payable",
	"buggy_fee": "4120", "buggy_surcharge": "4120", "cart_fee": "4120", "extra_cart": "4120",
	"hio": "4140", "hio_insurance": "4140", "golf_other": "4150", "water": "4150", "bag_storage": "4150",
	"driving_range": "4130", "tournament_fee": "4160", "sponsorship": "4160",
	"sport_entry": "4210", "facility_entry": "4210", "court": "4220", "class": "4230", "registration_fee": "4230", "locker": "4240",
	"bungalow": "4310", "vip_suite": "4320", "meeting": "4330", "equipment": "4330",
	"fnb": "4410", "banquet_package": "4420", "banquet_fnb": "4420", "event_fee": "4420", "corkage": "4420",
	"venue_rental": "4430", "outdoor_venue": "4430", "electricity": "4430",
	"pro_shop":       "4510",
	"membership_fee": "4610", "membership_annual_fee": "@deferred_clearing", "renewal_fee": "4630", "card_replacement_fee": "4630",
	"reactivation_fee": "4630", "nominee_fee": "4630",
	"voucher_deferred": "@deferred_clearing", "package": "@deferred_clearing", "deposit": "@customer_deposits",
	"gateway_fee": "4890", // payment gateway service fee charged to the guest (Sport Club FR-93)
	"breakage":    "4810", "cancellation_fee": "4820", "no_show_fee": "4820", "damage_charge": "4820", "late_checkout_fee": "4820",
	"discount": "4910", "promotion_discount": "4910", "rain_check_credit": "4910", "loyalty_redemption": "@loyalty_liability",
	"other": "4890",
}

// recognitionAccounts overrides componentAccounts for deferred revenue
// recognition (the revenue account once the liability is earned).
var recognitionAccounts = map[string]string{"membership_annual_fee": "4620", "package": "4700", "voucher_deferred": "4890", "deposit": "4890"}

var methodRoles = map[string]string{"cash": "cash", "card": "card_clearing", "qris": "gateway_clearing", "virtual_account": "gateway_clearing",
	"payment_gateway": "gateway_clearing", "bank_transfer": "bank", "member_account": "ar_unbilled", "voucher_prepaid": "deferred_clearing",
	"loyalty_points": "loyalty_liability", "folio_transfer": "guest_ledger"}

var purposeRoles = map[string]string{"settlement": "guest_ledger", "deposit": "customer_deposits", "account_settlement": "ar_unbilled"}

var liabilityRoles = map[string]string{"voucher": "deferred_voucher", "prepaid": "deferred_prepaid", "annual_fee": "deferred_annual_fee",
	"package": "deferred_package"}

type ruleSpec struct {
	Code, Name, Source string
	Cond               map[string]any
	Debit, Credit      string // @role or account code
	Priority           int
}

func defaultRuleSpecs() []ruleSpec {
	var out []ruleSpec
	add := func(code, name, source string, cond map[string]any, debit, credit string, prio int) {
		out = append(out, ruleSpec{Code: code, Name: name, Source: source, Cond: cond, Debit: debit, Credit: credit, Priority: prio})
	}
	comps := make([]string, 0, len(componentAccounts))
	for c := range componentAccounts {
		comps = append(comps, c)
	}
	sort.Strings(comps)
	for _, c := range comps {
		acct := componentAccounts[c]
		up := strings.ToUpper(c)
		add("DEF-LINE-"+up, "Charge "+c, "billing.folio_line", map[string]any{"part": "net", "component": c}, "@guest_ledger", acct, 100)
		add("DEF-RC-"+up, "Charge (line component) "+c, "billing.folio_line", map[string]any{"part": "net", "revenueComponent": c}, "@guest_ledger", acct, 200)
		rec := acct
		if r, ok := recognitionAccounts[c]; ok {
			rec = r
		}
		if !strings.HasPrefix(rec, "@") {
			add("DEF-REC-"+up, "Deferred revenue recognised "+c, "billing.deferred_recognition", map[string]any{"revenueComponent": c}, "@posting_clearing", rec, 100)
		}
	}
	add("DEF-LINE-SERVICE", "Service charge", "billing.folio_line", map[string]any{"part": "service"}, "@guest_ledger", "@service_charge_payable", 100)
	add("DEF-LINE-TAX-PB1", "Local tax (PBJT/PB1)", "billing.folio_line", map[string]any{"part": "tax", "businessLine": []any{"pos", "stay", "banquet", "sportclub"}},
		"@guest_ledger", "@pb1_payable", 100)
	add("DEF-LINE-TAX", "VAT output", "billing.folio_line", map[string]any{"part": "tax"}, "@guest_ledger", "@ppn_output", 900)
	for m, role := range methodRoles {
		add("DEF-PAY-M-"+strings.ToUpper(m), "Payment by "+m, "billing.payment_method", map[string]any{"methodType": m}, "@"+role, "@posting_clearing", 100)
		add("DEF-REF-M-"+strings.ToUpper(m), "Refund to "+m, "billing.refund_method", map[string]any{"methodType": m}, "@posting_clearing", "@"+role, 100)
	}
	add("DEF-REF-M-ACCOUNT", "Refund to the member account", "billing.refund_method", map[string]any{"destination": "member_account"}, "@posting_clearing",
		"@ar_unbilled", 50)
	for p, role := range purposeRoles {
		add("DEF-PAY-P-"+strings.ToUpper(p), "Payment purpose "+p, "billing.payment_purpose", map[string]any{"purpose": p}, "@posting_clearing", "@"+role, 100)
		add("DEF-REF-P-"+strings.ToUpper(p), "Refund of purpose "+p, "billing.refund_purpose", map[string]any{"purpose": p}, "@"+role, "@posting_clearing", 100)
	}
	add("DEF-DEP-APPLY", "Deposit applied to the folio", "billing.deposit_application", nil, "@customer_deposits", "@guest_ledger", 100)
	add("DEF-PAYOUT-CADDY", "Caddy fee settlement paid", "billing.payout", map[string]any{"part": "payable", "payoutType": "caddy_fee_settlement"},
		"@caddy_fee_payable", "@posting_clearing", 100)
	add("DEF-PAYOUT-INSTR", "Instructor fee paid", "billing.payout", map[string]any{"part": "payable", "payoutType": "instructor_fee"},
		"@instructor_fee_payable", "@posting_clearing", 100)
	add("DEF-PAYOUT-DEDUCT", "Caddy settlement deduction", "billing.payout", map[string]any{"part": "deduction"}, "@caddy_fee_payable",
		"@caddy_deduction_income", 100)
	add("DEF-PAYOUT-CASH", "Payout in cash", "billing.payout_method", map[string]any{"methodType": "cash"}, "@posting_clearing", "@cash", 100)
	add("DEF-PAYOUT-BANK", "Payout by transfer", "billing.payout_method", map[string]any{"methodType": "bank_transfer"}, "@posting_clearing", "@bank", 100)
	for t, role := range liabilityRoles {
		up := strings.ToUpper(t)
		add("DEF-DEF-DEFER-"+up, "Deferral "+t, "billing.deferred_revenue", map[string]any{"entryType": []any{"deferral", "adjustment"}, "liabilityType": t},
			"@deferred_clearing", "@"+role, 100)
		add("DEF-DEF-RECOG-"+up, "Recognition "+t, "billing.deferred_revenue", map[string]any{"entryType": "recognition", "liabilityType": t},
			"@"+role, "@posting_clearing", 100)
		add("DEF-DEF-BREAK-"+up, "Breakage "+t, "billing.deferred_revenue", map[string]any{"entryType": "breakage", "liabilityType": t},
			"@"+role, "@breakage_income", 100)
		add("DEF-DEF-REVERSE-"+up, "Reversal "+t, "billing.deferred_revenue", map[string]any{"entryType": "reversal", "liabilityType": t},
			"@"+role, "@deferred_clearing", 100)
	}
	add("DEF-REC-TENDER", "Voucher used as tender (revenue on the folio)", "billing.deferred_recognition", map[string]any{"viaTender": "true"},
		"@posting_clearing", "@deferred_clearing", 10)
	add("DEF-INV-ISSUE", "Invoice issued (city ledger → AR)", "billing.invoice_issued", nil, "@ar_control", "@ar_unbilled", 100)
	add("DEF-INV-ALLOC", "Payment allocated to an invoice", "billing.allocation", nil, "@ar_unbilled", "@ar_control", 100)
	add("DEF-INV-VOID", "Invoice voided", "billing.invoice_voided", nil, "@ar_unbilled", "@ar_control", 100)
	add("DEF-CN-NET", "Credit note", "billing.credit_note_issued", map[string]any{"part": "net"}, "@sales_allowance", "@ar_control", 100)
	add("DEF-CN-SVC", "Credit note service", "billing.credit_note_issued", map[string]any{"part": "service"}, "@service_charge_payable", "@ar_control", 100)
	add("DEF-CN-TAX", "Credit note tax", "billing.credit_note_issued", map[string]any{"part": "tax"}, "@ppn_output", "@ar_control", 100)
	add("DEF-WO-ALLOW", "Write-off against the allowance", "billing.invoice_written_off", map[string]any{"coveredByAllowance": "true"},
		"@allowance_doubtful", "@ar_control", 100)
	add("DEF-WO-EXP", "Write-off to bad debt expense", "billing.invoice_written_off", map[string]any{"coveredByAllowance": "false"},
		"@bad_debt_expense", "@ar_control", 100)
	add("DEF-LOY-EARN", "Loyalty points earned", "crm.loyalty_points_changed", map[string]any{"kind": "earned"}, "@loyalty_expense", "@loyalty_liability", 100)
	add("DEF-LOY-ADJ", "Loyalty points adjusted", "crm.loyalty_points_changed", map[string]any{"kind": "adjusted"}, "@loyalty_expense", "@loyalty_liability", 100)
	add("DEF-LOY-EXP", "Loyalty points expired (breakage)", "crm.loyalty_points_changed", map[string]any{"kind": "expired"}, "@loyalty_liability",
		"@breakage_income", 100)
	add("DEF-LOY-REV", "Loyalty points reversed", "crm.loyalty_points_changed", map[string]any{"kind": "reversed"}, "@loyalty_liability",
		"@loyalty_expense", 100)
	add("DEF-INSTR-FEE", "Instructor fee approved", "sportclub.instructor_fee_approved", nil, "@instructor_fee_expense", "@instructor_fee_payable", 100)
	add("DEF-INV-RECEIPT", "Stock received", "inventory.movement_posted", map[string]any{"movementType": []any{"receipt", "return_out"}}, "@inventory", "@grni", 100)
	add("DEF-INV-COGS-PRO", "Pro shop cost of sales", "inventory.movement_posted", map[string]any{"movementType": []any{"issue", "consumption"},
		"costCenter": "pro_shop"}, "@inventory", "5120", 50)
	add("DEF-INV-COGS-BQT", "Banquet cost of sales", "inventory.movement_posted", map[string]any{"movementType": []any{"issue", "consumption"},
		"sourceType": "banquet"}, "@inventory", "5140", 60)
	add("DEF-INV-COGS", "Cost of sales / consumption", "inventory.movement_posted", map[string]any{"movementType": []any{"issue", "consumption"}},
		"@inventory", "@cogs", 100)
	add("DEF-INV-OPENING", "Opening stock (balance sheet: opening balance equity, never P&L)", "inventory.movement_posted",
		map[string]any{"sourceType": "opening_stock"}, "@inventory", "@opening_balance_equity", 20)
	add("DEF-INV-ADJ", "Stock adjustment / opname variance", "inventory.movement_posted", map[string]any{"movementType": []any{"adjustment", "opname"}},
		"@inventory", "@inventory_variance", 100)
	add("DEF-INV-WASTE", "Waste / spoilage", "inventory.movement_posted", map[string]any{"movementType": "waste"}, "@inventory", "@waste_expense", 100)
	add("DEF-INV-PROD", "Production", "inventory.movement_posted", map[string]any{"movementType": []any{"production_in", "production_out"}}, "@inventory", "@wip", 100)
	add("DEF-INV-TRANSFER", "Transfer between locations", "inventory.movement_posted", map[string]any{"movementType": []any{"transfer_in", "transfer_out"}},
		"@inventory", "@inventory", 100)
	add("DEF-REVAL-STOCK", "Invoice price variance on stock", "inventory.revaluation_posted", map[string]any{"part": "stock"}, "@inventory", "@grni", 100)
	add("DEF-REVAL-PPV", "Invoice price variance consumed (price variance)", "inventory.revaluation_posted",
		map[string]any{"part": "consumed", "consumedTo": "price_variance"}, "@inventory_variance", "@grni", 90)
	add("DEF-REVAL-COGS", "Invoice price variance consumed (cost of sales)", "inventory.revaluation_posted", map[string]any{"part": "consumed"},
		"@cogs", "@grni", 100)
	add("DEF-ASSET-DEPR", "Asset depreciation", "inventory.asset_depreciated", nil, "@depreciation_expense", "@accumulated_depreciation", 100)
	add("DEF-ASSET-DISP-COST", "Asset disposal – cost derecognised", "inventory.asset_disposed", map[string]any{"part": "cost"}, "@posting_clearing",
		"@fixed_asset", 100)
	add("DEF-ASSET-DISP-ACC", "Asset disposal – accumulated depreciation", "inventory.asset_disposed", map[string]any{"part": "accumulated"},
		"@accumulated_depreciation", "@posting_clearing", 100)
	add("DEF-ASSET-DISP-PROCEEDS", "Asset disposal – proceeds receivable", "inventory.asset_disposed", map[string]any{"part": "proceeds"}, "@ar_other",
		"@posting_clearing", 100)
	add("DEF-ASSET-DISP-GAIN", "Asset disposal – gain", "inventory.asset_disposed", map[string]any{"part": "gain"}, "@posting_clearing",
		"@asset_disposal_gain", 100)
	add("DEF-ASSET-DISP-LOSS", "Asset disposal – loss", "inventory.asset_disposed", map[string]any{"part": "loss"}, "@asset_disposal_loss",
		"@posting_clearing", 100)
	add("DEF-COMMISSION", "Sales commission approved", "crm.commission_approved", nil, "@commission_expense", "@commission_payable", 100)
	add("DEF-CONSIGN", "Consignment item sold", "inventory.consignment_sold", nil, "@cogs_consignment", "@consignment_payable", 100)
	add("DEF-GR", "Goods received (GRNI)", "procurement.goods_received", nil, "@inventory", "@grni", 100)
	add("DEF-PRET", "Purchase return", "procurement.purchase_returned", nil, "@grni", "@inventory", 100)
	add("DEF-VI-INV", "Vendor invoice – stock (clears GRNI)", "procurement.vendor_invoice_line", map[string]any{"accountHint": "inventory"}, "@grni",
		"@posting_clearing", 100)
	add("DEF-VI-EXP", "Vendor invoice – expense", "procurement.vendor_invoice_line", map[string]any{"accountHint": "expense"}, "@general_expense",
		"@posting_clearing", 100)
	add("DEF-VI-ASSET", "Vendor invoice – asset", "procurement.vendor_invoice_line", map[string]any{"accountHint": "asset"}, "@fixed_asset",
		"@posting_clearing", 100)
	add("DEF-VI-TAX", "Vendor invoice – VAT input", "procurement.vendor_invoice_tax", nil, "@ppn_input", "@posting_clearing", 100)
	add("DEF-VI-WHT", "Vendor invoice – withholding tax", "procurement.vendor_invoice_withholding", nil, "@posting_clearing", "@pph_payable", 100)
	add("DEF-VI-AP", "Vendor invoice – payable", "procurement.vendor_invoice_payable", nil, "@posting_clearing", "@ap_control", 100)
	add("DEF-DN", "Debit note", "procurement.debit_note_issued", nil, "@ap_control", "@grni", 100)
	add("DEF-VPAY", "Vendor payment", "accounting.vendor_payment", nil, "@ap_control", "@posting_clearing", 100)
	add("DEF-GW-SETTLE", "Payment gateway settlement to the bank", "billing.gateway_settlement", nil, "@bank", "@gateway_clearing", 100)
	add("DEF-GW-FEE", "Payment gateway fees", "billing.gateway_fee", nil, "@bank_charges", "@gateway_clearing", 100)
	add("DEF-SHIFT-VAR", "Cash over / short of a closed shift", "billing.shift_variance", nil, "@cash", "@cash_over_short", 100)
	return append(out, extraRuleSpecs...)
}

// extraRuleSpecs are the default rules added by later areas in their own
// files (e.g. p5_payout_post.go).
var extraRuleSpecs []ruleSpec

// GenerateDefaultRules creates the default posting rules that do not exist
// yet (by code); returns the number created.
func GenerateDefaultRules(ctx context.Context, tx pgx.Tx) (int, error) {
	codes := map[string]uuid.UUID{}
	rows, err := tx.Query(ctx, `SELECT code, id FROM accounting.accounts`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var c string
		var aid uuid.UUID
		if err := rows.Scan(&c, &aid); err != nil {
			rows.Close()
			return 0, err
		}
		codes[c] = aid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	uid := actorPtr(ctx)
	for _, s := range defaultRuleSpecs() {
		cond := s.Cond
		if cond == nil {
			cond = map[string]any{}
		}
		raw, _ := json.Marshal(cond)
		var da, ca *uuid.UUID
		var dr, cr *string
		for _, x := range []struct {
			v    string
			acct **uuid.UUID
			role **string
		}{{s.Debit, &da, &dr}, {s.Credit, &ca, &cr}} {
			if strings.HasPrefix(x.v, "@") {
				r := strings.TrimPrefix(x.v, "@")
				*x.role = &r
			} else if aid, ok := codes[x.v]; ok {
				a := aid
				*x.acct = &a
			} else {
				r := "suspense"
				*x.role = &r
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO accounting.posting_rules (id, code, name, source, conditions, debit_account_id, debit_role, credit_account_id,
			credit_role, priority, description, created_by, updated_by)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'Generated from the Accounting Export components',$11,$11
			WHERE NOT EXISTS (SELECT 1 FROM accounting.posting_rules WHERE code = $2)`,
			id.New(), s.Code, s.Name, s.Source, raw, da, dr, ca, cr, s.Priority, uid)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// Roles lists the default-account roles (Accounting Configuration).
func Roles() []string {
	out := make([]string, 0, len(DefaultAccounts))
	for k := range DefaultAccounts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
