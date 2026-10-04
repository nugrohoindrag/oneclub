// Package e2e runs the P0 acceptance tests against a real PostgreSQL server:
// every run provisions fresh customer instances with `instance create`,
// starts the API and the River worker in-process and drives them over HTTP.
//
//	ONECLUB_TEST_ADMIN_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable go test ./test/e2e/...
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"

	"oneclub/internal/app"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/obs"
	"oneclub/internal/platform/provision"
)

// Instance is a running test instance.
type Instance struct {
	Code   string
	Res    *provision.CreateResult
	App    *app.App
	DB     *dbtx.DB
	Server *httptest.Server
	Main   uuid.UUID // MAIN property
	MDR    uuid.UUID // second property
	Device string    // demo POS device token
	Origin string
	cancel context.CancelFunc
}

var (
	adminURL string
	inst     *Instance             // primary instance used by most tests
	inst2    *Instance             // second instance for isolation tests
	secrets  = map[string]string{} // email -> TOTP secret
)

func TestMain(m *testing.M) {
	adminURL = os.Getenv("ONECLUB_TEST_ADMIN_URL")
	if adminURL == "" {
		fmt.Println("ONECLUB_TEST_ADMIN_URL not set; skipping e2e tests")
		os.Exit(0)
	}
	obs.SetupLogger(envOr("LOG_LEVEL", "error"), "e2e", "test")
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1e8)
	var err error
	inst, err = start("e2e-" + suffix)
	if err != nil {
		fmt.Println("start instance:", err)
		os.Exit(1)
	}
	inst2, err = start("e2f-" + suffix)
	if err != nil {
		fmt.Println("start instance 2:", err)
		stop(inst)
		os.Exit(1)
	}
	code := m.Run()

	// FR-AUD-01 / EP-07 AC: every mutating route that succeeded during the
	// suite must have written an audit entry, and every mutating route must
	// have been exercised at least once.
	if code == 0 {
		code = checkAudit()
	}
	stop(inst)
	stop(inst2)
	os.Exit(code)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func checkAudit() int {
	if v := inst.App.Server.AuditViolations(); len(v) > 0 {
		fmt.Println("FAIL: mutating routes succeeded without audit entries:", strings.Join(uniq(v), ", "))
		return 1
	}
	exercised := inst.App.Server.ExercisedMutations()
	for k := range inst2.App.Server.ExercisedMutations() {
		exercised[k] = true
	}
	// ONECLUB_COVERAGE_MODULES=banquet,crm limits the check to the routes of
	// those modules (an area running its own tests with -run).
	only := map[string]bool{}
	for _, m := range strings.Split(os.Getenv("ONECLUB_COVERAGE_MODULES"), ",") {
		if m = strings.TrimSpace(m); m != "" {
			only[m] = true
		}
	}
	var missing []string
	total := 0
	for _, rt := range inst.App.Registry.Routes() {
		if !rt.Mutating() || (len(only) > 0 && !only[rt.Module]) {
			continue
		}
		total++
		if !exercised[rt.ID()] {
			missing = append(missing, rt.ID())
		}
	}
	fmt.Printf("audit coverage: %d/%d mutating routes exercised with a successful, audited call\n", total-len(missing), total)
	if len(missing) > 0 && os.Getenv("ONECLUB_REQUIRE_FULL_COVERAGE") != "false" {
		sort.Strings(missing)
		fmt.Println("FAIL: mutating routes never exercised successfully:\n  " + strings.Join(missing, "\n  "))
		return 1
	}
	return 0
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func start(code string) (*Instance, error) {
	ctx := context.Background()
	seeds, err := app.Seeds()
	if err != nil {
		return nil, err
	}
	_ = provision.DropInstance(ctx, adminURL, code)
	res, err := provision.CreateInstance(ctx, provision.CreateOptions{
		AdminURL: adminURL, Code: code, Name: "Test Club " + code, SuperAdminEmail: "superadmin@" + code + ".test",
		PlatformAdminEmail: "platformadmin@" + code + ".test", Seeds: seeds,
	})
	if err != nil {
		return nil, err
	}
	os.Setenv("ONECLUB_ENV", "test")
	cfg, err := config.Load(false)
	if err != nil {
		return nil, err
	}
	cfg.Env = "test"
	cfg.InstanceCode = code
	cfg.DatabaseURL = res.AppURL
	cfg.DatabaseReplicaURL = res.ReportURL
	cfg.AppSecret = res.AppSecret
	cfg.AuditStrict = true
	cfg.Storage.Dir = os.TempDir() + "/oneclub-e2e"
	db, err := dbtx.Open(ctx, cfg.DatabaseURL, cfg.ReplicaURL(), 20)
	if err != nil {
		return nil, err
	}
	a, err := app.Build(cfg, db, app.Options{Worker: true, RetryBase: 200 * time.Millisecond, Concurrency: 10})
	if err != nil {
		return nil, err
	}
	srv := httptest.NewServer(a.Server.Handler())
	cfg.PublicBaseURL = srv.URL
	cfg.AllowedOrigins = append(cfg.AllowedOrigins, srv.URL)
	rctx, cancel := context.WithCancel(context.Background())
	if err := a.Jobs.River.Start(rctx); err != nil {
		cancel()
		return nil, err
	}
	demo, err := app.SeedDemo(ctx, db)
	if err != nil {
		cancel()
		return nil, err
	}
	in := &Instance{Code: code, Res: res, App: a, DB: db, Server: srv, Main: demo.Properties["MAIN"], MDR: demo.Properties["MDR"],
		Device: demo.DeviceToken, Origin: srv.URL, cancel: cancel}
	if err := createMatrixUsers(in); err != nil {
		cancel()
		return nil, err
	}
	return in, nil
}

func stop(in *Instance) {
	if in == nil {
		return
	}
	sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
	_ = in.App.Jobs.River.Stop(sctx)
	c()
	in.cancel()
	in.App.Hub.Stop()
	in.Server.Close()
	in.DB.Close()
	if os.Getenv("ONECLUB_KEEP_TEST_DB") == "" {
		_ = provision.DropInstance(context.Background(), adminURL, in.Code)
	}
}

// createMatrixUsers creates one user per role template ("role.<code>@matrix.test").
func createMatrixUsers(in *Instance) error {
	ctx := dbtx.System(context.Background())
	return in.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO platform.users (id, email, full_name, password_hash, password_changed_at)
			SELECT gen_random_uuid(), 'role.' || r.code || '@matrix.test', r.name || ' (matrix)', u.password_hash, now()
			FROM platform.roles r, (SELECT password_hash FROM platform.users WHERE email = 'gm@demo.oneclub.id') u
			WHERE r.is_template ON CONFLICT DO NOTHING`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO platform.role_assignments (id, user_id, role_id, property_id)
			SELECT gen_random_uuid(), u.id, r.id, CASE WHEN r.scope = 'property' THEN $1::uuid ELSE NULL END
			FROM platform.roles r JOIN platform.users u ON u.email = 'role.' || r.code || '@matrix.test'
			WHERE r.is_template ON CONFLICT DO NOTHING`, in.Main)
		return err
	})
}

