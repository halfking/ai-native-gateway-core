#!/usr/bin/env bash
# ============================================================================
# scripts/local-host-layout-helper.sh
#
# 245-style layout helpers for local-host deployments. Mirrors the contract of
# scripts/deploy-lib/host.sh (used by 245 / 154) but operates purely locally —
# no ssh, no systemd. The atomic-swap boundary is a `current` symlink inside
# $LLM_GATEWAY_FILES_ROOT/bin/.
#
# Source this file from other scripts:
#
#   source scripts/local-host-layout-helper.sh
#
# Public functions:
#   lh_root                          — print LLM_GATEWAY_FILES_ROOT (default: ~/Downloads/llm-gateway-files)
#   lh_layout_vars                   — print all canonical paths (root, bin, bundle, etc.)
#   lh_require_root                  — ensure all sub-dirs exist; emit fatal if PG not up
#   lh_stage_release <bundle_dir>    — write SHA256SUMS + deployment.json (verified=false)
#   lh_verify_bundle <bundle_dir>    — sha256sum -c SHA256SUMS --strict
#   lh_atomic_switch <version>       — update `current` symlink + rewrite top-level shortcuts
#   lh_list_verified_releases        — newest-first verified bundle dirs (skipping active)
#   lh_prune_releases <keep>         — keep N newest verified + active, delete the rest
#   lh_rollback_to <version>         — refuse if missing/unverified/active; then atomic switch
#   lh_mark_verified <version>       — flip deployment.json verified=true + verified_at
# ============================================================================
set -euo pipefail

# ── Canonical paths ───────────────────────────────────────────────────────
lh_root() {
  printf '%s\n' "${LLM_GATEWAY_FILES_ROOT:-$HOME/Downloads/llm-gateway-files}"
}

lh_layout_vars() {
  local root
  root=$(lh_root)
  cat <<EOF
root=$root
bin_dir=$root/bin
releases_dir=$root/bin/releases
postgres_data_dir=$root/postgres/data
redis_data_dir=$root/redis/data
logs_dir=$root/logs
attachments_dir=$root/attachments
raw_logs_dir=$root/raw-logs
backups_dir=$root/backups
run_dir=$root/run
pid_file=$root/run/gateway.pid
port_file=$root/run/gateway.port
EOF
}

# Top-level shortcut symlinks (each points into the active bundle). Mirrors
# 245's `gateway`, `web`, `version.json`, `configs` symlinks.
lh_top_shortcuts() {
  printf '%s\n' start.sh stop.sh status.sh env.sh
}

# ── Directory bootstrap ────────────────────────────────────────────────────
lh_require_root() {
  local vars
  vars=$(lh_layout_vars)
  local bin_dir postgres_data_dir logs_dir attachments_dir raw_logs_dir backups_dir run_dir redis_data_dir
  bin_dir=$(echo "$vars" | sed -n 's/^bin_dir=//p')
  postgres_data_dir=$(echo "$vars" | sed -n 's/^postgres_data_dir=//p')
  logs_dir=$(echo "$vars" | sed -n 's/^logs_dir=//p')
  attachments_dir=$(echo "$vars" | sed -n 's/^attachments_dir=//p')
  raw_logs_dir=$(echo "$vars" | sed -n 's/^raw_logs_dir=//p')
  backups_dir=$(echo "$vars" | sed -n 's/^backups_dir=//p')
  run_dir=$(echo "$vars" | sed -n 's/^run_dir=//p')
  redis_data_dir=$(echo "$vars" | sed -n 's/^redis_data_dir=//p')

  mkdir -p \
    "$bin_dir" \
    "$postgres_data_dir" \
    "$logs_dir" \
    "$attachments_dir" \
    "$raw_logs_dir" \
    "$backups_dir" \
    "$run_dir" \
    "$redis_data_dir"
}

