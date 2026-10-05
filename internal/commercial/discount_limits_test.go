package commercial

import (
	"encoding/json"
	"testing"

	"oneclub/internal/platform/rules"
)

// PO decision 4b: manual discount limits are role codes with 0–100 %.
func TestValidateDiscountLimits(t *testing.T) {
	if errs := ValidateDiscountLimits("f", map[string]string{"cashier": "0", "outlet_manager": "20", "general_manager": "100", "x1": "12.5"}); len(errs) != 0 {
		t.Fatalf("valid limits: %v", errs)
	}
	got := ValidateDiscountLimits("f", map[string]string{"cashier": "100.01", "pos_staff": "-1", "golf_admin": "ten", "Bad Role": "5"})
	want := map[string]string{"f.cashier": "out_of_range", "f.pos_staff": "out_of_range", "f.golf_admin": "out_of_range", "f.Bad Role": "invalid"}
	if len(got) != len(want) {
		t.Fatalf("errors: %v", got)
	}
	for _, e := range got {
		if want[e.Field] != e.Code {
			t.Fatalf("error %s: %s", e.Field, e.Code)
		}
	}
	// the Pricing Policies definition validates new versions
	var def rules.PolicyDef
	for _, d := range rules.PolicyDefs() {
		if d.Code == PolicyPricing {
			def = d
		}
	}
	if def.Validate == nil || len(def.Validate(json.RawMessage(`{"manualDiscountLimits":{"cashier":"101"}}`))) != 1 ||
		len(def.Validate(json.RawMessage(`{"peakPeriods":[]}`))) != 0 {
		t.Fatal("Pricing Policies validation")
	}
}
