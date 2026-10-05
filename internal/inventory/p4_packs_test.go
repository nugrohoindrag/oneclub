package inventory

import (
	"testing"

	"github.com/shopspring/decimal"
)

// PRD P4 EP-06 AC: 100 bottles with 24 per carton are 4 cartons + 4 bottles.
func TestPacks(t *testing.T) {
	f, ctn := "24", "CTN"
	for _, c := range []struct {
		qty  int64
		want string
	}{{100, "4 CTN + 4 BTL"}, {96, "4 CTN"}, {5, "5 BTL"}, {-100, "-4 CTN + 4 BTL"}, {0, "0 BTL"}} {
		if got := Packs(decimal.NewFromInt(c.qty), &f, &ctn, "BTL"); got != c.want {
			t.Errorf("Packs(%d) = %q, want %q", c.qty, got, c.want)
		}
	}
	if got := Packs(decimal.NewFromInt(3), nil, nil, "KG"); got != "3 KG" {
		t.Errorf("without purchase UOM: %q", got)
	}
}
