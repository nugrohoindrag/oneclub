package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// utcToday matches a business "today" taken as the UTC date: the clock is
// UTC and so is the database session, while the club's date (Asia/Jakarta,
// UTC+7) is already tomorrow from 17:00 to 24:00 UTC. Use the club's date:
// clock.Now().In(calendar.Location(ctx, q)) or a module's localToday helper
// in Go, billing.local_date(property_id) in SQL. Truncate(24 * time.Hour)
// cuts an instant at midnight UTC, not at the club's midnight.
var utcToday = regexp.MustCompile(`(?i:\bcurrent_date\b)|(clock|time)\.Now\(\)(\.UTC\(\))?\.Format\("2006-01-02"\)|Truncate\(24 ?\* ?time\.Hour\)`)

// utcDayBoundary matches a timestamp column cut at a UTC day boundary in
// SQL: its UTC date (created_at::date) or a comparison with a date
// parameter ($2::date, $2::date + 1 — midnight UTC, 07:00 at the club).
// Compare the instant with the club's midnight instead:
// ($2::date::timestamp AT TIME ZONE tz) / (($2::date + 1)::timestamp AT TIME
// ZONE tz), a Go bound from localStart / time.Date(…, loc), or take the
// local date with (col AT TIME ZONE tz)::date.
var utcDayBoundary = regexp.MustCompile(`\b\w+_at\)?::date\b|\b\w+_at\)?\s*(<|<=|>|>=|=)\s*\(?\$\d+::date([^:\w]|$)`)

// allowedUTCToday lists the remaining uses, each with its reason.
var allowedUTCToday = map[string]string{}

// allowedUTCDayBoundary lists the remaining UTC day cut-offs, each with the
// text of the line and its reason.
var allowedUTCDayBoundary = map[string][]string{
	// A coarse SQL window (midnight UTC two days after upTo contains the
	// whole local day upTo in any timezone); the exact club-day cut-off is
	// the localDate check in Go right after the query.
	"accounting/billing_post.go": {"o.paid_at < $3::date + 2", "a.created_at < $3::date + 2"},
}

// productGo calls check for every line of product Go code (non-test files
// under internal/) that is not a comment.
func productGo(t *testing.T, check func(rel string, n int, line string)) {
	t.Helper()
	base := filepath.Join(root(t), "internal")
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			check(rel, i+1, line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBusinessTodayIsTheClubDate(t *testing.T) {
	productGo(t, func(rel string, n int, line string) {
		if !utcToday.MatchString(line) {
			return
		}
		if ok := allowedUTCToday[rel]; ok != "" && strings.Contains(line, ok) {
			return
		}
		t.Errorf("%s:%d: business today taken as the UTC date: %s", rel, n, strings.TrimSpace(line))
	})
}

func TestTimestampsAreCutAtTheClubDay(t *testing.T) {
	productGo(t, func(rel string, n int, line string) {
		if !utcDayBoundary.MatchString(line) {
			return
		}
		for _, ok := range allowedUTCDayBoundary[rel] {
			if strings.Contains(line, ok) {
				return
			}
		}
		t.Errorf("%s:%d: timestamp cut at a UTC day boundary: %s", rel, n, strings.TrimSpace(line))
	})
}

// legacyMigrations are applied migrations (never edited) whose UTC dates
// were superseded by a later migration or only ran once.
var legacyMigrations = map[string]string{
	"reporting/00003_p1_read_models.sql": "golf_rain_checks: recreated by reporting/00025_club_date_views.sql",
	"reporting/00020_p5_hr_core.sql":     "hr_certification_requirements: recreated by reporting/00025_club_date_views.sql",
	"commercial/00003_p2_pricing.sql":    "rate_plans.effective_from default: dropped by commercial/00010_rate_plan_effective_from.sql",
	"hris/00002_core_hr.sql":             "one-off backfill of the employment history at migration time",
}

// TestMigrationsUseTheClubDate applies both rules to the Up part of the SQL
// migrations (a Down part restores the old definition on purpose).
func TestMigrationsUseTheClubDate(t *testing.T) {
	base := filepath.Join(root(t), "db", "migrations")
	files, err := filepath.Glob(filepath.Join(base, "*", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		rel, _ := filepath.Rel(base, path)
		rel = filepath.ToSlash(rel)
		if legacyMigrations[rel] != "" {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(src), "-- +goose Down")
		for i, line := range strings.Split(up, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if utcToday.MatchString(line) || utcDayBoundary.MatchString(line) {
				t.Errorf("%s:%d: UTC date in a migration: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestClubDateGuardsMatch keeps the patterns honest: the fixed forms pass,
// the UTC forms are caught.
func TestClubDateGuardsMatch(t *testing.T) {
	for _, bad := range []string{
		"AND a.created_at < ($2::date + 1)", "c.created_at < $3::date + 1", "add(\"f.created_at::date = ?::date\", v)",
		"coalesce(p.paid_at, p.created_at)::date = ?::date", "occurred_at >= $2::date AND", "max(s.ends_at)::date AS completed",
		"w.decided_at < $3::date + 1 AND", "voided_at >= ($2::date + 1)))",
	} {
		if !utcDayBoundary.MatchString(bad) {
			t.Errorf("not caught: %s", bad)
		}
	}
	for _, bad := range []string{"expires_on < current_date", "DEFAULT CURRENT_DATE", "clock.Now().UTC().Truncate(24 * time.Hour)"} {
		if !utcToday.MatchString(bad) {
			t.Errorf("not caught: %s", bad)
		}
	}
	for _, good := range []string{
		"posted_at >= ($2::date::timestamp AT TIME ZONE $4) AND posted_at < (($3::date + 1)::timestamp AT TIME ZONE $4)",
		"(occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date", "a.created_at < k.end_at", "c.created_at >= $2 AND c.created_at < $3",
		"journal_date <= $2::date", "i.issue_date <= $2::date", "business_date BETWEEN $3::date AND $4::date",
	} {
		if utcDayBoundary.MatchString(good) || utcToday.MatchString(good) {
			t.Errorf("false positive: %s", good)
		}
	}
}
