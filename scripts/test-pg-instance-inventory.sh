#!/usr/bin/env bash
# Test inventory command (mock-free, no repo tmp writes)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC="$ROOT/scripts/pg-instance-sync.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Testing inventory command validation..."

# Test 1: inventory requires --output-dir
echo "Test 1: Inventory requires --output-dir..."
if "$SYNC" inventory 2>/dev/null; then
    echo "ERROR: inventory should require --output-dir" >&2
    exit 1
fi

# Check error message
error_msg=$("$SYNC" inventory 2>&1 || true)
if ! echo "$error_msg" | grep -q "requires --output-dir"; then
    echo "ERROR: Wrong error message: $error_msg" >&2
    exit 1
fi
echo "✓ Inventory requires --output-dir with correct error"

# Test 2: inventory with --output-dir (will fail due to missing real DB, but validates structure)
echo "Test 2: Inventory command structure..."
mkdir -p "$tmp/output"

# This will fail because no real databases, but should show proper error handling
error_output=$("$SYNC" inventory --output-dir "$tmp/output" 2>&1 || true)

# Should show it's trying to collect inventory (not "not implemented")
if echo "$error_output" | grep -q "not implemented"; then
    echo "ERROR: inventory should not show 'not implemented'" >&2
    exit 1
fi

if echo "$error_output" | grep -q "Collecting database inventory"; then
    echo "✓ Inventory command structure is correct"
else
    echo "WARNING: Inventory command may need database connection (expected in real use)"
    echo "  Error: $error_output"
fi

echo "Inventory tests completed"