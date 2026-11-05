#!/usr/bin/env bash
# tests/envs_resolution_contract_test.sh —— scripts/envs-resolve.sh 的行为门。
#
# ★ 2026-11-05 新增；★ 起因是 245/154 部署的**真实故障**：
#   工作区重组（2026-11-04，根仓 85faac2a）把 envs 仓落到
#   ai-native-tools/deploy/envs ⇒ 部署脚本里那条**两候选链**
#   （ai-native-workspace/envs → ai-native-tools/envs）**两个都落空**
#   ⇒ 每次部署第一步就退出：
#     ✗ envs SSOT loader not found: .../ai-native-workspace/envs/loader.sh
#   ⇒ 报的是一条**本机从来不存在**的死路径 ⇒ 看起来像「envs 丢了」。
#
# ★ 本门钉住「envs SSOT 根解析」的解析与失败契约：
#   ①  显式 ENVS_ROOT 有效 ⇒ 解析到它，且**已 export**（子进程可见）；
#   ②  显式值指向**不存在**的目录 ⇒ rc=64 + `missing envs SSOT` + `fix:` 提示；
#   ③  显式值指向**存在但不是 SSOT** 的目录 ⇒ 同 ②；
#   ④  ★ **逐候选**：★ 每个候选**单独**存在时都必须解析到它
#       ⇒ ★ 证明解析器【★ 真的 ★】会试每一个候选（★ 而不只是第一个 ★）；
#   ⑤  全部候选都存在 ⇒ 解析到**第一个**（顺序语义）；
#   ⑥  ★ 只有**靠后**的候选存在 ⇒ 解析到它（回退语义、★ 不挑第一个不存在的 ★）；
#   ⑦  无显式值且无任何候选 ⇒ rc=64 + `missing envs SSOT`；
#   ⑧  **被 source 且调用方没有 set -e** ⇒ 仍须以 64 中止
#       （★ 核心回归点：旧写法只从被 source 的文件返回，调用方继续跑 ⇒ 带着
#         空的 ENVS_ROOT 继续 ⇒ 下游炸成 `No such file or directory`）；
#   ⑨  直接执行本文件 ⇒ rc=64；
#   ⑩  ★★ **无漂移断言**：★ 失败文案里列出的布局集合 **==** 候选链集合
#       ⇒ ★ 「★ 文案让人去 clone 的目录 ★」**必须**是「★ 解析器真的会试的目录 ★」；
#   ⑪  ★ 实现必须暴露【★ 候选链的单一真相 ★】且候选数 ≥ 2
#       ⇒ ★ 「★ 抽掉单一真相 ⇒ 红 ★」这一态**可被构造**。
#
# ★ ★ 本门【★ 一个布局名都不写死 ★】：★ 夹具由**实现的候选链**推导。
#   ★ 为什么：★ 旧写法把布局**写死** ⇒ 候选一改名、★ 门就**假红**
#   （★ 而假红会让人把门"改绿"而不是把实现改对 ★）⇒ ★ 现在【★ 改布局名不假红 ★】。
#
# ★ 零外部依赖、零仓库副作用（全部在 mktemp 里）⇒ 可进 CI 快门。
# ★ 可选参数 $1：★ 把门指向**候选**实现 ⇒ 「这个改动能不能过门」可以在提交前自测、
#   ★ 变异测试（★ 造一个坏实现看门会不会红 ★）也用它。
# Run: bash tests/envs_resolution_contract_test.sh [candidate-implementation-path]
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMPL="${1:-$REPO_ROOT/scripts/envs-resolve.sh}"
[[ -f "$IMPL" ]] || { echo "missing $IMPL" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

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

# 造一个「可用的 envs SSOT 根」：判据 = 下面有 loader.sh（与实现同口径）。
mk_envs() {
  mkdir -p "$1"
  printf '# fixture\n' > "$1/loader.sh"
}

# ★ ★ 取实现里的【★ 候选链单一真相 ★】那个函数（★ 连函数头一起取 ★）。
#   ★ 取不到 ⇒ 实现没有单一真相 ⇒ ⑪ 判红（★ 这就是「★ 抽掉单一真相 ⇒ 红 ★」★）。
CAND_FN="$(sed -n '/^_envs_candidates() {/,/^}/p' "$IMPL")"

# ★ 在【★ 指定 HOME ★】下展开候选链（★ 实现里用的是 $HOME ⇒ 展开结果随 HOME 变 ★）。
#   ★ 剥掉可能的「|<说明标签>」后缀：★ 门只关心**路径** ⇒ ★ 这条剥除式对
#   ★ 「只吐路径」与「吐 path|label」两种实现都成立 ⇒ ★ 门不锁死实现格式。
candidates_for() { # <HOME 根>
  [[ -n "$CAND_FN" ]] || return 1
  HOME="$1" bash -c "$CAND_FN"$'\n''_envs_candidates' 2>/dev/null | sed 's/|[^|]*$//'
}

NEUTRAL_HOME="$TMP/home-neutral"
mkdir -p "$NEUTRAL_HOME"
CANDS="$(candidates_for "$NEUTRAL_HOME")"
NCAND="$(printf '%s\n' "$CANDS" | awk 'NF{c++} END{print c+0}')"

# ★ ⑪a 实现必须暴露候选链的单一真相。
if [[ -n "$CAND_FN" ]]; then
  PASS=$((PASS + 1)); printf 'OK   ⑪a implementation exposes a single candidate source\n'
else
  FAIL=$((FAIL + 1)); printf 'FAIL ⑪a implementation does not expose _envs_candidates()\n' >&2
fi

# ★ ⑪b 候选链至少两个（★ 回退契约：★ 一个候选 = 没有回退 ★）。
# ★ ★ 这里【★ 必须显式判红 ★】而不是「跳过后面那些由链推导的场景」——
#   ★   因为【★ 夹具改成由链推导之后、★ 链为空会让那些断言【★ 一次都不执行 ★】
#   ★   ⇒ 零断言 = 假绿（★ 这正是「★ 空候选 ⇒ 断言零次执行 ⇒ 假绿 ★」那个洞 ★）
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
mk_envs "$TMP/envs-explicit"
got="$(ENVS_ROOT="$TMP/envs-explicit" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; printf "%s" "$ENVS_ROOT"' _ "$IMPL" 2>"$TMP/err1")"
check "① explicit valid wins" "$TMP/envs-explicit" "$got"
got="$(ENVS_ROOT="$TMP/envs-explicit" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; env | grep "^ENVS_ROOT="' _ "$IMPL" 2>/dev/null)"
check "①b success path exports ENVS_ROOT" "ENVS_ROOT=$TMP/envs-explicit" "$got"

# ② 显式值指向不存在的目录。
rc=0
out="$(ENVS_ROOT="$TMP/does-not-exist" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$IMPL" 2>"$TMP/err2")" || rc=$?
check "② explicit missing dir -> rc 64" "64" "$rc"
check_has "②b explicit missing dir -> actionable hint" "missing envs SSOT" "$(cat "$TMP/err2")"
check_has "②c explicit missing dir -> fix hint" "fix: export ENVS_ROOT=" "$(cat "$TMP/err2")"
check_lacks "②d explicit missing dir -> no downstream ENOENT" "No such file or directory" "$(cat "$TMP/err2")"
check_lacks "②e explicit missing dir -> caller did not continue" "CALLER-CONTINUED" "$out"

