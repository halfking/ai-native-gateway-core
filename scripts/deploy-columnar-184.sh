#!/bin/bash
# scripts/deploy-columnar-184.sh
#
# Deploy the columnar-invariant build to 184's gateway pod.
# Pushes binary + Dockerfile.incremental artifacts to 184, builds an
# image using the existing kx-llm-gateway-go:latest as base, then
# rolls the deployment.
#
# Run from services/llm-gateway-go on the build host.

set -euo pipefail

SERVER="184"
SSH_PORT=25022
SSH_KEY="$HOME/.ssh/56_id_rsa"
SSH_CMD="-p $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"
SCP_CMD="-P $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"

NAMESPACE="pms-test"
DEPLOYMENT="llm-gateway-go-deployment"
REMOTE_BUILD_DIR="/opt/kx-memora-build/services/llm-gateway-go"
LOCAL_TAG="columnar-$(date +%Y%m%d-%H%M%S)"

[[ -f llm-gateway-go-linux-amd64 ]] || { echo "Binary missing" >&2; exit 1; }
[[ -f VERSION ]] || { echo "VERSION missing" >&2; exit 1; }

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info() { echo -e "${GREEN}[INFO]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }

# 1. Stage remote build dir
info "1. Stage remote build dir..."
ssh $SSH_CMD root@$SERVER bash -s <<'REMOTE'
set -e
REMOTE_DIR="/tmp/columnar-deploy"
rm -rf $REMOTE_DIR && mkdir -p $REMOTE_DIR
mkdir -p $REMOTE_DIR/build
ls $REMOTE_DIR
REMOTE

# 2. Push build artifacts
info "2. Push binary + Dockerfile.incremental..."
scp $SCP_CMD llm-gateway-go-linux-amd64 root@$SERVER:/tmp/columnar-deploy/build/llm-gateway-go
scp $SCP_CMD VERSION .deploy_seq root@$SERVER:/tmp/columnar-deploy/build/
scp $SCP_CMD Dockerfile.incremental root@$SERVER:/tmp/columnar-deploy/build/
info "  (skipping web/dist — runtime base already has it)"

# 3. Build image on 184
info "3. Build image on 184 (Dockerfile.incremental FROM kx-llm-gateway-go:latest)..."
ssh $SSH_CMD root@$SERVER bash <<REMOTE
set -e
cd /tmp/columnar-deploy/build

docker build \
    --build-arg BASE_IMAGE=kx-llm-gateway-go:latest \
    -t 127.0.0.1:5000/kx-llm-gateway-go:$LOCAL_TAG \
    -t 127.0.0.1:5000/kx-llm-gateway-go:columnar-latest \
    -f Dockerfile.incremental .

docker push 127.0.0.1:5000/kx-llm-gateway-go:$LOCAL_TAG

echo "=== rolling deployment ==="
kubectl -n $NAMESPACE set image deployment/$DEPLOYMENT \
    llm-gateway-go=127.0.0.1:5000/kx-llm-gateway-go:$LOCAL_TAG

kubectl -n $NAMESPACE rollout status deployment/$DEPLOYMENT --timeout=2m
REMOTE

# 4. Verify
info "4. Verify the new boot logs include columnar_invariant_check..."
sleep 5
ssh $SSH_CMD root@$SERVER bash <<'REMOTE'
set -e
POD=$(kubectl -n pms-test get pods -l app=llm-gateway-go \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')
echo "Pod: $POD"
echo "Image: $(kubectl -n pms-test get pod $POD -o jsonpath='{.spec.containers[0].image}')"
echo ""
echo "=== columnar boot log lines ==="
kubectl -n pms-test logs "$POD" --tail=200 2>/dev/null | grep -E "columnar" || echo "(no columnar log lines yet)"
REMOTE
