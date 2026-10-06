package accounting

// EP-21 FR-REV-07/08 tax invoices: the output tax invoice (faktur pajak
// keluaran) of every issued billing invoice with PPN for a customer with an
// NPWP, uploaded to Coretax through the e-Faktur integration capability
// (FR-INT-P4-02; the sandbox adapter is mock-efaktur), cancelled with the
// invoice or replaced (pengganti); input tax invoices (masukan) from vendor
// invoices and bills; the e-Faktur CSV export per tax period and the PPN
// report (output − input per masa pajak).

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/numbering"
)

// TaxInvoiceLine is one line of a tax invoice.
type TaxInvoiceLine struct {
	Description string `json:"description"`
	DPP         string `json:"dpp"`
	PPN         string `json:"ppn"`
}

// pb1Lines are the business lines taxed with the local tax (PBJT / PB1)
// instead of PPN when a line has no tax detail (PRD P4 §16 #7).
var pb1Lines = map[string]bool{"pos": true, "stay": true, "banquet": true, "sportclub": true}

// outputVATCodes are the tax & service rule codes mapped to output VAT.
func outputVATCodes(ctx context.Context, q dbtx.Querier) (map[string]bool, map[string]bool, error) {
	vat, other := map[string]bool{}, map[string]bool{}
	rows, err := q.Query(ctx, `SELECT code, kind, rule_codes FROM accounting.tax_codes WHERE status = 'active'`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, kind string
		var rc []string
		if err := rows.Scan(&code, &kind, &rc); err != nil {
			return nil, nil, err
		}
		for _, c := range append(rc, code) {
			if kind == "output_vat" {
				vat[strings.ToUpper(c)] = true
			} else {
				other[strings.ToUpper(c)] = true
			}
		}
	}
	return vat, other, rows.Err()
}

// createOutputTaxInvoice drafts the faktur pajak keluaran of an issued
// invoice (once per invoice).
func (m *Module) createOutputTaxInvoice(ctx context.Context, tx pgx.Tx, property uuid.UUID, p invoiceDoc, d time.Time) error {
	npwp := strings.TrimSpace(deref(p.BillToNpwp))
	if npwp == "" {
		return nil // e-Faktur only for customers with an NPWP
	}
	vat, other, err := outputVATCodes(ctx, tx)
	if err != nil {
		return err
	}
	type lineRow struct {
		Description  string  `db:"description"`
		Net          string  `db:"net"`
		Service      string  `db:"service"`
		Tax          string  `db:"tax"`
		BusinessLine *string `db:"business_line"`
		TaxLines     []byte  `db:"tax_lines"`
	}
	rows, err := handle.List[lineRow](tx.Query(ctx, `SELECT description, net_amount::text AS net, service_amount::text AS service, tax_amount::text AS tax,
		business_line, tax_lines FROM reporting.acc_invoice_lines WHERE invoice_id = $1 ORDER BY seq`, p.ID))
	if err != nil {
		return err
	}
	var lines []TaxInvoiceLine
	dpp, ppn := decimal.Zero, decimal.Zero
	for _, l := range rows {
		var tls []taxLine
		_ = json.Unmarshal(l.TaxLines, &tls)
		lineVAT := decimal.Zero
		detailed := false
		for _, t := range tls {
			if t.Kind == "service" {
				continue
			}
			detailed = true
			if vat[strings.ToUpper(t.Code)] {
				lineVAT = lineVAT.Add(dec(t.Amount))
			}
		}
		if !detailed && !pb1Lines[deref(l.BusinessLine)] {
			lineVAT = dec(l.Tax) // no tax detail: PPN unless a local-tax line
		}
		_ = other
		if !lineVAT.IsPositive() {
			continue
		}
		base := dec(l.Net).Add(dec(l.Service))
		dpp, ppn = dpp.Add(base), ppn.Add(lineVAT)
		lines = append(lines, TaxInvoiceLine{Description: l.Description, DPP: base.String(), PPN: lineVAT.String()})
	}
	if !ppn.IsPositive() {
		return nil
	}
	code := "01"
	_ = tx.QueryRow(ctx, `SELECT coalesce(transaction_code, '01') FROM accounting.tax_codes WHERE kind = 'output_vat' AND status = 'active' ORDER BY code LIMIT 1`).
		Scan(&code)
	raw, _ := json.Marshal(lines)
	var partner *uuid.UUID
	switch {
	case p.CorporateAccountID != nil:
		partner = p.CorporateAccountID
	case p.CustomerID != nil:
		partner = p.CustomerID
	}
	var address *string
	_ = tx.QueryRow(ctx, `SELECT bill_to_address FROM reporting.acc_invoices WHERE id = $1`, p.ID).Scan(&address)
	_, err = tx.Exec(ctx, `INSERT INTO accounting.tax_invoices (id, property_id, direction, source_type, source_id, source_number, partner_id, partner_name,
		partner_npwp, partner_address, invoice_date, tax_period, dpp, ppn, transaction_code, lines)
		VALUES ($1,$2,'output','billing.invoice',$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12::numeric,$13,$14)
		ON CONFLICT (direction, source_type, source_id) WHERE status <> 'cancelled' DO NOTHING`,
		id.New(), property, p.ID, nz(deref(p.Number)), partner, p.BillToName, npwp, address, d, d.Format("2006-01"), dpp.String(), ppn.String(), code, raw)
	return err
}

