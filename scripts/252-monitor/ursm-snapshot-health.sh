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
# === 为什么用「同时段历史基线」而不是「当窗 p50/p90」 ===
#   ★ 这一版换过一次基线，换的原因是实测出的**假阴性**，不是审美：
#     旧版（窗口内 p90）在 10-03 24h 窗口下把 06:00 的 2,518 行判成
#     「TROUGH 昼夜低谷 · 正常节律 · 不告警」，而同一小时在 10-01/10-02
#     的实测是 123,059 / 108,565 行 —— **差 44~47 倍**，却被判成正常。
#     根因：昼夜节律跨度 58 倍，**窗口内的分位数根本不是同一个量纲**。
#     凌晨的低谷会把 p50 拉到谷底、白天高峰把 p90 拉到峰顶，
#     于是「凌晨的桶」永远落在 0.2*p90 以下被判 TROUGH，
#     「白天的桶」又常常落在 0.7*p90 以下被判 DEGRADED。
#     两头都错：真故障被叫成正常节律，正常波动被叫成降级。
#
#   现版基线 = **同一个小时位在最近 N 天里的中位数**（slot baseline）：
#     判「10-03 06:00 这个桶」时，拿 09-26~10-02 各天的 06:00 去比，
#     而不是拿 10-03 当天别的时段去比。**同量纲，才可比。**
#     降级阈值 DEGRADED_RATIO=0.5，直接对应「双写 2.00× ⇒ 单写 ≈ 0.5」。
#
#   ★ 换基线后必须重跑反向对照，否则就是又一次「换了量具就宣布没问题」：
#     2026-10-03 实测（7 天同时段基线）：
#       10-01 整天 24 个桶 → 24 ok（0 降级、0 空洞）
#       10-02 整天 24 个桶 → 22 ok + 1 DEGRADED(22:00, 34,458 vs ref 116,027)
#                              + 1 VOID(23:00)
#       10-03 整天 24 个桶 → 7 ok + 16 DEGRADED + 1 VOID(11:00)
#     ⇒ 判据在历史正常日**不响**（不是恒假），在当天**精确命中**（不是恒真）。
#     ⇒ 而 10-03 那 16 个降级桶是**真降级**，不是判据误报：
#        06:00 实得 2,518 行 vs 同时段常态 118,467 行。这与 §5.2 记录的
#        「10-03 上午写入量断崖」是同一件事，独立复核请看 §5.6。
#
# === 为什么按小时而不是按分钟 ===
#   2026-10-03 实测踩过：最初按分钟判定，24 小时窗口下报出「真空洞 921 分钟」
#   （占 64%），而那 921 分钟里绝大多数**根本不是故障** ——
#   凌晨是**突发式**写入，不是「低速但连续」：
#     10-03 01:00~01:20 这 20 分钟里，只有 01:16 有数据（2190 行），
#     其余 19 分钟全是 0 行。
#   但按小时看，凌晨每小时都有数据（00:00=14354 / 01:00=20694 / 02:00=10704），
#   而真正的 100 分钟空洞（10-03 10:20~12:00）在小时级清清楚楚：
#     10:00=4804   11:00=(整小时缺失)   12:00=10710
#   ⇒ **「一个计量桶为 0」只有在桶的粒度 ≥ 最小写入间隔时才可信。**
#     批间隔是 60s，但凌晨的写入是突发式的，分钟桶不满足这个前提；
#     小时桶满足。
#   ⇒ minute 粒度仍保留（用于对判为异常的桶**下钻**），不作巡检判定。
#     注意 minute 模式下同时段基线只有 7 个样本，中位数不稳，仅供调试。
#
# === 为什么必须区分「次数塌陷」和「速率下降」 ===
#   §5.6 那次 24 小时事件的指纹是：**有数据的那些分钟速率完全正常**
#   （单分钟中位 2,100~2,570，仍是双写的 ~2,400 量级），
#   塌掉的是**落库次数**（60/60 分钟 → 1~20/60）。
#   旧判据只看「每小时总行数」，会把下面两种根因完全不同的形态压成同一个「降级」：
#     SPARSE 次数塌陷 —— collect/Flush 整批失败（§5.3.1 路径 A/B）
#     SLOW   速率下降 —— 单批变小 / 流量或采样变稀
#   两者修法不同：前者指向 redis SCAN 超时与 ctx 预算，后者指向双写是否只剩单写
#   或采样率是否被动过。所以判据必须能分开报，退出码可以相同。
#   反向对照实测（NOW_EXPR，见 §5.7.6）：
#     10-01 12:00 → 0/0/0            exit 0（正常日不响）
#     10-03 08:00 → 3 SPARSE         exit 1（4/60、1/60、2/60 活跃分钟）
#     10-03 12:00 → 2 SPARSE + 1 VOID  exit 2
#   ⚠ 数据集里**没有**天然的 SLOW 样本，所以 SLOW 这一路**未被反向对照覆盖**。
#     它有代码路径但没有实测证据，将来真出现时要看它的判读是否合理。
#
# === 退出码（cron 与人工都能据此分流）===
#   0  正常
#   1  有单写降级桶（数据不丢，写入量低于同时段常态）
#   2  有真空洞桶（真丢数据）
#   3  **量具不可用**（SQL 失败 / 返回形状不对 / 基线取空）
#      —— 必须与 0 严格区分。见下方「量具自检」段。
#   64 参数错误
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

