#!/usr/bin/env bash
# scripts/data-init-category-parity_mutation_test.sh —— 「后端类别注册表 ↔ 前端默认全选」对账判据的变异有牙证明
#
# ## 这道脚本证明什么
#
# 判据：web/src/views/data-lifecycle/DataInitialization.categoryParity.test.ts
#
#   M1  改掉后端某个类别的 label_key（指向不存在的键）
#       ⇒ 判据必须红（8 语种里都查不到那个标签 ⇒ 前端那一格只能渲染原始 key）。
#   M2  抽掉前端的「首次默认全选」
#       ⇒ 判据必须红（默认重置姿态没了 ⇒ 打开面板要手动勾一遍）。
#   M3  后端**新增一个类别**（注册表 + 执行顺序都加上），但语言包没有它的标签
#       ⇒ 判据必须红（这正是这道门要挡的回归：后端做对了、前端没接住）。
#
# ★ 判定口径：**源码层变异** —— 把实现里的那一条行为抽掉，重跑判据，判据必须变红。
#   判据仍绿 ⇒ 那是恒绿门（钉不住任何东西）。
#
# ★ 纪律五条（与 scripts/data-init-isolation_mutation_test.sh 同款）：
#   ① 变异必须**自证生效**（锚点命中 + 文件确实被改到）—— 否则变异空跑，
#      空跑的「红」是假的。
#   ② 变异必须**还原**（trap 兜底 + 每条立刻还原），备份目录只在全部跑完后才删
#      （★ 第一版在每条之后删备份目录 ⇒ 后一条的备份没建成 ⇒ 变异残留在树上）。
#   ③ 判据**没跑**（SKIP）≠ 判据绿 ⇒ 显式判 SKIP 为「不算数」。
#   ④ 还原后要能**核对**：脚本结束时打印工作树状态。
#   ⑤ 判据必须打**生产代码**（Go 注册表 / Vue 组件），不是测试文件里的反向调用。
#
# 用法: bash scripts/data-init-category-parity_mutation_test.sh
set -uo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$REPO_ROOT/web"
cd "$WEB_DIR" || exit 2

PASS=0; FAIL=0
BACKUP_DIR="$(mktemp -d)"
RESTORE_FILES=()

restore_all() {
  local f
  for f in ${RESTORE_FILES[@]+"${RESTORE_FILES[@]}"}; do
    if [[ -n "$f" && -f "$BACKUP_DIR/$(basename "$f")" ]]; then
      cp "$BACKUP_DIR/$(basename "$f")" "$f" || echo "  x 还原失败: $f"
    fi
  done
}
cleanup() {
  restore_all
  rm -rf "$BACKUP_DIR"
}
trap cleanup EXIT

# ★ 目标路径一律用**仓内相对路径**（脚本虽然 cd 到 web/，但备份/还原/核对
#   都按仓根解析 ⇒ 两种口径混用会让备份落到不存在的路径上）。
GO_REL="admin/data_lifecycle_data_init.go"
VUE_REL="web/src/views/data-lifecycle/DataInitialization.vue"
TEST_REL="src/views/data-lifecycle/DataInitialization.categoryParity.test.ts"

# run_mutation <标签> <要改的仓内相对路径> <python 变异体>
run_mutation() {
  local label="$1" target_rel="$2" python_body="$3"
  local target="$REPO_ROOT/$target_rel"
  local backup="$BACKUP_DIR/$(basename "$target_rel")"

  cp "$target" "$backup" || { echo "  x ${label}: 备份失败"; FAIL=$((FAIL+1)); return; }
  RESTORE_FILES+=("$target")

  if ! python3 -c "$python_body"; then
    echo "  x ${label}: 锚点未命中 => 变异空跑（不算数）"
    cp "$backup" "$target"
    FAIL=$((FAIL+1)); return
  fi
  if cmp -s "$target" "$backup"; then
    echo "  x ${label}: 变异没有真正改到文件 => 变异空跑（不算数）"
    cp "$backup" "$target"
    FAIL=$((FAIL+1)); return
  fi

  local log="/tmp/di-parity-mutation-${label}.log"
  npx vitest run "$TEST_REL" >"$log" 2>&1
  local rc=$?

  # 还原**本条**（备份目录留给后面的条目用）。
  cp "$backup" "$target"

  if grep -q -- "skipped\|No test files found" "$log"; then
    if grep -qE -- "Tests .*skipped" "$log"; then
      echo "  x ${label}: 判据被 SKIP 了（没跑）=> 不算数（跳过不构成证据）"
      FAIL=$((FAIL+1)); return
    fi
  fi
  if [[ $rc -ne 0 ]]; then
    echo "  v ${label}: 判据变红（rc=${rc}）=> 守卫有牙"
    grep -E -- "^\s+×|Tests  " "$log" | head -4 | sed 's/^/      /'
    PASS=$((PASS+1))
  else
    echo "  x ${label}: 判据仍绿 => 这是恒绿门"
    FAIL=$((FAIL+1))
  fi
}

