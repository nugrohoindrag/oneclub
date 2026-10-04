package stay

import "oneclub/internal/platform/rules"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "stay.policy", Category: "Stay Policies", Name: "Check-in / check-out & deposit",
		Description: "Check-in and check-out times, deposit, late check-out fee and grace, ready unit requirement", Default: defaultPolicy})
}
