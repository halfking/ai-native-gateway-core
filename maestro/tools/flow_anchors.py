"""flow_anchors.py -- shared anchor-resolution library for Maestro flow audits.

WHY THIS MODULE EXISTS
----------------------
A Maestro flow selects an element with `id: "xxx"`.  For the assertion to ever
succeed on a real device, that id must exist in the front-end.  If it does
not, the assertion can never succeed -- and it fails *silently* (the flow
report says "element not found", which is easily misread as "the page is
broken" instead of "the gauge is wrong").

Two audit tools need the exact same answer to the same question:

  * audit-maestro-id-selectors.py  -- "is the id shadowed by title/aria-label?"
  * audit-flow-anchor-existence.py -- "does the id exist at all?"

Writing the resolution rules twice would let them drift, so they live here,
once.  (Same discipline as the L34 round 7/8 shared-library extraction.)

SOURCE ENCODING
---------------
This module is deliberately pure ASCII.  On this machine a .py file that
contains very long non-ASCII lines has been observed to be rejected by
python3 as "Non-UTF-8 code".  Human-readable CJK notes belong in the .sh
gate or in the audit report, not in this module.

WHERE AN ANCHOR ID MAY COME FROM (all three occur in this repository)
---------------------------------------------------------------------
1. static `data-testid="literal"`
2. bound `:data-testid="..."`:
   - template literal  :data-testid="`prefix-${expr}`"  -> becomes a pattern
   - plain expression  :data-testid="expr"              -> quoted literals
                          inside expr are concrete ids (e.g. a ternary)
3. plain HTML `id="literal"` -- an Android WebView exposes the HTML id
   attribute as the accessibility resource-id, and Maestro's `id:` matches
   the resource-id.

EXPLICITLY NOT IMPLEMENTED
--------------------------
* Native Android flows (they carry `appId:`) are skipped: their ids are
  Android resource-ids, not web anchors.
* `text:` selectors are out of scope: they are a different problem
  (locale coupling), not an anchor-existence problem.
"""

import glob
import os
import re

FLOW_GLOB_REL = "maestro/flows/**/*.yaml"
VUE_GLOB_REL = "web/src/**/*.vue"

# An id key in a flow file: the key starts a line (optionally indented, and
# optionally introduced by a YAML sequence dash).  Anchoring on the line start
# keeps `resource-id:` (and any other *-id key) out of the candidate set.
FLOW_ID_RE = re.compile(r"(?m)^[ \t]*(?:-[ \t]+)?id:[ \t]*\"?([A-Za-z0-9_-]+)\"?")

# A start tag, possibly spanning several lines.
TAG_RE = re.compile(r"<[A-Za-z][^<>]*?>", re.S)

# Static data-testid (the `:` prefix of a bound attribute is excluded).
STATIC_TESTID_RE = re.compile(r"(?<![:\w-])data-testid[ \t]*=[ \t]*\"([^\"$]+)\"")

# Bound data-testid, three quote flavours.
BOUND_TESTID_RE = re.compile(
    r":data-testid[ \t]*=[ \t]*(?:\"([^\"]*)\"|'([^']*)'|`([^`]*)`)"
)

# Plain HTML id attribute (not `:id`, not the tail of `data-testid`).
HTML_ID_RE = re.compile(r"(?<![:\w-])id[ \t]*=[ \t]*\"([^\"]+)\"")

# Quoted literals inside a bound expression.
QUOTED_LITERAL_RE = re.compile(r"'([^']*)'|\"([^\"]*)\"")


def strip_comment_lines(text):
    """Drop whole-line comments (leading whitespace + #).

    A comment line may mention an id; that is not a selector.
    """
    return "\n".join(l for l in text.split("\n") if not re.match(r"^\s*#", l))


def normalize_template(expr):
    """Normalize a template literal into a glob-ish pattern.

    `${anything}` becomes `*`.  Example:
      `kx-dr-preset-${preset.id}`  ->  `kx-dr-preset-*`
    """
    return re.sub(r"\$\{[^}]*\}", "*", expr)


