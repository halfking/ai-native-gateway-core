#!/usr/bin/env python3
"""Parse sql/schema/01-schema.sql into a machine-readable table inventory.

Outputs:
  .db-audit/tmp/tables.json     full column/index/fk detail
  .db-audit/tmp/tables.tsv      compact TSV: table \t column \t type \t null \t default \t pk
"""
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / "sql" / "schema" / "01-schema.sql"

def strip_comments(sql: str) -> str:
    # remove /* */ blocks and -- line comments (not inside quotes; good-enough heuristic)
    sql = re.sub(r"/\*.*?\*/", "", sql, flags=re.S)
    sql = re.sub(r"--[^\n]*", "", sql)
    return sql

def split_top_level(stmts: str):
    """Split on semicolons at paren depth 0, dollar-quoted blocks kept whole."""
    out, buf, depth, i, n = [], [], 0, 0, len(stmts)
    in_dq = in_sq = False
    while i < n:
        c = stmts[i]
        if stmts.startswith("$$", i) or stmts.startswith("$f$", i):
            end = "$f$" if stmts.startswith("$f$", i) else "$$"
            j = stmts.find(end, i + 2)
            j = n if j < 0 else j + len(end)
            buf.append(stmts[i:j]); i = j; continue
        if c == "'" and not in_dq:
            in_sq = not in_sq
        if c == '"' and not in_sq:
            in_dq = not in_dq
        if not in_sq and not in_dq:
            if c == "(":
                depth += 1
            elif c == ")":
                depth -= 1
            elif c == ";" and depth == 0:
                out.append("".join(buf)); buf = []; i += 1; continue
        buf.append(c); i += 1
    if buf:
        out.append("".join(buf))
    return out

TABLE_RE = re.compile(
    r"^CREATE\s+(?:UNLOGGED\s+|LOCAL\s+TEMP\s+|TEMP\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"
    r"(?:public\.)?([A-Za-z_][\w]*)\s*\(",
    re.I | re.S,
)
COL_RE = re.compile(
    r"^\s*([A-Za-z_][\w]*)\s+([A-Za-z_][\w\s\(\),\'\.0-9\[\]\-]*?)"
    r"(?=\s+(NOT\s+NULL|NULL|DEFAULT|PRIMARY\s+KEY|UNIQUE|REFERENCES|CHECK|CONSTRAINT|GENERATED|COLLATE|,|$))",
    re.I,
)
PK_INLINE_RE = re.compile(r"^\s*([A-Za-z_][\w]*)\s+.*PRIMARY\s+KEY", re.I | re.S)
FK_RE = re.compile(r"REFERENCES\s+(?:public\.)?([A-Za-z_]\w*)\s*\(([^)]*)\)", re.I)

def parse_columns(body: str, table: str):
    """Split column/constraint list at depth-0 commas."""
    parts, depth, cur, in_sq = [], 0, [], False
    for c in body:
        if c == "'" and not cur or (c == "'" and in_sq):
            in_sq = not in_sq
        if not in_sq:
            if c == "(":
                depth += 1
            elif c == ")":
                depth -= 1
            elif c == "," and depth == 0:
                parts.append("".join(cur)); cur = []; continue
        cur.append(c)
    if cur:
        parts.append("".join(cur))

    cols, pk, fks, uniques, checks = [], [], [], [], []
    for p in parts:
        s = p.strip()
        if not s:
            continue
        head = s.split()[0].lower() if s.split() else ""
        if head in ("primary", "constraint", "unique", "check", "foreign", "exclude"):
            u = re.match(r"^\s*(?:CONSTRAINT\s+(\w+)\s+)?UNIQUE\s*\(([^)]*)\)", s, re.I)
            if u:
                uniques.append((u.group(1) or "", [c.strip().strip('"') for c in u.group(2).split(",")]))
                continue
            pk_m = re.match(r"^\s*(?:CONSTRAINT\s+(\w+)\s+)?PRIMARY\s+KEY\s*\(([^)]*)\)", s, re.I)
            if pk_m:
                pk = [c.strip().strip('"') for c in pk_m.group(2).split(",")]
                continue
            fk_m = re.match(
                r"^\s*(?:CONSTRAINT\s+(\w+)\s+)?FOREIGN\s+KEY\s*\(([^)]*)\)\s*REFERENCES\s+(?:public\.)?(\w+)\s*\(([^)]*)\)",
                s, re.I)
            if fk_m:
                fks.append({
                    "name": fk_m.group(1) or "",
                    "columns": [c.strip().strip('"') for c in fk_m.group(2).split(",")],
                    "ref_table": fk_m.group(3),
                    "ref_columns": [c.strip().strip('"') for c in fk_m.group(4).split(",")],
                })
                continue
            ck_m = re.match(r"^\s*(?:CONSTRAINT\s+(\w+)\s+)?CHECK\s*\(", s, re.I)
            if ck_m:
                checks.append(ck_m.group(1) or "(unnamed)")
                continue
            continue  # other constraint forms
        m = re.match(r'^\s*("?)([A-Za-z_]\w*)\1\s+(.+)$', s, re.S)
        if not m:
            continue
        name, rest = m.group(2), m.group(3).strip()
        # type: up to first keyword boundary
        tm = re.match(
            r"([A-Za-z_][\w]*(?:\s+[A-Za-z_][\w]*)*?(?:\([^)]*\))?(?:\[\])?)"
            r"(?=\s+(NOT\s+NULL|NULL|DEFAULT|PRIMARY|UNIQUE|REFERENCES|CHECK|CONSTRAINT|GENERATED|COLLATE|ON\s+CONFLICT|$))",
            rest, re.I)
        typ = tm.group(1).strip() if tm else rest.split()[0]
        not_null = bool(re.search(r"\bNOT\s+NULL\b", rest, re.I))
        dflt = re.search(r"\bDEFAULT\s+((?:[^\s,]+(?:\s*\([^)]*\))?(?:\s*::\s*[\w\[\]]+)?(?:\s+'[^']*')?(?:\s*\|\|\s*'[^']*')*))", rest, re.I)
        inline_pk = bool(re.search(r"\bPRIMARY\s+KEY\b", rest, re.I))
        refs = FK_RE.search(rest)
        if inline_pk:
            pk = [name]
        if refs:
            fks.append({"name": f"{table}_{name}_fkey", "columns": [name],
                        "ref_table": refs.group(1), "ref_columns": [c.strip() for c in refs.group(2).split(",")]})
        cols.append({
            "name": name,
            "type": re.sub(r"\s+", " ", typ),
            "not_null": not_null or inline_pk,
            "default": dflt.group(1).strip() if dflt else None,
            "pk": inline_pk,
        })
    for c in cols:
        if c["name"] in pk:
            c["pk"] = True
            c["not_null"] = True
    return cols, pk, fks, uniques, checks

