package golf

// Hole-in-One (PRD P2 EP-10) and Hall of Fame (EP-11).

import (
	"bytes"
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/pdf"
	"oneclub/internal/platform/storage"
)

type Witness struct {
	Name      string `json:"name"`
	Statement string `json:"statement,omitempty"`
}

// HIO is a Hole-in-One record.
type HIO struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	HIONo           string     `json:"hioNo" db:"hio_no"`
	ScorecardID     *uuid.UUID `json:"scorecardId" db:"scorecard_id"`
	FlightID        *uuid.UUID `json:"flightId" db:"flight_id"`
	PlayerID        *uuid.UUID `json:"playerId" db:"player_id"`
	CustomerID      *uuid.UUID `json:"customerId" db:"customer_id"`
	PlayerName      string     `json:"playerName" db:"player_name"`
	HoleID          uuid.UUID  `json:"holeId" db:"hole_id"`
	HoleLabel       string     `json:"hole" db:"hole_label"`
	TeeSetID        *uuid.UUID `json:"teeSetId" db:"tee_set_id"`
	CaddyID         *uuid.UUID `json:"caddyId" db:"caddy_id"`
	AchievedOn      time.Time  `json:"achievedOn" db:"achieved_on"`
	Witnesses       []Witness  `json:"witnesses" db:"witnesses"`
	Attachments     []string   `json:"attachments" db:"attachments"`
	Insured         bool       `json:"insured" db:"insured"`
	PolicyRef       *string    `json:"policyRef" db:"policy_ref"`
	Status          string     `json:"status" db:"status" enum:"draft,pending_verification,verified,claimed,completed,rejected"`
	ApprovalID      *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	VerifiedAt      *time.Time `json:"verifiedAt" db:"verified_at"`
	ClaimSubmitted  *time.Time `json:"claimSubmittedOn" db:"claim_submitted_on"`
	ClaimProvider   *string    `json:"claimProviderRef" db:"claim_provider_ref"`
	ClaimDocuments  []string   `json:"claimDocuments" db:"claim_documents"`
	ClaimStatus     *string    `json:"claimStatus" db:"claim_status" enum:"submitted,in_review,paid,rejected"`
	ClaimPaidAmount *string    `json:"claimPaidAmount" db:"claim_paid_amount"`
	Notes           *string    `json:"notes" db:"notes"`
	CreatedAt       time.Time  `json:"createdAt" db:"created_at"`
}

const hioSelect = `SELECT r.id, r.hio_no, r.scorecard_id, r.flight_id, r.player_id, r.customer_id, r.player_name, r.hole_id,
	s.code || '-' || h.number AS hole_label, r.tee_set_id, r.caddy_id, r.achieved_on, r.witnesses, r.attachments, r.insured, r.policy_ref, r.status,
	r.approval_request_id, r.verified_at, r.claim_submitted_on, r.claim_provider_ref, r.claim_documents, r.claim_status,
	trim_scale(r.claim_paid_amount)::text AS claim_paid_amount, r.notes, r.created_at
	FROM golf.hio_records r JOIN golf.holes h ON h.id = r.hole_id JOIN golf.course_sections s ON s.id = h.section_id`

func (m *Module) hio(ctx context.Context, q dbtx.Querier, hid uuid.UUID) (HIO, error) {
	rows, err := q.Query(ctx, hioSelect+` WHERE r.id = $1`, hid)
	return handle.One[HIO](rows, err, "hole-in-one record")
}

