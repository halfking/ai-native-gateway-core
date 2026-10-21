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
# R83 轮实测出的**第二宇宙**：embeddata/startup/ 是独立于 canonical 目录的
# 另一个文件宇宙，里面有**非前缀名**的文件（`session_turns_hot_bootstrap.sql`
# —— ★★ canonical 目录里根本没有它）⇒ ⇒ 登记册必须覆盖两个宇宙。
#
# R83 轮的第二处升级：登记册的「效果落点」列把「有意不投递」从散文变成
# **可机械核对的事实** ⇒ ⇒ 那条决定不再只是「声明一下」。
#
# 本脚本给这两道门装上**可复核的牙**：
#
#   ── A 组：对**被审对象**（登记册 / 目录 / embeddata / 通道脚本）的变异 ──
#   M1  新增一条非纯数字前缀迁移（canonical 顶层）      ⇒ bash 门红（漏登记）
#   M2  同上                                           ⇒ Go 门红（漏登记）
#   M3  登记册里删掉一条                               ⇒ bash 门红（漏登记）
#   M4  同上                                           ⇒ Go 门红（漏登记）
#   M5  登记册里塞一条**陈旧条目**（磁盘上没有）         ⇒ bash 门红
#   M6  同上                                           ⇒ Go 门红
#   M7  投递腿改成 none，但该文件**在**通道 files=() 里 ⇒ bash 门红（声明≠事实）
#   M8  投递腿改成 none，但该文件**在** embeddata 里  ⇒ Go 门红（声明≠事实）
#   M9  投递腿改成 installer，但该文件不在 StartupFiles ⇒ bash 门红
#   M10 投递腿写成非法值                               ⇒ Go 门红（登记册格式门）
#   M11 理由列清空                                     ⇒ Go 门红（决定必须留理由）
#        ⚠ R83 轮第一版这条用正则反向引用 `\1` ⇒ **在 lambda 里不生效** ⇒
#          落地的形状是字面量 "\1" ⇒ 现在改用**按列清空**这个语义明确的助手。
#   M12 ★★ **效果落点不成立**（把落点改成基线里不存在的列）
#        ⇒ bash 门红（★ 这条**正是能抓住 R82 轮那个错的变异**：
#          R82 轮给 2026-07-13-… 写的理由引用了 `session_turns.reasoning_tokens`，
#          而**实测基线里 session_turns 这 5 列全缺** ⇒ 落点不成立 ⇒ 门必须红）
#   M13 ★★ `none` 条目的**效果落点列清空**（理由保留）⇒ bash 门红（散文不算依据）
#        ★★ **这条同时是「bash 侧读册语义」那条修复的证明**：
#          「落点空、理由非空」正是**人最自然的写法**，而 bash 的
#          `read`（IFS=TAB）会**折叠空字段** ⇒ 修复前这条判据是**瞎的**
#          ⇒ ⇒ 门改成用 \x1f 分隔后，M13 才真的能红。
#   M14 ★ 新增一条非纯数字前缀迁移到 canonical 的 **`up/` 子目录** ⇒ bash 门红
#        （★ 这条证明**递归穷举**是承重的：子目录从前完全隐形）
#   M15 ★★ 在 **embeddata 宇宙**新增一条非纯数字前缀迁移 ⇒ 两道门都红
#        （★ 这条证明**第二宇宙**也被穷举：R82 轮的门在这个宇宙上是瞎的）
#   M16 ★★ **从通道 files=() 里删掉一条**（册子仍声明走通道腿）
#        ⇒ bash 门红（★ 覆盖「声明走通道腿 ⇒ 必须真的在 files=() 里」这条判据
#          —— R82 轮的 15 条变异**没有一条覆盖它**）
#
#   ── B 组：对**门自己**的变异 ⇒ 证明判据不是靠「别的判据在红」蒙混过关 ──
#   M17 ★ 把 bash 门的非前缀分类从「按文件名」改成「按相对路径」
#        ⇒ bash 门必须红（★ 这正是 R82 轮第一版的真实错误：它把整个 up/ 子目录
#          误判成非前缀 ⇒ 门当场红在 13 条 up/ 文件上）
#        ⚠ R83 轮第一版这条的锚点写成 `^awk -F/ ...`，而文件里那行**前面有 4 个空格**
#          ⇒ 锚点**一次都没命中** ⇒ 变异"没落地"却跑过了 ⇒ **假通过**
#          ⇒ 现在改用**逐字锚点**（mutate_text：必须恰好命中一次）。
#   M18 ★ **同时**把 find 改回 `-maxdepth 1` **并且**新增 `up/912b_…`
#        ⇒ bash 门必须红（★ 两个因子各改一个都抓不到；合起来才抓得到
#          ⇒ 说明「递归」这条判据确实承重，而不是装饰）
#   M19 ★★ 登记册某行**少一列**（删掉理由列）⇒ Go 门必须红（列数门）
#        （★ 覆盖 R83 轮新加的「列数必须恰好 4 列」判据）
#   M20 ★★★ 把 `registry_rows()` 的**行分隔符去掉** ⇒ bash 门必须红（行数自检）
#        （★★ 这条是 R83 轮**实测出的最大缺陷**的专属变异：
#          第一版 printf 漏了 `\n` ⇒ 15 行被拼成 1 行 ⇒ ⇒ **③/④ 只检查了第一行**
#          ⇒ ⇒ **门在基线上是绿的、但 90% 是瞎的**
#          ⇒ ⇒ ★ **基线绿 + 全部变异红，也可能是假绿**）
#
# 自我约束（沿用 verify-l11b-cost-view-rowset.sh 的教训）：
#
#   1. **编译红不算数**："build failed" 不是 "assertion went red"。
#   2. **变异的「红」必须能归因**：expect_red 会回显断言消息，读者必须能看出
#      这条红确实来自本条变异对应的那条判据。
#   3. **变异必须真的落地**：助手失败 / 内容没变 ⇒ 直接判红（不可归因）。
#   4. **新增文件立刻删**：否则后续每条变异都从脏工作树开始 ⇒ 红的归因不了。
#   5. 还原按**原路径** md5 逐字校验。
#   6. **残留只查本脚本动过的路径** —— 开发者完全可能同时在改别的文件
#      （R83 轮实测：跑变异的同时在写 handoff / LESSONS ⇒ 「整个工作树干净」
#      会把合法的并发改动误判成「变异残留」⇒ 第一版就是这么假失败的）。
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
CHANNEL_SCRIPT="scripts/apply-db-revision-sequence.sh"
EMBED_DIR="installer/cmd/llm-gw-installer/embeddata/startup"

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

