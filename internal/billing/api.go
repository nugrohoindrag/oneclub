package billing

// Public interface of Billing & Payment (Technical Doc §4.2, §4.4): golf and
// membership post charges to folios, take payments and react to
// billing.payment_settled. Every money mutation runs in the caller's
// transaction, is audited and never deletes a row (void / cancel / refund).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/docno"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/rules"
)

// Domain events (FR-PAY-13).
const (
	EventPaymentSettled  = "billing.payment_settled"
	EventRefundProcessed = "billing.refund_processed"
	EventFolioClosed     = "billing.folio_closed"
	EventPaymentFailed   = "billing.payment_failed"
)

// Publisher publishes domain events (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Payments is the integration surface billing needs (integration.Service).
type Payments interface {
	PaymentWithCode(ctx context.Context) (integration.PaymentAdapter, string, error)
	ByCode(ctx context.Context, code string) (any, error)
}

// Service is the billing domain service shared by HTTP handlers and other
// modules (wired once by the composition root).
type Service struct {
	DB       *dbtx.DB
	Events   Publisher
	Gateways Payments
}

func places(cur string) int32 {
	if cur == "IDR" || cur == "JPY" {
		return 0
	}
	return 2
}

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func actor(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

func businessDate(ctx context.Context, q dbtx.Querier, property uuid.UUID, t time.Time) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

func number(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string) (string, error) {
	return docno.Daily(ctx, tx, "billing.sequences", property, prefix, businessDate(ctx, tx, property, clock.Now()))
}

// ── customer accounts (FR-MEM-11) ─────────────────────────────────────────

// Account is a customer / member account.
type Account struct {
	ID          uuid.UUID  `json:"id"`
	Number      string     `json:"number"`
	CustomerID  uuid.UUID  `json:"customerId"`
	AccountType string     `json:"accountType" enum:"member,corporate,customer"`
	MemberID    *uuid.UUID `json:"memberId"`
	CreditLimit *string    `json:"creditLimit"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status" enum:"active,inactive,suspended"`
	Balance     string     `json:"balance" doc:"Amount owed (positive) or credit (negative)"`
	CreatedAt   time.Time  `json:"createdAt"`
}

const accountCols = `a.id, a.number, a.customer_id, a.account_type, a.member_id, a.credit_limit::text, a.currency, a.status,
	coalesce((SELECT sum(amount) FROM billing.account_entries e WHERE e.account_id = a.id), 0)::text, a.created_at`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Number, &a.CustomerID, &a.AccountType, &a.MemberID, &a.CreditLimit, &a.Currency, &a.Status, &a.Balance, &a.CreatedAt)
	return a, err
}

