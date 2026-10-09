package stay

// Accommodation parts of the stay lifecycle (requirements §16–§19, §29):
// corporate terms and booking limits, room type capacity, add-ons on the
// folio, early check-in, the tiered late check-out, the cancellation and
// no-show policy of the accommodation rate plan, and the guest identity
// kept on the guest profile.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
)

// corporateTerms are the accommodation terms of a corporate account (§29).
type corporateTerms struct {
	AccountID          uuid.UUID
	Name               string
	RatePlanCode       string
	BillingArrangement string
	MaxRoomsPerNight   *int
	MonthlyRoomNights  *int
	PaymentTermsDays   int
}

func (m *Module) corporateTerms(ctx context.Context, q dbtx.Querier, property, aid uuid.UUID) (*corporateTerms, error) {
	name, err := crm.CorporateName(ctx, q, aid)
	if err != nil {
		return nil, err
	}
	t := &corporateTerms{AccountID: aid, Name: name, BillingArrangement: "guest_pays"}
	err = q.QueryRow(ctx, `SELECT coalesce(rate_plan_code, ''), billing_arrangement, max_rooms_per_night, monthly_room_nights, payment_terms_days
		FROM stay.corporate_terms WHERE property_id = $1 AND corporate_account_id = $2 AND status = 'active'`, property, aid).
		Scan(&t.RatePlanCode, &t.BillingArrangement, &t.MaxRoomsPerNight, &t.MonthlyRoomNights, &t.PaymentTermsDays)
	if err != nil && !dbtx.IsNoRows(err) {
		return nil, err
	}
	return t, nil
}

// checkCorporateLimits enforces the booking limits of the corporate terms:
// rooms per night and room nights per month of the arrival.
func (m *Module) checkCorporateLimits(ctx context.Context, q dbtx.Querier, property uuid.UUID, c *corporateTerms, start, end time.Time) error {
	if c.MaxRoomsPerNight != nil {
		var most int
		if err := q.QueryRow(ctx, `SELECT coalesce(max((SELECT count(*) FROM stay.stays s WHERE s.property_id = $1 AND s.corporate_account_id = $2
			AND s.kind = 'bungalow' AND s.status IN ('requested', 'reserved', 'checked_in') AND s.start_at <= d AND s.end_at > d)), 0)
			FROM generate_series($3::timestamptz, $4::timestamptz - interval '1 hour', interval '1 day') d`, property, c.AccountID, start, end).Scan(&most); err != nil {
			return err
		}
		if most+1 > *c.MaxRoomsPerNight {
			return errs.Conflict("corporate_limit", fmt.Sprintf("%s may book at most %d bungalows per night", c.Name, *c.MaxRoomsPerNight))
		}
	}
	if c.MonthlyRoomNights != nil {
		loc := calendar.Location(ctx, q)
		a := start.In(loc)
		month := time.Date(a.Year(), a.Month(), 1, 0, 0, 0, 0, loc)
		var used int
		if err := q.QueryRow(ctx, `SELECT coalesce(sum(greatest(1, round(extract(epoch FROM end_at - start_at) / 86400))), 0)::int FROM stay.stays
			WHERE property_id = $1 AND corporate_account_id = $2 AND kind = 'bungalow' AND status IN ('requested', 'reserved', 'checked_in', 'checked_out')
			AND start_at >= $3 AND start_at < $4`, property, c.AccountID, month, month.AddDate(0, 1, 0)).Scan(&used); err != nil {
			return err
		}
		if used+nightsBetween(start, end, loc) > *c.MonthlyRoomNights {
			return errs.Conflict("corporate_limit", fmt.Sprintf("%s has %d of %d room nights left this month", c.Name, max(*c.MonthlyRoomNights-used, 0),
				*c.MonthlyRoomNights))
		}
	}
	return nil
}

// checkCapacity validates the guests against the room type (§16).
func checkCapacity(ctx context.Context, q dbtx.Querier, typeID uuid.UUID, adults, children int) error {
	var name string
	var maxA, maxC int
	if err := q.QueryRow(ctx, `SELECT name, max_adults, max_children FROM stay.bungalow_types WHERE id = $1`, typeID).Scan(&name, &maxA, &maxC); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if adults > maxA || children > maxC {
		return errs.Validation("over_capacity", fmt.Sprintf("%s takes at most %d adults and %d children", name, maxA, maxC),
			errs.Field("adults", "over_capacity", fmt.Sprintf("maximum %d adults", maxA)))
	}
	return nil
}

