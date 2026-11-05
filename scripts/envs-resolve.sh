# envs-resolve.sh — resolve the envs SSOT root (the dir that holds loader.sh)
# without hardcoding a single layout name.
#
# ★★ 为什么有这个文件（2026-11-05）：
#   envs SSOT 仓（github.com/halfking/envs）的**实体位置**随工作区重组又变了一次：
#     旧：$HOME/workspace/ai-native-tools/envs
#     新：$HOME/workspace/ai-native-tools/deploy/envs
#   （工作区根仓 85faac2a「目录分类与规范中心重组」：deploy-lib/ → deploy/deploy-lib/，
#     嵌套的 envs 仓随之落在 deploy/ 下；envs 是**独立仓**、父仓 .gitignore 守着，
#     不参与那次 rename ⇒ 它的落位由那次重组的目录分类决定。）
#   ⇒ 而 245/154 的部署脚本里那条**两候选链**（ai-native-workspace/envs 与
#     ai-native-tools/envs）在新布局下**两个都落空** ⇒ 每次部署第一步就退出：
#       ✗ envs SSOT loader not found: .../ai-native-workspace/envs/loader.sh
#     ⇒ 报的是一条**本机从来不存在**的死路径 ⇒ 看起来像「envs 丢了」，
#        实际是「默认值没跟着迁移改」。
#
# 解析顺序（与兄弟解析器 scripts/deploy-lib-resolve.sh 同形）：
#   1. $ENVS_ROOT（已设置时）—— 调用方说了算，但**必须可用**（见下）
#   2. $HOME/workspace/ai-native-tools/deploy/envs   （current layout）
#   3. $HOME/workspace/ai-native-tools/envs           （legacy layout）
#   4. $HOME/workspace/ai-native-workspace/envs       （older legacy layout）
#
# ★ 「可用」= <dir>/loader.sh 存在且可读。
# ★ 失败语义：解析不到 ⇒ **rc=64** ＋ **可执行提示**（列出候选链，与解析器同源
#   ⇒ 文案不可能漂移）⇒ 绝不把下游炸成 `No such file or directory`。
# ★ 成功时 export ENVS_ROOT，且**不打印任何东西**。
#   （调用方仍可 source "$ENVS_ROOT/loader.sh"；本文件不代劳加载。）
#
# 行为门：tests/envs_resolution_contract_test.sh
#
# ★ bash 3.2 可跑（macOS 自带 bash）—— 不用 bash4 特性（无 mapfile / 关联数组）。

# ── 判据：★ 一个「可用的 envs SSOT 根」= 下面有 loader.sh ★ ──────────────
_envs_usable() {
  [[ -n "${1:-}" && -r "$1/loader.sh" ]]
}

# ★ 候选链的【★ 单一真相 ★】：★ 格式 = <绝对路径>|<给人看的说明标签> ★
#   ★ 解析循环与失败文案【★ 都从这一个函数取 ★】⇒ ★ 两处不可能各自写一份而漂移 ★
#   ★（兄弟文件 scripts/_shared-lib.sh 的 _shared_lib_candidates 同款形状 ★）。
_envs_candidates() {
  printf '%s\n' \
    "$HOME/workspace/ai-native-tools/deploy/envs|current layout (2026-11-04 workspace reorg)" \
    "$HOME/workspace/ai-native-tools/envs|legacy layout" \
    "$HOME/workspace/ai-native-workspace/envs|older legacy layout"
}

_resolve_envs_root() {
  # 显式指定优先：CI / 多仓共用 SSOT 时由调用方决定。
  # ★ 显式值也要**校验** ⇒ 无效时返回 2（与「没找到候选」的 1 区分开），
  #   ★ 这样错误提示能说清「是你指错了」而不是「机器上没有」。
  if [[ -n "${ENVS_ROOT:-}" ]]; then
    _envs_usable "$ENVS_ROOT" || return 2
    printf '%s\n' "$ENVS_ROOT"; return 0
  fi
  local candidate _label
  while IFS='|' read -r candidate _label; do
    [[ -n "$candidate" ]] || continue
    if _envs_usable "$candidate"; then printf '%s\n' "$candidate"; return 0; fi
  done < <(_envs_candidates)
  return 1
}

# ★ 记下调用方给的显式值：★ 解析失败时 ENVS_ROOT 已被赋成解析器的空输出，
#   不留底就报不出「到底是哪个值指错了」。
_envs_explicit="${ENVS_ROOT:-}"
_envs_resolve_rc=0
ENVS_ROOT="$(_resolve_envs_root)" || _envs_resolve_rc=$?
if [[ "$_envs_resolve_rc" -ne 0 ]]; then
  if [[ "$_envs_resolve_rc" -eq 2 ]]; then
    echo "missing envs SSOT: $_envs_explicit (loader.sh not found)" >&2
    echo "fix: export ENVS_ROOT=/path/to/envs-checkout" >&2
  else
    echo "missing envs SSOT: no loader.sh found under \$HOME/workspace/" >&2
    echo "fix: clone/create one of:" >&2
    # ★ 列出的布局【★ 与解析器实际会试的候选同源 ★】⇒ ★ 文案不可能再漂移 ★
    while IFS='|' read -r _envs_c _envs_l; do
      [[ -n "$_envs_c" ]] || continue
      printf '  ~/%-44s  (%s)\n' "${_envs_c#"$HOME/"}" "$_envs_l" >&2
    done < <(_envs_candidates)
  fi
  # ★ 中止语义与兄弟解析器一致：★ 脚本调用方一律 exit 64 ⇒ 不依赖调用方有没有 set -e；
  #   交互式 shell 保留 return（不杀掉用户的会话）⇒ 只把 64 交回调用方。
  if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    exit 64
  fi
  if [[ "$-" == *i* ]]; then
    return 64
  fi
  exit 64
fi
export ENVS_ROOT
