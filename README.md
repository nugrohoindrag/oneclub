# OneClub

Golf & country club platform — **P0 Platform Foundation** and **P1 Golf Core MVP (Release 1)** for Modern Golf & Country Club.

| Part | Path | Stack |
|---|---|---|
| Backend (modular monolith, one binary) | `cmd/`, `internal/`, `db/migrations/` | Go 1.27, chi, pgx, goose, River, PostgreSQL 18 |
| API contract | `api/openapi/openapi.json` | OpenAPI 3.0, generated from the route registry per build |
| Frontend monorepo | `web/` | pnpm, React 19, Vite, Next.js, TanStack Query, Morphic Design System |
| Deployment | `deploy/` | Docker, Docker Compose (no Kubernetes), Caddy, pgBackRest |
| CI/CD | `.github/workflows/` | lint, arch-lint, unit, integration, OpenAPI drift/breaking check, build, scan, deploy |

Product documents (PRD, Technical Documentation, Naming Convention, roadmap) live in `docs/product/` locally and are
not committed. The original Morphic design system source is kept in `design-system/` (restored as `web/packages/ui`).
P0 requirement → code → test mapping: [`docs/p0-traceability.md`](docs/p0-traceability.md); P1:
[`docs/p1-traceability.md`](docs/p1-traceability.md). Rhapsody migration: [`docs/migration/`](docs/migration/);
go-live: [`docs/runbooks/production-readiness.md`](docs/runbooks/production-readiness.md).

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
| Staff App (PWA): Back Office `/`, Management Dashboard `/management`, Operational `/ops` (offline, device + PIN), Caddy Tablet `/tablet` (offline), Platform Administration `/platform` | `web/apps/staff` | 5173 | every staff member; the role decides the areas |
| Member & Guest Portal (PWA) | `web/apps/member` | 5174 | members, guests |
| Website (Next.js SSR) | `web/apps/web` | 3000 | public |
| Morphic showcase | `web/packages/ui` (`pnpm showcase`) | 5199 | designers, engineers |

## Tests

```bash
make unit                                  # Go unit + architecture boundary tests
make e2e                                   # P0 + P1 acceptance tests on a real PostgreSQL (fresh instances per run)
cd web && pnpm -r typecheck && pnpm -r test
cd web && pnpm exec playwright test        # browser tests of every app and Staff App area, golf flow, offline POS and tablet (needs running API + previews)
k6 run test/load/teetime-rush.js           # load tests (see docs/runbooks/production-readiness.md §4)
```

`oneclub seed-demo` (dev/staging) seeds the Modern Golf course (MGC, 18 holes, 6,350 m), tee sheet templates, the
2026 rate card, membership types, caddies, golf carts, lockers, a demo member (`member@demo.oneclub.id`, Member No.
D0001) and one staff user per P1 role (`golf.admin@`, `caddy.master@`, `front.desk@`, `reservation@`, `golf.staff@`,
`membership@`, `membership.admin@demo.oneclub.id`).

## Binary commands

```
oneclub api | worker | migrate up|status|down <module> | instance create|drop | seed-demo
oneclub import rhapsody stage|validate|load|reconcile -property CODE [-dir DIR] [-out DIR]
oneclub openapi [-o file] | healthcheck | version
```
