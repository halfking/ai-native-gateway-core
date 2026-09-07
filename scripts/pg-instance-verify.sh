#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest=""
policy=""
output_dir=""
impact=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) manifest="${2:-}"; shift 2 ;;
    --policy) policy="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    --impact-matrix) impact="${2:-}"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ -r "$manifest" && -r "$policy" ]] || die "readable --manifest and --policy are required"
[[ -n "$output_dir" ]] || die "--output-dir is required"
mkdir -p "$output_dir"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-connections.sh"
pg_instance_connections_init
pg_instance_tunnel_open
trap pg_instance_tunnel_close EXIT

disabled_sql="SELECT count(*) FROM pg_trigger t
  JOIN pg_class c ON c.oid=t.tgrelid
  JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE NOT t.tgisinternal AND t.tgenabled<>'O'
    AND n.nspname NOT IN ('pg_catalog','information_schema')"

report="$output_dir/verify-report.tsv"
printf 'remote_database\tfk_orphans\tdisabled_triggers\tstatus\n' >"$report"
failed=0
while IFS=$'\t' read -r classification _local_db remote_db _mode; do
  [[ "$classification" == \#* || "$classification" == "classification" ]] && continue
  [[ "$classification" == "COMMON" || "$classification" == "ALIAS" ]] || continue
  orphans="$(pg_instance_remote_psql "$remote_db" \
    <"$ROOT/scripts/sql/pg-instance-fk-orphans.sql" | sed '/^$/d' | wc -l | tr -d ' ')"
  disabled="$(pg_instance_remote_psql "$remote_db" -Atc "$disabled_sql")"
  status="OK"
  if [[ "$orphans" != "0" || "$disabled" != "0" ]]; then
    status="FAIL"
    failed=1
  fi
  printf '%s\t%s\t%s\t%s\n' "$remote_db" "$orphans" "$disabled" "$status" >>"$report"
done <"$manifest"

deferred="$output_dir/deferred-semantic.tsv"
printf 'action\tlocal_database\tkind\tschema\tobject\tsub_name\n' >"$deferred"
if [[ -n "$impact" ]]; then
  [[ -r "$impact" ]] || die "readable --impact-matrix is required when supplied"
  awk -F'\t' 'NR>1 && $4!="OWNER" && $4!="GRANT" {
    print $1 "\t" $2 "\t" $4 "\t" $5 "\t" $6 "\t" $7
  }' "$impact" | LC_ALL=C sort >>"$deferred"
fi

printf 'verify_report=%s\n' "$report"
printf 'deferred_semantic=%s\n' "$deferred"
[[ "$failed" -eq 0 ]] || die "verification found FK orphans or disabled triggers"
