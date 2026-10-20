#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/probe-onetime-db-shape.sh
# Purpose:       Build ONE disposable database exactly the way the integration
#                gate does (prereqs -> 01-schema baseline -> registered startup
#                migrations, applied ONCE) and then print the *shape* facts that
#                the real-database failure triage needs.
#
# WHY THIS EXISTS (2026-10-21, R59 轮)
#   R57 measured 66 distinct failing test names on the real-database legs and
#   registered them as an untriaged leftover. The triage rule that decides
#   "product defect vs fixture shape" needs facts about the disposable database
#   that the failure messages do not carry:
#
#     * does the hot family carry the columns the settle worker reads
#       (rl.origin_stage / rl.quality_flags)?
#     * does the canonical view still expose the late-added columns
#       (request_class / origin_stage / quality_flags)?
#     * which monthly partitions actually exist (the "no partition found for
#       row" failures point at the current month)?
#     * do the relations the outbox / session-mirror legs read exist?
#
#   Those are measurements, not opinions: this script prints them.
#
# Usage:
#   bash scripts/audit/probe-onetime-db-shape.sh            # build, probe, DROP
#   KEEP_DB=1 bash scripts/audit/probe-onetime-db-shape.sh   # keep it for poking
#
# Env:
#   PG_CONTAINER  container (default: llm-gateway-pg)
#   PG_USER       database user (default: llm_gateway)
#   KEEP_DB       1 = do not drop the database at the end
# -----------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
DB="itgate_r59_shape"
KEEP_DB="${KEEP_DB:-0}"
SF_DIR="$REPO_ROOT/installer/cmd/llm-gw-installer/embeddata/startup"

psql_() { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" -q -A -t "$@"; }

echo "== 建一次性库 ${DB}（容器=${PG_CONTAINER}）=="

# 1) 空库
docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q -c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
if ! docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q -c "CREATE DATABASE $DB" >/dev/null 2>/tmp/r59-create.err; then
  echo "  ✗ CREATE DATABASE 失败:"; head -3 /tmp/r59-create.err; exit 2
fi

# 2) prereqs（citus / columnar 扩展）
cat sql/schema/00-prereqs.sql | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" \
  -q -v ON_ERROR_STOP=1 --single-transaction >/dev/null 2>/tmp/r59-prereq.err || {
  echo "  ✗ 00-prereqs.sql 失败:"; head -4 /tmp/r59-prereq.err; exit 2; }
echo "  ✓ prereqs"

# 3) 基线
cat sql/schema/01-schema.sql | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" \
  -q -v ON_ERROR_STOP=1 --single-transaction >/dev/null 2>/tmp/r59-baseline.err || {
  echo "  ✗ 01-schema.sql 失败:"; head -4 /tmp/r59-baseline.err; exit 2; }
echo "  ✓ baseline"

# 4) 注册的启动迁移（按 runner.go 的 StartupFiles 列表，遵守 no-transaction 标记）
ok=0; fail=0; missing=0
: > /tmp/r59-chain-fail.txt
while read -r f; do
  [[ -n "$f" ]] || continue
  p="$SF_DIR/$f"
  if [[ ! -f "$p" ]]; then missing=$((missing+1)); continue; fi
  args=(-q -v ON_ERROR_STOP=1)
  grep -qE '^[[:space:]]*--.*dbinit:no-transaction' "$p" || args+=(--single-transaction)
  if docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" "${args[@]}" < "$p" >/dev/null 2>/tmp/r59-sf.err; then
    ok=$((ok+1))
  else
    fail=$((fail+1))
    echo "$f :: $(grep -i '^ERROR' /tmp/r59-sf.err | head -1 | cut -c1-140)" >> /tmp/r59-chain-fail.txt
  fi
done < <(sed -n '/StartupFiles: \[\]string{/,/^\t}/p' \
           "$REPO_ROOT/installer/internal/dbinit/runner.go" \
         | grep -oE '"[0-9a-zA-Z_]+\.sql"' | tr -d '"')
echo "  startup: applied=$ok failed=$fail missing=$missing"
if [[ $fail -gt 0 ]]; then
  echo "  ── 链里失败的迁移（本次体检不因它中止）:"
  head -30 /tmp/r59-chain-fail.txt | sed 's/^/      /'
fi

echo
echo "== 体检读数 =="

echo "-- ① 热族表的列（settle worker 读的就是它）"
for t in _request_logs_hot request_logs_hot session_turns_hot; do
  if [[ "$(psql_ -c "SELECT count(*) FROM pg_class WHERE relname='$t'")" == "1" ]]; then
    cols=$(psql_ -c "SELECT string_agg(a.attname,',' ORDER BY a.attname) FROM pg_attribute a WHERE a.attrelid='$t'::regclass AND a.attnum>0 AND NOT a.attisdropped")
    echo "   $t 存在；origin_stage=$(echo "$cols" | grep -qw origin_stage && echo 有 || echo ★缺) quality_flags=$(echo "$cols" | grep -qw quality_flags && echo 有 || echo ★缺) is_auto_request=$(echo "$cols" | grep -qw is_auto_request && echo 有 || echo ★缺) 列数=$(echo "$cols" | tr ',' '\n' | wc -l | tr -d ' ')"
  else
    echo "   $t ★不存在"
  fi
