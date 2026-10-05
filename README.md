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
make e2e                                   # P0–P4 acceptance tests on a real PostgreSQL (fresh instances per run)
cd web && pnpm -r typecheck && pnpm -r test
cd web && pnpm exec playwright test        # browser tests of every app, Staff App area and domain, golf flow, offline POS and tablet, P3/P4 specs (needs running API + previews)
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

## Binary commands

```
oneclub api | worker | migrate up|status|down <module> | instance create|drop | seed-demo
oneclub import rhapsody stage|validate|load|reconcile -property CODE [-dir DIR] [-out DIR]
oneclub openapi [-o file] | healthcheck | version
```
