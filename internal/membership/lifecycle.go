package membership

// Membership lifecycle (roadmap §10):
//
//	Application → Approval → Membership Fee → Activation → Membership Card → Active Membership

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/docno"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/iam"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
)

// Domain events.
const (
	EventApplicationApproved = "membership.application_approved"
	EventActivated           = "membership.activated"
	EventRenewed             = "membership.renewed"
)

// ApplicationDocumentType is the approval document type (FR-MEM-04).
var ApplicationDocumentType = provision.DocumentType{Code: "membership_application", Module: "membership", Name: "Membership Application",
	Attributes: []provision.DocumentAttribute{{Key: "fee", Label: "Membership Fee", Type: "number"}, {Key: "category", Label: "Category", Type: "string"}}}

// Publisher publishes domain events.
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Portal provisions Member Portal accounts (iam.Service).
type Portal interface {
	EnsurePortalUser(ctx context.Context, tx pgx.Tx, u iam.PortalUser) (uuid.UUID, bool, error)
	SendActivation(ctx context.Context, tx pgx.Tx, uid uuid.UUID, name string) error
}

// Residents looks up Modernland resident data (FR-INT-P1-05).
type Residents interface {
	Resolve(ctx context.Context, capability string) (any, string, error)
}

// Module is the membership module.
type Module struct {
	DB        *dbtx.DB
	Events    Publisher
	Approvals *approval.Engine
	Billing   *billing.Service
	Notify    notify.Sender
	Portal    Portal
	Residents Residents
	PortalURL func() string
}

