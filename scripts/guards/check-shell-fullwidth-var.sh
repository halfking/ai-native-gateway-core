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
# 为什么要有门：这类缺陷不报错、不留痕，只在「变量恰好未定义」的那一次
# 炸掉某个部署步骤，而部署到第 N 步才炸 ⇒ 前面 N-1 步已经改了生产。
#
# ★ 本门与仓内 Go 门（cjk_var_expansion_test.go）**判据现已一致**：
#   两个都认「裸 $VAR 后紧跟 >= 0x80 的字节」；
#   两个都跳过整行注释与单引号区段。
#   ⇒ Go 门是 CI 的**强制路径**（本轮已把 ./scripts/ursmcheck 接进
#     GUARD_PACKAGES_SLOW）；本门保留为**不依赖 Go 的独立探针**，
#     便于在只拿到脚本的机器上就地检查。
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

hits=0
# ★★★ 判据订正（2026-10-22）：从「只认全角逗号 U+FF0C」改为
#   「**裸 $VAR 后紧跟任意 >= 0x80 的字节**」——与仓内 Go 门一致。
#   pattern 放变量再传双引号是对的：`\$` 已在单引号里定死为正则锚点，
#   变量展开不会二次解释它。写成双引号字面量才会被 shell 折成非法正则。
PATTERN='\$[A-Za-z_][A-Za-z0-9_]*[^ -~]'

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
SCAN_SKIP_DIRS='^\.git/|^node_modules/|^vendor/|^dist/|^build/|^\.venv/|__pycache__/'

scan_list() {
  {
    git ls-files '*.sh'
    git ls-files '*.bash'
    git ls-files --others --exclude-standard '*.sh'
    git ls-files --others --exclude-standard '*.bash'
  } | sort -u | grep -vE "$SCAN_SKIP_DIRS"
}

