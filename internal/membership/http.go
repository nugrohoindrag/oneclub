package membership

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/docno"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/ratelimit"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/iam"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/resource"
)

func propertyFrom(ctx context.Context) (uuid.UUID, bool) { return reqctx.Property(ctx) }

func withProperty(ctx context.Context, p uuid.UUID) context.Context {
	return reqctx.WithProperty(ctx, p)
}

// ── applications HTTP ─────────────────────────────────────────────────────

type ApplicationRequest struct {
	CustomerID         uuid.UUID   `json:"customerId"`
	TypeID             uuid.UUID   `json:"typeId"`
	PackageID          uuid.UUID   `json:"packageId"`
	CorporateAccountID *uuid.UUID  `json:"corporateAccountId,omitempty"`
	Dependents         []Dependent `json:"dependents,omitempty"`
	Documents          []Document  `json:"documents,omitempty"`
	Notes              string      `json:"notes,omitempty"`
}

type ApplicationPatch struct {
	PackageID          *uuid.UUID  `json:"packageId,omitempty"`
	CorporateAccountID *uuid.UUID  `json:"corporateAccountId,omitempty"`
	Dependents         []Dependent `json:"dependents,omitempty"`
	Documents          []Document  `json:"documents,omitempty"`
	Notes              *string     `json:"notes,omitempty"`
}

type ActivateRequest struct {
	WaivePayment bool   `json:"waivePayment,omitempty" doc:"Activate before the fee is paid (Payment Policy exception, permission activate_unpaid)"`
	Reason       string `json:"reason,omitempty"`
}

func validDeps(deps []Dependent) error {
	for _, d := range deps {
		switch d.Relationship {
		case "spouse", "child", "parent", "sibling", "other":
		default:
			return errs.Validation("invalid_dependent", "invalid relationship", errs.Field("dependents", "invalid", "spouse, child, parent, sibling or other"))
		}
	}
	return nil
}

func (m *Module) createApplication(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ApplicationRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if err := validDeps(req.Dependents); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		property := propOf(ctx)
		var out Application
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			if channel == "member_portal" {
				c, err := crm.CustomerByUser(ctx, tx, authz.From(ctx).UserID)
				if err != nil {
					return err
				}
				req.CustomerID = c.ID
				property = c.PropertyID
				ctx = withProperty(ctx, property)
			}
			ok, err := crm.ExistsInProperty(ctx, tx, property, req.CustomerID)
			if err != nil {
				return err
			}
			if !ok {
				return errs.Validation("invalid_customer", "customer not found", errs.Field("customerId", "not_found", "customer not found in this property"))
			}
			var pkgType uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT type_id FROM membership.packages WHERE id = $1 AND property_id = $2 AND status = 'active'`, req.PackageID, property).Scan(&pkgType); err != nil {
				return errs.Validation("invalid_package", "package not found", errs.Field("packageId", "not_found", "package not found"))
			}
			if pkgType != req.TypeID {
				return errs.Validation("invalid_package", "the package belongs to another membership type", errs.Field("packageId", "invalid", "package of the chosen type"))
			}
			num, _, err := docno.Running(ctx, tx, "membership.sequences", property, "APP")
			if err != nil {
				return err
			}
			deps, _ := json.Marshal(nonNil(req.Dependents))
			docs, _ := json.Marshal(nonNilDocs(req.Documents))
			aid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO membership.applications (id, property_id, number, channel, customer_id, type_id, package_id, corporate_account_id,
				dependents, documents, status, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'draft',$11,$12,$12)`,
				aid, property, num, channel, req.CustomerID, req.TypeID, req.PackageID, req.CorporateAccountID, deps, docs, nullStr(req.Notes), id.Ptr(actor(ctx))); err != nil {
				if dbtx.IsForeignKeyViolation(err) {
					return errs.Validation("invalid_reference", "membership type or corporate account not found")
				}
				return err
			}
			if out, err = GetApplication(ctx, tx, aid); err != nil {
				return err
			}
			if _, err := m.CheckEligibility(ctx, tx, out); err != nil {
				return err
			}
			if out, err = GetApplication(ctx, tx, aid); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionCreate, EntityType: "membership.application", EntityID: aid.String(),
				EntityLabel: num, PropertyID: &property, After: out})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, out)
	}
}

func nonNil(d []Dependent) []Dependent {
	if d == nil {
		return []Dependent{}
	}
	return d
}

func nonNilDocs(d []Document) []Document {
	if d == nil {
		return []Document{}
	}
	return d
}

func (m *Module) listApplications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := []string{"a.property_id = $1"}
	args := []any{propOf(ctx)}
	if v := lp.Filters["status"]; v != "" {
		args = append(args, strings.Split(v, ","))
		where = append(where, "a.status = ANY($"+strconv.Itoa(len(args))+")")
	}
	if v := lp.Filters["customerId"]; v != "" {
		args = append(args, v)
		where = append(where, "a.customer_id::text = $"+strconv.Itoa(len(args)))
	}
	out := []Application{}
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.id FROM membership.applications a WHERE `+strings.Join(where, " AND ")+` ORDER BY a.created_at DESC LIMIT 300`, args...)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var x uuid.UUID
			if err := rows.Scan(&x); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, x)
		}
		rows.Close()
		for _, x := range ids {
			a, err := GetApplication(ctx, tx, x)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Application]{Items: out})
}

func (m *Module) withApp(w http.ResponseWriter, r *http.Request, write bool, fn func(ctx context.Context, tx pgx.Tx, a Application) (Application, error)) {
	aid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	run := m.DB.WithReadTx
	if write {
		run = m.DB.WithTx
	}
	var out Application
	err = run(ctx, func(tx pgx.Tx) error {
		var prop uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id FROM membership.applications WHERE id = $1`, aid).Scan(&prop); err != nil || prop != propOf(ctx) {
			return errs.NotFound("membership application")
		}
		a, err := GetApplication(ctx, tx, aid)
		if err != nil {
			return err
		}
		out, err = fn(ctx, tx, a)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (m *Module) getApplication(w http.ResponseWriter, r *http.Request) {
	m.withApp(w, r, false, func(_ context.Context, _ pgx.Tx, a Application) (Application, error) { return a, nil })
}

