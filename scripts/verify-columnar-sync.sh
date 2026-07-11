#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# 对比 184 和本地的 columnar 配置一致性
#
# 检查项:
#   1. 扩展列表 (citus_columnar 版本)
#   2. columnar 表清单 (名称 + 数量)
#   3. columnar_drift_report() 合规性
#
# 用法:
#   ./scripts/verify-columnar-sync.sh
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 从 .env.local 加载 SSH 配置 ──
ENV_LOCAL="$ROOT_DIR/.env.local"
if [ -f "$ENV_LOCAL" ]; then set -a; . "$ENV_LOCAL"; set +a; fi

REMOTE_SSH_HOST="${REMOTE_SSH_HOST:-root@${INTERNAL_PUBLIC_IP:-47.97.111.154}}"  # 154 替代 184
REMOTE_SSH_PORT="${REMOTE_SSH_PORT:-${SSH_PORT_184:-25022}}"
REMOTE_SSH_IDENTITY="${REMOTE_SSH_IDENTITY:-${SSH_KEY_184_PATH:-$HOME/.ssh/56_id_rsa}}"
REMOTE_SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=15 -o IdentitiesOnly=yes -o BatchMode=yes"
REMOTE_NAMESPACE="${REMOTE_NAMESPACE:-pms-test}"
REMOTE_DEPLOYMENT="${REMOTE_DEPLOYMENT:-deployment/llm-gateway-pg}"
REMOTE_DB="${REMOTE_DB:-llm_gateway}"
REMOTE_DB_USER="${REMOTE_DB_USER:-llm_gateway}"

LOCAL_CONTAINER="${LOCAL_CONTAINER:-r112_postgres}"
LOCAL_DB="${LOCAL_DB:-llm_gateway}"
LOCAL_DB_USER="${LOCAL_DB_USER:-kxuser}"
LOCAL_DB_PASS="${LOCAL_DB_PASS:-kxpass}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
err()  { printf "${RED}✗ %s${NC}\n" "$*" >&2; }
ok()   { printf "${GREEN}✓ %s${NC}\n" "$*"; }
info() { printf "${YELLOW}▶ %s${NC}\n" "$*"; }

run_local_psql() {
  docker exec -e PGPASSWORD="$LOCAL_DB_PASS" "$LOCAL_CONTAINER" \
    psql -U "$LOCAL_DB_USER" -d "$LOCAL_DB" -tAc "$1" 2>/dev/null
}

run_remote_psql() {
  ssh $REMOTE_SSH_OPTS -p "$REMOTE_SSH_PORT" -i "$REMOTE_SSH_IDENTITY" "$REMOTE_SSH_HOST" \
    "kubectl -n $REMOTE_NAMESPACE exec $REMOTE_DEPLOYMENT -- psql -U $REMOTE_DB_USER -d $REMOTE_DB -tAc \"$1\"" 2>/dev/null
}

PASS=0; FAIL=0
check() {
  local name="$1" remote="$2" local_val="$3"
  if [ "$remote" = "$local_val" ]; then
    echo -e "  ${GREEN}✓${NC} $name  (两者一致: $remote)"
    PASS=$((PASS+1))
  else
    echo -e "  ${RED}✗${NC} $name  (184=$remote  本地=$local_val)"
    FAIL=$((FAIL+1))
  fi
}

info "对比 184 与本地 columnar 配置"
echo ""

# ── 1. 扩展版本 ──
echo "── 扩展 ──"
remote_ext=$(run_remote_psql "SELECT extname||'='||extversion FROM pg_extension WHERE extname LIKE '%citus%' ORDER BY extname;" | tr '\n' ',' | sed 's/,$//')
local_ext=$(run_local_psql "SELECT extname||'='||extversion FROM pg_extension WHERE extname LIKE '%citus%' ORDER BY extname;" | tr '\n' ',' | sed 's/,$//')
check "citus 扩展" "$remote_ext" "$local_ext"
echo ""

# ── 2. columnar 表数量 ──
echo "── columnar 表 ──"
remote_count=$(run_remote_psql "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_am am ON am.oid=c.relam WHERE n.nspname='public' AND c.relkind='r' AND am.amname='columnar';" | tr -d '[:space:]')
local_count=$(run_local_psql "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_am am ON am.oid=c.relam WHERE n.nspname='public' AND c.relkind='r' AND am.amname='columnar';" | tr -d '[:space:]')
check "columnar 表数量" "${remote_count:-0}" "${local_count:-0}"
echo ""

# ── 3. columnar 表清单差异 ──
echo "── columnar 表清单差异 ──"
remote_list=$(run_remote_psql "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_am am ON am.oid=c.relam WHERE n.nspname='public' AND c.relkind='r' AND am.amname='columnar' ORDER BY c.relname;" | sort)
local_list=$(run_local_psql "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_am am ON am.oid=c.relam WHERE n.nspname='public' AND c.relkind='r' AND am.amname='columnar' ORDER BY c.relname;" | sort)

# 只在 184 有的 (本地缺失)
only_remote=$(comm -23 <(echo "$remote_list") <(echo "$local_list") | grep -v '^$' || true)
# 只在本地有的 (184 没有)
only_local=$(comm -13 <(echo "$remote_list") <(echo "$local_list") | grep -v '^$' || true)

if [ -z "$only_remote" ] && [ -z "$only_local" ]; then
  ok "columnar 表清单完全一致"
  PASS=$((PASS+1))
else
  if [ -n "$only_remote" ]; then
    err "本地缺失的 columnar 表 (184 有):"
    echo "$only_remote" | sed 's/^/    /'
    FAIL=$((FAIL+1))
  fi
  if [ -n "$only_local" ]; then
    info "本地多出的 columnar 表 (184 没有, 可能是新分区):"
    echo "$only_local" | sed 's/^/    /'
  fi
fi
echo ""

# ── 4. drift report ──
echo "── columnar_drift_report() ──"
remote_drift=$(run_remote_psql "SELECT string_agg(parent_name||':noncompliant='||COALESCE(noncompliant_count,0), ', ') FROM columnar_drift_report() WHERE COALESCE(noncompliant_count,0) > 0;" | head -c 200)
local_drift=$(run_local_psql "SELECT string_agg(parent_name||':noncompliant='||COALESCE(noncompliant_count,0), ', ') FROM columnar_drift_report() WHERE COALESCE(noncompliant_count,0) > 0;" | head -c 200)

if [ -z "$remote_drift" ]; then remote_drift="(无违规)"; fi
if [ -z "$local_drift" ]; then local_drift="(无违规)"; fi
check "drift_report 非合规项" "$remote_drift" "$local_drift"
echo ""

# ── 总结 ──
if [ "$FAIL" = "0" ]; then
  ok "结果: $PASS pass, $FAIL fail — 本地与 184 columnar 配置一致"
  exit 0
else
  err "结果: $PASS pass, $FAIL fail — 存在差异"
  echo ""
  echo "修复: ./scripts/apply-columnar-local.sh"
  exit "$FAIL"
fi
