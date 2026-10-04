package pos

// POS (PRD P2 EP-20) and F&B Experience (EP-21). An order is priced from the
// product (member price, variant, modifiers, discount) with tax & service of
// the outlet and an immutable snapshot per line. Charges reach Billing only
// when the bill is paid (or charged to a stay / reservation / member folio),
// so an order can be built offline and synchronised later without duplicates
// (client UUIDv7 order id, idempotent payments).

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/commercial/voucher"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/rules"
)

// RefundType is the approval of POS refunds / voids of paid orders (FR-POS-09).
var RefundType = provision.DocumentType{Code: "pos_refund", Module: "commercial", Name: "POS Refund",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}

// POSPolicy is the POS Policies document (FR-POL-P2-06, proposed label).
type POSPolicy struct {
	MaxDiscountPercent    string `json:"maxDiscountPercent"`
	OfflineMemberCharge   bool   `json:"offlineMemberCharge"`
	ScheduledLeadMinutes  int    `json:"scheduledLeadMinutes"`
	DefaultKitchenStation string `json:"defaultKitchenStation"`
}

var defaultPOSPolicy = POSPolicy{MaxDiscountPercent: "10", OfflineMemberCharge: true, ScheduledLeadMinutes: 30, DefaultKitchenStation: "kitchen"}

// ── views ─────────────────────────────────────────────────────────────────

// OrderLine is one line of an order.
type OrderLine struct {
	ID             uuid.UUID        `json:"id" db:"id"`
	LineNo         int              `json:"lineNo" db:"line_no"`
	ProductID      uuid.UUID        `json:"productId" db:"product_id"`
	VariantID      *uuid.UUID       `json:"variantId" db:"variant_id"`
	Name           string           `json:"name" db:"name"`
	Quantity       string           `json:"quantity" db:"quantity"`
	UnitPrice      string           `json:"unitPrice" db:"unit_price"`
	Modifiers      []map[string]any `json:"modifiers" db:"modifiers"`
	DiscountAmount string           `json:"discountAmount" db:"discount_amount"`
	DiscountReason *string          `json:"discountReason" db:"discount_reason"`
	NetAmount      string           `json:"netAmount" db:"net_amount"`
	ServiceAmount  string           `json:"serviceAmount" db:"service_amount"`
	TaxAmount      string           `json:"taxAmount" db:"tax_amount"`
	TotalAmount    string           `json:"totalAmount" db:"total_amount"`
	KitchenStation *string          `json:"kitchenStation" db:"kitchen_station"`
	BillID         *uuid.UUID       `json:"billId" db:"bill_id"`
	Seat           *string          `json:"seat" db:"seat"`
	Notes          *string          `json:"notes" db:"notes"`
	Status         string           `json:"status" db:"status" enum:"active,voided"`
	SentAt         *time.Time       `json:"sentAt" db:"sent_at"`
	ChargedFolioID *uuid.UUID       `json:"chargedFolioId" db:"charged_folio_id"`
}

// Bill is a (split) bill of an order.
type Bill struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	BillNo     int        `json:"billNo" db:"bill_no"`
	Label      *string    `json:"label" db:"label"`
	CustomerID *uuid.UUID `json:"customerId" db:"customer_id"`
	Share      *string    `json:"share" db:"share"`
	FolioID    *uuid.UUID `json:"folioId" db:"folio_id"`
	Status     string     `json:"status" db:"status" enum:"open,paid,voided"`
	Total      string     `json:"total" db:"total"`
	Paid       string     `json:"paid" db:"paid"`
}

// Order is a POS / F&B order.
type Order struct {
	ID                 uuid.UUID   `json:"id" db:"id"`
	PropertyID         uuid.UUID   `json:"propertyId" db:"property_id"`
	OrderNo            string      `json:"orderNo" db:"order_no"`
	OutletID           uuid.UUID   `json:"outletId" db:"outlet_id"`
	OutletName         string      `json:"outletName" db:"outlet_name"`
	ShiftID            *uuid.UUID  `json:"shiftId" db:"shift_id"`
	OrderType          string      `json:"orderType" db:"order_type"`
	Source             string      `json:"source" db:"source"`
	TableNo            *string     `json:"tableNo" db:"table_no"`
	GuestCount         *int        `json:"guestCount" db:"guest_count"`
	CustomerID         *uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName       *string     `json:"customerName" db:"customer_name"`
	MemberPricing      bool        `json:"memberPricing" db:"member_pricing"`
	ServingDestination string      `json:"servingDestination" db:"serving_destination"`
	DestinationRef     *string     `json:"destinationRef" db:"destination_ref"`
	ScheduledFor       *time.Time  `json:"scheduledFor" db:"scheduled_for"`
	Status             string      `json:"status" db:"status" enum:"open,paid,charged,voided,refunded"`
	ServiceStatus      string      `json:"serviceStatus" db:"service_status" enum:"new,sent,preparing,ready,out_for_delivery,served"`
	ChargeFolioID      *uuid.UUID  `json:"chargeFolioId" db:"charge_folio_id"`
	Offline            bool        `json:"offline" db:"offline"`
	NeedsReview        bool        `json:"needsReview" db:"needs_review"`
	Notes              *string     `json:"notes" db:"notes"`
	VoidReason         *string     `json:"voidReason" db:"void_reason"`
	Total              string      `json:"total" db:"total"`
	CreatedAt          time.Time   `json:"createdAt" db:"created_at"`
	Lines              []OrderLine `json:"lines" db:"-"`
	Bills              []Bill      `json:"bills" db:"-"`
}

const orderSelect = `SELECT o.id, o.property_id, o.order_no, o.outlet_id, ou.name AS outlet_name, o.shift_id, o.order_type, o.source, o.table_no,
	o.guest_count, o.customer_id, c.name AS customer_name, o.member_pricing, o.serving_destination, o.destination_ref, o.scheduled_for, o.status,
	o.service_status, o.charge_folio_id, o.offline, o.needs_review, o.notes, o.void_reason, o.created_at,
	trim_scale(coalesce((SELECT sum(total_amount) FROM commercial.order_lines l WHERE l.order_id = o.id AND l.status = 'active'), 0))::text AS total
	FROM commercial.orders o JOIN commercial.outlets ou ON ou.id = o.outlet_id LEFT JOIN crm.customers c ON c.id = o.customer_id`

const orderLineSelect = `SELECT id, line_no, product_id, variant_id, name, trim_scale(quantity)::text AS quantity, trim_scale(unit_price)::text AS unit_price,
	modifiers, trim_scale(discount_amount)::text AS discount_amount, discount_reason, trim_scale(net_amount)::text AS net_amount,
	trim_scale(service_amount)::text AS service_amount, trim_scale(tax_amount)::text AS tax_amount, trim_scale(total_amount)::text AS total_amount,
	kitchen_station, bill_id, seat, notes, status, sent_at, charged_folio_id FROM commercial.order_lines`

