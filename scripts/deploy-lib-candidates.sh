#!/usr/bin/env bash
# deploy-lib-candidates.sh —— 【★ 共享部署库 SSOT 候选链的【★ 唯一真相 ★】★】
#
# ★ 这个文件是什么（2026-11-09 · 审计档 F51）：
#   仓内曾有**三份互不相同**的「SSOT 在哪」的候选链：
#     ① scripts/_shared-lib.sh                （缺「仓内软链」这一候选）
#     ② scripts/deploy-lib-resolve.sh         （缺「新布局」这一候选）
#     ③ tests/deploy_switch_lock_*_test.sh    （两者的并集，但同样缺「新布局」）
#   ⇒ 后果（审计档原文）：「★ 改代码不改测试 ⇒ 那个测试就在【测另一条链】、
#     而它照样绿 ⇒ ★ 「门是绿的」不再意味着「候选链一致」★」。
#
# ★ 而【★ ② 缺的那一条恰恰是今天最要紧的那一条 ★】：
#   「新布局」＝ $HOME/workspace/ai-native-tools/deploy/deploy-lib
#   ⇒ 而 `~/workspace/ai-native-tools/deploy-lib`（老布局的兼容软链）是
#     【★ 机器本地 ★】的、**不在任何仓里** ⇒ ⇒ 在**没有**那根软链的机器上
#     （CI runner / 新机器 / 重组后的干净工作区）：
#       · ① 能解析到新布局 ✔
#       · ② **解析不到** ⇒ exit 64 ⇒ deploy-154 / 245 / 211
#         （它们 source ②）**第一步就退出** ⇒ ★ 整条部署链断在那儿 ★
#       · ③ 同样解析不到 ⇒ 对拍门整段 **SKIP** ⇒ ★ **假绿** ★
#   ⇒ ★★ 这不是「理论上的不一致」，是【★ 一台机器上就能复现的部署中断 ★】★★
#
# ★ 本文件把「SSOT 可能在哪些位置」**定义一次**，其余全部**引用**它：
#   · deploy_lib_candidates            —— 候选链（唯一真相）
#   · deploy_lib_find <必需文件>       —— 按链找第一个可用的 SSOT（测试脚本用）
#   · deploy_lib_resolve <显式值> <可用性判据函数> —— 通用解析器
#   · deploy_lib_report_failure        —— 【★ 统一失败语义 ★】的报错文案
#
# ★ 本文件【★ 零副作用 ★】：只定义函数、不解析、不加载、不退出
#   ⇒ 可被任何脚本 / 测试安全 source。
# ★ bash 3.2 可跑（macOS 自带 bash）—— 不用 mapfile / 关联数组。
#
# ★ 「可用」的判据【★ 各消费方不同、且这是对的 ★】：
#   每个消费方要 source 的文件不同（① 要 prereqs + 镜像解析、② 要
#   parse-wrapper-flags、测试门要 lock.sh / db-changelog.sh）
#   ⇒ 【★ 判据贴合各自的前置条件 ★】，而【★ 候选链只有一条 ★】。
#   ⇒ ★ 判据不同 ⇒ 在「一个目录只满足一部分判据」的机器上，两个消费方可能
#     解析到**不同**目录 ⇒ ★ 这是既有的、可解释的口径（不是漂移）★。

# ── 候选链的【★ 唯一真相 ★】─────────────────────────────────────────────────
# ★ 格式 = <绝对路径>|<给人看的说明标签> ★
#   ★ 解析循环（deploy_lib_resolve / deploy_lib_find）与失败文案
#     （deploy_lib_report_failure）**都从这一个函数取** ⇒ ★ 两处不可能漂移 ★
#   ★ 顺序即优先级：★ 先到先得 ★
#     ① 仓内软链（相对 checkout 深度 ⇒ 在临时 worktree / 异深检出里会悬空）
#     ② 当前布局（2026-11-04 工作区重组后的规范位）
#     ③ 兼容软链（机器本地，不在任何仓里）
#     ④ 重组前的旧布局
deploy_lib_candidates() {
  printf '%s\n' \
    "$(deploy_lib_self_candidate)|checkout-relative compat symlink (dangles in temp worktrees)" \
    "$HOME/workspace/ai-native-tools/deploy/deploy-lib|current layout (2026-11-04 workspace reorg)" \
    "$HOME/workspace/ai-native-tools/deploy-lib|compat symlink → deploy/deploy-lib" \
    "$HOME/workspace/ai-native-workspace/deploy-lib|pre-reorg layout"
}

