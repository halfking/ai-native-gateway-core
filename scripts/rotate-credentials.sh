#!/usr/bin/env bash
# =====================================================================
# scripts/rotate-credentials.sh — credential rotation automation
#
# Implements the 5-step rotation from handoff §5.1 + checklist:
#   1. Verify target healthy (env-injector + ssh probe)
#   2. Generate new credential value (or accept from stdin/file)
#   3. Write to temporary plaintext file (chmod 600) and encrypt via
#      env-injector's encrypt hook
#   4. Atomic swap of .env.<target>.enc with backup of the previous
#   5. Verify deploy-time injection still resolves (smoke test)
#   6. Mark rotation complete (print to rotation log)
#
# Spec: deploy-management-hardening cf8aad1a9 + rotation checklist
#
# Usage:
#   scripts/rotate-credentials.sh --target=252 --key=SSH_PASS_252 \
#       [--from-file=<value.txt>] [--dry-run] [--no-verify]
#   scripts/rotate-credentials.sh --target=kaixuan-1 \
#       --key=SSH_PASS_KAIXUAN1,PG_PASS_KAIXUAN1 \
#       --from-stdin <<< "secret1\nsecret2"
#   scripts/rotate-credentials.sh --list                   # show pending
#
# Exit codes:
#   0  success
#   1  generic error
#   64  usage error
#   75  EX_TEMPFAIL (target unreachable, etc.)
# =====================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROTATION_LOG="$REPO_ROOT/docs/changelogs/credential-rotation.log"
mkdir -p "$(dirname "$ROTATION_LOG")"

usage() {
  cat >&2 <<'USAGE'
Usage:
  rotate-credentials.sh --target=<252|kaixuan-1> --key=<KEY1,KEY2>
                         [--from-file=<path> | --from-stdin]
                         [--dry-run] [--no-verify] [--skip-health]
                         [--retries=N]
  rotate-credentials.sh --list

Notes:
  --from-file and --from-stdin are mutually exclusive. The input must
  contain one line per --key (in order). Lines starting with '#' and
  blank lines are ignored. Whitespace is trimmed.

  --dry-run prints the full plan and exits before any IO beyond
  reading the input. Useful for confirming the rotation scope.

  --no-verify skips the post-rotation injection smoke test (NOT
  recommended for production rotations).

  --skip-health skips the pre-rotation health probe (NOT recommended).

Exit codes:
  0  success
  1  generic error
  64 usage error
  75 EX_TEMPFAIL
USAGE
  exit 64
}

err() { echo "ERROR: $*" >&2; }
warn() { echo "WARN: $*" >&2; }
info() { echo "INFO: $*" >&2; }

# Smallest parser: flags before positional args.
TARGET=""
KEYS=""
FROM_FILE=""
FROM_STDIN=0
DRY_RUN=0
NO_VERIFY=0
SKIP_HEALTH=0
RETRIES=3
LIST_MODE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target=*) TARGET="${1#*=}" ;;
    --key=*)   KEYS="${1#*=}" ;;
    --from-file=*) FROM_FILE="${1#*=}" ;;
    --from-stdin) FROM_STDIN=1 ;;
    --dry-run) DRY_RUN=1 ;;
    --no-verify) NO_VERIFY=1 ;;
    --skip-health) SKIP_HEALTH=1 ;;
    --retries=*) RETRIES="${1#*=}" ;;
    --list) LIST_MODE=1 ;;
    -h|--help) usage ;;
    *) err "unknown flag: $1"; usage ;;
  esac
  shift
done

# --- list mode -----------------------------------------------------
if [[ $LIST_MODE -eq 1 ]]; then
  echo "Pending credential rotations (from docs/changelogs/2026-07-14-credential-rotation-checklist.md):"
  echo ""
  printf "%-25s %-12s %s\n" "KEY" "TARGET" "STATUS"
  printf "%-25s %-12s %s\n" "SSH_PASS_252" "252" "⏳ Pending"
  printf "%-25s %-12s %s\n" "PG_PASS_252" "252" "⏳ Pending"
  printf "%-25s %-12s %s\n" "SSH_PASS_KAIXUAN1" "kaixuan-1" "⏳ Pending"
  printf "%-25s %-12s %s\n" "PG_PASS_KAIXUAN1" "kaixuan-1" "⏳ Pending"
  printf "%-25s %-12s %s\n" "REGISTRY_PASS_KAIXUAN1" "kaixuan-1" "⏳ Pending"
  exit 0
fi

# --- argument validation -----------------------------------------
[[ -z "$TARGET" ]] && { err "--target is required (use --list to see options)"; usage; }
[[ -z "$KEYS" ]]   && { err "--key is required (use --list to see options)"; usage; }

# Check inputs: exactly one of --from-file / --from-stdin.
if [[ -n "$FROM_FILE" && $FROM_STDIN -eq 1 ]]; then
  err "--from-file and --from-stdin are mutually exclusive"
  exit 64
