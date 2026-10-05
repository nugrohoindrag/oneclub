package banquet

// Event Billing (PRD P3 FR-EVT-08, FR-BQT-01..12): the event folio in
// Billing (business line banquet) carries the package per pax ++ / nett,
// additional pax, menu extras, venue add-on, corkage, electricity, overtime
// and vendor fees, priced with the Tax & Service rules of Commercial (an
// immutable pricing snapshot per charge); the payment schedule (DP, terms,
// final payment) and the final bill with deposits applied.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// Charge is a charge of the event (one folio line).
type EventCharge struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	EventID          uuid.UUID  `json:"eventId" db:"event_id"`
	Source           string     `json:"source" db:"source" enum:"package,extra_pax,menu,venue_addon,extra,vendor,resource,quotation,adjustment"`
	ChargeTypeID     *uuid.UUID `json:"chargeTypeId" db:"charge_type_id"`
	Kind             *string    `json:"kind" db:"kind"`
	RefID            *uuid.UUID `json:"refId" db:"ref_id"`
	ProductID        *uuid.UUID `json:"productId" db:"product_id"`
	Description      string     `json:"description" db:"description"`
	Quantity         string     `json:"quantity" db:"quantity"`
	UnitPrice        string     `json:"unitPrice" db:"unit_price"`
	PricingMode      string     `json:"pricingMode" db:"pricing_mode" enum:"nett,plus_plus"`
	Net              string     `json:"net" db:"net_amount"`
	Service          string     `json:"service" db:"service_amount"`
	Tax              string     `json:"tax" db:"tax_amount"`
	Total            string     `json:"total" db:"total"`
	RevenueComponent string     `json:"revenueComponent" db:"revenue_component"`
	FolioLineID      *uuid.UUID `json:"folioLineId" db:"folio_line_id"`
	SnapshotID       *uuid.UUID `json:"snapshotId" db:"snapshot_id"`
	Status           string     `json:"status" db:"status" enum:"posted,voided"`
	VoidReason       *string    `json:"voidReason" db:"void_reason"`
	CreatedAt        time.Time  `json:"createdAt" db:"created_at"`
}

const chargeSelect = `SELECT id, event_id, source, charge_type_id, kind, ref_id, product_id, description, trim_scale(quantity)::text AS quantity,
	trim_scale(unit_price)::text AS unit_price, pricing_mode, trim_scale(net_amount)::text AS net_amount, trim_scale(service_amount)::text AS service_amount,
	trim_scale(tax_amount)::text AS tax_amount, trim_scale(total)::text AS total, revenue_component, folio_line_id, snapshot_id, status, void_reason,
	created_at FROM banquet.event_charges`

func (m *Module) charges(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventCharge, error) {
	return handle.List[EventCharge](q.Query(ctx, chargeSelect+` WHERE event_id = $1 ORDER BY created_at, id`, eid))
}

// ensureFolio opens the event folio (source banquet_event) and links it to
// the payer's customer folio (unified folio, FR-BIL-P3-01).
func (m *Module) ensureFolio(ctx context.Context, tx pgx.Tx, e BanquetEvent) (uuid.UUID, error) {
	if e.FolioID != nil {
		return *e.FolioID, nil
	}
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: e.PropertyID, CustomerID: e.CustomerID,
		HolderName: e.holder(), SourceType: "banquet_event", SourceID: &e.ID, SourceRef: e.Number}, BusinessLine: billing.LineBanquet,
		CorporateName: deref(e.CorporateName)})
	if err != nil {
		return uuid.Nil, err
	}
	switch {
	case e.CorporateAccountID != nil:
		_, err = m.Billing.AttachFolio(ctx, tx, e.PropertyID, f.ID, billing.CustomerFolioInput{CorporateAccountID: e.CorporateAccountID})
	case e.CustomerID != nil:
		_, err = m.Billing.AttachFolio(ctx, tx, e.PropertyID, f.ID, billing.CustomerFolioInput{CustomerID: e.CustomerID})
	}
	if err != nil {
		return uuid.Nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE banquet.events SET folio_id = $2 WHERE id = $1`, e.ID, f.ID)
	return f.ID, err
}

func billingPaid(ctx context.Context, q dbtx.Querier, folioID uuid.UUID) (decimal.Decimal, decimal.Decimal, error) {
	return billing.PaidTowards(ctx, q, folioID)
}

type presetAmounts struct{ Net, Service, Tax, Total decimal.Decimal }

// chargeSpec is a charge to post on the event folio.
type chargeSpec struct {
	Source, Kind string
	ChargeTypeID *uuid.UUID
	RefID        *uuid.UUID
	ProductID    *uuid.UUID
	Description  string
	Quantity     decimal.Decimal
	UnitPrice    decimal.Decimal
	Mode         string
	TaxCodes     []string
	Component    string
	Preset       *presetAmounts // amounts already priced (quotation lines, fees)
}

// postCharge prices a charge (units × unit price, Nett or ++, Tax & Service
// of Commercial with a pricing snapshot) and posts it to the event folio.
func (m *Module) postCharge(ctx context.Context, tx pgx.Tx, eid uuid.UUID, c chargeSpec) (EventCharge, error) {
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return EventCharge{}, err
	}
	folio, err := m.ensureFolio(ctx, tx, e)
	if err != nil {
		return EventCharge{}, err
	}
	if st, err := billing.FolioStatus(ctx, tx, folio); err != nil {
		return EventCharge{}, err
	} else if st != "open" {
		return EventCharge{}, errs.Conflict("folio_closed", "the event folio is closed (final billing done)")
	}
	if c.Quantity.IsZero() {
		c.Quantity = decimal.NewFromInt(1)
	}
	if c.Mode != "nett" {
		c.Mode = "plus_plus"
	}
	if c.Component == "" {
		c.Component = "other"
	}
	var net, svc, tax, total decimal.Decimal
	var snap *uuid.UUID
	var lines any
	if p := c.Preset; p != nil {
		net, svc, tax, total = p.Net, p.Service, p.Tax, p.Total
	} else {
		item := c.Kind
		if item == "" {
			item = c.Source
		}
		sid, b, err := commercial.Pricer{}.ManualSnapshot(ctx, tx, e.PropertyID, "banquet", item, "", c.Quantity, c.UnitPrice, c.Mode, c.TaxCodes, clock.Now(), nil)
		if err != nil {
			return EventCharge{}, err
		}
		net, svc, tax, total = dec(b.NetAmount), commercial.SumKind(b.Lines, "service"), commercial.SumKind(b.Lines, "tax"), dec(b.Total)
		snap, lines = &sid, b.Lines
	}
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_charges (id, property_id, event_id, source, charge_type_id, kind, ref_id, product_id, description,
		quantity, unit_price, pricing_mode, net_amount, service_amount, tax_amount, total, revenue_component, snapshot_id, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11::numeric,$12,$13::numeric,$14::numeric,$15::numeric,$16::numeric,$17,$18,$19,$19)`,
		cid, e.PropertyID, eid, c.Source, c.ChargeTypeID, nzs(c.Kind), c.RefID, c.ProductID, c.Description, c.Quantity.String(), c.UnitPrice.String(),
		c.Mode, net.String(), svc.String(), tax.String(), total.String(), c.Component, snap, actor(ctx)); err != nil {
		return EventCharge{}, err
	}
	lid, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folio, Description: c.Description,
		Quantity: c.Quantity, UnitPrice: c.UnitPrice, Net: net, Service: svc, Tax: tax, Total: total, SnapshotID: snap,
		ReferenceType: "banquet.event_charge", ReferenceID: &cid}, BusinessLine: billing.LineBanquet, RevenueComponent: c.Component, TaxLines: lines})
	if err != nil {
		return EventCharge{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_charges SET folio_line_id = $2 WHERE id = $1`, cid, lid); err != nil {
		return EventCharge{}, err
	}
	if err := m.refreshTotals(ctx, tx, eid); err != nil {
		return EventCharge{}, err
	}
	rows, err := tx.Query(ctx, chargeSelect+` WHERE id = $1`, cid)
	return handle.One[EventCharge](rows, err, "charge")
}

// refreshTotals keeps the contract total (posted charges without the
// cancellation fee) and the version of the event.
func (m *Module) refreshTotals(ctx context.Context, tx pgx.Tx, eid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE banquet.events SET contract_total = coalesce((SELECT sum(total) FROM banquet.event_charges
		WHERE event_id = $1 AND status = 'posted' AND coalesce(kind, '') <> 'cancellation'), 0), version = version + 1, updated_by = $2 WHERE id = $1`,
		eid, actor(ctx))
	return err
}

