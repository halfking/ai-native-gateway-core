#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/measure-permanent-skip-axis.sh
# Purpose:       Measure the "permanently-SKIP axis" of the real-database gates:
#                for a set of gates, does the gate actually RUN when
#                TEST_DATABASE_URL points at a PRODUCT-SHAPE database, and does
#                it damage that database?
#
# WHY THIS EXISTS (2026-10-21, R92 轮)
#   §19.20.37 (R88 轮) published a table whose rows 1-2 claimed
#   "models_modality / pricing_import do NOT run against the product-shape DB"
#   ⇒ the table's reasoning was "the 8 fixture tables always exist there, and
#   the gate skips when they exist".
#   That reasoning measured the WRONG object: those two gates never touch the
#   product-shape DB at all. `admin/scratch_db_test.go` allocates its OWN
#   throwaway EMPTY database (`scratchEmptyPool` -> `internal/testdb.Create`)
#   and probes THAT. The shape DB's fixture tables are irrelevant to the
#   probe; the probe runs against a database where those tables do NOT exist.
#   => The R88 table is superseded: the gates DO run.
#   The peer session's R88 receipt said so; this script turns the claim into a
#   reproducible measurement.
#
#   P10 (in §19.20.37 ⑦) says: do not make this a permanent gate (it would
#   have to spin up a database on every gate run) ⇒ the agreed shape is
#   "a re-runnable measurement script + a ledger". This file is the script.
#
# Usage:
#   bash scripts/audit/measure-permanent-skip-axis.sh              # measure, print table
#   bash scripts/audit/measure-permanent-skip-axis.sh --verify     # measure + diff vs ledger
#   bash scripts/audit/measure-permanent-skip-axis.sh --dry-run-readonly
#                                                                  # ★ 只读台账 + 断言契约（★ 不碰库）
#   KEEP_DB=1 bash scripts/audit/measure-permanent-skip-axis.sh    # keep the shape DB
#   SHAPE_DB=itgate_xxx bash scripts/audit/measure-permanent-skip-axis.sh
#                                                                  # reuse an existing shape DB
#
# ★ --dry-run-readonly 的定位（2026-10-28 R86 轮新增）：
#   本脚本的**测量**需要真实库（docker + psql + 一次性形态库）⇒ ⇒「掏空法」门
#   （scripts/ursmcheck/tsv_hollowout_gate_test.go）**不能**跑它
#   ⇒ ⇒ R85 轮因此把 `permanent-skip-ledger.tsv` 登记成【例外】（理由 = 「需要真实库」）。
#   ★ 本模式把「**读台账 + 断言台账契约**」这一层单独抽出来 ⇒ **完全不碰库**
#   ⇒ ⇒ 「掏空 ⇒ 读方必须红」这条判据因此**可以在不碰库的前提下被行为化验证**。
#   ★ 诚实边界：本模式**不重跑测量** ⇒ 它证明「台账被掏空 ⇒ 台账的契约不成立 ⇒ 读方红」
#   ⇒ 而**不是**「台账被掏空 ⇒ 测量结果会与台账不符」（后者由 --verify 负责）⇒ 两者互补。
#
# Env:
#   PG_CONTAINER  container (default: llm-gateway-pg)
#   PG_USER       database user (default: llm_gateway)
#   DB_NAME       disposable shape DB name (default: itgate_permskip_shape)
#                 ★ MUST be lowercase: PostgreSQL folds unquoted identifiers.
#   SHAPE_DB      reuse an already-built shape DB (skips the build)
#   LEDGER        ledger file (default: scripts/audit/permanent-skip-ledger.tsv)
#
# ★ 对生产的写入 = 0 —— 本脚本只连**本地一次性形态库**及其派生的一次性库。
#   It never connects to a production instance.
# -----------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)" || exit 2
cd "$REPO_ROOT" || exit 2

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-itgate_permskip_shape}"
LEDGER="${LEDGER:-scripts/audit/permanent-skip-ledger.tsv}"
KEEP_DB="${KEEP_DB:-0}"
VERIFY=0
DRY_RUN_READONLY=0
case "${1:-}" in
  --verify) VERIFY=1 ;;
  --dry-run-readonly) DRY_RUN_READONLY=1 ;;
esac

