#!/usr/bin/env bash
# 迁移 839 的**行为**验证（runbook §10.101）。
#
# 为什么需要它：sql/migrations/startup/migration_839_test.go 只能证明
# 「文件里有那一行守卫」，证明不了这段 plpgsql 真能跑、真按预期**跳过**。
#
# 覆盖：
#   A 迁移能装进真库（up 与 down 都装）
#   B 当月**已分析过**的堆分区 ⇒ 必须被**跳过**（交回 autovacuum）
#   C 上月已分析过的堆分区 ⇒ 必须仍被跳过（838 的语义未被破坏）
#   D 当月**从未分析过**的分区 ⇒ 必须仍被分析（首次覆盖不能交回）
#   E 当月堆分区拿到 autovacuum_analyze_scale_factor=0.005，上月维持 0.02
#   F 负控：换回 down 的函数体后，B 那条必须**重新被分析**
#     ⇒ 证明 B 的差异确由 839 造成，而不是夹具或环境
#
# ⚠ 列存分支无法在此验证：USING columnar 是 Citus 扩展，stock postgres:16
#   装不出来，本机也无 kx-citus 镜像。该分支由文本门覆盖，
#   并在 §10.101.3 显式标注为「未行为验证」。
#
# ⚠ 每次调用与每次快照都必须跑在**独立事务**里（同 §10.94.2）：
#   单事务里连调两次，last_analyze 不反映第二次。
set -uo pipefail
cd "$(dirname "$0")/.."

CONTAINER="mv839b"
PORT=15496
IMAGE="${PG_TEST_IMAGE:-postgres:16-alpine}"
FAILED=0
fail() { echo "  FAIL $*" >&2; FAILED=1; }

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

CUR=$(date +%Y_%m)
LAST=$(date -v-1m +%Y_%m)
NEXT=$(date -v+1m +%Y-%m-01)

echo "== 起一次性容器（当月 $CUR / 上月 ${LAST}） =="
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=t -p "127.0.0.1:$PORT:5432" "$IMAGE" >/dev/null
for i in $(seq 1 30); do
  docker exec "$CONTAINER" pg_isready -U postgres -d t >/dev/null 2>&1 && break
  [ "$i" -eq 30 ] && { echo "容器未就绪"; exit 1; }
  sleep 1
done

