# Runbook — CI/CD (GitHub Actions)

Repository: https://github.com/textedoh/oneclub · workflows in `.github/workflows/`.

## Pipelines

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci` | every pull request, push to `main`, manual | `lint` (golangci-lint + depguard architecture rules) · `backend` (unit + race, migrations, 41 API acceptance tests on PostgreSQL 18, OpenAPI drift + breaking-change check on PRs) · `frontend` (API-client drift, typecheck, lint, unit tests, build) · `browser` (full stack on the runner + Playwright for every app and Staff App area, offline POS and caddy tablet) |
| `ci` (main only) | push to `main` after all checks pass | `images` → build, Trivy scan, push `oneclub`, `oneclub-static`, `oneclub-web` tagged with the commit SHA · `deploy-dev` (only when Dev is configured) |
| `vps` | push to branch `production` | VPS images built once — backend obfuscated (garble), Staff App, Member App and website obfuscated (`web/scripts/obfuscate.mjs`) without source maps — tagged `<sha>-vps`, Trivy scan, check that no source maps ship → deploy to the VPS + browser tests (once configured) |
| `release` | tag `vX.Y.Z` on a `production` commit | promotes that commit's `<sha>-vps` images to `vX.Y.Z` (no rebuild — client instances run exactly what the VPS tested) → GitHub release → `production` per instance after manual approval |

Images go to GitHub Container Registry (`ghcr.io/textedoh/…`) with the built-in `GITHUB_TOKEN`; set
`vars.REGISTRY` + `secrets.REGISTRY_USER/REGISTRY_PASSWORD` to use another registry.

Deploy jobs are skipped until their server is configured, so CI is green before hosting exists (Open Question #1).

## Branches and promotion

```text
staging ──PR──▶ main ──PR──▶ production ──tag vX.Y.Z──▶ client instances
(work)          (CI green)   (VPS, obfuscated)          (manual approval)
```

| Branch | Environment | How it changes |
|---|---|---|
| `staging` | — (work branch, one developer) | every commit goes here; push often |
| `main` | Dev (automatic, readable build) | PR `staging → main` with green CI, **merge commit** (never squash) |
| `production` | VPS (automatic, obfuscated build) | PR `main → production` (merge commit) when a set of features goes to the VPS |
| tag `vX.Y.Z` | client instances | `git tag vX.Y.Z origin/production && git push origin vX.Y.Z`; approve the `production` environment |

All three branches hold the same readable source: obfuscation happens only while the `vps` workflow builds the
images, and the VPS receives images only (never the repository). Nothing obfuscated is ever committed, so the
branches always merge cleanly. Nothing is committed to `main` or `production` directly; a fix found on the VPS is
made on `staging` → `main` → `production`.

## Working on staging

Development continues with one developer (Technical Documentation §12.3), so phases follow one another:
P1 + P2, then P3, P4, P5, P6.

1. Commit on `staging`. Before pushing: `make lint unit` (and `make e2e` when touching API/DB code).
2. When a set of changes is ready, open a pull request `staging → main` and merge it with a **merge commit** once
   `lint`, `backend`, `frontend` and `browser` are green. A squash merge would give `main` commits that
   `staging` does not have, and the next pull request would conflict.
3. After the merge, bring `staging` level with `main` again before the next commit:
   `git switch staging && git pull --ff-only origin main && git push origin staging`.
4. Generated files: `api/openapi/openapi.json` and `web/packages/api-client/src/schema.ts` are never edited by hand.
   Run `make openapi` and commit the result; CI fails with "stale" otherwise.
5. Migrations: each module has its own goose sequence (`db/migrations/<module>/`) and stays expand-only
   (no drop/rename in the same release).
6. Breaking API changes: on pull requests oasdiff compares the OpenAPI document with the base branch. A new value
   in a response enum is only a warning (`.github/oasdiff/severity-levels.txt`). An intentional contract change is
   added, with a comment saying why, to `.github/oasdiff/err-ignore.txt`.

## Branch protection (recommended)

Settings → Branches → Add rule:

- `main`: require a pull request (no required approvals; there is no second developer), require status checks
  `lint`, `backend`, `frontend`, `browser`, block force pushes and deletion.
- `production`: require a pull request (from `main` only, by convention), require the same status checks,
  block force pushes and deletion.
- `staging`: block force pushes and deletion.

## Configuration when servers exist

Settings → Environments → create `dev`, `vps`, `production` (production: required reviewers = manual approval).

| Kind | Name | Used by |
|---|---|---|
| variable | `DEV_INSTANCE` | instance code deployed on Dev; enables `deploy-dev` |
| secret | `DEV_HOST`, `DEV_SSH_KEY` | SSH to the Dev server (user `deploy`) |
| variable | `VPS_INSTANCE` | instance code on the VPS (e.g. `mgcc`); enables the `vps` deploy job |
| variable | `VPS_DASHBOARD_URL`, `VPS_CASHIER_URL`, `VPS_CADDY_URL`, `VPS_KITCHEN_URL`, `VPS_MEMBER_URL`, `VPS_WEB_URL` | browser tests against the VPS (the four Staff App domains, Member App, website) |
| secret | `VPS_HOST`, `VPS_SSH_KEY`, `VPS_DEMO_PASSWORD`, `VPS_DEVICE_TOKEN` | VPS deploy + browser tests |
| variable | `PRODUCTION_INSTANCES` | JSON list, e.g. `["mgcc"]`; enables `production` |
| secret | `PRODUCTION_HOST`, `PRODUCTION_SSH_KEY` | Production deploy |
| secret | `GARBLE_SEED` | fixed garble seed (reproducible backend builds, needed for `garble reverse` of stack traces); random when unset |
| secret | `OBFUSCATE_SEED` | seed of the JavaScript obfuscation (any number); `0` when unset |

Servers need `/srv/oneclub/scripts/` (`deploy.sh`, `rollback.sh`, `smoke-test.sh`, `e2e-setup.sh`) and
`/srv/oneclub/compose/` from `deploy/`. The demo VPS must be seeded with demo data (`oneclub seed-demo`);
`e2e-setup.sh` refuses to run when `ONECLUB_ENV=production`.

## Running the browser tests locally

API and worker on :8080, the apps via `pnpm --filter @oneclub/<app> preview` (`staff` :5173, `member` :5174) or `pnpm dev`, then in `web/`:
`PSQL=<path to psql> DEMO_DEVICE_TOKEN=<token from seed-demo> pnpm e2e`
(`E2E_DATABASE_URL` defaults to the local `oneclub_mgcc` database; `E2E_STAFF`, `E2E_MEMBER` and `E2E_WEB` override the
URLs, e.g. for a second stack next to `pnpm dev`). The Staff App domains are tested on `<surface>.localhost` at the
port of `E2E_STAFF`; the tests of development without a surface run only locally.
