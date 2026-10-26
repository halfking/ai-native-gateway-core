#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""measure-pipefail-scope-drop.py —— 「作用域模型盲区」候选集的**生成器**（R99 轮 · 段十三 / §19.20.72）。

★ 它是什么：
  一个**只产出候选集**的静态普查器（与并行会话那条线的 `measure-pipefail-race-surface.py`
  同族但**不是同一个东西**）⇒ 它回答：
    「哪些 shell 管道处，处在『顶层 pipefail 实际覆盖 + 变量写端 → 早退消费者』的
      风险形状里，且**对方量具的轴 2 会把它们静默丢弃**？」

★ 为什么要它（三个必要条件，机制见同目录 `probe-pipefail-sigpipe-shape.sh`）：
  危害成立需【同时】满足：
    ① 写端输出【多行】（`grep` 按行匹配 ⇒ 必须读到行尾才敢判 ⇒ 单行时它读完整条
      ⇒ 写端也写完 ⇒ 不会被 SIGPIPE/EPIPE 打断 ⇒ 退出码 = grep 自己的 ⇒ 无危害）
    ② 写端超管道缓冲（64 KiB）
    ③ 命中靠前（命中在末尾 ⇒ grep 也要读到末尾 ⇒ 写端写完 ⇒ 无危害）
  ⇒ 消费者是 `head -c` 时跨形态都有害（段十 §19.20.66 那条真缺陷）。
  ⇒ ⇒★ **本清单是【候选集】，不是【缺陷集】** ⇒ 每个站点都要人工回读定性。

★★★★★ **【R85 轮 · §19.25.18 实测订正】条件②的精确表述（★ 判据一个字没改，只改表述）**：
  ★★ **「危害成立」的真正必要条件不是「写端超 64 KiB」，而是**
  **「消费者在写端写完之前退出」** ⇒ ★★ 而「超 64 KiB」是
  **「写端【确定】写不完」的【充分代理】** ⇒ ⇒ ★★ **64 KiB 是一个【概率穿越点】，
  不是一个【阈值】** ⇒ ⇒ ★★★ 实测依据（★ macOS/darwin-arm64，管道缓冲实测 65,536 B）：
    · 载荷 ≤ 49,146 B ⇒ 翻转率 **0%**；· ≥ 65,946 B ⇒ **100%**；· 中间平滑过渡。
    · ⇒ 载荷【≈ 缓冲】时，**写端的尾部写入与消费者的退出赛跑** ⇒ 两种结果都可能出现。
  ⇒ ⇒ ★★★ **对本【静态筛查器】而言：判据不变、结论不变** ⇒ ★ 它只需要【充分性】
    （「超 64 KiB ⇒ 几乎必然有害」⇒ 足以入选候选集）⇒ ⇒ ★ **只有【表述】必须精确**：
    ★ 读这个清单的人应当知道「候选」⇒ **几乎必然**真，**不是**「必然」真。
  ⇒ ⇒ ★★ 配套的门：`scripts/ursmcheck/section_1975_pipefail_harm_conditions_test.go`
    （★ 判据①已从【定值】改成【判定式】：稳定-0 / 稳定-141 / **竞态**三类 ⇒ 竞态类不钉定值）。

★ 口径（三条同时成立）：
  ① 该行命中「变量写端 → 单管道 → 早退消费者」形态（echo/printf + 变量展开 | grep -[qm]/head）
  ② **顶层**（`<top>` 作用域）`set -o pipefail` 在该行之前已启用且未被关闭
     ⇒ 真实 shell 语义下该行受 pipefail 覆盖（`set -o pipefail` 是进程级选项，函数体继承）
  ③ 对方量具 `measure-pipefail-race-surface.py` 的 `after_pf=False`
     ⇒ 它的轴 2 要求「启用点与管道点作用域完全相同」⇒ 函数体里的点被**静默丢弃**

★ ★★ **量具自身被显式排除**：`probe-pipefail-sigpipe-shape.sh` 的测试管道**故意**要制造
  SIGPIPE ⇒ 命中被测形态是【设计如此】⇒ 不排除的话，消费方会把它当成待修的缺陷。

