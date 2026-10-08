package e2e

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/instance"
)

// EP-01 AC: two instances cannot read each other's data — neither through a
// database connection nor through the API.
func TestInstanceIsolation(t *testing.T) {
	ctx := context.Background()
	// The app role of instance A cannot connect to instance B's database.
	u, _ := url.Parse(inst.Res.AppURL)
	u.Path = "/" + strings.TrimPrefix(mustParse(t, inst2.Res.AppURL).Path, "/")
	if conn, err := pgx.Connect(ctx, u.String()); err == nil {
		conn.Close(ctx)
		t.Fatal("instance A credentials connected to instance B database")
	}
	// The reporting role of A cannot either.
	r, _ := url.Parse(inst.Res.ReportURL)
	r.Path = mustParse(t, inst2.Res.ReportURL).Path
	if conn, err := pgx.Connect(ctx, r.String()); err == nil {
		conn.Close(ctx)
		t.Fatal("instance A report role connected to instance B database")
	}
	// A session of instance A is meaningless on instance B.
	a := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	cookie := a.http.Jar.Cookies(mustParse(t, inst.Server.URL))
	b := anon(t, inst2)
	b.http.Jar.SetCookies(mustParse(t, inst2.Server.URL), cookie)
	b.Must(401, "GET", "/api/v1/auth/me", nil)
	// Data stays separate: a venue created in A is not visible in B.
	sa := superAdmin(t, inst)
	sa.Must(201, "POST", "/api/v1/platform/properties", map[string]any{"code": "ISOLATED", "name": "Only in A"})
	sb := superAdmin(t, inst2)
	for _, p := range sb.Must(200, "GET", "/api/v1/platform/properties", nil).Items() {
		if p["code"] == "ISOLATED" {
			t.Fatal("instance B sees instance A data")
		}
	}
	if sa.Must(200, "GET", "/api/v1/public/bootstrap", nil).JSON()["code"] == sb.Must(200, "GET", "/api/v1/public/bootstrap", nil).JSON()["code"] {
		t.Fatal("instances share configuration")
	}
}

// The public website books into the instance's first property, which the
// bootstrap lists first — the property the CMS site resolves to as well, not
// the alphabetically first one (demo: "Modern Driving Range" sorts before MAIN).
func TestBootstrapListsTheFirstPropertyFirst(t *testing.T) {
	props, _ := anon(t, inst).Must(200, "GET", "/api/v1/public/bootstrap", nil).JSON()["properties"].([]any)
	if len(props) < 2 {
		t.Fatalf("bootstrap lists %d properties, want the demo's two", len(props))
	}
	if code := props[0].(map[string]any)["code"]; code != "MAIN" {
		t.Fatalf("bootstrap lists %v first, want MAIN", code)
	}
}

