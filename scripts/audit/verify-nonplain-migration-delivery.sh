#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# verify-nonplain-migration-delivery.sh — 变异验证：证明「非三位数字前缀迁移的
# 投递登记册」那两道门**不是恒绿**
#
# WHAT THIS IS
# ------------
# 2026-10-22 R82 轮实测复现的缺陷：仓库里有两道门决定「一条 startup 迁移在
# 自己的视野里吗」，**两道都靠从文件名里解析一个纯整数**：
#
#   ① scripts/apply-db-revision-sequence_test.sh（投递路径契约门）
#      `[[ "$name" =~ ^[0-9]{3}_.*\.sql$ ]] || continue`
#   ② installer/cmd/llm-gw-installer/migration_embed_sync_gate_test.go
#      （强制 embed 门）`strconv.Atoi(name[:idx])` 失败即 `continue`
#
# ⇒ 任何**解析不出纯三位数字前缀**的迁移（`NNNb_` / `NNNpN_` / 日期名）
#   对**两道门都完全隐形**。而 `NNNb_` **是本仓正在使用的命名约定**
#   （361b/487b/487c/680b/682b/683b/706b/707b 共 8 条已交付件）。
#
# ⇒ 实测复现：工作树内新建 `912b_zzz_probe_delivery.sql`
#   → 两道门**都绿** ⇒ 该迁移既不在通道 files=()、也不在 embeddata/StartupFiles
#   → **升级库拿不到、新装库拿不到，而没有任何一道门会红**。
#
# 修法（与 R81 轮 L11-b 门同款）：**穷举全目录 ＋ 显式清单**。
#   · 穷举：`sql/migrations/startup/` **递归**全量 up 体；
#   · 显式清单：`sql/schema/startup_migration_nonplain.tsv`（13 条）。
#
# 本脚本给这两道门装上**可复核的牙**：
#
#   ── A 组：对**被审对象**（登记册 / 目录 / embeddata）的变异 ⇒ 门必须红 ──
#   M1  新增一条非纯数字前缀迁移（顶层）               ⇒ bash 门红（漏登记）
#   M2  同上                                           ⇒ Go 门红（漏登记）
#   M3  登记册里删掉一条                               ⇒ bash 门红（漏登记）
#   M4  同上                                           ⇒ Go 门红（漏登记）
#   M5  登记册里塞一条**陈旧条目**（磁盘上没有）         ⇒ bash 门红
#   M6  同上                                           ⇒ Go 门红
#   M7  投递腿改成 none，但该文件**在**通道 files=() 里 ⇒ bash 门红（声明≠事实）
#   M8  投递腿改成 none，但该文件**在** embeddata 里  ⇒ Go 门红（声明≠事实）
#   M9  删掉 embeddata 里 361b 的副本（登记册声明 both） ⇒ Go 门红
#   M10 投递腿写成非法值                               ⇒ Go 门红（登记册格式门）
#   M11 理由列清空                                     ⇒ Go 门红（决定必须留理由）
#   M12 投递腿改成 installer，但该文件不在 StartupFiles ⇒ bash 门红
#   M13 ★ 新增一条非纯数字前缀迁移到 **`up/` 子目录**   ⇒ bash 门红
#        （★ 这条证明**递归穷举**是承重的：子目录从前完全隐形）
#
#   ── B 组：对**门自己**的变异 ⇒ 证明判据不是靠「别的判据在红」蒙混过关 ──
#   M14 ★ 把 bash 门的非前缀分类从「按文件名」改成「按相对路径」
#        ⇒ bash 门必须红（★ 这正是本轮第一版的真实错误：它把整个 up/ 子目录
#          误判成非前缀 ⇒ 门当场红在 13 条 up/ 文件上）
#   M15 ★ **同时**把 find 改回 `-maxdepth 1` **并且**新增 `up/912b_…`
#        ⇒ bash 门必须红（★ 两个因子各改一个都抓不到；合起来才抓得到
#          ⇒ 说明「递归」这条判据确实承重，而不是装饰）
#
# 自我约束（沿用 verify-l11b-cost-view-rowset.sh 的教训）：
#
#   1. **编译红不算数**："build failed" 不是 "assertion went red"。
#   2. **变异的「红」必须能归因**：expect_red 会回显断言消息，读者必须能看出
#      这条红确实来自本条变异对应的那条判据。
#   3. **变异必须真的落地**：助手失败 / 内容没变 ⇒ 直接判红（不可归因）。
#   4. **新增文件立刻删**：否则后续每条变异都从脏工作树开始 ⇒ 红的归因不了。
#   5. 还原按**原路径** md5 逐字校验（还原到仓库根目录那种错位会让读数作废）。
#
# USAGE
#   bash scripts/audit/verify-nonplain-migration-delivery.sh
#
# Requires: go on PATH（~/sdk/go/bin 会被自动补上）、python3。
# ---------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

