#!/usr/bin/env bash
# scripts/guards/check-shell-vacuous-assertion.sh
#
# ★★★ 「判据恒真」shell 守卫（★ 2026-11-23 L33 二轮新增）★★★
#
# ── 这道门为什么存在（★ 真事故，★ 不是假想）────────────────────────────
# ★ 2026-11-23 L33 二轮：★ 本轮给「空库新装烟测门」扩第三阶段（★ 覆盖修法
#   F3：★ 安装器**只读**咨询部署通道权威台账 `gateway_db_revision_sequences`）
#   ⇒ ★★ 写完第三阶段后，★ 门输出**自相矛盾**：
#     「✔ 484 被权威台账按同一内容指纹跳过」 **同时**
#     「FAIL 484 被**重放**了」
#   ⇒ ★★ 而诊断打印显示：★ 日志里与 484 有关的行**只有 1 行**、★ 且是「跳过」行
#   ⇒ ★★ 矛盾不在日志里、★ **在判据写法里**★★。
#
# ── 根因（★ 一行 shell 语义）──────────────────────────────────────────
# ★ 第一版的写法是（★ 两个判据都是这个形态 ★）：
#     if (grep -q "…" "$LOG" || true); then
#         note "  ✔ 被跳过"        # ← 判绿
#     fi
#     if (grep -q "…" "$LOG" || true); then
#         bad  "  被重放了"         # ← 判红
#     fi
# ★★ `||` 的优先级高于 `;` ⇒ ★ `(grep -q … || true)` 整体退出码**恒为 0**
# ★★ ⇒ ★ **`if` 的条件恒为真** ⇒ ★★ 两条判据**同时恒成立** ⇒ ★★
# ★★ 「跳过」恒判绿（★ 假绿）、★ 「重放」恒判红（★ 假红）★★。
# ★★ 而 `|| true` 的本意只是「让 grep 无匹配（退出码 1）不触发 set -e」——
# ★★ **在命令替换里取计数时它是对的、★ 一旦放进 `if` 的条件位它就把判据掏空**★★。
#
# ── 为什么这道门值得存在（★ 缺陷类别，★ 不是个案）──────────────────────
# ★ 「判据恒真」是**门最坏的形态之一**：★
#   · 比「恒绿」更隐蔽（★ 恒绿至少看起来像通过）；
#   · 比「恒红」更危险（★ 恒红会被人当成真信号去查）；
#   · 而且它**自相矛盾时才会露头**（★ 本次就是靠「✔跳过」与「✘重放」同时
#     出现才定位到）★★ ⇒ ⇒ **门自己不会发现它** ⇒ ⇒ 必须由**另一道门**盯。
# ★★ 本门的作用：★ **让「恒真判据」在写出来的当天就红**，★ 而不是等它在一个
# ★★ 具体事故里以「自相矛盾的输出」的形式暴露（★ 这次暴露得晚了一整天）。
#
# ── 判据（★ 口径写死在这里，★ 免得下一个人按自己的理解改）──────────────
# ★ 违规形态：`if` / `elif` / `while` / `until` 的**条件部分**里出现
# ★   `|| true`（★ 或 `||true`、★ 或 `||` 与 `true` 之间夹任意空白）
# ★   **且** 条件部分里**没有** `$(`（★ 命令替换）。
# ★ 豁免（★ 这两类是对的、★ 不许误报）：
#   ① `if [[ -n "$(cmd || true)" ]]; then` ⇒ ★ `|| true` 在**命令替换内部**、
#      ★ 条件判的是**取值**而不是**退出码** ⇒ ★ 语义正确；
#   ② `if cmd; then body || true; fi` ⇒ ★ `|| true` 在**命令体**里 ⇒ ★ 正确；
# ★   ⇒ ★ 所以本门**只取条件部分**（★ 截到第一个 `; then` / `; do`）★★。
#
# ── 已知边界（★ 如实登记，★ 不假装覆盖）────────────────────────────────
# ★ ① 条件跨多行且**不是**反斜杠续行（如 `if cmd` 换行 `then`）⇒ 本门不判；
# ★ ② `if` 出现在 `$( … )` 内部且用 `if …;then` 无空格写法 ⇒ 可能漏；
# ★ ③ 本门只扫 **shell 脚本**（★ `.sh`/`.bash`）⇒ Go 门与 Python 门不在范围内；
# ★ ④ 条件里的 `$(` 出现在 `|| true` **之后**时 ⇒ 本门按「豁免①」处理（★ 保守
# ★    ⇒ **可能漏报、★ 不会误报**）★★。
#
# ── 三态判据（★ 与仓内既有门同款）──────────────────────────────────────
#   · 枚举面读空（★ 仓库根算错）⇒ **ABORT exit 2**（★ 不是通过）；
#   · 自探针没咬 ⇒ **ABORT exit 2**（★ 结论不可信）；
#   · 命中违规 ⇒ **FAIL exit 1**（★ 逐条打印 文件:行:内容）；
#   · 无命中 ⇒ **PASS exit 0**。
set -euo pipefail
trap 'printf "FAIL: 脚本在第 %s 行异常退出（★ 这是门自身的缺陷、★ 不是判据红）\n" "$LINENO" >&2; exit 1' ERR

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
while [ "$REPO_ROOT" != "/" ] && [ ! -e "$REPO_ROOT/.git" ]; do
  REPO_ROOT="$(dirname "$REPO_ROOT")"
done
[ -e "$REPO_ROOT/.git" ] || { echo "ABORT: 找不到 .git，仓库根推断失败" >&2; exit 2; }
cd "$REPO_ROOT"

