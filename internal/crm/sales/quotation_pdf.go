package sales

// Quotation document (FR-QUO-05, FR-QUO-08): one template per business line
// (title, event details for banquet lines), the branding of the instance
// and property (logo, club name, address, contact), the lines with their
// promotions, the payment terms and the terms & conditions — Banquet
// Policies for wedding / banquet / MICE / event quotations, else Sales
// Policies (set as the quotation terms when it is created).

import (
	"context"
	"encoding/json"
	"strings"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/pdf"
)

// BanquetLine reports whether a quotation line is sold by Banquet (its
// terms come from the Banquet Policies).
func BanquetLine(line string) bool {
	switch line {
	case "wedding", "banquet", "mice", "event":
		return true
	}
	return false
}

// quotationTitles are the document titles per business line (EN / ID).
var quotationTitles = map[string]string{
	"wedding":    "Wedding Quotation / Penawaran Pernikahan",
	"banquet":    "Banquet Quotation / Penawaran Banquet",
	"mice":       "Meeting & Event Proposal / Proposal Meeting & Event",
	"event":      "Event Quotation / Penawaran Acara",
	"tournament": "Tournament Proposal / Proposal Turnamen",
	"golf":       "Golf Quotation / Penawaran Golf",
	"stay":       "Stay Quotation / Penawaran Menginap",
	"package":    "Package Quotation / Penawaran Paket",
	"membership": "Membership Proposal / Proposal Keanggotaan",
}

// QuotationTemplate is the document title of a quotation of a line.
func QuotationTemplate(line string) string {
	if t, ok := quotationTitles[line]; ok {
		return t
	}
	return "Quotation / Penawaran"
}

// quotationBranding is the letterhead of a quotation.
type quotationBranding struct {
	club, legal, address, contact string
}

func brandingOf(ctx context.Context, q dbtx.Querier, d QuotationDetail) quotationBranding {
	var b quotationBranding
	_ = q.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&b.club)
	var legal, addr, city, phone, email *string
	_ = q.QueryRow(ctx, `SELECT coalesce(nullif(o.display_name, ''), o.legal_name) FROM platform.organization o LIMIT 1`).Scan(&legal)
	var prop string
	if err := q.QueryRow(ctx, `SELECT p.name, p.address, p.city, p.phone, p.email FROM crm.sales_quotations s
		JOIN platform.properties p ON p.id = s.property_id WHERE s.id = $1`, d.ID).Scan(&prop, &addr, &city, &phone, &email); err == nil && prop != "" {
		b.club = prop
	}
	if legal != nil && *legal != b.club {
		b.legal = *legal
	}
	b.address = joinNonEmpty(", ", deref(addr), deref(city))
	b.contact = joinNonEmpty(" · ", deref(phone), deref(email))
	return b
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// wrap splits text into lines of at most n characters (words kept).
func wrap(text string, n int) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line := ""
		for _, w := range strings.Fields(para) {
			if line != "" && len(line)+1+len(w) > n {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += w
		}
		out = append(out, line)
	}
	return out
}

// QuotationPDF renders a quotation without a logo.
func QuotationPDF(ctx context.Context, q dbtx.Querier, d QuotationDetail) ([]byte, error) {
	return renderQuotationPDF(ctx, q, d, nil)
}

// QuotationPDF renders a quotation with the branding logo.
func (m *Module) QuotationPDF(ctx context.Context, q dbtx.Querier, d QuotationDetail) ([]byte, error) {
	var logo []byte
	if m.Logo != nil {
		logo = m.Logo(ctx, q)
	}
	return renderQuotationPDF(ctx, q, d, logo)
}

