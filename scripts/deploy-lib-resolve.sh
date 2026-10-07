# deploy-lib-resolve.sh — resolve the deploy-lib SSOT without trusting the
# repo-relative scripts/deploy-lib symlink.
#
# scripts/deploy-lib is a RELATIVE symlink (../../../../ai-native-tools/
# deploy-lib) that only resolves inside the canonical checkout depth. Deploy
# entrypoints also run from temporary worktrees (/private/tmp/lgw-merge-*),
# where the link dangles and `source scripts/deploy-lib/...` dies instantly —
# both 2026-10-07 merge rounds each had to hand-patch the link before
# deploy-245.sh would even start. This helper retires that ritual.
#
# Resolution order (mirrors scripts/_shared-lib.sh so every entry resolves to
# the same SSOT):
#   1. $AIAN_DEPLOY_LIB when already set — caller wins, never clobbered
#   2. scripts/deploy-lib link target, when it yields a real
#      parse-wrapper-flags.sh (canonical checkout: same SSOT either way)
#   3. $HOME/workspace/ai-native-tools/deploy-lib — same default as
#      _shared-lib.sh
#
# Exits 64 when nothing yields a usable SSOT. Sets and exports
# AIAN_DEPLOY_LIB; prints nothing on success. Behavior gate:
# scripts/.verify-deploy-lib-resolution.sh

_dlr_self_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -z "${AIAN_DEPLOY_LIB:-}" ]]; then
  if [[ -f "$_dlr_self_dir/deploy-lib/parse-wrapper-flags.sh" ]]; then
    AIAN_DEPLOY_LIB="$_dlr_self_dir/deploy-lib"
  else
    AIAN_DEPLOY_LIB="$HOME/workspace/ai-native-tools/deploy-lib"
  fi
fi

if [[ ! -f "$AIAN_DEPLOY_LIB/parse-wrapper-flags.sh" ]]; then
  echo "missing deploy-lib SSOT: $AIAN_DEPLOY_LIB (parse-wrapper-flags.sh not found)" >&2
  echo "fix: export AIAN_DEPLOY_LIB=/path/to/deploy-lib-checkout" >&2
  exit 64
fi
export AIAN_DEPLOY_LIB
