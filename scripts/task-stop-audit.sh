#!/usr/bin/env bash
# task-stop-audit.sh — verify the repository stop summary before handoff/commit.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SUMMARY="$REPO_ROOT/.acc-task-stop-summary.md"

usage() {
  cat <<'EOF'
Usage:
  scripts/task-stop-audit.sh capture [summary-path]
  scripts/task-stop-audit.sh verify [summary-path]
EOF
}

capture() {
  local output="${1:-$SUMMARY}"
  local branch head status changed
  branch=$(git -C "$REPO_ROOT" branch --show-current)
  head=$(git -C "$REPO_ROOT" rev-parse HEAD)
  status=$(git -C "$REPO_ROOT" status --short)
  changed=$(git -C "$REPO_ROOT" status --short | wc -l | tr -d ' ')
  {
    printf '%s\n' '## Task Stop Summary'
    printf '%s\n' '- status: ready_to_stop'
    printf '%s\n' '- goal: <describe the task goal>'
    printf '%s\n' "- baseline: branch=$branch; head=$head"
    printf '%s\n' "- changed_files: $changed worktree entries"
    printf '%s\n' '- in_scope_files: <list files or directories>'
    printf '%s\n' '- out_of_scope_files: <none or list>'
    printf '%s\n' '- unnecessary_changes: <none or list>'
    printf '%s\n' '- accidental_changes: <none or list>'
    printf '%s\n' '- verification: <commands and evidence>'
    printf '%s\n' '- unresolved: <none or risks>'
    printf '%s\n' '- next_action: <handoff, commit, deploy, or stop>'
    printf '\n%s\n' '## Worktree Snapshot'
    if [[ -n "$status" ]]; then
      printf '%s\n' '```text'
      printf '%s\n' "$status"
      printf '%s\n' '```'
    else
      printf '%s\n' 'clean'
    fi
  } > "$output"
  echo "wrote $output"
}

verify() {
  local input="${1:-$SUMMARY}"
  [[ -f "$input" ]] || { echo "missing summary: $input" >&2; exit 1; }
  local required field
  for field in goal baseline changed_files in_scope_files out_of_scope_files \
    unnecessary_changes accidental_changes verification unresolved next_action; do
    required="- $field:"
    rg -q "^${required}" "$input" || {
      echo "summary missing field: $field" >&2
      exit 1
    }
  done
  git -C "$REPO_ROOT" diff --check
  if rg -n '^(<<<<<<<|>>>>>>>)' --glob '!vendor/**' --glob '!web/node_modules/**' "$REPO_ROOT"; then
    echo "merge conflict marker found" >&2
    exit 1
  fi
  echo "task-stop audit PASS: $input"
}

case "${1:-}" in
  capture) capture "${2:-}" ;;
  verify) verify "${2:-}" ;;
  -h|--help) usage ;;
  *) usage >&2; exit 2 ;;
esac
