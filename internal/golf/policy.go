package golf

// EP-15 Golf Policies & Configuration. Policies are versioned Club Policies
// (platform.rules, effective-dated, per property override) read through
// rules.Resolve; until a club saves its own version the defaults below
// apply. Every booking stores the policy versions in force when it was made
// (FR-POL-09), and fee calculations use those versions.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// Policy codes (Settings → Club Policies, Naming Convention §25).
const (
	PolicyGolf         = "golf.booking"      // Golf Policies
	PolicyGuest        = "golf.guest"        // Guest Policies
	PolicyCancellation = "golf.cancellation" // Cancellation Policies (incl. No-show Policy)
	PolicyWeather      = "golf.weather"      // Weather Policies / Rain Check
	PolicyCaddy        = "golf.caddy"        // Caddy Policies
	PolicyCart         = "golf.cart"         // Golf Cart Policies
	PolicyPayment      = "golf.payment"      // Golf Policies → Payment Policy
	PolicyEligibility  = "golf.eligibility"  // Member Policies → player eligibility
	PolicyOverride     = "pricing.override"  // Pricing Policies → price override limit
)

// PolicyCategory maps a code to its Club Policies category.
var PolicyCategory = map[string]string{
	PolicyGolf: "Golf Policies", PolicyGuest: "Guest Policies", PolicyCancellation: "Cancellation Policies", PolicyWeather: "Weather Policies",
	PolicyCaddy: "Caddy Policies", PolicyCart: "Golf Cart Policies", PolicyPayment: "Golf Policies", PolicyEligibility: "Member Policies",
	PolicyOverride: "Pricing Policies",
}

// GolfPolicy (FR-POL-01).
type GolfPolicy struct {
	TeeTimeIntervalMinutes int            `json:"teeTimeIntervalMinutes"`
	MaxPlayers             int            `json:"maxPlayers"`
	MinPlayers             int            `json:"minPlayers"`
	NightMinPlayers        int            `json:"nightMinPlayers"`
	HoldMinutes            int            `json:"holdMinutes"`
	BookingWindowDays      map[string]int `json:"bookingWindowDays"` // by player type: member, guest_of_member, non_member, reciprocal
	MemberPriorityDays     int            `json:"memberPriorityDays"`
	BookingCutoffMinutes   map[string]int `json:"bookingCutoffMinutes"` // by channel
	RescheduleCutoffHours  int            `json:"rescheduleCutoffHours"`
	MaxReschedules         int            `json:"maxReschedules"`
	AllowTBA               bool           `json:"allowTBA"`
	NoShowGraceMinutes     int            `json:"noShowGraceMinutes"`
	RoundMinutes18         int            `json:"roundMinutes18"`
	RoundMinutes9          int            `json:"roundMinutes9"`
	ReminderHour           int            `json:"reminderHour"`
	DressCode              string         `json:"dressCode"`
	ClubRules              string         `json:"clubRules"`
}

// GuestPolicy (FR-POL-02).
type GuestPolicy struct {
	MaxGuestsPerMember int    `json:"maxGuestsPerMember"`
	AllowedWeekdays    []int  `json:"allowedWeekdays"` // ISO 1..7; empty = every day
	AllowedFrom        string `json:"allowedFrom"`     // HH:MM
	AllowedTo          string `json:"allowedTo"`
	MemberMustPlay     bool   `json:"memberMustPlay"`
}

// CancellationPolicy (FR-POL-03) including the No-show Policy.
type CancellationPolicy struct {
	FreeCancelHours        int    `json:"freeCancelHours"`
	LateCancelFeePercent   string `json:"lateCancelFeePercent"`
	NoShowFeePercent       string `json:"noShowFeePercent"`
	NoShowBlockAfter       int    `json:"noShowBlockAfter"`
	NoShowBlockWindowDays  int    `json:"noShowBlockWindowDays"`
	WaiverRequiresApproval bool   `json:"waiverRequiresApproval"`
}

// WeatherRule is one rain check band.
type WeatherRule struct {
	MaxHolesPlayed int    `json:"maxHolesPlayed"`
	CreditPercent  string `json:"creditPercent"`
}

// WeatherPolicy (FR-POL-05).
type WeatherPolicy struct {
	Rules        []WeatherRule `json:"rules"`
	ValidityDays int           `json:"validityDays"`
}

// CaddyPolicy (FR-POL-06).
type CaddyPolicy struct {
	PlayersPerCaddy int  `json:"playersPerCaddy"`
	Mandatory       bool `json:"mandatory"`
	AllowRequest    bool `json:"allowRequest"`
}

