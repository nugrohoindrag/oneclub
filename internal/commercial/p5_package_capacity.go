package commercial

// PRD P5 EP-22 booking rules of multi-business packages, applied inside the
// P3 booking transaction (BookPackage) and in the availability check:
//
//   - FR-PKG-P5-01 choice groups: the customer picks N alternatives of a
//     group (chosen ids travel in addons; availability picks the first N),
//     the sequence & time gap after another component and the window a
//     component is served in move the component's start;
//   - FR-PKG-P5-02 capacity: package blackouts and allotments (bookings or
//     pax per start date), component blackouts and allotments (units,
//     bookings or pax per service date) and time blocks (capacity per block
//     of minutes; a component without a fixed time moves to the next block
//     with room). The package row is locked by the booking, so the counts
//     are consistent;
//   - FR-PKG-P5-03 the inventory requirement (BOM, P4) of large bookings is
//     enforced when the Package Policies say "block";
//   - FR-PKG-P5-05 payment schedule from the template of the package type.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// bookingRules is called by BookPackage (check = false) and Availability
// (check = true: default choices, nothing enforced that needs the customer).
var bookingRules func(ctx context.Context, tx pgx.Tx, property uuid.UUID, spec *PackageSpec, in *BookingInput, day time.Time, check bool) error

// componentRule is the active rule of a component.
type componentRule struct {
	ComponentID      uuid.UUID  `db:"component_id"`
	ChoiceGroup      *string    `db:"choice_group"`
	ChoicePick       int        `db:"choice_pick"`
	AfterComponentID *uuid.UUID `db:"after_component_id"`
	MinGap           int        `db:"min_gap_minutes"`
	MaxGap           *int       `db:"max_gap_minutes"`
	WindowStart      *string    `db:"window_start"`
	WindowEnd        *string    `db:"window_end"`
}

