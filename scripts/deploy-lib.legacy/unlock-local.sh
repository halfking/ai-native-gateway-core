#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/unlock-local.sh — operator-driven local lock removal
#
# scripts/deploy-lib/lock.sh deliberately fails fast when the local repo
# lock is held. That is the right default — it stops two concurrent
# deploys from racing on the same build artifacts and the same web/dist.
# But operators occasionally need an escape hatch:
#
#   - the holding process died and the EXIT trap never ran (kill -9,
#     SSH drop, host reboot under the deployer)
#   - the holding process is from a different repo checkout but you
#     know it is stale / wrongly scoped
#   - the holder is genuinely still running on the same target and you
#     want to abort it deliberately
#
# This helper covers exactly that. It never auto-cleans: in default mode
# it only reports; removal requires --force. Without --force it also
# refuses if the recorded PID is still alive.
#
# Usage:
#   bash scripts/deploy-lib/unlock-local.sh --target 245
#   bash scripts/deploy-lib/unlock-local.sh --target 245 --force
#   # Without --target, LOCK_LOCAL_DIR may be supplied explicitly for tooling.
#
# Exit codes:
#   0  — removed (or nothing to remove)
#   1  — lock held by a live PID, refused (without --force)
#   2  — usage error
# =====================================================================
set -euo pipefail

FORCE=0
TARGET=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --force|-f) FORCE=1; shift ;;
    --target)
      [[ $# -ge 2 ]] || { echo "missing value for --target" >&2; exit 2; }
      TARGET=$2; shift 2 ;;
    -h|--help)
      sed -n '2,28p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [[ -n "$TARGET" && "$TARGET" != 154 && "$TARGET" != 245 ]]; then
  echo "unsupported target: $TARGET (expected 154 or 245)" >&2
  exit 2
fi
# Match deploy-seamless.sh: each target has an independent local lock.
# LOCK_LOCAL_DIR remains an explicit override for tests and tooling.
if [[ -n "$TARGET" ]]; then
  # An explicit target must win over a stale inherited override; otherwise a
  # shell carrying the old global lock path could remove the wrong lock.
  LOCK_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-deploy-${TARGET}.lock"
elif [[ -n "${LOCK_LOCAL_DIR:-}" ]]; then
  LOCK_DIR=$LOCK_LOCAL_DIR
else
  echo "missing --target (154 or 245); set LOCK_LOCAL_DIR for an explicit path" >&2
  exit 2
fi

RED=$'\033[0;31m'; YELLOW=$'\033[1;33m'; GREEN=$'\033[0;32m'; NC=$'\033[0m'
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }

if [[ ! -e "$LOCK_DIR" ]]; then
  ok "no local lock at $LOCK_DIR — nothing to do"
  exit 0
fi

META="$LOCK_DIR/metadata"
if [[ ! -f "$META" ]]; then
  warn "lock dir exists but metadata is missing at $META"
  if [[ $FORCE -eq 0 ]]; then
    err "refusing to remove without --force"
    exit 1
  fi
  rm -rf "$LOCK_DIR"
  ok "removed malformed lock at $LOCK_DIR"
  exit 0
fi

echo "Local lock holder:"
sed 's/^/    /' "$META"
echo ""

# Parse the metadata. lock.sh writes key=value lines; fall back to "?"
# when a key is absent.
get_meta() {
  local key=$1
  awk -F= -v k="$key" '$1==k {sub($1"=",""); print; exit}' "$META"
}
HOLDER_PID=$(get_meta pid)
HOLDER_USER=$(get_meta source_user)
HOLDER_HOST=$(get_meta source_host)
HOLDER_STARTED=$(get_meta started_at)
HOLDER_TARGET=$(get_meta target)

pid_alive() {
  local pid=$1
  [[ -n "$pid" && "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null
}

ALIVE=0
if pid_alive "$HOLDER_PID"; then ALIVE=1; fi

CURRENT_USER=$(id -un 2>/dev/null || echo unknown)
SAME_USER=0
[[ "$HOLDER_USER" == "$CURRENT_USER" ]] && SAME_USER=1

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
      # Negative age means the host clock skews earlier than the recorded
      # started_at — treat as future rather than showing "-25367s".
      if [[ $ABS -lt 60 ]]; then
        AGE_HUMAN="future ~${ABS}s (host clock skew?)"
      elif [[ $ABS -lt 3600 ]]; then
        AGE_HUMAN="future ~$(( ABS / 60 ))m$(( ABS % 60 ))s (host clock skew?)"
      else
        AGE_HUMAN="future ~$(( ABS / 3600 ))h$(( (ABS % 3600) / 60 ))m (host clock skew?)"
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
echo "    pid alive : $([[ $ALIVE -eq 1 ]] && echo yes || echo no) (pid=$HOLDER_PID)"
echo "    same user : $([[ $SAME_USER -eq 1 ]] && echo yes || echo no) (you=$CURRENT_USER, holder=$HOLDER_USER)"
echo "    lock age  : $AGE_HUMAN"
echo "    target    : $HOLDER_TARGET"
echo ""

if [[ $FORCE -eq 0 ]]; then
  REASON=""
  if [[ $ALIVE -eq 1 ]]; then
    REASON="recorded PID $HOLDER_PID is still alive"
  fi
  if [[ $SAME_USER -eq 0 && -z "$REASON" ]]; then
    REASON="holder is a different user ($HOLDER_USER) — be sure you mean this"
  fi
  if [[ -z "$REASON" ]]; then
    REASON="lock looks stale, but refusing without --force"
  fi
  err "refusing to remove: $REASON"
  echo "    rerun with --force to remove anyway"
  exit 1
fi

if [[ $ALIVE -eq 1 ]]; then
  warn "PID $HOLDER_PID is still alive — killing it before removing the lock"
  kill "$HOLDER_PID" 2>/dev/null || true
  # Give it a moment, then SIGKILL if needed.
  for _ in 1 2 3 4 5; do
    pid_alive "$HOLDER_PID" || break
    sleep 1
  done
  if pid_alive "$HOLDER_PID"; then
    warn "PID $HOLDER_PID did not exit on SIGTERM, sending SIGKILL"
    kill -9 "$HOLDER_PID" 2>/dev/null || true
  fi
fi

rm -rf "$LOCK_DIR"
ok "removed local lock at $LOCK_DIR"
exit 0