# ★ 跳过口径与 Go 门一致：整行注释 + 尾随注释 + 单引号区段
#   （单引号里不发生展开；注释里也不发生展开）。
#   用 awk 做：按 bash 的真实语义单遍走一行（记住单/双引号状态），
#   剥掉尾随注释与单引号区段，再判剩余部分。
#
# ★★★★ 2026-10-24 magpie 对齐轮 §3.17：**本函数此前与 Go 门【不一致】**，
#   ⇒ 本门在既有代码上**恒红**（实测 5 处），而 Go 门（CI 必跑）早已修正过。
#   ⇒ 这是「两门自称判据一致、实际不一致」的形态：★ **坏的不是没同步的那一边**，
#     而是**没同步的那一边假红** ⇒ ⇒ 假红比假绿更费（它让人去改本来正确的代码）。
#
#   本轮对齐的三条（全部来自 Go 门 scripts/ursmcheck/cjk_var_expansion_test.go）：
#     ① **转义的 `$` 不是展开点**（反斜杠【奇偶】判定）：
#        bash 里 `\\` 是【一个字面反斜杠】⇒ 它后面的 `$` **仍然会**被展开；
#        只有【奇数个连续反斜杠】才把 `$` 变成字面量。
#        ★ 触发站点（实测）：scripts/mut-nginx-realip-patch.sh 与
#          tests/nginx_llm_realip_patch_test.sh 里的 `\$remote_addr` / `\$ns_real_ip`
#          —— 那是 nginx 配置的**字面文本**，不是变量展开 ⇒ 本门原先误报。
#     ② **尾随注释**不参与展开（`#` 在引号外且处于词首才是注释起始）。
#     ③ **`${VAR}` 形式是安全的**（有界定界，变量名不会被后续字节吞掉）。
_scan_line() {
  # $1 = 整行内容；命中则 echo 1
  #
  # ★★★ 2026-10-24 §3.17 二次修正：**必须显式 LC_ALL=C**。
  #   实测：非 C locale 下 awk 对含非 ASCII 的行报
  #   「towc: multibyte conversion failure」⇒ **awk 直接死、函数什么都不输出**
  #   ⇒ 门对所有行都返回「未命中」⇒ ★ **门变成恒绿门**（变异后仍绿）。
  #   ⇒ 这就是「0 命中的第一嫌疑是量具坏了」的一例。
  #   ⇒ LC_ALL=C 下 awk 按**字节**工作 ⇒ multibyte 转换不再发生。
  printf '%s\n' "$1" | LC_ALL=C awk '
    BEGIN { ins = 0; ind = 0 }
    {
      line = $0
      out = ""
      n = length(line)
      for (i = 1; i <= n; i++) {
        c = substr(line, i, 1)
        # ★ 反斜杠：下一个字节按字面处理（`\$` 不展开、`\\` 是一个字面反斜杠）。
        #   写出去时连同下一个字节一起写 ⇒ 保留「奇偶」信息供后面判定。
        if (c == "\\" && !ins) {
          out = out c
          if (i < n) { i++; out = out substr(line, i, 1) }
          continue
        }
        if (c == "\047" && !ind) { ins = !ins; continue }   # 单引号
        if (c == "\042" && !ins) { ind = !ind; out = out c; continue }  # 双引号
        # ★ 尾随注释：引号外 + 词首 ⇒ 后面整段不参与展开。
        if (c == "#" && !ins && !ind) {
          if (i == 1 || substr(line, i-1, 1) == " " || substr(line, i-1, 1) == "\t") {
            break
          }
        }
        if (!ins) out = out c
      }
      # ★ 在「会展开」的字节里找裸 $VAR + 非 ASCII 后续字节。
      #   反斜杠奇偶：数 `$` 前面连续反斜杠的个数，偶数 ⇒ `$` 仍会被展开。
      m = length(out)
      for (i = 1; i <= m; i++) {
        c = substr(out, i, 1)
        if (c != "$") continue
        # ★ 反斜杠奇偶判定（★ 不是「前一个字节是不是反斜杠」：
        #   bash 里 `\\` 是一个字面反斜杠 ⇒ 它后面的 `$` **仍然会**被展开）。
        if (i > 1 && substr(out, i-1, 1) == "\\") {
          nbs = 0
          for (k = i - 1; k >= 1; k--) {
            if (substr(out, k, 1) != "\\") break
            nbs++
          }
          if (nbs % 2 == 1) continue   # ★ 转义过的 $ ⇒ 字面量 ⇒ 不是展开点
        }
        # ★ `${VAR}` 形式安全 ⇒ 跳过（$ 后紧跟 { 不是裸变量名）
        if (i < m && substr(out, i+1, 1) == "{") continue
        # 变量名首字符
        if (i >= m) continue
        v = substr(out, i+1, 1)
        if (v != "_" && !(v >= "a" && v <= "z") && !(v >= "A" && v <= "Z")) continue
        # 变量名后续字符
        j = i + 2
        while (j <= m) {
          d = substr(out, j, 1)
          if (d == "_" || (d >= "a" && d <= "z") || (d >= "A" && d <= "Z") || (d >= "0" && d <= "9")) {
            j++; continue
          }
          break
        }
        # ★ 紧跟的下一个字节必须是非 ASCII（>= 0x80 ⇒ 字节值不在 0x20..0x7E）
        if (j <= m) {
          d = substr(out, j, 1)
          if (d !~ /[ -~]/) { print "1"; exit }
        }
      }
    }'
}