# ③ 显式值指向存在但不是 SSOT 的目录。
mkdir -p "$TMP/not-envs"
rc=0
out="$(ENVS_ROOT="$TMP/not-envs" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$IMPL" 2>"$TMP/err3")" || rc=$?
check "③ explicit non-SSOT dir -> rc 64" "64" "$rc"
check_has "③b explicit non-SSOT dir -> names the offending path" "$TMP/not-envs" "$(cat "$TMP/err3")"
check_lacks "③c explicit non-SSOT dir -> no downstream ENOENT" "No such file or directory" "$(cat "$TMP/err3")"
check_lacks "③d explicit non-SSOT dir -> caller did not continue" "CALLER-CONTINUED" "$out"

# ④ ★ 逐候选：★ 每个候选单独存在 ⇒ 解析到它。
if [[ "$CHAIN_OK" -eq 1 ]]; then
  idx=0
  while IFS= read -r _c; do
    [[ -n "$_c" ]] || continue
    idx=$((idx + 1))
    _h="$TMP/home-c$idx"
    mkdir -p "$_h"
    _ch="$(candidates_for "$_h" | sed -n "${idx}p")"
    mk_envs "$_ch"
    got="$(env -u ENVS_ROOT HOME="$_h" \
      bash -c 'source "$1"; printf "%s" "$ENVS_ROOT"' _ "$IMPL" 2>/dev/null)"
    check "④ candidate #$idx resolved when it is the only one" "$_ch" "$got"
  done <<< "$CANDS"
else
  FAIL=$((FAIL + 1)); printf 'FAIL ④ per-candidate resolution: chain has %s entries\n' "$NCAND" >&2
fi

