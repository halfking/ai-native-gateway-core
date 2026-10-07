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
  # ── 2026-10-07 补登：文件名里没有 realdb，但用例是真库判据 ──
  # 这两个是实测（19:01 一次性 PG）里被「名单外库连接用例」提示捞出来的。
  # 它们的名字是 `TestLedgerReconciler_RunOnce_RealDB` 与
  # `TestTaxonomyUpsertAlias_Live` —— 连库，但文件名不带 realdb，
  # 所以靠 `*_realdb_test.go` 前缀盘点会漏。
  # ★ 这正是「名单制」的代价：命名约定不是唯一真相，脚本末尾的
  #   「名单外库连接用例」提示是它的兜底探测器。
  ledger_reconciliation_test.go
  taxonomy_sync_alias_upsert_live_test.go
)
# ⚠ `auto_route_affinity_worker_integration_test.go` **刻意不在**名单里，
#   尽管它也是真库判据（2026-10-07 实测追查）：
#   它的首行是 `//go:build integration`，**默认构建下不参与编译**。
#   名单按源码 grep 抽取，看不见 build tag ⇒ 把它算进声明数，
#   而 `go test -run` 永远匹配不到 ⇒ 声明 50 / 实跑 49，
#   第二道自检拒绝汇报（这正是它该做的）。
#   要跑它必须 `go test -tags integration`，那是**另一条命令**，
#   不与本门混在一起（否则这里的读数会再次带错分母）。
#   ⇒ 泛化教训：**「文件里有个连真库的用例」不等于「默认构建下它会被跑到」。**
#      任何按源码抽取名单的门，都必须知道 build tag 存在。

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

