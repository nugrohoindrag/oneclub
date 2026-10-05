package app

// PRD P3 EP-05–09 Customer 360 & segmentation, campaigns & reminders, feedback/NPS/complaint tickets, Top Spender, Loyalty (owner: crm).
// CRM sits below Billing, Commercial and the business lines (Technical Doc
// §4.2): the composition root plugs the loyalty_points tender, the points
// liability of the night audit and the accounting export rows into Billing,
// wires the Customer 360 sections, the P2 campaign send and the events of
// the other modules (payments, refunds, rounds, won deals, promotions,
// events) into CRM.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/crm/engagement"
	"oneclub/internal/crm/loyalty"
	"oneclub/internal/crm/topspender"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// Events of other modules CRM engagement consumes (decoded by name,
// docs/p3-p4-contracts.md).
const (
	engEventOpportunityWon    = "crm.opportunity_won"
	engEventQuotationAccepted = "crm.quotation_accepted"
	engEventPromotionApplied  = "commercial.promotion_applied"
	engEventBanquetConfirmed  = "banquet.event_confirmed"
	engEventGuestCheckedIn    = "banquet.event_guest_checked_in"
	engEventTournamentRegs    = "golf.tournament_registration_confirmed"
	engEventTournamentResults = "golf.tournament_results_published"
)

// p3Engagement holds the services of the area.
type p3Engagement struct {
	Service    *engagement.Service
	Loyalty    *loyalty.Module
	TopSpender *topspender.Service
}

// p3EngagementContributions are the catalogue contributions (permissions, role mappings).
func p3EngagementContributions() []catalog.Contribution {
	perms := append(append(engagement.Permissions(), loyalty.Permissions()...), topspender.Permissions()...)
	roles := map[string][]string{}
	for _, m := range []map[string][]string{engagement.RolePermissions(), loyalty.RolePermissions(), topspender.RolePermissions()} {
		for role, ps := range m {
			roles[role] = append(roles[role], ps...)
		}
	}
	return []catalog.Contribution{{Permissions: perms, RolePermissions: roles}}
}

// p3EngagementDocumentTypes are the approval document types.
func p3EngagementDocumentTypes() []provision.DocumentType {
	return append(engagement.DocumentTypes(), loyalty.AdjustmentDocumentType)
}

// p3EngagementTemplates are the notification templates.
func p3EngagementTemplates() []provision.Template {
	return append(engagement.Templates(), loyalty.Templates()...)
}

// buildP3Engagement wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Engagement(reg *route.Registry, cfg *config.Config, db *dbtx.DB, _ *storage.Files) {
	lm := &loyalty.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, PayFolio: a.payFolioWithPoints}
	ts := &topspender.Service{DB: db}
	svc := &engagement.Service{DB: db, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, CRM: a.CRM, Loyalty: lm, TopSpender: ts,
		WebsiteURL: func() string { return cfg.WebsiteURL }, PortalURL: func() string { return cfg.MemberPortalURL }}
	a.Engage = p3Engagement{Service: svc, Loyalty: lm, TopSpender: ts}

	lm.Register(reg, a.Engine)
	svc.Register(reg, a.Engine)
	ts.Register(reg)
	a.Approvals.RegisterDocumentType(engagement.CampaignSendDocumentType, svc.CampaignDecision)
	a.Approvals.RegisterDocumentType(engagement.CompensationDocumentType, svc.CompensationDecision)
	a.Approvals.RegisterDocumentType(loyalty.AdjustmentDocumentType, lm.AdjustmentDecision)
	lm.RegisterJobs(a.Registrar, a.Instance.Location)
	svc.RegisterJobs(a.Registrar, a.Instance.Location)
	ts.RegisterJobs(a.Registrar, a.Instance.Location)

	// P2's campaign send follows the P3 consent, suppression, cap and approval rules.
	crm.SetCampaignSender(svc.SendNow)
	// Customer 360 sections (FR-C360-01); banquet and tournament add theirs.
	a.CRM.Sections["loyalty"] = lm.CustomerSection
	a.CRM.Sections["campaignResponse"] = svc.CampaignSectionOf
	a.CRM.Sections["complaints"] = svc.ComplaintSectionOf
	a.CRM.Sections["sales"] = engagement.SalesSectionOf

	// Points as a tender on POS, front desk and Member App folios (FR-LOY-05, FR-OPS-P3-03).
	a.Billing.RegisterTender("loyalty_points", func(ctx context.Context, tx pgx.Tx, r billing.TenderRequest) (billing.TenderResult, error) {
		res, err := lm.Redeem(ctx, tx, loyalty.RedeemRequest{PropertyID: r.PropertyID, FolioID: r.FolioID, CustomerID: r.CustomerID, Amount: r.Amount,
			Data: r.Data, IdempotencyKey: r.IdempotencyKey})
		return billing.TenderResult{Amount: res.Amount, Ref: res.Ref}, err
	})
	// Points liability in the business day summary / night audit and the
	// accounting export (FR-LOY-08, FR-INT-P3-05).
	a.Billing.RegisterLiability("loyalty_points", lm.Liability)
	a.Billing.RegisterExportSection("crm.loyalty", func(ctx context.Context, q dbtx.Querier, property uuid.UUID, _, from, to time.Time) ([]billing.ExportRow, error) {
		rows, err := loyalty.ExportRows(ctx, q, property, from, to)
		out := make([]billing.ExportRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, billing.ExportRow{Section: r.Section, Code: r.Code, Description: r.Description, Amount: r.Amount})
		}
		return out, err
	})
}

