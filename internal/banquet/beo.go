package banquet

// Banquet Event Order (PRD P3 EP-14, Naming Convention §11): the function
// sheet generated from the event (run-of-show, venues & setup, menu and
// portions from the final / guaranteed pax, extras, vendors, electricity,
// special requests), issued and revised in versions (changes marked,
// ETag / If-Match), distributed to departments with read confirmation,
// turned into the Kitchen "Banquet Production" list, and its menu BOM per
// pax published to P4 as the procurement requirement (contract K1).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/inventory"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
)

// Requirement is one ingredient line of the procurement requirement (K1)
// or of the consumption (K6), in the item's base UOM.
type BEORequirement struct {
	ItemID   uuid.UUID  `json:"itemId"`
	ItemCode string     `json:"itemCode"`
	ItemName string     `json:"itemName"`
	Quantity string     `json:"quantity"`
	UOMID    uuid.UUID  `json:"uomId"`
	UOM      string     `json:"uom"`
	NeededBy string     `json:"neededBy,omitempty"`
	Source   string     `json:"source" enum:"menu,extra"`
	RecipeID *uuid.UUID `json:"recipeId"`
}

// BEOContent is the function sheet.
type BEOContent struct {
	Event          BEOEvent       `json:"event"`
	Pax            BEOPax         `json:"pax"`
	Venues         []BEOVenue     `json:"venues"`
	Schedule       []BEOSchedule  `json:"schedule"`
	Menus          []BEOMenu      `json:"menus"`
	Extras         []BEOExtra     `json:"extras"`
	Vendors        []BEOVendor    `json:"vendors"`
	Resources      []BEOResource  `json:"resources"`
	Inclusions     []string       `json:"inclusions" doc:"Package inclusions (vouchers, services) delivered with the event"`
	Electricity    BEOElectricity `json:"electricity"`
	MeetingChanges []string       `json:"meetingChanges" doc:"Changes agreed at food tasting / technical meeting"`
	Payments       []BEOPayment   `json:"payments" doc:"Payment schedule with due dates (FR-BQT-08)"`
}

// BEOPayment is a due amount of the event's payment schedule.
type BEOPayment struct {
	Label   string `json:"label"`
	DueDate string `json:"dueDate"`
	Amount  string `json:"amount"`
	Paid    string `json:"paid"`
	Status  string `json:"status"`
}

type BEOEvent struct {
	Number          string    `json:"number"`
	Title           string    `json:"title"`
	EventType       string    `json:"eventType"`
	Category        string    `json:"category"`
	Status          string    `json:"status"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Customer        string    `json:"customer"`
	Contact         string    `json:"contact"`
	ContactPhone    string    `json:"contactPhone"`
	SalesOwner      string    `json:"salesOwner"`
	Layout          string    `json:"layout"`
	SpecialRequests string    `json:"specialRequests"`
}

type BEOPax struct {
	Expected   int `json:"expected"`
	Guaranteed int `json:"guaranteed"`
	Final      int `json:"final"`
	Basis      int `json:"basis" doc:"Pax the kitchen produces for"`
}

type BEOVenue struct {
	Venue    string    `json:"venue"`
	Function string    `json:"function"`
	Layout   string    `json:"layout"`
	Pax      int       `json:"pax"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Status   string    `json:"status"`
}

type BEOSchedule struct {
	Seq        int        `json:"seq"`
	Start      time.Time  `json:"start"`
	End        *time.Time `json:"end"`
	Title      string     `json:"title"`
	Venue      string     `json:"venue"`
	Owner      string     `json:"owner"`
	Department string     `json:"department"`
	Notes      string     `json:"notes"`
}

type BEOMenu struct {
	MenuID uuid.UUID     `json:"menuId"`
	Menu   string        `json:"menu"`
	Type   string        `json:"type"`
	Items  []BEOMenuItem `json:"items"`
}

type BEOMenuItem struct {
	MenuItemID uuid.UUID `json:"menuItemId"`
	Name       string    `json:"name"`
	Category   string    `json:"category"`
	Course     string    `json:"course"`
	Station    string    `json:"station" doc:"F&B station (buffet, food stall, kitchen)"`
	Portions   string    `json:"portions"`
	ServeAt    time.Time `json:"serveAt"`
	Allergens  []string  `json:"allergens"`
	Notes      string    `json:"notes"`
}

type BEOExtra struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
}

type BEOVendor struct {
	Vendor    string     `json:"vendor"`
	Type      string     `json:"type"`
	Service   string     `json:"service"`
	ArrivalAt *time.Time `json:"arrivalAt"`
	Contact   string     `json:"contact"`
}

