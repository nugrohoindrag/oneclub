package procurement

// PRD P4 EP-12 RFQ & Vendor Quotation: RFQ to several suppliers from
// approved requisition lines (consolidated per item, FR-PR-05) or direct
// lines, sent by e-mail with a token link where the supplier answers
// online (or the buyer records the quotation, FR-RFQ-02); comparison
// matrix per item (best price / lead time) and selection, with a reason and
// approval when the selected quotation is not the cheapest or fewer
// quotations than the policy minimum were compared (FR-RFQ-03/04).

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
)

// ProcurementRFQ is a request for quotation.
type ProcurementRFQ struct {
	ID              uuid.UUID                `json:"id" db:"id"`
	Number          string                   `json:"number" db:"number"`
	Title           string                   `json:"title" db:"title"`
	Status          string                   `json:"status" db:"status" enum:"draft,sent,closed,awarded,cancelled"`
	Currency        string                   `json:"currency" db:"currency"`
	ResponseDueAt   *time.Time               `json:"responseDueAt" db:"response_due_at"`
	DeliveryDate    *string                  `json:"deliveryDate" db:"delivery_date"`
	WarehouseID     *uuid.UUID               `json:"warehouseId" db:"warehouse_id"`
	EstimatedTotal  string                   `json:"estimatedTotal" db:"estimated_total"`
	Notes           *string                  `json:"notes" db:"notes"`
	Terms           *string                  `json:"terms" db:"terms"`
	Quotations      int                      `json:"quotations" db:"quotations"`
	SentAt          *time.Time               `json:"sentAt" db:"sent_at"`
	ClosedAt        *time.Time               `json:"closedAt" db:"closed_at"`
	AwardedAt       *time.Time               `json:"awardedAt" db:"awarded_at"`
	CancelledReason *string                  `json:"cancelledReason" db:"cancelled_reason"`
	CreatedAt       time.Time                `json:"createdAt" db:"created_at"`
	Lines           []ProcurementRFQLine     `json:"lines,omitempty" db:"-"`
	Suppliers       []ProcurementRFQSupplier `json:"suppliers,omitempty" db:"-"`
}

// ProcurementRFQLine is one requested item.
type ProcurementRFQLine struct {
	ID                 uuid.UUID   `json:"id" db:"id"`
	LineNo             int         `json:"lineNo" db:"line_no"`
	ItemID             *uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode           *string     `json:"itemCode" db:"item_code"`
	Description        string      `json:"description" db:"description"`
	Quantity           string      `json:"quantity" db:"quantity"`
	UOMID              *uuid.UUID  `json:"uomId" db:"uom_id"`
	UOM                *string     `json:"uom" db:"uom"`
	NeededBy           *string     `json:"neededBy" db:"needed_by"`
	EstimatedUnitPrice string      `json:"estimatedUnitPrice" db:"estimated_unit_price"`
	RequisitionLineIDs []uuid.UUID `json:"requisitionLineIds" db:"requisition_line_ids"`
}

// ProcurementRFQSupplier is an invited supplier.
type ProcurementRFQSupplier struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	SupplierID   uuid.UUID  `json:"supplierId" db:"supplier_id"`
	SupplierCode string     `json:"supplierCode" db:"supplier_code"`
	SupplierName string     `json:"supplierName" db:"supplier_name"`
	Email        *string    `json:"email" db:"email"`
	Status       string     `json:"status" db:"status" enum:"invited,sent,responded,declined"`
	SentAt       *time.Time `json:"sentAt" db:"sent_at"`
	RespondedAt  *time.Time `json:"respondedAt" db:"responded_at"`
}

// ProcurementRFQLineInput is a direct RFQ line (without requisition).
type ProcurementRFQLineInput struct {
	ItemID             *uuid.UUID `json:"itemId,omitempty"`
	Description        string     `json:"description,omitempty"`
	Quantity           string     `json:"quantity"`
	UOMID              *uuid.UUID `json:"uomId,omitempty"`
	NeededBy           string     `json:"neededBy,omitempty"`
	EstimatedUnitPrice string     `json:"estimatedUnitPrice,omitempty"`
}

// ProcurementRFQInput creates an RFQ.
type ProcurementRFQInput struct {
	Title              string                    `json:"title"`
	RequisitionLineIDs []uuid.UUID               `json:"requisitionLineIds,omitempty" doc:"Approved requisition lines, consolidated per item and UOM"`
	Lines              []ProcurementRFQLineInput `json:"lines,omitempty" doc:"Direct lines (purchases without requisition)"`
	SupplierIDs        []uuid.UUID               `json:"supplierIds"`
	ResponseDueAt      *time.Time                `json:"responseDueAt,omitempty" doc:"Default: now + Procurement Configuration rfqResponseDays"`
	DeliveryDate       string                    `json:"deliveryDate,omitempty"`
	WarehouseID        *uuid.UUID                `json:"warehouseId,omitempty"`
	Currency           string                    `json:"currency,omitempty"`
	Notes              string                    `json:"notes,omitempty"`
	Terms              string                    `json:"terms,omitempty"`
	Send               bool                      `json:"send,omitempty" doc:"Send to the suppliers right away"`
}

// ProcurementRFQUpdateInput edits a draft RFQ.
type ProcurementRFQUpdateInput struct {
	Title         string      `json:"title,omitempty"`
	SupplierIDs   []uuid.UUID `json:"supplierIds,omitempty" doc:"Replaces the invited suppliers"`
	ResponseDueAt *time.Time  `json:"responseDueAt,omitempty"`
	DeliveryDate  string      `json:"deliveryDate,omitempty"`
	Notes         string      `json:"notes,omitempty"`
	Terms         string      `json:"terms,omitempty"`
}

const rfqSelect = `SELECT q.id, q.number, q.title, q.status, q.currency, q.response_due_at, to_char(q.delivery_date, 'YYYY-MM-DD') AS delivery_date,
	q.warehouse_id, trim_scale(q.estimated_total)::text AS estimated_total, q.notes, q.terms,
	(SELECT count(*) FROM procurement.vendor_quotations v WHERE v.rfq_id = q.id AND v.status <> 'cancelled')::int AS quotations,
	q.sent_at, q.closed_at, q.awarded_at, q.cancelled_reason, q.created_at FROM procurement.rfqs q`

const rfqLineSelect = `SELECT l.id, l.line_no, l.item_id, i.code AS item_code, l.description, trim_scale(l.quantity)::text AS quantity, l.uom_id, u.code AS uom,
	to_char(l.needed_by, 'YYYY-MM-DD') AS needed_by, trim_scale(l.estimated_unit_price)::text AS estimated_unit_price,
	coalesce((SELECT array_agg(s.requisition_line_id) FROM procurement.rfq_line_sources s WHERE s.rfq_line_id = l.id), '{}') AS requisition_line_ids
	FROM procurement.rfq_lines l LEFT JOIN inventory.items i ON i.id = l.item_id LEFT JOIN inventory.uoms u ON u.id = l.uom_id`

const rfqSupplierSelect = `SELECT x.id, x.supplier_id, s.code AS supplier_code, s.name AS supplier_name, x.email, x.status, x.sent_at, x.responded_at
	FROM procurement.rfq_suppliers x JOIN procurement.suppliers s ON s.id = x.supplier_id`

