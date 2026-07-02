#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# deploy-to-184-after-local-test.sh — 本地测试通过后自动部署到 184
#
# 完整端到端流程:
#   1. 本地验证 (local-deploy-test)
#   2. 如果通过，部署到 184 (含 DB migration)
#
# 用法:
#   bash scripts/deploy-to-184-after-local-test.sh
#   SKIP_LOCAL_TEST=1 .../deploy-to-184-after-local-test.sh
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

REPO_DIR="${LLM_GATEWAY_REPO:-/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go}"
SKIP_LOCAL_TEST="${SKIP_LOCAL_TEST:-0}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
err()  { printf "${RED}✗ %s${NC}\n" "$*" >&2; }
ok()   { printf "${GREEN}✓ %s${NC}\n" "$*"; }
info() { printf "${YELLOW}▶ %s${NC}\n" "$*"; }
phase() { printf "\n${BLUE}━━━ %s ━━━${NC}\n" "$*"; }

cd "$REPO_DIR"

START_TIME=$(date +%s)

echo "═══════════════════════════════════════════════════════════"
echo "  本地测试 → 184 部署 端到端流程"
echo "═══════════════════════════════════════════════════════════"
echo ""

# ── Stage 1: 本地验证 ──
if [[ "$SKIP_LOCAL_TEST" = "1" ]]; then
  phase "Stage 1/2: 本地验证 (跳过)"
  info "SKIP_LOCAL_TEST=1"
else
  phase "Stage 1/2: 本地验证"
  
  VERIFY_SCRIPT="$HOME/.agents/skills/local-deploy-test/scripts/verify-all.sh"
  
  if [[ ! -x "$VERIFY_SCRIPT" ]]; then
    err "本地验证脚本不存在: $VERIFY_SCRIPT"
    exit 1
  fi
  
  SKIP_COLUMNAR=1 bash "$VERIFY_SCRIPT" || {
    err "本地验证失败，阻止部署到 184"
    exit 1
  }
  
  ok "本地验证通过"
fi

# ── Stage 2: 部署到 184 ──
phase "Stage 2/2: 部署到 184"

DEPLOY_SCRIPT="$REPO_DIR/scripts/deploy-to-184-with-migration.sh"

if [[ -x "$DEPLOY_SCRIPT" ]]; then
  bash "$DEPLOY_SCRIPT" || {
    err "部署失败"
    exit 1
  }
else
  err "$DEPLOY_SCRIPT 不存在"
  exit 1
fi

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo ""
echo "═══════════════════════════════════════════════════════════"
ok "端到端部署完成！(${ELAPSED}s)"
echo "═══════════════════════════════════════════════════════════"
echo ""
echo "  本地: http://localhost:8781 (v1) / http://localhost:8782 (v2)"
echo "  生产: https://llmgo.kxpms.cn"
echo ""