# mutate_text 用**逐字锚点**替换一段文本（必须恰好命中一次）。
mutate_text() {
  local file="$1" old_b64="$2" new_b64="$3"
  python3 - "$file" "$old_b64" "$new_b64" <<'PY'
import base64, sys
path, old_b64, new_b64 = sys.argv[1:4]
old = base64.b64decode(old_b64).decode("utf-8")
new = base64.b64decode(new_b64).decode("utf-8")
with open(path, "r", encoding="utf-8") as handle:
    content = handle.read()
count = content.count(old)
if count != 1:
    sys.exit("anchor found %d times in %s, want exactly 1" % (count, path))
with open(path, "w", encoding="utf-8") as handle:
    handle.write(content.replace(old, new, 1))
print("   mutation applied to %s" % path)
PY
}

# mutate_regex 用正则替换。
#
# ⚠⚠⚠ **两个必须**（都是本仓栽过的坑）：
#   1. **必须带 re.M** —— 否则 `^`/`$` 只匹配整串首尾 ⇒ 多行文件永远匹配不到
#      ⇒ **变异没落地却看起来跑过了** ⇒ 假通过（R81 轮 M15 就是这么假通过的）。
#   2. **替换串必须用 lambda 传入** —— Python 的 `re.sub` 会把替换串里的
#      `\n`/`\t`/`\1` 当转义处理 ⇒ 我们要塞进去的是**字面量**（比如 TSV 的
#      真 TAB、正则里的 `\(`）⇒ 走 lambda 才不会被二次解释。
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