func (m *Module) voidCharge(ctx context.Context, tx pgx.Tx, c EventCharge, reason string) error {
	if c.Status != "posted" {
		return nil
	}
	if c.FolioLineID != nil {
		if err := m.Billing.VoidCharge(ctx, tx, *c.FolioLineID, reason); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE banquet.event_charges SET status = 'voided', void_reason = $2, voided_at = now(), updated_by = $3 WHERE id = $1`,
		c.ID, nzs(reason), actor(ctx))
	return err
}

// voidCharges voids the posted charges of a source (and reference).
func (m *Module) voidCharges(ctx context.Context, tx pgx.Tx, eid uuid.UUID, source string, ref *uuid.UUID, reason string) error {
	list, err := handle.List[EventCharge](tx.Query(ctx, chargeSelect+` WHERE event_id = $1 AND status = 'posted' AND ($2 = '' OR source = $2)
		AND ($3::uuid IS NULL OR ref_id = $3)`, eid, source, ref))
	if err != nil || len(list) == 0 {
		return err
	}
	for _, c := range list {
		if err := m.voidCharge(ctx, tx, c, reason); err != nil {
			return err
		}
	}
	return m.refreshTotals(ctx, tx, eid)
}

// venueAddon posts the add-on of an outdoor venue (FR-BQT-05).
func (m *Module) venueAddon(ctx context.Context, tx pgx.Tx, eid, holdID uuid.UUID, v venueRow) error {
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	pol, _, err := chargesPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return err
	}
	_, err = m.postCharge(ctx, tx, eid, chargeSpec{Source: "venue_addon", Kind: "outdoor_venue", RefID: &holdID, Description: "Venue add-on · " + v.Name,
		Quantity: decimal.NewFromInt(1), UnitPrice: v.AddonPrice, Mode: pol.PricingMode, TaxCodes: pol.TaxCodes, Component: "outdoor_venue"})
	return err
}

// ── package & pax ─────────────────────────────────────────────────────────

type packageRow struct {
	ID              uuid.UUID
	Code, Name      string
	Category        string
	Method          string
	Price           decimal.Decimal
	Mode            string
	TaxCodes        []string
	MinPax          int
	IncludedPax     int
	ExtraPaxPrice   decimal.Decimal
	ExtraPaxMode    string
	DurationHours   int
	ElectricityWatt int
	MenuID          *uuid.UUID
	Component       string
	Status          string
}

func packageByID(ctx context.Context, q dbtx.Querier, property, pid uuid.UUID) (packageRow, error) {
	var p packageRow
	var price, extra string
	err := q.QueryRow(ctx, `SELECT id, code, name, category, pricing_method, price::text, pricing_mode, tax_codes, min_pax, included_pax, extra_pax_price::text,
		extra_pax_mode, duration_hours, electricity_watt, menu_id, revenue_component, status FROM banquet.packages
		WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, pid, property).
		Scan(&p.ID, &p.Code, &p.Name, &p.Category, &p.Method, &price, &p.Mode, &p.TaxCodes, &p.MinPax, &p.IncludedPax, &extra, &p.ExtraPaxMode, &p.DurationHours,
			&p.ElectricityWatt, &p.MenuID, &p.Component, &p.Status)
	if dbtx.IsNoRows(err) {
		return p, handle.Invalid("packageId", "not_found", "banquet package not found")
	}
	p.Price, p.ExtraPaxPrice = dec(price), dec(extra)
	return p, err
}

func eventDays(e BanquetEvent) int {
	d := int(e.End.Sub(e.Start).Hours()/24 + 0.999)
	return max(d, 1)
}

