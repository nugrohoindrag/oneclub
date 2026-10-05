package banquet

// Club Policies of the module (PRD P3 EP-22 FR-POL-P3-01 Banquet Policies,
// FR-POL-P3-05 Event Policies; defaults from PRD P3 §16 #11). Events store
// the versions they were decided under (policy_refs).

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// Policy codes.
const (
	PolicyBooking      = "banquet.booking"
	PolicyCancellation = "banquet.cancellation"
	PolicyCharges      = "banquet.charges"
	PolicyRegistration = "event.registration"
)

// BookingPolicy covers venue holds, payment terms and pax.
type BookingPolicy struct {
	TentativeHoldDays       int    `json:"tentativeHoldDays" doc:"Option date of a tentative hold = hold + N days"`
	WaitlistEnabled         bool   `json:"waitlistEnabled" doc:"A second hold on a taken date may be waitlisted"`
	MinDownPaymentPercent   string `json:"minDownPaymentPercent"`
	DownPaymentDueDays      int    `json:"downPaymentDueDays" doc:"DP due N days after the schedule is built (never after the option date)"`
	SecondTermPercent       string `json:"secondTermPercent" doc:"0 = no second term"`
	SecondTermDaysBefore    int    `json:"secondTermDaysBefore"`
	FinalPaymentDaysBefore  int    `json:"finalPaymentDaysBefore"`
	GuaranteedPaxDaysBefore int    `json:"guaranteedPaxDaysBefore" doc:"Final pax cut-off (H-N); later increases are charged"`
	MaxPaxDecreasePercent   string `json:"maxPaxDecreasePercent" doc:"Guaranteed pax may drop at most this much"`
	DefiniteRequiresDeposit bool   `json:"definiteRequiresDeposit" doc:"Manual Definite before the DP needs approval"`
	DefaultStartTime        string `json:"defaultStartTime" doc:"Start time of events created from a quotation (HH:MM)"`
	RequirementLeadDays     int    `json:"requirementLeadDays" doc:"Ingredients needed N days before the event (K1)"`
	PackageCancellation     string `json:"packageCancellation" enum:"cancel,tentative" doc:"Event of a cancelled package: cancelled with it, or back to Tentative until its option date (FR-PKG-08)"`
}

// DefaultBooking is the code default (PRD P3 §16 #11).
var DefaultBooking = BookingPolicy{TentativeHoldDays: 7, WaitlistEnabled: true, MinDownPaymentPercent: "30", DownPaymentDueDays: 7,
	SecondTermPercent: "0", SecondTermDaysBefore: 60, FinalPaymentDaysBefore: 7, GuaranteedPaxDaysBefore: 7, MaxPaxDecreasePercent: "10",
	DefiniteRequiresDeposit: true, DefaultStartTime: "10:00", RequirementLeadDays: 1, PackageCancellation: "cancel"}

// CancellationTier forfeits a share of a basis when the event is cancelled
// at least MinDaysBefore days before it starts.
type BanquetCancellationTier struct {
	MinDaysBefore  int    `json:"minDaysBefore"`
	ForfeitPercent string `json:"forfeitPercent"`
	Basis          string `json:"basis" enum:"down_payment,paid,contract" doc:"down_payment: the DP received; paid: everything received; contract: the contract total"`
}

// CancellationPolicy is the banquet cancellation & DP forfeiture policy.
type CancellationPolicy struct {
	Tiers []BanquetCancellationTier `json:"tiers"`
}

// DefaultCancellation: > 90 days 50% of the DP is kept, 30–90 days the DP
// is forfeited, < 30 days every payment is forfeited (PRD P3 §16 #11).
var DefaultCancellation = CancellationPolicy{Tiers: []BanquetCancellationTier{{MinDaysBefore: 91, ForfeitPercent: "50", Basis: "down_payment"},
	{MinDaysBefore: 30, ForfeitPercent: "100", Basis: "down_payment"}, {MinDaysBefore: 0, ForfeitPercent: "100", Basis: "paid"}}}