q() { docker exec -i "$CONTAINER" psql -U postgres -d t -X -t -A -F'|' -v ON_ERROR_STOP=1 -c "$1"; }
qf() {
  local out
  if ! out=$(docker exec -i "$CONTAINER" psql -U postgres -d t -X -q \
               -v ON_ERROR_STOP=1 -f - 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "psql -f 执行失败（上面是原始报错）"
    return 1
  fi
  return 0
}

echo "== 夹具：request_logs 父表 + 当月/上月堆分区 + 一个当月从未分析过的表 =="
qf <<SQL
CREATE TABLE request_logs (id BIGINT NOT NULL, ts TIMESTAMPTZ NOT NULL) PARTITION BY RANGE (ts);
CREATE TABLE request_logs_$CUR   PARTITION OF request_logs FOR VALUES FROM ('$CUR-01') TO ('$NEXT');
CREATE TABLE request_logs_$LAST  PARTITION OF request_logs FOR VALUES FROM ('$LAST-01') TO ('$CUR-01');
-- 404 给所有月分区设的基线
ALTER TABLE request_logs_$CUR  SET (autovacuum_analyze_scale_factor = 0.02, autovacuum_analyze_threshold = 50);
ALTER TABLE request_logs_$LAST SET (autovacuum_analyze_scale_factor = 0.02, autovacuum_analyze_threshold = 50);
INSERT INTO request_logs_$CUR  SELECT g, now() FROM generate_series(1,300) g;
INSERT INTO request_logs_$LAST SELECT g, '${LAST}-15'::timestamptz FROM generate_series(1,300) g;
ANALYZE request_logs_$CUR;
ANALYZE request_logs_$LAST;
-- 当月、命中函数正则、但**从未被分析过**（pg_statistic 无行）
CREATE TABLE request_logs_bodies_$CUR (id BIGINT, b TEXT);
INSERT INTO request_logs_bodies_$CUR SELECT g, repeat('x',20) FROM generate_series(1,50) g;
SQL

# 夹具自检：任何一样缺失都拒绝往下走（否则后面的「没变化」是恒真）
FIX=$(q "SELECT (SELECT count(*) FROM pg_class WHERE relname='request_logs_${CUR}')
             + (SELECT count(*) FROM pg_class WHERE relname='request_logs_${LAST}')
             + (SELECT count(*) FROM pg_class WHERE relname='request_logs_bodies_${CUR}')")
echo "  夹具对象数（应为 3）= $FIX"
[ "${FIX:-0}" -eq 3 ] || fail "夹具不完整（${FIX:-空}/3）⇒ 拒绝下结论"

echo "== A 装迁移 839 =="
qf < sql/migrations/startup/839_autovac_current_month_heap_handoff.sql >/dev/null || fail "839 装不进去"

echo "== B/C/D 前置读数（last_analyze 必须真的非空，否则后续比较是恒真） =="
BEFORE_CUR=$(q "SELECT coalesce(to_char(pg_stat_get_last_analyze_time(c.oid),'HH24:MI:SS'),'NEVER') FROM pg_class c WHERE c.relname='request_logs_${CUR}' AND c.relkind='r'")
BEFORE_LAST=$(q "SELECT coalesce(to_char(pg_stat_get_last_analyze_time(c.oid),'HH24:MI:SS'),'NEVER') FROM pg_class c WHERE c.relname='request_logs_$LAST'")
echo "  当月分区 last_analyze = $BEFORE_CUR"
echo "  上月分区 last_analyze = $BEFORE_LAST"
[ "$BEFORE_CUR" = "NEVER" ] && fail "当月分区的 last_analyze 是 NEVER ⇒ 夹具没分析过，后续比较是恒真"
[ "$BEFORE_LAST" = "NEVER" ] && fail "上月分区的 last_analyze 是 NEVER ⇒ 夹具没分析过，后续比较是恒真"

echo "== B/C/D/E 用 839 的函数体调一趟 =="
CALL_N=$(q "SELECT analyze_llm_gateway_table_stats(2)")
echo "  analyze_llm_gateway_table_stats(2) = $CALL_N"
sleep 1
AFTER_CUR=$(q "SELECT coalesce(to_char(pg_stat_get_last_analyze_time(c.oid),'HH24:MI:SS'),'NEVER') FROM pg_class c WHERE c.relname='request_logs_$CUR'")
AFTER_LAST=$(q "SELECT coalesce(to_char(pg_stat_get_last_analyze_time(c.oid),'HH24:MI:SS'),'NEVER') FROM pg_class c WHERE c.relname='request_logs_$LAST'")
COLD_HAS=$(q "SELECT count(*) FROM pg_statistic WHERE starelid='request_logs_bodies_$CUR'::regclass")
CUR_OPTS=$(q "SELECT coalesce(array_to_string(reloptions,','),'NONE') FROM pg_class WHERE relname='request_logs_${CUR}' AND relkind='r'")
CUR_SF=$(printf '%s' "$CUR_OPTS" | grep -oE 'autovacuum_analyze_scale_factor=[0-9.]+' | cut -d= -f2)
[ -n "$CUR_SF" ] || CUR_SF=NONE
LAST_OPTS=$(q "SELECT coalesce(array_to_string(reloptions,','),'NONE') FROM pg_class WHERE relname='request_logs_${LAST}' AND relkind='r'")
LAST_SF=$(printf '%s' "$LAST_OPTS" | grep -oE 'autovacuum_analyze_scale_factor=[0-9.]+' | cut -d= -f2)
[ -n "$LAST_SF" ] || LAST_SF=NONE
echo "  当月 last_analyze: $BEFORE_CUR -> $AFTER_CUR"
echo "  上月 last_analyze: $BEFORE_LAST -> $AFTER_LAST"
echo "  当月从未分析过表的 pg_statistic 行数: $COLD_HAS"
echo "  当月 scale_factor: $CUR_SF / 上月: $LAST_SF"

[ "$AFTER_CUR" = "$BEFORE_CUR" ] \
  || fail "B：当月已分析过的堆分区**仍被分析**（$BEFORE_CUR -> ${AFTER_CUR}）⇒ 没交回 autovacuum"
[ "$AFTER_LAST" = "$BEFORE_LAST" ] \
  || fail "C：上月分区被重新分析（$BEFORE_LAST -> ${AFTER_LAST}）⇒ 838 的语义被破坏"
[ "${COLD_HAS:-0}" -gt 0 ] \
  || fail "D：从未被分析过的当月分区仍未被分析 ⇒ 首次覆盖丢失"
echo "$CUR_SF" | grep -q "0.005" \
  || fail "E：当月堆分区的 scale_factor 不是 0.005（实际 ${CUR_SF}）"
echo "$LAST_SF" | grep -q "0.02" \
  || fail "E：上月分区的 scale_factor 被误改成 ${LAST_SF}（应为 0.02）"

echo
echo "== F 负控：换回 down 的函数体后，B 那条必须重新被分析 =="
qf < sql/migrations/startup/839_autovac_current_month_heap_handoff.down.sql >/dev/null || fail "839 down 装不进去"
sleep 1
q "SELECT analyze_llm_gateway_table_stats(2)" >/dev/null
sleep 1
AFTER_DOWN=$(q "SELECT coalesce(to_char(pg_stat_get_last_analyze_time(c.oid),'HH24:MI:SS'),'NEVER') FROM pg_class c WHERE c.relname='request_logs_$CUR'")
echo "  down 后当月 last_analyze: $AFTER_CUR -> $AFTER_DOWN"
[ "$AFTER_DOWN" != "$AFTER_CUR" ] \
  || fail "F：换回 down 的函数体后当月分区仍没被分析 ⇒ 负控失效，B 的差异可能来自别的原因"

echo
if [ "$FAILED" -eq 0 ]; then
  echo "==== 839 行为验证全部通过（B/C/D/E + 负控 F） ===="
else
  echo "==== 839 行为验证存在缺口 ===="
fi
exit "$FAILED"