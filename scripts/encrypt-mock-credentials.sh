#!/usr/bin/env bash
# Update all 60 mock credentials with properly encrypted tokens
set -euo pipefail

cd "$(dirname "$0")/.."
KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
PLAINTEXT="loadtest-mock-token"

echo "Encrypting token for all 60 credentials..."

# Encrypt the token once
CIPHER_HEX=$(bash scripts/_fpslot-make-cipher.sh "$KEY" "$PLAINTEXT")

# Generate SQL to update all credentials
SQL_FILE="/tmp/update-encrypted-credentials.sql"

cat > "$SQL_FILE" <<EOF
BEGIN;

-- Update all 60 mock credentials with properly encrypted token
UPDATE credentials 
SET secret_ciphertext = '\\x${CIPHER_HEX}'::bytea
WHERE id BETWEEN 9010 AND 9069;

SELECT COUNT(*) as updated_count FROM credentials WHERE id BETWEEN 9010 AND 9069;

COMMIT;
EOF

echo "Applying encrypted credentials to database..."
psql -d llm_gateway -f "$SQL_FILE"

echo "✓ All 60 credentials updated with encrypted token"
echo "Cipher (hex): ${CIPHER_HEX:0:40}..."
