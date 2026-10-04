package golf

// Reciprocal Club (PRD P2 EP-13): inbound member verification (replacing the
// P1 free-text input), introduction letters for members visiting partner
// clubs (approval + PDF), visit quota per agreement, club settlement and the
// Reciprocal Visits report.

import (
	"bytes"
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/membership"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
)

type club struct {
	ID         uuid.UUID  `db:"id"`
	Code       string     `db:"code"`
	Name       string     `db:"name"`
	Country    string     `db:"country"`
	City       *string    `db:"city"`
	From       *time.Time `db:"agreement_from"`
	To         *time.Time `db:"agreement_to"`
	Quota      *int       `db:"visit_quota"`
	Period     string     `db:"quota_period"`
	Settlement string     `db:"settlement_mode"`
	Status     string     `db:"status"`
}

func loadClub(ctx context.Context, q dbtx.Querier, property, cid uuid.UUID) (club, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, country, city, agreement_from, agreement_to, visit_quota, quota_period, settlement_mode, status
		FROM golf.reciprocal_clubs WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, cid, property)
	return handle.One[club](rows, err, "reciprocal club")
}

func (c club) activeOn(d time.Time) bool {
	day := d.Format("2006-01-02")
	return c.Status == "active" && (c.From == nil || c.From.Format("2006-01-02") <= day) && (c.To == nil || c.To.Format("2006-01-02") >= day)
}

// Visit is a reciprocal visit (inbound guest or outbound member).
type Visit struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	Number           string     `json:"number" db:"number"`
	Direction        string     `json:"direction" db:"direction" enum:"inbound,outbound"`
	ClubID           uuid.UUID  `json:"clubId" db:"club_id"`
	ClubName         string     `json:"clubName" db:"club_name"`
	Country          string     `json:"country" db:"country"`
	CustomerID       *uuid.UUID `json:"customerId" db:"customer_id"`
	VisitorName      string     `json:"visitorName" db:"visitor_name"`
	HomeCardNo       *string    `json:"homeCardNo" db:"home_card_no"`
	CardValidUntil   *time.Time `json:"cardValidUntil" db:"card_valid_until"`
	LetterRef        *string    `json:"letterRef" db:"letter_ref"`
	LetterID         *uuid.UUID `json:"letterId" db:"letter_id"`
	Documents        []string   `json:"documents" db:"documents"`
	VisitDate        time.Time  `json:"visitDate" db:"visit_date"`
	PlayerID         *uuid.UUID `json:"playerId" db:"booking_player_id" doc:"Booking player of the visit (P1 reciprocal player)"`
	ChargeAmount     string     `json:"chargeAmount" db:"charge_amount"`
	Verified         bool       `json:"verified" db:"verified"`
	SettlementStatus string     `json:"settlementStatus" db:"settlement_status" enum:"not_applicable,open,invoiced,settled"`
	CreatedAt        time.Time  `json:"createdAt" db:"created_at"`
}

const visitSelect = `SELECT v.id, v.number, v.direction, v.club_id, c.name AS club_name, c.country, v.customer_id, v.visitor_name, v.home_card_no,
	v.card_valid_until, v.letter_ref, v.letter_id, v.documents, v.visit_date, v.booking_player_id,
	trim_scale(coalesce((SELECT sum(p.price_total) FROM golf.booking_players p WHERE p.reciprocal_visit_id = v.id AND p.status IN ('booked', 'checked_in')),
	  v.charge_amount))::text AS charge_amount,
	v.verified, v.settlement_status, v.created_at FROM golf.reciprocal_visits v JOIN golf.reciprocal_clubs c ON c.id = v.club_id`

func (m *Module) visit(ctx context.Context, q dbtx.Querier, vid uuid.UUID) (Visit, error) {
	rows, err := q.Query(ctx, visitSelect+` WHERE v.id = $1`, vid)
	return handle.One[Visit](rows, err, "reciprocal visit")
}

