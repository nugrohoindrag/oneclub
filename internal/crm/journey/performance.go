package journey

// Journey performance (FR-JRN-07: sent, read, clicked, converted and the
// attributed revenue per journey and step), experiments (FR-JRN-06: A/B
// message variants and the control group), enrollment and event lists, the
// Member App Offers and the tracked link.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// JourneyEnrollment is one enrollment.
type JourneyEnrollment struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	JourneyID    uuid.UUID  `json:"journeyId" db:"journey_id"`
	CustomerID   uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerCode string     `json:"customerCode" db:"customer_code"`
	CustomerName string     `json:"customerName" db:"customer_name"`
	Occurrence   string     `json:"occurrence" db:"occurrence"`
	Cohort       string     `json:"cohort" db:"cohort" enum:"treatment,control"`
	Status       string     `json:"status" db:"status" enum:"active,completed,exited"`
	CurrentKey   *string    `json:"currentKey" db:"current_key"`
	NextRunAt    *time.Time `json:"nextRunAt" db:"next_run_at"`
	AnchorDate   *string    `json:"anchorDate" db:"anchor_date"`
	EnteredAt    time.Time  `json:"enteredAt" db:"entered_at"`
	CompletedAt  *time.Time `json:"completedAt" db:"completed_at"`
	ExitedAt     *time.Time `json:"exitedAt" db:"exited_at"`
	ExitReason   *string    `json:"exitReason" db:"exit_reason"`
	ConvertedAt  *time.Time `json:"convertedAt" db:"converted_at"`
	Revenue      string     `json:"revenue" db:"revenue"`
}

const enrollmentSelect = `SELECT e.id, e.journey_id, e.customer_id, c.code AS customer_code, c.name AS customer_name, e.occurrence, e.cohort, e.status,
	e.current_key, e.next_run_at, to_char(e.anchor_date, 'YYYY-MM-DD') AS anchor_date, e.entered_at, e.completed_at, e.exited_at, e.exit_reason,
	e.converted_at, trim_scale(e.revenue)::text AS revenue FROM crm.journey_enrollments e JOIN crm.customers c ON c.id = e.customer_id`

// Enrollments lists the enrollments of a journey.
func Enrollments(ctx context.Context, q dbtx.Querier, jid uuid.UUID, status, customer string, limit int) ([]JourneyEnrollment, error) {
	if limit <= 0 {
		limit = 100
	}
	return handle.List[JourneyEnrollment](q.Query(ctx, enrollmentSelect+` WHERE e.journey_id = $1 AND ($2 = '' OR e.status = $2)
		AND ($3 = '' OR e.customer_id::text = $3) ORDER BY e.entered_at DESC, e.id LIMIT $4`, jid, status, customer, limit))
}

// JourneyEvent is one executed step of an enrollment.
type JourneyEvent struct {
	ID             uuid.UUID      `json:"id" db:"id"`
	EnrollmentID   uuid.UUID      `json:"enrollmentId" db:"enrollment_id"`
	CustomerID     uuid.UUID      `json:"customerId" db:"customer_id"`
	CustomerName   string         `json:"customerName" db:"customer_name"`
	StepKey        string         `json:"stepKey" db:"step_key"`
	StepType       string         `json:"stepType" db:"step_type"`
	Outcome        string         `json:"outcome" db:"outcome"`
	Channel        *string        `json:"channel" db:"channel"`
	Variant        *string        `json:"variant" db:"variant"`
	Subject        *string        `json:"subject" db:"subject"`
	Body           *string        `json:"body" db:"body"`
	OfferTitle     *string        `json:"offerTitle" db:"offer_title"`
	PromoCode      *string        `json:"promoCode" db:"promo_code"`
	OfferExpiresOn *string        `json:"offerExpiresOn" db:"offer_expires_on"`
	Details        map[string]any `json:"details" db:"details"`
	ReadAt         *time.Time     `json:"readAt" db:"read_at"`
	ClickedAt      *time.Time     `json:"clickedAt" db:"clicked_at"`
	CreatedAt      time.Time      `json:"createdAt" db:"created_at"`
}