// GetRFQ loads an RFQ with lines and suppliers.
func GetRFQ(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (ProcurementRFQ, error) {
	r, err := oneOf[ProcurementRFQ]("RFQ")(q.Query(ctx, rfqSelect+` WHERE q.id = $1`, rid))
	if err != nil {
		return r, err
	}
	if r.Lines, err = handle.List[ProcurementRFQLine](q.Query(ctx, rfqLineSelect+` WHERE l.rfq_id = $1 ORDER BY l.line_no`, rid)); err != nil {
		return r, err
	}
	r.Suppliers, err = handle.List[ProcurementRFQSupplier](q.Query(ctx, rfqSupplierSelect+` WHERE x.rfq_id = $1 ORDER BY s.name`, rid))
	return r, err
}

func lockRFQ(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (ProcurementRFQ, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM procurement.rfqs WHERE id = $1 FOR UPDATE`, rid); err != nil {
		return ProcurementRFQ{}, err
	}
	return GetRFQ(ctx, tx, rid)
}

func (m *Module) setRFQSuppliers(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return errs.Validation("suppliers_required", "invite at least one supplier", errs.Field("supplierIds", "required", "invite at least one supplier"))
	}
	seen := map[uuid.UUID]bool{}
	for _, sid := range ids {
		if seen[sid] {
			continue
		}
		seen[sid] = true
		s, err := usableSupplier(ctx, tx, sid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.rfq_suppliers (id, property_id, rfq_id, supplier_id, email) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (rfq_id, supplier_id) DO NOTHING`, id.New(), property, rid, sid, nz(orderEmail(ctx, tx, sid, s.Email))); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM procurement.rfq_suppliers WHERE rfq_id = $1 AND NOT (supplier_id = ANY($2))`, rid, ids)
	return err
}

// CreateRFQ creates an RFQ (draft, or sent with Send).
func (m *Module) CreateRFQ(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ProcurementRFQInput) (ProcurementRFQ, error) {
	if err := handle.Required("title", in.Title); err != nil {
		return ProcurementRFQ{}, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return ProcurementRFQ{}, err
	}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if cur == "" {
		cur = cfg.DefaultCurrency
	}
	delivery, err := parseDate("deliveryDate", in.DeliveryDate)
	if err != nil {
		return ProcurementRFQ{}, err
	}
	due := in.ResponseDueAt
	if due == nil {
		t := clock.Now().Add(time.Duration(max(cfg.RFQResponseDays, 1)) * 24 * time.Hour)
		due = &t
	}
	type rline struct {
		item        *uuid.UUID
		uom         *uuid.UUID
		description string
		qty, price  decimal.Decimal
		needed      *time.Time
		sources     map[uuid.UUID]decimal.Decimal
	}
	var lines []*rline
	byKey := map[string]*rline{}
	var srcIDs []uuid.UUID
	if len(in.RequisitionLineIDs) > 0 {
		src, err := orderableLines(ctx, tx, in.RequisitionLineIDs)
		if err != nil {
			return ProcurementRFQ{}, err
		}
		for _, s := range src {
			if s.RFQID != nil {
				var open bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM procurement.rfqs WHERE id = $1 AND status IN ('draft', 'sent', 'closed', 'awarded'))`,
					*s.RFQID).Scan(&open); err != nil {
					return ProcurementRFQ{}, err
				}
				if open {
					return ProcurementRFQ{}, errs.Validation("line_in_rfq", "a line of "+s.Number+" is already in an open RFQ",
						errs.Field("requisitionLineIds", "in_rfq", "already in an open RFQ"))
				}
			}
			key := s.ID.String()
			if s.ItemID != nil && s.UOMID != nil {
				key = s.ItemID.String() + "/" + s.UOMID.String()
			}
			l := byKey[key]
			if l == nil {
				l = &rline{item: s.ItemID, uom: s.UOMID, description: s.Description, price: s.Price, needed: s.NeededBy, sources: map[uuid.UUID]decimal.Decimal{}}
				byKey[key] = l
				lines = append(lines, l)
			}
			l.qty = l.qty.Add(s.OpenQty)
			l.sources[s.ID] = s.OpenQty
			if s.NeededBy != nil && (l.needed == nil || s.NeededBy.Before(*l.needed)) {
				l.needed = s.NeededBy
			}
			srcIDs = append(srcIDs, s.ID)
			if in.WarehouseID == nil && s.WarehouseID != nil {
				in.WarehouseID = s.WarehouseID
			}
		}
	}
	for i, x := range in.Lines {
		pl, err := prepareItemLine(ctx, tx, fmt.Sprintf("lines[%d]", i), x.ItemID, x.UOMID, x.Description, x.Quantity, x.EstimatedUnitPrice, nil,
			localToday(ctx, tx, property))
		if err != nil {
			return ProcurementRFQ{}, err
		}
		needed, err := parseDate(fmt.Sprintf("lines[%d].neededBy", i), x.NeededBy)
		if err != nil {
			return ProcurementRFQ{}, err
		}
		lines = append(lines, &rline{item: pl.ItemID, uom: pl.UOMID, description: pl.Description, qty: pl.Quantity, price: pl.Price, needed: needed})
	}
	if len(lines) == 0 {
		return ProcurementRFQ{}, errs.Validation("lines_required", "select requisition lines or add lines", errs.Field("lines", "required", "add at least one line"))
	}
	num, err := nextNumber(ctx, tx, property, "RFQ")
	if err != nil {
		return ProcurementRFQ{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.rfqs (id, property_id, number, title, currency, response_due_at, delivery_date, warehouse_id, notes,
		terms, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, rid, property, num, strings.TrimSpace(in.Title), cur, due, delivery,
		in.WarehouseID, nz(in.Notes), nz(in.Terms), actor(ctx)); err != nil {
		return ProcurementRFQ{}, err
	}
	total := decimal.Zero
	for i, l := range lines {
		lid := id.New()
		total = total.Add(money(l.qty.Mul(l.price), cur))
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.rfq_lines (id, property_id, rfq_id, line_no, item_id, description, quantity, uom_id, needed_by,
			estimated_unit_price) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10::numeric)`, lid, property, rid, i+1, l.item, l.description, l.qty.String(),
			l.uom, l.needed, l.price.String()); err != nil {
			return ProcurementRFQ{}, err
		}
		for sid, q := range l.sources {
			if _, err := tx.Exec(ctx, `INSERT INTO procurement.rfq_line_sources (rfq_line_id, requisition_line_id, property_id, quantity)
				VALUES ($1,$2,$3,$4::numeric)`, lid, sid, property, q.String()); err != nil {
				return ProcurementRFQ{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.rfqs SET estimated_total = $2::numeric WHERE id = $1`, rid, total.String()); err != nil {
		return ProcurementRFQ{}, err
	}
	if len(srcIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisition_lines SET rfq_id = $2 WHERE id = ANY($1)`, srcIDs, rid); err != nil {
			return ProcurementRFQ{}, err
		}
	}
	if err := m.setRFQSuppliers(ctx, tx, property, rid, in.SupplierIDs); err != nil {
		return ProcurementRFQ{}, err
	}
	out, err := GetRFQ(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.rfq", EntityID: rid.String(),
		EntityLabel: num, PropertyID: &property, After: out}); err != nil {
		return out, err
	}
	if in.Send {
		return m.SendRFQ(ctx, tx, rid)
	}
	return out, nil
}

// UpdateRFQ edits a draft RFQ.
func (m *Module) UpdateRFQ(ctx context.Context, tx pgx.Tx, rid uuid.UUID, in ProcurementRFQUpdateInput) (ProcurementRFQ, error) {
	before, err := lockRFQ(ctx, tx, rid)
	if err != nil {
		return before, err
	}
	if before.Status != "draft" {
		return before, errs.Conflict("rfq_not_draft", "only draft RFQs can be edited")
	}
	delivery, err := parseDate("deliveryDate", in.DeliveryDate)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.rfqs SET title = coalesce($2, title), response_due_at = coalesce($3, response_due_at),
		delivery_date = coalesce($4, delivery_date), notes = coalesce($5, notes), terms = coalesce($6, terms), updated_by = $7 WHERE id = $1`,
		rid, nz(in.Title), in.ResponseDueAt, delivery, nz(in.Notes), nz(in.Terms), actor(ctx)); err != nil {
		return before, err
	}
	property := propertyOf(ctx)
	if len(in.SupplierIDs) > 0 {
		if err := m.setRFQSuppliers(ctx, tx, property, rid, in.SupplierIDs); err != nil {
			return before, err
		}
	}
	after, err := GetRFQ(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionUpdate, EntityType: "procurement.rfq", EntityID: rid.String(),
		EntityLabel: after.Number, PropertyID: &property, Before: before, After: after})
}

func (m *Module) rfqLink(token string) string {
	base := ""
	if m.WebsiteURL != nil {
		base = m.WebsiteURL()
	}
	return base + "/id/supplier/rfq/" + token
}

