package pdf

import (
	"bytes"
	"strings"
	"testing"
)

func TestDocumentStructure(t *testing.T) {
	d := New()
	d.Row(16, true, "Member Statement (Juli)", "Rp 1.280.000")
	for i := 0; i < 80; i++ { // forces a second page
		d.Row(10, false, "Green fee — Championship 18", "640.000")
	}
	b := d.Bytes()
	if !bytes.HasPrefix(b, []byte("%PDF-1.4")) || !bytes.HasSuffix(b, []byte("%%EOF\n")) {
		t.Fatal("not a PDF")
	}
	if !strings.Contains(string(b), "/Count 2") {
		t.Fatalf("expected two pages")
	}
	if !strings.Contains(string(b), `Member Statement \(Juli\)`) {
		t.Fatal("parentheses must be escaped")
	}
	// xref offsets point at "N 0 obj".
	idx := bytes.Index(b, []byte("xref\n"))
	if idx < 0 {
		t.Fatal("missing xref")
	}
	lines := strings.Split(string(b[idx:]), "\n")
	for i, l := range lines[3:] {
		if !strings.HasSuffix(l, " n ") {
			break
		}
		var off int
		for _, c := range l[:10] {
			off = off*10 + int(c-'0')
		}
		want := []byte(strings.TrimSpace(strings.Repeat(" ", 0)) + itoa(i+1) + " 0 obj")
		if !bytes.HasPrefix(b[off:], want) {
			t.Fatalf("object %d offset %d wrong", i+1, off)
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
