package e2e

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/app"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
)

// EP-03 AC: a role that requires MFA is forced to set up MFA before anything else.
func TestMFAEnforcedForAdminRoles(t *testing.T) {
	c := anon(t, inst)
	r := c.Must(200, "POST", "/api/v1/auth/login", map[string]any{"email": "property.admin@demo.oneclub.id", "password": demoPassword}).JSON()
	if r["mfaRequired"] != true {
		t.Fatalf("property admin must require MFA: %v", r)
	}
	// Before MFA: only MFA endpoints are reachable.
	if got := c.Do("GET", "/api/v1/platform/venues", nil); got.Status != 403 || !strings.Contains(string(got.Body), "mfa_required") {
		t.Fatalf("expected mfa_required, got %s", got)
	}
	me := c.Must(200, "GET", "/api/v1/auth/me", nil).JSON()
	if me["mfaPending"] != true || len(me["shells"].([]any)) != 0 {
		t.Fatalf("me before MFA: %v", me)
	}
	// Wrong code is rejected and audited.
	c.Must(200, "POST", "/api/v1/auth/mfa/setup", nil)
	c.Must(422, "POST", "/api/v1/auth/mfa/verify", map[string]any{"code": "000000"})
	// Full login with enrolment works and unlocks the API.
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	pa.Must(200, "GET", "/api/v1/platform/venues", nil)
	// Second login asks for the existing TOTP (no re-enrolment).
	again := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	if m := again.Must(200, "GET", "/api/v1/auth/me", nil).JSON(); m["mfaEnabled"] != true || m["mfaPending"] != false {
		t.Fatalf("me after MFA: %v", m)
	}
	// Roles without MFA requirement log in directly.
	if r := anon(t, inst).Must(200, "POST", "/api/v1/auth/login", map[string]any{"email": "gm@demo.oneclub.id", "password": demoPassword}).JSON(); r["mfaRequired"] != false {
		t.Fatalf("general manager should not require MFA: %v", r)
	}
}

// The first Super Admin has a temporary password that must be changed.
func TestTemporaryPasswordMustBeChanged(t *testing.T) {
	email := "superadmin@" + inst.Code + ".test"
	c := login(t, inst, email, inst.Res.SuperAdminPassword)
	if got := c.Do("GET", "/api/v1/platform/users", nil); got.Status != 403 || !strings.Contains(string(got.Body), "password_change_required") {
		t.Fatalf("expected password_change_required, got %s", got)
	}
	c.Must(422, "POST", "/api/v1/auth/password/change", map[string]any{"currentPassword": inst.Res.SuperAdminPassword, "newPassword": "short"})
	c.Must(204, "POST", "/api/v1/auth/password/change", map[string]any{"currentPassword": inst.Res.SuperAdminPassword, "newPassword": "Club#Super2026x"})
	c.Must(200, "GET", "/api/v1/platform/users", nil)
}

// FR-IAM-04: lockout after repeated failures; unknown e-mails look the same.
func TestLockoutAndNoEnumeration(t *testing.T) {
	sa := superAdmin(t, inst)
	u := sa.Must(201, "POST", "/api/v1/platform/users", map[string]any{"email": "lockme@demo.test", "fullName": "Lock Me", "password": "Lock#Me2026xx",
		"assignments": []map[string]any{{"roleId": roleID(t, sa, "golf_staff"), "propertyId": inst.Main}}}).JSON()
	c := anon(t, inst)
	unknown := c.Do("POST", "/api/v1/auth/login", map[string]any{"email": "nobody@demo.test", "password": "x"})
	bad := c.Do("POST", "/api/v1/auth/login", map[string]any{"email": "lockme@demo.test", "password": "wrong"})
	if unknown.Status != 401 || bad.Status != 401 || unknown.JSON()["code"] != bad.JSON()["code"] {
		t.Fatalf("unknown vs bad password must be indistinguishable: %s / %s", unknown, bad)
	}
	for i := 0; i < 4; i++ {
		c.Do("POST", "/api/v1/auth/login", map[string]any{"email": "lockme@demo.test", "password": "wrong"})
	}
	if r := c.Do("POST", "/api/v1/auth/login", map[string]any{"email": "lockme@demo.test", "password": "Lock#Me2026xx"}); r.Status != 423 {
		t.Fatalf("expected locked, got %s", r)
	}
	// Admin re-activation clears the lock.
	sa.Must(200, "POST", "/api/v1/platform/users/"+str(u["id"])+":activate", map[string]any{"reason": "unlock"})
	login(t, inst, "lockme@demo.test", "Lock#Me2026xx")
}

