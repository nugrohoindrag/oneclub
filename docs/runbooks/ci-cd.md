# Runbook — CI/CD (GitHub Actions)

Repository: https://github.com/textedoh/oneclub · workflows in `.github/workflows/`.

## Pipelines

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci` | every pull request, push to `main`, manual | `lint` (golangci-lint + depguard architecture rules) · `backend` (unit + race, migrations, 41 API acceptance tests on PostgreSQL 18, OpenAPI drift + breaking-change check on PRs) · `frontend` (API-client drift, typecheck, lint, unit tests, build) · `browser` (full stack on the runner + Playwright for every shell) |
| `ci` (main only) | push to `main` after all checks pass | `images` → build, Trivy scan, push `oneclub`, `oneclub-static`, `oneclub-web` tagged with the commit SHA · `deploy-dev` (only when Dev is configured) |
| `staging` | push to branch `staging` | VPS images built once — backend obfuscated (garble), frontends without source maps — tagged `<sha>-vps`, Trivy scan, smoke start → deploy to Staging + browser tests (once configured) |
| `release` | tag `vX.Y.Z` on a `staging` commit | promotes that commit's `<sha>-vps` images to `vX.Y.Z` (no rebuild — Production runs exactly what Staging tested) → GitHub release → `production` per instance after manual approval |

Images go to GitHub Container Registry (`ghcr.io/textedoh/…`) with the built-in `GITHUB_TOKEN`; set
`vars.REGISTRY` + `secrets.REGISTRY_USER/REGISTRY_PASSWORD` to use another registry.

Deploy jobs are skipped until their server is configured, so CI is green before hosting exists (Open Question #1).

## Branches and promotion

```text
feat/pN-<topic> ──PR──▶ main ──PR──▶ staging ──tag vX.Y.Z──▶ Production
                        (Dev)        (Staging)               (manual approval)
```

| Branch | Environment | Who merges | How |
|---|---|---|---|
| `feat/pN-*` | — (CI on the PR) | developer | short-lived, one feature |
| `main` | Dev (automatic) | both developers | PR with green CI + 1 approval, squash merge |
| `staging` | Staging (automatic) | release owner | PR `main → staging` (merge commit, never squash) when a set of features is ready for UAT |
| tag `vX.Y.Z` | Production | release owner | `git tag vX.Y.Z origin/staging && git push origin vX.Y.Z` after UAT sign-off; approve the `production` environment |

Rules: nothing is committed to `staging` directly — it only receives `main`. A fix found on Staging is made on a
`feat/*` branch → `main` → promoted to `staging` again, so `main` always contains everything that is on Staging.

## Working in parallel (P1–P6)

Phases are built in pairs by two developers at the same time: P1 ‖ P2, then P3 ‖ P4, then P5 ‖ P6
(Technical Documentation §12.3). Each pair branches from a `main` that already contains the previous pair.

1. Branch from `main` per feature, prefixed with the phase: `feat/p1-<topic>`, `feat/p2-<topic>`, … `feat/p6-<topic>`.
   Keep branches short-lived (days, not weeks) and rebase on `main` often — small, early merges keep two phases
   from drifting apart.
2. Open a pull request; merge only when `lint`, `backend`, `frontend` and `browser` are green
   (enforced by branch protection — see below). Squash merge keeps `main` linear.
3. Typical conflict points and how to resolve them:
   - `api/openapi/openapi.json` and `web/packages/api-client/src/schema.ts` are generated — never merge them by hand.
     After rebasing run `make openapi` and commit the result; CI fails with "stale" otherwise.
   - Migrations: each module has its own goose sequence (`db/migrations/<module>/`). Two branches adding the same
     number to the same module conflict on file name — renumber yours to the next free number after rebasing.
     Migrations must stay expand-only (no drop/rename in the same release).
   - `internal/app/app.go` (module wiring) and `internal/platform/catalog` (permissions, roles): additive edits;
     keep entries in the existing order and resolve by keeping both sides.
4. Before pushing: `make lint unit` (and `make e2e` when touching API/DB code).

## Branch protection (recommended)

Settings → Branches → Add rule:

- `main`: require a pull request (1 approval), require status checks `lint`, `backend`, `frontend`, `browser`,
  require branches to be up to date, block force pushes and deletion.
- `staging`: require a pull request (from `main` only, by convention), require the same status checks,
  block force pushes and deletion.

## Configuration when servers exist

Settings → Environments → create `dev`, `staging`, `production` (production: required reviewers = manual approval).

| Kind | Name | Used by |
|---|---|---|
| variable | `DEV_INSTANCE` | instance code deployed on Dev; enables `deploy-dev` |
| secret | `DEV_HOST`, `DEV_SSH_KEY` | SSH to the Dev server (user `deploy`) |
| variable | `STAGING_INSTANCE` | enables the `staging` job |
| variable | `STAGING_BACKOFFICE_URL`, `STAGING_MEMBER_URL`, `STAGING_OPS_URL`, `STAGING_PLATFORM_ADMIN_URL`, `STAGING_WEB_URL` | browser tests against Staging |
| secret | `STAGING_HOST`, `STAGING_SSH_KEY`, `STAGING_DEMO_PASSWORD`, `STAGING_DEVICE_TOKEN` | Staging deploy + browser tests |
| variable | `PRODUCTION_INSTANCES` | JSON list, e.g. `["mgcc"]`; enables `production` |
| secret | `PRODUCTION_HOST`, `PRODUCTION_SSH_KEY` | Production deploy |
| secret | `GARBLE_SEED` | fixed obfuscation seed (reproducible release builds); random when unset |

Servers need `/srv/oneclub/scripts/` (`deploy.sh`, `rollback.sh`, `smoke-test.sh`, `e2e-setup.sh`) and
`/srv/oneclub/compose/` from `deploy/`. Staging must be seeded with demo data (`oneclub seed-demo`);
`e2e-setup.sh` refuses to run when `ONECLUB_ENV=production`.

## Running the browser tests locally

API and worker on :8080, shells via `pnpm --filter @oneclub/<app> preview` (or `pnpm dev`), then in `web/`:
`PSQL=<path to psql> DEMO_DEVICE_TOKEN=<token from seed-demo> pnpm e2e`
(`E2E_DATABASE_URL` defaults to the local `oneclub_mgcc` database).
