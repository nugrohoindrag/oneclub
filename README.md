# OneClub

Golf & country club platform for Modern Golf & Country Club. **P0–P4 are implemented on staging**: P0 Platform
Foundation, P1 Golf Core MVP (Release 1), P2 Club Operations (Release 2), P3 Commercial & Business Expansion
(Release 3) and P4 Enterprise Back Office (Release 4).

| Part | Path | Stack |
|---|---|---|
| Backend (modular monolith, one binary) | `cmd/`, `internal/`, `db/migrations/` | Go 1.27, chi, pgx, goose, River, PostgreSQL 18 |
| API contract | `api/openapi/openapi.json` | OpenAPI 3.0, generated from the route registry per build |
| Frontend monorepo | `web/` | pnpm, React 19, Vite, Next.js, TanStack Query, Morphic Design System |
| Deployment | `deploy/` | Docker, Docker Compose (no Kubernetes), Caddy, pgBackRest |
| CI/CD | `.github/workflows/` | lint, arch-lint, unit, integration, OpenAPI drift/breaking check, build, scan, deploy |

Product documents (Product Overview, PRD P3/P4, Technical Documentation, Naming Convention, roadmap) are in
[`docs/product/`](docs/product/README.md). The original Morphic design system source is kept in `design-system/` (restored as `web/packages/ui`).
Requirement → code → test mapping: [P0](docs/p0-traceability.md), [P1](docs/p1-traceability.md),
[P2](docs/p2-traceability.md), [P3](docs/p3-traceability.md), [P4](docs/p4-traceability.md). Rhapsody migration:
[`docs/migration/`](docs/migration/); go-live: [`docs/runbooks/production-readiness.md`](docs/runbooks/production-readiness.md).

## Quick start (local)

Prerequisites: Go 1.27+, Node 24 + pnpm 10, PostgreSQL 18 (or `docker compose -f deploy/compose/dev/compose.yaml up -d`).

```bash
make build
make provision-dev            # creates database oneclub_mgcc, roles, migrations, seeds, Super Admin
# copy the printed DATABASE_URL / DATABASE_OWNER_URL / DATABASE_REPLICA_URL / APP_SECRET into .env.local
make seed-demo                # demo properties, venues, users per role (password Demo#Club2026, PIN 246810)
make api & make worker        # API on :8080, River worker
cd web && pnpm install && pnpm dev   # staff :5173, member :5174, web :3000
```

See [`docs/runbooks/local-development.md`](docs/runbooks/local-development.md). Team workflow (branches, pull requests,
resolving generated-file and migration conflicts) and CI/CD setup: [`docs/runbooks/ci-cd.md`](docs/runbooks/ci-cd.md).

## Applications (application surfaces)

| Application | App | Port (dev) | Who |
|---|---|---|---|
| Staff App (PWA), one build on four domains: `dashboard` (Back Office `/`, Management Dashboard `/management`, Platform Administration `/platform`, Clubhouse Screen `/screen`), `cashier` (Operational `/ops`: offline, device + PIN), `caddy` (Caddy Tablet `/tablet`: offline), `kitchen` (Kitchen Display `/kitchen`) | `web/apps/staff` | 5173 (`<domain>.localhost:5173` per domain) | every staff member; the domain locks the areas, the role decides which open |
| Member & Guest Portal (PWA) | `web/apps/member` | 5174 | members, guests |
| Website (Next.js SSR) | `web/apps/web` | 3000 | public |
| Morphic showcase | `web/packages/ui` (`pnpm showcase`) | 5199 | designers, engineers |

## Tests

```bash
make unit                                  # Go unit + architecture boundary tests
make e2e                                   # P0–P5 acceptance tests on a real PostgreSQL (fresh instances per run)
cd web && pnpm -r typecheck && pnpm -r test
cd web && pnpm exec playwright test        # browser tests of every app, Staff App area and domain, golf flow, offline POS and tablet, P3–P5 specs (needs running API + previews)
k6 run test/load/teetime-rush.js           # load tests (see docs/runbooks/production-readiness.md §4)
```