// draftHIO creates the Draft record when a finalized card has a 1 (FR-SCR-06).
func (m *Module) draftHIO(ctx context.Context, tx pgx.Tx, sc Scorecard, h ScoreHole) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.hio_records WHERE scorecard_id = $1 AND hole_id = $2)`, sc.ID, h.HoleID).Scan(&exists); err != nil || exists {
		return err
	}
	var insured bool
	var policy, caddy *string
	if sc.PlayerID != nil {
		_ = tx.QueryRow(ctx, `SELECT p.hio_insured, p.hio_policy_ref, (SELECT a.caddy_id::text FROM golf.caddy_assignments a WHERE a.flight_id = p.flight_id
			AND (a.player_id IS NULL OR a.player_id = p.id) AND a.from_seq <= $2 AND coalesce(a.to_seq, 99) >= $2 AND a.status IN ('completed', 'replaced', 'active')
			LIMIT 1) FROM golf.flight_players p WHERE p.id = $1`, *sc.PlayerID, h.Seq).Scan(&insured, &policy, &caddy)
	}
	no, err := numbering.Next(ctx, tx, sc.PropertyID, "HIO", localNow(ctx, tx))
	if err != nil {
		return err
	}
	hid := id.New()
	var caddyID *uuid.UUID
	if caddy != nil {
		c, _ := uuid.Parse(*caddy)
		caddyID = &c
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.hio_records (id, property_id, hio_no, scorecard_id, flight_id, player_id, customer_id, player_name, hole_id,
		tee_set_id, caddy_id, achieved_on, insured, policy_ref, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, hid, sc.PropertyID, no,
		sc.ID, sc.FlightID, sc.PlayerID, sc.CustomerID, sc.PlayerName, h.HoleID, sc.TeeSetID, caddyID, sc.PlayedOn, insured, policy, actor(ctx)); err != nil {
		return err
	}
	return m.publish(ctx, tx, "golf.hio_recorded", "golf.hio_record", hid, sc.PropertyID, map[string]any{"hioId": hid, "customerId": sc.CustomerID})
}

type HIOInput struct {
	CustomerID  *uuid.UUID `json:"customerId,omitempty"`
	PlayerName  string     `json:"playerName,omitempty"`
	HoleID      uuid.UUID  `json:"holeId"`
	TeeSetID    *uuid.UUID `json:"teeSetId,omitempty"`
	CaddyID     *uuid.UUID `json:"caddyId,omitempty"`
	FlightID    *uuid.UUID `json:"flightId,omitempty"`
	AchievedOn  string     `json:"achievedOn" doc:"YYYY-MM-DD"`
	Witnesses   []Witness  `json:"witnesses,omitempty"`
	Attachments []string   `json:"attachments,omitempty"`
	Insured     bool       `json:"insured,omitempty"`
	PolicyRef   string     `json:"policyRef,omitempty"`
	Notes       string     `json:"notes,omitempty"`
}

// CreateHIO records a Hole-in-One manually (paper card, migration).
func (m *Module) CreateHIO(ctx context.Context, tx pgx.Tx, property uuid.UUID, in HIOInput) (HIO, error) {
	d, err := time.Parse("2006-01-02", in.AchievedOn)
	if err != nil {
		return HIO{}, handle.Invalid("achievedOn", "invalid_date", "achievedOn must be YYYY-MM-DD")
	}
	name := in.PlayerName
	if in.CustomerID != nil {
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.customers WHERE id = $1`, *in.CustomerID).Scan(&name); err != nil {
			if dbtx.IsNoRows(err) {
				return HIO{}, errs.NotFound("customer")
			}
			return HIO{}, err
		}
	}
	if err := handle.Required("playerName", name); err != nil {
		return HIO{}, err
	}
	no, err := numbering.Next(ctx, tx, property, "HIO", localNow(ctx, tx))
	if err != nil {
		return HIO{}, err
	}
	if in.Witnesses == nil {
		in.Witnesses = []Witness{}
	}
	if in.Attachments == nil {
		in.Attachments = []string{}
	}
	hid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.hio_records (id, property_id, hio_no, flight_id, customer_id, player_name, hole_id, tee_set_id, caddy_id,
		achieved_on, witnesses, attachments, insured, policy_ref, notes, created_by)
		SELECT $1,$2,$3,$4,$5,$6,h.id,$8,$9,$10,$11,$12,$13,$14,$15,$16 FROM golf.holes h WHERE h.id = $7 AND h.property_id = $2`,
		hid, property, no, in.FlightID, in.CustomerID, name, in.HoleID, in.TeeSetID, in.CaddyID, d, jsonOf(in.Witnesses), in.Attachments, in.Insured,
		nzs(in.PolicyRef), nzs(in.Notes), actor(ctx)); err != nil {
		return HIO{}, err
	}
	r, err := m.hio(ctx, tx, hid)
	if err != nil {
		return r, err
	}
	return r, record(ctx, tx, "golf.hio_record", hid, no, audit.ActionCreate, property, nil, r, "")
}

type HIOVerifyInput struct {
	Witnesses   []Witness `json:"witnesses"`
	Attachments []string  `json:"attachments,omitempty"`
	Notes       string    `json:"notes,omitempty"`
}

// SubmitHIO sends the record with witness statements for verification by
// the Golf Manager (approval, FR-HIO-03).
func (m *Module) SubmitHIO(ctx context.Context, tx pgx.Tx, property, hid uuid.UUID, in HIOVerifyInput) (HIO, error) {
	r, err := m.hio(ctx, tx, hid)
	if err != nil {
		return r, err
	}
	if r.Status != "draft" {
		return r, errs.Conflict("invalid_status", "record is "+r.Status)
	}
	if len(in.Witnesses) == 0 {
		return r, handle.Invalid("witnesses", "required", "at least one witness statement is required")
	}
	if in.Attachments == nil {
		in.Attachments = r.Attachments
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hio_records SET witnesses = $2, attachments = $3, notes = coalesce($4, notes), status = 'pending_verification',
		updated_by = $5 WHERE id = $1`, hid, jsonOf(in.Witnesses), in.Attachments, nzs(in.Notes), actor(ctx)); err != nil {
		return r, err
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: HIOType.Code, DocumentID: hid, DocumentRef: r.HIONo,
		Title: "Hole-in-One verification · " + r.PlayerName + " · " + r.HoleLabel, PropertyID: property})
	if err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hio_records SET approval_request_id = $2 WHERE id = $1`, hid, rid); err != nil {
		return r, err
	}
	after, err := m.hio(ctx, tx, hid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.hio_record", hid, r.HIONo, "submit", property, r, after, "")
}

func (m *Module) hioDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "verified", approval.StatusRejected: "rejected", approval.StatusCancelled: "draft"}[d.Status]
	tag, err := tx.Exec(ctx, `UPDATE golf.hio_records SET status = $2, verified_at = CASE WHEN $2 = 'verified' THEN now() END
		WHERE id = $1 AND status = 'pending_verification'`, d.DocumentID, st)
	if err != nil || tag.RowsAffected() == 0 || st != "verified" {
		return err
	}
	r, err := m.hio(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	// Automatic Hall of Fame entry (FR-HIO-05), subject to the player's opt-in.
	sc := Scorecard{PropertyID: d.PropertyID, CustomerID: r.CustomerID, PlayerName: r.PlayerName, PlayedOn: r.AchievedOn}
	if r.TeeSetID != nil {
		sc.TeeSetID = *r.TeeSetID
	}
	one := 1
	if err := m.autoEntry(ctx, tx, sc, "hole_in_one", "Hole-in-One · hole "+r.HoleLabel, &r.HoleID, &one, "golf.hio_record", r.ID, false); err != nil {
		return err
	}
	return m.publish(ctx, tx, "golf.hio_verified", "golf.hio_record", r.ID, d.PropertyID, map[string]any{"hioId": r.ID, "customerId": r.CustomerID})
}

type ClaimInput struct {
	SubmittedOn string   `json:"submittedOn" doc:"YYYY-MM-DD"`
	ProviderRef string   `json:"providerRef,omitempty"`
	Documents   []string `json:"documents,omitempty"`
}

// ClaimHIO starts the insurance claim and generates the claim package PDF
// (FR-HIO-04, provider OQ #9).
func (m *Module) ClaimHIO(ctx context.Context, tx pgx.Tx, property, hid uuid.UUID, in ClaimInput) (HIO, error) {
	r, err := m.hio(ctx, tx, hid)
	if err != nil {
		return r, err
	}
	if r.Status != "verified" {
		return r, errs.Conflict("not_verified", "only a verified Hole-in-One can be claimed")
	}
	if !r.Insured {
		return r, errs.Conflict("not_insured", "the player had no HIO insurance for this round")
	}
	d, err := time.Parse("2006-01-02", in.SubmittedOn)
	if err != nil {
		return r, handle.Invalid("submittedOn", "invalid_date", "submittedOn must be YYYY-MM-DD")
	}
	docs := in.Documents
	if docs == nil {
		docs = []string{}
	}
	if m.Files != nil && m.Files.Blob != nil {
		var doc pdf.Doc
		doc.Title = "Hole-in-One Claim " + r.HIONo
		doc.Heading("Hole-in-One Insurance Claim", 16)
		doc.Blank()
		doc.Add("Record: %s", r.HIONo)
		doc.Add("Player: %s", r.PlayerName)
		doc.Add("Hole: %s", r.HoleLabel)
		doc.Add("Date: %s", r.AchievedOn.Format("2 January 2006"))
		doc.Add("Policy reference: %s", deref(r.PolicyRef))
		doc.Add("Provider reference: %s", in.ProviderRef)
		doc.Blank()
		doc.Heading("Witness statements", 12)
		for _, w := range r.Witnesses {
			doc.Add("- %s: %s", w.Name, w.Statement)
		}
		doc.Blank()
		doc.Add("Verified: %s", r.VerifiedAt.Format("2 January 2006 15:04 MST"))
		b := doc.Bytes()
		f, err := m.Files.Save(ctx, tx, "hio-claim-"+r.HIONo+".pdf", "application/pdf", "attachment", false, bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return r, err
		}
		docs = append(docs, storage.URLFor(f.ID))
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hio_records SET status = 'claimed', claim_submitted_on = $2, claim_provider_ref = $3, claim_documents = $4,
		claim_status = 'submitted', updated_by = $5 WHERE id = $1`, hid, d, nzs(in.ProviderRef), docs, actor(ctx)); err != nil {
		return r, err
	}
	after, err := m.hio(ctx, tx, hid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.hio_record", hid, r.HIONo, "claim", property, r, after, "")
}

