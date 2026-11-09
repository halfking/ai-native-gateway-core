#!/usr/bin/env bash
# tests/shared_lib_resolution_contract_test.sh —— scripts/_shared-lib.sh 的行为门。
#
# ★ 2026-11-04 审计轮（F-15）新增；★ 同日**第三轮**加固（★ 由变异测试驱动的重构 ★）。
# ★ 2026-11-09 审计轮（F51）适配：★ 候选链搬到了【★ 共享的唯一真相文件 ★】
#   scripts/deploy-lib-candidates.sh ⇒ ★ 本门改为「★ 两个实现都认 ★」
#   （★ 新实现读共享真相、★ 旧实现读自带函数 ★）⇒ ★ 变异测试仍可拿旧实现当对照 ★。
# ★ 本门钉住「共享部署库入口」的解析与失败契约：
#   ①  显式 AIAN_DEPLOY_LIB 有效 ⇒ 解析到它，且**已 export**（子进程可见）；
#   ②  显式值指向**不存在**的目录 ⇒ rc=64 + `missing deploy-lib SSOT` + `fix:` 提示；
#   ③  显式值指向**存在但不是 SSOT** 的目录 ⇒ 同 ②；
#   ④  ★ **逐候选**：★ 每个候选**单独**存在时都必须解析到它
#       ⇒ ★ 证明解析器【★ 真的 ★】会试每一个候选（★ 而不只是第一个 ★）；
#   ⑤  全部候选都存在 ⇒ 解析到**第一个**（顺序语义）；
#   ⑥  ★ 只有**靠后**的候选存在 ⇒ 解析到它（回退语义、★ 不挑第一个不存在的 ★）；
#   ⑦  无显式值且无任何候选 ⇒ rc=64 + `missing AIAN_DEPLOY_LIB`；
#   ⑧  **被 source 且调用方没有 set -e** ⇒ 仍须以 64 中止
#       （★ F-15 的核心回归点：旧写法只从被 source 的文件返回，调用方继续跑）；
#   ⑨  直接执行本文件 ⇒ rc=64；
#   ⑩  ★★ **无漂移断言**：★ 失败文案里列出的布局集合 **==** 候选链集合
#       ⇒ ★ 「★ 文案让人去 clone 的目录 ★」**必须**是「★ 解析器真的会试的目录 ★」；
#   ⑪  ★ 实现必须暴露【★ 候选链的单一真相 ★】且候选数 ≥ 2
#       ⇒ ★ 「★ 抽掉单一真相 ⇒ 红 ★」这一态**可被构造**（★ 旧版构造不出来 ★）。
#
# ★ ★ 本门【★ 一个布局名都不写死 ★】：★ 夹具由**实现的候选链**推导。
#   ★ 为什么：★ 旧版把两个布局**写死**在 ④⑤⑥ ⇒ ★ 候选一改名、★ 门就**假红**
#   （★ 而假红会让人把门"改绿"而不是把实现改对 ★）⇒ ★ 现在【★ 改布局名不假红 ★】。
#
# ★ ★ 2026-11-09（F51）新增的一条纪律：**★ 实现必须跑在隔离目录里 ★**
#   ★ 为什么：★ 候选链的第一项是【★ 仓内软链 ★】（<仓>/scripts/deploy-lib）
#     ⇒ ★ 而它的路径是【★ 由实现所在目录推导 ★】的 ⇒ ★ 若直接跑真实实现，
#     ★ 本门就会在【★ 真实 SSOT 目录里 ★】写夹具文件（★ 污染生产 SSOT ★）
#     ⇒ ★ 而且「每个候选单独存在」的矩阵会被真实软链**抢先命中** ⇒ 假红/假绿 ★
#   ★ ⇒ ★ 实现与它的「唯一真相」文件一起复制到隔离目录 ⇒ ★ 软链候选自然缺席 ★
#
# ★ 为什么要有这道门：★ 本仓的纪律是「门没有调用方 = 声明不是门」，
#   而 `_resolve_aian_deploy_lib` 在本门出现之前**没有任何机器覆盖**
#   ⇒ ★ 兄弟解析器 deploy-lib-resolve.sh 当时有 .verify-deploy-lib-resolution.sh，
#   本文件没有 ⇒ ★ 护栏不对称（F-15 的一部分）。
#   ★ 2026-11-09（F51）：★ 那份孤儿脚本已**删除**（★ 无调用方 ⇒ 不是门 ★）
#   ⇒ ★ 现在两个解析器的对拍由 tests/deploy_lib_candidate_chain_single_source_test.sh
#   ★ **对称覆盖**（★ 本门只钉 ① ★）★。
#
# ★ 零外部依赖、零仓库副作用（全部在 mktemp 里）⇒ 可进 CI 快门。
# ★ 可选参数 $1：★ 把门指向**候选**实现 ⇒ 「这个改动能不能过门」可以在提交前自测、
#   ★ 变异测试（★ 造一个坏实现看门会不会红 ★）也用它。
# Run: bash tests/shared_lib_resolution_contract_test.sh [candidate-implementation-path]
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB_SRC="${1:-$REPO_ROOT/scripts/_shared-lib.sh}"
[[ -f "$LIB_SRC" ]] || { echo "missing $LIB_SRC" >&2; exit 1; }

