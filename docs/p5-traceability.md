# P5 traceability — requirement → implementation → evidence

PRD P5 *People & Advanced Enterprise* (Release 5). Verified against the code of branch `PRD-P5` after the close-out
(base `77a9638`: staging + payouts + payroll + P5 gap closure, plus the close-out commits: browser run, follow-ups, this
document). Every status was checked against the current code and tests. The gap audit of the non-payroll scope that
preceded the gap closure is kept for its history: [`p5-gap-audit.md`](p5-gap-audit.md) (its "missing" and "partial"
rows are closed or listed under *Open / proposals* below).

Status: **Done** implemented and verified by an automated test · **Done (mock provider for trial)** implemented
end-to-end against a sandbox adapter of the integration layer; the real provider is a configuration step ·
**Partial** implemented with a remaining gap (named in the row) · **Not built by decision** · *n/a* process decision
without behaviour (not counted). 🟡 marks verification that needs an environment or a person the dev machine does not
have (Staging, club sign-off, tax consultant, bank, external pen test).

Test locations: `test/e2e/p5_*_test.go` (Go acceptance tests on real PostgreSQL, run together with the P0–P4 suites),
`web/e2e/p5-flows.spec.ts` (Playwright §9 flows), `test/load/{clockin-shift-change,journey-10k,bi-2y,payroll-run}.js`
(k6), unit tests next to the code: payroll engine `internal/hris/p5_payroll_calc_test.go` and
`p5_payroll_retro_test.go`, payouts `internal/hris/p5_payouts_calc_test.go`, time & attendance
`internal/hris/p5_time_calc_test.go`, HR rules `internal/hris/calc_test.go`, journeys / loyalty / analytics in
`internal/crm/{journey,loyalty,analytics}`, BI in `internal/reporting/p5_bi_test.go`, push in
`internal/platform/integration`, tournament formats in `internal/golf/tournament`. Module contracts (H1–H7, events,
payloads, hooks): [`p5-contracts.md`](p5-contracts.md).

P5 adds the module `hris` (layer 4; sub-packages `corehr`, `talent`, `hrtime`, `payroll`, `payouts`) and P5 files in
`crm` (`journey`, `analytics`, `loyalty/p5_*`), `reporting` (`p5_*`), `commercial` (advanced packages), `golf/tournament`
(`p5_*`), `platform/integration` (`p5_push.go`, attendance device adapters) and `platform/notification` (push channel).
Wiring lives in `internal/app/p5_*.go`; Staff App screens in `web/apps/staff/src/p5/*.tsx` (Back Office, Management,
`ops` ESS / kiosk / instructor, Caddy Tablet payouts), Member App in `web/apps/member/src/areas/{crm_p5,leisure}.tsx`,
careers page in `web/apps/web/app/[lang]/careers`.

## Summary

| Status | Count |
|---|---|
| Done | 223 |
| Done (mock provider for trial) | 2 |
| Partial | 8 |
| Not built by decision | 1 |
| **Total requirement rows** | **234** |

Counted over the per-ID tables below (FR, acceptance criteria, §12 NFR, §16 decisions with behaviour, §9 flows, §11
events); the exit criteria table is a roll-up and is not counted.

## Audit & route coverage

The e2e harness fails the run when a mutating route succeeds without an audit entry, or when any mutating route is
never exercised successfully. The full suite (P0–P5) runs every mutating route with a successful, audited call; see
the close-out report for the run of this commit.

## Exit criteria (PRD §13.1)

| # | Criterion | Evidence | Status |
|---|---|---|---|
| 1 | Employee & partner data complete | headcount, leave balances and payroll totals reconciled and signed by HR and Finance `TestP5GapsReconciliation`, `TestP5ClosePayrollReconciliation`; imports `TestP5HRImport`, `TestP5GapsHRImportOrganization`; Playwright *EP-28 migration* | ✅ code / 🟡 with the club's HR data |
| 2 | Schedules & attendance in use | `TestP5HRTimeSchedules`, `TestP5HRTimeAttendance`, `TestP5HRTimeLeave`, `TestP5HRTimeOvertime`; trial data `internal/app/trial_p5.go` | ✅ / 🟡 one period on site |
| 3 | Payroll correct | parallel run with sign-off `TestP5PayrollFullRun`; worked examples `p5_payroll_calc_test.go`, `p5_payroll_retro_test.go`; statutory rate set `ID-2024` marked *unverified* until the tax consultant verifies it | ✅ code / 🟡 consultant test cases, one parallel period |
| 4 | Partner payouts & commissions closed (GL = 0) | `TestP5PayoutsCaddyAcceptance`, `TestP5PayoutsInstructorRun`, `TestP5PayoutsServiceCharge`, `TestP5PayoutsCommissionBonus` | ✅ |
| 5 | Certifications enforced | `TestP5HRCertificationsAndTraining`, `TestP5GapsHireToWork` (pool shift refused before the certificates) | ✅ |
| 6 | Automatic retention | `TestP5CRMJourneyRenewal`, `TestP5CRMJourneyTemplates`, `TestP5CRMTierProgram` | ✅ |
| 7 | BI consistent | `TestP5BIExecutiveOverview` (KPI = source report), drill-down to folio lines | ✅ |
| 8 | Advanced package & tournament | `TestP5LeisurePackageAdvanced`, `TestP5LeisureTeamFormats`, `TestP5LeisureSeriesOrderOfMerit` | ✅ |
| 9 | No Release 1–4 regression | P0–P5 suites pass together; Playwright P1–P5 specs | ✅ |
| 10 | UAT approved | §9 scenarios automated hop by hop below | 🟡 sign-off HR Manager, Finance Manager, GM |

## EP-01 Organization & Employee

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-HR-01 | Organization: department hierarchy, cost center, positions, grades, supervisor, per property | Done | `hris/00002_core_hr.sql`, `/hris/org-units`, `/hris/positions`, `/hris/grades` (`corehr/defs.go`); HRIS → Organization | `TestP5HREmployees` |
| FR-HR-02 | Employee profile continuing the P0 employee; statuses | Done | `corehr/employees.go`, `/hris/employees/{id}/profile` | `TestP5HREmployees` |
| FR-HR-03 | NIK / NPWP / bank / salary / health masked by permission | Done | `maskEmployee`, `hris.harden_report_role()`, `hris.harden_report_role_payroll()` | `TestP5HREmployees`, `TestP5PayrollFullRun` (masked payslips) |
| FR-HR-04 | Login and role at onboarding; `hris.employee_terminated` deactivates the user (H7) | Done | `:create-account`, hire `createLogin`, `subscribeP5HR` | `TestP5HREmployees`, `TestP5HRTalentRecruitment`; Playwright §9.1 (first login of the hire) |
| FR-HR-05 | Transfer, promotion, rotation with effective date and history | Done | `:transfer`, `:promote`, `/employment-changes` | `TestP5HREmployees` |
| FR-HR-06 | Offboarding checklist (P4 assets returned, access revoked, final settlement) | Done | `/employees/{id}/offboarding`, `/offboarding-items/{id}`; asset custodian `inventory.assets.custodian_employee_id` (`inventory/00005_asset_custodian.sql`, field *Custodian (employee)* on Inventory → Assets & Equipment), assets still held listed on the *return_assets* item (`inventory.AssetsInCustody`), which cannot be ticked while any remains; final settlement run (`hris/payroll`) | `TestP5HREmployees`, `TestP5CloseOffboardingAssets`, `TestP5PayrollTHRAndFinalSettlement` |
| FR-HR-07 | Org chart (Should) | Done | `/hris/org-chart` | `TestP5HREmployees` |
| FR-HR-08 | Migrate P0 employees to `hris` without breaking users and approvals | Done | facade triggers `hris/00002_core_hr.sql` | `TestP5HREmployees` |
| EP-01 AC | Terminated: no login from the effective date, removed from active approvers, pending approvals to the supervisor | Done | H7 handler, approval reassignment | `TestP5HREmployees` |

