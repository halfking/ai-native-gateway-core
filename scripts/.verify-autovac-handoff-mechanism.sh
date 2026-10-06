#!/usr/bin/env bash
# §10.93.5 候选项「把当月堆分区交回 autovacuum」的**机制验证**。
#
# 背景：§10.93.5 原判「无法只读验证，理由是接手与否只有上线后才观察得到」。
# 这不成立 —— 该改法依赖的是**纯 PostgreSQL 机制**：
#   「手工 pass 每小时 ANALYZE ⇒ 清零 n_mod_since_analyze ⇒ autoanalyze 永不触发；
#     一旦不再手工 ANALYZE ⇒ 计数器累积 ⇒ 越过阈值后 autoanalyze 触发。」
# 这条机制可以在一次性容器里直接跑出来，不必上线。
#
# A/B 两组只差一件事：sim_handpass 每 1 秒被手工 ANALYZE 一次
#   （模拟生产「每小时一次」），sim_autovac 完全不���手工 ANALYZE。
# 时间按生产比例压缩：
#   生产  触发线 2,402 行 ÷ 794 行/小时 = 3.0 小时，手工 pass 周期 1 小时（1h < 3.0h）
#   本实验 触发线    12 行 ÷     5 行/秒   = 2.4 秒，手工 pass 周期 1 秒（1s < 2.4s）
# 两边都是「手工周期短于触发时间」，与生产同构。
#
# ★ 余量要留够：第一版把插入调到 5 行/秒（触发 2.4s vs 手工 1s，余量仅 2.4 倍），
#   跑两次得到不同结果——第二次 A 组被 autoanalyze 了 1 次。
#   根因是每轮 `docker exec` 的实际开销把 1 秒周期拉长到 1.2~1.5 秒，
#   偶尔就冲过了触发线。⇒ **手搓 A/B 的余量必须比生产宽**，
#   否则得到的是 flaky 结论而不是结论。
#
# ★ 判定必须双向，不能只判一边：
#   B 触发 ⇒ 证明「不手工 ANALYZE 就真的会触发」（候选项成立的前提）
#   A 不触发 ⇒ 证明「差异确实由手工 pass 造成」（排除夹具/环境解释）
# 只判 A 不触发是恒真的：A 里如果 autovacuum 根本没被调起来，A 也不会触发。
set -euo pipefail
cd "$(dirname "$0")/.."

CONTAINER="autovac-pg"
PORT=15498
IMAGE="${PG_TEST_IMAGE:-postgres:16-alpine}"
PW="autovac_local_only"
FAIL=0

fail() { echo "  FAIL $*" >&2; FAIL=1; }

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== 起一次性容器（autovacuum_naptime 压到 2s 以缩短实验） =="
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
# ★ -c 必须放在镜像**之后**：docker 自己把 -c 当成 --cpu-shares。
docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=t -p "127.0.0.1:$PORT:5432" \
  "$IMAGE" \
  -c autovacuum_naptime=2s \
  -c autovacuum_analyze_threshold=10 \
  -c autovacuum_analyze_scale_factor=0.02 \
  -c autovacuum_vacuum_threshold=10 \
  -c autovacuum_vacuum_scale_factor=0.02 >/dev/null
for i in $(seq 1 30); do
  docker exec "$CONTAINER" pg_isready -U postgres -d t >/dev/null 2>&1 && break
  [ "$i" -eq 30 ] && { echo "容器未就绪"; exit 1; }
  sleep 1
done

q() { docker exec -i "$CONTAINER" psql -U postgres -d t -X -t -A -F'|' -v ON_ERROR_STOP=1 -c "$1"; }

echo "== 建夹具（两张同形表，100 行 ⇒ 触发线 = 10 + 0.02*100 = 12） =="
q "CREATE TABLE sim_handpass (id INT PRIMARY KEY, pad TEXT);
   CREATE TABLE sim_autovac  (id INT PRIMARY KEY, pad TEXT);
   INSERT INTO sim_handpass SELECT g, repeat('x',40) FROM generate_series(1,100) g;
   INSERT INTO sim_autovac  SELECT g, repeat('x',40) FROM generate_series(1,100) g;
   -- 先各 ANALYZE 一次，让 reltuples 准确（生产里手工 pass 已经分析过）
   ANALYZE sim_handpass; ANALYZE sim_autovac;" >/dev/null

# 归零：只保留「改动计数」，从 ANALYZE 之后开始计
q "SELECT 'setup_ok', (SELECT count(*) FROM sim_handpass), (SELECT count(*) FROM sim_autovac);" | sed 's/^/  /'

