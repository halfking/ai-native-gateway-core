#!/usr/bin/env python3
"""Parse the 252 ursm-snapshot-health patrol log into run outcomes + elapsed times.

Why this exists (runbook §143.30):
§143.29.2.2 recorded a "contradiction" — the patrol query was said to be killed
by the role-level `statement_timeout=30s` on some runs while `pg_stat_statements`
showed 26 *successful* 190 s runs ⇒ conclusion "there must be two call paths".
That conclusion was wrong, and the reason it was wrong is visible **only** in the
client's own log: the elapsed time between `start` and the outcome line.

★ Two instrument facts that this parser handles (both were bugs in my first pass):
  ① the log file contains each line **three times** (tee/rotation) ⇒ must dedupe;
  ② the abort reason must be matched on the **error text**, not on "ABORT" alone
     (there are several ABORT kinds; only the statement-timeout one is relevant).

Usage:
    # on 252
    grep -a -E "^\\[20[0-9-]+ [0-9:]+\\] (start ursm-snapshot-health|control ok|baseline span|ABORT|summary: |OK: )" \\
        /var/log/ursm-snapshot-health.log | sort -u > /tmp/patrol.log
    # anywhere
    python3 parse_patrol_log.py /tmp/patrol.log [--era-cut "2026-10-20 00:31:04"]

Outputs (text): per-day OK/ABORT/UNFINISHED, elapsed distributions for OK vs
ABORT, the minimum elapsed among statement-timeout aborts (the number that
refutes "killed at 30 s"), and the pre/post `STMT_TIMEOUT` era split.
"""
import argparse
import collections
import datetime
import re
import sys

LINE_RE = re.compile(r"^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\] (.*)$")
TIMEOUT_MARK = "statement timeout"
OUTCOMES = (
    "start ursm-snapshot-health",
    "control ok",
    "baseline span",
    "ABORT",
    "summary: ",
    "OK: ",
)


def parse(path):
    rows = []
    seen = set()
    with open(path, encoding="utf-8", errors="replace") as fh:
        for ln in fh:
            m = LINE_RE.match(ln.rstrip("\n"))
            if not m:
                continue
            key = (m.group(1), m.group(2))
            if key in seen:          # ★ the log stores every line three times
                continue
            seen.add(key)
            rows.append((datetime.datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S"),
                         m.group(2)))
    runs = []
    cur = None
    for ts, msg in rows:
        if msg.startswith("start ursm-snapshot-health"):
            cur = {"start": ts, "bl": None, "end": None, "kind": None, "why": ""}
            runs.append(cur)
        elif cur is not None:
            if msg.startswith("baseline span"):
                cur["bl"] = ts
            elif msg.startswith("ABORT"):
                cur["end"] = ts
                cur["kind"] = "ABORT"
                cur["why"] = msg
            elif msg.startswith("summary: "):
                cur["end"] = ts
                cur["kind"] = "OK"
    return runs


def elapsed(r):
    return (r["end"] - r["start"]).total_seconds()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("log")
    ap.add_argument("--era-cut", default="2026-10-20 00:31:04",
                    help="STMT_TIMEOUT 180s -> 600s switch point")
    args = ap.parse_args()

    runs = parse(args.log)
    done = [r for r in runs if r["end"]]
    ok = [r for r in done if r["kind"] == "OK"]
    ab = [r for r in done if r["kind"] == "ABORT"]
    to = [r for r in ab if TIMEOUT_MARK in r["why"]]
    ctl = [r for r in ab if TIMEOUT_MARK not in r["why"]]

    print("runs=%d finished=%d (OK=%d ABORT=%d) unfinished=%d"
          % (len(runs), len(done), len(ok), len(ab), len(runs) - len(done)))
    print("abort reasons: statement_timeout=%d other=%d" % (len(to), len(ctl)))

    print("\n=== per-day OK / ABORT / UNFINISHED ===")
    bd = collections.defaultdict(collections.Counter)
    for r in runs:
        bd[r["start"].strftime("%m-%d")][r["kind"] or "UNFIN"] += 1
    for d in sorted(bd):
        c = bd[d]
        tot = c["OK"] + c["ABORT"]
        rate = ("%.0f%%" % (100.0 * c["OK"] / tot)) if tot else "n/a"
        print("   %s OK=%-3d ABORT=%-3d UNFIN=%-3d success=%s" % (d, c["OK"], c["ABORT"], c["UNFIN"], rate))

    def stat(v, label):
        if not v:
            print("   %s n=0" % label)
            return
        xs = sorted(v)
        print("   %-18s n=%-4d min=%.0f p50=%.0f max=%.0f mean=%.0f"
              % (label, len(xs), xs[0], xs[len(xs) // 2], xs[-1], sum(xs) / len(xs)))

    print("\n=== elapsed (seconds, from 'start' to outcome) ===")
    stat([elapsed(r) for r in ok], "OK")
    stat([elapsed(r) for r in to], "ABORT(timeout)")
    if to:
        print("   ★ min elapsed among statement-timeout aborts = %.0f s" % min(elapsed(r) for r in to))
        print("   ★ count below 180 s = %d" % sum(1 for r in to if elapsed(r) < 180))

    cut = datetime.datetime.strptime(args.era_cut, "%Y-%m-%d %H:%M:%S")
    e1 = sorted(elapsed(r) for r in to if r["start"] < cut)
    e2 = sorted(elapsed(r) for r in to if r["start"] >= cut)
    print("\n=== STMT_TIMEOUT era split (cut=%s) ===" % args.era_cut)
    stat(e1, "before (180s)")
    stat(e2, "after (600s)")

    print("\n=== longest statement-timeout aborts ===")
    for r in sorted(to, key=lambda r: -elapsed(r))[:8]:
        print("   %s  %.0f s" % (r["start"].strftime("%m-%d %H:%M"), elapsed(r)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