TMP="$(mktemp -d)"
# ★ 解析成真实路径（macOS 上 /var ⇒ /private/var）⇒ ★ 夹具路径与实现算出的
#   候选路径【★ 同一个字符串形态 ★】⇒ ★ 清理与比对都对得上 ★
TMP="$(cd "$TMP" && pwd -P)"
trap 'rm -rf "$TMP"' EXIT

# ★ 实现跑在隔离目录里（★ 见文件头的纪律说明 ★）
IMPL_DIR="$TMP/impl"
mkdir -p "$IMPL_DIR"
cp "$LIB_SRC" "$IMPL_DIR/_shared-lib.sh"
CANON_SRC="$REPO_ROOT/scripts/deploy-lib-candidates.sh"
[[ -f "$CANON_SRC" ]] && cp "$CANON_SRC" "$IMPL_DIR/deploy-lib-candidates.sh"
LIB="$IMPL_DIR/_shared-lib.sh"

PASS=0
FAIL=0
check() { # <name> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    PASS=$((PASS + 1)); printf 'OK   %s\n' "$1"
  else
    FAIL=$((FAIL + 1)); printf 'FAIL %s: want [%s] got [%s]\n' "$1" "$2" "$3" >&2
  fi
}
check_has() { # <name> <needle> <haystack>
  case "$3" in
    *"$2"*) PASS=$((PASS + 1)); printf 'OK   %s\n' "$1" ;;
    *) FAIL=$((FAIL + 1)); printf 'FAIL %s: missing [%s] in [%s]\n' "$1" "$2" "$3" >&2 ;;
  esac
}
check_lacks() { # <name> <needle> <haystack>
  case "$3" in
    *"$2"*) FAIL=$((FAIL + 1)); printf 'FAIL %s: unexpected [%s] in [%s]\n' "$1" "$2" "$3" >&2 ;;
    *) PASS=$((PASS + 1)); printf 'OK   %s\n' "$1" ;;
  esac
}

# 造一个「可用的 SSOT」：_shared-lib.sh 会 source 下面这两个文件。
mk_ssot() {
  mkdir -p "$1"
  printf '# fixture\n' > "$1/deploy-prereqs.sh"
  printf '# fixture\n' > "$1/deploy-image-resolution.sh"
  printf '# fixture\n' > "$1/parse-wrapper-flags.sh"
}

# ★ ★ 取实现用的【★ 候选链单一真相 ★】那个函数（★ 连函数头一起取 ★）。
#   ★ 取不到 ⇒ 实现没有单一真相 ⇒ ⑪ 判红（★ 这就是「★ 抽掉单一真相 ⇒ 红 ★」★）。
#   ★ 刻意**不**在门里写死任何布局名 ⇒ 布局改名时本门不会假红。
# ★ 2026-11-09（F51）：★ 候选链搬到了【★ 共享的唯一真相文件 ★】⇒ ★ 本门两个都认：
#   ★   共享真相在 ⇒ 用它（★ 现在唯一的真相 ★）；★ 否则回退到实现自带函数（★ 旧实现 ★）。
CHAIN_FILE=""
CHAIN_FN=""
if [[ -f "$IMPL_DIR/deploy-lib-candidates.sh" ]] \
   && grep -q '^deploy_lib_candidates() {' "$IMPL_DIR/deploy-lib-candidates.sh"; then
  CHAIN_FILE="$IMPL_DIR/deploy-lib-candidates.sh"
  CHAIN_FN="deploy_lib_candidates"
