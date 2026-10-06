package reporting

// PDF export of a report (PRD P4 FR-FIN-05 / FR-RPT-P4-05: "Export
// XLSX/PDF"): the rows of the registered report as a paginated A4 table
// with the report name, its parameters and the generation time, rendered
// with the dependency-free kernel PDF writer used by PO and quotation PDFs.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"oneclub/internal/kernel/pdf"
)

// reportPDF renders rows of rep as a PDF table.
func reportPDF(rep *Report, prm map[string]string, rows []map[string]any, generated time.Time) []byte {
	d := pdf.New()
	cols := rep.Columns
	if len(cols) == 0 {
		cols = []Column{{Key: "value", Label: "Value", Type: "string"}}
	}
	size := 8.0
	switch {
	case len(cols) > 10:
		size = 6
	case len(cols) > 6:
		size = 7
	}
	width := (pdf.PageWidth - 2*pdf.Margin) / float64(len(cols))
	maxChars := int(width/(size*0.52)) - 1
	if maxChars < 3 {
		maxChars = 3
	}

	d.Row(14, true, rep.Name)
	sub := "Generated " + generated.UTC().Format("2006-01-02 15:04 UTC") + " · " + fmt.Sprintf("%d rows", len(rows))
	d.Row(8, false, sub)
	if len(prm) > 0 {
		keys := make([]string, 0, len(prm))
		for k := range prm {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if prm[k] != "" {
				parts = append(parts, labelOf(rep, k)+": "+prm[k])
			}
		}
		if len(parts) > 0 {
			d.Row(8, false, clip(strings.Join(parts, " · "), 120))
		}
	}
	d.Space(4)

	header := func() {
		y := d.Y
		for i, c := range cols {
			cell(d, i, width, y, size, true, c, clip(c.Label, maxChars))
		}
		d.Rule(y - size*0.5)
		d.Y -= size * 1.8
	}
	header()
	for _, row := range rows {
		if d.Y < pdf.Margin+size*2 {
			d.AddPage()
			d.Row(8, false, rep.Name+" (continued)")
			header()
		}
		y := d.Y
		for i, c := range cols {
			cell(d, i, width, y, size, false, c, clip(pdfText(c, row[c.Key]), maxChars))
		}
		d.Y -= size * 1.5
	}
	if len(rows) == 0 {
		d.Row(size, false, "No rows for these parameters.")
	}
	return d.Bytes()
}

// cell draws column i of a table row: numbers right-aligned.
func cell(d *pdf.Doc, i int, width, y, size float64, bold bool, c Column, s string) {
	left := pdf.Margin + float64(i)*width
	if c.Type == "number" {
		d.TextRight(left+width-2, y, size, bold, s)
		return
	}
	d.Text(left, y, size, bold, s)
}

func labelOf(rep *Report, key string) string {
	for _, p := range rep.Params {
		if p.Key == key {
			return p.Label
		}
	}
	return key
}

// pdfText formats a cell: dates as YYYY-MM-DD HH:MM, numbers grouped.
func pdfText(c Column, v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case time.Time:
		if c.Type == "datetime" && (t.Hour() != 0 || t.Minute() != 0) {
			return t.UTC().Format("2006-01-02 15:04")
		}
		return t.Format("2006-01-02")
	case *time.Time:
		if t == nil {
			return ""
		}
		return pdfText(c, *t)
	case bool:
		if t {
			return "Yes"
		}
		return "No"
	}
	s := fmt.Sprint(cellValue(v))
	if c.Type == "number" {
		return groupDigits(s)
	}
	return s
}

// groupDigits adds thousands separators to a plain decimal string.
func groupDigits(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, hasFrac := strings.Cut(s, ".")
	for _, r := range intPart {
		if r < '0' || r > '9' {
			if neg {
				return "-" + s
			}
			return s
		}
	}
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if hasFrac {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// clip shortens s to n runes ("..." marks the cut; the standard PDF fonts
// have no ellipsis glyph in WinAnsi).
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}
