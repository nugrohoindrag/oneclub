-- PRD P5 time & attendance (EP-06 Shift Scheduling, EP-07 Attendance, EP-08
-- Leave, Permission & Overtime, the EP-16 sections of Employee Self Service
-- and the EP-25/26 attendance devices, kiosk and offline sync).
--
-- Dates are local business dates of the property (Asia/Jakarta for the
-- club); instants are timestamptz. Biometric templates are never stored
-- (PRD P5 §6 #9, FR-ATT-06): devices keep them, OneClub keeps the
-- attendance events, the device user number and the written consent.
-- Overtime pay is not stored here: it derives from salaries and is computed
-- for viewers allowed to see salaries (payroll computes its own).

-- +goose Up

-- Holiday calendar of HR (FR-OVT-03 workday vs holiday, leave working
-- days): public holidays, collective leave (cuti bersama, may deduct annual
-- leave) and company holidays. The P1 Day Calendar public holidays also
-- count as holidays.
CREATE TABLE hris.holidays (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  holiday_date          date NOT NULL,
  name                  text NOT NULL,
  kind                  text NOT NULL DEFAULT 'public_holiday' CHECK (kind IN ('public_holiday', 'collective_leave', 'company_holiday')),
  deducts_annual_leave  boolean NOT NULL DEFAULT false,
  notes                 text,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  archived_at           timestamptz,
  CHECK (kind = 'collective_leave' OR NOT deducts_annual_leave)
);
SELECT platform.enable_property_rls('hris.holidays');
SELECT platform.add_touch_trigger('hris.holidays');
CREATE UNIQUE INDEX holidays_day_uniq ON hris.holidays (property_id, holiday_date) WHERE archived_at IS NULL;

-- Shift templates per department (FR-SCH-01): local start / end, break,
-- crossing midnight when the end is not after the start.
CREATE TABLE hris.shift_templates (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  code              text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name              text NOT NULL,
  org_unit_id       uuid REFERENCES hris.org_units (id),
  start_time        time NOT NULL,
  end_time          time NOT NULL,
  break_minutes     int NOT NULL DEFAULT 60 CHECK (break_minutes BETWEEN 0 AND 240),
  crosses_midnight  boolean GENERATED ALWAYS AS (end_time <= start_time) STORED,
  work_minutes      int GENERATED ALWAYS AS ((EXTRACT(EPOCH FROM (end_time - start_time))::int / 60)
                      + CASE WHEN end_time <= start_time THEN 1440 ELSE 0 END - break_minutes) STORED,
  workforce_role    text CHECK (workforce_role IS NULL OR workforce_role IN ('lifeguard', 'caddy', 'instructor', 'food_handler', 'engineering',
                      'course_maintenance', 'security', 'sport_staff', 'starter', 'other')),
  color             text,
  description       text,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  archived_at       timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('hris.shift_templates');
SELECT platform.add_touch_trigger('hris.shift_templates');

-- Minimum staff per org unit, position and shift (FR-SCH-03 coverage vs
-- headcount). weekdays: ISO 1 = Monday … 7 = Sunday.
CREATE TABLE hris.staffing_requirements (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  org_unit_id        uuid NOT NULL REFERENCES hris.org_units (id),
  position_id        uuid REFERENCES hris.positions (id),
  shift_template_id  uuid REFERENCES hris.shift_templates (id),
  weekdays           int[] NOT NULL DEFAULT '{1,2,3,4,5,6,7}',
  min_staff          int NOT NULL CHECK (min_staff BETWEEN 1 AND 500),
  notes              text,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  CHECK (weekdays <@ '{1,2,3,4,5,6,7}'::int[] AND cardinality(weekdays) > 0)
);
SELECT platform.enable_property_rls('hris.staffing_requirements');
SELECT platform.add_touch_trigger('hris.staffing_requirements');

-- Shift Schedule (roster) of an org unit for a week or a month (FR-SCH-02).
-- Draft until published (FR-SCH-05); changes after publishing notify the
-- employees concerned.
CREATE TABLE hris.schedules (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  org_unit_id   uuid NOT NULL REFERENCES hris.org_units (id),
  name          text NOT NULL,
  period_start  date NOT NULL,
  period_end    date NOT NULL,
  status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'cancelled')),
  version       int NOT NULL DEFAULT 0,
  published_at  timestamptz,
  published_by  uuid,
  notes         text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  CHECK (period_end >= period_start AND period_end - period_start <= 41),
  CONSTRAINT schedules_no_overlap EXCLUDE USING gist (org_unit_id WITH =, daterange(period_start, period_end, '[]') WITH &&)
    WHERE (status <> 'cancelled')
);
SELECT platform.enable_property_rls('hris.schedules');
SELECT platform.add_touch_trigger('hris.schedules');
CREATE INDEX schedules_period_idx ON hris.schedules (property_id, period_start);

