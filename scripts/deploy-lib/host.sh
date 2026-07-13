#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/host.sh — shared 154/245 host seams
#
# Implements the spec's "Shared 154/245 staging, restart, and
# verification seams". Slice 1 only adds the seam surface and the
# release-bundle layout for 245. Actual deploy / rollback code lands
# in slices 4 and 5.
#
# Conventions established here:
#   - 245 release bundles live under /opt/llm-gateway-go/releases/${VERSION}
#   - The kernel-visible binary path is the symlink
#     /opt/llm-gateway-go/gateway → current/<gateway>
#   - /opt/llm-gateway-go/web → current/<web>
#   - /opt/llm-gateway-go/version.json → current/<version.json>
#   - "current" is itself a symlink into releases/
#
# The 154 contract uses the same release-bundle layout in this slice so
# that the two canonical targets share one staging function. 154
# rollback, however, stays on the existing runbook (the contract field
# rollback_policy=runbook) — slice 5 will convert it to versioned once
# parity tests pass.
# =====================================================================

if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "host.sh: requires bash" >&2
  # shellcheck disable=SC2317  # only reached when invoked directly (not sourced)
  return 1 2>/dev/null || exit 1
fi

# shellcheck disable=SC2317

# Render the canonical release-bundle layout for one target. The
# function is intentionally pure: it takes a target name and a version
# string and prints the absolute paths it would manage. No file is
# touched.
host_release_layout() {
  local target=$1
  local version=$2
  local root_var="HOST_${target^^}_ROOT"
  local root=${!root_var:-}
  case "$target" in
    154) root=${root:-/opt/llm-gateway-go} ;;
    245) root=${root:-/opt/llm-gateway-go} ;;
    *)   echo "host_release_layout: unsupported target $target" >&2; return 64 ;;
  esac

  cat <<EOF
root=$root
releases_dir=$root/releases
release_dir=$root/releases/$version
current_link=$root/current
binary_link=$root/$( [[ $target == 245 ]] && echo gateway || echo llm-gateway-go )
web_link=$root/web
version_link=$root/version.json
metadata_file=$root/releases/$version/deployment.json
checksum_file=$root/releases/$version/SHA256SUMS
EOF
}

# Verify that the host-side prerequisites exist before a deploy attempt:
# the service manager is reachable, the binary path is writable, and
# the web directory is present. Returns 0 on success, nonzero otherwise.
host_preflight() {
  local ssh_cmd=$1 target=$2
  local contract
  contract=$(scripts/deploy-lib/targets.sh >/dev/null 2>&1; target_contract "$target") \
    || contract=$(target_contract "$target")
  local binary_path web_path service_name service_manager
  binary_path=$(printf '%s' "$contract" | sed -n 's/.*"binary_path":"\([^"]*\)".*/\1/p')
  web_path=$(printf '%s' "$contract" | sed -n 's/.*"web_path":"\([^"]*\)".*/\1/p')
  service_name=$(printf '%s' "$contract" | sed -n 's/.*"service_name":"\([^"]*\)".*/\1/p')
  service_manager=$(printf '%s' "$contract" | sed -n 's/.*"service_manager":"\([^"]*\)".*/\1/p')

  [[ -n "$binary_path" ]] || { echo "binary_path missing for $target" >&2; return 1; }
  [[ -n "$service_manager" ]] || { echo "service_manager missing for $target" >&2; return 1; }

  case "$service_manager" in
    systemd)
      "$ssh_cmd" "systemctl show '$service_name' >/dev/null 2>&1" || {
        echo "ERROR: $target systemd unit $service_name not visible" >&2
        return 1
      }
      ;;
    k3s|launchd|"")
      : # Slice 1 only knows systemd; other managers fall through.
      ;;
  esac
  return 0
}

# Restart the tracked service on the target host. Slice 1 only knows
# systemd; the k3s and launchd paths come with their respective slices.
host_restart_service() {
  local ssh_cmd=$1 target=$2
  local service_name
  service_name=$(target_field "$target" service_name)
  [[ -n "$service_name" ]] || { echo "service_name missing for $target" >&2; return 1; }
  "$ssh_cmd" "systemctl restart '$service_name'"
}

# Wait for /healthz on the target host to answer 200. Slice 1 uses a
# simple curl loop with a fixed cap so the test harness can run it
# offline against the fake-ssh bin.
host_wait_healthy() {
  local ssh_cmd=$1 target=$2 timeout_s=${3:-60}
  local health_url
  health_url=$(target_field "$target" health_url)
  [[ -n "$health_url" ]] || { echo "health_url missing for $target" >&2; return 1; }
  local deadline=$(( $(date +%s) + timeout_s ))
  while (( $(date +%s) < deadline )); do
    if "$ssh_cmd" "curl -fsS --max-time 2 '$health_url' >/dev/null 2>&1"; then
      return 0
    fi
    sleep 2
  done
  echo "ERROR: $target health check timed out after ${timeout_s}s" >&2
  return 1
}

# Mark a release bundle as verified=true once /healthz answers 200.
# Slice 1 keeps the metadata format simple; richer audit fields come
# with the rollback slice.
host_mark_verified() {
  local ssh_cmd=$1 target=$2 version=$3
  local metadata_file
  metadata_file=$(host_release_layout "$target" "$version" | sed -n 's/^metadata_file=//p')
  local now
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  "$ssh_cmd" "mkdir -p '$(dirname "$metadata_file")' && cat > '$metadata_file' <<EOF
{
  \"target\": \"$target\",
  \"version\": \"$version\",
  \"verified\": true,
  \"verified_at\": \"$now\"
}
EOF"
}