// GetAccount loads an account with its balance.
func GetAccount(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (Account, error) {
	a, err := scanAccount(q.QueryRow(ctx, `SELECT `+accountCols+` FROM billing.customer_accounts a WHERE a.id = $1`, aid))
	if dbtx.IsNoRows(err) {
		return a, errs.NotFound("customer account")
	}
	return a, err
}

// AccountFor returns the account of a customer (by type), if any.
func AccountFor(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, accountType string) (*Account, error) {
	a, err := scanAccount(q.QueryRow(ctx, `SELECT `+accountCols+` FROM billing.customer_accounts a
		WHERE a.property_id = $1 AND a.customer_id = $2 AND a.account_type = $3`, property, customerID, accountType))
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// EnsureAccount opens the account if missing (member activation, migration).
func (s *Service) EnsureAccount(ctx context.Context, tx pgx.Tx, property, customerID uuid.UUID, accountType string, memberID *uuid.UUID) (Account, error) {
	if a, err := AccountFor(ctx, tx, property, customerID, accountType); err != nil || a != nil {
		if a != nil && memberID != nil && (a.MemberID == nil || *a.MemberID != *memberID) {
			if _, err := tx.Exec(ctx, `UPDATE billing.customer_accounts SET member_id = $2, status = 'active' WHERE id = $1`, a.ID, *memberID); err != nil {
				return Account{}, err
			}
			return GetAccount(ctx, tx, a.ID)
		}
		if a != nil {
			return *a, err
		}
		return Account{}, err
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return Account{}, err
	}
	num, err := number(ctx, tx, property, "ACC")
	if err != nil {
		return Account{}, err
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.customer_accounts (id, property_id, number, customer_id, account_type, member_id, currency, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, aid, property, num, customerID, accountType, memberID, cur, id.Ptr(actor(ctx))); err != nil {
		return Account{}, err
	}
	a, err := GetAccount(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	return a, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.customer_account",
		EntityID: aid.String(), EntityLabel: num, PropertyID: &property, After: a})
}

// PostEntry writes a ledger entry (positive = owed).
func PostEntry(ctx context.Context, tx pgx.Tx, property, accountID uuid.UUID, entryType string, amount decimal.Decimal, currency, description string,
	folioID, paymentID *uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO billing.account_entries (id, property_id, account_id, entry_type, amount, currency, description, folio_id, payment_id, created_by)
		VALUES ($1,$2,$3,$4,$5::numeric,$6,$7,$8,$9,$10)`, id.New(), property, accountID, entryType, amount.String(), currency, description, folioID, paymentID,
		id.Ptr(actor(ctx)))
	return err
}

// MemberPolicy is the Member Policies club policy (FR-POL-08).
type MemberPolicy struct {
	AllowMemberCharge bool   `json:"allowMemberCharge"`
	MemberChargeLimit string `json:"memberChargeLimit"`
}

// DefaultMemberPolicy applies until the club configures its own.
var DefaultMemberPolicy = MemberPolicy{AllowMemberCharge: true, MemberChargeLimit: "10000000"}

// LoadMemberPolicy resolves the Member Policies version in force.
func LoadMemberPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (MemberPolicy, int, error) {
	p := DefaultMemberPolicy
	raw, v, ok, err := rules.Resolve(ctx, q, "club_policy", "member.charge", &property, clock.Now())
	if err != nil || !ok {
		return p, 0, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return DefaultMemberPolicy, 0, nil
	}
	return p, v, nil
}

// CheckCredit verifies a member charge fits the limit.
func CheckCredit(ctx context.Context, q dbtx.Querier, property uuid.UUID, a Account, amount decimal.Decimal) error {
	if a.Status != "active" {
		return errs.Conflict("account_inactive", "the member account is not active")
	}
	pol, _, err := LoadMemberPolicy(ctx, q, property)
	if err != nil {
		return err
	}
	limit := dec(pol.MemberChargeLimit)
	if a.AccountType == "corporate" {
		// PRD P3 FR-BIL-P3-05: the Credit Policies default applies to
		// corporate accounts (city ledger), not the member charge switch.
		cp, _, err := LoadCreditPolicy(ctx, q, property)
		if err != nil {
			return err
		}
		limit = dec(cp.DefaultCorporateLimit)
	} else if !pol.AllowMemberCharge {
		return errs.Conflict("member_charge_disabled", "member charge is not allowed by the Member Policy")
	}
	if a.CreditLimit != nil {
		limit = dec(*a.CreditLimit)
	}
	if !limit.IsZero() {
		extra, err := creditHeadroom(ctx, q, a.ID, clock.Now()) // approved credit overrides (P3)
		if err != nil {
			return err
		}
		limit = limit.Add(extra)
	}
	if !limit.IsZero() && dec(a.Balance).Add(amount).GreaterThan(limit) {
		return errs.Conflict("credit_limit_exceeded", fmt.Sprintf("member charge limit exceeded (balance %s + %s > limit %s)", dec(a.Balance).String(), amount.String(), limit.String()))
	}
	return nil
}

// ── folios ────────────────────────────────────────────────────────────────

// FolioInput opens a folio.
type FolioInput struct {
	Property   uuid.UUID
	CustomerID *uuid.UUID
	GuestID    *uuid.UUID
	HolderName string
	SourceType string // golf_booking | walk_in | membership_fee | membership_renewal | bag_storage | other
	SourceID   *uuid.UUID
	SourceRef  string
}

// FolioRef identifies a folio.
type FolioRef struct {
	ID     uuid.UUID `json:"id"`
	Number string    `json:"number"`
}

// OpenFolio creates an open folio.
func (s *Service) OpenFolio(ctx context.Context, tx pgx.Tx, in FolioInput) (FolioRef, error) {
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return FolioRef{}, err
	}
	num, err := number(ctx, tx, in.Property, "FOL")
	if err != nil {
		return FolioRef{}, err
	}
	fid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.folios (id, property_id, number, customer_id, guest_id, holder_name, source_type, source_id, source_ref,
		currency, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
		fid, in.Property, num, in.CustomerID, in.GuestID, in.HolderName, in.SourceType, in.SourceID, nullStr(in.SourceRef), cur, id.Ptr(actor(ctx))); err != nil {
		return FolioRef{}, err
	}
	ref := FolioRef{ID: fid, Number: num}
	return ref, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.folio", EntityID: fid.String(),
		EntityLabel: num, PropertyID: &in.Property, After: map[string]any{"number": num, "holder": in.HolderName, "source": in.SourceType, "sourceRef": in.SourceRef}})
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// Charge is a folio line to post.
type Charge struct {
	FolioID       uuid.UUID
	ChargeType    string
	Description   string
	Quantity      decimal.Decimal
	UnitPrice     decimal.Decimal
	Net           decimal.Decimal
	Tax           decimal.Decimal
	Service       decimal.Decimal
	Total         decimal.Decimal
	Components    any
	SnapshotID    *uuid.UUID
	ReferenceType string
	ReferenceID   *uuid.UUID
	Liability     bool
}

// ErrFolioClosed is returned when posting to a closed folio.
var ErrFolioClosed = errs.Conflict("folio_closed", "the folio is closed; reopen it first")

func lockFolio(ctx context.Context, tx pgx.Tx, fid uuid.UUID) (uuid.UUID, string, string, error) {
	var pid uuid.UUID
	var status, cur string
	err := tx.QueryRow(ctx, `SELECT property_id, status, currency FROM billing.folios WHERE id = $1 FOR UPDATE`, fid).Scan(&pid, &status, &cur)
	if dbtx.IsNoRows(err) {
		return pid, "", "", errs.NotFound("folio")
	}
	return pid, status, cur, err
}

// AddCharge posts a charge line to an open folio.
func (s *Service) AddCharge(ctx context.Context, tx pgx.Tx, c Charge) (uuid.UUID, error) {
	pid, status, cur, err := lockFolio(ctx, tx, c.FolioID)
	if err != nil {
		return uuid.Nil, err
	}
	if status != "open" {
		return uuid.Nil, ErrFolioClosed
	}
	if c.Quantity.IsZero() {
		c.Quantity = decimal.NewFromInt(1)
	}
	if c.Total.IsZero() && !c.Net.IsZero() {
		c.Total = c.Net.Add(c.Tax).Add(c.Service)
	}
	if c.Net.IsZero() && !c.Total.IsZero() {
		c.Net = c.Total.Sub(c.Tax).Sub(c.Service)
	}
	comps := c.Components
	if comps == nil {
		comps = []any{}
	}
	raw, _ := json.Marshal(comps)
	lid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.folio_lines (id, property_id, folio_id, charge_type, description, quantity, unit_price, net_amount,
		tax_amount, service_amount, total, currency, components, pricing_snapshot_id, reference_type, reference_id, liability, posted_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12,$13,$14,$15,$16,$17,$18)`,
		lid, pid, c.FolioID, c.ChargeType, c.Description, c.Quantity.String(), c.UnitPrice.String(), c.Net.String(), c.Tax.String(), c.Service.String(),
		c.Total.String(), cur, raw, c.SnapshotID, nullStr(c.ReferenceType), c.ReferenceID, c.Liability, id.Ptr(actor(ctx))); err != nil {
		return uuid.Nil, err
	}
	if err := bumpVersion(ctx, tx, c.FolioID); err != nil {
		return uuid.Nil, err
	}
	return lid, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "add_charge", EntityType: "billing.folio", EntityID: c.FolioID.String(),
		EntityLabel: c.Description, PropertyID: &pid, After: map[string]any{"lineId": lid, "chargeType": c.ChargeType, "total": c.Total.String(), "snapshotId": c.SnapshotID}})
}