// Order returns an order with lines and bills.
func (m *Module) Order(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (Order, error) {
	rows, err := q.Query(ctx, orderSelect+` WHERE o.id = $1`, oid)
	o, err := handle.One[Order](rows, err, "order")
	if err != nil {
		return o, err
	}
	if o.Lines, err = handle.List[OrderLine](q.Query(ctx, orderLineSelect+` WHERE order_id = $1 ORDER BY line_no`, oid)); err != nil {
		return o, err
	}
	o.Bills, err = handle.List[Bill](q.Query(ctx, `SELECT b.id, b.bill_no, b.label, b.customer_id, b.share::text, b.folio_id, b.status,
		trim_scale(CASE WHEN b.share IS NOT NULL THEN round((SELECT coalesce(sum(total_amount),0) FROM commercial.order_lines l WHERE l.order_id = b.order_id AND l.status = 'active') * b.share)
		  ELSE (SELECT coalesce(sum(total_amount),0) FROM commercial.order_lines l WHERE l.bill_id = b.id AND l.status = 'active') END)::text AS total,
		trim_scale(coalesce((SELECT sum(CASE WHEN p.kind = 'refund' THEN -p.amount ELSE p.amount END) FROM billing.payments p
		  WHERE p.folio_id = b.folio_id AND p.status IN ('completed','refunded') AND (b.share IS NULL OR p.tender_ref->>'billId' = b.id::text)), 0))::text AS paid
		FROM commercial.order_bills b WHERE b.order_id = $1 ORDER BY b.bill_no`, oid))
	return o, err
}

// ── order entry ───────────────────────────────────────────────────────────

// LineInput is an item ordered.
type LineInput struct {
	ProductID   uuid.UUID   `json:"productId"`
	VariantID   *uuid.UUID  `json:"variantId,omitempty"`
	Quantity    string      `json:"quantity,omitempty" doc:"Default 1"`
	ModifierIDs []uuid.UUID `json:"modifierIds,omitempty"`
	Seat        string      `json:"seat,omitempty"`
	Notes       string      `json:"notes,omitempty"`
}

// OrderInput creates an order (POS, member app pre-order, caddy tablet
// on-course order, VIP suite add-on, meeting catering).
type OrderInput struct {
	ID                 *uuid.UUID  `json:"id,omitempty" doc:"Client UUIDv7 (offline terminals); a resubmission returns the existing order"`
	OutletID           uuid.UUID   `json:"outletId"`
	ShiftID            *uuid.UUID  `json:"shiftId,omitempty"`
	OrderType          string      `json:"orderType,omitempty" enum:"dine_in,takeaway,on_course,delivery,catering,pre_order,retail"`
	Source             string      `json:"source,omitempty" enum:"pos,member_app,caddy_tablet,vip_suite,meeting_catering,website,driving_range"`
	TableNo            string      `json:"tableNo,omitempty"`
	GuestCount         int         `json:"guestCount,omitempty"`
	CustomerID         *uuid.UUID  `json:"customerId,omitempty"`
	MemberPricing      *bool       `json:"memberPricing,omitempty" doc:"Default: true for customers with an active membership"`
	ServingDestination string      `json:"servingDestination,omitempty" enum:"table,pickup,hole,halfway_house,vip_suite,meeting_room,bungalow"`
	DestinationRef     string      `json:"destinationRef,omitempty" doc:"Table, hole number, halfway house, stay number …"`
	ScheduledFor       *time.Time  `json:"scheduledFor,omitempty" doc:"Pre-order ready time / catering serve time"`
	ChargeFolioID      *uuid.UUID  `json:"chargeFolioId,omitempty" doc:"Charge to a running stay, VIP suite or meeting room folio"`
	Lines              []LineInput `json:"lines"`
	Send               bool        `json:"send,omitempty" doc:"Send to the kitchen immediately"`
	Notes              string      `json:"notes,omitempty"`
	Offline            bool        `json:"offline,omitempty"`
	ClientCreatedAt    *time.Time  `json:"clientCreatedAt,omitempty"`
}

type outlet struct {
	ID               uuid.UUID `db:"id"`
	PropertyID       uuid.UUID `db:"property_id"`
	Name             string    `db:"name"`
	TaxCodes         []string  `db:"tax_codes"`
	PricingMode      string    `db:"pricing_mode"`
	KDSStations      []string  `db:"kds_stations"`
	RevenueComponent string    `db:"revenue_component"`
	Status           string    `db:"status"`
	OutletType       *string   `db:"outlet_type"`
	PrinterDevice    *string   `db:"printer_device"`
}

func loadOutlet(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (outlet, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, name, tax_codes, pricing_mode, kds_stations, revenue_component, status, outlet_type, printer_device
		FROM commercial.outlets WHERE id = $1`, oid)
	return handle.One[outlet](rows, err, "outlet")
}

type product struct {
	ID               uuid.UUID        `db:"id"`
	Code             string           `db:"code"`
	Name             string           `db:"name"`
	ProductType      string           `db:"product_type"`
	Price            string           `db:"price"`
	MemberPrice      *string          `db:"member_price"`
	KitchenStation   *string          `db:"kitchen_station"`
	OutletIDs        []string         `db:"outlet_ids"`
	RevenueComponent *string          `db:"revenue_component"`
	VoucherTypeID    *uuid.UUID       `db:"voucher_type_id"`
	ComboItems       []map[string]any `db:"combo_items"`
	Status           string           `db:"status"`
}

func loadProduct(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (product, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, product_type, price::text, member_price::text, kitchen_station, outlet_ids, revenue_component,
		voucher_type_id, combo_items, status FROM commercial.products WHERE id = $1`, pid)
	return handle.One[product](rows, err, "product")
}

func (m *Module) posPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (POSPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "pos.policy", property, defaultPOSPolicy)
	return p, err
}

// isMember tells whether the customer has an active membership granting
// Member Rate (read from the membership read side: memberships + types).
func isMember(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.membership_members mm JOIN membership.memberships ms ON ms.id = mm.membership_id
		JOIN membership.types t ON t.id = ms.type_id WHERE mm.customer_id = $1 AND mm.status = 'active' AND ms.status = 'active' AND ms.property_id = $2
		AND coalesce((t.entitlements->>'memberRate')::boolean, false))`, customer, property).Scan(&ok)
	return ok, err
}

// CreateOrder creates (or returns, for a known client id) an order.
func (m *Module) CreateOrder(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OrderInput) (Order, error) {
	if in.ID != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.orders WHERE id = $1)`, *in.ID).Scan(&exists); err != nil {
			return Order{}, err
		}
		if exists {
			return m.Order(ctx, tx, *in.ID)
		}
	}
	ou, err := loadOutlet(ctx, tx, in.OutletID)
	if err != nil {
		return Order{}, err
	}
	if ou.PropertyID != property || ou.Status != "active" {
		return Order{}, errs.Validation("outlet_unavailable", "outlet not available")
	}
	if len(in.Lines) == 0 {
		return Order{}, handle.Invalid("lines", "required", "at least one item is required")
	}
	defaults := map[string]*string{"orderType": &in.OrderType, "source": &in.Source, "servingDestination": &in.ServingDestination}
	for k, v := range map[string]string{"orderType": "dine_in", "source": "pos", "servingDestination": "table"} {
		if *defaults[k] == "" {
			*defaults[k] = v
		}
	}
	memberPricing := false
	if in.CustomerID != nil {
		mem, err := isMember(ctx, tx, property, *in.CustomerID)
		if err != nil {
			return Order{}, err
		}
		memberPricing = mem
	}
	if in.MemberPricing != nil {
		memberPricing = *in.MemberPricing && in.CustomerID != nil
	}
	oid := id.New()
	if in.ID != nil {
		oid = *in.ID
	}
	loc := calendar.Location(ctx, tx)
	no, err := numbering.Next(ctx, tx, property, "ORD", clock.Now().In(loc))
	if err != nil {
		return Order{}, err
	}
	var guests *int
	if in.GuestCount > 0 {
		guests = &in.GuestCount
	}
	p := authz.From(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.orders (id, property_id, order_no, outlet_id, shift_id, order_type, source, table_no, guest_count,
		customer_id, member_pricing, serving_destination, destination_ref, scheduled_for, charge_folio_id, offline, device_id, notes, client_created_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		oid, property, no, ou.ID, in.ShiftID, in.OrderType, in.Source, nullStr(in.TableNo), guests, in.CustomerID, memberPricing, in.ServingDestination,
		nullStr(in.DestinationRef), in.ScheduledFor, in.ChargeFolioID, in.Offline, deviceOf(p), nullStr(in.Notes), in.ClientCreatedAt, actorID(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return Order{}, errs.Validation("invalid_reference", "customer, shift or folio not found")
		}
		return Order{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.order_bills (id, property_id, order_id, bill_no, customer_id) VALUES ($1,$2,$3,1,$4)`,
		id.New(), property, oid, in.CustomerID); err != nil {
		return Order{}, err
	}
	if err := m.addLines(ctx, tx, property, oid, ou, memberPricing, in.Lines); err != nil {
		return Order{}, err
	}
	o, err := m.Order(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionCreate, EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: no + " · " + ou.Name, PropertyID: &property, After: o}); err != nil {
		return o, err
	}
	if in.Send || in.Source != "pos" {
		if o, err = m.SendToKitchen(ctx, tx, oid); err != nil {
			return o, err
		}
	}
	if in.ChargeFolioID != nil {
		return m.ChargeToFolio(ctx, tx, oid, *in.ChargeFolioID)
	}
	return o, nil
}

func deviceOf(p *authz.Principal) *uuid.UUID {
	if p == nil {
		return nil
	}
	return p.DeviceID
}

// AddLines adds items to an open order.
func (m *Module) AddLines(ctx context.Context, tx pgx.Tx, oid uuid.UUID, lines []LineInput) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "order "+o.OrderNo+" is "+o.Status)
	}
	ou, err := loadOutlet(ctx, tx, o.OutletID)
	if err != nil {
		return o, err
	}
	if err := m.addLines(ctx, tx, o.PropertyID, oid, ou, o.MemberPricing, lines); err != nil {
		return o, err
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionUpdate, EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Before: o, After: after})
}

func (m *Module) addLines(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, ou outlet, member bool, lines []LineInput) error {
	var next int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(line_no), 0) FROM commercial.order_lines WHERE order_id = $1`, oid).Scan(&next); err != nil {
		return err
	}
	var billID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM commercial.order_bills WHERE order_id = $1 ORDER BY bill_no LIMIT 1`, oid).Scan(&billID); err != nil {
		return err
	}
	pol, err := m.posPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	for i, li := range lines {
		pr, err := loadProduct(ctx, tx, li.ProductID)
		if err != nil {
			return errs.Validation("invalid_product", fmt.Sprintf("line %d: product not found", i+1))
		}
		if pr.Status != "active" {
			return errs.Conflict("product_inactive", pr.Name+" is not available")
		}
		if len(pr.OutletIDs) > 0 && !slices.Contains(pr.OutletIDs, ou.ID.String()) {
			return errs.Conflict("product_not_at_outlet", pr.Name+" is not sold at "+ou.Name)
		}
		qty, err := handle.Decimal("quantity", li.Quantity, decimal.NewFromInt(1))
		if err != nil || !qty.IsPositive() {
			return handle.Invalid("quantity", "invalid_quantity", "quantity must be positive")
		}
		base, _ := decimal.NewFromString(pr.Price)
		if member && pr.MemberPrice != nil {
			base, _ = decimal.NewFromString(*pr.MemberPrice)
		}
		name := pr.Name
		if li.VariantID != nil {
			var vname, delta string
			if err := tx.QueryRow(ctx, `SELECT name, price_delta::text FROM commercial.product_variants WHERE id = $1 AND product_id = $2 AND status = 'active'`,
				*li.VariantID, pr.ID).Scan(&vname, &delta); err != nil {
				return errs.Validation("invalid_variant", "variant not found for "+pr.Name)
			}
			d, _ := decimal.NewFromString(delta)
			base = base.Add(d)
			name += " (" + vname + ")"
		}
		mods := []map[string]any{}
		perGroup := map[uuid.UUID]int{}
		for _, mid := range li.ModifierIDs {
			var gid uuid.UUID
			var mname, delta string
			var prodIDs []string
			if err := tx.QueryRow(ctx, `SELECT m.group_id, m.name, m.price_delta::text, g.product_ids FROM commercial.modifiers m
				JOIN commercial.modifier_groups g ON g.id = m.group_id WHERE m.id = $1 AND m.status = 'active'`, mid).Scan(&gid, &mname, &delta, &prodIDs); err != nil {
				return errs.Validation("invalid_modifier", "modifier not found")
			}
			if len(prodIDs) > 0 && !slices.Contains(prodIDs, pr.ID.String()) {
				return errs.Validation("invalid_modifier", mname+" does not apply to "+pr.Name)
			}
			perGroup[gid]++
			d, _ := decimal.NewFromString(delta)
			base = base.Add(d)
			mods = append(mods, map[string]any{"modifierId": mid, "name": mname, "priceDelta": d.String()})
		}
		// group min / max (FR-POS-02)
		rows, err := tx.Query(ctx, `SELECT id, name, min_select, max_select FROM commercial.modifier_groups WHERE status = 'active' AND property_id = $1
			AND ($2 = ANY(product_ids))`, property, pr.ID.String())
		if err != nil {
			return err
		}
		for rows.Next() {
			var gid uuid.UUID
			var gname string
			var mn, mx int
			if err := rows.Scan(&gid, &gname, &mn, &mx); err != nil {
				rows.Close()
				return err
			}
			if perGroup[gid] < mn || perGroup[gid] > mx {
				rows.Close()
				return errs.Validation("modifier_selection", fmt.Sprintf("%s: choose %d–%d %s", pr.Name, mn, mx, gname))
			}
		}
		rows.Close()
		station := ou.firstStation(pol.DefaultKitchenStation)
		if pr.KitchenStation != nil && *pr.KitchenStation != "" {
			station = *pr.KitchenStation
		}
		var st *string
		if pr.ProductType == "food" || pr.ProductType == "beverage" {
			st = &station
		}
		net, svc, tax, total, snap, err := m.priceLine(ctx, tx, property, ou, pr, member, base, qty, decimal.Zero, nil)
		if err != nil {
			return err
		}
		next++
		modsRaw, _ := json.Marshal(mods)
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.order_lines (id, property_id, order_id, line_no, product_id, variant_id, name, quantity, unit_price,
			modifiers, net_amount, service_amount, tax_amount, total_amount, pricing_snapshot_id, kitchen_station, bill_id, seat, notes, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15,$16,$17,$18,$19,$20)`,
			id.New(), property, oid, next, pr.ID, li.VariantID, name, qty.String(), base.String(), modsRaw, net.String(), svc.String(), tax.String(),
			total.String(), snap, st, billID, nullStr(li.Seat), nullStr(li.Notes), actorID(ctx)); err != nil {
			return err
		}
	}
	return nil
}

