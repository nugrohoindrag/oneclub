package journey

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/errs"
)

func fieldsOf(err error) string {
	e, ok := errs.As(err)
	if !ok {
		return ""
	}
	out := ""
	for _, f := range e.Fields {
		out += " " + f.Field
	}
	return out
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestQuietUntil(t *testing.T) {
	jkt := time.FixedZone("WIB", 7*3600)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 5, h, m, 0, 0, jkt) }
	for _, c := range []struct {
		t     time.Time
		quiet bool
		until time.Time
	}{
		{at(20, 59), false, time.Time{}},
		{at(21, 0), true, time.Date(2026, 10, 6, 8, 0, 0, 0, jkt)},
		{at(23, 30), true, time.Date(2026, 10, 6, 8, 0, 0, 0, jkt)},
		{at(0, 15), true, time.Date(2026, 10, 5, 8, 0, 0, 0, jkt)},
		{at(7, 59), true, time.Date(2026, 10, 5, 8, 0, 0, 0, jkt)},
		{at(8, 0), false, time.Time{}},
		{at(13, 0), false, time.Time{}},
	} {
		until, quiet := QuietUntil(c.t.UTC(), jkt, "21:00", "08:00")
		if quiet != c.quiet || (quiet && !until.Equal(c.until)) {
			t.Errorf("%s: quiet %v until %s", c.t, quiet, until.In(jkt))
		}
	}
	// A daytime window (start < end) and a disabled window.
	if _, q := QuietUntil(at(12, 30).UTC(), jkt, "12:00", "13:00"); !q {
		t.Error("12:00–13:00 window")
	}
	if _, q := QuietUntil(at(23, 0).UTC(), jkt, "", ""); q {
		t.Error("no quiet hours")
	}
}

func TestCapped(t *testing.T) {
	pol := DefaultPolicy // 2 per week, 6 per month (PRD P5 §16 #15)
	for _, c := range []struct {
		w, m int
		want bool
	}{{0, 0, false}, {1, 5, false}, {2, 2, true}, {1, 6, true}, {0, 6, true}} {
		if got := Capped(c.w, c.m, pol); got != c.want {
			t.Errorf("%d/%d: %v", c.w, c.m, got)
		}
	}
	if Capped(10, 10, Policy{}) {
		t.Error("no cap configured")
	}
}

func TestStartKeyAnchored(t *testing.T) {
	in, err := TemplateInput("renewal", DefaultVoucherType)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(&in); err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		days int
		want string
	}{{60, "offer_h60"}, {45, "offer_h60"}, {30, "check_h30"}, {20, "check_h30"}, {7, "check_h7"}, {3, "check_h7"}, {0, "check_h0"}} {
		anchor := today.AddDate(0, 0, c.days)
		if got := StartKey(in.Steps, &anchor, today); got != c.want {
			t.Errorf("membership ends in %d days: start %s, want %s", c.days, got, c.want)
		}
	}
	if got := StartKey(in.Steps, nil, today); got != "offer_h60" {
		t.Errorf("without anchor: %s", got)
	}
}

func TestNextKeyAndVariant(t *testing.T) {
	steps := []JourneyStep{{Key: "a"}, {Key: "b", NextKey: sp("d")}, {Key: "c"}, {Key: "d"}}
	if nextKey(steps, 0) != "b" || nextKey(steps, 1) != "d" || nextKey(steps, 3) != TargetEnd {
		t.Error("next key")
	}
	e := uuid.New()
	if Variant(e, "msg", 0) != "A" || Variant(e, "msg", 100) != "B" {
		t.Error("variant bounds")
	}
	first := Variant(e, "msg", 50)
	for i := 0; i < 5; i++ {
		if Variant(e, "msg", 50) != first {
			t.Error("variant must be stable")
		}
	}
	b := 0
	for i := 0; i < 2000; i++ {
		if Variant(uuid.New(), "msg", 30) == "B" {
			b++
		}
	}
	if b < 450 || b > 750 {
		t.Errorf("30%% split gave %d of 2000", b)
	}
	j, c := uuid.New(), uuid.New()
	if InControl(j, c, 0) || !InControl(j, c, 100) {
		t.Error("control group bounds")
	}
	inCtl := InControl(j, c, 30)
	for i := 0; i < 5; i++ {
		if InControl(j, c, 30) != inCtl {
			t.Error("control group must be stable")
		}
	}
}

func TestNextBirthday(t *testing.T) {
	today := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	if got := NextBirthday(time.Date(1980, 3, 1, 0, 0, 0, 0, time.UTC), today); !got.Equal(today) {
		t.Errorf("birthday today: %s", got)
	}
	if got := NextBirthday(time.Date(1980, 2, 28, 0, 0, 0, 0, time.UTC), today); got.Year() != 2028 {
		t.Errorf("passed: %s", got)
	}
	if got := NextBirthday(time.Date(1984, 2, 29, 0, 0, 0, 0, time.UTC), time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)); got.Format("01-02") != "02-28" {
		t.Errorf("29 February in a common year: %s", got)
	}
}

func TestTemplatesValidate(t *testing.T) {
	for _, info := range TemplateInfos {
		in, err := TemplateInput(info.Template, DefaultVoucherType)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(&in); err != nil {
			t.Errorf("template %s: %v", info.Template, err)
		}
	}
	bad := JourneyInput{Code: "X", Name: "X", TriggerType: "event", TriggerEvent: "nope", Steps: []JourneyStep{
		{Key: "m", StepType: "message"}, {Key: "w", StepType: "wait", UntilDaysBefore: ip(7)}, {Key: "c", StepType: "condition", ConditionKind: sp("booked"),
			OnTrue: sp("missing")}, {Key: "m", StepType: "tag", Tag: sp("Bad Tag")}}}
	err := Validate(&bad)
	if err == nil {
		t.Fatal("invalid journey accepted")
	}
	for _, f := range []string{"triggerEvent", "steps[0].channel", "steps[0].body", "steps[1].untilDaysBefore", "steps[2].onTrue", "steps[3].key", "steps[3].tag"} {
		if !contains(err.Error()+fieldsOf(err), f) {
			t.Errorf("missing field error %s in %v", f, fieldsOf(err))
		}
	}
}
