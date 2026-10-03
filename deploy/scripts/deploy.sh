#!/usr/bin/env bash
# Zero-downtime deploy of one customer instance (Technical Doc §10.3, FR-TEC-08).
#
#   deploy.sh <instance> <version>
#
# 1. pull images            4. rolling update of api (new replicas up and healthy
# 2. migrate (expand-only)     before old ones are drained and removed)
# 3. refresh static shells  5. restart worker (River finishes active jobs)
#                           6. smoke test → done, or automatic rollback
#
# Rollback = run this script again with the previous version (safe because
# migrations follow expand → migrate → contract).
set -euo pipefail

INSTANCE="${1:?usage: deploy.sh <instance> <version>}"
VERSION="${2:?usage: deploy.sh <instance> <version>}"
ROOT="${ONECLUB_ROOT:-/srv/oneclub}"
export INSTANCE VERSION
export INSTANCE_DIR="$ROOT/instances/$INSTANCE"
COMPOSE_FILE="$ROOT/compose/app/compose.yaml"
STATE="$INSTANCE_DIR/.deploy"
API_REPLICAS="${API_REPLICAS:-2}"
export API_REPLICAS

mkdir -p "$STATE"
dc() { docker compose -p "oneclub-$INSTANCE" --env-file "$INSTANCE_DIR/.env" -f "$COMPOSE_FILE" "$@"; }
log() { printf '%s [deploy %s] %s\n' "$(date -u +%FT%TZ)" "$INSTANCE" "$*"; }

PREVIOUS="$(cat "$STATE/current_version" 2>/dev/null || true)"
log "deploying $VERSION (previous: ${PREVIOUS:-none})"

log "pulling images"
dc pull api worker migrate static web

log "running migrations (expand-only)"
dc --profile migrate run --rm migrate

log "publishing static shells"
dc run --rm static

wait_healthy() {
	local ids=("$@") deadline=$((SECONDS + 120))
	for id in "${ids[@]}"; do
		while true; do
			status="$(docker inspect -f '{{.State.Health.Status}}' "$id" 2>/dev/null || echo missing)"
			[[ "$status" == "healthy" ]] && break
			if (( SECONDS > deadline )); then
				log "container $id not healthy (status=$status)"; return 1
			fi
			sleep 2
		done
	done
}

log "rolling update of api"
mapfile -t OLD < <(dc ps -q api)
if (( ${#OLD[@]} == 0 )); then
	dc up -d --no-deps --scale "api=$API_REPLICAS" api
	mapfile -t NEW < <(dc ps -q api)
else
	# Start new replicas next to the old ones (same service, new image).
	dc up -d --no-deps --no-recreate --scale "api=$(( ${#OLD[@]} + API_REPLICAS ))" api
	mapfile -t ALL < <(dc ps -q api)
	NEW=()
	for id in "${ALL[@]}"; do
		[[ " ${OLD[*]} " == *" $id "* ]] || NEW+=("$id")
	done
fi
if ! wait_healthy "${NEW[@]}"; then
	log "new api replicas unhealthy; removing them and keeping the old version"
	docker rm -f "${NEW[@]}" >/dev/null || true
	exit 1
fi
# Caddy re-resolves the api DNS every second and health-checks /readyz;
# give it time to pick up the new replicas, then drain the old ones.
sleep 3
if (( ${#OLD[@]} > 0 )); then
	log "draining old api replicas"
	docker stop -t 30 "${OLD[@]}" >/dev/null
	docker rm "${OLD[@]}" >/dev/null
fi

log "restarting worker"
dc up -d --no-deps worker web caddy

log "smoke test"
if ! "$ROOT/scripts/smoke-test.sh" "$INSTANCE"; then
	log "smoke test failed"
	if [[ -n "$PREVIOUS" && "${ROLLBACK_ON_FAILURE:-true}" == "true" ]]; then
		log "rolling back to $PREVIOUS"
		ROLLBACK_ON_FAILURE=false exec "$0" "$INSTANCE" "$PREVIOUS"
	fi
	exit 1
fi

[[ -n "$PREVIOUS" ]] && echo "$PREVIOUS" > "$STATE/previous_version"
echo "$VERSION" > "$STATE/current_version"
log "deployed $VERSION"