func (o outlet) firstStation(def string) string {
	if len(o.KDSStations) > 0 {
		return o.KDSStations[0]
	}
	return def
}

// priceLine prices quantity × unit price minus discount with the outlet tax
// & service, storing an immutable snapshot (FR-PRC-P2-09/10).
func (m *Module) priceLine(ctx context.Context, tx pgx.Tx, property uuid.UUID, ou outlet, pr product, member bool, unit, qty, discount decimal.Decimal,
	override map[string]any) (net, svc, tax, total decimal.Decimal, snap *uuid.UUID, err error) {
	seg := "any"
	if member {
		seg = "member"
	}
	gross := unit.Mul(qty).Sub(discount)
	if gross.IsNegative() {
		return net, svc, tax, total, nil, handle.Invalid("discount", "invalid_discount", "discount exceeds the line amount")
	}
	unitAfter := gross.Div(qty)
	sid, b, err := commercial.Pricer{}.ManualSnapshot(ctx, tx, property, "pos", pr.Code, seg, qty, unitAfter, ou.PricingMode, ou.TaxCodes, clock.Now(), override)
	if err != nil {
		return net, svc, tax, total, nil, err
	}
	net, _ = decimal.NewFromString(b.NetAmount)
	total, _ = decimal.NewFromString(b.Total)
	return net, commercial.SumKind(b.Lines, "service"), commercial.SumKind(b.Lines, "tax"), total, &sid, nil
}

func (m *Module) lockOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (Order, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM commercial.orders WHERE id = $1 FOR UPDATE`, oid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Order{}, errs.NotFound("order")
		}
		return Order{}, err
	}
	return m.Order(ctx, tx, oid)
}

// DiscountInput applies a manual discount to a line (FR-POS-03).
type DiscountInput struct {
	Percent string `json:"percent,omitempty"`
	Amount  string `json:"amount,omitempty"`
	Reason  string `json:"reason"`
}

// Discount applies a manual discount; above the POS Policies limit the
// permission commercial.pos.discount_override (supervisor) is required.
func (m *Module) Discount(ctx context.Context, tx pgx.Tx, oid, lineID uuid.UUID, in DiscountInput) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "order is "+o.Status)
	}
	if strings.TrimSpace(in.Reason) == "" {
		return o, handle.Invalid("reason", "required", "a reason is required for a discount")
	}
	var line *OrderLine
	for i := range o.Lines {
		if o.Lines[i].ID == lineID && o.Lines[i].Status == "active" {
			line = &o.Lines[i]
		}
	}
	if line == nil || line.ChargedFolioID != nil {
		return o, errs.NotFound("open order line")
	}
	unit, _ := decimal.NewFromString(line.UnitPrice)
	qty, _ := decimal.NewFromString(line.Quantity)
	gross := unit.Mul(qty)
	var disc decimal.Decimal
	switch {
	case in.Percent != "":
		p, err := handle.Decimal("percent", in.Percent, decimal.Zero)
		if err != nil {
			return o, err
		}
		disc = gross.Mul(p).Div(decimal.NewFromInt(100)).Round(0)
	default:
		if disc, err = handle.Decimal("amount", in.Amount, decimal.Zero); err != nil {
			return o, err
		}
	}
	pol, err := m.posPolicy(ctx, tx, o.PropertyID)
	if err != nil {
		return o, err
	}
	limit, _ := decimal.NewFromString(pol.MaxDiscountPercent)
	if gross.IsPositive() && disc.Mul(decimal.NewFromInt(100)).Div(gross).GreaterThan(limit) {
		if err := authz.RequireAt(ctx, "commercial.pos.discount_override", o.PropertyID); err != nil {
			return o, errs.Forbidden("discounts above " + limit.String() + "% need a supervisor (POS Policies)")
		}
	}
	ou, err := loadOutlet(ctx, tx, o.OutletID)
	if err != nil {
		return o, err
	}
	pr, err := loadProduct(ctx, tx, line.ProductID)
	if err != nil {
		return o, err
	}
	net, svc, tax, total, snap, err := m.priceLine(ctx, tx, o.PropertyID, ou, pr, o.MemberPricing, unit, qty, disc,
		map[string]any{"discount": disc.String(), "reason": in.Reason})
	if err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET discount_amount = $2::numeric, discount_reason = $3, net_amount = $4::numeric,
		service_amount = $5::numeric, tax_amount = $6::numeric, total_amount = $7::numeric, pricing_snapshot_id = $8 WHERE id = $1`,
		lineID, disc.String(), in.Reason, net.String(), svc.String(), tax.String(), total.String(), snap); err != nil {
		return o, err
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "discount", EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo + " · " + line.Name, PropertyID: &o.PropertyID, Reason: in.Reason,
		Metadata: map[string]any{"lineId": lineID, "discount": disc.String()}})
}

