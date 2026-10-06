#!/usr/bin/env bash
# 变异验证：db/db.go 里 ensureProviderModelsCanonicalClearedAt 的目录短路守卫
# 规则：还原一律用 cp 文件副本，禁止 git checkout --
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestEnsureProviderModelsCanonicalClearedAt_ShortCircuitsDDL'
BAK="$(mktemp -d)/mv_pmcc_guard"
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
  if go test ./db/ -run "$GATE" -count=1 >/tmp/mut_pmcc.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestEnsureProviderModelsCanonical' /tmp/mut_pmcc.out | head -12
  else
    if grep -qE '\[build failed\]|build constraints|undefined:|syntax error' /tmp/mut_pmcc.out; then
      echo "  FAIL 门红了，但原因是**包编译失败**——变异没写出合法 Go，不是判据有牙"
      head -5 /tmp/mut_pmcc.out | sed 's/^/      /'
      fail=1; restore; return
    fi
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestEnsureProviderModelsCanonical' /tmp/mut_pmcc.out | head -6
    if [ -n "$expect" ] && ! grep -qE -- "--- FAIL: TestEnsureProviderModelsCanonicalClearedAt_ShortCircuitsDDL/$expect" /tmp/mut_pmcc.out; then
      echo "  FAIL 转红原因不是预期的 $expect"; fail=1
    fi
  fi
  restore
  if [ "$(md5 -q "$TARGET")" != "$MD5_BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

# M118 表改名 —— 裸 Contains("provider_models") 会被 _v2 绕过（§10.98.4 的 M97）
mutate "M118 表改名 provider_models → provider_models_v2" "
t = t.replace('ALTER TABLE public.provider_models\n', 'ALTER TABLE public.provider_models_v2\n')
t = t.replace('COMMENT ON COLUMN public.provider_models.canonical_cleared_at IS', 'COMMENT ON COLUMN public.provider_models_v2.canonical_cleared_at IS')
" ddl_is_a_separate_constant_without_the_stamp

# M119 列改名 —— canonical_cleared_at → canonical_cleared_at_v2
mutate "M119 列改名 canonical_cleared_at → _v2" "
t = t.replace('canonical_cleared_at', 'canonical_cleared_at_v2')
" ddl_is_a_separate_constant_without_the_stamp

# M120 stamp 被塞进被守卫的 DDL —— DDL 被跳过时 stamp 也一起没了
# 纯文本插入进 const（const 就是字符串，不影响编译）
mutate "M120 stamp 被并入被守卫的 DDL 常量" "
t = t.replace('显式重新关联时置回 NULL。\';\`',
              '显式重新关联时置回 NULL。;\n\n\t\tINSERT INTO public.schema_migrations (version, description) VALUES (\'693\', \'merged\') ON CONFLICT (version) DO NOTHING;\`')
" ddl_is_a_separate_constant_without_the_stamp

# M121 守卫恒假 —— 位置仍在 DDL 之前，但什么也不挡
mutate "M121 守卫恒假（false && ...）" "
t = t.replace('if d.providerModelsCanonicalClearedAtCurrent(ctx) {', 'if false && d.providerModelsCanonicalClearedAtCurrent(ctx) {')
" guard_precedes_the_ddl_and_drives_control_flow

# M122 守卫结果被丢弃 —— 探测了但无条件执行 DDL
mutate "M122 守卫结果被丢弃（探测后仍无条件执行）" "
t = t.replace('if d.providerModelsCanonicalClearedAtCurrent(ctx) {', 'if d.providerModelsCanonicalClearedAtCurrent(ctx); true {')
" guard_precedes_the_ddl_and_drives_control_flow

# M123 只守列、不守注释 —— COMMENT ON COLUMN 仍每次启动取 ACCESS EXCLUSIVE
mutate "M123 守卫丢掉注释检查" "
t = t.replace('AND col_description(a.attrelid, a.attnum) IS NOT NULL', 'AND TRUE')
" guard_covers_the_comment_as_well_as_the_column

# M124 探测出错时 return true —— 探测失败被读成「已就位」，DDL 永久跳过
mutate "M124 探测错误分支返回 true（不安全方向）" "
t = t.replace('applying DDL\", \"error\", err)\n\t\treturn false', 'applying DDL\", \"error\", err)\n\t\treturn true')
" probe_failure_falls_through_to_the_ddl

# M125 守卫命中时不再写 stamp —— 迁移台账缺一条
mutate "M125 守卫命中分支不再执行 stamp" "
t = t.replace('''		if _, err := d.pool.Exec(ctx, providerModelsCanonicalClearedAtStamp); err != nil {
			return fmt.Errorf(\"ensure provider_models canonical_cleared_at: %w\", err)
		}
		return nil''', '''		return nil''')
" stamp_runs_on_both_paths

# M126 stamp 被改成非幂等 —— ON CONFLICT 消失，每次启动都插一行
mutate "M126 stamp 丢掉 ON CONFLICT（不再幂等）" "
t = t.replace('ON CONFLICT (version) DO NOTHING;', ';')
" stamp_runs_on_both_paths

# M127 从启动序列里摘掉调用（合法 Go，不用不存在的名字）
mutate "M127 ensureProviderModelsCanonicalClearedAt 不再被调用" "
t = t.replace('if err := db.ensureProviderModelsCanonicalClearedAt(migCtx); err != nil {', 'if err := error(nil); err != nil {')
" still_wired_into_the_startup_sequence

restore
echo
if [ "$fail" -eq 0 ]; then
  echo "==== 全部变异均按预期转红 ===="
else
  echo "==== 存在未按预期转红的变异 ===="
fi
exit "$fail"