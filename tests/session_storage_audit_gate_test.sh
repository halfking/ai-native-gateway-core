#!/usr/bin/env bash
# Exercise release-gate decisions without starting Docker or running Go.
set -euo pipefail
cd "$(dirname "$0")/.."
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/repo/tests" "$scratch/repo/scripts/audit" "$scratch/bin" "$scratch/no-docker"
cp tests/session_storage_audit_gate.sh "$scratch/repo/tests/"
ln -s "$(command -v dirname)" "$scratch/no-docker/dirname"
cat > "$scratch/repo/scripts/audit/run-session-storage-stream.sh" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$GATE_ARGS"
exit "${GATE_RUNNER_RC:-0}"
SH
cat > "$scratch/bin/docker" <<'SH'
#!/usr/bin/env bash
case "$1" in
  info) exit "${GATE_DAEMON_RC:-0}" ;;
  image) exit "${GATE_IMAGE_RC:-0}" ;;
  *) exit 2 ;;
esac
SH
chmod +x "$scratch/bin/docker"
bash_path=$(command -v bash)
export GATE_ARGS="$scratch/args"
run_gate() {
  "$bash_path" "$scratch/repo/tests/session_storage_audit_gate.sh" "$@" > "$scratch/output" 2>&1
}
must_fail() {
  if run_gate "$@"; then
    echo "FAIL: required storage gate accepted unavailable dependencies or a failed runner" >&2
    return 1
  fi
}

# A successful release must never mean that this suite did not run.
original_path=$PATH
PATH="$scratch/no-docker" must_fail --require-coherence
PATH="$scratch/bin:$original_path" GATE_DAEMON_RC=1 must_fail --require-coherence
PATH="$scratch/bin:$original_path" GATE_IMAGE_RC=1 must_fail --require-coherence
PATH="$scratch/bin:$original_path" GATE_RUNNER_RC=17 must_fail

PATH="$scratch/bin:$original_path" run_gate
if ! rg -qx -- '--require-coherence' "$GATE_ARGS"; then
  echo 'FAIL: default release gate did not require coherence' >&2
  exit 1
fi
PATH="$scratch/bin:$original_path" run_gate --race
if ! rg -qx -- '--race' "$GATE_ARGS"; then
  echo 'FAIL: race option was not forwarded' >&2
  exit 1
fi
if ! rg -qx -- '--require-coherence' "$GATE_ARGS"; then
  echo 'FAIL: race mode disabled coherence' >&2
  exit 1
fi
echo 'PASS: storage gate requires dependencies, coherence, and a successful runner'