// VoidLine voids an unpaid line (kitchen ticket updated).
func (m *Module) VoidLine(ctx context.Context, tx pgx.Tx, oid, lineID uuid.UUID, reason string) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "paid orders are refunded, not voided")
	}
	tag, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET status = 'voided', void_reason = $3 WHERE id = $1 AND order_id = $2 AND status = 'active'
		AND charged_folio_id IS NULL`, lineID, oid, reason)
	if err != nil {
		return o, err
	}
	if tag.RowsAffected() == 0 {
		return o, errs.NotFound("open order line")
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.kitchen_tickets SET line_ids = array_remove(line_ids, $2) WHERE order_id = $1`, oid, lineID); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.kitchen_tickets SET status = 'cancelled' WHERE order_id = $1 AND cardinality(line_ids) = 0`, oid); err != nil {
		return o, err
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	if err := publishRT(ctx, tx, "kds", o.PropertyID, "order_changed", oid.String(), nil); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionVoid, EntityType: "commercial.order_line",
		EntityID: lineID.String(), EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Reason: reason})
}

// VoidOrder voids an unpaid order.
func (m *Module) VoidOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, reason string) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "paid orders are refunded, not voided")
	}
	for _, l := range o.Lines {
		if l.ChargedFolioID != nil {
			return o, errs.Conflict("partly_paid", "the order is partly paid; refund it instead")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET status = 'voided', void_reason = $2 WHERE order_id = $1 AND status = 'active'`, oid, reason); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.kitchen_tickets SET status = 'cancelled' WHERE order_id = $1 AND status <> 'served'`, oid); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET status = 'voided', void_reason = $2 WHERE id = $1`, oid, reason); err != nil {
		return o, err
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	if err := publishRT(ctx, tx, "kds", o.PropertyID, "order_changed", oid.String(), nil); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionVoid, EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Before: o, After: after, Reason: reason})
}

// ── kitchen (KDS) ─────────────────────────────────────────────────────────

// SendToKitchen creates kitchen tickets per station for unsent lines.
func (m *Module) SendToKitchen(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	groups := map[string][]uuid.UUID{}
	var order []string
	for _, l := range o.Lines {
		if l.Status != "active" || l.SentAt != nil || l.KitchenStation == nil {
			continue
		}
		if _, ok := groups[*l.KitchenStation]; !ok {
			order = append(order, *l.KitchenStation)
		}
		groups[*l.KitchenStation] = append(groups[*l.KitchenStation], l.ID)
	}
	due := clock.Now()
	if o.ScheduledFor != nil {
		due = *o.ScheduledFor
	}
	for _, st := range order {
		tid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.kitchen_tickets (id, property_id, order_id, station, line_ids, due_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			tid, o.PropertyID, oid, st, groups[st], due); err != nil {
			return o, err
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET sent_at = now() WHERE id = ANY($1)`, groups[st]); err != nil {
			return o, err
		}
		if err := publishRT(ctx, tx, "kds", o.PropertyID, "ticket_received", tid.String(), map[string]any{"station": st, "orderNo": o.OrderNo}); err != nil {
			return o, err
		}
	}
	if len(order) > 0 && (o.ServiceStatus == "new" || o.ServiceStatus == "served") {
		if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET service_status = 'sent' WHERE id = $1`, oid); err != nil {
			return o, err
		}
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	if len(order) == 0 {
		return after, nil
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "send_to_kitchen", EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Metadata: map[string]any{"stations": order}})
}

// Ticket is a Kitchen Display ticket.
type Ticket struct {
	ID                 uuid.UUID    `json:"id" db:"id"`
	OrderID            uuid.UUID    `json:"orderId" db:"order_id"`
	OrderNo            string       `json:"orderNo" db:"order_no"`
	OutletName         string       `json:"outletName" db:"outlet_name"`
	Station            string       `json:"station" db:"station"`
	Status             string       `json:"status" db:"status" enum:"received,preparing,ready,out_for_delivery,served,cancelled"`
	Source             string       `json:"source" db:"source"`
	OrderType          string       `json:"orderType" db:"order_type"`
	TableNo            *string      `json:"tableNo" db:"table_no"`
	ServingDestination string       `json:"servingDestination" db:"serving_destination"`
	DestinationRef     *string      `json:"destinationRef" db:"destination_ref"`
	DueAt              *time.Time   `json:"dueAt" db:"due_at"`
	ReceivedAt         time.Time    `json:"receivedAt" db:"received_at"`
	ReadyAt            *time.Time   `json:"readyAt" db:"ready_at"`
	ServedAt           *time.Time   `json:"servedAt" db:"served_at"`
	Items              []TicketItem `json:"items" db:"-"`
}

// TicketItem is one item on a ticket.
type TicketItem struct {
	Name      string           `json:"name" db:"name"`
	Quantity  string           `json:"quantity" db:"quantity"`
	Modifiers []map[string]any `json:"modifiers" db:"modifiers"`
	Notes     *string          `json:"notes" db:"notes"`
	Status    string           `json:"status" db:"status"`
}

const ticketSelect = `SELECT t.id, t.order_id, o.order_no, ou.name AS outlet_name, t.station, t.status, o.source, o.order_type, o.table_no,
	o.serving_destination, o.destination_ref, t.due_at, t.received_at, t.ready_at, t.served_at
	FROM commercial.kitchen_tickets t JOIN commercial.orders o ON o.id = t.order_id JOIN commercial.outlets ou ON ou.id = o.outlet_id`

func (m *Module) tickets(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]Ticket, error) {
	list, err := handle.List[Ticket](q.Query(ctx, ticketSelect+` WHERE `+where+` ORDER BY coalesce(t.due_at, t.received_at), t.received_at`, args...))
	if err != nil {
		return list, err
	}
	for i := range list {
		if list[i].Items, err = handle.List[TicketItem](q.Query(ctx, `SELECT l.name, trim_scale(l.quantity)::text AS quantity, l.modifiers, l.notes, l.status
			FROM commercial.order_lines l JOIN commercial.kitchen_tickets t ON l.id = ANY(t.line_ids) WHERE t.id = $1 ORDER BY l.line_no`, list[i].ID)); err != nil {
			return list, err
		}
	}
	return list, nil
}

var ticketFlow = map[string][]string{
	"received":         {"preparing", "ready", "cancelled"},
	"preparing":        {"ready", "cancelled"},
	"ready":            {"out_for_delivery", "served"},
	"out_for_delivery": {"served"},
}

// SetTicketState moves a ticket along Received → Preparing → Ready → Out for
// Delivery → Served (FR-FNB-04); the orderer is notified when it is Ready.
func (m *Module) SetTicketState(ctx context.Context, tx pgx.Tx, tid uuid.UUID, state string) (Ticket, error) {
	var cur string
	var oid, pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, order_id, property_id FROM commercial.kitchen_tickets WHERE id = $1 FOR UPDATE`, tid).Scan(&cur, &oid, &pid); err != nil {
		if dbtx.IsNoRows(err) {
			return Ticket{}, errs.NotFound("kitchen ticket")
		}
		return Ticket{}, err
	}
	if !slices.Contains(ticketFlow[cur], state) {
		return Ticket{}, errs.Conflict("invalid_transition", "a "+cur+" ticket cannot become "+state)
	}
	col := map[string]string{"preparing": "preparing_at", "ready": "ready_at", "out_for_delivery": "out_at", "served": "served_at", "cancelled": "updated_at"}[state]
	if _, err := tx.Exec(ctx, `UPDATE commercial.kitchen_tickets SET status = $2, `+col+` = now() WHERE id = $1`, tid, state); err != nil {
		return Ticket{}, err
	}
	// order service status = the least advanced active ticket
	var minStatus *string
	if err := tx.QueryRow(ctx, `SELECT status FROM commercial.kitchen_tickets WHERE order_id = $1 AND status <> 'cancelled'
		ORDER BY array_position(ARRAY['received','preparing','ready','out_for_delivery','served'], status) LIMIT 1`, oid).Scan(&minStatus); err != nil && !dbtx.IsNoRows(err) {
		return Ticket{}, err
	}
	if minStatus != nil {
		svc := *minStatus
		if svc == "received" {
			svc = "sent"
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET service_status = $2 WHERE id = $1`, oid, svc); err != nil {
			return Ticket{}, err
		}
		if svc == "ready" && state == "ready" {
			o, err := m.Order(ctx, tx, oid)
			if err != nil {
				return Ticket{}, err
			}
			if _, err := m.Events.Publish(ctx, tx, "commercial.order_ready", "commercial.order", &oid, &pid, map[string]any{"orderId": oid,
				"orderNo": o.OrderNo, "customerId": o.CustomerID, "source": o.Source, "destination": o.ServingDestination, "destinationRef": o.DestinationRef}); err != nil {
				return Ticket{}, err
			}
			if o.CustomerID != nil && (o.Source == "member_app" || o.Source == "website") {
				var user *uuid.UUID
				_ = tx.QueryRow(ctx, `SELECT user_id FROM crm.customers WHERE id = $1`, *o.CustomerID).Scan(&user)
				if user != nil {
					if err := m.Notify.Send(ctx, tx, notify.Message{Event: "commercial.order_ready", Category: "order", UserIDs: []uuid.UUID{*user}, PropertyID: &pid,
						Channels: []string{notify.ChannelInApp}, Data: map[string]any{"name": deref(o.CustomerName), "orderNo": o.OrderNo,
							"destination": deref(o.DestinationRef)}}); err != nil {
						return Ticket{}, err
					}
				}
			}
		}
	}
	if err := publishRT(ctx, tx, "kds", pid, "ticket_"+state, tid.String(), map[string]any{"orderId": oid}); err != nil {
		return Ticket{}, err
	}
	if err := publishRT(ctx, tx, "orders", pid, "order_"+state, oid.String(), nil); err != nil {
		return Ticket{}, err
	}
	list, err := m.tickets(ctx, tx, `t.id = $1`, tid)
	if err != nil || len(list) == 0 {
		return Ticket{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionStatusChange, EntityType: "commercial.kitchen_ticket",
		EntityID: tid.String(), EntityLabel: list[0].OrderNo + " · " + list[0].Station, PropertyID: &pid,
		Before: map[string]any{"status": cur}, After: map[string]any{"status": state}})
}

