#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASELINE_DIR="$ROOT_DIR/deploy/sql/schemas/baseline"
INSTALLER_DIR="$ROOT_DIR/installer/cmd/llm-gw-installer/embeddata"
STARTUP_DIR="$ROOT_DIR/sql/migrations/startup"

for name in 00-prereqs.sql 01-schema.sql; do
  cmp -s "$ROOT_DIR/sql/schema/$name" "$BASELINE_DIR/$name" || {
    printf 'schema mirror mismatch: sql/schema/%s != deploy baseline\n' "$name" >&2
    exit 1
  }
done

normalize_seed() {
  sed -E "/^INSERT INTO public\.routing_policy VALUES \(1, 'default',/d" "$1" |
    tr -d '[:space:]'
}
[[ "$(normalize_seed "$ROOT_DIR/sql/schema/02-seed.sql")" == "$(normalize_seed "$BASELINE_DIR/02-seed.sql")" ]] || {
  printf 'seed mirror mismatch outside permitted routing_policy model seed\n' >&2
  exit 1
}

for name in 01-schema.sql 02-seed.sql; do
  diff -w -B "$INSTALLER_DIR/$name" "$BASELINE_DIR/$name" >/dev/null || {
    printf 'installer mirror mismatch ignoring whitespace: %s\n' "$name" >&2
    exit 1
  }
done

normalize_prereqs() {
  sed -E 's/--.*$//' "$1" |
    grep -v -E '^[[:space:]]*CREATE EXTENSION IF NOT EXISTS (pg_stat_statements|pgstattuple|citus|citus_columnar|vector) ' |
    tr -d '[:space:]'
}
[[ "$(normalize_prereqs "$INSTALLER_DIR/00-prereqs.sql")" == "$(normalize_prereqs "$BASELINE_DIR/00-prereqs.sql")" ]] || {
  printf 'installer prereqs mirror mismatch outside permitted optional extensions\n' >&2
  exit 1
}

for name in \
  536_stats_analytics_foundation.sql \
  537_usage_facts.sql \
  539_stats_reconciliation_tenant.sql \
  540_stats_event_inbox_consumer.sql; do
  test -f "$STARTUP_DIR/$name" || {
    printf 'missing canonical startup migration: %s\n' "$name" >&2
    exit 1
  }
  cmp -s "$STARTUP_DIR/$name" "$INSTALLER_DIR/startup/$name" || {
    printf 'installer startup migration mismatch: %s\n' "$name" >&2
    exit 1
  }
done

if {
  find "$BASELINE_DIR" -maxdepth 1 -type f -name '*.sql' -exec grep -l -E 'stats_event_inbox|usage_facts|stats_reconciliation_diffs' {} +
  find "$INSTALLER_DIR" -maxdepth 1 -type f -name '*.sql' -exec grep -l -E 'stats_event_inbox|usage_facts|stats_reconciliation_diffs' {} +
} | grep -q .; then
  printf 'stats startup migrations must not be copied into baseline mirrors\n' >&2
  exit 1
fi

printf 'stats schema mirror verification passed\n'
