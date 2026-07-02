#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# deploy-to-184-with-migration.sh
#
# deploy-184.sh 的增强版本，在部署后自动执行 DB migration
#
# 用法:
#   bash scripts/deploy-to-184-with-migration.sh
#
# 流程:
#   1. 运行 deploy-184.sh (标准 8 步)
#   2. 执行 DB migration (run-migrations.sh)
#   3. 重启 Pod 使新 schema 生效
#   4. 验证部署
#   5. 提交 build_seq
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

REPO_DIR="${LLM_GATEWAY_REPO:-/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go}"
SKIP_BUILD_SEQ_COMMIT="${SKIP_BUILD_SEQ_COMMIT:-0}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
err()  { printf "${RED}✗ %s${NC}\n" "$*" >&2; }
ok()   { printf "${GREEN}✓ %s${NC}\n" "$*"; }
info() { printf "${YELLOW}▶ %s${NC}\n" "$*"; }
phase() { printf "\n${BLUE}━━━ %s ━━━${NC}\n" "$*"; }

cd "$REPO_DIR"

echo "═══════════════════════════════════════════════════════════"
echo "  184 部署（含 DB migration）$(date '+%Y-%m-%d %H:%M:%S')"
echo "═══════════════════════════════════════════════════════════"
echo ""

# ── Phase 1: 运行 deploy-184.sh ──
phase "Phase 1/5: 运行 deploy-184.sh"
./deploy-184.sh || {
  err "deploy-184.sh 失败"
  exit 1
}
ok "deploy-184.sh 完成"

# ── Phase 2: 执行 DB migration ──
phase "Phase 2/5: 执行 DB migration"
RUN_MIG_SCRIPT="$HOME/.agents/skills/deploy-184/scripts/run-migrations.sh"

if [[ -x "$RUN_MIG_SCRIPT" ]]; then
  bash "$RUN_MIG_SCRIPT" || {
    err "DB migration 失败"
    exit 1
  }
else
  info "run-migrations.sh 不存在，跳过 migration"
fi

# ── Phase 3: 重启 Pod ──
phase "Phase 3/5: 重启 Pod 使新 schema 生效"
ssh -p 25022 root@14.103.112.184 \
  "kubectl rollout restart deployment/llm-gateway-go-deployment -n pms-test"
ssh -p 25022 root@14.103.112.184 \
  "kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=3m"
ok "Pod 重启完成"

# ── Phase 4: 验证 ──
phase "Phase 4/5: 验证部署"
VERIFY_SCRIPT="$HOME/.agents/skills/deploy-184/scripts/verify.sh"
if [[ -x "$VERIFY_SCRIPT" ]]; then
  bash "$VERIFY_SCRIPT" || info "验证脚本报告问题，请手动检查"
else
  info "手动验证: curl http://14.103.112.184:30080/health | jq ."
fi

# ── Phase 5: 提交 build_seq ──
phase "Phase 5/5: 提交 build_seq"
if [[ "$SKIP_BUILD_SEQ_COMMIT" != "1" ]]; then
  CHANGED=$(git status --short | grep -E "build_seq|version.json" | wc -l | tr -d ' ')
  if [[ ${CHANGED} -gt 0 ]]; then
    NEW_BUILD_SEQ=$(cat build_seq 2>/dev/null || echo "unknown")
    git add build_seq version.json
    git commit -m "chore: bump build_seq to ${NEW_BUILD_SEQ} after 184 deploy"
    git push
    ok "build_seq 已提交"
  else
    info "build_seq 无改动"
  fi
else
  info "跳过 build_seq 提交"
fi

echo ""
echo "═══════════════════════════════════════════════════════════"
ok "184 部署完成（含 DB migration）！"
echo "═══════════════════════════════════════════════════════════"
echo ""
echo "  访问: https://llmgo.kxpms.cn"
echo "  验证: curl http://14.103.112.184:30080/health | jq ."
echo ""