// createInputTaxInvoice records the faktur pajak masukan of a vendor
// invoice or bill.
func (m *Module) createInputTaxInvoice(ctx context.Context, tx pgx.Tx, property uuid.UUID, sourceType string, sourceID uuid.UUID, number string,
	supplierID uuid.UUID, name, npwp string, d time.Time, dpp, ppn decimal.Decimal, fakturNo string) error {
	sup := supplierID
	raw, _ := json.Marshal([]TaxInvoiceLine{{Description: number, DPP: dpp.String(), PPN: ppn.String()}})
	_, err := tx.Exec(ctx, `INSERT INTO accounting.tax_invoices (id, property_id, direction, source_type, source_id, source_number, partner_id, partner_name,
		partner_npwp, invoice_date, tax_period, dpp, ppn, transaction_code, faktur_number, lines)
		VALUES ($1,$2,'input',$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12::numeric,'01',$13,$14)
		ON CONFLICT (direction, source_type, source_id) WHERE status <> 'cancelled' DO NOTHING`,
		id.New(), property, sourceType, sourceID, nz(number), &sup, name, nz(npwp), d, d.Format("2006-01"), dpp.String(), ppn.String(), nz(fakturNo), raw)
	return err
}

// cancelTaxInvoice cancels the tax invoices of a source document (voided
// invoice).
func (m *Module) cancelTaxInvoice(ctx context.Context, tx pgx.Tx, sourceType string, sourceID uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE accounting.tax_invoices SET status = 'cancelled', cancelled_at = now(), cancel_reason = $3
		WHERE source_type = $1 AND source_id = $2 AND status <> 'cancelled'`, sourceType, sourceID, reason)
	return err
}

// TaxInvoiceDoc is a tax invoice (e-Faktur).
type TaxInvoiceDoc struct {
	ID              uuid.UUID        `json:"id" db:"id"`
	Direction       string           `json:"direction" db:"direction" enum:"output,input"`
	SourceType      string           `json:"sourceType" db:"source_type"`
	SourceID        uuid.UUID        `json:"sourceId" db:"source_id"`
	SourceNumber    *string          `json:"sourceNumber" db:"source_number"`
	PartnerID       *uuid.UUID       `json:"partnerId" db:"partner_id"`
	PartnerName     string           `json:"partnerName" db:"partner_name"`
	PartnerNPWP     *string          `json:"partnerNpwp" db:"partner_npwp"`
	PartnerAddress  *string          `json:"partnerAddress" db:"partner_address"`
	InvoiceDate     string           `json:"invoiceDate" db:"invoice_date"`
	TaxPeriod       string           `json:"taxPeriod" db:"tax_period"`
	DPP             string           `json:"dpp" db:"dpp"`
	PPN             string           `json:"ppn" db:"ppn"`
	TransactionCode string           `json:"transactionCode" db:"transaction_code"`
	FakturNumber    *string          `json:"fakturNumber" db:"faktur_number"`
	Status          string           `json:"status" db:"status" enum:"draft,exported,uploaded,cancelled"`
	ExportID        *uuid.UUID       `json:"exportId" db:"export_id"`
	ReplacesID      *uuid.UUID       `json:"replacesId" db:"replaces_id"`
	ExternalID      *string          `json:"externalId" db:"external_id"`
	UploadError     *string          `json:"uploadError" db:"upload_error"`
	UploadedAt      *time.Time       `json:"uploadedAt" db:"uploaded_at"`
	CancelledAt     *time.Time       `json:"cancelledAt" db:"cancelled_at"`
	CancelReason    *string          `json:"cancelReason" db:"cancel_reason"`
	Lines           []TaxInvoiceLine `json:"lines" db:"lines"`
	CreatedAt       time.Time        `json:"createdAt" db:"created_at"`
}

const taxInvoiceSelect = `SELECT t.id, t.direction, t.source_type, t.source_id, t.source_number, t.partner_id, t.partner_name, t.partner_npwp, t.partner_address,
	to_char(t.invoice_date, 'YYYY-MM-DD') AS invoice_date, t.tax_period, trim_scale(t.dpp)::text AS dpp, trim_scale(t.ppn)::text AS ppn, t.transaction_code,
	t.faktur_number, t.status, t.export_id, t.replaces_id, t.external_id, t.upload_error, t.uploaded_at, t.cancelled_at, t.cancel_reason, t.lines, t.created_at
	FROM accounting.tax_invoices t`

// GetTaxInvoice loads a tax invoice of a property.
func GetTaxInvoice(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (TaxInvoiceDoc, error) {
	return getOne[TaxInvoiceDoc]("tax invoice")(q.Query(ctx, taxInvoiceSelect+` WHERE t.id = $1 AND t.property_id = $2`, tid, property))
}

// ListTaxInvoices lists the tax invoices of a property.
func ListTaxInvoices(ctx context.Context, q dbtx.Querier, property uuid.UUID, direction, period, status, sourceID string) ([]TaxInvoiceDoc, error) {
	return handle.List[TaxInvoiceDoc](q.Query(ctx, taxInvoiceSelect+` WHERE t.property_id = $1 AND ($2 = '' OR t.direction = $2) AND ($3 = '' OR t.tax_period = $3)
		AND ($4 = '' OR t.status = $4) AND ($5 = '' OR t.source_id::text = $5) ORDER BY t.invoice_date DESC, t.created_at DESC LIMIT 1000`,
		property, direction, period, status, sourceID))
}

// UploadTaxInvoice submits a draft or exported output tax invoice to
// Coretax through the e-Faktur integration and stores the serial number.
func (m *Module) UploadTaxInvoice(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (TaxInvoiceDoc, error) {
	t, err := GetTaxInvoice(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Direction != "output" {
		return t, errs.Conflict("input_invoice", "input tax invoices are issued by the supplier")
	}
	if t.Status == "uploaded" || t.Status == "cancelled" {
		return t, errs.Conflict("not_uploadable", "the tax invoice is "+t.Status)
	}
	if m.Integrations == nil {
		return t, errs.Conflict("not_configured", "no e-Faktur integration is configured")
	}
	a, code, err := m.Integrations.Resolve(ctx, integration.CapTaxInvoice)
	if err != nil {
		return t, errs.Conflict("not_configured", "no e-Faktur (Coretax) integration is enabled: "+err.Error())
	}
	ad, ok := a.(integration.TaxInvoiceAdapter)
	if !ok {
		return t, errs.Conflict("not_configured", "integration "+code+" does not provide e-Faktur")
	}
	d, _ := time.Parse("2006-01-02", t.InvoiceDate)
	res, err := ad.SubmitInvoice(ctx, integration.TaxInvoice{Reference: deref(t.SourceNumber), BuyerNPWP: deref(t.PartnerNPWP), BuyerName: t.PartnerName,
		TotalAmount: t.DPP, TaxAmount: t.PPN, IssuedAt: d})
	if err != nil {
		return t, errs.Conflict("upload_failed", "e-Faktur upload failed: "+err.Error())
	}
	if res.Status == "rejected" || res.Status == "failed" {
		return t, errs.Conflict("upload_rejected", "Coretax rejected the tax invoice ("+res.Status+")")
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.tax_invoices SET status = 'uploaded', faktur_number = $2, external_id = $3, upload_error = NULL,
		uploaded_at = now(), uploaded_by = $4 WHERE id = $1`, tid, res.Number, res.ExternalID, actorPtr(ctx)); err != nil {
		return t, err
	}
	after, err := GetTaxInvoice(ctx, tx, property, tid)
	if err != nil {
		return after, err
	}
	if m.Events != nil {
		if _, err := m.Events.Publish(ctx, tx, EventTaxInvoiceUploaded, "accounting.tax_invoice", &tid, &property, map[string]any{"taxInvoiceId": tid,
			"sourceType": t.SourceType, "sourceId": t.SourceID, "sourceNumber": t.SourceNumber, "fakturNumber": res.Number, "taxPeriod": t.TaxPeriod,
			"dpp": t.DPP, "ppn": t.PPN, "integration": code}); err != nil {
			return after, err
		}
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "upload", EntityType: "accounting.tax_invoice", EntityID: tid.String(),
		EntityLabel: deref(t.SourceNumber) + " · " + res.Number, PropertyID: &property, Before: map[string]any{"status": t.Status},
		After: map[string]any{"status": after.Status, "fakturNumber": res.Number, "integration": code}})
}

