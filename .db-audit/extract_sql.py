#!/usr/bin/env python3
"""Extract every SQL statement from non-test Go sources into a catalog.

Catches:
  - backtick raw strings containing SQL keywords
  - double-quoted strings containing SQL keywords (incl. Sprintf'd fragments)
Each hit recorded with file, line, kind (SELECT/INSERT/UPDATE/DELETE/WITH/DDL),
tables referenced (regex over FROM/INTO/UPDATE/JOIN), and flags for common
anti-patterns. Output: tmp/sql_catalog.jsonl + per-module summary.
"""
import json
import re
import sys
from collections import Counter, defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = Path(__file__).parent / "tmp"

SQL_START = re.compile(
    r"\b(SELECT\s|INSERT\s+INTO\s|UPDATE\s+\w|DELETE\s+FROM\s|WITH\s+\w+\s+AS\s*\(|"
    r"CREATE\s+(?:TABLE|INDEX|UNIQUE|SCHEMA|VIEW|MATERIALIZED|POLICY|TYPE|EXTENSION|SEQUENCE)|"
    r"ALTER\s+TABLE|DROP\s+TABLE|TRUNCATE|VACUUM|ANALYZE|REFRESH\s+MATERIALIZED|"
    r"LOCK\s+TABLE|COPY\s+\w|COMMENT\s+ON|GRANT\s|REVOKE\s|SET\s+LOCAL|SET\s+search_path|"
    r"BEGIN|COMMIT|ROLLBACK|SAVEPOINT|NOTIFY|LISTEN|PREPARE|EXPLAIN)\b", re.I)

TABLE_OF = re.compile(
    r"(?:\bFROM\s+|\bINTO\s+|\bUPDATE\s+|\bJOIN\s+|\bDELETE\s+FROM\s+|\bTRUNCATE\s+(?:TABLE\s+)?)"
    r"((?:public\.)?[a-z_][\w\.]*)", re.I)

def classify(sql: str) -> str:
    s = sql.strip().upper()
    for kw, kind in [("SELECT", "SELECT"), ("WITH", "WITH"), ("INSERT", "INSERT"),
                     ("UPDATE", "UPDATE"), ("DELETE", "DELETE")]:
        if s.startswith(kw):
            return kind
    for kw in ("CREATE", "ALTER", "DROP", "TRUNCATE", "VACUUM", "REFRESH", "COMMENT", "GRANT", "REVOKE", "LOCK", "COPY"):
        if s.startswith(kw):
            return "DDL/ADMIN"
    if "SELECT" in s:
        return "SELECT*"
    return "OTHER"

FLAG_PATTERNS = {
    "select_star": re.compile(r"SELECT\s+\*", re.I),
    "no_where_update_delete": re.compile(r"^\s*(UPDATE|DELETE)(?!.*\bWHERE\b)", re.I | re.S),
    "fn_on_predicate": re.compile(r"WHERE[^\n]*\b(UPPER|LOWER|DATE|COALESCE|NOW|DATE_TRUNC|TO_CHAR|CAST|::)\s*\(", re.I),
    "not_in": re.compile(r"NOT\s+IN\s*\(\s*SELECT", re.I),
    "offset": re.compile(r"OFFSET\s+", re.I),
    "select_no_limit": re.compile(r"^\s*SELECT\b.*", re.I | re.S),
    "or_condition": re.compile(r"\bOR\b", re.I),
    "like_wildcard": re.compile(r"LIKE\s+'%[^']*%'", re.I),
    "ilike_wildcard": re.compile(r"ILIKE\s+'%[^']*%'", re.I),
    "distinct_on": re.compile(r"DISTINCT\s+ON", re.I),
    "subquery_in_from": re.compile(r"FROM\s*\(", re.I),
    "order_by_no_limit": re.compile(r"ORDER\s+BY", re.I),
    "sprintf": re.compile(r"%[sdvfqL]|\%\(w", re.I),
    "for_update": re.compile(r"FOR\s+UPDATE", re.I),
    "on_conflict": re.compile(r"ON\s+CONFLICT", re.I),
    "cte": re.compile(r"\bWITH\s+\w+\s+AS\s*\(", re.I),
    "union": re.compile(r"\bUNION\b", re.I),
    "window": re.compile(r"\bOVER\s*\(", re.I),
    "lock_timeout": re.compile(r"SET\s+LOCAL\s+lock_timeout", re.I),
    "advisory": re.compile(r"pg_(?:advisory|try_advisory)", re.I),
}

def norm(s: str) -> str:
    return re.sub(r"\s+", " ", s).strip()

def main():
    records = []
    go_files = [p for p in ROOT.rglob("*.go")
                if "vendor" not in p.parts and not p.name.endswith("_test.go")
                and "testdata" not in p.parts]
    for p in go_files:
        try:
            text = p.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        rel = p.relative_to(ROOT).as_posix()
        # --- backtick raw strings ---
        for m in re.finditer(r"`([^`]*)`", text, re.S):
            body = m.group(1)
            if not SQL_START.search(body):
                continue
            line = text.count("\n", 0, m.start()) + 1
            records.append((rel, line, body))
        # --- double-quoted strings ---
        for m in re.finditer(r'"((?:[^"\\\n]|\\.)*)"', text):
            body = m.group(1)
            if not SQL_START.search(body) or len(body) < 12:
                continue
            # skip pure format strings without SQL verbs we already matched
            line = text.count("\n", 0, m.start()) + 1
            records.append((rel, line, body))

    cat = []
    for rel, line, body in records:
        kind = classify(body)
        tables = sorted({t.removeprefix("public.") for t in TABLE_OF.findall(body)
                         if not t.lower().startswith(("pg_", "information_schema", "(", "unnest", "generate_series", "lateral"))})
        flags = [k for k, rx in FLAG_PATTERNS.items()
                 if rx.search(body) and not (k == "select_no_limit" and re.search(r"\bLIMIT\b|\bFOR UPDATE\b|count\(|RETURNING", body, re.I))]
        cat.append({"file": rel, "line": line, "kind": kind,
                    "tables": tables, "flags": flags, "sql": norm(body)[:1200]})

    OUT.mkdir(parents=True, exist_ok=True)
    with (OUT / "sql_catalog.jsonl").open("w", encoding="utf-8") as f:
        for r in cat:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    by_kind = Counter(r["kind"] for r in cat)
    flag_count = Counter(fl for r in cat for fl in r["flags"])
    mod_counter = defaultdict(Counter)
    for r in cat:
        module = r["file"].split("/")[0]
        mod_counter[module][r["kind"]] += 1
    print(f"total_statements={len(cat)}")
    print("by_kind:", dict(by_kind))
    print("flags:", dict(flag_count))
    print("\ntop modules:")
    for mod, c in sorted(mod_counter.items(), key=lambda kv: -sum(kv[1].values()))[:25]:
        print(f"  {mod:30s} {sum(c.values()):5d}  {dict(c)}")
    # table usage histogram
    tbl = Counter(t for r in cat for t in r["tables"])
    print("\ntop tables:")
    for t, n in tbl.most_common(40):
        print(f"  {t:40s} {n}")

if __name__ == "__main__":
    sys.exit(main() or 0)