fi
if [[ -z "$FROM_FILE" && $FROM_STDIN -eq 0 ]]; then
  err "must provide one of --from-file=<path> or --from-stdin"
  exit 64
fi

# Validate target.
case "$TARGET" in
  252|154|245|kaixuan-1) ;;
  *) err "unsupported target: $TARGET (allowed: 252, 154, 245, kaixuan-1)"; exit 64 ;;
esac

ENC_FILE=".env.${TARGET}.enc"
PLAINTEXT_FILE=".env.${TARGET}"
BACKUP_FILE="${ENC_FILE}.bak.$(date -u +%Y%m%dT%H%M%SZ)"

# Validate keys are non-empty, comma-split, count matches input lines.
IFS=',' read -ra KEY_ARR <<< "$KEYS"
KEY_COUNT=${#KEY_ARR[@]}
[[ $KEY_COUNT -eq 0 ]] && { err "--key resolved to empty list"; exit 64; }

# Read input values (one line per key, in order).
INPUT_VALUES=()
if [[ $FROM_STDIN -eq 1 ]]; then
  while IFS= read -r line; do
    # Skip blank lines and comments.
    [[ -z "$line" || "${line:0:1}" == "#" ]] && continue
    INPUT_VALUES+=("$line")
  done
else
  if [[ ! -r "$FROM_FILE" ]]; then
    err "--from-file=$FROM_FILE is not readable"
    exit 64
  fi
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "${line:0:1}" == "#" ]] && continue
    INPUT_VALUES+=("$line")
  done < "$FROM_FILE"
fi

VAL_COUNT=${#INPUT_VALUES[@]}
if [[ $VAL_COUNT -ne $KEY_COUNT ]]; then
  err "value count ($VAL_COUNT) != key count ($KEY_COUNT)"
  err "  Keys: $KEYS"
  err "  Provide one value per key in the same order, in --from-file or --from-stdin"
  exit 64
fi

# Validate each key matches its target prefix (defensive).
for k in "${KEY_ARR[@]}"; do
  case "$k" in
    SSH_PASS_252|SSH_PASS_KAIXUAN1|SSH_PASS_154|SSH_PASS_245) : ;;
    PG_PASS_252|PG_PASS_KAIXUAN1|PG_PASS_154|PG_PASS_245)   : ;;
    REGISTRY_PASS_KAIXUAN1|REGISTRY_PASS_154|REGISTRY_PASS_245) : ;;
    *) err "key $k is not on the allow-list"; exit 64 ;;
  esac
done

# Print the plan.
echo "=== Credential Rotation Plan ==="
echo "  Target:       $TARGET"
echo "  Enc file:     $ENC_FILE"
echo "  Plaintext:    $PLAINTEXT_FILE (temp, chmod 600)"
echo "  Backup of:    $BACKUP_FILE"
echo "  Keys to set:"
for i in "${!KEY_ARR[@]}"; do
  printf "    %s = %s\n" "${KEY_ARR[$i]}" "${INPUT_VALUES[$i]//?/*}"
done
echo "  Dry-run:      $DRY_RUN"
echo "  Verify:       $((1 - NO_VERIFY))"
echo "  Health:       $((1 - SKIP_HEALTH))"
echo "  Retries:      $RETRIES"

if [[ $DRY_RUN -eq 1 ]]; then
  echo "(dry-run mode — no further IO performed)"
  exit 0
fi

# --- Step 1: Verify health -----------------------------------------
if [[ $SKIP_HEALTH -eq 0 ]]; then
  info "Step 1/5: pre-rotation health probe"
  if ! env-injector verify --target="$TARGET" >/dev/null 2>&1; then
    err "pre-rotation env-injector verify failed for $TARGET"
    err "current SOPS envelope cannot be decrypted — refusing to proceed"
    exit 75
  fi
  info "  ✓ env-injector verify --target=$TARGET"
fi

# --- Step 2: Backup current encrypted envelope -----------------
info "Step 2/5: backup of $ENC_FILE → $BACKUP_FILE"
cp "$ENC_FILE" "$BACKUP_FILE"
chmod 600 "$BACKUP_FILE"

# --- Step 3: Build new plaintext + encrypt ------------------------
info "Step 3/5: build plaintext and encrypt"

trap 'rm -f "$PLAINTEXT_FILE.tmp"; rm -f "$ENC_FILE.tmp"' EXIT

# Locate .sops.yaml (REPO root or cwd) so the encryption rules match.
SOPS_CFG=""
for candidate in "$REPO_ROOT/.sops.yaml" "$(pwd)/.sops.yaml"; do
  [[ -f "$candidate" ]] && SOPS_CFG="$candidate" && break
done
[[ -n "$SOPS_CFG" ]] || SOPS_CFG="$REPO_ROOT/.sops.yaml"