# mutate_clear_field 清空登记册某行的第 N 列（0-based）。
#
# ⚠⚠ **为什么不用正则的反向引用 `\1`**：mutate_regex 的替换串走 **lambda**
#   （否则 re.sub 会把替换串里的 \n/\t/\1 二次解释）⇒ ★ **反向引用在 lambda 里
#   不生效** ⇒ 替换串里的 `\1` 会变成**字面量 "\1"** ⇒ 变异确实"落地"了，
#   **但落地的形状不是我们想要的** ⇒ ⇒ M11/M13 会红在错的地方
#   （R83 轮第一版就是这么写的：\1 把「落点列」变成了字面量 "\1"，
#     而 bash 的 read 折叠空字段 ⇒ 「没有写效果落点」那条判据压根不触发）。
#   ⇒ ⇒ 改用**按列清空**这个语义明确的助手。
mutate_clear_field() {
  local file="$1" key="$2" col="$3"
  python3 - "$file" "$key" "$col" <<'PY'
import sys
path, key, col = sys.argv[1], sys.argv[2], int(sys.argv[3])
with open(path, "r", encoding="utf-8") as handle:
    lines = handle.read().split("\n")
hits = 0
for i, line in enumerate(lines):
    if not line.startswith(key + "\t"):
        continue
    fields = line.split("\t")
    if col >= len(fields):
        sys.exit("line %d has %d fields; cannot clear col %d" % (i + 1, len(fields), col))
    fields[col] = ""
    lines[i] = "\t".join(fields)
    hits += 1
if hits != 1:
    sys.exit("key %s matched %d lines, want exactly 1" % (key, hits))
with open(path, "w", encoding="utf-8") as handle:
    handle.write("\n".join(lines))
print("   mutation applied to %s (cleared col %d of %s)" % (path, col, key))
PY
}

# mutate_drop_field **整列删掉**登记册某行的一列（0-based）—— 与「清空」不同：
# 清空保留列数（⇒ Go 侧看到的仍是 4 列），删列才会让列数变少（⇒ 触发列数门）。
mutate_drop_field() {
  local file="$1" key="$2" col="$3"
  python3 - "$file" "$key" "$col" <<'PY'
import sys
path, key, col = sys.argv[1], sys.argv[2], int(sys.argv[3])
with open(path, "r", encoding="utf-8") as handle:
    lines = handle.read().split("\n")
hits = 0
for i, line in enumerate(lines):
    if not line.startswith(key + "\t"):
        continue
    fields = line.split("\t")
    if col >= len(fields):
        sys.exit("line %d has %d fields; cannot drop col %d" % (i + 1, len(fields), col))
    del fields[col]
    lines[i] = "\t".join(fields)
    hits += 1
if hits != 1:
    sys.exit("key %s matched %d lines, want exactly 1" % (key, hits))
with open(path, "w", encoding="utf-8") as handle:
    handle.write("\n".join(lines))
print("   mutation applied to %s (dropped col %d of %s)" % (path, col, key))
PY
}

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
GO_LEG='TestNonPlainRegistryLandingKindsAreKnown'

PROBE_SQL="-- probe: 非纯数字前缀迁移（审计变异用）
CREATE TABLE IF NOT EXISTS public.zzz_probe_delivery_marker (id bigint PRIMARY KEY);
"
PROBE_B64="$(b64 "$PROBE_SQL")"

echo "=== 非纯数字前缀迁移的投递登记册门：变异验证 ==="
echo "repo: $REPO_ROOT"
echo

backup "$REGISTRY"
backup "$BASH_GATE"
backup "$CHANNEL_SCRIPT"

expect_green_bash "baseline"
expect_green_go "baseline" "$GO_MAIN|$GO_LEG"

