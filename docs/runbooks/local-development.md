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

Files go to `STORAGE_DIR` by default. To use MinIO like the object storage in production (the compose stack creates
the bucket `oneclub`), replace `STORAGE_DIR` with:

```
STORAGE_DRIVER=s3
S3_ENDPOINT=localhost:9000
S3_BUCKET=oneclub
S3_ACCESS_KEY=oneclub
S3_SECRET_KEY=oneclub-dev-secret
S3_USE_SSL=false
S3_REGION=us-east-1
```

## 3. Frontend

```bash
cd web
pnpm install
pnpm dev                     # Staff App :5173, Member App :5174, website :3000; /api is proxied to :8080
pnpm --filter @oneclub/api-client generate   # after changing the API (make openapi does both)
```

The Staff App (`web/apps/staff`) holds every staff area (Technical Doc §6.1): Back Office at `/` (module paths such as
`/golf/tee-sheet`), Management Dashboard `/management`, Platform Administration `/platform`, Clubhouse Screen
`/screen`, Operational `/ops`, Caddy Tablet `/tablet` and Kitchen Display `/kitchen`. One login at `/login`; the user
lands on their first area in the order Management, Back Office, Platform Administration, Clubhouse Screen, Caddy
Tablet, Kitchen Display, Operational (or on `?next=` when that area is theirs) and switches areas from the user menu.

In production the one build is served on four domains, and the domain (its *surface*, from `/surface.json`) locks the
areas: `dashboard` (Back Office, Management, Platform Administration, Clubhouse Screen; password login only),
`cashier` (Operational), `caddy` (Caddy Tablet), `kitchen` (Kitchen Display). Locally:

| URL | Surface | Opens |
|---|---|---|
| `http://localhost:5173` | none | every area by path (development) |
| `http://dashboard.localhost:5173` | `dashboard` | office areas; device areas show 403 with a link to their domain |
| `http://cashier.localhost:5173` | `cashier` | Operational only |
| `http://caddy.localhost:5173` | `caddy` | Caddy Tablet only |
| `http://kitchen.localhost:5173` | `kitchen` | Kitchen Display only |

Browsers resolve `*.localhost` to this machine; `vite.config.ts` serves each subdomain its `/surface.json` and manifest
the way Caddy does in production. Each domain keeps its own session, device registration and service worker. On a
device domain a shared device is registered once at `/login/device` with the token printed by `make seed-demo`;
afterwards `/login` asks for e-mail + PIN.

**POS Cashier** (`/ops/pos` on `cashier`): a full-screen app — Table View (floor plan with Available / Booked / Billed /
Occupied), menu with photos and Current Order, Payment Method, Payment Confirm, Order History and Settings (outlet, shift,
table reservations). Occupied tables show the kitchen state (KDS) and can be moved or joined (Move Table). Offline the POS keeps
working on the last data it saw: new orders, items and cash / QRIS / card payments go to the sync queue and show as local orders
(dashed tables, "Offline" lines) until the queue is sent. The floor plan of each outlet is laid out in Back Office → Commercial → Floor Plan; product photos are
uploaded on the product form. `seed-demo --trial` adds the floor plans, the product photos (`internal/app/trialphotos`,
open licenses in CREDITS.md) and a Clubhouse Restaurant in service today; on a completed trial dataset it runs only the
steps of seeders added since.

## 4. Demo accounts (seed-demo)

| E-mail | Role | Staff App areas | Notes |
|---|---|---|---|
| gm@demo.oneclub.id | General Manager (MAIN) | Management, Back Office | no MFA |
| property.admin@demo.oneclub.id | Property Admin (MAIN) | Management, Back Office on `dashboard`; Operational, Caddy Tablet, Kitchen Display on their domains | MFA enrolment at first login |
| property.admin2@demo.oneclub.id | Property Admin (MDR) | as above | sees only MDR |
| finance@demo.oneclub.id | Finance Manager | Management, Back Office | MFA, approves step 2 |
| starter@demo.oneclub.id / cashier@… | Staff | Operational on `cashier` (device + PIN 246810) | |
| caddy@demo.oneclub.id | Caddy (C001 Siti) | Caddy Tablet on `caddy` (device + PIN 246810) | My Assignments shows C001's assigned / in-play flights |
| member@demo.oneclub.id | Member | — (Member App) | |

Password for all: `Demo#Club2026`.

**Demo access** (`/demo` on the Staff App, also linked under the login form): cards per business (Golf, Sport Club,
Bungalow, VIP Suite, Wedding / MICE / Banquet) with their demo users; a click opens that user's login (on the right
domain) with the e-mail filled in and signs out whoever is logged in. It lists the active `@demo.oneclub.id` accounts
from `GET /api/v1/public/demo-access`, which answers 404 when `ONECLUB_ENV=production` or without the demo seed.

## 5. Tests

`make e2e` provisions two throw-away instances per run (`ONECLUB_TEST_ADMIN_URL`), starts the API and River worker
in-process and drives them over HTTP. At the end it fails if any mutating route returned 2xx without an audit
entry or was never exercised.

- One test while iterating: `ONECLUB_REQUIRE_FULL_COVERAGE=false go test -count=1 -run TestX ./test/e2e/`.
- Money is a 4-decimal string; compare amounts numerically, not as text. A settled payment has status `completed`.
- Rounds and caddy fee splits are computed from timestamps; golf tests move them back with SQL instead of waiting.
- Dead code: `go run golang.org/x/tools/cmd/deadcode@latest -test ./...`.
- API paths in the frontend and in `test/e2e` are plain strings, so typecheck does not catch a wrong path; compare
  them with the paths printed by `go run ./cmd/oneclub openapi` after renaming a route.