func bumpVersion(ctx context.Context, tx pgx.Tx, fid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE billing.folios SET version = version + 1, updated_by = $2 WHERE id = $1`, fid, id.Ptr(actor(ctx)))
	return err
}

// VoidCharge voids a line (never deleted).
func (s *Service) VoidCharge(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, reason string) error {
	var fid, property uuid.UUID
	var voided, bday *time.Time
	var total string
	if err := tx.QueryRow(ctx, `SELECT folio_id, property_id, voided_at, total::text, business_date FROM billing.folio_lines WHERE id = $1`, lineID).
		Scan(&fid, &property, &voided, &total, &bday); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("folio line")
		}
		return err
	}
	if voided != nil {
		return nil
	}
	if bday != nil {
		if err := ensureOpenPeriod(ctx, tx, property, *bday, "post a correcting charge instead of voiding"); err != nil {
			return err
		}
	}
	pid, status, _, err := lockFolio(ctx, tx, fid)
	if err != nil {
		return err
	}
	if status != "open" {
		return ErrFolioClosed
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.folio_lines SET voided_at = now(), voided_by = $2, void_reason = $3 WHERE id = $1`, lineID, id.Ptr(actor(ctx)), reason); err != nil {
		return err
	}
	if err := bumpVersion(ctx, tx, fid); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionVoid, EntityType: "billing.folio", EntityID: fid.String(),
		EntityLabel: "line " + lineID.String(), PropertyID: &pid, Reason: reason, Before: map[string]any{"lineId": lineID, "total": total}})
}

// Summary is the money position of a folio.
type Summary struct {
	Currency     string `json:"currency"`
	Charges      string `json:"charges"`
	Payments     string `json:"payments" doc:"Completed payments net of refunds (deposits applied included)"`
	HeldDeposits string `json:"heldDeposits"`
	Balance      string `json:"balance" doc:"Charges − payments; positive = still to pay"`
}

// FolioSummary computes charges, payments and balance.
func FolioSummary(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (Summary, error) {
	var s Summary
	var charges, payments, held string
	err := q.QueryRow(ctx, `SELECT f.currency,
		coalesce((SELECT sum(total) FROM billing.folio_lines l WHERE l.folio_id = f.id AND l.voided_at IS NULL), 0)::text,
		coalesce((SELECT sum(amount - refunded_amount) FROM billing.payments p WHERE p.folio_id = f.id AND p.status IN ('completed', 'refunded') AND p.purpose = 'settlement'), 0)
		  + coalesce((SELECT sum(applied_amount) FROM billing.deposits d WHERE d.folio_id = f.id), 0),
		coalesce((SELECT sum(amount - applied_amount) FROM billing.deposits d WHERE d.folio_id = f.id AND d.status = 'held'), 0)::text
		FROM billing.folios f WHERE f.id = $1`, fid).Scan(&s.Currency, &charges, &payments, &held)
	if dbtx.IsNoRows(err) {
		return s, errs.NotFound("folio")
	}
	if err != nil {
		return s, err
	}
	p := places(s.Currency)
	c, pay := dec(charges), dec(payments)
	s.Charges, s.Payments, s.HeldDeposits = c.StringFixed(p), pay.StringFixed(p), dec(held).StringFixed(p)
	s.Balance = c.Sub(pay).StringFixed(p)
	return s, nil
}

// ── payments ──────────────────────────────────────────────────────────────

// Payment is the API view of a payment.
type Payment struct {
	ID              uuid.UUID  `json:"id"`
	Number          string     `json:"number"`
	FolioID         *uuid.UUID `json:"folioId"`
	FolioNumber     *string    `json:"folioNumber"`
	AccountID       *uuid.UUID `json:"accountId"`
	PaymentMethodID *uuid.UUID `json:"paymentMethodId"`
	MethodType      string     `json:"methodType" enum:"cash,bank_transfer,virtual_account,qris,card,payment_gateway,member_account,voucher_prepaid,folio_transfer,loyalty_points"`
	Channel         string     `json:"channel" enum:"online,venue,member_account"`
	Purpose         string     `json:"purpose" enum:"settlement,deposit,account_settlement"`
	Amount          string     `json:"amount"`
	RefundedAmount  string     `json:"refundedAmount"`
	Currency        string     `json:"currency"`
	Status          string     `json:"status" enum:"pending,completed,cancelled,refunded"`
	IntegrationCode *string    `json:"integrationCode"`
	ExternalID      *string    `json:"externalId"`
	CheckoutURL     *string    `json:"checkoutUrl"`
	QRString        *string    `json:"qrString"`
	VANumber        *string    `json:"vaNumber"`
	Reference       *string    `json:"reference"`
	PayerName       *string    `json:"payerName"`
	ExpiresAt       *time.Time `json:"expiresAt"`
	PaidAt          *time.Time `json:"paidAt"`
	ReceivedBy      *uuid.UUID `json:"receivedBy"`
	ReceivedByName  *string    `json:"receivedByName"`
	CreatedAt       time.Time  `json:"createdAt"`
}

const paymentCols = `p.id, p.number, p.folio_id, f.number, p.account_id, p.payment_method_id, p.method_type, p.channel, p.purpose, p.amount::text,
	p.refunded_amount::text, p.currency, p.status, p.integration_code, p.external_id, p.checkout_url, p.qr_string, p.va_number, p.reference,
	p.payer_name, p.expires_at, p.paid_at, p.received_by, u.full_name, p.created_at`

const paymentFrom = ` FROM billing.payments p LEFT JOIN billing.folios f ON f.id = p.folio_id LEFT JOIN platform.users u ON u.id = p.received_by`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.Number, &p.FolioID, &p.FolioNumber, &p.AccountID, &p.PaymentMethodID, &p.MethodType, &p.Channel, &p.Purpose,
		&p.Amount, &p.RefundedAmount, &p.Currency, &p.Status, &p.IntegrationCode, &p.ExternalID, &p.CheckoutURL, &p.QRString, &p.VANumber,
		&p.Reference, &p.PayerName, &p.ExpiresAt, &p.PaidAt, &p.ReceivedBy, &p.ReceivedByName, &p.CreatedAt)
	return p, err
}

