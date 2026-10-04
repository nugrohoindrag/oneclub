package reservation

import "oneclub/internal/platform/rules"

func init() {
	for _, t := range []struct{ code, name string }{{"sport_court", "Sport Court"}, {"class_session", "Class Session"}, {"facility_entry", "Facility Entry"},
		{"bungalow", "Bungalow"}, {"vip_suite", "VIP Suite"}, {"meeting_room", "Meeting Room"}, {"driving_range_bay", "Driving Range Bay"}, {"tee_time", "Tee Time"}} {
		rules.RegisterPolicy(rules.PolicyDef{Code: "reservation.deposit." + t.code, Category: "Cancellation Policies", Name: t.name + " deposit",
			Description: "Deposit percentage or fixed amount, due time and whether it is required to confirm", Default: DepositPolicy{Percent: "0", Fixed: "0", DueHours: 24}})
		rules.RegisterPolicy(rules.PolicyDef{Code: "reservation.cancellation." + t.code, Category: "Cancellation Policies", Name: t.name + " cancellation",
			Description: "Free cancellation window, cancellation fee and no-show fee", Default: CancellationPolicy{FreeCancelHours: 24, FeePercent: "50", NoShowFeePercent: "100"}})
	}
}
