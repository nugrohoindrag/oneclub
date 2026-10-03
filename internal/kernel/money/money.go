// Package money represents monetary amounts. Never float: numeric(19,4) in the
// database and shopspring/decimal in Go (Technical Doc §5.3).
package money

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Money is an amount in a currency.
type Money struct {
	Amount   decimal.Decimal `json:"amount"`
	Currency string          `json:"currency"`
}

// New builds Money from a decimal string, e.g. New("150000.00", "IDR").
func New(amount, currency string) (Money, error) {
	d, err := decimal.NewFromString(amount)
	if err != nil {
		return Money{}, fmt.Errorf("money: invalid amount %q: %w", amount, err)
	}
	return Money{Amount: d, Currency: currency}, nil
}

// Round rounds to the currency's minor unit. IDR has no minor unit in practice.
func (m Money) Round() Money {
	places := int32(2)
	if m.Currency == "IDR" || m.Currency == "JPY" {
		places = 0
	}
	return Money{Amount: m.Amount.Round(places), Currency: m.Currency}
}

func (m Money) Add(o Money) Money {
	m.mustMatch(o)
	return Money{Amount: m.Amount.Add(o.Amount), Currency: m.Currency}
}

func (m Money) Sub(o Money) Money {
	m.mustMatch(o)
	return Money{Amount: m.Amount.Sub(o.Amount), Currency: m.Currency}
}

// MulPercent returns m * pct / 100.
func (m Money) MulPercent(pct decimal.Decimal) Money {
	return Money{Amount: m.Amount.Mul(pct).Div(decimal.NewFromInt(100)), Currency: m.Currency}
}

func (m Money) mustMatch(o Money) {
	if m.Currency != o.Currency {
		panic(fmt.Sprintf("money: currency mismatch %s vs %s", m.Currency, o.Currency))
	}
}

func (m Money) String() string { return m.Amount.StringFixed(4) + " " + m.Currency }