func actor(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

func (m *Module) portalURL() string {
	if m.PortalURL != nil {
		return m.PortalURL()
	}
	return ""
}

func today(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	n := clock.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func history(ctx context.Context, tx pgx.Tx, property, memberID uuid.UUID, membershipID *uuid.UUID, event, from, to string, details any) error {
	raw, _ := json.Marshal(details)
	if details == nil {
		raw = []byte("{}")
	}
	_, err := tx.Exec(ctx, `INSERT INTO membership.history (id, property_id, member_id, membership_id, event, from_status, to_status, details, actor_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, memberID, membershipID, event, nullStr(from), nullStr(to), raw, id.Ptr(actor(ctx)))
	return err
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// ── applications ──────────────────────────────────────────────────────────

// Application is the API view of a membership application.
type Application struct {
	ID                 uuid.UUID          `json:"id"`
	Number             string             `json:"number"`
	Channel            string             `json:"channel" enum:"back_office,member_portal"`
	CustomerID         uuid.UUID          `json:"customerId"`
	CustomerName       string             `json:"customerName"`
	TypeID             uuid.UUID          `json:"typeId"`
	TypeName           string             `json:"typeName"`
	PackageID          uuid.UUID          `json:"packageId"`
	PackageName        string             `json:"packageName"`
	CorporateAccountID *uuid.UUID         `json:"corporateAccountId"`
	Dependents         []Dependent        `json:"dependents"`
	Documents          []Document         `json:"documents"`
	Eligibility        *EligibilityResult `json:"eligibility"`
	Status             string             `json:"status" enum:"draft,pending,approved,rejected,cancelled,completed"`
	ApprovalRequestID  *uuid.UUID         `json:"approvalRequestId"`
	FeeFolioID         *uuid.UUID         `json:"feeFolioId"`
	Fee                string             `json:"fee"`
	MembershipID       *uuid.UUID         `json:"membershipId"`
	Notes              *string            `json:"notes"`
	SubmittedAt        *time.Time         `json:"submittedAt"`
	DecidedAt          *time.Time         `json:"decidedAt"`
	DecisionReason     *string            `json:"decisionReason"`
	CreatedAt          time.Time          `json:"createdAt"`
}

// Document is an uploaded supporting document.
type Document struct {
	FileID   *uuid.UUID `json:"fileId,omitempty"`
	Kind     string     `json:"kind" enum:"identity,family_card,resident_proof,student_card,company_letter,other"`
	Name     string     `json:"name,omitempty"`
	Verified bool       `json:"verified"`
}

func scanAppExtra(a *Application, deps, docs, elig []byte, err error) error {
	if err != nil {
		return err
	}
	a.Dependents, a.Documents = []Dependent{}, []Document{}
	_ = json.Unmarshal(deps, &a.Dependents)
	_ = json.Unmarshal(docs, &a.Documents)
	if len(elig) > 0 && string(elig) != "null" {
		var e EligibilityResult
		if json.Unmarshal(elig, &e) == nil {
			a.Eligibility = &e
		}
	}
	return nil
}

// GetApplication loads an application.
func GetApplication(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (Application, error) {
	var a Application
	var deps, docs, elig []byte
	err := q.QueryRow(ctx, `SELECT a.id, a.number, a.channel, a.customer_id, a.type_id, t.name, a.package_id, p.name, a.corporate_account_id, a.dependents,
		a.documents, a.eligibility, a.status, a.approval_request_id, a.fee_folio_id, (p.joining_fee + p.period_fee)::text, a.membership_id, a.notes,
		a.submitted_at, a.decided_at, a.decision_reason, a.created_at
		FROM membership.applications a JOIN membership.types t ON t.id = a.type_id JOIN membership.packages p ON p.id = a.package_id WHERE a.id = $1`, aid).
		Scan(&a.ID, &a.Number, &a.Channel, &a.CustomerID, &a.TypeID, &a.TypeName, &a.PackageID, &a.PackageName, &a.CorporateAccountID, &deps, &docs,
			&elig, &a.Status, &a.ApprovalRequestID, &a.FeeFolioID, &a.Fee, &a.MembershipID, &a.Notes, &a.SubmittedAt, &a.DecidedAt, &a.DecisionReason, &a.CreatedAt)
	if dbtx.IsNoRows(err) {
		return a, errs.NotFound("membership application")
	}
	if err := scanAppExtra(&a, deps, docs, elig, err); err != nil {
		return a, err
	}
	names, err := crm.Names(ctx, q, []uuid.UUID{a.CustomerID})
	if err != nil {
		return a, err
	}
	a.CustomerName = names[a.CustomerID]
	return a, nil
}

type typeRow struct {
	ID          uuid.UUID
	Code, Name  string
	Category    string
	MaxFamily   int
	MaxNominees int
	Eligibility Eligibility
}

func loadType(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (typeRow, error) {
	var t typeRow
	var raw []byte
	err := q.QueryRow(ctx, `SELECT id, code, name, category, max_family_members, max_nominees, eligibility FROM membership.types WHERE id = $1 AND status = 'active'`, tid).
		Scan(&t.ID, &t.Code, &t.Name, &t.Category, &t.MaxFamily, &t.MaxNominees, &raw)
	if dbtx.IsNoRows(err) {
		return t, errs.Validation("invalid_type", "membership type not found or inactive", errs.Field("typeId", "not_found", "membership type not found"))
	}
	_ = json.Unmarshal(raw, &t.Eligibility)
	return t, err
}

// CheckEligibility evaluates and stores the eligibility of an application.
func (m *Module) CheckEligibility(ctx context.Context, tx pgx.Tx, a Application) (EligibilityResult, error) {
	t, err := loadType(ctx, tx, a.TypeID)
	if err != nil {
		return EligibilityResult{}, err
	}
	property := propOf(ctx)
	applicant, err := crm.GetCustomer(ctx, tx, a.CustomerID)
	if err != nil {
		return EligibilityResult{}, err
	}
	deps := map[uuid.UUID]crm.Customer{}
	family, nominees := 0, 0
	for _, d := range a.Dependents {
		c, err := crm.GetCustomer(ctx, tx, d.CustomerID)
		if err != nil {
			return EligibilityResult{}, errs.Validation("invalid_dependent", "dependent not found", errs.Field("dependents", "not_found", "customer not found"))
		}
		deps[d.CustomerID] = c
		if d.Relationship == "other" && a.CorporateAccountID != nil {
			nominees++
		} else {
			family++
		}
	}
	student, resident := false, false
	for _, d := range a.Documents {
		if d.Kind == "student_card" && d.Verified {
			student = true
		}
		if d.Kind == "resident_proof" && d.Verified {
			resident = true
		}
	}
	// Resident data integration (FR-INT-P1-05): automatic when available.
	if t.Eligibility.ResidentOnly && !resident && !applicant.Resident && m.Residents != nil {
		if a, _, err := m.Residents.Resolve(ctx, integration.CapResidentData); err == nil {
			if rl, ok := a.(integration.ResidentDataAdapter); ok {
				if list, err := rl.LookupResident(ctx, applicant.Name); err == nil {
					for _, r := range list {
						if strings.EqualFold(r.Status, "active") && strings.EqualFold(r.Name, applicant.Name) {
							resident = true
						}
					}
				}
			}
		}
	}
	res := Evaluate(t.Eligibility, applicant, a.Dependents, deps, student, resident, today(ctx, tx, property))
	if t.Category == "family" && family > t.MaxFamily && t.MaxFamily > 0 {
		res.Eligible = false
		res.Checks = append(res.Checks, Check{Rule: "max_family_members", Subject: applicant.Name, Passed: false, Message: "too many family members"})
	}
	if t.Category != "family" && family > 0 {
		res.Eligible = false
		res.Checks = append(res.Checks, Check{Rule: "family_not_allowed", Subject: applicant.Name, Passed: false, Message: "this type has no family members"})
	}
	if t.Category == "corporate" {
		if a.CorporateAccountID == nil {
			res.Eligible = false
			res.Checks = append(res.Checks, Check{Rule: "corporate_account", Subject: applicant.Name, Passed: false, Message: "a corporate account is required"})
		} else {
			for _, d := range a.Dependents {
				ok, err := crm.IsNominee(ctx, tx, *a.CorporateAccountID, d.CustomerID)
				if err != nil {
					return res, err
				}
				res.Checks = append(res.Checks, Check{Rule: "corporate_nominee", Subject: deps[d.CustomerID].Name, Passed: ok, Message: "must be a nominee of the corporate account"})
				res.Eligible = res.Eligible && ok
			}
			if t.MaxNominees > 0 && nominees > t.MaxNominees {
				res.Eligible = false
				res.Checks = append(res.Checks, Check{Rule: "max_nominees", Subject: applicant.Name, Passed: false, Message: "too many nominees"})
			}
		}
	}
	raw, _ := json.Marshal(res)
	_, err = tx.Exec(ctx, `UPDATE membership.applications SET eligibility = $2, updated_by = $3 WHERE id = $1`, a.ID, raw, id.Ptr(actor(ctx)))
	return res, err
}

// Submit sends a draft application to approval (FR-MEM-03/04).
func (m *Module) Submit(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (Application, error) {
	a, err := GetApplication(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if a.Status != "draft" {
		return a, errs.Conflict("application_not_draft", "only draft applications can be submitted")
	}
	res, err := m.CheckEligibility(ctx, tx, a)
	if err != nil {
		return a, err
	}
	if !res.Eligible {
		var failed []string
		for _, c := range res.Checks {
			if !c.Passed {
				failed = append(failed, c.Subject+": "+c.Message)
			}
		}
		e := errs.Validation("not_eligible", "eligibility check failed: "+strings.Join(failed, "; "))
		e.Fields = []errs.FieldError{errs.Field("eligibility", "failed", strings.Join(failed, "; "))}
		return a, e
	}
	property := propOf(ctx)
	if _, err := tx.Exec(ctx, `UPDATE membership.applications SET status = 'pending', submitted_at = now(), updated_by = $2 WHERE id = $1`, aid, id.Ptr(actor(ctx))); err != nil {
		return a, err
	}
	t, err := loadType(ctx, tx, a.TypeID)
	if err != nil {
		return a, err
	}
	fee, _ := decimal.NewFromString(a.Fee)
	f, _ := fee.Float64()
	reqID, status, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: ApplicationDocumentType.Code, DocumentID: aid, DocumentRef: a.Number,
		Title: a.TypeName + " — " + a.CustomerName, PropertyID: property, Attributes: map[string]any{"fee": f, "category": t.Category}})
	if err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.applications SET approval_request_id = $2 WHERE id = $1`, aid, reqID); err != nil {
		return a, err
	}
	c, _ := crm.GetCustomer(ctx, tx, a.CustomerID)
	if err := m.notifyCustomer(ctx, tx, c, "membership.application_submitted", map[string]any{"name": c.Name, "number": a.Number, "type": a.TypeName}); err != nil {
		return a, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "submit", EntityType: "membership.application", EntityID: aid.String(),
		EntityLabel: a.Number, PropertyID: &property, After: map[string]any{"status": "pending", "approval": status}}); err != nil {
		return a, err
	}
	return GetApplication(ctx, tx, aid)
}

func (m *Module) notifyCustomer(ctx context.Context, tx pgx.Tx, c crm.Customer, event string, data map[string]any) error {
	if m.Notify == nil {
		return nil
	}
	msg := notify.Message{Event: event, Category: "general", Data: data, Link: m.portalURL() + "/membership"}
	switch {
	case c.UserID != nil:
		msg.UserIDs = []uuid.UUID{*c.UserID}
		msg.Channels = []string{notify.ChannelInApp, notify.ChannelEmail, notify.ChannelWhatsApp}
	case c.Email != "" || c.Phone != "":
		msg.Email, msg.Phone, msg.Name = c.Email, c.Phone, c.Name
		msg.Channels = nil
		if c.Email != "" {
			msg.Channels = append(msg.Channels, notify.ChannelEmail)
		}
		if c.Phone != "" {
			msg.Channels = append(msg.Channels, notify.ChannelWhatsApp)
		}
	default:
		return nil
	}
	return m.Notify.Send(ctx, tx, msg)
}

func propOf(ctx context.Context) uuid.UUID {
	p, _ := propertyFrom(ctx)
	return p
}

// ApplicationDecision is the approval hook (FR-MEM-04): approved → fee
// folio (activation follows payment); rejected → applicant informed.
func (m *Module) ApplicationDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	ctx = withProperty(ctx, d.PropertyID)
	a, err := GetApplication(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	c, _ := crm.GetCustomer(ctx, tx, a.CustomerID)
	switch d.Status {
	case approval.StatusApproved:
		if _, err := tx.Exec(ctx, `UPDATE membership.applications SET status = 'approved', decided_at = now(), decision_reason = $2 WHERE id = $1`,
			a.ID, nullStr(d.Reason)); err != nil {
			return err
		}
		fee, _ := decimal.NewFromString(a.Fee)
		if _, err := m.Events.Publish(ctx, tx, EventApplicationApproved, "membership.application", &a.ID, &d.PropertyID,
			map[string]any{"applicationId": a.ID, "number": a.Number, "fee": fee.String()}); err != nil {
			return err
		}
		if fee.IsPositive() {
			folio, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: d.PropertyID, CustomerID: &a.CustomerID, HolderName: c.Name,
				SourceType: "membership_fee", SourceID: &a.ID, SourceRef: a.Number})
			if err != nil {
				return err
			}
			var jf, pf string
			if err := tx.QueryRow(ctx, `SELECT joining_fee::text, period_fee::text FROM membership.packages WHERE id = $1`, a.PackageID).Scan(&jf, &pf); err != nil {
				return err
			}
			for _, x := range []struct{ desc, amt string }{{"Joining fee — " + a.TypeName, jf}, {"Membership fee — " + a.PackageName, pf}} {
				amt, _ := decimal.NewFromString(x.amt)
				if !amt.IsPositive() {
					continue
				}
				if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: folio.ID, ChargeType: "membership_fee", Description: x.desc,
					UnitPrice: amt, Net: amt, Total: amt, ReferenceType: "membership_application", ReferenceID: &a.ID}); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE membership.applications SET fee_folio_id = $2 WHERE id = $1`, a.ID, folio.ID); err != nil {
				return err
			}
			return m.notifyCustomer(ctx, tx, c, "membership.application_approved", map[string]any{"name": c.Name, "number": a.Number, "type": a.TypeName,
				"fee": "IDR " + fee.StringFixed(0)})
		}
		_, err := m.Activate(ctx, tx, a.ID, false, "")
		return err
	case approval.StatusRejected, approval.StatusCancelled:
		st := "rejected"
		if d.Status == approval.StatusCancelled {
			st = "cancelled"
		}
		if _, err := tx.Exec(ctx, `UPDATE membership.applications SET status = $2, decided_at = now(), decision_reason = $3 WHERE id = $1`,
			a.ID, st, nullStr(d.Reason)); err != nil {
			return err
		}
		if st == "rejected" {
			return m.notifyCustomer(ctx, tx, c, "membership.application_rejected", map[string]any{"name": c.Name, "number": a.Number, "reason": d.Reason})
		}
	}
	return nil
}

func memberNumber(ctx context.Context, tx pgx.Tx, property uuid.UUID) (string, error) {
	for i := 0; i < 5; i++ {
		_, n, err := docno.Running(ctx, tx, "membership.sequences", property, "M")
		if err != nil {
			return "", err
		}
		code := "M" + leftPad(n, 6)
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.members WHERE property_id = $1 AND code = $2)`, property, code).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return code, nil
		}
	}
	return "", errs.Conflict("member_number", "could not allocate a member number")
}

