#!/usr/bin/env bash
# pg17-index-reader-census.sh —— 「这条索引有谁在用」普查（2026-10-18，§82）
#
# 为什么这道巡检存在：
#   pg17-duplicate-index-check.sh 的页脚写着「删索引前须自行确认无查询依赖」，
#   而**仓库里没有任何工具能做这件事**。§79/§81 那两轮普查都是手写 SQL，
#   两次都写错了：
#     ① 筛选条件 `AND query ~* 'request_id'` 把整族「ts 范围不带 tenant_id」
#        的聚合查询一并漏掉 ⇒ 得出「没有任何读查询依赖」的错误结论；
#     ② `ILIKE '%sessions%'` 把 **goal_sessions** 的 52,950 次读算成
#        sessions 表的承重读方。
#   ⇒ 「普查」这件事本身需要一个工具，而不是每次现写 SQL。
#
# 本脚本刻意**不做**的事：
#   · **不做任何列名过滤。** 只列出「碰了这张表的全部语句形态」。
#     §81 的教训正是：任何按列名的筛选都会把读者挡在外面，而筛选条件
#     是人随手写的。
#   · **不判定「有没有依赖」。** 它给证据，不给结论。
#
# ★ 本脚本无条件在输出里打出**普查窗口长度**与**pg_stat_statements 容量**，
#   因为它们决定这份普查能证明什么：
#     · 窗口只有 8 天 ⇒ 「没查到低频读方」**不能**推出「没有低频读方」；
#     · track=top 且 tracked 逼近 max 时，新来的低开销语句**根本不会被记录**。
#   任何「据此删索引」的结论都必须带上这两项。
#
# ★ 为什么**不叫 pg17-***、也不进 etc.cron.d.pg17：
#   本目录的命名约定（`pg17-*` / `ursm-*`）等于「这是一个定时巡检」，
#   cron 登记门就是按这个前缀划候选集的。
#   本工具是**按需诊断**——它只在「有人要删索引、需要看读方」时被调用。
#   挂 cron 会每小时打一份语句形态清单：既没有可告警的判据，
#   又会把真正有用的巡检日志轮转掉（错误因此更难被翻到）。
#   ⇒ 按约定**本意**办：名字不占前缀，门自然不要求它登记。
#   同 precedent：browser-*-test.py 也是因为不占前缀而被排除。
#
# 用法：index-reader-census.sh <索引名> [TOP_N]
#   TOP_N 默认 25，仅截断打印；**总数照报**。
#
# 退出码（与同目录其余巡检同一套契约）：
#   0  产出普查结果（命中 0 条也是结果 —— 但结论强度取决于上面的窗口与容量）
#   2  用法错误 / 索引不存在
#   3  **本次没有结论**（pg_stat_statements 不可用 / psql 失败）
#
# ★ 本脚本不 DROP 任何东西，不改任何东西，纯只读。

set -uo pipefail

IDX=${1:-}
TOP_N=${2:-25}

if [ -z "$IDX" ]; then
  echo "用法: $(basename "$0") <索引名> [TOP_N]" >&2
  echo "例：  $(basename "$0") idx_usage_ledger_part_ts" >&2
  exit 2
fi

case "$TOP_N" in
  ''|*[!0-9]*) echo "ABORT: TOP_N 必须是非负整数，实得 '$TOP_N'" >&2; exit 2 ;;
esac

CONTAINER=${PG17_CONTAINER:-pg-252-pg17}
PG_USER=${PG17_USER:-postgres}
DBNAME=${PG17_DB:-llm_gateway}
PSQL_CMD=${PSQL_CMD:-docker exec -i "$CONTAINER" psql -U "$PG_USER" -X -q -A -t -F'|' -v ON_ERROR_STOP=1 -d "$DBNAME" -c}

log() { printf '%s %s\n' "$(date -Is)" "$*" >&2; }

# 索引名里的引号会打断 SQL 字面量。只允许标识符字符，其余一律拒。
case "$IDX" in
  *[!A-Za-z0-9_.$]*) log "ABORT: 索引名只允许标识符字符（字母数字 _ . $），实得 '$IDX'"; exit 2 ;;