func (m *Module) patchApplication(w http.ResponseWriter, r *http.Request) {
	var req ApplicationPatch
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := validDeps(req.Dependents); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.withApp(w, r, true, func(ctx context.Context, tx pgx.Tx, a Application) (Application, error) {
		if a.Status != "draft" {
			return a, errs.Conflict("application_not_draft", "only draft applications can be edited")
		}
		if req.PackageID != nil {
			var t uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT type_id FROM membership.packages WHERE id = $1 AND status = 'active'`, *req.PackageID).Scan(&t); err != nil || t != a.TypeID {
				return a, errs.Validation("invalid_package", "package of the chosen type required", errs.Field("packageId", "invalid", "package of the chosen type"))
			}
			if _, err := tx.Exec(ctx, `UPDATE membership.applications SET package_id = $2 WHERE id = $1`, a.ID, *req.PackageID); err != nil {
				return a, err
			}
		}
		if req.CorporateAccountID != nil {
			if _, err := tx.Exec(ctx, `UPDATE membership.applications SET corporate_account_id = $2 WHERE id = $1`, a.ID, *req.CorporateAccountID); err != nil {
				return a, err
			}
		}
		if req.Dependents != nil {
			raw, _ := json.Marshal(req.Dependents)
			if _, err := tx.Exec(ctx, `UPDATE membership.applications SET dependents = $2 WHERE id = $1`, a.ID, raw); err != nil {
				return a, err
			}
		}
		if req.Documents != nil {
			raw, _ := json.Marshal(req.Documents)
			if _, err := tx.Exec(ctx, `UPDATE membership.applications SET documents = $2 WHERE id = $1`, a.ID, raw); err != nil {
				return a, err
			}
		}
		if req.Notes != nil {
			if _, err := tx.Exec(ctx, `UPDATE membership.applications SET notes = $2 WHERE id = $1`, a.ID, nullStr(*req.Notes)); err != nil {
				return a, err
			}
		}
		after, err := GetApplication(ctx, tx, a.ID)
		if err != nil {
			return a, err
		}
		if _, err := m.CheckEligibility(ctx, tx, after); err != nil {
			return a, err
		}
		p := propOf(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionUpdate, EntityType: "membership.application", EntityID: a.ID.String(),
			EntityLabel: a.Number, PropertyID: &p, Before: a, After: after}); err != nil {
			return a, err
		}
		return GetApplication(ctx, tx, a.ID)
	})
}

func (m *Module) checkApplication(w http.ResponseWriter, r *http.Request) {
	m.withApp(w, r, true, func(ctx context.Context, tx pgx.Tx, a Application) (Application, error) {
		res, err := m.CheckEligibility(ctx, tx, a)
		if err != nil {
			return a, err
		}
		p := propOf(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "eligibility_check", EntityType: "membership.application", EntityID: a.ID.String(),
			EntityLabel: a.Number, PropertyID: &p, After: res}); err != nil {
			return a, err
		}
		return GetApplication(ctx, tx, a.ID)
	})
}

func (m *Module) submitApplication(w http.ResponseWriter, r *http.Request) {
	m.withApp(w, r, true, func(ctx context.Context, tx pgx.Tx, a Application) (Application, error) {
		return m.Submit(ctx, tx, a.ID)
	})
}

func (m *Module) cancelApplication(w http.ResponseWriter, r *http.Request) {
	var req billing.ReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.withApp(w, r, true, func(ctx context.Context, tx pgx.Tx, a Application) (Application, error) {
		if a.Status != "draft" && a.Status != "pending" && a.Status != "approved" {
			return a, errs.Conflict("application_closed", "this application can no longer be cancelled")
		}
		if a.Status == "pending" && a.ApprovalRequestID != nil {
			if err := m.Approvals.Cancel(ctx, tx, *a.ApprovalRequestID, req.Reason); err != nil {
				return a, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE membership.applications SET status = 'cancelled', decision_reason = $2, decided_at = now() WHERE id = $1`, a.ID, nullStr(req.Reason)); err != nil {
			return a, err
		}
		if a.FeeFolioID != nil {
			if err := m.Billing.CancelPending(ctx, tx, *a.FeeFolioID, "application cancelled"); err != nil {
				return a, err
			}
		}
		p := propOf(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionStatusChange, EntityType: "membership.application", EntityID: a.ID.String(),
			EntityLabel: a.Number, PropertyID: &p, Reason: req.Reason, After: map[string]any{"status": "cancelled"}}); err != nil {
			return a, err
		}
		return GetApplication(ctx, tx, a.ID)
	})
}

