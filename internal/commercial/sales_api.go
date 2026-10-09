package commercial

// Public interface of the P2 commercial sub-parts (PRD P2 §5.4.1:
// commercial/pos and commercial/voucher). Other modules use POS and voucher
// only through these types and interfaces (Tech Doc §4.2 #1); internal/app
// wires the implementations. Additive; review: P1 developer.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// POS creates and reads F&B orders (on-course order, stay catering, VIP
// suite add-on).
type POS interface {
	CreateOrder(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OrderInput) (Order, error)
	Order(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (Order, error)
	// DefaultOutlet is the outlet that prepares banquet / catering orders
	// when none is chosen.
	DefaultOutlet(ctx context.Context, q dbtx.Querier, property uuid.UUID) (uuid.UUID, error)
	// FolioOrders lists the orders charged to folios (e.g. a golf booking's
	// on-course orders); CancelOrder cancels one the kitchen has not started.
	FolioOrders(ctx context.Context, q dbtx.Querier, folios []uuid.UUID) ([]Order, error)
	CancelOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, reason string) (Order, error)
}

// Vouchers redeems vouchers, prepaid balances and quotas (contract C3).
type Vouchers interface {
	Redeem(ctx context.Context, tx pgx.Tx, r RedeemRequest) (RedeemResult, error)
	RedeemBalance(ctx context.Context, tx pgx.Tx, r RedeemRequest, category string) ([]RedeemResult, error)
	Quota(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, category, item string) (decimal.Decimal, error)
	UseQuota(ctx context.Context, tx pgx.Tx, property, customerID uuid.UUID, category, item string, qty decimal.Decimal,
		serviceType, sourceType string, sourceID *uuid.UUID, key string) (*uuid.UUID, error)
	Restore(ctx context.Context, tx pgx.Tx, voucherID uuid.UUID, qty decimal.Decimal, reason, key string) error
}

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
	// PRD P3 (additive): promotions applied to the line (FR-PRM-06).
	PromotionDiscount string             `json:"promotionDiscount" db:"promotion_discount"`
	Promotions        []AppliedPromotion `json:"promotions" db:"promotions"`
	// PRD P5 (additive): loyalty tier F&B discount of the line (member tier class).
	TierDiscount string `json:"tierDiscount" db:"tier_discount"`
	// Who the item is for (additive, demo feedback 10 Oct 2026 #35).
	GuestName  *string    `json:"guestName" db:"guest_name" doc:"Player / member the item is for"`
	CustomerID *uuid.UUID `json:"customerId" db:"customer_id"`
	GuestRef   *uuid.UUID `json:"guestRef" db:"guest_ref" doc:"Source party of the item, e.g. the golf booking player"`
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
	// PRD P3 (additive): promo codes, removed promotions, the offline
	// terminal total and the promotion mismatch flag (FR-PRM-06, FR-PRM-09).
	PromoCodes          []string     `json:"promoCodes" db:"promo_codes"`
	PromotionExclusions []string     `json:"promotionExclusions" db:"promotion_exclusions"`
	ClientTotal         *string      `json:"clientTotal" db:"client_total"`
	PromotionMismatch   bool         `json:"promotionMismatch" db:"promotion_mismatch"`
	Promotions          []Redemption `json:"promotions" db:"-"`
	// PRD P5 (additive): the loyalty tier of the customer when the order was
	// opened and its F&B discount, shown as a separate discount line.
	TierCode            *string `json:"tierCode" db:"tier_code"`
	TierName            *string `json:"tierName" db:"tier_name"`
	TierDiscountPercent *string `json:"tierDiscountPercent" db:"tier_discount_percent"`
	TierDiscountLabel   *string `json:"tierDiscountLabel" db:"tier_discount_label" doc:"e.g. Gold member 5%"`
	TierDiscount        string  `json:"tierDiscount" db:"tier_discount" doc:"Tier discount of the active lines"`
	// POS Table View (additive): the tables the order seats, the
	// reservation it seated and when the bill was presented.
	TableIDs           []uuid.UUID `json:"tableIds" db:"table_ids"`
	TableReservationID *uuid.UUID  `json:"tableReservationId" db:"table_reservation_id"`
	BilledAt           *time.Time  `json:"billedAt" db:"billed_at"`
	// Who the order is for when no customer is linked, and the source
	// reference, e.g. the golf booking code (additive, demo feedback #34).
	GuestName *string `json:"guestName" db:"guest_name"`
	Reference *string `json:"reference" db:"reference" doc:"e.g. golf booking BK-261010-0015"`
}