# ---------------------------------------------------------------------------
# M1 — 新增一条非纯数字前缀迁移（canonical 顶层）⇒ bash 门必须红（漏登记）
# ---------------------------------------------------------------------------
PROBE1="sql/migrations/startup/912b_zzz_probe_delivery.sql"
if run_mutation_new "M1" "$PROBE1" add_file "$PROBE1" "$PROBE_B64"; then
  expect_red_bash "M1" 'canonical/912b_zzz_probe_delivery\.sql.*解析不出纯三位数字前缀'
  remove_added "$PROBE1"
fi

# ---------------------------------------------------------------------------
# M2 — 同一条变异，从 Go 侧看 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation_new "M2" "$PROBE1" add_file "$PROBE1" "$PROBE_B64"; then
  expect_red_go "M2" 'canonical/912b_zzz_probe_delivery\.sql.*解析不出纯三位数字前缀' "$GO_MAIN"
  remove_added "$PROBE1"
fi

# ---------------------------------------------------------------------------
# M3 — 登记册里删掉一条（361b）⇒ bash 门必须红（漏登记）
# ---------------------------------------------------------------------------
if run_mutation "M3" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/361b_provider_models_source_prelude\.sql\t.*$' ''; then
  expect_red_bash "M3" 'canonical/361b_provider_models_source_prelude\.sql.*解析不出纯三位数字前缀'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M4 — 同一条变异，从 Go 侧看 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M4" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/361b_provider_models_source_prelude\.sql\t.*$' ''; then
  expect_red_go "M4" 'canonical/361b_provider_models_source_prelude\.sql.*解析不出纯三位数字前缀' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M5 — 登记册里塞一条**陈旧条目**（磁盘上没有）⇒ bash 门必须红
#
# ⚠⚠ **必须替换「整行」而不是行首一小段**：只替换行首的话，原行**剩下的部分**
#   会留在下一行 ⇒ 那行没有 TAB ⇒ Go 侧读册子时先撞「字段数 < 3」的格式错
#   ⇒ **红在了错的地方**（R82 轮 M6 第一版就是这么假通过的）。
# ---------------------------------------------------------------------------
GHOST_LINE='canonical/999z_ghost_migration_that_does_not_exist.sql	none	baseline:usage_ledger	这条文件在磁盘上不存在：门必须报「登记册陈旧」。'
if run_mutation "M5" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/2026-07-13-multimodal-token-fields-hot\.sql\t.*$' \
  "$(b64 "$GHOST_LINE")"; then
  expect_red_bash "M5" 'canonical/999z_ghost_migration_that_does_not_exist\.sql.*登记册陈旧'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M6 — 同一条陈旧条目，从 Go 侧看 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M6" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/2026-07-13-multimodal-token-fields-hot\.sql\t.*$' \
  "$(b64 "$GHOST_LINE")"; then
  expect_red_go "M6" 'canonical/999z_ghost_migration_that_does_not_exist\.sql.*登记册陈旧' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M7 — 投递腿改成 none，但该文件**在**通道 files=() 里 ⇒ bash 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M7" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/487b_request_logs_hot_system_fingerprint\.sql\tchannel\t' \
  "$(b64 'canonical/487b_request_logs_hot_system_fingerprint.sql	none	')"; then
  expect_red_bash "M7" 'canonical/487b_request_logs_hot_system_fingerprint\.sql.*声明与事实不符'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M8 — 投递腿改成 none，但该文件**在** embeddata 里 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M8" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^embeddata/361b_provider_models_source_prelude\.sql\tinstaller\t' \
  "$(b64 'embeddata/361b_provider_models_source_prelude.sql	none	')"; then
  expect_red_go "M8" 'embeddata/361b_provider_models_source_prelude\.sql.*声明与事实不符' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M9 — 投递腿改成 installer，但该文件不在 StartupFiles ⇒ bash 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M9" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/487b_request_logs_hot_system_fingerprint\.sql\tchannel\t' \
  "$(b64 'canonical/487b_request_logs_hot_system_fingerprint.sql	installer	')"; then
  expect_red_bash "M9" 'canonical/487b_request_logs_hot_system_fingerprint\.sql.*StartupFiles'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M10 — 投递腿写成非法值 ⇒ Go 门必须红（登记册格式门）
