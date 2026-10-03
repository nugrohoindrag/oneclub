#!/usr/bin/env bash
# Restore drill (FR-TEC-11, PRD §12.2): restore the latest backup (or a
# point in time) into a scratch PostgreSQL on Staging, verify it, and record
# the duration against the RTO target (≤ 4 hours).
#
#   restore-drill.sh <instance-code> [--target-time "2026-10-04 10:00:00+07"]
set -euo pipefail
INSTANCE="${1:?usage: restore-drill.sh <instance-code> [--target-time TS]}"
shift || true
TARGET_ARGS=()
if [[ "${1:-}" == "--target-time" ]]; then
	TARGET_ARGS=(--type=time "--target=$2" --target-action=promote)
fi
ROOT="${ONECLUB_ROOT:-/srv/oneclub}"
LOG="$ROOT/restore-drills.log"
NAME="oneclub-restore-drill-$$"
IMAGE="${PG_IMAGE:-registry.oneclub.id/postgres-pgbackrest:18}"
START=$(date +%s)

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; docker volume rm "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "[$(date -u +%FT%TZ)] restoring latest backup of stanza oneclub into $NAME"
docker volume create "$NAME" >/dev/null
docker run --rm -u postgres -v "$NAME:/var/lib/postgresql" \
	-v "$ROOT/pgbackrest/pgbackrest.conf:/etc/pgbackrest/pgbackrest.conf:ro" \
	--env-file "$ROOT/compose/db/secrets/pgbackrest.env" \
	"$IMAGE" pgbackrest --stanza=oneclub --delta "${TARGET_ARGS[@]}" restore

docker run -d --name "$NAME" -v "$NAME:/var/lib/postgresql" -e POSTGRES_PASSWORD=drill "$IMAGE" >/dev/null
for _ in $(seq 1 120); do
	docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1 && break
	sleep 2
done

DB="oneclub_${INSTANCE//-/_}"
echo "verifying restored database $DB"
docker exec "$NAME" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAc "
	SELECT 'instance', code FROM platform.instance;
	SELECT 'users', count(*) FROM platform.users;
	SELECT 'properties', count(*) FROM platform.properties;
	SELECT 'audit_entries', count(*) FROM audit.audit_log;
	SELECT 'latest_audit', max(occurred_at) FROM audit.audit_log;
	SELECT 'migrations_platform', max(version_id) FROM public.goose_platform;"

END=$(date +%s)
DURATION=$((END - START))
RESULT="ok"
(( DURATION > 4 * 3600 )) && RESULT="exceeds RTO"
echo "$(date -u +%FT%TZ) instance=$INSTANCE duration=${DURATION}s target=${TARGET_ARGS[*]:-latest} result=$RESULT" | tee -a "$LOG"