// TaxInvoiceCancelInput cancels a tax invoice, optionally with a
// replacement (faktur pengganti).
type TaxInvoiceCancelInput struct {
	Reason  string `json:"reason"`
	Replace bool   `json:"replace,omitempty" doc:"Create a replacement draft (faktur pengganti) with the same figures"`
}

// CancelTaxInvoice cancels a tax invoice (and drafts its replacement).
func (m *Module) CancelTaxInvoice(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TaxInvoiceCancelInput) (TaxInvoiceDoc, error) {
	t, err := GetTaxInvoice(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status == "cancelled" {
		return t, errs.Conflict("cancelled", "the tax invoice is already cancelled")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return t, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.tax_invoices SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2 WHERE id = $1`, tid, in.Reason); err != nil {
		return t, err
	}
	var out TaxInvoiceDoc
	if in.Replace {
		nid := id.New()
		raw, _ := json.Marshal(t.Lines)
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.tax_invoices (id, property_id, direction, source_type, source_id, source_number, partner_id, partner_name,
			partner_npwp, partner_address, invoice_date, tax_period, dpp, ppn, transaction_code, replaces_id, lines)
			SELECT $1, property_id, direction, source_type, source_id, source_number, partner_id, partner_name, partner_npwp, partner_address, invoice_date,
			tax_period, dpp, ppn, transaction_code, id, $3 FROM accounting.tax_invoices WHERE id = $2`, nid, tid, raw); err != nil {
			return t, err
		}
		if out, err = GetTaxInvoice(ctx, tx, property, nid); err != nil {
			return out, err
		}
	} else if out, err = GetTaxInvoice(ctx, tx, property, tid); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "cancel", EntityType: "accounting.tax_invoice", EntityID: tid.String(),
		EntityLabel: deref(t.SourceNumber), PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": t.Status},
		After: map[string]any{"status": "cancelled", "replacementId": out.ID}})
}