// GetPayment loads a payment.
func GetPayment(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (Payment, error) {
	p, err := scanPayment(q.QueryRow(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE p.id = $1`, pid))
	if dbtx.IsNoRows(err) {
		return p, errs.NotFound("payment")
	}
	return p, err
}

// PaymentInput takes a payment.
type PaymentInput struct {
	FolioID         *uuid.UUID
	AccountID       *uuid.UUID // member charge target, or the account being settled
	MethodType      string
	PaymentMethodID *uuid.UUID
	Channel         string // online | venue | member_account
	Purpose         string // settlement | deposit | account_settlement
	Amount          decimal.Decimal
	Reference       string
	PayerName       string
	Description     string
	ExpiresAt       *time.Time // online payments
	SkipCreditCheck bool
}

var onlineMethods = map[string]bool{"qris": true, "virtual_account": true, "card": true, "payment_gateway": true}

// TakePayment records a payment. Venue payments and member charges complete
// immediately; online payments create a gateway checkout and stay Pending
// until the signed webhook settles them (FR-PAY-03..05).
func (s *Service) TakePayment(ctx context.Context, tx pgx.Tx, in PaymentInput) (Payment, error) {
	if !in.Amount.IsPositive() {
		return Payment{}, errs.Validation("invalid_amount", "amount must be positive", errs.Field("amount", "invalid", "positive decimal"))
	}
	if in.Purpose == "" {
		in.Purpose = "settlement"
	}
	if in.Channel == "" {
		switch in.MethodType {
		case "member_account":
			in.Channel = "member_account"
		default:
			in.Channel = "venue"
		}
	}
	var property uuid.UUID
	var cur, holder string
	if in.FolioID != nil {
		var status string
		var err error
		property, status, cur, err = lockFolio(ctx, tx, *in.FolioID)
		if err != nil {
			return Payment{}, err
		}
		if status != "open" {
			return Payment{}, ErrFolioClosed
		}
		_ = tx.QueryRow(ctx, `SELECT holder_name FROM billing.folios WHERE id = $1`, *in.FolioID).Scan(&holder)
	} else if in.AccountID != nil {
		a, err := GetAccount(ctx, tx, *in.AccountID)
		if err != nil {
			return Payment{}, err
		}
		if err := tx.QueryRow(ctx, `SELECT property_id FROM billing.customer_accounts WHERE id = $1`, a.ID).Scan(&property); err != nil {
			return Payment{}, err
		}
		cur = a.Currency
		in.Purpose = "account_settlement"
	} else {
		return Payment{}, errs.Validation("folio_required", "a folio or an account is required")
	}
	if in.PaymentMethodID == nil && in.MethodType != "" {
		var pmID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM billing.payment_methods WHERE method_type = $1 AND status = 'active' AND archived_at IS NULL
			ORDER BY sort_order, code LIMIT 1`, in.MethodType).Scan(&pmID)
		if err == nil {
			in.PaymentMethodID = &pmID
		}
	}
	if in.PaymentMethodID != nil {
		var mt string
		if err := tx.QueryRow(ctx, `SELECT method_type FROM billing.payment_methods WHERE id = $1 AND status = 'active'`, *in.PaymentMethodID).Scan(&mt); err != nil {
			return Payment{}, errs.Validation("invalid_payment_method", "payment method not found or inactive", errs.Field("paymentMethodId", "invalid", "unknown method"))
		}
		in.MethodType = mt
		// availability per property (FR-MD-08)
		sets, err := Effective(ctx, tx, property, clock.Now())
		if err != nil {
			return Payment{}, err
		}
		for _, st := range sets {
			if st.PaymentMethodID == *in.PaymentMethodID && st.OutletID == nil && !st.Enabled {
				return Payment{}, errs.Conflict("payment_method_disabled", "this payment method is not available at this property")
			}
		}
	}
	if in.MethodType == "" {
		return Payment{}, errs.Validation("method_required", "payment method is required", errs.Field("methodType", "required", "choose a payment method"))
	}
	num, err := number(ctx, tx, property, "PAY")
	if err != nil {
		return Payment{}, err
	}
	pid := id.New()
	status := "completed"
	var paidAt *time.Time
	now := clock.Now()
	var receivedBy *uuid.UUID
	var integrationCode, externalID, checkout, qr, va *string

	switch {
	case in.MethodType == "member_account":
		if in.Purpose == "account_settlement" {
			return Payment{}, errs.Validation("invalid_method", "an account cannot be settled with a member charge")
		}
		if in.AccountID == nil {
			return Payment{}, errs.Validation("account_required", "member account is required for a member charge", errs.Field("accountId", "required", "choose the member account"))
		}
		a, err := GetAccount(ctx, tx, *in.AccountID)
		if err != nil {
			return Payment{}, err
		}
		if !in.SkipCreditCheck {
			if err := CheckCredit(ctx, tx, property, a, in.Amount); err != nil {
				return Payment{}, err
			}
		}
		in.Channel = "member_account"
		paidAt = &now
	case in.Channel == "online" || (onlineMethods[in.MethodType] && in.Channel != "venue"):
		in.Channel = "online"
		if s.Gateways == nil {
			return Payment{}, errs.Unavailable("payment gateway not configured")
		}
		gw, code, err := s.Gateways.PaymentWithCode(ctx)
		if errors.Is(err, integration.ErrNotConfigured) {
			return Payment{}, errs.Unavailable("online payment is not available: no payment gateway integration is enabled")
		}
		if err != nil {
			return Payment{}, err
		}
		method := in.MethodType
		if method == "payment_gateway" {
			method = ""
		}
		res, err := gw.CreatePayment(ctx, integration.PaymentRequest{Reference: num, Amount: in.Amount.String(), Currency: cur, Method: method,
			Description: in.Description, CustomerRef: holder})
		if err != nil {
			return Payment{}, errs.Unavailable("payment gateway error: " + err.Error())
		}
		integrationCode, externalID = &code, &res.ExternalID
		checkout, qr, va = nullStr(res.CheckoutURL), nullStr(res.QRString), nullStr(res.VANumber)
		status = "pending"
		if res.Status == "paid" {
			status, paidAt = "completed", &now
		}
	default:
		paidAt = &now
		u := actor(ctx)
		receivedBy = id.Ptr(u)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.payments (id, property_id, number, folio_id, account_id, payment_method_id, method_type, channel, purpose,
		amount, currency, status, integration_code, external_id, checkout_url, qr_string, va_number, reference, payer_name, expires_at, paid_at, received_by,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$23)`,
		pid, property, num, in.FolioID, in.AccountID, in.PaymentMethodID, in.MethodType, in.Channel, in.Purpose, in.Amount.String(), cur, status,
		integrationCode, externalID, checkout, qr, va, nullStr(in.Reference), nullStr(in.PayerName), in.ExpiresAt, paidAt, receivedBy, id.Ptr(actor(ctx))); err != nil {
		return Payment{}, err
	}
	p, err := GetPayment(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.payment", EntityID: pid.String(),
		EntityLabel: num, PropertyID: &property, After: p}); err != nil {
		return p, err
	}
	if status == "completed" {
		if err := s.afterSettled(ctx, tx, p, property); err != nil {
			return p, err
		}
	}
	return GetPayment(ctx, tx, pid)
}

// afterSettled posts the side effects of a completed payment: member
// account entries, deposits, folio version and the settled event.
func (s *Service) afterSettled(ctx context.Context, tx pgx.Tx, p Payment, property uuid.UUID) error {
	amt := dec(p.Amount)
	switch {
	case p.MethodType == "member_account" && p.AccountID != nil:
		desc := "Member charge " + p.Number
		if p.FolioNumber != nil {
			desc = "Member charge " + *p.FolioNumber
		}
		if err := PostEntry(ctx, tx, property, *p.AccountID, "charge", amt, p.Currency, desc, p.FolioID, &p.ID); err != nil {
			return err
		}
	case p.Purpose == "account_settlement" && p.AccountID != nil:
		if err := PostEntry(ctx, tx, property, *p.AccountID, "payment", amt.Neg(), p.Currency, "Payment "+p.Number, nil, &p.ID); err != nil {
			return err
		}
	}
	if p.Purpose == "deposit" && p.FolioID != nil {
		num, err := number(ctx, tx, property, "DEP")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO billing.deposits (id, property_id, number, folio_id, customer_id, payment_id, amount, currency, status, created_by, updated_by)
			SELECT $1, $2, $3, f.id, f.customer_id, $4, $5::numeric, $6, 'held', $7, $7 FROM billing.folios f WHERE f.id = $8`,
			id.New(), property, num, p.ID, p.Amount, p.Currency, id.Ptr(actor(ctx)), *p.FolioID); err != nil {
			return err
		}
	}
	if p.FolioID != nil {
		if err := bumpVersion(ctx, tx, *p.FolioID); err != nil {
			return err
		}
	}
	var sourceType, sourceID *string
	if p.FolioID != nil {
		_ = tx.QueryRow(ctx, `SELECT source_type, source_id::text FROM billing.folios WHERE id = $1`, *p.FolioID).Scan(&sourceType, &sourceID)
	}
	payload := map[string]any{"paymentId": p.ID, "number": p.Number, "folioId": p.FolioID, "amount": p.Amount, "currency": p.Currency,
		"methodType": p.MethodType, "purpose": p.Purpose, "sourceType": sourceType, "sourceId": sourceID}
	_, err := s.Events.Publish(ctx, tx, EventPaymentSettled, "billing.payment", &p.ID, &property, payload)
	return err
}

