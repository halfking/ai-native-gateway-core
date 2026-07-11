#!/usr/bin/env bash
set -euo pipefail

REPORT_FILE="${REPORT_FILE:-LOCAL_DATABASE_SCHEMA_REPORT.md}"
: "${DATABASE_URL:?DATABASE_URL must be explicitly set}"
command -v psql >/dev/null 2>&1 || { printf 'error: psql is required\n' >&2; exit 1; }
umask 077
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

{
  printf '# Local Database Schema Report\n\n'
  printf '**Generated**: %s\n\n' "$(date '+%Y-%m-%d %H:%M:%S')"
  printf '## Statistics\n\n```text\n'
  psql -X -v ON_ERROR_STOP=1 -At "$DATABASE_URL" -c "SELECT 'tables='||count(*) FROM pg_tables WHERE schemaname='public' UNION ALL SELECT 'indexes='||count(*) FROM pg_indexes WHERE schemaname='public' UNION ALL SELECT 'views='||count(*) FROM pg_views WHERE schemaname='public';"
  printf '```\n\n## Tables\n\n```text\n'
  psql -X -v ON_ERROR_STOP=1 -At "$DATABASE_URL" -c "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename;"
  printf '```\n\n## Columns\n\n```text\n'
  psql -X -v ON_ERROR_STOP=1 -At "$DATABASE_URL" -c "SELECT table_name||'.'||column_name||' '||data_type||CASE WHEN is_nullable='NO' THEN ' NOT NULL' ELSE '' END||COALESCE(' DEFAULT '||column_default,'') FROM information_schema.columns WHERE table_schema='public' ORDER BY table_name,ordinal_position;"
  printf '```\n\n## Indexes\n\n```text\n'
  psql -X -v ON_ERROR_STOP=1 -At "$DATABASE_URL" -c "SELECT indexname||' '||indexdef FROM pg_indexes WHERE schemaname='public' ORDER BY tablename,indexname;"
  printf '```\n'
} >"$tmp"
mv "$tmp" "$REPORT_FILE"
chmod 0600 "$REPORT_FILE"
printf 'Schema report written to %s (0600).\n' "$REPORT_FILE"
