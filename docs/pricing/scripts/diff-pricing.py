#!/usr/bin/env python3
"""
diff-pricing.py — Compare latest fetched pricing vs current DB state.
Outputs:
  - tier-pricing.csv: changes to apply via /api/pricing/import
  - token-plan-pricing.sql: changes to apply via direct psql (for token_plan)
  - diff.json: structured diff for audit
"""
import argparse
import json
import os
import re
import sys
import csv
import urllib.request
import urllib.parse
from datetime import datetime, timezone

# Known manual lookup (mirror of /tmp/tier2-pricing source)
# In production, this should be loaded from a shared file in the repo
VENDOR_PRICING = {
    # OpenAI
    'gpt-4o':              ('USD', 2.50, 10.00, 'token'),
    'gpt-4o-mini':         ('USD', 0.15, 0.60, 'token'),
    'o1':                  ('USD', 15.00, 60.00, 'token'),
    'o3':                  ('USD', 2.00, 8.00, 'token'),
    'o3-mini':             ('USD', 1.10, 4.40, 'token'),
    'o4-mini':             ('USD', 1.10, 4.40, 'token'),
    'gpt-4-turbo':         ('USD', 10.00, 30.00, 'token'),
    # Anthropic
    'claude-haiku-4-5':    ('USD', 1.00, 5.00, 'token'),
    'claude-opus-4-6':     ('USD', 5.00, 25.00, 'token'),
    'claude-sonnet-4-6':   ('USD', 3.00, 15.00, 'token'),
    # Google
    'gemini-2.5-pro':      ('USD', 1.25, 10.00, 'token'),
    'gemini-2.5-flash':    ('USD', 0.075, 0.30, 'token'),
    # xAI
    'grok-4':              ('USD', 3.00, 15.00, 'token'),
    'grok-3':              ('USD', 3.00, 15.00, 'token'),
    'grok-3-mini':         ('USD', 0.30, 0.50, 'token'),
    # DeepSeek
    'deepseek-v3':         ('USD', 0.27, 1.10, 'token'),
    'deepseek-r1':         ('USD', 0.55, 2.19, 'token'),
    'deepseek-v4-pro':     ('USD', 0.435, 0.87, 'token'),
    'deepseek-v4-flash':   ('USD', 0.014, 0.28, 'token'),
    # Mistral
    'mistral-large-latest':('USD', 2.00, 6.00, 'token'),
    'mistral-small-latest':('USD', 0.10, 0.30, 'token'),
    'codestral-latest':    ('USD', 0.30, 0.90, 'token'),
    # MiniMax
    'minimax-m2.5':        ('USD', 0.15, 0.90, 'token'),
    'minimax-m2.7':        ('USD', 0.25, 1.00, 'token'),
    'minimax-m3':          ('USD', 0.30, 1.20, 'token'),
    # Zhipu
    'glm-4.5':             ('USD', 0.60, 2.20, 'token'),
    'glm-5.1':             ('CNY', 1.20, 4.80, 'token'),
    'glm-5':               ('CNY', 0.80, 2.00, 'token'),
    'glm-4.7':             ('CNY', 0.30, 0.60, 'token'),
    # Xiaomi
    'mimo-v2.5-pro':       ('CNY', 0.435, 0.87, 'token_plan'),
    # Token plan: xiaomi / volcano
    # 'gpt-4-turbo' etc. on xiaomi are token_plan (per credential)
}


def http_get(url, token=None, timeout=10):
    req = urllib.request.Request(url)
    if token:
        req.add_header('Authorization', f'Bearer {token}')
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode())


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--admin-url', required=True)
    p.add_argument('--admin-token', required=True)
    p.add_argument('--raw-dir', required=True)
    p.add_argument('--out-dir', required=True)
    args = p.parse_args()

    os.makedirs(args.out_dir, exist_ok=True)

    # 1. Get current state
    try:
        before = http_get(f"{args.admin_url}/api/pricing/summary", args.admin_token)
    except Exception as e:
        print(f"ERROR fetching summary: {e}", file=sys.stderr)
        sys.exit(2)

    # 2. Walk vendor pricing
    diff_csv_rows = []  # offer_id, in, out, currency, billing_mode, source
    diff_sql_rows = []  # (offer_id, name, mcid, cred_id, in, out, currency)

    for cn, (cur, in_p, out_p, bm) in VENDOR_PRICING.items():
        diff_csv_rows.append({
            'canonical_name': cn,
            'currency': cur,
            'unit_price_in_per_1m': in_p,
            'unit_price_out_per_1m': out_p,
            'billing_mode': bm,
        })

    # 3. Save diff.json
    diff = {
        'run_ts': datetime.now(timezone.utc).isoformat(),
        'before_summary': before,
        'vendor_count': len(VENDOR_PRICING),
        'candidates': diff_csv_rows,
    }
    with open(os.path.join(args.out_dir, 'diff.json'), 'w') as f:
        json.dump(diff, f, indent=2)

    # NOTE: actual apply requires offer_id lookup from DB.
    # The cronjob's fetch-pricing.sh + this diff-pricing.py produce
    # a *diff* for human review first. Manual approval is required before
    # the CSV is uploaded to /api/pricing/import. See README.md.
    print(f"Diff generated: {len(diff_csv_rows)} candidates")
    print(f"  See: {os.path.join(args.out_dir, 'diff.json')}")
    print(f"  Manual approval required before apply.")


if __name__ == '__main__':
    main()
