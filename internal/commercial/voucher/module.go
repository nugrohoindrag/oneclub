// Package voucher is Voucher & Prepaid of PRD P2 (EP-19), the P2-owned
// commercial/voucher sub-package (PRD P2 §5.4.1): voucher types, sale and
// issue, redemption as a billing tender (contract C3), quota, transfer,
// extension, expiry and breakage on the deferred revenue sub-ledger.
package voucher

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

// Module is the voucher service.
type Module struct {
	DB        *dbtx.DB
	Billing   *billing.Service
	Events    *outbox.Bus
	Approvals *approval.Engine
	Notify    notify.Sender
}

// VoucherPolicy is "Voucher Policies" (FR-POL-P2-06, proposed label).
type VoucherPolicy struct {
	MaxExtensionMonths int   `json:"maxExtensionMonths" doc:"Longest expiry extension per request"`
	ReminderDays       []int `json:"reminderDays"`
	TransferAllowed    bool  `json:"transferAllowed"`
}

var defaultVoucherPolicy = VoucherPolicy{MaxExtensionMonths: 6, ReminderDays: []int{30, 7}, TransferAllowed: true}

func (m *Module) voucherPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (VoucherPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "commercial.voucher", property, defaultVoucherPolicy)
	return p, err
}

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "commercial.voucher", Category: "Voucher Policies", Name: "Voucher extension & transfer",
		Description: "Maximum expiry extension, reminder days before expiry, transfer between customers", Default: defaultVoucherPolicy})
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// ActiveVouchers lists the active vouchers and prepaid balances of a
// customer (Customer 360).
func ActiveVouchers(ctx context.Context, q dbtx.Querier, customer uuid.UUID) ([]Voucher, error) {
	return handle.List[Voucher](q.Query(ctx, voucherSelect+` WHERE v.customer_id = $1 AND v.status IN ('active', 'partially_redeemed')
		ORDER BY v.expires_at NULLS LAST`, customer))
}

// Register adds the voucher routes (wired by internal/app).
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	m.registerVouchers(reg, eng)
	m.registerMe(reg)
}

// Types of the public commercial interface (internal/commercial/sales_api.go).
type (
	Voucher       = commercial.Voucher
	RedeemRequest = commercial.RedeemRequest
	RedeemResult  = commercial.RedeemResult
)

// Quota is the remaining quota of a customer (method form for the commercial.Vouchers interface).
func (m *Module) Quota(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, category, item string) (decimal.Decimal, error) {
	return Quota(ctx, q, property, customerID, category, item)
}

var (
	_ commercial.Vouchers = (*Module)(nil)
)
