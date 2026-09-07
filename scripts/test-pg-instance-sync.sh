#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC="$ROOT/scripts/pg-instance-sync.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/local.tsv" <<'EOF'
acc_db
acc_test
llm_gateway
smm_data
local_business
EOF

cat >"$tmp/remote.tsv" <<'EOF'
acc_db
kxmemory
llm_gateway
smm
EOF

cat >"$tmp/policy.conf" <<'EOF'
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway'
DB_ALIASES='smm_data=smm'
LOCAL_ONLY_ALLOWLIST='local_business'
EOF

"$SYNC" plan \
  --local-inventory "$tmp/local.tsv" \
  --remote-inventory "$tmp/remote.tsv" \
  --policy "$tmp/policy.conf" \
  --manifest "$tmp/manifest.tsv"

grep -Fxq $'COMMON\tacc_db\tacc_db\tSCHEMA_AND_INSERT_ONLY' "$tmp/manifest.tsv"
grep -Fxq $'EXCLUDED\tacc_test\t-\tNAME_POLICY' "$tmp/manifest.tsv"
grep -Fxq $'COMMON\tllm_gateway\tllm_gateway\tSCHEMA_ONLY' "$tmp/manifest.tsv"
grep -Fxq $'ALIAS\tsmm_data\tsmm\tSCHEMA_AND_INSERT_ONLY' "$tmp/manifest.tsv"
grep -Fxq $'LOCAL_ONLY\tlocal_business\t-\tCREATE_REMOTE_AND_INSERT_ONLY' "$tmp/manifest.tsv"
grep -Fxq $'REMOTE_ONLY\t-\tkxmemory\tBOOTSTRAP_LOCAL_FULL' "$tmp/manifest.tsv"

first_hash="$(shasum -a 256 "$tmp/manifest.tsv" | awk '{print $1}')"
"$SYNC" plan \
  --local-inventory "$tmp/local.tsv" \
  --remote-inventory "$tmp/remote.tsv" \
  --policy "$tmp/policy.conf" \
  --manifest "$tmp/manifest-2.tsv" >/dev/null
second_hash="$(shasum -a 256 "$tmp/manifest-2.tsv" | awk '{print $1}')"
[[ "$first_hash" == "$second_hash" ]]

printf 'pg-instance-sync CLI test passed\n'