// applyPackage prices the package for pax: per pax ++ with the minimum pax
// (Social Event 25 pax billed 30), nett package with the pax it includes
// plus additional pax, or per pax per day (MICE).
func (m *Module) applyPackage(ctx context.Context, tx pgx.Tx, e BanquetEvent, packageID uuid.UUID, pax, days int) ([]EventCharge, error) {
	p, err := packageByID(ctx, tx, e.PropertyID, packageID)
	if err != nil {
		return nil, err
	}
	if p.Status != "active" {
		return nil, handle.Invalid("packageId", "inactive", "the package is inactive")
	}
	if pax <= 0 {
		return nil, handle.Invalid("pax", "required", "pax is required to price the package")
	}
	if err := m.voidCharges(ctx, tx, e.ID, "package", nil, "package re-priced"); err != nil {
		return nil, err
	}
	if err := m.voidCharges(ctx, tx, e.ID, "extra_pax", nil, "package re-priced"); err != nil {
		return nil, err
	}
	var out []EventCharge
	post := func(c chargeSpec) error {
		c.RefID = &p.ID
		ch, err := m.postCharge(ctx, tx, e.ID, c)
		out = append(out, ch)
		return err
	}
	charged := pax
	switch p.Method {
	case "fixed":
		if err := post(chargeSpec{Source: "package", Kind: "package", Description: p.Name, Quantity: decimal.NewFromInt(1), UnitPrice: p.Price,
			Mode: p.Mode, TaxCodes: p.TaxCodes, Component: p.Component}); err != nil {
			return out, err
		}
		if p.IncludedPax > 0 {
			charged = max(pax, p.IncludedPax)
			if extra := pax - p.IncludedPax; extra > 0 && p.ExtraPaxPrice.IsPositive() {
				if err := post(chargeSpec{Source: "extra_pax", Kind: "extra_pax", Description: fmt.Sprintf("Additional pax · %d pax", extra),
					Quantity: decimal.NewFromInt(int64(extra)), UnitPrice: p.ExtraPaxPrice, Mode: p.ExtraPaxMode, TaxCodes: p.TaxCodes,
					Component: "banquet_fnb"}); err != nil {
					return out, err
				}
			}
		}
	case "per_pax_per_day":
		if days <= 0 {
			days = eventDays(e)
		}
		units := max(pax, p.MinPax)
		charged = units
		desc := fmt.Sprintf("%s · %d pax × %d day(s)", p.Name, units, days)
		if units > pax {
			desc += fmt.Sprintf(" (minimum %d pax)", p.MinPax)
		}
		if err := post(chargeSpec{Source: "package", Kind: "package", Description: desc, Quantity: decimal.NewFromInt(int64(units * days)),
			UnitPrice: p.Price, Mode: p.Mode, TaxCodes: p.TaxCodes, Component: p.Component}); err != nil {
			return out, err
		}
	default: // per_pax
		units := max(pax, p.MinPax)
		charged = units
		desc := fmt.Sprintf("%s · %d pax", p.Name, units)
		if units > pax {
			desc += fmt.Sprintf(" (minimum %d pax)", p.MinPax)
		}
		if err := post(chargeSpec{Source: "package", Kind: "package", Description: desc, Quantity: decimal.NewFromInt(int64(units)), UnitPrice: p.Price,
			Mode: p.Mode, TaxCodes: p.TaxCodes, Component: p.Component}); err != nil {
			return out, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET package_id = $2, charged_pax = $3 WHERE id = $1`, e.ID, p.ID, charged); err != nil {
		return out, err
	}
	return out, m.applyInclusions(ctx, tx, e.ID, p.ID)
}

// chargeExtraPax charges the pax above the charged pax (after the cut-off
// or at completion, FR-BQT-10): the additional pax price of a nett
// package, otherwise the per pax price.
func (m *Module) chargeExtraPax(ctx context.Context, tx pgx.Tx, e BanquetEvent, pax int) error {
	if e.PackageID == nil || pax <= e.ChargedPax {
		return nil
	}
	p, err := packageByID(ctx, tx, e.PropertyID, *e.PackageID)
	if err != nil {
		return err
	}
	extra := pax - e.ChargedPax
	unit, mode := p.Price, p.Mode
	qty := decimal.NewFromInt(int64(extra))
	switch p.Method {
	case "fixed":
		unit, mode = p.ExtraPaxPrice, p.ExtraPaxMode
	case "per_pax_per_day":
		qty = qty.Mul(decimal.NewFromInt(int64(eventDays(e))))
	}
	if unit.IsPositive() {
		if _, err := m.postCharge(ctx, tx, e.ID, chargeSpec{Source: "extra_pax", Kind: "extra_pax", RefID: &p.ID,
			Description: fmt.Sprintf("Additional pax · %d pax", extra), Quantity: qty, UnitPrice: unit, Mode: mode, TaxCodes: p.TaxCodes,
			Component: "banquet_fnb"}); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE banquet.events SET charged_pax = $2 WHERE id = $1`, e.ID, pax)
	return err
}

// PackageInput prices the event with a banquet package.
type EventPackageInput struct {
	PackageID uuid.UUID `json:"packageId"`
	Pax       int       `json:"pax,omitempty" doc:"Default: guaranteed, else expected pax"`
	Days      int       `json:"days,omitempty" doc:"Per pax per day packages: default the event days"`
}

// SetPackage selects (or re-prices) the package of an event.
func (m *Module) SetPackage(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventPackageInput) (EventDetail, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if !e.open() {
		return EventDetail{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot be re-priced")
	}
	pax := in.Pax
	if pax <= 0 {
		pax = e.paxBasis()
	}
	if _, err := m.applyPackage(ctx, tx, e, in.PackageID, pax, in.Days); err != nil {
		return EventDetail{}, err
	}
	after, err := m.Get(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "set_package", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number, PropertyID: &e.PropertyID, Before: e, After: after, Metadata: map[string]any{"pax": pax}}); err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, eid)
}

// GuaranteeInput sets the guaranteed pax.
type GuaranteedPaxInput struct {
	Pax    int    `json:"pax"`
	Reason string `json:"reason,omitempty"`
}