func (m *Module) activateApplication(w http.ResponseWriter, r *http.Request) {
	var req ActivateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.withApp(w, r, true, func(ctx context.Context, tx pgx.Tx, a Application) (Application, error) {
		if req.WaivePayment {
			p := propOf(ctx)
			if !authz.From(ctx).Can("membership.application.activate_unpaid", &p) {
				return a, errs.Forbidden("missing permission membership.application.activate_unpaid")
			}
			if strings.TrimSpace(req.Reason) == "" {
				return a, errs.Validation("reason_required", "a reason is required to activate before payment", errs.Field("reason", "required", "reason is required"))
			}
		}
		return m.Activate(ctx, tx, a.ID, req.WaivePayment, req.Reason)
	})
}

// ── memberships, cards, history ───────────────────────────────────────────

// Membership is the API view of a membership period.
type Membership struct {
	ID                 uuid.UUID  `json:"id"`
	MemberID           uuid.UUID  `json:"memberId"`
	MemberNo           string     `json:"memberNo"`
	MemberName         string     `json:"memberName"`
	TypeID             uuid.UUID  `json:"typeId"`
	TypeName           string     `json:"typeName"`
	PackageID          *uuid.UUID `json:"packageId"`
	PrincipalID        *uuid.UUID `json:"principalId"`
	Role               string     `json:"role" enum:"principal,family,nominee"`
	Relationship       *string    `json:"relationship"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	StartsOn           string     `json:"startsOn"`
	EndsOn             *string    `json:"endsOn"`
	Status             string     `json:"status" enum:"pending,active,expired,inactive"`
	DaysToExpiry       *int       `json:"daysToExpiry"`
	RenewalPending     bool       `json:"renewalPending"`
}

const membershipCols = `ms.id, ms.member_id, mb.code, mb.name, ms.type_id, t.name, ms.package_id, ms.principal_id, ms.role, ms.relationship, ms.corporate_account_id,
	ms.starts_on, ms.ends_on, ms.status, (ms.ends_on - current_date),
	EXISTS (SELECT 1 FROM membership.renewals rn WHERE rn.membership_id = ms.id AND rn.status = 'pending')
	FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id JOIN membership.types t ON t.id = ms.type_id`

func scanMembership(row pgx.Row) (Membership, error) {
	var x Membership
	var s time.Time
	var e *time.Time
	err := row.Scan(&x.ID, &x.MemberID, &x.MemberNo, &x.MemberName, &x.TypeID, &x.TypeName, &x.PackageID, &x.PrincipalID, &x.Role, &x.Relationship,
		&x.CorporateAccountID, &s, &e, &x.Status, &x.DaysToExpiry, &x.RenewalPending)
	x.StartsOn = s.Format("2006-01-02")
	if e != nil {
		v := e.Format("2006-01-02")
		x.EndsOn = &v
	}
	return x, err
}

func listMemberships(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]Membership, error) {
	rows, err := q.Query(ctx, `SELECT `+membershipCols+` WHERE `+where+` ORDER BY ms.ends_on NULLS LAST, mb.name LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		x, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (m *Module) listMembershipsHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := []string{"ms.property_id = $1"}
	args := []any{propOf(ctx)}
	if v := lp.Filters["status"]; v != "" {
		args = append(args, strings.Split(v, ","))
		where = append(where, "ms.status = ANY($"+strconv.Itoa(len(args))+")")
	}
	if v := lp.Filters["memberId"]; v != "" {
		args = append(args, v)
		where = append(where, "ms.member_id::text = $"+strconv.Itoa(len(args)))
	}
	if v := r.URL.Query().Get("expiringWithinDays"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("invalid_days", "expiringWithinDays must be a number"))
			return
		}
		args = append(args, n)
		where = append(where, "ms.status = 'active' AND ms.role = 'principal' AND ms.ends_on <= current_date + $"+strconv.Itoa(len(args))+"::int")
	}
	if lp.Q != "" {
		args = append(args, "%"+lp.Q+"%")
		where = append(where, "(mb.code ILIKE $"+strconv.Itoa(len(args))+" OR mb.name ILIKE $"+strconv.Itoa(len(args))+")")
	}
	var out []Membership
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = listMemberships(ctx, tx, strings.Join(where, " AND "), args...)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Membership]{Items: out})
}

type RenewRequest struct {
	PackageID *uuid.UUID `json:"packageId,omitempty"`
}

type RenewResult struct {
	RenewalID  uuid.UUID  `json:"renewalId"`
	Membership Membership `json:"membership"`
	Status     string     `json:"status" enum:"pending,completed"`
	FolioID    *uuid.UUID `json:"folioId"`
}

