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
// in Go, billing.local_date(property_id) in SQL.
var utcToday = regexp.MustCompile(`\bcurrent_date\b|(clock|time)\.Now\(\)(\.UTC\(\))?\.Format\("2006-01-02"\)`)

// allowedUTCToday lists the remaining uses, each with its reason.
var allowedUTCToday = map[string]string{
	// age in days of a requisition: both dates are UTC dates.
	"procurement/requisition.go": "greatest(0, current_date - r.created_at::date) AS age_days",
}

func TestBusinessTodayIsTheClubDate(t *testing.T) {
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
			if strings.HasPrefix(strings.TrimSpace(line), "//") || !utcToday.MatchString(line) {
				continue
			}
			if ok := allowedUTCToday[rel]; ok != "" && strings.Contains(line, ok) {
				continue
			}
			t.Errorf("%s:%d: business today taken as the UTC date: %s", rel, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