// GuaranteePax records the guaranteed pax: before the cut-off the package
// is re-priced (a decrease of at most the policy share), after it only
// increases are accepted and charged (FR-BQT-10).
func (m *Module) GuaranteePax(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in GuaranteedPaxInput) (EventDetail, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if !e.open() {
		return EventDetail{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot change its pax")
	}
	if in.Pax <= 0 {
		return EventDetail{}, handle.Invalid("pax", "required", "pax must be positive")
	}
	pol, _, err := bookingPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return EventDetail{}, err
	}
	loc := calendar.Location(ctx, tx)
	locked := false
	if e.PaxDeadline != nil {
		if d, err := time.ParseInLocation("2006-01-02", *e.PaxDeadline, loc); err == nil && localDay(clock.Now(), loc).After(d) {
			locked = true
		}
	}
	if e.GuaranteedPax != nil && in.Pax < *e.GuaranteedPax {
		if locked {
			return EventDetail{}, errs.Validation("pax_locked", "the final pax is locked since "+*e.PaxDeadline+"; it can only increase",
				errs.Field("pax", "pax_locked", "the final pax cut-off has passed"))
		}
		floor := decimal.NewFromInt(int64(*e.GuaranteedPax)).Mul(decimal.NewFromInt(100).Sub(dec(pol.MaxPaxDecreasePercent))).
			Div(decimal.NewFromInt(100)).Ceil().IntPart()
		if int64(in.Pax) < floor {
			return EventDetail{}, errs.Validation("below_allowed_decrease", fmt.Sprintf("the guaranteed pax may decrease to %d at most (%s%% of %d)",
				floor, pol.MaxPaxDecreasePercent, *e.GuaranteedPax), errs.Field("pax", "below_allowed_decrease", fmt.Sprintf("minimum %d", floor)))
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET guaranteed_pax = $2 WHERE id = $1`, eid, in.Pax); err != nil {
		return EventDetail{}, err
	}
	if e.PackageID != nil {
		if locked {
			if err := m.chargeExtraPax(ctx, tx, e, in.Pax); err != nil {
				return EventDetail{}, err
			}
		} else if _, err := m.applyPackage(ctx, tx, e, *e.PackageID, in.Pax, 0); err != nil {
			return EventDetail{}, err
		}
	}
	if err := m.repriceMenus(ctx, tx, eid); err != nil {
		return EventDetail{}, err
	}
	if err := m.touch(ctx, tx, eid); err != nil {
		return EventDetail{}, err
	}
	after, err := m.Get(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "guarantee_pax", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number, PropertyID: &e.PropertyID, Before: e, After: after, Reason: in.Reason}); err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, eid)
}

// ── menu selection (FR-BQT-02/03) ─────────────────────────────────────────

// MenuSelectionInput selects the dishes of one menu.
type MenuSelectionInput struct {
	MenuID  uuid.UUID   `json:"menuId"`
	ItemIDs []uuid.UUID `json:"itemIds"`
	Notes   string      `json:"notes,omitempty"`
}

// MenuSelection is the selection of one menu with the category quotas.
type EventMenuSelection struct {
	MenuID     uuid.UUID            `json:"menuId"`
	MenuName   string               `json:"menuName"`
	MenuType   string               `json:"menuType"`
	Categories []MenuCategoryChoice `json:"categories"`
	Items      []SelectedMenuItem   `json:"items"`
}

// CategoryChoice is the quota use of a menu category.
type MenuCategoryChoice struct {
	CategoryID uuid.UUID `json:"categoryId"`
	Name       string    `json:"name"`
	Quota      int       `json:"quota"`
	Selected   int       `json:"selected"`
	Extra      int       `json:"extra" doc:"Choices above the quota (charged per pax)"`
}

// SelectedMenuItem is one selected dish.
type SelectedMenuItem struct {
	MenuItemID    uuid.UUID  `json:"menuItemId" db:"menu_item_id"`
	MenuID        uuid.UUID  `json:"menuId" db:"menu_id"`
	Name          string     `json:"name" db:"name"`
	CategoryID    *uuid.UUID `json:"categoryId" db:"category_id"`
	Category      *string    `json:"category" db:"category"`
	Course        *string    `json:"course" db:"course"`
	Station       *string    `json:"station" db:"station"`
	PortionPerPax string     `json:"portionPerPax" db:"portion_per_pax"`
	ProductID     *uuid.UUID `json:"productId" db:"product_id"`
	RecipeID      *uuid.UUID `json:"recipeId" db:"recipe_id"`
	ServeOffset   int        `json:"serveOffsetMinutes" db:"serve_offset_minutes"`
	Allergens     []string   `json:"allergens" db:"allergens"`
	Notes         *string    `json:"notes" db:"notes"`
}

const selectedSelect = `SELECT s.menu_item_id, s.menu_id, i.name, i.category_id, c.name AS category, i.course, i.station,
	trim_scale(i.portion_per_pax)::text AS portion_per_pax,
	i.product_id, i.recipe_id, i.serve_offset_minutes, i.allergens, s.notes
	FROM banquet.event_menu_items s JOIN banquet.menu_items i ON i.id = s.menu_item_id LEFT JOIN banquet.menu_categories c ON c.id = i.category_id`

func (m *Module) selectedItems(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]SelectedMenuItem, error) {
	return handle.List[SelectedMenuItem](q.Query(ctx, selectedSelect+` WHERE s.event_id = $1 ORDER BY s.menu_id, c.sort_order NULLS LAST, i.name`, eid))
}

type menuRow struct {
	ID          uuid.UUID
	Name, Type  string
	PricePerPax decimal.Decimal
	Mode        string
	TaxCodes    []string
	OutletID    *uuid.UUID
	Status      string
}

func menuByID(ctx context.Context, q dbtx.Querier, property, mid uuid.UUID) (menuRow, error) {
	var mr menuRow
	var price string
	err := q.QueryRow(ctx, `SELECT id, name, menu_type, price_per_pax::text, pricing_mode, tax_codes, outlet_id, status FROM banquet.menus
		WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, mid, property).Scan(&mr.ID, &mr.Name, &mr.Type, &price, &mr.Mode, &mr.TaxCodes, &mr.OutletID, &mr.Status)
	if dbtx.IsNoRows(err) {
		return mr, handle.Invalid("menuId", "not_found", "menu not found")
	}
	mr.PricePerPax = dec(price)
	return mr, err
}

type categoryRow struct {
	ID         uuid.UUID
	Name       string
	Quota      int
	ExtraPrice *decimal.Decimal
}

func menuCategories(ctx context.Context, q dbtx.Querier, mid uuid.UUID) ([]categoryRow, error) {
	rows, err := q.Query(ctx, `SELECT id, name, quota, extra_choice_price::text FROM banquet.menu_categories WHERE menu_id = $1 ORDER BY sort_order, name`, mid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []categoryRow
	for rows.Next() {
		var c categoryRow
		var extra *string
		if err := rows.Scan(&c.ID, &c.Name, &c.Quota, &extra); err != nil {
			return nil, err
		}
		if extra != nil {
			d := dec(*extra)
			c.ExtraPrice = &d
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SelectMenu stores the dishes of one menu: choices above a category quota
// are refused, or charged per pax when the category has an extra choice
// price; an add-on menu (food stall, coffee break) is charged per pax.
func (m *Module) SelectMenu(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in MenuSelectionInput) (EventDetail, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventDetail{}, err
	}
	if !e.open() {
		return EventDetail{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot change its menu")
	}
	mr, err := menuByID(ctx, tx, e.PropertyID, in.MenuID)
	if err != nil {
		return EventDetail{}, err
	}
	if mr.Status != "active" {
		return EventDetail{}, handle.Invalid("menuId", "inactive", "the menu is inactive")
	}
	if len(in.ItemIDs) == 0 {
		return EventDetail{}, handle.Invalid("itemIds", "required", "select at least one dish")
	}
	rows, err := tx.Query(ctx, `SELECT id, category_id FROM banquet.menu_items WHERE id = ANY($1) AND menu_id = $2 AND status = 'active' AND archived_at IS NULL`,
		in.ItemIDs, in.MenuID)
	if err != nil {
		return EventDetail{}, err
	}
	count := map[uuid.UUID]int{}
	found := map[uuid.UUID]bool{}
	for rows.Next() {
		var iid uuid.UUID
		var cat *uuid.UUID
		if err := rows.Scan(&iid, &cat); err != nil {
			rows.Close()
			return EventDetail{}, err
		}
		found[iid] = true
		if cat != nil {
			count[*cat]++
		}
	}
	rows.Close()
	for i, iid := range in.ItemIDs {
		if !found[iid] {
			return EventDetail{}, handle.Invalid(fmt.Sprintf("itemIds[%d]", i), "not_found", "the dish is not on this menu")
		}
	}
	cats, err := menuCategories(ctx, tx, in.MenuID)
	if err != nil {
		return EventDetail{}, err
	}
	extraPerPax := decimal.Zero
	extraChoices := 0
	var fields []errs.FieldError
	for _, c := range cats {
		n := count[c.ID]
		if n <= c.Quota {
			continue
		}
		if c.ExtraPrice == nil {
			fields = append(fields, errs.Field("itemIds", "quota_exceeded", fmt.Sprintf("%s: at most %d choice(s), %d selected", c.Name, c.Quota, n)))
			continue
		}
		extraPerPax = extraPerPax.Add(c.ExtraPrice.Mul(decimal.NewFromInt(int64(n - c.Quota))))
		extraChoices += n - c.Quota
	}
	if len(fields) > 0 {
		return EventDetail{}, errs.Validation("quota_exceeded", "the selection exceeds the menu quota", fields...)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM banquet.event_menu_items WHERE event_id = $1 AND menu_id = $2`, eid, in.MenuID); err != nil {
		return EventDetail{}, err
	}
	for _, iid := range in.ItemIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_menu_items (id, property_id, event_id, menu_id, menu_item_id, notes, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (event_id, menu_item_id) DO NOTHING`, id.New(), e.PropertyID, eid, in.MenuID, iid, nzs(in.Notes), actor(ctx)); err != nil {
			return EventDetail{}, err
		}
	}
	if err := m.priceMenu(ctx, tx, e, mr, extraPerPax, extraChoices); err != nil {
		return EventDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "menu_selection", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number + " · " + mr.Name, PropertyID: &e.PropertyID, After: map[string]any{"menuId": in.MenuID, "items": in.ItemIDs,
			"extraChoices": extraChoices}}); err != nil {
		return EventDetail{}, err
	}
	if err := m.touch(ctx, tx, eid); err != nil {
		return EventDetail{}, err
	}
	return m.Detail(ctx, tx, eid)
}

// priceMenu (re)posts the per pax charges of a selected menu.
func (m *Module) priceMenu(ctx context.Context, tx pgx.Tx, e BanquetEvent, mr menuRow, extraPerPax decimal.Decimal, extraChoices int) error {
	if err := m.voidCharges(ctx, tx, e.ID, "menu", &mr.ID, "menu selection changed"); err != nil {
		return err
	}
	pax := e.paxBasis()
	if pax <= 0 {
		return nil
	}
	packageMenu := false
	if e.PackageID != nil {
		var pm *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT menu_id FROM banquet.packages WHERE id = $1`, *e.PackageID).Scan(&pm)
		packageMenu = pm != nil && *pm == mr.ID
	}
	if mr.PricePerPax.IsPositive() && !packageMenu {
		if _, err := m.postCharge(ctx, tx, e.ID, chargeSpec{Source: "menu", Kind: "menu", RefID: &mr.ID, Description: fmt.Sprintf("%s · %d pax", mr.Name, pax),
			Quantity: decimal.NewFromInt(int64(pax)), UnitPrice: mr.PricePerPax, Mode: mr.Mode, TaxCodes: mr.TaxCodes, Component: "banquet_fnb"}); err != nil {
			return err
		}
	}
	if extraPerPax.IsPositive() {
		if _, err := m.postCharge(ctx, tx, e.ID, chargeSpec{Source: "menu", Kind: "extra_choice", RefID: &mr.ID,
			Description: fmt.Sprintf("%s · %d extra choice(s) · %d pax", mr.Name, extraChoices, pax), Quantity: decimal.NewFromInt(int64(pax)),
			UnitPrice: extraPerPax, Mode: mr.Mode, TaxCodes: mr.TaxCodes, Component: "banquet_fnb"}); err != nil {
			return err
		}
	}
	return nil
}

// repriceMenus re-prices the selected menus for the current pax.
func (m *Module) repriceMenus(ctx context.Context, tx pgx.Tx, eid uuid.UUID) error {
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT menu_id FROM banquet.event_menu_items WHERE event_id = $1`, eid)
	if err != nil {
		return err
	}
	menus, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, mid := range menus {
		mr, err := menuByID(ctx, tx, e.PropertyID, mid)
		if err != nil {
			return err
		}
		cats, err := menuCategories(ctx, tx, mid)
		if err != nil {
			return err
		}
		extra, n := decimal.Zero, 0
		for _, c := range cats {
			var sel int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM banquet.event_menu_items s JOIN banquet.menu_items i ON i.id = s.menu_item_id
				WHERE s.event_id = $1 AND i.category_id = $2`, eid, c.ID).Scan(&sel); err != nil {
				return err
			}
			if sel > c.Quota && c.ExtraPrice != nil {
				extra = extra.Add(c.ExtraPrice.Mul(decimal.NewFromInt(int64(sel - c.Quota))))
				n += sel - c.Quota
			}
		}
		if err := m.priceMenu(ctx, tx, e, mr, extra, n); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) menuSelections(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventMenuSelection, error) {
	items, err := m.selectedItems(ctx, q, eid)
	if err != nil {
		return nil, err
	}
	var out []EventMenuSelection
	idx := map[uuid.UUID]int{}
	for _, it := range items {
		i, ok := idx[it.MenuID]
		if !ok {
			var name, typ string
			if err := q.QueryRow(ctx, `SELECT name, menu_type FROM banquet.menus WHERE id = $1`, it.MenuID).Scan(&name, &typ); err != nil {
				return nil, err
			}
			cats, err := menuCategories(ctx, q, it.MenuID)
			if err != nil {
				return nil, err
			}
			sel := EventMenuSelection{MenuID: it.MenuID, MenuName: name, MenuType: typ, Categories: []MenuCategoryChoice{}}
			for _, c := range cats {
				sel.Categories = append(sel.Categories, MenuCategoryChoice{CategoryID: c.ID, Name: c.Name, Quota: c.Quota})
			}
			out = append(out, sel)
			i = len(out) - 1
			idx[it.MenuID] = i
		}
		out[i].Items = append(out[i].Items, it)
		if it.CategoryID != nil {
			for j := range out[i].Categories {
				if out[i].Categories[j].CategoryID == *it.CategoryID {
					out[i].Categories[j].Selected++
					out[i].Categories[j].Extra = max(0, out[i].Categories[j].Selected-out[i].Categories[j].Quota)
				}
			}
		}
	}
	if out == nil {
		out = []EventMenuSelection{}
	}
	return out, nil
}

// ── extra charges ─────────────────────────────────────────────────────────

// ChargeInput adds an extra charge (corkage, overtime, electricity,
// decoration, AV, damage, additional F&B …).
type EventChargeInput struct {
	ChargeTypeID *uuid.UUID `json:"chargeTypeId,omitempty"`
	Kind         string     `json:"kind,omitempty" enum:"corkage,outdoor_venue,electricity,overtime,decoration,av_equipment,vendor_fee,additional_fnb,venue_rental,damage,other"`
	Description  string     `json:"description,omitempty"`
	Quantity     string     `json:"quantity,omitempty" doc:"Default 1; electricity: kW above the included quota"`
	UnitPrice    string     `json:"unitPrice,omitempty" doc:"Default: the extra charge price or the Banquet Policies fee"`
	ProductID    *uuid.UUID `json:"productId,omitempty" doc:"Additional F&B product (its recipe joins the requirement, K1)"`
	VendorID     *uuid.UUID `json:"vendorId,omitempty" doc:"Corkage of outside food: the partner vendor"`
	OutsideFood  bool       `json:"outsideFood,omitempty" doc:"Corkage for outside food (partner vendors only)"`
}

// includedWatt is the electricity included by the held venues and package.
func (m *Module) includedWatt(ctx context.Context, q dbtx.Querier, e BanquetEvent, pol ChargesPolicy) (int, error) {
	var venues int
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN v.electricity_watt > 0 THEN v.electricity_watt WHEN v.venue_type = 'ballroom' THEN $2 ELSE 0 END), 0)
		FROM banquet.event_venues h JOIN banquet.venues v ON v.id = h.venue_id WHERE h.event_id = $1 AND h.status IN ('tentative', 'definite', 'completed')`,
		e.ID, pol.ElectricityIncludedWatt).Scan(&venues); err != nil {
		return 0, err
	}
	pkg := 0
	if e.PackageID != nil {
		_ = q.QueryRow(ctx, `SELECT electricity_watt FROM banquet.packages WHERE id = $1`, *e.PackageID).Scan(&pkg)
	}
	return venues + pkg, nil
}

// AddCharge posts an extra charge to the event folio.
func (m *Module) AddCharge(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventChargeInput) (EventCharge, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventCharge{}, err
	}
	if e.Status == StatusCancelled {
		return EventCharge{}, errs.Conflict("event_closed", "the event is cancelled")
	}
	pol, _, err := chargesPolicy(ctx, tx, e.PropertyID)
	if err != nil {
		return EventCharge{}, err
	}
	spec := chargeSpec{Source: "extra", Kind: in.Kind, ProductID: in.ProductID, Mode: pol.PricingMode, TaxCodes: pol.TaxCodes}
	name := strings.ReplaceAll(in.Kind, "_", " ")
	if in.ChargeTypeID != nil {
		var price string
		var comp *string
		var status string
		if err := tx.QueryRow(ctx, `SELECT name, kind, unit_price::text, pricing_mode, tax_codes, revenue_component, status FROM banquet.charge_types
			WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, *in.ChargeTypeID, e.PropertyID).
			Scan(&name, &spec.Kind, &price, &spec.Mode, &spec.TaxCodes, &comp, &status); err != nil {
			if dbtx.IsNoRows(err) {
				return EventCharge{}, handle.Invalid("chargeTypeId", "not_found", "extra charge not found")
			}
			return EventCharge{}, err
		}
		if status != "active" {
			return EventCharge{}, handle.Invalid("chargeTypeId", "inactive", "the extra charge is inactive")
		}
		spec.ChargeTypeID, spec.UnitPrice, spec.Component = in.ChargeTypeID, dec(price), deref(comp)
	}
	if spec.Kind == "" {
		return EventCharge{}, handle.Invalid("kind", "required", "choose an extra charge or a kind")
	}
	if !contains(ChargeKinds, spec.Kind) {
		return EventCharge{}, handle.Invalid("kind", "invalid", "unknown kind")
	}
	qty, err := handle.Decimal("quantity", in.Quantity, decimal.Zero)
	if err != nil {
		return EventCharge{}, err
	}
	if qty.IsNegative() {
		return EventCharge{}, handle.Invalid("quantity", "invalid", "quantity must be positive")
	}
	if spec.UnitPrice.IsZero() {
		switch spec.Kind {
		case "corkage":
			spec.UnitPrice = dec(pol.CorkageFeePerBottle)
		case "overtime":
			spec.UnitPrice = dec(pol.OvertimeFeePerHour)
		case "electricity":
			spec.UnitPrice = dec(pol.ElectricityRatePerKw)
		}
	}
	if in.UnitPrice != "" {
		if spec.UnitPrice, err = handle.Decimal("unitPrice", in.UnitPrice, decimal.Zero); err != nil {
			return EventCharge{}, err
		}
	}
	if spec.Kind == "electricity" && qty.IsZero() {
		included, err := m.includedWatt(ctx, tx, e, pol)
		if err != nil {
			return EventCharge{}, err
		}
		excess := e.PowerWatt - included
		if excess <= 0 {
			return EventCharge{}, errs.Validation("within_quota", fmt.Sprintf("%d W needed is within the %d W included", e.PowerWatt, included))
		}
		qty = decimal.NewFromInt(int64(excess)).Div(decimal.NewFromInt(1000))
		name = fmt.Sprintf("Electricity above the quota · %d W", excess)
	}
	if qty.IsZero() {
		qty = decimal.NewFromInt(1)
	}
	if spec.Kind == "corkage" && in.OutsideFood && pol.OutsideFoodPartnerOnly {
		if in.VendorID == nil {
			return EventCharge{}, handle.Invalid("vendorId", "required", "outside food is allowed from partner vendors only")
		}
		var partner bool
		if err := tx.QueryRow(ctx, `SELECT partner FROM banquet.vendors WHERE id = $1 AND property_id = $2`, *in.VendorID, e.PropertyID).Scan(&partner); err != nil {
			if dbtx.IsNoRows(err) {
				return EventCharge{}, handle.Invalid("vendorId", "not_found", "vendor not found")
			}
			return EventCharge{}, err
		}
		if !partner {
			return EventCharge{}, handle.Invalid("vendorId", "not_partner", "outside food is allowed from partner vendors only")
		}
	}
	if in.ProductID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.products WHERE id = $1 AND property_id = $2)`, *in.ProductID, e.PropertyID).Scan(&ok); err != nil {
			return EventCharge{}, err
		}
		if !ok {
			return EventCharge{}, handle.Invalid("productId", "not_found", "product not found")
		}
	}
	if !spec.UnitPrice.IsPositive() {
		return EventCharge{}, handle.Invalid("unitPrice", "required", "a positive unit price is required")
	}
	if spec.Component == "" {
		spec.Component = componentOfKind(spec.Kind)
	}
	spec.Quantity = qty
	spec.RefID = in.VendorID
	spec.Description = strings.TrimSpace(in.Description)
	if spec.Description == "" {
		spec.Description = strings.ToUpper(name[:1]) + name[1:]
		if !qty.Equal(decimal.NewFromInt(1)) {
			spec.Description += " × " + qty.String()
		}
	}
	c, err := m.postCharge(ctx, tx, eid, spec)
	if err != nil {
		return c, err
	}
	return c, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "add_charge", EntityType: "banquet.event_charge", EntityID: c.ID.String(),
		EntityLabel: e.Number + " · " + c.Description, PropertyID: &e.PropertyID, After: c})
}

// VoidChargeInput voids a charge.
type EventChargeVoid struct {
	Reason string `json:"reason"`
}

// VoidEventCharge voids a charge of an event (folio still open).
func (m *Module) VoidEventCharge(ctx context.Context, tx pgx.Tx, cid uuid.UUID, reason string) (EventCharge, error) {
	if err := handle.Required("reason", reason); err != nil {
		return EventCharge{}, err
	}
	rows, err := tx.Query(ctx, chargeSelect+` WHERE id = $1 FOR UPDATE`, cid)
	c, err := handle.One[EventCharge](rows, err, "charge")
	if err != nil {
		return c, err
	}
	if c.Status != "posted" {
		return c, errs.Conflict("already_voided", "the charge is already voided")
	}
	e, err := m.lock(ctx, tx, c.EventID)
	if err != nil {
		return c, err
	}
	if err := m.voidCharge(ctx, tx, c, reason); err != nil {
		return c, err
	}
	if err := m.refreshTotals(ctx, tx, c.EventID); err != nil {
		return c, err
	}
	rows, err = tx.Query(ctx, chargeSelect+` WHERE id = $1`, cid)
	after, err := handle.One[EventCharge](rows, err, "charge")
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionVoid, EntityType: "banquet.event_charge", EntityID: cid.String(),
		EntityLabel: e.Number + " · " + c.Description, PropertyID: &e.PropertyID, Before: c, After: after, Reason: reason})
}

