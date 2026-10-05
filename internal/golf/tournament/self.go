package tournament

// Self-service registration (FR-TRN-03, FR-APP-P3-04, FR-WEB-P3-03): the
// Member App registers the signed-in player and pays the Tournament Fee by
// member charge or online; the website registers a public tournament with
// an online payment and returns a secure manage link. Players withdraw
// themselves until the Tournament Policies cut-off (later only at the golf
// office); the refund follows the same policy as the Back Office.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/handle"
)

// MemberRegistrationInput registers the signed-in player (Member App).
type TournamentMemberRegistrationInput struct {
	PackageID     *uuid.UUID `json:"packageId,omitempty" doc:"Default: the default package of the player type"`
	HandicapIndex string     `json:"handicapIndex,omitempty" doc:"Declared index, used only when OneClub has no handicap for the player"`
	ShirtSize     string     `json:"shirtSize,omitempty"`
	Preferences   string     `json:"preferences,omitempty"`
	PublicConsent bool       `json:"publicConsent,omitempty" doc:"Show my name on the public leaderboard and the Hall of Fame"`
	Payment       string     `json:"payment,omitempty" enum:"online,member_charge" doc:"Default online"`
	PaymentMethod string     `json:"paymentMethod,omitempty" enum:"qris,virtual_account,card"`
}

// RegisterMe registers the signed-in customer from the Member App.
func (m *Module) RegisterMe(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, c crm.Customer, in TournamentMemberRegistrationInput) (TournamentRegistrationDetail, error) {
	pay := in.Payment
	if pay == "" {
		pay = "online"
	}
	if err := oneOf("payment", pay, "online", "member_charge"); err != nil {
		return TournamentRegistrationDetail{}, err
	}
	if pay == "online" && in.PaymentMethod != "" {
		if err := oneOf("paymentMethod", in.PaymentMethod, "qris", "virtual_account", "card"); err != nil {
			return TournamentRegistrationDetail{}, err
		}
	}
	return m.register(ctx, tx, property, tid, registerParams{channel: "member_app", customer: c, packageID: in.PackageID, handicap: in.HandicapIndex,
		handicapSrc: "declared", shirtSize: in.ShirtSize, preferences: in.Preferences, publicConsent: in.PublicConsent, payment: pay, method: in.PaymentMethod})
}

// PublicRegistrationInput registers a player on the website (public
// tournaments, FR-WEB-P3-03).
type PublicTournamentRegistrationInput struct {
	PropertyID    uuid.UUID       `json:"propertyId"`
	Guest         crm.PublicGuest `json:"guest"`
	Gender        string          `json:"gender,omitempty" enum:"male,female"`
	PackageID     *uuid.UUID      `json:"packageId,omitempty"`
	HandicapIndex string          `json:"handicapIndex,omitempty" doc:"Declared handicap index"`
	ShirtSize     string          `json:"shirtSize,omitempty"`
	PublicConsent bool            `json:"publicConsent,omitempty" doc:"Show my name on the public leaderboard and the Hall of Fame"`
	Consent       bool            `json:"consent" doc:"Agreement to the privacy notice (required)"`
	PaymentMethod string          `json:"paymentMethod" enum:"qris,virtual_account,card"`
	CaptchaToken  string          `json:"captchaToken,omitempty"`
}

// Property implements crm.PublicRequest.
func (in PublicTournamentRegistrationInput) Property() uuid.UUID { return in.PropertyID }

// Visitor implements crm.PublicRequest.
func (in PublicTournamentRegistrationInput) Visitor() crm.PublicGuest { return in.Guest }

// PublicRegistration is a registration as the player sees it (website,
// manage link).
type PublicTournamentRegistration struct {
	Number           string               `json:"number"`
	TournamentID     uuid.UUID            `json:"tournamentId"`
	TournamentCode   string               `json:"tournamentCode"`
	TournamentName   string               `json:"tournamentName"`
	StartDate        string               `json:"startDate"`
	PlayerName       string               `json:"playerName"`
	Status           string               `json:"status" enum:"registered,waitlisted,withdrawn,checked_in"`
	WaitlistPosition *int                 `json:"waitlistPosition"`
	PackageName      *string              `json:"packageName"`
	FeeTotal         string               `json:"feeTotal"`
	Balance          string               `json:"balance"`
	Currency         string               `json:"currency"`
	PaymentStatus    string               `json:"paymentStatus"`
	PaymentDueAt     *time.Time           `json:"paymentDueAt"`
	Checkout         *TournamentCheckout  `json:"checkout" doc:"Pending online payment"`
	Start            *TournamentStartInfo `json:"start" doc:"Published start (flight, hole / tee time)"`
	ManageToken      string               `json:"manageToken,omitempty" doc:"Secure link token; returned once at registration"`
	CanWithdraw      bool                 `json:"canWithdraw"`
	WithdrawUntil    *time.Time           `json:"withdrawUntil"`
	RefundPercent    string               `json:"refundPercent" doc:"Refund share if withdrawn now (Tournament Policies)"`
	Customer         *uuid.UUID           `json:"-"`
	RegistrationID   uuid.UUID            `json:"-"`
}