## EP-02 Employment Contract & Documents

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-CTR-01 | PKWT / PKWTT: dates, position, fixed pay, probation | Done | `corehr/contracts.go` | `TestP5HRContracts` |
| FR-CTR-02 | Renewal and permanent appointment with history; H-30 / H-7 reminders | Done | `:renew`, `:make-permanent`, `hris.contract_expiring` job | `TestP5HRContracts` |
| FR-CTR-03 | PKWT limits configurable (§16 #3) | Done | HR Configuration `pkwtMaxMonths`, renewals | `TestP5HRContracts`; unit `TestPKWTCompensation` |
| FR-CTR-04 | Employee documents with validity and restricted access | Done | `corehr/documents.go`, `/employee-documents` | `TestP5HRDocuments` |
| FR-CTR-05 | Letter templates (Should) | Done | `/letter-templates`, `/employees/{id}/letters/{code}` | `TestP5HRContracts` |

## EP-03 Recruitment

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-RCT-01 | Job requisition with approval and headcount | Done | `hris/talent`, `/job-requisitions` | `TestP5HRTalentRecruitment` |
| FR-RCT-02 | Candidate & application stages | Done | `/candidates`, `/applications` (+ `:move-stage`) | `TestP5HRTalentRecruitment` |
| FR-RCT-03 | Interview schedule, results, scoring | Done | `/interviews` (+ scorecards) | `TestP5HRTalentRecruitment` |
| FR-RCT-04 | Offer with approval; Hired creates employee + contract + onboarding | Done | `:offer`, `/job-offers`, `:hire` | `TestP5HRTalentRecruitment`, `TestP5GapsHireToWork`; Playwright §9.1 |
| FR-RCT-05 | Public careers page with consent (Should) | Done | `web/apps/web/app/[lang]/careers`, `POST /public/careers/applications` | `TestP5HRTalentRecruitment` |
| FR-RCT-06 | Applicant data retention (§16 #10) | Done | retention date + erase (`:erase`), job | `TestP5HRTalentRecruitment` |

## EP-04 Training & Certification

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-TRC-01 | Certification types (Lifeguard, Caddy, Food Handler …) | Done | `/certification-types`, demo per §16 #9 | `TestP5HRCertificationsAndTraining` |
| FR-TRC-02 | Certificates of employees and partners; H-60 / H-30 reminders | Done | `corehr/training.go` | `TestP5HRCertificationsAndTraining` |
| FR-TRC-03 | Enforcement in scheduling and caddy assignment; `hris.certification_expired` (H7) | Done | `hris.CheckPartner`, schedule validation, golf / sport club hooks | `TestP5HRCertificationsAndTraining`, `TestP5HRTimeSchedules`, `TestP5GapsHireToWork` |
| FR-TRC-04 | Training programmes, sessions, attendance, results, cost | Done | `/training-programs`, `/training-sessions` | `TestP5HRCertificationsAndTraining` |
| FR-TRC-05 | Training matrix (Should) | Done | `/training-matrix` | `TestP5HRCertificationsAndTraining` |
| EP-04 AC | Expired lifeguard not on the pool shift; expired caddy not in the Caddy Queue | Done | as FR-TRC-03 | `TestP5HRCertificationsAndTraining`, `TestP5GapsHireToWork` |

## EP-05 Performance Review

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PRF-HR-01 | Review cycles with templates per position | Done | `talent/reviews.go`, `/review-cycles`, `/review-templates` | `TestP5HRTalentPerformance` |
| FR-PRF-HR-02 | Self assessment, manager review, HR calibration | Done | ESS reviews, `:calibrate` | `TestP5HRTalentPerformance` |
| FR-PRF-HR-03 | Operational data as input (Should) | Done | attendance, caddy rating, NPS, sales targets in the review inputs | `TestP5HRTalentPerformance` |
| FR-PRF-HR-04 | Results feed raises / bonus and appointment | Done | promotion through Core HR; `/payroll-adjustments:from-reviews` | `TestP5HRTalentPerformance`, `TestP5PayoutsCommissionBonus` |

## EP-06 Shift Scheduling

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-SCH-01 | Shift templates (breaks, overnight) | Done | `hrtime/schedules.go`, `/shift-templates` | `TestP5HRTimeSchedules` |
| FR-SCH-02 | Weekly / monthly roster with patterns and copy | Done | `/schedules` (+ `:apply-pattern`, `:copy`) | `TestP5HRTimeSchedules` |
| FR-SCH-03 | Validation: coverage, certifications, hours, rest days, leave | Done | schedule validation | `TestP5HRTimeSchedules` |
| FR-SCH-04 | Staffing demand from operations (Should) | Done | `/staffing-demand`, `/staffing-requirements` | `TestP5HRTimeSchedules` |
| FR-SCH-05 | Publish to ESS with notification | Done | `:publish`, `hris.schedule_published` | `TestP5HRTimeSchedules` |
| FR-SCH-06 | Shift swap with approval | Done | `hrtime/swaps.go` | `TestP5HRTimeSchedules` |
| FR-SCH-07 | Schedule offline in ESS (Should) | Done | ESS offline copy (IndexedDB) | `TestP5HRTimeSelfServiceAndReports` |

## EP-07 Attendance

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-ATT-01 | Fingerprint, face recognition (bridge agent), mobile GPS | Done | `hrtime/attendance.go`, `hrtime/devices.go`, `/ess/attendance:clock` | `TestP5HRTimeAttendance`; Playwright §9.1 |
| FR-ATT-02 | Attendance Kiosk with QR / PIN | Done | `/hris/attendance/kiosk*`, `/ops/attendance-kiosk` | `TestP5HRTimeAttendance` |
| FR-ATT-03 | Matching to shifts with tolerances | Done | day statuses, Attendance Policy | `TestP5HRTimeAttendance`; unit `TestEvaluateDay` |
| FR-ATT-04 | Corrections with approval | Done | `/attendance-corrections` | `TestP5HRTimeAttendance` |
| FR-ATT-05 | Coordinates, accuracy, geofence review | Done | geofences, review queue | `TestP5HRTimeAttendance`; unit `TestDistance` |
| FR-ATT-06 | No biometric templates; consent; non-biometric alternative | Done | consent on attendance profiles, no template column | `TestP5HRTimeAttendance` |
| FR-ATT-07 | Offline clock-in synced idempotently | Done | kiosk sync, client event id | `TestP5HRTimeAttendance` |
| FR-ATT-08 | Device clock-in for partner caddies & instructors (Should) | Partial | `hrtime/p5_partners.go`, `hris/00007_p5_gaps.sql`, consumer `golf.caddy_device_attendance` | `TestP5GapsPartnerClockIn` — biometric devices only; the kiosk QR / PIN for partners is not built (open item) |
| EP-07 AC 1 | Clock-in 400 m from the geofence → Out of Area review | Done | geofence check | `TestP5HRTimeAttendance` |
| EP-07 AC 2 | Duplicate offline kiosk event recorded once | Done | idempotent sync | `TestP5HRTimeAttendance` |

## EP-08 Leave, Permission & Overtime

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-LVE-01 | Leave types, balances, accrual (§16 #3) | Done | `hrtime/leave.go`, Leave Policy | `TestP5HRTimeLeave`; unit `TestLeaveAccrual` |
| FR-LVE-02 | Requests from ESS with approval levels; balance reduced | Done | `/ess/leave-requests`, approval engine | `TestP5HRTimeLeave` |
| FR-LVE-03 | Team leave calendar, conflicts | Done | `/leave-calendar`, `/ess/team-calendar` | `TestP5HRTimeLeave` |
| FR-OVT-01 | Overtime request before / after with approval | Done | `hrtime/overtime.go` | `TestP5HRTimeOvertime`; Playwright §9.1 |
| FR-OVT-02 | Hours from actual attendance vs schedule; limits | Done | payable hours, Overtime Policy | `TestP5HRTimeOvertime`; unit `TestPayableOvertime` |
| FR-OVT-03 | Overtime pay by the configured formula | Done | overtime rate tiers of the Overtime Policy (`hris/p5_time_calc.go`), payroll `OVERTIME` line (fixed wage ÷ 173 × multiplied hours) | `TestP5HRTimeOvertime`, `TestP5PayrollFullRun`; unit `TestOvertimePayAcceptance`, `TestOvertimeTiers` |
| FR-OVT-04 | Overtime without approval as exception | Done | `/overtime-exceptions` | `TestP5HRTimeOvertime` |
| EP-08 AC | Rp5,190,000 ÷ 173 = Rp30,000; 3 h = Rp165,000 | Done | as above | `TestP5HRTimeOvertime`, `TestP5PayrollFullRun`; unit `TestOvertimePayAcceptance` |

## EP-09 Payroll Engine

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PAY-01 | Salary structures per grade / position, effective-dated | Done | `hris/payroll/structures.go`, pay components | `TestP5PayrollStructuresAndRates`, `TestP5CloseRetroBPJS` |
| FR-PAY-02 | Payroll run from attendance, overtime, unpaid leave, service charge, commission, bonus, loans | Done | `payroll/engine.go`, payroll input registry (`hris/p5_payroll_inputs.go`) | `TestP5PayrollFullRun`, `TestP5PayoutsServiceCharge`, `TestP5PayoutsCommissionBonus` |
| FR-PAY-03 | Proration of joiners / leavers | Done | `hris.ProrationFactor` | `TestP5PayrollFullRun`; unit `TestPayrollProration` |
| FR-PAY-04 | THR (≥ 12 months 1×, else pro rata) | Done | THR run | `TestP5PayrollTHRAndFinalSettlement`; unit `TestPayrollTHRAmount`, `TestTHR` |
| FR-PAY-05 | Bonus from reviews with approval | Done | adjustments, bonus programmes | `TestP5PayrollFullRun`, `TestP5PayoutsCommissionBonus` |
| FR-PAY-06 | Draft → Calculated → Approved → Posted → Paid; approved locked; corrections by adjustment runs | Done | `payroll/runs.go`; adjustment run pays the retro differences of pay **and BPJS contributions** of the corrected period with its statutory rates (`retroItems`, `hris.BPJSDifferences`) | `TestP5PayrollFullRun`, `TestP5PayrollRetroAdjustment`, `TestP5CloseRetroBPJS`; unit `TestPayrollRetroBPJSDifferences` |
| FR-PAY-07 | Final settlement (remaining pay, leave, severance, PKWT compensation) | Done | final settlement run | `TestP5PayrollTHRAndFinalSettlement`; unit `TestPayrollSeveranceFinalTax`, `TestSeverance` |
| FR-PAY-08 | Simulation and comparison with the previous period | Done | `/payroll-runs/{id}/comparison` | `TestP5PayrollFullRun` |
| EP-09 AC 1 | THR Rp6,000,000 after 6 months = Rp3,000,000 | Done | | `TestP5PayrollTHRAndFinalSettlement`; unit `TestPayrollTHRAmount` |
| EP-09 AC 2 | Approved run immutable; later attendance changes go to the next period's adjustment run | Done | | `TestP5PayrollRetroAdjustment`, `TestP5CloseRetroBPJS` |

## EP-10 PPh 21 & BPJS

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-TAX-HR-01 | PPh 21 (TER monthly, annual in the last period), PTKP; versioned tables | Done | `hris/p5_payroll_calc.go`, statutory rate sets | `TestP5PayrollFullRun`; unit `TestPayrollMonthlyTER`, `TestPayrollDecemberAnnual`, `TestPayrollTERCombinedMonth`, `TestPayrollNonNPWPSurcharge`, `TestPayrollAnnualRefundPartialYear` |
| FR-TAX-HR-02 | PPh 21 non-employee for partners (§16 #4) | Done | `hris/p5_payouts_calc.go` | `TestP5PayoutsCaddyAcceptance`; unit `TestPartnerPPh21` |
| FR-TAX-HR-03 | BPJS Kesehatan / JHT / JKK / JKM / JP with caps, versioned | Done | `StatutoryRates.BPJSContributions`; retro differences on corrections | `TestP5PayrollFullRun`, `TestP5CloseRetroBPJS`; unit `TestPayrollBPJSCaps`, `TestBPJSContributions`, `TestPayrollRetroBPJSDifferences` |
| FR-TAX-HR-04 | e-Bupot and BPJS files | Done | `/payroll-exports/e-bupot`, `bpjs-kesehatan`, `bpjs-ketenagakerjaan`, `1721-a1` | `TestP5PayrollFullRun` — 🟡 file formats to check against the current DJP / BPJS import templates |
| FR-TAX-HR-05 | Consultant test cases before go-live | Partial | worked examples in unit tests; rate sets `:verify`; set `ID-2024` *unverified* and a warning on every run | `TestP5PayrollStructuresAndRates` — 🟡 verification by the tax consultant pending |
| EP-10 AC 1 | Rp6,000,000: Kesehatan 240,000 / 60,000, JHT 222,000 / 120,000, JP 120,000 / 60,000 | Done | | unit `TestPayrollBPJSCaps` |
| EP-10 AC 2 | PPh 21 equal to the consultant's cases to the rupiah | Partial | engine and worked examples ready | 🟡 the consultant's case table is not available yet |

## EP-11 Service Charge Distribution

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-SVC-01 | Pool per period & property from P4 (H4) | Done | `hris/payouts/service_charge.go` | `TestP5PayoutsServiceCharge` |
| FR-SVC-02 | Configurable distribution rule (§16 #5) | Done | Service Charge Policy | `TestP5PayoutsServiceCharge`; unit `TestServiceChargeDepartmentsAndPoints`, `TestServiceChargeEligibilityAndReserve` |
| FR-SVC-03 | Simulation, approval, into payroll | Done | `:simulate`, `:approve`, payroll input | `TestP5PayoutsServiceCharge` |
| FR-SVC-04 | Distributed = pool; rounding account; GL liability 0 after payment | Done | accounting `p5_payout_post.go` | `TestP5PayoutsServiceCharge` |
| EP-11 AC | Rp100 M ÷ 50 = Rp2 M; 20 of 25 days = 80 %, rest redistributed | Done | | unit `TestServiceChargeAcceptance`, `TestAttendanceFactor`; `TestP5PayoutsServiceCharge` |

## EP-12 Sales Commission & Bonus Payout

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-CMS-HR-01 | Approved commission statements (H3) | Done | `hris/payouts/sources.go` | `TestP5PayoutsCommissionBonus` |
| FR-CMS-HR-02 | Paid through payroll; clawback next period | Done | payroll inputs | `TestP5PayoutsCommissionBonus` |
| FR-CMS-HR-03 | P3 statement Paid after posting | Done | | `TestP5PayoutsCommissionBonus` |

## EP-13 Non-Employee Workforce: Caddy

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-CDY-01 | Caddy partner profile | Done | `/partner-profiles` | `TestP5PayoutsCaddyRun` |
| FR-CDY-02 | Caddy payout run per settlement period (H1) | Done | `/payout-runs` | `TestP5PayoutsCaddyRun`; unit `TestPartnerPayoutCaddy`, `TestPartnerPeriods` |
| FR-CDY-03 | Statement in the Caddy App and PDF; Finance approval | Done | Caddy Tablet → Payouts; statement PDF | `TestP5PayoutsCaddyRun`; unit `TestStatementPDF` |
| FR-CDY-04 | Bank file and cash payments | Done | `/payout-runs/{id}/bank-file`, `:mark-paid` | `TestP5PayoutsCaddyRun` |
| FR-CDY-05 | Journal clears the caddy fee liability (H5) | Done | accounting `p5_payout_post.go` | `TestP5PayoutsCaddyAcceptance` |
| FR-CDY-06 | BPJS for caddies (Should, §16 #4 BPU) | Done | JKK / JKM BPU in the payout | `TestP5PayoutsCaddyAcceptance` |
| EP-13 AC | Rp6,000,000 + Rp400,000 = Rp6,400,000; liability 0 after Paid | Done | | `TestP5PayoutsCaddyAcceptance` |

## EP-14 Non-Employee Workforce: Instructor

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-INS-HR-01 | Instructor profile, certifications, rates | Done | partner profiles, P2 fee rates | `TestP5PayoutsInstructorRun` |
| FR-INS-HR-02 | Teaching schedule from P2 classes | Done | sessions of the P2 class schedule (H2 fee lines) on the honor statement; partner device clock-in of instructors (`/hris/partner-attendance-events?holderKind=instructor`) | `TestP5PayoutsInstructorRun` |
| FR-INS-HR-03 | Employee instructors via payroll, partners via payout run (H2) | Done | `INSTRUCTOR_FEE` payroll input, instructor run | `TestP5PayoutsInstructorRun`; unit `TestPartnerPayoutInstructor` |
| FR-INS-HR-04 | Honor statement in the instructor interface | Done | `/ops/instructor/honor` | `TestP5PayoutsInstructorRun` |
| EP-14 AC | 8 × Rp150,000 = Rp1,200,000; cancelled sessions excluded | Done | | `TestP5PayoutsInstructorRun` |

## EP-15 Payroll Accounting, Payment & Payslip

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PPY-01 | Payroll journal through P4 posting rules (H5) | Done | `hris.payroll_posted`, `accounting/p5_payroll_post.go` | `TestP5PayrollFullRun` |
| FR-PPY-02 | Bank payment file; payment confirmation → Paid, bank in P4 | Done | `/bank-file?layout=generic_csv|bca_payroll`, `:mark-paid`, `hris.payroll_paid` | `TestP5PayrollFullRun`; unit `TestRenderBankFile` — 🟡 BCA layout to verify with the bank |
| FR-PPY-03 | Digital payslip in ESS (PDF, protected) | Partial | `/ess/payslips` (own, posted runs, `private, no-store`, never cached offline), PDF | `TestP5PayrollFullRun`; Playwright §9.1 — the PDF has no password (open item) |
| FR-PPY-04 | Payroll reports per department / component / annual | Done | `hris.payroll_summary`, `payroll_cost`, `payroll_annual`, … | `TestP5PayrollFullRun` |
| FR-PPY-05 | Payroll data for payroll roles only; two-person review of payroll rules | Done | permissions; two-person activation of structures and rate sets | `TestP5PayrollStructuresAndRates`, `TestP5PayrollFullRun` |

## EP-16 Employee Self Service

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-ESS-01 | Personal login in the `ops` PWA (§16 #6) | Done | ESS role, `/ops/ess` | `TestP5HRSelfService`; Playwright *ESS on the phone*, §9.1 (first login of the hire) |
| FR-ESS-02 | My Schedule, Clock In / Out (GPS), Attendance History | Done | `hrtime/ess.go` | `TestP5HRTimeSelfServiceAndReports`; Playwright *ESS on the phone* |
| FR-ESS-03 | Leave & permission, overtime requests | Done | `/ess/leave-requests`, `/ess/overtime-requests` | `TestP5HRTimeLeave`, `TestP5HRTimeOvertime` |
| FR-ESS-04 | Payslip; my documents, certificates and training | Done | ESS sections `payslip`, `documents`, `training` | `TestP5HRSelfService`, `TestP5PayrollFullRun`; Playwright §9.1 |
| FR-ESS-05 | Manager view | Done | `/ess/team*`, approvals | `TestP5HRTimeSelfServiceAndReports` |
| FR-ESS-06 | Personal data changes verified by HR | Done | `/ess/profile-changes` | `TestP5HRSelfService` |
| FR-ESS-07 | Push / WhatsApp notifications | Done | push channel + WhatsApp templates | `TestP5GapsPush`, `TestP5HRTimeSchedules` |

## EP-17 Advanced Segmentation & VIP

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-SEG-01 | Cross-business behaviour, RFM per line | Done | `crm/analytics` | `TestP5CRMAnalytics` |
| FR-SEG-02 | RFM scoring and groups | Done | | `TestP5CRMAnalytics`; unit `TestRFMGroup`, `TestQuintileScores` |
| FR-SEG-03 | VIP from Top Spender, tier, manual; marker at check-in / POS | Done | `/crm/vip`, `/ops/front-desk/vip` | `TestP5CRMAnalytics` |
| FR-SEG-04 | Scheduled computation in the analytics store | Done | analytics refresh job | `TestP5CRMAnalytics` |
| FR-SEG-05 | Segment comparison and movement (Should) | Done | RFM movement | `TestP5CRMAnalytics` |

## EP-18 Advanced Loyalty

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-LOY-P5-01 | Automatic tiers with grace (§16 #14) | Done | `crm/loyalty/p5_*`, tier classes | `TestP5CRMTierProgram`, `TestP5TiersMaster`; unit `TestDecideTier`, `TestQualifyTier` |
| FR-LOY-P5-02 | Tier benefits read by pricing / booking | Done | booking window hook, tier F&B discount | `TestP5CRMTierBookingWindow`, `TestP5TiersPOSDiscount` |
| FR-LOY-P5-03 | Reward catalogue | Done | `/crm/loyalty-rewards` | `TestP5CRMRewards` |
| FR-LOY-P5-04 | Reward eligibility engine | Done | reward rules | `TestP5CRMRewards`; unit `TestEvaluateRewardRule` |
| FR-LOY-P5-05 | Top Spender engagement | Done | | `TestP5CRMRewards` |
| FR-LOY-P5-06 | Programme cost (Should) | Done | Loyalty Programme Cost report | `TestP5CRMRewards` |
| EP-18 AC | Gold reached → upgraded, notified, new multiplier | Done | | `TestP5CRMTierProgram` |

## EP-19 Campaign & Lifecycle Automation

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-JRN-01 | Multi-step journeys | Done | `crm/journey` | `TestP5CRMJourneyRules` |
| FR-JRN-02 | Renewal automation | Done | template renewal | `TestP5CRMJourneyRenewal` |
| FR-JRN-03 | Birthday automation | Done | template birthday | `TestP5CRMJourneyTemplates`; unit `TestNextBirthday` |
| FR-JRN-04 | Templates (welcome, win-back, post-event, abandoned booking) | Done | `journey/templates.go` | `TestP5CRMJourneyTemplates` |
| FR-JRN-05 | Consent, frequency cap, quiet hours, suppression (§16 #15) | Done | | `TestP5CRMJourneyRules`; unit `TestQuietUntil` |
| FR-JRN-06 | A/B and control group (Should) | Done | experiments | `TestP5CRMJourneyRules` |
| FR-JRN-07 | Performance per journey and step | Done | Journey Performance report | `TestP5CRMJourneyRenewal` |
| EP-19 AC | Renewal messages in order; stops after payment | Done | | `TestP5CRMJourneyRenewal` |

## EP-20 CRM & Sales Analytics

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-CRA-01 | NPS analytics | Done | `/crm/analytics/nps` | `TestP5CRMAnalytics` |
| FR-CRA-02 | Complaint SLA analytics | Done | `/crm/analytics/sla` | `TestP5CRMAnalytics` |
| FR-CRA-03 | Sales performance | Done | `/crm/analytics/sales` | `TestP5CRMAnalytics` |
| FR-CRA-04 | Commission analytics | Done | | `TestP5CRMAnalytics` |
| FR-CRA-05 | CLV and cohorts (Should) | Done | `/crm/analytics/clv` | `TestP5CRMAnalytics`; unit `TestCLV` |

## EP-21 Management Dashboard & BI

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-BI-01 | Analytics store, latency ≤ 15 min | Done | `reporting/p5_bi_store.go` (refresh every 10 min, latency shown) | `TestP5BIExecutiveOverview` |
| FR-BI-02 | Executive Overview across domains | Done | `/reporting/executive`, Management → Executive Overview | `TestP5BIExecutiveOverview`; Playwright §9.5 |
| FR-BI-03 | Targets per KPI, target vs actual (§16 #13) | Done | `/reporting/kpi-targets` | `TestP5BITargets` |
| FR-BI-04 | Drill-down to source transactions | Done | `/reporting/drilldown` | `TestP5BIExecutiveOverview`; Playwright §9.5 |
| FR-BI-05 | Scheduled reports | Done | `/reporting/scheduled-reports` | `TestP5BIScheduledReports` |
| FR-BI-06 | Report builder (Should) | Done | `/reporting/datasets` | `TestP5BIReportBuilderAndHR`, `TestP5GapsBIRegistry` |
| FR-BI-07 | One documented definition per KPI | Done | KPI registry | `TestP5BIExecutiveOverview`; unit `TestExecutiveCatalogueResolves` |
| FR-BI-08 | MoM / YoY and per property | Done | | `TestP5BIExecutiveOverview` |
| EP-21 AC 1 | Each executive KPI equals its source report | Done | | `TestP5BIExecutiveOverview` |
| EP-21 AC 2 | Golf Revenue → day → component → folio lines | Done | | `TestP5BIExecutiveOverview`; Playwright §9.5 |

## EP-22 Advanced Package Management

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PKG-P5-01 | Conditional multi-business components, sequence & gaps | Done | `commercial` P5 package | `TestP5LeisurePackageAdvanced`; unit `TestResolveChoices` |
| FR-PKG-P5-02 | Capacity & time blocks | Done | `/commercial/packages/{id}/capacity` | `TestP5LeisurePackageAdvanced` |
| FR-PKG-P5-03 | Inventory requirement (BOM) | Done | | `TestP5LeisurePackageAdvanced` |
| FR-PKG-P5-04 | Package profitability | Done | `/commercial/package-profitability` | `TestP5LeisurePackageAdvanced` |
| FR-PKG-P5-05 | Payment schedule template per package type | Done | | `TestP5LeisurePackageAdvanced` |

## EP-23 Advanced Tournament

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-TRN-P5-01 | Team formats with handicap allowances (§16 #11) | Done | `golf/tournament` P5 | `TestP5LeisureTeamFormats`; unit `TestTeamHandicaps`, `TestFormTeams` |
| FR-TRN-P5-02 | Series and Order of Merit | Done | `/golf/tournament-series` | `TestP5LeisureSeriesOrderOfMerit`; unit `TestSeriesEventPoints`; Playwright §9.6 |
| FR-TRN-P5-03 | Multi-round with cut and re-pairing | Done | | `TestP5LeisureSeriesOrderOfMerit` |
| FR-TRN-P5-04 | Tournament history | Done | `/golf/tournament-history` | `TestP5LeisureSeriesOrderOfMerit`; Playwright §9.6 |
| FR-TRN-P5-05 | Federation integration (Should) | Partial | PGI handicap indexes entered in bulk, result report | `TestP5LeisureRegistrationFederation` — no federation API (none available, §16 #11) |
| FR-TRN-P5-06 | Registration categories, quotas, early bird | Done | | `TestP5LeisureRegistrationFederation` |

## EP-24 HR & Workforce Policies

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-POL-P5-01 | Attendance Policy | Done | `hris/policies.go` (versioned, labels §7.6) | `TestP5HRPoliciesAndCatalog` |
| FR-POL-P5-02 | Leave Policy | Done | | `TestP5HRPoliciesAndCatalog`, `TestP5HRTimeLeave` |
| FR-POL-P5-03 | Overtime Policy | Done | | `TestP5HRPoliciesAndCatalog`, `TestP5HRTimeOvertime` |
| FR-POL-P5-04 | Payroll Configuration | Done | + `hris.payroll_processing` | `TestP5HRPoliciesAndCatalog`, `TestP5PayrollFullRun` |
| FR-POL-P5-05 | Service Charge Policy | Done | | `TestP5PayoutsServiceCharge` |
| FR-POL-P5-06 | Caddy Policies (payout part) | Done | Partner Payout Policy | `TestP5PayoutsCaddyAcceptance` |
| FR-POL-P5-07 | Policy version stored per run | Done | `policy_refs` of payroll and payout runs | `TestP5PayrollFullRun`, `TestP5PayoutsCaddyAcceptance` |

## EP-25 Integration P5

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-INT-P5-01 | Attendance devices through the bridge agent (§16 #7) | Done (mock provider for trial) | `hrtime/devices.go`, `/bridge/hris/attendance-events`, `:simulate`, `:sync-employees` | `TestP5HRTimeAttendance`, `TestP5GapsPartnerClockIn` — 🟡 the ZKTeco device on site through the bridge agent |
| FR-INT-P5-02 | Third-party payroll export | Not built by decision | §16 #1 Mode A | — |
| FR-INT-P5-03 | e-Bupot and BPJS files | Done | as FR-TAX-HR-04 | `TestP5PayrollFullRun` — 🟡 official templates |
| FR-INT-P5-04 | Bank payroll file | Done | generic CSV, BCA fixed width | `TestP5PayrollFullRun` — 🟡 BCA layout with the bank |
| FR-INT-P5-05 | Push for ESS and Member App (Should) | Done (mock provider for trial) | `platform/integration/p5_push.go` (Web Push VAPID, `mock-push`), `push-sw.js` in both PWAs | `TestP5GapsPush`; unit `TestEncryptPushRoundTrip`, `TestVAPIDToken` |
| FR-INT-P5-06 | P5 notification templates ID / EN | Done | `corehr/catalog.go`, `hrtime/catalog.go`, `talent/catalog.go`, `payroll/catalog.go`, journeys | `TestP5HRPoliciesAndCatalog`, `TestP5PayrollFullRun` |

## EP-26 Operational Interfaces & Apps P5

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-OPS-P5-01 | ESS and manager view in `ops` | Done | `/ops/ess` (`p5/hr.tsx`) | `TestP5HRSelfService`; Playwright *ESS on the phone*, §9.1 |
| FR-OPS-P5-02 | Attendance Kiosk on registered devices | Done | `/ops/attendance-kiosk` | `TestP5HRTimeAttendance` |
| FR-OPS-P5-03 | Caddy App: payout history & statement | Done | `/tablet/payouts` | `TestP5PayoutsCaddyRun` |
| FR-OPS-P5-04 | Instructor: My Sessions, Honor Statement | Done | `/ops/instructor/honor` | `TestP5PayoutsInstructorRun` |
| FR-OPS-P5-05 | ESS as PWA; payroll never cached offline | Done | `web/apps/staff/src/sw-cache.ts`; the service worker's matcher is self-contained (`cacheableApiPattern`: workbox copies its source into `sw.js`) | vitest `sw-cache.test.ts`; Playwright *ESS on the phone*, §9.1 (no payslip API in the cache), `shells.spec.ts` (app opens offline) |

## EP-27 HR KPI & Reports

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-RPT-P5-01 | HR Performance KPIs | Done | `RegisterHRKPI` (`reporting/p5_hr_core.go`, `p5_hr_time.go`, `p5_payroll.go`) | `TestP5GapsBIRegistry`, `TestP5PayrollFullRun`, `TestP5HRTimeSelfServiceAndReports` |
| FR-RPT-P5-02 | HR reports incl. payroll and payouts | Done | `RegisterP5Report` | `TestP5HRReports`, `TestP5HRTimeSelfServiceAndReports`, `TestP5PayrollFullRun`, `TestP5PayoutsCaddyRun`, `TestP5HRTalentPerformance` |
| FR-RPT-P5-03 | CRM, package and series reports | Done | | `TestP5GapsBIRegistry`, `TestP5CRMAnalytics`, `TestP5LeisurePackageAdvanced` |
| FR-RPT-P5-04 | Permission per report; salary reports for payroll roles; CSV / XLSX / PDF | Done | | `TestP5GapsBIRegistry`, `TestP5PayrollFullRun` |

## EP-28 Migration wave 5

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-MIG-P5-01 | Employees, organization, contracts, documents | Done | `oneclub import hris --grades --org-units --positions --employees --contracts --documents` | `TestP5HRImport`, `TestP5GapsHRImportOrganization` |
| FR-MIG-P5-02 | Leave balances at cutover | Done | `--leave-balances` | `TestP5HRTimeLeave` |
| FR-MIG-P5-03 | Salary structure, BPJS numbers, PTKP, payroll YTD | Done | employee import (PTKP, BPJS), structures, `--payroll-ytd` | `TestP5HRImport`, `TestP5PayrollFullRun` |
| FR-MIG-P5-04 | Certifications | Done | `--certifications` | `TestP5HRImport` |
| FR-MIG-P5-05 | Reconciliation: headcount, leave balances, payroll totals of the last legacy period = parallel run; HR & Finance sign-off | Done | `hris/p5_reconcile.go` metric registry; Core HR `headcount`, `leave_balance`; payroll `payroll_gross`, `payroll_net` (`hris/payroll/reconcile.go`: key TOTAL = last legacy payroll period on or before the cutover, or a period YYYY-MM); HRIS → Migration Reconciliation; `--reconcile` | `TestP5GapsReconciliation`, `TestP5ClosePayrollReconciliation`; Playwright *EP-28 migration* |
| FR-MIG-P5-06 | Parallel run ≥ 1 period; dry run ×2 on Staging | Done | `--legacy-payroll`, `/parallel-run`, `:sign-off-parallel-run` | `TestP5PayrollFullRun` — 🟡 dry runs on Staging |

## EP-29 Production Readiness (Release 5)

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-REL-P5-01 | k6: clock-in burst, payroll run, journey 10k, BI 2 years | Done | `test/load/clockin-shift-change.js`, `payroll-run.js`, `journey-10k.js`, `bi-2y.js`; runbook §4 | 🟡 runs on Staging with results recorded |
| FR-REL-P5-02 | Playwright for the §9 flows | Partial | `web/e2e/p5-flows.spec.ts`: §9.1 Hire to First Pay through payroll (posted, payslip in the new employee's ESS), ESS, §9.4, §9.5, §9.6, EP-28 sign-off | Playwright run of the close-out — §9.2 and §9.3 run as Go acceptance tests only (open item) |
| FR-REL-P5-03 | P1–P4 regression | Done | full suite | P0–P5 Go e2e and all Playwright specs |
| FR-REL-P5-04 | Payroll / service charge / payout / journal integration tests | Done | | `TestP5Payroll*`, `TestP5Payouts*`, `TestP5Close*` |
| FR-REL-P5-05 | Pen test scope | Done | `docs/security/p5-pentest-checklist.md` | 🟡 external pen test |
| FR-REL-P5-06 | DPIA | Done | `docs/security/p5-dpia.md` | 🟡 signature of the club's DPO |
| FR-REL-P5-07 | VPS build with garble, no source maps | Done | `deploy/docker/Dockerfile` (`OBFUSCATE=true`), `SOURCEMAPS=false` | staging workflow |
| FR-REL-P5-08 | Training and hypercare (proposal) | Done | `docs/runbooks/production-readiness.md` §7 Release 5 | 🟡 execution with the club |

## §12 Non-functional requirements

| Area | Requirement | Status | Evidence |
|---|---|---|---|
| Availability | Kiosk and ESS work offline (queue) | Done | kiosk offline sync `TestP5HRTimeAttendance`; ESS schedule offline; `shells.spec.ts` offline app |
| Performance | Clock-in < 2 s; payroll run < 5 min; BI p95 < 5 s | Done | k6 thresholds in `clockin-shift-change.js`, `payroll-run.js`, `bi-2y.js` — 🟡 Staging runs |
| Data latency | Analytics store ≤ 15 min; last data time shown | Done | `TestP5BIExecutiveOverview` |
| Payroll integrity | Approved immutable; journal = run; distribution = pool; payout = settlement | Done | `TestP5PayrollFullRun`, `TestP5PayrollRetroAdjustment`, `TestP5PayoutsServiceCharge`, `TestP5PayoutsCaddyAcceptance` |
| Compliance | Versioned PPh 21 / BPJS / THR / overtime / leave tables | Done | statutory rate sets, policies `TestP5PayrollStructuresAndRates`, `TestP5HRPoliciesAndCatalog` |
| Privacy | No biometrics stored; salary & NIK / NPWP encrypted at rest (proposal) and masked; DPIA | Partial | masking and no templates `TestP5HREmployees`, `TestP5HRTimeAttendance`; column-level encryption of salary / NIK / NPWP not built (volume / database encryption of the deployment) — open item |
| Security | ASVS L2; optional MFA for ESS; two-person review of payroll rules | Done | optional MFA of the platform; two-person activation `TestP5PayrollStructuresAndRates` |
| Retention | Payroll & tax ≥ 10 years; audit ≥ 5 years | Done | nothing purges payroll; applicant retention `TestP5HRTalentRecruitment` |
| Device | Personal phones (PWA), kiosk tablets, devices via bridge | Done | Playwright mobile contexts; `TestP5HRTimeAttendance` |
| Regression | P1–P4 suites on every merge | Done | CI `e2e` and `browser` jobs |
| Language | English labels, Indonesian helper text and HR documents | Done | templates ID / EN `TestP5HRPoliciesAndCatalog` |

## §16 Decisions

| # | Decision | Status | Implementation | Evidence |
|---|---|---|---|---|
| §16 #1 | Mode A: payroll fully in OneClub | Done | `hris/payroll` | `TestP5PayrollFullRun` |
| §16 #2 | Wave 3 = P5 ‖ P6 | n/a | process | — |
| §16 #3 | PKWT, probation, leave, overtime, THR, severance rules | Done | HR Configuration, Leave / Overtime Policies, Payroll Configuration | `TestP5HRContracts`, `TestP5HRTimeLeave`, `TestP5HRTimeOvertime`, `TestP5PayrollTHRAndFinalSettlement` |
| §16 #4 | Caddies & instructors are partners: PPh 21 non-employee 50 % × Art. 17, BPU, payout schedule | Done | Partner Payout Policy | `TestP5PayoutsCaddyAcceptance`, `TestP5PayoutsInstructorRun`; unit `TestPartnerPPh21`, `TestPartnerPeriods` |
| §16 #5 | Service charge 95 / 5, equal share × attendance, eligibility | Done | Service Charge Policy | `TestP5PayoutsServiceCharge`; unit `TestServiceChargeEligibilityAndReserve` |
| §16 #6 | ESS in the `ops` shell | Done | `/ops/ess` | Playwright *ESS on the phone* |
| §16 #7 | Face + fingerprint devices at 6 points, GPS 300 m | Done | device registry, geofences, demo | `TestP5HRTimeAttendance` |
| §16 #8 | Organization and grades G1–G7 | Done | demo seed `corehr/demo.go` | `TestP5HRTalentDemo`, `TestP5HREmployees` |
| §16 #9 | Mandatory certifications per position | Done | demo certification types | `TestP5HRCertificationsAndTraining` |
| §16 #10 | Retention: applicants 1 year, payroll 10 years, biometrics on devices ≤ 30 days after exit | Partial | applicant retention job; payroll kept; HR Configuration `biometricDaysAfterExit` + offboarding item *biometric_removal* | `TestP5HRTalentRecruitment` — the removal from devices is a checklist item, not an automatic job (open item) |
| §16 #11 | PGI manual handicap; Scramble and Four-ball first | Done | | `TestP5LeisureTeamFormats`, `TestP5LeisureRegistrationFederation` |
| §16 #12 | Waves R5.1–R5.4; Should may move | n/a | module flags | — |
| §16 #13 | KPI targets from the annual budget | Done | `/reporting/kpi-targets` | `TestP5BITargets` |
| §16 #14 | Silver / Gold / Platinum, benefits, grace, 2 % budget | Done | | `TestP5CRMTierProgram`, `TestP5CRMRewards` |
| §16 #15 | Journey priorities, frequency cap 2 / 6, quiet hours | Done | | `TestP5CRMJourneyTemplates`, `TestP5CRMJourneyRules` |
| §16 #16 | Labels and statuses of §7.6 | Done | policy and menu labels | `TestP5HRPoliciesAndCatalog`, `TestStaffAppAreas` |

## §9 End-to-end flows

| Flow | Status | Evidence (hop by hop) |
|---|---|---|
| §9.1 Hire to First Pay | Done | requisition → candidate → interview → offer → hired (employee + PKWT + ESS login) → lifeguard certificates → pool shift (refused before the certificates) → face recognition clock-in → 2 h overtime approved `TestP5GapsHireToWork` → payroll run with proration, overtime, BPJS, PPh 21 → approval → posting (journal) → payslip in ESS → bank file → paid `TestP5PayrollFullRun`; in the browser `p5-flows.spec.ts` *§9.1 hire to first pay* (on a property of its own: recruitment → certificates → pool shift → device clock-in → overtime → Payroll screen Calculate / Submit / Post → the new employee's first login on the phone → Payslip of the period with the overtime line) |
| §9.2 Service charge & commission | Done | pool → distribution per department and attendance → approval → payroll → liability 0 `TestP5PayoutsServiceCharge`; commission statement → payroll → statement Paid, clawback next period `TestP5PayoutsCommissionBonus` (Go acceptance; no Playwright spec) |
| §9.3 Caddy & instructor payout | Done | settlement + non-cash tip → caddy payout run → Finance approval → statement in the Caddy App → bank file → liability 0 `TestP5PayoutsCaddyRun`, `TestP5PayoutsCaddyAcceptance`; instructor fees → partner run / payroll `TestP5PayoutsInstructorRun` (Go acceptance; no Playwright spec) |
| §9.4 Retention journey | Done | At Risk segment `TestP5CRMAnalytics` → win-back journey with voucher / points branches `TestP5CRMJourneyTemplates`, `TestP5CRMJourneyRules` → tier evaluation `TestP5CRMTierProgram` → Journey Performance report; Playwright *§9.4* |
| §9.5 Executive review | Done | Executive Overview vs target → drill-down → NPS `TestP5BIExecutiveOverview`, `TestP5CRMAnalytics` → scheduled report, next month's target `TestP5BIScheduledReports`, `TestP5BITargets`; Playwright *§9.5* |
| §9.6 Tournament series | Done | four-event series, Four-ball event, Order of Merit, multi-round final with cut, champion, history `TestP5LeisureSeriesOrderOfMerit`, `TestP5LeisureTeamFormats`; Playwright *§9.6* |

## §11 Outbox events

| Event (PRD) | Status | Producer | Evidence |
|---|---|---|---|
| `hris.employee_hired` | Done | `internal/hris/hris.go` | `TestP5HRTalentRecruitment`, `TestP5HREmployees` |
| `hris.employee_terminated` | Done | `hris/corehr/employees.go` (H7) | `TestP5HREmployees` |
| `hris.contract_expiring` | Done | `corehr/jobs.go` | `TestP5HRContracts` |
| `hris.certification_expired` | Done | `corehr/jobs.go` (H7) | `TestP5HRCertificationsAndTraining` |
| `hris.schedule_published` | Done | `hris/p5_time.go` | `TestP5HRTimeSchedules` |
| `hris.attendance_recorded` | Done | `hris/p5_time.go` | `TestP5HRTimeAttendance` |
| `hris.leave_approved` | Done | `hris/p5_time.go` | `TestP5HRTimeLeave` |
| `hris.overtime_approved` | Done | `hris/p5_time.go` | `TestP5HRTimeOvertime` |
| `hris.payroll_calculated` | Done | `hris/payroll` | `TestP5PayrollFullRun` |
| `hris.payroll_posted` | Done | `hris/payroll` (H5) | `TestP5PayrollFullRun` (payload and journal) |
| `hris.payroll_paid` | Done | `hris/payroll` | `TestP5PayrollFullRun` |
| `hris.payout_posted` | Done | `hris/payouts` (H5) | `TestP5PayoutsCaddyRun`, `TestP5PayoutsCaddyAcceptance` |
| `hris.payout_paid` | Done | `hris/payouts` | `TestP5PayoutsCaddyRun` (mark paid, bank journal) |
| `crm.journey_step_executed` | Done | `crm/journey/journey.go` | `TestP5CRMJourneyRenewal`, `TestP5CRMJourneyRules` |
| `crm.tier_evaluated` | Done | `crm/loyalty/p5_tiers.go` | `TestP5CRMTierProgram` |
| `crm.reward_issued` | Done | `crm/loyalty/p5_tiers.go` | `TestP5CRMRewards` |
| `golf.series_standing_updated` | Done | `golf/tournament/p5_series.go` | `TestP5LeisureSeriesOrderOfMerit` |

## Open / proposals

| # | Item | Why open | Owner (suggested) |
|---|---|---|---|
| 1 | FR-TAX-HR-05, EP-10 AC 2: the tax consultant's test cases (PPh 21 TER / annual, BPJS caps, THR, severance) run against the engine; then `:verify` the statutory rate set `ID-2024` (every run warns until then) | needs the consultant's case table | Club Finance + tax consultant |
| 2 | FR-TAX-HR-04 / FR-INT-P5-03: compare the e-Bupot, BPJS (SIPP / EDABU) and 1721-A1 exports with the current official import templates | templates change; no access to DJP / BPJS portals here | Club HR + payroll area |
| 3 | FR-PPY-02 / FR-INT-P5-04: verify the `bca_payroll` fixed-width layout with BCA (KlikBCA Bisnis payroll upload) and add the club's other banks if needed | bank specification not available | Club Finance + payroll area |
| 4 | FR-PPY-03: payslip PDF "protected" — today access-controlled (own payslips, `private, no-store`, never cached) but without a password. Proposal: password = employee number + date of birth (PDF standard security, AES-256), configurable in the Payroll Configuration | needs a PDF encryption implementation without new dependencies and a product-owner decision on the password rule | Product owner + payroll area |
| 5 | FR-ATT-08: partner clock-in at the Attendance Kiosk by QR / PIN (devices work today) | the kiosk identifies employees only; needs partner PINs / QR on the partner profile | HR time area |
| 6 | §16 #10: automatic removal of a leaver's templates from the devices within 30 days (today the checklist item *biometric_removal* and the device employee sync) | device vendor API decision (OQ #7) | HR time area + club IT |
| 7 | §12 Privacy: column-level encryption of salary, NIK and NPWP (PRD *usulan*); today masking, permissions and encrypted volumes / backups | product-owner decision on key management and its cost on reports | Product owner + platform |
| 8 | FR-REL-P5-02: Playwright specs for §9.2 (service charge & commission) and §9.3 (caddy & instructor payout); both are covered hop by hop by Go acceptance tests | not required by the close-out brief | Engineering (QA) |
| 9 | FR-TRN-P5-05: federation API (PGI) when one becomes available | no API (§16 #11) | Golf area |
| 10 | 🟡 Release 5 k6 runs on Staging with results recorded (`clockin-shift-change.js`, `payroll-run.js`, `journey-10k.js`, `bi-2y.js`); external pen test of `p5-pentest-checklist.md`; DPIA signed; payroll dry run ×2 and one parallel period with sign-off; UAT of §9 signed by HR Manager, Finance Manager and GM | environment / people | OneClub ops + club |
| 11 | Real providers before production: attendance devices through the bridge agent (trial on the mock adapter), Web Push VAPID key per instance (trial on `mock-push` / the log pusher) | configuration per instance | OneClub ops + club IT |

## Product decisions pending

Awaiting the product owner; the code ships the values below as defaults or proposals.

1. Payslip PDF password rule (open item 4).
2. Column-level encryption of salary / NIK / NPWP (open item 7).
3. Offboarding: "Company assets returned" cannot be ticked while assets are still in the leaver's custody on the asset
   register (implemented; *N/A* remains possible) — confirm that HR should not be able to override it.
4. HR migration reconciliation of payroll: totals are compared for gross and net pay of the last legacy period (key
   TOTAL) or a named period; per-employee and PPh 21 / BPJS totals are read in the payroll *Parallel Run* tab — confirm
   that this split matches the club's sign-off form.