if ! command -v go >/dev/null 2>&1; then
  if [ -x "$HOME/sdk/go/bin/go" ]; then
    export PATH="$HOME/sdk/go/bin:$PATH"
  else
    echo "FAIL: go not found on PATH" >&2
    exit 2
  fi
fi

WORK_DIR="$(mktemp -d)"
BACKUP_DIR="$WORK_DIR/backup"
mkdir -p "$BACKUP_DIR"
ADDED_LIST="$WORK_DIR/added.list"
DIRS_LIST="$WORK_DIR/added_dirs.list"
: > "$ADDED_LIST"
: > "$DIRS_LIST"

MUTATIONS_RUN=0
MUTATIONS_RED=0
FAILED=()

REGISTRY="sql/schema/startup_migration_nonplain.tsv"
BASH_GATE="scripts/apply-db-revision-sequence_test.sh"
EMBED_361B="installer/cmd/llm-gw-installer/embeddata/startup/361b_provider_models_source_prelude.sql"

md5_of() {
  if command -v md5sum >/dev/null 2>&1; then
    md5sum "$1" | awk '{print $1}'
  else
    md5 -q "$1"
  fi
}

b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

backup() {
  local file="$1"
  local key
  key="$(printf '%s' "$file" | tr '/' '_')"
  cp "$file" "$BACKUP_DIR/$key"
  printf '%s\t%s\n' "$key" "$file" >> "$BACKUP_DIR/index"
}

restore() {
  local file="$1"
  local key
  key="$(printf '%s' "$file" | tr '/' '_')"
  cp "$BACKUP_DIR/$key" "$file"
  local before after
  before="$(md5_of "$BACKUP_DIR/$key")"
  after="$(md5_of "$file")"
  if [ "$before" != "$after" ]; then
    echo "   ✘ RESTORE MISMATCH for $file ($before != $after)" >&2
    FAILED+=("restore:$file")
    return 1
  fi
  echo "   ↩ restored $file (md5 $after)"
  return 0
}

# mutate_regex 用正则替换。
#
# ⚠⚠⚠ **两个必须**（都是本仓栽过的坑）：
#   1. **必须带 re.M** —— 否则 `^`/`$` 只匹配整串首尾 ⇒ 多行文件永远匹配不到
#      ⇒ **变异没落地却看起来跑过了** ⇒ 假通过（R81 轮 M15 就是这么假通过的）。
#   2. **替换串必须用 lambda 传入** —— Python 的 `re.sub` 会把替换串里的
#      `\n`/`\t`/`\1` 当转义处理 ⇒ 我们要塞进去的是**字面量**（比如 TSV 的
#      真 TAB、正则里的 `\(`）⇒ 走 lambda 才不会被二次解释。
#      ⇒ 替换内容一律经 base64 传入，避免 shell 与 Python 两层转义打架。
mutate_regex() {
  local file="$1" pattern="$2" new_b64="$3"
  python3 - "$file" "$pattern" "$new_b64" <<'PY'
import base64, re, sys
path, pattern, new_b64 = sys.argv[1:4]
new = base64.b64decode(new_b64).decode("utf-8")
with open(path, "r", encoding="utf-8") as handle:
    content = handle.read()
updated, n = re.subn(pattern, lambda _m: new, content, count=1, flags=re.M)
if n != 1:
    sys.exit("pattern matched %d times in %s, want exactly 1" % (n, path))
with open(path, "w", encoding="utf-8") as handle:
    handle.write(updated)
print("   mutation applied to %s" % path)
PY
}

