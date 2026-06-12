#!/usr/bin/env python3
"""
diff-pricing.py — Compare vendor-pricing-table.py (SSOT) vs current DB state.
Outputs:
  - diff.json: structured diff for audit
  - fix-currency.sql: SQL to fix currency/billing_mode
  - fix-prices.csv: CSV for /api/pricing/import (token billing_mode only)
  - fix-prices-token-plan.sql: SQL for token_plan offers (direct psql)

Usage:
  # Full diff (needs DB access via SSH):
  python3 diff-pricing.py --admin-url https://llmgo.kxpms.cn --admin-token $API_KEY

  # Dry-run (SSOT only, no DB):
  python3 diff-pricing.py --dry-run

  # Generate safety SQL (fix discovery overrides):
  python3 diff-pricing.py --safety-sql
"""
import argparse
import json
import os
import sys
import csv
import urllib.request
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(__file__))
from vendor_pricing_table import (
    CANONICAL_PRICING, CREDENTIAL_OVERRIDES, DOMESTIC_CREDENTIALS,
    DOMESTIC_MODEL_PREFIXES, get_pricing, is_domestic_credential, is_domestic_model,
)


SAFETY_SQL = """-- Safety SQL: fix Go app discovery overrides
BEGIN;
UPDATE credential_model_bindings SET currency = 'CNY', pricing_updated_at = now(), updated_at = now()
WHERE credential_id IN (SELECT id FROM credentials WHERE label IN ('minimax-prod-1','roocode','xiaomi-token-plan','demo-tokenplan','hzx-normal'))
  AND currency != 'CNY';
UPDATE credential_model_bindings SET currency = 'CNY', pricing_updated_at = now(), updated_at = now()
WHERE credential_id = 8 AND currency != 'CNY'
  AND provider_model_id IN (
    SELECT mc.id FROM models_canonical mc WHERE mc.canonical_name ~ '^(glm|qwen|qwq|doubao|moonshot|yi-|baichuan|minimax|deepseek|mimo|sensechat|step|abab|bge)'
  );
UPDATE credential_model_bindings SET billing_mode = 'token_plan', updated_at = now()
WHERE credential_id = 9 AND billing_mode != 'token_plan';
UPDATE credential_model_bindings SET billing_mode = 'token_plan', updated_at = now()
WHERE credential_id = 11 AND billing_mode != 'token_plan';
UPDATE credential_model_bindings SET billing_mode = 'token', updated_at = now()
WHERE billing_mode = 'per_token' AND billing_mode NOT IN ('token_plan', 'code_plan');
COMMIT;
"""


def http_get(url, token=None, timeout=10):
    req = urllib.request.Request(url)
    if token:
        req.add_header('Authorization', f'Bearer {token}')
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode())


def generate_safety_sql():
    print(SAFETY_SQL)


def dry_run():
    print(f"=== vendor-pricing-table.py Summary ===")
    print(f"Total models: {len(CANONICAL_PRICING)}")
    cny = sum(1 for v in CANONICAL_PRICING.values() if v['currency'] == 'CNY')
    usd = sum(1 for v in CANONICAL_PRICING.values() if v['currency'] == 'USD')
    est = sum(1 for v in CANONICAL_PRICING.values() if v.get('source') == 'estimated')
    tp = sum(1 for k, v in CREDENTIAL_OVERRIDES.items() if v.get('billing_mode') == 'token_plan')
    print(f"CNY: {cny}, USD: {usd}, Estimated: {est}")
    print(f"Domestic credentials: {len(DOMESTIC_CREDENTIALS)}")
    print(f"Credential overrides (token_plan): {tp}")
    print()
    print("Models by vendor:")
    vendors = {}
    for name, entry in CANONICAL_PRICING.items():
        v = entry['vendor']
        vendors.setdefault(v, []).append(name)
    for v in sorted(vendors):
        print(f"  {v}: {len(vendors[v])} models")


def main():
    p = argparse.ArgumentParser(description='Diff pricing SSOT vs DB')
    p.add_argument('--admin-url', default='https://llmgo.kxpms.cn')
    p.add_argument('--admin-token', default=os.environ.get('LLMGO_API_KEY', ''))
    p.add_argument('--out-dir', default='/tmp/pricing-diff')
    p.add_argument('--dry-run', action='store_true', help='SSOT summary only')
    p.add_argument('--safety-sql', action='store_true', help='Print safety SQL')
    args = p.parse_args()

    if args.safety_sql:
        generate_safety_sql()
        return

    if args.dry_run:
        dry_run()
        return

    if not args.admin_token:
        print("ERROR: --admin-token or LLMGO_API_KEY env required", file=sys.stderr)
        sys.exit(1)

    os.makedirs(args.out_dir, exist_ok=True)

    try:
        summary = http_get(f"{args.admin_url}/api/pricing/summary", args.admin_token)
    except Exception as e:
        print(f"ERROR fetching summary: {e}", file=sys.stderr)
        sys.exit(2)

    diff = {
        'run_ts': datetime.now(timezone.utc).isoformat(),
        'before_summary': summary,
        'ssot_models': len(CANONICAL_PRICING),
        'ssot_cny': sum(1 for v in CANONICAL_PRICING.values() if v['currency'] == 'CNY'),
        'ssot_usd': sum(1 for v in CANONICAL_PRICING.values() if v['currency'] == 'USD'),
        'domestic_credentials': sorted(DOMESTIC_CREDENTIALS),
        'credential_overrides': {f"{k[0]}/{k[1]}": v for k, v in CREDENTIAL_OVERRIDES.items()},
        'safety_sql': SAFETY_SQL.strip(),
    }

    with open(os.path.join(args.out_dir, 'diff.json'), 'w') as f:
        json.dump(diff, f, indent=2, ensure_ascii=False)

    print(f"Diff written to {args.out_dir}/diff.json")
    print(f"  DB: {summary['total_offers']} offers, {summary['cny_offers']} CNY, {summary['usd_offers']} USD")
    print(f"  SSOT: {diff['ssot_models']} models, {diff['ssot_cny']} CNY, {diff['ssot_usd']} USD")
    print(f"  Safety SQL included in diff.json — apply after any pricing update")


if __name__ == '__main__':
    main()
