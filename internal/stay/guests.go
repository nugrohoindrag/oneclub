package stay

// Guests (requirements §14): the CRM customer is the central record; the
// accommodation keeps the guest profile next to it (address, identity —
// masked, UU PDP — date of birth, nationality, preferences, notes, VIP) and
// derives the guest history from the stays: total stays and nights, last
// stay, spending, favourite room type, previous requests, cancellations and
// no-shows, with the booking, stay and payment history.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// GuestSummary is a guest of the accommodation with the stay totals.
type GuestSummary struct {
	CustomerID    uuid.UUID  `json:"customerId" db:"customer_id"`
	Name          string     `json:"name" db:"name"`
	Phone         *string    `json:"phone" db:"phone"`
	Email         *string    `json:"email" db:"email"`
	VIP           bool       `json:"vip" db:"vip"`
	Nationality   *string    `json:"nationality" db:"nationality"`
	TotalStays    int        `json:"totalStays" db:"total_stays"`
	TotalNights   int        `json:"totalNights" db:"total_nights"`
	LastStay      *time.Time `json:"lastStay" db:"last_stay"`
	NextStay      *time.Time `json:"nextStay" db:"next_stay"`
	TotalSpending string     `json:"totalSpending" db:"total_spending"`
	Cancellations int        `json:"cancellations" db:"cancellations"`
	NoShows       int        `json:"noShows" db:"no_shows"`
	InHouse       bool       `json:"inHouse" db:"in_house"`
}

const guestSummarySQL = `SELECT c.id AS customer_id, c.name, c.phone, c.email, coalesce(g.vip, false) AS vip, g.nationality,
	count(*) FILTER (WHERE s.status IN ('checked_in', 'checked_out'))::int AS total_stays,
	coalesce(sum(greatest(1, round(extract(epoch FROM coalesce(s.actual_end_at, s.end_at) - s.start_at) / 86400))) FILTER (WHERE s.status IN ('checked_in', 'checked_out')), 0)::int AS total_nights,
	max(s.start_at) FILTER (WHERE s.status IN ('checked_in', 'checked_out')) AS last_stay,
	min(s.start_at) FILTER (WHERE s.status IN ('reserved', 'requested') AND s.start_at > now()) AS next_stay,
	trim_scale(coalesce(sum(ef.charges) FILTER (WHERE s.status IN ('checked_in', 'checked_out')), 0))::text AS total_spending,
	count(*) FILTER (WHERE s.status = 'cancelled')::int AS cancellations, count(*) FILTER (WHERE s.status = 'no_show')::int AS no_shows,
	bool_or(s.status = 'checked_in') AS in_house
	FROM stay.stays s JOIN crm.customers c ON c.id = s.customer_id LEFT JOIN stay.guest_profiles g ON g.customer_id = c.id
	LEFT JOIN reporting.eng_folios ef ON ef.folio_id = s.folio_id
	WHERE s.property_id = $1 AND s.kind = 'bungalow'`

// Guests lists the accommodation guests (q: name, phone, e-mail).
func (m *Module) Guests(ctx context.Context, q dbtx.Querier, property uuid.UUID, search string, vip bool, limit int) ([]GuestSummary, error) {
	return handle.List[GuestSummary](q.Query(ctx, guestSummarySQL+` AND ($2 = '' OR c.name ILIKE '%' || $2 || '%' OR c.phone ILIKE '%' || $2 || '%'
		OR c.email ILIKE '%' || $2 || '%') AND (NOT $3 OR coalesce(g.vip, false))
		GROUP BY c.id, c.name, c.phone, c.email, g.vip, g.nationality ORDER BY max(s.start_at) DESC LIMIT $4`, property, search, vip, limit))
}

