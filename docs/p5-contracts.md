# P5 module contracts (PRD P5 §5.4.2, §11)

PRD P5 People & Advanced Enterprise adds the `hris` module (layer 4, back office) and extends `crm`, `reporting`,
`commercial/package` and `golf/tournament` additively. Business-line modules never call `hris`; `hris` reads caddy
settlements, instructor fees and commissions through public interfaces or events, and posts to `accounting` through
events (Tech Doc §4.2–4.3). Contracts are **additive only**: an area that publishes or consumes an event adds a note here.

| # | Contract | Owner | Used by P5 for |
|---|---|---|---|
| H1 | `golf.caddy_settlement_approved` (caddy settlement & tip) | golf (P2) | Caddy payout run (EP-13) |
| H2 | `sportclub.instructor_fee_approved` | sportclub (P2) | Instructor payout run (EP-14) |
| H3 | `crm.commission_approved` (commission statement) | crm/sales (P3) | Commission payout (EP-12) |
| H4 | Service charge pool & distribution basis per department | billing / accounting (P4) | Service charge distribution (EP-11) |
| H5 | Payroll & payout posting rules, AP / bank payment | accounting (P4) | Payroll journal, PPh 21 / BPJS liabilities, payment (EP-15) |
| H6 | `hris` module list and HR role / policy seed for provisioning and feature tiers | hris (P5) → P6 | SaaS packages (P6) |
| H7 | `hris.employee_terminated`, `hris.certification_expired` | hris (P5) | IAM deactivates the user; golf / sportclub block assignments of staff without a valid certification |

## Events published in P5 (§11)

`hris.employee_hired`, `hris.employee_terminated`, `hris.contract_expiring`, `hris.certification_expired`,
`hris.schedule_published`, `hris.attendance_recorded`, `hris.leave_approved`, `hris.overtime_approved`,
`hris.payroll_calculated`, `hris.payroll_posted`, `hris.payroll_paid`, `hris.payout_posted`, `hris.payout_paid`,
`crm.journey_step_executed`, `crm.tier_evaluated`, `crm.reward_issued`, `golf.series_standing_updated`.

Each area documents the payload of the events it publishes below (JSON shape, like docs/p3-p4-contracts.md).

## BI (EP-21, EP-27) — registry for the P5 areas

Owner: reporting (P5 BI). The BI area publishes and consumes no outbox event: the analytics store (schema `analytics`)
is refreshed by its own jobs (every 10 minutes for the last days, nightly backfill of the BI Policies) from the
KPI definitions of the domain dashboards, so an executive KPI always equals its source dashboard / report.

