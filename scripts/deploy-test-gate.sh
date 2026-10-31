#!/usr/bin/env bash
# requires-bash: 4
# ↑ declare -A（137-138 行）；Linux 侧自然满足，macOS 侧用 ~/sdk/bash5/bin/bash 跑。
# =====================================================================
# scripts/deploy-test-gate.sh — 部署前的**测试闸门** v2（2026-10-30 修正轮）
# =====================================================================
# ★ 为什么要有这个闸门（★ 审计轮的实测发现，★ 不是假想）★★
#
#  部署脚本**只编译、不跑测试**（★ `docker build` 会因编译错误失败 ⇒ 语法
#   门是有的；★ 但**没有**任何一步跑 `go test`）⇒ ★★ **红灯也能上线** ★★
#
#   ★ 实证：★ 2026-10-29「输出合规误报轮」把守卫的越界处置从「阻断」改成
#   「降级放行」⇒ ★★ 而 2026-10-02 存储审计立的那条容量契约测试
#   （`security/sanitize/stream_capacity_audit_test.go`）**一直是红的** ★★
#   ⇒ ★★ 那条测试守的正是「越界不再杀请求」这条修复的**另一半**（★ 缓冲有界
#   + 降级放行）★★ ⇒ ★★ 如果部署跑测试，这一轮会在上线前就停住 ★★
#
# ★ v1（2026-10-29 首版）的四个实测缺陷 —— 2026-10-30 部署实测全部踩中 ★★
#
#   ① **零输出假死**：全量测试输出被 `$(...)` 捕获 ⇒ 跑完之前终端一个字都
#      没有；而 docker 冷编译（GOCACHE 在容器 /tmp + `--rm` ⇒ 每次部署都是
#      全冷）8 个包要 10~20 分钟 ⇒ **部署看起来像死住了**（用户实测反馈
#      「系统反应不正常」的根因）。
#   ② **冷缓存税**：`-e GOCACHE=/tmp/gocache`（容器内临时层）+ `--rm` ⇒
#      每次部署把整个依赖树重新编译一遍 ⇒ 闸门永远慢。
#   ③ **BASE_REF 未定义**：脚本体引用 `$BASE_REF` 但从未赋值 ⇒ 只要当前树
#      有红走基线臂，`set -u` 当场崩在 unbound variable，报错与原因完全脱钩。
#   ④ **两个伪绿洞**：(a) docker 自身失败（daemon 挂/挂载失败）⇒ 输出为空 ⇒
#      「0 个 --- FAIL 行」被判成**全绿**；(b) 包**编译失败**只打
#      `FAIL pkg [build failed]`、没有 `--- FAIL:` 行 ⇒ 同样被判成全绿。
#      ★ 闸门最坏的失败方式就是把「没跑成」报告成「绿」。
#
# ★ v2 修法 ★
#
#   · **原生 go 优先**：本机 go（~/sdk/go 等）+ GOTOOLCHAIN=local（旧 go 会
#     快速失败落 docker，**绝不**在线下载工具链）+ `-mod=vendor` ⇒ 离线、
#     复用本机 48G 热构建缓存 ⇒ **分钟级**（实测 8 包 3.6 分钟）。
#   · **docker 兜底**：没有可用 go 时才走；GOCACHE 挂**持久命名卷**
#     （默认 `llm-gw-go-gocache`）⇒ 只有第一次冷。
#   · **流式输出**：`tee` 直通终端，go test 逐包打 `ok/FAIL`，进度可见。
#   · **全覆盖断言**：`go list` 拿全量被测包清单 ⇒ 每个包必须有结果行，
#     缺任何一行 ⇒ 按**失败**处理（宁可红报，不许伪绿）。
#   · **红 = 包级 FAIL 行 ∪ 测试级 --- FAIL 名**：编译失败/进程崩溃/超时
#     都落进红名单，不再可能被「0 个 --- FAIL 行」洗成绿。
#   · `BASE_REF` 缺省 `origin/main`（可用环境变量覆盖）。
#
# ★ 逃生舱：★ `DD_SKIP_TEST_GATE=1` 跳过闸门（★ 必须**显式**给，★ 不允许
#   「闸门自己默默跳过」——★ 那样又是一次静默降级）★★
#
# 用法:
#   bash scripts/deploy-test-gate.sh              # 按 diff 选包跑测试
#   DD_SKIP_TEST_GATE=1 bash scripts/deploy-test-gate.sh   # 显式跳过
# =====================================================================
# ★★ r1048 轮（★2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★目录约定与6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒也不要把
# ★★ GOCACHE设成垃圾值 ⇒ ⇒ 那会让 go直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
 __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
 . "$__gocache_dir/scripts/lib/gocache.sh"
 gocache_use script
