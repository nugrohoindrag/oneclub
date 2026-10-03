// Command oneclub is the single OneClub backend binary (Technical Doc §3.2):
//
//	oneclub api                      REST API (+ webhooks)
//	oneclub worker                   River background jobs
//	oneclub migrate up|status|down   database migrations (+ catalogue sync)
//	oneclub instance create|drop     provision a dedicated customer instance
//	oneclub seed-demo                demo data for dev/staging
//	oneclub openapi [-o file]        write the OpenAPI document
//	oneclub healthcheck              container health check
//	oneclub version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"oneclub/internal/app"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/obs"
	"oneclub/internal/platform/provision"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "api":
		err = runAPI()
	case "worker":
		err = runWorker()
	case "migrate":
		err = runMigrate(args)
	case "instance":
		err = runInstance(args)
	case "seed-demo":
		err = runSeedDemo()
	case "openapi":
		err = runOpenAPI(args)
	case "healthcheck":
		err = runHealthcheck()
	case "version":
		fmt.Println(app.Version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: oneclub <api|worker|migrate|instance|seed-demo|openapi|healthcheck|version> [args]`)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func load(process string) (*config.Config, *dbtx.DB, error) {
	cfg, err := config.Load(true)
	if err != nil {
		return nil, nil, err
	}
	cfg.Version = app.Version
	obs.SetupLogger(cfg.LogLevel, cfg.InstanceCode, process)
	db, err := dbtx.Open(context.Background(), cfg.DatabaseURL, cfg.ReplicaURL(), 20)
	if err != nil {
		return nil, nil, err
	}
	return cfg, db, nil
}

func runAPI() error {
	cfg, db, err := load("api")
	if err != nil {
		return err
	}
	defer db.Close()
	a, err := app.Build(cfg, db, app.Options{})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: a.Server.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second}
	ctx, stop := signalContext()
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", cfg.HTTPAddr, "version", app.Version, "routes", len(a.Registry.Routes()))
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	// Graceful shutdown: Caddy stops routing to this replica once /readyz
	// fails or the container stops; in-flight requests drain (Technical Doc §10.3).
	slog.Info("api shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

func runWorker() error {
	cfg, db, err := load("worker")
	if err != nil {
		return err
	}
	defer db.Close()
	a, err := app.Build(cfg, db, app.Options{Worker: true, Concurrency: cfg.WorkerConcurrency})
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	if err := a.Jobs.River.Start(context.Background()); err != nil {
		return err
	}
	slog.Info("worker started", "version", app.Version)
	<-ctx.Done()
	slog.Info("worker stopping; finishing active jobs")
	sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return a.Jobs.River.Stop(sctx)
}

func ownerURL() (string, error) {
	cfg, err := config.Load(false)
	if err != nil {
		return "", err
	}
	_ = cfg
	if v := os.Getenv("DATABASE_OWNER_URL_FILE"); v != "" {
		b, err := os.ReadFile(v) //nolint:gosec // G304: operator-mounted secret file
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := os.Getenv("DATABASE_OWNER_URL"); v != "" {
		return v, nil
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v, nil
	}
	return "", errors.New("DATABASE_OWNER_URL (or DATABASE_URL) is required")
}

// syncCatalog runs the catalogue sync as the owner.
func syncCatalog(ctx context.Context, url string) error {
	seeds, err := app.Seeds()
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return provision.Sync(ctx, tx, seeds) })
}

func runMigrate(args []string) error {
	obs.SetupLogger(os.Getenv("LOG_LEVEL"), os.Getenv("ONECLUB_INSTANCE"), "migrate")
	url, err := ownerURL()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sub := "up"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "up":
		if err := provision.MigrateUp(ctx, url); err != nil {
			return err
		}
		if err := syncCatalog(ctx, url); err != nil {
			return fmt.Errorf("catalogue sync: %w", err)
		}
		slog.Info("migrations applied and catalogue synchronised")
		return nil
	case "status":
		st, err := provision.Status(ctx, url)
		if err != nil {
			return err
		}
		for _, s := range st {
			fmt.Printf("%-12s version=%-6d pending=%d\n", s.Module, s.Version, s.Pending)
		}
		return nil
	case "down":
		if len(args) < 2 {
			return errors.New("usage: oneclub migrate down <module>")
		}
		if os.Getenv("ONECLUB_ENV") == "production" {
			return errors.New("migrate down is disabled in production; roll forward with a new migration")
		}
		return provision.MigrateDown(ctx, url, args[1])
	}
	return fmt.Errorf("unknown migrate command %q", sub)
}

func runInstance(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: oneclub instance create|drop [flags]")
	}
	obs.SetupLogger("info", "provision", "instance")
	ctx := context.Background()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("instance create", flag.ExitOnError)
		o := provision.CreateOptions{}
		fs.StringVar(&o.AdminURL, "admin-url", os.Getenv("ONECLUB_ADMIN_DATABASE_URL"), "superuser URL of the PostgreSQL server")
		fs.StringVar(&o.Code, "code", "", "instance code, e.g. mgcc")
		fs.StringVar(&o.Name, "name", "", "club name")
		fs.StringVar(&o.Locale, "locale", "id", "default locale (id|en)")
		fs.StringVar(&o.Currency, "currency", "IDR", "currency (ISO 4217)")
		fs.StringVar(&o.Timezone, "timezone", "Asia/Jakarta", "IANA timezone")
		fs.StringVar(&o.OrgLegalName, "legal-name", "", "organization legal name")
		fs.StringVar(&o.PropertyCode, "property-code", "MAIN", "first property code")
		fs.StringVar(&o.PropertyName, "property-name", "", "first property name")
		fs.StringVar(&o.SuperAdminEmail, "super-admin-email", "", "first Super Admin e-mail")
		fs.StringVar(&o.SuperAdminName, "super-admin-name", "Super Admin", "first Super Admin name")
		fs.StringVar(&o.PlatformAdminEmail, "platform-admin-email", "", "optional OneClub Platform Admin e-mail")
		fs.StringVar(&o.BundleDir, "bundle-dir", "", "write the deploy bundle (.env + secrets) here")
		fs.StringVar(&o.DBHostOverride, "db-host", "", "database host name used inside the bundle (e.g. db)")
		fs.StringVar(&o.PublicBaseURL, "public-url", "", "Back Office URL used in e-mails")
		_ = fs.Parse(args[1:])
		seeds, err := app.Seeds()
		if err != nil {
			return err
		}
		o.Seeds = seeds
		res, err := provision.CreateInstance(ctx, o)
		if err != nil {
			return err
		}
		fmt.Printf("\nInstance %q provisioned in database %s\n", o.Code, res.Database)
		fmt.Printf("  Super Admin:    %s  temporary password: %s  (change at first login, MFA setup required)\n", o.SuperAdminEmail, res.SuperAdminPassword)
		if res.PlatformAdminPassword != "" {
			fmt.Printf("  Platform Admin: %s  temporary password: %s\n", o.PlatformAdminEmail, res.PlatformAdminPassword)
		}
		if res.BundleDir != "" {
			fmt.Printf("  Deploy bundle:  %s (store securely; contains secrets)\n", res.BundleDir)
		} else {
			fmt.Printf("  DATABASE_URL=%s\n  DATABASE_OWNER_URL=%s\n  DATABASE_REPLICA_URL=%s\n  APP_SECRET=%s\n", res.AppURL, res.OwnerURL, res.ReportURL, res.AppSecret)
		}
		return nil
	case "drop":
		fs := flag.NewFlagSet("instance drop", flag.ExitOnError)
		admin := fs.String("admin-url", os.Getenv("ONECLUB_ADMIN_DATABASE_URL"), "superuser URL")
		code := fs.String("code", "", "instance code")
		confirm := fs.String("confirm", "", "repeat the instance code to confirm")
		_ = fs.Parse(args[1:])
		if os.Getenv("ONECLUB_ENV") == "production" {
			return errors.New("instance drop is disabled when ONECLUB_ENV=production")
		}
		if *code == "" || *confirm != *code {
			return errors.New("pass -code and -confirm with the same instance code")
		}
		return provision.DropInstance(ctx, *admin, *code)
	}
	return fmt.Errorf("unknown instance command %q", args[0])
}

func runSeedDemo() error {
	cfg, db, err := load("seed-demo")
	if err != nil {
		return err
	}
	defer db.Close()
	if cfg.Env == "production" {
		return errors.New("seed-demo is disabled in production")
	}
	res, err := app.SeedDemo(context.Background(), db)
	if err != nil {
		return err
	}
	fmt.Println("Demo data ready. Password for every demo user:", app.DemoPassword, " staff PIN:", app.DemoPIN)
	for _, u := range res.Users {
		p := u.Property
		if p == "" {
			p = "all properties"
		}
		fmt.Printf("  %-34s %-18s %s\n", u.Email, u.Role, p)
	}
	if res.DeviceToken != "" {
		fmt.Println("  Demo POS device token (shown once):", res.DeviceToken)
	}
	return nil
}

func runOpenAPI(args []string) error {
	fs := flag.NewFlagSet("openapi", flag.ExitOnError)
	out := fs.String("o", "", "output file (default stdout)")
	_ = fs.Parse(args)
	cfg, err := config.Load(false)
	if err != nil {
		return err
	}
	cfg.Version = app.Version
	a, err := app.Build(cfg, nil, app.Options{})
	if err != nil {
		return err
	}
	spec := append(a.Server.OpenAPI, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(spec)
		return err
	}
	return os.WriteFile(*out, spec, 0o600)
}

func runHealthcheck() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" || strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
		if addr == "127.0.0.1" {
			addr = "127.0.0.1:8080"
		}
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/readyz") //nolint:gosec // G704: local readiness probe, address from HTTP_ADDR
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %s", resp.Status)
	}
	return nil
}