The other P5 areas plug into BI from an `init()` in their own `internal/reporting/p5_<area>.go` (package `reporting`,
reading only `reporting.*` views — never another module's schema directly):

| Function | Use |
|---|---|
| `RegisterHRKPI(HRKPI{Key, Label, Unit, Kind, Direction, SQL, Breakdown, Permission, Module, Executive, Report})` | A KPI of **HR Performance** (FR-RPT-P5-01). The keys `headcount`, `attendance_rate`, `overtime_hours`, `payroll_cost`, `turnover`, `certification_compliance` exist as defaults (headcount from the P0 employee master; the others "coming soon") — registering the same key replaces the default. `Executive: true` adds it to the HR domain of the Executive Overview and to the analytics store. SQL follows the dashboard convention: one row, one text column, `$1` from, `$2` to (inclusive), `$3` time zone. `Permission` hides it from users without that permission (e.g. Payroll Cost → payroll roles); `Module: "hris"` hides it while HRIS is disabled. |
| `RegisterP5Report(report *Report, roles ...string)` | A report (HR Reports FR-RPT-P5-02, CRM / package / tournament reports FR-RPT-P5-03). BI contributes its permission (`reporting.<code with _>.view` when built with `sqlReport`) and grants it, with `reporting.report.view` and `reporting.export.create`, to the roles given — salary reports to payroll roles only (FR-RPT-P5-04). The report joins the registry, the CSV / XLSX / PDF export and the scheduled reports. Do **not** contribute the same permission again from the area's own catalogue contribution (duplicate permissions fail the catalogue build). |
| `RegisterDataset(Dataset{Code, Name, Permission, Module, Source, DateExpr, Dimensions, Metrics})` | A dataset of the self-service report builder (FR-BI-06) with its own permission. |

KPI kinds: `flow` (additive; monthly targets pro-rate within the running month), `rate`, `stock` (balance at the end
date), `current` (state now; history accrues day by day in the analytics store).

KPI targets: approval document type `reporting.kpi_target_plan` (attributes `year`, `version`); without a workflow a
submitted plan is approved at once; approving a revision supersedes the plan in force of the year.

Scheduled reports notify with the template event `reporting.scheduled_report_ready` (in-app, e-mail, WhatsApp; ID/EN).

### Advanced Package & Tournament (EP-22–23)

**Published — `golf.series_standing_updated`** (aggregate `golf.tournament_series`), after an event of an active series is counted (the
tournament was finalized, an event added / reweighted / removed), on activation, recalculation and completion (`final = true`):

```json
{ "seriesId": "uuid", "code": "CCS-2026", "name": "Club Championship Series", "season": 2026, "status": "active", "final": false,
  "tournamentId": "uuid|null", "players": 24,
  "leaders": [{ "rank": 1, "positionLabel": "1", "customerId": "uuid|null", "playerName": "…", "points": "45", "events": 3 }] }
```

`leaders` are the first five ranked players (names unmasked: internal consumers only; public channels mask players without consent).

**Consumed:**

| Event | Subscriber | Use |
|---|---|---|
| `golf.tournament_finalized` (P3) | `golf.tournament_series_points` | Freeze the team results of a team tournament, count the tournament in its active series (points per player; team members get the team's position) |
| `commercial.package_booked`, `commercial.package_consumed`, `commercial.package_cancelled` (P3, K3/K6) | `commercial.package_costs_*` | Refresh the cost lines of the booking (BOM COGS estimated / actual at the P4 inventory cost, cost rules) for the Package Profitability |

**Hooks (additive, inside P3 code):** the P3 package booking and availability call the P5 booking rules (choice groups, sequence & time
gap, service windows, allotment / blackout / time blocks, inventory check of large bookings, payment template of the package type);
the P3 tournament registration takes the P5 registration category (quota per member / guest / sponsor invitation) and the category /
early-bird fees, and the waitlist promotion skips players whose category is full (`golf.tournament_category_full`).

## Core HR & ESS (`hris`, EP-01/02/04/16/24/28) — notes for the other P5 areas

**Packages.** `internal/hris` (root) is the public API other P5 areas (time, payroll, payouts, BI) import; it has no
HTTP. `internal/hris/corehr` implements Core HR and ESS (HTTP, jobs, import, demo); other `hris` sub-packages import
the root only. Business-line modules (golf, sportclub, …) never import `hris`: internal/app wires hooks.

**Data ownership (expand → migrate → contract).** `hris.org_units` and `hris.employees` are the master; they keep the
ids of `platform.departments` / `platform.employees`, which stay as a facade kept in sync both ways by triggers
(`hris/00002_core_hr.sql`, guarded by `pg_trigger_depth()`). P0 code and the BI view `reporting.bi_employees` (reads
`platform.employees`) keep working unchanged. A later `hris` migration that adds sensitive columns must end with
`SELECT hris.harden_report_role();` (revokes NIK, NPWP, salary, bank and data-change columns from the report role;
reporting views are `security_invoker`).

**Lookups** (`lookup.go`, all take a `dbtx.Querier`, RLS applies): `EmployeeByID`, `EmployeeByUser` (nil when the user
has no profile), `EmployeeByNo`, `Employees(EmployeeFilter{…})`, `Employee.EmployedOn(day)`, `Supervisor` (position
line, else org-unit head), `Team(managerID)` (recursive), `IsManagerOf`, `OrgUnits` / `OrgUnitByCode`, `Grades`,
`ContractAt(employee, day)` + `Contract.FixedWage()` (base + fixed allowances), `WarningLevel(employee, day)` (active
SP level), `PayrollProfileOf` (PTKP, NPWP, BPJS numbers, bank account — payroll only). `EmployeeSelect` is the shared
SELECT for custom queries.

**HR policies** (`policies.go`, `rules.RegisterPolicy`, versioned with effective date, edited under Settings):
`hris.hr_configuration`, `hris.payroll_configuration`, `hris.attendance_configuration`, `hris.leave_policy`,
`hris.overtime_policy`, `hris.attendance_policy`, `hris.service_charge_policy` (`hris.PolicyCodes`, H6). Defaults
(`New*`) follow §16 #3/#5 (PKWT ≤ 5 years, probation 3 months PKWTT only, 12 days leave after 12 months, overtime
multipliers of PP 35/2021, service charge 95/5, PPh 21 TER / Pasal 17, BPJS rates, THR, severance). Load with
`hris.LoadX(ctx, q, property, hris.PolicyTime(day))` — `PolicyTime` gives "now" for today (a version made today
applies at once) and the end of the day otherwise. Calculators (`calc.go`): `OvertimePolicy.Pay/Multiplied/Steps`,
`PayrollConfiguration.THRAmount/SeverancePay/TERRate`, `ProgressiveTax`, `Contribution`,
`HRConfiguration.PKWTCompensation`, `ServiceMonths`.

**Certifications (H7).** `CheckEmployee(ctx, q, employeeID, role, day)`, `CheckPartner(ctx, q, property,
HolderCaddy|HolderInstructor, partnerID, role, day)` and `PartnersWithGaps(...)` return the mandatory types of a
workforce role (`WorkforceRoles`) and the gaps; `CertificationCheck.Err(name)` is the 409 refusal. The mode is
`HRConfiguration.certificationEnforcement` (`expired` default / `required` / `off`). internal/app wires
`golf.SetCaddyCertificationCheck` (Caddy Queue shows uncertified caddies as not available; assignment refused with
`caddy_not_certified`) and `sportclub.SetInstructorCertificationCheck` (session generation refused) only when `hris`
is enabled; without the hook nothing is checked.

**ESS registry (EP-16, §16 #6).** Server: `hris.RegisterESSSection(hris.ESSSection{Key, Label, LabelID, Icon, Path:
"/ops/ess/<key>", Permission (default hris.ess.use), Module, Manager, Order, Offline})` from your package's `init`.
`GET /api/v1/ess/me` returns the profile and the sections the user may open (module enabled + permission; manager
sections only for team leads). Core sections: profile 10, documents 70, training 80, team 100; time/payroll take
the orders in between (e.g. schedule 20, attendance 30, leave 40, overtime 50, payslips 60). Staff App:
`registerEssSection(key, view)` exported by `web/apps/staff/src/p5/hr.tsx` maps the key to its screen (routes
`/ops/ess/:section` and Back Office `/ess/:section` already exist). Permissions: `hris.ess.use` (all staff roles),
`hris.team.view`, `hris.team.approve` (department heads approving team leave / overtime / swaps). Role
`department_head` is new; `employee_self_service` now also grants `hris.ess.use`.

**Events published** (outbox; aggregates `hris.employee`, `hris.contract`, `hris.certification`):

```jsonc
// hris.employee_hired — when an employee is created
{ "employeeId": "uuid", "employeeNo": "EMP-00041", "fullName": "…", "propertyId": "uuid", "orgUnitId": "uuid|null",
  "positionId": "uuid|null", "gradeId": "uuid|null", "employmentStatus": "probation|contract|permanent",
  "workerCategory": "regular|daily|intern", "joinDate": "2026-10-05" }
