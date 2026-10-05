# DPIA — Release 5: biometrics, salary data and advanced CRM profiling (PRD P5 FR-REL-P5-06)

Data Protection Impact Assessment under UU No. 27/2022 on Personal Data Protection (UU PDP), Art. 34 (assessment
required for specific personal data, large-scale processing, profiling and automated decisions). Controller: the club
(Modern Golf & Country Club); processor: OneClub (hosting, support). This assessment covers what Release 5 adds; it is
reviewed by the club's data protection officer / legal counsel and signed before go-live (§6 below) and again whenever
one of the processing activities changes.

## 1. Processing activities

| # | Activity | Data subjects | Personal data | Specific data (UU PDP Art. 4(2)) | Purpose | Legal basis |
|---|---|---|---|---|---|---|
| A | Attendance by face recognition / fingerprint (FR-ATT-01, FR-INT-P5-01) | employees, partner caddies and instructors (FR-ATT-08) | device user number, clock events (time, device, method) | **biometric data** — templates on the device only | working time, overtime, payroll inputs, caddy queue | written explicit consent (FR-ATT-06); employment contract for the attendance itself |
| B | Mobile GPS clock-in (FR-ATT-05) | field staff (§16 #7) | position, accuracy and distance at clock-in / out only | — (location) | geofence check | employment contract; legitimate interest (fraud prevention) |
| C | Payroll and partner payouts (EP-09–15, payroll / payouts areas) | employees, partners | salary, allowances, deductions, PTKP, NPWP, NIK, BPJS numbers, bank account, payslips | **financial data**; health data of sick leave certificates | pay, tax and social security obligations | legal obligation (PPh 21, BPJS), contract |
| D | HR records (EP-01–05) | employees, candidates | identity, contracts, documents (KTP, KK, diplomas, warning letters, medical), reviews, training | health data (medical documents), children's data (KK, PTKP dependants) | employment management, recruitment | contract, legal obligation, consent (careers page, talent pool) |
| E | Advanced CRM profiling (EP-17–20) | customers, members | spend and visits per line, RFM group, VIP level, tier, journey enrolments, NPS answers | — | segmentation, retention journeys, loyalty tiers | consent (profiling consent of the Member App, marketing opt-in); legitimate interest for transactional journeys (renewal reminders) |
| F | Push notifications (FR-INT-P5-05) | staff, members | browser push endpoint and keys per device | — | ESS and Member App notifications | consent (the user turns push on, per device) |

## 2. Necessity and proportionality

* **Biometrics.** OneClub never receives or stores a template or image: the vendor device keeps the template; OneClub keeps
  the device user number, the consent (date, scanned form) and the clock events (`hris.attendance_profiles`,
  `hris.partner_attendance_profiles`, no template column — enforced by `TestP5HRTimeAttendance`). A non-biometric
  alternative always exists: Attendance Kiosk with QR / PIN (FR-ATT-02), mobile GPS for field staff. Consent withdrawal
  removes the person from the devices (`delete_users` command, §16 #10: templates deleted ≤ 30 days after leaving).
* **Location.** Only the position of a clock-in / out is recorded — no tracking between events; staff outside the
  geofence are flagged for review, not refused silently.
* **Salary.** Visible only to payroll roles (`hris.contract.view_salary`, payroll permissions); masked elsewhere and in
  exports; reports with salary data only for payroll roles (FR-RPT-P5-04); the service worker never caches payroll data
  (`web/apps/staff/src/sw-cache.ts`); push notices of payslips carry no amounts.
* **Profiling.** RFM / VIP / tiers use transactions the club already processes; marketing journeys require marketing
  consent and respect suppression, a frequency cap (2 / week, 6 / month) and quiet hours 21:00–08:00 (§16 #15); no
  automated decision with legal effect (a tier changes benefits only; manual override with approval).

## 3. Risks and measures

| Risk | Likelihood | Impact | Measures (implemented) | Residual |
|---|---|---|---|---|
| Biometric template leak | low | high | templates never leave the device; vendor device hardening and network segmentation (club IT); bridge agent token per property; no template in API / DB | low |
| Coerced biometric consent | medium | medium | written consent recorded per person; QR / PIN / GPS alternative offered and documented in the HR Policies | low |
| Salary data disclosure (insider, misconfigured role) | medium | high | permission per field / report, masking, audit log of reads and exports, two-person review of payroll rules, report-role column revokes, CODEOWNERS on `internal/hris/**` | low |
| Payslip exposure on a shared or lost phone | medium | medium | personal login only, MFA available, no offline cache of payroll data, sessions revocable, PDF protected | low |
| Location over-collection | low | medium | position at clock events only, geofence radius configurable (300 m), positions not readable by the report role | low |
| Profiling beyond consent | medium | medium | profiling consent and marketing opt-in checked at every journey step; erasure / opt-out honoured; analytics recomputed without erased customers | low |
| Retention exceeded | medium | medium | §16 #10: rejected applicants 1 year (talent pool consent) then erased; payroll, tax, BPJS and supporting attendance 10 years; biometrics none in OneClub; leads 2 years | low |
| Migration files with personal data left on disk | medium | high | `oneclub import hris` reads operator files once; runbook requires deleting the export files after reconciliation sign-off | medium → low after the runbook step |

## 4. Data subject rights

Access and rectification through ESS (profile change with HR verification, FR-ESS-06) and the Member App; erasure of
candidates (`POST /api/v1/hris/candidates/{id}:erase`) and customers (CRM erase, P2); objection to profiling and marketing
in the Member App (profiling consent, unsubscribe); withdrawal of biometric consent by HR on request
(`…/attendance-profiles/{id}:consent`, `…/partner-attendance-profiles/{id}:consent`); push notifications off per device.
Retention exceptions (tax law) are explained in the privacy notice.

## 5. Transfers and processors

Hosting in Indonesia (VPS, PRD P0); e-mail / WhatsApp BSP and Web Push services (Google FCM, Apple, Mozilla) receive
only the rendered notification; payment and bank files go to the club's banks; e-Bupot / BPJS files to DJP / BPJS by the
club. Processor agreements with OneClub and the BSP are in place before go-live.

## 6. Sign-off

| Role | Name | Decision | Date |
|---|---|---|---|
| Data Protection Officer / Legal counsel (club) | | | |
| HR Manager | | | |
| General Manager | | | |
| OneClub delivery lead | | | |