type InboundInput struct {
	ClubID         uuid.UUID `json:"clubId"`
	VisitorName    string    `json:"visitorName"`
	Phone          string    `json:"phone,omitempty"`
	Email          string    `json:"email,omitempty"`
	HomeCardNo     string    `json:"homeCardNo,omitempty"`
	CardValidUntil string    `json:"cardValidUntil,omitempty" doc:"YYYY-MM-DD"`
	LetterRef      string    `json:"letterRef,omitempty" doc:"Introduction letter number of the home club"`
	LetterDate     string    `json:"letterDate,omitempty" doc:"YYYY-MM-DD"`
	Documents      []string  `json:"documents,omitempty" doc:"Scans of the letter and card"`
	VisitDate      string    `json:"visitDate,omitempty" doc:"YYYY-MM-DD; default today"`
}

// VerifyInbound verifies a reciprocal guest: active agreement, introduction
// letter, home club card validity and visit quota (FR-RCP-03/05).
func (m *Module) VerifyInbound(ctx context.Context, tx pgx.Tx, property uuid.UUID, in InboundInput) (Visit, error) {
	if err := handle.Required("visitorName", in.VisitorName); err != nil {
		return Visit{}, err
	}
	c, err := loadClub(ctx, tx, property, in.ClubID)
	if err != nil {
		return Visit{}, err
	}
	pol, err := m.reciprocalPolicy(ctx, tx, property)
	if err != nil {
		return Visit{}, err
	}
	now := localNow(ctx, tx)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if in.VisitDate != "" {
		if day, err = time.Parse("2006-01-02", in.VisitDate); err != nil {
			return Visit{}, handle.Invalid("visitDate", "invalid_date", "visitDate must be YYYY-MM-DD")
		}
	}
	if !c.activeOn(day) {
		return Visit{}, errs.Conflict("agreement_inactive", "the reciprocal agreement with "+c.Name+" is not active on this date")
	}
	if pol.RequireLetter {
		if in.LetterRef == "" {
			return Visit{}, handle.Invalid("letterRef", "required", "the introduction letter of the home club is required")
		}
		if in.LetterDate != "" {
			ld, err := time.Parse("2006-01-02", in.LetterDate)
			if err != nil {
				return Visit{}, handle.Invalid("letterDate", "invalid_date", "letterDate must be YYYY-MM-DD")
			}
			if day.Sub(ld) > time.Duration(pol.LetterValidityDays)*24*time.Hour {
				return Visit{}, errs.Conflict("letter_expired", "the introduction letter is older than the Reciprocal Policies allow")
			}
		}
	}
	var valid *time.Time
	if pol.RequireHomeCard {
		if in.HomeCardNo == "" || in.CardValidUntil == "" {
			return Visit{}, handle.Invalid("homeCardNo", "required", "home club card number and validity are required")
		}
	}
	if in.CardValidUntil != "" {
		v, err := time.Parse("2006-01-02", in.CardValidUntil)
		if err != nil {
			return Visit{}, handle.Invalid("cardValidUntil", "invalid_date", "cardValidUntil must be YYYY-MM-DD")
		}
		if v.Before(day) {
			return Visit{}, errs.Conflict("card_expired", "the home club membership card has expired")
		}
		valid = &v
	}
	if pol.EnforceQuota && c.Quota != nil {
		start := time.Date(day.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		if c.Period == "month" {
			start = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
		var used int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.reciprocal_visits WHERE club_id = $1 AND direction = 'inbound' AND verified
			AND visit_date >= $2 AND visit_date <= $3`, c.ID, start, day).Scan(&used); err != nil {
			return Visit{}, err
		}
		if used >= *c.Quota {
			return Visit{}, errs.Conflict("quota_exhausted", "the visit quota of the agreement with "+c.Name+" is used up")
		}
	}
	var cust *uuid.UUID
	if in.Phone != "" || in.Email != "" {
		p, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: in.VisitorName, Phone: in.Phone, Email: in.Email})
		if err != nil {
			return Visit{}, err
		}
		cust = &p.ID
	}
	no, err := number(ctx, tx, property, "RCV")
	if err != nil {
		return Visit{}, err
	}
	settle := "not_applicable"
	if c.Settlement == "periodic" {
		settle = "open"
	}
	if in.Documents == nil {
		in.Documents = []string{}
	}
	vid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.reciprocal_visits (id, property_id, number, direction, club_id, customer_id, visitor_name, home_card_no,
		card_valid_until, letter_ref, documents, visit_date, verified, verified_by, settlement_status, created_by)
		VALUES ($1,$2,$3,'inbound',$4,$5,$6,$7,$8,$9,$10,$11,true,$12,$13,$12)`, vid, property, no, c.ID, cust, in.VisitorName, nullStr(in.HomeCardNo), valid,
		nullStr(in.LetterRef), in.Documents, day, actorPtr(ctx), settle); err != nil {
		return Visit{}, err
	}
	v, err := m.visit(ctx, tx, vid)
	if err != nil {
		return v, err
	}
	return v, record(ctx, tx, "golf.reciprocal_visit", vid, no+" · "+in.VisitorName, "verify", property, nil, v, "")
}

// ── introduction letters (FR-RCP-04) ──────────────────────────────────────

type LetterInput struct {
	ClubID     uuid.UUID `json:"clubId"`
	CustomerID uuid.UUID `json:"customerId"`
	PlayFrom   string    `json:"playFrom" doc:"YYYY-MM-DD"`
	PlayTo     string    `json:"playTo" doc:"YYYY-MM-DD"`
	Players    int       `json:"players,omitempty"`
	Notes      string    `json:"notes,omitempty"`
}

// Letter is an introduction letter.
type Letter struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Number       string     `json:"number" db:"number"`
	ClubID       uuid.UUID  `json:"clubId" db:"club_id"`
	ClubName     string     `json:"clubName" db:"club_name"`
	CustomerID   uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName string     `json:"customerName" db:"customer_name"`
	PlayFrom     time.Time  `json:"playFrom" db:"play_from"`
	PlayTo       time.Time  `json:"playTo" db:"play_to"`
	Players      int        `json:"players" db:"players"`
	Notes        *string    `json:"notes" db:"notes"`
	Status       string     `json:"status" db:"status" enum:"requested,approved,rejected,issued,cancelled"`
	ApprovalID   *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	FileURL      *string    `json:"fileUrl" db:"file_url"`
	IssuedAt     *time.Time `json:"issuedAt" db:"issued_at"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
}

const letterSelect = `SELECT l.id, l.number, l.club_id, c.name AS club_name, l.customer_id, cu.name AS customer_name, l.play_from, l.play_to, l.players,
	l.notes, l.status, l.approval_request_id, CASE WHEN l.file_id IS NOT NULL THEN '/api/v1/files/' || l.file_id END AS file_url, l.issued_at, l.created_at
	FROM golf.introduction_letters l JOIN golf.reciprocal_clubs c ON c.id = l.club_id JOIN crm.customers cu ON cu.id = l.customer_id`

func (m *Module) letter(ctx context.Context, q dbtx.Querier, lid uuid.UUID) (Letter, error) {
	rows, err := q.Query(ctx, letterSelect+` WHERE l.id = $1`, lid)
	return handle.One[Letter](rows, err, "introduction letter")
}

// RequestLetter asks for an introduction letter for an active golf member;
// it is issued after approval.
func (m *Module) RequestLetter(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LetterInput) (Letter, error) {
	c, err := loadClub(ctx, tx, property, in.ClubID)
	if err != nil {
		return Letter{}, err
	}
	from, err := time.Parse("2006-01-02", in.PlayFrom)
	if err != nil {
		return Letter{}, handle.Invalid("playFrom", "invalid_date", "playFrom must be YYYY-MM-DD")
	}
	to, err := time.Parse("2006-01-02", in.PlayTo)
	if err != nil || to.Before(from) {
		return Letter{}, handle.Invalid("playTo", "invalid_date", "playTo must be on or after playFrom")
	}
	if !c.activeOn(from) {
		return Letter{}, errs.Conflict("agreement_inactive", "the reciprocal agreement with "+c.Name+" is not active for these dates")
	}
	ok, err := membership.IsActive(ctx, tx, property, in.CustomerID, "golf")
	if err != nil {
		return Letter{}, err
	}
	if !ok {
		return Letter{}, errs.Conflict("not_a_member", "introduction letters are for active golf members")
	}
	if in.Players <= 0 {
		in.Players = 1
	}
	no, err := number(ctx, tx, property, "LTR")
	if err != nil {
		return Letter{}, err
	}
	lid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.introduction_letters (id, property_id, number, club_id, customer_id, play_from, play_to, players, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, lid, property, no, c.ID, in.CustomerID, from, to, in.Players, nullStr(in.Notes), actorPtr(ctx)); err != nil {
		return Letter{}, err
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: IntroductionLetterType.Code, DocumentID: lid, DocumentRef: no,
		Title: "Introduction letter · " + c.Name, PropertyID: property})
	if err != nil {
		return Letter{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.introduction_letters SET approval_request_id = $2 WHERE id = $1`, lid, rid); err != nil {
		return Letter{}, err
	}
	l, err := m.letter(ctx, tx, lid)
	if err != nil {
		return l, err
	}
	return l, record(ctx, tx, "golf.introduction_letter", lid, no, audit.ActionCreate, property, nil, l, "")
}

