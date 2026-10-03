package provision_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
)

func adminURL(t *testing.T) string {
	u := os.Getenv("ONECLUB_TEST_ADMIN_URL")
	if u == "" {
		t.Skip("ONECLUB_TEST_ADMIN_URL not set")
	}
	return u
}

func testSeeds(t *testing.T) provision.Seeds {
	cat, err := catalog.Build(catalog.Contribution{Permissions: catalog.P("reporting", "dashboard", "view")},
		catalog.Contribution{Permissions: append(catalog.P("reporting", "report", "view"), catalog.P("reporting", "export", "create")...)})
	if err != nil {
		t.Fatal(err)
	}
	return provision.Seeds{Catalog: cat}
}

func TestCreateInstanceMigratesAndSeeds(t *testing.T) {
	ctx := context.Background()
	admin := adminURL(t)
	_ = provision.DropInstance(ctx, admin, "prov-test")
	t.Cleanup(func() { _ = provision.DropInstance(context.Background(), admin, "prov-test") })

	res, err := provision.CreateInstance(ctx, provision.CreateOptions{
		AdminURL: admin, Code: "prov-test", Name: "Provision Test Club",
		SuperAdminEmail: "admin@prov.test", Seeds: testSeeds(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Migrations are idempotent.
	if err := provision.MigrateUp(ctx, res.OwnerURL); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	conn, err := pgx.Connect(ctx, res.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var roles, users int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM platform.roles`).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM platform.users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if roles < 40 || users != 1 {
		t.Fatalf("roles=%d users=%d", roles, users)
	}
	// FR-AUD-03: the application role cannot UPDATE or DELETE audit rows.
	if _, err := conn.Exec(ctx, `UPDATE audit.audit_log SET reason = 'x'`); err == nil {
		t.Fatal("app role could update audit log")
	}
	if _, err := conn.Exec(ctx, `DELETE FROM audit.audit_log`); err == nil {
		t.Fatal("app role could delete audit log")
	}
}