// ── HTTP client ───────────────────────────────────────────────────────────

// Client is a logged-in (or anonymous) API client.
type Client struct {
	t        testing.TB
	in       *Instance
	http     *http.Client
	Property uuid.UUID
	Email    string
	Bearer   string
}

// Resp is an API response.
type Resp struct {
	Status int
	Body   []byte
	Header http.Header
}

// JSON decodes the body into a map.
func (r Resp) JSON() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	return m
}

// Items returns body.items.
func (r Resp) Items() []map[string]any {
	var p struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(r.Body, &p)
	return p.Items
}

func (r Resp) String() string { return fmt.Sprintf("%d %s", r.Status, string(r.Body)) }

func anon(t testing.TB, in *Instance) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{t: t, in: in, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}, Property: in.Main}
}

// Do performs a request; body may be nil, []byte (raw) or any JSON value.
func (c *Client) Do(method, path string, body any, hdr ...string) Resp {
	c.t.Helper()
	var rd io.Reader
	ctype := "application/json"
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.in.Server.URL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", ctype)
	}
	if c.Property != uuid.Nil {
		req.Header.Set("X-Property-Id", c.Property.String())
	}
	if c.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.Bearer)
	}
	req.Header.Set("Origin", c.in.Origin)
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return Resp{Status: resp.StatusCode, Body: b, Header: resp.Header}
}

// Must asserts a status.
func (c *Client) Must(want int, method, path string, body any, hdr ...string) Resp {
	c.t.Helper()
	r := c.Do(method, path, body, hdr...)
	if r.Status != want {
		c.t.Fatalf("%s %s: want %d, got %s", method, path, want, r.String())
	}
	return r
}

const demoPassword = app.DemoPassword

// login logs in and completes MFA enrolment/verification when required.
func login(t testing.TB, in *Instance, email, pw string) *Client {
	t.Helper()
	c := anon(t, in)
	c.Email = email
	r := c.Must(200, "POST", "/api/v1/auth/login", map[string]any{"email": email, "password": pw})
	lr := r.JSON()
	if lr["mfaRequired"] == true {
		key := in.Code + "/" + email
		sec, ok := secrets[key]
		if !ok || lr["mfaEnrolled"] != true {
			s := c.Must(200, "POST", "/api/v1/auth/mfa/setup", nil).JSON()
			sec = s["secret"].(string)
			secrets[key] = sec
		}
		code, err := totp.GenerateCode(sec, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		c.Must(204, "POST", "/api/v1/auth/mfa/verify", map[string]any{"code": code})
	}
	return c
}

// roleUser logs in the matrix user of a role template.
func roleUser(t testing.TB, in *Instance, role string) *Client {
	return login(t, in, "role."+role+"@matrix.test", demoPassword)
}

// superAdmin logs in as the matrix Super Admin.
func superAdmin(t testing.TB, in *Instance) *Client { return roleUser(t, in, "super_admin") }

// platformAdmin logs in as the matrix Platform Admin.
func platformAdmin(t testing.TB, in *Instance) *Client { return roleUser(t, in, "platform_admin") }

// waitFor polls cond until true or timeout.
func waitFor(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// sysQuery runs a query with an all-properties scope.
func sysQueryRow(t testing.TB, in *Instance, sql string, args []any, dest ...any) {
	t.Helper()
	ctx := dbtx.System(context.Background())
	err := in.DB.WithReadTx(ctx, func(tx pgx.Tx) error { return tx.QueryRow(ctx, sql, args...).Scan(dest...) })
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
}

func str(v any) string { return fmt.Sprint(v) }
