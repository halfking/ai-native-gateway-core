#!/usr/bin/env bash
# verify-migration-checksums.sh — verify SHA-256 of the SQL migration files
# against the registry recorded in docs/db-changelog.md.
#
# Scans two directories:
#   sql/migrations/startup/ — auto-applied by the deploy pending-migration
#                             scanner and by the installer's startup sequence
#   sql/migrations/manual/  — deliberately NOT auto-applied; an operator runs
#                             these by hand, with an explicit confirmation point
#
# ⚠️ 2026-10-06: manual/ was added because 830 (ursm node snapshot
# partitioning) is manual **by design** (its header: the RENAME +
# CREATE PARENT TABLE half renames a 10 GB live table) yet had been left in
# startup/, where deploy's _deploy_pending_startup_migrations() — which scans
# the directory and consults no registry — ran it on every single deploy.
# Those files still need checksum verification: they are the ones a human is
# most likely to run against production by hand.
#
# Any mismatch or stale registry entry is fatal. Files present on disk but
# absent from the registry are warnings only because db-changelog.md records
# deployed/pending ledger entries rather than every historical startup file.
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIG_DIRS=(
  "$REPO_ROOT/sql/migrations/startup"
  "$REPO_ROOT/sql/migrations/manual"
)
CHANGELOG="$REPO_ROOT/docs/db-changelog.md"

# --quiet suppresses per-file WARN lines (unregistered-on-disk). Useful in CI
# where hundreds of historical startup files are expected to be absent from
# the changelog and the warn spam drowns the real signal (MISMATCH / STALE /
# summary). Exit code is unaffected.
QUIET=false
for arg in "$@"; do
  case "$arg" in
    --quiet|-q) QUIET=true ;;
    *) echo "[verify-checksums] unknown option: $arg" >&2; exit 2 ;;
  esac
done

for d in "${MIG_DIRS[@]}"; do
  if [[ ! -d "$d" ]]; then
    echo "[verify-checksums] missing migrations dir: $d" >&2
    exit 2
  fi
done
if [[ ! -f "$CHANGELOG" ]]; then
  echo "[verify-checksums] missing changelog: $CHANGELOG" >&2
  exit 2
fi

if command -v shasum >/dev/null 2>&1; then
  _sha() { shasum -a 256 "$1" | awk '{print $1}'; }
elif command -v sha256sum >/dev/null 2>&1; then
  _sha() { sha256sum "$1" | awk '{print $1}'; }
else
  echo "[verify-checksums] neither shasum nor sha256sum available" >&2
  exit 2
fi

fail=0
missing_in_registry=0
mismatch=0

# Build map: filename -> recorded sha256 from changelog table rows.
# Changelog rows look like: `| 614 | `614_session_bodies_hot.sql` | `<sha>` | ...`
declare -A recorded
while IFS= read -r line; do
  # Match: `| <num> | `<name>` | `<sha>` | <status> |`
  if [[ "$line" =~ \|\ [0-9]+\ \|\ \`([0-9]+_[a-zA-Z0-9_]+\.sql)\`\ \|\ \`([0-9a-f]{64})\`\ \|\  ]]; then
    name="${BASH_REMATCH[1]}"
    sha="${BASH_REMATCH[2]}"
    # A file may legitimately appear more than once when its SHA was
    # updated by an audit-and-rewrite (e.g. the 627
    # candidate_failure_logs_aggregation_id_unified fix, where the
    # changelog carries both a `pending deploy` row and an
    # `applied+verified` row). The verification step accepts ANY of the
    # recorded SHAs as a match, so we accumulate them in a newline-
    # delimited string per file name.
    existing="${recorded[$name]:-}"
    if [[ -z "$existing" ]]; then
      recorded[$name]="$sha"
    else
      recorded[$name]="$existing
$sha"
    fi
  fi
done < "$CHANGELOG"

if [[ ${#recorded[@]} -eq 0 ]]; then
  echo "[verify-checksums] no registry rows parsed from $CHANGELOG" >&2
  exit 2
fi

# Walk every .sql under the migration dirs; compare to the recorded map.
# Files present on disk but absent from the registry are reported as
# warnings only — db-changelog.md only records recent deploys, so older
# startup/ files are expected to be unregistered.
#
# basename_seen guards the one hazard that came with scanning two directories:
# the recorded map is keyed by bare filename, so the same basename in both
# startup/ and manual/ would be ambiguous. Identical content is tolerated;
# differing content is fatal rather than silently "first dir wins".
declare -A basename_seen
while IFS= read -r -d '' f; do
  base=$(basename "$f")
  actual=$(_sha "$f")
  if [[ -n "${basename_seen[$base]:-}" && "${basename_seen[$base]}" != "$actual" ]]; then
    echo "[verify-checksums] AMBIGUOUS-BASENAME: $base exists in more than one migrations dir with different content" >&2
    echo "  ${basename_seen[$base]}" >&2
    echo "  $actual  ($f)" >&2
    fail=1
    continue
  fi
  basename_seen[$base]="$actual"
  expected="${recorded[$base]:-}"
  if [[ -z "$expected" ]]; then
    if [[ "$QUIET" != true ]]; then
      echo "[verify-checksums] WARN: missing-in-registry: $base (on disk but not in changelog)" >&2
    fi
    missing_in_registry=$((missing_in_registry + 1))
    continue
  fi
  # The recorded value may be a newline-delimited list of acceptable SHAs
  # (the changelog may carry a `pending deploy` row + an `applied+verified`
  # row for the same file after an audit-and-rewrite). Accept any.
  match=0
  while IFS= read -r cand; do
    if [[ "$cand" == "$actual" ]]; then
      match=1
      break
    fi
  done <<< "$expected"
  if [[ "$match" -ne 1 ]]; then
    echo "[verify-checksums] MISMATCH: $base" >&2
    echo "  recorded: $expected" >&2
    echo "  actual:   $actual" >&2
    mismatch=$((mismatch + 1))
    fail=1
  fi
done < <(for d in "${MIG_DIRS[@]}"; do find "$d" -maxdepth 1 -type f -name '*.sql' -print0; done | sort -z)

# Detect stale entries (recorded but no on-disk file). These are fatal:
# the changelog references a file that the deploy will not ship.
for name in "${!recorded[@]}"; do
  found=0
  for d in "${MIG_DIRS[@]}"; do
    [[ -f "$d/$name" ]] && { found=1; break; }
  done
  if [[ "$found" -ne 1 ]]; then
    echo "[verify-checksums] STALE-IN-REGISTRY: $name (no on-disk file in ${MIG_DIRS[*]})" >&2
    fail=1
  fi
done

if [[ "$fail" -ne 0 ]]; then
  echo "[verify-checksums] FAILED: $mismatch mismatch ($missing_in_registry unregistered on disk)" >&2
  exit 1
fi

echo "[verify-checksums] OK: $((${#recorded[@]})) registered migrations verified, $missing_in_registry unregistered (warn-only)"
exit 0
