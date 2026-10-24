#!/usr/bin/env bash
# 扫描「$变量 紧跟非 ASCII 字节」的 shell 隐患。
#
# 背景（2026-10-03，818 部署在第 2/9 步被它挡住）：
#   scripts/lib/node-pm.sh 报「行 72: pm?: 未绑定的变量」，
#   而 pm_resolve 其实成功返回了 pnpm —— 诊断把方向完全指错了。
#
# -----------------------------------------------------------------------------
# ★★★ 2026-10-22 12h 审计轮：**判据被证明是错的，本轮订正**
# -----------------------------------------------------------------------------
# 原判据：「吞掉变量名的是紧跟其后的**全角逗号 U+FF0C**」，
#   「**不是所有中文标点都有问题**，只有全角逗号这一种」，
#   扫描式：`\$[A-Za-z_][A-Za-z0-9_]*，`（只认全角逗号）。
#
# ⇒ **那个断言是错的。** 本轮用 bash 3.2.57（macOS）实测了字符矩阵：
#
#   locale=en_US.UTF-8，set -u，形态 `v=VAL; printf "%s" "X$v<字符>Y"`：
#     ASCII 逗号 / 右括号 / 空格      ⇒ SAFE（值保留）
#     U+FF09 全角右括号 ）            ⇒ ★ 变量名被吞 ⇒ set -u 下脚本中止
#     U+FF08 全角左括号 （            ⇒ ★ 同上
#     U+FF0C 全角逗号 ，              ⇒ ★ 同上
#     U+FF1A 全角冒号 ：              ⇒ ★ 同上
#     U+3002 表意句号 。              ⇒ ★ 同上
#     U+4E2D 汉字「中」               ⇒ ★ 同上
#
#   ⇒ 规律是「**任意 >= 0x80 的字节**」，**不是**某一种标点。
#   ⇒ 而按仓内 Go 门（scripts/ursmcheck/cjk_var_expansion_test.go）的扫描口径
#     全仓实测 **50 处**站点里：
#       U+FF09 ）29 处 + U+FF08 （18 处 = 47 处（94%）
#       U+FF0C ，**只有 1 处**
#   ⇒ ⇒ ★ **旧判据恰好只抓最罕见的那一种，漏掉 49 处（98%）**。
#   ⇒ ⇒ 最刺眼的一例：2026-10-22 崩掉的那个并行会话，它要修的
#        `$label（助手报错）`（**全角左括号**）就在
#        scripts/audit/verify-l11b-cost-view-rowset.sh:233,250
#        ⇒ **旧判据对那个真实缺陷完全隐形**。
#
# ⚠★ **且这个缺陷是【locale 依赖】的**（本轮实测）：
#   LC_ALL=en_US.UTF-8 ⇒ 变量名被吞（set -u 下中止 / 否则静默失真）
#   LC_ALL=C 或未设置    ⇒ **完全正常**（值保留）
#   ⇒ 所以「生产是否中招」取决于宿主机 locale ⇒ 巡检/部署脚本
#      **不要依赖宿主 locale**，要么显式 `LC_ALL=C`，要么把 `$VAR` 写成 `${VAR}`。
#
# -----------------------------------------------------------------------------
# ★★★★ 2026-10-24 12h 审计轮（R147）：★ **判据与 Go 门再次对齐**
#   —— 起因是「**origin/main 上 `make guards` 就是红的**」
# -----------------------------------------------------------------------------
# ① 事实经过（pristine 对拍，`git worktree add --detach /tmp/lgw-pristine origin/main`）：
#      · pristine origin/main 上跑本门 ⇒ **rc=1**，报 5 处
#        （scripts/mut-nginx-realip-patch.sh:47/56
#          + tests/nginx_llm_realip_patch_test.sh:143/144/147）
#      · 本轮工作树上跑本门 ⇒ 同样 5 处、同样的行号 ⇒ **不是本轮改动引入的**
#      · ⇒ 但它**确实是窗口内引入的**：本门最后一次改是 aaa52091d（10-22 12:55），
#        而 Go 门在 3c9e42d41（10-23 07:21）**升级了扫描器**
#        （加了「尾随注释」与「`\$` 转义」两条口径）⇒ **两门判据漂移**。
#        ⇒ ⇒ 976a2b8de（10-24 01:32，窗口内）声称「收口【origin/main 上就红】的
#          CJK 门」时，**只收了 Go 侧**（那确实是红的），**shell 侧留着红**。
#
# ② 那 5 处**一处真站点都没有**（逐条用 bash 实测，判据见下）：
#      · 2 处是**尾随注释**（mut-nginx-realip-patch.sh:47/56）
#        ⇒ bash **不展开注释** ⇒ 不是站点
#        （★ 这两处**就是 Go 门自己在注释里点名、并且已经修掉的假阳性案例**）
#      · 3 处是**转义美元**（tests/nginx_llm_realip_patch_test.sh:143/144/147）
#        ⇒ `"\$remote_addr（…）"` 里 `\$` 是字面 `$` ⇒ `$remote_addr` **不会被展开**
#        ⇒ 不是站点
#      · ⇒ ⇒ **5 处全是假阳性** ⇒ 门红得没有道理 ⇒ 属于「门自身腐坏」，
#        它的危害不是「漏放缺陷」，而是「**假红 ⇒ 噪音 ⇒ 被忽略 ⇒ 真红也看不见**」。
#
# ③ 本轮修法：**把本门的判据对齐到 Go 门**（不是改那 5 处代码）。
#      ⇒ 理由：那 5 处的写法**是对的**（`\$remote_addr` 是在断言 nginx 配置里的
#        字面量；尾随注释里的 `$MUT_ENGINE` 只是给人看的）⇒ 改代码会破坏断言语义。
#      ⇒ 而「**判据的事实源**」自 2026-10-22 起就明确是 Go 门
#        （本文件原 :47-52 写着「两个都跳过整行注释与单引号区段」）。
#      ⇒ ⇒ 本门现在**逐字复刻** Go 门的四条口径（见下）+ 自带**自检矩阵**。
#
# ④ ★ 判据（与 scripts/ursmcheck/cjk_var_expansion_test.go **逐字一致**）：
#      ① 只认**裸** `$VAR`；`${VAR}` 带花括号是**安全**的（花括号终止变量名）。
#      ② 剥掉**尾随注释**（`#` 在引号外 **且**处于**词首** ⇒ `a#b` 是字面量）。
#      ③ 剥掉**单引号**区段（单引号里不发生展开）。
#      ④ `\$`（**奇数个**连续反斜杠）不是展开点。
#      ⑤ 其余位置：凡 `$VAR` 后面紧跟一个 **>= 0x80 的字节**，记 1 个站点。
#
# ⑤ ★ 扫描面与 Go 门**故意不同**，这里说清楚（不要再被「同扫描面」的旧文案误导）：
#      · Go 门：只扫 **git 追踪**的文件 ⇒ 读数只取决于「提交的树」
#        （它要拿读数去对**棘轮登记册**，读数必须确定）。
#      · 本门：扫 **已追踪 ∪ 未跟踪未忽略** ⇒ 刻意更宽 ⇒
#        「新写好还没 git add 的脚本」也在视野里（本门 2026-10-03 起就是这个意图：
#        新脚本最可能还没提交，也最可能藏着刚踩过的坑）。
#        本门**没有登记册**，读数不需要确定 ⇒ 宽面不引入不确定性。
#      ⇒ ⇒ 两门关系是：**判据逐字一致；扫描面 shell 门 ⊇ Go 门**。
#
# -----------------------------------------------------------------------------
# 为什么要有门：这类缺陷不报错、不留痕，只在「变量恰好未定义」的那一次
# 炸掉某个部署步骤，而部署到第 N 步才炸 ⇒ 前面 N-1 步已经改了生产。
#
# ★ 本门与仓内 Go 门（cjk_var_expansion_test.go）**判据现已一致**：
#   两个都认「裸 $VAR 后紧跟 >= 0x80 的字节」；
#   两个都剥掉尾随注释、单引号区段、`${VAR}` 形态与 `\$` 转义。
#   ⇒ Go 门是 CI 的**强制路径**（./scripts/ursmcheck 已接进 GUARD_PACKAGES_SLOW）；
#     本门保留为**不依赖 Go 的独立探针**，便于在只拿到脚本的机器上就地检查。
#   ⇒ ★ 两门的一致性由 scripts/ursmcheck/shell_fullwidth_var_gate_test.go
#     **逐条断言**（同一个合成输入喂两门，比对命中集合）⇒ 不是「口头一致」。
set -euo pipefail