else
  _own_fn="$(sed -n '/^_shared_lib_candidates() {/,/^}/p' "$LIB")"
  if [[ -n "$_own_fn" ]]; then
    CHAIN_FILE="$LIB"
    CHAIN_FN="_shared_lib_candidates"
  fi
fi
CAND_FN="$( [[ -n "$CHAIN_FILE" ]] && sed -n "/^${CHAIN_FN}() {/,/^}/p" "$CHAIN_FILE" )"

# ★ 在【★ 指定 HOME ★】下展开候选链（★ 实现里用的是 $HOME ⇒ 展开结果随 HOME 变 ★）。
#   ★ 剥掉可能的「|<说明标签>」后缀：★ 门只关心**路径** ⇒ ★ 这条剥除式对
#   ★ 「只吐路径」与「吐 path|label」两种实现都成立 ⇒ ★ 门不锁死实现格式。
candidates_for() { # <HOME 根>
  [[ -n "$CAND_FN" ]] || return 1
  HOME="$1" bash -c '
    set +e
    [[ -f "$1" ]] && source "$1"
    "$2"
  ' _ "$CHAIN_FILE" "$CHAIN_FN" 2>/dev/null | sed 's/|[^|]*$//'
}

NEUTRAL_HOME="$TMP/home-neutral"
mkdir -p "$NEUTRAL_HOME"
CANDS="$(candidates_for "$NEUTRAL_HOME")"
NCAND="$(printf '%s\n' "$CANDS" | awk 'NF{c++} END{print c+0}')"