// ── payment schedule (FR-BQT-08) ──────────────────────────────────────────

// ScheduleRequest builds the payment schedule of an event.
type EventScheduleRequest struct {
	Lines []billing.ScheduleLineInput `json:"lines,omitempty" doc:"Default: Banquet Policies — DP, second term, final payment H-7"`
}

// defaultTerms are the payment terms of Banquet Policies for a total.
func defaultTerms(pol BookingPolicy, today, eventDay time.Time, option *time.Time) []billing.ScheduleLineInput {
	dpDue := today.AddDate(0, 0, max(pol.DownPaymentDueDays, 0))
	if option != nil {
		if o := localDay(*option, today.Location()); o.Before(dpDue) && !o.Before(today) {
			dpDue = o
		}
	}
	if dpDue.After(eventDay) {
		dpDue = eventDay
	}
	pct := dec(pol.MinDownPaymentPercent)
	lines := []billing.ScheduleLineInput{{Label: "Down Payment " + pct.String() + "%", Kind: "down_payment", Percent: pct.String(),
		DueDate: dpDue.Format("2006-01-02")}}
	last := dpDue
	if p2 := dec(pol.SecondTermPercent); p2.IsPositive() && pol.SecondTermDaysBefore > 0 {
		if d := eventDay.AddDate(0, 0, -pol.SecondTermDaysBefore); d.After(dpDue) {
			lines = append(lines, billing.ScheduleLineInput{Label: "Second Payment " + p2.String() + "%", Kind: "installment", Percent: p2.String(),
				DueDate: d.Format("2006-01-02")})
			last = d
		}
	}
	final := eventDay.AddDate(0, 0, -pol.FinalPaymentDaysBefore)
	if final.Before(last) {
		final = last
	}
	lines = append(lines, billing.ScheduleLineInput{Label: "Final Payment", Kind: "final", DueDate: final.Format("2006-01-02")})
	if len(lines) == 2 && dpDue.Equal(final) && pct.Equal(decimal.NewFromInt(100)) {
		lines = lines[:1]
	}
	return lines
}

