#!/usr/bin/env bash
# Tests for dl_load_project_env (deploy-local-lib.sh).
#
# Background (R54-F1a, regression audit 2026-09-22): the deploy helper reads
# a .env.local style file (DSNs, encryption keys, PEM blocks) before
# docker-compose starts the container. The previous `set -a; source $file`
# approach silently mangled any unquoted value containing shell
# metacharacters: a deploy with `LLM_GATEWAY_ADMIN_PASSWORD=Veritrans&9527`
# (unquoted) saw bash split at `&` (control operator), so the env got
# `Veritrans` while `9527` ran in the background, then
# sync_admin_password_from_env bcrypt-hashed the truncated `Veritrans`
# into users.password_hash and admin/auth.go never falls back to env once
# a row exists (incident 2026-09-05, re-observed in regression audit
# 2026-09-22 R54-F1a).
#
# R55-F1b follow-up: the first Python-parser attempt (commit c48651e58)
# accidentally introduced two more bugs:
#   (1) `_dl_safe_env_source ... >/dev/null 2>&1; env -0` discarded Python's
#       NUL-delimited output AND `env -0` could not see Python's os.environ
#       writes (separate process), so the bash loop had nothing to read and
#       the function silently became a no-op.
#   (2) Bash 5.x segfaults / parse-errors when a long comment with `${VAR}`
#       and `(...)` characters lives INSIDE a `<(...)` process substitution
#       and the function is called from a `()` subshell (the standard test
#       wrapper below).
# Both fixed in R55: Python now skips expansion for single-quoted values
# (matching bash semantics) and the bash wrapper drops the redirect,
# moves the context block out of `<(...)`, and calls _dl_safe_env_source
# directly so the NUL records reach the loop.
#
# These four tests cover the regression scenarios the prior approach
# failed on; the function-under-test is deploy-local-lib.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

env_file="$tmpdir/project.env"
# Single .env file with all four scenarios; each test subshell unsets its
# own target key so dl_load_project_env's "caller wins" rule does not mask
# the file value (deploy() relies on caller-wins for secrets-manager
# injection to bypass the file).
cat > "$env_file" <<'EOF'
# 1. Veritrans&9527: unquoted & must NOT split the value at the metachar.
DLT_ADMIN_PASSWORD=Veritrans&9527
# 2. ${VAR} interpolation referencing env vars the caller must export.
DLT_DATABASE_URL=postgres://${DLT_DB_USER}@${DLT_DB_HOST}/crm
# 3a. Double-quoted value with inner ampersand + interpolation preserved.
DLT_DQ="ampersand & ${DLT_DB_USER} interpolated"
# 3b. Single-quoted value with literal ${...} that must NOT be expanded.
DLT_SQ='dollar ${DLT_NOT_EXPANDED} literal'
# 4. Multi-line PEM block via literal \n inside double quotes (single-line
#    in the file). Real-newline PEM (line-by-line .env) is intentionally
#    NOT supported by the Python parser; literal \n is the convention.
DLT_PEM_KEY="-----BEGIN [REDACTED-KEY]-----\nMIIE...\n-----END [REDACTED-KEY]-----\n"
EOF

# ── Test 1: Veritrans&9527 ─────────────────────────────────────────────
# Reproduces the 2026-09-05 regression: unquoted `&` must NOT split the
# value, must NOT background a process, must NOT be missing.
out="$(bash -c '
  set -euo pipefail
  unset DLT_ADMIN_PASSWORD
  source "'"$LIB"'"
  dl_load_project_env "'"$env_file"'"
  printf "%s\n" "$DLT_ADMIN_PASSWORD"
')"
expected='Veritrans&9527'
if [[ "$out" != "$expected" ]]; then
  printf 'test1 (Veritrans&9527 unquoted metachar) mismatch:\n  got:      %q\n  expected: %q\n' "$out" "$expected" >&2
  exit 1
fi

# ── Test 2: ${VAR} interpolation ──────────────────────────────────────
# Caller exports DLT_DB_USER / DLT_DB_HOST; the .env file references them
# via ${...}. The interpolated DSN must match what bash-source would have
# produced (deploy() concatenates DSN pieces across files).
out="$(bash -c '
  set -euo pipefail
  unset DLT_DATABASE_URL
  source "'"$LIB"'"
  export DLT_DB_USER=crm
  export DLT_DB_HOST=db.local
  dl_load_project_env "'"$env_file"'"
  printf "%s\n" "$DLT_DATABASE_URL"
')"
expected='postgres://crm@db.local/crm'
if [[ "$out" != "$expected" ]]; then
  printf 'test2 (${VAR} interpolation) mismatch:\n  got:      %q\n  expected: %q\n' "$out" "$expected" >&2
  exit 1
fi

# ── Test 3: single/double quote passthrough ───────────────────────────
# Double-quoted value keeps the inner & as data AND honors ${...}
# interpolation; single-quoted value keeps ${...} as literal text
# (caller exported DLT_NOT_EXPANDED to verify the parser does NOT use it
# inside single quotes — bash semantics: single quotes forbid ALL
# expansion, fixed in R55-F1b).
out="$(bash -c '
  set -euo pipefail
  unset DLT_DQ DLT_SQ
  source "'"$LIB"'"
  export DLT_DB_USER=crm
  export DLT_NOT_EXPANDED=should_not_be_used
  dl_load_project_env "'"$env_file"'"
  printf "%s\n%s\n" "$DLT_DQ" "$DLT_SQ"
')"
expected=$'ampersand & crm interpolated\ndollar ${DLT_NOT_EXPANDED} literal'
if [[ "$out" != "$expected" ]]; then
  printf 'test3 (single/double quote passthrough) mismatch:\n  got:      %q\n  expected: %q\n' "$out" "$expected" >&2
  exit 1
fi

# ── Test 4: PEM block via literal \n inside double quotes ─────────────
# The value is one line in the .env, but contains three literal-backslash-n
# sequences (\\n). Parser preserves them; downstream code (Go crypto/x509
# or PEM parser) interprets them when actually using the key. No trailing
# newline in the captured value (printf "%s" emits nothing after the
# value), so the expected string ends with the literal \\n rather than a
# real newline.
out="$(bash -c '
  set -euo pipefail
  unset DLT_PEM_KEY
  source "'"$LIB"'"
  dl_load_project_env "'"$env_file"'"
  printf "%s" "$DLT_PEM_KEY"
')"
expected='-----BEGIN [REDACTED-KEY]-----\nMIIE...\n-----END [REDACTED-KEY]-----\n'
if [[ "$out" != "$expected" ]]; then
  printf 'test4 (PEM block literal \\n) mismatch:\n  got:      %q\n  expected: %q\n' "$out" "$expected" >&2
  exit 1
fi

# ── Missing file is a silent skip with a warning (deploy() gates the
#    fatal case at the top of deploy()). The function must NOT fail,
#    AND must surface the absence so a misconfigured clean checkout does
#    not silently come up with no DSN / no encryption key (incident
#    2026-09-05, provider 587).
warn_file="$tmpdir/warn"
if ! bash -c '
  set -euo pipefail
  source "'"$LIB"'"
  dl_load_project_env "'"$tmpdir"'/does-not-exist.env"
' 2> "$warn_file"; then
  printf 'dl_load_project_env must NOT fail on a missing file\n' >&2
  exit 1
fi
if ! grep -q "does-not-exist.env" "$warn_file"; then
  printf 'dl_load_project_env must emit the missing-file warning\n' >&2
  cat "$warn_file" >&2
  exit 1
fi

echo "dl_load_project_env: all checks passed"