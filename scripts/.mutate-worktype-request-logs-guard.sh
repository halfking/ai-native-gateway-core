#!/usr/bin/env bash
# 变异验证：ensureWorkTypeSchema 的 request_logs 独占锁守卫（M78~M81）
# 规则：还原一律用 cp 文件副本，禁止 git checkout --（会静默丢弃未提交工作）
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestEnsureWorkTypeSchema'
T=db/db.go
BAK="$(mktemp -d)/wtguard.db.go"
cp "$T" "$BAK"
restore() { cp "$BAK" "$T"; }
trap restore EXIT

fail=0
mutate() {
  local name="$1" pyexpr="$2"
  echo "----------------- $name -----------------"
  restore
  BEFORE=$(md5 -q "$T")
  python3 - "$T" <<PY
import sys
p = sys.argv[1]
t = open(p).read()
$pyexpr
open(p, 'w').write(t)
PY
  if diff -q "$BAK" "$T" >/dev/null; then
    echo "  FAIL 变异没施上（字节没变）—— 量具问题"; fail=1; return
  fi
  echo "  变异已施上（$(diff "$BAK" "$T" | grep -c '^[<>]') 行差异）"
  if go test ./db/ -run "$GATE" -count=1 >/tmp/mutwt.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestEnsure' /tmp/mutwt.out | head -8
  else
    echo "  PASS 门转红："
    grep -E -- '    --- FAIL: TestEnsure' /tmp/mutwt.out | head -4
  fi
  restore
  if [ "$(md5 -q "$T")" != "$BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

mutate "M78 把 ALTER 塞回 workTypeSchemaSQL（守卫形同虚设）" "
t = t.replace('''CREATE INDEX IF NOT EXISTS idx_work_type_config_l1 ON work_type_config (l1_task_type);''', '''CREATE INDEX IF NOT EXISTS idx_work_type_config_l1 ON work_type_config (l1_task_type);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS work_type TEXT;''')
"

mutate "M79 先执行 DDL 再探测（等于没守卫）" "
old = '''	if d.workTypeRequestLogsCurrent(ctx) {'''
new = '''	if false && d.workTypeRequestLogsCurrent(ctx) {'''
assert t.count(old) == 1
t = t.replace(old, new)
"

mutate "M80 探测失败时当成「已就绪」跳过 DDL（缺列的库将永远补不上）" "
old = '''		slog.Warn(\"work_type request_logs probe failed; applying DDL\", \"error\", err)
		return false'''
new = '''		slog.Warn(\"work_type request_logs probe failed; skipping DDL\", \"error\", err)
		return true'''
assert t.count(old) == 1
t = t.replace(old, new)
"

mutate "M81 守卫只查列、不查索引（缺索引的库将永远补不上）" "
old = '''		  (SELECT count(*) FROM (VALUES ('idx_request_logs_work_type')) AS want(name)
		   WHERE NOT EXISTS (
		       SELECT 1 FROM pg_indexes
		        WHERE schemaname='public' AND indexname = want.name))'''
new = '''		  0 * 1'''
assert t.count(old) == 1
t = t.replace(old, new)
"

echo
if [ "$fail" -eq 0 ]; then echo "===== 全部变异均被门抓到 ====="; else echo "===== 存在无牙判据或还原失败 ====="; fi
exit $fail
