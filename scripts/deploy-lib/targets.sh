#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/targets.sh — target contracts for the canonical CLI
#
# Implements the "Target contracts" deliverable from spec
# 2026-07-13-deployment-management-hardening-design.md §"Slice 1":
#
#   Each canonical target exposes a single bash function
#   `target_<name>_contract` that prints a normalized JSON document with
#   these fields (fixed by tests/fixtures/plan_schema.json):
#
#     target                canonical key (154, 245, ...)
#     support               "canonical" | "retired" | "deferred" | "sops-only"
#     service_manager       "systemd" | "k3s" | "launchd" | ""
#     service_name          unit / deployment name
#     binary_path           absolute path to the gateway executable
#     web_path              absolute path to the web/ static root
#     health_url            local URL that should answer 200 after restart
#     ssh_host              user@host[:port] for ssh/scp, empty if N/A
#     ssh_key_env           name of the SSH_KEY_<TARGET> env var
#     rollback_policy       "versioned" | "runbook" | "refuse"
#     legacy_aliases        list of accepted aliases that rewrite to this
#                           canonical target (e.g. "71" -> 154)
#
#   A second family of helpers (`target_status`, `target_service_manager`)
#   reads those fields back. All access from scripts/deploy.sh goes
#   through these helpers — scripts/deploy.sh never reads positional
#   parameters directly.
#
#   Status semantics (per spec §"Target Matrix"):
#     canonical  — supported, full plan / deploy / verify / rollback
#     retired    — refuse all mutating actions; 186 falls here
#     deferred   — refuse because topology / runtime contract is unresolved
#     sops-only  — service contract unknown; SOPS coverage only
#
#   This slice only adds the contract layer. The actual deploy / verify /
#   rollback implementations land in slices 4 and 5.
# =====================================================================

# Empty-string guards against accidental sourcing in non-bash shells.
if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "targets.sh: requires bash" >&2
  # shellcheck disable=SC2317  # only reached when invoked directly (not sourced)
  return 1 2>/dev/null || exit 1
fi

# shellcheck disable=SC2317  # errexit / nounset are opt-in for the caller
# This file is sourced as a library; the calling orchestrator (scripts/deploy.sh)
# owns its own `set -euo pipefail`. We deliberately avoid it here so that the
# library helpers can be invoked from test harnesses with looser failure modes.

# ---- internal helpers --------------------------------------------------

# Print a JSON object using only bash string concat (no python / jq
# dependency). Numbers and bare words are emitted unquoted; values that
# contain spaces or shell-special characters go through json_escape.
_json_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  printf '%s' "$s"
}

# Emit a JSON object built from alternating keys and values. Trailing
# commas are not allowed — callers pass exactly an even number of args.
# Strings are always quoted; numeric and boolean literals are detected
# via a leading sigil so callers can opt in (e.g. ":true" for true).
_json_object() {
  local first=1 out="{"
  while [[ $# -ge 2 ]]; do
    if [[ $first -eq 0 ]]; then out+=","; fi
    first=0
    out+="\"$1\":"
    shift
    local raw=$1
    case "$raw" in
      ":true")  out+="true" ;;
      ":false") out+="false" ;;
      ":null")  out+="null" ;;
      *)        out+="\"$(_json_escape "$raw")\"" ;;
    esac
    shift
  done
  out+="}"
  printf '%s\n' "$out"
}

