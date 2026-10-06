package commercial

// Package booking (FR-PKG-04..08): one transaction prices the package
// (base price per package / pax / night, add-ons, promotions, tax & service
// nett or ++), allocates every component through the registered allocators
// (all-or-nothing), splits the revenue over the components (standalone
// selling price, fixed amounts or percentages — the last component takes
// the rounding so the allocation equals the package price, K3), posts one
// folio line per component on the customer's folio (business line package),
// opens a payment schedule for large packages and publishes
// commercial.package_booked. Components are consumed in their line
// (check-in, tee-off, POS) — commercial.package_consumed with the BOM of the
// component (K6) — and unused components expire.

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/inventory"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/membership"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/outbox"
)

// Events of packages (contracts K3, K6).
const (
	EventPackageBooked    = "commercial.package_booked"
	EventPackageConsumed  = "commercial.package_consumed"
	EventPackageCancelled = "commercial.package_cancelled"
)

// PackageHoldMinutes is how long an unpaid website / app booking holds its
// allocations.
const PackageHoldMinutes = 30

// P3Module serves the PRD P3 promotions and packages (wired by internal/app).
type P3Module struct {
	DB        *dbtx.DB
	Billing   *billing.Service
	Events    *outbox.Bus
	Approvals *approval.Engine
	Notify    notify.Sender
	// WebsiteURL is the public website (links in customer notifications).
	WebsiteURL func() string
}

// BookingPayment is a payment taken with a booking.
type BookingPayment struct {
	MethodType string `json:"methodType" enum:"cash,bank_transfer,qris,card,virtual_account,payment_gateway,member_account,voucher_prepaid"`
	Amount     string `json:"amount,omitempty" doc:"Default: the down payment of the schedule, else the total"`
	Reference  string `json:"reference,omitempty"`
}

// BookingInput books a package (staff, website, member app, quotation).
type BookingInput struct {
	PackageID          *uuid.UUID                  `json:"packageId,omitempty"`
	PackageCode        string                      `json:"packageCode,omitempty"`
	StartDate          string                      `json:"startDate" doc:"Local start date YYYY-MM-DD"`
	Pax                int                         `json:"pax,omitempty" doc:"Default: the package minimum"`
	Nights             int                         `json:"nights,omitempty" doc:"Default: the package nights"`
	Addons             []uuid.UUID                 `json:"addons,omitempty" doc:"Optional components (add-ons) chosen"`
	CustomerID         *uuid.UUID                  `json:"customerId,omitempty"`
	CorporateAccountID *uuid.UUID                  `json:"corporateAccountId,omitempty" doc:"Billed to the company (corporate package)"`
	GuestName          string                      `json:"guestName,omitempty"`
	GuestPhone         string                      `json:"guestPhone,omitempty"`
	GuestEmail         string                      `json:"guestEmail,omitempty"`
	Channel            string                      `json:"channel,omitempty" enum:"back_office,website,member_app,ops,quotation"`
	PromoCodes         []string                    `json:"promoCodes,omitempty"`
	Notes              string                      `json:"notes,omitempty"`
	SourceType         string                      `json:"sourceType,omitempty" doc:"e.g. quotation"`
	SourceID           *uuid.UUID                  `json:"sourceId,omitempty"`
	Payment            *BookingPayment             `json:"payment,omitempty"`
	ScheduleLines      []billing.ScheduleLineInput `json:"scheduleLines,omitempty" doc:"Payment terms (default for large packages: down payment now, the rest H-7)"`
	// AgreedTotal is the price agreed in an accepted quotation (set by the
	// quotation conversion, not accepted from HTTP input): it replaces the
	// promotions and the total is exactly this amount.
	AgreedTotal *decimal.Decimal `json:"-"`
}

// BookingComponent is a component of a booking with its revenue allocation.
type BookingComponent struct {
	ID               uuid.UUID      `json:"id" db:"id"`
	ComponentID      uuid.UUID      `json:"componentId" db:"component_id"`
	Seq              int            `json:"seq" db:"seq"`
	ComponentType    string         `json:"componentType" db:"component_type"`
	Name             string         `json:"name" db:"name"`
	Quantity         string         `json:"quantity" db:"quantity"`
	ConsumedQuantity string         `json:"consumedQuantity" db:"consumed_quantity"`
	ServiceDate      string         `json:"serviceDate" db:"service_date"`
	ScheduledStart   *time.Time     `json:"scheduledStart" db:"scheduled_start"`
	ScheduledEnd     *time.Time     `json:"scheduledEnd" db:"scheduled_end"`
	ResourceTypeCode *string        `json:"resourceTypeCode" db:"resource_type_code"`
	ResourceID       *uuid.UUID     `json:"resourceId" db:"resource_id"`
	RefCode          *string        `json:"refCode" db:"ref_code"`
	ProductID        *uuid.UUID     `json:"productId" db:"product_id"`
	RecipeID         *uuid.UUID     `json:"recipeId" db:"recipe_id"`
	AllocationRef    *string        `json:"allocationRef" db:"allocation_ref"`
	AllocationID     *uuid.UUID     `json:"allocationId" db:"allocation_id"`
	Details          map[string]any `json:"allocationDetails" db:"allocation_details"`
	AllocatedNet     string         `json:"allocatedNet" db:"allocated_net"`
	AllocatedService string         `json:"allocatedService" db:"allocated_service"`
	AllocatedTax     string         `json:"allocatedTax" db:"allocated_tax"`
	AllocatedTotal   string         `json:"allocatedTotal" db:"allocated_total"`
	RevenueComponent string         `json:"revenueComponent" db:"revenue_component"`
	BusinessLine     string         `json:"businessLine" db:"business_line"`
	Liability        bool           `json:"liability" db:"liability"`
	Optional         bool           `json:"optional" db:"optional"`
	FolioLineID      *uuid.UUID     `json:"folioLineId" db:"folio_line_id"`
	Status           string         `json:"status" db:"status" enum:"unused,consumed,expired,cancelled"`
	ExpiresAt        *time.Time     `json:"expiresAt" db:"expires_at"`
	ConsumedAt       *time.Time     `json:"consumedAt" db:"consumed_at"`
}

// PackageBooking is a booked package.
type PackageBooking struct {
	ID                 uuid.UUID          `json:"id" db:"id"`
	Number             string             `json:"number" db:"number"`
	PackageID          uuid.UUID          `json:"packageId" db:"package_id"`
	PackageCode        string             `json:"packageCode" db:"package_code"`
	PackageName        string             `json:"packageName" db:"package_name"`
	PackageVersion     int                `json:"packageVersion" db:"package_version"`
	CustomerID         *uuid.UUID         `json:"customerId" db:"customer_id"`
	CustomerName       *string            `json:"customerName" db:"customer_name"`
	CorporateAccountID *uuid.UUID         `json:"corporateAccountId" db:"corporate_account_id"`
	GuestName          *string            `json:"guestName" db:"guest_name"`
	GuestPhone         *string            `json:"guestPhone" db:"guest_phone"`
	GuestEmail         *string            `json:"guestEmail" db:"guest_email"`
	StartDate          string             `json:"startDate" db:"start_date"`
	StartAt            time.Time          `json:"startAt" db:"start_at"`
	EndAt              time.Time          `json:"endAt" db:"end_at"`
	Pax                int                `json:"pax" db:"pax"`
	Nights             int                `json:"nights" db:"nights"`
	Addons             []string           `json:"addons" db:"addons"`
	Status             string             `json:"status" db:"status" enum:"pending,confirmed,completed,cancelled,expired"`
	HoldExpiresAt      *time.Time         `json:"holdExpiresAt" db:"hold_expires_at"`
	Currency           string             `json:"currency" db:"currency"`
	ListTotal          string             `json:"listTotal" db:"list_total"`
	DiscountTotal      string             `json:"discountTotal" db:"discount_total"`
	NetTotal           string             `json:"netTotal" db:"net_total"`
	ServiceTotal       string             `json:"serviceTotal" db:"service_total"`
	TaxTotal           string             `json:"taxTotal" db:"tax_total"`
	Total              string             `json:"total" db:"total"`
	FolioID            *uuid.UUID         `json:"folioId" db:"folio_id"`
	CustomerFolioID    *uuid.UUID         `json:"customerFolioId" db:"customer_folio_id"`
	ScheduleID         *uuid.UUID         `json:"scheduleId" db:"schedule_id"`
	SnapshotID         *uuid.UUID         `json:"pricingSnapshotId" db:"snapshot_id"`
	PromoCodes         []string           `json:"promoCodes" db:"promo_codes"`
	Channel            string             `json:"channel" db:"channel"`
	SourceType         *string            `json:"sourceType" db:"source_type"`
	SourceID           *uuid.UUID         `json:"sourceId" db:"source_id"`
	Notes              *string            `json:"notes" db:"notes"`
	ConfirmedAt        *time.Time         `json:"confirmedAt" db:"confirmed_at"`
	CompletedAt        *time.Time         `json:"completedAt" db:"completed_at"`
	CancelReason       *string            `json:"cancelReason" db:"cancel_reason"`
	CancellationFee    *string            `json:"cancellationFee" db:"cancellation_fee"`
	CancelledAt        *time.Time         `json:"cancelledAt" db:"cancelled_at"`
	CreatedAt          time.Time          `json:"createdAt" db:"created_at"`
	Components         []BookingComponent `json:"components" db:"-"`
	Promotions         []Redemption       `json:"promotions" db:"-"`
	Folio              *billing.Summary   `json:"folio" db:"-"`
	Schedule           *billing.Schedule  `json:"schedule" db:"-"`
}