// ── add-ons on the folio (§13) ────────────────────────────────────────────

type pendingAddon struct {
	AddonLine
	Source      string
	FolioLineID *uuid.UUID
}

// addonComponent maps an add-on category to its revenue component.
func addonComponent(category string) string {
	switch category {
	case "breakfast", "lunch", "dinner", "bbq":
		return "fnb"
	case "extra_bed", "extra_pillow", "extra_towel":
		return "bungalow"
	}
	return "other"
}

func (m *Module) chargeAddon(ctx context.Context, tx pgx.Tx, folioID, _ uuid.UUID, l AddonLine) (uuid.UUID, error) {
	desc := l.Name
	if n := l.Quantity * l.Units; n > 1 {
		desc = fmt.Sprintf("%s × %d", l.Name, n)
	}
	return m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folioID, ReferenceType: "stay.addon", ReferenceID: &l.AddonID,
		Description: desc, Quantity: decimal.NewFromInt(int64(max(l.Quantity*l.Units, 1))), Net: decOf(l.Net), Service: decOf(l.Service), Tax: decOf(l.Tax)},
		BusinessLine: billing.LineStay, RevenueComponent: addonComponent(l.Category)})
}

func (m *Module) insertAddon(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, a pendingAddon) error {
	_, err := tx.Exec(ctx, `INSERT INTO stay.stay_addons (id, property_id, stay_id, addon_id, name, quantity, units, unit_price, total, included, folio_line_id,
		source, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10,$11,$12,$13)`, id.New(), property, sid, a.AddonID, a.Name, a.Quantity,
		a.Units, a.UnitPrice, a.Total, a.Included, a.FolioLineID, a.Source, actor(ctx))
	return err
}

// recordAccommodation stores the accommodation details of a new stay:
// source, corporate, package, promotion, add-ons and the waitlist entry it
// converts.
func (m *Module) recordAccommodation(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in StayInput, sq *RoomQuote, corp *corporateTerms,
	addons []pendingAddon) error {
	var arrangement *string
	if corp != nil {
		arrangement = &corp.BillingArrangement
	}
	var pkg, promo *uuid.UUID
	var promoCode *string
	discount := "0"
	if sq != nil {
		pkg, promo, promoCode, discount = sq.PackageID, sq.PromotionID, sq.PromotionCode, sq.Discount
	}
	vip := in.VIP
	if !vip && in.CustomerID != nil {
		_ = tx.QueryRow(ctx, `SELECT vip FROM stay.guest_profiles WHERE customer_id = $1`, *in.CustomerID).Scan(&vip)
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET booking_source = $2, corporate_account_id = $3, billing_arrangement = $4, stay_package_id = $5,
		promotion_id = $6, promotion_code = $7, discount = $8::numeric, notes = $9, vip = $10, expected_arrival = $11, waitlist_id = $12 WHERE id = $1`,
		sid, nzs(in.BookingSource), in.CorporateAccountID, arrangement, pkg, promo, promoCode, discount, nzs(in.Notes), vip, nzs(in.ExpectedArrival),
		in.WaitlistID); err != nil {
		return err
	}
	for _, a := range addons {
		if err := m.insertAddon(ctx, tx, property, sid, a); err != nil {
			return err
		}
	}
	if promo != nil && decOf(discount).IsPositive() {
		if _, err := tx.Exec(ctx, `INSERT INTO stay.promotion_redemptions (id, property_id, promotion_id, stay_id, discount) VALUES ($1,$2,$3,$4,$5::numeric)`,
			id.New(), property, *promo, sid, discount); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.promotions SET used_count = used_count + 1 WHERE id = $1`, *promo); err != nil {
			return err
		}
	}
	if in.WaitlistID != nil {
		if _, err := tx.Exec(ctx, `UPDATE stay.waitlist SET status = 'converted', stay_id = $2, updated_by = $3 WHERE id = $1 AND status IN ('waiting', 'offered')`,
			*in.WaitlistID, sid, actor(ctx)); err != nil {
			return err
		}
	}
	return nil
}

