package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/platform/integration"
	"oneclub/internal/reporting"
)

func init() {
	resourceCRUDModules["cms"] = true
	// Redirect paths are validated (an old path ≠ the new one); covered by TestP4CMSSiteData.
	resourceCRUDSkip["cms.redirect"] = "covered by TestP4CMSSiteData"
}

// cmsPNG is a w×h PNG image.
func cmsPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// cmsUpload uploads an image to the media library.
func cmsUpload(t *testing.T, c *Client, name string, w, h int, fields map[string]string) map[string]any {
	t.Helper()
	body, ctype := multipartBody(t, fields, "file", name, cmsPNG(t, w, h))
	return c.Must(201, "POST", "/api/v1/cms/media", body, "Content-Type", ctype).JSON()
}

// cmsApprove submits content for review and approves it as the Marketing
// Manager (demo workflow "Website Content Publication" of MAIN).
func cmsApprove(t *testing.T, mk, gm *Client, base, cid string) map[string]any {
	t.Helper()
	sub := mk.Must(200, "POST", base+"/"+cid+":submit", map[string]any{"note": "please review"}).JSON()
	if sub["status"] != "in_review" || sub["approvalRequestId"] == nil {
		t.Fatalf("submit: %v", sub)
	}
	gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(sub["approvalRequestId"])+":approve", map[string]any{"reason": "looks good"})
	return mk.Must(200, "GET", base+"/"+cid, nil).JSON()
}

func cmsBlocks(media string) []map[string]any {
	return []map[string]any{
		{"type": "rich_text", "content": map[string]any{
			"id": map[string]any{"heading": "Selamat datang", "html": `<p onclick="steal()">Halo <script>alert(1)</script><a href="javascript:alert(1)">klik</a> <b>tebal</b> <a href="https://moderngolf.test" target="_blank">situs</a></p><iframe src="https://evil.test"></iframe>`},
			"en": map[string]any{"heading": "Welcome", "html": "<p>Hello <em>members</em></p>"}}},
		{"type": "image", "config": map[string]any{"mediaId": media, "layout": "wide"},
			"content": map[string]any{"id": map[string]any{"caption": "Clubhouse baru"}, "en": map[string]any{"caption": "New clubhouse"}}},
		{"type": "data", "config": map[string]any{"source": "rates", "filter": map[string]any{"line": "sportclub"}, "limit": 10, "layout": "table"},
			"content": map[string]any{"id": map[string]any{"heading": "Tarif"}, "en": map[string]any{"heading": "Rates"}}},
		{"type": "data", "config": map[string]any{"source": "promotions", "limit": 6},
			"content": map[string]any{"id": map[string]any{"heading": "Promosi"}, "en": map[string]any{"heading": "Promotions"}}},
		{"type": "cta", "config": map[string]any{"link": map[string]any{"type": "route", "routeKey": "book_golf"}, "style": "primary"},
			"content": map[string]any{"id": map[string]any{"label": "Pesan"}, "en": map[string]any{"label": "Book"}}},
		{"type": "video", "config": map[string]any{"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}},
		{"type": "faq", "content": map[string]any{"id": map[string]any{"items": []map[string]any{{"question": "Buka?", "answer": "<p>Setiap hari</p>"}}},
			"en": map[string]any{"items": []map[string]any{{"question": "Open?", "answer": "<p>Every day</p>"}}}}},
	}
}

func cmsFindBlock(blocks []any, typ string) map[string]any {
	for _, b := range blocks {
		if m := b.(map[string]any); m["type"] == typ {
			return m
		}
	}
	return nil
}

// website is a stand-in for the Next.js revalidation route: it verifies the
// signature with the shared secret and records the requested paths.
type cmsWebsite struct {
	mu     sync.Mutex
	secret string
	calls  []integration.RevalidateRequest
	srv    *httptest.Server
}

func newCMSWebsite(secret string) *cmsWebsite {
	w := &cmsWebsite{secret: secret}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/revalidate" || integration.VerifyHMAC(r.Header.Get(integration.SignatureHeader), w.secret, body, time.Now()) != nil {
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = rw.Write([]byte(`{"revalidated":false}`))
			return
		}
		var req integration.RevalidateRequest
		_ = json.Unmarshal(body, &req)
		w.mu.Lock()
		w.calls = append(w.calls, req)
		w.mu.Unlock()
		_, _ = rw.Write([]byte(`{"revalidated":true,"paths":[],"tags":["cms"]}`))
	}))
	return w
}

func (w *cmsWebsite) revalidated(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.calls {
		for _, p := range c.Paths {
			if p == path {
				return true
			}
		}
	}
	return false
}

