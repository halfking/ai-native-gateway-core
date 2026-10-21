#!/usr/bin/env bash
# verify_cgo_convention_test.sh —— 本机 go 验证口径（cgo=0）的回归门。
#
# ## 它守的是什么
#
# 2026-10-14 实测（本机 macOS SDK 27.0 × CLT linker）：**cgo 形态的 go 验证在本机
# 结构上不可用**。同一 commit、同一台机：
#
#     go build ./...                → RC=1，clang `tapi error: malformed file` /
#                                     `unknown architecture arm64e.x1-macos`
#     CGO_ENABLED=0 go build ./...  → RC=0
#
# 也就是说这台机上「编译红」是宿主工具链的缺口，不是代码回归。以前每轮都要重新踩
# 一遍、再花时间把 clang 报错和真回归分开。这道门把口径钉进 verify.sh：默认
# CGO_ENABLED=0，可用 VERIFY_CGO_ENABLED=1 覆盖（CI 的 Linux 上 cgo 可用，行为不变）。
#
# ## 这道门同时守住两件容易被顺手做坏的事
#
# 1. **不能偷偷缩小验证面**。本机 cgo=0 下 SQLite 真实驱动不编入，那 6 个包的用例
#    在本宿主必然红。 tempting 的做法是在 verify.sh 里把它们剔除/加白名单——但
#    AGENTS.md 的既有策略明写「主门长期为红是维护者决定，不要改 CI 或加白名单」。
#    剔除 = 替维护者决定哪些失败可以不看。所以门钉死：目标集就是 `./...`。
# 2. **cgo 专属名单不能悄悄漂移**。它由依赖闭包算出（`mattn/go-sqlite3`），并与
#    登记名单逐条比对：哪天 sqlite 实现换了（modernc 等），这道门立刻红，逼人重新
#    评估 AGENTS.md 里那段「CGO 边界」与本机口径，而不是安静地变成另一套现实。
#
# ## 为什么静态 grep 不够
#
# - 只钉「注释里有原因」挡不住注释被删：那段注释是踩坑的唯一解释来源。
# - 只钉 export 不带行首锚点会被 `# export CGO_ENABLED=...` 骗过（本轮首版实测假绿，
#   变异 M1 才暴露出来）。
# - 只钉「cgo=0 能编译」会放过「双形态驱动只剩一形态」——那正是 cgo=0 退化成权宜
#   之计的起点。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERIFY="$ROOT/verify.sh"

# cgo 专属包名单（登记值）。动态探测若与之不等 → 依赖图变了，更新本名单与 AGENTS.md
# 的「CGO 边界」一节，并重新评估本机口径。
EXPECTED_CGO_BOUND='bg
cmd/gateway
storage
storage/factory
storage/sqlite
tests/integration'

