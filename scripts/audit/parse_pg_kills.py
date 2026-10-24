#!/usr/bin/env python3
"""Parse a podman conmon `ctr.log` (or any PG text log) and report statements
that PostgreSQL killed, plus per-view REFRESH cost.

Why this exists (§143.26, runbook `2026-10-04-ursm-write-amplification-measurement.md`):

`pg_stat_statements` only records **successful** statements, so every statement
killed by `statement_timeout` (or cancelled by a context deadline) is invisible
to it.  A prior claim in this project — "about 32-33 REFRESH statements per day
are killed by statement_timeout" — was **wrong**: it was derived by grepping for
the error line and looking at the surrounding *context window*, which contained
REFRESH lines belonging to **other** statements.

This parser reconstructs PostgreSQL log **records** (a record may span several
physical lines, e.g. a multi-line `STATEMENT:` body) and then attributes each
kill to the statement that actually produced it.

§143.104 (2026-10-24): oversized single lines are also delivered as
conmon `stderr P` partial chunks; those are folded into the open record
(see CONTAINER_PREFIX_PARTIAL) instead of breaking it.

Usage:
    python3 parse_pg_kills.py <ctrlog> [<ctrlog> ...]

Options:
    --refresh-only     only print the per-view REFRESH cost table
    --kills-only       only print the kill inventory

Read-only: opens files in text mode, writes nothing.
"""

import argparse
import re
import sys
from collections import OrderedDict, defaultdict

# A new PostgreSQL log record looks like:
#   2026-10-19 23:49:18.905 CST [1648027] ERROR:  canceling statement due to ...
PG_REC = re.compile(
    r"^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+ [A-Z]+) \[(\d+)\] ([A-Z]+):  (.*)$"
)
# The container log wraps every line: "<iso ts> stderr F <pg text>"
CONTAINER_PREFIX = "stderr F "
# ★ §143.104（2026-10-24）: journald/conmon split oversized single lines into
# "<iso ts> stderr P <chunk>" pieces (~8 KiB each). A multi-MB statement (e.g. a
# session_turns mirror INSERT carrying a giant inlined prompt) arrives as a run
# of P chunks, so treating "no stderr F tag" as "end of record" (a) dropped the
# chunk bytes and (b) broke the open record, orphaning every following
# continuation line. Measured on 252's ctrlog-20261023.log: 529.7 MB file,
# 270.7 MB in P chunks + 142.2 MB in orphaned continuations — i.e. most of the
# day was invisible to the F-only reader.
CONTAINER_PREFIX_PARTIAL = "stderr P "
# LOG:  duration: 1234.567 ms  statement: <sql>
DURATION = re.compile(
    r"^duration: ([0-9.]+) ms\s+(statement|execute [^:]*):\s*(.*)$", re.S
)
REFRESH = re.compile(r"REFRESH MATERIALIZED VIEW (?:CONCURRENTLY )?([A-Za-z0-9_]+)")
# ★ Attribution must be based on the statement's **syntax head**, never on
#   "the body happens to contain the word REFRESH".  A psql session can paste a
#   runbook excerpt into a query; that text lands in the same STATEMENT record
#   and would otherwise masquerade as a REFRESH kill (§143.26.2, runbook).
REFRESH_HEAD = re.compile(
    r"^\s*REFRESH\s+MATERIALIZED\s+VIEW\s+(?:CONCURRENTLY\s+)?([A-Za-z0-9_]+)",
    re.IGNORECASE,
)
STATEMENT_HEAD = re.compile(r"^\s*([A-Za-z_]+)")


def refresh_view(stmt):
    """Return the matview name iff the statement **is** a REFRESH, else None."""
    m = REFRESH_HEAD.match(stmt or "")
    return m.group(1) if m else None


def statement_kind(stmt):
    """Return the leading SQL keyword of a statement (for head-based attribution)."""
    m = STATEMENT_HEAD.match(stmt or "")
    return m.group(1).upper() if m else ""



def iter_records(path):
    """Yield [ts, pid, level, text] with continuation lines folded into their record."""
    rec = None
    with open(path, "r", errors="replace") as fh:
        for raw in fh:
            idx_f = raw.find(CONTAINER_PREFIX)
            idx_p = raw.find(CONTAINER_PREFIX_PARTIAL)
            if idx_f < 0 and idx_p < 0:
                # Not a PG line (or a different stream): end any open record.
                if rec is not None:
                    yield rec
                    rec = None
                continue
            if idx_f >= 0:
                line = raw[idx_f + len(CONTAINER_PREFIX):].rstrip("\n")
                m = PG_REC.match(line)
                if m:
                    if rec is not None:
                        yield rec
                    rec = list(m.groups())
                elif rec is not None:
                    # Continuation of the previous record (statement body).
                    rec[3] += "\n" + line
                # else: orphan full line before any record — nothing to fold into.
            else:
                # ★ stderr P chunk: a mid-line piece of one oversized physical
                # line. Two cases (§143.105):
                #   (a) the chunk BEGINS a new PG record — this happens when the
                #      record's own first physical line is itself oversized (its
                #      first piece arrives as P). PG_REC matched at chunk
                #      position 0 starts a new record.
                #   (b) anything else — including a PG-looking line pasted
                #      MID-statement (§143.26's poisoned-STATEMENT family,
                #      which lands mid-chunk) — belongs to the open record and
                #      must never split it. Chunks are byte pieces of the same
                #      line, so concatenate raw.
                chunk = raw[idx_p + len(CONTAINER_PREFIX_PARTIAL):].rstrip("\n")
                m = PG_REC.match(chunk)
                if m:
                    if rec is not None:
                        yield rec
                    rec = list(m.groups())
                elif rec is not None:
                    rec[3] += chunk
    if rec is not None:
        yield rec