// ── bills & payment ───────────────────────────────────────────────────────

// SplitInput splits an order (FR-POS-06).
type SplitInput struct {
	Mode    string      `json:"mode" enum:"per_item,equal"`
	Bills   []SplitBill `json:"bills,omitempty" doc:"per_item: lines per bill"`
	Persons int         `json:"persons,omitempty" doc:"equal: number of bills"`
}

type SplitBill struct {
	Label      string      `json:"label,omitempty"`
	CustomerID *uuid.UUID  `json:"customerId,omitempty"`
	LineIDs    []uuid.UUID `json:"lineIds"`
}

// Split re-groups the unpaid order into bills.
func (m *Module) Split(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in SplitInput) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "order is "+o.Status)
	}
	for _, b := range o.Bills {
		if b.FolioID != nil {
			return o, errs.Conflict("payment_started", "payment has started; the bill can no longer be split")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET bill_id = NULL WHERE order_id = $1`, oid); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM commercial.order_bills WHERE order_id = $1`, oid); err != nil {
		return o, err
	}
	switch in.Mode {
	case "equal":
		if in.Persons < 2 || in.Persons > 20 {
			return o, handle.Invalid("persons", "invalid", "split equally between 2 and 20 persons")
		}
		share := decimal.NewFromInt(1).Div(decimal.NewFromInt(int64(in.Persons)))
		var first uuid.UUID
		for i := 1; i <= in.Persons; i++ {
			bid := id.New()
			if i == 1 {
				first = bid
			}
			if _, err := tx.Exec(ctx, `INSERT INTO commercial.order_bills (id, property_id, order_id, bill_no, label, share, customer_id) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7)`,
				bid, o.PropertyID, oid, i, fmt.Sprintf("Person %d", i), share.StringFixed(6), o.CustomerID); err != nil {
				return o, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET bill_id = $2 WHERE order_id = $1`, oid, first); err != nil {
			return o, err
		}
	case "per_item":
		if len(in.Bills) < 2 {
			return o, handle.Invalid("bills", "invalid", "split per item needs at least 2 bills")
		}
		assigned := map[uuid.UUID]bool{}
		for i, b := range in.Bills {
			bid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO commercial.order_bills (id, property_id, order_id, bill_no, label, customer_id) VALUES ($1,$2,$3,$4,$5,$6)`,
				bid, o.PropertyID, oid, i+1, nullStr(b.Label), b.CustomerID); err != nil {
				return o, err
			}
			for _, lid := range b.LineIDs {
				if assigned[lid] {
					return o, handle.Invalid("bills", "duplicate_line", "a line can be on one bill only")
				}
				assigned[lid] = true
				tag, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET bill_id = $3 WHERE id = $1 AND order_id = $2 AND status = 'active'`, lid, oid, bid)
				if err != nil {
					return o, err
				}
				if tag.RowsAffected() == 0 {
					return o, handle.Invalid("bills", "invalid_line", "line not found in this order")
				}
			}
		}
		for _, l := range o.Lines {
			if l.Status == "active" && !assigned[l.ID] {
				return o, handle.Invalid("bills", "unassigned_line", l.Name+" is not on any bill")
			}
		}
	default:
		return o, handle.Invalid("mode", "invalid_mode", "mode must be per_item or equal")
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "split", EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Before: o, After: after})
}

// TenderInput is one tender of a (multi-tender) payment.
type TenderInput struct {
	MethodType string         `json:"methodType" enum:"cash,bank_transfer,qris,card,member_account,voucher_prepaid,folio_transfer,payment_gateway"`
	Amount     string         `json:"amount,omitempty" doc:"Default: the remaining balance"`
	Reference  string         `json:"reference,omitempty"`
	Tender     map[string]any `json:"tender,omitempty" doc:"voucher_prepaid {code}; folio_transfer {targetFolioId}"`
}

// PayInput pays an order or one of its bills (FR-POS-08).
type PayInput struct {
	BillID     *uuid.UUID    `json:"billId,omitempty"`
	CustomerID *uuid.UUID    `json:"customerId,omitempty" doc:"Member for member charge on this bill"`
	ShiftID    *uuid.UUID    `json:"shiftId,omitempty"`
	Tenders    []TenderInput `json:"tenders"`
	Offline    bool          `json:"offline,omitempty"`
	ReceivedAt *time.Time    `json:"receivedAt,omitempty" doc:"Offline: when the payment was taken"`
}

func (m *Module) billFolio(ctx context.Context, tx pgx.Tx, o Order, b Bill, customer *uuid.UUID) (uuid.UUID, error) {
	if b.FolioID != nil {
		return *b.FolioID, nil
	}
	if b.Share != nil {
		// equal split shares one folio
		var shared *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT folio_id FROM commercial.order_bills WHERE order_id = $1 AND folio_id IS NOT NULL LIMIT 1`, o.ID).Scan(&shared); err == nil && shared != nil {
			_, err := tx.Exec(ctx, `UPDATE commercial.order_bills SET folio_id = $2 WHERE id = $1`, b.ID, *shared)
			return *shared, err
		}
	}
	cust := customer
	if cust == nil {
		cust = b.CustomerID
	}
	if cust == nil {
		cust = o.CustomerID
	}
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: o.PropertyID, CustomerID: cust, SourceType: "pos_order", SourceID: &o.ID}, BusinessLine: billing.LinePOS})
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.order_bills SET folio_id = $2, customer_id = coalesce(customer_id, $3) WHERE id = $1`, b.ID, f.ID, cust); err != nil {
		return uuid.Nil, err
	}
	return f.ID, nil
}

// chargeLines posts the order lines to a folio (once) and issues vouchers
// for products that sell vouchers (e.g. a ball package at the range counter).
func (m *Module) chargeLines(ctx context.Context, tx pgx.Tx, o Order, folioID uuid.UUID, lines []OrderLine) error {
	ou, err := loadOutlet(ctx, tx, o.OutletID)
	if err != nil {
		return err
	}
	line := billing.LinePOS
	for _, l := range lines {
		if l.Status != "active" || l.ChargedFolioID != nil {
			continue
		}
		pr, err := loadProduct(ctx, tx, l.ProductID)
		if err != nil {
			return err
		}
		comp := ou.RevenueComponent
		if pr.RevenueComponent != nil && *pr.RevenueComponent != "" {
			comp = *pr.RevenueComponent
		} else if pr.ProductType == "retail" {
			comp = "pro_shop"
		}
		net, _ := decimal.NewFromString(l.NetAmount)
		svc, _ := decimal.NewFromString(l.ServiceAmount)
		tax, _ := decimal.NewFromString(l.TaxAmount)
		qty, _ := decimal.NewFromString(l.Quantity)
		liability := false
		lid := l.ID
		var snap *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT pricing_snapshot_id FROM commercial.order_lines WHERE id = $1`, lid).Scan(&snap)
		if pr.VoucherTypeID != nil {
			comp, liability = "voucher_deferred", true
			total := net.Add(svc).Add(tax)
			for i := 0; i < int(qty.IntPart()); i++ {
				if _, err := m.Vouchers.Issue(ctx, tx, voucher.IssueRequest{PropertyID: o.PropertyID, TypeID: pr.VoucherTypeID, CustomerID: o.CustomerID, Via: "sale",
					PricePaid: total.Div(qty), FolioID: &folioID, SourceType: "commercial.order_line", SourceID: &lid}); err != nil {
					return err
				}
			}
		}
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folioID, ReferenceType: "commercial.order_line", ReferenceID: &lid, Description: l.Name, Quantity: qty, Net: net, Service: svc, Tax: tax, SnapshotID: snap, Liability: liability}, BusinessLine: line, RevenueComponent: comp}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET charged_folio_id = $2 WHERE id = $1`, l.ID, folioID); err != nil {
			return err
		}
	}
	return nil
}

// Pay takes one or more tenders for a bill (default: the first open bill).
func (m *Module) Pay(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in PayInput, key string) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status == "paid" {
		return o, nil // offline retry after success
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "order is "+o.Status)
	}
	if len(in.Tenders) == 0 {
		return o, handle.Invalid("tenders", "required", "at least one tender is required")
	}
	var bill *Bill
	for i := range o.Bills {
		if (in.BillID != nil && o.Bills[i].ID == *in.BillID) || (in.BillID == nil && o.Bills[i].Status == "open" && bill == nil) {
			bill = &o.Bills[i]
		}
	}
	if bill == nil || bill.Status != "open" {
		return o, errs.Conflict("bill_not_open", "no open bill to pay")
	}
	fid, err := m.billFolio(ctx, tx, o, *bill, in.CustomerID)
	if err != nil {
		return o, err
	}
	var lines []OrderLine
	for _, l := range o.Lines {
		if l.BillID != nil && *l.BillID == bill.ID {
			lines = append(lines, l)
		}
	}
	if err := m.chargeLines(ctx, tx, o, fid, lines); err != nil {
		return o, err
	}
	shift := in.ShiftID
	if shift == nil {
		shift = o.ShiftID
	}
	due, _ := decimal.NewFromString(bill.Total)
	paid, _ := decimal.NewFromString(bill.Paid)
	remaining := due.Sub(paid)
	for i, t := range in.Tenders {
		f, err := billing.GetFolio(ctx, tx, fid)
		if err != nil {
			return o, err
		}
		bal, _ := decimal.NewFromString(f.Balance)
		def := remaining
		if bill.Share == nil || remaining.GreaterThan(bal) {
			def = bal
		}
		amt, err := handle.Decimal(fmt.Sprintf("tenders[%d].amount", i), t.Amount, def)
		if err != nil {
			return o, err
		}
		if !amt.IsPositive() {
			continue
		}
		tender := t.Tender
		if tender == nil {
			tender = map[string]any{}
		}
		tender["billId"] = bill.ID.String()
		pk := ""
		if key != "" {
			pk = fmt.Sprintf("%s-%d", key, i)
		}
		p, err := m.Billing.TakeTender(ctx, tx, billing.TenderPaymentInput{PaymentInput: billing.PaymentInput{FolioID: &fid, MethodType: t.MethodType, Amount: amt, Reference: t.Reference}, Tender: tender, OutletID: &o.OutletID, ShiftID: shift, Offline: in.Offline, IdempotencyKey: pk})
		if err != nil {
			return o, err
		}
		if bill.Share != nil {
			// keep the bill reference on the payment for equal splits
			if _, err := tx.Exec(ctx, `UPDATE billing.payments SET tender_ref = tender_ref || jsonb_build_object('billId', $2::text) WHERE id = $1`, p.ID, bill.ID); err != nil {
				return o, err
			}
		}
		if pt, err := billing.TenderOf(ctx, tx, p.ID); err != nil {
			return o, err
		} else if pt.NeedsReview {
			if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET needs_review = true WHERE id = $1`, oid); err != nil {
				return o, err
			}
		}
		pa, _ := decimal.NewFromString(p.Amount)
		remaining = remaining.Sub(pa)
	}
	f, err := billing.GetFolio(ctx, tx, fid)
	if err != nil {
		return o, err
	}
	folioBal, _ := decimal.NewFromString(f.Balance)
	if (bill.Share == nil && !folioBal.IsPositive()) || (bill.Share != nil && !remaining.IsPositive()) {
		if _, err := tx.Exec(ctx, `UPDATE commercial.order_bills SET status = 'paid' WHERE id = $1`, bill.ID); err != nil {
			return o, err
		}
	}
	if !folioBal.IsPositive() && f.Status == "open" {
		var openBills int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM commercial.order_bills WHERE folio_id = $1 AND status = 'open'`, fid).Scan(&openBills); err != nil {
			return o, err
		}
		if openBills == 0 {
			if err := m.Billing.CloseFolio(ctx, tx, fid); err != nil {
				return o, err
			}
		}
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM commercial.order_bills WHERE order_id = $1 AND status = 'open'`, oid).Scan(&open); err != nil {
		return o, err
	}
	if open == 0 {
		return m.complete(ctx, tx, oid, "paid")
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "pay", EntityType: "commercial.order", EntityID: oid.String(),
		EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Metadata: map[string]any{"billId": bill.ID}})
}