# 判定窗口（小时）。默认 3 —— cron 每小时跑一次时，3 小时窗能覆盖
# 「上一轮跑完之后到现在」的完整区间，不会漏掉只持续一小时的空洞。
: "${WINDOW_HOURS:=3}"

# 同时段基线回看天数。默认 7 —— 与快照保留天数一致，
# 超过保留期的数据已被 retention 删掉，取更长不会让基线更准，只会变慢。
: "${BASELINE_DAYS:=7}"

# 「现在」的可覆盖表达式，默认 now()。
# ★ 存在的唯一理由：**反向对照要能重跑**。
#   判据只查「最近 N 小时」的话，只能在当前时刻跑一次，
#   于是「历史正常日会不会误报」永远只能靠一次性的手写 SQL 验证 ——
#   换基线、改阈值之后那套对照就作废了，又变成「换了量具就宣布没问题」。
#   覆盖它就能让**同一个脚本**跑在已知合格/不合格的历史时刻上：
#     NOW_EXPR="timestamp '2026-10-01 12:00'" bash ursm-snapshot-health.sh
#   覆盖时同样只读，且 BASELINE_DAYS 会相对该时刻回看。
: "${NOW_EXPR:=now()}"

# 降级阈值：实得 < 阈值 × 同时段基线中位数 ⇒ 判降级。
# 0.5 对应「双写 2.00×，掉一台即半写」（b2077c204 实测定论）。
: "${DEGRADED_RATIO:=0.5}"

# 「落库次数塌陷」的判据（§5.6 事件形态）。需要**同时**满足两条才算塌陷：
#   ① 活跃分钟数 < SPARSE_MIN_RATIO × 同时段基线活跃分钟数
#   ② 但「每个活跃分钟的行数」仍 ≥ SPARSE_KEEP_RATIO × 基线
# ② 的作用是把「次数塌陷」与「速率下降」分开：
#   只满足 ①（活跃分钟少**且**每次也写得少）⇒ 判 SLOW 而不是 SPARSE，
#   因为那更像「流量本身变少 / 采样变稀」，不是「整批没落库」。
: "${ACTIVE_DAYS:=3}"

# 「落库次数塌陷」的两个比值阈值
: "${SPARSE_MIN_RATIO:=0.5}"
: "${SPARSE_KEEP_RATIO:=0.5}"

# 本脚本只读会话的 statement_timeout（应用角色的 30s 不够用，见 de() 注释）
: "${STMT_TIMEOUT:=180s}"

