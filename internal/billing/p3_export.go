package billing

// Accounting Export extended for PRD P3 (FR-INT-P3-05) until Accounting P4
// takes over: invoices & receivables, deposits / DP per business line,
// liabilities (vouchers, prepaid, annual fees, packages, loyalty points …),
// the business day status of the night audit (K4) and sections contributed
// by other modules (sales commission) through RegisterExportSection. Package
// revenue allocation and promotion discounts arrive as revenue components of
// the folio lines and are already part of the revenue section.

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// ExportSection adds rows to the accounting export of a business date;
// from / to bound the local calendar day.
type ExportSection func(ctx context.Context, q dbtx.Querier, property uuid.UUID, day, from, to time.Time) ([]ExportRow, error)

var (
	exportMu       sync.RWMutex
	exportSections = map[string]ExportSection{}
)

// RegisterExportSection plugs rows of another module (e.g. sales commission
// from CRM) into the accounting export.
func (s *Service) RegisterExportSection(name string, fn ExportSection) {
	exportMu.Lock()
	defer exportMu.Unlock()
	exportSections[name] = fn
}

func appendP3Export(ctx context.Context, q dbtx.Querier, out []ExportRow, property uuid.UUID, day, from, to time.Time, pl int32) ([]ExportRow, error) {
	d := day.Format("2006-01-02")
	// business day of the night audit (K4)
	status := "open"
	var st *string
	if err := q.QueryRow(ctx, `SELECT status FROM billing.business_days WHERE property_id = $1 AND business_date = $2::date`, property, d).Scan(&st); err != nil &&
		!dbtx.IsNoRows(err) {
		return nil, err
	}
	if st != nil {
		status = *st
	}
	var charges string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(total), 0)::text FROM billing.folio_lines WHERE property_id = $1 AND business_date = $2::date
		AND voided_at IS NULL`, property, d).Scan(&charges); err != nil {
		return nil, err
	}
	out = append(out, ExportRow{Section: "business_day", Code: status, Description: "Business day " + d + " charges (" + status + ")",
		Amount: dec(charges).StringFixed(pl)})
	// invoices & receivables (K2)
	for _, x := range []struct{ code, desc, sql string }{
		{"issued", "Invoices issued", `SELECT coalesce(sum(total), 0)::text FROM billing.invoices WHERE property_id = $1 AND issue_date = $2::date
			AND status <> 'void'`},
		{"allocated", "Payments allocated to invoices", `SELECT coalesce(sum(amount), 0)::text FROM billing.payment_allocations
			WHERE property_id = $1 AND invoice_id IS NOT NULL AND created_at >= $3 AND created_at < $4`},
		{"credit_note", "Credit notes", `SELECT coalesce(sum(amount), 0)::text FROM billing.credit_notes WHERE property_id = $1
			AND created_at >= $3 AND created_at < $4`},
		{"write_off", "Receivables written off", `SELECT coalesce(sum(amount), 0)::text FROM billing.write_offs WHERE property_id = $1
			AND status = 'approved' AND decided_at >= $3 AND decided_at < $4`},
	} {
		var v string
		if err := q.QueryRow(ctx, x.sql+` AND $2::text <> '' AND $3::timestamptz IS NOT NULL AND $4::timestamptz IS NOT NULL`, property, d, from, to).Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, ExportRow{Section: "invoice", Code: x.code, Description: x.desc, Amount: dec(v).StringFixed(pl)})
	}
	ag, err := AgingAsOf(ctx, q, property, day, nil)
	if err != nil {
		return nil, err
	}
	out = append(out, ExportRow{Section: "receivable", Code: "ar_outstanding", Description: "Open invoices at the end of the day",
		Amount: dec(ag.Totals.Total).StringFixed(pl)},
		ExportRow{Section: "receivable", Code: "ar_overdue", Description: "Overdue invoices at the end of the day", Amount: dec(ag.Totals.Overdue).StringFixed(pl)})
	// deposits / DP held per business line (banquet DP, package DP …)
	rows, err := q.Query(ctx, `SELECT coalesce(f.business_line, 'other'), sum(d.amount - d.applied_amount)::text FROM billing.deposits d
		LEFT JOIN billing.folios f ON f.id = d.folio_id WHERE d.property_id = $1 AND d.status = 'held' AND d.created_at < $2
		GROUP BY 1 ORDER BY 1`, property, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var line, amt string
		if err := rows.Scan(&line, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, ExportRow{Section: "deposit_balance", Code: line, Description: "Deposits held: " + lineLabel(line), Amount: dec(amt).StringFixed(pl)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// liabilities at the end of the day: deferred revenue and providers
	// (loyalty points …)
	liab := map[string]decimal.Decimal{}
	for _, lt := range []string{"voucher", "prepaid", "annual_fee", "package"} {
		v, err := Liability(ctx, q, property, lt, to)
		if err != nil {
			return nil, err
		}
		liab[lt] = v
	}
	auditMu.RLock()
	providers := make([]LiabilityProvider, 0, len(liabilities))
	for _, fn := range liabilities {
		providers = append(providers, fn)
	}
	auditMu.RUnlock()
	for _, fn := range providers {
		m, err := fn(ctx, q, property, to)
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			liab[k] = v
		}
	}
	keys := make([]string, 0, len(liab))
	for k := range liab {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, ExportRow{Section: "liability_balance", Code: k, Description: "Liability at the end of the day: " + k, Amount: liab[k].StringFixed(pl)})
	}
	// sections of other modules
	exportMu.RLock()
	names := make([]string, 0, len(exportSections))
	for n := range exportSections {
		names = append(names, n)
	}
	sort.Strings(names)
	fns := make([]ExportSection, 0, len(names))
	for _, n := range names {
		fns = append(fns, exportSections[n])
	}
	exportMu.RUnlock()
	for _, fn := range fns {
		extra, err := fn(ctx, q, property, day, from, to)
		if err != nil {
			return nil, err
		}
		for _, r := range extra {
			r.Amount = dec(r.Amount).StringFixed(pl)
			out = append(out, r)
		}
	}
	return out, nil
}

func lineLabel(line string) string {
	if l, ok := lineLabels[line]; ok {
		return l
	}
	return line
}