# add_file 造一个新文件，并登记到清理列表。
add_file() {
  local file="$1" content_b64="$2"
  local dir
  dir="$(dirname "$file")"
  if [ ! -d "$dir" ]; then
    mkdir -p "$dir"
    echo "$dir" >> "$DIRS_LIST"
  fi
  printf '%s' "$content_b64" | base64 -d > "$file"
  echo "$file" >> "$ADDED_LIST"
  echo "   mutation applied (new file) $file"
}


# remove_added 立刻删掉 add_file 造出来的文件。
#
# ⚠⚠⚠ **为什么必须「立刻」删**：新增文件型变异（M1/M2/M13/M15）造出来的文件
#   会**留在工作树里** ⇒ 后续每条变异跑的时候门都是红的 ⇒ **后续变异的「红」
#   归因不了**（可能是上一步的残留造成的）⇒ ⇒ **量具故障：假通过**。
remove_added() {
  local file="$1"
  if [ -f "$file" ]; then
    rm -f "$file"
    echo "   ↩ removed added file $file"
  fi
  local dir
  dir="$(dirname "$file")"
  if [ -f "$DIRS_LIST" ] && grep -Fxq "$dir" "$DIRS_LIST"; then
    rmdir "$dir" 2>/dev/null || true
  fi
}

# run_mutation 跑一次变异，并**自检它真的落地了**（文件内容确实变了）。
run_mutation() {
  local label="$1" file="$2"; shift 2
  local before after
  before="$(md5_of "$file")"
  if ! "$@"; then
    echo "   ✘ MUTATION FAILED TO APPLY — ${label}（助手报错）⇒ 本条的「红」不可归因 ⇒ 判红"
    FAILED+=("$label(not-applied)")
    return 1
  fi
  after="$(md5_of "$file")"
  if [ "$before" = "$after" ]; then
    echo "   ✘ MUTATION NOT APPLIED — $label 没有改变 $file ⇒ 不可归因 ⇒ 判红"
    FAILED+=("$label(not-applied)")
    return 1
  fi
  return 0
}

# run_mutation_new 跑一次「新增文件」型变异，并自检文件确实造出来了。
run_mutation_new() {
  local label="$1" file="$2"; shift 2
  if ! "$@"; then
    echo "   ✘ MUTATION FAILED TO APPLY — ${label}（助手报错）⇒ 判红"
    FAILED+=("$label(not-applied)")
    return 1
  fi
  if [ ! -f "$file" ]; then
    echo "   ✘ MUTATION NOT APPLIED — $label 没有造出 $file ⇒ 判红"
    FAILED+=("$label(not-applied)")
    return 1
  fi
  return 0
}


# expect_red_bash 期望 **bash 侧**的门红，且红在**指定的那条判据**上。
#
# ⚠ 编译红 / 语法错不算数（"build failed" 不是 "assertion went red"）。
expect_red_bash() {
  local label="$1" reason_pattern="$2"
  MUTATIONS_RUN=$((MUTATIONS_RUN + 1))
  echo "── MUTATION $label: expect RED (bash gate)"
  local output
  output="$(bash "$BASH_GATE" 2>&1)"
  if echo "$output" | grep -qE 'syntax error|command not found|unbound variable'; then
    echo "   ✘ HARNESS/BASH ERROR — the mutation broke the gate script itself; not a valid mutation"
    echo "$output" | tail -10
    FAILED+=("$label(harness-error)")
    return
  fi
  if ! echo "$output" | grep -q 'contract FAILED'; then
    echo "   ✘ STILL GREEN — the bash gate does not have teeth"
    echo "$output" | tail -12
    FAILED+=("$label(still-green)")
    return
  fi
  if ! echo "$output" | grep -qE "$reason_pattern"; then
    echo "   ✘ RED BUT NOT FOR THE RIGHT REASON — 没有出现预期的断言消息：$reason_pattern"
    echo "$output" | grep -A3 'contract FAILED' | head -12 | sed 's/^/     /'
    FAILED+=("$label(wrong-reason)")
    return
  fi
  echo "   ✔ RED as expected（断言消息已回显）"
  echo "   ↳ reason:"
  echo "$output" | grep -E '^\s+\[[0-9]+\]' | head -2 | sed 's/^/     /'
  MUTATIONS_RED=$((MUTATIONS_RED + 1))
}

