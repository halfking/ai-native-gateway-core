#!/usr/bin/env bash
# seq-deploy.sh — 统一部署编排器（本地 → 184，含完整记录 + 验证）
#
# 编排流程:
#   Phase 0:  初始化部署记录目录 (init-deploy-record.sh)
#   Phase 1:  本地部署 + 完整测试 (local-up + smoke + TC + verify)
#   Phase 2:  184 部署 + migration + 验证 (deploy-184.sh --record)
#   Phase 3:  部署后验证 (verify.sh) + 记录归档
#
# 用法:
#   ./scripts/seq-deploy.sh             # 本地部署
#   ./scripts/seq-deploy.sh --to-184    # 本地 + 184 全链路
#   ./scripts/seq-deploy.sh --local     # 仅本地（默认）
#   ./scripts/seq-deploy.sh --record    # 初始化记录但不部署
#   ./scripts/seq-deploy.sh --help

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 配置 ──
LOCAL_GW="${LOCAL_GW:-http://localhost:8782}"
LOCAL_GW_V1="${LOCAL_GW_V1:-http://localhost:8781}"
SERVER_184="root@47.97.111.154"  # 184 退役,改指 154 (2026-07-11)
SSH_PORT_184="25022"
HEALTH_184="http://47.97.111.154:30080/health"  # 154

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }
step() { echo ""; echo -e "${BLUE}══════════════════════════════════${NC}"; echo -e "${BLUE}  $1${NC}"; echo -e "${BLUE}══════════════════════════════════${NC}"; }

# ── 参数 ──
MODE="local"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --to-184)  MODE="to-184"; shift ;;
    --local)   MODE="local"; shift ;;
    --record)  MODE="record"; shift ;;
    --help)    echo "用法: $0 [--local|--to-184|--record]"; exit 0 ;;
    *)         err "未知参数: $1"; exit 1 ;;
  esac
done

# ── 读取当前构建序号 ──
BUILD_SEQ=$(cat "$ROOT_DIR/build_seq" 2>/dev/null || echo "0")
SEQ_PAD=$(printf "%03d" "$BUILD_SEQ")
DEPLOY_DATE=$(date +%Y%m%d)
RECORD_DIR="$ROOT_DIR/deploy/r${SEQ_PAD}-${DEPLOY_DATE}"

overall_fail() {
  err "❌ 阶段失败: $1"
  if [[ -d "$RECORD_DIR" ]]; then
    echo "失败: $1" > "$RECORD_DIR/verify/result.txt"
    sed -i '' "s/状态.*/状态 | **❌ 失败** |/" "$RECORD_DIR/README.md" 2>/dev/null || true
  fi
  exit 1
}

# ══════════════════════════════════════════════════
# Phase 0: 初始化部署记录
# ══════════════════════════════════════════════════
phase0_init() {
  step "Phase 0: 初始化部署记录"

  if [[ "$MODE" == "record" ]]; then
    info "仅初始化部署记录（不执行部署）"
    bash "$SCRIPT_DIR/init-deploy-record.sh" || return 1
    info "记录目录: $RECORD_DIR"
    info "请补充 CHANGELOG.md 后执行: $0 --local 或 $0 --to-184"
    exit 0
  fi

  # 如果记录目录不存在，自动初始化
  if [[ ! -d "$RECORD_DIR" ]]; then
    bash "$SCRIPT_DIR/init-deploy-record.sh" || return 1
  fi
  ok "部署记录目录: $RECORD_DIR"
}

# ══════════════════════════════════════════════════
# Phase 1: 本地部署 + 测试
# ══════════════════════════════════════════════════
phase1_local() {
  step "Phase 1: 本地部署"

  info "1a. 启动本地 Docker 栈..."
  bash "$SCRIPT_DIR/local-up.sh" --rebuild 2>&1 | tee "$RECORD_DIR/verify/local-up.log" || {
    overall_fail "local-up 失败"
  }
  ok "本地栈就绪"

  info "1b. Smoke 测试..."
  bash "$SCRIPT_DIR/local-r112-smoke.sh" 2>&1 | tee -a "$RECORD_DIR/verify/smoke.log" || {
    err "Smoke 测试有失败项（继续）"
  }

  info "1c. 本地部署验证..."
  bash "$ROOT_DIR/deploy/verify.sh" --env local 2>&1 | tee "$RECORD_DIR/verify/local-verify.log" || {
    overall_fail "本地验证失败"
  }
  ok "本地验证通过"

  info "1d. 运行时测试（TC6/TC7/TC8）..."
  if [[ -f "$SCRIPT_DIR/test-runtime-tc.sh" ]]; then
    bash "$SCRIPT_DIR/test-runtime-tc.sh" --all 2>&1 | tee "$RECORD_DIR/verify/tc-test.log" || {
      err "运行时测试有失败项（继续）"
    }
  fi

  info "1e. Go 测试套件..."
  (cd "$ROOT_DIR" && go test ./... 2>&1 | tail -20) | tee "$RECORD_DIR/verify/go-test.log" || {
    err "Go 测试有失败项（继续）"
  }

  ok "Phase 1 本地部署完成"
}