# 统计粒度。默认 hour，理由见文件头「为什么按小时而不是按分钟」。
: "${GRANULARITY:=hour}"
case "$GRANULARITY" in
  # ★ 两套 slot 表达式：表里按 snapshot_ts 算（基线 CTE 用），
  #   joined 里已经看不到表了，得按桶列 s.m 算。踩过：两处共用一个表达式，
  #   joined 里 snapshot_ts 不在作用域 ⇒ ERROR: column "snapshot_ts" does not exist。
  #
  # ★ G_ACTIVE：桶内「有多少个子单位真的有数据」。这是 §5.6 事件的直接指纹 ——
  #   事件期间**有数据的那些分钟速率完全正常**（~2,400/分），
  #   塌掉的是「落库的次数」（60/60 分钟 → 1~20/60）。
  #   只看「每小时总行数」会把两种根因不同的形态压成同一个「降级」：
  #     次数塌陷（collect/Flush 整批失败）vs 速率下降（单批变小 / 采样变稀）
  #   两者修法不同，判据必须能分开。
  #   minute 粒度下每个桶只含 1 个子单位 ⇒ 恒为 1，该判据自动失效（设计如此）。
  hour)
    G_UNIT="hour"; G_TRUNC="date_trunc('hour',"; G_LABEL="HH24:00"; G_STEP="1 hour"; G_IV="interval '1 hour'"
    G_SLOT_TS="extract(hour from date_trunc('hour', snapshot_ts))::int"
    G_SLOT_M="extract(hour from s.m)::int"
    G_ACTIVE="count(DISTINCT date_trunc('minute', snapshot_ts))"; G_MAXA=60 ;;
  minute)
    G_UNIT="minute"; G_TRUNC="date_trunc('minute',"; G_LABEL="HH24:MI"; G_STEP="1 minute"; G_IV="interval '1 minute'"
    G_SLOT_TS="(extract(hour from snapshot_ts)::int * 60 + extract(minute from snapshot_ts)::int)"
    G_SLOT_M="(extract(hour from s.m)::int * 60 + extract(minute from s.m)::int)"
    G_ACTIVE="1::bigint"; G_MAXA=1 ;;
  *) echo "GRANULARITY 只能是 hour 或 minute，收到: $GRANULARITY" >&2; exit 64 ;;
esac

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }

# 必须吞掉 psql 的退出码：stderr 已并入 stdout，错误文本会作为「取值」流到下面的
# 显式检查里。没有这层时，`x=$(de ...)` 会因 psql 返回非零被 set -e 直接掐死 ——
# 那样日志停在 start、退出码 1、零诊断，运维只看到「脚本挂了」而不是「量具不可用」。
#   PRINT_SQL=1 时只回显 SQL 不执行 —— 用来把生成的判据语句抠出来单独跑，
#   避免「脚本里拼错了但看不出拼成什么样」。
de() {
  # 回显走 stderr：`x=$(de ...)` 会把 stdout 收进变量，回显混进去会污染取值。
  if [ -n "${PRINT_SQL:-}" ]; then printf '%s\n' "$1" >&2; printf '0\n'; return 0; fi
  # ★ 每条查询单独放宽 statement_timeout。
  #   本脚本以应用角色 llm_gateway 连库，而该角色在库上带
  #   `statement_timeout=30s`（应用侧的保护，正常写链路确实需要）。
  #   而基线聚合要扫 7 天 ~600 万行，实测单独就要 11s，
  #   叠加窗口查询在有负载时超过 30s ⇒ 报 `canceling statement due to
  #   statement timeout`，脚本按设计判 exit 3（量具不可用）。
  #   ⇒ 「每小时都失败一次的健康巡检」等于没有巡检。
  #   这里只对本脚本自己的**只读**会话放宽，不改角色默认值，
  #   应用侧的 30s 保护原样保留。
  #   ★ 用 PGOPTIONS 而不是 `psql -c "SET ...; <sql>"`：
  #     SET 会回显自己的命令标签（输出里多一行 `SET`），
  #     而下游 `x=$(de ...)` 是按「单行取值」解析的，
  #     多这一行会直接触发「返回多行，无法解析」的 exit 3。
  #     PGOPTIONS 在连接建立时生效，不产生任何输出。
  podman exec -e "PGOPTIONS=-c statement_timeout=${STMT_TIMEOUT:-180s}" \
    "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -c "$1" 2>&1 || true
}

# 任一行以 ERROR/FATAL 开头 ⇒ psql 整条挂掉。
# ★ 必须扫**全文每一行**，不能只看前几个字符（${x:0:6}）。
#   旧版用 ${x:0:6}，多行输出下漏判，SQL 挂掉却 exit=0 报 OK ——
#   那是最危险的一种故障形态：**量具坏了却在报绿**。
sql_failed() { printf '%s\n' "$1" | grep -qE '^\s*(ERROR|FATAL)'; }