// SettledPayload is the payload of billing.payment_settled.
type SettledPayload struct {
	PaymentID  uuid.UUID  `json:"paymentId"`
	Number     string     `json:"number"`
	FolioID    *uuid.UUID `json:"folioId"`
	Amount     string     `json:"amount"`
	MethodType string     `json:"methodType"`
	Purpose    string     `json:"purpose"`
	SourceType *string    `json:"sourceType"`
	SourceID   *string    `json:"sourceId"`
}

// SettleGateway completes a pending online payment from a verified webhook.
// It is idempotent: an already completed payment is left unchanged
// (FR-PAY-03 AC: the same webhook three times adds one payment).
func (s *Service) SettleGateway(ctx context.Context, tx pgx.Tx, integrationCode string, data map[string]any) error {
	ext, _ := data["externalId"].(string)
	ref, _ := data["reference"].(string)
	status, _ := data["status"].(string)
	var pid, property uuid.UUID
	var cur, amount, current string
	err := tx.QueryRow(ctx, `SELECT id, property_id, currency, amount::text, status FROM billing.payments
		WHERE (external_id = $1 AND $1 <> '' AND integration_code = $3) OR (number = $2 AND $2 <> '' AND channel = 'online') FOR UPDATE LIMIT 1`, ext, ref, integrationCode).
		Scan(&pid, &property, &cur, &amount, &current)
	if dbtx.IsNoRows(err) {
		return nil // not ours (or another instance); acknowledged
	}
	if err != nil {
		return err
	}
	if current != "pending" && (current != "cancelled" || status != "paid") {
		return nil
	}
	switch status {
	case "paid":
		if got, ok := data["amount"].(string); ok && got != "" && !dec(got).Equal(dec(amount)) {
			return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "payment_amount_mismatch", Category: audit.CategorySystem,
				EntityType: "billing.payment", EntityID: pid.String(), PropertyID: &property, ActorName: "webhook:" + integrationCode,
				Metadata: map[string]any{"expected": amount, "received": got}})
		}
		if _, err := tx.Exec(ctx, `UPDATE billing.payments SET status = 'completed', paid_at = now() WHERE id = $1`, pid); err != nil {
			return err
		}
		p, err := GetPayment(ctx, tx, pid)
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "payment_settled", EntityType: "billing.payment", EntityID: pid.String(),
			EntityLabel: p.Number, PropertyID: &property, ActorName: "webhook:" + integrationCode, After: p}); err != nil {
			return err
		}
		return s.afterSettled(ctx, tx, p, property)
	case "failed", "expired":
		if _, err := tx.Exec(ctx, `UPDATE billing.payments SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2 WHERE id = $1`, pid, "gateway: "+status); err != nil {
			return err
		}
		if _, err := s.Events.Publish(ctx, tx, EventPaymentFailed, "billing.payment", &pid, &property, map[string]any{"paymentId": pid, "status": status}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "payment_failed", EntityType: "billing.payment", EntityID: pid.String(),
			PropertyID: &property, ActorName: "webhook:" + integrationCode, Metadata: map[string]any{"status": status}})
	}
	return nil
}

