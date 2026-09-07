#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest=""
policy=""
output_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) manifest="${2:-}"; shift 2 ;;
    --policy) policy="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ -r "$manifest" ]] || die "readable --manifest is required"
[[ -r "$policy" ]] || die "readable --policy is required"
[[ -n "$output_dir" ]] || die "--output-dir is required"
mkdir -p "$output_dir/signatures"

DATABASE_SCHEMA_ALLOWLIST=""
EXCLUDE_SCHEMA_REGEX=""
# shellcheck disable=SC1090
source "$policy"
export PG_INSTANCE_EXCLUDE_SCHEMA_REGEX="$EXCLUDE_SCHEMA_REGEX"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-connections.sh"
pg_instance_connections_init
pg_instance_tunnel_open
trap pg_instance_tunnel_close EXIT

schema_regex_for() {
  local database="$1" mapping names
  for mapping in $DATABASE_SCHEMA_ALLOWLIST; do
    [[ "${mapping%%=*}" == "$database" ]] || continue
    names="${mapping#*=}"
    printf '^(%s)$' "$(printf '%s' "$names" | tr ',' '|')"
    return
  done
  printf ''
}

impact="$output_dir/impact-matrix.tsv"
printf 'action\tlocal_database\tremote_database\tkind\tschema\tobject\tsub_name\tlocal_hash\tremote_hash\n' >"$impact"

compare_pair() {
  local local_db="$1" remote_db="$2" regex="$3"
  local left="$output_dir/signatures/local-$local_db.txt"
  local right="$output_dir/signatures/remote-$remote_db.txt"
  local pair="$output_dir/signatures/pair-$local_db-$remote_db.tsv"
  pg_instance_signature local "$local_db" "$regex" "$left"
  pg_instance_signature remote "$remote_db" "$regex" "$right"
  awk -F'|' -v OFS='\t' -v ldb="$local_db" -v rdb="$remote_db" '
    NR==FNR {
      key=$1 FS $2 FS $3 FS $4
      remote[key]=$5
      next
    }
    {
      key=$1 FS $2 FS $3 FS $4
      seen[key]=1
      if (!(key in remote))
        print "ADD_REMOTE",ldb,rdb,$1,$2,$3,$4,$5,""
      else if (remote[key] != $5)
        print "CONFLICT_LOCAL_WINS",ldb,rdb,$1,$2,$3,$4,$5,remote[key]
    }
    END {
      for (key in remote) {
        if (key in seen) continue
        split(key, part, FS)
        print "ADD_LOCAL",ldb,rdb,part[1],part[2],part[3],part[4],"",remote[key]
      }
    }
  ' "$right" "$left" | LC_ALL=C sort >"$pair"
  cat "$pair" >>"$impact"
}

while IFS=$'\t' read -r classification local_db remote_db mode; do
  [[ "$classification" == \#* || "$classification" == "classification" ]] && continue
  case "$classification" in
    COMMON|ALIAS)
      compare_pair "$local_db" "$remote_db" "$(schema_regex_for "$local_db")"
      ;;
    LOCAL_ONLY)
      printf 'CREATE_REMOTE_DATABASE\t%s\t-\tDATABASE\t-\t%s\t-\t-\t-\n' \
        "$local_db" "$local_db" >>"$impact"
      ;;
    REMOTE_ONLY)
      printf 'BOOTSTRAP_LOCAL_DATABASE\t-\t%s\tDATABASE\t-\t%s\t-\t-\t-\n' \
        "$remote_db" "$remote_db" >>"$impact"
      ;;
    EXCLUDED) : ;;
    *) die "unsupported manifest classification: $classification ($mode)" ;;
  esac
done <"$manifest"

summary="$output_dir/impact-summary.tsv"
{
  printf 'action\tcount\n'
  awk -F'\t' 'NR>1 {count[$1]++} END {for (k in count) print k "\t" count[k]}' \
    "$impact" | LC_ALL=C sort
} >"$summary"
printf 'impact_matrix=%s\nimpact_summary=%s\n' "$impact" "$summary"