// CartPolicy (FR-POL-07).
type CartPolicy struct {
	PlayersPerCart     int    `json:"playersPerCart"`
	Mandatory          bool   `json:"mandatory"`
	SingleRiderAllowed bool   `json:"singleRiderAllowed"`
	AfterReturn        string `json:"afterReturn"` // not_ready | charging
}

// PaymentRule decides the Payment Policy of a booking (FR-PAY-02).
type PaymentRule struct {
	Channel        string `json:"channel,omitempty"`
	BookingType    string `json:"bookingType,omitempty"`
	Mode           string `json:"mode"` // prepaid | deposit | pay_at_venue | member_charge
	DepositPercent string `json:"depositPercent,omitempty"`
	DueMinutes     int    `json:"dueMinutes,omitempty"`
}

// PaymentPolicy (FR-PAY-02).
type PaymentPolicy struct {
	Rules            []PaymentRule `json:"rules"`
	Default          string        `json:"default"`
	PayBeforeCheckIn bool          `json:"payBeforeCheckIn"`
}

// EligibilityPolicy (FR-FLT-04) defines the special segments.
type EligibilityPolicy struct {
	SeniorMinAge int    `json:"seniorMinAge"`
	JuniorMaxAge int    `json:"juniorMaxAge"`
	LadiesGender string `json:"ladiesGender"`
	ResidentOnly bool   `json:"residentOnly"` // special segments only for residents
}

// OverridePolicy limits manual prices (FR-PRC-09).
type OverridePolicy struct {
	MaxDiscountPercent string `json:"maxDiscountPercent"` // above → approval
}

// Defaults are the values in force until a club saves a version.
var (
	DefaultGolf = GolfPolicy{TeeTimeIntervalMinutes: 8, MaxPlayers: 4, MinPlayers: 1, NightMinPlayers: 3, HoldMinutes: 10,
		BookingWindowDays:     map[string]int{"member": 14, "guest_of_member": 14, "reciprocal": 7, "non_member": 7},
		MemberPriorityDays:    7,
		BookingCutoffMinutes:  map[string]int{"member_app": 60, "website": 120, "back_office": 0, "walk_in": 0, "import": 0},
		RescheduleCutoffHours: 24, MaxReschedules: 2, AllowTBA: true, NoShowGraceMinutes: 30, RoundMinutes18: 300, RoundMinutes9: 150, ReminderHour: 8,
		DressCode: "Collared shirt, golf trousers or shorts, soft-spike golf shoes.", ClubRules: "Please arrive 30 minutes before your tee time."}
	DefaultGuest        = GuestPolicy{MaxGuestsPerMember: 3, MemberMustPlay: true}
	DefaultCancellation = CancellationPolicy{FreeCancelHours: 24, LateCancelFeePercent: "50", NoShowFeePercent: "100", NoShowBlockAfter: 3, NoShowBlockWindowDays: 90,
		WaiverRequiresApproval: true}
	DefaultWeather = WeatherPolicy{Rules: []WeatherRule{{MaxHolesPlayed: 0, CreditPercent: "100"}, {MaxHolesPlayed: 9, CreditPercent: "50"},
		{MaxHolesPlayed: 36, CreditPercent: "0"}}, ValidityDays: 90}
	DefaultCaddy   = CaddyPolicy{PlayersPerCaddy: 1, Mandatory: true, AllowRequest: true}
	DefaultCart    = CartPolicy{PlayersPerCart: 2, Mandatory: true, SingleRiderAllowed: true, AfterReturn: "charging"}
	DefaultPayment = PaymentPolicy{Default: "pay_at_venue", PayBeforeCheckIn: true, Rules: []PaymentRule{
		{Channel: "website", Mode: "prepaid", DueMinutes: 15},
		{Channel: "member_app", Mode: "member_charge"},
		{BookingType: "group", Mode: "deposit", DepositPercent: "30", DueMinutes: 4320},
		{BookingType: "corporate", Mode: "deposit", DepositPercent: "30", DueMinutes: 4320},
	}}
	DefaultEligibility = EligibilityPolicy{SeniorMinAge: 60, JuniorMaxAge: 18, LadiesGender: "female"}
	DefaultOverride    = OverridePolicy{MaxDiscountPercent: "20"}
)

// Policies are the versions in force for one booking.
type Policies struct {
	Golf         GolfPolicy
	Guest        GuestPolicy
	Cancellation CancellationPolicy
	Weather      WeatherPolicy
	Caddy        CaddyPolicy
	Cart         CartPolicy
	Payment      PaymentPolicy
	Eligibility  EligibilityPolicy
	Override     OverridePolicy
	Versions     map[string]int // code → version (0 = default)
}

