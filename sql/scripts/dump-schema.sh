#!/usr/bin/env bash
# dump-schema.sh — Regenerate 00-prereqs.sql + 01-schema.sql from a live database
# Usage:
#   DB_INIT_OUT_DIR=/path/to/output LLM_GATEWAY_DATABASE_URL=... ./dump-schema.sh
#
# Round 43: this wrapper used to source "$SCRIPT_DIR/../../../../scripts/_lib/
# db-init-lib.sh". That path climbs out of this repository into a sibling
# workspace tree, so the script could never run here — and git history shows
# the library was never in this repository either (git log --all --
# '*db-init-lib.sh' is empty). The script had been copy-pasted out of the
# official-deploy monorepo with monorepo-relative paths intact, and had been
# unrunnable since 28d4d8612 (2026-07-05). The library now lives in-repo at
# scripts/_lib/, so the path is repo-relative.
#
# The library also carried a monorepo output layout (services/<name>/deploy/sql)
# that no longer exists even in the monorepo, so DB_INIT_OUT_DIR is required.
#
# All logic lives in scripts/_lib/db-init-lib.sh (the SSOT). This wrapper just
# sources the lib and calls the right function.
#
# CAUTION: db_init::dump_schema post-processes the pg_dump output (it relocates
# some definitions into a DEFERRED FUNCTIONS block). That post-processing is the
# origin of the forward-reference failures round 42 fixed by hand in the
# committed baseline. Generate into a scratch dir and diff against the committed
# baseline; do not overwrite it in place.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
source "$REPO_ROOT/scripts/_lib/db-init-lib.sh"

if [[ -z "${DB_INIT_OUT_DIR:-}" ]]; then
  echo "ERROR: DB_INIT_OUT_DIR is required." >&2
  echo "  This repo keeps its committed baseline in three places, and the" >&2
  echo "  generator emits exactly one output. Generate to a scratch dir and" >&2
  echo "  diff, e.g.:" >&2
  echo "    DB_INIT_OUT_DIR=/tmp/baseline LLM_GATEWAY_DATABASE_URL=... \\" >&2
  echo "      bash sql/scripts/dump-schema.sh" >&2
  echo "    diff -u sql/schema/01-schema.sql /tmp/baseline/01-schema.sql" >&2
  exit 1
fi

URL=$(db_init::resolve_db_url) || { echo "ERROR: set LLM_GATEWAY_DATABASE_URL" >&2; exit 1; }
db_init::dump_prereqs "llm-gateway-go" "$URL"
echo
db_init::dump_schema  "llm-gateway-go" "llm_gateway" "$URL"