func leftPad(n, width int) string {
	s := strings.Repeat("0", width) + itoa(n)
	return s[len(s)-width:]
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// ensureMember finds or creates the member record of a customer.
func ensureMember(ctx context.Context, tx pgx.Tx, property uuid.UUID, c crm.Customer, typeName string, day time.Time) (uuid.UUID, string, bool, error) {
	if mid, err := MemberByCustomer(ctx, tx, c.ID); err != nil {
		return uuid.Nil, "", false, err
	} else if mid != nil {
		var code string
		if err := tx.QueryRow(ctx, `UPDATE membership.members SET status = 'active', membership_type = $2, archived_at = NULL WHERE id = $1 RETURNING code`,
			*mid, typeName).Scan(&code); err != nil {
			return uuid.Nil, "", false, err
		}
		return *mid, code, false, nil
	}
	code, err := memberNumber(ctx, tx, property)
	if err != nil {
		return uuid.Nil, "", false, err
	}
	mid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO membership.members (id, property_id, code, name, email, phone, membership_type, customer_id, joined_on, status, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::date,'active',$10,$10)`, mid, property, code, c.Name, nullStr(c.Email), nullStr(c.Phone), typeName, c.ID,
		day.Format("2006-01-02"), id.Ptr(actor(ctx))); err != nil {
		return uuid.Nil, "", false, err
	}
	return mid, code, true, nil
}

// IssueCard creates a card for a member (digital cards carry a QR token).
func IssueCard(ctx context.Context, tx pgx.Tx, property, memberID uuid.UUID, membershipID *uuid.UUID, cardType, number, legacy string, validUntil *time.Time) (uuid.UUID, error) {
	if number == "" {
		var code string
		if err := tx.QueryRow(ctx, `SELECT code FROM membership.members WHERE id = $1`, memberID).Scan(&code); err != nil {
			return uuid.Nil, err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM membership.cards WHERE member_id = $1`, memberID).Scan(&n); err != nil {
			return uuid.Nil, err
		}
		number = code + "-" + leftPad(n+1, 2)
		if cardType == "digital" {
			number = "D" + number
		}
	}
	cid := id.New()
	token := "MC" + secret.RandomToken(18)
	_, err := tx.Exec(ctx, `INSERT INTO membership.cards (id, property_id, member_id, membership_id, card_number, legacy_number, card_type, qr_token, valid_until,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, cid, property, memberID, membershipID, strings.ToUpper(number), nullStr(legacy),
		cardType, token, validUntil, id.Ptr(actor(ctx)))
	if ok, _ := dbtx.IsUniqueViolation(err); ok {
		return uuid.Nil, errs.Validation("card_number_taken", "card number already exists", errs.Field("cardNumber", "taken", "card number already exists"))
	}
	if err != nil {
		return uuid.Nil, err
	}
	return cid, history(ctx, tx, property, memberID, membershipID, "card_issued", "", "", map[string]any{"cardNumber": number, "cardType": cardType})
}

// Activate turns an approved application into Active memberships with
// cards, member account and portal access (FR-MEM-06/08/09).
func (m *Module) Activate(ctx context.Context, tx pgx.Tx, aid uuid.UUID, waivePayment bool, reason string) (Application, error) {
	// The payment subscriber and a manual activation may run together; the
	// second one waits here and then sees the completed application.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM membership.applications WHERE id = $1 FOR UPDATE`, aid); err != nil {
		return Application{}, err
	}
	a, err := GetApplication(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if a.Status == "completed" {
		return a, nil
	}
	if a.Status != "approved" {
		return a, errs.Conflict("application_not_approved", "only approved applications can be activated")
	}
	property := propOf(ctx)
	if a.FeeFolioID != nil {
		sum, err := billing.FolioSummary(ctx, tx, *a.FeeFolioID)
		if err != nil {
			return a, err
		}
		if bal, _ := decimal.NewFromString(sum.Balance); bal.IsPositive() && !waivePayment {
			return a, errs.Conflict("fee_unpaid", "the membership fee is not paid yet (balance "+sum.Balance+")")
		}
	}
	day := today(ctx, tx, property)
	t, err := loadType(ctx, tx, a.TypeID)
	if err != nil {
		return a, err
	}
	var unit string
	var count int
	if err := tx.QueryRow(ctx, `SELECT period_unit, period_count FROM membership.packages WHERE id = $1`, a.PackageID).Scan(&unit, &count); err != nil {
		return a, err
	}
	ends := PeriodEnd(day, unit, count)
	applicant, err := crm.GetCustomer(ctx, tx, a.CustomerID)
	if err != nil {
		return a, err
	}
	memberID, memberNo, _, err := ensureMember(ctx, tx, property, applicant, t.Name, day)
	if err != nil {
		return a, err
	}
	msID := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO membership.memberships (id, property_id, member_id, type_id, package_id, role, corporate_account_id, starts_on, ends_on,
		status, application_id, activated_at, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,'principal',$6,$7::date,$8::date,'active',$9,now(),$10,$10)`,
		msID, property, memberID, a.TypeID, a.PackageID, a.CorporateAccountID, day.Format("2006-01-02"), ends.Format("2006-01-02"), aid, id.Ptr(actor(ctx))); err != nil {
		return a, err
	}
	if err := history(ctx, tx, property, memberID, &msID, "activated", "pending", "active", map[string]any{"applicationId": aid, "type": t.Name,
		"endsOn": ends.Format("2006-01-02"), "waivedPayment": waivePayment, "reason": reason}); err != nil {
		return a, err
	}
	if _, err := IssueCard(ctx, tx, property, memberID, &msID, "digital", "", "", &ends); err != nil {
		return a, err
	}
	for _, d := range a.Dependents {
		dep, err := crm.GetCustomer(ctx, tx, d.CustomerID)
		if err != nil {
			return a, err
		}
		depID, _, _, err := ensureMember(ctx, tx, property, dep, t.Name, day)
		if err != nil {
			return a, err
		}
		role := "family"
		if a.CorporateAccountID != nil && t.Category == "corporate" {
			role = "nominee"
		} else if err := crm.EnsureRelationship(ctx, tx, property, a.CustomerID, d.CustomerID, d.Relationship); err != nil {
			return a, err
		}
		depMS := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO membership.memberships (id, property_id, member_id, type_id, package_id, principal_id, role, relationship,
			corporate_account_id, starts_on, ends_on, status, application_id, activated_at, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,$11::date,'active',$12,now(),$13,$13)`,
			depMS, property, depID, a.TypeID, a.PackageID, msID, role, d.Relationship, a.CorporateAccountID, day.Format("2006-01-02"), ends.Format("2006-01-02"),
			aid, id.Ptr(actor(ctx))); err != nil {
			return a, err
		}
		if err := history(ctx, tx, property, depID, &depMS, "activated", "", "active", map[string]any{"principalMemberNo": memberNo, "role": role}); err != nil {
			return a, err
		}
		if _, err := IssueCard(ctx, tx, property, depID, &depMS, "digital", "", "", &ends); err != nil {
			return a, err
		}
	}
	if _, err := m.Billing.EnsureAccount(ctx, tx, property, a.CustomerID, "member", &memberID); err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.applications SET status = 'completed', membership_id = $2, updated_by = $3 WHERE id = $1`,
		aid, msID, id.Ptr(actor(ctx))); err != nil {
		return a, err
	}
	if err := m.provisionPortal(ctx, tx, property, memberID, applicant); err != nil {
		return a, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventActivated, "membership.membership", &msID, &property, map[string]any{"membershipId": msID,
		"memberId": memberID, "memberNo": memberNo, "customerId": a.CustomerID, "applicationId": aid, "endsOn": ends.Format("2006-01-02")}); err != nil {
		return a, err
	}
	applicant, _ = crm.GetCustomer(ctx, tx, a.CustomerID)
	if err := m.notifyCustomer(ctx, tx, applicant, "membership.activated", map[string]any{"name": applicant.Name, "memberNo": memberNo, "type": t.Name,
		"validUntil": ends.Format("02 Jan 2006"), "link": m.portalURL() + "/membership"}); err != nil {
		return a, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "activate", EntityType: "membership.application", EntityID: aid.String(),
		EntityLabel: a.Number, PropertyID: &property, Reason: reason, After: map[string]any{"memberNo": memberNo, "membershipId": msID, "endsOn": ends.Format("2006-01-02"),
			"waivedPayment": waivePayment}}); err != nil {
		return a, err
	}
	return GetApplication(ctx, tx, aid)
}

// provisionPortal gives the member a Member Portal account (activation
// e-mail for new accounts).
func (m *Module) provisionPortal(ctx context.Context, tx pgx.Tx, property, memberID uuid.UUID, c crm.Customer) error {
	if m.Portal == nil || c.Email == "" {
		return nil
	}
	uid, created, err := m.Portal.EnsurePortalUser(ctx, tx, iam.PortalUser{Email: c.Email, Name: c.Name, Phone: c.Phone, RoleCode: "member", PropertyID: property})
	if err != nil {
		return err
	}
	if err := crm.LinkUser(ctx, tx, c.ID, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.members SET user_id = $2 WHERE id = $1 AND user_id IS NULL`, memberID, uid); err != nil {
		return err
	}
	if created {
		return m.Portal.SendActivation(ctx, tx, uid, c.Name)
	}
	return nil
}

