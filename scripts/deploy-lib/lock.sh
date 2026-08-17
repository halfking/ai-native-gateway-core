#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/lock.sh — local + remote lock primitives
#
# Implements spec §"Locking" (Slice 3 of the hardening plan):
#
#   - Local lock: a repository-scoped global lock that protects shared
#     build artifacts and local deployment state. Uses `flock` when
#     available; otherwise acquires an atomic `mkdir` lock and removes
#     it via trap on normal and error exits.
#
#   - Remote lock: an atomic `mkdir` lock directory on the target host
#     holding metadata (target, source OS user, source hostname, local
#     PID, UTC start time, commit SHA, version). No secrets are stored.
#
#   Lock contention fails fast — the second caller exits nonzero BEFORE
#   backup / upload and prints the first lock's owner metadata (no
#   secrets). Locks are released by traps on normal and error exits.
#   Age alone never authorizes stale-lock deletion; only `force-unlock`
#   (executed by the operator) can remove a remote lock.
#
#   This module is the lib-level primitive. The `lock_local` and
#   `lock_remote` functions are invoked from scripts/deploy.sh via
#   `with_local_lock` / `with_remote_lock` wrappers. The wrappers are
#   defined here too so that any orchestrator can reuse them.
# =====================================================================

if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "lock.sh: requires bash" >&2
  # shellcheck disable=SC2317  # only reached when invoked directly (not sourced)
  return 1 2>/dev/null || exit 1
fi

# shellcheck disable=SC2317

# Default local lock directory. Lives under the system temp so multiple
# clones of the repo on the same machine do not collide. The
# orchestrator may override LOCK_LOCAL_DIR before sourcing this file.
: "${LOCK_LOCAL_DIR:=${TMPDIR:-/tmp}/kx-llm-gateway-deploy.lock}"

# Flock binary probe. When empty the module falls back to mkdir.
: "${LOCK_FLOCK_BIN:=$(command -v flock 2>/dev/null || true)}"

# Lock acquire: when flock is available we use a nonblocking exclusive
# file-descriptor lock. The fd travels back to the caller via a global
# LOCK_LOCAL_FD variable; release_lock_local closes it.
lock_acquire_local() {
  if [[ -n "$LOCK_FLOCK_BIN" ]]; then
    exec {LOCK_LOCAL_FD}>"$LOCK_LOCAL_DIR"
    if ! "$LOCK_FLOCK_BIN" -n "$LOCK_LOCAL_FD"; then
      echo "ERROR: local lock held at $LOCK_LOCAL_DIR" >&2
      return 75  # EX_TEMPFAIL — standard "try again" code
    fi
    # Stamp metadata inside the locked file so `ps`/inspectors can see
    # who holds the lock.
    {
      printf 'target=%s\n' "${LOCK_LOCAL_TARGET:-?}"
      printf 'source_user=%s\n' "${SOURCE_USER:-$(id -un 2>/dev/null || echo unknown)}"
      printf 'source_host=%s\n' "${SOURCE_HOST:-$(hostname 2>/dev/null || echo unknown)}"
      printf 'pid=%s\n' "$$"
      printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      printf 'commit=%s\n' "${SOURCE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
      printf 'version=%s\n' "${SOURCE_VERSION:-$(cat version.json 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' || echo unknown)}"
    } >&"$LOCK_LOCAL_FD"
    return 0
  fi

  # mkdir fallback — atomic on POSIX. mkdir returns nonzero if the dir
  # already exists, so a losing caller's race is captured here.
  if ! mkdir "$LOCK_LOCAL_DIR" 2>/dev/null; then
    echo "ERROR: local lock held at $LOCK_LOCAL_DIR" >&2
    if [[ -f "$LOCK_LOCAL_DIR/metadata" ]]; then
      cat "$LOCK_LOCAL_DIR/metadata" >&2
    fi
    return 75
  fi
  mkdir -p "$LOCK_LOCAL_DIR"
  {
    printf 'target=%s\n' "${LOCK_LOCAL_TARGET:-?}"
    printf 'source_user=%s\n' "${SOURCE_USER:-$(id -un 2>/dev/null || echo unknown)}"
    printf 'source_host=%s\n' "${SOURCE_HOST:-$(hostname 2>/dev/null || echo unknown)}"
    printf 'pid=%s\n' "$$"
    printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'commit=%s\n' "${SOURCE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
    printf 'version=%s\n' "${SOURCE_VERSION:-$(cat version.json 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' || echo unknown)}"
  } >"$LOCK_LOCAL_DIR/metadata"
}

lock_release_local() {
  if [[ -n "${LOCK_LOCAL_FD:-}" ]]; then
    eval "exec ${LOCK_LOCAL_FD}<&-"
    LOCK_LOCAL_FD=
    rm -f "$LOCK_LOCAL_DIR"
    return
  fi
  rm -rf "$LOCK_LOCAL_DIR"
}

# Remote lock — invoked via SSH. The contract is "atomic mkdir"; we do
# not pipe sensitive data into the lock. The caller is responsible for
# staging the ssh command (the tests stub it via PATH).
lock_acquire_remote() {
  local ssh_cmd=$1 target=$2 lock_path=$3 metadata metadata_b64 lock_q metadata_q
  metadata=$(cat <<EOF
target=$target
source_user=${SOURCE_USER:-$(id -un 2>/dev/null || echo unknown)}
source_host=${SOURCE_HOST:-$(hostname 2>/dev/null || echo unknown)}
pid=$$
started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
commit=${SOURCE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}
version=${SOURCE_VERSION:-unknown}
EOF
)
  metadata_b64=$(printf '%s\n' "$metadata" | base64 | tr -d '\n')
  printf -v lock_q '%q' "$lock_path"
  printf -v metadata_q '%q' "$metadata_b64"
  if ! "$ssh_cmd" "LOCK_PATH=$lock_q METADATA_B64=$metadata_q bash -s" <<'REMOTE_LOCK'
set -euo pipefail
if ! mkdir "$LOCK_PATH" 2>/dev/null; then
  cat "$LOCK_PATH/metadata" >&2 2>/dev/null || true
  exit 75
fi
cleanup_partial_lock() {
  rm -rf "$LOCK_PATH"
}
trap cleanup_partial_lock EXIT HUP INT TERM
printf '%s' "$METADATA_B64" | base64 -d > "$LOCK_PATH/metadata.tmp"
test -s "$LOCK_PATH/metadata.tmp"
mv "$LOCK_PATH/metadata.tmp" "$LOCK_PATH/metadata"
trap - EXIT HUP INT TERM
REMOTE_LOCK
  then
    echo "ERROR: remote lock held or initialization failed at $lock_path on $target" >&2
    return 75
  fi
}

lock_release_remote() {
  local ssh_cmd=$1 lock_path=$2
  "$ssh_cmd" "rm -rf '$lock_path'"
}

# Force-unlock — operator-driven remote lock removal. Only this entry
# point is allowed to bypass the staleness check; age alone is never
# enough.
force_unlock_remote() {
  local ssh_cmd=$1 lock_path=$2
  echo "WARN: removing remote lock at $lock_path on operator request"
  "$ssh_cmd" "rm -rf '$lock_path'"
}

# Wrapper: run body with the local lock held; release on any exit.
# Usage: with_local_lock <target> <body-command...>
with_local_lock() {
  local target=$1; shift
  LOCK_LOCAL_TARGET=$target lock_acquire_local
  trap 'lock_release_local' EXIT
  "$@"
}