// SendRFQ e-mails every invited supplier a response link (FR-RFQ-01);
// suppliers without e-mail stay Invited for manual quotation entry.
func (m *Module) SendRFQ(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (ProcurementRFQ, error) {
	r, err := lockRFQ(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != "draft" && r.Status != "sent" {
		return r, errs.Conflict("rfq_not_open", "only draft or sent RFQs can be sent")
	}
	property := propertyOf(ctx)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return r, err
	}
	if dec(r.EstimatedTotal).GreaterThan(dec(pol.RFQRequiredAbove)) && len(r.Suppliers) < pol.MinQuotations {
		return r, errs.Validation("min_suppliers", fmt.Sprintf("invite at least %d suppliers above the RFQ threshold (Procurement Policies)", pol.MinQuotations),
			errs.Field("supplierIds", "too_few", fmt.Sprintf("at least %d suppliers", pol.MinQuotations)))
	}
	club := clubName(ctx, tx)
	due := ""
	if r.ResponseDueAt != nil {
		due = r.ResponseDueAt.In(localNow(ctx, tx, property).Location()).Format("2006-01-02 15:04")
	}
	sent := 0
	for _, s := range r.Suppliers {
		if s.Status != "invited" || s.Email == nil || *s.Email == "" {
			continue
		}
		token := secret.RandomToken(24)
		if _, err := tx.Exec(ctx, `UPDATE procurement.rfq_suppliers SET token_hash = $2, status = 'sent', sent_at = now() WHERE id = $1`,
			s.ID, secret.HashToken(token)); err != nil {
			return r, err
		}
		if m.Notify != nil {
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "procurement.rfq", Category: "procurement", Email: *s.Email, Name: s.SupplierName,
				Channels: []string{notify.ChannelEmail}, PropertyID: &property, Data: map[string]any{"number": r.Number, "title": r.Title, "club": club,
					"supplier": s.SupplierName, "dueAt": due, "lines": len(r.Lines), "link": m.rfqLink(token)}}); err != nil {
				return r, err
			}
		}
		sent++
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.rfqs SET status = 'sent', sent_at = coalesce(sent_at, now()), updated_by = $2 WHERE id = $1`,
		rid, actor(ctx)); err != nil {
		return r, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "send", EntityType: "procurement.rfq", EntityID: rid.String(),
		EntityLabel: r.Number, PropertyID: &property, Before: map[string]any{"status": r.Status}, After: map[string]any{"status": "sent"},
		Metadata: map[string]any{"emailed": sent, "suppliers": len(r.Suppliers)}}); err != nil {
		return r, err
	}
	return GetRFQ(ctx, tx, rid)
}

// CloseRFQ stops accepting quotations; CancelRFQ releases the requisition lines.
func (m *Module) setRFQStatus(ctx context.Context, tx pgx.Tx, rid uuid.UUID, to, reason string) (ProcurementRFQ, error) {
	r, err := lockRFQ(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	switch to {
	case "closed":
		if r.Status != "sent" {
			return r, errs.Conflict("rfq_not_sent", "only sent RFQs can be closed")
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.rfqs SET status = 'closed', closed_at = now(), updated_by = $2 WHERE id = $1`, rid, actor(ctx)); err != nil {
			return r, err
		}
	case "cancelled":
		if err := handle.Required("reason", reason); err != nil {
			return r, err
		}
		if r.Status == "cancelled" || r.Status == "awarded" {
			return r, errs.Conflict("rfq_not_cancellable", "an awarded or cancelled RFQ cannot be cancelled")
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.rfqs SET status = 'cancelled', cancelled_reason = $2, updated_by = $3 WHERE id = $1`,
			rid, reason, actor(ctx)); err != nil {
			return r, err
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisition_lines SET rfq_id = NULL WHERE rfq_id = $1`, rid); err != nil {
			return r, err
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET status = 'cancelled' WHERE rfq_id = $1 AND status IN ('received', 'pending_approval')`,
			rid); err != nil {
			return r, err
		}
	}
	property := propertyOf(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: map[string]string{"closed": "close", "cancelled": "cancel"}[to],
		EntityType: "procurement.rfq", EntityID: rid.String(), EntityLabel: r.Number, PropertyID: &property, Reason: reason,
		Before: map[string]any{"status": r.Status}, After: map[string]any{"status": to}}); err != nil {
		return r, err
	}
	return GetRFQ(ctx, tx, rid)
}

// ── comparison (FR-RFQ-03) ──────────────────────────────────────────────

// VendorQuotationOffer is one supplier price for an RFQ line.
type VendorQuotationOffer struct {
	QuotationID     uuid.UUID `json:"quotationId"`
	QuotationNumber string    `json:"quotationNumber"`
	QuotationLineID uuid.UUID `json:"quotationLineId"`
	SupplierID      uuid.UUID `json:"supplierId"`
	SupplierName    string    `json:"supplierName"`
	UnitPrice       string    `json:"unitPrice"`
	DiscountPercent string    `json:"discountPercent"`
	NetUnitPrice    string    `json:"netUnitPrice"`
	TaxPercent      string    `json:"taxPercent"`
	LineTotal       string    `json:"lineTotal"`
	LeadTimeDays    *int      `json:"leadTimeDays"`
	ValidUntil      *string   `json:"validUntil"`
	Cheapest        bool      `json:"cheapest"`
	Fastest         bool      `json:"fastest"`
	Selected        bool      `json:"selected"`
}

// VendorQuotationComparisonLine is one RFQ line with its offers.
type VendorQuotationComparisonLine struct {
	RFQLineID   uuid.UUID              `json:"rfqLineId"`
	Description string                 `json:"description"`
	Quantity    string                 `json:"quantity"`
	UOM         *string                `json:"uom"`
	Offers      []VendorQuotationOffer `json:"offers"`
}

// VendorQuotationComparison is the quotation comparison matrix.
type VendorQuotationComparison struct {
	RFQID      uuid.UUID                       `json:"rfqId"`
	Number     string                          `json:"number"`
	Status     string                          `json:"status"`
	Quotations int                             `json:"quotations"`
	Lines      []VendorQuotationComparisonLine `json:"lines"`
	// BestTotal is the sum of the cheapest offer per line.
	BestTotal string `json:"bestTotal"`
}

type offerRow struct {
	RFQLineID       uuid.UUID  `db:"rfq_line_id"`
	QuotationID     uuid.UUID  `db:"quotation_id"`
	QuotationNumber string     `db:"quotation_number"`
	QuotationLineID uuid.UUID  `db:"quotation_line_id"`
	SupplierID      uuid.UUID  `db:"supplier_id"`
	SupplierName    string     `db:"supplier_name"`
	UnitPrice       string     `db:"unit_price"`
	DiscountPercent string     `db:"discount_percent"`
	TaxPercent      string     `db:"tax_percent"`
	LineTotal       string     `db:"line_total"`
	LeadTimeDays    *int       `db:"lead_time_days"`
	ValidUntil      *string    `db:"valid_until"`
	Selected        bool       `db:"selected"`
	Status          string     `db:"status"`
	RFQID           *uuid.UUID `db:"rfq_id"`
}

func rfqOffers(ctx context.Context, q dbtx.Querier, rid uuid.UUID) ([]offerRow, error) {
	return handle.List[offerRow](q.Query(ctx, `SELECT ql.rfq_line_id, v.id AS quotation_id, v.number AS quotation_number, ql.id AS quotation_line_id,
		v.supplier_id, s.name AS supplier_name, trim_scale(ql.unit_price)::text AS unit_price, trim_scale(ql.discount_percent)::text AS discount_percent,
		trim_scale(ql.tax_percent)::text AS tax_percent, trim_scale(ql.line_total)::text AS line_total, coalesce(ql.lead_time_days, v.lead_time_days) AS lead_time_days,
		to_char(v.valid_until, 'YYYY-MM-DD') AS valid_until, ql.selected, v.status, v.rfq_id
		FROM procurement.vendor_quotation_lines ql JOIN procurement.vendor_quotations v ON v.id = ql.quotation_id
		JOIN procurement.suppliers s ON s.id = v.supplier_id
		WHERE v.rfq_id = $1 AND v.status IN ('received', 'pending_approval', 'selected', 'not_selected') AND ql.rfq_line_id IS NOT NULL
		ORDER BY ql.rfq_line_id, ql.unit_price`, rid))
}

// Compare builds the comparison matrix of an RFQ.
func Compare(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (VendorQuotationComparison, error) {
	r, err := GetRFQ(ctx, q, rid)
	if err != nil {
		return VendorQuotationComparison{}, err
	}
	offers, err := rfqOffers(ctx, q, rid)
	if err != nil {
		return VendorQuotationComparison{}, err
	}
	out := VendorQuotationComparison{RFQID: r.ID, Number: r.Number, Status: r.Status, Quotations: r.Quotations, Lines: []VendorQuotationComparisonLine{}}
	best := decimal.Zero
	for _, l := range r.Lines {
		cl := VendorQuotationComparisonLine{RFQLineID: l.ID, Description: l.Description, Quantity: l.Quantity, UOM: l.UOM, Offers: []VendorQuotationOffer{}}
		var minPrice *decimal.Decimal
		var minLead *int
		for _, o := range offers {
			if o.RFQLineID != l.ID {
				continue
			}
			np := netPrice(dec(o.UnitPrice), dec(o.DiscountPercent))
			if minPrice == nil || np.LessThan(*minPrice) {
				minPrice = &np
			}
			if o.LeadTimeDays != nil && (minLead == nil || *o.LeadTimeDays < *minLead) {
				lt := *o.LeadTimeDays
				minLead = &lt
			}
			cl.Offers = append(cl.Offers, VendorQuotationOffer{QuotationID: o.QuotationID, QuotationNumber: o.QuotationNumber, QuotationLineID: o.QuotationLineID,
				SupplierID: o.SupplierID, SupplierName: o.SupplierName, UnitPrice: o.UnitPrice, DiscountPercent: o.DiscountPercent, NetUnitPrice: np.String(),
				TaxPercent: o.TaxPercent, LineTotal: o.LineTotal, LeadTimeDays: o.LeadTimeDays, ValidUntil: o.ValidUntil, Selected: o.Selected})
		}
		for i := range cl.Offers {
			o := &cl.Offers[i]
			o.Cheapest = minPrice != nil && dec(o.NetUnitPrice).Equal(*minPrice)
			o.Fastest = minLead != nil && o.LeadTimeDays != nil && *o.LeadTimeDays == *minLead
		}
		sort.SliceStable(cl.Offers, func(i, j int) bool { return dec(cl.Offers[i].NetUnitPrice).LessThan(dec(cl.Offers[j].NetUnitPrice)) })
		if minPrice != nil {
			best = best.Add(money(minPrice.Mul(dec(l.Quantity)), r.Currency))
		}
		out.Lines = append(out.Lines, cl)
	}
	out.BestTotal = best.String()
	return out, nil
}

// ── vendor quotations (FR-RFQ-02) ───────────────────────────────────────

// VendorQuotation is a supplier's quotation.
type VendorQuotation struct {
	ID                uuid.UUID             `json:"id" db:"id"`
	Number            string                `json:"number" db:"number"`
	RFQID             *uuid.UUID            `json:"rfqId" db:"rfq_id"`
	RFQNumber         *string               `json:"rfqNumber" db:"rfq_number"`
	SupplierID        uuid.UUID             `json:"supplierId" db:"supplier_id"`
	SupplierName      string                `json:"supplierName" db:"supplier_name"`
	SupplierReference *string               `json:"supplierReference" db:"supplier_reference"`
	QuotationDate     string                `json:"quotationDate" db:"quotation_date"`
	ValidUntil        *string               `json:"validUntil" db:"valid_until"`
	Currency          string                `json:"currency" db:"currency"`
	LeadTimeDays      *int                  `json:"leadTimeDays" db:"lead_time_days"`
	PaymentTermDays   *int                  `json:"paymentTermDays" db:"payment_term_days"`
	DeliveryTerms     *string               `json:"deliveryTerms" db:"delivery_terms"`
	Subtotal          string                `json:"subtotal" db:"subtotal"`
	DiscountTotal     string                `json:"discountTotal" db:"discount_total"`
	TaxTotal          string                `json:"taxTotal" db:"tax_total"`
	Total             string                `json:"total" db:"total"`
	Status            string                `json:"status" db:"status" enum:"received,pending_approval,selected,not_selected,cancelled"`
	Source            string                `json:"source" db:"source" enum:"manual,supplier_link"`
	Notes             *string               `json:"notes" db:"notes"`
	SelectionReason   *string               `json:"selectionReason" db:"selection_reason"`
	ApprovalRequestID *uuid.UUID            `json:"approvalRequestId" db:"approval_request_id"`
	SelectedAt        *time.Time            `json:"selectedAt" db:"selected_at"`
	CreatedAt         time.Time             `json:"createdAt" db:"created_at"`
	Lines             []VendorQuotationLine `json:"lines,omitempty" db:"-"`
}

// VendorQuotationLine is one quoted line.
type VendorQuotationLine struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	LineNo          int        `json:"lineNo" db:"line_no"`
	RFQLineID       *uuid.UUID `json:"rfqLineId" db:"rfq_line_id"`
	ItemID          *uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode        *string    `json:"itemCode" db:"item_code"`
	Description     string     `json:"description" db:"description"`
	Quantity        string     `json:"quantity" db:"quantity"`
	UOMID           *uuid.UUID `json:"uomId" db:"uom_id"`
	UOM             *string    `json:"uom" db:"uom"`
	UnitPrice       string     `json:"unitPrice" db:"unit_price"`
	DiscountPercent string     `json:"discountPercent" db:"discount_percent"`
	TaxPercent      string     `json:"taxPercent" db:"tax_percent"`
	LineSubtotal    string     `json:"lineSubtotal" db:"line_subtotal"`
	TaxAmount       string     `json:"taxAmount" db:"tax_amount"`
	LineTotal       string     `json:"lineTotal" db:"line_total"`
	LeadTimeDays    *int       `json:"leadTimeDays" db:"lead_time_days"`
	Selected        bool       `json:"selected" db:"selected"`
	Notes           *string    `json:"notes" db:"notes"`
}

// VendorQuotationLineInput is one quoted line; for an RFQ quotation item, UOM
// and quantity default to the RFQ line.
type VendorQuotationLineInput struct {
	RFQLineID       *uuid.UUID `json:"rfqLineId,omitempty"`
	ItemID          *uuid.UUID `json:"itemId,omitempty"`
	Description     string     `json:"description,omitempty"`
	Quantity        string     `json:"quantity,omitempty"`
	UOMID           *uuid.UUID `json:"uomId,omitempty"`
	UnitPrice       string     `json:"unitPrice"`
	DiscountPercent string     `json:"discountPercent,omitempty"`
	TaxPercent      string     `json:"taxPercent,omitempty" doc:"Default: PPN of the configuration for PKP suppliers, else 0"`
	LeadTimeDays    *int       `json:"leadTimeDays,omitempty"`
	Notes           string     `json:"notes,omitempty"`
}

// VendorQuotationInput records a vendor quotation.
type VendorQuotationInput struct {
	RFQID             *uuid.UUID                 `json:"rfqId,omitempty"`
	SupplierID        uuid.UUID                  `json:"supplierId"`
	SupplierReference string                     `json:"supplierReference,omitempty"`
	QuotationDate     string                     `json:"quotationDate,omitempty" doc:"Default: today"`
	ValidUntil        string                     `json:"validUntil,omitempty"`
	Currency          string                     `json:"currency,omitempty"`
	LeadTimeDays      *int                       `json:"leadTimeDays,omitempty"`
	PaymentTermDays   *int                       `json:"paymentTermDays,omitempty"`
	DeliveryTerms     string                     `json:"deliveryTerms,omitempty"`
	Notes             string                     `json:"notes,omitempty"`
	Lines             []VendorQuotationLineInput `json:"lines"`
}

// VendorQuotationSelectInput selects a quotation (all lines or the given ones).
type VendorQuotationSelectInput struct {
	LineIDs             []uuid.UUID `json:"lineIds,omitempty" doc:"Quotation lines to select; default all"`
	Reason              string      `json:"reason,omitempty" doc:"Required when the selection is not the cheapest (approval)"`
	CreatePurchaseOrder bool        `json:"createPurchaseOrder,omitempty" doc:"Create a draft purchase order once selected"`
}

const quotationSelect = `SELECT v.id, v.number, v.rfq_id, q.number AS rfq_number, v.supplier_id, s.name AS supplier_name, v.supplier_reference,
	to_char(v.quotation_date, 'YYYY-MM-DD') AS quotation_date, to_char(v.valid_until, 'YYYY-MM-DD') AS valid_until, v.currency, v.lead_time_days,
	v.payment_term_days, v.delivery_terms, trim_scale(v.subtotal)::text AS subtotal, trim_scale(v.discount_total)::text AS discount_total,
	trim_scale(v.tax_total)::text AS tax_total, trim_scale(v.total)::text AS total, v.status, v.source, v.notes, v.selection_reason, v.approval_request_id,
	v.selected_at, v.created_at FROM procurement.vendor_quotations v JOIN procurement.suppliers s ON s.id = v.supplier_id
	LEFT JOIN procurement.rfqs q ON q.id = v.rfq_id`

const quotationLineSelect = `SELECT l.id, l.line_no, l.rfq_line_id, l.item_id, i.code AS item_code, l.description, trim_scale(l.quantity)::text AS quantity,
	l.uom_id, u.code AS uom, trim_scale(l.unit_price)::text AS unit_price, trim_scale(l.discount_percent)::text AS discount_percent,
	trim_scale(l.tax_percent)::text AS tax_percent, trim_scale(l.line_subtotal)::text AS line_subtotal, trim_scale(l.tax_amount)::text AS tax_amount,
	trim_scale(l.line_total)::text AS line_total, l.lead_time_days, l.selected, l.notes
	FROM procurement.vendor_quotation_lines l LEFT JOIN inventory.items i ON i.id = l.item_id LEFT JOIN inventory.uoms u ON u.id = l.uom_id`

// GetQuotation loads a vendor quotation with lines.
func GetQuotation(ctx context.Context, q dbtx.Querier, qid uuid.UUID) (VendorQuotation, error) {
	v, err := oneOf[VendorQuotation]("vendor quotation")(q.Query(ctx, quotationSelect+` WHERE v.id = $1`, qid))
	if err != nil {
		return v, err
	}
	v.Lines, err = handle.List[VendorQuotationLine](q.Query(ctx, quotationLineSelect+` WHERE l.quotation_id = $1 ORDER BY l.line_no`, qid))
	return v, err
}

func lockQuotation(ctx context.Context, tx pgx.Tx, qid uuid.UUID) (VendorQuotation, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM procurement.vendor_quotations WHERE id = $1 FOR UPDATE`, qid); err != nil {
		return VendorQuotation{}, err
	}
	return GetQuotation(ctx, tx, qid)
}

