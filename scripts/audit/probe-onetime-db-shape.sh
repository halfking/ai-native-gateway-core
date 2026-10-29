#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
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
#   DB_NAME       override the database name (default itgate_r59_shape)
#
# ★ 2026-10-21（R68 轮）：加 DB_NAME 覆盖的原因 ——
#   库名 `itgate_r59_shape` 是**跨会话共享**的：脚本开头就
#   `DROP DATABASE IF EXISTS $DB` ⇒ **两个会话同时跑就会互删**。
#   （本轮实测：并行会话跑了一次 probe ⇒ 我的一次性库被整个 DROP，
#     后续读数全部变成 "database does not exist" ⇒ 读数无意义。）
#   ⇒ 并行工作请用**自己的库名**（DB_NAME=itgate_r68_xxx）。
# -----------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
DB="${DB_NAME:-itgate_r59_shape}"
KEEP_DB="${KEEP_DB:-0}"
SF_DIR="$REPO_ROOT/installer/cmd/llm-gw-installer/embeddata/startup"

psql_() { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" -q -A -t "$@"; }

# ★★★ 回收必须兜住【所有】退出路径（R94 轮实测修）。
#   事实：本脚本原本把 DROP 放在**尾部**（KEEP_DB != 1 时）⇒ 建库之后的三条
#   `exit 2`（建库失败 / prereqs 失败 / 基线失败）**全部跳过回收** ⇒ 每次失败
#   都在容器里留一个一次性库（每个 ~1 GB）。
#   ⇒ 尤其危险的是：`DB_NAME` 是**每会话独立**的名字 ⇒ 开头的
#   「先 DROP 再 CREATE」只能自愈**同名**的库 ⇒ 别的会话的残留救不了。
#   ⇒ 修法：trap 兜底（★ Go 侧用 defer、shell 侧用 trap，是同一个机制）。
cleanup_db() {
  if [[ "$KEEP_DB" == "1" ]]; then return 0; fi
  if [[ -n "${DB:-}" ]]; then
    # ★ 防卡保护：SET / terminate / DROP 三条独立 -c
    docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
    docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "SET statement_timeout = '120s'" \
      -c "DROP DATABASE IF EXISTS $DB WITH (FORCE);" >/dev/null 2>&1 || true
  fi
}

echo "== 建一次性库 ${DB}（容器=${PG_CONTAINER}）=="

# 1) 空库
# ★ 防卡保护：SET / terminate / DROP 三条独立 -c
docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
  -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
  -c "SET statement_timeout = '120s'" \
  -c "DROP DATABASE IF EXISTS $DB WITH (FORCE);" >/dev/null 2>&1
# ★ 先注册 trap：建库过程中失败 ⇒ 也要回收
trap cleanup_db EXIT
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
#
# ★★★ 2026-10-21 R85 轮修：这一段的「形态可信」判据此前是**假绿**（四个变体）。
#
#   旧写法：`if go test … | tail -12; then echo "  ✓ ensure 链已跑（库现在 = 产品真实形态）"`
#
#   ⇒ ★★ 四个变体都会打印出那句「✓ 库现在 = 产品真实形态」：
#     (V0) 上面的「取不到容器口令/端口 ⇒ 跳过 ensure 链」分支 —— 库停在「装完但没启动」，
#          脚本却继续跑完并以 0 退出（**读数失真却没有任何人知道**）；
#     (V1) `go test` 对 **SKIP** 返回 0 ⇒ 跳过也被当成「跑过了」
#          （★ 本仓 §19.20.28 给那道门加了**诚实的 SKIP 分支** ⇒ 这条路径**更近了**）；
#     (V2) `-run` 的名字没匹配到任何测试 ⇒ go test 打印 "no tests to run" 且退出 0；
#     (V3) 第 ④ 步的 startup 链 failed/missing ⇒ 库压根不是产品形态
#          （第 ④ 步**容忍**链失败并继续往下跑）。
#
#   ⇒ ★★★ 最坏的形态：**量具只在「已经坏掉」的时候才说谎** ⇒ 平时看着绿，
#          一旦链出问题（正是你最需要它的时候）它反而报「形态可信」。
#   ⇒ 修法：**只有「目标测试真的 PASS 且 startup 链干净」才认形态可信**；
#     其余一律显式说清「不可信」，并让脚本以 **3** 退出（3 = 量具坏了，
#     与 db-package-isolation-check.sh 的退出码契约一致）。
_shape_trusted=0
_pg_pw="$(docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null | head -1)"
_pg_port="$(docker port "$PG_CONTAINER" 5432/tcp 2>/dev/null | head -1 | sed 's/.*://')"
if [[ -z "$_pg_pw" || -z "$_pg_port" ]]; then
  echo "  ⚠ 取不到容器口令/端口（POSTGRES_PASSWORD / docker port）⇒ 跳过 ensure 链。"
  echo "    ⇒ 后果：库停留在「装完但没启动」形态 ⇒ 拿它量列差集会得到**失真**读数。"
  echo "    ⇒ ★ 本次读数**不可信**（脚本将以 3 退出）。"
else
  export TEST_DATABASE_URL="postgres://${PG_USER}:${_pg_pw}@127.0.0.1:${_pg_port}/${DB}?sslmode=disable"
  # ★ 捕获**真实**结果：退出码 + 「目标测试到底跑了没有 / 是不是 SKIP」。
  #   ⚠ 不能只看退出码：go test 对 SKIP 返回 0；名字没匹配上任何测试也返回 0。
  _out="$(go test ./db/ -run TestFreshInstallRuntimeShape_EnsureChain -count=1 -v 2>&1)"
  _rc=$?
  printf '%s\n' "$_out" | tail -12
  _verdict="none"
  if grep -q -- '--- PASS: TestFreshInstallRuntimeShape_EnsureChain' <<<"$_out"; then
    _verdict="pass"
  elif grep -q -- '--- SKIP: TestFreshInstallRuntimeShape_EnsureChain' <<<"$_out"; then
    _verdict="skip"
  elif grep -q -- '--- FAIL: TestFreshInstallRuntimeShape_EnsureChain' <<<"$_out"; then
    _verdict="fail"
  elif grep -q -- 'no tests to run' <<<"$_out"; then
    _verdict="nomatch"
  fi
  case "$_verdict" in
    pass)
      if [[ "$fail" -gt 0 || "$missing" -gt 0 ]]; then
        echo "  ⚠ ensure 链 PASS，但第 ④ 步的 startup 链有失败（failed=${fail} missing=${missing}）"
        echo "    ⇒ 库**未必**是产品真实形态 ⇒ 本次读数**不可信**（脚本将以 3 退出）。"
      else
        echo "  ✓ ensure 链已跑（库现在 = 产品真实形态）"
        _shape_trusted=1
      fi ;;
    skip)
      echo "  ⚠ ensure 链**未真正执行**（SKIP）⇒ 库形态不可信（见上面输出）"
      echo "    ⇒ ★ 常见成因：目标库不是「全新安装形态」（缺 session_turns_hot 且同源 promote 函数也不在）"
      echo "    ⇒   或 startup 链没跑成 ⇒ **别拿这次的读数当「装完且启动过」的形态**。" ;;
    nomatch)
      echo "  ✗ 目标测试名没匹配到任何测试（no tests to run）⇒ 第 ⑥ 步什么都没跑 ⇒ 库形态不可信"
      echo "    ⇒ ★ 多半是测试被改名/删除 ⇒ 查 db/db_fresh_install_runtime_shape_test.go。" ;;
    *)
      echo "  ✗ ensure 链未通过（rc=${_rc}）⇒ 库形态不可信（见上面输出）" ;;
  esac
  unset TEST_DATABASE_URL
fi

if [[ "$KEEP_DB" != "1" ]]; then
  cleanup_db
  echo
  echo "== 已 DROP $DB =="
fi
# ★ 正常跑完 ⇒ 交回 trap（避免退出时重复 DROP）
trap - EXIT

# ★★★ 退出码契约（R85 轮加）：
#   0 = 形态可信（= 产品真实形态，「装完且启动过」）
#   3 = **量具坏了**（形态不可信）⇒ 与 db-package-isolation-check.sh 的契约一致。
#   ⚠ 这一行是 R85 轮修掉的那个假绿的**唯一对外信号**：在它之前，
#     「链坏了 / 目标测试被 SKIP / 名字没匹配上」全部以 0 退出 ⇒ 调用方无从分辨。
#   ⚠ 放在 DROP 之后 ⇒ 库照样回收（不改成「不可信就留库」，避免共享工作树里堆一次性库）；
#     要留库现场看请用 KEEP_DB=1 重跑。
if [[ "$_shape_trusted" != "1" ]]; then
  echo
  echo "== ★ 本次形态读数**不可信**（退出码 3 = 量具坏了）=="
  exit 3
fi