`oneclub seed-demo` (dev/staging) seeds the Modern Golf course (MGC, 18 holes, 6,350 m), tee sheet templates, the
2026 rate card, membership types, caddies, golf carts, lockers, a demo member (`member@demo.oneclub.id`, Member No.
D0001) and one staff user per P1 role (`golf.admin@`, `caddy.master@`, `front.desk@`, `reservation@`, `golf.staff@`,
`membership@`, `membership.admin@demo.oneclub.id`).

## Release 3–4 (P3 Commercial, P4 Enterprise Back Office)

| Release | Modules (`internal/`) | Staff App | Key flows |
|---|---|---|---|
| P3 | `crm/sales` (leads, pipeline, quotations, commission), `crm/engagement` (segments, campaigns, tickets, NPS), `crm/loyalty`, `crm/topspender`, `banquet` (events, BEO, venues), `golf/tournament`; P3 files in `commercial` (promotions, packages) and `billing` (`p3_*.go`: customer folio, payment schedules, invoices, corporate AR, cashier shift, night audit) | `web/apps/staff/src/p3/` | wedding (lead → quotation with one-time code → DP online → BEO → final billing → commission), corporate event package, club tournament, promotion & loyalty at the POS, night audit |
| P4 | `inventory` (`p4_*.go`), `procurement`, `accounting`, `cms`; P4 adapters in `platform/integration/p4_*.go` | `web/apps/staff/src/p4/` | procure-to-pay, banquet supply, POS → stock → journal, month-end close, website update from the CMS |

Module wiring lives in `internal/app/p3_*.go` / `p4_*.go`; event contracts between the two releases in
[`docs/p3-p4-contracts.md`](docs/p3-p4-contracts.md). Status per requirement, open items and the product decisions
still pending: [`docs/p3-traceability.md`](docs/p3-traceability.md), [`docs/p4-traceability.md`](docs/p4-traceability.md).

**Mock providers for the trial.** The integration layer ships sandbox adapters. `oneclub instance create` enables
`mock-payment` (payment links, DP / invoice payments), `mock-whatsapp` and `mock-email` (notifications, campaigns
and the one-time quotation-acceptance codes); `seed-demo` enables `mock-emeterai` (e-Meterai on quotations above
Rp5 jt). `mock-efaktur` (e-Faktur / Coretax), `mock-resident`, `mock-website` and `mock-captcha` are added by the
Platform Admin under Integrations when needed. Real adapters (Xendit, WhatsApp Cloud, SendGrid / SMTP, Coretax via
PJAP, Modernland registry, Next.js revalidation, Turnstile) are configured per instance the same way; e-Meterai has
no production adapter yet.

