#!/usr/bin/env bash
# deploy-lib/db-changelog.sh — 部署前应用 pending 迁移并写入 docs/db-changelog.md
#
# 切换前在 252 PG 上应用 sql/migrations/startup/NNN_*.sql，避免 restart 时
# EnsureSchema 长时间阻塞。
#
set -euo pipefail

# 与 post-deploy-verify.sh 共用 SSH 调用约定（remote_ssh / ssh 函数名）
_deploy_verify_ssh() {
  local ssh_cmd=$1
  shift
  # shellcheck disable=SC2086
  $ssh_cmd "$@"
}

_db_changelog_repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd
}

: "${DB_CHANGELOG_FILE:=$(_db_changelog_repo_root)/docs/db-changelog.md}"

_db_log()  { echo -e "${DEPLOY_VERIFY_GREEN:-}[db]${DEPLOY_VERIFY_NC:-} $*" >&2; }
_db_warn() { echo -e "${DEPLOY_VERIFY_YELLOW:-}[db]${DEPLOY_VERIFY_NC:-} $*" >&2; }
_db_err()  { echo -e "${DEPLOY_VERIFY_RED:-}[db]${DEPLOY_VERIFY_NC:-} $*" >&2; }

# 远端 psql 包装（245 无 psql → docker postgres:17-alpine）
_deploy_remote_psql_script() {
  local env_file=$1
  cat <<REMOTE
set -euo pipefail
ENV='$env_file'
DB=\$(grep '^LLM_GATEWAY_DATABASE_URL=' "\$ENV" | cut -d= -f2-)
test -n "\$DB"
_psql() {
  if command -v psql >/dev/null 2>&1; then psql "\$DB" "\$@"
  else docker run --rm --network host postgres:17-alpine psql "\$DB" "\$@"
  fi
}

REMOTE
}

_db_migration_checksum() {
  local file=$1
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    sha256sum "$file" | awk '{print $1}'
  fi
}

# 2026-08-13 P0 fix (rule 11 §6 audit, round 3): extract POST_CONDITION
# assertions declared at the top of a migration file. Each line that
# matches `^[[:space:]]*--[[:space:]]*POST_CONDITION: <sql>` becomes one
# assertion; the SQL must return at least one row (typically a SELECT
# that returns 1 when the expected object exists). Migrations with no
# POST_CONDITION header are treated as having zero assertions — no
# extra SQL is run, so legacy migrations keep working without changes.
# See _db_run_postcondition for the execution semantics.
_db_extract_postcondition() {
  local file=$1
  # Use `|| true` to swallow grep's exit-1 on no-match (set -o pipefail
  # in the deploy lib would otherwise kill the caller). The actual
  # assertions are still parsed correctly when present.
  head -50 "$file" | { grep -E '^[[:space:]]*--[[:space:]]*POST_CONDITION:[[:space:]]*' || true; } \
    | sed -E 's/^[[:space:]]*--[[:space:]]*POST_CONDITION:[[:space:]]*//'
}

