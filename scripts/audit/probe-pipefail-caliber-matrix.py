#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""probe-pipefail-caliber-matrix.py —— 「变量写端 → 早退消费者」族的【口径矩阵穷举器】。

★ 它是什么：
  一个**纯只读**的穷举器（★ **不是**清单的写头处 ⇒ **本脚本不产出、不修改任何清单**）
  ⇒ 它把「这一族」的【口径】拆成三轴，穷举全部组合 ⇒ ⇒ 回答：
    「某个读数（例如 121 处 / 43 文件）到底是【哪一棵树】×【哪一格口径】的产物？」

★ 为什么要有它（★ 本轮的真实起因）：
  R99 轮段十三（`§19.20.72`）① 记了一个「121 处 / 43 文件」，与「已入库清单 120 行 / 43 文件」差 1；
  段十三 ⑥ 写「对方报的 121 我用 8 种定义在他们的基线上都复现不出（最近的是 119/42 与 127/47）」。
  ⇒ ⇒ ★★★ **⇒ 穷举是比「逐个猜」更硬的定位手段**：
    口径有 k 维 ⇒ 先把每维的档位列出来 ⇒ 跑笛卡尔积 ⇒
    ★ **「复现不出」本身就是结论**（⇒ 它不是任何口径变体的产物 ⇒ 必是【别的树】或【别的轴】）。

★ 三轴（每轴的档位都可穷举 ⇒ 12 组合）：
  A 剥注释        : no / yes
  B 排除量具自身  : none / seg11(1) / all3
  C 顶层 pipefail : loose（任何作用域的启用点都算顶层覆盖） / strict（只认 <top>）

★ 用法：
    python3 scripts/audit/probe-pipefail-caliber-matrix.py                 # 当前仓
    python3 scripts/audit/probe-pipefail-caliber-matrix.py --archive <sha> # ★ 纯净副本（git archive ⇒ 不动共享树/分支）
    python3 scripts/audit/probe-pipefail-caliber-matrix.py --tree /path    # 任意目录
    python3 scripts/audit/probe-pipefail-caliber-matrix.py --verify-manifest
        ⇒ ★★ 用【那棵树自己的生成器】跑一份清单，并与该树内已入库的清单【逐字比对】
          ⇒ 强判据（★ 比行数强：★ 内容过期但计数不变时，比行数会静默通过）

★ ★★ **只读保证**：唯一可能的写入是 `--out`（默认写到 stdout）⇒ ⇒ **绝不动仓内任何文件**。
★ ★★ **量具只读**：本脚本只 `import` 树内的 `scripts/audit/measure-pipefail-race-surface.py`
   与 `scripts/audit/measure-pipefail-scope-drop.py`（不修改它们）⇒ ⇒ 口径不会因本脚本而漂移。