func (m *Module) renewHTTP(w http.ResponseWriter, r *http.Request) {
	msID, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req RenewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out RenewResult
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rid, err := m.Renew(ctx, tx, msID, req.PackageID)
		if err != nil {
			return err
		}
		out.RenewalID = rid
		if err := tx.QueryRow(ctx, `SELECT status, folio_id FROM membership.renewals WHERE id = $1`, rid).Scan(&out.Status, &out.FolioID); err != nil {
			return err
		}
		list, err := listMemberships(ctx, tx, "ms.id = $1", msID)
		if err != nil || len(list) == 0 {
			return err
		}
		out.Membership = list[0]
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// Card is the API view of a member card.
type Card struct {
	ID           uuid.UUID  `json:"id"`
	MemberID     uuid.UUID  `json:"memberId"`
	MemberNo     string     `json:"memberNo"`
	MemberName   string     `json:"memberName"`
	MembershipID *uuid.UUID `json:"membershipId"`
	CardNumber   string     `json:"cardNumber"`
	LegacyNumber *string    `json:"legacyNumber"`
	CardType     string     `json:"cardType" enum:"physical,digital"`
	QRToken      string     `json:"qrToken" doc:"Encode as oneclub:card:<token> in the QR"`
	IssuedAt     time.Time  `json:"issuedAt"`
	ValidUntil   *string    `json:"validUntil"`
	Status       string     `json:"status" enum:"active,inactive"`
}

const cardCols = `c.id, c.member_id, m.code, m.name, c.membership_id, c.card_number, c.legacy_number, c.card_type, c.qr_token, c.issued_at, c.valid_until, c.status
	FROM membership.cards c JOIN membership.members m ON m.id = c.member_id`

func scanCard(row pgx.Row) (Card, error) {
	var c Card
	var vu *time.Time
	err := row.Scan(&c.ID, &c.MemberID, &c.MemberNo, &c.MemberName, &c.MembershipID, &c.CardNumber, &c.LegacyNumber, &c.CardType, &c.QRToken, &c.IssuedAt, &vu, &c.Status)
	if vu != nil {
		s := vu.Format("2006-01-02")
		c.ValidUntil = &s
	}
	return c, err
}

func listCards(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]Card, error) {
	rows, err := q.Query(ctx, `SELECT `+cardCols+` WHERE `+where+` ORDER BY c.issued_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Module) listCardsHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := "c.property_id = $1"
	args := []any{propOf(ctx)}
	if v := lp.Filters["memberId"]; v != "" {
		args = append(args, v)
		where += " AND c.member_id::text = $2"
	}
	var out []Card
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = listCards(ctx, tx, where, args...)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Card]{Items: out})
}

type IssueCardRequest struct {
	MemberID     uuid.UUID `json:"memberId"`
	CardType     string    `json:"cardType" enum:"physical,digital"`
	CardNumber   string    `json:"cardNumber,omitempty" doc:"Physical card number; generated when empty"`
	LegacyNumber string    `json:"legacyNumber,omitempty" doc:"Card number from Rhapsody"`
}

func (m *Module) issueCardHTTP(w http.ResponseWriter, r *http.Request) {
	var req IssueCardRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.CardType != "physical" && req.CardType != "digital" {
		httpx.WriteError(w, r, errs.Validation("invalid_card_type", "card type must be physical or digital", errs.Field("cardType", "invalid", "physical or digital")))
		return
	}
	ctx := r.Context()
	var out Card
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		p := propOf(ctx)
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.members WHERE id = $1 AND property_id = $2)`, req.MemberID, p).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errs.Validation("invalid_member", "member not found", errs.Field("memberId", "not_found", "member not found"))
		}
		var msID *uuid.UUID
		var ends *time.Time
		_ = tx.QueryRow(ctx, `SELECT id, ends_on FROM membership.memberships WHERE member_id = $1 AND status = 'active' ORDER BY starts_on DESC LIMIT 1`,
			req.MemberID).Scan(&msID, &ends)
		cid, err := IssueCard(ctx, tx, p, req.MemberID, msID, req.CardType, strings.TrimSpace(req.CardNumber), strings.TrimSpace(req.LegacyNumber), ends)
		if err != nil {
			return err
		}
		list, err := listCards(ctx, tx, "c.id = $1", cid)
		if err != nil {
			return err
		}
		out = list[0]
		return audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "issue_card", EntityType: "membership.card", EntityID: cid.String(),
			EntityLabel: out.CardNumber, PropertyID: &p, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (m *Module) deactivateCard(w http.ResponseWriter, r *http.Request) {
	cid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req billing.ReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out Card
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		p := propOf(ctx)
		var mid uuid.UUID
		var ms *uuid.UUID
		var num string
		err := tx.QueryRow(ctx, `UPDATE membership.cards SET status = 'inactive', updated_by = $3 WHERE id = $1 AND property_id = $2 AND status = 'active'
			RETURNING member_id, membership_id, card_number`, cid, p, id.Ptr(actor(ctx))).Scan(&mid, &ms, &num)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("active card")
		}
		if err != nil {
			return err
		}
		if err := history(ctx, tx, p, mid, ms, "card_deactivated", "active", "inactive", map[string]any{"cardNumber": num, "reason": req.Reason}); err != nil {
			return err
		}
		list, err := listCards(ctx, tx, "c.id = $1", cid)
		if err != nil {
			return err
		}
		out = list[0]
		return audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionStatusChange, EntityType: "membership.card", EntityID: cid.String(),
			EntityLabel: num, PropertyID: &p, Reason: req.Reason, After: map[string]any{"status": "inactive"}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// HistoryEntry is one membership history event.
type HistoryEntry struct {
	ID           uuid.UUID       `json:"id"`
	MemberID     uuid.UUID       `json:"memberId"`
	MemberNo     string          `json:"memberNo"`
	MemberName   string          `json:"memberName"`
	MembershipID *uuid.UUID      `json:"membershipId"`
	Event        string          `json:"event"`
	FromStatus   *string         `json:"fromStatus"`
	ToStatus     *string         `json:"toStatus"`
	Details      json.RawMessage `json:"details"`
	OccurredAt   time.Time       `json:"occurredAt"`
}