echo "== 跑 60 秒：持续插入（2.5 行/秒），sim_handpass 每秒被手工 ANALYZE 一次 =="
SECONDS_TO_RUN=60
(
  # 插入器：每 0.2 秒一行 ⇒ 5 行/秒，两张表同步插
  end=$(( $(date +%s) + SECONDS_TO_RUN ))
  i=101
  while [ "$(date +%s)" -lt "$end" ]; do
    docker exec "$CONTAINER" psql -U postgres -d t -X -q -v ON_ERROR_STOP=1 \
      -c "INSERT INTO sim_handpass VALUES ($i,'y');" >/dev/null
    docker exec "$CONTAINER" psql -U postgres -d t -X -q -v ON_ERROR_STOP=1 \
      -c "INSERT INTO sim_autovac VALUES ($i,'y');" >/dev/null
    i=$((i+1))
    sleep 0.4
  done
) &
INS=$!

# 手工 pass：每 1 秒 ANALYZE sim_handpass 一次（这是唯一差异）
(
  end=$(( $(date +%s) + SECONDS_TO_RUN ))
  while [ "$(date +%s)" -lt "$end" ]; do
    docker exec "$CONTAINER" psql -U postgres -d t -X -q \
      -c "ANALYZE sim_handpass;" >/dev/null 2>&1 || true
    sleep 1
  done
) &
PASS=$!

wait $INS || true
wait $PASS || true

# 插入器退出后还要给 autovacuum 一点时间跨过 naptime
sleep 8

echo
echo "== 读数 =="
q "SELECT relname,
          n_live_tup,
          autoanalyze_count,
          coalesce(to_char(last_autoanalyze,'HH24:MI:SS'),'never') AS last_auto,
          n_mod_since_analyze
   FROM pg_stat_user_tables
   WHERE relname IN ('sim_handpass','sim_autovac')
   ORDER BY relname;" | sed 's/^/  /'

ROWS_A=$(q "SELECT n_live_tup FROM pg_stat_user_tables WHERE relname='sim_handpass'" | tr -d ' ')
ROWS_B=$(q "SELECT n_live_tup FROM pg_stat_user_tables WHERE relname='sim_autovac'" | tr -d ' ')

# ★ 致命前置：两张表都必须真的长出行。第一版脚本里 A 组的 INSERT 尾部多了个逗号
#   （语法错误），又被 `|| true` 吞掉 ⇒ A 组全程零写入，autoanalyze_count=0
#   证明不了任何事 —— 那是**恒真**，不是结论。零结果必须先怀疑量具。
for pair in "sim_handpass:${ROWS_A:-空}" "sim_autovac:${ROWS_B:-空}"; do
  name="${pair%%:*}"; n="${pair##*:}"
  echo "  $name 最终行数 = $n"
  if [ -z "$n" ]; then
    fail "$name 的行数读数为空 —— 量具坏了"
  elif [ "$n" -le 100 ]; then
    fail "$name 行数只有 $n（起点 100）⇒ 写入根本没发生，A/B 实验不成立，拒绝下结论"
  fi
done

A=$(q "SELECT autoanalyze_count FROM pg_stat_user_tables WHERE relname='sim_handpass'" | tr -d ' ')
B=$(q "SELECT autoanalyze_count FROM pg_stat_user_tables WHERE relname='sim_autovac'" | tr -d ' ')

echo
echo "  sim_handpass（被手工 pass 每秒 ANALYZE）autoanalyze_count = ${A:-空}"
echo "  sim_autovac （完全交回 autovacuum）  autoanalyze_count = ${B:-空}"

# ★ 两个方向都要有读数，空值不得当成 0
[ -z "${A:-}" ] && fail "sim_handpass 的 autoanalyze_count 读数为空 —— 量具坏了，不能比较"
[ -z "${B:-}" ] && fail "sim_autovac 的 autoanalyze_count 读数为空 —— 量具坏了，不能比较"

if [ "${A:-0}" -gt 0 ]; then
  fail "sim_handpass 竟然被 autoanalyze 了 ${A} 次 ⇒ 实验没能复现「手工 pass 清零计数器」这个前提"
fi
if [ "${B:-0}" -le 0 ]; then
  fail "sim_autovac 没有触发 autoanalyze ⇒ 候选项的前提（不手工 ANALYZE 就会触发）**不成立**"
fi

echo
if [ "$FAIL" -eq 0 ]; then
  echo "==== 机制成立：交回 autovacuum 后确实会触发，且触发完全由「是否手工 ANALYZE」决定 ===="
else
  echo "==== 机制未成立，候选项的前提被证伪 ===="
fi
exit "$FAIL"