esac

# ★ 2026-10-19 修正（§143.13）：索引名**必须用双引号**展开进 SQL。
#   原写法 `SQL=${SQL/__IDX__/'$IDX'}` 把替换值写在**单引号**里
#   ⇒ shell 不展开 $IDX ⇒ SQL 里留下字面量 `$IDX`
#   ⇒ 真库直接报 `syntax error at or near "$"` ⇒ 工具**从来没跑起来过**
#     （exit 3「量具不可用」，不是「索引不存在」的结论）。
#   ⇒ 修法：`"'$IDX'"`（双引号里放单引号）⇒ 展开成 SQL 字符串字面量 'session_turns'。
#   ⇒ 安全性：上面的字符类守卫已把 $IDX 限死在 [A-Za-z0-9_.$] ⇒ 展开不可能破字面量。
#   ⚠ **踩过的坑（别再试）**：psql 的变量插值（`:'var'`）**只在 stdin 路径生效**，
#     `-c "select :'v'"` **不插值**（2026-10-19 本地 PG 17.10 实测：
#     `-c` 报 `syntax error at or near ":"`，同一语句走 stdin 则正确返回 hello）。
#     ⇒ 想用 psql 变量就必须把 SQL 走 stdin，而本脚本的 PSQL_CMD 契约是 `-c`。
#   ⚠ 这类 bug 在假 psql 桩下**永远不会暴露**（桩照单全收）——
#     脚本里已有同款注释（关于 `c.relkind::text`），这是同仓**第三次**同族事故。
#     ⇒ 已补一道**真库**门（把实际下发的 SQL 打到真 PG 上跑，见 ursmcheck）。
SQL=$(cat <<'SQL'
-- ★ `c.relkind` 必须 ::text —— 它的类型是 "char"，
--   `"char" || unknown` 算子不唯一，真库直接报
--   `operator is not unique: "char" || unknown`。
--   这个 bug 在假 psql 桩下**永远不会暴露**（桩对 SQL 照单全收），
--   只有把这段真 SQL 打到真库才看得见 ⇒ 与本仓 $1 那条同一个坑，第二次。
-- 元数据：索引 → 表 / 占用 / idx_scan / 受约束 / 键列
-- ★ 父表索引自身恒为 0 字节（分区父表的正常渲染），所以对 relkind='p'
--   一律取 pg_inherits 的跨分区聚合；直接报父表大小会得到「0 bytes」，
--   读起来像「量具坏了」。idx_scan 同理：父表上恒为空。
SELECT coalesce((
  SELECT c.relkind::text || '|' || t.relname || '|' ||
         COALESCE((SELECT sum(pg_relation_size(ch.inhrelid::regclass::oid))
                     FROM pg_inherits ch WHERE ch.inhparent = i.indexrelid),
                  pg_relation_size(i.indexrelid))::bigint || '|' ||
         COALESCE((SELECT sum(COALESCE(cs.idx_scan, 0))
                     FROM pg_inherits ch
                     JOIN pg_stat_user_indexes cs ON cs.indexrelid = ch.inhrelid
                    WHERE ch.inhparent = i.indexrelid),
                  COALESCE((SELECT s2.idx_scan FROM pg_stat_user_indexes s2
                             WHERE s2.indexrelid = i.indexrelid), 0))::bigint || '|' ||
         (SELECT count(*) FROM pg_constraint k WHERE k.conindid = i.indexrelid)::text || '|' ||
         i.indnkeyatts::text || '|' ||
         coalesce((SELECT string_agg(pg_get_indexdef(i.indexrelid, k, true), ',')
                     FROM generate_series(1, i.indnkeyatts) k), '')
  FROM pg_index i
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_class c  ON c.oid  = i.indrelid
  JOIN pg_class t  ON t.oid  = i.indrelid
  WHERE ic.relname = __IDX__), '');
SQL
)
# ⚠ 替换值必须用**双引号**包住：单引号会让 shell 不展开 $IDX（详见上方注释）。
SQL=${SQL/__IDX__/"'$IDX'"}

