package tournament

// FR-TRN-03 Registration (Back Office, Member App, website) with the
// handicap snapshot (P2 Handicap Index or official input), Tournament Fee
// and Tournament Package posted to a billing folio and paid by member
// charge, online payment link or at the desk; FR-TRN-04 Participants:
// Registered, Waitlisted, Withdrawn, Checked-in, automatic waitlist
// promotion and refunds per Tournament Policies; complimentary entries
// through the Tournament Fee Waiver approval.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/membership"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// TournamentRegistration is a participant.
type TournamentRegistration struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	TournamentID      uuid.UUID  `json:"tournamentId" db:"tournament_id"`
	Number            string     `json:"number" db:"number"`
	CustomerID        uuid.UUID  `json:"customerId" db:"customer_id"`
	MemberID          *uuid.UUID `json:"memberId" db:"member_id"`
	PlayerName        string     `json:"playerName" db:"player_name"`
	Gender            *string    `json:"gender" db:"gender" enum:"male,female"`
	Email             *string    `json:"email" db:"email"`
	Phone             *string    `json:"phone" db:"phone"`
	PlayerType        string     `json:"playerType" db:"player_type" enum:"member,guest"`
	Channel           string     `json:"channel" db:"channel" enum:"back_office,member_app,website,quotation,import"`
	DivisionID        *uuid.UUID `json:"divisionId" db:"division_id"`
	DivisionName      *string    `json:"divisionName" db:"division_name"`
	PackageID         *uuid.UUID `json:"packageId" db:"package_id"`
	PackageName       *string    `json:"packageName" db:"package_name"`
	HandicapIndex     *string    `json:"handicapIndex" db:"handicap_index" doc:"Snapshot at registration"`
	HandicapSource    string     `json:"handicapSource" db:"handicap_source" enum:"whs,federation,manual,official,declared,none"`
	ShirtSize         *string    `json:"shirtSize" db:"shirt_size"`
	Preferences       *string    `json:"preferences" db:"preferences"`
	PairingGroup      *string    `json:"pairingGroup" db:"pairing_group" doc:"Players of a group are drawn into the same flight (sponsor guests)"`
	SponsorID         *uuid.UUID `json:"sponsorId" db:"sponsor_id"`
	Status            string     `json:"status" db:"status" enum:"registered,waitlisted,withdrawn,checked_in"`
	WaitlistPosition  *int       `json:"waitlistPosition" db:"waitlist_position"`
	PublicConsent     bool       `json:"publicConsent" db:"public_consent" doc:"Name shown on the public leaderboard and the Hall of Fame"`
	FeeTotal          string     `json:"feeTotal" db:"fee_total"`
	Currency          string     `json:"currency" db:"currency"`
	FolioID           *uuid.UUID `json:"folioId" db:"folio_id"`
	PaymentStatus     string     `json:"paymentStatus" db:"payment_status" enum:"not_required,pending,paid,waived,refunded,partially_refunded,cancelled"`
	PaymentDueAt      *time.Time `json:"paymentDueAt" db:"payment_due_at"`
	FeeWaiverStatus   *string    `json:"feeWaiverStatus" db:"fee_waiver_status" enum:"pending,approved,rejected,cancelled"`
	FeeWaiverReason   *string    `json:"feeWaiverReason" db:"fee_waiver_reason"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	RefundAmount      string     `json:"refundAmount" db:"refund_amount"`
	MadeCut           *bool      `json:"madeCut" db:"made_cut"`
	RegisteredAt      time.Time  `json:"registeredAt" db:"registered_at"`
	PromotedAt        *time.Time `json:"promotedAt" db:"promoted_at"`
	WithdrawnAt       *time.Time `json:"withdrawnAt" db:"withdrawn_at"`
	WithdrawReason    *string    `json:"withdrawReason" db:"withdraw_reason"`
	CheckedInAt       *time.Time `json:"checkedInAt" db:"checked_in_at"`
	PropertyID        uuid.UUID  `json:"-" db:"property_id"`
	ManageTokenHash   *string    `json:"-" db:"manage_token_hash"`
}

const registrationSelect = `SELECT r.id, r.property_id, r.tournament_id, r.number, r.customer_id, r.member_id, r.player_name, r.gender, r.email, r.phone,
	r.player_type, r.channel, r.division_id, d.name AS division_name, r.package_id, p.name AS package_name, trim_scale(r.handicap_index)::text AS handicap_index,
	r.handicap_source, r.shirt_size, r.preferences, r.pairing_group, r.sponsor_id, r.status, r.waitlist_position, r.public_consent,
	trim_scale(r.fee_total)::text AS fee_total, r.currency, r.folio_id, r.payment_status, r.payment_due_at, r.fee_waiver_status, r.fee_waiver_reason,
	r.approval_request_id, trim_scale(r.refund_amount)::text AS refund_amount, r.made_cut, r.registered_at, r.promoted_at, r.withdrawn_at, r.withdraw_reason,
	r.checked_in_at, r.manage_token_hash
	FROM golf.tournament_registrations r LEFT JOIN golf.tournament_divisions d ON d.id = r.division_id LEFT JOIN golf.tournament_packages p ON p.id = r.package_id`

func getRegistration(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (TournamentRegistration, error) {
	rows, err := q.Query(ctx, registrationSelect+` WHERE r.id = $1`, rid)
	return handle.One[TournamentRegistration](rows, err, "registration")
}

func lockRegistration(ctx context.Context, tx pgx.Tx, property, tid, rid uuid.UUID) (TournamentRegistration, error) {
	var p, t uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id, tournament_id FROM golf.tournament_registrations WHERE id = $1 FOR UPDATE`, rid).Scan(&p, &t); err != nil ||
		p != property || (tid != uuid.Nil && t != tid) {
		if err == nil || dbtx.IsNoRows(err) {
			return TournamentRegistration{}, errs.NotFound("registration")
		}
		return TournamentRegistration{}, err
	}
	return getRegistration(ctx, tx, rid)
}

// StartInfo is a player's start of the current round (start sheet).
type TournamentStartInfo struct {
	RoundNo    int       `json:"roundNo"`
	FlightID   uuid.UUID `json:"flightId"`
	FlightNo   int       `json:"flightNo"`
	StartLabel string    `json:"startLabel" doc:"Start hole and group (shotgun: 7A) or tee (1 / 10)"`
	StartAt    time.Time `json:"startAt"`
	LocalTime  string    `json:"localTime"`
	Published  bool      `json:"published" doc:"The draw is published (visible to players)"`
}

// RegistrationDetail is a participant with folio, payment and start.
type TournamentRegistrationDetail struct {
	TournamentRegistration
	TournamentCode string               `json:"tournamentCode"`
	TournamentName string               `json:"tournamentName"`
	StartDate      string               `json:"startDate"`
	Folio          *billing.Summary     `json:"folio"`
	Payment        *billing.Payment     `json:"payment" doc:"Pending online checkout (payment link)"`
	Start          *TournamentStartInfo `json:"start"`
}

