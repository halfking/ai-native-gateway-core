#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/llm-gw-swap.sh
#
# Atomic swap of /opt/llm-gateway-go/current -> releases/<dir>
# with pre-flight integrity checks. Designed to be invoked from
# `scripts/deploy.sh` or directly from a maintenance shell.
#
# Why this exists
# ---------------
# 2026-08-09 (audit AUDIT-2026-08-09-seq1479-symlink-mislabel-and-audit-sql-regression.md):
#   A previous session's handoff claimed seq 1479 was live on 154 prod at
#   23:36:35 CST. Verification (sha256sum + fix-marker string grep) showed
#   the actually-running binary did NOT contain the IR-converter fallback
#   strings from cddb9956, even though its directory's version.json claimed
#   build_seq 1479. The deployment + post-deploy swap had silently rotated
#   to an unrelated artifact. Two TimeoutStopSec-driven SIGKILLs at 23:48 /
#   23:52 compounded the divergence.
#
#   When that session was diagnosed, a manual swap to a binary that DID
#   contain the fix strings triggered a different latent regression
#   (audit-keyword SQL), which produced HTTP 500 on 79% of requests for
#   ~7 minutes. Manual rollback was required.
#
# This script makes both classes of failure detectable *before* the
# service is asked to consume the binary. It is **the** entrypoint for any
# future swap on 154 / 245 / any sibling topology that uses the same
# release-dir pattern.
#
# Contract
# --------
# Reads (positional + flags):
#   $1                         absolute path to the target release dir under
#                              /opt/llm-gateway-go/releases/<name>/.
#                              Must contain an executable at the conventional
#                              name (binary_name flag below; defaults to
#                              "llm-gateway-go" on 154, "gateway" on 245).
#
#   --service <name>           systemd unit name (default: llm-gateway-go)
#   --binary-name <name>       binary filename inside the release dir
#                              (default: llm-gateway-go; 245 uses "gateway")
#   --install-dir <dir>        gateway install root (default: /opt/llm-gateway-go)
#   --require-marker <s>       string that MUST be present in the binary's
#                              text (run `--list-expected` for built-ins).
#                              Pass multiple times to require multiple
#                              markers (all must match).
#   --expect-sha <sha>         optional; if set, refuse to swap unless
#                              binary sha256 matches this exactly.
#   --history-file <path>      file where pre/post state is recorded
#                              (default: <install-dir>/.deploy-history)
#   --skip-systemd-restart     do not run systemctl restart; useful for
#                              CI that restarts via a different path.
#   --dry-run                  print the would-be steps and exit 0.
#   --yes                      skip the interactive confirmation prompt.
#   --list-expected            list built-in fix markers and exit.
#   --help                     show this header and exit.
#
# Records one line per swap attempt:
#   SWAP_AT=YYYYMMDD-HHMMSS  service=<svc>  from=<old_release>  to=<new_release>  \
#       new_sha=<sha>  markers=<m1,m2,...>  result=ok|fail-reason
# appended to the history file. Failures are also recorded.
#
# Exit codes:
#   0   swap succeeded (or --dry-run equivalent)
#   2   usage / argument error
#   3   target release dir missing or empty
#   4   target binary missing or not executable
#   5   sha mismatch (when --expect-sha given)
#   6   required marker string absent in binary
#   7   systemd restart failed
#   8   post-swap pid's /proc/<pid>/exe does not point at the new release
#
# Refs:
#   docs/audits/AUDIT-2026-08-09-seq1479-symlink-mislabel-and-audit-sql-regression.md
#   deploy/llm-gateway-go.service (154)
#   deploy/llmgo-245.service        (245)
# =====================================================================

set -euo pipefail

# ---- built-in marker strings ------------------------------------------
# Each entry is "<id>|<literal substring>". Markers are the dispatch id
# and the literal token the binary's compiled string-table must contain.
# Keep this table in sync with the fixes landed in source tree; the
# audit-log addendum is the canonical source.
MARKERS_SEQ1479_LEGACY=(
  "seq1479|ir_converter_circuit_open_fallback_to_legacy_chat"
  "seq1479|ir_converter_circuit_open_fallback_to_legacy_anthropic"
  "seq1479|ErrConverterCircuitOpen"
)
# Export both groups so callers (and shellcheck) see them as part of the
# public contract: see --list-expected output and the recommended marker
# combinations in docs/changelogs/.
export MARKERS_SEQ1479_LEGACY
MARKERS_SEQ1477_CRED_CIRCUIT=(
  "seq1477|executor: circuit open on sole candidate, failing open"
)
export MARKERS_SEQ1477_CRED_CIRCUIT

# ---- helpers ----------------------------------------------------------
_red()   { printf '\033[0;31m%s\033[0m\n' "$*" >&2; }
_green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
_yellow(){ printf '\033[1;33m%s\033[0m\n' "$*"; }
_blue()  { printf '\033[0;34m%s\033[0m\n' "$*"; }
err()    { _red "✗ $*"; }
ok()     { _green "✓ $*"; }
info()   { _yellow "▶ $*"; }