-- One assignment per employee and day: a shift (from a template) or a day
-- off. starts_at / ends_at are the instants of the local shift.
CREATE TABLE hris.shift_assignments (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  schedule_id        uuid NOT NULL REFERENCES hris.schedules (id) ON DELETE CASCADE,
  employee_id        uuid NOT NULL REFERENCES hris.employees (id),
  work_date          date NOT NULL,
  kind               text NOT NULL DEFAULT 'shift' CHECK (kind IN ('shift', 'off')),
  shift_template_id  uuid REFERENCES hris.shift_templates (id),
  starts_at          timestamptz,
  ends_at            timestamptz,
  break_minutes      int NOT NULL DEFAULT 0,
  work_minutes       int NOT NULL DEFAULT 0,
  workforce_role     text,
  status             text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'on_leave', 'cancelled')),
  swapped_from_id    uuid REFERENCES hris.employees (id),
  changed_after_publish boolean NOT NULL DEFAULT false,
  notes              text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  CHECK ((kind = 'shift' AND shift_template_id IS NOT NULL AND starts_at IS NOT NULL AND ends_at > starts_at)
         OR (kind = 'off' AND shift_template_id IS NULL))
);
SELECT platform.enable_property_rls('hris.shift_assignments');
SELECT platform.add_touch_trigger('hris.shift_assignments');
CREATE UNIQUE INDEX shift_assignments_day_uniq ON hris.shift_assignments (employee_id, work_date) WHERE status <> 'cancelled';
CREATE INDEX shift_assignments_schedule_idx ON hris.shift_assignments (schedule_id, work_date);
CREATE INDEX shift_assignments_date_idx ON hris.shift_assignments (property_id, work_date);

-- Approval trail of the time requests (leave, permission, overtime, shift
-- swap, attendance correction): the manager levels of the policy
-- (supervisor, department head, HR) run in HRIS; afterwards the approval
-- engine applies the workflow configured for the document type (none =
-- approved at once).
CREATE TABLE hris.request_approvals (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  request_kind          text NOT NULL CHECK (request_kind IN ('leave', 'permission', 'overtime', 'shift_swap', 'attendance_correction')),
  request_id            uuid NOT NULL,
  step_no               int NOT NULL,
  level                 text NOT NULL CHECK (level IN ('supervisor', 'department_head', 'hr', 'workflow')),
  approver_employee_id  uuid REFERENCES hris.employees (id),
  status                text NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting', 'pending', 'approved', 'rejected', 'skipped', 'cancelled')),
  decided_by            uuid,
  decided_at            timestamptz,
  note                  text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (request_kind, request_id, step_no)
);
SELECT platform.enable_property_rls('hris.request_approvals');
SELECT platform.add_touch_trigger('hris.request_approvals');
CREATE INDEX request_approvals_pending_idx ON hris.request_approvals (approver_employee_id) WHERE status = 'pending';

-- Shift swaps between employees (FR-SCH-06): the colleague accepts, then
-- the request is approved. counterpart_assignment_id NULL = the colleague
-- covers the shift (takes it without giving one back).
CREATE TABLE hris.shift_swaps (
  id                          uuid PRIMARY KEY,
  property_id                 uuid NOT NULL REFERENCES platform.properties (id),
  number                      text NOT NULL,
  requester_employee_id       uuid NOT NULL REFERENCES hris.employees (id),
  requester_assignment_id     uuid NOT NULL REFERENCES hris.shift_assignments (id),
  counterpart_employee_id     uuid NOT NULL REFERENCES hris.employees (id),
  counterpart_assignment_id   uuid REFERENCES hris.shift_assignments (id),
  reason                      text NOT NULL,
  status                      text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'submitted', 'approved', 'rejected', 'declined', 'cancelled')),
  counterpart_responded_at    timestamptz,
  approval_step               int,
  approval_request_id         uuid,
  decided_by                  uuid,
  decided_at                  timestamptz,
  decision_note               text,
  created_at                  timestamptz NOT NULL DEFAULT now(),
  created_by                  uuid,
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  updated_by                  uuid,
  UNIQUE (property_id, number),
  CHECK (requester_employee_id <> counterpart_employee_id)
);
SELECT platform.enable_property_rls('hris.shift_swaps');
SELECT platform.add_touch_trigger('hris.shift_swaps');

