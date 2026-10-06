#!/usr/bin/env bash
# 变异验证：db/goal_client_signal_schema.go 的 session_summaries 目录短路守卫
# 规则：还原一律用 cp 文件副本，禁止 git checkout --
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestEnsureGoalClientSignalSchema_ShortCircuitsSessionSummariesDDL'
BAK="$(mktemp -d)/mv_gcs_guard"
mkdir -p "$BAK/db"
TARGET="db/goal_client_signal_schema.go"
# M117 mutates db.go (the call site), so both files are backed up and both are
# restored between rounds — otherwise restore() leaves db.go mutated and the
# next round starts from an unknown baseline.
TARGET2="db/db.go"
cp "$TARGET" "$BAK/$TARGET"
cp "$TARGET2" "$BAK/$TARGET2"

restore() {
  cp "$BAK/$TARGET" "$TARGET"
  cp "$BAK/$TARGET2" "$TARGET2"
}
trap restore EXIT

fail=0

mutate() {
  local name="$1" pyexpr="$2" expect="${3:-}"
  echo "----------------- $name -----------------"
  restore
  MD5_BEFORE=$(md5 -q "$TARGET")
  MD5_BEFORE2=$(md5 -q "$TARGET2")
  python3 - "$TARGET" <<PY
import sys
p = sys.argv[1]
t = open(p).read()
$pyexpr
open(p, 'w').write(t)
PY
  if diff -q "$BAK/$TARGET" "$TARGET" >/dev/null && \
     diff -q "$BAK/$TARGET2" "$TARGET2" >/dev/null; then
    echo "  FAIL 变异没施上（字节没变）—— 量具问题"; fail=1; return
  fi
  echo "  变异已施上（$(( $(diff "$BAK/$TARGET" "$TARGET" | grep -c '^[<>]') + $(diff "$BAK/$TARGET2" "$TARGET2" | grep -c '^[<>]') )) 行差异）"
  if go test ./db/ -run "$GATE" -count=1 >/tmp/mut_gcs.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestEnsureGoalClientSignal' /tmp/mut_gcs.out | head -12
  else
    if grep -qE '\[build failed\]|build constraints|undefined:|syntax error' /tmp/mut_gcs.out; then
      echo "  FAIL 门红了，但原因是**包编译失败**——变异没写出合法 Go，不是判据有牙"
      head -5 /tmp/mut_gcs.out | sed 's/^/      /'
      fail=1; restore; return
    fi
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestEnsureGoalClientSignal' /tmp/mut_gcs.out | head -6
    if [ -n "$expect" ] && ! grep -qE -- "--- FAIL: TestEnsureGoalClientSignalSchema_ShortCircuitsSessionSummariesDDL/$expect" /tmp/mut_gcs.out; then
      echo "  FAIL 转红原因不是预期的 $expect"; fail=1
    fi
  fi
  restore
  if [ "$(md5 -q "$TARGET")" != "$MD5_BEFORE" ] || \
     [ "$(md5 -q "$TARGET2")" != "$MD5_BEFORE2" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

# M108 表改名 —— §10.98.4 的 M97 同形：长名包含短名，裸 Contains 挡不住
mutate "M108 表改名 session_summaries → session_summaries_v2" "
t = t.replace('ALTER TABLE public.session_summaries\n', 'ALTER TABLE public.session_summaries_v2\n')
t = t.replace('ON public.session_summaries(tenant_id, parent_session_key)', 'ON public.session_summaries_v2(tenant_id, parent_session_key)')
" ddl_is_a_separate_constant

# M109 索引改名 —— idx_session_summaries_parent → _disabled 后缀
mutate "M109 索引改名 idx_session_summaries_parent → _disabled" "
t = t.replace('idx_session_summaries_parent', 'idx_session_summaries_parent_disabled')
" ddl_is_a_separate_constant

# M110 守卫恒假 —— 位置仍在 DDL 之前，但什么也不挡
mutate "M110 守卫恒假（false && ...）" "
t = t.replace('if d.goalClientSignalCurrent(ctx) {', 'if false && d.goalClientSignalCurrent(ctx) {')
" guard_precedes_the_ddl_and_drives_control_flow

# M111 守卫结果被丢弃 —— 探测了但无条件执行 DDL
mutate "M111 守卫结果被丢弃（探测后仍无条件执行）" "
t = t.replace('''if d.goalClientSignalCurrent(ctx) {
		return nil
	}
''', '''d.goalClientSignalCurrent(ctx)
''')
" guard_precedes_the_ddl_and_drives_control_flow

# M112 只守 goal_sessions，丢掉 session_summaries —— 917 MB 那半边重新裸奔
mutate "M112 守卫丢掉 session_summaries 一半" "
t = t.replace('''	if !d.columnsAllPresent(ctx, \"session_summaries\", goalClientSignalSummaryColumns) {
		return false
	}
''', '')
" guard_covers_both_tables_and_the_index

# M113 守卫查错索引名 —— 合法 Go（不删块，否则 slog 变未使用会编译失败），
# 但真实索引再也匹配不上，守卫对缺索引的库会误判为「已就位」
mutate "M113 守卫查错索引名（真实索引再也匹配不上）" "
t = t.replace(\"WHERE schemaname='public' AND indexname='idx_session_summaries_parent'\",
              \"WHERE schemaname='public' AND indexname='idx_session_summaries_parent_v2'\")
" guard_covers_both_tables_and_the_index

# M114 探测出错时 return true —— 探测失败被读成「已就位」，DDL 永久跳过
mutate "M114 探测错误分支返回 true（不安全方向）" "
t = t.replace('applying DDL\", \"error\", err)\n\t\treturn false', 'applying DDL\", \"error\", err)\n\t\treturn true')
" probe_failure_falls_through_to_the_ddl

# M115 守卫清单少一列 —— 列仍在 DDL 里但不再被守，漂移无人察觉
mutate "M115 守卫清单丢掉 sub_agents_pending" "
t = t.replace('\t\t\"sub_agents_pending\",\n', '')
" column_lists_match_the_ddl_exactly

# M116 DDL 少加一列 —— 守卫仍守着它，但库再也拿不到这列
mutate "M116 DDL 丢掉 handoff_reason 这一列" "
t = t.replace(\",\n\t\t\tADD COLUMN IF NOT EXISTS handoff_reason VARCHAR(64) DEFAULT '';\", ';')
" column_lists_match_the_ddl_exactly

# M117 从启动序列里摘掉调用（合法 Go，不用不存在的名字）
mutate "M117 ensureGoalClientSignalSchema 不再被调用" "
import re
dbgo = 'db/db.go'
s = open(dbgo).read()
s = s.replace('if err := db.ensureGoalClientSignalSchema(migCtx); err != nil {', 'if err := error(nil); err != nil {')
open(dbgo,'w').write(s)
t = t
" still_wired_into_the_startup_sequence

restore
echo
if [ "$fail" -eq 0 ]; then
  echo "==== 全部变异均按预期转红 ===="
else
  echo "==== 存在未按预期转红的变异 ===="
fi
exit "$fail"