# _db_run_postcondition executes every POST_CONDITION assertion extracted
# from $file. Failures abort the deploy (return 1) so the symbol-link
# switch in deploy-seamless.sh is never reached — but the migration
# itself is NOT rolled back, since schema changes are already applied.
# The operator must investigate; the deploy log records which
# assertion failed and the suspected cause (PL/pgSQL DO block wrapping
# an EXECUTE that did not raise on failure, like the 2026-08-12 481
# partition regression).
_db_run_postcondition() {
  local file=$1 ssh_cmd=$2 env_file=$3
  local remote_psql
  remote_psql=$(_deploy_remote_psql_script "$env_file")

  local conditions
  conditions=$(_db_extract_postcondition "$file" || true)
  if [[ -z "$conditions" ]]; then
    return 0
  fi

  while IFS= read -r cond; do
    [[ -z "$cond" ]] && continue
    _db_log "    [assert] $cond"
    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"$cond\" 2>/dev/null" 2>/dev/null | grep -qE '^[1-9]' ; then
      _db_err "    ✗ POST_CONDITION FAILED: $cond"
      _db_err "      The migration SQL applied cleanly (no syntax / permission /"
      _db_err "      constraint errors) but the expected schema state is NOT present."
      _db_err "      This is the silent-fail pattern that masked the 481"
      _db_err "      partition regression on 2026-08-12."
      _db_err "      Likely causes:"
      _db_err "        - PL/pgSQL DO block wrapping EXECUTE that returns 0 rows"
      _db_err "          without RAISE EXCEPTION"
      _db_err "        - Migration idempotency check that saw 'already exists'"
      _db_err "          and skipped the actual CREATE"
      _db_err "      Deploy is being aborted BEFORE the symbol-link switch."
      return 1
    fi
    _db_log "    [assert] ✓"
  done <<<"$conditions"
}

_deploy_state_transitions_contract_gate() {
  local ssh_cmd=$1 env_file=$2
  local remote_psql result
  remote_psql=$(_deploy_remote_psql_script "$env_file")

  result=$(_deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT
  (SELECT count(*) FROM information_schema.columns
   WHERE table_schema = current_schema()
     AND table_name = 'request_state_transitions'
     AND column_name = 'tenant_id'
     AND is_nullable = 'NO') || '|' ||
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname = current_schema()
     AND tablename = 'request_state_transitions'
     AND indexname IN ('idx_state_transitions_tenant_request', 'uq_state_transitions_tenant_request_seq')) || '|' ||
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname = current_schema()
     AND tablename = 'request_state_transitions'
     AND indexname IN ('uq_state_transitions_request_seq', 'uq_state_transitions_legacy_request_seq'))\"" 2>/dev/null) || {
    _db_err "无法读取 request_state_transitions schema contract"
    return 1
  }
  result=$(printf '%s' "$result" | tr -d '[:space:]')

  local tenant_column tenant_index replay_index
  IFS='|' read -r tenant_column tenant_index replay_index <<<"$result"
  # contract fix 2026-08-19: V531 同时创建了 idx_state_transitions_tenant_request
  # 和 uq_state_transitions_tenant_request_seq 两个租户相关索引,以及
  # uq_state_transitions_legacy_request_seq 替代旧的 uq_state_transitions_request_seq。
  # 老 contract 严格要求各组正好 1 个索引,与 V531 后现状不符。
  # 新 contract: 每组至少 1 个索引存在即可。
  if [[ "$tenant_column" != "1" || "$tenant_index" -lt 1 || "$replay_index" -lt 1 ]]; then
    _db_err "request_state_transitions schema contract 不完整 (tenant_id_not_null=${tenant_column:-0}, tenant_index=${tenant_index:-0}, replay_index=${replay_index:-0})"
    _db_err "拒绝切换版本；请先应用前向修复 migration 并核验真实表结构"
    return 1
  fi
}

