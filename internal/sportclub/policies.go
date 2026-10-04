package sportclub

import "oneclub/internal/platform/rules"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "sportclub.policy", Category: "Sport Club Policies", Name: "Sport Club access, guests & classes",
		Description: "Opening hours per day type, Guest of Member rules, child age limit, class quota on absence, class booking cut-off", Default: defaultPolicy})
}
