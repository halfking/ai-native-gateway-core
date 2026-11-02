#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""measure-pipefail-race-surface.py —— 「SIGPIPE 竞态面」普查量具（六轴判别）

## 它是什么

一个**只产出候选集**的静态普查器。给定仓库根，它回答：

    「哪些 shell 管道处，处在『pipefail 生效 + 消费者会早退 + 写端可能很大』的
      风险形状里？」

⇒ ★ 它**不判定任何一处是不是真缺陷** —— 静态判据只能给候选集（§19.20.54 ⑥ 2）。

## 为什么需要六轴（不是一轴）

R96 轮实测出一个真缺陷：`printf … | grep -q` + `set -o pipefail`
⇒ 写端被 SIGPIPE 打死 ⇒ 管道退出码 141 ⇒ 判据随机翻转。
本轮把它推广到全仓时，发现**「有没有 `| grep -q`」远不足以定位风险面**：

| 轴 | 名称 | 静态可判？ | 判据 |
|----|------|-----------|------|
| 1 | pipefail **真启用** | ✔ 可判 | 剥注释后真有 `set` 命令启用它（`+o` 关闭要排除） |
| 2 | **作用域** | △ 半可判 | 启用点与管道点是否同一生效作用域（顶层 / 同函数 / 继承） |
| 3 | **时序** | ✔ 可判 | 同一作用域内，管道行号 > 启用行号 |
| 4 | **消费者早退性** | ✔ 可判 | `grep -q` / `head -c` / `head -n` / `read` / `awk … exit` |
| 5 | **输入形态** | ✘ 需实测 | 有无行边界（无换行的巨型单行 ⇒ `grep` 读到 EOF 才判 ⇒ **不早退**） |
| 6 | **写端量级**（★ 已拆成 6a/6b，见下） | ✘ 需实测 | 决定是否进入「翻转概率斜坡」（★ **不是二分**：实测边界内不翻转 / > 边界**不保证**翻转） |
| 7 | **退出码的用途** | ✔ 可判 | `judgment`（退出码作判据）/ `value`（取值）/ `bare`（裸管道） |

★ 轴 5、6 **静态不可判** ⇒ 这正是「必须逐条人工裁决」的原因。
★ 轴 7 与 R66 轮的「判别型 vs 兜底型」同族：**只有退出码参与语义判断的管道，
  SIGPIPE 才会把「结论」翻掉**；取值用的管道危害形态不同（`set -e` 下是意外中止）。

## ★★★ 轴 6 拆成**两个量**（第三十一轮，§28.18.8 登记项 1 ⇒ 本轮落地）

★ ★★ 第**三十**轮实测把「管道缓冲 65,536 B」这个**单一**说法推翻成**两个量**
⇒ ⇒ **同一个 65,536 B 数字，在两种形态下是两种东西**：

★ ★★ **统一判据（★ 四族实测全部被这一条解释）**：
① ★ **谁被杀** = **紧邻消费者的那个阶段**（消费者早退 ⇒ 关闭读端 ⇒ 它的写端挨 EPIPE）
② ★ **阈值** = **被杀者的写入粒度**

| 谁被杀 | 判据量 | 阈值（行式 vs 块写） | Linux 实测 | macOS 实测 |
|---|---|---|---|---|
| ★ **写端**（写端**直连**消费者） | **写端总量** | 块写命令 ⇒ **管道容量 ≈ 64 KiB** | 65,536 B → 0/5 翻转、85,831 B → 1/5、173,831 B → 4/5 | 65,536 B → 0/5、85,831 B → 5/5 |
| ★ **写端**（但写端**本身**是 `grep`） | **写端总量** | 行式小写 ⇒ **`PIPE_BUF`** | （同 `PIPE_BUF` 档） | |
| ★★ **中间段**（消费者前面还有管道段） | **中间段的输出体量** | 写 `grep` ⇒ **`PIPE_BUF`**；写 `cat`/`zcat` ⇒ **管道容量** | `grep` 中间段：4,096 B → 0/5、**4,200 B → 5/5**；`cat` 中间段：65,520 B → 0/5、98,280 B → 5/5 | `grep`：16,384 B → 0/5、**32,768 B → 5/5**；`cat`：65,520 B → 0/5、173,820 B → 5/5 |

★ ★★ **两条纪律（第三十一轮复测后写死）**：
1. ★ **禁止二分措辞**：翻转是**竞态 ⇒ 概率型**（实测每点 5 次）⇒
   「≤ 边界**确定**不翻转 / > 边界**确定**翻转」这种二分**一律不成立**
   ⇒ 台账里凡是引阈值，必须写成「**实测边界内**不翻转 / > 边界**不保证**翻转」。
2. ★ **阈值必须带平台**：★ 同一形态在两平台的边界可差 **16 倍**
   （行式档：Linux 4,096 B vs 块写档 64 KiB = 16×）⇒ ⇒ **本机（macOS）量的阈值
   不能用来判生产（211 = Linux）**。

## ★ 轴 1 的三个坑（本轮实测踩出来的）

1. **`set -uo pipefail` / `set -euo pipefail` 也算启用** —— `-o` 可以**内嵌在组合 flag 里**
   （bash 解析 `-uo` = `-u` + `-o`，参数取下一个词 `pipefail`）。
   ⇒ 只认 `set -o pipefail`（`-o` 独立）的实现会**整片漏掉**。
2. **`set +o pipefail` 是关闭** ⇒ 不能算启用。
3. **注释里的 `set -o pipefail` 不是启用** ⇒ 必须先剥注释。
   ⇒ （实测：仓里有 4 个文件只是**注释**提到 pipefail，其中一个的注释恰恰写着
   「别把内容灌进 `grep -q`」—— 文本扫描会把它们全算成启用。）

## 退出码

    0  候选集已产出（分母自检全过）
    3  ★ 量具自检失败 ⇒ 分母不可信 ⇒ 拒绝出读数
    4  用法错误
