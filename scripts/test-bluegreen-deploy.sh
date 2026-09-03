#!/usr/bin/env bash
# ============================================================================
# scripts/test-bluegreen-deploy.sh — 蓝绿部署集成测试
#
# 测试场景：
#   1. Nginx 初始化测试
#   2. 首次蓝绿部署测试
#   3. 零停机切换验证（并发请求）
#   4. 快速回滚测试
#   5. 端口切换测试
#   6. 故障恢复测试
#
# 用法:
#   bash scripts/test-bluegreen-deploy.sh              # 完整测试套件
#   bash scripts/test-bluegreen-deploy.sh --quick      # 快速冒烟测试
#   bash scripts/test-bluegreen-deploy.sh --cleanup    # 清理测试环境
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# shellcheck source=local-host-layout-helper.sh
source "$SCRIPT_DIR/local-host-layout-helper.sh"

# ── Colors ───────────────────────────────────────────────────────────────
RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'
BLUE=$'\033[0;34m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'

test_pass() { echo -e "${GREEN}  ✓${NC} $1"; }
test_fail() { echo -e "${RED}  ✗${NC} $1"; FAILED=$((FAILED + 1)); }
test_skip() { echo -e "${YELLOW}  ⊘${NC} $1 (skipped)"; }
test_info() { echo -e "${BLUE}  ℹ${NC} $1"; }
section()   { echo -e "\n${CYAN}━━━ $* ━━━${NC}"; }

TOTAL=0
PASSED=0
FAILED=0
MODE="${1:-full}"

# ── 测试报告 ──────────────────────────────────────────────────────────────
report_summary() {
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  Test Summary"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  Total:  $TOTAL"
  echo "  Passed: $PASSED"
  echo "  Failed: $FAILED"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  
  if [[ $FAILED -gt 0 ]]; then
    echo -e "${RED}❌ Tests FAILED${NC}"
    return 1
  else
    echo -e "${GREEN}✅ All tests PASSED${NC}"
    return 0
  fi
}

# ── 测试用例 ──────────────────────────────────────────────────────────────

# Test 1: Nginx 初始化
test_nginx_init() {
  section "Test 1: Nginx Initialization"
  TOTAL=$((TOTAL + 1))
  
  local nginx_root
  nginx_root=$(lh_nginx_root)
  
  # 如果 Nginx 已安装，跳过
  if [[ -f "$nginx_root/nginx.conf" ]] && [[ -f "$nginx_root/active-port.conf" ]]; then
    test_skip "Nginx already initialized"
    PASSED=$((PASSED + 1))
    return 0
  fi
  
  # 初始化 Nginx
  if bash "$SCRIPT_DIR/setup-local-nginx.sh" install 2>&1 | grep -q "安装完成"; then
    test_pass "Nginx initialized successfully"
    PASSED=$((PASSED + 1))
  else
    test_fail "Nginx initialization failed"
  fi
  
  # 验证配置文件
  if [[ -f "$nginx_root/nginx.conf" ]]; then
    test_pass "nginx.conf exists"
  else
    test_fail "nginx.conf missing"
  fi
  
  if [[ -f "$nginx_root/active-port.conf" ]]; then
    test_pass "active-port.conf exists"
  else
    test_fail "active-port.conf missing"
  fi
  
  # 验证 Nginx 进程
  if pgrep -f "nginx.*$nginx_root" >/dev/null; then
    test_pass "Nginx process running"
  else
    test_fail "Nginx process not running"
  fi
}