func (m *Module) letterDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	tag, err := tx.Exec(ctx, `UPDATE golf.introduction_letters SET status = $2 WHERE id = $1 AND status = 'requested'`, d.DocumentID, st)
	if err != nil || tag.RowsAffected() == 0 || st != "approved" {
		return err
	}
	_, err = m.IssueLetter(ctx, tx, d.PropertyID, d.DocumentID)
	return err
}

// IssueLetter renders the letter PDF, records the outbound visit and
// notifies the member.
func (m *Module) IssueLetter(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID) (Letter, error) {
	l, err := m.letter(ctx, tx, lid)
	if err != nil {
		return l, err
	}
	if l.Status != "approved" {
		return l, errs.Conflict("not_approved", "only approved letters can be issued")
	}
	var propName string
	_ = tx.QueryRow(ctx, `SELECT name FROM platform.properties WHERE id = $1`, property).Scan(&propName)
	var fileID *uuid.UUID
	if m.Files != nil && m.Files.Blob != nil {
		doc := newTextDoc()
		doc.Title = "Introduction Letter " + l.Number
		doc.Heading(propName, 16)
		doc.Blank()
		doc.Add("No: %s", l.Number)
		doc.Add("Date: %s", localNow(ctx, tx).Format("2 January 2006"))
		doc.Blank()
		doc.Add("To: The Secretary, %s", l.ClubName)
		doc.Blank()
		doc.Heading("Letter of Introduction", 13)
		doc.Add("We are pleased to introduce %s, a member in good standing of %s, who wishes to play at your club "+
			"between %s and %s (%d player(s)) under our reciprocal agreement.", l.CustomerName, propName,
			l.PlayFrom.Format("2 January 2006"), l.PlayTo.Format("2 January 2006"), l.Players)
		doc.Blank()
		doc.Add("We would be grateful for the courtesy of your club. Charges are settled according to our agreement.")
		doc.Blank()
		doc.Add("Yours sincerely,")
		doc.Blank()
		doc.Add("Membership Office, %s", propName)
		b := doc.Bytes()
		f, err := m.Files.Save(ctx, tx, "introduction-letter-"+l.Number+".pdf", "application/pdf", "attachment", false, bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return l, err
		}
		fileID = &f.ID
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.introduction_letters SET status = 'issued', issued_at = now(), file_id = $2 WHERE id = $1`, lid, fileID); err != nil {
		return l, err
	}
	no, err := number(ctx, tx, property, "RCV")
	if err != nil {
		return l, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.reciprocal_visits (id, property_id, number, direction, club_id, customer_id, visitor_name, letter_ref, letter_id,
		visit_date, verified, created_by) VALUES ($1,$2,$3,'outbound',$4,$5,$6,$7,$8,$9,true,$10)`, id.New(), property, no, l.ClubID, l.CustomerID,
		l.CustomerName, l.Number, lid, l.PlayFrom, actorPtr(ctx)); err != nil {
		return l, err
	}
	if m.Notify != nil {
		msg := notify.Message{Event: "golf.introduction_letter_issued", Category: "membership", PropertyID: &property,
			Data: map[string]any{"name": l.CustomerName, "club": l.ClubName, "from": l.PlayFrom.Format("2006-01-02"), "to": l.PlayTo.Format("2006-01-02")}}
		var user *uuid.UUID
		var email *string
		_ = tx.QueryRow(ctx, `SELECT user_id, email FROM crm.customers WHERE id = $1`, l.CustomerID).Scan(&user, &email)
		switch {
		case user != nil:
			msg.UserIDs = []uuid.UUID{*user}
		case email != nil:
			msg.Email, msg.Channels = *email, []string{notify.ChannelEmail}
		}
		if len(msg.UserIDs) > 0 || msg.Email != "" {
			if err := m.Notify.Send(ctx, tx, msg); err != nil {
				return l, err
			}
		}
	}
	after, err := m.letter(ctx, tx, lid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.introduction_letter", lid, l.Number, "issue", property, l, after, "")
}

type SettlementMarkInput struct {
	VisitIDs []uuid.UUID `json:"visitIds"`
	Status   string      `json:"status" enum:"invoiced,settled"`
}

// MarkSettlement moves visits of a periodic-settlement club (FR-RCP-06).
func (m *Module) MarkSettlement(ctx context.Context, tx pgx.Tx, property uuid.UUID, in SettlementMarkInput) (int, error) {
	if in.Status != "invoiced" && in.Status != "settled" {
		return 0, handle.Invalid("status", "invalid", "invoiced or settled")
	}
	if len(in.VisitIDs) == 0 {
		return 0, handle.Invalid("visitIds", "required", "visits are required")
	}
	tag, err := tx.Exec(ctx, `UPDATE golf.reciprocal_visits SET settlement_status = $3 WHERE property_id = $1 AND id = ANY($2)
		AND settlement_status IN ('open', 'invoiced')`, property, in.VisitIDs, in.Status)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	return n, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "settlement_" + in.Status, EntityType: "golf.reciprocal_visit",
		EntityID: in.VisitIDs[0].String(), EntityLabel: "reciprocal settlement", PropertyID: &property, After: map[string]any{"visits": in.VisitIDs, "updated": n}})
}

