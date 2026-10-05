package rules

import (
	"slices"
	"testing"
)

// A club policy registered under a category outside the Naming Convention
// list (e.g. "Sales Policies") can still be versioned through the API.
func TestCategoriesIncludeRegisteredPolicies(t *testing.T) {
	RegisterPolicy(PolicyDef{Code: "test.categories", Category: "Test Policies", Name: "Test", Default: map[string]any{}})
	t.Cleanup(func() {
		defsMu.Lock()
		delete(defs, "test.categories")
		defsMu.Unlock()
	})
	cats := Categories()
	if !slices.Contains(cats, "Test Policies") || !slices.Contains(cats, "Golf Policies") {
		t.Fatalf("categories: %v", cats)
	}
	if slices.Index(cats, "Golf Policies") > slices.Index(cats, "Test Policies") {
		t.Fatalf("Naming Convention labels come first: %v", cats)
	}
	if n := len(slices.Compact(slices.Sorted(slices.Values(cats)))); n != len(cats) {
		t.Fatalf("duplicate categories: %v", cats)
	}
}
