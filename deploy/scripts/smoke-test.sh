#!/usr/bin/env bash
# Post-deploy smoke test: health, readiness, public bootstrap and one shell.
#   smoke-test.sh <instance>
set -euo pipefail
INSTANCE="${1:?usage: smoke-test.sh <instance>}"
ROOT="${ONECLUB_ROOT:-/srv/oneclub}"
# shellcheck disable=SC1090
source "$ROOT/instances/$INSTANCE/.env"
BASE="https://${DOMAIN_BACKOFFICE:?DOMAIN_BACKOFFICE missing in .env}"

check() {
	local url="$1" expect="$2"
	code="$(curl -sk -o /tmp/smoke.$$ -w '%{http_code}' --max-time 10 "$url")"
	if [[ "$code" != "$expect" ]]; then
		echo "FAIL $url → $code (want $expect)"; cat /tmp/smoke.$$; rm -f /tmp/smoke.$$; return 1
	fi
	echo "ok   $url → $code"
}

check "$BASE/api/v1/public/bootstrap" 200
check "$BASE/api/v1/auth/me" 401
check "$BASE/" 200
# 20 requests across replicas must all succeed while traffic is shifting.
for i in $(seq 1 20); do check "$BASE/api/v1/public/bootstrap" 200 >/dev/null; done
rm -f /tmp/smoke.$$
echo "smoke test passed"