// CancelPending cancels pending payments of a folio (expired online
// checkout, booking released).
func (s *Service) CancelPending(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, reason string) error {
	rows, err := tx.Query(ctx, `UPDATE billing.payments SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2
		WHERE folio_id = $1 AND status = 'pending' RETURNING id, property_id, number`, folioID, reason)
	if err != nil {
		return err
	}
	type c struct {
		id, prop uuid.UUID
		num      string
	}
	var list []c
	for rows.Next() {
		var x c
		if err := rows.Scan(&x.id, &x.prop, &x.num); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionStatusChange, EntityType: "billing.payment", EntityID: x.id.String(),
			EntityLabel: x.num, PropertyID: &x.prop, Reason: reason, After: map[string]any{"status": "cancelled"}}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ── refunds (FR-PAY-07) ───────────────────────────────────────────────────

// RefundPolicy is the Refund Policies club policy (FR-POL-04).
type RefundPolicy struct {
	AutoRefundLimit string   `json:"autoRefundLimit" doc:"Refunds up to this amount complete without approval"`
	Destinations    []string `json:"destinations"`
}

var DefaultRefundPolicy = RefundPolicy{AutoRefundLimit: "2000000", Destinations: []string{"original_method", "member_account"}}

func LoadRefundPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (RefundPolicy, int, error) {
	p := DefaultRefundPolicy
	raw, v, ok, err := rules.Resolve(ctx, q, "club_policy", "refund.policy", &property, clock.Now())
	if err != nil || !ok {
		return p, 0, err
	}
	if json.Unmarshal(raw, &p) != nil {
		return DefaultRefundPolicy, 0, nil
	}
	return p, v, nil
}

// Refund is the API view of a refund.
type Refund struct {
	ID                uuid.UUID  `json:"id"`
	Number            string     `json:"number"`
	PaymentID         uuid.UUID  `json:"paymentId"`
	PaymentNumber     string     `json:"paymentNumber"`
	FolioID           *uuid.UUID `json:"folioId"`
	Amount            string     `json:"amount"`
	Currency          string     `json:"currency"`
	Destination       string     `json:"destination" enum:"original_method,member_account"`
	Status            string     `json:"status" enum:"pending,completed,rejected,cancelled"`
	Reason            string     `json:"reason"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId"`
	ProcessedAt       *time.Time `json:"processedAt"`
	CreatedAt         time.Time  `json:"createdAt"`
}

const refundCols = `r.id, r.number, r.payment_id, p.number, r.folio_id, r.amount::text, r.currency, r.destination, r.status, r.reason,
	r.approval_request_id, r.processed_at, r.created_at FROM billing.refunds r JOIN billing.payments p ON p.id = r.payment_id`

func scanRefund(row pgx.Row) (Refund, error) {
	var r Refund
	err := row.Scan(&r.ID, &r.Number, &r.PaymentID, &r.PaymentNumber, &r.FolioID, &r.Amount, &r.Currency, &r.Destination, &r.Status, &r.Reason,
		&r.ApprovalRequestID, &r.ProcessedAt, &r.CreatedAt)
	return r, err
}

// GetRefund loads a refund.
func GetRefund(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (Refund, error) {
	r, err := scanRefund(q.QueryRow(ctx, `SELECT `+refundCols+` WHERE r.id = $1`, rid))
	if dbtx.IsNoRows(err) {
		return r, errs.NotFound("refund")
	}
	return r, err
}

// Approver submits approval requests (approval engine).
type Approver interface {
	SubmitRefund(ctx context.Context, tx pgx.Tx, refundID uuid.UUID, property uuid.UUID, ref, title string, amount decimal.Decimal) (uuid.UUID, string, error)
}

// RequestRefund creates a refund: within the Refund Policy limit it
// completes immediately, above it waits for approval.
func (s *Service) RequestRefund(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID, amount decimal.Decimal, destination, reason string, approver Approver) (Refund, error) {
	if strings.TrimSpace(reason) == "" {
		return Refund{}, errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason is required"))
	}
	if !amount.IsPositive() {
		return Refund{}, errs.Validation("invalid_amount", "amount must be positive", errs.Field("amount", "invalid", "positive decimal"))
	}
	var property uuid.UUID
	var status, pamount, refunded, cur, method string
	var folioID, accountID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id, status, amount::text, refunded_amount::text, currency, method_type, folio_id, account_id
		FROM billing.payments WHERE id = $1 FOR UPDATE`, paymentID).Scan(&property, &status, &pamount, &refunded, &cur, &method, &folioID, &accountID); err != nil {
		if dbtx.IsNoRows(err) {
			return Refund{}, errs.NotFound("payment")
		}
		return Refund{}, err
	}
	if status != "completed" && status != "refunded" {
		return Refund{}, errs.Conflict("payment_not_completed", "only completed payments can be refunded")
	}
	var pendingRefunds string
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM billing.refunds WHERE payment_id = $1 AND status = 'pending'`, paymentID).Scan(&pendingRefunds); err != nil {
		return Refund{}, err
	}
	if amount.GreaterThan(dec(pamount).Sub(dec(refunded)).Sub(dec(pendingRefunds))) {
		return Refund{}, errs.Validation("refund_too_large", "refund exceeds the refundable amount", errs.Field("amount", "too_large", "at most the paid amount not yet refunded"))
	}
	if destination == "" {
		destination = "original_method"
	}
	if method == "member_account" {
		destination = "member_account"
	}
	pol, _, err := LoadRefundPolicy(ctx, tx, property)
	if err != nil {
		return Refund{}, err
	}
	num, err := number(ctx, tx, property, "REF")
	if err != nil {
		return Refund{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.refunds (id, property_id, number, payment_id, folio_id, amount, currency, destination, status, reason, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8,'pending',$9,$10,$10)`, rid, property, num, paymentID, folioID, amount.String(), cur, destination, reason, id.Ptr(actor(ctx))); err != nil {
		return Refund{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.refund", EntityID: rid.String(),
		EntityLabel: num, PropertyID: &property, Reason: reason, After: map[string]any{"amount": amount.String(), "destination": destination, "paymentId": paymentID}}); err != nil {
		return Refund{}, err
	}
	limit := dec(pol.AutoRefundLimit)
	if approver != nil && !limit.IsZero() && amount.GreaterThan(limit) {
		reqID, st, err := approver.SubmitRefund(ctx, tx, rid, property, num, "Refund "+num, amount)
		if err != nil {
			return Refund{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE billing.refunds SET approval_request_id = $2 WHERE id = $1`, rid, reqID); err != nil {
			return Refund{}, err
		}
		if st != "approved" {
			return GetRefund(ctx, tx, rid)
		}
	}
	if err := s.CompleteRefund(ctx, tx, rid); err != nil {
		return Refund{}, err
	}
	return GetRefund(ctx, tx, rid)
}

// Refunder is implemented by gateways that refund through their API.
type Refunder interface {
	RefundPayment(ctx context.Context, externalID, amount, reason string) (string, error)
}

// CompleteRefund executes a pending refund (also called by the approval hook).
func (s *Service) CompleteRefund(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	r, err := GetRefund(ctx, tx, rid)
	if err != nil {
		return err
	}
	if r.Status != "pending" {
		return nil
	}
	var property uuid.UUID
	var accountID *uuid.UUID
	var integrationCode, externalID *string
	var pamount, refunded string
	if err := tx.QueryRow(ctx, `SELECT property_id, account_id, integration_code, external_id, amount::text, refunded_amount::text FROM billing.payments WHERE id = $1 FOR UPDATE`,
		r.PaymentID).Scan(&property, &accountID, &integrationCode, &externalID, &pamount, &refunded); err != nil {
		return err
	}
	amt := dec(r.Amount)
	var ext *string
	switch r.Destination {
	case "member_account":
		target := accountID
		if target == nil && r.FolioID != nil {
			var cust *uuid.UUID
			_ = tx.QueryRow(ctx, `SELECT customer_id FROM billing.folios WHERE id = $1`, *r.FolioID).Scan(&cust)
			if cust != nil {
				if a, err := AccountFor(ctx, tx, property, *cust, "member"); err == nil && a != nil {
					target = &a.ID
				}
			}
		}
		if target == nil {
			return errs.Conflict("no_member_account", "the customer has no member account to refund to")
		}
		if err := PostEntry(ctx, tx, property, *target, "refund", amt.Neg(), r.Currency, "Refund "+r.Number, r.FolioID, &r.PaymentID); err != nil {
			return err
		}
	default:
		if integrationCode != nil && externalID != nil && s.Gateways != nil {
			if a, err := s.Gateways.ByCode(ctx, *integrationCode); err == nil {
				if rf, ok := a.(Refunder); ok {
					x, err := rf.RefundPayment(ctx, *externalID, r.Amount, r.Reason)
					if err != nil {
						return errs.Unavailable("payment gateway refund failed: " + err.Error())
					}
					ext = &x
				}
			}
		}
	}
	newRefunded := dec(refunded).Add(amt)
	pstatus := "completed"
	if newRefunded.GreaterThanOrEqual(dec(pamount)) {
		pstatus = "refunded"
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.payments SET refunded_amount = $2::numeric, status = $3 WHERE id = $1`, r.PaymentID, newRefunded.String(), pstatus); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.refunds SET status = 'completed', processed_at = now(), processed_by = $2, external_id = $3 WHERE id = $1`,
		rid, id.Ptr(actor(ctx)), ext); err != nil {
		return err
	}
	if r.FolioID != nil {
		if err := bumpVersion(ctx, tx, *r.FolioID); err != nil {
			return err
		}
	}
	if _, err := s.Events.Publish(ctx, tx, EventRefundProcessed, "billing.refund", &rid, &property, map[string]any{"refundId": rid, "number": r.Number,
		"paymentId": r.PaymentID, "folioId": r.FolioID, "amount": r.Amount, "destination": r.Destination}); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "refund_processed", EntityType: "billing.refund", EntityID: rid.String(),
		EntityLabel: r.Number, PropertyID: &property, After: map[string]any{"status": "completed", "amount": r.Amount}})
}

