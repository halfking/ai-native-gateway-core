#!/usr/bin/env bash
# =====================================================================
# scripts/guards/check-shell-backtick-substitution.sh
#   —— 「反引号命令替换」shell 门（★ 2026-11-24 新增 ★）
#
# ★★ 这道门为什么存在（一句话）★★：
#   ★ ★★ **双引号串里的反引号会被 shell 真执行** ★★ ⇒ ★★ 注释里、报错文案里、
#   ★★ 文档片段里随手写一个反引号包裹的标识符 ⇒ ★★ **shell 会去执行它** ⇒ ★★
#   ★★ ⇒ ★★ 轻则 `command not found` 打到 stderr、★ 重则**真的执行了不该执行的东西**
#   ★★ （★ 仓里实测到过：★ 一条 heredoc 里的 `curl http://…` 会在生成部署记录时被真的跑 ★★）★★
#   ★★ ⇒ ★★ 而这类缺陷**在 macOS 与 Linux 上都会出现**（★ 与 mktemp 那道门不同 ★★）★★
#
# ★ ★★ 本仓已经在这上面栽过两次（★ 两次都是「自己的文案里踩的」 ★★）：★★
#   ★ ① **2026-11-24 L34 三轮**：★ 夹具里的 heredoc 用了**未加引号**的 `<<EOF` ⇒ ★
#   ★   **注释里的示例代码被 shell 真执行**（★ 含命令替换的示例 ⇒ 报 command not found ★★）★★
#   ★ ② **2026-11-24 L34 四轮**：★ 覆盖门的报错文案里用反引号包了 `sha256_file` ⇒ ★
#   ★   **在生产 211 上真跑时被 shell 执行** ⇒ ★ 实测报
#   ★   `scripts/checks/deploy-ledger-coverage-gate.sh: line 322: sha256_file: command not found` ★★
#   ★   ⇒ ★★ **⇒ 同一类缺陷、同一个作者、连着两轮 ⇒ ★★ 必须落地成门** ★★（★ 仓里的老
#   ★   教训原话：★ 「教训不落地成门 ⇒ 同类问题会再来一次」 ★★）★★
#
# ★★ 判据（三条、★ 全部可判定）★★：
#   ① **扫描面**：★ 全仓 `.sh`（★ **含未跟踪文件** ★★）★★
#   ② **命中条件**：★ 非注释行里出现**未被转义的反引号**、★ 且**不在单引号内** ★★
#      （★ 单引号内 ⇒ 字面量；★ 带引号的 heredoc 体 ⇒ 字面量 ⇒ **都不算** ★★）
#   ③ **自证**：★ 自探针（★ 注入已知违规样本 ⇒ 必须被抓到 ★★）＋ ★ 反向对照
#      （★ 合法样本 ⇒ 必须不被抓 ★★）★★
#
# ★★ 为什么规则是「禁用反引号、★ 一律用命令替换的新写法」★★：
#   ★ 反引号是**老式**命令替换：★ 不能嵌套、★ 转义规则与新写法不同、★
#   ★ 且在**双引号串里同样会执行** ⇒ ★★ 而新写法是仓内既有惯例 ★★
#   ★★ ⇒ ★★ **⇒ 规则本身就是「可读的一致性要求」、★ 而不只是防那个坑** ★★
#
# ★ ★★ 扫描器必须做对的四件事（★ 每个都对应一类真实误报、★ 都是本轮实测撞出来的 ★★）：★★
#   ★ ① **引号状态跨行保持**（★ bash 的字符串可以跨行 ★★）★★
#   ★ ② **行尾注释里的反引号是字面量**（★ bash 不执行注释 ★★）★★
#   ★ ③ **命令替换里引号上下文重新开始**（★ bash 语义 ★★）★★
#   ★ ④ **命令替换的右括号要括号配平**（★ 否则替换体里普通的 `if ( … )` 会把栈提前
#   ★   弹掉 ⇒ ★★ 后面整段都扫错 ★★）★★ ★★ ⇒ ★★ **算术展开 `$(( … ))` 不是命令替换** ★★
#
# ★★ 已知边界（如实记、★ 不装作覆盖）★★：
#   ★ ① heredoc 起点用**形态匹配**（★ `<<` / `<<-` / `<<'X'` / `<<"X"` ★★）⇒ ★
#   ★   **heredoc 起点写在单引号串里的那种写法识别不出** ⇒ ★ 那类文件的后续行会被
#   ★   当成代码行扫（★ **多报不会漏报** ⇒ ★ 方向是安全的 ★★）★★
#   ★ ② 扫描是**纯文本**的 ⇒ ★ 不执行任何被测脚本 ⇒ ★ 跨平台、★ 零副作用 ★★
#   ★ ③ **纯 awk 实现**（★ 不依赖 python3 ★★）⇒ ★ codeup 载体（★ alpine、★ 实测
#   ★   **没有 python3** ★★）也能跑 ★★
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/shell-backtick-gate.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

