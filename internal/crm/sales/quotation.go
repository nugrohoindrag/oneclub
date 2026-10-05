package sales

// EP-03 Quotation & Conversion: quotations from an opportunity, a lead or a
// customer with lines priced by the pricing engine or manually (FR-QUO-01),
// line and total discounts with approval above the Sales Policies limit
// (FR-QUO-02), versions (FR-QUO-03), validity and option date (FR-QUO-04),
// sending as PDF and secure link with acceptance by name and terms, IP and
// time recorded (FR-QUO-05), conversion through crm.quotation_accepted
// (FR-QUO-06) exactly once (FR-QUO-07).

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
)

// ItemTypes of a quotation line (crm.quotation_accepted lines.itemType).
var ItemTypes = []string{"banquet_package", "venue", "product", "service", "package", "other"}

// ── pricing hook ──────────────────────────────────────────────────────────

// PriceInput is a quotation line to price.
type PriceInput struct {
	ItemType    string
	ItemRef     string
	ServiceType string           // Commercial service type of the pricing rules (optional)
	Quantity    decimal.Decimal  // units
	UnitPrice   *decimal.Decimal // manual / already resolved price; nil = resolve
	Discount    decimal.Decimal  // line discount incl. the share of the quotation discount
	At          time.Time        // service date (event date) or now
	Currency    string
	PricingMode string   // nett | plus_plus
	TaxCodes    []string // tax & service rule codes (manual prices)
}

// PricedLine is a priced quotation line.
type PricedLine struct {
	UnitPrice   decimal.Decimal
	Source      string // manual | pricing_rule | product
	PricingMode string
	TaxCodes    []string
	Net         decimal.Decimal
	Service     decimal.Decimal
	Tax         decimal.Decimal
	Total       decimal.Decimal // incl. tax & service
	Snapshot    map[string]any  // rule, version, tax lines (kept on the line)
}

// Pricer resolves list prices (pricing rules, product prices) and tax &
// service of a line; internal/app wires it to Commercial (crm cannot import
// commercial, Technical Doc §4.2).
type Pricer func(ctx context.Context, q dbtx.Querier, property uuid.UUID, in PriceInput) (PricedLine, error)

// manualPrice is the pricing without the hook: manual prices, no tax.
func manualPrice(in PriceInput) (PricedLine, error) {
	if in.UnitPrice == nil {
		return PricedLine{}, handle.Invalid("lines.unitPrice", "required", "enter the unit price")
	}
	base := in.UnitPrice.Mul(in.Quantity).Sub(in.Discount)
	return PricedLine{UnitPrice: *in.UnitPrice, Source: "manual", PricingMode: in.PricingMode, TaxCodes: in.TaxCodes, Net: base, Total: base,
		Snapshot: map[string]any{}}, nil
}

func (m *Module) price(ctx context.Context, q dbtx.Querier, property uuid.UUID, in PriceInput) (PricedLine, error) {
	if m.Price == nil {
		return manualPrice(in)
	}
	return m.Price(ctx, q, property, in)
}

// ── types ─────────────────────────────────────────────────────────────────

// QuotationLineInput is one line of a quotation.
type QuotationLineInput struct {
	ItemType        string `json:"itemType" enum:"banquet_package,venue,product,service,package,other"`
	ItemRef         string `json:"itemRef,omitempty" doc:"Banquet package code, venue / product id, package code …"`
	ServiceType     string `json:"serviceType,omitempty" doc:"Commercial service type to price from the pricing rules (e.g. meeting_package)"`
	Description     string `json:"description"`
	Quantity        string `json:"quantity"`
	UnitPrice       string `json:"unitPrice,omitempty" doc:"Manual unit price; empty = from the pricing engine"`
	Discount        string `json:"discount,omitempty" doc:"Line discount amount"`
	DiscountPercent string `json:"discountPercent,omitempty" doc:"Line discount percent (instead of an amount)"`

	src   string   // kept price source when lines are re-priced
	mode  string   // kept pricing mode
	codes []string // kept tax codes
}

// PaymentTermInput is one payment term (DP, installment, final payment).
type PaymentTermInput struct {
	Label           string      `json:"label"`
	Percent         string      `json:"percent,omitempty"`
	Amount          string      `json:"amount,omitempty"`
	DueDate         *route.Date `json:"dueDate,omitempty"`
	DueDays         *int        `json:"dueDays,omitempty" doc:"Due N days after acceptance"`
	DaysBeforeEvent *int        `json:"daysBeforeEvent,omitempty" doc:"Due N days before the event"`
}

// PaymentTerm is a computed payment term (due dates are final at acceptance).
type PaymentTerm struct {
	Label           string `json:"label"`
	Percent         string `json:"percent"`
	Amount          string `json:"amount"`
	DueDate         string `json:"dueDate"`
	DueDays         *int   `json:"dueDays,omitempty"`
	DaysBeforeEvent *int   `json:"daysBeforeEvent,omitempty"`
	FixedDate       bool   `json:"fixedDate,omitempty"`
	ByAmount        bool   `json:"byAmount,omitempty"`
}

// QuotationInput creates (or, on PATCH, edits) a quotation.
type QuotationInput struct {
	OpportunityID      *uuid.UUID           `json:"opportunityId,omitempty"`
	LeadID             *uuid.UUID           `json:"leadId,omitempty"`
	CustomerID         *uuid.UUID           `json:"customerId,omitempty"`
	CorporateAccountID *uuid.UUID           `json:"corporateAccountId,omitempty"`
	Title              string               `json:"title,omitempty"`
	Line               string               `json:"line,omitempty" enum:"wedding,banquet,mice,event,tournament,stay,golf,package,membership,other"`
	EventType          string               `json:"eventType,omitempty" enum:"wedding,meeting,conference,gathering,birthday,tournament,other"`
	EventDate          *route.Date          `json:"eventDate,omitempty"`
	EndDate            *route.Date          `json:"endDate,omitempty"`
	Pax                *int                 `json:"pax,omitempty"`
	VenueResourceID    *uuid.UUID           `json:"venueResourceId,omitempty"`
	PackageRef         string               `json:"packageRef,omitempty"`
	Currency           string               `json:"currency,omitempty"`
	PricingMode        string               `json:"pricingMode,omitempty" enum:"nett,plus_plus" doc:"Of manually priced lines; default Sales Policies"`
	ValidUntil         *route.Date          `json:"validUntil,omitempty" doc:"Default: today + Sales Policies validity"`
	OptionDate         *route.Date          `json:"optionDate,omitempty" doc:"Option date of the tentative venue hold"`
	Discount           *string              `json:"discount,omitempty" doc:"Discount on the total (amount)"`
	DiscountPercent    *string              `json:"discountPercent,omitempty" doc:"Discount on the total (percent)"`
	Terms              *string              `json:"terms,omitempty" doc:"Terms & conditions (default Sales Policies)"`
	Notes              *string              `json:"notes,omitempty"`
	PaymentTerms       []PaymentTermInput   `json:"paymentTerms,omitempty" doc:"Default: Sales Policies payment terms"`
	Lines              []QuotationLineInput `json:"lines,omitempty"`
	OwnerUserID        *uuid.UUID           `json:"ownerUserId,omitempty" doc:"Sales credited with the deal (default: the opportunity owner)"`
	ClearOptionDate    bool                 `json:"clearOptionDate,omitempty"`
}

// QuotationLine is a stored line.
type QuotationLine struct {
	ID            uuid.UUID       `json:"id" db:"id"`
	LineNo        int             `json:"lineNo" db:"line_no"`
	ItemType      string          `json:"itemType" db:"item_type" enum:"banquet_package,venue,product,service,package,other"`
	ItemRef       *string         `json:"itemRef" db:"item_ref"`
	ServiceType   *string         `json:"serviceType" db:"service_type"`
	Description   string          `json:"description" db:"description"`
	Quantity      string          `json:"quantity" db:"quantity"`
	UnitPrice     string          `json:"unitPrice" db:"unit_price"`
	Discount      string          `json:"discount" db:"discount" doc:"Line discount + share of the quotation discount"`
	LineDiscount  string          `json:"lineDiscount" db:"line_discount"`
	Total         string          `json:"total" db:"total" doc:"Quantity × unit price − discount"`
	NetAmount     string          `json:"netAmount" db:"net_amount"`
	ServiceAmount string          `json:"serviceAmount" db:"service_amount"`
	TaxAmount     string          `json:"taxAmount" db:"tax_amount"`
	GrossTotal    string          `json:"grossTotal" db:"gross_total" doc:"Incl. tax & service"`
	PriceSource   string          `json:"priceSource" db:"price_source" enum:"manual,pricing_rule,product"`
	Pricing       json.RawMessage `json:"pricing" db:"pricing"`
}

const lineSelect = `SELECT id, line_no, item_type, item_ref, service_type, description, trim_scale(quantity)::text AS quantity,
	trim_scale(unit_price)::text AS unit_price, trim_scale(discount + header_discount_share)::text AS discount, trim_scale(discount)::text AS line_discount,
	trim_scale(total)::text AS total, trim_scale(net_amount)::text AS net_amount, trim_scale(service_amount)::text AS service_amount,
	trim_scale(tax_amount)::text AS tax_amount, trim_scale(gross_total)::text AS gross_total, price_source, pricing
	FROM crm.sales_quotation_lines`