// GuestProfile is the accommodation profile of a guest.
type GuestProfile struct {
	CustomerID     uuid.UUID  `json:"customerId" db:"customer_id"`
	Address        *string    `json:"address" db:"address"`
	IDType         *string    `json:"idType" db:"id_type"`
	IDNumberMasked *string    `json:"idNumberMasked" db:"id_number_masked"`
	DateOfBirth    *time.Time `json:"dateOfBirth" db:"date_of_birth"`
	Nationality    *string    `json:"nationality" db:"nationality"`
	Preferences    *string    `json:"preferences" db:"preferences"`
	Notes          *string    `json:"notes" db:"notes"`
	VIP            bool       `json:"vip" db:"vip"`
}

// GuestDetail is the guest record with the history.
type GuestDetail struct {
	Customer         crm.Customer    `json:"customer"`
	Profile          GuestProfile    `json:"profile"`
	Summary          GuestSummary    `json:"summary"`
	FavoriteRoomType *string         `json:"favoriteRoomType"`
	Stays            []Stay          `json:"stays" doc:"Booking and stay history"`
	Requests         []GuestRequest  `json:"requests" doc:"Previous requests"`
	Payments         []GuestPayment  `json:"payments" doc:"Payment history of the stay folios"`
	Preferences      []CRMPreference `json:"crmPreferences"`
}

// GuestPayment is a payment on a stay folio.
type GuestPayment struct {
	Number     string     `json:"number" db:"number"`
	FolioID    uuid.UUID  `json:"folioId" db:"folio_id"`
	StayNo     string     `json:"stayNo" db:"stay_no"`
	MethodType string     `json:"methodType" db:"method_type"`
	Purpose    string     `json:"purpose" db:"purpose"`
	Amount     string     `json:"amount" db:"amount"`
	Refunded   string     `json:"refunded" db:"refunded"`
	Status     string     `json:"status" db:"status"`
	PaidAt     *time.Time `json:"paidAt" db:"paid_at"`
}

// CRMPreference is a preference kept in CRM.
type CRMPreference struct {
	Category string  `json:"category" db:"category"`
	Key      string  `json:"key" db:"pref_key"`
	Value    *string `json:"value" db:"pref_value"`
}

