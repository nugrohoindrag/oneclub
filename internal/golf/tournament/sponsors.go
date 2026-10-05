package tournament

// FR-TRN-10 Sponsors (package, logo on the leaderboard and start sheet,
// hole sponsorship, sponsorship invoiced to the sponsor's corporate account
// through the billing folio and invoice) and Prizes (per position / award,
// recipient and hand-over); FR-TRN-09 special awards (Nearest to Pin,
// Longest Drive, Hole-in-One).

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// TournamentSponsor is a sponsor of a tournament.
type TournamentSponsor struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Name               string     `json:"name" db:"name"`
	SponsorLevel       string     `json:"sponsorLevel" db:"sponsor_level" enum:"title,platinum,gold,silver,bronze,hole,supporting,in_kind"`
	PackageName        *string    `json:"packageName" db:"package_name"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id"`
	ContactName        *string    `json:"contactName" db:"contact_name"`
	ContactEmail       *string    `json:"contactEmail" db:"contact_email"`
	ContactPhone       *string    `json:"contactPhone" db:"contact_phone"`
	Amount             string     `json:"amount" db:"amount" doc:"Sponsorship (nett)"`
	Currency           string     `json:"currency" db:"currency"`
	TaxCodes           []string   `json:"taxCodes" db:"tax_codes"`
	LogoFileID         *uuid.UUID `json:"logoFileId" db:"logo_file_id"`
	LogoURL            *string    `json:"logoUrl" db:"logo_url"`
	Holes              []int32    `json:"holes" db:"holes" doc:"Hole sponsorship (hole numbers)"`
	ShowOnLeaderboard  bool       `json:"showOnLeaderboard" db:"show_on_leaderboard"`
	ShowOnStartSheet   bool       `json:"showOnStartSheet" db:"show_on_start_sheet"`
	Sequence           int        `json:"sequence" db:"sequence"`
	FolioID            *uuid.UUID `json:"folioId" db:"folio_id"`
	InvoiceID          *uuid.UUID `json:"invoiceId" db:"invoice_id"`
	InvoiceNumber      *string    `json:"invoiceNumber" db:"invoice_number"`
	InvoiceStatus      *string    `json:"invoiceStatus" db:"invoice_status"`
	InvoicedAt         *time.Time `json:"invoicedAt" db:"invoiced_at"`
	Status             string     `json:"status" db:"status" enum:"active,cancelled"`
	Notes              *string    `json:"notes" db:"notes"`
}

const sponsorSelect = `SELECT s.id, s.name, s.sponsor_level, s.package_name, s.corporate_account_id, s.customer_id, s.contact_name, s.contact_email,
	s.contact_phone, trim_scale(s.amount)::text AS amount, s.currency, s.tax_codes, s.logo_file_id, s.logo_url, s.holes, s.show_on_leaderboard,
	s.show_on_start_sheet, s.sequence, s.folio_id, s.invoice_id, i.number AS invoice_number, i.status AS invoice_status, s.invoiced_at, s.status, s.notes
	FROM golf.tournament_sponsors s LEFT JOIN billing.invoices i ON i.id = s.invoice_id`

func listSponsors(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentSponsor, error) {
	return handle.List[TournamentSponsor](q.Query(ctx, sponsorSelect+` WHERE s.tournament_id = $1 ORDER BY s.sequence, s.name`, tid))
}

func getSponsor(ctx context.Context, q dbtx.Querier, tid, sid uuid.UUID) (TournamentSponsor, error) {
	rows, err := q.Query(ctx, sponsorSelect+` WHERE s.id = $1 AND s.tournament_id = $2`, sid, tid)
	return handle.One[TournamentSponsor](rows, err, "sponsor")
}

