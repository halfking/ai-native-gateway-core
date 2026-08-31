#!/usr/bin/env bash
# ============================================================================
# RETIRED: historical local/kaixuan-1 → 252 schema sync entry point.
#
# This path predates the managed dynamic 252 tunnel and has unsafe credential
# fallback/import semantics.  Reconcile is intentionally available only through
# the explicit gated interface:
#   bash scripts/local-dev/verify-db-consistency.sh --reconcile <tables...> --yes
#
# That command must be separately approved because it changes 252.
# ============================================================================
set -euo pipefail

printf '%s\n' "scripts/sync-schema-to-252.sh is retired." >&2
printf '%s\n' "Use the explicitly gated verify-db-consistency.sh --reconcile ... --yes path after approval." >&2
exit 64