const eventSelect = `SELECT v.id, v.enrollment_id, v.customer_id, c.name AS customer_name, v.step_key, v.step_type, v.outcome, v.channel, v.variant,
	v.subject, v.body, v.offer_title, v.promo_code, to_char(v.offer_expires_on, 'YYYY-MM-DD') AS offer_expires_on, v.details, v.read_at, v.clicked_at,
	v.created_at FROM crm.journey_events v JOIN crm.customers c ON c.id = v.customer_id`

// Events lists the executed steps of a journey (optionally of one enrollment).
func Events(ctx context.Context, q dbtx.Querier, jid uuid.UUID, enrollment string, limit int) ([]JourneyEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	return handle.List[JourneyEvent](q.Query(ctx, eventSelect+` WHERE v.journey_id = $1 AND ($2 = '' OR v.enrollment_id::text = $2)
		ORDER BY v.created_at, v.id LIMIT $3`, jid, enrollment, limit))
}

// ── performance ───────────────────────────────────────────────────────────

// JourneyCohortStats are the figures of one cohort.
type JourneyCohortStats struct {
	Enrolled       int    `json:"enrolled"`
	Converted      int    `json:"converted"`
	ConversionRate string `json:"conversionRate"`
	Revenue        string `json:"revenue"`
}

// JourneyVariantStats are the figures of one A/B variant.
type JourneyVariantStats struct {
	Variant        string `json:"variant" enum:"A,B"`
	Sent           int    `json:"sent"`
	Read           int    `json:"read"`
	Clicked        int    `json:"clicked"`
	Converted      int    `json:"converted"`
	ConversionRate string `json:"conversionRate"`
}

// JourneyStepStats are the figures of one step.
type JourneyStepStats struct {
	Key       string                `json:"key"`
	Name      string                `json:"name"`
	StepType  string                `json:"stepType"`
	Executed  int                   `json:"executed"`
	Sent      int                   `json:"sent"`
	Skipped   map[string]int        `json:"skipped" doc:"Per reason (consent, suppression, contact, frequency cap, control, budget)"`
	Read      int                   `json:"read"`
	Clicked   int                   `json:"clicked"`
	Converted int                   `json:"converted" doc:"Enrollments that converted after receiving this step"`
	Revenue   string                `json:"revenue"`
	Variants  []JourneyVariantStats `json:"variants"`
}

// JourneyPerformance is the performance of a journey.
type JourneyPerformance struct {
	JourneyID      uuid.UUID          `json:"journeyId"`
	Code           string             `json:"code"`
	Name           string             `json:"name"`
	Status         string             `json:"status"`
	Enrolled       int                `json:"enrolled"`
	Active         int                `json:"active"`
	Completed      int                `json:"completed"`
	Exited         int                `json:"exited"`
	Converted      int                `json:"converted"`
	ConversionRate string             `json:"conversionRate"`
	Revenue        string             `json:"revenue" doc:"Attributed revenue of the converted enrollments"`
	Sent           int                `json:"sent"`
	Read           int                `json:"read"`
	Clicked        int                `json:"clicked"`
	Treatment      JourneyCohortStats `json:"treatment"`
	Control        JourneyCohortStats `json:"control"`
	Lift           string             `json:"lift" doc:"Conversion rate of the treatment − the control group"`
	Steps          []JourneyStepStats `json:"steps"`
}

func rate(n, d int) string {
	if d == 0 {
		return "0"
	}
	return decimal.NewFromInt(int64(n)).Div(decimal.NewFromInt(int64(d))).Round(4).String()
}

