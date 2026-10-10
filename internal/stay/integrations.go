package stay

// The bungalows in the other modules (docs/requirement-booking-hotel-mgcc.md
// Bagian D): Customer 360 › Stay (FR-H77), the stay facts of the CRM
// segments (FR-H78), the promotion codes tracked to their campaign and
// reservations (FR-H78) and the Accommodation profit center by room type
// with its KPI (FR-H85).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// StayActivity is the Stay section of Customer 360.
type StayActivity struct {
	Stays         int        `json:"stays" doc:"Stays checked in or out"`
	Nights        int        `json:"nights"`
	Upcoming      int        `json:"upcoming"`
	NoShows       int        `json:"noShows"`
	Cancellations int        `json:"cancellations"`
	LastStay      *time.Time `json:"lastStay"`
	FavoriteType  *string    `json:"favoriteType"`
	Spend         string     `json:"spend" doc:"Folio charges of the stays"`
	VIP           bool       `json:"vip"`
	Preferences   *string    `json:"preferences" doc:"View, bed, allergies … (guest profile)"`
	Nationality   *string    `json:"nationality"`
	Recent        []Stay     `json:"recent"`
}

// CustomerSection is Customer 360 › Stay.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	a := StayActivity{Recent: []Stay{}}
	var spend string
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE s.status IN ('checked_in', 'checked_out'))::int,
		coalesce(sum(greatest(1, round(extract(epoch FROM coalesce(s.actual_end_at, s.end_at) - s.start_at) / 86400))) FILTER (WHERE s.status IN ('checked_in', 'checked_out')), 0)::int,
		count(*) FILTER (WHERE s.status = 'reserved' AND s.start_at > now())::int, count(*) FILTER (WHERE s.status = 'no_show')::int,
		count(*) FILTER (WHERE s.status = 'cancelled')::int, max(s.start_at) FILTER (WHERE s.status IN ('checked_in', 'checked_out')),
		(SELECT t.name FROM stay.stays x JOIN stay.bungalow_types t ON t.id = x.unit_type_id WHERE x.customer_id = $1 AND x.status IN ('checked_in', 'checked_out')
		  GROUP BY t.name ORDER BY count(*) DESC LIMIT 1),
		trim_scale(coalesce(sum(ef.charges) FILTER (WHERE s.status IN ('checked_in', 'checked_out')), 0))::text,
		bool_or(s.vip)
		FROM stay.stays s LEFT JOIN reporting.eng_folios ef ON ef.folio_id = s.folio_id WHERE s.customer_id = $1 AND s.kind = 'bungalow'`, customer).
		Scan(&a.Stays, &a.Nights, &a.Upcoming, &a.NoShows, &a.Cancellations, &a.LastStay, &a.FavoriteType, &spend, &a.VIP); err != nil {
		return a, err
	}
	a.Spend = nonEmpty(spend, "0")
	var vip bool
	if err := q.QueryRow(ctx, `SELECT preferences, nationality, vip FROM stay.guest_profiles WHERE customer_id = $1 AND property_id = $2`, customer, property).
		Scan(&a.Preferences, &a.Nationality, &vip); err != nil && !dbtx.IsNoRows(err) {
		return a, err
	}
	a.VIP = a.VIP || vip
	var err error
	a.Recent, err = handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.customer_id = $1 AND s.kind = 'bungalow' AND s.status NOT IN ('expired', 'void')
		ORDER BY s.start_at DESC LIMIT 5`, customer))
	return a, err
}

// SegmentFact is what the stays tell about a customer for a CRM segment.
type SegmentFact struct {
	Stays    int
	LastStay *time.Time
}

// SegmentFacts returns the stays in the window and the last stay of every
// customer of a property (e.g. stayed twice this year, not for 6 months).
func SegmentFacts(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]SegmentFact, error) {
	rows, err := q.Query(ctx, `SELECT customer_id, count(*) FILTER (WHERE start_at >= $2)::int, max(start_at) FROM stay.stays
		WHERE property_id = $1 AND kind = 'bungalow' AND customer_id IS NOT NULL AND status IN ('checked_in', 'checked_out') GROUP BY customer_id`, property, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]SegmentFact{}
	for rows.Next() {
		var c uuid.UUID
		var f SegmentFact
		if err := rows.Scan(&c, &f.Stays, &f.LastStay); err != nil {
			return nil, err
		}
		out[c] = f
	}
	return out, rows.Err()
}

// PromotionUse is a Stay promotion with its campaign and the reservations
// made with it (FR-H78).
type PromotionUse struct {
	Code         string  `json:"code" db:"code"`
	Name         string  `json:"name" db:"name"`
	PromoCode    *string `json:"promoCode" db:"promo_code"`
	Campaign     *string `json:"campaign" db:"campaign"`
	Reservations int     `json:"reservations" db:"reservations"`
	Nights       int     `json:"nights" db:"nights"`
	Discount     string  `json:"discount" db:"discount"`
	Revenue      string  `json:"revenue" db:"revenue"`
}