func renderQuotationPDF(ctx context.Context, q dbtx.Querier, d QuotationDetail, logo []byte) ([]byte, error) {
	b := brandingOf(ctx, q, d)
	doc := pdf.New()
	if len(logo) > 0 {
		_ = doc.Logo(logo, 140, 48) // an unreadable logo leaves the club name only
	}
	doc.Row(18, true, b.club)
	for _, s := range []string{b.legal, b.address, b.contact} {
		if s != "" {
			doc.Row(9, false, s)
		}
	}
	doc.Space(4)
	doc.Row(13, true, QuotationTemplate(d.Line))
	doc.Space(4)
	doc.Rule(doc.Y + 10)
	doc.Row(10, false, "Quotation No. / No. Penawaran", d.Number+" v"+itoa(d.Version))
	doc.Row(10, false, "Title / Judul", d.Title)
	if d.CorporateName != nil {
		doc.Row(10, false, "Company / Perusahaan", *d.CorporateName)
	}
	if d.CustomerName != nil {
		doc.Row(10, false, "Customer / Pelanggan", *d.CustomerName)
	}
	if d.OwnerName != nil {
		doc.Row(10, false, "Sales / Tenaga Penjual", *d.OwnerName)
	}
	if d.EventDate != nil {
		label := "Service Date / Tanggal Layanan"
		if BanquetLine(d.Line) || d.Line == "tournament" {
			label = "Event Date / Tanggal Acara"
		}
		v := *d.EventDate
		if d.EndDate != nil && *d.EndDate != *d.EventDate {
			v += " - " + *d.EndDate
		}
		doc.Row(10, false, label, v)
	}
	if d.Pax != nil {
		doc.Row(10, false, "Pax / Jumlah Tamu", itoa(*d.Pax))
	}
	doc.Row(10, false, "Valid Until / Berlaku s.d.", d.ValidUntil)
	if d.OptionDate != nil {
		doc.Row(10, false, "Option Date / Batas Opsi", *d.OptionDate)
	}
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	cols := []float64{pdf.Margin, -230, -120, -1}
	doc.Columns(9, true, cols, []string{"Item", "Qty", "Unit Price / Harga", "Total"})
	for _, l := range d.Lines {
		desc := l.Description
		if r := []rune(desc); len(r) > 60 {
			desc = string(r[:57]) + "..."
		}
		doc.Columns(9, false, cols, []string{desc, l.Quantity, formatAmount(dec(l.UnitPrice), d.Currency), formatAmount(dec(l.Total), d.Currency)})
		var snap struct {
			PromotionDiscount string `json:"promotionDiscount"`
			Promotions        []struct {
				Code string `json:"code"`
				Name string `json:"name"`
			} `json:"promotions"`
		}
		_ = json.Unmarshal(l.Pricing, &snap)
		if p := dec(snap.PromotionDiscount); p.IsPositive() {
			names := make([]string, 0, len(snap.Promotions))
			for _, x := range snap.Promotions {
				names = append(names, x.Name)
			}
			doc.Row(8, false, "   Promotion / Promosi: "+strings.Join(names, ", "), "−"+formatAmount(p, d.Currency))
		}
		if ld := dec(l.LineDiscount).Sub(dec(snap.PromotionDiscount)); ld.IsPositive() {
			doc.Row(8, false, "   Discount / Diskon", "−"+formatAmount(ld, d.Currency))
		}
	}
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	doc.Row(10, false, "Subtotal", formatAmount(dec(d.Subtotal), d.Currency))
	if dec(d.Discount).IsPositive() {
		doc.Row(10, false, "Discount & Promotions / Diskon & Promosi", "−"+formatAmount(dec(d.Discount), d.Currency))
	}
	doc.Row(10, false, "Service", formatAmount(dec(d.ServiceAmount), d.Currency))
	doc.Row(10, false, "Tax / Pajak", formatAmount(dec(d.TaxAmount), d.Currency))
	doc.Row(14, true, "Total", d.Currency+" "+formatAmount(dec(d.Total), d.Currency))
	if len(d.PaymentTerms) > 0 {
		doc.Space(6)
		doc.Row(11, true, "Payment Terms / Termin Pembayaran")
		for _, t := range d.PaymentTerms {
			doc.Row(9, false, t.Label+" · "+t.DueDate, d.Currency+" "+formatAmount(dec(t.Amount), d.Currency))
		}
	}
	eMeteraiPDF(doc, d)
	if d.Terms != nil && strings.TrimSpace(*d.Terms) != "" {
		doc.Space(6)
		doc.Row(11, true, "Terms & Conditions / Syarat & Ketentuan")
		for _, l := range wrap(*d.Terms, 110) {
			doc.Row(8, false, l)
		}
	}
	return doc.Bytes(), nil
}
