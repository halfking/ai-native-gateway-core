#!/bin/bash
# 252 PG17 bloat 周级清理（VACUUM FULL 防患于未然）
#
# 每 7 天（周日凌晨 03:15）跑。查找表 bloat 超过阈值的大表执行 VACUUM FULL，
# 释放死元组归还物理页（autovacuum 不会归还已分配页）。
#
# 阈值：bloat_pct >= 30% AND size >= 256MB 才处理。
# VACUUM FULL 期间会 ACCESS EXCLUSIVE 锁表，所以限制单表时长 30 分钟，
# 同时错开 pg17-drop-old-columnar-partitions.sh（每月 1 号 02:30）。
#
# 创建：2026-07-15（伴随 252 磁盘满事件）
set -euo pipefail

# === 单实例闸门（2026-10-10 生产事故修复）================================
# 起因：2026-10-10 252 现场，pg17-disk-watch.sh 堆到 310 份副本、149 个
# `docker exec ... psql` 僵死，把 15G 内存吃到 swap 100% / load 300，
# 反而把 PG 压到无法服务 154 网关（SQLSTATE 57P03、一小时 194 个 503）。
# 根因是本目录**所有**巡检都缺同一道闸门：PG 半死（容器还在 listen、
# 事务卡死）时 psql 会一直挂着，而 cron 的下一次触发照常发生 ——
# 进程只增不减。⇒ 每 10 分钟一跑的巡检，在最需要它的时刻（PG 出问题）
# 恰恰是最不能多跑的时刻。
#
# ★ 为什么 inline 而不是 source 一个公共 helper：
#   本目录按**整文件覆盖**部署到 /opt/scripts/（见 etc.cron.d.pg17 头部
#   「漏一条 = 删一条」）。helper 没跟着传上去，source 失败会让**整个目录**
#   的巡检同时哑掉。本目录已经吃过一次这种耦合的亏，不再引入第二次。
#
# ★ 拿不到锁时**放行**（fail-open），不拒绝执行：
#   闸门不可用时让监控整体静默，比重复采样更糟 —— 静默的监控不会被任何人
#   发现已经坏了。下次采样行会记 gate=FAIL_OPEN，让 fail-open 本身可见。
#
# ★ 抢不到锁时退出 0 而不是 3：本轮确实没有新采样，但巡检**没有失效**
#   （上一轮正在产出结论）。3 在本目录契约里是「量具不可用」，报它会
#   触发一次假的「巡检失效」告警。
LLMGW_LOCK_DIR=${LLMGW_LOCK_DIR:-/var/run/lock}
_llmgw_lock_file="$LLMGW_LOCK_DIR/llmgw-$(basename "$0" .sh).lock"

_llmgw_single_instance_guarded=false
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && exec 9>"$_llmgw_lock_file" 2>/dev/null; then
  if ! flock -n 9; then
    echo "[$(date +%Y-%m-%dT%H:%M:%S%z)] skip: $(basename "$0") 上一轮仍在运行（lock=${_llmgw_lock_file}），本轮不重复执行" \
      >> "${LLMGW_LOCK_SKIP_LOG:-/var/log/llmgw-single-instance-skip.log}" 2>/dev/null || true
    exit 0
  fi
  _llmgw_single_instance_guarded=true
fi

CONTAINER=${CONTAINER:-pg-252-pg17}
PG_USER=${PG_USER:-llm_gateway}
PG_DB=${PG_DB:-llm_gateway}
LOG=${LOG:-/var/log/pg17-vacuum-bloat.log}
TABLE_TIMEOUT_MIN=30   # 单表 VACUUM FULL 上限（分钟）
# 真实死元组比例门槛：pgstattuple 实测低于此值一律 SKIP（§117 确认闸）。
# 取 30 与 BLOAT_SQL 的估算门槛同值 ⇒ 估算只负责「筛出来」，实测负责「做不做」。
MIN_DEAD_PCT=${MIN_DEAD_PCT:-30}
# 可注入：让行为门能用假 docker 驱动真实判定分支（同 emergency-cleanup / proactive）。
DOCKER_BIN=${DOCKER_BIN:-docker}

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
echo "[$(date -Iseconds)] start vacuum-bloat weekly cleanup" >> "$LOG"