// ChargeToFolio posts the whole order to a running stay / reservation folio
// (Charge to Stay, VIP suite add-ons, meeting catering — FR-BIL-P2-04).
func (m *Module) ChargeToFolio(ctx context.Context, tx pgx.Tx, oid, folioID uuid.UUID) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "order is "+o.Status)
	}
	f, err := billing.GetFolio(ctx, tx, folioID)
	if err != nil {
		return o, err
	}
	if f.Status != "open" {
		return o, errs.Conflict("folio_closed", "the target folio is not open")
	}
	if err := m.chargeLines(ctx, tx, o, folioID, o.Lines); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET charge_folio_id = $2 WHERE id = $1`, oid, folioID); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.order_bills SET status = 'paid', folio_id = $2 WHERE order_id = $1`, oid, folioID); err != nil {
		return o, err
	}
	return m.complete(ctx, tx, oid, "charged")
}

// complete closes the sale and publishes commercial.sale_completed (FR-POS-14).
func (m *Module) complete(ctx context.Context, tx pgx.Tx, oid uuid.UUID, status string) (Order, error) {
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET status = $2 WHERE id = $1`, oid, status); err != nil {
		return Order{}, err
	}
	o, err := m.Order(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	lines := []map[string]any{}
	for _, l := range o.Lines {
		if l.Status != "active" {
			continue
		}
		mods := []string{}
		for _, md := range l.Modifiers {
			mods = append(mods, fmt.Sprint(md["modifierId"]))
		}
		lines = append(lines, map[string]any{"lineId": l.ID, "productId": l.ProductID, "variantId": l.VariantID, "quantity": l.Quantity,
			"modifierIds": mods, "netAmount": l.NetAmount, "totalAmount": l.TotalAmount})
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "complete", EntityType: "commercial.order", EntityID: oid.String(),
		EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, After: map[string]any{"status": status, "total": o.Total}}); err != nil {
		return o, err
	}
	_, err = m.Events.Publish(ctx, tx, "commercial.sale_completed", "commercial.order", &oid, &o.PropertyID, map[string]any{"orderId": oid,
		"orderNo": o.OrderNo, "outletId": o.OutletID, "customerId": o.CustomerID, "source": o.Source, "total": o.Total, "lines": lines,
		"at": clock.Now()})
	return o, err
}

// RefundInput requests a refund of a paid order (approval).
type RefundInput struct {
	Reason string `json:"reason"`
}

// OrderRequest is a refund waiting for approval.
type OrderRequest struct {
	ID         uuid.UUID  `json:"id"`
	OrderID    uuid.UUID  `json:"orderId"`
	Status     string     `json:"status"`
	ApprovalID *uuid.UUID `json:"approvalId"`
}

// RequestRefund submits a refund of a paid order for approval (FR-POS-09).
func (m *Module) RequestRefund(ctx context.Context, tx pgx.Tx, oid uuid.UUID, reason string) (OrderRequest, error) {
	o, err := m.Order(ctx, tx, oid)
	if err != nil {
		return OrderRequest{}, err
	}
	if o.Status != "paid" {
		return OrderRequest{}, errs.Conflict("not_paid", "only paid orders can be refunded")
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.order_requests (id, property_id, order_id, request_type, reason, created_by) VALUES ($1,$2,$3,'refund',$4,$5)`,
		rid, o.PropertyID, oid, reason, actorID(ctx)); err != nil {
		return OrderRequest{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionCreate, EntityType: "commercial.order_request",
		EntityID: rid.String(), EntityLabel: "Refund " + o.OrderNo, PropertyID: &o.PropertyID, Reason: reason}); err != nil {
		return OrderRequest{}, err
	}
	aid, status, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: RefundType.Code, DocumentID: rid, DocumentRef: o.OrderNo,
		Title: "Refund " + o.OrderNo + " (" + o.Total + ")", PropertyID: o.PropertyID, Attributes: map[string]any{"amount": o.Total}})
	if err != nil {
		return OrderRequest{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.order_requests SET approval_id = $2 WHERE id = $1`, rid, aid); err != nil {
		return OrderRequest{}, err
	}
	st := "pending"
	if status == approval.StatusApproved {
		st = "applied"
	}
	return OrderRequest{ID: rid, OrderID: oid, Status: st, ApprovalID: &aid}, nil
}

// RefundDecision refunds the payments of an approved refund request.
func (m *Module) RefundDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	var oid uuid.UUID
	var reason string
	err := tx.QueryRow(ctx, `UPDATE commercial.order_requests SET status = $2 WHERE id = $1 AND status = 'pending' RETURNING order_id, reason`, d.DocumentID, st).
		Scan(&oid, &reason)
	if dbtx.IsNoRows(err) || (err == nil && st != "approved") {
		return nil
	}
	if err != nil {
		return err
	}
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return err
	}
	folios := map[uuid.UUID]bool{}
	for _, b := range o.Bills {
		if b.FolioID != nil {
			folios[*b.FolioID] = true
		}
	}
	for fid := range folios {
		f, err := billing.GetFolio(ctx, tx, fid)
		if err != nil {
			return err
		}
		if f.Status == "closed" {
			if err := m.Billing.ReopenFolio(ctx, tx, fid, "refund "+o.OrderNo); err != nil {
				return err
			}
		}
		if _, err := m.Billing.SettleCancellation(ctx, tx, fid, decimal.Zero, billing.LinePOS, "refund: "+reason); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET status = 'refunded', void_reason = $2 WHERE id = $1`, oid, reason); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE commercial.order_requests SET status = 'applied' WHERE id = $1`, d.DocumentID)
	if err != nil {
		return err
	}
	pid := o.PropertyID
	return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "refund", EntityType: "commercial.order", EntityID: oid.String(),
		EntityLabel: o.OrderNo, PropertyID: &pid, Reason: reason})
}

// ── shifts (FR-POS-10) ────────────────────────────────────────────────────

// Shift is a cashier shift with its X / Z figures.
type Shift struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	ShiftNo      string         `json:"shiftNo" db:"shift_no"`
	OutletID     uuid.UUID      `json:"outletId" db:"outlet_id"`
	OutletName   string         `json:"outletName" db:"outlet_name"`
	CashierID    uuid.UUID      `json:"cashierId" db:"cashier_id"`
	CashierName  string         `json:"cashierName" db:"cashier_name"`
	OpeningCash  string         `json:"openingCash" db:"opening_cash"`
	CountedCash  *string        `json:"countedCash" db:"counted_cash"`
	ExpectedCash *string        `json:"expectedCash" db:"expected_cash"`
	Variance     *string        `json:"variance" db:"variance"`
	Status       string         `json:"status" db:"status" enum:"open,closed"`
	OpenedAt     time.Time      `json:"openedAt" db:"opened_at"`
	ClosedAt     *time.Time     `json:"closedAt" db:"closed_at"`
	ZReport      map[string]any `json:"zReport" db:"z_report"`
}

const shiftSelect = `SELECT s.id, s.shift_no, s.outlet_id, o.name AS outlet_name, s.cashier_id, u.full_name AS cashier_name,
	trim_scale(s.opening_cash)::text AS opening_cash, trim_scale(s.counted_cash)::text AS counted_cash, trim_scale(s.expected_cash)::text AS expected_cash,
	trim_scale(s.variance)::text AS variance, s.status, s.opened_at, s.closed_at, s.z_report
	FROM commercial.pos_shifts s JOIN commercial.outlets o ON o.id = s.outlet_id JOIN platform.users u ON u.id = s.cashier_id`

// ShiftReport is the X (running) or Z (closing) report.
type ShiftReport struct {
	Shift        Shift            `json:"shift"`
	Kind         string           `json:"kind" enum:"x,z"`
	Orders       int              `json:"orders"`
	Sales        string           `json:"sales"`
	Payments     []map[string]any `json:"payments"`
	CashIn       string           `json:"cashIn"`
	CashOut      string           `json:"cashOut"`
	CashPayments string           `json:"cashPayments"`
	ExpectedCash string           `json:"expectedCash"`
}

type OpenShiftInput struct {
	OutletID    uuid.UUID `json:"outletId"`
	OpeningCash string    `json:"openingCash"`
}

// OpenShift opens a shift for the logged-in cashier at an outlet.
func (m *Module) OpenShift(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OpenShiftInput) (Shift, error) {
	p := authz.From(ctx)
	ou, err := loadOutlet(ctx, tx, in.OutletID)
	if err != nil {
		return Shift{}, err
	}
	if ou.PropertyID != property {
		return Shift{}, errs.NotFound("outlet")
	}
	cash, err := handle.Decimal("openingCash", in.OpeningCash, decimal.Zero)
	if err != nil {
		return Shift{}, err
	}
	no, err := numbering.Next(ctx, tx, property, "SHF", clock.Now().In(calendar.Location(ctx, tx)))
	if err != nil {
		return Shift{}, err
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.pos_shifts (id, property_id, shift_no, outlet_id, cashier_id, device_id, opening_cash)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric)`, sid, property, no, ou.ID, p.UserID, p.DeviceID, cash.String()); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Shift{}, errs.Conflict("shift_open", "you already have an open shift at "+ou.Name)
		}
		return Shift{}, err
	}
	rows, err := tx.Query(ctx, shiftSelect+` WHERE s.id = $1`, sid)
	s, err := handle.One[Shift](rows, err, "shift")
	if err != nil {
		return s, err
	}
	return s, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "open_shift", EntityType: "commercial.pos_shift",
		EntityID: sid.String(), EntityLabel: no + " · " + ou.Name, PropertyID: &property, After: s})
}

