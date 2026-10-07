#!/usr/bin/env bash
# 变异验证：db/maas_schema.go 的 credits_charged 目录短路守卫
# 规则：还原一律用 cp 文件副本，禁止 git checkout --（会静默丢弃未提交工作）
# 规则：python 用引用 heredoc <<'PY'
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestEnsureMaasSchema_ShortCircuitsRequestLogsDDL'
BAK="$(mktemp -d)/mv_maas_guard"
mkdir -p "$BAK/db"
TARGET="db/maas_schema.go"
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
  if go test ./db/ -run "$GATE" -count=1 >/tmp/mut_maas.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestEnsureMaasSchema' /tmp/mut_maas.out | head -12
  else
    if grep -qE '\[build failed\]|build constraints|undefined:|syntax error' /tmp/mut_maas.out; then
      echo "  FAIL 门红了，但原因是**包编译失败**——这不是判据有牙，是变异没写出合法 Go"
      head -5 /tmp/mut_maas.out | sed 's/^/      /'
      fail=1
      restore
      return
    fi
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestEnsureMaasSchema' /tmp/mut_maas.out | head -6
    if [ -n "$expect" ] && ! grep -qE -- "--- FAIL: TestEnsureMaasSchema_ShortCircuitsRequestLogsDDL/$expect" /tmp/mut_maas.out; then
      echo "  FAIL 转红原因不是预期的 $expect"; fail=1
    fi
  fi
  restore
  if [ "$(md5 -q "$TARGET")" != "$MD5_BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

# M86 把守卫条件改成永假 —— 位置仍在 DDL 之前，但 DDL 会无条件执行（锁回来）
mutate "M86 守卫条件恒假（&& false，语法合法）" "
t = t.replace('if d.maasRequestLogsCurrent(ctx) {', 'if d.maasRequestLogsCurrent(ctx) && false {')
" ddl_is_applied_only_after_the_guard_misses

# M87 探测失败时返回 true —— 真缺列的库会被静默跳过、再也补不上列
mutate "M87 探测失败返回 true（危险方向）" "
t = t.replace('return false', 'return true')
" failed_probe_falls_through_to_the_ddl

# M88 守卫只看列、不看索引 —— 缺索引的库会跳过 DDL 再也拿不到索引
mutate "M88 守卫漏检索引" "
t = t.replace('idx_request_logs_credits_charged', 'idx_never_created_by_this_guard')
" guard_checks_both_the_column_and_the_index

# M89 探测函数里跑 DDL —— 探测本身就锁表，等于没守卫
mutate "M89 探测函数里跑 DDL" "
t = t.replace('''	\`).Scan(&missing)''', '''	\`).Scan(&missing)
	_, _ = d.pool.Exec(ctx, \"ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS probe_side_effect BIGINT\")
	_ = missing''')
" probe_must_not_run_ddl

# M90 把 pricing 表的 DDL 塞进被守卫的常量 —— 守卫一命中，pricing 表也不再被建
mutate "M90 被守卫常量里混入 pricing 表" "
t = t.replace('''const maasRequestLogsDDL = \`
''', '''const maasRequestLogsDDL = \`
		CREATE TABLE IF NOT EXISTS maas_settings (id INT PRIMARY KEY DEFAULT 1);
''')
" request_logs_ddl_is_a_separate_constant

# M91 守卫结果被丢弃 —— 位置正确但控制流断了
mutate "M91 守卫结果被丢弃" "
t = t.replace('if d.maasRequestLogsCurrent(ctx) {', '_ = d.maasRequestLogsCurrent(ctx); if false {')
" ddl_is_applied_only_after_the_guard_misses

echo "=================================================="
if [ "$fail" -eq 0 ]; then
  echo "变异验证：6/6 全部按预期转红"
else
  echo "变异验证：存在未按预期转红或还原失败的条目"
fi
exit $fail