// hris.employee_terminated (H7) — on the effective date (daily job, or at once when the date is today or past)
{ "employeeId": "uuid", "employeeNo": "…", "fullName": "…", "propertyId": "uuid", "userId": "uuid|null",
  "effectiveDate": "2026-10-31", "terminationType": "resigned|terminated|contract_ended|retired|deceased",
  "employmentStatus": "resigned|terminated", "reason": "…", "supervisorId": "uuid|null", "supervisorUserId": "uuid|null" }
// hris.contract_expiring — at each reminder day of the HR Configuration (H-30, H-7)
{ "contractId": "uuid", "number": "…", "employeeId": "uuid", "employeeNo": "…", "fullName": "…", "propertyId": "uuid",
  "contractType": "pkwt|pkwtt", "endDate": "2026-11-04", "daysRemaining": 30 }
// hris.certification_expired (H7) — daily job (00:05), once per certificate past expiry without a valid renewal
{ "certificationId": "uuid", "certificationTypeId": "uuid", "certificationTypeCode": "CADDY", "certificationTypeName": "…",
  "propertyId": "uuid", "holderKind": "employee|caddy|instructor", "employeeId": "uuid|null", "partnerId": "uuid|null",
  "holderName": "…", "expiresOn": "2026-10-04", "mandatoryFor": ["caddy"] }
