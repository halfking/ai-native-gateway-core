#!/usr/bin/env bash
#
# 提示词优化模块 —— 本地 Docker 测试脚本
#
# 修正说明（2026-11-11 实测后）：
#   - 原版用 `docker-compose`（v1，已在多数机器上不存在）⇒ 改用 `docker compose`（v2 插件）。
#   - 原版没有覆盖「错误路径」（空 prompt / 非法 JSON / 非 POST）⇒ 补齐，
#     因为只测 happy path 的测试证明不了「服务真的在按契约工作」。
#   - 原版没有覆盖 memora 桩路径 ⇒ 补一条 use_memora=true 的用例，
#     验证「接了记忆后端」这条链路真的能跑通（而不是因为没后端被跳过）。
#   - 原版性能测试用 $(date +%s) 秒级计时，10 次请求根本看不出差异
#     ⇒ 改用毫秒计时并把样本量提到 50。
#
# 用法：
#   ./scripts/test-prompt-optimizer.sh all        # 构建+启动+全量测试
#   ./scripts/test-prompt-optimizer.sh build      # 仅构建
#   ./scripts/test-prompt-optimizer.sh start      # 仅启动
#   ./scripts/test-prompt-optimizer.sh test       # 仅跑测试（需服务已启动）
#   ./scripts/test-prompt-optimizer.sh logs       # 看日志
#   ./scripts/test-prompt-optimizer.sh stop       # 停止
#   ./scripts/test-prompt-optimizer.sh clean      # 清理

set -uo pipefail

COMPOSE_FILE="docker-compose.prompt-optimizer.yml"
SERVICE="prompt-optimizer"
BASE_URL="http://localhost:8090"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()  { echo -e "${BLUE}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_ok()    { echo -e "${GREEN}  ✓${NC} $1"; }

# ── 基础设施 ────────────────────────────────────────────────────────

# compose_cmd 解析可用的 compose 命令（v2 插件优先，回退 v1）。
compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    echo "docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    echo "docker-compose"
  else
    return 1
  fi
}

COMPOSE=""
resolve_compose() {
  COMPOSE="$(compose_cmd)" || {
    log_error "未找到 docker compose（v2 插件或 v1 二进制），无法继续"
    exit 1
  }
  log_info "使用 compose 命令: $COMPOSE"
}

check_docker() {
  log_info "检查 Docker 环境..."
  if ! docker info >/dev/null 2>&1; then
    log_error "Docker 未运行，请先启动 Docker"
    exit 1
  fi
  resolve_compose
  log_info "Docker 环境正常"
}

cleanup() {
  log_info "清理旧容器..."
  $COMPOSE -f "$COMPOSE_FILE" down -v --remove-orphans 2>/dev/null || true
  log_info "清理完成"
}

build() {
  log_info "构建 Docker 镜像..."
  $COMPOSE -f "$COMPOSE_FILE" build "$SERVICE" || {
    log_error "镜像构建失败"
    $COMPOSE -f "$COMPOSE_FILE" logs --tail=50
    exit 1
  }
  log_info "镜像构建完成"
}

start() {
  log_info "启动服务..."
  $COMPOSE -f "$COMPOSE_FILE" up -d || {
    log_error "服务启动失败"
    $COMPOSE -f "$COMPOSE_FILE" logs --tail=50
    exit 1
  }
  check_health
}

check_health() {
  log_info "检查服务健康状态..."
  local max_attempts=40 attempt=0

  while [ "$attempt" -lt "$max_attempts" ]; do
    if curl -fsS "$BASE_URL/health" >/dev/null 2>&1; then
      log_info "服务健康检查通过"
      return 0
    fi
    attempt=$((attempt + 1))
    log_warn "等待服务启动... ($attempt/$max_attempts)"
    sleep 2
  done

  log_error "服务启动失败（超时 ${max_attempts} 次）"
  $COMPOSE -f "$COMPOSE_FILE" logs --tail=80 "$SERVICE"
  exit 1
}

stop() {
  log_info "停止服务..."
  $COMPOSE -f "$COMPOSE_FILE" down
  log_info "服务已停止"
}

show_logs() {
  $COMPOSE -f "$COMPOSE_FILE" logs --tail=100 "$SERVICE"
}

# ── 测试用例 ────────────────────────────────────────────────────────

PASS_COUNT=0
FAIL_COUNT=0