// OnPaymentSettled activates applications and completes renewals whose fee
// folio is fully paid (subscriber of billing.payment_settled).
func (m *Module) OnPaymentSettled(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p billing.SettledPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.SourceType == nil || p.SourceID == nil || p.FolioID == nil {
		return nil
	}
	if *p.SourceType != "membership_fee" && *p.SourceType != "membership_renewal" {
		return nil
	}
	sid, err := uuid.Parse(*p.SourceID)
	if err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	if e.PropertyID == nil {
		return nil
	}
	ctx = withProperty(dbtx.System(ctx), *e.PropertyID)
	sum, err := billing.FolioSummary(ctx, tx, *p.FolioID)
	if err != nil {
		return err
	}
	if bal, _ := decimal.NewFromString(sum.Balance); bal.IsPositive() {
		return nil
	}
	if *p.SourceType == "membership_fee" {
		_, err := m.Activate(ctx, tx, sid, false, "")
		if errs.Is(err, errs.KindConflict) {
			return nil
		}
		return err
	}
	return m.CompleteRenewal(ctx, tx, sid)
}

// ── renewal (FR-MEM-13) ───────────────────────────────────────────────────

// Renew starts a renewal: a fee folio for the next period, completed when
// paid (immediately when the fee is zero).
func (m *Module) Renew(ctx context.Context, tx pgx.Tx, membershipID uuid.UUID, packageID *uuid.UUID) (uuid.UUID, error) {
	property := propOf(ctx)
	var memberID, typeID, curPkg uuid.UUID
	var role, status string
	var endsOn *time.Time
	err := tx.QueryRow(ctx, `SELECT member_id, type_id, coalesce(package_id, '00000000-0000-0000-0000-000000000000'::uuid), role, status, ends_on
		FROM membership.memberships WHERE id = $1 AND property_id = $2 FOR UPDATE`, membershipID, property).Scan(&memberID, &typeID, &curPkg, &role, &status, &endsOn)
	if dbtx.IsNoRows(err) {
		return uuid.Nil, errs.NotFound("membership")
	}
	if err != nil {
		return uuid.Nil, err
	}
	if role != "principal" {
		return uuid.Nil, errs.Conflict("renew_principal", "renew the principal membership; family members follow it")
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.renewals WHERE membership_id = $1 AND status = 'pending')`, membershipID).Scan(&pending); err != nil {
		return uuid.Nil, err
	}
	if pending {
		return uuid.Nil, errs.Conflict("renewal_pending", "a renewal is already waiting for payment")
	}
	pkg := curPkg
	if packageID != nil {
		pkg = *packageID
	}
	var unit, pkgName, fee string
	var count int
	var pkgType uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT type_id, period_unit, period_count, name, period_fee::text FROM membership.packages WHERE id = $1 AND status = 'active'`, pkg).
		Scan(&pkgType, &unit, &count, &pkgName, &fee); err != nil {
		return uuid.Nil, errs.Validation("invalid_package", "package not found", errs.Field("packageId", "not_found", "package not found"))
	}
	if pkgType != typeID {
		return uuid.Nil, errs.Validation("invalid_package", "the package belongs to another membership type (upgrade/downgrade arrives in P2)",
			errs.Field("packageId", "invalid", "same membership type"))
	}
	start := today(ctx, tx, property)
	if endsOn != nil && !endsOn.Before(start) {
		start = endsOn.AddDate(0, 0, 1)
	}
	newEnd := PeriodEnd(start, unit, count)
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO membership.renewals (id, property_id, membership_id, package_id, from_ends_on, new_ends_on, status, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6::date,'pending',$7,$7)`, rid, property, membershipID, pkg, endsOn, newEnd.Format("2006-01-02"), id.Ptr(actor(ctx))); err != nil {
		return uuid.Nil, err
	}
	amt, _ := decimal.NewFromString(fee)
	if amt.IsPositive() {
		var custID uuid.UUID
		var name, code string
		if err := tx.QueryRow(ctx, `SELECT customer_id, name, code FROM membership.members WHERE id = $1`, memberID).Scan(&custID, &name, &code); err != nil {
			return uuid.Nil, err
		}
		folio, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: property, CustomerID: &custID, HolderName: name, SourceType: "membership_renewal",
			SourceID: &rid, SourceRef: code})
		if err != nil {
			return uuid.Nil, err
		}
		if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: folio.ID, ChargeType: "renewal_fee", Description: "Renewal — " + pkgName,
			UnitPrice: amt, Net: amt, Total: amt, ReferenceType: "membership_renewal", ReferenceID: &rid}); err != nil {
			return uuid.Nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE membership.renewals SET folio_id = $2 WHERE id = $1`, rid, folio.ID); err != nil {
			return uuid.Nil, err
		}
	}
	if err := history(ctx, tx, property, memberID, &membershipID, "renewal_started", status, status, map[string]any{"renewalId": rid,
		"newEndsOn": newEnd.Format("2006-01-02"), "fee": fee}); err != nil {
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "renew", EntityType: "membership.membership", EntityID: membershipID.String(),
		PropertyID: &property, After: map[string]any{"renewalId": rid, "newEndsOn": newEnd.Format("2006-01-02"), "fee": fee}}); err != nil {
		return uuid.Nil, err
	}
	if !amt.IsPositive() {
		return rid, m.CompleteRenewal(ctx, tx, rid)
	}
	return rid, nil
}

