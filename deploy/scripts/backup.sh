#!/usr/bin/env bash
# Scheduled backups on the DB Host (Technical Doc §7.7). Install as cron:
#   15 1 * * *   backup.sh full      # daily full backup
#   15 13 * * *  backup.sh diff      # midday differential
#   30 2 1 * *   backup.sh monthly   # monthly copy kept 60 months = 5 years (repo2, FR-REL-P4-06)
# WAL is archived continuously by archive_command (RPO ≤ 5 minutes).
set -euo pipefail
TYPE="${1:-full}"
DC="docker compose -p oneclub-db -f ${ONECLUB_ROOT:-/srv/oneclub}/compose/db/compose.yaml"
case "$TYPE" in
	full|diff|incr)
		$DC exec -T -u postgres primary pgbackrest --stanza=oneclub --repo=1 --type="$TYPE" backup ;;
	monthly)
		$DC exec -T -u postgres primary pgbackrest --stanza=oneclub --repo=2 --type=full backup ;;
	*)
		echo "usage: backup.sh full|diff|incr|monthly" >&2; exit 2 ;;
esac
$DC exec -T -u postgres primary pgbackrest --stanza=oneclub info
