package instance

import "testing"

func TestContrastKnownValues(t *testing.T) {
	c, err := Contrast("#000000", "#FFFFFF")
	if err != nil || c < 20.9 || c > 21.1 {
		t.Fatalf("black/white contrast = %v, %v", c, err)
	}
}

func TestGenerateAccentMeetsAA(t *testing.T) {
	// Light brand colours (yellow, lime) must be darkened to pass.
	for _, brand := range []string{"#0B6E4F", "#F2C230", "#9CEC5D", "#2F73B8", "#C2185B", "#777777"} {
		tok, err := GenerateAccent(brand)
		if err != nil {
			t.Fatalf("%s: %v", brand, err)
		}
		for _, c := range tok.Checks {
			if !c.Pass || c.Ratio < 4.5 {
				t.Fatalf("%s: %s ratio %.2f", brand, c.Pair, c.Ratio)
			}
		}
	}
	if _, err := GenerateAccent("not-a-color"); err == nil {
		t.Fatal("expected error for invalid colour")
	}
}
