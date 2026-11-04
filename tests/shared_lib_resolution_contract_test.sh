#!/usr/bin/env bash
# tests/shared_lib_resolution_contract_test.sh —— scripts/_shared-lib.sh 的行为门。
#
# ★ 2026-11-04 审计轮（F-15）新增。★ 本门钉住「共享部署库入口」的解析与失败契约：
#   ①  显式 AIAN_DEPLOY_LIB 有效 ⇒ 解析到它，且**已 export**（子进程可见）；
#   ②  显式值指向**不存在**的目录 ⇒ rc=64 + `missing deploy-lib SSOT` + `fix:` 提示；
#   ③  显式值指向**存在但不是 SSOT** 的目录 ⇒ 同 ②；
#   ④  无显式值 ⇒ 解析到 $HOME/workspace/ai-native-workspace/deploy-lib（current 布局）；
#   ⑤  current 与 legacy 同时存在 ⇒ **current 优先**；
#   ⑥  只有 legacy ⇒ 解析到 $HOME/workspace/ai-native-tools/deploy-lib；
#   ⑦  无显式值且无任何候选 ⇒ rc=64 + `missing AIAN_DEPLOY_LIB`；
#   ⑧  **被 source 且调用方没有 set -e** ⇒ 仍须以 64 中止
#       （★ F-15 的核心回归点：旧写法只从被 source 的文件返回，调用方继续跑）；
#   ⑨  直接执行本文件 ⇒ rc=64。
#
# ★ 为什么要有这道门：★ 本仓的纪律是「门没有调用方 = 声明不是门」，
#   而 `_resolve_aian_deploy_lib` 在本门出现之前**没有任何机器覆盖**
#   ⇒ 兄弟解析器 deploy-lib-resolve.sh 有 .verify-deploy-lib-resolution.sh，
#   本文件没有 ⇒ ★ 护栏不对称（F-15 的一部分）。
#
# ★ 零外部依赖、零仓库副作用（全部在 mktemp 里）⇒ 可进 CI 快门。
# Run: bash tests/shared_lib_resolution_contract_test.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# ★ 可选参数：★ 允许把门指向**候选**实现 ⇒ 「这个改动能不能过门」可以在提交前自测。
LIB="${1:-$REPO_ROOT/scripts/_shared-lib.sh}"
[[ -f "$LIB" ]] || { echo "missing $LIB" >&2; exit 1; }

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

# 造一个「可用的 SSOT」：_shared-lib.sh 会 source 下面这两个文件。
mk_ssot() {
  mkdir -p "$1"
  printf '# fixture\n' > "$1/deploy-prereqs.sh"
  printf '# fixture\n' > "$1/deploy-image-resolution.sh"
  printf '# fixture\n' > "$1/parse-wrapper-flags.sh"
}

EMPTY_HOME="$TMP/home-empty"
mkdir -p "$EMPTY_HOME"

# ① 显式有效值优先，且已 export。
mk_ssot "$TMP/ssot-explicit"
got="$(AIAN_DEPLOY_LIB="$TMP/ssot-explicit" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>"$TMP/err1")"
check "① explicit valid wins" "$TMP/ssot-explicit" "$got"
got="$(AIAN_DEPLOY_LIB="$TMP/ssot-explicit" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; env | grep "^AIAN_DEPLOY_LIB="' _ "$LIB" 2>/dev/null)"
check "①b success path exports AIAN_DEPLOY_LIB" "AIAN_DEPLOY_LIB=$TMP/ssot-explicit" "$got"

# ② 显式值指向不存在的目录。
rc=0
out="$(AIAN_DEPLOY_LIB="$TMP/does-not-exist" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err2")" || rc=$?
check "② explicit missing dir -> rc 64" "64" "$rc"
check_has "②b explicit missing dir -> actionable hint" "missing deploy-lib SSOT" "$(cat "$TMP/err2")"
check_has "②c explicit missing dir -> fix hint" "fix: export AIAN_DEPLOY_LIB=" "$(cat "$TMP/err2")"
check_lacks "②d explicit missing dir -> no downstream ENOENT" "No such file or directory" "$(cat "$TMP/err2")"
check_lacks "②e explicit missing dir -> caller did not continue" "CALLER-CONTINUED" "$out"