# expect_red_go 期望 **Go 侧**的门红，且红在**指定的那条判据**上。
expect_red_go() {
  local label="$1" reason_pattern="$2" test_pattern="$3"
  MUTATIONS_RUN=$((MUTATIONS_RUN + 1))
  echo "── MUTATION $label: expect RED (go gate)"
  local output
  output="$(cd installer && go test ./cmd/llm-gw-installer/ -run "$test_pattern" -count=1 2>&1)"
  if echo "$output" | grep -qE '\[build failed\]|^# |cannot use|undefined:'; then
    echo "   ✘ BUILD FAILED — the mutation does not compile; not a valid mutation"
    echo "$output" | head -12
    FAILED+=("$label(build)")
    return
  fi
  if ! echo "$output" | grep -qE '^--- FAIL:'; then
    echo "   ✘ STILL GREEN — the go gate does not have teeth"
    echo "$output" | tail -12
    FAILED+=("$label(still-green)")
    return
  fi
  if ! echo "$output" | grep -qE "$reason_pattern"; then
    echo "   ✘ RED BUT NOT FOR THE RIGHT REASON — 没有出现预期的断言消息：$reason_pattern"
    echo "$output" | grep -E '^    [a-z_]+_test\.go:[0-9]+:' | head -5 | sed 's/^/     /'
    FAILED+=("$label(wrong-reason)")
    return
  fi
  echo "   ✔ RED as expected（断言消息已回显）"
  echo "   ↳ reason:"
  echo "$output" | grep -E '^    [a-z_]+_test\.go:[0-9]+:' | head -3 | sed 's/^/     /'
  MUTATIONS_RED=$((MUTATIONS_RED + 1))
}

expect_green_bash() {
  local label="$1"
  echo "── BASELINE $label: expect GREEN (bash gate)"
  local output
  output="$(bash "$BASH_GATE" 2>&1)"
  if echo "$output" | grep -q 'contract FAILED'; then
    echo "   ✘ RED at baseline:"
    echo "$output" | grep -A6 'contract FAILED' | head -10
    FAILED+=("$label(baseline-red)")
  else
    echo "   ✔ GREEN as expected"
  fi
}

expect_green_go() {
  local label="$1" test_pattern="$2"
  echo "── BASELINE $label: expect GREEN (go gate)"
  local output
  output="$(cd installer && go test ./cmd/llm-gw-installer/ -run "$test_pattern" -count=1 2>&1)"
  if echo "$output" | grep -qE '^--- FAIL:'; then
    echo "   ✘ RED at baseline:"
    echo "$output" | tail -20
    FAILED+=("$label(baseline-red)")
  else
    echo "   ✔ GREEN as expected"
  fi
}

