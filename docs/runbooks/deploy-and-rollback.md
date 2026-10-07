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

The Back Office, Operational, Caddy Tablet and Platform Administration apps became one Staff App, served on four
domains (Technical Doc §6.1). Before deploying it to an instance that runs the separate apps:

1. In the instance `.env`:
   - rename `DOMAIN_BACKOFFICE` to `DOMAIN_DASHBOARD` with the same hostname, so `PUBLIC_BASE_URL` and the links in
     e-mails keep working;
   - rename `DOMAIN_OPS` to `DOMAIN_CASHIER` (the hostname may stay);
   - keep `DOMAIN_CADDY`; add `DOMAIN_KITCHEN` (new DNS record, see [instance-provisioning.md](instance-provisioning.md));
   - add `DOMAIN_PRESENCE` (new DNS record for the Attendance Form);
   - remove `DOMAIN_PLATFORM_ADMIN`: Platform Administration opens on the dashboard domain.
2. Shared devices open the domain of their area: POS and counter devices `cashier`, caddy tablets `caddy`, kitchen
   screens `kitchen`. Each domain keeps its own device registration, so a device that shows the password form instead
   of the PIN is registered again at `/login/device` on its domain (Settings → System Settings → Devices → rotate
   token when the old token is lost). The clubhouse TV signs in on `dashboard` with a user holding the role **Screen**.
3. Custom domains (Platform Administration → Custom Domain) registered for `backoffice`, `platform-admin` or `ops`
   keep working: they are served as `dashboard`, `dashboard` and `cashier`.
4. In GitHub, replace the Staging variables with `STAGING_DASHBOARD_URL`, `STAGING_CASHIER_URL`, `STAGING_CADDY_URL`
   and `STAGING_KITCHEN_URL` ([ci-cd.md](ci-cd.md)).

## Checks after deploy

- `https://<dashboard>/api/v1/public/bootstrap` returns 200; `https://<each staff domain>/surface.json` names its
  surface (`smoke-test.sh` checks both)
- Back Office login works; Settings → System Settings → Background Jobs shows no new failures
