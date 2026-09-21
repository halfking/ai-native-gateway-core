#!/usr/bin/env bash
# Tests for dl_load_env_file (deploy-local-lib.sh).
#
# Background (mock system test 2026-09-07 §5.5): runtime env files written by
# dl_write_env carry values VERBATIM (docker --env-file contract, no shell
# quoting). A value containing '&' made `source` truncate the assignment and
# execute the remainder as a command, so native-mode starts lost the admin
# password. dl_load_env_file must export such values intact WITHOUT shell
# interpretation.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

env_file="$tmpdir/test.env"
cat > "$env_file" <<'EOF'
# comment line — must be skipped
LLM_GATEWAY_LISTEN=:8782
LLM_GATEWAY_ADMIN_PASSWORD=s3cr3t&restore`id`$(whoami)|x;rm
LLM_GATEWAY_EMPTY=
LLM_GATEWAY_QUOTES="double" 'single'
EOF

# Run the loader in a subshell so exports cannot leak into this test process.
# shellcheck disable=SC1090
out="$(bash -c '
  set -euo pipefail
  source "'"$LIB"'"
  dl_load_env_file "'"$env_file"'"
  printf "%s\n%s\n%s\n" \
    "$LLM_GATEWAY_LISTEN" \
    "$LLM_GATEWAY_ADMIN_PASSWORD" \
    "$LLM_GATEWAY_QUOTES"
')"

expected=':8782
s3cr3t&restore`id`$(whoami)|x;rm
"double" '"'"'single'"'"''

if [[ "$out" != "$expected" ]]; then
  printf 'dl_load_env_file output mismatch:\n  got:      %q\n  expected: %q\n' "$out" "$expected" >&2
  exit 1
fi

# The metacharacter value must have been exported as data, never executed:
# reaching this point with the exact value already proves no `id`/`whoami`
# command substitution ran (both are embedded in the expected literal).

# Empty value stays empty (not unset).
empty_val="$(bash -c '
  set -euo pipefail
  source "'"$LIB"'"
  dl_load_env_file "'"$env_file"'"
  printf "%s" "${LLM_GATEWAY_EMPTY-UNSET}"
')"
if [[ "$empty_val" != "" ]]; then
  printf 'empty value round-trip failed: got %q\n' "$empty_val" >&2
  exit 1
fi

# Missing file fails loudly.
if bash -c '
  set -euo pipefail
  source "'"$LIB"'"
  dl_load_env_file "'"$tmpdir"'/does-not-exist.env"
' >/dev/null 2>&1; then
  printf 'dl_load_env_file must fail on a missing file\n' >&2
  exit 1
fi

echo "dl_load_env_file: all checks passed"
