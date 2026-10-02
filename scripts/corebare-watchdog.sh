#!/usr/bin/env bash
# corebare-watchdog.sh — core.bare 毒化高频治愈 + 留痕
#
# 背景（三十七轮续五，2026-10-01）：本仓 module config
# (official-deploy/.git/modules/services/llm-gateway-go/config) 周期性被未知
# 工具写坏为 core.bare=true（与 core.worktree 并存 = 无效状态），使
# git status / go build VCS stamping 全线 exit 128。单会话内曾 5 连发
# （00:11-01:00），又一次于 github 推送门运行窗口内复发（01:43:00，
# deploy_sops/deploy_nocgo_build/deploy_local_contract 三套件同根假红，
# 此前被误归因为"环境互踩"）。签名（周期性、只写 module config、与
# push/commit 无关）与在跑且已收藏本仓的 Sourcetree 已知嵌套仓刷新
# bug 吻合；watchdog 日志持续积累击中时间戳供最终归因。
#
# 自愈分层：pre-commit §0 / pre-push §0（事件驱动）→ 本 watchdog
# （时间驱动兜底，覆盖钩子外空闲窗口）。安装：
#   launchd agent（StartInterval=5）指向本脚本，见
#   ~/Library/LaunchAgents/cn.kxpms.llm-gateway-corebare-watchdog.plist
#
# 实现注意：直接 --file 读写 config，避免对毒化态仓库调用 git（那会
# 打警告且依赖工作区语义）。
set -u

MODULE_CONFIG="__DEV_HOME__/workspace/official-deploy/.git/modules/services/llm-gateway-go/config"
LOG="${HOME}/Library/Logs/llm-gateway-corebare-watchdog.log"

[[ -f "$MODULE_CONFIG" ]] || exit 0

bare="$(git config --file "$MODULE_CONFIG" core.bare 2>/dev/null || true)"
if [[ "$bare" == "true" ]]; then
  git config --file "$MODULE_CONFIG" core.bare false
  printf '%s healed core.bare=true (mtime=%s)\n' \
    "$(date '+%Y-%m-%d %H:%M:%S')" \
    "$(stat -f '%Sm' -t '%Y-%m-%d %H:%M:%S' "$MODULE_CONFIG")" >> "$LOG"
fi
