package membership

import "oneclub/internal/platform/rules"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "membership.lifecycle", Category: "Member Policies", Name: "Membership lifecycle",
		Description: "Pause eligibility and limits, grace period auto-suspension, expiry, credit limit, child age notice", Default: defaultLifecycle})
}
