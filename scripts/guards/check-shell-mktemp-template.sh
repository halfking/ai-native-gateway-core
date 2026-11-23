#!/usr/bin/env bash
# =====================================================================
# scripts/guards/check-shell-mktemp-template.sh —— 「mktemp 模板」跨平台门
#
# ★★ 2026-11-24 新增（★ 起因：★ **在生产 Linux 上真跑一道 macOS 上跑过的门** ★★）★★
#
# ★★★ 这道门为什么存在（一句话）★★★：
#   ★★ macOS 的 `mktemp -t <模板>` **容忍**模板里没有 X（★ 旧版 BSD 行为 ★★）
#   ★★ 而 **Linux 的 GNU coreutils `mktemp -t`** 要求模板里**至少 3 个 X** ★★
#   ★★ ⇒ ★★ **⇒ 一道在 macOS 上跑得好好的门、★ 在生产 Linux 上会直接坏掉** ★★
#   ★★ ★★ 本轮实测（★ 211 生产、★ Linux 9.7 coreutils ★★）：
#   ★★   $ mktemp -t deploy-ledger-plan
#   ★★   mktemp: too few X's in template 'deploy-ledger-plan'   ← ★★ 直接失败 ★★
#   ★★ ⇒ ★★ **⇒ 这不是「假绿」、★ 是「门自己坏了」★★ ⇒ ★★ ⇒ ★★ **⇒ 而它
#   ★★ 当时就在「空库新装烟测门」里、★ 那道门本来是要在生产跑的 ★★ ⇒ ★★ ⇒ ★★
#   ★★ **⇒ 这类缺陷只能靠【真在目标平台上跑一次】才暴露** ★★。
#
# ★★★ ★★ 判据（★ 逐条写明、★ 不做隐式放行 ★★★）★★★：
# ★★ ① 扫描面：★ 仓内全部 `.sh`（★ 含未跟踪文件 —— ★ 与全角变量门同口径 ★★）；
# ★★ ② 只看**真正会被执行**的行：★ 排除**整行注释**（★ `#` 开头 ★）★★
# ★★    与**行尾注释**（★ 首个 `#` 之后的内容 ★★）；
# ★★ ③ 只看 `mktemp` **带 `-t`** 的调用（★ 不带 `-t` 的、★ 模板自带 XXXXXX 的
# ★★    本来就安全 ★★）；
# ★★ ④ ★★ **模板必须像一个真实的路径前缀** ★★：★ **不含** `<` `>` `"` `'` `)`
# ★★    `(` `$` 这些**标记字符** ★★ ⇒ ★★ ⇒ ★ **⇒ 文档/示例语境被排除** ★★
# ★★    （★ ★ 关键：★ 排除不能靠「读起来像注释」来判、★ 那是量具自身的
# ★★    主观判断 ★★ ⇒ ★★ ⇒ ★ **用【模板的字符形态】来排除** ★★ ★★）；
# ★★ ⑤ 模板里**最长连续 X 段 < 3** ⇒ ★ 违规 ★★。
#
# ★ ★★ 三态 ★★：★ 命中违规 ⇒ FAIL（退出码 1）｜★ 无命中 ⇒ PASS（退出码 0）｜★
# ★ ★ 自探针没咬 / 反向对照被误报 ⇒ **ABORT（退出码 2）**（★ 门自己坏了 ★★）。
# ★ ★★ 量具自证（★ 本门自己的三条自检）★★：★
# ★   · 自探针：★ 一个已知违规样本（★ 模板无 X、★ 无标记字符 ★）必须被报出来 ★★
# ★   · 反向对照：★ 一个合法样本（★ 模板带 XXXXXX ★）必须**不**被报出来 ★★
# ★   · 文档语境：★ 一个示例文案样本（★ 模板含 `<` `>` ★）必须**不**被报出来 ★★
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

FAIL=0
note() { printf '%s\n' "$*"; }
bad()  { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL+1)); }