func mustParse(t testing.TB, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// EP-01 AC: disabling a module hides its menus in every shell and its API
// answers 403.
func TestDisableModule(t *testing.T) {
	pa := platformAdmin(t, inst2)
	gm := login(t, inst2, "gm@demo.oneclub.id", demoPassword)
	golfMgr := login(t, inst2, "golf.manager@demo.oneclub.id", demoPassword)
	golfMgr.Must(200, "GET", "/api/v1/golf/courses", nil)
	hasKey := func(c *Client, shell, key string) bool {
		var walk func(items []any) bool
		walk = func(items []any) bool {
			for _, it := range items {
				m := it.(map[string]any)
				if m["key"] == key {
					return true
				}
				if ch, ok := m["children"].([]any); ok && walk(ch) {
					return true
				}
			}
			return false
		}
		return walk(c.Must(200, "GET", "/api/v1/platform/navigation?shell="+shell, nil).JSON()["items"].([]any))
	}
	if !hasKey(gm, "backoffice", "golf") || !hasKey(gm, "management", "golf-performance") {
		t.Fatal("golf menus should be visible before disabling")
	}
	// Super Admin cannot toggle modules (Platform Admin only).
	superAdmin(t, inst2).Must(403, "PUT", "/api/v1/platform/modules", map[string]any{"modules": []map[string]any{{"code": "golf", "enabled": false}}})
	pa.Must(422, "PUT", "/api/v1/platform/modules", map[string]any{"modules": []map[string]any{{"code": "platform", "enabled": false}}})
	pa.Must(200, "PUT", "/api/v1/platform/modules", map[string]any{"modules": []map[string]any{{"code": "golf", "enabled": false}}})
	golfMgr.Must(403, "GET", "/api/v1/golf/courses", nil)
	if r := golfMgr.Do("GET", "/api/v1/golf/courses", nil); !strings.Contains(string(r.Body), "module_disabled") {
		t.Fatalf("expected module_disabled: %s", r)
	}
	if hasKey(gm, "backoffice", "golf") || hasKey(gm, "management", "golf-performance") || hasKey(superAdmin(t, inst2), "backoffice", "courses") {
		t.Fatal("golf menus still visible after disabling")
	}
	for _, m := range anon(t, inst2).Must(200, "GET", "/api/v1/public/bootstrap", nil).JSON()["enabledModules"].([]any) {
		if m == "golf" {
			t.Fatal("bootstrap still lists golf")
		}
	}
	pa.Must(200, "PUT", "/api/v1/platform/modules", map[string]any{"modules": []map[string]any{{"code": "golf", "enabled": true}}})
	golfMgr.Must(200, "GET", "/api/v1/golf/courses", nil)
}

// FR-INS-03: a suspended instance only serves Platform Admin.
func TestSuspendInstance(t *testing.T) {
	pa := platformAdmin(t, inst2)
	gm := login(t, inst2, "gm@demo.oneclub.id", demoPassword)
	pa.Must(200, "PATCH", "/api/v1/platform/instance", map[string]any{"status": "suspended"})
	gm.Must(503, "GET", "/api/v1/reporting/dashboards/executive-overview", nil)
	pa.Must(200, "GET", "/api/v1/platform/venues", nil)
	pa.Must(200, "PATCH", "/api/v1/platform/instance", map[string]any{"status": "active", "name": "Test Club Two"})
	gm.Must(200, "GET", "/api/v1/reporting/dashboards/executive-overview", nil)
	superAdmin(t, inst2).Must(403, "PATCH", "/api/v1/platform/instance", map[string]any{"name": "nope"})
}

// G6 / FR-TEC-04: Row Level Security is enabled on every table with a
// property_id column, and actually filters rows for the application role.
func TestRLSOnEveryPropertyTable(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, inst.Res.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `
		SELECT c.table_schema || '.' || c.table_name, cl.relrowsecurity
		FROM information_schema.columns c
		JOIN pg_class cl ON cl.relname = c.table_name
		JOIN pg_namespace n ON n.oid = cl.relnamespace AND n.nspname = c.table_schema
		WHERE c.column_name = 'property_id' AND cl.relkind IN ('r', 'p')
		  AND c.table_schema NOT IN ('reporting')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		var rls bool
		_ = rows.Scan(&name, &rls)
		tables = append(tables, name)
		if !rls && name != "audit.audit_log" && !strings.HasPrefix(name, "audit.audit_log_") && name != "platform.role_assignments" &&
			name != "platform.sessions" && name != "platform.api_keys" && name != "platform.rules" && name != "platform.approval_workflows" &&
			name != "platform.imports" && name != "platform.outbox" && name != "reporting.exports" {
			t.Errorf("table %s has property_id but RLS is disabled", name)
		}
	}
	rows.Close()
	if len(tables) < 15 {
		t.Fatalf("expected many property tables, got %v", tables)
	}
	// With the scope of MAIN only, MDR rows are invisible and cannot be written.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	scoped := dbtx.WithScope(ctx, dbtx.Scope{PropertyIDs: []uuid.UUID{inst.Main}})
	if err := dbtx.ApplyScope(scoped, tx); err != nil {
		t.Fatal(err)
	}
	var mdrVenues, allVenues int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM platform.venues WHERE property_id = $1`, inst.MDR).Scan(&mdrVenues)
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM platform.venues`).Scan(&allVenues)
	if mdrVenues != 0 || allVenues == 0 {
		t.Fatalf("RLS leak: MDR venues visible=%d, total=%d", mdrVenues, allVenues)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.venues (id, property_id, code, name) VALUES (gen_random_uuid(), $1, 'HACK', 'Hack')`, inst.MDR); err == nil {
		t.Fatal("RLS allowed writing into another property")
	}
	_ = tx.Rollback(ctx)
	// Without any scope the application role sees nothing.
	tx2, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(ctx)
	var none int
	_ = tx2.QueryRow(ctx, `SELECT count(*) FROM platform.venues`).Scan(&none)
	if none != 0 {
		t.Fatalf("unscoped app role sees %d venues", none)
	}
}

