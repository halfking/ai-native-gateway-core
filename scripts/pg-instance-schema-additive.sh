#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest=""
manifest_hash=""
policy=""
impact=""
work_dir=""
yes=false
fresh=false
dry_run=false
database_filter=""
llm_ssot_allowlist=""
llm_ssot_allowlist_hash=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) manifest="${2:-}"; shift 2 ;;
    --manifest-hash) manifest_hash="${2:-}"; shift 2 ;;
    --policy) policy="${2:-}"; shift 2 ;;
    --impact-matrix) impact="${2:-}"; shift 2 ;;
    --work-dir) work_dir="${2:-}"; shift 2 ;;
    --yes) yes=true; shift ;;
    --freshness-check) fresh=true; shift ;;
    --dry-run) dry_run=true; shift ;;
    --database) database_filter="${2:-}"; shift 2 ;;
    --llm-ssot-allowlist) llm_ssot_allowlist="${2:-}"; shift 2 ;;
    --llm-ssot-allowlist-hash) llm_ssot_allowlist_hash="${2:-}"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ "$yes" == true && "$fresh" == true ]] ||
  die "--yes and --freshness-check are required"
[[ -r "$manifest" && -r "$policy" && -r "$impact" ]] ||
  die "manifest, policy, and impact matrix must be readable"
[[ -n "$work_dir" ]] || die "--work-dir is required"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-guardrails.sh"
validate_manifest_freshness "$manifest" "$manifest_hash" "$policy"
validate_llm_gateway_protection apply-schema "$manifest"
if [[ -n "$llm_ssot_allowlist" ]]; then
  [[ -r "$llm_ssot_allowlist" && -n "$llm_ssot_allowlist_hash" ]] ||
    die "LLM SSOT allowlist requires readable file and hash"
  [[ "$(shasum -a 256 "$llm_ssot_allowlist" | awk '{print $1}')" ==
    "$llm_ssot_allowlist_hash" ]] || die "LLM SSOT allowlist hash mismatch"
fi
LLM_SCHEMA_EVENT_TRIGGER=""
# shellcheck disable=SC1090
source "$policy"
mkdir -p "$work_dir"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-connections.sh"
pg_instance_connections_init
pg_instance_tunnel_open
trap pg_instance_tunnel_close EXIT

source_query() {
  local side="$1" database="$2"
  shift 2
  if [[ "$side" == "local" ]]; then
    pg_instance_local_psql "$database" "$@"
  else
    pg_instance_remote_psql "$database" "$@"
  fi
}

llm_object_allowed() {
  local database="$1" schema="$2" object="$3"
  [[ "$database" != "llm_gateway" ]] && return 0
  [[ -r "$llm_ssot_allowlist" ]] &&
    grep -Fxq "$schema.$object" "$llm_ssot_allowlist"
}

