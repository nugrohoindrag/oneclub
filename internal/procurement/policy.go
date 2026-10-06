package procurement

// Procurement Policies and Procurement Configuration (PRD P4 FR-POL-P4-02,
// labels proposed in PRD P4 §7.6 / NC §33) on the P0 club policy framework
// (versioned, effective dated, per property). Defaults follow PRD P4 §16
// #10: approval tiers ≤ Rp5 jt department head, ≤ Rp25 jt + Finance
// Manager, ≤ Rp100 jt + General Manager, above + Owner; RFQ with at least
// three suppliers above Rp10 jt unless the supplier is a contract supplier.

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// Policy codes and their NC §33 categories.
const (
	PolicyCode        = "procurement.policy"
	ConfigurationCode = "procurement.configuration"

	PolicyCategory        = "Procurement Policies"
	ConfigurationCategory = "Procurement Configuration"
)

// ProcurementApprovalTier maps a document amount to an approval level; approval
// workflows (Settings → Approval Workflows) add steps on the
// `approvalLevel` (or `amount`) attribute of the procurement documents.
type ProcurementApprovalTier struct {
	UpTo  string `json:"upTo" doc:"Upper bound of the tier (inclusive); empty = no upper bound"`
	Level int    `json:"level"`
	Label string `json:"label"`
}

// ProcurementPolicy is the Procurement Policies document.
type ProcurementPolicy struct {
	ApprovalTiers               []ProcurementApprovalTier `json:"approvalTiers" doc:"PR / PO approval matrix by amount"`
	RFQRequiredAbove            string                    `json:"rfqRequiredAbove" doc:"Purchases above this amount need an RFQ (contract suppliers excepted)"`
	MinQuotations               int                       `json:"minQuotations" doc:"Suppliers invited / quotations compared above the RFQ threshold"`
	PriceTolerancePercent       string                    `json:"priceTolerancePercent" doc:"3-way matching: invoice price above the PO price"`
	QuantityTolerancePercent    string                    `json:"quantityTolerancePercent" doc:"3-way matching: invoiced quantity above the received quantity"`
	OverReceiptTolerancePercent string                    `json:"overReceiptTolerancePercent" doc:"Goods receipt above the ordered quantity"`
	OverReceiptAction           string                    `json:"overReceiptAction" enum:"reject,approval" doc:"Receipt beyond the tolerance: rejected or sent for approval"`
	ReceiptWithoutPOCategories  []string                  `json:"receiptWithoutPOCategories" doc:"Item categories / types that may be received without a PO (with approval)"`
	AutoRequisitionMode         string                    `json:"autoRequisitionMode" enum:"draft,off" doc:"Reorder-based requisitions: draft for review, or off"`
	HoldOnMismatch              bool                      `json:"holdOnMismatch" doc:"A mismatched vendor invoice is put On Hold automatically"`
	SubmitMatchedInvoices       bool                      `json:"submitMatchedInvoices" doc:"A matched vendor invoice is submitted for approval automatically"`
}

// DefaultPolicy follows PRD P4 §16 #10.
var DefaultPolicy = ProcurementPolicy{
	ApprovalTiers: []ProcurementApprovalTier{{UpTo: "5000000", Level: 1, Label: "Department Head"}, {UpTo: "25000000", Level: 2, Label: "Finance Manager"},
		{UpTo: "100000000", Level: 3, Label: "General Manager"}, {UpTo: "", Level: 4, Label: "Owner / Board"}},
	RFQRequiredAbove: "10000000", MinQuotations: 3, PriceTolerancePercent: "1", QuantityTolerancePercent: "0", OverReceiptTolerancePercent: "0",
	OverReceiptAction: "approval", ReceiptWithoutPOCategories: []string{}, AutoRequisitionMode: "draft", HoldOnMismatch: true, SubmitMatchedInvoices: true,
}

// VendorScoreWeights weigh the vendor scorecard (sum 100).
type VendorScoreWeights struct {
	OnTime   int `json:"onTime"`
	Quality  int `json:"quality"`
	Fill     int `json:"fill"`
	Price    int `json:"price"`
	Response int `json:"response"`
}

