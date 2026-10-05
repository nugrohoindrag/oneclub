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