// FR-INS-05: feature flags readable by backend and frontend.
func TestFeatureFlags(t *testing.T) {
	pa := platformAdmin(t, inst)
	superAdmin(t, inst).Must(403, "PUT", "/api/v1/platform/feature-flags/demo.flag", map[string]any{"value": true})
	pa.Must(422, "PUT", "/api/v1/platform/feature-flags/demo.flag", map[string]any{"value": "yes", "valueType": "boolean"})
	pa.Must(200, "PUT", "/api/v1/platform/feature-flags/demo.flag", map[string]any{"value": true, "description": "Demo"})
	pa.Must(200, "PUT", "/api/v1/platform/feature-flags/demo.limit", map[string]any{"value": 5, "valueType": "number", "clientVisible": false})
	flags := anon(t, inst).Must(200, "GET", "/api/v1/public/bootstrap", nil).JSON()["flags"].(map[string]any)
	if flags["demo.flag"] != true {
		t.Fatalf("client-visible flag missing: %v", flags)
	}
	if _, ok := flags["demo.limit"]; ok {
		t.Fatal("server-only flag leaked to bootstrap")
	}
	if !inst.App.Instance.FlagBool(context.Background(), inst.DB.Primary, "demo.flag") {
		t.Fatal("backend cannot read flag")
	}
	superAdmin(t, inst).Must(200, "GET", "/api/v1/platform/feature-flags", nil)
}