// SponsorInput creates a sponsor; on update omitted fields stay.
type TournamentSponsorInput struct {
	Name               *string    `json:"name,omitempty" doc:"Required on create"`
	SponsorLevel       *string    `json:"sponsorLevel,omitempty" enum:"title,platinum,gold,silver,bronze,hole,supporting,in_kind"`
	PackageName        *string    `json:"packageName,omitempty"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId,omitempty" doc:"Billed company (city ledger)"`
	CustomerID         *uuid.UUID `json:"customerId,omitempty" doc:"Billed person when the sponsor is not a company"`
	ContactName        *string    `json:"contactName,omitempty"`
	ContactEmail       *string    `json:"contactEmail,omitempty"`
	ContactPhone       *string    `json:"contactPhone,omitempty"`
	Amount             *string    `json:"amount,omitempty" doc:"Sponsorship amount (nett)"`
	TaxCodes           []string   `json:"taxCodes,omitempty" doc:"Tax & Service rules included in the amount"`
	LogoFileID         *uuid.UUID `json:"logoFileId,omitempty"`
	LogoURL            *string    `json:"logoUrl,omitempty"`
	Holes              []int32    `json:"holes,omitempty" doc:"Sponsored holes"`
	ShowOnLeaderboard  *bool      `json:"showOnLeaderboard,omitempty"`
	ShowOnStartSheet   *bool      `json:"showOnStartSheet,omitempty"`
	Sequence           *int       `json:"sequence,omitempty"`
	Status             *string    `json:"status,omitempty" enum:"active,cancelled"`
	Notes              *string    `json:"notes,omitempty"`
}