// Quotation is a quotation version.
type Quotation struct {
	ID                    uuid.UUID     `json:"id" db:"id"`
	Number                string        `json:"number" db:"number"`
	Version               int           `json:"version" db:"version"`
	Status                string        `json:"status" db:"status" enum:"draft,pending_approval,sent,accepted,rejected,expired,revised"`
	ApprovalStatus        string        `json:"approvalStatus" db:"approval_status" enum:"not_required,required,pending,approved,rejected"`
	ApprovalRequestID     *uuid.UUID    `json:"approvalRequestId" db:"approval_request_id"`
	OpportunityID         *uuid.UUID    `json:"opportunityId" db:"opportunity_id"`
	LeadID                *uuid.UUID    `json:"leadId" db:"lead_id"`
	CustomerID            *uuid.UUID    `json:"customerId" db:"customer_id"`
	CustomerName          *string       `json:"customerName" db:"customer_name"`
	CorporateAccountID    *uuid.UUID    `json:"corporateAccountId" db:"corporate_account_id"`
	CorporateName         *string       `json:"corporateName" db:"corporate_name"`
	OwnerUserID           *uuid.UUID    `json:"ownerUserId" db:"owner_user_id"`
	OwnerName             *string       `json:"ownerName" db:"owner_name"`
	Line                  string        `json:"line" db:"line"`
	EventType             *string       `json:"eventType" db:"event_type"`
	EventDate             *string       `json:"eventDate" db:"event_date"`
	EndDate               *string       `json:"endDate" db:"end_date"`
	Pax                   *int          `json:"pax" db:"pax"`
	VenueResourceID       *uuid.UUID    `json:"venueResourceId" db:"venue_resource_id"`
	PackageRef            *string       `json:"packageRef" db:"package_ref"`
	Title                 string        `json:"title" db:"title"`
	Currency              string        `json:"currency" db:"currency"`
	PricingMode           string        `json:"pricingMode" db:"pricing_mode" enum:"nett,plus_plus"`
	TaxCodes              []string      `json:"taxCodes" db:"tax_codes"`
	HeaderDiscount        string        `json:"headerDiscount" db:"header_discount"`
	HeaderDiscountPercent *string       `json:"headerDiscountPercent" db:"header_discount_percent"`
	Subtotal              string        `json:"subtotal" db:"subtotal"`
	Discount              string        `json:"discount" db:"discount"`
	DiscountPercent       string        `json:"discountPercent" db:"discount_percent"`
	NetAmount             string        `json:"netAmount" db:"net_amount"`
	ServiceAmount         string        `json:"serviceAmount" db:"service_amount"`
	TaxAmount             string        `json:"taxAmount" db:"tax_amount"`
	Total                 string        `json:"total" db:"total"`
	ValidUntil            string        `json:"validUntil" db:"valid_until"`
	OptionDate            *string       `json:"optionDate" db:"option_date"`
	PaymentTerms          []PaymentTerm `json:"paymentTerms" db:"payment_terms"`
	Terms                 *string       `json:"terms" db:"terms"`
	Notes                 *string       `json:"notes" db:"notes"`
	SentAt                *time.Time    `json:"sentAt" db:"sent_at"`
	SentVia               []string      `json:"sentVia" db:"sent_via"`
	AcceptedAt            *time.Time    `json:"acceptedAt" db:"accepted_at"`
	AcceptedVia           *string       `json:"acceptedVia" db:"accepted_via" enum:"staff,public_link"`
	AcceptedByName        *string       `json:"acceptedByName" db:"accepted_by_name"`
	RejectedAt            *time.Time    `json:"rejectedAt" db:"rejected_at"`
	RejectReason          *string       `json:"rejectReason" db:"reject_reason"`
	ExpiredAt             *time.Time    `json:"expiredAt" db:"expired_at"`
	RevisedFromID         *uuid.UUID    `json:"revisedFromId" db:"revised_from_id"`
	PaidAmount            string        `json:"paidAmount" db:"paid_amount" doc:"Money received for the deal (commission recognition)"`
	PaidAt                *time.Time    `json:"paidAt" db:"paid_at" doc:"Deal paid in full"`
	PolicyVersion         int           `json:"policyVersion" db:"policy_version"`
	CreatedAt             time.Time     `json:"createdAt" db:"created_at"`
	UpdatedAt             time.Time     `json:"updatedAt" db:"updated_at"`
}

const quotationSelect = `SELECT q.id, q.number, q.version, q.status, q.approval_status, q.approval_request_id, q.opportunity_id, q.lead_id,
	q.customer_id, c.name AS customer_name, q.corporate_account_id, ca.name AS corporate_name, q.owner_user_id, u.full_name AS owner_name, q.line,
	q.event_type, to_char(q.event_date, 'YYYY-MM-DD') AS event_date, to_char(q.end_date, 'YYYY-MM-DD') AS end_date, q.pax, q.venue_resource_id,
	q.package_ref, q.title, q.currency, q.pricing_mode, q.tax_codes, trim_scale(q.header_discount)::text AS header_discount,
	trim_scale(q.header_discount_percent)::text AS header_discount_percent, trim_scale(q.subtotal)::text AS subtotal,
	trim_scale(q.discount)::text AS discount, trim_scale(q.discount_percent)::text AS discount_percent, trim_scale(q.net_amount)::text AS net_amount,
	trim_scale(q.service_amount)::text AS service_amount, trim_scale(q.tax_amount)::text AS tax_amount, trim_scale(q.total)::text AS total,
	to_char(q.valid_until, 'YYYY-MM-DD') AS valid_until, to_char(q.option_date, 'YYYY-MM-DD') AS option_date, q.payment_terms, q.terms, q.notes,
	q.sent_at, q.sent_via, q.accepted_at, q.accepted_via, q.accepted_by_name, q.rejected_at, q.reject_reason, q.expired_at, q.revised_from_id,
	trim_scale(q.paid_amount)::text AS paid_amount, q.paid_at, q.policy_version, q.created_at, q.updated_at
	FROM crm.sales_quotations q LEFT JOIN crm.customers c ON c.id = q.customer_id LEFT JOIN crm.corporate_accounts ca ON ca.id = q.corporate_account_id
	LEFT JOIN platform.users u ON u.id = q.owner_user_id`

// QuotationBrief is a quotation in lists.
type QuotationBrief struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	Number         string     `json:"number" db:"number"`
	Version        int        `json:"version" db:"version"`
	Status         string     `json:"status" db:"status" enum:"draft,pending_approval,sent,accepted,rejected,expired,revised"`
	ApprovalStatus string     `json:"approvalStatus" db:"approval_status"`
	Title          string     `json:"title" db:"title"`
	Line           string     `json:"line" db:"line"`
	OpportunityID  *uuid.UUID `json:"opportunityId" db:"opportunity_id"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName   *string    `json:"customerName" db:"customer_name"`
	OwnerName      *string    `json:"ownerName" db:"owner_name"`
	Total          string     `json:"total" db:"total"`
	Currency       string     `json:"currency" db:"currency"`
	ValidUntil     string     `json:"validUntil" db:"valid_until"`
	SentAt         *time.Time `json:"sentAt" db:"sent_at"`
	AcceptedAt     *time.Time `json:"acceptedAt" db:"accepted_at"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
}

const quotationBriefSelect = `SELECT q.id, q.number, q.version, q.status, q.approval_status, q.title, q.line, q.opportunity_id, q.customer_id,
	coalesce(c.name, l.name) AS customer_name, u.full_name AS owner_name, trim_scale(q.total)::text AS total, q.currency,
	to_char(q.valid_until, 'YYYY-MM-DD') AS valid_until, q.sent_at, q.accepted_at, q.created_at
	FROM crm.sales_quotations q LEFT JOIN crm.customers c ON c.id = q.customer_id LEFT JOIN crm.sales_leads l ON l.id = q.lead_id
	LEFT JOIN platform.users u ON u.id = q.owner_user_id`

// QuotationVersion is a version of the same quotation number.
type QuotationVersion struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Version   int       `json:"version" db:"version"`
	Status    string    `json:"status" db:"status"`
	Total     string    `json:"total" db:"total"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// QuotationDetail is a quotation with lines, versions and its link.
type QuotationDetail struct {
	Quotation
	Lines      []QuotationLine    `json:"lines"`
	Versions   []QuotationVersion `json:"versions"`
	PublicLink *string            `json:"publicLink" doc:"Secure acceptance link (sent quotations)"`
	Decision   *QuotationDecision `json:"decision"`
	// Acceptance evidence and e-Meterai (PRD P3 §16 #18, p3_acceptance.go).
	AcceptanceEvidence *QuotationAcceptanceEvidence `json:"acceptanceEvidence" doc:"IP, user agent and verified one-time code of the acceptance"`
	EMeteraiRequired   bool                         `json:"eMeteraiRequired" doc:"Total above the e-Meterai threshold of the Sales Policies"`
	EMeterai           QuotationEMeterai            `json:"eMeterai"`
	otpRequired        bool
}

// QuotationDecision is the recorded acceptance or rejection.
type QuotationDecision struct {
	Decision      string    `json:"decision" db:"decision" enum:"accepted,rejected"`
	Via           string    `json:"via" db:"via" enum:"staff,public_link"`
	Name          *string   `json:"name" db:"name"`
	IP            *string   `json:"ip" db:"ip"`
	TermsAccepted bool      `json:"termsAccepted" db:"terms_accepted"`
	Note          *string   `json:"note" db:"note"`
	DecidedAt     time.Time `json:"decidedAt" db:"decided_at"`
}

// GetQuotation loads a quotation version.
func GetQuotation(ctx context.Context, q dbtx.Querier, qid uuid.UUID) (Quotation, error) {
	return getOne[Quotation]("quotation")(q.Query(ctx, quotationSelect+` WHERE q.id = $1`, qid))
}

// QuotationDetailOf loads a quotation with its lines and versions.
func (m *Module) QuotationDetailOf(ctx context.Context, q dbtx.Querier, qid uuid.UUID) (QuotationDetail, error) {
	h, err := GetQuotation(ctx, q, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	d := QuotationDetail{Quotation: h}
	if d.Lines, err = handle.List[QuotationLine](q.Query(ctx, lineSelect+` WHERE quotation_id = $1 ORDER BY line_no`, qid)); err != nil {
		return d, err
	}
	if d.Versions, err = handle.List[QuotationVersion](q.Query(ctx, `SELECT id, version, status, trim_scale(total)::text AS total, created_at
		FROM crm.sales_quotations WHERE property_id = (SELECT property_id FROM crm.sales_quotations WHERE id = $1)
		AND number = $2 ORDER BY version`, qid, h.Number)); err != nil {
		return d, err
	}
	var token *string
	if err := q.QueryRow(ctx, `SELECT public_token FROM crm.sales_quotations WHERE id = $1`, qid).Scan(&token); err != nil {
		return d, err
	}
	if token != nil && h.Status != "draft" && h.Status != "pending_approval" {
		l := m.publicLink(*token)
		d.PublicLink = &l
	}
	decs, err := handle.List[QuotationDecision](q.Query(ctx, `SELECT decision, via, name, ip, terms_accepted, note, decided_at
		FROM crm.sales_quotation_decisions WHERE quotation_id = $1`, qid))
	if err != nil {
		return d, err
	}
	if len(decs) > 0 {
		d.Decision = &decs[0]
	}
	return d, m.fillAcceptance(ctx, q, &d)
}

func lockQuotation(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID) (Quotation, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_quotations WHERE id = $1 AND property_id = $2 FOR UPDATE`, qid, property).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Quotation{}, errs.NotFound("quotation")
		}
		return Quotation{}, err
	}
	return GetQuotation(ctx, tx, qid)
}

