#!/bin/bash
# 245-disk-check-hardening.sh —— ★ 245 的 IO 风暴收敛补丁（★ 仓内可复现版本 ★）
#
# ★ 为什么要固化进仓（★ 2026-11-26 审计轮收口复核轮 ★）：
#   根因＝**自反馈死循环**，三要素同时成立：
#     ① 触发源每周期必到：/etc/cron.d/disk-check ⇒ `17 * * * *` ⇒ 每小时拉起一次
#        /usr/local/sbin/disk-check.sh
#     ② 动作不收敛：脚本第 28 行 `find "$LOGDIR" -type f … -mtime +30` 对
#        /var/log/llm-gateway-go **全树无边界递归**（无 -maxdepth、无 -xdev、无超时）
#        ⇒ 实测单轮跑 1 小时+ ⇒ 每小时新增一个 ⇒ **必然重叠**
#     ③ 清理被另一个条件门控：磁盘 76% < 阈值 80% ⇒ TRIGGER=0 ⇒
#        归档只写一行 pending **永不删除** ⇒ 目录只增不减 ⇒ find 越来越慢 ⇒ IO 永久高压
#   ⇒ **治法必须同时对准三要素**（★ 只杀一次进程会在 1 小时后复发 ★）：
#     A1 flock 防重叠（要素①的叠加面：同时刻只允许一个实例）
#     A2 find 加 `-xdev` + `timeout 120`（要素②：动作收敛，单轮有上界）
#     A3 归档删除脱离磁盘阈值（要素③：打断正反馈环）
#     C1 cron 由每小时改为每 3 小时（要素①：降低触发频率）
#     B1/B2 cleanup-known-junk 的两处 `find /` 加 timeout（次级放大项）
#     C2 cleanup-known-junk 由 `*/30` 改为 `17,47`（与 disk-check 错峰，避免同刻叠加）
#
# ★ 用法（★ 默认 dry-run、★ 不碰机器 ★）：
#     bash scripts/ops/245-disk-check-hardening.sh            # 只打印将要做的改动
#     bash scripts/ops/245-disk-check-hardening.sh --apply    # 真正动手（改前自动备份）
#     bash scripts/ops/245-disk-check-hardening.sh --verify   # 只校验当前状态
#
# ★ 回滚：★ 每个被改的文件都会留一份 `.bak.<时间戳>` ⇒ 回滚命令在结尾打印 ★
set -euo pipefail

MODE="dry-run"
case "${1:-}" in
  "") ;;
  --apply) MODE="apply" ;;
  --verify) MODE="verify" ;;
  *) echo "用法：$0 [--apply|--verify]（默认 dry-run）" >&2; exit 2 ;;
esac

DISK_SH="/usr/local/sbin/disk-check.sh"
JUNK_SH="/opt/scripts/cleanup-known-junk.sh"
CRON_DISK="/etc/cron.d/disk-check"
CRON_JUNK="/etc/cron.d/cleanup-known-junk"
TS="$(date +%Y%m%d-%H%M%S)"

say() { printf '%s\n' "$*"; }
hr()  { say "------------------------------------------------------------"; }

hr; say "245 IO 风暴收敛补丁 · 模式=$MODE · $(date -Iseconds)"; hr

if [ "$MODE" = "verify" ]; then
  rc=0
  check() { # check <文件> <模式> <说明>
    if grep -qE "$2" "$1" 2>/dev/null; then
      say "  [已加固] $3  ← $1"
    else
      say "  [未加固] $3  ← $1"; rc=1
    fi
  }
  check "$DISK_SH" 'flock -n 9'                    "flock 防重叠"
  check "$DISK_SH" 'timeout 120 find "\$LOGDIR" -xdev' "find 有界（timeout + -xdev）"
  check "$DISK_SH" '不受磁盘阈值门控'               "归档删除脱离 80% 阈值"
  check "$JUNK_SH" 'timeout 120 find / '            "cleanup 的全盘 find 加 timeout"
  check "$CRON_DISK" '^17 \*/3 \* \* \*'             "disk-check cron 改 3 小时"
  check "$CRON_JUNK" '^17,47 \* \* \* \*'            "cleanup cron 错峰"
  hr
  [ "$rc" = "0" ] && say "verify：全部已加固" || say "verify：存在未加固项（★ 补丁尚未应用或被回退 ★）"
  exit "$rc"