func TestPasswordResetFlow(t *testing.T) {
	c := anon(t, inst)
	c.Must(202, "POST", "/api/v1/auth/password/reset", map[string]any{"email": "nobody@nowhere.test"})
	c.Must(202, "POST", "/api/v1/auth/password/reset", map[string]any{"email": "golf.manager@demo.oneclub.id"})
	// The reset link is delivered through the notification service.
	var body string
	waitFor(t, 10*time.Second, "reset e-mail", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries d JOIN platform.users u ON u.id = d.user_id
			WHERE u.email = 'golf.manager@demo.oneclub.id' AND d.event_code = 'auth.password_reset'`, nil, &n)
		if n == 0 {
			return false
		}
		sysQueryRow(t, inst, `SELECT d.body FROM platform.notification_deliveries d JOIN platform.users u ON u.id = d.user_id
			WHERE u.email = 'golf.manager@demo.oneclub.id' AND d.event_code = 'auth.password_reset' ORDER BY d.created_at DESC LIMIT 1`, nil, &body)
		return true
	})
	m := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no token in reset e-mail: %q", body)
	}
	// Indonesian template is used for an "id" locale user (FR-L10N-01).
	if !strings.Contains(body, "kata sandi") {
		t.Fatalf("expected Indonesian template, got %q", body)
	}
	old := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	c.Must(422, "POST", "/api/v1/auth/password/reset/confirm", map[string]any{"token": m[1], "newPassword": "weak"})
	c.Must(204, "POST", "/api/v1/auth/password/reset/confirm", map[string]any{"token": m[1], "newPassword": "Golf#Reset2026"})
	c.Must(422, "POST", "/api/v1/auth/password/reset/confirm", map[string]any{"token": m[1], "newPassword": "Golf#Reset2027"}) // single use
	old.Must(401, "GET", "/api/v1/auth/me", nil)                                                                               // sessions revoked
	login(t, inst, "golf.manager@demo.oneclub.id", "Golf#Reset2026")
	// restore for other tests
	sa := superAdmin(t, inst)
	_ = sa
	ctx := dbtx.System(context.Background())
	_ = inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.users SET password_hash = (SELECT password_hash FROM platform.users WHERE email = 'gm@demo.oneclub.id')
			WHERE email = 'golf.manager@demo.oneclub.id'`)
		return err
	})
}

// EP-03 AC: matrix test — for every role template, every endpoint outside
// its permissions answers 403, and endpoints within never answer 403 for
// lack of permission.
func TestRolePermissionMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("matrix is long")
	}
	routes := inst.App.Registry.Routes()
	param := regexp.MustCompile(`\{[^}]+\}`)
	roles := inst.App.Catalog.Roles
	for _, role := range roles {
		role := role
		t.Run(role.Code, func(t *testing.T) {
			c := roleUser(t, inst, role.Code)
			held := map[string]bool{}
			for _, p := range role.Permissions {
				held[p] = true
			}
			for _, rt := range routes {
				// SSE streams never end; their permission is covered by TestRealtimeTeeSheet.
				if rt.Permission == "" || rt.Auth != route.AuthRequired || rt.RawContent == "text/event-stream" {
					continue
				}
				path := param.ReplaceAllString(rt.Path, uuid.NewString())
				if strings.Contains(rt.Path, "{id}") && strings.HasPrefix(rt.Path, "/api/v1/platform/jobs/") {
					path = param.ReplaceAllString(rt.Path, "999999999")
				}
				if strings.Contains(rt.Path, "{key}") {
					path = param.ReplaceAllString(rt.Path, "matrix.flag")
				}
				if strings.Contains(rt.Path, "{code}") {
					path = param.ReplaceAllString(rt.Path, "platform.unknown")
				}
				// Use an invalid body so permitted calls fail validation
				// instead of changing data.
				var body any
				if rt.Mutating() {
					body = []byte(`{"__matrix": true}`)
				}
				r := c.Do(rt.Method, path, body)
				denied := r.Status == http.StatusForbidden && strings.Contains(string(r.Body), "missing permission")
				if held[rt.Permission] && denied {
					t.Errorf("%s holds %s but %s %s was denied: %s", role.Code, rt.Permission, rt.Method, rt.Path, r)
				}
				if !held[rt.Permission] && r.Status != http.StatusForbidden {
					t.Errorf("%s lacks %s but %s %s returned %s", role.Code, rt.Permission, rt.Method, rt.Path, r)
				}
			}
		})
	}
}

