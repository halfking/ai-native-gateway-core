#!/bin/bash
# 252 PG17 bloat 周级清理（VACUUM FULL 防患于未然）
#
# 每 7 天（周日凌晨 03:15）跑。查找表 bloat 超过阈值的大表执行 VACUUM FULL，
# 释放死元组归还物理页（autovacuum 不会归还已分配页）。
#
# 阈值（2026-10-19 §143.7 改，见下方 MIN_RECLAIM_MB）：
#   候选层：堆表且 size > 256MB（**不再**要求死元组比例）
#   决策层：确认闸实测 approx_free_space >= MIN_RECLAIM_MB(256MB)
# ⚠ VACUUM FULL 期间会 ACCESS EXCLUSIVE 锁表，所以限制单表时长 30 分钟，
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
# ★ 打开锁文件的那一行**不得**把 stderr 一起重定向（§143.14）：
#   exec 不带命令时，重定向施加到**当前 shell** ⇒ "exec 9>file 2>/dev/null"
#   会让本脚本此后所有 stderr 输出永久消失 ⇒ log()（写 stderr）全部失声。
#   ⇒ 用 { exec 9>file; } 2>/dev/null：组的重定向只作用于组本身，组结束即恢复；
#     fd 9 的语义不变（exec 的重定向仍然持久）。
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && { exec 9>"$_llmgw_lock_file"; } 2>/dev/null; then
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
# ★★ 2026-10-19（§143.7）：判据的**决策量**从「死元组比例」改成「可回收字节数」。
#
# 为什么必须改（§119 实测，§143 复现）：
#   `VACUUM FULL` 的**代价**只与表的大小成正比（整表重写 + 全程 ACCESS EXCLUSIVE，
#   本脚本上限 30 分钟），而**收益**是 `approx_free_space` 那点空闲空间。
#   旧判据量的却是「有多少行死了」—— §119 的 6 个校准样本证明这两个量**几乎脱钩**：
#     stats_event_inbox_default   死元组 5.574%（最高）  可回收 2.8 MB（最小之一）
#     session_project_attribution 死元组 0.369%（最低）  可回收 16.2 MB（最大）
#   ⇒ 对一张 130MB 的表锁 30 分钟去换 2.8MB，是明确的**负收益**。
#
# ★ 旧判据在本库还有一个更糟的性质：**它永远不会触发**。
#   候选层要求估算 dead_pct >= 30%，而 §119 实测 44 张堆表大表的估算最高只有 8.51%，
#   且 `n_dead_tup` 在本库高估 1.5×~21.1×（无固定系数）⇒ 候选集恒为空。
#   脚本每周日 03:15 打印一行「no business tables need VACUUM FULL」，
#   **读起来像一次健康检查，实际是一个不可能命中的判据**（fail-safe，但不是有意的）。
#
# ⇒ 所以候选层也跟着改：去掉死元组比例筛选，把体积门槛从 256MB 降到 100MB
#   （§143.3 点名的三张「空闲 ≫ 死元组」表都在 100~256MB 这一段），
#   让「可回收多少」这件事**有机会被测到**。
#   代价：确认闸要对 ≤20 张表真读页，实测约 5~17s/张 ⇒ 周任务多花 100~340s
#   （LIMIT 20 是既有上限；且周任务 03:15，252 当日无大流量，04:26 前完成无压力）。
#
# ★ 判据是两条 **OR** 的腿，缺一条就有系统性盲区：
#   ① 绝对收益腿：`approx_free_space >= MIN_RECLAIM_MB`（默认 256MB）
#      —— 大表要回收够多才值得锁。252 实测 ≥256MB 段**最大 62.9MB**（session_turns_2026_09）
#         ⇒ 这条腿在本库当前不触发。合理：2.7GB 的表锁 30 分钟换 60MB 是负收益。
#   ② 比例腿：`free/total >= MIN_FREE_PCT`（默认 30%）**且** `total <= MAX_RECLAIM_MB`
#      （默认 1024MB）—— 小表即使绝对回收不大，比例高也值得，且体积小 ⇒ 锁时间短。
#   ⇒ 两条腿分别有判据（TestVacuumBloat…ReclaimBytes / …FreeRatio），缺任一条都能被门抓到。
#
# ⚠⚠ **诚实边界：按当前默认值（256MB / 30% / 1024MB），②里的体积上限是不可达的。**
#   推导：②要求 free ≥ 30%×size，而上限针对 size > 1GB ⇒ free > 300MB
#   > 256MB ⇒ **①必然先满足** ⇒ 上限永远轮不到。
#   这是 2026-10-19 变异验证发现的：把上限删掉，夹具照样全绿（第一版夹具验错了对象）。
#   ⇒ 所以它现在的身份是「**旋钮被调小之后的事故护栏**」，不是当前在起作用的判据：
#   若有人把 MIN_RECLAIM_MB 调到 >300MB，②才会真正被上限约束。
#   （对应的门要注入 MIN_RECLAIM_MB=512 才能测到，见 TestVacuumBloatRatioLegRespectsSizeCap。）
#   别把它当成「当前保护着 2GB+ 的表」—— 它现在什么也没保护。
#
# ⚠⚠ **量纲**：§143.3 记的 free% 是 free/heap（65.26% / 55.43% / 42.90%），
#   而本判据的分母是 `size` = **pg_total_relation_size（含索引与 TOAST）**。
#   换成同一分母后实测是 45.66% / 47.73% / 27.92%（见下）——
#   **照抄 §143.3 的数字会把阈值定高一截**，本节已就地更正。
#   252 只读实测（2026-10-19，逐张 pgstattuple_approx 真读页）：
#     request_stage_events            total=153,927,680  free=73,465,104  47.73%  dead=0.940%
#     candidate_failure_logs_2026_09  total=154,894,336  free=70,724,256  45.66%  dead=0.000%
#     armor_judgments                total=126,910,464  free=35,425,424  27.92%  dead=0.000%
#   ⇒ 默认 MIN_FREE_PCT=30 命中前两张，合计可回收 **144.2MB**；
#     armor_judgments（27.92%）差 2 个点，**改成 25 就多收 35.4MB** —— 属主可选。
#   ★ 注意这两张的 dead_pct 极低（0.94% / 0.00%）而 free 极高 ——
#     这正是「autovacuum 已把死元组回收成 free space、但 free space 永不还给 OS」的形态。
#     旧判据（死元组比例）对它们**结构性地视而不见**，新判据正是冲着这个去的。
MIN_RECLAIM_MB=${MIN_RECLAIM_MB:-256}
MIN_RECLAIM_BYTES=$((MIN_RECLAIM_MB * 1024 * 1024))
MIN_FREE_PCT=${MIN_FREE_PCT:-30}
MAX_RECLAIM_MB=${MAX_RECLAIM_MB:-1024}
MAX_RECLAIM_BYTES=$((MAX_RECLAIM_MB * 1024 * 1024))
# 可注入：让行为门能用假 docker 驱动真实判定分支（同 emergency-cleanup / proactive）。
DOCKER_BIN=${DOCKER_BIN:-docker}
# 确认闸（pgstattuple_approx）单表读超时，秒。
# ★ 2026-10-19 新增：本脚本原先**不给确认闸设任何超时**，直接继承会话/库默认，
#   而 252 实测 statement_timeout=30s，确认闸在本库最坏一次用到 17s
#   （session_turns_2026_09，2.7GB）—— 只有 43% 余量。
# ★ 不可按表大小外推：pgstattuple_approx 按**行**采样，耗时主要由行宽与采样行数决定。
#   252 实测反例：4,029MB 的 session_bodies_2026_10 只要 1s，
#   2,734MB 的 session_turns_2026_09 反而 17s。
#   ⇒ 120s = 实测最坏的约 7 倍余量；LIMIT 20 ⇒ 最坏整轮 40min（周任务可接受）。
CONFIRM_TIMEOUT_SEC=${CONFIRM_TIMEOUT_SEC:-120}

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
WHERE size > 100*1024*1024
ORDER BY size DESC
LIMIT 20;
"

