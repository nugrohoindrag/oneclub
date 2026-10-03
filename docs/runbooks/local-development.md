# Local development

## 1. Database

Either Docker (`docker compose -f deploy/compose/dev/compose.yaml up -d` — PostgreSQL 18, Mailpit on :8025, MinIO on :9001)
or a native PostgreSQL 18 with superuser `postgres/postgres` on `localhost:5432`.

Windows without admin rights: download the EDB "binaries" zip, then

```bash
initdb -D C:/Users/<you>/pgdata18 -U postgres --pwfile=pw.txt -A scram-sha-256 -E UTF8 --locale=C
pg_ctl -D C:/Users/<you>/pgdata18 -l C:/Users/<you>/pgdata18/server.log start
```

## 2. Provision a development instance

```bash
make build
./bin/oneclub instance create -admin-url "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
  -code mgcc -name "Modern Golf & Country Club" -super-admin-email admin@moderngolf.id \
  -platform-admin-email platform@oneclub.id -public-url http://localhost:5173
```

Create `.env.local` (git-ignored) from the printed values:

```
ONECLUB_ENV=dev
ONECLUB_INSTANCE=mgcc
DATABASE_URL=postgres://oc_mgcc_app:…@localhost:5432/oneclub_mgcc?sslmode=disable
DATABASE_OWNER_URL=postgres://oc_mgcc_owner:…@localhost:5432/oneclub_mgcc?sslmode=disable
DATABASE_REPLICA_URL=postgres://oc_mgcc_report:…@localhost:5432/oneclub_mgcc?sslmode=disable
APP_SECRET=…
PUBLIC_BASE_URL=http://localhost:5173
STORAGE_DIR=./var/storage
```

Then `make seed-demo` (prints the demo users and a POS device token once) and run `make api` and `make worker`.
Without `SMTP_HOST`, e-mails are written to the integration log (Settings → System Settings → Integration Logs);
with Mailpit set `SMTP_HOST=localhost SMTP_PORT=1025`.

## 3. Frontend

```bash
cd web
pnpm install
pnpm dev                     # all apps; /api is proxied to :8080
pnpm --filter @oneclub/api-client generate   # after changing the API (make openapi does both)
```

## 4. Demo accounts (seed-demo)

| E-mail | Role | Shell | Notes |
|---|---|---|---|
| gm@demo.oneclub.id | General Manager (MAIN) | Back Office, Management | no MFA |
| property.admin@demo.oneclub.id | Property Admin (MAIN) | Back Office | MFA enrolment at first login |
| property.admin2@demo.oneclub.id | Property Admin (MDR) | Back Office | sees only MDR |
| finance@demo.oneclub.id | Finance Manager | Back Office | MFA, approves step 2 |
| starter@demo.oneclub.id / cashier@… | Staff | Ops (device + PIN 246810) | |
| member@demo.oneclub.id | Member | Member Portal | |

Password for all: `Demo#Club2026`.

## 5. Tests

`make e2e` provisions two throw-away instances per run (`ONECLUB_TEST_ADMIN_URL`), starts the API and River worker
in-process and drives them over HTTP. At the end it fails if any mutating route returned 2xx without an audit
entry or was never exercised.