# Render a JSON array from a list passed via positional args. Empty input
# emits "[]".
_json_array() {
  if [[ $# -eq 0 ]]; then
    printf '[]\n'
    return
  fi
  local out="[" first=1 v
  for v in "$@"; do
    if [[ $first -eq 0 ]]; then out+=","; fi
    first=0
    out+="\"$(_json_escape "$v")\""
  done
  out+="]"
  printf '%s\n' "$out"
}

# ---- shared value resolvers (env-overridable) --------------------------

# All ssh host / key lookups prefer the per-target env override (e.g.
# SSH_KEY_245), fall back to the global defaults set in scripts/deploy.sh,
# and otherwise return the documented value. This keeps the canonical CLI
# compatible with both fresh invocations and CI overrides.

_ssh_key_for() {
  local target=$1
  local key_var="SSH_KEY_${target^^}"
  local v="${!key_var:-}"
  if [[ -n "$v" ]]; then
    printf '%s\n' "$v"
    return
  fi
  case "$target" in
    154|71)        printf '%s\n' "${HOME}/.ssh/id_ed25519" ;;
    245)           printf '%s\n' "${HOME}/.ssh/id_ed25519" ;;
    252|184)       printf '%s\n' "${HOME}/.ssh/id_ed25519" ;;
    186)           printf '%s\n' "${HOME}/.ssh/id_ed25519" ;;
    kaixuan-1)     printf '%s\n' "${HOME}/.ssh/kaixuan1_id_rsa" ;;
    kaixuan-2)     printf '%s\n' "${HOME}/.ssh/kaixuan2_id_rsa" ;;
    kaixuan-3)     printf '%s\n' "${HOME}/.ssh/kaixuan3_id_rsa" ;;
    *)             printf '%s\n' "" ;;
  esac
}

_ssh_host_for() {
  local target=$1
  case "$target" in
    154|71)        printf '%s\n' "root@47.97.111.154" ;;
    245)           printf '%s\n' "root@8.136.114.245" ;;
    252|184)       printf '%s\n' "root@115.29.212.252" ;;
    186)           printf '%s\n' "root@118.31.18.168" ;;
    kaixuan-1)     printf '%s\n' "kaixuan@192.168.31.28" ;;
    kaixuan-2)     printf '%s\n' "kaixuan@192.168.31.19" ;;
    kaixuan-3)     printf '%s\n' "kaixuan@192.168.31.30" ;;
    *)             printf '%s\n' "" ;;
  esac
}

# ---- canonical target contracts -----------------------------------------

# Slice 1 / 5 target: 154 (production gateway, host-mode systemd).
# Mirrors the same contract as 245 (release bundle + versioned rollback)
# but rollback stays on the existing runbook for this slice.
target_154_contract() {
  _json_object \
    target "154" \
    support "canonical" \
    service_manager "systemd" \
    service_name "llm-gateway-go.service" \
    binary_path "/opt/llm-gateway-go/llm-gateway-go" \
    web_path "/opt/llm-gateway-go/web" \
    health_url "http://127.0.0.1:8781/healthz" \
    ssh_host "$(_ssh_host_for 154)" \
    ssh_key_env "SSH_KEY_154" \
    rollback_policy "runbook" \
    legacy_aliases "71"
}

# Slice 1 / 4 target: 245 (gateway server, full versioned rollback).
target_245_contract() {
  _json_object \
    target "245" \
    support "canonical" \
    service_manager "systemd" \
    service_name "llmgo-245.service" \
    binary_path "/opt/llm-gateway-go/gateway" \
    web_path "/opt/llm-gateway-go/web" \
    health_url "http://127.0.0.1:8781/healthz" \
    ssh_host "$(_ssh_host_for 245)" \
    ssh_key_env "SSH_KEY_245" \
    rollback_policy "versioned" \
    legacy_aliases ""
}

# Slice 1 retirement: 186 must fail with explicit guidance before any
# lock / build / ssh operation. The contract still renders so that
# `plan 186` reports the retirement context.
target_186_contract() {
  _json_object \
    target "186" \
    support "retired" \
    service_manager "systemd" \
    service_name "llm-gateway-go.service" \
    binary_path "/opt/llm-gateway-go/llm-gateway-go" \
    web_path "/opt/llm-gateway-go/web" \
    health_url "http://127.0.0.1:8781/healthz" \
    ssh_host "$(_ssh_host_for 186)" \
    ssh_key_env "SSH_KEY_186" \
    rollback_policy "refuse" \
    legacy_aliases ""
}

# Slice 1 deferral: 252 / 184 — service-manager conflict not yet
# resolved (inventory says systemd, canary script says k3s). The
# canonical CLI refuses to guess.
target_252_contract() {
  _json_object \
    target "252" \
    support "deferred" \
    service_manager "" \
    service_name "" \
    binary_path "" \
    web_path "" \
    health_url "" \
    ssh_host "$(_ssh_host_for 252)" \
    ssh_key_env "SSH_KEY_252" \
    rollback_policy "refuse" \
    legacy_aliases "184"
}

