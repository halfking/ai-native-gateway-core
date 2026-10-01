#!/usr/bin/env python3
"""Round 44 §9-4: characterise every remaining red test individually.

§9-4 asks for each remaining red to be classified, rather than leaving
"14 red" as an unowned aggregate. Two independent axes are used, and a test is
only assigned a family when there is evidence for it:

  axis 1 — the error itself: SQLSTATE when the server reported one, otherwise
           the first attributed error line.
  axis 2 — where the test gets its database. This is decided by reading the
           test's own source, not by its name:
             GATE_DSN      reads TEST_PG_URL / TEST_DATABASE_URL / TEST_DB_URL
             OWN_CONTAINER starts its own testcontainer (postgres.Run)
             HARDCODE_DSN  embeds a postgres:// literal
             UNKNOWN       none of the above

Axis 2 is what separates "the test needs a schema" from "the test needs
production-shaped DATA" from "the test never talks to the gate database at
all". Those three need different owners, and a name like
TestAutoRouteSettle* matching several entries proves nothing about any of them.
"""
import os
import re
import sys
import glob
import subprocess
from collections import Counter, defaultdict

if len(sys.argv) < 2:
    sys.exit("usage: classify-red-tests.py <sweep-dir> [repo-root]\n"
             "  <sweep-dir> is a directory of per-package *.log files as written by\n"
             "  scripts/audit/run-integration-gate.sh (GATE_LOG=<sweep-dir>/<pkg>.log).")
OUT = sys.argv[1]
REPO = sys.argv[2] if len(sys.argv) > 2 else os.getcwd()

RUN = re.compile(r"^=== RUN\s+(\S+)")
FAIL = re.compile(r"^\s*--- FAIL:\s+(\S+)")
ERRLINE = re.compile(r"^\s+([\w./-]+\.go:\d+):\s+(.*)$")
SQLSTATE = re.compile(r"\(SQLSTATE ([0-9A-Z]{5})\)")
ERRCLASS = re.compile(r"\b(ERROR|FATAL|panic):\s*(.*)")

GATE_VARS = ("TEST_PG_URL", "TEST_DATABASE_URL", "TEST_DB_URL", "LLM_GATEWAY_PG_URL",
             "DATABASE_URL", "LLM_GATEWAY_ROLLUP_TEST_DSN", "LLM_GATEWAY_FEATURE_STATS_TEST_DSN")


def source_index():
    """Map every Test function name to (file, body) across the repo."""
    idx = {}
    out = subprocess.run(
        ["grep", "-rn", "--include=*_test.go", r"^func Test", REPO],
        capture_output=True, text=True).stdout
    for line in out.splitlines():
        m = re.match(r"^(.+?):(\d+):func (Test\w+)\(", line)
        if m:
            idx[m.group(3)] = (m.group(1), int(m.group(2)))
    return idx


SRC = source_index()


def dsn_source(name):
    base = name.split("/")[0]
    hit = SRC.get(base)
    if not hit:
        return "UNKNOWN", ""
    path, ln = hit
    try:
        body = open(path, errors="replace").read()
    except OSError:
        return "UNKNOWN", path
    # Search the WHOLE file, not a window starting at the test function. Several
    # tests read their DSN in a helper defined earlier in the same file (e.g.
    # fault/fault_integration_test.go:29 and licensing:28 both read
    # LLM_GATEWAY_PG_URL from a helper above the Test function). A window from
    # the test function onward reported those 26 tests as NO_DSN, which is the
    # detector being wrong, not the tests being wrong.
    window = body
    gates = [v for v in GATE_VARS if ('Getenv("%s")' % v) in body]
    if gates:
        return "GATE_DSN", ",".join(gates[:2])
    if "postgres.Run(" in window or "testcontainers.GenericContainer(" in window:
        return "OWN_CONTAINER", os.path.relpath(path, REPO)
    if re.search(r"postgres(ql)?://[^\"'`\s]*", window):
        return "HARDCODE_DSN", os.path.relpath(path, REPO)
    return "NO_DSN", os.path.relpath(path, REPO)


def parse(path):
    tests, current, pending = {}, None, None
    with open(path, errors="replace") as fh:
        for line in fh:
            line = line.rstrip("\n")
            m = RUN.match(line)
            if m:
                if current and pending:
                    tests.setdefault(current, []).append(pending)
                current, pending = m.group(1), None
                continue
            m = FAIL.match(line)
            if m:
                if pending:
                    tests.setdefault(current or m.group(1), []).append(pending)
                current, pending = None, None
                continue
            m = ERRLINE.match(line)
            if m and current:
                pending = (m.group(1), m.group(2))
    if current and pending:
        tests.setdefault(current, []).append(pending)
    reds = []
    with open(path, errors="replace") as fh:
        for line in fh:
            m = FAIL.match(line)
            if m:
                reds.append(m.group(1))
    # A parent FAIL and its subtest FAIL are ONE red, not two.
    roots, seen = [], set()
    for r in sorted(set(reds)):
        root = r.split("/")[0]
        if root in seen:
            continue
        seen.add(root)
        roots.append(root)
    return roots, tests


def classify(text):
    m = SQLSTATE.search(text)
    if m:
        return m.group(1), text.strip()[:130]
    m = ERRCLASS.search(text)
    if m:
        return "NO-SQLSTATE", (m.group(1) + ": " + m.group(2)).strip()[:130]
    return "UNCLASSIFIED", text.strip()[:130]


rows = []
by_code, by_dsn, by_pkg = Counter(), Counter(), defaultdict(list)

for log in sorted(glob.glob(os.path.join(OUT, "*.log"))):
    pkg = os.path.basename(log)[:-4]
    reds, tests = parse(log)
    for name in reds:
        entries = tests.get(name, [])
        if entries:
            loc, text = entries[0]
            code, cause = classify(text)
        else:
            loc, code, cause = "", "UNCLASSIFIED", "no error line captured for this test"
        dsnsrc, where = dsn_source(name)
        by_code[code] += 1
        by_dsn[dsnsrc] += 1
        by_pkg[pkg].append(name)
        rows.append((pkg, name, code, dsnsrc, where, cause))

w = max((len(r[1]) for r in rows), default=10)
print(f"{'PACKAGE':<26} {'TEST':<{w}}  {'SQLSTATE':<12} {'DSN SOURCE':<14} CAUSE")
print("-" * 165)
for pkg, name, code, dsnsrc, where, cause in rows:
    print(f"{pkg:<26} {name:<{w}}  {code:<12} {dsnsrc:<14} {cause}")

print("\n" + "=" * 165)
print(f"total red TESTS (subtests folded into their parent): {len(rows)}")
print("\nby SQLSTATE / class:")
for c, n in by_code.most_common():
    print(f"  {c:<14} {n}")
print("\nby DSN source:")
for c, n in by_dsn.most_common():
    print(f"  {c:<14} {n}")
print("\nby package:")
for p in sorted(by_pkg):
    print(f"  {p:<26} {len(by_pkg[p])}")
