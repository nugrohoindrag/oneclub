package pos

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// Tier F&B discount of a line with exact amounts (IDR, 0 decimals).
func TestTierLineDiscount(t *testing.T) {
	for _, c := range []struct {
		name                  string
		base, promo, pct, cap string
		stack                 bool
		want                  string
	}{
		{"Gold 5% of Rp150.000", "150000", "0", "5", "100", false, "7500"},
		{"Platinum 10% of Rp87.500", "87500", "0", "10", "100", false, "8750"},
		{"rounded to the rupiah", "33333", "0", "5", "100", false, "1667"},
		{"no stacking: promotion better", "150000", "15000", "5", "100", false, "0"},
		{"no stacking: tier tops up the promotion", "150000", "5000", "5", "100", false, "2500"},
		{"stacked on the rest after the promotion", "150000", "15000", "5", "100", true, "6750"},
		{"stacked within the 15% ceiling", "150000", "15000", "10", "15", true, "7500"},
		{"ceiling used up by the promotion", "150000", "22500", "5", "15", true, "0"},
		{"no tier discount", "150000", "0", "0", "100", false, "0"},
		{"free line", "0", "0", "5", "100", true, "0"},
	} {
		got := TierLineDiscount(dd(c.base), dd(c.promo), dd(c.pct), dd(c.cap), c.stack, 0)
		if !got.Equal(dd(c.want)) {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	if got := TierLineDiscount(dd("10.10"), dd("0"), dd("5"), dd("100"), false, 2); !got.Equal(dd("0.51")) {
		t.Errorf("2 decimals: %s", got)
	}
	if l := TierDiscountLabel("{tier} member {percent}%", "Gold", dd("5.00")); l != "Gold member 5%" {
		t.Errorf("label %q", l)
	}
}
