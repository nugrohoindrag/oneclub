package accounting

// Account balances (debit − credit) used by postings, reconciliations and
// reports. Properties nil = every property the caller sees (RLS), which is
// the consolidated view of MAIN + MDR (FR-FIN-03).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// accountIDByCode returns the id of an account code.
func accountIDByCode(ctx context.Context, q dbtx.Querier, code string) (uuid.UUID, error) {
	var aid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM accounting.accounts WHERE code = $1`, code).Scan(&aid)
	return aid, err
}

// accountBalance is the balance (debit − credit) of an account at a
// property up to asOf (inclusive; nil = all).
func accountBalance(ctx context.Context, q dbtx.Querier, property *uuid.UUID, accountID uuid.UUID, asOf *time.Time) (decimal.Decimal, error) {
	var s *string
	var at any
	if asOf != nil {
		at = dateOnly(*asOf)
	}
	var prop any
	if property != nil {
		prop = *property
	}
	err := q.QueryRow(ctx, `SELECT sum(debit - credit)::text FROM accounting.journal_lines WHERE account_id = $1 AND ($2::uuid IS NULL OR property_id = $2)
		AND ($3::date IS NULL OR journal_date <= $3)`, accountID, prop, at).Scan(&s)
	return decp(s), err
}

// roleBalance is the balance of the default account of a role.
func roleBalance(ctx context.Context, q dbtx.Querier, property uuid.UUID, cfg AccountingConfiguration, role string, asOf *time.Time) (decimal.Decimal, error) {
	code := cfg.Accounts[role]
	if code == "" {
		return decimal.Zero, nil
	}
	aid, err := accountIDByCode(ctx, q, code)
	if dbtx.IsNoRows(err) {
		return decimal.Zero, nil
	}
	if err != nil {
		return decimal.Zero, err
	}
	return accountBalance(ctx, q, &property, aid, asOf)
}