echo "M1: 后端 label_key 指向不存在的键 ⇒ 8 语种标签覆盖判据必须红"
run_mutation "M1" "$GO_REL" '
import pathlib
p = pathlib.Path("'"$REPO_ROOT"'/'"$GO_REL"'")
s = p.read_text()
old = "LabelKey: \"dataLifecycle.dataInit.categories.requestRecords\","
new = "LabelKey: \"dataLifecycle.dataInit.categories.requestRecordsMUTATED\","
assert old in s, "M1 anchor (label_key) not found"
s = s.replace(old, new, 1)
p.write_text(s)
'

echo "M2: 抽掉前端「首次默认全选」⇒ 默认全选判据必须红"
run_mutation "M2" "$VUE_REL" '
import pathlib
p = pathlib.Path("'"$REPO_ROOT"'/'"$VUE_REL"'")
s = p.read_text()
old = "  selected.value = categories.value.map(c => c.key)\n}\n\nfunction selectAll()"
new = "  selected.value = []\n}\n\nfunction selectAll()"
assert old in s, "M2 anchor (applyDefaultSelection) not found"
s = s.replace(old, new, 1)
p.write_text(s)
'

echo "M3: 后端新增一个类别（语言包没有它的标签）⇒ 前端没接住判据必须红"
run_mutation "M3" "$GO_REL" '
import pathlib
p = pathlib.Path("'"$REPO_ROOT"'/'"$GO_REL"'")
s = p.read_text()

old_order = """var dataInitCategoryOrder = []string{
	\"request_records\","""
new_order = """var dataInitCategoryOrder = []string{
	\"widgets_mutation\",
	\"request_records\","""
assert old_order in s, "M3 anchor 1 (order list) not found"
s = s.replace(old_order, new_order, 1)

old_cat = """var dataInitCategories = []dataInitCategory{
	{
		Key:      \"request_records\","""
new_cat = """var dataInitCategories = []dataInitCategory{
	{
		Key:      \"widgets_mutation\",
		LabelKey: \"dataLifecycle.dataInit.categories.widgetsMutation\",
		Level:    dataInitLevelOperational,
		Tables: []dataInitTable{
			{Name: \"widgets_mutation\", TimeCol: \"created_at\"},
		},
	},
	{
		Key:      \"request_records\","""
assert old_cat in s, "M3 anchor 2 (categories list) not found"
s = s.replace(old_cat, new_cat, 1)
p.write_text(s)
'

# ★ 还原核对：源码必须回到变异前的样子（否则变异残留在树上）。
#   ★ 核对式必须挑**只在变异体里出现**的字符串 ——
#     「selected.value = []」在组件里本来就有（那是「全不选」的合法实现），
#     用它做核对会把「已还原」误报成「有残留」（本轮真踩到）。
echo "--- 还原核对 ---"
if grep -q "requestRecordsMUTATED" "$REPO_ROOT/$GO_REL"; then
  echo "  x M1 的变异残留在源码里"; FAIL=$((FAIL+1))
fi
if grep -q "widgets_mutation" "$REPO_ROOT/$GO_REL"; then
  echo "  x M3 的变异残留在源码里"; FAIL=$((FAIL+1))
fi
# ★ M2 的核对式 = 「原始那行必须还在」（变异把它改成了空选中）。
if ! grep -q "selected.value = categories.value.map(c => c.key)" "$REPO_ROOT/$VUE_REL"; then
  echo "  x M2 的变异残留在源码里（默认全选那行没回来）"; FAIL=$((FAIL+1))
fi

echo "--- 工作树状态（由调用方比对） ---"
git -C "$REPO_ROOT" status --porcelain -- "$GO_REL" "$VUE_REL" | sed 's/^/  /'
echo "  (空 = 两个目标文件已还原)"

echo
echo "变异有牙结果: ${PASS} 红 / ${FAIL} 失败"
[[ $FAIL -eq 0 ]] || exit 1
exit 0
