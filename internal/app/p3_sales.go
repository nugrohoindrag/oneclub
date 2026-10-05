package app

// PRD P3 EP-01–04 CRM Sales: leads, pipelines, opportunities, quotations,
// sales targets & commission (owner: crm/sales). CRM sits below Commercial
// and Billing (Technical Doc §4.2), so the composition root plugs in the
// quotation pricing, the website contact form, the billing events of the
// commission engine and the commission rows of the accounting export.

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/banquet"
	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/crm/sales"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/rules"
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
		WebsiteURL: func() string { return cfg.WebsiteURL }, StaffURL: func() string { return cfg.PublicBaseURL },
		Integrations: a.Integrations, OTPKey: []byte(cfg.AppSecret), Logo: brandingLogo(files), BanquetTerms: banquetQuotationTerms}
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
	// The pipeline follows the banquet event status (FR-BQT-14).
	a.Bus.Subscribe(sales.BanquetEventConfirmed, "crm.sales_banquet_event_confirmed", m.OnBanquetEvent)
	a.Bus.Subscribe(sales.BanquetEventCancelled, "crm.sales_banquet_event_cancelled", m.OnBanquetEvent)
	a.Bus.Subscribe(sales.EventQuotationAccepted, "membership.quotation_application", a.Membership.OnQuotationAccepted)
	a.Bus.Subscribe(sales.EventQuotationAccepted, "billing.quotation_schedule", a.quotationSchedule)
	a.subscribeP3Money()
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

