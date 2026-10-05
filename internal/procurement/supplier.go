package procurement

// PRD P4 EP-10 Supplier Management: contacts, addresses, bank accounts
// (masked without procurement.supplier_bank_account.view_sensitive),
// documents, supplier items & price lists (FR-SUP-01/02) and the supplier
// blacklist with reason and approval (FR-SUP-04). Suppliers are imported
// through the P0 Master Data Import (entity procurement.supplier, EP-29).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

func supplierRef() *resource.Ref {
	return &resource.Ref{Table: "procurement.suppliers", SameProperty: true, Label: "supplier"}
}

func supplierField() resource.Field {
	return resource.Field{Name: "supplierId", Column: "supplier_id", Label: "Supplier", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
		Ref: supplierRef()}
}

var SupplierContacts = &resource.Def{
	Key: "procurement.supplier_contact", Module: "procurement", Perm: "procurement.supplier", Path: "/api/v1/procurement/supplier-contacts",
	Table: "procurement.supplier_contacts", Name: "Supplier Contact", Plural: "Supplier Contacts", Tag: "Suppliers", PropertyScoped: true,
	OrderBy: "supplier_id, is_primary DESC, name, id",
	Fields: []resource.Field{supplierField(),
		{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 160, Search: true},
		{Name: "position", Column: "position", Label: "Position", Kind: resource.String, Max: 120},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
		{Name: "isPrimary", Column: "is_primary", Label: "Primary Contact", Kind: resource.Bool, Default: false},
		{Name: "receivesOrders", Column: "receives_orders", Label: "Receives RFQ / PO", Kind: resource.Bool, Default: true},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000}},
}

var SupplierAddresses = &resource.Def{
	Key: "procurement.supplier_address", Module: "procurement", Perm: "procurement.supplier", Path: "/api/v1/procurement/supplier-addresses",
	Table: "procurement.supplier_addresses", Name: "Supplier Address", Plural: "Supplier Addresses", Tag: "Suppliers", PropertyScoped: true,
	OrderBy: "supplier_id, is_primary DESC, created_at, id",
	Fields: []resource.Field{supplierField(),
		{Name: "addressType", Column: "address_type", Label: "Type", Kind: resource.Enum, Enum: []string{"office", "billing", "warehouse", "pickup"},
			Default: "office", Filter: true},
		{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Required: true, Max: 500},
		{Name: "city", Column: "city", Label: "City", Kind: resource.String, Max: 120},
		{Name: "province", Column: "province", Label: "Province", Kind: resource.String, Max: 120},
		{Name: "postalCode", Column: "postal_code", Label: "Postal Code", Kind: resource.String, Max: 20},
		{Name: "isPrimary", Column: "is_primary", Label: "Primary Address", Kind: resource.Bool, Default: false}},
}

var SupplierBankAccounts = &resource.Def{
	Key: "procurement.supplier_bank_account", Module: "procurement", Perm: "procurement.supplier_bank_account",
	Path: "/api/v1/procurement/supplier-bank-accounts", Table: "procurement.supplier_bank_accounts", Name: "Supplier Bank Account",
	Plural: "Supplier Bank Accounts", Tag: "Suppliers", PropertyScoped: true, OrderBy: "supplier_id, is_primary DESC, created_at, id",
	Fields: []resource.Field{supplierField(),
		{Name: "bankName", Column: "bank_name", Label: "Bank", Kind: resource.String, Required: true, Max: 120},
		{Name: "branch", Column: "branch", Label: "Branch", Kind: resource.String, Max: 120},
		{Name: "accountNumber", Column: "account_number", Label: "Account Number", Kind: resource.String, Required: true, Max: 40},
		{Name: "accountName", Column: "account_name", Label: "Account Holder", Kind: resource.String, Required: true, Max: 160},
		{Name: "currency", Column: "currency", Label: "Currency", Kind: resource.String, Max: 3, Upper: true, Default: "IDR", Pattern: currencyRe,
			PatternMsg: "ISO 4217 code, e.g. IDR"},
		{Name: "isPrimary", Column: "is_primary", Label: "Primary Account", Kind: resource.Bool, Default: false},
		resource.Status("active", "inactive")},
	Hooks: resource.Hooks{AfterRead: maskBankAccount},
}

var SupplierDocuments = &resource.Def{
	Key: "procurement.supplier_document", Module: "procurement", Perm: "procurement.supplier", Path: "/api/v1/procurement/supplier-documents",
	Table: "procurement.supplier_documents", Name: "Supplier Document", Plural: "Supplier Documents", Tag: "Suppliers", PropertyScoped: true,
	OrderBy: "supplier_id, document_type, created_at, id",
	Fields: []resource.Field{supplierField(),
		{Name: "documentType", Column: "document_type", Label: "Document Type", Kind: resource.Enum,
			Enum: []string{"npwp", "nib", "siup", "sppkp", "contract", "bank_letter", "certificate", "other"}, Default: "other", Filter: true},
		{Name: "documentNo", Column: "document_no", Label: "Document No.", Kind: resource.String, Max: 80, Search: true},
		{Name: "fileId", Column: "file_id", Label: "File", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.files", Label: "file"}},
		{Name: "validUntil", Column: "valid_until", Label: "Valid Until", Kind: resource.Date},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000}},
}

