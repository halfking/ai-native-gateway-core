#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# LLM Gateway 本地部署验证脚本
#
# 目的: 自动化执行 L1-L4 验证层级，生成详细报告
# 用法:
#   ./scripts/verify-local-deployment.sh                # 完整验证
#   ./scripts/verify-local-deployment.sh --quick        # 快速验证（仅 L1-L2）
#   ./scripts/verify-local-deployment.sh --with-mock    # 包含 Mock Provider 测试
#   ./scripts/verify-local-deployment.sh --port 8782    # 指定端口
#
# 依赖:
#   - curl（HTTP 请求）
#   - jq（JSON 解析，可选）
#   - docker（容器检查）
#
# 输出:
#   - 控制台彩色输出
#   - 验证报告: /tmp/llm-gateway-verify-report-$(date +%Y%m%d-%H%M%S).md
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 配置 ──
GATEWAY_PORT="${LLM_GATEWAY_ACTIVE_PORT:-8782}"
GATEWAY_URL="http://127.0.0.1:${GATEWAY_PORT}"
TIMEOUT=10
QUICK_MODE=false
WITH_MOCK=false
REPORT_FILE="/tmp/llm-gateway-verify-report-$(date +%Y%m%d-%H%M%S).md"

# ── 颜色 ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

err()   { echo -e "${RED}✗ $*${NC}"; echo "✗ $*" >> "$REPORT_FILE" >&2; }
ok()    { echo -e "${GREEN}✓ $*${NC}"; echo "✓ $*" >> "$REPORT_FILE"; }
warn()  { echo -e "${YELLOW}⚠ $*${NC}"; echo "⚠ $*" >> "$REPORT_FILE"; }
info()  { echo -e "${CYAN}▶ $*${NC}"; echo "▶ $*" >> "$REPORT_FILE"; }
heading() { echo -e "\n${CYAN}━━━ $* ━━━${NC}"; echo -e "\n## $*\n" >> "$REPORT_FILE"; }

# ── 计数器 ──
PASS=0
FAIL=0
SKIP=0
TOTAL=0

pass() {
  PASS=$((PASS+1))
  TOTAL=$((TOTAL+1))
  ok "$1"
}

fail() {
  FAIL=$((FAIL+1))
  TOTAL=$((TOTAL+1))
  err "$1"
}

skip() {
  SKIP=$((SKIP+1))
  TOTAL=$((TOTAL+1))
  warn "$1 (跳过)"
}

# ── 解析参数 ──
while [[ $# -gt 0 ]]; do
  case "$1" in
    --quick)
      QUICK_MODE=true
      shift
      ;;
    --with-mock)
      WITH_MOCK=true
      shift
      ;;
    --port)
      GATEWAY_PORT="$2"
      GATEWAY_URL="http://127.0.0.1:${GATEWAY_PORT}"
      shift 2
      ;;
    --timeout)
      TIMEOUT="$2"
      shift 2
      ;;
    --help)
      echo "用法: $0 [--quick] [--with-mock] [--port PORT] [--timeout SECONDS]"
      echo ""
      echo "选项:"
      echo "  --quick        快速验证（仅 L1-L2）"
      echo "  --with-mock    包含 Mock Provider 测试"
      echo "  --port PORT    指定网关端口（默认: 8782）"
      echo "  --timeout SEC  HTTP 超时时间（默认: 10）"
      echo "  --help         显示帮助"
      exit 0
      ;;
    *)
      err "未知参数: $1"
      exit 1
      ;;
  esac
done

# ── 初始化报告 ──
cat > "$REPORT_FILE" <<EOF
# LLM Gateway 本地部署验证报告

**生成时间**: $(date '+%Y-%m-%d %H:%M:%S %Z')
**目标**: $GATEWAY_URL
**模式**: $([ "$QUICK_MODE" = true ] && echo "Quick (L1-L2)" || echo "Full (L1-L4)")

---

EOF