// ── calculation ───────────────────────────────────────────────────────────

type calcLine struct {
	in                                       QuotationLineInput
	qty, unit, lineDisc, share, total        decimal.Decimal
	net, svc, tax, gross                     decimal.Decimal
	source, mode                             string
	codes                                    []string
	snapshot                                 map[string]any
	itemType, itemRef, serviceType, describe string
}

type quoteCalc struct {
	lines                                    []calcLine
	subtotal, discount, net, svc, tax, total decimal.Decimal
	discountPct, headerDiscount              decimal.Decimal
	headerPct                                *decimal.Decimal
	terms                                    []PaymentTerm
}

type quoteHeader struct {
	currency, mode string
	codes          []string
	at             time.Time
	eventDate      *time.Time
	headerDisc     string
	headerPct      string
	terms          []PaymentTermInput
	termDefaults   []PaymentTermRule
	base           time.Time // due date base (today, or the acceptance date)
}

// calculate prices the lines, allocates the total discount pro rata and
// computes the totals and the payment terms.
func (m *Module) calculate(ctx context.Context, q dbtx.Querier, property uuid.UUID, h quoteHeader, inputs []QuotationLineInput) (quoteCalc, error) {
	var c quoteCalc
	afterLine := decimal.Zero
	for i, in := range inputs {
		f := "lines[" + itoa(i) + "]"
		if !oneOf(ItemTypes, in.ItemType) {
			return c, enumErr(f+".itemType", ItemTypes)
		}
		if err := handle.Required(f+".description", in.Description); err != nil {
			return c, err
		}
		qty, err := handle.Decimal(f+".quantity", in.Quantity, decimal.NewFromInt(1))
		if err != nil {
			return c, err
		}
		if !qty.IsPositive() {
			return c, handle.Invalid(f+".quantity", "invalid", "a positive quantity")
		}
		l := calcLine{in: in, qty: qty, itemType: in.ItemType, itemRef: strings.TrimSpace(in.ItemRef), serviceType: strings.TrimSpace(in.ServiceType),
			describe: strings.TrimSpace(in.Description), mode: h.mode, codes: h.codes, source: "manual"}
		if in.mode != "" {
			l.mode, l.codes = in.mode, in.codes
		}
		if strings.TrimSpace(in.UnitPrice) != "" {
			u, err := handle.Decimal(f+".unitPrice", in.UnitPrice, decimal.Zero)
			if err != nil {
				return c, err
			}
			if u.IsNegative() {
				return c, handle.Invalid(f+".unitPrice", "invalid", "a positive price")
			}
			l.unit = u
			if in.src != "" {
				l.source = in.src
			}
		} else {
			p, err := m.price(ctx, q, property, PriceInput{ItemType: l.itemType, ItemRef: l.itemRef, ServiceType: l.serviceType, Quantity: qty,
				At: h.at, Currency: h.currency, PricingMode: h.mode, TaxCodes: h.codes})
			if err != nil {
				return c, err
			}
			l.unit, l.source, l.snapshot = p.UnitPrice, p.Source, p.Snapshot
			if p.PricingMode != "" {
				l.mode = p.PricingMode
			}
			if p.TaxCodes != nil {
				l.codes = p.TaxCodes
			}
		}
		gross := l.unit.Mul(qty)
		switch {
		case strings.TrimSpace(in.DiscountPercent) != "":
			p, err := handle.Decimal(f+".discountPercent", in.DiscountPercent, decimal.Zero)
			if err != nil || p.IsNegative() || p.GreaterThan(hundred) {
				return c, handle.Invalid(f+".discountPercent", "invalid", "percent between 0 and 100")
			}
			l.lineDisc = round(gross.Mul(p).Div(hundred), h.currency)
		case strings.TrimSpace(in.Discount) != "":
			d, err := handle.Decimal(f+".discount", in.Discount, decimal.Zero)
			if err != nil {
				return c, err
			}
			l.lineDisc = d
		}
		if l.lineDisc.IsNegative() || l.lineDisc.GreaterThan(gross) {
			return c, handle.Invalid(f+".discount", "invalid", "between 0 and the line amount")
		}
		c.subtotal = c.subtotal.Add(gross)
		afterLine = afterLine.Add(gross.Sub(l.lineDisc))
		c.lines = append(c.lines, l)
	}
	// discount on the total
	switch {
	case strings.TrimSpace(h.headerPct) != "":
		p, err := handle.Decimal("discountPercent", h.headerPct, decimal.Zero)
		if err != nil || p.IsNegative() || p.GreaterThan(hundred) {
			return c, handle.Invalid("discountPercent", "invalid", "percent between 0 and 100")
		}
		c.headerPct = &p
		c.headerDiscount = round(afterLine.Mul(p).Div(hundred), h.currency)
	case strings.TrimSpace(h.headerDisc) != "":
		d, err := handle.Decimal("discount", h.headerDisc, decimal.Zero)
		if err != nil {
			return c, err
		}
		c.headerDiscount = d
	}
	if c.headerDiscount.IsNegative() || c.headerDiscount.GreaterThan(afterLine) {
		return c, handle.Invalid("discount", "invalid", "between 0 and the quotation amount")
	}
	// allocate the total discount pro rata; the last line takes the rest
	left := c.headerDiscount
	for i := range c.lines {
		l := &c.lines[i]
		base := l.unit.Mul(l.qty).Sub(l.lineDisc)
		if i == len(c.lines)-1 {
			l.share = left
		} else if afterLine.IsPositive() {
			l.share = round(c.headerDiscount.Mul(base).Div(afterLine), h.currency)
			if l.share.GreaterThan(left) {
				l.share = left
			}
		}
		left = left.Sub(l.share)
		unit := l.unit
		p, err := m.price(ctx, q, property, PriceInput{ItemType: l.itemType, ItemRef: l.itemRef, ServiceType: l.serviceType, Quantity: l.qty,
			UnitPrice: &unit, Discount: l.lineDisc.Add(l.share), At: h.at, Currency: h.currency, PricingMode: l.mode, TaxCodes: l.codes})
		if err != nil {
			return c, err
		}
		l.total = base.Sub(l.share)
		l.net, l.svc, l.tax, l.gross = p.Net, p.Service, p.Tax, p.Total
		if l.snapshot == nil {
			l.snapshot = p.Snapshot
		}
		if l.snapshot == nil {
			l.snapshot = map[string]any{}
		}
		l.snapshot["pricingMode"], l.snapshot["taxCodes"] = l.mode, l.codes
		c.discount = c.discount.Add(l.lineDisc).Add(l.share)
		c.net, c.svc, c.tax, c.total = c.net.Add(l.net), c.svc.Add(l.svc), c.tax.Add(l.tax), c.total.Add(l.gross)
	}
	if c.subtotal.IsPositive() {
		c.discountPct = c.discount.Mul(hundred).Div(c.subtotal).Round(4)
	}
	terms, err := buildTerms(h.terms, h.termDefaults, c.total, h.currency, h.base, h.eventDate)
	if err != nil {
		return c, err
	}
	c.terms = terms
	return c, nil
}