# ★ 分母必须是「登记的真库判据」，不能是整个 ./bg/ 包。
#
#   第一版直接 `go test ./bg/` 然后 grep 全包输出，报出的却是
#   「真库判据读数 PASS=1077 / FAIL=11 / SKIP=23」。实测（2026-10-07 19:01，
#   一次性 PG 17）：那 11 条 FAIL 里**只有 6 条**来自登记名单，
#   另外 5 条来自 auto_index_refresher_dedup / materialized_view_refresher /
#   realschema_migration_health_e2e / taxonomy_sync_alias_upsert_live /
#   ledger_reconciliation —— 这些是普通测试文件，与真库判据无关。
#
#   ⇒ 报数不带分母 = 把别的失败算进自己头上。这正是本仓反复记的那条纪律，
#     而第一版的脚本自己就犯了。以 PASS=1077 报「真库判据读数」是**错的**：
#     分母 1077 全部是 ./bg/ 包的用例数，不是真库判据数。
#
#   做法：用 -run 按「登记文件里的 Test 名」精确圈定子集，
#   再从输出里只统计这些用例。
#
# 第一步：从登记文件里抽出全部 `func TestXxx(` 名。
mapfile -t declared_tests < <(
  for f in "${REALDB_FILES[@]}"; do
    grep -oE '^func (Test[A-Za-z0-9_]+)\(' "bg/$f" 2>/dev/null | sed -E 's/^func //; s/\($//'
  done | sort -u
)
if (( ${#declared_tests[@]} == 0 )); then
  printf 'run-realdb-gate: 从登记文件里没抽到任何 Test 函数名，拒绝运行\n' >&2
  printf '  （函数抽取失效会让读数变成 0/0/0，而 0/0/0 看起来像「全过」）\n' >&2
  exit 1
fi

# ★ build tag 检查：名单里的文件若带 `//go:build`，它**默认不参与编译**，
#   抽取却照样数得到它的函数名 ⇒ 声明数 > 实跑数。
#   这个盲区 2026-10-07 实测撞到过（auto_route_affinity_worker_integration_test.go），
#   当时只表现为「声明 50 / 实跑 49」，**看不出原因**。
#   代价是读数不可信，而更糟的是：这类文件若被误当成「跑过了」，
#   就等于给一条从没执行的判据盖了章。
tagged=0
for f in "${REALDB_FILES[@]}"; do
  if head -5 "bg/$f" 2>/dev/null | grep -qE '^//go:build'; then
    printf 'run-realdb-gate: bg/%s 带 //go:build，默认构建下不参与编译\n' "$f" >&2
    printf '  请把它移出名单，或改用 `go test -tags <tag>` 的独立命令。\n' >&2
    tagged=$((tagged + 1))
  fi
done
if (( tagged > 0 )); then
  exit 1
fi

run_expr="^($(IFS='|'; echo "${declared_tests[*]}"))$"

# 用例级重跑，把子集输出单独收下来（-run 之外的用例不会出现在这里）。
subset=$(go test ./bg/ -count=1 -v -timeout=900s -run "$run_expr" 2>&1)

passed=$(printf '%s\n' "$subset" | grep -cE '^--- PASS')
failed=$(printf '%s\n' "$subset" | grep -cE '^--- FAIL')
skipped=$(printf '%s\n' "$subset" | grep -cE '^--- SKIP')
declared_n=${#declared_tests[@]}

# ★ 第二道自检：跑出来的用例数必须等于声明的用例数。
#   少一个 = 有用例名没被 go test 匹配上（改名、正则元字符、重复名），
#   而那会让读数**少报**失败 —— 一个漏跑的用例和一个通过的用例长得一样。
observed=$(printf '%s\n' "$subset" | grep -cE '^--- (PASS|FAIL|SKIP)')
if (( observed != declared_n )); then
  printf 'run-realdb-gate: 声明 %d 个用例，实测只跑出 %d 个 —— 读数不可信，拒绝汇报\n' \
    "$declared_n" "$observed" >&2
  printf '  （少跑的用例不会变红，它只会从读数里消失）\n' >&2
  exit 1
fi

printf '\nrun-realdb-gate: 真库判据读数 —— PASS=%d / FAIL=%d / SKIP=%d（分母=登记判据 %d 个用例）\n' \
  "$passed" "$failed" "$skipped" "$declared_n"

# ★ 名单之外的库连接用例：它们同样连了真库，但不在本脚本的管辖内。
#   判据是「用例名带 _RealDB / _Live 后缀」—— 后缀是本仓约定的真库标记。
#   `ledger_reconciliation_test.go` 与 `taxonomy_sync_alias_upsert_live_test.go`
#   就是靠它才被捞出来的（它们的**文件名**里没有 realdb，
#   靠文件名前缀匹配会漏掉 —— 那是名单的第二个盲区）。
#
#   ⚠ 带 `//go:build` 的文件在这里**点名但不入分母**：默认构建下它们不编译，
#     混进来会让「声明 ≠ 实跑」。它们属于另一条命令（-tags integration）。
outside=$(cd "$REPO_ROOT/bg" && grep -hoE '^func (Test[A-Za-z0-9_]+)\(' *_test.go 2>/dev/null \
  | sed -E 's/^func //; s/\($//' | grep -E '_(RealDB|Live)$' | sort -u)
if [[ -n "$outside" ]]; then
  not_in_list=$(comm -23 <(printf '%s\n' "$outside") <(printf '%s\n' "${declared_tests[@]}"))
  if [[ -n "$not_in_list" ]]; then
    printf '\n⚠ 以下用例同样连真库（名字带 _RealDB/_Live），但所在文件**不在登记名单**里：\n'
    printf '%s\n' "$not_in_list" | while read -r t; do
      # ⚠ $f 是 `cd bg` 后 grep 出来的**裸文件名**，而下面的 head 在
      #   仓库根执行 —— 第一版直接写 `head -5 "$f"` 找不到文件，
      #   grep 静默失败 ⇒ 标注恒为空。症状是「代码写了这个功能，
      #   输出里却从来看不到它」，而它不报错。
      #   ⇒ 静默失效的特征就是**没有错误输出**。这里必须给绝对路径。
      f=$(cd "$REPO_ROOT/bg" && grep -lE "^func ${t}\(" *_test.go 2>/dev/null | head -1)
      tag=""
      if [[ -n "$f" ]]; then
        if head -5 "$REPO_ROOT/bg/$f" 2>/dev/null | grep -qE '^//go:build'; then
          tag="  [带 //go:build，默认构建不跑 —— 需 -tags integration]"
        fi
      fi
      printf '    %-52s %s%s\n' "$t" "${f:-?}" "$tag"
    done
    printf '  ⇒ 它们不在本门的分母内。若要把它们纳入，请连同文件登记进 REALDB_FILES。\n'
  fi
fi

if (( failed > 0 )); then
  printf '\n失败明细（仅登记判据）：\n'
  printf '%s\n' "$subset" | grep -E '^--- FAIL|^\s+.*_test\.go:[0-9]+:' | head -40
  printf '\n※ 整包 ./bg/ 另有 %s 条非本门管辖的失败（其余用例），不在上面的分母内。\n' \
    "$(printf '%s\n' "$out" | grep -cE '^--- FAIL')"
  exit 1
fi
if (( skipped > 0 )); then
  printf '\n⚠ %d 条被 SKIP —— 引用本读数时**必须**同时写 SKIP=%d，不能只报 PASS=%d。\n' "$skipped" "$skipped" "$passed"
  printf '  SKIP 原因逐条列出：\n'
  printf '%s\n' "$subset" | grep -A1 -E '^--- SKIP' | grep -E '_test\.go:[0-9]+:' | sed 's/^/    /' | head -20
fi
exit 0
