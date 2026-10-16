#!/usr/bin/env bash
# Explicit session-write opt-ins must survive both local runtime shapes.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/deploy-local-lib.sh" || { printf 'FATAL: 无法 source %s\n' ""$ROOT/scripts/deploy-local-lib.sh"" >&2; exit 1; }
TASK_TMP="$(mktemp -d -t session-write-env.XXXXXX)"
trap 'rm -rf "$TASK_TMP"' EXIT
export LLM_GATEWAY_ROOT="$TASK_TMP/root"

assert_value() {
  local file="$1" key="$2" expected="$3"
  if ! grep -Fqx "$key=$expected" "$file"; then
    printf 'FAIL: emitted runtime environment must preserve %s=%s\n' "$key" "$expected" >&2
    exit 1
  fi
}

for shape in 0 1; do
  export DL_DOCKER="$shape"
  for value in true false; do
    export LLM_GATEWAY_SESSIONS_V2_ENABLED="$value"
    export LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE="$value"
    dl_write_env "$TASK_TMP/runtime.env" 8782
    assert_value "$TASK_TMP/runtime.env" LLM_GATEWAY_SESSIONS_V2_ENABLED "$value"
    assert_value "$TASK_TMP/runtime.env" LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE "$value"
  done
  unset LLM_GATEWAY_SESSIONS_V2_ENABLED LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE
  dl_write_env "$TASK_TMP/runtime.env" 8782
  assert_value "$TASK_TMP/runtime.env" LLM_GATEWAY_SESSIONS_V2_ENABLED ''
  assert_value "$TASK_TMP/runtime.env" LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE ''
  if grep -Eq '^LLM_GATEWAY_SESSIONS_V2_(L3_READ|PRIMARY_READ|DUAL_READ)=true$' "$TASK_TMP/runtime.env"; then
    printf 'FAIL: session-write environment must not enable read experiments\n' >&2
    exit 1
  fi
done
printf 'PASS: explicit session write settings survive host/docker runtime environment\n'