# 主判据与基线共用同一段桶表达式，改一处就够。
# ★ 序列取到**最后一个完整桶**（上一桶），不含当前这个还没走完的桶 ——
#   否则当前小时必然是「0 行」或「半截数据」，恒定多报一个 VOID。
# ★ 边界必须是**左闭右开** [lo, hi_excl)，且 series 与 cur 覆盖**同一段**。
#   这里的 hi_excl = 当前桶的起点，也就是「最后一个完整桶」= 上一桶。
#   踩坑记录（第 N 次同族错误，每一轮都换个形态出现）：
#     ① series 含当前未走完的桶、cur 不含 ⇒ 当前桶恒为 0 ⇒ 每次必报 1 个 VOID；
#     ② 改成 `<= hi_incl` 而 hi_incl = 上一桶的**起点** ⇒ 那一桶只被统计到
#        整点那一瞬间的若干行，其余全丢 ⇒ 上一桶恒接近 0 ⇒ 又报 1 个 VOID。
#        （本次实测：10-03 23:00 明明有 122,735 行，被算成 0 行 VOID；
#          同一时刻 24h 窗口的旧查询给出的是 122,735 —— 两个数对不上才暴露出来。）
#   正解：统一用右开区间，序列取到 hi_excl 往前一个 step。
BUCKETS_SQL="
WITH win AS (
  SELECT $G_TRUNC $NOW_EXPR - make_interval(hours => $WINDOW_HOURS)) AS lo,
         $G_TRUNC $NOW_EXPR)                                          AS hi_excl
),
series AS (SELECT generate_series(lo, hi_excl - $G_IV, '$G_STEP') AS m FROM win),
cur AS (
  SELECT $G_TRUNC snapshot_ts) AS m, count(*) AS c, $G_ACTIVE AS a
  FROM ursm_node_snapshot_min, win
  WHERE snapshot_ts >= win.lo AND snapshot_ts < win.hi_excl
  GROUP BY 1
),
joined AS (
  SELECT s.m, coalesce(c.c, 0) AS c, coalesce(c.a, 0) AS a, $G_SLOT_M AS slot
  FROM series s LEFT JOIN cur c ON c.m = s.m
),
-- 基线：同一批小时位在更早的 N 天里的**每个桶**各自计数，
-- 然后对每个小时位取中位数。
-- ★ 这里必须先按 (日期, 桶) 分组再取分位数。踩过的坑：
--   漏掉日期维度、直接对原始行 GROUP BY 小时位，得到的是
--   「该小时位 7 天的总行数」（~700,000），比真实单桶高 5 倍，
--   于是 10-01/10-02 这些正常日会被整片判成 DEGRADED（实测 25/25 全红）。
base_buckets AS (
  SELECT $G_SLOT_TS AS slot, count(*) AS c
  FROM ursm_node_snapshot_min
  WHERE snapshot_ts >= $NOW_EXPR - make_interval(days => $BASELINE_DAYS)
    AND snapshot_ts < (SELECT lo FROM win)
  GROUP BY 1, $G_TRUNC snapshot_ts)),
-- 活跃子单位的基线**单独用短窗口**算。
-- ★ 性能实测（252，7 天 ≈ 600 万行）：
--     count(DISTINCT date_trunc('minute', ts))  7 天 = 44.5s   ← 不可接受
--                                        2 天 =  9.7s
--                                        1 天 =  3.6s
--   而同一批数据只做 count(*) 是 7.2s ⇒ **DISTINCT 是全部增量**。
--   行数基线要 7 天（量纲敏感），但「这个小时位正常时该有多少分钟有数据」
--   是一个**比行数稳定得多**的量（干净日实测 53~60/60），短窗口足够。
base_active AS (
  SELECT $G_SLOT_TS AS slot, count(*) AS c, $G_ACTIVE AS a
  FROM ursm_node_snapshot_min
  WHERE snapshot_ts >= $NOW_EXPR - make_interval(days => $ACTIVE_DAYS)
    AND snapshot_ts < (SELECT lo FROM win)
    -- ★ 只算窗口里**真的用到**的小时位（3h 窗 ⇒ 3~4 个 slot，不是 24 个）。
    --   DISTINCT 的排序/哈希成本正比于分组数，砍掉 6~8 倍。
    --   不加这个过滤时主判据 32.9s，加上后实测见提交信息。
    AND $G_SLOT_TS IN (SELECT slot FROM joined)
  GROUP BY 1, $G_TRUNC snapshot_ts))
"
log "start ursm-snapshot-health mode=report now=${NOW_EXPR} window=${WINDOW_HOURS}h granularity=${G_UNIT} baseline=${BASELINE_DAYS}d ratio=${DEGRADED_RATIO}"

