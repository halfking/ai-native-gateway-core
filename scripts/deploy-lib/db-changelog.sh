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

_deploy_applied_migration_ids() {
  local ssh_cmd=$1 env_file=$2
  local remote_psql
  remote_psql=$(_deploy_remote_psql_script "$env_file")
  "$ssh_cmd" "${remote_psql}
_psql -tAc \"SELECT version FROM schema_migrations WHERE version ~ '^[0-9]+$' ORDER BY version::int\"" \
    2>/dev/null | grep -E '^[0-9]+$' || true
}

_deploy_pending_startup_migrations() {
  local ssh_cmd=$1 env_file=$2
  local repo_root f base ver line remote_psql
  local -a applied_ids=()
  declare -A applied=()

  repo_root=$(_db_changelog_repo_root)
  remote_psql=$(_deploy_remote_psql_script "$env_file")
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    applied_ids+=("$line")
    applied["$((10#$line))"]=1
  done < <(_deploy_applied_migration_ids "$ssh_cmd" "$env_file")
  _db_log "schema_migrations 已记录 ${#applied_ids[@]} 个数字迁移（按缺号逐个检查）"

  cd "$repo_root"
  for f in sql/migrations/startup/[0-9]*.sql; do
    [[ -f "$f" ]] || continue
    base=$(basename "$f")
    [[ "$base" == *.down.sql ]] && continue
    [[ "$base" == *.skip ]] && continue
    [[ "$base" == *.bak.skip ]] && continue
    ver=$(echo "$base" | grep -oE '^[0-9]+' || true)
    [[ -n "$ver" ]] || continue
    # 仅三位编号 startup 迁移（跳过 2026-07-13-*.sql 等）
    [[ "$base" =~ ^[0-9]{3}_ ]] || continue
    if head -15 "$f" | grep -qiE 'SUPERSEDED|superceded|DEPRECATED'; then
      continue
    fi
    if [[ -z "${applied[$((10#$ver))]+x}" ]]; then
      printf '%s\n' "$f"
    fi
  done
}

deploy_apply_pending_migrations() {
  local ssh_cmd=$1 env_file=$2 target=$3 seq=$4 git_sha=$5
  local repo_root remote_dir applied_count=0 remote_psql
  local -a pending=() applied_files=()

  repo_root=$(_db_changelog_repo_root)
  while IFS= read -r line; do
    [[ -n "$line" ]] && pending+=("$line")
  done < <(_deploy_pending_startup_migrations "$ssh_cmd" "$env_file")

  if [[ ${#pending[@]} -eq 0 ]]; then
    _db_log "无 pending startup 迁移"
    return 0
  fi

  _db_log "切换前应用 ${#pending[@]} 个 pending 迁移..."
  remote_dir="/tmp/llm-gateway-migrations-$(date +%s)"
  "$ssh_cmd" "mkdir -p '$remote_dir'"

  local f base
  for f in "${pending[@]}"; do
    base=$(basename "$f")
    tar czf - -C "$repo_root" "$f" | "$ssh_cmd" "tar xzf - -C /tmp && mv '/tmp/$f' '$remote_dir/$base'"
  done

  for f in "${pending[@]}"; do
    local ver desc
    base=$(basename "$f")
    ver=$(echo "$base" | grep -oE '^[0-9]+')
    desc=$(echo "$base" | sed 's/^[0-9]*_//;s/.sql$//')
    _db_log "  → $base"
    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -f '$remote_dir/$base' 2>/tmp/_mig_err_${ver}.log"; then
      if grep -qiE 'already exists|duplicate key|relation .* already exists' "/tmp/_mig_err_${ver}.log" 2>/dev/null; then
        _db_warn "  ⊘ $base idempotent"
      else
        _db_err "  ✗ $base:"
        tail -15 "/tmp/_mig_err_${ver}.log" >&2 || true
        "$ssh_cmd" "rm -rf '$remote_dir'" || true
        return 1
      fi
    else
      applied_count=$((applied_count + 1))
    fi

    if ! _deploy_verify_ssh "$ssh_cmd" "${remote_psql}
_psql -v ON_ERROR_STOP=1 -c \"BEGIN; SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:schema_migrations', 0)); INSERT INTO schema_migrations (version, description) SELECT '$ver', '$desc' WHERE NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '$ver'); COMMIT;\" >/dev/null"; then
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
    applied_files+=("$base")
  done

  "$ssh_cmd" "rm -rf '$remote_dir'" || true
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
    echo "| Migration | File |"
    echo "|-----------|------|"
    local base ver
    for base in "${files[@]}"; do
      ver=$(echo "$base" | grep -oE '^[0-9]+')
      echo "| $ver | \`$base\` |"
    done
    echo ""
  } >>"$DB_CHANGELOG_FILE"

  _db_log "已写入 $DB_CHANGELOG_FILE"
}
