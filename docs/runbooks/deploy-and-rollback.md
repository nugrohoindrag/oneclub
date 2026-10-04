# Runbook — deploy and rollback (FR-TEC-07/08/09)

## Pipeline

| Trigger | What happens |
|---|---|
| Pull request | Go lint + arch-lint, unit, migrations on empty DB, e2e on PostgreSQL, OpenAPI drift + breaking-change check, frontend typecheck/lint/test/build, Playwright browser tests |
| Merge to `main` | Images `oneclub`, `oneclub-static`, `oneclub-web` tagged with the commit SHA → Trivy scan → push to GHCR → automatic deploy to **Dev** (once configured) |
| Merge `main` → `staging` | Obfuscated backend (garble) + frontends without source maps, built once (`<sha>-vps`) → **Staging** + Playwright |
| Tag `vX.Y.Z` on `staging` | The Staging-tested images are promoted to `vX.Y.Z` (no rebuild) → **Production** per instance after manual approval |

Obfuscation is applied only to VPS builds (Technical Doc §9.4). Secrets, variables and branch protection:
[ci-cd.md](ci-cd.md).

## Zero-downtime deploy (`deploy/scripts/deploy.sh <instance> <version>`)

1. `docker compose pull`
2. `migrate up` as a one-shot container (expand-only migrations)
3. Copy the static apps (Staff App, Member App) into the Caddy volume
4. Start new `api` replicas next to the old ones → wait for Docker health (`/readyz`) → Caddy (dynamic DNS upstreams,
   1 s refresh, active health checks, `lb_try_duration`) sends traffic to them → stop old replicas with a 30 s drain
5. Restart `worker` (River finishes active jobs, 60 s grace)
6. `smoke-test.sh` — on failure the script redeploys the previous version automatically

## Rollback

`deploy/scripts/rollback.sh <instance> [version]` redeploys the previous image tag. Safe because migrations follow
expand → migrate → contract (old code keeps working against the expanded schema).

## Upgrading an instance to the Staff App (once)

The Back Office, Operational, Caddy Tablet and Platform Administration apps became one Staff App. Before deploying it
to an instance that runs the separate apps:

1. In the instance `.env`, rename `DOMAIN_BACKOFFICE` to `DOMAIN_STAFF` with the same hostname, so `PUBLIC_BASE_URL`
   and the links in e-mails keep working. Remove `DOMAIN_OPS`, `DOMAIN_CADDY` and `DOMAIN_PLATFORM_ADMIN`.
2. Shared devices (POS, ops and caddy tablets) open the Staff App domain and are registered there again at
   `/login/device` (Settings → System Settings → Devices → rotate token when the old token is lost).
3. In GitHub, replace the variables `STAGING_BACKOFFICE_URL`, `STAGING_OPS_URL` and `STAGING_PLATFORM_ADMIN_URL` with
   `STAGING_STAFF_URL`.

## Checks after deploy

- `https://<backoffice>/api/v1/public/bootstrap` returns 200
- Back Office login works; Settings → System Settings → Background Jobs shows no new failures