-- Geofences of the mobile GPS clock-in (FR-ATT-05, §16 #7: 300 m around
-- the club area).
CREATE TABLE hris.geofences (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name            text NOT NULL,
  latitude        numeric(9,6) NOT NULL CHECK (latitude BETWEEN -90 AND 90),
  longitude       numeric(9,6) NOT NULL CHECK (longitude BETWEEN -180 AND 180),
  radius_meters   int NOT NULL DEFAULT 300 CHECK (radius_meters BETWEEN 20 AND 20000),
  org_unit_codes  text[] NOT NULL DEFAULT '{}',
  notes           text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('hris.geofences');
SELECT platform.add_touch_trigger('hris.geofences');

-- Attendance devices (FR-ATT-01/02, FR-INT-P5-01, §16 #7): biometric
-- terminals (face recognition + fingerprint, through the bridge agent) and
-- Attendance Kiosks (a registered ops device with QR / PIN).
CREATE TABLE hris.attendance_devices (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  code                text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name                text NOT NULL,
  device_kind         text NOT NULL CHECK (device_kind IN ('biometric', 'kiosk')),
  location_point      text,
  methods             text[] NOT NULL DEFAULT '{}',
  vendor              text NOT NULL DEFAULT 'mock' CHECK (vendor IN ('mock', 'zkteco', 'other')),
  serial_no           text,
  platform_device_id  uuid REFERENCES platform.devices (id),
  bridge_agent_id     uuid REFERENCES platform.bridge_agents (id),
  geofence_id         uuid REFERENCES hris.geofences (id),
  last_event_at       timestamptz,
  last_sync_at        timestamptz,
  notes               text,
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  archived_at         timestamptz,
  UNIQUE (property_id, code),
  CHECK (methods <@ '{fingerprint,face_recognition,kiosk_qr,kiosk_pin}'::text[])
);
SELECT platform.enable_property_rls('hris.attendance_devices');
SELECT platform.add_touch_trigger('hris.attendance_devices');
CREATE UNIQUE INDEX attendance_devices_serial_uniq ON hris.attendance_devices (property_id, serial_no) WHERE serial_no IS NOT NULL AND archived_at IS NULL;
CREATE UNIQUE INDEX attendance_devices_platform_uniq ON hris.attendance_devices (platform_device_id) WHERE platform_device_id IS NOT NULL AND archived_at IS NULL;

-- Attendance profile of an employee: the user number on the biometric
-- devices, the kiosk PIN (hash), the QR badge and the written biometric
-- consent (FR-ATT-06). No biometric template is ever stored.
CREATE TABLE hris.attendance_profiles (
  employee_id            uuid PRIMARY KEY REFERENCES hris.employees (id) ON DELETE CASCADE,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  device_user_no         text NOT NULL,
  pin_hash               text,
  pin_set_at             timestamptz,
  failed_pin_count       int NOT NULL DEFAULT 0,
  locked_until           timestamptz,
  biometric_consent      boolean NOT NULL DEFAULT false,
  consent_signed_on      date,
  consent_file_id        uuid REFERENCES platform.files (id),
  consent_withdrawn_at   timestamptz,
  enrolled_at            timestamptz,
  removal_requested_at   timestamptz,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, device_user_no)
);
SELECT platform.enable_property_rls('hris.attendance_profiles');
SELECT platform.add_touch_trigger('hris.attendance_profiles');

-- Attendance day per employee (FR-ATT-03): matched against the published
-- shift; status Present / Late / Early Leave / Absent / On Leave / Off /
-- Holiday, or Scheduled while the day is still open.
CREATE TABLE hris.attendance_days (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  employee_id           uuid NOT NULL REFERENCES hris.employees (id),
  work_date             date NOT NULL,
  assignment_id         uuid REFERENCES hris.shift_assignments (id) ON DELETE SET NULL,
  shift_template_id     uuid REFERENCES hris.shift_templates (id),
  scheduled_start       timestamptz,
  scheduled_end         timestamptz,
  scheduled_minutes     int NOT NULL DEFAULT 0,
  first_in              timestamptz,
  last_out              timestamptz,
  worked_minutes        int NOT NULL DEFAULT 0,
  late_minutes          int NOT NULL DEFAULT 0,
  early_leave_minutes   int NOT NULL DEFAULT 0,
  overtime_minutes      int NOT NULL DEFAULT 0,
  status                text NOT NULL DEFAULT 'scheduled'
                          CHECK (status IN ('scheduled', 'present', 'late', 'early_leave', 'absent', 'on_leave', 'off', 'holiday')),
  day_kind              text NOT NULL DEFAULT 'workday' CHECK (day_kind IN ('workday', 'rest_day', 'shortest_day')),
  holiday_name          text,
  leave_request_id      uuid,
  leave_type            text,
  leave_paid            boolean,
  leave_fraction        numeric(3,2) NOT NULL DEFAULT 0,
  permission_minutes    int NOT NULL DEFAULT 0,
  unpaid_permission_minutes int NOT NULL DEFAULT 0,
  flags                 text[] NOT NULL DEFAULT '{}',
  finalized             boolean NOT NULL DEFAULT false,
  locked                boolean NOT NULL DEFAULT false,
  computed_at           timestamptz NOT NULL DEFAULT now(),
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (employee_id, work_date)
);
SELECT platform.enable_property_rls('hris.attendance_days');
SELECT platform.add_touch_trigger('hris.attendance_days');
CREATE INDEX attendance_days_date_idx ON hris.attendance_days (property_id, work_date);

-- Attendance events (clock in / out) from ESS (mobile GPS), the kiosk,
-- biometric devices and approved corrections. client_event_id makes offline
-- and device resubmissions idempotent (FR-ATT-07).
CREATE TABLE hris.attendance_events (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  employee_id         uuid NOT NULL REFERENCES hris.employees (id),
  work_date           date NOT NULL,
  direction           text NOT NULL CHECK (direction IN ('in', 'out')),
  occurred_at         timestamptz NOT NULL,
  method              text NOT NULL CHECK (method IN ('mobile_gps', 'kiosk_qr', 'kiosk_pin', 'fingerprint', 'face_recognition', 'manual', 'correction')),
  source              text NOT NULL CHECK (source IN ('ess', 'kiosk', 'device', 'hr', 'correction')),
  device_id           uuid REFERENCES hris.attendance_devices (id),
  latitude            numeric(9,6),
  longitude           numeric(9,6),
  accuracy_meters     numeric(8,1),
  distance_meters     numeric(10,1),
  geofence_id         uuid REFERENCES hris.geofences (id),
  within_geofence     boolean,
  flags               text[] NOT NULL DEFAULT '{}',
  review_status       text NOT NULL DEFAULT 'not_required' CHECK (review_status IN ('not_required', 'pending', 'accepted', 'rejected')),
  reviewed_by         uuid,
  reviewed_at         timestamptz,
  review_note         text,
  client_event_id     text,
  offline             boolean NOT NULL DEFAULT false,
  correction_id       uuid,
  voided_at           timestamptz,
  note                text,
  recorded_by         uuid,
  received_at         timestamptz NOT NULL DEFAULT now(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('hris.attendance_events');
SELECT platform.add_touch_trigger('hris.attendance_events');
CREATE UNIQUE INDEX attendance_events_client_uniq ON hris.attendance_events (employee_id, client_event_id) WHERE client_event_id IS NOT NULL;
CREATE INDEX attendance_events_day_idx ON hris.attendance_events (employee_id, work_date);
CREATE INDEX attendance_events_review_idx ON hris.attendance_events (property_id) WHERE review_status = 'pending';

-- Attendance corrections with reason and approval (FR-ATT-04).
CREATE TABLE hris.attendance_corrections (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  employee_id           uuid NOT NULL REFERENCES hris.employees (id),
  work_date             date NOT NULL,
  clock_in              timestamptz,
  clock_out             timestamptz,
  reason                text NOT NULL,
  attachment_file_id    uuid REFERENCES platform.files (id),
  status                text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'approved', 'rejected', 'cancelled')),
  approval_step         int,
  approval_request_id   uuid,
  decided_by            uuid,
  decided_at            timestamptz,
  decision_note         text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number),
  CHECK (clock_in IS NOT NULL OR clock_out IS NOT NULL),
  CHECK (clock_in IS NULL OR clock_out IS NULL OR clock_out > clock_in)
);
SELECT platform.enable_property_rls('hris.attendance_corrections');
SELECT platform.add_touch_trigger('hris.attendance_corrections');
CREATE INDEX attendance_corrections_employee_idx ON hris.attendance_corrections (employee_id, work_date);

