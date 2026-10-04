#!/usr/bin/env bash
# .asset-heartbeat-realdb.sh —— assets 心跳门控的**真库行为门**（2026-10-04）
#
# 为什么门要分成两个文件：
#   · apihub/upsert_heartbeat_contract_test.go 验的是**文本**（WHERE 存在、
#     用 IS DISTINCT FROM、两半都在、窗口是 5 分钟）
#   · 本脚本验的是**语义**（条件到底会不会触发、NULL 边界会不会漏）
# 文本门验不出「条件写了但永远不成立」，语义门跑不了本地 PG（要连 252）。
#
# ★ 纪律：SQL 从 apihub/pg_store.go **提取**，不在这里重抄。
#   重抄的那份会与源码漂移，而门一旦验的是副本，源码改了门照样绿 ——
#   同一个「我修过了不等于被守住了」的形态。
#
# ★ 全部用 CREATE TEMP TABLE：会话级、进程退出即消失，不进 pg_class 持久目录，
#   生产零残留。本脚本对生产只有只读语义。
#
# 退出码：0 = 5 个场景全过 / 1 = 有场景不符 / 3 = 量具不可用（没跑到场景）

set -uo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
GO_SRC="$REPO/apihub/pg_store.go"
HOST=${HEARTBEAT_DB_HOST:-252}
CONTAINER=${HEARTBEAT_DB_CONTAINER:-pg-252-pg17}
DBNAME=${HEARTBEAT_DB_NAME:-llm_gateway}

if [ ! -f "$GO_SRC" ]; then
  echo "ABORT: 找不到 $GO_SRC" >&2
  exit 3
fi

# --- 从 Go 源码提取 upsertAssetSQL 与心跳窗口常量 ---
read -r -d '' EXTRACT <<'PYEOF' || true
import re, sys
src = open(sys.argv[1], encoding="utf-8").read()
m = re.search(r'const upsertAssetSQL = `(.*?)`', src, re.S)
if not m:
    print("ERROR:no-such-const", file=sys.stderr); sys.exit(3)
w = re.search(r'assetHeartbeatRefreshInterval\s*=\s*(\d+)\s*\*\s*time\.Minute', src)
if not w:
    print("ERROR:no-window-const", file=sys.stderr); sys.exit(3)
sql = m.group(1)
# 表名改写成 TEMP 表名，其余（含 WHERE 条件）逐字不动
sql = sql.replace("public.assets", "assets_probe")
sys.stdout.write(sql)
sys.stdout.write("\n--WINDOW--\n")
sys.stdout.write(w.group(1))
PYEOF

EXTRACTED=$(python3 -c "$EXTRACT" "$GO_SRC" 2>&1) || {
  echo "ABORT: 无法从 Go 源码提取 SQL（$EXTRACTED）" >&2; exit 3; }
case "$EXTRACTED" in
  ERROR:*) echo "ABORT: $EXTRACTED —— 门自身失效，不是被测对象的问题" >&2; exit 3 ;;
esac

SQL=$(printf '%s' "$EXTRACTED" | sed -n '1,/^--WINDOW--$/p' | sed '$d')
WINDOW_MIN=$(printf '%s' "$EXTRACTED" | sed -n '/^--WINDOW--$/,$p' | tail -1)
if [ -z "$SQL" ] || [ -z "$WINDOW_MIN" ]; then
  echo "ABORT: 提取结果为空（SQL=${#SQL} 字节, WINDOW='$WINDOW_MIN'）" >&2; exit 3
fi

WINDOW_SEC=$(( WINDOW_MIN * 60 ))

# --- 5 个场景。每个场景的判据都用**独立快照表**做基准：
#     绝不用 min(last_seen_at) 之类从被测对象自身取基准的写法 ——
#     那样「每次都重写」也会恒等通过（结构性恒真）。
cat > /tmp/hb_probe.sql <<SQLEOF
CREATE TEMP TABLE assets_probe (
  kind text, ref_id bigint, tenant_id text, name text, owner text, team text,
  cost_center text, tags jsonb, health_state text, version text,
  registered_at timestamptz, last_seen_at timestamptz, metadata jsonb,
  PRIMARY KEY (kind, ref_id));