// payFolioWithPoints takes a loyalty_points payment through Billing, which
// calls back the tender (front desk / Member App "pay with points").
func (a *App) payFolioWithPoints(ctx context.Context, tx pgx.Tx, in loyalty.FolioPayment) (loyalty.LoyaltyFolioPayment, error) {
	tender := map[string]any{"accountId": in.AccountID.String()}
	if in.Points > 0 {
		tender["points"] = in.Points
	}
	fid := in.FolioID
	p, err := a.Billing.TakeTender(ctx, tx, billing.TenderPaymentInput{PaymentInput: billing.PaymentInput{FolioID: &fid, MethodType: "loyalty_points",
		Amount: in.Amount, Description: "Loyalty points"}, Tender: tender, IdempotencyKey: in.IdempotencyKey})
	if err != nil {
		return loyalty.LoyaltyFolioPayment{}, err
	}
	t, err := billing.TenderOf(ctx, tx, p.ID)
	return loyalty.LoyaltyFolioPayment{PaymentID: p.ID, Number: p.Number, Amount: p.Amount, Status: p.Status, TenderRef: t.TenderRef}, err
}

// subscribeP3Engagement registers the event subscribers.
func (a *App) subscribeP3Engagement() {
	e := a.Engage
	// Loyalty: earn on settled payments (never the order), reverse on
	// refunds, activity points, referral reward, identity changes.
	a.Bus.Subscribe(billing.EventPaymentSettled, "crm.loyalty_earn", e.Loyalty.OnPaymentSettled)
	a.Bus.Subscribe(billing.EventRefundProcessed, "crm.loyalty_refund", e.Loyalty.OnRefundProcessed)
	a.Bus.Subscribe(golf.EventRoundFinished, "crm.loyalty_round_points", e.Loyalty.OnRoundFinished)
	a.Bus.Subscribe(engEventOpportunityWon, "crm.loyalty_referral", e.Loyalty.OnOpportunityWon)
	a.Bus.Subscribe(engEventGuestCheckedIn, "crm.loyalty_event_points", e.Loyalty.OnEventAttended)
	a.Bus.Subscribe(crm.EventCustomerMerged, "crm.loyalty_customer_merged", e.Loyalty.OnCustomerMerged)
	a.Bus.Subscribe(crm.EventCustomerErased, "crm.loyalty_customer_erased", e.Loyalty.OnCustomerErased)
	// Campaign conversions: a payment or a promo code use within the window.
	a.Bus.Subscribe(billing.EventPaymentSettled, "crm.campaign_conversion_payment", e.Service.OnPaymentSettled)
	a.Bus.Subscribe(engEventPromotionApplied, "crm.campaign_conversion_promo", e.Service.OnPromotionApplied)
	// Automatic interactions and segmentation tags from events (FR-C360-02/05).
	a.Bus.Subscribe(engEventBanquetConfirmed, "crm.interaction_event", engagementEventInteraction)
	a.Bus.Subscribe(engEventQuotationAccepted, "crm.tag_quotation", engagementQuotationTag)
	a.Bus.Subscribe(engEventTournamentRegs, "crm.tag_tournament_participant", engagementTournamentTag)
	a.Bus.Subscribe(engEventTournamentResults, "crm.tag_tournament_champion", engagementChampionTag)
}