// EFakturExportInput exports the tax invoices of a period (e-Faktur CSV).
type EFakturExportInput struct {
	TaxPeriod string `json:"taxPeriod" doc:"YYYY-MM"`
	Direction string `json:"direction,omitempty" enum:"output,input"`
}

// EFakturExport is an e-Faktur export file.
type EFakturExport struct {
	ID        uuid.UUID `json:"id"`
	Number    string    `json:"number"`
	TaxPeriod string    `json:"taxPeriod"`
	Invoices  int       `json:"invoices"`
	Content   string    `json:"content" doc:"CSV in the e-Faktur import layout (FK / LT / OF rows)"`
}

// ExportTaxInvoices writes the e-Faktur CSV of the open tax invoices of a
// period and marks the drafts exported (official export fallback of
// FR-INT-P4-02).
func (m *Module) ExportTaxInvoices(ctx context.Context, tx pgx.Tx, property uuid.UUID, in EFakturExportInput) (EFakturExport, error) {
	if _, err := time.Parse("2006-01", in.TaxPeriod); err != nil {
		return EFakturExport{}, handle.Invalid("taxPeriod", "invalid", "YYYY-MM")
	}
	dir := in.Direction
	if dir == "" {
		dir = "output"
	}
	if dir != "output" && dir != "input" {
		return EFakturExport{}, handle.Invalid("direction", "invalid", "output or input")
	}
	list, err := handle.List[TaxInvoiceDoc](tx.Query(ctx, taxInvoiceSelect+` WHERE t.property_id = $1 AND t.tax_period = $2 AND t.direction = $3
		AND t.status <> 'cancelled' ORDER BY t.invoice_date, t.source_number`, property, in.TaxPeriod, dir))
	if err != nil {
		return EFakturExport{}, err
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	if dir == "output" {
		_ = w.Write([]string{"FK", "KD_JENIS_TRANSAKSI", "FG_PENGGANTI", "NOMOR_FAKTUR", "MASA_PAJAK", "TAHUN_PAJAK", "TANGGAL_FAKTUR", "NPWP", "NAMA",
			"ALAMAT_LENGKAP", "JUMLAH_DPP", "JUMLAH_PPN", "JUMLAH_PPNBM", "REFERENSI"})
		_ = w.Write([]string{"OF", "KODE_OBJEK", "NAMA", "HARGA_SATUAN", "JUMLAH_BARANG", "HARGA_TOTAL", "DISKON", "DPP", "PPN"})
	} else {
		_ = w.Write([]string{"FM", "KD_JENIS_TRANSAKSI", "FG_PENGGANTI", "NOMOR_FAKTUR", "MASA_PAJAK", "TAHUN_PAJAK", "TANGGAL_FAKTUR", "NPWP", "NAMA",
			"ALAMAT_LENGKAP", "JUMLAH_DPP", "JUMLAH_PPN", "JUMLAH_PPNBM", "IS_CREDITABLE"})
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, t := range list {
		d, _ := time.Parse("2006-01-02", t.InvoiceDate)
		repl := "0"
		if t.ReplacesID != nil {
			repl = "1"
		}
		npwp := strings.NewReplacer(".", "", "-", "").Replace(deref(t.PartnerNPWP))
		row := []string{map[string]string{"output": "FK", "input": "FM"}[dir], t.TransactionCode, repl, deref(t.FakturNumber), d.Format("1"), d.Format("2006"),
			d.Format("02/01/2006"), npwp, t.PartnerName, deref(t.PartnerAddress), t.DPP, t.PPN, "0"}
		if dir == "output" {
			row = append(row, deref(t.SourceNumber))
		} else {
			row = append(row, "1")
		}
		_ = w.Write(row)
		if dir == "output" {
			for _, l := range t.Lines {
				_ = w.Write([]string{"OF", "", l.Description, l.DPP, "1", l.DPP, "0", l.DPP, l.PPN})
			}
		}
		ids = append(ids, t.ID)
	}
	w.Flush()
	eid := id.New()
	d, _ := time.Parse("2006-01", in.TaxPeriod)
	num, err := numbering.Next(ctx, tx, property, "EFK", d)
	if err != nil {
		return EFakturExport{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.tax_exports (id, property_id, number, format, tax_period, invoices, content, created_by)
		VALUES ($1,$2,$3,'csv',$4,$5,$6,$7)`, eid, property, num, in.TaxPeriod, len(ids), b.String(), actorPtr(ctx)); err != nil {
		return EFakturExport{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.tax_invoices SET export_id = $2, status = CASE WHEN status = 'draft' THEN 'exported' ELSE status END
		WHERE id = ANY ($1)`, ids, eid); err != nil {
		return EFakturExport{}, err
	}
	out := EFakturExport{ID: eid, Number: num, TaxPeriod: in.TaxPeriod, Invoices: len(ids), Content: b.String()}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionExport, EntityType: "accounting.tax_export", EntityID: eid.String(),
		EntityLabel: num + " · " + in.TaxPeriod, PropertyID: &property, After: map[string]any{"direction": dir, "invoices": len(ids)}})
}

// PPNReport is the VAT report of a tax period (FR-REV-08).
type PPNReport struct {
	TaxPeriod      string `json:"taxPeriod"`
	OutputDPP      string `json:"outputDpp"`
	OutputPPN      string `json:"outputPpn"`
	OutputInvoices int    `json:"outputInvoices"`
	Uploaded       int    `json:"uploaded"`
	InputDPP       string `json:"inputDpp"`
	InputPPN       string `json:"inputPpn"`
	InputInvoices  int    `json:"inputInvoices"`
	NetPayable     string `json:"netPayable" doc:"Output − input PPN (kurang / lebih bayar when negative)"`
	GLOutputPPN    string `json:"glOutputPpn" doc:"Credits to the PPN output account in the period (includes sales without a tax invoice)"`
	GLInputPPN     string `json:"glInputPpn" doc:"Debits to the PPN input account in the period"`
}

// PPNReportOf computes the VAT report of a period.
func PPNReportOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, period string) (PPNReport, error) {
	start, err := time.Parse("2006-01", period)
	if err != nil {
		return PPNReport{}, handle.Invalid("period", "invalid", "YYYY-MM")
	}
	r := PPNReport{TaxPeriod: period}
	var od, op, id_, ip string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(dpp) FILTER (WHERE direction = 'output'), 0)::text, coalesce(sum(ppn) FILTER (WHERE direction = 'output'), 0)::text,
		count(*) FILTER (WHERE direction = 'output')::int, count(*) FILTER (WHERE direction = 'output' AND status = 'uploaded')::int,
		coalesce(sum(dpp) FILTER (WHERE direction = 'input'), 0)::text, coalesce(sum(ppn) FILTER (WHERE direction = 'input'), 0)::text,
		count(*) FILTER (WHERE direction = 'input')::int FROM accounting.tax_invoices WHERE property_id = $1 AND tax_period = $2 AND status <> 'cancelled'`,
		property, period).Scan(&od, &op, &r.OutputInvoices, &r.Uploaded, &id_, &ip, &r.InputInvoices); err != nil {
		return r, err
	}
	r.OutputDPP, r.OutputPPN, r.InputDPP, r.InputPPN = dec(od).String(), dec(op).String(), dec(id_).String(), dec(ip).String()
	r.NetPayable = dec(op).Sub(dec(ip)).String()
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return r, err
	}
	gl := func(role string, sign int64) (string, error) {
		var s *string
		err := q.QueryRow(ctx, `SELECT sum(l.credit - l.debit)::text FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id
			WHERE a.code = $1 AND l.property_id = $2 AND l.journal_date BETWEEN $3 AND $4`, cfg.Accounts[role], property, start, monthEnd(start)).Scan(&s)
		return decp(s).Mul(decimal.NewFromInt(sign)).String(), err
	}
	if r.GLOutputPPN, err = gl("ppn_output", 1); err != nil {
		return r, err
	}
	r.GLInputPPN, err = gl("ppn_input", -1)
	return r, err
}