func loadComponentRules(ctx context.Context, q dbtx.Querier, comps []ComponentSpec) (map[uuid.UUID]componentRule, error) {
	ids := make([]uuid.UUID, 0, len(comps))
	for _, c := range comps {
		ids = append(ids, c.ID)
	}
	out := map[uuid.UUID]componentRule{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := handle.List[componentRule](q.Query(ctx, `SELECT DISTINCT ON (component_id) component_id, choice_group, choice_pick, after_component_id,
		min_gap_minutes, max_gap_minutes, to_char(window_start, 'HH24:MI') AS window_start, to_char(window_end, 'HH24:MI') AS window_end
		FROM commercial.package_component_rules WHERE component_id = ANY($1) AND status = 'active' ORDER BY component_id, updated_at DESC`, ids))
	for _, r := range list {
		out[r.ComponentID] = r
	}
	return out, err
}

// resolveChoices keeps the chosen alternatives of every choice group (pick
// N; auto picks the first N by sequence when nothing is chosen) and returns
// the components to book and the chosen ids that were group members.
func resolveChoices(comps []ComponentSpec, rules map[uuid.UUID]componentRule, chosen []uuid.UUID, auto bool) ([]ComponentSpec, []uuid.UUID, error) {
	type group struct {
		pick    int
		members []int
	}
	groups := map[string]*group{}
	var order []string
	for i, c := range comps {
		r, ok := rules[c.ID]
		if !ok || r.ChoiceGroup == nil || *r.ChoiceGroup == "" {
			continue
		}
		g := groups[*r.ChoiceGroup]
		if g == nil {
			g = &group{}
			groups[*r.ChoiceGroup] = g
			order = append(order, *r.ChoiceGroup)
		}
		g.pick = max(g.pick, r.ChoicePick, 1)
		g.members = append(g.members, i)
	}
	if len(groups) == 0 {
		return comps, nil, nil
	}
	keep := make([]bool, len(comps))
	for i := range comps {
		keep[i] = true
	}
	var used []uuid.UUID
	for _, name := range order {
		g := groups[name]
		sort.SliceStable(g.members, func(a, b int) bool { return comps[g.members[a]].Seq < comps[g.members[b]].Seq })
		var picked []int
		for _, i := range g.members {
			keep[i] = false
			if slices.Contains(chosen, comps[i].ID) {
				picked = append(picked, i)
				used = append(used, comps[i].ID)
			}
		}
		if len(picked) == 0 && auto {
			picked = g.members[:min(g.pick, len(g.members))]
		}
		if len(picked) != min(g.pick, len(g.members)) {
			names := make([]string, 0, len(g.members))
			for _, i := range g.members {
				names = append(names, comps[i].Name)
			}
			return nil, nil, errs.Validation("choice_required", fmt.Sprintf("choose %d of %s: %s", g.pick, name, strings.Join(names, ", ")),
				errs.Field("addons", "choice_required", fmt.Sprintf("%d of %s", g.pick, name)))
		}
		for _, i := range picked {
			keep[i] = true
		}
	}
	out := make([]ComponentSpec, 0, len(comps))
	for i, c := range comps {
		if !keep[i] {
			continue
		}
		if r, ok := rules[c.ID]; ok && r.ChoiceGroup != nil && *r.ChoiceGroup != "" {
			c.Optional = false // a chosen alternative is part of the package price
		}
		out = append(out, c)
	}
	return out, used, nil
}

// componentDuration is the scheduled length of a component (P3 defaults).
func componentDuration(c ComponentSpec) time.Duration {
	switch {
	case c.DurationMinutes != nil:
		return time.Duration(*c.DurationMinutes) * time.Minute
	case c.ComponentType == "tee_time":
		return 120 * time.Minute
	}
	return 60 * time.Minute
}

// planSlot is the scheduled start of a component.
type planSlot struct {
	Start, End time.Time
	Explicit   bool // the component has its own start time
}

// fitFunc moves a start to the first time with capacity (time blocks).
type fitFunc func(c ComponentSpec, slot planSlot) (time.Time, error)

// planSchedule places the components on the package day: sequence & time
// gap after the previous component, then the service window, then fit
// (capacity). Components are placed after their predecessor.
func planSchedule(comps []ComponentSpec, rules map[uuid.UUID]componentRule, day time.Time, pkgStart string, loc *time.Location,
	fit fitFunc) (map[uuid.UUID]planSlot, error) {
	out := map[uuid.UUID]planSlot{}
	inPkg := map[uuid.UUID]ComponentSpec{}
	for _, c := range comps {
		inPkg[c.ID] = c
	}
	done := map[uuid.UUID]bool{}
	for pass := 0; pass <= len(comps); pass++ {
		progress := false
		for _, c := range comps {
			if done[c.ID] {
				continue
			}
			r, hasRule := rules[c.ID]
			var pred *planSlot
			if hasRule && r.AfterComponentID != nil {
				if _, ok := inPkg[*r.AfterComponentID]; ok {
					p, placed := out[*r.AfterComponentID]
					if !placed {
						continue // its predecessor first
					}
					pred = &p
				}
			}
			serviceDate := day.AddDate(0, 0, c.DayOffset)
			st := pkgStart
			explicit := c.StartTime != nil && *c.StartTime != ""
			if explicit {
				st = *c.StartTime
			}
			start := clockAt(serviceDate, st, loc)
			dur := componentDuration(c)
			if pred != nil {
				earliest := pred.End.Add(time.Duration(r.MinGap) * time.Minute)
				if start.Before(earliest) {
					if explicit {
						return nil, errs.Conflict("sequence_violation", fmt.Sprintf("%s starts %s, before the end of the previous component plus %d minutes",
							c.Name, start.In(loc).Format("15:04"), r.MinGap))
					}
					start = earliest
				}
			}
			if hasRule && r.WindowStart != nil {
				ws := clockAt(start.In(loc), *r.WindowStart, loc)
				if start.Before(ws) {
					if explicit {
						return nil, errs.Conflict("outside_window", fmt.Sprintf("%s is served from %s", c.Name, *r.WindowStart))
					}
					start = ws
				}
			}
			slot := planSlot{Start: start, End: start.Add(dur), Explicit: explicit}
			if fit != nil {
				s, err := fit(c, slot)
				if err != nil {
					return nil, err
				}
				slot.Start, slot.End = s, s.Add(dur)
			}
			if hasRule && r.WindowEnd != nil {
				we := clockAt(slot.Start.In(loc), *r.WindowEnd, loc)
				if slot.End.After(we) {
					return nil, errs.Conflict("outside_window", fmt.Sprintf("%s must finish by %s (starts %s)", c.Name, *r.WindowEnd,
						slot.Start.In(loc).Format("15:04")))
				}
			}
			if pred != nil && r.MaxGap != nil && slot.Start.After(pred.End.Add(time.Duration(*r.MaxGap)*time.Minute)) {
				return nil, errs.Conflict("sequence_violation", fmt.Sprintf("%s would start %s, more than %d minutes after the previous component",
					c.Name, slot.Start.In(loc).Format("15:04"), *r.MaxGap))
			}
			out[c.ID] = slot
			done[c.ID] = true
			progress = true
		}
		if len(done) == len(comps) {
			break
		}
		if !progress {
			return nil, errs.Conflict("sequence_cycle", "the component sequence rules form a cycle")
		}
	}
	return out, nil
}

// capacityRule is an active capacity of a package.
type capacityRule struct {
	ID           uuid.UUID  `db:"id"`
	ComponentID  *uuid.UUID `db:"component_id"`
	Type         string     `db:"capacity_type"`
	DateFrom     string     `db:"date_from"`
	DateTo       *string    `db:"date_to"`
	Weekdays     []int32    `db:"weekdays"`
	Quota        *int       `db:"quota"`
	Basis        string     `db:"basis"`
	BlockMinutes *int       `db:"block_minutes"`
	WindowStart  *string    `db:"window_start"`
	WindowEnd    *string    `db:"window_end"`
	Reason       *string    `db:"reason"`
}

func (c capacityRule) applies(d time.Time) bool {
	s := d.Format("2006-01-02")
	if s < c.DateFrom || (c.DateTo != nil && s > *c.DateTo) {
		return false
	}
	return len(c.Weekdays) == 0 || slices.Contains(c.Weekdays, int32(isoWeekday(d)))
}

func (c capacityRule) reason() string {
	if c.Reason != nil && strings.TrimSpace(*c.Reason) != "" {
		return " (" + strings.TrimSpace(*c.Reason) + ")"
	}
	return ""
}

func loadCapacities(ctx context.Context, q dbtx.Querier, pkg uuid.UUID) ([]capacityRule, error) {
	return handle.List[capacityRule](q.Query(ctx, `SELECT id, component_id, capacity_type, to_char(date_from, 'YYYY-MM-DD') AS date_from,
		to_char(date_to, 'YYYY-MM-DD') AS date_to, weekdays, quota, basis, block_minutes, to_char(window_start, 'HH24:MI') AS window_start,
		to_char(window_end, 'HH24:MI') AS window_end, reason FROM commercial.package_capacities WHERE package_id = $1 AND status = 'active'
		ORDER BY date_from, created_at`, pkg))
}

// packageUse is the bookings and pax of a package on a start date.
func packageUse(ctx context.Context, q dbtx.Querier, pkg uuid.UUID, d time.Time) (bookings, pax int, err error) {
	err = q.QueryRow(ctx, `SELECT count(*)::int, coalesce(sum(pax), 0)::int FROM commercial.package_bookings WHERE package_id = $1 AND start_date = $2::date
		AND status IN ('pending', 'confirmed', 'completed')`, pkg, d.Format("2006-01-02")).Scan(&bookings, &pax)
	return
}

// componentUse is what bookings use of a component on a service date (or in
// a time window of it): units, bookings and pax.
func componentUse(ctx context.Context, q dbtx.Querier, comp uuid.UUID, d time.Time, from, to *time.Time) (units decimal.Decimal, bookings, pax int, err error) {
	var u string
	err = q.QueryRow(ctx, `WITH c AS (SELECT c.booking_id, c.quantity FROM commercial.package_booking_components c
		JOIN commercial.package_bookings b ON b.id = c.booking_id
		WHERE c.component_id = $1 AND c.service_date = $2::date AND c.status <> 'cancelled' AND b.status IN ('pending', 'confirmed', 'completed')
		AND ($3::timestamptz IS NULL OR (c.scheduled_start >= $3 AND c.scheduled_start < $4)))
		SELECT coalesce(sum(quantity), 0)::text, count(DISTINCT booking_id)::int,
		coalesce((SELECT sum(pax) FROM commercial.package_bookings WHERE id IN (SELECT booking_id FROM c)), 0)::int FROM c`,
		comp, d.Format("2006-01-02"), from, to).Scan(&u, &bookings, &pax)
	return dec(u), bookings, pax, err
}

func basisNeed(basis string, units decimal.Decimal, pax int) decimal.Decimal {
	switch basis {
	case "pax":
		return decimal.NewFromInt(int64(pax))
	case "units":
		return units
	}
	return decimal.NewFromInt(1)
}

func basisUsed(basis string, units decimal.Decimal, bookings, pax int) decimal.Decimal {
	switch basis {
	case "pax":
		return decimal.NewFromInt(int64(pax))
	case "units":
		return units
	}
	return decimal.NewFromInt(int64(bookings))
}

// componentQuantity is the booked quantity of a component (P3 rules).
func componentQuantity(c ComponentSpec, pax, nights int) decimal.Decimal {
	q := dec(c.Quantity)
	if c.PerPax {
		q = q.Mul(decimal.NewFromInt(int64(pax)))
	}
	if c.PerNight {
		q = q.Mul(decimal.NewFromInt(int64(max(nights, 1))))
	}
	return q
}

// blockOf is the time block of a start within a window.
func blockOf(start time.Time, windowStart time.Time, minutes int) (time.Time, time.Time) {
	b := time.Duration(minutes) * time.Minute
	if start.Before(windowStart) {
		return windowStart, windowStart.Add(b)
	}
	n := start.Sub(windowStart) / b
	from := windowStart.Add(n * b)
	return from, from.Add(b)
}

// applyBookingRules implements bookingRules.
func applyBookingRules(ctx context.Context, tx pgx.Tx, property uuid.UUID, spec *PackageSpec, in *BookingInput, day time.Time, check bool) error {
	rules, err := loadComponentRules(ctx, tx, spec.Components)
	if err != nil {
		return err
	}
	comps, used, err := resolveChoices(spec.Components, rules, in.Addons, true)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		var rest []uuid.UUID
		for _, a := range in.Addons {
			if !slices.Contains(used, a) {
				rest = append(rest, a)
			}
		}
		in.Addons = rest
	}
	caps, err := loadCapacities(ctx, tx, spec.ID)
	if err != nil {
		return err
	}
	pol, err := LoadPackagePolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	loc := calendar.Location(ctx, tx)
	// package blackout and allotments (start date)
	for _, c := range caps {
		if c.ComponentID != nil || !c.applies(day) {
			continue
		}
		switch c.Type {
		case "blackout":
			return errs.Conflict("package_blackout", fmt.Sprintf("%s is not available on %s%s", spec.Name, day.Format("2006-01-02"), c.reason()))
		case "allotment":
			bookings, pax, err := packageUse(ctx, tx, spec.ID, day)
			if err != nil {
				return err
			}
			use := basisUsed(c.Basis, decimal.NewFromInt(int64(bookings)), bookings, pax)
			need := basisNeed(c.Basis, decimal.NewFromInt(1), in.Pax)
			if c.Quota != nil && use.Add(need).GreaterThan(decimal.NewFromInt(int64(*c.Quota))) {
				return errs.Conflict("package_allotment_full", fmt.Sprintf("%s is fully booked on %s (allotment %d %s)", spec.Name, day.Format("2006-01-02"),
					*c.Quota, c.Basis))
			}
		}
	}
	// component blackouts and allotments (service date), time blocks
	qty := map[uuid.UUID]decimal.Decimal{}
	for _, c := range comps {
		qty[c.ID] = componentQuantity(c, in.Pax, in.Nights)
		sd := day.AddDate(0, 0, c.DayOffset)
		for _, k := range caps {
			if k.ComponentID == nil || *k.ComponentID != c.ID || !k.applies(sd) {
				continue
			}
			switch k.Type {
			case "blackout":
				return errs.Conflict("component_blackout", fmt.Sprintf("%s is not available on %s%s", c.Name, sd.Format("2006-01-02"), k.reason()))
			case "allotment":
				units, bookings, pax, err := componentUse(ctx, tx, c.ID, sd, nil, nil)
				if err != nil {
					return err
				}
				if k.Quota != nil && basisUsed(k.Basis, units, bookings, pax).Add(basisNeed(k.Basis, qty[c.ID], in.Pax)).GreaterThan(decimal.NewFromInt(int64(*k.Quota))) {
					return errs.Conflict("component_allotment_full", fmt.Sprintf("%s is fully booked on %s (allotment %d %s)", c.Name, sd.Format("2006-01-02"),
						*k.Quota, k.Basis))
				}
			}
		}
	}
	fit := func(c ComponentSpec, slot planSlot) (time.Time, error) {
		start := slot.Start
		for _, k := range caps {
			if k.Type != "time_block" || k.ComponentID == nil || *k.ComponentID != c.ID || !k.applies(start.In(loc)) || k.Quota == nil {
				continue
			}
			minutes := pol.DefaultBlockMinutes
			if k.BlockMinutes != nil {
				minutes = *k.BlockMinutes
			}
			local := start.In(loc)
			ws := clockAt(local, "00:00", loc)
			if k.WindowStart != nil {
				ws = clockAt(local, *k.WindowStart, loc)
			}
			we := clockAt(local, "00:00", loc).AddDate(0, 0, 1)
			if k.WindowEnd != nil {
				we = clockAt(local, *k.WindowEnd, loc)
			}
			from, to := blockOf(start, ws, minutes)
			if start.Before(ws) && slot.Explicit {
				return start, errs.Conflict("outside_window", fmt.Sprintf("%s time blocks start at %s", c.Name, ws.Format("15:04")))
			}
			if start.Before(ws) {
				start = ws
			}
			for {
				if to.After(we) || from.After(we) {
					return start, errs.Conflict("time_block_full", fmt.Sprintf("%s has no free time block on %s", c.Name, local.Format("2006-01-02")))
				}
				units, bookings, pax, err := componentUse(ctx, tx, c.ID, local, &from, &to)
				if err != nil {
					return start, err
				}
				if !basisUsed(k.Basis, units, bookings, pax).Add(basisNeed(k.Basis, qty[c.ID], in.Pax)).GreaterThan(decimal.NewFromInt(int64(*k.Quota))) {
					break
				}
				if slot.Explicit {
					return start, errs.Conflict("time_block_full", fmt.Sprintf("%s is fully booked at %s on %s", c.Name, from.In(loc).Format("15:04"),
						local.Format("2006-01-02")))
				}
				from, to = to, to.Add(time.Duration(minutes)*time.Minute)
				start = from
			}
		}
		return start, nil
	}
	plan, err := planSchedule(comps, rules, day, spec.StartTime, loc, fit)
	if err != nil {
		return err
	}
	for i := range comps {
		s, ok := plan[comps[i].ID]
		if !ok {
			continue
		}
		local := s.Start.In(loc)
		hhmm := local.Format("15:04")
		comps[i].StartTime = &hhmm
		ld := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		comps[i].DayOffset = int(ld.Sub(time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)).Hours()+12) / 24
	}
	spec.Components = comps
	if check {
		return nil
	}
	// FR-PKG-P5-03: large bookings and the inventory requirement
	if pol.InventoryCheck == "block" && in.Pax >= pol.LargeQuotaPax {
		req, err := inventoryRequirement(ctx, tx, property, *spec, in.Pax, in.Nights, 1)
		if err != nil {
			return err
		}
		for _, r := range req.Items {
			if r.Short {
				return errs.Conflict("inventory_shortage", fmt.Sprintf("%s: %s is short (%s %s needed, %s on hand)", spec.Name, r.ItemName, r.Required, r.UOM,
					r.OnHand))
			}
		}
	}
	// FR-PKG-P5-05: payment schedule template of the package type
	if len(in.ScheduleLines) == 0 && in.AgreedTotal == nil {
		lines, err := templateLines(ctx, tx, *spec, *in, day.In(loc), clock.Now().In(loc))
		if err != nil {
			return err
		}
		in.ScheduleLines = lines
	}
	return nil
}