# ═══════════════════════════════════════════════════════════════════════
# ★★★ 2026-10-24 §3.17.8：`--self-test` 模式 —— **扫描器量具自检**
# ═══════════════════════════════════════════════════════════════════════
#
# ★ 这道门此前**没有任何自检**：它只证明「bash 会咬」，不证明「**扫描器会咬**」。
# ⇒ ★★★ 本轮实测过这条失效路径：把 `_scan_line` 里的 awk 换成非 C locale
#   ⇒ `towc: multibyte conversion failure` ⇒ awk 死、函数什么都不输出
#   ⇒ **门对所有行都返回「未命中」** ⇒ ★★ **恒绿门**（M20b 变异后仍绿）。
# ⇒ 而那种失效**只有靠外部注入真站点才能发现** ⇒ 靠人记得跑变异，不是门。
#
# ⇒ 所以本模式把「扫描器有没有牙」变成**自检**：
#   ① 用**合成用例**直接喂 `_scan_line` ⇒ 必须判对（真站点=1 / 安全形态=0）；
#   ② 任何一条判错 ⇒ **exit 2（ABORT）** ⇒ 门不会静默变绿。
#
# ★ 输出格式（供 Go 门 scripts/ursmcheck/shell_cjk_var_crosscheck_test.go 对拍）：
#   每行 `CASE <期望0|1> <实际0|1> <用例名>`
#   ⇒ Go 门读它、与自己的扫描器逐条比对 ⇒ ★★ **两门结论必须一致**。
#
# ★ 用例的**期望值来自 bash 的真实语义**（实测：bash 3.2.57 / 5.3.9，locale=zh_CN.UTF-8）
#   ⇒ 与 Go 门 cjk_var_expansion_test.go 的合成用例**同源**。
#   ★★ 但**对拍的那份表不在本门手里**：跨门门 scripts/ursmcheck/
#     shell_cjk_var_crosscheck_test.go **自带一份独立的 13 条**
#   ⇒ ⇒ ★ 理由：若复用同一份表 ⇒ 一边改错 ⇒ 两边用同一份错表 ⇒ **两门会一致地错**。
if [ "${1:-}" = "--self-test" ]; then
  _self_fail=0
  _self_case() {
    # $1=期望(0/1) $2=实际(0/1) $3=用例名
    local want="$1" got="$2" name="$3"
    printf 'CASE %s %s %s\n' "$want" "$got" "$name"
    if [ "$want" != "$got" ]; then
      _self_fail=$((_self_fail + 1))
    fi
  }
  _st() { # $1=期望 $2=行内容 ⇒ 实际
    local got
    got="$(_scan_line "$2")"
    if [ -n "$got" ]; then got=1; else got=0; fi
    printf '%s' "$got"
  }
  # ★★ 下面这 13 条是**合成用例载荷**，它们**故意**含 `$v中` 这类形态。
  #
  # ★★★ 2026-10-24 §3.17.9 改法（**删掉哨兵机制**，理由见下）：
  #
  #   本轮先按「把用例包在 `$(_st 1 '…')` 里」写 ⇒ ★ **门把自己判成 8 处站点**：
  #   ① Go 门（scripts/ursmcheck/cjk_var_expansion_test.go，CI 必跑）按
  #      **bash 语义**扫描 `.sh`/`.bash` ⇒ 它对 `"$( … '…' … )"` 这种
  #      **嵌套引号**判定不了（`'` 在 `"` 里不算引号）⇒ 把载荷**整段当成展开点**；
  #   ② 本门（shell 门）此前靠**哨兵标记**把自己的用例行从扫描面排除
  #      ⇒ ⇒ ★★ **两门对「仓库面」的结论就此分裂**（Go 门算 8 处、shell 门算 0 处）
  #      ⇒ ★ 这正是「两门自称判据一致、实际不一致」的形态（只是这次是**本轮自己**）。
  #
  #   ⇒★ 正解：**载荷用单引号赋值**（`_p1='echo "值=$v中"'`）。
  #     bash 语义上**单引号里不发生展开** ⇒ ⇒ 这些行**本来就不是站点**
  #     ⇒ ⇒ 两门（Go 门 / shell 门）**都**判 0 ⇒ **结论自然一致**
  #     ⇒ ⇒ **不需要任何排除机制** ⇒ ★ 哨兵机制整个删掉。
  #
  #   ★★ 为什么「删掉哨兵」比「保留哨兵」更好：
  #     哨兵 = 「这段不许扫」 ⇒ 任何人写一段
  #     `SHELL-GATE-SELFTEST-CASES:BEGIN … END` 就能把**真的站点藏起来**
  #     ⇒ ★ **那是给自己开的后门** ⇒ 删除它比保留它更安全。
  #
  #   ★ 用例的**可读性没有损失**：载荷仍然逐条写在文件里、期望值仍然逐条可见。
  #   ★ 唯一例外是第 4 条（载荷本身含单引号）⇒ 用双引号赋值 + `\$` 转义
  #     （★ 转义后 `$` 不展开 ⇒ 运行时载荷与原来**逐字相同**）。
  _p1='echo "值=$v中"'
  _p2='echo "值=${v}中"'
  _p3='# echo "值=$v中"'
  _p4="echo '值=\$v中'"
  _p5='echo "值=$v)"'
  _p6='echo "a=$x中"'
  _p7='echo "值=\$v中"'
  _p8='echo "值=\\$v中"'
  _p9='echo "ok"  # （用 $v中）'
  _p10='run_gate() {  # 说明（用 $MUT_ENGINE）'
  _p11='echo "x"#y $v中'
  _p12='echo "a#b $v中"'
  _p13=''
  _self_case 1 "$(_st 1 "$_p1")"                    "裸变量紧跟汉字"
  _self_case 0 "$(_st 0 "$_p2")"                    "花括号形式安全"
  _self_case 0 "$(_st 0 "$_p3")"                    "整行注释"
  _self_case 0 "$(_st 0 "$_p4")"                    "单引号内"
  _self_case 0 "$(_st 0 "$_p5")"                    "ASCII 紧跟安全"
  _self_case 1 "$(_st 1 "$_p6")"                    "两处各算一次"
  _self_case 0 "$(_st 0 "$_p7")"                    "转义美元不展开"
  _self_case 1 "$(_st 1 "$_p8")"                    "偶数反斜杠仍展开"
  _self_case 0 "$(_st 0 "$_p9")"                    "尾随注释"
  _self_case 0 "$(_st 0 "$_p10")"                   "尾随注释在函数定义后"
  _self_case 1 "$(_st 1 "$_p11")"                   "双引号内的井号"
  _self_case 1 "$(_st 1 "$_p12")"                   "井号非词首不是注释"
  _self_case 0 "$(_st 0 "$_p13")"                   "空行"

  if [ "$_self_fail" -ne 0 ]; then
    echo "SELFTEST FAIL ${_self_fail}" >&2
    echo "ABORT: 扫描器自检失败（${_self_fail} 条判错）⇒ 本门的结论不可信" >&2
    exit 2
  fi
  echo "SELFTEST OK 13"
  exit 0