// SaveSponsor creates (sid nil) or updates a sponsor.
func (m *Module) SaveSponsor(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, sid *uuid.UUID, in TournamentSponsorInput) (TournamentSponsor, error) {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return TournamentSponsor{}, err
	}
	cur := TournamentSponsor{SponsorLevel: "supporting", Amount: "0", Currency: t.Currency, TaxCodes: []string{}, Holes: []int32{}, ShowOnLeaderboard: true,
		ShowOnStartSheet: true, Sequence: 1, Status: "active"}
	if sid != nil {
		if cur, err = getSponsor(ctx, tx, tid, *sid); err != nil {
			return cur, err
		}
	} else if in.Name == nil {
		return cur, handle.Invalid("name", "required", "name is required")
	}
	before := cur
	str := func(dst **string, v *string) {
		if v != nil {
			*dst = nullStr(*v)
		}
	}
	if in.Name != nil {
		cur.Name = strings.TrimSpace(*in.Name)
	}
	if in.SponsorLevel != nil {
		cur.SponsorLevel = *in.SponsorLevel
	}
	str(&cur.PackageName, in.PackageName)
	str(&cur.ContactName, in.ContactName)
	str(&cur.ContactEmail, in.ContactEmail)
	str(&cur.ContactPhone, in.ContactPhone)
	str(&cur.LogoURL, in.LogoURL)
	str(&cur.Notes, in.Notes)
	for _, u := range []struct {
		dst **uuid.UUID
		v   *uuid.UUID
	}{{&cur.CorporateAccountID, in.CorporateAccountID}, {&cur.CustomerID, in.CustomerID}, {&cur.LogoFileID, in.LogoFileID}} {
		if u.v != nil {
			*u.dst = u.v
			if *u.v == uuid.Nil {
				*u.dst = nil
			}
		}
	}
	if in.Amount != nil {
		a, err := handle.Decimal("amount", *in.Amount, decimal.Zero)
		if err != nil {
			return cur, err
		}
		if a.IsNegative() {
			return cur, handle.Invalid("amount", "invalid", "amount must not be negative")
		}
		if cur.InvoiceID != nil && !a.Equal(dec(cur.Amount)) && deref(cur.InvoiceStatus) != "void" {
			return cur, errs.Conflict("sponsor_invoiced", "the sponsorship is invoiced; void the invoice before changing the amount")
		}
		cur.Amount = a.String()
	}
	if in.TaxCodes != nil {
		cur.TaxCodes = in.TaxCodes
	}
	if in.Holes != nil {
		cur.Holes = in.Holes
	}
	if in.ShowOnLeaderboard != nil {
		cur.ShowOnLeaderboard = *in.ShowOnLeaderboard
	}
	if in.ShowOnStartSheet != nil {
		cur.ShowOnStartSheet = *in.ShowOnStartSheet
	}
	if in.Sequence != nil {
		cur.Sequence = *in.Sequence
	}
	if in.Status != nil {
		cur.Status = *in.Status
	}
	if cur.Name == "" {
		return cur, handle.Invalid("name", "required", "name is required")
	}
	if err := oneOf("sponsorLevel", cur.SponsorLevel, "title", "platinum", "gold", "silver", "bronze", "hole", "supporting", "in_kind"); err != nil {
		return cur, err
	}
	if err := oneOf("status", cur.Status, "active", "cancelled"); err != nil {
		return cur, err
	}
	for _, h := range cur.Holes {
		if h < 1 || h > 36 {
			return cur, handle.Invalid("holes", "invalid", "hole numbers 1–36")
		}
	}
	if err := checkTaxCodes(ctx, tx, property, cur.TaxCodes); err != nil {
		return cur, err
	}
	action := audit.ActionUpdate
	if sid == nil {
		action = audit.ActionCreate
		cur.ID = id.New()
		_, err = tx.Exec(ctx, `INSERT INTO golf.tournament_sponsors (id, property_id, tournament_id, name, sponsor_level, package_name, corporate_account_id,
			customer_id, contact_name, contact_email, contact_phone, amount, currency, tax_codes, logo_file_id, logo_url, holes, show_on_leaderboard,
			show_on_start_sheet, sequence, status, notes, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::numeric,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$23)`, cur.ID, property, tid, cur.Name,
			cur.SponsorLevel, cur.PackageName, cur.CorporateAccountID, cur.CustomerID, cur.ContactName, cur.ContactEmail, cur.ContactPhone, cur.Amount,
			cur.Currency, cur.TaxCodes, cur.LogoFileID, cur.LogoURL, cur.Holes, cur.ShowOnLeaderboard, cur.ShowOnStartSheet, cur.Sequence, cur.Status,
			cur.Notes, actor(ctx))
	} else {
		_, err = tx.Exec(ctx, `UPDATE golf.tournament_sponsors SET name = $2, sponsor_level = $3, package_name = $4, corporate_account_id = $5, customer_id = $6,
			contact_name = $7, contact_email = $8, contact_phone = $9, amount = $10::numeric, tax_codes = $11, logo_file_id = $12, logo_url = $13, holes = $14,
			show_on_leaderboard = $15, show_on_start_sheet = $16, sequence = $17, status = $18, notes = $19, updated_by = $20 WHERE id = $1`, cur.ID, cur.Name,
			cur.SponsorLevel, cur.PackageName, cur.CorporateAccountID, cur.CustomerID, cur.ContactName, cur.ContactEmail, cur.ContactPhone, cur.Amount,
			cur.TaxCodes, cur.LogoFileID, cur.LogoURL, cur.Holes, cur.ShowOnLeaderboard, cur.ShowOnStartSheet, cur.Sequence, cur.Status, cur.Notes, actor(ctx))
	}
	if err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return cur, handle.Invalid("corporateAccountId", "not_found", "corporate account, customer or logo file not found")
		}
		return cur, err
	}
	out, err := getSponsor(ctx, tx, tid, cur.ID)
	if err != nil {
		return out, err
	}
	if err := record(ctx, tx, "golf.tournament_sponsor", out.ID, t.Code+" · "+out.Name, action, property, before, out, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "sponsor", nil)
}

