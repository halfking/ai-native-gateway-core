#!/usr/bin/env bash
# =====================================================================
# scripts/ci-migration-checksum-verify.sh — CI 级迁移字节冻结检测
# =====================================================================
# 用途：比对生产账本（llm_gateway_migration_checksums）的 sha256 与仓内文件字节，
#       发现「已应用迁移被就地改写」⇒ 在合并/发版前报出，而不是部署期才 fail-closed。
#
# 用法：
#   bash scripts/ci-migration-checksum-verify.sh           # 默认检查 252（245/154 共用）
#   bash scripts/ci-migration-checksum-verify.sh --env 211 # 检查 211
#
# 退出码：
#   0   全部一致（或账本未初始化 ⇒ 首次部署）
#   1   检测到字节漂移（已应用迁移被就地改写）
#   64  参数错误 / 环境不可达
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认环境：252（245 与 154 共用）
ENV="${CI_CHECK_ENV:-252}"

usage() {
  cat <<'EOF'
用法:
  bash scripts/ci-migration-checksum-verify.sh           # 默认检查 252
  bash scripts/ci-migration-checksum-verify.sh --env 211 # 检查 211
选项:
  --env ENV     环境名（211 | 252），默认 252
  -h, --help    打印本用法
退出码:
  0   全部一致 / 账本未初始化
  1   检测到字节漂移
  64  参数错误 / 环境不可达
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env)
      [[ $# -ge 2 && -n "$2" ]] || { echo "--env requires a value" >&2; exit 64; }
      ENV="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 64 ;;
  esac
done

log() { printf '[ci-checksum] %s\n' "$*" >&2; }
fail() { printf '[ci-checksum] FATAL: %s\n' "$*" >&2; exit 64; }

# ── 环境配置 ────────────────────────────────────────────────────────
case "$ENV" in
  211)
    SSH_HOST="${LLM_GATEWAY_211_SSH:-root@llm.itestu.cn}"
    SSH_PORT="${LLM_GATEWAY_211_SSH_PORT:-22}"
    DB_CONTAINER="${DB_CONTAINER_211:-goacos-pg17}"
    DB_USER="${DB_USER_211:-llm_gateway}"
    DB_NAME="${DB_NAME_211:-llm_gateway}"
    ;;
  252)
    SSH_HOST="${LLM_GATEWAY_252_SSH:-root@115.29.212.252}"
    SSH_PORT="${LLM_GATEWAY_252_SSH_PORT:-25022}"
    DB_CONTAINER="${DB_CONTAINER_252:-pg-252-pg17}"
    DB_USER="${DB_USER_252:-llm_gateway}"
    DB_NAME="${DB_NAME_252:-llm_gateway}"
    ;;
  *)
    fail "unknown environment: $ENV (支持 211 | 252)"
    ;;
esac

SSH_KEY="${SSH_KEY_FILE:-$HOME/.ssh/id_ed25519}"
[[ -f "$SSH_KEY" ]] || fail "SSH key not found: $SSH_KEY"

SSH_OPTS=(-i "$SSH_KEY" -p "$SSH_PORT" -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)
SSH=(ssh "${SSH_OPTS[@]}" "$SSH_HOST")

remote_psql() {
  local sql=$1
  "${SSH[@]}" "docker exec -i '$DB_CONTAINER' psql -X -v ON_ERROR_STOP=1 -A -t -U '$DB_USER' -d '$DB_NAME'" <<<"$sql" 2>/dev/null
}

# ── 检查账本是否存在 ─────────────────────────────────────────────────
log "检查环境 $ENV（$SSH_HOST）..."
ledger_exists=$(remote_psql "SELECT CASE WHEN to_regclass('public.llm_gateway_migration_checksums') IS NULL THEN 'no' ELSE 'yes' END" || echo "error")

if [[ "$ledger_exists" == "error" ]]; then
  fail "无法连接环境 $ENV（$SSH_HOST:$SSH_PORT）或数据库 $DB_NAME"
fi

if [[ "$ledger_exists" == "no" ]]; then
  log "★ 账本 llm_gateway_migration_checksums 不存在 ⇒ 首次部署或未初始化 ⇒ 跳过检查（退出码 0）"
  exit 0
fi

# ── 拉取账本 ────────────────────────────────────────────────────────
log "拉取账本..."
ledger=$(remote_psql "SELECT version, migration_name, checksum FROM public.llm_gateway_migration_checksums ORDER BY version" || echo "")

if [[ -z "$ledger" ]]; then
  log "★ 账本为空 ⇒ 无已应用迁移 ⇒ 跳过检查（退出码 0）"
  exit 0
fi

ledger_count=$(echo "$ledger" | wc -l | tr -d ' ')
log "账本有 $ledger_count 条记录"

# ── 逐条比对 ────────────────────────────────────────────────────────
drift_count=0
missing_count=0
match_count=0

while IFS='|' read -r version migration_name db_checksum; do
  [[ -n "$version" ]] || continue
  
  # 查找仓内文件（startup / applied 两处都找）
  file=""
  for candidate in \
    "sql/migrations/startup/${migration_name}" \
    "sql/migrations/applied/${migration_name}" \
    "installer/cmd/llm-gw-installer/embeddata/startup/${migration_name}" \
    "installer/cmd/llm-gw-installer/embeddata/applied/${migration_name}"; do
    if [[ -f "$REPO_ROOT/$candidate" ]]; then
      file="$REPO_ROOT/$candidate"
      break
    fi
  done
  
  if [[ -z "$file" ]]; then
    log "  [$version] $migration_name: 文件不存在（可能是远端独有的老迁移）"
    missing_count=$((missing_count + 1))
    continue
  fi
  
  # 计算仓内文件的 sha256
  if command -v sha256sum >/dev/null 2>&1; then
    repo_checksum=$(sha256sum "$file" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    repo_checksum=$(shasum -a 256 "$file" | awk '{print $1}')
  else
    fail "sha256sum / shasum not found"
  fi
  
  # 比对
  if [[ "$repo_checksum" == "$db_checksum" ]]; then
    match_count=$((match_count + 1))
  else
    log "  [$version] $migration_name: ★ 字节漂移 ★"
    log "    账本 sha256: $db_checksum"
    log "    仓内 sha256: $repo_checksum"
    drift_count=$((drift_count + 1))
  fi
done <<< "$ledger"

# ── 汇总 ────────────────────────────────────────────────────────────
log "汇总：一致=$match_count / 字节漂移=$drift_count / 文件缺失=$missing_count（总计 $ledger_count 条）"

if [[ $drift_count -gt 0 ]]; then
  log "★ 检测到 $drift_count 条已应用迁移被就地改写 ⇒ 退出码 1 ★"
  log "  修复方法："
  log "    1. 如果改写是修复 bug（如 966 补 setval）："
  log "       - 先在目标环境手工执行修复 SQL（幂等）"
  log "       - 更新账本校验和：bash scripts/repair-252-migration-ledger.sh --apply"
  log "    2. 如果改写是语义变更（改 DDL / 改数据）："
  log "       - 还原迁移文件到原始版本"
  log "       - 把语义变更写成新的 forward-only 迁移"
  log "    3. 绝对不要「绕过这个检测」或「直接改账本」——那会让账本与实际不一致"
  exit 1
fi

log "★ 全部一致 ⇒ 退出码 0 ★"
exit 0