# assert_contains <名称> <JSON> <子串>
assert_contains() {
  local name="$1" payload="$2" needle="$3"
  if printf '%s' "$payload" | grep -qF -- "$needle"; then
    log_ok "$name"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    log_error "$name —— 输出中找不到: $needle"
    printf '%s\n' "$payload" | head -20
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# assert_status <名称> <期望HTTP码> <实际HTTP码>
assert_status() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    log_ok "$name (HTTP $got)"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    log_error "$name —— 期望 HTTP ${want}，实际 ${got}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# post <路径> <JSON体> → 输出 "<HTTP码>\n<body>"
post() {
  local path="$1" body="$2"
  curl -sS -o /tmp/po_body.txt -w '%{http_code}' \
    -X POST "$BASE_URL$path" \
    -H 'Content-Type: application/json' \
    -d "$body"
}

test_health() {
  log_info "测试: 健康检查"
  local payload
  payload="$(curl -sS "$BASE_URL/health")"
  assert_contains "健康检查返回 healthy" "$payload" '"status":"healthy"'
}

test_basic_optimize() {
  log_info "测试: 基础优化"
  local code payload
  code="$(post /api/v1/optimize '{
    "prompt": "帮我翻译这段文字",
    "task_type": "translation",
    "domain": "technical",
    "inject_harness": true
  }')"
  payload="$(cat /tmp/po_body.txt)"

  assert_status "基础优化返回 200" 200 "$code"
  assert_contains "返回优化后提示词" "$payload" '"optimized"'
  assert_contains "记录了应用策略" "$payload" '"applied_strategies"'
  assert_contains "注入了 harness 要素" "$payload" '"harness_elements"'
  assert_contains "产出角色定义" "$payload" '角色定义'
  assert_contains "产出安全规则" "$payload" '安全规则'
}

test_optimize_without_harness() {
  log_info "测试: 不注入 harness 时不应出现 harness 章节"
  local code payload
  code="$(post /api/v1/optimize '{
    "prompt": "帮我写个函数",
    "task_type": "code_generation",
    "inject_harness": false
  }')"
  payload="$(cat /tmp/po_body.txt)"

  assert_status "返回 200" 200 "$code"
  if printf '%s' "$payload" | grep -q '安全规则'; then
    log_error "未开启 harness 却出现安全规则章节"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    log_ok "未开启 harness 时无 harness 章节"
    PASS_COUNT=$((PASS_COUNT + 1))
  fi
}

test_optimize_with_memora() {
  log_info "测试: 接入 memora 桩的优化链路"
  local code payload
  code="$(post /api/v1/optimize '{
    "prompt": "帮我总结这份材料",
    "task_type": "summarization",
    "use_memora": true,
    "inject_harness": false
  }')"
  payload="$(cat /tmp/po_body.txt)"

  assert_status "memora 链路返回 200" 200 "$code"
  assert_contains "memora 链路产出提示词" "$payload" '"optimized"'
}

test_batch_optimize() {
  log_info "测试: 批量优化"
  local code payload count
  code="$(post /api/v1/optimize/batch '{
    "prompts": ["总结这篇文章", "分析这组数据", "生成一个报告"],
    "task_type": "general",
    "inject_harness": true
  }')"
  payload="$(cat /tmp/po_body.txt)"

  assert_status "批量优化返回 200" 200 "$code"
  count="$(printf '%s' "$payload" | jq -r '.results | length' 2>/dev/null || echo 0)"
  if [ "$count" = "3" ]; then
    log_ok "批量优化返回 3 条结果"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    log_error "批量优化应返回 3 条结果，实际 $count"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

test_compare() {
  log_info "测试: 提示词对比（quality 模式，无需模型通道）"
  local code payload
  code="$(post /api/v1/compare '{
    "prompt_a": "简短提示词",
    "prompt_b": "# 角色定义\n你是一个助手\n# 任务目标\n做事\n# 约束条件\n- 必须准确\n# 输出格式\n[内容]\n# 质量检查\n完成后检查",
    "mode": "quality"
  }')"
  payload="$(cat /tmp/po_body.txt)"

  assert_status "quality 对比返回 200" 200 "$code"
  assert_contains "回显对比口径" "$payload" '"mode":"quality"'
  assert_contains "回显判读口径" "$payload" '"interpretation"'
  assert_contains "返回胜者" "$payload" '"winner"'

  # 结构更完整的提示词应得分更高。
  local score_a score_b
  score_a="$(printf '%s' "$payload" | jq -r '.score_a' 2>/dev/null || echo -1)"
  score_b="$(printf '%s' "$payload" | jq -r '.score_b' 2>/dev/null || echo -1)"
  if [ "$score_b" = "-" ] || [ "$score_a" = "-" ]; then
    log_error "对比未返回数值分数"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif awk "BEGIN{exit !($score_b > $score_a)}"; then
    log_ok "结构更完整的提示词得分更高 (${score_a} → ${score_b})"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    log_error "结构更完整的提示词应得分更高，实际 ${score_a} → ${score_b}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

