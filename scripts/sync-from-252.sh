#!/usr/bin/env bash
# ============================================================================
# RETIRED: historical 252 → target sync entry point.
#
# It predates the managed dynamic 252 tunnel, schema-import error fencing, and
# mandatory seven-dimension structure plus ordinary-table data audits. It is
# intentionally disabled rather than maintained as a divergent path.
#
# Use:
#   bash scripts/local-host-sync-db.sh [--schema-only|--verify]
# ============================================================================
set -euo pipefail

printf '%s\n' "scripts/sync-from-252.sh is retired: use scripts/local-host-sync-db.sh instead." >&2
printf '%s\n' "The supported path owns the dynamic 252 tunnel and runs structure plus data audits." >&2
exit 64