psql_17() {
  # shellcheck disable=SC2086 # PSQL_CMD 是刻意按多词命令传入的
  $PSQL_CMD "$1" 2>&1
}

meta=$(psql_17 "$SQL")
rc=$?
if [ $rc -ne 0 ]; then
  log "ABORT: psql 失败 rc=$rc —— 量具不可用（不是「索引不存在」的结论）。"
  printf '%s\n' "$meta" >&2
  exit 3
fi

if [ -z "$meta" ]; then
  log "索引 '$IDX' 不存在于本库（本脚本不据此推断它是否被使用）。"
  exit 2
fi

IFS='|' read -r RELKIND TBL BYTES SCANS NCONSTRAINED NKEYS KEYCOLS <<< "$meta"

# 表名要进**正则字面量**，所以只能出现标识符字符。
# 表名来自 pg_class（不是用户输入），但仍然必须挡住引号/反斜杠 ——
# 否则一条带引号的表名会把正则字面量劈开 ⇒ SQL 语法错 ⇒ exit 3。
case "$TBL" in
  *[!A-Za-z0-9_$]*|"")
    log "ABORT: 表名 '$TBL' 含非常规字符，无法安全内联进正则字面量 —— 本次不出结论。"
    exit 3
    ;;
esac

echo "索引        : $IDX"
echo "表          : $TBL   （relkind=${RELKIND}，键列 ${NKEYS} 个：${KEYCOLS}）"
if [ "$RELKIND" = "p" ]; then
  echo "            ★ 这是**分区父表**上的索引：CREATE TABLE … PARTITION OF 会克隆它。"
fi
echo "占用        : ${BYTES} 字节（relkind='p' 时为 pg_inherits 跨分区聚合值）"
echo "idx_scan    : ${SCANS}（同一聚合口径）"
echo "受约束      : ${NCONSTRAINED}（>0 = 被 PK/UNIQUE 背着，不能直接删索引）"

# ---- 第二段：普查窗口 + 容量（决定这份普查能证明什么）--------------------
SQL_WIN=$(cat <<'SQL'
WITH info AS (
  SELECT stats_reset FROM pg_stat_statements_info
)
SELECT coalesce(to_char(info.stats_reset, 'YYYY-MM-DD HH24:MI:SS'), '(取不到)') || '|' ||
       coalesce(date_trunc('second', now() - info.stats_reset)::text, '(取不到)') || '|' ||
       (SELECT count(*) FROM pg_stat_statements)::text || '|' ||
       coalesce((SELECT setting FROM pg_settings WHERE name = 'pg_stat_statements.max'), '(未知)') || '|' ||
       coalesce((SELECT setting FROM pg_settings WHERE name = 'pg_stat_statements.track'), '(未知)')
FROM info;
SQL
)

win=$(psql_17 "$SQL_WIN")
rc=$?
if [ $rc -ne 0 ] || [ -z "$win" ]; then
  log "ABORT: 取不到 pg_stat_statements 窗口 —— 没有它，本次普查不成立（exit 3）。"
  printf '%s\n' "$win" >&2
  exit 3
fi