cleanup() {
  if [ -f "$BACKUP_DIR/index" ]; then
    while IFS= read -r line; do
      key="${line%%	*}"
      file="${line#*	}"
      [ -f "$BACKUP_DIR/$key" ] && cp "$BACKUP_DIR/$key" "$file"
    done < "$BACKUP_DIR/index"
  fi
  if [ -f "$ADDED_LIST" ]; then
    while IFS= read -r file; do
      [ -n "$file" ] && rm -f "$file"
    done < "$ADDED_LIST"
  fi
  if [ -f "$DIRS_LIST" ]; then
    while IFS= read -r dir; do
      [ -n "$dir" ] && rmdir "$dir" 2>/dev/null || true
    done < "$DIRS_LIST"
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

GO_MAIN='TestCanonicalNonPlainMigrationsHaveDeclaredDeliveryLeg'
GO_LEG='TestNonPlainRegistryLegValuesAreKnown'

PROBE_SQL="-- probe: 非纯数字前缀迁移（审计变异用）
CREATE TABLE IF NOT EXISTS public.zzz_probe_delivery_marker (id bigint PRIMARY KEY);
"
PROBE_B64="$(b64 "$PROBE_SQL")"

echo "=== 非纯数字前缀迁移的投递登记册门：变异验证 ==="
echo "repo: $REPO_ROOT"
echo

backup "$REGISTRY"
backup "$BASH_GATE"
backup "$EMBED_361B"

expect_green_bash "baseline"
expect_green_go "baseline" "$GO_MAIN|$GO_LEG"

# ---------------------------------------------------------------------------
# M1 — 新增一条非纯数字前缀迁移（顶层）⇒ bash 门必须红（漏登记）
# ---------------------------------------------------------------------------
PROBE1="sql/migrations/startup/912b_zzz_probe_delivery.sql"
if run_mutation_new "M1" "$PROBE1" add_file "$PROBE1" "$PROBE_B64"; then
  expect_red_bash "M1" '912b_zzz_probe_delivery\.sql.*解析不出纯三位数字前缀'
  remove_added "$PROBE1"
fi

# ---------------------------------------------------------------------------
# M2 — 同一条变异，从 Go 侧看 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation_new "M2" "$PROBE1" add_file "$PROBE1" "$PROBE_B64"; then
  expect_red_go "M2" '912b_zzz_probe_delivery\.sql.*解析不出纯三位数字前缀' "$GO_MAIN"
  remove_added "$PROBE1"
fi

# ---------------------------------------------------------------------------
# M3 — 登记册里删掉一条（361b）⇒ bash 门必须红（漏登记）
# ---------------------------------------------------------------------------
if run_mutation "M3" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^361b_provider_models_source_prelude\.sql\tboth\t.*$' ''; then
  expect_red_bash "M3" '361b_provider_models_source_prelude\.sql.*解析不出纯三位数字前缀'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M4 — 同一条变异，从 Go 侧看 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M4" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^361b_provider_models_source_prelude\.sql\tboth\t.*$' ''; then
  expect_red_go "M4" '361b_provider_models_source_prelude\.sql.*解析不出纯三位数字前缀' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M5 — 登记册里塞一条**陈旧条目**（磁盘上没有）⇒ bash 门必须红
#
# ⚠⚠ **必须替换「整行」而不是行首一小段**：只替换行首的话，原行**剩下的部分**
#   会留在下一行 ⇒ 那行没有 TAB ⇒ Go 侧读册子时先撞「字段数 1」的格式错
#   ⇒ **红在了错的地方**（M6 第一版就是这么假通过的：门红了，但红的不是本条判据）。
# ---------------------------------------------------------------------------
GHOST_LINE='999z_ghost_migration_that_does_not_exist.sql	none	这条文件在磁盘上不存在：门必须报「登记册陈旧」。'
if run_mutation "M5" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^2026-07-13-multimodal-token-fields-hot\.sql\tnone\t.*$' \
  "$(b64 "$GHOST_LINE")"; then
  expect_red_bash "M5" '999z_ghost_migration_that_does_not_exist\.sql.*登记册陈旧'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M6 — 同一条陈旧条目，从 Go 侧看 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M6" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^2026-07-13-multimodal-token-fields-hot\.sql\tnone\t.*$' \
  "$(b64 "$GHOST_LINE")"; then
  expect_red_go "M6" '999z_ghost_migration_that_does_not_exist\.sql.*登记册陈旧' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M7 — 投递腿改成 none，但该文件**在**通道 files=() 里 ⇒ bash 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M7" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^487b_request_logs_hot_system_fingerprint\.sql\tchannel\t' \
  "$(b64 '487b_request_logs_hot_system_fingerprint.sql	none	')"; then
  expect_red_bash "M7" '487b_request_logs_hot_system_fingerprint\.sql.*声明与事实不符'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M8 — 投递腿改成 none，但该文件**在** embeddata 里 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M8" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^361b_provider_models_source_prelude\.sql\tboth\t' \
  "$(b64 '361b_provider_models_source_prelude.sql	none	')"; then
  expect_red_go "M8" '361b_provider_models_source_prelude\.sql.*声明与事实不符' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M9 — 把一条**不在 embeddata 里**的迁移声明成安装器腿（706b：只走通道腿）
#      ⇒ Go 门必须红（声明走安装器腿 ⇒ 必须有 embed 副本 ⇒ 新装库拿不到）
#
# ⚠⚠ **为什么不能「删掉 embeddata 里的副本」**：那是 `go:embed` 指令的一个目标
#   ⇒ 删掉它 ⇒ **整包编译失败** ⇒ 「编译红不算数」⇒ 测不到东西。
#   ⇒ 改成「声明与事实不符」这一侧 ⇒ 同样打的是「声明走安装器腿 ⇒ 必须有副本」
#   这条判据，但**不破坏编译**。
# ---------------------------------------------------------------------------
if run_mutation "M9" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^706b_session_turns_hot_prelude\.sql\tchannel\t' \
  "$(b64 '706b_session_turns_hot_prelude.sql	installer	')"; then
  expect_red_go "M9" '706b_session_turns_hot_prelude\.sql.*新装库拿不到' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M10 — 投递腿写成非法值 ⇒ Go 门必须红（登记册格式门）
# ---------------------------------------------------------------------------
if run_mutation "M10" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^487c_request_logs_hot_raw_model_name\.sql\tchannel\t' \
  "$(b64 '487c_request_logs_hot_raw_model_name.sql	unknown-leg	')"; then
  expect_red_go "M10" '投递腿 .* 非法' "$GO_MAIN|$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M11 — 理由列清空 ⇒ Go 门必须红（「有意不投递」这类决定必须留理由）
# ---------------------------------------------------------------------------
if run_mutation "M11" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^478p2_affinity_indexes\.sql\tnone\t.*$' \
  "$(b64 '478p2_affinity_indexes.sql	none	')"; then
  expect_red_go "M11" '没有写理由' "$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M12 — 投递腿改成 installer，但该文件不在 StartupFiles ⇒ bash 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M12" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^487b_request_logs_hot_system_fingerprint\.sql\tchannel\t' \
  "$(b64 '487b_request_logs_hot_system_fingerprint.sql	installer	')"; then
  expect_red_bash "M12" '487b_request_logs_hot_system_fingerprint\.sql.*StartupFiles'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M13 ★ 新增一条非纯数字前缀迁移到 **`up/` 子目录** ⇒ bash 门必须红
#      （★ 这条证明**递归穷举**是承重的：子目录从前完全隐形）
# ---------------------------------------------------------------------------
PROBE3="sql/migrations/startup/up/912b_zzz_probe_in_subdir.sql"
if run_mutation_new "M13" "$PROBE3" add_file "$PROBE3" "$PROBE_B64"; then
  expect_red_bash "M13" 'up/912b_zzz_probe_in_subdir\.sql.*解析不出纯三位数字前缀'
  remove_added "$PROBE3"
fi

# ---------------------------------------------------------------------------
# M14 ★ 把 bash 门的非前缀分类从「按文件名」改成「按相对路径」
#      ⇒ bash 门必须红（★ 这正是本轮第一版的真实错误）
# ---------------------------------------------------------------------------
PATH_BASED_LINE="nonplain_actual=\$(printf '%s\\n' \"\$startup_up_bodies\" | grep -vE '^[0-9]{3}_' || true)"
if run_mutation "M14" "$BASH_GATE" mutate_regex "$BASH_GATE" \
  '^nonplain_actual=.*$' \
  "$(b64 "$PATH_BASED_LINE")"; then
  expect_red_bash "M14" 'up/376_autoupdate\.sql.*解析不出纯三位数字前缀'
  restore "$BASH_GATE"
fi

# ---------------------------------------------------------------------------
# M15 ★ **同时**把 find 改回 `-maxdepth 1` **并且**新增 `up/912b_…`
#      ⇒ bash 门必须红（★ 两个因子各改一个都抓不到；合起来才抓得到）
# ---------------------------------------------------------------------------
PROBE4="sql/migrations/startup/up/912b_zzz_probe_in_subdir.sql"
if run_mutation_new "M15" "$PROBE4" add_file "$PROBE4" "$PROBE_B64"; then
  if run_mutation "M15" "$BASH_GATE" mutate_regex "$BASH_GATE" \
    '^startup_up_bodies=\$\(find "\$ROOT_DIR/sql/migrations/startup" -type f' \
    "$(b64 'startup_up_bodies=$(find "$ROOT_DIR/sql/migrations/startup" -maxdepth 1 -type f')"; then
    expect_red_bash "M15" 'up/912b_zzz_probe_in_subdir\.sql.*解析不出纯三位数字前缀|穷举没有扫到 up/600_outbound_body_to_bodies_hot\.sql'
  fi
  restore "$BASH_GATE"
  remove_added "$PROBE4"
fi

# ---------------------------------------------------------------------------
# 收尾：还原校验 + 工作树干净
# ---------------------------------------------------------------------------
echo
echo "── 还原校验"
restore "$REGISTRY"
restore "$BASH_GATE"
restore "$EMBED_361B"

echo
echo "── 变异残留校验（只查**本脚本动过的路径**）"
#
# ⚠⚠⚠ **为什么不能查「整个工作树干净」**：本脚本运行时，开发者**完全可能在
#   同时改别的文件**（本轮实测就是如此：跑变异的同时在写 handoff / LESSONS）
#   ⇒ ⇒ 「整个工作树干净」会把**合法的并发改动**误判成「变异残留」
#   ⇒ ⇒ 本轮第一版就是这么假失败的（worktree-dirty）。
# ⇒ ⇒ **正确口径**：残留 = 「**本脚本动过的路径**上多出来的东西」。
RESIDUE=""
RESIDUE+=$(git status --porcelain -- sql/migrations/startup/ | sed 's/^/    /')
RESIDUE+=$(git status --porcelain -- installer/cmd/llm-gw-installer/embeddata/ | sed 's/^/    /')
for leftover in \
  sql/migrations/startup/912b_zzz_probe_delivery.sql \
  sql/migrations/startup/up/912b_zzz_probe_in_subdir.sql; do
  if [ -e "$leftover" ]; then
    RESIDUE+="    $leftover （探针残留）
"
  fi
done
if [ -n "$RESIDUE" ]; then
  echo "   ✘ 变异在它动过的路径上留下了残留："
  printf '%s' "$RESIDUE"
  FAILED+=("mutation-residue")
else
  echo "   ✔ 变异动过的路径上无残留（探针已删）"
fi

# ★ 被改文件的还原校验（**按原路径** md5 逐字比对）——
#   ⚠ 不能用 `git status` 判这几个文件：它们**本来就是本轮的改动** ⇒ git 必然显示 M
#   ⇒ ⇒ 还原是否正确，只能靠**与备份的 md5 比对**（restore 已打印，这里再复核一次）。
echo "── 被改文件还原校验（按原路径 md5）"
for f in "$REGISTRY" "$BASH_GATE" "$EMBED_361B"; do
  key="$(printf '%s' "$f" | tr '/' '_')"
  if [ ! -f "$BACKUP_DIR/$key" ]; then
    echo "   ✘ 没有 $f 的备份 ⇒ 还原无法校验"
    FAILED+=("restore-missing-backup:$f")
    continue
  fi
  if [ "$(md5_of "$BACKUP_DIR/$key")" = "$(md5_of "$f")" ]; then
    echo "   ✔ $f 与备份逐字一致"
  else
    echo "   ✘ $f 与备份不一致 ⇒ 还原失败"
    FAILED+=("restore-mismatch:$f")
  fi
done

echo
echo "=== 结果：${MUTATIONS_RED}/${MUTATIONS_RUN} 条变异按预期变红 ==="
if [ "${#FAILED[@]}" -gt 0 ]; then
  echo "FAILED:"
  for f in "${FAILED[@]}"; do echo "  - $f"; done
  exit 1
fi
echo "RESULT: PASS"