**Tests.** Go: `make e2e` (`ADMIN_URL` = a superuser URL of the PostgreSQL 18 server; one area while iterating:
`ONECLUB_TEST_ADMIN_URL=<admin url> ONECLUB_REQUIRE_FULL_COVERAGE=false go test -count=1 -run TestP4FixLedger ./test/e2e/`);
P3/P4 tests are `test/e2e/p3*_test.go` and `p4*_test.go`. Browser (`web/e2e/p3.spec.ts`, `p4.spec.ts`,
`p4fix-website.spec.ts` and the earlier specs): start the stack (API + worker on :8080, `pnpm --filter @oneclub/staff
preview`, `… member preview`, website on :3000 — or `.github/scripts/browser-stack.sh` as in CI), then in `web/`
`PSQL=psql DEMO_DEVICE_TOKEN=<token from seed-demo> pnpm e2e`. The config uses the installed Google Chrome
(`channel: 'chrome'`); set `PW_CHANNEL=chromium` for Playwright's own Chromium (`pnpm exec playwright install
chromium`). With a pre-installed Chromium whose revision differs from the one Playwright expects, run with a local
config that spreads `web/playwright.config.ts` and sets `use.channel: undefined` and
`use.launchOptions.executablePath` to that browser. Details: [`docs/runbooks/ci-cd.md`](docs/runbooks/ci-cd.md).
Load scenarios of both releases: [`production-readiness.md`](docs/runbooks/production-readiness.md) §4.

**Migration and cut-over runbooks.** Release 3: [banquet](docs/runbooks/banquet-migration-p3.md),
[corporate AR](docs/runbooks/corporate-ar-migration-p3.md), [tournaments](docs/runbooks/tournament-migration-p3.md),
[leads](docs/runbooks/leads-migration-p3.md). Release 4: [inventory](docs/runbooks/inventory-migration-p4.md),
[procurement](docs/runbooks/procurement-migration-p4.md), [accounting](docs/runbooks/accounting-migration-p4.md).
Database migrations are per module under `db/migrations/<module>/`; `oneclub migrate up` applies them and then
synchronises the permission catalogue (new permissions and role templates such as Auditor).

**Deployment notes for existing environments.**

- After deploying this release, run `POST /api/v1/accounting/posting-rules:generate-defaults` once per instance
  (Super Admin or a user with `accounting.posting_rule.create`). It is idempotent — it only adds the default posting
  rules that are missing, such as `DEF-INV-OPENING` (opening stock to opening balance equity), `DEF-COMMISSION`
  (sales commission) and the asset disposal rules (`DEF-ASSET-DISP-*`); existing and edited rules are left alone.
- Bungalow room charge is **nightly by default** (Stay Policies `roomChargePosting = "nightly"`): the night audit
  posts each in-house night and the check-out posts the nights left; a website / app booking pays its deposit online as a
  held deposit. Instances without a saved Stay Policies version (demo, trial and new instances — neither provisioning
  nor the demo seed saves one) switch automatically; no migration is needed. Stays booked before the deploy keep the
  posting they were booked with (`stay.stays.room_posting`). An instance that saved Stay Policies with
  `roomChargePosting = "at_booking"` keeps it — change it in *Settings → Club Policies → Stay Policies* if the club
  wants nightly posting; `at_booking` remains a supported option.

## Release 5 (P5 People & Advanced Enterprise)

| Area | Modules (`internal/`) | Staff App / apps | Key flows |
|---|---|---|---|
| People | `hris` (layer 4): `corehr` (organization, employees, contracts, documents, certifications & training, ESS, migration reconciliation), `talent` (recruitment, performance review), `hrtime` (schedules, attendance & devices, leave, overtime), `payroll` (salary structures, runs, PPh 21 & BPJS, payslips, bank file, statutory exports, parallel run), `payouts` (service charge, commission & bonus, caddy & instructor payout runs) | `web/apps/staff/src/p5/{hr,hr_talent,hr_time,payroll,payouts,gaps}.tsx`: HRIS in the Back Office; Employee Self Service `/ops/ess` (personal login, PWA), Attendance Kiosk `/ops/attendance-kiosk`, Honor Statement `/ops/instructor/honor`, Caddy Tablet *Payouts*; careers page on the website | §9.1 hire to first pay, §9.2 service charge & commission, §9.3 caddy & instructor payout |
| Advanced CRM & BI | `crm/journey`, `crm/analytics`, `crm/loyalty/p5_*` (tiers Silver / Gold / Platinum, rewards), `reporting/p5_*` (analytics store, Executive Overview, KPI targets, drill-down, scheduled reports, report builder, HR Performance) | `p5/{crm,tiers,bi}.tsx`; Member App `areas/crm_p5.tsx` (tier, rewards, offers) | §9.4 retention journey, §9.5 executive review |
| Package & tournament | `commercial` (advanced packages), `golf/tournament` (`p5_*`: team formats, series & Order of Merit, history) | `p5/leisure.tsx`; Member App `areas/leisure.tsx` | §9.6 tournament series |

Wiring: `internal/app/p5_*.go`; module contracts (H1–H7, events, hooks): [`docs/p5-contracts.md`](docs/p5-contracts.md);
status per requirement, open items and pending product decisions: [`docs/p5-traceability.md`](docs/p5-traceability.md).
Payroll is fully in OneClub (Mode A). The statutory rate set `ID-2024` (PPh 21 TER / Article 17, PTKP, BPJS) is shipped
*unverified*: every payroll run shows a warning until the tax consultant verifies it (`:verify` on HRIS → Payroll →
Statutory Rates).

**Demo and trial data.** `oneclub seed-demo` adds the P5 demo to MAIN: organization with grades G1–G7, employees with
contracts, documents and certifications (§16 #8–#9), four weeks of published rosters and attendance with leave and
overtime requests, a recruitment pipeline and a review cycle, last month's payroll posted and paid and this month's
calculated, last month's service charge distribution, a paid caddy payout run and a calculated instructor payout run,
journeys, member tiers, the KPI target plan of the year and three scheduled management reports.
P5 demo users (password `Demo#Club2026`): `hr@` (HR Manager), `hr.admin@`, `dept.head@` (F&B Manager, approvals in ESS),
`employee@` (Employee Self Service) `demo.oneclub.id`; `gm@`, `finance@`, `golf.manager@` and `caddy.master@` are linked to
their employee profiles. `oneclub seed-demo --trial` adds the P5 history (`internal/app/trial_p5.go`): weekly rosters,
device clock-ins, overtime and the daily attendance job, partner caddies clocking in on the caddy house device, the daily
journey run, RFM / VIP refresh, monthly tier evaluation and a final analytics store refresh.