-- Leave balances per employee, leave type and calendar year (FR-LVE-01,
-- §16 #3: 12 days after 12 months, carry-over at most 6 days lapsing on
-- 31 March). available = entitled + carried_over − carried_expired +
-- adjusted − used.
CREATE TABLE hris.leave_balances (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  employee_id            uuid NOT NULL REFERENCES hris.employees (id),
  leave_type             text NOT NULL,
  year                   int NOT NULL CHECK (year BETWEEN 2000 AND 2100),
  entitled               numeric(6,2) NOT NULL DEFAULT 0,
  carried_over           numeric(6,2) NOT NULL DEFAULT 0,
  carry_over_expires_on  date,
  used_carried           numeric(6,2) NOT NULL DEFAULT 0,
  carried_expired        numeric(6,2) NOT NULL DEFAULT 0,
  adjusted               numeric(6,2) NOT NULL DEFAULT 0,
  used                   numeric(6,2) NOT NULL DEFAULT 0,
  granted_on             date,
  policy_version         int NOT NULL DEFAULT 0,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  UNIQUE (employee_id, leave_type, year),
  CHECK (used_carried <= used AND used_carried + carried_expired <= carried_over)
);
SELECT platform.enable_property_rls('hris.leave_balances');
SELECT platform.add_touch_trigger('hris.leave_balances');