// RejectRefund closes a refund that was not approved.
func (s *Service) RejectRefund(ctx context.Context, tx pgx.Tx, rid uuid.UUID, status, reason string) error {
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `UPDATE billing.refunds SET status = $2, updated_by = $3 WHERE id = $1 AND status = 'pending' RETURNING property_id`,
		rid, status, id.Ptr(actor(ctx))).Scan(&property); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionStatusChange, EntityType: "billing.refund", EntityID: rid.String(),
		PropertyID: &property, Reason: reason, After: map[string]any{"status": status}})
}

// RefundFolio refunds the paid amount above the folio charges (cancelled
// booking): completed payments are refunded newest first.
func (s *Service) RefundFolio(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, reason string, approver Approver) ([]Refund, error) {
	sum, err := FolioSummary(ctx, tx, folioID)
	if err != nil {
		return nil, err
	}
	excess := dec(sum.Payments).Sub(dec(sum.Charges))
	var out []Refund
	if !excess.IsPositive() {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, (amount - refunded_amount)::text FROM billing.payments WHERE folio_id = $1 AND status = 'completed'
		AND purpose = 'settlement' ORDER BY created_at DESC`, folioID)
	if err != nil {
		return nil, err
	}
	type pr struct {
		id  uuid.UUID
		amt decimal.Decimal
	}
	var list []pr
	for rows.Next() {
		var x pr
		var a string
		if err := rows.Scan(&x.id, &a); err != nil {
			rows.Close()
			return nil, err
		}
		x.amt = dec(a)
		list = append(list, x)
	}
	rows.Close()
	for _, p := range list {
		if !excess.IsPositive() {
			break
		}
		amt := decimal.Min(excess, p.amt)
		if !amt.IsPositive() {
			continue
		}
		r, err := s.RequestRefund(ctx, tx, p.id, amt, "", reason, approver)
		if err != nil {
			return out, err
		}
		out = append(out, r)
		excess = excess.Sub(amt)
	}
	return out, nil
}

// CloseFolio applies held deposits and closes a fully settled folio
// (FR-PAY-09).
func (s *Service) CloseFolio(ctx context.Context, tx pgx.Tx, folioID uuid.UUID) error {
	property, status, _, err := lockFolio(ctx, tx, folioID)
	if err != nil {
		return err
	}
	if status != "open" {
		return errs.Conflict("folio_not_open", "the folio is not open")
	}
	if err := s.applyDeposits(ctx, tx, folioID); err != nil {
		return err
	}
	sum, err := FolioSummary(ctx, tx, folioID)
	if err != nil {
		return err
	}
	if dec(sum.Balance).IsPositive() {
		return errs.Conflict("folio_unpaid", "the folio still has an outstanding balance of "+sum.Balance)
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing.payments WHERE folio_id = $1 AND status = 'pending'`, folioID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errs.Conflict("folio_pending_payment", "a payment is still pending")
	}
	var num string
	if err := tx.QueryRow(ctx, `UPDATE billing.folios SET status = 'closed', closed_at = now(), closed_by = $2, version = version + 1 WHERE id = $1 RETURNING number`,
		folioID, id.Ptr(actor(ctx))).Scan(&num); err != nil {
		return err
	}
	if _, err := s.Events.Publish(ctx, tx, EventFolioClosed, "billing.folio", &folioID, &property, map[string]any{"folioId": folioID, "number": num,
		"charges": sum.Charges, "payments": sum.Payments}); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "close", EntityType: "billing.folio", EntityID: folioID.String(),
		EntityLabel: num, PropertyID: &property, After: sum})
}