const bookingSelect = `SELECT b.id, b.number, b.package_id, b.package_code, p.name AS package_name, b.package_version, b.customer_id, c.name AS customer_name,
	b.corporate_account_id, b.guest_name, b.guest_phone, b.guest_email, to_char(b.start_date, 'YYYY-MM-DD') AS start_date, b.start_at, b.end_at, b.pax,
	b.nights, b.addons, b.status, b.hold_expires_at, b.currency, trim_scale(b.list_total)::text AS list_total, trim_scale(b.discount_total)::text AS discount_total,
	trim_scale(b.net_total)::text AS net_total, trim_scale(b.service_total)::text AS service_total, trim_scale(b.tax_total)::text AS tax_total,
	trim_scale(b.total)::text AS total, b.folio_id, b.customer_folio_id, b.schedule_id, b.snapshot_id, b.promo_codes, b.channel, b.source_type, b.source_id,
	b.notes, b.confirmed_at, b.completed_at, b.cancel_reason, trim_scale(b.cancellation_fee)::text AS cancellation_fee, b.cancelled_at, b.created_at
	FROM commercial.package_bookings b JOIN commercial.packages p ON p.id = b.package_id LEFT JOIN reporting.customer_directory c ON c.id = b.customer_id`

const bookingComponentSelect = `SELECT id, component_id, seq, component_type, name, trim_scale(quantity)::text AS quantity,
	trim_scale(consumed_quantity)::text AS consumed_quantity, to_char(service_date, 'YYYY-MM-DD') AS service_date, scheduled_start, scheduled_end,
	resource_type_code, resource_id, ref_code, product_id, recipe_id, allocation_ref, allocation_id, allocation_details,
	trim_scale(allocated_net)::text AS allocated_net, trim_scale(allocated_service)::text AS allocated_service, trim_scale(allocated_tax)::text AS allocated_tax,
	trim_scale(allocated_total)::text AS allocated_total, revenue_component, business_line, liability, optional, folio_line_id, status, expires_at, consumed_at
	FROM commercial.package_booking_components`

// GetPackageBooking loads a booking with its components, promotions, folio
// summary and payment schedule.
func GetPackageBooking(ctx context.Context, q dbtx.Querier, bid uuid.UUID) (PackageBooking, error) {
	rows, err := q.Query(ctx, bookingSelect+` WHERE b.id = $1`, bid)
	b, err := handle.One[PackageBooking](rows, err, "package booking")
	if err != nil {
		return b, err
	}
	if b.Components, err = handle.List[BookingComponent](q.Query(ctx, bookingComponentSelect+` WHERE booking_id = $1 ORDER BY seq, id`, bid)); err != nil {
		return b, err
	}
	if b.Promotions, err = Redemptions(ctx, q, SourcePackageBooking, bid); err != nil {
		return b, err
	}
	if b.FolioID != nil {
		s, err := billing.FolioSummary(ctx, q, *b.FolioID)
		if err != nil {
			return b, err
		}
		b.Folio = &s
	}
	if b.ScheduleID != nil {
		s, err := billing.GetSchedule(ctx, q, *b.ScheduleID)
		if err != nil {
			return b, err
		}
		b.Schedule = &s
	}
	if b.Addons == nil {
		b.Addons = []string{}
	}
	if b.PromoCodes == nil {
		b.PromoCodes = []string{}
	}
	return b, nil
}

// bookComp is a component while a booking is made.
type bookComp struct {
	spec                 ComponentSpec
	id                   uuid.UUID
	qty                  decimal.Decimal
	serviceDate          time.Time
	start, end           time.Time
	nights               int
	weight               decimal.Decimal
	net, svc, tax, total decimal.Decimal
	revenueComponent     string
	liability            bool
	alloc                Allocation
}

func multiplier(mode string, pax, nights int) decimal.Decimal {
	n := max(nights, 1)
	switch mode {
	case "per_pax":
		return decimal.NewFromInt(int64(pax))
	case "per_night":
		return decimal.NewFromInt(int64(n))
	case "per_pax_per_night":
		return decimal.NewFromInt(int64(pax * n))
	}
	return decimal.NewFromInt(1)
}

func clockAt(day time.Time, hhmm string, loc *time.Location) time.Time {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		t, _ = time.Parse("15:04", "08:00")
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc)
}

// splitByWeights splits amount pro rata to weights; the last component with
// a weight takes the rounding remainder (FR-PKG-05).
func splitByWeights(amount decimal.Decimal, weights []decimal.Decimal, places int32) []decimal.Decimal {
	out := make([]decimal.Decimal, len(weights))
	for i := range out {
		out[i] = decimal.Zero
	}
	if len(weights) == 0 {
		return out
	}
	wsum := sumDec(weights)
	last := -1
	for i, w := range weights {
		if w.IsPositive() {
			last = i
		}
	}
	if !wsum.IsPositive() || last < 0 {
		out[len(out)-1] = amount
		return out
	}
	given := decimal.Zero
	for i, w := range weights {
		if !w.IsPositive() {
			continue
		}
		if i == last {
			out[i] = amount.Sub(given)
			break
		}
		out[i] = amount.Mul(w).Div(wsum).Round(places)
		given = given.Add(out[i])
	}
	return out
}

// allocationWeights are the list-price weights of the components: the base
// price by the package's allocation method, add-ons by their price.
func allocationWeights(spec PackageSpec, comps []*bookComp, base, mult decimal.Decimal) {
	var included []*bookComp
	for _, c := range comps {
		if c.spec.Optional {
			continue
		}
		included = append(included, c)
	}
	standalone := func(cs []*bookComp, amount decimal.Decimal) {
		s := decimal.Zero
		for _, c := range cs {
			s = s.Add(dec(c.spec.StandalonePrice).Mul(c.qty))
		}
		for _, c := range cs {
			if s.IsPositive() {
				c.weight = amount.Mul(dec(c.spec.StandalonePrice).Mul(c.qty)).Div(s)
			} else if len(cs) > 0 {
				c.weight = amount.Div(decimal.NewFromInt(int64(len(cs))))
			}
		}
	}
	switch spec.AllocationMethod {
	case "fixed", "percent":
		given := decimal.Zero
		var rest []*bookComp
		for _, c := range included {
			if c.spec.AllocationValue == nil {
				rest = append(rest, c)
				continue
			}
			v := dec(*c.spec.AllocationValue)
			if spec.AllocationMethod == "fixed" {
				c.weight = v.Mul(mult)
			} else {
				c.weight = base.Mul(v).Div(hundred)
			}
			given = given.Add(c.weight)
		}
		standalone(rest, decimal.Max(base.Sub(given), decimal.Zero))
	default:
		standalone(included, base)
	}
	for _, c := range comps {
		if c.spec.Optional && c.spec.AddonPrice != nil {
			c.weight = dec(*c.spec.AddonPrice).Mul(c.qty)
		}
	}
}

// customerSegment is the pricing segment of a customer for packages and
// promotions: member when an Active membership grants Member Rate.
func customerSegment(ctx context.Context, q dbtx.Querier, property uuid.UUID, customer *uuid.UUID) (string, error) {
	if customer == nil {
		return "non_member", nil
	}
	seg, err := membership.Segment(ctx, q, property, *customer, "")
	if err != nil || seg == "" {
		return "non_member", err
	}
	return seg, nil
}

