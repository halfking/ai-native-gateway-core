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
EXCLUDE_MEASURE = {"scripts/audit/probe-pipefail-sigpipe-shape.sh"}

RE_VAR = re.compile(
    r"(?:\becho\b|\bprintf\b)[^|;&]*\$[A-Za-z_{@*0-9][^|;&]*\|\s*"
    r"(?:grep\b[^|;&]*-[a-zA-Z]*[qm]|head\b)")
RE_W = re.compile(r'(?:echo|printf\s+\'%s\')\s+"\$([A-Za-z_][A-Za-z0-9_]*)"')
RE_ASSIGN = re.compile(r"^\s*(?:local\s+|export\s+|declare\s+|readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)=")

BULK_CMDS = ("cat", "awk", "grep", "egrep", "sed", "find", "tail", "head", "docker",
             "go ", "psql", "curl", "wget", "sort", "uniq", "tr", "cut", "xargs",
             "kubectl", "journalctl", "npm", "make", "git ")
SCALAR_CMDS = ("read", "wc ", "printf", "echo", "basename", "dirname", "pwd", "date")


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
                rows.append((rel, str(i), sc, "var-write", usage, form, hint, consumer, ln.strip()))
    rows.sort()
    return rows


HEADER = """# R99 轮 · 段十三（§19.20.72）· 「作用域模型盲区」候选清单（重生成 + 写端形态定性版）
#
# ★ 口径（三条同时成立）：
#   ① 该行命中「变量写端 → 单管道 → 早退消费者」形态（echo/printf + 变量展开 | grep -[qm]/head）
#   ② **顶层**（<top> 作用域）`set -o pipefail` 在该行之前已启用且未被关闭
#      ⇒ 真实 shell 语义下该行受 pipefail 覆盖（`set -o pipefail` 是进程级选项，函数体继承）
#   ③ 对方量具 `measure-pipefail-race-surface.py` 的 after_pf=False
#      ⇒ 它的轴 2 要求「启用点与管道点作用域完全相同」⇒ 函数体里的点被**静默丢弃**
#
# 列：file\tline\tscope\tkind\tusage\twrite_end_form\tverdict_hint\tconsumer\tfull_line
# ★ usage = 对方 classify_usage 的读数：judgment=退出码被当判据消费｜bare=未被消费｜value=从管道取值
#     ★ judgment **不等于真缺陷面** ⇒ 真缺陷面要看 write_end_form + 人工回读
# ★ write_end_form = **写端输出的行数形态**（危害成立的必要条件之一）：
#     bulk   = 变量来自无上界的多行来源（命令替换里是 cat/awk/grep/sed/find/docker logs/go test…）
#     scalar = 变量来自单条记录 / 单行标量（read / head -1 / grep -m1 / 字面量）
#     unknown= 静态判不了 ⇒ 必须人工回读源码
# ★ verdict_hint：bulk ⇒ real-defect-candidate（**真缺陷面**）｜scalar ⇒ safe-single-line（**无危害**）
#   ｜unknown ⇒ needs-human-read
#
# ★★★★★★ 【重要】write_end_form / verdict_hint 是【静态代理】，只可当【分诊线索】，
#   ★★ **不可当【定性】用** ⇒ 抽样核对当场抓到【两个方向的错】：
#   ① 假阳性方向（把无害报成缺陷）：`scripts/audit-all-tables.sh:268`
#      `if echo "$updated" | grep -q "completed"; then` ⇒ 代理判 bulk，但 `$updated` 需人工回读
#      （大概率是单条更新消息 ⇒ 实际应为 safe-single-line）
#   ② 假阴性方向（把缺陷报成无害）：`scripts/health-check-154.sh:138`
#      `if [[ -n "$health_response" ]] && echo "$health_response" | grep -q "healthy"; then`
#      ⇒ 代理判 scalar，但 `$health_response` 是 HTTP 响应体 ⇒ **可能多行** ⇒ 需人工回读
#   ③ 未找到赋值 ⇒ unknown（例：`scripts/.coverage-820-probe.sh:33`）
#   ⇒⇒★ **通用式**：★ 静态代理分类可以用来【分诊】，但**不能用来【定性】**
#      ⇒ 一旦拿它批量改代码，就会在【最危险的维度】上造假阳性（把无害的点报成缺陷）
#      ⇒ ⇒ **抽样核对是最低要求**；⇒ 本族**必须逐处人工回读**
#
# ★★ **量具自身被显式排除**：`scripts/audit/probe-pipefail-sigpipe-shape.sh`
#   （它的测试管道**故意**要制造 SIGPIPE ⇒ 命中被测形态是【设计如此】
#     ⇒ 不排除的话，消费方会把它当成待修的缺陷 ⇒ ★ 扫描面必须显式排除量具自身）
#
# ★★ 机制依据（可重跑，bash 5.3.9/darwin-arm64）：`scripts/audit/probe-pipefail-sigpipe-shape.sh`
#   ⇒ `grep -q` 只有在写端【多行】时才可能早退（单行时 grep 必须读到行尾才敢判）
#   ⇒ 多行 + 超管道缓冲(64 KiB) + 命中靠前 ⇒ 写端被 SIGPIPE ⇒ 管道退出码 141
#     ⇒ pipefail ⇒ 假非零 ⇒ 判定塌成「没匹配」
#   ⇒ 而消费者是 `head -c` ⇒ 无论行数都早退 ⇒ **有害**
#
# ★★ **四变体单变量对照（同一棵树、逐项开关、含量具排除）＝真实归因**：
#      A 段十一口径（不剥注释 + 顶层宽松）= 120 处 / 43 文件
#      B 只剥注释                        = 118 处 / 42 文件 ⇒ **剥注释 = −2**
#      C 只收紧顶层                      = 120 处 / 43 文件 ⇒ **顶层收紧 = ±0**
#      D 段十三现状（剥注释 + 顶层收紧）   = 118 处 / 42 文件
#      ⇒ ⇒ ★ 「顶层收紧」让口径更严谨但**不改任何一条的归属**（该保留，但不可声称它改了几个数）
#      ⇒ ⇒ ★★ **段十一清单的 120 ⇒ 多报 2 条**（它把「修复注释里引用旧写法」的注释行收进了候选集）
#           ⇒ ★ 引用任何计数都必须带口径（同一棵树上 A 与 D 差 2）
#
# ★ 本版相对段十一那版的三处修正：① 剥注释（段十一版把「修复注释里引用旧写法」的
#   注释行也收进来了 ⇒ 多报 2 条）② 顶层覆盖判据收紧为「只认 <top> 作用域的 enable」
#   （段十一版用「任何作用域的 enable」⇒ 太宽；但实测**不改任何一条的归属**）
#   ③ 加 write_end_form / verdict_hint 两列
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
    print("  verdict_hint 分布 = %s" % dict(h))
    return 0


if __name__ == "__main__":
    sys.exit(main())
