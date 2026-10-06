package voucher

// PRD P3 vouchers issued for CRM (owner: commercial): one voucher per
// campaign recipient when the campaign carries a voucher type (FR-CMP-05),
// the voucher of a loyalty reward (FR-LOY-05, through internal/app) and
// the voucher of an approved complaint compensation (FR-TKT-05). Every
// issue is idempotent per source document and customer.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
)

// NotifyVoucherIssued tells a customer about a voucher issued for them.
const NotifyVoucherIssued = "voucher.issued"

// CompensationVoucherType is the value voucher type of complaint
// compensations; created on first use (value voucher, 12 months) so the
// club can rename it or change its validity.
const CompensationVoucherType = "COMPENSATION"

// IssueOnceRequest issues the vouchers of a source document for a customer
// unless they exist already.
type IssueOnceRequest struct {
	PropertyID uuid.UUID
	TypeCode   string
	CustomerID uuid.UUID
	Quantity   int              // vouchers to issue (default 1)
	Value      *decimal.Decimal // value of a value voucher (else the type face value)
	SourceType string
	SourceID   uuid.UUID
	Notes      string
	Reason     string // notification text ("" = no notification)
}

// IssueOnce issues the vouchers of a source document for a customer once:
// vouchers already issued for the same source and customer are returned.
func (m *Module) IssueOnce(ctx context.Context, tx pgx.Tx, r IssueOnceRequest) ([]Voucher, bool, error) {
	n := max(r.Quantity, 1)
	rows, err := tx.Query(ctx, `SELECT id FROM commercial.vouchers WHERE property_id = $1 AND source_type = $2 AND source_id = $3 AND customer_id = $4
		ORDER BY created_at, code`, r.PropertyID, r.SourceType, r.SourceID, r.CustomerID)
	if err != nil {
		return nil, false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, false, err
	}
	out := make([]Voucher, 0, n)
	for _, vid := range ids {
		v, err := m.Voucher(ctx, tx, vid)
		if err != nil {
			return nil, false, err
		}
		out = append(out, v)
	}
	if len(out) >= n {
		return out, false, nil
	}
	cust := r.CustomerID
	for len(out) < n {
		req := IssueRequest{PropertyID: r.PropertyID, TypeCode: strings.ToUpper(strings.TrimSpace(r.TypeCode)), CustomerID: &cust, Via: "issue",
			SourceType: r.SourceType, SourceID: &r.SourceID, Notes: r.Notes}
		if r.Value != nil {
			req.Original, req.Quantity = r.Value, r.Value
		}
		v, err := m.Issue(ctx, tx, req)
		if err != nil {
			return out, false, err
		}
		out = append(out, v)
		if r.Reason != "" && m.Notify != nil {
			exp := ""
			if v.ExpiresAt != nil {
				exp = v.ExpiresAt.In(calendar.Location(ctx, tx)).Format("2006-01-02")
			}
			msg := notify.Message{Event: NotifyVoucherIssued, Category: "voucher", PropertyID: &r.PropertyID,
				Data: map[string]any{"name": deref(v.CustomerName), "code": v.Code, "typeName": v.TypeName, "expiresAt": exp, "reason": r.Reason}}
			if ok, err := crm.Recipient(ctx, tx, cust, &msg); err != nil {
				return out, false, err
			} else if ok {
				if err := m.Notify.Send(ctx, tx, msg); err != nil {
					return out, false, err
				}
			}
		}
	}
	return out, true, nil
}

// campaignSent is the part of crm.campaign_sent the voucher issue uses
// (docs/p3-p4-contracts.md).
type campaignSent struct {
	CampaignID     uuid.UUID `json:"campaignId"`
	Code           string    `json:"code"`
	Name           string    `json:"name"`
	VoucherTypeRef *string   `json:"voucherTypeRef"`
	Recipients     []struct {
		RecipientID uuid.UUID `json:"recipientId"`
		CustomerID  uuid.UUID `json:"customerId"`
	} `json:"recipients"`
}

// OnCampaignSent issues one voucher of the campaign's voucher type to every
// recipient of the sent batch (FR-CMP-05), idempotent per campaign and
// recipient (crm.campaign_sent subscriber). An unknown or inactive voucher
// type is logged and skipped: the campaign message is already out.
func (m *Module) OnCampaignSent(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p campaignSent
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.VoucherTypeRef == nil || strings.TrimSpace(*p.VoucherTypeRef) == "" {
		return nil //nolint:nilerr // another shape or no voucher
	}
	property := *e.PropertyID
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	n := 0
	for _, r := range p.Recipients {
		if r.CustomerID == uuid.Nil {
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, created, err := m.IssueOnce(ctx, sp, IssueOnceRequest{PropertyID: property, TypeCode: *p.VoucherTypeRef, CustomerID: r.CustomerID,
			SourceType: "crm.campaign", SourceID: p.CampaignID, Notes: "Campaign " + p.Code, Reason: "Campaign " + p.Name})
		if err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				slog.WarnContext(ctx, "campaign voucher not issued", "campaign", p.Code, "customer", r.CustomerID, "reason", de.Message)
				continue
			}
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		if created {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "issue_campaign_vouchers", EntityType: "crm.campaign",
		EntityID: p.CampaignID.String(), EntityLabel: p.Code, PropertyID: &property,
		After: map[string]any{"voucherType": *p.VoucherTypeRef, "count": n}, Metadata: map[string]any{"event": e.ID}})
}

// compensationApproved is crm.ticket_compensation_approved
// (docs/p3-p4-contracts.md).
type compensationApproved struct {
	CompensationID uuid.UUID  `json:"compensationId"`
	Number         string     `json:"number"`
	TicketNumber   string     `json:"ticketNumber"`
	CustomerID     *uuid.UUID `json:"customerId"`
	Type           string     `json:"type"`
	Amount         *string    `json:"amount"`
	Description    string     `json:"description"`
}

// OnCompensationApproved issues the value voucher of an approved complaint
// compensation of type voucher (FR-TKT-05): a COMPENSATION value voucher
// (the type is created when missing) of the approved amount, once per
// compensation (crm.ticket_compensation_approved subscriber).
func (m *Module) OnCompensationApproved(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p compensationApproved
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.Type != "voucher" || p.CustomerID == nil || p.Amount == nil {
		return nil //nolint:nilerr // another shape or no voucher
	}
	amount, err := decimal.NewFromString(*p.Amount)
	if err != nil || !amount.IsPositive() {
		return nil //nolint:nilerr // nothing to issue
	}
	property := *e.PropertyID
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	typ := CompensationVoucherType
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.voucher_types (id, property_id, code, name, kind, category, unit, face_value, validity_months,
		revenue_component) VALUES ($1,$2,$3,'Complaint Compensation','value','gift','rupiah',0,12,'other') ON CONFLICT (property_id, code) DO NOTHING`,
		id.New(), property, typ); err != nil {
		return err
	}
	vs, created, err := m.IssueOnce(ctx, tx, IssueOnceRequest{PropertyID: property, TypeCode: typ, CustomerID: *p.CustomerID, Value: &amount,
		SourceType: "crm.ticket_compensation", SourceID: p.CompensationID, Notes: "Compensation " + p.Number + " · complaint " + p.TicketNumber,
		Reason: "Compensation for complaint " + p.TicketNumber})
	if err != nil || !created {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "issue_compensation_voucher", EntityType: "crm.ticket_compensation",
		EntityID: p.CompensationID.String(), EntityLabel: p.Number, PropertyID: &property,
		After: map[string]any{"voucher": vs[0].Code, "amount": amount.String()}, Metadata: map[string]any{"event": e.ID}})
}
