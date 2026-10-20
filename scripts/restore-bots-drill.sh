#!/usr/bin/env sh
# Restore a Bots schema dump into a dedicated, already-created database and
# compare row counts with the source. This is a drill, not a live restore.
set -eu

: "${BOTS_DATABASE_URL:?export BOTS_DATABASE_URL (source) before running}"
: "${BOTS_RESTORE_DATABASE_URL:?export BOTS_RESTORE_DATABASE_URL (dedicated drill database)}"
dump=${1:?usage: restore-bots-drill.sh BACKUP.dump}
[ -f "$dump" ] || { echo "backup not found: $dump" >&2; exit 2; }
command -v pg_restore >/dev/null 2>&1 || { echo "pg_restore is required" >&2; exit 2; }
server_version=$(psql "$BOTS_RESTORE_DATABASE_URL" -Atc 'SHOW server_version')
server_major=${server_version%%.*}
client_major=$(pg_restore --version | awk '{print $3}' | cut -d. -f1)
if [ "$client_major" != "$server_major" ]; then
  echo "pg_restore major $client_major does not match target server major $server_major" >&2
  echo "use the PostgreSQL container's pg_restore for the matching major" >&2
  exit 2
fi

source_db=$(psql "$BOTS_DATABASE_URL" -Atc 'SELECT current_database()')
target_db=$(psql "$BOTS_RESTORE_DATABASE_URL" -Atc 'SELECT current_database()')
if [ "$source_db" = "$target_db" ]; then
  echo "refusing to restore over the source database $source_db" >&2
  exit 2
fi
case "$target_db" in
  bots_restore_*) ;;
  *) echo "refusing target database $target_db: name must start with bots_restore_" >&2; exit 2 ;;
esac
if [ "$(psql "$BOTS_RESTORE_DATABASE_URL" -Atc "SELECT to_regnamespace('bots') IS NOT NULL")" = "t" ]; then
  echo "refusing target database $target_db: it already contains a bots schema" >&2
  exit 2
fi

# The dump contains only the bots schema. pg_restore --schema=bots filters out
# its CREATE SCHEMA entry, so restore the scoped archive without that filter.
pg_restore --dbname="$BOTS_RESTORE_DATABASE_URL" --no-owner --no-privileges "$dump"

source_bots=$(psql "$BOTS_DATABASE_URL" -Atc 'SELECT count(*) FROM bots.bots')
target_bots=$(psql "$BOTS_RESTORE_DATABASE_URL" -Atc 'SELECT count(*) FROM bots.bots')
source_audit=$(psql "$BOTS_DATABASE_URL" -Atc 'SELECT count(*) FROM bots.audit_events')
target_audit=$(psql "$BOTS_RESTORE_DATABASE_URL" -Atc 'SELECT count(*) FROM bots.audit_events')
[ "$source_bots" = "$target_bots" ] || { echo "bots count mismatch: $source_bots != $target_bots" >&2; exit 1; }
[ "$source_audit" = "$target_audit" ] || { echo "audit count mismatch: $source_audit != $target_audit" >&2; exit 1; }
printf 'restore drill passed: bots=%s audit=%s target=%s\n' "$target_bots" "$target_audit" "$target_db"
