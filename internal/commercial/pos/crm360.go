package pos

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/commercial/voucher"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// POSActivity is the POS & voucher section of the Customer 360.
type POSActivity struct {
	Orders      int               `json:"orders"`
	Spend       string            `json:"spend"`
	LastOrder   *time.Time        `json:"lastOrder"`
	TopProducts []TopProduct      `json:"topProducts"`
	Vouchers    []voucher.Voucher `json:"vouchers" doc:"Active vouchers and prepaid balances"`
}

type TopProduct struct {
	ProductID uuid.UUID `json:"productId" db:"product_id"`
	Name      string    `json:"name" db:"name"`
	Quantity  string    `json:"quantity" db:"quantity"`
}

func topProducts(ctx context.Context, q dbtx.Querier, customer uuid.UUID, n int) ([]TopProduct, error) {
	return handle.List[TopProduct](q.Query(ctx, `SELECT l.product_id, max(l.name) AS name, trim_scale(sum(l.quantity))::text AS quantity
		FROM commercial.order_lines l JOIN commercial.orders o ON o.id = l.order_id
		WHERE o.customer_id = $1 AND o.status IN ('paid', 'charged') AND l.status = 'active' GROUP BY l.product_id ORDER BY sum(l.quantity) DESC LIMIT $2`, customer, n))
}

// CustomerSection is the POS & voucher part of the Customer 360.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	a := POSActivity{}
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT o.id)::int, trim_scale(coalesce(sum(l.total_amount), 0))::text, max(o.created_at) FROM commercial.orders o
		LEFT JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active' WHERE o.customer_id = $1 AND o.status IN ('paid', 'charged')`, customer).Scan(&a.Orders, &a.Spend, &a.LastOrder); err != nil {
		return a, err
	}
	var err error
	if a.TopProducts, err = topProducts(ctx, q, customer, 5); err != nil {
		return a, err
	}
	a.Vouchers, err = voucher.ActiveVouchers(ctx, q, customer)
	return a, err
}

// CustomerBehavior derives F&B habits: most ordered items (FR-PRF-02/03).
func (m *Module) CustomerBehavior(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (crm.Behavior, error) {
	b := crm.Behavior{Facts: map[string]any{}, Highlights: []string{}}
	top, err := topProducts(ctx, q, customer, 3)
	if err != nil {
		return b, err
	}
	names := []string{}
	for _, t := range top {
		names = append(names, t.Name)
	}
	b.Facts["mostOrdered"] = names
	if len(top) > 0 {
		b.Highlights = append(b.Highlights, "Usually orders "+top[0].Name)
	}
	var spend string
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(l.total_amount), 0))::text FROM commercial.orders o JOIN commercial.order_lines l ON l.order_id = o.id
		AND l.status = 'active' WHERE o.customer_id = $1 AND o.status IN ('paid', 'charged') AND o.created_at > now() - interval '90 days'`, customer).Scan(&spend); err != nil {
		return b, err
	}
	b.Facts["spendLast90Days"] = spend
	return b, nil
}