# ════════════════════════════════════════════════════════════════════
# 前置检查
# ════════════════════════════════════════════════════════════════════
precheck() {
  heading "前置检查"

  # 检查必需工具
  if command -v curl >/dev/null 2>&1; then
    pass "curl 已安装"
  else
    fail "curl 未安装（必需）"
    return 1
  fi

  if command -v jq >/dev/null 2>&1; then
    pass "jq 已安装"
  else
    warn "jq 未安装（可选，JSON 解析能力受限）"
  fi

  if command -v docker >/dev/null 2>&1; then
    pass "docker 已安装"
  else
    warn "docker 未安装（无法检查容器状态）"
  fi

  # 检查网关是否可达
  info "检查网关端口 $GATEWAY_PORT..."
  if curl -sf --max-time 3 "$GATEWAY_URL/healthz" >/dev/null 2>&1; then
    pass "网关端口 $GATEWAY_PORT 可达"
  else
    fail "网关端口 $GATEWAY_PORT 不可达"
    err "提示: 检查网关是否启动，或使用 --port 指定正确端口"
    return 1
  fi
}

# ════════════════════════════════════════════════════════════════════
# L1: 基础健康检查
# ════════════════════════════════════════════════════════════════════
verify_l1_health() {
  heading "L1: 基础健康检查"

  # 1. /healthz
  info "检查 /healthz..."
  HEALTH_RESPONSE=$(curl -sf --max-time "$TIMEOUT" "$GATEWAY_URL/healthz" 2>/dev/null || echo "")
  if [ -n "$HEALTH_RESPONSE" ]; then
    if echo "$HEALTH_RESPONSE" | grep -q '"status":"ok"'; then
      pass "/healthz: 返回 ok"
      
      # 提取版本信息（如果有 jq）
      if command -v jq >/dev/null 2>&1; then
        VERSION=$(echo "$HEALTH_RESPONSE" | jq -r '.version // empty')
        GIT_SHA=$(echo "$HEALTH_RESPONSE" | jq -r '.git_sha // empty')
        BUILD_SEQ=$(echo "$HEALTH_RESPONSE" | jq -r '.build_seq // empty')
        
        if [ -n "$VERSION" ]; then
          info "  版本: $VERSION (git: $GIT_SHA, seq: $BUILD_SEQ)"
          echo "  - 版本: \`$VERSION\`" >> "$REPORT_FILE"
          echo "  - Git SHA: \`$GIT_SHA\`" >> "$REPORT_FILE"
          echo "  - Build Seq: \`$BUILD_SEQ\`" >> "$REPORT_FILE"
        fi
      fi
    else
      fail "/healthz: 状态不是 ok"
    fi
  else
    fail "/healthz: 无响应或超时"
  fi

  # 2. /readyz
  info "检查 /readyz..."
  READY_RESPONSE=$(curl -sf --max-time "$TIMEOUT" "$GATEWAY_URL/readyz" 2>/dev/null || echo "")
  if [ -n "$READY_RESPONSE" ]; then
    if echo "$READY_RESPONSE" | grep -q '"status":"ready"'; then
      pass "/readyz: 返回 ready"
    else
      fail "/readyz: 状态不是 ready"
    fi
  else
    fail "/readyz: 无响应或超时"
  fi

  # 3. /version
  info "检查 /version..."
  VERSION_RESPONSE=$(curl -sf --max-time "$TIMEOUT" "$GATEWAY_URL/version" 2>/dev/null || echo "")
  if [ -n "$VERSION_RESPONSE" ]; then
    if echo "$VERSION_RESPONSE" | grep -q '"version"'; then
      pass "/version: 返回版本信息"
    else
      fail "/version: 响应格式异常"
    fi
  else
    fail "/version: 无响应或超时"
  fi
}

