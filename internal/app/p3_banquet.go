package app

// PRD P3 EP-12–15 Event Management, Banquet/MICE/Wedding, BEO, Banquet
// Venue & Event Resource (owner: banquet). Banquet sits in the business
// line layer: it calls the Reservation Engine and Billing through their
// root packages, CRM quotations reach it as outbox events (crm.quotation_*)
// and the F&B vouchers of packages are issued through the voucher engine,
// plugged in here.

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/banquet"
	"oneclub/internal/billing"
	"oneclub/internal/commercial/voucher"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p3Banquet holds the services of the area.
type p3Banquet struct {
	Module *banquet.Module
}

// p3BanquetContributions are the catalogue contributions (permissions, role mappings).
func p3BanquetContributions() []catalog.Contribution {
	return []catalog.Contribution{banquet.Contribution()}
}

// p3BanquetDocumentTypes are the approval document types.
func p3BanquetDocumentTypes() []provision.DocumentType { return banquet.DocumentTypes() }

// p3BanquetTemplates are the notification templates.
func p3BanquetTemplates() []provision.Template { return banquet.Templates() }

// buildP3Banquet wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Banquet(reg *route.Registry, cfg *config.Config, db *dbtx.DB, _ *storage.Files) {
	m := &banquet.Module{DB: db, Res: a.Reservations, Billing: a.Billing, Invoices: a.BillingHTTP, Events: a.Bus, Approvals: a.Approvals,
		Notify: a.Notification, Vouchers: a.banquetVouchers, WebsiteURL: func() string { return cfg.WebsiteURL },
		StaffURL: func() string { return cfg.PublicBaseURL }}
	m.Register(reg, a.Engine)
	a.Approvals.RegisterDocumentType(banquet.DefiniteOverrideType, m.DefiniteDecision)
	m.RegisterJobs(a.Registrar, a.Instance.Location)
	a.Billing.RegisterNightAuditCheck("banquet_events", m.NightAuditCheck)
	a.Sync.Handle("banquet.event_check_in", m.SyncCheckIn)
	a.Banquet.Module = m
	a.convertsQuotations("banquet", banquet.QuotationLines...)
	// Customer 360 and Corporate 360 Banquet / Event sections (FR-C360-01/04).
	if a.CRM != nil && a.CRM.Sections != nil {
		a.CRM.Sections["banquet"] = m.CustomerSection
	}
	a.corporateSection("banquet", m.CorporateSection)
}

// corporateSection adds a Corporate 360 section of a business line
// (FR-C360-04); engagement is built before the business lines.
func (a *App) corporateSection(key string, f crm.SectionFunc) {
	svc := a.Engage.Service
	if svc == nil {
		return
	}
	if svc.CorporateSections == nil {
		svc.CorporateSections = map[string]crm.SectionFunc{}
	}
	svc.CorporateSections[key] = f
}

// banquetVouchers issues the F&B vouchers of a package inclusion (FR-BQT-06).
func (a *App) banquetVouchers(ctx context.Context, tx pgx.Tx, property uuid.UUID, typeCode string, quantity int, customerID, eventID uuid.UUID,
	note string) ([]string, error) {
	codes := make([]string, 0, quantity)
	for range max(quantity, 1) {
		v, err := a.Vouchers.Issue(ctx, tx, voucher.IssueRequest{PropertyID: property, TypeCode: strings.ToUpper(typeCode), CustomerID: &customerID,
			Via: "issue", SourceType: "banquet_event", SourceID: &eventID, Notes: note})
		if err != nil {
			return codes, err
		}
		codes = append(codes, v.Code)
	}
	return codes, nil
}

