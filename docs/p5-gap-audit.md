# PRD P5 gap audit (non-payroll, non-payout scope)

Baseline: staging `bd183f1` (Core HR & ESS, recruitment & performance, time & attendance, Advanced CRM, member tier
classes, BI, package & tournament, trial dataset, payroll input registry). Checked against
`docs/product/OneClub-PRD-P5-People-Advanced-Enterprise.md` by reading code, routes (`api/openapi/openapi.json`) and
e2e tests. Payroll (EP-09, EP-10, EP-15, payslip, PPh 21 / BPJS / e-Bupot, bank payroll file, payroll reports, payroll
YTD import, parallel run) and payouts (EP-11–14, caddy / instructor statements, payout reports) are owned by the
payroll and payouts areas and only marked here.

Status: **done** (implemented and covered by a test), **partial**, **missing**, **payroll area**, **payouts area**.
The last column says what this change (P5 gap closure, `test/e2e/p5_gaps_test.go`) does about it.

## EP-01 – EP-08 (Core HR, recruitment, training, performance, time & attendance)

| FR | Status | Evidence | Gap → closed by |
|---|---|---|---|
| FR-HR-01 org units (hierarchy, cost center), positions, grades, supervisor | done | `hris/00002_core_hr.sql`, `/hris/org-units`, `/hris/positions`, `/hris/grades`; `TestP5HREmployees` | — |
| FR-HR-02 employee profile, statuses | done | `corehr/employees.go`; `TestP5HREmployees` | — |
| FR-HR-03 masking of NIK / NPWP / bank / salary | done | `maskEmployee`, `hris.harden_report_role()`; `TestP5HREmployees` | — |
| FR-HR-04 login on onboarding, H7 deactivation | done | `:create-account`, `subscribeP5HR`; `TestP5HREmployees` | — |
| FR-HR-05 transfer / promotion with history | done | `:transfer`, `:promote`, `/employment-changes`; `TestP5HREmployees` | — |
| FR-HR-06 offboarding checklist | done | `/employees/{id}/offboarding` (handover, return assets, access, biometric removal, final settlement) | Proposal: link P4 fixed-asset custodian (free text today) |
| FR-HR-07 org chart (Should) | done | `/hris/org-chart` | — |
| FR-HR-08 P0 employee migration with facade | done | triggers in `hris/00002_core_hr.sql` | — |
| FR-CTR-01..04 contracts, renewals, H-30/H-7, PKWT limits, documents | done | `corehr/contracts.go`, `corehr/documents.go`, jobs; `TestP5HRContracts`, `TestP5HRDocuments` | — |
| FR-CTR-05 letter templates (Should) | done | `/hris/letter-templates`, `/employees/{id}/letters/{code}` | — |
| FR-RCT-01..06 recruitment, careers page, retention | done | `hris/talent`, `web/apps/web/app/[lang]/careers`; `TestP5HRTalentRecruitment` | — |
| FR-TRC-01..04 certification types, H-60/H-30, enforcement (H7), training | done | `corehr/training.go`, `hris.CheckPartner`, golf / sport club hooks; `TestP5HRCertificationsAndTraining` | — |
| FR-TRC-05 training matrix (Should) | done | `/hris/training-matrix` | — |
| FR-PRF-HR-01..04 review cycles, scoring, inputs, promotion | done | `hris/talent/reviews.go`; `TestP5HRTalentPerformance` | — |
| FR-SCH-01..07 templates, schedules, validation, demand, publish, swaps, offline | done | `hrtime/schedules.go`, `hrtime/swaps.go`, ESS offline copy; `TestP5HRTimeSchedules` | — |
| FR-ATT-01..07 methods, kiosk, matching, corrections, geofence, biometrics, offline idempotent | done | `hrtime/attendance.go`, `hrtime/devices.go`; `TestP5HRTimeAttendance` | — |
| FR-ATT-08 device clock-in for caddies & instructors (Should) | **missing** | device users are employees only; golf caddy attendance is manual | Partner device users (caddy / instructor), device events → `hris.partner_clocked` → golf caddy attendance (present, joins the queue) |
| FR-LVE-01..03, FR-OVT-01..04 | done | `hrtime/leave.go`, `hrtime/overtime.go`; `TestP5HRTimeLeave`, `TestP5HRTimeOvertime` | — |

