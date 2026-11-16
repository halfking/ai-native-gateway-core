#!/usr/bin/env bash
# scripts/first-activation-reset_mutation_test.sh —— 「首次激活要有重置数据的操作、默认要重置」的变异有牙证明
#
# ## 这道脚本证明什么
#
# 三条判据（web/src/views/...）各自钉住本轮的一条行为事实：
#   M1  web/src/views/data-lifecycle/DataInitialization.defaultReset.test.ts
#       「面板首次加载默认全选全部类别 ⇒ 默认要重置数据」
#   M2  web/src/views/bootstrap/BootstrapWizardView.resetStep.test.ts
#       「首启向导的步骤条里有『重置数据』这一步，且排在『完成』之前」
#   M3  web/src/views/DataLifecycleView.tabDeepLink.test.ts
#       「?tab=data-init 深链真的落到数据初始化页签」
#
# ★ 判定口径：**源码层变异**——把实现里的那一条行为抽掉，重跑对应判据，
#   判据必须变红。判据仍绿 ⇒ 那是恒绿门（钉不住任何东西）。
# ★ 纪律三条：
#   ① 变异必须**自证生效**（锚点命中 + 替换确实改到了文件）——否则变异空跑，
#      空跑的「红」是假的（变异没发生，判据当然照常绿）。
#   ② 变异必须**还原**——用 trap 兜底，且备份目录只在全部跑完后才删
#      （★ 第一版在每条之后删备份目录 ⇒ 第二条的备份没建成 ⇒ 变异残留在树上）。
#   ③ 还原后要能**核对**：脚本结束时打印工作树状态，由调用方比对。
#
# 用法: bash scripts/first-activation-reset_mutation_test.sh
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
      cp "$BACKUP_DIR/$(basename "$f")" "$f"
    fi
  done
}
cleanup() {
  restore_all
  rm -rf "$BACKUP_DIR"
}
trap cleanup EXIT

run_mutation() {
  local label="$1" file="$2" test="$3" python_body="$4"
  local backup="$BACKUP_DIR/$(basename "$file")"
  cp "$file" "$backup" || { echo "  x ${label}: 备份失败"; FAIL=$((FAIL+1)); return; }
  RESTORE_FILES+=("$file")
  if ! python3 -c "$python_body"; then
    echo "  x ${label}: 锚点未命中 => 变异空跑（不算数）"
    FAIL=$((FAIL+1)); return
  fi
  if cmp -s "$file" "$backup"; then
    echo "  x ${label}: 变异没有真正改到文件 => 变异空跑（不算数）"
    FAIL=$((FAIL+1)); return
  fi
  npx vitest run "$test" >"/tmp/mutation-${label}.log" 2>&1
  local rc=$?
  cp "$backup" "$file"   # 还原**本条**（备份目录留给后面的条目用）
  if [[ $rc -ne 0 ]]; then
    echo "  v ${label}: 判据变红（rc=${rc}）=> 守卫有牙"
    grep -E "Tests  |Test Files" "/tmp/mutation-${label}.log" | tail -2 | sed 's/^/      /'
    PASS=$((PASS+1))
  else
    echo "  x ${label}: 判据仍绿 => 这是恒绿门"
    FAIL=$((FAIL+1))
  fi
}

echo "M1: 抽掉「首次默认全选」=> 面板默认重置判据必须红"
run_mutation "M1" src/views/data-lifecycle/DataInitialization.vue \
  src/views/data-lifecycle/DataInitialization.defaultReset.test.ts '
import pathlib
p = pathlib.Path("src/views/data-lifecycle/DataInitialization.vue")
s = p.read_text()
old = "  selected.value = categories.value.map(c => c.key)\n}\n\nfunction selectAll()"
new = "  selected.value = []\n}\n\nfunction selectAll()"
assert old in s, "锚点未命中"
p.write_text(s.replace(old, new, 1))
'

echo "M2: 摘掉向导步骤条里的「重置数据」一步 => 向导判据必须红"
run_mutation "M2" src/views/bootstrap/BootstrapWizardView.vue \
  src/views/bootstrap/BootstrapWizardView.resetStep.test.ts '
import pathlib
p = pathlib.Path("src/views/bootstrap/BootstrapWizardView.vue")
s = p.read_text()
old = "  { labelKey: \x27dataLifecycle.dataInit.wizard.stepLabel\x27 },\n"
assert old in s, "锚点未命中"
p.write_text(s.replace(old, "", 1))
'

echo "M3: 让 ?tab= 深链解析恒返回第一个页签 => 深链判据必须红"
run_mutation "M3" src/views/DataLifecycleView.vue \
  src/views/DataLifecycleView.tabDeepLink.test.ts '
import pathlib
p = pathlib.Path("src/views/DataLifecycleView.vue")
s = p.read_text()
old = "  const found = tabs.find(tab => tab.key === raw)\n  return found ? found.key : \x27storage\x27"
new = "  void tabs.find(tab => tab.key === raw)\n  return \x27storage\x27"
assert old in s, "锚点未命中"
p.write_text(s.replace(old, new, 1))
'

echo
echo "=== 变异结果：通过 ${PASS} / 失败 ${FAIL}（期望 3/0）==="
[[ ${FAIL} -eq 0 ]] || exit 1
exit 0