fi

while IFS= read -r f; do
  [ -f "$f" ] || continue
  # ★★ 2026-10-24 §3.17.9：本门**不再排除任何行**。
  #
  #   此前靠哨兵标记（`SHELL-GATE-SELFTEST-CASES:BEGIN/END`）把自己的合成用例行
  #   从扫描面里排除 ⇒ ★★ 那是**给自己开的后门**（任何人写一段同款哨兵就能把
  #   真的站点藏起来）⇒ ⇒ 本轮把哨兵机制**整个删掉**。
  #
  #   ⇒ 现在自检用例改用**单引号赋值**（`_p1='echo "值=$v中"'`）
  #     ⇒ bash 语义上**单引号里不发生展开** ⇒ 这些行**本来就不是站点**
  #     ⇒ ⇒ 本门与 Go 门（scripts/ursmcheck/cjk_var_expansion_test.go）
  #     **都**判 0 ⇒ 两门结论一致，且**没有排除机制**。
  #   ⇒ ⇒ ★ 附带收益：本门**重新具备「抓自己」的牙齿**
  #     （往本文件注入一处真站点 ⇒ 本门与 Go 门都会红）。
  while IFS= read -r line; do
    ln="${line%%:*}"; body="${line#*:}"
    # 整行注释跳过（Go 门同款口径：首个非空白字符是 #）
    trimmed="$(printf '%s' "$body" | sed 's/^[[:space:]]*//')"
    case "$trimmed" in
      '#'*) continue ;;
    esac
    if [ -n "$(_scan_line "$body")" ]; then
      printf '  %s:%s\n    %s\n' "$f" "$ln" "$body"
      hits=$((hits + 1))
    fi
  done < <(LC_ALL=C grep -nE "$PATTERN" "$f" 2>/dev/null)
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