_deploy_migration_history_gate() {
  local ssh_cmd=$1 env_file=$2
  local remote_psql result
  remote_psql=$(_deploy_remote_psql_script "$env_file")

  result=$(_deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT
  (SELECT COALESCE(sum(n-1),0) FROM (SELECT count(*) n FROM schema_migrations GROUP BY version HAVING count(*) > 1) d) || '|' ||
  (SELECT count(*) FROM schema_migrations a JOIN schema_migrations b
   ON a.version=b.version AND a.ctid < b.ctid
   WHERE ROW(a.description,a.applied_at) IS DISTINCT FROM ROW(b.description,b.applied_at)) || '|' ||
  (SELECT count(*) FROM pg_constraint c JOIN pg_attribute a
   ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)
   WHERE c.conrelid='public.schema_migrations'::regclass
     AND c.contype IN ('p','u') AND a.attname='version') || '|' ||
  CASE WHEN to_regclass('public.llm_gateway_migration_checksums') IS NULL THEN 'missing' ELSE 'present' END\"" 2>/dev/null) || {
    _db_err "无法读取 schema_migrations migration gate 状态"
    return 1
  }
  result=$(printf '%s' "$result" | tr -d '[:space:]')
  local duplicate_rows inconsistent_pairs version_key ledger
  IFS='|' read -r duplicate_rows inconsistent_pairs version_key ledger <<<"$result"
  if [[ "$duplicate_rows" != "0" ]]; then
    _db_err "schema_migrations 存在 $duplicate_rows 条重复版本；先运行 scripts/repair-252-migration-ledger.sh --apply"
    return 1
  fi
  if [[ "$inconsistent_pairs" != "0" ]]; then
    _db_err "schema_migrations 重复行字段不一致，拒绝自动修复；需要人工审计"
    return 1
  fi
  if [[ "$version_key" != "1" ]]; then
    _db_err "schema_migrations.version 缺少唯一约束；先运行 migration ledger repair"
    return 1
  fi
  if [[ "$ledger" != "present" ]]; then
    _db_err "llm_gateway_migration_checksums 不存在；普通部署不隐式初始化，请先运行 migration ledger repair"
    return 1
  fi
}

_deploy_applied_migration_ids() {
  local ssh_cmd=$1 env_file=$2
  local remote_psql
  remote_psql=$(_deploy_remote_psql_script "$env_file")
  "$ssh_cmd" "${remote_psql}
_psql -tAc \"SELECT version FROM schema_migrations WHERE version ~ '^[0-9]+$' ORDER BY version::int\"" \
    2>/dev/null | grep -E '^[0-9]+$' || true
}

_deploy_max_applied_migration_id() {
  local ssh_cmd=$1 env_file=$2
  local remote_psql max
  remote_psql=$(_deploy_remote_psql_script "$env_file")
  max=$($ssh_cmd "${remote_psql}
_psql -tAc \"SELECT COALESCE(max(version::int), 0) FROM schema_migrations WHERE version ~ '^[0-9]+$'\"" \
    2>/dev/null | tr -d '[:space:]')
  max=${max:-0}
  echo "$max"
}

