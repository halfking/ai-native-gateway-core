#!/usr/bin/env bash
# =============================================================================
# deploy-pre-gate.sh — D3 部署前置测试门禁
# =============================================================================
# 定位：蓝绿部署的**前置检查**，在任何破坏性动作（停容器/切流）之前运行。
#      失败时阻止部署继续，保持当前服务状态不变。
#
# 检查项（W1 设计）：
#   G1. sqlguard 迁移校验（TestStartupMigrations* / TestMigrationRegistry*）
#   G2. Maestro 核心 5-8 个 flow 快速回归（01/03/06/09 必跑）
#   G3. 前端产物新鲜度检查（web/dist 存在且mtime<10min）
#
# 用法：
#   bash scripts/deploy-pre-gate.sh                      # 本地检查
#   bash scripts/deploy-pre-gate.sh --maestro-only       # 仅Maestro回归
#   bash scripts/deploy-pre-gate.sh --skip-maestro       # 跳过Maestro（sqlguard+产物）
#   bash scripts/deploy-pre-gate.sh --via <host>         # SSH到目标机检查
#
# 环境变量：
#   GATE_MAESTRO_FLOWS   Maestro flow列表（空格分隔），默认"01 03 06 09"
#   GATE_MAESTRO_TIMEOUT Maestro超时（秒），默认120
#   GATE_SKIP_MAESTRO    跳过Maestro（1=跳过），默认0
#   BASE_URL             Maestro测试目标URL，默认http://localhost:8780
#   ADMIN_USER           Maestro登录用户
#   ADMIN_PASSWORD       Maestro登录密码
#
# 返回值：
#   0 = 所有检查通过，可继续部署
#   1 = 检查失败，阻止部署
#   2 = 参数错误
#
# =============================================================================
set -euo pipefail

# ── 参数与环境变量 ────────────────────────────────────────────────────────
GATE_MODE="full"                    # full / maestro-only / no-maestro
GATE_VIA_HOST=""                    # SSH目标机
GATE_MAESTRO_FLOWS="${GATE_MAESTRO_FLOWS:-01 03 06 09}"
GATE_MAESTRO_TIMEOUT="${GATE_MAESTRO_TIMEOUT:-120}"
GATE_SKIP_MAESTRO="${GATE_SKIP_MAESTRO:-0}"
GATE_BASE_URL="${BASE_URL:-http://localhost:8780}"
GATE_ADMIN_USER="${ADMIN_USER:-admin}"
GATE_ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
GATE_DIST_MAX_AGE_MIN="${GATE_DIST_MAX_AGE_MIN:-10}"

# ── 解析参数 ──────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --maestro-only)
      GATE_MODE="maestro-only"
      shift
      ;;
    --skip-maestro)
      GATE_SKIP_MAESTRO=1
      shift
      ;;
    --via)
      GATE_VIA_HOST="$2"
      shift 2
      ;;
    --help|-h)
      sed -n '2,/^# ====/p' "$0" | sed 's/^# //; s/^#//'
      exit 0
      ;;
    *)
      echo "错误：未知参数 $1" >&2
      echo "用法：bash $0 [--maestro-only|--skip-maestro] [--via HOST]" >&2
      exit 2
      ;;
  esac
done

# ── 工具函数 ──────────────────────────────────────────────────────────────
log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [GATE] $*" >&2
}

err() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [GATE] ❌ $*" >&2
}

ok() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [GATE] ✅ $*" >&2
}

# ── G1: sqlguard 迁移校验 ─────────────────────────────────────────────────
run_sqlguard() {
  log "G1: sqlguard 迁移校验..."
  
  if ! command -v go &>/dev/null; then
    err "G1: go 命令不存在，跳过 sqlguard"
    return 1
  fi
  
  # 运行核心三个测试
  local tests=(
    "TestStartupMigrationsStayInEveryDeliveryChannel"
    "TestStartupManifestEmbeddata"
    "TestMigrationRegistryStaysContinuous"
  )
  
  local failed=0
  for test in "${tests[@]}"; do
    log "  运行 $test..."
    if ! go test -timeout 30s ./startup/sqlguard -run "^${test}$" -v 2>&1 | tee /tmp/gate-sqlguard-${test}.log; then
      err "  $test 失败"
      failed=1
    else
      ok "  $test 通过"
    fi
  done
  
  if [[ $failed -eq 1 ]]; then
    err "G1: sqlguard 检查失败"
    return 1
  fi
  
  ok "G1: sqlguard 检查通过"
  return 0
}