TARGETS=$("$DOCKER_BIN" exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc "$BLOAT_SQL" 2>/dev/null || echo "")
if [ -z "$TARGETS" ]; then
  echo "[$(date -Iseconds)] no business tables need VACUUM FULL (no heap table > 100MB passed the reclaim gate: 绝对腿 ≥${MIN_RECLAIM_MB}MB 或 比例腿 ≥${MIN_FREE_PCT}% 且 ≤${MAX_RECLAIM_MB}MB)" >> "$LOG"
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
    # ★★ 2026-10-19：本行原来是 `x=$(docker … psql … 2>/dev/null | tr …)`。
    #   本脚本开头是 `set -euo pipefail` ⇒ **psql 一失败，管道整体失败，
    #   赋值语句的退出码非 0，`set -e` 直接终止整个脚本。**
    #   （本机实测：`set -euo pipefail` 下 `x=$(false | tr …)` 之后的那行不执行，退出码 1；
    #     去掉 pipefill 才会继续。）
    #   ⇒ 也就是说「量不出来就 SKIP」在真实失败路径上**从未发生过**：
    #     量不出来的那张表不会被跳过，而是**整轮任务在这里静默终止**，
    #     后面的候选与收尾行一起没了（cron 把输出丢进 /dev/null）。
    #   而 §118 已实测列存表会让它必然失败（only heap AM is supported）——
    #   即「静默永久失效」的真实形态比当时写的更严重：**不是跳过，是整轮死掉**。
    #   ⇒ 修法两条：① `|| true` 显式吃掉失败，让 SKIP 分支真的可达；
    #          ② 合并 stderr 以便**分类**失败原因（下面 _why）。
    # ★★ 2026-10-19（§143.7）：一次调用同时取**两个**量。
    #   旧写法只取 dead_tuple_percent，因为它是当时的决策量；新决策量是
    #   approx_free_space（可回收字节）。两者来自同一次真读页 ⇒ 顺手一起取，
    #   **不要拆成两次调用**（那会把确认闸的读页成本翻倍，而实测 5~17s/张）。
    #   dead_tuple_percent 仍然取，但它**不再参与决策** —— 它只进日志，
    #   用来回答「这堆空闲是 autovacuum 刚收的死元组，还是一直就有的」这个诊断问题。
    _raw=$("$DOCKER_BIN" exec \
      -e "PGOPTIONS=-c statement_timeout=${CONFIRM_TIMEOUT_SEC}s" \
      "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -F'|' \
      -c "SELECT round(dead_tuple_percent::numeric,3)||'|'||approx_free_space FROM pgstattuple_approx('$qname');" \
      2>&1 || true)
    _meas=$(printf '%s' "$_raw" | tr -d '[:space:]' | grep -oE '^[0-9]+(\.[0-9]+)?\|[0-9]+$' || true)
    real_dead_pct=${_meas%%|*}
    real_free_bytes=${_meas##*|}
    # ⚠ 空值形态（量不出来）：_meas 为空 ⇒ real_free_bytes 也是空串。
    #   下面的数值比较必须先挡住这一支，否则 `[ "" -lt N ]` 在不同 bash 下行为不一致
    #   （bash 3.2 报算术错、4+ 视作 0）⇒ 判据方向不确定。
    #   「判不出来的表一律 SKIP」（宁可不做，不可错做）由下面这一支保证。
    if ! [[ "$_meas" =~ ^[0-9]+(\.[0-9]+)?\|[0-9]+$ ]]; then
      # 三类失败的**修法完全不同**，混成一句话就等于把「判据缺陷」报成「环境有问题」。
      # 判别依据取 PG 原始报错文本，不猜。
      _why="psql 读失败或返回非数值"
      case "$_raw" in
        *"statement timeout"*)      _why="确认闸读超时（>${CONFIRM_TIMEOUT_SEC}s）" ;;
        *"only heap AM is supported"*) _why="非堆表（候选层漏判）—— 这是判据缺陷，不是环境问题" ;;
        *"pgstattuple_approx"*)     _why="pgstattuple_approx 不可用（扩展缺失？）" ;;
      esac
      echo "[$(date -Iseconds)]   SKIP ${tname}: 估算 dead_pct=${dead_pct}% 但确认闸量不出来 —— ${_why}；不做不可逆动作" >> "$LOG"
      continue
    fi
    # 决策：两条腿取 **OR**（定义见文件头 MIN_RECLAIM_MB 处）。
    #   ① 绝对收益腿：free_bytes >= MIN_RECLAIM_BYTES
    #   ② 比例腿：free/total >= MIN_FREE_PCT  且  total <= MAX_RECLAIM_BYTES
    # ⚠ 用 awk 做浮点比较：free/size 是 0~100 的小数，bash 的整数比较会把它当 0
    #   ⇒ 比例腿会**静默失效**（恒不满足），而日志上看起来只是一直 SKIP。
    # ⚠ 比例腿的分母用 $size（= pg_total_relation_size），**不是堆大小** ——
    #   见文件头「量纲」注：§143.3 的 65.26% 是 free/heap，两者不可混用。
    free_mb=$((real_free_bytes / 1024 / 1024))
    total_mb=$((size / 1024 / 1024))
    free_pct=$(awk -v f="$real_free_bytes" -v s="$size" 'BEGIN{ if (s>0) printf "%.2f", 100.0*f/s; else printf "0.00" }')
    _do_abs=0; _do_ratio=0; _which="—"
    if [ "$real_free_bytes" -ge "$MIN_RECLAIM_BYTES" ]; then _do_abs=1; _which="绝对"; fi
    if [ "$size" -le "$MAX_RECLAIM_BYTES" ] \
       && awk -v p="$free_pct" -v t="$MIN_FREE_PCT" 'BEGIN{exit !(p+0 >= t+0)}'; then
      _do_ratio=1
      [ "$_do_abs" -eq 1 ] || _which="比例"
    fi
    if [ "$_do_abs" -eq 0 ] && [ "$_do_ratio" -eq 0 ]; then
      echo "[$(date -Iseconds)]   SKIP ${tname}: 实测 free=${free_mb}MB/${total_mb}MB=${free_pct}%（dead=${real_dead_pct}%）—— 未达绝对腿 ${MIN_RECLAIM_MB}MB，也未达比例腿 ${MIN_FREE_PCT}% 且 ≤${MAX_RECLAIM_MB}MB；锁 ${TABLE_TIMEOUT_MIN}min 不划算" >> "$LOG"
      continue
    fi
    echo "[$(date -Iseconds)]   VACUUM FULL ${tname} (size=${size} bytes, 实测 dead=${real_dead_pct}%, free=${free_mb}MB/${total_mb}MB=${free_pct}%, 触发腿=${_which})" >> "$LOG"
    # 2026-10-03 同上修复（事务块问题）
    "$DOCKER_BIN" exec -e PGOPTIONS="-c statement_timeout=${TABLE_TIMEOUT_MIN}min -c lock_timeout=5min" \
      "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX \
      -c "VACUUM FULL ${qname}" >> "$LOG" 2>&1 \
      || echo "[$(date -Iseconds)]   VACUUM FULL ${tname} FAILED" >> "$LOG"
  done <<< "$TARGETS"
fi

echo "[$(date -Iseconds)] done" >> "$LOG"