// EP-03 AC: revoking a role takes effect on the next request (no logout).
func TestRoleRevocationIsImmediate(t *testing.T) {
	sa := superAdmin(t, inst)
	u := sa.Must(201, "POST", "/api/v1/platform/users", map[string]any{"email": "revoke@demo.test", "fullName": "Revoke Me", "password": "Rv#Strong2026x",
		"assignments": []map[string]any{{"roleId": roleID(t, sa, "general_manager"), "propertyId": inst.Main}}}).JSON()
	c := login(t, inst, "revoke@demo.test", "Rv#Strong2026x")
	c.Must(204, "POST", "/api/v1/auth/password/change", map[string]any{"currentPassword": "Rv#Strong2026x", "newPassword": "Rv#Changed2026x"})
	c.Must(200, "GET", "/api/v1/reporting/dashboards/executive-overview", nil)
	as := sa.Must(200, "GET", "/api/v1/platform/role-assignments?filter[userId]="+str(u["id"]), nil).Items()
	sa.Must(204, "DELETE", "/api/v1/platform/role-assignments/"+str(as[0]["id"]), nil)
	c.Must(403, "GET", "/api/v1/reporting/dashboards/executive-overview", nil)
	// Deactivation revokes sessions immediately.
	sa.Must(201, "POST", "/api/v1/platform/role-assignments", map[string]any{"userId": u["id"], "roleId": roleID(t, sa, "general_manager"), "propertyId": inst.Main})
	c.Must(200, "GET", "/api/v1/reporting/dashboards/executive-overview", nil)
	sa.Must(200, "POST", "/api/v1/platform/users/"+str(u["id"])+":deactivate", map[string]any{"reason": "left the club"})
	c.Must(401, "GET", "/api/v1/auth/me", nil)
}