# ════════════════════════════════════════════════════════════════════
# L2: 依赖连通性
# ════════════════════════════════════════════════════════════════════
verify_l2_dependencies() {
  heading "L2: 依赖连通性"

  # 解析 /readyz 响应
  READY_RESPONSE=$(curl -sf --max-time "$TIMEOUT" "$GATEWAY_URL/readyz" 2>/dev/null || echo "{}")

  # PostgreSQL
  info "检查 PostgreSQL 连通性..."
  if command -v jq >/dev/null 2>&1; then
    # 尝试新格式（.database.connected）和旧格式（.database = "ok"）
    DB_CONNECTED=$(echo "$READY_RESPONSE" | jq -r '.database.connected // empty')
    if [ "$DB_CONNECTED" = "true" ]; then
      DB_LATENCY=$(echo "$READY_RESPONSE" | jq -r '.database.latency // "unknown"')
      pass "PostgreSQL: connected (latency: $DB_LATENCY)"
    else
      DB_STATUS=$(echo "$READY_RESPONSE" | jq -r '.database // "unknown"')
      if [ "$DB_STATUS" = "ok" ]; then
        pass "PostgreSQL: ok"
      else
        fail "PostgreSQL: $DB_STATUS"
      fi
    fi
  else
    if echo "$READY_RESPONSE" | grep -qE '"database".*"ok"|"connected".*true'; then
      pass "PostgreSQL: ok"
    else
      warn "PostgreSQL: 状态未知（无 jq）"
    fi
  fi

  # Redis
  info "检查 Redis 连通性..."
  if command -v jq >/dev/null 2>&1; then
    # 尝试新格式（.redis.connected）和旧格式（.redis = "ok"）
    REDIS_CONNECTED=$(echo "$READY_RESPONSE" | jq -r '.redis.connected // empty')
    if [ "$REDIS_CONNECTED" = "true" ]; then
      REDIS_LATENCY=$(echo "$READY_RESPONSE" | jq -r '.redis.latency // "unknown"')
      pass "Redis: connected (latency: $REDIS_LATENCY)"
    else
      REDIS_STATUS=$(echo "$READY_RESPONSE" | jq -r '.redis // "unknown"')
      if [ "$REDIS_STATUS" = "ok" ]; then
        pass "Redis: ok"
      else
        fail "Redis: $REDIS_STATUS"
      fi
    fi
  else
    if echo "$READY_RESPONSE" | grep -qE '"redis".*"ok"|"connected".*true'; then
      pass "Redis: ok"
    else
      warn "Redis: 状态未知（无 jq）"
    fi
  fi

  # 容器状态（如果有 docker）
  if command -v docker >/dev/null 2>&1; then
    info "检查 Docker 容器状态..."
    
    if docker ps | grep -q llm-gateway-pg; then
      pass "PostgreSQL 容器运行中"
    else
      warn "PostgreSQL 容器未运行（可能使用外部数据库）"
    fi
    
    if docker ps | grep -q llm-gateway-redis; then
      pass "Redis 容器运行中"
    else
      warn "Redis 容器未运行（可能使用外部 Redis）"
    fi
  fi
}

# ════════════════════════════════════════════════════════════════════
# L3: 功能链路验证
# ════════════════════════════════════════════════════════════════════
verify_l3_functional() {
  heading "L3: 功能链路验证"

  if [ "$QUICK_MODE" = true ]; then
    skip "L3 验证（Quick 模式）"
    return 0
  fi

  # 3.1 Admin API 端点可达性
  info "检查 Admin API 端点..."
  set +u  # Temporarily disable unbound variable check
  ADMIN_RESPONSE=$(curl -sf --max-time "$TIMEOUT" "$GATEWAY_URL/api/admin/providers" 2>/dev/null || true)
  
  if [ -n "$ADMIN_RESPONSE" ]; then
    # 未认证应该返回 401
    if echo "$ADMIN_RESPONSE" | grep -qE '"error"|"message"'; then
      pass "Admin API 端点可达（未认证，符合预期）"
    else
      warn "Admin API 端点响应异常"
    fi
  else
    ADMIN_CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time "$TIMEOUT" "$GATEWAY_URL/api/admin/providers" 2>/dev/null || echo "000")
    if [ "$ADMIN_CODE" = "401" ] || [ "$ADMIN_CODE" = "403" ]; then
      pass "Admin API 端点可达（HTTP $ADMIN_CODE）"
    else
      fail "Admin API 端点不可达或响应异常（HTTP $ADMIN_CODE）"
    fi
  fi
  set -u  # Re-enable unbound variable check

  # 3.2 OpenAI-compatible 端点
  info "检查 OpenAI-compatible 端点..."
  
  # /v1/models
  MODELS_RESPONSE=$(curl -sf --max-time "$TIMEOUT" "$GATEWAY_URL/v1/models" 2>/dev/null || true)
  if [ -n "$MODELS_RESPONSE" ]; then
    if echo "$MODELS_RESPONSE" | grep -qE '"data"|"models"'; then
      pass "/v1/models 端点可达"
    else
      warn "/v1/models 端点响应格式异常"
    fi
  else
    MODELS_CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time "$TIMEOUT" "$GATEWAY_URL/v1/models" 2>/dev/null || echo "000")
    if [ "$MODELS_CODE" = "401" ]; then
      pass "/v1/models 端点可达（需要认证）"
    else
      fail "/v1/models 端点不可达（HTTP $MODELS_CODE）"
    fi
  fi

  # 3.3 静态文件服务（前端）
  info "检查前端静态文件..."
  INDEX_RESPONSE=$(curl -sf --max-time "$TIMEOUT" "$GATEWAY_URL/" 2>/dev/null || true)
  if [ -n "$INDEX_RESPONSE" ]; then
    if echo "$INDEX_RESPONSE" | grep -qE '<html|<!DOCTYPE'; then
      pass "前端静态文件可访问"
    else
      warn "前端静态文件格式异常"
    fi
  else
    INDEX_CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time "$TIMEOUT" "$GATEWAY_URL/" 2>/dev/null || echo "000")
    if [ "$INDEX_CODE" = "200" ]; then
      pass "前端静态文件可访问（HTTP 200）"
    else
      fail "前端静态文件不可访问（HTTP $INDEX_CODE）"
    fi
  fi
}