fi

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ★ 闸门用的 Go 基础镜像（docker 兜底路径用）。
GO_IMAGE="${DD_TEST_GO_IMAGE:-golang:1.27-alpine}"
# ★ 原生 go 的路径；留空自动探测（PATH → ~/sdk/go → /usr/local/go → homebrew）。
NATIVE_GO="${DD_TEST_GO_BIN:-}"
# ★ go test 自身的测试超时（防单个测试挂死）；docker 侧用秒。
TEST_TIMEOUT="${DD_TEST_TIMEOUT:-15m}"
DOCKER_TIMEOUT_SECS="${DD_TEST_DOCKER_TIMEOUT_SECS:-1800}"
CACHE_VOLUME="${DD_TEST_CACHE_VOLUME:-llm-gw-go-gocache}"
# ★ 基线参照（★ v1 缺陷③：引用了但从未定义 ⇒ 有红必 unbound 崩）★
BASE_REF="${BASE_REF:-origin/main}"

# ★ 回落清单：★ diff 为空（★ 例如从干净树部署）时跑这些核心包。
FALLBACK_PACKAGES=(
  ./admin/
  ./domains/sanitizerules/
  ./domains/hooks/outputcompliance/
  ./security/sanitize/
)

log() { printf '[test-gate] %s\n' "$*" >&2; }
die() { printf '[test-gate] FAIL: %s\n' "$*" >&2; exit 1; }

if [[ "${DD_SKIP_TEST_GATE:-0}" == "1" ]]; then
  log "★ 闸门被显式跳过（DD_SKIP_TEST_GATE=1）★ —— 这次上线**没有**测试证据"
  exit 0
fi

cd "$REPO_ROOT"

# ── 选包：改动触及的目录（只留有 _test.go 的） ──────────────────────────
changed_files="$({
  git diff --name-only HEAD 2>/dev/null || true
  git diff --name-only --cached 2>/dev/null || true
  git diff --name-only HEAD~1 HEAD 2>/dev/null || true
} | sort -u)"

packages=()
if [[ -n "$changed_files" ]]; then
  dirs="$(printf '%s\n' "$changed_files" | grep '\.go$' | xargs -n1 dirname 2>/dev/null | sort -u || true)"
  while IFS= read -r dir; do
    [[ -n "$dir" ]] || continue
    if compgen -G "$dir/*_test.go" >/dev/null 2>&1; then
      packages+=("./$dir")
    fi
  done <<< "$dirs"
fi

