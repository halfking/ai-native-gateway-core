#!/usr/bin/env python3
"""
build-plan-meta-sql.py — 从 credentials-with-plan-type.csv 生成 plan_meta UPDATE SQL
正确处理非标准 JSON (无引号键名) 转为合法 JSON
"""
import csv
import sys
import json
import re


def normalize_plan_json(s: str) -> str:
    s = (s or '').strip()
    if not s or s == '{}':
        return '{}'
    try:
        json.loads(s)
        return s
    except json.JSONDecodeError:
        pass
    s = re.sub(r'(\b\w+)\s*:', r'"\1":', s)
    s = re.sub(r':\s*(\w[\w.]*)', r': "\1"', s)
    s = re.sub(r':\s*"true"', ': true', s)
    s = re.sub(r':\s*"false"', ': false', s)
    s = re.sub(r':\s*"null"', ': null', s)
    s = s.replace('""', '"')
    try:
        json.loads(s)
        return s
    except json.JSONDecodeError:
        return '{}'


if len(sys.argv) < 2:
    print("Usage: build-plan-meta-sql.py <credentials-csv>", file=sys.stderr)
    sys.exit(1)

csv_path = sys.argv[1]

print("-- Auto-generated from 2026-06-12-credentials-with-plan-type.csv")
print("-- Updates credential_model_bindings.plan_meta JSONB column")
print("")
print("BEGIN;")
print("")

with open(csv_path) as f:
    reader = csv.DictReader(f)
    for r in reader:
        plan_json_str = normalize_plan_json(r.get('plan_json', ''))
        if plan_json_str == '{}':
            continue
        offer_id = r['offer_id']
        if not offer_id:
            continue
        plan_json_escaped = plan_json_str.replace("'", "''")
        print(
            f"UPDATE credential_model_bindings SET plan_meta = '{plan_json_escaped}'::jsonb, "
            f"pricing_source = 'imported' WHERE id = {offer_id};"
        )

print("")
print("COMMIT;")
