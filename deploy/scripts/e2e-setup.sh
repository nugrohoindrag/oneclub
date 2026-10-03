#!/usr/bin/env bash
# Prepares browser-test accounts on a Staging instance (release workflow).
# Reads web/e2e/setup.sql from stdin and runs it on the DB host:
#
#   ssh deploy@staging /srv/oneclub/scripts/e2e-setup.sh <instance> < web/e2e/setup.sql
set -euo pipefail

INSTANCE="${1:?usage: e2e-setup.sh <instance> < setup.sql}"
if [ "${ONECLUB_ENV:-staging}" = "production" ]; then
	echo "e2e-setup.sh must never run on Production" >&2
	exit 1
fi
DC="docker compose -p oneclub-db -f ${ONECLUB_ROOT:-/srv/oneclub}/compose/db/compose.yaml"
$DC exec -T -u postgres primary psql -d "oneclub_$INSTANCE" -v ON_ERROR_STOP=1 -q -f -
