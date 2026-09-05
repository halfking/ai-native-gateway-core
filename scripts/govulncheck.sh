#!/usr/bin/env bash
# govulncheck.sh — Go vulnerability gate for local and CI verification.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

GOVULNCHECK="$(command -v govulncheck 2>/dev/null || true)"
if [[ -z "$GOVULNCHECK" ]] && command -v go >/dev/null 2>&1; then
  GOPATH_BIN="$(go env GOPATH)/bin/govulncheck"
  [[ -x "$GOPATH_BIN" ]] && GOVULNCHECK="$GOPATH_BIN"
fi

if [[ -z "$GOVULNCHECK" ]]; then
  cat >&2 <<'EOF'
govulncheck is required but not installed.
Install it with:
  go install golang.org/x/vuln/cmd/govulncheck@latest
Then rerun:
  ./scripts/govulncheck.sh
EOF
  exit 2
fi

echo "[govulncheck] scanning Go packages and reachable symbols"
"$GOVULNCHECK" ./...