// writeQuotationLines validates and stores the lines and totals.
func (m *Module) writeQuotationLines(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, rfq *ProcurementRFQ, s supplierInfo, cur string, in []VendorQuotationLineInput) error {
	if len(in) == 0 {
		return errs.Validation("lines_required", "quote at least one line", errs.Field("lines", "required", "quote at least one line"))
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	defTax := decimal.Zero
	if s.PKP {
		defTax = dec(cfg.DefaultTaxPercent)
	}
	var sub, disc, tax decimal.Decimal
	seen := map[uuid.UUID]bool{}
	for i, l := range in {
		f := fmt.Sprintf("lines[%d]", i)
		itemID, uomID, desc, qtyS := l.ItemID, l.UOMID, l.Description, l.Quantity
		if rfq != nil {
			if l.RFQLineID == nil {
				return errs.Validation("rfq_line_required", "each line of an RFQ quotation refers to an RFQ line", errs.Field(f+".rfqLineId", "required", "select the RFQ line"))
			}
			var rl *ProcurementRFQLine
			for j := range rfq.Lines {
				if rfq.Lines[j].ID == *l.RFQLineID {
					rl = &rfq.Lines[j]
				}
			}
			if rl == nil || seen[rl.ID] {
				return errs.Validation("invalid_rfq_line", "RFQ line not found or quoted twice", errs.Field(f+".rfqLineId", "invalid", "not a line of this RFQ"))
			}
			seen[rl.ID] = true
			// Comparable prices: item and UOM of the RFQ line.
			itemID, uomID = rl.ItemID, rl.UOMID
			if desc == "" {
				desc = rl.Description
			}
			if strings.TrimSpace(qtyS) == "" {
				qtyS = rl.Quantity
			}
		} else if l.RFQLineID != nil {
			return errs.Validation("invalid_rfq_line", "an RFQ line needs the RFQ", errs.Field(f+".rfqLineId", "invalid", "set rfqId"))
		}
		qty, err := positive(f+".quantity", qtyS)
		if err != nil {
			return err
		}
		if itemID != nil && desc == "" {
			it, err := inventoryItemName(ctx, tx, *itemID)
			if err != nil {
				return err
			}
			desc = it
		}
		if strings.TrimSpace(desc) == "" {
			return errs.Validation("description_required", "describe the quoted line", errs.Field(f+".description", "required", "required"))
		}
		price, err := nonNegative(f+".unitPrice", l.UnitPrice, decimal.Zero)
		if err != nil {
			return err
		}
		if strings.TrimSpace(l.UnitPrice) == "" {
			return errs.Validation("price_required", "quote a unit price", errs.Field(f+".unitPrice", "required", "required"))
		}
		dp, err := percent(f+".discountPercent", l.DiscountPercent, decimal.Zero)
		if err != nil {
			return err
		}
		tp, err := percent(f+".taxPercent", l.TaxPercent, defTax)
		if err != nil {
			return err
		}
		ls, lt, total, gross := lineAmounts(qty, price, dp, tp, cur)
		sub, tax, disc = sub.Add(ls), tax.Add(lt), disc.Add(gross.Sub(ls))
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.vendor_quotation_lines (id, property_id, quotation_id, line_no, rfq_line_id, item_id, description,
			quantity, uom_id, unit_price, discount_percent, tax_percent, line_subtotal, tax_amount, line_total, lead_time_days, notes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15::numeric,$16,$17)`,
			id.New(), property, qid, i+1, l.RFQLineID, itemID, strings.TrimSpace(desc), qty.String(), uomID, price.String(), dp.String(), tp.String(),
			ls.String(), lt.String(), total.String(), l.LeadTimeDays, nz(l.Notes)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET subtotal = $2::numeric, discount_total = $3::numeric, tax_total = $4::numeric,
		total = $5::numeric WHERE id = $1`, qid, sub.String(), disc.String(), tax.String(), sub.Add(tax).String())
	return err
}

func inventoryItemName(ctx context.Context, q dbtx.Querier, item uuid.UUID) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT name FROM inventory.items WHERE id = $1`, item).Scan(&name)
	if dbtx.IsNoRows(err) {
		return "", errs.Validation("invalid_item", "item not found", errs.Field("itemId", "not_found", "item not found"))
	}
	return name, err
}

// RecordQuotation records a vendor quotation (manual entry or supplier link).
func (m *Module) RecordQuotation(ctx context.Context, tx pgx.Tx, property uuid.UUID, in VendorQuotationInput, source string) (VendorQuotation, error) {
	s, err := usableSupplier(ctx, tx, in.SupplierID)
	if err != nil {
		return VendorQuotation{}, err
	}
	var rfq *ProcurementRFQ
	if in.RFQID != nil {
		r, err := lockRFQ(ctx, tx, *in.RFQID)
		if err != nil {
			return VendorQuotation{}, err
		}
		if r.Status != "sent" && r.Status != "draft" && (r.Status != "closed" || source != "manual") {
			return VendorQuotation{}, errs.Conflict("rfq_not_open", "the RFQ no longer accepts quotations")
		}
		invited := false
		for _, x := range r.Suppliers {
			invited = invited || x.SupplierID == in.SupplierID
		}
		if !invited {
			return VendorQuotation{}, errs.Validation("supplier_not_invited", "the supplier was not invited to this RFQ",
				errs.Field("supplierId", "not_invited", "not invited to the RFQ"))
		}
		rfq = &r
	}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if cur == "" {
		cur = s.Currency
		if rfq != nil {
			cur = rfq.Currency
		}
	}
	qdate, err := parseDate("quotationDate", in.QuotationDate)
	if err != nil {
		return VendorQuotation{}, err
	}
	if qdate == nil {
		t := localToday(ctx, tx, property)
		qdate = &t
	}
	valid, err := parseDate("validUntil", in.ValidUntil)
	if err != nil {
		return VendorQuotation{}, err
	}
	num, err := nextNumber(ctx, tx, property, "VQ")
	if err != nil {
		return VendorQuotation{}, err
	}
	term := in.PaymentTermDays
	if term == nil {
		term = &s.TermDays
	}
	qid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.vendor_quotations (id, property_id, number, rfq_id, supplier_id, supplier_reference, quotation_date,
		valid_until, currency, lead_time_days, payment_term_days, delivery_terms, source, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)`, qid, property, num, in.RFQID, in.SupplierID, nz(in.SupplierReference), qdate,
		valid, cur, in.LeadTimeDays, term, nz(in.DeliveryTerms), source, nz(in.Notes), actor(ctx)); err != nil {
		return VendorQuotation{}, err
	}
	if err := m.writeQuotationLines(ctx, tx, property, qid, rfq, s, cur, in.Lines); err != nil {
		return VendorQuotation{}, err
	}
	if rfq != nil {
		if _, err := tx.Exec(ctx, `UPDATE procurement.rfq_suppliers SET status = 'responded', responded_at = now() WHERE rfq_id = $1 AND supplier_id = $2`,
			rfq.ID, in.SupplierID); err != nil {
			return VendorQuotation{}, err
		}
	}
	out, err := GetQuotation(ctx, tx, qid)
	if err != nil {
		return out, err
	}
	e := audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.vendor_quotation", EntityID: qid.String(),
		EntityLabel: num + " · " + s.Name, PropertyID: &property, After: out}
	if source == "supplier_link" {
		e.ActorName = s.Name + " (supplier link)"
	}
	return out, audit.Record(ctx, tx, e)
}

