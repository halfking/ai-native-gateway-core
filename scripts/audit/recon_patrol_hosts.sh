#!/usr/bin/env bash
# §143.34 的只读侦察：查某台主机上有没有跑 `ursm-snapshot-health.sh`。
#
# ★ 为什么要做成脚本：§143.30.8 / §143.31.4 / §143.32.6 三处都留了同一个缺口
#   「154 / 245 上有没有同名巡检，没查」⇒ §143.34 闭合它。
#
# ★★ ★ 四路并查，缺一不可（这是本脚本存在的全部理由）：
#   ① /etc/cron.d 全量列出 —— 不只 grep 关键词，要看到「那里本来有什么」
#   ② crontab -l 全量      —— root 的 crontab 常常不在 /etc/cron.d
#   ③ systemctl list-timers --all —— timer 是第三条路径，只查 cron 会漏
#   ④ 按名字全盘 find 脚本本体 —— ★ 这一路把「装了没排期」与「压根没有」区分开
#   ⇒⇒ ★★ 只查 ①②【不够】：它们排除不掉「脚本已部署、只是调度没配」——
#        而那恰恰是最容易让人误判「这台没跑」的那一种形态（两个方向都会误判）。
#
# 用法：
#   bash scripts/audit/recon_patrol_hosts.sh 154 245
#
# ⚠ 全部只读：不写任何文件、不改任何调度、不重启任何服务。

set -uo pipefail

HOSTS=("$@")
if [ ${#HOSTS[@]} -eq 0 ]; then
  echo "用法: bash $0 <host> [host ...]" >&2
  exit 2
fi

for H in "${HOSTS[@]}"; do
  echo "════════ $H ════════"
  ssh -o ConnectTimeout=10 "$H" '
    set -u
    echo "[1] /etc/cron.d 全量"
    ls -1 /etc/cron.d/ 2>/dev/null || echo "(不存在)"
    echo
    echo "[2] crontab -l 全量"
    crontab -l 2>/dev/null || echo "(空或不可读)"
    echo
    echo "[3] systemd timers (含 ursm/snapshot/health)"
    systemctl list-timers --all --no-pager 2>/dev/null | grep -i "ursm\|snapshot\|health" || echo "(none)"
    echo
    echo "[4] 按名字全盘找脚本本体"
    find / -xdev -maxdepth 5 \
      \( -name "*ursm-snapshot*" -o -name "*snapshot-health*" -o -name "*snapshot_health*" \) \
      2>/dev/null || true
    echo "(以上为全部命中；空 = 该主机没有同名脚本)"
  '
  echo
done
