#!/bin/bash
# 252 PG17 URSM 快照写入健康巡检（三态判据：正常 / 单写降级 / 真空洞）
#
# 为什么要单独一个脚本，不并进 pg17-index-bloat.sh：
#   那个判的是「空间浪费」（索引膨胀），本脚本判的是「写入是否发生」。
#   两者是不同面 —— 一张表可以空间很健康，但连续 100 分钟一行都没写。
#
# === 判据：落库速率是三态的，不是有/无两态 ===
#   2026-10-03 逐分钟实测（journal 与数据层逐分钟精确咬合）：
#     20:05|2540  20:08|2536  20:09|  0  20:10|1267  20:11|  0  20:12|2532
#     21:15|1250  21:16|1250  21:17|2500  21:24|2508
#   对应 journal：20:09/10/11 collect failed，21:14/15/16 flush failed。
#
#   正常   ~2530 行/分   两台都成功（154 authoritative + 245 shadow 双写）
#   降级   ~1250 行/分   一台失败、另一台顶上 ⇒ **数据不丢，但写入量减半**
#   真空洞     0 行/分   两台同时失败 ⇒ 真丢数据
#
#   ★ 把「降级」和「真空洞」混成一个「空洞」是会误导人的：
#     降级只影响当日存储写入量（减半），真空洞才影响正确性。
#     两者的处置优先级完全不同。
#
#   ★ 成因（已由 error 原文实测坐实，不是推断）：
#     路径 A「collect 超时」：`redis scan failed: context deadline exceeded`
#       —— URSM 与 158 万会话键共用 db2，SCAN 必须走完整个键空间，
#          实测 12.95~30.40s，正面顶在 writer 的 30s 预算上。
#     路径 B「Flush 无预算」：`insert row ...: timeout: context already done`
#       —— Collect 与 Flush 共用同一个 30s ctx（cmd/gateway/main.go），
#          Collect 吃满后 Flush 拿不到时间，事务回滚、零提交。
#     实测失败率：154 = 3.0%（193 committed / 6 failed），245 = 0%。
#     迁 URSM 到独立 db 后 SCAN 实测 0.11s，**两条路径同时消失**。
#     所以本脚本的告警是迁键收益的可验证指标，不是纯观测。
#
# === 为什么用相对判据而不是写死 500/1800 ===
#   绝对阈值建立在「当前是双写」这个假设上，而 245 退出 shadow 后
#   正常态会变成 ~1242、降级态变成 ~620，写死阈值会全量误报。
#   故主判据用「相对当窗中位数」，绝对值只作参考输出。
#   报告仍会打印 p50 本身，让人能一眼看出当前是双写还是单写。
#
# === 只读 ===
#   本脚本**不写任何数据**，只有 --report 一种模式。
#   部署：把本文件放到 252 的 /opt/scripts/，cron 里加一行即可。
set -euo pipefail

CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
[ -f "$CONF" ] && source "$CONF"
: "${CONTAINER:=pg-252-pg17}"
: "${PG_USER:=llm_gateway}"
: "${PG_DB:=llm_gateway}"

# 回看窗口（小时）。默认 3 —— 要够长到能覆盖昼夜节律的低谷段，
# 又要短到单次查询仍然便宜（实测 3 小时约 45s，见下方性能注记）。
: "${WINDOW_HOURS:=3}"

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }

# 必须吞掉 psql 的退出码：stderr 已并入 stdout，错误文本会作为「取值」流到下面的
# 显式检查里。没有这层时，`x=$(de ...)` 会因 psql 返回非零被 set -e 直接掐死 ——
# 那样日志停在 start、退出码 1、零诊断，运维只看到「脚本挂了」而不是「量具不可用」。
de() { podman exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -c "$1" 2>&1 || true; }

log "start ursm-snapshot-health mode=report window=${WINDOW_HOURS}h"

# === 对照断言：先证明量具本身可用 ====================================
# 不做这一步，「0 个真空洞」在「真的没有」与「量具根本跑不通」
# （表被改名、权限不足、连接错库、时区错）之间不可区分 ——
# 那是一个恒真的假绿灯。判据能报绿，不代表它有分辨力。
control=$(de "SELECT coalesce(sum(rows),0) FROM (
              SELECT count(*) AS rows FROM ursm_node_snapshot_min
              WHERE snapshot_ts > now() - interval '10 minutes'
              GROUP BY date_trunc('minute', snapshot_ts)) t;")