# 向上找仓库根，不要手写 ../../..：
# 我最初写的是 ../../..，但本文件在 scripts/guards/（两层深），
# 上溯三层落到 syncfield —— 那里不是 git 仓库，
# 于是 `git ls-files` 枚举到 0 个文件，**门静默全绿**。
# 硬编码层数就会数错；改成「找到 .git 为止」，顺带对任意深度都对。
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# ★ 2026-10-22 订正：`-d` 只认**目录**，而 git worktree 里的 `.git` 是一个
#   **文件**（里面写着一行 `gitdir: …`）⇒ 在任何 worktree 里跑本门都会
#   「找不到 .git ⇒ ABORT exit 2」⇒ ⇒ 门在 worktree 里**根本跑不起来**。
#   ⇒ 改成 `-e`（文件或目录都算）。
while [ "$REPO_ROOT" != "/" ] && [ ! -e "$REPO_ROOT/.git" ]; do
  REPO_ROOT="$(dirname "$REPO_ROOT")"
done
[ -e "$REPO_ROOT/.git" ] || { echo "ABORT: 找不到 .git，仓库根推断失败" >&2; exit 2; }
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
# ★★ 2026-10-22 订正：探针改用**全角左括号**（真实缺陷里最常见的一种，
#   且**旧探针用的全角逗号恰恰是最罕见的一种**）⇒
#   探针覆盖的是「本轮实测会咬」的那一类，而不是「旧判据自认会咬」的那一类。
probe_out=$(LC_ALL=en_US.UTF-8 bash -c 'set -u; pm=X; printf "%s" "（$pm'"$(printf '\xef\xbc\x88')"'y）"' 2>&1 >/dev/null || true)
if ! _is_unbound "$probe_out"; then
  echo "ABORT: 探针未复现，本 bash 的行为已变或规则已失效，结论不可信" >&2
  echo "       （探针用的是全角左括号 U+FF08，且显式 LC_ALL=en_US.UTF-8）" >&2
  echo "       probe stderr: ${probe_out}" >&2
  exit 2