## EP-09 – EP-16

| FR | Status | Evidence | Gap → closed by |
|---|---|---|---|
| EP-09, EP-10, EP-15 (FR-PAY-*, FR-TAX-HR-*, FR-PPY-*) | payroll area | `p5_payroll_inputs.go` (input registry only) | — |
| EP-11 – EP-14 (FR-SVC-*, FR-CMS-HR-*, FR-CDY-*, FR-INS-HR-*) | payouts area | — | — |
| FR-ESS-01..03, 05, 06 personal login, schedule, clock, requests, manager view, data changes | done | `corehr/ess.go`, `hrtime/ess.go`, ESS registry; `TestP5HRSelfService`, `TestP5HRTimeSelfServiceAndReports` | — |
| FR-ESS-04 payslip / documents / training | partial | documents & training done; payslip = payroll area | — |
| FR-ESS-07 push / WhatsApp notifications | partial | WhatsApp + e-mail + in-app templates (`hrtime/catalog.go`); no push channel | Push channel (see FR-INT-P5-05) |

## EP-17 – EP-23 (advanced CRM, BI, package, tournament)

| FR | Status | Evidence | Gap → closed by |
|---|---|---|---|
| FR-SEG-01..05 behaviour per line, RFM, VIP, scheduled segments, movement | done | `crm/analytics`; `TestP5CRMAnalytics` | — |
| FR-LOY-P5-01..06 tiers, benefits, rewards, eligibility, Top Spender, cost | done | `crm/loyalty/p5_*`; `TestP5CRMTierProgram`, `TestP5CRMRewards`, `TestP5Tiers*` | — |
| FR-JRN-01..07 journeys, renewal, birthday, templates, consent / cap / quiet hours, A/B, performance | done | `crm/journey`; `TestP5CRMJourney*` | — |
| FR-CRA-01..05 NPS, SLA, sales, commission analytics, CLV / cohorts | done | `/crm/analytics/{nps,sla,sales,clv}`; `TestP5CRMAnalytics` | — |
| FR-BI-01..08 analytics store, executive overview, targets, drill-down, schedules, builder, comparisons | done | `internal/reporting/p5_bi_*`; `TestP5BI*` | HR datasets for the report builder added |
| FR-PKG-P5-01..05 | done | `commercial` P5 package; `TestP5LeisurePackageAdvanced` | — |
| FR-TRN-P5-01..06 | done | `golf/tournament` P5; `TestP5Leisure*` | — |

## EP-24 – EP-29

