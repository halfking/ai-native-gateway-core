#!/usr/bin/env bash
# deploy-local-lib-pgmemory_test.sh — 2026-10-20
#
# 钉住「shared_buffers 下限体检」这道门的行为。
#
# 为什么这道门值得单独一道门（背景见 dl_pg_check_memory 的注释）：
# shared_buffers 被调小到几百 kB 时，**探活全绿、部署报 VERIFY_PASS=1**，
# 但每条语句的规划退化到 ~1s ⇒ session 族写路径在 context deadline 内超时
# ⇒ **请求返回 200 而一行都没落库**。这是本仓最难发现的一类：
# 「部署成功」与「数据可用」在这里是两件事。
#
# 本门驱动**真实现**（不是复制品）：stub 掉 log/warn/die 与 _dl_pg_scalar，
# 直接调 dl_pg_check_memory，断言它的判定与分支。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

# 跑一个场景：给定 SHOW shared_buffers 的返回值，收集 dl_pg_check_memory 的输出与退出码。
# $1 = shared_buffers 原样字符串；$2 = 是否要求 fail-closed
run_case() {
  local raw="$1" require="$2"
  # die 的桩会 exit 7 —— 那正是被测行为，这里必须**收下**而不是让 set -e 掐掉整道门。
  ( DL_PG_MIN_SHARED_BUFFERS_REQUIRED="$require" \
    DL_PG_MIN_SHARED_BUFFERS_KB=131072 \
    bash -c '
      set -uo pipefail
      ROOT_DIR="'"$ROOT_DIR"'"
      LIB="'"$LIB"'"
      log()  { printf "LOG:%s\n" "$*"; }
      warn() { printf "WARN:%s\n" "$*"; }
      die()  { printf "DIE:%s\n" "$*"; exit 7; }
      # shellcheck disable=SC1090
      source "$LIB"
      # 把取值那一层换成常量：真实现读 SHOW shared_buffers，这里给受控读数。
      _dl_pg_scalar() { printf "%s\n" "'"$raw"'"; return 0; }
      dl_pg_check_memory "u" "p" "h" "5432" "db"
    ' 2>&1 ) || true
}

# ── 1. 单位换算（每种形态都要有确定读数）────────────────────────────────
kb_of() { run_case "$1" 0 | grep -oE '≈[0-9]+kB' | head -1 | sed -E 's/≈([0-9]+)kB/\1/'; }

assert_kb() {
  local raw="$1" want="$2" got
  got="$(kb_of "$raw")"
  if [[ "$got" != "$want" ]]; then
    printf 'FAIL: %s -> %s kB, want %s kB\n' "$raw" "${got:-<none>}" "$want" >&2
    exit 1
  fi
}
assert_kb '400kB'  400
assert_kb '128MB'  131072
assert_kb '2GB'    2097152
assert_kb '16384'  131072

# ── 2. 低于阈值 ⇒ warn（默认不致命）────────────────────────────────────
out="$(run_case '400kB' 0)"
if ! printf '%s' "$out" | grep -q '^WARN:'; then
  printf 'FAIL: 400kB did not produce a warning\n%s\n' "$out" >&2
  exit 1
fi
if printf '%s' "$out" | grep -q '^DIE:'; then
  printf 'FAIL: the guard killed the deploy by default; it must warn unless DL_PG_MIN_SHARED_BUFFERS_REQUIRED=1\n%s\n' "$out" >&2
  exit 1
fi

# ── 3. 低于阈值 + fail-closed ⇒ die ────────────────────────────────────
out="$(run_case '400kB' 1)"
if ! printf '%s' "$out" | grep -q '^DIE:'; then
  printf 'FAIL: fail-closed mode did not die on 400kB\n%s\n' "$out" >&2
  exit 1
fi

# ── 4. 达到/超过阈值 ⇒ 不 warn ──────────────────────────────────────────
out="$(run_case '2GB' 1)"
if printf '%s' "$out" | grep -qE '^(WARN|DIE):'; then
  printf 'FAIL: 2GB must pass even in fail-closed mode\n%s\n' "$out" >&2
  exit 1
fi
if ! printf '%s' "$out" | grep -q 'shared_buffers=2GB'; then
  printf 'FAIL: the passing case must still report the observed value\n%s\n' "$out" >&2
  exit 1
fi

# ── 5. 门槛可调 ─────────────────────────────────────────────────────────
out="$(DL_PG_MIN_SHARED_BUFFERS_KB=1048576 bash -c '
  set -uo pipefail
  log()  { printf "LOG:%s\n" "$*"; }
  warn() { printf "WARN:%s\n" "$*"; }
  die()  { printf "DIE:%s\n" "$*"; exit 7; }
  # shellcheck disable=SC1090
  source "'"$LIB"'"
  _dl_pg_scalar() { printf "512MB\n"; return 0; }
  dl_pg_check_memory "u" "p" "h" "5432" "db"
' 2>&1)"
if ! printf '%s' "$out" | grep -q '^WARN:'; then
  printf 'FAIL: raising the threshold to 1GB must make 512MB warn\n%s\n' "$out" >&2
  exit 1
fi

# ── 6. 取不到读数时不得误杀（降级而不是编造）──────────────────────────
out="$(bash -c '
  set -uo pipefail
  log()  { printf "LOG:%s\n" "$*"; }
  warn() { printf "WARN:%s\n" "$*"; }
  die()  { printf "DIE:%s\n" "$*"; exit 7; }
  # shellcheck disable=SC1090
  source "'"$LIB"'"
  _dl_pg_scalar() { return 1; }
  dl_pg_check_memory "u" "p" "h" "5432" "db"
  printf "SURVIVED\n"
' 2>&1)"
if ! printf '%s' "$out" | grep -q 'SURVIVED'; then
  printf 'FAIL: an unreadable shared_buffers must not kill the deploy\n%s\n' "$out" >&2
  exit 1
fi
if printf '%s' "$out" | grep -qE '^(WARN|DIE):'; then
  printf 'FAIL: an unreadable shared_buffers must stay silent, not warn on a value we never saw\n%s\n' "$out" >&2
  exit 1
fi

# ── 7. 门必须接在探活成功分支上（否则永远不触发）────────────────────────
call_sites="$(grep -c 'dl_pg_check_memory "\$user"' "$LIB" || true)"
if (( call_sites < 2 )); then
  printf 'FAIL: dl_pg_check_memory wired into only %s probe success path(s); want >= 2 (docker exec + psql)\n' "$call_sites" >&2
  exit 1
fi

echo 'deploy-local-lib PG shared_buffers guard test: PASS'
