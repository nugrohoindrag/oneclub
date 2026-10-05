package sportclub

// Instructor certification enforcement of PRD P5 (FR-TRC-03, contract H7):
// a class schedule is refused when its instructor lacks a valid mandatory
// certification during the schedule. Sport club does not import hris
// (layer 1): internal/app wires the check to the HRIS certification
// register; without it nothing is checked.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
)

// InstructorCertificationCheck returns an error (a conflict) when the
// instructor (an employee when employeeID is set, else a partner) may not
// teach between from and to.
type InstructorCertificationCheck func(ctx context.Context, q dbtx.Querier, property, instructorID uuid.UUID, employeeID *uuid.UUID, name string,
	from, to time.Time) error

var instructorCertificationCheck InstructorCertificationCheck

// SetInstructorCertificationCheck wires the certification check (internal/app).
func SetInstructorCertificationCheck(f InstructorCertificationCheck) {
	instructorCertificationCheck = f
}

// checkInstructorCertification applies the wired check to a schedule.
func checkInstructorCertification(ctx context.Context, q dbtx.Querier, property, instructor uuid.UUID, from, to time.Time) error {
	if instructorCertificationCheck == nil {
		return nil
	}
	var emp *uuid.UUID
	var name string
	if err := q.QueryRow(ctx, `SELECT employee_id, name FROM sportclub.instructors WHERE id = $1`, instructor).Scan(&emp, &name); err != nil {
		return err
	}
	return instructorCertificationCheck(ctx, q, property, instructor, emp, name, from, to)
}
