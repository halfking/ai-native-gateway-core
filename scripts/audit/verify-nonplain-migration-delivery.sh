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
#   ── C 组：R84 轮新增（5 条）—— 审 R83 轮自己交付的门时挖出的真缺陷 ──────
#   M21 ★★★ **收窄「读册子」的过滤器**（只改读的那一处）⇒ bash 门必须红
#        （★★ 这条覆盖 R84 轮修掉的**真缺陷**：R83 轮那条「行数自检」的
#          【参考计数】与【读册子】**共用同一个谓词** ⇒ ⇒ **同源自证**
#          ⇒ ⇒ 过滤器一旦被收窄、两边**同时**少读 ⇒ ⇒ 计数**仍然相等**
#          ⇒ ⇒ 自检看不见 ⇒ ⇒ ③/④ 少检查了一行而门**是绿的**）
#   M22 ★★★★ **复合变异**：收窄读册子的过滤器 ＋ 把那一行的投递腿改成假的
#        ⇒ bash 门必须红（★★★★ 这条是 **R84 轮实测出的那个「假绿」的专属变异**：
#          ①②**各自单做**都抓不到它（①不红、②红），**合起来才绿**
#          ⇒ ⇒ ★ 通用式：★ **只做单点变异的集合【天然看不见】复合盲区**）
#   M23 ★★ 登记册某行**只有 3 列** ⇒ bash 门必须红（R84 轮新增的**列数门**）
#   M24 ★★ 登记册里加一行**带前导空白的注释行**（含 TAB）⇒ 两道门都**绿**
#        （★ **期望绿**的用例：它证明 bash 与 Go 对「缩进注释」的理解**一致**）
#   M25 ★★★ **清空登记册的数据行** ⇒ 两道门都**必须红**
#        （★★★ 「观测手段失效」的行为化检验：门把登记册读成 0 条时**必须红**
#          ⇒ 这也是 R84 轮**选题①普查**用的方法：把登记册掏空，看门的表现）
#   M26 ★★★★ **复合变异（期望绿）**：收窄读册子的过滤器 ＋ **同时**让参考计数
#        **也少算那一行** ⇒ ⇒ 两边**同时**少读 ⇒ ⇒ 计数**仍然相等** ⇒ ⇒ 门**仍绿**
#        （★★★ 这条**不是找缺陷**，而是**证明 R84 轮那处修复是【承重的】**：
#          「独立参考计数」的全部价值 = 「单边被收窄时计数会不等 ⇒ 门红」
#          ⇒ ⇒ 参考计数若也一起被收窄 ⇒ ⇒ 这条修复**完全失效** ⇒ ⇒ 门变瞎
#          ⇒ ⇒ ★ M26 是 **M21 的反向自检**（★ LESSONS §294：
#          「判定式必须配反向自检」⇒ ★ M21 说「单边收窄 ⇒ 红」，
#          M26 说「双边收窄 ⇒ 绿」⇒ ⇒ 两条合起来才钉住「独立性」这个性质）
#          ⇒ ⇒ ★★ 真实用途是**守回归**：若有人把参考计数改回共用谓词
#          ⇒ ⇒ ⇒ M21 会变绿 ⇒ ⇒ M21 的「期望红」失败 ⇒ ⇒ 回归被 harness 抓到）
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
# R84 轮：把「期望绿」的用例**单独计数** ⇒ 它们与「期望红」不是一回事
# （期望绿 = 证明某条【对齐/收窄】确实生效；混进红绿一个计数器里会读不懂）
GREEN_EXPECTED=0
GREEN_OK=0
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

# ── R84 轮新增的三个变异助手 ────────────────────────────────────────────────
#
# mutate_append_line 在登记册**末尾追加一行**（用于「加一条带前导空白的注释行」
#   这类**形状**变异）。追加时保证文件末尾有换行 ⇒ 不会把上一行黏住。
mutate_append_line() {
  local file="$1" line_b64="$2"
  python3 - "$file" "$line_b64" <<'PY'
import base64, sys
path, line_b64 = sys.argv[1], sys.argv[2]
line = base64.b64decode(line_b64).decode("utf-8")
with open(path, "a", encoding="utf-8") as handle:
    if handle.tell() > 0:
        with open(path, "rb") as probe:
            probe.seek(-1, 2)
            if probe.read(1) != b"\n":
                handle.write("\n")
    handle.write(line + "\n")
print("   mutation applied to %s (appended 1 line)" % path)
PY
}