func (m *Module) registrationDetail(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (TournamentRegistrationDetail, error) {
	r, err := getRegistration(ctx, q, rid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	d := TournamentRegistrationDetail{TournamentRegistration: r}
	if err := q.QueryRow(ctx, `SELECT code, name, start_date::text FROM golf.tournaments WHERE id = $1`, r.TournamentID).
		Scan(&d.TournamentCode, &d.TournamentName, &d.StartDate); err != nil {
		return d, err
	}
	if r.FolioID != nil {
		s, err := billing.FolioSummary(ctx, q, *r.FolioID)
		if err != nil {
			return d, err
		}
		d.Folio = &s
		if d.Payment, err = billing.PendingOnline(ctx, q, *r.FolioID); err != nil {
			return d, err
		}
	}
	d.Start, err = startOf(ctx, q, r.TournamentID, r.ID, r.PropertyID)
	return d, err
}

// startOf returns the start of the latest drawn round of a player.
func startOf(ctx context.Context, q dbtx.Querier, tid, rid, property uuid.UUID) (*TournamentStartInfo, error) {
	var s TournamentStartInfo
	var hole int
	var group *string
	var status string
	err := q.QueryRow(ctx, `SELECT x.round_no, f.id, f.flight_no, f.start_hole, f.start_group, f.start_at, x.status
		FROM golf.tournament_flight_players fp JOIN golf.tournament_flights f ON f.id = fp.flight_id JOIN golf.tournament_rounds x ON x.id = fp.round_id
		WHERE fp.registration_id = $1 AND x.tournament_id = $2 ORDER BY x.round_no DESC LIMIT 1`, rid, tid).
		Scan(&s.RoundNo, &s.FlightID, &s.FlightNo, &hole, &group, &s.StartAt, &status)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.StartLabel = startLabel(hole, group)
	s.LocalTime = s.StartAt.In(location(ctx, q, property)).Format("15:04")
	s.Published = status != "drawn" && status != "scheduled"
	return &s, nil
}

func startLabel(hole int, group *string) string {
	return itoa(hole) + deref(group)
}

// ── registering ───────────────────────────────────────────────────────────

// GuestInput identifies a player without a customer profile.
type TournamentGuestInput struct {
	Name   string `json:"name"`
	Phone  string `json:"phone,omitempty"`
	Email  string `json:"email,omitempty"`
	Gender string `json:"gender,omitempty" enum:"male,female"`
}

// RegistrationInput registers a player at the Back Office / Tournament Desk.
type TournamentRegistrationInput struct {
	CustomerID    *uuid.UUID            `json:"customerId,omitempty"`
	MemberNo      string                `json:"memberNo,omitempty" doc:"Member number or card number instead of customerId"`
	Guest         *TournamentGuestInput `json:"guest,omitempty" doc:"Player without a customer profile (deduplicated by phone / e-mail)"`
	PackageID     *uuid.UUID            `json:"packageId,omitempty" doc:"Default: the default package of the player type"`
	DivisionID    *uuid.UUID            `json:"divisionId,omitempty" doc:"Default: automatic by gender, player type, handicap and age"`
	HandicapIndex string                `json:"handicapIndex,omitempty" doc:"Official handicap index (e.g. PGI) recorded for this tournament"`
	ShirtSize     string                `json:"shirtSize,omitempty"`
	Preferences   string                `json:"preferences,omitempty"`
	PairingGroup  string                `json:"pairingGroup,omitempty" doc:"Keep players of the group in one flight (sponsor guests)"`
	SponsorID     *uuid.UUID            `json:"sponsorId,omitempty" doc:"Guest of a sponsor"`
	PublicConsent bool                  `json:"publicConsent,omitempty"`
	Payment       string                `json:"payment,omitempty" enum:"pay_later,member_charge,online" doc:"Default pay_later (paid at the desk)"`
	PaymentMethod string                `json:"paymentMethod,omitempty" enum:"qris,virtual_account,card,payment_gateway"`
}

type registerParams struct {
	channel       string
	customer      crm.Customer
	packageID     *uuid.UUID
	divisionID    *uuid.UUID
	handicap      string
	handicapSrc   string // official (staff) | declared (member / website)
	shirtSize     string
	preferences   string
	pairingGroup  string
	sponsorID     *uuid.UUID
	publicConsent bool
	gender        string
	payment       string // pay_later | member_charge | online
	method        string
	staff         bool
	manageToken   string
}

// resolveCustomer finds the player of a staff registration.
func resolveCustomer(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentRegistrationInput) (crm.Customer, error) {
	switch {
	case in.CustomerID != nil:
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return c, err
		}
		if c.PropertyID != property {
			return c, handle.Invalid("customerId", "not_found", "customer of this property")
		}
		return c, nil
	case strings.TrimSpace(in.MemberNo) != "":
		mid, err := membership.CardLookup(ctx, tx, property, in.MemberNo)
		if err != nil {
			return crm.Customer{}, err
		}
		if mid == nil {
			return crm.Customer{}, handle.Invalid("memberNo", "not_found", "member not found")
		}
		var cid *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT customer_id FROM membership.members WHERE id = $1`, *mid).Scan(&cid); err != nil {
			return crm.Customer{}, err
		}
		if cid == nil {
			return crm.Customer{}, handle.Invalid("memberNo", "invalid", "the member has no customer profile")
		}
		return crm.GetCustomer(ctx, tx, *cid)
	case in.Guest != nil:
		c, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: in.Guest.Name, Phone: in.Guest.Phone, Email: in.Guest.Email})
		return c, err
	}
	return crm.Customer{}, handle.Invalid("customerId", "required", "choose a customer, a member number or enter the guest")
}

// Register registers a player from the Back Office / Tournament Desk.
func (m *Module) Register(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentRegistrationInput) (TournamentRegistrationDetail, error) {
	c, err := resolveCustomer(ctx, tx, property, in)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	pay := in.Payment
	if pay == "" {
		pay = "pay_later"
	}
	if err := oneOf("payment", pay, "pay_later", "member_charge", "online"); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	gender := ""
	if in.Guest != nil {
		gender = in.Guest.Gender
	}
	return m.register(ctx, tx, property, tid, registerParams{channel: "back_office", customer: c, packageID: in.PackageID, divisionID: in.DivisionID,
		handicap: in.HandicapIndex, handicapSrc: "official", shirtSize: in.ShirtSize, preferences: in.Preferences, pairingGroup: in.PairingGroup,
		sponsorID: in.SponsorID, publicConsent: in.PublicConsent, gender: gender, payment: pay, method: in.PaymentMethod, staff: true})
}

// register is the registration use case of every channel.
func (m *Module) register(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, p registerParams) (TournamentRegistrationDetail, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	pol, ref, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	switch {
	case t.Status == "open":
		if !p.staff && ((t.RegistrationOpensAt != nil && t.RegistrationOpensAt.After(now())) || (t.RegistrationClosesAt != nil && !t.RegistrationClosesAt.After(now()))) {
			return TournamentRegistrationDetail{}, errs.Conflict("registration_closed", "registration for "+t.Name+" is not open now")
		}
	case p.staff && t.Status == "closed":
		// late entries by the Tournament Desk
	default:
		return TournamentRegistrationDetail{}, errs.Conflict("registration_closed", "registration for "+t.Name+" is not open")
	}
	c := p.customer
	if c.Status != "" && c.Status != "active" {
		return TournamentRegistrationDetail{}, errs.Conflict("customer_inactive", "the customer profile is "+c.Status)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_registrations WHERE tournament_id = $1 AND customer_id = $2 AND status <> 'withdrawn')`,
		tid, c.ID).Scan(&exists); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if exists {
		return TournamentRegistrationDetail{}, errs.Conflict("already_registered", c.Name+" is already registered for this tournament")
	}
	// player type: an Active membership on the first day
	startDay, _ := time.Parse("2006-01-02", t.StartDate)
	playerType := "guest"
	var memberID *uuid.UUID
	if mid, err := membership.MemberByCustomer(ctx, tx, c.ID); err != nil {
		return TournamentRegistrationDetail{}, err
	} else if mid != nil {
		info, err := membership.Standing(ctx, tx, *mid, startDay)
		if err != nil {
			return TournamentRegistrationDetail{}, err
		}
		memberID = mid
		if info.Active() {
			playerType = "member"
		}
	}
	// eligibility (FR-TRN-01)
	switch {
	case t.Eligibility == "members" && playerType != "member":
		return TournamentRegistrationDetail{}, errs.Conflict("not_eligible", t.Name+" is for members with an active membership")
	case t.Eligibility == "invitation" && !p.staff:
		return TournamentRegistrationDetail{}, errs.Conflict("not_eligible", t.Name+" is by invitation; please contact the golf office")
	case p.channel == "website" && (!t.Public || (t.Eligibility != "open" && t.Eligibility != "members_and_guests")):
		return TournamentRegistrationDetail{}, errs.Conflict("not_eligible", t.Name+" does not take website registrations")
	}
	// handicap snapshot (P2 handicap or the official / declared index)
	var hcp *string
	var src string
	if v := strings.TrimSpace(p.handicap); v != "" && p.handicapSrc == "official" {
		if hcp, err = decimalOrNil("handicapIndex", v, -10, 54); err != nil {
			return TournamentRegistrationDetail{}, err
		}
		src = "official"
	} else {
		hcp, src = experience.TournamentHandicap(ctx, tx, c.ID, pol.HandicapSource != "local_first")
		if hcp == nil && v != "" {
			if hcp, err = decimalOrNil("handicapIndex", v, -10, 54); err != nil {
				return TournamentRegistrationDetail{}, err
			}
			src = "declared"
		}
	}
	if hcp == nil && pol.RequireHandicap {
		return TournamentRegistrationDetail{}, handle.Invalid("handicapIndex", "required", "a handicap index is required for "+t.Name)
	}
	gender := c.Gender
	if gender == "" {
		gender = p.gender
	}
	if gender != "male" && gender != "female" {
		gender = ""
	}
	// maximum handicap (Tournament Policies): above it the player is refused
	// (reject) or plays off the maximum (cap, applied to the playing handicap)
	if hcp != nil && pol.HandicapLimitMode == "reject" {
		maxHcp := dec(pol.MaxHandicap)
		if gender == "female" {
			maxHcp = dec(pol.MaxHandicapLadies)
		}
		if t.MaxHandicap != nil {
			maxHcp = dec(*t.MaxHandicap)
		}
		if maxHcp.IsPositive() && dec(*hcp).GreaterThan(maxHcp) {
			return TournamentRegistrationDetail{}, errs.Conflict("handicap_above_maximum", fmt.Sprintf("the handicap index %s is above the maximum %s of %s", *hcp,
				maxHcp.String(), t.Name))
		}
	}
	divs, err := listDivisions(ctx, tx, tid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	division := p.divisionID
	if division != nil {
		ok := false
		for _, d := range divs {
			ok = ok || d.ID == *division
		}
		if !ok {
			return TournamentRegistrationDetail{}, handle.Invalid("divisionId", "not_found", "division of this tournament")
		}
	} else {
		division = pickDivision(divs, gender, playerType, hcp, c.Age(startDay))
	}
	fees, err := listFees(ctx, tx, tid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	pkgs, err := listPackages(ctx, tx, tid, fees)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	pkg := p.packageID
	if pkg != nil {
		var found *TournamentPackage
		for i := range pkgs {
			if pkgs[i].ID == *pkg {
				found = &pkgs[i]
			}
		}
		if found == nil || found.Status != "active" {
			return TournamentRegistrationDetail{}, handle.Invalid("packageId", "not_found", "active package of this tournament")
		}
		if found.PlayerType != "any" && found.PlayerType != playerType {
			return TournamentRegistrationDetail{}, handle.Invalid("packageId", "not_eligible", "the package is for "+found.PlayerType+"s")
		}
	} else {
		for i := range pkgs {
			if pkgs[i].IsDefault && pkgs[i].Status == "active" && (pkgs[i].PlayerType == "any" || pkgs[i].PlayerType == playerType) {
				pkg = &pkgs[i].ID
				break
			}
		}
	}
	if p.sponsorID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_sponsors WHERE id = $1 AND tournament_id = $2)`, *p.sponsorID, tid).Scan(&ok); err != nil {
			return TournamentRegistrationDetail{}, err
		}
		if !ok {
			return TournamentRegistrationDetail{}, handle.Invalid("sponsorId", "not_found", "sponsor of this tournament")
		}
	}
	// field size / waitlist under the tournament lock (no overbooking)
	status := "registered"
	var position *int
	if t.Registered >= t.FieldSize {
		if !t.WaitlistEnabled {
			return TournamentRegistrationDetail{}, errs.Conflict("field_full", t.Name+" is full")
		}
		if pol.WaitlistMax > 0 && t.Waitlisted >= pol.WaitlistMax {
			return TournamentRegistrationDetail{}, errs.Conflict("waitlist_full", "the waitlist of "+t.Name+" is full")
		}
		var next int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(waitlist_position), 0) + 1 FROM golf.tournament_registrations WHERE tournament_id = $1 AND status = 'waitlisted'`,
			tid).Scan(&next); err != nil {
			return TournamentRegistrationDetail{}, err
		}
		status, position = "waitlisted", &next
	}
	loc := location(ctx, tx, property)
	number, err := numbering.Next(ctx, tx, property, "TRG", now().In(loc))
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	var tokenHash *string
	if p.manageToken != "" {
		h := secret.HashToken(p.manageToken)
		tokenHash = &h
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_registrations (id, property_id, tournament_id, number, customer_id, member_id, player_name, gender,
		email, phone, player_type, channel, division_id, package_id, handicap_index, handicap_source, shirt_size, preferences, pairing_group, sponsor_id, status,
		waitlist_position, public_consent, currency, manage_token_hash, policy_versions, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::numeric,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$27)`,
		rid, property, tid, number, c.ID, memberID, c.Name, nullStr(gender), nullStr(c.Email), nullStr(c.Phone), playerType, p.channel, division, pkg, hcp, src,
		nullStr(p.shirtSize), nullStr(p.preferences), nullStr(p.pairingGroup), p.sponsorID, status, position, p.publicConsent, t.Currency, tokenHash,
		jsonOf(map[string]int{PolicyCode: ref.Version}), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return TournamentRegistrationDetail{}, errs.Conflict("already_registered", c.Name+" is already registered for this tournament")
		}
		return TournamentRegistrationDetail{}, err
	}
	reg, err := getRegistration(ctx, tx, rid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if status == "registered" {
		if err := m.charge(ctx, tx, property, t, &reg, pol); err != nil {
			return TournamentRegistrationDetail{}, err
		}
		if err := m.collect(ctx, tx, property, t, reg, p.payment, p.method, pol); err != nil {
			return TournamentRegistrationDetail{}, err
		}
		if err := m.publish(ctx, tx, EventRegistrationConfirmed, "golf.tournament_registration", rid, property, map[string]any{"tournamentId": tid,
			"registrationId": rid, "number": number, "customerId": c.ID, "playerType": playerType, "channel": p.channel, "status": status}); err != nil {
			return TournamentRegistrationDetail{}, err
		}
	}
	d, err := m.registrationDetail(ctx, tx, rid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament_registration", rid, number+" · "+c.Name, audit.ActionCreate, property, nil, d.TournamentRegistration, ""); err != nil {
		return d, err
	}
	if err := m.notifyRegistration(ctx, tx, t, d, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "registration", nil)
}

// pickDivision chooses the first active division (by sequence) whose
// gender, player type, handicap range and minimum age match.
func pickDivision(divs []TournamentDivision, gender, playerType string, hcp *string, age int) *uuid.UUID {
	for _, d := range divs {
		if d.Status != "active" || (d.Gender != "any" && d.Gender != gender) || (d.PlayerType != "any" && d.PlayerType != playerType) {
			continue
		}
		if d.AgeMin != nil && (age < 0 || age < *d.AgeMin) {
			continue
		}
		if d.HandicapMin != nil || d.HandicapMax != nil {
			if hcp == nil {
				continue
			}
			h := dec(*hcp)
			if (d.HandicapMin != nil && h.LessThan(dec(*d.HandicapMin))) || (d.HandicapMax != nil && h.GreaterThan(dec(*d.HandicapMax))) {
				continue
			}
		}
		return &d.ID
	}
	return nil
}

// revenueComponents map the fee components to the billing revenue
// components of the accounting export (billing.RevenueComponents).
var revenueComponents = map[string]string{"entry_fee": "tournament_fee", "green_fee": "green_fee", "caddy_fee": "caddy_fee", "cart_fee": "buggy_fee",
	"dinner": "fnb", "goodie_bag": "golf_other", "insurance": "hio_insurance", "other": "tournament_fee"}

var chargeTypes = map[string]string{"caddy_fee": "caddy_fee", "cart_fee": "cart_fee", "green_fee": "golf_round"}

// charge opens the registration folio and posts the Tournament Fee and the
// fees of the Tournament Package (nett amounts split by their Tax & Service
// rules) — FR-TRN-03 through billing.
func (m *Module) charge(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, reg *TournamentRegistration, pol TournamentPolicy) error {
	fees, err := listFees(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	list := applicableFees(fees, reg.PackageID, reg.PlayerType)
	total := decimal.Zero
	for _, f := range list {
		total = total.Add(dec(f.Amount))
	}
	if !total.IsPositive() {
		_, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET fee_total = 0, payment_status = 'not_required' WHERE id = $1`, reg.ID)
		reg.PaymentStatus, reg.FeeTotal = "not_required", "0"
		return err
	}
	if m.Billing == nil {
		return errs.Unavailable("billing is not available")
	}
	folio := reg.FolioID
	if folio == nil {
		f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: &reg.CustomerID,
			HolderName: reg.PlayerName, SourceType: "tournament", SourceID: &reg.ID, SourceRef: t.Code + " " + reg.Number}, BusinessLine: billing.LineGolf})
		if err != nil {
			return err
		}
		folio = &f.ID
	}
	for _, f := range list {
		amount := dec(f.Amount)
		if !amount.IsPositive() {
			continue
		}
		net, svc, tax, lines, err := taxSplit(ctx, tx, property, f.TaxCodes, amount, f.Currency)
		if err != nil {
			return err
		}
		ct := chargeTypes[f.Component]
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *folio, ChargeType: ct,
			Description: f.Name + " · " + t.Name, Quantity: decimal.NewFromInt(1), UnitPrice: amount, Net: net, Service: svc, Tax: tax, Total: amount,
			ReferenceType: "tournament_registration", ReferenceID: &reg.ID, Liability: f.Liability}, BusinessLine: billing.LineGolf,
			RevenueComponent: revenueComponents[f.Component], TaxLines: lines}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET folio_id = $2, fee_total = $3::numeric, payment_status = 'pending' WHERE id = $1`,
		reg.ID, *folio, total.String()); err != nil {
		return err
	}
	reg.FolioID, reg.FeeTotal, reg.PaymentStatus = folio, total.String(), "pending"
	return nil
}

// collect takes the payment of a registration: member charge to the
// member account, an online checkout (payment link, due per Tournament
// Policies) or payment later at the desk.
func (m *Module) collect(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, reg TournamentRegistration, mode, method string, pol TournamentPolicy) error {
	if reg.FolioID == nil || reg.PaymentStatus == "not_required" {
		return nil
	}
	sum, err := billing.FolioSummary(ctx, tx, *reg.FolioID)
	if err != nil {
		return err
	}
	balance := dec(sum.Balance)
	switch mode {
	case "member_charge":
		acct, err := m.memberAccount(ctx, tx, property, reg)
		if err != nil {
			return err
		}
		if balance.IsPositive() {
			if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: reg.FolioID, AccountID: &acct, MethodType: "member_account",
				Amount: balance, Description: "Tournament " + t.Code}); err != nil {
				return err
			}
		}
	case "online":
		if method == "" {
			method = "payment_gateway"
		}
		if err := oneOf("paymentMethod", method, "qris", "virtual_account", "card", "payment_gateway"); err != nil {
			return err
		}
		due := now().Add(time.Duration(pol.PaymentDueHours) * time.Hour)
		if balance.IsPositive() {
			if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: reg.FolioID, MethodType: method, Channel: "online", Amount: balance,
				ExpiresAt: &due, Description: "Tournament " + t.Code + " " + reg.Number}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET payment_due_at = $2 WHERE id = $1`, reg.ID, due); err != nil {
			return err
		}
	}
	return refreshPayment(ctx, tx, reg.ID)
}

