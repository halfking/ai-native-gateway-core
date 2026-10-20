#!/usr/bin/env bash
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
# 本脚本给这道门装上**六条可复核的牙**：
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
#
# ★ **本轮的门第一版就是被 M1/M2 抓住的**：它只断言「该视图在**某个**形态里
#   读冻结表」⇒ 改掉 883 那份、基线那份还在 ⇒ **照样绿** ⇒ 门会放过形态被改。
#   ⇒ **变异的价值就是把「文档里写的意图」与「代码实际断言的东西」对齐**。
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

expect_red() {
  local label="$1" pattern="$2"
  MUTATIONS_RUN=$((MUTATIONS_RUN + 1))
  echo "── MUTATION $label: expect RED"
  local output
  output="$(go test ./admin/ -run "$pattern" -count=1 2>&1)"
  if echo "$output" | grep -qE '\[build failed\]|^# |cannot use|undefined:'; then
    echo "   ✘ BUILD FAILED — the mutation does not compile; not a valid mutation"
    echo "$output" | head -10
    FAILED+=("$label(build)")
    return
  fi
  local failed unexpected
  failed="$(echo "$output" | grep -oE '^--- FAIL: [A-Za-z0-9_]+' | sed 's/^--- FAIL: //' | sort -u)"
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
  MUTATIONS_RED=$((MUTATIONS_RED + 1))
}

expect_green() {
  local label="$1" pattern="$2"
  echo "── BASELINE $label: expect GREEN"
  local output
  output="$(go test ./admin/ -run "$pattern" -count=1 2>&1)"
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
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

GATE="admin/auto_route_cost_views_rowset_source_test.go"
MIG883="sql/migrations/startup/883_session_family_cost_view_metrics.sql"
BASE_MODEL="sql/objects/views/model_cost_per_task_view.sql"

ROWSET='TestAutoRouteCostViewsRowsetSourceIsRegistered'
PARSER='TestCostViewRowsetParserSelfCheck'
EVIDENCE='TestCostViewRowsetRegistryIsBackedByRepoEvidence'

echo "=== L11-b 成本视图行集来源登记门：变异验证 ==="
echo "repo: $REPO_ROOT"
echo

expect_green "baseline" "$ROWSET|$PARSER|$EVIDENCE"

# ---------------------------------------------------------------------------
# M1 — 部署形态：customer_cost_view 的顶层 FROM 换成会话族。
# ---------------------------------------------------------------------------
backup "$MIG883"
mutate "$MIG883" \
  "$(b64 'FROM (public.api_key_model_cost akmc')" \
  "$(b64 'FROM (public.session_turns akmc')"
expect_red "M1-deployed-shape-rowset-changed" "$ROWSET"
restore "$MIG883"

# ---------------------------------------------------------------------------
# M2 — 基线形态：model_cost_per_task_view 的顶层 FROM 换成会话族。
#      （只改一种形态 ⇒ 也必须红 ⇒ 证明两种形态都在门眼里）
# ---------------------------------------------------------------------------
backup "$BASE_MODEL"
mutate "$BASE_MODEL" \
  "$(b64 'FROM public.api_key_model_cost mcp')" \
  "$(b64 'FROM public.session_turns mcp')"
expect_red "M2-baseline-shape-rowset-changed" "$ROWSET"
restore "$BASE_MODEL"

# ---------------------------------------------------------------------------
# M3 — 登记册里删掉一条（这个已知缺陷没被登记）。
# ---------------------------------------------------------------------------
backup "$GATE"
mutate_drop_registry_entry "$GATE" "model_cost_per_task_view"
expect_red "M3-registry-entry-dropped" "$ROWSET"
restore "$GATE"

# ---------------------------------------------------------------------------
# M4 — 迁移 883 里加一个**第三个**读冻结表的视图（缺陷在扩散，却没登记）。
# ---------------------------------------------------------------------------
backup "$MIG883"
THIRD_VIEW=$'\n\nCREATE OR REPLACE VIEW public.mutation_third_view AS\n SELECT count(*) AS n FROM public.api_key_model_cost;\n'
mutate "$MIG883" \
  "$(b64 "--   SELECT pg_get_viewdef('public.model_cost_per_task_view'::regclass, true) LIKE '%FROM session_turns st_par%';")" \
  "$(b64 "--   SELECT pg_get_viewdef('public.model_cost_per_task_view'::regclass, true) LIKE '%FROM session_turns st_par%';${THIRD_VIEW}")"
expect_red "M4-third-frozen-sourced-view-not-registered" "$ROWSET"
restore "$MIG883"

# ---------------------------------------------------------------------------
# M5 — 剥注释逻辑打坏（注释里的 FROM 不再被忽略）。
# ---------------------------------------------------------------------------
backup "$GATE"
mutate "$GATE" \
  "$(b64 "case c == '-' && i+1 < len(sql) && sql[i+1] == '-':")" \
  "$(b64 "case c == '-' && i+1 < len(sql) && sql[i+1] == '=':")"
expect_red "M5-comment-stripping-broken" "$PARSER"
restore "$GATE"

# ---------------------------------------------------------------------------
# M6 — 字符串字面量处理打坏（字面量里的括号打乱深度）。
# ---------------------------------------------------------------------------
backup "$GATE"
mutate "$GATE" \
  "$(b64 "case c == '\\'':")" \
  "$(b64 "case c == '@':")"
expect_red "M6-string-literal-handling-broken" "$PARSER"
restore "$GATE"

# ---------------------------------------------------------------------------
# M7 — **被 apply 的基线**（sql/schema/01-schema.sql）：改掉顶层 FROM。
#      ★ 这一条专门打第一版门的盲区：第一版只钉了 `sql/objects/views/*.sql`，
#        而那目录**根本没有任何 apply 通道** ⇒ 改真正被应用的基线时它是绿的。
# ---------------------------------------------------------------------------
BASE_SCHEMA="sql/schema/01-schema.sql"
backup "$BASE_SCHEMA"
mutate "$BASE_SCHEMA" \
  "$(b64 'FROM (public.api_key_model_cost akmc')" \
  "$(b64 'FROM (public.session_turns akmc')"
expect_red "M7-applied-baseline-schema-rowset-changed" "$ROWSET"
restore "$BASE_SCHEMA"

# ---------------------------------------------------------------------------
# M8 — 安装器内嵌基线（embeddata/01-schema.sql）：改掉顶层 FROM。
# ---------------------------------------------------------------------------
BASE_EMBED="installer/cmd/llm-gw-installer/embeddata/01-schema.sql"
backup "$BASE_EMBED"
mutate "$BASE_EMBED" \
  "$(b64 'FROM public.api_key_model_cost mcp')" \
  "$(b64 'FROM public.session_turns mcp')"
expect_red "M8-applied-baseline-embeddata-rowset-changed" "$ROWSET"
restore "$BASE_EMBED"

# ---------------------------------------------------------------------------
# M9 — 让解析器少认一个视图（提前 break）⇒ 完整性自检必须红。
# ---------------------------------------------------------------------------
backup "$GATE"
mutate "$GATE" \
  "$(b64 '		out[name] = body[:end]')" \
  "$(b64 '		if len(out) >= 2 {
			break
		}
		out[name] = body[:end]')"
expect_red "M9-parser-under-parses-views" "$ROWSET"
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
