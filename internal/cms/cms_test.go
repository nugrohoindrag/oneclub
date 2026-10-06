package cms

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/config"
)

func TestSanitizeHTML(t *testing.T) {
	for in, want := range map[string]string{
		`<p>Hi <script>alert(1)</script><b>bold</b></p>`:                     `<p>Hi <b>bold</b></p>`,
		`<a href="javascript:alert(1)" onclick="x()">x</a>`:                  `<a>x</a>`,
		`<a href="https://a.test" target="_blank">a</a>`:                     `<a href="https://a.test" target="_blank" rel="noopener noreferrer">a</a>`,
		`<img src="x" onerror="alert(1)">`:                                   ``,
		`<img src="/api/v1/files/1" alt="A&quot;B" width="10px">`:            `<img src="/api/v1/files/1" alt="A&#34;B">`,
		`<iframe src="https://evil.test"></iframe><style>*{}</style>text`:    `text`,
		`<svg><script>alert(1)</script></svg><math>x</math>ok`:               `ok`,
		`<div><span style="color:red">kept</span></div>`:                     `<span>kept</span>`,
		`<table><tr><td colspan="2" rowspan="x">c</td></tr></table>`:         `<table><tbody><tr><td colspan="2">c</td></tr></tbody></table>`,
		`<p>&lt;script&gt; stays text</p>`:                                   `<p>&lt;script&gt; stays text</p>`,
		`<a href="//evil.test/x">protocol relative</a>`:                      `<a>protocol relative</a>`,
		`<a href="mailto:info@club.test">mail</a><a href="/id/golf">rel</a>`: `<a href="mailto:info@club.test">mail</a><a href="/id/golf">rel</a>`,
	} {
		if got := sanitizeHTML(in); got != want {
			t.Errorf("sanitizeHTML(%q) = %q, want %q", in, got, want)
		}
	}
	if got := plainText("  <b>Promo</b> <script>x()</script>Ramadan\x07 "); got != "Promo Ramadan" {
		t.Fatalf("plainText: %q", got)
	}
}

func TestSlugsPathsAndEmbeds(t *testing.T) {
	if s := slugify("Promo Ramadan 2026! Café — Spesial"); s != "promo-ramadan-2026-cafe-spesial" {
		t.Fatalf("slugify: %q", s)
	}
	home, golf := "home", "golf"
	if contentPath(KindPage, "en", "x", &home, nil) != "/en" || contentPath(KindPage, "id", "golf", &golf, nil) != "/id/golf" ||
		contentPath(KindArticle, "en", "a", nil, nil) != "/en/news/a" || contentPath(KindGallery, "id", "", nil, nil) != "" {
		t.Fatal("content paths")
	}
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=1": "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                    "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
		"https://vimeo.com/123456789":                     "https://player.vimeo.com/video/123456789",
		"https://evil.test/watch?v=dQw4w9WgXcQ":           "",
	} {
		if got := videoEmbed(in); got != want {
			t.Errorf("videoEmbed(%s) = %s", in, got)
		}
	}
	if routePath("en", "book_golf") != "/en/book-golf" || routePath("id", "unknown") != "/id" {
		t.Fatal("route paths")
	}
}

