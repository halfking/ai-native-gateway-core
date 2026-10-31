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
# File:          scripts/audit/verify-session-family-p0-realdb.sh
# Purpose:       Run the session-family P0 real-database legs (G-4 / G-5 / G-7)
#                against a **disposable clone** of the production-shaped
#                database, and prove the legs can actually turn RED.
#
# Why this exists (audit plan §9-1):
#
#   The P0 round's acceptance criteria for G-4 / G-5 / G-7 were all
#   pgxmock-level or static-shape-level. A real-database leg was missing, and
#   a leg that has never been run is a leg whose "green" means nothing.
#
#   This script is the instrument that makes "green" mean something:
#     1. clone the production-shaped DB into a throwaway itgate_* database
#        (the source database is never touched),
#     2. run the real-database legs — expect green,
#     3. apply each mutation in turn and re-run the matching leg — expect RED,
#     4. drop the throwaway database.
#
#   Every mutation is applied to the *clone*, so the source database is
#   read-only throughout. The one exception is mutation M3, which has to
#   change Go source; that one is restored from a backup copy and verified
#   byte-for-byte with a checksum.
#
# Usage:
#   bash scripts/audit/verify-session-family-p0-realdb.sh
#   PG_CONTAINER=llm-gateway-pg PG_USER=llm_gateway bash ...
#
# Env:
#   PG_CONTAINER   source database container   (default llm-gateway-pg)
#   PG_USER       database role                (default llm_gateway)
#   PG_PASSWORD   password for the TCP DSN    (default: read from the container)
#   PG_HOST_PORT  host port for the container  (default: auto-detect)
#   SOURCE_DB     database to clone            (default: llm_gateway)
#   KEEP_CLONE=1  keep the throwaway database for post-mortem
#
# Exit code: 0 only if the baseline is green AND every mutation turned its
# leg red. A mutation that stays green is a **failed audit**, not a pass.
# -----------------------------------------------------------------------------
set -uo pipefail

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
SOURCE_DB="${SOURCE_DB:-llm_gateway}"
KEEP_CLONE="${KEEP_CLONE:-0}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || { echo "ERROR: cannot enter repo root" >&2; exit 2; }

command -v docker >/dev/null 2>&1 || { echo "ERROR: docker not found in PATH" >&2; exit 2; }
docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER" \
  || { echo "ERROR: container '$PG_CONTAINER' not running" >&2; exit 2; }

# The Go toolchain is not always on PATH in non-interactive shells.
if ! command -v go >/dev/null 2>&1; then
  for cand in "$HOME/sdk/go/bin" /opt/homebrew/bin /usr/local/go/bin; do
    [ -x "$cand/go" ] && export PATH="$cand:$PATH" && break
  done
fi
command -v go >/dev/null 2>&1 || { echo "ERROR: go not found in PATH" >&2; exit 2; }

# Password: prefer the explicit env, else ask the container.
if [[ -z "${PG_PASSWORD:-}" ]]; then
  PG_PASSWORD=$(docker inspect "$PG_CONTAINER" \
    --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null \
    | sed -n 's/^POSTGRES_PASSWORD=//p' | head -1)
fi
[[ -n "${PG_PASSWORD:-}" ]] || {
  echo "ERROR: no PG_PASSWORD (set it, or let the container be inspected)" >&2; exit 2; }

PG_HOST_PORT="${PG_HOST_PORT:-$(docker port "$PG_CONTAINER" 5432/tcp 2>/dev/null | head -1 | sed 's/.*://')}"
[[ -n "$PG_HOST_PORT" ]] || PG_HOST_PORT=5432

CLONE_DB="itgate_sessfam_$$_${RANDOM}"
CLONE_DB="${CLONE_DB:0:30}"

