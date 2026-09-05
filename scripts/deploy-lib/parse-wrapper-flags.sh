#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/parse-wrapper-flags.sh — shared flag pre-parsing
#
# Both deploy-154.sh and deploy-245.sh need to strip operator-level
# flags (e.g. --force-unlock) BEFORE delegating to deploy-seamless.sh,
# because those flags are handled locally and must not reach the
# delegated command. This module holds that parsing so the two wrappers
# stay DRY.
#
# Usage (in a wrapper that has already done `set -euo pipefail` and
# defined SCRIPT_DIR):
#
#   source "$SCRIPT_DIR/deploy-lib/parse-wrapper-flags.sh"
#   declare -a ARGS=()
#   FORCE_UNLOCK=0
#   extract_force_unlock FORCE_UNLOCK ARGS "$@"
#   # now $FORCE_UNLOCK is set and ${ARGS[@]} holds the remaining args
#   # --force and --force-unlock both set FORCE_UNLOCK=1
#
# --force and --force-unlock are operator-level recovery flags. Both are
# stripped before delegation; every unrecognized token passes through to
# ARGS unchanged.
# =====================================================================

if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "parse-wrapper-flags.sh: requires bash" >&2
  return 1 2>/dev/null || exit 1
fi

# extract_force_unlock <flag_var> <args_var> <args...>
#
# Strips --force/--force-unlock from the argument list, sets the boolean flag
# variable to 1, and writes the remaining tokens back into the args array
# variable. Uses nameref (bash 4.3+) so the caller's variables
# are updated in place.
extract_force_unlock() {
  local _flag_var=$1 _args_var=$2; shift 2
  local _flag=0
  local -a _rest=()
  local _arg
  for _arg in "$@"; do
    case "$_arg" in
      --force|--force-unlock) _flag=1 ;;
      *)              _rest+=("$_arg") ;;
    esac
  done
  # Write back through namerefs.
  printf -v "$_flag_var" '%s' "$_flag"
  # Rebuild the caller's array in place.
  local -n _args_ref="$_args_var"
  _args_ref=("${_rest[@]}")
  return 0
}

# Convenience: parse common wrapper flags in one call and echo a
# machine-readable summary. Kept simple — wrappers usually just need
# extract_force_unlock, but this is here if a third flag appears.
parse_wrapper_flags() {
  local _flag_var=${1:-FORCE_UNLOCK} _args_var=${2:-ARGS}; shift 2
  extract_force_unlock "$_flag_var" "$_args_var" "$@"
}