// tier returns the cancellation tier for daysBefore.
func (p CancellationPolicy) tier(daysBefore int) (BanquetCancellationTier, bool) {
	ts := append([]BanquetCancellationTier{}, p.Tiers...)
	sort.Slice(ts, func(i, j int) bool { return ts[i].MinDaysBefore > ts[j].MinDaysBefore })
	for _, t := range ts {
		if daysBefore >= t.MinDaysBefore {
			return t, true
		}
	}
	if len(ts) > 0 {
		return ts[len(ts)-1], true
	}
	return BanquetCancellationTier{}, false
}

// ChargesPolicy prices the extras without a configured charge type.
type ChargesPolicy struct {
	CorkageFeePerBottle     string   `json:"corkageFeePerBottle"`
	OutsideFoodPartnerOnly  bool     `json:"outsideFoodPartnerOnly" doc:"Outside food only from partner vendors"`
	OvertimeFeePerHour      string   `json:"overtimeFeePerHour"`
	ElectricityIncludedWatt int      `json:"electricityIncludedWatt" doc:"Included per ballroom when the venue has no own quota"`
	ElectricityRatePerKw    string   `json:"electricityRatePerKw"`
	PricingMode             string   `json:"pricingMode" enum:"nett,plus_plus"`
	TaxCodes                []string `json:"taxCodes" doc:"Tax & service codes of the extras (empty = all in force)"`
}

// DefaultCharges (PRD P3 §16 #11: corkage Rp150.000 per bottle, 10.000 W).
var DefaultCharges = ChargesPolicy{CorkageFeePerBottle: "150000", OutsideFoodPartnerOnly: true, OvertimeFeePerHour: "2500000",
	ElectricityIncludedWatt: 10000, ElectricityRatePerKw: "250000", PricingMode: "nett", TaxCodes: []string{}}

// RegistrationPolicy is the Event Policies document (registration rules).
type RegistrationPolicy struct {
	WaitlistEnabled               bool   `json:"waitlistEnabled"`
	AutoPromoteWaitlist           bool   `json:"autoPromoteWaitlist"`
	MaxPartySize                  int    `json:"maxPartySize"`
	WithdrawDeadlineHours         int    `json:"withdrawDeadlineHours" doc:"Guests may withdraw until N hours before the start"`
	WithdrawRefundPercent         string `json:"withdrawRefundPercent" doc:"Share of a paid fee refunded on withdrawal"`
	RegistrationClosesHoursBefore int    `json:"registrationClosesHoursBefore"`
	CheckInOpensMinutesBefore     int    `json:"checkInOpensMinutesBefore"`
}

// DefaultRegistration is the code default.
var DefaultRegistration = RegistrationPolicy{WaitlistEnabled: true, AutoPromoteWaitlist: true, MaxPartySize: 6, WithdrawDeadlineHours: 24,
	WithdrawRefundPercent: "100", RegistrationClosesHoursBefore: 0, CheckInOpensMinutesBefore: 240}

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyBooking, Category: "Banquet Policies", Name: "Holds, down payment & pax",
		Description: "Tentative hold (option date) and waitlist, DP minimum and payment terms, final pax cut-off and allowed decrease", Default: DefaultBooking})
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCancellation, Category: "Banquet Policies", Name: "Cancellation & DP forfeiture",
		Description: "Share of the DP / payments kept per days before the event", Default: DefaultCancellation})
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCharges, Category: "Banquet Policies", Name: "Corkage, overtime & electricity",
		Description: "Corkage fee, outside food rule, overtime fee and electricity quota and rate", Default: DefaultCharges})
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyRegistration, Category: "Event Policies", Name: "Event registration",
		Description: "Waitlist, party size, withdrawal deadline and refund, registration close and check-in window", Default: DefaultRegistration})
}

func bookingPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (BookingPolicy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, PolicyBooking, property, DefaultBooking)
}

func cancellationPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CancellationPolicy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, PolicyCancellation, property, DefaultCancellation)
}

func chargesPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ChargesPolicy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, PolicyCharges, property, DefaultCharges)
}

func registrationPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (RegistrationPolicy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, PolicyRegistration, property, DefaultRegistration)
}
