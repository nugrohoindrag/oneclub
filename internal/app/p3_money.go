package app

// PRD P3 vouchers and refunds CRM asks for (CRM sits below Commercial and
// Billing, so the composition root plugs them in): loyalty voucher rewards
// (FR-LOY-05), campaign vouchers per recipient (FR-CMP-05) and voucher /
// refund compensations of complaint tickets (FR-TKT-05).

import (
	"context"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/commercial/voucher"
	"oneclub/internal/crm/loyalty"
)

// rewardVouchers issues the Commercial vouchers of a loyalty voucher reward
// (once per redemption).
func (a *App) rewardVouchers(ctx context.Context, tx pgx.Tx, in loyalty.RewardVoucher) ([]string, error) {
	vs, _, err := a.Vouchers.IssueOnce(ctx, tx, voucher.IssueOnceRequest{PropertyID: in.PropertyID, TypeCode: in.TypeCode, CustomerID: in.CustomerID,
		Quantity: in.Quantity, SourceType: "crm.reward_redemption", SourceID: in.RedemptionID, Notes: "Loyalty reward " + in.RewardName + " · " + in.Number})
	codes := make([]string, 0, len(vs))
	for _, v := range vs {
		codes = append(codes, v.Code)
	}
	return codes, err
}

// subscribeP3Money registers the voucher and refund subscribers of CRM
// campaigns and complaint compensations.
func (a *App) subscribeP3Money() {
	a.Bus.Subscribe("crm.campaign_sent", "commercial.campaign_vouchers", a.Vouchers.OnCampaignSent)
	a.Bus.Subscribe(billing.EventCompensationApproved, "commercial.compensation_voucher", a.Vouchers.OnCompensationApproved)
	a.Bus.Subscribe(billing.EventCompensationApproved, "billing.compensation_refund", a.Billing.OnCompensationApproved)
}