// buildTerms computes the amounts and due dates of the payment terms; the
// last percent term absorbs the rounding so the terms add up to the total.
func buildTerms(in []PaymentTermInput, defaults []PaymentTermRule, total decimal.Decimal, cur string, base time.Time, event *time.Time) ([]PaymentTerm, error) {
	if len(in) == 0 {
		for _, d := range defaults {
			in = append(in, PaymentTermInput{Label: d.Label, Percent: d.Percent, DueDays: d.DueDays, DaysBeforeEvent: d.DaysBeforeEvent})
		}
	}
	out := []PaymentTerm{}
	sum := decimal.Zero
	for i, t := range in {
		f := "paymentTerms[" + itoa(i) + "]"
		if err := handle.Required(f+".label", t.Label); err != nil {
			return nil, err
		}
		pt := PaymentTerm{Label: strings.TrimSpace(t.Label), DueDays: t.DueDays, DaysBeforeEvent: t.DaysBeforeEvent}
		var amt, pct decimal.Decimal
		switch {
		case strings.TrimSpace(t.Amount) != "":
			a, err := handle.Decimal(f+".amount", t.Amount, decimal.Zero)
			if err != nil {
				return nil, err
			}
			amt, pt.ByAmount = a, true
			if total.IsPositive() {
				pct = a.Mul(hundred).Div(total).Round(2)
			}
		case strings.TrimSpace(t.Percent) != "":
			p, err := handle.Decimal(f+".percent", t.Percent, decimal.Zero)
			if err != nil {
				return nil, err
			}
			pct = p
			amt = round(total.Mul(p).Div(hundred), cur)
			if i == len(in)-1 {
				amt = total.Sub(sum)
			}
		default:
			return nil, handle.Invalid(f+".percent", "required", "percent or amount")
		}
		if amt.IsNegative() || pct.IsNegative() {
			return nil, handle.Invalid(f+".amount", "invalid", "a positive amount")
		}
		due := base.AddDate(0, 0, 7)
		switch {
		case t.DueDate != nil:
			d, err := parseDate(f+".dueDate", string(*t.DueDate))
			if err != nil {
				return nil, err
			}
			if d != nil {
				due, pt.FixedDate = *d, true
			}
		case t.DueDays != nil:
			due = base.AddDate(0, 0, *t.DueDays)
		case t.DaysBeforeEvent != nil && event != nil:
			due = event.AddDate(0, 0, -*t.DaysBeforeEvent)
		case t.DaysBeforeEvent != nil:
			due = base.AddDate(0, 0, 30)
		}
		if due.Before(base) && !pt.FixedDate {
			due = base
		}
		pt.DueDate = due.Format("2006-01-02")
		pt.Amount, pt.Percent = amt.String(), pct.String()
		sum = sum.Add(amt)
		out = append(out, pt)
	}
	if total.IsPositive() && len(out) > 0 && !sum.Equal(total) {
		return nil, errs.Validation("payment_terms_mismatch", "the payment terms add up to "+sum.String()+", not "+total.String(),
			errs.Field("paymentTerms", "mismatch", "terms must add up to the quotation total"))
	}
	return out, nil
}

