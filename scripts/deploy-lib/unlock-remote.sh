#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/unlock-remote.sh — operator-driven remote lock removal
#
# scripts/deploy-lib/lock.sh deliberately fails fast when the remote
# deploy lock is held on the target host. That is the right default —
# it stops two concurrent deploys from racing on the same release
# bundle and corrupting the atomic symlink switch.
#
# But operators occasionally need an escape hatch:
#
#   - the holding process died and the SSH connection drop killed the
#     deploy-seamless.sh EXIT trap before it could `rm -rf` the lock
#   - the holding host rebooted mid-deploy
#   - the lock is stale from a manually-killed deploy attempt and the
#     next deploy cannot acquire it
#
# This helper covers exactly that. It never auto-cleans: in default mode
# it only reports; removal requires --force.
#
# Differences from unlock-local.sh:
#
#   - lock.sh records the source/deployer PID, not a PID on the remote host.
#     This helper never kills that PID remotely. The operator must verify the
#     source process has ended before using --force; otherwise a new deploy
#     could overlap the still-running source process.
#
#   - Metadata target validation is strict. Missing or malformed metadata is
#     never silently treated as a different target.
#
# Usage:
#   bash scripts/deploy-lib/unlock-remote.sh <target>            # report only
#   bash scripts/deploy-lib/unlock-remote.sh <target> --force    # remove
#   bash scripts/deploy-lib/unlock-remote.sh 154 --ssh-key ~/.ssh/154 --direct
#
# Env overrides:
#   LOCK_REMOTE_PATH     — override the lock path (default: /var/lib/llm-gateway-go/deploy.lock)
#   LOCK_REMOTE_SSH_CMD  — function name to invoke instead of plain ssh
#                          (deploy-seamless.sh exports this so its retry
#                          and 252-hop logic is reused transparently)
#
# Exit codes:
#   0  — removed (or nothing to remove)
#   1  — refused (live holder, cross-target metadata, or no --force)
#   2  — usage error
#   3  — ssh / remote read failure
# =====================================================================
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: unlock-remote.sh <target> [--force] [--ssh-key PATH] [--direct] [--path PATH]" >&2
  exit 2
fi

TARGET=$1; shift
FORCE=0
SSH_KEY_OPT=""
DIRECT=0
SSH_KEY_OVERRIDE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --force|-f)        FORCE=1; shift ;;
    --ssh-key)
      [[ $# -ge 2 ]] || { echo "missing value for --ssh-key" >&2; exit 2; }
      SSH_KEY_OVERRIDE=$2; shift 2 ;;
    --direct)          DIRECT=1; shift ;;
    --path)            LOCK_REMOTE_PATH=$2; shift 2 ;;
    -h|--help)
      sed -n '2,46p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# Resolve target → host:port. Mirrors scripts/deploy-seamless.sh so the
# helper can stand alone for on-call use without sourcing the wrapper.
case "$TARGET" in
  154)
    REMOTE_HOST="${HOST_154:-${LOCK_REMOTE_HOST_154:-47.97.111.154}}"
    SSH_PORT="${SSH_PORT_154:-${LOCK_REMOTE_PORT_154:-25022}}"
    SSH_KEY_FILE="${SSH_KEY_OVERRIDE:-${SSH_KEY_154:-${SSH_KEY_FILE:-}}}"
    REMOTE_USER="${REMOTE_USER:-root}"
    ;;
  245)
    REMOTE_HOST="${HOST_245:-${LOCK_REMOTE_HOST_245:-8.136.114.245}}"
    SSH_PORT="${SSH_PORT_245:-${LOCK_REMOTE_PORT_245:-25022}}"
    SSH_KEY_FILE="${SSH_KEY_OVERRIDE:-${SSH_KEY_245:-${SSH_KEY_FILE:-}}}"
    REMOTE_USER="${REMOTE_USER:-root}"
    ;;
  *) echo "unknown target: $TARGET (expected 154 or 245)" >&2; exit 2 ;;
esac

: "${LOCK_REMOTE_PATH:=/var/lib/llm-gateway-go/deploy.lock}"

RED=$'\033[0;31m'; YELLOW=$'\033[1;33m'; GREEN=$'\033[0;32m'; NC=$'\033[0m'
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }

# Build the remote shell invocation. Resolution order:
#
#   1. LOCK_REMOTE_SSH_CMD is a defined function in this shell
#      (deploy-seamless.sh exports remote_ssh this way) — call it
#      verbatim. That gets us 252-hop retry + ControlMaster for free.
#
#   2. LOCK_REMOTE_SSH_CMD is a non-empty string but not a function —
#      treat it as a command prefix to prepend to the remote command.
#      This is the supported harness for tests / CI:
#        LOCK_REMOTE_SSH_CMD='bash /path/stub-ssh.sh' \
#          bash unlock-remote.sh 154 --force
#
#   3. Neither — fall back to a plain ssh built from the resolved
#      host / key / port. ConnectTimeout keeps a dead target from
#      hanging the operator's terminal.
ssh_invoke() {
  if [[ -n "${LOCK_REMOTE_SSH_CMD:-}" ]] && declare -F "$LOCK_REMOTE_SSH_CMD" >/dev/null 2>&1; then
    "$LOCK_REMOTE_SSH_CMD" "$1"
  elif [[ -n "${LOCK_REMOTE_SSH_CMD:-}" ]]; then
    eval "$LOCK_REMOTE_SSH_CMD" "'$1'"
  else
    local key_opt=()
    local host_opt="${REMOTE_USER}@${REMOTE_HOST}"
    [[ -n "$SSH_KEY_FILE" && -f "$SSH_KEY_FILE" ]] && key_opt=(-i "$SSH_KEY_FILE")
    # --direct is meaningful for 154: bypass the 252 hop explicitly.
    # Without --direct, this standalone helper uses the target host directly;
    # callers needing deploy-seamless retry/proxy behavior use LOCK_REMOTE_SSH_CMD.
    if [[ "$DIRECT" == 1 ]]; then
      host_opt="${REMOTE_USER}@${REMOTE_HOST}"
    fi
    ssh "${key_opt[@]}" -p "$SSH_PORT" \
      -o BatchMode=yes -o ConnectTimeout=10 \
      -o StrictHostKeyChecking=accept-new \
      "$host_opt" "$1"
  fi
}

# Read whether the remote lock directory exists. We use a unique
# sentinel so we can tell "does not exist" from "exists but cat failed".
remote_lock_exists() {
  local result rc
  result=$(ssh_invoke "if [ -e '$LOCK_REMOTE_PATH' ]; then printf EXISTS; else printf MISSING; fi" 2>/dev/null) || {
    err "cannot query remote lock state (SSH/read failure)"
    return 2
  }
  case "$result" in
    EXISTS) return 0 ;;
    MISSING) return 1 ;;
    *) err "invalid remote lock state: $result"; return 2 ;;
  esac
}

# Pull metadata as a single base64 blob so newlines in field values
# cannot poison our parser. base64 is universally available on the
# supported distros (Debian / RHEL / Alpine) and on macOS.
remote_read_metadata() {
  ssh_invoke "if [ -f '$LOCK_REMOTE_PATH/metadata' ]; then cat '$LOCK_REMOTE_PATH/metadata' | base64 | tr -d '\\n'; else echo MISSING; fi" \
    2>/dev/null
}

echo "Target: $TARGET  host=$REMOTE_USER@$REMOTE_HOST:$SSH_PORT  path=$LOCK_REMOTE_PATH"
echo ""

if remote_lock_exists; then
  :
else
  rc=$?
  if [[ $rc -eq 1 ]]; then
    ok "no remote lock at $LOCK_REMOTE_PATH — nothing to do"
    exit 0
  fi
  exit 3
fi

META_B64=$(remote_read_metadata || true)
if [[ -z "$META_B64" || "$META_B64" == "MISSING" ]]; then
  warn "lock dir exists but metadata is missing or unreadable"
  if [[ $FORCE -eq 0 ]]; then
    err "refusing to remove without --force"
    exit 1
  fi
  ssh_invoke "rm -rf '$LOCK_REMOTE_PATH'" || { err "remote rm failed"; exit 3; }
  ok "removed malformed lock at $LOCK_REMOTE_PATH"
  exit 0
fi

META_TEXT=$(printf '%s' "$META_B64" | base64 -d 2>/dev/null || true)

echo "Remote lock holder:"
printf '%s\n' "$META_TEXT" | sed 's/^/    /'
echo ""