type CashMovementInput struct {
	Kind   string `json:"kind" enum:"cash_in,cash_out"`
	Amount string `json:"amount"`
	Reason string `json:"reason"`
}

// CashMovement records cash in / out during a shift.
func (m *Module) CashMovement(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in CashMovementInput) (ShiftReport, error) {
	var status string
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, property_id FROM commercial.pos_shifts WHERE id = $1 FOR UPDATE`, sid).Scan(&status, &pid); err != nil {
		if dbtx.IsNoRows(err) {
			return ShiftReport{}, errs.NotFound("shift")
		}
		return ShiftReport{}, err
	}
	if status != "open" {
		return ShiftReport{}, errs.Conflict("shift_closed", "the shift is closed")
	}
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil || !amt.IsPositive() {
		return ShiftReport{}, handle.Invalid("amount", "invalid_amount", "amount must be positive")
	}
	if in.Kind != "cash_in" && in.Kind != "cash_out" {
		return ShiftReport{}, handle.Invalid("kind", "invalid_kind", "kind must be cash_in or cash_out")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return ShiftReport{}, err
	}
	mid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.cash_movements (id, property_id, shift_id, kind, amount, reason, created_by) VALUES ($1,$2,$3,$4,$5::numeric,$6,$7)`,
		mid, pid, sid, in.Kind, amt.String(), in.Reason, actorID(ctx)); err != nil {
		return ShiftReport{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: in.Kind, EntityType: "commercial.pos_shift", EntityID: sid.String(),
		PropertyID: &pid, Reason: in.Reason, Metadata: map[string]any{"amount": amt.String()}}); err != nil {
		return ShiftReport{}, err
	}
	return m.ShiftReport(ctx, tx, sid, "x")
}

