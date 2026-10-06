package hrtime

import (
	"testing"

	"oneclub/internal/hris"
)

func TestFilterDays(t *testing.T) {
	days := []AttendanceDayView{
		{EmployeeName: "clean", Flags: []string{}},
		{EmployeeName: "no out", Flags: []string{hris.FlagMissingOut}},
		{EmployeeName: "no in", Flags: []string{hris.FlagMissingIn, hris.FlagCorrected}},
		{EmployeeName: "far", Flags: []string{hris.FlagOutOfArea}},
	}
	names := func(ds []AttendanceDayView) (out []string) {
		for _, d := range ds {
			out = append(out, d.EmployeeName)
		}
		return out
	}
	for _, c := range []struct {
		flag string
		want int
	}{{"", 4}, {"missing", 2}, {"any", 3}, {hris.FlagOutOfArea, 1}, {hris.FlagUnapprovedOvertime, 0}} {
		if got := filterDays(days, c.flag); len(got) != c.want {
			t.Errorf("flag %q: %v", c.flag, names(got))
		}
	}
}