type BEOResource struct {
	Resource string    `json:"resource"`
	Quantity int       `json:"quantity"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
}

type BEOElectricity struct {
	RequiredWatt int `json:"requiredWatt"`
	IncludedWatt int `json:"includedWatt"`
}

// BEOChange marks what changed against the previous version (FR-BEO-02).
type BEOChange struct {
	Section string `json:"section"`
	Item    string `json:"item"`
	Change  string `json:"change" enum:"added,removed,changed"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

// BEODepartment is the distribution to one department (FR-BEO-03).
type BEODepartment struct {
	Department     string     `json:"department" db:"department"`
	Instructions   *string    `json:"instructions" db:"instructions"`
	NotifiedAt     *time.Time `json:"notifiedAt" db:"notified_at"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt" db:"acknowledged_at" doc:"Empty: the department has not confirmed this version"`
	AcknowledgedBy *string    `json:"acknowledgedBy" db:"acknowledged_by"`
}

// BEO is one version of a Banquet Event Order.
type BEO struct {
	ID             uuid.UUID         `json:"id" db:"id"`
	EventID        uuid.UUID         `json:"eventId" db:"event_id"`
	EventNumber    string            `json:"eventNumber" db:"event_number"`
	EventTitle     string            `json:"eventTitle" db:"event_title"`
	Number         string            `json:"number" db:"number"`
	Version        int               `json:"version" db:"version"`
	Status         string            `json:"status" db:"status" enum:"draft,issued,superseded"`
	Pax            int               `json:"pax" db:"pax"`
	EventDate      string            `json:"eventDate" db:"event_date"`
	OutletID       *uuid.UUID        `json:"outletId" db:"outlet_id"`
	Content        BEOContent        `json:"content" db:"content"`
	Instructions   map[string]string `json:"instructions" db:"instructions" doc:"Instructions per department"`
	Notes          *string           `json:"notes" db:"notes"`
	Requirements   []BEORequirement  `json:"requirements" db:"requirements"`
	Changes        []BEOChange       `json:"changes" db:"changes"`
	RevisionReason *string           `json:"revisionReason" db:"revision_reason"`
	SupersedesID   *uuid.UUID        `json:"supersedesId" db:"supersedes_id"`
	IssuedAt       *time.Time        `json:"issuedAt" db:"issued_at"`
	SupersededAt   *time.Time        `json:"supersededAt" db:"superseded_at"`
	LockedAt       *time.Time        `json:"lockedAt" db:"locked_at" doc:"Set when the event is completed (FR-BEO-06)"`
	Rev            int               `json:"rev" db:"rev" doc:"ETag value for If-Match"`
	CreatedAt      time.Time         `json:"createdAt" db:"created_at"`
	Departments    []BEODepartment   `json:"departments" db:"-"`
}

const beoSelect = `SELECT b.id, b.event_id, e.number AS event_number, e.title AS event_title, b.number, b.version, b.status, b.pax,
	to_char(b.event_date, 'YYYY-MM-DD') AS event_date, b.outlet_id, b.content, b.instructions, b.notes, b.requirements, b.changes, b.revision_reason,
	b.supersedes_id, b.issued_at, b.superseded_at, b.locked_at, b.rev, b.created_at
	FROM banquet.beos b JOIN banquet.events e ON e.id = b.event_id`

// GetBEO returns a BEO version with its distribution.
func (m *Module) GetBEO(ctx context.Context, q dbtx.Querier, bid uuid.UUID) (BEO, error) {
	rows, err := q.Query(ctx, beoSelect+` WHERE b.id = $1`, bid)
	b, err := handle.One[BEO](rows, err, "BEO")
	if err != nil {
		return b, err
	}
	b.Departments, err = handle.List[BEODepartment](q.Query(ctx, `SELECT d.department, d.instructions, d.notified_at, d.acknowledged_at,
		u.full_name AS acknowledged_by FROM banquet.beo_departments d LEFT JOIN platform.users u ON u.id = d.acknowledged_by
		WHERE d.beo_id = $1 ORDER BY d.department`, bid))
	if b.Instructions == nil {
		b.Instructions = map[string]string{}
	}
	return b, err
}

func (m *Module) lockBEO(ctx context.Context, tx pgx.Tx, bid uuid.UUID) (BEO, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM banquet.beos WHERE id = $1 FOR UPDATE`, bid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return BEO{}, errs.NotFound("BEO")
		}
		return BEO{}, err
	}
	return m.GetBEO(ctx, tx, bid)
}

// ── content & requirements ────────────────────────────────────────────────

func (m *Module) buildContent(ctx context.Context, q dbtx.Querier, e BanquetEvent) (BEOContent, *uuid.UUID, error) {
	loc := calendar.Location(ctx, q)
	_ = loc
	c := BEOContent{Venues: []BEOVenue{}, Schedule: []BEOSchedule{}, Menus: []BEOMenu{}, Extras: []BEOExtra{}, Vendors: []BEOVendor{},
		Resources: []BEOResource{}, Inclusions: []string{}, MeetingChanges: []string{}, Payments: []BEOPayment{}}
	if e.ScheduleID != nil {
		sc, err := billing.GetSchedule(ctx, q, *e.ScheduleID)
		if err != nil {
			return c, nil, err
		}
		for _, l := range sc.Lines {
			c.Payments = append(c.Payments, BEOPayment{Label: l.Label, DueDate: l.DueDate, Amount: l.Amount, Paid: l.PaidAmount, Status: l.Status})
		}
	}
	c.Event = BEOEvent{Number: e.Number, Title: e.Title, EventType: e.EventTypeName, Category: e.Category, Status: e.Status, Start: e.Start, End: e.End,
		Customer: e.holder(), Contact: deref(e.ContactName), ContactPhone: deref(e.ContactPhone), SalesOwner: deref(e.SalesOwnerName),
		Layout: deref(e.Layout), SpecialRequests: deref(e.SpecialRequests)}
	c.Pax = BEOPax{Expected: e.ExpectedPax, Basis: e.paxBasis()}
	if e.GuaranteedPax != nil {
		c.Pax.Guaranteed = *e.GuaranteedPax
	}
	if e.FinalPax != nil {
		c.Pax.Final = *e.FinalPax
	}
	hs, err := m.holds(ctx, q, e.ID)
	if err != nil {
		return c, nil, err
	}
	for _, h := range hs {
		if !activeHold(h.Status) && h.Status != "completed" {
			continue
		}
		pax := 0
		if h.Pax != nil {
			pax = *h.Pax
		}
		c.Venues = append(c.Venues, BEOVenue{Venue: h.VenueName, Function: deref(h.FunctionName), Layout: deref(h.Layout), Pax: pax, Start: h.Start,
			End: h.End, Status: h.Status})
	}
	sched, err := m.schedule(ctx, q, e.ID)
	if err != nil {
		return c, nil, err
	}
	for _, s := range sched {
		c.Schedule = append(c.Schedule, BEOSchedule{Seq: s.Seq, Start: s.Start, End: s.End, Title: s.Title, Venue: deref(s.VenueName),
			Owner: deref(s.OwnerName), Department: deref(s.Department), Notes: deref(s.Notes)})
	}
	sels, err := m.menuSelections(ctx, q, e.ID)
	if err != nil {
		return c, nil, err
	}
	var outlet *uuid.UUID
	pax := decimal.NewFromInt(int64(e.paxBasis()))
	for _, s := range sels {
		mr, err := menuByID(ctx, q, e.PropertyID, s.MenuID)
		if err != nil {
			return c, nil, err
		}
		if outlet == nil {
			outlet = mr.OutletID
		}
		bm := BEOMenu{MenuID: s.MenuID, Menu: s.MenuName, Type: s.MenuType, Items: []BEOMenuItem{}}
		for _, it := range s.Items {
			al := it.Allergens
			if al == nil {
				al = []string{}
			}
			bm.Items = append(bm.Items, BEOMenuItem{MenuItemID: it.MenuItemID, Name: it.Name, Category: deref(it.Category), Course: deref(it.Course),
				Station:  deref(it.Station),
				Portions: pax.Mul(dec(it.PortionPerPax)).Ceil().String(), ServeAt: e.Start.Add(time.Duration(it.ServeOffset) * time.Minute),
				Allergens: al, Notes: deref(it.Notes)})
		}
		c.Menus = append(c.Menus, bm)
	}
	chs, err := m.charges(ctx, q, e.ID)
	if err != nil {
		return c, nil, err
	}
	for _, ch := range chs {
		if ch.Status != "posted" || (ch.Source != "extra" && ch.Source != "vendor") {
			continue
		}
		c.Extras = append(c.Extras, BEOExtra{Kind: deref(ch.Kind), Description: ch.Description, Quantity: ch.Quantity})
	}
	vs, err := m.eventVendors(ctx, q, e.ID)
	if err != nil {
		return c, nil, err
	}
	for _, v := range vs {
		if v.Status != "confirmed" {
			continue
		}
		c.Vendors = append(c.Vendors, BEOVendor{Vendor: v.VendorName, Type: v.VendorType, Service: v.Service, ArrivalAt: v.ArrivalAt,
			Contact: strings.TrimSpace(deref(v.ContactName) + " " + deref(v.Phone))})
	}
	rs, err := m.resources(ctx, q, e.ID)
	if err != nil {
		return c, nil, err
	}
	for _, r := range rs {
		if r.Status == "released" {
			continue
		}
		c.Resources = append(c.Resources, BEOResource{Resource: r.ResourceName, Quantity: r.Quantity, Start: r.Start, End: r.End})
	}
	incs, err := m.inclusions(ctx, q, e.ID)
	if err != nil {
		return c, nil, err
	}
	for _, in := range incs {
		if in.Kind == "resource" || in.Status == "cancelled" {
			continue
		}
		c.Inclusions = append(c.Inclusions, fmt.Sprintf("%s × %d (%s)", in.Label, in.Quantity, in.Status))
	}
	pol, _, err := chargesPolicy(ctx, q, e.PropertyID)
	if err != nil {
		return c, nil, err
	}
	inc, err := m.includedWatt(ctx, q, e, pol)
	if err != nil {
		return c, nil, err
	}
	c.Electricity = BEOElectricity{RequiredWatt: e.PowerWatt, IncludedWatt: inc}
	rows, err := q.Query(ctx, `SELECT kind, coalesce(outcome, ''), changes FROM banquet.event_meetings WHERE event_id = $1 AND status = 'done' ORDER BY scheduled_at`, e.ID)
	if err != nil {
		return c, nil, err
	}
	for rows.Next() {
		var kind, outcome string
		var changes []string
		if err := rows.Scan(&kind, &outcome, &changes); err != nil {
			rows.Close()
			return c, nil, err
		}
		for _, ch := range changes {
			c.MeetingChanges = append(c.MeetingChanges, strings.ReplaceAll(kind, "_", " ")+": "+ch)
		}
	}
	rows.Close()
	return c, outlet, rows.Err()
}

// requirements explodes the recipes of the selected menu items × pax (and
// of additional F&B products) into base-UOM ingredients (K1, K6).
func (m *Module) requirements(ctx context.Context, q dbtx.Querier, e BanquetEvent, pax int, neededBy string) ([]BEORequirement, error) {
	type key struct {
		item   uuid.UUID
		source string
	}
	type acc struct {
		r       inventory.Requirement
		recipes map[uuid.UUID]bool
	}
	merged := map[key]*acc{}
	var order []key
	add := func(source string, recipe uuid.UUID, units decimal.Decimal) error {
		if !units.IsPositive() {
			return nil
		}
		reqs, err := inventory.ExplodeRecipe(ctx, q, recipe, units)
		if err != nil {
			return err
		}
		for _, r := range reqs {
			k := key{r.ItemID, source}
			a, ok := merged[k]
			if !ok {
				a = &acc{r: r, recipes: map[uuid.UUID]bool{}}
				a.r.Quantity = decimal.Zero
				merged[k] = a
				order = append(order, k)
			}
			a.r.Quantity = a.r.Quantity.Add(r.Quantity)
			a.recipes[recipe] = true
		}
		return nil
	}
	items, err := m.selectedItems(ctx, q, e.ID)
	if err != nil {
		return nil, err
	}
	p := decimal.NewFromInt(int64(pax))
	for _, it := range items {
		recipe := it.RecipeID
		if recipe == nil && it.ProductID != nil {
			if recipe, err = inventory.ProductRecipe(ctx, q, *it.ProductID); err != nil {
				return nil, err
			}
		}
		if recipe == nil {
			continue
		}
		if err := add("menu", *recipe, p.Mul(dec(it.PortionPerPax))); err != nil {
			return nil, err
		}
	}
	rows, err := q.Query(ctx, `SELECT product_id, quantity::text FROM banquet.event_charges WHERE event_id = $1 AND status = 'posted' AND product_id IS NOT NULL`, e.ID)
	if err != nil {
		return nil, err
	}
	type extra struct {
		product uuid.UUID
		qty     string
	}
	var extras []extra
	for rows.Next() {
		var x extra
		if err := rows.Scan(&x.product, &x.qty); err != nil {
			rows.Close()
			return nil, err
		}
		extras = append(extras, x)
	}
	rows.Close()
	for _, x := range extras {
		recipe, err := inventory.ProductRecipe(ctx, q, x.product)
		if err != nil {
			return nil, err
		}
		if recipe != nil {
			if err := add("extra", *recipe, dec(x.qty)); err != nil {
				return nil, err
			}
		}
	}
	out := make([]BEORequirement, 0, len(order))
	for _, k := range order {
		a := merged[k]
		var rid *uuid.UUID
		if len(a.recipes) == 1 {
			for r := range a.recipes {
				x := r
				rid = &x
			}
		}
		out = append(out, BEORequirement{ItemID: a.r.ItemID, ItemCode: a.r.ItemCode, ItemName: a.r.ItemName, Quantity: a.r.Quantity.Round(4).String(),
			UOMID: a.r.UOMID, UOM: a.r.UOM, NeededBy: neededBy, Source: k.source, RecipeID: rid})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ItemCode != out[j].ItemCode {
			return out[i].ItemCode < out[j].ItemCode
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

func (m *Module) neededBy(ctx context.Context, q dbtx.Querier, e BanquetEvent) (string, string, error) {
	pol, _, err := bookingPolicy(ctx, q, e.PropertyID)
	if err != nil {
		return "", "", err
	}
	loc := calendar.Location(ctx, q)
	day := localDay(e.Start, loc)
	return day.Format("2006-01-02"), day.AddDate(0, 0, -pol.RequirementLeadDays).Format("2006-01-02"), nil
}

// ProcurementRequirement is the ingredient requirement of an event (K1).
type EventProcurementRequirement struct {
	EventID      uuid.UUID        `json:"eventId"`
	EventNumber  string           `json:"eventNumber"`
	EventDate    string           `json:"eventDate"`
	Pax          int              `json:"pax"`
	BEOID        *uuid.UUID       `json:"beoId" doc:"The issued BEO the list comes from (empty: computed from the current menu)"`
	BEONumber    *string          `json:"beoNumber"`
	Version      *int             `json:"version"`
	OutletID     *uuid.UUID       `json:"outletId"`
	Requirements []BEORequirement `json:"requirements"`
}

// Requirement returns the procurement requirement of the issued BEO, or
// computes it from the current menu and pax.
func (m *Module) Requirement(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (EventProcurementRequirement, error) {
	e, err := m.Get(ctx, q, eid)
	if err != nil {
		return EventProcurementRequirement{}, err
	}
	out := EventProcurementRequirement{EventID: e.ID, EventNumber: e.Number}
	var bid *uuid.UUID
	_ = q.QueryRow(ctx, `SELECT id FROM banquet.beos WHERE event_id = $1 AND status = 'issued' ORDER BY version DESC LIMIT 1`, eid).Scan(&bid)
	if bid != nil {
		b, err := m.GetBEO(ctx, q, *bid)
		if err != nil {
			return out, err
		}
		out.BEOID, out.BEONumber, out.Version, out.OutletID = &b.ID, &b.Number, &b.Version, b.OutletID
		out.EventDate, out.Pax, out.Requirements = b.EventDate, b.Pax, b.Requirements
		return out, nil
	}
	day, need, err := m.neededBy(ctx, q, e)
	if err != nil {
		return out, err
	}
	_, outlet, err := m.buildContent(ctx, q, e)
	if err != nil {
		return out, err
	}
	out.EventDate, out.Pax, out.OutletID = day, e.paxBasis(), outlet
	out.Requirements, err = m.requirements(ctx, q, e, e.paxBasis(), need)
	return out, err
}

// ── diff ──────────────────────────────────────────────────────────────────

type flat struct{ label, value string }

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// flatten turns a BEO into comparable items keyed per section.
func flatten(c BEOContent, instr map[string]string, notes string) map[string]flat {
	out := map[string]flat{}
	put := func(section, key, label string, v any) { out[section+"|"+key] = flat{label: label, value: jsonOf(v)} }
	put("event", "header", c.Event.Title, c.Event)
	put("pax", "pax", "pax", c.Pax)
	for _, v := range c.Venues {
		put("venues", v.Venue+"/"+v.Function, v.Venue, v)
	}
	for _, s := range c.Schedule {
		put("schedule", fmt.Sprint(s.Seq), s.Title, s)
	}
	for _, mn := range c.Menus {
		for _, it := range mn.Items {
			put("menu", it.MenuItemID.String(), mn.Menu+" · "+it.Name, it)
		}
	}
	for i, x := range c.Extras {
		put("extras", fmt.Sprint(i)+"/"+x.Description, x.Description, x)
	}
	for _, v := range c.Vendors {
		put("vendors", v.Vendor+"/"+v.Service, v.Vendor, v)
	}
	for _, r := range c.Resources {
		put("resources", r.Resource, r.Resource, r)
	}
	for _, in := range c.Inclusions {
		put("inclusions", in, in, in)
	}
	put("electricity", "power", "electricity", c.Electricity)
	for d, t := range instr {
		put("instructions", d, d, t)
	}
	if notes != "" {
		put("notes", "notes", "notes", notes)
	}
	return out
}

func diffBEO(old, cur map[string]flat) []BEOChange {
	var keys []string
	for k := range old {
		keys = append(keys, k)
	}
	for k := range cur {
		if _, ok := old[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := []BEOChange{}
	for _, k := range keys {
		section := strings.SplitN(k, "|", 2)[0]
		o, inOld := old[k]
		n, inNew := cur[k]
		switch {
		case inOld && !inNew:
			out = append(out, BEOChange{Section: section, Item: o.label, Change: "removed", From: o.value})
		case !inOld && inNew:
			out = append(out, BEOChange{Section: section, Item: n.label, Change: "added", To: n.value})
		case o.value != n.value:
			out = append(out, BEOChange{Section: section, Item: n.label, Change: "changed", From: o.value, To: n.value})
		}
	}
	return out
}

// ── draft, issue, revise ──────────────────────────────────────────────────

// BEOInput creates a draft BEO (next version) for an event.
type BEOInput struct {
	EventID      uuid.UUID         `json:"eventId"`
	Instructions map[string]string `json:"instructions,omitempty" doc:"Per department: kitchen, fnb_service, venue, engineering, front_desk, golf …"`
	Notes        string            `json:"notes,omitempty" doc:"Special notes (allergy, VIP)"`
}

// BEOPatch edits a draft BEO (If-Match: rev).
type BEOPatch struct {
	Instructions map[string]string `json:"instructions,omitempty"`
	Notes        *string           `json:"notes,omitempty"`
	Refresh      bool              `json:"refresh,omitempty" doc:"Regenerate the function sheet from the event"`
}

// BEORevise issues the next version of an issued BEO.
type BEORevise struct {
	Reason       string            `json:"reason"`
	Instructions map[string]string `json:"instructions,omitempty" doc:"Changed instructions (others are kept)"`
	Notes        *string           `json:"notes,omitempty"`
}

func checkDepartments(instr map[string]string) error {
	for d := range instr {
		if !contains(Departments, d) {
			return handle.Invalid("instructions", "invalid_department", "unknown department "+d)
		}
	}
	return nil
}

// CreateBEO generates a draft BEO from the event (version 1, or the next
// version of an issued BEO).
func (m *Module) CreateBEO(ctx context.Context, tx pgx.Tx, in BEOInput) (BEO, error) {
	e, err := m.lock(ctx, tx, in.EventID)
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return BEO{}, handle.Invalid("eventId", "not_found", "event not found")
		}
		return BEO{}, err
	}
	if !e.open() {
		return BEO{}, errs.Conflict("event_closed", "the BEO of a "+e.Status+" event is locked")
	}
	if err := checkDepartments(in.Instructions); err != nil {
		return BEO{}, err
	}
	var draft int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM banquet.beos WHERE event_id = $1 AND status = 'draft'`, e.ID).Scan(&draft); err != nil {
		return BEO{}, err
	}
	if draft > 0 {
		return BEO{}, errs.Conflict("draft_exists", "the event already has a draft BEO; edit or issue it")
	}
	var number *string
	var version int
	instr := in.Instructions
	notes := in.Notes
	if err := tx.QueryRow(ctx, `SELECT max(number), coalesce(max(version), 0) FROM banquet.beos WHERE event_id = $1`, e.ID).Scan(&number, &version); err != nil {
		return BEO{}, err
	}
	if number == nil {
		loc := calendar.Location(ctx, tx)
		n, err := numbering.Next(ctx, tx, e.PropertyID, "BEO", clock.Now().In(loc))
		if err != nil {
			return BEO{}, err
		}
		number = &n
	} else if instr == nil {
		// next version starts from the instructions of the latest one
		var raw []byte
		var prevNotes *string
		if err := tx.QueryRow(ctx, `SELECT instructions, notes FROM banquet.beos WHERE event_id = $1 ORDER BY version DESC LIMIT 1`, e.ID).Scan(&raw, &prevNotes); err != nil {
			return BEO{}, err
		}
		_ = json.Unmarshal(raw, &instr)
		if notes == "" {
			notes = deref(prevNotes)
		}
	}
	if instr == nil {
		instr = map[string]string{}
	}
	content, outlet, err := m.buildContent(ctx, tx, e)
	if err != nil {
		return BEO{}, err
	}
	day, _, err := m.neededBy(ctx, tx, e)
	if err != nil {
		return BEO{}, err
	}
	bid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.beos (id, property_id, event_id, number, version, status, pax, event_date, outlet_id, content, instructions,
		notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,'draft',$6,$7,$8,$9,$10,$11,$12,$12)`, bid, e.PropertyID, e.ID, *number, version+1,
		e.paxBasis(), day, outlet, jsonOf(content), jsonOf(instr), nzs(notes), actor(ctx)); err != nil {
		return BEO{}, err
	}
	b, err := m.GetBEO(ctx, tx, bid)
	if err != nil {
		return b, err
	}
	return b, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionCreate, EntityType: "banquet.beo", EntityID: bid.String(),
		EntityLabel: fmt.Sprintf("%s v%d · %s", b.Number, b.Version, e.Number), PropertyID: &e.PropertyID, After: b})
}

// UpdateBEO edits a draft BEO.
func (m *Module) UpdateBEO(ctx context.Context, tx pgx.Tx, bid uuid.UUID, in BEOPatch, ifMatch string) (BEO, error) {
	b, err := m.lockBEO(ctx, tx, bid)
	if err != nil {
		return b, err
	}
	if err := checkIfMatch(ifMatch, b.Rev, "BEO"); err != nil {
		return b, err
	}
	if b.Status != "draft" {
		return b, errs.Conflict("not_draft", "issued BEOs are revised, not edited")
	}
	if err := checkDepartments(in.Instructions); err != nil {
		return b, err
	}
	instr := b.Instructions
	for d, t := range in.Instructions {
		if strings.TrimSpace(t) == "" {
			delete(instr, d)
		} else {
			instr[d] = t
		}
	}
	notes := b.Notes
	if in.Notes != nil {
		notes = nzs(*in.Notes)
	}
	content := b.Content
	if in.Refresh {
		e, err := m.Get(ctx, tx, b.EventID)
		if err != nil {
			return b, err
		}
		if content, _, err = m.buildContent(ctx, tx, e); err != nil {
			return b, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.beos SET instructions = $2, notes = $3, content = $4, rev = rev + 1, updated_by = $5 WHERE id = $1`,
		bid, jsonOf(instr), notes, jsonOf(content), actor(ctx)); err != nil {
		return b, err
	}
	after, err := m.GetBEO(ctx, tx, bid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionUpdate, EntityType: "banquet.beo", EntityID: bid.String(),
		EntityLabel: fmt.Sprintf("%s v%d", b.Number, b.Version), PropertyID: nil, Before: b, After: after})
}