// Checkout is the pending online payment of a registration.
type TournamentCheckout struct {
	PaymentID   uuid.UUID  `json:"paymentId"`
	Method      string     `json:"method"`
	Amount      string     `json:"amount"`
	Status      string     `json:"status"`
	ExternalID  *string    `json:"externalId"`
	CheckoutURL *string    `json:"checkoutUrl"`
	QRString    *string    `json:"qrString"`
	VANumber    *string    `json:"vaNumber"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

// selfWithdrawal tells whether a player may still withdraw himself and the
// refund share of now.
func (m *Module) selfWithdrawal(ctx context.Context, q dbtx.Querier, property uuid.UUID, t Tournament) (bool, *time.Time, decimal.Decimal, error) {
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return false, nil, decimal.Zero, err
	}
	rounds, err := listRounds(ctx, q, t.ID)
	if err != nil || len(rounds) == 0 {
		return false, nil, decimal.Zero, err
	}
	loc := location(ctx, q, property)
	day, _ := time.Parse("2006-01-02", rounds[0].PlayDate)
	until := atLocal(day, rounds[0].StartTime, loc).Add(-time.Duration(pol.SelfWithdrawalCutoffHours) * time.Hour)
	start, _ := time.Parse("2006-01-02", t.StartDate)
	days := int(start.Sub(localDate(now(), loc)).Hours() / 24)
	pct := decimal.Zero
	switch {
	case days >= pol.FullRefundDays:
		pct = hundred
	case days >= pol.PartialRefundDays:
		pct = dec(pol.PartialRefundPercent)
	}
	ok := (t.Status == "open" || t.Status == "closed") && now().Before(until)
	return ok, &until, pct, nil
}

// publicRegistration renders a registration for the player.
func (m *Module) publicRegistration(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (PublicTournamentRegistration, error) {
	d, err := m.registrationDetail(ctx, q, rid)
	if err != nil {
		return PublicTournamentRegistration{}, err
	}
	t, err := GetTournament(ctx, q, d.TournamentID)
	if err != nil {
		return PublicTournamentRegistration{}, err
	}
	out := PublicTournamentRegistration{Number: d.Number, TournamentID: d.TournamentID, TournamentCode: d.TournamentCode, TournamentName: d.TournamentName,
		StartDate: d.StartDate, PlayerName: d.PlayerName, Status: d.Status, WaitlistPosition: d.WaitlistPosition, PackageName: d.PackageName,
		FeeTotal: d.FeeTotal, Balance: "0", Currency: d.Currency, PaymentStatus: d.PaymentStatus, PaymentDueAt: d.PaymentDueAt, RefundPercent: "0",
		Customer: &d.CustomerID, RegistrationID: d.ID}
	if d.Folio != nil {
		out.Balance = d.Folio.Balance
	}
	if d.Payment != nil {
		p := d.Payment
		out.Checkout = &TournamentCheckout{PaymentID: p.ID, Method: p.MethodType, Amount: p.Amount, Status: p.Status, ExternalID: p.ExternalID,
			CheckoutURL: p.CheckoutURL, QRString: p.QRString, VANumber: p.VANumber, ExpiresAt: p.ExpiresAt}
	}
	if d.Start != nil && d.Start.Published {
		out.Start = d.Start
	}
	if d.Status == "registered" || d.Status == "waitlisted" {
		ok, until, pct, err := m.selfWithdrawal(ctx, q, d.PropertyID, t)
		if err != nil {
			return out, err
		}
		out.CanWithdraw, out.WithdrawUntil, out.RefundPercent = ok, until, pct.String()
	}
	return out, nil
}

// RegisterPublic registers a website visitor (customer deduplicated by
// phone / e-mail) with an online payment due per Tournament Policies.
func (m *Module) RegisterPublic(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, c crm.Customer, in PublicTournamentRegistrationInput) (PublicTournamentRegistration, error) {
	if !in.Consent {
		return PublicTournamentRegistration{}, handle.Invalid("consent", "required", "please agree to the privacy notice")
	}
	if !strings.Contains(in.Guest.Email, "@") {
		return PublicTournamentRegistration{}, handle.Invalid("guest.email", "required", "an e-mail address is required for the payment and the start sheet")
	}
	if err := oneOf("paymentMethod", in.PaymentMethod, "qris", "virtual_account", "card"); err != nil {
		return PublicTournamentRegistration{}, err
	}
	if in.Gender != "" {
		if err := oneOf("gender", in.Gender, "male", "female"); err != nil {
			return PublicTournamentRegistration{}, err
		}
	}
	token := secret.RandomToken(24)
	d, err := m.register(ctx, tx, property, tid, registerParams{channel: "website", customer: c, packageID: in.PackageID, handicap: in.HandicapIndex,
		handicapSrc: "declared", shirtSize: in.ShirtSize, publicConsent: in.PublicConsent, gender: in.Gender, payment: "online", method: in.PaymentMethod,
		manageToken: token})
	if err != nil {
		return PublicTournamentRegistration{}, err
	}
	out, err := m.publicRegistration(ctx, tx, d.ID)
	out.ManageToken = token
	return out, err
}

// RegistrationByToken finds a website registration by its manage token.
func RegistrationByToken(ctx context.Context, q dbtx.Querier, property uuid.UUID, token string) (uuid.UUID, error) {
	var rid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM golf.tournament_registrations WHERE manage_token_hash = $1 AND property_id = $2`, secret.HashToken(token), property).
		Scan(&rid)
	if err != nil {
		if dbtx.IsNoRows(err) {
			return rid, errs.NotFound("registration")
		}
		return rid, err
	}
	return rid, nil
}

