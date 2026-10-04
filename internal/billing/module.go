// Package billing is the Billing & Payment module. P0 owns the Payment
// Methods master (FR-MD-03) and its effective-dated availability per
// property/outlet (FR-MD-08); payment processing arrives in P1.
package billing

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

// MethodTypes follow Naming Convention §18.
var MethodTypes = []string{"cash", "bank_transfer", "virtual_account", "qris", "card", "payment_gateway", "member_account", "voucher_prepaid"}

// PaymentMethods is the instance-wide payment method master.
var PaymentMethods = &resource.Def{
	Key: "billing.payment_method", Module: "billing", Perm: "billing.payment_method", Path: "/api/v1/billing/payment-methods",
	Table: "billing.payment_methods", Name: "Payment Method", Plural: "Payment Methods", Tag: "Payment Methods", Archive: true,
	CodeField: "code", OrderBy: "sort_order, name, id",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true,
			Pattern: regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`), PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true},
		{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 80, Search: true},
		{Name: "methodType", Column: "method_type", Label: "Method Type", Kind: resource.Enum, Enum: MethodTypes, Required: true, Filter: true},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 500},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
		resource.Status("active", "inactive"),
	},
}

// Setting is one effective-dated availability version.
type Setting struct {
	ID               uuid.UUID  `json:"id"`
	PaymentMethodID  uuid.UUID  `json:"paymentMethodId"`
	PaymentMethod    string     `json:"paymentMethod"`
	MethodType       string     `json:"methodType"`
	OutletID         *uuid.UUID `json:"outletId"`
	Enabled          bool       `json:"enabled"`
	SurchargePercent string     `json:"surchargePercent"`
	EffectiveFrom    time.Time  `json:"effectiveFrom"`
	CreatedAt        time.Time  `json:"createdAt"`
}

type SettingRequest struct {
	PaymentMethodID  uuid.UUID  `json:"paymentMethodId"`
	OutletID         *uuid.UUID `json:"outletId,omitempty"`
	Enabled          bool       `json:"enabled"`
	SurchargePercent string     `json:"surchargePercent" doc:"Decimal 0–100"`
	EffectiveFrom    *time.Time `json:"effectiveFrom,omitempty" doc:"Defaults to now; must not be in the past once a version exists"`
}

// Module serves payment method settings.
type Module struct{ DB *dbtx.DB }

// Effective returns, per payment method (and outlet), the version in force
// at time at — changes never rewrite earlier versions (FR-MD-08).
func Effective(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) ([]Setting, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (s.payment_method_id, s.outlet_id) s.id, s.payment_method_id, pm.name, pm.method_type,
		s.outlet_id, s.enabled, s.surcharge_percent::text, s.effective_from, s.created_at
		FROM billing.payment_method_settings s JOIN billing.payment_methods pm ON pm.id = s.payment_method_id
		WHERE s.property_id = $1 AND s.effective_from <= $2
		ORDER BY s.payment_method_id, s.outlet_id, s.effective_from DESC`, property, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Setting{}
	for rows.Next() {
		var s Setting
		if err := rows.Scan(&s.ID, &s.PaymentMethodID, &s.PaymentMethod, &s.MethodType, &s.OutletID, &s.Enabled, &s.SurchargePercent,
			&s.EffectiveFrom, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	at := clock.Now()
	if v := r.URL.Query().Get("at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("invalid_at", "at must be RFC 3339"))
			return
		}
		at = t
	}
	var out []Setting
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		if r.URL.Query().Get("history") == "true" {
			rows, err := tx.Query(ctx, `SELECT s.id, s.payment_method_id, pm.name, pm.method_type, s.outlet_id, s.enabled, s.surcharge_percent::text,
				s.effective_from, s.created_at FROM billing.payment_method_settings s JOIN billing.payment_methods pm ON pm.id = s.payment_method_id
				WHERE s.property_id = $1 ORDER BY pm.sort_order, s.effective_from DESC`, pid)
			if err != nil {
				return err
			}
			defer rows.Close()
			out = []Setting{}
			for rows.Next() {
				var s Setting
				if err := rows.Scan(&s.ID, &s.PaymentMethodID, &s.PaymentMethod, &s.MethodType, &s.OutletID, &s.Enabled, &s.SurchargePercent,
					&s.EffectiveFrom, &s.CreatedAt); err != nil {
					return err
				}
				out = append(out, s)
			}
			return rows.Err()
		}
		out, err = Effective(ctx, tx, pid, at)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Setting]{Items: out})
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	var req SettingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.SurchargePercent == "" {
		req.SurchargePercent = "0"
	}
	pct, err := decimal.NewFromString(req.SurchargePercent)
	if err != nil || pct.IsNegative() || pct.GreaterThan(decimal.NewFromInt(100)) {
		httpx.WriteError(w, r, errs.Validation("invalid_surcharge", "invalid surcharge", errs.Field("surchargePercent", "invalid", "decimal between 0 and 100")))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	eff := clock.Now()
	if req.EffectiveFrom != nil {
		eff = req.EffectiveFrom.UTC()
	}
	var out Setting
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.payment_method_settings WHERE payment_method_id = $1 AND property_id = $2
			AND outlet_id IS NOT DISTINCT FROM $3)`, req.PaymentMethodID, pid, req.OutletID).Scan(&exists); err != nil {
			return err
		}
		if exists && eff.Before(clock.Now().Add(-time.Minute)) {
			return errs.Validation("effective_date_past", "a new version cannot take effect in the past",
				errs.Field("effectiveFrom", "past", "existing transactions must not change; choose now or a future date"))
		}
		sid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO billing.payment_method_settings (id, payment_method_id, property_id, outlet_id, enabled,
			surcharge_percent, effective_from, created_by) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8)`,
			sid, req.PaymentMethodID, pid, req.OutletID, req.Enabled, pct.String(), eff, id.Ptr(authz.From(ctx).UserID)); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("invalid_reference", "payment method or outlet not found")
			}
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return errs.Conflict("duplicate_version", "a version with this effective date already exists")
			}
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT s.id, s.payment_method_id, pm.name, pm.method_type, s.outlet_id, s.enabled, s.surcharge_percent::text,
			s.effective_from, s.created_at FROM billing.payment_method_settings s JOIN billing.payment_methods pm ON pm.id = s.payment_method_id
			WHERE s.id = $1`, sid).Scan(&out.ID, &out.PaymentMethodID, &out.PaymentMethod, &out.MethodType, &out.OutletID, &out.Enabled,
			&out.SurchargePercent, &out.EffectiveFrom, &out.CreatedAt); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.payment_method_setting",
			EntityID: sid.String(), EntityLabel: out.PaymentMethod, PropertyID: &pid, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// Register adds the routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	eng.Register(reg, PaymentMethods)
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/payment-method-settings", Module: "billing", Tag: "Payment Methods",
		Summary: "Payment method availability in force at a time (or full history)", Permission: "billing.payment_method.view",
		Scope: route.ScopeProperty, Response: Setting{}, List: true,
		Query: []route.Param{{Name: "at", Description: "RFC 3339 time; default now"}, {Name: "history", Type: "boolean"}}, Handler: m.list})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payment-method-settings", Module: "billing", Tag: "Payment Methods",
		Summary: "Add an effective-dated availability version", Permission: "billing.payment_method_setting.manage",
		Scope: route.ScopeProperty, Request: SettingRequest{}, Response: Setting{}, Handler: m.create})
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	perms := append(resource.Permissions(PaymentMethods), catalog.P("billing", "payment_method_setting", "manage")...)
	perms = append(perms, catalog.P("billing", "folio", "view", "create", "add_charge", "void", "close", "reopen")...)
	perms = append(perms, catalog.P("billing", "payment", "view", "create")...)
	perms = append(perms, catalog.P("billing", "refund", "view", "create")...)
	perms = append(perms, catalog.P("billing", "customer_account", "view", "manage", "adjust")...)
	perms = append(perms, catalog.P("billing", "reconciliation", "view", "manage")...)
	perms = append(perms, catalog.P("billing", "statement", "generate")...)
	perms = append(perms, catalog.P("billing", "accounting_export", "create")...)
	finance := []string{"billing.folio.view", "billing.folio.create", "billing.folio.add_charge", "billing.folio.void", "billing.folio.close",
		"billing.folio.reopen", "billing.payment.view", "billing.payment.create", "billing.refund.view", "billing.refund.create",
		"billing.customer_account.view", "billing.customer_account.manage", "billing.customer_account.adjust", "billing.reconciliation.view",
		"billing.reconciliation.manage", "billing.statement.generate", "billing.accounting_export.create"}
	desk := []string{"billing.folio.view", "billing.folio.create", "billing.folio.add_charge", "billing.folio.close", "billing.payment.view",
		"billing.payment.create", "billing.refund.view", "billing.customer_account.view", "billing.payment_method.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":     append([]string{"billing.payment_method.view", "billing.payment_method.export", "billing.payment_method_setting.manage"}, finance...),
			"finance_manager":    append([]string{"billing.payment_method.view", "billing.payment_method_setting.manage"}, finance...),
			"accountant":         {"billing.payment_method.view", "billing.folio.view", "billing.payment.view", "billing.refund.view", "billing.customer_account.view", "billing.reconciliation.view", "billing.reconciliation.manage", "billing.accounting_export.create"},
			"general_manager":    {"billing.folio.view", "billing.payment.view", "billing.refund.view", "billing.customer_account.view", "billing.reconciliation.view"},
			"front_desk":         desk,
			"reservation_staff":  desk,
			"cashier":            desk,
			"golf_manager":       {"billing.folio.view", "billing.payment.view", "billing.customer_account.view"},
			"membership_admin":   {"billing.folio.view", "billing.payment.view", "billing.payment.create", "billing.customer_account.view", "billing.customer_account.manage"},
			"membership_manager": {"billing.folio.view", "billing.payment.view", "billing.customer_account.view", "billing.customer_account.manage", "billing.statement.generate"},
		},
	}
}