// SupplierItems are the supplier items & price lists (FR-SUP-02).
var SupplierItems = &resource.Def{
	Key: "procurement.supplier_item", Module: "procurement", Perm: "procurement.supplier_item", Path: "/api/v1/procurement/supplier-items",
	Table: "procurement.supplier_items", Name: "Supplier Item", Plural: "Supplier Items & Price Lists", Tag: "Suppliers", PropertyScoped: true,
	OrderBy: "supplier_id, item_id, valid_from NULLS FIRST, id",
	Fields: []resource.Field{supplierField(),
		{Name: "itemId", Column: "item_id", Label: "Item", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "supplierItemCode", Column: "supplier_item_code", Label: "Supplier Item Code", Kind: resource.String, Max: 80, Search: true},
		{Name: "uomId", Column: "uom_id", Label: "Purchase UOM", Kind: resource.UUID, Required: true, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "unitPrice", Column: "unit_price", Label: "Unit Price", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "currency", Column: "currency", Label: "Currency", Kind: resource.String, Max: 3, Upper: true, Default: "IDR", Pattern: currencyRe,
			PatternMsg: "ISO 4217 code, e.g. IDR"},
		{Name: "minOrderQuantity", Column: "min_order_quantity", Label: "Minimum Order Quantity", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "leadTimeDays", Column: "lead_time_days", Label: "Lead Time (days)", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To", Kind: resource.Date},
		{Name: "preferred", Column: "preferred", Label: "Preferred Supplier", Kind: resource.Bool, Default: false, Filter: true},
		{Name: "lastPrice", Column: "last_price", Label: "Last Purchase Price", Kind: resource.Decimal, ReadOnly: true},
		{Name: "lastPriceAt", Column: "last_price_at", Label: "Last Purchased", Kind: resource.Timestamp, ReadOnly: true},
		resource.Status("active", "inactive")},
}

// SupplierDefs are the P4 supplier master data resources.
var SupplierDefs = []*resource.Def{SupplierContacts, SupplierAddresses, SupplierBankAccounts, SupplierDocuments, SupplierItems}

// BankSensitivePermission shows full bank account numbers.
const BankSensitivePermission = "procurement.supplier_bank_account.view_sensitive"

func canSee(ctx context.Context, permission string) bool {
	p := authz.From(ctx)
	if p == nil {
		return false
	}
	if pid, ok := reqctx.Property(ctx); ok {
		return p.Can(permission, &pid)
	}
	return p.Can(permission, nil)
}

func maskBankAccount(ctx context.Context, row map[string]any) {
	if s, ok := row["accountNumber"].(string); ok && s != "" && !canSee(ctx, BankSensitivePermission) {
		row["accountNumber"] = mask.Phone(s)
	}
}

// maskSupplierNPWP masks the NPWP for viewers without the sensitive
// permission (PRD P4 §12 Security).
func maskSupplierNPWP(ctx context.Context, row map[string]any) {
	if s, ok := row["npwp"].(string); ok && s != "" && !canSee(ctx, BankSensitivePermission) {
		row["npwp"] = mask.Phone(s)
	}
}

// supplierPriceFor returns the valid price-list price of an item from a
// supplier in a UOM (nil when there is none).
func supplierPriceFor(ctx context.Context, q dbtx.Querier, supplier, item, uom uuid.UUID, on time.Time) (*string, error) {
	var p *string
	err := q.QueryRow(ctx, `SELECT trim_scale(unit_price)::text FROM procurement.supplier_items WHERE supplier_id = $1 AND item_id = $2 AND uom_id = $3
		AND status = 'active' AND (valid_from IS NULL OR valid_from <= $4) AND (valid_to IS NULL OR valid_to >= $4)
		ORDER BY valid_from DESC NULLS LAST, created_at DESC LIMIT 1`, supplier, item, uom, on).Scan(&p)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	return p, err
}

// ── block / unblock (FR-SUP-04) ──────────────────────────────────────────

// ProcurementSupplierStatusInput carries the reason of a block / unblock.
type ProcurementSupplierStatusInput struct {
	Reason string `json:"reason"`
}

// ProcurementSupplierStatusRequest is a block / unblock request.
type ProcurementSupplierStatusRequest struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	SupplierID        uuid.UUID  `json:"supplierId" db:"supplier_id"`
	Action            string     `json:"action" db:"action" enum:"block,unblock"`
	Reason            string     `json:"reason" db:"reason"`
	Status            string     `json:"status" db:"status" enum:"pending,approved,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	SupplierStatus    string     `json:"supplierStatus" db:"supplier_status" enum:"active,inactive,blocked"`
	DecidedAt         *time.Time `json:"decidedAt" db:"decided_at"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

const statusRequestSelect = `SELECT r.id, r.supplier_id, r.action, r.reason, r.status, r.approval_request_id, s.status AS supplier_status, r.decided_at,
	r.created_at FROM procurement.supplier_status_requests r JOIN procurement.suppliers s ON s.id = r.supplier_id`

// RequestSupplierStatus asks to block or unblock a supplier.
func (m *Module) RequestSupplierStatus(ctx context.Context, tx pgx.Tx, sid uuid.UUID, action string, in ProcurementSupplierStatusInput) (ProcurementSupplierStatusRequest, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return ProcurementSupplierStatusRequest{}, err
	}
	property := propertyOf(ctx)
	s, err := loadSupplier(ctx, tx, sid)
	if err != nil {
		return ProcurementSupplierStatusRequest{}, err
	}
	if action == "block" && s.Status == "blocked" {
		return ProcurementSupplierStatusRequest{}, errs.Conflict("already_blocked", "the supplier is already blocked")
	}
	if action == "unblock" && s.Status != "blocked" {
		return ProcurementSupplierStatusRequest{}, errs.Conflict("not_blocked", "the supplier is not blocked")
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.supplier_status_requests (id, property_id, supplier_id, action, reason, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6)`, rid, property, sid, action, strings.TrimSpace(in.Reason), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return ProcurementSupplierStatusRequest{}, errs.Conflict("request_pending", "a block / unblock request is already pending")
		}
		return ProcurementSupplierStatusRequest{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: action + "_requested", EntityType: "procurement.supplier",
		EntityID: sid.String(), EntityLabel: s.Code + " · " + s.Name, PropertyID: &property, Reason: in.Reason}); err != nil {
		return ProcurementSupplierStatusRequest{}, err
	}
	areq, _, err := m.submitApproval(ctx, tx, SupplierStatusDocumentType, rid, property, s.Code, strings.ToUpper(action[:1])+action[1:]+" supplier "+s.Name,
		map[string]any{"action": action})
	if err != nil {
		return ProcurementSupplierStatusRequest{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.supplier_status_requests SET approval_request_id = $2 WHERE id = $1`, rid, areq); err != nil {
		return ProcurementSupplierStatusRequest{}, err
	}
	return oneOf[ProcurementSupplierStatusRequest]("supplier status request")(tx.Query(ctx, statusRequestSelect+` WHERE r.id = $1`, rid))
}

