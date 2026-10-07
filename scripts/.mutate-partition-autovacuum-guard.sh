#!/usr/bin/env bash
# 变异验证：ensurePartitionAutovacuumSchema 的 reloptions 守卫（M82~M86）
# 规则：还原一律用 cp 文件副本，禁止 git checkout --（会静默丢弃未提交工作）
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestEnsurePartitionAutovacuum'
T=db/db.go
BAK="$(mktemp -d)/pav.db.go"
cp "$T" "$BAK"
restore() { cp "$BAK" "$T"; }
trap restore EXIT

GUARD="AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY["
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
  if go test ./db/ -run "$GATE" -count=1 >/tmp/mutpav.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
  else
    echo "  PASS 门转红："
    grep -E -- '    --- FAIL: TestEnsure' /tmp/mutpav.out | head -3
  fi
  restore
  if [ "$(md5 -q "$T")" != "$BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

mutate "M82 只删掉 hot 表循环的守卫（分区循环仍锁）" "
old = '''		          AND (c.relname LIKE '%\\_hot' ESCAPE '\\\\'
		            OR c.relname = 'credential_probe_model_log')
		          -- Skip tables whose reloptions already match. ALTER TABLE SET
		          -- takes ACCESS EXCLUSIVE even when it changes nothing, so an
		          -- unguarded loop locks every hot table on every boot.
		          -- Measured on 252 production: 23 hot tables + 40 partitions =
		          -- 63 exclusive locks per start, all already correct.
		          AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[
		              'autovacuum_enabled=true',
		              'autovacuum_vacuum_scale_factor=0.05',
		              'autovacuum_vacuum_threshold=10',
		              'autovacuum_analyze_scale_factor=0.02',
		              'autovacuum_analyze_threshold=50'])'''
new = '''		          AND (c.relname LIKE '%\\_hot' ESCAPE '\\\\'
		            OR c.relname = 'credential_probe_model_log')'''
assert t.count(old) == 1, 'M82 锚点没对上'
t = t.replace(old, new)
"

mutate "M83 只删掉分区循环的守卫（hot 表循环仍锁）" "
old = '''		          AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[
		              'autovacuum_enabled=true',
		              'autovacuum_vacuum_scale_factor=0.05',
		              'autovacuum_vacuum_threshold=10',
		              'autovacuum_analyze_scale_factor=0.02',
		              'autovacuum_analyze_threshold=50'])
		    LOOP
		        BEGIN
		            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
		            applied := applied + 1;
		        EXCEPTION WHEN others THEN
		            RAISE NOTICE 'skip autovacuum partition %: %', r.relname, SQLERRM;'''
new = '''		    LOOP
		        BEGIN
		            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
		            applied := applied + 1;
		        EXCEPTION WHEN others THEN
		            RAISE NOTICE 'skip autovacuum partition %: %', r.relname, SQLERRM;'''
assert t.count(old) == 1, 'M83 锚点没对上'
t = t.replace(old, new)
"

mutate "M84 把包含运算符改成相等（reloptions 有其它项就永不匹配 ⇒ 守卫静默失效）" "
t = t.replace('''AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[''',
              '''AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) = ARRAY[''')
assert '@>' not in t, 'M84 锚点没对上'
"

mutate "M85 去掉 COALESCE（reloptions 为 NULL 的表会被 WHERE 过滤掉、永不被配置）" "
t = t.replace('''AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[''',
              '''AND NOT (c.reloptions @> ARRAY[''')
assert 'COALESCE(c.reloptions' not in t, 'M85 锚点没对上'
"

mutate "M86 守卫清单少查一项（缺该项的表会被跳过、永不修复）" "
old = '''		              'autovacuum_analyze_scale_factor=0.02',
		              'autovacuum_analyze_threshold=50'])'''
new = '''		              'autovacuum_analyze_scale_factor=0.02'])'''
assert t.count(old) == 2, 'M86 锚点没对上'
t = t.replace(old, new)
"

echo
if [ "$fail" -eq 0 ]; then echo "===== 全部变异均被门抓到 ====="; else echo "===== 存在无牙判据或还原失败 ====="; fi
exit $fail