# Resolve the active version (whatever `current` symlink targets, or empty).
lh_active_version() {
  local bin_dir
  bin_dir=$(lh_layout_vars | sed -n 's/^bin_dir=//p')
  local link="$bin_dir/current"
  if [[ -L "$link" ]]; then
    basename "$(readlink "$link")"
  else
    printf '\n'
  fi
}

# Resolve the bundle directory for a given version string.
lh_bundle_dir() {
  local version=$1
  local bin_dir
  bin_dir=$(lh_layout_vars | sed -n 's/^bin_dir=//p')
  printf '%s\n' "$bin_dir/$version"
}

# ── Stage a release bundle ────────────────────────────────────────────────
# After staging, the bundle contains:
#   gateway (executable), web/, version.json, VERSION, configs/ (if present),
#   SHA256SUMS, deployment.json (verified=false).
#
# Caller is responsible for placing gateway + web/ + version.json + VERSION +
# configs/ before invoking this; we only write the manifest + metadata.
lh_stage_release() {
  local bundle_dir=$1
  local version=${2:-dev}
  local target=${LH_STAGE_TARGET:-local-host}

  [[ -d "$bundle_dir" ]] || { echo "lh_stage_release: missing bundle_dir $bundle_dir" >&2; return 1; }
  [[ -f "$bundle_dir/gateway" ]] || { echo "lh_stage_release: missing gateway in $bundle_dir" >&2; return 1; }
  [[ -d "$bundle_dir/web" ]] || { echo "lh_stage_release: missing web/ in $bundle_dir" >&2; return 1; }

  (
    cd "$bundle_dir"
    {
      sha256sum gateway
      [[ -f version.json ]] && sha256sum version.json
      [[ -f VERSION      ]] && sha256sum VERSION
      if [[ -d configs ]]; then
        find configs -type f -print | sort | while IFS= read -r f; do
          sha256sum "$f"
        done
      fi
    } > SHA256SUMS
  )

  cat >"$bundle_dir/deployment.json" <<EOF
{"target":"$target","version":"$version","verified":false,"created_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
EOF
}

lh_verify_bundle() {
  local bundle_dir=$1
  [[ -f "$bundle_dir/SHA256SUMS" ]] || { echo "lh_verify_bundle: SHA256SUMS missing" >&2; return 1; }
  (cd "$bundle_dir" && sha256sum -c SHA256SUMS --strict >/dev/null 2>&1) || {
    echo "lh_verify_bundle: SHA256SUMS verification failed for $bundle_dir" >&2
    return 1
  }
}

# ── Atomic switch ─────────────────────────────────────────────────────────
# Rewrite `current` symlink to the named bundle and refresh top-level shortcut
# symlinks (start.sh / stop.sh / status.sh / env.sh). Mirrors
# host_atomic_switch from deploy-lib/host.sh.
lh_atomic_switch() {
  local version=$1
  local vars bin_dir bundle_dir
  vars=$(lh_layout_vars)
  bin_dir=$(echo "$vars" | sed -n 's/^bin_dir=//p')
  bundle_dir=$(lh_bundle_dir "$version")

  [[ -d "$bundle_dir" ]] || { echo "lh_atomic_switch: bundle not found: $bundle_dir" >&2; return 1; }

  local current_link="$bin_dir/current"
  # atomic ln -sfn to the new version
  ln -sfn "$version" "$current_link"

  # Refresh top-level shortcuts: each points into current/<script>
  for script in start.sh stop.sh status.sh env.sh logs.sh; do
    if [[ -f "$bundle_dir/$script" ]]; then
      ln -sfn "current/$script" "$bin_dir/$script"
    fi
  done
}

# ── Verified release listing ──────────────────────────────────────────────
# Newest-first; skip the active version.
lh_list_verified_releases() {
  local bin_dir
  bin_dir=$(lh_layout_vars | sed -n 's/^bin_dir=//p')
  local active
  active=$(lh_active_version)
  for d in "$bin_dir"/*/; do
    [[ -d "$d" ]] || continue
    local meta="$d/deployment.json"
    [[ -f "$meta" ]] || continue
    grep -q '"verified":true' "$meta" || continue
    local v
    v=$(basename "$d")
    [[ "$v" == "$active" ]] && continue
    local ts
    ts=$(grep -o '"verified_at":"[^"]*"' "$meta" | head -1)
    ts=${ts#'"verified_at":"'}
    ts=${ts%'"'}
    printf '%s %s\n' "$ts" "$v"
  done | sort -r | awk '{print $2}'
}

# ── Mark a bundle verified ────────────────────────────────────────────────
lh_mark_verified() {
  local version=$1
  local bundle_dir
  bundle_dir=$(lh_bundle_dir "$version")
  local meta="$bundle_dir/deployment.json"
  [[ -f "$meta" ]] || { echo "lh_mark_verified: $meta not found" >&2; return 1; }

  local now
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  # Single python3 pass — avoid sed -E with multi-line JSON edits.
  python3 - "$meta" "$version" "$now" <<'PY'
import json
import sys
path, version, now = sys.argv[1:4]
with open(path, 'r') as f:
    data = json.load(f)
data['version'] = version
data['verified'] = True
data['verified_at'] = now
with open(path, 'w') as f:
    json.dump(data, f, indent=2)
    f.write('\n')
PY
}

# ── Prune ─────────────────────────────────────────────────────────────────
# Keep N newest verified bundles + the active one; delete the rest.
# Unverified bundles are kept (for post-mortem) until verified.
lh_prune_releases() {
  local keep=${1:-3}
  local bin_dir
  bin_dir=$(lh_layout_vars | sed -n 's/^bin_dir=//p')
  local active
  active=$(lh_active_version)

  if [[ ! -d "$bin_dir" ]]; then return 0; fi

  # Compute keep-set: newest $keep verified (sorted by verified_at desc) + active
  local keep_set=""
  keep_set=$(lh_list_verified_releases | head -n "$keep" | tr '\n' ' ')
  if [[ -n "$active" ]]; then
    case " $keep_set " in *" $active "*) ;; *) keep_set="$keep_set $active" ;; esac
  fi

  for d in "$bin_dir"/*/; do
    [[ -d "$d" ]] || continue
    # Skip the `current` symlink and any other non-bundle entries. A symlink
    # `current` resolves to a bundle dir, but it is NOT itself a bundle and
    # must never be deleted.
    [[ -L "$d" ]] && continue
    local v
    v=$(basename "$d")
    case " $keep_set " in
      *" $v "*) ;;
      *)
        # Safety: never delete the currently active bundle, even if its keep_set
        # entry was lost during a race (e.g. user pruned right after atomic switch
        # but before reading the active symlink). This is the "do not lose my
        # current" guard.
        if [[ "$v" == "$active" && -n "$active" ]]; then
          continue
        fi
        rm -rf "$d"
        ;;
    esac
  done
  printf '%s\n' "$keep_set"
}

# ── Rollback to a specific verified bundle ─────────────────────────────────
lh_rollback_to() {
  local version=$1
  local bundle_dir
  bundle_dir=$(lh_bundle_dir "$version")
  local meta="$bundle_dir/deployment.json"
  [[ -f "$meta" ]] || { echo "lh_rollback_to: no bundle at $version" >&2; return 1; }
  local active
  active=$(lh_active_version)
  [[ "$version" == "$active" ]] && { echo "lh_rollback_to: $version is already active" >&2; return 1; }
  grep -q '"verified":true' "$meta" || { echo "lh_rollback_to: $version is not verified" >&2; return 1; }
  lh_atomic_switch "$version"
}

# Sanity: emit the script's own version for diagnostics.
lh_helper_version() { printf 'local-host-layout-helper v1.0.0\n'; }