func (s *Service) applyDeposits(ctx context.Context, tx pgx.Tx, folioID uuid.UUID) error {
	sum, err := FolioSummary(ctx, tx, folioID)
	if err != nil {
		return err
	}
	bal := dec(sum.Balance)
	rows, err := tx.Query(ctx, `SELECT id, (amount - applied_amount)::text FROM billing.deposits WHERE folio_id = $1 AND status = 'held' ORDER BY created_at FOR UPDATE`, folioID)
	if err != nil {
		return err
	}
	type d struct {
		id  uuid.UUID
		amt decimal.Decimal
	}
	var list []d
	for rows.Next() {
		var x d
		var a string
		if err := rows.Scan(&x.id, &a); err != nil {
			rows.Close()
			return err
		}
		x.amt = dec(a)
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		use := decimal.Min(x.amt, decimal.Max(bal, decimal.Zero))
		if _, err := tx.Exec(ctx, `UPDATE billing.deposits SET applied_amount = applied_amount + $2::numeric, status = 'applied', applied_at = now() WHERE id = $1`,
			x.id, use.String()); err != nil {
			return err
		}
		bal = bal.Sub(use)
	}
	return nil
}

// ApplyDeposits applies held deposits to the folio balance.
func (s *Service) ApplyDeposits(ctx context.Context, tx pgx.Tx, folioID uuid.UUID) error {
	return s.applyDeposits(ctx, tx, folioID)
}

// PaidTowards returns completed settlement payments plus held deposits.
func PaidTowards(ctx context.Context, q dbtx.Querier, folioID uuid.UUID) (decimal.Decimal, decimal.Decimal, error) {
	sum, err := FolioSummary(ctx, q, folioID)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	return dec(sum.Payments), dec(sum.HeldDeposits), nil
}

// PendingOnline returns the latest pending online payment of a folio.
func PendingOnline(ctx context.Context, q dbtx.Querier, folioID uuid.UUID) (*Payment, error) {
	p, err := scanPayment(q.QueryRow(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE p.folio_id = $1 AND p.status = 'pending' AND p.channel = 'online'
		ORDER BY p.created_at DESC LIMIT 1`, folioID))
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PaymentsOf lists the payments of a folio.
func PaymentsOf(ctx context.Context, q dbtx.Querier, folioID uuid.UUID) ([]Payment, error) {
	rows, err := q.Query(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE p.folio_id = $1 ORDER BY p.created_at`, folioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LinesOf returns the active (not voided) charge lines of a folio by type.
type LineRef struct {
	ID            uuid.UUID
	ChargeType    string
	Total         decimal.Decimal
	ReferenceType string
	ReferenceID   *uuid.UUID
	Components    []byte
}

func LinesOf(ctx context.Context, q dbtx.Querier, folioID uuid.UUID) ([]LineRef, error) {
	rows, err := q.Query(ctx, `SELECT id, charge_type, total::text, coalesce(reference_type, ''), reference_id, components FROM billing.folio_lines
		WHERE folio_id = $1 AND voided_at IS NULL ORDER BY posted_at`, folioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LineRef
	for rows.Next() {
		var l LineRef
		var t string
		if err := rows.Scan(&l.ID, &l.ChargeType, &t, &l.ReferenceType, &l.ReferenceID, &l.Components); err != nil {
			return nil, err
		}
		l.Total = dec(t)
		out = append(out, l)
	}
	return out, rows.Err()
}

// FolioStatus returns the status of a folio.
func FolioStatus(ctx context.Context, q dbtx.Querier, folioID uuid.UUID) (string, error) {
	var st string
	err := q.QueryRow(ctx, `SELECT status FROM billing.folios WHERE id = $1`, folioID).Scan(&st)
	return st, err
}

// PendingRefunds counts refunds of a folio waiting for approval.
func PendingRefunds(ctx context.Context, q dbtx.Querier, folioID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM billing.refunds WHERE folio_id = $1 AND status = 'pending'`, folioID).Scan(&n)
	return n, err
}
