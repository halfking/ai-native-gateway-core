#!/usr/bin/env bash
# 扫描「$变量 紧跟全角逗号」的 shell 隐患。
#
# 背景（2026-10-03，818 部署在第 2/9 步被它挡住）：
#   scripts/lib/node-pm.sh 报「行 72: pm?: 未绑定的变量」，
#   而 pm_resolve 其实成功返回了 pnpm —— 诊断把方向完全指错了。
#
# 已实证的精确规则（bash 5.3 / macOS 默认）：
#   吞掉变量名的是紧跟其后的**全角逗号 U+FF0C**。
#     "（$pm，y）"   -> 炸：$pm 与全角逗号的字节被并成一个不存在的标识符
#     "（$pm, y）"   -> 正常：半角逗号
#     "值：$pm/x"    -> 正常：全角冒号、斜杠都不构成标识符延续
#   所以**不是所有中文标点都有问题**，只有全角逗号这一种。
#   早先按「所有中文标点」扫出 31 行，绝大多数是误报（变量本就已定义）；
#   收窄到全角逗号后只剩 5 处（其中部分「侥幸安全」：变量恰好已定义，
#   一旦有人改成未定义就立刻炸）。
#
# 为什么要有门：这类缺陷不报错、不留痕，只在「变量恰好未定义」的那一次
# 炸掉某个部署步骤，而部署到第 N 步才炸 ⇒ 前面 N-1 步已经改了生产。
set -euo pipefail

# 向上找仓库根，不要手写 ../../..：
# 我最初写的是 ../../..，但本文件在 scripts/guards/（两层深），
# 上溯三层落到 syncfield —— 那里不是 git 仓库，
# 于是 `git ls-files` 枚举到 0 个文件，**门静默全绿**。
# 硬编码层数就是会数错；改成「找到 .git 为止」，顺带对任意深度都对。
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
while [ "$REPO_ROOT" != "/" ] && [ ! -d "$REPO_ROOT/.git" ]; do
  REPO_ROOT="$(dirname "$REPO_ROOT")"
done
[ -d "$REPO_ROOT/.git" ] || { echo "ABORT: 找不到 .git，仓库根推断失败" >&2; exit 2; }
cd "$REPO_ROOT"

# 枚举面自检：找不到任何 .sh 就说明根算错了，而不是「仓库里没有 shell 脚本」。
sh_count=$(git ls-files '*.sh' | wc -l | tr -d ' ')
if [ "$sh_count" -lt 10 ]; then
  echo "ABORT: 在 ${REPO_ROOT} 只枚举到 ${sh_count} 个 .sh —— 仓库根很可能算错了" >&2
  exit 2
fi

# 判「是否未绑定」不能 grep 中文「未绑定的变量」—— bash 的报错文案随 locale 变，
# 非 UTF-8 locale 下 grep 匹配不到，探针会假阴性、门直接 ABORT（我自己踩过）。
# 改判稳定特征：报错里变量名后紧跟一个非 ASCII 字节。
_is_unbound() { printf '%s' "$1" | LC_ALL=C grep -qE 'pm[^ -~]'; }

# 探针：先证明本门自己会咬（否则「0 命中」分不清是真干净还是门失效了）。
#
# 探针**不能免疫**：它必须真的复现「$变量 + 全角逗号」才能验证规则。
# 但它也不该需要豁免 —— 所以把全角逗号的字节用 '"$FW_COMMA"' 拼进去，
# 源码里就没有「$变量 紧跟全角逗号」这一形态，天然不被自己的规则命中。
# （第一版用行号白名单豁免自己：文件一编辑行号就漂、豁免静默失效，
#   门于是开始报自己的探针；改内容匹配后，M1 变异又因循环里 cd 改了工作
#   目录而失效。豁免机制本身就是负债。）
#
# 整段用单引号包住：里面的 $pm 必须保持字面、不被本脚本提前展开；
# 全角逗号则在单引号段之间用 '"…"' 拼进去。
FW_COMMA=$(printf '\xef\xbc\x8c')          # U+FF0C 全角逗号的 UTF-8 字节
probe_out=$(bash -c 'set -u; pm=X; echo "（$pm'"$FW_COMMA"'y）"' 2>&1 >/dev/null || true)
if ! _is_unbound "$probe_out"; then
  echo "ABORT: 探针未复现，本 bash 的行为已变或规则已失效，结论不可信" >&2
  echo "       probe stderr: ${probe_out}" >&2
  exit 2
fi

# 反向对照：半角逗号必须不炸，否则说明我们对规则的理解是错的
ctl_out=$(bash -c 'set -u; pm=X; echo "（$pm, y）"' 2>&1 >/dev/null || true)
if _is_unbound "$ctl_out"; then
  echo "ABORT: 半角逗号也炸 —— 规则已失效，不要按本门结论改代码" >&2
  exit 2
fi

hits=0
# pattern 放变量再传双引号是对的：`\$` 已在单引号里定死为正则锚点，
# 变量展开不会二次解释它。写成双引号字面量才会被 shell 折成非法正则。
PATTERN='\$[A-Za-z_][A-Za-z0-9_]*，'
FILTER=':\s*#|:\s*\*'

while IFS= read -r f; do
  while IFS= read -r line; do
    ln="${line%%:*}"; body="${line#*:}"
    printf '  %s:%s\n    %s\n' "$f" "$ln" "$body"
    hits=$((hits + 1))
  done < <(grep -nE "$PATTERN" "$f" 2>/dev/null | grep -vE "$FILTER")
done < <(git ls-files '*.sh')

if [ "$hits" -gt 0 ]; then
  echo ""
  echo "发现 ${hits} 处「\$变量紧跟全角逗号」。这类写法一旦变量未定义就报"
  echo "「未绑定的变量 var?」，且往往炸在部署的中途（前面的步骤已改生产）。"
  echo "修法：改用 \${var} 界定。已实证半角逗号 / 全角冒号 / 斜杠都不受影响。"
  exit 1
fi

echo "✓ 无「\$变量紧跟全角逗号」隐患（探针与反向对照均已通过）"
