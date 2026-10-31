#!/usr/bin/env bash
# collect-canonical-statements.sh —— R179 轮：从**生产库**（252，只读）采集
# 「所有碰到 canonical 视图（request_logs_with_current_month）的真实语句」。
#
# # 为什么用 pg_stat_statements 而不是从 Go 源码重建 SQL
#
# ★★ 源码里的读点 SQL 大量是**运行时拼接**的（★ `...` + tenantWhere + `...`），
#   ⇒ ⇒ **静态重建出来的 SQL 是不完整的**（★ R179 第一版实测：62/66 个形态
#   ⇒ ⇒ EXPLAIN 直接报语法/语义错误）⇒ ⇒ ⇒ **量具必须换一个事实源**。
#
# ★★ `pg_stat_statements` 记的是**数据库真的跑过的语句**（★ 常量已归一化成 $N）
#   ⇒ ⇒ ① 它**包含**运行时拼接的中间段；② 它**就是生产上真实在跑的形态**；
#   ③ 它自带【调用次数】与【平均耗时】⇒ ⇒ 「哪些读点真的重」也有事实依据。
#
# ★ 本脚本**只读**：只跑 SELECT，且只连 252（只读库）。
#
# 用法：
#   bash scripts/audit/collect-canonical-statements.sh > scripts/audit/asset/canonical-view-statements.tsv
#
# ★ 输出列：calls	total_exec_time_ms	mean_exec_time_ms	rows	query

set -euo pipefail

HOST="${KX_252_HOST:-115.29.212.252}"
PORT="${KX_252_PORT:-25022}"
USER="${KX_252_USER:-root}"
CONTAINER="${KX_252_CONTAINER:-pg-252-pg17}"
DB_USER="${KX_252_DB_USER:-llm_gateway}"
DB_NAME="${KX_252_DB_NAME:-llm_gateway}"

cat <<'SQL' | ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	-o ConnectTimeout=8 -o ControlMaster=auto -o ControlPath=/tmp/kx-pbp/ssh-252-stmt \
	-o ControlPersist=600 -p "${PORT}" "${USER}@${HOST}" \
	"docker exec -i ${CONTAINER} psql -X -q -A -t -F '	' -v ON_ERROR_STOP=1 -U ${DB_USER} -d ${DB_NAME} -f -"
SET statement_timeout = '60s';
SELECT calls,
       round(total_exec_time::numeric, 1),
       round(mean_exec_time::numeric, 3),
       rows,
       regexp_replace(query, '\s+', ' ', 'g')
FROM pg_stat_statements
WHERE query ILIKE '%request_logs_with_current_month%'
  AND dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY calls DESC, query;
SQL