test_compare_output_mode() {
  log_info "测试: 提示词对比（output 模式，走模型通道）"
  local code payload
  code="$(post /api/v1/compare '{
    "prompt_a": "翻译成英文",
    "prompt_b": "你是专业翻译。请将输入翻译成英文，保持原文语气与术语准确。",
    "mode": "output",
    "test_cases": [
      {"id": "t1", "input": "你好世界", "expected": "Hello world"}
    ]
  }')"
  payload="$(cat /tmp/po_body.txt)"

  assert_status "output 对比返回 200" 200 "$code"
  assert_contains "回显对比口径" "$payload" '"mode":"output"'
  assert_contains "返回胜者" "$payload" '"winner"'
}

test_optimize_with_evaluation() {
  log_info "测试: 带评估的优化（走模型通道）"
  local code payload
  code="$(post /api/v1/optimize/evaluate '{
    "prompt": "翻译成英文",
    "test_cases": [
      {"id": "t1", "input": "你好世界", "expected": "Hello world"}
    ]
  }')"
  payload="$(cat /tmp/po_body.txt)"

  assert_status "带评估优化返回 200" 200 "$code"
  assert_contains "返回基线分" "$payload" '"baseline_score"'
  assert_contains "返回优化后分" "$payload" '"optimized_score"'
  assert_contains "返回绝对分差" "$payload" '"absolute_delta"'
}

test_error_paths() {
  log_info "测试: 错误路径"

  local code
  # 1. 空 prompt
  code="$(post /api/v1/optimize '{"prompt": ""}')"
  assert_status "空 prompt 返回 400" 400 "$code"

  # 2. 非法 JSON
  code="$(post /api/v1/optimize '{not json}')"
  assert_status "非法 JSON 返回 400" 400 "$code"

  # 3. 未知字段（DisallowUnknownFields）
  code="$(post /api/v1/optimize '{"prompt":"x","unknown_field":1}')"
  assert_status "未知字段返回 400" 400 "$code"

  # 4. GET 打到 POST 端点
  code="$(curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/api/v1/optimize")"
  assert_status "GET 打到 POST 端点返回 405" 405 "$code"

  # 5. 对比缺测试用例（output 模式才需要 test_cases）
  code="$(post /api/v1/compare '{"prompt_a":"a","prompt_b":"b","mode":"output"}')"
  assert_status "output 模式缺测试用例返回 400" 400 "$code"

  # 6. 非法 mode
  code="$(post /api/v1/compare '{"prompt_a":"a","prompt_b":"b","mode":"bogus"}')"
  assert_status "非法 mode 返回 400" 400 "$code"
}

test_performance() {
  log_info "测试: 性能（50 次请求）"
  local n=50
  local start_ms end_ms total_ms avg_ms

  start_ms="$(python3 -c 'import time; print(int(time.time()*1000))')"
  for _ in $(seq 1 "$n"); do
    curl -sS -o /dev/null -X POST "$BASE_URL/api/v1/optimize" \
      -H 'Content-Type: application/json' \
      -d '{"prompt":"性能测试提示词","task_type":"general","inject_harness":true}' 2>/dev/null
  done
  end_ms="$(python3 -c 'import time; print(int(time.time()*1000))')"

  total_ms=$((end_ms - start_ms))
  avg_ms=$((total_ms / n))
  log_ok "$n 次请求总耗时 ${total_ms}ms，平均 ${avg_ms}ms/次"
}

run_all_tests() {
  log_info "=========================================="
  log_info "开始运行完整测试套件"
  log_info "=========================================="

  test_health
  test_basic_optimize
  test_optimize_without_harness
  test_optimize_with_memora
  test_batch_optimize
  test_compare
  test_compare_output_mode
  test_optimize_with_evaluation
  test_error_paths
  test_performance

  log_info "=========================================="
  if [ "$FAIL_COUNT" -eq 0 ]; then
    log_info "✓ 全部 ${PASS_COUNT} 项断言通过"
    log_info "=========================================="
    return 0
  fi
  log_error "✗ ${FAIL_COUNT} 项断言失败（共 $((PASS_COUNT + FAIL_COUNT)) 项）"
  log_info "=========================================="
  return 1
}

main() {
  case "${1:-all}" in
    build)
      check_docker; cleanup; build ;;
    start)
      check_docker; start ;;
    test)
      resolve_compose; run_all_tests ;;
    stop)
      resolve_compose; stop ;;
    logs)
      resolve_compose; show_logs ;;
    clean)
      resolve_compose; cleanup ;;
    all)
      check_docker
      cleanup
      build
      start
      run_all_tests
      rc=$?
      echo ""
      log_info "提示：查看日志用 '$0 logs'，停止服务用 '$0 stop'"
      exit $rc ;;
    *)
      sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 1 ;;
  esac
}

main "$@"
