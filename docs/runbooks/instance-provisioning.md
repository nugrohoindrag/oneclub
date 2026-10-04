# Runbook — provision a customer instance (FR-INS-01/02)

Each customer gets its **own database, database roles and Docker Compose stack** (Dedicated Customer Instance).

## Steps

1. **Database host**: PostgreSQL 18 with pgBackRest (`deploy/compose/db`). Superuser URL available to the operator.
2. **Create the instance** (from the operator workstation or the DB host):

   ```bash
   oneclub instance create \
     -admin-url "postgres://postgres:***@db-host:5432/postgres?sslmode=require" \
     -code mgcc -name "Modern Golf & Country Club" \
     -legal-name "PT Modern Golf Indonesia" -property-code MAIN -property-name "Modern Golf & Country Club" \
     -locale id -currency IDR -timezone Asia/Jakarta \
     -super-admin-email it@moderngolf.id -platform-admin-email platform@oneclub.id \
     -public-url https://backoffice.moderngolf.id \
     -bundle-dir /srv/oneclub/instances -db-host db
   ```

   This creates database `oneclub_mgcc` and roles `oc_mgcc_owner` (migrations), `oc_mgcc_app` (API/worker, RLS
   applies, no UPDATE/DELETE on audit), `oc_mgcc_report` (read-only, replica). `CONNECT` is revoked from `PUBLIC`,
   so one instance's credentials cannot open another instance's database. It runs all migrations, synchronises
   permissions and role templates, seeds payment methods and sandbox integrations, creates the first Super Admin and
   writes the deploy bundle (`.env` + `secrets/*`, mode 0600).

3. **Secure the bundle**: encrypt with `sops`/`age` or move to the secret store. Never commit it.
4. **Complete `.env`** with `DOMAIN_BACKOFFICE`, `DOMAIN_MEMBER`, `DOMAIN_OPS`, `DOMAIN_PLATFORM_ADMIN`, `DOMAIN_CADDY` (caddy tablet), `DOMAIN_WEB`,
   `ACME_EMAIL`, SMTP and S3 settings; create `.env.pgbouncer`.
5. **Deploy**: `deploy/scripts/deploy.sh mgcc <version>`.
6. **First login**: the Super Admin logs in with the printed temporary password, changes it and enrols MFA.
7. **Verify isolation**: `make e2e` covers it automatically (`TestInstanceIsolation`); on staging also try the app
   URL of instance A against database B — it must be refused.

## Suspend / reactivate

Platform Administration → Instance Configuration → Suspend Instance (only Platform Admin keeps access).

## Remove (non-production only)

`oneclub instance drop -admin-url … -code <code> -confirm <code>` (refused when `ONECLUB_ENV=production`).
