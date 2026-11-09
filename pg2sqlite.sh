#!/usr/bin/env bash
# Usage: ./pg2sqlite.sh [target.db]   (streams, no intermediate file)
set -euo pipefail
DB="${1:-meteocomp.db}"
SCHEMA="$(dirname "$0")/internal/store/schema_sqlite.sql"

{
  echo "PRAGMA journal_mode=WAL;"
  cat "$SCHEMA"
  echo "BEGIN;"
  echo "CREATE TEMP TABLE readings_in AS SELECT * FROM readings WHERE 0;"
  docker exec meteocomp-mcdb-1 pg_dump -U meteocomp --data-only --inserts --column-inserts \
      --no-comments -t readings -t settings -t locations -t providers \
    | grep -E '^INSERT INTO ' \
    | sed -E -e 's/^INSERT INTO public\.readings /INSERT INTO readings_in /' \
             -e 's/^INSERT INTO public\./INSERT OR REPLACE INTO /'
  # readings has no primary key: only add rows not already present
  echo "INSERT INTO readings SELECT i.* FROM readings_in i WHERE NOT EXISTS (
    SELECT 1 FROM readings r WHERE r.polled_at = i.polled_at AND r.provider = i.provider
      AND r.location_name = i.location_name AND r.forecast_time = i.forecast_time AND r.kind = i.kind);"
  echo "COMMIT;"
} | sqlite3 "$DB"
