package app_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "oneclub/internal/app" // registers every module's ESS sections
	"oneclub/internal/hris"
	"oneclub/internal/platform/navigation"
)

// TestServerIconsAreInTheSelfHostedFont: the apps render icons with a subset
// of Material Symbols Rounded (decision 4g) so they work offline; a name
// missing from it shows as text. Every icon the API returns must be in
// web/packages/shell/src/assets/material-symbols-rounded.icons.json — run
// `python3 scripts/fonts/subset-icons.py` after adding one.
func TestServerIconsAreInTheSelfHostedFont(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "web", "packages", "shell", "src", "assets", "material-symbols-rounded.icons.json"))
	if err != nil {
		t.Fatal(err)
	}
	var subset struct {
		Icons []string `json:"icons"`
	}
	if err := json.Unmarshal(raw, &subset); err != nil {
		t.Fatal(err)
	}
	included := make(map[string]bool, len(subset.Icons))
	for _, n := range subset.Icons {
		included[n] = true
	}

	checked := 0
	check := func(where, icon string) {
		if icon == "" {
			return
		}
		checked++
		if !included[icon] {
			t.Errorf("%s: icon %q is not in the self-hosted font; run python3 scripts/fonts/subset-icons.py", where, icon)
		}
	}
	var walk func(shell string, items []navigation.Item)
	walk = func(shell string, items []navigation.Item) {
		for _, it := range items {
			check("navigation "+shell+"/"+it.Key, it.Icon)
			walk(shell, it.Children)
		}
	}
	for shell, items := range navigation.Trees {
		walk(shell, items)
	}
	for _, s := range hris.ESSSections() {
		check("ESS section "+s.Key, s.Icon)
	}
	if checked < 20 {
		t.Fatalf("only %d icons checked; navigation or ESS registry not loaded", checked)
	}
}