# ---------------------------------------------------------------------------
if run_mutation "M10" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/487c_request_logs_hot_raw_model_name\.sql\tchannel\t' \
  "$(b64 'canonical/487c_request_logs_hot_raw_model_name.sql	unknown-leg	')"; then
  expect_red_go "M10" '投递腿 .* 非法' "$GO_MAIN|$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M11 — 理由列清空 ⇒ Go 门必须红（决定必须留理由）
# ---------------------------------------------------------------------------
if run_mutation "M11" "$REGISTRY" mutate_clear_field "$REGISTRY" \
  "canonical/478p2_affinity_indexes.sql" 3; then
  expect_red_go "M11" '没有写理由' "$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M12 ★★ **效果落点不成立** ⇒ bash 门必须红
#      （把落点改成基线里**不存在的列** —— ★★ 这正是 R82 轮那个错的形状：
#        R82 轮给 2026-07-13-… 写的理由引用了 `session_turns.reasoning_tokens`，
#        而实测基线里 session_turns 这 5 列全缺）
# ---------------------------------------------------------------------------
if run_mutation "M12" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/2026-07-13-multimodal-token-fields-hot\.sql\tnone\t' \
  "$(b64 'canonical/2026-07-13-multimodal-token-fields-hot.sql	none	baseline:session_turns.reasoning_tokens	')"; then
  expect_red_bash "M12" 'public\.session_turns 没有 reasoning_tokens 这一列.*效果落点不成立'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M13 ★★ `none` 条目的**效果落点列清空** ⇒ bash 门必须红（散文不算依据）
# ---------------------------------------------------------------------------
if run_mutation "M13" "$REGISTRY" mutate_clear_field "$REGISTRY" \
  "canonical/395c_canonical_raw_name_unprefix.sql" 2; then
  expect_red_bash "M13" '声明「有意不投递」却没有写效果落点'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M14 ★ 新增一条非纯数字前缀迁移到 canonical 的 **`up/` 子目录** ⇒ bash 门红
#      （★ 这条证明**递归穷举**是承重的：子目录从前完全隐形）
# ---------------------------------------------------------------------------
PROBE3="sql/migrations/startup/up/912b_zzz_probe_in_subdir.sql"
if run_mutation_new "M14" "$PROBE3" add_file "$PROBE3" "$PROBE_B64"; then
  expect_red_bash "M14" 'canonical/up/912b_zzz_probe_in_subdir\.sql.*解析不出纯三位数字前缀'
  remove_added "$PROBE3"
fi

# ---------------------------------------------------------------------------
# M15 ★★ 在 **embeddata 宇宙**新增一条非纯数字前缀迁移 ⇒ 两道门都红
#      （★ 这条证明**第二宇宙**也被穷举：R82 轮的门在这个宇宙上是瞎的）
# ---------------------------------------------------------------------------
PROBE5="$EMBED_DIR/912b_zzz_probe_in_embeddata.sql"
if run_mutation_new "M15" "$PROBE5" add_file "$PROBE5" "$PROBE_B64"; then
  expect_red_bash "M15" 'embeddata/912b_zzz_probe_in_embeddata\.sql.*解析不出纯三位数字前缀'
  expect_red_go "M15" 'embeddata/912b_zzz_probe_in_embeddata\.sql.*解析不出纯三位数字前缀' "$GO_MAIN"
  remove_added "$PROBE5"
fi

# ---------------------------------------------------------------------------
# M16 ★★ **从通道 files=() 里删掉一条**（册子仍声明走通道腿）⇒ bash 门必须红
#      （★ 覆盖「声明走通道腿 ⇒ 必须真的在 files=() 里」这条判据 ——
#        R82 轮的 15 条变异**没有一条覆盖它** ⇒ 那条判据当时是**未被验证的**）
# ---------------------------------------------------------------------------
if run_mutation "M16" "$CHANNEL_SCRIPT" mutate_regex "$CHANNEL_SCRIPT" \
  '^  "\$ROOT_DIR/sql/migrations/startup/706b_session_turns_hot_prelude\.sql"$' \
  ''; then
  expect_red_bash "M16" 'canonical/706b_session_turns_hot_prelude\.sql.*不在 scripts/apply-db-revision-sequence\.sh 的 files=\(\) 里'
  restore "$CHANNEL_SCRIPT"