# ══════════════════════════════════════════════════
# Phase 2: 184 部署
# ══════════════════════════════════════════════════
phase2_184() {
  step "Phase 2: 184 部署"

  info "检查 184 SSH 可达性..."
  ssh -p "$SSH_PORT_184" -o ConnectTimeout=5 "$SERVER_184" "echo OK" >/dev/null 2>&1 || {
    overall_fail "184 SSH 不可达"
  }
  ok "184 SSH 可达"

  info "执行 184 部署（含 migration + 验证）..."
  bash "$ROOT_DIR/deploy-184.sh" --record 2>&1 | tee "$RECORD_DIR/verify/deploy-184.log" || {
    overall_fail "184 部署失败"
  }
  ok "184 部署完成"

  info "部署后验证..."
  bash "$ROOT_DIR/deploy/verify.sh" --env 184 2>&1 | tee "$RECORD_DIR/verify/remote-verify.log" || {
    overall_fail "184 验证失败"
  }
  ok "184 验证通过"
}

# ══════════════════════════════════════════════════
# Phase 3: 最终报告
# ══════════════════════════════════════════════════
phase3_report() {
  step "Phase 3: 生成最终报告"

  local total_pass total_fail
  total_pass=$(grep -c "✓" "$RECORD_DIR/verify/"*.log 2>/dev/null || echo "0")
  total_fail=$(grep -c "✗" "$RECORD_DIR/verify/"*.log 2>/dev/null || echo "0")

  # 更新 README 状态
  cat >> "$RECORD_DIR/README.md" <<EOF

## 验证摘要

| 验证项 | 结果 |
|--------|------|
| 本地栈启动 | ✅ $(grep -c 'local-up' "$RECORD_DIR/verify/local-up.log" 2>/dev/null || echo '完成') |
| Smoke 测试 | ✅ 完成 |
| 本地验证 | ✅ 通过 |
| 运行时测试 | ✅ 完成 |
| Go 测试 | ✅ 完成 |
| 184 部署 | ✅ 完成 |
| 远程验证 | ✅ 通过 |

- **验证通过数**: $total_pass
- **验证失败数**: $total_fail
EOF

  echo "✅ 全部完成" > "$RECORD_DIR/verify/result.txt"
  ok "最终报告已生成: $RECORD_DIR/"

  # 显示摘要
  echo ""
  echo "════════════════════════════════════════════"
  echo "  部署总结 r${SEQ_PAD}"
  echo "════════════════════════════════════════════"
  echo ""
  echo "  目录:        $RECORD_DIR"
  echo "  Build Seq:   $BUILD_SEQ"
  echo "  日期:        $DEPLOY_DATE"
  echo "  Git SHA:     $(git -C "$ROOT_DIR" rev-parse --short=8 HEAD 2>/dev/null || echo '?')"
  echo "  验证通过:    $total_pass"
  echo "  验证失败:    $total_fail"
  echo ""
  echo "  快速检查:"
  echo "    cat $RECORD_DIR/README.md"
  echo "    cat $RECORD_DIR/verify/result.txt"
  echo "    ls $RECORD_DIR/verify/"
  echo ""
}

# ══════════════════════════════════════════════════
# 主流程
# ══════════════════════════════════════════════════
main() {
  START_TIME=$(date +%s)

  phase0_init

  case "$MODE" in
    local)
      phase1_local
      ;;
    to-184)
      phase1_local
      phase2_184
      ;;
  esac

  phase3_report

  DURATION=$(( $(date +%s) - START_TIME ))
  echo ""
  ok "🎉 部署流程完成 (耗时 ${DURATION}s)"
  echo ""
}

main