// memberAccount is the member account (of the principal member) that pays
// a member charge.
func (m *Module) memberAccount(ctx context.Context, tx pgx.Tx, property uuid.UUID, reg TournamentRegistration) (uuid.UUID, error) {
	if reg.MemberID == nil || reg.PlayerType != "member" {
		return uuid.Nil, handle.Invalid("payment", "member_charge_not_allowed", "member charge needs an active member")
	}
	holder, err := membership.AccountHolder(ctx, tx, *reg.MemberID)
	if err != nil {
		return uuid.Nil, err
	}
	if holder == nil {
		holder = &reg.CustomerID
	}
	a, err := billing.AccountFor(ctx, tx, property, *holder, "member")
	if err != nil {
		return uuid.Nil, err
	}
	if a == nil {
		acc, err := m.Billing.EnsureAccount(ctx, tx, property, *holder, "member", reg.MemberID)
		if err != nil {
			return uuid.Nil, err
		}
		return acc.ID, nil
	}
	return a.ID, nil
}

// refreshPayment derives the payment status from the folio (paid once the
// balance is settled).
func refreshPayment(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	var folio *uuid.UUID
	var status string
	if err := tx.QueryRow(ctx, `SELECT folio_id, payment_status FROM golf.tournament_registrations WHERE id = $1`, rid).Scan(&folio, &status); err != nil {
		return err
	}
	if folio == nil || (status != "pending" && status != "paid") {
		return nil
	}
	sum, err := billing.FolioSummary(ctx, tx, *folio)
	if err != nil {
		return err
	}
	next := "pending"
	if dec(sum.Charges).IsPositive() && !dec(sum.Balance).IsPositive() {
		next = "paid"
	}
	if next == status {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE golf.tournament_registrations SET payment_status = $2, payment_due_at = CASE WHEN $2 = 'paid' THEN NULL ELSE payment_due_at END
		WHERE id = $1`, rid, next)
	return err
}

func (m *Module) notifyRegistration(ctx context.Context, tx pgx.Tx, t Tournament, d TournamentRegistrationDetail, event string) error {
	if event == "" {
		event = "golf.tournament_registered"
		if d.Status == "waitlisted" {
			event = "golf.tournament_waitlisted"
		}
	}
	user, err := customerUser(ctx, tx, d.CustomerID)
	if err != nil {
		return err
	}
	pay := map[string]string{"not_required": "no fee", "pending": "to be paid", "paid": "paid", "waived": "waived"}[d.PaymentStatus]
	due := ""
	if d.PaymentDueAt != nil {
		due = d.PaymentDueAt.In(location(ctx, tx, d.PropertyID)).Format("02 Jan 2006 15:04")
	}
	return m.notifyCustomer(ctx, tx, d.PropertyID, deref(d.Email), d.PlayerName, user, event, map[string]any{"name": d.PlayerName, "tournament": t.Name,
		"date": t.StartDate, "number": d.Number, "fee": money(dec(d.FeeTotal), d.Currency), "payment": pay, "position": d.WaitlistPosition, "due": due})
}

func customerUser(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (*uuid.UUID, error) {
	var u *uuid.UUID
	err := q.QueryRow(ctx, `SELECT user_id FROM crm.customers WHERE id = $1`, customer).Scan(&u)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	return u, err
}

// ── participants ──────────────────────────────────────────────────────────

// RegistrationPatch changes a participant (division, package, handicap …).
type TournamentRegistrationPatch struct {
	DivisionID    *uuid.UUID `json:"divisionId,omitempty" doc:"Zero UUID clears the division"`
	PackageID     *uuid.UUID `json:"packageId,omitempty" doc:"Before payment only"`
	HandicapIndex *string    `json:"handicapIndex,omitempty" doc:"Official handicap correction before the draw"`
	ShirtSize     *string    `json:"shirtSize,omitempty"`
	Preferences   *string    `json:"preferences,omitempty"`
	PairingGroup  *string    `json:"pairingGroup,omitempty"`
	SponsorID     *uuid.UUID `json:"sponsorId,omitempty"`
	PublicConsent *bool      `json:"publicConsent,omitempty"`
}

// UpdateRegistration changes a participant.
func (m *Module) UpdateRegistration(ctx context.Context, tx pgx.Tx, property, tid, rid uuid.UUID, in TournamentRegistrationPatch) (TournamentRegistrationDetail, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	r, err := lockRegistration(ctx, tx, property, tid, rid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if r.Status == "withdrawn" || t.Status == "completed" || t.Status == "cancelled" {
		return TournamentRegistrationDetail{}, errs.Conflict("registration_closed", "the registration cannot be changed")
	}
	before := r
	if in.DivisionID != nil {
		r.DivisionID = in.DivisionID
		if *in.DivisionID == uuid.Nil {
			r.DivisionID = nil
		} else {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_divisions WHERE id = $1 AND tournament_id = $2)`, *in.DivisionID, tid).Scan(&ok); err != nil {
				return TournamentRegistrationDetail{}, err
			}
			if !ok {
				return TournamentRegistrationDetail{}, handle.Invalid("divisionId", "not_found", "division of this tournament")
			}
		}
	}
	if in.HandicapIndex != nil {
		if t.Status == "in_progress" {
			return TournamentRegistrationDetail{}, errs.Conflict("tournament_started", "the handicap is frozen once the tournament started")
		}
		if r.HandicapIndex, err = decimalOrNil("handicapIndex", *in.HandicapIndex, -10, 54); err != nil {
			return TournamentRegistrationDetail{}, err
		}
		r.HandicapSource = "official"
		if r.HandicapIndex == nil {
			r.HandicapSource = "none"
		}
	}
	str := func(dst **string, v *string) {
		if v != nil {
			*dst = nullStr(*v)
		}
	}
	str(&r.ShirtSize, in.ShirtSize)
	str(&r.Preferences, in.Preferences)
	str(&r.PairingGroup, in.PairingGroup)
	if in.SponsorID != nil {
		r.SponsorID = in.SponsorID
		if *in.SponsorID == uuid.Nil {
			r.SponsorID = nil
		}
	}
	if in.PublicConsent != nil {
		r.PublicConsent = *in.PublicConsent
	}
	repost := false
	if in.PackageID != nil && (r.PackageID == nil || *r.PackageID != *in.PackageID) {
		if r.PaymentStatus == "paid" || r.PaymentStatus == "waived" {
			return TournamentRegistrationDetail{}, errs.Conflict("registration_paid", "the fees are paid; withdraw and register again to change the package")
		}
		r.PackageID = in.PackageID
		if *in.PackageID == uuid.Nil {
			r.PackageID = nil
		}
		repost = r.Status == "registered" || r.Status == "checked_in"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET division_id = $2, package_id = $3, handicap_index = $4::numeric, handicap_source = $5,
		shirt_size = $6, preferences = $7, pairing_group = $8, sponsor_id = $9, public_consent = $10, updated_by = $11 WHERE id = $1`,
		rid, r.DivisionID, r.PackageID, r.HandicapIndex, r.HandicapSource, r.ShirtSize, r.Preferences, r.PairingGroup, r.SponsorID, r.PublicConsent, actor(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return TournamentRegistrationDetail{}, handle.Invalid("packageId", "not_found", "package or sponsor of this tournament")
		}
		return TournamentRegistrationDetail{}, err
	}
	if repost {
		pol, _, err := LoadPolicy(ctx, tx, property)
		if err != nil {
			return TournamentRegistrationDetail{}, err
		}
		if r.FolioID != nil {
			if err := m.voidFees(ctx, tx, *r.FolioID, "package changed"); err != nil {
				return TournamentRegistrationDetail{}, err
			}
		}
		if err := m.charge(ctx, tx, property, t, &r, pol); err != nil {
			return TournamentRegistrationDetail{}, err
		}
		if err := refreshPayment(ctx, tx, rid); err != nil {
			return TournamentRegistrationDetail{}, err
		}
	}
	d, err := m.registrationDetail(ctx, tx, rid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament_registration", rid, r.Number+" · "+r.PlayerName, audit.ActionUpdate, property, before, d.TournamentRegistration, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "registration", nil)
}

// voidFees voids the tournament charges still on a folio and cancels a
// pending online checkout.
func (m *Module) voidFees(ctx context.Context, tx pgx.Tx, folio uuid.UUID, reason string) error {
	if err := m.Billing.CancelPending(ctx, tx, folio, reason); err != nil {
		return err
	}
	lines, err := billing.LinesOf(ctx, tx, folio)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if err := m.Billing.VoidCharge(ctx, tx, l.ID, reason); err != nil {
			return err
		}
	}
	return nil
}

// WithdrawResult is a withdrawn registration with its refunds.
type TournamentWithdrawResult struct {
	TournamentRegistrationDetail
	RefundPercent string           `json:"refundPercent" doc:"Refund share per Tournament Policies"`
	Refunds       []billing.Refund `json:"refunds"`
	Promoted      *string          `json:"promoted" doc:"Registration number promoted from the waitlist"`
}

type withdrawal struct {
	reason     string
	fullRefund bool // club cancellation
	cancelled  bool // tournament cancelled: no waitlist promotion
	noPromote  bool
}

// Withdraw withdraws a participant with the refund of Tournament Policies
// and promotes the waitlist (FR-TRN-04).
func (m *Module) Withdraw(ctx context.Context, tx pgx.Tx, property, tid, rid uuid.UUID, reason string) (TournamentWithdrawResult, error) {
	if err := handle.Required("reason", reason); err != nil {
		return TournamentWithdrawResult{}, err
	}
	if _, err := lockTournament(ctx, tx, property, tid); err != nil {
		return TournamentWithdrawResult{}, err
	}
	if _, err := lockRegistration(ctx, tx, property, tid, rid); err != nil {
		return TournamentWithdrawResult{}, err
	}
	return m.withdraw(ctx, tx, property, rid, withdrawal{reason: reason})
}

func (m *Module) withdraw(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, w withdrawal) (TournamentWithdrawResult, error) {
	r, err := getRegistration(ctx, tx, rid)
	if err != nil {
		return TournamentWithdrawResult{}, err
	}
	t, err := GetTournament(ctx, tx, r.TournamentID)
	if err != nil {
		return TournamentWithdrawResult{}, err
	}
	if r.Status == "withdrawn" {
		return TournamentWithdrawResult{}, errs.Conflict("already_withdrawn", "the registration is already withdrawn")
	}
	if !w.cancelled && t.Status == "in_progress" && r.Status != "waitlisted" {
		return TournamentWithdrawResult{}, errs.Conflict("tournament_started", "the tournament has started; record WD on the player's scorecard instead")
	}
	if !w.cancelled && (t.Status == "completed" || t.Status == "cancelled") {
		return TournamentWithdrawResult{}, errs.Conflict("tournament_closed", "the tournament is "+t.Status)
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return TournamentWithdrawResult{}, err
	}
	loc := location(ctx, tx, property)
	start, _ := time.Parse("2006-01-02", t.StartDate)
	days := int(start.Sub(localDate(now(), loc)).Hours() / 24)
	pct := decimal.Zero
	switch {
	case w.fullRefund || days >= pol.FullRefundDays:
		pct = hundred
	case days >= pol.PartialRefundDays:
		pct = dec(pol.PartialRefundPercent)
	}
	out := TournamentWithdrawResult{RefundPercent: pct.String(), Refunds: []billing.Refund{}}
	payStatus := r.PaymentStatus
	refunded := decimal.Zero
	if r.FolioID != nil && m.Billing != nil {
		if err := m.Billing.CancelPending(ctx, tx, *r.FolioID, w.reason); err != nil {
			return out, err
		}
		sum, err := billing.FolioSummary(ctx, tx, *r.FolioID)
		if err != nil {
			return out, err
		}
		paid := dec(sum.Payments)
		retained := decimal.Zero
		if paid.IsPositive() && pct.LessThan(hundred) {
			retained = paid.Mul(hundred.Sub(pct)).Div(hundred).Round(places(r.Currency))
		}
		if err := m.voidFees(ctx, tx, *r.FolioID, w.reason); err != nil {
			return out, err
		}
		if retained.IsPositive() {
			if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *r.FolioID, ChargeType: "cancellation_fee",
				Description: "Withdrawal fee · " + t.Name, Quantity: decimal.NewFromInt(1), UnitPrice: retained, Net: retained, Total: retained,
				ReferenceType: "tournament_registration", ReferenceID: &r.ID}, BusinessLine: billing.LineGolf, RevenueComponent: "cancellation_fee"}); err != nil {
				return out, err
			}
		}
		if paid.IsPositive() {
			refunds, err := m.Billing.RefundFolio(ctx, tx, *r.FolioID, w.reason, m.Refunds)
			if err != nil {
				return out, err
			}
			out.Refunds = append(out.Refunds, refunds...)
			for _, x := range refunds {
				refunded = refunded.Add(dec(x.Amount))
			}
		}
		switch {
		case paid.IsPositive() && refunded.GreaterThanOrEqual(paid):
			payStatus = "refunded"
		case refunded.IsPositive():
			payStatus = "partially_refunded"
		case paid.IsPositive():
			payStatus = "paid" // nothing refunded: the payment stays as withdrawal fee
		case payStatus != "waived":
			payStatus = "cancelled"
		}
	}
	wasPlaying := r.Status == "registered" || r.Status == "checked_in"
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET status = 'withdrawn', withdrawn_at = now(), withdraw_reason = $2, waitlist_position = NULL,
		payment_status = $3, refund_amount = $4::numeric, payment_due_at = NULL, updated_by = $5 WHERE id = $1`, rid, w.reason, payStatus, refunded.String(), actor(ctx)); err != nil {
		return out, err
	}
	// out of the draws of rounds not started
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_scores s SET status = 'wd', status_reason = $2 FROM golf.tournament_rounds x
		WHERE x.id = s.round_id AND s.registration_id = $1 AND x.status IN ('scheduled', 'drawn', 'published')`, rid, w.reason); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_flight_players fp USING golf.tournament_rounds x
		WHERE x.id = fp.round_id AND fp.registration_id = $1 AND x.status IN ('scheduled', 'drawn', 'published')`, rid); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_flights f WHERE f.tournament_id = $1 AND f.status = 'scheduled'
		AND NOT EXISTS (SELECT 1 FROM golf.tournament_flight_players p WHERE p.flight_id = f.id)`, t.ID); err != nil {
		return out, err
	}
	if r.Status == "waitlisted" {
		if err := renumberWaitlist(ctx, tx, t.ID); err != nil {
			return out, err
		}
	}
	if wasPlaying && !w.cancelled && !w.noPromote && pol.WaitlistAutoPromote {
		promoted, err := m.promoteWaitlist(ctx, tx, property, t.ID)
		if err != nil {
			return out, err
		}
		if len(promoted) > 0 {
			out.Promoted = &promoted[0]
		}
	}
	d, err := m.registrationDetail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	out.TournamentRegistrationDetail = d
	if err := record(ctx, tx, "golf.tournament_registration", rid, r.Number+" · "+r.PlayerName, "withdraw", property, r, map[string]any{"status": "withdrawn",
		"refundPercent": pct.String(), "refund": refunded.String(), "paymentStatus": payStatus}, w.reason); err != nil {
		return out, err
	}
	user, err := customerUser(ctx, tx, r.CustomerID)
	if err != nil {
		return out, err
	}
	if err := m.notifyCustomer(ctx, tx, property, deref(r.Email), r.PlayerName, user, "golf.tournament_withdrawn", map[string]any{"name": r.PlayerName,
		"tournament": t.Name, "number": r.Number, "reason": w.reason, "refund": money(refunded, r.Currency)}); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, t.ID, "registration", nil)
}

func renumberWaitlist(ctx context.Context, tx pgx.Tx, tid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations r SET waitlist_position = x.n FROM (
		SELECT id, row_number() OVER (ORDER BY waitlist_position, registered_at) AS n FROM golf.tournament_registrations
		WHERE tournament_id = $1 AND status = 'waitlisted') x WHERE r.id = x.id`, tid)
	return err
}