// Performance computes the performance of a journey.
func Performance(ctx context.Context, q dbtx.Querier, property, jid uuid.UUID) (JourneyPerformance, error) {
	j, err := Get(ctx, q, property, jid, false)
	if err != nil {
		return JourneyPerformance{}, err
	}
	out := JourneyPerformance{JourneyID: j.ID, Code: j.Code, Name: j.Name, Status: j.Status, Steps: []JourneyStepStats{}}
	type cohort struct {
		Cohort    string `db:"cohort"`
		Enrolled  int    `db:"enrolled"`
		Active    int    `db:"active"`
		Completed int    `db:"completed"`
		Exited    int    `db:"exited"`
		Converted int    `db:"converted"`
		Revenue   string `db:"revenue"`
	}
	cs, err := handle.List[cohort](q.Query(ctx, `SELECT cohort, count(*)::int AS enrolled, count(*) FILTER (WHERE status = 'active')::int AS active,
		count(*) FILTER (WHERE status = 'completed')::int AS completed, count(*) FILTER (WHERE status = 'exited')::int AS exited,
		count(*) FILTER (WHERE converted_at IS NOT NULL)::int AS converted, coalesce(sum(revenue) FILTER (WHERE converted_at IS NOT NULL), 0)::text AS revenue
		FROM crm.journey_enrollments WHERE journey_id = $1 GROUP BY cohort`, jid))
	if err != nil {
		return out, err
	}
	total := decimal.Zero
	out.Treatment = JourneyCohortStats{ConversionRate: "0", Revenue: "0"}
	out.Control = JourneyCohortStats{ConversionRate: "0", Revenue: "0"}
	for _, c := range cs {
		out.Enrolled += c.Enrolled
		out.Active += c.Active
		out.Completed += c.Completed
		out.Exited += c.Exited
		out.Converted += c.Converted
		rev, _ := decimal.NewFromString(c.Revenue)
		total = total.Add(rev)
		st := JourneyCohortStats{Enrolled: c.Enrolled, Converted: c.Converted, ConversionRate: rate(c.Converted, c.Enrolled), Revenue: rev.String()}
		if c.Cohort == "control" {
			out.Control = st
		} else {
			out.Treatment = st
		}
	}
	out.Revenue, out.ConversionRate = total.String(), rate(out.Converted, out.Enrolled)
	tr, _ := decimal.NewFromString(out.Treatment.ConversionRate)
	cr, _ := decimal.NewFromString(out.Control.ConversionRate)
	out.Lift = tr.Sub(cr).String()
	type stepRow struct {
		Key       string  `db:"step_key"`
		Outcome   string  `db:"outcome"`
		Variant   *string `db:"variant"`
		N         int     `db:"n"`
		Read      int     `db:"read"`
		Clicked   int     `db:"clicked"`
		Converted int     `db:"converted"`
		Revenue   string  `db:"revenue"`
	}
	rows, err := handle.List[stepRow](q.Query(ctx, `SELECT m.step_key, m.outcome, m.variant, count(*)::int AS n,
		count(*) FILTER (WHERE m.read_at IS NOT NULL)::int AS read, count(*) FILTER (WHERE m.clicked_at IS NOT NULL)::int AS clicked,
		count(*) FILTER (WHERE m.converted_at IS NOT NULL AND m.converted_at >= m.sent_at)::int AS converted,
		coalesce(sum(m.revenue) FILTER (WHERE m.converted_at IS NOT NULL AND m.converted_at >= m.sent_at), 0)::text AS revenue
		FROM reporting.crm_journey_events m WHERE m.journey_id = $1 GROUP BY m.step_key, m.outcome, m.variant`, jid))
	if err != nil {
		return out, err
	}
	byKey := map[string]*JourneyStepStats{}
	for _, st := range j.Steps {
		s := JourneyStepStats{Key: st.Key, Name: st.Name, StepType: st.StepType, Skipped: map[string]int{}, Revenue: "0", Variants: []JourneyVariantStats{}}
		out.Steps = append(out.Steps, s)
	}
	for i := range out.Steps {
		byKey[out.Steps[i].Key] = &out.Steps[i]
	}
	variants := map[string]map[string]*JourneyVariantStats{}
	for _, r := range rows {
		s := byKey[r.Key]
		if s == nil {
			continue
		}
		s.Executed += r.N
		switch r.Outcome {
		case "sent":
			s.Sent += r.N
			s.Read += r.Read
			s.Clicked += r.Clicked
			s.Converted += r.Converted
			rev, _ := decimal.NewFromString(s.Revenue)
			add, _ := decimal.NewFromString(r.Revenue)
			s.Revenue = rev.Add(add).String()
			out.Sent += r.N
			out.Read += r.Read
			out.Clicked += r.Clicked
			if r.Variant != nil {
				if variants[r.Key] == nil {
					variants[r.Key] = map[string]*JourneyVariantStats{}
				}
				v := variants[r.Key][*r.Variant]
				if v == nil {
					v = &JourneyVariantStats{Variant: *r.Variant}
					variants[r.Key][*r.Variant] = v
				}
				v.Sent += r.N
				v.Read += r.Read
				v.Clicked += r.Clicked
				v.Converted += r.Converted
			}
		case "skipped_no_consent", "skipped_suppressed", "skipped_no_contact", "skipped_frequency_cap", "control", "skipped_budget", "failed":
			s.Skipped[r.Outcome] += r.N
		}
	}
	for k, vs := range variants {
		s := byKey[k]
		for _, name := range []string{"A", "B"} {
			if v := vs[name]; v != nil {
				v.ConversionRate = rate(v.Converted, v.Sent)
				s.Variants = append(s.Variants, *v)
			}
		}
	}
	return out, nil
}