# Slice 1 deferral: kaixuan-1 — SOPS coverage only because the runtime
# contract is unresolved (inventory: macOS launchd; canonical script: k3s).
target_kaixuan_1_contract() {
  _json_object \
    target "kaixuan-1" \
    support "sops-only" \
    service_manager "" \
    service_name "" \
    binary_path "" \
    web_path "" \
    health_url "" \
    ssh_host "$(_ssh_host_for kaixuan-1)" \
    ssh_key_env "SSH_KEY_KAIXUAN_1" \
    rollback_policy "refuse" \
    legacy_aliases ""
}

# Unsupported local hosts — they are explicitly out of scope this slice.
target_unsupported_contract() {
  local target=$1
  _json_object \
    target "$target" \
    support "unsupported" \
    service_manager "" \
    service_name "" \
    binary_path "" \
    web_path "" \
    health_url "" \
    ssh_host "$(_ssh_host_for "$target")" \
    ssh_key_env "SSH_KEY_${target^^}" \
    rollback_policy "refuse" \
    legacy_aliases ""
}

# ---- dispatcher --------------------------------------------------------

# Render the contract for a canonical / legacy target. The argument is
# post-alias-resolution (i.e. 71 → 154 happens in scripts/deploy.sh).
target_contract() {
  local target=${1:-}
  case "$target" in
    154)            target_154_contract ;;
    245)            target_245_contract ;;
    186)            target_186_contract ;;
    252)            target_252_contract ;;
    kaixuan-1)      target_kaixuan_1_contract ;;
    kaixuan-2|kaixuan-3) target_unsupported_contract "$target" ;;
    *)              target_unsupported_contract "$target" ;;
  esac
}

# Look up a single field from a target's contract. Implementation note:
# we deliberately avoid jq to keep the offline test harness dependency-
# free. The contract is a single-line JSON object with stable key order
# (see _json_object), so a bash regex extractor is sufficient.
target_field() {
  local target=$1 field=$2
  local contract
  contract=$(target_contract "$target")
  # Match "<field>":<value> where <value> is either "..." (string) or a
  # bare word (number / true / false / null).
  if [[ $contract =~ \"$field\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
    return
  fi
  if [[ $contract =~ \"$field\"[[:space:]]*:[[:space:]]*([^,}[:space:]]+) ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
    return
  fi
  printf '\n'
}

# ---- public high-level helpers (consumed by scripts/deploy.sh) ---------

# Refuse with a clear message before any lock / build / ssh operation.
target_check_actionable() {
  local target=$1
  # shellcheck disable=SC2034  # action is part of the public contract for callers
  local action=$2
  local support
  support=$(target_field "$target" support)
  case "$support" in
    canonical) return 0 ;;
    retired)
      echo "ERROR: target $target is retired. Use 154 / 245 instead." >&2
      return 64
      ;;
    deferred)
      echo "ERROR: target $target is deferred. Repository evidence conflicts and the canonical CLI refuses to guess." >&2
      echo "       See spec §'Target Matrix' for the resolution checklist." >&2
      return 64
      ;;
    sops-only)
      echo "ERROR: target $target is sops-only: runtime contract unknown, deployment not yet unified." >&2
      return 64
      ;;
    unsupported|"")
      echo "ERROR: target $target is not supported by this CLI." >&2
      return 64
      ;;
  esac
}

# List every canonical / legacy / deferred target — used by tests and by
# the `--list-targets` help flag.
target_list_all() {
  printf 'canonical\t154\n'
  printf 'canonical\t245\n'
  printf 'retired\t186\n'
  printf 'deferred\t252\n'
  printf 'deferred\t184\n'
  printf 'sops-only\tkaixuan-1\n'
  printf 'unsupported\tkaixuan-2\n'
  printf 'unsupported\tkaixuan-3\n'
  printf 'legacy-alias\t71\n'
}

# Resolve a legacy alias (e.g. "71" -> "154"). Returns the input on a
# miss so callers can pass canonical / legacy / unknown indifferently.
target_resolve_alias() {
  case "$1" in
    71)  printf '%s\n' "154" ;;
    184) printf '%s\n' "252" ;;
    *)   printf '%s\n' "$1" ;;
  esac
}