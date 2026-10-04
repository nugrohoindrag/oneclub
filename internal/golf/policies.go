package golf

import "oneclub/internal/platform/rules"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "golf.caddy", Category: "Caddy Policies", Name: "Caddy rotation, fee split & settlement",
		Description: "Rotation (arrival, round robin, per level), caddy fee split on replacement, settlement period and deductions", Default: defaultCaddyPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: "golf.cart", Category: "Golf Cart Policies", Name: "Golf cart inspection, service & damage",
		Description: "Charging after use, release inspection, minimum battery, damage charge approval", Default: defaultCartPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: "golf.reciprocal", Category: "Reciprocal Policies", Name: "Reciprocal verification",
		Description: "Introduction letter and home club card requirements, letter validity, visit quota", Default: defaultReciprocalPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: "golf.hall_of_fame", Category: "Hall of Fame Policies", Name: "Hall of Fame curation",
		Description: "Auto-publish of automatic entries, kiosk rotation, course record minimum holes", Default: defaultHOFPolicy})
}