if [ "${control:0:6}" = "ERROR" ] || [ -z "$control" ]; then
  log "ABORT: control query failed: ${control//$'\n'/ }"
  echo "ABORT: 量具自检失败，无法区分「真没问题」与「量具跑不通」。" >&2
  exit 1
fi
# 快照每 60s 一批，单批约 1.2K 行 ⇒ 最近 10 分钟正常态至少有几千行。
# 若这里接近 0，要么真的刚发生长时间停写，要么量具坏了 —— 两种都必须喊人。
if [ "$control" -lt 1000 ] 2>/dev/null; then
  log "CONTROL-WARN: 最近 10 分钟仅 ${control} 行（正常应 >= 几千）"
  echo "⚠️  量具自检偏低：最近 10 分钟只有 ${control} 行。" >&2
  echo "    这可能是真的长时间停写，也可能是量具坏了。人工确认后再看下面的三态。" >&2
fi
log "control ok: rows_in_last_10min=$control"

# === 主判据：逐分钟三态 ==============================================
# ★ 必须用 generate_series 生成完整分钟序列再 LEFT JOIN 实际数据。
#   只 GROUP BY 实际存在的 snapshot_ts，空缺的那一分钟**根本不会出现**，
#   于是「真空洞」在结果里彻底消失 —— 巡检会报「0 个真空洞」而真空洞正在发生。
#   这是本脚本最容易写错、且错了还不报错的地方。
#   （本脚本 2026-10-03 的实测正是靠这个序列才把 20:09/20:11 抓出来。）
rows=$(de "
WITH bounds AS (
  SELECT date_trunc('minute', now() - make_interval(hours => $WINDOW_HOURS)) AS lo,
         date_trunc('minute', now())                                          AS hi_excl,
         date_trunc('minute', now()) - interval '1 minute'                   AS hi_incl
),
-- ★ series 与 per_min 必须覆盖**同一批完整分钟**，否则边界那一分钟会恒为 0，
--   变成「每次运行都必然多报一个 VOID」的恒真假阳性。两次踩坑记录：
--     ① series 用 generate_series(lo, hi)（含 hi）而 per_min 用 < hi（排除）
--        ⇒ hi 那一分钟永远 0 行
--     ② 改成 <= hi 后，hi 落到「该分钟的第 0 秒」——批在 :17/:47 秒，
--        那一刻同样没有任何数据 ⇒ 假阳性只是换了个小时（23:35 → 23:36）
--   正解：序列取 [lo, 上一完整分钟]，数据取 [lo, 当前分钟)。
series AS (SELECT generate_series(lo, hi_incl, '1 minute') AS m FROM bounds),
per_min AS (
  SELECT date_trunc('minute', snapshot_ts) AS m, count(*) AS c
  FROM ursm_node_snapshot_min, bounds
  WHERE snapshot_ts >= bounds.lo AND snapshot_ts < bounds.hi_excl
  GROUP BY 1
),
joined AS (
  SELECT s.m, coalesce(p.c, 0) AS c FROM series s LEFT JOIN per_min p ON p.m = s.m
),
stats AS (
  SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY c) FILTER (WHERE c > 0) AS p50
  FROM joined
)
SELECT
  (SELECT count(*) FROM joined)                                    AS minutes_total,
  (SELECT count(*) FROM joined WHERE c = 0)                         AS void_minutes,
  (SELECT count(*) FROM joined WHERE c > 0 AND c < 0.7 * p50)       AS degraded_minutes,
  (SELECT count(*) FROM joined WHERE c >= 0.7 * p50)                AS normal_minutes,
  round((SELECT p50 FROM stats))                                    AS p50,
  round((SELECT max(c) FROM joined))                                AS peak,
  (SELECT min(snapshot_ts) FROM (SELECT m, c FROM joined WHERE c > 0 ORDER BY m LIMIT 1) x
     JOIN (SELECT min(snapshot_ts) AS snapshot_ts FROM ursm_node_snapshot_min) y ON true) AS first_ok