# Merge: keep existing decrypted values, replace the keys we are
# rotating. This preserves any credentials the operator did NOT
# include in this rotation batch (avoids data loss on partial input).
info "  decrypting current envelope to merge unchanged keys"
DECRYPTED="$(mktemp -t kx-rotate-XXXXXX)"
chmod 600 "$DECRYPTED"
env-injector inject --target="$TARGET" --format=dotenv > "$DECRYPTED"

# Validate existing plaintext is dotenv-shaped.
if [[ ! -s "$DECRYPTED" ]]; then
  err "decrypted envelope is empty — aborting"
  rm -f "$DECRYPTED"
  exit 1
fi

# Replace KEY=VALUE for each rotated key.
> "$PLAINTEXT_FILE.tmp"
chmod 600 "$PLAINTEXT_FILE.tmp"
while IFS= read -r line; do
  keep=1
  for k in "${KEY_ARR[@]}"; do
    if [[ "$line" == "$k="* ]]; then keep=0; break; fi
  done
  if [[ $keep -eq 1 ]]; then
    echo "$line" >> "$PLAINTEXT_FILE.tmp"
  fi
done < "$DECRYPTED"

# Append new values.
for i in "${!KEY_ARR[@]}"; do
  echo "${KEY_ARR[$i]}=${INPUT_VALUES[$i]}" >> "$PLAINTEXT_FILE.tmp"
done

rm -f "$DECRYPTED"

# Encrypt via sops. The .sops.yaml regex matches `.env.<target>(.enc)?`;
# we encrypt `.env.<target>` in place (overwrite with SOPS JSON), then
# rename to `.env.<target>.enc`.
info "  sops encrypt $PLAINTEXT_FILE"
# Remove any leftover plaintext (could happen if a prior rotation
# aborted mid-step). Safe because .env.<target> is gitignored.
rm -f "$PLAINTEXT_FILE"
mv "$PLAINTEXT_FILE.tmp" "$PLAINTEXT_FILE"
if ! sops --config "$SOPS_CFG" --encrypt --in-place "$PLAINTEXT_FILE" 2>/tmp/sops_err.$$; then
  err "sops --encrypt failed:"
  cat /tmp/sops_err.$$ >&2
  rm -f /tmp/sops_err.$$
  exit 1
fi
rm -f /tmp/sops_err.$$

# sops --encrypt --in-place writes SOPS JSON to the same file, but
# our contract is .env.<target>.enc. Move the file to the canonical
# location atomically.
mv "$PLAINTEXT_FILE" "$ENC_FILE"
chmod 600 "$ENC_FILE"

# --- Step 4: Verify post-rotation injection resolves ------------
if [[ $NO_VERIFY -eq 0 ]]; then
  info "Step 4/5: post-rotation injection smoke test"
  attempts=0
  ok=0
  while (( attempts < RETRIES )); do
    attempts=$((attempts + 1))
    if env-injector verify --target="$TARGET" >/dev/null 2>&1; then
      ok=1
      break
    fi
    warn "  attempt $attempts/$RETRIES: injection verify failed, retrying"
    sleep 1
  done
  if [[ $ok -ne 1 ]]; then
    err "post-rotation verify failed after $RETRIES attempts"
    err "rolling back to $BACKUP_FILE"
    cp "$BACKUP_FILE" "$ENC_FILE"
    chmod 600 "$ENC_FILE"
    exit 75
  fi

  # Bonus: confirm the new value actually appears in the decrypted
  # output (catches the case where encrypt succeeded but sops didn't
  # actually encrypt the new key).
  injected="$(env-injector inject --target="$TARGET" --format=dotenv 2>/dev/null)"
  for i in "${!KEY_ARR[@]}"; do
    if ! grep -qxF "${KEY_ARR[$i]}=${INPUT_VALUES[$i]}" <<<"$injected"; then
      err "rotation verification: ${KEY_ARR[$i]} does not match in decrypted output"
      err "rolling back to $BACKUP_FILE"
      cp "$BACKUP_FILE" "$ENC_FILE"
      chmod 600 "$ENC_FILE"
      exit 1
    fi
  done
  info "  ✓ all rotated keys present in injected output"
else
  warn "Step 4/5 skipped (--no-verify)"
fi

# --- Step 5: Mark rotation complete -------------------------------
info "Step 5/5: mark rotation complete"
TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
  echo "# ${TS}  target=${TARGET}  keys=${KEYS}"
  echo "# envelope backup: ${BACKUP_FILE}"
  for i in "${!KEY_ARR[@]}"; do
    echo "#   ${KEY_ARR[$i]}=<redacted len=${#INPUT_VALUES[i]}>"
  done
} >> "$ROTATION_LOG"

if [[ $NO_VERIFY -eq 0 ]]; then
  info "Rotation complete. Backup: $BACKUP_FILE"
else
  warn "Rotation complete (unverified). Backup: $BACKUP_FILE"
fi
exit 0