// promoteWaitlist fills free places from the waitlist (first come, first
// served): fees are posted and must be paid by the Tournament Policies due
// time for online channels.
func (m *Module) promoteWaitlist(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) ([]string, error) {
	var out []string
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	for {
		t, err := GetTournament(ctx, tx, tid)
		if err != nil {
			return out, err
		}
		if t.Registered >= t.FieldSize || t.Status == "cancelled" || t.Status == "completed" {
			return out, nil
		}
		var rid uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM golf.tournament_registrations WHERE tournament_id = $1 AND status = 'waitlisted'
			ORDER BY waitlist_position, registered_at LIMIT 1 FOR UPDATE`, tid).Scan(&rid)
		if dbtx.IsNoRows(err) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET status = 'registered', waitlist_position = NULL, promoted_at = now() WHERE id = $1`, rid); err != nil {
			return out, err
		}
		r, err := getRegistration(ctx, tx, rid)
		if err != nil {
			return out, err
		}
		if err := m.charge(ctx, tx, property, t, &r, pol); err != nil {
			return out, err
		}
		if r.PaymentStatus == "pending" && (r.Channel == "website" || r.Channel == "member_app") {
			if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET payment_due_at = $2 WHERE id = $1`, rid,
				now().Add(time.Duration(pol.PaymentDueHours)*time.Hour)); err != nil {
				return out, err
			}
		}
		if err := renumberWaitlist(ctx, tx, tid); err != nil {
			return out, err
		}
		d, err := m.registrationDetail(ctx, tx, rid)
		if err != nil {
			return out, err
		}
		if err := record(ctx, tx, "golf.tournament_registration", rid, r.Number+" · "+r.PlayerName, "promote", property, map[string]any{"status": "waitlisted"},
			map[string]any{"status": "registered"}, "waitlist promotion"); err != nil {
			return out, err
		}
		if err := m.publish(ctx, tx, EventRegistrationConfirmed, "golf.tournament_registration", rid, property, map[string]any{"tournamentId": tid,
			"registrationId": rid, "number": r.Number, "customerId": r.CustomerID, "playerType": r.PlayerType, "channel": r.Channel, "status": "registered",
			"promoted": true}); err != nil {
			return out, err
		}
		if err := m.notifyRegistration(ctx, tx, t, d, "golf.tournament_promoted"); err != nil {
			return out, err
		}
		out = append(out, r.Number)
	}
}

// CheckIn checks a participant in at the Tournament Desk.
func (m *Module) CheckIn(ctx context.Context, tx pgx.Tx, property, tid, rid uuid.UUID) (TournamentRegistrationDetail, error) {
	t, err := tournamentAt(ctx, tx, property, tid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	r, err := lockRegistration(ctx, tx, property, tid, rid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if t.Status == "draft" || t.Status == "completed" || t.Status == "cancelled" {
		return TournamentRegistrationDetail{}, errs.Conflict("invalid_status", "check-in is not possible for a "+t.Status+" tournament")
	}
	switch r.Status {
	case "checked_in":
		return m.registrationDetail(ctx, tx, rid) // idempotent (offline replay)
	case "registered":
	default:
		return TournamentRegistrationDetail{}, errs.Conflict("not_registered", r.PlayerName+" is "+r.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET status = 'checked_in', checked_in_at = now(), checked_in_by = $2, updated_by = $2
		WHERE id = $1`, rid, actor(ctx)); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	d, err := m.registrationDetail(ctx, tx, rid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament_registration", rid, r.Number+" · "+r.PlayerName, "check_in", property, map[string]any{"status": r.Status},
		map[string]any{"status": "checked_in", "paymentStatus": d.PaymentStatus}, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "check_in", nil)
}

