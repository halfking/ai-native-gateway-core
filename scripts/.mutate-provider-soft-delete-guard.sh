#!/usr/bin/env bash
# 变异验证：db/db.go 里 ensureProviderSoftDelete 的 providers 目录短路守卫
# 规则：还原一律用 cp 文件副本，禁止 git checkout --
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestEnsureProviderSoftDelete_ShortCircuitsProvidersDDL'
BAK="$(mktemp -d)/mv_psd_guard"
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
  if go test ./db/ -run "$GATE" -count=1 >/tmp/mut_psd.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestEnsureProviderSoftDelete' /tmp/mut_psd.out | head -12
  else
    if grep -qE '\[build failed\]|build constraints|undefined:|syntax error' /tmp/mut_psd.out; then
      echo "  FAIL 门红了，但原因是**包编译失败**——变异没写出合法 Go，不是判据有牙"
      head -5 /tmp/mut_psd.out | sed 's/^/      /'
      fail=1; restore; return
    fi
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestEnsureProviderSoftDelete' /tmp/mut_psd.out | head -6
    if [ -n "$expect" ] && ! grep -qE -- "--- FAIL: TestEnsureProviderSoftDelete_ShortCircuitsProvidersDDL/$expect" /tmp/mut_psd.out; then
      echo "  FAIL 转红原因不是预期的 $expect"; fail=1
    fi
  fi
  restore
  if [ "$(md5 -q "$TARGET")" != "$MD5_BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

# M98 索引改名 —— Contains("idx_providers_live") 会被 _disabled 后缀绕过？
mutate "M98 索引改名 idx_providers_live → idx_providers_live_disabled" "
t = t.replace('idx_providers_live', 'idx_providers_live_disabled')
" providers_ddl_is_a_separate_constant

# M99 表改名 —— §10.98.4 的 M97 同形：providers → providers_disabled
mutate "M99 表改名 providers → providers_disabled（DDL 侧）" "
t = t.replace('ALTER TABLE providers\n', 'ALTER TABLE providers_disabled\n')
t = t.replace('ON providers (id)', 'ON providers_disabled (id)')
" providers_ddl_is_a_separate_constant

# M100 守卫方向写反 —— 列/索引缺失时反而跳过 DDL，真缺列的库永远补不上
mutate "M100 守卫方向写反（去掉 !）" "
t = t.replace('if !d.providerSoftDeleteCurrent(ctx) {', 'if d.providerSoftDeleteCurrent(ctx) {')
" guard_precedes_the_ddl_and_its_result_drives_control_flow

# M101 位置不是控制流 —— 守卫仍在 DDL 之前，但恒假
mutate "M101 守卫恒假（false && ...）" "
t = t.replace('if !d.providerSoftDeleteCurrent(ctx) {', 'if false && d.providerSoftDeleteCurrent(ctx) {')
" guard_precedes_the_ddl_and_its_result_drives_control_flow

# M102 守卫结果被丢弃 —— 探测了但无条件执行 DDL
mutate "M102 守卫结果被丢弃（探测后仍无条件执行）" "
t = t.replace('''if !d.providerSoftDeleteCurrent(ctx) {
		if _, err := d.pool.Exec(ctx, providerSoftDeleteDDL); err != nil {
			return err
		}
	}''', '''d.providerSoftDeleteCurrent(ctx)
	if _, err := d.pool.Exec(ctx, providerSoftDeleteDDL); err != nil {
		return err
	}''')
" guard_precedes_the_ddl_and_its_result_drives_control_flow

# M103 只守列不守索引 —— 有列但缺 idx_providers_live 的库会被永久跳过
mutate "M103 守卫只查列、丢掉索引检查" "
t = t.replace('''		  (SELECT count(*) FROM (VALUES ('idx_providers_live')) AS want(name)
		   WHERE NOT EXISTS (
		       SELECT 1 FROM pg_indexes
		        WHERE schemaname='public' AND indexname = want.name))''', '')
t = t.replace('''		  (SELECT count(*) FROM (VALUES ('deleted_at')) AS want(name)
		   WHERE to_regclass('public.providers') IS NOT NULL
		     AND NOT EXISTS (
		       SELECT 1 FROM information_schema.columns
		        WHERE table_schema='public' AND table_name='providers'
		          AND column_name = want.name))
		  +''', '''		  (SELECT count(*) FROM (VALUES ('deleted_at')) AS want(name)
		   WHERE to_regclass('public.providers') IS NOT NULL
		     AND NOT EXISTS (
		       SELECT 1 FROM information_schema.columns
		        WHERE table_schema='public' AND table_name='providers'
		          AND column_name = want.name))''')
" guard_covers_column_and_index_the_ddl_creates

# M104 探测出错时 return true —— 探测失败被读成「已就位」，DDL 永久跳过
mutate "M104 探测错误分支返回 true（不安全方向）" "
t = t.replace('applying DDL\", \"error\", err)\n\t\treturn false',
              'applying DDL\", \"error\", err)\n\t\treturn true')
" guard_covers_column_and_index_the_ddl_creates

# M105 credentials 约束批次被删 —— 守卫只覆盖 providers 时，另半边被静默吞掉
mutate "M105 credentials 约束批次被移除" "
t = t.replace(\"credentials_status_check\", \"renamed_status_check\")
" credentials_constraint_batch_still_runs

# M106 credentials 约束批次被塞进被守卫的常量 —— 两个守卫混成一个，语义不可推理
mutate "M106 credentials 约束批次被并入 providerSoftDeleteDDL" "
t = t.replace('''const providerSoftDeleteDDL = \`
		ALTER TABLE providers
		    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;''', '''const providerSoftDeleteDDL = \`
		DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'credentials_status_check') THEN END IF; END \$\$;
		ALTER TABLE providers
		    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;''')
" providers_ddl_is_a_separate_constant

# M107 整个函数从启动序列里摘掉
mutate "M107 ensureProviderSoftDelete 不再被调用" "
t = t.replace('if err := db.ensureProviderSoftDelete(migCtx); err != nil {', 'if err := error(nil); err != nil {')
" still_wired_into_the_startup_sequence

restore
echo
if [ "$fail" -eq 0 ]; then
  echo "==== 全部变异均按预期转红 ===="
else
  echo "==== 存在未按预期转红的变异 ===="
fi
exit "$fail"