# ---------------------------------------------------------------- 量具纪律 0
# ★ 本脚本自己踩过一次坑，记在这里免得下一个人再踩：
#   ① `pg_dump --schema-only` 的输出**每次都不一样** —— PG 17.6+ 给每次 dump
#      加一对随机 `\restrict <token>` / `\unrestrict <token>` 行
#      ⇒ `pg_dump | md5` **不是**结构指纹（实测：同一库连取 3 次 ⇒ 3 个不同 md5，
#        diff 只有那 8 行）⇒ ⇒ 仓里 `scripts/_lib/db-init-lib.sh` 的
#        `db_init::dump_schema` 早就显式剥掉了这两行 —— **用 pg_dump 产物做指纹
#        前，先看仓里现成的剥离逻辑**。
#   ② `pg_class.relkind` 的类型是 `"char"`（不是 `char`）⇒ `c.relkind || ':'`
#      会报 `operator does not exist: character || unknown`
#      ⇒ 若 psql 的 stderr 被 `2>/dev/null` 吞掉，stdout 就是**空**
#      ⇒ ⇒ **空输出看起来像「0 个对象」** ⇒ 必须 `c.relkind::text`。
#   ③ 凡从 psql 读值，一律滤掉 `^(WARNING|HINT|NOTICE|INFO|DETAIL)` ——
#      citus 容器把 `WARNING: could not start maintenance background worker`
#      打在 stdout 上。
#   ④ 量具自检必须跑在读数之前，失败即退出。
# ---------------------------------------------------------------- 量具纪律 0

say() { echo "$*"; }
die() { echo "!! $*" >&2; exit 2; }

# read_val <dsn> <sql>  —— 读一个值，滤噪声行；空输出 = 读失败（不是「值为空」）
read_val() {
  local dsn="$1" sql="$2" out
  out="$(psql "$dsn" -q -A -t -c "$sql" 2>/dev/null | grep -v -E '^(WARNING|HINT|NOTICE|INFO|DETAIL|ERROR:|FATAL:)' | head -1)"
  printf '%s' "$out"
}