def main():
    raw = SCHEMA.read_text(encoding="utf-8", errors="replace")
    sql = strip_comments(raw)
    tables = {}
    idx_statements = []
    alter_fks = []
    for stmt in split_top_level(sql):
        s = stmt.strip()
        if not s:
            continue
        # standalone CREATE INDEX / CREATE UNIQUE INDEX (incl. IF NOT EXISTS)
        im = re.match(
            r"^CREATE\s+(UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s+ON\s+(?:public\.)?(\w+)\s*(USING\s+\w+\s*)?\((.+?)(?:\))?\s*(?:WHERE\s+.+)?$",
            s, re.I | re.S)
        if im and TABLE_RE.match(s) is None:
            idx_statements.append({
                "name": im.group(2), "table": im.group(3), "unique": bool(im.group(1)),
                "columns": (im.group(4) or "").replace("\n", " ").strip()[:300],
            })
            continue
        tm = TABLE_RE.match(s)
        if not tm:
            am = re.match(
                r"^ALTER\s+TABLE\s+(?:ONLY\s+)?(?:public\.)?(\w+)\s+ADD\s+(?:CONSTRAINT\s+(\w+)\s+)?FOREIGN\s+KEY\s*\(([^)]*)\)\s*REFERENCES\s+(?:public\.)?(\w+)\s*\(([^)]*)\)",
                s, re.I | re.S)
            if am:
                alter_fks.append({
                    "table": am.group(1), "name": am.group(2) or "",
                    "columns": [c.strip().strip('"') for c in am.group(3).split(",")],
                    "ref_table": am.group(4),
                    "ref_columns": [c.strip().strip('"') for c in am.group(5).split(",")],
                })
            continue
        name = tm.group(1)
        # table body: from first '(' to matching close at depth 0
        i = s.find("(", tm.end() - 1)
        depth, j = 0, i
        in_sq = False
        while j < len(s):
            c = s[j]
            if c == "'":
                in_sq = not in_sq
            if not in_sq:
                if c == "(":
                    depth += 1
                elif c == ")":
                    depth -= 1
                    if depth == 0:
                        break
            j += 1
        body = s[i + 1:j]
        tail = s[j + 1:]
        cols, pk, fks, uniques, checks = parse_columns(body, name)
        inherits = re.search(r"INHERITS\s*\(([^)]*)\)", tail, re.I)
        partof = re.search(r"PARTITION\s+OF\s+(?:public\.)?(\w+)", tail, re.I)
        unlogged = bool(re.match(r"^CREATE\s+UNLOGGED", s, re.I))
        tables[name] = {
            "name": name, "unlogged": unlogged,
            "partition_of": partof.group(1) if partof else None,
            "columns": cols, "pk": pk, "fks": fks,
            "uniques": uniques, "checks": checks,
        }
    # attach indexes
    for t in tables.values():
        t["indexes"] = []
    for idx in idx_statements:
        if idx["table"] in tables:
            tables[idx["table"]]["indexes"].append(
                {"name": idx["name"], "unique": idx["unique"], "columns": idx["columns"]})
    for fk in alter_fks:
        if fk["table"] in tables:
            tables[fk["table"]]["fks"].append(
                {"name": fk["name"], "columns": fk["columns"],
                 "ref_table": fk["ref_table"], "ref_columns": fk["ref_columns"]})
    outdir = Path(__file__).parent / "tmp"
    outdir.mkdir(parents=True, exist_ok=True)
    (outdir / "tables.json").write_text(json.dumps(tables, indent=1, ensure_ascii=False), encoding="utf-8")
    with (outdir / "tables.tsv").open("w", encoding="utf-8") as f:
        for t in sorted(tables.values(), key=lambda x: x["name"]):
            for c in t["columns"]:
                f.write("\t".join([t["name"], c["name"], c["type"],
                                   "NN" if c["not_null"] else "", c["default"] or "",
                                   "PK" if c["pk"] else ""]) + "\n")
    real = [t for t in tables.values() if not t["partition_of"]]
    parts = [t for t in tables.values() if t["partition_of"]]
    print(f"tables={len(tables)} real={len(real)} static_partitions={len(parts)} "
          f"indexes={len(idx_statements)} fks={sum(len(t['fks']) for t in tables.values())}")

if __name__ == "__main__":
    sys.exit(main() or 0)