★ 用法：
    python3 scripts/audit/measure-pipefail-scope-drop.py            # 写默认清单
    python3 scripts/audit/measure-pipefail-scope-drop.py --out X.tsv # 指定输出
  ⇒ 只读对方量具（import，不改）⇒ 产物不依赖对方改不改。

★ ★★ **两个脚本不要再分头写文件头**（本轮踩过：生成器改了归因、另一个脚本没改 ⇒
  清单里出现两套数）⇒ 本脚本是**唯一**的写头处。
"""
import argparse
import importlib.util
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
DEFAULT_OUT = os.path.join(
    REPO, "docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv")
PEER_TOOL = os.path.join(REPO, "scripts/audit/measure-pipefail-race-surface.py")

SKIP_DIRS = {".git", "vendor", "node_modules", ".venv"}
# ★ 量具自身（被测形态是其【设计目标】⇒ 必须排除）
EXCLUDE_MEASURE = {
    "scripts/audit/probe-pipefail-sigpipe-shape.sh",
    # ★ 段十四新增的探针（同样**故意**要制造 SIGPIPE ⇒ 命中被测形态是设计如此）
    "scripts/audit/probe-pipefail-harm-conditions.sh",
    # ★★ 段十四第十一小节新增的探针（测 errexit × pipefail ⇒ 同样要跑管道）
    "scripts/audit/probe-pipefail-errexit-interaction.sh",
}

RE_VAR = re.compile(
    r"(?:\becho\b|\bprintf\b)[^|;&]*\$[A-Za-z_{@*0-9][^|;&]*\|\s*"
    r"(?:grep\b[^|;&]*-[a-zA-Z]*[qm]|head\b)")
RE_W = re.compile(r'(?:echo|printf\s+\'%s\')\s+"\$([A-Za-z_][A-Za-z0-9_]*)"')
RE_ASSIGN = re.compile(r"^\s*(?:local\s+|export\s+|declare\s+|readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)=")

BULK_CMDS = ("cat", "awk", "grep", "egrep", "sed", "find", "tail", "head", "docker",
             "go ", "psql", "curl", "wget", "sort", "uniq", "tr", "cut", "xargs",
             "kubectl", "journalctl", "npm", "make", "git ")
SCALAR_CMDS = ("read", "wc ", "printf", "echo", "basename", "dirname", "pwd", "date")



# ── 段十四（§19.20.75）· 「管道退出码是否真被消费」判定 ────────────────────────
# ★ 为什么要有这一列（段十三的三个必要条件不够）：
#   危害成立还需要【条件④】：管道退出码必须【真的】被消费 ⇒ 否则 141 只是一条被忽略的
#   退出码 ⇒ 无害。消费方式只有四种：
#     (a) `if <管道>; then` / elif / while / until ⇒ 判据
#     (b) `<管道> && cmd` / `<管道> || cmd` ⇒ 左侧状态
#     (c) `x="$(<管道>)"` ⇒ **赋值语句的状态 = 命令替换的状态** ⇒ 消费
#     (d) `echo "$(<管道>)"` ⇒ **命令替换的退出码被丢弃** ⇒ **不消费** ⇒ 假阳性
#   ★ 对方的 `classify_usage` 只看管道前缀有没有 if/&&/|| ⇒ (d) 这种形态会被误判成 judgment。
#
# ★★ bash 语义要点：`$( )` 内部**引号上下文重置** ⇒ `echo "$(echo "$x" | head)"` 里那个
#   `|` 是【未加引号】的管道 ⇒ 扫描器必须为每个 `$( )` 开一层新的引号上下文
#   （我自己第一版就栽在这里：把外层引号一路带进去 ⇒ 漏掉了管道）。


def scan_pipes(line):
    """按 bash 语义分层扫描 ⇒ [(管道位置, 是否在命令替换内, 替换是否在赋值右侧)]。"""
    stack = [[False, False, False]]     # (单引号内, 双引号内, 赋值右侧)
    pipes = []
    i, n = 0, len(line)
    while i < n:
        c = line[i]
        ctx = stack[-1]
        if ctx[0]:
            if c == "'":
                ctx[0] = False
            i += 1
            continue
        if ctx[1]:
            if c == "\\":
                i += 2
                continue
            if c == '"':
                ctx[1] = False
                i += 1
                continue
            if line.startswith("$(", i):
                stack.append([False, False, bool(re.search(r"=\s*$", line[:i].rstrip()))])
                i += 2
                continue
            i += 1
            continue
        if c == "'":
            ctx[0] = True
        elif c == '"':
            ctx[1] = True
        elif c == "\\":
            i += 2
            continue
        elif line.startswith("$(", i):
            stack.append([False, False, bool(re.search(r"=\s*$", line[:i].rstrip()))])
            i += 2
            continue
        elif c == "(":
            stack.append([False, False, False])
        elif c == ")":
            if len(stack) > 1:
                stack.pop()
        elif c == "|" and not (i + 1 < n and line[i + 1] == "|"):
            pipes.append((i, len(stack) > 1, stack[-1][2] if len(stack) > 1 else False))
        i += 1
    return pipes


def exit_consumed(line):
    """返回 'consumed' / 'discarded' / 'unknown'。"""
    ps = scan_pipes(line)
    if not ps:
        return "unknown"
    p, in_subst, is_assign = ps[0]
    if in_subst:
        return "consumed" if is_assign else "discarded"
    pre = line[:p]
    if re.search(r"\b(if|elif|while|until)\b", pre) or re.search(r"&&|\|\|", pre):
        return "consumed"
    # ★★★ 段十五（§19.20.76）补：管道在 `&&` / `||` 【左侧】时，其退出码同样【被消费】
    #   （形态：`A | B || cmd` / `A | B && cmd`）⇒ ⇒ 此前只扫【管道之前】的 pre
    #   ⇒ ⇒ **假阴性** ⇒ ⇒ 段十四「89 处 consumed」是【低估】的 ⇒ ⇒ ★ 与段十四⑪同族：
    #   ★「查某物」型断言的正则必须覆盖【所有实际写法】⇒ 这里漏了「管道在 && / || 左侧」。
    if tail_after_pipe(line, p) in ("&&", "||"):
        return "consumed"
    return "discarded"


def tail_after_pipe(line, p):
    """返回管道【之后】遇到的第一个顶层分隔符（&& / || / ;）；忽略引号内的内容。

    ★ 为什么要按【顶层】走：形如 `echo "a && b" | grep x` 的引号内容不能算分隔符。
    ★ 为什么要找【第一个】：`A | B ; C || D` 里的 `||` 属于另一条命令 ⇒ 不算消费。
    """
    i = p
    n = len(line)
    quote = None
    while i < n:
        c = line[i]
        if quote:
            if c == quote:
                quote = None
        elif c in "'\"":
            quote = c
        elif c == ";":
            return ";"
        elif c == "&" and line[i:i + 2] == "&&":
            return "&&"
        elif c == "|" and line[i:i + 2] == "||":
            return "||"
        i += 1
    return None


def load_peer():
    spec = importlib.util.spec_from_file_location("peerpipe", PEER_TOOL)
    pp = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(pp)
    return pp


def classify_var(lines, var):
    """静态代理：写端变量的内容形态（bulk / scalar / unknown）—— ★ 只可分诊，不可定性。"""
    pat = re.compile(r"^\s*(?:local\s+|export\s+|declare\s+|readonly\s+)?%s\s*=" % re.escape(var))
    for ln in lines:
        m = pat.match(ln)
        if not m:
            continue
        rhs = ln[m.end():].strip()
        if rhs.startswith("$(") or rhs.startswith("$(("):
            inner = rhs[2:] if rhs.startswith("$(") else rhs
            head = inner.strip()[:80]
            for c in SCALAR_CMDS:
                if head.startswith(c) or (" %s " % c) in inner[:120]:
                    return "scalar"
            for c in BULK_CMDS:
                if head.startswith(c) or (" %s " % c) in inner[:120]:
                    if re.search(r"\bhead\s+-1\b", inner) or re.search(r"\bgrep\b[^|;]*-m1\b", inner):
                        return "scalar"
                    return "bulk"
            return "unknown"
        if rhs.startswith('"') or rhs.startswith("'"):
            if "$(" in rhs:
                for c in BULK_CMDS:
                    if c in rhs:
                        return "bulk"
                return "unknown"
            return "scalar"
        return "unknown"
    return "unknown"


def collect(pp):
    rows = []
    for dp, dn, fns in os.walk(REPO):
        dn[:] = [d for d in dn if d not in SKIP_DIRS]
        for fn in sorted(fns):
            if not fn.endswith(".sh"):
                continue
            p = os.path.join(dp, fn)
            rel = os.path.relpath(p, REPO)
            if rel.startswith("docs/") or rel in EXCLUDE_MEASURE:
                continue
            try:
                raw = open(p, encoding="utf-8", errors="replace").read()
            except OSError:
                continue
            body = pp.strip_shell_comments(raw)      # ★ 剥注释（保持行数）
            lines = body.split("\n")
            scopes = pp.scope_map(lines)
            pf_on, pf_off = [], []
            for i, ln in enumerate(lines, 1):
                m = pp.RE_SET.match(ln)
                if not m:
                    continue
                on, off = pp.parse_set_flags(m.group("rest"))
                sc = scopes.get(i, "<top>")
                if on:
                    pf_on.append((i, sc))
                if off:
                    pf_off.append((i, sc))
            for i, ln in enumerate(lines, 1):
                if not RE_VAR.search(ln):
                    continue
                sc = scopes.get(i, "<top>")
                on_b = [pl for pl, ps in pf_on if ps == sc and pl < i]
                off_a = [pl for pl, ps in pf_off if ps == sc and on_b and pl > max(on_b)]
                if on_b and not off_a:
                    continue                                   # 对方已放行
                top_on = [pl for pl, ps in pf_on if ps == "<top>" and pl < i]
                top_off = [pl for pl, ps in pf_off if ps == "<top>" and pl < i]
                if not (top_on and not (top_off and max(top_off) > max(top_on))):
                    continue                                   # 顶层没开 pipefail
                usage, consumer = "", ""
                for m in pp.RE_PIPE_CONSUMER.finditer(ln):
                    early, k = pp.consumer_kind(m.group("c"))
                    if early:
                        usage = pp.classify_usage(ln, m.start())[0]
                        consumer = k
                        break
                wm = RE_W.search(ln)
                form = classify_var(lines, wm.group(1)) if wm else "unknown"
                hint = {"bulk": "real-defect-candidate",
                        "scalar": "safe-single-line"}.get(form, "needs-human-read")
                raw_line = raw.split("\n")[i - 1] if i - 1 < len(raw.split("\n")) else ln
                ec = exit_consumed(raw_line)
                rows.append((rel, str(i), sc, "var-write", usage, form, hint, consumer,
                             ec, ln.strip()))
    rows.sort()
    return rows


HEADER = """# R99 轮 · 段十四（§19.20.75）· 「作用域模型盲区」候选清单（**五条件收敛版**）
#
# ★★★★★★ **本文件是 `scripts/audit/measure-pipefail-scope-drop.py` 的【逐字产物】**
#   ⇒ ★ **禁止手改**；★ **改了生成器就必须重跑本文件**；★ 门判据⑤保证二者逐字一致
#   ⇒ ★ 引用本文件任何计数都必须带【时点 + 树】（本文件随仓内形态漂移）
#
# ★ 口径（三条同时成立）：
#   ① 该行命中「变量写端 → 单管道 → 早退消费者」形态（echo/printf + 变量展开 | grep -[qm]/head）
#   ② **顶层**（<top> 作用域）`set -o pipefail` 在该行之前已启用且未被关闭
#      ⇒ 真实 shell 语义下该行受 pipefail 覆盖（`set -o pipefail` 是进程级选项，函数体继承）
#   ③ 对方量具 `measure-pipefail-race-surface.py` 的 after_pf=False
#      ⇒ 它的轴 2 要求「启用点与管道点作用域完全相同」⇒ 函数体里的点被**静默丢弃**
#
# 列：file\tline\tscope\tkind\tusage\twrite_end_form\tverdict_hint\tconsumer\texit_consumed\tfull_line
# ★ usage = 对方 classify_usage 的读数：judgment=退出码被当判据消费｜bare=未被消费｜value=从管道取值
#     ★ ★ judgment **不等于真缺陷面** ⇒ 还要看 exit_consumed + write_end_form + 人工回读
# ★ exit_consumed = **本清单自算**（段十四新增）：管道退出码是否【真的】被消费
#     consumed  = 被消费（if/elif/while/until 判据 ｜ &&/|| 左侧 ｜ x="$(管道)" 赋值）
#     discarded = **被丢弃** ⇒ 假非零只是一条被忽略的退出码 ⇒ **无危害**
#     ★ ★ 对方的 `usage=judgment` 在 `echo "$(管道)"` 这种形态上会**误判**（命令替换的退出码
#        被丢弃）⇒ 本轮实测到 **2 处假阳性**（pg-error-classifier.sh:151、scan-secrets.sh:196）
# ★ write_end_form = **写端输出的行数形态**（危害成立的必要条件之一）：
#     bulk   = 变量来自无上界的多行来源（命令替换里是 cat/awk/grep/sed/find/docker logs/go test…）
#     scalar = 变量来自单条记录 / 单行标量（read / head -1 / grep -m1 / 字面量）
#     unknown= 静态判不了 ⇒ 必须人工回读源码
# ★ verdict_hint：bulk ⇒ real-defect-candidate（**只是分诊线索**）｜scalar ⇒ safe-single-line｜unknown ⇒ needs-human-read
#     ★ ★ 它**只说明「多行」，不说明「能超 64 KiB」** ⇒ 抽样核对抓到两个方向的错
#       （假阳性 scripts/audit-all-tables.sh:268：单条 SQL 结果；假阴性 scripts/health-check-154.sh:138：
#        HTTP 响应体可能多行但远小于 64 KiB ⇒ 人工回读结论 = 无害）⇒ **代理只可分诊、不可定性**
#
# ★★★★★★ **危害成立的【五个必要条件】（段十三三条 + 段十四两条，缺一即无害）**：
#   ① 写端输出【多行】（grep 按行匹配 ⇒ 必须读到行尾才敢判 ⇒ 单行时它读完整条 ⇒ 写端也写完 ⇒ 无害）
#   ② 写端可能【超管道缓冲 64 KiB】（★ 段十三只写了「多行」⇒ 漏了「上界」这一维）
#   ③ 命中【靠前】（命中在末尾 ⇒ grep 也要读到末尾 ⇒ 写端写完 ⇒ 无害）
#   ④ ★★ **管道退出码【真被消费】**（段十四新增 ⇒ 对方的 usage=judgment 会误判 `echo "$(管道)"`）
#   ⑤ 消费者【早退】（grep -q / head -c）
#   ⇒ ★ **118 → 110 的收敛**（★ 段十四当时的树）：118 候选 ⇒ 条件④后 89 ⇒ 逐处人工回读
#     ⇒ **真缺陷面 8 处**（已改 herestring：verify-g1-route-contract.sh:127、
#     verify-l11b-cost-view-rowset.sh:175、deploy-seamless.sh:1401/1403/1405/1407、
#     verify-ursm-v2-rollout.sh:112、verify-local-deployment.sh:333）
#   ⇒ ★★ **当前树上是 120 行 / 43 文件** ⇒ 与 110 的差额 **+10 = 对方的两笔提交**
#     （R82 `459d80a3c` 引入 verify-nonplain-migration-delivery.sh 的 9 处管道
#       ＋ R75 `dea4eda4d` 引入 ursm-snapshot-slot-refresh.sh 的 1 处）
#     ⇒ ★ **不是**本轮扫描口径变化（口径的差值「剥注释 −2 / 顶层收紧 ±0」**不变**）
#
# ★★ **机制依据（可重跑，bash 5.3.9/darwin-arm64）**：`scripts/audit/probe-pipefail-sigpipe-shape.sh`
#   ⇒ 多行 + 超管道缓冲(64 KiB) + 命中靠前 ⇒ 写端被 SIGPIPE ⇒ 管道退出码 141
#     ⇒ pipefail ⇒ 假非零 ⇒ 判定塌成「没匹配」
#   ⇒ ★★ **段十四实测的两条边界事实**（段十三的表格说「任意」是过宽表述）：
#     · `head -c` 消费者**同样要求写端 > 64 KiB**（60,000 B ⇒ 0；70,000 B ⇒ 141）
#       ⇒ ★ **且紧邻边界【有竞态】**：70,000 B 连跑 8 次 ⇒ 0 出现 3 次、141 出现 5 次
#         ⇒ **边界读数不可钉进门**（只可钉 100 KB / 200 KB 这类远离边界的点）
#     · `grep -q` 的同一边界是【确定的】：65,537 B ⇒ 0、70,000 B ⇒ 141（6/6 稳定）
#
# ★★ **四变体单变量对照（同一棵树、逐项开关、含量具排除）＝真实归因**
#    （★ 段十四**定稿时在当前树重测** ⇒ 不抄旧值）：
#      A 段十一口径（不剥注释 + 顶层宽松）= 122 处 / 44 文件
#      B 只剥注释                        = 120 处 / 43 文件 ⇒ **剥注释 = −2**
#      C 只收紧顶层                      = 122 处 / 44 文件 ⇒ **顶层收紧 = ±0**
#      D 段十四现状（剥注释 + 顶层收紧）   = 120 处 / 43 文件
#      ⇒ ⇒ ★ **差值不变**（剥注释 −2、顶层收紧 ±0）⇒ ★ **差值是结构性的、绝对值随树漂移**
#      ⇒ ⇒ ★ 引用任何计数都必须带口径 + 带时点 + 带【树】（同一棵树上 A 与 D 差 2；跨轮次全部变）
#      ⇒ ⇒ ★ 那 2 条幽灵注释行已定位到具体站点：scripts/audit/verify-session-family-p0-realdb.sh:116/124
#
# ★ 本版相对段十一那版的三处修正：① 剥注释（多报 2 条）② 顶层覆盖判据收紧为「只认 <top> 作用域」
#   （口径该严谨，但实测不改任何一条的归属）③ 加 write_end_form / verdict_hint 两列
#   ④ ★★ 段十四：加 exit_consumed 列 + 修掉 8 处真缺陷 ⇒ 118 → 110 行 / 42 → 41 文件
#   ⑤ ★★ 段十四定稿：并入对方的 R82/R75 共 10 处新站点 ⇒ 110 → 120 行 / 41 → 43 文件
#
# ★★★★★★ **本文件【禁止手改】** ⇒ 它是本生成器的【逐字产物】
#   ⇒ ★ **改了生成器就必须重跑本文件**（`python3 scripts/audit/measure-pipefail-scope-drop.py`）
#     ⇒ 否则仓内形态与本清单【脱节】⇒ 而这种脱节**行数基线挡不住**（行数可能不变、内容已过期）
#   ⇒ ★ 由 §19.20.75 的门判据⑤保证：**清单 ↔ 生成器产出必须逐字一致**（强判据）
#   ⇒ ★★ 本轮就栽在这上面：段十四初版清单是【跨一次 merge 未重跑】的过期值（110 vs 实际 120）
#     ⇒ 而当时的门只比【行数】、基线又是【从清单抄的】⇒ ★ **门绿、量具瞎了**（自证）
#
# ★★ **量具自身被显式排除**：`scripts/audit/probe-pipefail-sigpipe-shape.sh`（段十三）
#   与 `scripts/audit/probe-pipefail-harm-conditions.sh`（段十四）
#   （它的测试管道**故意**要制造 SIGPIPE ⇒ 命中被测形态是【设计如此】
#     ⇒ 不排除的话，消费方会把它当成待修的缺陷 ⇒ ★ 扫描面必须显式排除量具自身）
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=DEFAULT_OUT)
    args = ap.parse_args()

    pp = load_peer()
    rows = collect(pp)
    out = args.out
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with open(out, "w", encoding="utf-8") as f:
        f.write(HEADER)
        for r in rows:
            f.write("\t".join(r) + "\n")

    from collections import Counter
    c = Counter(r[4] for r in rows)
    h = Counter(r[6] for r in rows)
    print("写出 %s" % out)
    print("  行数 = %d ；文件 = %d" % (len(rows), len(set(r[0] for r in rows))))
    print("  usage 分布 = %s" % dict(c))
    print("  exit_consumed 分布 = %s" % dict(Counter(r[8] for r in rows)))
    print("  verdict_hint 分布 = %s" % dict(h))
    return 0


if __name__ == "__main__":
    sys.exit(main())
