package golf

import (
	"fmt"
	"strings"

	"oneclub/internal/kernel/pdf"
)

// textDoc is a flowing text document (HIO claim package, introduction
// letter) on P1's PDF writer: headings, wrapped paragraphs and blank lines.
type textDoc struct {
	Title string
	d     *pdf.Doc
}

func newTextDoc() *textDoc { return &textDoc{d: pdf.New()} }

func (t *textDoc) doc() *pdf.Doc {
	if t.d == nil {
		t.d = pdf.New()
	}
	return t.d
}

// Heading writes a bold line.
func (t *textDoc) Heading(text string, size float64) { t.doc().Row(size, true, text) }

// Add writes a paragraph wrapped at about 95 characters.
func (t *textDoc) Add(format string, args ...any) {
	words := strings.Fields(fmt.Sprintf(format, args...))
	line := ""
	for _, w := range words {
		if len(line)+len(w)+1 > 95 && line != "" {
			t.doc().Row(10, false, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	t.doc().Row(10, false, line)
}

// Blank adds vertical space.
func (t *textDoc) Blank() { t.doc().Space(8) }

// Bytes renders the PDF.
func (t *textDoc) Bytes() []byte { return t.doc().Bytes() }