func checkTaxCodes(ctx context.Context, q dbtx.Querier, property uuid.UUID, codes []string) error {
	for _, c := range codes {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.tax_service_rules WHERE property_id = $1 AND code = $2)`, property, c).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid("taxCodes", "not_found", "unknown Tax & Service rule "+c)
		}
	}
	return nil
}

// DeleteSponsor removes a sponsor that was not invoiced and has no prize or
// guest attached.
func (m *Module) DeleteSponsor(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID) error {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return err
	}
	s, err := getSponsor(ctx, tx, tid, sid)
	if err != nil {
		return err
	}
	if s.FolioID != nil {
		return errs.Conflict("sponsor_invoiced", "the sponsorship is billed; set the sponsor cancelled instead")
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_prizes WHERE sponsor_id = $1)
		OR EXISTS (SELECT 1 FROM golf.tournament_registrations WHERE sponsor_id = $1)`, sid).Scan(&used); err != nil {
		return err
	}
	if used {
		return errs.Conflict("sponsor_in_use", "prizes or guests refer to this sponsor; set it cancelled instead")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_sponsors WHERE id = $1`, sid); err != nil {
		return err
	}
	return record(ctx, tx, "golf.tournament_sponsor", sid, t.Code+" · "+s.Name, audit.ActionDelete, property, s, nil, "")
}

// taxSplit derives net, service and tax of a nett amount from the Tax &
// Service rules named by codes (the amount is inclusive).
func taxSplit(ctx context.Context, q dbtx.Querier, property uuid.UUID, codes []string, amount decimal.Decimal, cur string) (net, service, tax decimal.Decimal, lines []commercial.Line, err error) {
	if len(codes) == 0 || amount.IsZero() {
		return amount, decimal.Zero, decimal.Zero, []commercial.Line{}, nil
	}
	all, err := commercial.RulesAt(ctx, q, property, now())
	if err != nil {
		return amount, decimal.Zero, decimal.Zero, nil, err
	}
	var rules []commercial.Rule
	for _, r := range all {
		for _, c := range codes {
			if r.Code == c {
				rules = append(rules, r)
			}
		}
	}
	b := commercial.CalculateMode(rules, amount, cur, now(), "nett")
	return dec(b.NetAmount), commercial.SumKind(b.Lines, "service"), commercial.SumKind(b.Lines, "tax"), b.Lines, nil
}

// SponsorInvoiceInput invoices a sponsorship.
type TournamentSponsorInvoiceInput struct {
	TermsDays *int   `json:"termsDays,omitempty" doc:"Default: Credit Policies term for companies"`
	Notes     string `json:"notes,omitempty"`
}

// InvoiceSponsor bills the sponsorship: a tournament folio of the sponsor
// with the sponsorship charge, issued as an invoice to the corporate account
// (or the sponsoring customer) through billing (FR-TRN-10, FR-TRN-13).
func (m *Module) InvoiceSponsor(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID, in TournamentSponsorInvoiceInput) (TournamentSponsor, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentSponsor{}, err
	}
	if t.Status == "cancelled" {
		return TournamentSponsor{}, errs.Conflict("tournament_cancelled", "the tournament is cancelled")
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM golf.tournament_sponsors WHERE id = $1 FOR UPDATE`, sid); err != nil {
		return TournamentSponsor{}, err
	}
	s, err := getSponsor(ctx, tx, tid, sid)
	if err != nil {
		return s, err
	}
	if s.Status != "active" {
		return s, errs.Conflict("sponsor_cancelled", "the sponsor is cancelled")
	}
	if s.InvoiceID != nil && deref(s.InvoiceStatus) != "void" {
		return s, errs.Conflict("already_invoiced", "the sponsorship is already invoiced ("+deref(s.InvoiceNumber)+")")
	}
	amount := dec(s.Amount)
	if !amount.IsPositive() {
		return s, errs.Conflict("nothing_to_invoice", "the sponsorship amount is zero (in kind)")
	}
	if s.CorporateAccountID == nil && s.CustomerID == nil {
		return s, handle.Invalid("corporateAccountId", "payer_required", "set the sponsor's corporate account or customer first")
	}
	if m.Billing == nil || m.Invoices == nil {
		return s, errs.Unavailable("billing is not available")
	}
	folio := s.FolioID
	if folio == nil || deref(s.InvoiceStatus) == "void" {
		f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: s.CustomerID,
			HolderName: s.Name, SourceType: "tournament", SourceID: &s.ID, SourceRef: t.Code + " sponsor"}, BusinessLine: billing.LineGolf,
			CorporateName: s.Name})
		if err != nil {
			return s, err
		}
		folio = &f.ID
		net, svc, tax, lines, err := taxSplit(ctx, tx, property, s.TaxCodes, amount, t.Currency)
		if err != nil {
			return s, err
		}
		desc := "Sponsorship · " + t.Name
		if s.PackageName != nil {
			desc = "Sponsorship " + *s.PackageName + " · " + t.Name
		}
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: f.ID, Description: desc, Quantity: decimal.NewFromInt(1),
			UnitPrice: amount, Net: net, Service: svc, Tax: tax, Total: amount, ReferenceType: "tournament_sponsor", ReferenceID: &s.ID},
			BusinessLine: billing.LineGolf, RevenueComponent: "sponsorship", TaxLines: lines}); err != nil {
			return s, err
		}
	}
	inv, err := m.Invoices.CreateInvoice(ctx, tx, property, billing.InvoiceInput{FolioID: folio, CorporateAccountID: s.CorporateAccountID, TermsDays: in.TermsDays,
		Notes: strings.TrimSpace(t.Code + " " + t.Name + " sponsorship. " + in.Notes), Issue: true,
		BillTo: &billing.BillTo{Email: deref(s.ContactEmail), Phone: deref(s.ContactPhone)}})
	if err != nil {
		return s, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_sponsors SET folio_id = $2, invoice_id = $3, invoiced_at = now(), updated_by = $4 WHERE id = $1`,
		sid, *folio, inv.ID, actor(ctx)); err != nil {
		return s, err
	}
	out, err := getSponsor(ctx, tx, tid, sid)
	if err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.tournament_sponsor", sid, t.Code+" · "+s.Name, "invoice", property, s,
		map[string]any{"invoiceId": inv.ID, "number": inv.Number, "total": inv.Total}, "")
}

// ── prizes ────────────────────────────────────────────────────────────────

// TournamentPrize is a prize (per position or special award).
type TournamentPrize struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	Category       string     `json:"category" db:"category" enum:"gross,net,stableford,nearest_to_pin,longest_drive,hole_in_one,lucky_draw,other"`
	DivisionID     *uuid.UUID `json:"divisionId" db:"division_id" doc:"Null: overall"`
	DivisionName   *string    `json:"divisionName" db:"division_name"`
	Position       *int       `json:"position" db:"position" doc:"Ranking prizes: 1st, 2nd …"`
	HoleNumber     *int       `json:"holeNumber" db:"hole_number" doc:"Nearest to Pin, Longest Drive, Hole-in-One"`
	Name           string     `json:"name" db:"name"`
	Description    *string    `json:"description" db:"description"`
	Value          string     `json:"value" db:"value" doc:"Prize value (prize cost in the Tournament Report)"`
	Currency       string     `json:"currency" db:"currency"`
	SponsorID      *uuid.UUID `json:"sponsorId" db:"sponsor_id"`
	SponsorName    *string    `json:"sponsorName" db:"sponsor_name"`
	RegistrationID *uuid.UUID `json:"registrationId" db:"registration_id" doc:"Recipient"`
	RecipientName  *string    `json:"recipientName" db:"recipient_name"`
	ResultText     *string    `json:"resultText" db:"result_text" doc:"e.g. 1.35 m, 285 m, Net 68 (C/B)"`
	Status         string     `json:"status" db:"status" enum:"open,awarded,handed_over,cancelled"`
	AwardedAt      *time.Time `json:"awardedAt" db:"awarded_at"`
	HandedOverAt   *time.Time `json:"handedOverAt" db:"handed_over_at"`
	HandedOverTo   *string    `json:"handedOverTo" db:"handed_over_to"`
}

const prizeSelect = `SELECT p.id, p.category, p.division_id, d.name AS division_name, p.position, p.hole_number, p.name, p.description,
	trim_scale(p.value)::text AS value, p.currency, p.sponsor_id, s.name AS sponsor_name, p.registration_id, r.player_name AS recipient_name, p.result_text,
	p.status, p.awarded_at, p.handed_over_at, p.handed_over_to
	FROM golf.tournament_prizes p LEFT JOIN golf.tournament_divisions d ON d.id = p.division_id LEFT JOIN golf.tournament_sponsors s ON s.id = p.sponsor_id
	LEFT JOIN golf.tournament_registrations r ON r.id = p.registration_id`

func listPrizes(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentPrize, error) {
	return handle.List[TournamentPrize](q.Query(ctx, prizeSelect+` WHERE p.tournament_id = $1
		ORDER BY CASE p.category WHEN 'gross' THEN 0 WHEN 'net' THEN 1 WHEN 'stableford' THEN 1 ELSE 2 END, d.sequence NULLS FIRST, p.position, p.name`, tid))
}

func getPrize(ctx context.Context, q dbtx.Querier, tid, pid uuid.UUID) (TournamentPrize, error) {
	rows, err := q.Query(ctx, prizeSelect+` WHERE p.id = $1 AND p.tournament_id = $2`, pid, tid)
	return handle.One[TournamentPrize](rows, err, "prize")
}

// PrizeInput creates a prize; on update omitted fields stay.
type TournamentPrizeInput struct {
	Category    *string    `json:"category,omitempty" enum:"gross,net,stableford,nearest_to_pin,longest_drive,hole_in_one,lucky_draw,other" doc:"Required on create"`
	DivisionID  *uuid.UUID `json:"divisionId,omitempty" doc:"Empty: overall"`
	Position    *int       `json:"position,omitempty" doc:"Required for gross, net and stableford prizes"`
	HoleNumber  *int       `json:"holeNumber,omitempty"`
	Name        *string    `json:"name,omitempty" doc:"Required on create"`
	Description *string    `json:"description,omitempty"`
	Value       *string    `json:"value,omitempty"`
	SponsorID   *uuid.UUID `json:"sponsorId,omitempty"`
	Status      *string    `json:"status,omitempty" enum:"open,cancelled"`
}

var rankingCategories = []string{"gross", "net", "stableford"}

func isRanking(c string) bool {
	for _, x := range rankingCategories {
		if x == c {
			return true
		}
	}
	return false
}

// SavePrize creates (pid nil) or updates a prize.
func (m *Module) SavePrize(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, pid *uuid.UUID, in TournamentPrizeInput) (TournamentPrize, error) {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return TournamentPrize{}, err
	}
	cur := TournamentPrize{Value: "0", Currency: t.Currency, Status: "open"}
	if pid != nil {
		if cur, err = getPrize(ctx, tx, tid, *pid); err != nil {
			return cur, err
		}
		if cur.Status != "open" && cur.Status != "cancelled" {
			return cur, errs.Conflict("prize_awarded", "an awarded prize cannot be changed")
		}
	} else if in.Category == nil || in.Name == nil {
		return cur, handle.Invalid("category", "required", "category and name are required")
	}
	before := cur
	if in.Category != nil {
		cur.Category = *in.Category
	}
	if in.DivisionID != nil {
		cur.DivisionID = in.DivisionID
		if *in.DivisionID == uuid.Nil {
			cur.DivisionID = nil
		}
	}
	if in.Position != nil {
		cur.Position = in.Position
	}
	if in.HoleNumber != nil {
		cur.HoleNumber = in.HoleNumber
	}
	if in.Name != nil {
		cur.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		cur.Description = nullStr(*in.Description)
	}
	if in.Value != nil {
		v, err := handle.Decimal("value", *in.Value, decimal.Zero)
		if err != nil {
			return cur, err
		}
		if v.IsNegative() {
			return cur, handle.Invalid("value", "invalid", "value must not be negative")
		}
		cur.Value = v.String()
	}
	if in.SponsorID != nil {
		cur.SponsorID = in.SponsorID
		if *in.SponsorID == uuid.Nil {
			cur.SponsorID = nil
		}
	}
	if in.Status != nil {
		cur.Status = *in.Status
	}
	if err := oneOf("category", cur.Category, "gross", "net", "stableford", "nearest_to_pin", "longest_drive", "hole_in_one", "lucky_draw", "other"); err != nil {
		return cur, err
	}
	if err := oneOf("status", cur.Status, "open", "cancelled"); err != nil {
		return cur, err
	}
	if cur.Name == "" {
		return cur, handle.Invalid("name", "required", "name is required")
	}
	if isRanking(cur.Category) && (cur.Position == nil || *cur.Position < 1) {
		return cur, handle.Invalid("position", "required", "ranking prizes need a position")
	}
	if (cur.Category == "nearest_to_pin" || cur.Category == "longest_drive" || cur.Category == "hole_in_one") && cur.HoleNumber == nil {
		return cur, handle.Invalid("holeNumber", "required", "special awards need the hole")
	}
	if cur.DivisionID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_divisions WHERE id = $1 AND tournament_id = $2)`, *cur.DivisionID, tid).Scan(&ok); err != nil {
			return cur, err
		}
		if !ok {
			return cur, handle.Invalid("divisionId", "not_found", "division of this tournament")
		}
	}
	if cur.SponsorID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_sponsors WHERE id = $1 AND tournament_id = $2)`, *cur.SponsorID, tid).Scan(&ok); err != nil {
			return cur, err
		}
		if !ok {
			return cur, handle.Invalid("sponsorId", "not_found", "sponsor of this tournament")
		}
	}
	action := audit.ActionUpdate
	if pid == nil {
		action = audit.ActionCreate
		cur.ID = id.New()
		_, err = tx.Exec(ctx, `INSERT INTO golf.tournament_prizes (id, property_id, tournament_id, category, division_id, position, hole_number, name, description,
			value, currency, sponsor_id, status, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,$14)`,
			cur.ID, property, tid, cur.Category, cur.DivisionID, cur.Position, cur.HoleNumber, cur.Name, cur.Description, cur.Value, cur.Currency, cur.SponsorID,
			cur.Status, actor(ctx))
	} else {
		_, err = tx.Exec(ctx, `UPDATE golf.tournament_prizes SET category = $2, division_id = $3, position = $4, hole_number = $5, name = $6, description = $7,
			value = $8::numeric, sponsor_id = $9, status = $10, updated_by = $11 WHERE id = $1`, cur.ID, cur.Category, cur.DivisionID, cur.Position,
			cur.HoleNumber, cur.Name, cur.Description, cur.Value, cur.SponsorID, cur.Status, actor(ctx))
	}
	if err != nil {
		if ok, _ := dbtx.IsCheckViolation(err); ok {
			return cur, handle.Invalid("position", "invalid", "position 1–50, hole 1–36")
		}
		return cur, err
	}
	out, err := getPrize(ctx, tx, tid, cur.ID)
	if err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.tournament_prize", out.ID, t.Code+" · "+out.Name, action, property, before, out, "")
}

// DeletePrize removes an open prize.
func (m *Module) DeletePrize(ctx context.Context, tx pgx.Tx, property, tid, pid uuid.UUID) error {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return err
	}
	p, err := getPrize(ctx, tx, tid, pid)
	if err != nil {
		return err
	}
	if p.Status == "awarded" || p.Status == "handed_over" {
		return errs.Conflict("prize_awarded", "an awarded prize cannot be deleted")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_prizes WHERE id = $1`, pid); err != nil {
		return err
	}
	return record(ctx, tx, "golf.tournament_prize", pid, t.Code+" · "+p.Name, audit.ActionDelete, property, p, nil, "")
}

