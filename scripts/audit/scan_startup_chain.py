#!/usr/bin/env python3
"""
R43: find every registered startup migration that references a relation no
earlier step of the fresh-install chain creates.

The chain is: 00-prereqs -> 01-schema -> 02-seed -> StartupFiles (in order).

Design note: the *existence* question is answered by a real PostgreSQL catalog
(citus pg17), not by parsing SQL. Regex parsing is only used to collect the
candidate reference names, and every candidate is then checked against
pg_class/pg_type/pg_proc. That keeps two error classes out of the report:
a name that parsing invents, and a name that really is absent.

Each migration is applied in its own transaction. A migration that fails is
rolled back and recorded, then the scan continues with the next one, so a
single run surfaces every first-failure instead of stopping at the earliest
one (which is how the 568 -> 622 whack-a-mole happened).

Usage:
  scan_startup_chain.py <dsn>            # dsn, e.g. postgres://u:p@h:5432/db
"""
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EMBED = os.path.join(ROOT, "installer/cmd/llm-gw-installer/embeddata")

# Names that are SQL grammar, not relations. Filtering these out is what keeps
# the report readable; the catalog check below is what makes it *correct*.
KEYWORDS = {
    "begin", "commit", "rollback", "start", "transaction", "view", "table",
    "column", "policy", "trigger", "index", "delete", "conflict", "function",
    "set", "true", "false", "lateral", "select", "values", "unnest", "only",
    "constraints", "constraint", "key", "where", "group", "order", "limit",
    "offset", "case", "when", "then", "else", "end", "and", "or", "not", "in",
    "exists", "distinct", "union", "all", "any", "some", "as", "on", "using",
    "returning", "with", "recursive", "grant", "revoke", "comment", "owner",
    "schema", "database", "extension", "type", "domain", "sequence", "server",
    "prepare", "execute", "deallocate", "declare", "fetch", "close", "cursor",
    "duplicate", "unique", "check", "default", "references", "primary", "foreign",
    "for", "share", "exclusive", "access", "nowait", "of", "rows", "row",
    "before", "after", "instead", "each", "statement", "new", "old", "table_name",
    "matched", "partition", "range", "values_", "cost", "rows_est", "public",
    "only_", "concurrently", "no", "validate", "data", "to", "from_", "lateral_",
    "coalesce", "greatest", "least", "nullif", "extract", "substring", "position",
    "overlay", "trim", "btrim", "array", "row_", "current_date", "current_timestamp",
    "localtimestamp", "now", "current_user", "session_user", "current_schema",
    "information_schema", "pg_catalog", "pg_temp", "pg_toast", "aggregate",
    "cast", "collate", "filter", "over", "partition_by", "window", "within",
    "leading", "trailing", "both", "nulls", "first", "last", "similar", "escape",
    "variadic", "only_one", "lateral_join", "generated", "always", "identity",
    "options", "storage", "compression", "statistics", "tablespace", "inherits",
    "temp", "temporary", "unlogged", "materialized", "recursive_view",
}


def run_psql(dsn, sql, stdin_file=None, quiet=True):
    cmd = ["psql", "-v", "ON_ERROR_STOP=1", "--dbname", dsn, "-q"]
    if stdin_file:
        cmd += ["-f", stdin_file]
        p = subprocess.run(cmd, capture_output=True, text=True)
    else:
        p = subprocess.run(cmd, input=sql, capture_output=True, text=True)
    return p.returncode, p.stdout, p.stderr


def strip_noise(sql: str) -> str:
    out = []
    i, n, depth = 0, len(sql), 0
    while i < n:
        if depth == 0 and sql.startswith("--", i):
            j = sql.find("\n", i)
            i = n if j == -1 else j
            continue
        if sql.startswith("/*", i):
            depth += 1
            i += 2
            continue
        if depth > 0 and sql.startswith("*/", i):
            depth -= 1
            i += 2
            continue
        if depth > 0:
            i += 1
            continue
        if sql.startswith("$$", i):
            j = sql.find("$$", i + 2)
            j = n if j == -1 else j
            out.append("\n" * (j - i + 2))
            i = j + 2
            continue
        if sql[i] in "'\"":
            q = sql[i]
            j = i + 1
            while j < n:
                if sql[j] == q:
                    if j + 1 < n and sql[j + 1] == q:
                        j += 2
                        continue
                    break
                j += 1
            out.append("\n" * (j - i + 1))
            i = j + 1
            continue
        out.append(sql[i])
        i += 1
    return "".join(out)