def pattern_to_regex(pattern):
    """Compile a normalized pattern into an anchored regex."""
    escaped = re.escape(pattern).replace(r"\*", ".*")
    return re.compile("^" + escaped + "$")


def build_anchor_index(root):
    """Scan the front-end and return the anchor index.

    Returns a dict with:
      data_testid        : {id: (file_rel, start_tag)}
      html_id            : {id: (file_rel, start_tag)}
      bound_literal      : {id: (file_rel, start_tag)}
      patterns           : [(pattern_str, file_rel, start_tag)]
      unresolved_dynamic : [(file_rel, expr)]
    """
    data_testid = {}
    html_id = {}
    bound_literal = {}
    patterns = []
    unresolved_dynamic = []

    for path in sorted(glob.glob(os.path.join(root, VUE_GLOB_REL), recursive=True)):
        try:
            with open(path, encoding="utf-8") as fh:
                src = fh.read()
        except OSError:
            continue
        rel = os.path.relpath(path, root).replace(os.sep, "/")
        for m in TAG_RE.finditer(src):
            tag = m.group(0)
            for tid in STATIC_TESTID_RE.findall(tag):
                data_testid.setdefault(tid, (rel, tag))
            for hid in HTML_ID_RE.findall(tag):
                html_id.setdefault(hid, (rel, tag))
            for quoted in BOUND_TESTID_RE.findall(tag):
                expr = quoted[0] if quoted[0] != "" else (
                    quoted[1] if quoted[1] != "" else quoted[2]
                )
                if expr.startswith("`") and expr.endswith("`"):
                    patterns.append(
                        (normalize_template(expr[1:-1]), rel, tag)
                    )
                    continue
                literals = [a if a != "" else b for a, b in QUOTED_LITERAL_RE.findall(expr)]
                if literals:
                    for lit in literals:
                        bound_literal.setdefault(lit, (rel, tag))
                else:
                    # A bound expression with no literal inside (e.g.
                    # `:data-testid="testId"`) cannot be resolved statically.
                    unresolved_dynamic.append((rel, expr))

    return {
        "data_testid": data_testid,
        "html_id": html_id,
        "bound_literal": bound_literal,
        "patterns": patterns,
        "unresolved_dynamic": unresolved_dynamic,
    }


def is_native_flow(text):
    """True when the flow targets a native app (Android) rather than the web app."""
    return re.search(r"(?m)^[ \t]*appId:", text) is not None


def collect_flow_selectors(root):
    """Return (selectors, skipped_native_flows).

    selectors       : [(flow_rel, anchor_id)]
    skipped_native  : [flow_rel]
    """
    selectors = []
    skipped = []
    for path in sorted(glob.glob(os.path.join(root, FLOW_GLOB_REL), recursive=True)):
        rel = os.path.relpath(path, root).replace(os.sep, "/")
        base = os.path.basename(rel)
        if base.startswith("_tmp-"):
            continue  # temporary probes are not part of the contract
        try:
            with open(path, encoding="utf-8") as fh:
                raw = fh.read()
        except OSError:
            continue
        code = strip_comment_lines(raw)
        if is_native_flow(code):
            skipped.append(rel)
            continue
        for aid in sorted(set(FLOW_ID_RE.findall(code))):
            selectors.append((rel, aid))
    return selectors, skipped


def resolve_anchor(anchor_id, index):
    """Resolve one anchor id against the index.

    Returns (ok: bool, source: str).
    source is one of: data-testid / html-id / bound-literal / pattern / missing
    """
    if anchor_id in index["data_testid"]:
        return True, "data-testid"
    if anchor_id in index["html_id"]:
        return True, "html-id"
    if anchor_id in index["bound_literal"]:
        return True, "bound-literal"
    for pattern, _rel, _tag in index["patterns"]:
        if pattern_to_regex(pattern).match(anchor_id):
            return True, "pattern"
    return False, "missing"
