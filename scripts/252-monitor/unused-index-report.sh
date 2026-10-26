#!/usr/bin/env bash
# unused-index-report.sh —— 「这条索引 14 天没人用过」普查（2026-10-19，§143.9）
#
# 为什么这道工具存在：
#   仓里已有三条相邻的轴，**没有一条覆盖「未被使用」**：
#     · pg17-index-bloat.sh            → 索引**膨胀**（页密度）
#     · pg17-duplicate-index-check.sh  → **重复**与前缀覆盖
#     · index-reader-census.sh          → 「这张表有谁在读」（语句形态取证）
#   而「14 天零扫描、却占着 387MB」是**第四条轴**：它既不膨胀、也不重复、
#   而且表本身还很热（session_dim 的表有 5292 万次索引扫描）—— 只是这一条没人选。
#
# ★ 本工具**只给证据，不给结论**，且与 index-reader-census.sh 配套：
#   本工具回答「**这条索引**没人用过」；
#   index-reader-census.sh 回答「**这张表**有谁在读、读了哪些列」。
#   ⇒ 任何「据此删索引」的结论必须带上 census 的读方证据。
#
# ★★ 本工具无条件打印**统计窗口**，因为它决定这份普查能证明什么：
#     · PG 重启会清零 idx_scan ⇒ 窗口 = postmaster 启动至今（252 实测 14 天）；
#     · 窗口只有 14 天 ⇒「14 天没人用过」**不能**推出「永远没人用」
#       （月度报表、人工排查、灾备恢复都可能落在窗口外）；
#     · ⚠ 纯 INSERT **不计入** idx_scan ⇒「只写不读」的索引必然显示 0，
#       而它每次写都在付维护成本 —— 这是**存储 + 写放大**双输，不是零成本。
#       （2026-10-19 本地沙箱实测：ON CONFLICT +2 / DO UPDATE +1 /
#         显式 SELECT +1 / 纯 INSERT +0。）
#
# 用法：unused-index-report.sh [最小字节数]
#   默认 10MB。只读，不改任何东西。

set -uo pipefail

MIN_BYTES=${1:-10485760}
case "$MIN_BYTES" in
  ''|*[!0-9]*) echo "ABORT: 最小字节数必须是非负整数，实得 '$MIN_BYTES'" >&2; exit 2 ;;
esac

CONTAINER=${PG17_CONTAINER:-pg-252-pg17}
PG_USER=${PG17_USER:-llm_gateway}
DBNAME=${PG17_DB:-llm_gateway}
PSQL_CMD=${PSQL_CMD:-docker exec -i "$CONTAINER" psql -U "$PG_USER" -X -q -A -F'|' -v ON_ERROR_STOP=1 -d "$DBNAME" -c}

echo "=== 未使用索引普查（阈值 ${MIN_BYTES} 字节）==="

# ---- ① 统计窗口（决定这份普查能证明什么）--------------------------------
$PSQL_CMD "
SELECT 'window|postmaster_start|' || pg_postmaster_start_time()::text
UNION ALL SELECT 'window|uptime|' || (now() - pg_postmaster_start_time())::text
UNION ALL SELECT 'window|pg_stat_statements_rows|' || count(*)::text FROM pg_stat_statements
UNION ALL SELECT 'window|pg_stat_statements_reset|' || COALESCE((SELECT stats_reset::text FROM pg_stat_statements_info), 'n/a')
UNION ALL SELECT 'window|total_idx_scan|' || sum(idx_scan)::text FROM pg_stat_user_indexes
UNION ALL SELECT 'window|indexes_total|' || count(*)::text FROM pg_stat_user_indexes
UNION ALL SELECT 'window|indexes_used|' || count(*) FILTER (WHERE idx_scan>0)::text FROM pg_stat_user_indexes;
" 2>/dev/null || echo "window|__ERROR__|pg_stat_statements 不可用（不影响 idx_scan 口径）"

# ---- ② 分类汇总：按「删了会不会复发 / 删索引是不是等于删约束」-----------
$PSQL_CMD "
WITH z AS (
  SELECT s.indexrelid, s.relid, t.n_live_tup, t.idx_scan,
         pg_relation_size(s.indexrelid) AS sz,
         (con.conindid IS NOT NULL) AS cb,
         c.relispartition,
         (SELECT pr.relname FROM pg_inherits i JOIN pg_class pr ON pr.oid=i.inhparent WHERE i.inhrelid=s.relid) AS parent
  FROM pg_stat_user_indexes s
  JOIN pg_stat_user_tables t ON t.relid = s.relid
  JOIN pg_class c ON c.oid = s.relid
  LEFT JOIN pg_constraint con ON con.conindid = s.indexrelid
  WHERE s.idx_scan = 0 AND pg_relation_size(s.indexrelid) >= $MIN_BYTES
)
SELECT CASE
         WHEN relispartition AND parent IS NOT NULL THEN 'C_叶分区索引(删了会长回来,须动父表声明)'
         WHEN cb                              THEN 'D_非分区表的约束附带索引(删索引=删约束)'
         ELSE 'E_非分区表的独立索引(删了就没了)'
       END,
       count(*), sum(sz)
FROM z GROUP BY 1 ORDER BY 1;
" 2>/dev/null || echo "__ERROR__|分类查询失败"

# ---- ③ 明细 ------------------------------------------------------------
$PSQL_CMD "
WITH z AS (
  SELECT s.indexrelid, s.relid, t.n_live_tup, t.idx_scan, t.seq_scan,
         pg_relation_size(s.indexrelid) AS sz,
         (con.conindid IS NOT NULL) AS cb,
         c.relispartition,
         (SELECT pr.relname FROM pg_inherits i JOIN pg_class pr ON pr.oid=i.inhparent WHERE i.inhrelid=s.relid) AS parent
  FROM pg_stat_user_indexes s
  JOIN pg_stat_user_tables t ON t.relid = s.relid
  JOIN pg_class c ON c.oid = s.relid
  LEFT JOIN pg_constraint con ON con.conindid = s.indexrelid
  WHERE s.idx_scan = 0 AND pg_relation_size(s.indexrelid) >= $MIN_BYTES
)
SELECT z.parent IS NOT NULL,
       CASE WHEN z.cb THEN '约束附带' ELSE '独立索引' END,
       coalesce(z.parent, t2.relname),
       i.relname,
       z.sz,
       z.n_live_tup,
       t2.idx_scan,
       t2.seq_scan,
       pg_get_indexdef(z.indexrelid)
FROM z
JOIN pg_class i ON i.oid = z.indexrelid
JOIN pg_stat_user_tables t2 ON t2.relid = z.relid
ORDER BY z.sz DESC;
" 2>/dev/null || echo "__ERROR__|明细查询失败"

echo
echo "=== 下一步（不自动做）==="
echo "1) 对上面 E 类（独立索引、非分区表）逐条跑 index-reader-census.sh <表名> 取证读方；"
echo "2) 注意窗口只有上面 window|uptime 那一行 ⇒ 窗口外仍可能有低频读方；"
echo "3) 纯 INSERT 不计入 idx_scan ⇒ 只写不读的索引必然上榜，而它每次写都在付维护成本；"
echo "4) 任何 DROP INDEX 都是生产写，须属主授权，并应走 startup 迁移（§126 立的规）。"
