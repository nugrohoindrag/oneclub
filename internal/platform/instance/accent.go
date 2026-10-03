package instance

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Presets are the Morphic accents (data-accent). Their token values live in
// packages/ui tokens/themes.css; the API only stores the preset name.
var Presets = []string{"lime", "blue", "violet", "orange", "rose"}

var hexRe = regexp.MustCompile(`^#?([0-9a-fA-F]{6})$`)

type rgb struct{ r, g, b float64 }

func parseHex(s string) (rgb, error) {
	m := hexRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return rgb{}, fmt.Errorf("color must be a #RRGGBB hex value")
	}
	v, _ := strconv.ParseUint(m[1], 16, 32)
	return rgb{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255}, nil
}

func (c rgb) hex() string {
	cl := func(x float64) int { return int(math.Round(math.Max(0, math.Min(1, x)) * 255)) }
	return fmt.Sprintf("#%02X%02X%02X", cl(c.r), cl(c.g), cl(c.b))
}

func channel(x float64) float64 {
	if x <= 0.03928 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

// luminance is the WCAG relative luminance.
func (c rgb) luminance() float64 {
	return 0.2126*channel(c.r) + 0.7152*channel(c.g) + 0.0722*channel(c.b)
}

// Contrast returns the WCAG 2.1 contrast ratio between two colors.
func Contrast(a, b string) (float64, error) {
	ca, err := parseHex(a)
	if err != nil {
		return 0, err
	}
	cb, err := parseHex(b)
	if err != nil {
		return 0, err
	}
	l1, l2 := ca.luminance(), cb.luminance()
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05), nil
}

// HSL helpers.
func (c rgb) hsl() (h, s, l float64) {
	mx := math.Max(c.r, math.Max(c.g, c.b))
	mn := math.Min(c.r, math.Min(c.g, c.b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case c.r:
		h = (c.g - c.b) / d
		if c.g < c.b {
			h += 6
		}
	case c.g:
		h = (c.b-c.r)/d + 2
	default:
		h = (c.r-c.g)/d + 4
	}
	return h / 6, s, l
}

func fromHSL(h, s, l float64) rgb {
	if s == 0 {
		return rgb{l, l, l}
	}
	hue := func(p, q, t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		switch {
		case t < 1.0/6:
			return p + (q-p)*6*t
		case t < 0.5:
			return q
		case t < 2.0/3:
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	return rgb{hue(p, q, h+1.0/3), hue(p, q, h), hue(p, q, h-1.0/3)}
}

func withL(c rgb, l float64) rgb {
	h, s, _ := c.hsl()
	return fromHSL(h, s, l)
}

// AccentTokens is a generated custom accent for both themes.
type AccentTokens struct {
	Light  map[string]string `json:"light"`
	Dark   map[string]string `json:"dark"`
	Checks []ContrastCheck   `json:"checks"`
}

// ContrastCheck documents one WCAG AA verification.
type ContrastCheck struct {
	Pair  string  `json:"pair"`
	Ratio float64 `json:"ratio"`
	Pass  bool    `json:"pass"`
}

// minAA is the WCAG 2.1 AA contrast for normal text.
const minAA = 4.5

// GenerateAccent derives the Morphic accent token set (same token names as a
// preset in themes.css) from one brand colour. The primary is darkened (or
// lightened in dark mode) until on-primary text reaches WCAG AA; generation
// fails if AA cannot be reached (FR-BRD-03).
func GenerateAccent(brand string) (AccentTokens, error) {
	base, err := parseHex(brand)
	if err != nil {
		return AccentTokens{}, err
	}
	_, _, l := base.hsl()
	out := AccentTokens{}

	// Light theme: white text on primary.
	primary := base
	for i := 0; i < 60; i++ {
		if c, _ := Contrast(primary.hex(), "#FFFFFF"); c >= minAA {
			break
		}
		l -= 0.01
		primary = withL(base, l)
	}
	container := withL(base, 0.86)
	onContainer := withL(base, 0.14)
	soft := withL(base, 0.91)
	strong := withL(base, math.Max(0.05, l-0.08))
	out.Light = map[string]string{
		"--md-sys-color-primary":              primary.hex(),
		"--md-sys-color-primary-strong":       strong.hex(),
		"--md-sys-color-on-primary":           "#FFFFFF",
		"--md-sys-color-primary-container":    container.hex(),
		"--md-sys-color-on-primary-container": onContainer.hex(),
		"--md-sys-color-primary-soft":         soft.hex(),
		"--md-sys-color-chart-primary":        primary.hex(),
		"--md-sys-color-chart-secondary":      withL(base, 0.62).hex(),
		"--md-sys-color-inverse-primary":      withL(base, 0.72).hex(),
		"--hero-banner-bg":                    "linear-gradient(135deg, " + soft.hex() + " 0%, " + container.hex() + " 45%, " + withL(base, 0.74).hex() + " 100%)",
		"--hero-banner-text":                  onContainer.hex(),
		"--hero-banner-subtext":               withL(base, 0.22).hex(),
		"--hero-banner-pill-bg":               withL(base, 0.22).hex(),
		"--hero-banner-pill-text":             withL(base, 0.95).hex(),
		"--hero-banner-pill-icon":             container.hex(),
	}

	// Dark theme: dark text on a light primary.
	_, _, ld := base.hsl()
	dprim := withL(base, math.Max(ld, 0.6))
	for i := 0; i < 60; i++ {
		if c, _ := Contrast(dprim.hex(), "#10140C"); c >= minAA {
			break
		}
		ld = math.Max(ld, 0.6) + 0.01*float64(i+1)
		dprim = withL(base, math.Min(ld, 0.95))
	}
	dcont := withL(base, 0.24)
	donc := withL(base, 0.88)
	out.Dark = map[string]string{
		"--md-sys-color-primary":              dprim.hex(),
		"--md-sys-color-primary-strong":       withL(base, 0.7).hex(),
		"--md-sys-color-on-primary":           "#10140C",
		"--md-sys-color-primary-container":    dcont.hex(),
		"--md-sys-color-on-primary-container": donc.hex(),
		"--md-sys-color-primary-soft":         withL(base, 0.2).hex(),
		"--md-sys-color-chart-primary":        dprim.hex(),
		"--md-sys-color-chart-secondary":      withL(base, 0.5).hex(),
		"--md-sys-color-inverse-primary":      primary.hex(),
	}

	pairs := []struct{ name, a, b string }{
		{"light on-primary / primary", "#FFFFFF", out.Light["--md-sys-color-primary"]},
		{"light on-primary-container / primary-container", out.Light["--md-sys-color-on-primary-container"], out.Light["--md-sys-color-primary-container"]},
		{"dark on-primary / primary", "#10140C", out.Dark["--md-sys-color-primary"]},
		{"dark on-primary-container / primary-container", out.Dark["--md-sys-color-on-primary-container"], out.Dark["--md-sys-color-primary-container"]},
	}
	var failed []string
	for _, p := range pairs {
		c, _ := Contrast(p.a, p.b)
		pass := c >= minAA
		out.Checks = append(out.Checks, ContrastCheck{Pair: p.name, Ratio: math.Round(c*100) / 100, Pass: pass})
		if !pass {
			failed = append(failed, p.name)
		}
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("brand color cannot meet WCAG AA contrast for: %s", strings.Join(failed, ", "))
	}
	return out, nil
}
