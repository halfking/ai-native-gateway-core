#!/usr/bin/env bash
# .verify-deploy-lib-resolution.sh — behavior gate for deploy-lib-resolve.sh.
#
# Scenarios (all inside mktemp dirs, zero side effects on the real checkout):
#   ① $AIAN_DEPLOY_LIB preset wins over a resolvable symlink
#   ② resolvable symlink honored (canonical-checkout shape)
#   ③ dangling symlink + overridden $HOME → $HOME default chosen
#   ④ bogus $AIAN_DEPLOY_LIB → exit 64 with actionable fix hint
# Run: bash scripts/.verify-deploy-lib-resolution.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESOLVER="$SCRIPT_DIR/deploy-lib-resolve.sh"
[[ -f "$RESOLVER" ]] || { echo "resolver missing: $RESOLVER" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
check() { # <name> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    pass=$((pass + 1)); echo "✅ $1"
  else
    fail=$((fail + 1)); echo "❌ $1: want [$2] got [$3]"
  fi
}

# Fixtures: two fake SSOTs (marker file only) + fake HOME with the default SSOT.
mkdir -p "$TMP/ssot-link-target" "$TMP/ssot-env" "$TMP/scripts" \
  "$TMP/home/workspace/ai-native-tools/deploy-lib"
: > "$TMP/ssot-link-target/parse-wrapper-flags.sh"
: > "$TMP/ssot-env/parse-wrapper-flags.sh"
: > "$TMP/home/workspace/ai-native-tools/deploy-lib/parse-wrapper-flags.sh"
cp "$RESOLVER" "$TMP/scripts/deploy-lib-resolve.sh"

# ① env preset wins even when the symlink also resolves.
ln -sfn "$TMP/ssot-link-target" "$TMP/scripts/deploy-lib"
got="$(AIAN_DEPLOY_LIB="$TMP/ssot-env" bash -c \
  'source "$0/scripts/deploy-lib-resolve.sh"; printf %s "$AIAN_DEPLOY_LIB"' "$TMP")"
check "① env preset wins over resolvable symlink" "$TMP/ssot-env" "$got"

# ② resolvable symlink honored (no env).
got="$(env -u AIAN_DEPLOY_LIB bash -c \
  'source "$0/scripts/deploy-lib-resolve.sh"; printf %s "$AIAN_DEPLOY_LIB"' "$TMP")"
check "② resolvable symlink honored" "$TMP/scripts/deploy-lib" "$got"

# ③ dangling symlink (temporary-worktree shape) + overridden $HOME → default.
rm "$TMP/scripts/deploy-lib"
ln -sfn "$TMP/does-not-exist" "$TMP/scripts/deploy-lib"
got="$(env -u AIAN_DEPLOY_LIB HOME="$TMP/home" bash -c \
  'source "$0/scripts/deploy-lib-resolve.sh"; printf %s "$AIAN_DEPLOY_LIB"' "$TMP")"
check "③ dangling symlink falls back to \$HOME default" \
  "$TMP/home/workspace/ai-native-tools/deploy-lib" "$got"

# ④ bogus env → exit 64 (helper exits the sourcing shell directly).
rc=0
AIAN_DEPLOY_LIB="$TMP/nope" bash -c \
  'source "$0/scripts/deploy-lib-resolve.sh"; exit 0' "$TMP" || rc=$?
if [[ $rc -eq 64 ]]; then
  pass=$((pass + 1)); echo "✅ ④ bogus env fails with exit 64"
else
  fail=$((fail + 1)); echo "❌ ④ bogus env: want rc=64 got rc=$rc"
fi

echo "deploy-lib-resolve selftest: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
