#!/usr/bin/env sh
# Back up only the Bots-owned schema. Chronotope run/timer state is owned by
# the other repository and needs a coordinated backup for full-stack recovery.
set -eu

: "${BOTS_DATABASE_URL:?export BOTS_DATABASE_URL before running this backup}"
command -v pg_dump >/dev/null 2>&1 || { echo "pg_dump is required" >&2; exit 2; }

server_version=$(psql "$BOTS_DATABASE_URL" -Atc 'SHOW server_version')
server_major=${server_version%%.*}
client_major=$(pg_dump --version | awk '{print $3}' | cut -d. -f1)
if [ "$client_major" != "$server_major" ]; then
  echo "pg_dump major $client_major does not match server major $server_major" >&2
  echo "use the PostgreSQL container's pg_dump for the matching major" >&2
  exit 2
fi

out=${1:-"backups/bots-$(date -u +%Y%m%dT%H%M%SZ).dump"}
mkdir -p "$(dirname "$out")"
umask 077
pg_dump "$BOTS_DATABASE_URL" --schema=bots --format=custom --no-owner --no-privileges --file="$out"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$out" > "$out.sha256"
else
  shasum -a 256 "$out" > "$out.sha256"
fi
printf 'bots schema backup: %s\n' "$out"
cat "$out.sha256"