// ProcurementConfiguration is the Procurement Configuration document.
type ProcurementConfiguration struct {
	DefaultCurrency        string             `json:"defaultCurrency"`
	DefaultTaxPercent      string             `json:"defaultTaxPercent" doc:"PPN on purchases of PKP suppliers"`
	DefaultTaxCode         string             `json:"defaultTaxCode"`
	WithholdingRates       map[string]string  `json:"withholdingRates" doc:"PPh withholding rates: pph23 (services), pph4_2 (rent)"`
	DefaultPaymentTermDays int                `json:"defaultPaymentTermDays"`
	RFQResponseDays        int                `json:"rfqResponseDays" doc:"Default RFQ response deadline"`
	SupplierLinkDays       int                `json:"supplierLinkDays" doc:"Validity of the supplier links (RFQ response, PO PDF)"`
	PurchaseOrderTerms     string             `json:"purchaseOrderTerms" doc:"Terms printed on the purchase order"`
	ScoreWeights           VendorScoreWeights `json:"scoreWeights" doc:"Vendor scorecard weights"`
}

// DefaultConfiguration: PPN 11%, PPh 23 2%, PPh 4(2) 10% (PRD P4 §16 #7).
var DefaultConfiguration = ProcurementConfiguration{DefaultCurrency: "IDR", DefaultTaxPercent: "11", DefaultTaxCode: "PPN",
	WithholdingRates: map[string]string{"pph23": "2", "pph4_2": "10"}, DefaultPaymentTermDays: 30, RFQResponseDays: 3, SupplierLinkDays: 30,
	PurchaseOrderTerms: "Please quote the PO number on the delivery note and invoice. / Cantumkan nomor PO pada surat jalan dan faktur.",
	ScoreWeights:       VendorScoreWeights{OnTime: 35, Quality: 30, Fill: 15, Price: 10, Response: 10}}

func init() {
	// The categories are proposed by PRD P4 §7.6 (Settings → Club Policies).
	for _, c := range []string{PolicyCategory, ConfigurationCategory} {
		if !slices.Contains(rules.PolicyCategories, c) {
			rules.PolicyCategories = append(rules.PolicyCategories, c)
		}
	}
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: PolicyCategory, Name: "Procurement approval, RFQ & matching",
		Description: "PR / PO approval tiers, RFQ threshold and minimum quotations, 3-way matching and over-receipt tolerances, " +
			"receipt without PO, automatic requisitions and vendor invoice holds", Default: DefaultPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: ConfigurationCode, Category: ConfigurationCategory, Name: "Procurement defaults",
		Description: "Currency, PPN, PPh withholding rates, payment term, RFQ response time, supplier links, PO terms and scorecard weights",
		Default:     DefaultConfiguration})
}

// LoadPolicy returns the Procurement Policies in force at a property.
func LoadPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ProcurementPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, PolicyCode, property, DefaultPolicy)
	return p, err
}

// LoadConfiguration returns the Procurement Configuration in force.
func LoadConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ProcurementConfiguration, error) {
	c, _, err := rules.PolicyAt(ctx, q, ConfigurationCode, property, DefaultConfiguration)
	if c.WithholdingRates == nil {
		c.WithholdingRates = DefaultConfiguration.WithholdingRates
	}
	return c, err
}

// ApprovalLevel returns the tier of an amount (1 when no tier is set).
func (p ProcurementPolicy) ApprovalLevel(amount decimal.Decimal) (int, string) {
	for _, t := range p.ApprovalTiers {
		if t.UpTo == "" || amount.LessThanOrEqual(dec(t.UpTo)) {
			return t.Level, t.Label
		}
	}
	if n := len(p.ApprovalTiers); n > 0 {
		return p.ApprovalTiers[n-1].Level, p.ApprovalTiers[n-1].Label
	}
	return 1, ""
}

// tolerance returns 1 + pct/100.
func tolerance(pct string) decimal.Decimal {
	return decimal.NewFromInt(1).Add(dec(pct).Div(decimal.NewFromInt(100)))
}
