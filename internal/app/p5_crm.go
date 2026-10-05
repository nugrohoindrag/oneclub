package app

// PRD P5 EP-17–20: advanced segmentation & VIP, advanced loyalty, campaign & lifecycle journeys, CRM & sales analytics. internal/app/p5.go calls these functions; the area fills them.
//
// CRM sits above golf, membership, commercial and billing (Technical Doc
// §4.2): the composition root wires the tier booking-window benefit into
// golf booking, the Commercial vouchers of journey steps, the analytics
// segmentation tags and Customer 360 section, and the domain events of
// other modules into the journeys (triggers, exit rules, goals).

import (
	"context"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/platform/calendar"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/commercial/voucher"
	"oneclub/internal/crm/analytics"
	"oneclub/internal/crm/engagement"
	"oneclub/internal/crm/journey"
	"oneclub/internal/crm/loyalty"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reporting"
)

// p5CRM holds the services of the area.
type p5CRM struct {
	Journeys  *journey.Service
	Analytics *analytics.Service
}

func init() {
	// Reports of the area (FR-RPT-P5-03) next to the P2–P4 reports.
	Reports = append(Reports, reporting.P5CRMReports()...)
}

func p5CRMContributions() []catalog.Contribution {
	perms := append(append(loyalty.P5Permissions(), journey.Permissions()...), analytics.Permissions()...)
	roles := map[string][]string{}
	for _, m := range []map[string][]string{loyalty.P5RolePermissions(), journey.RolePermissions(), analytics.RolePermissions()} {
		for role, ps := range m {
			roles[role] = append(roles[role], ps...)
		}
	}
	return append([]catalog.Contribution{{Permissions: perms, RolePermissions: roles}, reporting.P5CRMContribution()}, p5TiersContributions()...)
}

func p5CRMDocumentTypes() []provision.DocumentType {
	return append([]provision.DocumentType{journey.ActivationDocumentType}, p5TiersDocumentTypes()...)
}

func p5CRMTemplates() []provision.Template {
	return append(loyalty.P5Templates(), journey.Templates()...)
}

// buildP5CRM wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5CRM(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	e := a.Engage
	js := &journey.Service{DB: db, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, Loyalty: e.Loyalty, IssueVoucher: a.journeyVouchers,
		WebsiteURL: func() string { return cfg.WebsiteURL }, PortalURL: func() string { return cfg.MemberPortalURL }}
	as := &analytics.Service{DB: db, TopSpender: e.TopSpender}
	a.CRMPlus = p5CRM{Journeys: js, Analytics: as}

	e.Loyalty.RegisterP5(reg, a.Engine, e.TopSpender)
	js.Register(reg)
	as.Register(reg)
	a.Approvals.RegisterDocumentType(journey.ActivationDocumentType, js.Decision)
	e.Loyalty.RegisterP5Jobs(a.Registrar, e.TopSpender, a.Instance.Location)
	js.RegisterJobs(a.Registrar)
	as.RegisterJobs(a.Registrar, a.Instance.Location)

	// Analytics tags in segment rules (rfm_<group>, vip, vvip, multi_line, line_<line>; FR-SEG-01..03).
	if e.Service.Tags == nil {
		e.Service.Tags = map[string]engagement.TagFunc{}
	}
	for _, t := range analytics.Tags {
		e.Service.Tags[t] = analytics.TagSet(t)
	}
	// Customer 360: VIP, RFM and behaviour per line.
	a.CRM.Sections["analytics"] = analytics.Section
	// Tier benefit "booking window + days" honoured by golf booking (FR-LOY-P5-02).
	golf.SetTierBookingWindow(loyalty.BookingWindowBonus, loyalty.MaxBookingWindowBonus)
	a.buildP5Tiers(reg) // tier classes & classification, POS tier discount (p5_tiers.go)
}

// journeyVouchers issues the Commercial vouchers of a journey voucher step
// once per step and enrollment.
func (a *App) journeyVouchers(ctx context.Context, tx pgx.Tx, in journey.VoucherRequest) ([]string, error) {
	vs, _, err := a.Vouchers.IssueOnce(ctx, tx, voucher.IssueOnceRequest{PropertyID: in.PropertyID, TypeCode: in.TypeCode, CustomerID: in.CustomerID,
		Quantity: 1, SourceType: "crm.journey", SourceID: in.SourceID, Notes: "Journey " + in.Reference})
	codes := make([]string, 0, len(vs))
	for _, v := range vs {
		codes = append(codes, v.Code)
	}
	return codes, err
}

// subscribeP5CRM registers the event subscribers.
func (a *App) subscribeP5CRM() {
	js := a.CRMPlus.Journeys
	if js == nil {
		return
	}
	for _, ev := range journey.TriggerEvents {
		a.Bus.Subscribe(ev, "crm.journey."+ev, js.OnEvent)
	}
}