# ── 检测函数（★ 纯解析、★ 绝不执行被测行 ★）─────────────────────────
# ★ ★★ ★★ 量具自身的坑（★ 本轮实测连撞三次、★ 都记下来 ★★）★★ ★★：
# ★ ★★ ① 第一版用「数组元素精确等于 `mktemp`」来定位 ⇒ ★★ 而 `mktemp` 常被
# ★ ★★    写成 `$(mktemp …)` ⇒ ★★ 参数序列里它与 `-t` 会被 `$(` `)` 切开 ⇒ ★★
# ★ ★★    ⇒ ★★ **改用「含 mktemp 的词」** ★★；
# ★ ★★ ② 第一版把 **`mktemp` 当成命令真执行了**（★ 打印 usage ★★）⇒ ★★ ⇒ ★★
# ★ ★★    **⇒ 判据位绝不能执行被测代码** ⇒ ★★ ⇒ ★★ **⇒ 检测必须是【纯文本解析】** ★★；
# ★ ★★ ③ 第一版只排除「整行注释」⇒ ★★ ⇒ ★★ **行尾注释**与**文档串里的示例**
# ★ ★★    全部变成假阳性（★ 实测：★ 门自己脚本里的说明文字被判了 12 条 ★★）⇒ ★★
# ★ ★★    ⇒ ★★ **⇒ 排除要靠【模板的字符形态】**（★ 见判据④ ★★）★★。
violations() {
  # ★ 用法：★ violations <文件> ⇒ ★ 打印「行号:模板」★★
  awk '
    {
      raw = $0
      # ★ ★ 去掉**行尾注释**（★ 判据② ★）★★
      # ★ ★ 注意：★ `mktemp` 的模板里**不含** `#` ⇒ ★★ 所以首个 `#` 之后即可截断 ★★
      h = index(raw, "#")
      if (h > 0) { raw = substr(raw, 1, h - 1) }
      t = raw
      sub(/^[ \t]+/, "", t)
      if (t == "" || t ~ /^#/) { next }
      if (raw !~ /mktemp/) { next }
      n = split(raw, a, /[ \t]+/)
      for (i = 1; i <= n; i++) {
        if (a[i] ~ /mktemp/) {
          for (j = i + 1; j <= n; j++) {
            if (a[j] == "-t") {
              tmpl = a[j+1]
              gsub(/^["\x27$(]+/, "", tmpl)
              gsub(/["\x27)]+$/, "", tmpl)
              if (tmpl == "") { continue }
              # ★ ★ 判据④：★ 模板必须像一个**真实的路径前缀** ★★
              # ★ ★ （★ 不含标记字符 ⇒ ★ 文档/示例语境被排除 ★★）★★
              if (tmpl ~ /[<>"'"'"'()$]/) { continue }
              # ★ 数最长连续 X ★
              best = 0; k = 1; L = length(tmpl)
              while (k <= L) {
                if (substr(tmpl, k, 1) == "X") {
                  c = 0
                  while (k <= L && substr(tmpl, k, 1) == "X") { c++; k++ }
                  if (c > best) { best = c }
                } else { k++ }
              }
              # ★ 判据⑤：★ 最长连续 X 段 < 3 ⇒ 违规 ★
              if (best < 3) { print NR ":" tmpl }
            }
          }
        }
      }
    }
  ' "$1" 2>/dev/null
}

# ── 自探针（★ 检测逻辑必须能咬住已知违规样本 ★）─────────────────────
probe="$(mktemp "${TMPDIR:-/tmp}/mktemp-probe.XXXXXX")"
ok_sample="$(mktemp "${TMPDIR:-/tmp}/mktemp-ok.XXXXXX")"
doc_sample="$(mktemp "${TMPDIR:-/tmp}/mktemp-doc.XXXXXX")"
trap 'rm -f "$probe" "$ok_sample" "$doc_sample"' EXIT

printf '%s\n' 'TMP="$(mktemp -t bad-template-without-x)"' >"$probe"
if [ -z "$(violations "$probe")" ]; then
  printf 'ABORT: 自探针没咬住（★ 已知违规样本都没被判出来 ⇒ ★ 门自己的检测逻辑坏了 ⇒ ★ 不许判红）\n' >&2
  exit 2
fi
note "自探针 ✔（★ 检测逻辑能咬住已知违规样本）"

printf '%s\n' 'TMP="$(mktemp -t good-template.XXXXXX)"' >"$ok_sample"
if [ -n "$(violations "$ok_sample")" ]; then
  printf 'ABORT: 反向对照被误报（★ 合法样本被判违规 ⇒ ★ 门自己的检测逻辑坏了 ⇒ ★ 不许判红）\n' >&2
  exit 2
fi
note "反向对照 ✔（★ 合法样本未被误报）"

printf '%s\n' 'note "  ℹ 用法：mktemp -t <模板前缀>"' >"$doc_sample"
if [ -n "$(violations "$doc_sample")" ]; then
  printf 'ABORT: 文档语境被误报（★ 示例文案被判违规 ⇒ ★ 门自己的检测逻辑坏了 ⇒ ★ 不许判红）\n' >&2
  exit 2
fi
note "文档语境反向对照 ✔（★ 示例文案未被误报）"

# ── 平台读数（★ 本门自己就是那个跨平台缺陷的探测器 ★）─────────────
if [ "$(uname -s)" = "Linux" ]; then
  # ★ ★★ 用**含标记字符**的模板做探测（★ ⇒ 不触发本门判据 ★★）
  if mktemp -t "<probe-no-x>" >/dev/null 2>&1; then
    note "  ℹ 本机是 Linux：★ \`mktemp -t\` 竟对无 X 模板成功（★ 分布被改过 ⇒ ★ 结论仍成立、★ 但实测读数与本轮不符 ★）"
  else
    note "  ✔ 本机是 Linux：★ \`mktemp -t\` 对无 X 模板**实测失败**（★ GNU coreutils 要求 ≥3 个 X）★ ⇒ ★ **本轮的缺陷根因在 Linux 上可复现** ★"
  fi
else
  note "  ℹ 本机不是 Linux（★ $(uname -s) ★）⇒ ★ macOS 容忍无 X 的模板 ⇒ ★ **⇒ 这类缺陷只在 Linux 上暴露**（★ 本轮的教训）★"
fi

# ── 全仓扫描（★ 排除注释行 ★★）─────────────────────────────────────
# ★ ★★ 扫描面的边界（★ 逐条写明 ★★）：★★
# ★ · **排除 vendor/**（★ 第三方代码、★ 修它不属于本仓的职责、★ 且改第三方
# ★ ★   代码会在下次依赖升级时被覆盖 ★★）；
# ★ · **排除 node_modules/**（★ 同上 ★）；
# ★ · **排除 .git/**（★ 同上 ★）；
# ★ · **排除本门自己的脚本**（★ 它**故意**含违规样本、★ 用于自探针 ★★）。
while IFS= read -r f; do
  case "$f" in
    ./vendor/*|./node_modules/*|./.git/*) continue ;;
    ./scripts/guards/check-shell-mktemp-template.sh) continue ;;
  esac
  out="$(violations "$f")"
  [ -n "$out" ] || continue
  while IFS= read -r hitline; do
    [ -n "$hitline" ] || continue
    bad "★ ${f}:${hitline}（★ \`mktemp -t\` 的模板里必须含 **≥3 个连续 X**：★ GNU coreutils（★ Linux ★）要求 ≥3、★ 而 macOS 容忍没有 ⇒ ★ ⇒ ★ **在生产 Linux 上这道脚本会直接失败** ★★）"
  done <<EOF
$out
EOF
done < <(find . -type f -name "*.sh" \
             -not -path "./.git/*" \
             -not -path "./vendor/*" \
             -not -path "./node_modules/*" \
             2>/dev/null | sort)

if [ "$FAIL" -ne 0 ]; then
  printf 'shell-mktemp-template: FAIL（%d 条判据不满足）\n' "$FAIL" >&2
  exit 1
fi
note "shell-mktemp-template: PASS（★ 扫描面含未跟踪文件）"
exit 0