// subscribeP3Banquet registers the event subscribers.
func (a *App) subscribeP3Banquet() {
	m := a.Banquet.Module
	a.Bus.Subscribe(banquet.QuotationSent, "banquet.quotation_hold", m.OnQuotationSent)
	a.Bus.Subscribe(banquet.QuotationAccepted, "banquet.quotation_event", m.OnQuotationAccepted)
	a.Bus.Subscribe(banquet.QuotationRejected, "banquet.quotation_release_rejected", m.OnQuotationClosed)
	a.Bus.Subscribe(banquet.QuotationExpired, "banquet.quotation_release_expired", m.OnQuotationClosed)
	a.Bus.Subscribe(banquet.PackageBooked, "banquet.package_event", m.OnPackageBooked)
	a.Bus.Subscribe(banquet.PackageCancelled, "banquet.package_cancelled", m.OnPackageCancelled)
	// A Definite event that uses the golf course blocks its tee times (FR-EVT-03).
	a.Bus.Subscribe(banquet.EventGolfBlockRequested, "golf.event_block_requested", a.Golf.OnEventGolfBlock)
	a.Bus.Subscribe(banquet.EventGolfBlockReleased, "golf.event_block_released", a.Golf.OnEventGolfBlock)
	a.Bus.Subscribe(billing.EventPaymentSettled, "banquet.payment_settled", m.OnPaymentSettled)
	a.Bus.Subscribe(billing.EventInvoicePaid, "banquet.invoice_paid", m.OnInvoicePaid)
	a.Bus.Subscribe(billing.EventScheduleDue, "banquet.payment_due", m.OnScheduleDue)
}

