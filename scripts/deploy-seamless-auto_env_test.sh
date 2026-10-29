#!/usr/bin/env bash
# Execute the deployment engine's own read/write/inject/forward blocks locally.
set -euo pipefail
self=$(cd "$(dirname "$0")" && pwd)
engine=${ENGINE:-$self/deploy-seamless-docker.sh}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# Even a broken validation gate must not reach a real SSH/Docker command.
mkdir "$tmp/bin"
for tool in ssh scp docker; do
  printf '#!/usr/bin/env bash\necho unexpected_external_command >&2\nexit 97\n' > "$tmp/bin/$tool"
  chmod +x "$tmp/bin/$tool"
done
block() {
  awk -v label="$1" '
    index($0, label ":BEGIN") { found=1; active=1; next }
    index($0, label ":END") { if (active) { closed=1; exit } }
    active { print }
    END { if (!found || !closed) exit 1 }
  ' "$engine"
}
read_block=$(block AUTO-GENERATOR-ENV) || { echo 'missing auto flag reader' >&2; exit 1; }
write_block=$(block GATEWAY-ENV-WRITE)
inject_block=$(block AUTO-GENERATOR-INJECT)
# 2026-10-29 客户端识别轮：GATEWAY-ENV-WRITE 块引用了同一文件更早段落的
# TRUSTED_PROXY_CIDRS（可信代理网段分支），沙箱只抽块不抽全文件 ⇒ set -u 下
# 必须在这里先给缺省值（空 = 分支不写，正是要测的缺省形态）。
TRUSTED_PROXY_CIDRS=''
forward_block=$(awk '/^  _dd_fwd=""$/ { active=1 } active { print } active && /^  done$/ { exit }' "$engine")
[[ -n "$forward_block" ]]
die() { echo "$*" >&2; exit 1; }
assert_equal() { [[ "$1" == "$2" ]] || die "$3"; }
run_case() (
  unset LLM_GATEWAY_AUTO_TITLE_ENABLED LLM_GATEWAY_AUTO_SUMMARY_ENABLED
  ENV_FILE="$1"
  shift
  while [[ $# -gt 0 ]]; do export "${1?}"; shift; done
  # No provider, database or deployment credentials are read by this fixture.
  export DD_HOST=test DB_URL=placeholder REDIS_ADDR=placeholder REDIS_DB=1
  export REDIS_PASSWORD='' REDIS_URL=placeholder SECRET_KEY=placeholder CORS_ORIGINS=''
  export CRED_ENC_KEY='' URSM_REDIS_DB=2 GW_STATIC_API_KEY=placeholder GW_ADMIN_API_KEY=placeholder
  export GW_CURSOR_HMAC=placeholder HARD_BLOCK_RULES='' ASM_INTERNAL_ENDPOINT='' ASM_EVENT_SECRET=''
  export LLM_GATEWAY_ASM_BASE_URL='' LLM_GATEWAY_ASM_PLUGIN_ID=''
  # 2026-10-29 客户端识别轮：write_block 新增了可信代理网段分支（同 HARD_BLOCK_RULES
  # 的空值可省写形态），set -u 沙箱里必须先给值。
  export LLM_GATEWAY_TRUSTED_PROXY_CIDRS=''
  eval "$read_block"
  dd_auto_generator_env "$ENV_FILE"
  eval "$write_block"
  GW_ENV=()
  eval "$inject_block"
  printf '%s\n' "${GW_ENV[@]+"${GW_ENV[@]}"}"
)
title=LLM_GATEWAY_AUTO_TITLE_ENABLED
summary=LLM_GATEWAY_AUTO_SUMMARY_ENABLED
cat > "$tmp/keep" <<EOF
export $title=false
$summary='false'
EOF
out=$(run_case "$tmp/keep")
assert_equal "$out" "$(printf '%s\n' -e "$title=false" -e "$summary=false")" 'false lost during container injection'
grep -qx "export $title=false" "$tmp/keep"
grep -qx "export $summary=false" "$tmp/keep"
# A second deployment reads the previously generated file.
assert_equal "$(run_case "$tmp/keep")" "$out" 'next deployment did not retain both false values'
out=$(run_case "$tmp/keep" "$title=true" "$summary=False")
assert_equal "$out" "$(printf '%s\n' -e "$title=true" -e "$summary=false")" 'explicit environment did not override saved values'
out=$(run_case "$tmp/absent")
assert_equal "$out" '' 'absent flags changed Gateway defaults'
if grep -q 'LLM_GATEWAY_AUTO_.*_ENABLED=' "$tmp/absent"; then die 'absent flags persisted a default'; fi
printf 'export %s="false"\n' "$title" > "$tmp/single"
assert_equal "$(run_case "$tmp/single")" "$(printf '%s\n' -e "$title=false")" 'independent title flag requires summary'
for literal in 0 f F false FALSE False 1 t T true TRUE True; do
  expected=true
  case "$literal" in 0|f|F|false|FALSE|False) expected=false;; esac
  assert_equal "$(run_case "$tmp/bools" "$title=$literal" "$summary=$literal")" \
    "$(printf '%s\n' -e "$title=$expected" -e "$summary=$expected")" 'boolean normalization differs from Gateway'
done
for literal in '' garbage 'false; echo leaked-sentinel' "\$(touch '$tmp/command-ran')" "false' ; echo leaked-sentinel; '"; do
  if run_case "$tmp/invalid" "$title=$literal" > "$tmp/error" 2>&1; then die 'invalid environment accepted'; fi
  if grep -q 'leaked-sentinel\|command-ran' "$tmp/error"; then die 'invalid value escaped into output'; fi
  printf 'export %s=%s\n' "$summary" "$literal" > "$tmp/invalid-file"
  if run_case "$tmp/invalid-file" > "$tmp/error" 2>&1; then die 'invalid saved value accepted'; fi
  # Run the entire actual entry point: validation must happen before SSH or writes.
  if env "$title=$literal" DD_STATE_DIR="$tmp/forbidden-state" PATH="$tmp/bin:$PATH" \
    bash "$engine" --status --via no-such-gateway-fixture > "$tmp/error" 2>&1; then
    die 'entry point accepted invalid flag'
  fi
  grep -q "invalid auto generator boolean: $title" "$tmp/error"
  [[ ! -e "$tmp/forbidden-state" ]] || die 'invalid flag caused state writes'
done
[[ ! -e "$tmp/command-ran" ]] || die 'saved configuration executed a command'
mkdir -p "$tmp/invalid-state/env"
printf 'export %s=garbage\n' "$summary" > "$tmp/invalid-state/env/gateway.env"
if (unset "$title" "$summary"; DD_STATE_DIR="$tmp/invalid-state" PATH="$tmp/bin:$PATH" \
  bash "$engine" --status) > "$tmp/error" 2>&1; then die 'entry point accepted invalid saved flag'; fi
grep -q "invalid auto generator boolean: $summary" "$tmp/error"
forward=$(
  unset LLM_GATEWAY_AUTO_TITLE_ENABLED LLM_GATEWAY_AUTO_SUMMARY_ENABLED
  export "$title=False" "$summary=0"
  eval "$read_block"
  dd_auto_generator_env ''
  _dd_fwd=''
  eval "$forward_block"
  printf '%s' "$_dd_fwd"
)
[[ "$forward" == *" $title='false'"* ]] || die 'title missing from --via forwarding'
[[ "$forward" == *" $summary='false'"* ]] || die 'summary missing from --via forwarding'
printf '%s\n' 'PASS: retained flags, explicit override, absent defaults, boolean normalization, safe rejection and --via forwarding'