# ── G2: Maestro 快速回归 ──────────────────────────────────────────────────
run_maestro() {
  log "G2: Maestro 快速回归（flows: $GATE_MAESTRO_FLOWS）..."
  
  if ! command -v maestro &>/dev/null; then
    err "G2: maestro 命令不存在，跳过"
    return 1
  fi
  
  # 检查必需环境变量
  if [[ -z "$GATE_ADMIN_PASSWORD" ]]; then
    err "G2: ADMIN_PASSWORD 未设置，无法运行 Maestro"
    return 1
  fi
  
  # 生成临时 env 文件
  local env_file="/tmp/gate-maestro-env-$$.yaml"
  cat > "$env_file" <<EOF
appId: ai.native.llm-gateway
env:
  BASE_URL: "$GATE_BASE_URL"
  ADMIN_USER: "$GATE_ADMIN_USER"
  ADMIN_PASSWORD: "$GATE_ADMIN_PASSWORD"
EOF
  
  local failed=0
  for flow in $GATE_MAESTRO_FLOWS; do
    local flow_file="maestro/flows/${flow}-*.yaml"
    if ! ls $flow_file &>/dev/null; then
      err "  flow $flow 文件不存在：$flow_file"
      failed=1
      continue
    fi
    
    log "  运行 flow $flow..."
    local log_file="/tmp/gate-maestro-flow-${flow}.log"
    
    # 使用 timeout 防止挂起
    if timeout ${GATE_MAESTRO_TIMEOUT}s maestro test \
      --env "$env_file" \
      $flow_file \
      > "$log_file" 2>&1; then
      ok "  flow $flow 通过"
    else
      err "  flow $flow 失败（日志：$log_file）"
      tail -20 "$log_file" >&2
      failed=1
    fi
  done
  
  rm -f "$env_file"
  
  if [[ $failed -eq 1 ]]; then
    err "G2: Maestro 回归失败"
    return 1
  fi
  
  ok "G2: Maestro 回归通过"
  return 0
}

# ── G3: 前端产物新鲜度检查 ───────────────────────────────────────────────
check_dist_freshness() {
  log "G3: 前端产物新鲜度检查..."
  
  local dist_dir="web/dist"
  if [[ ! -d "$dist_dir" ]]; then
    err "G3: $dist_dir 不存在"
    return 1
  fi
  
  # 检查 index.html mtime
  local index_html="$dist_dir/index.html"
  if [[ ! -f "$index_html" ]]; then
    err "G3: $index_html 不存在"
    return 1
  fi
  
  local mtime
  mtime=$(stat -f %m "$index_html" 2>/dev/null || stat -c %Y "$index_html" 2>/dev/null)
  local now
  now=$(date +%s)
  local age_sec=$((now - mtime))
  local age_min=$((age_sec / 60))
  
  if [[ $age_min -gt $GATE_DIST_MAX_AGE_MIN ]]; then
    err "G3: 前端产物过期（age=${age_min}min > ${GATE_DIST_MAX_AGE_MIN}min）"
    err "    提示：运行 npm run build 重新构建"
    return 1
  fi
  
  ok "G3: 前端产物新鲜（age=${age_min}min）"
  return 0
}

# ── 主流程 ────────────────────────────────────────────────────────────────
main() {
  log "开始部署前置测试门禁"
  log "模式：$GATE_MODE"
  
  local failed=0
  
  case "$GATE_MODE" in
    maestro-only)
      run_maestro || failed=1
      ;;
    *)
      # full 模式
      if [[ "$GATE_MODE" != "no-maestro" ]] && [[ "$GATE_SKIP_MAESTRO" -eq 0 ]]; then
        check_dist_freshness || failed=1
        run_sqlguard || failed=1
        run_maestro || failed=1
      else
        check_dist_freshness || failed=1
        run_sqlguard || failed=1
      fi
      ;;
  esac
  
  if [[ $failed -eq 1 ]]; then
    err "==============================================="
    err "部署前置测试门禁失败，阻止部署"
    err "==============================================="
    return 1
  fi
  
  ok "==============================================="
  ok "部署前置测试门禁全部通过"
  ok "==============================================="
  return 0
}

# ── 远程执行支持 ──────────────────────────────────────────────────────────
if [[ -n "$GATE_VIA_HOST" ]]; then
  log "通过 SSH 在 $GATE_VIA_HOST 上执行门禁检查"
  # 将脚本和环境变量传递到远端
  ssh "$GATE_VIA_HOST" "bash -s" < "$0" -- "${@}"
  exit $?
fi

# ── 执行 ──────────────────────────────────────────────────────────────────
main
exit $?
