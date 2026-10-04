// Package pdf writes simple text-only PDF documents (member statements,
// receipts) without third-party dependencies: A4 pages, the standard
// Helvetica fonts, left/right aligned text and horizontal rules.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
)

// A4 in points.
const (
	PageWidth  = 595.28
	PageHeight = 841.89
	Margin     = 48.0
)

// Doc is a PDF under construction.
type Doc struct {
	pages []*bytes.Buffer
	cur   *bytes.Buffer
	// Y is the current baseline used by Line helpers (top-down cursor).
	Y float64
}

// New starts a document with one page.
func New() *Doc {
	d := &Doc{}
	d.AddPage()
	return d
}

// AddPage starts a new page and resets the cursor to the top margin.
func (d *Doc) AddPage() {
	d.cur = &bytes.Buffer{}
	d.pages = append(d.pages, d.cur)
	d.Y = PageHeight - Margin
}

// escape renders s as a PDF literal string in WinAnsi (non-Latin-1 runes
// become '?').
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 32:
		case r < 128:
			b.WriteRune(r)
		case r < 256:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// width approximates the Helvetica text width (average glyph 0.52 em).
func width(s string, size float64, bold bool) float64 {
	f := 0.52
	if bold {
		f = 0.56
	}
	n := 0
	for range s {
		n++
	}
	return float64(n) * size * f
}

// Text draws s with its baseline at (x, y) from the bottom-left corner.
func (d *Doc) Text(x, y, size float64, bold bool, s string) {
	font := "F1"
	if bold {
		font = "F2"
	}
	fmt.Fprintf(d.cur, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n", font, size, x, y, escape(s))
}

// TextRight draws s right-aligned at x.
func (d *Doc) TextRight(x, y, size float64, bold bool, s string) {
	d.Text(x-width(s, size, bold), y, size, bold, s)
}

// Rule draws a horizontal line at y.
func (d *Doc) Rule(y float64) {
	fmt.Fprintf(d.cur, "0.75 w %.2f %.2f m %.2f %.2f l S\n", Margin, y, PageWidth-Margin, y)
}

// Row writes left text and optional right-aligned columns on the cursor
// line, then moves the cursor down; a new page starts when needed.
func (d *Doc) Row(size float64, bold bool, left string, right ...string) {
	if d.Y < Margin+size {
		d.AddPage()
	}
	d.Text(Margin, d.Y, size, bold, left)
	x := PageWidth - Margin
	for i := len(right) - 1; i >= 0; i-- {
		d.TextRight(x, d.Y, size, bold, right[i])
		x -= 110
	}
	d.Y -= size * 1.6
}

// Columns writes cells at fixed x positions (right-aligned when the x is
// negative, measured from the right margin).
func (d *Doc) Columns(size float64, bold bool, xs []float64, cells []string) {
	if d.Y < Margin+size {
		d.AddPage()
	}
	for i, c := range cells {
		if i >= len(xs) {
			break
		}
		if xs[i] < 0 {
			d.TextRight(PageWidth-Margin+xs[i], d.Y, size, bold, c)
		} else {
			d.Text(xs[i], d.Y, size, bold, c)
		}
	}
	d.Y -= size * 1.6
}

// Space moves the cursor down by pts.
func (d *Doc) Space(pts float64) { d.Y -= pts }

// Bytes renders the document.
func (d *Doc) Bytes() []byte {
	var out bytes.Buffer
	offsets := []int{}
	obj := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// 1 catalog, 2 pages, 3 font regular, 4 font bold, then page/content pairs.
	n := len(d.pages)
	kids := make([]string, n)
	for i := range d.pages {
		kids[i] = fmt.Sprintf("%d 0 R", 5+i*2)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	for i, p := range d.pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			PageWidth, PageHeight, 6+i*2))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", p.Len(), p.String()))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes()
}
