package approval

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"oneclub/internal/kernel/authz"
)

// PO decision 4d: thresholds edited by admins are validated.
func TestValidateConditionValue(t *testing.T) {
	cases := []struct {
		typ  string
		c    Condition
		want string // "" = valid, else the error code
	}{
		{"number", Condition{"amount", "gt", float64(5000000)}, ""},
		{"number", Condition{"amount", "gte", "25000000"}, ""},
		{"number", Condition{"amount", "gt", float64(0)}, ""},
		{"number", Condition{"amount", "gt", float64(-1)}, "out_of_range"},
		{"number", Condition{"amount", "gt", "five"}, "invalid"},
		{"number", Condition{"amount", "eq", nil}, "required"},
		{"number", Condition{"amount", "lt", ""}, "required"},
		{"string", Condition{"warehouse", "eq", "MAIN"}, ""},
		{"string", Condition{"warehouse", "gt", "MAIN"}, "invalid"},
		{"string", Condition{"warehouse", "in", []any{"A", "B"}}, ""},
		{"string", Condition{"warehouse", "in", []any{}}, "invalid"},
		{"string", Condition{"warehouse", "in", "A"}, "invalid"},
	}
	for _, c := range cases {
		got := validateConditionValue("v", c.typ, c.c)
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("%v: unexpected %v", c.c, got)
		case c.want != "" && (len(got) != 1 || got[0].Code != c.want):
			t.Errorf("%v: got %v, want %s", c.c, got, c.want)
		}
	}
}

// A workflow for all properties needs an instance-wide assignment.
func TestCanManage(t *testing.T) {
	main, other := uuid.New(), uuid.New()
	perm := map[string]struct{}{"platform.approval_workflow.manage": {}}
	propAdmin := authz.WithPrincipal(context.Background(), &authz.Principal{UserID: uuid.New(),
		Assignments: []authz.Assignment{{RoleCode: "property_admin", PropertyID: &main, Permissions: perm}}})
	superAdmin := authz.WithPrincipal(context.Background(), &authz.Principal{UserID: uuid.New(),
		Assignments: []authz.Assignment{{RoleCode: "super_admin", Permissions: perm}}})
	if canManage(propAdmin, &main) != nil || canManage(propAdmin, nil) == nil || canManage(propAdmin, &other) == nil {
		t.Fatal("property admin: own property only")
	}
	if canManage(superAdmin, nil) != nil || canManage(superAdmin, &other) != nil {
		t.Fatal("super admin: every workflow")
	}
}
