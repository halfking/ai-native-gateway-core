#!/usr/bin/env bash
# analyze-partition-stats.sh — 154/245 经 SSH 对 252 llm_gateway 执行分区 ANALYZE
#
# 用法:
#   bash scripts/ops/analyze-partition-stats.sh 154
#   bash scripts/ops/analyze-partition-stats.sh 245
#   bash scripts/ops/analyze-partition-stats.sh 154 --months 3
#
set -euo pipefail

TARGET=${1:-154}
MONTHS=2
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --months) MONTHS=$2; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

case "$TARGET" in
  154)
    SSH_HOST=${LLM_GATEWAY_154_SSH:-root@47.97.111.154}
    ENV_FILE=/etc/llm-gateway-go/env
    ;;
  245)
    SSH_HOST=${LLM_GATEWAY_245_SSH:-root@8.136.114.245}
    ENV_FILE=/opt/llm-gateway-go/.env
    ;;
  *) echo "target must be 154 or 245" >&2; exit 1 ;;
esac

SSH_PORT=${LLM_GATEWAY_SSH_PORT:-25022}
SSH_KEY="${SSH_KEY_FILE:-}"
for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
  [[ -f "$k" ]] && SSH_KEY="$k" && break
done

SSH=(ssh -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
[[ -n "$SSH_KEY" ]] && SSH+=(-i "$SSH_KEY")
SSH+=("$SSH_HOST")

echo "[analyze] target=$TARGET months=$MONTHS env=$ENV_FILE"

"${SSH[@]}" "set -euo pipefail
ENV='$ENV_FILE'
DB=\$(grep '^LLM_GATEWAY_DATABASE_URL=' \"\$ENV\" | cut -d= -f2-)
test -n \"\$DB\"
psql \"\$DB\" -v ON_ERROR_STOP=1 -c \"SELECT apply_llm_gateway_autovacuum_settings() AS autovacuum_tables_set;\"
psql \"\$DB\" -v ON_ERROR_STOP=1 -c \"SELECT analyze_llm_gateway_table_stats($MONTHS) AS tables_analyzed;\"
psql \"\$DB\" -c \"
-- 口径：**按父表递归枚举**，不用名字前缀正则（2026-10-20 复核轮改）。
--
-- 为什么：待删标志在**父表**上（迁移 860 把 request_logs 族父表改名成
-- _request_logs*，**分区子表名保持不变** —— 那是 860 的明确设计：
-- 分区随父表一起 DROP，索引名同理保持）。所以子表名**不带** _ 前缀，
-- 而旧口径（relname ~ '^(...|_request_logs)_(202[0-9]_[0-9]{2}|hot)\$'）
-- 恰恰要求子表名带 _request_logs 前缀 ⇒ **漏掉 7 张真实存在的分区**
-- （request_logs_2026_07/08/10/11/default + request_logs_bodies_2026_10/11），
-- 这些分区从此既不被 ANALYZE 也不被列存轮转看到。
--
-- 而且「按名字枚举」本身是**结构性脆弱**的：新装环境与存量环境都由
-- 正典基线 schema 造出**不带前缀**的分区名（deploy/sql/schemas/baseline/01-schema.sql
-- 建 request_logs_2026_07/08，860 只改父表）⇒ 两边都漏，不是「分叉」。
-- 改成从 pg_inherits 走父表 ⇒ 无论子表叫什么都能被看到。
SELECT s.relname, s.last_analyze, s.last_autoanalyze, s.n_live_tup
FROM pg_stat_user_tables s
JOIN pg_class c ON c.relname = s.relname
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE c.oid IN (
        -- 分区子表：父表在名单里就算
        SELECT i.inhrelid
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname IN ('credential_model_index', 'model_probe_runs', '_request_logs')
      )
   OR c.relname IN ('credential_model_index_hot', 'model_probe_runs_hot', '_request_logs_hot')
ORDER BY s.relname;
\""

echo "[analyze] done"