// BookPackage books a package in one transaction (all components or none).
func (m *P3Module) BookPackage(ctx context.Context, tx pgx.Tx, property uuid.UUID, in BookingInput, key string) (PackageBooking, error) {
	if key != "" {
		var existing uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM commercial.package_bookings WHERE property_id = $1 AND idempotency_key = $2`, property, key).Scan(&existing)
		if err == nil {
			return GetPackageBooking(ctx, tx, existing)
		}
		if !dbtx.IsNoRows(err) {
			return PackageBooking{}, err
		}
	}
	if in.SourceID != nil && in.SourceType != "" {
		var existing uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM commercial.package_bookings WHERE source_type = $1 AND source_id = $2 AND status NOT IN ('cancelled', 'expired')`,
			in.SourceType, *in.SourceID).Scan(&existing)
		if err == nil {
			return GetPackageBooking(ctx, tx, existing) // a source converted twice books once
		}
		if !dbtx.IsNoRows(err) {
			return PackageBooking{}, err
		}
	}
	var pid uuid.UUID
	switch {
	case in.PackageID != nil:
		pid = *in.PackageID
	case in.PackageCode != "":
		var err error
		if pid, err = packageByCode(ctx, tx, property, in.PackageCode); err != nil {
			return PackageBooking{}, err
		}
	default:
		return PackageBooking{}, handle.Invalid("packageId", "required", "choose a package")
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM commercial.packages WHERE id = $1 AND property_id = $2 FOR UPDATE`, pid, property); err != nil {
		return PackageBooking{}, err
	}
	spec, err := PublishedSpec(ctx, tx, pid)
	if err != nil {
		return PackageBooking{}, err
	}
	loc := calendar.Location(ctx, tx)
	day, err := time.ParseInLocation("2006-01-02", in.StartDate, loc)
	if err != nil {
		return PackageBooking{}, handle.Invalid("startDate", "invalid", "start date YYYY-MM-DD")
	}
	now := clock.Now()
	if in.Pax <= 0 {
		in.Pax = spec.MinPax
	}
	if in.Nights <= 0 {
		in.Nights = spec.Nights
	}
	if in.Channel == "" {
		in.Channel = "back_office"
	}
	if !slices.Contains(PackageChannels, in.Channel) {
		return PackageBooking{}, handle.Invalid("channel", "invalid_channel", "unknown channel "+in.Channel)
	}
	holder := strings.TrimSpace(in.GuestName)
	if in.CustomerID != nil {
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return PackageBooking{}, handle.Invalid("customerId", "not_found", "customer not found")
		}
		if holder == "" {
			holder = c.Name
		}
	}
	if in.CorporateAccountID != nil {
		n, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID)
		if err != nil {
			return PackageBooking{}, handle.Invalid("corporateAccountId", "not_found", "corporate account not found")
		}
		if holder == "" {
			holder = n
		}
	}
	if holder == "" {
		return PackageBooking{}, handle.Invalid("guestName", "required", "choose a customer or enter the guest name")
	}
	segment, err := customerSegment(ctx, tx, property, in.CustomerID)
	if err != nil {
		return PackageBooking{}, err
	}
	today := now.In(loc)
	if p := saleProblem(spec, today, day, in.Pax, in.Channel, segment); p != "" {
		return PackageBooking{}, errs.Conflict("package_unavailable", spec.Name+": "+p)
	}
	if left, err := quotaLeft(ctx, tx, spec, day, uuid.Nil); err != nil {
		return PackageBooking{}, err
	} else if left != nil && *left <= 0 {
		return PackageBooking{}, errs.Conflict("package_sold_out", spec.Name+" is sold out on "+in.StartDate+" (daily quota)")
	}
	// PRD P5 EP-22 (p5_package_capacity.go): choices, sequence & time windows,
	// capacity and time blocks, payment template of the package type
	if bookingRules != nil {
		if err := bookingRules(ctx, tx, property, &spec, &in, day, false); err != nil {
			return PackageBooking{}, err
		}
	}
	cur := currencyOf(ctx, tx)
	pl := places(cur)
	mult := multiplier(spec.PricingMode, in.Pax, in.Nights)
	base := dec(spec.Price).Mul(mult).Round(pl)
	// components to book: included + chosen add-ons
	var comps []*bookComp
	for _, cs := range spec.Components {
		if cs.Optional && !slices.Contains(in.Addons, cs.ID) {
			continue
		}
		q := dec(cs.Quantity)
		if cs.PerPax {
			q = q.Mul(decimal.NewFromInt(int64(in.Pax)))
		}
		if cs.PerNight {
			q = q.Mul(decimal.NewFromInt(int64(max(in.Nights, 1))))
		}
		comps = append(comps, &bookComp{spec: cs, id: id.New(), qty: q})
	}
	for _, a := range in.Addons {
		ok := false
		for _, cs := range spec.Components {
			ok = ok || (cs.ID == a && cs.Optional)
		}
		if !ok {
			return PackageBooking{}, handle.Invalid("addons", "invalid_addon", "add-on not part of this package")
		}
	}
	list := base
	for _, c := range comps {
		if c.spec.Optional && c.spec.AddonPrice != nil {
			list = list.Add(dec(*c.spec.AddonPrice).Mul(c.qty).Round(pl))
		}
	}
	bid := id.New()
	startAt := clockAt(day, spec.StartTime, loc)
	// promotions on the package (business line package, FR-PRM-06)
	promo, err := EvaluatePromotions(ctx, tx, PromoContext{Property: property, At: startAt, Channel: promoChannel(in.Channel), BusinessLine: "package",
		CustomerID: in.CustomerID, Segment: segment, Codes: in.PromoCodes, Currency: cur, Source: &PromoSource{Type: SourcePackageBooking, ID: bid},
		Lines: []PromoLine{{Key: "package", ServiceType: "package", ItemRef: spec.Code, Quantity: decimal.NewFromInt(1), UnitPrice: list}}})
	if err != nil {
		return PackageBooking{}, err
	}
	discount := decimal.Min(promo.TotalDiscount(), list)
	gross := list.Sub(discount)
	taxRules, err := RulesAt(ctx, tx, property, startAt)
	if err != nil {
		return PackageBooking{}, err
	}
	if len(spec.TaxCodes) > 0 {
		var sel []Rule
		for _, t := range taxRules {
			if slices.Contains(spec.TaxCodes, t.Code) {
				sel = append(sel, t)
			}
		}
		taxRules = sel
	}
	if in.AgreedTotal != nil {
		// quotation price: no promotions; the gross is chosen so the total
		// (tax & service included) is the agreed amount
		promo.Applied, promo.Discount = []AppliedPromotion{}, "0"
		gross = grossForTotal(taxRules, *in.AgreedTotal, cur, startAt, spec.TaxMode, pl)
		discount = decimal.Max(list.Sub(gross), decimal.Zero)
		list = gross.Add(discount)
	}
	b := CalculateMode(taxRules, gross, cur, startAt, spec.TaxMode)
	net, svc, tax, total := dec(b.NetAmount), SumKind(b.Lines, "service"), SumKind(b.Lines, "tax"), dec(b.Total)
	// revenue allocation (FR-PKG-05)
	allocationWeights(spec, comps, base, mult)
	weights := make([]decimal.Decimal, len(comps))
	for i, c := range comps {
		weights[i] = c.weight
	}
	nets, svcs, taxes := splitByWeights(net, weights, pl), splitByWeights(svc, weights, pl), splitByWeights(tax, weights, pl)
	for i, c := range comps {
		c.net, c.svc, c.tax = nets[i], svcs[i], taxes[i]
		c.total = c.net.Add(c.svc).Add(c.tax)
		c.revenueComponent, c.liability = c.spec.RevenueComponent, c.spec.Liability
		if c.spec.ComponentType == "voucher" {
			// the voucher defers its share until it is redeemed (P2 voucher liability)
			c.revenueComponent, c.liability = "voucher_deferred", true
		}
	}
	number, err := numbering.Next(ctx, tx, property, "PKB", now.In(loc))
	if err != nil {
		return PackageBooking{}, err
	}
	status := "confirmed"
	var hold *time.Time
	if in.Channel == "website" || in.Channel == "member_app" {
		status = "pending"
		t := now.Add(PackageHoldMinutes * time.Minute)
		hold = &t
	}
	var srcType *string
	if in.SourceType != "" {
		srcType = &in.SourceType
	}
	codes := normCodes(in.PromoCodes)
	if codes == nil {
		codes = []string{}
	}
	addons := []string{}
	for _, a := range in.Addons {
		addons = append(addons, a.String())
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_bookings (id, property_id, number, package_id, package_code, package_version, customer_id,
		corporate_account_id, guest_name, guest_phone, guest_email, start_date, start_at, end_at, pax, nights, addons, status, hold_expires_at, currency,
		list_total, discount_total, net_total, service_total, tax_total, total, promo_codes, channel, source_type, source_id, idempotency_key, notes,
		created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::date,$13,$13,$14,$15,$16,$17,$18,$19,$20::numeric,$21::numeric,$22::numeric,$23::numeric,
		$24::numeric,$25::numeric,$26,$27,$28,$29,$30,$31,$32,$32)`,
		bid, property, number, pid, spec.Code, spec.Version, in.CustomerID, in.CorporateAccountID, nullStr(in.GuestName), nullStr(in.GuestPhone),
		nullStr(in.GuestEmail), in.StartDate, startAt, in.Pax, in.Nights, addons, status, hold, cur, list.String(), discount.String(), net.String(),
		svc.String(), tax.String(), total.String(), codes, in.Channel, srcType, in.SourceID, nullStr(key), nullStr(in.Notes), actorPtr(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return PackageBooking{}, errs.Conflict("duplicate_booking", "this source has already been booked")
		}
		return PackageBooking{}, err
	}
	// allocate every component (all-or-nothing: an error rolls back the transaction)
	endAt := startAt
	for _, c := range comps {
		c.serviceDate = day.AddDate(0, 0, c.spec.DayOffset)
		st := spec.StartTime
		if c.spec.StartTime != nil && *c.spec.StartTime != "" {
			st = *c.spec.StartTime
		}
		c.start = clockAt(c.serviceDate, st, loc)
		c.nights = in.Nights
		if c.spec.Nights != nil {
			c.nights = *c.spec.Nights
		}
		dur := 60
		if c.spec.DurationMinutes != nil {
			dur = *c.spec.DurationMinutes
		} else if c.spec.ComponentType == "tee_time" {
			dur = 120
		}
		c.end = c.start.Add(time.Duration(dur) * time.Minute)
		if c.nights > 0 && (c.spec.ComponentType == "reservation" || c.spec.ComponentType == "banquet") && c.spec.DurationMinutes == nil {
			c.end = c.serviceDate.AddDate(0, 0, c.nights)
		}
		if err := m.allocateComponent(ctx, tx, property, bid, number, spec, in, c, false); err != nil {
			return PackageBooking{}, err
		}
		if c.end.After(endAt) {
			endAt = c.end
		}
	}
	// unused components expire at the end of the package's last day plus the
	// validity days of the package (FR-PKG-06)
	ed := endAt.In(loc)
	expiry := time.Date(ed.Year(), ed.Month(), ed.Day(), 23, 59, 59, 0, loc).AddDate(0, 0, spec.ComponentValidityDays)
	expires := &expiry
	for _, c := range comps {
		details, _ := json.Marshal(nonNilDetails(c.alloc.Details))
		start, end := c.start, c.end
		if c.alloc.Start != nil {
			start = *c.alloc.Start
		}
		if c.alloc.End != nil {
			end = *c.alloc.End
		}
		resourceID := c.spec.ResourceID
		if c.alloc.ResourceID != nil {
			resourceID = c.alloc.ResourceID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_booking_components (id, property_id, booking_id, component_id, seq, component_type, name,
			quantity, service_date, scheduled_start, scheduled_end, resource_type_code, resource_id, ref_code, product_id, recipe_id, allocation_ref,
			allocation_id, allocation_details, allocated_net, allocated_service, allocated_tax, allocated_total, revenue_component, business_line, liability,
			optional, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::date,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::numeric,$21::numeric,$22::numeric,$23::numeric,
			$24,$25,$26,$27,$28)`,
			c.id, property, bid, c.spec.ID, c.spec.Seq, c.spec.ComponentType, c.spec.Name, c.qty.String(), c.serviceDate.Format("2006-01-02"), start, end,
			c.spec.ResourceTypeCode, resourceID, c.spec.RefCode, c.spec.ProductID, c.spec.RecipeID, nullStr(c.alloc.Ref), c.alloc.ID, details,
			c.net.String(), c.svc.String(), c.tax.String(), c.total.String(), c.revenueComponent, c.spec.BusinessLine, c.liability, c.spec.Optional,
			expires); err != nil {
			return PackageBooking{}, err
		}
	}
	// folio: one line per component on the payer's customer folio (FR-BIL-P3-01)
	var custFolio *uuid.UUID
	folio, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: in.CustomerID,
		HolderName: holder, SourceType: "package_booking", SourceID: &bid, SourceRef: number}, BusinessLine: billing.LinePackage})
	if err != nil {
		return PackageBooking{}, err
	}
	if in.CorporateAccountID != nil || in.CustomerID != nil {
		payer := billing.CustomerFolioInput{CustomerID: in.CustomerID}
		if in.CorporateAccountID != nil {
			payer = billing.CustomerFolioInput{CorporateAccountID: in.CorporateAccountID}
		}
		cf, err := m.Billing.AttachFolio(ctx, tx, property, folio.ID, payer)
		if err != nil {
			return PackageBooking{}, err
		}
		custFolio = &cf
	}
	sid, err := packageSnapshot(ctx, tx, property, spec, in, bid, list, b, promo.Applied, startAt, mult)
	if err != nil {
		return PackageBooking{}, err
	}
	for _, c := range comps {
		cid := c.id
		lid, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folio.ID, Description: spec.Name + " — " + c.spec.Name,
			Quantity: c.qty, Net: c.net, Service: c.svc, Tax: c.tax, Total: c.total, SnapshotID: &sid, ReferenceType: "commercial.package_booking_component",
			ReferenceID: &cid, Liability: c.liability}, BusinessLine: billing.LinePackage, RevenueComponent: c.revenueComponent})
		if err != nil {
			return PackageBooking{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.package_booking_components SET folio_line_id = $2 WHERE id = $1`, cid, lid); err != nil {
			return PackageBooking{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.package_bookings SET folio_id = $2, customer_folio_id = $3, snapshot_id = $4, end_at = $5 WHERE id = $1`,
		bid, folio.ID, custFolio, sid, endAt); err != nil {
		return PackageBooking{}, err
	}
	if err := ReplaceRedemptions(ctx, tx, RedemptionInput{Property: property, Source: PromoSource{Type: SourcePackageBooking, ID: bid}, SourceRef: number,
		CustomerID: in.CustomerID, Channel: promoChannel(in.Channel), BusinessLine: "package", Currency: cur}, promo.Applied); err != nil {
		return PackageBooking{}, err
	}
	// payment terms for large packages (FR-BIL-P3-07, PRD P3 §16 #10)
	cfg, _, err := billing.LoadPaymentConfiguration(ctx, tx, property)
	if err != nil {
		return PackageBooking{}, err
	}
	var downPayment decimal.Decimal
	if total.IsPositive() && (len(in.ScheduleLines) > 0 || total.GreaterThanOrEqual(dec(cfg.MinAmountForTerms))) {
		lines := in.ScheduleLines
		if len(lines) == 0 {
			final := day.AddDate(0, 0, -7)
			if final.Before(today) {
				final = today
			}
			lines = []billing.ScheduleLineInput{{Label: "Down Payment " + cfg.MinDownPaymentPercent + "%", Kind: "down_payment",
				Percent: cfg.MinDownPaymentPercent, DueDate: today.Format("2006-01-02")},
				{Label: "Final Payment", Kind: "final", DueDate: final.Format("2006-01-02")}}
		}
		sc, err := m.Billing.CreateSchedule(ctx, tx, property, billing.ScheduleInput{Title: "Package " + number + " · " + spec.Name, FolioID: &folio.ID,
			CustomerID: in.CustomerID, CorporateAccountID: in.CorporateAccountID, SourceType: "package_booking", SourceID: &bid, SourceRef: number,
			TotalAmount: total.String(), Lines: lines})
		if err != nil {
			return PackageBooking{}, err
		}
		if len(sc.Lines) > 0 {
			downPayment = dec(sc.Lines[0].Amount)
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.package_bookings SET schedule_id = $2 WHERE id = $1`, bid, sc.ID); err != nil {
			return PackageBooking{}, err
		}
	}
	if in.Payment != nil && total.IsPositive() {
		amt := total
		if downPayment.IsPositive() {
			amt = downPayment
		}
		if amt, err = handle.Decimal("payment.amount", in.Payment.Amount, amt); err != nil {
			return PackageBooking{}, err
		}
		if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: &folio.ID, MethodType: in.Payment.MethodType, Amount: amt,
			Reference: in.Payment.Reference, Description: "Package " + number, PayerName: holder}); err != nil {
			return PackageBooking{}, err
		}
	}
	out, err := GetPackageBooking(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionCreate, EntityType: "commercial.package_booking",
		EntityID: bid.String(), EntityLabel: number + " · " + spec.Name, PropertyID: &property, After: out}); err != nil {
		return out, err
	}
	if status == "confirmed" {
		if err := m.confirm(ctx, tx, out); err != nil {
			return out, err
		}
		return GetPackageBooking(ctx, tx, bid)
	}
	amount := out.Total
	if out.Schedule != nil && len(out.Schedule.Lines) > 0 {
		amount = out.Schedule.Lines[0].Amount
	}
	holdUntil := ""
	if out.HoldExpiresAt != nil {
		holdUntil = out.HoldExpiresAt.In(loc).Format("02 Jan 2006 15:04")
	}
	return out, m.notifyBooking(ctx, tx, out, NotifyPackagePayment, map[string]any{"amount": amount, "holdUntil": holdUntil})
}

