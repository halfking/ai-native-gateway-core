#!/usr/bin/env bash
# deploy/sync-env.sh
# 自动同步加密的环境变量到目标服务器
# Usage: bash deploy/sync-env.sh [154|252|kaixuan-1]
#   154      主机部署（llm.kxpms.cn，47.97.111.154）
#   252      阿里云数据面（llm.itestu.cn，115.29.212.252）
#   kaixuan-1  内网 k3s 控制面（192.168.31.28，kaixuan/<SSH_PASSWORD>）

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

[[ "$TARGET" != "154" && "$TARGET" != "252" && "$TARGET" != "kaixuan-1" ]] && error "Usage: $0 [154|252|kaixuan-1]"

export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
[[ ! -f "$SOPS_AGE_KEY_FILE" ]] && error "SOPS age key not found: $SOPS_AGE_KEY_FILE"

ENC_FILE="$PROJECT_ROOT/.env.${TARGET}.enc"
[[ ! -f "$ENC_FILE" ]] && error "Encrypted env file not found: $ENC_FILE"

info "Decrypting $ENC_FILE..."
TEMP_ENV=$(mktemp)
trap "rm -f $TEMP_ENV" EXIT
sops -d "$ENC_FILE" > "$TEMP_ENV" || error "Failed to decrypt $ENC_FILE"

if [[ "$TARGET" == "154" ]]; then
  SSH_HOST="47.97.111.154"
  SSH_USER="root"
  SSH_KEY="${SSH_KEY_154:-$HOME/.ssh/id_ed25519}"
elif [[ "$TARGET" == "252" ]]; then
  SSH_HOST="115.29.212.252"
  SSH_USER="root"
  SSH_KEY="${SSH_KEY_252:-$HOME/.ssh/id_ed25519}"
else  # kaixuan-1
  SSH_HOST="192.168.31.28"
  SSH_USER="kaixuan"
  SSH_KEY="${SSH_KEY_KAIXUAN_1:-$HOME/.ssh/kaixuan1_id_rsa}"
fi

SSH_PORT=25022
SSH_OPTS="-P $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"
SSH_CMD="-p $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"

info "Backing up remote ops-env.sh..."
ssh $SSH_CMD ${SSH_USER}@$SSH_HOST "mkdir -p /etc/llm-gateway-go; \
  [[ -f /etc/llm-gateway-go/ops-env.sh ]] && \
  cp /etc/llm-gateway-go/ops-env.sh /etc/llm-gateway-go/ops-env.sh.bak.\$(date +%s)" || true

info "Uploading ops-env.sh to $SSH_HOST..."
scp $SSH_OPTS "$TEMP_ENV" ${SSH_USER}@$SSH_HOST:/etc/llm-gateway-go/ops-env.sh || error "SCP failed"
ssh $SSH_CMD ${SSH_USER}@$SSH_HOST "chmod 600 /etc/llm-gateway-go/ops-env.sh"

info "Setting up auto-load..."
ssh $SSH_CMD ${SSH_USER}@$SSH_HOST 'bash -s' <<'REMOTE'
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
if [[ "$TARGET" == "154" ]]; then
  VAR="HOST_154_IP"
  EXPECTED="47.97.111.154"
elif [[ "$TARGET" == "252" ]]; then
  VAR="HOST_252_IP"
  EXPECTED="115.29.212.252"
else
  VAR="KAIXUAN_1_IP"
  EXPECTED="192.168.31.28"
fi

ACTUAL=$(ssh $SSH_CMD ${SSH_USER}@$SSH_HOST "bash -l -c 'echo \$$VAR'")
[[ "$ACTUAL" == "$EXPECTED" ]] && info "+ Verified: $VAR=$ACTUAL" || error "Failed: got $ACTUAL"

info "+ Deployment complete for $TARGET"