// BuildSchedule creates the payment schedule of the event on its folio
// (deposits until the event; reminders by Billing, D-14 / D-8 …).
func (m *Module) BuildSchedule(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventScheduleRequest) (EventBilling, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventBilling{}, err
	}
	if !e.open() {
		return EventBilling{}, errs.Conflict("event_closed", "a "+e.Status+" event has no payment schedule")
	}
	if err := m.createSchedule(ctx, tx, e, in.Lines); err != nil {
		return EventBilling{}, err
	}
	after, err := m.Get(ctx, tx, eid)
	if err != nil {
		return EventBilling{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "payment_schedule", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number, PropertyID: &e.PropertyID, Before: e, After: after}); err != nil {
		return EventBilling{}, err
	}
	return m.Billing2(ctx, tx, eid)
}

func (m *Module) createSchedule(ctx context.Context, tx pgx.Tx, e BanquetEvent, lines []billing.ScheduleLineInput) error {
	if e.ScheduleID != nil {
		if sc, err := billing.GetSchedule(ctx, tx, *e.ScheduleID); err == nil && sc.Status != "cancelled" {
			return errs.Conflict("schedule_exists", "the event already has payment schedule "+sc.Number)
		}
	}
	total := dec(e.ContractTotal)
	if !total.IsPositive() {
		return errs.Conflict("nothing_to_schedule", "price the event (package or charges) before building the payment schedule")
	}
	folio, err := m.ensureFolio(ctx, tx, e)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		pol, _, err := bookingPolicy(ctx, tx, e.PropertyID)
		if err != nil {
			return err
		}
		loc := calendar.Location(ctx, tx)
		lines = defaultTerms(pol, localDay(clock.Now(), loc), localDay(e.Start, loc), e.OptionDate)
	}
	sc, err := m.Billing.CreateSchedule(ctx, tx, e.PropertyID, billing.ScheduleInput{Title: e.Number + " · " + e.Title, FolioID: &folio,
		CustomerID: e.CustomerID, CorporateAccountID: e.CorporateAccountID, SourceType: "banquet_event", SourceID: &e.ID, SourceRef: e.Number,
		TotalAmount: total.String(), Lines: lines})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE banquet.events SET schedule_id = $2, version = version + 1 WHERE id = $1`, e.ID, sc.ID)
	return err
}

// ── billing view, final bill, cancellation settlement ─────────────────────

// ComponentTotal is the revenue of one component.
type EventComponentTotal struct {
	RevenueComponent string `json:"revenueComponent" db:"revenue_component"`
	Net              string `json:"net" db:"net"`
	Service          string `json:"service" db:"service"`
	Tax              string `json:"tax" db:"tax"`
	Total            string `json:"total" db:"total"`
}

// EventBilling is the Event Billing screen: charges, folio, schedule,
// deposits and the final invoice.
type EventBilling struct {
	EventID             uuid.UUID              `json:"eventId"`
	Number              string                 `json:"number"`
	Status              string                 `json:"status"`
	Currency            string                 `json:"currency"`
	ContractTotal       string                 `json:"contractTotal"`
	Charges             []EventCharge          `json:"charges"`
	ByComponent         []EventComponentTotal  `json:"byComponent"`
	FolioID             *uuid.UUID             `json:"folioId"`
	Folio               *billing.Summary       `json:"folio"`
	FolioStatus         *string                `json:"folioStatus"`
	Received            string                 `json:"received" doc:"Payments and deposits received"`
	Balance             string                 `json:"balance" doc:"Charges − received"`
	DownPaymentRequired string                 `json:"downPaymentRequired"`
	DownPaymentReceived bool                   `json:"downPaymentReceived"`
	Schedule            *billing.Schedule      `json:"schedule"`
	Deposits            []billing.Deposit      `json:"deposits"`
	FinalInvoice        *billing.InvoiceDetail `json:"finalInvoice"`
	FinalBilledAt       *time.Time             `json:"finalBilledAt"`
	SettledAt           *time.Time             `json:"settledAt"`
}

// Billing2 returns the Event Billing view.
func (m *Module) Billing2(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (EventBilling, error) {
	e, err := m.Get(ctx, q, eid)
	if err != nil {
		return EventBilling{}, err
	}
	out := EventBilling{EventID: e.ID, Number: e.Number, Status: e.Status, Currency: e.Currency, ContractTotal: e.ContractTotal, FolioID: e.FolioID,
		FinalBilledAt: e.FinalBilledAt, SettledAt: e.SettledAt, Received: "0", Balance: "0", Deposits: []billing.Deposit{}}
	if out.Charges, err = m.charges(ctx, q, eid); err != nil {
		return out, err
	}
	if out.ByComponent, err = handle.List[EventComponentTotal](q.Query(ctx, `SELECT revenue_component, trim_scale(sum(net_amount))::text AS net,
		trim_scale(sum(service_amount))::text AS service, trim_scale(sum(tax_amount))::text AS tax, trim_scale(sum(total))::text AS total
		FROM banquet.event_charges WHERE event_id = $1 AND status = 'posted' GROUP BY revenue_component ORDER BY revenue_component`, eid)); err != nil {
		return out, err
	}
	required, received, err := m.depositStatus(ctx, q, e)
	if err != nil {
		return out, err
	}
	out.DownPaymentRequired = required.String()
	out.DownPaymentReceived = required.IsPositive() && received.GreaterThanOrEqual(required)
	if e.FolioID != nil {
		f, err := billing.GetFolio(ctx, q, *e.FolioID)
		if err != nil {
			return out, err
		}
		out.Folio, out.FolioStatus, out.Deposits = &f.Summary, &f.Status, f.Deposits
		out.Received = received.String()
		out.Balance = dec(f.Summary.Charges).Sub(received).String()
	}
	if e.ScheduleID != nil {
		sc, err := billing.GetSchedule(ctx, q, *e.ScheduleID)
		if err != nil {
			return out, err
		}
		out.Schedule = &sc
	}
	if e.FinalInvoiceID != nil {
		inv, err := billing.GetInvoice(ctx, q, *e.FinalInvoiceID, "")
		if err != nil {
			return out, err
		}
		out.FinalInvoice = &inv
	}
	return out, nil
}

// refundLeftovers refunds the part of the deposits that was not applied to
// the folio (overpaid down payment, refundable share on cancellation).
func (m *Module) refundLeftovers(ctx context.Context, tx pgx.Tx, folio uuid.UUID, reason string) (decimal.Decimal, error) {
	f, err := billing.GetFolio(ctx, tx, folio)
	if err != nil {
		return decimal.Zero, err
	}
	total := decimal.Zero
	for _, d := range f.Deposits {
		if d.Status == "held" || d.Status == "refunded" {
			continue
		}
		left := dec(d.Amount).Sub(dec(d.AppliedAmount))
		if !left.IsPositive() {
			continue
		}
		var refundable string
		if err := tx.QueryRow(ctx, `SELECT (amount - refunded_amount)::text FROM billing.payments WHERE id = $1`, d.PaymentID).Scan(&refundable); err != nil {
			return total, err
		}
		left = decimal.Min(left, dec(refundable))
		if !left.IsPositive() {
			continue
		}
		if _, err := m.Billing.RequestRefund(ctx, tx, d.PaymentID, left, "", reason, nil); err != nil {
			return total, err
		}
		total = total.Add(left)
	}
	return total, nil
}

// settleCancellation voids the charges, keeps the forfeited fee from the
// deposits and refunds the rest; the folio is closed when settled.
func (m *Module) settleCancellation(ctx context.Context, tx pgx.Tx, e BanquetEvent, fee decimal.Decimal, reason string) (decimal.Decimal, error) {
	folio := *e.FolioID
	st, err := billing.FolioStatus(ctx, tx, folio)
	if err != nil || st != "open" {
		return decimal.Zero, err
	}
	list, err := m.charges(ctx, tx, e.ID)
	if err != nil {
		return decimal.Zero, err
	}
	for _, c := range list {
		if err := m.voidCharge(ctx, tx, c, "event cancelled: "+reason); err != nil {
			return decimal.Zero, err
		}
	}
	lines, err := billing.LinesOf(ctx, tx, folio)
	if err != nil {
		return decimal.Zero, err
	}
	for _, l := range lines {
		if err := m.Billing.VoidCharge(ctx, tx, l.ID, "event cancelled: "+reason); err != nil {
			return decimal.Zero, err
		}
	}
	if fee.IsPositive() {
		if _, err := m.postCharge(ctx, tx, e.ID, chargeSpec{Source: "adjustment", Kind: "cancellation", Description: "Cancellation fee (forfeited deposit)",
			Quantity: decimal.NewFromInt(1), UnitPrice: fee, Mode: "nett", Component: "cancellation_fee",
			Preset: &presetAmounts{Net: fee, Total: fee}}); err != nil {
			return decimal.Zero, err
		}
	}
	if err := m.Billing.ApplyDeposits(ctx, tx, folio); err != nil {
		return decimal.Zero, err
	}
	refunded, err := m.refundLeftovers(ctx, tx, folio, "event cancelled: "+reason)
	if err != nil {
		return refunded, err
	}
	refs, err := m.Billing.RefundFolio(ctx, tx, folio, "event cancelled: "+reason, nil)
	if err != nil {
		return refunded, err
	}
	for _, r := range refs {
		refunded = refunded.Add(dec(r.Amount))
	}
	return refunded, m.tryClose(ctx, tx, folio)
}

// tryClose closes a settled folio (a pending online payment keeps it open).
func (m *Module) tryClose(ctx context.Context, tx pgx.Tx, folio uuid.UUID) error {
	sum, err := billing.FolioSummary(ctx, tx, folio)
	if err != nil {
		return err
	}
	if dec(sum.Balance).IsPositive() {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := m.Billing.CloseFolio(ctx, sp, folio); err != nil {
		_ = sp.Rollback(ctx)
		if de, ok := errs.As(err); ok && de.Kind == errs.KindConflict {
			return nil
		}
		return err
	}
	return sp.Commit(ctx)
}

// PaymentInput settles the balance of the final bill.
type EventPaymentInput struct {
	MethodType string `json:"methodType" enum:"cash,bank_transfer,card,qris,virtual_account"`
	Amount     string `json:"amount,omitempty" doc:"Default: the balance"`
	Reference  string `json:"reference,omitempty"`
}

// FinalBillInput produces the final bill of a completed event.
type EventFinalBillInput struct {
	Invoice   bool               `json:"invoice,omitempty" doc:"Issue the final invoice (always for company events)"`
	TermsDays *int               `json:"termsDays,omitempty" doc:"Invoice terms (default Credit Policies for companies)"`
	Payment   *EventPaymentInput `json:"payment,omitempty" doc:"Settle the balance now"`
}

// FinalBill is the result of the final billing (FR-BQT-12).
type EventFinalBill struct {
	Billing  EventBilling     `json:"billing"`
	Charges  string           `json:"charges"`
	Applied  string           `json:"depositsApplied"`
	Refunded string           `json:"refunded"`
	Due      string           `json:"due" doc:"Balance left after deposits, before the invoice / payment"`
	Payment  *billing.Payment `json:"payment"`
	Closed   bool             `json:"folioClosed"`
}

// FinalBilling applies the DP and terms paid (deposits) to the event folio,
// refunds an overpayment and settles the rest by payment or by the final
// invoice (company events: the corporate account with its terms).
func (m *Module) FinalBilling(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventFinalBillInput) (EventFinalBill, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventFinalBill{}, err
	}
	if e.Status != StatusCompleted {
		return EventFinalBill{}, errs.Conflict("not_completed", "complete the event (final pax) before the final billing")
	}
	if e.FolioID == nil {
		return EventFinalBill{}, errs.Conflict("nothing_to_bill", "the event has no charges")
	}
	folio := *e.FolioID
	if st, err := billing.FolioStatus(ctx, tx, folio); err != nil {
		return EventFinalBill{}, err
	} else if st != "open" {
		return EventFinalBill{}, errs.Conflict("already_billed", "the event folio is already closed")
	}
	_, held, err := billingPaid(ctx, tx, folio)
	if err != nil {
		return EventFinalBill{}, err
	}
	if err := m.Billing.ApplyDeposits(ctx, tx, folio); err != nil {
		return EventFinalBill{}, err
	}
	out := EventFinalBill{}
	refunded, err := m.refundLeftovers(ctx, tx, folio, "final billing: overpaid deposit")
	if err != nil {
		return out, err
	}
	refs, err := m.Billing.RefundFolio(ctx, tx, folio, "final billing: overpayment", nil)
	if err != nil {
		return out, err
	}
	for _, r := range refs {
		refunded = refunded.Add(dec(r.Amount))
	}
	sum, err := billing.FolioSummary(ctx, tx, folio)
	if err != nil {
		return out, err
	}
	out.Charges, out.Refunded = sum.Charges, refunded.String()
	out.Applied = decimal.Min(held, dec(sum.Charges)).String()
	due := dec(sum.Balance)
	out.Due = due.String()
	var invoiceID *uuid.UUID
	switch {
	case !due.IsPositive():
	case in.Payment != nil:
		amt, err := handle.Decimal("payment.amount", in.Payment.Amount, due)
		if err != nil {
			return out, err
		}
		if !amt.IsPositive() || amt.GreaterThan(due) {
			return out, handle.Invalid("payment.amount", "invalid", "between 0 and the balance")
		}
		method := in.Payment.MethodType
		if method == "" {
			method = "cash"
		}
		p, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: &folio, MethodType: method, Amount: amt, Reference: in.Payment.Reference,
			Description: "Final payment " + e.Number})
		if err != nil {
			return out, err
		}
		out.Payment = &p
	case in.Invoice || e.CorporateAccountID != nil:
		inv, err := m.Invoices.CreateInvoice(ctx, tx, e.PropertyID, billing.InvoiceInput{FolioID: &folio, Kind: "final", CorporateAccountID: e.CorporateAccountID,
			TermsDays: in.TermsDays, Notes: "Final billing " + e.Number + " · " + e.Title, Issue: true})
		if err != nil {
			return out, err
		}
		invoiceID = &inv.ID
	}
	if err := m.tryClose(ctx, tx, folio); err != nil {
		return out, err
	}
	st, err := billing.FolioStatus(ctx, tx, folio)
	if err != nil {
		return out, err
	}
	out.Closed = st == "closed"
	settled := out.Closed && invoiceID == nil
	if e.ScheduleID != nil && (out.Closed || invoiceID != nil) {
		// the remaining terms are settled by the final bill (or owed on the final invoice)
		sc, err := billing.GetSchedule(ctx, tx, *e.ScheduleID)
		if err != nil {
			return out, err
		}
		if sc.Status == "active" {
			if _, err := m.Billing.CancelSchedule(ctx, tx, *e.ScheduleID, "settled by the final billing of "+e.Number); err != nil {
				return out, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET final_invoice_id = coalesce($2, final_invoice_id),
		final_billed_at = CASE WHEN $3 THEN coalesce(final_billed_at, now()) ELSE final_billed_at END,
		settled_at = CASE WHEN $4 THEN coalesce(settled_at, now()) ELSE settled_at END, version = version + 1 WHERE id = $1`,
		eid, invoiceID, out.Closed || invoiceID != nil, settled); err != nil {
		return out, err
	}
	if out.Billing, err = m.Billing2(ctx, tx, eid); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "final_bill", EntityType: "banquet.event", EntityID: eid.String(),
		EntityLabel: e.Number + " · " + e.Title, PropertyID: &e.PropertyID, After: map[string]any{"charges": out.Charges, "due": out.Due,
			"refunded": out.Refunded, "invoiceId": invoiceID, "closed": out.Closed}})
}

// canWaive reports whether the caller may waive a cancellation fee.
func canWaive(ctx context.Context, property uuid.UUID) bool {
	p := authz.From(ctx)
	return p != nil && p.Can("banquet.billing.manage", &property)
}