// reverseAccommodation undoes the promotion use and voids the add-ons of a
// cancelled or no-show stay (their folio lines are voided by the folio).
func (m *Module) reverseAccommodation(ctx context.Context, tx pgx.Tx, s Stay) error {
	rows, err := tx.Query(ctx, `UPDATE stay.promotion_redemptions SET status = 'reversed' WHERE stay_id = $1 AND status = 'applied' RETURNING promotion_id`, s.ID)
	if err != nil {
		return err
	}
	promos, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, p := range promos {
		if _, err := tx.Exec(ctx, `UPDATE stay.promotions SET used_count = greatest(used_count - 1, 0) WHERE id = $1`, p); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE stay.stay_addons SET voided_at = now() WHERE stay_id = $1 AND voided_at IS NULL`, s.ID)
	return err
}

// StayAddon is an add-on of a stay.
type StayAddon struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	AddonID   uuid.UUID  `json:"addonId" db:"addon_id"`
	Name      string     `json:"name" db:"name"`
	Category  string     `json:"category" db:"category"`
	Quantity  int        `json:"quantity" db:"quantity"`
	Units     int        `json:"units" db:"units"`
	UnitPrice string     `json:"unitPrice" db:"unit_price"`
	Total     string     `json:"total" db:"total"`
	Included  bool       `json:"included" db:"included"`
	Source    string     `json:"source" db:"source"`
	VoidedAt  *time.Time `json:"voidedAt" db:"voided_at"`
	CreatedAt time.Time  `json:"createdAt" db:"created_at"`
}

func stayAddons(ctx context.Context, q dbtx.Querier, sid uuid.UUID) ([]StayAddon, error) {
	rows, err := q.Query(ctx, `SELECT sa.id, sa.addon_id, sa.name, a.category, sa.quantity, sa.units, trim_scale(sa.unit_price)::text AS unit_price,
		trim_scale(sa.total)::text AS total, sa.included, sa.source, sa.voided_at, sa.created_at FROM stay.stay_addons sa JOIN stay.addons a ON a.id = sa.addon_id
		WHERE sa.stay_id = $1 ORDER BY sa.created_at`, sid)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[StayAddon])
	if out == nil {
		out = []StayAddon{}
	}
	return out, err
}

// ── check-in / check-out (§17, §18) ──────────────────────────────────────

// activeBlock returns the kind of the room block covering at ("" = none).
func (m *Module) activeBlock(ctx context.Context, q dbtx.Querier, unitID uuid.UUID, at time.Time) (string, error) {
	var kind string
	err := q.QueryRow(ctx, `SELECT kind FROM stay.room_blocks WHERE bungalow_id = $1 AND status = 'active' AND start_at <= $2 AND end_at > $2
		ORDER BY CASE kind WHEN 'out_of_order' THEN 0 WHEN 'maintenance' THEN 1 ELSE 2 END LIMIT 1`, unitID, at).Scan(&kind)
	if dbtx.IsNoRows(err) {
		return "", nil
	}
	return kind, err
}

// earlyCheckIn moves the stay start to now on the arrival day (from the
// earliest early check-in time of Stay Policies) when the bungalow is free
// before the check-in time, and posts the early check-in fee.
func (m *Module) earlyCheckIn(ctx context.Context, tx pgx.Tx, s Stay, pol Policy, waive bool) error {
	now := clock.Now()
	if !now.Before(s.Start) || s.PackageBooking != nil {
		return nil // a package stay follows its package (consumed at check-in, PRD P3)
	}
	loc := calendar.Location(ctx, tx)
	a := s.Start.In(loc)
	arrival := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc)
	if now.In(loc).Format(time.DateOnly) != arrival.Format(time.DateOnly) {
		return errs.Conflict("too_early", "the arrival is on "+arrival.Format("02 Jan 2006")+"; reschedule the stay to check in today")
	}
	if from := clockOn(arrival, pol.EarlyCheckInFrom, loc); now.Before(from) {
		return errs.Conflict("too_early", "early check-in is possible from "+pol.EarlyCheckInFrom)
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return err
	}
	if len(r.Lines) == 0 {
		return nil
	}
	if err := m.Res.AdvanceLineStart(ctx, tx, r.Lines[0].ID, now); err != nil {
		return err
	}
	fee := decOf(pol.EarlyCheckInFee)
	if !waive && fee.IsPositive() && s.FolioID != nil {
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "stay.stay", ReferenceID: &s.ID,
			Description: "Early check-in " + now.In(loc).Format("15:04"), Quantity: decimal.NewFromInt(1), Net: fee}, BusinessLine: billing.LineStay,
			RevenueComponent: "bungalow"}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET start_at = $2, early_check_in = true WHERE id = $1`, s.ID, now); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "early_check_in", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Before: map[string]any{"start": s.Start}, After: map[string]any{"start": now, "feeWaived": waive}})
}

