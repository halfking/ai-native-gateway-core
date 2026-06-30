#!/usr/bin/env bash
# deploy/sync-env.sh
# 自动同步加密的环境变量到目标服务器
# Usage: bash deploy/sync-env.sh [71|184]

set -euo pipefail

TARGET="${1:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

[[ "$TARGET" != "71" && "$TARGET" != "184" ]] && error "Usage: $0 [71|184]"

export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
[[ ! -f "$SOPS_AGE_KEY_FILE" ]] && error "SOPS age key not found: $SOPS_AGE_KEY_FILE"

ENC_FILE="$PROJECT_ROOT/.env.${TARGET}.enc"
[[ ! -f "$ENC_FILE" ]] && error "Encrypted env file not found: $ENC_FILE"

info "Decrypting $ENC_FILE..."
TEMP_ENV=$(mktemp)
trap "rm -f $TEMP_ENV" EXIT
sops -d "$ENC_FILE" > "$TEMP_ENV" || error "Failed to decrypt $ENC_FILE"

if [[ "$TARGET" == "71" ]]; then
  SSH_HOST="14.103.174.71"
  SSH_KEY="$HOME/.ssh/71_id_rsa"
else
  SSH_HOST="14.103.112.184"
  SSH_KEY="$HOME/.ssh/56_id_rsa"
fi

SSH_PORT=25022
SSH_OPTS="-P $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"
SSH_CMD="-p $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"

info "Backing up remote ops-env.sh..."
ssh $SSH_CMD root@$SSH_HOST "mkdir -p /etc/llm-gateway-go; \
  [[ -f /etc/llm-gateway-go/ops-env.sh ]] && \
  cp /etc/llm-gateway-go/ops-env.sh /etc/llm-gateway-go/ops-env.sh.bak.\$(date +%s)" || true

info "Uploading ops-env.sh to $SSH_HOST..."
scp $SSH_OPTS "$TEMP_ENV" root@$SSH_HOST:/etc/llm-gateway-go/ops-env.sh || error "SCP failed"
ssh $SSH_CMD root@$SSH_HOST "chmod 600 /etc/llm-gateway-go/ops-env.sh"

info "Setting up auto-load..."
ssh $SSH_CMD root@$SSH_HOST 'bash -s' <<'REMOTE'
set -e
if [[ ! -f /etc/profile.d/llm-gateway-ops.sh ]]; then
  cat > /etc/profile.d/llm-gateway-ops.sh <<'LOADER'
if [[ -n "${BASH_VERSION:-}" || -n "${ZSH_VERSION:-}" ]]; then
  [[ -r /etc/llm-gateway-go/ops-env.sh ]] && source /etc/llm-gateway-go/ops-env.sh
fi
LOADER
  chmod 644 /etc/profile.d/llm-gateway-ops.sh
  echo "  + profile.d created"
else
  echo "  + profile.d exists"
fi
if ! grep -qF '# llm-gateway-ops' /etc/bash.bashrc 2>/dev/null; then
  echo '' >> /etc/bash.bashrc
  echo '# llm-gateway-ops auto-load' >> /etc/bash.bashrc
  echo '[[ -r /etc/llm-gateway-go/ops-env.sh ]] && source /etc/llm-gateway-go/ops-env.sh' >> /etc/bash.bashrc
  echo "  + bash.bashrc updated"
else
  echo "  + bash.bashrc configured"
fi
REMOTE

info "Verifying..."
if [[ "$TARGET" == "71" ]]; then
  VAR="HOST_71_IP"
  EXPECTED="14.103.174.71"
else
  VAR="INTERNAL_PUBLIC_IP"
  EXPECTED="14.103.112.184"
fi

ACTUAL=$(ssh $SSH_CMD root@$SSH_HOST "bash -l -c 'echo \$$VAR'")
[[ "$ACTUAL" == "$EXPECTED" ]] && info "+ Verified: $VAR=$ACTUAL" || error "Failed: got $ACTUAL"

info "+ Deployment complete for $TARGET"
