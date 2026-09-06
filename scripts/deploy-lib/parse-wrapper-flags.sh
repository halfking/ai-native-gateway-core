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
# variable. We avoid `local -n` (bash 4.3+) so the module keeps working on
# macOS's bundled bash 3.2.57; instead we shell-quote the remaining tokens
# with `printf %q` (bash 3.0+) and let `eval` rebuild the caller's array.
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
  # Write back through the caller's variable names. `printf %q` is safe under
  # bash 3.0+ and yields a single shell token per array element, preserving
  # embedded spaces, single quotes, and `$` characters.
  printf -v "$_flag_var" '%s' "$_flag"
  local _quoted="" _q
  for _q in "${_rest[@]}"; do
    printf -v _quoted '%s %q' "$_quoted" "$_q"
  done
  # shellcheck disable=SC2154
  eval "$_args_var=( $_quoted )"
  return 0
}

# Convenience: parse common wrapper flags in one call and echo a
# machine-readable summary. Kept simple — wrappers usually just need
# extract_force_unlock, but this is here if a third flag appears.
parse_wrapper_flags() {
  local _flag_var=${1:-FORCE_UNLOCK} _args_var=${2:-ARGS}; shift 2
  extract_force_unlock "$_flag_var" "$_args_var" "$@"
}
