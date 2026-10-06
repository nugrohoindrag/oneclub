package procurement

// Demo data of procurement (Modern Golf, MAIN property; idempotent):
// suppliers with contacts and the approval workflows of the procurement
// documents following PRD P4 §16 #10 (Procurement Manager, then Finance
// Manager above the first tier and General Manager above the second).

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/id"
)

type demoSupplier struct {
	code, name, email, phone, address, withholding string
	categories                                     []string
	pkp, contract                                  bool
	lead                                           int
	contact, position                              string
}

var demoSuppliers = []demoSupplier{
	{"SUP-SINAR", "PT Sinar Pangan Sejahtera", "purchasing@sinarpangan.test", "+62215550101", "Jl. Daan Mogot 88, Tangerang", "none",
		[]string{"F&B", "Banquet"}, true, false, 2, "Rudi Hartono", "Sales Manager"},
	{"SUP-MITRA", "CV Mitra Amenities", "sales@mitraamenities.test", "+62215550102", "Jl. Gatot Subroto 12, Tangerang", "none",
		[]string{"Bungalow", "General Supplies"}, true, false, 3, "Lina Wijaya", "Account Executive"},
	{"SUP-PRIMA", "PT Prima Golf Supplies", "order@primagolf.test", "+62215550103", "Jl. TB Simatupang 5, Jakarta", "none",
		[]string{"Pro Shop", "Golf"}, true, true, 5, "Andi Saputra", "Key Account"},
	{"SUP-TEKNIK", "CV Jasa Teknik Mandiri", "admin@jasateknik.test", "+62215550104", "Jl. Raya Serpong 21, Tangerang", "pph23",
		[]string{"Maintenance", "Services"}, false, false, 1, "Bambang Susilo", "Owner"},
}

type demoStep struct {
	no         int
	name, role string
	conds      []map[string]any
}

func level(n int) []map[string]any {
	return []map[string]any{{"attribute": "approvalLevel", "operator": "gte", "value": n}}
}

var demoWorkflows = []struct {
	doc, name string
	steps     []demoStep
}{
	{RequisitionDocumentType.Code, "Purchase Requisition — approval matrix", []demoStep{
		{1, "Procurement Manager approval", "procurement_manager", nil},
		{2, "Finance Manager above IDR 5,000,000", "finance_manager", level(2)},
		{3, "General Manager above IDR 25,000,000", "general_manager", level(3)}}},
	{PurchaseOrderDocumentType.Code, "Purchase Order — approval matrix", []demoStep{
		{1, "Procurement Manager approval", "procurement_manager", nil},
		{2, "Finance Manager above IDR 5,000,000", "finance_manager", level(2)},
		{3, "General Manager above IDR 25,000,000", "general_manager", level(3)}}},
	{QuotationSelectionDocumentType.Code, "Vendor Quotation Selection (not the cheapest)", []demoStep{
		{1, "Procurement Manager approval", "procurement_manager", nil}}},
	{GoodsReceiptDocumentType.Code, "Goods Receipt Exception (over-receipt / without PO)", []demoStep{
		{1, "Procurement Manager approval", "procurement_manager", nil}}},
	{VendorInvoiceDocumentType.Code, "Vendor Invoice — Finance above IDR 5,000,000", []demoStep{
		{1, "Finance Manager approval", "finance_manager", level(2)}}},
	{InvoiceOverrideDocumentType.Code, "Vendor Invoice Mismatch Override", []demoStep{
		{1, "Finance Manager approval", "finance_manager", nil}}},
	{SupplierStatusDocumentType.Code, "Supplier Block / Unblock", []demoStep{
		{1, "General Manager approval", "general_manager", nil}}},
}

// SeedDemo seeds the procurement demo data on a property (idempotent).
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	for _, s := range demoSuppliers {
		sid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO procurement.suppliers (id, property_id, code, name, email, phone, address, categories, pkp, withholding_type,
			contract_supplier, lead_time_days) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, sid, property, s.code, s.name, s.email, s.phone, s.address,
			s.categories, s.pkp, s.withholding, s.contract, s.lead).Scan(&sid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.supplier_contacts (id, property_id, supplier_id, name, position, email, phone, is_primary)
			SELECT $1, $2, $3, $4, $5, $6, $7, true WHERE NOT EXISTS (SELECT 1 FROM procurement.supplier_contacts WHERE supplier_id = $3)`,
			id.New(), property, sid, s.contact, s.position, s.email, s.phone); err != nil {
			return err
		}
	}
	for _, wf := range demoWorkflows {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_workflows WHERE document_type = $1)`, wf.doc).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		wid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflows (id, document_type, name) VALUES ($1,$2,$3)`, wid, wf.doc, wf.name); err != nil {
			return err
		}
		sla := 24
		for _, st := range wf.steps {
			conds := st.conds
			if conds == nil {
				conds = []map[string]any{}
			}
			raw, _ := json.Marshal(conds)
			if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflow_steps (id, workflow_id, step_no, name, approver_type, approver_role_id, conditions,
				sla_hours) SELECT $1, $2, $3, $4, 'role', r.id, $5, $6 FROM platform.roles r WHERE r.code = $7`,
				id.New(), wid, st.no, st.name, raw, sla, st.role); err != nil {
				return err
			}
		}
	}
	return nil
}
