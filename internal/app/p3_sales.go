package app

// PRD P3 EP-01–04 CRM Sales: leads, pipelines, opportunities, quotations,
// sales targets & commission (owner: crm/sales). CRM sits below Commercial
// and Billing (Technical Doc §4.2), so the composition root plugs in the
// quotation pricing, the website contact form, the billing events of the
// commission engine and the commission rows of the accounting export.

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/crm/sales"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p3Sales holds the services of the area.
type p3Sales struct {
	Module *sales.Module
	// converted are the business lines whose accepted quotations another
	// module converts with its own billing (line → module).
	converted map[string]string
}

// convertsQuotations records that a module converts the accepted quotations
// of these lines (FR-QUO-06); other lines get a quotation payment schedule.
func (a *App) convertsQuotations(module string, lines ...string) {
	if a.Sales.converted == nil {
		a.Sales.converted = map[string]string{}
	}
	for _, l := range lines {
		a.Sales.converted[l] = module
	}
}

// p3SalesContributions are the catalogue contributions (permissions, role mappings).
func p3SalesContributions() []catalog.Contribution {
	return []catalog.Contribution{sales.Contribution()}
}

// p3SalesDocumentTypes are the approval document types.
func p3SalesDocumentTypes() []provision.DocumentType { return sales.DocumentTypes() }

// p3SalesTemplates are the notification templates.
func p3SalesTemplates() []provision.Template { return sales.Templates() }

// buildP3Sales wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Sales(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &sales.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, Price: quotePrice,
		WebsiteURL: func() string { return cfg.WebsiteURL }, StaffURL: func() string { return cfg.PublicBaseURL }}
	m.Register(reg, a.Engine)
	a.Approvals.RegisterDocumentType(sales.DiscountDocumentType, m.DiscountDecision)
	a.Approvals.RegisterDocumentType(sales.StatementDocumentType, m.StatementDecision)
	m.RegisterJobs(a.Registrar, a.Instance.Location)
	crm.SetSalesContactHook(m.ContactHook)
	a.Billing.RegisterExportSection("crm.sales_commission", func(ctx context.Context, q dbtx.Querier, property uuid.UUID, _, from, to time.Time) ([]billing.ExportRow, error) {
		rows, err := sales.CommissionExport(ctx, q, property, from, to)
		out := make([]billing.ExportRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, billing.ExportRow{Section: r.Section, Code: r.Code, Description: r.Description, Amount: r.Amount})
		}
		return out, err
	})
	a.Sales.Module = m
	a.convertsQuotations("membership", "membership")
}

// subscribeP3Sales registers the event subscribers: commission follows the
// money of accepted quotations; WhatsApp messages from unknown numbers
// become leads (FR-INT-P3-01).
func (a *App) subscribeP3Sales() {
	m := a.Sales.Module
	a.Bus.Subscribe(sales.BillingInvoicePaid, "crm.sales_commission_invoice_paid", m.OnInvoicePaid)
	a.Bus.Subscribe(sales.BillingPaymentSettled, "crm.sales_commission_payment", m.OnPaymentSettled)
	a.Bus.Subscribe(sales.BillingRefund, "crm.sales_commission_refund", m.OnRefund)
	a.Bus.Subscribe(sales.BillingInvoiceVoided, "crm.sales_commission_invoice_voided", m.OnInvoiceVoided)
	a.Bus.Subscribe(sales.WebhookReceived, "crm.sales_whatsapp_lead", m.OnWebhook)
	a.Bus.Subscribe(sales.MembershipActivated, "crm.sales_membership_deal", m.OnMembershipActivated)
	a.Bus.Subscribe(sales.EventQuotationAccepted, "membership.quotation_application", a.Membership.OnQuotationAccepted)
	a.Bus.Subscribe(sales.EventQuotationAccepted, "billing.quotation_schedule", a.quotationSchedule)
}

