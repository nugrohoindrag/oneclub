package talent

// Operational inputs of a review from the HRIS itself (FR-PRF-HR-03):
// training attended and passed in the period and the mandatory
// certifications of the position. Other areas register theirs through
// hris.RegisterReviewInput (attendance from time & attendance, sales target
// achievement from CRM through internal/app).

import (
	"context"
	"time"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
)

func init() {
	hris.RegisterReviewInput("training", trainingInput)
	hris.RegisterReviewInput("certifications", certificationInput)
}

// trainingInput counts the training sessions attended and passed.
func trainingInput(ctx context.Context, q dbtx.Querier, e hris.Employee, from, to time.Time) ([]hris.ReviewInput, error) {
	var attended, passed int
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE p.attendance = 'attended'), count(*) FILTER (WHERE p.result = 'passed')
		FROM hris.training_participants p JOIN hris.training_sessions s ON s.id = p.session_id
		WHERE p.employee_id = $1 AND s.archived_at IS NULL AND (s.starts_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date`, e.ID, ymd(from), ymd(to),
		location(ctx, q, e.PropertyID).String()).
		Scan(&attended, &passed); err != nil {
		return nil, err
	}
	if attended == 0 {
		return nil, nil
	}
	return []hris.ReviewInput{{Key: "training", Label: "Training attended", Value: itoa(attended), Unit: "count", Note: itoa(passed) + " passed"}}, nil
}

// certificationInput reports the mandatory certifications missing at the
// end of the period (FR-TRC-03).
func certificationInput(ctx context.Context, q dbtx.Querier, e hris.Employee, _, to time.Time) ([]hris.ReviewInput, error) {
	chk, err := hris.CheckEmployee(ctx, q, e.ID, "", to)
	if err != nil || len(chk.Required) == 0 {
		return nil, err
	}
	note := "All mandatory certifications valid"
	if len(chk.Gaps) > 0 {
		note = itoa(len(chk.Gaps)) + " mandatory certification(s) missing or expired"
	}
	return []hris.ReviewInput{{Key: "certifications", Label: "Certification gaps", Value: itoa(len(chk.Gaps)), Unit: "count", Note: note}}, nil
}