# === 量具自检 =========================================================
# 不做这一步，「0 个真空洞」在「真的没有」与「量具根本跑不通」之间不可区分。
control=$(de "SELECT coalesce(sum(rows),0) FROM (
              SELECT count(*) AS rows FROM ursm_node_snapshot_min
              WHERE snapshot_ts > $NOW_EXPR - interval '10 minutes'
              GROUP BY $G_TRUNC snapshot_ts)) t;")
if sql_failed "$control" || [ -z "$control" ]; then
  log "ABORT: control query failed: ${control//$'\n'/ }"
  echo "ABORT: 量具自检失败，无法区分「真没问题」与「量具跑不通」。" >&2
  exit 3
fi
log "control ok: rows_in_last_10min=$control"

rows=$(de "$BUCKETS_SQL,
-- 行数基线（7 天，只有 count(*)，便宜）
ref_rows AS (
  SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50
  FROM base_buckets GROUP BY 1
),
-- 活跃度基线（短窗口，需要 DISTINCT）
-- ★ a50 用 **max** 而不是中位数：「这个小时位正常时该有多少分钟有数据」是
--   **上界**问题，用 max 才不会被某个坏日子把基线拉低（那会让 SPARSE 变宽松）。
ref_act AS (
  SELECT slot,
         max(a) AS a50,
         -- 「每个活跃子单位写多少行」是中心量 ⇒ 中位数。
         -- a>0 才入样本，否则整点空桶会把基线拉到 0，让 SPARSE 恒不成立。
         percentile_cont(0.5) WITHIN GROUP (ORDER BY c::numeric / a)
           FILTER (WHERE a > 0) AS rate
  FROM base_active GROUP BY 1
),
-- 两段聚合各自独立算完再 1:1 连接。写成 `base_buckets b LEFT JOIN base_active a
-- ON a.slot=b.slot GROUP BY b.slot` 会产生 7×3 的笛卡尔积（虽然中位数碰巧不变，
-- 但那是「碰巧对」，不是「对」）。
slot_ref AS (
  SELECT r.slot, r.p50, t.a50, t.rate
  FROM ref_rows r LEFT JOIN ref_act t ON t.slot = r.slot
),
-- 逐桶先定判定，再聚合。五种判定互斥且穷尽，计数不会重复也不会漏。
-- ★ SPARSE 与 SLOW 的分界就是「次数塌陷」vs「速率下降」（见 DEGRADED_RATIO 上方注释）：
--     SPARSE = 活跃分钟腰斩，但**每一次落库的量仍然正常** ⇒ 整批没落库
--     SLOW   = 活跃分钟还行，但总量低 ⇒ 单批变小 / 流量或采样变稀
--   两者退出码相同（1），但指向的根因完全不同，报告里必须能分开看。
judged AS (
  SELECT j.c, j.a, r.p50 AS ref_rows, r.a50 AS ref_active, r.rate AS ref_rate,
         CASE WHEN r.slot IS NULL OR r.p50 <= 0 THEN 'NO_BASELINE'
              WHEN j.c = 0                          THEN 'VOID'
              WHEN j.c >= $DEGRADED_RATIO * r.p50    THEN 'ok'
              WHEN j.a <  $SPARSE_MIN_RATIO * r.a50
               AND (j.c::numeric / greatest(j.a, 1)) >= $SPARSE_KEEP_RATIO * r.rate
                                                     THEN 'SPARSE'
              ELSE 'SLOW' END AS v
  FROM joined j LEFT JOIN slot_ref r ON r.slot = j.slot
)
SELECT
  count(*),
  count(*) FILTER (WHERE v = 'VOID'),
  count(*) FILTER (WHERE v = 'SPARSE'),
  count(*) FILTER (WHERE v = 'SLOW'),
  count(*) FILTER (WHERE v = 'NO_BASELINE'),
  count(*) FILTER (WHERE v = 'ok'),
  round(percentile_cont(0.5) WITHIN GROUP (ORDER BY c) FILTER (WHERE c > 0)),
  round((SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY p50) FROM slot_ref)),
  round(max(c)),
  -- 活跃子单位占比中位数（1.0 = 每个子单位都有数据）。1.0 是常态。
  round(100.0 * percentile_cont(0.5) WITHIN GROUP (ORDER BY a::numeric / greatest(1, $G_MAXA)) FILTER (WHERE c > 0))
FROM judged;")