// ── experiments (FR-JRN-06) ───────────────────────────────────────────────

// JourneyExperimentArm is one arm of an experiment.
type JourneyExperimentArm struct {
	Arm            string `json:"arm" doc:"A / B (message variants) or treatment / control"`
	Size           int    `json:"size" doc:"Messages sent (A/B) or enrollments (control group)"`
	Clicked        int    `json:"clicked"`
	Converted      int    `json:"converted"`
	ConversionRate string `json:"conversionRate"`
}

// JourneyExperiment is an A/B test of a message step or the control group of a journey.
type JourneyExperiment struct {
	JourneyID   uuid.UUID              `json:"journeyId"`
	JourneyCode string                 `json:"journeyCode"`
	JourneyName string                 `json:"journeyName"`
	Kind        string                 `json:"kind" enum:"ab_test,control_group"`
	StepKey     *string                `json:"stepKey"`
	Percent     int                    `json:"percent" doc:"% in variant B / in the control group"`
	Arms        []JourneyExperimentArm `json:"arms"`
	Leader      *string                `json:"leader" doc:"Arm with the higher conversion rate"`
}

func leader(arms []JourneyExperimentArm) *string {
	if len(arms) < 2 || arms[0].Size == 0 || arms[1].Size == 0 {
		return nil
	}
	a, _ := decimal.NewFromString(arms[0].ConversionRate)
	b, _ := decimal.NewFromString(arms[1].ConversionRate)
	switch {
	case a.GreaterThan(b):
		return &arms[0].Arm
	case b.GreaterThan(a):
		return &arms[1].Arm
	}
	return nil
}