// demoP5CRM seeds the demo data of the area (idempotent): tiers with the
// PRD P5 §16 #14 thresholds and benefits, reward costs and eligibility, a
// Top Spender programme, the journey voucher type and the five priority
// journeys (§16 #15) Active / Paused with sample enrollments.
func demoP5CRM(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	for _, t := range []struct {
		code, spend, mult, disc, benefits string
		window                            int
		event, prio                       bool
	}{
		{"SILVER", "0", "1", "0", "1 point per Rp10.000 net spend", 0, false, false},
		{"GOLD", "25000000", "1.25", "5", "Points × 1.25, booking window +2 days, 5% F&B discount (spend ≥ Rp25 jt / 12 months)", 2, false, false},
		{"PLATINUM", "75000000", "1.5", "10", "Points × 1.5, booking window +4 days, 10% F&B discount, VIP event invitations (spend ≥ Rp75 jt / 12 months)", 4, true, true},
	} {
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tiers SET min_points = 0, min_spend = $3::numeric, multiplier = $4::numeric, fnb_discount_percent = $5::numeric,
			benefits = $6, booking_window_days = $7, event_access = $8, priority_service = $9 WHERE property_id = $1 AND code = $2`, property, t.code, t.spend,
			t.mult, t.disc, t.benefits, t.window, t.event, t.prio); err != nil {
			return err
		}
	}
	for code, cost := range map[string]string{"RW-COFFEE": "35000", "RW-BALLS": "40000", "RW-CAP": "150000", "RW-FNB100": "100000"} {
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_rewards SET unit_cost = $3::numeric WHERE property_id = $1 AND code = $2 AND unit_cost = 0`,
			property, code, cost); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_reward_rules (id, property_id, code, name, reward_id, min_tier_id, max_per_customer, limit_period)
		SELECT $2, $1, 'RR-CAP-GOLD', 'Club cap: Gold and above, once a year', w.id, t.id, 1, 'year' FROM crm.loyalty_rewards w, crm.loyalty_tiers t
		WHERE w.property_id = $1 AND w.code = 'RW-CAP' AND t.property_id = $1 AND t.code = 'GOLD' ON CONFLICT (property_id, code) DO NOTHING`, property, id.New()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.top_spender_programs (id, property_id, code, name, period_type, top_n, reward_id)
		SELECT $2, $1, 'TSP-MONTHLY', 'Monthly Top 10: F&B voucher', 'month', 10, w.id FROM crm.loyalty_rewards w WHERE w.property_id = $1 AND w.code = 'RW-FNB100'
		ON CONFLICT (property_id, code) DO NOTHING`, property, id.New()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.voucher_types (id, property_id, code, name, kind, category, unit, face_value, validity_months, revenue_component)
		VALUES ($1, $2, $3, 'Journey F&B voucher Rp100.000', 'value', 'fnb', 'rupiah', 100000, 3, 'fnb') ON CONFLICT (property_id, code) DO NOTHING`,
		id.New(), property, journey.DefaultVoucherType); err != nil {
		return err
	}
	js := &journey.Service{}
	status := map[string]string{"renewal": "active", "birthday": "active", "welcome": "active", "win_back": "paused", "post_event": "paused"}
	for _, info := range journey.TemplateInfos {
		st, ok := status[info.Template]
		if !ok {
			continue
		}
		var jid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM crm.journeys WHERE property_id = $1 AND code = $2`, property, info.Code).Scan(&jid)
		if dbtx.IsNoRows(err) {
			j, err := js.FromTemplate(ctx, tx, property, journey.TemplateRequest{Template: info.Template})
			if err != nil {
				return err
			}
			jid = j.ID
			if _, err := tx.Exec(ctx, `UPDATE crm.journeys SET status = $2, activated_at = now(), paused_at = CASE WHEN $2 = 'paused' THEN now() END
				WHERE id = $1`, jid, st); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	// Sample enrollments: members whose membership ends within 60 days in
	// the renewal journey, recent customers in the (paused) win-back.
	if _, err := tx.Exec(ctx, `INSERT INTO crm.journey_enrollments (id, property_id, journey_id, customer_id, occurrence, current_key, next_run_at, anchor_date, context)
		SELECT gen_random_uuid(), $1, j.id, m.customer_id, 'ms:' || m.membership_id || ':' || to_char(m.ends_on, 'YYYY-MM-DD'), 'offer_h60',
		now() + interval '1 hour', m.ends_on, jsonb_build_object('membershipId', m.membership_id, 'detail', m.type_name, 'source', 'demo')
		FROM crm.journeys j JOIN LATERAL (SELECT DISTINCT ON (customer_id) customer_id, membership_id, ends_on, type_name FROM reporting.membership_lifecycle
		  WHERE property_id = $1 AND role = 'principal' AND status = 'active' AND customer_id IS NOT NULL AND ends_on BETWEEN $2::date AND $2::date + 60
		  ORDER BY customer_id, ends_on LIMIT 3) m ON true
		WHERE j.property_id = $1 AND j.code = 'JRN-RENEWAL' ON CONFLICT (journey_id, customer_id, occurrence) DO NOTHING`, property,
		clock.Now().In(calendar.Location(ctx, tx)).Format(time.DateOnly)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO crm.journey_enrollments (id, property_id, journey_id, customer_id, occurrence, current_key, next_run_at, context)
		SELECT gen_random_uuid(), $1, j.id, c.id, 'demo', 'offer', now(), '{"source": "demo"}'::jsonb
		FROM crm.journeys j JOIN LATERAL (SELECT id FROM crm.customers WHERE property_id = $1 AND status = 'active' AND erased_at IS NULL
		  ORDER BY created_at, id LIMIT 3) c ON true
		WHERE j.property_id = $1 AND j.code = 'JRN-WINBACK' ON CONFLICT (journey_id, customer_id, occurrence) DO NOTHING`, property)
	if err != nil {
		return err
	}
	return demoP5Tiers(ctx, tx, property) // tier badges, demo members per tier (p5_tiers.go)
}
