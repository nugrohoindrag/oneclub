package provision

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/iam/password"
)

// CreateOptions configures `oneclub instance create` (FR-INS-01).
type CreateOptions struct {
	AdminURL           string // superuser URL of the PostgreSQL server
	Code               string // e.g. "mgcc"
	Name               string // e.g. "Modern Golf & Country Club"
	Locale             string
	Currency           string
	Timezone           string
	OrgLegalName       string
	PropertyCode       string
	PropertyName       string
	SuperAdminEmail    string
	SuperAdminName     string
	PlatformAdminEmail string // optional internal OneClub account
	BundleDir          string // where the deploy bundle (.env + secrets) is written; "" = none
	DBHostOverride     string // host name used inside generated URLs (e.g. "db" for Compose)
	PublicBaseURL      string
	Seeds              Seeds
}

// CreateResult is returned once; generated passwords are not stored anywhere
// else in clear text.
type CreateResult struct {
	Database              string
	OwnerURL              string
	AppURL                string
	ReportURL             string
	AppSecret             string
	SuperAdminPassword    string
	PlatformAdminPassword string
	BundleDir             string
}

var codeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

// Names derives database and role names from an instance code.
func Names(code string) (db, owner, app, report string) {
	c := strings.ReplaceAll(code, "-", "_")
	return "oneclub_" + c, "oc_" + c + "_owner", "oc_" + c + "_app", "oc_" + c + "_report"
}