// EP-24 FR-CMS-01/03/05/06/08/10 and §9.5 Website Update: Marketing builds a
// bilingual page with text, image, video, FAQ, CTA and DATA blocks (rates
// from the Pricing Engine, promotions — never copies), the content is
// sanitised (no script injection), saved as versions with optimistic
// concurrency, previewed through a signed link, approved by the Marketing
// approver (approval engine) before it goes live, scheduled, taken down
// automatically, rolled back and archived; every publication revalidates
// the website cache through the website integration and is audited.
func TestP4CMSPageWorkflow(t *testing.T) {
	sa := superAdmin(t, inst)
	pa := platformAdmin(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	gm := roleUser(t, inst, "marketing_manager") // website publication approver (PRD P4 §16 #15)
	pub := anon(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	q := "?propertyId=" + inst.Main.String()

	// The website integration: signed on-demand revalidation (FR-CMS-10).
	site := newCMSWebsite("cms-revalidation-secret-" + sfx + "-0123456789")
	defer site.srv.Close()
	web := pa.Must(201, "POST", "/api/v1/platform/integrations", map[string]any{"code": "website-" + sfx, "adapter": "nextjs-website",
		"name": "Website", "mode": "production", "enabled": true, "credentials": map[string]string{"revalidateSecret": site.secret},
		"settings": map[string]any{"baseUrl": site.srv.URL}}).JSON()
	defer pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+str(web["id"]), map[string]any{"enabled": false})
	if r := pa.Must(200, "POST", "/api/v1/platform/integrations/"+str(web["id"])+":test", nil).JSON(); r["ok"] != true {
		t.Fatalf("website test connection: %v", r)
	}

	// Images library (FR-CMS-03): type sniffed, web sizes generated, alt per language.
	img := cmsUpload(t, mk, "Club House.png", 2000, 120, map[string]string{"alt": "Clubhouse Modern Golf", "alt.en": "Modern Golf clubhouse",
		"tags": "Clubhouse,Building", "folder": "home"})
	if v := img["variants"].([]any); len(v) != 3 || img["width"].(float64) != 2000 || img["alt"].(map[string]any)["en"] != "Modern Golf clubhouse" {
		t.Fatalf("upload: %v", img)
	}
	media := str(img["id"])
	svg, ctype := multipartBody(t, nil, "file", "x.png", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)
	if r := mk.Do("POST", "/api/v1/cms/media", svg, "Content-Type", ctype); r.Status != 422 || !strings.Contains(string(r.Body), "file_type") {
		t.Fatalf("active content must be rejected: %s", r)
	}
	if r := mk.Must(200, "PATCH", "/api/v1/cms/media/"+media, map[string]any{"caption": map[string]any{"id": "Clubhouse <b>baru</b>"}}).JSON(); r["caption"].(map[string]any)["id"] != "Clubhouse baru" {
		t.Fatalf("caption is plain text: %v", r)
	}
	if l := mk.Must(200, "GET", "/api/v1/cms/media?filter[tag]=clubhouse", nil).Items(); !containsID(l, media) {
		t.Fatalf("media by tag: %v", l)
	}
	mk.Must(200, "GET", "/api/v1/cms/media/"+media, nil)
	if r := anon(t, inst).Do("GET", str(img["url"]), nil); r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "image/png") {
		t.Fatalf("public image: %d %v", r.Status, r.Header)
	}

	// Validation: block types, data sources, languages, links, embeds.
	slug := "promo-ramadan-" + sfx
	base := map[string]any{"template": "landing", "translations": map[string]any{
		"id": map[string]any{"title": "Promo Ramadan " + sfx, "slug": slug, "summary": "Paket buka puasa", "seo": map[string]any{"metaTitle": "Promo Ramadan",
			"metaDescription": "Buka puasa di Modern Golf", "ogImageId": media}},
		"en": map[string]any{"title": "Ramadan Promo " + sfx, "slug": "ramadan-promo-" + sfx, "summary": "Iftar packages"}}}
	bad := func(blocks []map[string]any, want string) {
		t.Helper()
		b := map[string]any{}
		for k, v := range base {
			b[k] = v
		}
		b["blocks"] = blocks
		if r := mk.Do("POST", "/api/v1/cms/pages", b); r.Status != 422 || !strings.Contains(string(r.Body), want) {
			t.Fatalf("want 422 %s: %s", want, r)
		}
	}
	bad([]map[string]any{{"type": "marquee"}}, "invalid_option")
	bad([]map[string]any{{"type": "data", "config": map[string]any{"source": "rates"}}}, "required")
	bad([]map[string]any{{"type": "data", "config": map[string]any{"source": "rates", "filter": map[string]any{"line": "yacht"}}}}, "invalid_option")
	bad([]map[string]any{{"type": "data", "config": map[string]any{"source": "stock_prices"}}}, "invalid_option")
	bad([]map[string]any{{"type": "video", "config": map[string]any{"url": "https://evil.test/video.mp4"}}}, "only YouTube and Vimeo")
	bad([]map[string]any{{"type": "cta", "config": map[string]any{"link": map[string]any{"type": "url", "url": "javascript:alert(1)"}},
		"content": map[string]any{"id": map[string]any{"label": "x"}}}}, "invalid")
	bad([]map[string]any{{"type": "rich_text", "content": map[string]any{"fr": map[string]any{"html": "<p>Bonjour</p>"}}}}, "unsupported_language")
	bad([]map[string]any{{"type": "image", "config": map[string]any{"mediaId": uuid.NewString()}}}, "media_not_found")

	// Create (Draft, version 1) and save version 2 with If-Match.
	in := map[string]any{"blocks": cmsBlocks(media), "note": "first draft"}
	for k, v := range base {
		in[k] = v
	}
	page := mk.Must(201, "POST", "/api/v1/cms/pages", in, "Idempotency-Key", newKey()).JSON()
	pid := str(page["id"])
	tr := page["translations"].(map[string]any)
	if page["status"] != "draft" || page["latestVersion"].(float64) != 1 || page["key"] != slug || tr["en"].(map[string]any)["status"] != "complete" ||
		tr["en"].(map[string]any)["path"] != "/en/ramadan-promo-"+sfx {
		t.Fatalf("new page: %v", page)
	}
	rt := cmsFindBlock(page["document"].(map[string]any)["blocks"].([]any), "rich_text")
	html := str(rt["content"].(map[string]any)["id"].(map[string]any)["html"])
	if strings.Contains(html, "<script") || strings.Contains(html, "javascript:") || strings.Contains(html, "onclick") || strings.Contains(html, "iframe") ||
		!strings.Contains(html, "<b>tebal</b>") || !strings.Contains(html, `rel="noopener noreferrer"`) {
		t.Fatalf("rich text not sanitised: %s", html)
	}
	// Another page cannot take the same slug.
	dupe := map[string]any{"translations": map[string]any{"id": map[string]any{"title": "Duplikat", "slug": slug}}}
	if r := mk.Do("POST", "/api/v1/cms/pages", dupe); r.Status != 422 || !strings.Contains(string(r.Body), "slug") {
		t.Fatalf("slug uniqueness: %s", r)
	}
	g := mk.Must(200, "GET", "/api/v1/cms/pages/"+pid, nil)
	if g.Header.Get("ETag") != `"1"` {
		t.Fatalf("etag: %v", g.Header)
	}
	doc := g.JSON()["document"].(map[string]any)
	doc["translations"].(map[string]any)["en"].(map[string]any)["title"] = "Ramadan Promo 2026 " + sfx
	v2 := mk.Must(200, "PUT", "/api/v1/cms/pages/"+pid+"/content", map[string]any{"translations": doc["translations"], "blocks": doc["blocks"],
		"note": "english title"}, "If-Match", `"1"`).JSON()
	if v2["latestVersion"].(float64) != 2 || v2["title"] != "Promo Ramadan "+sfx {
		t.Fatalf("version 2: %v", v2)
	}
	mk.Must(412, "PUT", "/api/v1/cms/pages/"+pid+"/content", map[string]any{"translations": doc["translations"]}, "If-Match", `"1"`)
	if vs := mk.Must(200, "GET", "/api/v1/cms/pages/"+pid+"/versions", nil).Items(); len(vs) != 2 || vs[0]["versionNo"].(float64) != 2 {
		t.Fatalf("versions: %v", vs)
	}
	mk.Must(200, "GET", "/api/v1/cms/pages/"+pid+"/versions/1", nil)
	mk.Must(200, "PATCH", "/api/v1/cms/pages/"+pid, map[string]any{"sortOrder": 5, "showInSitemap": true})
	if r := mk.Do("PATCH", "/api/v1/cms/pages/"+pid, map[string]any{"placement": "home_hero"}); r.Status != 422 {
		t.Fatalf("banner settings on a page: %s", r)
	}

	// Preview (signed, expiring, noindex) before publication.
	pv := mk.Must(200, "POST", "/api/v1/cms/pages/"+pid+":preview-link", map[string]any{"language": "en", "hours": 2}).JSON()
	prev := pub.Must(200, "GET", str(pv["apiPath"]), nil)
	if p := prev.JSON(); p["preview"] != true || p["title"] != "Ramadan Promo 2026 "+sfx || p["seo"].(map[string]any)["noindex"] != true ||
		prev.Header.Get("X-Robots-Tag") == "" {
		t.Fatalf("preview: %v", p)
	}
	pub.Must(404, "GET", str(pv["apiPath"])+"x", nil)
	pub.Must(404, "GET", "/api/v1/public/cms/pages/"+slug+q, nil)

	// Approval before publish (open question #15): Marketing cannot publish
	// alone; the review goes to the approver of the demo workflow.
	if r := mk.Do("POST", "/api/v1/cms/pages/"+pid+":publish", map[string]any{}); r.Status != 409 || !strings.Contains(string(r.Body), "approval_required") {
		t.Fatalf("publish without approval: %s", r)
	}
	sub := mk.Must(200, "POST", "/api/v1/cms/pages/"+pid+":submit", map[string]any{"note": "for Ramadan"}).JSON()
	if sub["status"] != "in_review" || sub["reviewVersion"].(float64) != 2 {
		t.Fatalf("submit: %v", sub)
	}
	mk.Must(409, "PUT", "/api/v1/cms/pages/"+pid+"/content", map[string]any{"translations": doc["translations"]})
	if w := mk.Must(200, "POST", "/api/v1/cms/pages/"+pid+":withdraw", map[string]any{"reason": "one more change"}).JSON(); w["status"] != "draft" {
		t.Fatalf("withdraw: %v", w)
	}
	live := cmsApprove(t, mk, gm, "/api/v1/cms/pages", pid)
	if live["status"] != "published" || live["live"] != true || live["publishedVersion"].(float64) != 2 || live["hasUnpublishedChanges"] == true {
		t.Fatalf("approved page goes live: %v", live)
	}

	// Public page: served language, fallback, SEO, sanitised blocks, data
	// blocks pointing at the owning module's public API (K5).
	r := pub.Must(200, "GET", "/api/v1/public/cms/pages/ramadan-promo-"+sfx+q+"&lang=en", nil)
	p := r.JSON()
	if p["language"] != "en" || p["title"] != "Ramadan Promo 2026 "+sfx || p["path"] != "/en/ramadan-promo-"+sfx || p["fallback"] == true {
		t.Fatalf("public page: %v", p)
	}
	blocks := p["blocks"].([]any)
	rates := cmsFindBlock(blocks, "data")["data"].(map[string]any)
	if rates["owner"] != "commercial" || !strings.HasPrefix(str(rates["url"]), "/api/v1/public/rates/sportclub?") || !strings.Contains(str(rates["url"]), inst.Main.String()) {
		t.Fatalf("rates data block: %v", rates)
	}
	if v := cmsFindBlock(blocks, "video"); v["embedUrl"] != "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ" {
		t.Fatalf("video embed: %v", v)
	}
	if im := cmsFindBlock(blocks, "image"); len(im["media"].([]any)) != 1 || im["media"].([]any)[0].(map[string]any)["alt"] != "Modern Golf clubhouse" {
		t.Fatalf("image block: %v", im)
	}
	if c := cmsFindBlock(blocks, "cta"); c["link"].(map[string]any)["href"] != "/en/book-golf" {
		t.Fatalf("cta: %v", c)
	}
	seo := p["seo"].(map[string]any)
	if len(seo["alternates"].([]any)) != 3 || seo["ogImage"] == "" || p["structuredData"].(map[string]any)["@type"] != "WebPage" {
		t.Fatalf("seo: %v", seo)
	}
	// Rates change in the Pricing Engine show without editing the CMS: the
	// block references the rate table endpoint, which answers live data.
	pub.Must(200, "GET", str(rates["url"]), nil)
	pub.Must(304, "GET", "/api/v1/public/cms/pages/ramadan-promo-"+sfx+q+"&lang=en", nil, "If-None-Match", r.Header.Get("ETag"))
	if fb := pub.Must(200, "GET", "/api/v1/public/cms/pages/"+slug+q+"&lang=fr", nil).JSON(); fb["language"] != "id" || fb["fallback"] != true {
		t.Fatalf("fallback to the default language: %v", fb)
	}
	if pages := pub.Must(200, "GET", "/api/v1/public/cms/pages"+q+"&lang=en", nil).JSON(); !strings.Contains(fmt.Sprint(pages["items"]), "ramadan-promo-"+sfx) {
		t.Fatalf("published pages: %v", pages)
	}
	// The publication asked the website to revalidate both language paths.
	slsDispatch(t, "website revalidated", func() bool { return site.revalidated("/en/ramadan-promo-"+sfx) && site.revalidated("/id/"+slug) })

	// A new version keeps the live one until approved; then rollback.
	doc["translations"].(map[string]any)["en"].(map[string]any)["slug"] = "ramadan-iftar-" + sfx
	mk.Must(200, "PUT", "/api/v1/cms/pages/"+pid+"/content", map[string]any{"translations": doc["translations"], "blocks": doc["blocks"]})
	pub.Must(200, "GET", "/api/v1/public/cms/pages/ramadan-promo-"+sfx+q, nil)
	v3 := cmsApprove(t, mk, gm, "/api/v1/cms/pages", pid)
	if v3["publishedVersion"].(float64) != 3 {
		t.Fatalf("version 3 live: %v", v3)
	}
	pub.Must(200, "GET", "/api/v1/public/cms/pages/ramadan-iftar-"+sfx+q+"&lang=en", nil)
	var redirect string
	for _, rd := range pub.Must(200, "GET", "/api/v1/public/cms/redirects"+q, nil).JSON()["items"].([]any) {
		if m := rd.(map[string]any); m["from"] == "/en/ramadan-promo-"+sfx {
			redirect = str(m["to"])
		}
	}
	if redirect != "/en/ramadan-iftar-"+sfx {
		t.Fatalf("slug change creates a 301 redirect, got %q", redirect)
	}
	mk.Must(422, "POST", "/api/v1/cms/pages/"+pid+":rollback", map[string]any{"versionNo": 2})
	if rb := mk.Must(200, "POST", "/api/v1/cms/pages/"+pid+":rollback", map[string]any{"versionNo": 2, "reason": "wrong slug"}).JSON(); rb["publishedVersion"].(float64) != 4 || rb["live"] != true {
		t.Fatalf("rollback: %v", rb)
	}
	pub.Must(200, "GET", "/api/v1/public/cms/pages/ramadan-promo-"+sfx+q+"&lang=en", nil)
	if rs := mk.Must(200, "POST", "/api/v1/cms/pages/"+pid+":restore", map[string]any{"versionNo": 3}).JSON(); rs["latestVersion"].(float64) != 5 || rs["hasUnpublishedChanges"] != true {
		t.Fatalf("restore: %v", rs)
	}
	// Unpublish (reason required), then publish the approved version again.
	mk.Must(422, "POST", "/api/v1/cms/pages/"+pid+":unpublish", map[string]any{})
	if u := mk.Must(200, "POST", "/api/v1/cms/pages/"+pid+":unpublish", map[string]any{"reason": "Ramadan is over"}).JSON(); u["status"] != "unpublished" || u["live"] == true {
		t.Fatalf("unpublish: %v", u)
	}
	pub.Must(404, "GET", "/api/v1/public/cms/pages/"+slug+q, nil)
	if rp := mk.Must(200, "POST", "/api/v1/cms/pages/"+pid+":publish", map[string]any{"note": "again"}).JSON(); rp["live"] != true || rp["publishedVersion"].(float64) != 4 {
		t.Fatalf("republish the approved version: %v", rp)
	}

	// Scheduling (§9.5): approved content goes live at publishAt and comes
	// down at unpublishAt without manual action.
	cms := inst.App.Content.CMS
	now := time.Now().UTC()
	sched := mk.Must(201, "POST", "/api/v1/cms/pages", map[string]any{"translations": map[string]any{
		"id": map[string]any{"title": "Lebaran " + sfx, "slug": "lebaran-" + sfx}, "en": map[string]any{"title": "Eid " + sfx, "slug": "eid-" + sfx}},
		"blocks":    []map[string]any{{"type": "rich_text", "content": map[string]any{"id": map[string]any{"html": "<p>Selamat Idul Fitri</p>"}}}},
		"publishAt": now.Add(2 * time.Hour).Format(time.RFC3339)}).JSON()
	sid := str(sched["id"])
	if s := cmsApprove(t, mk, gm, "/api/v1/cms/pages", sid); s["status"] != "scheduled" || s["live"] == true {
		t.Fatalf("approved with a future go-live: %v", s)
	}
	if en := mk.Must(200, "GET", "/api/v1/cms/pages/"+sid, nil).JSON()["translations"].(map[string]any)["en"].(map[string]any); en["status"] != "incomplete" {
		t.Fatalf("english text missing in a block: %v", en)
	}
	found := false
	for _, e := range mk.Must(200, "GET", "/api/v1/cms/schedule", nil).Items() {
		found = found || (e["id"] == sid && e["action"] == "publish")
	}
	if !found {
		t.Fatal("schedule lists the go-live")
	}
	pub.Must(404, "GET", "/api/v1/public/cms/pages/lebaran-"+sfx+q, nil)
	if res, err := cms.RunSchedule(context.Background(), now.Add(3*time.Hour)); err != nil || res.Published < 1 {
		t.Fatalf("scheduler go-live: %v %v", res, err)
	}
	pub.Must(200, "GET", "/api/v1/public/cms/pages/lebaran-"+sfx+q, nil)
	mk.Must(200, "POST", "/api/v1/cms/pages/"+sid+":schedule", map[string]any{"unpublishAt": now.Add(5 * time.Hour).Format(time.RFC3339)})
	if res, err := cms.RunSchedule(context.Background(), now.Add(6*time.Hour)); err != nil || res.Unpublished < 1 {
		t.Fatalf("scheduler take-down: %v %v", res, err)
	}
	pub.Must(404, "GET", "/api/v1/public/cms/pages/lebaran-"+sfx+q, nil)
	if s := mk.Must(200, "GET", "/api/v1/cms/pages/"+sid, nil).JSON(); s["status"] != "unpublished" {
		t.Fatalf("taken down: %v", s)
	}
	// A scheduled go-live can be cancelled (unpublish).
	future := mk.Must(201, "POST", "/api/v1/cms/pages", map[string]any{"translations": map[string]any{"id": map[string]any{"title": "Natal " + sfx}},
		"publishAt": now.Add(48 * time.Hour).Format(time.RFC3339)}).JSON()
	cmsApprove(t, mk, gm, "/api/v1/cms/pages", str(future["id"]))
	mk.Must(200, "POST", "/api/v1/cms/pages/"+str(future["id"])+":schedule", map[string]any{"publishAt": now.Add(72 * time.Hour).Format(time.RFC3339)})
	if c := mk.Must(200, "POST", "/api/v1/cms/pages/"+str(future["id"])+":unpublish", map[string]any{"reason": "postponed"}).JSON(); c["status"] != "draft" {
		t.Fatalf("cancelled go-live: %v", c)
	}

	// Rejected review: back to draft, the version is marked rejected.
	rej := mk.Must(201, "POST", "/api/v1/cms/pages", map[string]any{"translations": map[string]any{"id": map[string]any{"title": "Draf " + sfx}}}).JSON()
	rs := mk.Must(200, "POST", "/api/v1/cms/pages/"+str(rej["id"])+":submit", map[string]any{}).JSON()
	gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(rs["approvalRequestId"])+":reject", map[string]any{"reason": "off brand"})
	if v := mk.Must(200, "GET", "/api/v1/cms/pages/"+str(rej["id"])+"/versions/1", nil).JSON(); v["reviewStatus"] != "rejected" {
		t.Fatalf("rejected version: %v", v)
	}
	// Delete: never published → deleted; published → archived (off the website).
	mk.Must(204, "DELETE", "/api/v1/cms/pages/"+str(rej["id"]), nil)
	mk.Must(404, "GET", "/api/v1/cms/pages/"+str(rej["id"]), nil)
	mk.Must(204, "DELETE", "/api/v1/cms/pages/"+sid, nil)
	if l := mk.Must(200, "GET", "/api/v1/cms/pages?includeArchived=true&q=Lebaran", nil).Items(); !containsID(l, sid) {
		t.Fatalf("archived page kept: %v", l)
	}

	// Audit (FR-CMS-10) and the publishing log.
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE entity_id = $1 AND action IN ('create', 'update', 'submit', 'publish', 'rollback', 'unpublish')`,
		[]any{pid}, &n)
	if n < 6 {
		t.Fatalf("audit entries of the page: %d", n)
	}
	log := mk.Must(200, "GET", "/api/v1/cms/publishing-log?filter[contentId]="+pid+"&limit=100", nil).Items()
	actions := map[string]bool{}
	for _, e := range log {
		actions[str(e["action"])] = true
	}
	for _, a := range []string{"created", "saved", "submitted", "withdrawn", "approved", "published", "rolled_back", "restored", "unpublished"} {
		if !actions[a] {
			t.Fatalf("publishing log misses %s: %v", a, actions)
		}
	}
	// The scheduler is also a periodic job (every minute).
	_ = sa
}

// EP-24 FR-CMS-02/04/07/05: News with categories and tags, Gallery albums,
// Banners with display period and target pages (taken down at the end of
// the period), the publication without review where Content Policies allow
// it (MDR), and the translation status per language.
func TestP4CMSNewsGalleryBanners(t *testing.T) {
	sa := superAdmin(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	gm := roleUser(t, inst, "marketing_manager") // website publication approver (PRD P4 §16 #15)
	pub := anon(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	q := "?propertyId=" + inst.Main.String()

	cat := idOf(mk.Must(201, "POST", "/api/v1/cms/categories", map[string]any{"code": "PROMO" + sfx, "name": "Promo", "labels": map[string]any{"en": "Offers"}}))
	if r := mk.Do("POST", "/api/v1/cms/categories", map[string]any{"code": "BAD" + sfx, "name": "Bad", "labels": map[string]any{"fr": "Offres"}}); r.Status != 422 {
		t.Fatalf("labels per website language: %s", r)
	}
	img := str(cmsUpload(t, mk, "news.png", 800, 400, map[string]string{"alt": "Berita"})["id"])
	art := mk.Must(201, "POST", "/api/v1/cms/articles", map[string]any{"categoryId": cat, "tags": []string{"Ramadan", "F&B"}, "authorName": "Marketing",
		"featured": true, "displayDate": "2026-03-01", "translations": map[string]any{
			"id": map[string]any{"title": "Buka Puasa Bersama " + sfx, "summary": "Menu iftar", "mediaId": img},
			"en": map[string]any{"title": "Iftar Together " + sfx, "summary": "Iftar menu"}},
		"blocks": []map[string]any{{"type": "rich_text", "content": map[string]any{"id": map[string]any{"html": "<p>Isi</p>"}, "en": map[string]any{"html": "<p>Body</p>"}}}}}).JSON()
	aid := str(art["id"])
	idSlug := "buka-puasa-bersama-" + sfx
	if art["tags"].([]any)[1] != "f&b" || art["translations"].(map[string]any)["id"].(map[string]any)["slug"] != idSlug {
		t.Fatalf("article: %v", art)
	}
	if r := mk.Do("PATCH", "/api/v1/cms/articles/"+aid, map[string]any{"template": "home"}); r.Status != 422 {
		t.Fatalf("template on an article: %s", r)
	}
	cmsApprove(t, mk, gm, "/api/v1/cms/articles", aid)
	news := pub.Must(200, "GET", "/api/v1/public/cms/news"+q+"&lang=en&category=promo"+sfx, nil).JSON()
	items := news["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["path"] != "/en/news/iftar-together-"+sfx || items[0].(map[string]any)["date"] != "2026-03-01" ||
		items[0].(map[string]any)["category"].(map[string]any)["label"] != "Offers" || items[0].(map[string]any)["image"] == nil {
		t.Fatalf("news list: %v", news)
	}
	if f := pub.Must(200, "GET", "/api/v1/public/cms/news"+q+"&featured=true&tag=ramadan", nil).JSON()["items"].([]any); len(f) != 1 {
		t.Fatalf("featured news by tag: %v", f)
	}
	a := pub.Must(200, "GET", "/api/v1/public/cms/news/"+idSlug+q, nil).JSON()
	if a["structuredData"].(map[string]any)["@type"] != "NewsArticle" || len(a["breadcrumbs"].([]any)) != 2 || a["image"] == nil {
		t.Fatalf("article page: %v", a)
	}

	// Gallery album with captions per language.
	img2 := str(cmsUpload(t, mk, "g2.png", 300, 300, nil)["id"])
	alb := mk.Must(201, "POST", "/api/v1/cms/galleries", map[string]any{"featured": true, "translations": map[string]any{
		"id": map[string]any{"title": "Turnamen " + sfx, "summary": "Foto turnamen"}, "en": map[string]any{"title": "Tournament " + sfx}},
		"items": []map[string]any{{"mediaId": img, "caption": map[string]any{"id": "Pembukaan", "en": "Opening"}}, {"mediaId": img2}}}).JSON()
	if r := mk.Do("POST", "/api/v1/cms/galleries", map[string]any{"translations": map[string]any{"id": map[string]any{"title": "x"}},
		"blocks": []map[string]any{{"type": "rich_text"}}}); r.Status != 422 {
		t.Fatalf("albums have no blocks: %s", r)
	}
	cmsApprove(t, mk, gm, "/api/v1/cms/galleries", str(alb["id"]))
	gl := pub.Must(200, "GET", "/api/v1/public/cms/gallery"+q+"&lang=en", nil).JSON()["items"].([]any)
	if len(gl) == 0 || !strings.Contains(fmt.Sprint(gl), "/en/gallery/tournament-"+sfx) {
		t.Fatalf("albums: %v", gl)
	}
	ga := pub.Must(200, "GET", "/api/v1/public/cms/gallery/tournament-"+sfx+q+"&lang=en", nil).JSON()
	if its := ga["items"].([]any); len(its) != 2 || its[0].(map[string]any)["caption"] != "Opening" {
		t.Fatalf("album: %v", ga)
	}
	// The image is used by live content: it cannot be removed.
	mk.Must(409, "DELETE", "/api/v1/cms/media/"+img, nil)
	spare := str(cmsUpload(t, mk, "spare.png", 64, 64, nil)["id"])
	mk.Must(204, "DELETE", "/api/v1/cms/media/"+spare, nil)

	// Banners: placement, target page, display period (FR-CMS-02).
	home := ""
	for _, p := range mk.Must(200, "GET", "/api/v1/cms/pages?q=home&limit=50", nil).Items() {
		if p["key"] == "home" {
			home = str(p["id"])
		}
	}
	if home == "" {
		t.Fatal("demo home page")
	}
	now := time.Now().UTC()
	bn := mk.Must(201, "POST", "/api/v1/cms/banners", map[string]any{"key": "ramadan-" + sfx, "placement": "home_highlight", "pageIds": []string{home},
		"link": map[string]any{"type": "page", "pageId": home}, "unpublishAt": now.Add(24 * time.Hour).Format(time.RFC3339),
		"translations": map[string]any{"id": map[string]any{"title": "Promo Ramadan", "buttonLabel": "Lihat", "mediaId": img},
			"en": map[string]any{"title": "Ramadan Offer", "buttonLabel": "See"}}}).JSON()
	if r := mk.Do("POST", "/api/v1/cms/banners", map[string]any{"translations": map[string]any{"id": map[string]any{"title": "x"}}}); r.Status != 422 {
		t.Fatalf("a banner needs a placement: %s", r)
	}
	cmsApprove(t, mk, gm, "/api/v1/cms/banners", str(bn["id"]))
	bl := pub.Must(200, "GET", "/api/v1/public/cms/banners"+q+"&lang=en&placement=home_highlight&page=home", nil).JSON()["items"].([]any)
	var banner map[string]any
	for _, b := range bl {
		if b.(map[string]any)["id"] == bn["id"] {
			banner = b.(map[string]any)
		}
	}
	if banner == nil || banner["image"] == nil || banner["link"].(map[string]any)["href"] != "/en" || banner["endsAt"] == nil || banner["buttonLabel"] != "See" {
		t.Fatalf("banner: %v", bl)
	}
	if other := pub.Must(200, "GET", "/api/v1/public/cms/banners"+q+"&placement=home_highlight&page=golf", nil).JSON()["items"].([]any); strings.Contains(fmt.Sprint(other), str(bn["id"])) {
		t.Fatalf("banner shown on another page: %v", other)
	}
	pub.Must(400, "GET", "/api/v1/public/cms/banners"+q+"&placement=sky", nil)
	// The end of the display period takes the banner down (FR-CMS-07 pattern).
	if res, err := inst.App.Content.CMS.RunSchedule(context.Background(), now.Add(25*time.Hour)); err != nil || res.Unpublished < 1 {
		t.Fatalf("banner period end: %v %v", res, err)
	}
	if bl := pub.Must(200, "GET", "/api/v1/public/cms/banners"+q+"&placement=home_highlight", nil).JSON()["items"].([]any); strings.Contains(fmt.Sprint(bl), str(bn["id"])) {
		t.Fatalf("expired banner still shown: %v", bl)
	}

	// MDR: Content Policies without review — Marketing publishes directly.
	mdr := superAdmin(t, inst)
	mdr.Property = inst.MDR
	pol := map[string]any{"requireApproval": false, "defaultLanguage": "id", "supportedLanguages": []string{"id", "en"}, "fallbackToDefault": true,
		"requiredLanguages": []string{"id", "en"}, "previewLinkHours": 24, "autoRedirectOnSlugChange": true, "allowIndexing": false,
		"robotsDisallow": []string{"/booking/"}, "maxUploadMb": 5}
	sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Content Policies", "code": "cms.content", "name": "Website content (MDR)",
		"propertyId": inst.MDR.String(), "value": pol})
	langs := mdr.Must(200, "GET", "/api/v1/cms/languages", nil).JSON()
	if langs["requireApproval"] != false || len(langs["languages"].([]any)) != 2 {
		t.Fatalf("languages of MDR: %v", langs)
	}
	na := mdr.Must(201, "POST", "/api/v1/cms/articles", map[string]any{"translations": map[string]any{"id": map[string]any{"title": "Berita MDR " + sfx}}}).JSON()
	if r := mdr.Do("POST", "/api/v1/cms/articles/"+str(na["id"])+":publish", map[string]any{}); r.Status != 422 || !strings.Contains(string(r.Body), "translation_missing") {
		t.Fatalf("required English translation: %s", r)
	}
	mdr.Must(200, "PUT", "/api/v1/cms/articles/"+str(na["id"])+"/content", map[string]any{"translations": map[string]any{
		"id": map[string]any{"title": "Berita MDR " + sfx}, "en": map[string]any{"title": "MDR News " + sfx}}})
	if p := mdr.Must(200, "POST", "/api/v1/cms/articles/"+str(na["id"])+":publish", map[string]any{}).JSON(); p["live"] != true {
		t.Fatalf("publish without review: %v", p)
	}
	mq := "?propertyId=" + inst.MDR.String()
	pub.Must(200, "GET", "/api/v1/public/cms/news/mdr-news-"+sfx+mq, nil)
	pub.Must(404, "GET", "/api/v1/public/cms/news/mdr-news-"+sfx+q, nil)
	if rb := pub.Must(200, "GET", "/api/v1/public/cms/robots"+mq, nil).JSON(); rb["allowIndexing"] != false || !strings.Contains(str(rb["text"]), "Disallow: /") {
		t.Fatalf("robots of a non-indexed site: %v", rb)
	}
	// Translation status per language (Languages menu).
	ts := mk.Must(200, "GET", "/api/v1/cms/translation-status?filter[kind]=page&filter[language]=en&filter[translation]=missing,incomplete", nil).Items()
	for _, row := range ts {
		if st := row["translations"].(map[string]any)["en"].(map[string]any)["status"]; st != "missing" && st != "incomplete" {
			t.Fatalf("translation filter: %v", row)
		}
	}
	if l := mk.Must(200, "GET", "/api/v1/cms/languages", nil).JSON(); l["defaultLanguage"] != "id" || l["requireApproval"] != true {
		t.Fatalf("languages: %v", l)
	}
}

// FR-CMS-04/08/09 and EP-29: navigation menus (NC §26, ordered, per
// language), redirects, contact information, course guide texts, the
// sitemap / robots / site bootstrap, the data sources registry (K5), the
// cache purge and the import of the old website.
func TestP4CMSSiteData(t *testing.T) {
	mk := roleUser(t, inst, "marketing_staff")
	pub := anon(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	q := "?propertyId=" + inst.Main.String()

	site := pub.Must(200, "GET", "/api/v1/public/cms/site"+q, nil).JSON()
	if site["defaultLanguage"] != "id" || len(site["languages"].([]any)) != 2 {
		t.Fatalf("site: %v", site)
	}
	pub.Must(200, "GET", "/api/v1/public/cms/site?property=main", nil)
	pub.Must(404, "GET", "/api/v1/public/cms/site?property=nope", nil)
	// Demo navigation follows NC §26.
	nav := pub.Must(200, "GET", "/api/v1/public/cms/navigation"+q+"&lang=en&location=header", nil).JSON()
	var labels []string
	for _, mn := range nav["menus"].([]any) {
		for _, it := range mn.(map[string]any)["items"].([]any) {
			labels = append(labels, str(it.(map[string]any)["label"]))
		}
	}
	// Grouped header (P4 fix H proposal; children: TestP4FixWebsiteNavigation).
	if strings.Join(labels, ",") != "Golf,Membership,Facilities,Events & Wedding,Offers,News & Gallery,Contact" {
		t.Fatalf("header navigation: %v", labels)
	}

	// A menu with a page link, a route and an external URL; validated.
	var golf string
	for _, p := range mk.Must(200, "GET", "/api/v1/cms/pages?filter[template]=golf", nil).Items() {
		golf = str(p["id"])
	}
	if r := mk.Do("POST", "/api/v1/cms/menus", map[string]any{"code": "BAD" + sfx, "name": "Bad", "items": []map[string]any{{"label": map[string]any{"en": "Only EN"}, "type": "route", "routeKey": "golf"}}}); r.Status != 422 {
		t.Fatalf("label in the default language: %s", r)
	}
	if r := mk.Do("POST", "/api/v1/cms/menus", map[string]any{"code": "BAD" + sfx, "name": "Bad", "items": []map[string]any{{"label": map[string]any{"id": "x"}, "type": "route", "routeKey": "casino"}}}); r.Status != 422 {
		t.Fatalf("unknown route: %s", r)
	}
	menu := idOf(mk.Must(201, "POST", "/api/v1/cms/menus", map[string]any{"code": "LEGAL" + sfx, "name": "Legal", "location": "legal", "items": []map[string]any{
		{"label": map[string]any{"id": "Golf kami", "en": "Our golf"}, "type": "page", "pageId": golf, "children": []map[string]any{
			{"label": map[string]any{"id": "Pesan"}, "type": "route", "routeKey": "book_golf"}}},
		{"label": map[string]any{"id": "Instagram"}, "type": "url", "url": "https://instagram.com/moderngolf", "newTab": true, "languages": []string{"en"}}}}))
	mk.Must(200, "PATCH", "/api/v1/cms/menus/"+menu, map[string]any{"name": "Legal & links"})
	lg := pub.Must(200, "GET", "/api/v1/public/cms/navigation"+q+"&lang=id&location=legal", nil).JSON()["menus"].([]any)
	var legal map[string]any
	for _, mn := range lg {
		if mn.(map[string]any)["code"] == "LEGAL"+sfx {
			legal = mn.(map[string]any)
		}
	}
	if legal == nil || len(legal["items"].([]any)) != 1 || legal["items"].([]any)[0].(map[string]any)["href"] != "/id/golf" ||
		legal["items"].([]any)[0].(map[string]any)["children"].([]any)[0].(map[string]any)["href"] != "/id/book-golf" {
		t.Fatalf("legal menu in Indonesian (Instagram shown in English only): %v", lg)
	}

	// Redirects: paths normalised, loops rejected.
	rd := idOf(mk.Must(201, "POST", "/api/v1/cms/redirects", map[string]any{"fromPath": "old-promo-" + sfx, "toPath": "/id/promotions"}))
	if r := mk.Do("POST", "/api/v1/cms/redirects", map[string]any{"fromPath": "/id/promotions", "toPath": "/old-promo-" + sfx}); r.Status != 422 || !strings.Contains(string(r.Body), "loop") {
		t.Fatalf("redirect loop: %s", r)
	}
	if r := mk.Do("POST", "/api/v1/cms/redirects", map[string]any{"fromPath": "/same", "toPath": "/same"}); r.Status != 422 {
		t.Fatalf("redirect to itself: %s", r)
	}
	mk.Must(200, "PATCH", "/api/v1/cms/redirects/"+rd, map[string]any{"statusCode": 302, "note": "seasonal"})
	if rs := pub.Must(200, "GET", "/api/v1/public/cms/redirects"+q, nil).JSON()["items"].([]any); !strings.Contains(fmt.Sprint(rs), "/old-promo-"+sfx) {
		t.Fatalf("public redirects: %v", rs)
	}
	mk.Must(204, "DELETE", "/api/v1/cms/redirects/"+rd, nil)

	// Contact information.
	ct := idOf(mk.Must(201, "POST", "/api/v1/cms/contacts", map[string]any{"code": "SPORT" + sfx, "name": "Sport Club Reception", "phones": []string{"+62 21 555 0101"},
		"whatsapp": "0811-2222-3333", "email": "sport@moderngolf.test", "latitude": "-6.19", "longitude": "106.64",
		"openingHours": []map[string]any{{"day": "mon", "open": "06:00", "close": "22:00"}, {"day": "public_holiday", "closed": true}},
		"socialLinks":  map[string]any{"instagram": "https://instagram.com/msc"}, "translations": map[string]any{"en": map[string]any{"note": "Members only"}}}))
	if r := mk.Do("POST", "/api/v1/cms/contacts", map[string]any{"code": "X" + sfx, "name": "X", "openingHours": []map[string]any{{"day": "mon", "open": "22:00", "close": "06:00"}}}); r.Status != 422 {
		t.Fatalf("opening hours: %s", r)
	}
	mk.Must(200, "PATCH", "/api/v1/cms/contacts/"+ct, map[string]any{"address": "Jl. Sport <b>Club</b>"})
	cs := pub.Must(200, "GET", "/api/v1/public/cms/contact"+q+"&lang=en", nil).JSON()["contacts"].([]any)
	var sport map[string]any
	for _, c := range cs {
		if c.(map[string]any)["code"] == "SPORT"+sfx {
			sport = c.(map[string]any)
		}
	}
	if sport == nil || sport["whatsappUrl"] != "https://wa.me/6281122223333" || sport["address"] != "Jl. Sport Club" || sport["note"] != "Members only" {
		t.Fatalf("contact: %v", cs)
	}

	// Course guide texts complete the golf course data.
	img := str(cmsUpload(t, mk, "hole.png", 200, 100, nil)["id"])
	cg := idOf(mk.Must(201, "POST", "/api/v1/cms/course-guides", map[string]any{"courseCode": "west" + sfx[:3], "holeNumber": 7, "title": "Hole 7",
		"translations": map[string]any{"en": map[string]any{"title": "The Lake", "description": "<p>Carry the water<script>x()</script></p>"}}, "mediaIds": []string{img}}))
	mk.Must(200, "PATCH", "/api/v1/cms/course-guides/"+cg, map[string]any{"sortOrder": 7})
	guide := pub.Must(200, "GET", "/api/v1/public/cms/course-guide"+q+"&lang=en&course=west"+sfx[:3], nil).JSON()
	h := guide["holes"].([]any)[0].(map[string]any)
	if h["title"] != "The Lake" || strings.Contains(str(h["description"]), "script") || len(h["media"].([]any)) != 1 || !strings.Contains(str(guide["golfInfo"]), "/public/golf/info") {
		t.Fatalf("course guide: %v", guide)
	}

	// Sitemap (hreflang alternates) and robots.
	sm := pub.Must(200, "GET", "/api/v1/public/cms/sitemap"+q, nil).JSON()["entries"].([]any)
	if !strings.Contains(fmt.Sprint(sm), "/en/golf") || !strings.Contains(fmt.Sprint(sm), "/id/hall-of-fame") {
		t.Fatalf("sitemap: %v", sm)
	}
	xml := pub.Must(200, "GET", "/api/v1/public/cms/sitemap.xml"+q, nil)
	if !strings.Contains(string(xml.Body), "<urlset") || !strings.Contains(string(xml.Body), `hreflang="en"`) {
		t.Fatalf("sitemap.xml: %s", xml.Body)
	}
	if rb := pub.Must(200, "GET", "/api/v1/public/cms/robots"+q, nil).JSON(); !strings.Contains(str(rb["text"]), "Sitemap: ") || rb["allowIndexing"] != true {
		t.Fatalf("robots: %v", rb)
	}

	// Data sources (K5): what a data block can show and who uses it.
	ds := mk.Must(200, "GET", "/api/v1/cms/data-sources", nil).Items()
	keys := map[string]map[string]any{}
	for _, d := range ds {
		keys[str(d["key"])] = d
	}
	for _, k := range []string{"rates", "packages", "promotions", "events", "tournaments", "hall_of_fame", "course_guide", "availability"} {
		if keys[k] == nil {
			t.Fatalf("data source %s: %v", k, ds)
		}
	}
	if len(keys["promotions"]["usedBy"].([]any)) == 0 {
		t.Fatalf("demo pages use promotions: %v", keys["promotions"])
	}
	v := mk.Must(200, "POST", "/api/v1/cms/data-sources:validate", map[string]any{"source": "tournaments", "filter": map[string]any{"id": uuid.NewString(),
		"leaderboard": true}}).JSON()
	if !strings.Contains(str(v["endpoint"]), "/leaderboard") || v["owner"] != "golf" {
		t.Fatalf("validate data block: %v", v)
	}
	if v := mk.Must(200, "POST", "/api/v1/cms/data-sources:validate", map[string]any{"source": "rates", "filter": map[string]any{"line": "golf"}}).JSON(); !strings.Contains(str(v["url"]), "/api/v1/public/golf/rates?") || !strings.Contains(str(v["url"]), "property=") {
		t.Fatalf("golf rates come from golf: %v", v)
	}
	mk.Must(422, "POST", "/api/v1/cms/data-sources:validate", map[string]any{"source": "events", "filter": map[string]any{"from": "tomorrow"}})

	// Purge the website cache (revision bump).
	rev := pub.Must(200, "GET", "/api/v1/public/cms/site"+q, nil).JSON()["revision"].(float64)
	if r := mk.Must(200, "POST", "/api/v1/cms/site:revalidate", nil).JSON(); r["revision"].(float64) <= rev {
		t.Fatalf("revision bump: %v after %v", r, rev)
	}

	// Import of the old website (EP-29): dry run, then idempotent import.
	bundle := map[string]any{"dryRun": true,
		"pages": []map[string]any{{"key": "faq-" + sfx, "translations": map[string]any{"id": map[string]any{"title": "Tanya Jawab " + sfx, "slug": "tanya-jawab-" + sfx}}}},
		"articles": []map[string]any{{"categoryCode": "club", "publishedAt": "2025-12-01T03:00:00Z", "translations": map[string]any{"id": map[string]any{"title": "Arsip " + sfx}}},
			{"categoryCode": "nope", "translations": map[string]any{"id": map[string]any{"title": "Salah " + sfx}}}},
		"redirects": []map[string]any{{"from": "/index.php?page=promo-" + sfx, "to": "/id/promotions"}, {"from": "/same-" + sfx, "to": "/same-" + sfx}}}
	dry := mk.Must(200, "POST", "/api/v1/cms/imports", bundle).JSON()
	if dry["created"].(float64) != 2 || dry["redirects"].(float64) != 1 || len(dry["errors"].([]any)) != 2 {
		t.Fatalf("dry run: %v", dry)
	}
	pub.Must(200, "GET", "/api/v1/public/cms/site"+q, nil)
	if l := mk.Must(200, "GET", "/api/v1/cms/pages?q=faq-"+sfx, nil).Items(); len(l) != 0 {
		t.Fatalf("dry run saved: %v", l)
	}
	bundle["dryRun"] = false
	if res := mk.Must(200, "POST", "/api/v1/cms/imports", bundle).JSON(); res["created"].(float64) != 2 {
		t.Fatalf("import: %v", res)
	}
	if res := mk.Must(200, "POST", "/api/v1/cms/imports", bundle).JSON(); res["updated"].(float64) != 2 || res["created"].(float64) != 0 {
		t.Fatalf("re-import updates: %v", res)
	}
	arch := mk.Must(200, "GET", "/api/v1/cms/articles?q=Arsip+"+sfx, nil).Items()
	if len(arch) != 1 || arch[0]["displayDate"] != "2025-12-01" || arch[0]["status"] != "draft" || arch[0]["latestVersion"].(float64) != 2 {
		t.Fatalf("imported article: %v", arch)
	}
	mk.Must(204, "DELETE", "/api/v1/cms/menus/"+menu, nil)

	// Website reports (FR-RPT-P4-05: one permission per report, Marketing Staff).
	from, to := time.Now().AddDate(0, 0, -30).Format("2006-01-02"), time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	n := 0
	for _, r := range reporting.P4Reports() {
		if r.Module != "cms" {
			continue
		}
		n++
		rows := mk.Must(200, "GET", "/api/v1/reporting/reports/"+r.Code+"?params[from]="+from+"&params[to]="+to, nil).JSON()["rows"].([]any)
		if len(rows) == 0 {
			t.Fatalf("%s has no rows", r.Code)
		}
	}
	pubLog := mk.Must(200, "GET", "/api/v1/reporting/reports/cms.website_publishing?params[from]="+from+"&params[to]="+to+"&params[action]=imported", nil).JSON()
	if n != 3 || !strings.Contains(fmt.Sprint(pubLog["rows"]), "Arsip "+sfx) {
		t.Fatalf("publishing report: %v", pubLog)
	}
	if tr := mk.Must(200, "GET", "/api/v1/reporting/reports/cms.website_translation?params[language]=en&params[translation]=missing", nil).JSON()["rows"].([]any); !strings.Contains(fmt.Sprint(tr), "Tanya Jawab "+sfx) {
		t.Fatalf("translation report: %v", tr)
	}
	roleUser(t, inst, "golf_admin").Must(403, "GET", "/api/v1/reporting/reports/cms.website_content", nil)
}

// EP-25 Integration Layer P4: payment routing per method and property and
// the gateway settlement per method (FR-INT-P4-01), the e-mail sender
// domain check (FR-INT-P4-05), the hardware profiles of the bridge agent
// (FR-INT-P4-04); every call is in the masked integration log (FR-INT-P4-08).
func TestP4CMSIntegrations(t *testing.T) {
	pa := platformAdmin(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)

	routes := pa.Must(200, "GET", "/api/v1/platform/payment-routes", nil).JSON()
	served := map[string]bool{}
	for _, r := range routes["routes"].([]any) {
		m := r.(map[string]any)
		if m["integrationCode"] != nil {
			served[str(m["propertyCode"])+"/"+str(m["method"])] = true
		}
	}
	if !served["MAIN/qris"] || !served["MAIN/virtual_account"] || !served["MAIN/card"] {
		t.Fatalf("payment routes: %v", routes)
	}
	mock := integrationID(t, inst, "mock-payment")
	day := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	st := pa.Must(200, "GET", "/api/v1/platform/integrations/"+mock+"/settlement?from="+day+"&items=true", nil).JSON()
	if len(st["methods"].([]any)) != 3 || st["total"].(map[string]any)["count"].(float64) != 3 || len(st["items"].([]any)) != 3 {
		t.Fatalf("settlement per method: %v", st)
	}
	for _, m := range st["methods"].([]any) {
		mm := m.(map[string]any)
		if bilDec(mm["gross"]).Sub(bilDec(mm["fees"])).Cmp(bilDec(mm["net"])) != 0 {
			t.Fatalf("gross − fees = net: %v", mm)
		}
	}
	pa.Must(422, "GET", "/api/v1/platform/integrations/"+mock+"/settlement?from=2026-01-10&to=2026-01-01", nil)
	// Without a period: the club's yesterday (the UTC yesterday is a day
	// earlier from 17:00 to 24:00 UTC), already settled by the gateway.
	yday := clubDateAgo(inst, 0, 0, 1)
	def := pa.Must(200, "GET", "/api/v1/platform/integrations/"+mock+"/settlement", nil).JSON()
	if def["from"] != yday || def["to"] != yday || def["total"].(map[string]any)["settled"].(float64) != 3 {
		t.Fatalf("default settlement period is the club's yesterday %s, settled: %v", yday, def)
	}

	// E-mail sender domain (SPF / DKIM / DMARC) — DNS answered by a stub.
	old := integration.LookupTXT
	defer func() { integration.LookupTXT = old }()
	dns := map[string][]string{
		"moderngolf.test":                {"v=spf1 include:sendgrid.net -all", "google-site-verification=x"},
		"s1._domainkey.moderngolf.test":  {"v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GN"},
		"s2._domainkey.moderngolf.test":  {"v=DKIM1; k=rsa; p="},
		"_dmarc.moderngolf.test":         {"v=DMARC1; p=quarantine; rua=mailto:dmarc@moderngolf.test"},
		"other.test":                     {"v=spf1 include:mailgun.org ~all"},
		"_dmarc.other.test":              {"v=DMARC1; p=none"},
		"s1._domainkey.other.test":       {"p=MIGf"},
		"s2._domainkey.other.test":       {"p=MIGf"},
		"s1._domainkey.notfound.example": nil,
	}
	integration.LookupTXT = func(_ context.Context, name string) ([]string, error) {
		if v, ok := dns[name]; ok && v != nil {
			return v, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	sg := pa.Must(201, "POST", "/api/v1/platform/integrations", map[string]any{"code": "sendgrid-" + sfx, "adapter": "sendgrid", "name": "SendGrid",
		"mode": "sandbox", "enabled": false, "credentials": map[string]string{"apiKey": "SG.secret-" + sfx},
		"settings": map[string]any{"from": "Modern Golf <no-reply@moderngolf.test>"}}).JSON()
	rep := pa.Must(200, "POST", "/api/v1/platform/integrations/"+str(sg["id"])+":check-email-domain", map[string]any{}).JSON()
	byName := map[string]string{}
	for _, c := range rep["checks"].([]any) {
		m := c.(map[string]any)
		byName[str(m["name"])] = str(m["status"])
	}
	if rep["domain"] != "moderngolf.test" || rep["ok"] != false || byName["moderngolf.test"] != "pass" || byName["s1._domainkey.moderngolf.test"] != "pass" ||
		byName["s2._domainkey.moderngolf.test"] != "invalid" || byName["_dmarc.moderngolf.test"] != "pass" {
		t.Fatalf("domain check: %v", rep)
	}
	other := pa.Must(200, "POST", "/api/v1/platform/integrations/"+str(sg["id"])+":check-email-domain", map[string]any{"domain": "other.test"}).JSON()
	if other["ok"] != false || !strings.Contains(fmt.Sprint(other["checks"]), "does not include sendgrid.net") {
		t.Fatalf("SPF without the provider: %v", other)
	}
	pa.Must(422, "POST", "/api/v1/platform/integrations/"+str(sg["id"])+":check-email-domain", map[string]any{"domain": "not a domain"})
	pa.Must(422, "POST", "/api/v1/platform/integrations/"+mock+":check-email-domain", map[string]any{})
	// The API key never reaches the integration log.
	var logged string
	sysQueryRow(t, inst, `SELECT coalesce(string_agg(request::text || response::text, ' '), '') FROM platform.integration_logs WHERE integration_code = $1`,
		[]any{"sendgrid-" + sfx}, &logged)
	if logged == "" || strings.Contains(logged, "SG.secret") {
		t.Fatalf("integration log: %q", logged)
	}

	// Hardware profiles of the bridge agent.
	hp := pa.Must(200, "GET", "/api/v1/platform/hardware-profiles", nil).JSON()["profiles"].([]any)
	kinds := map[string]string{}
	for _, p := range hp {
		kinds[str(p.(map[string]any)["kind"])] = str(p.(map[string]any)["status"])
	}
	if kinds["locker"] != "available" || kinds["turnstile"] != "available" || kinds["ball_dispenser"] != "available" || kinds["golf_cart_gps"] != "deferred" {
		t.Fatalf("hardware profiles: %v", kinds)
	}
	if err := integration.ValidateHardwareCommand("ball_dispenser", "dispense", map[string]any{"balls": float64(50)}); err != nil {
		t.Fatal(err)
	}
	if err := integration.ValidateHardwareCommand("turnstile", "open", map[string]any{"direction": "sideways"}); err == nil {
		t.Fatal("turnstile direction")
	}
	if err := integration.ValidateHardwareCommand("golf_cart_gps", "stop", nil); err == nil {
		t.Fatal("deferred GPS commands")
	}
}

// FR-CMS-06 for every content type (news articles, banners, gallery
// albums): settings, versions, preview, review (withdraw / approve),
// scheduled take-down, restore, rollback, unpublish / publish and archive.
func TestP4CMSContentLifecycle(t *testing.T) {
	mk := roleUser(t, inst, "marketing_staff")
	gm := roleUser(t, inst, "marketing_manager") // website publication approver (PRD P4 §16 #15)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	img := str(cmsUpload(t, mk, "life.png", 120, 80, nil)["id"])
	for _, k := range []struct {
		path     string
		create   map[string]any
		settings map[string]any
	}{
		{"/api/v1/cms/articles", map[string]any{"translations": map[string]any{"id": map[string]any{"title": "Artikel " + sfx}}}, map[string]any{"featured": true}},
		{"/api/v1/cms/banners", map[string]any{"placement": "announcement_bar", "translations": map[string]any{"id": map[string]any{"title": "Info " + sfx, "mediaId": img}}},
			map[string]any{"placement": "sidebar", "sortOrder": 2}},
		{"/api/v1/cms/galleries", map[string]any{"translations": map[string]any{"id": map[string]any{"title": "Album " + sfx}}, "items": []map[string]any{{"mediaId": img}}},
			map[string]any{"showInSitemap": false}},
	} {
		c := mk.Must(201, "POST", k.path, k.create).JSON()
		cid := str(c["id"])
		mk.Must(200, "PATCH", k.path+"/"+cid, k.settings)
		doc := c["document"].(map[string]any)
		tr := doc["translations"].(map[string]any)
		tr["en"] = map[string]any{"title": "English " + sfx}
		if k.path == "/api/v1/cms/banners" {
			tr["en"].(map[string]any)["buttonLabel"] = "More"
		}
		save := map[string]any{"translations": tr}
		if doc["items"] != nil {
			save["items"] = doc["items"]
		}
		if v := mk.Must(200, "PUT", k.path+"/"+cid+"/content", save).JSON(); v["latestVersion"].(float64) != 2 {
			t.Fatalf("%s save: %v", k.path, v)
		}
		mk.Must(200, "POST", k.path+"/"+cid+":preview-link", map[string]any{})
		mk.Must(200, "POST", k.path+"/"+cid+":submit", map[string]any{})
		mk.Must(200, "POST", k.path+"/"+cid+":withdraw", map[string]any{})
		if live := cmsApprove(t, mk, gm, k.path, cid); live["live"] != true {
			t.Fatalf("%s approved: %v", k.path, live)
		}
		mk.Must(200, "POST", k.path+"/"+cid+":schedule", map[string]any{"unpublishAt": time.Now().Add(240 * time.Hour).Format(time.RFC3339)})
		tr["en"].(map[string]any)["title"] = "English v3 " + sfx
		mk.Must(200, "PUT", k.path+"/"+cid+"/content", save)
		if v := cmsApprove(t, mk, gm, k.path, cid); v["publishedVersion"].(float64) != 3 {
			t.Fatalf("%s version 3: %v", k.path, v)
		}
		if rb := mk.Must(200, "POST", k.path+"/"+cid+":rollback", map[string]any{"versionNo": 2, "reason": "previous text"}).JSON(); rb["publishedVersion"].(float64) != 4 {
			t.Fatalf("%s rollback: %v", k.path, rb)
		}
		mk.Must(200, "POST", k.path+"/"+cid+":restore", map[string]any{"versionNo": 1})
		mk.Must(200, "POST", k.path+"/"+cid+":unpublish", map[string]any{"reason": "season over"})
		if p := mk.Must(200, "POST", k.path+"/"+cid+":publish", map[string]any{}).JSON(); p["live"] != true {
			t.Fatalf("%s republish: %v", k.path, p)
		}
		mk.Must(204, "DELETE", k.path+"/"+cid, nil)
		if r := mk.Do("GET", k.path+"/"+cid, nil); r.Status != 404 {
			t.Fatalf("%s archived: %s", k.path, r)
		}
	}
}