```

Consumers wired in internal/app (`p5_hr.go`): `hris.employee_terminated` → `iam.DeactivateEmployeeUser` (user
Inactive, sessions revoked) and `approval.Engine.ReassignUser` (pending approval steps move to the supervisor's
user). Employee logins are created with `iam.ProvisionEmployeeUser` (HR action "Create login").

**Reports & KPIs.** `reporting/00020_p5_hr_core.sql` adds `reporting.hr_employees`, `hr_contracts`,
`hr_certifications`, `hr_training`, `hr_certification_requirements`. Reports `hris.headcount`, `hris.turnover`,
`hris.contract_expiry`, `hris.certification_expiry`, `hris.training` (`reporting.HRCoreReports()`) and KPI definitions
`headcount`, `turnover`, `certification_compliance` (`reporting.HRCoreKPIs`) are shaped for the BI registry. On this
branch they are appended to the report list with permissions from `reporting.HRCoreContribution()`; after merging
with BI (f416980) `p5_hr.go` init switches to `RegisterP5Report` / `RegisterHRKPI` and drops `HRCoreContribution()`
(otherwise duplicate permissions).

**Import (EP-28).** `oneclub import hris -property MAIN --employees F --contracts F --certifications F [--dry-run]`
or `POST /api/v1/hris/imports` (same CSV columns, `corehr.ImportColumns`). Repeatable: employees are upserted by
employee number; contracts and certifications skip duplicates.

## CRM (EP-17 – EP-20)

Money is a decimal string, dates `YYYY-MM-DD`; `propertyId` is on the event envelope.

### `crm.tier_evaluated` — one per account per tier evaluation run (annual, periodic or grace review)
```json
{ "evaluationId": "uuid", "evaluationNumber": "TEV-2026-00001", "kind": "annual | periodic | grace_review",
  "accountId": "uuid", "customerId": "uuid", "fromTierId": "uuid|null", "qualifiedTierId": "uuid|null",
  "toTierId": "uuid|null", "outcome": "upgraded | retained | grace_started | in_grace | downgraded | locked",
  "spendBasis": "25000000", "pointsBasis": 1200, "graceUntil": "2027-04-01|null",
  "windowFrom": "2025-10-05", "windowTo": "2026-10-05" }
```
A tier change still publishes the P3 `crm.tier_changed` (unchanged).

### `crm.reward_issued` — automatic or manual reward issue (top spender programme, journey, staff)
```json
{ "issueId": "uuid", "number": "RWI-2026-00001", "customerId": "uuid", "rewardId": "uuid", "rewardCode": "GOLD-FNB",
  "rewardType": "voucher | merchandise | service | other", "quantity": 1,
  "source": "top_spender | journey | staff", "sourceId": "uuid|null", "sourceRef": "text|null",
  "period": "2026-10|null", "unitCost": "100000", "totalCost": "100000", "voucherCodes": ["VC-..."] }
