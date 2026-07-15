#!/usr/bin/env bash
# setup-download-domain-245.sh — install download.kxpms.cn nginx + artifact dir on 245
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

SSH_PORT=25022
SSH_HOST="${SSH_HOST_245:-root@8.136.114.245}"
SSH_KEY="${SSH_KEY_FILE:-$HOME/.ssh/id_ed25519}"

remote() {
  if [[ -f "$SSH_KEY" ]]; then
    ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new "$SSH_HOST" "$@"
  else
    sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new "$SSH_HOST" "$@"
  fi
}

echo "[setup] Creating artifact directory on 245..."
remote "mkdir -p /var/www/download/llm-gateway-go && chown -R root:root /var/www/download"

echo "[setup] Uploading nginx config..."
remote "mkdir -p /etc/nginx/conf.d"
scp -P "$SSH_PORT" ${SSH_KEY:+-i "$SSH_KEY"} \
  "$PROJECT_ROOT/deploy/download.kxpms.cn.nginx.conf" \
  "$SSH_HOST:/etc/nginx/conf.d/download.kxpms.cn.conf"

echo "[setup] Expanding SSL cert SAN for download.kxpms.cn (if certbot available)..."
remote "certbot certonly --nginx --non-interactive --agree-tos --expand \
  -d kxpms.cn -d '*.kxpms.cn' -d llmgo.kxpms.cn -d download.kxpms.cn \
  --email admin@kxpms.cn 2>/dev/null || \
  certbot certonly --webroot -w /var/www/certbot --non-interactive --agree-tos --expand \
  -d download.kxpms.cn --email admin@kxpms.cn 2>/dev/null || \
  echo 'WARN: certbot expand skipped — verify SAN manually'"

echo "[setup] Testing nginx and reloading..."
remote "nginx -t && (systemctl reload nginx 2>/dev/null || nginx -s reload)"

echo "[setup] Setting gateway env for download artifacts..."
remote "grep -q '^DOWNLOAD_ARTIFACT_ROOT=' /opt/llm-gateway-go/.env 2>/dev/null || \
  echo 'DOWNLOAD_ARTIFACT_ROOT=/var/www/download/llm-gateway-go' >> /opt/llm-gateway-go/.env; \
  grep -q '^DOWNLOAD_BASE_URL=' /opt/llm-gateway-go/.env 2>/dev/null || \
  echo 'DOWNLOAD_BASE_URL=https://download.kxpms.cn/llm-gateway-go' >> /opt/llm-gateway-go/.env; \
  grep -q '^GIT_REPO_URL=' /opt/llm-gateway-go/.env 2>/dev/null || \
  echo 'GIT_REPO_URL=https://github.com/halfking/SI-LLM-Gateway' >> /opt/llm-gateway-go/.env"

echo "[setup] Restarting gateway to pick up download env..."
remote "systemctl restart llm-gateway-go && sleep 4"

echo "[setup] Verifying endpoints..."
curl -sfI "https://download.kxpms.cn/healthz" | head -3 || echo "WARN: download.kxpms.cn healthz check failed"
curl -sfI "https://llmgo.kxpms.cn/download" | head -3 || echo "WARN: llmgo download page check failed"

echo "[setup] Done. Artifact root: /var/www/download/llm-gateway-go/{version}/"