// Experiments lists the experiments of a property's journeys.
func Experiments(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]JourneyExperiment, error) {
	js, err := List(ctx, q, property, "", "", 500)
	if err != nil {
		return nil, err
	}
	out := []JourneyExperiment{}
	for _, j := range js {
		var steps []JourneyStep
		if steps, err = Steps(ctx, q, j.ID); err != nil {
			return nil, err
		}
		hasAB := false
		for _, s := range steps {
			if s.StepType == "message" && s.SplitPercent > 0 {
				hasAB = true
			}
		}
		if j.ControlPercent == 0 && !hasAB {
			continue
		}
		p, err := Performance(ctx, q, property, j.ID)
		if err != nil {
			return nil, err
		}
		if j.ControlPercent > 0 {
			arms := []JourneyExperimentArm{{Arm: "treatment", Size: p.Treatment.Enrolled, Converted: p.Treatment.Converted, ConversionRate: p.Treatment.ConversionRate},
				{Arm: "control", Size: p.Control.Enrolled, Converted: p.Control.Converted, ConversionRate: p.Control.ConversionRate}}
			out = append(out, JourneyExperiment{JourneyID: j.ID, JourneyCode: j.Code, JourneyName: j.Name, Kind: "control_group", Percent: j.ControlPercent,
				Arms: arms, Leader: leader(arms)})
		}
		for _, s := range steps {
			if s.StepType != "message" || s.SplitPercent == 0 {
				continue
			}
			arms := []JourneyExperimentArm{{Arm: "A", ConversionRate: "0"}, {Arm: "B", ConversionRate: "0"}}
			for _, st := range p.Steps {
				if st.Key != s.Key {
					continue
				}
				for _, v := range st.Variants {
					i := 0
					if v.Variant == "B" {
						i = 1
					}
					arms[i] = JourneyExperimentArm{Arm: v.Variant, Size: v.Sent, Clicked: v.Clicked, Converted: v.Converted, ConversionRate: v.ConversionRate}
				}
			}
			key := s.Key
			out = append(out, JourneyExperiment{JourneyID: j.ID, JourneyCode: j.Code, JourneyName: j.Name, Kind: "ab_test", StepKey: &key,
				Percent: s.SplitPercent, Arms: arms, Leader: leader(arms)})
		}
	}
	return out, nil
}

// ── Member App Offers & tracked link ──────────────────────────────────────

// MemberOffer is an offer of a journey in the Member App.
type MemberOffer struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	JourneyName string     `json:"journeyName" db:"journey_name"`
	Title       string     `json:"title" db:"title"`
	Message     *string    `json:"message" db:"body"`
	PromoCode   *string    `json:"promoCode" db:"promo_code"`
	ExpiresOn   *string    `json:"expiresOn" db:"offer_expires_on"`
	ReadAt      *time.Time `json:"readAt" db:"read_at"`
	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
}

const offerSelect = `SELECT v.id, j.name AS journey_name, coalesce(v.offer_title, v.subject, j.name) AS title, v.body, v.promo_code,
	to_char(v.offer_expires_on, 'YYYY-MM-DD') AS offer_expires_on, v.read_at, v.created_at FROM crm.journey_events v JOIN crm.journeys j ON j.id = v.journey_id`

// Offers lists the open offers of a customer (sent messages with an offer or
// in-app, and issued vouchers / rewards).
func Offers(ctx context.Context, q dbtx.Querier, customer uuid.UUID, today time.Time) ([]MemberOffer, error) {
	return handle.List[MemberOffer](q.Query(ctx, offerSelect+` WHERE v.customer_id = $1 AND ((v.outcome = 'sent' AND (v.offer_title IS NOT NULL OR v.channel = 'in_app'))
		OR (v.outcome = 'issued' AND v.step_type IN ('voucher', 'reward'))) AND (v.offer_expires_on IS NULL OR v.offer_expires_on >= $2::date)
		ORDER BY v.created_at DESC LIMIT 100`, customer, today))
}

// MarkOfferRead marks an offer of a customer as read.
func MarkOfferRead(ctx context.Context, tx pgx.Tx, customer, oid uuid.UUID) (MemberOffer, error) {
	tag, err := tx.Exec(ctx, `UPDATE crm.journey_events SET read_at = coalesce(read_at, now()) WHERE id = $1 AND customer_id = $2`, oid, customer)
	if err != nil {
		return MemberOffer{}, err
	}
	if tag.RowsAffected() == 0 {
		return MemberOffer{}, errs.NotFound("offer")
	}
	rows, err := tx.Query(ctx, offerSelect+` WHERE v.id = $1`, oid)
	return handle.One[MemberOffer](rows, err, "offer")
}

// TrackClick records a click of a journey link.
func TrackClick(ctx context.Context, tx pgx.Tx, token string) error {
	tag, err := tx.Exec(ctx, `UPDATE crm.journey_events SET clicked_at = coalesce(clicked_at, now()), click_count = click_count + 1,
		read_at = coalesce(read_at, now()) WHERE token = $1`, token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.NotFound("link")
	}
	return nil
}