-- Ledger of every balance movement (accrual, carry-over, expiry, usage,
-- reversal, adjustment, import).
CREATE TABLE hris.leave_balance_entries (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  balance_id        uuid NOT NULL REFERENCES hris.leave_balances (id) ON DELETE CASCADE,
  employee_id       uuid NOT NULL REFERENCES hris.employees (id),
  kind              text NOT NULL CHECK (kind IN ('accrual', 'carry_over', 'expiry', 'usage', 'reversal', 'adjustment', 'import')),
  days              numeric(6,2) NOT NULL,
  leave_request_id  uuid,
  note              text,
  created_by        uuid,
  created_at        timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('hris.leave_balance_entries');
CREATE INDEX leave_balance_entries_balance_idx ON hris.leave_balance_entries (balance_id, created_at);

-- Leave requests (FR-LVE-02): the balance is reduced when approved.
CREATE TABLE hris.leave_requests (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  employee_id           uuid NOT NULL REFERENCES hris.employees (id),
  leave_type            text NOT NULL,
  start_date            date NOT NULL,
  end_date              date NOT NULL,
  half_day              text CHECK (half_day IS NULL OR half_day IN ('am', 'pm')),
  days                  numeric(6,2) NOT NULL CHECK (days > 0),
  carried_days          numeric(6,2) NOT NULL DEFAULT 0,
  balance_year          int,
  paid                  boolean NOT NULL DEFAULT true,
  reason                text,
  document_file_id      uuid REFERENCES platform.files (id),
  schedule_conflicts    int NOT NULL DEFAULT 0,
  status                text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'approved', 'rejected', 'cancelled')),
  approval_step         int,
  approval_request_id   uuid,
  decided_by            uuid,
  decided_at            timestamptz,
  decision_note         text,
  cancelled_at          timestamptz,
  policy_version        int NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number),
  CHECK (end_date >= start_date AND end_date - start_date <= 366),
  CHECK (half_day IS NULL OR start_date = end_date)
);
SELECT platform.enable_property_rls('hris.leave_requests');
SELECT platform.add_touch_trigger('hris.leave_requests');
CREATE INDEX leave_requests_employee_idx ON hris.leave_requests (employee_id, start_date);
CREATE INDEX leave_requests_period_idx ON hris.leave_requests (property_id, start_date, end_date) WHERE status IN ('submitted', 'approved');

-- Permission (izin) by hours (FR-LVE-02): late arrival, early leave,
-- personal matters.
CREATE TABLE hris.permission_requests (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  employee_id           uuid NOT NULL REFERENCES hris.employees (id),
  permission_type       text NOT NULL,
  work_date             date NOT NULL,
  starts_at             timestamptz NOT NULL,
  ends_at               timestamptz NOT NULL,
  minutes               int NOT NULL CHECK (minutes > 0),
  paid                  boolean NOT NULL DEFAULT true,
  reason                text NOT NULL,
  status                text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'approved', 'rejected', 'cancelled')),
  approval_step         int,
  approval_request_id   uuid,
  decided_by            uuid,
  decided_at            timestamptz,
  decision_note         text,
  policy_version        int NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number),
  CHECK (ends_at > starts_at)
);
SELECT platform.enable_property_rls('hris.permission_requests');
SELECT platform.add_touch_trigger('hris.permission_requests');
CREATE INDEX permission_requests_employee_idx ON hris.permission_requests (employee_id, work_date);

