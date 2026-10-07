#!/usr/bin/env bash
# scripts/run-realdb-gate.sh —— 真库判据的**显式**运行入口（防线 B 常态化）
#
# 为什么不并进 pre-commit-check.sh：
#   真库判据需要 TEST_DATABASE_URL（连的是**真生产库**，不是临时库）。
#   把连生产库的测试塞进每次提交的门禁，等于让每个人的每次提交都有权限
#   打生产库 —— 那是权限问题，不是门禁松紧问题。
#   ⇒ 本脚本**不**被 pre-commit-check.sh 调用；它是**显式登记**的独立命令，
#     需要谁主动跑、或由运维在部署前后跑。
#
# 为什么要它存在（它治的是什么病）：
#   裸 `go test ./bg/` 时，全部 20 个真库判据文件（45 个用例）在
#   TEST_DATABASE_URL 未设置时**静默 t.Skip**。这在语法上完全合规，
#   输出里只是一行 `--- SKIP`，与「跑过且通过」混在同一个 `ok` 里。
#   ⇒ 目标里写的「在裸 go test 下静默 SKIP，是本仓两层测试设计的正常形态」
#     描述准确，但代价是：**没人能凭一次 go test 说出真库判据到底跑没跑**。
#   本脚本的职责就是让「跑没跑」变成一个**独立的、不会被误读为通过的读数**。
#
# 用法：
#   TEST_DATABASE_URL='postgres://...' scripts/run-realdb-gate.sh
#   scripts/run-realdb-gate.sh --list        # 只列出会跑哪些，不连库
#
# 退出码：
#   0  全部真库判据真跑且通过
#   1  有判据 FAIL
#   2  DSN 未设置（**不是通过** —— 这是「没跑成」，必须与「红了」分开表述）

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# 真库判据的过滤表达式：文件名单独列在下面，不靠命名通配，
# 因为漏一个文件名 = 静默少跑一条判据，而那种漏是看不见的。
REALDB_FILES=(
  analyze_throttle_840_realdb_test.go
  baseline_price_ssot_parity_realdb_test.go
  baseline_price_sync_realdb_test.go
  capability_evidence_realdb_test.go
  default_residue_realdb_test.go
  health_check_optional_skip_realdb_test.go
  hot_ts_column_realdb_test.go
  modality_due_targets_realdb_test.go
  modality_evidence_realdb_test.go
  modality_rollup_realdb_test.go
  modality_verify_loop_realdb_test.go
  modality_zero_egress_realdb_test.go
  partition_retention_841_realdb_test.go
  pricing_plan_stale_realdb_test.go
  project_backfill_worker_realdb_test.go
  recorded_cost_negative_realdb_test.go
  report_rollup_worker_realdb_test.go
  sql_audit_realdb_test.go
  stats_ground_truth_gap_realdb_test.go
  ursm_830_realdb_test.go
)

# ⚠ 名单必须与磁盘**逐字一致**。任何一���不存在的文件名都会被 go test 静默忽略
#   （-run 对不存在的文件不报错），所以下面先做存在性自检。
missing=0
for f in "${REALDB_FILES[@]}"; do
  if [[ ! -f "bg/$f" ]]; then
    printf 'run-realdb-gate: 名单里的 bg/%s 在磁盘上不存在\n' "$f" >&2
    missing=$((missing + 1))
  fi
done
if (( missing > 0 )); then
  printf 'run-realdb-gate: %d 个文件不存在，名单已腐化，拒绝运行（否则这些判据会静默消失）\n' "$missing" >&2
  exit 1
fi

# 反向自检：磁盘上有而名单里没有的 realdb 文件。少了它 = 有人新写了判据
# 却没登记，而「没登记」的默认表现就是不跑。
on_disk=$(cd "$REPO_ROOT/bg" && ls -1 *_realdb_test.go 2>/dev/null | sort)
in_list=$(printf '%s\n' "${REALDB_FILES[@]}" | sort)
unlisted=$(comm -23 <(printf '%s\n' "$on_disk") <(printf '%s\n' "$in_list"))
if [[ -n "$unlisted" ]]; then
  printf 'run-realdb-gate: 以下真库判据文件未登记进本脚本，**它们不会被跑**：\n' >&2
  printf '  %s\n' "$unlisted" >&2
  printf '  新增判据必须登记到 REALDB_FILES，否则等于没写。\n' >&2
  exit 1
fi

if [[ "${1:-}" == "--list" ]]; then
  printf 'run-realdb-gate: 登记真库判据 %d 个文件（未连库）\n' "${#REALDB_FILES[@]}"
  printf '%s\n' "${REALDB_FILES[@]}" | sed 's/^/  /'
  exit 0
fi

DSN="${TEST_DATABASE_URL:-${TEST_DB_URL:-}}"
if [[ -z "$DSN" ]]; then
  printf 'run-realdb-gate: TEST_DATABASE_URL / TEST_DB_URL 未设置\n' >&2
  printf '  ⇒ **没跑成**（不是通过）。裸 go test 下这些判据会静默 SKIP，\n' >&2
  printf '    引用它们的状态时必须写 PASS=N/SKIP=M，不能只写「全绿」。\n' >&2
  exit 2
fi

printf 'run-realdb-gate: %d 个真库判据文件，连库执行中…\n' "${#REALDB_FILES[@]}"

# -v：SKIP 必须逐条可见。不用 -v 的话 SKIP 行会被 go test 折叠掉，
# 而「折叠掉的 SKIP」与「通过」在输出上无法区分 —— 那正是本脚本要治的病。
out=$(go test ./bg/ -count=1 -v -timeout=900s 2>&1)
rc=$?

passed=$(printf '%s\n' "$out" | grep -cE '^--- PASS')
failed=$(printf '%s\n' "$out" | grep -cE '^--- FAIL')
skipped=$(printf '%s\n' "$out" | grep -cE '^--- SKIP')

printf '\nrun-realdb-gate: 真库判据读数 —— PASS=%d / FAIL=%d / SKIP=%d\n' "$passed" "$failed" "$skipped"
if (( failed > 0 )); then
  printf '\n失败明细：\n'
  printf '%s\n' "$out" | grep -E '^--- FAIL|^\s+.*_test\.go:[0-9]+:' | head -40
  exit 1
fi
if (( skipped > 0 )); then
  printf '\n⚠ %d 条被 SKIP —— 引用本读数时**必须**同时写 SKIP=%d，不能只报 PASS=%d。\n' "$skipped" "$skipped" "$passed"
  printf '  SKIP 原因逐条列出：\n'
  printf '%s\n' "$out" | grep -A1 -E '^--- SKIP' | grep -E '_test\.go:[0-9]+:' | sed 's/^/    /' | head -20
fi
exit 0
