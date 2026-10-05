#!/usr/bin/env bash
# Per-file coverage evidence for the 820 fix.
#
# Why this exists: canonical_delivery_path_check in
# scripts/apply-db-revision-sequence_test.sh does `return 1` on the FIRST
# uncovered ≥690 file. 819 sorts before 820, so while 819's .sql is on disk
# (restored by 251fc9a7a to satisfy verify-migration-checksums.sh) the full-gate
# run reports 819 and never reaches 820 — the full gate is byte-identical with
# and without the 820 line, so it is NOT evidence about 820 either way.
#
# This harness reuses the script's own three real lists and its own grep
# matching, but reports coverage PER FILE, so one uncovered file cannot hide
# the next. Same inputs, same logic, observable per file.
set -uo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR" || exit 1

startup_files=$(grep -E '^[[:space:]]*"[0-9]{3}_.*\.sql",?$' installer/internal/dbinit/runner.go \
  | sed -E 's/^[[:space:]]*"([0-9]{3}_[^"]+)".*/\1/' | sort -u)
sequence_files=$(grep -E '"\$ROOT_DIR/sql/migrations/startup/[0-9]{3}_[^"]+\.sql"' scripts/apply-db-revision-sequence.sh \
  | sed -nE 's#.*startup/([0-9]{3}_[^"]+\.sql)".*#\1#p' | sort -u)
ensure_allowlist=$(cat <<'EOF'
690_session_summaries_archived_ttl_index.sql
704_plan_quota_probe_backoff.sql
709_work_type_route_coverage.sql
715_route_incidents_pending_state.sql
820_audio_modality_backfill.sql
EOF
)

covered() {
  local name="$1"
  printf '%s\n' "$startup_files"  | grep -Fxq "$name" && { echo StartupFiles; return; }
  printf '%s\n' "$sequence_files" | grep -Fxq "$name" && { echo sequence; return; }
  printf '%s\n' "$ensure_allowlist" | grep -Fxq "$name" && { echo ensure_allowlist; return; }
  echo "*** UNCOVERED ***"
}

rc_all=0
for f in 819_request_abandoned.sql 820_audio_modality_backfill.sql 821_session_turns_abandoned_marker.sql; do
  c=$(covered "$f")
  printf '%-46s -> %s\n' "$f" "$c"
  [[ "$c" == "*** UNCOVERED ***" ]] && rc_all=1
done
echo
echo "A/B: 820 with the ensure_allowlist line REMOVED (negative control)"
ensure_allowlist=$(printf '%s\n' "$ensure_allowlist" | grep -v '^820_audio_modality_backfill.sql$')
printf '%-46s -> %s\n' "820_audio_modality_backfill.sql" "$(covered 820_audio_modality_backfill.sql)"
echo "(that control MUST read *** UNCOVERED ***, otherwise the positive result above is vacuous)"
exit $rc_all