# mutate_empty_registry 把登记册的**数据行全部删掉**（只留注释行与空行）。
#   ★ 这是「观测手段失效」的行为化检验：门必须**红**（读了个空册子还绿 = 瞎）。
mutate_empty_registry() {
  local file="$1"
  python3 - "$file" <<'PY'
import sys
path = sys.argv[1]
with open(path, encoding="utf-8") as handle:
    lines = handle.read().split("\n")
kept = [line for line in lines if line.startswith("#") or line.strip() == ""]
with open(path, "w", encoding="utf-8") as handle:
    handle.write("\n".join(kept))
print("   mutation applied to %s (all data rows removed)" % path)
PY
}

# ★★★★ R84 轮新增：变异落地的【后置条件】自检（选题⑥：补「读回来看一眼」）。
#
#   R83 轮的「变异落地」自检**只查 md5 变了** ⇒ ⇒ 查不出「变了但形状不对」
#   （R83 轮第一版的 M11/M13 就是这么漏过去的：`\1` 反向引用在 lambda 里不生效
#     ⇒ 落地的是**字面量 "\1"** ⇒ md5 变了、但形状不是要的 ⇒ 那两条是【假通过】）。
#   ⇒ 这里补一条【读回来核对】的断言：变异落地后，把目标内容**读回来**核对。
#   ⇒ ★ 通用式：★ **「md5 变了」≠「变异是想要的那个」** ⇒ 落地判据必须能表达【形状】。
assert_contains() {
  local label="$1" file="$2" needle="$3"
  if ! grep -Fq -- "$needle" "$file"; then
    echo "   ✘ MUTATION LANDED BUT NOT IN THE INTENDED SHAPE — ${label}：${file} 里读不到期望内容"
    FAILED+=("$label(wrong-shape)")
    return 1
  fi
  echo "   ✔ 形状核对通过（读回来看一眼）：${label}"
  return 0
}

