package payroll

// Demo data of payroll: the default pay components, a salary structure per
// grade (meal allowance per present day; basic salary, transport and
// position allowances come from the demo contracts), one payroll run of
// last month posted and paid, and one run of this month calculated (the
// month is still running). The demo runs are booked without accounting
// events: the demo accounting book is opened by Finance with its cut-over.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/id"
)

// demoMeal is the meal allowance per present day of a grade.
var demoMeal = map[string]string{"G1": "35000", "G2": "35000", "G3": "40000", "G4": "45000", "G5": "50000", "G6": "50000"}

// SeedDemo seeds the payroll demo of a property (idempotent).
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var seeded, employees bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.payroll_runs WHERE property_id = $1),
		EXISTS (SELECT 1 FROM hris.employees WHERE property_id = $1 AND employee_no = 'EMP-00001')`, property).Scan(&seeded, &employees); err != nil {
		return err
	}
	if seeded || !employees {
		return nil
	}
	if _, _, err := ensureComponents(ctx, tx, property); err != nil {
		return err
	}
	day := today(ctx, tx, property)
	eff := time.Date(day.Year()-1, 1, 1, 0, 0, 0, 0, time.UTC)
	rows, err := tx.Query(ctx, `SELECT id, code FROM hris.grades WHERE archived_at IS NULL ORDER BY level`)
	if err != nil {
		return err
	}
	grades := map[string]uuid.UUID{}
	for rows.Next() {
		var gid uuid.UUID
		var code string
		if err := rows.Scan(&gid, &code); err != nil {
			rows.Close()
			return err
		}
		grades[code] = gid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for code, meal := range demoMeal {
		gid, ok := grades[code]
		if !ok {
			continue
		}
		lines, _ := json.Marshal([]SalaryStructureLine{{ComponentCode: "MEAL", Amount: meal, Note: "Per present day"}})
		if _, err := tx.Exec(ctx, `INSERT INTO hris.salary_structures (id, property_id, code, version, name, scope, grade_id, effective_from, status, lines,
			activated_at) VALUES ($1,$2,$3,1,$4,'grade',$5,$6,'active',$7,now()) ON CONFLICT (property_id, code, version) DO NOTHING`,
			id.New(), property, "GRADE-"+code, "Grade "+code+" structure", gid, ymd(eff), lines); err != nil {
			return err
		}
	}
	m := &Module{}
	last := time.Date(day.Year(), day.Month()-1, 1, 0, 0, 0, 0, time.UTC)
	rid, err := m.CreateRun(ctx, tx, property, PayrollRunInput{RunType: hris.RunRegular, Notes: strPtr("Demo: posted and paid")}, last)
	if err != nil {
		return err
	}
	if _, err := m.Calculate(ctx, tx, rid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET status = 'approved', submitted_at = now(), approved_at = now(),
		decision_note = 'Demo approval' WHERE id = $1`, rid); err != nil {
		return err
	}
	if err := m.Post(ctx, tx, rid); err != nil {
		return err
	}
	var pay time.Time
	if err := tx.QueryRow(ctx, `SELECT payment_date FROM hris.payroll_runs WHERE id = $1`, rid).Scan(&pay); err != nil {
		return err
	}
	if err := m.MarkPaid(ctx, tx, rid, PayrollPaymentInput{PaidOn: ymd(pay), Reference: "DEMO-TRF-" + periodCode(last)}); err != nil {
		return err
	}
	cur := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	rid, err = m.CreateRun(ctx, tx, property, PayrollRunInput{RunType: hris.RunRegular, Notes: strPtr("Demo: calculated, to approve")}, cur)
	if err != nil {
		return err
	}
	_, err = m.Calculate(ctx, tx, rid)
	return err
}

func strPtr(s string) *string { return &s }
