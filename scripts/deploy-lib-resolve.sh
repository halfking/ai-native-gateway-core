# shellcheck shell=bash
# deploy-lib-resolve.sh — resolve the deploy-lib SSOT without trusting the
# repo-relative scripts/deploy-lib symlink.
#
# scripts/deploy-lib is a RELATIVE symlink (../../../deploy-lib) that only
# resolves inside the canonical checkout depth. Deploy entrypoints also run
# from temporary worktrees (/private/tmp/lgw-merge-*), where the link dangles
# and `source scripts/deploy-lib/...` dies instantly — both 2026-10-07 merge
# rounds each had to hand-patch the link before deploy-245.sh would even
# start. This helper retires that ritual.
#
# ★ 2026-11-09 审计轮（F51）：★ **候选链不再是本文件私有的 ★**
#   ① 此前本文件自带一条链（env → 仓内软链 → $HOME 旧布局），
#      ★ 而它【★ 缺「新布局」★】（$HOME/workspace/ai-native-tools/deploy/deploy-lib）
#      ⇒ ★★ 在**没有**那根机器本地兼容软链的机器上（CI runner / 干净工作区）：
#        本文件解析失败 ⇒ exit 64 ⇒ ★ deploy-154 / 245 / 211 第一步就退出 ★★
#      ⇒ ⇒ 而兄弟解析器 scripts/_shared-lib.sh **能**解析到新布局
#        ⇒ ⇒ 两个兄弟对**同一个机器**给出**相反**的结论 ★
#   ② 现在候选链【★ 只有一条 ★】：scripts/deploy-lib-candidates.sh
#      ⇒ 本文件与 ① 与各测试门**共用**它 ⇒ ★ 不可能再各自漂移 ★
#   ③ 失败语义【★ 也共用同一份文案 ★】（deploy_lib_report_failure）
#      ⇒ ★ 同一个故障不会被讲成两个故事 ★
#
# Resolution order (★ the single shared chain ★):
#   1. $AIAN_DEPLOY_LIB when already set — caller wins, never clobbered
#      (★ but it IS validated ★ — an unusable explicit value is an error,
#        not a silent fallback)
#   2. the shared candidate chain (checkout symlink → current layout →
#      compat symlink → pre-reorg layout)
#
# Exits 64 when nothing yields a usable SSOT. Sets and exports
# AIAN_DEPLOY_LIB; prints nothing on success.
# ★ Gates: tests/deploy_lib_candidate_chain_single_source_test.sh
#   （★ 2026-11-09：★ 原先那个 scripts/.verify-deploy-lib-resolution.sh 是
#     【★ 没有调用方的孤儿 ★】⇒ 按本仓纪律「门没有调用方 = 声明不是门」⇒
#     【★ 删除 ★】⇒ ★ 它的四个场景（env 优先 / 软链命中 / 悬空回落 / 显式值无效
#     ⇒ rc 64）现在由上面那道门的【★ 对称臂 ★】覆盖，且它**已接进强制路径** ★）

_dlr_self_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

# ★ 候选链与失败文案的【★ 唯一真相 ★】★（★ 本文件不含任何布局字面量 ★）
# shellcheck source=deploy-lib-candidates.sh
source "$_dlr_self_dir/deploy-lib-candidates.sh"

# ★ 「可用的 SSOT」判据：★ 本文件的调用方要 source parse-wrapper-flags.sh
#   ⇒ 判据贴合**自己的**前置条件（★ 而候选链是共享的 ★）
_dlr_usable() {
  [[ -n "${1:-}" && -f "$1/parse-wrapper-flags.sh" ]]
}

# ★ 记下调用方给的显式值：★ 解析失败时 AIAN_DEPLOY_LIB 已被赋成解析器的空输出，
#   ★ 不留底就报不出「到底是哪个值指错了」。
_dlr_explicit="${AIAN_DEPLOY_LIB:-}"
_dlr_resolve_rc=0
AIAN_DEPLOY_LIB="$(deploy_lib_resolve "$_dlr_explicit" _dlr_usable)" || _dlr_resolve_rc=$?
if [[ "$_dlr_resolve_rc" -ne 0 ]]; then
  # ★ 文案与兄弟解析器【★ 同一份 ★】⇒ ★ 同一个故障 ⇒ 同一段话 ★
  deploy_lib_report_failure "$_dlr_resolve_rc" "$_dlr_explicit" "parse-wrapper-flags.sh"
  # ★ 中止语义与兄弟解析器一致：★ 脚本调用方一律 exit 64（★ 不依赖调用方
  #   自己有没有 set -e）⇒ ★ 交互式 shell 保留 return（不杀掉用户的会话）★
  if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    exit 64
  fi
  if [[ "$-" == *i* ]]; then
    return 64
  fi
  exit 64
fi
export AIAN_DEPLOY_LIB