func withDB(raw, dbname, user, pass, hostOverride string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = "/" + dbname
	u.User = url.UserPassword(user, pass)
	if hostOverride != "" {
		port := u.Port()
		if port == "" {
			port = "5432"
		}
		u.Host = hostOverride + ":" + port
	}
	return u.String(), nil
}

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// CreateInstance provisions a dedicated database and roles, runs migrations,
// seeds role templates and the first Super Admin, and writes a deploy bundle.
// Each instance has its own database; the application role may connect only
// to its own database (FR-INS-02, Technical Doc §7.1).
func CreateInstance(ctx context.Context, o CreateOptions) (*CreateResult, error) {
	if !codeRe.MatchString(o.Code) {
		return nil, errors.New("instance code must match ^[a-z][a-z0-9-]{1,30}$")
	}
	if o.Name == "" || o.SuperAdminEmail == "" {
		return nil, errors.New("name and super admin e-mail are required")
	}
	if o.Locale == "" {
		o.Locale = "id"
	}
	if o.Currency == "" {
		o.Currency = "IDR"
	}
	if o.Timezone == "" {
		o.Timezone = "Asia/Jakarta"
	}
	if _, err := time.LoadLocation(o.Timezone); err != nil {
		return nil, fmt.Errorf("invalid timezone %q", o.Timezone)
	}
	if o.PropertyCode == "" {
		o.PropertyCode = "MAIN"
	}
	if o.PropertyName == "" {
		o.PropertyName = o.Name
	}
	if o.OrgLegalName == "" {
		o.OrgLegalName = o.Name
	}
	if o.SuperAdminName == "" {
		o.SuperAdminName = "Super Admin"
	}

	dbName, owner, app, report := Names(o.Code)
	res := &CreateResult{Database: dbName, AppSecret: secret.RandomToken(48)}
	ownerPw, appPw, reportPw := secret.RandomToken(24), secret.RandomToken(24), secret.RandomToken(24)

	admin, err := pgx.Connect(ctx, o.AdminURL)
	if err != nil {
		return nil, fmt.Errorf("connect admin: %w", err)
	}
	defer admin.Close(ctx)

	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("database %s already exists; instance %q is already provisioned", dbName, o.Code)
	}

	stmts := []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD %s`, ident(owner), literal(ownerPw)),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD %s`, ident(app), literal(appPw)),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD %s`, ident(report), literal(reportPw)),
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s ENCODING 'UTF8' TEMPLATE template0`, ident(dbName), ident(owner)),
		fmt.Sprintf(`REVOKE ALL ON DATABASE %s FROM PUBLIC`, ident(dbName)),
		fmt.Sprintf(`GRANT CONNECT, TEMPORARY ON DATABASE %s TO %s, %s, %s`, ident(dbName), ident(owner), ident(app), ident(report)),
		fmt.Sprintf(`ALTER DATABASE %s SET oneclub.app_role = %s`, ident(dbName), literal(app)),
		fmt.Sprintf(`ALTER DATABASE %s SET oneclub.report_role = %s`, ident(dbName), literal(report)),
		fmt.Sprintf(`ALTER DATABASE %s SET timezone = 'UTC'`, ident(dbName)),
		// Reporting role reads through views only; it is never allowed to write.
		fmt.Sprintf(`ALTER ROLE %s SET default_transaction_read_only = on`, ident(report)),
	}
	for _, s := range stmts {
		if _, err := admin.Exec(ctx, s); err != nil {
			return nil, fmt.Errorf("provision: %w", err)
		}
	}

	superURL, err := withDB(o.AdminURL, dbName, "", "", "")
	if err != nil {
		return nil, err
	}
	// keep admin credentials
	au, _ := url.Parse(o.AdminURL)
	su, _ := url.Parse(superURL)
	su.User = au.User
	sconn, err := pgx.Connect(ctx, su.String())
	if err != nil {
		return nil, err
	}
	for _, s := range []string{
		`CREATE EXTENSION IF NOT EXISTS btree_gist`,
		`CREATE EXTENSION IF NOT EXISTS citext`,
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s, %s, %s`, ident(owner), ident(app), ident(report)),
		fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO %s`, ident(owner)),
	} {
		if _, err := sconn.Exec(ctx, s); err != nil {
			sconn.Close(ctx)
			return nil, fmt.Errorf("provision extensions: %w", err)
		}
	}
	sconn.Close(ctx)

	if res.OwnerURL, err = withDB(o.AdminURL, dbName, owner, ownerPw, ""); err != nil {
		return nil, err
	}
	if res.AppURL, err = withDB(o.AdminURL, dbName, app, appPw, ""); err != nil {
		return nil, err
	}
	if res.ReportURL, err = withDB(o.AdminURL, dbName, report, reportPw, ""); err != nil {
		return nil, err
	}

	if err := MigrateUp(ctx, res.OwnerURL); err != nil {
		return nil, err
	}

	pool, err := pgxpool.New(ctx, res.OwnerURL)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := Sync(ctx, tx, o.Seeds); err != nil {
			return err
		}
		return seedInstance(ctx, tx, o, res)
	})
	if err != nil {
		return nil, err
	}

	if o.BundleDir != "" {
		if err := writeBundle(o, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func seedInstance(ctx context.Context, tx pgx.Tx, o CreateOptions, res *CreateResult) error {
	branding := fmt.Sprintf(`{"appName": %q, "emailSenderName": %q, "accent": "lime"}`, o.Name, o.Name)
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.instance (id, code, name, default_locale, currency, timezone, branding)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)`,
		id.New(), o.Code, o.Name, o.Locale, o.Currency, o.Timezone, branding); err != nil {
		return fmt.Errorf("seed instance: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.organization (id, legal_name, display_name) VALUES ($1, $2, $3)`,
		id.New(), o.OrgLegalName, o.Name); err != nil {
		return fmt.Errorf("seed organization: %w", err)
	}
	propID := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.properties (id, code, name) VALUES ($1, $2, $3)`,
		propID, strings.ToUpper(o.PropertyCode), o.PropertyName); err != nil {
		return fmt.Errorf("seed property: %w", err)
	}

	// Default payment methods (Naming Convention §18). Availability per
	// property is configured by the club.
	for i, pm := range [][3]string{
		{"CASH", "Cash", "cash"}, {"BANK_TRANSFER", "Bank Transfer", "bank_transfer"},
		{"VA", "Virtual Account", "virtual_account"}, {"QRIS", "QRIS", "qris"}, {"CARD", "Card", "card"},
		{"PG", "Payment Gateway", "payment_gateway"}, {"MEMBER_ACCOUNT", "Member Account", "member_account"},
		{"VOUCHER_PREPAID", "Voucher & Prepaid", "voucher_prepaid"},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO billing.payment_methods (id, code, name, method_type, sort_order) VALUES ($1, $2, $3, $4, $5)`,
			id.New(), pm[0], pm[1], pm[2], (i+1)*10); err != nil {
			return fmt.Errorf("seed payment method: %w", err)
		}
	}

	// Sandbox integrations so P1 development can start before vendors are
	// chosen (FR-INT-05). Credentials are not needed for mock adapters.
	box, err := secret.NewBox(res.AppSecret)
	if err != nil {
		return err
	}
	whSecret, err := box.Seal([]byte(secret.RandomToken(32)))
	if err != nil {
		return err
	}
	for _, it := range []struct {
		code, adapter, capability, name string
		enabled                         bool
		webhook                         []byte
	}{
		{"mock-payment", "mock-payment", "payment", "Mock Payment Gateway (Sandbox)", true, whSecret},
		{"mock-whatsapp", "mock-whatsapp", "messaging", "Mock WhatsApp (Sandbox)", true, nil},
		{"mock-email", "mock-email", "email", "Mock E-mail (Sandbox)", false, nil},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.integrations (id, code, adapter, capability, name, enabled, mode, webhook_secret_enc)
			VALUES ($1, $2, $3, $4, $5, $6, 'sandbox', $7)`, id.New(), it.code, it.adapter, it.capability, it.name, it.enabled, it.webhook); err != nil {
			return fmt.Errorf("seed integration: %w", err)
		}
	}

	pw := password.Generate()
	res.SuperAdminPassword = pw
	if err := seedUser(ctx, tx, o.SuperAdminEmail, o.SuperAdminName, pw, "super_admin"); err != nil {
		return err
	}
	if o.PlatformAdminEmail != "" {
		ppw := password.Generate()
		res.PlatformAdminPassword = ppw
		if err := seedUser(ctx, tx, o.PlatformAdminEmail, "OneClub Platform Admin", ppw, "platform_admin"); err != nil {
			return err
		}
	}
	// Provisioning is itself audited.
	_, err = tx.Exec(ctx, `
		INSERT INTO audit.audit_log (id, actor_type, actor_name, module, action, category, entity_type, entity_id, entity_label, after)
		VALUES ($1, 'system', 'oneclub instance create', 'platform', 'instance_provisioned', 'system', 'platform.instance', $2, $3,
		        jsonb_build_object('code', $2::text, 'name', $3::text, 'locale', $4::text, 'currency', $5::text, 'timezone', $6::text))`,
		id.New(), o.Code, o.Name, o.Locale, o.Currency, o.Timezone)
	return err
}

func seedUser(ctx context.Context, tx pgx.Tx, email, name, plain, roleCode string) error {
	h, err := password.Hash(plain)
	if err != nil {
		return err
	}
	uid := id.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.users (id, email, full_name, password_hash, password_changed_at, must_change_password)
		VALUES ($1, $2, $3, $4, now(), true)`, uid, strings.ToLower(email), name, h); err != nil {
		return fmt.Errorf("seed user %s: %w", email, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.role_assignments (id, user_id, role_id)
		SELECT $1, $2, id FROM platform.roles WHERE code = $3`, id.New(), uid, roleCode); err != nil {
		return fmt.Errorf("seed role assignment: %w", err)
	}
	return nil
}

// writeBundle writes the per-instance deploy bundle used by Docker Compose:
// a non-secret .env and one file per secret (FR-TEC-12). The bundle must be
// stored outside the repository (e.g. encrypted with sops).
func writeBundle(o CreateOptions, r *CreateResult) error {
	dir := filepath.Join(o.BundleDir, o.Code)
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		return err
	}
	r.BundleDir = dir
	host := o.DBHostOverride
	rewrite := func(u string) string {
		if host == "" {
			return u
		}
		pu, _ := url.Parse(u)
		port := pu.Port()
		if port == "" {
			port = "5432"
		}
		pu.Host = host + ":" + port
		return pu.String()
	}
	base := o.PublicBaseURL
	if base == "" {
		base = "https://backoffice." + o.Code + ".oneclub.id"
	}
	env := strings.Join([]string{
		"# OneClub customer instance " + o.Code + " — generated by `oneclub instance create`",
		"INSTANCE=" + o.Code,
		"ONECLUB_INSTANCE=" + o.Code,
		"ONECLUB_ENV=production",
		"PUBLIC_BASE_URL=" + base,
		"DATABASE_URL_FILE=/run/secrets/database_url",
		"DATABASE_REPLICA_URL_FILE=/run/secrets/database_report_url",
		"DATABASE_OWNER_URL_FILE=/run/secrets/database_owner_url",
		"APP_SECRET_FILE=/run/secrets/app_secret",
		"COOKIE_SECURE=true",
		"STORAGE_DRIVER=s3",
		"",
	}, "\n")
	files := map[string]string{
		".env":                        env,
		"secrets/database_url":        rewrite(r.AppURL),
		"secrets/database_owner_url":  rewrite(r.OwnerURL),
		"secrets/database_report_url": rewrite(r.ReportURL),
		"secrets/app_secret":          r.AppSecret,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// DropInstance removes an instance database and its roles (tests, dev only).
func DropInstance(ctx context.Context, adminURL, code string) error {
	dbName, owner, app, report := Names(code)
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	for _, s := range []string{
		fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, ident(dbName)),
		fmt.Sprintf(`DROP ROLE IF EXISTS %s`, ident(app)),
		fmt.Sprintf(`DROP ROLE IF EXISTS %s`, ident(report)),
		fmt.Sprintf(`DROP ROLE IF EXISTS %s`, ident(owner)),
	} {
		if _, err := conn.Exec(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
