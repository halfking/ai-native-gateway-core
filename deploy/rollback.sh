#!/usr/bin/env bash
# =====================================================================
# deploy/rollback.sh — DEPRECATION WRAPPER (Slice 8)
#
# Per spec cf8aad1a9 §"Compatibility disposition":
#   deploy/rollback.sh → Make fail-closed deprecation wrappers
#                         pointing to ./scripts/deploy.sh
#
# The old deploy/rollback.sh used to consume /var/lib/deploy-tracker
# tags and run its own docker / kubectl flow. The canonical CLI now
# owns the rollback contract:
#
#   ./scripts/deploy.sh rollback 245 [--to <version>]
#   ./scripts/deploy.sh rollback 154                  # runbook guidance
#   ./scripts/deploy.sh rollback 252                  # deferred (refused)
#   ./scripts/deploy.sh rollback 186                  # retired (refused)
#
# For 245 the canonical CLI does versioned rollback via the
# releases/${VERSION}/`current/` symlink chain. For 154 it points
# operator at the existing runbook. For 186/252/kaixuan-* it refuses.
# All callers must migrate.
# =====================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANONICAL="$REPO_ROOT/scripts/deploy.sh"

if [[ ! -x "$CANONICAL" ]]; then
  echo "ERROR: $0 cannot find canonical CLI at $CANONICAL" >&2
  echo "       Re-clone the repository or run scripts/deploy.sh directly." >&2
  exit 64
fi

warn() {
  printf '\033[1;33m!\033[0m %s\n' "$*" >&2
}

warn "deploy/rollback.sh is deprecated (spec cf8aad1a9 Slice 8)"
warn "Forwarding to canonical CLI: $CANONICAL rollback $*"
warn "Update CI / runbooks to call scripts/deploy.sh rollback <target> directly."

exec "$CANONICAL" rollback "$@"