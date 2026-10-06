package talent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
)

func TestScoreCriteria(t *testing.T) {
	cfg := hris.NewRecruitmentConfiguration()
	all := func(score string) []InterviewCriterionScore {
		var out []InterviewCriterionScore
		for _, c := range cfg.InterviewCriteria {
			out = append(out, InterviewCriterionScore{Code: c.Code, Score: score})
		}
		return out
	}
	scores, overall, err := scoreCriteria(cfg, all("4"))
	if err != nil || !overall.Equal(decimal.NewFromInt(4)) || len(scores) != len(cfg.InterviewCriteria) || scores[0].Label == "" {
		t.Fatalf("uniform: %v %s %v", scores, overall, err)
	}
	// Weights 3,3,2,1,1: job knowledge 5, service 5, communication 3, teamwork 2, culture 2 → (15+15+6+2+2)/10 = 4.
	in := []InterviewCriterionScore{{Code: "job_knowledge", Score: "5"}, {Code: "service_attitude", Score: "5"}, {Code: "communication", Score: "3"},
		{Code: "teamwork", Score: "2"}, {Code: "culture_fit", Score: "2"}}
	if _, overall, err := scoreCriteria(cfg, in); err != nil || !overall.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("weighted: %s %v", overall, err)
	}
	if _, _, err := scoreCriteria(cfg, in[:4]); err == nil {
		t.Error("a missing criterion must be refused")
	}
	if _, _, err := scoreCriteria(cfg, append(all("3"), InterviewCriterionScore{Code: "unknown", Score: "3"})); err == nil {
		t.Error("an unknown criterion must be refused")
	}
	if _, _, err := scoreCriteria(cfg, all("6")); err == nil {
		t.Error("a score above the scale must be refused")
	}
	if _, _, err := scoreCriteria(cfg, all("x")); err == nil {
		t.Error("a non-number must be refused")
	}
}

func TestRupiah(t *testing.T) {
	for v, want := range map[string]string{"0": "Rp 0", "950": "Rp 950", "5500000": "Rp 5.500.000", "1234567.6": "Rp 1.234.568", "-25000": "-Rp 25.000",
		"100000000": "Rp 100.000.000"} {
		if got := rupiah(decimal.RequireFromString(v)); got != want {
			t.Errorf("rupiah(%s) = %s, want %s", v, got, want)
		}
	}
}

func TestNormPhone(t *testing.T) {
	for in, want := range map[string]string{"0812-3456-7890": "6281234567890", "+62 812 3456 7890": "6281234567890", "": "", "(021) 555 1234": "62215551234"} {
		if got := normPhone(in); got != want {
			t.Errorf("normPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCVType(t *testing.T) {
	pdf := []byte("%PDF-1.4\n%…")
	if got := cvType("cv.pdf", pdf); got != "application/pdf" {
		t.Errorf("pdf: %s", got)
	}
	zip := []byte("PK\x03\x04\x14\x00\x06\x00rest of a docx archive")
	if got := cvType("Resume.DOCX", zip); got != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("docx: %s", got)
	}
	if got := cvType("archive.zip", zip); cvTypes[got] {
		t.Errorf("a zip that is not a docx must be refused: %s", got)
	}
	if got := cvType("script.sh", []byte("#!/bin/sh\necho hi")); cvTypes[got] {
		t.Errorf("text must be refused: %s", got)
	}
}

func TestPickTemplate(t *testing.T) {
	waiter, cook := "WAITER", "COOK"
	def := uuid.New()
	list := []templateRow{
		{id: uuid.New(), reviewType: "annual", positions: []string{"WAITER"}},
		{id: uuid.New(), reviewType: "any", positions: nil},
		{id: uuid.New(), reviewType: "probation", positions: nil},
		{id: def, reviewType: "any", positions: []string{"GM"}},
	}
	if got := pickTemplate(list, "annual", &waiter, nil); got == nil || got.id != list[0].id {
		t.Errorf("position template: %v", got)
	}
	if got := pickTemplate(list, "annual", &cook, nil); got == nil || got.id != list[1].id {
		t.Errorf("every-position template: %v", got)
	}
	if got := pickTemplate(list, "probation", &waiter, nil); got == nil || got.id != list[2].id {
		t.Errorf("probation template: %v", got)
	}
	if got := pickTemplate(list[3:], "annual", &cook, &def); got == nil || got.id != def {
		t.Errorf("cycle default: %v", got)
	}
	if got := pickTemplate(list[:1], "semester", &cook, nil); got != nil {
		t.Errorf("no template: %v", got)
	}
}

func TestParseItems(t *testing.T) {
	items, err := parseItems("competencies", `[{"code":"Service","label":" Service "},{"code":"team_work","label":"Teamwork","weight":"2"}]`)
	if err != nil || len(items) != 2 || items[0].Code != "service" || items[0].Weight != "1" || items[0].Label != "Service" {
		t.Fatalf("parse: %+v %v", items, err)
	}
	for _, bad := range []string{`[{"code":"a b","label":"x"}]`, `[{"code":"a","label":""}]`, `[{"code":"a","label":"x"},{"code":"a","label":"y"}]`,
		`[{"code":"a","label":"x","weight":"0"}]`, `[{"code":"a","label":"x","extra":1}]`, `{}`} {
		if _, err := parseItems("kpis", bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestWrapText(t *testing.T) {
	lines := wrapText("one two three four five six seven\nnext", 10)
	if len(lines) != 5 || lines[0] != "one two" || lines[4] != "next" {
		t.Errorf("wrap: %q", lines)
	}
}