func resolve(ctx context.Context, q dbtx.Querier, code string, property uuid.UUID, at time.Time, dst any) (int, error) {
	raw, v, ok, err := rules.Resolve(ctx, q, "club_policy", code, &property, at)
	if err != nil || !ok {
		return 0, err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return 0, nil //nolint:nilerr // a malformed stored value falls back to the default
	}
	return v, nil
}

// LoadPolicies resolves every golf policy in force at time at.
func LoadPolicies(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (Policies, error) {
	p := Policies{Golf: DefaultGolf, Guest: DefaultGuest, Cancellation: DefaultCancellation, Weather: DefaultWeather, Caddy: DefaultCaddy,
		Cart: DefaultCart, Payment: DefaultPayment, Eligibility: DefaultEligibility, Override: DefaultOverride, Versions: map[string]int{}}
	// copy maps so a stored version never mutates the defaults
	p.Golf.BookingWindowDays = map[string]int{}
	for k, v := range DefaultGolf.BookingWindowDays {
		p.Golf.BookingWindowDays[k] = v
	}
	p.Golf.BookingCutoffMinutes = map[string]int{}
	for k, v := range DefaultGolf.BookingCutoffMinutes {
		p.Golf.BookingCutoffMinutes[k] = v
	}
	for code, dst := range map[string]any{PolicyGolf: &p.Golf, PolicyGuest: &p.Guest, PolicyCancellation: &p.Cancellation, PolicyWeather: &p.Weather,
		PolicyCaddy: &p.Caddy, PolicyCart: &p.Cart, PolicyPayment: &p.Payment, PolicyEligibility: &p.Eligibility, PolicyOverride: &p.Override} {
		v, err := resolve(ctx, q, code, property, at, dst)
		if err != nil {
			return p, err
		}
		p.Versions[code] = v
	}
	if p.Golf.MaxPlayers <= 0 {
		p.Golf.MaxPlayers = 4
	}
	if p.Golf.HoldMinutes <= 0 {
		p.Golf.HoldMinutes = 10
	}
	if p.Cart.PlayersPerCart <= 0 {
		p.Cart.PlayersPerCart = 2
	}
	if p.Caddy.PlayersPerCaddy <= 0 {
		p.Caddy.PlayersPerCaddy = 1
	}
	return p, nil
}

// LoadPoliciesAsOf resolves the versions stored on a booking: the booking's
// creation time decides (FR-POL-09 AC: a later version does not change the
// fee of an existing booking).
func LoadPoliciesAsOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, createdAt time.Time) (Policies, error) {
	return LoadPolicies(ctx, q, property, createdAt)
}

// PolicyView is one policy with its value in force (Golf Settings).
type PolicyView struct {
	Code     string          `json:"code"`
	Category string          `json:"category"`
	Version  int             `json:"version" doc:"0 = built-in default"`
	Value    json.RawMessage `json:"value"`
}

// CurrentPolicies lists every golf policy with the value in force now.
func CurrentPolicies(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]PolicyView, error) {
	p, err := LoadPolicies(ctx, q, property, clock.Now())
	if err != nil {
		return nil, err
	}
	vals := map[string]any{PolicyGolf: p.Golf, PolicyGuest: p.Guest, PolicyCancellation: p.Cancellation, PolicyWeather: p.Weather, PolicyCaddy: p.Caddy,
		PolicyCart: p.Cart, PolicyPayment: p.Payment, PolicyEligibility: p.Eligibility, PolicyOverride: p.Override}
	var out []PolicyView
	for _, code := range []string{PolicyGolf, PolicyPayment, PolicyGuest, PolicyCancellation, PolicyWeather, PolicyCaddy, PolicyCart, PolicyEligibility, PolicyOverride} {
		raw, _ := json.Marshal(vals[code])
		out = append(out, PolicyView{Code: code, Category: PolicyCategory[code], Version: p.Versions[code], Value: raw})
	}
	return out, nil
}

// PaymentFor picks the payment rule for a booking.
func (p Policies) PaymentFor(channel, bookingType string) PaymentRule {
	for _, r := range p.Payment.Rules {
		if (r.Channel == "" || r.Channel == channel) && (r.BookingType == "" || r.BookingType == bookingType) {
			return r
		}
	}
	mode := p.Payment.Default
	if mode == "" {
		mode = "pay_at_venue"
	}
	return PaymentRule{Mode: mode}
}
