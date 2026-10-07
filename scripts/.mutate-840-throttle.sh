#!/usr/bin/env bash
# 840 节流槽的变异验证：每条变异都必须让**至少一道门转红**，且还原必须逐字节。
# 纪律：备份用 cp（禁 git checkout --）；每次变异后自证文件真的变了；
#       还原后自证 md5 回到原值。
set -uo pipefail
cd "$(dirname "$0")/.."

SQL="sql/migrations/startup/840_analyze_stats_throttle_slot.sql"
GO="bg/partition_manager.go"
BAK=/tmp/840_mut_bak
mkdir -p "$BAK"
cp "$SQL" "$BAK/sql.orig"
cp "$GO" "$BAK/go.orig"
SQL_MD5=$(md5 -q "$SQL"); GO_MD5=$(md5 -q "$GO")
echo "原文件 md5: SQL=$SQL_MD5 GO=$GO_MD5"

P=$(docker port llmgw_840_probe 5432/tcp 2>/dev/null | head -1 | tr -d '\r' | sed 's/.*://')
if [ -z "$P" ]; then echo "★ 一次性容器不在，先起：docker run -d --rm -p PORT:5432 ..."; exit 1; fi
export TEST_DATABASE_URL="postgres://postgres:pw@127.0.0.1:${P}/testdb?sslmode=disable"

restore() {
  cp "$BAK/sql.orig" "$SQL"
  cp "$BAK/go.orig" "$GO"
  local m1 m2
  m1=$(md5 -q "$SQL"); m2=$(md5 -q "$GO")
  [ "$m1" = "$SQL_MD5" ] && [ "$m2" = "$GO_MD5" ] \
    && echo "   还原自证 OK（md5 回到原值）" \
    || { echo "   ★ 还原失败！SQL $m1 / GO $m2"; exit 9; }
}

run_gates() {
  go test ./sql/migrations/startup/ -run TestMigration840 -count=1 2>&1 | tail -3 | tr '\n' ' '
  echo -n " | "
  go test ./bg/ -run "TestThrottle840" -count=1 2>&1 | tail -3 | tr '\n' ' '
  echo
}

PASS=0; FAIL=0
mutate() { # $1=编号 $2=文件 $3=python替换表达式(用 old/new 文件对)
  local id="$1" file="$2" old="$3" new="$4" target
  case "$file" in SQL) target="$SQL";; GO) target="$GO";; esac
  python3 - "$target" "$old" "$new" <<'PY'
import sys
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p).read()
if s.count(old) != 1:
    sys.exit(f"锚点命中 {s.count(old)} 次（必须恰好 1），变异未施上")
open(p, "w").write(s.replace(old, new, 1))
PY
  if [ $? -ne 0 ]; then echo "[$id] ★ 变异未施上（锚点问题）"; FAIL=$((FAIL+1)); restore; return; fi
  local changed
  changed=$(md5 -q "$target")
  if [ "$changed" = "$( [ "$file" = SQL ] && echo "$SQL_MD5" || echo "$GO_MD5")" ]; then
    echo "[$id] ★ 变异后 md5 没变 ⇒ 没真的改到文件"; FAIL=$((FAIL+1)); restore; return
  fi
  local out; out=$(run_gates)
  if echo "$out" | grep -qE "FAIL"; then
    echo "[$id] ✅ 转红"; echo "    $out"; PASS=$((PASS+1))
  else
    echo "[$id] ★ 仍全绿 ⇒ 这条门没有牙"; echo "    $out"; FAIL=$((FAIL+1))
  fi
  restore
}

echo "=== 基线（未变异，应全绿）==="
run_gates
echo

echo "=== M1 删掉 DO UPDATE 的 WHERE ⇒ 节流失效，多个赢家 ==="
mutate M1 SQL \
  "     WHERE COALESCE(s.last_completed_at, s.last_started_at) < now() - p_min_interval
    RETURNING TRUE INTO got;" \
  "    RETURNING TRUE INTO got;"

echo
echo "=== M2 去掉 COALESCE ⇒ 崩溃后槽永久卡死 ==="
mutate M2 SQL \
  "WHERE COALESCE(s.last_completed_at, s.last_started_at) < now() - p_min_interval" \
  "WHERE s.last_started_at < now() - p_min_interval"

echo
echo "=== M3 去掉 RETURNING ⇒ 调用方永远拿不到槽 ==="
mutate M3 SQL \
  "    RETURNING TRUE INTO got;" \
  "    got := true;"

echo
echo "=== M4 Go 降级分支写反（true→false）⇒ 缺表会静默停掉 analyze ==="
mutate M4 GO \
  "			slotClaimed = true" \
  "			slotClaimed = false"

echo
echo "=== M5 Go 把 complete 挪到 commit 之前（索引比较）==="
mutate M5 GO \
  "	if cerr := tx.Commit(timeoutCtx); cerr != nil {" \
  "	if _, cerr := pm.db.Exec(timeoutCtx, \"SELECT complete_llm_gateway_task_slot(\$1)\", analyzeThrottleTaskName); false && cerr != nil {"

echo
echo "════════ 结果：$PASS 条有牙，$FAIL 条无牙/未施上 ════════"
[ "$FAIL" -eq 0 ] || exit 1