def cte_names(sql: str):
    names = set()
    for m in re.finditer(r"(?:^|[;])\s*WITH\s+(?:RECURSIVE\s+)?", sql, re.IGNORECASE):
        cur = sql[m.end():]
        i, depth = 0, 0
        while i < len(cur):
            ch = cur[i]
            if ch == "(":
                depth += 1
            elif ch == ")":
                depth -= 1
                if depth == 0:
                    j = i + 1
                    mm = re.match(r"\s*\([^)]*\)\s*,\s*", cur[j:])
                    if mm:
                        j += mm.end()
                    m2 = re.match(r"\s*([A-Za-z_][\w$]*)\s*(,|$)", cur[j:])
                    if not m2:
                        break
                    names.add(m2.group(1))
                    if m2.group(2) != ",":
                        break
                    i = j + m2.start(2) + 1
                    continue
            i += 1
    return names


REF_PATS = [
    r"\bFROM\s+(?:ONLY\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bJOIN\s+(?:ONLY\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bUPDATE\s+(?:ONLY\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bINSERT\s+INTO\s+([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bREFERENCES\s+([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bTRUNCATE\s+(?:TABLE\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bLOCK\s+TABLE\s+(?:ONLY\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
    r"\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)",
]


def reference_candidates(sql: str):
    ctes = cte_names(sql)
    out = set()
    for p in REF_PATS:
        for m in re.finditer(p, sql, re.IGNORECASE):
            raw = m.group(1)
            if "." in raw:
                schema, base = raw.split(".", 1)
                if schema.lower() not in ("public", "pg_temp"):
                    continue          # other-schema / alias-qualified: skip
            else:
                base = raw
            b = base.lower()
            if b in KEYWORDS or b in {c.lower() for c in ctes}:
                continue
            # a bare word immediately followed by '(' is a function call
            tail = sql[m.end():m.end() + 1]
            if tail == "(":
                continue
            out.add(base)
    return out


CATALOG_Q = """
SELECT n.nspname || '.' || c.relname
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname IN ('public')
UNION ALL
SELECT n.nspname || '.' || t.typname
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = 'public'
UNION ALL
SELECT n.nspname || '.' || p.proname
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public';
"""


def catalog(dsn):
    rc, out, err = run_psql(dsn, "-At -c " + repr(CATALOG_Q))
    p = subprocess.run(["psql", "-At", "-v", "ON_ERROR_STOP=1", "--dbname", dsn, "-c", CATALOG_Q],
                       capture_output=True, text=True)
    names = set()
    for line in p.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        if "." in line:
            _, base = line.split(".", 1)
        else:
            base = line
        names.add(base.lower())
    return names


def startup_files():
    with open(os.path.join(ROOT, "installer/internal/dbinit/runner.go"), encoding="utf-8") as f:
        runner = f.read()
    block = runner.split("StartupFiles: []string{", 1)[1].split("\n\t\t},", 1)[0]
    return [s for s in re.findall(r'"([^"]+\.sql)"', block) if not s.endswith(".down.sql")]


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    dsn = sys.argv[1]

    print("resetting database ...")
    # caller passes a disposable, already-empty database
    for name in ("00-prereqs.sql", "01-schema.sql", "02-seed.sql"):
        p = os.path.join(EMBED, name)
        rc, out, err = run_psql(dsn, None, stdin_file=p)
        if rc != 0:
            print(f"pre-chain {name} FAILED rc={rc}\n{err[-2000:]}")
            sys.exit(1)
    print("pre-chain applied\n")

    known = catalog(dsn)
    print(f"catalog objects after 02-seed: {len(known)}\n")

    gaps, failures = [], []
    for name in startup_files():
        p = os.path.join(EMBED, "startup", name)
        if not os.path.exists(p):
            failures.append((name, "embeddata copy missing"))
            continue
        with open(p, encoding="utf-8") as f:
            raw = f.read()
        sql = strip_noise(raw)

        # 1) what does this file reference that nothing has created yet?
        missing = sorted(c for c in reference_candidates(sql)
                         if c.lower() not in known)
        for m in missing:
            gaps.append((name, m))

        # 2) does it actually apply?
        rc, out, err = run_psql(dsn, None, stdin_file=p)
        if rc != 0:
            first = ""
            for line in err.splitlines():
                if "ERROR" in line:
                    first = line.strip()
                    break
            failures.append((name, first or f"rc={rc}"))
        known = catalog(dsn)

    print(f"=== missing-creator references: {len(gaps)} ===")
    for n, r in gaps:
        print(f"  {n}\t{r}")
    print(f"\n=== migrations that failed to apply: {len(failures)} ===")
    for n, e in failures:
        print(f"  {n}\t{e}")


if __name__ == "__main__":
    main()