```

### `crm.journey_step_executed` — every executed journey step (also control-group and skipped outcomes; quiet hours defer a message, they do not skip it)
```json
{ "journeyId": "uuid", "journeyCode": "JRN-RENEWAL", "enrollmentId": "uuid", "customerId": "uuid",
  "cohort": "treatment | control", "stepKey": "remind_30", "stepType": "message | wait | condition | voucher | points | reward | sales_task | tag | exit",
  "outcome": "sent | skipped_no_consent | skipped_suppressed | skipped_no_contact | skipped_frequency_cap | control | waiting | condition_true | condition_false | issued | skipped_budget | failed | created | tagged | exited",
  "channel": "email | whatsapp | in_app | null", "variant": "A | B | null", "eventId": "uuid", "category": "marketing | transactional" }
```

### Consumed by CRM journeys
`membership.activated`, `membership.renewed`, `banquet.event_completed`, `banquet.event_confirmed`, `billing.payment_settled`,
`golf.booking_confirmed`, `golf.booking_cancelled`, `golf.round_finished`, `crm.tier_changed`, `crm.ticket_resolved`,
`commercial.package_booked` (subscriber names `crm.journey.<event>`; the customer is read from `customerId` /
`billingCustomerId` / `memberCustomerId` in the payload). They start event-triggered journeys, count goal conversions and apply exit rules.

### Hook: tier booking window (golf, FR-LOY-P5-02)
`golf.SetTierBookingWindow(bonus, maxBonus)` is wired by `internal/app` with `loyalty.BookingWindowBonus` (extra days of the
customer's tier) and `loyalty.MaxBookingWindowBonus` (largest bonus of the property, tee sheet horizon). Golf never imports crm;
without the hook the windows are unchanged. Applied to Member App holds, staff member bookings and member players.

### Read models (reporting schema, security_invoker)
`reporting.crm_tier_benefits` (customer → tier benefits: points multiplier, booking window days, F&B discount %, event access,
priority service — the F&B discount is exposed for POS/commercial to apply), `crm_tier_accounts`, `crm_tier_evaluations`,
`crm_loyalty_issues`, `crm_reward_redemption_costs`, `crm_journey_events`, `crm_journey_enrollments`, `crm_rfm_scores`, `crm_vip`,
`crm_sales_owner_commissions`, `crm_banquet_event_customers`. CRM reports (`crm.journey_performance`, `crm.loyalty_tier`,
`crm.tier_evaluation`, `crm.reward_redemption`, `crm.loyalty_cost`, `crm.nps_analytics`, `crm.complaint_sla`,
`crm.sales_performance`, `crm.rfm_segment`, `crm.vip_customers`) are in `internal/reporting/p5_crm.go`
(`P5CRMReports`, `P5CRMContribution`); when merged with the BI registry they should be registered through `RegisterP5Report`.

## Recruitment & Performance Review (`hris/talent`, EP-03, EP-05, EP-27 part)

**Packages.** `internal/hris/talent` (imports the hris root only); root additions in `internal/hris/talent.go`:
`hris.RecruitmentConfiguration` / `hris.PerformanceConfiguration` (policies `hris.recruitment_configuration`,
`hris.performance_configuration`, category HR Configuration, appended to `hris.PolicyCodes`), the scoring engine
(`WeightedScore`, `CombineScores`, `PerformanceConfiguration.Band/RecommendedScore/Distribution/AtLeast`), the stage
constants, `hris.Onboarding` and `hris.LatestReviewResult`.

**Core HR through `hris.Onboarding`** (wired in `internal/app/p5_hr_talent.go`): `Hire` creates the employee through the
employee resource (Core HR publishes `hris.employee_hired`), the active contract (`corehr.Module.CreateContract`, new
file `corehr/hire.go`, same validation / numbering / audit as `POST /hris/contracts`) and the ESS login
(`iam.ProvisionEmployeeUser`); `ChangeEmployment` is Core HR `:promote` (used by `POST /hris/reviews/{id}:promote`).

**Operational review inputs (FR-PRF-HR-03).** `hris.RegisterReviewInput(key, func(ctx, q, employee, from, to)
([]hris.ReviewInput, error))`: snapshot at cycle launch. Registered: `training`, `certifications` (talent),
`sales_target` (internal/app, `sales.TargetAchievements` of the employee's login). The time & attendance area can
register `attendance` the same way.

**Approval document types.** `hris.job_requisition` (attributes `headcount`, `salaryMax`, `orgUnitId`, `reason`,
`contractType`) and `hris.job_offer` (`baseSalary`, `aboveBudget` 1/0, `gradeLevel`, `contractType`, `orgUnitId`).
Without a workflow a submitted document is approved at once.

**Event published — `hris.performance_review_completed`** (aggregate `hris.performance_review`), once per review
when its cycle is closed; the payroll area may read it (or `hris.LatestReviewResult`) as the salary increase / bonus
basis (FR-PRF-HR-04):

```json
{ "reviewId": "uuid", "cycleId": "uuid", "cycleCode": "FNB-ANNUAL-2026", "cycleType": "annual|semester|probation",
  "periodEnd": "2026-12-31", "propertyId": "uuid", "employeeId": "uuid", "employeeNo": "EMP-00024", "finalScore": "3.9",
  "finalRating": "exceeds", "recommendation": "salary_increase", "increasePercent": "7", "bonusMonths": "1.5" }