type ClaimUpdateInput struct {
	ClaimStatus string `json:"claimStatus" enum:"in_review,paid,rejected"`
	PaidAmount  string `json:"paidAmount,omitempty"`
	ProviderRef string `json:"providerRef,omitempty"`
}

// UpdateClaim tracks the claim; paid completes the record.
func (m *Module) UpdateClaim(ctx context.Context, tx pgx.Tx, property, hid uuid.UUID, in ClaimUpdateInput) (HIO, error) {
	r, err := m.hio(ctx, tx, hid)
	if err != nil {
		return r, err
	}
	if r.Status != "claimed" {
		return r, errs.Conflict("not_claimed", "the record has no open claim")
	}
	amt, err := handle.Decimal("paidAmount", in.PaidAmount, decimal.Zero)
	if err != nil {
		return r, err
	}
	status := r.Status
	switch in.ClaimStatus {
	case "paid":
		if !amt.IsPositive() {
			return r, handle.Invalid("paidAmount", "required", "paid amount is required")
		}
		status = "completed"
	case "rejected":
		status = "completed"
	case "in_review":
	default:
		return r, handle.Invalid("claimStatus", "invalid", "in_review, paid or rejected")
	}
	var paid *string
	if amt.IsPositive() {
		s := amt.String()
		paid = &s
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hio_records SET claim_status = $2, claim_paid_amount = coalesce($3::numeric, claim_paid_amount),
		claim_provider_ref = coalesce($4, claim_provider_ref), status = $5, updated_by = $6 WHERE id = $1`, hid, in.ClaimStatus, paid, nzs(in.ProviderRef), status, actor(ctx)); err != nil {
		return r, err
	}
	after, err := m.hio(ctx, tx, hid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.hio_record", hid, r.HIONo, audit.ActionStatusChange, property, r, after, "")
}

// ── Hall of Fame (EP-11) ──────────────────────────────────────────────────

// autoEntry creates an automatic Hall of Fame entry. It is public only with
// the player's consent and — unless the policy auto-publishes — after a
// Golf Admin publishes it (FR-HOF-02/04/06).
func (m *Module) autoEntry(ctx context.Context, tx pgx.Tx, sc Scorecard, category, title string, hole *uuid.UUID, score *int, srcType string, src uuid.UUID,
	verify bool) error {
	pol, err := m.hofPolicy(ctx, tx, sc.PropertyID)
	if err != nil {
		return err
	}
	var tee *uuid.UUID
	if sc.TeeSetID != uuid.Nil {
		tee = &sc.TeeSetID
	}
	year := sc.PlayedOn.Year()
	_, err = tx.Exec(ctx, `INSERT INTO golf.hall_of_fame (id, property_id, category, title, year, customer_id, player_name, tee_set_id, hole_id, score, achieved_on,
		source_type, source_id, needs_verification, published, published_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15, CASE WHEN $15 THEN now() END,$16)
		ON CONFLICT (source_type, source_id, category) WHERE source_id IS NOT NULL DO UPDATE SET score = EXCLUDED.score`,
		id.New(), sc.PropertyID, category, title, year, sc.CustomerID, sc.PlayerName, tee, hole, score, sc.PlayedOn, srcType, src, verify,
		pol.AutoPublish && !verify, actor(ctx))
	return err
}

type ConsentInput struct {
	Consent string `json:"consent" enum:"granted,withdrawn"`
}

// SetEntryConsent records the player's opt-in for one entry (revocable).
func (m *Module) SetEntryConsent(ctx context.Context, tx pgx.Tx, property, eid uuid.UUID, consent string, customer *uuid.UUID) error {
	if consent != "granted" && consent != "withdrawn" {
		return handle.Invalid("consent", "invalid", "granted or withdrawn")
	}
	var owner *uuid.UUID
	var title, before string
	if err := tx.QueryRow(ctx, `SELECT customer_id, title, consent FROM golf.hall_of_fame WHERE id = $1 AND property_id = $2 FOR UPDATE`, eid, property).
		Scan(&owner, &title, &before); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("hall of fame entry")
		}
		return err
	}
	if customer != nil && (owner == nil || *owner != *customer) {
		return errs.Forbidden("only the player can give consent for this entry")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hall_of_fame SET consent = $2, consent_at = now() WHERE id = $1`, eid, consent); err != nil {
		return err
	}
	return record(ctx, tx, "golf.hall_of_fame", eid, title, "consent", property, map[string]any{"consent": before}, map[string]any{"consent": consent}, "")
}

