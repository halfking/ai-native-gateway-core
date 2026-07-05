#!/usr/bin/env python3
"""Split pg_dump --schema-only output into one file per object.

Usage:
  python3 split-schema.py <input.sql> <output-dir>

Output structure:
  <output-dir>/
    tables/<name>.sql
    sequences/<name>.sql
    functions/<name>.sql
    views/<name>.sql
    triggers/<name>.sql
    indexes/<name>.sql
    policies/<name>.sql
    constraints/<name>.sql
"""

import re
import os
import sys
from collections import OrderedDict

HEADER_RE = re.compile(
    r"^--\s*\n"
    r"--\s*Name:\s*(?P<name>.+?);\s*Type:\s*(?P<type>.+?);\s*Schema:\s*public;\s*Owner:\s*-\s*\n"
    r"--\s*\n",
    re.MULTILINE,
)

TYPE_MAP = {
    "TABLE": "tables",
    "VIEW": "views",
    "MATERIALIZED VIEW": "views",
    "FUNCTION": "functions",
    "TRIGGER": "triggers",
    "INDEX": "indexes",
    "INDEX ATTACH": "indexes",
    "POLICY": "policies",
    "ROW SECURITY": "policies",
    "CONSTRAINT": "constraints",
    "TABLE CONSTRAINT": "constraints",
    "SEQUENCE": "sequences",
    "SEQUENCE OWNED BY": "sequences",
    "DEFAULT": "sequences",
    "COMMENT": None,  # merged into parent object
    "TABLE ATTACH": "tables",
    "RULE": "triggers",
}

# Objects that have a paired COMMENT block that should be merged
COMMENTABLE = {
    "TABLE",
    "VIEW",
    "MATERIALIZED VIEW",
    "FUNCTION",
    "TRIGGER",
    "COLUMN",
    "SEQUENCE",
    "INDEX",
    "TABLE CONSTRAINT",
    "POLICY",
}


def sanitize_name(name):
    """Clean object name for use as filename."""
    name = name.strip()
    name = re.sub(r"[^a-zA-Z0-9_]", "_", name)
    name = re.sub(r"_+", "_", name)
    name = name.strip("_")
    return name.lower()


def extract_objects(content):
    """Split pg_dump output into object blocks.

    Returns list of (name, type, text, start_line).
    """
    objects = []
    lines = content.split("\n")

    # Find header positions
    positions = []
    for i, line in enumerate(lines):
        if line.strip() == "--":
            if i + 2 < len(lines):
                m = re.match(
                    r"--\s*Name:\s*(?P<name>.+?);\s*Type:\s*(?P<type>.+?);\s*Schema:\s*public;\s*Owner:\s*-\s*",
                    lines[i + 1],
                )
                if m:
                    positions.append((i, m.group("name"), m.group("type")))

    # Extract blocks between headers
    for idx, (start, name, otype) in enumerate(positions):
        end = positions[idx + 1][0] if idx + 1 < len(positions) else len(lines)
        # Skip the header lines themselves for the block content
        block_start = start
        block_text = "\n".join(lines[block_start:end])
        objects.append((name, otype, block_text, start))

    return objects


def merge_comments(objects):
    """Merge COMMENT blocks into their parent objects."""
    merged = OrderedDict()
    standalone = []

    for name, otype, text, line in objects:
        key = (name, otype)
        if otype == "COMMENT":
            # Find the referenced object name from the COMMENT text
            # Format: COMMENT ON <type> <name> IS '...'
            cm = re.search(r"COMMENT ON\s+(\w+)\s+(?:public\.)?(\S+)", text)
            if cm:
                ref_type = cm.group(1)
                ref_name = cm.group(2).rstrip(";")
                ref_key = (ref_name, ref_type)
                if ref_key in merged:
                    merged[ref_key]["comments"].append(text)
                    continue
            standalone.append((name, otype, text, line))
        else:
            if key not in merged:
                merged[key] = {
                    "name": name,
                    "type": otype,
                    "lines": [],
                    "comments": [],
                    "start_line": line,
                }
            merged[key]["lines"].append(text)

    result = list(merged.values()) + [
        {"name": n, "type": t, "lines": [txt], "comments": [], "start_line": ln}
        for n, t, txt, ln in standalone
    ]
    return result


def write_objects(merged, output_dir):
    """Write merged objects to individual files.
    output_dir is the top-level directory that already has tables/, functions/, etc.
    """
    for obj in merged:
        otype = obj["type"]
        name = obj["name"]
        subdir = TYPE_MAP.get(otype, "other")
        if subdir is None:
            continue

        target_dir = os.path.join(output_dir, subdir)
        os.makedirs(target_dir, exist_ok=True)

        filename = sanitize_name(name.split(".")[-1]) + ".sql"  # strip schema prefix
        filepath = os.path.join(target_dir, filename)

        content_lines = []
        for block in obj["lines"]:
            content_lines.append(block)
        for comment in obj["comments"]:
            content_lines.append(comment)

        content = "\n".join(content_lines)

        # Append newline if missing
        if not content.endswith("\n"):
            content += "\n"

        with open(filepath, "w") as f:
            f.write(content)

        otype_display = otype
        if otype == "MATERIALIZED VIEW":
            otype_display = "MATERIALIZED VIEW"
        print(f"  {subdir}/{filename}  ({otype_display})")


def main():
    if len(sys.argv) != 3:
        print("Usage: python3 split-schema.py <input.sql> <output-dir>")
        sys.exit(1)

    input_file = sys.argv[1]
    output_dir = sys.argv[2]

    with open(input_file) as f:
        content = f.read()

    print(f"Reading {input_file} ({len(content)} bytes)...")
    raw_objects = extract_objects(content)
    print(f"Found {len(raw_objects)} raw objects")

    merged = merge_comments(raw_objects)
    print(f"Merged into {len(merged)} objects (with comments attached)")

    print(f"\nWriting to {output_dir}/:")
    # Remove the "objects" subdir nesting since output_dir IS the objects dir
    write_objects(merged, output_dir)
    print(f"\nDone! Files written to {output_dir}/ (by type)")


if __name__ == "__main__":
    main()