// discountLimit is the discount the user may give without approval: the
// highest Sales Policies role limit of the user, else the general limit.
func discountLimit(ctx context.Context, q dbtx.Querier, property uuid.UUID, pol SalesPolicy, user *uuid.UUID) decimal.Decimal {
	limit := dec(pol.MaxDiscountPercent)
	if len(pol.RoleDiscountLimits) == 0 || user == nil {
		return limit
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT r.code FROM platform.role_assignments ra JOIN platform.roles r ON r.id = ra.role_id
		WHERE ra.user_id = $1 AND (ra.property_id IS NULL OR ra.property_id = $2)`, *user, property)
	if err != nil {
		return limit
	}
	codes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return limit
	}
	var best *decimal.Decimal
	for _, c := range codes {
		if v, ok := pol.RoleDiscountLimits[c]; ok {
			d := dec(v)
			if best == nil || d.GreaterThan(*best) {
				best = &d
			}
		}
	}
	if best != nil {
		return *best
	}
	return limit
}

func approvalStatusFor(pct, limit decimal.Decimal, approved *decimal.Decimal) string {
	switch {
	case !pct.GreaterThan(limit):
		return "not_required"
	case approved != nil && !pct.GreaterThan(*approved):
		return "approved"
	}
	return "required"
}

// ── create & edit ─────────────────────────────────────────────────────────

// CreateQuotation creates a draft quotation version 1.
func (m *Module) CreateQuotation(ctx context.Context, tx pgx.Tx, property uuid.UUID, in QuotationInput) (QuotationDetail, error) {
	pol, ref, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return QuotationDetail{}, err
	}
	today := localToday(ctx, tx, property)
	var oppOwner *uuid.UUID
	if in.OpportunityID != nil {
		o, err := GetOpportunity(ctx, tx, *in.OpportunityID)
		if err != nil {
			return QuotationDetail{}, handle.Invalid("opportunityId", "not_found", "opportunity not found")
		}
		var prop uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id FROM crm.sales_opportunities WHERE id = $1`, o.ID).Scan(&prop); err != nil || prop != property {
			return QuotationDetail{}, handle.Invalid("opportunityId", "not_found", "opportunity not found")
		}
		if o.Status != "open" {
			return QuotationDetail{}, conflict("opportunity_closed", "the opportunity is "+o.Status)
		}
		if in.CustomerID == nil {
			in.CustomerID = o.CustomerID
		}
		if in.CorporateAccountID == nil {
			in.CorporateAccountID = o.CorporateAccountID
		}
		if in.LeadID == nil {
			in.LeadID = o.LeadID
		}
		if in.Line == "" {
			in.Line = o.Line
		}
		if in.EventType == "" && o.EventType != nil {
			in.EventType = *o.EventType
		}
		if in.EventDate == nil && o.EventDate != nil {
			d := route.Date(*o.EventDate)
			in.EventDate = &d
		}
		if in.EndDate == nil && o.EndDate != nil {
			d := route.Date(*o.EndDate)
			in.EndDate = &d
		}
		if in.Pax == nil {
			in.Pax = o.Pax
		}
		if in.VenueResourceID == nil {
			in.VenueResourceID = o.VenueResourceID
		}
		if in.PackageRef == "" && o.PackageRef != nil {
			in.PackageRef = *o.PackageRef
		}
		if strings.TrimSpace(in.Title) == "" {
			in.Title = o.Title
		}
		oppOwner = o.OwnerUserID
	} else if in.LeadID != nil {
		l, err := GetLead(ctx, tx, *in.LeadID)
		if err != nil {
			return QuotationDetail{}, handle.Invalid("leadId", "not_found", "lead not found")
		}
		if l.Status == "unqualified" {
			return QuotationDetail{}, conflict("lead_unqualified", "the lead is unqualified")
		}
		if l.OpportunityID != nil {
			in.OpportunityID = l.OpportunityID
		}
		if in.CustomerID == nil {
			in.CustomerID = l.CustomerID
		}
		if in.CorporateAccountID == nil {
			in.CorporateAccountID = l.CorporateAccountID
		}
		if in.Line == "" {
			in.Line = l.Line
		}
		if in.EventType == "" && l.EventType != nil {
			in.EventType = *l.EventType
		}
		if in.EventDate == nil && l.EventDate != nil {
			d := route.Date(*l.EventDate)
			in.EventDate = &d
		}
		if in.Pax == nil {
			in.Pax = l.Pax
		}
		if strings.TrimSpace(in.Title) == "" {
			in.Title = lineLabel(l.Line) + " · " + l.Name
		}
		oppOwner = l.OwnerUserID
	}
	if in.CustomerID == nil && in.LeadID == nil {
		return QuotationDetail{}, handle.Invalid("customerId", "required", "an opportunity, a lead or a customer is required")
	}
	if in.CustomerID != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *in.CustomerID); err != nil || !ok {
			if err != nil {
				return QuotationDetail{}, err
			}
			return QuotationDetail{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
	}
	if in.CorporateAccountID != nil {
		if _, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID); err != nil {
			return QuotationDetail{}, err
		}
	}
	owner := in.OwnerUserID
	if owner == nil {
		owner = oppOwner
	}
	if owner == nil {
		owner = actor(ctx)
	}
	if owner != nil {
		if err := ensureUser(ctx, tx, "ownerUserId", *owner); err != nil {
			return QuotationDetail{}, err
		}
	}
	if err := handle.Required("title", in.Title); err != nil {
		return QuotationDetail{}, err
	}
	if len(in.Lines) == 0 {
		return QuotationDetail{}, handle.Invalid("lines", "required", "at least one line")
	}
	if err := checkFreeItems(ctx, property, in.Lines, nil); err != nil {
		return QuotationDetail{}, err
	}
	hdr, err := headerOf(in, pol, today)
	if err != nil {
		return QuotationDetail{}, err
	}
	validUntil := today.AddDate(0, 0, max(pol.QuotationValidityDays, 1))
	if in.ValidUntil != nil {
		d, err := parseDate("validUntil", string(*in.ValidUntil))
		if err != nil {
			return QuotationDetail{}, err
		}
		if d != nil {
			validUntil = *d
		}
	}
	if validUntil.Before(today) {
		return QuotationDetail{}, handle.Invalid("validUntil", "past", "the validity date cannot be in the past")
	}
	optionDate, err := optDate("optionDate", in.OptionDate)
	if err != nil {
		return QuotationDetail{}, err
	}
	if optionDate == nil && in.VenueResourceID != nil && pol.OptionDays > 0 {
		d := today.AddDate(0, 0, pol.OptionDays)
		if d.After(validUntil) {
			d = validUntil
		}
		optionDate = &d
	}
	calc, err := m.calculate(ctx, tx, property, hdr.quoteHeader, in.Lines)
	if err != nil {
		return QuotationDetail{}, err
	}
	limit := discountLimit(ctx, tx, property, pol, actor(ctx))
	approvalStatus := approvalStatusFor(calc.discountPct, limit, nil)
	number, err := yearlyNumber(ctx, tx, property, "QUO", today.Year())
	if err != nil {
		return QuotationDetail{}, err
	}
	terms := pol.QuotationTerms
	if in.Terms != nil {
		terms = *in.Terms
	}
	notes := ""
	if in.Notes != nil {
		notes = *in.Notes
	}
	var hdrPct *string
	if calc.headerPct != nil {
		s := calc.headerPct.String()
		hdrPct = &s
	}
	termsJSON, _ := json.Marshal(calc.terms)
	qid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_quotations (id, property_id, number, version, opportunity_id, lead_id, customer_id,
		corporate_account_id, owner_user_id, line, event_type, event_date, end_date, pax, venue_resource_id, package_ref, title, currency, pricing_mode,
		tax_codes, header_discount, header_discount_percent, valid_until, option_date, approval_status, payment_terms, terms, notes, policy_version,
		created_by, updated_by)
		VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::numeric,$21::numeric,$22,$23,$24,$25,$26,$27,$28,$29,$29)`,
		qid, property, number, in.OpportunityID, in.LeadID, in.CustomerID, in.CorporateAccountID, owner, hdr.line, nullStr(in.EventType),
		hdr.eventDate, hdr.endDate, in.Pax, in.VenueResourceID, nullStr(in.PackageRef), strings.TrimSpace(in.Title), hdr.currency, hdr.mode, hdr.codes,
		calc.headerDiscount.String(), hdrPct, validUntil, optionDate, approvalStatus, termsJSON, nullStr(terms), nullStr(notes), ref.Version,
		actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	if err := saveLines(ctx, tx, property, qid, calc); err != nil {
		return QuotationDetail{}, err
	}
	d, err := m.QuotationDetailOf(ctx, tx, qid)
	if err != nil {
		return d, err
	}
	return d, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: number + " v1 · " + d.Title, PropertyID: &property, After: d})
}

type headerSpec struct {
	quoteHeader
	line    string
	endDate *time.Time
}

// headerOf validates the header fields of a quotation input.
func headerOf(in QuotationInput, pol SalesPolicy, today time.Time) (headerSpec, error) {
	h := headerSpec{line: in.Line}
	if h.line == "" {
		h.line = "other"
	}
	if !oneOf(Lines, h.line) {
		return h, enumErr("line", Lines)
	}
	if in.EventType != "" && !oneOf(EventTypes, in.EventType) {
		return h, enumErr("eventType", EventTypes)
	}
	if in.Pax != nil && *in.Pax <= 0 {
		return h, handle.Invalid("pax", "invalid", "a positive number of guests")
	}
	var err error
	if h.eventDate, err = optDate("eventDate", in.EventDate); err != nil {
		return h, err
	}
	if h.endDate, err = optDate("endDate", in.EndDate); err != nil {
		return h, err
	}
	h.currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if h.currency == "" {
		h.currency = "IDR"
	}
	h.mode = in.PricingMode
	if h.mode == "" {
		h.mode = pol.PricingMode
	}
	if h.mode != "nett" && h.mode != "plus_plus" {
		return h, enumErr("pricingMode", []string{"nett", "plus_plus"})
	}
	h.codes = append([]string{}, pol.TaxCodes...)
	h.at = today
	if h.eventDate != nil {
		h.at = *h.eventDate
	}
	if in.Discount != nil {
		h.headerDisc = *in.Discount
	}
	if in.DiscountPercent != nil {
		h.headerPct = *in.DiscountPercent
	}
	h.terms, h.termDefaults, h.base = in.PaymentTerms, pol.DefaultPaymentTerms, today
	return h, nil
}

// saveLines replaces the lines of a quotation and stores its totals.
func saveLines(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, c quoteCalc) error {
	if _, err := tx.Exec(ctx, `DELETE FROM crm.sales_quotation_lines WHERE quotation_id = $1`, qid); err != nil {
		return err
	}
	for i, l := range c.lines {
		snap, _ := json.Marshal(l.snapshot)
		if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_quotation_lines (id, property_id, quotation_id, line_no, item_type, item_ref, service_type,
			description, quantity, unit_price, discount, header_discount_share, total, net_amount, service_amount, tax_amount, gross_total, price_source, pricing)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15::numeric,$16::numeric,
			$17::numeric,$18,$19)`, id.New(), property, qid, i+1, l.itemType, nullStr(l.itemRef), nullStr(l.serviceType), l.describe, l.qty.String(),
			l.unit.String(), l.lineDisc.String(), l.share.String(), l.total.String(), l.net.String(), l.svc.String(), l.tax.String(), l.gross.String(),
			l.source, snap); err != nil {
			return err
		}
	}
	termsJSON, _ := json.Marshal(c.terms)
	_, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET subtotal = $2::numeric, discount = $3::numeric, discount_percent = $4::numeric,
		net_amount = $5::numeric, service_amount = $6::numeric, tax_amount = $7::numeric, total = $8::numeric, header_discount = $9::numeric,
		payment_terms = $10 WHERE id = $1`, qid, c.subtotal.String(), c.discount.String(), c.discountPct.String(), c.net.String(), c.svc.String(),
		c.tax.String(), c.total.String(), c.headerDiscount.String(), termsJSON)
	return err
}

// storedLines turns stored lines back into inputs (keeping their price
// source, pricing mode and tax codes).
// freeItemKeys identifies the free-text items (item type other) of lines.
func freeItemKeys(ls []QuotationLineInput) map[string]bool {
	out := map[string]bool{}
	for _, l := range ls {
		if l.ItemType == "other" {
			out[strings.TrimSpace(l.Description)+"|"+dec(l.UnitPrice).String()+"|"+dec(l.Quantity).String()] = true
		}
	}
	return out
}

// checkFreeItems requires crm.quotation.free_item to add or change a
// free-text item; items from packages, menus, rates, products and services
// do not need it (FR-QUO-01).
func checkFreeItems(ctx context.Context, property uuid.UUID, lines, stored []QuotationLineInput) error {
	if can(ctx, "crm.quotation.free_item", property) {
		return nil
	}
	old := freeItemKeys(stored)
	for k := range freeItemKeys(lines) {
		if !old[k] {
			return errs.Forbidden("free-text items (item type other) need crm.quotation.free_item")
		}
	}
	return nil
}

func storedLines(ls []QuotationLine) []QuotationLineInput {
	out := make([]QuotationLineInput, 0, len(ls))
	for _, l := range ls {
		in := QuotationLineInput{ItemType: l.ItemType, ItemRef: deref(l.ItemRef), ServiceType: deref(l.ServiceType), Description: l.Description,
			Quantity: l.Quantity, UnitPrice: l.UnitPrice, Discount: l.LineDiscount, src: l.PriceSource}
		var snap struct {
			PricingMode string   `json:"pricingMode"`
			TaxCodes    []string `json:"taxCodes"`
		}
		if json.Unmarshal(l.Pricing, &snap) == nil && snap.PricingMode != "" {
			in.mode, in.codes = snap.PricingMode, snap.TaxCodes
			if in.codes == nil {
				in.codes = []string{}
			}
		}
		out = append(out, in)
	}
	return out
}

func storedTerms(ts []PaymentTerm) []PaymentTermInput {
	out := make([]PaymentTermInput, 0, len(ts))
	for _, t := range ts {
		in := PaymentTermInput{Label: t.Label, Percent: t.Percent, DueDays: t.DueDays, DaysBeforeEvent: t.DaysBeforeEvent}
		if t.ByAmount {
			in.Percent, in.Amount = "", t.Amount
		}
		if t.FixedDate {
			d := route.Date(t.DueDate)
			in.DueDate = &d
		}
		out = append(out, in)
	}
	return out
}

// UpdateQuotation edits a draft (lines given replace all lines).
func (m *Module) UpdateQuotation(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, in QuotationInput) (QuotationDetail, error) {
	before, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	if before.Status != "draft" {
		return QuotationDetail{}, conflict("quotation_not_draft", "only a draft can be edited; revise a "+before.Status+" quotation")
	}
	if (in.OpportunityID != nil && (before.OpportunityID == nil || *in.OpportunityID != *before.OpportunityID)) ||
		(in.LeadID != nil && (before.LeadID == nil || *in.LeadID != *before.LeadID)) {
		return QuotationDetail{}, handle.Invalid("opportunityId", "immutable", "the opportunity / lead of a quotation cannot change")
	}
	cur, err := m.QuotationDetailOf(ctx, tx, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return QuotationDetail{}, err
	}
	today := localToday(ctx, tx, property)
	merged := QuotationInput{Title: before.Title, Line: before.Line, EventType: deref(before.EventType), PackageRef: deref(before.PackageRef),
		Currency: before.Currency, PricingMode: before.PricingMode, Pax: before.Pax, VenueResourceID: before.VenueResourceID,
		CustomerID: before.CustomerID, CorporateAccountID: before.CorporateAccountID, Terms: before.Terms, Notes: before.Notes}
	if before.EventDate != nil {
		d := route.Date(*before.EventDate)
		merged.EventDate = &d
	}
	if before.EndDate != nil {
		d := route.Date(*before.EndDate)
		merged.EndDate = &d
	}
	if before.HeaderDiscountPercent != nil {
		merged.DiscountPercent = before.HeaderDiscountPercent
	} else {
		merged.Discount = &before.HeaderDiscount
	}
	if in.Title != "" {
		merged.Title = in.Title
	}
	if in.Line != "" {
		merged.Line = in.Line
	}
	if in.EventType != "" {
		merged.EventType = in.EventType
	}
	if in.EventDate != nil {
		merged.EventDate = in.EventDate
	}
	if in.EndDate != nil {
		merged.EndDate = in.EndDate
	}
	if in.Pax != nil {
		merged.Pax = in.Pax
	}
	if in.VenueResourceID != nil {
		merged.VenueResourceID = in.VenueResourceID
	}
	if in.PackageRef != "" {
		merged.PackageRef = in.PackageRef
	}
	if in.Currency != "" {
		merged.Currency = in.Currency
	}
	if in.PricingMode != "" {
		merged.PricingMode = in.PricingMode
	}
	if in.CustomerID != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *in.CustomerID); err != nil || !ok {
			if err != nil {
				return QuotationDetail{}, err
			}
			return QuotationDetail{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
		merged.CustomerID = in.CustomerID
	}
	if in.CorporateAccountID != nil {
		if _, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID); err != nil {
			return QuotationDetail{}, err
		}
		merged.CorporateAccountID = in.CorporateAccountID
	}
	if in.Discount != nil {
		merged.Discount, merged.DiscountPercent = in.Discount, nil
	}
	if in.DiscountPercent != nil {
		merged.DiscountPercent, merged.Discount = in.DiscountPercent, nil
	}
	if in.Terms != nil {
		merged.Terms = in.Terms
	}
	if in.Notes != nil {
		merged.Notes = in.Notes
	}
	merged.PaymentTerms = storedTerms(cur.PaymentTerms)
	if in.PaymentTerms != nil {
		merged.PaymentTerms = in.PaymentTerms
	}
	lines := storedLines(cur.Lines)
	if in.Lines != nil {
		if len(in.Lines) == 0 {
			return QuotationDetail{}, handle.Invalid("lines", "required", "at least one line")
		}
		if err := checkFreeItems(ctx, property, in.Lines, lines); err != nil {
			return QuotationDetail{}, err
		}
		lines = in.Lines
	}
	if err := handle.Required("title", merged.Title); err != nil {
		return QuotationDetail{}, err
	}
	hdr, err := headerOf(merged, pol, today)
	if err != nil {
		return QuotationDetail{}, err
	}
	hdr.codes = before.TaxCodes
	if hdr.codes == nil {
		hdr.codes = []string{}
	}
	validUntil, _ := time.Parse("2006-01-02", before.ValidUntil)
	if in.ValidUntil != nil {
		d, err := parseDate("validUntil", string(*in.ValidUntil))
		if err != nil {
			return QuotationDetail{}, err
		}
		if d != nil {
			validUntil = *d
		}
	}
	if validUntil.Before(today) {
		return QuotationDetail{}, handle.Invalid("validUntil", "past", "the validity date cannot be in the past")
	}
	var optionDate *time.Time
	if before.OptionDate != nil {
		d, _ := time.Parse("2006-01-02", *before.OptionDate)
		optionDate = &d
	}
	if in.OptionDate != nil {
		if optionDate, err = optDate("optionDate", in.OptionDate); err != nil {
			return QuotationDetail{}, err
		}
	}
	if in.ClearOptionDate {
		optionDate = nil
	}
	calc, err := m.calculate(ctx, tx, property, hdr.quoteHeader, lines)
	if err != nil {
		return QuotationDetail{}, err
	}
	var approved *decimal.Decimal
	if s := approvedPercent(ctx, tx, qid); s != nil {
		approved = s
	}
	approvalStatus := approvalStatusFor(calc.discountPct, discountLimit(ctx, tx, property, pol, actor(ctx)), approved)
	var hdrPct *string
	if calc.headerPct != nil {
		s := calc.headerPct.String()
		hdrPct = &s
	}
	owner := before.OwnerUserID
	if in.OwnerUserID != nil {
		if err := ensureUser(ctx, tx, "ownerUserId", *in.OwnerUserID); err != nil {
			return QuotationDetail{}, err
		}
		owner = in.OwnerUserID
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET title = $2, line = $3, event_type = $4, event_date = $5, end_date = $6, pax = $7,
		venue_resource_id = $8, package_ref = $9, currency = $10, pricing_mode = $11, customer_id = $12, corporate_account_id = $13,
		header_discount_percent = $14::numeric, valid_until = $15, option_date = $16, approval_status = $17, terms = $18, notes = $19,
		owner_user_id = $20, updated_by = $21 WHERE id = $1`,
		qid, strings.TrimSpace(merged.Title), hdr.line, nullStr(merged.EventType), hdr.eventDate, hdr.endDate, merged.Pax, merged.VenueResourceID,
		nullStr(merged.PackageRef), hdr.currency, hdr.mode, merged.CustomerID, merged.CorporateAccountID, hdrPct, validUntil, optionDate,
		approvalStatus, merged.Terms, merged.Notes, owner, actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	if err := saveLines(ctx, tx, property, qid, calc); err != nil {
		return QuotationDetail{}, err
	}
	after, err := m.QuotationDetailOf(ctx, tx, qid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionUpdate, EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: before.Number + " v" + itoa(before.Version) + " · " + after.Title, PropertyID: &property, Before: cur, After: after})
}