func listHistory(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]HistoryEntry, error) {
	rows, err := q.Query(ctx, `SELECT h.id, h.member_id, m.code, m.name, h.membership_id, h.event, h.from_status, h.to_status, h.details, h.occurred_at
		FROM membership.history h JOIN membership.members m ON m.id = h.member_id WHERE `+where+` ORDER BY h.occurred_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var x HistoryEntry
		if err := rows.Scan(&x.ID, &x.MemberID, &x.MemberNo, &x.MemberName, &x.MembershipID, &x.Event, &x.FromStatus, &x.ToStatus, &x.Details, &x.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (m *Module) historyHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	where := "h.property_id = $1"
	args := []any{propOf(ctx)}
	if v := r.URL.Query().Get("memberId"); v != "" {
		args = append(args, v)
		where += " AND h.member_id::text = $2"
	}
	var out []HistoryEntry
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = listHistory(ctx, tx, where, args...)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[HistoryEntry]{Items: out})
}

// MemberProfile is the member detail page (Members → Member Profile).
type MemberProfile struct {
	MemberID    uuid.UUID        `json:"memberId"`
	MemberNo    string           `json:"memberNo"`
	Name        string           `json:"name"`
	CustomerID  *uuid.UUID       `json:"customerId"`
	Status      string           `json:"status"`
	Standing    Info             `json:"standing"`
	Memberships []Membership     `json:"memberships"`
	Family      []Membership     `json:"family"`
	Cards       []Card           `json:"cards"`
	Account     *billing.Account `json:"account"`
	History     []HistoryEntry   `json:"history"`
	Renewals    []RenewalView    `json:"renewals"`
	PortalUser  *uuid.UUID       `json:"portalUserId"`
}

// RenewalView is a renewal of a membership.
type RenewalView struct {
	ID        uuid.UUID  `json:"id"`
	Status    string     `json:"status" enum:"pending,completed,cancelled"`
	NewEndsOn string     `json:"newEndsOn"`
	FolioID   *uuid.UUID `json:"folioId"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Profile builds the member profile.
func Profile(ctx context.Context, q dbtx.Querier, memberID uuid.UUID) (MemberProfile, error) {
	var p MemberProfile
	var prop uuid.UUID
	err := q.QueryRow(ctx, `SELECT id, code, name, customer_id, status, user_id, property_id FROM membership.members WHERE id = $1`, memberID).
		Scan(&p.MemberID, &p.MemberNo, &p.Name, &p.CustomerID, &p.Status, &p.PortalUser, &prop)
	if dbtx.IsNoRows(err) {
		return p, errs.NotFound("member")
	}
	if err != nil {
		return p, err
	}
	if p.Standing, err = Standing(ctx, q, memberID, clock.Now()); err != nil {
		return p, err
	}
	if p.Memberships, err = listMemberships(ctx, q, "ms.member_id = $1", memberID); err != nil {
		return p, err
	}
	if p.Family, err = listMemberships(ctx, q, `ms.principal_id IN (SELECT id FROM membership.memberships WHERE member_id = $1)
		OR ms.id IN (SELECT principal_id FROM membership.memberships WHERE member_id = $1 AND principal_id IS NOT NULL)
		OR ms.principal_id IN (SELECT principal_id FROM membership.memberships WHERE member_id = $1 AND principal_id IS NOT NULL)`, memberID); err != nil {
		return p, err
	}
	filtered := p.Family[:0]
	for _, f := range p.Family {
		if f.MemberID != memberID {
			filtered = append(filtered, f)
		}
	}
	p.Family = filtered
	if p.Cards, err = listCards(ctx, q, "c.member_id = $1", memberID); err != nil {
		return p, err
	}
	if p.History, err = listHistory(ctx, q, "h.member_id = $1", memberID); err != nil {
		return p, err
	}
	p.Renewals = []RenewalView{}
	rows, err := q.Query(ctx, `SELECT r.id, r.status, r.new_ends_on, r.folio_id, r.created_at FROM membership.renewals r
		JOIN membership.memberships ms ON ms.id = r.membership_id WHERE ms.member_id = $1 ORDER BY r.created_at DESC`, memberID)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var rv RenewalView
		var d time.Time
		if err := rows.Scan(&rv.ID, &rv.Status, &d, &rv.FolioID, &rv.CreatedAt); err != nil {
			rows.Close()
			return p, err
		}
		rv.NewEndsOn = d.Format("2006-01-02")
		p.Renewals = append(p.Renewals, rv)
	}
	rows.Close()
	if holder, err := AccountHolder(ctx, q, memberID); err == nil && holder != nil {
		if a, err := billing.AccountFor(ctx, q, prop, *holder, "member"); err == nil {
			p.Account = a
		}
	}
	return p, nil
}

// AccountHolder returns the customer whose member account pays for a member
// (the principal of a family / corporate membership).
func AccountHolder(ctx context.Context, q dbtx.Querier, memberID uuid.UUID) (*uuid.UUID, error) {
	var cust *uuid.UUID
	err := q.QueryRow(ctx, `SELECT pm.customer_id FROM membership.memberships ms
		JOIN membership.memberships pms ON pms.id = coalesce(ms.principal_id, ms.id)
		JOIN membership.members pm ON pm.id = pms.member_id
		WHERE ms.member_id = $1 ORDER BY (ms.status = 'active') DESC, ms.starts_on DESC LIMIT 1`, memberID).Scan(&cust)
	if dbtx.IsNoRows(err) {
		err = q.QueryRow(ctx, `SELECT customer_id FROM membership.members WHERE id = $1`, memberID).Scan(&cust)
	}
	return cust, err
}

func (m *Module) profileHTTP(w http.ResponseWriter, r *http.Request) {
	mid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out MemberProfile
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var prop uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id FROM membership.members WHERE id = $1`, mid).Scan(&prop); err != nil || prop != propOf(ctx) {
			return errs.NotFound("member")
		}
		out, err = Profile(ctx, tx, mid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// inviteMember sends the Member Portal activation (members migrated from
// Rhapsody, FR-APP-01).
func (m *Module) inviteMember(w http.ResponseWriter, r *http.Request) {
	mid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out MemberProfile
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		p := propOf(ctx)
		var cust *uuid.UUID
		var name string
		if err := tx.QueryRow(ctx, `SELECT customer_id, name FROM membership.members WHERE id = $1 AND property_id = $2`, mid, p).Scan(&cust, &name); err != nil {
			return errs.NotFound("member")
		}
		if cust == nil {
			return errs.Conflict("no_customer", "link the member to a customer profile with an e-mail first")
		}
		c, err := crm.GetCustomer(ctx, tx, *cust)
		if err != nil {
			return err
		}
		if c.Email == "" {
			return errs.Conflict("email_required", "the customer profile has no e-mail address")
		}
		uid, _, err := m.Portal.EnsurePortalUser(ctx, tx, iamPortalUser(c, p))
		if err != nil {
			return err
		}
		if err := crm.LinkUser(ctx, tx, c.ID, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE membership.members SET user_id = $2 WHERE id = $1`, mid, uid); err != nil {
			return err
		}
		if err := m.Portal.SendActivation(ctx, tx, uid, c.Name); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "portal_invite", EntityType: "membership.member", EntityID: mid.String(),
			EntityLabel: name, PropertyID: &p}); err != nil {
			return err
		}
		out, err = Profile(ctx, tx, mid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// PublicActivateRequest lets a migrated member request the activation link.
type PublicActivateRequest struct {
	MemberNo  string     `json:"memberNo"`
	Email     string     `json:"email"`
	BirthDate route.Date `json:"birthDate"`
}

var publicLimiter = ratelimit.New()

// publicActivate always answers 202 (no account enumeration).
func (m *Module) publicActivate(w http.ResponseWriter, r *http.Request) {
	var req PublicActivateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	if !publicLimiter.Allow("activate:"+reqctx.GetMeta(ctx).IP, 10, time.Minute) {
		httpx.WriteError(w, r, errs.RateLimited())
		return
	}
	sys := dbtx.System(ctx)
	err := m.DB.WithTx(sys, func(tx pgx.Tx) error {
		var mid, prop uuid.UUID
		var cust *uuid.UUID
		err := tx.QueryRow(sys, `SELECT id, property_id, customer_id FROM membership.members WHERE upper(code) = upper($1) AND status = 'active'
			UNION ALL SELECT m.id, m.property_id, m.customer_id FROM membership.cards c JOIN membership.members m ON m.id = c.member_id
			WHERE upper(c.legacy_number) = upper($1) AND m.status = 'active' LIMIT 1`, strings.TrimSpace(req.MemberNo)).Scan(&mid, &prop, &cust)
		matched := err == nil && cust != nil
		var c crm.Customer
		if matched {
			c, err = crm.GetCustomer(sys, tx, *cust)
			matched = err == nil && strings.EqualFold(c.Email, strings.TrimSpace(req.Email)) && c.BirthDate != nil &&
				c.BirthDate.Format("2006-01-02") == string(req.BirthDate)
		}
		if !matched {
			return audit.Record(sys, tx, audit.Entry{Module: "membership", Action: "portal_activation_refused", Category: audit.CategorySecurity,
				EntityType: "membership.member", ActorName: "public", Metadata: map[string]any{"memberNo": req.MemberNo}})
		}
		pctx := withProperty(sys, prop)
		uid, _, err := m.Portal.EnsurePortalUser(pctx, tx, iamPortalUser(c, prop))
		if err != nil {
			return err
		}
		if err := crm.LinkUser(pctx, tx, c.ID, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(pctx, `UPDATE membership.members SET user_id = $2 WHERE id = $1`, mid, uid); err != nil {
			return err
		}
		if err := m.Portal.SendActivation(pctx, tx, uid, c.Name); err != nil {
			return err
		}
		return audit.Record(pctx, tx, audit.Entry{Module: "membership", Action: "portal_activation_requested", Category: audit.CategorySecurity,
			EntityType: "membership.member", EntityID: mid.String(), EntityLabel: c.Name, PropertyID: &prop, ActorName: c.Name})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// ── Member Portal (EP-13) ─────────────────────────────────────────────────

// MyMembership is the member's own view (My Membership, Digital Member
// Card, Membership Benefits, Family Members).
type MyMembership struct {
	Profile  MemberProfile `json:"profile"`
	Card     *Card         `json:"card" doc:"Digital Member Card (cache it offline)"`
	Benefits []string      `json:"benefits"`
	ClubName string        `json:"clubName"`
}

func (m *Module) myMembership(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out MyMembership
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		mid, err := MemberByUser(ctx, tx, authz.From(ctx).UserID)
		if err != nil {
			return err
		}
		if mid == nil {
			return errs.NotFound("membership")
		}
		if out.Profile, err = Profile(ctx, tx, *mid); err != nil {
			return err
		}
		for i := range out.Profile.Cards {
			c := out.Profile.Cards[i]
			if c.Status == "active" && c.CardType == "digital" {
				out.Card = &c
				break
			}
		}
		pr := out.Profile.Standing.Privileges
		out.Benefits = []string{}
		if pr.MemberRate {
			out.Benefits = append(out.Benefits, "Member Rate on golf")
		}
		if pr.GolfAccess {
			out.Benefits = append(out.Benefits, fmt.Sprintf("Book tee times up to %d days ahead", pr.BookingWindowDays))
		}
		if pr.MaxGuests > 0 {
			out.Benefits = append(out.Benefits, fmt.Sprintf("Bring up to %d guests (Guest of Member rate)", pr.MaxGuests))
		}
		if out.Profile.Account != nil {
			out.Benefits = append(out.Benefits, "Member charge (signing bill) to your member account")
		}
		_ = tx.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&out.ClubName)
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (m *Module) myApplications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []Application{}
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		c, err := crm.CustomerByUser(ctx, tx, authz.From(ctx).UserID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM membership.applications WHERE customer_id = $1 ORDER BY created_at DESC`, c.ID)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var x uuid.UUID
			if err := rows.Scan(&x); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, x)
		}
		rows.Close()
		for _, x := range ids {
			a, err := GetApplication(ctx, tx, x)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Application]{Items: out})
}

// ── jobs: expiry and renewal reminders ────────────────────────────────────

type LifecycleArgs struct{}

func (LifecycleArgs) Kind() string { return "membership_lifecycle" }
func (LifecycleArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type LifecycleWorker struct {
	river.WorkerDefaults[LifecycleArgs]
	M *Module
}

// Work expires ended memberships and sends H-30 / H-7 renewal reminders.
func (w *LifecycleWorker) Work(ctx context.Context, _ *river.Job[LifecycleArgs]) error {
	_, err := w.M.RunLifecycle(dbtx.System(ctx))
	return err
}

// LifecycleResult reports what the daily run did.
type LifecycleResult struct {
	Expired   int `json:"expired"`
	Reminders int `json:"reminders"`
}

// RenewalReminderDays are the reminder offsets (FR-MEM-13).
var RenewalReminderDays = []int{30, 7}

// RunLifecycle runs the daily membership job for every property.
func (m *Module) RunLifecycle(ctx context.Context) (LifecycleResult, error) {
	var res LifecycleResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active'`)
		if err != nil {
			return err
		}
		var props []uuid.UUID
		for rows.Next() {
			var p uuid.UUID
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			props = append(props, p)
		}
		rows.Close()
		for _, p := range props {
			pctx := withProperty(ctx, p)
			day := today(pctx, tx, p)
			ex, err := tx.Query(pctx, `UPDATE membership.memberships SET status = 'expired' WHERE property_id = $1 AND status = 'active'
				AND ends_on < $2::date RETURNING id, member_id`, p, day.Format("2006-01-02"))
			if err != nil {
				return err
			}
			type pair struct{ ms, member uuid.UUID }
			var list []pair
			for ex.Next() {
				var x pair
				if err := ex.Scan(&x.ms, &x.member); err != nil {
					ex.Close()
					return err
				}
				list = append(list, x)
			}
			ex.Close()
			for _, x := range list {
				ms := x.ms
				if err := history(pctx, tx, p, x.member, &ms, "expired", "active", "expired", nil); err != nil {
					return err
				}
				if _, err := tx.Exec(pctx, `UPDATE membership.members SET status = 'expired' WHERE id = $1 AND NOT EXISTS
					(SELECT 1 FROM membership.memberships WHERE member_id = $1 AND status = 'active')`, x.member); err != nil {
					return err
				}
				res.Expired++
			}
			for _, days := range RenewalReminderDays {
				target := day.AddDate(0, 0, days).Format("2006-01-02")
				rr, err := tx.Query(pctx, `SELECT ms.id, ms.ends_on, mb.code, mb.customer_id, t.name FROM membership.memberships ms
					JOIN membership.members mb ON mb.id = ms.member_id JOIN membership.types t ON t.id = ms.type_id
					WHERE ms.property_id = $1 AND ms.status = 'active' AND ms.role = 'principal' AND ms.ends_on = $2::date
					AND NOT EXISTS (SELECT 1 FROM membership.reminders r WHERE r.membership_id = ms.id AND r.days_before = $3 AND r.ends_on = ms.ends_on)`,
					p, target, days)
				if err != nil {
					return err
				}
				type rem struct {
					ms       uuid.UUID
					ends     time.Time
					code     string
					customer *uuid.UUID
					typeName string
				}
				var rl []rem
				for rr.Next() {
					var x rem
					if err := rr.Scan(&x.ms, &x.ends, &x.code, &x.customer, &x.typeName); err != nil {
						rr.Close()
						return err
					}
					rl = append(rl, x)
				}
				rr.Close()
				for _, x := range rl {
					if x.customer != nil {
						c, err := crm.GetCustomer(pctx, tx, *x.customer)
						if err == nil {
							if err := m.notifyCustomer(pctx, tx, c, "membership.renewal_reminder", map[string]any{"name": c.Name, "memberNo": x.code,
								"type": x.typeName, "endsOn": x.ends.Format("02 Jan 2006"), "daysLeft": days}); err != nil {
								return err
							}
						}
					}
					if _, err := tx.Exec(pctx, `INSERT INTO membership.reminders (membership_id, property_id, days_before, ends_on) VALUES ($1,$2,$3,$4)`,
						x.ms, p, days, x.ends); err != nil {
						return err
					}
					res.Reminders++
				}
			}
		}
		if res.Expired > 0 || res.Reminders > 0 {
			slog.InfoContext(ctx, "membership lifecycle", "expired", res.Expired, "reminders", res.Reminders)
			return audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "lifecycle_run", Category: audit.CategorySystem,
				EntityType: "membership.membership", ActorName: "membership job", Metadata: map[string]any{"expired": res.Expired, "reminders": res.Reminders}})
		}
		return nil
	})
	return res, err
}

