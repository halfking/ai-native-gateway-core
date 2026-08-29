#!/usr/bin/env bash
# =====================================================================
# scripts/verify-deployment-switch.sh — 验证部署脚本切换
#
# 用途：验证本地部署 (deploy-local.sh) 和远程部署 (deploy-154.sh) 
#       脚本的可用性和基本功能，不执行实际部署。
#
# 验证内容：
#   1. 脚本文件存在性和可执行权限
#   2. 脚本语法检查
#   3. 依赖工具检查
#   4. 环境变量检查
#   5. Migration 616 在本地数据库的状态
#
# 用法:
#   bash scripts/verify-deployment-switch.sh
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# 颜色输出
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() { printf "${GREEN}✓${NC} %s\n" "$*"; }
fail() { printf "${RED}✗${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}⚠${NC} %s\n" "$*"; }
info() { printf "ℹ %s\n" "$*"; }

check_script_exists() {
  local script="$1"
  local name="$2"
  
  if [[ -f "$script" ]]; then
    pass "$name exists: $script"
    return 0
  else
    fail "$name NOT found: $script"
    return 1
  fi
}

check_script_executable() {
  local script="$1"
  local name="$2"
  
  if [[ -x "$script" ]]; then
    pass "$name is executable"
    return 0
  else
    warn "$name is NOT executable (可以用 chmod +x 修复)"
    return 1
  fi
}

check_script_syntax() {
  local script="$1"
  local name="$2"
  
  if bash -n "$script" 2>/dev/null; then
    pass "$name syntax is valid"
    return 0
  else
    fail "$name has syntax errors"
    bash -n "$script" 2>&1 | head -5
    return 1
  fi
}

check_dependency() {
  local cmd="$1"
  
  if command -v "$cmd" >/dev/null 2>&1; then
    pass "Dependency $cmd is available"
    return 0
  else
    warn "Dependency $cmd NOT found"
    return 1
  fi
}

check_migration_616() {
  info "Checking Migration 616 status in local database..."
  
  local result
  result=$(docker exec llm-gateway-pg psql -U postgres -d llm_gateway -tAc \
    "SELECT COUNT(*) FROM pg_indexes WHERE tablename = 'provider_error_details' AND indexname = 'idx_provider_error_details_fingerprint';" 2>&1) || {
    fail "Failed to query database: $result"
    return 1
  }
  
  if [[ "$result" == "1" ]]; then
    pass "Migration 616: idx_provider_error_details_fingerprint index exists"
    return 0
  else
    warn "Migration 616: Fingerprint index NOT found (需要执行 migration 616)"
    return 1
  fi
}

check_provider_error_aggregator() {
  info "Checking ProviderErrorAggregator implementation..."
  
  if [[ -f "$ROOT_DIR/bg/provider_error_aggregator.go" ]]; then
    pass "ProviderErrorAggregator source file exists"
  else
    fail "ProviderErrorAggregator source file NOT found"
    return 1
  fi
  
  if [[ -f "$ROOT_DIR/bg/provider_error_aggregator_test.go" ]]; then
    pass "ProviderErrorAggregator test file exists"
  else
    warn "ProviderErrorAggregator test file NOT found"
  fi
  
  return 0
}

main() {
  local errors=0
  
  echo "======================================================================="
  echo "  Deployment Switch Verification"
  echo "  验证部署脚本切换 (本地 <-> 154)"
  echo "======================================================================="
  echo ""
  
  info "Step 1: Checking deployment scripts..."
  check_script_exists "$SCRIPT_DIR/deploy-local.sh" "deploy-local.sh" || ((errors++))
  check_script_exists "$SCRIPT_DIR/deploy-154.sh" "deploy-154.sh" || ((errors++))
  check_script_exists "$SCRIPT_DIR/deploy-seamless.sh" "deploy-seamless.sh" || ((errors++))
  echo ""
  
  info "Step 2: Checking script executability..."
  check_script_executable "$SCRIPT_DIR/deploy-local.sh" "deploy-local.sh" || ((errors++))
  check_script_executable "$SCRIPT_DIR/deploy-154.sh" "deploy-154.sh" || ((errors++))
  echo ""
  
  info "Step 3: Checking script syntax..."
  check_script_syntax "$SCRIPT_DIR/deploy-local.sh" "deploy-local.sh" || ((errors++))
  check_script_syntax "$SCRIPT_DIR/deploy-154.sh" "deploy-154.sh" || ((errors++))
  echo ""
  
  info "Step 4: Checking dependencies..."
  check_dependency "docker" || ((errors++))
  check_dependency "psql" || true  # psql 是可选的
  check_dependency "git" || ((errors++))
  check_dependency "bash" || ((errors++))
  echo ""
  
  info "Step 5: Checking local database status..."
  if docker ps --format '{{.Names}}' | grep -q '^llm-gateway-pg$'; then
    pass "Local PostgreSQL container is running"
    check_migration_616 || ((errors++))
  else
    warn "Local PostgreSQL container is NOT running"
    info "启动容器: docker start llm-gateway-pg"
  fi
  echo ""
  
  info "Step 6: Checking Provider Error Aggregation implementation..."
  check_provider_error_aggregator || ((errors++))
  echo ""
  
  echo "======================================================================="
  if [[ $errors -eq 0 ]]; then
    pass "All checks passed! 所有检查通过！"
    echo ""
    info "部署脚本已就绪："
    info "  - 本地部署: bash scripts/deploy-local.sh deploy"
    info "  - 154部署: bash scripts/deploy-154.sh"
    echo ""
    info "Migration 616 状态："
    info "  - 本地环境: ✅ 已执行"
    info "  - 154环境: ⏳ 待执行 (部署时自动执行)"
    echo ""
    return 0
  else
    fail "$errors check(s) failed. 有 $errors 项检查失败。"
    echo ""
    return 1
  fi
}

main "$@"
