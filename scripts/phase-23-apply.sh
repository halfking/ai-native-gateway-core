#!/usr/bin/env bash
# scripts/phase-23-apply.sh
#
# Apply phase-23 SQL files to the llm-gateway-pg primary in 184 (or 71).
#
# Phase 23 enforces the columnar invariant for INSERT-only parents,
# so future partitions created by any path inherit the correct access
# method automatically.
#
# Usage:
#   bash scripts/phase-23-apply.sh 184
#   bash scripts/phase-23-apply.sh 71
#
# Idempotent: safe to run repeatedly.

set -euo pipefail

TARGET="${1:-184}"
case "$TARGET" in
  184)
    SSH_HOST="14.103.112.184"
    SSH_PORT=25022
    NS="pms-test"
    ;;
  71)
    SSH_HOST="14.103.174.71"
    SSH_PORT=25022
    NS="pms-test"
    ;;
  *)
    echo "Usage: $0 [71|184]" >&2
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PHASE_DIR="$PROJECT_ROOT/sql/scripts/phase-23-columnar-invariant"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info() { echo -e "${GREEN}[INFO]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }

[[ -d "$PHASE_DIR" ]] || { echo "Phase 23 dir not found: $PHASE_DIR" >&2; exit 1; }

# Decrypt env to get DB password (no-op if already source-able)
ENC_FILE="$PROJECT_ROOT/.env.${TARGET}.enc"
if [[ -f "$ENC_FILE" ]]; then
    export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
    if [[ -f "$SOPS_AGE_KEY_FILE" ]]; then
        eval "$(sops -d "$ENC_FILE" 2>/dev/null | grep -E '^export ' | sed 's/^export //')" \
            || warn "Failed to decrypt $ENC_FILE — continuing without it"
    fi
fi

SSH_CMD="-p $SSH_PORT -o StrictHostKeyChecking=no -o BatchMode=yes"
SCP_CMD="-P $SSH_PORT -o StrictHostKeyChecking=no -o BatchMode=yes"

info "Locating llm-gateway-pg pod in namespace $NS on $SSH_HOST..."
POD=$(ssh $SSH_CMD root@$SSH_HOST \
    "kubectl get pod -n $NS -l app=llm-gateway-pg -o jsonpath='{.items[0].metadata.name}'")
[[ -n "$POD" ]] || { echo "No pod found" >&2; exit 1; }
info "Pod: $NS/$POD"

REMOTE_DIR="/tmp/phase-23"
ssh $SSH_CMD root@$SSH_HOST "rm -rf $REMOTE_DIR && mkdir -p $REMOTE_DIR"
info "Copying phase-23 SQL files..."
scp $SCP_CMD "$PHASE_DIR"/[0-9]*.sql root@$SSH_HOST:$REMOTE_DIR/

info "Copying files into pod..."
ssh $SSH_CMD root@$SSH_HOST \
    "kubectl cp $REMOTE_DIR $NS/$POD:/tmp/phase-23 -c citus"

RUN_DIR="/tmp/phase-23"
RC=0
for f in $(ssh $SSH_CMD root@$SSH_HOST "ls $REMOTE_DIR" | sort); do
    info "Applying $f"
    set +e
    ssh $SSH_CMD root@$SSH_HOST \
        "kubectl exec -n $NS $POD -c citus -- \
            env PGPASSWORD='$DB_PASSWORD' \
            psql -U $DB_USER -d $DB_NAME \
            -v ON_ERROR_STOP=1 -e -f $RUN_DIR/$f" 2>&1
    rc=$?
    set -e
    if [[ "$rc" -ne 0 ]]; then
        warn "$f exited with rc=$rc"
        RC=$rc
    fi
done

info "Phase 23 apply complete (overall rc=$RC)."
exit "$RC"