// PayInput takes a tournament fee payment at the desk.
type TournamentPayInput struct {
	MethodType string `json:"methodType" enum:"cash,card,bank_transfer,qris,virtual_account,member_account"`
	Amount     string `json:"amount,omitempty" doc:"Default: the folio balance"`
	Reference  string `json:"reference,omitempty"`
}

// Pay takes a payment of the registration folio at the desk.
func (m *Module) Pay(ctx context.Context, tx pgx.Tx, property, tid, rid uuid.UUID, in TournamentPayInput) (TournamentRegistrationDetail, error) {
	if _, err := tournamentAt(ctx, tx, property, tid); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	r, err := lockRegistration(ctx, tx, property, tid, rid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if r.FolioID == nil || r.Status == "withdrawn" || r.Status == "waitlisted" {
		return TournamentRegistrationDetail{}, errs.Conflict("nothing_to_pay", "there is nothing to pay for this registration")
	}
	sum, err := billing.FolioSummary(ctx, tx, *r.FolioID)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	amount, err := handle.Decimal("amount", in.Amount, dec(sum.Balance))
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if !amount.IsPositive() {
		return TournamentRegistrationDetail{}, errs.Conflict("nothing_to_pay", "the tournament fees are paid")
	}
	if err := oneOf("methodType", in.MethodType, "cash", "card", "bank_transfer", "qris", "virtual_account", "member_account"); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	pin := billing.PaymentInput{FolioID: r.FolioID, MethodType: in.MethodType, Channel: "venue", Amount: amount, Reference: in.Reference,
		PayerName: r.PlayerName, Description: "Tournament " + r.Number}
	if in.MethodType == "member_account" {
		acct, err := m.memberAccount(ctx, tx, property, r)
		if err != nil {
			return TournamentRegistrationDetail{}, err
		}
		pin.AccountID, pin.Channel = &acct, ""
	}
	if _, err := m.Billing.TakePayment(ctx, tx, pin); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if err := refreshPayment(ctx, tx, rid); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	d, err := m.registrationDetail(ctx, tx, rid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament_registration", rid, r.Number+" · "+r.PlayerName, "payment", property, map[string]any{"paymentStatus": r.PaymentStatus},
		map[string]any{"paymentStatus": d.PaymentStatus, "amount": amount.String(), "method": in.MethodType}, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "registration", nil)
}

// RequestFeeWaiver submits a complimentary entry for approval (no workflow
// configured: approved at once).
func (m *Module) RequestFeeWaiver(ctx context.Context, tx pgx.Tx, property, tid, rid uuid.UUID, reason string) (TournamentRegistrationDetail, error) {
	if err := handle.Required("reason", reason); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	t, err := tournamentAt(ctx, tx, property, tid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	r, err := lockRegistration(ctx, tx, property, tid, rid)
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if r.Status == "withdrawn" || r.Status == "waitlisted" || !dec(r.FeeTotal).IsPositive() || r.PaymentStatus == "waived" {
		return TournamentRegistrationDetail{}, errs.Conflict("nothing_to_waive", "there is no fee to waive for this registration")
	}
	if deref(r.FeeWaiverStatus) == "pending" {
		return TournamentRegistrationDetail{}, errs.Conflict("waiver_pending", "a fee waiver is already waiting for approval")
	}
	if m.Approvals == nil {
		return TournamentRegistrationDetail{}, errs.Unavailable("approvals are not available")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET fee_waiver_status = 'pending', fee_waiver_reason = $2, updated_by = $3 WHERE id = $1`,
		rid, reason, actor(ctx)); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	reqID, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: FeeWaiverType.Code, DocumentID: rid, DocumentRef: r.Number,
		Title: "Fee waiver · " + r.PlayerName + " · " + t.Name, PropertyID: property, Attributes: map[string]any{"amount": dec(r.FeeTotal).InexactFloat64(),
			"tournament": t.Code, "reason": reason}})
	if err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET approval_request_id = $2 WHERE id = $1`, rid, reqID); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	d, err := m.registrationDetail(ctx, tx, rid)
	if err != nil {
		return d, err
	}
	return d, record(ctx, tx, "golf.tournament_registration", rid, r.Number+" · "+r.PlayerName, "request_fee_waiver", property, nil,
		map[string]any{"approvalRequestId": reqID, "feeWaiverStatus": d.FeeWaiverStatus}, reason)
}

// Decision applies approval decisions of tournament documents.
func (m *Module) Decision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	if d.DocumentType != FeeWaiverType.Code {
		return nil
	}
	r, err := getRegistration(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	if d.Status != "approved" {
		st := "rejected"
		if d.Status == "cancelled" {
			st = "cancelled"
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET fee_waiver_status = $2 WHERE id = $1`, r.ID, st); err != nil {
			return err
		}
		return record(ctx, tx, "golf.tournament_registration", r.ID, r.Number, "fee_waiver_"+st, r.PropertyID, nil, map[string]any{"feeWaiverStatus": st}, d.Reason)
	}
	refunded := decimal.Zero
	if r.FolioID != nil && m.Billing != nil {
		if err := m.voidFees(ctx, tx, *r.FolioID, "fee waived"); err != nil {
			return err
		}
		refunds, err := m.Billing.RefundFolio(ctx, tx, *r.FolioID, "Tournament fee waived", m.Refunds)
		if err != nil {
			return err
		}
		for _, x := range refunds {
			refunded = refunded.Add(dec(x.Amount))
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET fee_waiver_status = 'approved', payment_status = 'waived', payment_due_at = NULL,
		refund_amount = refund_amount + $2::numeric WHERE id = $1`, r.ID, refunded.String()); err != nil {
		return err
	}
	if err := record(ctx, tx, "golf.tournament_registration", r.ID, r.Number+" · "+r.PlayerName, "fee_waived", r.PropertyID,
		map[string]any{"paymentStatus": r.PaymentStatus}, map[string]any{"paymentStatus": "waived", "refund": refunded.String()}, d.Reason); err != nil {
		return err
	}
	return live(ctx, tx, r.PropertyID, r.TournamentID, "registration", nil)
}

// ── lists ─────────────────────────────────────────────────────────────────

// Registrations lists the participants of a tournament.
func Registrations(ctx context.Context, q dbtx.Querier, tid uuid.UUID, status, division, search string, limit int) ([]TournamentRegistration, error) {
	return handle.List[TournamentRegistration](q.Query(ctx, registrationSelect+` WHERE r.tournament_id = $1 AND ($2 = '' OR r.status = ANY(string_to_array($2, ',')))
		AND ($3 = '' OR r.division_id::text = $3) AND ($4 = '' OR r.player_name ILIKE '%' || $4 || '%' OR r.number ILIKE '%' || $4 || '%')
		ORDER BY CASE r.status WHEN 'checked_in' THEN 0 WHEN 'registered' THEN 0 WHEN 'waitlisted' THEN 1 ELSE 2 END, r.waitlist_position, r.registered_at
		LIMIT $5`, tid, status, division, search, limit))
}

// ── payment settled (billing.payment_settled) ─────────────────────────────

// OnPaymentSettled marks a registration paid when its folio is settled
// (online payment link, gateway webhook).
func (m *Module) OnPaymentSettled(ctx context.Context, tx pgx.Tx, p billing.SettledPayload) error {
	if p.SourceType == nil || *p.SourceType != "tournament" || p.FolioID == nil {
		return nil
	}
	var rid, property, tid uuid.UUID
	var before string
	err := tx.QueryRow(ctx, `SELECT id, property_id, tournament_id, payment_status FROM golf.tournament_registrations WHERE folio_id = $1 FOR UPDATE`, *p.FolioID).
		Scan(&rid, &property, &tid, &before)
	if dbtx.IsNoRows(err) {
		return nil // a sponsor folio
	}
	if err != nil {
		return err
	}
	if err := refreshPayment(ctx, tx, rid); err != nil {
		return err
	}
	var after string
	if err := tx.QueryRow(ctx, `SELECT payment_status FROM golf.tournament_registrations WHERE id = $1`, rid).Scan(&after); err != nil {
		return err
	}
	if after == before {
		return nil
	}
	if err := record(ctx, tx, "golf.tournament_registration", rid, p.Number, "payment_settled", property, map[string]any{"paymentStatus": before},
		map[string]any{"paymentStatus": after, "paymentId": p.PaymentID}, ""); err != nil {
		return err
	}
	return live(ctx, tx, property, tid, "registration", nil)
}

// ReleaseUnpaid withdraws online registrations whose payment is overdue
// (Tournament Policies payment due) and promotes the waitlist.
func (m *Module) ReleaseUnpaid(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `SELECT r.id, r.property_id FROM golf.tournament_registrations r JOIN golf.tournaments t ON t.id = r.tournament_id
		WHERE r.status = 'registered' AND r.payment_status = 'pending' AND r.payment_due_at < now() AND t.status IN ('open', 'closed') LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type due struct{ id, property uuid.UUID }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.property); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	for _, d := range list {
		if _, err := m.withdraw(ctx, tx, d.property, d.id, withdrawal{reason: "Payment not received in time"}); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// CloseDueRegistrations closes registrations whose window ended.
func (m *Module) CloseDueRegistrations(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `UPDATE golf.tournaments SET status = 'closed' WHERE status = 'open' AND registration_closes_at < now() RETURNING id, property_id, code`)
	if err != nil {
		return 0, err
	}
	type closed struct {
		id, property uuid.UUID
		code         string
	}
	var list []closed
	for rows.Next() {
		var c closed
		if err := rows.Scan(&c.id, &c.property, &c.code); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, c)
	}
	rows.Close()
	for _, c := range list {
		if err := record(ctx, tx, "golf.tournament", c.id, c.code, "close_registration", c.property, map[string]any{"status": "open"},
			map[string]any{"status": "closed"}, "registration deadline"); err != nil {
			return 0, err
		}
		if err := live(ctx, tx, c.property, c.id, "status", nil); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}
