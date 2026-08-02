#!/usr/bin/env bash
# Regression tests for the fail-closed migration history gate.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT/scripts/deploy-lib/db-changelog.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fake_ssh="$TMP/fake-ssh.sh"
cat >"$fake_ssh" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${*: -1}" >>"${FAKE_SSH_LOG:?}"
case "${FAKE_SSH_MODE:?}" in
  gate-duplicate) printf '%s\n' '2|0|0|missing' ;;
  gate-inconsistent) printf '%s\n' '0|1|1|present' ;;
  gate-no-key) printf '%s\n' '0|0|0|present' ;;
  gate-ok) printf '%s\n' '0|0|1|present' ;;
  *) exit 91 ;;
esac
FAKE
chmod +x "$fake_ssh"

# shellcheck disable=SC1090
source "$LIB"

run_gate() {
  local mode=$1 expected=$2
  : >"$TMP/ssh.log"
  FAKE_SSH_MODE="$mode" FAKE_SSH_LOG="$TMP/ssh.log" \
    _deploy_migration_history_gate "$fake_ssh" /tmp/env >/dev/null 2>"$TMP/error.log" && rc=0 || rc=$?
  [[ "$rc" == "$expected" ]] || {
    printf 'FAIL mode=%s expected=%s actual=%s\n' "$mode" "$expected" "$rc" >&2
    cat "$TMP/error.log" >&2
    exit 1
  }
}

run_gate gate-duplicate 1
run_gate gate-inconsistent 1
run_gate gate-no-key 1
run_gate gate-ok 0

printf 'migration gate regression tests: 4 passed\n'