if sql_failed "$rows"; then
  log "ABORT: main query failed: ${rows//$'\n'/ }"
  echo "ABORT: 主判据查询失败 —— 本次**没有结论**，不要把上一轮结果当现状。" >&2
  echo "      （量具不可用与「真没问题」必须可区分，见文件头退出码段。）" >&2
  exit 3
fi
case "$rows" in
  *$'\n'*) echo "ABORT: 主判据返回多行，无法解析: ${rows//$'\n'/ }" >&2; exit 3 ;;
esac
# 必须恰好 10 个字段，少一个都不行（下游 awk 会静默取到空值并当成 0，
# 那样就是「判据字段缺失 ⇒ 全部报 0 ⇒ 报 OK」——又是一次恒真假绿灯）。
nfield=$(printf '%s' "$rows" | awk -F'|' '{print NF}')
[ "$nfield" = "10" ] || { echo "ABORT: 期望 10 个字段，实得 $nfield: $rows" >&2; exit 3; }
# 基线为空 ⇒ 判据没有任何参照系，所有桶都会被判成 ok = 恒真绿灯。
ref_p50=$(printf '%s' "$rows" | awk -F'|' '{print $8+0}')
if [ "$ref_p50" -le 0 ] 2>/dev/null; then
  echo "ABORT: 同时段基线为空（ref_p50=${ref_p50:-空}）—— 判据没有参照系，本次不出结论。" >&2
  echo "      常见原因：BASELINE_DAYS 超过快照保留天数，或表被清空。" >&2
  exit 3
fi
log "summary: ${rows//$'\n'/ }"

buckets_total=$(printf '%s' "$rows" | awk -F'|' '{print $1+0}')
void_b=$(printf '%s' "$rows"        | awk -F'|' '{print $2+0}')
sparse=$(printf '%s' "$rows"        | awk -F'|' '{print $3+0}')
slow=$(printf '%s' "$rows"          | awk -F'|' '{print $4+0}')
no_base=$(printf '%s' "$rows"       | awk -F'|' '{print $5+0}')
normal=$(printf '%s' "$rows"        | awk -F'|' '{print $6+0}')
p50=$(printf '%s' "$rows"           | awk -F'|' '{print $7+0}')
peak=$(printf '%s' "$rows"          | awk -F'|' '{print $9+0}')
active_pct=$(printf '%s' "$rows"    | awk -F'|' '{print $10+0}')
degraded=$(( sparse + slow ))

pct=$(awk -v r="$DEGRADED_RATIO" 'BEGIN{printf "%.0f", r*100}')
echo
echo "===== URSM 快照写入健康（最近 ${WINDOW_HOURS} 小时，粒度 ${G_UNIT}，基线 ${BASELINE_DAYS} 天同时段）====="
printf '  统计桶总数     : %s\n' "$buckets_total"
printf '  正常           : %s   (>= %s%% 同时段基线)\n' "$normal" "$pct"
printf '  ├ 次数塌陷     : %s   活跃分钟腰斩、但每次落库量正常 ⇒ 整批没落库（§5.6 形态）\n' "$sparse"
printf '  └ 速率下降     : %s   活跃分钟尚可、但总量低 ⇒ 单批变小 / 流量或采样变稀\n' "$slow"
printf '  真空洞         : %s   (同时段基线>0 但本桶 0 行, 真丢数据)\n' "$void_b"
printf '  无基线桶       : %s   (该小时位历史无流量, 不参与判定)\n' "$no_base"
printf '  本窗 p50 / 峰值: %s 行/桶 / %s 行/桶\n' "$p50" "$peak"
printf '  同时段基线中位 : %s 行/桶\n' "$ref_p50"
if [ "$G_UNIT" = "hour" ]; then
  printf '  活跃分钟占比   : %s%%   (常态 100%%；塌到个位数即 §5.6 的指纹)\n' "$active_pct"
fi
echo
case "$G_UNIT" in
  hour)
    if [ "$peak" -gt 90000 ] 2>/dev/null; then
      echo "  当前基线推断   : 双写（154 authoritative + 245 shadow）"
    elif [ "$peak" -gt 45000 ] 2>/dev/null; then
      echo "  当前基线推断   : 单写（245 可能已退出 shadow）"
    else
      echo "  当前基线推断   : 显著低于常态，请结合上面逐桶判定看"
    fi ;;
  minute) echo "  当前基线推断   : 见 peak（分钟粒度，跨小时比较意义有限）" ;;