# ---------------------------------------------------------------- --dry-run-readonly
# ★ 只读台账 + 断言台账契约 ⇒ ★ **完全不碰库**（不建库 / 不连 psql / 不 docker exec）
#   ⇒ ⇒ 「掏空法」门可以把它当作 `permanent-skip-ledger.tsv` 的**行为化读方**
#   ⇒ ⇒ 例外表里那条「需要真实库」的例外因此被降级为「已验证」。
#
# ★ 断言的是【台账契约】——★ 那正是本脚本每次测量跑完会写出来的那份形状：
#   ① 台账存在；
#   ② 数据行 ≥ 1（★ 掏空 ⇒ 这里红）；
#   ③ 每行 5 列（gate / file / probe_target / verdict / reason）；
#   ④ verdict ∈ {PASS, FAIL, SKIP}（★ 写进台账的必须是实测判定）；
#   ⑤ gate 列非空且**唯一**（★ 一条门一行 ⇒ 不能重复登记）；
#   ⑥ file 列指向仓内**真实存在**的文件（★ 台账不能指向不存在的测试文件）。
#
# ★★ 诚实边界（★ 与 `--verify` 的分工）：
#   ★ 本模式**不重跑测量** ⇒ 它证明的是「台账被掏空 ⇒ 台账契约不成立 ⇒ 读方红」
#   ⇒ 而**不是**「台账被掏空 ⇒ 测量结果会与台账不符」
#   ⇒ 后者由 `--verify` 模式负责（★ 它需要真实库）⇒ ⇒ 两者是【互补】的两层。
if [ "$DRY_RUN_READONLY" = "1" ]; then
  say "== --dry-run-readonly：只读台账 + 断言契约（★ 不建库 / 不连 psql / 不 docker）=="
  if [ ! -f "$LEDGER" ]; then
    say "  ✗ 台账不存在：${LEDGER}"
    exit 1
  fi
  say "  ✓ 台账存在：${LEDGER}"

  # ② 数据行 ≥ 1
  rows="$(grep -c -vE '^[[:space:]]*(#|$)' "$LEDGER" || true)"
  if [ "${rows:-0}" -lt 1 ]; then
    say "  ✗ 台账 0 数据行 ⇒ 台账被掏空 ⇒ 拒绝出结论"
    exit 1
  fi
  say "  ✓ 数据行 = ${rows}"

  # ③ 每行 5 列
  badcols="$(awk -F'\t' '!/^[[:space:]]*(#|$)/ && NF!=5 {n++} END{print n+0}' "$LEDGER")"
  if [ "$badcols" != "0" ]; then
    say "  ✗ 台账有 ${badcols} 行列数不是 5（契约：gate/file/probe_target/verdict/reason）"
    exit 1
  fi
  say "  ✓ 每行 5 列"

  # ④ verdict ∈ {PASS,FAIL,SKIP}
  badverdict="$(awk -F'\t' '!/^[[:space:]]*(#|$)/ && $4 !~ /^(PASS|FAIL|SKIP)$/ {n++} END{print n+0}' "$LEDGER")"
  if [ "$badverdict" != "0" ]; then
    say "  ✗ 台账有 ${badverdict} 行 verdict 不是 PASS/FAIL/SKIP"
    exit 1
  fi
  say "  ✓ verdict 取值合法"

  # ⑤ gate 列非空且唯一
  emptygate="$(awk -F'\t' '!/^[[:space:]]*(#|$)/ && $1=="" {n++} END{print n+0}' "$LEDGER")"
  if [ "$emptygate" != "0" ]; then
    say "  ✗ 台账有 ${emptygate} 行 gate 列为空"
    exit 1
  fi
  dupgate="$(awk -F'\t' '!/^[[:space:]]*(#|$)/ {print $1}' "$LEDGER" | sort | uniq -d | wc -l | tr -d ' ')"
  if [ "${dupgate:-0}" != "0" ]; then
    say "  ✗ 台账有 ${dupgate} 个 gate 被重复登记"
    exit 1
  fi
  say "  ✓ gate 列非空且唯一"

  # ⑥ file 列指向仓内真实存在的文件
  missingfile="$(awk -F'\t' '!/^[[:space:]]*(#|$)/ {print $2}' "$LEDGER" | sort -u | while IFS= read -r f; do
      [ -n "$f" ] || continue
      [ -f "$f" ] || echo "$f"
    done | wc -l | tr -d ' ')"
  if [ "${missingfile:-0}" != "0" ]; then
    say "  ✗ 台账有 ${missingfile} 行的 file 列指向仓内不存在的文件"
    awk -F'\t' '!/^[[:space:]]*(#|$)/ {print $2}' "$LEDGER" | sort -u | while IFS= read -r f; do
      [ -n "$f" ] || continue
      [ -f "$f" ] || say "      缺：${f}"
    done
    exit 1
  fi
  say "  ✓ file 列指向仓内真实存在的文件"

  say "== 汇总 =="
  say "  台账契约 = 通过（★ 本模式未碰任何数据库）"
  exit 0
fi

# ---------------------------------------------------------------- 形态库
if [ -z "${SHAPE_DB:-}" ]; then
  say "== 1. 建一次性形态库 ${DB_NAME}（prereqs + 基线 + 注册的启动迁移）=="
  DB_NAME="${DB_NAME}" KEEP_DB=1 PG_CONTAINER="$PG_CONTAINER" PG_USER="$PG_USER" \
    bash scripts/audit/probe-onetime-db-shape.sh > /tmp/permskip-build.log 2>&1 || {
      say "  ✗ 建库失败："; tail -6 /tmp/permskip-build.log; exit 2; }
  say "  ✓ 建库完成（日志 /tmp/permskip-build.log）"
  SHAPE_DB="$DB_NAME"
else
  say "== 1. 复用已有形态库 ${SHAPE_DB} =="
fi

PW="$(docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null | head -1)"
[ -n "$PW" ] || die "取不到 ${PG_CONTAINER} 的 POSTGRES_PASSWORD"
DSN="postgres://${PG_USER}:${PW}@127.0.0.1:5432/${SHAPE_DB}?sslmode=disable"

# ---------------------------------------------------------------- 量具自检
say "== 2. 量具自检（自检不过 ⇒ 中止，绝不进入读数阶段）=="
got="$(read_val "$DSN" "SELECT current_database()")"
[ "$got" = "$SHAPE_DB" ] || die "自检失败：连上的库是 '${got}'，期望 '${SHAPE_DB}'（库名大小写折叠 / DSN 指错库）"
say "  ✓ current_database() = ${got}"

