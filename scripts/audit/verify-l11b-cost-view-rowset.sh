#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# ---------------------------------------------------------------------------
# verify-l11b-cost-view-rowset.sh — 变异验证：证明 L11-b 登记门**不是恒绿**
#
# WHAT THIS IS
# ------------
# L11-b 是一条**已知缺陷**：auto-route 成本仪表盘的两个视图
# （customer_cost_view / model_cost_per_task_view）的**行集来源**仍是
# v1 冻结聚合表 `public.api_key_model_cost`（写方随迁移 890 删除）⇒
# 现在真正在跑 auto 流量的 API key 在视图里**没有行**（生产 252 只读实测：
# 视图行集 {1,10,11,17,29} 与近 30 天 auto 流量的 api_key
# {3,6,7,89,105,114,199} **零交集**）。
#
# 修法有三个候选（复活写侧触发器 / 视图改读会话族 / 明确废弃端点），
# **每个都改产品语义** ⇒ 属主拍板 ⇒ 仓内先立一道**登记门**：
# 「以冻结表作为行集来源的视图集合 == 登记册」—— 缺陷不许静悄悄扩散，
# 形态一旦被改就必须**显式重判**登记册。
#
# 本脚本给这道门装上**十六条可复核的牙**：
#
#   M1  迁移 883（部署形态）里 customer_cost_view 的顶层 FROM 换成会话族
#       ⇒ 「登记与形状不符」必须红
#   M2  基线形态文件 model_cost_per_task_view.sql 的顶层 FROM 换成会话族
#       ⇒ 同上（**只改一种形态时也要红** ⇒ 两种形态都在门眼里）
#   M3  登记册里删掉一条（这个已知缺陷没被登记）
#       ⇒ 红
#   M4  迁移 883 里加一个**第三个**读冻结表的视图（缺陷在扩散）
#       ⇒ 红
#   M5  把剥注释逻辑打坏（注释不再被忽略）
#       ⇒ 解析器自检必须红（否则 M1–M4 都是恒绿）
#   M6  把字符串字面量处理打坏
#       ⇒ 解析器自检必须红
#   M7  **被 apply 的基线**（sql/schema/01-schema.sql）里改掉顶层 FROM
#       ⇒ 红（★ 第一版门只钉了「不被 apply」的 sql/objects/ 副本 ⇒ 这条能抓住它）
#   M8  安装器内嵌基线（embeddata/01-schema.sql）里改掉顶层 FROM
#       ⇒ 红（同上，第三份被 apply 的基线也在门眼里）
#   M9  让解析器少认一个视图（提前 break）
#       ⇒ **解析完整性自检**必须红（漏解析 ⇒ 反向检查不可信）
#   M10 迁移目录里**多出一个**定义这两个视图的文件
#       ⇒ 「部署形态源唯一性」判据必须红
#   M11 安装器**内嵌目录**里多出一个定义体（新装通道同样是形态源）
#       ⇒ 必须红（★ 只扫迁移目录的话这条会漏）
#   M12 ★★ **部署基线**（deploy/sql/schemas/baseline/01-schema.sql）里改掉顶层 FROM
#       ⇒ 必须红（★ **续做轮实测抓到的真盲点**：这份**随离线包与一键安装分发**
#         的基线里同样定义着这两个视图，而上一版门**根本没钉它**
#         ⇒ 改掉它门照样绿 ⇒ 门是瞎的而外观完全正常）
#   M13 ★ 迁移 883 的 **down 体**（回滚形态）里改掉顶层 FROM
#       ⇒ 必须红（★ 上一版只把 down 体列进白名单，**没钉它的形状**
#         ⇒ 回滚之后的行集来源无人看管）
#   M14 ★★ **全新目录** + **无 schema 限定**的 `CREATE VIEW customer_cost_view`
#       ⇒ 必须红（★ 同时打两条性质：(a) 判据是**穷举全仓 .sql**而不是「扫几个
#         已知目录」⇒ 新目录也抓得到；(b) 正则认无 schema 限定的写法
#         ⇒ 仓内两份归档副本就是这种写法，上一版对它们**完全隐形**）
#   M15 允许清单里塞一条**陈旧条目**（指向一个不含定义体的文件）
#       ⇒ 「不许有陈旧条目」判据必须红
#   M16 把一个形态源的角色从「形态源」改成「归档」
#       ⇒ 「角色 ↔ 形态源清单一一对应」判据必须红
#
# ★ **本轮的门第一版就是被 M1/M2 抓住的**：它只断言「该视图在**某个**形态里
#   读冻结表」⇒ 改掉 883 那份、基线那份还在 ⇒ **照样绿** ⇒ 门会放过形态被改。
#   ⇒ **变异的价值就是把「文档里写的意图」与「代码实际断言的东西」对齐**。
#
# ★★ **M12 是同一个病的第二例**：上一版把形态源从 1 个扩到 6 个，
#   但**门自己的注释写着「三份 01-schema.sql 都是被 apply 的」而代码只钉了两份**
#   ⇒ ⇒ **「我钉的是不是被 apply 的那一份」**要升级成
#       **「我钉的是不是全部形态源」** ⇒ 现在形态源 9 个 + 判据**穷举全仓 .sql**。
#
# 两条自我约束（沿用 verify-g1-route-contract.sh 的教训）：
#
#   1. **编译红不算数**："build failed" 不是 "assertion went red"。
#   2. **变红的必须是预期集合里的测试**：预期外的红说明门红在了别处。
#
# 每个被改的文件都会被还原，并按 **md5 逐字校验**（还原不一致 = 脚本失败）。
#
# USAGE
#   bash scripts/audit/verify-l11b-cost-view-rowset.sh
#
# Requires: go on PATH（~/sdk/go/bin 会被自动补上）。
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

