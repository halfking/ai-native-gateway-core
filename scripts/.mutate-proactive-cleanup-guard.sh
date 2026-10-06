#!/usr/bin/env bash
# 变异验证：pg17-proactive-empty-table-cleanup.sh 的分区守卫（M69~M71）
# 规则：还原一律用 cp 文件副本，禁止 git checkout --（会静默丢弃未提交工作）
set -uo pipefail
cd "$(dirname "$0")/.."

S=scripts/252-monitor/pg17-proactive-empty-table-cleanup.sh
BAK="$(mktemp -d)/proactive-cleanup.sh"
cp "$S" "$BAK"

restore() { cp "$BAK" "$S"; }
trap restore EXIT

fail=0

mutate() {
  local name="$1" gate="$2" pyexpr="$3"
  echo "----------------- $name -----------------"
  restore
  MD5_BEFORE=$(md5 -q "$S")
  python3 - "$S" <<PY
import sys
p = sys.argv[1]
t = open(p).read()
$pyexpr
open(p, 'w').write(t)
PY
  if diff -q "$BAK" "$S" >/dev/null; then
    echo "  FAIL 变异没施上（字节没变）—— 量具问题"
    fail=1
    return
  fi
  echo "  变异已施上（$(diff "$BAK" "$S" | grep -c '^[<>]') 行差异）"
  if ( cd scripts/ursmcheck && go test -run "$gate" ./... >/tmp/mut.out 2>&1 ); then
    echo "  FAIL 门仍然全绿 —— 判据无牙"
    fail=1
  else
    echo "  PASS 门转红"
    grep -E -- '--- FAIL|proactive_cleanup' /tmp/mut.out | head -3
  fi
  restore
  if [ "$(md5 -q "$S")" != "$MD5_BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

mutate "M69 拆掉 _default 名字兜底" 'ProactiveCleanupNeverDropsPartitionEvenIfFlagSaysPlain' \
"t = t.replace('''  case \"\$tname\" in
    *_default) is_part=1 ;;
  esac
''', '')"

mutate "M70 拆掉 is_partition 行为层结构闸（改永假条件，保留代码形态）" 'ProactiveCleanupNeverDropsPartition$' \
"t = t.replace('''  if [ \"\$is_part\" = \"1\" ]; then''', '''  if [ \"\$is_part\" = \"2\" ]; then''')"

mutate "M71 拆掉 SQL 层 NOT c.relispartition" 'ProactiveCleanupCandidateSQLExcludesPartitions' \
"t = t.replace('  AND NOT c.relispartition\n', '')"

echo
echo "============ 还原自证 ============"
if diff -q "$BAK" "$S" >/dev/null; then
  echo "PASS 脚本已还原为变异前状态"
else
  echo "FAIL 还原失败！"; fail=1
fi
bash -n "$S" && echo "PASS 语法 OK"
exit $fail
