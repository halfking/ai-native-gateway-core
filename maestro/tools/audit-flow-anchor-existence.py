#!/usr/bin/env python3
"""audit-flow-anchor-existence.py -- does every Maestro flow `id:` exist in the web app?

WHY THIS TOOL EXISTS
--------------------
A Maestro flow asserts on an element with `id: "xxx"`.  If the front-end has no
such anchor, the assertion can never succeed on a real device.  The failure is
*silent*: the flow report says "element not found", which is easily misread as
"the page is broken" instead of "the gauge points at nothing".

This is the **offline half** of the session-analytics E2E contract.  The
online half (drive the flow on a device) needs node, a browser and a running
gateway, so it cannot run in the CI carrier.  This half needs python3 only.

WHAT IS CHECKED
---------------
For every `maestro/flows/**/*.yaml` (native Android flows excluded, they carry
`appId:` and their ids are Android resource-ids):

    every `id:` selector  ->  must resolve against the front-end

Resolution sources (see flow_anchors.py for the full list):
  * static `data-testid="literal"`
  * bound `:data-testid="..."` (template literal -> pattern; plain expression
    -> quoted literals inside it)
  * plain HTML `id="literal"`

EXIT CODES
----------
  0  every flow anchor resolves
  1  at least one anchor does not resolve (or the allowlist is stale/empty)
  2  tool error (including "scanned nothing", which would make the gate
     vacuously green)

USAGE
-----
  python3 maestro/tools/audit-flow-anchor-existence.py
  python3 maestro/tools/audit-flow-anchor-existence.py --json
  python3 maestro/tools/audit-flow-anchor-existence.py --root=<dir>
  python3 maestro/tools/audit-flow-anchor-existence.py --min-flows=1 --min-anchors=1
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import flow_anchors  # noqa: E402  (path must be set first)

ALLOWLIST_REL = "maestro/flow-anchor-allowlist.tsv"

# A scan that finds nothing is a broken scan, not a clean bill of health.
MIN_FLOWS = 5
MIN_ANCHORS = 10


def repo_root():
    for arg in sys.argv[1:]:
        if arg.startswith("--root="):
            return os.path.abspath(arg[len("--root="):])
    return os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def int_arg(name, default):
    """Read --<name>=<int>; an unparsable value is a tool error, not a silent default."""
    prefix = "--" + name + "="
    for arg in sys.argv[1:]:
        if arg.startswith(prefix):
            try:
                return int(arg[len(prefix):])
            except ValueError:
                sys.stderr.write("tool error: %s must be an integer\n" % prefix)
                sys.exit(2)
    return default


def read_allowlist(root):
    """Read the allowlist: rows of flow_rel <TAB> anchor_id <TAB> reason."""
    path = os.path.join(root, ALLOWLIST_REL)
    entries = []
    if not os.path.exists(path):
        return entries
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            raw = line.rstrip("\n")
            if not raw.strip() or raw.lstrip().startswith("#"):
                continue
            parts = raw.split("\t")
            if len(parts) < 3:
                entries.append({
                    "line": lineno,
                    "flow": parts[0] if parts else "",
                    "id": parts[1] if len(parts) > 1 else "",
                    "reason": "",
                    "malformed": True,
                })
                continue
            entries.append({
                "line": lineno,
                "flow": parts[0].strip(),
                "id": parts[1].strip(),
                "reason": parts[2].strip(),
                "malformed": False,
            })
    return entries


def run():
    root = repo_root()
    index = flow_anchors.build_anchor_index(root)
    selectors, skipped_native = flow_anchors.collect_flow_selectors(root)

    flows = sorted(set(f for f, _ in selectors))
    unresolved = []
    resolved = 0
    for flow, anchor in selectors:
        ok, source = flow_anchors.resolve_anchor(anchor, index)
        if ok:
            resolved += 1
        else:
            unresolved.append({"flow": flow, "id": anchor})

    allowlist = read_allowlist(root)
    unresolved_keys = set((u["flow"], u["id"]) for u in unresolved)
    stale = []
    for entry in allowlist:
        key = (entry["flow"], entry["id"])
        if entry["malformed"] or not entry["reason"]:
            stale.append({"entry": entry, "why": "malformed-or-no-reason"})
        elif key not in unresolved_keys:
            stale.append({"entry": entry, "why": "no-longer-needed"})

    tool_errors = []
    min_flows = int_arg("min-flows", MIN_FLOWS)
    min_anchors = int_arg("min-anchors", MIN_ANCHORS)
    if len(flows) < min_flows:
        tool_errors.append(
            "only %d flow(s) with anchors scanned (min %d) => scan surface broken"
            % (len(flows), min_flows)
        )
    if len(selectors) < min_anchors:
        tool_errors.append(
            "only %d anchor(s) scanned (min %d) => scan surface broken"
            % (len(selectors), min_anchors)
        )

    # An allowlisted id must actually be a real (flow, id) pair.
    known_keys = set(selectors)
    for entry in allowlist:
        if entry["malformed"]:
            continue
        if (entry["flow"], entry["id"]) not in known_keys:
            stale.append({"entry": entry, "why": "ghost-entry"})

    fail = bool(unresolved) or bool(stale) or bool(tool_errors)

    if "--json" in sys.argv:
        print(json.dumps({
            "flows": len(flows),
            "anchors": len(selectors),
            "resolved": resolved,
            "unresolved": unresolved,
            "skippedNativeFlows": skipped_native,
            "allowlist": allowlist,
            "staleAllowlist": stale,
            "toolErrors": tool_errors,
            "pass": not fail,
        }, ensure_ascii=False, indent=2))
    else:
        print("# Maestro flow anchor existence audit")
        print("flows with anchors: %d / anchors: %d / resolved: %d"
              % (len(flows), len(selectors), resolved))
        if skipped_native:
            print("native (appId:) flows skipped: %d" % len(skipped_native))
        if unresolved:
            print("")
            print("MISSING anchors (flow asserts an id the front-end does not have):")
            for u in unresolved:
                print("  - %s -> id: %s" % (u["flow"], u["id"]))
        if stale:
            print("")
            print("STALE allowlist entries:")
            for s in stale:
                e = s["entry"]
                print("  - %s line %d (%s): %s"
                      % (ALLOWLIST_REL, e.get("line", 0), s["why"],
                         "%s -> %s" % (e.get("flow", ""), e.get("id", ""))))
        if tool_errors:
            print("")
            print("TOOL ERRORS:")
            for t in tool_errors:
                print("  - %s" % t)
        print("")
        print("RESULT: %s" % ("PASS" if not fail else "FAIL"))
    return 2 if tool_errors else (1 if fail else 0)


if __name__ == "__main__":
    sys.exit(run())
