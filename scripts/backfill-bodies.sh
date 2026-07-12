#!/usr/bin/env bash
# scripts/backfill-bodies.sh
#
# Drain the body-table backfill loop. Each batch is its own implicit
# transaction (procedural CALL), so memory stays bounded regardless
# of how large individual JSONB rows are.
#
# Usage:
#   bash scripts/backfill-bodies.sh 184         # default batch 200
#   BATCH=500 bash scripts/backfill-bodies.sh 184

set -euo pipefail

TARGET="${1:-184}"
case "$TARGET" in
  184)
    SSH_HOST="47.97.111.154"  # 154 替代 184
    SSH_PORT=25022
    NS="pms-test"
    ;;
  71)
    SSH_HOST="47.97.111.154"  # 154 替代 71 (docker 栈迁回)
    SSH_PORT=25022
    NS="pms-test"
    ;;
  *)
    echo "Usage: $0 [71|184]" >&2
    exit 1
    ;;
esac

BATCH="${BATCH:-200}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info() { echo -e "${GREEN}[INFO]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }

# Decrypt env to get DB password
ENC_FILE="$PROJECT_ROOT/.env.${TARGET}.enc"
if [[ -f "$ENC_FILE" ]]; then
    export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
    [[ -f "$SOPS_AGE_KEY_FILE" ]] || warn "No SOPS key — using known password"
fi
DB_PASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"
DB_USER="llm_gateway"
DB_NAME="llm_gateway"

SSH_CMD="-p $SSH_PORT -o StrictHostKeyChecking=no -o BatchMode=yes"

info "Locating llm-gateway-pg pod in namespace $NS on $SSH_HOST..."
POD=$(ssh $SSH_CMD root@$SSH_HOST \
    "kubectl get pod -n $NS -l app=llm-gateway-pg -o jsonpath='{.items[0].metadata.name}'")
[[ -n "$POD" ]] || { echo "No pod found" >&2; exit 1; }
info "Pod: $NS/$POD"

# Each CALL backfills one batch and returns a count in the NOTICE.
# Loop until progress view shows 0 rows pending.
info "Starting backfill loop (batch=$BATCH, Ctrl-C to stop safely)..."
while :; do
    set +e
    BATCH_LOG=$(ssh $SSH_CMD root@$SSH_HOST \
        "kubectl exec -n $NS $POD -c citus -- \
            env PGPASSWORD='$DB_PASSWORD' \
            psql -U $DB_USER -d $DB_NAME -X -tA -v ON_ERROR_STOP=1 \
            -c \"CALL backfill_request_logs_bodies($BATCH);\"" 2>&1)
    CALL_RC=$?
    set -e

    if [[ "$CALL_RC" -ne 0 ]]; then
        warn "CALL failed (rc=$CALL_RC), will retry in 3s"
        sleep 3
        continue
    fi

    # Pull pending count
    PENDING=$(ssh $SSH_CMD root@$SSH_HOST \
        "kubectl exec -n $NS $POD -c citus -- \
            env PGPASSWORD='$DB_PASSWORD' \
            psql -U $DB_USER -d $DB_NAME -X -tA \
            -c 'SELECT rows_pending_backfill FROM request_logs_bodies_progress'" 2>/dev/null)
    PENDING=$(echo "$PENDING" | tr -d '[:space:]')

    if [[ -z "$PENDING" || "$PENDING" -le 0 ]]; then
        info "Backfill complete (rows_pending=$PENDING)"
        break
    fi

    info "  progress: $PENDING rows pending..."
done

info "Final state:"
ssh $SSH_CMD root@$SSH_HOST \
    "kubectl exec -n $NS $POD -c citus -- \
        env PGPASSWORD='$DB_PASSWORD' \
        psql -U $DB_USER -d $DB_NAME -X \
        -c 'SELECT source_rows_with_body, bodies_rows, rows_pending_backfill FROM request_logs_bodies_progress'"