// acceptedQuotation is the part of crm.quotation_accepted the payment
// schedule needs (docs/p3-p4-contracts.md).
type acceptedQuotation struct {
	QuotationID        uuid.UUID  `json:"quotationId"`
	Number             string     `json:"number"`
	PropertyID         uuid.UUID  `json:"propertyId"`
	Line               string     `json:"line"`
	CustomerID         *uuid.UUID `json:"customerId"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	Title              string     `json:"title"`
	Total              string     `json:"total"`
	PaymentTerms       []struct {
		Label   string `json:"label"`
		Percent string `json:"percent"`
		Amount  string `json:"amount"`
		DueDate string `json:"dueDate"`
	} `json:"paymentTerms"`
}

// quotationSchedule issues the payment schedule of an accepted quotation
// whose line no module converts (golf, stay, other): a deposit folio of the
// customer (on the company's customer folio) and the payment terms of the
// quotation — the DP first (FR-QUO-06); once per quotation (FR-QUO-07).
func (a *App) quotationSchedule(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p acceptedQuotation
	if err := e.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	total, err := decimal.NewFromString(p.Total)
	if err != nil || !total.IsPositive() || p.CustomerID == nil || a.Sales.converted[p.Line] != "" {
		return nil //nolint:nilerr // nothing to bill
	}
	property := p.PropertyID
	if property == uuid.Nil && e.PropertyID != nil {
		property = *e.PropertyID
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	if sc, err := billing.ScheduleBySource(ctx, tx, "quotation", p.QuotationID); err != nil || sc != nil {
		return err
	}
	holder, _, _, err := crm.Contact(ctx, tx, p.CustomerID, nil)
	if err != nil {
		return err
	}
	corporate := ""
	if p.CorporateAccountID != nil {
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.corporate_accounts WHERE id = $1`, *p.CorporateAccountID).Scan(&corporate); err != nil {
			return err
		}
	}
	line := billing.LineOther
	if slices.Contains(billing.BusinessLines, p.Line) {
		line = p.Line
	}
	f, err := a.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: p.CustomerID,
		HolderName: holder, SourceType: "quotation", SourceID: &p.QuotationID, SourceRef: p.Number}, BusinessLine: line, CorporateName: corporate})
	if err != nil {
		return err
	}
	payer := billing.CustomerFolioInput{CustomerID: p.CustomerID}
	if p.CorporateAccountID != nil {
		payer = billing.CustomerFolioInput{CorporateAccountID: p.CorporateAccountID}
	}
	if _, err := a.Billing.AttachFolio(ctx, tx, property, f.ID, payer); err != nil {
		return err
	}
	var lines []billing.ScheduleLineInput
	acc := decimal.Zero
	for i, t := range p.PaymentTerms {
		amt, _ := decimal.NewFromString(t.Amount)
		if i == len(p.PaymentTerms)-1 {
			amt = total.Sub(acc)
		}
		if !amt.IsPositive() {
			continue
		}
		acc = acc.Add(amt)
		lines = append(lines, billing.ScheduleLineInput{Label: t.Label, Amount: amt.String(), DueDate: t.DueDate})
	}
	if len(lines) == 0 {
		lines = []billing.ScheduleLineInput{{Label: "Full Payment", Kind: "final", Amount: total.String(),
			DueDate: clock.Now().In(a.Instance.Location()).Format("2006-01-02")}}
	}
	_, err = a.Billing.CreateSchedule(ctx, tx, property, billing.ScheduleInput{Title: p.Number + " · " + p.Title, FolioID: &f.ID,
		CustomerID: p.CustomerID, CorporateAccountID: p.CorporateAccountID, SourceType: "quotation", SourceID: &p.QuotationID, SourceRef: p.Number,
		TotalAmount: total.String(), Lines: lines})
	return err
}

// demoP3Sales seeds the default pipelines of the demo property.
func demoP3Sales(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	_, err := sales.SeedPipelines(ctx, tx, property)
	return err
}

// quotePrice prices a quotation line: a manual price, the Commercial pricing
// rule of the line's service type or the product price; tax & service from
// the rules in force on the service date.
func quotePrice(ctx context.Context, q dbtx.Querier, property uuid.UUID, in sales.PriceInput) (sales.PricedLine, error) {
	out := sales.PricedLine{Source: "manual", PricingMode: in.PricingMode, TaxCodes: in.TaxCodes, Snapshot: map[string]any{}}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	var unit decimal.Decimal
	switch {
	case in.UnitPrice != nil:
		unit = *in.UnitPrice
	case in.ServiceType != "":
		lp, err := commercial.Pricer{}.Resolve(ctx, q, property, commercial.PriceRequest{ServiceType: in.ServiceType, ItemRef: in.ItemRef, Start: at,
			Quantity: 1})
		if err != nil {
			return out, err
		}
		unit, _ = decimal.NewFromString(lp.UnitPrice)
		out.Source, out.PricingMode = "pricing_rule", lp.Tax.PricingMode
		out.TaxCodes = make([]string, 0, len(lp.Tax.Lines))
		for _, l := range lp.Tax.Lines {
			out.TaxCodes = append(out.TaxCodes, l.Code)
		}
		out.Snapshot = map[string]any{"ruleCode": lp.RuleCode, "ruleVersion": lp.RuleVersion, "serviceType": in.ServiceType}
	case in.ItemType == "product" && in.ItemRef != "":
		var price string
		if err := q.QueryRow(ctx, `SELECT price::text FROM commercial.products WHERE property_id = $1 AND (id::text = $2 OR code = $2) LIMIT 1`,
			property, in.ItemRef).Scan(&price); err != nil {
			return out, handle.Invalid("lines.itemRef", "not_found", "product not found")
		}
		unit, _ = decimal.NewFromString(price)
		out.Source = "product"
		out.Snapshot = map[string]any{"product": in.ItemRef}
	default:
		return out, handle.Invalid("lines.unitPrice", "required", "enter the unit price or a service type to price from")
	}
	out.UnitPrice = unit
	base := unit.Mul(in.Quantity).Sub(in.Discount)
	rules, err := commercial.RulesAt(ctx, q, property, at)
	if err != nil {
		return out, err
	}
	var applied []commercial.Rule
	for _, r := range rules {
		if slices.Contains(out.TaxCodes, r.Code) {
			applied = append(applied, r)
		}
	}
	mode := out.PricingMode
	if mode == "" {
		mode = "nett"
	}
	b := commercial.CalculateMode(applied, base, in.Currency, at, mode)
	out.PricingMode = mode
	out.Net, _ = decimal.NewFromString(b.NetAmount)
	out.Total, _ = decimal.NewFromString(b.Total)
	out.Service, out.Tax = commercial.SumKind(b.Lines, "service"), commercial.SumKind(b.Lines, "tax")
	out.Snapshot["taxLines"] = b.Lines
	return out, nil
}