// TypeCenter is a room type in the Accommodation profit center.
type TypeCenter struct {
	TypeID          uuid.UUID `json:"typeId"`
	TypeName        string    `json:"typeName"`
	Units           int       `json:"units"`
	Available       int       `json:"available" doc:"Room nights available"`
	Sold            int       `json:"sold"`
	Occupancy       string    `json:"occupancy"`
	ADR             string    `json:"adr"`
	RevPAR          string    `json:"revpar"`
	ALOS            string    `json:"alos"`
	RoomRevenue     string    `json:"roomRevenue"`
	AddonRevenue    string    `json:"addonRevenue"`
	FnbChargeToRoom string    `json:"fnbChargeToRoom"`
	Maintenance     string    `json:"maintenance" doc:"Work order costs of the bungalows of the type"`
	SharedCost      string    `json:"sharedCost" doc:"Bungalow costs of the ledger (housekeeping staff, amenities …) shared by room nights sold"`
	Margin          string    `json:"margin"`
}

// ProfitCenter is the Accommodation profit center by room type.
type ProfitCenter struct {
	From   string       `json:"from"`
	To     string       `json:"to"`
	Types  []TypeCenter `json:"types"`
	Total  TypeCenter   `json:"total"`
	Ledger string       `json:"ledgerCost" doc:"Costs of the Bungalow profit center in the general ledger"`
}

// ProfitCenterOf builds the profit center of the local dates [from, to].
func (m *Module) ProfitCenterOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) (ProfitCenter, error) {
	loc := calendar.Location(ctx, q)
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	days := max(int(end.Sub(start).Hours()/24+0.5), 1)
	out := ProfitCenter{From: start.Format(time.DateOnly), To: end.AddDate(0, 0, -1).Format(time.DateOnly), Types: []TypeCenter{}}
	rows, err := q.Query(ctx, `SELECT t.id, t.name, (SELECT count(*) FROM stay.bungalows b WHERE b.type_id = t.id AND b.status = 'active' AND b.archived_at IS NULL)::int
		FROM stay.bungalow_types t WHERE t.property_id = $1 AND t.archived_at IS NULL ORDER BY t.sort_order, t.name`, property)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var t TypeCenter
		if err := rows.Scan(&t.TypeID, &t.TypeName, &t.Units); err != nil {
			rows.Close()
			return out, err
		}
		out.Types = append(out.Types, t)
	}
	rows.Close()
	var ledger string
	if err := q.QueryRow(ctx, `SELECT coalesce(-sum(profit), 0)::text FROM reporting.acc_profit_center_lines WHERE property_id = $1 AND profit_center = 'bungalow'
		AND section IN ('cogs', 'expense') AND journal_date >= $2::date AND journal_date < $3::date`, property, start.Format(time.DateOnly),
		end.Format(time.DateOnly)).Scan(&ledger); err != nil {
		return out, err
	}
	out.Ledger = decOf(ledger).Round(0).String()
	totalSold := 0
	for i := range out.Types {
		t := &out.Types[i]
		var room, addon, fnb, maint string
		var alos *string
		if err := q.QueryRow(ctx, `SELECT
			coalesce((SELECT sum(l.net_amount) FROM reporting.eng_folio_lines l JOIN stay.stays x ON x.folio_id = l.folio_id WHERE x.unit_type_id = $2
			  AND l.reference_type = 'reservation.line' AND l.posted_at >= $3 AND l.posted_at < $4), 0)::text,
			coalesce((SELECT sum(l.net_amount) FROM reporting.eng_folio_lines l JOIN stay.stays x ON x.folio_id = l.folio_id WHERE x.unit_type_id = $2
			  AND l.reference_type = 'stay.addon' AND l.posted_at >= $3 AND l.posted_at < $4), 0)::text,
			coalesce((SELECT sum(l.net_amount) FROM reporting.eng_folio_lines l JOIN stay.stays x ON x.folio_id = l.folio_id WHERE x.unit_type_id = $2
			  AND l.business_line = 'pos' AND l.posted_at >= $3 AND l.posted_at < $4), 0)::text,
			coalesce((SELECT sum(w.cost) FROM stay.work_orders w JOIN stay.bungalows b ON b.id = w.bungalow_id WHERE b.type_id = $2
			  AND w.created_at >= $3 AND w.created_at < $4), 0)::text,
			(SELECT trim_scale(round(avg(greatest(1, round(extract(epoch FROM coalesce(x.actual_end_at, x.end_at) - x.start_at) / 86400))), 1))::text
			  FROM stay.stays x WHERE x.unit_type_id = $2 AND x.status = 'checked_out' AND x.checked_out_at >= $3 AND x.checked_out_at < $4)
			WHERE $1::uuid IS NOT NULL`, property, t.TypeID, start, end).Scan(&room, &addon, &fnb, &maint, &alos); err != nil {
			return out, err
		}
		// room nights sold: the nights (18:00) of the period covered by a stay of the type
		if err := q.QueryRow(ctx, `SELECT count(*)::int FROM stay.stays s JOIN reporting.reservations r ON r.reservation_id = s.reservation_id
			CROSS JOIN generate_series(0, $5 - 1) g WHERE s.property_id = $1 AND s.unit_type_id = $2 AND s.kind = 'bungalow'
			AND s.status IN ('reserved', 'checked_in', 'checked_out') AND r.status <> 'expired'
			AND s.start_at <= $3::timestamptz + g * interval '1 day' + interval '18 hours' AND coalesce(s.actual_end_at, s.end_at) > $3::timestamptz + g * interval '1 day' + interval '18 hours'`,
			property, t.TypeID, start, end, days).Scan(&t.Sold); err != nil {
			return out, err
		}
		t.Available = t.Units * days
		t.Sold = min(t.Sold, t.Available)
		totalSold += t.Sold
		t.RoomRevenue, t.AddonRevenue, t.FnbChargeToRoom = decOf(room).Round(0).String(), decOf(addon).Round(0).String(), decOf(fnb).Round(0).String()
		t.Maintenance, t.ALOS = decOf(maint).Round(0).String(), nonEmpty(deref(alos), "0")
		t.Occupancy, t.ADR, t.RevPAR = kpis(decOf(t.RoomRevenue), t.Sold, t.Available)
	}
	tot := TypeCenter{TypeName: "Total"}
	sum := func(a, b string) string { return decOf(a).Add(decOf(b)).String() }
	for i := range out.Types {
		t := &out.Types[i]
		shared := decimal.Zero
		if totalSold > 0 {
			shared = decOf(out.Ledger).Mul(decimal.NewFromInt(int64(t.Sold))).Div(decimal.NewFromInt(int64(totalSold))).Round(0)
		}
		t.SharedCost = shared.String()
		t.Margin = decOf(t.RoomRevenue).Add(decOf(t.AddonRevenue)).Add(decOf(t.FnbChargeToRoom)).Sub(decOf(t.Maintenance)).Sub(shared).String()
		tot.Units, tot.Available, tot.Sold = tot.Units+t.Units, tot.Available+t.Available, tot.Sold+t.Sold
		tot.RoomRevenue, tot.AddonRevenue, tot.FnbChargeToRoom = sum(tot.RoomRevenue, t.RoomRevenue), sum(tot.AddonRevenue, t.AddonRevenue),
			sum(tot.FnbChargeToRoom, t.FnbChargeToRoom)
		tot.Maintenance, tot.SharedCost, tot.Margin = sum(tot.Maintenance, t.Maintenance), sum(tot.SharedCost, t.SharedCost), sum(tot.Margin, t.Margin)
	}
	tot.Occupancy, tot.ADR, tot.RevPAR = kpis(decOf(tot.RoomRevenue), tot.Sold, tot.Available)
	out.Total = tot
	return out, nil
}

