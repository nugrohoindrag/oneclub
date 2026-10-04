package billing

// Contract additions for PRD P2 (§5.4.2 C1–C3): folios, charges and
// payments of every business line on top of P1's OpenFolio, AddCharge and
// TakePayment, which stay unchanged. The P2 attributes (business line,
// revenue component, tender, POS outlet & shift, offline flag) are written
// to the expand columns of billing/00003. Review: P1 developer.

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// LineFolioInput opens a folio of a business line (C1).
type LineFolioInput struct {
	FolioInput
	BusinessLine  string     // golf | sportclub | stay | pos | membership | voucher | other
	ReservationID *uuid.UUID // Reservation Engine reservation of the folio
	CorporateName string
}

// OpenLineFolio opens a P1 folio and records its business line and
// reservation.
func (s *Service) OpenLineFolio(ctx context.Context, tx pgx.Tx, in LineFolioInput) (FolioRef, error) {
	if in.HolderName == "" {
		in.HolderName = "Walk-in"
	}
	if in.BusinessLine == "" {
		in.BusinessLine = LineOther
	}
	if !slices.Contains(BusinessLines, in.BusinessLine) {
		return FolioRef{}, errs.Validation("invalid_business_line", "unknown business line")
	}
	f, err := s.OpenFolio(ctx, tx, in.FolioInput)
	if err != nil {
		return f, err
	}
	_, err = tx.Exec(ctx, `UPDATE billing.folios SET business_line = $2, reservation_id = $3, corporate_name = $4 WHERE id = $1`,
		f.ID, in.BusinessLine, in.ReservationID, nullStr(in.CorporateName))
	return f, err
}

// LineCharge is a charge of a business line with its revenue component (C1).
type LineCharge struct {
	Charge
	BusinessLine     string     // default: the folio's
	RevenueComponent string     // default: the charge type
	TaxLines         any        // tax breakdown of the pricing snapshot
	BeneficiaryType  string     // caddy | instructor: money held for a partner
	BeneficiaryID    *uuid.UUID //
}

// chargeTypes are P1's charge types; other revenue components post as
// "other" with their component.
var chargeTypes = []string{"golf_round", "caddy_fee", "cart_fee", "extra_cart", "caddy_tip", "cancellation_fee", "no_show_fee",
	"membership_fee", "renewal_fee", "bag_storage", "locker", "rain_check_credit", "discount", "other"}

// AddLineCharge posts a charge through P1's AddCharge and records the
// business line, revenue component, tax lines and beneficiary.
func (s *Service) AddLineCharge(ctx context.Context, tx pgx.Tx, c LineCharge) (uuid.UUID, error) {
	if c.Quantity.IsZero() {
		c.Quantity = decimal.NewFromInt(1)
	}
	if c.UnitPrice.IsZero() {
		c.UnitPrice = c.Net.Div(c.Quantity)
	}
	if c.Total.IsZero() {
		c.Total = c.Net.Add(c.Tax).Add(c.Service)
	}
	if c.ChargeType == "" {
		c.ChargeType = "other"
		if slices.Contains(chargeTypes, c.RevenueComponent) {
			c.ChargeType = c.RevenueComponent
		}
	}
	if c.RevenueComponent == "" {
		c.RevenueComponent = c.ChargeType
	}
	lid, err := s.AddCharge(ctx, tx, c.Charge)
	if err != nil {
		return lid, err
	}
	taxes := c.TaxLines
	if taxes == nil {
		taxes = []any{}
	}
	raw, _ := json.Marshal(taxes)
	_, err = tx.Exec(ctx, `UPDATE billing.folio_lines l SET business_line = coalesce($2, f.business_line), revenue_component = $3, tax_lines = $4,
		beneficiary_type = $5, beneficiary_id = $6 FROM billing.folios f WHERE l.id = $1 AND f.id = l.folio_id`,
		lid, nullStr(c.BusinessLine), c.RevenueComponent, raw, nullStr(c.BeneficiaryType), c.BeneficiaryID)
	return lid, err
}

