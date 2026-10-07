#!/usr/bin/env bash
# =====================================================================
# tests/deploy_seamless_mobile_surface_test.sh
#
# 契约：stage_mobile_surface（scripts/deploy-seamless.sh）在 web-mobile/dist
# 缺失时的三路判定。
#
# 背景：原实现在 dist 缺失时只 warn 就继续，于是 release 里带一个**空**
# web-mobile/ 目录；网关侧 NewMobileStaticHandler 启动探测一次拿不到就返回 nil，
# /m 与统一入口分流都不注册 —— 发布日志一片绿，手机用户却悄悄回到桌面页。
# 2485 就是这样把移动端下线掉的。
#
# 三条用例各锁一个格子：
#   1. dist 存在            → 拷贝、rc=0、不 warn
#   2. 缺失 + --no-frontend → rc=0 **且** warn（预期语义，不是静默）
#   3. 缺失 + 构建跑过      → rc=1（缺陷形态）
#
# ★ 第 2 条是**反向格子**：把判定写成无条件 exit 1 会拦死所有后端专用发布，
#   那条必须因此转红。否则这条契约就只是把「静默失败」换成「误伤合法发布」。
#
# 判据跑的是从 deploy-seamless.sh 里抽出来的**真实函数体**，不是字符串断言。
# 抽取用函数名锚定的 `^stage_mobile_surface() {` ... `^}` —— bash 函数以行首
# `}` 结束，这是精确定界；不靠括号配平猜边界（那种抽法在本仓已错过三次，
# 每次都静默取到错块）。
# =====================================================================

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="$REPO_ROOT/scripts/deploy-seamless.sh"

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); echo "  ok    $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  FAIL  $1"; }

extract_fn() {
  awk '/^stage_mobile_surface\(\) \{$/,/^\}$/' "$TARGET"
}

# run_case <名字> <期望rc> <SKIP_FRONTEND> <是否造 dist>
# 跑在 mktemp -d 里，返回体落在 $WORK 供断言。
run_case() {
  local name=$1 want_rc=$2 skip=$3 make_dist=$4
  WORK="$(mktemp -d)"
  (
    cd "$WORK" || exit 99
    if [[ "$make_dist" == "yes" ]]; then
      mkdir -p web-mobile/dist/assets
      echo '<html>' > web-mobile/dist/index.html
      echo 'x' > web-mobile/dist/assets/app.js
    fi
    SKIP_FRONTEND="$skip"
    warn() { echo "WARN: $*" >>"$WORK/calls.log"; }
    err()  { echo "ERR:  $*" >>"$WORK/calls.log"; }
    ok()   { echo "OK:   $*" >>"$WORK/calls.log"; }
    # shellcheck disable=SC1090
    eval "$(extract_fn)"
    stage_mobile_surface "$WORK/bundle"
    echo $? >"$WORK/rc"
  ) >/dev/null 2>&1
  RC="$(cat "$WORK/rc" 2>/dev/null || echo 99)"
  CALLS="$(cat "$WORK/calls.log" 2>/dev/null || true)"
  if [[ "$RC" == "$want_rc" ]]; then
    ok "${name}（rc=${RC}）"
  else
    bad "${name}：期望 rc=${want_rc}，实际 rc=${RC}。调用日志：${CALLS:-（无）}"
  fi
}

# ---------------------------------------------------------------------
echo "== stage_mobile_surface 三路判定 =="

run_case "dist 存在：拷贝进 bundle" 0 false yes
if [[ -f "$WORK/bundle/web-mobile/index.html" ]]; then
  ok "dist 存在：index.html 落到 bundle/web-mobile/"
else
  bad "dist 存在：bundle/web-mobile/index.html 不存在"
fi
if [[ -f "$WORK/bundle/web-mobile/assets/app.js" ]]; then
  ok "dist 存在：嵌套 assets/ 一并平铺"
else
  bad "dist 存在：bundle/web-mobile/assets/app.js 不存在"
fi
if [[ "$CALLS" != *WARN* ]]; then
  ok "dist 存在：不发 warn"
else
  bad "dist 存在：不该 warn，实际：$CALLS"
fi

run_case "缺失 + --no-frontend：rc=0（沿用线上是既定语义）" 0 true no
if [[ "$CALLS" == *WARN* ]]; then
  ok "缺失 + --no-frontend：显式 warn，不静默"
else
  bad "缺失 + --no-frontend：应当 warn 却静默了。调用日志：${CALLS:-（无）}"
fi
if [[ "$CALLS" != *ERR* ]]; then
  ok "缺失 + --no-frontend：不误判为硬失败"
else
  bad "缺失 + --no-frontend：被当成硬失败，会拦死后端专用发布。调用日志：$CALLS"
fi

run_case "缺失 + 构建跑过：rc=1（缺陷形态必须拦住）" 1 false no
if [[ "$CALLS" == *ERR* ]]; then
  ok "缺失 + 构建跑过：err 说明为什么拒发"
else
  bad "缺失 + 构建跑过：应当 err。调用日志：${CALLS:-（无）}"
fi

# 契约自身的前提：函数还在被调用点使用，且调用点没有把返回值丢掉。
CALL_SITE="$(grep -c 'stage_mobile_surface "\$bundle_dir" || exit 1' "$TARGET")"
if [[ "$CALL_SITE" -ge 1 ]]; then
  ok "调用点检查了返回值（|| exit 1）"
else
  bad "调用点没有检查返回值 —— 函数会跑但失败被忽略"
fi

echo
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]