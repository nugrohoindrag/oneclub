package cms

// Content safety (PRD P4 §12 Security: the CMS sanitises content against
// XSS and validates uploads; FR-REL-P4-05 pen test of CMS upload and
// content XSS). Rich text is reduced to an allowlist of tags and
// attributes; every other text field is plain text.

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"
)

// allowed tags and their allowed attributes.
var allowedTags = map[atom.Atom][]string{
	atom.P: nil, atom.Br: nil, atom.Strong: nil, atom.B: nil, atom.Em: nil, atom.I: nil, atom.U: nil, atom.S: nil,
	atom.H2: nil, atom.H3: nil, atom.H4: nil, atom.Ul: nil, atom.Ol: nil, atom.Li: nil, atom.Blockquote: nil,
	atom.Hr: nil, atom.Sup: nil, atom.Sub: nil, atom.Code: nil, atom.Pre: nil, atom.Small: nil, atom.Span: nil,
	atom.Table: nil, atom.Thead: nil, atom.Tbody: nil, atom.Tr: nil, atom.Th: {"colspan", "rowspan", "scope"}, atom.Td: {"colspan", "rowspan"},
	atom.Caption: nil, atom.Figure: nil, atom.Figcaption: nil,
	atom.A:   {"href", "title", "target"},
	atom.Img: {"src", "alt", "width", "height"},
}

// dropped entirely, with their content.
var droppedTags = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true, atom.Embed: true,
	atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Math: true, atom.Form: true, atom.Input: true, atom.Button: true,
	atom.Select: true, atom.Textarea: true, atom.Link: true, atom.Meta: true, atom.Base: true, atom.Frame: true, atom.Frameset: true,
	atom.Title: true, atom.Head: true}

var voidTags = map[atom.Atom]bool{atom.Br: true, atom.Hr: true, atom.Img: true}

var numRe = regexp.MustCompile(`^[0-9]{1,4}$`)

// sanitizeHTML keeps only safe markup: allowlisted tags and attributes,
// http(s)/mailto/tel/relative links (rel="noopener noreferrer" on new
// tabs) and http(s) or same-site images. Text is HTML-escaped.
func sanitizeHTML(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(in), ctx)
	if err != nil {
		return html.EscapeString(in)
	}
	var b strings.Builder
	for _, n := range nodes {
		writeNode(&b, n)
	}
	return strings.TrimSpace(b.String())
}

func writeNode(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(html.EscapeString(n.Data))
		return
	case html.ElementNode:
	default:
		// comments, doctypes …: drop, keep children of documents
		if n.Type == html.DocumentNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeNode(b, c)
			}
		}
		return
	}
	if droppedTags[n.DataAtom] || n.DataAtom == 0 && strings.Contains(n.Data, ":") {
		return
	}
	attrs, ok := allowedTags[n.DataAtom]
	if !ok {
		// unknown element: keep its (sanitised) content only
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeNode(b, c)
		}
		return
	}
	var out []html.Attribute
	newTab := false
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if a.Namespace != "" || !contains(attrs, key) {
			continue
		}
		val := strings.TrimSpace(a.Val)
		switch key {
		case "href":
			if !safeHref(val) {
				continue
			}
		case "src":
			if !safeSrc(val) {
				continue
			}
		case "target":
			if val != "_blank" {
				continue
			}
			newTab = true
		case "width", "height", "colspan", "rowspan":
			if !numRe.MatchString(val) {
				continue
			}
		case "scope":
			if val != "row" && val != "col" {
				continue
			}
		default:
			val = plainText(val)
		}
		out = append(out, html.Attribute{Key: key, Val: val})
	}
	if n.DataAtom == atom.Img && !hasAttr(out, "src") {
		return
	}
	if newTab {
		out = append(out, html.Attribute{Key: "rel", Val: "noopener noreferrer"})
	}
	b.WriteString("<" + n.DataAtom.String())
	for _, a := range out {
		b.WriteString(" " + a.Key + `="` + html.EscapeString(a.Val) + `"`)
	}
	b.WriteString(">")
	if voidTags[n.DataAtom] {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeNode(b, c)
	}
	b.WriteString("</" + n.DataAtom.String() + ">")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasAttr(attrs []html.Attribute, key string) bool {
	for _, a := range attrs {
		if a.Key == key {
			return true
		}
	}
	return false
}

// safeHref allows http(s), mailto, tel, same-site paths and fragments.
func safeHref(v string) bool {
	if v == "" {
		return false
	}
	if strings.HasPrefix(v, "#") || (strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//")) {
		return !strings.ContainsAny(v, "\\\x00")
	}
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto", "tel":
		return true
	}
	return false
}

// safeSrc allows http(s) images and same-site paths (media library files).
func safeSrc(v string) bool {
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
		return !strings.ContainsAny(v, "\\\x00")
	}
	return isHTTPURL(v)
}

// plainText strips markup and control characters from a text field.
func plainText(s string) string {
	if s == "" {
		return s
	}
	if strings.ContainsAny(s, "<>") {
		if nodes, err := html.ParseFragment(strings.NewReader(s), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}); err == nil {
			var b strings.Builder
			for _, n := range nodes {
				textOf(&b, n)
			}
			s = b.String()
		}
	}
	s = norm.NFC.String(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

func textOf(b *strings.Builder, n *html.Node) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		return
	}
	if n.Type == html.ElementNode && droppedTags[n.DataAtom] {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		textOf(b, c)
	}
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	sch := strings.ToLower(u.Scheme)
	return (sch == "http" || sch == "https") && u.Host != "" && !strings.ContainsAny(s, " \t\n<>\"")
}

func isAbsoluteURL(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "mailto:") || strings.HasPrefix(strings.ToLower(s), "tel:")
}

var (
	langRe = regexp.MustCompile(`^[a-z]{2}$`)
	slugRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	keyRe  = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)
)

// slugify turns a title into a URL slug ("Promo Ramadan 2026!" → "promo-ramadan-2026").
func slugify(s string) string {
	s = norm.NFKD.String(strings.ToLower(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case unicode.Is(unicode.Mn, r):
			// combining accents dropped
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 120 {
		out = strings.Trim(out[:120], "-")
	}
	return out
}
