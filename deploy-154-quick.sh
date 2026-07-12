#!/usr/bin/env bash
# deploy-154-quick.sh — Fast deploy: rebuild Go binary + web dist and upload both.
# Use this when both Go and frontend changed (or just for safety).
#
# 前置 (HARD-GATE):
#   export DEPLOY_SSH_PASS='<your-password>'   # 密码已不再硬编码
set -euo pipefail

# ── 前置 (HARD-GATE): DEPLOY_SSH_PASS 必须从 env 注入 ─────────
if [ -z "${DEPLOY_SSH_PASS:-}" ]; then
  echo "✗ 必须设置 DEPLOY_SSH_PASS 环境变量（密码已不再硬编码）" >&2
  echo "  示例: export DEPLOY_SSH_PASS='<your-password>'" >&2
  echo "  推荐: 使用 ~/.ssh/id_ed25519 密钥登录" >&2
  exit 1
fi

SSH_HOST="47.97.111.154"
SSH_PORT="25022"
SSH_USER="root"
SSH_PASS="${DEPLOY_SSH_PASS}"
REMOTE_DIR="/opt/llm-gateway-go"
BINARY_NAME="llm-gateway-go"
export SSHPASS="$SSH_PASS"

G='\033[0;32m'; Y='\033[1;33m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
phase() { echo -e "\n${B}═══════ $* ═══════${N}"; }

phase "1. Local Go cross-compile (Linux AMD64)"
unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy
go env -w GOPROXY=https://goproxy.cn,direct
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod -o bin/llm-gateway-go-linux-amd64 ./cmd/gateway
ls -lh bin/llm-gateway-go-linux-amd64
ok "compiled"

phase "2. vite build"
cd web
npx vite build 2>&1 | tail -3
cd ..
ok "vite built"

phase "3. Upload binary + dist"
# Binary
VER=$(sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" \
  "ls /opt/llm-gateway-go/${BINARY_NAME}.v*.linux.amd64 2>/dev/null | grep -oE 'v[0-9]+' | grep -oE '[0-9]+' | sort -n | tail -1")
VER=${VER:-0}
NEXT_VER=$((VER + 1))
info "next version: v${NEXT_VER}"
REMOTE_BIN="${REMOTE_DIR}/${BINARY_NAME}.v${NEXT_VER}.linux.amd64"
sshpass -e scp -P "$SSH_PORT" -o StrictHostKeyChecking=no \
  "bin/llm-gateway-go-linux-amd64" "$SSH_USER@$SSH_HOST:${REMOTE_BIN}"

# Dist (tar)
tar -czf /tmp/llmgo-154-dist.tar.gz -C web dist
sshpass -e scp -P "$SSH_PORT" -o StrictHostKeyChecking=no \
  /tmp/llmgo-154-dist.tar.gz "$SSH_USER@$SSH_HOST:/tmp/"

# Extract on remote. Production serves from ${REMOTE_DIR}/web (not web/dist),
# so we flatten dist/. into web/ root. Backup the served files first.
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
set -e
cd ${REMOTE_DIR}/web
TS=\$(date +%Y%m%d-%H%M%S)
if [ -d assets ]; then mv assets assets.bak.\$TS; fi
if [ -f index.html ]; then mv index.html index.html.bak.\$TS; fi
rm -rf assets index.html
tar -xzf /tmp/llmgo-154-dist.tar.gz
rm -f /tmp/llmgo-154-dist.tar.gz
cp -a dist/. .
find . -name '._*' -delete 2>/dev/null
chown -R root:root assets index.html favicon.png 2>/dev/null || true
ls -la | head -10
"
ok "binary + dist uploaded"

phase "4. Switch symlink + restart"
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
set -e
cd ${REMOTE_DIR}
if [ -L ${BINARY_NAME} ] && [ -e ${BINARY_NAME} ]; then
  TARGET=\$(readlink ${BINARY_NAME})
  echo \"current symlink -> \$TARGET\"
  LAST_VER=\$(echo \"\$TARGET\" | grep -oE 'v[0-9]+' | grep -oE '[0-9]+')
  cp -a ${BINARY_NAME} ${BINARY_NAME}.bak-\$(date +%Y%m%d-%H%M%S)-v\${LAST_VER} || true
fi
ln -sf ${REMOTE_BIN} ${BINARY_NAME}
chmod +x ${BINARY_NAME}
ls -la ${BINARY_NAME}
systemctl restart ${BINARY_NAME}.service
sleep 6
systemctl status ${BINARY_NAME}.service --no-pager | head -5
echo ---
curl -sS -o /dev/null -w 'healthz HTTP %{http_code}\n' https://llm.kxpms.cn/healthz
"
ok "deploy complete → $(git log -1 --format='%h %s')"