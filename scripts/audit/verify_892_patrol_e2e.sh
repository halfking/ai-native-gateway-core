#!/usr/bin/env bash
# §143.35 / P11 的端到端验证：在本地真库上跑【巡检脚本本身】，
# 分别强制走「旧路」与「新路」，比对两者的判定结论是否逐字相同。
#
# ★★★ 这是「零语义风险」的**端到端**证据：
#   前面 verify_892_patrol_ab.sh 证明的是「两段 SQL 逻辑等价」；
#   本脚本证明的是「**脚本实际生成并执行的那段 SQL**，在两条路下判定一致」。
#   ⇒ ★ 因为脚本会拼 SQL（变量替换 + 引号），「逻辑等价」不等于「生成的 SQL 等价」。
#
# 用法：bash scripts/audit/verify_892_patrol_e2e.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
DBUSER="${PG_USER:-llm_gateway}"
DB="itgate_e2e892"
SCRIPT="$REPO_ROOT/scripts/252-monitor/ursm-snapshot-health.sh"

TMP="$(mktemp /tmp/verify_892e2e_XXXXXX.sql)"

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

# ── 1) 造库 + 造数 + 应用迁移 ───────────────────────────────────────────
cat > "$TMP" <<'SQL'
DROP TABLE IF EXISTS public.ursm_snapshot_slot_daily;
DROP FUNCTION IF EXISTS public.refresh_ursm_snapshot_slot_daily(integer, text);
DROP TABLE IF EXISTS public.ursm_node_snapshot_min;
CREATE TABLE public.ursm_node_snapshot_min (
    id bigserial PRIMARY KEY,
    snapshot_ts timestamptz NOT NULL,
    node_id text,
    tenant_id text,
    payload jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO public.ursm_node_snapshot_min (snapshot_ts, node_id)
SELECT ts, 'n' || (g % 7)
  FROM generate_series((now() - interval '10 days')::timestamptz,
                       now() - interval '1 hour',
                       interval '30 minutes') AS ts,
       LATERAL generate_series(1,
         10 + ((EXTRACT(doy FROM ts)::int * 7 + EXTRACT(hour FROM ts)::int * 3) % 80)) AS g;
SQL

python3 - "$TMP" "$REPO_ROOT/sql/migrations/startup/892_ursm_snapshot_slot_daily.sql" <<'PY'
import sys
out, up = sys.argv[1], sys.argv[2]
with open(out, "a", encoding="utf-8") as fh:
    fh.write(open(up, encoding="utf-8").read())
PY

echo "== 重建一次性库 $DB =="
# ★ 删旧库走【同一个】cleanup（★ 避免两份 DROP 实现漂移 ⇒ 也顺带拿到防卡保护）
cleanup
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d postgres -c "CREATE DATABASE $DB;" >/dev/null || exit 2
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d "$DB" -q -f /dev/stdin < "$TMP" >/dev/null || exit 2

# ── 2) 让脚本在真库上跑两次：旧路 / 新路 ─────────────────────────────────
# ★ 脚本用 PSQL_CMD 注入 psql 形态（与 252 的 podman 形态不同 ⇒ 用 docker）。
export PSQL_CMD="docker exec -i $CONTAINER psql -U $DBUSER -d $DB -tAX -c"
export CONTAINER PG_USER="$DBUSER" PG_DB="$DB"

echo "== 跑【旧路】=（汇总表改名 ⇒ 探测失败 ⇒ 自动回退） =="
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d "$DB" -q \
  -c "ALTER TABLE public.ursm_snapshot_slot_daily RENAME TO ursm_snapshot_slot_daily_hidden;" >/dev/null
bash "$SCRIPT" > /tmp/e2e_old.out 2>&1
old_rc=$?
docker exec -i "$CONTAINER" psql -U "$DBUSER" -d "$DB" -q \
  -c "ALTER TABLE public.ursm_snapshot_slot_daily_hidden RENAME TO ursm_snapshot_slot_daily;" >/dev/null
grep -E "base_source=" /tmp/e2e_old.out | head -2
echo "old exit=$old_rc"

echo "== 跑【新路】=（汇总表在位 ⇒ 探测通过） =="
bash "$SCRIPT" > /tmp/e2e_new.out 2>&1
new_rc=$?
grep -E "base_source=" /tmp/e2e_new.out | head -2
echo "new exit=$new_rc"

# ── 3) 比对两路的判定结论 ────────────────────────────────────────────────
echo "== 比对判定结论 =="
python3 - <<'PY'
import re, sys

def verdicts(path):
    """抽取每个桶的判定行（'  时间  行数  基线  活跃  判定' 表格的最后两列）。"""
    out = []
    started = False
    for ln in open(path, encoding="utf-8", errors="replace"):
        if "异常桶明细" in ln or "时间" in ln and "行数" in ln:
            started = True
            continue
        if started:
            m = re.match(r"\s{2}(\S+\s+\S+)\s+(\d+)\s+(\S+)\s+(\S+)\s+(\S+)\s*$", ln)
            if m:
                out.append((m.group(1).strip(), m.group(2), m.group(3), m.group(4), m.group(5)))
    return out

old = verdicts("/tmp/e2e_old.out")
new = verdicts("/tmp/e2e_new.out")
print("old verdict rows:", len(old))
print("new verdict rows:", len(new))
if old == new:
    print("E2E_VERDICT: PASS (两路判定逐字相同)")
    sys.exit(0)
print("E2E_VERDICT: FAIL")
for a, b in zip(old, new):
    if a != b:
        print("  old:", a)
        print("  new:", b)
sys.exit(1)
PY
rc=$?

# ── 4) 也比对主判据的三态计数 ────────────────────────────────────────────
echo "== 比对主判据 =="
for f in /tmp/e2e_old.out /tmp/e2e_new.out; do
  echo "-- $f"
  grep -E "base_source=|控制|control|baseline span" "$f" | head -4
done

echo "== 清理一次性库 =="
# ★ 正常跑完也走同一个 cleanup（★ EXIT trap 还会再跑一次 ⇒ DROP IF EXISTS ⇒ 无害）
cleanup
exit $rc