usage() {
  sed -n '2,/^# ====/p' "$0" | sed -e 's/^# \{0,1\}//' -e '/^=====/d'
}

list_expected() {
  echo "Built-in marker groups:"
  for entry in "${MARKERS_SEQ1479_LEGACY[@]}"; do
    echo "  --require-marker ${entry#*|}"
  done
  echo
  echo "Marker groups are emitted as multiple --require-marker flags."
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage; exit 0
fi
if [[ "${1:-}" == "--list-expected" ]]; then
  list_expected; exit 0
fi

# ---- arg parsing ------------------------------------------------------
SERVICE="llm-gateway-go"
BINARY_NAME="llm-gateway-go"
INSTALL_DIR="/opt/llm-gateway-go"
HISTORY_FILE=""
EXPECT_SHA=""
SKIP_RESTART=0
DRY_RUN=0
ASSUME_YES=0
declare -a REQUIRED_MARKERS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --service)            SERVICE="$2"; shift 2;;
    --binary-name)        BINARY_NAME="$2"; shift 2;;
    --install-dir)        INSTALL_DIR="$2"; shift 2;;
    --history-file)       HISTORY_FILE="$2"; shift 2;;
    --require-marker)     REQUIRED_MARKERS+=("$2"); shift 2;;
    --expect-sha)         EXPECT_SHA="$2"; shift 2;;
    --skip-systemd-restart) SKIP_RESTART=1; shift;;
    --dry-run)            DRY_RUN=1; shift;;
    --yes)                ASSUME_YES=1; shift;;
    --list-expected)      list_expected; exit 0;;
    --help|-h)            usage; exit 0;;
    -*)                   err "unknown flag: $1"; usage; exit 2;;
    *)
      if [[ -z "${TARGET_RELEASE_DIR:-}" ]]; then
        TARGET_RELEASE_DIR="$1"
      else
        err "multiple positional args not supported: got both '$TARGET_RELEASE_DIR' and '$1'"
        exit 2
      fi
      shift;;
  esac
done

[[ -z "${TARGET_RELEASE_DIR:-}" ]] && { err "no target release dir given"; usage; exit 2; }

# Resolve history path
if [[ -z "$HISTORY_FILE" ]]; then
  HISTORY_FILE="${INSTALL_DIR}/.deploy-history"
fi