// RegisterJobs adds the daily lifecycle job.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &LifecycleWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 30, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return LifecycleArgs{}, nil }, nil))
}

// Register adds the membership routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{Members, Programs, Types, Packages} {
		eng.Register(reg, d)
	}
	add := func(rt route.Route) {
		rt.Module = "membership"
		if rt.Scope == route.ScopeGlobal && rt.Auth == route.AuthRequired && !strings.HasPrefix(rt.Path, "/api/v1/member/") {
			rt.Scope = route.ScopeProperty
		}
		reg.Add(rt)
	}
	const ta, tm = "Applications", "Members"
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/applications", Tag: ta, Summary: "Membership applications",
		Permission: "membership.application.view", Response: Application{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[customerId]"}},
		Handler: m.listApplications})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/applications", Tag: ta, Summary: "Create a membership application (draft)",
		Permission: "membership.application.create", Request: ApplicationRequest{}, Response: Application{}, Idempotent: true, Handler: m.createApplication("back_office")})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/applications/{id}", Tag: ta, Summary: "View a membership application",
		Permission: "membership.application.view", Response: Application{}, Handler: m.getApplication})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/membership/applications/{id}", Tag: ta, Summary: "Edit a draft application",
		Permission: "membership.application.create", Request: ApplicationPatch{}, Response: Application{}, Handler: m.patchApplication})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/applications/{id}:check-eligibility", Tag: ta, Summary: "Eligibility Check",
		Permission: "membership.application.create", Response: Application{}, Status: http.StatusOK, Handler: m.checkApplication})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/applications/{id}:submit", Tag: ta, Summary: "Submit for Membership Approval",
		Permission: "membership.application.submit", Response: Application{}, Status: http.StatusOK, Handler: m.submitApplication})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/applications/{id}:cancel", Tag: ta, Summary: "Cancel an application",
		Permission: "membership.application.create", Request: billing.ReasonRequest{}, Response: Application{}, Status: http.StatusOK, Handler: m.cancelApplication})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/applications/{id}:activate", Tag: ta, Summary: "Activate Membership (fee paid, or waived with permission)",
		Permission: "membership.application.activate", Request: ActivateRequest{}, Response: Application{}, Status: http.StatusOK, Handler: m.activateApplication})

	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/memberships", Tag: tm, Summary: "Memberships (Renewals: expiringWithinDays)",
		Permission: "membership.membership.view", Response: Membership{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[memberId]"}, {Name: "expiringWithinDays", Type: "integer"}}, Handler: m.listMembershipsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:renew", Tag: tm, Summary: "Renew Membership",
		Permission: "membership.membership.renew", Request: RenewRequest{}, Response: RenewResult{}, Handler: m.renewHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/members/{id}/profile", Tag: tm, Summary: "Member Profile (memberships, family, cards, account, history)",
		Permission: "membership.member.view", Response: MemberProfile{}, Handler: m.profileHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/members/{id}:invite", Tag: tm, Summary: "Send the Member Portal activation link",
		Permission: "membership.member.update", Response: MemberProfile{}, Status: http.StatusOK, Handler: m.inviteMember})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/cards", Tag: tm, Summary: "Membership Cards", Permission: "membership.card.view",
		Response: Card{}, List: true, Query: []route.Param{{Name: "filter[memberId]"}}, Handler: m.listCardsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/cards", Tag: tm, Summary: "Issue a member card (physical or digital)",
		Permission: "membership.card.issue", Request: IssueCardRequest{}, Response: Card{}, Handler: m.issueCardHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/cards/{id}:deactivate", Tag: tm, Summary: "Deactivate a card",
		Permission: "membership.card.issue", Request: billing.ReasonRequest{}, Response: Card{}, Status: http.StatusOK, Handler: m.deactivateCard})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/history", Tag: tm, Summary: "Membership History", Permission: "membership.membership.view",
		Response: HistoryEntry{}, List: true, Query: []route.Param{{Name: "memberId"}}, Handler: m.historyHTTP})

	// Public & Member Portal
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/membership/activate", Module: "membership", Tag: "Member Portal",
		Summary: "Request the Member Portal activation link (member number + e-mail + date of birth)", Auth: route.AuthPublic,
		Request: PublicActivateRequest{}, Response: map[string]string{}, Status: http.StatusAccepted, Handler: m.publicActivate})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/membership", Module: "membership", Tag: "Member Portal",
		Summary: "My Membership, Digital Member Card, Benefits and Family Members", Permission: catalog.ShellMemberPortal, Response: MyMembership{}, Handler: m.myMembership})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/membership-applications", Module: "membership", Tag: "Member Portal",
		Summary: "My membership applications", Permission: catalog.ShellMemberPortal, Response: Application{}, List: true, Handler: m.myApplications})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/member/membership-applications", Module: "membership", Tag: "Member Portal",
		Summary: "Apply for a membership from the Member Portal", Permission: catalog.ShellMemberPortal, Request: ApplicationRequest{}, Response: Application{},
		Handler: m.createApplication("member_portal")})
}

func iamPortalUser(c crm.Customer, property uuid.UUID) iam.PortalUser {
	return iam.PortalUser{Email: c.Email, Name: c.Name, Phone: c.Phone, RoleCode: "member", PropertyID: property}
}
