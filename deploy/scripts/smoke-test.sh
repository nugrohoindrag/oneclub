#!/usr/bin/env bash
# Post-deploy smoke test: health, readiness, public bootstrap and one shell.
#   smoke-test.sh <instance>
set -euo pipefail
INSTANCE="${1:?usage: smoke-test.sh <instance>}"
ROOT="${ONECLUB_ROOT:-/srv/oneclub}"
# shellcheck disable=SC1090
source "$ROOT/instances/$INSTANCE/.env"
BASE="https://${DOMAIN_DASHBOARD:?DOMAIN_DASHBOARD missing in .env}"

check() {
	local url="$1" expect="$2"
	code="$(curl -sk -o /tmp/smoke.$$ -w '%{http_code}' --max-time 10 "$url")"
	if [[ "$code" != "$expect" ]]; then
		echo "FAIL $url → $code (want $expect)"; cat /tmp/smoke.$$; rm -f /tmp/smoke.$$; return 1
	fi
	echo "ok   $url → $code"
}

# A fresh host gets its certificates on the first requests: wait up to 2 minutes.
for i in $(seq 1 24); do
	[[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$BASE/api/v1/public/bootstrap")" == 200 ]] && break
	sleep 5
done
check "$BASE/api/v1/public/bootstrap" 200
check "$BASE/api/v1/auth/me" 401
check "$BASE/" 200
# The Staff App domains each serve their surface (Technical Doc §6.1).
for d in "$DOMAIN_DASHBOARD" "${DOMAIN_CASHIER:?}" "${DOMAIN_CADDY:?}" "${DOMAIN_KITCHEN:?}" "${DOMAIN_PRESENCE:?}"; do check "https://$d/surface.json" 200; done
# 20 requests across replicas must all succeed while traffic is shifting.
for i in $(seq 1 20); do check "$BASE/api/v1/public/bootstrap" 200 >/dev/null; done
rm -f /tmp/smoke.$$
echo "smoke test passed"