"""
import argparse
import hashlib
import os
import re
import sys

# ── 脚本集合口径 ────────────────────────────────────────────────────────────
# ★ `.audit-validation/` 是 **.gitignore 的临时校验区**（每次校验跑一份脚本/迁移副本）
#   ⇒ 它**不属于仓库** ⇒ 必须排除，否则分母会随「最近跑没跑校验」漂移。
#   实测：同一基线，干净检出 = 684 个脚本；共享树（含 64 个未跟踪副本）= 798 个
#   ⇒ 候选集两边完全相同（352 行同键）⇒ 差异只在分母，不在结论。
#
# ★★★ R107 轮收尾（2026-10-28）★ **同一个洞，长到了 5117 个脚本**：
#   `.worktrees/` 与 `.audit-validation/` 是**同一类东西**（★ .gitignore 的、
#   **不属于仓库内容**的目录）⇒ ⇒ 按上面那条已经写死的纪律，它**同样必须排除**。
#   实测（本机，2026-10-28）：`.worktrees/` = **2.0 GB / 4352 个 `.sh`**
#   ⇒ ⇒ 脏树扫描面 = **5117 个脚本**；★ 同一棵树的**纯净副本**（`git archive`）= 少数几百
#   ⇒ ⇒ ★★ **同一棵树、同一套代码、同一个工具 ⇒ 两个读数**
#   （★ **对方生成器** `measure-pipefail-scope-drop.py` 在脏树上 **605~623** 行
#     vs 纯净副本 **83~89** 行）
#   ⇒ ⇒ 差异 **100% 来自扫描面** ⇒ ⇒ **不是口径变体、也不是被测对象变了**。
#   ⇒ ⇒ ★★★ **【订正·2026-10-28 复测后】**：★ 上行那对读数**不是本文件的口径矩阵**
#   （★ `probe-pipefail-caliber-matrix.py` **共用本文件的 `collect_scripts`** ⇒ ★ 本文件
#   把 `.worktrees` 排除后**连带把矩阵也修了**）⇒ ⇒ ★★ **⇒ 修复后本文件/矩阵在
#   【脏 checkout 面】上已读出 89/87/85/83 行 ⇒ 与【纯净面】逐格相同** ⇒ ⇒ ⇒ ★
#   **⇒ 「脏 checkout 605~623」是【修复前】的读数，且属【对方生成器】**。
#   ⇒ ⇒ ★★ **⇒ 报数必须把【分母的面】与【分子的面】分开标注**：★ **分母** = 扫描脚本数
#   （★ **717 ＝ 脏 checkout 面 / 714 ＝ 干净树面**；★ 差 3 个未跟踪 `.sh` ⇒ 它们
#   **不命中任何判据** ⇒ ⇒ **判据面完全相同**）｜★ **分子对扫描面不敏感**（★ 候选
#   **369 处 / 361 物理行 / 157 文件**、被丢弃 **267 处 / 98 文件** ⇒ ★ 脏面与干净面
#   **逐项相同**）⇒ ⇒ ⇒ ★ **⇒ 只有分母敏感 ⇒ ⇒ 「717 / 361 / 267」这一组数必须写成
#   「脏面 717 · 候选 361 · 被丢弃 267」**。
#   ⇒ ⇒ ★★★ **【订正·2026-10-28 复测后 · 口径】**：★ 上行「被丢弃 267 处 / 98 文件」是
#   **【主计数口径】**（★ 本文件主计数**排除量具自身探针**）⇒ ⇒ ★ **独立复算（含自身探针）
#   在【同一棵树】上读出 269 处 / 99 文件** ⇒ ⇒ ★★ **⇒ 差 2 = 本仓 `scripts/audit/
#   probe-pipefail-sigpipe-shape.sh` 自身的 2 处丢弃点**（★ 它的测试管道【故意】要制造
#   SIGPIPE ⇒ 命中被测形态是设计如此 ⇒ 【排除它零判定影响】）⇒ ⇒ ⇒ ★★★ **⇒ 「269」在
#   这个仓里有【两个互不相干的口径】：★ ① R107 轮的 269 =【旧树 + 主计数口径】⇒ 现行
#   267 = 树漂移 −2；★ ② 现在的 269 =【新树 + 含自身探针口径】⇒ 差 2 = 口径差** ⇒ ⇒ ⇒ ★
#   **⇒ 同一个数在两个口径下撞车 ⇒ 就会被读成「没漂移」⇒ ⇒ ⇒ ⇒ 报「某个数变了」之前
#   必须先问「两把尺子的口径一样吗？」⇒ ⇒ ⇒ ⇒ 口径不同 ⇒ 差值里【混着两件事】（树漂移 +
#   口径差）⇒ ⇒ ⇒ ⇒ ★★ **⇒ 差值必须【归因到单一机制】后才算读数成立**。
#   ⇒ ⇒ ★★ **⇒ 读数要钉【三个锚点】：基线 SHA（钉内容）＋ 扫描面指纹（钉分母）＋ 工具版本（钉判据）**
#   ⇒ ⇒ ★★★ **⇒ 因为污染源（残留 worktree）**不在 git 里** ⇒ ⇒ ⇒ **基线 SHA 钉不住它。**
#   ⇒ ⇒ ★★ **⇒ 曾经被量化过的洞不会自己变小 ⇒ ⇒ 它只会在你不看的时候变大。**
#   ⇒ ⇒ ★ 配套：对方的 `measure-pipefail-scope-drop.py` 用**自己独立的** `collect()`
#   ⇒ ⇒ **不调用本文件的 `collect_scripts`** ⇒ ⇒ ⇒ **本行的改动不会影响对方的清单**
#   ⇒ ⇒ ⇒ （★ 已实测：清单 ↔ 生成器强判据仍 PASS ⇒ 清单逐字未变）。
SKIP_DIRS = {".git", "node_modules", "dist", "build", ".venv", "venv", "__pycache__",
             ".audit-validation", ".worktrees"}
DEFAULT_SKIP_PREFIXES = ("vendor/",)

# ── 剥注释（shell 版）：引号内的 # 不算注释 ─────────────────────────────────
def strip_shell_comments(text):
    """逐行剥掉 shell 注释。引号内的 '#' 保留。

    ★ 必须处理：单引号 / 双引号 / 反引号 / 行尾续行。
    ★ heredoc 不做处理（heredoc 体里的 '#' 会被误当注释）⇒ 记为已知局限。
    """
    out = []
    for line in text.split("\n"):
        i = 0
        n = len(line)
        sq = False      # 单引号内
        dq = False      # 双引号内
        bt = False      # 反引号内
        cut = -1        # 注释起点
        while i < n:
            c = line[i]
            if sq:
                if c == "'":
                    sq = False
            elif dq:
                if c == "\\" and i + 1 < n:
                    i += 2
                    continue
                if c == '"':
                    dq = False
            elif bt:
                if c == "`":
                    bt = False
            else:
                if c == "\\" and i + 1 < n:
                    i += 2
                    continue
                if c == "'":
                    sq = True
                elif c == '"':
                    dq = True
                elif c == "`":
                    bt = True
                elif c == "#":
                    # ★ 只有当 # 是该 token 的开头才是注释
                    if i == 0 or line[i - 1] in " \t":
                        cut = i
                        break
            i += 1
        out.append(line if cut < 0 else line[:cut])
    return "\n".join(out)


# ── 轴 1：pipefail 启停点 ─────────────────────────────────────────────────
RE_SET = re.compile(r"^\s*set\s+(?P<rest>\S.*)$")


def parse_set_flags(rest):
    """把 `set` 之后的参数解析成 (启用 pipefail?, 关闭 pipefail?)。

    ★ 关键：**`-o` 可以内嵌在组合 flag 里** —— `set -uo pipefail` / `set -euo pipefail`
      都会被 bash 解析成「-u/-e 加上 -o pipefail」。
      ⇒ 判据：把参数切成 token；对每个形如 `-<letters>` 的 token，
        若其字母集合里含 `o` ⇒ 该 token 的**下一个 token** 就是 `-o` 的参数。
    """
    toks = rest.split()
    on = False
    off = False
    for idx, t in enumerate(toks):
        if t.startswith("-") and not t.startswith("--"):
            letters = t[1:]
            if "o" in letters:
                nxt = toks[idx + 1] if idx + 1 < len(toks) else ""
                if nxt == "pipefail":
                    on = True
                elif nxt:
                    # 形如 `set -o` 后面跟别的选项名 ⇒ 不是 pipefail
                    pass
        elif t.startswith("+") and "o" in t[1:]:
            nxt = toks[idx + 1] if idx + 1 < len(toks) else ""
            if nxt == "pipefail":
                off = True
    return on, off


# ── 轴 2：作用域（函数边界）────────────────────────────────────────────────
RE_FUNC_DEF = re.compile(r"^\s*(?:function\s+)?(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*\(\s*\)\s*\{")


def scope_map(lines):
    """返回 {行号(1-based): 作用域名}。顶层为 '<top>'。

    ★ 简易实现：见到函数定义行开一个作用域，见到该函数体的闭合 '}' 关闭。
      缩进/嵌套不做严格判定 ⇒ **函数体识别可能不准** ⇒ 这是已知局限，
      逐条裁决时必须回读原文件确认。
    """
    scopes = {}
    stack = []          # [(func_name, brace_depth_at_open)]
    depth = 0
    for i, ln in enumerate(lines, 1):
        m = RE_FUNC_DEF.match(ln)
        if m and depth == 0:
            stack.append((m.group("name"), depth))
        scopes[i] = stack[-1][0] if stack else "<top>"
        # 粗略计花括号
        depth += ln.count("{") - ln.count("}")
        while stack and depth <= stack[-1][1]:
            stack.pop()
    return scopes


# ── 轴 4：早退消费者 ───────────────────────────────────────────────────────
# ★ `(?<!\|)`：**排除 `||` 的第二个竖线** —— 否则 `if grep -q 'a||b' "$X" || grep -q c`
#   这种**根本没有管道**的行会被当成候选（本轮实测到的假阳性来源）。
RE_PIPE_CONSUMER = re.compile(
    r"(?<!\|)\|\s*(?P<c>"
    r"grep\b(?P<ga>[^\n|]*)"
    r"|head\b(?P<ha>[^\n|]*)"
    r"|read\b(?P<ra>[^\n|]*)"
    r"|awk\b(?P<aa>[^\n|]*)"
    r"|tail\b(?P<ta>[^\n|]*)"
    r"|sed\b(?P<sa>[^\n|]*)"
    r")"
)


def consumer_kind(cons):
    """返回 (是否早退, 类别标签)。"""
    c = cons.strip()
    if c.startswith("grep"):
        m = re.search(r"(^|\s)-[a-zA-Z]*q", c)
        if m:
            return True, "grep-q"
        return False, "grep"
    if c.startswith("head"):
        return True, "head"
    if c.startswith("read"):
        return True, "read"
    if c.startswith("awk"):
        return (True, "awk-exit") if re.search(r"\bexit\b", c) else (False, "awk")
    if c.startswith("tail"):
        return False, "tail"
    if c.startswith("sed"):
        return (True, "sed-q") if re.search(r"\bq\b", c) else (False, "sed")
    return False, "other"


# ── 候选集 ─────────────────────────────────────────────────────────────────
# ── 轴 7：管道退出码的「用途」（判据用 vs 取值用）──────────────────────────
# ★ 这是 R66 轮「判别型 vs 兜底型」同族：只有**退出码参与语义判断**的管道，
#   SIGPIPE 才会把「结论」翻掉。取值用（`x=$(… | head -1)`）的管道，
#   退出码多半被丢弃 ⇒ 危害形态不同（set -e 下是「意外中止」而非「假红」）。
RE_IF_PIPE = re.compile(r"^\s*(?:if|elif|while|until)\b")
RE_AND_OR = re.compile(r"&&|\|\|")


def strip_quoted(s):
    """把字符串里的**引号内**内容去掉（保留引号外的字符）。

    ★ 本轮实测到的假阳性来源：`if grep -q 'a||b' "$X" || grep -q c; then`
      里的 `||` 在**单引号内的正则**中，不是管道分隔符。
      ⇒ 判断「管道前是否有 `||`/`&&`」时必须先剥引号，否则会把「取值/判据」判错。
    """
    out = []
    i = 0
    n = len(s)
    sq = dq = bt = False
    while i < n:
        c = s[i]
        if sq:
            if c == "'":
                sq = False
            out.append(" ")
        elif dq:
            if c == "\\" and i + 1 < n:
                i += 2
                continue
            if c == '"':
                dq = False
            out.append(" ")
        elif bt:
            if c == "`":
                bt = False
            out.append(" ")
        else:
            if c == "\\" and i + 1 < n:
                out.append(" ")
                out.append(" ")
                i += 2
                continue
            if c == "'":
                sq = True
                out.append(" ")
            elif c == '"':
                dq = True
                out.append(" ")
            elif c == "`":
                bt = True
                out.append(" ")
            else:
                out.append(c)
        i += 1
    return "".join(out)


def cmd_tail(text):
    """取「**同一条命令**」在 text 里的尾部：遇到**未加引号的** `;` 就停。

    ★ 为什么必须有这个边界（2026-11-03 第二十九轮）：
      shell 的 AND-OR 列表（`a | b || c`）以 `;` 结束 ⇒ `;` 之后的 `||`/`&&`
      属于**另一条命令** ⇒ ⇒ 若不设边界，会把「别人的退出码」算到这条管道头上
      ⇒ ⇒ ★ 这是**假阳性**方向，必须避免（★ 宁可漏判也不能误判）。
    """
    s = strip_quoted(text)
    idx = s.find(";")
    return s if idx < 0 else s[:idx]


def classify_usage(line, pipe_start, post="", next_line=""):
    """返回 (用途, 依据片段)。

    用途取值：judgment（退出码作判据）/ value（取值）/ bare（裸管道）。
    ★ 判断前先剥引号（引号里的 `||`/`&&` 不是运算符）。

    ★★★ 2026-11-03 第二十九轮：**「消费退出码」有三处位置，全都要看**：
      ① 管道**之前**的前缀（既有判据：`if … | …`、`cmd && cmd | …`）
      ② 同一条命令**尾部**（管道**之后**）：★ `cmd | grep -q X || fail "..."`
         ⇒ 退出码**参与控制流**（fail-closed 方向）
      ③ **续行**：当前行以 `\` 结尾 ⇒ 下一物理行是**同一条命令**
         ⇒ ★ `cmd | grep -q X \` + `|| fail "..."` ⇒ 同上

    ⇒ ★★ **漏掉 ②③ 的后果**（★ 本轮实测的缺陷本体）：
      这些行的退出码**明明参与控制流**，却被判成 `bare`
      ⇒ ⇒ 自动初稿给出「**退出码不参与语义判断**」这个**假前提**
      ⇒ ⇒ 而假前提会被后来人当成「体量与本判据无关」的依据 ⇒ ⇒ **门被弄瞎**。
      ⇒ ★★★ **规模实测（★ 用本文件自己的函数做「新旧口径对照探针」量出来的
      ——★ 不是用外部脚本量的）**：
      ★ 全仓扫描 735 个脚本 / 733 处管道消费者 site：

      | 量 | 读数 |
      |---|---|
      | ② 命中（同行尾部有 `||`/`&&`） | **102 处** |
      | ③ 命中（真续行的下一行有 `||`/`&&`） | **15 处**（②③ 无交集） |
      | ⇒ ②③ 合计命中 | 117 处 |
      | － 其中**赋值上下文**（`x="$(cmd \| …)"` ⇒ `value` 规则**优先**） | －54 处 ⇒ **不翻转**（正确） |
      | － 其中 ① 管道**前**的前缀已覆盖（已是 `judgment`） | －1 处 ⇒ **不翻转**（正确） |
      | ⇒ ★ **实际 usage 翻转**（`bare`→`judgment`） | ★ **62 处** |
      | ⇒ · 落在**候选集内**（`after_pf=True` ⇒ 进台账 ⇒ ★ **真实爆炸半径**） | ★ **44 处** |
      | ⇒ · 落在**候选集外**（被作用域口径丢弃 ⇒ 本工具已印为「静默丢弃」） | 18 处 |

      ⇒ ⇒ ★★ **台账层面的爆炸半径 = 44**（与变异臂实测的「缺 44 + 多 44」独立吻合）。
      ⇒ ⇒ ★★ **两个曾经登记过的数都是错的**：
      ★ 「**23 行**」（§28.15.5）—— 探针读的是台账 `writer` 列，不是源码行；
      ★ 「**58 + 15 = 73 行**」（本轮早先的草稿）—— 用外部剥注释器量的是「**命中数**」，
        **不是**「usage **实际翻转**数」⇒ ⇒ ★ **命中 ≠ 翻转**（赋值上下文会吃掉 54 处）。
      ⇒ ⇒ ★ **纪律第三次兑现**：「我量出来的数」本身也要被重新测量 ——
        ★ 而且**本轮自己草稿里的数也是错的** ⇒ ⇒ ★ 量具里**连 docstring 都得写实测值**。
    """
    pre = line[:pipe_start]
    pre_raw = pre
    pre_nq = strip_quoted(pre)
    stripped = pre_nq.strip()
    if RE_IF_PIPE.match(stripped):
        return "judgment", stripped[:40]
    if re.search(r"\b(if|elif|while|until)\b", pre_nq):
        return "judgment", stripped[:40]
    # ★ 「取值」判定用**原始**前缀：`x="$(cmd | head -1)"` 的 `$(` 在双引号内，
    #   剥引号会把 `$(` 一起剥掉 ⇒ 用 pre_nq 判会把取值误判成 bare。
    if "=" in pre_raw and re.search(r"=\s*[\"']?\$\(", pre_raw):
        return "value", stripped[:40]
    if RE_AND_OR.search(pre_nq):
        return "judgment", stripped[:40]
    # ★★ ② 同一条命令的**尾部**（管道之后）的 `||` / `&&`
    tail = cmd_tail(post)
    if RE_AND_OR.search(tail):
        return "judgment", (stripped + " … " + tail.strip())[:40]
    # ★★ ③ 续行里的 `||` / `&&`（★ 只有**真续行**才算：管道行以 `\` 结尾）
    if line.rstrip().endswith("\\") and next_line:
        cont = cmd_tail(next_line)
        if RE_AND_OR.search(cont):
            return "judgment", (stripped + " ⏎ " + cont.strip())[:40]
    return "bare", stripped[:40]


# ── 轴 6 的静态启发：写端输出量级（bounded / unbounded / unknown）──────────
# ★ 静态只能给**启发**，不能给结论 ⇒ 台账里仍需逐条裁决。
# ★ 2026-10-21 R97 轮按实测细化：把「确定短输出」的命令归入 bounded
#   （docker ps --format / psql -tAc / redis-cli ping / pg_isready / launchctl list …）。
# ★★★ **本轮自查抓到的第四个（也是最严重的）量具缺陷**：
#   下面这组分支原先是用 `|` 拼在**一个**正则里的 ⇒ 最后一个分支是**空串**
#   ⇒ **匹配一切** ⇒ 整个轴 6 恒真 ⇒ 之前那批「自动 bounded」的裁决全是这个 bug 的产物。
#   ⇒ ⇒ **通用式：★ 用 `|` 拼多个分支时，最后一个分支不能是空的；空分支 ⇒ 匹配一切 ⇒ 判据恒真。**
#   ⇒ ⇒ 修法：**一组独立正则 + 逐个匹配**（不用 alternation）⇒ 下面每条都自带非空模式。
R97_WRITER_BOUNDED_PATTERNS = [
    re.compile(r"grep\b[^|]*\|\s*(head|wc)\b"),
    re.compile(r"head\b[^|]*\|\s*(head|wc)\b"),
    re.compile(r"printf\b[^|]*\|\s*(head|wc)\b"),
    # —— 短输出命令（本轮逐条读码确认）——
    re.compile(r"docker\s+ps\b[^|]*--format"),
    re.compile(r"docker\s+inspect\b[^|]*--format"),
    re.compile(r"\bpsql\b[^|]*-tA"),
    re.compile(r"\bpsql\b[^|]*-At"),
    re.compile(r"redis-cli\b[^|]*\bping\b"),
    re.compile(r"\bpg_isready\b"),
    re.compile(r"launchctl\s+list\b"),
    re.compile(r"\bcrontab\s+-l\b"),
    re.compile(r"\bgit\b[^|]*ls-files\b"),
    re.compile(r"\bcurl\b[^|]*-f\s+-s\b"),
    re.compile(r"\bsystemctl\b[^|]*list-unit-files\b"),
    re.compile(r"\bgo\s+list\b[^|]*-deps\b"),
]
R97_WRITER_UNBOUNDED_PATTERNS = [
    re.compile(r"\bcat\b"), re.compile(r"\btail\b"), re.compile(r"\bfind\b"),
    re.compile(r"\bls\b"), re.compile(r"\bps\b"), re.compile(r"\bpgrep\b"),
    re.compile(r"docker\s+(inspect|ps|logs)\b"), re.compile(r"kubectl\s+get\b"),
    re.compile(r"\bpsql\b"), re.compile(r"\bpg_dump\b"), re.compile(r"\bwc\b"),
    re.compile(r"\bdu\b"), re.compile(r"\bdf\b"),
    # ★ gunzip/zcat/cat 家族：解压后体量未知（hotzone 那条真缺陷就在这里）
    re.compile(r"\bgunzip\b"), re.compile(r"\bzcat\b"), re.compile(r"\bunzip\b"),
]
# ★★★ `echo`/`printf` **带变量参数**时，写端体量取决于**变量内容**
#   ⇒ 不能算 bounded ⇒ 必须判 unknown。
#   （本轮自查时抓到的第三个口径缺陷：`echo "$out" | grep -q x` 的 `$out`
#     可能是任意大小 ⇒ 「有界」是假的。）
RE_ECHO_VAR = re.compile(r"\b(echo|printf)\b[^|]*\$")
RE_ECHO_LITERAL = re.compile(r"(^|[;&|(\s])(echo|printf)\b")


# ── ★★★ 轴 6 的拆分：判据量 + 适用阈值（第三十一轮，§28.18.8 登记项 1）─────
# ★ 这四个短码是**机器可读**的 ⇒ 门按它逐行核对台账措辞（⇒ 措辞与形态漂移 ⇒ 门红）。
CRIT_A = "写端总量/管道容量"            # A 类：`grep -q`/`grep -m` ⇒ 杀的是**写端**
CRIT_C = "写端总量/管道容量"            # C 类：`head`/`awk-exit` **直连**写端 ⇒ 杀的是**写端**
CRIT_B_GREP = "中间段匹配输出/PIPE_BUF"   # B 类 + 中间段是 grep ⇒ 杀的是**中间段**（行式小写）
CRIT_B_BULK = "中间段输出体量/管道容量"    # B 类 + 中间段是块写命令 ⇒ 杀的是**中间段**
CRIT_A_GREP = "写端匹配输出/PIPE_BUF"      # ★ 写端**本身**是行式命令（grep）⇒ 阈值也降档
CRIT_C_GREP = "写端匹配输出/PIPE_BUF"      # ★ 同上（head/awk 直连，但写端是 grep）
CRIT_UNKNOWN = "跨行管道/未知"            # ★ 写端侧在上一行 ⇒ **判据量看不出来** ⇒ 不猜
# ★ ★★ 实测读数（第三十一轮，★ 每点 5 次重复；★ 负载已同时记录）：
#   A 类（写端总量）：Linux 65,536 B → 0/5 翻转、85,831 B → 1/5、173,831 B → 4/5；
#                     macOS 65,536 B → 0/5、85,831 B → 5/5
#   C 类（写端总量）：Linux 65,520 B → 0/5、85,800 B → 5/5；macOS 65,520 B → 0/5、131,040 B → 3/5
#   B 类（中间段=grep，判据量=匹配输出）：Linux 4,096 B → 0/5、**4,200 B → 5/5**（≈`PIPE_BUF`）；
#                     macOS 16,384 B → 0/5、**32,768 B → 5/5**
#   B 类（中间段=cat，判据量=透传体量）：Linux 65,520 B → 0/5、85,800 B → 1/5、98,280 B → 5/5；
#                     macOS 65,520 B → 0/5、131,040 B → 2/5、173,820 B → 5/5
# ⇒ ⇒ ★★ **翻转是「竞态 ⇒ 概率型」** ⇒ 「≤ 边界**确定**不翻转 / > 边界**确定**翻转」
#      这个二分在实测上**两个方向都不成立**（Linux 87,943 / 107,831 / 131,072 B 实测 0/5 翻转）。
RE_SINGLE_PIPE = re.compile(r"(?<!\|)\|(?!\|)")
# ★★★ 第三十二轮：形态 A 的两个结构判据（★ **不依赖** RE_PIPE_CONSUMER 那条贪婪正则
#   ⇒ ★ 探测器的读数才可信 ⇒ ★ 第一版探测器就是栽在这条正则的贪婪上）
RE_SINGLE_PIPE_END = re.compile(r"(?<!\|)\|(?!\|)\s*$")
RE_CONSUMER_AT_START = re.compile(
    r"^\s*(?P<c>"
    r"grep\b[^\n|]*"
    r"|head\b[^\n|]*"
    r"|read\b[^\n|]*"
    r"|awk\b[^\n|]*"
    r"|tail\b[^\n|]*"
    r"|sed\b[^\n|]*"
    r")")


def mask_quotes(ln):
    """★ 把**引号内**的字符替换成空格（★ 长度不变 ⇒ 下标对得上）。

    ★ 为什么需要：管道行里常有**字面量管道**（`cut -d'|'`、`grep -E "a|b"`）
    ⇒ ★ 若不屏蔽，就会把「引号里的 |」当成管道段 ⇒ **形态分类错**。
    """
    out = list(ln)
    i, n = 0, len(ln)
    while i < n:
        c = ln[i]
        if c == "'":
            j = ln.find("'", i + 1)
            j = n - 1 if j < 0 else j
            for k in range(i, j + 1):
                out[k] = " "
            i = j + 1
        elif c == '"':
            j = i + 1
            while j < n:
                if ln[j] == "\\":
                    j += 2
                    continue
                if ln[j] == '"':
                    break
                j += 1
            j = min(j, n - 1)
            for k in range(i, j + 1):
                out[k] = " "
            i = j + 1
        else:
            i += 1
    return "".join(out)


def middle_stage_of(ln, consumer_start):
    """★ 取「早退消费者的**前一段**」文本（★ 没有 ⇒ 返回 None ⇒ 直连写端）。

    ★ 只认**单个**管道（`|`）⇒ `||`（逻辑或）不算管道段（★ 与 RE_PIPE_CONSUMER 同口径）。
    ★ ★ 管道位置在**屏蔽引号后**的文本里找（★ 否则 `cut -d'|'` 会被误判成管道段）。
    """
    masked = mask_quotes(ln)
    head = masked[:consumer_start]
    last = None
    for m in RE_SINGLE_PIPE.finditer(head):
        last = m
    if last is None:
        return None
    seg = ln[last.end():consumer_start].strip()
    return seg or None


def writer_volume_hint(writer):
    """静态启发：写端输出量级。返回 bounded / unbounded / unknown。

    ★ 判别次序（顺序本身就是规则）：
      ① 明确的无界命令（cat/find/ps/…）⇒ unbounded；
      ② 明确的短输出命令（docker ps --format / psql -tAc / …）⇒ bounded；
      ③ echo/printf **带变量** ⇒ unknown（体量取决于变量内容）★ 本轮自查修正；
      ④ echo/printf **纯字面量** / herestring ⇒ bounded；
      ⑤ 其余（grep 读文件、awk 范围、命令替换…）⇒ unknown。
    """
    w = writer.strip()
    if not w:
        return "unknown"
    # ★ 顺序即规则：**精确的短输出形态先判**（带 --format / -At / -tAc 的那些）
    #   ⇒ 否则 `docker ps` / `psql` 会被无界列表先命中 ⇒ 短输出被误判。
    for pat in R97_WRITER_BOUNDED_PATTERNS:
        if pat.search(w):
            return "bounded"
    for pat in R97_WRITER_UNBOUNDED_PATTERNS:
        if pat.search(w):
            return "unbounded"
    if RE_ECHO_VAR.search(w):
        return "unknown"
    if RE_ECHO_LITERAL.search(w):
        return "bounded"
    return "unknown"


def criterion_of(kind, middle_stage, writer=""):
    """★ 第三十一轮：算出这一行的「**判据量**」与「**适用阈值**」。

    ★★ 为什么要有这个函数（§28.18.8 登记项 1：把轴 6 拆成两个量）：
      ⇒ 台账原来到处写「管道缓冲 65,536 B」这**一个**数字，
        但第三十/三十一轮实测证明：**同一个 65,536 B，在两种形态下是两种东西**。

    ★ ★★ **统一判据（★ 四族实测都能被这一条解释）**：

      ① ★ **谁被杀** = **紧邻消费者的那个阶段**
         （消费者 `grep -q`/`head`/`awk` 早退 ⇒ 关闭读端 ⇒ 它的写端挨 EPIPE）
         ⇒ 写端**直连**消费者 ⇒ 杀的是**写端**（判据量 = 写端总量）
         ⇒ 写端前面**还有管道段** ⇒ 杀的是**中间段**（判据量 = 中间段输出体量）
      ② ★ **阈值** = **被杀者的写入粒度**
         ⇒ 被杀者是**行式小写**命令（`grep`：一行一次 write）⇒ 总输出 ≤ `PIPE_BUF`
           时**一次原子写**就写完了 ⇒ 写者先退出 ⇒ **不翻转**
           ⇒ ★ 阈值 = **`PIPE_BUF`**（Linux **4,096 B** / macOS ≈16–32 KiB）
         ⇒ 被杀者是**块写**命令（`cat`/`curl`/`echo`：一次 write 写一大块）
           ⇒ 写满管道容量（Linux 64 KiB）才阻塞 ⇒ ★ 阈值 = **管道容量**

    ★ ⇒ ★★ **判据与阈值是两个独立维度**（★ §28.18.8 的表把它们绑在「形态类」上
      ⇒ 那是简化 ⇒ ★ 本轮按「谁被杀 × 它的写入粒度」重新分档）。
    ★ ⇒ 返回值用 `/` 分隔的**机器可读**短码 ⇒ 门按它逐行核对台账措辞。
    """
    victim = middle_stage if middle_stage else writer
    line_wise = bool(re.search(r"\bgrep\b", victim or ""))
    if middle_stage:
        return CRIT_B_GREP if line_wise else CRIT_B_BULK
    return CRIT_A_GREP if line_wise else CRIT_A


class Site(object):
    __slots__ = ("path", "line", "scope", "consumer", "kind", "writer", "pf_line",
                 "pf_scope", "after_pf", "usage", "usage_basis", "volume",
                 "full_line", "dropped_by_scope", "middle", "criterion")

    def __init__(self, **kw):
        for k in self.__slots__:
            setattr(self, k, kw.get(k))


def scan_file(path, rel):
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            raw = f.read()
    except OSError:
        return None, None
    body = strip_shell_comments(raw)
    lines = body.split("\n")
    scopes = scope_map(lines)

    # 轴 1 + 3：pipefail 启停点
    pf_on = []       # (line, scope)
    pf_off = []      # (line, scope)
    for i, ln in enumerate(lines, 1):
        m = RE_SET.match(ln)
        if not m:
            continue
        on, off = parse_set_flags(m.group("rest"))
        if on:
            pf_on.append((i, scopes.get(i, "<top>")))
        if off:
            pf_off.append((i, scopes.get(i, "<top>")))

    # 轴 4 + 3：管道处
    sites = []

    def mk_site(i, ln, m, kind, writer_override=None, invisible=False):
        """★ 建一个 site（★ 两条检测路径**共用** ⇒ ★ 口径零漂移）。"""
        sc = scopes.get(i, "<top>")
        # 同作用域内，pipefail 是否已启用（且之后未被关闭）
        on_before = [pl for pl, ps in pf_on if ps == sc and pl < i]
        off_after = [pl for pl, ps in pf_off if ps == sc and on_before and pl > max(on_before)]
        pf_line = max(on_before) if on_before else 0
        after_pf = bool(on_before) and not off_after
        # ★★ R103 轮：把【被作用域模型静默丢弃】的点标出来（★ 只标不改）
        #   ★ 真实 shell 语义：`set -o pipefail` 是**进程级**选项 ⇒ **函数体继承**
        #     ⇒ 顶层的 pipefail 对函数体内的管道点**是生效**的
        #   ★ 本量具口径（after_pf）：要求 pipefail 的作用域与 site 的作用域**完全相同**
        #     ⇒ ⇒ 函数体里的管道点 after_pf=False ⇒ **不进候选集** ⇒ 静默丢弃
        #   ⇒ 这里只统计 + 打印，**不改 after_pf 的判定** ⇒ 候选集/台账/门零影响
        top_on = [pl for pl, ps in pf_on if ps == "<top>" and pl < i]
        top_off = [pl for pl, ps in pf_off
                   if ps == "<top>" and top_on and pl > max(top_on)]
        real_covered = bool(top_on) and not top_off
        dropped_by_scope = real_covered and not after_pf
        writer = writer_override if writer_override is not None else ln[: m.start()].strip()
        # ★★★ 第三十一轮：算出「判据量 + 适用阈值」（★ 轴 6 拆成两个量）
        #   ⇒ ★ 中间段要先**屏蔽引号**再找管道（★ 否则 `cut -d'|'` 会被误判）。
        middle = middle_stage_of(ln, m.start())
        # ★★ 第三十一轮抓到的**第三个量具真缺陷**（★ 本轮自己撞出来的）：
        #   ★ **跨行管道**（`\` 续行）时，**写端侧在上一行** ⇒ 本行的写端文本是**空的**
        #   ⇒ ⇒ **判据量根本看不出来** ⇒ ★ 绝不能**猜**成「写端总量」
        #     （★ 猜错的代价 = 把 PIPE_BUF 档误写成管道容量档 ⇒ 阈值错 16 倍）。
        #   ⇒ ⇒ 这里老实标成「未知」，★ 由**门**要求这类行必须人工判读并写明。
        invisible_writer = False
        if not writer and i >= 2 and lines[i - 2].rstrip().endswith("\\"):
            invisible_writer = True
        if invisible:
            invisible_writer = True
        # ★★ 2026-11-03 第二十九轮：usage 轴要看到「管道之后」与「续行」里的
        #   `||`/`&&` ⇒ ⇒ 传入**消费者尾部**与**下一物理行**。
        nxt = lines[i] if i < len(lines) else ""
        usage, basis = classify_usage(ln, m.start(), ln[m.end():], nxt)
        return Site(path=rel, line=i, scope=sc, consumer=m.group("c").strip(),
                    kind=kind, writer=writer, pf_line=pf_line,
                    pf_scope=sc, after_pf=after_pf, usage=usage,
                    usage_basis=basis, volume=writer_volume_hint(writer),
                    full_line=ln.strip(), middle=middle,
                    criterion=(CRIT_UNKNOWN if invisible_writer
                               else criterion_of(kind, middle, writer)),
                    dropped_by_scope=dropped_by_scope)

    for i, ln in enumerate(lines, 1):
        for m in RE_PIPE_CONSUMER.finditer(ln):
            early, kind = consumer_kind(m.group("c"))
            if not early:
                continue
            sites.append(mk_site(i, ln, m, kind))

    # ★★★ 第三十二轮：补上「形态 A」的**检测盲区**
    #   ★ 形态 A = ★ **管道在上一行末尾**（且带续行反斜杠）+ ★ **消费者在下一行开头**
    #     + ★ **消费者自己那一行没有管道符号** ⇒ ⇒ 物理行扫描**完全看不见**这条管道。
    #   ★ ★ 实测（§28.19.8 登记项 ③）：★ 全仓 **1 处**
    #     （`scripts/ab-test-snapshot.sh:32`：`ssh … | \` ⏎ `grep -E … | \` ⏎ `head -50`）
    #   ★ ★★ 而那 1 处**在函数内** ⇒ ★ 作用域模型（顶层 pipefail ≠ 函数作用域）会丢弃它
    #     ⇒ ⇒ **候选集零影响**（★ ★★ 不是靠推理定的，是靠「改完重跑、读数不变」定的）。
    #   ⇒ ★ 所以本条修正的价值是：★ **给未来的同形态代码补上探测能力**，而不是修历史债。
    for i, ln in enumerate(lines, 1):
        if i < 2:
            continue
        prev = lines[i - 2].rstrip()
        if not prev.endswith("\\"):
            continue
        prev = prev[:-1].rstrip()
        if not RE_SINGLE_PIPE_END.search(prev):
            continue
        if RE_SINGLE_PIPE.search(ln):
            continue          # ★ 消费者自己那行有管道 ⇒ 形态 B ⇒ 第一轮已扫到
        m2 = RE_CONSUMER_AT_START.search(ln)
        if not m2:
            continue
        early, kind = consumer_kind(m2.group("c"))
        if not early:
            continue
        sites.append(mk_site(i, ln, m2, kind, writer_override=prev, invisible=True))
    return sites, (len(pf_on), len(pf_off))


def collect_scripts(root, skip_prefixes):
    files = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for fn in filenames:
            p = os.path.join(dirpath, fn)
            rel = os.path.relpath(p, root)
            if rel.startswith("./"):
                rel = rel[2:]
            if any(rel.startswith(pre) for pre in skip_prefixes):
                continue
            if fn.endswith(".sh"):
                files.append((p, rel))
                continue
            # shebang 口径：无后缀但首行是 shell
            try:
                with open(p, "r", encoding="utf-8", errors="replace") as f:
                    first = f.readline()
            except OSError:
                continue
            if re.match(r"^#!.*\b(bash|sh|zsh|dash|ksh)\b", first):
                files.append((p, rel))
    return files


# ── 量具自检：注入夹具，扫描器必须命中 ─────────────────────────────────────
FIXTURES = {
    # (文件名, 期望命中数, 期望 axis1_enabled)
    "fx_set_o.sh": (1, True),          # set -o pipefail
    "fx_set_euo.sh": (1, True),        # set -euo pipefail   ← 内嵌 -o
    "fx_set_uo.sh": (1, True),         # set -uo pipefail    ← 内嵌 -o
    "fx_set_plus_o.sh": (0, False),    # set +o pipefail    ← 关闭
    "fx_comment_only.sh": (0, False),  # 注释里提到         ← 剥注释
    "fx_no_pipefail.sh": (0, False),   # 没有 pipefail
    "fx_after_disable.sh": (0, False), # 启用后又关闭
    # ★★★ 第三十二轮：**形态 A**（★ 管道在上一行末尾、消费者在下一行开头、
    #   且消费者自己那一行**没有管道符号** ⇒ 物理行扫描看不见它）
    #   ⇒ ★ 这条夹具是「形态 A 修复」的**牙齿**：★ 删掉那段检测 ⇒ 自检命中 0 ≠ 1
    #   ⇒ ⇒ ★ 自检失败 ⇒ exit 3 ⇒ **门红** ⇒ ⇒ ★ 这条路径从此**可观测、可变异守卫**
    "fx_continuation_pipe.sh": (1, True),
}


def selfcheck():
    """扫描器自检：★ 扫描了个寂寞的检验。失败 ⇒ exit 3 ⇒ 拒绝出读数。"""
    import tempfile
    tmp = tempfile.mkdtemp(prefix="r97selfcheck")
    for name in FIXTURES:
        with open(os.path.join(tmp, name), "w", encoding="utf-8") as f:
            f.write("#!/usr/bin/env bash\n")
            if name == "fx_set_o.sh":
                f.write("set -o pipefail\nprintf 'x\\n' | grep -q x\n")
            elif name == "fx_set_euo.sh":
                f.write("set -euo pipefail\nprintf 'x\\n' | grep -q x\n")
            elif name == "fx_set_uo.sh":
                f.write("set -uo pipefail\nprintf 'x\\n' | grep -q x\n")
            elif name == "fx_set_plus_o.sh":
                f.write("set +o pipefail\nprintf 'x\\n' | grep -q x\n")
            elif name == "fx_comment_only.sh":
                f.write("# set -o pipefail —— 只是注释\nprintf 'x\\n' | grep -q x\n")
            elif name == "fx_no_pipefail.sh":
                f.write("set -eu\nprintf 'x\\n' | grep -q x\n")
            elif name == "fx_after_disable.sh":
                f.write("set -o pipefail\nset +o pipefail\nprintf 'x\\n' | grep -q x\n")
            elif name == "fx_continuation_pipe.sh":
                # ★ 形态 A：★ 管道在上一行末尾（带续行反斜杠）、消费者在下一行开头、
                #   且**消费者自己那一行没有管道符号** ⇒ ★ 物理行扫描看不见这条管道
                f.write("set -o pipefail\nprintf 'x\\n' | \\\n  grep -q x\n")
    ok = True
    for name, (exp_hits, exp_on) in FIXTURES.items():
        sites, pf = scan_file(os.path.join(tmp, name), name)
        hits = [s for s in sites if s.after_pf]
        got = len(hits)
        if got != exp_hits:
            ok = False
            print("  ✗ 自检 %-22s 期望命中 %d，实际 %d" % (name, exp_hits, got))
        else:
            print("  ✓ 自检 %-22s 命中 %d" % (name, got))
    # 清理（★ 脚本内 rm 要静默，避免污染 stdout）
    import shutil
    shutil.rmtree(tmp, ignore_errors=True)
    return ok


def determinism_check(sites, root, skip_prefixes):
    """确定性自检：同一输入跑两遍，输出指纹必须一致。

    ★ 口径（有意收窄，公开写明）：第二遍**只重扫候选集涉及的文件**
      （而不是全仓 798 个文件）⇒ 覆盖「候选读数可复现」，
      但**不覆盖**「非候选文件在两遍之间的漂移」。
      ⇒ 收窄的理由：全量三遍扫描要 50 s，而门每次都要跑它 ⇒ CI 成本不可接受。
      ★ 收窄本身是**取舍**，不是等价替换 ⇒ 台账与文档里都要写明。
    """
    def digest(rows):
        return hashlib.md5("\n".join(sorted(rows)).encode("utf-8")).hexdigest()

    def cand_rows(ss):
        # ★ 两遍**同口径**：都只取候选行（after_pf=True）⇒ 指纹才有可比性
        return ["%s\t%d\t%s\t%s\t%s\t%s" % (s.path, s.line, s.scope, s.kind,
                                             s.usage, s.volume)
                for s in ss if s.after_pf]
    d1 = digest(cand_rows(sites))
    # 第二遍：只重扫候选集涉及的文件
    cand_files = sorted(set(s.path for s in sites if s.after_pf))
    sites2 = []
    for p, rel in collect_scripts(root, skip_prefixes):
        if rel not in cand_files:
            continue
        s, _ = scan_file(p, rel)
        if s:
            sites2.extend(s)
    d2 = digest(cand_rows(sites2))
    return d1, d2


# ── 台账（逐条裁决的 SSOT）────────────────────────────────────────────────
# ★ 台账列（13 列）★ 末列是 fingerprint，倒数第三是 occ ⇒ 主键 = (file, fingerprint, occ)
LEDGER_COLS = ("file", "line", "occ", "scope", "kind", "usage", "volume",
               "writer", "verdict", "reason", "evidence", "owner", "fingerprint")
LEDGER_HEADER = "\t".join(LEDGER_COLS)
# ★ 旧台账布局（12 列，**无 occ 列**）：
#   file,line,scope,kind,usage,volume,writer,verdict,reason,evidence,owner,fingerprint
#   ⇒ occ 由旧行**现算**（同形组内按行号排序）⇒ 与新算法同源
#   ⇒ 旧裁决可**零改动**迁移到新键方案（不需要人工重裁）。
LEDGER_OLD_FIELDS = ("file", "line", "scope", "kind", "usage", "volume",
                     "writer", "verdict", "reason", "evidence", "owner",
                     "fingerprint")
# ★ 候选集 TSV 列（13 列）★ **物理行**粒度（与台账同粒度 ⇒ 键集可比）
# ★★★ 第三十一轮：**末尾追加** `criterion` 列（★ 判据量 + 适用阈值）
#   ⇒ ★★ **追加在末尾**（而不是插在中间）⇒ ★ **fingerprint 仍在 cols[11]**
#     ⇒ ⇒ ★ 门与裁决脚本的**列索引零漂移**（★ 台账也**不需要**改列数
#     ⇒ ⇒ ⇒ ★ **台账是人工裁决的 SSOT ⇒ 不做 schema 迁移**（★ 迁移风险 ≫ 收益）
#   ⇒ ★ 台账侧对应的「分列两个量」写在每行的 reason/evidence 里（★ 由门逐行核对）。
CAND_COLS = ("file", "line", "occ", "scope", "kind", "usage", "volume",
             "consumer", "writer", "pf_line", "full_line", "fingerprint",
             "criterion")
CAND_HEADER = "\t".join(CAND_COLS)
# ★ 末列是 fingerprint ⇒ 前面各列的索引不变（门与裁决脚本都依赖这一点）
VALID_VERDICTS = {
    "benign-bounded",   # 写端输出有界（读码/实测确认远低于翻转边界）⇒ 不进入概率翻转区
    "benign-if-bounded",  # ★ 条件性 benign：写端体量是运行时事实（如变量写端）
    "benign-value",     # 取值用：退出码被命令替换丢弃
    "benign-bare",      # 裸管道：退出码不参与语义判断（`set -e` 下最多意外中止）
    "risk-deterministic",  # 实测确认：确定性翻转（消费者「成功」被表达成 141）
    "risk-race",        # 写端进入概率斜坡 ⇒ 可能翻转
    "conditional",      # 当前无害，但形态一变即翻转
    "pending",          # ★ 待人工裁决（门不接受 pending ⇒ 门会红）
}
RE_HEAD_C = re.compile(r"head\s+-c")

# ★★★ **占位证据串**（R98 轮新增）：自动初稿里这两个串是「等人工/实测」的占位，
#   **不是依据** ⇒ ⇒ 补齐零证据时**不能**拿它们当证据填进去
#   ⇒⇒ 否则会出现「证据非空但内容是『待裁决』」的假通过。
R97_PLACEHOLDER_EVIDENCE = ("待裁决", "待实测写端体量")


def auto_verdict(site):
    """自动初稿。★ 只对「读码/实测可机械判定」的两桶给终值，其余一律 pending。

    ★ 返回 (verdict, reason, evidence) —— evidence **不许留空**：
      本轮曾出现过一整批 evidence 为空的行（根因是量具判据恒真），
      而「证据非空」现在是门的一条断言 ⇒ 门会当场抓。

    ⇒ pending 的存在是**故意的**：静态判据只能给候选集（§19.20.54 ⑥ 2），
      最终裁决必须逐条人工/实测做，并把证据写进台账。
    """
    if site.usage == "value":
        return ("benign-value", "取值用：管道退出码被命令替换丢弃",
                "用途判据（与写端体量无关）")
    if site.usage == "bare":
        return ("benign-bare", "裸管道：退出码不参与语义判断",
                "用途判据（与写端体量无关）")
    # judgment
    if RE_HEAD_C.search(site.consumer):
        return ("pending",
                "消费者只读极少字节（head -c）⇒ 写端必被 SIGPIPE 杀 ⇒ "
                "消费者「成功(0)」会被 pipefail 表达成 141 ⇒ 需实测确认写端体量",
                "待实测写端体量")
    if site.volume == "bounded":
        return ("benign-bounded",
                "写端是短输出命令（读码/实测确认）⇒ 实测量级远低于翻转边界 ⇒ "
                "不进入概率翻转区",
                "写端是短输出命令族（docker ps --format / psql -At / redis-cli ping / "
                "pg_isready / launchctl list / crontab -l / git ls-files / "
                "curl -f -s / systemctl list-unit-files / go list -deps）⇒ "
                "实测样例 ≤ 6,032 B ≪ 管道容量 64 KiB"
                "（★ 平台相关：linux/debian:bookworm-slim 与 macOS/darwin25.6.0 "
                "实测同量级；★ 翻转是概率型 ⇒ 只能说「实测边界内不翻转」）")
    return ("pending", "写端体量未知 ⇒ 需读码/实测裁决", "待裁决")


def fingerprint(site):
    """★ 形态指纹 = 裁决真正依据的那几个量（不含行号）。

    ★★ 为什么不用行号做键：**上游改一行代码，台账里那一行的行号就漂了**
      ⇒ 行号键 ⇒ 台账「缺行/多行」⇒ 门红 ⇒ 而裁决其实仍然成立
      ⇒ ⇒ 键必须绑【形态】⇒ 行号只作定位。
    """
    raw = "%s|%s|%s|%s" % (site.kind, site.usage, site.volume,
                           "|".join(sorted(x.consumer.strip() for x in
                                          [site] if x.consumer)))
    return hashlib.md5(raw.encode("utf-8")).hexdigest()[:12]


def group_sites(cand):
    """把候选（消费者处）聚成**物理行**组。

    ★ 分母口径：**候选集按「消费者处」计，台账按「物理行」计**。
      一个物理行可能有多个早退消费者
      （`gunzip -c f | head -c 1 | grep -q x` ⇒ head + grep-q 两个消费者）
      ⇒ **两侧粒度必须一致才能比对** ⇒ 候选集 TSV 也按物理行输出
      ⇒ 否则键集天然不可比（★ 本轮实测：候选 361 处 vs 台账 352 行）。
    """
    g = {}
    for s in cand:
        g.setdefault((s.path, str(s.line)), []).append(s)
    out = []
    for key in sorted(g, key=lambda k: (k[0], int(k[1]))):
        # ★★ **不要重排组内顺序**：fingerprint 取组内**第一个** site 的 consumer
        #   （即行内最左的那个早退消费者）⇒ 重排会改掉指纹 ⇒ 裁决被误判成「口径漂移」。
        #   ⇒ 实测踩过：给组内按 consumer 字典序排序 ⇒ 3 行指纹变了
        #   ⇒ 含那条 risk-deterministic ⇒ 裁决被作废回 pending。
        #   ⇒ 顺序必须保持「行内左→右」（RE_PIPE_CONSUMER.finditer 的顺序，稳定且确定）。
        out.append((key, g[key]))
    return out


def assign_occ(groups):
    """给每个物理行组分配 occ = 同 (file, fingerprint) 组内按行号排序的序号（从 1 起）。

    ★★ 为什么必须要有 occ（★ **本轮实测踩出来的真缺陷**）：
      形态指纹**在同一文件内不唯一** —— 实测 352 行只有 281 个唯一指纹，
      41 个 (file, fingerprint) 组含 >1 行、合计 71 行；
      另有 8 组连**源码文本都完全相同** ⇒ `full_line` 也不能当区分键。
      ⇒ (file, fingerprint) **不是主键** ⇒ 拿它当 carry-over 键 ⇒
        `old[(file,fp)] = cols` **后写覆盖先写** ⇒ 同形行互相冒用裁决
        ⇒ **真缺陷的 risk 裁决可能被邻行的 benign 裁决洗掉**（本轮核心不变量被破）。
      ⇒ 第三维 occ（同形组内的顺序位）把它变成真主键：
        唯一 ✓ / 抗行号漂移 ✓（顺序位不随行号变）/ 顺序稳定 ✓。
    """
    byfp = {}
    for key, sites in groups:
        fp = fingerprint(sites[0])
        byfp.setdefault((key[0], fp), []).append((int(key[1]), key))
    occ = {}
    for lst in byfp.values():
        lst.sort()
        for i, (ln, key) in enumerate(lst):
            occ[key] = i + 1
    return occ


def owner_of(path):
    """粗粒度归属：顶层目录 ⇒ 用于「谁该处置」的分派。"""
    top = path.split("/", 1)[0]
    return top + "/" if "/" in path else top


class LedgerBleed(Exception):
    """★ 裁决串味：同形组内旧裁决的多样性在重建后丢失 ⇒ 台账不可信 ⇒ 拒绝写出。"""

    def __init__(self, errors):
        super(LedgerBleed, self).__init__("裁决串味：%d 处" % len(errors))
        self.errors = errors


class LedgerShape(Exception):
    """★ 台账**形状**坏了：某行的列数与**表头**不符。

    ★★ 为什么必须拒写（★ 2026-11-03 第三十轮实测）：
      `read_ledger` 用 `ln.split("\t")` 切列 ⇒ ★ **列数多出来的行**（reason/evidence 里
      手写了一个 tab）⇒ ⇒ `zip(LEDGER_COLS, cols)` 会**静默丢掉多出来的列**
      ⇒ ⇒ ★★ **那一行的 verdict/reason/evidence 整体错位** ⇒ ⇒ **台账被静默写坏**；
      ★★ 而**列数不够**的行 ⇒ ⇒ 被 `continue` **静默丢弃** ⇒ ⇒ **那一行的人工裁决凭空消失**。
      ⇒ ⇒ ★ 两者都**不报错** ⇒ ⇒ ★ 「台账读不出错」不等于「台账是对的」
      ⇒ ⇒ ★ 所以这里**按表头列数严格校验**，形状不对 ⇒ **拒绝写出**（exit 3）。

    ★ ★ 与 `LedgerBleed` 是**同族但不同轴**：那一条管「裁决串味（语义）」，
      这一条管「形状（结构）」⇒ ⇒ ★ 两边都过 ⇒ 台账才可信。
    """

    def __init__(self, errors):
        super(LedgerShape, self).__init__("台账形状异常：%d 处" % len(errors))
        self.errors = errors


def _int(s, default=0):
    try:
        return int(str(s).strip())
    except (TypeError, ValueError):
        return default


def read_ledger(path):
    """读旧台账 ⇒ 三个索引。

    ★ 返回 (by_key, by_fp, by_line)
      by_key[(file, fp, occ)] = row    ← 主键索引
      by_fp[(file, fp)] = [row…]（按行号升序）
      by_line[(file, line)] = row

    ★ occ 一律**现算**（同形组内按行号排序）⇒ 与新算法同源
      ⇒ 旧台账（无 occ 列）**零改动**迁移；occ 是派生列，手工改它没有意义。
    """
    by_key, by_fp, by_line = {}, {}, {}
    if not path or not os.path.exists(path):
        return by_key, by_fp, by_line
    rows = []
    # ★★ 第三十轮新增：**按【表头】的列数严格校验每一行**（★ 见 LedgerShape 的说明）
    shape_errors = []
    expected_cols = None
    with open(path, "r", encoding="utf-8") as f:
        for lineno, ln in enumerate(f, 1):
            ln = ln.rstrip("\n")
            if not ln.strip() or ln.startswith("#"):
                continue
            cols = ln.split("\t")
            if cols[0] == "file":
                expected_cols = len(cols)
                continue
            if expected_cols is not None and len(cols) != expected_cols:
                shape_errors.append(
                    "★ %s:%d 的列数 = %d，表头是 %d 列 ⇒ %s"
                    % (path, lineno, len(cols), expected_cols,
                       ("多出来的 tab 会让字段**静默错位** ⇒ 裁决被改坏"
                        if len(cols) > expected_cols
                        else "列数不足 ⇒ 该行会被**静默丢弃** ⇒ 人工裁决凭空消失")))
                continue
            if len(cols) >= len(LEDGER_COLS):
                fields = dict(zip(LEDGER_COLS, cols))
            elif len(cols) >= len(LEDGER_OLD_FIELDS):
                fields = dict(zip(LEDGER_OLD_FIELDS, cols))
                fields["occ"] = ""
            else:
                continue
            if not fields.get("fingerprint", "").strip():
                continue
            rows.append(fields)
    if shape_errors:
        raise LedgerShape(shape_errors)
    grp = {}
    for r in rows:
        grp.setdefault((r["file"], r["fingerprint"]), []).append(r)
    for lst in grp.values():
        lst.sort(key=lambda r: _int(r["line"]))
        for i, r in enumerate(lst):
            r["occ"] = i + 1
    for r in rows:
        by_key[(r["file"], r["fingerprint"], r["occ"])] = r
        by_fp.setdefault((r["file"], r["fingerprint"]), []).append(r)
        by_line[(r["file"], r["line"])] = r
    return by_key, by_fp, by_line


def build_ledger(cand, ledger_path, groups=None, occ=None):
    """生成/增量更新台账。★ 已有的人工裁决**必须保留**（人工裁决是 SSOT）。

    ★ 台账按**物理行**计（一行可能有多个早退消费者 ⇒ 两个候选处、但一个裁决单元）。
    ★ 两个分母分别发布：候选处数（消费者处）/ 台账行数（物理行）。

    ★★★ **carry-over 的六步匹配级联**（★ 本轮实测踩出来的真缺陷的修法）：
      0. ★★ **前置闸（R98 轮修正）**：**同形组行数变了 ⇒ occ 锚失效**
         （occ 是位置序号 ⇒ 组内插删一行 ⇒ 其余成员 occ 全部位移）
         ⇒ ⇒ 组大小**稳定**才允许第 1 步；否则只信**行号锚**（第 2 步）。
      1. 主键精确命中 (file, fp, occ)              ⇒ 直接沿用；
      2. 同 (file, fp) 组内**行号未变**            ⇒ 沿用（行号是更强的锚）；
      3. 同 (file, fp) 组内**组内行数未变** ⇒ 按序重锚 ⇒ 沿用（附注「顺序重锚」）；
      4. 同 (file, fp) 组但**组内行数变了**        ⇒ **pending**（不许自动重锚）；
      5. 同 (file, line) 有旧行但**指纹变了**      ⇒ **口径漂移 ⇒ pending**
         （★ 不许自动重判 ⇒ 尤其不许把 risk 降级；原 evidence 保留）；
      6. 其它                                      ⇒ 新增（自动初稿）。

    ★★ 第 1 步之所以要 occ：形态指纹**同文件内不唯一** ⇒ 拿 (file, fp) 当键
      ⇒ `old[(file,fp)] = cols` 后写覆盖先写 ⇒ **同形行互相冒用裁决**
      ⇒ 真缺陷的 risk 裁决可能被邻行的 benign 裁决洗掉 ⇒ 本轮核心不变量被破。

    ★★★★ **R98 轮的实测订正（★ 这段曾经是「写在纸上、没落在代码里」）**：
      第 4 步原先写在 `if old is not None:` 的 `elif` 里 ⇒ **不可达**
      （该分支里 `old` 恒为 None）⇒ ⇒ 「不许自动重锚」这条约束**从未生效**
      ⇒ 实测：该类行走「新增 ⇒ 自动初稿」⇒ **人工裁决被自动初稿的终值顶替**
      ⇒ 且 `stats["shift"]` 恒 0 ⇒ **量具自己都报不出这件事**。
      ⇒ ⇒ 本轮把第 4 步提到顶层（`status == "shift"`）⇒ 并加了第 0 步前置闸
      ⇒ ⇒ 修完实测：真实台账**只有 evidence 列变 34 行、verdict 变 0 行**
      ⇒⇒ （见 handoff §19.20.68 —— ★ 节号是活事实，落笔时当场查过一次。）
    """
    if groups is None:
        groups = group_sites(cand)
    if occ is None:
        occ = assign_occ(groups)
    old_by_key, old_by_fp, old_by_line = read_ledger(ledger_path)
    cur_cnt = {}
    for key, sites in groups:
        k = (key[0], fingerprint(sites[0]))
        cur_cnt[k] = cur_cnt.get(k, 0) + 1

    rows = []
    stats = {"kept": 0, "new": 0, "invalidated": 0, "reanchored": 0,
             "shift": 0, "drift": 0}
    used_old = set()          # ★ 防裁决串味：一条旧行最多被沿用一次
    group_new = {}            # (file,fp) ⇒ [(verdict, invalidated)]
    for key, sites in groups:
        path, line_s = key[0], key[1]
        line = _int(line_s)
        s0 = sites[0]
        kinds = sorted(set(x.kind for x in sites))
        cons = sorted(set(x.consumer.strip() for x in sites))
        fp = fingerprint(s0)
        o = occ[key]
        usage, volume = s0.usage, s0.volume
        av, ar, ae = auto_verdict(s0)

        old = None
        status = "new"
        note = ""
        near = None
        grp = old_by_fp.get((path, fp), [])
        # ★★ **组大小是否稳定 = 「occ 还值不值得信」**（R98 轮修正，★ 见下方说明）。
        #
        # ★ occ 是**位置序号** ⇒ 同形组里插入/删除一行 ⇒ 其余成员 occ **全部位移**
        #   ⇒ ⇒ 组大小变了 ⇒ occ 锚失效 ⇒ **不许再按 occ 命中**（否则会串味：
        #   新行会冒用旧行的裁决，而旧行的裁决会被顶到别的行上）。
        # ⇒ ⇒ 所以：**组大小稳定才允许 exact（occ）命中**；否则只信**行号锚**。
        size_stable = (len(grp) == cur_cnt[(path, fp)])
        if size_stable and (path, fp, o) in old_by_key:
            old, status = old_by_key[(path, fp, o)], "exact"
        elif grp:
            same_line = [r for r in grp if r["line"] == line_s]
            if len(same_line) == 1:
                # ★ 行号锚 = 最强的身份证据（这行还在、形态也没变 ⇒ 裁决依据未变）
                old, status = same_line[0], "line"
            elif size_stable:
                old, status = grp[o - 1], "order"
                note = "（顺序重锚：同形组行数未变）"
            else:
                status = "shift"
                near = min(grp, key=lambda r: abs(_int(r["line"]) - line))
        else:
            prev = old_by_line.get((path, line_s))
            if prev is not None:
                old, status = prev, "drift"

        bleed = False
        if old is not None and id(old) in used_old:
            bleed = True
            old = None
            status = "bleed"

        v, reason, evid, own = av, ar, ae, owner_of(path)
        invalidated = False
        if status == "shift":
            # ★★★ **同形组行数变化 ⇒ occ 位移 ⇒ 无法安全重锚 ⇒ 回 pending**
            #   （★ R98 轮实测发现：这段原先写在 `if old is not None:` 里 ⇒ **不可达**
            #    —— 该分支里 `old` 恒为 None ⇒ ⇒ 「不许自动重锚」这条约束**从未落地**
            #    ⇒ 该类行走「新增 ⇒ 自动初稿」⇒ 人工裁决被自动初稿的终值顶替
            #    ⇒（可把 risk 静默降级成 benign）⇒ 且 stats["shift"] 恒 0 ⇒ 无任何信号。）
            src = near if near is not None else None
            v = "pending"
            reason = ("★ 同形组行数变化 ⇒ occ 位移 ⇒ **无法安全重锚** ⇒ 需人工重裁"
                      "（原证据保留供重裁参考）")
            evid = src["evidence"] if src is not None else "待裁决"
            own = src["owner"] if src is not None else owner_of(path)
            stats["invalidated"] += 1
            stats["shift"] += 1
            invalidated = True
        elif old is not None:
            drift = []
            if old["kind"] != "+".join(kinds):
                drift.append("kind %s→%s" % (old["kind"], "+".join(kinds)))
            if old["usage"] != usage:
                drift.append("usage %s→%s" % (old["usage"], usage))
            if old["volume"] != volume:
                drift.append("volume %s→%s" % (old["volume"], volume))
            if status == "drift":
                drift.append("fingerprint %s→%s" % (old["fingerprint"], fp))
            if drift:
                # ★★ **作废后不许自动重判** —— 尤其**不许把 risk/conditional 降级**。
                was_risk = (old["verdict"].startswith("risk-")
                            or old["verdict"] == "conditional")
                v = "pending"
                reason = ("★ 口径漂移（原裁决作废：" + "，".join(drift) + "）"
                          + ("；原裁决是 %s ⇒ **必须人工重裁，不许自动重判**"
                             % old["verdict"] if was_risk else "；请重新裁决"))
                evid = old["evidence"]
                own = old["owner"]
                stats["invalidated"] += 1
                stats["drift"] += 1
                invalidated = True
            else:
                v = old["verdict"]
                reason = old["reason"] + note
                evid = old["evidence"]
                # ★★ **零证据补齐**（R98 轮修正）：旧行证据为空 ⇒ 用**该行的机械判据**补上；
                #   ★ 只在自动初稿给的是**真依据**时补（排除两个占位串）
                #   ⇒ 真实台账里有 34 行「零证据的终值」（R97 之前的旧行）⇒ 门抓不到
                #   ⇒ 补齐后门才能对**全表**要求证据非空。
                if not evid.strip() and ae not in R97_PLACEHOLDER_EVIDENCE:
                    evid = ae
                own = old["owner"]
                stats["kept"] += 1
                if status in ("line", "order"):
                    stats["reanchored"] += 1
                used_old.add(id(old))
        else:
            stats["new"] += 1
            if bleed or status == "bleed":
                v = "pending"
                reason = "★ 裁决串味自检失败 ⇒ 拒绝自动裁决 ⇒ 待人工裁决"
                evid = "待裁决"
        group_new.setdefault((path, fp), []).append((v, invalidated))
        rows.append("\t".join([
            path, line_s, str(o), s0.scope, "+".join(kinds), usage, volume,
            s0.writer.replace("\t", " ")[-80:], v, reason,
            evid.replace("\t", " "), own, fp]))

    # ★★ **裁决串味自检**：同形组内「旧裁决的多样性」不得在重建后丢失。
    #   （旧实现用 (file,fp) 当 carry-over 键 ⇒ 后写覆盖先写 ⇒ 同形行互相冒用裁决
    #     ⇒ 真缺陷的 risk 裁决可能被 benign 洗掉 ⇒ 本自检就是那道闸。）
    bleed_errors = []
    for (path, fp), oldgrp in old_by_fp.items():
        old_v = set(r["verdict"] for r in oldgrp if r["verdict"] != "pending")
        if len(old_v) <= 1:
            continue
        news = group_new.get((path, fp), [])
        if any(inv for _, inv in news):
            continue          # 有意作废（口径漂移）⇒ 不是串味
        new_v = set(v for v, _ in news if v != "pending")
        if new_v != old_v:
            bleed_errors.append("%s fp=%s 旧裁决集合 %s ⇒ 新 %s"
                                % (path, fp, sorted(old_v), sorted(new_v)))
    if bleed_errors:
        raise LedgerBleed(bleed_errors)

    # 上游已消失的**物理行**（按 (file,line) 判，不按 occ ⇒ 免受 occ 位移影响）
    cur_lines = set((k[0], k[1]) for k, _ in groups)
    removed = sorted(set((r["file"], r["line"]) for r in
                         sum(old_by_fp.values(), []) if (r["file"], r["line"]) not in cur_lines))
    return rows, stats, removed


def main():
    ap = argparse.ArgumentParser(description="SIGPIPE 竞态面候选集普查（静态）")
    ap.add_argument("--root", default=".")
    ap.add_argument("--out", default="-", help="候选集 TSV 输出路径（- = stdout）")
    ap.add_argument("--skip-prefix", action="append", default=[],
                    help="要跳过的路径前缀（可多次）")
    ap.add_argument("--no-selfcheck", action="store_true",
                    help="★ 跳过量具自检（仅供调试；正式读数不得使用）")
    ap.add_argument("--ledger", default="",
                    help="台账 TSV 路径：生成/增量更新（已有的人工裁决会保留）")
    args = ap.parse_args()

    root = os.path.abspath(args.root)
    skip = tuple(args.skip_prefix) if args.skip_prefix else DEFAULT_SKIP_PREFIXES

    if not args.no_selfcheck:
        print("== 量具自检（读数之前）==")
        if not selfcheck():
            print("✗ 量具自检失败 ⇒ 分母不可信 ⇒ **拒绝出读数**")
            return 3
        print("  ✓ 自检全过")

    files = collect_scripts(root, skip)
    all_sites = []
    pf_files = []
    for p, rel in files:
        sites, pf = scan_file(p, rel)
        if not sites:
            continue
        if pf and pf[0] > 0:
            pf_files.append(rel)
        all_sites.extend(sites)

    # 候选集 = 轴 3（时序成立）∧ 轴 4（消费者早退）—— 轴 1 已由 after_pf 蕴含
    cand = [s for s in all_sites if s.after_pf]
    cand.sort(key=lambda s: (s.path, s.line))

    # ★ 物理行分组（台账粒度）⇒ 候选集 TSV 也按它输出 ⇒ **两侧键集可比**
    groups = group_sites(cand)
    occ = assign_occ(groups)
    # ★ 键唯一性自检：主键 = (file, fingerprint, occ) 必须**真的**唯一
    #   （★ 本轮实测：形态指纹同文件内不唯一 ⇒ 没有 occ 时键会撞 ⇒ 裁决串味）
    keys = [(k[0], fingerprint(s[0]), occ[k]) for k, s in groups]
    key_unique = (len(set(keys)) == len(keys))

    # 恒等式自检：分桶之和 == 候选总数
    buckets = {}
    for s in cand:
        buckets[s.kind] = buckets.get(s.kind, 0) + 1
    by_usage = {}
    for s in cand:
        by_usage[s.usage] = by_usage.get(s.usage, 0) + 1
    ident = (sum(buckets.values()) == len(cand)
             and sum(by_usage.values()) == len(cand))
    # ★ 最值得人看的一格：退出码作判据 ∧ 写端可能无界
    hot = [s for s in cand if s.usage == "judgment" and s.volume == "unbounded"]

    # 确定性自检
    d1, d2 = determinism_check(all_sites, root, skip)

    print("")
    print("== 口径 ==")
    print("  仓库根          = %s" % root)
    print("  脚本集合        = .sh 后缀 ∪ shebang 判定")
    print("  排除路径前缀    = %s" % (", ".join(skip) or "(无)"))
    print("  扫描脚本数      = %d" % len(files))
    print("  启用 pipefail 的脚本数 = %d" % len(pf_files))
    print("")
    print("== 候选集（轴3 ∧ 轴4）==")
    print("  候选处数（消费者处）= %d" % len(cand))
    print("  物理行数（裁决单元）= %d" % len(groups))
    print("  涉及文件数 = %d" % len(set(s.path for s in cand)))
    for k in sorted(buckets):
        print("    消费者 %-10s %d" % (k, buckets[k]))
    print("  ── 轴 7：管道退出码的用途 ──")
    for k in sorted(by_usage):
        print("    %-10s %d" % (k, by_usage[k]))
    print("  ★ 判据用 ∧ 写端可能无界 = %d 处 / %d 文件（最值得人看的一格）"
          % (len(hot), len(set(s.path for s in hot))))
    print("  恒等式（分桶之和 == 候选总数）= %s" % ("✓" if ident else "✗"))
    print("  主键唯一性（(file,fingerprint,occ) 无碰撞）= %s [%d 键 / %d 行]"
          % ("✓" if key_unique else "✗", len(set(keys)), len(keys)))
    print("  确定性（两遍指纹一致）= %s  [%s / %s]" % ("✓" if d1 == d2 else "✗", d1[:12], d2[:12]))
    if not ident or d1 != d2 or not key_unique:
        print("✗ 自检未过 ⇒ **拒绝出读数**")
        return 3

    print("")
    print("== 启用 pipefail 的脚本（轴 1）==")
    for f in sorted(pf_files):
        print("    %s" % f)

    # ★★ R103 轮：把「被【作用域模型】静默丢弃」的点**显式印出来**（★ 零判定语义变化）
    #   ★ 为什么要印：**误报会被看见、丢弃不会** ⇒ ⇒ 丢弃是比误报更危险的方向
    #   ★ 真实语义：pipefail 是**进程级**选项、函数体**继承**
    #   ★ 本量具口径：要求作用域**完全相同** ⇒ 函数体里的点不进候选集
    #   ⇒ ⇒ 这一段让「被丢弃的部分」从【不可见】变成【可见】⇒ 属主据此决定是否收紧口径
    #   ★ R103 轮补：collect_scripts **不去重** ⇒ 同一文件可能被收两次
    #     ⇒ 同一 (file,line) 会出现多次 ⇒ ★ 这里按 (file,line) 去重再计数
    #     （★ 只改【显示】，不动候选集/台账/判定）
    dropped_all = [s for s in all_sites if getattr(s, "dropped_by_scope", False)]
    dropped_map = {}
    for s in dropped_all:
        dropped_map.setdefault((s.path, s.line), s)
    dropped = list(dropped_map.values())
    #   ★ 量具自身探针**不在候选集**（实测 0 处）⇒ ⇒ 把它从主计数里**单列**
    #     ⇒ 【零判定影响】（候选集/台账不变）⇒ 且主计数干净
    SELF_PROBE = "probe-pipefail-sigpipe-shape.sh"
    dropped_self = [s for s in dropped if SELF_PROBE in s.path]
    dropped = [s for s in dropped if SELF_PROBE not in s.path]
    dropped_files = sorted(set(s.path for s in dropped))
    #   ★ 量具自身的探针：它的测试管道【故意】要制造 SIGPIPE ⇒ 命中被测形态是【设计如此】
    #     ⇒ ★ 这里只【标注】，不排除（★ 排除属口径变更 ⇒ 属主决策；且必须先确认
    #     它不在候选集里才谈得上「排除不影响判定」）
    print("")
    print("== ★★ 被【作用域模型】静默丢弃的管道点（真实语义下受顶层 pipefail 覆盖、"
          "但本量具按「同作用域」口径丢弃）==")
    print("   共 %d 处 / %d 个文件（★ 已按 (file,line) 去重；原始条目 %d 条）"
          % (len(dropped), len(dropped_files), len(dropped_all)))
    if dropped_self:
        print("   ★ 另单列：量具自身探针 %s 的 %d 处"
              % (SELF_PROBE, len(dropped_self)))
        print("     ⇒ ★ 它的测试管道【故意】要制造 SIGPIPE ⇒ 命中被测形态是【设计如此】")
        print("     ⇒ ★ 实测它**不在候选集**（0 处）⇒ ⇒ 【排除它零判定影响】")
        print("     ⇒ ★ 但「要不要把它作为待修项呈现」= 口径决策 ⇒ **属主决策**")
    print("   ⇒ ★ 丢弃是**静默**的（误报会被看见、丢弃不会）⇒ 这一段就是让它可见")
    print("   ⇒ ★ 是否收紧口径（顶层覆盖 + 函数体继承）⇒ **属主决策**；本轮**只让丢弃可见、不改判定**")
    for s in sorted(dropped, key=lambda x: (x.path, x.line))[:40]:
        tag = ("　← ★ 量具自身探针（命中是【设计如此】⇒ 是否排除 = 属主决策）"
               if SELF_PROBE in s.path else "")
        print("    %s:%d  scope=%s  kind=%s  usage=%s%s"
              % (s.path, s.line, s.scope, s.kind, s.usage, tag))
    if len(dropped) > 40:
        print("    …（其余 %d 处省略；全量见并行会话的 scope-drop 候选清单）"
              % (len(dropped) - 40))

    # ★ 候选集 TSV = **物理行**粒度（与台账同粒度 ⇒ 键集可比）
    #   列：file line occ scope kind usage volume consumer writer pf_line full_line fingerprint criterion
    lines = [CAND_HEADER]
    for key, sites in groups:
        s0 = sites[0]
        kinds = sorted(set(x.kind for x in sites))
        cons = sorted(set(x.consumer.strip() for x in sites))
        lines.append("%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s" % (
            key[0], key[1], occ[key], s0.scope, "+".join(kinds), s0.usage, s0.volume,
            "|".join(cons).replace("\t", " "),
            s0.writer.replace("\t", " ")[-100:], s0.pf_line,
            s0.full_line.replace("\t", " ")[:160], fingerprint(s0),
            s0.criterion))
    out = "\n".join(lines) + "\n"
    if args.out == "-":
        sys.stdout.write(out)
    else:
        with open(args.out, "w", encoding="utf-8") as f:
            f.write(out)
        print("  ⇒ 候选集写入 %s" % args.out)

    if args.ledger:
        try:
            rows, stats, removed = build_ledger(cand, args.ledger, groups, occ)
        except LedgerBleed as e:
            print("✗ ★ 裁决串味自检失败 ⇒ 台账不可信 ⇒ **拒绝写出**：")
            for x in e.errors[:20]:
                print("    %s" % x)
            return 3
        except LedgerShape as e:
            print("✗ ★ 台账**形状**自检失败 ⇒ 台账不可信 ⇒ **拒绝写出**：")
            for x in e.errors[:20]:
                print("    %s" % x)
            print("    ★ 典型成因：reason / evidence 里手写了一个 **tab**（或整行被截断）")
            print("    ★ 修法：把那行里的 tab 换掉（或删掉多余列），再重跑本工具")
            return 3
        with open(args.ledger, "w", encoding="utf-8") as f:
            f.write("# SIGPIPE 竞态面 · 逐条裁决台账（由 scripts/audit/measure-pipefail-race-surface.py 生成）\n")
            f.write("# ★ 人工裁决（verdict/reason/evidence）是 SSOT；再次运行本工具会**保留**已有裁决。\n")
            f.write("# ★ verdict=pending 的行**必须**被人工裁决（门不接受 pending）。\n")
            f.write("# ★ 主键 = (file, fingerprint, occ)：fingerprint = 形态指纹（不含行号），"
                    "occ = 同形组内序号 ⇒ 唯一且抗行号漂移。\n")
            f.write(LEDGER_HEADER + "\n")
            for r in rows:
                f.write(r + "\n")
        pend = sum(1 for r in rows if "\tpending\t" in r)
        print("  ⇒ 台账写入 %s：共 %d 行（沿用人工裁决 %d / 其中重锚 %d / 新增 %d / "
              "因口径漂移或位移作废 %d / 待裁决 %d）"
              % (args.ledger, len(rows), stats["kept"], stats["reanchored"],
                 stats["new"], stats["invalidated"], pend))
        if removed:
            print("  ★ 上游已消失的物理行 %d 处（已从台账移除）：%s"
                  % (len(removed), ", ".join("%s:%s" % k for k in removed[:10])))
    return 0


if __name__ == "__main__":
    sys.exit(main())
