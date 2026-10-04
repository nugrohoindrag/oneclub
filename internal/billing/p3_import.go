package billing

// Migration wave 3 (PRD P3 FR-MIG-P3-02): open corporate receivables from
// Rhapsody arrive as issued invoices on the city ledger, keeping the legacy
// number and dates, so ageing, reminders and payments continue in OneClub.
// The outstanding amount is the opening receivable of the AR account; the
// part already paid in Rhapsody stays history there.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/org"
)

// LegacyInvoice is an open invoice of the legacy system.
type LegacyInvoice struct {
	LegacyNumber       string
	CorporateAccountID *uuid.UUID
	CustomerID         *uuid.UUID
	IssueDate          time.Time
	DueDate            time.Time
	Outstanding        decimal.Decimal
	OriginalTotal      decimal.Decimal
	Description        string
}

// ImportOpenInvoice records a migrated open invoice (idempotent per legacy
// number): an opening receivable on the AR account and an issued invoice
// numbered RH-<legacy number>. inserted is false when it already exists.
func (s *Service) ImportOpenInvoice(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LegacyInvoice) (inv Invoice, inserted bool, err error) {
	if in.LegacyNumber == "" || !in.Outstanding.IsPositive() {
		return Invoice{}, false, errs.Validation("invalid_legacy_invoice", "legacy number and a positive outstanding amount are required")
	}
	number := "RH-" + in.LegacyNumber
	var existing uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM billing.invoices WHERE property_id = $1 AND number = $2`, property, number).Scan(&existing); err == nil {
		inv, err := oneOf[Invoice]("invoice")(tx.Query(ctx, invoiceSelect+` WHERE i.id = $1`, existing))
		return inv, false, err
	} else if !dbtx.IsNoRows(err) {
		return Invoice{}, false, err
	}
	var acct Account
	var billTo string
	switch {
	case in.CorporateAccountID != nil:
		if acct, err = s.EnsureCorporateAccount(ctx, tx, property, *in.CorporateAccountID); err != nil {
			return Invoice{}, false, err
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.corporate_accounts WHERE id = $1`, *in.CorporateAccountID).Scan(&billTo); err != nil {
			return Invoice{}, false, err
		}
	case in.CustomerID != nil:
		if acct, err = s.EnsureAccount(ctx, tx, property, *in.CustomerID, "customer", nil); err != nil {
			return Invoice{}, false, err
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.customers WHERE id = $1`, *in.CustomerID).Scan(&billTo); err != nil {
			return Invoice{}, false, err
		}
	default:
		return Invoice{}, false, errs.Validation("invalid_legacy_invoice", "a corporate account or a customer is required")
	}
	cur, _ := org.Currency(ctx, tx)
	amt := round(in.Outstanding, cur)
	desc := in.Description
	if desc == "" {
		desc = "Rhapsody invoice " + in.LegacyNumber
	}
	issue, due := in.IssueDate, in.DueDate
	if issue.IsZero() {
		issue = localToday(ctx, tx, property, time.Now())
	}
	if due.IsZero() || due.Before(issue) {
		due = issue
	}
	status := "issued"
	if due.Before(localToday(ctx, tx, property, time.Now())) {
		status = "overdue"
	}
	eid, iid := id.New(), id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.account_entries (id, property_id, account_id, entry_type, amount, currency, description, created_by)
		VALUES ($1,$2,$3,'opening_balance',$4::numeric,$5,$6,$7)`, eid, property, acct.ID, amt.String(), cur, desc, id.Ptr(actor(ctx))); err != nil {
		return Invoice{}, false, err
	}
	note := "Migrated from Rhapsody"
	if in.OriginalTotal.IsPositive() {
		note += "; original total " + in.OriginalTotal.String()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.invoices (id, property_id, number, kind, customer_id, corporate_account_id, account_id, bill_to_name,
		issue_date, due_date, terms_days, currency, subtotal, total, status, public_token, notes, issued_at, created_by, updated_by)
		VALUES ($1,$2,$3,'standard',$4,$5,$6,$7,$8,$9,$10,$11,$12::numeric,$12::numeric,$13,$14,$15,now(),$16,$16)`,
		iid, property, number, acct.CustomerID, in.CorporateAccountID, acct.ID, billTo, issue, due, int(due.Sub(issue).Hours()/24), cur, amt.String(),
		status, newToken(), note, id.Ptr(actor(ctx))); err != nil {
		return Invoice{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.invoice_lines (id, property_id, invoice_id, seq, account_entry_id, description, quantity, unit_price, net_amount, total)
		VALUES ($1,$2,$3,1,$4,$5,1,$6::numeric,$6::numeric,$6::numeric)`, id.New(), property, iid, eid, desc, amt.String()); err != nil {
		return Invoice{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.account_entries SET invoice_id = $2 WHERE id = $1`, eid, iid); err != nil {
		return Invoice{}, false, err
	}
	inv, err = oneOf[Invoice]("invoice")(tx.Query(ctx, invoiceSelect+` WHERE i.id = $1`, iid))
	if err != nil {
		return inv, false, err
	}
	return inv, true, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "import", EntityType: "billing.invoice", EntityID: iid.String(),
		EntityLabel: number, PropertyID: &property, After: inv})
}
