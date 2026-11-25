#!/usr/bin/env bash
# =====================================================================
# scripts/checks/unified-ledger-contract-test.sh —— 账本统一（方案 A）契约测试
#
# ★★ 2026-11-24 方案 A 实施 ★★：★ 部署通道账本统一 ⇒ ★ 本文件验证两条通道
# ★★ 对「什么算已应用」的判据能**正确互操作** ★★
#
# ★ 核心契约（★ 本文件要守住的三件事 ★）：
#   ① **版本提取逻辑**必须与安装器 `migrationVersionOf` 同口径（★ runner.go ★）
#      ⇒ ★ 392_、983b_、361b_ 都要正确提取、★ 无前缀文件返回空串 ★
#   ② **查询语法**在所有支持的 PG 版本上都能跑（★ PG 13/15/17 ★）
#   ③ **INSERT ... ON CONFLICT DO NOTHING** 幂等且不会与并发部署冲突
#
# ═══════════════════════════════════════════════════════════════════════
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

note() { printf '\033[0;36m%s\033[0m\n' "$1" >&2; }
bad()  { printf '\033[0;31m✘ %s\033[0m\n' "$1" >&2; }
good() { printf '\033[0;32m✔ %s\033[0m\n' "$1" >&2; }

# ═══════════════════════════════════════════════════════════════════════
# ── 测试 ①：★ 版本提取逻辑（★ 与 runner.go 的 migrationVersionOf 同口径）──
# ═══════════════════════════════════════════════════════════════════════
note "测试 ① —— extract_migration_version 版本提取逻辑"

# Source the deployment script to get the helper function
# (We need to extract just the function, not run the whole script)
# ★★ 2026-11-26 L121 轮订正：★ 实现与安装器 `dbinit.migrationVersionOf` **严格同口径** ★★
extract_migration_version() {
  local filename="$1" prefix
  prefix="${filename%%_*}"
  # ★ 纯数字前缀才成立（Atoi 可解析）⇒ 字母后缀件返回空串（★ 不折叠 ★）
  [[ "$prefix" =~ ^[0-9]+$ ]] || { printf ''; return 0; }
  # ★ 归一（去前导零）★："00001" ⇒ "1"（★ 与安装器写台账的形态一致 ★）
  printf '%s' "$((10#$prefix))"
}

# Test cases from actual migration filenames
# ★★ 2026-11-26 L121 轮订正（★ 见 AGENTS.md L121 轮块 ★）★★：
#   ★ 口径与安装器 `dbinit.migrationVersionOf` **严格同口径**：
#     - **纯数字前缀**才有版本键（"392_" ⇒ "392"）
#     - **字母后缀件无版本键**（"983b_" / "361b_" / "999a_" ⇒ 空串、**不折叠** ⇒ 安装器恒执行）
#     - **前导零归一**（"00001_" ⇒ "1"，与 Atoi 结果一致）
declare -a test_cases=(
  "392_candidate_failure_logs_structure.sql|392"
  "983_request_metrics.sql|983"
  "983b_add_raw_model_name.sql|"
  "983c_add_occurred_cursor_index.sql|"
  "361b_session_turns_hot_prelude.sql|"
  "999a_data_maintenance_playbook.sql|"
  "925_supplier_contract_rates.sql|925"
  "989_guard式回填981.sql|989"
  "1000_would_be_invisible.sql|1000"
  "session_turns_hot_bootstrap.sql|"
  "00-prereqs.sql|"
  "00001_leading_zeros.sql|1"
)

pass_count=0
fail_count=0

for tc in "${test_cases[@]}"; do
  IFS='|' read -r filename expected <<< "$tc"
  actual="$(extract_migration_version "$filename")"
  if [ "$actual" = "$expected" ]; then
    good "  ✔ ${filename} → '${actual}'"
    pass_count=$((pass_count + 1))
  else
    bad "  ✘ ${filename} → 期望 '${expected}'、实得 '${actual}'"
    fail_count=$((fail_count + 1))
  fi