// LineInput is an item ordered.
type LineInput struct {
	ProductID   uuid.UUID   `json:"productId"`
	VariantID   *uuid.UUID  `json:"variantId,omitempty"`
	Quantity    string      `json:"quantity,omitempty" doc:"Default 1"`
	ModifierIDs []uuid.UUID `json:"modifierIds,omitempty"`
	Seat        string      `json:"seat,omitempty"`
	Notes       string      `json:"notes,omitempty"`
	// Who the item is for (additive, demo feedback 10 Oct 2026 #35)
	GuestName  string     `json:"guestName,omitempty" doc:"Player / member the item is for"`
	CustomerID *uuid.UUID `json:"customerId,omitempty"`
	GuestRef   *uuid.UUID `json:"guestRef,omitempty" doc:"Source party, e.g. the golf booking player"`
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
	// PRD P3 (additive)
	PromoCodes  []string `json:"promoCodes,omitempty" doc:"Promo codes entered"`
	ClientTotal string   `json:"clientTotal,omitempty" doc:"Offline: the total the terminal computed with its cached promotions (checked at sync)"`
	// PromotionExclusions are promotions the cashier removed before sending
	// the order (permission commercial.pos.promotion_override).
	PromotionExclusions []uuid.UUID `json:"promotionExclusions,omitempty"`
	// POS Table View (additive)
	TableIDs           []uuid.UUID `json:"tableIds,omitempty" doc:"Dining tables the order seats (Table View); tableNo defaults to their codes"`
	TableReservationID *uuid.UUID  `json:"tableReservationId,omitempty" doc:"Table reservation seated by this order"`
	// additive (demo feedback 10 Oct 2026 #34)
	GuestName string `json:"guestName,omitempty" doc:"Who the order is for when no customer is linked (e.g. a guest player)"`
	Reference string `json:"reference,omitempty" doc:"Source reference shown on the order, e.g. a golf booking code"`
}

// Voucher is a voucher with its type.
type Voucher struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Code              string     `json:"code" db:"code"`
	VoucherTypeID     uuid.UUID  `json:"voucherTypeId" db:"voucher_type_id"`
	TypeCode          string     `json:"typeCode" db:"type_code"`
	TypeName          string     `json:"typeName" db:"type_name"`
	Kind              string     `json:"kind" db:"kind" enum:"value,quota,promo"`
	Category          string     `json:"category" db:"category"`
	Unit              string     `json:"unit" db:"unit"`
	Prepaid           bool       `json:"prepaid" db:"prepaid"`
	Transferable      bool       `json:"transferable" db:"transferable"`
	CustomerID        *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName      *string    `json:"customerName" db:"customer_name"`
	Status            string     `json:"status" db:"status" enum:"pending,active,partially_redeemed,redeemed,expired,void"`
	OriginalQuantity  string     `json:"originalQuantity" db:"original_quantity"`
	RemainingQuantity string     `json:"remainingQuantity" db:"remaining_quantity"`
	UnitValue         string     `json:"unitValue" db:"unit_value"`
	PricePaid         string     `json:"pricePaid" db:"price_paid"`
	UsesCount         int        `json:"usesCount" db:"uses_count"`
	IssuedVia         string     `json:"issuedVia" db:"issued_via"`
	FolioID           *uuid.UUID `json:"folioId" db:"folio_id"`
	IssuedAt          time.Time  `json:"issuedAt" db:"issued_at"`
	ExpiresAt         *time.Time `json:"expiresAt" db:"expires_at"`
	VoidReason        *string    `json:"voidReason" db:"void_reason"`
}

// RedeemRequest uses (part of) a voucher (FR-VCH-03).
type RedeemRequest struct {
	PropertyID     uuid.UUID
	VoucherID      *uuid.UUID
	Code           string
	Quantity       decimal.Decimal // units (quota) or rupiah (value)
	ServiceType    string
	ResourceType   string
	ItemRef        string
	OutletID       *uuid.UUID
	CustomerID     *uuid.UUID
	Terminal       string
	Reference      string
	SourceType     string
	SourceID       *uuid.UUID
	IdempotencyKey string
	BaseAmount     decimal.Decimal // promo vouchers: amount the discount applies to
}

// RedeemResult reports a redemption.
type RedeemResult struct {
	Voucher    Voucher `json:"voucher"`
	Quantity   string  `json:"quantity"`
	Recognized string  `json:"recognizedRevenue"`
	Discount   string  `json:"discount" doc:"Promo vouchers: discount granted"`
	Duplicate  bool    `json:"duplicate" doc:"The same redemption (idempotency key) was already processed"`
}