// IssueBEO issues a draft BEO: the previous issued version is superseded,
// the changes are marked, departments are notified, the production list is
// rebuilt and the requirement is published (K1).
func (m *Module) IssueBEO(ctx context.Context, tx pgx.Tx, bid uuid.UUID, ifMatch, reason string) (BEO, error) {
	b, err := m.lockBEO(ctx, tx, bid)
	if err != nil {
		return b, err
	}
	if err := checkIfMatch(ifMatch, b.Rev, "BEO"); err != nil {
		return b, err
	}
	if b.Status != "draft" {
		return b, errs.Conflict("not_draft", "only a draft BEO can be issued")
	}
	return m.issue(ctx, tx, b, reason)
}

func (m *Module) issue(ctx context.Context, tx pgx.Tx, b BEO, reason string) (BEO, error) {
	e, err := m.lock(ctx, tx, b.EventID)
	if err != nil {
		return b, err
	}
	if !e.open() {
		return b, errs.Conflict("event_closed", "the BEO of a "+e.Status+" event is locked")
	}
	if e.Status != StatusDefinite {
		return b, errs.Conflict("not_definite", "the BEO is issued once the event is Definite (down payment received)")
	}
	content, outlet, err := m.buildContent(ctx, tx, e)
	if err != nil {
		return b, err
	}
	day, need, err := m.neededBy(ctx, tx, e)
	if err != nil {
		return b, err
	}
	pax := e.paxBasis()
	reqs, err := m.requirements(ctx, tx, e, pax, need)
	if err != nil {
		return b, err
	}
	var prev *BEO
	var pid *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT id FROM banquet.beos WHERE event_id = $1 AND status = 'issued' AND id <> $2 ORDER BY version DESC LIMIT 1`, e.ID, b.ID).Scan(&pid)
	changes := []BEOChange{}
	if pid != nil {
		p, err := m.GetBEO(ctx, tx, *pid)
		if err != nil {
			return b, err
		}
		prev = &p
		changes = diffBEO(flatten(p.Content, p.Instructions, deref(p.Notes)), flatten(content, b.Instructions, deref(b.Notes)))
		if _, err := tx.Exec(ctx, `UPDATE banquet.beos SET status = 'superseded', superseded_at = now(), rev = rev + 1 WHERE id = $1`, p.ID); err != nil {
			return b, err
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.production_items SET status = 'cancelled', updated_at = now() WHERE beo_id = $1 AND status <> 'served'`, p.ID); err != nil {
			return b, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.beos SET status = 'issued', content = $2, pax = $3, event_date = $4, outlet_id = $5, requirements = $6,
		changes = $7, revision_reason = $8, supersedes_id = $9, issued_at = now(), issued_by = $10, rev = rev + 1, updated_by = $10 WHERE id = $1`,
		b.ID, jsonOf(content), pax, day, outlet, jsonOf(reqs), jsonOf(changes), nzs(reason), pid, actor(ctx)); err != nil {
		return b, err
	}
	// distribution: departments with instructions plus the core departments
	depts := map[string]string{"kitchen": "", "fnb_service": "", "venue": "", "engineering": "", "front_desk": ""}
	if e.Category == "sport" || e.Category == "tournament" {
		depts["golf"] = ""
	}
	for d, t := range b.Instructions {
		depts[d] = t
	}
	for d, t := range depts {
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.beo_departments (id, property_id, beo_id, department, instructions, notified_at)
			VALUES ($1,$2,$3,$4,$5,now()) ON CONFLICT (beo_id, department) DO UPDATE SET instructions = EXCLUDED.instructions, notified_at = now()`,
			id.New(), e.PropertyID, b.ID, d, nzs(t)); err != nil {
			return b, err
		}
	}
	if err := m.buildProduction(ctx, tx, e, b.ID, prev, content, outlet); err != nil {
		return b, err
	}
	after, err := m.GetBEO(ctx, tx, b.ID)
	if err != nil {
		return after, err
	}
	evType := EventBEOIssued
	if prev != nil {
		evType = EventBEORevise
	}
	if _, err := m.Events.Publish(ctx, tx, evType, "banquet.beo", &after.ID, &e.PropertyID, map[string]any{"beoId": after.ID, "beoNumber": after.Number,
		"version": after.Version, "eventId": e.ID, "eventNumber": e.Number, "eventDate": day, "pax": pax, "outletId": outlet,
		"requirements": reqs}); err != nil {
		return after, err
	}
	if err := m.notifyDepartments(ctx, tx, e, after, prev != nil); err != nil {
		return after, err
	}
	action := "issue"
	if prev != nil {
		action = "revise"
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: action, EntityType: "banquet.beo", EntityID: after.ID.String(),
		EntityLabel: fmt.Sprintf("%s v%d · %s", after.Number, after.Version, e.Number), PropertyID: &e.PropertyID, Before: b, After: after, Reason: reason,
		Metadata: map[string]any{"changes": len(changes), "requirements": len(reqs)}})
}

