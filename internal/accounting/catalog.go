package accounting

// Catalogue contribution of Accounting: permissions per object and their
// role templates (Product Overview §44: Finance Manager, Accountant, General
// Manager; an external Auditor role is created in Roles & Permissions with
// the *.view permissions, FR-ACC-09), the approval document types and the
// notification templates.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Permissions of Accounting.
func Permissions() []catalog.Permission {
	out := resource.Permissions(Defs()...)
	for _, g := range [][]catalog.Permission{
		catalog.P("accounting", "setup", "view", "manage", "sign_off"),
		catalog.P("accounting", "journal", "view", "create", "reverse"),
		catalog.P("accounting", "ledger", "view"),
		catalog.P("accounting", "period", "view", "close", "reopen"),
		catalog.P("accounting", "posting", "view", "manage"),
		catalog.P("accounting", "receivable", "view", "manage"),
		catalog.P("accounting", "payable", "view", "manage"),
		catalog.P("accounting", "payment_run", "view", "create", "execute"),
		catalog.P("accounting", "bank_transaction", "view", "import", "reconcile"),
		catalog.P("accounting", "cash", "view", "manage"),
		catalog.P("accounting", "tax_invoice", "view", "manage"),
		catalog.P("accounting", "revenue", "view", "manage"),
		catalog.P("accounting", "report", "view"),
		catalog.P("accounting", "opening_balance", "view", "manage", "post"),
	} {
		out = append(out, g...)
	}
	return out
}

// viewPermissions are the read permissions (management, auditors).
func viewPermissions() []string {
	var out []string
	for _, p := range Permissions() {
		if strings.HasSuffix(p.Code, ".view") || strings.HasSuffix(p.Code, ".export") {
			out = append(out, p.Code)
		}
	}
	return out
}

// Contribution adds the Accounting permissions to the role templates.
func Contribution() catalog.Contribution {
	all := make([]string, 0, 80)
	for _, p := range Permissions() {
		all = append(all, p.Code)
	}
	accountant := make([]string, 0, len(all))
	for _, p := range all {
		switch p {
		case "accounting.setup.sign_off", "accounting.period.reopen", "accounting.opening_balance.post", "accounting.payment_run.execute":
			continue
		}
		accountant = append(accountant, p)
	}
	return catalog.Contribution{Permissions: Permissions(), RolePermissions: map[string][]string{
		"finance_manager": all,
		"accountant":      accountant,
		"general_manager": viewPermissions(),
		"property_admin":  {"accounting.setup.view", "accounting.report.view", "accounting.ledger.view"},
	}}
}

// DocumentTypes are the approval document types of Accounting.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{ManualJournalDocumentType, ReopenDocumentType, PaymentRunDocumentType, AllowanceDocumentType}
}

// Templates are the notification templates of Accounting (ID/EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"accounting.posting_exception": {
			"en": {"Posting exception: {{.eventType}}", "An event could not be posted ({{.reason}}): {{.message}}. Review it in Accounting → Posting Exceptions."},
			"id": {"Posting exception: {{.eventType}}", "Sebuah event tidak dapat dijurnal ({{.reason}}): {{.message}}. Tinjau di Accounting → Posting Exceptions."},
		},
		"accounting.period_closed": {
			"en": {"Financial period {{.period}} {{.status}}", "The financial period {{.period}} is now {{.status}}."},
			"id": {"Periode keuangan {{.period}} {{.status}}", "Periode keuangan {{.period}} sekarang {{.status}}."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// NewRuleVersion ends a posting rule the day before a date and creates
// its new version from that date (FR-PST-06: journals keep the rule they
// were posted with).
// RuleVersion is a created posting rule version.
type PostingRuleVersion struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	EffectiveFrom string    `json:"effectiveFrom"`
	PreviousID    uuid.UUID `json:"previousId"`
}

func (m *Module) NewRuleVersion(ctx context.Context, tx pgx.Tx, rid uuid.UUID, in PostingRuleVersionInput) (PostingRuleVersion, error) {
	from, err := parseDate("effectiveFrom", in.EffectiveFrom)
	if err != nil {
		return PostingRuleVersion{}, err
	}
	var code, name, source, status string
	var cond []byte
	var da, ca *uuid.UUID
	var dr, cr *string
	var prio int
	var effFrom time.Time
	var effTo *time.Time
	if err := tx.QueryRow(ctx, `SELECT code, name, source, conditions, debit_account_id, debit_role, credit_account_id, credit_role, priority, effective_from,
		effective_to, status FROM accounting.posting_rules WHERE id = $1 FOR UPDATE`, rid).Scan(&code, &name, &source, &cond, &da, &dr, &ca, &cr, &prio, &effFrom,
		&effTo, &status); err != nil {
		return PostingRuleVersion{}, errs.NotFound("posting rule")
	}
	if !from.After(effFrom) {
		return PostingRuleVersion{}, errs.Validation("invalid_date", "the new version must start after "+ymd(effFrom), errs.Field("effectiveFrom", "invalid", "after "+ymd(effFrom)))
	}
	if effTo != nil && !from.Before(effTo.AddDate(0, 0, 1)) {
		return PostingRuleVersion{}, errs.Conflict("ended", "the rule ends before the new version")
	}
	if in.Conditions != nil {
		cond, _ = json.Marshal(in.Conditions)
	}
	if in.DebitAccountID != nil || in.DebitRole != "" {
		da, dr = in.DebitAccountID, nz(strings.TrimPrefix(in.DebitRole, "@"))
	}
	if in.CreditAccountID != nil || in.CreditRole != "" {
		ca, cr = in.CreditAccountID, nz(strings.TrimPrefix(in.CreditRole, "@"))
	}
	for _, r := range []*string{dr, cr} {
		if r != nil {
			if _, ok := DefaultAccounts[*r]; !ok {
				return PostingRuleVersion{}, errs.Validation("invalid", "unknown default account role "+*r, errs.Field("debitRole", "invalid", "unknown role"))
			}
		}
	}
	if in.Priority != nil {
		prio = *in.Priority
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.posting_rules SET effective_to = $2, updated_by = $3 WHERE id = $1`, rid, from.AddDate(0, 0, -1), actorPtr(ctx)); err != nil {
		return PostingRuleVersion{}, err
	}
	nid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.posting_rules (id, code, name, source, conditions, debit_account_id, debit_role, credit_account_id, credit_role,
		priority, effective_from, effective_to, description, status, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'New version',$13,$14,$14)`, nid, code, name, source, cond, da, dr, ca, cr, prio, from, effTo, status,
		actorPtr(ctx)); err != nil {
		return PostingRuleVersion{}, err
	}
	after := PostingRuleVersion{ID: nid, Code: code, EffectiveFrom: ymd(from), PreviousID: rid}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "new_version", EntityType: "accounting.posting_rule", EntityID: nid.String(),
		EntityLabel: code, After: after})
}
