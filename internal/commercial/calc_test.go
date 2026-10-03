package commercial

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func rule(code, kind, rate, basis, mode string, seq int) Rule {
	return Rule{Code: code, Name: code, Kind: kind, Rate: decimal.RequireFromString(rate), Basis: basis, PricingMode: mode, Sequence: seq}
}

func TestPlusPlus(t *testing.T) {
	b := Calculate([]Rule{rule("SVC", "service", "10", "net_amount", "plus_plus", 1), rule("PB1", "tax", "10", "net_plus_service", "plus_plus", 2)},
		decimal.NewFromInt(100000), "IDR", time.Now())
	if b.NetAmount != "100000" || b.Total != "121000" || b.Lines[0].Amount != "10000" || b.Lines[1].Amount != "11000" {
		t.Fatalf("%+v", b)
	}
}

func TestNettAbsorbsRounding(t *testing.T) {
	// 121,000 nett with 10% service + 10% tax on (net + service) = 100,000 net.
	b := Calculate([]Rule{rule("SVC", "service", "10", "net_amount", "nett", 1), rule("PB1", "tax", "10", "net_plus_service", "nett", 2)},
		decimal.NewFromInt(121000), "IDR", time.Now())
	if b.NetAmount != "100000" || b.Total != "121000" {
		t.Fatalf("%+v", b)
	}
	// An amount that does not divide evenly still sums exactly to the input.
	b = Calculate([]Rule{rule("SVC", "service", "10", "net_amount", "nett", 1), rule("PB1", "tax", "11", "net_plus_service", "nett", 2)},
		decimal.NewFromInt(99999), "IDR", time.Now())
	sum := decimal.RequireFromString(b.NetAmount)
	for _, l := range b.Lines {
		sum = sum.Add(decimal.RequireFromString(l.Amount))
	}
	if !sum.Equal(decimal.NewFromInt(99999)) || b.Total != "99999" {
		t.Fatalf("components %s must equal total %s", sum, b.Total)
	}
}

func TestNoRules(t *testing.T) {
	b := Calculate(nil, decimal.NewFromInt(50000), "IDR", time.Now())
	if b.Total != "50000" || len(b.Lines) != 0 {
		t.Fatalf("%+v", b)
	}
}