WIN_START=${win%%|*}
rest=${win#*|}
WIN_LEN=${rest%%|*}
rest=${rest#*|}
TRACKED=${rest%%|*}
rest=${rest#*|}
MAXROWS=${rest%%|*}
TRACK=${rest##*|}

echo
echo "普查窗口    : 自 $WIN_START 起，共 $WIN_LEN"
echo "统计容量    : 已记录 $TRACKED / 上限 ${MAXROWS}（track=${TRACK}）"
if [ "$TRACK" = "top" ]; then
  echo "              ★ track=top ⇒ **未进入 top-N 的语句根本不会被记录**。"
  echo "                「没查到某类读方」不等于「不存在该读方」。"
fi

# ---- 第三段：全量语句形态（不做任何列名过滤）---------------------------
SQL_Q=$(cat <<'SQL'
SELECT s.calls::text || '|' ||
       round(s.mean_exec_time::numeric, 1)::text || '|' ||
       round(s.total_exec_time::numeric / 1000, 1)::text || '|' ||
       replace(replace(left(s.query, 400), E'\n', ' '), E'\t', ' ')
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
WHERE d.datname = current_database()
  AND s.query ~* '(FROM|JOIN|UPDATE|INTO)[[:space:]]+(public\.)?__TBL__(_20[0-9]{2}_[0-9]{2})?[[:space:](,]'
ORDER BY s.calls DESC, s.total_exec_time DESC
LIMIT __TOPN__;
SQL
)
SQL_Q=${SQL_Q/__TBL__/$TBL}
SQL_Q=${SQL_Q/__TOPN__/$TOP_N}

qs=$(psql_17 "$SQL_Q")
rc=$?
if [ $rc -ne 0 ]; then
  log "ABORT: psql 失败 rc=$rc —— 语句形态取不到，本次不出结论（不报健康）。"
  printf '%s\n' "$qs" >&2
  exit 3
fi

n=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  n=$((n + 1))
done <<< "$qs"

echo
# ★ 必须单独取**总数**。只报「打印了 N 条」会把截断读成「只有 N 种读法」，
#   而 §81 的教训正是「没打印出来 ≠ 不存在」。
SQL_CNT=$(cat <<'SQL'
SELECT count(*)::text
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
WHERE d.datname = current_database()
  AND s.query ~* '(FROM|JOIN|UPDATE|INTO)[[:space:]]+(public\\.)?__TBL__(_20[0-9]{2}_[0-9]{2})?[[:space:](,]';
SQL
)
SQL_CNT=${SQL_CNT/__TBL__/$TBL}

total=$(psql_17 "$SQL_CNT")
rc=$?
if [ $rc -ne 0 ] || ! printf '%s' "$total" | grep -qE '^[0-9]+$'; then
  log "ABORT: 语句形态**总数**取不到（rc=${rc}，输出='$total'）——"
  log "       没有它就分不清「只有这些」与「只打印了这么多」，本次不出结论。"
  exit 3
fi

echo "语句形态    : 共 $total 种，打印前 $n 条（TOP_N=${TOP_N}）"
if [ "$total" -gt "$TOP_N" ]; then
  echo "              ★ 打印的是**按 calls 排序的前 $TOP_N 种**，不是全部 $total 种；"
  echo "                「没打印出来」不等于「不存在」。加大 TOP_N 再看。"
fi
if [ "$n" -eq 0 ]; then
  echo "              ★ 0 条**不是**「没有读方」，只是「窗口 + 容量内没查到」。"
else
  printf 'calls\tmean_ms\ttotal_s\t语句\n'
  i=0
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    i=$((i + 1))
    [ $i -gt "$TOP_N" ] && break
    c=$(printf '%s' "$line" | cut -d'|' -f1)
    m=$(printf '%s' "$line" | cut -d'|' -f2)
    tt=$(printf '%s' "$line" | cut -d'|' -f3)
    q=$(printf '%s' "$line" | cut -d'|' -f4-)
    printf '%s\t%s\t%s\t%s\n' "$c" "$m" "$tt" "$q"
  done <<< "$qs"
fi

echo
echo "这份普查**不能**证明的事（请逐条读，别跳过）："
echo "  1. 窗口只有 $WIN_LEN —— 低于该频率的读方（比如月报）不会出现在这里。"
echo "  2. track=${TRACK}，容量 $TRACKED/$MAXROWS —— 未进 top-N 的语句压根没被记录。"
echo "  3. pg_stat_statements 不记录**谁发的** —— 网关自动查询与手工 psql 混在一起。"
echo "  4. 「列出了 N 条形态」不等于「只有 N 种读法」：同一条语句可能命中也可能不命中某索引。"
echo "     要看真走哪条索引，请对具体语句跑 EXPLAIN（COSTS ON）。"
exit 0