// UpdateQuotation corrects a received quotation (lines replaced when given).
func (m *Module) UpdateQuotation(ctx context.Context, tx pgx.Tx, qid uuid.UUID, in VendorQuotationInput) (VendorQuotation, error) {
	before, err := lockQuotation(ctx, tx, qid)
	if err != nil {
		return before, err
	}
	if before.Status != "received" {
		return before, errs.Conflict("quotation_not_editable", "only received quotations can be corrected")
	}
	property := propertyOf(ctx)
	valid, err := parseDate("validUntil", in.ValidUntil)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET supplier_reference = coalesce($2, supplier_reference), valid_until = coalesce($3, valid_until),
		lead_time_days = coalesce($4, lead_time_days), payment_term_days = coalesce($5, payment_term_days), delivery_terms = coalesce($6, delivery_terms),
		notes = coalesce($7, notes), updated_by = $8 WHERE id = $1`, qid, nz(in.SupplierReference), valid, in.LeadTimeDays, in.PaymentTermDays,
		nz(in.DeliveryTerms), nz(in.Notes), actor(ctx)); err != nil {
		return before, err
	}
	if len(in.Lines) > 0 {
		s, err := loadSupplier(ctx, tx, before.SupplierID)
		if err != nil {
			return before, err
		}
		var rfq *ProcurementRFQ
		if before.RFQID != nil {
			r, err := GetRFQ(ctx, tx, *before.RFQID)
			if err != nil {
				return before, err
			}
			rfq = &r
		}
		if _, err := tx.Exec(ctx, `DELETE FROM procurement.vendor_quotation_lines WHERE quotation_id = $1`, qid); err != nil {
			return before, err
		}
		if err := m.writeQuotationLines(ctx, tx, property, qid, rfq, s, before.Currency, in.Lines); err != nil {
			return before, err
		}
	}
	after, err := GetQuotation(ctx, tx, qid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionUpdate, EntityType: "procurement.vendor_quotation",
		EntityID: qid.String(), EntityLabel: after.Number, PropertyID: &property, Before: before, After: after})
}

// SelectQuotation selects (awards) a quotation (FR-RFQ-03): a selection
// that is not the cheapest per line, or compares fewer quotations than
// the policy minimum above the RFQ threshold, needs a reason and approval.
func (m *Module) SelectQuotation(ctx context.Context, tx pgx.Tx, qid uuid.UUID, in VendorQuotationSelectInput) (VendorQuotation, error) {
	v, err := lockQuotation(ctx, tx, qid)
	if err != nil {
		return v, err
	}
	if v.Status != "received" {
		return v, errs.Conflict("quotation_not_selectable", "only received quotations can be selected")
	}
	property := propertyOf(ctx)
	if v.ValidUntil != nil && *v.ValidUntil < localToday(ctx, tx, property).Format("2006-01-02") {
		return v, errs.Conflict("quotation_expired", "the quotation validity has expired")
	}
	chosen := map[uuid.UUID]bool{}
	for _, lid := range in.LineIDs {
		chosen[lid] = true
	}
	var lines []VendorQuotationLine
	for _, l := range v.Lines {
		if len(chosen) == 0 || chosen[l.ID] {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 || (len(chosen) > 0 && len(lines) != len(chosen)) {
		return v, errs.Validation("invalid_lines", "select lines of this quotation", errs.Field("lineIds", "invalid", "not lines of this quotation"))
	}
	amount, diff := decimal.Zero, decimal.Zero
	quotations := 1
	needReason := false
	var rfqTotal decimal.Decimal
	if v.RFQID != nil {
		r, err := GetRFQ(ctx, tx, *v.RFQID)
		if err != nil {
			return v, err
		}
		rfqTotal, quotations = dec(r.EstimatedTotal), r.Quotations
		offers, err := rfqOffers(ctx, tx, r.ID)
		if err != nil {
			return v, err
		}
		for _, l := range lines {
			if l.RFQLineID == nil {
				continue
			}
			np := netPrice(dec(l.UnitPrice), dec(l.DiscountPercent))
			minP := np
			for _, o := range offers {
				if o.RFQLineID != *l.RFQLineID {
					continue
				}
				if o.Selected && o.QuotationID != v.ID {
					return v, errs.Conflict("line_already_awarded", "RFQ line "+l.Description+" is already awarded to another quotation")
				}
				if op := netPrice(dec(o.UnitPrice), dec(o.DiscountPercent)); op.LessThan(minP) {
					minP = op
				}
			}
			if np.GreaterThan(minP) {
				needReason = true
				diff = diff.Add(money(np.Sub(minP).Mul(dec(l.Quantity)), v.Currency))
			}
		}
	}
	for _, l := range lines {
		amount = amount.Add(dec(l.LineTotal))
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return v, err
	}
	if v.RFQID != nil && rfqTotal.GreaterThan(dec(pol.RFQRequiredAbove)) && quotations < pol.MinQuotations {
		needReason = true
	}
	if needReason && strings.TrimSpace(in.Reason) == "" {
		return v, errs.Validation("reason_required", "explain why this quotation is selected (not the cheapest or too few quotations)",
			errs.Field("reason", "required", "required when the selection is not the cheapest"))
	}
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.ID)
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET selection_reason = $2, selection_line_ids = $3, create_order = $4,
		updated_by = $5 WHERE id = $1`, qid, nz(in.Reason), ids, in.CreatePurchaseOrder, actor(ctx)); err != nil {
		return v, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "select_requested", EntityType: "procurement.vendor_quotation",
		EntityID: qid.String(), EntityLabel: v.Number, PropertyID: &property, Reason: in.Reason,
		Metadata: map[string]any{"lines": len(ids), "notCheapest": diff.IsPositive(), "priceDifference": diff.String(), "quotations": quotations}}); err != nil {
		return v, err
	}
	if !needReason {
		if err := m.applySelection(ctx, tx, property, qid); err != nil {
			return v, err
		}
		return GetQuotation(ctx, tx, qid)
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET status = 'pending_approval' WHERE id = $1`, qid); err != nil {
		return v, err
	}
	fa, _ := amount.Float64()
	fd, _ := diff.Float64()
	areq, _, err := m.submitApproval(ctx, tx, QuotationSelectionDocumentType, qid, property, v.Number,
		"Vendor quotation "+v.Number+" – "+v.SupplierName, map[string]any{"amount": fa, "priceDifference": fd, "quotations": quotations})
	if err != nil {
		return v, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET approval_request_id = $2 WHERE id = $1`, qid, areq); err != nil {
		return v, err
	}
	return GetQuotation(ctx, tx, qid)
}

