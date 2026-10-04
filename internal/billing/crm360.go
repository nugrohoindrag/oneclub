package billing

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// PaymentActivity is the payment section of the Customer 360.
type PaymentActivity struct {
	MemberAccount *Account  `json:"memberAccount"`
	OpenFolios    int       `json:"openFolios"`
	Outstanding   string    `json:"outstanding"`
	Recent        []Payment `json:"recentPayments"`
}

// CustomerSection is the payment part of the Customer 360 (FR-CRM-01).
func (s *Service) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	a := PaymentActivity{Recent: []Payment{}}
	var err error
	if a.MemberAccount, err = AccountFor(ctx, q, property, customer, "member"); err != nil {
		return a, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*)::int, coalesce(sum(
		(SELECT coalesce(sum(l.total), 0) FROM billing.folio_lines l WHERE l.folio_id = f.id AND l.voided_at IS NULL) -
		(SELECT coalesce(sum(p.amount - p.refunded_amount), 0) FROM billing.payments p WHERE p.folio_id = f.id
		  AND p.status IN ('completed', 'refunded') AND p.purpose = 'settlement')), 0)::text
		FROM billing.folios f WHERE f.customer_id = $1 AND f.status = 'open'`, customer).Scan(&a.OpenFolios, &a.Outstanding); err != nil {
		return a, err
	}
	a.Outstanding = dec(a.Outstanding).String()
	rows, err := q.Query(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE p.folio_id IN (SELECT id FROM billing.folios WHERE customer_id = $1)
		ORDER BY p.created_at DESC LIMIT 10`, customer)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return a, err
		}
		a.Recent = append(a.Recent, p)
	}
	return a, rows.Err()
}

// Activity is the visit count and spend of a customer since a date.
type Activity struct {
	Visits int
	Spend  decimal.Decimal
}

// ActivitySince returns folio visits and revenue spend per customer
// (segmentation input, FR-CRM-03). Liabilities (caddy fee, tips) are not
// club revenue.
func ActivitySince(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]Activity, error) {
	rows, err := q.Query(ctx, `SELECT f.customer_id, count(DISTINCT f.id)::int, coalesce(sum(l.total) FILTER (WHERE NOT l.liability), 0)::text
		FROM billing.folios f LEFT JOIN billing.folio_lines l ON l.folio_id = f.id AND l.voided_at IS NULL
		WHERE f.property_id = $1 AND f.customer_id IS NOT NULL AND f.created_at >= $2 GROUP BY f.customer_id`, property, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]Activity{}
	for rows.Next() {
		var c uuid.UUID
		var n int
		var s string
		if err := rows.Scan(&c, &n, &s); err != nil {
			return nil, err
		}
		out[c] = Activity{Visits: n, Spend: dec(s)}
	}
	return out, rows.Err()
}