func approvedPercent(ctx context.Context, q dbtx.Querier, qid uuid.UUID) *decimal.Decimal {
	var s *string
	if err := q.QueryRow(ctx, `SELECT approved_discount_percent::text FROM crm.sales_quotations WHERE id = $1`, qid).Scan(&s); err != nil || s == nil {
		return nil
	}
	d := dec(*s)
	return &d
}

// ── approval (FR-QUO-02) ──────────────────────────────────────────────────

// SubmitDiscountApproval asks approval for a discount above the limit.
func (m *Module) SubmitDiscountApproval(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID) (QuotationDetail, error) {
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	if q.Status != "draft" {
		return QuotationDetail{}, conflict("quotation_not_draft", "only a draft can be submitted for approval")
	}
	if q.ApprovalStatus != "required" && q.ApprovalStatus != "rejected" {
		return QuotationDetail{}, conflict("approval_not_required", "the discount is within the Sales Policies limit")
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET status = 'pending_approval', approval_status = 'pending', updated_by = $2 WHERE id = $1`,
		qid, actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "submit_approval", EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, Before: map[string]any{"status": q.Status},
		After: map[string]any{"status": "pending_approval", "discountPercent": q.DiscountPercent}}); err != nil {
		return QuotationDetail{}, err
	}
	pct, _ := dec(q.DiscountPercent).Float64()
	total, _ := dec(q.Total).Float64()
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: DiscountDocumentType.Code, DocumentID: qid,
		DocumentRef: q.Number + " v" + itoa(q.Version), Title: "Quotation discount " + q.Number + " · " + q.DiscountPercent + "%", PropertyID: property,
		Attributes: map[string]any{"discountPercent": pct, "total": total, "line": q.Line}})
	if err != nil {
		return QuotationDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET approval_request_id = $2 WHERE id = $1`, qid, rid); err != nil {
		return QuotationDetail{}, err
	}
	return m.QuotationDetailOf(ctx, tx, qid)
}

// DiscountDecision applies the approval decision (approval engine hook).
func (m *Module) DiscountDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, number, pct string
	var version int
	var owner *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, number, version, discount_percent::text, owner_user_id FROM crm.sales_quotations WHERE id = $1 FOR UPDATE`,
		d.DocumentID).Scan(&status, &number, &version, &pct, &owner); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if status != "pending_approval" {
		return nil
	}
	approvalStatus := "rejected"
	var approvedPct *string
	switch d.Status {
	case approval.StatusApproved:
		approvalStatus, approvedPct = "approved", &pct
	case approval.StatusCancelled:
		approvalStatus = "required"
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET status = 'draft', approval_status = $2,
		approved_discount_percent = coalesce($3::numeric, approved_discount_percent) WHERE id = $1`, d.DocumentID, approvalStatus, approvedPct); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "discount_" + approvalStatus, EntityType: "crm.quotation",
		EntityID: d.DocumentID.String(), EntityLabel: number + " v" + itoa(version), PropertyID: &d.PropertyID, Reason: d.Reason,
		Before: map[string]any{"status": status}, After: map[string]any{"status": "draft", "approvalStatus": approvalStatus}}); err != nil {
		return err
	}
	if owner != nil && d.Status != approval.StatusCancelled {
		return m.notifyUsers(ctx, tx, d.PropertyID, []uuid.UUID{*owner}, "crm.sales_quotation_discount_decided",
			m.staffLink("/crm/quotations/"+d.DocumentID.String()), map[string]any{"number": number, "decision": approvalStatus, "discount": pct})
	}
	return nil
}

// ── send (FR-QUO-05) ──────────────────────────────────────────────────────

// SendInput sends a quotation by e-mail and / or WhatsApp.
type SendInput struct {
	Channels []string `json:"channels,omitempty" enum:"email,whatsapp" doc:"Default: e-mail, plus WhatsApp when a phone is known"`
	Email    string   `json:"email,omitempty" doc:"Override the customer e-mail"`
	Phone    string   `json:"phone,omitempty" doc:"Override the customer phone"`
	Message  string   `json:"message,omitempty"`
}

