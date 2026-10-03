#!/usr/bin/env bash
# Starts a complete OneClub stack on the CI runner for the Playwright browser
# tests: provisions instance "mgcc" on the PostgreSQL service, seeds demo
# data, runs api + worker, and serves the built shells (vite preview) and the
# website (next start). Exports DEMO_DEVICE_TOKEN via $GITHUB_ENV.
#
# Expects: ./bin/oneclub built, web/ installed and built, ADMIN_URL set.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
LOGS="$ROOT/var/ci-logs"
mkdir -p "$LOGS"

./bin/oneclub instance create -admin-url "$ADMIN_URL" -code mgcc -name "Modern Golf & Country Club" \
	-super-admin-email admin@moderngolf.id -platform-admin-email platform@oneclub.id \
	-public-url http://localhost:5173 | tee "$LOGS/instance.txt"

{
	echo "ONECLUB_ENV=dev"
	echo "ONECLUB_INSTANCE=mgcc"
	echo "PUBLIC_BASE_URL=http://localhost:5173"
	echo "STORAGE_DIR=$ROOT/var/storage"
	grep -E '^ +(DATABASE_URL|DATABASE_OWNER_URL|DATABASE_REPLICA_URL|APP_SECRET)=' "$LOGS/instance.txt" | sed 's/^ *//'
} > .env.local

set -a
# shellcheck disable=SC1091
source .env.local
set +a

./bin/oneclub seed-demo | tee "$LOGS/seed.txt"
token="$(sed -n 's/.*Demo POS device token (shown once): *//p' "$LOGS/seed.txt")"
echo "::add-mask::$token"
echo "DEMO_DEVICE_TOKEN=$token" >> "${GITHUB_ENV:-/dev/null}"

nohup ./bin/oneclub api > "$LOGS/api.log" 2>&1 &
nohup ./bin/oneclub worker > "$LOGS/worker.log" 2>&1 &

cd web
for app in backoffice member ops platform-admin; do
	nohup pnpm --filter "@oneclub/$app" preview > "$LOGS/$app.log" 2>&1 &
done
nohup pnpm --filter @oneclub/web start > "$LOGS/web.log" 2>&1 &

wait_for() {
	for _ in $(seq 1 60); do
		curl -fsS -o /dev/null "$1" && return 0
		sleep 1
	done
	echo "timed out waiting for $1" >&2
	return 1
}
wait_for http://localhost:8080/readyz
for port in 5173 5174 5175 5176 3000; do wait_for "http://localhost:$port/"; done
echo "stack ready"