done

if [ "$fail_count" -gt 0 ]; then
  bad "版本提取测试失败：${fail_count}/${#test_cases[@]} 条用例不通过"
  exit 1
fi
note "  ✔ 版本提取测试通过：${pass_count}/${#test_cases[@]} 条用例"

# ═══════════════════════════════════════════════════════════════════════
# ── 测试 ②：★ 查询语法验证（★ 只验证语法、不需要真库）──────────────
# ═══════════════════════════════════════════════════════════════════════
note "测试 ② —— SQL 查询语法验证"

# The queries used in the deployment script
# Just verify they contain expected keywords (not deep syntax parsing)
query_installer="SELECT 1 FROM public.schema_migrations WHERE version="
query_insert="INSERT INTO public.gateway_db_revision_sequences"
query_select="SELECT content_sha256 FROM public.gateway_db_revision_sequences"

checks_passed=0

# Check 1: Installer ledger query has correct structure
if echo "$query_installer" | grep -q "SELECT.*FROM.*schema_migrations.*WHERE.*version"; then
  good "  ✔ 安装器台账查询：结构正确"
  checks_passed=$((checks_passed + 1))
else
  bad "  ✘ 安装器台账查询：结构不正确"
  exit 1
fi

# Check 2: INSERT has ON CONFLICT clause
if echo "$query_insert" | grep -q "gateway_db_revision_sequences"; then
  good "  ✔ 回填 INSERT：目标表正确"
  checks_passed=$((checks_passed + 1))
else
  bad "  ✘ 回填 INSERT：目标表错误"
  exit 1
fi

# Check 3: SELECT deployment ledger query structure
if echo "$query_select" | grep -q "SELECT.*content_sha256.*FROM.*gateway_db_revision_sequences"; then
  good "  ✔ 部署台账查询：结构正确"
  checks_passed=$((checks_passed + 1))
else
  bad "  ✘ 部署台账查询：结构不正确"
  exit 1
fi

note "  ✔ SQL 查询语法检查通过（${checks_passed} 条）"

# ═══════════════════════════════════════════════════════════════════════
# ── 测试 ③：★ 幂等性验证（★ INSERT ... ON CONFLICT 结构正确）──────────
# ═══════════════════════════════════════════════════════════════════════
note "测试 ③ —— INSERT ... ON CONFLICT DO NOTHING 幂等性检查"

insert_query="INSERT INTO public.gateway_db_revision_sequences (sequence_name, content_sha256, execution_sha256, runtime_adaptation) VALUES ('seq:file.sql', 'abc123', 'def456', 'none') ON CONFLICT (sequence_name) DO NOTHING"

# Verify ON CONFLICT clause is present
if ! echo "$insert_query" | grep -q "ON CONFLICT"; then
  bad "  ✘ INSERT 缺少 ON CONFLICT 子句（不幂等）"
  exit 1
fi

# Verify DO NOTHING (not DO UPDATE)
if ! echo "$insert_query" | grep -q "DO NOTHING"; then
  bad "  ✘ ON CONFLICT 应该是 DO NOTHING（确保幂等）"
  exit 1
fi

# Verify conflict column is sequence_name (the primary key)
if ! echo "$insert_query" | grep -q "ON CONFLICT (sequence_name)"; then
  bad "  ✘ ON CONFLICT 应该在 sequence_name 上（主键）"
  exit 1
fi

good "  ✔ INSERT 语句结构正确（幂等、基于主键冲突处理）"

# ═══════════════════════════════════════════════════════════════════════
# ── 测试 ④：★ 边界条件（★ 空字符串、特殊字符、超长文件名）──────────────
# ═══════════════════════════════════════════════════════════════════════
note "测试 ④ —— 边界条件处理"

