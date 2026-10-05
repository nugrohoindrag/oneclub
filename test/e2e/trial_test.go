package e2e

// Trial dataset (`oneclub seed-demo --trial`, internal/app/trial.go): on a
// fresh instance with the demo configuration the trial seeder simulates the
// club's history (60 days here, 90 in the CLI) and books the next 30 days;
// every module has data, the books are consistent and a second run adds
// nothing.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"oneclub/internal/app"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/provision"
)

// startTrialInstance provisions a fresh instance with the demo
// configuration. No worker runs: the trial seeder dispatches the outbox
// itself at the simulated time (as `oneclub seed-demo --trial` does).
func startTrialInstance(t *testing.T) (*app.App, *dbtx.DB) {
	t.Helper()
	ctx := context.Background()
	code := fmt.Sprintf("e2t-%d", time.Now().UnixNano()%1e8)
	seeds, err := app.Seeds()
	if err != nil {
		t.Fatal(err)
	}
	res, err := provision.CreateInstance(ctx, provision.CreateOptions{AdminURL: adminURL, Code: code, Name: "Trial Club " + code,
		SuperAdminEmail: "superadmin@" + code + ".test", Seeds: seeds})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(false)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Env, cfg.InstanceCode, cfg.DatabaseURL, cfg.DatabaseReplicaURL, cfg.AppSecret = "test", code, res.AppURL, res.ReportURL, res.AppSecret
	cfg.AuditStrict = true
	cfg.Storage.Dir = os.TempDir() + "/oneclub-e2e"
	db, err := dbtx.Open(ctx, app.TrialDatabaseURL(cfg.DatabaseURL), cfg.ReplicaURL(), 20)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.Build(cfg, db, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Hub.Stop()
		db.Close()
		if os.Getenv("ONECLUB_KEEP_TEST_DB") == "" {
			_ = provision.DropInstance(context.Background(), adminURL, code)
		}
	})
	if _, err := app.SeedDemo(ctx, db); err != nil {
		t.Fatal(err)
	}
	return a, db
}

func TestTrialDataset(t *testing.T) {
	a, db := startTrialInstance(t)
	ctx := context.Background()
	started := time.Now()
	// 60 days cover every monthly cycle (statements, bank reconciliation,
	// commission run, Top Spender snapshot, a closed month) whatever the
	// date, and keep the suite bounded; ONECLUB_TRIAL_DAYS=90 runs the
	// history of `oneclub seed-demo --trial`.
	days := 60
	if v, err := strconv.Atoi(os.Getenv("ONECLUB_TRIAL_DAYS")); err == nil && v > 0 {
		days = v
	}
	res, err := a.SeedTrial(ctx, app.TrialOptions{Days: days, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("trial dataset: %d steps in %s (history %s – %s)", res.Steps, time.Since(started).Round(time.Second), res.Start, res.Today)
	if res.AlreadyComplete || res.Steps == 0 {
		t.Fatalf("first run must create the dataset: %+v", res)
	}
	// every module and entity of the coverage summary has data
	var empty []string
	counts := map[string]int{}
	for _, c := range res.Coverage {
		counts[c.Module+"/"+c.Entity] = c.Rows
		t.Logf("  %-12s %-26s %7d", c.Module, c.Entity, c.Rows)
		if c.Rows == 0 {
			empty = append(empty, c.Module+"/"+c.Entity)
		}
	}
	if len(empty) > 0 {
		t.Errorf("entities without trial data: %s", strings.Join(empty, ", "))
	}

	// consistency: balanced books, stock valuation = GL inventory, no
	// posting exceptions, no failed events, every past day closed
	h, err := app.TrialHealthOf(ctx, db, res.Property)
	if err != nil {
		t.Fatal(err)
	}
	if !h.TrialBalanced {
		t.Errorf("trial balance not balanced: debit %s credit %s", h.TotalDebit, h.TotalCredit)
	}
	if !h.InventoryMatches {
		t.Errorf("stock valuation differs from the GL inventory: %+v", h.InventoryChecks)
	}
	if len(h.PostingExceptions) > 0 {
		t.Errorf("open posting exceptions: %v", h.PostingExceptions)
	}
	if h.OutboxFailed > 0 || h.OutboxPending > 0 {
		t.Errorf("outbox: %d failed, %d pending events", h.OutboxFailed, h.OutboxPending)
	}
	if h.ClosedDays < days {
		t.Errorf("closed business days: %d", h.ClosedDays)
	}
	if h.ClosedPeriods == 0 || h.BankRecsCompleted == 0 {
		t.Errorf("month-end close: %d closed periods, %d completed bank reconciliations", h.ClosedPeriods, h.BankRecsCompleted)
	}

	// idempotent: a second run adds nothing
	again, err := a.SeedTrial(ctx, app.TrialOptions{Days: days, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyComplete || again.Start != res.Start {
		t.Fatalf("second run must find the dataset complete: %+v", again)
	}
	for _, c := range again.Coverage {
		if n := counts[c.Module+"/"+c.Entity]; n != c.Rows {
			t.Errorf("second run changed %s/%s: %d → %d", c.Module, c.Entity, n, c.Rows)
		}
	}
}
