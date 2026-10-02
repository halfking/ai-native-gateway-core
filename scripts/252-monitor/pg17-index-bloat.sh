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
# === REINDEX CONCURRENTLY ===
#   必须单语句下发。与 SET 放同一个 -c 会被 psql 包进隐式事务块，
#   报 "cannot run inside a transaction block"（pg17-vacuum-bloat.sh 踩过，
#   自 2026-07-15 起每个周日 100% 静默失败）。超时参数走 PGOPTIONS。
set -euo pipefail

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
log() { echo "[$ts] $*" >> "$LOG"; }

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
candidates=$(de "SELECT c.oid::regclass::text
                   FROM pg_index i
                   JOIN pg_class c  ON c.oid = i.indexrelid
                   JOIN pg_am am    ON am.oid = c.relam AND am.amname = 'btree'
                   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
                  WHERE c.relkind = 'i'
                    AND NOT c.relispartition
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
invalid_idx=$(de "SELECT c.oid::regclass::text
                  FROM pg_index i
                  JOIN pg_class c  ON c.oid = i.indexrelid
                  JOIN pg_am am    ON am.oid = c.relam AND am.amname = 'btree'
                  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
                 WHERE c.relkind = 'i'
                   AND NOT c.relispartition
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

while IFS= read -r idx; do
  [ -z "$idx" ] && continue
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
  headroom_mb=$(awk -v d="$dead_pages" -v l="$leaf_pages" -v y="$density" \
    'BEGIN{printf "%d", (d + l*(1-y/100))*8192/1048576}')
  reason=""
  [ "$dead_pages" -ge "$MIN_DEAD_PAGES" ] && reason="dead_pages=$dead_pages($(( dead_pages * 8192 / 1024 / 1024 ))MB)"
  if awk -v y="$density" -v t="$MIN_LEAF_DENSITY" 'BEGIN{exit !(y<t)}'; then
    reason="${reason:+$reason,}low_density=${density}%"
  fi
  # 累计 headroom 也能单独越过门槛（页数极多、每页都只空一点点：两个单信号都没破线，
  # 汇总起来却超过 MIN_RECLAIM_MB）。此时 reason 会是空串，日志会打出 "reason="
  # 这种读不出所以然的行，--fix 的日志同样带不出来。补一个兜底说明。
  [ -z "$reason" ] && reason="aggregate(leaf_pages=${leaf_pages}×${density}%空隙)"

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
  baseline=$(awk -F'\t' -v k="$idx" '$1==k{print $2; exit}' "$STATE" 2>/dev/null || true)
  if [ -n "$baseline" ] && awk -v y="$density" -v b="$baseline" -v t="$DENSITY_TOLERANCE" \
       'BEGIN{exit !(y >= b - t)}'; then
    log "ok        $idx size=${size_mb}MB density=${density}% >= 自身重建后基线 ${baseline}% -${DENSITY_TOLERANCE}（已达自然填充率，再重建无收益）"
    continue
  fi

  if [ "$headroom_mb" -ge "$MIN_RECLAIM_MB" ]; then
    flagged=$((flagged + 1))
    cand_mb=$((cand_mb + headroom_mb))
    flag_list="${flag_list}${idx}|${headroom_mb}|${reason}"$'\n'
    log "CANDIDATE $idx size=${size_mb}MB headroom≈${headroom_mb}MB reason=${reason}"
  else
    log "ok        $idx size=${size_mb}MB dead_pages=$dead_pages density=${density}% headroom≈${headroom_mb}MB"
  fi
done <<< "$candidates"

log "summary: measured=$measured unmeasured=$unmeasured flagged=$flagged headroom≈${cand_mb}MB invalid_skipped=$invalid_count"

# === 汇总必须能区分「没有」与「没测到」==============================
if [ "$measured" -eq 0 ]; then
  log "ABORT: 0 indexes measurable (unmeasured=$unmeasured) — 不是「无膨胀」，是「测不了」"
  echo "ABORT: 成功量到 0 个索引（unmeasured=$unmeasured），本轮结论不成立" >&2
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
  flag_list=$(printf '%s' "$flag_list" | grep . | sort -t'|' -k2,2nr)
  while IFS='|' read -r idx headroom_mb reason; do
    [ -z "$idx" ] && continue
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
          { [ -f "$STATE" ] && grep -v "^${idx}[[:space:]]" "$STATE" || true; \
            printf '%s\t%s\n' "$idx" "$post_d"; } > "$tmp_state"
          mv "$tmp_state" "$STATE"
          log "baseline  $idx : post-reindex density=${post_d}% 记入 $STATE"
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
    "$NOTIFY" "252 索引巡检：膨胀候选 $flagged 个（约 ${cand_mb}MB，已测 $measured / 未测 $unmeasured）；INVALID 未测 $invalid_count 个" \
      >> "$LOG" 2>&1 || log "notify failed"
  fi
fi

log "done mode=$MODE"
