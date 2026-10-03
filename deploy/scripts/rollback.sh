#!/usr/bin/env bash
# Roll back an instance to the previously deployed version (FR-TEC-08).
#   rollback.sh <instance> [version]
set -euo pipefail
INSTANCE="${1:?usage: rollback.sh <instance> [version]}"
ROOT="${ONECLUB_ROOT:-/srv/oneclub}"
TARGET="${2:-$(cat "$ROOT/instances/$INSTANCE/.deploy/previous_version" 2>/dev/null || true)}"
if [[ -z "$TARGET" ]]; then
	echo "no previous version recorded; pass a version explicitly" >&2
	exit 1
fi
echo "rolling back $INSTANCE to $TARGET"
ROLLBACK_ON_FAILURE=false exec "$ROOT/scripts/deploy.sh" "$INSTANCE" "$TARGET"
