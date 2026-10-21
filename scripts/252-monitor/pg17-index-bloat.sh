#!/bin/bash
# 252 PG17 索引膨胀治理（pgstatindex 双信号判据 + REINDEX CONCURRENTLY）
#
# 为什么单独一个脚本，不并进 pg17-vacuum-bloat.sh：
#   vacuum-bloat 判的是「堆死元组」(pg_stat_user_tables.n_dead_tup)，
#   本脚本判的是「索引内部空间浪费」。两者是不同的面 ——
#   autovacuum 回收堆死元组不会让索引变紧凑。
#
# === 判据：pgstatindex 有三种索引浪费，形状不同，必须分开量再相加 ===
#   deleted_pages      完全空页数。只在「按范围批量删除」时显著 ——
#                      retention 按时间段删，页被整片清空。
#   leaf_pages         叶页总数。算「未用空间」的基数。
#   avg_leaf_density   页内**已用字节**占比(0~100)。反映零散 churn 型膨胀。
#   leaf_fragmentation 既不是密度也不是空页率，别拿它当判据（见下方实测）。
#
#   ★ avg_leaf_density 度量的是「已用字节」，不是「存活元组占比」——
#     死元组的行指针在被 vacuum 之前一直占位，计为已用。
#     决定性实测（2026-10-03 02:00，同一索引同一 341 叶页）：
#       before VACUUM  deleted_pages=0  avg_leaf_density=92.26
#       (跑一次 VACUUM)
#       after  VACUUM  deleted_pages=0  avg_leaf_density=33.94
#     同 autovacuum_enabled=false 的表，密度却是 92.26 ——
#     可见它统计的是「有没有被 vacuum 清过行指针」，不是「页有多空」。
#     ⇒ 曾据此加过一个「密度>=88 视为已达重建上限」的豁免分支，已撤除：
#       该阈值是建立在这个误读上的，不能留。
#     正面用途不变：它确实能区分 churn 型膨胀（33.94 vs 重建后 92.52）。
#
#   实测样本 A（临时库，30 万行删 90%，零散 churn）：
#     重建前: size=2.79MB deleted_pages=0 leaf_pages=341  density=33.94
#     重建后: size=0.30MB deleted_pages=0 leaf_pages=34   density=92.52
#     deleted_pages 全程为 0，REINDEX 却回收了 89%（9.4×）→ 只看 deleted_pages 完全看不见。
#     另测：把 autovacuum_enabled 关掉重做，deleted_pages 仍是 0 ——
#     变量不是 autovacuum，别再往那个方向排查。
#
#   实测样本 B（生产 request_state_transitions_pkey，retention 批量删除）：
#     size=94MB deleted_pages=9572 leaf_pages=2469 density=90.09
#     空页占大头但活页排得很紧。只按密度算 → 9.3MB（低于门槛，会被误判成健康）；
#     计入空页后 → 76.7MB。两种形态混用单一公式都会漏报。
#
#   可回收量 = deleted_pages + leaf_pages*(1-density/100)，单位页 × 8KB。
#
#   ★ 这个数是「上界」不是「期望值」（2026-10-03 生产实测校准）：
#     retention 批量删除型（request_state_transitions_pkey）
#       估 76MB → 实收 77MB，比值 0.99x。预估模型与实际机制同构，很准。
#     零散 churn 型的大表（ursm_node_snapshot_min_pkey, 1987 万行）
#       估 132MB → 实收 46MB，比值 2.87x。
#       该表 89.14% 密度，未用空间分散在 15.6 万个叶页里；REINDEX 只能合并
#       相邻键范围的页，不能重排行序，所以回收远小于「密度×体积」的直算。
#   ⇒ leaf_pages 分散在大表上时，本式严重高估。
#     但它仍可用于**排序**（相对大小有意义），不要拿来当收益承诺。
#     日志里的 headroom 措辞用「≈」也是为此。
#
# === 报告型判据必须能区分「没有」与「没测到」===
#   逐索引超时/报错单独计入 unmeasured 并在汇总里显式出现；
#   一个都测不出来时 exit 2，而不是报「无膨胀」。0 个命中不打印成功符号。
#
# === 性能 ===
#   pgstatindex 逐页读，>1GB 索引实测 >90s。故先按 MIN_INDEX_MB 圈定，
#   每个索引单独加 statement_timeout，超时记 unmeasured。
#
#   2026-10-19 生产只读实测探测速率（§115，pgstatindex 单条耗时）：
#     383.8MB → 6,529ms   349.3MB → 5,284ms   305.6MB → 3,011ms
#     240.0MB → 2,073ms   224.0MB → 3,835ms   398.4MB → 16,172ms（冷缓存首条）
#   ⇒ 热态速率约 60~115 MB/s。放开 relispartition 后新增 72 条 / 4,455MB，
#     预计**新增探测耗时约 60~120 秒**（含每条一次 podman exec 的进程开销），
#     不是担心的「10 分钟」—— §104 留下的「必须先确认运行时间」前置条件已满足。
#
# === 覆盖面：候选集必须看得见两类索引（2026-10-19 两次独立发现）===
#   ① 叶分区上的索引：分区父表的索引是 relkind='I'（无物理存储），
#      本来就被 relkind='i' 排除；而**叶分区**的索引 relkind 与普通表一样是 'i'，
#      早先那个 `AND NOT c.relispartition` 把它们**全挡在外面**（§104：72 条 / 4,455MB），
#      却什么也没挡住 ⇒ 已删除。
#   ② TOAST 表的唯一索引：pg_toast 命名空间早先被 nspname='public' 整体排除，
#      其中 `pg_toast_81522141_index` 是 **229MB、29,299 个死页、密度 0.54%**
#      的纯垃圾（§114）⇒ 命名空间已放宽到 public + pg_toast。
#   ⚠ 放宽命名空间**不等于**按尺寸一刀切 REINDEX：pg_toast 里 3 条 ≥16MB 的索引
#     只有 1 条真膨胀（另两条叶页密度 89.88% / 90.18%），
#     所以判据仍是 pgstatindex 的**双信号**（deleted_pages OR avg_leaf_density）。
#   ✅ PG 17.10 实测：`REINDEX INDEX CONCURRENTLY` 作用在 TOAST 索引上**允许且有效**
#     （本地同镜像 kx-citus-pg17 沙箱：1,800kB → 8,192 bytes；活行在表时也允许）。
#
# === REINDEX CONCURRENTLY ===
#   必须单语句下发。与 SET 放同一个 -c 会被 psql 包进隐式事务块，
#   报 "cannot run inside a transaction block"（pg17-vacuum-bloat.sh 踩过，
#   自 2026-07-15 起每个周日 100% 静默失败）。超时参数走 PGOPTIONS。
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

CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
[ -f "$CONF" ] && source "$CONF"

CONTAINER=${CONTAINER:-pg-252-pg17}
PG_USER=${PG_USER:-postgres}
PG_DB=${PG_DB:-llm_gateway}
LOG=${LOG:-/var/log/pg17-index-bloat.log}
NOTIFY=${NOTIFY:-/opt/scripts/notify.sh}

# === 阈值（可被 /etc/llmgw/pg17.conf 覆盖）==========================
MIN_INDEX_MB=${MIN_INDEX_MB:-16}          # 低于此尺寸不测：量它比回收它还贵
MIN_DEAD_PAGES=${MIN_DEAD_PAGES:-2000}     # 批量删除型：死页下限（2000 页 ≈ 16MB）
MIN_LEAF_DENSITY=${MIN_LEAF_DENSITY:-60}   # 零散 churn 型：叶页密度下限（%）
MIN_RECLAIM_MB=${MIN_RECLAIM_MB:-32}       # 重建后至少能回收这么多才值得动手
# 重建目标密度 = btree 的 fillfactor 默认值（90）。★ 这不是猜的：
#   REINDEX / CREATE INDEX 走**排序建**，实测可达密度三处互证 ——
#     90.20%（2026-10-03 生产真值，见 $STATE 基线文件）
#     89.82% / 89.88%（2026-10-19 两个沙箱对照实验，见 §143.11）
#   旧公式按「填到 100%」算 ⇒ **越接近 90% 越发狂**：
#   密度 90.07% 的 route_incident_events_pkey 旧口径报「可回收 3MB」，
#   而排序建到 90% 只差 0.07% ⇒ 真值 ≈ 0；密度 89.14% 的 1987 万行索引
#   旧口径报 115MB、实测只收回 46MB（2026-10-03 生产）。
#   ⇒ 收益必须按「重建到 90%」算，否则 --fix 会反复挑中重建不动的索引。
FILLFACTOR=${FILLFACTOR:-90}
MAX_FIX_PER_RUN=${MAX_FIX_PER_RUN:-10}     # 单次运行最多重建几个，避免 cron 跑成长任务
DENSITY_TOLERANCE=${DENSITY_TOLERANCE:-2}  # 当前密度高于「上次重建后密度」这么多点以内，视为已在自然填充率
STATE=${STATE:-/var/lib/pg17-index-bloat/seen-density.tsv}  # index<TAB>重建后密度，自校准基线
PROBE_TIMEOUT=${PROBE_TIMEOUT:-180}        # 单个索引 pgstatindex 超时（秒）
REINDEX_TIMEOUT_MIN=${REINDEX_TIMEOUT_MIN:-30}
LOCK_TIMEOUT=${LOCK_TIMEOUT:-5min}
ALERT_CANDIDATE_MB=${ALERT_CANDIDATE_MB:-256}  # 候选索引总体积超此值才告警