// applySelection marks the selected lines, settles the RFQ award and
// optionally creates the draft purchase order.
func (m *Module) applySelection(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID) error {
	var rfqID *uuid.UUID
	var ids []uuid.UUID
	var createOrder bool
	if err := tx.QueryRow(ctx, `UPDATE procurement.vendor_quotations SET status = 'selected', selected_at = now() WHERE id = $1
		RETURNING rfq_id, selection_line_ids, create_order`, qid).Scan(&rfqID, &ids, &createOrder); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotation_lines SET selected = (id = ANY($2)) WHERE quotation_id = $1`, qid, ids); err != nil {
		return err
	}
	if rfqID != nil {
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM procurement.rfq_lines l WHERE l.rfq_id = $1 AND NOT EXISTS (SELECT 1
			FROM procurement.vendor_quotation_lines ql JOIN procurement.vendor_quotations v ON v.id = ql.quotation_id
			WHERE ql.rfq_line_id = l.id AND ql.selected AND v.status = 'selected')`, *rfqID).Scan(&open); err != nil {
			return err
		}
		if open == 0 {
			if _, err := tx.Exec(ctx, `UPDATE procurement.rfqs SET status = 'awarded', awarded_at = now() WHERE id = $1`, *rfqID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET status = 'not_selected' WHERE rfq_id = $1 AND status = 'received'`,
				*rfqID); err != nil {
				return err
			}
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "select", EntityType: "procurement.vendor_quotation", EntityID: qid.String(),
		PropertyID: &property, After: map[string]any{"status": "selected", "lines": len(ids)}}); err != nil {
		return err
	}
	if createOrder {
		_, err := m.CreateOrder(ctx, tx, property, PurchaseOrderInput{QuotationID: &qid})
		return err
	}
	return nil
}

func (m *Module) selectionDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM procurement.vendor_quotations WHERE id = $1 FOR UPDATE`, d.DocumentID).Scan(&status); err != nil {
		return err
	}
	if status != "pending_approval" {
		return nil
	}
	if d.Status == approval.StatusApproved {
		return m.applySelection(ctx, tx, d.PropertyID, d.DocumentID)
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET status = 'received', selection_line_ids = '{}', create_order = false
		WHERE id = $1`, d.DocumentID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionStatusChange, EntityType: "procurement.vendor_quotation",
		EntityID: d.DocumentID.String(), PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": status},
		After: map[string]any{"status": "received", "selection": d.Status}})
}

// ── public supplier link ─────────────────────────────────────────────────

// ProcurementPublicRFQLine is an RFQ line shown to the supplier.
type ProcurementPublicRFQLine struct {
	RFQLineID   uuid.UUID `json:"rfqLineId"`
	Description string    `json:"description"`
	Quantity    string    `json:"quantity"`
	UOM         *string   `json:"uom"`
	NeededBy    *string   `json:"neededBy"`
}

// ProcurementPublicRFQ is the RFQ behind a supplier link.
type ProcurementPublicRFQ struct {
	Number        string                     `json:"number"`
	Title         string                     `json:"title"`
	Club          string                     `json:"club"`
	SupplierName  string                     `json:"supplierName"`
	Status        string                     `json:"status"`
	ResponseDueAt *time.Time                 `json:"responseDueAt"`
	DeliveryDate  *string                    `json:"deliveryDate"`
	Currency      string                     `json:"currency"`
	Notes         *string                    `json:"notes"`
	Terms         *string                    `json:"terms"`
	Responded     bool                       `json:"responded"`
	Lines         []ProcurementPublicRFQLine `json:"lines"`
}

// VendorPublicQuoteLine is a supplier's price for an RFQ line.
type VendorPublicQuoteLine struct {
	RFQLineID       uuid.UUID `json:"rfqLineId"`
	UnitPrice       string    `json:"unitPrice"`
	DiscountPercent string    `json:"discountPercent,omitempty"`
	TaxPercent      string    `json:"taxPercent,omitempty"`
	LeadTimeDays    *int      `json:"leadTimeDays,omitempty"`
	Notes           string    `json:"notes,omitempty"`
}

// VendorPublicQuoteInput is the supplier's quotation through the link.
type VendorPublicQuoteInput struct {
	SupplierReference string                  `json:"supplierReference,omitempty"`
	ValidUntil        string                  `json:"validUntil,omitempty"`
	LeadTimeDays      *int                    `json:"leadTimeDays,omitempty"`
	PaymentTermDays   *int                    `json:"paymentTermDays,omitempty"`
	DeliveryTerms     string                  `json:"deliveryTerms,omitempty"`
	Notes             string                  `json:"notes,omitempty"`
	Lines             []VendorPublicQuoteLine `json:"lines"`
}

// VendorPublicQuoteResult acknowledges the quotation.
type VendorPublicQuoteResult struct {
	Number string `json:"number"`
	Status string `json:"status"`
	Total  string `json:"total"`
}

var publicLimiter = &handle.Limiter{N: 30, Period: time.Minute}

func rfqByToken(ctx context.Context, q dbtx.Querier, token string) (uuid.UUID, uuid.UUID, uuid.UUID, error) {
	var rid, sid, property uuid.UUID
	err := q.QueryRow(ctx, `SELECT x.rfq_id, x.supplier_id, x.property_id FROM procurement.rfq_suppliers x WHERE x.token_hash = $1`,
		secret.HashToken(token)).Scan(&rid, &sid, &property)
	if dbtx.IsNoRows(err) || token == "" {
		return rid, sid, property, errs.NotFound("request for quotation")
	}
	return rid, sid, property, err
}

func (m *Module) registerPublic(reg *route.Registry) {
	db := m.DB
	pub := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Auth = "procurement", "Public Supplier", route.AuthPublic
		reg.Add(rt)
	}
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/procurement/rfqs/{token}", Summary: "RFQ behind a supplier link (e-mail)",
		Response: ProcurementPublicRFQ{}, Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.System(r.Context())
			var out ProcurementPublicRFQ
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				rid, sid, _, err := rfqByToken(ctx, tx, chi.URLParam(r, "token"))
				if err != nil {
					return err
				}
				q, err := GetRFQ(ctx, tx, rid)
				if err != nil {
					return err
				}
				out = ProcurementPublicRFQ{Number: q.Number, Title: q.Title, Club: clubName(ctx, tx), Status: q.Status, ResponseDueAt: q.ResponseDueAt,
					DeliveryDate: q.DeliveryDate, Currency: q.Currency, Notes: q.Notes, Terms: q.Terms, Lines: []ProcurementPublicRFQLine{}}
				for _, s := range q.Suppliers {
					if s.SupplierID == sid {
						out.SupplierName, out.Responded = s.SupplierName, s.Status == "responded"
					}
				}
				for _, l := range q.Lines {
					out.Lines = append(out.Lines, ProcurementPublicRFQLine{RFQLineID: l.ID, Description: l.Description, Quantity: l.Quantity, UOM: l.UOM, NeededBy: l.NeededBy})
				}
				return nil
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/procurement/rfqs/{token}:quote",
		Summary: "Submit the vendor quotation through the supplier link (a new submission replaces the previous one)",
		Request: VendorPublicQuoteInput{}, Response: VendorPublicQuoteResult{}, Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var in VendorPublicQuoteInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := dbtx.System(r.Context())
			var out VendorPublicQuoteResult
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				rid, sid, property, err := rfqByToken(ctx, tx, chi.URLParam(r, "token"))
				if err != nil {
					return err
				}
				var status string
				var due *time.Time
				if err := tx.QueryRow(ctx, `SELECT status, response_due_at FROM procurement.rfqs WHERE id = $1`, rid).Scan(&status, &due); err != nil {
					return err
				}
				if status != "sent" || (due != nil && clock.Now().After(*due)) {
					return errs.Conflict("rfq_closed", "this request for quotation is closed")
				}
				if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_quotations SET status = 'cancelled' WHERE rfq_id = $1 AND supplier_id = $2
					AND source = 'supplier_link' AND status = 'received'`, rid, sid); err != nil {
					return err
				}
				lines := make([]VendorQuotationLineInput, 0, len(in.Lines))
				for _, l := range in.Lines {
					lid := l.RFQLineID
					lines = append(lines, VendorQuotationLineInput{RFQLineID: &lid, UnitPrice: l.UnitPrice, DiscountPercent: l.DiscountPercent,
						TaxPercent: l.TaxPercent, LeadTimeDays: l.LeadTimeDays, Notes: l.Notes})
				}
				v, err := m.RecordQuotation(ctx, tx, property, VendorQuotationInput{RFQID: &rid, SupplierID: sid, SupplierReference: in.SupplierReference,
					ValidUntil: in.ValidUntil, LeadTimeDays: in.LeadTimeDays, PaymentTermDays: in.PaymentTermDays, DeliveryTerms: in.DeliveryTerms,
					Notes: in.Notes, Lines: lines}, "supplier_link")
				if err != nil {
					return err
				}
				out = VendorPublicQuoteResult{Number: v.Number, Status: v.Status, Total: v.Total}
				return nil
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusCreated, out)
		})})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/procurement/purchase-orders/{token}/pdf", Summary: "Purchase order PDF behind a supplier link",
		RawContent: "application/pdf", Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.System(r.Context())
			var b []byte
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				var oid uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT id FROM procurement.purchase_orders WHERE public_token_hash = $1 AND status NOT IN ('draft', 'cancelled')`,
					secret.HashToken(chi.URLParam(r, "token"))).Scan(&oid); err != nil {
					if dbtx.IsNoRows(err) {
						return errs.NotFound("purchase order")
					}
					return err
				}
				o, err := GetOrder(ctx, tx, oid)
				if err != nil {
					return err
				}
				b, err = OrderPDF(ctx, tx, o)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(b)
		})})
}

func (m *Module) registerRFQs(reg *route.Registry) {
	db := m.DB
	add := func(tag string, rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", tag, route.ScopeProperty
		reg.Add(rt)
	}
	const trfq, tvq = "RFQ", "Vendor Quotations"
	const base = "/api/v1/procurement/rfqs"
	add(trfq, route.Route{Method: http.MethodGet, Path: base, Summary: "RFQs", Permission: "procurement.rfq.view", Response: ProcurementRFQ{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ProcurementRFQ], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[ProcurementRFQ](tx.Query(ctx, rfqSelect+` WHERE q.property_id = $1 AND ($2 = '' OR q.status = ANY(string_to_array($2, ',')))
				AND ($3 = '' OR q.number ILIKE '%' || $3 || '%' OR q.title ILIKE '%' || $3 || '%') ORDER BY q.created_at DESC LIMIT $4`,
				handle.Property(ctx), lp.Filters["status"], lp.Q, lp.Limit)))
		})})
	add(trfq, route.Route{Method: http.MethodPost, Path: base, Summary: "Create RFQ from requisition lines (consolidated) or direct lines",
		Permission: "procurement.rfq.create", Request: ProcurementRFQInput{}, Response: ProcurementRFQ{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in ProcurementRFQInput) (ProcurementRFQ, error) {
			return m.CreateRFQ(ctx, tx, handle.Property(ctx), in)
		})})
	add(trfq, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "RFQ with lines and invited suppliers", Permission: "procurement.rfq.view",
		Response: ProcurementRFQ{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ProcurementRFQ, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return ProcurementRFQ{}, err
			}
			return GetRFQ(ctx, tx, rid)
		})})
	add(trfq, route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Edit a draft RFQ (suppliers, deadline, notes)",
		Permission: "procurement.rfq.update", Request: ProcurementRFQUpdateInput{}, Response: ProcurementRFQ{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ProcurementRFQUpdateInput) (ProcurementRFQ, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return ProcurementRFQ{}, err
			}
			return m.UpdateRFQ(ctx, tx, rid, in)
		})})
	add(trfq, route.Route{Method: http.MethodPost, Path: base + "/{id}:send", Summary: "Send RFQ to the suppliers (e-mail with response link)",
		Permission: "procurement.rfq.send", Response: ProcurementRFQ{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ProcurementRFQ, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return ProcurementRFQ{}, err
			}
			return m.SendRFQ(ctx, tx, rid)
		})})
	add(trfq, route.Route{Method: http.MethodPost, Path: base + "/{id}:close", Summary: "Close the RFQ for new quotations", Permission: "procurement.rfq.send",
		Response: ProcurementRFQ{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ProcurementRFQ, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return ProcurementRFQ{}, err
			}
			return m.setRFQStatus(ctx, tx, rid, "closed", "")
		})})
	add(trfq, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel the RFQ (requisition lines released)",
		Permission: "procurement.rfq.cancel", Request: ProcurementReasonInput{}, Response: ProcurementRFQ{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ProcurementReasonInput) (ProcurementRFQ, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return ProcurementRFQ{}, err
			}
			return m.setRFQStatus(ctx, tx, rid, "cancelled", in.Reason)
		})})
	add(trfq, route.Route{Method: http.MethodGet, Path: base + "/{id}/comparison", Summary: "Quotation comparison matrix per item (price, lead time)",
		Permission: "procurement.vendor_quotation.view", Response: VendorQuotationComparison{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (VendorQuotationComparison, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return VendorQuotationComparison{}, err
			}
			return Compare(ctx, tx, rid)
		})})
	add(trfq, route.Route{Method: http.MethodGet, Path: base + "/{id}/pdf", Summary: "RFQ PDF (to send manually)", Permission: "procurement.rfq.view",
		RawContent: "application/pdf", Handler: func(w http.ResponseWriter, r *http.Request) {
			rid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var b []byte
			err = db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				q, err := GetRFQ(r.Context(), tx, rid)
				if err != nil {
					return err
				}
				b = RFQPDF(clubName(r.Context(), tx), q)
				return nil
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(b)
		}})

	const vq = "/api/v1/procurement/vendor-quotations"
	add(tvq, route.Route{Method: http.MethodGet, Path: vq, Summary: "Vendor quotations", Permission: "procurement.vendor_quotation.view",
		Response: VendorQuotation{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[rfqId]"}, {Name: "filter[supplierId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VendorQuotation], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[VendorQuotation](tx.Query(ctx, quotationSelect+` WHERE v.property_id = $1
				AND ($2 = '' OR v.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR v.rfq_id::text = $3) AND ($4 = '' OR v.supplier_id::text = $4)
				AND ($5 = '' OR v.number ILIKE '%' || $5 || '%' OR s.name ILIKE '%' || $5 || '%' OR v.supplier_reference ILIKE '%' || $5 || '%')
				ORDER BY v.created_at DESC LIMIT $6`, handle.Property(ctx), lp.Filters["status"], lp.Filters["rfqId"], lp.Filters["supplierId"], lp.Q, lp.Limit)))
		})})
	add(tvq, route.Route{Method: http.MethodPost, Path: vq, Summary: "Record Vendor Quotation (prices, discount, PPN, lead time, terms, validity)",
		Permission: "procurement.vendor_quotation.create", Request: VendorQuotationInput{}, Response: VendorQuotation{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in VendorQuotationInput) (VendorQuotation, error) {
			return m.RecordQuotation(ctx, tx, handle.Property(ctx), in, "manual")
		})})
	add(tvq, route.Route{Method: http.MethodGet, Path: vq + "/{id}", Summary: "Vendor quotation with lines", Permission: "procurement.vendor_quotation.view",
		Response: VendorQuotation{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (VendorQuotation, error) {
			qid, err := handle.ID(r)
			if err != nil {
				return VendorQuotation{}, err
			}
			return GetQuotation(ctx, tx, qid)
		})})
	add(tvq, route.Route{Method: http.MethodPatch, Path: vq + "/{id}", Summary: "Correct a received quotation", Permission: "procurement.vendor_quotation.update",
		Request: VendorQuotationInput{}, Response: VendorQuotation{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in VendorQuotationInput) (VendorQuotation, error) {
			qid, err := handle.ID(r)
			if err != nil {
				return VendorQuotation{}, err
			}
			return m.UpdateQuotation(ctx, tx, qid, in)
		})})
	add(tvq, route.Route{Method: http.MethodPost, Path: vq + "/{id}:select",
		Summary: "Select quotation (award); reason and approval when not the cheapest", Permission: "procurement.vendor_quotation.select",
		Request: VendorQuotationSelectInput{}, Response: VendorQuotation{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in VendorQuotationSelectInput) (VendorQuotation, error) {
			qid, err := handle.ID(r)
			if err != nil {
				return VendorQuotation{}, err
			}
			return m.SelectQuotation(ctx, tx, qid, in)
		})})
}