fi

# 反向对照：ASCII 括号必须不炸，否则说明我们对规则的理解是错的
ctl_out=$(LC_ALL=en_US.UTF-8 bash -c 'set -u; pm=X; printf "%s" "（$pm, y）"' 2>&1 >/dev/null || true)
if _is_unbound "$ctl_out"; then
  echo "ABORT: ASCII 字符也炸 —— 规则已失效，不要按本门结论改代码" >&2
  exit 2
fi

# -----------------------------------------------------------------------------
# ★ 扫描器（awk 实现，★ 逐字复刻 Go 门 cjk_var_expansion_test.go 的口径）
# -----------------------------------------------------------------------------
# 输入：grep -n 的输出，每行形如 `123:内容`。
# 输出：命中的行，形如 `123\t内容`（★ 用 TAB 分隔，因为内容里可能有 `:`）。
#
# ★ 全部按 **字节** 处理 ⇒ 整个扫描阶段强制 LC_ALL=C
#   （否则 awk 的 length()/substr() 按字符算，偏移全乱）。
#
# ★ 「非 ASCII 字节」用 `[\200-\377]` 精确表达（不是 `[^ -~]`：
#   后者还会命中 0x00-0x1F / 0x7F 这些控制字节，而 bash 不会把控制字节
#   并进变量名 ⇒ 用 `[^ -~]` 会把「$VAR<TAB>」误判成站点）。
SCANNER_AWK='
function is_word_start(c) {
  return (c == " " || c == "\t" || c == "\n" || c == "\r" || c == "\v" || c == "\f" ||
          c == ";" || c == "&" || c == "|" || c == "(" || c == ")" || c == "<" || c == ">")
}
function is_name_start(c) {
  return (c == "_" || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z"))
}
function is_name_char(c) {
  return (c == "_" || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z") || (c >= "0" && c <= "9"))
}
# code_segment：返回「会参与变量展开」的字节。
#   ★ 剥掉尾随注释（`#` 在引号外且处于词首）+ 单引号区段；
#     反斜杠在单引号外会吃掉下一个字节（并原样保留两个字节）。
function code_segment(line,   out, i, n, c, in_single, in_double, prev) {
  out = ""; i = 1; n = length(line); in_single = 0; in_double = 0
  while (i <= n) {
    c = substr(line, i, 1)
    if (c == "\\" && !in_single) {
      out = out c
      if (i < n) { i++; out = out substr(line, i, 1) }
      i++; continue
    }
    if (c == "\047" && !in_double) { in_single = 1 - in_single; i++; continue }
    if (c == "\"" && !in_single) { in_double = 1 - in_double }
    else if (c == "#" && !in_single && !in_double) {
      prev = (i > 1) ? substr(line, i - 1, 1) : ""
      if (i == 1 || is_word_start(prev)) return out
    }
    if (in_single) { i++; continue }
    out = out c; i++
  }
  return out
}
# strip_braced：把 `${NAME}` 换成等长占位（保持后续字节偏移不变）。
#   ★ `${NAME}` 是**安全**形态（花括号终止变量名）⇒ 不能当成站点。
function strip_braced(s,   out, i, j, n, c, k) {
  out = ""; i = 1; n = length(s)
  while (i <= n) {
    c = substr(s, i, 1)
    if (c == "$" && i < n && substr(s, i + 1, 1) == "{") {
      j = i + 2
      while (j <= n && is_name_char(substr(s, j, 1))) j++
      if (j <= n && substr(s, j, 1) == "}" && j > i + 2) {
        for (k = i; k <= j; k++) out = out "\001"
        i = j + 1; continue
      }
    }
    out = out c; i++
  }
  return out
}
# has_site：段里是否存在「裸 $VAR 紧跟 >= 0x80 字节」。
function has_site(s,   i, j, n, c, nb) {
  i = 1; n = length(s)
  while (i <= n) {
    c = substr(s, i, 1)
    if (c != "$") { i++; continue }
    # ★ `\$` 不是展开点 ⇒ 用【反斜杠奇偶】判定（不是「前一个字节是不是反斜杠」）：
    #   bash 里 `\\` 是一个字面反斜杠 ⇒ 它后面的 `$` 仍然会展开；
    #   只有【奇数个】连续反斜杠才把 `$` 变成字面量。
    if (i > 1 && substr(s, i - 1, 1) == "\\") {
      nb = 0; j = i - 1
      while (j >= 1 && substr(s, j, 1) == "\\") { nb++; j-- }
      if (nb % 2 == 1) { i++; continue }
    }
    j = i + 1
    if (j > n) break
    if (!is_name_start(substr(s, j, 1))) { i++; continue }
    while (j <= n && is_name_char(substr(s, j, 1))) j++
    if (j <= n && substr(s, j, 1) ~ /[\200-\377]/) return 1
    i = j
  }
  return 0
}
{
  rec = $0
  p = index(rec, ":")
  if (p > 0) { ln = substr(rec, 1, p - 1); body = substr(rec, p + 1) }
  else { ln = "0"; body = rec }
  t = body
  sub(/^[ \t]+/, "", t)
  if (t == "" || substr(t, 1, 1) == "#") next      # ★ 整行注释
  if (has_site(strip_braced(code_segment(body)))) print ln "\t" body
}
'

# -----------------------------------------------------------------------------
# ★★ 自检矩阵：证明**扫描器有牙**，而且**不咬不该咬的**。
#   ★ 每一条期望值都不是拍脑袋写的 ⇒ 全部用真实 bash（LC_ALL=en_US.UTF-8）
#     实测过：变量先定义（v=X），只观察「变量名是否被 CJK 字节吞掉」
#     （签名 = 报错文案里出现那个非 ASCII 字节）。
#   ★ 形态            期望      bash 实测
#     "X$v中Y"        HAZARD    bash: v中: unbound variable
#     "$v）"          HAZARD    bash: v）: unbound variable
#     "$v（"          HAZARD    bash: v（: unbound variable
#     a#b "$v中"      HAZARD    bash: v中: unbound variable  ★ `#` 不在词首 ⇒ 不是注释
#     "X\\$v中Y"      HAZARD    bash: v中: unbound variable  ★ `\\` ⇒ `$` 仍展开
#     "X${v}中Y"      SAFE      值正常保留
#     "X\$v中Y"       SAFE      值正常保留
#     x # 注释 $v中   SAFE      注释不展开
#     '$v中'          SAFE      单引号里不展开
#     "X$v" # 尾随…   SAFE      尾随注释不展开
#     "X$v" && …      SAFE      正常位置
# -----------------------------------------------------------------------------
_scan_line() {
  # $1 = 整行内容；命中站点则返回 0（true），否则返回 1（false）
  # ★ 注意：这里**不**用 `awk ... | grep -q .` —— 那种写法在 `set -o pipefail` 下
  #   会把「awk 自己执行失败」与「没命中」**混成同一个退出码** ⇒ ⇒ 扫描器坏了
  #   门也会「假装干净」⇒ ⇒ 这里显式分开：awk 失败 ⇒ ABORT；awk 成功但无输出 ⇒ 没命中。
  local out
  out=$(LC_ALL=C awk "$SCANNER_AWK" <<< "0:$1") || {
    echo "ABORT: 扫描器执行失败（判据不可信，不要按本门结论改代码）" >&2
    exit 2
  }
  [ -n "$out" ]
}

_self_test() {
  local fails=0 line want got
  # 真站点（必须命中）
  for line in 'echo "X$v中Y"' 'echo "$v）"' 'echo "$v（"' \
              'echo a#b "$v中"' 'echo "X\\$v中Y"'; do
    want=hit
    if _scan_line "$line"; then got=hit; else got=miss; fi
    if [ "$got" != "$want" ]; then
      echo "ABORT: 自检矩阵失败（应命中却没命中）：$line" >&2; fails=$((fails + 1))
    fi
  done
  # 非站点（必须放过）
  for line in 'echo "X${v}中Y"' 'echo "X\$v中Y"' 'echo x # 注释 $v中' \
              "echo '\$v中'" 'echo "X$v" # 尾随注释 $v中' 'echo "X$v" && echo "Y"'; do
    want=miss
    if _scan_line "$line"; then got=hit; else got=miss; fi
    if [ "$got" != "$want" ]; then
      echo "ABORT: 自检矩阵失败（不应命中却命中了）：$line" >&2; fails=$((fails + 1))
    fi
  done
  if [ "$fails" -gt 0 ]; then
    echo "ABORT: 扫描器自检 ${fails} 条不符 ⇒ 判据不可信，不要按本门结论改代码" >&2
    exit 2
  fi
}
_self_test

# 枚举面 = 已跟踪 ∪ 未跟踪但未忽略。
# ★ 2026-10-03 补：原先只有 `git ls-files`，于是「新建但还没 git add」的
#   脚本完全不在门的视野里。写 ursm-redis-db-migrate.sh 时就踩中了：
#   脚本第 73 行有 `$ENV_FILE，`，门却报「无隐患」——因为文件当时未跟踪。
#   而这恰恰是风险最高的窗口：新写的脚本最可能还没提交，作者也最可能
#   手动直接运行它。门要能看见它。
#   未跟踪项走 --exclude-standard，node_modules / dist 等仍被 gitignore 排除。
#
# ★★ 2026-10-22 R107 轮（12h 审计 C11）修正：跳过目录口径**与 Go 门对齐**。
#   本门原先扫 `git ls-files '*.sh'` 的**全部**结果 ⇒ 把 **16 个 vendored
#   第三方 .sh**（vendor/github.com/blevesearch/... 等）也扫了进去；
#   而 Go 门 scripts/ursmcheck/cjk_var_expansion_test.go 的 cjkVarSkipDirs
#   明确跳过 vendor ⇒ **两个门自称「判据现已一致」（本文件原 :47-52），
#   实际扫描面不同** ⇒ 两边的结论无法互证。
#   ⇒ 而且这是一条**会红的路**：任何一次依赖升级把带 `$var<非ASCII>` 的
#     第三方脚本带进来，**本仓的门就会红** —— 而那与本仓代码质量无关
#     ⇒ 误报即噪音，噪音即被忽略。
#   ⇒ 跳过集现在**逐项对齐** cjkVarSkipDirs：.git / node_modules / vendor /
#     dist / build / .venv / __pycache__。
#   ★ 事实源单一化：下一层若要改跳过集，**两门必须同时改**。
#   ★★ 2026-10-24 订正：Go 门在 3c9e42d41 起改为「只扫 git 追踪的文件」
#     ⇒ 它的**跳过目录**已无意义（vendor 等都不再被扫）；
#     而本门是「追踪 ∪ 未跟踪」的并集 ⇒ **仍需**跳过目录口径
#     （未跟踪项里就有 vendor/ 与 node_modules/）。
SCAN_SKIP_DIRS='^\.git/|^node_modules/|^vendor/|^dist/|^build/|^\.venv/|__pycache__/'

scan_list() {
  {
    git ls-files '*.sh'
    git ls-files '*.bash'
    git ls-files --others --exclude-standard '*.sh'
    git ls-files --others --exclude-standard '*.bash'
  } | sort -u | grep -vE "$SCAN_SKIP_DIRS"
}

# ★ 预筛（**超集**）：只看原始形态「`$VAR` 后紧跟一个非 0x20-0x7E 字节」。
#   ⇒ 它只会**多**报候选行（注释里、单引号里、`\$` 转义的、`${VAR}` 的都在内），
#     真正的判定由 awk 按 Go 门口径做 ⇒ 预筛**不会漏**真站点
#     （真站点在原始字节里就是「$VAR」紧跟一个 >= 0x80 的字节）。
PATTERN='\$[A-Za-z_][A-Za-z0-9_]*[^ -~]'

hits=0
while IFS= read -r f; do
  [ -f "$f" ] || continue
  # ① 预筛（超集）：只有候选行才进扫描器 ⇒ 扫描器只跑在【少数几行】上
  cand=$(LC_ALL=C grep -nE "$PATTERN" "$f" 2>/dev/null || true)
  [ -n "$cand" ] || continue
  # ② 真正的判定（Go 门口径）
  #    ★ awk 失败必须 ABORT，而不是被 `|| true` 吞掉 ⇒ ⇒ 否则「扫描器坏了」
  #      会被读成「这个文件干净」⇒ ⇒ 门就会【假装干净】。
  found=$(printf '%s\n' "$cand" | LC_ALL=C awk "$SCANNER_AWK") || {
    echo "ABORT: 扫描器在 ${f} 上执行失败（判据不可信，不要按本门结论改代码）" >&2
    exit 2
  }
  [ -n "$found" ] || continue
  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    ln="${hit%%$'\t'*}"
    body="${hit#*$'\t'}"
    printf '  %s:%s\n    %s\n' "$f" "$ln" "$body"
    hits=$((hits + 1))
  done <<< "$found"
done < <(scan_list)

if [ "$hits" -gt 0 ]; then
  echo ""
  echo "发现 ${hits} 处「\$变量紧跟非 ASCII 字节」。这类写法一旦变量未定义就报"
  echo "「未绑定的变量 var?」，且往往炸在部署的中途（前面的步骤已改生产）。"
  echo "★ 只在 **UTF-8 locale**（en_US.UTF-8 / zh_CN.UTF-8）下发生；LC_ALL=C 下正常。"
  echo "★ 修法：改用 \${var} 界定。"
  exit 1
fi

echo "✓ 无「\$变量紧跟非 ASCII 字节」隐患（探针与反向对照均已通过）"