# 形态库必须有这些对象，否则「跑不跑」这个问题本身无意义
for t in models_canonical model_offers credentials providers; do
  k="$(read_val "$DSN" "SELECT coalesce((SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='${t}'),'ABSENT')")"
  [ "$k" != "ABSENT" ] || die "自检失败：形态库缺 ${t}（这不是产品形态库）"
done
say "  ✓ 形态库关键对象在位"

# ★ 指纹的**确定性自检**：同一库连取 2 次结构指纹，必须逐字一致
fingerprint() {
  local dsn="$1"
  {
    read_val "$dsn" "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'"
    read_val "$dsn" "SELECT count(*) FROM pg_tables WHERE schemaname='public'"
    read_val "$dsn" "SELECT count(*) FROM pg_views WHERE schemaname='public'"
    read_val "$dsn" "SELECT count(*) FROM pg_matviews WHERE schemaname='public'"
    read_val "$dsn" "SELECT count(*) FROM pg_indexes WHERE schemaname='public'"
    read_val "$dsn" "SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='public'"
    read_val "$dsn" "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND NOT t.tgisinternal"
    read_val "$dsn" "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public'"
    # ★ 结构指纹：relkind 必须 ::text（"char" 拼接会报错）
    read_val "$dsn" "SELECT md5(string_agg(c.relname||':'||c.relkind::text, ',' ORDER BY c.relname)) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'"
    # ★ 第二道独立指纹：pg_dump 产物，**剥掉随机 \restrict / \unrestrict**（见纪律 0 ①）
    docker exec "$PG_CONTAINER" pg_dump -U "$PG_USER" --schema-only "$SHAPE_DB" 2>/dev/null \
      | grep -v -E '^\\(un)?restrict ' | md5 | awk '{print $NF}'
  } | sed '/^$/d'
}

FP_A="$(fingerprint "$DSN")"
sleep 1
FP_B="$(fingerprint "$DSN")"
if [ "$FP_A" != "$FP_B" ]; then
  say "  ✗ 指纹量具不确定（连取 2 次结果不同）⇒ 中止："
  diff <(printf '%s\n' "$FP_A") <(printf '%s\n' "$FP_B") | head -10
  exit 2
fi
say "  ✓ 指纹量具确定性自检通过（连取 2 次逐字一致）"

# ---------------------------------------------------------------- 跑门
say "== 3. 跑门（TEST_DATABASE_URL = 形态库）=="
LOG=/tmp/permskip-gates.log
TEST_DATABASE_URL="$DSN" go test ./admin/ -count=1 -v -timeout 900s \
  -run 'TestListModels_modalityProvenanceReachesTheList|TestGetModel_exposesProvenanceInTheResponseBody|TestPricingImport_|TestSessionFinalSuccessBacklogIsClosed|TestSessionFamilyRuntimeSQL_ExecutesOnRealDB|TestRetiredPartitionTableStatusIsNotAnError' \
  > "$LOG" 2>&1
RC=$?
say "  go test exit=${RC}（日志 ${LOG}）"

say "== 4. 每条门的判定 =="
grep -E '^--- (PASS|FAIL|SKIP): ' "$LOG" | sed 's/^--- /  /' | sed 's/ (.*)$//'

# ---------------------------------------------------------------- AFTER 对拍
say "== 5. AFTER 指纹对拍（形态库是否受损）=="
FP_AFTER="$(fingerprint "$DSN")"
if [ "$FP_A" = "$FP_AFTER" ]; then
  say "  ✓ 形态库零损伤（9 项指纹逐字一致）"
  DAMAGE=0
else
  say "  ✗ 形态库有变化："
  diff <(printf '%s\n' "$FP_A") <(printf '%s\n' "$FP_AFTER") | head -10
  DAMAGE=1
fi

