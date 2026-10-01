#!/usr/bin/env python3
"""Compare two integration-gate sweeps package-by-package and test-by-test.

    compare-sweeps.py <baseline-dir> <current-dir>

Each sweep directory is the output of a run of the package sweep: one
`<pkg>.log` per package plus (optionally) a `totals.txt` summarising
rc/PASS/SKIP/FAIL/verdict per package.

What this tool refuses to do, and why
------------------------------------
* It does not report a package as "not in the current set" merely because no
  FAIL line was found for it. A package that ran and passed all its tests
  produces no FAIL line, so absence-from-the-FAIL-set is ambiguous between
  "ran and went green" and "never ran". The two are opposite findings, and
  conflating them silently deletes real improvements from the report. The
  ambiguity is resolved against `totals.txt` (which records the sweep's own
  verdict) or, when the baseline has no `totals.txt`, against the per-package
  PASS counts in the logs themselves.
* It does not sum PASS counts across packages into a headline number. Package
  sizes differ by two orders of magnitude, so such a sum would read as a
  claim about coverage rather than about reds.
* It does not treat a package absent from the baseline as improvement.
* It does not call a package with zero tests "ran". The sweep's own VACUOUS
  verdict means "executed but produced no results", which is not a
  measurement.

Counting: FAIL *lines* vs FAIL *tests*
-------------------------------------
`--- FAIL:` lines include subtests. A red parent with two red subtests is
three lines but one red test. Both counts are legitimate; they answer
different questions. This tool reports the parent-folded count as the headline
("red tests") and prints the raw line count next to it, so the two never have
to be reconciled by hand.
"""
import os
import re
import sys
from collections import defaultdict

FAIL = re.compile(r"^\s*--- FAIL:\s+(\S+)")
PASS = re.compile(r"^\s*--- PASS:\s+(\S+)")
SKIP = re.compile(r"^\s*--- SKIP:\s+(\S+)")


def reds(d):
    """pkg -> set of red test roots (subtests folded into the parent)."""
    out = defaultdict(set)
    raw = defaultdict(int)
    if not os.path.isdir(d):
        return out, raw
    for log in sorted(os.listdir(d)):
        if not log.endswith(".log"):
            continue
        pkg = log[:-4]
        for line in open(os.path.join(d, log), errors="replace"):
            m = FAIL.match(line)
            if m:
                out[pkg].add(m.group(1).split("/")[0])
                raw[pkg] += 1
    return out, raw


def counts(d):
    """pkg -> (PASS, SKIP, FAIL, verdict) from totals.txt, if present."""
    out = {}
    tot = os.path.join(d, "totals.txt")
    if os.path.exists(tot):
        for line in open(tot):
            f = line.split()
            if len(f) >= 5 and f[1].startswith("rc="):
                out[f[0]] = (int(f[2].split("=")[1]), int(f[3].split("=")[1]),
                             int(f[4].split("=")[1]), f[5] if len(f) > 5 else "")
    return out


def pass_counts(d):
    """pkg -> PASS count straight from the logs. Works without totals.txt."""
    out = {}
    if not os.path.isdir(d):
        return out
    for log in sorted(os.listdir(d)):
        if not log.endswith(".log"):
            continue
        n = 0
        for line in open(os.path.join(d, log), errors="replace"):
            if PASS.match(line):
                n += 1
        out[log[:-4]] = n
    return out


def ran_in(d, pkg, totals, passes):
    """Did this sweep actually produce a result for `pkg`?

    totals.txt's verdict is authoritative when present. When it is absent
    (older sweeps did not record one) fall back to the log's PASS count,
    which is the minimum evidence that tests executed.
    """
    if pkg in totals:
        return totals[pkg][3] in ("GREEN", "RED")
    return passes.get(pkg, 0) > 0


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    base, cur = sys.argv[1], sys.argv[2]

    b, braw = reds(base)
    c, craw = reds(cur)
    cb, cc = counts(base), counts(cur)
    pb, pc = pass_counts(base), pass_counts(cur)

    # --- completeness proof -------------------------------------------------
    # Reported before the delta, because a delta computed over a sweep where
    # some packages never ran is not comparable to one where they all did.
    for label, d, reds_, totals, passes in (("基线", base, b, cb, pb),
                                            ("当前", cur, c, cc, pc)):
        # Union, not just the logs. A package the sweep ATTEMPTED but produced
        # no log for has no .log to enumerate, so reading the directory alone
        # drops it from the completeness proof entirely and the proof then
        # reports "all 27 ran" while 28 were tried. That is the one thing this
        # section exists to prevent.
        pkgs = sorted(set(passes) | set(totals))
        ran = [p for p in pkgs if ran_in(d, p, totals, passes)]
        not_ran = [p for p in pkgs if p not in ran]
        print(f"[{label}] 目录 {d}")
        print(f"[{label}] 尝试 {len(pkgs)} 包，产出结果 {len(ran)} 包，"
              f"未产出 {len(not_ran)} 包")
        for p in not_ran:
            v = totals.get(p, ("", "", "", "?"))[3]
            print(f"[{label}]   未产出结果: {p}  (verdict={v}, PASS={passes.get(p, 0)})")
        print()

    pkgs = sorted(set(b) | set(c))

    tb = tc = trawb = trawc = 0
    print(f"{'PACKAGE':<28} {'基线红':>8} {'当前红':>8} {'Δ':>5}  状态")
    print("-" * 96)
    for p in pkgs:
        nb, nc = len(b.get(p, ())), len(c.get(p, ()))
        if nb and not nc and ran_in(cur, p, cc, pc):
            mark = f"跑了，{nb} 红 -> 0（转绿）"
        elif nb and not nc:
            mark = "本轮未产出结果，不可称转绿"
        elif nc and not nb:
            mark = "基线无此红（新增红，需查）" if b.get(p) is not None else \
                   "基线未覆盖此包（新增覆盖）"
        elif nb > nc:
            mark = f"改善 {nb - nc}"
        elif nb < nc:
            mark = f"**回退 {nc - nb}**"
        else:
            mark = "持平" if nb else "两轮皆绿"
        tb += nb
        tc += nc
        trawb += braw.get(p, 0)
        trawc += craw.get(p, 0)
        print(f"{p:<28} {nb:>8} {nc:>8} {nc - nb:>+5}  {mark}")

    print("-" * 96)
    print(f"{'合计（红测试，子测试折叠）':<24} {tb:>8} {tc:>8} {tc - tb:>+5}")
    print(f"{'合计（FAIL 原始行，含子测试）':<22} {trawb:>8} {trawc:>8} {trawc - trawb:>+5}")
    print()
    print(f"两数之差 = 被折叠的子测试行：基线 {trawb - tb}，当前 {trawc - tc}")

    print()
    print("仍红的测试（当前集合）：")
    for p in sorted(c):
        for t in sorted(c[p]):
            was = "基线亦红" if t in b.get(p, ()) else "**本轮新增**"
            print(f"  {p:<28} {t:<58} {was}")

    gone = sorted(p for p in b if b[p] and not c.get(p)
                  and ran_in(cur, p, cc, pc))
    if gone:
        print()
        print("本轮从红转绿的包：")
        for p in gone:
            print(f"  {p:<28} {len(b[p])} -> 0   {sorted(b[p])}")


if __name__ == "__main__":
    main()