// ReviseBEO issues the next version of an issued BEO with the change
// reason (FR-BEO-02: version raised, changed items marked, departments
// must confirm again).
func (m *Module) ReviseBEO(ctx context.Context, tx pgx.Tx, bid uuid.UUID, in BEORevise, ifMatch string) (BEO, error) {
	b, err := m.lockBEO(ctx, tx, bid)
	if err != nil {
		return b, err
	}
	if err := checkIfMatch(ifMatch, b.Rev, "BEO"); err != nil {
		return b, err
	}
	if b.Status != "issued" {
		return b, errs.Conflict("not_issued", "only the issued BEO can be revised")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return b, err
	}
	if err := checkDepartments(in.Instructions); err != nil {
		return b, err
	}
	var draft int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM banquet.beos WHERE event_id = $1 AND status = 'draft'`, b.EventID).Scan(&draft); err != nil {
		return b, err
	}
	if draft > 0 {
		return b, errs.Conflict("draft_exists", "a draft version exists; issue it instead")
	}
	instr := map[string]string{}
	for d, t := range b.Instructions {
		instr[d] = t
	}
	for d, t := range in.Instructions {
		instr[d] = t
	}
	notes := b.Notes
	if in.Notes != nil {
		notes = nzs(*in.Notes)
	}
	nid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.beos (id, property_id, event_id, number, version, status, pax, event_date, outlet_id, content,
		instructions, notes, created_by, updated_by) SELECT $1, property_id, event_id, number, version + 1, 'draft', pax, event_date, outlet_id, content,
		$3, $4, $5, $5 FROM banquet.beos WHERE id = $2`, nid, bid, jsonOf(instr), notes, actor(ctx)); err != nil {
		return b, err
	}
	nb, err := m.GetBEO(ctx, tx, nid)
	if err != nil {
		return nb, err
	}
	return m.issue(ctx, tx, nb, in.Reason)
}