get_meta() {
  local key=$1
  printf '%s\n' "$META_TEXT" \
    | awk -F= -v k="$key" '$1==k {sub($1"=",""); print; exit}'
}
HOLDER_PID=$(get_meta pid)
HOLDER_USER=$(get_meta source_user)
HOLDER_HOST=$(get_meta source_host)
HOLDER_STARTED=$(get_meta started_at)
HOLDER_TARGET=$(get_meta target)
HOLDER_COMMIT=$(get_meta commit)
HOLDER_VERSION=$(get_meta version)

# We cannot tell from this machine whether the remote PID is still
# alive (the remote kernel has its own process table). The strongest
# signal we have is "the metadata says pid=N; if N is plausible, treat
# it as live". The operator must SSH in and `ps -p N` to verify.
AGE_HUMAN="unknown"
if [[ "$HOLDER_STARTED" =~ ^[0-9TZ:-]+$ ]]; then
  START_EPOCH=$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$HOLDER_STARTED" +%s 2>/dev/null \
                || date -u -d "$HOLDER_STARTED" +%s 2>/dev/null \
                || echo 0)
  if [[ "$START_EPOCH" =~ ^[0-9]+$ ]] && [[ "$START_EPOCH" -gt 0 ]]; then
    NOW_EPOCH=$(date -u +%s)
    AGE_S=$(( NOW_EPOCH - START_EPOCH ))
    ABS=${AGE_S#-}
    if [[ $AGE_S -lt 0 ]]; then
      if [[ $ABS -lt 60 ]]; then
        AGE_HUMAN="future ~${ABS}s (clock skew?)"
      elif [[ $ABS -lt 3600 ]]; then
        AGE_HUMAN="future ~$(( ABS / 60 ))m$(( ABS % 60 ))s (clock skew?)"
      else
        AGE_HUMAN="future ~$(( ABS / 3600 ))h$(( (ABS % 3600) / 60 ))m (clock skew?)"
      fi
    elif [[ $AGE_S -lt 60 ]]; then
      AGE_HUMAN="${AGE_S}s"
    elif [[ $AGE_S -lt 3600 ]]; then
      AGE_HUMAN="$(( AGE_S / 60 ))m$(( AGE_S % 60 ))s"
    else
      AGE_HUMAN="$(( AGE_S / 3600 ))h$(( (AGE_S % 3600) / 60 ))m"
    fi
  fi
fi

echo "Sanity:"
echo "    recorded pid : $HOLDER_PID (verify with: ssh ... 'ps -p $HOLDER_PID')"
echo "    source user  : $HOLDER_USER"
echo "    source host  : $HOLDER_HOST"
echo "    lock age     : $AGE_HUMAN"
echo "    target       : $HOLDER_TARGET"
echo "    commit       : $HOLDER_COMMIT"
echo "    version      : $HOLDER_VERSION"
echo ""

# Cross-target check. The 154 and 245 deploys share version files /
# web/dist / releases on each host, but the lock directory is
# host-scoped. If the recorded target != the one we are unlocking,
# something is very wrong (probably a wrong SSH target).
if [[ -n "$HOLDER_TARGET" && "$HOLDER_TARGET" != "$TARGET" ]]; then
  err "metadata target=$HOLDER_TARGET does not match requested target=$TARGET"
  err "refusing to remove — verify which host you actually SSH'd into"
  exit 1
fi

if [[ $FORCE -eq 0 ]]; then
  REASON=""
  if [[ -n "$HOLDER_PID" && "$HOLDER_PID" =~ ^[0-9]+$ ]]; then
    REASON="recorded PID $HOLDER_PID is plausibly alive (verify with: ssh $REMOTE_USER@$REMOTE_HOST 'ps -p $HOLDER_PID')"
  fi
  if [[ -z "$REASON" ]]; then
    REASON="lock metadata looks stale, but refusing without --force"
  fi
  err "refusing to remove: $REASON"
  echo "    rerun with --force to remove anyway"
  exit 1
fi

# Force path. Note we DO NOT kill the remote PID — that is the
# operator's responsibility via a separate SSH session. We only clear
# the lock so the next deploy can acquire it.
warn "removing remote lock at $LOCK_REMOTE_PATH (operator request)"
if [[ -n "$HOLDER_PID" && "$HOLDER_PID" =~ ^[0-9]+$ ]]; then
  warn "recorded PID $HOLDER_PID belongs to the source deploy host; verify it is no longer running before force removal"
fi
if ! ssh_invoke "rm -rf '$LOCK_REMOTE_PATH'"; then
  err "remote rm failed"
  exit 3
fi
ok "removed remote lock at $LOCK_REMOTE_PATH"
exit 0