FROM stats;")

if [ "${rows:0:6}" = "ERROR" ] || [ -z "$rows" ]; then
  log "ABORT: main query failed: ${rows//$'\n'/ }"
  echo "ABORT: 主判据查询失败。" >&2
  exit 1
fi
log "summary: ${rows//$'\n'/ }"

minutes_total=$(echo "$rows"  | awk -F'|' '{print $1+0}')
void_minutes=$(echo "$rows"     | awk -F'|' '{print $2+0}')
degraded=$(echo "$rows"         | awk -F'|' '{print $3+0}')
normal=$(echo "$rows"           | awk -F'|' '{print $4+0}')
p50=$(echo "$rows"              | awk -F'|' '{print $5+0}')

echo
echo "===== URSM 快照写入健康（最近 ${WINDOW_HOURS} 小时）====="
printf '  时间窗口分钟数 : %s\n' "$minutes_total"
printf '  正常           : %s 分钟\n' "$normal"
printf '  单写降级       : %s 分钟  (数据不丢, 写入量减半)\n' "$degraded"
printf '  真空洞         : %s 分钟  (真丢数据)\n' "$void_minutes"
printf '  中位速率 p50   : %s 行/分   峰值: %s\n' "$p50" "$(echo "$rows" | awk -F'|' '{print $6+0}')"
echo
if [ "$p50" -gt 1800 ] 2>/dev/null; then
  echo "  当前基线推断   : 双写（154 authoritative + 245 shadow）"
elif [ "$p50" -gt 600 ] 2>/dev/null; then
  echo "  当前基线推断   : 单写（245 可能已退出 shadow）"
else
  echo "  当前基线推断   : 异常低，请人工确认写入是否正常"
fi

# === 异常分钟明细（最多 20 条）===
echo
echo "----- 异常分钟明细（真空洞 + 降级，最多 20 条）-----"
detail=$(de "
WITH bounds AS (
  SELECT date_trunc('minute', now() - make_interval(hours => $WINDOW_HOURS)) AS lo,
         date_trunc('minute', now())                                          AS hi_excl,
         date_trunc('minute', now()) - interval '1 minute'                   AS hi_incl
),
series AS (SELECT generate_series(lo, hi_incl, '1 minute') AS m FROM bounds),
per_min AS (
  SELECT date_trunc('minute', snapshot_ts) AS m, count(*) AS c
  FROM ursm_node_snapshot_min, bounds
  WHERE snapshot_ts >= bounds.lo AND snapshot_ts < bounds.hi_excl GROUP BY 1
),
joined AS (SELECT s.m, coalesce(p.c,0) AS c FROM series s LEFT JOIN per_min p ON p.m=s.m),
stats AS (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY c) FILTER (WHERE c>0) AS p50 FROM joined)
SELECT to_char(m,'MM-DD HH24:MI'), c,
       CASE WHEN c=0 THEN 'VOID' WHEN c < 0.7*p50 THEN 'DEGRADED' ELSE 'ok' END
FROM joined, stats WHERE c < 0.7 * p50 ORDER BY m LIMIT 20;")
if [ "${detail:0:6}" = "ERROR" ]; then
  echo "  (明细查询失败: ${detail//$'\n'/ })"
elif [ -z "$detail" ]; then
  echo "  无异常分钟"
else
  printf '%s\n' "$detail" | awk -F'|' '{printf "  %-14s %7s 行  %s\n", $1, $2, $3}'
fi

# === 告警 ===
echo
if [ "$void_minutes" -gt 0 ]; then
  echo "🚨 真空洞 ${void_minutes} 分钟 —— 两台同时未提交，已丢数据。" >&2
  echo "   优先排查：redis SCAN 超时（路径 A）与 Collect 挤占 Flush 预算（路径 B）。" >&2
  echo "   迁 URSM 到独立 db 是两条路径的共同解（SCAN 0.11s）。" >&2
  exit 2
fi
if [ "$degraded" -gt 0 ]; then
  echo "⚠️  单写降级 ${degraded} 分钟 —— 数据未丢，但当日写入量减少了。" >&2
  exit 1
fi
log "OK: 无真空洞，无降级"
exit 0