// demoP3Banquet seeds demo data for the area on the MAIN property
// (idempotent): event types, the venues of PRD P3 FR-VEN-01 with their
// bookable resources and layouts, the packages of FR-BQT-01, a wedding
// buffet menu with category quotas, the corkage charge and a wedding
// checklist template.
func demoP3Banquet(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	types := [][3]string{{"WEDDING", "Wedding", "wedding"}, {"CORP_GATHERING", "Corporate Gathering", "social"}, {"TOURNAMENT", "Golf Tournament", "tournament"},
		{"BIRTHDAY", "Birthday", "social"}, {"KIDS_BIRTHDAY", "Kids Birthday", "social"}, {"FAMILY_GATHERING", "Family Gathering", "social"},
		{"COMPANY_OUTING", "Company Outing", "social"}, {"SEMINAR", "Seminar", "mice"}, {"MEETING", "Meeting", "mice"},
		{"COMMUNITY", "Community Event", "social"}, {"SOCIAL", "Social Event", "banquet"}}
	for _, t := range types {
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_types (id, property_id, code, name, category) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, t[0], t[1], t[2]); err != nil {
			return err
		}
	}
	type venue struct {
		code, name, kind, parent string
		capacity, minPax         int
		addon                    string
		layouts                  map[string]int
	}
	venues := []venue{
		{"BALLROOM", "Grand Ballroom", "ballroom", "", 600, 0, "0", map[string]int{"round_table": 400, "theater": 600, "standing": 600, "classroom": 300}},
		{"BALLROOM-A", "Grand Ballroom A", "ballroom", "BALLROOM", 300, 0, "0", map[string]int{"round_table": 200, "theater": 300}},
		{"BALLROOM-B", "Grand Ballroom B", "ballroom", "BALLROOM", 300, 0, "0", map[string]int{"round_table": 200, "theater": 300}},
		{"SAPPHIRE", "Sapphire Room", "function_room", "", 80, 0, "0", map[string]int{"classroom": 50, "u_shape": 30, "theater": 80, "boardroom": 24}},
		{"EMERALD", "Emerald Room", "function_room", "", 60, 0, "0", map[string]int{"classroom": 40, "u_shape": 24, "theater": 60, "boardroom": 20}},
		{"JADE", "Jade Room", "function_room", "", 80, 0, "0", map[string]int{"classroom": 60, "u_shape": 30, "theater": 80}},
		{"RUBY", "Ruby Room", "function_room", "", 40, 0, "0", map[string]int{"classroom": 24, "boardroom": 16}},
		{"GARDEN", "Garden & Lake View", "outdoor", "", 500, 100, "15000000", map[string]int{"round_table": 400, "standing": 500}},
		{"POOLSIDE", "Pool Side", "outdoor", "", 300, 100, "10000000", map[string]int{"round_table": 200, "standing": 300}},
		{"VIP-SUITE", "VIP Suite", "vip_suite", "", 20, 0, "0", map[string]int{"boardroom": 12, "standing": 20}},
	}
	ids := map[string]uuid.UUID{}
	for _, v := range venues {
		var vid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM banquet.venues WHERE property_id = $1 AND code = $2`, property, v.code).Scan(&vid)
		if err == nil {
			ids[v.code] = vid
			continue
		}
		if !dbtx.IsNoRows(err) {
			return err
		}
		var rid uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO reservation.resources (id, property_id, code, name, resource_type, capacity, status, attributes)
			VALUES ($1,$2,$3,$4,'banquet_venue',$5,'active',$6) ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			id.New(), property, "BQV-"+v.code, v.name, v.capacity, fmt.Sprintf(`{"venueType":%q}`, v.kind)).Scan(&rid); err != nil {
			return err
		}
		var parent *uuid.UUID
		if v.parent != "" {
			p := ids[v.parent]
			parent = &p
		}
		vid = id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.venues (id, property_id, code, name, venue_type, parent_venue_id, max_capacity, min_pax, addon_price,
			electricity_watt, resource_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11)`, vid, property, v.code, v.name, v.kind, parent, v.capacity, v.minPax,
			v.addon, map[bool]int{true: 10000, false: 0}[v.kind == "ballroom" && v.parent == ""], rid); err != nil {
			return err
		}
		for l, c := range v.layouts {
			if _, err := tx.Exec(ctx, `INSERT INTO banquet.venue_layouts (id, property_id, venue_id, layout, capacity) VALUES ($1,$2,$3,$4,$5)`,
				id.New(), property, vid, l, c); err != nil {
				return err
			}
		}
		ids[v.code] = vid
	}
	var menu uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM banquet.menus WHERE property_id = $1 AND code = 'WED-BUFFET'`, property).Scan(&menu)
	if dbtx.IsNoRows(err) {
		menu = id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.menus (id, property_id, code, name, menu_type, price_per_pax) VALUES ($1,$2,'WED-BUFFET',
			'Wedding Buffet','buffet',190000)`, menu, property); err != nil {
			return err
		}
		cats := []struct {
			name  string
			quota int
			items []string
		}{{"Appetizer", 2, []string{"Gado-gado Salad", "Lumpia Semarang", "Caesar Salad"}}, {"Soup", 1, []string{"Sop Buntut", "Cream of Mushroom"}},
			{"Rice", 1, []string{"Nasi Putih", "Nasi Goreng Kampung"}}, {"Noodle / Pasta", 1, []string{"Bakmi Goreng", "Spaghetti Bolognese"}},
			{"Chicken", 1, []string{"Ayam Bakar Taliwang", "Chicken Teriyaki"}}, {"Fish", 1, []string{"Ikan Asam Manis", "Dory Sambal Matah"}},
			{"Vegetable", 1, []string{"Cap Cay", "Tumis Kangkung"}}, {"Dessert", 2, []string{"Es Campur", "Pudding Coklat", "Buah Potong"}}}
		for i, c := range cats {
			cid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO banquet.menu_categories (id, property_id, menu_id, name, quota, extra_choice_price, sort_order)
				VALUES ($1,$2,$3,$4,$5,25000,$6)`, cid, property, menu, c.name, c.quota, i); err != nil {
				return err
			}
			for _, it := range c.items {
				if _, err := tx.Exec(ctx, `INSERT INTO banquet.menu_items (id, property_id, menu_id, category_id, name, station) VALUES ($1,$2,$3,$4,$5,'buffet')`,
					id.New(), property, menu, cid, it); err != nil {
					return err
				}
			}
		}
	} else if err != nil {
		return err
	}
	pkgs := []struct {
		code, name, category, method, price, mode string
		minPax, included, hours                   int
		inclusions                                string
	}{
		{"WEDDING", "Wedding Package", "wedding", "fixed", "88000000", "nett", 0, 320, 5,
			`[{"kind":"service","label":"Buffet 300 pax + VIP family 20 pax"},{"kind":"service","label":"2 food stalls"},` +
				`{"kind":"resource","label":"Bungalow suite (1 night)","resourceType":"bungalow","nights":1},{"kind":"service","label":"Food tasting"}]`},
		{"INTIMATE", "Intimate Wedding", "wedding", "fixed", "28000000", "nett", 0, 100, 4, `[{"kind":"service","label":"Food tasting"}]`},
		{"PREWED", "Pre-wedding", "wedding", "fixed", "3800000", "nett", 0, 0, 4, `[]`},
		{"BIRTHDAY", "Birthday", "birthday", "fixed", "18000000", "nett", 0, 50, 4, `[]`},
		{"KIDS-BDAY", "Kids Birthday", "birthday", "fixed", "14500000", "nett", 0, 40, 3, `[]`},
		{"SOCIAL", "Social Event", "social", "per_pax", "190000", "plus_plus", 30, 0, 4, `[]`},
		{"MICE-FD", "Full Day Meeting", "mice", "per_pax_per_day", "450000", "plus_plus", 20, 0, 8, `[{"kind":"service","label":"2 coffee breaks + lunch"}]`},
	}
	for _, p := range pkgs {
		var menuID *uuid.UUID
		if p.category == "wedding" && p.code == "WEDDING" {
			menuID = &menu
		}
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.packages (id, property_id, code, name, category, pricing_method, price, pricing_mode, min_pax, included_pax,
			extra_pax_price, extra_pax_mode, duration_hours, menu_id, inclusions) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,190000,'plus_plus',$11,$12,$13::jsonb)
			ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, p.code, p.name, p.category, p.method, p.price, p.mode, p.minPax, p.included,
			p.hours, menuID, p.inclusions); err != nil {
			return err
		}
	}
	for _, c := range [][5]string{{"CORKAGE", "Corkage (per bottle)", "corkage", "bottle", "150000"}, {"OVERTIME", "Venue overtime", "overtime", "hour", "2500000"},
		{"FOODSTALL", "Additional food stall", "additional_fnb", "item", "3500000"}} {
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.charge_types (id, property_id, code, name, kind, unit, unit_price, pricing_mode, revenue_component)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,'nett',$8) ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, c[0], c[1], c[2], c[3], c[4],
			map[string]string{"corkage": "corkage", "overtime": "venue_rental", "additional_fnb": "banquet_fnb"}[c[2]]); err != nil {
			return err
		}
	}
	var tpl uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM banquet.checklist_templates WHERE property_id = $1 AND code = 'WEDDING'`, property).Scan(&tpl)
	if dbtx.IsNoRows(err) {
		tpl = id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.checklist_templates (id, property_id, code, name, event_type_id) SELECT $1, $2, 'WEDDING',
			'Wedding checklist', id FROM banquet.event_types WHERE property_id = $2 AND code = 'WEDDING'`, tpl, property); err != nil {
			return err
		}
		for i, t := range []struct {
			task, dept string
			days       int
		}{{"Food tasting with the couple", "kitchen", 30}, {"Technical meeting", "banquet", 14}, {"Confirm final pax", "sales", 7},
			{"Decoration & sound check", "venue", 1}, {"Generator / electricity check", "engineering", 1}} {
			if _, err := tx.Exec(ctx, `INSERT INTO banquet.checklist_template_items (id, property_id, template_id, task, department, days_before, sort_order)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`, id.New(), property, tpl, t.task, t.dept, t.days, i); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	return nil
}