fi

# ---------------------------------------------------------------------------
# M17 ★ 把 bash 门的非前缀分类从「按文件名」改成「按相对路径」
#      ⇒ bash 门必须红（★ 这正是 R82 轮第一版的真实错误）
# ---------------------------------------------------------------------------
# ⚠ 用**逐字锚点**（mutate_text）而不是正则：
#   正则版的锚点写成 `^awk -F/ ...`，而文件里那一行**前面有 4 个空格** ⇒
#   锚点**一次都没命中** ⇒ 变异"没落地"却看起来跑过了 ⇒ **假通过**
#   （这正是本仓栽过的第 N 次「变异没落地」）。逐字替换只要求**恰好命中一次**。
#
# ⚠⚠⚠ **锚点文本里不能出现 `)`**（当它被包在 `$( )` 命令替换里时）——
#   实测（macOS 自带 bash 3.2.57）：`X=$(cat <<'EOF' …含 ')' 的一行… EOF )`
#   ⇒ **语法错误**「syntax error near unexpected token `)'」
#   ⇒ 原因：bash 3.2 在解析 `$( )` 时先找配对的 `)`，**heredoc 体里的 `)` 也被算进去**
#   ⇒ ⇒ 变异的锚点里恰好有 `sort)` ⇒ 第一版就是这么写崩的
#   ⇒ ⇒ **修法：heredoc 写【文件】，再用 `$(cat 文件)` 取**（这一步没有 `$( )` 包裹 heredoc）。
#   ⇒ ★ 这是本仓栽过的**第五次**「变异没落地 / 写崩脚本」⇒ 通用式：
#     **变异脚本自己先要能跑通**（`bash -n` 通过只是第一步，还要真的能落地）。
cat > "$WORK_DIR/m17_old.txt" <<'EOF'
    printf '%s\n' "$canon_up_bodies" | awk -F/ '$NF !~ /^[0-9][0-9][0-9]_/ {print "canonical/" $0}'
EOF
cat > "$WORK_DIR/m17_new.txt" <<'EOF'
    printf '%s\n' "$canon_up_bodies" | awk '{ if ($0 !~ /^[0-9][0-9][0-9]_/) print "canonical/" $0 }'
EOF
M17_OLD="$(cat "$WORK_DIR/m17_old.txt")"
M17_NEW="$(cat "$WORK_DIR/m17_new.txt")"
if run_mutation "M17" "$BASH_GATE" mutate_text "$BASH_GATE" \
  "$(b64 "$M17_OLD")" "$(b64 "$M17_NEW")"; then
  expect_red_bash "M17" 'canonical/up/376_autoupdate\.sql.*解析不出纯三位数字前缀'
  restore "$BASH_GATE"
fi

# ---------------------------------------------------------------------------
# M18 ★ **同时**把 find 改回 `-maxdepth 1` **并且**新增 `up/912b_…`
#      ⇒ bash 门必须红（★ 两个因子各改一个都抓不到；合起来才抓得到）
# ---------------------------------------------------------------------------
PROBE6="sql/migrations/startup/up/912b_zzz_probe_in_subdir.sql"
cat > "$WORK_DIR/m18_old.txt" <<'EOF'
  && find . -type f -name '*.sql' ! -name '*.down.sql' | sed 's|^\./||' | sort)
EOF
cat > "$WORK_DIR/m18_new.txt" <<'EOF'
  && find . -maxdepth 1 -type f -name '*.sql' ! -name '*.down.sql' | sed 's|^\./||' | sort)