// SendQuotation issues the secure link and sends the quotation.
func (m *Module) SendQuotation(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, in SendInput) (QuotationDetail, error) {
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	if q.Status != "draft" && q.Status != "sent" {
		return QuotationDetail{}, conflict("quotation_not_sendable", "a "+q.Status+" quotation cannot be sent")
	}
	if q.ApprovalStatus != "not_required" && q.ApprovalStatus != "approved" {
		return QuotationDetail{}, conflict("approval_required", "the discount of "+q.DiscountPercent+"% needs approval before sending")
	}
	if !dec(q.Total).IsPositive() {
		return QuotationDetail{}, conflict("quotation_empty", "the quotation has no amount")
	}
	today := localToday(ctx, tx, property)
	valid, _ := time.Parse("2006-01-02", q.ValidUntil)
	if valid.Before(today) {
		return QuotationDetail{}, conflict("quotation_expired", "the validity date has passed; revise the quotation")
	}
	for _, ch := range in.Channels {
		if ch != "email" && ch != "whatsapp" {
			return QuotationDetail{}, enumErr("channels", []string{"email", "whatsapp"})
		}
	}
	var token *string
	if err := tx.QueryRow(ctx, `SELECT public_token FROM crm.sales_quotations WHERE id = $1`, qid).Scan(&token); err != nil {
		return QuotationDetail{}, err
	}
	tok := ""
	if token != nil {
		tok = *token
	} else {
		tok = secret.RandomToken(24)
	}
	loc := location(ctx, tx, property)
	expires := time.Date(valid.Year(), valid.Month(), valid.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	// recipient
	name, email, phone := "", "", ""
	switch {
	case q.CustomerID != nil:
		if name, email, phone, err = crm.Contact(ctx, tx, q.CustomerID, nil); err != nil {
			return QuotationDetail{}, err
		}
	case q.LeadID != nil:
		l, err := GetLead(ctx, tx, *q.LeadID)
		if err != nil {
			return QuotationDetail{}, err
		}
		name, email, phone = l.Name, deref(l.Email), deref(l.Phone)
	}
	if strings.TrimSpace(in.Email) != "" {
		email = strings.TrimSpace(in.Email)
	}
	if strings.TrimSpace(in.Phone) != "" {
		phone = crm.NormalizePhone(in.Phone)
	}
	channels := in.Channels
	if len(channels) == 0 {
		if email != "" {
			channels = append(channels, "email")
		}
		if phone != "" {
			channels = append(channels, "whatsapp")
		}
	}
	link := m.publicLink(tok)
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET status = 'sent', public_token = $2, token_expires_at = $3, sent_at = now(),
		sent_via = $4, updated_by = $5 WHERE id = $1`, qid, tok, expires, channels, actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	if m.Notify != nil && (email != "" || phone != "") && len(channels) > 0 {
		msg := notify.Message{Event: "crm.sales_quotation", Category: "crm", Name: name, Mandatory: true, PropertyID: &property,
			Data: map[string]any{"name": name, "number": q.Number, "version": q.Version, "title": q.Title, "total": formatAmount(dec(q.Total), q.Currency),
				"currency": q.Currency, "validUntil": q.ValidUntil, "link": link, "message": in.Message}}
		for _, ch := range channels {
			if ch == "email" && email != "" {
				msg.Email = email
				msg.Channels = append(msg.Channels, notify.ChannelEmail)
			}
			if ch == "whatsapp" && phone != "" {
				msg.Phone = phone
				msg.Channels = append(msg.Channels, notify.ChannelWhatsApp)
			}
		}
		if len(msg.Channels) > 0 {
			if err := m.Notify.Send(ctx, tx, msg); err != nil {
				return QuotationDetail{}, err
			}
		}
	}
	if q.CustomerID != nil {
		ch := "email"
		if len(channels) > 0 && channels[0] == "whatsapp" {
			ch = "whatsapp"
		}
		if _, err := crm.LogInteraction(ctx, tx, property, *q.CustomerID, crm.InteractionInput{Channel: ch, Direction: "outbound",
			Subject: "Quotation " + q.Number + " v" + itoa(q.Version) + " sent", Body: q.Title}, "notification", "crm.quotation", &qid); err != nil {
			return QuotationDetail{}, err
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "send", EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, Before: map[string]any{"status": q.Status},
		After: map[string]any{"status": "sent", "channels": channels}}); err != nil {
		return QuotationDetail{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventQuotationSent, "crm.quotation", &qid, &property, map[string]any{"quotationId": qid,
		"number": q.Number, "version": q.Version, "customerId": q.CustomerID, "corporateAccountId": q.CorporateAccountID,
		"opportunityId": q.OpportunityID, "line": q.Line, "title": q.Title, "eventType": q.EventType, "eventDate": q.EventDate, "endDate": q.EndDate,
		"pax": q.Pax, "venueResourceId": q.VenueResourceID, "optionDate": q.OptionDate, "packageRef": q.PackageRef, "total": q.Total,
		"currency": q.Currency, "validUntil": q.ValidUntil, "channels": channels}); err != nil {
		return QuotationDetail{}, err
	}
	return m.QuotationDetailOf(ctx, tx, qid)
}

// formatAmount renders 1.234.567 (Indonesian grouping).
func formatAmount(d decimal.Decimal, cur string) string {
	s := d.StringFixed(places(cur))
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// ── revise (FR-QUO-03) ────────────────────────────────────────────────────

// ReviseQuotation creates the next version as a draft; the old version
// becomes revised (its link can no longer be accepted).
func (m *Module) ReviseQuotation(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID) (QuotationDetail, error) {
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	if q.Status != "sent" && q.Status != "rejected" && q.Status != "expired" {
		return QuotationDetail{}, conflict("quotation_not_revisable", "a "+q.Status+" quotation cannot be revised")
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return QuotationDetail{}, err
	}
	today := localToday(ctx, tx, property)
	valid, _ := time.Parse("2006-01-02", q.ValidUntil)
	if valid.Before(today) {
		valid = today.AddDate(0, 0, max(pol.QuotationValidityDays, 1))
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET status = 'revised', revised_at = now(), updated_by = $2 WHERE id = $1`,
		qid, actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	nid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_quotations (id, property_id, number, version, opportunity_id, lead_id, customer_id,
		corporate_account_id, owner_user_id, line, event_type, event_date, end_date, pax, venue_resource_id, package_ref, title, currency, pricing_mode,
		tax_codes, header_discount, header_discount_percent, subtotal, discount, discount_percent, net_amount, service_amount, tax_amount, total,
		valid_until, option_date, status, approval_status, approved_discount_percent, payment_terms, terms, notes, policy_version, revised_from_id,
		created_by, updated_by)
		SELECT $2, property_id, number, version + 1, opportunity_id, lead_id, customer_id, corporate_account_id, owner_user_id, line, event_type,
		event_date, end_date, pax, venue_resource_id, package_ref, title, currency, pricing_mode, tax_codes, header_discount, header_discount_percent,
		subtotal, discount, discount_percent, net_amount, service_amount, tax_amount, total, $3, option_date, 'draft',
		CASE WHEN approval_status = 'pending' THEN 'required' ELSE approval_status END, approved_discount_percent, payment_terms, terms, notes,
		policy_version, id, $4, $4 FROM crm.sales_quotations WHERE id = $1`, qid, nid, valid, actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_quotation_lines (id, property_id, quotation_id, line_no, item_type, item_ref, service_type,
		description, quantity, unit_price, discount, header_discount_share, total, net_amount, service_amount, tax_amount, gross_total, price_source, pricing)
		SELECT gen_random_uuid(), property_id, $2, line_no, item_type, item_ref, service_type, description, quantity, unit_price, discount,
		header_discount_share, total, net_amount, service_amount, tax_amount, gross_total, price_source, pricing
		FROM crm.sales_quotation_lines WHERE quotation_id = $1`, qid, nid); err != nil {
		return QuotationDetail{}, err
	}
	d, err := m.QuotationDetailOf(ctx, tx, nid)
	if err != nil {
		return d, err
	}
	return d, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "revise", EntityType: "crm.quotation", EntityID: nid.String(),
		EntityLabel: q.Number + " v" + itoa(d.Version), PropertyID: &property,
		Before: map[string]any{"id": qid, "version": q.Version, "status": q.Status}, After: map[string]any{"id": nid, "version": d.Version, "status": "draft"}})
}

// ── accept / reject (FR-QUO-05..07) ───────────────────────────────────────

// AcceptInput is the staff acceptance (signed copy, verbal confirmation).
type AcceptInput struct {
	AcceptedByName string `json:"acceptedByName,omitempty" doc:"Signatory (default: the customer)"`
	Note           string `json:"note,omitempty"`
}

// RejectInput rejects a quotation.
type RejectInput struct {
	Reason string `json:"reason"`
}

type decisionMeta struct {
	via, name, ip, userAgent, note string
	termsAccepted                  bool
	otp                            *acceptanceOtp // verified one-time code (public link)
}

// AcceptQuotation accepts a quotation for the customer (staff).
func (m *Module) AcceptQuotation(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, in AcceptInput) (QuotationDetail, error) {
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	return m.accept(ctx, tx, property, q, decisionMeta{via: "staff", name: in.AcceptedByName, note: in.Note, termsAccepted: true})
}

func decisionConflict(q Quotation) error {
	switch q.Status {
	case "accepted":
		return conflict("quotation_already_accepted", "the quotation was already accepted")
	case "revised":
		return conflict("quotation_revised", "a newer version of this quotation exists")
	case "expired":
		return conflict("quotation_expired", "the quotation has expired")
	case "rejected":
		return conflict("quotation_rejected", "the quotation was rejected")
	}
	return conflict("quotation_not_sent", "the quotation is "+q.Status)
}

