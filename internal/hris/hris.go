// Package hris is the public interface of the HRIS module (PRD P5 §5.4.1,
// Technical Doc §4.2): the API the other P5 areas (time & attendance,
// payroll, BI) and internal/app build on. It holds no HTTP code:
//
//   - domain events and their payloads (contracts H6/H7, docs/p5-contracts.md)
//   - employee, organization, position, grade and contract lookups
//   - certification checks (FR-TRC-03 enforcement)
//   - the HR & Workforce policy types with their versioned defaults and
//     loaders (EP-24)
//   - the Employee Self Service section registry (EP-16)
//
// Core HR itself (screens, actions, jobs) lives in hris/corehr. The hris
// module sits in the back office layer: business lines never import it;
// golf and sport club reach it through hooks wired by internal/app.
package hris

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Module is the module code (Enabled Modules, permissions, routes).
const Module = "hris"

// Domain events published through the outbox (PRD P5 §11).
const (
	EventEmployeeHired        = "hris.employee_hired"
	EventEmployeeTerminated   = "hris.employee_terminated"
	EventContractExpiring     = "hris.contract_expiring"
	EventCertificationExpired = "hris.certification_expired"
)

// Employment statuses (PRD P5 §7.6, Naming Convention §31).
const (
	StatusProbation  = "probation"
	StatusContract   = "contract"
	StatusPermanent  = "permanent"
	StatusResigned   = "resigned"
	StatusTerminated = "terminated"
)

// EmploymentStatuses lists the statuses of an employee.
var EmploymentStatuses = []string{StatusProbation, StatusContract, StatusPermanent, StatusResigned, StatusTerminated}

// WorkforceRoles are the roles a certification can be mandatory for
// (PRD P5 §16 #9). Caddies and instructors may be partners (non-employees).
var WorkforceRoles = []string{"lifeguard", "caddy", "instructor", "food_handler", "engineering", "course_maintenance", "security", "sport_staff",
	"starter", "other"}

// Publisher publishes domain events (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// EmployeeHired is the payload of hris.employee_hired.
type EmployeeHired struct {
	EmployeeID       uuid.UUID  `json:"employeeId"`
	EmployeeNo       string     `json:"employeeNo"`
	FullName         string     `json:"fullName"`
	PropertyID       uuid.UUID  `json:"propertyId"`
	OrgUnitID        *uuid.UUID `json:"orgUnitId"`
	PositionID       *uuid.UUID `json:"positionId"`
	GradeID          *uuid.UUID `json:"gradeId"`
	EmploymentStatus string     `json:"employmentStatus"`
	WorkerCategory   string     `json:"workerCategory"`
	JoinDate         string     `json:"joinDate"`
}

// EmployeeTerminated is the payload of hris.employee_terminated (H7),
// published on the effective date: from that date the employee cannot log
// in (IAM deactivates UserID) and pending approvals assigned to the user
// move to the supervisor.
type EmployeeTerminated struct {
	EmployeeID       uuid.UUID  `json:"employeeId"`
	EmployeeNo       string     `json:"employeeNo"`
	FullName         string     `json:"fullName"`
	PropertyID       uuid.UUID  `json:"propertyId"`
	UserID           *uuid.UUID `json:"userId"`
	EffectiveDate    string     `json:"effectiveDate" doc:"First day the employee no longer works (YYYY-MM-DD)"`
	TerminationType  string     `json:"terminationType" enum:"resigned,terminated,contract_ended,retired,deceased"`
	EmploymentStatus string     `json:"employmentStatus" enum:"resigned,terminated"`
	Reason           string     `json:"reason"`
	SupervisorID     *uuid.UUID `json:"supervisorId"`
	SupervisorUserID *uuid.UUID `json:"supervisorUserId"`
}

// ContractExpiring is the payload of hris.contract_expiring, published at
// each reminder of the HR Configuration (H-30 and H-7 by default).
type ContractExpiring struct {
	ContractID    uuid.UUID `json:"contractId"`
	Number        string    `json:"number"`
	EmployeeID    uuid.UUID `json:"employeeId"`
	EmployeeNo    string    `json:"employeeNo"`
	FullName      string    `json:"fullName"`
	PropertyID    uuid.UUID `json:"propertyId"`
	ContractType  string    `json:"contractType" enum:"pkwt,pkwtt"`
	EndDate       string    `json:"endDate"`
	DaysRemaining int       `json:"daysRemaining"`
}

// CertificationExpired is the payload of hris.certification_expired (H7),
// published by the daily job once a certificate is past its expiry date
// without a valid renewal.
type CertificationExpired struct {
	CertificationID       uuid.UUID  `json:"certificationId"`
	CertificationTypeID   uuid.UUID  `json:"certificationTypeId"`
	CertificationTypeCode string     `json:"certificationTypeCode"`
	CertificationTypeName string     `json:"certificationTypeName"`
	PropertyID            uuid.UUID  `json:"propertyId"`
	HolderKind            string     `json:"holderKind" enum:"employee,caddy,instructor"`
	EmployeeID            *uuid.UUID `json:"employeeId"`
	PartnerID             *uuid.UUID `json:"partnerId" doc:"golf caddy id or sport club instructor id of a partner holder"`
	HolderName            string     `json:"holderName"`
	ExpiresOn             string     `json:"expiresOn"`
	MandatoryFor          []string   `json:"mandatoryFor" doc:"Workforce roles that need a valid certificate of this type"`
}
