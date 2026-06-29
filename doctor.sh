#!/usr/bin/env bash
# =====================================================================
# doctor.sh — llm-gateway-go 服务自检（rule 22 §9）
#
# 用法: ./doctor.sh
# 返回: 0 = 全部通过, 1 = 检查失败
# =====================================================================

set -euo pipefail

PASS=0; FAIL=0; WARN=0

check_pass() { echo "  ✅ $1"; ((PASS++)); }
check_fail() { echo "  ❌ $1"; ((FAIL++)); }
check_warn() { echo "  ⚠️  $1"; ((WARN++)); }

echo "━━━ doctor: llm-gateway-go ━━━"

# ── 1. 配置完整性 ──────────────────────────────────────────────────
[[ -f .env ]] && check_pass ".env 存在" || check_warn ".env 不存在（cp .env.example .env）"
[[ -f .env.example ]] && check_pass ".env.example 存在" || check_fail ".env.example 缺失"

# ── 2. 构建文件完整性 ─────────────────────────────────────────────
[[ -f Dockerfile ]] && check_pass "Dockerfile 存在" || check_fail "Dockerfile 缺失"
[[ -f README.md ]] && check_pass "README.md 存在" || check_warn "README.md 缺失"

# ── 3. deploy/ 完整性 ──────────────────────────────────────────────
if [[ -d deploy ]]; then
  check_pass "deploy/ 存在"
  for script in deploy.sh verify.sh rollback.sh build-image.sh; do
    [[ -f "deploy/$script" ]] && check_pass "deploy/$script 存在" || check_fail "deploy/$script 缺失"
  done
else
  check_fail "deploy/ 缺失"
fi

# ── 4. 本地配置（rule 21） ──────────────────────────────────────────
[[ -f LOCAL_CONFIG.md.template ]] && check_pass "LOCAL_CONFIG.md.template 存在" || check_warn "LOCAL_CONFIG.md.template 缺失"
# 如果 LOCAL_CONFIG.md 存在，检查权限
if [[ -f LOCAL_CONFIG.md ]]; then
  mode=$(stat -c %a LOCAL_CONFIG.md 2>/dev/null || stat -f %Lp LOCAL_CONFIG.md)
  [[ "$mode" == "600" ]] && check_pass "LOCAL_CONFIG.md 权限 600" || check_warn "LOCAL_CONFIG.md 权限 $mode（建议 600）"
fi

# ── 5. 端口检查（可选） ────────────────────────────────────────────
# PORT=${PORT:-8080}
# if lsof -i :$PORT &>/dev/null 2>&1; then
#   check_pass "端口 $PORT 已监听"
# else
#   check_warn "端口 $PORT 未监听（服务未运行？）"
# fi

# ── 汇总 ───────────────────────────────────────────────────────────
echo "━━━ 汇总 ━━━"
echo "  通过: $PASS, 警告: $WARN, 失败: $FAIL"
exit $FAIL
