# Production readiness — Release 1 (Golf Core MVP)

PRD P1 EP-19 (FR-REL-01..09) and Exit Criterion #8. Each item has an owner and evidence; the go-live checklist at the end
must be complete before the cutover (`docs/migration/cutover-runbook.md`).

## 1. Production instance (FR-REL-01)

Provision with `docs/runbooks/instance-provisioning.md` on the chosen hosting (P0 Open Question #1), instance code `mgcc`:

```bash
oneclub instance create -admin-url "postgres://postgres:***@db-host:5432/postgres?sslmode=require"   -code mgcc -name "Modern Golf & Country Club" -property-code MAIN -property-name "Modern Golf & Country Club"   -locale id -currency IDR -timezone Asia/Jakarta -super-admin-email it@moderngolf.id -platform-admin-email platform@oneclub.id   -public-url https://backoffice.moderngolf.id -bundle-dir /srv/oneclub/instances -db-host db
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
| Uptime of API, Back Office, Member Portal, website | External probe (e.g. UptimeRobot / Better Stack) every 1 min on `/readyz` and the website home | SMS + WhatsApp to on-call |
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

## 5. End-to-end tests (FR-REL-05)

- Go acceptance tests on real PostgreSQL: `test/e2e/p1_*_test.go` (golf day, booking changes, rate card, website booking
  with webhook ×3, membership lifecycle, member portal, billing, CRM, Rhapsody migration).
- Browser: `web/e2e/golf.spec.ts` — booking → payment → caddy & golf cart → check-in → tee-off; member card; website.

## 6. Security review (FR-REL-06, FR-REL-07)

External penetration test scope: website `web` (Book Golf, manage-booking link), Member Portal `member` (OTP login,
bookings), payment webhook `/api/v1/webhooks/{integration}`. Release only with **no open High finding**.

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

## 8. Go-live checklist

| # | Item | Evidence | Owner | ✔ |
|---|---|---|---|---|
| 1 | Production instance provisioned and configured | §1 | OneClub ops | |
| 2 | HA option A in place, failover drill recorded | §2 | OneClub ops | |
| 3 | Restore drill (PITR) recorded | §2, `restore-drill.sh` | OneClub ops | |
| 4 | Uptime probe and healthwatch alerts tested | §3 | OneClub ops | |
| 5 | Load tests passed | §4 | Engineering | |
| 6 | E2E (Go + Playwright) green on the release build | §5 | Engineering | |
| 7 | Pen test: no open High | §6 | Security vendor | |
| 8 | Migration reconciled 100% and signed (Exit #5) | `cutover-runbook.md` | Club + OneClub | |
| 9 | Parallel run without open critical finding (Exit #6) | `cutover-runbook.md` §2 | Club | |
| 10 | Staff trained per interface (Starter, Caddy Master, Front Desk, Golf Staff, Back Office) | training log | Club | |