// AckInput confirms a BEO version for a department.
type BEOAckInput struct {
	Department string `json:"department" enum:"banquet,sales,kitchen,fnb_service,venue,engineering,front_desk,golf,finance,security,housekeeping,other"`
}

// AcknowledgeBEO records the read confirmation of a department.
func (m *Module) AcknowledgeBEO(ctx context.Context, tx pgx.Tx, bid uuid.UUID, in BEOAckInput) (BEO, error) {
	b, err := m.lockBEO(ctx, tx, bid)
	if err != nil {
		return b, err
	}
	if b.Status != "issued" {
		return b, errs.Conflict("not_current", "only the issued (current) BEO version is confirmed")
	}
	tag, err := tx.Exec(ctx, `UPDATE banquet.beo_departments SET acknowledged_at = coalesce(acknowledged_at, now()), acknowledged_by = coalesce(acknowledged_by, $3)
		WHERE beo_id = $1 AND department = $2`, bid, in.Department, actor(ctx))
	if err != nil {
		return b, err
	}
	if tag.RowsAffected() == 0 {
		return b, handle.Invalid("department", "not_distributed", "the BEO was not distributed to "+in.Department)
	}
	after, err := m.GetBEO(ctx, tx, bid)
	if err != nil {
		return after, err
	}
	var property uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM banquet.beos WHERE id = $1`, bid).Scan(&property)
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "acknowledge", EntityType: "banquet.beo", EntityID: bid.String(),
		EntityLabel: fmt.Sprintf("%s v%d · %s", b.Number, b.Version, in.Department), PropertyID: &property, Metadata: map[string]any{"department": in.Department}})
}

// notifyDepartments tells the users who confirm BEOs that a version was
// issued or revised.
func (m *Module) notifyDepartments(ctx context.Context, tx pgx.Tx, e BanquetEvent, b BEO, revised bool) error {
	if m.Notify == nil {
		return nil
	}
	users, err := notify.Holders(ctx, tx, e.PropertyID, "banquet.beo.acknowledge")
	if err != nil || len(users) == 0 {
		return err
	}
	loc := calendar.Location(ctx, tx)
	action := "issued"
	if revised {
		action = "revised"
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: "banquet.beo_distributed", Category: "banquet", UserIDs: users, PropertyID: &e.PropertyID,
		Link: "/banquet-event/beo/" + b.ID.String(), Data: map[string]any{"number": b.Number, "version": b.Version, "event": e.Number + " · " + e.Title,
			"date": e.Start.In(loc).Format("02 Jan 2006 15:04"), "action": action, "changes": len(b.Changes)}})
}

// ── Kitchen Banquet Production (FR-BEO-04) ────────────────────────────────

func (m *Module) buildProduction(ctx context.Context, tx pgx.Tx, e BanquetEvent, bid uuid.UUID, prev *BEO, c BEOContent, outlet *uuid.UUID) error {
	carried := map[uuid.UUID]string{}
	if prev != nil {
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (menu_item_id) menu_item_id, status FROM banquet.production_items WHERE beo_id = $1
			AND menu_item_id IS NOT NULL ORDER BY menu_item_id, updated_at DESC`, prev.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var mi uuid.UUID
			var st string
			if err := rows.Scan(&mi, &st); err != nil {
				rows.Close()
				return err
			}
			carried[mi] = st
		}
		rows.Close()
	}
	for _, mn := range c.Menus {
		var mo *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT outlet_id FROM banquet.menus WHERE id = $1`, mn.MenuID).Scan(&mo)
		if mo == nil {
			mo = outlet
		}
		for _, it := range mn.Items {
			st := "pending"
			if s, ok := carried[it.MenuItemID]; ok && s != "cancelled" {
				st = s
			}
			cat := it.Category
			if cat == "" {
				cat = mn.Menu
			}
			if _, err := tx.Exec(ctx, `INSERT INTO banquet.production_items (id, property_id, beo_id, event_id, menu_item_id, name, category, station, quantity,
				serve_at, outlet_id, status) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12)`, id.New(), e.PropertyID, bid, e.ID, it.MenuItemID, it.Name,
				cat, nzs(it.Station), it.Portions, it.ServeAt, mo, st); err != nil {
				return err
			}
		}
	}
	return nil
}

// ProductionItem is one dish to produce for a banquet.
type BanquetProductionItem struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	BEOID       uuid.UUID  `json:"beoId" db:"beo_id"`
	BEONumber   string     `json:"beoNumber" db:"beo_number"`
	BEOVersion  int        `json:"beoVersion" db:"beo_version"`
	EventID     uuid.UUID  `json:"eventId" db:"event_id"`
	EventNumber string     `json:"eventNumber" db:"event_number"`
	EventTitle  string     `json:"eventTitle" db:"event_title"`
	MenuItemID  *uuid.UUID `json:"menuItemId" db:"menu_item_id"`
	Name        string     `json:"name" db:"name"`
	Category    *string    `json:"category" db:"category"`
	Station     *string    `json:"station" db:"station"`
	Quantity    string     `json:"quantity" db:"quantity" doc:"Portions"`
	ServeAt     time.Time  `json:"serveAt" db:"serve_at"`
	OutletID    *uuid.UUID `json:"outletId" db:"outlet_id"`
	OutletName  *string    `json:"outletName" db:"outlet_name"`
	Status      string     `json:"status" db:"status" enum:"pending,in_progress,ready,served,cancelled"`
	UpdatedAt   time.Time  `json:"updatedAt" db:"updated_at"`
}

const productionSelect = `SELECT p.id, p.beo_id, b.number AS beo_number, b.version AS beo_version, p.event_id, e.number AS event_number, e.title AS event_title,
	p.menu_item_id, p.name, p.category, p.station, trim_scale(p.quantity)::text AS quantity, p.serve_at, p.outlet_id, o.name AS outlet_name, p.status,
	p.updated_at
	FROM banquet.production_items p JOIN banquet.beos b ON b.id = p.beo_id JOIN banquet.events e ON e.id = p.event_id
	LEFT JOIN commercial.outlets o ON o.id = p.outlet_id`

// Production lists the dishes of the issued BEOs to produce on a day.
func (m *Module) Production(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, outlet *uuid.UUID, status, station string) ([]BanquetProductionItem, error) {
	loc := calendar.Location(ctx, q)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return handle.List[BanquetProductionItem](q.Query(ctx, productionSelect+` WHERE p.property_id = $1 AND b.status = 'issued' AND e.status IN ('tentative', 'definite', 'completed')
		AND p.serve_at >= $2 AND p.serve_at < $3 AND ($4::uuid IS NULL OR p.outlet_id = $4) AND ($5 = '' OR p.status = $5) AND p.status <> 'cancelled'
		AND ($6 = '' OR p.station = $6) ORDER BY p.serve_at, e.number, p.category, p.name`, property, from, from.AddDate(0, 0, 1), outlet, status, station))
}

// ProductionStatusInput updates the production status of a dish.
type BanquetProductionStatus struct {
	Status string `json:"status" enum:"pending,in_progress,ready,served"`
}

// SetProductionStatus moves a dish through production.
func (m *Module) SetProductionStatus(ctx context.Context, tx pgx.Tx, pid uuid.UUID, in BanquetProductionStatus) (BanquetProductionItem, error) {
	if !contains([]string{"pending", "in_progress", "ready", "served"}, in.Status) {
		return BanquetProductionItem{}, handle.Invalid("status", "invalid", "pending, in_progress, ready or served")
	}
	rows, err := tx.Query(ctx, productionSelect+` WHERE p.id = $1`, pid)
	before, err := handle.One[BanquetProductionItem](rows, err, "production item")
	if err != nil {
		return before, err
	}
	if before.Status == "cancelled" {
		return before, errs.Conflict("cancelled", "the dish was cancelled by a BEO revision")
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.production_items SET status = $2, updated_by = $3 WHERE id = $1`, pid, in.Status, actor(ctx)); err != nil {
		return before, err
	}
	rows, err = tx.Query(ctx, productionSelect+` WHERE p.id = $1`, pid)
	after, err := handle.One[BanquetProductionItem](rows, err, "production item")
	if err != nil {
		return after, err
	}
	var property uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM banquet.production_items WHERE id = $1`, pid).Scan(&property)
	return after, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionStatusChange, EntityType: "banquet.production_item",
		EntityID: pid.String(), EntityLabel: before.EventNumber + " · " + before.Name, PropertyID: &property, Before: before, After: after})
}

// ── K6 consumption ────────────────────────────────────────────────────────

// completedPayload is banquet.event_completed: the BOM of the menu for the
// final pax (contract K6).
func (m *Module) completedPayload(ctx context.Context, q dbtx.Querier, e BanquetEvent) (map[string]any, error) {
	day, _, err := m.neededBy(ctx, q, e)
	if err != nil {
		return nil, err
	}
	reqs, err := m.requirements(ctx, q, e, e.paxBasis(), "")
	if err != nil {
		return nil, err
	}
	_, outlet, err := m.buildContent(ctx, q, e)
	if err != nil {
		return nil, err
	}
	cons := make([]map[string]any, 0, len(reqs))
	for _, r := range reqs {
		cons = append(cons, map[string]any{"itemId": r.ItemID, "quantity": r.Quantity, "uomId": r.UOMID, "recipeId": r.RecipeID})
	}
	completed := clock.Now()
	if e.CompletedAt != nil {
		completed = *e.CompletedAt
	}
	return map[string]any{"eventId": e.ID, "number": e.Number, "completedAt": completed.UTC().Format(time.RFC3339), "businessDate": day,
		"finalPax": e.paxBasis(), "outletId": outlet, "consumption": cons}, nil
}

// ── PDF (FR-BEO-05) ───────────────────────────────────────────────────────

// BEOPDF renders the standard banquet function sheet.
func BEOPDF(ctx context.Context, q dbtx.Querier, b BEO) []byte {
	loc := calendar.Location(ctx, q)
	var club string
	_ = q.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&club)
	doc := pdf.New()
	doc.Row(16, true, club)
	doc.Row(12, true, "Banquet Event Order / BEO", fmt.Sprintf("%s v%d", b.Number, b.Version))
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	c := b.Content
	doc.Row(10, false, "Event", c.Event.Number+" · "+c.Event.Title)
	doc.Row(10, false, "Type", c.Event.EventType)
	doc.Row(10, false, "Date / Tanggal", c.Event.Start.In(loc).Format("Mon 02 Jan 2006 15:04")+" – "+c.Event.End.In(loc).Format("15:04"))
	doc.Row(10, false, "Customer", c.Event.Customer)
	doc.Row(10, false, "Contact", strings.TrimSpace(c.Event.Contact+" "+c.Event.ContactPhone))
	doc.Row(10, false, "Pax (guaranteed / basis)", fmt.Sprintf("%d / %d", c.Pax.Guaranteed, c.Pax.Basis))
	doc.Row(10, false, "Status", strings.ToUpper(b.Status))
	section := func(title string) {
		doc.Space(6)
		doc.Rule(doc.Y + 10)
		doc.Row(11, true, title)
	}
	section("Venue & Setup")
	for _, v := range c.Venues {
		doc.Row(9, false, v.Venue+" · "+v.Function+" · "+strings.ReplaceAll(v.Layout, "_", " "), fmt.Sprintf("%d pax · %s–%s", v.Pax,
			v.Start.In(loc).Format("15:04"), v.End.In(loc).Format("15:04")))
	}
	section("Run of Show / Rundown")
	for _, s := range c.Schedule {
		doc.Row(9, false, s.Start.In(loc).Format("15:04")+"  "+s.Title, strings.TrimSpace(s.Venue+" "+s.Owner))
	}
	section("Menu")
	for _, mn := range c.Menus {
		doc.Row(10, true, mn.Menu)
		for _, it := range mn.Items {
			doc.Row(9, false, "  "+it.Category+" · "+it.Name, it.Portions+" portions")
		}
	}
	if len(c.Extras) > 0 {
		section("Beverage, AV, Decoration & Extras")
		for _, x := range c.Extras {
			doc.Row(9, false, x.Description, x.Quantity)
		}
	}
	if len(c.Vendors) > 0 {
		section("Vendors")
		for _, v := range c.Vendors {
			doc.Row(9, false, v.Vendor+" · "+v.Service, v.Contact)
		}
	}
	section("Electricity")
	doc.Row(9, false, "Required / included (W)", fmt.Sprintf("%d / %d", c.Electricity.RequiredWatt, c.Electricity.IncludedWatt))
	if len(c.Inclusions) > 0 {
		section("Package Inclusions")
		for _, in := range c.Inclusions {
			doc.Row(9, false, in)
		}
	}
	if len(c.Payments) > 0 {
		section("Payment Schedule")
		for _, p := range c.Payments {
			doc.Row(9, false, p.Label+" · due "+p.DueDate, p.Paid+" / "+p.Amount+" · "+strings.ReplaceAll(p.Status, "_", " "))
		}
	}
	keys := make([]string, 0, len(b.Instructions))
	for d := range b.Instructions {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		section("Department Instructions")
		for _, d := range keys {
			doc.Row(9, true, strings.ReplaceAll(d, "_", " "))
			doc.Row(9, false, "  "+b.Instructions[d])
		}
	}
	if b.Notes != nil || c.Event.SpecialRequests != "" {
		section("Special Notes (allergy, VIP)")
		doc.Row(9, false, strings.TrimSpace(c.Event.SpecialRequests+" "+deref(b.Notes)))
	}
	if len(b.Changes) > 0 {
		section(fmt.Sprintf("Changes since v%d", b.Version-1))
		for _, ch := range b.Changes {
			doc.Row(9, false, ch.Change+": "+ch.Section+" · "+ch.Item)
		}
	}
	return doc.Bytes()
}
