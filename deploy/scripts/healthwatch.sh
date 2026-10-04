#!/usr/bin/env bash
# Minimum production monitoring for Release 1 (PRD P1 FR-REL-03, Technical
# Doc §11). P0 builds no monitoring stack; this script runs every 5 minutes
# from cron on the App Host and posts an alert to ALERT_WEBHOOK_URL (Slack /
# Google Chat / WhatsApp gateway compatible JSON {"text": …}) when a check
# fails. An external uptime probe (UptimeRobot, Better Stack …) must also
# watch the public URLs: this script cannot report its own host being down.
#
#   */5 * * * *  ONECLUB_INSTANCE=mgcc /srv/oneclub/scripts/healthwatch.sh
#
# Environment:
#   ONECLUB_INSTANCE     instance code (database oneclub_<code>)
#   API_URL              default https://api.<instance>.oneclub.id
#   WEB_URL              public website URL (optional)
#   ALERT_WEBHOOK_URL    where alerts are posted (required in production)
#   DB_URL               psql connection for read-only checks (default: owner via docker)
#   DISK_PATHS           mount points to watch (default "/ /var/lib/docker")
set -uo pipefail

INSTANCE="${ONECLUB_INSTANCE:?set ONECLUB_INSTANCE}"
API_URL="${API_URL:-https://api.${INSTANCE}.oneclub.id}"
WEB_URL="${WEB_URL:-}"
DISK_PATHS="${DISK_PATHS:-/ /var/lib/docker}"
STATE_DIR="${STATE_DIR:-/var/lib/oneclub-healthwatch}"
DC_DB="docker compose -p oneclub-db -f ${ONECLUB_ROOT:-/srv/oneclub}/compose/db/compose.yaml"
mkdir -p "$STATE_DIR"

failures=()
fail() { failures+=("$1"); }

psql_q() {
	if [[ -n "${DB_URL:-}" ]]; then
		psql "$DB_URL" -At -c "$1"
	else
		$DC_DB exec -T -u postgres primary psql -d "oneclub_${INSTANCE}" -At -c "$1"
	fi
}

# 1. API and website respond (readiness includes the database)
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$API_URL/readyz" || echo 000)
[[ "$code" == "200" ]] || fail "API /readyz returned $code"
if [[ -n "$WEB_URL" ]]; then
	code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$WEB_URL" || echo 000)
	[[ "$code" == "200" ]] || fail "website returned $code"
fi

# 2. Background jobs failing repeatedly (River) and queue backlog
failed=$(psql_q "SELECT count(*) FROM river_job WHERE state = 'discarded' AND finalized_at > now() - interval '15 minutes'" 2>/dev/null || echo "?")
[[ "$failed" == "?" ]] && fail "cannot read River jobs" || { (( failed > 0 )) && fail "$failed background job(s) failed for good in the last 15 min"; }
backlog=$(psql_q "SELECT count(*) FROM river_job WHERE state = 'available' AND scheduled_at < now() - interval '10 minutes'" 2>/dev/null || echo 0)
(( backlog > 100 )) && fail "$backlog jobs waiting for more than 10 min"

# 3. Outbox lag (events not dispatched for 5 minutes)
lag=$(psql_q "SELECT coalesce(extract(epoch FROM now() - min(occurred_at))::int, 0) FROM platform.outbox WHERE dispatched_at IS NULL" 2>/dev/null || echo 0)
(( lag > 300 )) && fail "outbox lag ${lag}s"

# 4. Replication lag of the read replica
rlag=$(psql_q "SELECT coalesce(max(extract(epoch FROM replay_lag))::int, -1) FROM pg_stat_replication" 2>/dev/null || echo -1)
if (( rlag < 0 )); then fail "no streaming replica connected"; elif (( rlag > 60 )); then fail "replica lag ${rlag}s"; fi

# 5. Last backup younger than 26 h and WAL archiving healthy
if out=$($DC_DB exec -T -u postgres primary pgbackrest --stanza=oneclub --output=json info 2>/dev/null); then
	last=$(echo "$out" | python3 -c 'import json,sys; d=json.load(sys.stdin); b=d[0]["backup"]; print(b[-1]["timestamp"]["stop"] if b else 0)')
	age=$(( $(date +%s) - ${last:-0} ))
	(( age > 93600 )) && fail "last backup is $((age / 3600)) h old"
	archived=$(psql_q "SELECT coalesce(extract(epoch FROM now() - last_archived_time)::int, 999999) FROM pg_stat_archiver" 2>/dev/null || echo 0)
	afail=$(psql_q "SELECT failed_count FROM pg_stat_archiver" 2>/dev/null || echo 0)
	(( archived > 900 )) && fail "WAL not archived for ${archived}s"
	prev=$(cat "$STATE_DIR/archive_failed" 2>/dev/null || echo "$afail"); echo "$afail" > "$STATE_DIR/archive_failed"
	(( afail > prev )) && fail "WAL archive failures increased ($prev → $afail)"
else
	fail "pgBackRest info failed"
fi

# 6. Disk usage
for p in $DISK_PATHS; do
	[[ -d "$p" ]] || continue
	use=$(df -P "$p" | awk 'NR==2 {gsub("%","",$5); print $5}')
	(( use > 80 )) && fail "disk $p ${use}% used"
done

# 7. TLS certificate expiry (< 14 days)
host=$(echo "$API_URL" | sed -E 's#https?://([^/:]+).*#\1#')
if [[ "$API_URL" == https://* ]]; then
	end=$(echo | openssl s_client -servername "$host" -connect "$host:443" 2>/dev/null | openssl x509 -noout -enddate 2>/dev/null | cut -d= -f2)
	if [[ -n "$end" ]]; then
		days=$(( ($(date -d "$end" +%s) - $(date +%s)) / 86400 ))
		(( days < 14 )) && fail "TLS certificate of $host expires in $days days"
	fi
fi

# Alert once per distinct failure set; a recovery message when it clears.
sig=$(printf '%s\n' "${failures[@]:-}" | sha256sum | cut -c1-16)
prev=$(cat "$STATE_DIR/last" 2>/dev/null || echo "ok")
if (( ${#failures[@]} > 0 )); then
	if [[ "$sig" != "$prev" ]]; then
		text="OneClub ${INSTANCE} ALERT:"$'\n'"$(printf -- '- %s\n' "${failures[@]}")"
		[[ -n "${ALERT_WEBHOOK_URL:-}" ]] && curl -s -X POST -H 'Content-Type: application/json' --max-time 10 \
			-d "$(python3 -c 'import json,sys; print(json.dumps({"text": sys.argv[1]}))' "$text")" "$ALERT_WEBHOOK_URL" >/dev/null
		echo "$text" >&2
		echo "$sig" > "$STATE_DIR/last"
	fi
	exit 1
fi
if [[ "$prev" != "ok" ]]; then
	[[ -n "${ALERT_WEBHOOK_URL:-}" ]] && curl -s -X POST -H 'Content-Type: application/json' --max-time 10 \
		-d "{\"text\":\"OneClub ${INSTANCE}: all checks OK again\"}" "$ALERT_WEBHOOK_URL" >/dev/null
	echo ok > "$STATE_DIR/last"
fi
exit 0