**Migration (`oneclub import hris`).** One command loads the HR master data and the payroll opening data of a property
from CSV (columns in `--help`), each with `--dry-run`:

```bash
./bin/oneclub import hris -property MAIN --grades grades.csv --org-units units.csv --positions positions.csv \
  --employees employees.csv --contracts contracts.csv --documents documents.csv --documents-dir scans/ \
  --certifications certs.csv --leave-balances leave.csv --payroll-ytd ytd.csv [--dry-run]
./bin/oneclub import hris -property MAIN --legacy-payroll legacy-2026-09.csv --period 2026-09     # parallel run
./bin/oneclub import hris -property MAIN --reconcile control-totals.csv --cutover 2026-10-01 \
  --legacy-system "HR Excel" --out reports/                                                      # FR-MIG-P5-05
```

The reconciliation CSV is `metric,key,legacy` with the metrics `headcount` (TOTAL or org unit code), `leave_balance`
(TOTAL, leave type or EMPLOYEENO:TYPE), `payroll_gross` and `payroll_net` (TOTAL = the last legacy payroll period on or
before the cutover, i.e. the parallel run, or a period YYYY-MM); the HR Manager and the Finance Manager sign it off in
HRIS → Migration Reconciliation.

**Push notifications.** ESS (Staff App) and the Member App subscribe with the browser Push API (`push-sw.js` of both
PWAs; `/api/v1/platform/push-subscriptions`); in-app notifications are mirrored to subscribed devices unless the user
opts out. Configure per instance under *Platform Administration → Integrations*: **Web Push (VAPID)** (`webpush`:
credential *VAPID private key* — the raw 32-byte P-256 private key, base64url — and setting *Contact (VAPID subject)*, `mailto:` or
https) for production; `mock-push` records pushes in the integration log for the trial. Without a push integration a log
pusher is used outside production, with a VAPID key derived from the instance secret so devices can still subscribe.
iOS needs the PWA installed (Safari 16.4+).