// PublishEntry curates an entry for public display (FR-HOF-06).
func (m *Module) PublishEntry(ctx context.Context, tx pgx.Tx, property, eid uuid.UUID, publish bool) error {
	var title string
	var verify bool
	if err := tx.QueryRow(ctx, `SELECT title, needs_verification FROM golf.hall_of_fame WHERE id = $1 AND property_id = $2 FOR UPDATE`, eid, property).
		Scan(&title, &verify); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("hall of fame entry")
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hall_of_fame SET published = $2, published_at = CASE WHEN $2 THEN now() END, needs_verification = false
		WHERE id = $1`, eid, publish); err != nil {
		return err
	}
	act := "publish"
	if !publish {
		act = "unpublish"
	}
	return record(ctx, tx, "golf.hall_of_fame", eid, title, act, property, nil, map[string]any{"published": publish, "verified": verify}, "")
}

// PublicEntry is a Hall of Fame entry shown on the website, Member App and
// Clubhouse Screen: published, active and consented (or consent not needed).
type PublicEntry struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Category   string     `json:"category" db:"category"`
	Title      string     `json:"title" db:"title"`
	Year       *int       `json:"year" db:"year"`
	Division   *string    `json:"division" db:"division"`
	PlayerName *string    `json:"playerName" db:"player_name"`
	TeeSet     *string    `json:"teeSet" db:"tee_set"`
	Score      *int       `json:"score" db:"score"`
	AchievedOn *time.Time `json:"achievedOn" db:"achieved_on"`
	PhotoURL   *string    `json:"photoUrl" db:"photo_url"`
	Desc       *string    `json:"description" db:"description"`
}

// PublicFilter is the condition for public display (FR-HOF-04).
const PublicFilter = `e.published AND e.status = 'active' AND e.archived_at IS NULL AND e.consent IN ('granted', 'not_required')`

func (m *Module) PublicEntries(ctx context.Context, q dbtx.Querier, property uuid.UUID, category string) ([]PublicEntry, error) {
	return handle.List[PublicEntry](q.Query(ctx, `SELECT e.id, e.category, e.title, e.year, e.division, e.player_name, t.name AS tee_set, e.score, e.achieved_on,
		e.photo_url, e.description FROM golf.hall_of_fame e LEFT JOIN golf.tee_sets t ON t.id = e.tee_set_id
		WHERE e.property_id = $1 AND `+PublicFilter+` AND ($2 = '' OR e.category = $2)
		ORDER BY e.achieved_on DESC NULLS LAST, e.year DESC NULLS LAST`, property, category))
}
