#!/usr/bin/env bash
# 变异验证：db/db.go 里 ensureQualityFixModeSchema 的 providers 目录短路守卫
# 规则：还原一律用 cp 文件副本，禁止 git checkout --
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestEnsureQualityFixModeSchema_ShortCircuitsProvidersDDL'
BAK="$(mktemp -d)/mv_qfm_guard"
mkdir -p "$BAK/db"
TARGET="db/db.go"
cp "$TARGET" "$BAK/$TARGET"

restore() { cp "$BAK/$TARGET" "$TARGET"; }
trap restore EXIT

fail=0

mutate() {
  local name="$1" pyexpr="$2" expect="${3:-}"
  echo "----------------- $name -----------------"
  restore
  MD5_BEFORE=$(md5 -q "$TARGET")
  python3 - "$TARGET" <<PY
import sys
p = sys.argv[1]
t = open(p).read()
$pyexpr
open(p, 'w').write(t)
PY
  if diff -q "$BAK/$TARGET" "$TARGET" >/dev/null; then
    echo "  FAIL 变异没施上（字节没变）—— 量具问题"; fail=1; return
  fi
  echo "  变异已施上（$(diff "$BAK/$TARGET" "$TARGET" | grep -c '^[<>]') 行差异）"
  if go test ./db/ -run "$GATE" -count=1 >/tmp/mut_qfm.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestEnsureQualityFixMode' /tmp/mut_qfm.out | head -12
  else
    if grep -qE '\[build failed\]|build constraints|undefined:|syntax error' /tmp/mut_qfm.out; then
      echo "  FAIL 门红了，但原因是**包编译失败**——变异没写出合法 Go，不是判据有牙"
      head -5 /tmp/mut_qfm.out | sed 's/^/      /'
      fail=1; restore; return
    fi
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestEnsureQualityFixMode' /tmp/mut_qfm.out | head -6
    if [ -n "$expect" ] && ! grep -qE -- "--- FAIL: TestEnsureQualityFixModeSchema_ShortCircuitsProvidersDDL/$expect" /tmp/mut_qfm.out; then
      echo "  FAIL 转红原因不是预期的 $expect"; fail=1
    fi
  fi
  restore
  if [ "$(md5 -q "$TARGET")" != "$MD5_BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

# M92 守卫方向写反 —— 列缺失时反而跳过 DDL，真缺列的库永远补不上
mutate "M92 守卫方向写反（去掉 !）" "
t = t.replace('if !d.columnsAllPresent(ctx, \"providers\", []string{\"quality_fix_mode\"}) {',
              'if d.columnsAllPresent(ctx, \"providers\", []string{\"quality_fix_mode\"}) {')
" guard_uses_the_repo_helper_and_precedes_the_ddl

# M93 守卫结果被丢弃 —— 位置正确、控制流断了
mutate "M93 守卫结果被丢弃" "
t = t.replace('if !d.columnsAllPresent(ctx, \"providers\", []string{\"quality_fix_mode\"}) {',
              '_ = d.columnsAllPresent(ctx, \"providers\", []string{\"quality_fix_mode\"})\n\tif false {')
" guard_uses_the_repo_helper_and_precedes_the_ddl

# M94 rollup 建表被挪进被守卫的常量 —— 守卫一命中，rollup 表永远建不出来
mutate "M94 rollup 建表挪进被守卫常量" "
t = t.replace('''		        CHECK (quality_fix_mode IN ('off', 'detect_only', 'fix'));\`''',
              '''		        CHECK (quality_fix_mode IN ('off', 'detect_only', 'fix'));
		CREATE TABLE IF NOT EXISTS provider_quality_rollup (provider_id INT NOT NULL);\`''')
" providers_alter_is_a_separate_constant

# M95 守卫查错列名 —— 查一个恒存在的列，守卫永远为真、DDL 再也不执行
mutate "M95 守卫查错列名" "
t = t.replace('[]string{\"quality_fix_mode\"}) {', '[]string{\"quality_fix_mod\"}) {')
" guard_covers_the_column_that_the_ddl_creates

# M96 守卫查错了表（查 rollup 表而不是 providers）
mutate "M96 守卫查错表" "
t = t.replace('d.columnsAllPresent(ctx, \"providers\", []string{\"quality_fix_mode\"})',
              'd.columnsAllPresent(ctx, \"provider_quality_rollup\", []string{\"quality_fix_mode\"})')
" guard_covers_the_column_that_the_ddl_creates

# M97 rollup 表与其索引被整段删除 —— 守卫会把「有列但缺表」的库放过
mutate "M97 删掉 rollup 批次" "
t = t.replace('CREATE TABLE IF NOT EXISTS provider_quality_rollup (', 'CREATE TABLE IF NOT EXISTS provider_quality_rollup_disabled (')
t = t.replace('idx_provider_quality_rollup_bucket', 'idx_provider_quality_rollup_bucket_disabled')
" rollup_batch_is_preserved

echo "=================================================="
if [ "$fail" -eq 0 ]; then
  echo "变异验证：6/6 全部按预期转红"
else
  echo "变异验证：存在未按预期转红或还原失败的条目"
fi
exit $fail