| FR | Status | Evidence | Gap → closed by |
|---|---|---|---|
| FR-POL-P5-01..03 Attendance / Leave / Overtime Policy | done | `hris/policies.go` (versioned, effective date, labels §7.6, category HR Policies); `TestP5HRPoliciesAndCatalog` | — |
| FR-POL-P5-04 Payroll Configuration | done (store) / payroll area (use) | `hris/policies.go` | — |
| FR-POL-P5-05 Service Charge Policy | done (store) / payouts area (use) | `hris/policies.go` | — |
| FR-POL-P5-06 Caddy Policies (payout part) | payouts area | — | — |
| FR-POL-P5-07 policy version per run | payroll / payouts area | — | — |
| FR-INT-P5-01 attendance devices via bridge agent, mock adapter | done | `hrtime/devices.go`, `/bridge/hris/attendance-events`, `:simulate`, `:sync-employees`; `TestP5HRTimeAttendance` | Partner (caddy / instructor) device users added (FR-ATT-08) |
| FR-INT-P5-03 / 04 e-Bupot, BPJS, bank payroll file | payroll area | — | — |
| FR-INT-P5-05 push notifications ESS & Member App (Should) | **missing** | channels are in-app / e-mail / WhatsApp | Push provider interface + log (mock) provider, Web Push subscriptions from the Staff App (ESS) and Member App service workers, `push` channel of the notification service |
| FR-INT-P5-06 P5 templates ID/EN | done | `corehr/catalog.go`, `hrtime/catalog.go`, `talent/catalog.go`, `journey/http.go`, `loyalty` (payslip = payroll area) | Push variants of the ESS / Member App templates |
| FR-OPS-P5-01 ESS & manager view in ops | done | `/ops/ess` (`web/apps/staff/src/p5/hr.tsx`) | — |
| FR-OPS-P5-02 Attendance Kiosk on registered devices | done | `/ops/attendance-kiosk`, `/hris/attendance/kiosk*` | Partner clock-in at the kiosk (QR / PIN of caddies) |
| FR-OPS-P5-03 / 04 caddy payout history, honor statement | payouts area | — | — |
| FR-OPS-P5-05 ESS as PWA, payroll never cached offline | partial | PWA manifest + service worker; runtime cache is an allow-list without a guard | Cache allow-list moved to `web/apps/staff/sw-cache.ts` with an explicit payroll deny-list and a vitest guard |
| FR-RPT-P5-01 HR Performance KPIs | partial | Attendance, Overtime (time), Caddy Attendance / Rating (defaults) registered; Headcount reads the P0 master; Turnover and Certification Compliance "coming soon" (`HRCoreKPIs` never registered); Payroll Cost = payroll area | Core HR KPIs registered through `RegisterHRKPI` |
| FR-RPT-P5-02 HR reports (non-payroll) | partial | all 11 exist; Core HR's five are appended outside the BI registry (`HRCoreReports` + `HRCoreContribution`) | Registered through `RegisterP5Report` (same codes, permissions, URLs) |
| FR-RPT-P5-03 CRM / package / tournament reports | partial | all exist; registered outside the registry (`P5CRMReports`, `P5TierReports`, `LeisureReports` + own contributions) | Registered through `RegisterP5Report` |
| FR-RPT-P5-04 permission per report, CSV / XLSX / PDF | done | `sqlReport` permission, report export; `TestP5HRReports` | — |
| FR-MIG-P5-01 employees, organization, contracts, documents | partial | `oneclub import hris --employees --contracts --certifications`; no org units / positions / grades, no employee documents | `--org-units`, `--positions`, `--grades`, `--documents` (CSV, files from a directory) |
| FR-MIG-P5-02 leave balances at cutover | done | `--leave-balances`, `/hris/leave-balances:import` | — |
| FR-MIG-P5-03 salary structure, BPJS, PTKP, payroll YTD | partial / payroll area | PTKP and BPJS numbers in the employee import; salary structure & YTD = payroll | — |
| FR-MIG-P5-04 certifications | done | `--certifications`; `TestP5HRImport` | — |
| FR-MIG-P5-05 reconciliation with HR & Finance sign-off | **missing** (headcount, leave) | — | HR Migration Reconciliation (legacy totals vs OneClub, per org unit / leave type) with HR Manager and Finance Manager sign-off; payroll totals = payroll area |
| FR-MIG-P5-06 parallel run payroll | payroll area | — | — |
| FR-REL-P5-01 k6 load tests | **missing** (P5) | `test/load/*` covers P2–P4 | `clockin-shift-change.js`, `journey-10k.js`, `bi-2y.js` (payroll run = payroll area) |
| FR-REL-P5-02 Playwright §9 flows | **missing** (P5) | `web/e2e/*` covers P1–P4 | `web/e2e/p5-flows.spec.ts` (§9.1 up to overtime, §9.4, §9.5, §9.6) |
| FR-REL-P5-03 regression P1–P4 | done | full e2e suite | re-run |
| FR-REL-P5-04 payroll / payout integration tests | payroll / payouts area | — | — |
| FR-REL-P5-05 pen-test scope | **missing** | — | `docs/security/p5-pentest-checklist.md` |
| FR-REL-P5-06 DPIA (biometrics, salary, CRM profiling) | **missing** | — | `docs/security/p5-dpia.md` |
| FR-REL-P5-07 VPS build with garble, no source maps | done | `deploy/docker/Dockerfile` (`OBFUSCATE=true`), `.github/workflows/staging.yml`, `SOURCEMAPS=false` | — |
| FR-REL-P5-08 training per role & hypercare (usulan) | missing | — | Release 5 section in `docs/runbooks/production-readiness.md` |
| Trial dataset for the P5 areas | **missing** | `trialSeeders` has no P5 seeder; P5 screens show only the static demo seed | Trial seeders: HR (devices, schedules, daily clock-ins, leave, overtime, corrections), advanced CRM (tier evaluation, journeys run, analytics refresh), BI (analytics store refresh) |