pass() { printf 'PASS: %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

[[ -f "$VERIFY" ]] || fail "$VERIFY 不存在"
command -v go >/dev/null 2>&1 || fail "PATH 上没有 go —— verify.sh 的 Go 腿同样跑不了，本门无从判断"

# ── 1. 默认口径：Darwin 上钉 cgo=0，Linux/CI 一律不碰 ──────────────────
# 必须锚在行首：`# export CGO_ENABLED=...` 这类注释掉的写法里有同一个子串，
# 无锚点的 grep 会放它过关（本轮首版的假绿，变异 M1 实测）。
grep -qE '^[[:space:]]*export CGO_ENABLED="\$\{VERIFY_CGO_ENABLED:-0\}"[[:space:]]*$' "$VERIFY" \
  || fail 'verify.sh 未钉 export CGO_ENABLED="${VERIFY_CGO_ENABLED:-0}"（macOS 上必须默认 cgo=0，且可用 VERIFY_CGO_ENABLED 覆盖）'
grep -qE '^if \[\[ "\$\(uname -s\)" == "Darwin" \]\]; then$' "$VERIFY" \
  || fail 'verify.sh 的 cgo 口径没有按宿主划界 —— 全局钉 0 会让 Linux/CI 上 6 个 lite/sqlite 包的用例整片红（driver_nocgo.go 按设计 panic）'

# 行为断言：把 verify.sh 里那段守卫原样抽出来跑，验两种宿主 + 覆盖口。
GUARD_BLOCK="$(awk '
  /^if \[\[ "\$\(uname -s\)" == "Darwin" \]\]; then$/ { grab=1 }
  grab { print }
  grab && /^fi$/ { exit }
' "$VERIFY")"
[[ -n "$GUARD_BLOCK" ]] || fail 'verify.sh 里抽不出 Darwin 守卫块（格式变了？）'

cgo_after_guard() {  # $1=uname 的假返回值；其余参数是 KEY=VALUE 预设环境
  local fake_uname="$1"; shift
  (
    # verify.sh exports CGO_ENABLED before invoking this gate. Each fixture
    # must start clean, then apply only its explicit environment presets.
    unset CGO_ENABLED VERIFY_CGO_ENABLED
    local kv
    for kv in "$@"; do export "$kv"; done
    uname() { printf '%s\n' "$fake_uname"; }
    eval "$GUARD_BLOCK"
    printf '%s\n' "${CGO_ENABLED:-<unset>}"
  )
}

got="$(cgo_after_guard Darwin)" \
  || fail "抽取的守卫块在 Darwin 上执行失败：$got"
[[ "$got" == "0" ]] \
  || fail "Darwin 上默认 CGO_ENABLED 应为 0，实得 $got"

got="$(cgo_after_guard Darwin VERIFY_CGO_ENABLED=1)" \
  || fail "抽取的守卫块在 Darwin+cgo 强制下执行失败：$got"
[[ "$got" == "1" ]] \
  || fail "VERIFY_CGO_ENABLED=1 未能让 macOS 上 CGO_ENABLED 变成 1（覆盖口形同虚设），实得 $got"

got="$(cgo_after_guard Linux)" \
  || fail "抽取的守卫块在 Linux 上执行失败：$got"
[[ "$got" == "<unset>" ]] \
  || fail "Linux/CI 上也被钉成 CGO_ENABLED=$got —— CI 的 cgo 可用，改它会让 lite 族整片红，且违反「不要改 CI」"

# The gate itself must work both after verify.sh's default export and after
# VERIFY_CGO_ENABLED=1. Removing fixture isolation must fail even in a clean shell.
for inherited_cgo in 0 1; do
  got="$(export CGO_ENABLED="$inherited_cgo" VERIFY_CGO_ENABLED="$inherited_cgo"; cgo_after_guard Darwin)"
  [[ "$got" == "0" ]] \
    || fail "继承 cgo=$inherited_cgo 污染 Darwin 默认夹具：$got"
  got="$(export CGO_ENABLED="$inherited_cgo" VERIFY_CGO_ENABLED="$inherited_cgo"; cgo_after_guard Linux)"
  [[ "$got" == "<unset>" ]] \
    || fail "继承 cgo=$inherited_cgo 污染 Linux 未设置夹具：$got"
done

got="$(cgo_after_guard Linux CGO_ENABLED=1 VERIFY_CGO_ENABLED=0)"
[[ "$got" == "1" ]] \
  || fail "Linux 守卫改写调用方显式 CGO_ENABLED=1：$got"

# ── 2. 口径必须带着「为什么」一起被钉住 ───────────────────────────────
grep -q 'tapi error' "$VERIFY" \
  || fail 'verify.sh 里的 cgo 口径注释丢了 tapi 错误标识 —— 解释来源被删，后来者会重新把 clang 报错当回归查'
grep -qE 'CGO_ENABLED=0 go build ./\.\.\.' "$VERIFY" \
  || fail 'verify.sh 的 cgo 口径注释丢了「cgo=0 实测 RC=0」这条实测证据'

# ── 3. 验证面不得被顺手缩小（AGENTS.md：不要改 CI 或加白名单） ─────────
grep -qE '^go test \./\.\.\. -count=1' "$VERIFY" \
  || fail 'verify.sh 的全量测试腿不再是 `go test ./...` —— 有人缩小了本机验证面？AGENTS.md 明写不要加白名单'
if grep -nE 'go test[[:space:]]+\$|VERIFY_TEST_TARGETS=|go list \./\.\.\.[[:space:]]*\|' "$VERIFY" >/dev/null \
   || grep -qF 'go test $(' "$VERIFY"; then
  fail 'verify.sh 出现按变量/命令替换拼出来的 go test 目标集 —— 动态剔除等于静默缩小验证面'
fi

# ── 4. 编译结果不得经管道（判据纪律第 11 条：tail 会把 go build 的失败码报成 0）──
if grep -nE '^\s*(go build|go vet|go test)[^|]*\|' "$VERIFY" >/dev/null; then
  fail 'verify.sh 里有 go build/vet/test 经过管道 —— tail 的退出码会盖掉真实失败码'
fi

# ── 5. 行为断言：cgo=0 是**受支持**的构建形态，不是权宜之计 ─────────────
DRV_CGO="$ROOT/storage/sqlite/driver_cgo.go"
DRV_NOCGO="$ROOT/storage/sqlite/driver_nocgo.go"
[[ -f "$DRV_CGO"   ]] || fail "$DRV_CGO 缺失 —— cgo 形态的 SQLite 驱动注册腿不见了"
[[ -f "$DRV_NOCGO" ]] || fail "$DRV_NOCGO 缺失 —— cgo=0 的替身腿不见了，cgo=0 构建会直接编译失败"
grep -q '^//go:build cgo$'   "$DRV_CGO"   || fail 'driver_cgo.go 的 build tag 不是 `//go:build cgo`'
grep -q '^//go:build !cgo$' "$DRV_NOCGO" || fail 'driver_nocgo.go 的 build tag 不是 `//go:build !cgo`'
grep -q 'CGO_ENABLED=0' "$DRV_NOCGO" \
  || fail 'driver_nocgo.go 不再说明「CGO_ENABLED=0 下驱动不可用」—— cgo=0 的行为契约丢了'

if ! CGO_ENABLED=0 go build ./storage/sqlite >/dev/null 2>&1; then
  rc=$?
  fail "CGO_ENABLED=0 go build ./storage/sqlite 失败（rc=${rc}）—— 本机默认口径的地基没了"
fi
pass 'CGO_ENABLED=0 builds storage/sqlite (both driver forms coexist)'

# ── 6. 行为断言：cgo 专属面的动态探测与登记名单一致（漂移报警） ───────────
# 探测集 = （传递依赖 mattn/go-sqlite3 的包）∩（go list ./... 的真实包全集）。
# 取交集是必须的：`go list -test` 还会吐出 `storage_test` 这类测试变体包名。
# Inspect the cgo graph even when verify.sh builds without cgo; otherwise
# driver_cgo.go is excluded and its registered dependency boundary looks empty.
# go list resolves dependencies here without compiling or linking cgo binaries.
raw="$(mktemp)"; all="$(mktemp)"
detected="$(cd "$ROOT" && {
  CGO_ENABLED=1 go list -e -test -f '{{.ImportPath}}|{{range .Deps}}{{.}} {{end}}' ./... 2>/dev/null \
    | grep 'mattn/go-sqlite3' | cut -d'|' -f1 \
    | sed 's/ \[.*//; s/\.test$//' | grep -v '\.test$' \
    | sed 's#^github.com/kaixuan/llm-gateway-go/##' | LC_ALL=C sort -u > "$raw" || true
  CGO_ENABLED=1 go list ./... | sed 's#^github.com/kaixuan/llm-gateway-go/##' | LC_ALL=C sort -u > "$all"
  LC_ALL=C comm -12 "$all" "$raw"
})"
rm -f "$raw" "$all" >/dev/null 2>&1 || true

[[ -n "$detected" ]] \
  || fail '动态探测得到空集合 —— cgo 边界已不存在（sqlite 实现可能已换），AGENTS.md 的 CGO 边界一节需重评'
if [[ "$detected" != "$EXPECTED_CGO_BOUND" ]]; then
  fail "cgo 专属面与登记名单不一致。
  实际（探测）:
$(printf '%s\n' "$detected" | sed 's/^/    /')
  登记（本文件 EXPECTED_CGO_BOUND）:
$(printf '%s\n' "$EXPECTED_CGO_BOUND" | sed 's/^/    /')
  → 更新本名单与 AGENTS.md 的「CGO 边界」，并重新评估本机 cgo=0 口径。"
fi
pass "cgo boundary matches registry ($(printf '%s\n' "$detected" | grep -c '') packages)"

# ── 7. 行为断言：export 真的在任何 go 调用之前生效 ──────────────────────
head -60 "$VERIFY" | grep -q 'export CGO_ENABLED=' \
  || fail 'verify.sh 的 export CGO_ENABLED 不在前 60 行内 —— 可能在某个 go 调用之后才生效'

echo 'OK: verify cgo convention contract'
