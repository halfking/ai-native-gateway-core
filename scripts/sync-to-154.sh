#!/usr/bin/env bash
# ============================================================================
# sync-to-154.sh — certbot deploy hook for kxpms.cn cert (2026-07-12)
#
# Installed on 252 at /etc/letsencrypt/renewal-hooks/deploy/sync-to-154.sh.
# After certbot on 252 renews the kxpms.cn cert, this hook:
#   1. Copies the fresh cert+key to 154 via scp (using a dedicated ed25519 key)
#      so 154 nginx keeps serving llm.kxpms.cn with a valid cert
#   2. Reloads 154 nginx (via SSH) to pick up the new cert
#   3. Reloads local 252 nginx to pick up the fresh cert
#
# Auth: dedicated ed25519 key at /root/.ssh/cert-sync-252-154 (pub key
#       installed on 154 /root/.ssh/authorized_keys).
# Trigger: certbot deploy phase after any successful renewal.
# Wired by: certbot-renew.service runs "/usr/bin/certbot renew ... $DEPLOY_HOOK"
#           where $DEPLOY_HOOK is set in /etc/sysconfig/certbot.
#
# 关键经验 (2026-07-12):
#   - 必须 reload 两边 nginx，否则 cert 写到磁盘但 nginx 仍 serve 旧 cert
#     （nginx 启动时 load cert 到内存，HUP 信号触发 reload 才重新 load）
#   - 用 ed25519 key 而非 sshpass；pubkey 在 setup 时已 push 到 154
#   - StrictHostKeyChecking=no + known_hosts 避免 BatchMode 失败
# ============================================================================
set -euo pipefail

LOG=/var/log/sync-to-154.log
ts() { date +"%F %T"; }
say() { printf "[%s] %s\n" "$(ts)" "$*" | tee -a "$LOG"; }

CERT_DIR_LOCAL=/etc/letsencrypt/live/kxpms.cn
HOST_154="${HOST_154:-47.97.111.154}"
PORT="${PORT:-25022}"
SSH_KEY="${SSH_KEY:-/root/.ssh/cert-sync-252-154}"
KNOWN="${KNOWN:-/root/.ssh/known_hosts}"
CERT_DIR_REMOTE="/etc/letsencrypt/live/kxpms.cn"

say "== certbot deploy hook: kxpms.cn renewal sync to 154 =="

say "  scp cert+key to 154..."
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile="$KNOWN" \
    -P "$PORT" "$CERT_DIR_LOCAL/fullchain.pem" "root@${HOST_154}:$CERT_DIR_REMOTE/fullchain.pem"
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile="$KNOWN" \
    -P "$PORT" "$CERT_DIR_LOCAL/privkey.pem"   "root@${HOST_154}:$CERT_DIR_REMOTE/privkey.pem"

say "  reload 154 nginx..."
ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile="$KNOWN" \
    -p "$PORT" "root@${HOST_154}" "nginx -t && nginx -s reload"

say "  reload 252 nginx..."
nginx -t && nginx -s reload

say "== done =="