MODE=report
for arg in "$@"; do
  case "$arg" in
    --report) MODE=report ;;
    --fix)    MODE=fix ;;
    *) echo "unknown arg: $arg (use --report | --fix)" >&2; exit 64 ;;
  esac
done

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
ts=$(date -Iseconds)
# 每行取一次当前时间。早先写成 log(){ echo "[$ts] ..."; } 而 $ts 在脚本开头
# 取一次就整轮复用 —— 结果整轮日志的时间戳全是「启动那一刻」，
# 排查时看不出单次探测花了多久、失败发生在什么时刻。
# 这正是 pg17-vacuum-bloat.sh 静默失败两个月而无法从日志定位的同款问题：
# 日志本身不可用于诊断。
log() { echo "[$(date -Iseconds)] $*" >> "$LOG"; }

log "start index-bloat mode=$MODE min_index=${MIN_INDEX_MB}MB min_dead_pages=${MIN_DEAD_PAGES} min_leaf_density=${MIN_LEAF_DENSITY}"

# 必须吞掉 psql 的退出码：stderr 已并入 stdout，错误文本会作为「取值」流到下面的
# 显式检查里。没有这层时，`x=$(de ...)` 会因 psql 返回非零被 set -e 直接掐死 ——
# 日志停在 start、退出码 1、零诊断，运维只会看到「脚本挂了」而看不到「量具不可用」。
de() { podman exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -c "$1" 2>&1 || true; }

