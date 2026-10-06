#!/usr/bin/env bash
# 变异验证：迁移 839 的当月堆分区交接判据
# 规则：还原一律用 cp 文件副本，禁止 git checkout --
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestMigration839_AutovacCurrentMonthHeapHandoff'
BAK="$(mktemp -d)/mv839_guard"
TARGET="sql/migrations/startup/839_autovac_current_month_heap_handoff.sql"
TARGET2="sql/migrations/startup/839_autovac_current_month_heap_handoff.down.sql"

# ★ 备份集必须覆盖**所有**会被变异改到的文件。
#   只备份 up/down 两条时，M136~M138 改的 schema / embeddata / runner.go
#   变异后没人还原 ⇒ 污染工作区。备份按目录层级保留，路径一一对应。
FILES=(
  sql/migrations/startup/839_autovac_current_month_heap_handoff.sql
  sql/migrations/startup/839_autovac_current_month_heap_handoff.down.sql
  sql/schema/01-schema.sql
  installer/cmd/llm-gw-installer/embeddata/startup/839_autovac_current_month_heap_handoff.sql
  installer/internal/dbinit/runner.go
)
for f in "${FILES[@]}"; do
  mkdir -p "$BAK/$(dirname "$f")"
  cp "$f" "$BAK/$f"
done

restore() { for f in "${FILES[@]}"; do cp "$BAK/$f" "$f"; done; }
trap restore EXIT

fail=0
mutate() {
  local name="$1" target="$2" pyexpr="$3" expect="${4:-}"
  echo "----------------- $name -----------------"
  restore
  python3 - "$target" <<PY
import sys
p = sys.argv[1]
t = open(p).read()
$pyexpr
open(p, 'w').write(t)
PY
  if diff -q "$BAK/$target" "$target" >/dev/null 2>&1; then
    echo "  FAIL 变异没施上（字节没变）—— 量具问题"; fail=1; return
  fi
  echo "  变异已施上（${target}，$(diff "$BAK/$target" "$target" | grep -c '^[<>]') 行差异）"
  if go test ./sql/migrations/startup/ -run "$GATE" -count=1 >/tmp/mut839.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestMigration839' /tmp/mut839.out | head -12
  else
    if grep -qE '\[build failed\]|build constraints|undefined:|syntax error' /tmp/mut839.out; then
      echo "  FAIL 门红了，但原因是**包编译失败**——不是判据有牙"
      head -5 /tmp/mut839.out | sed 's/^/      /'
      fail=1; restore; return
    fi
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestMigration839' /tmp/mut839.out | head -6
    if [ -n "$expect" ] && ! grep -q -- "--- FAIL: TestMigration839_AutovacCurrentMonthHeapHandoff/$expect" /tmp/mut839.out; then
      echo "  FAIL 转红原因不是预期的 $expect"; fail=1
    fi
  fi
  restore
  for f in "${FILES[@]}"; do
    cmp -s "$BAK/$f" "$f" || { echo "  FAIL 还原失败（md5 漂移）: $f"; fail=1; }
  done
}

UP="$TARGET"
DOWN="$TARGET2"

# M129 回退成 838 的判据 —— 整个改动等于没做
# ★ 锚点用 regex（\\s+ 容错缩进）：迁移文件与 schema 正本的缩进不同，
#   逐字锚点会静默「没施上」而不是报错。
mutate "M129 回退成 838 判据（m = 0 无条件分析）" "$UP" "
import re
pat = re.compile(r'AND \\(\\s*-- ①[\\s\\S]*?\\n\\s*\\)', re.M)
assert pat.search(t), 'M129 锚点没匹配上'
t = pat.sub('AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))', t, count=1)
" 当月堆分区的交接判据

# M130 丢掉列存保留分支 —— 生产实测列存 autoanalyze_count = 0，交回就永远没人管
mutate "M130 丢掉列存保留分支" "$UP" "
import re
pat = re.compile(r'OR \\(\\s*-- ②[\\s\\S]*?\\n\\s*\\)\\n', re.M)
assert pat.search(t), 'M130 锚点没匹配上'
t = pat.sub('', t, count=1)
" 当月堆分区的交接判据

# M131 丢掉首次覆盖 —— 从未被分析过的分区会被永久跳过
mutate "M131 丢掉首次覆盖（pg_statistic 无行仍被分析）" "$UP" "
import re
pat = re.compile(r'NOT EXISTS \\(SELECT 1 FROM pg_statistic s WHERE s\\.starelid = c\\.oid\\)')
assert pat.search(t), 'M131 锚点没匹配上'
t = pat.sub('false', t, count=1)
" 当月堆分区的交接判据

# M132 scale_factor 的月后缀限定被去掉 —— 往月分区也会被改
mutate "M132 去掉当前月后缀限定（往月也会被改）" "$UP" "
t = t.replace(\"AND c.relname ~ ('_' || cur_suffix || '\$')\", 'AND true')
" 当月堆分区按_pg_am=heap_限定

# M133 heap 限定被去掉 —— 父表/列存也会被 SET
mutate "M133 去掉 heap 限定" "$UP" "
t = t.replace('AND c.relam = heap_oid', 'AND true')
" 当月堆分区按_pg_am=heap_限定

# M134 回滚不再把 scale_factor 交还 0.02
mutate "M134 回滚不再交还 0.02" "$DOWN" "
t = t.replace('apply_llm_gateway_current_month_analyze_scale_factor(0.02)', 'apply_llm_gateway_current_month_analyze_scale_factor(0.005)')
" scale_factor_取_0.005_且带参数可回滚

# M135 回滚里残留 839 的新判据 —— 回滚等于半回滚
mutate "M135 down 里残留 839 新判据" "$DOWN" "
t = t.replace('AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))',
              'AND (NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))')
" down_恢复_838_的函数体

# M136 四处正本之一没同步（改 schema 副本）
mutate "M136 schema 正本之一未同步" "sql/schema/01-schema.sql" "
t = t.replace(\"AND c.relam <> (SELECT oid FROM pg_am WHERE amname = 'heap')\", 'AND true')
" 四处函数正本同步

# M137 没登记进 installer（embeddata 副本与正本不一致）
mutate "M137 embeddata 副本与正本不一致" "installer/cmd/llm-gw-installer/embeddata/startup/839_autovac_current_month_heap_handoff.sql" "
t = t.replace('p_scale_factor numeric DEFAULT 0.005', 'p_scale_factor numeric DEFAULT 0.05')
" 已接线到_installer_与台账

# M138 没登记进 runner / tsv
mutate "M138 runner 未登记 839" "installer/internal/dbinit/runner.go" "
t = t.replace('\t\t\t\"839_autovac_current_month_heap_handoff.sql\",\n', '')
" 已接线到_installer_与台账

restore
echo
if [ "$fail" -eq 0 ]; then
  echo "==== 全部变异均按预期转红 ===="
else
  echo "==== 存在未按预期转红的变异 ===="
fi
exit "$fail"