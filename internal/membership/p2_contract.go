package membership

// Wiring of PRD P2 membership lifecycle (membership/lifecycle sub-part,
// PRD P2 §5.4.1) next to P1's membership: routes, jobs, catalogue, the P2
// fields of P1's resources and the reaction to P1's activation event. P1
// files are not changed. Review: P1 developer.

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/docno"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/resource"
)

// P2 fields on P1's membership resources (additive; expand columns of
// membership/00003).
var (
	p2TypeFields = []resource.Field{
		{Name: "entitlements", Column: "entitlements", Label: "Entitlements per line (FR-MBL-05)", Kind: resource.JSON, Default: "{}"},
		{Name: "annualFee", Column: "annual_fee", Label: "Annual Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "graceDays", Column: "grace_days", Label: "Grace Period (days)", Kind: resource.Int, Default: int64(30), Min: resource.Min(0)},
		{Name: "rank", Column: "rank", Label: "Rank (upgrade order)", Kind: resource.Int, Default: int64(0)},
		{Name: "cardReplacementFee", Column: "card_replacement_fee", Label: "Card Replacement Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "reactivationFee", Column: "reactivation_fee", Label: "Reactivation Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "nomineeChangeFee", Column: "nominee_change_fee", Label: "Nominee Replacement Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
	}
	p2TypeCategories = []string{"couple", "senior", "student", "junior", "residence", "bulk_entrance", "monthly", "other"}
	p2MemberStatuses = []string{"paused", "cancelled"}
)

func init() {
	for i := range Types.Fields {
		if Types.Fields[i].Name == "category" {
			Types.Fields[i].Enum = append(Types.Fields[i].Enum, p2TypeCategories...)
		}
	}
	// before Description / Status: append keeps the P1 order, the UI orders by label
	Types.Fields = append(Types.Fields, p2TypeFields...)
	for i := range Members.Fields {
		if Members.Fields[i].Name == "status" {
			Members.Fields[i].Enum = append(Members.Fields[i].Enum, p2MemberStatuses...)
		}
	}
}

// RegisterP2 adds the P2 membership routes (wired by internal/app).
func (m *Module) RegisterP2(reg *route.Registry) {
	m.registerLifecycle(reg)
	m.registerMe(reg)
	m.registerPublic(reg)
}

// RegisterP2Jobs adds the daily P2 lifecycle job (annual fee, pause end,
// suspension, age limit).
func (m *Module) RegisterP2Jobs(reg *jobs.Registrar) { m.registerDailyJob(reg) }

// P2Contribution is the P2 part of the membership catalogue.
func P2Contribution() catalog.Contribution { return p2Contribution() }

// OnActivated sets the first annual fee due date of a new membership (a
// year after the start; the first period is paid with the package fee,
// FR-MBL-04). Subscriber of P1's membership.activated.
func (m *Module) OnActivated(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		MembershipID uuid.UUID `json:"membershipId"`
	}
	if err := e.Decode(&p); err != nil || p.MembershipID == uuid.Nil || e.PropertyID == nil {
		return nil //nolint:nilerr // foreign payload
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID)
	_, err := tx.Exec(ctx, `UPDATE membership.memberships ms SET next_fee_due = ms.starts_on + interval '1 year' FROM membership.types t
		WHERE ms.id = $1 AND t.id = ms.type_id AND t.annual_fee > 0 AND ms.next_fee_due IS NULL`, p.MembershipID)
	return err
}

// NewApplication creates a draft application with its eligibility check
// for the P2 channels (Member Portal, website — FR-MBL-15); same rules as
// P1's back-office application.
func (m *Module) NewApplication(ctx context.Context, tx pgx.Tx, property uuid.UUID, channel string, req ApplicationRequest) (Application, error) {
	var out Application
	if err := validDeps(req.Dependents); err != nil {
		return out, err
	}
	ok, err := crm.ExistsInProperty(ctx, tx, property, req.CustomerID)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, errs.Validation("invalid_customer", "customer not found", errs.Field("customerId", "not_found", "customer not found in this property"))
	}
	var pkgType uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT type_id FROM membership.packages WHERE id = $1 AND property_id = $2 AND status = 'active'`, req.PackageID, property).Scan(&pkgType); err != nil {
		return out, errs.Validation("invalid_package", "package not found", errs.Field("packageId", "not_found", "package not found"))
	}
	if pkgType != req.TypeID {
		return out, errs.Validation("invalid_package", "the package belongs to another membership type", errs.Field("packageId", "invalid", "package of the chosen type"))
	}
	num, _, err := docno.Running(ctx, tx, "membership.sequences", property, "APP")
	if err != nil {
		return out, err
	}
	deps, _ := json.Marshal(nonNil(req.Dependents))
	docs, _ := json.Marshal(nonNilDocs(req.Documents))
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO membership.applications (id, property_id, number, channel, customer_id, type_id, package_id, corporate_account_id,
		dependents, documents, status, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'draft',$11,$12,$12)`,
		aid, property, num, channel, req.CustomerID, req.TypeID, req.PackageID, req.CorporateAccountID, deps, docs, nullStr(req.Notes), id.Ptr(actor(ctx))); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return out, errs.Validation("invalid_reference", "membership type or corporate account not found")
		}
		return out, err
	}
	if out, err = GetApplication(ctx, tx, aid); err != nil {
		return out, err
	}
	if _, err := m.CheckEligibility(ctx, tx, out); err != nil {
		return out, err
	}
	if out, err = GetApplication(ctx, tx, aid); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: audit.ActionCreate, EntityType: "membership.application", EntityID: aid.String(),
		EntityLabel: num, PropertyID: &property, After: out})
}
