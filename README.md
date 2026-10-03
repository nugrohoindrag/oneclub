# OneClub

Golf & country club platform — **P0 Platform Foundation (Release 0)**.

| Part | Path | Stack |
|---|---|---|
| Backend (modular monolith, one binary) | `cmd/`, `internal/`, `db/migrations/` | Go 1.27, chi, pgx, goose, River, PostgreSQL 18 |
| API contract | `api/openapi/openapi.json` | OpenAPI 3.0, generated from the route registry per build |
| Frontend monorepo | `web/` | pnpm, React 19, Vite, Next.js, TanStack Query, Morphic Design System |
| Deployment | `deploy/` | Docker, Docker Compose (no Kubernetes), Caddy, pgBackRest |
| CI/CD | `.github/workflows/` | lint, arch-lint, unit, integration, OpenAPI drift/breaking check, build, scan, deploy |

Product documents (PRD, Technical Documentation, Naming Convention, roadmap) live in `docs/product/` locally and are
not committed. The original Morphic design system source is kept in `design-system/` (restored as `web/packages/ui`).
P0 requirement → code → test mapping: [`docs/p0-traceability.md`](docs/p0-traceability.md).

## Quick start (local)

Prerequisites: Go 1.27+, Node 24 + pnpm 10, PostgreSQL 18 (or `docker compose -f deploy/compose/dev/compose.yaml up -d`).

```bash
make build
make provision-dev            # creates database oneclub_mgcc, roles, migrations, seeds, Super Admin
# copy the printed DATABASE_URL / DATABASE_OWNER_URL / DATABASE_REPLICA_URL / APP_SECRET into .env.local
make seed-demo                # demo properties, venues, users per role (password Demo#Club2026, PIN 246810)
make api & make worker        # API on :8080, River worker
cd web && pnpm install && pnpm dev   # backoffice :5173, member :5174, ops :5175, platform-admin :5176, web :3000
```

See [`docs/runbooks/local-development.md`](docs/runbooks/local-development.md).

## Applications (application surfaces)

| Shell | App | Port (dev) | Who |
|---|---|---|---|
| Back Office + Management Dashboard (`/management`) | `web/apps/backoffice` | 5173 | admins, back office, management |
| Member & Guest Portal (PWA) | `web/apps/member` | 5174 | members, guests |
| Operational Staff (offline PWA, device + PIN) | `web/apps/ops` | 5175 | starters, cashiers, front desk … |
| Platform Administration | `web/apps/platform-admin` | 5176 | OneClub Platform Admin |
| Website (Next.js SSR) | `web/apps/web` | 3000 | public |
| Morphic showcase | `web/packages/ui` (`pnpm showcase`) | 5199 | designers, engineers |

## Tests

```bash
make unit                                  # Go unit + architecture boundary tests
make e2e                                   # 41 acceptance tests on a real PostgreSQL (fresh instances per run)
cd web && pnpm -r typecheck && pnpm -r test
cd web && pnpm exec playwright test        # browser tests of every shell (needs running API + previews)
```

## Binary commands

```
oneclub api | worker | migrate up|status|down <module> | instance create|drop | seed-demo | openapi [-o file] | healthcheck | version
```
