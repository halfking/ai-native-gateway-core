#!/usr/bin/env bash
# 迁移 838 的**行为**验证（不是文本断言）。
#
# 为什么需要它：sql/migrations/startup/migration_838_test.go 只能证明
# 「文件里有那一行守卫」，证明不了这段 plpgsql 真能跑、真按预期跳过。
# 一次语法错误要等到部署时才炸，那时已经在生产上了。
#
# 覆盖四个场景：
#   A 守卫确实装进了函数体
#   B 首次调用：四个关系全是「从未分析过」⇒ 必须全部被分析（首次覆盖保证）
#   C 第二次调用：上月分区被跳过（时间戳不动），当月与 hot 仍被分析
#   D 负控：换回 down 的函数体后，上月分区**必须**被重新分析
#     ⇒ 证明差异来自 838 的守卫，而不是夹具或环境
#
# ⚠ 每次调用必须在**独立事务/独立会话**里跑。
#   实测：在同一个 DO 块（= 单个事务）里连调两次，第二次虽然返回 3，
#   但 pg_stat_get_last_analyze_time 完全不反映这次 ANALYZE
#   ⇒ 在单事务里断言会得到「跳过了全部四个」的假象。
#
# 用完即弃的本地容器，与任何生产无关。不碰 154/245/252。
set -uo pipefail
cd "$(dirname "$0")/.."

CONTAINER="mv838-pg"
IMAGE="${PG_TEST_IMAGE:-postgres:16-alpine}"
UP="sql/migrations/startup/838_analyze_skip_frozen_month.sql"
DOWN="sql/migrations/startup/838_analyze_skip_frozen_month.down.sql"

psql_in() { docker exec -i "$CONTAINER" psql -U postgres -d t -X -q -v ON_ERROR_STOP=1 "$@"; }
# 调用一次函数并回显它返回的关系数（独立会话 ⇒ 独立事务）
call_fn() { psql_in -t -A -c 'SELECT analyze_llm_gateway_table_stats(2)'; }
# 快照：关系名|last_analyze 的 epoch 秒（独立事务 ⇒ 看得见上一次调用的效果）
snapshot() {
  psql_in -t -A -F'|' -c "
    SELECT c.relname, COALESCE(extract(epoch FROM pg_stat_get_last_analyze_time(c.oid))::text, 'NEVER')
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public'
      AND (c.relname LIKE '%\_hot' ESCAPE '\\'
           OR c.relname ~ ('_(' || to_char(date_trunc('month', now()), 'YYYY_MM')
                           || '|' || to_char(date_trunc('month', now()) - interval '1 month', 'YYYY_MM') || ')$'))
    ORDER BY c.relname;"
}
# ⚠ 空快照会让后面的「两边相等 ⇒ 通过」变成恒真（实测踩过：快照 SQL 自己报正则错误，
#   两次都拿到空串，断言照样绿）。所以比较前先验行数。
require_snapshot() {
  local snap="$1" where="$2"
  local lines
  lines="$(printf '%s\n' "$snap" | grep -c '|' || true)"
  [ "$lines" -eq 4 ] || fail "$where：快照只有 $lines 行（期望 4），后面的比较会是恒真 —— 拒绝比较。原始输出：[$snap]"
}
prev_rel_name() { psql_in -t -A -c "SELECT 'request_logs_' || to_char(date_trunc('month', now()) - interval '1 month', 'YYYY_MM');"; }
stamp_of() { printf '%s\n' "$1" | awk -F'|' -v k="$2" '$1==k {print $2}'; }

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1; }
fail() { echo "FAIL $1"; exit 1; }
trap cleanup EXIT

echo "### 1/5 拉起一次性容器 $IMAGE"
cleanup
docker run -d --name "$CONTAINER" -e POSTGRES_PASSWORD=x -e POSTGRES_DB=t "$IMAGE" >/dev/null || fail "容器起不来"
# ⚠ 只用 pg_isready 会连上正在关停的旧实例（实测报 shutting down）；
#   必须真跑一条 SELECT 才算就绪。
ready=0
for _ in $(seq 1 60); do
  if docker exec "$CONTAINER" psql -U postgres -d t -X -q -c 'SELECT 1' >/dev/null 2>&1; then
    ready=1; break
  fi
  sleep 1
done
[ "$ready" -eq 1 ] || fail "容器 60 秒内未就绪"

