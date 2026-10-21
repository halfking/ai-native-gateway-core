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

# ★★ 兜底载体（§19.20.62 修）：★ 一次性【库】的回收必须挂在这个载体上。
#   ⇒ ★ 原先这里只有 `trap 'rm -f "$TMP"' EXIT` ⇒ 它只删临时 SQL 文件、**不回收库**
#   ⇒ ⇒ 而库回收只在【正常路径】末尾 ⇒ 任何提前退出（建库失败 `|| exit 2` /
#      `set -u` 报错 / Ctrl-C / 被杀）都**泄漏一次性库** ⇒ ⇒ 这是**真缺陷**
#      （不是「存在性」问题：★ 实测「中途 SIGTERM」⇒ 未修版残留 1 个库、修后版残留 0 个）。
#   ⇒ ⇒ ★「回收」与「收割」挂【同一个】载体；★ **不挂 INT/TERM**
#      （实测 bash 会把 trap 推迟到当前前台命令结束 ⇒ 挂信号 trap 反而让回收更晚 ⇒ 详见 §19.20.59）。
cleanup() {
  # ★ 防卡保护（三件）：
  #   ① 先踢掉残留连接（否则 DROP 会卡在别的会话上 ⇒ 整个脚本卡死）
  #   ② statement_timeout（给 DROP 自己设上限 ⇒ 防「慢 DROP 把退出卡住」）
  #   ③ WITH (FORCE)（PG 13+ ⇒ 兜底再踢一次连接）
  docker exec -i "$CONTAINER" psql -U "$DBUSER" -d postgres -q \
    -c "SET statement_timeout = '120s'" \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$DB' AND pid <> pg_backend_pid();" \
    -c "DROP DATABASE IF EXISTS \"$DB\" WITH (FORCE);" >/dev/null 2>&1 || true
}
trap 'cleanup; rm -f "$TMP"' EXIT

python3 - "$SQL_TPL" "$UP" "$DOWN" "$TMP" <<'PY'
import sys
tpl, up, down, out = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
s = open(tpl, encoding="utf-8").read()
s = s.replace(">>>MIGRATION_UP<<<", open(up, encoding="utf-8").read())
s = s.replace(">>>MIGRATION_DOWN<<<", open(down, encoding="utf-8").read())
open(out, "w", encoding="utf-8").write(s)
PY

echo "== 重建一次性库 $DB =="
# ★ 删旧库走【同一个】cleanup（★ 避免两份 DROP 实现漂移 ⇒ 也顺带拿到防卡保护）
cleanup
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d postgres -c "CREATE DATABASE $DB;" || exit 2

echo "== 跑实测 =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d "$DB" -f /dev/stdin < "$TMP"
rc=$?

echo "== 清理一次性库 =="
# ★ 正常跑完也走同一个 cleanup（★ EXIT trap 还会再跑一次 ⇒ DROP IF EXISTS ⇒ 无害）
cleanup
exit $rc