// FR-INS-06: custom domain verification by DNS TXT, then allowed for TLS.
func TestCustomDomain(t *testing.T) {
	pa := platformAdmin(t, inst)
	d := pa.Must(201, "POST", "/api/v1/platform/domains", map[string]any{"surface": "web", "hostname": "Booking.ModernGolf.example"}).JSON()
	if d["hostname"] != "booking.moderngolf.example" {
		t.Fatalf("hostname not normalised: %v", d)
	}
	pa.Must(422, "POST", "/api/v1/platform/domains", map[string]any{"surface": "web", "hostname": "not a host"})
	pa.Must(422, "POST", "/api/v1/platform/domains", map[string]any{"surface": "staff", "hostname": "staff.moderngolf.example"})
	anon(t, inst).Must(404, "GET", "/api/v1/public/domains/allowed?domain=booking.moderngolf.example", nil)
	instance.Resolver = func(ctx context.Context, name string) ([]string, error) { return []string{"something-else"}, nil }
	if v := pa.Must(200, "POST", "/api/v1/platform/domains/"+str(d["id"])+":verify", nil).JSON(); v["status"] != "failed" {
		t.Fatalf("expected failed: %v", v)
	}
	var records []string
	instance.Resolver = func(ctx context.Context, name string) ([]string, error) { return records, nil }
	verify := func(d map[string]any) {
		t.Helper()
		records = []string{str(d["verificationRecordValue"])}
		if v := pa.Must(200, "POST", "/api/v1/platform/domains/"+str(d["id"])+":verify", nil).JSON(); v["status"] != "active" {
			t.Fatalf("expected active: %v", v)
		}
	}
	verify(d)
	// Caddy asks before issuing the certificate; the answer also names the
	// application to serve there (Technical Doc §6.1).
	if r := anon(t, inst).Must(200, "GET", "/api/v1/public/domains/allowed?domain=booking.moderngolf.example", nil); r.Header.Get("X-Surface") != "web" {
		t.Fatalf("surface of a website domain: %s", r)
	}
	// Staff App surfaces; the surfaces of the former staff apps stay accepted
	// and are served as their Staff App surface.
	for surface, served := range map[string]string{"dashboard": "dashboard", "cashier": "cashier", "caddy": "caddy", "kitchen": "kitchen", "presence": "presence",
		"backoffice": "dashboard", "platform-admin": "dashboard", "ops": "cashier"} {
		host := surface + ".moderngolf.example"
		s := pa.Must(201, "POST", "/api/v1/platform/domains", map[string]any{"surface": surface, "hostname": host}).JSON()
		verify(s)
		r := anon(t, inst).Must(200, "GET", "/api/v1/public/domains/allowed?domain="+host, nil)
		if r.Header.Get("X-Surface") != served || r.JSON()["surface"] != served {
			t.Fatalf("%s is served as %s: %s", surface, served, r)
		}
		pa.Must(204, "DELETE", "/api/v1/platform/domains/"+str(s["id"]), nil)
	}
	// A custom domain proxies /api like the instance's own domains: a
	// request from the page it serves passes the CSRF check, another site's does not.
	cashier := roleUser(t, inst, "cashier")
	from := func(origin, host string) int {
		t.Helper()
		req, _ := http.NewRequest("PATCH", inst.Server.URL+"/api/v1/auth/me/preferences", strings.NewReader(`{"locale":"id"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Property-Id", inst.Main.String())
		req.Header.Set("Origin", origin)
		for _, ck := range cashier.http.Jar.Cookies(req.URL) {
			req.AddCookie(ck) // the jar would match the cookie against Host
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := from("https://pos.moderngolf.example", "pos.moderngolf.example"); got != 204 {
		t.Fatalf("same-origin request from a custom domain: %d", got)
	}
	if got := from("https://evil.example", "pos.moderngolf.example"); got != 403 {
		t.Fatalf("cross-site request: %d", got)
	}
	pa.Must(200, "GET", "/api/v1/platform/domains", nil)
	pa.Must(204, "DELETE", "/api/v1/platform/domains/"+str(d["id"]), nil)
}

// Technical Doc §6.1: GET /auth/me lists the Staff App areas of the user's
// roles. The Screen role opens only the Clubhouse Screen; the Kitchen
// Display is for kitchen staff, not for cashiers who view kitchen orders.
func TestStaffAppAreas(t *testing.T) {
	for role, want := range map[string][]string{
		"screen":          {"screen"},
		"kitchen_staff":   {"ops", "kitchen"},
		"cashier":         {"ops"},
		"caddy":           {"ops", "caddy"},
		"general_manager": {"backoffice", "management"},
	} {
		var got []string
		for _, s := range roleUser(t, inst, role).Must(200, "GET", "/api/v1/auth/me", nil).JSON()["shells"].([]any) {
			got = append(got, str(s))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: shells %v, want %v", role, got, want)
		}
	}
	screen := roleUser(t, inst, "screen")
	screen.Must(403, "GET", "/api/v1/golf/courses", nil) // no Back Office data
	screen.Must(200, "GET", "/api/v1/public/hall-of-fame/kiosk?propertyId="+inst.Main.String(), nil)
	// The Clubhouse Screen and the Kitchen Display left the Operational menu.
	for _, it := range superAdmin(t, inst).Must(200, "GET", "/api/v1/platform/navigation?shell=ops", nil).JSON()["items"].([]any) {
		if k := it.(map[string]any)["key"]; k == "kitchen" || k == "clubhouse-screen" {
			t.Errorf("ops menu still has %v", k)
		}
	}
}

// FR-BRD-01..03: branding with preset or custom accent passing WCAG AA.
func TestBrandingAndLocalization(t *testing.T) {
	sa := superAdmin(t, inst)
	sa.Must(422, "PATCH", "/api/v1/platform/branding", map[string]any{"accent": "teal"})
	sa.Must(422, "PATCH", "/api/v1/platform/branding", map[string]any{"accent": "custom", "primaryColor": "zzz"})
	prev := sa.Must(200, "POST", "/api/v1/platform/branding/accent-preview", map[string]any{"primaryColor": "#0B6E4F"}).JSON()
	if len(prev["checks"].([]any)) != 4 {
		t.Fatalf("preview checks: %v", prev)
	}
	b := sa.Must(200, "PATCH", "/api/v1/platform/branding", map[string]any{"appName": "Modern Golf", "emailSenderName": "Modern Golf Club",
		"accent": "custom", "primaryColor": "#0B6E4F", "logoUrl": "https://cdn.example/logo.png"}).JSON()
	if b["customAccent"] == nil {
		t.Fatalf("custom accent tokens missing: %v", b)
	}
	pub := anon(t, inst).Must(200, "GET", "/api/v1/public/branding", nil).JSON()
	if pub["appName"] != "Modern Golf" || pub["accent"] != "custom" {
		t.Fatalf("public branding: %v", pub)
	}
	sa.Must(200, "PATCH", "/api/v1/platform/branding", map[string]any{"accent": "blue"})
	// File upload (logo) is served publicly.
	// Localization (FR-L10N-02).
	sa.Must(422, "PATCH", "/api/v1/platform/localization", map[string]any{"timezone": "Mars/Base"})
	l := sa.Must(200, "PATCH", "/api/v1/platform/localization", map[string]any{"defaultLocale": "en", "timezone": "Asia/Makassar", "currency": "IDR", "defaultTheme": "dark"}).JSON()
	if l["timezone"] != "Asia/Makassar" || l["defaultTheme"] != "dark" {
		t.Fatalf("localization: %v", l)
	}
	sa.Must(200, "PATCH", "/api/v1/platform/localization", map[string]any{"defaultLocale": "id", "timezone": "Asia/Jakarta", "defaultTheme": "light"})
	sa.Must(200, "GET", "/api/v1/platform/instance", nil)
	sa.Must(200, "GET", "/api/v1/platform/modules", nil)
}