done

echo "-- ② canonical 视图的列"
for v in request_logs_with_current_month request_logs_bodies_with_current_month; do
  if [[ "$(psql_ -c "SELECT count(*) FROM pg_class WHERE relname='$v'")" == "1" ]]; then
    cols=$(psql_ -c "SELECT string_agg(a.attname,',' ORDER BY a.attname) FROM pg_attribute a WHERE a.attrelid='$v'::regclass AND a.attnum>0 AND NOT a.attisdropped")
    echo "   $v 存在；request_class=$(echo "$cols" | grep -qw request_class && echo 有 || echo ★缺) origin_stage=$(echo "$cols" | grep -qw origin_stage && echo 有 || echo ★缺) quality_flags=$(echo "$cols" | grep -qw quality_flags && echo 有 || echo ★缺) 列数=$(echo "$cols" | tr ',' '\n' | wc -l | tr -d ' ')"
  else
    echo "   $v ★不存在"
  fi
done

echo "-- ③ 各 RANGE 分区族的月份分区（★缺当月 ⇒ 23514）"
psql_ -c "
SELECT c.relname AS parent,
       string_agg(DISTINCT regexp_replace(d.relname,'^.*_([0-9]{4}_[0-9]{2})\$','\\1'), ',' ORDER BY regexp_replace(d.relname,'^.*_([0-9]{4}_[0-9]{2})\$','\\1')) AS months,
       count(*) AS n
FROM pg_inherits i
JOIN pg_class c ON c.oid=i.inhparent
JOIN pg_class d ON d.oid=i.inhrelid
WHERE c.relkind='p' AND d.relname ~ '_[0-9]{4}_[0-9]{2}\$'
  AND c.relname IN ('request_logs','_request_logs','request_logs_bodies','_request_logs_bodies',
                    'session_turns','session_bodies','credential_model_index','usage_ledger',
                    'routing_decision_log','sessions','request_wal')
GROUP BY 1 ORDER BY 1;" | sed 's/^/   /'

echo "-- ④ 其它腿要读的关系是否存在"
for r in outbox_events session_turns_hot node_probe_state report_snapshots models_canonical api_keys auto_route_selections; do
  n=$(psql_ -c "SELECT count(*) FROM pg_class WHERE relname='$r'")
  echo "   $r: $([[ "$n" == "0" ]] && echo ★不存在 || echo 存在)"
done

echo "-- ⑤ 今天与当月"
psql_ -c "SELECT 'today='||to_char(now(),'YYYY-MM-DD')||' month='||to_char(now(),'YYYY_MM')"

# ── ⑥ ensure 链（= 产品启动一次）────────────────────────────────────────────
#
# ★ 为什么这一步是**必须的**（2026-10-21 R61 轮实测）：
#   「装完」与「装完能用」不是一回事 —— 产品启动时的 ensure 链
#   （db.Open）会自己 ADD COLUMN 补掉一批列（models_canonical.standard_iq* /
#   credentials 三列 / provider_catalog 三列 / _request_logs.outbound_body /
#   session_turns.host,tags）。
#   ⇒ 只量「装完」会把这 20 列算成缺口（R60 轮的「23 列缺族」就是这么来的），
#     而量「装完 + 启动过」才是产品真实形态 ⇒ 差集只剩 26 个**遗留列**。
#
#   跑法：走一次 db.Open（真·启动一次），顺带把
#   db/db_fresh_install_runtime_shape_test.go 的「ensure 自愈清单」也钉一遍。
echo
echo "== ⑥ ensure 链（= 产品启动一次）=="
_pg_pw="$(docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null | head -1)"
_pg_port="$(docker port "$PG_CONTAINER" 5432/tcp 2>/dev/null | head -1 | sed 's/.*://')"
if [[ -z "$_pg_pw" || -z "$_pg_port" ]]; then
  echo "  ⚠ 取不到容器口令/端口（POSTGRES_PASSWORD / docker port）⇒ 跳过 ensure 链。"
  echo "    ⇒ 后果：库停留在「装完但没启动」形态 ⇒ 拿它量列差集会得到**失真**读数。"
else
  export TEST_DATABASE_URL="postgres://${PG_USER}:${_pg_pw}@127.0.0.1:${_pg_port}/${DB}?sslmode=disable"
  if go test ./db/ -run TestFreshInstallRuntimeShape_EnsureChain -count=1 -v 2>&1 | tail -12; then
    echo "  ✓ ensure 链已跑（库现在 = 产品真实形态）"
  else
    echo "  ✗ ensure 链未通过 ⇒ 库形态不可信（见上面输出）"
  fi
  unset TEST_DATABASE_URL
fi

if [[ "$KEEP_DB" != "1" ]]; then
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q -c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
  echo
  echo "== 已 DROP $DB =="
fi