fi

for f in "$DISK_SH" "$JUNK_SH" "$CRON_DISK" "$CRON_JUNK"; do
  [ -f "$f" ] || { say "缺失文件：$f ⇒ 本补丁不适用这台机器" >&2; exit 3; }
done

# ── 干跑：只打印将要发生的替换 ──
if [ "$MODE" = "dry-run" ]; then
  say "【干跑】以下改动**尚未执行**（加 --apply 才动手）："
  say "  A1 $DISK_SH：在 LOG 定义后插入 flock 防重叠（同一时刻只允许一个实例）"
  say "  A2 $DISK_SH：find 加 \`-xdev\` 与 \`timeout 120\`（单轮时长有上界）"
  say "  A3 $DISK_SH：归档删除脱离 \`TRIGGER\`（打断「只增不减 ⇒ find 变慢」的正反馈环）"
  say "  B1 $JUNK_SH：第 51 行 \`find /\` 加 \`timeout 120\`"
  say "  B2 $JUNK_SH：第 69 行 \`find /\` 加 \`timeout 120\`"
  say "  C1 $CRON_DISK：\`17 * * * *\` ⇒ \`17 */3 * * *\`"
  say "  C2 $CRON_JUNK：\`*/30 * * * *\` ⇒ \`17,47 * * * *\`（与 disk-check 错峰）"
  hr; say "干跑结束：**机器未被改动**。"
  exit 0
fi

# ── 应用 ──
say "【应用】改前备份到 *.bak.$TS"
for f in "$DISK_SH" "$JUNK_SH" "$CRON_DISK" "$CRON_JUNK"; do
  cp -p "$f" "$f.bak.$TS"
done

python3 - "$DISK_SH" "$JUNK_SH" "$CRON_DISK" "$CRON_JUNK" <<'PYEOF'
import sys, re

disk_sh, junk_sh, cron_disk, cron_junk = sys.argv[1:5]

def patch(path, pairs):
    s = open(path, encoding="utf-8").read()
    for old, new in pairs:
        if new in s and old not in s:
            print("  [skip] %s：已是加固形态" % path)
            continue
        if s.count(old) != 1:
            raise SystemExit("锚点不唯一或缺失：%s :: %r（命中 %d 次）"
                             % (path, old[:70], s.count(old)))
        s = s.replace(old, new)
    open(path, "w", encoding="utf-8").write(s)
    print("  [patched] %s" % path)

# ── A1 flock：插在 ts() 定义之后（LOG 与 ts 都已就位）──
flock_block = '''
# ★ 2026-11-26 IO 风暴收敛（★ 仓内 scripts/ops/245-disk-check-hardening.sh ★）：
# ★ 每小时起一个、每个跑 1 小时+ ⇒ 必然重叠 ⇒ flock 保证同一时刻只有一个实例 ★
LOCKFILE=/run/disk-check.lock
exec 9>"$LOCKFILE" 2>/dev/null || true
if ! flock -n 9; then
  echo "[$(ts)] another disk-check instance holds the lock, skip this round" >> "$LOG"
  exit 0
fi
'''

# ── A2 find 有界 ──
old_find = '''OLD=$(find "$LOGDIR" -type f \\( -name '*.gz' -o -name '*.log-*' -o -name '*-20[0-9][0-9]*' \\) -mtime +30 2>/dev/null | grep -vE 'wtmp|btmp' | head -500)'''
new_find = '''OLD=$(timeout 120 find "$LOGDIR" -xdev -type f \\( -name '*.gz' -o -name '*.log-*' -o -name '*-20[0-9][0-9]*' \\) -mtime +30 2>/dev/null | grep -vE 'wtmp|btmp' | head -500)'''