// grossForTotal is the gross amount whose total (tax & service of the mode)
// equals total: nett prices include them, ++ prices are scaled down and the
// rounding difference corrected.
func grossForTotal(rules []Rule, total decimal.Decimal, cur string, at time.Time, mode string, pl int32) decimal.Decimal {
	if mode != "plus_plus" || !total.IsPositive() {
		return total
	}
	probe := CalculateMode(rules, total, cur, at, mode)
	t := dec(probe.Total)
	if !t.IsPositive() {
		return total
	}
	g := total.Mul(total).Div(t).Round(pl)
	for i := 0; i < 3; i++ {
		diff := total.Sub(dec(CalculateMode(rules, g, cur, at, mode).Total))
		if diff.IsZero() {
			break
		}
		g = g.Add(diff)
	}
	return g
}

func nonNilDetails(d map[string]any) map[string]any {
	if d == nil {
		return map[string]any{}
	}
	return d
}

// allocateComponent calls the allocator of a component (when it needs one).
func (m *P3Module) allocateComponent(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, number string, spec PackageSpec, in BookingInput, c *bookComp,
	dry bool) error {
	hasResource := c.spec.ResourceTypeCode != nil || c.spec.ResourceID != nil
	if !needsAllocator(c.spec.ComponentType, hasResource) {
		return nil
	}
	a, ok := allocatorFor(c.spec.ComponentType, hasResource)
	if !ok {
		return errs.Conflict("component_unavailable", c.spec.Name+": no allocator is configured for "+c.spec.ComponentType+" components")
	}
	req := AllocationRequest{Property: property, BookingID: bid, BookingNumber: number, PackageCode: spec.Code, PackageName: spec.Name,
		BookingComponentID: c.id, ComponentType: c.spec.ComponentType, ComponentName: c.spec.Name, ResourceTypeCode: deref(c.spec.ResourceTypeCode),
		ResourceID: c.spec.ResourceID, RefCode: deref(c.spec.RefCode), ServiceDate: c.serviceDate, Start: c.start, End: c.end, Nights: c.nights,
		Quantity: c.qty, Pax: in.Pax, CustomerID: in.CustomerID, CorporateAccountID: in.CorporateAccountID, GuestName: in.GuestName,
		GuestPhone: in.GuestPhone, GuestEmail: in.GuestEmail, Channel: in.Channel, Value: c.total, DryRun: dry}
	if req.GuestName == "" && in.CustomerID != nil {
		if cust, err := crm.GetCustomer(ctx, tx, *in.CustomerID); err == nil {
			req.GuestName = cust.Name
		}
	}
	alloc, err := a.Allocate(ctx, tx, req)
	if err != nil {
		if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
			return errs.Conflict("component_unavailable", c.spec.Name+": "+de.Message)
		}
		return err
	}
	c.alloc = alloc
	return nil
}