// Visits lists reciprocal visits (Reciprocal Visits report, FR-RCP-07).
func (m *Module) Visits(ctx context.Context, q dbtx.Querier, property uuid.UUID, direction, clubID, settlement string, from, to time.Time, limit int) ([]Visit, error) {
	return handle.List[Visit](q.Query(ctx, visitSelect+` WHERE v.property_id = $1 AND ($2 = '' OR v.direction = $2) AND ($3 = '' OR v.club_id::text = $3)
		AND ($4 = '' OR v.settlement_status = $4) AND v.visit_date >= $5 AND v.visit_date < $6 ORDER BY v.visit_date DESC, v.created_at DESC LIMIT $7`,
		property, direction, clubID, settlement, from.Format("2006-01-02"), to.Format("2006-01-02"), limit))
}

// LinkPlayer ties a verified inbound visit to the P1 booking player of the
// reciprocal guest: the player is marked verified with the partner club.
func (m *Module) LinkPlayer(ctx context.Context, tx pgx.Tx, property, vid, player uuid.UUID) (Visit, error) {
	v, err := m.visit(ctx, tx, vid)
	if err != nil {
		return v, err
	}
	if v.Direction != "inbound" || !v.Verified {
		return v, errs.Conflict("not_verified", "only verified inbound visits can be linked to a player")
	}
	var ptype string
	if err := tx.QueryRow(ctx, `SELECT player_type FROM golf.booking_players WHERE id = $1 AND property_id = $2 FOR UPDATE`, player, property).Scan(&ptype); err != nil {
		if dbtx.IsNoRows(err) {
			return v, errs.NotFound("player")
		}
		return v, err
	}
	if ptype != "reciprocal" {
		return v, errs.Conflict("not_reciprocal", "the player is not a reciprocal player")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET reciprocal_visit_id = $2, reciprocal_club = $3, reciprocal_verified = true, updated_by = $4
		WHERE id = $1`, player, vid, v.ClubName, actorPtr(ctx)); err != nil {
		return v, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.reciprocal_visits SET booking_player_id = $2, customer_id = coalesce(customer_id,
		(SELECT customer_id FROM golf.booking_players WHERE id = $2)) WHERE id = $1`, vid, player); err != nil {
		return v, err
	}
	after, err := m.visit(ctx, tx, vid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.reciprocal_visit", vid, v.Number, "link_player", property, v, after, "")
}
