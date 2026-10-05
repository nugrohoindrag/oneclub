package hris

// Device clock-in of partners (PRD P5 FR-ATT-08, Should): caddies and
// instructors are not employees (§16 #4), but the club may let them clock
// in on the biometric devices (face recognition / fingerprint through the
// bridge agent, §16 #7). hris keeps their device user number, consent and
// clock events; golf reads the caddies' first clock-in of the day from the
// outbox event below and marks the caddy present in Caddy Master (the
// Caddy Queue), wired by internal/app — golf never imports hris.

import (
	"time"

	"github.com/google/uuid"
)

// EventPartnerAttendanceRecorded is published for every accepted partner
// clock event (a resubmitted device event is published once).
const EventPartnerAttendanceRecorded = "hris.partner_attendance_recorded"

// PartnerAttendanceRecorded is the payload of
// hris.partner_attendance_recorded.
type PartnerAttendanceRecorded struct {
	EventID     uuid.UUID `json:"eventId"`
	PropertyID  uuid.UUID `json:"propertyId"`
	ProfileID   uuid.UUID `json:"profileId"`
	HolderKind  string    `json:"holderKind" enum:"caddy,instructor"`
	PartnerID   uuid.UUID `json:"partnerId"`
	PartnerName string    `json:"partnerName"`
	WorkDate    string    `json:"workDate"`
	Direction   string    `json:"direction" enum:"in,out"`
	OccurredAt  time.Time `json:"occurredAt"`
	Method      string    `json:"method" enum:"face_recognition,fingerprint"`
	DeviceID    uuid.UUID `json:"deviceId"`
	DeviceCode  string    `json:"deviceCode"`
	Offline     bool      `json:"offline"`
	// FirstInOfDay is true for the partner's first clock-in of the work date.
	FirstInOfDay bool `json:"firstInOfDay"`
}

// PartnerDeviceUserBase is the first device user number of partners: the
// employees keep the numbers of their employee numbers below it.
const PartnerDeviceUserBase = 90001