# ③ 显式值指向存在但不是 SSOT 的目录。
mkdir -p "$TMP/not-ssot"
rc=0
out="$(AIAN_DEPLOY_LIB="$TMP/not-ssot" HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err3")" || rc=$?
check "③ explicit non-SSOT dir -> rc 64" "64" "$rc"
check_has "③b explicit non-SSOT dir -> names the offending path" "$TMP/not-ssot" "$(cat "$TMP/err3")"
check_lacks "③c explicit non-SSOT dir -> no downstream ENOENT" "No such file or directory" "$(cat "$TMP/err3")"
check_lacks "③d explicit non-SSOT dir -> caller did not continue" "CALLER-CONTINUED" "$out"

# ④ current 布局（$HOME/workspace/ai-native-workspace/deploy-lib）。
mk_ssot "$TMP/home-current/workspace/ai-native-workspace/deploy-lib"
got="$(env -u AIAN_DEPLOY_LIB HOME="$TMP/home-current" \
  bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>/dev/null)"
check "④ current layout resolved" "$TMP/home-current/workspace/ai-native-workspace/deploy-lib" "$got"

# ⑤ current 与 legacy 同时存在 ⇒ current 优先。
mk_ssot "$TMP/home-both/workspace/ai-native-workspace/deploy-lib"
mk_ssot "$TMP/home-both/workspace/ai-native-tools/deploy-lib"
got="$(env -u AIAN_DEPLOY_LIB HOME="$TMP/home-both" \
  bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>/dev/null)"
check "⑤ current layout wins over legacy" "$TMP/home-both/workspace/ai-native-workspace/deploy-lib" "$got"

# ⑥ 只有 legacy 布局。
mk_ssot "$TMP/home-legacy/workspace/ai-native-tools/deploy-lib"
got="$(env -u AIAN_DEPLOY_LIB HOME="$TMP/home-legacy" \
  bash -c 'source "$1"; printf "%s" "$AIAN_DEPLOY_LIB"' _ "$LIB" 2>/dev/null)"
check "⑥ legacy layout resolved" "$TMP/home-legacy/workspace/ai-native-tools/deploy-lib" "$got"

# ⑦ 无显式值且无任何候选。
rc=0
out="$(env -u AIAN_DEPLOY_LIB HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err7")" || rc=$?
check "⑦ no SSOT anywhere -> rc 64" "64" "$rc"
check_has "⑦b no SSOT anywhere -> actionable hint" "missing AIAN_DEPLOY_LIB" "$(cat "$TMP/err7")"
check_lacks "⑦c no SSOT anywhere -> caller did not continue" "CALLER-CONTINUED" "$out"

# ⑧ ★ 核心回归点：★ 被 source 且调用方**没有** set -e ⇒ 仍须以 64 中止。
#    ★ 旧写法 `return 64 2>/dev/null || exit 64` 在这里会放行调用方。
rc=0
out="$(env -u AIAN_DEPLOY_LIB HOME="$EMPTY_HOME" \
  bash -c 'source "$1"; echo CALLER-CONTINUED' _ "$LIB" 2>"$TMP/err8")" || rc=$?
check "⑧ sourced without set -e -> rc 64" "64" "$rc"
check_lacks "⑧b sourced without set -e -> caller did not continue" "CALLER-CONTINUED" "$out"

# ⑨ 直接执行本文件（不是被 source）。
rc=0
env -u AIAN_DEPLOY_LIB HOME="$EMPTY_HOME" bash "$LIB" >/dev/null 2>"$TMP/err9" || rc=$?
check "⑨ executed directly -> rc 64" "64" "$rc"

printf 'shared_lib_resolution selftest: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