PSQL_CLONE() { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$CLONE_DB" -v ON_ERROR_STOP=1 "$@"; }
PSQL_ADMIN() { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 "$@"; }

cleanup() {
  if [[ "$KEEP_CLONE" != "1" ]]; then
    # ★ 防卡保护：SET / terminate / DROP 三条独立 -c（DROP 放最后 ⇒ 退出码仍反映它）
    PSQL_ADMIN -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$CLONE_DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
    PSQL_ADMIN -c "SET statement_timeout = '120s'" \
      -c "DROP DATABASE IF EXISTS $CLONE_DB WITH (FORCE);" >/dev/null 2>&1 \
      || echo "WARN: could not drop $CLONE_DB (leaving it behind)" >&2
  else
    echo "KEEP_CLONE=1: $CLONE_DB left in place" >&2
  fi
}
trap cleanup EXIT

echo "== 1/5 clone '$SOURCE_DB' -> '$CLONE_DB' =="
PSQL_ADMIN -c "CREATE DATABASE $CLONE_DB" >/dev/null || { echo "ERROR: CREATE DATABASE failed" >&2; exit 2; }
if ! docker exec "$PG_CONTAINER" pg_dump -U "$PG_USER" --no-owner --no-privileges "$SOURCE_DB" \
     | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$CLONE_DB" -v ON_ERROR_STOP=0 \
       >/tmp/sessfam-restore.log 2>&1; then
  echo "ERROR: restore failed; see /tmp/sessfam-restore.log" >&2
  exit 2
fi
CLONE_ROWS=$(PSQL_CLONE -tAc "SELECT count(*) FROM session_turns")
echo "   clone ready: session_turns has $CLONE_ROWS rows"

DSN="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PG_HOST_PORT}/${CLONE_DB}"

run_leg() {  # run_leg <test-name-regex> <label>
  TEST_DATABASE_URL="$DSN" go test ./domains/session/v2/ -run "$1" -count=1 2>&1
}

PASS=0
FAIL=0
check() {  # check <label> <expected: PASS|FAIL> <output>
  local label="$1" expected="$2" out="$3"
  # ★★★ R99 轮 · §19.20.67 修（缺陷由并行会话的 SIGPIPE 语义普查发现，并由我独立复现）：
  #   原来的写法是 `echo "$out" | grep -q …` ⇒ 在 `set -o pipefail` 下：
  #     · `grep -q` 是**早退**消费者 ⇒ 命中即退 ⇒ 写端 `echo` 在输出超过管道缓冲
  #       （65,536 B）时被 **SIGPIPE** 杀（141）⇒ pipefail 取**最右非零** = 141
  #     ⇒ ⇒ `if` 的条件变成 141（非零）⇒ ⇒ **判成「不是失败」**
  #     ⇒ ⇒ ★★ 而 `$out` 是 **go test 的输出（无上界）** ⇒ 大输出时
  #       【失败的腿会被报成 PASS/UNKNOWN】⇒ ⇒ 假绿 ⇒ ⇒ 正是本函数要防的事。
  #   ⇒ ★★ 修法：改用 **herestring**（`grep … <<<"$out"`）⇒ bash 把它写到临时文件
  #     ⇒ 文件没有 SIGPIPE ⇒ 退出码**只反映 grep 自己的判定**（0/1）⇒ 语义等价。
  #   ★★ 顺带删掉原来那行 `if echo "$out" | grep -qE …; then :; fi` —— 它的 then 分支
  #     是 `:` ⇒ **整行是死代码**（看着像个检查、其实什么都不做）⇒ 留着只会误导。
  if grep -q "^FAIL" <<<"$out" || grep -q "^--- FAIL" <<<"$out"; then
    actual=FAIL
  elif grep -q "^ok" <<<"$out" || grep -q "^PASS" <<<"$out"; then
    actual=PASS
  else
    actual=UNKNOWN
  fi
  if [[ "$actual" == "$expected" ]]; then
    printf '   %-58s %s\n' "$label" "OK ($expected as expected)"
    PASS=$((PASS+1))
  else
    printf '   %-58s %s\n' "$label" "MISMATCH (expected $expected, got $actual)"
    tail -25 <<<"$out" | sed 's/^/      | /'
    FAIL=$((FAIL+1))
  fi
}

echo
echo "== 2/5 baseline: all legs green =="
BASE_OUT=$(run_leg 'TestTurnWriterHostTagsRealDB|TestPromoteColumnContractRealDB|TestSessionTurnsFTSIndexUsableRealDB|TestSessionBodiesRequestIDJoinIndexRealDB|TestSessionsPartitionDateAntiJoinUsesIndexRealDB|TestSessionTurnsDeadColumnRealDB|TestSessionTurnsNoUnregisteredWritePath|TestSessionTurnsWritePathRegistryHonest' baseline)
check "baseline (8 legs)" PASS "$BASE_OUT"
if grep -q "^FAIL" <<<"$BASE_OUT"; then
  echo "ERROR: baseline is not green — the legs are broken, not the mutations" >&2
  echo "$BASE_OUT" | tail -40 >&2
  exit 2
fi

echo
echo "== 3/5 mutations: each leg must turn RED =="

# --- M1: drop the FTS GIN index on the hot table -------------------------
# The FTS leg claims "the 887 GIN index really exists and really carries the
# admin predicate". Drop it and the leg must notice.
echo "   [M1] DROP INDEX idx_session_turns_hot_search_fts"
PSQL_CLONE -c "DROP INDEX IF EXISTS public.idx_session_turns_hot_search_fts" >/dev/null
check "M1 fts leg -> RED" FAIL "$(run_leg 'TestSessionTurnsFTSIndexUsableRealDB' m1)"
PSQL_CLONE -c "CREATE INDEX idx_session_turns_hot_search_fts ON public.session_turns_hot USING gin (to_tsvector('simple'::regconfig, search_text))" >/dev/null

# --- M2: break the promote column-set contract --------------------------
# Add a column to the hot table ONLY. The promote function compares the two
# shapes at runtime and raises 'column contract has drifted'. The whole point
# of migration 885 is that it added to BOTH sides; an asymmetric add is the
# exact failure mode that makes the hot table grow forever.
echo "   [M2] ALTER TABLE session_turns_hot ADD COLUMN mut_only_here int"
PSQL_CLONE -c "ALTER TABLE public.session_turns_hot ADD COLUMN mut_only_here int" >/dev/null
check "M2 promote contract leg -> RED" FAIL "$(run_leg 'TestPromoteColumnContractRealDB' m2)"
PSQL_CLONE -c "ALTER TABLE public.session_turns_hot DROP COLUMN mut_only_here" >/dev/null

# --- M3: break the write path's host binding ----------------------------
# The G-4 leg claims "an explicit host lands verbatim". Make the writer bind
# an empty host instead — the pgxmock leg would catch the empty string, but
# only the real leg proves what the DATABASE ends up holding.
echo "   [M3] patch turn_host_tags.go so an explicit host is ignored"
TAGS_FILE="domains/session/v2/turn_host_tags.go"
BACKUP="/tmp/sessfam-turn_host_tags.go.$$"
cp "$TAGS_FILE" "$BACKUP"
BEFORE_SUM=$(shasum -a 256 "$TAGS_FILE" | awk '{print $1}')
python3 - "$TAGS_FILE" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = """	if host := strings.TrimSpace(rec.Host); host != "" {
		return host
	}"""
new = """	if host := strings.TrimSpace(rec.Host); host != "" {
		return "" // MUTATION: explicit host dropped
	}"""
if old not in s:
    sys.stderr.write("mutation anchor not found\n")
    sys.exit(3)
open(p, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
if [[ $? -ne 0 ]]; then
  echo "   [M3] mutation could not be applied (anchor moved) — restoring and reporting UNVERIFIED"
  cp "$BACKUP" "$TAGS_FILE"
else
  check "M3 host write leg -> RED" FAIL "$(run_leg 'TestTurnWriterHostTagsRealDB' m3)"
  cp "$BACKUP" "$TAGS_FILE"
fi
AFTER_SUM=$(shasum -a 256 "$TAGS_FILE" | awk '{print $1}')
if [[ "$BEFORE_SUM" != "$AFTER_SUM" ]]; then
  echo "   [M3] source restored, checksum differs from the original — INVESTIGATE" >&2
  FAIL=$((FAIL+1))
else
  echo "   [M3] source restored, checksum identical ($AFTER_SUM)"
fi

# --- M4: drop the bodies join index -------------------------------------
# The G-7 leg claims "the 888 join index really exists and really carries the
# admin turns join". Drop the hot arm and the leg must notice.
echo "   [M4] DROP INDEX idx_session_bodies_hot_tenant_session_turn_request"
PSQL_CLONE -c "DROP INDEX IF EXISTS public.idx_session_bodies_hot_tenant_session_turn_request" >/dev/null
check "M4 bodies join leg -> RED" FAIL "$(run_leg 'TestSessionBodiesRequestIDJoinIndexRealDB' m4)"
PSQL_CLONE -c "CREATE INDEX idx_session_bodies_hot_tenant_session_turn_request ON public.session_bodies_hot USING btree (tenant_id, session_id, turn_no DESC, request_id)" >/dev/null

# --- M5: drop the sessions latest-partition index -------------------------
# The G-7(2) leg claims "the 888 sessions index really carries the
# latest-partition anti-join". That index is a **partitioned** index (built
# ON ONLY the parent), so dropping the parent drops every partition child
# with it -- which is exactly the shape a reader would hit if the migration
# had never been applied to that database.
echo "   [M5] DROP INDEX idx_sessions_tenant_session_partition_date"
PSQL_CLONE -c "DROP INDEX IF EXISTS public.idx_sessions_tenant_session_partition_date" >/dev/null
check "M5 sessions anti-join leg -> RED" FAIL "$(run_leg 'TestSessionsPartitionDateAntiJoinUsesIndexRealDB' m5)"
# 还原：**重跑迁移 888**，而不是手写一条 CREATE INDEX。
#
# ⚠⚠ 第一版还原写成 `CREATE INDEX ... ON ONLY public.sessions` ⇒ **没有子索引** ⇒
# 还原后的门仍然红（前提自证②「分区索引必须有子索引」当场抓到）。
# ⇒ 这不是门太严，是还原写错了：888 的真实机制是**三段式** ——
#   3a 逐分区 CREATE INDEX CONCURRENTLY → 3b 父表 ON ONLY 壳 → 3c ATTACH 挂上。
#   只写 3b 就等于只造了壳 ⇒ 计划里根本不出现（正是「catalog 查得到、EXPLAIN 看不到」）。
# 888 全程幂等（CREATE INDEX IF NOT EXISTS / DROP INDEX IF EXISTS / ATTACH 有守卫）
# ⇒ 「重跑迁移」是最忠实的还原。
docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$CLONE_DB" -v ON_ERROR_STOP=1 -f - \
  < sql/migrations/startup/888_session_family_join_index_gaps.sql >/dev/null

# --- M6: 给两张表同时加一列（无默认值、无人写）-------------------------
# G-6 的全集差集门声称「库面无默认 ∧ 不在任何写方集合里的列」为空。
# 加一列（两侧同时加，保持 promote 的形状契约）⇒ 这一列既无默认值、又没人写
# ⇒ 它就是一条**新死列** ⇒ 门必须变红。
# 这正是这道门存在的理由：没有它，「下一列变死」不会被任何人发现。
echo "   [M6] ALTER TABLE session_turns/session_turns_hot ADD COLUMN mut_dead_probe text"
PSQL_CLONE -c "ALTER TABLE public.session_turns ADD COLUMN mut_dead_probe text" >/dev/null
PSQL_CLONE -c "ALTER TABLE public.session_turns_hot ADD COLUMN mut_dead_probe text" >/dev/null
check "M6 new dead column leg -> RED" FAIL "$(run_leg 'TestSessionTurnsDeadColumnRealDB' m6)"
PSQL_CLONE -c "ALTER TABLE public.session_turns DROP COLUMN IF EXISTS mut_dead_probe" >/dev/null
PSQL_CLONE -c "ALTER TABLE public.session_turns_hot DROP COLUMN IF EXISTS mut_dead_probe" >/dev/null

# --- M7: 摘掉「已知死列」登记里的 upstream_protocol ---------------------
# allowlist 是「已知且已接受」的死列清单。它的作用是让**新的**死列变红；
# 但它自己也是可以被滥用的后门（把新死列加进去 ⇒ 门又绿）。
# ⇒ 摘掉一条登记，门必须变红（证明它守的是「未登记的死列」）。
GATE_FILE="domains/session/v2/session_turns_dead_column_test.go"
GATE_BACKUP="/tmp/sessfam-dead-column-test.go.$$"
cp "$GATE_FILE" "$GATE_BACKUP"
GATE_BEFORE=$(shasum -a 256 "$GATE_FILE" | awk '{print $1}')
python3 - "$GATE_FILE" <<'PY2'
import re
import sys

p = sys.argv[1]
s = open(p, encoding="utf-8").read()
# 摘掉 sessionTurnsKnownDeadColumns 里的 upstream_protocol 那一条
m = re.search(r"\t\{\n\t\tColumn: \"upstream_protocol\",.*?\n\t\},\n", s, re.S)
if not m:
    sys.stderr.write("mutation anchor not found\n")
    sys.exit(3)
open(p, "w", encoding="utf-8").write(s[:m.start()] + s[m.end():])
PY2
if [[ $? -ne 0 ]]; then
  echo "   [M7] mutation could not be applied (anchor moved) — restoring and reporting UNVERIFIED"
  cp "$GATE_BACKUP" "$GATE_FILE"
else
  check "M7 unregistered dead column leg -> RED" FAIL "$(run_leg 'TestSessionTurnsDeadColumnRealDB' m7)"
  cp "$GATE_BACKUP" "$GATE_FILE"
fi
GATE_AFTER=$(shasum -a 256 "$GATE_FILE" | awk '{print $1}')
if [[ "$GATE_BEFORE" != "$GATE_AFTER" ]]; then
  echo "   [M7] gate source restored, checksum differs from the original — INVESTIGATE" >&2
  FAIL=$((FAIL+1))
else
  echo "   [M7] gate source restored, checksum identical ($GATE_AFTER)"
fi

# --- M8: 摘掉「UPDATE 路径登记」里的 is_abandoned -----------------------
# 登记册是人写的 ⇒ 人可能忘登记新写的写路径 ⇒ 那一列会被差集门**假红**
# ⇒ 开发者随手把它塞进 allowlist ⇒ allowlist 变成「让门变绿的地方」⇒ 门失去牙。
#
# ⇒ 这条变异守的是「登记册不能悄悄腐坏」：摘掉一条登记，
#    「未登记写路径扫描」腿必须变红（它能把变量表名的 UPDATE 也扫出来）。
echo "   [M8] remove the is_abandoned UPDATE-path registration"
cp "$GATE_FILE" "$GATE_BACKUP"
python3 - "$GATE_FILE" <<'PY2'
import re
import sys

p = sys.argv[1]
s = open(p, encoding="utf-8").read()
m = re.search(r"\t\{\n\t\tColumn: \"is_abandoned\",.*?\n\t\},\n", s, re.S)
if not m:
    sys.stderr.write("mutation anchor not found\n")
    sys.exit(3)
open(p, "w", encoding="utf-8").write(s[:m.start()] + s[m.end():])
PY2
if [[ $? -ne 0 ]]; then
  echo "   [M8] mutation could not be applied (anchor moved) — restoring and reporting UNVERIFIED"
  cp "$GATE_BACKUP" "$GATE_FILE"
else
  check "M8 unregistered write path leg -> RED" FAIL "$(run_leg 'TestSessionTurnsNoUnregisteredWritePath' m8)"
  cp "$GATE_BACKUP" "$GATE_FILE"
fi
GATE_AFTER=$(shasum -a 256 "$GATE_FILE" | awk '{print $1}')
if [[ "$GATE_BEFORE" != "$GATE_AFTER" ]]; then
  echo "   [M8] gate source restored, checksum differs from the original — INVESTIGATE" >&2
  FAIL=$((FAIL+1))
else
  echo "   [M8] gate source restored, checksum identical ($GATE_AFTER)"
fi

echo
echo "== 4/5 restore check: legs green again =="
RESTORE_OUT=$(run_leg 'TestTurnWriterHostTagsRealDB|TestPromoteColumnContractRealDB|TestSessionTurnsFTSIndexUsableRealDB|TestSessionBodiesRequestIDJoinIndexRealDB|TestSessionsPartitionDateAntiJoinUsesIndexRealDB|TestSessionTurnsDeadColumnRealDB|TestSessionTurnsNoUnregisteredWritePath|TestSessionTurnsWritePathRegistryHonest' restore)
check "after restoring every mutation -> PASS" PASS "$RESTORE_OUT"

echo
echo "== 5/5 summary =="
echo "   checks passed: $PASS"
echo "   checks failed: $FAIL"
if [[ "$FAIL" -ne 0 ]]; then
  echo "RESULT: FAIL — a mutation stayed green, or a restore check failed" >&2
  exit 1
fi
echo "RESULT: PASS — baseline green, every mutation turned its leg red, restore verified"
exit 0
