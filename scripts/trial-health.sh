#!/usr/bin/env sh
# Loopback trial check. Exits 1 when the process is not ready, 2 when any
# delivery is unknown. Rejected deliveries are reported but do not fail the
# liveness check; an operator must review them separately.
set -eu
BOTS_URL="${BOTS_URL:-http://127.0.0.1:8090}"
metrics=$(curl -fsS --max-time 5 "$BOTS_URL/metrics") || {
  echo "trial health: metrics endpoint unavailable at $BOTS_URL" >&2
  exit 1
}
metric() {
  awk -v key="$1" '$1 == key { print $2; found=1 } END { if (!found) print "0" }' <<EOF
$metrics
EOF
}
ready=$(metric bots_ready)
unknown=$(printf '%s\n' "$metrics" | awk '$1 ~ /^bots_deliveries_total\{state="unknown"\}/ { print $2; found=1 } END { if (!found) print "0" }')
rejected=$(printf '%s\n' "$metrics" | awk '$1 ~ /^bots_deliveries_total\{state="rejected"\}/ { print $2; found=1 } END { if (!found) print "0" }')
knowledge=$(metric bots_knowledge_active)
echo "trial health: ready=$ready unknown=$unknown rejected=$rejected active_knowledge=$knowledge"
[ "$ready" = "1" ] || exit 1
[ "$unknown" = "0" ] || exit 2
