#!/usr/bin/env bash
# P11 的 A/B 实测：巡检脚本的【两条基线路径】必须给出逐字相同的结果。
#
# ★★★ 为什么这是最重要的一个门：
#   「零语义风险」这个结论的唯一硬证据就是它。
#   新路（读每日 slot 汇总表）与旧路（扫源表）必须给出：
#     ① 相同的行集（slot, day, bucket_ts, c）；
#     ② 相同的中位数 p50；
#     ③ ★★ 相同的【判定结论】（ok / VOID / SLOW / NO-BASELINE）。
#   ⇒ 第 ③ 条才是巡检真正用来告警的那个量。
#
# ★ 这份脚本的夹具**故意**让每天的行数不同（昼夜节律 + 周内波动）——
#   若每天行数相同，窗口边界差异会被平均掉 ⇒ 测不出问题 ⇒ 假绿。
#
# 用法：bash scripts/audit/verify_892_patrol_ab.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
DBUSER="${PG_USER:-llm_gateway}"
DB="itgate_ab892"

SQL_TPL="$REPO_ROOT/scripts/audit/sql/verify_892_patrol_ab.sql"
UP="$REPO_ROOT/sql/migrations/startup/892_ursm_snapshot_slot_daily.sql"

TMP="$(mktemp /tmp/verify_892ab_XXXXXX.sql)"
trap 'rm -f "$TMP"' EXIT

python3 - "$SQL_TPL" "$UP" "$TMP" <<'PY'
import sys
tpl, up, out = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(tpl, encoding="utf-8").read()
s = s.replace(">>>MIGRATION_UP<<<", open(up, encoding="utf-8").read())
open(out, "w", encoding="utf-8").write(s)
PY

echo "== 重建一次性库 $DB =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d postgres \
  -c "DROP DATABASE IF EXISTS $DB;" -c "CREATE DATABASE $DB;" || exit 2

echo "== 跑 A/B =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d "$DB" -f /dev/stdin < "$TMP"
rc=$?

echo "== 清理一次性库 =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d postgres -c "DROP DATABASE IF EXISTS $DB;" >/dev/null
exit $rc
