#!/usr/bin/env bash
# 变异验证：migration 838 —— 往月分区只在「从未被分析过」时才 ANALYZE
# 规则：还原一律用 cp 文件副本，禁止 git checkout --（会静默丢弃未提交工作）
# 规则：python 用引用 heredoc <<'PY'，否则 $'_' 会被 bash 当 ANSI-C 引号展开
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestMigration838'
BAK="$(mktemp -d)/mv838"
mkdir -p "$BAK"
FILES=(
  "sql/migrations/startup/838_analyze_skip_frozen_month.sql"
  "sql/migrations/startup/838_analyze_skip_frozen_month.down.sql"
  "sql/objects/functions/analyze_llm_gateway_table_stats_integer.sql"
  "sql/schema/01-schema.sql"
  "installer/cmd/llm-gw-installer/embeddata/startup/838_analyze_skip_frozen_month.sql"
  "installer/cmd/llm-gw-installer/main.go"
  "installer/internal/dbinit/runner.go"
  "sql/schema/installed_startup_migrations.tsv"
)
for f in "${FILES[@]}"; do
  mkdir -p "$BAK/$(dirname "$f")"
  cp "$f" "$BAK/$f"
done

restore() { for f in "${FILES[@]}"; do cp "$BAK/$f" "$f"; done; }
trap restore EXIT

export GUARD='AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))'

fail=0

mutate() {
  local name="$1" target="$2" pyexpr="$3" expect="${4:-}"
  echo "----------------- $name -----------------"
  restore
  MD5_BEFORE=$(md5 -q "$target")
  python3 - "$target" <<PY
import os, sys
p = sys.argv[1]
t = open(p).read()
g = os.environ['GUARD']
$pyexpr
open(p, 'w').write(t)
PY
  # 量具自检：字节没变就是变异没施上，绝不放行
  if diff -q "$BAK/$target" "$target" >/dev/null; then
    echo "  FAIL 变异没施上（字节没变）—— 量具问题"; fail=1; return
  fi
  # diff 不加过滤器：曾因此滤掉真 bug
  echo "  变异已施上（$(diff "$BAK/$target" "$target" | grep -c '^[<>]') 行差异）"
  if go test ./sql/migrations/startup/ -run "$GATE" -count=1 >/tmp/mut838.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestMigration838' /tmp/mut838.out | head -12
  else
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestMigration838' /tmp/mut838.out | head -6
    if [ -n "$expect" ] && ! grep -qE -- "--- FAIL: TestMigration838_AnalyzeSkipFrozenMonth/$expect" /tmp/mut838.out; then
      echo "  FAIL 转红原因不是预期的 $expect"; fail=1
    fi
  fi
  restore
  if [ "$(md5 -q "$target")" != "$MD5_BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

UP=sql/migrations/startup/838_analyze_skip_frozen_month.sql
DOWN=sql/migrations/startup/838_analyze_skip_frozen_month.down.sql
OBJ=sql/objects/functions/analyze_llm_gateway_table_stats_integer.sql

# M78 整条守卫删掉 —— 退回 837 的无条件重分析，838 的全部收益归零
mutate "M78 删掉整条守卫（回到无条件分析）" "$UP" "
t = t.replace(g, '')
" up_adds_never_analyzed_guard_to_partition_loop

# M79 只删 m = 0 OR —— 最危险的简化：当月也不再分析
mutate "M79 去掉 'm = 0 OR'（当月被误跳过）" "$UP" "
t = t.replace('AND (m = 0 OR NOT EXISTS', 'AND (NOT EXISTS')
" up_keeps_current_month_unconditional

# M80 守卫挪到 hot 表分支 —— hot 表持续写入，跳过它会悄悄丢统计新鲜度
mutate "M80 守卫挪到 hot 表分支（分区半失效）" "$UP" "
out, done = [], False
for l in t.splitlines(keepends=True):
    out.append(l)
    if (not done) and 'LIKE' in l and '_hot' in l:
        out.append('          AND NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid)\n')
        done = True
t = ''.join(out).replace(g, '')
" guard_is_attached_to_the_partition_loop_only

# M81 守卫谓词恒真化（starelid = 0）—— 跳过一切往月分区，看起来仍只跑当月
mutate "M81 守卫谓词恒真化（starelid = 0）" "$UP" "
t = t.replace('s.starelid = c.oid', 's.starelid = 0')
" up_adds_never_analyzed_guard_to_partition_loop

# M82 down 忘了撤守卫 —— 回滚后仍跳过往月分区，且看起来「回滚成功」
mutate "M82 down 没撤守卫（回滚不彻底）" "$DOWN" "
out, done = [], False
for l in t.splitlines(keepends=True):
    out.append(l)
    if (not done) and '|| suffix' in l:
        out.append('              ' + g + '\n')
        done = True
t = ''.join(out)
" down_restores_unconditional_analysis

# M83 四处正本只改了迁移、忘了正本 —— 重建出来的库会静默保持旧行为
mutate "M83 正本漏改（fresh install 仍是旧行为）" "$OBJ" "
t = t.replace(g, '')
" mirror_copies_carry_the_guard

# M84 installer 没接线 —— 迁移在重建机上永不执行
mutate "M84 installer 漏接线（重建机不执行 838）" installer/cmd/llm-gw-installer/main.go "
t = t.replace('startup/838_analyze_skip_frozen_month.sql\":', 'startup/838_ANALYZE_skip_frozen_month.sql\":')
" installer_wiring

# M85 迁移登记名打错 —— 门与实际安装清单不平
mutate "M85 迁移登记漏掉 838" sql/schema/installed_startup_migrations.tsv "
t = t.replace('838_analyze_skip_frozen_month.sql', '838_analyze_skip_frozen_mont.sql')
" migration_registry

echo "=================================================="
if [ "$fail" -eq 0 ]; then
  echo "变异验证：8/8 全部按预期转红"
else
  echo "变异验证：存在未按预期转红或还原失败的条目"
fi
exit $fail