// templateLines builds the payment schedule of the package type's template
// (the template with the highest minimum not above the list price).
func templateLines(ctx context.Context, q dbtx.Querier, spec PackageSpec, in BookingInput, day, today time.Time) ([]billing.ScheduleLineInput, error) {
	list := dec(spec.Price).Mul(multiplier(spec.PricingMode, in.Pax, in.Nights))
	var code string
	var raw []byte
	err := q.QueryRow(ctx, `SELECT code, lines FROM commercial.package_payment_templates WHERE package_type = $1 AND status = 'active' AND archived_at IS NULL
		AND min_amount <= $2::numeric ORDER BY min_amount DESC, code LIMIT 1`, spec.PackageType, list.String()).Scan(&code, &raw)
	if dbtx.IsNoRows(err) || !list.IsPositive() {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []PaymentTemplateLine
	if err := json.Unmarshal(raw, &lines); err != nil || len(lines) == 0 {
		return nil, nil //nolint:nilerr // an unreadable template is skipped (validated on save)
	}
	return scheduleFromTemplate(lines, code, day, today), nil
}

// scheduleFromTemplate turns template lines into due amounts (never before today).
func scheduleFromTemplate(lines []PaymentTemplateLine, code string, day, today time.Time) []billing.ScheduleLineInput {
	out := make([]billing.ScheduleLineInput, 0, len(lines))
	t0 := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	for i, l := range lines {
		due := t0.AddDate(0, 0, l.Days)
		if l.From == "before_start" {
			due = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -l.Days)
		}
		if due.Before(t0) {
			due = t0
		}
		pct := l.Percent
		if i == len(lines)-1 {
			pct = "" // the last due amount takes the rest
		}
		out = append(out, billing.ScheduleLineInput{Label: l.Label + " (" + code + ")", Kind: l.Kind, Percent: pct, DueDate: due.Format("2006-01-02")})
	}
	return out
}

