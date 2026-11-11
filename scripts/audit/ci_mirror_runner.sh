#!/bin/bash
# ─────────────────────────────────────────────────────────────────────────────
# CI 镜像执行器 —— 逐字复刻 .github/workflows/ir-protocol-conversion-ci.yml
#
# 用途：远端是 codeup.aliyun.com（阿里云 Codeup），**不是 GitHub**
# ⇒ 该 GitHub Actions 工作流在远端**不会真正执行**
# ⇒ 本脚本在**干净 worktree**（对齐远端提交）里按工作流定义的 step 逐条执行，
#    产出**可追溯**的本地等价证据（方法 + 观测值 + 退出码）。
#
# 语义对齐 GitHub Actions：某个 step 非零 ⇒ **后续 step 不再执行**（不跳过失败）。
#
# 用法：ci_mirror_runner.sh <worktree-dir> <transcript-path> <完整 40 位 SHA>
#
# ★★ 两条必须遵守的使用纪律（否则读数不可信）：
#  ① **对照树必须建在仓根之外、且与主树同父深度** ——
#     `scripts/deploy-lib` 是仓内**相对符号链接**（`../../../deploy-lib`）⇒
#     建在仓内子目录（如 `.worktrees/`）会让它解析到**另一个路径** ⇒
#     部署类门以**另一个错误**失败 ⇒ 读数同为 exit=1 ⇒ **假红**。
#     （本仓 `AGENTS.md:509` 已记同一坑。）
#  ② **必须传完整 40 位 SHA** —— 本脚本用 `git rev-parse HEAD` 的完整值做等值比对，
#     传缩写会被判为「HEAD 与目标提交不一致」⇒ 门正确地红（这是设计，不是 bug）。
#
# 记录实例：docs/audit/2026-11-11-origin-main-CI验证记录.md
# ─────────────────────────────────────────────────────────────────────────────
set -u

WT="$1"
TRANSCRIPT="$2"
TARGET_SHA="${3:-}"
COVER="/tmp/ir.cover"          # 与工作流同名
FAILED=0

cd "$WT" || exit 1

# 覆盖率文件必须**先删**：上一轮的残留文件会让「go test 失败但 cover 读旧文件」
# 变成假绿（量具层缺陷 ⇒ 门失效）。
rm -f "$COVER"

exec > >(tee "$TRANSCRIPT") 2>&1

echo "=============================================================="
echo " CI 镜像执行记录（ir-protocol-conversion-ci.yml 逐 step 复刻）"
echo "=============================================================="
echo "工作树        : $WT"
echo "对齐提交      : $(git rev-parse HEAD)"
echo "提交标题      : $(git log -1 --format='%s')"
echo "工作树状态    : $(git status --porcelain | wc -l | tr -d ' ') 个改动（0=干净）"
echo "远端          : $(git config --get remote.origin.url)"
echo "工具链        : $(go version)"
echo "宿主          : $(uname -srm)"
echo "开始时间      : $(date '+%Y-%m-%d %H:%M:%S %z')"
echo "=============================================================="

step() {
  local name="$1"; shift
  echo ""
  echo "--------------------------------------------------------------"
  echo ">>> STEP: $name"
  echo ">>> CMD : $*"
  echo "--------------------------------------------------------------"
  local t0 t1 rc
  t0=$(date '+%s')
  "$@"
  rc=$?
  t1=$(date '+%s')
  echo "<<< EXIT: $rc   (耗时 $((t1-t0))s)"
  if [ $rc -ne 0 ]; then
    FAILED=1
    echo "!!! STEP FAILED: $name"
    # GitHub Actions 语义：失败后不再执行后续 step
    return 1
  fi
  return 0
}

# ── step 1: Checkout（worktree 已就位 ⇒ 核对等价条件）────────────────
step "Checkout（核对：HEAD == 目标提交 且 工作树干净）" \
  bash -c 'test "$(git rev-parse HEAD)" = "'"${TARGET_SHA}"'" && test -z "$(git status --porcelain)" && echo "OK: HEAD 与目标提交一致，工作树干净"'
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── step 2: Setup Go（工作流 go-version: 1.23）──────────────────────
echo ""
echo "--------------------------------------------------------------"
echo ">>> STEP: Setup Go（工作流声明 go-version=1.23）"
echo "--------------------------------------------------------------"
echo "实测工具链： $(go version)"
echo "★ 差异披露：本地工具链与工作流声明的版本不一致 ⇒ 记录如实标注。"
echo ""

# ── step 3: go build ./... ─────────────────────────────────────────
step "go build ./..." go build ./...
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── step 4~6: 三个变异 harness ─────────────────────────────────────
step "mutation harness — client error fix" bash scripts/audit/ir-client-error-fix-mutation.sh
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }
step "mutation harness — gemini only loss" bash scripts/audit/ir-gemini-only-loss-mutation.sh
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }
step "mutation harness — coerce numeric" bash scripts/audit/ir-coerce-numeric-mutation.sh
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── step 7: ir package tests (with coverage) ────────────────────────
step "ir package tests (with coverage)" \
  bash -c 'go test ./internal/ir/... -count=1 -timeout=600s -coverprofile=/tmp/ir.cover -covermode=atomic && go tool cover -func=/tmp/ir.cover | tail -1'
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── step 8: coverage gate (ir >= 75%) ──────────────────────────────
step "coverage gate (ir >= 75%)" \
  bash -c 'COV=$(go tool cover -func=/tmp/ir.cover | awk "/^total:/ {gsub(\"%\",\"\",\$3); print \$3}"); echo "total coverage: ${COV}%"; awk -v c="$COV" "BEGIN { if (c+0 < 75) { printf \"coverage %.2f%% < 75%% gate\n\", c; exit 1 } }"'
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── step 9: production session replay ──────────────────────────────
step "production session replay" \
  go test ./internal/ir -run 'TestProductionReplay' -count=1 -v -timeout=600s
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── step 10: concurrency (race detector) ───────────────────────────
step "concurrency (race detector)" \
  go test -race ./internal/ir -run 'TestConcurrent' -count=1 -timeout=600s
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── step 11: 跨模块回归（本工作流未含 ⇒ 追加的更宽回归面）────────────
step "跨模块回归（追加：IR 工作流未覆盖的包）" \
  go test ./internal/ir/... ./internal/protocolformat/... ./internal/paramreg/... \
        ./domains/transformation/... ./domains/streaming/... ./cmd/gateway/... -count=1
[ $FAILED -ne 0 ] && { echo "ABORT: 前序 step 失败"; exit 1; }

# ── 收尾：工作树必须重新变干净（harness 还原的证据）─────────────────
echo ""
echo "=============================================================="
echo " 工作树还原复核（harness 必须把文件改回原样）"
echo "=============================================================="
DIRTY=$(git status --porcelain | wc -l | tr -d ' ')
echo "结束后工作树改动数： $DIRTY"
if [ "$DIRTY" -ne 0 ]; then
  echo "!!! 工作树未还原干净："; git status --porcelain
  FAILED=1
else
  echo "OK: 工作树已还原干净（0 改动）"
fi
echo ""
echo "结束时间： $(date '+%Y-%m-%d %H:%M:%S %z')"
echo "总判定  ： $([ $FAILED -eq 0 ] && echo '全部 step 通过' || echo '存在失败 step')"
echo "=============================================================="
exit $FAILED