// demoP3Sales seeds the default pipelines of the demo property and enables
// the sandbox e-Meterai for the trial (PRD P3 §16 #18; mock until a PERURI
// distributor is contracted).
func demoP3Sales(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	if _, err := sales.SeedPipelines(ctx, tx, property); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.integrations (id, code, adapter, capability, name, enabled, mode)
		VALUES ($1, 'mock-emeterai', 'mock-emeterai', $2, 'Mock e-Meterai (Sandbox)', true, 'sandbox') ON CONFLICT (code) DO NOTHING`,
		id.New(), integration.CapEMeterai)
	return err
}

// quotePrice prices a quotation line (FR-QUO-01): a manual price, the
// Commercial pricing rule of the line's service type, the product price,
// the published Commercial package or the banquet package / menu per pax;
// catalogue prices get the active promotions of the back office channel
// when the Sales Policies allow it (FR-PRM-06), stored in the line snapshot
// (FR-PRM-07); tax & service from the rules in force on the service date.
func quotePrice(ctx context.Context, q dbtx.Querier, property uuid.UUID, in sales.PriceInput) (sales.PricedLine, error) {
	out := sales.PricedLine{Source: "manual", PricingMode: in.PricingMode, TaxCodes: in.TaxCodes, Snapshot: map[string]any{}}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	qty := in.Quantity
	promo := commercial.PromoLine{Key: "1", ServiceType: in.ServiceType, ItemRef: in.ItemRef}
	var unit decimal.Decimal
	switch {
	case in.UnitPrice != nil:
		unit = *in.UnitPrice
	case in.ServiceType != "":
		lp, err := commercial.Pricer{}.Resolve(ctx, q, property, commercial.PriceRequest{ServiceType: in.ServiceType, ItemRef: in.ItemRef, Start: at,
			Quantity: 1, NoPromotions: true})
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
		var pid uuid.UUID
		var category *string
		if err := q.QueryRow(ctx, `SELECT id, price::text, category FROM commercial.products WHERE property_id = $1 AND (id::text = $2 OR code = $2) LIMIT 1`,
			property, in.ItemRef).Scan(&pid, &price, &category); err != nil {
			return out, handle.Invalid("lines.itemRef", "not_found", "product not found")
		}
		unit, _ = decimal.NewFromString(price)
		out.Source = "product"
		out.Snapshot = map[string]any{"product": in.ItemRef}
		promo.ProductID = &pid
		if category != nil {
			promo.Category = *category
		}
	case in.ItemType == "package" && in.ItemRef != "":
		var err error
		if unit, qty, err = quotePackage(ctx, q, property, in.ItemRef, qty, &out); err != nil {
			return out, err
		}
		promo.ServiceType, promo.ItemRef = "package", strings.ToUpper(strings.TrimSpace(in.ItemRef))
	case (in.ItemType == "banquet_package" || in.ItemType == "banquet_menu") && in.ItemRef != "":
		var err error
		if unit, qty, err = quoteBanquet(ctx, q, property, in.ItemType, in.ItemRef, qty, &out); err != nil {
			return out, err
		}
	default:
		return out, handle.Invalid("lines.unitPrice", "required",
			"enter the unit price, a service type or a catalogue item (package, banquet package / menu, product) to price from")
	}
	out.UnitPrice = unit
	if !qty.Equal(in.Quantity) {
		out.Quantity = &qty
	}
	// promotions on catalogue prices (never on manual / negotiated prices)
	if in.UnitPrice == nil && in.Promotions && unit.IsPositive() && qty.IsPositive() {
		promo.Quantity, promo.UnitPrice = qty, unit
		res, err := commercial.EvaluatePromotions(ctx, q, commercial.PromoContext{Property: property, At: at, Channel: "back_office",
			BusinessLine: quotePromoLine(in), CustomerID: in.CustomerID, Currency: in.Currency, Lines: []commercial.PromoLine{promo}})
		if err != nil {
			return out, err
		}
		if d := res.TotalDiscount(); d.IsPositive() {
			out.PromoDiscount = d
			out.Snapshot["promotions"] = res.AppliedTo("1")
		}
	}
	base := unit.Mul(qty).Sub(in.Discount).Sub(out.PromoDiscount)
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

// quotePackage prices a published Commercial package sold through the
// quotation channel (FR-QUO-01 / EP-11): per pax with the minimum pax, a
// fixed price for the party, or per night (× the package nights).
func quotePackage(ctx context.Context, q dbtx.Querier, property uuid.UUID, ref string, qty decimal.Decimal,
	out *sales.PricedLine) (decimal.Decimal, decimal.Decimal, error) {
	var pid uuid.UUID
	if err := q.QueryRow(ctx, `SELECT id FROM commercial.packages WHERE property_id = $1 AND (id::text = $2 OR code = upper($2)) AND archived_at IS NULL`,
		property, strings.TrimSpace(ref)).Scan(&pid); err != nil {
		if dbtx.IsNoRows(err) {
			return qty, qty, handle.Invalid("lines.itemRef", "not_found", "package not found")
		}
		return qty, qty, err
	}
	spec, err := commercial.PublishedSpec(ctx, q, pid)
	if err != nil {
		return qty, qty, err
	}
	if spec.Status != "active" {
		return qty, qty, handle.Invalid("lines.itemRef", "inactive", "the package is not active")
	}
	if len(spec.Channels) > 0 && !slices.Contains(spec.Channels, "quotation") {
		return qty, qty, handle.Invalid("lines.itemRef", "channel", "the package is not sold through quotations")
	}
	price, _ := decimal.NewFromString(spec.Price)
	nights := decimal.NewFromInt(int64(max(spec.Nights, 1)))
	minPax := decimal.NewFromInt(int64(max(spec.MinPax, 1)))
	unit := price
	switch spec.PricingMode {
	case "per_pax":
		qty = decimal.Max(qty, minPax)
	case "per_night":
		unit, qty = price.Mul(nights), decimal.NewFromInt(1)
	case "per_pax_per_night":
		unit, qty = price.Mul(nights), decimal.Max(qty, minPax)
	default: // fixed: one package for the party
		qty = decimal.NewFromInt(1)
	}
	out.Source, out.PricingMode, out.TaxCodes = "package", spec.TaxMode, append([]string{}, spec.TaxCodes...)
	out.Snapshot = map[string]any{"package": spec.Code, "packageVersion": spec.Version, "packagePricing": spec.PricingMode, "minPax": spec.MinPax}
	return unit, qty, nil
}

// quoteBanquet prices a banquet package (per pax with the minimum pax, per
// pax per day, or a fixed price with the pax it includes plus additional
// pax) or a banquet menu per pax, as Banquet prices the event (EP-13).
func quoteBanquet(ctx context.Context, q dbtx.Querier, property uuid.UUID, itemType, ref string, qty decimal.Decimal,
	out *sales.PricedLine) (decimal.Decimal, decimal.Decimal, error) {
	ref = strings.TrimSpace(ref)
	if itemType == "banquet_menu" {
		var code, price, mode string
		var codes []string
		err := q.QueryRow(ctx, `SELECT code, price_per_pax::text, pricing_mode, tax_codes FROM banquet.menus WHERE property_id = $1
			AND (id::text = $2 OR upper(code) = upper($2)) AND status = 'active' AND archived_at IS NULL`, property, ref).Scan(&code, &price, &mode, &codes)
		if dbtx.IsNoRows(err) {
			return qty, qty, handle.Invalid("lines.itemRef", "not_found", "banquet menu not found")
		}
		if err != nil {
			return qty, qty, err
		}
		unit, _ := decimal.NewFromString(price)
		out.Source, out.PricingMode, out.TaxCodes = "banquet_menu", mode, codes
		out.Snapshot = map[string]any{"menu": code}
		return unit, qty, nil
	}
	var code, method, price, mode, extra string
	var codes []string
	var minPax, includedPax int
	err := q.QueryRow(ctx, `SELECT code, pricing_method, price::text, pricing_mode, tax_codes, min_pax, included_pax, extra_pax_price::text
		FROM banquet.packages WHERE property_id = $1 AND (id::text = $2 OR upper(code) = upper($2)) AND status = 'active' AND archived_at IS NULL`,
		property, ref).Scan(&code, &method, &price, &mode, &codes, &minPax, &includedPax, &extra)
	if dbtx.IsNoRows(err) {
		return qty, qty, handle.Invalid("lines.itemRef", "not_found", "banquet package not found")
	}
	if err != nil {
		return qty, qty, err
	}
	unit, _ := decimal.NewFromString(price)
	switch method {
	case "fixed":
		if includedPax > 0 {
			// quantity = pax: the package covers the included pax, the rest is
			// charged at the additional pax price
			if more := qty.Sub(decimal.NewFromInt(int64(includedPax))); more.IsPositive() {
				x, _ := decimal.NewFromString(extra)
				unit = unit.Add(more.Mul(x))
			}
			qty = decimal.NewFromInt(1)
		}
	case "per_pax":
		qty = decimal.Max(qty, decimal.NewFromInt(int64(minPax)))
	}
	out.Source, out.PricingMode, out.TaxCodes = "banquet_package", mode, codes
	out.Snapshot = map[string]any{"banquetPackage": code, "pricingMethod": method, "minPax": minPax, "includedPax": includedPax}
	return unit, qty, nil
}

// quotePromoLine is the business line of a quotation line for the
// promotion scope.
func quotePromoLine(in sales.PriceInput) string {
	switch in.ServiceType {
	case "golf", "driving_range":
		return billing.LineGolf
	case "sport_court", "facility_entry", "class_session", "class_registration", "class_package", "locker":
		return billing.LineSport
	case "bungalow", "vip_suite", "meeting_room", "meeting_package", "equipment":
		return billing.LineStay
	case "membership":
		return billing.LineMembership
	}
	switch in.ItemType {
	case "package":
		return billing.LinePackage
	case "banquet_package", "banquet_menu":
		return billing.LineBanquet
	}
	switch in.Line {
	case "wedding", "banquet", "mice", "event":
		return billing.LineBanquet
	case "golf", "tournament":
		return billing.LineGolf
	case "stay":
		return billing.LineStay
	case "membership":
		return billing.LineMembership
	case "package":
		return billing.LinePackage
	}
	return billing.LineOther
}

// brandingLogo reads the logo of the quotation letterhead (FR-QUO-08): the
// organization logo, else the instance branding logo when it is an
// uploaded file; nil when there is none or it cannot be read.
func brandingLogo(files *storage.Files) func(ctx context.Context, q dbtx.Querier) []byte {
	return func(ctx context.Context, q dbtx.Querier) []byte {
		if files == nil {
			return nil
		}
		var fid *uuid.UUID
		var url *string
		_ = q.QueryRow(ctx, `SELECT (SELECT logo_file_id FROM platform.organization LIMIT 1), (SELECT branding->>'logoUrl' FROM platform.instance)`).
			Scan(&fid, &url)
		if fid == nil && url != nil && strings.HasPrefix(*url, "/api/v1/files/") {
			if u, err := uuid.Parse(strings.TrimPrefix(*url, "/api/v1/files/")); err == nil {
				fid = &u
			}
		}
		if fid == nil {
			return nil
		}
		rc, _, err := files.Open(ctx, *fid)
		if err != nil {
			return nil
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		if err != nil {
			return nil
		}
		return b
	}
}

// banquetQuotationTerms writes the terms & conditions of wedding, banquet,
// MICE and event quotations from the Banquet Policies in force (FR-QUO-08,
// PRD P3 §16 #11): down payment and payment terms, final pax cut-off,
// cancellation tiers, corkage, outside food and electricity.
func banquetQuotationTerms(ctx context.Context, q dbtx.Querier, property uuid.UUID) (string, error) {
	bk, _, err := rules.PolicyAt(ctx, q, banquet.PolicyBooking, property, banquet.DefaultBooking)
	if err != nil {
		return "", err
	}
	cn, _, err := rules.PolicyAt(ctx, q, banquet.PolicyCancellation, property, banquet.CancellationPolicy{
		Tiers: append([]banquet.BanquetCancellationTier{}, banquet.DefaultCancellation.Tiers...)})
	if err != nil {
		return "", err
	}
	ch, _, err := rules.PolicyAt(ctx, q, banquet.PolicyCharges, property, banquet.DefaultCharges)
	if err != nil {
		return "", err
	}
	var en, id []string
	pay := fmt.Sprintf("1. A down payment of %s%% confirms the booking; the date is held as Definite once the down payment is received.", bk.MinDownPaymentPercent)
	bayar := fmt.Sprintf("1. Uang muka %s%% mengonfirmasi pemesanan; tanggal menjadi Definite setelah uang muka diterima.", bk.MinDownPaymentPercent)
	if p, _ := decimal.NewFromString(bk.SecondTermPercent); p.IsPositive() && bk.SecondTermDaysBefore > 0 {
		pay += fmt.Sprintf(" A second payment of %s%% is due %d days before the event.", bk.SecondTermPercent, bk.SecondTermDaysBefore)
		bayar += fmt.Sprintf(" Termin kedua %s%% jatuh tempo H-%d.", bk.SecondTermPercent, bk.SecondTermDaysBefore)
	}
	pay += fmt.Sprintf(" The balance is due %d days before the event.", bk.FinalPaymentDaysBefore)
	bayar += fmt.Sprintf(" Pelunasan paling lambat H-%d.", bk.FinalPaymentDaysBefore)
	en = append(en, pay, fmt.Sprintf("2. The final number of guests is confirmed %d days before the event and may drop by at most %s%% of the guaranteed pax.",
		bk.GuaranteedPaxDaysBefore, bk.MaxPaxDecreasePercent))
	id = append(id, bayar, fmt.Sprintf("2. Jumlah tamu final dikonfirmasi H-%d dan boleh turun maksimal %s%% dari jumlah garansi.",
		bk.GuaranteedPaxDaysBefore, bk.MaxPaxDecreasePercent))
	tiers := append([]banquet.BanquetCancellationTier{}, cn.Tiers...)
	slices.SortFunc(tiers, func(a, b banquet.BanquetCancellationTier) int { return b.MinDaysBefore - a.MinDaysBefore })
	var ce, ci []string
	for _, t := range tiers {
		basis, dasar := "of the down payment", "dari uang muka"
		switch t.Basis {
		case "paid":
			basis, dasar = "of all payments", "dari seluruh pembayaran"
		case "contract":
			basis, dasar = "of the contract value", "dari nilai kontrak"
		}
		ce = append(ce, fmt.Sprintf("%d days or more before the event: %s%% %s is forfeited", t.MinDaysBefore, t.ForfeitPercent, basis))
		ci = append(ci, fmt.Sprintf(">= H-%d: %s%% %s hangus", t.MinDaysBefore, t.ForfeitPercent, dasar))
	}
	if len(ce) > 0 {
		en = append(en, "3. Cancellation: "+strings.Join(ce, "; ")+".")
		id = append(id, "3. Pembatalan: "+strings.Join(ci, "; ")+".")
	}
	extra := fmt.Sprintf("4. Corkage %s per bottle of wine / liquor.", fmtAmount(ch.CorkageFeePerBottle))
	tambahan := fmt.Sprintf("4. Corkage %s per botol wine / liquor.", fmtAmount(ch.CorkageFeePerBottle))
	if ch.OutsideFoodPartnerOnly {
		extra += " Outside food only from partner vendors."
		tambahan += " Makanan luar hanya dari vendor rekanan."
	}
	if ch.ElectricityIncludedWatt > 0 {
		extra += fmt.Sprintf(" Electricity up to %d W per ballroom is included; more is charged at the rate card.", ch.ElectricityIncludedWatt)
		tambahan += fmt.Sprintf(" Listrik termasuk %d W per ballroom; tambahan sesuai rate card.", ch.ElectricityIncludedWatt)
	}
	en, id = append(en, extra), append(id, tambahan)
	return strings.Join(en, "\n") + "\n\n" + strings.Join(id, "\n"), nil
}

// fmtAmount formats an amount with thousands separators (Rp 150.000).
func fmtAmount(s string) string {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return s
	}
	raw := d.Round(0).String()
	neg := strings.HasPrefix(raw, "-")
	raw = strings.TrimPrefix(raw, "-")
	var b strings.Builder
	for i, c := range raw {
		if i > 0 && (len(raw)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "Rp -" + b.String()
	}
	return "Rp " + b.String()
}