PREPARE up(text,bigint,text,text,text,text,text,text,text,text,text,float8) AS
$SQL;
\echo '@S1@'
EXECUTE up('llm_endpoint',1,'t1','gpt-4o',NULL,NULL,NULL,'{}','unknown','1.0','{}',$WINDOW_SEC);
CREATE TEMP TABLE snap1 AS SELECT last_seen_at FROM assets_probe WHERE ref_id=1;
\echo '@S2@'
SELECT pg_sleep(1.2);
EXECUTE up('llm_endpoint',1,'t1','gpt-4o',NULL,NULL,NULL,'{}','unknown','1.0','{}',$WINDOW_SEC);
SELECT 'S2' AS s, CASE WHEN a.last_seen_at = b.last_seen_at THEN 'PASS' ELSE 'FAIL' END AS r
FROM assets_probe a, snap1 b WHERE a.ref_id=1;
\echo '@S3@'
SELECT pg_sleep(1.2);
EXECUTE up('llm_endpoint',1,'t1','gpt-4o-mini',NULL,NULL,NULL,'{}','unknown','1.0','{}',$WINDOW_SEC);
SELECT 'S3' AS s, CASE WHEN a.last_seen_at > b.last_seen_at THEN 'PASS' ELSE 'FAIL' END AS r
FROM assets_probe a, snap1 b WHERE a.ref_id=1;
\echo '@S4@'
UPDATE assets_probe SET last_seen_at = now() - interval '10 min' WHERE ref_id=1;
CREATE TEMP TABLE snap2 AS SELECT last_seen_at FROM assets_probe WHERE ref_id=1;
SELECT pg_sleep(1.2);
EXECUTE up('llm_endpoint',1,'t1','gpt-4o-mini',NULL,NULL,NULL,'{}','unknown','1.0','{}',$WINDOW_SEC);
SELECT 'S4' AS s, CASE WHEN a.last_seen_at > b.last_seen_at THEN 'PASS' ELSE 'FAIL' END AS r
FROM assets_probe a, snap2 b WHERE a.ref_id=1;
\echo '@S5@'
EXECUTE up('llm_endpoint',2,'t1','mcp-x','alice',NULL,NULL,'{}','unknown','1.0','{}',$WINDOW_SEC);
CREATE TEMP TABLE snap3 AS SELECT last_seen_at FROM assets_probe WHERE ref_id=2;
SELECT pg_sleep(1.2);
EXECUTE up('llm_endpoint',2,'t1','mcp-x',NULL,NULL,NULL,'{}','unknown','1.0','{}',$WINDOW_SEC);
SELECT 'S5' AS s, CASE WHEN a.last_seen_at > b.last_seen_at THEN 'PASS' ELSE 'FAIL' END AS r
FROM assets_probe a, snap3 b WHERE a.ref_id=2;
SQLEOF

OUT=$(ssh -o ConnectTimeout=10 "$HOST" "docker exec -i $CONTAINER psql -U postgres -X -q -A -F'|' -v ON_ERROR_STOP=1 -d $DBNAME < /dev/stdin" < /tmp/hb_probe.sql 2>&1)
rm -f /tmp/hb_probe.sql
rc=$?

# 只取 S2..S5 的判定行；@S1@ 之后才有判定，S1 本身靠 PREPARE 成功来验
RES=$(printf '%s\n' "$OUT" | grep -E '^(S[2-5])\|(PASS|FAIL)$' || true)
RAN=$(printf '%s\n' "$RES" | grep -c . || true)

if [ "$rc" -ne 0 ] || [ "$RAN" -eq 0 ]; then
  echo "ABORT: 真库探针不可用（rc=$rc, 判定行=$RAN）" >&2
  printf '%s\n' "$OUT" | tail -5 >&2
  exit 3
fi

FAILED=$(printf '%s\n' "$RES" | grep -c 'FAIL' || true)
printf '%s\n' "$RES"
echo "ran=$RAN failed=$FAILED window=${WINDOW_MIN}min"
[ "$FAILED" -eq 0 ] || exit 1
exit 0