// TenderPaymentInput is a payment of any business line (C2, C3).
type TenderPaymentInput struct {
	PaymentInput
	Tender         map[string]any // voucher_prepaid: {code | voucherId}; folio_transfer: {targetFolioId}
	OutletID       *uuid.UUID     // POS outlet
	ShiftID        *uuid.UUID     // POS shift
	Offline        bool           // recorded offline: a member charge over the cached limit is accepted and reviewed
	IdempotencyKey string         // a retried payment returns the first one
}

// PaymentTender is the P2 part of a payment.
type PaymentTender struct {
	TenderRef   map[string]any `json:"tenderRef"`
	OutletID    *uuid.UUID     `json:"outletId"`
	ShiftID     *uuid.UUID     `json:"shiftId"`
	Offline     bool           `json:"offline"`
	NeedsReview bool           `json:"needsReview"`
}

// TenderOf returns the P2 part of a payment.
func TenderOf(ctx context.Context, q dbtx.Querier, paymentID uuid.UUID) (PaymentTender, error) {
	var t PaymentTender
	var raw []byte
	err := q.QueryRow(ctx, `SELECT tender_ref, outlet_id, shift_id, offline, needs_review FROM billing.payments WHERE id = $1`, paymentID).
		Scan(&raw, &t.OutletID, &t.ShiftID, &t.Offline, &t.NeedsReview)
	if dbtx.IsNoRows(err) {
		return t, errs.NotFound("payment")
	}
	_ = json.Unmarshal(raw, &t.TenderRef)
	return t, err
}

// TakeTender takes a payment through P1's TakePayment: a member charge on
// the member account of the folio's customer (any business line, offline
// accepted for review), a voucher / prepaid redemption or a transfer to
// another folio (registered tenders).
func (s *Service) TakeTender(ctx context.Context, tx pgx.Tx, in TenderPaymentInput) (Payment, error) {
	if in.IdempotencyKey != "" {
		var existing uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM billing.payments WHERE idempotency_key = $1`, in.IdempotencyKey).Scan(&existing)
		if err == nil {
			return GetPayment(ctx, tx, existing)
		}
		if !dbtx.IsNoRows(err) {
			return Payment{}, err
		}
	}
	ref := map[string]any{}
	review := false
	switch in.MethodType {
	case "member_account":
		if in.AccountID == nil && in.FolioID != nil {
			var cust *uuid.UUID
			var property uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT customer_id, property_id FROM billing.folios WHERE id = $1`, *in.FolioID).Scan(&cust, &property); err != nil {
				return Payment{}, err
			}
			if cust != nil {
				a, err := AccountFor(ctx, tx, property, *cust, "member")
				if err != nil {
					return Payment{}, err
				}
				if a != nil {
					in.AccountID = &a.ID
				}
			}
		}
		if in.AccountID == nil {
			return Payment{}, errs.Conflict("no_member_account", "the customer has no member account to charge")
		}
		if in.Offline {
			in.SkipCreditCheck, review = true, true // FR-POS-11
		}
	case "voucher_prepaid", "folio_transfer":
		if in.FolioID == nil {
			return Payment{}, errs.Validation("folio_required", "a folio is required for this payment method")
		}
		var property uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id FROM billing.folios WHERE id = $1`, *in.FolioID).Scan(&property); err != nil {
			return Payment{}, err
		}
		amt, r, err := s.applyTender(ctx, tx, property, *in.FolioID, in)
		if err != nil {
			return Payment{}, err
		}
		in.Amount, ref = amt, r
		in.Channel = "venue"
	}
	p, err := s.TakePayment(ctx, tx, in.PaymentInput)
	if err != nil {
		return p, err
	}
	raw, _ := json.Marshal(ref)
	if _, err := tx.Exec(ctx, `UPDATE billing.payments SET tender_ref = $2, outlet_id = $3, shift_id = $4, offline = $5, needs_review = $6,
		idempotency_key = $7 WHERE id = $1`, p.ID, raw, in.OutletID, in.ShiftID, in.Offline, review, nullStr(in.IdempotencyKey)); err != nil {
		return p, err
	}
	if in.MethodType == "member_account" {
		if err := tagEntries(ctx, tx, p.ID); err != nil {
			return p, err
		}
	}
	return p, nil
}