// AwardInput names the recipient of a special award.
type TournamentAwardInput struct {
	RegistrationID uuid.UUID `json:"registrationId"`
	ResultText     string    `json:"resultText,omitempty" doc:"e.g. 1.35 m (Nearest to Pin), 285 m (Longest Drive)"`
}

// AwardPrize awards a prize to a participant (special awards measured on
// the course; ranking prizes are awarded by Finalize).
func (m *Module) AwardPrize(ctx context.Context, tx pgx.Tx, property, tid, pid uuid.UUID, in TournamentAwardInput) (TournamentPrize, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentPrize{}, err
	}
	if t.Status != "in_progress" && t.Status != "completed" {
		return TournamentPrize{}, errs.Conflict("invalid_status", "prizes are awarded once the tournament has started")
	}
	p, err := getPrize(ctx, tx, tid, pid)
	if err != nil {
		return p, err
	}
	if p.Status != "open" && p.Status != "awarded" {
		return p, errs.Conflict("invalid_status", "the prize is "+p.Status)
	}
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM golf.tournament_registrations WHERE id = $1 AND tournament_id = $2`, in.RegistrationID, tid).Scan(&st); err != nil {
		if dbtx.IsNoRows(err) {
			return p, handle.Invalid("registrationId", "not_found", "participant of this tournament")
		}
		return p, err
	}
	if st != "registered" && st != "checked_in" {
		return p, handle.Invalid("registrationId", "invalid", "the participant is "+st)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_prizes SET registration_id = $2, result_text = $3, status = 'awarded', awarded_at = now(), awarded_by = $4,
		updated_by = $4 WHERE id = $1`, pid, in.RegistrationID, nullStr(in.ResultText), actor(ctx)); err != nil {
		return p, err
	}
	out, err := getPrize(ctx, tx, tid, pid)
	if err != nil {
		return out, err
	}
	if err := record(ctx, tx, "golf.tournament_prize", pid, t.Code+" · "+p.Name, "award", property, p, out, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "prize", nil)
}

