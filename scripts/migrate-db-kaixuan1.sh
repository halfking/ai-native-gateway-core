#!/bin/bash
# scripts/migrate-db-kaixuan1.sh — kaixuan-1 K3s PG migration
#
# 通过 kubectl exec postgresql-0 跑 sql/migrations/startup/*.sql
# 自动跳过已应用的 migration
#
# 用法:
#   ./scripts/migrate-db-kaixuan1.sh           # 应用所有未跑迁移
#   ./scripts/migrate-db-kaixuan1.sh --list   # 列出待跑迁移
#   ./scripts/migrate-db-kaixuan1.sh --dry-run # 只显示计划

set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPTS_DIR/.." && pwd)"
MIGRATION_DIR="$PROJECT_ROOT/sql/migrations/startup"

KUBECONFIG_PATH="${KUBECONFIG_PATH:-${HOME}/.kube/kaixuan-1-config}"
K8S_NAMESPACE="${K8S_NAMESPACE:-pms-test}"
PG_POD="${PG_POD:-postgresql-0}"
PG_USER="${PG_USER:-postgres}"
PG_DB="${PG_DB:-llm_gateway}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓${NC} $*"; }
info() { echo -e "${YELLOW}▶${NC} $*"; }
err()  { echo -e "${RED}✗${NC} $*" >&2; }
phase() { echo -e "\n${BLUE}═══════ $* ═══════${NC}"; }

DRY_RUN=false
LIST_ONLY=false
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=true
[[ "${1:-}" == "--list" ]] && LIST_ONLY=true

phase "kaixuan-1 K3s PG Migration"
echo "  kubeconfig = $KUBECONFIG_PATH"
echo "  namespace  = $K8S_NAMESPACE"
echo "  PG pod     = $PG_POD"
echo "  DB         = $PG_DB"

# ── 前置检查 ──────────────────────────────────────────────────
if [[ ! -f "$KUBECONFIG_PATH" ]]; then
  err "kubeconfig 不存在: $KUBECONFIG_PATH"
  echo "  请配置: scp <kaixuan-1>:.kube/kaixuan-1-config $KUBECONFIG_PATH"
  exit 1
fi
ok "kubeconfig present"

if ! kubectl --kubeconfig="$KUBECONFIG_PATH" -n "$K8S_NAMESPACE" get pod "$PG_POD" -o name &>/dev/null; then
  err "Pod $PG_POD 不在 namespace $K8S_NAMESPACE"
  kubectl --kubeconfig="$KUBECONFIG_PATH" -n "$K8S_NAMESPACE" get pods | head -10
  exit 1
fi
ok "PG pod reachable"

# ── 读已应用的迁移 ──────────────────────────────────────────
APPLIED=$(kubectl --kubeconfig="$KUBECONFIG_PATH" -n "$K8S_NAMESPACE" exec "$PG_POD" -- \
  psql -U "$PG_USER" -d "$PG_DB" -tA -c \
  "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 100" 2>/dev/null || echo "")
info "已应用 migration: $(echo $APPLIED | tr '\n' ' ')"

# ── 列出待跑迁移 ──────────────────────────────────────────
phase "扫描待跑 migration"
PENDING=()
for f in "$MIGRATION_DIR"/*.sql; do
  [[ "$(basename "$f")" == *.down.sql ]] && continue
  # 从文件名提取版本号 (开头 3 位数字)
  ver=$(basename "$f" | grep -oE '^[0-9]{3}' || echo "")
  if [[ -z "$ver" ]]; then continue; fi
  # 检查是否已应用
  if echo "$APPLIED" | grep -qE "^${ver}$|^${ver}\s"; then
    continue
  fi
  PENDING+=("$f")
  if $LIST_ONLY; then
    echo "  [pending] $(basename $f)"
  fi
done

if $LIST_ONLY; then
  ok "共 ${#PENDING[@]} 个待跑"
  exit 0
fi

if [[ ${#PENDING[@]} -eq 0 ]]; then
  ok "无待跑 migration"
  exit 0
fi

info "待跑 migration: ${#PENDING[@]} 个"
for f in "${PENDING[@]}"; do
  echo "  - $(basename $f)"
done

if $DRY_RUN; then
  warn "[dry-run] 不实际执行"
  exit 0
fi

# ── 确认 ──────────────────────────────────────────
echo
read -rp "确认应用 ${#PENDING[@]} 个 migration? (yes/no): " confirm
[[ "$confirm" == "yes" ]] || { info "已取消"; exit 0; }

# ── 应用 ──────────────────────────────────────────
phase "应用 migration"
APPLIED_COUNT=0
FAILED_COUNT=0
for f in "${PENDING[@]}"; do
  name=$(basename "$f")
  info "应用 $name..."
  if kubectl --kubeconfig="$KUBECONFIG_PATH" -n "$K8S_NAMESPACE" exec -i "$PG_POD" -- \
       psql -v ON_ERROR_STOP=1 -U "$PG_USER" -d "$PG_DB" < "$f" 2>&1 | tail -5; then
    ok "  ✓ $name"
    APPLIED_COUNT=$((APPLIED_COUNT + 1))
  else
    err "  ✗ $name 失败"
    FAILED_COUNT=$((FAILED_COUNT + 1))
  fi
done

# ── 验证关键表 ──────────────────────────────────────────
phase "验证关键表存在"
for table in model_name_mapping intent_classifier_config prompt_injection_rules output_compliance_policies session_intent_evolution; do
  if kubectl --kubeconfig="$KUBECONFIG_PATH" -n "$K8S_NAMESPACE" exec "$PG_POD" -- \
       psql -U "$PG_USER" -d "$PG_DB" -tA -c "SELECT to_regclass('public.$table')" 2>/dev/null | grep -q "$table"; then
    ok "  $table ✓"
  else
    warn "  $table ✗ (可能未应用相关 migration)"
  fi
done

phase "完成"
echo "  applied: $APPLIED_COUNT"
echo "  failed:  $FAILED_COUNT"
[[ $FAILED_COUNT -gt 0 ]] && exit 1 || exit 0