# ════════════════════════════════════════════════════════════════════
# L4: 业务场景验证
# ════════════════════════════════════════════════════════════════════
verify_l4_business() {
  heading "L4: 业务场景验证"

  if [ "$QUICK_MODE" = true ]; then
    skip "L4 验证（Quick 模式）"
    return 0
  fi

  set +u  # Disable unbound variable check for this section

  # 4.1 日志文件检查
  info "检查日志文件..."
  
  LOG_ROOT="${LLM_GATEWAY_ROOT:-$HOME/kaixuan/llm-gateway-go}"
  CURRENT_PORT="$GATEWAY_PORT"
  LOG_FILE="$LOG_ROOT/logs/gateway-${CURRENT_PORT}.log"
  
  if [ -f "$LOG_FILE" ]; then
    pass "日志文件存在: $LOG_FILE"
    
    # 检查最近 1 分钟内是否有日志更新
    if [ "$(uname)" = "Darwin" ]; then
      # macOS
      LAST_MODIFIED=$(stat -f "%m" "$LOG_FILE" 2>/dev/null || echo 0)
    else
      # Linux
      LAST_MODIFIED=$(stat -c "%Y" "$LOG_FILE" 2>/dev/null || echo 0)
    fi
    NOW=$(date +%s)
    AGE=$((NOW - LAST_MODIFIED))
    
    if [ "$AGE" -lt 60 ]; then
      pass "日志文件活跃（最后更新: ${AGE}秒前）"
    else
      warn "日志文件较旧（最后更新: ${AGE}秒前）"
    fi
    
    # 检查是否有 ERROR 或 PANIC
    if grep -qE "ERROR|PANIC|FATAL" "$LOG_FILE" 2>/dev/null; then
      warn "日志包含错误级别消息"
      echo "" >> "$REPORT_FILE"
      echo "**最近的错误日志**:" >> "$REPORT_FILE"
      echo '```' >> "$REPORT_FILE"
      grep -E "ERROR|PANIC|FATAL" "$LOG_FILE" | tail -5 >> "$REPORT_FILE" || true
      echo '```' >> "$REPORT_FILE"
    else
      pass "日志无严重错误"
    fi
  else
    warn "日志文件不存在: $LOG_FILE"
  fi

  # 4.2 进程检查
  info "检查网关进程..."
  GREP_PATTERN="gateway.*${CURRENT_PORT}"
  if ps aux | grep -v grep | grep -q "$GREP_PATTERN"; then
    pass "网关进程运行中（端口 $CURRENT_PORT）"
    
    # 获取进程信息
    PID=$(ps aux | grep -v grep | grep "$GREP_PATTERN" | awk '{print $2}' | head -1)
    if [ -n "$PID" ]; then
      info "  PID: $PID"
      
      # CPU 和内存使用（macOS 和 Linux 兼容）
      if [ "$(uname)" = "Darwin" ]; then
        CPU_MEM=$(ps -p "$PID" -o %cpu,%mem 2>/dev/null | tail -1 || echo "N/A")
      else
        CPU_MEM=$(ps -p "$PID" -o %cpu,%mem --no-headers 2>/dev/null || echo "N/A")
      fi
      info "  资源使用: $CPU_MEM (CPU% MEM%)"
    fi
  else
    fail "网关进程未运行（端口 $CURRENT_PORT）"
  fi

  # 4.3 Mock Provider 测试（可选）
  if [ "$WITH_MOCK" = true ]; then
    info "检查 Mock Provider..."
    MOCK_URL="http://localhost:18080/healthz"
    
    if curl -sf --max-time 3 "$MOCK_URL" >/dev/null 2>&1; then
      pass "Mock Provider 可达 (http://localhost:18080)"
    else
      warn "Mock Provider 不可达（跳过 Mock 测试）"
    fi
  fi

  # 4.4 数据库表检查（如果可以访问）
  if command -v docker >/dev/null 2>&1; then
    if docker ps 2>/dev/null | grep -q llm-gateway-pg; then
      info "检查数据库表..."
      
      TABLES=$(docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -t -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public';" 2>/dev/null | xargs || echo "0")
      
      if [ "$TABLES" -gt 0 ]; then
        pass "数据库包含 $TABLES 个表"
      else
        warn "数据库表为空（可能未初始化）"
      fi
      
      # 检查关键表
      for table in providers credentials sessions request_logs_hot; do
        if docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -t -c "SELECT to_regclass('public.$table');" 2>/dev/null | grep -q "$table"; then
          pass "关键表存在: $table"
        else
          warn "关键表不存在: $table"
        fi
      done
    fi
  fi

  set -u  # Re-enable unbound variable check
}

# ════════════════════════════════════════════════════════════════════
# 生成总结报告
# ════════════════════════════════════════════════════════════════════
generate_summary() {
  heading "验证总结"

  echo "" >> "$REPORT_FILE"
  echo "## 验证结果统计" >> "$REPORT_FILE"
  echo "" >> "$REPORT_FILE"
  echo "| 项目 | 数量 |" >> "$REPORT_FILE"
  echo "|------|------|" >> "$REPORT_FILE"
  echo "| 总检查项 | $TOTAL |" >> "$REPORT_FILE"
  echo "| ✓ 通过 | $PASS |" >> "$REPORT_FILE"
  echo "| ✗ 失败 | $FAIL |" >> "$REPORT_FILE"
  echo "| ⚠ 跳过 | $SKIP |" >> "$REPORT_FILE"
  echo "" >> "$REPORT_FILE"

  SUCCESS_RATE=0
  if [ "$TOTAL" -gt 0 ]; then
    SUCCESS_RATE=$((PASS * 100 / TOTAL))
  fi

  echo "| 成功率 | ${SUCCESS_RATE}% |" >> "$REPORT_FILE"
  echo "" >> "$REPORT_FILE"

  if [ "$FAIL" -eq 0 ]; then
    echo "## 🎉 验证通过" >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    echo "所有检查项均通过，网关部署正常！" >> "$REPORT_FILE"
    
    ok "所有检查通过 ($PASS/$TOTAL)"
    info "成功率: ${SUCCESS_RATE}%"
    return 0
  else
    echo "## ❌ 验证失败" >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    echo "发现 $FAIL 个失败项，请检查上述详情。" >> "$REPORT_FILE"
    
    err "$FAIL 个检查失败 ($PASS/$TOTAL 通过)"
    info "成功率: ${SUCCESS_RATE}%"
    return 1
  fi
}

# ════════════════════════════════════════════════════════════════════
# 主流程
# ════════════════════════════════════════════════════════════════════
main() {
  info "LLM Gateway 本地部署验证"
  info "目标: $GATEWAY_URL"
  info "报告: $REPORT_FILE"
  echo ""

  # 执行验证
  precheck || exit 1
  verify_l1_health
  verify_l2_dependencies
  verify_l3_functional
  verify_l4_business

  # 生成总结
  generate_summary
  RESULT=$?

  echo ""
  info "完整报告已保存到: $REPORT_FILE"
  
  if [ "$RESULT" -eq 0 ]; then
    echo ""
    ok "验证通过！网关可以正常使用。"
    echo ""
    info "后续步骤:"
    echo "  1. 查看 UI: $GATEWAY_URL"
    echo "  2. 配置 Provider: $GATEWAY_URL/admin"
    echo "  3. 测试 API: curl $GATEWAY_URL/v1/models"
    exit 0
  else
    echo ""
    err "验证失败，请查看报告排查问题。"
    echo ""
    info "故障排查:"
    echo "  1. 查看日志: tail -f $LOG_ROOT/logs/gateway-${GATEWAY_PORT}.log"
    echo "  2. 检查进程: ps aux | grep gateway"
    echo "  3. 重新部署: ./scripts/deploy-local.sh deploy"
    exit 1
  fi
}

# 执行主流程
main "$@"