// HandOverInput records the prize hand-over (serah terima).
type TournamentHandOverInput struct {
	HandedOverTo string `json:"handedOverTo,omitempty" doc:"Default: the recipient"`
}

// HandOverPrize records that an awarded prize was handed over.
func (m *Module) HandOverPrize(ctx context.Context, tx pgx.Tx, property, tid, pid uuid.UUID, in TournamentHandOverInput) (TournamentPrize, error) {
	t, err := tournamentAt(ctx, tx, property, tid)
	if err != nil {
		return TournamentPrize{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM golf.tournament_prizes WHERE id = $1 FOR UPDATE`, pid); err != nil {
		return TournamentPrize{}, err
	}
	p, err := getPrize(ctx, tx, tid, pid)
	if err != nil {
		return p, err
	}
	if p.Status != "awarded" {
		return p, errs.Conflict("not_awarded", "only an awarded prize can be handed over")
	}
	to := strings.TrimSpace(in.HandedOverTo)
	if to == "" {
		to = deref(p.RecipientName)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_prizes SET status = 'handed_over', handed_over_at = now(), handed_over_by = $2, handed_over_to = $3,
		updated_by = $2 WHERE id = $1`, pid, actor(ctx), to); err != nil {
		return p, err
	}
	out, err := getPrize(ctx, tx, tid, pid)
	if err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.tournament_prize", pid, t.Code+" · "+p.Name, "hand_over", property, p, out, "")
}