# Normalize target: must be absolute and under INSTALL_DIR/releases/
case "$TARGET_RELEASE_DIR" in
  "${INSTALL_DIR}/releases/"*)
    RELEASE_DIR="$TARGET_RELEASE_DIR"
    ;;
  /*)
    err "target release dir must be under ${INSTALL_DIR}/releases/ — got '$TARGET_RELEASE_DIR'"
    exit 2
    ;;
  *)
    err "target release dir must be an absolute path — got '$TARGET_RELEASE_DIR'"
    exit 2
    ;;
esac

TS=$(date +%Y%m%d-%H%M%S)
RECORD_AT="SWAP_AT=${TS}  service=${SERVICE}  to=${RELEASE_DIR}"

# ---- dry-run short circuit --------------------------------------------
run_step() {
  if [[ $DRY_RUN -eq 1 ]]; then
    info "[dry-run] $*"
  else
    "$@"
  fi
}

# ---- pre-flight checks ------------------------------------------------
info "Pre-flight: target = ${RELEASE_DIR}"

# History-record helper: skip writes during --dry-run so we don't pollute
# .deploy-history with phantom swap attempts.
record() {
  local line="$1"
  if [[ $DRY_RUN -eq 1 ]]; then
    info "[dry-run] history: $line"
  else
    printf '%s\n' "$line" >> "$HISTORY_FILE"
  fi
}

if [[ ! -d "$RELEASE_DIR" ]]; then
  err "release dir does not exist: $RELEASE_DIR"
  record "${RECORD_AT}  result=fail-missing-dir"
  exit 3
fi

NEW_BIN="${RELEASE_DIR}/${BINARY_NAME}"
if [[ ! -x "$NEW_BIN" ]]; then
  err "binary missing or not executable: $NEW_BIN"
  record "${RECORD_AT}  result=fail-missing-binary"
  exit 4
fi

NEW_SHA="$(sha256sum "$NEW_BIN" | awk '{print $1}')"
info "  sha256: ${NEW_SHA}"
info "  size:   $(stat -c '%s' "$NEW_BIN") bytes"

if [[ -n "$EXPECT_SHA" && "$NEW_SHA" != "$EXPECT_SHA" ]]; then
  err "sha mismatch — expected $EXPECT_SHA, got $NEW_SHA"
  record "${RECORD_AT}  result=fail-sha  new_sha=${NEW_SHA}"
  exit 5
fi

declare -a MISSING_MARKERS=()
if [[ ${#REQUIRED_MARKERS[@]} -gt 0 ]]; then
  info "Checking ${#REQUIRED_MARKERS[@]} required marker(s)..."
  # `strings` can be expensive on 60+ MB binaries; only run once.
  # NB: do NOT pipe BIN_STRINGS through `grep -q` — `set -o pipefail`
  # combined with grep's early-exit (it closes stdin after first match)
  # makes `printf "%s" "$BIN_STRINGS"` look like it failed with SIGPIPE
  # (exit 141), which under `set -e` aborts the script before our
  # MISSING_MARKERS accumulator is reached. Use a here-string + bash's
  # built-in substring matching instead — same semantics, no pipeline.
  BIN_STRINGS="$(strings "$NEW_BIN" 2>/dev/null || true)"
  for marker in "${REQUIRED_MARKERS[@]}"; do
    if [[ "$BIN_STRINGS" == *"$marker"* ]]; then
      info "  ✓ marker present: ${marker}"
    else
      MISSING_MARKERS+=("$marker")
      err "  ✗ marker MISSING: ${marker}"
    fi
  done
fi
if [[ ${#MISSING_MARKERS[@]} -gt 0 ]]; then
  err "binary lacks required marker(s): ${MISSING_MARKERS[*]}"
  record "${RECORD_AT}  result=fail-marker  new_sha=${NEW_SHA}  missing=${MISSING_MARKERS[*]}"
  exit 6
fi

# ---- current symlink state --------------------------------------------
CURRENT_LINK="${INSTALL_DIR}/current"
if [[ -L "$CURRENT_LINK" ]]; then
  OLD_RELEASE_DIR="$(readlink -f "$CURRENT_LINK")"
else
  OLD_RELEASE_DIR=""
fi
info "current -> ${OLD_RELEASE_DIR:-<none>}"
info "new     -> ${RELEASE_DIR}"

# Confirm with the operator unless --yes
if [[ $ASSUME_YES -eq 0 && $DRY_RUN -eq 0 ]]; then
  printf 'Proceed with swap + restart? [y/N] '
  read -r ans
  case "$ans" in
    y|Y|yes|YES) ;;
    *) err "aborted by operator"; record "${RECORD_AT}  result=abort"; exit 1;;
  esac
fi

# ---- execute swap -----------------------------------------------------
run_step rm -f "$CURRENT_LINK"
run_step ln -s "$RELEASE_DIR" "$CURRENT_LINK"

run_step systemctl daemon-reload

if [[ $SKIP_RESTART -eq 0 ]]; then
  if [[ $DRY_RUN -eq 0 ]]; then
    if ! systemctl restart "$SERVICE"; then
      err "systemctl restart ${SERVICE} failed"
      record "${RECORD_AT}  result=fail-restart  new_sha=${NEW_SHA}"
      exit 7
    fi
  fi
fi

# ---- post-swap verification ------------------------------------------
PID=""
if [[ $DRY_RUN -eq 0 ]]; then
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    PID="$(pgrep -f "/${BINARY_NAME}\$" | head -1 || true)"
    [[ -n "$PID" ]] && break
    sleep 1
  done
    if [[ -z "$PID" ]]; then
      err "no running process found for /${BINARY_NAME}\$"
      record "${RECORD_AT}  result=fail-no-pid  new_sha=${NEW_SHA}"
      exit 8
    fi

  EXE="$(readlink "/proc/${PID}/exe" || true)"
  EXPECTED_EXE="${RELEASE_DIR}/${BINARY_NAME}"
  if [[ "$EXE" != "$EXPECTED_EXE" ]]; then
    err "running pid's exe ($EXE) is not the new release (expected $EXPECTED_EXE)"
    record "${RECORD_AT}  result=fail-exe-mismatch  new_sha=${NEW_SHA}  pid_exe=${EXE}"
    exit 8
  fi

  RUNNING_SHA="$(sha256sum "/proc/${PID}/exe" | awk '{print $1}')"
  if [[ "$RUNNING_SHA" != "$NEW_SHA" ]]; then
    err "running pid's sha ($RUNNING_SHA) != new sha ($NEW_SHA) — kernel-cached stale binary?"
    record "${RECORD_AT}  result=fail-sha-mismatch  new_sha=${NEW_SHA}  running_sha=${RUNNING_SHA}"
    exit 8
  fi

  info "running pid $PID, exe $EXE, sha matches new"
fi

# ---- record success ---------------------------------------------------
MARKERS_STR=""
if [[ ${#REQUIRED_MARKERS[@]} -gt 0 ]]; then
  MARKERS_STR="$(IFS=,; echo "${REQUIRED_MARKERS[*]}")"
fi

FROM="from=${OLD_RELEASE_DIR:-<none>}"
SHA_FIELD="new_sha=${NEW_SHA}"
MARKER_FIELD=""
[[ -n "$MARKERS_STR" ]] && MARKER_FIELD="markers=${MARKERS_STR}"

record "${RECORD_AT}  ${FROM}  ${SHA_FIELD}  ${MARKER_FIELD}  result=ok  pid=${PID:-<dry-run>}"

ok "swap complete"
ok "  ${CURRENT_LINK} -> ${RELEASE_DIR}"
ok "  ${SERVICE} running pid ${PID:-<dry-run>}, sha ${NEW_SHA}"