esac

# === 异常桶明细（最多 20 条）===
echo
if [ $(( void_b + sparse + slow + no_base )) -eq 0 ]; then
  echo "----- 异常桶明细：本窗无异常桶，跳过（省一次整条基线重算，实测省 ~33s）-----"
  log "OK: 无真空洞，无降级"
  exit 0
fi
echo "----- 异常桶明细（真空洞 + 次数塌陷 + 速率下降 + 无基线，最多 20 条）-----"
detail=$(de "$BUCKETS_SQL,
ref_rows AS (SELECT slot, percentile_cont(0.5) WITHIN GROUP (ORDER BY c) AS p50 FROM base_buckets GROUP BY 1),
ref_act AS (SELECT slot, max(a) AS a50,
                   percentile_cont(0.5) WITHIN GROUP (ORDER BY c::numeric / a) FILTER (WHERE a > 0) AS rate
            FROM base_active GROUP BY 1),
slot_ref AS (SELECT r.slot, r.p50, t.a50, t.rate FROM ref_rows r LEFT JOIN ref_act t ON t.slot = r.slot)
SELECT to_char(j.m,'MM-DD $G_LABEL'), j.c, coalesce(round(r.p50)::text,'-'),
       j.a::text || '/' || $G_MAXA,
       CASE WHEN r.slot IS NULL OR r.p50 <= 0 THEN 'NO-BASELINE'
            WHEN j.c = 0                 THEN 'VOID'
            WHEN j.c >= $DEGRADED_RATIO * r.p50 THEN 'ok'
            WHEN j.a <  $SPARSE_MIN_RATIO * r.a50
             AND (j.c::numeric / greatest(j.a, 1)) >= $SPARSE_KEEP_RATIO * r.rate THEN 'SPARSE'
            ELSE 'SLOW' END
FROM joined j LEFT JOIN slot_ref r ON r.slot = j.slot
WHERE r.slot IS NULL OR r.p50 <= 0 OR j.c < $DEGRADED_RATIO * r.p50
ORDER BY (CASE WHEN j.c = 0 THEN 0 ELSE 1 END), j.m LIMIT 20;")
if sql_failed "$detail"; then
  echo "  (明细查询失败: ${detail//$'\n'/ })" >&2
  echo "  ↑ 明细只是补充信息；主判据已成功，不影响上面的结论与退出码。" >&2
elif [ -z "$detail" ]; then
  echo "  无异常桶"
else
  echo "  时间                 行数     同时段基线  活跃子单位  判定"
  printf '%s\n' "$detail" | while IFS='|' read -r t c ref a v; do
    printf '  %-20s %8s  %10s  %9s  %s\n' "$t" "$c" "$ref" "$a" "$v"
  done
fi

# === 告警 ===
echo
if [ "$void_b" -gt 0 ]; then
  echo "🚨 真空洞 ${void_b} 个${G_UNIT}桶 —— 两台同时未提交，已丢数据。" >&2
  echo "   优先排查：redis SCAN 超时（路径 A）与 Collect 挤占 Flush 预算（路径 B）。" >&2
  echo "   迁 URSM 到独立 db 是两条路径的共同解（SCAN 实测 0.11s）。" >&2
  exit 2
fi
if [ "$degraded" -gt 0 ]; then
  if [ "$sparse" -gt 0 ]; then
    echo "⚠️  次数塌陷 ${sparse} 个${G_UNIT}桶 —— 活跃分钟腰斩但每次落库量正常 ⇒ 整批没落库。" >&2
    echo "   优先排查：redis SCAN/TYPE 超时（路径 A）与 Collect 挤占 Flush 预算（路径 B）。" >&2
    echo "   迁 URSM 到独立 db 是两条路径的共同解（SCAN 实测 0.11s）。" >&2
  fi
  if [ "$slow" -gt 0 ]; then
    echo "⚠️  速率下降 ${slow} 个${G_UNIT}桶 —— 活跃分钟尚可但总量低于同时段常态（数据未丢）。" >&2
    echo "   方向不同：查双写是否只剩单写（§7 shadow 门禁），或采样率是否变化。" >&2
  fi
  exit 1
fi
if [ "$no_base" -gt 0 ]; then
  echo "ℹ️  ${no_base} 个桶无同时段基线（该小时位历史无流量），本窗未判定。" >&2
fi
log "OK: 无真空洞，无降级"