_deploy_pending_startup_migrations() {
  local ssh_cmd=$1 env_file=$2
  local repo_root f base ver line
  local ledger_reconcile_from=${DB_LEDGER_RECONCILE_FROM:-412}
  local -a applied_ids=()
  declare -A applied=()

  repo_root=$(_db_changelog_repo_root)
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    applied_ids+=("$line")
    applied["$((10#$line))"]=1
  done < <(_deploy_applied_migration_ids "$ssh_cmd" "$env_file")
  _db_log "schema_migrations 已记录 ${#applied_ids[@]} 个数字迁移（${ledger_reconcile_from}+逐版本核对）"

  cd "$repo_root"
  for f in sql/migrations/startup/[0-9]*.sql; do
    [[ -f "$f" ]] || continue
    base=$(basename "$f")
    [[ "$base" == *.down.sql ]] && continue
    [[ "$base" == *.skip ]] && continue
    [[ "$base" == *.bak.skip ]] && continue
    ver=$(echo "$base" | grep -oE '^[0-9]+' || true)
    [[ -n "$ver" ]] || continue
    [[ "$base" =~ ^[0-9]{3}_ ]] || continue
    if head -15 "$f" | grep -qiE 'SUPERSEDED|superceded|DEPRECATED'; then
      continue
    fi
    if (( 10#$ver >= ledger_reconcile_from )) && [[ -z "${applied[$((10#$ver))]+x}" ]]; then
      printf '%s\n' "$f"
    fi
  done
}

_deploy_validate_pending_migration_versions() {
  local -a files=("$@")
  local -A seen=()
  local -a duplicates=()
  local f base ver

  for f in "${files[@]}"; do
    base=$(basename "$f")
    ver=${base%%_*}
    if [[ -n "${seen[$ver]+x}" ]]; then
      duplicates+=("$ver:$base")
    else
      seen["$ver"]=$base
    fi
  done

  if [[ ${#duplicates[@]} -gt 0 ]]; then
    _db_err "pending startup migration version 冲突，拒绝部署；同一 version 不能在一次切换中应用多个文件："
    local entry version first
    for entry in "${duplicates[@]}"; do
      version=${entry%%:*}
      first=${seen[$version]}
      _db_err "  version $version: $first, ${entry#*:}"
    done
    return 1
  fi
}

_deploy_migration_checksum_reconcile() {
  local ssh_cmd=$1 env_file=$2 repo_root=$3
  local remote_psql row ver description f base checksum recorded_name recorded_checksum
  local ledger_reconcile_from=${DB_LEDGER_RECONCILE_FROM:-412}
  remote_psql=$(_deploy_remote_psql_script "$env_file")

  while IFS='|' read -r ver description; do
    [[ -n "$ver" ]] || continue
    (( 10#$ver >= ledger_reconcile_from )) || continue

    case "$ver:$description" in
      461:request_wal_hot_request_id_unique) description="request_wal_hot_unique_request_id" ;;
    esac

    local -a candidates=()
    local candidate suffix
    shopt -s nullglob
    for f in "$repo_root"/sql/migrations/startup/"${ver}"_*.sql; do
      base=$(basename "$f")
      [[ "$base" != *.down.sql && "$base" != *.skip && "$base" != *.bak.skip ]] || continue
      head -15 "$f" | grep -qiE 'SUPERSEDED|superceded|DEPRECATED|deprecated' && continue
      candidates+=("$f")
    done
    shopt -u nullglob

    f=""
    for candidate in "${candidates[@]}"; do
      base=$(basename "$candidate")
      suffix=${base#"${ver}"_}
      suffix=${suffix%.sql}
      if [[ "$suffix" == "$description" || "${base%.sql}" == "$description" ]]; then
        f=$candidate
        break
      fi
    done
    if [[ -z "$f" && ${#candidates[@]} -eq 1 ]]; then
      f=${candidates[0]}
    fi
    if [[ -z "$f" ]]; then
      _db_err "已应用迁移 version $ver description='$description' 无法解析为唯一 canonical 文件，拒绝部署"
      printf '  candidate: %s\n' "${candidates[@]}" >&2
      return 1
    fi

    base=$(basename "$f")
    checksum=$(_db_migration_checksum "$f")
    row=$(_deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT migration_name || '|' || checksum FROM llm_gateway_migration_checksums WHERE version = '$ver' LIMIT 1\"" 2>/dev/null) || {
      _db_err "无法读取 migration checksum ledger: $ver"
      return 1
    }
    row=$(printf '%s' "$row" | tr -d '[:space:]')
    if [[ -z "$row" ]]; then
      _db_err "已应用迁移 $ver:$base 缺少 checksum ledger；普通部署不补录，请先运行 migration ledger repair"
      return 1
    fi
    recorded_name=${row%%|*}
    recorded_checksum=${row#*|}
    if [[ "$recorded_name" != "$base" ]]; then
      _db_err "migration ledger 文件名不匹配，拒绝继续：$ver:recorded=$recorded_name:local=$base"
      return 1
    fi
    if [[ "$recorded_checksum" != "$checksum" ]]; then
      _db_err "已应用迁移 checksum 不匹配，拒绝继续：$ver:$base:recorded=$recorded_checksum:local=$checksum"
      return 1
    fi
  done < <(_deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT version || '|' || COALESCE(description,'') FROM schema_migrations WHERE version ~ '^[0-9]+$' AND version::int >= $ledger_reconcile_from ORDER BY version::int\"" 2>/dev/null)
}


_deploy_apply_ursm_repository_migrations() {
  local ssh_cmd=$1 env_file=$2
  local repo_root remote_psql remote_dir f base version checksum stored applied=0
  local -a files=(
    sql/migrations/080-ursm-key-migration-ledger.sql
    sql/migrations/081-ursm-key-migration-ledger-add-rollback-deadline.sql
  )

  repo_root=$(_db_changelog_repo_root)
  remote_psql=$(_deploy_remote_psql_script "$env_file")
  if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -c \"CREATE TABLE IF NOT EXISTS public.repository_schema_migrations (scope TEXT NOT NULL, version TEXT NOT NULL, migration_name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (scope, migration_name));\" >/dev/null"; then
    _db_err "无法初始化 repository_schema_migrations URSM scope ledger"
    return 1
  fi

  remote_dir="/tmp/llm-gateway-ursm-migrations-$(date +%s)"
  "$ssh_cmd" "mkdir -p '$remote_dir'"
  for f in "${files[@]}"; do
    [[ -f "$repo_root/$f" ]] || {
      _db_err "缺少 URSM root migration: $f"
      "$ssh_cmd" "rm -rf '$remote_dir'" || true
      return 1
    }
    base=$(basename "$f")
    checksum=$(_db_migration_checksum "$repo_root/$f")
    stored=$(_deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT checksum FROM public.repository_schema_migrations WHERE scope = 'ursm' AND migration_name = '$base'\"" 2>/dev/null) || {
      _db_err "无法读取 URSM root migration ledger: $base"
      "$ssh_cmd" "rm -rf '$remote_dir'" || true
      return 1
    }
    stored=$(printf '%s' "$stored" | tr -d '[:space:]')
    if [[ -n "$stored" ]]; then
      if [[ "$stored" != "$checksum" ]]; then
        _db_err "已应用 URSM root migration checksum 不匹配：$base"
        "$ssh_cmd" "rm -rf '$remote_dir'" || true
        return 1
      fi
      _db_log "  ⊘ URSM root migration 已应用: $base"
      continue
    fi

    tar czf - -C "$repo_root" "$f" | "$ssh_cmd" "tar xzf - -C /tmp && mv '/tmp/$f' '$remote_dir/$base'"
    _db_log "  → URSM root migration $base (sha256=${checksum:0:12})"
    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -f '$remote_dir/$base'"; then
      _db_err "  ✗ URSM root migration 失败: $base"
      "$ssh_cmd" "rm -rf '$remote_dir'" || true
      return 1
    fi
    version=${base%%-*}
    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -c \"INSERT INTO public.repository_schema_migrations (scope, version, migration_name, checksum) VALUES ('ursm', '$version', '$base', '$checksum');\" >/dev/null"; then
      _db_err "  ✗ URSM root migration 已执行但 ledger 写入失败: $base"
      "$ssh_cmd" "rm -rf '$remote_dir'" || true
      return 1
    fi
    applied=$((applied + 1))
  done
  "$ssh_cmd" "rm -rf '$remote_dir'" || true
  _db_log "✓ URSM root migration scope 完成 ($applied 新应用)"
}
deploy_apply_pending_migrations() {
  local ssh_cmd=$1 env_file=$2 target=$3 seq=$4 git_sha=$5
  local repo_root remote_dir applied_count=0 remote_psql
  local -a pending=() applied_files=()

  repo_root=$(_db_changelog_repo_root)
  _deploy_migration_history_gate "$ssh_cmd" "$env_file" || return 1
  remote_psql=$(_deploy_remote_psql_script "$env_file")
  while IFS= read -r line; do
    [[ -n "$line" ]] && pending+=("$line")
  done < <(_deploy_pending_startup_migrations "$ssh_cmd" "$env_file")
  if [[ ${#pending[@]} -gt 0 ]]; then
    _deploy_validate_pending_migration_versions "${pending[@]}" || return 1
  fi

  # The repair script is the only code path allowed to create this table.
  # Ordinary deployments must fail closed when the ledger is absent.
  if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT 1 FROM pg_class WHERE oid = 'public.llm_gateway_migration_checksums'::regclass\" | grep -qx '1'" >/dev/null; then
    _db_err "migration checksum ledger missing after history gate; run scripts/repair-252-migration-ledger.sh --apply"
    return 1
  fi

  local applied_csv
  applied_csv=$(_deploy_applied_migration_ids "$ssh_cmd" "$env_file" | paste -sd, -)
  if [[ -n "$applied_csv" ]]; then
    _deploy_migration_checksum_reconcile "$ssh_cmd" "$env_file" "$repo_root" || return 1
  fi
  _deploy_apply_ursm_repository_migrations "$ssh_cmd" "$env_file" || return 1

  if [[ ${#pending[@]} -eq 0 ]]; then
    _db_log "无 pending startup 迁移；checksum ledger 校验通过"
    return 0
  fi

  _db_log "切换前应用 ${#pending[@]} 个 pending 迁移..."
  remote_dir="/tmp/llm-gateway-migrations-$(date +%s)"
  "$ssh_cmd" "mkdir -p '$remote_dir'"

  local f base ver
  for f in "${pending[@]}"; do
    base=$(basename "$f")
    tar czf - -C "$repo_root" "$f" | "$ssh_cmd" "tar xzf - -C /tmp && mv '/tmp/$f' '$remote_dir/$base'"
  done

  for f in "${pending[@]}"; do
    local ver desc checksum
    base=$(basename "$f")
    ver=$(echo "$base" | grep -oE '^[0-9]+')
    desc=$(echo "$base" | sed 's/^[0-9]*_//;s/.sql$//')
    checksum=$(_db_migration_checksum "$f")
    _db_log "  → $base (sha256=${checksum:0:12})"
    # 2026-08-13 P0 fix (rule 11 §6 audit): capture psql stdout AND stderr
    # into the deploy log so DO blocks that silently swallow EXECUTE
    # failures surface. Previous invocation redirected stderr to
    # /tmp/_mig_err_*.log but left stdout to the ssh tunnel, where it
    # could vanish depending on caller pipe setup — which masked the
    # 481 partition-creation regression on 2026-08-12 (deploy log showed
    # BEGIN/DO/COMMIT, but the partition was never actually created).
    # Even with stdout capture alone, psql writes PL/pgSQL RAISE NOTICE
    # to STDERR (PG client convention), so the fix is to also redirect
    # stderr into the same captured stream via 2>&1. client_min_messages=NOTICE
    # guarantees NOTICE is emitted regardless of caller session settings,
    # and ON_ERROR_ROLLBACK=1 aborts cleanly on error.
    local psql_output psql_rc=0
    psql_output=$(_deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -v ON_ERROR_ROLLBACK=1 -v client_min_messages=NOTICE -f '$remote_dir/$base' 2>&1" 2>/dev/null) || psql_rc=$?
    if (( psql_rc != 0 )); then
      local migration_error
      migration_error=$psql_output
      if [[ -n "$migration_error" ]] && grep -qiE 'already exists|duplicate key|relation .* already exists' <<<"$migration_error"; then
        _db_warn "  ⊘ $base idempotent"
      elif [[ -z "$migration_error" ]]; then
        _db_err "  ✗ $base: psql 退出非零，但 psql stdout/stderr 为空。"
        "$ssh_cmd" "rm -rf '$remote_dir'" || true
        return 1
      else
        _db_err "  ✗ $base:"
        printf '%s\n' "$migration_error" | tail -15 >&2
        "$ssh_cmd" "rm -rf '$remote_dir'" || true
        return 1
      fi
    else
      # Forward psql stdout + stderr (NOTICE / BEGIN / DO / COMMIT /
      # CREATE TABLE) into deploy log so silent-fail migrations like the
      # 481 partition case become visible to the operator reading the
      # deploy output. PL/pgSQL RAISE NOTICE goes to stderr; BEGIN /
      # COMMIT echoes go to stdout; both are now captured.
      if [[ -n "$psql_output" ]]; then
        while IFS= read -r _db_psql_line; do
          _db_log "    | $_db_psql_line"
        done <<<"$psql_output"
      fi
      applied_count=$((applied_count + 1))
    fi

    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -c \"BEGIN; SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:schema_migrations', 0)); INSERT INTO schema_migrations (version, description) SELECT '$ver', '$desc' WHERE NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '$ver'); INSERT INTO llm_gateway_migration_checksums (version, migration_name, checksum) VALUES ('$ver', '$base', '$checksum') ON CONFLICT (version) DO UPDATE SET migration_name = EXCLUDED.migration_name, checksum = EXCLUDED.checksum; COMMIT;\" >/dev/null"; then
      _db_err "  ✗ $base: migration applied but schema_migrations ledger write failed"
      "$ssh_cmd" "rm -rf '$remote_dir'" || true
      return 1
    fi
    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT 1 FROM schema_migrations WHERE version = '$ver' LIMIT 1\" | grep -qx '1'"; then
      _db_err "  ✗ $base: schema_migrations ledger verification failed"
      "$ssh_cmd" "rm -rf '$remote_dir'" || true
      return 1
    fi
    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -tAc \"SELECT checksum FROM llm_gateway_migration_checksums WHERE version = '$ver' AND migration_name = '$base' AND checksum = '$checksum' LIMIT 1\" | grep -Fxq '$checksum'"; then
      _db_err "  ✗ $base: migration checksum ledger verification failed"
      "$ssh_cmd" "rm -rf '$remote_dir'" || true
      return 1
    fi
    applied_files+=("$base:$checksum")
    # 2026-08-13 P0 fix (rule 11 §6 audit): run POST_CONDITION assertions
    # extracted from the migration header to catch silent-fail patterns
    # like the 481 partition regression. Failures abort the deploy
    # BEFORE the symbol-link switch but do NOT roll back the migration.
    _db_run_postcondition "$f" "$ssh_cmd" "$env_file" || return 1
  done

  "$ssh_cmd" "rm -rf '$remote_dir'" || true
  _deploy_state_transitions_contract_gate "$ssh_cmd" "$env_file" || return 1
  _db_log "✓ 迁移完成 ($applied_count 新应用 / ${#pending[@]} 检查)"
  deploy_append_db_changelog "$target" "$seq" "$git_sha" "${applied_files[@]}"
}

deploy_append_db_changelog() {
  local target=$1 seq=$2 git_sha=$3
  shift 3
  local -a files=("$@")
  [[ ${#files[@]} -gt 0 ]] || return 0

  local ts
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mkdir -p "$(dirname "$DB_CHANGELOG_FILE")"
  if [[ ! -f "$DB_CHANGELOG_FILE" ]]; then
    cat >"$DB_CHANGELOG_FILE" <<'HDR'
# DB Changelog

部署时在 **切换前** 于 252 PG 应用的 `sql/migrations/startup/` 变更。
其它环境请按时间倒序手工同步，或使用 `deploy-seamless.sh` 自动 apply。

---

HDR
  fi

  {
    echo "## $ts — deploy $target build_seq $seq (${git_sha:0:8})"
    echo ""
    echo "| Migration | File | SHA-256 | Status |"
    echo "|-----------|------|---------|--------|"
    local entry base checksum ver
    for entry in "${files[@]}"; do
      base=${entry%%:*}
      checksum=${entry#*:}
      ver=$(echo "$base" | grep -oE '^[0-9]+')
      echo "| $ver | \`$base\` | \`${checksum}\` | applied+verified |"
    done
    echo ""
  } >>"$DB_CHANGELOG_FILE"

  _db_log "已写入 $DB_CHANGELOG_FILE"
}
