#!/bin/bash
# q-211-readonly-sql.sh —— 在 211 的 acc-pg17 容器内跑一条**只读** SQL。
#
# 凭据不落盘：从容器 env 取 POSTGRES_PASSWORD（ssh 到目标机后在本机执行，
# 密钥全程不出 211）；SQL 走 stdin ⇒ 无需任何转义。
#
# 用法:
#   ./q-211-readonly-sql.sh "SELECT ..."
#   ./q-211-readonly-sql.sh -f some.sql
#
# 用途：2026-11-16 对账页数据完整性审计轮（docs/audit/2026-11-16-对账页供应商与模型
# 数据完整性审计-主档.md）的取证工具。
set -euo pipefail
if [[ "${1:-}" == "-f" ]]; then
  SQL_FILE="${2:?usage: q.sh -f file.sql}"
  cat "$SQL_FILE" | ssh -o BatchMode=yes 211 'docker exec -i acc-pg17 sh -c "PGPASSWORD=$(printenv POSTGRES_PASSWORD) psql -w -U acc_super -d llm_gateway -X -A -F\"|\" -t -f -"'
else
  SQL="${1:?usage: q.sh \"SQL\"}"
  printf '%s\n' "$SQL" | ssh -o BatchMode=yes 211 'docker exec -i acc-pg17 sh -c "PGPASSWORD=$(printenv POSTGRES_PASSWORD) psql -w -U acc_super -d llm_gateway -X -A -F\"|\" -t -f -"'
fi