func (m *Module) supplierStatusDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var sid uuid.UUID
	var action, reason, status string
	if err := tx.QueryRow(ctx, `SELECT supplier_id, action, reason, status FROM procurement.supplier_status_requests WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&sid, &action, &reason, &status); err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	if _, err := tx.Exec(ctx, `UPDATE procurement.supplier_status_requests SET status = $2, decided_at = now() WHERE id = $1`, d.DocumentID, st); err != nil {
		return err
	}
	if st != "approved" {
		return nil
	}
	var before string
	if err := tx.QueryRow(ctx, `SELECT status FROM procurement.suppliers WHERE id = $1 FOR UPDATE`, sid).Scan(&before); err != nil {
		return err
	}
	after := "blocked"
	if action == "unblock" {
		after = "active"
		_, err := tx.Exec(ctx, `UPDATE procurement.suppliers SET status = 'active', blocked_reason = NULL, blocked_at = NULL WHERE id = $1`, sid)
		if err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE procurement.suppliers SET status = 'blocked', blocked_reason = $2, blocked_at = now() WHERE id = $1`, sid, reason); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionStatusChange, EntityType: "procurement.supplier",
		EntityID: sid.String(), PropertyID: &d.PropertyID, Reason: reason, Before: map[string]any{"status": before}, After: map[string]any{"status": after}})
}

func (m *Module) registerSuppliers(reg *route.Registry, eng *resource.Engine) {
	for _, d := range SupplierDefs {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", "Suppliers", route.ScopeProperty
		reg.Add(rt)
	}
	for _, action := range []string{"block", "unblock"} {
		action := action
		summary := "Block Supplier (blacklist; reason and approval)"
		if action == "unblock" {
			summary = "Unblock Supplier (reason and approval)"
		}
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/procurement/suppliers/{id}:" + action, Summary: summary,
			Permission: "procurement.supplier.block", Request: ProcurementSupplierStatusInput{}, Response: ProcurementSupplierStatusRequest{},
			Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ProcurementSupplierStatusInput) (ProcurementSupplierStatusRequest, error) {
				sid, err := handle.ID(r)
				if err != nil {
					return ProcurementSupplierStatusRequest{}, err
				}
				return m.RequestSupplierStatus(ctx, tx, sid, action, in)
			})})
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/procurement/supplier-status-requests", Summary: "Supplier block / unblock requests",
		Permission: "procurement.supplier.view", Response: ProcurementSupplierStatusRequest{}, List: true,
		Query: []route.Param{{Name: "filter[supplierId]"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ProcurementSupplierStatusRequest], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[ProcurementSupplierStatusRequest](tx.Query(ctx, statusRequestSelect+` WHERE r.property_id = $1
				AND ($2 = '' OR r.supplier_id::text = $2) AND ($3 = '' OR r.status = $3) ORDER BY r.created_at DESC LIMIT $4`,
				handle.Property(ctx), lp.Filters["supplierId"], lp.Filters["status"], lp.Limit)))
		})})
}