# === 对照断言：先证明量具本身可用 ====================================
# 不做这一步，「0 个膨胀索引」在「真的没有」与「量具根本跑不通」
# （扩展没装、权限不足、版本字段改名）之间不可区分。
control_idx=$(de "SELECT c.oid::regclass::text
                   FROM pg_index i
                   JOIN pg_class c ON c.oid = i.indexrelid
                   JOIN pg_am am ON am.oid = c.relam AND am.amname = 'btree'
                  WHERE c.relkind = 'i' AND NOT c.relispartition
                    AND c.relpages > 0
                  ORDER BY pg_relation_size(c.oid) ASC LIMIT 1;")
if [ -z "$control_idx" ] || [ "${control_idx:0:5}" = "ERROR" ]; then
  log "ABORT: control probe could not name a btree index: ${control_idx:-<empty>}"
  echo "ABORT: 找不到可探测的 btree 索引，量具路径不可用" >&2
  exit 2
fi
# 2026-10-03 首次实跑在这里红过：最小索引是空 TOAST 索引，avg_leaf_density 返回
# 'NaN'（无叶页时密度无定义），被正则判成量具故障。NaN 是合法返回值，不是故障 ——
# 探针要证明的是「pgstatindex 能跑通且返回列结构完整」，不是「密度必须是个数」。
control_val=$(de "SELECT deleted_pages, avg_leaf_density FROM pgstatindex('$control_idx');")
if ! [[ "$control_val" =~ ^[0-9]+\|(NaN|[0-9.]+)$ ]]; then
  log "ABORT: control probe failed on $control_idx: ${control_val:-<empty>}"
  echo "ABORT: 对照探针失败（$control_idx → ${control_val:-<空>}），本次报告不可信" >&2
  exit 2
fi
log "control probe OK: $control_idx ${control_val}"

# === 圈定候选（只取尺寸达标的 btree 索引）===========================
# ★ 覆盖面两次修正（2026-10-19，§104 / §115），两处都是**结构性盲区**：
#   ① `AND NOT c.relispartition` 已删除：它挡的是「分区父表上无物理存储的索引」，
#      而那些是 relkind='I'、压根不在 relkind='i' 的候选集里 ⇒ 这一条什么都没挡，
#      却把**叶分区**上的 72 条 / 4,455MB 索引全部挡在外面（机制「压根没量」）。
#   ② 命名空间从 `= 'public'` 放宽为 `IN ('public','pg_toast')`：
#      TOAST 表的唯一索引早先整体不可见，其中一条是 229MB 的纯死索引（§114）。
# ⚠ 判据仍是 pgstatindex 的**双信号**（deleted_pages OR avg_leaf_density），
#   **不**因为放宽覆盖面就改成按尺寸一刀切 —— pg_toast 里 3 条 ≥16MB 的
#   只有 1 条真膨胀，另两条叶页密度 89.88% / 90.18%，一刀切会纯浪费。
# ★ 候选集同时返回「族键」（family key），用来对齐自校准基线（见下方 baseline 段）：
#   叶分区索引的名字与**父表**索引的名字不同（`ursm_node_snapshot_min_20261019_pkey`
#   vs `ursm_node_snapshot_min_pkey`），而 $STATE 记的是重建时**实际动过的那个索引**。
#   2026-10-19 实测：基线文件里唯一的真实数据点 `ursm_node_snapshot_min_pkey 90.20`
#   （分区父表上的索引，relkind='I'）**永远匹配不到任何被测的叶分区索引**
#   ⇒ 自校准在生产上是死的 ⇒ 每次都按「未达自然填充率」重新挑中同一族索引。
#   族键取「同一父表上、索引形状（USING 之后的定义本体）完全相同」的那个父表索引名；
#   非分区表没有父表，族键就是它自己 ⇒ 旧格式基线条目继续可用。
candidates=$(de "SELECT c.oid::regclass::text || '|' ||
                   COALESCE(
                     (SELECT pi_idx.relname
                        FROM pg_inherits inh
                        JOIN pg_index  pi     ON pi.indrelid = inh.inhparent
                        JOIN pg_class pi_idx ON pi_idx.oid = pi.indexrelid
                       WHERE inh.inhrelid = i.indrelid
                         AND regexp_replace(pg_get_indexdef(pi.indexrelid), '^.*?USING ', 'USING ')
                           = regexp_replace(pg_get_indexdef(c.oid),          '^.*?USING ', 'USING ')
                       ORDER BY pi_idx.relname
                       LIMIT 1),
                     c.relname)
                   FROM pg_index i
                   JOIN pg_class c  ON c.oid = i.indexrelid
                   JOIN pg_am am    ON am.oid = c.relam AND am.amname = 'btree'
                   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname IN ('public','pg_toast')
                  WHERE c.relkind = 'i'
                    AND i.indisvalid
                    AND pg_relation_size(c.oid) >= ${MIN_INDEX_MB} * 1024 * 1024
                  ORDER BY pg_relation_size(c.oid) DESC;")

# de() 把 stderr 合并进 stdout，psql 报错会以 ERROR 开头混进候选名单，
# 那样每一行都会被记成 UNMEASURED，最后以「测了 0 个」收场 —— 结论不成立但看不出错在哪。
if [ "${candidates:0:5}" = "ERROR" ]; then
  log "ABORT: candidate query failed: ${candidates//$'\n'/ }"
  echo "ABORT: 候选索引查询失败：${candidates//$'\n'/ }" >&2
  exit 2
fi
cand_count=$(printf '%s' "$candidates" | grep -c . || true)
log "candidates(>=${MIN_INDEX_MB}MB btree, valid, public): $cand_count"

# === INVALID 索引：与膨胀是不同的病，必须单独报 =====================
# 2026-10-03 补：候选查询带 `AND i.indisvalid`，INVALID 索引因此**静默消失** ——
# 既不在候选里，也不在 measured/unmeasured 任何计数里。这是本脚本自己的
# 「没有」与「没测到」混淆：量具因为「测不了这个对象」而报出「没有问题」。
#
# 为什么要单独报而不是顺手放进候选：INVALID 索引是**正确性**问题而非空间问题。
# PostgreSQL 会跳过它做查询规划，REINDEX CONCURRENTLY 之外还需确认业务查询
# 是否正依赖它（若是，索引失效期间等价于该查询全表扫）。处置路径不同。
# ★ 覆盖面与候选集保持一致（2026-10-19 §104/§115）：同样放宽命名空间到
#   public + pg_toast，同样不排除叶分区索引 —— 否则「INVALID 索引」这条
#   独立告警线（indisvalid=false，需 REINDEX/VALIDATE）会漏掉同一批对象。
invalid_idx=$(de "SELECT c.oid::regclass::text
                  FROM pg_index i
                  JOIN pg_class c  ON c.oid = i.indexrelid
                  JOIN pg_am am    ON am.oid = c.relam AND am.amname = 'btree'
                  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname IN ('public','pg_toast')
                 WHERE c.relkind = 'i'
                   AND NOT i.indisvalid
                 ORDER BY pg_relation_size(c.oid) DESC;")
if [ "${invalid_idx:0:5}" = "ERROR" ]; then
  log "ABORT: invalid-index query failed: ${invalid_idx//$'\n'/ }"
  echo "ABORT: INVALID 索引查询失败：${invalid_idx//$'\n'/ }" >&2
  exit 2
fi
invalid_count=$(printf '%s' "$invalid_idx" | grep -c . || true)
if [ "$invalid_count" -gt 0 ]; then
  log "INVALID $invalid_count 个索引未被测量（indisvalid=false，需 REINDEX/VALIDATE，非膨胀问题）:"
  while IFS= read -r bad; do
    [ -z "$bad" ] && continue
    bad_mb=$(de "SELECT pg_relation_size('$bad')/1024/1024;" | tr -d '[:space:]')
    log "INVALID  $bad size=${bad_mb}MB indisvalid=false"
  done <<< "$invalid_idx"
fi

measured=0; unmeasured=0; flagged=0; cand_mb=0
flag_list=""

while IFS='|' read -r idx fam_key; do
  [ -z "$idx" ] && continue
  [ -z "$fam_key" ] && fam_key="$idx"
  row=$(podman exec -e PGOPTIONS="-c statement_timeout=${PROBE_TIMEOUT}s" \
          "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -c \
          "SELECT pg_relation_size('$idx'), s.deleted_pages, s.leaf_pages, s.avg_leaf_density
             FROM pgstatindex('$idx') s;" 2>&1)

  size_bytes=$(printf '%s' "$row" | head -1 | cut -d'|' -f1 | tr -d '[:space:]')
  dead_pages=$(printf '%s' "$row"  | head -1 | cut -d'|' -f2 | tr -d '[:space:]')
  leaf_pages=$(printf '%s' "$row"  | head -1 | cut -d'|' -f3 | tr -d '[:space:]')
  density=$(printf '%s' "$row"     | head -1 | cut -d'|' -f4 | tr -d '[:space:]')

  if ! [[ "$dead_pages" =~ ^[0-9]+$ ]] || ! [[ "$leaf_pages" =~ ^[0-9]+$ ]] \
     || ! [[ "$density" =~ ^(NaN|[0-9.]+)$ ]]; then
    unmeasured=$((unmeasured + 1))
    log "UNMEASURED $idx : ${row//$'\n'/ }"
    continue
  fi

  measured=$((measured + 1))
  size_mb=$(( size_bytes / 1024 / 1024 ))

  # 空索引（无叶页）密度为 NaN：没有可回收空间，直接记 ok，不要当成量不出来。
  if [ "$density" = "NaN" ]; then
    log "ok        $idx size=${size_mb}MB dead_pages=$dead_pages density=NaN(无叶页，无可回收)"
    continue
  fi

  # 可回收页数 = 全空页 + 叶页未用空间。
  # 2026-10-03 修正：原先只用 size*(1-density/100)，漏算了「全空页」这一项。
  # 漏算的后果不是少报一点，而是把最大的回收机会判成 ok ——
  # request_state_transitions_pkey 明明有 9572 个全空页(74.8MB)，但活页密度 90%
  # 使旧公式只算出 9.3MB，低于门槛被跳过。实测该索引 leaf_pages 仅 2469，
  # 正确公式给出 76.7MB。两种浪费形态必须分开算再相加。
  #
  # ★ 但它对 churn 型大表是**上界**，不是期望值：
  #   REINDEX 只能合并相邻键范围的页，不能重排行序。索引越大、行序越乱，
  #   重建后的可达密度就越低。
  #   生产实测 2026-10-03：ursm_node_snapshot_min_pkey（1987 万行）估 132MB、
  #   实收 46MB；重建后密度 90.20%、仍估 115MB —— 它会持续被选中、每次只回收一点。
  #   曾为此加「密度>=88 视为已达上限」的豁免分支，因 avg_leaf_density 语义被误读
  #   （见文件头）而撤除。此处仅记录该行为已知，不再据此下结论。
  #
  # ★★ 2026-10-19（§143.12）再修正：**重建目标不是 100%，是 fillfactor 90%**。
  #   REINDEX 走**排序建**（按键序重排条目后重建 btree），不是「合并相邻页」——
  #   上面那段「不能重排行序」的说法是**错的**，且与同一文件 2026-10-03 记下的
  #   「重建后密度 90.20%」直接矛盾：若不重排，1987 万行的索引不可能从 89.14%
  #   变成 90.20%。就地订正。
  #   ⇒ 重建后的可达密度 = btree fillfactor 默认值 90%，与表的行序无关。
  #   旧公式 (1−y/100) 的后果：密度越接近 90%，高估越离谱 ——
  #     密度 90.07%（route_incident_events_pkey）旧口径「可回收 3.8MB」，
  #     真值 ≈ 0（排序建只能到 90%）；密度 89.14% 的 1987 万行索引旧口径 115MB。
  #   新公式按「重建到 FILLFACTOR(=90)」算：reclaim = dead + leaf×(1 − min(y,90)/90)。
  #   ★ 校准依据是**受控实验**，不是生产数字：
  #     2026-10-19 两个沙箱对照（同数据、只改插入顺序）——
  #       随机插 68.13% → 排序建 89.88%，实测体积少 24.22%；公式给 24.3%；
  #       批内同首列 70.20% → 排序建 89.82%，实测少 21.98%；公式给 22.0%。
  #     ⇒ 公式与受控实测吻合到 0.1 个百分点。
  #   ⚠ 2026-10-03 生产那次（重建前 89.14%、重建后 90.20%、估 132MB 实收 46MB）
  #     **不能当校准点**：6.5 小时里该表在持续写入 + TTL 删除，
  #     重建前后的活元组字节数本身变了（按密度反推，活字节从 1,138MB 降到 1,120MB）
  #     ⇒ 「实收 46MB」里混着数据变化，不是干净的重建收益。此处只留作背景。
  #   ★ 语义提醒：dead_pages 与 leaf_pages 是**互斥**的两批页，不重复计数 ——
  #     2026-10-19 实测 pgstatindex 全字段逐页对得上：
  #     pg_toast_81522141_index 的 leaf=1 + deleted=29299 + internal=2 + root=1
  #     = rel_pages=29303；三个正常索引（stats_event_dedup_pkey 等）同样对得上。
  #   ★ 口径性质：新公式给的是**下界**（保证能收回的那部分）。
  #     排序建会顺带丢掉**活页里残留的死元组字节**，而工具只测「死页」不测「死字节」
  #     ⇒ churn 型的真实收益高于此数；工具没有便宜的办法测死字节（pgstattuple 要全扫）。
  #   ★ empty_pages 实测恒为 0（>20MB 的 btree 索引无一例外，2026-10-19）⇒ 不进公式。
  headroom_mb=$(awk -v d="$dead_pages" -v l="$leaf_pages" -v y="$density" -v f="$FILLFACTOR" \
    'BEGIN{ y=(y>f?f:y); printf "%d", (d + l*(1-y/f))*8192/1048576}')
  reason=""
  [ "$dead_pages" -ge "$MIN_DEAD_PAGES" ] && reason="dead_pages=$dead_pages($(( dead_pages * 8192 / 1024 / 1024 ))MB)"
  if awk -v y="$density" -v t="$MIN_LEAF_DENSITY" 'BEGIN{exit !(y<t)}'; then
    reason="${reason:+$reason,}low_density=${density}%"
  fi
  # 累计 headroom 也能单独越过门槛（页数极多、每页都只空一点点：两个单信号都没破线，
  # 汇总起来却超过 MIN_RECLAIM_MB）。此时 reason 会是空串，日志会打出 "reason="
  # 这种读不出所以然的行，--fix 的日志同样带不出来。补一个兜底说明。
  # ⚠ 旧串写的是「×${density}%空隙」，但 density 是**已用**占比（avg_leaf_density），
  #   空隙是 100−density ⇒ 旧串把「已用 89.14%」标成「空隙 89.14%」，
  #   照字面读会把回收量算反一个量级（2026-10-19 发现，就地订正）。
  [ -z "$reason" ] && reason="aggregate(leaf_pages=${leaf_pages}×已用${density}%,重建可达${FILLFACTOR}%)"

  # === 自校准：已达自然填充率的索引不再重复重建 =====================
  # 问题（2026-10-03 生产实测）：ursm_node_snapshot_min_pkey 重建后密度 90.20%，
  # 仍按公式估出 115MB，于是每轮都被选中，每次只回收 40-50MB。
  # 根因：B-tree 的自然填充率由键序与页大小决定，**不等于 100%**。
  # 对 1987 万行、按时间滚动删除的表，键序与插入序天然错位，
  # REINDEX 也只能合并相邻键范围的页 —— 重建后的 90.20% 就是它的自然填充率，
  # 剩下 9.8% 不是「浪费」，永远收不回来。
  #
  # 为什么不设绝对阈值：曾用「密度>=88 视为已达上限」，该阈值建立在
  # avg_leaf_density 语义误读之上，已撤除（见文件头）。绝对阈值要靠猜。
  # 这里改成**相对判据**：拿该索引自己上一次重建后的密度当基线。
  # 基线是实测出来的，不是猜的；且随该索引自身的数据分布自动校准。
  # 基线先按**族键**找（叶分区索引继承父表索引的自然填充率），
  # 再退回按索引名找（非分区表的族键就是它自己 ⇒ 等价于旧行为，旧文件继续可用）。
  baseline=$(awk -F'\t' -v k="$fam_key" '$1==k{print $2; exit}' "$STATE" 2>/dev/null || true)
  baseline_src="$fam_key"
  if [ -z "$baseline" ]; then
    baseline=$(awk -F'\t' -v k="$idx" '$1==k{print $2; exit}' "$STATE" 2>/dev/null || true)
    baseline_src="$idx"
  fi
  if [ -n "$baseline" ] && awk -v y="$density" -v b="$baseline" -v t="$DENSITY_TOLERANCE" \
       'BEGIN{exit !(y >= b - t)}'; then
    log "ok        $idx size=${size_mb}MB density=${density}% >= 族基线 ${baseline}% (${baseline_src}) -${DENSITY_TOLERANCE}（已达自然填充率，再重建无收益）"
    continue
  fi

  if [ "$headroom_mb" -ge "$MIN_RECLAIM_MB" ]; then
    flagged=$((flagged + 1))
    cand_mb=$((cand_mb + headroom_mb))
    flag_list="${flag_list}${idx}|${fam_key}|${headroom_mb}|${reason}"$'\n'
    log "CANDIDATE $idx size=${size_mb}MB headroom≈${headroom_mb}MB reason=${reason}"
  else
    log "ok        $idx size=${size_mb}MB dead_pages=$dead_pages density=${density}% headroom≈${headroom_mb}MB"
  fi
done <<< "$candidates"

log "summary: measured=$measured unmeasured=$unmeasured flagged=$flagged headroom≈${cand_mb}MB invalid_skipped=$invalid_count"

# === 汇总必须能区分「没有」与「没测到」==============================
if [ "$measured" -eq 0 ]; then
  log "ABORT: 0 indexes measurable (unmeasured=$unmeasured) — 不是「无膨胀」，是「测不了」"
  echo "ABORT: 成功量到 0 个索引（unmeasured=${unmeasured}），本轮结论不成立" >&2
  exit 2
fi
if [ "$flagged" -eq 0 ] && [ "$unmeasured" -gt 0 ]; then
  log "no candidate among $measured measured, but $unmeasured unmeasured — 结论仅覆盖已测部分"
fi
# 「无候选」与「无问题」不是一回事：INVALID 索引从未被测量。
if [ "$flagged" -eq 0 ] && [ "$invalid_count" -gt 0 ]; then
  log "no bloat candidate, but $invalid_count INVALID index(es) exist — 本轮「无膨胀」不覆盖它们"
  echo "注意：$invalid_count 个 INVALID 索引未被测量（indisvalid=false），需另行 REINDEX/VALIDATE" >&2
fi

# === --fix：REINDEX CONCURRENTLY ====================================
# CONCURRENTLY 不阻塞读写，但会等待所有长事务；故设 lock_timeout，
# 抢不到锁就记 FAILED 跳过，不排队阻塞后续索引。
if [ "$MODE" = "fix" ] && [ "$flagged" -gt 0 ]; then
  done_count=0; reclaimed_mb=0
  # 按 headroom 降序，不按索引体积降序（候选集是按体积排的）。
  # 二者会选出不同的前 N：体积第 4 大的候选是 idx_route_incident_events_type_created
  # (111MB/54MB)，而 headroom 第 4 大是 idx_state_transitions_created (93MB/75MB)。
  # MAX_FIX_PER_RUN 的意义是「先做最值钱的几个」，所以必须按 headroom 排。
  flag_list=$(printf '%s' "$flag_list" | grep . | sort -t'|' -k3,3nr)
  while IFS='|' read -r idx fam_key headroom_mb reason; do
    [ -z "$idx" ] && continue
    [ -z "$fam_key" ] && fam_key="$idx"
    if [ "$done_count" -ge "$MAX_FIX_PER_RUN" ]; then
      log "SKIP $idx : reached MAX_FIX_PER_RUN=$MAX_FIX_PER_RUN, 留待下次"
      continue
    fi
    before=$(de "SELECT pg_relation_size('$idx');" | tr -d '[:space:]')
    [[ "$before" =~ ^[0-9]+$ ]] || { log "SKIP $idx : 无法读取当前体积"; continue; }
    log "REINDEX CONCURRENTLY $idx (before=$((before/1024/1024))MB reason=${reason})"
    if podman exec -e PGOPTIONS="-c statement_timeout=${REINDEX_TIMEOUT_MIN}min -c lock_timeout=${LOCK_TIMEOUT}" \
         "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -c "REINDEX INDEX CONCURRENTLY $idx;" \
         >> "$LOG" 2>&1; then
      after=$(de "SELECT pg_relation_size('$idx');" | tr -d '[:space:]')
      if [[ "$after" =~ ^[0-9]+$ ]] && [ "$after" -lt "$before" ]; then
        done_count=$((done_count + 1))
        reclaimed_mb=$(( reclaimed_mb + (before - after) / 1024 / 1024 ))
        log "REINDEX OK $idx : $((before/1024/1024))MB -> $((after/1024/1024))MB"
        # 记下重建后的密度作为该索引的自然填充率基线（见候选循环里的自校准段）。
        # 必须在重建后测，且取重建后的值 —— 重建前的密度是被膨胀污染过的。
        post_d=$(podman exec -e PGOPTIONS="-c statement_timeout=${PROBE_TIMEOUT}s" \
                   "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -c \
                   "SELECT avg_leaf_density FROM pgstatindex('$idx');" 2>&1 \
                 | head -1 | tr -d '[:space:]')
        if [[ "$post_d" =~ ^(NaN|[0-9.]+)$ ]]; then
          mkdir -p "$(dirname "$STATE")"
          tmp_state="${STATE}.tmp.$$"
          # 基线记在**族键**下：同一父表索引的各个叶分区共享一个自然填充率
          # （它们是同一份定义、同一批数据，只是分区不同）⇒ 记族键才能让
          # 「这一族已经重建到位」这件事在下一个分区上被认出来。
          { [ -f "$STATE" ] && awk -F'\t' -v k="$fam_key" '$1!=k' "$STATE" || true; \
            printf '%s\t%s\n' "$fam_key" "$post_d"; } > "$tmp_state"
          mv "$tmp_state" "$STATE"
          log "baseline  $idx : post-reindex density=${post_d}% 记入 ${STATE}（族键 ${fam_key}）"
        else
          log "baseline  $idx : 重建后密度读取失败（${post_d}），基线未更新"
        fi
      else
        log "REINDEX NO-GAIN $idx : $((before/1024/1024))MB -> $((after/1024/1024))MB（阈值判据有偏差，需复核）"
      fi
    else
      log "REINDEX FAILED $idx (锁等待或超时，未强制；下次运行会再次尝试)"
    fi
  done <<< "$flag_list"
  log "fix done: $done_count reindexed, ~${reclaimed_mb}MB reclaimed"
fi

# === 告警 ===========================================================
if [ -x "$NOTIFY" ]; then
  # INVALID 索引数常态为 0，出现即异常，故与膨胀阈值同级告警；
  # 不会造成刷屏（除非真有大量索引失效，那本身就值得知道）。
  if [ "$cand_mb" -ge "$ALERT_CANDIDATE_MB" ] || [ "$invalid_count" -gt 0 ]; then
    # 2026-10-18（§102）：此处此前只传了 **一个** 位置参数。
    # notify.sh 以 `set -euo pipefail` 开头并读 BODY="$2"，参数不足时
    # $2 未绑定 ⇒ set -u 在参数展开阶段就退出，**stdout/stderr 全空**、
    # 退出码 1。而本行的 `2>&1 || log` 会把根因吃掉，只留下一句自说自话的
    # `notify failed`。实测：2026-10-03 起 6/6 次 run 的告警全部静默失败，
    # 期间脚本已累计报出 471MB 的膨胀候选而无人知晓。
    # 契约见 notify.sh 用法：notify.sh <title> <body>。
    # 同时不要用 `2>&1` 吞掉 notify 自己的错误输出 —— 那正是根因消失的原因。
    "$NOTIFY" \
      "252 索引巡检：膨胀候选 $flagged 个（约 ${cand_mb}MB，已测 $measured / 未测 ${unmeasured}）；INVALID 未测 $invalid_count 个" \
      "候选索引：$(cut -f1 <<<"$flag_list" 2>/dev/null | paste -sd, -)" \
      || log "notify FAILED (exit=$?): 见上方 notify.sh 的 stderr"
  fi
fi

log "done mode=$MODE"