"""
import argparse
import importlib.util
import os
import re
import shutil
import subprocess
import sys
import tempfile

# peer 的窄族判据（逐字取自 measure-pipefail-scope-drop.py 的 RE_VAR）
RE_PEER_VAR = re.compile(
    r"(?:\becho\b|\bprintf\b)[^|;&]*\$[A-Za-z_{@*0-9][^|;&]*\|\s*"
    r"(?:grep\b[^|;&]*-[a-zA-Z]*[qm]|head\b)")
# 我方的宽族判据（逐字取自 measure-pipefail-race-surface.py 的 RE_ECHO_VAR）
RE_MY_VAR = re.compile(r"\b(echo|printf)\b[^|]*\$")

SEG11_PROBES = {"scripts/audit/probe-pipefail-sigpipe-shape.sh"}
ALL3_PROBES = {
    "scripts/audit/probe-pipefail-sigpipe-shape.sh",
    "scripts/audit/probe-pipefail-harm-conditions.sh",
    "scripts/audit/probe-pipefail-errexit-interaction.sh",
}
MANIFEST_REL = "docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv"
RACE_TOOL_REL = "scripts/audit/measure-pipefail-race-surface.py"
GEN_REL = "scripts/audit/measure-pipefail-scope-drop.py"


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def make_tree(args):
    """返回 (树根目录, 临时目录或None)。--archive 用 git archive ⇒ 纯净副本。"""
    if args.tree:
        return os.path.abspath(args.tree), None
    if not args.archive:
        return os.getcwd(), None
    tmp = tempfile.mkdtemp(prefix="pipefail-matrix-")
    dest = os.path.join(tmp, "tree")
    os.makedirs(dest)
    p = subprocess.run(["git", "archive", args.archive], stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE)
    if p.returncode != 0:
        sys.stderr.write("git archive %s 失败：%s\n"
                         % (args.archive, p.stderr.decode("utf-8", "replace")))
        sys.exit(2)
    tar = subprocess.run(["tar", "-x", "-C", dest], input=p.stdout,
                         stderr=subprocess.PIPE)
    if tar.returncode != 0:
        sys.stderr.write("解包失败：%s\n" % tar.stderr.decode("utf-8", "replace"))
        sys.exit(2)
    return dest, tmp


def collect(pp, root, strip, exclude, top):
    """跑一遍，返回 [(rel, line), ...]。"""
    rows = []
    for p, rel in pp.collect_scripts(root, ("vendor/",)):
        if rel.startswith("docs/") or rel in exclude:
            continue
        try:
            with open(p, encoding="utf-8", errors="replace") as f:
                raw = f.read()
        except OSError:
            continue
        body = pp.strip_shell_comments(raw) if strip else raw
        lines = body.split("\n")
        scopes = pp.scope_map(lines)
        on, off = [], []
        for i, ln in enumerate(lines, 1):
            m = pp.RE_SET.match(ln)
            if not m:
                continue
            o, of = pp.parse_set_flags(m.group("rest"))
            sc = scopes.get(i, "<top>")
            if o:
                on.append((i, sc))
            if of:
                off.append((i, sc))
        for i, ln in enumerate(lines, 1):
            if not RE_PEER_VAR.search(ln):
                continue
            sc = scopes.get(i, "<top>")
            same_on = [pl for pl, ps in on if ps == sc and pl < i]
            same_off = [pl for pl, ps in off if ps == sc and same_on and pl > max(same_on)]
            if same_on and not same_off:
                continue                      # 对方已放行（after_pf=True）
            if top == "strict":
                t_on = [pl for pl, ps in on if ps == "<top>" and pl < i]
                t_off = [pl for pl, ps in off
                         if ps == "<top>" and t_on and pl > max(t_on)]
                covered = bool(t_on) and not (t_off and max(t_off) > max(t_on))
            else:
                a_on = [pl for pl, ps in on if pl < i]
                a_off = [pl for pl, ps in off if a_on and pl > max(a_on)]
                covered = bool(a_on) and not (a_off and max(a_off) > max(a_on))
            if covered:
                rows.append((rel, i))
    return rows


def verify_manifest(root):
    """★ 强判据：用该树自己的生成器跑一份清单，与该树内已入库的清单逐字比对。"""
    gen = os.path.join(root, GEN_REL)
    man = os.path.join(root, MANIFEST_REL)
    if not os.path.exists(gen):
        return "SKIP（该树没有生成器）"
    if not os.path.exists(man):
        return "SKIP（该树没有已入库清单）"
    tmpd = tempfile.mkdtemp(prefix="pipefail-verify-")
    try:
        out = os.path.join(tmpd, "gen.tsv")
        p = subprocess.run([sys.executable, gen, "--out", out], cwd=root,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if p.returncode != 0:
            return "FAIL（生成器退出码 %d）" % p.returncode
        a = open(out, "rb").read()
        b = open(man, "rb").read()
        if a == b:
            return "PASS（清单 ↔ 生成器逐字一致）"
        la, lb = a.decode("utf-8", "replace").split("\n"), b.decode("utf-8", "replace").split("\n")
        na = len([x for x in la if x and not x.startswith("#")])
        nb = len([x for x in lb if x and not x.startswith("#")])
        return ("FAIL（不一致：生成器 %d 行 / 仓内 %d 行 ⇒ ★ 行数判据挡不住这种漂移）"
                % (na, nb))
    finally:
        shutil.rmtree(tmpd, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tree", help="任意目录（默认当前仓）")
    ap.add_argument("--archive", help="git commit sha ⇒ 用 git archive 解出纯净副本")
    ap.add_argument("--out", help="矩阵输出到文件（默认 stdout）")
    ap.add_argument("--verify-manifest", action="store_true",
                    help="★ 用该树自己的生成器跑一份清单，并与已入库清单逐字比对")
    args = ap.parse_args()

    root, tmp = make_tree(args)
    try:
        tool = os.path.join(root, RACE_TOOL_REL)
        if not os.path.exists(tool):
            sys.stderr.write("该树没有 %s ⇒ 无法跑\n" % RACE_TOOL_REL)
            return 2
        pp = load("race", tool)

        out = []
        out.append("### 树：%s" % (args.archive and ("git archive %s" % args.archive) or root))
        # ★★★ 扫描面指纹（★ 第三个锚点）：读数要钉【基线 SHA（内容）】＋【扫描面（分母）】＋【工具版本（判据）】
        #   ⇒ ⇒ ★ 因为污染源（残留 worktree / 临时校验区）**不在 git 里** ⇒ ⇒ ⇒ 基线 SHA 钉不住它。
        files = pp.collect_scripts(root, ("vendor/",))
        hidden = sorted({rel.split("/")[0] for _p, rel in files if rel.startswith(".")})
        out.append("== 扫描面（★ 钉分母的第三个锚点）==")
        out.append("  扫描脚本数   = %d" % len(files))
        out.append("  排除目录     = %s" % ", ".join(sorted(getattr(pp, "SKIP_DIRS", ()))))
        out.append("  隐藏目录命中 = %s" % (", ".join(hidden) if hidden else "(无)"))
        if hidden:
            out.append("  ★ 命中隐藏目录 %d 个（%s）⇒ ⇒ 读数【是否可跨机比较】取决于它们是否命中判据"
                       " ⇒ ⇒ ★ **必须用 --archive <sha> 对拍确认**"
                       % (len(hidden), ", ".join(hidden)))
        else:
            out.append("  ✓ 扫描面无隐藏目录副本 ⇒ 读数可跨机比较")
        if args.verify_manifest:
            out.append("  清单 ↔ 生成器 强判据：%s" % verify_manifest(root))
        out.append("")
        out.append("%-7s %-9s %-7s %8s %7s" % ("剥注释", "排除量具", "顶层", "行数", "文件数"))
        out.append("-" * 56)
        hits = []
        for strip in (False, True):
            for nm, ex in (("none", set()), ("seg11(1)", SEG11_PROBES), ("all3", ALL3_PROBES)):
                for top in ("loose", "strict"):
                    rows = collect(pp, root, strip, ex, top)
                    n, nf = len(rows), len(set(r[0] for r in rows))
                    if n == 121:
                        hits.append((strip, nm, top, n, nf))
                    out.append("%-7s %-9s %-7s %8d %7d%s" % (
                        "yes" if strip else "no", nm, top, n, nf,
                        "  ★★★ 命中 121" if n == 121 else ""))
        out.append("")
        if hits:
            for s, e, t, n, nf in hits:
                out.append("⇒ ★★★ 「121」出现于：剥注释=%s 排除=%s 顶层=%s ⇒ %d 行 / %d 文件"
                           % ("yes" if s else "no", e, t, n, nf))
        else:
            out.append("⇒ 「121」在【这棵树】的任何口径组合下都不出现")
        text = "\n".join(out)
        if args.out:
            with open(args.out, "w", encoding="utf-8") as f:
                f.write(text + "\n")
            print("矩阵写到 %s" % args.out)
        else:
            print(text)
        return 0
    finally:
        if tmp:
            shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
