package procurement

// Purchase order and RFQ documents for the supplier (ID / EN labels,
// FR-PO-02, FR-RFQ-01; PRD P4 §12 Bahasa: PO / RFQ documents ID / EN).

import (
	"context"
	"strings"

	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/pdf"
)

// formatAmount renders 1234567.5 as "1.234.567,50" (IDR without decimals).
func formatAmount(d decimal.Decimal, cur string) string {
	s := d.StringFixed(places(cur))
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// OrderPDF renders a purchase order.
func OrderPDF(ctx context.Context, q dbtx.Querier, o PurchaseOrder) ([]byte, error) {
	s, err := loadSupplier(ctx, q, o.SupplierID)
	if err != nil {
		return nil, err
	}
	doc := pdf.New()
	doc.Row(18, true, clubName(ctx, q))
	doc.Row(12, false, "Purchase Order / Pesanan Pembelian")
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	doc.Row(10, false, "PO No. / No. PO", o.Number)
	doc.Row(10, false, "Version / Versi", itoa(o.Version))
	doc.Row(10, false, "Order Date / Tanggal", o.OrderDate)
	doc.Row(10, false, "Delivery Date / Tanggal Kirim", deref(o.ExpectedDate))
	doc.Row(10, false, "Supplier / Pemasok", s.Name)
	if s.NPWP != nil {
		doc.Row(10, false, "NPWP", *s.NPWP)
	}
	if o.DeliveryAddress != nil {
		doc.Row(10, false, "Deliver To / Kirim Ke", short(*o.DeliveryAddress, 60))
	}
	doc.Row(10, false, "Payment Term / Termin", itoa(o.PaymentTermDays)+" days / hari")
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	xs := []float64{pdf.Margin, 290, -110, -1}
	doc.Columns(9, true, xs, []string{"Item", "Qty", "Unit Price / Harga", "Amount / Jumlah"})
	for _, l := range o.Lines {
		doc.Columns(9, false, xs, []string{short(l.Description, 46), l.Quantity + " " + deref(l.UOM),
			formatAmount(dec(l.UnitPrice), o.Currency), formatAmount(dec(l.LineSubtotal), o.Currency)})
		if l.ExpectedDate != nil && (o.ExpectedDate == nil || *l.ExpectedDate != *o.ExpectedDate) {
			doc.Row(8, false, "   delivery / kirim "+*l.ExpectedDate)
		}
	}
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	doc.Row(10, false, "Subtotal", formatAmount(dec(o.Subtotal), o.Currency))
	if dec(o.DiscountTotal).IsPositive() {
		doc.Row(10, false, "Discount / Diskon (included)", formatAmount(dec(o.DiscountTotal), o.Currency))
	}
	doc.Row(10, false, "PPN", formatAmount(dec(o.TaxTotal), o.Currency))
	doc.Row(14, true, "Total", o.Currency+" "+formatAmount(dec(o.Total), o.Currency))
	if o.Terms != nil {
		doc.Space(8)
		for _, part := range strings.Split(*o.Terms, "\n") {
			doc.Row(8, false, short(part, 110))
		}
	}
	return doc.Bytes(), nil
}

// RFQPDF renders a request for quotation (no prices).
func RFQPDF(club string, r ProcurementRFQ) []byte {
	doc := pdf.New()
	doc.Row(18, true, club)
	doc.Row(12, false, "Request for Quotation / Permintaan Penawaran")
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	doc.Row(10, false, "RFQ No.", r.Number)
	doc.Row(10, false, "Subject / Perihal", short(r.Title, 60))
	if r.ResponseDueAt != nil {
		doc.Row(10, false, "Quote before / Batas penawaran", r.ResponseDueAt.Format("2006-01-02 15:04 MST"))
	}
	if r.DeliveryDate != nil {
		doc.Row(10, false, "Delivery Date / Tanggal Kirim", *r.DeliveryDate)
	}
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	xs := []float64{pdf.Margin, -120, -1}
	doc.Columns(9, true, xs, []string{"Item", "Qty", "Needed by / Dibutuhkan"})
	for _, l := range r.Lines {
		doc.Columns(9, false, xs, []string{short(l.Description, 60), l.Quantity + " " + deref(l.UOM), deref(l.NeededBy)})
	}
	if r.Terms != nil {
		doc.Space(8)
		doc.Row(8, false, short(*r.Terms, 110))
	}
	return doc.Bytes()
}

func itoa(n int) string { return decimal.NewFromInt(int64(n)).String() }