foundation_sql() {
  local action="$1" source_side="$2" database="$3" local_db="$4" remote_db="$5"
  local phase="$6" output="$7"
  local _action _local _remote kind schema object _sub _priority query ddl
  printf "SET lock_timeout='5s';\nSET statement_timeout='10min';\n" >"$output"
  while IFS=$'\034' read -r _action _local _remote kind schema object _sub _priority; do
    llm_object_allowed "$local_db" "$schema" "$object" || continue
    [[ "$phase" == "functions" && "$kind" != "FUNCTION" ]] && continue
    [[ "$phase" == "foundation" && "$kind" == "FUNCTION" ]] && continue
    if [[ "$kind" == "COLUMN" || "$kind" == "CONSTRAINT" || "$kind" == "INDEX" ]] &&
      awk -F'\t' -v a="$action" -v l="$local_db" -v r="$remote_db" \
        -v s="$schema" -v o="$object" '
        $1==a && $2==l && $3==r && $4=="RELATION" && $5==s && $6==o {found=1}
        END {exit found ? 0 : 1}
      ' "$impact"; then
      continue
    fi
    if [[ "$kind" == "INDEX" ]] &&
      awk -F'\t' -v a="$action" -v l="$local_db" -v r="$remote_db" \
        -v s="$schema" -v o="$object" -v sub_name="$_sub" '
        $1==a && $2==l && $3==r && $4=="CONSTRAINT" &&
          $5==s && $6==o && $7==sub_name {found=1}
        END {exit found ? 0 : 1}
      ' "$impact"; then
      continue
    fi
    case "$kind" in
      SCHEMA)
        query="SELECT format('CREATE SCHEMA IF NOT EXISTS %I;', :'schema')"
        ddl="$(printf '%s\n' "$query" |
          source_query "$source_side" "$database" -At -v schema="$schema")"
        ;;
      EXTENSION)
        query="SELECT format('CREATE EXTENSION IF NOT EXISTS %I WITH SCHEMA %I;', :'object', :'schema')"
        ddl="$(printf '%s\n' "$query" |
          source_query "$source_side" "$database" -At \
            -v object="$object" -v schema="$schema")"
        ;;
      TYPE)
        query="
          SELECT format('CREATE TYPE %I.%I AS ENUM (%s);', n.nspname,t.typname,
            string_agg(quote_literal(e.enumlabel),',' ORDER BY e.enumsortorder))
          FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
          JOIN pg_enum e ON e.enumtypid=t.oid
          WHERE n.nspname=:'schema' AND t.typname=:'object'
          GROUP BY n.nspname,t.typname"
        ddl="$(printf '%s\n' "$query" |
          source_query "$source_side" "$database" -At \
            -v schema="$schema" -v object="$object")"
        [[ -n "$ddl" ]] || die "unsupported or missing type: $database/$schema.$object"
        ;;
      COLUMN)
        query="
          SELECT format(
            'ALTER TABLE %I.%I ADD COLUMN IF NOT EXISTS %I %s%s%s%s;',
            n.nspname,c.relname,a.attname,format_type(a.atttypid,a.atttypmod),
            CASE
              WHEN a.attgenerated<>'' THEN
                ' GENERATED ALWAYS AS ('||pg_get_expr(d.adbin,d.adrelid)||') '||
                CASE a.attgenerated WHEN 'v' THEN 'VIRTUAL' ELSE 'STORED' END
              WHEN a.attidentity<>'' THEN
                ' GENERATED '||CASE a.attidentity WHEN 'a' THEN 'ALWAYS' ELSE 'BY DEFAULT' END||
                ' AS IDENTITY'
              WHEN d.adbin IS NOT NULL THEN ' DEFAULT '||pg_get_expr(d.adbin,d.adrelid)
              ELSE ''
            END,
            CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END,
            ''
          )
          FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          JOIN pg_attribute a ON a.attrelid=c.oid
          LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
          WHERE n.nspname=:'schema' AND c.relname=:'object'
            AND c.relkind IN ('r','p')
            AND a.attname=:'sub' AND NOT a.attisdropped"
        ddl="$(printf '%s\n' "$query" |
          source_query "$source_side" "$database" -At \
            -v schema="$schema" -v object="$object" -v sub="$_sub")"
        [[ -n "$ddl" ]] || continue
        ;;
      FUNCTION)
        query="
          SELECT pg_get_functiondef(p.oid) || ';'
          FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname=:'schema' AND p.proname=:'object'
            AND pg_get_function_identity_arguments(p.oid)=:'sub'"
        ddl="$(printf '%s\n' "$query" |
          source_query "$source_side" "$database" -At \
            -v schema="$schema" -v object="$object" -v sub="$_sub")"
        [[ -n "$ddl" ]] || die "missing source function: $database/$schema.$object($_sub)"
        ;;
      CONSTRAINT)
        query="
          SELECT format('ALTER TABLE %I.%I ADD CONSTRAINT %I %s;',
            n.nspname,c.relname,con.conname,pg_get_constraintdef(con.oid,true))
          FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid
          JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname=:'schema' AND c.relname=:'object'
            AND con.conname=:'sub'"
        ddl="$(printf '%s\n' "$query" |
          source_query "$source_side" "$database" -At \
            -v schema="$schema" -v object="$object" -v sub="$_sub")"
        [[ -n "$ddl" ]] || die "missing source constraint: $database/$schema.$object.$_sub"
        ;;
      INDEX)
        query="
          SELECT pg_get_indexdef(idx.oid) || ';'
          FROM pg_index i JOIN pg_class tbl ON tbl.oid=i.indrelid
          JOIN pg_class idx ON idx.oid=i.indexrelid
          JOIN pg_namespace n ON n.oid=tbl.relnamespace
          WHERE n.nspname=:'schema' AND tbl.relname=:'object'
            AND idx.relname=:'sub'"
        ddl="$(printf '%s\n' "$query" |
          source_query "$source_side" "$database" -At \
            -v schema="$schema" -v object="$object" -v sub="$_sub")"
        [[ -n "$ddl" ]] || die "missing source index: $database/$schema.$object.$_sub"
        ;;
      *) continue ;;
    esac
    printf '%s\n' "$ddl" >>"$output"
  done < <(
    awk -F'\t' -v a="$action" -v l="$local_db" -v r="$remote_db" '
      BEGIN {OFS=sprintf("%c",28)}
      $1==a && $2==l && $3==r &&
        ($4=="SCHEMA" || $4=="EXTENSION" || $4=="TYPE" ||
         $4=="COLUMN" || $4=="FUNCTION" || $4=="CONSTRAINT" ||
         $4=="INDEX") {
        if ($4!="FUNCTION") priority=0
        else if ($6=="columnar_insert_only_parents") priority=1
        else if ($6=="columnar_healthcheck") priority=2
        else priority=3
        print $1,$2,$3,$4,$5,$6,$7,priority
      }
    ' "$impact" | LC_ALL=C sort -t $'\034' -k8,8n -k6,6
  )
}