```

Completed reviews are also written to `hris.employment_history` with the new kind `performance_review` (reason =
cycle, rating, score; reference = cycle code). `hris.employee_hired` is published by Core HR for every hire.

**Report role hardening.** `hris.harden_report_role()` now also runs every `hris.harden_report_role_<area>()`
function; talent adds `hris.harden_report_role_talent()` (candidate personal data, salary budgets and offer salaries
are not readable by the report role). Later hris areas: add your own `harden_report_role_<area>()` instead of
replacing `harden_report_role()`.

**ESS sections.** `reviews` (My Reviews, order 85) and `team-reviews` (Team Reviews, manager, `hris.team.view`,
order 105); Staff App views in `web/apps/staff/src/p5/hr_talent.tsx` (`TALENT_ESS`).

**Reports & KPIs** (`internal/reporting/p5_hr_talent.go`, views `reporting.hr_requisitions`, `hr_applications`,
`hr_performance_reviews` in `reporting/00023_p5_hr_talent.sql`): reports `hris.recruitment_funnel`,
`hris.time_to_hire`, `hris.performance_review`, `hris.performance_distribution` (RegisterP5Report: HR Manager,
HR Admin, GM, Property Admin); HR Performance KPIs `time_to_hire`, `open_positions`, `review_completion`,
`review_score` (RegisterHRKPI).

**Public API.** `GET /api/v1/public/careers?propertyId=`, `GET /api/v1/public/careers/{id}`,
`POST /api/v1/public/careers/applications` (consent required, talent pool consent optional, CV base64 up to the
Recruitment Configuration limit, honeypot `website`, 10 / minute per IP).

## Time & Attendance (`hris`, EP-06/07/08, ESS sections, EP-25/26/27/28 parts) — notes for payroll, BI and the other areas

**Packages.** `internal/hris/hrtime` implements schedules, attendance, devices / kiosk, leave, permission and overtime (HTTP,
jobs, demo); the public part lives in the `hris` root: `p5_time.go` (events, payloads, statuses, ESS sections),
`p5_time_calc.go` (pure engine: `EvaluateDay`, `DayKindOf`, `OvertimePolicy.Tiers / CountableOvertime / PayableHours`,
`MultipliedOf`, `AnnualGrant`, `LeavePolicy.CarryOver / CarryOverExpiryDate`, `DistanceMeters`) and `p5_time_payroll.go`
(payroll contract below). Policies are Core HR's: Attendance Configuration, Attendance Policy, Leave Policy, Overtime
Policy (versioned; requests store the policy version). Holidays: `hris.holidays` (public holiday / collective leave /
company holiday) plus the P1 Day Calendar public holidays.

**Payroll contract (EP-09 reads EP-07/08)** — all take the caller's querier (RLS applies); local dates `from`–`to` inclusive:

| Function | Returns |
|---|---|
| `hris.FinalizeAttendance(ctx, tx, property, from, to)` | Closes every attendance day of the period up to yesterday (Absent for shifts without clock-in, Missing Clock-out, approved leave → On Leave). Call before calculating; the daily job (00:20) does it for yesterday. |
| `hris.TimeSummaries(ctx, q, property, from, to, employeeIDs)` | `[]TimeSummary` per employee employed in the period (empty ids = all): `scheduledDays`, `presentDays` (present + late + early leave), `lateDays` / `lateMinutes`, `earlyLeaveDays` / `earlyLeaveMinutes`, `absentDays`, `paidLeaveDays`, **`unpaidLeaveDays`**, `offDays`, `holidayDays`, `workedHours`, `unpaidPermissionHours`, `openDays` (not closed yet), `exceptions` (missing clock-in/out, clock-ins waiting for review, corrections waiting), and `overtime` (below). `TimeSummary.AttendanceFactor(paidLeaveCounts)` = present ÷ scheduled (service charge §16 #5). |
| `hris.ApprovedOvertime(ctx, q, property, from, to, ids)` | `map[employee]OvertimeSummary`: `approvedHours`, **`payableHours`** (approved hours capped by the clocked overtime of the day, rounded down to the policy unit, minimum / daily limit applied — FR-OVT-02), `multipliedHours` (Σ payable hours × factor), **`tiers`** `[{factor:"1.5", hours:"1"}, {factor:"2", hours:"2"}]` merged per factor, and `lines` per request (`dayKind` workday / rest_day / shortest_day). Pay = `OvertimePolicy.HourlyWage(contract.FixedWage())` (1/173) × multiplied hours — payroll computes it; time & attendance stores no amounts. |
| `hris.UnpaidLeaveDays(ctx, q, property, from, to, ids)` | `map[employee]decimal` of approved unpaid leave days. |
| `hris.LockTimePeriod(ctx, q, property, from, to, reference, by)` / `hris.ReleaseTimeLock(ctx, q, property, lockID, by, note)` / `hris.TimeLocked(ctx, q, property, day)` | Lock the attendance, leave, permission and overtime of a payroll period (changes are refused with `409 period_locked`); the caller audits. HR / Finance also lock from HRIS → Attendance (`POST /api/v1/hris/time-locks`). |

`GET /api/v1/hris/time-summary?from&to[&employeeId]` serves `TimeSummaries` (permission `hris.time_lock.view`).

**Events published** (outbox; aggregates `hris.schedule`, `hris.attendance_event`, `hris.leave_request`, `hris.overtime_request`):

```jsonc
// hris.schedule_published — a Shift Schedule is published (version 1) and again for every change after publishing (republished: true)
{ "scheduleId": "uuid", "propertyId": "uuid", "orgUnitId": "uuid", "orgUnitCode": "FNB", "orgUnitName": "Food & Beverage", "name": "…",
  "periodStart": "2026-10-12", "periodEnd": "2026-10-18", "version": 1, "republished": false, "shifts": 42,
  "employeeIds": ["uuid"], "scheduledHours": "294" }