**Tests.** Go: `test/e2e/p5_*_test.go` (one area while iterating, e.g. `ONECLUB_TEST_ADMIN_URL=<admin url>
ONECLUB_COVERAGE_PATHS=/api/v1/hris/payroll-runs go test -count=1 -run TestP5Payroll ./test/e2e/`); payroll worked
examples in `internal/hris/p5_payroll_calc_test.go` and `p5_payroll_retro_test.go`. Browser: `web/e2e/p5-flows.spec.ts`
(§9.1 hire to first pay through the payslip in ESS, ESS on the phone, §9.4–§9.6, migration sign-off) with the stack of
the P3/P4 section; with a pre-installed Chromium set `PW_EXECUTABLE_PATH=/path/to/chrome` (the config then drops the
Chrome channel). The §9.1 spec creates a property of its own per run, so it repeats on Staging; the wedding flow of
`p34-flows.spec.ts` reads `APP_SECRET` (`E2E_APP_SECRET` or the stack's `.env.local`) to set the customer's one-time code.

**Release 5 runbooks.** Load scenarios `clockin-shift-change.js`, `payroll-run.js`, `journey-10k.js`, `bi-2y.js`
([`production-readiness.md`](docs/runbooks/production-readiness.md) §4 Release 5), training per role and hypercare of
one payroll period (§7), go-live checklist incl. the HR migration reconciliation sign-off (§8); security:
[`p5-pentest-checklist.md`](docs/security/p5-pentest-checklist.md), DPIA [`p5-dpia.md`](docs/security/p5-dpia.md). Cut-over
order for HR: master data import → leave balances and payroll YTD at the cutover → one parallel payroll period
(`--legacy-payroll`, run tab *Parallel Run*, sign-off by HR and Finance) → reconciliation sign-off → first live payroll.

## Trial dataset

`oneclub seed-demo --trial` (after the demo configuration, on a **fresh** instance) makes a trial instance look alive:
it simulates the last 90 business days of the main property and books the next 30, so dashboards, reports, KPIs,
the books, CRM and the Member App show meaningful, internally consistent numbers. Stop the worker of the instance
while it runs (the seeder dispatches the events itself at the simulated time); it takes a few minutes.

```bash
./bin/oneclub seed-demo --trial                 # -days 90 -ahead 30 -seed 20260401 (defaults)
```

- **What is generated**: go-live (books, periods, bank accounts and opening balances, Rhapsody migration of
  customers, corporates and members, opening stock), then every day golf bookings, check-in, caddies, carts,
  scorecards and the driving range, POS sales in five outlets consuming stock by recipe, sport club visits,
  classes and instructor fees, bungalow stays, banquet events with BEOs, membership applications and renewals,
  CRM leads → quotations → commission, tickets, surveys / NPS, loyalty, campaigns, a completed and an upcoming
  tournament, promotions and Family Day packages, purchasing (PR → PO → GR → vendor invoice → payment run), stock
  opname, member statements and settlements, corporate invoices, e-Faktur upload (`mock-efaktur`), cashier shifts
  and the Night Audit of every day, the monthly bank reconciliation and a closed month. Trial records use `TRL`
  codes; the trial staff users (password and PIN of the demo) are printed at the end with a row count per module.
- **How**: through the modules' own use cases (the in-process API as demo users), deterministic (fixed seed) and
  idempotent (a marker per step in the audit log; re-running adds nothing), relative to the club's business date.
  The application clock follows the simulated time; timestamps the database sets itself are moved to the
  simulated moment except in append-only tables.
- **Extension point**: an area adds `internal/app/trial_<area>.go` with
  `registerTrialSeeder(trialSeeder{Name, Order, Setup, Day, Final})` in an `init()` and its main tables in
  `trialCoverageTables`; see the header of `internal/app/trial.go`.
- **Test**: `TestTrialDataset` (60 days; `ONECLUB_TRIAL_DAYS=90` for the CLI history) asserts the coverage, a
  balanced trial balance, stock valuation = GL inventory, no posting exceptions or outbox failures, a closed month
  and that a second run adds nothing.

## Binary commands

```
oneclub api | worker | migrate up|status|down <module> | instance create|drop | seed-demo [--trial [-days N] [-ahead N] [-seed N]]
oneclub import rhapsody stage|validate|load|reconcile -property CODE [-dir DIR] [-out DIR]
oneclub openapi [-o file] | healthcheck | version
```
