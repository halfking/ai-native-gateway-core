#!/usr/bin/env bash
# §143.80 取证脚本：从 154 网关服务日志里量化【压缩器冷启动读链】的服务侧表现。
#
# ★★★ 为什么需要这个脚本（★ 连续三轮的诚实边界未验项）：
#   §143.77 / §143.78 / §143.79 三轮都只用了【库侧证据】（pg_stat_statements），
#   并在各自的「诚实边界」里登记「★ 本轮仍未用 154 的服务日志做交叉验证」。
#   ⇒ ⇒ 本脚本把那三轮的未验项补上。
#
# ★★★ 它读的是什么（★ 关键）：
#   154 上跑的是 `llm-gateway-go-canary@8782.service`（蓝绿候选槽；★ `llm-gateway-go`
#   这个服务名在 154 上是 **masked** ⇒ ★ 别找错）。
#   ★ 日志里的黄金信号是：
#     {"msg":"session_compressor: v2 cache loaded successfully","session":"...","body_size":N}
#   ⇒ ★★ `body_size` 就是【冷启动读链取到的正文长度】：
#     ★ `body_size = 0` ⇒ **语义落空**（★ 这正是 §143.79 说的
#       「量具 rows 记 1、但 Go 侧判空」在【服务侧】的对应物）
#     ★ `body_size > 0` ⇒ 真的取到了正文
#
# 用法（★ 只读；★ 不改任何生产状态）：
#   bash scripts/audit/verify_gateway_log_coldstart_evidence.sh [分钟窗口，默认 80]
#
# ★ 诚实边界：
#   ★ 服务名与槽位是【154 的当前形态】⇒ 若部署换槽（蓝绿切换），脚本要改端口参数。
#   ★ 窗口越短样本越少 ⇒ 本脚本只报【分布】，不报【比率结论】（★ 比率要看 §143.80 的结论）。
# ★★ 关于退出码（★ 本轮踩到的真缺陷，★ 别改回去）：
#   ★ 本轮一直用 `bash lgwp-prod.sh … | tail -N` 然后 `echo $?`
#   ★ ⇒ ⇒ ★★ **`$?` 拿到的是 `tail` 的退出码，不是 psql 的！**
#   ★ ⇒ ⇒ ★★ 于是「rc=0」这个判据一直是【假的】⇒ ⇒ 一个真报错的脚本也会被读成「跑通了」
#   ★ ⇒ ⇒ ★★ 本轮就有一个脚本真的报了 `syntax error at or near "UNION"`，而当时的判据说它 rc=0
#   ★ ⇒ ⇒ ⇒ ★★ **修法：`set -o pipefail` + 显式捕获脚本的退出码**（★ 本脚本的 ssh 不用管道，
#   ★⇒ ⇒ 但仍显式捕获，保证任何未来的管道改动都不会吞掉退出码）
set -euo pipefail
set -o pipefail

WINDOW_MIN="${1:-80}"
SSH_TIMEOUT="${SSH_TIMEOUT:-240}"

# ★ timeout 可移植（2026-10-23 12h 审计轮修）：
#   ★ 原写法直接用 `timeout "$SSH_TIMEOUT" ssh ...`，而 **macOS 自带没有 GNU
#   coreutils 的 `timeout`**（本机实测 `timeout: command not found`）
#   ⇒ ⇒ 在 macOS 上整脚本跑不起来（`set -e` ⇒ 退出 127）⇒ ⇒ 取证脚本等于没有。
#   ⇒ 优先 `timeout`（GNU/Linux）→ 其次 `gtimeout`（brew coreutils）
#   ⇒ 都没有就**直接跑**（只失去「SSH 超时上限」这一个保护，脚本本身仍可用）
#   ⇒ ⇒ 并把这件事**显式说出来**（不能让「没有超时保护」被当成「有」）。
if command -v timeout >/dev/null 2>&1; then
  _timeout_cmd=(timeout "$SSH_TIMEOUT")
elif command -v gtimeout >/dev/null 2>&1; then
  _timeout_cmd=(gtimeout "$SSH_TIMEOUT")