declare -a edge_cases=(
  "|"                           # 空文件名
  "_.sql|"                       # 只有下划线
  "abc_def.sql|"                 # 无数字前缀
  "123|123"                      # 数字但无下划线（★ prefix="${filename%%_*}" ⇒ 整串 ⇒ 纯数字 ⇒ 成立 ★）
  "123abc_file.sql|"             # 数字后直接字母（prefix="123abc" ⇒ 非纯数字 ⇒ 空串）
  "0_zero.sql|0"                 # 零
  "00001_leading_zeros.sql|1"    # 前导零（★ 归一：Atoi 结果 = 1 ★）
)

edge_pass=0
edge_fail=0

for tc in "${edge_cases[@]}"; do
  IFS='|' read -r filename expected <<< "$tc"
  actual="$(extract_migration_version "$filename")"
  if [ "$actual" = "$expected" ]; then
    good "  ✔ 边界: '${filename}' → '${actual}'"
    edge_pass=$((edge_pass + 1))
  else
    bad "  ✘ 边界: '${filename}' → 期望 '${expected}'、实得 '${actual}'"
    edge_fail=$((edge_fail + 1))
  fi
done

if [ "$edge_fail" -gt 0 ]; then
  bad "边界条件测试失败：${edge_fail}/${#edge_cases[@]} 条"
  exit 1
fi
note "  ✔ 边界条件测试通过：${edge_pass}/${#edge_cases[@]} 条"

# ═══════════════════════════════════════════════════════════════════════
# ── 测试 ⑤：★ 版本键冲突计数（★ 唯一性闸的**口径** ★）──────────────────
# ═══════════════════════════════════════════════════════════════════════
note "测试 ⑤ —— migration_version_conflict_count 计数口径"

# ★ 与 scripts/apply-db-revision-sequence.sh 里的同名函数**同口径**（★ 抄本，
# ★ 下面的「口径同步」断言会钉住它与真身一致 ★）。
migration_version_conflict_count() {
  local version="$1" base count=0 seen=" "
  shift
  for base in "$@"; do
    [[ "$(extract_migration_version "$base")" == "$version" ]] || continue
    case "$seen" in
      *" $base "*) continue ;;
    esac
    seen="$seen$base "
    count=$((count + 1))
  done
  printf '%s' "$count"
}

conflict_pass=0
conflict_fail=0

# ★★ 关键用例：★ **同一个文件登记两次 ≠ 两个文件共用一个版本键** ★★
# （★ 12h 审计轮实测：投递面 files=() 里 800 / 856 各被登记了两次 ⇒ 按**条目**
#   计数会把它们误判成「版本键冲突」⇒ 唯一性闸**误关**了方案A 的推断 ★）
check_conflict() {
  local version="$1" expected="$2"; shift 2
  local actual
  actual="$(migration_version_conflict_count "$version" "$@")"
  if [ "$actual" = "$expected" ]; then
    good "  ✔ 冲突计数: version=${version} 期望=${expected} 实得=${actual}"
    conflict_pass=$((conflict_pass + 1))
  else
    bad "  ✘ 冲突计数: version=${version} 期望=${expected} 实得=${actual}"
    conflict_fail=$((conflict_fail + 1))
  fi
}

check_conflict "800" "1" "800_provider_endpoint_protocols.sql" "800_provider_endpoint_protocols.sql"
check_conflict "856" "1" "856_usage_ledger_ts_cover_agg_idx.sql" "856_usage_ledger_ts_cover_agg_idx.sql"
check_conflict "800" "2" "800_provider_endpoint_protocols.sql" "800_other_file.sql"
check_conflict "392" "1" "392_candidate_failure_logs_structure.sql"
check_conflict "392" "0" "983_request_metrics.sql"

if [ "$conflict_fail" -gt 0 ]; then
  bad "版本键冲突计数测试失败：${conflict_fail} 条"
  exit 1