// FR-IAM-07/08: per-property roles, Property Admin limits and no privilege escalation.
func TestPropertyAdminBoundaries(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword) // MAIN only
	// Cannot switch to MDR.
	pa.Property = inst.MDR
	pa.Must(403, "GET", "/api/v1/platform/venues", nil)
	pa.Property = inst.Main
	me := pa.Must(200, "GET", "/api/v1/auth/me", nil).JSON()
	if len(me["properties"].([]any)) != 1 {
		t.Fatalf("property admin must see only MAIN in the switcher: %v", me["properties"])
	}
	// Can create a property user with a property role at MAIN…
	u := pa.Must(201, "POST", "/api/v1/platform/users", map[string]any{"email": "pa.created@demo.test", "fullName": "PA Created",
		"assignments": []map[string]any{{"roleId": roleID(t, pa, "starter_marshal"), "propertyId": inst.Main}}}).JSON()
	// …but not at MDR, not Super Admin, not a role with permissions it lacks.
	pa.Must(403, "POST", "/api/v1/platform/role-assignments", map[string]any{"userId": u["id"], "roleId": roleID(t, pa, "starter_marshal"), "propertyId": inst.MDR})
	sa := superAdmin(t, inst)
	pa.Must(403, "POST", "/api/v1/platform/role-assignments", map[string]any{"userId": u["id"], "roleId": roleID(t, sa, "super_admin")})
	pa.Must(403, "POST", "/api/v1/platform/role-assignments", map[string]any{"userId": u["id"], "roleId": roleID(t, sa, "finance_manager"), "propertyId": inst.Main})
	// Cannot see or change users of other properties or instance-wide admins.
	users := pa.Must(200, "GET", "/api/v1/platform/users?limit=500", nil).Items()
	for _, x := range users {
		if strings.HasPrefix(str(x["email"]), "property.admin2@") || strings.HasPrefix(str(x["email"]), "role.super_admin@") {
			t.Fatalf("property admin can see %v", x["email"])
		}
	}
	var saID string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'role.super_admin@matrix.test'`, nil, &saID)
	pa.Must(404, "PATCH", "/api/v1/platform/users/"+saID, map[string]any{"fullName": "Hacked"})
	// Custom roles cannot include permissions the creator lacks or platform-only ones.
	pa2 := superAdmin(t, inst)
	pa2.Must(422, "POST", "/api/v1/platform/roles", map[string]any{"code": "x_custom", "name": "X", "scope": "property", "permissions": []string{"platform.module.update"}})
	role := pa2.Must(201, "POST", "/api/v1/platform/roles", map[string]any{"code": "night_auditor", "name": "Night Auditor", "category": "Finance",
		"scope": "property", "permissions": []string{"platform.backoffice.access", "audit.log.view"}}).JSON()
	pa2.Must(200, "PATCH", "/api/v1/platform/roles/"+str(role["id"]), map[string]any{"permissions": []string{"platform.backoffice.access"}})
	pa2.Must(409, "PATCH", "/api/v1/platform/roles/"+roleID(t, pa2, "cashier"), map[string]any{"permissions": []string{}})
	pa2.Must(200, "GET", "/api/v1/platform/roles/"+str(role["id"]), nil)
	pa2.Must(204, "DELETE", "/api/v1/platform/roles/"+str(role["id"]), nil)
	pa2.Must(200, "GET", "/api/v1/platform/permissions", nil)
}

// FR-IAM-09: device registration + staff PIN, confined to the device's property.
func TestDevicePINLogin(t *testing.T) {
	c := anon(t, inst)
	c.Must(401, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": "ocd_wrong", "email": "starter@demo.oneclub.id", "pin": app.DemoPIN})
	c.Must(401, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": inst.Device, "email": "starter@demo.oneclub.id", "pin": "999999"})
	c.Must(200, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": inst.Device, "email": "starter@demo.oneclub.id", "pin": app.DemoPIN})
	me := c.Must(200, "GET", "/api/v1/auth/me", nil).JSON()
	if me["kind"] != "device" || me["deviceId"] == nil {
		t.Fatalf("device session expected: %v", me)
	}
	// A back-office-only user cannot use the operational device.
	anon(t, inst).Must(403, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": inst.Device, "email": "member@demo.oneclub.id", "pin": app.DemoPIN})
	// PIN management.
	c.Must(422, "PUT", "/api/v1/auth/me/pin", map[string]any{"pin": "111111"})
	c.Must(204, "PUT", "/api/v1/auth/me/pin", map[string]any{"pin": app.DemoPIN})
	// Device administration.
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	d := pa.Must(201, "POST", "/api/v1/platform/devices", map[string]any{"name": "Starter Tablet", "deviceType": "tablet"}).JSON()
	if !strings.HasPrefix(str(d["deviceToken"]), "ocd_") {
		t.Fatalf("token missing: %v", d)
	}
	rot := pa.Must(200, "POST", "/api/v1/platform/devices/"+str(d["id"])+":rotate-token", nil).JSON()
	anon(t, inst).Must(401, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": d["deviceToken"], "email": "starter@demo.oneclub.id", "pin": app.DemoPIN})
	anon(t, inst).Must(200, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": rot["deviceToken"], "email": "starter@demo.oneclub.id", "pin": app.DemoPIN})
	pa.Must(200, "PATCH", "/api/v1/platform/devices/"+str(d["id"]), map[string]any{"status": "inactive"})
	if len(pa.Must(200, "GET", "/api/v1/platform/devices", nil).Items()) < 2 {
		t.Fatal("devices list")
	}
}

// FR-IAM-10: users see and revoke their sessions; admins revoke others'.
func TestSessions(t *testing.T) {
	a := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	b := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	ss := a.Must(200, "GET", "/api/v1/platform/sessions", nil).Items()
	var other string
	for _, s := range ss {
		if s["current"] != true {
			other = str(s["id"])
		}
	}
	if other == "" {
		t.Fatal("expected another session")
	}
	a.Must(204, "DELETE", "/api/v1/platform/sessions/"+other, nil)
	// The other session may be b's or an older one; at least listing still works for a.
	a.Must(200, "GET", "/api/v1/auth/me", nil)
	// Admin revokes a's current session.
	sa := superAdmin(t, inst)
	var gmID string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'gm@demo.oneclub.id'`, nil, &gmID)
	all := sa.Must(200, "GET", "/api/v1/platform/sessions?userId="+gmID, nil).Items()
	for _, s := range all {
		sa.Must(204, "DELETE", "/api/v1/platform/sessions/"+str(s["id"]), nil)
	}
	a.Must(401, "GET", "/api/v1/auth/me", nil)
	b.Must(401, "GET", "/api/v1/auth/me", nil)
	// Logout.
	c := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	c.Must(204, "POST", "/api/v1/auth/logout", nil)
	c.Must(401, "GET", "/api/v1/auth/me", nil)
	// Preferences.
	d := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	d.Must(204, "PATCH", "/api/v1/auth/me/preferences", map[string]any{"locale": "en", "theme": "dark"})
	if me := d.Must(200, "GET", "/api/v1/auth/me", nil).JSON(); me["locale"] != "en" || me["theme"] != "dark" {
		t.Fatalf("preferences: %v", me)
	}
	d.Must(204, "PATCH", "/api/v1/auth/me/preferences", map[string]any{"locale": "id"})
}