// engagementEventInteraction logs a confirmed banquet / wedding / MICE event
// in the customer's interaction history and tags the customer.
func engagementEventInteraction(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		EventID    uuid.UUID  `json:"eventId"`
		Number     string     `json:"number"`
		EventType  string     `json:"eventType"`
		Title      string     `json:"title"`
		CustomerID *uuid.UUID `json:"customerId"`
		StartDate  string     `json:"startDate"`
	}
	if err := e.Decode(&p); err != nil || p.CustomerID == nil || e.PropertyID == nil {
		return nil //nolint:nilerr // not an event of a known customer
	}
	ok, err := crm.ExistsInProperty(ctx, tx, *e.PropertyID, *p.CustomerID)
	if err != nil || !ok {
		return err
	}
	eid := p.EventID
	if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, source, ref_type, ref_id)
		SELECT gen_random_uuid(), $1, $2, 'other', 'outbound', $3, 'event', 'banquet.event', $4
		WHERE NOT EXISTS (SELECT 1 FROM crm.interactions WHERE ref_type = 'banquet.event' AND ref_id = $4 AND customer_id = $2)`,
		*e.PropertyID, *p.CustomerID, "Event confirmed · "+p.Number+" "+p.Title+" ("+p.StartDate+")", eid); err != nil {
		return err
	}
	tag := "event_host"
	if p.EventType == "wedding" {
		tag = "wedding"
	}
	return engagement.TagCustomer(ctx, tx, *e.PropertyID, *p.CustomerID, tag, "banquet.event", &eid)
}

// engagementQuotationTag tags the customer of an accepted quotation with its
// business line (e.g. wedding, mice, tournament).
func engagementQuotationTag(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		QuotationID uuid.UUID  `json:"quotationId"`
		PropertyID  uuid.UUID  `json:"propertyId"`
		CustomerID  *uuid.UUID `json:"customerId"`
		Line        string     `json:"line"`
	}
	if err := e.Decode(&p); err != nil || p.CustomerID == nil || p.Line == "" {
		return nil //nolint:nilerr // nothing to tag
	}
	property := p.PropertyID
	if property == uuid.Nil && e.PropertyID != nil {
		property = *e.PropertyID
	}
	qid := p.QuotationID
	return engagement.TagCustomer(ctx, tx, property, *p.CustomerID, "deal_"+p.Line, "crm.quotation", &qid)
}

// engagementTournamentTag tags a registered tournament participant.
func engagementTournamentTag(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		TournamentID uuid.UUID  `json:"tournamentId"`
		CustomerID   *uuid.UUID `json:"customerId"`
	}
	if err := e.Decode(&p); err != nil || p.CustomerID == nil || e.PropertyID == nil {
		return nil //nolint:nilerr // not a registration of a known customer
	}
	tid := p.TournamentID
	return engagement.TagCustomer(ctx, tx, *e.PropertyID, *p.CustomerID, "tournament_participant", "golf.tournament", &tid)
}

// engagementChampionTag tags the champions of a tournament (contract
// golf.tournament_results_published).
func engagementChampionTag(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		TournamentID uuid.UUID `json:"tournamentId"`
		Champions    []struct {
			CustomerID *uuid.UUID `json:"customerId"`
		} `json:"champions"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil {
		return nil //nolint:nilerr // foreign payload
	}
	tid := p.TournamentID
	for _, c := range p.Champions {
		if c.CustomerID == nil {
			continue
		}
		if err := engagement.TagCustomer(ctx, tx, *e.PropertyID, *c.CustomerID, "tournament_participant", "golf.tournament", &tid); err != nil {
			return err
		}
		if err := engagement.TagCustomer(ctx, tx, *e.PropertyID, *c.CustomerID, "tournament_champion", "golf.tournament", &tid); err != nil {
			return err
		}
	}
	return nil
}

// demoP3Engagement seeds demo data for the area on the MAIN property (idempotent).
func demoP3Engagement(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	if err := loyalty.SeedDefaults(ctx, tx, property); err != nil {
		return err
	}
	return engagement.SeedDefaults(ctx, tx, property)
}