// hris.attendance_recorded — every accepted clock-in / out (ESS GPS, kiosk QR / PIN, device, HR, correction); a resubmitted offline event is published once
{ "eventId": "uuid", "propertyId": "uuid", "employeeId": "uuid", "employeeNo": "EMP-00021", "workDate": "2026-10-05", "direction": "in|out",
  "occurredAt": "2026-10-05T00:58:00Z", "method": "mobile_gps|kiosk_qr|kiosk_pin|fingerprint|face_recognition|manual|correction",
  "source": "ess|kiosk|device|hr|correction", "deviceId": "uuid|null", "offline": false, "flags": ["out_of_area"],
  "reviewStatus": "not_required|pending|accepted|rejected",
  "day": { "status": "scheduled|present|late|early_leave|absent|on_leave|off|holiday", "firstIn": "…|null", "lastOut": "…|null",
           "workedMinutes": 0, "lateMinutes": 0, "earlyLeaveMinutes": 0, "overtimeMinutes": 0 } }
// hris.leave_approved — leave approved (the balance is reduced at that moment)
{ "leaveRequestId": "uuid", "number": "LV-2026-00012", "propertyId": "uuid", "employeeId": "uuid", "employeeNo": "…", "fullName": "…",
  "leaveType": "ANNUAL", "leaveTypeName": "Annual Leave", "paid": true, "startDate": "2026-10-20", "endDate": "2026-10-21", "halfDay": "am|pm|null",
  "days": "2", "balanceYear": 2026, "remainingBalance": "8", "policyVersion": 0 }