echo "### 2/5 建夹具 + 应用 838（语法错在这里就该炸）"
psql_in <<'SQL'
CREATE TABLE request_logs_2026_10(id int, payload text);
CREATE TABLE request_logs_2026_09(id int, payload text);
CREATE TABLE request_wal_2026_10(id int, payload text);
CREATE TABLE demo_hot(id int, payload text);
INSERT INTO request_logs_2026_10 SELECT g, repeat('x',50) FROM generate_series(1,500) g;
INSERT INTO request_logs_2026_09 SELECT g, repeat('x',50) FROM generate_series(1,500) g;
INSERT INTO request_wal_2026_10 SELECT g, repeat('x',50) FROM generate_series(1,500) g;
INSERT INTO demo_hot          SELECT g, repeat('x',50) FROM generate_series(1,500) g;
SQL
psql_in -f - < "$UP" || fail "838 应用失败（语法/定义错误）"
psql_in -t -A -c "SELECT CASE WHEN pg_get_functiondef(oid) LIKE '%m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid)%'
  THEN 'INSTALLED' ELSE 'MISSING' END FROM pg_proc WHERE proname='analyze_llm_gateway_table_stats'" \
  | grep -q INSTALLED || fail "A 装进库的函数体里没有 838 的守卫"
echo "    A 通过：守卫已装入函数体"

PREV="$(prev_rel_name)"
echo "### 3/5 首次调用（四个关系全是从未分析过 ⇒ 必须全部补齐）"
n1="$(call_fn)"; echo "    call1 返回 $n1 个关系（上月分区 = ${PREV}）"
snapA="$(snapshot)"; require_snapshot "$snapA" "首次调用后"
echo "$snapA" | sed 's/^/      /'
[ "$n1" = "4" ] || fail "B 首次调用只分析了 $n1 个，期望 4（首次覆盖保证被破坏）"
[ "$(stamp_of "$snapA" "$PREV")" = "NEVER" ] && fail "B 首次调用后上月分区仍无统计"
echo "    B 通过：首次覆盖完整（上月分区拿到了统计）"

echo "### 4/5 稳态调用（跨事务；上月应跳过）"
sleep 1.2
n2="$(call_fn)"; echo "    call2 返回 $n2 个关系"
snapB="$(snapshot)"; require_snapshot "$snapB" "第二次调用后"
echo "$snapB" | sed 's/^/      /'
[ "$n2" = "3" ] || fail "C 第二次调用分析了 $n2 个，期望 3（hot + 当月两张）"
[ "$(stamp_of "$snapB" "$PREV")" = "$(stamp_of "$snapA" "$PREV")" ] \
  || fail "C 上月分区 $PREV 被重新分析了（守卫没生效）"
for r in $(printf '%s\n' "$snapB" | awk -F'|' -v p="$PREV" '$1!=p {print $1}'); do
  [ "$(stamp_of "$snapB" "$r")" != "$(stamp_of "$snapA" "$r")" ] \
    || fail "C $r 没有被重新分析（守卫过宽，当月/热表必须照常分析）"
done
echo "    C 通过：上月跳过、当月与热表照常"

echo "### 5/5 负控：换回 down 的函数体，上月必须被重新分析"
psql_in -f - < "$DOWN" || fail "down 应用失败"
psql_in -t -A -c "SELECT CASE WHEN pg_get_functiondef(oid) LIKE '%m = 0 OR NOT EXISTS%'
  THEN 'STILL' ELSE 'GONE' END FROM pg_proc WHERE proname='analyze_llm_gateway_table_stats'" \
  | grep -q GONE || fail "负控：down 没把守卫撤掉"
call_fn >/dev/null; snapC="$(snapshot)"; require_snapshot "$snapC" "负控基线"
sleep 1.2
n3="$(call_fn)"; snapD="$(snapshot)"; require_snapshot "$snapD" "负控第二次调用后"
[ "$n3" = "4" ] || fail "负控：down 版只分析了 $n3 个，期望 4"
[ "$(stamp_of "$snapD" "$PREV")" != "$(stamp_of "$snapC" "$PREV")" ] \
  || fail "负控：down 版没有重新分析上月分区 ⇒ 差异不是 838 守卫带来的"
echo "    D 通过：down 版四次全分析 ⇒ 差异确由 838 守卫造成"

echo "=================================================="
echo "迁移 838 行为验证：A/B/C + 负控 D 全部通过（一次性容器，与生产无关）"
exit 0