# ---------------------------------------------------------------- 台账
say "== 6. 台账 =="
mkdir -p "$(dirname "$LEDGER")"
NOW="$(date +%Y-%m-%dT%H:%M:%S)"
{
  printf '# permanent-skip-ledger.tsv —— 「门在产品形态库上跑不跑」的实测台账\n'
  printf '# 生成方式：bash scripts/audit/measure-permanent-skip-axis.sh --verify\n'
  printf '# 列：gate\tfile\tprobe_target\tverdict\treason\n'
  printf '# ★ verdict = 该门在「TEST_DATABASE_URL 指向产品形态库」这一档的实测判定（PASS/SKIP/FAIL）\n'
  printf '# ★ reason  = 判定依据（哪一行代码 / 哪条分支）\n'
  printf '# ★ damage  = 形态库是否受损（0=零损伤 1=有变化）\n'
  grep -E '^--- (PASS|FAIL|SKIP): ' "$LOG" | sed -E 's/^--- (PASS|FAIL|SKIP): ([A-Za-z0-9_]+).*/\2\t\1/' \
    | while IFS=$'\t' read -r g v; do
        case "$g" in
          TestListModels_modalityProvenanceReachesTheList|TestGetModel_exposesProvenanceInTheResponseBody)
            printf '%s\tadmin/models_modality_source_realdb_test.go\t8 张夹具表（建在自有一次性空库上）\t%s\tscratchEmptyPool 自建一次性空库 ⇒ 探针在空库上命中「0 张表」⇒ 真跑\n' "$g" "$v" ;;
          TestPricingImport_*)
            printf '%s\tadmin/pricing_import_realdb_test.go\tmodel_offers（建在自有一次性空库上）\t%s\tscratchEmptyPool 自建一次性空库 ⇒ 探针在空库上命中「不存在」⇒ 真跑\n' "$g" "$v" ;;
          TestSessionFinalSuccessBacklogIsClosed)
            printf '%s\tadmin/session_final_success_backlog_realdb_test.go\tpublic.request_logs_2026_09\t%s\t直读 TEST_DATABASE_URL；v1 族已退役 ⇒ to_regclass 为空 ⇒ 恒 Skip\n' "$g" "$v" ;;
          TestSessionFamilyRuntimeSQL_ExecutesOnRealDB)
            printf '%s\tadmin/session_family_runtime_sql_test.go\tsession_summaries/session_dim/models_canonical\t%s\t直读 TEST_DATABASE_URL ⇒ 探针就在形态库上；只跑 SELECT ⇒ 零足迹\n' "$g" "$v" ;;
          TestRetiredPartitionTableStatusIsNotAnError)
            printf '%s\tadmin/session_family_runtime_sql_test.go\t_request_logs（已退役）/ usage_ledger（在位）\t%s\t直读 TEST_DATABASE_URL ⇒ 探针就在形态库上；_request_logs 不在位 ⇒ 不走 Skip 分支\n' "$g" "$v" ;;
          *) printf '%s\t-\t-\t%s\t-\n' "$g" "$v" ;;
        esac
      done
} > "$LEDGER.new"

if [ "$VERIFY" = "1" ]; then
  say "== 7. --verify：本次实测 vs 仓内已提交的台账 =="
  if git cat-file -e "HEAD:${LEDGER}" 2>/dev/null; then
    git show "HEAD:${LEDGER}" > /tmp/permskip-ledger-committed.tsv
    if diff -u /tmp/permskip-ledger-committed.tsv "$LEDGER.new" > /tmp/permskip-ledger.diff 2>&1; then
      say "  ✓ 台账与实测逐字一致（无 diff）"
    else
      say "  ✗ 台账与实测不一致（diff 见下）"
      head -30 /tmp/permskip-ledger.diff
      VERIFY_FAIL=1
    fi
  else
    say "  ! 台账尚未入仓（HEAD 里没有 ${LEDGER}）—— 这是首次生成，请把 ${LEDGER} 一起提交"
    VERIFY_FAIL=1
  fi
  rm -f "$LEDGER.new"
else
  mv "$LEDGER.new" "$LEDGER"
  say "  ✓ 写入 ${LEDGER}（${NOW}）"
fi

# ---------------------------------------------------------------- 回收
if [ "$KEEP_DB" = "1" ]; then
  say "== 8. 保留形态库 ${SHAPE_DB}（KEEP_DB=1）=="
else
  say "== 8. 回收形态库 ${SHAPE_DB} =="
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "DROP DATABASE IF EXISTS \"${SHAPE_DB}\" WITH (FORCE)" >/dev/null 2>&1
  say "  ✓ 已 DROP"
fi

say "== 汇总 =="
say "  形态库受损 = ${DAMAGE}"
if [ "${VERIFY_FAIL:-0}" = "1" ]; then
  say "  台账对拍 = 不一致"
  exit 1
fi
exit 0