EOF
M18_OLD="$(cat "$WORK_DIR/m18_old.txt")"
M18_NEW="$(cat "$WORK_DIR/m18_new.txt")"
if run_mutation_new "M18" "$PROBE6" add_file "$PROBE6" "$PROBE_B64"; then
  if run_mutation "M18" "$BASH_GATE" mutate_text "$BASH_GATE" \
    "$(b64 "$M18_OLD")" "$(b64 "$M18_NEW")"; then
    expect_red_bash "M18" 'canonical/up/912b_zzz_probe_in_subdir\.sql.*解析不出纯三位数字前缀|穷举没有扫到 up/600_outbound_body_to_bodies_hot\.sql'
  fi
  restore "$BASH_GATE"
  remove_added "$PROBE6"
fi

# ---------------------------------------------------------------------------
# M19 ★★ 登记册某行**少一列**（删掉理由列）⇒ Go 门必须红（列数门）
#      （★ 这条覆盖 R83 轮新加的「列数必须恰好 4 列」判据：
#        之前 bash 侧用 IFS=TAB 的 read 读册子 ⇒ 空字段被折叠 ⇒
#        「列数不对」这件事在 bash 侧根本表达不出来 ⇒ 现在 Go 侧明确接住）
# ---------------------------------------------------------------------------
if run_mutation "M19" "$REGISTRY" mutate_drop_field "$REGISTRY" \
  "canonical/2026-07-13-multimodal-token-fields-hot.sql" 3; then
  expect_red_go "M19" '列数是 3' "$GO_MAIN|$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M20 ★★★ **把 `registry_rows()` 的行分隔符（\n）去掉** ⇒ bash 门必须红
#      （★★ 这条是 R83 轮**实测出的最大缺陷**的专属变异：
#        第一版 printf **漏了 \n** ⇒ 15 行被拼成 1 行 ⇒ ⇒ **③/④ 两个循环
#        只检查了登记册的第一行** ⇒ ⇒ **门在基线上是绿的、但 90% 是瞎的**
#        ⇒ ⇒ ★ **基线绿 + 全部变异红，也可能是假绿** ——
#        只有「针对非第一行的变异」（M7）才抓得到它）
#      ⇒ 修法 = 给门加**行数自检**（「读到几行」本身变成一条判据）
# ---------------------------------------------------------------------------
cat > "$WORK_DIR/m20_old.txt" <<'EOF'
  awk -F'\t' '!/^#/ && NF>=2 {printf "%s%c%s%c%s%c%s\n", $1, 31, $2, 31, $3, 31, $4}' \
EOF
cat > "$WORK_DIR/m20_new.txt" <<'EOF'
  awk -F'\t' '!/^#/ && NF>=2 {printf "%s%c%s%c%s%c%s", $1, 31, $2, 31, $3, 31, $4}' \
EOF
M20_OLD="$(cat "$WORK_DIR/m20_old.txt")"
M20_NEW="$(cat "$WORK_DIR/m20_new.txt")"
if run_mutation "M20" "$BASH_GATE" mutate_text "$BASH_GATE" \
  "$(b64 "$M20_OLD")" "$(b64 "$M20_NEW")"; then
  expect_red_bash "M20" '登记册读取的行数（[0-9]+）与数据行数（[0-9]+）不一致'
  restore "$BASH_GATE"
fi

# ---------------------------------------------------------------------------
# 收尾：还原校验 + 变异残留校验
# ---------------------------------------------------------------------------
echo
echo "── 还原校验"
restore "$REGISTRY"
restore "$BASH_GATE"
restore "$CHANNEL_SCRIPT"

echo
echo "── 变异残留校验（只查**本脚本动过的路径**）"
RESIDUE=""
RESIDUE+=$(git status --porcelain -- sql/migrations/startup/ | sed 's/^/    /')
RESIDUE+=$(git status --porcelain -- "$EMBED_DIR" | sed 's/^/    /')
for leftover in \
  sql/migrations/startup/912b_zzz_probe_delivery.sql \
  sql/migrations/startup/up/912b_zzz_probe_in_subdir.sql \
  "$EMBED_DIR/912b_zzz_probe_in_embeddata.sql"; do
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

echo "── 被改文件还原校验（按原路径 md5）"
for f in "$REGISTRY" "$BASH_GATE" "$CHANNEL_SCRIPT"; do
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