# === 列存元数据表（最需要 VACUUM FULL，DROP 列存表后必有大量 dead tuple）===
META_TABLES=("chunk" "stripe" "chunk_group")
for meta in "${META_TABLES[@]}"; do
  size=$("$DOCKER_BIN" exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
    "SELECT pg_total_relation_size('columnar_internal.${meta}')/1024/1024" 2>/dev/null || echo "0")
  echo "[$(date -Iseconds)]   columnar_internal.${meta} size=${size}MB - start VACUUM FULL" >> "$LOG"
  # 2026-10-03 修复：原写法把 SET 与 VACUUM FULL 放在同一个 -c 里，psql 会把它们
  # 包进一个隐式事务块，VACUUM 恒报 "cannot run inside a transaction block" ——
  # 自 2026-07-15 上线起每一个周日 100% 失败（已核对 /var/log/pg17-vacuum-bloat.log）。
  # 退出码还被 `|| echo FAILED` 吞掉，结尾照打 done，所以长期无人察觉。
  # 正确写法：超时参数走 PGOPTIONS，VACUUM FULL 单独一条语句下发。
  "$DOCKER_BIN" exec -e PGOPTIONS="-c statement_timeout=${TABLE_TIMEOUT_MIN}min -c lock_timeout=5min" \
    "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX \
    -c "VACUUM FULL columnar_internal.${meta}" >> "$LOG" 2>&1 \
    || echo "[$(date -Iseconds)]   VACUUM FULL columnar_internal.${meta} FAILED" >> "$LOG"
  new_size=$("$DOCKER_BIN" exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
    "SELECT pg_total_relation_size('columnar_internal.${meta}')/1024/1024" 2>/dev/null || echo "0")
  echo "[$(date -Iseconds)]   columnar_internal.${meta} size=${size}MB -> ${new_size}MB" >> "$LOG"
done

# === 业务表 bloat 检测 ===
# 2026-10-19（§118）就地订正上面那句旧注释「用 pgstattuple 扩展（如果没有就用 dead_tup_pct 估算）」：
#   那句描述的两条路径里，**只有估算那条存在**，pgstattuple 从未被调用过。
#   现已改为：pg_stat 估算只负责「筛出可疑项」，真动手前用 pgstattuple_approx 真读页复核。
#   生产实测校准（2026-10-19，session_summaries 857MB）：
#     估算 dead_pct = 62,877/(800,561+62,877) = 7.29%
#     实测 pgstattuple_approx dead_tuple_percent = 0.346%
#     ⇒ n_dead_tup 在本库**高估约 21 倍**。估算不是「不够准」，是量纲级别不准。
#   复核耗时实测：852MB 表 5.95s、857MB 表 8.12s ⇒ 约 100~130 MB/s（与 pgstatindex 热态同量级）。
#   ⇒ 确认闸代价 = 候选数 × 单表耗时；LIMIT 20 且门槛 256MB，最坏约 2~3 分钟（周日 03:15，可接受）。
#
# ★ 列存表（amname='columnar'）**不在本脚本范围**：pgstattuple_approx 对非堆表直接报
#   `ERROR: only heap AM is supported`（2026-10-19 实测 routing_decision_log_2026_09）。
#   旧 WHERE 只排除了 columnar_internal 这个 schema，**没排除 public 下的列存表**
#   （routing_decision_log_2026_09、provider_events 都是 public.columnar），
#   它们会进候选 → 确认闸必然失败 → 被 fail-safe 跳过 ⇒ 静默的永久失效。
#   ⇒ 现在在候选层显式排除，让「不在范围内」成为**看得见的口径**，而不是靠运行时失败。
#   VACUUM FULL 是堆表维护操作；列存的回收/合并走 pgcolumnar 自己的路径，属另一件事。
BLOAT_SQL="
WITH ranked AS (
  SELECT
    s.schemaname||'.'||s.relname AS tname,
    -- 2026-10-19 审计修正：执行用名必须是**标识符引用过的** form。
    --   tname 是 schemaname||'.'||relname 的裸拼，PG 只在名字需要时才加引号；
    --   而本脚本把它直接插进两条要发给 PG 执行的语句（确认闸的实测调用，
    --   以及最终那条重建表空间的调用）。
    --   朴素名（含引号/大小写/空格的表名或 schema 名）会拼出坏 SQL。
    --   后果方向是 fail-safe（psql 报错 => 量不出来 => SKIP），不是错做；
    --   但同族 emergency-cleanup 的 DROP 路径早已用 format 带引号形态，
    --   两边形态不一致本身就是下一个读者的坑 => 这里对齐。
    --   format 带引号形态对朴素标识符**不加**引号，日志与既有行为完全不变。
    --   ⚠ 纪律一：本注在 BLOAT_SQL 的双引号串里，不可出现反引号、美元-花括号
    --     或双引号，否则 bash 在赋值时做命令替换/参数展开（3.2 下未定义变量
    --     还会被 set -u 直接打死）。本轮踩过一次。
    --   ⚠ 纪律二：也**不要**在本串里写出下方那两条语句的函数名/关键字全文 ——
    --     行为门注入的假 docker 是按子串匹配 SQL 来应答的，注释里出现同样的
    --     字面量会让假 docker 认错分支。本轮同样踩过一次。
    format('%I.%I', s.schemaname, s.relname) AS qname,
    pg_total_relation_size(s.relid)::bigint AS size,
    s.n_live_tup, s.n_dead_tup,
    CASE WHEN s.n_live_tup + s.n_dead_tup > 0
         THEN 100.0 * s.n_dead_tup / (s.n_live_tup + s.n_dead_tup)
         ELSE 0 END AS dead_pct
  FROM pg_stat_user_tables s
  JOIN pg_class c ON c.oid = s.relid
  JOIN pg_am am ON am.oid = c.relam
  WHERE s.schemaname NOT IN ('columnar_internal')
    AND am.amname = 'heap'
)
SELECT tname, size, round(dead_pct::numeric, 1), qname
FROM ranked
WHERE dead_pct >= 30 AND size > 256*1024*1024
ORDER BY dead_pct DESC, size DESC
LIMIT 20;
"