// ShiftReport computes the X / Z report of a shift.
func (m *Module) ShiftReport(ctx context.Context, q dbtx.Querier, sid uuid.UUID, kind string) (ShiftReport, error) {
	rows, err := q.Query(ctx, shiftSelect+` WHERE s.id = $1`, sid)
	s, err := handle.One[Shift](rows, err, "shift")
	if err != nil {
		return ShiftReport{}, err
	}
	r := ShiftReport{Shift: s, Kind: kind, Payments: []map[string]any{}}
	var sales, cashIn, cashOut, cashPay string
	if err := q.QueryRow(ctx, `SELECT count(*), trim_scale(coalesce(sum(l.total_amount), 0))::text FROM commercial.orders o
		JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active' WHERE o.shift_id = $1 AND o.status IN ('paid', 'charged')`, sid).Scan(&r.Orders, &sales); err != nil {
		return r, err
	}
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT id) FROM commercial.orders WHERE shift_id = $1 AND status IN ('paid', 'charged')`, sid).Scan(&r.Orders); err != nil {
		return r, err
	}
	prow, err := q.Query(ctx, `SELECT method_type, count(*), trim_scale(sum(CASE WHEN kind = 'refund' THEN -amount ELSE amount END))::text
		FROM billing.payments WHERE shift_id = $1 AND status IN ('completed', 'refunded') GROUP BY method_type ORDER BY method_type`, sid)
	if err != nil {
		return r, err
	}
	for prow.Next() {
		var method, amt string
		var n int
		if err := prow.Scan(&method, &n, &amt); err != nil {
			prow.Close()
			return r, err
		}
		r.Payments = append(r.Payments, map[string]any{"methodType": method, "count": n, "amount": amt})
	}
	prow.Close()
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(amount) FILTER (WHERE kind = 'cash_in'), 0))::text,
		trim_scale(coalesce(sum(amount) FILTER (WHERE kind = 'cash_out'), 0))::text FROM commercial.cash_movements WHERE shift_id = $1`, sid).Scan(&cashIn, &cashOut); err != nil {
		return r, err
	}
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(CASE WHEN kind = 'refund' THEN -amount ELSE amount END), 0))::text FROM billing.payments
		WHERE shift_id = $1 AND method_type = 'cash' AND status IN ('completed', 'refunded')`, sid).Scan(&cashPay); err != nil {
		return r, err
	}
	open, _ := decimal.NewFromString(s.OpeningCash)
	ci, _ := decimal.NewFromString(cashIn)
	co, _ := decimal.NewFromString(cashOut)
	cp, _ := decimal.NewFromString(cashPay)
	r.Sales, r.CashIn, r.CashOut, r.CashPayments = sales, cashIn, cashOut, cashPay
	r.ExpectedCash = open.Add(ci).Sub(co).Add(cp).String()
	return r, nil
}

type CloseShiftInput struct {
	CountedCash string `json:"countedCash"`
}

// CloseShift closes the shift: expected vs counted cash, Z report stored.
func (m *Module) CloseShift(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in CloseShiftInput) (ShiftReport, error) {
	var status string
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, property_id FROM commercial.pos_shifts WHERE id = $1 FOR UPDATE`, sid).Scan(&status, &pid); err != nil {
		if dbtx.IsNoRows(err) {
			return ShiftReport{}, errs.NotFound("shift")
		}
		return ShiftReport{}, err
	}
	if status != "open" {
		return ShiftReport{}, errs.Conflict("shift_closed", "the shift is already closed")
	}
	var openOrders int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM commercial.orders o WHERE o.shift_id = $1 AND o.status = 'open'
		AND EXISTS (SELECT 1 FROM commercial.order_lines l WHERE l.order_id = o.id AND l.status = 'active')`, sid).Scan(&openOrders); err != nil {
		return ShiftReport{}, err
	}
	if openOrders > 0 {
		return ShiftReport{}, errs.Conflict("open_orders", fmt.Sprintf("%d order(s) of this shift are still open", openOrders))
	}
	counted, err := handle.Decimal("countedCash", in.CountedCash, decimal.Zero)
	if err != nil {
		return ShiftReport{}, err
	}
	r, err := m.ShiftReport(ctx, tx, sid, "z")
	if err != nil {
		return r, err
	}
	exp, _ := decimal.NewFromString(r.ExpectedCash)
	z, _ := json.Marshal(r)
	if _, err := tx.Exec(ctx, `UPDATE commercial.pos_shifts SET status = 'closed', closed_at = now(), counted_cash = $2::numeric, expected_cash = $3::numeric,
		variance = $4::numeric, z_report = $5 WHERE id = $1`, sid, counted.String(), exp.String(), counted.Sub(exp).String(), z); err != nil {
		return r, err
	}
	r, err = m.ShiftReport(ctx, tx, sid, "z")
	if err != nil {
		return r, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "close_shift", EntityType: "commercial.pos_shift", EntityID: sid.String(),
		EntityLabel: r.Shift.ShiftNo, PropertyID: &pid, After: r}); err != nil {
		return r, err
	}
	_, err = m.Events.Publish(ctx, tx, "commercial.shift_closed", "commercial.pos_shift", &sid, &pid, map[string]any{"shiftId": sid,
		"shiftNo": r.Shift.ShiftNo, "outletId": r.Shift.OutletID, "sales": r.Sales, "expectedCash": r.ExpectedCash, "countedCash": counted.String(),
		"variance": counted.Sub(exp).String()})
	return r, err
}

// ── receipt, favourites, offline sync ─────────────────────────────────────

// Receipt renders a 42-column thermal receipt (FR-POS-12).
func (m *Module) Receipt(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (string, Order, error) {
	o, err := m.Order(ctx, q, oid)
	if err != nil {
		return "", o, err
	}
	var b strings.Builder
	line := func(l, r string) {
		pad := 42 - len([]rune(l)) - len([]rune(r))
		if pad < 1 {
			pad = 1
		}
		b.WriteString(l + strings.Repeat(" ", pad) + r + "\n")
	}
	b.WriteString(center(o.OutletName) + "\n")
	b.WriteString(center(o.OrderNo) + "\n")
	b.WriteString(center(o.CreatedAt.In(calendar.Location(ctx, q)).Format("02 Jan 2006 15:04")) + "\n")
	b.WriteString(strings.Repeat("-", 42) + "\n")
	for _, l := range o.Lines {
		if l.Status != "active" {
			continue
		}
		line(l.Quantity+" x "+trunc(l.Name, 28), money(l.TotalAmount))
		for _, md := range l.Modifiers {
			b.WriteString("    + " + fmt.Sprint(md["name"]) + "\n")
		}
		if l.DiscountAmount != "0" {
			line("    discount", "-"+money(l.DiscountAmount))
		}
	}
	b.WriteString(strings.Repeat("-", 42) + "\n")
	line("TOTAL", money(o.Total))
	rows, err := q.Query(ctx, `SELECT p.method_type, trim_scale(p.amount)::text FROM billing.payments p JOIN commercial.order_bills b ON b.folio_id = p.folio_id
		WHERE b.order_id = $1 AND p.kind <> 'refund' AND p.status = 'completed' GROUP BY p.id, p.method_type, p.amount ORDER BY p.received_at`, oid)
	if err != nil {
		return "", o, err
	}
	for rows.Next() {
		var mt, amt string
		if err := rows.Scan(&mt, &amt); err != nil {
			rows.Close()
			return "", o, err
		}
		line("  "+strings.ToUpper(strings.ReplaceAll(mt, "_", " ")), money(amt))
	}
	rows.Close()
	b.WriteString("\n" + center("Terima kasih / Thank you") + "\n")
	return b.String(), o, nil
}

func center(s string) string {
	pad := (42 - len([]rune(s))) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + s
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func money(s string) string {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return s
	}
	neg := d.IsNegative()
	str := d.Abs().StringFixed(0)
	var out []byte
	for i, c := range []byte(str) {
		if i > 0 && (len(str)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// FavoriteItem is a customer's most ordered item (FR-FNB-05).
type FavoriteItem struct {
	ProductID    uuid.UUID `json:"productId" db:"product_id"`
	Name         string    `json:"name" db:"name"`
	TimesOrdered int       `json:"timesOrdered" db:"times_ordered"`
	Quantity     string    `json:"quantity" db:"quantity"`
	LastOrdered  time.Time `json:"lastOrdered" db:"last_ordered"`
}

// Favorites returns the most ordered items of a customer.
func (m *Module) Favorites(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) ([]FavoriteItem, error) {
	return handle.List[FavoriteItem](q.Query(ctx, `SELECT l.product_id, p.name, count(DISTINCT o.id)::int AS times_ordered,
		trim_scale(sum(l.quantity))::text AS quantity, max(o.created_at) AS last_ordered
		FROM commercial.orders o JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active' JOIN commercial.products p ON p.id = l.product_id
		WHERE o.property_id = $1 AND o.customer_id = $2 AND o.status IN ('paid', 'charged', 'open')
		GROUP BY l.product_id, p.name ORDER BY times_ordered DESC, last_ordered DESC LIMIT 10`, property, customer))
}

// Repeat creates a new order with the same items (one-tap reorder).
func (m *Module) Repeat(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in OrderInput) (Order, error) {
	o, err := m.Order(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	in.Lines = nil
	for _, l := range o.Lines {
		if l.Status != "active" {
			continue
		}
		var mods []uuid.UUID
		for _, md := range l.Modifiers {
			if u, err := uuid.Parse(fmt.Sprint(md["modifierId"])); err == nil {
				mods = append(mods, u)
			}
		}
		in.Lines = append(in.Lines, LineInput{ProductID: l.ProductID, VariantID: l.VariantID, Quantity: l.Quantity, ModifierIDs: mods, Notes: deref(l.Notes)})
	}
	if in.OutletID == uuid.Nil {
		in.OutletID = o.OutletID
	}
	if in.CustomerID == nil {
		in.CustomerID = o.CustomerID
	}
	if in.ServingDestination == "" {
		in.ServingDestination = o.ServingDestination
	}
	return m.CreateOrder(ctx, tx, o.PropertyID, in)
}

// SyncOrder is the offline POS sync payload: the order and its payment.
type SyncOrder struct {
	Order   OrderInput `json:"order"`
	Payment *PayInput  `json:"payment,omitempty"`
}

// SyncHandler processes a queued offline sale (FR-POS-11): the order keeps
// its client id and the payment idempotency keys derive from it, so a resent
// queue never duplicates a sale.
func (m *Module) SyncHandler(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in SyncOrder
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid order payload")
	}
	if in.Order.ID == nil {
		return nil, errs.Validation("order_id_required", "offline orders need a client id")
	}
	in.Order.Offline = true
	property := handle.Property(ctx)
	o, err := m.CreateOrder(ctx, tx, property, in.Order)
	if err != nil {
		return nil, err
	}
	if in.Payment != nil && o.Status == "open" {
		pol, err := m.posPolicy(ctx, tx, property)
		if err != nil {
			return nil, err
		}
		for _, t := range in.Payment.Tenders {
			if t.MethodType == "member_account" && !pol.OfflineMemberCharge {
				return nil, errs.Conflict("offline_member_charge_disabled", "member charge is not allowed offline (POS Policies)")
			}
		}
		in.Payment.Offline = true
		if o, err = m.Pay(ctx, tx, o.ID, *in.Payment, "sync-"+o.ID.String()); err != nil {
			return nil, err
		}
	}
	return map[string]any{"orderId": o.ID, "orderNo": o.OrderNo, "status": o.Status, "total": o.Total}, nil
}