## §9 end-to-end flows

| Flow | Status | Evidence | Gap → closed by |
|---|---|---|---|
| 9.1 Hire to first pay | partial | hire (`TestP5HRTalentRecruitment`), certification, schedule, clock-in and overtime tested separately; pay = payroll area | `TestP5GapsHireToWork` chains requisition → hire → ESS login → certification → pool shift → GPS clock-in → overtime approved; Playwright §9.1 (up to overtime) |
| 9.2 Service charge & commission | payroll / payouts area | — | — |
| 9.3 Caddy & instructor payout | payouts area | — | — |
| 9.4 Retention journey | done | `TestP5CRMJourneyRules`, `TestP5CRMJourneyTemplates` | Playwright §9.4 |
| 9.5 Executive review | done | `TestP5BIExecutiveOverview`, `TestP5BITargets`, `TestP5BIScheduledReports` | Playwright §9.5 |
| 9.6 Tournament series | done | `TestP5LeisureSeriesOrderOfMerit`, `TestP5LeisureTeamFormats` | Playwright §9.6 |

## Outcome of the gap closure

Closed (implemented, tested in `test/e2e/p5_gaps_test.go` unless noted):

| FR | Now | Evidence |
|---|---|---|
| FR-RPT-P5-01..04 | done | Core HR / CRM / tier / leisure reports and core HR KPIs in the BI registry, HR datasets; `TestP5GapsBIRegistry` |
| FR-ATT-08, FR-INT-P5-01 (partners) | done | `hrtime/p5_partners.go`, `hris/00007_p5_gaps.sql`, consumer `golf.caddy_device_attendance`; `TestP5GapsPartnerClockIn` |
| FR-INT-P5-05, FR-ESS-07 (push) | done | `platform/integration/p5_push.go` (Web Push VAPID + mock), `platform/notification/p5_push.go`, `platform/00016_p5_push.sql`, `push-sw.js` in both PWAs; `TestP5GapsPush`, `TestEncryptPushRoundTrip`, `TestVAPIDToken` |
| FR-OPS-P5-05 | done | `web/apps/staff/src/sw-cache.ts` (+ `sw-cache.test.ts` guard) |
| FR-MIG-P5-01 | done | `--grades`, `--org-units`, `--positions`, `--documents` (+ `--documents-dir`); `TestP5GapsHRImportOrganization` |
| FR-MIG-P5-05 (headcount, leave) | done | `hris/p5_reconcile.go` (metric registry), `corehr/p5_reconcile.go`, `--reconcile`; `TestP5GapsReconciliation` |
| FR-REL-P5-01 (non-payroll) | done | `test/load/clockin-shift-change.js`, `journey-10k.js`, `bi-2y.js`; runbook §4 Release 5 |
| FR-REL-P5-02 (non-payroll §9) | done | `web/e2e/p5-flows.spec.ts`; `TestP5GapsHireToWork` |
| FR-REL-P5-05 / 06 | done | `docs/security/p5-pentest-checklist.md`, `docs/security/p5-dpia.md` |
| FR-REL-P5-08 | done (plan) | `docs/runbooks/production-readiness.md` §7 Release 5 |
| Trial dataset P5 | done | `internal/app/trial_p5.go` (HR rosters / clock-ins / overtime / partner caddies, journeys, tiers, analytics); `TestTrialDataset` coverage rows |

Left open (proposals, not Must):

* FR-HR-06: the offboarding checklist has "Company assets returned" as a manual item; P4 fixed assets record the custodian as
  free text — proposal: a custodian employee reference on fixed assets so offboarding lists the assets to return.
* FR-ATT-08: partners clock in on the biometric devices only; a kiosk QR / PIN for partners is not built (the kiosk
  identifies employees).
* Payroll / payouts rows above stay with their areas (payroll totals of the reconciliation plug into
  `hris.RegisterReconciliationMetric`; Payroll Cost KPI and salary reports into the BI registry).
