package app

// Wiring of PRD P2 Complete Golf Experience & Shared Core next to P1 (Tech
// Doc §12.3 #6: the composition root changes additively). P2 modules and the
// P2 sub-parts of P1's modules (PRD P2 §5.4.1) use P1 through its public API
// and events.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/commercial/pos"
	"oneclub/internal/commercial/voucher"
	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/golf/experience"
	"oneclub/internal/inventory"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/membership"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reporting"
	"oneclub/internal/reservation"
	"oneclub/internal/sportclub"
	"oneclub/internal/stay"
)

// p2Contributions are the catalogue contributions of P2.
func p2Contributions() []catalog.Contribution {
	return []catalog.Contribution{
		experience.Contribution(), billing.P2Contribution(), pos.Contribution(), crm.EngagementContribution(), membership.P2Contribution(),
		stay.Contribution(), inventory.Contribution(), reporting.P2Contribution(),
	}
}

// p2DocumentTypes are the approval document types of P2.
var p2DocumentTypes = []provision.DocumentType{
	voucher.VoucherIssueType, voucher.VoucherExtendType, voucher.VoucherAdjustType, pos.RefundType, sportclub.InstructorFeeType,
	membership.PauseType, membership.PostponeType, membership.ReactivationType, membership.ChangeType, membership.NomineeType,
	experience.PromotionType, experience.SettlementType, experience.HIOType, experience.DamageChargeType, experience.IntroductionLetterType,
}

// p2Templates are the notification templates of the P2 modules.
func p2Templates() []provision.Template {
	var out []provision.Template
	for _, t := range [][]provision.Template{voucher.Templates(), membership.Templates(), sportclub.Templates(), experience.Templates(), crm.Templates(),
		reservation.Templates(), stay.Templates()} {
		out = append(out, t...)
	}
	return out
}

// P2 holds the P2 services.
type P2 struct {
	Reservations *reservation.Engine
	Vouchers     *voucher.Module
	POS          *pos.Module
	SportClub    *sportclub.Module
	Stay         *stay.Module
	Inventory    *inventory.Module
	CRM          *crm.Engagement
	Experience   *experience.Module
}

// buildP2 wires the P2 services and routes after P1's.
func (a *App) buildP2(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files, billingHTTP *billing.HTTP) {
	a.Engine.RegisterMeta(reg) // resource definitions the shell renders settings screens from
	billingHTTP.RegisterP2(reg)
	(&commercial.Module{DB: db}).RegisterP2(reg, a.Engine)
	a.Reporting.RegisterP2(reg)
	a.Reservations = &reservation.Engine{DB: db, Events: a.Bus, Billing: a.Billing, Notify: a.Notification, Price: indicativePrice}
	a.Reservations.Register(reg, a.Engine)
	a.Vouchers = &voucher.Module{DB: db, Billing: a.Billing, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification}
	a.Vouchers.Register(reg, a.Engine)
	a.Billing.RegisterTender("voucher_prepaid", a.Vouchers.TenderHandler())
	for _, dt := range []provision.DocumentType{voucher.VoucherIssueType, voucher.VoucherExtendType, voucher.VoucherAdjustType} {
		a.Approvals.RegisterDocumentType(dt, a.Vouchers.VoucherDecision)
	}
	a.POS = &pos.Module{DB: db, Billing: a.Billing, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, Realtime: a.Hub, Vouchers: a.Vouchers}
	a.POS.Register(reg, a.Engine)
	a.Approvals.RegisterDocumentType(pos.RefundType, a.POS.RefundDecision)
	a.Sync.Handle("commercial.pos_order", a.POS.SyncHandler)
	a.Membership.RegisterP2(reg)
	for _, dt := range membership.LifecycleDocumentTypes() {
		a.Approvals.RegisterDocumentType(dt, a.Membership.Decision)
	}
	a.SportClub = &sportclub.Module{DB: db, Res: a.Reservations, Billing: a.Billing, Vouchers: a.Vouchers, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification,
		Hub: a.Hub, Leads: a.sportLead}
	a.SportClub.Register(reg, a.Engine)
	a.Approvals.RegisterDocumentType(sportclub.InstructorFeeType, a.SportClub.FeeDecision)
	a.Sync.Handle("sportclub.access_validate", a.SportClub.SyncAccess)
	a.Stay = &stay.Module{DB: db, Res: a.Reservations, Billing: a.Billing, POS: a.POS, Vouchers: a.Vouchers, Events: a.Bus, Notify: a.Notification, Files: files}
	a.Stay.Register(reg, a.Engine)
	a.Inventory = &inventory.Module{DB: db, Products: a.POS}
	a.Inventory.Register(reg, a.Engine)
	a.CRM = &crm.Engagement{Module: &crm.Module{DB: db, Events: a.Bus, Files: files}, Notify: a.Notification, PublicURL: cfg.PublicBaseURL}
	a.CRM.RegisterP2(reg, a.Engine)
	a.Experience = &experience.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Billing: a.Billing, Notify: a.Notification, Golf: a.Golf,
		POS: a.POS, Vouchers: a.Vouchers, Reservations: a.Reservations, CRM: a.CRM, Files: files}
	a.Experience.Register(reg, a.Engine)
	a.Golf.SetReadyGuard(a.Experience.ReadyGuard) // FR-CTL-02 through contract C8
	a.Experience.RegisterSync(a.Sync)
	a.Experience.RegisterFeedbackSubject()
	for _, dt := range experience.DocumentTypes() {
		a.Approvals.RegisterDocumentType(dt, a.Experience.Decision)
	}
	// Customer 360, behaviour profile and segmentation facts contributed by
	// the business lines (CRM sits below them, Technical Doc §4.2).
	a.CRM.Sections = map[string]crm.SectionFunc{
		"membership": membership.CustomerSection, "golf": a.Experience.CustomerSection, "sportclub": a.SportClub.CustomerSection,
		"bookings": a.Reservations.CustomerSection, "pos": a.POS.CustomerSection, "payments": a.Billing.CustomerSection,
		"stay": a.Stay.CustomerSection, // Customer 360 › Stay (docs/requirement-booking-hotel-mgcc.md FR-H77)
	}
	a.CRM.Behavior = map[string]crm.BehaviorFunc{
		"golf": a.Experience.CustomerBehavior, "sportclub": a.SportClub.CustomerBehavior, "booking": a.Reservations.CustomerBehavior, "pos": a.POS.CustomerBehavior,
	}
	a.CRM.Facts = segmentFacts

	// Workers and schedules.
	reservation.RegisterEngineJobs(a.Registrar, db)
	a.SportClub.RegisterJobs(a.Registrar) // court reminders, membership prospects
	a.Vouchers.RegisterJobs(a.Registrar)
	a.Membership.RegisterP2Jobs(a.Registrar)
	a.Experience.RegisterJobs(a.Registrar)
	a.Stay.RegisterJobs(a.Registrar, a.Instance.Location) // accommodation: reminders, preventive maintenance, stayover cleaning
}