# ⑤ 全部候选都存在 ⇒ 解析到第一个（顺序语义）。
if [[ "$CHAIN_OK" -eq 1 ]]; then
  _h="$TMP/home-all"; mkdir -p "$_h"
  _all="$(candidates_for "$_h")"
  while IFS= read -r _c; do [[ -n "$_c" ]] && mk_envs "$_c"; done <<< "$_all"
  _first="$(printf '%s\n' "$_all" | sed -n 1p)"
  got="$(env -u ENVS_ROOT HOME="$_h" \
    bash -c 'source "$1"; printf "%s" "$ENVS_ROOT"' _ "$IMPL" 2>/dev/null)"
  check "⑤ all candidates present -> first one wins" "$_first" "$got"
else
  FAIL=$((FAIL + 1)); printf 'FAIL ⑤ order semantics: chain has %s entries\n' "$NCAND" >&2
fi

# ⑥ 只有靠后的候选存在 ⇒ 解析到它（回退语义）。
if [[ "$CHAIN_OK" -eq 1 ]]; then
  _h="$TMP/home-later"; mkdir -p "$_h"
  _later="$(candidates_for "$_h" | sed -n 2p)"
  mk_envs "$_later"
  got="$(env -u ENVS_ROOT HOME="$_h" \
    bash -c 'source "$1"; printf "%s" "$ENVS_ROOT"' _ "$IMPL" 2>/dev/null)"
  check "⑥ only the later candidate present -> it resolves (fallback)" "$_later" "$got"
else
  FAIL=$((FAIL + 1)); printf 'FAIL ⑥ fallback semantics: chain has %s entries\n' "$NCAND" >&2
fi

# ⑦ 无显式值且无任何候选。
rc=0
out="$(env -u ENVS_ROOT HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$IMPL" 2>"$TMP/err7")" || rc=$?
check "⑦ no SSOT anywhere -> rc 64" "64" "$rc"
check_has "⑦b no SSOT anywhere -> actionable hint" "missing envs SSOT" "$(cat "$TMP/err7")"
check_lacks "⑦c no SSOT anywhere -> caller did not continue" "CALLER-CONTINUED" "$out"

# ⑧ ★ 核心回归点：★ 被 source 且调用方**没有** set -e ⇒ 仍须以 64 中止。
rc=0
out="$(env -u ENVS_ROOT HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$IMPL" 2>"$TMP/err8")" || rc=$?
check "⑧ sourced without set -e -> rc 64" "64" "$rc"
check_lacks "⑧b sourced without set -e -> caller did not continue" "CALLER-CONTINUED" "$out"

# ⑨ 直接执行本文件（不是被 source）。
rc=0
env -u ENVS_ROOT HOME="$EMPTY_HOME" bash "$IMPL" >/dev/null 2>"$TMP/err9" || rc=$?
check "⑨ executed directly -> rc 64" "64" "$rc"

# ⑩ ★★ 无漂移断言：★ 文案列出的布局集合 == 候选链集合。
if [[ "$CHAIN_OK" -eq 1 ]]; then
  _listed="$(grep -E '^[[:space:]]+~/' "$TMP/err7" | awk '{print $1}' | LC_ALL=C sort)"
  _expected="$(while IFS= read -r _c; do
      [[ -n "$_c" ]] || continue
      printf '~/%s\n' "${_c#"$NEUTRAL_HOME/"}"
    done <<< "$CANDS" | LC_ALL=C sort)"
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

# ⑪（P-15，2026-11-06 审计）跨仓防回归：共享部署库 deploy-prereqs.sh 的
#    ENVS_ROOT 回退不得再是单一死默认（旧值 `:-$HOME/workspace/ai-native-tools/envs`
#    是 2026-11-04 重组前路径，245/154 断链同源）。父仓不可达时显式 SKIP 披露。
_PREREQS=""
for _cand in "$REPO_ROOT/../deploy/deploy-lib/deploy-prereqs.sh" \
             "$REPO_ROOT/../../deploy/deploy-lib/deploy-prereqs.sh"; do
  [[ -f "$_cand" ]] && { _PREREQS="$_cand"; break; }
done
if [[ -n "$_PREREQS" ]]; then
  if grep -q 'ENVS_ROOT:-\$HOME/workspace' "$_PREREQS"; then
    FAIL=$((FAIL + 1))
    printf 'FAIL ⑪ deploy-prereqs.sh fell back to a single dead ENVS_ROOT default\n' >&2
  else
    PASS=$((PASS + 1)); printf 'OK   ⑪ deploy-prereqs.sh ENVS_ROOT fallback is chain-based (P-15)\n'
  fi
else
  PASS=$((PASS + 1)); printf 'SKIP ⑪ deploy-prereqs.sh not reachable (no parent checkout)\n'
fi

printf 'envs_resolution selftest: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
