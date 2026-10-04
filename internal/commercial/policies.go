package commercial

import (
	"context"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

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
	rules.RegisterPolicy(rules.PolicyDef{Code: "pos.policy", Category: "POS Policies", Name: "Discount, offline member charge & pre-order",
		Description: "Discount limit without supervisor, member charge while offline, pre-order lead time, default kitchen station", Default: defaultPOSPolicy})
}