md5_of() {
  if command -v md5sum >/dev/null 2>&1; then
    md5sum "$1" | awk '{print $1}'
  else
    md5 -q "$1"
  fi
}

b64() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

backup() {
  local file="$1"
  mkdir -p "$BACKUP_DIR"
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

mutate() {
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

# mutate_drop_registry_entry 从门文件的登记册里删掉某个视图的整条登记。
# 用正则而不是逐字锚点：登记条目里有缩进（tab）与多行中文 ⇒ 逐字锚点极易
# 因为一个空格之差而失配（失配 = 脚本红，而不是"门红" ⇒ 测不到东西）。
mutate_drop_registry_entry() {
  local file="$1" view="$2"
  python3 - "$file" "$view" <<'PY'
import re, sys
path, view = sys.argv[1:3]
with open(path, "r", encoding="utf-8") as handle:
    content = handle.read()
pattern = re.compile(r'\n\t"' + re.escape(view) + r'": \{.*?\n\t\},\n', re.S)
new, n = pattern.subn("\n", content)
if n != 1:
    sys.exit("registry entry for %s matched %d times, want exactly 1" % (view, n))
with open(path, "w", encoding="utf-8") as handle:
    handle.write(new)
print("   mutation applied to %s (dropped registry entry %s)" % (path, view))
PY
}

# add_file 造一个**新文件**（用于「多了一个定义体」这类变异），并登记到清理列表。
add_file() {
  local file="$1" content_b64="$2"
  local dir
  dir="$(dirname "$file")"
  if [ ! -d "$dir" ]; then
    mkdir -p "$dir"
    echo "$dir" >> "$DIRS_LIST"
  fi
  mkdir -p "$(dirname "$file")"
  printf '%s' "$content_b64" | base64 -d > "$file"
  echo "$file" >> "$ADDED_LIST"
  echo "   mutation applied (new file) $file"
}

# remove_added 立刻删掉 add_file 造出来的文件。
#
# ⚠⚠⚠ **为什么必须「立刻」删，而不是等 cleanup**：新增文件型变异（M10/M11/M14）
#   造出来的文件会**留在工作树里** ⇒ 后续每一条变异跑的时候门都是红的
#   ⇒ **后续变异的「红」归因不了**（可能是上一步的残留造成的）
#   ⇒ ⇒ **量具故障：假通过**。
#   ★ 本轮真踩过：M15 的变异**根本没被应用**（正则漏了 re.M），
#     而 M10/M11/M14 的残留文件让门一直红着 ⇒ M15「红得很漂亮」但**是假的**。
#   ⇒ ⇒ 现在每条新增文件型变异跑完**立刻**删 ⇒ 后面的变异从干净工作树开始。
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
#
# ⚠⚠⚠ **为什么必须自检**：变异助手失败时（锚点找不到 / 正则没匹配），
#   它只是**打印一行错误**就返回 ⇒ 脚本继续往下跑 ⇒
#   **门因为别的原因红着**（比如上一条变异残留的文件）⇒
#   ⇒ ★ **「红」不可归因 ⇒ 假通过**。
#   ★ 本轮真踩过：M15 的变异**根本没被应用**（正则漏了 re.M），
#     而 M10/M11/M14 造出来的文件**还留在工作树里** ⇒ 门一直红着
#     ⇒ M15「红得很漂亮」但**是假的** ⇒ ⇒ 现在每条变异都自检落地 + 立刻清理。
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
# 用正则定位 ⇒ 不受 gofmt 对齐差异影响。
#
# ⚠⚠ **必须带 re.M**：否则 `^`/`$` 只匹配整个字符串的首尾 ⇒
#   多行文件里**永远匹配不到** ⇒ 变异**没被应用**却「看起来跑过了」
#   ⇒ ★ **量具故障：变异没落地，门却因为别的原因红着 ⇒ 假通过**。
#   （本轮真踩过：M15 就是这么假通过的 —— 见下面 remove_added 的教训。）
mutate_gate_line() {
  local file="$1" pattern="$2" insert="$3"
  python3 - "$file" "$pattern" "$insert" <<'PY'
import re, sys
path, pattern, insert = sys.argv[1:4]
with open(path, "r", encoding="utf-8") as handle:
    content = handle.read()
new, n = re.subn(pattern, lambda m: m.group(0) + "\n" + insert, content, count=1, flags=re.M)
if n != 1:
    sys.exit("pattern matched %d times in %s, want exactly 1" % (n, path))
with open(path, "w", encoding="utf-8") as handle:
    handle.write(new)
print("   mutation applied to %s (inserted a line)" % path)
PY
}

# mutate_gate_role 把允许清单里**某一条的角色**换掉（shape ↔ archived）。
#
# ⚠ 必须**替换**而不是追加：同一个 map literal 里出现重复 key ⇒ **编译错**
#   ⇒ 脚本会判成「无效变异」（编译红不算数）⇒ 测不到东西。
mutate_gate_role() {
  local file="$1" pattern="$2" from="$3" to="$4"
  python3 - "$file" "$pattern" "$from" "$to" <<'PY'
import re, sys
path, pattern, frm, to = sys.argv[1:5]
with open(path, "r", encoding="utf-8") as handle:
    content = handle.read()
match = re.search(pattern, content, re.M)
if not match:
    sys.exit("pattern not found in %s" % path)
line = match.group(0)
if line.count(frm) != 1:
    sys.exit("role %s appears %d times in the matched line, want 1" % (frm, line.count(frm)))
new_line = line.replace(frm, to)
new = content[:match.start()] + new_line + content[match.end():]
with open(path, "w", encoding="utf-8") as handle:
    handle.write(new)
print("   mutation applied to %s (role %s -> %s)" % (path, frm, to))
PY
}

expect_red() {
  local label="$1" pattern="$2"
  MUTATIONS_RUN=$((MUTATIONS_RUN + 1))
  echo "── MUTATION $label: expect RED"
  local output
  output="$(go test ./admin/ -run "$pattern" -count=1 2>&1)"
  if grep -qE '\[build failed\]|^# |cannot use|undefined:' <<<"$output"; then
    echo "   ✘ BUILD FAILED — the mutation does not compile; not a valid mutation"
    echo "$output" | head -10
    FAILED+=("$label(build)")
    return
  fi
  local failed unexpected
  failed="$(grep -oE '^--- FAIL: [A-Za-z0-9_]+' <<<"$output" | sed 's/^--- FAIL: //' | sort -u)"
  if [ -z "$failed" ]; then
    echo "   ✘ STILL GREEN — the gate does not have teeth"
    echo "$output" | tail -15
    FAILED+=("$label(still-green)")
    return
  fi
  unexpected="$(echo "$failed" | grep -vE "^($pattern)$" || true)"
  if [ -n "$unexpected" ]; then
    echo "   ✘ UNEXPECTED failures (the intended assertion may still hold): $unexpected"
    FAILED+=("$label(unexpected)")
    return
  fi
  echo "   ✔ RED as expected: $(echo $failed | tr '\n' ' ')"
  # ★ 把**断言消息**打出来：光有「红了」不能证明「红在对的地方」
  #   ⇒ 读者必须能看出这条红确实来自本条变异对应的那条判据。
  echo "   ↳ reason:"
  grep -E '^    [a-z_]+_test\.go:[0-9]+:' <<<"$output" | head -3 | sed 's/^/     /'
  MUTATIONS_RED=$((MUTATIONS_RED + 1))
}

expect_green() {
  local label="$1" pattern="$2"
  echo "── BASELINE $label: expect GREEN"
  local output
  output="$(go test ./admin/ -run "$pattern" -count=1 2>&1)"
  if grep -qE '^--- FAIL:' <<<"$output"; then
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
  # 变异期间新建的目录（只删那些本次新建、且现在已空的）
  if [ -f "$DIRS_LIST" ]; then
    while IFS= read -r dir; do
      [ -n "$dir" ] && rmdir "$dir" 2>/dev/null || true
    done < "$DIRS_LIST"
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

GATE="admin/auto_route_cost_views_rowset_source_test.go"
MIG883="sql/migrations/startup/883_session_family_cost_view_metrics.sql"
BASE_MODEL="sql/objects/views/model_cost_per_task_view.sql"

ROWSET='TestAutoRouteCostViewsRowsetSourceIsRegistered'
PARSER='TestCostViewRowsetParserSelfCheck'
EVIDENCE='TestCostViewRowsetRegistryIsBackedByRepoEvidence'
UNIQUE='TestCostViewRowsetNoOtherMigrationRedefinesTheViews'

echo "=== L11-b 成本视图行集来源登记门：变异验证 ==="
echo "repo: $REPO_ROOT"
echo

expect_green "baseline" "$ROWSET|$PARSER|$EVIDENCE|$UNIQUE"

# ---------------------------------------------------------------------------
# M1 — 部署形态：customer_cost_view 的顶层 FROM 换成会话族。
# ---------------------------------------------------------------------------
backup "$MIG883"
if run_mutation "M1" "$MIG883" mutate "$MIG883" \
  "$(b64 'FROM (public.api_key_model_cost akmc')" \
  "$(b64 'FROM (public.session_turns akmc')"; then
  expect_red "M1-deployed-shape-rowset-changed" "$ROWSET"
fi
restore "$MIG883"

# ---------------------------------------------------------------------------
# M2 — 基线形态：model_cost_per_task_view 的顶层 FROM 换成会话族。
#      （只改一种形态 ⇒ 也必须红 ⇒ 证明两种形态都在门眼里）
# ---------------------------------------------------------------------------
backup "$BASE_MODEL"
if run_mutation "M2" "$BASE_MODEL" mutate "$BASE_MODEL" \
  "$(b64 'FROM public.api_key_model_cost mcp')" \
  "$(b64 'FROM public.session_turns mcp')"; then
  expect_red "M2-baseline-shape-rowset-changed" "$ROWSET"
fi
restore "$BASE_MODEL"

# ---------------------------------------------------------------------------
# M3 — 登记册里删掉一条（这个已知缺陷没被登记）。
# ---------------------------------------------------------------------------
backup "$GATE"
if run_mutation "M3" "$GATE" mutate_drop_registry_entry "$GATE" "model_cost_per_task_view"; then
  expect_red "M3-registry-entry-dropped" "$ROWSET"
fi
restore "$GATE"

# ---------------------------------------------------------------------------
# M4 — 迁移 883 里加一个**第三个**读冻结表的视图（缺陷在扩散，却没登记）。
# ---------------------------------------------------------------------------
backup "$MIG883"
THIRD_VIEW=$'\n\nCREATE OR REPLACE VIEW public.mutation_third_view AS\n SELECT count(*) AS n FROM public.api_key_model_cost;\n'
if run_mutation "M4" "$MIG883" mutate "$MIG883" \
  "$(b64 "--   SELECT pg_get_viewdef('public.model_cost_per_task_view'::regclass, true) LIKE '%FROM session_turns st_par%';")" \
  "$(b64 "--   SELECT pg_get_viewdef('public.model_cost_per_task_view'::regclass, true) LIKE '%FROM session_turns st_par%';${THIRD_VIEW}")"; then
  expect_red "M4-third-frozen-sourced-view-not-registered" "$ROWSET"
fi
restore "$MIG883"

# ---------------------------------------------------------------------------
# M5 — 剥注释逻辑打坏（注释里的 FROM 不再被忽略）。
# ---------------------------------------------------------------------------
backup "$GATE"
if run_mutation "M5" "$GATE" mutate "$GATE" \
  "$(b64 "case c == '-' && i+1 < len(sql) && sql[i+1] == '-':")" \
  "$(b64 "case c == '-' && i+1 < len(sql) && sql[i+1] == '=':")"; then
  expect_red "M5-comment-stripping-broken" "$PARSER"
fi
restore "$GATE"

# ---------------------------------------------------------------------------
# M6 — 字符串字面量处理打坏（字面量里的括号打乱深度）。
# ---------------------------------------------------------------------------
backup "$GATE"
if run_mutation "M6" "$GATE" mutate "$GATE" \
  "$(b64 "case c == '\\'':")" \
  "$(b64 "case c == '@':")"; then
  expect_red "M6-string-literal-handling-broken" "$PARSER"
fi
restore "$GATE"

# ---------------------------------------------------------------------------
# M7 — **被 apply 的基线**（sql/schema/01-schema.sql）：改掉顶层 FROM。
#      ★ 这一条专门打第一版门的盲区：第一版只钉了「不被 apply」的 sql/objects/ 副本，
#        而那目录**根本没有任何 apply 通道** ⇒ 改真正被应用的基线时它是绿的。
# ---------------------------------------------------------------------------
BASE_SCHEMA="sql/schema/01-schema.sql"
backup "$BASE_SCHEMA"
if run_mutation "M7" "$BASE_SCHEMA" mutate "$BASE_SCHEMA" \
  "$(b64 'FROM (public.api_key_model_cost akmc')" \
  "$(b64 'FROM (public.session_turns akmc')"; then
  expect_red "M7-applied-baseline-schema-rowset-changed" "$ROWSET"
fi
restore "$BASE_SCHEMA"

# ---------------------------------------------------------------------------
# M8 — 安装器内嵌基线（embeddata/01-schema.sql）：改掉顶层 FROM。
# ---------------------------------------------------------------------------
BASE_EMBED="installer/cmd/llm-gw-installer/embeddata/01-schema.sql"
backup "$BASE_EMBED"
if run_mutation "M8" "$BASE_EMBED" mutate "$BASE_EMBED" \
  "$(b64 'FROM public.api_key_model_cost mcp')" \
  "$(b64 'FROM public.session_turns mcp')"; then
  expect_red "M8-applied-baseline-embeddata-rowset-changed" "$ROWSET"
fi
restore "$BASE_EMBED"

# ---------------------------------------------------------------------------
# M9 — 让解析器少认一个视图（提前 break）⇒ 完整性自检必须红。
# ---------------------------------------------------------------------------
backup "$GATE"
if run_mutation "M9" "$GATE" mutate "$GATE" \
  "$(b64 '		out[name] = body[:end]')" \
  "$(b64 '		if len(out) >= 2 {
			break
		}
		out[name] = body[:end]')"; then
  expect_red "M9-parser-under-parses-views" "$ROWSET"
fi
restore "$GATE"

# ---------------------------------------------------------------------------
# M10 — 迁移目录里**多出一个**定义这两个视图的文件（部署形态源漂移）
#       ⇒ 「形态源唯一性」判据必须红
# ⚠ 跑完**立刻删掉**造出来的文件 ⇒ 后续变异从干净工作树开始（否则「红」不可归因）。
# ---------------------------------------------------------------------------
M10_FILE="sql/migrations/startup/999_mutation_other_definer.sql"
if run_mutation_new "M10" "$M10_FILE" add_file "$M10_FILE" \
  "$(b64 'CREATE OR REPLACE VIEW public.customer_cost_view AS
 SELECT 1 AS mutation_marker;
')"; then
  expect_red "M10-another-migration-redefines-the-view" "$UNIQUE"
fi
remove_added "$M10_FILE"

# ---------------------------------------------------------------------------
# M11 — 安装器**内嵌目录**里多出一个定义体（新装通道 ⇒ 同样是形态源）
#       ⇒ 必须红（★ 只扫迁移目录的话这条会漏）
# ---------------------------------------------------------------------------
M11_FILE="installer/cmd/llm-gw-installer/embeddata/startup/998_mutation_other_definer.sql"
if run_mutation_new "M11" "$M11_FILE" add_file "$M11_FILE" \
  "$(b64 'CREATE OR REPLACE VIEW public.model_cost_per_task_view AS
 SELECT 1 AS mutation_marker;
')"; then
  expect_red "M11-installer-embeddata-redefines-the-view" "$UNIQUE"
fi
remove_added "$M11_FILE"

# ---------------------------------------------------------------------------
# M12 — ★★ **部署基线**（deploy/sql/schemas/baseline/01-schema.sql）：改掉顶层 FROM。
#      ★ 这一条打的是**续做轮实测抓到的真盲点**：上一版门只钉了 6 个形态源，
#        而这份**随离线包与一键安装分发**的部署基线里同样 `CREATE VIEW` 了这两个视图，
#        却不在形态源清单里 ⇒ 改掉它 ⇒ 门**照样绿**（实测复现）。
# ---------------------------------------------------------------------------
BASE_DEPLOY="deploy/sql/schemas/baseline/01-schema.sql"
backup "$BASE_DEPLOY"
if run_mutation "M12" "$BASE_DEPLOY" mutate "$BASE_DEPLOY" \
  "$(b64 'FROM (public.api_key_model_cost akmc')" \
  "$(b64 'FROM (public.session_turns akmc')"; then
  expect_red "M12-deploy-baseline-rowset-changed" "$ROWSET"
fi
restore "$BASE_DEPLOY"

# ---------------------------------------------------------------------------
# M13 — ★ 迁移 883 的 **down 体**（回滚形态）：改掉顶层 FROM。
#      上一版只把 down 体列进白名单（「不是它」），**没钉它的形状**
#      ⇒ 回滚之后的行集来源无人看管 ⇒ 这条打的就是那个缺口。
# ---------------------------------------------------------------------------
DOWN883="sql/migrations/startup/883_session_family_cost_view_metrics.down.sql"
backup "$DOWN883"
if run_mutation "M13" "$DOWN883" mutate "$DOWN883" \
  "$(b64 'FROM (public.api_key_model_cost akmc')" \
  "$(b64 'FROM (public.session_turns akmc')"; then
  expect_red "M13-rollback-shape-rowset-changed" "$ROWSET"
fi
restore "$DOWN883"

# ---------------------------------------------------------------------------
# M14 — ★★ **全新目录** + **无 schema 限定**的写法（`CREATE VIEW customer_cost_view`）。
#      两条性质同时打：
#        (a) 判据是**穷举全仓 .sql**（不是「扫几个已知目录」）⇒ 新目录也必须抓到；
#        (b) 正则认**无 schema 限定**的写法 ⇒ 仓内两份归档副本就是这种写法
#            （上一版只认 `public.` ⇒ 那两份文件对判据完全隐形）。
#      ⚠ 这条同时证明「上一版的扫描根清单」是个**活事实**：新开目录 ⇒ 静默漏掉。
# ---------------------------------------------------------------------------
M14_FILE="l11b_mutation_probe/unqualified_definer.sql"
if run_mutation_new "M14" "$M14_FILE" add_file "$M14_FILE" \
  "$(b64 'CREATE VIEW customer_cost_view AS
 SELECT 1 AS mutation_marker;
')"; then
  expect_red "M14-brand-new-dir-unqualified-definer" "$UNIQUE"
fi
remove_added "$M14_FILE"

# ---------------------------------------------------------------------------
# M15 — 允许清单里塞一条**陈旧条目**（指向一个根本不含定义体的文件）
#       ⇒ 「不许有陈旧条目」判据必须红（否则清单会越来越假、越来越自说自话）。
# ⚠ 这一条在**第一版脚本里是假通过**：变异助手因漏 re.M 压根没匹配上（只打印一行
#   错误就返回），而 M10/M11/M14 造出来的文件还留在工作树里 ⇒ 门一直红着
#   ⇒ 「红」不可归因 ⇒ ⇒ 现在（a）变异必须自检落地 （b）新增文件立刻清理。
# ---------------------------------------------------------------------------
backup "$GATE"
if run_mutation "M15" "$GATE" mutate_gate_line "$GATE" \
  '^\tfilepath\.Join\("sql", "schema", "01-schema\.sql"\):.*$' \
  $'\tfilepath.Join("sql", "schema", "02-seed.sql"): costViewRoleArchived,'; then
  expect_red "M15-stale-allowlist-entry" "$UNIQUE"
fi
restore "$GATE"

# ---------------------------------------------------------------------------
# M16 — 把一个**形态源**的角色从「形态源」改成「归档」
#       ⇒ 「角色 ↔ 形态源清单一一对应」判据必须红
#       （钉了形状却登记成归档 ⇒ 两份清单漂移 ⇒ 门在骗自己）。
# ---------------------------------------------------------------------------
backup "$GATE"
if run_mutation "M16" "$GATE" mutate_gate_role "$GATE" \
  '^\tfilepath\.Join\("deploy", "sql", "schemas", "baseline", "01-schema\.sql"\):.*$' \
  'costViewRoleShape' 'costViewRoleArchived'; then
  expect_red "M16-role-shape-drift" "$UNIQUE"
fi
restore "$GATE"

echo
echo "=== summary ==="
echo "mutations: $MUTATIONS_RUN, turned red: $MUTATIONS_RED"
if [ "${#FAILED[@]}" -ne 0 ]; then
  echo "RESULT: FAIL — ${FAILED[*]}" >&2
  exit 1
fi
echo "RESULT: PASS — baseline green, every mutation turned its leg red, restore verified"
exit 0