func TestDataBlocksReferenceOwnerAPIs(t *testing.T) {
	pid := uuid.New()
	pol := DefaultContentPolicy.normalize()
	blocks := []CmsBlock{{Type: "data", Config: map[string]any{"source": "packages", "filter": map[string]any{"code": "WED-GOLD"}, "limit": float64(3)}}}
	if err := checkBlocks(blocks, pol, &refs{}); err != nil {
		t.Fatal(err)
	}
	ref := dataRef(blocks[0].Config, pid, "MAIN", "en")
	if ref.Endpoint != "/api/v1/public/packages/WED-GOLD" || ref.Owner != "commercial" || ref.Limit != 3 || !strings.Contains(ref.URL, "propertyId="+pid.String()) {
		t.Fatalf("package ref: %+v", ref)
	}
	golf := dataRef(map[string]any{"source": "rates", "filter": map[string]any{"line": "golf", "date": "2026-10-01"}}, pid, "MAIN", "id")
	if golf.Endpoint != "/api/v1/public/golf/rates" || golf.Query["property"] != "MAIN" || golf.Query["date"] != "2026-10-01" {
		t.Fatalf("golf rates ref: %+v", golf)
	}
	news := dataRef(map[string]any{"source": "news", "filter": map[string]any{"featured": "true"}}, pid, "MAIN", "en")
	if news.Query["lang"] != "en" || news.Query["featured"] != "true" {
		t.Fatalf("news ref: %+v", news)
	}
	if dataRef(map[string]any{"source": "nope"}, pid, "MAIN", "en") != nil {
		t.Fatal("unknown source")
	}
	for _, bad := range []map[string]any{
		{"source": "events", "filter": map[string]any{"id": "not-a-uuid"}},
		{"source": "promotions", "filter": map[string]any{"line": "golf/../../admin"}},
		{"source": "promotions", "filter": map[string]any{"colour": "red"}},
		{"source": "news", "limit": float64(500)},
		{"source": "availability"},
	} {
		if err := checkBlocks([]CmsBlock{{Type: "data", Config: bad}}, pol, &refs{}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	// Blocks keep translatable texts per language; the default language must have required texts.
	if err := checkBlocks([]CmsBlock{{Type: "cta", Config: map[string]any{"link": map[string]any{"type": "route", "routeKey": "membership"}},
		Content: map[string]map[string]any{"en": {"label": "Join"}}}}, pol, &refs{}); err == nil {
		t.Fatal("label required in the default language")
	}
	doc := CmsDocument{Translations: map[string]CmsTranslation{"id": {Title: "A", Slug: "a"}, "en": {Title: "A", Slug: "a"}},
		Blocks: []CmsBlock{{ID: "b1", Type: "rich_text", Content: map[string]map[string]any{"id": {"html": "<p>x</p>"}}}}}
	st := translationStates(settingsRow{}, KindPage, doc, map[string]int{"id": 2, "en": 1}, pol)
	if st["id"].Status != "complete" || st["en"].Status != "incomplete" {
		t.Fatalf("states: %+v", st)
	}
	doc.Blocks[0].Content["en"] = map[string]any{"html": "<p>y</p>"}
	if st := translationStates(settingsRow{}, KindPage, doc, map[string]int{"id": 3, "en": 2}, pol); st["en"].Status != "outdated" {
		t.Fatalf("outdated: %+v", st)
	}
}

func TestImageVariantsAndWebP(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	vs, err := makeVariants("image/png", buf.Bytes(), 1000)
	if err != nil || len(vs) != 2 || vs[0].name != "medium" || vs[0].width != 960 || vs[0].height != 480 || vs[1].width != 320 {
		t.Fatalf("variants: %+v %v", vs, err)
	}
	if vs, _ := makeVariants("image/gif", nil, 4000); vs != nil {
		t.Fatal("GIF keeps the original")
	}
	vp8x := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X"), make([]byte, 14)...)
	vp8x[24], vp8x[27] = 99, 49 // width-1 / height-1
	if w, h, err := webpSize(vp8x); err != nil || w != 100 || h != 50 {
		t.Fatalf("webp: %d %d %v", w, h, err)
	}
	if _, _, err := webpSize([]byte("RIFF")); err == nil {
		t.Fatal("short webp")
	}
}

func TestPreviewTokens(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	m := &Module{Cfg: &config.Config{AppSecret: "a-secret-of-at-least-32-characters!!"}, Now: func() time.Time { return now }}
	cid := uuid.New()
	tok := m.sign("v1|" + cid.String() + "|3|en|" + "1800000000")
	c, err := m.verify(tok)
	if err != nil || c.content != cid || c.version != 3 || c.lang != "en" {
		t.Fatalf("verify: %+v %v", c, err)
	}
	if _, err := m.verify(tok[:len(tok)-2] + "xx"); err == nil {
		t.Fatal("tampered token")
	}
	other := &Module{Cfg: &config.Config{AppSecret: "another-secret-of-at-least-32-chars"}, Now: m.Now}
	if _, err := other.verify(tok); err == nil {
		t.Fatal("other instance key")
	}
	expired := m.sign("v1|" + cid.String() + "|3|en|" + "1700000000")
	if _, err := m.verify(expired); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired: %v", err)
	}
}
