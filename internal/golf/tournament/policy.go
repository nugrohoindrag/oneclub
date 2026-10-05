package tournament

// Tournament Policies (PRD P3 FR-POL-P3-05, label proposed in PRD P3 §7.6):
// registration deadline and cancellation, payment due, withdrawal & fee
// refund, waitlist, handicap allowance and maximum handicap, tie-break
// (countback), attestation, prizes, course block and Leaderboard Screen.
// Versioned club policy (P0 framework, Settings → Club Policies → Tournament
// Policies); registrations and tournaments store the version in force.
// Defaults follow PRD P3 §16 #12 and the WHS / R&A recommendations.

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// PolicyCode is the club policy code of the Tournament Policies.
const PolicyCode = "golf.tournament"

// TournamentPolicy is the Tournament Policies document.
type TournamentPolicy struct {
	RegistrationDeadlineDays  int    `json:"registrationDeadlineDays" doc:"Default registration close: N days before the first round"`
	SelfWithdrawalCutoffHours int    `json:"selfWithdrawalCutoffHours" doc:"Players cancel their registration in the Member App / website until N hours before the first start; later only at the golf office"`
	PaymentDueHours           int    `json:"paymentDueHours" doc:"Unpaid online registrations (website, Member App, waitlist promotion) are released after N hours"`
	FullRefundDays            int    `json:"fullRefundDays" doc:"Withdrawal at least N days before the start: full refund of the fee"`
	PartialRefundDays         int    `json:"partialRefundDays" doc:"Withdrawal at least N days before the start: partial refund; later: no refund"`
	PartialRefundPercent      string `json:"partialRefundPercent"`
	HandicapAllowancePercent  string `json:"handicapAllowancePercent" doc:"Playing handicap = course handicap × allowance (WHS: 95% individual stroke play and stableford)"`
	MaxHandicap               string `json:"maxHandicap" doc:"Maximum handicap index (men / open)"`
	MaxHandicapLadies         string `json:"maxHandicapLadies"`
	HandicapLimitMode         string `json:"handicapLimitMode" doc:"cap: players above the maximum play off the maximum | reject: registration is refused above the maximum"`
	HandicapSource            string `json:"handicapSource" doc:"federation_first (PGI index when the player has one) | local_first (OneClub WHS index)"`
	RequireHandicap           bool   `json:"requireHandicap" doc:"Players need a handicap index to register"`
	TieBreak                  string `json:"tieBreak" doc:"countback (last 9, 6, 3, 1 holes) | shared"`
	WaitlistAutoPromote       bool   `json:"waitlistAutoPromote" doc:"A withdrawal promotes the first waitlisted player automatically"`
	WaitlistMax               int    `json:"waitlistMax" doc:"0 = no limit"`
	RequireAttestation        bool   `json:"requireAttestation" doc:"A card is validated only after the marker attested it"`
	OnePrizePerPlayer         bool   `json:"onePrizePerPlayer" doc:"A player wins at most one ranking prize (gross prizes are awarded first)"`
	RoundDurationMinutes      int    `json:"roundDurationMinutes" doc:"Course block after the last start of a round"`
	BlockLeadMinutes          int    `json:"blockLeadMinutes" doc:"Course block before the first start of a round"`
	LeaderboardRotateSeconds  int    `json:"leaderboardRotateSeconds" doc:"Leaderboard Screen rotation between boards"`
	LeaderboardScreenRows     int    `json:"leaderboardScreenRows"`
	DefaultFormat             string `json:"defaultFormat" doc:"Format of a tournament created from an accepted quotation"`
	DefaultStartTime          string `json:"defaultStartTime" doc:"HH:MM start of a tournament created from an accepted quotation"`
}

// DefaultPolicy applies until a club configures Tournament Policies.
var DefaultPolicy = TournamentPolicy{RegistrationDeadlineDays: 1, SelfWithdrawalCutoffHours: 24, PaymentDueHours: 24, FullRefundDays: 7, PartialRefundDays: 3,
	PartialRefundPercent: "50", HandicapAllowancePercent: "95", MaxHandicap: "36", MaxHandicapLadies: "40", HandicapLimitMode: "cap",
	HandicapSource: "federation_first", TieBreak: "countback", WaitlistAutoPromote: true, RequireAttestation: true, OnePrizePerPlayer: true,
	RoundDurationMinutes: 330, BlockLeadMinutes: 30, LeaderboardRotateSeconds: 15, LeaderboardScreenRows: 20, DefaultFormat: "stableford",
	DefaultStartTime: "07:00"}

func init() {
	// PRD P3 §7.6 proposed label (FR-POL-P3-05), added to the club policy
	// categories of Settings → Club Policies.
	if !slices.Contains(rules.PolicyCategories, "Tournament Policies") {
		rules.PolicyCategories = append(rules.PolicyCategories, "Tournament Policies")
	}
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: "Tournament Policies", Name: "Tournament registration, refunds & scoring",
		Description: "Registration deadline and cancellation, payment due, withdrawal refunds, waitlist promotion, handicap allowance & maximum handicap, tie-break, attestation, prizes",
		Default:     DefaultPolicy})
}

// LoadPolicy returns the Tournament Policies in force at the property.
func LoadPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (TournamentPolicy, rules.PolicyRef, error) {
	p, ref, err := rules.PolicyAt(ctx, q, PolicyCode, property, DefaultPolicy)
	if err != nil {
		return p, ref, err
	}
	// Documents saved before a field existed keep the default of the field.
	if p.HandicapLimitMode == "" {
		p.HandicapLimitMode = DefaultPolicy.HandicapLimitMode
	}
	if p.TieBreak == "" {
		p.TieBreak = DefaultPolicy.TieBreak
	}
	if p.HandicapAllowancePercent == "" {
		p.HandicapAllowancePercent = DefaultPolicy.HandicapAllowancePercent
	}
	if p.DefaultFormat == "" {
		p.DefaultFormat = DefaultPolicy.DefaultFormat
	}
	if p.DefaultStartTime == "" {
		p.DefaultStartTime = DefaultPolicy.DefaultStartTime
	}
	if p.LeaderboardRotateSeconds <= 0 {
		p.LeaderboardRotateSeconds = DefaultPolicy.LeaderboardRotateSeconds
	}
	if p.LeaderboardScreenRows <= 0 {
		p.LeaderboardScreenRows = DefaultPolicy.LeaderboardScreenRows
	}
	return p, ref, nil
}