// ── capacity calendar (GET /commercial/packages/{id}/capacity) ────────────

// PackageCapacityUse is a capacity with its use.
type PackageCapacityUse struct {
	CapacityID   uuid.UUID `json:"capacityId"`
	CapacityType string    `json:"capacityType" enum:"blackout,allotment,time_block"`
	Basis        string    `json:"basis" enum:"bookings,pax,units"`
	Quota        *int      `json:"quota"`
	Used         string    `json:"used"`
	Left         *string   `json:"left"`
	Reason       *string   `json:"reason"`
}

// PackageTimeBlock is the use of one time block of a component.
type PackageTimeBlock struct {
	From     string `json:"from" doc:"HH:MM"`
	To       string `json:"to"`
	Capacity int    `json:"capacity"`
	Used     string `json:"used"`
	Left     string `json:"left"`
}

// PackageComponentCapacity is a component on a package date.
type PackageComponentCapacity struct {
	ComponentID   uuid.UUID            `json:"componentId"`
	Name          string               `json:"name"`
	ComponentType string               `json:"componentType"`
	ServiceDate   string               `json:"serviceDate"`
	ChoiceGroup   *string              `json:"choiceGroup"`
	Booked        string               `json:"booked" doc:"Units booked on the service date"`
	Available     bool                 `json:"available"`
	Capacities    []PackageCapacityUse `json:"capacities"`
	TimeBlocks    []PackageTimeBlock   `json:"timeBlocks"`
}