# ── A3 归档删除脱离阈值 ──
old_gate = '''    if [ "$TRIGGER" = "1" ]; then
      echo "$OLD" | xargs rm -f 2>/dev/null
      echo "[$(ts)] removed $N archived logs (>30d) in $LOGDIR" >> "$LOG"
    else
      echo "[$(ts)] pending: $N archived logs >30d (below threshold)" >> "$LOG"
    fi'''
new_gate = '''    # ★ 2026-11-26 IO 风暴收敛：★ 归档删除【不再被磁盘阈值门控】★
    # ★ 理由：★ 日志归档目录只增不减 ⇒ find 越来越慢 ⇒ IO 永久高压（自反馈死循环）★
    # ★ 日志归档治理与「整机磁盘是否超阈值」是两件事 ⇒ 阈值只该管【整机清理】★
    echo "$OLD" | xargs rm -f 2>/dev/null
    echo "[$(ts)] removed $N archived logs (>30d) in $LOGDIR (★ 不受磁盘阈值门控 ★)" >> "$LOG"'''

patch(disk_sh, [
    ("ts() { date -Iseconds; }\n", "ts() { date -Iseconds; }\n" + flock_block),
    (old_find, new_find),
    (old_gate, new_gate),
])

# ── B1/B2 全盘 find 加 timeout ──
s = open(junk_sh, encoding="utf-8").read()
if "timeout 120 find /" in s:
    print("  [skip] %s：已是加固形态" % junk_sh)
else:
    n = s.count("$(find / ")
    if n != 2:
        raise SystemExit("cleanup-known-junk.sh 里 `$(find / ` 命中 %d 次（期望 2）" % n)
    s = s.replace("$(find / ", "$(timeout 120 find / ")
    open(junk_sh, "w", encoding="utf-8").write(s)
    print("  [patched] %s（2 处全盘 find 加 timeout）" % junk_sh)

# ── C1/C2 cron ──
s = open(cron_disk, encoding="utf-8").read()
if "17 */3 * * *" not in s:
    s2 = s.replace("17 * * * *", "17 */3 * * *")
    if s2 == s:
        raise SystemExit("cron disk-check 的 `17 * * * *` 没找到")
    open(cron_disk, "w", encoding="utf-8").write(s2)
    print("  [patched] %s：每小时 ⇒ 每 3 小时" % cron_disk)
else:
    print("  [skip] %s：已是 3 小时口径" % cron_disk)

s = open(cron_junk, encoding="utf-8").read()
if "17,47 * * * *" not in s:
    s2 = s.replace("*/30 * * * *", "17,47 * * * *")
    if s2 == s:
        raise SystemExit("cron cleanup-known-junk 的 `*/30 * * * *` 没找到")
    open(cron_junk, "w", encoding="utf-8").write(s2)
    print("  [patched] %s：每 30 分钟 ⇒ 错峰到 17/47 分" % cron_junk)
else:
    print("  [skip] %s：已错峰" % cron_junk)
PYEOF

hr
say "语法检查（bash -n）："
for f in "$DISK_SH" "$JUNK_SH"; do
  if bash -n "$f"; then say "  [ok] $f"; else say "  [FAIL] $f ⇒ 请从备份回滚"; exit 1; fi
done

hr
say "已改文件（备份 *.bak.$TS）："
for f in "$DISK_SH" "$JUNK_SH" "$CRON_DISK" "$CRON_JUNK"; do say "  $f   （备份：$f.bak.$TS）"; done
say "回滚：逐个执行  cp -p <file>.bak.$TS <file>"
hr
say "建议下一步（★ 观察一轮 ★）："
say "  1) 手工跑一轮验证不报错：  bash $DISK_SH"
say "  2) 看日志：                tail -n 20 /var/log/disk-check.log"
say "  3) 下一轮 cron（:17）后复查：iostat -x 1 2 与 ps -eo pid,etime,args | grep 'find /var/log'"
hr
say "补丁应用完成。"