note() { printf '%s\n' "$*"; }

# ── 扫描器（★ awk、★ 纯文本、★ 不执行任何被测脚本 ★★）────────────────
# ★★ 写成独立的 .awk 文件（★ 放在带引号的 heredoc 里 ⇒ 字面量、★ 不被 shell 展开 ★★）★★
cat >"$WORK_DIR/scanner.awk" <<'AWKSCANNER'
# ── 反引号扫描器（★ POSIX awk、★ 不依赖任何扩展 ★★）──────────────────
function push_state() {
  depth++
  stk_s[depth] = in_s
  stk_d[depth] = in_d
  stk_pd[depth] = pd
}
function pop_state() {
  if (depth <= 0) return
  in_s = stk_s[depth]
  in_d = stk_d[depth]
  pd = stk_pd[depth]
  depth--
}
function scan_code(line,   i, c, n) {
  n = length(line)
  for (i = 1; i <= n; i++) {
    c = substr(line, i, 1)
    if (c == "\\") { i++; continue }
    if (c == "$" && substr(line, i + 1, 1) == "(" && substr(line, i + 2, 1) != "(" && !in_s) {
      # ★ 命令替换里、★ **引号上下文重新开始**（★ bash 语义 ★★）
      push_state()
      in_s = 0; in_d = 0; pd = 1
      i += 2
      continue
    }
    if (c == "(" && !in_s && !in_d) { pd++; continue }
    if (c == ")" && !in_s && !in_d) {
      # ★★ 只有【括号配平】的那个右括号才结束命令替换 ★★
      if (pd > 1) { pd--; continue }
      if (depth > 0) { pop_state(); continue }
      pd = 0
      continue
    }
    if (c == "'" && !in_d) { in_s = !in_s; continue }
    if (c == "\"" && !in_s) { in_d = !in_d; continue }
    if (c == "#" && !in_s && !in_d) {
      # ★ 行尾注释（★ 只有在引号外、★ 且前一个字符是空白或行首时才是注释 ★★）
      if (i == 1 || substr(line, i - 1, 1) == " " || substr(line, i - 1, 1) == "\t") { return 0 }
      continue
    }
    if (c == "`" && !in_s) { return 1 }
  }
  return 0
}
function has_any_backtick(s,   i, n) {
  n = length(s)
  for (i = 1; i <= n; i++) {
    if (substr(s, i, 1) == "\\") { i++; continue }
    if (substr(s, i, 1) == "`") { return 1 }
  }
  return 0
}
function trimmed(s) {
  gsub(/^[ \t]+/, "", s)
  gsub(/[ \t]+$/, "", s)
  return s
}
{ lines[NR] = $0; total = NR }
END {
  in_here = 0; here_term = ""; here_quoted = 0; hits = 0
  in_s = 0; in_d = 0; depth = 0; pd = 0
  for (i = 1; i <= total; i++) {
    line = lines[i]
    t = trimmed(line)
    if (in_here) {
      if (t == here_term) {
        in_here = 0; here_term = ""; here_quoted = 0
        in_s = saved_s; in_d = saved_d; depth = saved_depth; pd = saved_pd
        continue
      }
      if (!here_quoted && has_any_backtick(line)) { printf "%s:%d\n", FILENAME, i; hits++ }
      continue
    }
    if (t ~ /^#/ || t == "") { continue }
    if (scan_code(line)) { printf "%s:%d\n", FILENAME, i; hits++ }
    if (match(line, /<<-?['"]?[A-Za-z_][A-Za-z0-9_]*['"]?/)) {
      frag = substr(line, RSTART, RLENGTH)
      q = 0
      if (index(frag, "'") > 0) q = 1
      if (index(frag, "\"") > 0) q = 1
      gsub(/<<-?['"]?/, "", frag)
      gsub(/['"]?$/, "", frag)
      term = frag
      found = 0
      for (j = i + 1; j <= total; j++) {
        if (trimmed(lines[j]) == term) { found = 1; break }
      }
      if (found) {
        in_here = 1; here_term = term; here_quoted = q
        saved_s = in_s; saved_d = in_d; saved_depth = depth; saved_pd = pd
        in_s = 0; in_d = 0; depth = 0; pd = 0
      }
    }
  }
}
AWKSCANNER

# ── ① 主扫描（全仓 .sh、★ 含未跟踪 ★★）───────────────────────────────
list_file="$WORK_DIR/files.txt"
find . -name '*.sh' \
  -not -path './.git/*' \
  -not -path './vendor/*' \
  -not -path './node_modules/*' \
  -not -path './.audit-validation/*' \
  -print >"$list_file"
file_count=$(wc -l <"$list_file" | tr -d ' ')

hits_file="$WORK_DIR/hits.txt"
: >"$hits_file"
scan_ok=1
while IFS= read -r f; do
  [ -n "$f" ] || continue
  awk -f "$WORK_DIR/scanner.awk" "$f" >>"$hits_file" || scan_ok=0
done <"$list_file"
if [ "$scan_ok" != "1" ]; then
  note "shell-backtick-substitution: ABORT（★ 扫描器自身出错 ⇒ 门坏了、★ 不是判据红 ★★）"
  exit 2
fi
hit_count=$(wc -l <"$hits_file" | tr -d ' ')
note "shell-backtick-substitution: 扫 ${file_count} 个脚本，命中 ${hit_count} 条"

# ── ② 自探针（★ 注入已知违规 ⇒ 必须被抓到 ★★）────────────────────────
cat >"$WORK_DIR/probe-bad.sh" <<'PROBE_BAD'
#!/usr/bin/env bash
note "★ 口径＝通道的 `sha256_file` ★★"
PROBE_BAD
probe_hits=$(awk -f "$WORK_DIR/scanner.awk" "$WORK_DIR/probe-bad.sh" | wc -l | tr -d ' ')
if [ "${probe_hits:-0}" -lt 1 ]; then
  note "✘ 自探针没抓到已知违规样本 ⇒ ★ 门自己的检测逻辑坏了 ⇒ ★ 门恒绿却什么都不报"
  exit 1
fi
note "自探针 ✔（★ 检测逻辑能咬住已知违规样本 ★）"

# ── ③ 反向对照（★ 合法样本 ⇒ 必须不被抓 ★★）──────────────────────────
cat >"$WORK_DIR/probe-good.sh" <<'PROBE_GOOD'
#!/usr/bin/env bash
now="$(date +%s)"
printf '%s\n' "escaped \`tick\` here"
printf '%s\n' 'single quoted `tick` here'
cat <<'EOF'
heredoc body with `tick` is literal
EOF
rate=$(awk "BEGIN {if (\$TOTAL > 0) printf \"%.1f\", (\$PASSED/\$TOTAL)*100; else print \"0\"}")
PROBE_GOOD
good_hits=$(awk -f "$WORK_DIR/scanner.awk" "$WORK_DIR/probe-good.sh" | wc -l | tr -d ' ')
if [ "${good_hits:-0}" -ne 0 ]; then
  note "✘ 反向对照被误报（★ ${good_hits} 条 ★）⇒ ★ 门会把合法写法判红"
  awk -f "$WORK_DIR/scanner.awk" "$WORK_DIR/probe-good.sh" | sed 's/^/    · /'
  exit 1
fi
note "反向对照 ✔（★ 合法写法未被误报 ★）"

# ── ④ 判据位（★ 真实退出码、★ 不用「|| true」兜 ★★）──────────────────
if [ "${hit_count:-0}" -gt 0 ]; then
  note "✘ 发现 ${hit_count} 处**未转义的反引号**（★ 非注释行、★ 非单引号内 ★★）⇒ ★★"
  note "  ★★ 反引号是**老式**命令替换、★ 在双引号串里**同样会被 shell 真执行** ⇒ ★★"
  note "  ★★ ⇒ ★★ 请一律改用命令替换的新写法（★ 仓内既有惯例 ★★）★★"
  sed 's/^/      · /' "$hits_file" | head -30
  if [ "${hit_count:-0}" -gt 30 ]; then
    note "      · ……（★ 其余 $((hit_count - 30)) 条省略 ★）"
  fi
  note "shell-backtick-substitution: FAIL（${hit_count} 条判据不满足）"
  exit 1
fi

note "shell-backtick-substitution: PASS"
exit 0