// hris.overtime_approved — overtime approved; payableHours follows the attendance afterwards (read TimeSummaries for payroll)
{ "overtimeRequestId": "uuid", "number": "OT-2026-00007", "propertyId": "uuid", "employeeId": "uuid", "employeeNo": "…", "workDate": "2026-10-05",
  "hours": "3", "payableHours": "3", "dayKind": "workday", "workWeekDays": 5, "tiers": [{ "factor": "1.5", "hours": "1" }, { "factor": "2", "hours": "2" }],
  "multipliedHours": "5.5", "policyVersion": 0 }
```

**Consumed:** `hris.employee_terminated` → `hris.attendance_device_removal` (the leaver's attendance PIN is cleared and
the device user is removed from the real biometric devices through the bridge agent, §16 #10).

**Approvals.** Manager levels of the policy (Leave Policy `approvalLevels`: supervisor, department_head, hr; overtime,
permission, shift swap, attendance correction: supervisor) run in HRIS (ESS → Approvals for department heads with
`hris.team.approve`, Back Office for HR with the approve permission); afterwards the approval engine applies the workflow
of the document types `hris.leave_request`, `hris.permission_request`, `hris.overtime_request`, `hris.shift_swap`,
`hris.attendance_correction` (attributes days / hours / leaveType / dayKind / orgUnit; none configured = approved at once).

**Devices (FR-INT-P5-01).** Bridge agent profile `attendance_terminal`: commands `sync_users {users: [{deviceUserNo, name}],
remove: [deviceUserNo]}` and `delete_users {deviceUserNos}`; the agent pushes `POST /api/v1/bridge/hris/attendance-events`
(bearer agent token) `{deviceSerial|deviceCode, events: [{eventId, deviceUserNo, occurredAt, method: face_recognition|fingerprint, direction?}]}`
→ per event accepted / duplicate / rejected. No biometric template is ever sent or stored. Vendor `mock` is the trial
adapter (`:simulate`). Offline sync actions of the ops shell: `hris.kiosk_clock`, `hris.ess_clock` (platform sync).

**ESS sections** (registered in `hris`): schedule 20, clock 25, attendance 30, leave 40, overtime 50; manager: approvals 102,
team-schedule 104, team-attendance 106. Staff App screens in `web/apps/staff/src/p5/hr_time.tsx` (`registerEssSection`).

**BI.** Reports `hris.attendance`, `hris.late_absence`, `hris.overtime`, `hris.leave_balance`, `hris.shift_coverage`
(`RegisterP5Report`) and KPIs `attendance_rate`, `overtime_hours` (executive), `late_rate`, `absenteeism` (`RegisterHRKPI`)
on `reporting.hr_attendance_days`, `hr_overtime`, `hr_leave_balances`, `hr_leave_requests`, `hr_shift_assignments`,
`hr_staffing_requirements` (reporting/00022). **Report-role hardening:** `hris.harden_report_role_time()` (attendance PIN
hash, consent files and GPS positions) — to be called by `hris.harden_report_role()` once the per-area pattern is merged.

**Import (EP-28 FR-MIG-P5-02).** `oneclub import hris -property MAIN --leave-balances F` or
`POST /api/v1/hris/leave-balances:import` (columns `employeeNo, leaveType, year, entitled, carriedOver, carryOverExpiresOn, used, note`; upsert per
employee / type / year, dry run).