TARGETS=$("$DOCKER_BIN" exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc "$BLOAT_SQL" 2>/dev/null || echo "")
if [ -z "$TARGETS" ]; then
  echo "[$(date -Iseconds)] no business tables need VACUUM FULL (no bloat > 30% with size >= 256MB)" >> "$LOG"
else
  echo "[$(date -Iseconds)] bloat targets:" >> "$LOG"
  echo "$TARGETS" >> "$LOG"
  while IFS='|' read -r tname size dead_pct qname; do
    [ -z "$tname" ] && continue
    # 执行一律用 qname（format('%I.%I')，见 BLOAT_SQL 里的修正注）。
    # tname 只用于日志/报错——日志要的是人能读的裸名，不是引号形态。
    # 缺第 4 列时（旧的候选集形态）回退到 tname，保持向后兼容。
    if [ -z "${qname:-}" ]; then qname="$tname"; fi
    # ★ 2026-10-19（§117）：BLOAT_SQL 的 dead_pct 是**从 pg_stat 的估计值算的**
    #   （100*n_dead_tup/(n_live_tup+n_dead_tup)），而 pg_stat 会被 PG 重启清零、
    #   只在 ANALYZE/VACUUM 之后才回来。本文件头部本来就写着「用 pgstattuple 扩展
    #   （如果没有就用 dead_tup_pct 估算）」，但**估算那条路径是唯一的实现**。
    # ⇒ 与 §116 同族的处置：**估计量只能筛可疑项，不能驱动不可逆动作**。
    #   VACUUM FULL 会拿 ACCESS EXCLUSIVE 锁住整表（TABLE_TIMEOUT_MIN=30），
    #   对着一张其实很健康的表锁 30 分钟，代价落在 252 上所有在线业务。
    # ⇒ 确认闸：真去读表（pgstattuple_approx 真读页），<阈值一律 SKIP；
    #   量不出来（扩展缺失 / 读失败）也 SKIP 并显式留痕 —— 宁可不做，不可错做。
    #   ※ 候选层已排除非堆表，所以走到这里的失败只剩「扩展缺失 / 读失败」两种。
    real_dead_pct=$("$DOCKER_BIN" exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX \
      -c "SELECT round(dead_tuple_percent::numeric,1) FROM pgstattuple_approx('$qname');" 2>/dev/null \
      | tr -d '[:space:]')
    if ! [[ "$real_dead_pct" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
      echo "[$(date -Iseconds)]   SKIP ${tname}: 估算 dead_pct=${dead_pct}% 但 pgstattuple_approx 量不出来（扩展缺失或读失败；候选已限堆表），不做不可逆动作" >> "$LOG"
      continue
    fi
    if awk -v r="$real_dead_pct" -v t="$MIN_DEAD_PCT" 'BEGIN{exit !(r<t)}'; then
      echo "[$(date -Iseconds)]   SKIP ${tname}: 估算 dead_pct=${dead_pct}% 但实测仅 ${real_dead_pct}%（<${MIN_DEAD_PCT}%），统计估计不可信" >> "$LOG"
      continue
    fi
    echo "[$(date -Iseconds)]   VACUUM FULL ${tname} (size=${size} bytes, 估算 dead_pct=${dead_pct}%, 实测=${real_dead_pct}%)" >> "$LOG"
    # 2026-10-03 同上修复（事务块问题）
    "$DOCKER_BIN" exec -e PGOPTIONS="-c statement_timeout=${TABLE_TIMEOUT_MIN}min -c lock_timeout=5min" \
      "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX \
      -c "VACUUM FULL ${qname}" >> "$LOG" 2>&1 \
      || echo "[$(date -Iseconds)]   VACUUM FULL ${tname} FAILED" >> "$LOG"
  done <<< "$TARGETS"
fi

echo "[$(date -Iseconds)] done" >> "$LOG"
