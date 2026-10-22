#!/bin/bash
# =====================================================================
# §143.81 生产只读执行器（★ 取【真实】退出码 + 完整输出落盘）
#
# ★★★ 为什么需要它：
#   ★ `bash lgwp-prod.sh x.sql 900 | tail -40` 之后的 `$?` 是【tail】的退出码
#     ⇒ ⇒ 一条真报 `syntax error at or near "UNION"` 的脚本也会被判成 rc=0
#     ⇒ ⇒ ★§143.80 已经在门 `TestSection14380ProdExitCodeMustBeReal` 里钉死这条
#   ★ 本脚本用 `set -o pipefail` + 显式 `$?` 捕获 psql 的真实退出码
#   ★ 且【不截断输出】⇒ ⇒ 完整日志落盘（★ 供 SNAPSHOT 行被后续差分器读到）
#
# 用法：
#   bash scripts/audit/run_prod_sql.sh <sql文件> <超时秒> [输出前缀]
#
#   例（★ 库侧经 154 跳板 ⇒ 252 共享库）：
#   bash scripts/audit/run_prod_sql.sh scripts/audit/verify_l3_leg_value.sql 900 /private/tmp/p14381/snap_a
#
#   ★ 长窗口增量复核（★ 两次运行 + 差分）：
#   bash scripts/audit/run_prod_sql.sh scripts/audit/verify_l3_leg_value.sql 900 /private/tmp/p14381/snap_a
#   ...（等 N 分钟）...
#   bash scripts/audit/run_prod_sql.sh scripts/audit/verify_l3_leg_value.sql 900 /private/tmp/p14381/snap_b
#   python3 scripts/audit/diff_l3_snapshots.py /private/tmp/p14381/snap_a.log /private/tmp/p14381/snap_b.log
#
# ★ 本脚本【只读】：它只调 lgwp-prod.sh（生产只读护栏）⇒ ⇒ 不做任何写操作。
# =====================================================================
set -uo pipefail

SQL="${1:?用法: run_prod_sql.sh <sql文件> <超时秒> [输出前缀]}"
TMO="${2:-900}"
PREFIX="${3:-/private/tmp/lgwp_prod_snapshot}"

if [[ ! -f "$SQL" ]]; then
  echo "run_prod_sql.sh: SQL 文件不存在: $SQL" >&2
  exit 2
fi
if [[ ! -x "$HOME/.mavis-tools/lgwp-prod.sh" && ! -f "$HOME/.mavis-tools/lgwp-prod.sh" ]]; then
  echo "run_prod_sql.sh: 找不到生产只读护栏 ~/.mavis-tools/lgwp-prod.sh" >&2
  exit 2
fi

OUT="${PREFIX}.out"
ERR="${PREFIX}.err"
LOG="${PREFIX}.log"

bash "$HOME/.mavis-tools/lgwp-prod.sh" "$SQL" "$TMO" >"$OUT" 2>"$ERR"
RC=$?

# ★ 合并 stdout+stderr（★ NOTICE / ERROR 都走 stderr ⇒ 合并后 SNAPSHOT 行才完整）
cat "$OUT" "$ERR" >"$LOG"

ERRCNT=$(grep -cE '^psql.*ERROR|^psql.*FATAL' "$ERR" 2>/dev/null || true)
: "${ERRCNT:=0}"

echo "===== REAL_PSQL_RC=${RC} ====="
echo "===== STDERR_ERROR_LINES=${ERRCNT} ====="
echo "===== FULL_LOG=${LOG} ====="
echo "===== SNAPSHOT 行（★ 供 diff_l3_snapshots.py 做差）====="
grep -E 'SNAPSHOT \{' "$LOG" | tail -2 || true
if [[ "${ERRCNT}" != "0" ]]; then
  echo "===== STDERR 尾部 ====="
  tail -20 "$ERR"
fi

exit "${RC}"
