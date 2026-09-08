#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERIFY="$ROOT/scripts/pg-instance-verify.sh"
RESTORE="$ROOT/scripts/pg-instance-restore.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Testing verify/restore flag guards..."
verify_error="$("$VERIFY" 2>&1 || true)"
grep -q "readable --manifest" <<<"$verify_error"

restore_error="$("$RESTORE" --yes --freshness-check \
  --manifest /dev/null --manifest-hash x --policy /dev/null \
  --backup-index /dev/null --source-side local --source-database acc_db \
  --target-side remote --target-database llm_gateway 2>&1 || true)"
grep -q "refusing to restore into llm_gateway" <<<"$restore_error"
echo "✓ verify/restore guards passed"