// accept converts an accepted quotation exactly once: opportunity won,
// lead converted when needed, crm.quotation_accepted published.
func (m *Module) accept(ctx context.Context, tx pgx.Tx, property uuid.UUID, q Quotation, meta decisionMeta) (QuotationDetail, error) {
	if q.Status != "sent" && (meta.via != "staff" || q.Status != "draft") {
		return QuotationDetail{}, decisionConflict(q)
	}
	if q.ApprovalStatus != "not_required" && q.ApprovalStatus != "approved" {
		return QuotationDetail{}, conflict("approval_required", "the discount needs approval first")
	}
	today := localToday(ctx, tx, property)
	valid, _ := time.Parse("2006-01-02", q.ValidUntil)
	if valid.Before(today) {
		return QuotationDetail{}, conflict("quotation_expired", "the quotation has expired")
	}
	now := clock.Now()
	customer, opp := q.CustomerID, q.OpportunityID
	if opp == nil && q.LeadID != nil {
		l, err := GetLead(ctx, tx, *q.LeadID)
		if err != nil {
			return QuotationDetail{}, err
		}
		if l.Status == "converted" && l.OpportunityID != nil {
			opp = l.OpportunityID
			if customer == nil {
				customer = l.CustomerID
			}
		} else {
			res, err := m.ConvertLead(ctx, tx, property, l.ID, ConvertInput{CustomerID: customer, ExpectedValue: q.Total, Title: q.Title})
			if err != nil {
				return QuotationDetail{}, err
			}
			customer = &res.CustomerID
			if res.Opportunity != nil {
				opp = &res.Opportunity.ID
			}
		}
	}
	if customer == nil {
		return QuotationDetail{}, conflict("customer_required", "the quotation has no customer")
	}
	name := strings.TrimSpace(meta.name)
	if name == "" {
		if n, _, _, err := crm.Contact(ctx, tx, customer, nil); err == nil {
			name = n
		}
	}
	// payment terms: due dates are final from the acceptance date
	d, err := m.QuotationDetailOf(ctx, tx, q.ID)
	if err != nil {
		return QuotationDetail{}, err
	}
	var event *time.Time
	if q.EventDate != nil {
		t, _ := time.Parse("2006-01-02", *q.EventDate)
		event = &t
	}
	terms, err := buildTerms(storedTerms(d.PaymentTerms), nil, dec(q.Total), q.Currency, today, event)
	if err != nil {
		return QuotationDetail{}, err
	}
	termsJSON, _ := json.Marshal(terms)
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_quotation_decisions (id, property_id, quotation_id, decision, via, name, ip, user_agent,
		terms_accepted, note, decided_by, decided_at) VALUES ($1,$2,$3,'accepted',$4,$5,$6,$7,$8,$9,$10,$11)`, id.New(), property, q.ID, meta.via,
		nullStr(name), nullStr(meta.ip), nullStr(meta.userAgent), meta.termsAccepted, nullStr(meta.note), actor(ctx), now); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return QuotationDetail{}, conflict("quotation_already_accepted", "the quotation was already decided")
		}
		return QuotationDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET status = 'accepted', accepted_at = $2, accepted_via = $3, accepted_by_name = $4,
		payment_terms = $5, customer_id = $6, opportunity_id = $7, updated_by = $8 WHERE id = $1`, q.ID, now, meta.via, nullStr(name), termsJSON,
		customer, opp, actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	if err := m.recordAcceptance(ctx, tx, q, meta, now); err != nil {
		return QuotationDetail{}, err
	}
	if opp != nil {
		o, err := lockOpportunity(ctx, tx, property, *opp)
		if err != nil {
			return QuotationDetail{}, err
		}
		if q.OwnerUserID == nil && o.OwnerUserID != nil {
			if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET owner_user_id = $2 WHERE id = $1`, q.ID, o.OwnerUserID); err != nil {
				return QuotationDetail{}, err
			}
		}
		if o.Status != "won" {
			accepted, err := GetQuotation(ctx, tx, q.ID)
			if err != nil {
				return QuotationDetail{}, err
			}
			if err := m.markWon(ctx, tx, property, o, accepted, "quotation accepted ("+meta.via+")"); err != nil {
				return QuotationDetail{}, err
			}
		}
	}
	after, err := m.QuotationDetailOf(ctx, tx, q.ID)
	if err != nil {
		return after, err
	}
	if after, err = m.eMeteraiOnAcceptance(ctx, tx, property, after); err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "accept", EntityType: "crm.quotation", EntityID: q.ID.String(),
		EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, Before: map[string]any{"status": q.Status},
		After: map[string]any{"status": "accepted", "via": meta.via, "name": name, "ip": meta.ip}, Metadata: meta.otpAudit()}); err != nil {
		return after, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventQuotationAccepted, "crm.quotation", &q.ID, &property, AcceptedPayload(after, property, now,
		meta.via)); err != nil {
		return after, err
	}
	if _, err := crm.LogInteraction(ctx, tx, property, *customer, crm.InteractionInput{Channel: "other", Direction: "inbound",
		Subject: "Quotation " + q.Number + " v" + itoa(q.Version) + " accepted", Body: q.Title}, "manual", "crm.quotation", &q.ID); err != nil {
		return after, err
	}
	if after.OwnerUserID != nil {
		if err := m.notifyUsers(ctx, tx, property, []uuid.UUID{*after.OwnerUserID}, "crm.sales_quotation_decided",
			m.staffLink("/crm/quotations/"+q.ID.String()), map[string]any{"number": q.Number, "title": q.Title, "decision": "accepted",
				"name": name, "total": formatAmount(dec(q.Total), q.Currency)}); err != nil {
			return after, err
		}
	}
	return after, nil
}

// AcceptedPayload is the crm.quotation_accepted payload (docs/p3-p4-contracts.md).
func AcceptedPayload(q QuotationDetail, property uuid.UUID, acceptedAt time.Time, via string) map[string]any {
	lines := []map[string]any{}
	for _, l := range q.Lines {
		lines = append(lines, map[string]any{"itemType": l.ItemType, "itemRef": l.ItemRef, "description": l.Description, "quantity": l.Quantity,
			"unitPrice": l.UnitPrice, "discount": l.Discount, "total": l.Total})
	}
	terms := []map[string]any{}
	for _, t := range q.PaymentTerms {
		terms = append(terms, map[string]any{"label": t.Label, "percent": t.Percent, "amount": t.Amount, "dueDate": t.DueDate})
	}
	return map[string]any{"quotationId": q.ID, "number": q.Number, "version": q.Version, "propertyId": property, "opportunityId": q.OpportunityID,
		"leadId": q.LeadID, "customerId": q.CustomerID, "corporateAccountId": q.CorporateAccountID, "line": q.Line, "eventType": q.EventType,
		"eventDate": q.EventDate, "endDate": q.EndDate, "pax": q.Pax, "venueResourceId": q.VenueResourceID, "packageRef": q.PackageRef,
		"title": q.Title, "currency": q.Currency, "subtotal": q.Subtotal, "discount": q.Discount, "service": q.ServiceAmount, "tax": q.TaxAmount,
		"total": q.Total, "lines": lines, "paymentTerms": terms, "acceptedAt": acceptedAt.UTC().Format(time.RFC3339), "acceptedVia": via}
}

// RejectQuotation records a rejection by staff.
func (m *Module) RejectQuotation(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, in RejectInput) (QuotationDetail, error) {
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return QuotationDetail{}, err
	}
	return m.reject(ctx, tx, property, q, decisionMeta{via: "staff", note: in.Reason})
}

func (m *Module) reject(ctx context.Context, tx pgx.Tx, property uuid.UUID, q Quotation, meta decisionMeta) (QuotationDetail, error) {
	if q.Status != "sent" {
		return QuotationDetail{}, decisionConflict(q)
	}
	now := clock.Now()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_quotation_decisions (id, property_id, quotation_id, decision, via, name, ip, user_agent, note,
		decided_by, decided_at) VALUES ($1,$2,$3,'rejected',$4,$5,$6,$7,$8,$9,$10)`, id.New(), property, q.ID, meta.via, nullStr(meta.name),
		nullStr(meta.ip), nullStr(meta.userAgent), nullStr(meta.note), actor(ctx), now); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return QuotationDetail{}, conflict("quotation_already_decided", "the quotation was already decided")
		}
		return QuotationDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET status = 'rejected', rejected_at = $2, reject_reason = $3, updated_by = $4 WHERE id = $1`,
		q.ID, now, nullStr(meta.note), actor(ctx)); err != nil {
		return QuotationDetail{}, err
	}
	after, err := m.QuotationDetailOf(ctx, tx, q.ID)
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "reject", EntityType: "crm.quotation", EntityID: q.ID.String(),
		EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, Reason: meta.note, Before: map[string]any{"status": q.Status},
		After: map[string]any{"status": "rejected", "via": meta.via}}); err != nil {
		return after, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventQuotationRejected, "crm.quotation", &q.ID, &property, map[string]any{"quotationId": q.ID,
		"number": q.Number, "version": q.Version, "customerId": q.CustomerID, "opportunityId": q.OpportunityID, "reason": meta.note}); err != nil {
		return after, err
	}
	if q.OwnerUserID != nil {
		if err := m.notifyUsers(ctx, tx, property, []uuid.UUID{*q.OwnerUserID}, "crm.sales_quotation_decided",
			m.staffLink("/crm/quotations/"+q.ID.String()), map[string]any{"number": q.Number, "title": q.Title, "decision": "rejected",
				"name": meta.name, "total": formatAmount(dec(q.Total), q.Currency)}); err != nil {
			return after, err
		}
	}
	return after, nil
}

// ExpireQuotations marks sent quotations past their validity expired and
// publishes crm.quotation_expired (daily job); returns the count.
func (m *Module) ExpireQuotations(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	today := localToday(ctx, tx, property)
	rows, err := tx.Query(ctx, `SELECT id FROM crm.sales_quotations WHERE property_id = $1 AND status = 'sent' AND valid_until < $2 FOR UPDATE`,
		property, today)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	for _, qid := range ids {
		q, err := GetQuotation(ctx, tx, qid)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET status = 'expired', expired_at = now() WHERE id = $1`, qid); err != nil {
			return 0, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "expire", EntityType: "crm.quotation", EntityID: qid.String(),
			EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, ActorName: "system",
			Before: map[string]any{"status": q.Status}, After: map[string]any{"status": "expired", "validUntil": q.ValidUntil}}); err != nil {
			return 0, err
		}
		if _, err := m.Events.Publish(ctx, tx, EventQuotationExpired, "crm.quotation", &qid, &property, map[string]any{"quotationId": qid,
			"number": q.Number, "version": q.Version, "customerId": q.CustomerID, "opportunityId": q.OpportunityID}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