func (m *Module) registerIntegrations(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "stay", tagAccommodation, route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/profit-center", Summary: "Accommodation profit center by room type: revenue, costs, margin and KPI",
		Permission: "stay.dashboard.view", Response: ProfitCenter{}, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ProfitCenter, error) {
			t := localToday(ctx, tx)
			from, err := handle.QueryDate(r, "from", time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()))
			if err != nil {
				return ProfitCenter{}, err
			}
			to, err := handle.QueryDate(r, "to", t)
			if err != nil {
				return ProfitCenter{}, err
			}
			return m.ProfitCenterOf(ctx, tx, handle.Property(ctx), from, to)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/reports/promotions", Summary: "Stay promotions with their CRM campaign and the reservations made with them",
		Permission: "stay.dashboard.view", Response: PromotionUse{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PromotionUse], error) {
			return handle.Page(handle.List[PromotionUse](tx.Query(ctx, `SELECT p.code, p.name, p.promo_code, c.name AS campaign,
				count(DISTINCT s.id) FILTER (WHERE s.status NOT IN ('expired', 'void'))::int AS reservations,
				coalesce(sum(greatest(1, round(extract(epoch FROM s.end_at - s.start_at) / 86400))) FILTER (WHERE s.status NOT IN ('expired', 'void', 'cancelled')), 0)::int AS nights,
				trim_scale(coalesce(sum(pr.discount) FILTER (WHERE pr.status = 'applied'), 0))::text AS discount,
				trim_scale(coalesce(sum(ef.charges) FILTER (WHERE s.status IN ('checked_in', 'checked_out')), 0))::text AS revenue
				FROM stay.promotions p LEFT JOIN crm.campaigns c ON c.id = p.campaign_id
				LEFT JOIN stay.promotion_redemptions pr ON pr.promotion_id = p.id LEFT JOIN stay.stays s ON s.id = pr.stay_id
				LEFT JOIN reporting.eng_folios ef ON ef.folio_id = s.folio_id
				WHERE p.property_id = $1 AND p.archived_at IS NULL GROUP BY p.id, c.name ORDER BY count(DISTINCT s.id) DESC, p.code`, handle.Property(ctx))))
		})})
}