// WithdrawSelf withdraws the player's own registration (Member App or the
// website manage link) until the Tournament Policies cut-off.
func (m *Module) WithdrawSelf(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, reason string) (PublicTournamentRegistration, error) {
	var tid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT tournament_id FROM golf.tournament_registrations WHERE id = $1 AND property_id = $2`, rid, property).Scan(&tid); err != nil {
		if dbtx.IsNoRows(err) {
			return PublicTournamentRegistration{}, errs.NotFound("registration")
		}
		return PublicTournamentRegistration{}, err
	}
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return PublicTournamentRegistration{}, err
	}
	r, err := lockRegistration(ctx, tx, property, tid, rid)
	if err != nil {
		return PublicTournamentRegistration{}, err
	}
	if r.Status != "registered" && r.Status != "waitlisted" {
		return PublicTournamentRegistration{}, errs.Conflict("not_withdrawable", "the registration is "+r.Status)
	}
	ok, until, _, err := m.selfWithdrawal(ctx, tx, property, t)
	if err != nil {
		return PublicTournamentRegistration{}, err
	}
	if !ok {
		msg := "the online withdrawal has closed; please contact the golf office"
		if until != nil {
			msg = "the online withdrawal closed at " + until.In(location(ctx, tx, property)).Format("02 Jan 2006 15:04") + "; please contact the golf office"
		}
		return PublicTournamentRegistration{}, errs.Conflict("withdrawal_closed", msg)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "withdrawn by the player"
	}
	if _, err := m.withdraw(ctx, tx, property, rid, withdrawal{reason: reason}); err != nil {
		return PublicTournamentRegistration{}, err
	}
	return m.publicRegistration(ctx, tx, rid)
}

// MyRegistration is a tournament of the signed-in player (My Tournaments).
type MyTournamentRegistration struct {
	PublicTournamentRegistration
	RegistrationID uuid.UUID `json:"registrationId"`
	Format         string    `json:"format"`
	EndDate        string    `json:"endDate"`
	CourseName     string    `json:"courseName"`
	TournamentStat string    `json:"tournamentStatus" enum:"draft,open,closed,in_progress,completed,cancelled"`
	HasResults     bool      `json:"hasResults"`
}

// MyRegistrations lists the tournaments of a customer, latest first.
func (m *Module) MyRegistrations(ctx context.Context, q dbtx.Querier, customer uuid.UUID) ([]MyTournamentRegistration, error) {
	type row struct {
		ID uuid.UUID `db:"id"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT r.id FROM golf.tournament_registrations r JOIN golf.tournaments t ON t.id = r.tournament_id
		WHERE r.customer_id = $1 ORDER BY t.start_date DESC, r.registered_at DESC LIMIT 50`, customer))
	if err != nil {
		return nil, err
	}
	out := []MyTournamentRegistration{}
	for _, x := range rows {
		p, err := m.publicRegistration(ctx, q, x.ID)
		if err != nil {
			return nil, err
		}
		t, err := GetTournament(ctx, q, p.TournamentID)
		if err != nil {
			return nil, err
		}
		out = append(out, MyTournamentRegistration{PublicTournamentRegistration: p, RegistrationID: x.ID, Format: t.Format, EndDate: t.EndDate, CourseName: t.CourseName,
			TournamentStat: t.Status, HasResults: t.Status == "completed"})
	}
	return out, nil
}

// MyRegistrationID returns the customer's registration (404 otherwise).
func MyRegistrationID(ctx context.Context, q dbtx.Querier, customer, rid uuid.UUID) (uuid.UUID, error) {
	var p uuid.UUID
	err := q.QueryRow(ctx, `SELECT property_id FROM golf.tournament_registrations WHERE id = $1 AND customer_id = $2`, rid, customer).Scan(&p)
	if dbtx.IsNoRows(err) {
		return p, errs.NotFound("registration")
	}
	return p, err
}
