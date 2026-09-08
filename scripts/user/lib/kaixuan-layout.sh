#!/usr/bin/env bash
# kaixuan-layout.sh — customer install root + blue-green bin helpers.
# SOURCE_OF_TRUTH for new-install defaults: llm-gateway-go scripts/deploy-local-lib.sh
# Safe to source; no set -e so callers keep their own errexit.
#
# New install: macOS ~/kaixuan/llm-gateway-go, Linux /opt/kaixuan/llm-gateway-go,
# Windows D:/kaixuan/llm-gateway-go (else C:/).
# Upgrade: never auto-migrate /opt/llm-gateway or ~/Downloads/llm-gateway-files.

kx_uname() { uname -s 2>/dev/null || printf unknown; }

kx_default_shared_root() {
  local os
  os=$(kx_uname)
  case "$os" in
    Darwin) printf '%s\n' "${HOME}/kaixuan" ;;
    Linux) printf '%s\n' "/opt/kaixuan" ;;
    MINGW*|MSYS*|CYGWIN*)
      if [[ -d /d && -w /d ]]; then printf '%s\n' '/d/kaixuan'
      elif [[ -d /c && -w /c ]]; then printf '%s\n' '/c/kaixuan'
      else printf '%s\n' "${HOME}/kaixuan"; fi
      ;;
    *) printf '%s\n' "${HOME}/kaixuan" ;;
  esac
}

kx_default_project_root() {
  printf '%s/llm-gateway-go\n' "$(kx_default_shared_root)"
}

kx_legacy_candidates() {
  printf '%s\n' \
    "/opt/llm-gateway" \
    "${HOME}/Downloads/llm-gateway-files"
}

# Prints first existing legacy tree, or empty.
kx_detect_legacy_root() {
  local p
  for p in $(kx_legacy_candidates); do
    if [[ -d "$p" ]]; then
      printf '%s\n' "$p"
      return 0
    fi
  done
  return 0
}

# Resolve install root. INSTALL_ROOT / LLM_GATEWAY_ROOT win; else legacy; else default.
kx_resolve_install_root() {
  if [[ -n "${INSTALL_ROOT:-}" ]]; then
    printf '%s\n' "$INSTALL_ROOT"
    return 0
  fi
  if [[ -n "${LLM_GATEWAY_ROOT:-}" ]]; then
    printf '%s\n' "$LLM_GATEWAY_ROOT"
    return 0
  fi
  local legacy
  legacy="$(kx_detect_legacy_root)"
  if [[ -n "$legacy" ]]; then
    printf '%s\n' "$legacy"
    return 0
  fi
  kx_default_project_root
}

kx_bin_slot() {
  local version="${1:-}" build="${2:-}"
  version="${version#v}"
  if [[ -n "$build" ]]; then
    printf '%s.%s\n' "$version" "$build"
  else
    printf '%s\n' "$version"
  fi
}

kx_bin_version_dir() {
  local root="$1" version="$2" build="${3:-}"
  printf '%s/bin/%s\n' "$root" "$(kx_bin_slot "$version" "$build")"
}

kx_prepare_layout() {
  local root="$1" need_pg="${2:-0}" need_redis="${3:-0}"
  mkdir -p "$root/attachments" "$root/bin" "$root/backups" "$root/logs" "$root/raw-logs" "$root/run"
  if [[ "$need_pg" == "1" || "$need_pg" == "true" ]]; then
    mkdir -p "$(kx_default_shared_root)/postgres/logs" \
      "$(kx_default_shared_root)/postgres/backups" \
      "$(kx_default_shared_root)/postgres/run"
  fi
  if [[ "$need_redis" == "1" || "$need_redis" == "true" ]]; then
    mkdir -p "$(kx_default_shared_root)/redis/logs" \
      "$(kx_default_shared_root)/redis/run"
  fi
}

# Atomic bin/current -> bin/{version}.{build}
kx_switch_current() {
  local root="$1" version="$2" build="${3:-}"
  local slot dest tmp
  slot="$(kx_bin_slot "$version" "$build")"
  dest="$root/bin/$slot"
  mkdir -p "$dest"
  tmp="$root/bin/.current.new.$$"
  ln -s "$slot" "$tmp"
  rm -f "$root/bin/current"
  mv "$tmp" "$root/bin/current"
}

kx_current_slot() {
  local p="$1/bin/current"
  [[ -L "$p" ]] && basename "$(readlink "$p")" || true
}