# assert_tsv_field 读回登记册某行的第 N 列（0-based）并与期望值比对。
assert_tsv_field() {
  local label="$1" file="$2" key="$3" col="$4" expected="$5"
  local got
  got=$(python3 - "$file" "$key" "$col" <<'PY'
import sys
path, key, col = sys.argv[1], sys.argv[2], int(sys.argv[3])
found = "<找不到该行>"
with open(path, encoding="utf-8") as handle:
    for line in handle.read().split("\n"):
        if line.startswith(key + "\t"):
            fields = line.split("\t")
            found = fields[col] if col < len(fields) else "<缺列>"
            break
print(found)
PY
)
  if [ "$got" != "$expected" ]; then
    echo "   ✘ MUTATION LANDED BUT NOT IN THE INTENDED SHAPE — ${label}：第 ${col} 列期望「${expected}」，实际「${got}」"
    FAILED+=("$label(wrong-shape)")
    return 1
  fi
  echo "   ✔ 形状核对通过（读回来看一眼）：${label}"
  return 0
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

# ★★★★ R84 轮新增：run_mutation_pair —— **两个文件**上的复合变异。
#
#   ★ 为什么需要它：R83 轮的「变异必须打到非第一行」这条纪律**只能覆盖单点变异**；
#     而 R84 轮实测出的那个**假绿**恰恰是**复合**的：
#       ① 读册子的过滤器被收窄（少读一行）＋
#       ② 那一行里的声明被改成假的（投递腿与事实不符）
#     ⇒ 单点变异抓不到它（要么只是「少读」不红、要么「声明错」被抓到），
#       **两个一起才绿** ⇒ ⇒ 只做单点变异的集合**天然看不见这一族**。
#   ⇒ 用法：run_mutation_pair <标签> <文件A> <文件B> <A 的命令…> -- <B 的命令…>
run_mutation_pair() {
  local label="$1" file1="$2" file2="$3"; shift 3
  local cmd1=()
  local cmd2=()
  local phase=1
  local arg
  for arg in "$@"; do
    if [ "$arg" = "--" ]; then
      phase=2
      continue
    fi
    if [ "$phase" -eq 1 ]; then
      cmd1+=("$arg")
    else
      cmd2+=("$arg")
    fi
  done
  if [ "${#cmd1[@]}" -eq 0 ] || [ "${#cmd2[@]}" -eq 0 ]; then
    echo "   ✘ MUTATION PAIR MALFORMED — ${label}（两段命令必须用 -- 分隔）⇒ 判红"
    FAILED+=("$label(malformed)")
    return 1
  fi
  local before1 before2 after1 after2
  before1="$(md5_of "$file1")"
  before2="$(md5_of "$file2")"
  if ! "${cmd1[@]}"; then
    echo "   ✘ MUTATION FAILED TO APPLY — ${label}（第一条助手报错）⇒ 判红"
    FAILED+=("$label(not-applied)")
    return 1
  fi
  if ! "${cmd2[@]}"; then
    echo "   ✘ MUTATION FAILED TO APPLY — ${label}（第二条助手报错）⇒ 判红"
    FAILED+=("$label(not-applied)")
    return 1
  fi
  after1="$(md5_of "$file1")"
  after2="$(md5_of "$file2")"
  if [ "$before1" = "$after1" ] || [ "$before2" = "$after2" ]; then
    echo "   ✘ MUTATION NOT APPLIED — ${label} 没有改变两个目标文件 ⇒ 不可归因 ⇒ 判红"
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
  if grep -qE 'syntax error|command not found|unbound variable' <<<"$output"; then
    echo "   ✘ HARNESS/BASH ERROR — the mutation broke the gate script itself; not a valid mutation"
    echo "$output" | tail -10
    FAILED+=("$label(harness-error)")
    return
  fi
  if ! grep -q 'contract FAILED' <<<"$output"; then
    echo "   ✘ STILL GREEN — the bash gate does not have teeth"
    echo "$output" | tail -12
    FAILED+=("$label(still-green)")
    return
  fi
  if ! grep -qE "$reason_pattern" <<<"$output"; then
    echo "   ✘ RED BUT NOT FOR THE RIGHT REASON — 没有出现预期的断言消息：$reason_pattern"
    grep -A3 'contract FAILED' <<<"$output" | head -12 | sed 's/^/     /'
    FAILED+=("$label(wrong-reason)")
    return
  fi
  echo "   ✔ RED as expected（断言消息已回显）"
  echo "   ↳ reason:"
  grep -E '^\s+\[[0-9]+\]' <<<"$output" | head -2 | sed 's/^/     /'
  MUTATIONS_RED=$((MUTATIONS_RED + 1))
}

expect_red_go() {
  local label="$1" reason_pattern="$2" test_pattern="$3"
  MUTATIONS_RUN=$((MUTATIONS_RUN + 1))
  echo "── MUTATION $label: expect RED (go gate)"
  local output
  output="$(cd installer && go test ./cmd/llm-gw-installer/ -run "$test_pattern" -count=1 2>&1)"
  if grep -qE '\[build failed\]|^# |cannot use|undefined:' <<<"$output"; then
    echo "   ✘ BUILD FAILED — the mutation does not compile; not a valid mutation"
    echo "$output" | head -12
    FAILED+=("$label(build)")
    return
  fi
  if ! grep -qE '^--- FAIL:' <<<"$output"; then
    echo "   ✘ STILL GREEN — the go gate does not have teeth"
    echo "$output" | tail -12
    FAILED+=("$label(still-green)")
    return
  fi
  if ! grep -qE "$reason_pattern" <<<"$output"; then
    echo "   ✘ RED BUT NOT FOR THE RIGHT REASON — 没有出现预期的断言消息：$reason_pattern"
    grep -E '^    [a-z_]+_test\.go:[0-9]+:' <<<"$output" | head -5 | sed 's/^/     /'
    FAILED+=("$label(wrong-reason)")
    return
  fi
  echo "   ✔ RED as expected（断言消息已回显）"
  echo "   ↳ reason:"
  grep -E '^    [a-z_]+_test\.go:[0-9]+:' <<<"$output" | head -3 | sed 's/^/     /'
  MUTATIONS_RED=$((MUTATIONS_RED + 1))
}

expect_green_bash() {
  local label="$1"
  GREEN_EXPECTED=$((GREEN_EXPECTED + 1))
  echo "── BASELINE $label: expect GREEN (bash gate)"
  local output
  output="$(bash "$BASH_GATE" 2>&1)"
  if grep -q 'contract FAILED' <<<"$output"; then
    echo "   ✘ RED at baseline:"
    grep -A6 'contract FAILED' <<<"$output" | head -10
    FAILED+=("$label(baseline-red)")
  else
    echo "   ✔ GREEN as expected"
    GREEN_OK=$((GREEN_OK + 1))
  fi
}

expect_green_go() {
  local label="$1" test_pattern="$2"
  GREEN_EXPECTED=$((GREEN_EXPECTED + 1))
  echo "── BASELINE $label: expect GREEN (go gate)"
  local output
  output="$(cd installer && go test ./cmd/llm-gw-installer/ -run "$test_pattern" -count=1 2>&1)"
  if grep -qE '^--- FAIL:' <<<"$output"; then
    echo "   ✘ RED at baseline:"
    echo "$output" | tail -20
    FAILED+=("$label(baseline-red)")
  else
    echo "   ✔ GREEN as expected"
    GREEN_OK=$((GREEN_OK + 1))
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
  assert_tsv_field "M7（487b 的投递腿被改成 none）" "$REGISTRY" \
    "canonical/487b_request_logs_hot_system_fingerprint.sql" 1 "none"
  expect_red_bash "M7" 'canonical/487b_request_logs_hot_system_fingerprint\.sql.*声明与事实不符'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M8 — 投递腿改成 none，但该文件**在** embeddata 里 ⇒ Go 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M8" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^embeddata/361b_provider_models_source_prelude\.sql\tinstaller\t' \
  "$(b64 'embeddata/361b_provider_models_source_prelude.sql	none	')"; then
  assert_tsv_field "M8（embeddata/361b 的投递腿被改成 none）" "$REGISTRY" \
    "embeddata/361b_provider_models_source_prelude.sql" 1 "none"
  expect_red_go "M8" 'embeddata/361b_provider_models_source_prelude\.sql.*声明与事实不符' "$GO_MAIN"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M9 — 投递腿改成 installer，但该文件不在 StartupFiles ⇒ bash 门必须红
# ---------------------------------------------------------------------------
if run_mutation "M9" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/487b_request_logs_hot_system_fingerprint\.sql\tchannel\t' \
  "$(b64 'canonical/487b_request_logs_hot_system_fingerprint.sql	installer	')"; then
  assert_tsv_field "M9（487b 的投递腿被改成 installer）" "$REGISTRY" \
    "canonical/487b_request_logs_hot_system_fingerprint.sql" 1 "installer"
  expect_red_bash "M9" 'canonical/487b_request_logs_hot_system_fingerprint\.sql.*StartupFiles'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M10 — 投递腿写成非法值 ⇒ Go 门必须红（登记册格式门）
# ---------------------------------------------------------------------------
if run_mutation "M10" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/487c_request_logs_hot_raw_model_name\.sql\tchannel\t' \
  "$(b64 'canonical/487c_request_logs_hot_raw_model_name.sql	unknown-leg	')"; then
  assert_tsv_field "M10（487c 的投递腿被写成 unknown-leg）" "$REGISTRY" \
    "canonical/487c_request_logs_hot_raw_model_name.sql" 1 "unknown-leg"
  expect_red_go "M10" '投递腿 .* 非法' "$GO_MAIN|$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M11 — 理由列清空 ⇒ Go 门必须红（决定必须留理由）
# ---------------------------------------------------------------------------
if run_mutation "M11" "$REGISTRY" mutate_clear_field "$REGISTRY" \
  "canonical/478p2_affinity_indexes.sql" 3; then
  assert_tsv_field "M11（478p2 的理由列被清空）" "$REGISTRY" \
    "canonical/478p2_affinity_indexes.sql" 3 ""
  expect_red_go "M11" '没有写理由' "$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M12 ★★ **效果落点不成立** ⇒ bash 门必须红
#      （把落点改成基线里**不存在的列** —— ★★ 这正是 R82 轮那个错的形状：
#        R82 轮给 2026-07-13-… 写的理由引用了 `session_turns.reasoning_tokens`，
#        而实测基线里 session_turns 这 5 列全缺）
# ---------------------------------------------------------------------------
# ⚠⚠⚠ **R84 轮实测出的第三处 harness 缺陷（本轮补断言后当场暴露）**：
#   第一版的锚点只匹配到「键<TAB>投递腿<TAB>」⇒ ⇒ 替换后**旧的效果落点被留在
#   后面** ⇒ ⇒ 那一行变成 **5 列**（不是 4 列）⇒ ⇒ ★ **红的理由变成了
#   「列数是 5」**（新列数门抓到的）⇒ ⇒ **而不是**本条要测的
#   「效果落点不成立」⇒ ⇒ ★ **「md5 变了」≠「变异是想要的那个」**
#   ⇒ ★⇒ 修法 = 锚点**吃掉整个旧的效果落点字段**（`[^\t]*`）⇒ 保持 4 列
#   ⇒ ★⇒ 并且**补上「读回来看一眼」断言** ⇒ ★⇒ 这条断言一加上就立刻
#   把「变异落地了但形状不对」变成**可归因**的诊断。
if run_mutation "M12" "$REGISTRY" mutate_regex "$REGISTRY" \
  '^canonical/2026-07-13-multimodal-token-fields-hot\.sql\tnone\t[^\t]*\t' \
  "$(b64 'canonical/2026-07-13-multimodal-token-fields-hot.sql	none	baseline:session_turns.reasoning_tokens	')"; then
  assert_tsv_field "M12（落点被换成基线里不存在的列 ⇒ 该行仍是 4 列）" "$REGISTRY" \
    "canonical/2026-07-13-multimodal-token-fields-hot.sql" 2 "baseline:session_turns.reasoning_tokens"
  expect_red_bash "M12" 'public\.session_turns 没有 reasoning_tokens 这一列.*效果落点不成立'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M13 ★★ `none` 条目的**效果落点列清空** ⇒ bash 门必须红（散文不算依据）
# ---------------------------------------------------------------------------
if run_mutation "M13" "$REGISTRY" mutate_clear_field "$REGISTRY" \
  "canonical/395c_canonical_raw_name_unprefix.sql" 2; then
  assert_tsv_field "M13（395c 的效果落点列被清空）" "$REGISTRY" \
    "canonical/395c_canonical_raw_name_unprefix.sql" 2 ""
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
  assert_tsv_field "M19（2026-07-13 那一行的理由列被删掉 ⇒ 3 列）" "$REGISTRY" \
    "canonical/2026-07-13-multimodal-token-fields-hot.sql" 3 "<缺列>"
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
# ⚠⚠⚠ **R84 轮实测出的第四处 harness 缺陷（★ 这次是 harness 自己抓到的）**：
#   R84 轮把 `registry_rows()` 从「单行 awk」改写成「多行 awk」（注释口径对齐 ＋
#   只输出 4 列的行）⇒ ⇒ **M20 的旧锚点不复存在** ⇒ ⇒ `mutate_text` 报
#   「anchor found 0 times」⇒ ⇒ ★ **变异没落地** ⇒ ⇒ 旧的 harness 只会报
#   「not-applied」（★ 这是**它自己的落地守卫**在起作用 —— ★ 若是更早的版本
#   只比 md5，这条会变成【假通过】⇒ ⇒ ★★ **「变异必须真的落地」这条守卫，
#   在本轮【替本轮挡住了一次】自欺**）。
#   ⇒ ⇒ 修法 = 锚点跟着实现走（改成多行 awk 里那行 printf）。
cat > "$WORK_DIR/m20_old.txt" <<'EOF'
    { printf "%s%c%s%c%s%c%s\n", $1, 31, $2, 31, $3, 31, $4 }
EOF
cat > "$WORK_DIR/m20_new.txt" <<'EOF'
    { printf "%s%c%s%c%s%c%s", $1, 31, $2, 31, $3, 31, $4 }
EOF
M20_OLD="$(cat "$WORK_DIR/m20_old.txt")"
M20_NEW="$(cat "$WORK_DIR/m20_new.txt")"
if run_mutation "M20" "$BASH_GATE" mutate_text "$BASH_GATE" \
  "$(b64 "$M20_OLD")" "$(b64 "$M20_NEW")"; then
  assert_contains "M20（printf 的行分隔符被去掉）" "$BASH_GATE" "$M20_NEW"
  expect_red_bash "M20" '登记册读取的行数（[0-9]+）与数据行数（[0-9]+）不一致'
  restore "$BASH_GATE"
fi

# ---------------------------------------------------------------------------
# M21 ★★★ **收窄「读册子」的过滤器**（只改读的那一处）⇒ bash 门必须红
#      （★★ 这条覆盖 R84 轮修掉的那个**真缺陷**：
#        R83 轮那条「行数自检」的【参考计数】与【读册子】**共用同一个谓词**
#        ⇒ ⇒ **同源自证** ⇒ ⇒ 过滤器一旦被收窄、两边**同时**少读
#        ⇒ ⇒ 计数**仍然相等** ⇒ ⇒ 自检看不见 ⇒ ⇒ ③/④ 少检查了一行而门是绿的）
#      ⇒ 修法 = 参考计数改成**独立**口径（三个不同的工具算）⇒ ⇒ 本条必须红
# ---------------------------------------------------------------------------
cat > "$WORK_DIR/m21_old.txt" <<'EOF'
    NF != 4 { next }
EOF
cat > "$WORK_DIR/m21_new.txt" <<'EOF'
    NF != 4 || $1 ~ /^canonical\/395c_canonical_raw_name_unprefix\.sql$/ { next }
EOF
M21_OLD="$(cat "$WORK_DIR/m21_old.txt")"
M21_NEW="$(cat "$WORK_DIR/m21_new.txt")"
if run_mutation "M21" "$BASH_GATE" mutate_text "$BASH_GATE" \
  "$(b64 "$M21_OLD")" "$(b64 "$M21_NEW")"; then
  assert_contains "M21（读册子的过滤器被收窄）" "$BASH_GATE" "$M21_NEW"
  expect_red_bash "M21" '登记册读取的行数（[0-9]+）与数据行数（[0-9]+）不一致'
  restore "$BASH_GATE"
fi

# ---------------------------------------------------------------------------
# M22 ★★★★ **复合变异**：收窄读册子的过滤器 **＋** 把那一行的投递腿改成假的
#      ⇒ bash 门必须红
#      （★★★★ 这条是 **R84 轮实测出的那个「假绿」的专属变异**：
#        ① 只收窄过滤器 ⇒ 门绿（少读了一行，但没人知道）；
#        ② 只改投递腿 ⇒ 门红（③ 判据抓得住）；
#        ③ **两个一起** ⇒ ★★ **修复前是绿的**（实测：bash 门绿 + Go 门绿）
#        ⇒ ⇒ ★ 通用式：★ **只做单点变异的集合【天然看不见】复合盲区**
#          ⇒ 复合盲区的形状 = 「观测手段变窄」×「被观测内容变假」
#      ⇒ 修复后：独立参考计数 ⇒ ① 立刻被抓 ⇒ 门红）
# ---------------------------------------------------------------------------
if run_mutation_pair "M22" "$BASH_GATE" "$REGISTRY" \
  mutate_text "$BASH_GATE" "$(b64 "$M21_OLD")" "$(b64 "$M21_NEW")" -- \
  mutate_regex "$REGISTRY" \
  '^canonical/395c_canonical_raw_name_unprefix\.sql\tnone\t' \
  "$(b64 'canonical/395c_canonical_raw_name_unprefix.sql	channel	')"; then
  assert_contains "M22（读册子的过滤器被收窄）" "$BASH_GATE" "$M21_NEW"
  assert_tsv_field "M22（395c 的投递腿被改成 channel）" "$REGISTRY" \
    "canonical/395c_canonical_raw_name_unprefix.sql" 1 "channel"
  expect_red_bash "M22" '登记册读取的行数（[0-9]+）与数据行数（[0-9]+）不一致'
  restore "$BASH_GATE"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M26 ★★★★ **复合变异（期望绿）**：收窄读册子的过滤器 ＋ **同时**把参考计数也改成
#      **少算那一行** ⇒ ⇒ 两边**同时**少读 ⇒ ⇒ 计数**仍然相等** ⇒ ⇒ 门**必须仍绿**
#
#      ⇒ ★★★ 这条**不是「找缺陷」，而是「证明 R84 轮那处修复是【承重的】」**：
#      ★ 「独立参考计数」这条修复的全部价值 = 「单边被收窄时计数会不等 ⇒ 门红」
#      ⇒ ⇒ **如果参考计数也一起被收窄，这条修复就【完全失效】**
#      ⇒ ⇒ M26 把这个失效场景**显式跑出来**（门绿 ⇒ 那一行不再被检查）
#      ⇒ ⇒ ⇒ 它是 **M21 的反向自检**（★ 本线纪律 §294：
#      ⇒ ⇒ ⇒   「判定式必须配反向自检」⇒ ⇒ M21 说「单边收窄 ⇒ 红」，
#      ⇒ ⇒ ⇒   M26 说「双边收窄 ⇒ 绿」⇒ ⇒ 两条合起来才钉住「独立性」这个性质）
#
#      ★★ 而 M26 的**真实用途是【守回归】**：
#      ⇒ 若将来有人把 `registry_reference_rows` 改回与读册子**共用谓词**
#      ⇒ ⇒ 则 M21 会**变绿**（两边同时少读 ⇒ 计数仍相等）
#      ⇒ ⇒ ★★ M21 的「期望红」就会失败 ⇒ ⇒ **回归会被 harness 抓到**（不必靠 M26）
#      ⇒ ⇒ 而 M26 补的是另一半：**证明「红」来自「单边」而不是「别的判据在红」**
#      ⇒ ⇒ （★ 若 M21 的红其实来自某条无关判据，M26 也会红 ⇒ ⇒ M26 立刻揭穿它）
#
#      ⇒ 诚实边界：★ 本条**期望绿** ⇒ ⇒ 它证明的是「这个形状下门确实会瞎」
#      ⇒ ⇒ ★ 它**不是**「门该绿」的许可 ⇒ ⇒ 恰恰相反：它是「独立性一旦被破坏，
#      ⇒ ⇒   门就会瞎」这一事实的**可复跑读数**。
#
# ★★★ 写这条时踩到的坑（★ 值得记，因为它就是本线的老熟人）：
#   ★ 第一版的参考计数变异写成 `$0 !~ /^canonical\/395c_...\.sql$/`
#   ⇒ ⇒ ★★ **该正则要求「整行恰好等于那个路径」** ⇒ ⇒ 而那一行后面还有别的列
#   ⇒ ⇒ ⇒ **排除从未触发** ⇒ ⇒ ⇒ 参考计数仍是 15、读册子是 14 ⇒ ⇒ ⇒ **门是红的**
#   ⇒ ⇒ ★★ ⇒ **「变异落地」（文本变了）≠「变异有预期效果」（语义生效了）**
#   ⇒ ⇒ ★★ ⇒ 而本条是**期望绿**的用例 ⇒ ⇒ ⇒ 它**当场把这条假落地揭穿了**
#   ⇒ ⇒ ⇒ ⇒ **「期望绿」用例的价值在这里又多了一条**：★ 它能揭穿
#   ⇒ ⇒ ⇒ ⇒ 「变异没生效」这种**假通过**（★ 而「期望红」的用例做不到：
#   ⇒ ⇒ ⇒ ⇒   一条无效变异会让门保持绿 ⇒ ⇒ ⇒ ⇒ 期望红的用例会**误判成「门有洞」**）。
#   ⇒ ⇒ ★★ 修法：按**首字段**匹配（`$1 !~ …`，且 awk 要 `-F'\t'`）⇒ ⇒ 实测 GREEN。
# ---------------------------------------------------------------------------
cat > "$WORK_DIR/m26_ref_old.txt" <<'EOF'
  total=$(awk 'END{print NR+0}' "$nonplain_registry")
EOF
cat > "$WORK_DIR/m26_ref_new.txt" <<'EOF'
  total=$(awk -F'\t' '$1 !~ /^canonical\/395c_canonical_raw_name_unprefix\.sql$/ {n++} END{print n+0}' "$nonplain_registry")
EOF
M26_REF_OLD="$(cat "$WORK_DIR/m26_ref_old.txt")"
M26_REF_NEW="$(cat "$WORK_DIR/m26_ref_new.txt")"
if run_mutation_pair "M26" "$BASH_GATE" "$BASH_GATE" \
  mutate_text "$BASH_GATE" "$(b64 "$M21_OLD")" "$(b64 "$M21_NEW")" -- \
  mutate_text "$BASH_GATE" "$(b64 "$M26_REF_OLD")" "$(b64 "$M26_REF_NEW")"; then
  assert_contains "M26（读册子的过滤器被收窄）" "$BASH_GATE" "$M21_NEW"
  assert_contains "M26（参考计数也少算同一行）" "$BASH_GATE" "$M26_REF_NEW"
  expect_green_bash "M26"
  restore "$BASH_GATE"
fi

# ---------------------------------------------------------------------------
# M23 ★★ 登记册某行**只有 3 列** ⇒ bash 门必须红（R84 轮新增的**列数门**）
#      （★ R83 轮把「列数 = 4」只放在 Go 侧 ⇒ bash 侧读到列数不对的行时
#        会把「第 3 列」当成效果落点 ⇒ 报出「效果落点语法非法」这种看不懂的错
#        ⇒ ⇒ 两道门对同一份册子的**形状**判断不一致 ⇒ 现在 bash 侧也核列数）
# ---------------------------------------------------------------------------
if run_mutation "M23" "$REGISTRY" mutate_drop_field "$REGISTRY" \
  "canonical/395c_canonical_raw_name_unprefix.sql" 3; then
  assert_tsv_field "M23（395c 的理由列被删掉 ⇒ 3 列）" "$REGISTRY" \
    "canonical/395c_canonical_raw_name_unprefix.sql" 3 "<缺列>"
  expect_red_bash "M23" '列数是 3'
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M24 ★★ 登记册里加一行**带前导空白的注释行**（且含一个 TAB）⇒ 两道门都**绿**
#      （★ 这条是【期望绿】的：它证明 bash 侧与 Go 侧对「缩进注释」的理解**一致**。
#        R83 轮的 bash 口径是 `!/^#/`（**不剥前导空白**）⇒ 一行带缩进的注释
#        会被当**数据行** ⇒ ② 会拿它当键去查目录 ⇒ 报「登记册陈旧（键的宇宙
#        前缀也非法）」这种**看不懂的假红** ⇒ ⇒ R84 轮把两侧口径对齐。
#        ⇒ 期望绿 = 证明「对齐」这件事真的生效了）
# ---------------------------------------------------------------------------
INDENT_COMMENT_LINE=$'   # R84 轮：带前导空白的注释行\t第二段含一个制表符'
if run_mutation "M24" "$REGISTRY" mutate_append_line "$REGISTRY" \
  "$(b64 "$INDENT_COMMENT_LINE")"; then
  expect_green_bash "M24"
  expect_green_go "M24" "$GO_MAIN|$GO_LEG"
  restore "$REGISTRY"
fi

# ---------------------------------------------------------------------------
# M25 ★★★ **清空登记册的数据行**（只留注释）⇒ 两道门都**必须红**
#      （★★★ 这条是「观测手段失效」的行为化检验：
#        门把登记册读成 0 条时，**必须红**（「读了个空册子还绿」= 门是瞎的）。
#        ★ 也是 R84 轮**选题①普查**用的那个方法：把登记册掏空，看门的表现。
#        ⇒ 本仓已普查的 10 份登记册里，**唯一**能把门读空的读取路径，
#          就是本门与 embed 门共享的那份 ⇒ 这条把它钉住）
# ---------------------------------------------------------------------------
if run_mutation "M25" "$REGISTRY" mutate_empty_registry "$REGISTRY"; then
  expect_red_bash "M25" '登记册一条数据行都没有|登记册读取的行数（[0-9]+）与数据行数（[0-9]+）不一致'
  expect_red_go "M25" '一条数据行都没有' "$GO_MAIN|$GO_LEG"
  restore "$REGISTRY"
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
echo "=== 结果：${MUTATIONS_RED}/${MUTATIONS_RUN} 条变异按预期变红；${GREEN_OK}/${GREEN_EXPECTED} 条「期望绿」用例保持绿 ==="
if [ "${#FAILED[@]}" -gt 0 ]; then
  echo "FAILED:"
  for f in "${FAILED[@]}"; do echo "  - $f"; done
  exit 1
fi
echo "RESULT: PASS"