// subscribeP2 registers the P2 event subscribers.
func (a *App) subscribeP2() {
	// Golf rounds follow P1's starter (contract C7).
	a.Bus.Subscribe(golf.EventFlightTeedOff, "golf_experience.round_started", a.Experience.OnFlightTeedOff)
	a.Bus.Subscribe(golf.EventRoundFinished, "golf_experience.round_finished", a.Experience.OnRoundFinished)
	// Membership annual fee and lifecycle fees.
	a.Bus.Subscribe(membership.EventActivated, "membership.first_annual_fee", a.Membership.OnActivated)
	a.Bus.Subscribe(billing.EventPaymentSettled, "membership.fee_paid", a.Membership.OnFeePaid)
	// Online payments confirm held website / app bookings.
	a.Bus.Subscribe(billing.EventPaymentSettled, "reservation.confirm_paid", a.Reservations.ConfirmPaid)
	a.Bus.Subscribe("reservation.confirmed", "reservation.notify_confirmed", a.Reservations.NotifyConfirmed)
	// court bookings changed elsewhere (online payment, hold expiry) reach the open court boards
	for _, ev := range []string{"reservation.confirmed", "reservation.cancelled", "reservation.checked_in", "reservation.completed", "reservation.no_show",
		"reservation.rescheduled"} {
		a.Bus.Subscribe(ev, "sportclub.board_live", a.SportClub.OnReservationEvent)
	}
	// Theoretical consumption / food cost of every sale (FR-BOM-04).
	a.Bus.Subscribe("commercial.sale_completed", "inventory.consumption", a.Inventory.SaleCompleted)
}

// indicativePrice is the availability price hook of the Reservation Engine
// (FR-RSV-03): the total of the matching Commercial pricing rule.
func indicativePrice(ctx context.Context, q dbtx.Querier, property uuid.UUID, rt reservation.ResourceType, res reservation.Resource,
	start, end time.Time, segment string) (*string, error) {
	if rt.ServiceType == nil {
		return nil, nil
	}
	pr, err := commercial.Pricer{}.Resolve(ctx, q, property, commercial.PriceRequest{ServiceType: *rt.ServiceType, ItemRef: res.PriceItem(),
		Segment: segment, Start: start, End: &end})
	if err != nil {
		return nil, nil //nolint:nilerr // no matching rule: no indicative price
	}
	t := pr.Tax.Total
	return &t, nil
}

// segmentFacts are the CRM segmentation facts: membership types and
// programs, visits and spend.
func segmentFacts(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]crm.Facts, error) {
	types, programs, err := membership.SegmentFacts(ctx, q, property)
	if err != nil {
		return nil, err
	}
	act, err := billing.ActivitySince(ctx, q, property, since)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]crm.Facts{}
	for c, t := range types {
		f := out[c]
		f.MembershipTypes, f.Programs = t, programs[c]
		out[c] = f
	}
	for c, x := range act {
		f := out[c]
		f.Visits, f.Spend = x.Visits, x.Spend
		out[c] = f
	}
	courts, err := sportclub.SegmentFacts(ctx, q, property, since)
	if err != nil {
		return nil, err
	}
	for c, x := range courts {
		f := out[c]
		f.CourtBookings, f.Sports, f.LastCourtPlay = x.CourtBookings, x.Sports, x.LastCourtPlay
		out[c] = f
	}
	// bungalow stays (docs/requirement-booking-hotel-mgcc.md FR-H78)
	stays, err := stay.SegmentFacts(ctx, q, property, since)
	if err != nil {
		return nil, err
	}
	for c, x := range stays {
		f := out[c]
		f.Stays, f.LastStay = x.Stays, x.LastStay
		out[c] = f
	}
	return out, nil
}