// CompleteRenewal extends the principal and its family / nominees.
func (m *Module) CompleteRenewal(ctx context.Context, tx pgx.Tx, renewalID uuid.UUID) error {
	var msID, pkg uuid.UUID
	var status string
	var newEnd time.Time
	var property uuid.UUID
	err := tx.QueryRow(ctx, `SELECT membership_id, package_id, status, new_ends_on, property_id FROM membership.renewals WHERE id = $1 FOR UPDATE`, renewalID).
		Scan(&msID, &pkg, &status, &newEnd, &property)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.renewals SET status = 'completed', completed_at = now() WHERE id = $1`, renewalID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `UPDATE membership.memberships SET ends_on = $2::date, status = 'active', package_id = CASE WHEN id = $1 THEN $3 ELSE package_id END
		WHERE (id = $1 OR principal_id = $1) AND status IN ('active', 'expired') RETURNING id, member_id`, msID, newEnd.Format("2006-01-02"), pkg)
	if err != nil {
		return err
	}
	type pair struct{ ms, member uuid.UUID }
	var list []pair
	for rows.Next() {
		var x pair
		if err := rows.Scan(&x.ms, &x.member); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	var principalMember uuid.UUID
	for _, x := range list {
		if x.ms == msID {
			principalMember = x.member
		}
		if _, err := tx.Exec(ctx, `UPDATE membership.members SET status = 'active' WHERE id = $1`, x.member); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE membership.cards SET valid_until = $2::date WHERE membership_id = $1 AND status = 'active'`, x.ms, newEnd.Format("2006-01-02")); err != nil {
			return err
		}
		ms := x.ms
		if err := history(ctx, tx, property, x.member, &ms, "renewed", "", "active", map[string]any{"endsOn": newEnd.Format("2006-01-02"), "renewalId": renewalID}); err != nil {
			return err
		}
	}
	if _, err := m.Events.Publish(ctx, tx, EventRenewed, "membership.membership", &msID, &property, map[string]any{"membershipId": msID,
		"renewalId": renewalID, "endsOn": newEnd.Format("2006-01-02")}); err != nil {
		return err
	}
	if principalMember != uuid.Nil {
		var custID *uuid.UUID
		var code string
		_ = tx.QueryRow(ctx, `SELECT customer_id, code FROM membership.members WHERE id = $1`, principalMember).Scan(&custID, &code)
		if custID != nil {
			if c, err := crm.GetCustomer(ctx, tx, *custID); err == nil {
				if err := m.notifyCustomer(ctx, tx, c, "membership.renewed", map[string]any{"name": c.Name, "memberNo": code,
					"validUntil": newEnd.Format("02 Jan 2006")}); err != nil {
					return err
				}
			}
		}
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "renewal_completed", EntityType: "membership.membership", EntityID: msID.String(),
		PropertyID: &property, After: map[string]any{"endsOn": newEnd.Format("2006-01-02")}})
}