# ── 枚举面自检（★ 读空 ⇒ 门会「一个都不检查」⇒ 必须报出来）────────────
sh_count=$(git ls-files -z '*.sh' '*.bash' | tr '\0' '\n' | wc -l | tr -d ' ')
if [ "$sh_count" -lt 10 ]; then
  echo "ABORT: 在 ${REPO_ROOT} 只枚举到 ${sh_count} 个 shell 脚本 —— 仓库根很可能算错了" >&2
  exit 2
fi

# ★ 跳过集**逐项对齐** scripts/ursmcheck/cjk_var_expansion_test.go 的
# ★ cjkVarSkipDirs（★ 事实源单一化：★ 下一层要改就**两门同时改**）★★。
SCAN_SKIP_DIRS='^\.git/|^node_modules/|^vendor/|^dist/|^build/|^\.venv/|__pycache__/'

scan_list() {
  {
    git ls-files -z '*.sh' '*.bash'
    git ls-files -z --others --exclude-standard '*.sh' '*.bash'
  } | tr '\0' '\n' | grep -vE "$SCAN_SKIP_DIRS" | LC_ALL=C sort -u
}

# ── 判据实现（★ awk：★ 逻辑行拼接 ＋ 条件部分截取）────────────────────
# ★ 用法：detect_vacuous <文件> ⇒ 命中时打印「行号<TAB>内容」到 stdout。
detect_vacuous() {
  awk '
    function process(logical, start) {
      if (logical !~ /^[[:space:]]*(if|elif|while|until)[[:space:]]/) return
      # 条件部分 = 截到第一个 "; then" 或 "; do"（★ 排除命令体里的 || true ★）
      cut = 0
      if (match(logical, /;[[:space:]]*then/)) cut = RSTART
      else if (match(logical, /;[[:space:]]*do/)) cut = RSTART
      if (cut == 0) return
      cond = substr(logical, 1, cut - 1)
      if (cond !~ /\|\|[[:space:]]*true/) return
      if (cond ~ /\$\(/) return          # ★ 命令替换内部 ⇒ 判的是取值、★ 语义正确
      printf "%d\t%s\n", start, logical
    }
    {
      if (pending) { logical = logical " " $0; pending = 0 }
      else { logical = $0; start = NR }
      if ($0 ~ /\\$/) { pending = 1; next }
      process(logical, start)
    }
    END { if (pending) process(logical, start) }
  ' "$1"
}

# ── 自探针（★ 证明本门**会咬**：★ 否则「0 命中」分不清是真干净还是门失效）──
# ★★ 自探针语料**刻意不在本文件里写成字面量**（★ 否则本门会把自己扫成违规 ★★
# ★★ ⇒ ★ 所以用变量拼装；★ 拼装出来的那行**以赋值语句开头** ⇒ ★ 本门的锚点
# ★★ （行首是 if/elif/while/until）不会命中它 ★★）。
probe_dir="$(mktemp -d)"
list_file="$(mktemp)"
cleanup_probe() { rm -rf "$probe_dir" "$list_file"; }
trap cleanup_probe EXIT
probe_vacuous_line='if (grep -q "x" "$LOG" || true); then'
probe_control_line='if [[ -n "$(cmd || true)" ]]; then'
{
  printf '%s\n' '#!/usr/bin/env bash'
  printf '%s\n' "$probe_vacuous_line"
  printf '%s\n' '  echo bad'
  printf '%s\n' 'fi'
} > "$probe_dir/vacuous.sh"
{
  printf '%s\n' '#!/usr/bin/env bash'
  printf '%s\n' "$probe_control_line"
  printf '%s\n' '  echo ok'
  printf '%s\n' 'fi'
} > "$probe_dir/control.sh"
probe_hits=$(detect_vacuous "$probe_dir/vacuous.sh" | wc -l | tr -d ' ')
if [ "$probe_hits" -lt 1 ]; then
  echo "ABORT: 自探针没咬（★ 判据已失效、★ 结论不可信）" >&2
  exit 2
fi
ctl_hits=$(detect_vacuous "$probe_dir/control.sh" | wc -l | tr -d ' ')
if [ "$ctl_hits" -ne 0 ]; then
  echo "ABORT: 反向对照被误报（★ 命令替换里的 || true 是对的、★ 不该判红）" >&2
  exit 2
fi

# ── 主扫描 ───────────────────────────────────────────────────────────
hits=0
scanned=0
scan_list > "$list_file"
while IFS= read -r f; do
  [ -n "$f" ] || continue
  [ -f "$f" ] || continue
  scanned=$((scanned + 1))
  out="$(detect_vacuous "$f")"
  if [ -n "$out" ]; then
    while IFS=$'\t' read -r ln content; do
      [ -n "$ln" ] || continue
      hits=$((hits + 1))
      printf 'FAIL: %s:%s: ★ 判据恒真（★ `|| true` 落在条件位 ⇒ ★ if 条件恒为真 ⇒ ★ 这条断言永远成立）\n' "$f" "$ln" >&2
      printf '      %s\n' "$content" >&2
    done <<< "$out"
  fi
done < "$list_file"

printf 'shell-vacuous-assertion: 扫 %s 个脚本，命中 %s 条\n' "$scanned" "$hits"
if [ "$hits" -ne 0 ]; then
  printf 'shell-vacuous-assertion: FAIL\n' >&2
  exit 1
fi
printf 'shell-vacuous-assertion: PASS\n'
exit 0