// PackageCapacityDay is the capacity of a package on a start date.
type PackageCapacityDay struct {
	Date       string                     `json:"date"`
	Available  bool                       `json:"available"`
	Reason     string                     `json:"reason,omitempty"`
	Bookings   int                        `json:"bookings"`
	Pax        int                        `json:"pax"`
	DailyQuota *int                       `json:"dailyQuota"`
	Capacities []PackageCapacityUse       `json:"capacities"`
	Components []PackageComponentCapacity `json:"components"`
	LargeQuota bool                       `json:"largeQuota" doc:"An allotment reaches the Package Policies large quota: see the inventory requirement"`
}

// PackageCapacityCalendar is GET /commercial/packages/{id}/capacity.
type PackageCapacityCalendar struct {
	PackageID uuid.UUID            `json:"packageId"`
	Code      string               `json:"code"`
	Name      string               `json:"name"`
	Days      []PackageCapacityDay `json:"days"`
}

// specForCapacity is the published spec, else the draft.
func specForCapacity(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (PackageSpec, error) {
	s, err := PublishedSpec(ctx, q, pid)
	if err == nil {
		return s, nil
	}
	if de, ok := errs.As(err); ok && de.Code == "package_not_published" {
		return draftSpec(ctx, q, pid)
	}
	return s, err
}

func intPtrDec(d decimal.Decimal) *string {
	s := d.String()
	return &s
}

// CapacityCalendar computes the capacity of a package for consecutive dates.
func CapacityCalendar(ctx context.Context, q dbtx.Querier, property, pid uuid.UUID, from time.Time, days int) (PackageCapacityCalendar, error) {
	spec, err := specForCapacity(ctx, q, pid)
	if err != nil {
		return PackageCapacityCalendar{}, err
	}
	out := PackageCapacityCalendar{PackageID: pid, Code: spec.Code, Name: spec.Name, Days: []PackageCapacityDay{}}
	caps, err := loadCapacities(ctx, q, pid)
	if err != nil {
		return out, err
	}
	rules, err := loadComponentRules(ctx, q, spec.Components)
	if err != nil {
		return out, err
	}
	pol, err := LoadPackagePolicy(ctx, q, property)
	if err != nil {
		return out, err
	}
	loc := calendar.Location(ctx, q)
	for i := 0; i < max(days, 1); i++ {
		d := time.Date(from.Year(), from.Month(), from.Day()+i, 0, 0, 0, 0, loc)
		day := PackageCapacityDay{Date: d.Format("2006-01-02"), Available: true, DailyQuota: spec.DailyQuota, Capacities: []PackageCapacityUse{},
			Components: []PackageComponentCapacity{}}
		if day.Bookings, day.Pax, err = packageUse(ctx, q, pid, d); err != nil {
			return out, err
		}
		if spec.DailyQuota != nil && day.Bookings >= *spec.DailyQuota {
			day.Available, day.Reason = false, "sold out (daily quota)"
		}
		for _, c := range caps {
			if c.ComponentID != nil || !c.applies(d) {
				continue
			}
			u := PackageCapacityUse{CapacityID: c.ID, CapacityType: c.Type, Basis: c.Basis, Quota: c.Quota, Reason: c.Reason}
			used := basisUsed(c.Basis, decimal.NewFromInt(int64(day.Bookings)), day.Bookings, day.Pax)
			u.Used = used.String()
			switch c.Type {
			case "blackout":
				day.Available, day.Reason = false, "blackout"+c.reason()
			case "allotment":
				if c.Quota != nil {
					left := decimal.Max(decimal.NewFromInt(int64(*c.Quota)).Sub(used), decimal.Zero)
					u.Left = intPtrDec(left)
					if !left.IsPositive() {
						day.Available, day.Reason = false, "allotment full"
					}
					paxQuota := *c.Quota
					if c.Basis == "bookings" {
						paxQuota = *c.Quota * max(spec.MinPax, 1)
					}
					day.LargeQuota = day.LargeQuota || paxQuota >= pol.LargeQuotaPax
				}
			}
			day.Capacities = append(day.Capacities, u)
		}
		for _, comp := range spec.Components {
			sd := d.AddDate(0, 0, comp.DayOffset)
			units, bookings, pax, err := componentUse(ctx, q, comp.ID, sd, nil, nil)
			if err != nil {
				return out, err
			}
			cc := PackageComponentCapacity{ComponentID: comp.ID, Name: comp.Name, ComponentType: comp.ComponentType, ServiceDate: sd.Format("2006-01-02"),
				Booked: units.String(), Available: true, Capacities: []PackageCapacityUse{}, TimeBlocks: []PackageTimeBlock{}}
			if r, ok := rules[comp.ID]; ok {
				cc.ChoiceGroup = r.ChoiceGroup
			}
			for _, k := range caps {
				if k.ComponentID == nil || *k.ComponentID != comp.ID || !k.applies(sd) {
					continue
				}
				u := PackageCapacityUse{CapacityID: k.ID, CapacityType: k.Type, Basis: k.Basis, Quota: k.Quota, Reason: k.Reason}
				used := basisUsed(k.Basis, units, bookings, pax)
				u.Used = used.String()
				switch k.Type {
				case "blackout":
					cc.Available = false
				case "allotment":
					if k.Quota != nil {
						left := decimal.Max(decimal.NewFromInt(int64(*k.Quota)).Sub(used), decimal.Zero)
						u.Left = intPtrDec(left)
						cc.Available = cc.Available && left.IsPositive()
					}
				case "time_block":
					minutes := pol.DefaultBlockMinutes
					if k.BlockMinutes != nil {
						minutes = *k.BlockMinutes
					}
					ws, we := clockAt(sd, "00:00", loc), clockAt(sd, "00:00", loc).AddDate(0, 0, 1)
					if k.WindowStart != nil {
						ws = clockAt(sd, *k.WindowStart, loc)
					}
					if k.WindowEnd != nil {
						we = clockAt(sd, *k.WindowEnd, loc)
					}
					for f := ws; !f.Add(time.Duration(minutes) * time.Minute).After(we); f = f.Add(time.Duration(minutes) * time.Minute) {
						t := f.Add(time.Duration(minutes) * time.Minute)
						bu, bb, bp, err := componentUse(ctx, q, comp.ID, sd, &f, &t)
						if err != nil {
							return out, err
						}
						bused := basisUsed(k.Basis, bu, bb, bp)
						capQ := 0
						if k.Quota != nil {
							capQ = *k.Quota
						}
						cc.TimeBlocks = append(cc.TimeBlocks, PackageTimeBlock{From: f.In(loc).Format("15:04"), To: t.In(loc).Format("15:04"), Capacity: capQ,
							Used: bused.String(), Left: decimal.Max(decimal.NewFromInt(int64(capQ)).Sub(bused), decimal.Zero).String()})
					}
				}
				cc.Capacities = append(cc.Capacities, u)
			}
			if !cc.Available && cc.ChoiceGroup == nil && day.Available {
				day.Available, day.Reason = false, comp.Name+" is not available"
			}
			day.Components = append(day.Components, cc)
		}
		out.Days = append(out.Days, day)
	}
	return out, nil
}

// ── inventory requirement (FR-PKG-P5-03) ─────────────────────────────────

// PackageInventoryLine is one ingredient of the package BOM.
type PackageInventoryLine struct {
	ItemID   uuid.UUID `json:"itemId"`
	ItemCode string    `json:"itemCode"`
	ItemName string    `json:"itemName"`
	UOM      string    `json:"uom"`
	Required string    `json:"required"`
	OnHand   string    `json:"onHand"`
	Short    bool      `json:"short"`
	UnitCost string    `json:"unitCost"`
	Cost     string    `json:"cost"`
}

// PackageInventoryRequirement is the BOM of N bookings of a package.
type PackageInventoryRequirement struct {
	PackageID  uuid.UUID              `json:"packageId"`
	Bookings   int                    `json:"bookings"`
	Pax        int                    `json:"pax"`
	LargeQuota bool                   `json:"largeQuota" doc:"At least the Package Policies large quota (pax)"`
	Items      []PackageInventoryLine `json:"items"`
	TotalCost  string                 `json:"totalCost"`
	Shortages  int                    `json:"shortages"`
}

// itemCost is the unit cost of an item (base UOM) by the cost basis.
func itemCost(ctx context.Context, q dbtx.Querier, item uuid.UUID, basis string) (decimal.Decimal, error) {
	var avg *string
	var std string
	err := q.QueryRow(ctx, `SELECT CASE WHEN sum(b.quantity) > 0 THEN (sum(b.value) / sum(b.quantity))::text END, i.standard_cost::text
		FROM inventory.items i LEFT JOIN inventory.stock_balances b ON b.item_id = i.id WHERE i.id = $1 GROUP BY i.id`, item).Scan(&avg, &std)
	if dbtx.IsNoRows(err) {
		return decimal.Zero, nil
	}
	if err != nil {
		return decimal.Zero, err
	}
	if basis != "standard" && avg != nil {
		return dec(*avg), nil
	}
	return dec(std), nil
}

// componentRecipe is the BOM of a component: its recipe or its product's.
func componentRecipe(ctx context.Context, q dbtx.Querier, recipe, product *uuid.UUID) (*uuid.UUID, error) {
	if recipe != nil {
		return recipe, nil
	}
	if product != nil {
		return inventory.ProductRecipe(ctx, q, *product)
	}
	return nil, nil
}

func inventoryRequirement(ctx context.Context, q dbtx.Querier, property uuid.UUID, spec PackageSpec, pax, nights, bookings int) (PackageInventoryRequirement, error) {
	out := PackageInventoryRequirement{PackageID: spec.ID, Bookings: bookings, Pax: pax * bookings, Items: []PackageInventoryLine{}}
	pol, err := LoadPackagePolicy(ctx, q, property)
	if err != nil {
		return out, err
	}
	out.LargeQuota = out.Pax >= pol.LargeQuotaPax
	var lists [][]inventory.Requirement
	for _, c := range spec.Components {
		if c.Optional {
			continue
		}
		rid, err := componentRecipe(ctx, q, c.RecipeID, c.ProductID)
		if err != nil || rid == nil {
			if err != nil {
				return out, err
			}
			continue
		}
		units := componentQuantity(c, pax, nights).Mul(decimal.NewFromInt(int64(bookings)))
		reqs, err := inventory.ExplodeRecipe(ctx, q, *rid, units)
		if err != nil {
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				continue // incomplete recipe: nothing to show
			}
			return out, err
		}
		lists = append(lists, reqs)
	}
	total := decimal.Zero
	for _, r := range inventory.MergeRequirements(lists...) {
		var onHand string
		if err := q.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text FROM inventory.stock_balances WHERE property_id = $1 AND item_id = $2`, property,
			r.ItemID).Scan(&onHand); err != nil {
			return out, err
		}
		uc, err := itemCost(ctx, q, r.ItemID, pol.CostBasis)
		if err != nil {
			return out, err
		}
		cost := uc.Mul(r.Quantity).Round(2)
		total = total.Add(cost)
		short := dec(onHand).LessThan(r.Quantity)
		if short {
			out.Shortages++
		}
		out.Items = append(out.Items, PackageInventoryLine{ItemID: r.ItemID, ItemCode: r.ItemCode, ItemName: r.ItemName, UOM: r.UOM,
			Required: r.Quantity.Round(4).String(), OnHand: dec(onHand).Round(4).String(), Short: short, UnitCost: uc.Round(4).String(), Cost: cost.String()})
	}
	out.TotalCost = total.String()
	return out, nil
}

// InventoryRequirement is GET /commercial/packages/{id}/inventory-requirement:
// the BOM of `bookings` bookings of `pax` (default: the allotment of the date).
func InventoryRequirement(ctx context.Context, q dbtx.Querier, property, pid uuid.UUID, date *time.Time, pax, nights, bookings int) (PackageInventoryRequirement, error) {
	spec, err := specForCapacity(ctx, q, pid)
	if err != nil {
		return PackageInventoryRequirement{}, err
	}
	if pax <= 0 {
		pax = spec.MinPax
	}
	if nights <= 0 {
		nights = spec.Nights
	}
	if bookings <= 0 {
		bookings = 1
		if date != nil {
			caps, err := loadCapacities(ctx, q, pid)
			if err != nil {
				return PackageInventoryRequirement{}, err
			}
			for _, c := range caps {
				if c.ComponentID == nil && c.Type == "allotment" && c.Quota != nil && c.applies(*date) {
					switch c.Basis {
					case "pax":
						bookings = max(1, *c.Quota/max(pax, 1))
					default:
						bookings = max(1, *c.Quota)
					}
				}
			}
			if bookings == 1 && spec.DailyQuota != nil {
				bookings = *spec.DailyQuota
			}
		}
	}
	return inventoryRequirement(ctx, q, property, spec, pax, nights, bookings)
}