# ── 候选之一：仓内软链（★ 本文件所在目录下的 deploy-lib ★）────────────────
# ★ 取【★ 本文件自己的目录 ★】⇒ 任何消费方（不论它在 scripts/ 还是 tests/）
#   拿到的都是**同一个**仓内软链 ⇒ 不可能各写各的。
deploy_lib_self_candidate() {
  local self_dir
  self_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd -P)" || self_dir=""
  printf '%s\n' "${self_dir:+$self_dir/deploy-lib}"
}

# ── 通用解析器：★ 显式值 → 候选链 ★ ───────────────────────────────────────
#   用法：deploy_lib_resolve "$AIAN_DEPLOY_LIB" _shared_lib_usable
#   返回：0 = 解析成功（路径打到 stdout）
#         2 = ★ 显式值无效 ★（⇒ 文案要说「是你指错了」而不是「机器上没有」）
#         1 = 一个候选都没命中
#   ★ 显式值【★ 也要校验 ★】：无效的显式值不得被当成「解析成功」
#     （旧写法：解析阶段照样成功 ⇒ 直到下游 source 才炸出 ENOENT）
deploy_lib_resolve() {
  local explicit="${1:-}" usable_fn="${2:-}"
  if [[ -n "$explicit" ]]; then
    if "$usable_fn" "$explicit"; then printf '%s\n' "$explicit"; return 0; fi
    return 2
  fi
  local candidate _label
  while IFS='|' read -r candidate _label; do
    [[ -n "$candidate" ]] || continue
    if "$usable_fn" "$candidate"; then printf '%s\n' "$candidate"; return 0; fi
  done < <(deploy_lib_candidates)
  return 1
}

# ── 按链找第一个「含某个必需文件」的 SSOT ──────────────────────────────────
#   用法：deploy_lib_find lock.sh [显式值]
#   ★ 显式值优先（调用方说了算），但**同样要校验**（含那个必需文件才算数）。
#   ★ 找不到 ⇒ 返回 1 且**不打印任何东西**（调用方自己决定怎么报）。
deploy_lib_find() {
  local required="${1:-}" explicit="${2:-${AIAN_DEPLOY_LIB:-}}"
  local candidate _label
  if [[ -n "$explicit" && -f "$explicit/$required" ]]; then
    printf '%s\n' "$explicit"; return 0
  fi
  while IFS='|' read -r candidate _label; do
    [[ -n "$candidate" ]] || continue
    if [[ -f "$candidate/$required" ]]; then printf '%s\n' "$candidate"; return 0; fi
  done < <(deploy_lib_candidates)
  return 1
}

# ── 统一失败语义：★ 报错文案 ★ ────────────────────────────────────────────
#   用法：deploy_lib_report_failure <rc> <显式值> <必需文件描述>
#   ★ 【★ 两条解析路径（① 与 ②）用同一份文案 ★】⇒ ★ 同一个故障不会被
#     讲成两个故事 ⇒ ★ 运维看到的第一行就能对上口径 ★
deploy_lib_report_failure() {
  local rc="${1:-1}" explicit="${2:-}" required="${3:-required file}"
  if [[ "$rc" == "2" ]]; then
    echo "missing deploy-lib SSOT: $explicit ($required not found)" >&2
    echo "fix: export AIAN_DEPLOY_LIB=/path/to/deploy-lib-checkout" >&2
  else
    echo "missing AIAN_DEPLOY_LIB: no SSOT found under \$HOME/workspace/" >&2
    echo "fix: clone/create one of:" >&2
    deploy_lib_print_candidates >&2
    echo "  (SSOT created 2026-09-09 per docs/deployment/UNIFICATION-PLAN-2026-09-09.md §3.1)" >&2
    echo "  or set AIAN_DEPLOY_LIB explicitly" >&2
  fi
}

# ── 候选清单的【★ 展示形态 ★】──────────────────────────────────────────────
#   ★ 与解析器同源（同一个 deploy_lib_candidates）⇒ ★ 文案不可能漂移 ★
#   ★ HOME 下的布局打成 ~/… 形态（人看得懂），仓内软链打绝对路径 ★
#   ★ 默认写到 stdout；★ 需要走 stderr 的调用方自己重定向（>&2）★
deploy_lib_print_candidates() {
  local candidate _label shown
  while IFS='|' read -r candidate _label; do
    [[ -n "$candidate" ]] || continue
    if [[ "$candidate" == "$HOME/"* ]]; then
      # shellcheck disable=SC2088  # ★ 这个 ~ 是**给人看的展示串**、★ 不是 shell 展开 ★
      shown="~/${candidate#"$HOME/"}"
    else
      shown="$candidate"
    fi
    printf '  %-52s  (%s)\n' "$shown" "$_label"
  done < <(deploy_lib_candidates)
}
