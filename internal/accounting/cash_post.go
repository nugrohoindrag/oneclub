package accounting

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/platform/handle"
)

type settlementRow struct {
	ID              uuid.UUID `db:"id"`
	BusinessDate    time.Time `db:"business_date"`
	IntegrationCode string    `db:"integration_code"`
	SettlementTotal string    `db:"settlement_total"`
	OneclubTotal    string    `db:"oneclub_total"`
}

type shiftRow struct {
	ID       uuid.UUID `db:"id"`
	Number   string    `db:"number"`
	Kind     string    `db:"kind"`
	Day      time.Time `db:"day"`
	Variance string    `db:"variance"`
}

// sweepCash posts the cash side of the day: payment gateway settlements to
// the bank (Dr bank, Dr gateway fees / Cr gateway clearing, P1 payment
// reconciliation → FR-BNK-03) and the counted-cash variance of closed
// cashier and POS shifts (cash over / short, FR-BNK-04).
func (m *Module) sweepCash(ctx context.Context, tx pgx.Tx, property uuid.UUID, h sweepHeader, cut, upTo time.Time) (*AccountingJournal, []Missing, error) {
	setts, err := handle.List[settlementRow](tx.Query(ctx, `SELECT g.id, g.business_date, g.integration_code, g.settlement_total::text AS settlement_total,
		g.oneclub_total::text AS oneclub_total FROM reporting.acc_gateway_settlements g WHERE g.property_id = $1 AND g.business_date BETWEEN $2 AND $3
		AND g.settlement_total <> 0
		AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.gateway_settlement' AND s.source_id = g.id) LIMIT 2000`,
		property, cut, upTo))
	if err != nil {
		return nil, nil, err
	}
	var sources []sourceMark
	for _, g := range setts {
		bd := g.BusinessDate
		st, oc := dec(g.SettlementTotal), dec(g.OneclubTotal)
		attrs := map[string]string{"integration": g.IntegrationCode}
		desc := "Gateway settlement " + g.IntegrationCode + " " + ymd(bd)
		items := []Item{{Source: "billing.gateway_settlement", Attrs: attrs, Amount: st, Description: desc, SourceType: "billing.gateway_settlement",
			SourceID: g.ID.String(), FallbackDebit: "bank", FallbackCredit: "gateway_clearing"}}
		if fee := oc.Sub(st); fee.IsPositive() {
			items = append(items, Item{Source: "billing.gateway_fee", Attrs: attrs, Amount: fee, Description: desc + " fee", SourceType: "billing.gateway_settlement",
				SourceID: g.ID.String(), FallbackDebit: "bank_charges", FallbackCredit: "gateway_clearing"})
		}
		sources = append(sources, sourceMark{Type: "billing.gateway_settlement", ID: g.ID, BusinessDate: &bd, Key1: g.IntegrationCode, Amount: st, Items: items})
	}
	shifts, err := handle.List[shiftRow](tx.Query(ctx, `SELECT * FROM (
		SELECT s.id, s.number, 'cashier_shift' AS kind, s.business_date AS day, s.variance::text AS variance FROM reporting.acc_cashier_shifts s
		WHERE s.property_id = $1 AND s.status = 'closed' AND s.variance IS NOT NULL AND s.variance <> 0 AND s.business_date BETWEEN $2 AND $3
		UNION ALL
		SELECT s.id, s.shift_no, 'pos_shift', s.closed_date, s.variance::text FROM reporting.acc_pos_shifts s
		WHERE s.property_id = $1 AND s.status = 'closed' AND s.variance IS NOT NULL AND s.variance <> 0 AND s.closed_date BETWEEN $2 AND $3) x
		WHERE NOT EXISTS (SELECT 1 FROM accounting.posted_sources p WHERE p.source_type = 'billing.shift_variance' AND p.source_id = x.id) LIMIT 2000`,
		property, cut, upTo))
	if err != nil {
		return nil, nil, err
	}
	for _, s := range shifts {
		bd := s.Day
		v := dec(s.Variance)
		sources = append(sources, sourceMark{Type: "billing.shift_variance", ID: s.ID, BusinessDate: &bd, Key1: s.Kind, Key2: s.Number, Amount: v,
			Items: []Item{{Source: "billing.shift_variance", Attrs: map[string]string{"shiftType": s.Kind}, Amount: v,
				Description: "Cash over / short " + s.Number, SourceType: "billing.shift_variance", SourceID: s.ID.String(), FallbackDebit: "cash",
				FallbackCredit: "cash_over_short"}}})
	}
	if len(sources) == 0 {
		return nil, nil, nil
	}
	return m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: h.Date, SourceType: h.SourceType, SourceID: h.SourceID,
		SourceRef: h.SourceRef, EventID: h.EventID, Description: "Cash & settlements · " + h.Description}, Sources: sources})
}