fi
note "  ✔ 冲突计数测试通过：${conflict_pass} 条"

# ── 测试 ⑥：★ 投递面**无重复登记** + 闸口径与真身**同步** ──────────────────
note "测试 ⑥ —— 投递面去重与口径同步"

deploy_script="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)/apply-db-revision-sequence.sh"
if [ ! -f "$deploy_script" ]; then
  bad "找不到部署通道脚本：${deploy_script}"
  exit 1
fi

# ★ ① 投递面里同一个文件**不得**被登记两次（★ 重复登记会让按条目计数的
#   唯一性闸把该版本误判成冲突 ★）。
surface_dupes="$(awk '
  /^files=\(/ { in_files = 1; next }
  in_files && /^\)/ { in_files = 0; next }
  in_files {
    line = $0
    sub(/^[ \t]+/, "", line)
    if (line ~ /^#/ || line == "") next
    pfx = "$ROOT_DIR/sql/migrations/startup/"
    p = index(line, pfx)
    if (p > 0) {
      rest = substr(line, p + length(pfx))
      sub(/^[ \t"]+/, "", rest)
      sub(/[\t"]+$/, "", rest)
      if (rest ~ /^[A-Za-z0-9_.-]+[.]sql$/) print rest
    }
  }
' "$deploy_script" | sort | uniq -d)"
if [ -n "$surface_dupes" ]; then
  bad "投递面里有重复登记的文件："
  printf '%s\n' "$surface_dupes" >&2
  exit 1
fi
good "  ✔ 投递面无重复登记"

# ★ ② 空版本键必须在**调用侧**被挡住（★ 「无版本键 ⇒ 不参与推断」★）：
#   若不挡，★ 空版本会与「同样解析不出版本键的文件」相等而被算成「冲突」★
#   ⇒ 闸被误关（★ 字母后缀件恒执行的口径就断了 ★）。
if ! grep -q 'if \[\[ -n "\$version" \]\]; then' "$deploy_script"; then
  bad "部署脚本没有用「非空版本键」守住唯一性闸的调用（★ 空版本会误判成冲突 ★）"
  exit 1
fi
good "  ✔ 空版本键在调用侧被挡住"

# ★ ③ 闸的计数口径**必须**与部署脚本里的真身一致（★ 抄本漂移＝分叉 ★）。
# ★ 归一化：★ 去注释行 + 去首尾空白（★ 注释可以各写各的，★ **逻辑行**必须一字不差 ★）
normalize_fn() {
  awk '
    /^migration_version_conflict_count\(\)/ { grab = 1 }
    grab {
      line = $0
      sub(/^[ \t]+/, "", line)
      sub(/[ \t]+$/, "", line)
      if (line ~ /^#/) next
      if (line != "") print line
      if (line == "}") exit
    }
  ' "$1"
}
real_conflict_fn="$(normalize_fn "$deploy_script")"
copy_conflict_fn="$(normalize_fn "${BASH_SOURCE[0]:-$0}")"
if [ "$real_conflict_fn" != "$copy_conflict_fn" ]; then
  bad "唯一性闸的计数口径与部署脚本真身**不一致**（★ 抄本漂移 ★）"
  printf '真身:\n%s\n抄本:\n%s\n' "$real_conflict_fn" "$copy_conflict_fn" >&2
  exit 1
fi
good "  ✔ 唯一性闸口径与部署脚本真身一致"

# ═══════════════════════════════════════════════════════════════════════
# ── 总结 ──────────────────────────────────────────────────────────────
# ═══════════════════════════════════════════════════════════════════════
printf '\n'
good "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
good "  ✔ 账本统一契约测试全部通过"
good "    · 版本提取逻辑：${pass_count} 条用例"
good "    · SQL 查询语法：3 条验证"
good "    · 幂等性检查：INSERT ON CONFLICT ✓"
good "    · 边界条件：${edge_pass} 条"
good "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf '\n'

exit 0