# ★ 清掉所有候选夹具 ⇒ ★ 「★ 只有这一个候选存在 ★」才是真的 ★
#   ★ 只删 $TMP 下的（★ 真实检出的软链候选不在 $TMP 下 ⇒ 永远不会被删 ★）
clear_candidate_fixtures() {
  local c
  while IFS= read -r c; do
    [[ -n "$c" ]] || continue
    case "$c" in
      "$TMP"/*) rm -rf "$c" ;;
    esac
  done <<< "$CANDS"
}

# ★ ⑪a 实现必须暴露候选链的单一真相。
if [[ -n "$CAND_FN" ]]; then
  PASS=$((PASS + 1)); printf 'OK   ⑪a implementation exposes a single candidate source (%s)\n' "$CHAIN_FN"
else
  FAIL=$((FAIL + 1)); printf 'FAIL ⑪a implementation exposes no candidate source\n' >&2
fi

# ★ ⑪b 候选链至少两个（★ 回退契约：★ 一个候选 = 没有回退 ★）。
# ★ ★ 这里【★ 必须显式判红 ★】而不是「跳过后面那些由链推导的场景」——
#   ★   因为【★ 夹具改成由链推导之后、★ 链为空会让那些断言【★ 一次都不执行 ★】
#   ★   ⇒ 零断言 = 假绿（★ 这正是「★ 空候选 ⇒ 断言零次执行 ⇒ 假绿 ★」那个洞 ★）
#   ★   ⇒ ★ 链为空时必须【★ 显式判红 ★】、★ 且后面的场景【★ 也要显式判红 ★】（★ 见 CHAIN_OK ★）。
CHAIN_OK=1
if [[ "$NCAND" -ge 2 ]]; then
  PASS=$((PASS + 1)); printf 'OK   ⑪b candidate chain has >= 2 entries (%s)\n' "$NCAND"
else
  CHAIN_OK=0
  FAIL=$((FAIL + 1)); printf 'FAIL ⑪b candidate chain must have >= 2 entries, got %s\n' "$NCAND" >&2
fi

EMPTY_HOME="$TMP/home-empty"
mkdir -p "$EMPTY_HOME"

# ① 显式有效值优先，且已 export。
clear_candidate_fixtures
mk_ssot "$TMP/ssot-explicit"
got="$(AIAN_DEPLOY_LIB="$TMP/ssot-explicit" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>"$TMP/err1")"
check "① explicit valid wins" "$TMP/ssot-explicit" "$got"
got="$(AIAN_DEPLOY_LIB="$TMP/ssot-explicit" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; env | grep "^AIAN_DEPLOY_LIB="' _ "$LIB" 2>/dev/null)"
check "①b success path exports AIAN_DEPLOY_LIB" "AIAN_DEPLOY_LIB=$TMP/ssot-explicit" "$got"

# ② 显式值指向不存在的目录。
clear_candidate_fixtures
rc=0
out="$(AIAN_DEPLOY_LIB="$TMP/does-not-exist" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err2")" || rc=$?
check "② explicit missing dir -> rc 64" "64" "$rc"
check_has "②b explicit missing dir -> actionable hint" "missing deploy-lib SSOT" "$(cat "$TMP/err2")"
check_has "②c explicit missing dir -> fix hint" "fix: export AIAN_DEPLOY_LIB=" "$(cat "$TMP/err2")"
check_lacks "②d explicit missing dir -> no downstream ENOENT" "No such file or directory" "$(cat "$TMP/err2")"
check_lacks "②e explicit missing dir -> caller did not continue" "CALLER-CONTINUED" "$out"

# ③ 显式值指向存在但不是 SSOT 的目录。
clear_candidate_fixtures
mkdir -p "$TMP/not-ssot"
rc=0
out="$(AIAN_DEPLOY_LIB="$TMP/not-ssot" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err3")" || rc=$?
check "③ explicit non-SSOT dir -> rc 64" "64" "$rc"
check_has "③b explicit non-SSOT dir -> names the offending path" "$TMP/not-ssot" "$(cat "$TMP/err3")"
check_lacks "③c explicit non-SSOT dir -> no downstream ENOENT" "No such file or directory" "$(cat "$TMP/err3")"
check_lacks "③d explicit non-SSOT dir -> caller did not continue" "CALLER-CONTINUED" "$out"

# ④ ★ 逐候选：★ 每个候选单独存在 ⇒ 解析到它。
#   ★ 这一条【★ 同时证明解析器真的会试每一个候选 ★】⇒ ★ 是 ⑩「文案 == 实试」的前半句。
if [[ "$CHAIN_OK" -eq 1 ]]; then
  idx=0
  while IFS= read -r _c; do
    [[ -n "$_c" ]] || continue
    idx=$((idx + 1))
    _h="$TMP/home-c$idx"
    mkdir -p "$_h"
    _ch="$(candidates_for "$_h" | sed -n "${idx}p")"
    # ★ 清掉上一轮的夹具 ⇒ ★ 「★ 只有这一个候选存在 ★」是真的 ★
    clear_candidate_fixtures
    mk_ssot "$_ch"
    got="$(env -u AIAN_DEPLOY_LIB HOME="$_h" \
      bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>/dev/null)"
    check "④ candidate #$idx resolved when it is the only one" "$_ch" "$got"
  done <<< "$CANDS"
else
  FAIL=$((FAIL + 1)); printf 'FAIL ④ per-candidate resolution: chain has %s entries\n' "$NCAND" >&2
fi

# ⑤ 全部候选都存在 ⇒ 解析到第一个（顺序语义）。
if [[ "$CHAIN_OK" -eq 1 ]]; then
  clear_candidate_fixtures
  _h="$TMP/home-all"; mkdir -p "$_h"
  _all="$(candidates_for "$_h")"
  while IFS= read -r _c; do [[ -n "$_c" ]] && mk_ssot "$_c"; done <<< "$_all"
  _first="$(printf '%s\n' "$_all" | sed -n 1p)"
  got="$(env -u AIAN_DEPLOY_LIB HOME="$_h" \
    bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>/dev/null)"
  check "⑤ all candidates present -> first one wins" "$_first" "$got"
else
  FAIL=$((FAIL + 1)); printf 'FAIL ⑤ order semantics: chain has %s entries\n' "$NCAND" >&2
fi

# ⑥ 只有靠后的候选存在 ⇒ 解析到它（回退语义）。
if [[ "$CHAIN_OK" -eq 1 ]]; then
  clear_candidate_fixtures
  _h="$TMP/home-later"; mkdir -p "$_h"
  _later="$(candidates_for "$_h" | sed -n 2p)"
  mk_ssot "$_later"
  got="$(env -u AIAN_DEPLOY_LIB HOME="$_h" \
    bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>/dev/null)"
  check "⑥ only the later candidate present -> it resolves (fallback)" "$_later" "$got"
else
  FAIL=$((FAIL + 1)); printf 'FAIL ⑥ fallback semantics: chain has %s entries\n' "$NCAND" >&2
fi

# ⑦ 无显式值且无任何候选。
clear_candidate_fixtures
rc=0
out="$(env -u AIAN_DEPLOY_LIB HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err7")" || rc=$?
check "⑦ no SSOT anywhere -> rc 64" "64" "$rc"
check_has "⑦b no SSOT anywhere -> actionable hint" "missing AIAN_DEPLOY_LIB" "$(cat "$TMP/err7")"
check_lacks "⑦c no SSOT anywhere -> caller did not continue" "CALLER-CONTINUED" "$out"

# ⑧ ★ 核心回归点：★ 被 source 且调用方**没有** set -e ⇒ 仍须以 64 中止。
#    ★ 旧写法 `return 64 2>/dev/null || exit 64` 在这里会放行调用方。
clear_candidate_fixtures
rc=0
out="$(env -u AIAN_DEPLOY_LIB HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err8")" || rc=$?
check "⑧ sourced without set -e -> rc 64" "64" "$rc"
check_lacks "⑧b sourced without set -e -> caller did not continue" "CALLER-CONTINUED" "$out"

# ⑨ 直接执行本文件（不是被 source）。
clear_candidate_fixtures
rc=0
env -u AIAN_DEPLOY_LIB HOME="$EMPTY_HOME" bash "$LIB" >/dev/null 2>"$TMP/err9" || rc=$?
check "⑨ executed directly -> rc 64" "64" "$rc"

# ⑩ ★★ 无漂移断言：★ 文案列出的布局集合 == 候选链集合。
#   ★ 配合 ④（★ 每个候选真会被试 ★）⇒ ★ 「★ 文案让人去 clone 的目录 ★」**就是**
#   ★ 「★ 解析器真的会试的目录 ★」⇒ ★ 用户不会再被指向一个死布局。
#   ★ 2026-11-09（F51）：★ 文案现在会列出【★ 仓内软链 ★】（绝对路径）与
#   ★ HOME 下的布局（~/… 形态）⇒ ★ 抽取要同时认这两种形态并把 ~/ 展开回绝对路径 ★
if [[ "$CHAIN_OK" -eq 1 ]]; then
  _listed="$(grep -E '^[[:space:]]+(~|/)' "$TMP/err7" | awk '{print $1}' \
    | sed "s|^~|$EMPTY_HOME|" | LC_ALL=C sort)"
  _expected="$(while IFS= read -r _c; do
      [[ -n "$_c" ]] || continue
      printf '%s\n' "$_c"
    done <<< "$(candidates_for "$EMPTY_HOME")" | LC_ALL=C sort)"
  if [[ "$_listed" == "$_expected" ]]; then
    PASS=$((PASS + 1)); printf 'OK   ⑩ error text layouts == candidate chain (no drift)\n'
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL ⑩ error text layouts drifted from candidate chain\n' >&2
    printf '     listed:   %s\n' "$(printf '%s' "$_listed" | tr '\n' ' ')" >&2
    printf '     expected: %s\n' "$(printf '%s' "$_expected" | tr '\n' ' ')" >&2
  fi
else
  FAIL=$((FAIL + 1)); printf 'FAIL ⑩ no-drift assertion: chain has %s entries\n' "$NCAND" >&2
fi

printf 'shared_lib_resolution selftest: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