// packageSnapshot stores the immutable pricing snapshot of a booking.
func packageSnapshot(ctx context.Context, tx pgx.Tx, property uuid.UUID, spec PackageSpec, in BookingInput, bid uuid.UUID, list decimal.Decimal,
	b Breakdown, applied []AppliedPromotion, at time.Time, mult decimal.Decimal) (uuid.UUID, error) {
	sid := id.New()
	taxLines, _ := json.Marshal(b.Lines)
	cx, _ := json.Marshal(map[string]any{"bookingId": bid, "packageId": spec.ID, "version": spec.Version, "pricingMode": spec.PricingMode,
		"price": spec.Price, "multiplier": mult.String(), "pax": in.Pax, "nights": in.Nights, "addons": in.Addons, "allocationMethod": spec.AllocationMethod})
	_, err := tx.Exec(ctx, `INSERT INTO commercial.pricing_snapshots (id, property_id, charge_type, service_type, item_ref, unit, quantity, list_price,
		gross_amount, net_amount, service_amount, tax_amount, total, currency, pricing_mode, tax_service, context, play_at, channel, created_by, promotions)
		VALUES ($1,$2,'package','package',$3,'package',1,$4::numeric,$4::numeric,$5::numeric,$6::numeric,$7::numeric,$8::numeric,$9,$10,$11,$12,$13,$14,$15,$16)`,
		sid, property, spec.Code, list.String(), b.NetAmount, SumKind(b.Lines, "service").String(), SumKind(b.Lines, "tax").String(), b.Total, b.Currency,
		b.PricingMode, taxLines, cx, at, in.Channel, actorPtr(ctx), SnapshotPromotions(applied))
	return sid, err
}

// ── confirmation & events (K3) ────────────────────────────────────────────

// PackageBookedComponent is one component of commercial.package_booked.
type PackageBookedComponent struct {
	BookingComponentID uuid.UUID  `json:"bookingComponentId"`
	ComponentID        uuid.UUID  `json:"componentId"`
	ComponentType      string     `json:"componentType"`
	ResourceTypeCode   *string    `json:"resourceTypeCode"`
	AllocationRef      *string    `json:"allocationRef"`
	ServiceDate        string     `json:"serviceDate"`
	Quantity           string     `json:"quantity"`
	AllocatedNet       string     `json:"allocatedNet"`
	AllocatedService   string     `json:"allocatedService"`
	AllocatedTax       string     `json:"allocatedTax"`
	AllocatedTotal     string     `json:"allocatedTotal"`
	RevenueComponent   string     `json:"revenueComponent"`
	Name               string     `json:"name"`
	BusinessLine       string     `json:"businessLine"`
	Liability          bool       `json:"liability"`
	AllocationID       *uuid.UUID `json:"allocationId"`
	ResourceID         *uuid.UUID `json:"resourceId"`
	ScheduledStart     *time.Time `json:"scheduledStart"`
	ScheduledEnd       *time.Time `json:"scheduledEnd"`
}

// PackageBookedPayload is commercial.package_booked (contract; fields after
// components are additive).
type PackageBookedPayload struct {
	BookingID          uuid.UUID                `json:"bookingId"`
	Number             string                   `json:"number"`
	PackageID          uuid.UUID                `json:"packageId"`
	PackageCode        string                   `json:"packageCode"`
	Version            int                      `json:"version"`
	CustomerID         *uuid.UUID               `json:"customerId"`
	FolioID            *uuid.UUID               `json:"folioId"`
	StartDate          string                   `json:"startDate"`
	EndDate            string                   `json:"endDate"`
	Pax                int                      `json:"pax"`
	Total              string                   `json:"total"`
	Currency           string                   `json:"currency"`
	Components         []PackageBookedComponent `json:"components"`
	CorporateAccountID *uuid.UUID               `json:"corporateAccountId"`
	Nights             int                      `json:"nights"`
	Channel            string                   `json:"channel"`
	Discount           string                   `json:"discount"`
	Net                string                   `json:"net"`
	Service            string                   `json:"service"`
	Tax                string                   `json:"tax"`
}

// confirm makes a booking Confirmed: promotions redeemed (K3) and
// commercial.package_booked published for the business lines.
func (m *P3Module) confirm(ctx context.Context, tx pgx.Tx, b PackageBooking) error {
	if _, err := tx.Exec(ctx, `UPDATE commercial.package_bookings SET status = 'confirmed', confirmed_at = now(), hold_expires_at = NULL
		WHERE id = $1 AND status IN ('pending', 'confirmed')`, b.ID); err != nil {
		return err
	}
	if err := RedeemSource(ctx, tx, m.Events, PromoSource{Type: SourcePackageBooking, ID: b.ID}, SourcePackageBooking, b.ID, b.FolioID); err != nil {
		return err
	}
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.package_bookings WHERE id = $1`, b.ID).Scan(&property); err != nil {
		return err
	}
	loc := calendar.Location(ctx, tx)
	p := PackageBookedPayload{BookingID: b.ID, Number: b.Number, PackageID: b.PackageID, PackageCode: b.PackageCode, Version: b.PackageVersion,
		CustomerID: b.CustomerID, FolioID: b.FolioID, StartDate: b.StartDate, EndDate: b.EndAt.In(loc).Format("2006-01-02"), Pax: b.Pax, Total: b.Total,
		Currency: b.Currency, Components: []PackageBookedComponent{}, CorporateAccountID: b.CorporateAccountID, Nights: b.Nights, Channel: b.Channel,
		Discount: b.DiscountTotal, Net: b.NetTotal, Service: b.ServiceTotal, Tax: b.TaxTotal}
	for _, c := range b.Components {
		p.Components = append(p.Components, PackageBookedComponent{BookingComponentID: c.ID, ComponentID: c.ComponentID, ComponentType: c.ComponentType,
			ResourceTypeCode: c.ResourceTypeCode, AllocationRef: c.AllocationRef, ServiceDate: c.ServiceDate, Quantity: c.Quantity, AllocatedNet: c.AllocatedNet,
			AllocatedService: c.AllocatedService, AllocatedTax: c.AllocatedTax, AllocatedTotal: c.AllocatedTotal, RevenueComponent: c.RevenueComponent,
			Name: c.Name, BusinessLine: c.BusinessLine, Liability: c.Liability, AllocationID: c.AllocationID, ResourceID: c.ResourceID,
			ScheduledStart: c.ScheduledStart, ScheduledEnd: c.ScheduledEnd})
	}
	if _, err := m.Events.Publish(ctx, tx, EventPackageBooked, "commercial.package_booking", &b.ID, &property, p); err != nil {
		return err
	}
	return m.notifyBooking(ctx, tx, b, NotifyPackageConfirmed, nil)
}

// OnPaymentSettled confirms a pending (website / app) package booking when
// a payment on its folio settles (billing.payment_settled subscriber).
func (m *P3Module) OnPaymentSettled(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		FolioID    *uuid.UUID `json:"folioId"`
		SourceType *string    `json:"sourceType"`
	}
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.FolioID == nil || p.SourceType == nil || *p.SourceType != "package_booking" {
		return nil
	}
	var bid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM commercial.package_bookings WHERE folio_id = $1 AND status = 'pending' FOR UPDATE`, *p.FolioID).Scan(&bid)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := GetPackageBooking(ctx, tx, bid)
	if err != nil {
		return err
	}
	if err := m.confirm(ctx, tx, b); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "confirm", EntityType: "commercial.package_booking", EntityID: bid.String(),
		EntityLabel: b.Number, PropertyID: e.PropertyID, Before: map[string]any{"status": "pending"}, After: map[string]any{"status": "confirmed"},
		Metadata: map[string]any{"event": e.ID}})
}

// ── cancellation (FR-PKG-08) ──────────────────────────────────────────────

// PackageCancelInput cancels a booking.
type PackageCancelInput struct {
	Reason string `json:"reason"`
}

// PackageCancelResult reports a cancellation.
type PackageCancelResult struct {
	Booking  PackageBooking `json:"booking"`
	Fee      string         `json:"fee"`
	Released int            `json:"released" doc:"Components whose allocation was released"`
	Refunds  int            `json:"refunds" doc:"Refunds requested for the overpaid amount"`
}