process_database() {
  local action="$1" source_side="$2" target_side="$3"
  local local_db="$4" remote_db="$5" source_db target_db sql functions archive
  local event_trigger=""
  local relations=() relation
  source_db="$local_db"; target_db="$remote_db"
  if [[ "$source_side" == "remote" ]]; then
    source_db="$remote_db"; target_db="$local_db"
  fi
  if [[ "$local_db" == "llm_gateway" && "$target_side" == "remote" ]]; then
    event_trigger="$LLM_SCHEMA_EVENT_TRIGGER"
  fi
  [[ "$local_db" == "llm_gateway" && ! -r "$llm_ssot_allowlist" ]] && {
    printf '%s\t%s\t%s\tSKIPPED_LLM_SSOT\n' \
      "$action" "$local_db" "$remote_db" >>"$work_dir/schema-additive.tsv"
    return
  }
  sql="$work_dir/$action-$source_db-foundation.sql"
  functions="$work_dir/$action-$source_db-functions.sql"
  archive="$work_dir/$action-$source_db-relations.dump"
  foundation_sql "$action" "$source_side" "$source_db" \
    "$local_db" "$remote_db" foundation "$sql"
  foundation_sql "$action" "$source_side" "$source_db" \
    "$local_db" "$remote_db" functions "$functions"
  # shellcheck disable=SC2094
  while IFS=$'\t' read -r row_action row_local row_remote kind schema object _rest; do
    [[ "$row_action" == "$action" && "$kind" == "RELATION" ]] || continue
    [[ "$row_local" == "$local_db" && "$row_remote" == "$remote_db" ]] || continue
    llm_object_allowed "$local_db" "$schema" "$object" || continue
    relation="$schema.$object"
    relations+=("$relation")
  done <"$impact"

  if [[ "$dry_run" == true ]]; then
    printf '%s\t%s\t%s\tDRY_RUN_%s_RELATIONS\n' \
      "$action" "$local_db" "$remote_db" "${#relations[@]}" \
      >>"$work_dir/schema-additive.tsv"
    return
  fi
  [[ $(wc -l <"$sql" | tr -d ' ') -le 2 ]] ||
    pg_instance_apply_sql_file "$target_side" "$target_db" "$sql"
  if (( ${#relations[@]} > 0 )); then
    pg_instance_schema_dump_tables "$source_side" "$source_db" \
      "$archive" "${relations[@]}"
    pg_instance_restore_schema "$target_side" "$target_db" "$archive" \
      pre-data "$event_trigger"
  fi
  [[ $(wc -l <"$functions" | tr -d ' ') -le 2 ]] ||
    pg_instance_apply_sql_file "$target_side" "$target_db" "$functions"
  if (( ${#relations[@]} > 0 )); then
    pg_instance_restore_schema "$target_side" "$target_db" "$archive" post-data
  fi
  printf '%s\t%s\t%s\tAPPLIED_%s_RELATIONS\n' \
    "$action" "$local_db" "$remote_db" "${#relations[@]}" \
    >>"$work_dir/schema-additive.tsv"
}

printf 'action\tlocal_database\tremote_database\tstatus\n' \
  >"$work_dir/schema-additive.tsv"
while IFS=$'\t' read -r classification local_db remote_db _mode; do
  [[ "$classification" == \#* || "$classification" == "classification" ]] && continue
  [[ "$classification" == "COMMON" || "$classification" == "ALIAS" ]] || continue
  [[ -z "$database_filter" || "$local_db" == "$database_filter" ]] || continue
  process_database ADD_REMOTE local remote "$local_db" "$remote_db"
  process_database ADD_LOCAL remote local "$local_db" "$remote_db"
done <"$manifest"
printf 'schema_additive_report=%s\n' "$work_dir/schema-additive.tsv"
