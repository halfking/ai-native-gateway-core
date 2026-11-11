#!/usr/bin/env bash
# passive-fingerprint-check.sh — 被动指纹采集系统健康检查
#
# 把 docs/monitoring/passive-fingerprint-monitoring-guide.md 的两层检查
# 落地成可执行脚本（判定标准与指南/goal 文档逐条对应）：
#
#   第一部分 实时健康检查（30 分钟窗口，每次运行）
#   第二部分 深度数据质量分析（2 小时窗口，--deep）
#
# 用法:
#   passive-fingerprint-check.sh [--host 211] [--container NAME] [--deep]
#
# 退出码:
#   0 健康/优秀   1 警告/合格   2 异常/不达标   3 环境失败（连不上/容器不唯一）
#
# ★ 容器名自动发现 ⇒ 不唯一/取不到 ⇒ 显式失败（fail-closed，rc=3）。
#   蓝绿窗口期两台同跑时用 --container 显式钉住。
# ★ docker 日志时间戳恒为 UTC ⇒ 报告时钟统一 UTC（勿与 +08 混读）。
# ★ 口径注记：flush 日志只在 nodes>0 时打（worker 无节点静默）⇒ 「Flush次数」
#   是「有写入的 flush 数」，与心跳 ticks 口径不同；成功率 = flush / (flush+failures)。
set -u

HOST="211"
CONTAINER=""
DEEP=0

while [ $# -gt 0 ]; do
    case "$1" in
        --host) HOST="$2"; shift 2 ;;
        --container) CONTAINER="$2"; shift 2 ;;
        --deep) DEEP=1; shift ;;
        *) echo "unknown arg: $1" >&2; exit 3 ;;
    esac
done

# ── 容器自动发现（fail-closed）───────────────────────────────────────────
if [ -z "$CONTAINER" ]; then
    NAMES=$(ssh "$HOST" "docker ps --format '{{.Names}}'" 2>/dev/null | grep -E '^llm-gateway-211-[a-z]+$' || true)
    COUNT=$(printf '%s' "$NAMES" | grep -c '^llm-gateway-211-' || true)
    if [ "$COUNT" -ne 1 ]; then
        echo "FAIL: llm-gateway-211-* 容器不唯一(${COUNT} 台)或取不到 ⇒ 用 --container 显式钉住" >&2
        printf '%s\n' "$NAMES" >&2
        exit 3
    fi
    CONTAINER="$NAMES"
fi