// CancelBooking cancels the unused components of a booking: allocations
// are released, their charges voided, the late cancellation fee of the
// package posted and the overpayment refunded; consumed components stay.
func (m *P3Module) CancelBooking(ctx context.Context, tx pgx.Tx, bid uuid.UUID, reason, status string) (PackageCancelResult, error) {
	if strings.TrimSpace(reason) == "" {
		return PackageCancelResult{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if status == "" {
		status = "cancelled"
	}
	var property uuid.UUID
	var cur string
	if err := tx.QueryRow(ctx, `SELECT property_id, status, currency FROM commercial.package_bookings WHERE id = $1 FOR UPDATE`, bid).Scan(&property, new(string), &cur); err != nil {
		if dbtx.IsNoRows(err) {
			return PackageCancelResult{}, errs.NotFound("package booking")
		}
		return PackageCancelResult{}, err
	}
	b, err := GetPackageBooking(ctx, tx, bid)
	if err != nil {
		return PackageCancelResult{}, err
	}
	if b.Status != "pending" && b.Status != "confirmed" {
		return PackageCancelResult{}, errs.Conflict("invalid_status", "a "+b.Status+" package booking cannot be cancelled")
	}
	spec, err := VersionSpec(ctx, tx, b.PackageID, b.PackageVersion)
	if err != nil {
		return PackageCancelResult{}, err
	}
	out := PackageCancelResult{Fee: "0"}
	if b.FolioID != nil {
		if st, err := billing.FolioStatus(ctx, tx, *b.FolioID); err != nil {
			return out, err
		} else if st == "closed" {
			if err := m.Billing.ReopenFolio(ctx, tx, *b.FolioID, "package cancelled: "+reason); err != nil {
				return out, err
			}
		}
	}
	unused := decimal.Zero
	consumed := false
	for _, c := range b.Components {
		if c.Status != "unused" {
			consumed = consumed || c.Status == "consumed"
			continue
		}
		if dec(c.ConsumedQuantity).IsPositive() {
			consumed = true
		}
		if c.AllocationID != nil || c.AllocationRef != nil {
			hasResource := c.ResourceTypeCode != nil || c.ResourceID != nil
			if a, ok := allocatorFor(c.ComponentType, hasResource); ok && a.Release != nil {
				if err := a.Release(ctx, tx, ReleaseRequest{Property: property, BookingID: bid, BookingComponentID: c.ID, ComponentType: c.ComponentType,
					AllocationID: c.AllocationID, AllocationRef: deref(c.AllocationRef), Details: c.Details, Reason: reason}); err != nil {
					return out, err
				}
				out.Released++
			}
		}
		if _, err := m.Billing.VoidSourceCharges(ctx, tx, "commercial.package_booking_component", c.ID, "package cancelled: "+reason); err != nil {
			return out, err
		}
		unused = unused.Add(dec(c.AllocatedTotal))
		if _, err := tx.Exec(ctx, `UPDATE commercial.package_booking_components SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, c.ID); err != nil {
			return out, err
		}
	}
	fee := decimal.Zero
	pct := dec(spec.CancellationFeePercent)
	if status == "cancelled" && pct.IsPositive() && b.Status == "confirmed" && b.StartAt.Sub(clock.Now()) < time.Duration(spec.CancellationHours)*time.Hour {
		fee = unused.Mul(pct).Div(hundred).Round(places(cur))
	}
	if fee.IsPositive() && b.FolioID != nil {
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *b.FolioID, ChargeType: "cancellation_fee",
			Description: "Cancellation fee " + b.Number, Net: fee, ReferenceType: "commercial.package_booking", ReferenceID: &bid},
			BusinessLine: billing.LinePackage, RevenueComponent: "cancellation_fee"}); err != nil {
			return out, err
		}
	}
	if b.ScheduleID != nil && !consumed {
		if _, err := m.Billing.CancelSchedule(ctx, tx, *b.ScheduleID, reason); err != nil {
			return out, err
		}
	}
	if b.FolioID != nil {
		if err := m.Billing.CancelPending(ctx, tx, *b.FolioID, "package cancelled"); err != nil {
			return out, err
		}
		refunds, err := m.Billing.RefundFolio(ctx, tx, *b.FolioID, "Package "+b.Number+" cancelled", nil)
		if err != nil {
			return out, err
		}
		out.Refunds = len(refunds)
	}
	if !consumed {
		if err := ReverseSource(ctx, tx, PromoSource{Type: SourcePackageBooking, ID: bid}, reason); err != nil {
			return out, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.package_bookings SET status = $2, cancel_reason = $3, cancellation_fee = $4::numeric, cancelled_at = now(),
		cancelled_by = $5, hold_expires_at = NULL, updated_by = $5 WHERE id = $1`, bid, status, reason, fee.String(), actorPtr(ctx)); err != nil {
		return out, err
	}
	out.Fee = fee.String()
	if out.Booking, err = GetPackageBooking(ctx, tx, bid); err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionStatusChange, EntityType: "commercial.package_booking",
		EntityID: bid.String(), EntityLabel: b.Number, PropertyID: &property, Reason: reason, Before: map[string]any{"status": b.Status},
		After: map[string]any{"status": status, "fee": out.Fee, "released": out.Released}}); err != nil {
		return out, err
	}
	if b.Status == "confirmed" {
		if _, err = m.Events.Publish(ctx, tx, EventPackageCancelled, "commercial.package_booking", &bid, &property, map[string]any{"bookingId": bid,
			"number": b.Number, "packageId": b.PackageID, "packageCode": b.PackageCode, "status": status, "reason": reason, "fee": out.Fee}); err != nil {
			return out, err
		}
	}
	return out, m.notifyBooking(ctx, tx, out.Booking, NotifyPackageCancelled, map[string]any{"reason": reason, "fee": out.Fee})
}

// ── consumption (FR-PKG-06, FR-PKG-07, K6) ────────────────────────────────

// ConsumeInput uses (part of) a component.
type ConsumeInput struct {
	BookingComponentID uuid.UUID  `json:"bookingComponentId"`
	Quantity           string     `json:"quantity,omitempty" doc:"Default: everything left"`
	OutletID           *uuid.UUID `json:"outletId,omitempty"`
	Reference          string     `json:"reference,omitempty" doc:"POS order, check-in, voucher redemption …"`
}

// ConsumptionLine is one BOM item of a consumption (base UOM).
type ConsumptionLine struct {
	ItemID   uuid.UUID `json:"itemId"`
	Quantity string    `json:"quantity"`
	UOMID    uuid.UUID `json:"uomId"`
}

// ConsumptionRevenue is the revenue share of a consumption.
type ConsumptionRevenue struct {
	RevenueComponent string `json:"revenueComponent"`
	Net              string `json:"net"`
	Service          string `json:"service"`
	Tax              string `json:"tax"`
	Total            string `json:"total"`
	Currency         string `json:"currency"`
}

// PackageConsumption is a recorded use of a component.
type PackageConsumption struct {
	ID                 uuid.UUID          `json:"id"`
	BookingID          uuid.UUID          `json:"bookingId"`
	BookingComponentID uuid.UUID          `json:"bookingComponentId"`
	Quantity           string             `json:"quantity"`
	BusinessDate       string             `json:"businessDate"`
	ComponentStatus    string             `json:"componentStatus"`
	BookingStatus      string             `json:"bookingStatus"`
	PackageConsumption []ConsumptionLine  `json:"consumption"`
	Revenue            ConsumptionRevenue `json:"revenue"`
	Duplicate          bool               `json:"duplicate"`
}

// PackageConsumedPayload is commercial.package_consumed (K6); fields after
// revenue are additive.
type PackageConsumedPayload struct {
	BookingID          uuid.UUID          `json:"bookingId"`
	BookingComponentID uuid.UUID          `json:"bookingComponentId"`
	PackageID          uuid.UUID          `json:"packageId"`
	ComponentType      string             `json:"componentType"`
	ConsumedAt         time.Time          `json:"consumedAt"`
	BusinessDate       string             `json:"businessDate"`
	Quantity           string             `json:"quantity"`
	OutletID           *uuid.UUID         `json:"outletId"`
	RecipeID           *uuid.UUID         `json:"recipeId"`
	ProductID          *uuid.UUID         `json:"productId"`
	PackageConsumption []ConsumptionLine  `json:"consumption"`
	Revenue            ConsumptionRevenue `json:"revenue"`
	ConsumptionID      uuid.UUID          `json:"consumptionId"`
	BookingNumber      string             `json:"bookingNumber"`
	PackageCode        string             `json:"packageCode"`
	BusinessLine       string             `json:"businessLine"`
	Reference          string             `json:"reference,omitempty"`
}

// Consume records the use of a component: its share of the allocated
// revenue and the BOM of its recipe (inventory.ExplodeRecipe) are published
// as commercial.package_consumed for stock deduction and revenue (P4).
func (m *P3Module) Consume(ctx context.Context, tx pgx.Tx, bid uuid.UUID, in ConsumeInput, key string) (PackageConsumption, error) {
	var property uuid.UUID
	var bstatus, number, pcode, cur string
	var pkg uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id, status, number, package_code, package_id, currency FROM commercial.package_bookings WHERE id = $1 FOR UPDATE`, bid).
		Scan(&property, &bstatus, &number, &pcode, &pkg, &cur); err != nil {
		if dbtx.IsNoRows(err) {
			return PackageConsumption{}, errs.NotFound("package booking")
		}
		return PackageConsumption{}, err
	}
	if key != "" {
		var cid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM commercial.package_consumptions WHERE booking_component_id = $1 AND idempotency_key = $2`,
			in.BookingComponentID, key).Scan(&cid)
		if err == nil {
			out, err := consumptionView(ctx, tx, cid)
			out.Duplicate = true
			return out, err
		}
		if !dbtx.IsNoRows(err) {
			return PackageConsumption{}, err
		}
	}
	if bstatus != "confirmed" {
		return PackageConsumption{}, errs.Conflict("invalid_status", "only Confirmed package bookings can be used (is "+bstatus+")")
	}
	rows, err := tx.Query(ctx, bookingComponentSelect+` WHERE id = $1 AND booking_id = $2 FOR UPDATE`, in.BookingComponentID, bid)
	c, err := handle.One[BookingComponent](rows, err, "package component")
	if err != nil {
		return PackageConsumption{}, err
	}
	if c.Status != "unused" {
		return PackageConsumption{}, errs.Conflict("component_"+c.Status, c.Name+" is "+c.Status)
	}
	qty, consumedBefore := dec(c.Quantity), dec(c.ConsumedQuantity)
	left := qty.Sub(consumedBefore)
	use, err := handle.Decimal("quantity", in.Quantity, left)
	if err != nil {
		return PackageConsumption{}, err
	}
	if !use.IsPositive() || use.GreaterThan(left) {
		return PackageConsumption{}, handle.Invalid("quantity", "invalid_quantity", "between 0 and the "+left.String()+" left")
	}
	pl := places(cur)
	share := func(allocated string, column string) (decimal.Decimal, error) {
		a := dec(allocated)
		if use.Equal(left) {
			var before string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(`+column+`), 0)::text FROM commercial.package_consumptions WHERE booking_component_id = $1`,
				c.ID).Scan(&before); err != nil {
				return decimal.Zero, err
			}
			return a.Sub(dec(before)), nil // the last use takes the remainder
		}
		return a.Mul(use).Div(qty).Round(pl), nil
	}
	net, err := share(c.AllocatedNet, "net")
	if err != nil {
		return PackageConsumption{}, err
	}
	svc, err := share(c.AllocatedService, "service")
	if err != nil {
		return PackageConsumption{}, err
	}
	tax, err := share(c.AllocatedTax, "tax")
	if err != nil {
		return PackageConsumption{}, err
	}
	total := net.Add(svc).Add(tax)
	// BOM of the component (P2 recipes) for P4 stock deduction
	recipe := c.RecipeID
	if recipe == nil && c.ProductID != nil {
		if recipe, err = inventory.ProductRecipe(ctx, tx, *c.ProductID); err != nil {
			return PackageConsumption{}, err
		}
	}
	lines := []ConsumptionLine{}
	if recipe != nil {
		reqs, err := inventory.ExplodeRecipe(ctx, tx, *recipe, use)
		if err != nil {
			return PackageConsumption{}, err
		}
		for _, r := range reqs {
			lines = append(lines, ConsumptionLine{ItemID: r.ItemID, Quantity: r.Quantity.String(), UOMID: r.UOMID})
		}
	}
	bd, err := billing.CurrentBusinessDate(ctx, tx, property)
	if err != nil {
		return PackageConsumption{}, err
	}
	now := clock.Now()
	cid := id.New()
	raw, _ := json.Marshal(lines)
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_consumptions (id, property_id, booking_id, booking_component_id, quantity, business_date,
		outlet_id, reference, net, service, tax, total, consumption, idempotency_key, consumed_at, created_by)
		VALUES ($1,$2,$3,$4,$5::numeric,$6::date,$7,$8,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13,$14,$15,$16)`,
		cid, property, bid, c.ID, use.String(), bd.Format("2006-01-02"), in.OutletID, nullStr(in.Reference), net.String(), svc.String(), tax.String(),
		total.String(), raw, nullStr(key), now, actorPtr(ctx)); err != nil {
		return PackageConsumption{}, err
	}
	newStatus := "unused"
	if use.Equal(left) {
		newStatus = "consumed"
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.package_booking_components SET consumed_quantity = consumed_quantity + $2::numeric, status = $3,
		consumed_at = CASE WHEN $3 = 'consumed' THEN now() ELSE consumed_at END WHERE id = $1`, c.ID, use.String(), newStatus); err != nil {
		return PackageConsumption{}, err
	}
	bstatus, err = completeIfDone(ctx, tx, bid)
	if err != nil {
		return PackageConsumption{}, err
	}
	rev := ConsumptionRevenue{RevenueComponent: c.RevenueComponent, Net: net.String(), Service: svc.String(), Tax: tax.String(), Total: total.String(), Currency: cur}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "consume", EntityType: "commercial.package_booking", EntityID: bid.String(),
		EntityLabel: number + " · " + c.Name, PropertyID: &property, Reason: in.Reference,
		After: map[string]any{"componentId": c.ID, "quantity": use.String(), "status": newStatus, "revenue": rev}}); err != nil {
		return PackageConsumption{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventPackageConsumed, "commercial.package_booking", &bid, &property, PackageConsumedPayload{BookingID: bid,
		BookingComponentID: c.ID, PackageID: pkg, ComponentType: c.ComponentType, ConsumedAt: now, BusinessDate: bd.Format("2006-01-02"),
		Quantity: use.String(), OutletID: in.OutletID, RecipeID: recipe, ProductID: c.ProductID, PackageConsumption: lines, Revenue: rev, ConsumptionID: cid,
		BookingNumber: number, PackageCode: pcode, BusinessLine: c.BusinessLine, Reference: in.Reference}); err != nil {
		return PackageConsumption{}, err
	}
	return PackageConsumption{ID: cid, BookingID: bid, BookingComponentID: c.ID, Quantity: use.String(), BusinessDate: bd.Format("2006-01-02"),
		ComponentStatus: newStatus, BookingStatus: bstatus, PackageConsumption: lines, Revenue: rev}, nil
}

func consumptionView(ctx context.Context, q dbtx.Querier, cid uuid.UUID) (PackageConsumption, error) {
	var out PackageConsumption
	var raw []byte
	var net, svc, tax, total, cur, comp, cstatus, bstatus string
	var bd time.Time
	err := q.QueryRow(ctx, `SELECT pc.id, pc.booking_id, pc.booking_component_id, trim_scale(pc.quantity)::text, pc.business_date, pc.consumption,
		trim_scale(pc.net)::text, trim_scale(pc.service)::text, trim_scale(pc.tax)::text, trim_scale(pc.total)::text, b.currency, c.revenue_component,
		c.status, b.status FROM commercial.package_consumptions pc JOIN commercial.package_booking_components c ON c.id = pc.booking_component_id
		JOIN commercial.package_bookings b ON b.id = pc.booking_id WHERE pc.id = $1`, cid).
		Scan(&out.ID, &out.BookingID, &out.BookingComponentID, &out.Quantity, &bd, &raw, &net, &svc, &tax, &total, &cur, &comp, &cstatus, &bstatus)
	if err != nil {
		return out, err
	}
	out.BusinessDate = bd.Format("2006-01-02")
	_ = json.Unmarshal(raw, &out.PackageConsumption)
	out.Revenue = ConsumptionRevenue{RevenueComponent: comp, Net: net, Service: svc, Tax: tax, Total: total, Currency: cur}
	out.ComponentStatus, out.BookingStatus = cstatus, bstatus
	return out, nil
}

// completeIfDone completes a booking whose components are all used,
// expired or cancelled.
func completeIfDone(ctx context.Context, tx pgx.Tx, bid uuid.UUID) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `UPDATE commercial.package_bookings b SET status = 'completed', completed_at = now()
		WHERE b.id = $1 AND b.status = 'confirmed' AND NOT EXISTS (SELECT 1 FROM commercial.package_booking_components c WHERE c.booking_id = b.id
		AND c.status = 'unused') RETURNING status`, bid).Scan(&status)
	if dbtx.IsNoRows(err) {
		err = tx.QueryRow(ctx, `SELECT status FROM commercial.package_bookings WHERE id = $1`, bid).Scan(&status)
	}
	return status, err
}

// ConsumeAllocation consumes the component allocated as allocationID (a
// reservation checked in, a tee time teed off); no-op for other ids.
func (m *P3Module) ConsumeAllocation(ctx context.Context, tx pgx.Tx, allocationID uuid.UUID, reference string) error {
	var bid, cid uuid.UUID
	var ctype string
	err := tx.QueryRow(ctx, `SELECT c.booking_id, c.id, c.component_type FROM commercial.package_booking_components c
		JOIN commercial.package_bookings b ON b.id = c.booking_id
		WHERE (c.allocation_id = $1 OR c.allocation_details->'voucherIds' ? $1::text) AND c.status = 'unused' AND b.status = 'confirmed' LIMIT 1`,
		allocationID).Scan(&bid, &cid, &ctype)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	qty := ""
	if ctype == "voucher" {
		qty = "1" // one of the vouchers of the component
	}
	_, err = m.Consume(ctx, tx, bid, ConsumeInput{BookingComponentID: cid, Quantity: qty, Reference: reference}, "auto-"+allocationID.String())
	return err
}

// ── expiry (FR-PKG-06) ────────────────────────────────────────────────────

// RunPackageExpiry expires unpaid website / app holds and unused components
// past their validity, then completes finished bookings (job, exported for
// tests and operations).
func (m *P3Module) RunPackageExpiry(ctx context.Context) (holds, components int, err error) {
	sys := dbtx.System(ctx)
	err = m.DB.WithTx(sys, func(tx pgx.Tx) error {
		rows, err := tx.Query(sys, `SELECT id FROM commercial.package_bookings WHERE status = 'pending' AND hold_expires_at < now() ORDER BY created_at LIMIT 200`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		for _, bid := range ids {
			if _, err := m.CancelBooking(sys, tx, bid, "payment not received before the hold expired", "expired"); err != nil {
				return err
			}
			holds++
		}
		rows, err = tx.Query(sys, `UPDATE commercial.package_booking_components c SET status = 'expired' FROM commercial.package_bookings b
			WHERE b.id = c.booking_id AND b.status = 'confirmed' AND c.status = 'unused' AND c.expires_at < now() RETURNING c.booking_id, c.property_id, c.name`)
		if err != nil {
			return err
		}
		type exp struct {
			bid, pid uuid.UUID
			name     string
		}
		var list []exp
		for rows.Next() {
			var x exp
			if err := rows.Scan(&x.bid, &x.pid, &x.name); err != nil {
				rows.Close()
				return err
			}
			list = append(list, x)
		}
		rows.Close()
		for _, x := range list {
			components++
			if _, err := completeIfDone(sys, tx, x.bid); err != nil {
				return err
			}
			pid := x.pid
			if err := audit.Record(sys, tx, audit.Entry{Module: "commercial", Action: "expire", EntityType: "commercial.package_booking",
				EntityID: x.bid.String(), EntityLabel: x.name, PropertyID: &pid, After: map[string]any{"component": x.name, "status": "expired"}}); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && (holds > 0 || components > 0) {
		slog.InfoContext(ctx, "package expiry", "holds", holds, "components", components)
	}
	return holds, components, err
}

// ── availability (FR-PKG-03) ──────────────────────────────────────────────

// ComponentAvailability tells whether one component can be allocated.
type ComponentAvailability struct {
	Seq           int    `json:"seq"`
	Name          string `json:"name"`
	ComponentType string `json:"componentType"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	Ref           string `json:"ref,omitempty" doc:"What would be allocated (resource, tee time …)"`
}