def norm(text):
    return " ".join(text.split())


def collect(paths):
    per_view = defaultdict(list)
    kills = []

    for path in paths:
        recs = list(iter_records(path))
        for i, (ts, pid, level, text) in enumerate(recs):
            dm = DURATION.match(text)
            if dm:
                # duration lines look like: "duration: N ms  statement: <sql>"
                view = refresh_view(dm.group(3).split("statement:", 1)[-1])
                if view:
                    per_view[view].append(float(dm.group(1)))

            if level != "ERROR":
                continue
            if "canceling statement due to statement timeout" in text:
                kind = "statement_timeout"
            elif "canceling statement due to user request" in text:
                kind = "user_request"
            else:
                continue

            # Attribute the kill to the statement that produced it: the
            # STATEMENT: record that immediately follows this ERROR record.
            stmt = ""
            if i + 1 < len(recs) and recs[i + 1][2] == "STATEMENT":
                stmt = recs[i + 1][3]
            elif i + 1 < len(recs) and recs[i + 1][2] == "LOG":
                stmt = recs[i + 1][3]
            kills.append({"ts": ts, "pid": pid, "kind": kind, "stmt": norm(stmt)})

    return per_view, kills


def print_refresh(per_view):
    print("############ A. REFRESH cost per view (from LOG 'duration:' lines) ############")
    if not per_view:
        print("  (no REFRESH duration lines found)")
        return
    print("%-32s %6s %10s %10s %10s %12s" % ("view", "n", "min_ms", "median_ms", "max_ms", "total_s"))
    total_ms = 0.0
    total_n = 0
    for view, vals in sorted(per_view.items(), key=lambda kv: -sum(kv[1])):
        vals.sort()
        total_ms += sum(vals)
        total_n += len(vals)
        print("%-32s %6d %10.0f %10.0f %10.0f %12.1f" % (
            view, len(vals), vals[0], vals[len(vals) // 2], vals[-1], sum(vals) / 1000.0))
    print("%-32s %6d %10s %10s %10s %12.1f" % ("TOTAL", total_n, "", "", "", total_ms / 1000.0))
    print()
    print("  NOTE: 'duration:' lines only exist for statements slower than")
    print("        log_min_duration_statement (252 prod = 1000 ms) => this is a")
    print("        TAIL SAMPLE, not a total. Use pg_stat_statements for totals.")


def print_kills(kills):
    print("############ B. kills that hit a REFRESH ############")
    n_to = n_ur = 0
    for k in kills:
        view = refresh_view(k["stmt"])
        if not view:
            continue
        if k["kind"] == "statement_timeout":
            n_to += 1
        else:
            n_ur += 1
        print("  %s  %-18s %s" % (k["ts"], k["kind"], view))
    print("  => REFRESH killed by statement timeout: %d ; by user request: %d" % (n_to, n_ur))
    print("  (attribution is by the statement's SYNTAX HEAD, not by 'body contains REFRESH')")
    print()

    print("############ C. ALL statement_timeout kills, grouped by fingerprint ############")
    fp = OrderedDict()
    for k in kills:
        if k["kind"] != "statement_timeout":
            continue
        key = k["stmt"][:90] or "<empty>"
        fp[key] = fp.get(key, 0) + 1
    print("  total statement_timeout kills: %d" % sum(fp.values()))
    for key, n in sorted(fp.items(), key=lambda kv: -kv[1]):
        print("  %-5d %s" % (n, key))
    print()

    print("############ D. user_request kills (context cancellation) ############")
    fpu = OrderedDict()
    for k in kills:
        if k["kind"] == "user_request":
            key = k["stmt"][:90] or "<empty>"
            fpu[key] = fpu.get(key, 0) + 1
    print("  total user_request kills: %d" % sum(fpu.values()))
    for key, n in sorted(fpu.items(), key=lambda kv: -kv[1])[:15]:
        print("  %-5d %s" % (n, key))


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("logs", nargs="+", help="PG text logs (podman ctr.log or PG logfile)")
    ap.add_argument("--refresh-only", action="store_true")
    ap.add_argument("--kills-only", action="store_true")
    args = ap.parse_args()

    per_view, kills = collect(args.logs)

    if not args.kills_only:
        print_refresh(per_view)
    if not args.refresh_only:
        print_kills(kills)
    return 0


if __name__ == "__main__":
    sys.exit(main())