else
  _timeout_cmd=()
  echo "WARN: 本机没有 timeout/gtimeout ⇒ 去掉 SSH 超时上限（SSH_TIMEOUT=${SSH_TIMEOUT} 不生效）" >&2
fi

# ★ 自证：窗口参数必须是正整数（★ 避免 "80 min ago" 被拼成非法表达式而静默空跑）
case "$WINDOW_MIN" in
  ''|*[!0-9]*) echo "window must be a positive integer (minutes), got: $WINDOW_MIN" >&2; exit 2 ;;
esac
[ "$WINDOW_MIN" -gt 0 ] || { echo "window must be > 0" >&2; exit 2; }

echo "=== §143.80 取证：154 网关日志（★ 只读；窗口 ${WINDOW_MIN} 分钟）==="

"${_timeout_cmd[@]}" ssh 154 "set +u;
  journalctl -u llm-gateway-go-canary@8782.service --no-pager --since '${WINDOW_MIN} min ago' 2>/dev/null \
  | grep 'session_compressor: v2 cache loaded successfully' > /tmp/p14380_v2cache.txt || true
  echo '--- 采集到的冷启动条数 ---'
  wc -l < /tmp/p14380_v2cache.txt
  echo '--- body_size 分布（★ 0 = 语义落空）---'
  grep -oE '\"body_size\":[0-9]+' /tmp/p14380_v2cache.txt | sort | uniq -c | sort -rn | head -12
  echo '--- 会话形态分类（★ ★ 关键：一次性会话 vs 固定长会话）---'
  python3 - <<'PYEOF'
import re, collections
recs = []
for l in open('/tmp/p14380_v2cache.txt', encoding='utf-8', errors='replace'):
    m = re.search(r'\"session\":\"([^\"]+)\"', l)
    z = re.search(r'\"body_size\":(\d+)', l)
    if m and z:
        recs.append((m.group(1), int(z.group(1))))
if not recs:
    print('NO_RECORDS  ★ 窗口内没有该信号（★ 可能是服务刚重启或日志已轮转）')
    raise SystemExit(0)

def cls(s):
    if 'omp-global-kxpms' in s:
        return 'A_omp_fixed_long'
    if re.match(r'^gw_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\$', s):
        return 'B_gw_uuid_oneshot'
    if re.match(r'^gt_gw_[0-9a-f]{8}-', s):
        return 'C_gt_gw_branch'
    if s.startswith('gs_'):
        return 'D_gs_prefix'
    return 'E_other'

agg = collections.defaultdict(lambda: [0, 0])
dist = collections.defaultdict(set)
for s, z in recs:
    c = cls(s); agg[c][0] += 1
    if z == 0:
        agg[c][1] += 1
    dist[c].add(s)
print('total_records=%d distinct_sessions=%d' % (len(recs), len(set(s for s, _ in recs))))
print()
print('%-20s %8s %8s %10s %10s' % ('class', 'records', 'body=0', 'pct_zero', 'distinct'))
for c in sorted(agg):
    t, z = agg[c]
    print('%-20s %8d %8d %9.2f%% %10d' % (c, t, z, (100.0 * z / t) if t else 0, len(dist[c])))
non = [(s, z) for s, z in recs if cls(s) != 'A_omp_fixed_long']
zn = sum(1 for s, z in non if z == 0)
print()
print('★★★ EXCLUDING_omp: records=%d body0=%d pct_zero=%.2f%%' % (len(non), zn, (100.0 * zn / len(non)) if non else 0))
print('★★★ coldstarts_per_distinct_session=%.2f' % (len(recs) / float(len(set(s for s, _ in recs)))))
PYEOF
  rm -f /tmp/p14380_v2cache.txt
"
# ★★★ 显式捕获 ssh 的真实退出码（★ ★ 不用 `$?` 于任何管道之后）
SSH_RC=$?
echo "===== REAL_SSH_RC=${SSH_RC} ====="
exit "${SSH_RC}"