// DayAvailability is the availability of a package on a start date.
type DayAvailability struct {
	Date       string                  `json:"date"`
	Available  bool                    `json:"available"`
	Reason     string                  `json:"reason,omitempty"`
	QuotaLeft  *int                    `json:"quotaLeft"`
	Price      string                  `json:"price" doc:"List price for the pax and nights (before promotions)"`
	Components []ComponentAvailability `json:"components"`
}

// Availability checks a package for consecutive start dates: the sale
// rules, the daily quota and every component (each allocation is tried in
// a savepoint that is rolled back — nothing is held).
func (m *P3Module) Availability(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID, from time.Time, days, pax, nights int, channel string,
	customer *uuid.UUID) ([]DayAvailability, error) {
	spec, err := PublishedSpec(ctx, tx, pid)
	if err != nil {
		return nil, err
	}
	loc := calendar.Location(ctx, tx)
	if pax <= 0 {
		pax = spec.MinPax
	}
	if nights <= 0 {
		nights = spec.Nights
	}
	segment, err := customerSegment(ctx, tx, property, customer)
	if err != nil {
		return nil, err
	}
	today := clock.Now().In(loc)
	cur := currencyOf(ctx, tx)
	var out []DayAvailability
	for d := 0; d < max(days, 1); d++ {
		day := time.Date(from.Year(), from.Month(), from.Day()+d, 0, 0, 0, 0, loc)
		da := DayAvailability{Date: day.Format("2006-01-02"), Components: []ComponentAvailability{},
			Price: dec(spec.Price).Mul(multiplier(spec.PricingMode, pax, nights)).StringFixed(places(cur))}
		if p := saleProblem(spec, today, day, pax, channel, segment); p != "" {
			da.Reason = p
			out = append(out, da)
			continue
		}
		left, err := quotaLeft(ctx, tx, spec, day, uuid.Nil)
		if err != nil {
			return nil, err
		}
		da.QuotaLeft = left
		if left != nil && *left <= 0 {
			da.Reason = "sold out (daily quota)"
			out = append(out, da)
			continue
		}
		// PRD P5 EP-22: default choices, schedule and capacity of the day
		dspec := spec
		dspec.Components = slices.Clone(spec.Components)
		if bookingRules != nil {
			if err := bookingRules(ctx, tx, property, &dspec, &BookingInput{Pax: pax, Nights: nights, Channel: channel, CustomerID: customer}, day,
				true); err != nil {
				de, ok := errs.As(err)
				if !ok || de.Kind == errs.KindInternal {
					return nil, err
				}
				da.Reason = de.Message
				out = append(out, da)
				continue
			}
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		da.Available = true
		in := BookingInput{Pax: pax, Nights: nights, Channel: channel, CustomerID: customer, GuestName: "Availability check"}
		for _, cs := range dspec.Components {
			if cs.Optional {
				continue
			}
			q := dec(cs.Quantity)
			if cs.PerPax {
				q = q.Mul(decimal.NewFromInt(int64(pax)))
			}
			if cs.PerNight {
				q = q.Mul(decimal.NewFromInt(int64(max(nights, 1))))
			}
			c := &bookComp{spec: cs, id: id.New(), qty: q, serviceDate: day.AddDate(0, 0, cs.DayOffset), nights: nights}
			st := spec.StartTime
			if cs.StartTime != nil && *cs.StartTime != "" {
				st = *cs.StartTime
			}
			c.start = clockAt(c.serviceDate, st, loc)
			if cs.Nights != nil {
				c.nights = *cs.Nights
			}
			dur := 60
			if cs.DurationMinutes != nil {
				dur = *cs.DurationMinutes
			} else if cs.ComponentType == "tee_time" {
				dur = 120
			}
			c.end = c.start.Add(time.Duration(dur) * time.Minute)
			if c.nights > 0 && (cs.ComponentType == "reservation" || cs.ComponentType == "banquet") && cs.DurationMinutes == nil {
				c.end = c.serviceDate.AddDate(0, 0, c.nights)
			}
			ca := ComponentAvailability{Seq: cs.Seq, Name: cs.Name, ComponentType: cs.ComponentType, Available: true}
			inner, err := sp.Begin(ctx)
			if err != nil {
				_ = sp.Rollback(ctx)
				return nil, err
			}
			if err := m.allocateComponent(ctx, inner, property, id.New(), "AVAILABILITY", spec, in, c, true); err != nil {
				_ = inner.Rollback(ctx)
				de, ok := errs.As(err)
				if !ok || de.Kind == errs.KindInternal {
					_ = sp.Rollback(ctx)
					return nil, err
				}
				ca.Available, ca.Reason = false, de.Message
				da.Available = false
				if da.Reason == "" {
					da.Reason = de.Message
				}
			} else {
				ca.Ref = c.alloc.Ref
				if err := inner.Commit(ctx); err != nil {
					_ = sp.Rollback(ctx)
					return nil, err
				}
			}
			da.Components = append(da.Components, ca)
		}
		if err := sp.Rollback(ctx); err != nil {
			return nil, err
		}
		out = append(out, da)
	}
	return out, nil
}

// OnQuotationAccepted books the package of an accepted quotation (CRM
// quotation line "package" with packageRef = package code; source quotation
// keeps it idempotent). A package that cannot be booked is logged for the
// sales team (crm.quotation_accepted subscriber).
func (m *P3Module) OnQuotationAccepted(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		QuotationID        uuid.UUID  `json:"quotationId"`
		PropertyID         *uuid.UUID `json:"propertyId"`
		Line               string     `json:"line"`
		PackageRef         *string    `json:"packageRef"`
		CustomerID         *uuid.UUID `json:"customerId"`
		CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
		EventDate          *string    `json:"eventDate"`
		Pax                int        `json:"pax"`
		Number             string     `json:"number"`
		Title              string     `json:"title"`
		Total              string     `json:"total"`
		Lines              []struct {
			ItemType string  `json:"itemType"`
			ItemRef  *string `json:"itemRef"`
			Quantity string  `json:"quantity"`
		} `json:"lines"`
		PaymentTerms []struct {
			Label   string `json:"label"`
			Percent string `json:"percent"`
			Amount  string `json:"amount"`
			DueDate string `json:"dueDate"`
		} `json:"paymentTerms"`
	}
	if err := e.Decode(&p); err != nil {
		return nil //nolint:nilerr // another shape: not a package quotation
	}
	ref := p.PackageRef
	pax := p.Pax
	for _, l := range p.Lines {
		if l.ItemType == "package" && l.ItemRef != nil && *l.ItemRef != "" {
			if ref == nil || *ref == "" {
				ref = l.ItemRef
			}
			if q, err := decimal.NewFromString(l.Quantity); err == nil && pax <= 0 {
				pax = int(q.IntPart())
			}
		}
	}
	if p.Line != "package" || ref == nil || *ref == "" || p.EventDate == nil {
		return nil
	}
	property := p.PropertyID
	if property == nil {
		property = e.PropertyID
	}
	if property == nil {
		return nil
	}
	in := BookingInput{PackageCode: *ref, StartDate: *p.EventDate, Pax: pax, CustomerID: p.CustomerID, CorporateAccountID: p.CorporateAccountID,
		Channel: "quotation", SourceType: "quotation", SourceID: &p.QuotationID, Notes: strings.TrimSpace("Quotation " + p.Number + " · " + p.Title)}
	if t, err := decimal.NewFromString(p.Total); err == nil && t.IsPositive() {
		in.AgreedTotal = &t
		// the payment terms of the quotation (DP first, FR-QUO-06); the last
		// term takes the rounding
		acc := decimal.Zero
		for i, term := range p.PaymentTerms {
			amt, _ := decimal.NewFromString(term.Amount)
			if i == len(p.PaymentTerms)-1 {
				amt = t.Sub(acc)
			}
			if !amt.IsPositive() {
				continue
			}
			acc = acc.Add(amt)
			kind := "installment"
			if len(in.ScheduleLines) == 0 {
				kind = "down_payment"
			}
			if i == len(p.PaymentTerms)-1 {
				kind = "final"
			}
			in.ScheduleLines = append(in.ScheduleLines, billing.ScheduleLineInput{Label: term.Label, Kind: kind, Amount: amt.String(), DueDate: term.DueDate})
		}
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), *property)
	_, err = m.BookPackage(ctx, sp, *property, in, "")
	if err != nil {
		_ = sp.Rollback(ctx)
		if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
			slog.WarnContext(ctx, "quotation package not booked", "quotation", p.Number, "reason", de.Message)
			return nil
		}
		return err
	}
	return sp.Commit(ctx)
}
