package voucher

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
)

// Quota returns the usable quota of a customer for a voucher category and
// item (e.g. class_package for program SWIM-KIDS): remaining units across
// active, unexpired vouchers whose applicable items include item (or none).
func Quota(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, category, item string) (decimal.Decimal, error) {
	var raw string
	err := q.QueryRow(ctx, `SELECT coalesce(sum(v.remaining_quantity), 0)::text FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
		WHERE v.property_id = $1 AND v.customer_id = $2 AND t.category = $3 AND v.status IN ('active', 'partially_redeemed')
		AND (v.expires_at IS NULL OR v.expires_at > now()) AND (cardinality(t.applicable_items) = 0 OR $4 = ANY(t.applicable_items))`,
		property, customerID, category, item).Scan(&raw)
	d, _ := decimal.NewFromString(raw)
	return d, err
}

// UseQuota redeems units of a customer's quota (oldest expiry first) for an
// item; idempotent per key. Returns the voucher used first.
func (m *Module) UseQuota(ctx context.Context, tx pgx.Tx, property, customerID uuid.UUID, category, item string, qty decimal.Decimal,
	serviceType, sourceType string, sourceID *uuid.UUID, key string) (*uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT v.id FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
		WHERE v.property_id = $1 AND v.customer_id = $2 AND t.category = $3 AND v.status IN ('active', 'partially_redeemed')
		AND (v.expires_at IS NULL OR v.expires_at > now()) AND (cardinality(t.applicable_items) = 0 OR $4 = ANY(t.applicable_items))
		ORDER BY v.expires_at NULLS LAST, v.issued_at`, property, customerID, category, item)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var x uuid.UUID
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, x)
	}
	rows.Close()
	if key != "" {
		// a retry finds the earlier redemption on any of the vouchers
		var vid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT voucher_id FROM commercial.voucher_ledger WHERE idempotency_key = $1 AND property_id = $2`, key, property).Scan(&vid)
		if err == nil {
			return &vid, nil
		}
		if !dbtx.IsNoRows(err) {
			return nil, err
		}
	}
	for _, vid := range ids {
		v, err := m.Voucher(ctx, tx, vid)
		if err != nil {
			return nil, err
		}
		if rem, _ := decimal.NewFromString(v.RemainingQuantity); rem.LessThan(qty) {
			continue
		}
		x := vid
		if _, err := m.Redeem(ctx, tx, RedeemRequest{PropertyID: property, VoucherID: &x, Quantity: qty, ServiceType: serviceType, ItemRef: item,
			CustomerID: &customerID, SourceType: sourceType, SourceID: sourceID, IdempotencyKey: key}); err != nil {
			return nil, err
		}
		return &x, nil
	}
	return nil, errs.Conflict("quota_exhausted", "no package quota left for "+item+"; buy a new package")
}

// Restore gives units back to a voucher (e.g. class session cancelled by the
// club, FR-CLS-07) and re-defers the revenue recognised for them.
func (m *Module) Restore(ctx context.Context, tx pgx.Tx, voucherID uuid.UUID, qty decimal.Decimal, reason, key string) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM commercial.vouchers WHERE id = $1 FOR UPDATE`, voucherID); err != nil {
		return err
	}
	if key != "" {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM commercial.voucher_ledger WHERE voucher_id = $1 AND idempotency_key = $2`, voucherID, key).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	v, err := m.Voucher(ctx, tx, voucherID)
	if err != nil {
		return err
	}
	if v.Status == "void" || v.Status == "expired" {
		return nil
	}
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.vouchers WHERE id = $1`, voucherID).Scan(&pid); err != nil {
		return err
	}
	rem, _ := decimal.NewFromString(v.RemainingQuantity)
	orig, _ := decimal.NewFromString(v.OriginalQuantity)
	after := decimal.Min(rem.Add(qty), orig)
	status := "partially_redeemed"
	if after.Equal(orig) {
		status = "active"
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.vouchers SET remaining_quantity = $2::numeric, status = $3 WHERE id = $1`, voucherID, after.String(), status); err != nil {
		return err
	}
	unit, _ := decimal.NewFromString(v.UnitValue)
	amt := after.Sub(rem).Mul(unit).Round(0)
	if err := m.ledger(ctx, tx, pid, voucherID, "reversal", after.Sub(rem), amt.Neg(), after, ledgerOpts{reason: reason, idem: key}); err != nil {
		return err
	}
	if amt.IsPositive() {
		vt, err := loadVoucherType(ctx, tx, `id = $1`, v.VoucherTypeID)
		if err != nil {
			return err
		}
		if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: pid, LiabilityType: liabilityType(vt), RefType: "commercial.voucher",
			RefID: voucherID, EntryType: "adjustment", Amount: amt, RevenueComponent: vt.RevenueComponent, Description: "Voucher " + v.Code + " restored: " + reason}); err != nil {
			return err
		}
	}
	after2, _ := m.Voucher(ctx, tx, voucherID)
	return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "restore", EntityType: "commercial.voucher", EntityID: voucherID.String(),
		EntityLabel: v.Code, PropertyID: &pid, Before: v, After: after2, Reason: reason})
}
