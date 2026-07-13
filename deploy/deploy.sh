#!/usr/bin/env bash
# =====================================================================
# deploy/deploy.sh — DEPRECATION WRAPPER (Slice 8)
#
# Per spec cf8aad1a9 §"Compatibility disposition":
#   deploy/deploy.sh → Make fail-closed deprecation wrappers
#                       pointing to ./scripts/deploy.sh
#
# This script used to ship its own docker / kubectl / systemctl flow
# independent from scripts/deploy.sh. As of 2026-07-13 that flow is
# superseded by the canonical CLI:
#
#   ./scripts/deploy.sh plan <target>
#   ./scripts/deploy.sh deploy <target>
#   ./scripts/deploy.sh verify <target>
#   ./scripts/deploy.sh rollback <target> [--to <version>]
#
# Calling this old entrypoint forwards arguments to the canonical CLI.
# A deprecation warning is printed once per invocation (not per call)
# so CI consumers see the migration path without scripts breaking.
#
# Exit codes match the underlying canonical CLI (0, 4=no_rollback,
# 64=usage error, etc.). The deprecation marker does NOT change them.
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

warn "deploy/deploy.sh is deprecated (spec cf8aad1a9 Slice 8)"
warn "Forwarding to canonical CLI: $CANONICAL $*"
warn "Update CI / runbooks to call scripts/deploy.sh directly."

exec "$CANONICAL" "$@"