-- Overtime requests (FR-OVT-01–04): before the work or afterwards with a
-- reason; payable hours = approved hours capped by the attendance of the
-- day, split into multiplier tiers of the Overtime Policy (1/173, PP
-- 35/2021).
CREATE TABLE hris.overtime_requests (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  employee_id           uuid NOT NULL REFERENCES hris.employees (id),
  work_date             date NOT NULL,
  starts_at             timestamptz NOT NULL,
  ends_at               timestamptz NOT NULL,
  hours                 numeric(5,2) NOT NULL CHECK (hours > 0),
  timing                text NOT NULL DEFAULT 'before' CHECK (timing IN ('before', 'after')),
  reason                text NOT NULL,
  day_kind              text NOT NULL DEFAULT 'workday' CHECK (day_kind IN ('workday', 'rest_day', 'shortest_day')),
  work_week_days        int NOT NULL DEFAULT 5 CHECK (work_week_days IN (5, 6)),
  actual_hours          numeric(5,2),
  payable_hours         numeric(5,2) NOT NULL DEFAULT 0,
  multiplied_hours      numeric(6,2) NOT NULL DEFAULT 0,
  tiers                 jsonb NOT NULL DEFAULT '[]'::jsonb,
  status                text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'approved', 'rejected', 'cancelled')),
  approval_step         int,
  approval_request_id   uuid,
  decided_by            uuid,
  decided_at            timestamptz,
  decision_note         text,
  policy_version        int NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number),
  CHECK (ends_at > starts_at)
);
SELECT platform.enable_property_rls('hris.overtime_requests');
SELECT platform.add_touch_trigger('hris.overtime_requests');
CREATE INDEX overtime_requests_employee_idx ON hris.overtime_requests (employee_id, work_date);
CREATE INDEX overtime_requests_period_idx ON hris.overtime_requests (property_id, work_date) WHERE status = 'approved';

-- Payroll period locks: attendance, leave, permission and overtime of a
-- locked period no longer change (payroll calculated / approved).
CREATE TABLE hris.time_locks (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  period_start  date NOT NULL,
  period_end    date NOT NULL,
  reference     text NOT NULL,
  status        text NOT NULL DEFAULT 'locked' CHECK (status IN ('locked', 'released')),
  locked_by     uuid,
  locked_at     timestamptz NOT NULL DEFAULT now(),
  released_by   uuid,
  released_at   timestamptz,
  release_note  text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CHECK (period_end >= period_start)
);
SELECT platform.enable_property_rls('hris.time_locks');
SELECT platform.add_touch_trigger('hris.time_locks');

-- The report role never reads the kiosk PIN, the consent files or the GPS
-- positions of employees (UU PDP). Per-area hardening function of time &
-- attendance: hris.harden_report_role() (Core HR) calls the per-area
-- functions once the areas are merged; until then this migration calls it
-- itself after the Core HR hardening.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hris.harden_report_role_time() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
BEGIN
  IF rep IS NULL OR rep = '' THEN
    RETURN;
  END IF;
  EXECUTE format('REVOKE SELECT ON hris.attendance_profiles, hris.attendance_events FROM %I', rep);
  EXECUTE format('GRANT SELECT (employee_id, property_id, device_user_no, biometric_consent, consent_signed_on, consent_withdrawn_at, enrolled_at,
    created_at, updated_at) ON hris.attendance_profiles TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, employee_id, work_date, direction, occurred_at, method, source, device_id, accuracy_meters,
    distance_meters, geofence_id, within_geofence, flags, review_status, reviewed_at, offline, correction_id, voided_at, received_at, created_at,
    updated_at) ON hris.attendance_events TO %I', rep);
END $$;
-- +goose StatementEnd

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();
SELECT hris.harden_report_role_time();

-- +goose Down
DROP TABLE hris.time_locks, hris.overtime_requests, hris.permission_requests, hris.leave_requests, hris.leave_balance_entries, hris.leave_balances,
  hris.attendance_corrections, hris.attendance_events, hris.attendance_days, hris.attendance_profiles, hris.attendance_devices, hris.geofences,
  hris.shift_swaps, hris.request_approvals, hris.shift_assignments, hris.schedules, hris.staffing_requirements, hris.shift_templates, hris.holidays;
DROP FUNCTION hris.harden_report_role_time();