// Guest returns the guest record of a customer.
func (m *Module) Guest(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (GuestDetail, error) {
	var out GuestDetail
	var err error
	if out.Customer, err = crm.GetCustomer(ctx, q, customer); err != nil {
		return out, err
	}
	if out.Profile, err = guestProfile(ctx, q, property, customer); err != nil {
		return out, err
	}
	list, err := handle.List[GuestSummary](q.Query(ctx, guestSummarySQL+` AND c.id = $2 GROUP BY c.id, c.name, c.phone, c.email, g.vip, g.nationality`,
		property, customer))
	if err != nil {
		return out, err
	}
	if len(list) > 0 {
		out.Summary = list[0]
	} else {
		out.Summary = GuestSummary{CustomerID: customer, Name: out.Customer.Name, TotalSpending: "0"}
	}
	var fav string
	if err := q.QueryRow(ctx, `SELECT t.name FROM stay.stays s JOIN stay.bungalow_types t ON t.id = s.unit_type_id WHERE s.customer_id = $1
		AND s.status IN ('checked_in', 'checked_out') GROUP BY t.name ORDER BY count(*) DESC LIMIT 1`, customer).Scan(&fav); err == nil {
		out.FavoriteRoomType = &fav
	} else if !dbtx.IsNoRows(err) {
		return out, err
	}
	if out.Stays, err = handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.customer_id = $1 AND s.property_id = $2 ORDER BY s.start_at DESC LIMIT 100`,
		customer, property)); err != nil {
		return out, err
	}
	if out.Requests, err = handle.List[GuestRequest](q.Query(ctx, requestSelect+` WHERE s.customer_id = $1 ORDER BY g.created_at DESC LIMIT 50`, customer)); err != nil {
		return out, err
	}
	if out.Payments, err = handle.List[GuestPayment](q.Query(ctx, `SELECT p.number, p.folio_id, s.stay_no, p.method_type, p.purpose, trim_scale(p.amount)::text AS amount,
		trim_scale(p.refunded_amount)::text AS refunded, p.status, p.paid_at FROM reporting.eng_payments p JOIN stay.stays s ON s.folio_id = p.folio_id
		WHERE s.customer_id = $1 ORDER BY p.paid_at DESC NULLS LAST LIMIT 100`, customer)); err != nil {
		return out, err
	}
	out.Preferences, err = handle.List[CRMPreference](q.Query(ctx, `SELECT category, pref_key, pref_value FROM crm.customer_preferences
		WHERE customer_id = $1 AND status = 'active' ORDER BY category, pref_key`, customer))
	return out, err
}

func guestProfile(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (GuestProfile, error) {
	rows, err := q.Query(ctx, `SELECT customer_id, address, id_type, id_number_masked, date_of_birth, nationality, preferences, notes, vip
		FROM stay.guest_profiles WHERE property_id = $1 AND customer_id = $2`, property, customer)
	if err != nil {
		return GuestProfile{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[GuestProfile])
	if err != nil || len(list) == 0 {
		return GuestProfile{CustomerID: customer}, err
	}
	return list[0], nil
}

// GuestProfileInput updates the accommodation profile of a guest.
type GuestProfileInput struct {
	Address     *string `json:"address,omitempty"`
	IDType      *string `json:"idType,omitempty" enum:"ktp,passport,sim,kitas,other"`
	IDNumber    *string `json:"idNumber,omitempty" doc:"Stored masked (UU PDP)"`
	DateOfBirth *string `json:"dateOfBirth,omitempty" doc:"YYYY-MM-DD"`
	Nationality *string `json:"nationality,omitempty"`
	Preferences *string `json:"preferences,omitempty"`
	Notes       *string `json:"notes,omitempty"`
	VIP         *bool   `json:"vip,omitempty"`
}

// SaveGuestProfile upserts the profile.
func (m *Module) SaveGuestProfile(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, in GuestProfileInput) (GuestProfile, error) {
	if _, err := crm.GetCustomer(ctx, tx, customer); err != nil {
		return GuestProfile{}, err
	}
	before, err := guestProfile(ctx, tx, property, customer)
	if err != nil {
		return before, err
	}
	var masked *string
	if in.IDNumber != nil && *in.IDNumber != "" {
		v := mask.Phone(*in.IDNumber)
		masked = &v
	}
	if in.DateOfBirth != nil && *in.DateOfBirth != "" {
		if _, err := time.Parse(time.DateOnly, *in.DateOfBirth); err != nil {
			return before, handle.Invalid("dateOfBirth", "invalid_date", "dateOfBirth must be YYYY-MM-DD")
		}
	} else {
		in.DateOfBirth = nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stay.guest_profiles (id, property_id, customer_id, address, id_type, id_number_masked, date_of_birth, nationality,
		preferences, notes, vip, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7::date,$8,$9,$10,coalesce($11, false),$12)
		ON CONFLICT (property_id, customer_id) DO UPDATE SET address = coalesce(EXCLUDED.address, stay.guest_profiles.address),
		id_type = coalesce(EXCLUDED.id_type, stay.guest_profiles.id_type), id_number_masked = coalesce(EXCLUDED.id_number_masked, stay.guest_profiles.id_number_masked),
		date_of_birth = coalesce(EXCLUDED.date_of_birth, stay.guest_profiles.date_of_birth), nationality = coalesce(EXCLUDED.nationality, stay.guest_profiles.nationality),
		preferences = coalesce(EXCLUDED.preferences, stay.guest_profiles.preferences), notes = coalesce(EXCLUDED.notes, stay.guest_profiles.notes),
		vip = coalesce($11, stay.guest_profiles.vip), updated_by = EXCLUDED.created_by`, id.New(), property, customer, in.Address, in.IDType, masked,
		in.DateOfBirth, in.Nationality, in.Preferences, in.Notes, in.VIP, actor(ctx)); err != nil {
		return before, err
	}
	after, err := guestProfile(ctx, tx, property, customer)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionUpdate, EntityType: "stay.guest_profile", EntityID: customer.String(),
		PropertyID: &property, Before: before, After: after})
}