// FR-INT-06: server-to-server API keys with limited scope, rotation, revoke.
func TestAPIKeys(t *testing.T) {
	sa := superAdmin(t, inst)
	k := sa.Must(201, "POST", "/api/v1/platform/api-keys", map[string]any{"name": "Rhapsody bridge", "scopes": []string{"platform.venue.view"}}).JSON()
	key := str(k["key"])
	api := anon(t, inst)
	api.Bearer = key
	api.Must(200, "GET", "/api/v1/platform/venues", nil)
	api.Must(403, "GET", "/api/v1/platform/users", nil)
	rot := sa.Must(200, "POST", "/api/v1/platform/api-keys/"+str(k["id"])+":rotate", nil).JSON()
	api.Must(401, "GET", "/api/v1/platform/venues", nil)
	api.Bearer = str(rot["key"])
	api.Must(200, "GET", "/api/v1/platform/venues", nil)
	sa.Must(200, "POST", "/api/v1/platform/api-keys/"+str(k["id"])+":revoke", nil)
	api.Must(401, "GET", "/api/v1/platform/venues", nil)
	sa.Must(200, "GET", "/api/v1/platform/api-keys", nil)
}

// User management basics (FR-IAM-01) incl. invitation e-mail and MFA reset.
func TestUserManagement(t *testing.T) {
	sa := superAdmin(t, inst)
	u := sa.Must(201, "POST", "/api/v1/platform/users", map[string]any{"email": "Invite.Me@Demo.test", "fullName": "Invite Me", "locale": "en",
		"assignments": []map[string]any{{"roleId": roleID(t, sa, "golf_admin"), "propertyId": inst.Main}}}).JSON()
	if u["email"] != "invite.me@demo.test" {
		t.Fatalf("email must be normalised: %v", u["email"])
	}
	sa.Must(422, "POST", "/api/v1/platform/users", map[string]any{"email": "invite.me@demo.test", "fullName": "Dup",
		"assignments": []map[string]any{{"roleId": roleID(t, sa, "golf_admin"), "propertyId": inst.Main}}})
	sa.Must(200, "PATCH", "/api/v1/platform/users/"+str(u["id"]), map[string]any{"fullName": "Invited Person", "phone": "+628123456789"})
	sa.Must(200, "GET", "/api/v1/platform/users/"+str(u["id"]), nil)
	sa.Must(204, "PUT", "/api/v1/platform/users/"+str(u["id"])+"/pin", map[string]any{"pin": "135790"})
	var inv int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'auth.account_invite' AND recipient = 'invite.me@demo.test'`, nil, &inv)
	if inv != 1 {
		t.Fatalf("invitation e-mail expected, got %d", inv)
	}
	// MFA reset requires a reason.
	login(t, inst, "property.admin2@demo.oneclub.id", demoPassword)
	var pa2 string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'property.admin2@demo.oneclub.id'`, nil, &pa2)
	sa.Must(422, "POST", "/api/v1/platform/users/"+pa2+":reset-mfa", map[string]any{"reason": ""})
	sa.Must(204, "POST", "/api/v1/platform/users/"+pa2+":reset-mfa", map[string]any{"reason": "lost phone"})
	delete(secrets, inst.Code+"/property.admin2@demo.oneclub.id")
	login(t, inst, "property.admin2@demo.oneclub.id", demoPassword) // re-enrols
}

func roleID(t testing.TB, c *Client, code string) string {
	t.Helper()
	var idv string
	sysQueryRow(t, c.in, `SELECT id::text FROM platform.roles WHERE code = $1`, []any{code}, &idv)
	return idv
}