if [[ ${#packages[@]} -eq 0 ]]; then
  log "★ diff 没有触及带测试的包 ⇒ 回落核心包清单 ★"
  packages=("${FALLBACK_PACKAGES[@]}")
fi

log "要跑的包：${packages[*]}"

# ── 嵌套 Go 模块分箱（★ 2026-10-30 部署实测缺陷）────────────────────────
# installer/ 自带 go.mod ⇒ 是嵌套模块，从主模块根 `go test ./installer/...`
# 必然 setup failed（"main module does not contain package"）⇒ ① 触及
# installer 的提交会让干净树部署卡死在闸门；② assert_full_coverage 的
# go list 也在主模块根跑 ⇒ 同样拿不到 installer 的包清单 ⇒ 走「读数不全」
# 红报。修法 = 按模块根分箱：嵌套模块的包**在其模块根**下跑（GOFLAGS 按
# 树内有无 vendor 决定：installer 无 vendor ⇒ -mod=mod 走模块缓存），覆盖
# 断言同源同根。响亮记录分箱结果，不许静默跳过（那会把闸门降级成摆设）。
main_packages=()
nested_modules=()   # 去重后的嵌套模块根列表，如 installer
declare -A nested_seen=()
declare -A nested_pkgs=()
for p in "${packages[@]}"; do
  dir="${p#./}"; dir="${dir%/}"
  modroot="$dir"
  while [[ -n "$modroot" && "$modroot" != "." && ! -f "$REPO_ROOT/$modroot/go.mod" ]]; do
    modroot="$(dirname "$modroot")"
  done
  if [[ -z "$modroot" || "$modroot" == "." || ! -f "$REPO_ROOT/$modroot/go.mod" ]]; then
    main_packages+=("$p")
  else
    if [[ -z "${nested_seen[$modroot]+x}" ]]; then
      nested_seen[$modroot]=1
      nested_modules+=("$modroot")
    fi
    nested_pkgs[$modroot]+=" ./${dir#"$modroot"/}"
  fi
done
packages=("${main_packages[@]}")
if [[ ${#nested_modules[@]} -gt 0 ]]; then
  log "★ 嵌套模块分箱：${nested_modules[*]} ⇒ 各自模块根下跑（不从主模块根）★"
fi

# ── 工作目录与清理 ────────────────────────────────────────────────────
workdir="$(mktemp -d)"
base_tree=""
cleanup() {
  if [[ -n "$base_tree" && -d "$base_tree" ]]; then
    git worktree remove --force "$base_tree" >/dev/null 2>&1 || true
  fi
  rm -rf "$workdir" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── 原生 go 探测 ──────────────────────────────────────────────────────
native_ok() {
  if [[ -n "$NATIVE_GO" ]]; then
    [[ -x "$NATIVE_GO" ]] || return 1
    return 0
  fi
  local c
  for c in "$(command -v go 2>/dev/null)" "$HOME/sdk/go/bin/go" \
           /usr/local/go/bin/go /opt/homebrew/bin/go; do
    if [[ -n "$c" && -x "$c" ]]; then
      NATIVE_GO="$c"
      return 0
    fi
  done
  return 1
}

# modflag_for <tree> <subdir>：树内该子目录有 vendor ⇒ -mod=vendor（主模块
# 惯例）；没有 ⇒ -mod=mod（嵌套模块如 installer 无 vendor，走本机模块缓存）。
modflag_for() {
  if [[ -d "$1/${2:-}/vendor" ]]; then echo "-mod=vendor"; else echo "-mod=mod"; fi
}

# ── 跑法一：原生 go（热缓存 ⇒ 分钟级；GOTOOLCHAIN=local ⇒ 绝不在线拉工具链）──
run_native() {
  local tree="$1" outfile="$2" subdir="$3"; shift 3
  local modflag; modflag="$(modflag_for "$tree" "$subdir")"
  log "跑法：原生 go（$(GOTOOLCHAIN=local "$NATIVE_GO" version 2>/dev/null | awk '{print $3}')，复用本机构建缓存；超时 ${TEST_TIMEOUT}；${subdir:-主模块根} ${modflag}）"
  log "首个包出结果前有 1~2 分钟编译静默，属 go test 正常形态（不是卡死）…"
  (
    cd "$tree${subdir:+/$subdir}" &&
    GOFLAGS="$modflag" GOTOOLCHAIN=local "$NATIVE_GO" test -count=1 -timeout "$TEST_TIMEOUT" "$@"
  ) 2>&1 | tee "$outfile"
  # 红绿由结果行判定，不按退出码（红退出码非 0 是**预期**，不能让 set -e 杀掉解析）。
}

# ── 跑法二：docker 兜底（GOCACHE 持久命名卷 ⇒ 只有第一次冷） ────────────
run_docker() {
  local tree="$1" outfile="$2" subdir="$3"; shift 3
  local modflag; modflag="$(modflag_for "$tree" "$subdir")"
  log "跑法：docker（${GO_IMAGE}；GOCACHE 用持久卷 ${CACHE_VOLUME} ⇒ 仅首次冷编译；容器内超时 ${DOCKER_TIMEOUT_SECS}s；${subdir:-主模块根} ${modflag}）"
  docker run --rm \
    -v "$tree":/w -w "/w${subdir:+/$subdir}" \
    -v "$CACHE_VOLUME":/tmp/gocache \
    -e GOFLAGS="$modflag" -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
    "$GO_IMAGE" \
    sh -c "timeout $DOCKER_TIMEOUT_SECS go test -count=1 $*" 2>&1 | tee "$outfile"
}

# ── 判定原语 ──────────────────────────────────────────────────────────
# readable_results：输出里至少有一行包级结果（ok / FAIL / ? [no test files]）。
readable_results() { grep -qE '^(ok|FAIL|\?)' "$1"; }

# red_names：红名单 = 测试级 `--- FAIL:` 名 ∪ 包级 `FAIL <pkg>` 行。
# ★ 包级 FAIL 行是权威：`[build failed]`、测试进程 panic、超时击杀都只有
#   这一行 ⇒ v1 只认 `--- FAIL:` 的口径会把它们全部洗成「全绿」。
red_names() {
  {
    sed -n 's/^ *--- FAIL: \([^ ]*\).*/\1/p' "$1"
    awk '$1 == "FAIL" && NF > 1 { print $2 }' "$1"
  } | sort -u
}

# assert_full_coverage <outfile> <native|docker> <tree> <subdir> [packages...]
# 每个被选中的包都必须有结果行，缺任何一行 ⇒ 失败。
# ★ 这是伪绿的主闸：runner 半途夭折（OOM/超时/断连）时「已有行」都是绿的 ⇒
#   只看红行会把「没跑完」读成「全绿」。
# ★ go list 与 go test 必须同根同 mod 标志（嵌套模块在它自己的模块根 list，
#   -mod=mod）⇒ 要不然主模块根拿不到嵌套模块的包清单 ⇒ 误报「读数不全」。
assert_full_coverage() {
  local outfile="$1" mode="$2" tree="$3" subdir="$4"; shift 4
  local import_paths
  if [[ "$mode" == "native" ]]; then
    import_paths="$( cd "$tree${subdir:+/$subdir}" && \
      GOTOOLCHAIN=local GOFLAGS="$(modflag_for "$tree" "$subdir")" "$NATIVE_GO" list "$@" 2>/dev/null || true )"
  else
    import_paths="$(docker run --rm -v "$tree":/w -w "/w${subdir:+/$subdir}" \
      -e GOFLAGS="$(modflag_for "$tree" "$subdir")" "$GO_IMAGE" go list "$@" 2>/dev/null || true)"
  fi
  if [[ -z "$import_paths" ]]; then
    die "go list 拿不到被测包清单（根=${tree}${subdir:+/$subdir}）⇒ 覆盖断言做不成 ⇒ 按失败处理"
  fi
  local p missing=0
  while IFS= read -r p; do
    [[ -n "$p" ]] || continue
    if ! grep -Eq "^(ok|FAIL|\?)[[:space:]]+${p}([[:space:]]|\[|$)" "$outfile"; then
      log "★ 包 $p 没有任何结果行 ⇒ 读数不全"
      missing=1
    fi
  done <<< "$import_paths"
  if [[ $missing -ne 0 ]]; then
    printf '%s\n' '—— 测试输出尾部 ——' >&2
    tail -15 "$outfile" >&2
    die "测试读数不全（有包没有结果行）⇒ 宁可红报，不许伪绿（★ v1 缺陷④）"
  fi
}

# ── 跑一棵树：主模块 + 每个嵌套模块各跑一次，输出合并、逐箱覆盖断言 ──
# 用法：run_tree <tree> <outfile> <native|docker>   （native|docker 由调用方
# 已定的跑法决定；当前树与基线树共用。）
run_tree() {
  local tree="$1" outfile="$2" mode="$3" box sub
  : > "$outfile"
  for box in "" "${nested_modules[@]}"; do
    sub="$box"
    local -a pkgs=()
    if [[ -z "$box" ]]; then
      pkgs=("${packages[@]}")
      [[ ${#pkgs[@]} -eq 0 ]] && continue
    else
      read -r -a pkgs <<< "${nested_pkgs[$box]}"
    fi
    if [[ "$mode" == native ]]; then
      run_native "$tree" "$outfile.part" "$sub" "${pkgs[@]}" || true
    else
      run_docker "$tree" "$outfile.part" "$sub" "${pkgs[@]}" || true
    fi
    if ! readable_results "$outfile.part"; then
      log "★ ${box:-主模块}：该箱没有产出可判读的结果 ⇒ ${mode} 跑法不可用"
      return 1
    fi
    assert_full_coverage "$outfile.part" "$mode" "$tree" "$sub" "${pkgs[@]}"
    cat "$outfile.part" >> "$outfile"
  done
}

# ── 主跑：当前树 ──────────────────────────────────────────────────────
out_current="$workdir/current.txt"
use_native=0

if native_ok; then
  if run_tree "$REPO_ROOT" "$out_current" native; then
    use_native=1
  else
    log "★ 原生 go 没有产出可判读的结果 ⇒ 落 docker 兜底（原生输出尾部：$(tail -2 "$out_current" | tr '\n' ' ')）"
  fi
else
  log "★ 本机没有可用的 go ⇒ docker 兜底 ★"
fi

if [[ $use_native -ne 1 ]]; then
  if ! docker image inspect "$GO_IMAGE" >/dev/null 2>&1; then
    log "拉取测试镜像 $GO_IMAGE ..."
    docker pull "$GO_IMAGE" || die "测试镜像不可用（${GO_IMAGE}）⇒ 闸门做不成。★ 要跳过请显式给 DD_SKIP_TEST_GATE=1（★ 那次上线将没有测试证据）★"
  fi
  if ! run_tree "$REPO_ROOT" "$out_current" docker; then
    printf '%s\n' '—— 测试输出尾部 ——' >&2
    tail -15 "$out_current" >&2
    die "docker 测试没有产出可判读的结果 ⇒ 宁可红报，不许伪绿（★ v1 缺陷④a）"
  fi
fi

current_reds="$(red_names "$out_current")"

if [[ -z "$current_reds" ]]; then
  log "★ 闸门通过：${packages[*]} 全绿 ★"
  exit 0
fi

log "★ 当前树有红（$(printf '%s' "$current_reds" | tr '\n' ' ')）★ ⇒ 比对基线（${BASE_REF}）"

# ── 基线树（★ 只在有红时才跑）────────────────────────────────────────
base_commit="$(git merge-base HEAD "$BASE_REF" 2>/dev/null || true)"
if [[ -z "$base_commit" ]]; then
  printf '%s\n' '—— 当前树输出尾部 ——' >&2
  tail -40 "$out_current" >&2
  die "有红灯且拿不到基线（git merge-base HEAD $BASE_REF 失败）⇒ 无法区分新旧红灯 ⇒ 拒绝上线"
fi
base_tree="$(mktemp -d)/base"
git worktree add --detach "$base_tree" "$base_commit" >/dev/null 2>&1 || {
  die "★ 建基线 worktree 失败 ⇒ 无法区分新旧红灯 ⇒ 拒绝上线 ★"
}

out_base="$workdir/base.txt"
base_use_native=0
if [[ $use_native -eq 1 ]]; then
  if run_tree "$base_tree" "$out_base" native; then
    base_use_native=1
  else
    log "★ 基线树原生跑法没有产出可判读结果 ⇒ docker 兜底"
  fi
fi
if [[ $base_use_native -ne 1 ]]; then
  if ! run_tree "$base_tree" "$out_base" docker; then
    printf '%s\n' '—— 基线输出尾部 ——' >&2
    tail -15 "$out_base" >&2
    die "基线树测试没有产出可判读的结果 ⇒ 无法区分新旧红灯 ⇒ 拒绝上线"
  fi
fi

base_reds="$(red_names "$out_base")"

new_reds="$(comm -23 <(printf '%s\n' "$current_reds") <(printf '%s\n' "$base_reds") | grep -v '^$' || true)"

if [[ -n "$base_reds" ]]; then
  log "★ 基线（${base_commit}）**本来就红**：$(printf '%s' "$base_reds" | tr '\n' ' ')★"
  log "★   ⇒ 这些红灯**不是**本次改动带来的（★ 告警，不拦）★"
fi

if [[ -z "$new_reds" ]]; then
  log "★ 闸门通过：没有**新红**（★ 现有红灯均为基线既有）★"
  exit 0
fi

log "★ **新红**（本次改动带来的）：$(printf '%s' "$new_reds" | tr '\n' ' ')★"
grep -E "^(--- FAIL|    --- FAIL|FAIL|ok)" "$out_current" | head -30 >&2 || true
die "★ 新红 ⇒ 拒绝上线（★ 红灯上线过一次：★ 2026-10-29 的容量契约测试）★"