# ── 环境采集（一次性拉 2h 窗口，30 分钟窗口由分析侧过滤）────────────────
PS_LINE=$(ssh "$HOST" "docker ps --filter name=^${CONTAINER}$ --format '{{.Names}}|{{.Status}}'" 2>/dev/null || true)
VERSION_JSON=$(curl -s --max-time 10 https://llm.itestu.cn/api/system/version || true)
NOW_UTC=$(date -u +%Y-%m-%dT%H:%M:%SZ)

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
ssh "$HOST" "docker logs --since 2h ${CONTAINER} 2>&1" 2>/dev/null \
    | grep -E '"msg":"passive baseline (flushed|worker alive)"|"msg":"passive baseline flush had failures"' \
    > "$TMP" || true

# ── 分析与判定（判定标准与监控指南逐条对应）────────────────────────────
PF_LOGS="$TMP" \
PF_CONTAINER="$CONTAINER" \
PF_PS="$PS_LINE" \
PF_VERSION="$VERSION_JSON" \
PF_NOW="$NOW_UTC" \
PF_DEEP="$DEEP" \
python3 - <<'PY'
import json, os, sys
from datetime import datetime, timedelta, timezone
from collections import Counter

def parse_ts(s):
    # docker 日志纳秒精度 ⇒ 截到微秒再解析
    if '.' in s:
        head, tail = s.split('.', 1)
        frac = ''.join(c for c in tail if c.isdigit())[:6]
        s = head + '.' + frac + ('Z' if tail.rstrip().endswith('Z') or 'Z' in tail else '+00:00')
    s = s.replace('Z', '+00:00')
    return datetime.fromisoformat(s)

now = datetime.fromisoformat(os.environ["PF_NOW"].replace('Z', '+00:00'))
container = os.environ["PF_CONTAINER"]
ps_line = os.environ["PF_PS"]
version_raw = os.environ["PF_VERSION"]
deep = os.environ["PF_DEEP"] == "1"

flushes, beats, failures = [], [], []
with open(os.environ["PF_LOGS"]) as f:
    for line in f:
        line = line.strip()
        if not line.startswith('{'):
            continue
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        msg = rec.get('msg', '')
        try:
            ts = parse_ts(rec['timestamp'])
        except Exception:
            continue
        if msg == 'passive baseline flushed':
            flushes.append((ts, int(rec.get('nodes', 0))))
        elif msg == 'passive baseline worker alive':
            beats.append((ts, rec))
        elif 'flush had failures' in msg:
            failures.append((ts, rec))

def fmt_verdict(v):
    return {'ok': '✅', 'warn': '⚠️', 'bad': '❌', 'na': '—'}[v]

# ── 版本 ────────────────────────────────────────────────────────────────
ver_str = "版本获取失败 ⚠️"
try:
    v = json.loads(version_raw)
    ver_str = f"{v.get('git_sha','?')} (build {v.get('build_seq','?')})"
except Exception:
    pass

# ── 容器状态 ────────────────────────────────────────────────────────────
if ps_line and '|' in ps_line:
    name, status = ps_line.split('|', 1)
    container_ok = ('healthy' in status) and ('Restarting' not in status)
    container_str = f"健康，{status.strip()}" if container_ok else f"异常：{status.strip()} ❌"
else:
    container_ok = False
    container_str = "取不到容器状态 ❌"

# ── 第一部分：实时健康（30 分钟窗口）───────────────────────────────────
w1 = [x for x in flushes if x[0] >= now - timedelta(minutes=30)]
f1 = [x for x in failures if x[0] >= now - timedelta(minutes=30)]
last_beat = beats[-1] if beats else None

issues_bad, issues_warn = [], []

if not w1:
    n1, avg1, ago = 0, 0.0, None
else:
    n1 = len(w1)
    avg1 = sum(x[1] for x in w1) / n1
    ago = (now - w1[-1][0]).total_seconds() / 60

pending = None
if last_beat:
    pending = int(last_beat[1].get('pending', -1))

if f1:
    issues_bad.append(f"有 {len(f1)} 次 flush had failures 错误")
if not w1:
    issues_bad.append("30 分钟窗口内无任何 flush 记录")
elif all(x[1] == 0 for x in w1):
    issues_bad.append("所有 flush 节点数 = 0")
if not container_ok:
    issues_bad.append("容器不健康或状态不可得")
if pending is not None and pending > 20:
    issues_bad.append(f"Pending = {pending} > 20")
if ago is not None and ago > 15:
    issues_bad.append(f"距上次 flush {ago:.0f} 分钟 > 15")

if n1 and n1 <= 4 and not w1 == []:
    issues_warn.append(f"Flush 次数偏少（{n1} 次，目标 5-6）")
if w1 and avg1 < 3:
    issues_warn.append(f"平均节点数偏低（{avg1:.1f} < 3）")
if pending is not None and 10 < pending <= 20:
    issues_warn.append(f"Pending = {pending}（10-20）")
if ago is not None and 8 < ago <= 15:
    issues_warn.append(f"距上次 flush {ago:.0f} 分钟（8-12+）")

# 窗口完整性：容器中途重启会把 30 分钟窗口截短（flush 次数偏低是伪影）
partial_note = ""
if w1:
    span_min = (now - w1[0][0]).total_seconds() / 60
    if span_min < 25:
        partial_note = f"\n注：数据仅覆盖自首条记录起 ~{span_min:.0f} 分钟（容器中途启动/重启 ⇒ Flush 次数偏低是窗口伪影，不是回归）"

if issues_bad:
    verdict1 = 'bad'
elif issues_warn:
    verdict1 = 'warn'
else:
    verdict1 = 'ok'

print("━" * 62)
print(f"【被动指纹健康检查】{now.strftime('%Y-%m-%d %H:%M')} UTC  容器: {container}")
print("━" * 62)
print()
print(f"状态：{fmt_verdict(verdict1)} " + {'ok': '健康', 'warn': '警告', 'bad': '异常'}[verdict1])
if issues_bad:
    for i in issues_bad:
        print(f"  ❌ {i}")
if issues_warn:
    for i in issues_warn:
        print(f"  ⚠️ {i}")
print()
print("最近30分钟统计：")
if ago is None:
    print("- Flush次数：0 次")
    print("- 最后flush：窗口内无记录")
else:
    print(f"- Flush次数：{n1} 次")
    print(f"- 平均节点数：{avg1:.1f} 个/flush")
    print(f"- 最后flush：{ago:.0f} 分钟前")
print(f"- Pending积压：{pending if pending is not None else '无心跳记录'}")
print(f"- Flush失败：{len(f1)} 次")
print()
print(f"容器状态：{container_str}")
print(f"版本：{ver_str}")
print(partial_note)

rc = {'ok': 0, 'warn': 1, 'bad': 2}[verdict1]

# ── 第二部分：深度数据质量（2 小时窗口）───────────────────────────────
if deep:
    total = sum(x[1] for x in flushes)
    n2 = len(flushes)
    avg2 = total / n2 if n2 else 0.0
    rate = total / 2
    succ = len(flushes)
    fail = len(failures)
    succ_rate = 100.0 if fail == 0 else succ / (succ + fail) * 100.0

    if total >= 120 and succ_rate == 100.0 and (pending is None or pending == 0):
        rating, rc2 = 'ok', 0
    elif total >= 60 and succ_rate >= 95.0:
        rating, rc2 = 'warn', 1
    else:
        rating, rc2 = 'bad', 2
        if total < 60:
            pass  # 不达标主因在总节点数或成功率，报告正文已给数字
    rc = max(rc, rc2)

    print()
    print("━" * 62)
    print(f"【被动指纹数据质量分析】{now.strftime('%Y-%m-%d %H:%M')} UTC")
    print("━" * 62)
    print()
    print(f"评级：{fmt_verdict(rating)} " + {'ok': '优秀（目标达成）', 'warn': '合格（可接受）', 'bad': '不达标（需优化）'}[rating])
    print()
    print("2小时性能指标：")
    print(f"- 总Flush次数：{n2} 次（成功率 {succ_rate:.0f}%）")
    print(f"- 总写入节点：{total} 个（目标 ≥120）")
    print(f"- 平均节点/flush：{avg2:.1f} 个（目标 ≥5）")
    print(f"- 写入速率：{rate:.0f} 节点/小时")
    print(f"- Flush失败：{fail} 次")
    print()
    print("累计统计（最新心跳）：")
    if last_beat:
        b = last_beat[1]
        print(f"- nodes_written_since_last_beat：{b.get('nodes_written_since_last_beat')}")
        print(f"- pending：{b.get('pending')}")
        print(f"- ticks：{b.get('ticks')}")
    else:
        print("- 窗口内无心跳记录（容器运行不足 30 分钟？）")
    print()
    if n2 >= 4:
        half = n2 // 2
        a = sum(x[1] for x in flushes[:half]) / half
        b2 = sum(x[1] for x in flushes[half:]) / (n2 - half)
        d = Counter(x[1] for x in flushes)
        print("趋势分析：")
        print(f"- 前半段平均 {a:.1f} → 后半段平均 {b2:.1f} 个/flush")
        print("- 节点数分布：" + "，".join(f"{k}节点×{v}" for k, v in sorted(d.items())))
        if b2 >= a:
            print("- 趋势稳定或递增 ✅")
        else:
            print("- 后半段走低（结合时段流量判断，深夜低谷属正常）")
    print()
    print("结论：" + {
        'ok':   "系统达到业务目标：写入量、成功率、积压全部达标。",
        'warn': "系统可接受：成功率与积压健康，写入量处于目标带下沿（结合时段流量判读）。",
        'bad':  "不达标：写入量或成功率低于目标带，需按监控指南『故障排查』章节处置。",
    }[rating])

sys.exit(rc)
PY
