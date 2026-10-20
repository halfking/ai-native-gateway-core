#!/usr/bin/env bash
# P11 迁移 892 的本地真库实测（一次性库，退出即 DROP）。
#
# 为什么要有它：迁移是【写生产】的动作 ⇒ 上线前必须先在真库上证明
#   ① 迁移能跑通；② ★★ 填充产出与「直接算 base_buckets」判定等价
#      （这是「零语义风险」这个结论的**唯一**硬证据）；
#   ③ 幂等；④ 首次回填让表立刻非空（否则巡检会失明）；⑤ down 干净。
#
# 用法：
#   bash scripts/audit/verify_892_local.sh
#
# ⚠ 只在本地一次性库上跑；**不碰任何生产库**。

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
DBUSER="${PG_USER:-llm_gateway}"
DB="itgate_892"

SQL_TPL="$REPO_ROOT/scripts/audit/sql/verify_892_local.sql"
UP="$REPO_ROOT/sql/migrations/startup/892_ursm_snapshot_slot_daily.sql"
DOWN="$REPO_ROOT/sql/migrations/startup/892_ursm_snapshot_slot_daily.down.sql"

TMP="$(mktemp /tmp/verify_892_XXXXXX.sql)"
trap 'rm -f "$TMP"' EXIT

python3 - "$SQL_TPL" "$UP" "$DOWN" "$TMP" <<'PY'
import sys
tpl, up, down, out = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
s = open(tpl, encoding="utf-8").read()
s = s.replace(">>>MIGRATION_UP<<<", open(up, encoding="utf-8").read())
s = s.replace(">>>MIGRATION_DOWN<<<", open(down, encoding="utf-8").read())
open(out, "w", encoding="utf-8").write(s)
PY

echo "== 重建一次性库 $DB =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d postgres \
  -c "DROP DATABASE IF EXISTS $DB;" -c "CREATE DATABASE $DB;" || exit 2

echo "== 跑实测 =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d "$DB" -f /dev/stdin < "$TMP"
rc=$?

echo "== 清理一次性库 =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d postgres -c "DROP DATABASE IF EXISTS $DB;" >/dev/null
exit $rc