// nightPrice is the price of one night of the stay (the additional night
// of a late check-out after the cut-off).
func (m *Module) nightPrice(ctx context.Context, q dbtx.Querier, s Stay, fallback decimal.Decimal) decimal.Decimal {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT room_quote FROM stay.stays WHERE id = $1`, s.ID).Scan(&raw); err == nil && raw != nil {
		var rq roomQuote
		if json.Unmarshal(raw, &rq) == nil && rq.Nights > 0 {
			if len(rq.Shares) > 0 {
				l := rq.Shares[len(rq.Shares)-1]
				return decOf(l.Net)
			}
			return decOf(rq.Net).Div(decimal.NewFromInt(int64(rq.Nights))).Round(0)
		}
	}
	if s.Nights > 0 {
		if due := decOf(s.TotalDue); due.IsPositive() {
			return due.Div(decimal.NewFromInt(int64(s.Nights))).Round(0)
		}
	}
	return fallback
}

// chargeLateCheckout posts the late check-out charge of Stay Policies:
// per started hour (hourly) or one fee until the cut-off and an additional
// night (or the configured charge) after it (tiered).
func (m *Module) chargeLateCheckout(ctx context.Context, tx pgx.Tx, s Stay, pol Policy, at time.Time, late time.Duration) error {
	add := func(desc string, qty int64, net decimal.Decimal, component string) error {
		if !net.IsPositive() {
			return nil
		}
		_, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "stay.stay", ReferenceID: &s.ID,
			Description: desc, Quantity: decimal.NewFromInt(qty), Net: net}, BusinessLine: billing.LineStay, RevenueComponent: component})
		return err
	}
	if pol.LateCheckoutMode == "hourly" {
		hours := int(late.Hours()) + 1
		fee := decOf(pol.LateCheckoutFeePerHour)
		return add(fmt.Sprintf("Late check-out %d h", hours), int64(hours), fee.Mul(decimal.NewFromInt(int64(hours))), "late_checkout_fee")
	}
	loc := calendar.Location(ctx, tx)
	d := s.End.In(loc)
	cutoff := clockOn(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), pol.LateCheckoutUntil, loc)
	if !at.After(cutoff) {
		return add("Late check-out until "+pol.LateCheckoutUntil, 1, decOf(pol.LateCheckoutFee), "late_checkout_fee")
	}
	if strings.EqualFold(strings.TrimSpace(pol.AfterCutoffCharge), "night") || pol.AfterCutoffCharge == "" {
		return add("Late check-out after "+pol.LateCheckoutUntil+" (additional night)", 1, m.nightPrice(ctx, tx, s, decOf(pol.LateCheckoutFee)), "bungalow")
	}
	return add("Late check-out after "+pol.LateCheckoutUntil, 1, decOf(pol.AfterCutoffCharge), "late_checkout_fee")
}

// saveGuestIdentity keeps the verified identity (masked, UU PDP) on the
// guest profile.
func (m *Module) saveGuestIdentity(ctx context.Context, tx pgx.Tx, s Stay, idType, idNumber string) error {
	if s.CustomerID == nil || idNumber == "" {
		return nil
	}
	switch idType {
	case "ktp", "passport", "sim", "kitas", "other":
	default:
		idType = "other"
	}
	_, err := tx.Exec(ctx, `INSERT INTO stay.guest_profiles (id, property_id, customer_id, id_type, id_number_masked, created_by) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (property_id, customer_id) DO UPDATE SET id_type = EXCLUDED.id_type, id_number_masked = EXCLUDED.id_number_masked, updated_by = EXCLUDED.created_by`,
		id.New(), s.PropertyID, *s.CustomerID, idType, mask.Phone(idNumber), actor(ctx))
	return err
}

// ── cancellation and no-show (§19) ────────────────────────────────────────

// stayPlan returns the accommodation rate plan whose policies govern the
// stay (nil: the Reservation Engine policies of P2 apply).
func (m *Module) stayPlan(ctx context.Context, q dbtx.Querier, s Stay) (*ratePlanRow, error) {
	if s.Kind != "bungalow" || s.PackageBooking != nil {
		return nil, nil
	}
	if s.StayPackageID != nil {
		return ratePlan(ctx, q, s.PropertyID, "")
	}
	if s.RatePlan == nil || *s.RatePlan == "" {
		return nil, nil
	}
	return ratePlan(ctx, q, s.PropertyID, *s.RatePlan)
}

// cancellationFee applies the rate plan: free until its deadline, the
// cancellation fee after it, everything for a non-refundable rate.
func (m *Module) cancellationFee(ctx context.Context, q dbtx.Querier, s Stay, waive bool) (decimal.Decimal, bool, error) {
	plan, err := m.stayPlan(ctx, q, s)
	if err != nil || plan == nil {
		return decimal.Zero, false, err
	}
	if waive || s.Status == "requested" {
		return decimal.Zero, true, nil
	}
	total := decOf(s.TotalDue)
	if plan.NonRefundable {
		return total, true, nil
	}
	if s.Start.Sub(clock.Now()) >= time.Duration(plan.FreeCancelHours)*time.Hour {
		return decimal.Zero, true, nil
	}
	return total.Mul(decOf(plan.CancelFeePercent)).Div(decimal.NewFromInt(100)).Round(0), true, nil
}

// chargeNoShow applies the no-show charge of the rate plan: the folio
// charges are voided, the charge (a share of the first night, everything
// for a non-refundable rate) is posted and the rest refunded. Stays without
// an accommodation rate plan keep their folio as in P2.
func (m *Module) chargeNoShow(ctx context.Context, tx pgx.Tx, s Stay, reason string) (decimal.Decimal, error) {
	plan, err := m.stayPlan(ctx, tx, s)
	if err != nil || plan == nil || s.FolioID == nil {
		return decimal.Zero, err
	}
	first := m.nightPrice(ctx, tx, s, decimal.Zero)
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT room_quote FROM stay.stays WHERE id = $1`, s.ID).Scan(&raw); err == nil && raw != nil {
		var rq roomQuote
		if json.Unmarshal(raw, &rq) == nil && len(rq.Shares) > 0 {
			sh := rq.Shares[0]
			first = decOf(sh.Net).Add(decOf(sh.Service)).Add(decOf(sh.Tax))
		}
	}
	fee := first.Mul(decOf(plan.NoShowFeePercent)).Div(decimal.NewFromInt(100)).Round(0)
	if plan.NonRefundable {
		fee = decOf(s.TotalDue)
	}
	if status, err := billing.FolioStatus(ctx, tx, *s.FolioID); err != nil || status != "open" {
		return fee, err
	}
	lines, err := billing.LinesOf(ctx, tx, *s.FolioID)
	if err != nil {
		return fee, err
	}
	for _, l := range lines {
		if err := m.Billing.VoidCharge(ctx, tx, l.ID, "no-show: "+reason); err != nil {
			return fee, err
		}
	}
	if fee.IsPositive() {
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ChargeType: "no_show_fee",
			ReferenceType: "stay.stay", ReferenceID: &s.ID, Description: "No-show charge · " + plan.Name, Quantity: decimal.NewFromInt(1), Net: fee},
			BusinessLine: billing.LineStay, RevenueComponent: "no_show_fee"}); err != nil {
			return fee, err
		}
	}
	if _, err := m.Billing.RefundFolio(ctx, tx, *s.FolioID, "no-show "+s.StayNo, nil); err != nil {
		return fee, err
	}
	return fee, m.reverseAccommodation(ctx, tx, s)
}
