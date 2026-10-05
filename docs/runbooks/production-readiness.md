# Production readiness — Release 1 (Golf Core MVP)

PRD P1 EP-19 (FR-REL-01..09) and Exit Criterion #8. Each item has an owner and evidence; the go-live checklist at the end
must be complete before the cutover (`docs/migration/cutover-runbook.md`).

## 1. Production instance (FR-REL-01)

Provision with `docs/runbooks/instance-provisioning.md` on the chosen hosting (P0 Open Question #1), instance code `mgcc`:

```bash
oneclub instance create -admin-url "postgres://postgres:***@db-host:5432/postgres?sslmode=require"   -code mgcc -name "Modern Golf & Country Club" -property-code MAIN -property-name "Modern Golf & Country Club"   -locale id -currency IDR -timezone Asia/Jakarta -super-admin-email it@moderngolf.id -platform-admin-email platform@oneclub.id   -public-url https://dashboard.moderngolf.id -bundle-dir /srv/oneclub/instances -db-host db
```

Then configure (not `seed-demo`, which is disabled in production): course structure and templates, rate card, tax,
membership configuration, payment methods (Xendit live keys in Integrations), WhatsApp BSP, approval workflows,
Club Policies, staff users and devices.

## 2. Database high availability (FR-REL-02)

Decision (Technical Doc §10.5 recommendation): **Option A — self-hosted primary + streaming replica, semi-manual
failover with this runbook**, target recovery < 30 minutes. Re-evaluate Option B (Patroni) or C (managed PostgreSQL in
an Indonesian region) before the second customer.

Failover procedure (drill recorded before go-live, FR-REL-08):

1. Confirm the primary is down (`docker compose -p oneclub-db ps`, host unreachable) — do not fail over on a network blip (< 2 min).
2. Promote the replica: `docker compose -p oneclub-db exec -u postgres replica pg_ctl promote -D /var/lib/postgresql/data`.
3. Point `DATABASE_URL` / `DATABASE_OWNER_URL` of the instance to the promoted host; `deploy/scripts/deploy.sh` restart api + worker.
4. `deploy/scripts/smoke-test.sh` and check `/readyz`.
5. Rebuild a new replica from the promoted primary (`pg_basebackup`), re-enable `archive_command` / pgBackRest stanza.
6. Record times in the drill log below.

| Drill | Date | Detection → promoted | Promoted → API healthy | Data loss | Operator |
|---|---|---|---|---|---|
| Failover drill 1 | | | | | |
| Restore drill (`restore-drill.sh`, PITR) | | | | | |

## 3. Minimum monitoring (FR-REL-03)

P0 decided not to build a monitoring stack (Technical Doc §11). For Release 1:

| Signal | How | Alert |
|---|---|---|
| Uptime of API, the four Staff App domains, Member Portal, website | External probe (e.g. UptimeRobot / Better Stack) every 1 min on `/readyz`, `/surface.json` of `dashboard`, `cashier`, `caddy` and `kitchen`, and the website home | SMS + WhatsApp to on-call |
| API errors, failed jobs, outbox lag, replica lag, backup age, WAL archiving, disk, TLS expiry | `deploy/scripts/healthwatch.sh` every 5 min (cron on the App Host) → `ALERT_WEBHOOK_URL` | Chat channel of the on-call team |
| Repeatedly failing jobs | Built-in (FR-JOB-05): in-app + e-mail to Platform Admin | — |
| Logs | JSON on stdout, `docker logs` with rotation; request id on every error page | — |

## 4. Load test (FR-REL-04)

k6 scripts in `test/load/`, run on Staging with production-like data:

| Scenario | Script | Target | Result |
|---|---|---|---|
| Tee time rush when the booking window opens (300 holds/s on 5 prime slots) | `teetime-rush.js` | no 5xx, p95 hold < 1 s, never over capacity (verify the tee sheet after the run) | |
| Morning check-in peak (20 desks + 5 starter tablets, 3 min) | `checkin-peak.js` | no 5xx, p95 < 1 s, no duplicate check-in | |

Correctness under contention is also proved by the automated test `TestP1ParallelHoldsAndExpiry` (200 parallel holds on
the last seat → exactly one hold).

### Release 3 — Commercial & Business Expansion (PRD P3 FR-REL-P3-01, §12)

Run on Staging with a production-size copy before each P3 wave goes live (R3.1–R3.4, §16 #20); record the k6
summary (p95 / p99, errors, the custom counters) in the *Result* column and attach the HTML report to the go-live
checklist.

| Scenario | Script | Target | Result |
|---|---|---|---|
| Tournament registration when it opens: desk + website registrations on one field | `tournament-registration.js` | no 5xx, p95 < 1.5 s, registered = field size, registered + waitlisted = accepted requests (never over the field) | |
| Campaign to 10.000 recipients (approval, throttled batches) | `campaign-10k.js` | every opted-in recipient has a delivery status; no dispatch minute above the Campaign Policies batch size (BSP limit) | |
| Promotions at the POS peak (online + offline sync, promo codes) | `promotion-pos-peak.js` | apply promotion p95 < 800 ms, evaluation p95 < 300 ms, no 5xx; offline totals that differ are flagged, never lost | |
| Night audit on a month of volume while cashiers keep posting | `night-audit.js` | close < 60 s, Daily Revenue = Σ charges of the day, cashier p95 < 800 ms | |
| POS peak and simultaneous voucher redemption (P2 regression) | `pos-peak.js`, `voucher-redeem.js` | as in Release 2 | |

Correctness under contention is also covered by the Go acceptance tests: tournament field never exceeded
(`TestP3TournamentClub`), one redemption per idempotency key and the daily redemption limit (`TestP3EngagementLoyalty`,
`TestP3FixMoneyLoyalty`), promotion budgets and code limits (`TestP3CommercialPromotions`), venue holds without double
booking (`TestP3BanquetVenueHolds`).

### Release 4 — Enterprise Back Office (PRD P4 FR-REL-P4-01, NFR Performance / Availability)

Run on Staging with a production-size copy (a month of P1–P3 transactions, the Release 4 master data and an open
accounting book) before R4.2 (Inventory + Procurement) and R4.3 (Accounting) go live; record the k6 summary in
*Result* and attach the HTML report to the go-live checklist.

| Scenario | Script | Target | Result |
|---|---|---|---|
| Posting a month of transactions (12,000 charges + payments → automatic journals) while cashiers keep posting | `posting-month.js` | posting lag (last event → posted) < 5 min, no new posting exception, cashier p95 < 800 ms, no 5xx | |
| Period close under operations: postings run, posting & control reconciliations, TB, **annual** P&L / Balance Sheet / Cash Flow / Revenue by Business Line per property and consolidated on the read replica, Report Library PDF export, soft-close of a Staging period | `period-close.js` | each annual financial report < 30 s, soft-close < 60 s, cashier p95 < 800 ms during the close, no 5xx | |
| Procure-to-pay: 10 buyers × PO → approval → GR → 3-way-matched vendor invoice | `procure-to-pay.js` | p95 of each step < 1.5 s, every cycle ends with a matched (approved) vendor invoice; afterwards GRNI and AP control = sub-ledgers (Closing → Reconciliations) | |
| Stock opname of the main store counted from 8 handhelds (offline replays), submit and post | `opname.js` | count p95 < 800 ms, replays counted once, post < 30 s, no 5xx | |
| Stock deduction at the POS peak (K6 consumption) | `pos-peak.js` with Inventory enabled | as in Release 2, and Inventory → Posting Exceptions empty / consumption posted within 5 min after the run | |
| CMS publication under website traffic: 50 public reads/s (site revision, navigation, page, news, sitemap) while Marketing creates and submits pages and the approver publishes them | `cms-publish.js` | visitor p95 < 500 ms, approve → page served by the public API and website revision bumped (cache purge) p95 < 5 s, every page live, no 5xx | |

Correctness under contention is covered by the Go acceptance tests: idempotent posting of an event delivered three
times (`TestP4AccountingGolfPerTransaction`), balanced and append-only journals enforced by the database
(`TestP4AccountingLedgerCore`), period guard for late documents (`TestP4AccountingTransitionAndPeriodGuard`), opname
replays counted once (`TestP4InventoryOpname`), 3-way matching (`TestP4ProcurementMatching`).

## 5. End-to-end tests (FR-REL-05)

- Go acceptance tests on real PostgreSQL: `test/e2e/p1_*_test.go` (golf day, booking changes, rate card, website booking
  with webhook ×3, membership lifecycle, member portal, billing, CRM, Rhapsody migration).
- Browser: `web/e2e/golf.spec.ts` — booking → payment → caddy & golf cart → check-in → tee-off; member card; website.

## 6. Security review (FR-REL-06, FR-REL-07)

External penetration test scope: website `web` (Book Golf, manage-booking link), Member Portal `member` (OTP login,
bookings), payment webhook `/api/v1/webhooks/{integration}`. Release only with **no open High finding**.

Release 3 adds these public and member endpoints to the scope (FR-REL-P3-04):

| Area | Endpoints | Focus |
|---|---|---|
| Public quotation link | `GET /api/v1/public/quotations/{token}`, `POST …/{token}:request-otp`, `POST …/{token}:accept`, `POST …/{token}:reject` | token entropy and expiry, revised / expired versions not acceptable, accept once, IP & time recorded, rate limit; one-time code (§16 #18): cooldown, attempt lock, expiry, only a hash stored, code redacted from integration call logs (still in the notification delivery until the scrubbing decision); e-Meterai above Rp5 jt |
| Public forms | `POST /api/v1/public/inquiries`, `/public/contact`, `/public/complaints`, `GET/POST /public/feedback/{token}`, `GET/POST /public/unsubscribe/{token}`, `GET /public/campaign-links/{token}` | CAPTCHA / rate limit, no enumeration of customers, consent recorded, masked contact data, open-redirect check of tracked links |
| Promotions & packages | `POST /api/v1/public/promo-codes:check`, `GET /public/promotions`, `GET /public/packages`, `POST /public/package-bookings` | code-check rate limit (Promotion Policies), personal codes not usable by others, hold expiry |
| Events & tournaments | `POST /api/v1/public/events/{id}/registrations`, `GET /public/event-tickets/{code}`, `POST /public/tournaments/{id}/registrations`, `GET/POST /public/tournament-registrations/{token}…` | capacity / field under concurrency, ticket and withdrawal tokens, leaderboard shows consented names only |
| Invoice & member payments | `GET /api/v1/public/invoices/{token}`, `POST /public/invoices/{token}:pay`, `POST /api/v1/member/invoices/{id}:pay-online`, `POST /member/folios/{id}:pay-with-points`, `POST /member/loyalty/rewards/{id}:redeem` | IDOR (another customer's invoice / folio / account), amount tampering, idempotency keys, daily points limit, gateway webhook replay |
| Imports (staff) | `POST /api/v1/billing/invoices:import`, `/crm/leads:import`, `/golf/tournaments:import`, `/banquet/events:import` | permission per import, file size, formula injection in exported spreadsheets |

Release 4 adds back-office and finance endpoints to the scope (FR-REL-P4-05):

| Area | Endpoints | Focus |
|---|---|---|
| CMS content & media | `POST /api/v1/cms/media` (upload), page / news / banner editors, `GET /api/v1/public/cms/preview/{token}`, `GET /public/cms/pages/{slug}`, `/public/cms/redirects` | file type / size / image re-encoding, SVG & HTML sanitising (stored XSS in rich text), preview token entropy and expiry, open redirects |
| Public supplier links | `GET /api/v1/public/procurement/rfqs/{token}`, `POST …/{token}:quote`, `GET /api/v1/public/procurement/purchase-orders/{token}/pdf` | token entropy / expiry, one supplier cannot read another's RFQ or PO, quote after the deadline refused, rate limit |
| Bank & Coretax integration | `POST /api/v1/accounting/bank-transactions:import` (CSV / MT940), `POST /accounting/tax-invoices/{id}:upload`, `POST /accounting/tax-invoices:export`, Coretax adapter credentials, `/api/v1/webhooks/{integration}` | file parsing (size, formula injection, malformed MT940), credentials encrypted at rest and never returned, signed calls, replay |
| Financial data access | `/api/v1/accounting/*` (journals, GL, trial balance, reports, AR/AP, payment runs and bank files), `/api/v1/reporting/exports` (CSV / XLSX / PDF) | IDOR across properties (`propertyId` filters, `{id}` of another property), consolidated reports only with every property in scope, permission per report, export files private to the requester, payment-run approval cannot be bypassed |
| Auditor access | role **Auditor** (read-only, *Access until*), `GET /api/v1/audit/logs` | no write route reachable, expiry enforced on the next request, every read in the audit log (`action = view`) |
| Masked data | supplier bank accounts, NPWP, employee data in Procurement / Accounting | masking by permission, exports respect the masking |

Self-check before the external test:

| Item | Where | ✔ |
|---|---|---|
| Webhooks verify signatures (HMAC / callback token) and are idempotent | `internal/platform/integration`, `TestWebhooks`, `TestP1WebsiteBooking` | |
| Public endpoints rate limited (holds, bookings, OTP, activation) | `kernel/ratelimit`, golf portal, iam portal | |
| CAPTCHA enabled on public booking (Turnstile integration) | Settings → Integrations | |
| Manage-booking and hold tokens are random, single purpose | golf portal | |
| No account enumeration on OTP request / activation | iam portal, `/public/membership/activate` | |
| Personal data masked by permission, export/erase audited | CRM, `TestP1CustomerProfiles` | |
| Production build with `garble` (build arg `OBFUSCATE=true`) and **no source maps** (`SOURCEMAPS=false`) | `deploy/docker/Dockerfile`, vite / next configs | |
| Secrets only in `.env` / integration credentials encrypted at rest | provisioning, integrations | |
| RLS on every property table | `TestRLSOnEveryPropertyTable` | |
| Release 4: CMS upload sanitiser and preview tokens | `TestP4CMSPageWorkflow` | |
| Release 4: Auditor role read-only, time-bound, reads logged | `TestP4FixFinanceAuditor`, `TestRolePermissionMatrix` | |

### Retention (Release 4, FR-REL-P4-06, Technical Doc §7.7 / §14)

* Audit log and financial documents are kept **≥ 5 years**: `audit.audit_log` is append-only with no purge job, the
  accounting tables are append-only (journals are corrected by reversal).
* Backups: 30 days PITR (repo 1) and **60 monthly full backups = 5 years** (repo 2, `deploy/pgbackrest/pgbackrest.conf`
  `repo2-retention-full=60`). The object-storage bucket of repo 2 needs a lifecycle / object-lock rule of ≥ 5 years —
  an infrastructure change applied by OneClub ops (not in this repository).
* The monthly restore drill (`deploy/scripts/restore-drill.sh`) also checks the `accounting` schema: journal count,
  latest journal date, no unbalanced journal, tax invoices.

### Two-person review (NFR Security, PRD §5.4 / Technical Doc §13) — proposal

Changes to `internal/accounting/**`, `db/migrations/**` (destructive migrations) and `deploy/**` should need two
approvals. Proposal (not applied: repository governance is decided by the repository owners): add
`.github/CODEOWNERS` with the Finance-platform owners for `internal/accounting/`, `internal/reporting/p4_accounting.go`,
`db/migrations/accounting/` and the platform owners for `db/migrations/` and `deploy/`, and enable *Require review
from Code Owners* with 2 required approvals on `main` / `staging`.

## 7. Hypercare (FR-REL-09)

Two to four weeks after go-live:

| Item | Plan |
|---|---|
| On site | OneClub delivery lead at the club on cutover day and the first weekend |
| Daily check | 07:00 and 15:00: healthwatch status, failed jobs, reconciliation exceptions, open approvals |
| Daily report | Daily Tee Sheet, Daily Payment, Refund, No-show & Cancellation reports compared with operations |
| Escalation | Level 1 club super user → Level 2 OneClub support (chat, response < 30 min 05:00–22:00) → Level 3 engineering on call (critical: booking, check-in, payment down) |
| Severity | Critical = golf operation blocked; High = workaround exists; Normal = cosmetic / report |
| Exit | 10 consecutive days without critical or high incidents, club sign-off |
| Release 4 | Training per role before go-live: accountant & finance manager (journals, reconciliations, period close, e-Faktur), warehouse & store keepers (GR, issues, opname on the handheld), procurement (PR → PO → GR → invoice matching), inventory manager, marketing (CMS). Hypercare lasts until the **first period close** in OneClub is completed and signed (FR-REL-P4-08) |

## 8. Go-live checklist

| # | Item | Evidence | Owner | ✔ |
|---|---|---|---|---|
| 1 | Production instance provisioned and configured | §1 | OneClub ops | |
| 2 | HA option A in place, failover drill recorded | §2 | OneClub ops | |
| 3 | Restore drill (PITR) recorded | §2, `restore-drill.sh` | OneClub ops | |
| 4 | Uptime probe and healthwatch alerts tested | §3 | OneClub ops | |
| 5 | Load tests passed (Release 3: the P3 scenarios of §4) | §4 | Engineering | |
| 6 | E2E (Go + Playwright) green on the release build | §5 | Engineering | |
| 7 | Pen test: no open High (Release 3: the P3 endpoints of §6) | §6 | Security vendor | |
| 8 | Migration reconciled 100% and signed (Exit #5) | `cutover-runbook.md` | Club + OneClub | |
| 8a | Release 3 migrations reconciled and signed: banquet, corporate AR, tournaments, leads | `banquet-migration-p3.md`, `corporate-ar-migration-p3.md`, `tournament-migration-p3.md`, `leads-migration-p3.md` | Club + OneClub | |
| 8b | Release 4 cut-overs: two dry runs each, go/no-go minutes, sign-off — inventory (cut-over opname, purchase freeze), procurement, accounting (opening balances, parallel-run month, Excel TB comparison, sign-off stops the Accounting Export) | `inventory-migration-p4.md`, `procurement-migration-p4.md`, `accounting-migration-p4.md` | Club Finance + OneClub | |
| 8c | Release 4 load tests (§4 Release 4), pen-test P4 scope (§6) without open High, retention 5 years configured and a restore drill including `accounting` recorded | §4, §6, Retention, `backup-restore.md` | OneClub ops + Engineering | |
| 9 | Parallel run without open critical finding (Exit #6) | `cutover-runbook.md` §2 | Club | |
| 10 | Staff trained per interface (Starter, Caddy Master, Front Desk, Golf Staff, Back Office) | training log | Club | |