# Test 2: 首次部署
test_first_deploy() {
  section "Test 2: First Blue-Green Deployment"
  TOTAL=$((TOTAL + 1))
  
  local start_time end_time duration
  start_time=$(date +%s)
  
  # 执行部署
  if bash "$SCRIPT_DIR/local-host-deploy-bluegreen.sh" deploy 2>&1 | tee /tmp/bluegreen-test-deploy.log | grep -q "✅ Deployment complete"; then
    test_pass "Deployment completed"
    PASSED=$((PASSED + 1))
  else
    test_fail "Deployment failed"
    cat /tmp/bluegreen-test-deploy.log | tail -50
    return 1
  fi
  
  end_time=$(date +%s)
  duration=$((end_time - start_time))
  test_info "Deployment took ${duration}s"
  
  # 验证 Nginx 可访问
  if curl -fsS --max-time 5 http://localhost:8781/healthz >/dev/null 2>&1; then
    test_pass "Nginx proxy responding"
  else
    test_fail "Nginx proxy not responding"
  fi
  
  # 验证版本信息
  local version_body
  version_body=$(curl -fsS --max-time 5 http://localhost:8781/version 2>&1 || echo "{}")
  local version
  version=$(echo "$version_body" | python3 -c "import sys,json;print(json.load(sys.stdin).get('version',''))" 2>/dev/null || echo "")
  
  if [[ -n "$version" ]]; then
    test_pass "Version endpoint working: $version"
  else
    test_fail "Version endpoint not working"
  fi
}

# Test 3: 零停机验证（并发请求）
test_zero_downtime() {
  section "Test 3: Zero-Downtime Switch Verification"
  TOTAL=$((TOTAL + 1))
  
  test_info "Starting background request loop..."
  
  # 启动后台请求循环（每 100ms 一次，持续 30s）
  local success_count=0
  local fail_count=0
  local result_file="/tmp/bluegreen-test-requests.log"
  : > "$result_file"
  
  (
    for i in {1..300}; do
      if curl -fsS --max-time 1 http://localhost:8781/healthz >/dev/null 2>&1; then
        echo "OK" >> "$result_file"
      else
        echo "FAIL" >> "$result_file"
      fi
      sleep 0.1
    done
  ) &
  local request_loop_pid=$!
  
  sleep 2  # 让请求循环稳定
  
  # 执行第二次部署（触发切换）
  test_info "Triggering deployment during request loop..."
  bash "$SCRIPT_DIR/local-host-deploy-bluegreen.sh" deploy >/dev/null 2>&1 &
  local deploy_pid=$!
  
  # 等待部署完成
  wait "$deploy_pid" || true
  
  # 等待请求循环结束
  wait "$request_loop_pid" || true
  
  # 统计结果
  success_count=$(grep -c "OK" "$result_file" || echo 0)
  fail_count=$(grep -c "FAIL" "$result_file" || echo 0)
  
  test_info "Requests: $success_count succeeded, $fail_count failed"
  
  # 判断：失败率 < 1%
  local total_requests=$((success_count + fail_count))
  if [[ $total_requests -gt 0 ]]; then
    local fail_rate=$((fail_count * 100 / total_requests))
    if [[ $fail_rate -lt 1 ]]; then
      test_pass "Zero-downtime achieved (failure rate: ${fail_rate}%)"
      PASSED=$((PASSED + 1))
    else
      test_fail "High failure rate: ${fail_rate}%"
    fi
  else
    test_fail "No requests recorded"
  fi
}

# Test 4: 快速回滚
test_rollback() {
  section "Test 4: Fast Rollback"
  TOTAL=$((TOTAL + 1))
  
  # 获取当前版本
  local current_version
  current_version=$(lh_active_version)
  test_info "Current version: $current_version"
  
  # 列出可回滚版本
  local rollback_target
  rollback_target=$(lh_list_verified_releases | head -1 || echo "")
  
  if [[ -z "$rollback_target" ]]; then
    test_skip "No rollback target available"
    PASSED=$((PASSED + 1))
    return 0
  fi
  
  test_info "Rollback target: $rollback_target"
  
  # 执行回滚
  local start_time end_time duration
  start_time=$(date +%s)
  
  if bash "$SCRIPT_DIR/local-host-deploy-bluegreen.sh" rollback "$rollback_target" 2>&1 | grep -q "✅ Rollback complete"; then
    test_pass "Rollback succeeded"
    PASSED=$((PASSED + 1))
  else
    test_fail "Rollback failed"
    return 1
  fi
  
  end_time=$(date +%s)
  duration=$((end_time - start_time))
  test_info "Rollback took ${duration}s"
  
  # 验证切换生效
  if curl -fsS --max-time 5 http://localhost:8781/healthz >/dev/null 2>&1; then
    test_pass "Service still healthy after rollback"
  else
    test_fail "Service unhealthy after rollback"
  fi
  
  # 验证版本变更
  local new_version
  new_version=$(lh_active_version)
  if [[ "$new_version" == "$rollback_target" ]]; then
    test_pass "Active version switched to $rollback_target"
  else
    test_fail "Active version mismatch: expected=$rollback_target, got=$new_version"
  fi
}

# Test 5: 端口切换
test_port_switch() {
  section "Test 5: Manual Port Switch"
  TOTAL=$((TOTAL + 1))
  
  local current_port
  current_port=$(lh_active_port)
  local target_port
  target_port=$(lh_candidate_port)
  
  test_info "Current port: $current_port, Target port: $target_port"
  
  # 确保目标端口有实例运行
  if ! lh_port_in_use "$target_port"; then
    test_skip "Target port $target_port not in use"
    PASSED=$((PASSED + 1))
    return 0
  fi
  
  # 执行切换
  if bash "$SCRIPT_DIR/local-host-deploy-bluegreen.sh" switch "$target_port" 2>&1 | grep -q "Switched to port"; then
    test_pass "Port switched to $target_port"
    PASSED=$((PASSED + 1))
  else
    test_fail "Port switch failed"
    return 1
  fi
  
  # 验证切换生效
  local new_port
  new_port=$(lh_active_port)
  if [[ "$new_port" == "$target_port" ]]; then
    test_pass "Active port updated to $target_port"
  else
    test_fail "Active port not updated: $new_port != $target_port"
  fi
  
  # 验证服务仍可访问
  if curl -fsS --max-time 5 http://localhost:8781/healthz >/dev/null 2>&1; then
    test_pass "Service accessible after port switch"
  else
    test_fail "Service not accessible after port switch"
  fi
}

# Test 6: 状态检查
test_status_check() {
  section "Test 6: Status Check"
  TOTAL=$((TOTAL + 1))
  
  if bash "$SCRIPT_DIR/local-host-deploy-bluegreen.sh" status 2>&1 | grep -q "Blue-Green Deployment Status"; then
    test_pass "Status command working"
    PASSED=$((PASSED + 1))
  else
    test_fail "Status command failed"
  fi
}

# ── 清理测试环境 ──────────────────────────────────────────────────────────
cleanup_test_env() {
  section "Cleanup Test Environment"
  
  # 停止所有 gateway 实例
  for port in 18781 18782; do
    if lh_port_in_use "$port"; then
      test_info "Stopping gateway on port $port..."
      lh_stop_port "$port" || true
    fi
  done
  
  # 停止 Nginx
  local nginx_root
  nginx_root=$(lh_nginx_root)
  if [[ -f "$nginx_root/nginx.pid" ]]; then
    test_info "Stopping Nginx..."
    bash "$SCRIPT_DIR/setup-local-nginx.sh" --stop || true
  fi
  
  test_pass "Test environment cleaned up"
}

# ── 主测试流程 ────────────────────────────────────────────────────────────
main() {
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  Blue-Green Deployment Integration Test"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  
  # 前置检查
  lh_require_root >/dev/null
  
  if [[ "$MODE" == "--cleanup" ]]; then
    cleanup_test_env
    exit 0
  fi
  
  # 执行测试用例
  test_nginx_init
  test_first_deploy
  
  if [[ "$MODE" != "--quick" ]]; then
    test_zero_downtime
    test_rollback
    test_port_switch
  fi
  
  test_status_check
  
  # 输出报告
  report_summary
}

# 错误处理
trap 'echo -e "\n${RED}Test interrupted${NC}"; report_summary; exit 1' INT TERM

main "$@"
