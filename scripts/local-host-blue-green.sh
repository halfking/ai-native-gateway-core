#!/usr/bin/env bash
# Local blue-green rehearsal for the versioned host layout.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
source "$SCRIPT_DIR/local-host-layout-helper.sh"
source "$SCRIPT_DIR/deploy-lib/zero-downtime.sh"

PORT_ACTIVE="${PORT_ACTIVE:-8781}"
PORT_CANDIDATE="${PORT_CANDIDATE:-8782}"
PROXY_CMD="${PROXY_CMD:-}"

usage() { printf 'usage: %s <version> [--proxy command]\n' "$0"; }
version="${1:-}"
[[ -n "$version" ]] || { usage >&2; exit 64; }
shift
while (($#)); do
  case "$1" in
    --proxy) PROXY_CMD="$2"; shift 2 ;;
    *) usage >&2; exit 64 ;;
  esac
done

bundle=$(lh_bundle_dir "$version")
[[ -x "$bundle/gateway" ]] || { echo "candidate bundle missing: $bundle" >&2; exit 1; }
vars=$(lh_layout_vars)
run_dir=$(printf '%s\n' "$vars" | sed -n 's/^run_dir=//p')
mkdir -p "$run_dir"

# The local adapter intentionally requires a proxy command. Direct clients
# cannot be switched without dropping the listener, so they use local-host's
# existing single-port rollback path instead.
[[ -n "$PROXY_CMD" ]] || { echo "local blue-green requires --proxy; direct 8781 fallback is not blue-green" >&2; exit 2; }

old=$(lh_active_version)
old_port="$PORT_ACTIVE"
new_port="$PORT_CANDIDATE"
start_ns=$(zd_now_ns)

candidate_pid="$run_dir/candidate-${new_port}.pid"
(
  source "$bundle/env.sh"
  export LLM_GATEWAY_LISTEN=":$new_port"
  export LLM_GATEWAY_RUNTIME_ROLE=traffic-only
  cd "$bundle"
  nohup ./gateway >"$run_dir/candidate-${new_port}.log" 2>&1 &
  echo $! >"$candidate_pid"
)

cleanup_candidate() {
  if [[ -f "$candidate_pid" ]]; then
    kill "$(cat "$candidate_pid")" 2>/dev/null || true
    rm -f "$candidate_pid"
  fi
}
trap cleanup_candidate EXIT

for _ in {1..30}; do
  curl -fsS --max-time 1 "http://127.0.0.1:$new_port/healthz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS --max-time 2 "http://127.0.0.1:$new_port/healthz" >/dev/null
curl -fsS --max-time 2 "http://127.0.0.1:$new_port/readyz" >/dev/null

# The proxy command receives the selected upstream port. A real local adapter
# can be nginx -s reload; the rehearsal harness may simply record the value.
"$PROXY_CMD" "$new_port"
end_ns=$(zd_now_ns)
elapsed_ms=$(( (end_ns - start_ns) / 1000000 ))
zd_handoff_record "$old_port" "$new_port" "$elapsed_ms" "$old" "$version"

# Promote only after proxy acknowledgement. Keep the old process alive until
# the caller's post-switch smoke test completes; this script never kills it.
ln -sfn "$version" "$(printf '%s\n' "$vars" | sed -n 's/^bin_dir=//p')/current"
trap - EXIT
