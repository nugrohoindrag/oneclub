package payouts

// Employee Self Service sections of the area (EP-16, §16 #6): Service
// Charge — the employee's share per month with the attendance factor and
// the redistribution, or why they were not eligible — and Commission &
// Bonus — approved commission statements (with clawbacks) and bonuses with
// the payroll that pays them. Payroll data: never cached offline
// (FR-OPS-P5-05).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

func init() {
	hris.RegisterESSSection(hris.ESSSection{Key: "service-charge", Label: "Service Charge", LabelID: "Service Charge", Icon: "room_service",
		Path: "/ops/ess/service-charge", Order: 62})
	hris.RegisterESSSection(hris.ESSSection{Key: "commissions", Label: "Commission & Bonus", LabelID: "Komisi & Bonus", Icon: "workspace_premium",
		Path: "/ops/ess/commissions", Order: 64})
}

// EmployeeServiceCharge is one month of the employee's service charge.
type EmployeeServiceCharge struct {
	DistributionID   uuid.UUID  `json:"distributionId" db:"distribution_id"`
	Number           string     `json:"number" db:"number"`
	Period           string     `json:"period" db:"period" doc:"YYYY-MM of the pool"`
	PayPeriod        string     `json:"payPeriod" db:"pay_period"`
	Status           string     `json:"status" db:"status" enum:"approved,paid"`
	Eligible         bool       `json:"eligible" db:"eligible"`
	ExclusionReason  *string    `json:"exclusionReason" db:"exclusion_reason"`
	ScheduledDays    int        `json:"scheduledDays" db:"scheduled_days"`
	PresentDays      string     `json:"presentDays" db:"present_days"`
	AttendanceFactor string     `json:"attendanceFactor" db:"attendance_factor"`
	BaseShare        string     `json:"baseShare" db:"base_share"`
	AttendanceAmount string     `json:"attendanceAmount" db:"attendance_amount"`
	Redistributed    string     `json:"redistributed" db:"redistributed"`
	Amount           string     `json:"amount" db:"amount"`
	PaidAt           *time.Time `json:"paidAt" db:"paid_at" doc:"Posted with the payroll"`
}

// EmployeePayoutItem is an approved commission statement or bonus of the employee.
type EmployeePayoutItem struct {
	Type        string     `json:"type" db:"type" enum:"commission,bonus"`
	ID          uuid.UUID  `json:"id" db:"id"`
	Number      string     `json:"number" db:"number"`
	Description string     `json:"description" db:"description"`
	Period      string     `json:"period" db:"period" doc:"Commission month / bonus pay period (YYYY-MM)"`
	Earning     string     `json:"earning" db:"earning"`
	Deduction   string     `json:"deduction" db:"deduction" doc:"Clawback of earlier commission"`
	Net         string     `json:"net" db:"net"`
	Status      string     `json:"status" db:"status" enum:"ready,paid"`
	PaidAt      *time.Time `json:"paidAt" db:"paid_at"`
}

func (m *Module) registerESS(reg *route.Registry) {
	tag := "Employee Self Service"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/service-charge", Summary: "My service charge per month",
		Permission: hris.PermissionESS, Response: EmployeeServiceCharge{}, List: true, Handler: listRead(m.DB, m.essServiceChargeHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/commissions", Summary: "My commission statements and bonuses paid with payroll",
		Permission: hris.PermissionESS, Response: EmployeePayoutItem{}, List: true, Handler: listRead(m.DB, m.essCommissionsHTTP)})
}

func (m *Module) essServiceChargeHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]EmployeeServiceCharge, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	return handle.List[EmployeeServiceCharge](tx.Query(ctx, `SELECT d.id AS distribution_id, d.number, to_char(make_date(d.year, d.month, 1), 'YYYY-MM') AS period,
		d.pay_period, d.status, l.eligible, l.exclusion_reason, l.scheduled_days, trim_scale(l.present_days)::text AS present_days,
		trim_scale(l.attendance_factor)::text AS attendance_factor, trim_scale(l.base_share)::text AS base_share,
		trim_scale(l.attendance_amount)::text AS attendance_amount, trim_scale(l.redistributed)::text AS redistributed, trim_scale(l.amount)::text AS amount,
		l.consumed_at AS paid_at
		FROM hris.service_charge_lines l JOIN hris.service_charge_distributions d ON d.id = l.distribution_id
		WHERE l.employee_id = $1 AND d.status IN ('approved', 'paid') ORDER BY d.year DESC, d.month DESC LIMIT 36`, e.ID))
}

func (m *Module) essCommissionsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]EmployeePayoutItem, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	return handle.List[EmployeePayoutItem](tx.Query(ctx, `SELECT * FROM (
		SELECT 'commission' AS type, c.id, c.number, 'Sales commission ' || c.period AS description, c.period, trim_scale(c.earning)::text AS earning,
		  trim_scale(c.deduction)::text AS deduction, trim_scale(c.total)::text AS net, c.status, c.consumed_at AS paid_at
		FROM hris.commission_payouts c WHERE c.employee_id = $1 AND c.status IN ('ready', 'paid')
		UNION ALL
		SELECT 'bonus', l.id, b.number, b.name, b.pay_period, trim_scale(l.amount)::text, '0', trim_scale(l.amount)::text,
		  CASE WHEN l.consumed_at IS NULL THEN 'ready' ELSE 'paid' END, l.consumed_at
		FROM hris.bonus_lines l JOIN hris.bonus_programmes b ON b.id = l.programme_id WHERE l.employee_id = $1 AND b.status IN ('approved', 'paid')
		) x ORDER BY period DESC, number DESC LIMIT 100`, e.ID))
}
