#!/usr/bin/env bash
# sync-llm-kxpms-cert.sh — keep 252 llm.kxpms.cn cert in sync with 154
#
# Source: 154 /etc/letsencrypt/live/kxpms.cn/{fullchain.pem,privkey.pem}
# Target: 252 /etc/nps/conf/certs/hosts/llm.kxpms.cn/{fullchain.pem,privkey.pem}
#
# Why: letsencrypt cert on 154 already has llm.kxpms.cn in SAN; 154 has
#      certbot auto-renewal. We mirror the cert here so 252 nginx (now the
#      primary ingress for llm.kxpms.cn via NPS) keeps a valid cert.
#      154 also keeps a copy, so DNS can be flipped back to 154 at any time.
#
# Auth: dedicated ed25519 key at /root/.ssh/cert-sync-252-154 (pub key
#       installed on 154 /root/.ssh/authorized_keys).
# Trigger: cron @ 03:17 daily, plus manual run.
#
# 关键经验 (2026-07-11):
#   - openssl pkey 没有 -modulus flag（EC 私钥），校验用 pubkey fingerprint
#   - scp 默认 BatchMode=yes 拒绝未知 host，先 ssh-keyscan
#   - 让 nginx 静默 reload：nginx -t && nginx -s reload

set -euo pipefail

LOG=/var/log/sync-llm-kxpms-cert.log
SRC_HOST=47.97.111.154
SRC_PORT=25022
SRC_DIR=/etc/letsencrypt/live/kxpms.cn
DST_DIR=/etc/nps/conf/certs/hosts/llm.kxpms.cn
SSH_KEY=/root/.ssh/cert-sync-252-154
KNOWN=/root/.ssh/known_hosts
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

ts() { date +"%F %T"; }
say() { printf "[%s] %s\n" "$(ts)" "$*" | tee -a "$LOG"; }

mkdir -p "$DST_DIR"

say "== start sync from ${SRC_HOST}:${SRC_DIR} -> ${DST_DIR} =="

# Pull to temp first so we never have a half-written cert at the live path
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile="$KNOWN" \
    -P "$SRC_PORT" "root@${SRC_HOST}:${SRC_DIR}/fullchain.pem" "$TMP_DIR/fullchain.pem" \
    || { say "ERROR: scp fullchain.pem failed"; exit 1; }
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile="$KNOWN" \
    -P "$SRC_PORT" "root@${SRC_HOST}:${SRC_DIR}/privkey.pem"   "$TMP_DIR/privkey.pem" \
    || { say "ERROR: scp privkey.pem failed"; exit 1; }

# Validate cert/key match (public key fingerprint must agree).
# Note: openssl pkey has no -modulus flag (EC keys), so use pubkey md5.
pub_from_cert=$(openssl x509 -in "$TMP_DIR/fullchain.pem" -noout -pubkey 2>/dev/null | openssl md5 | awk '{print $NF}')
pub_from_key=$(openssl pkey -in "$TMP_DIR/privkey.pem" -pubout 2>/dev/null | openssl md5 | awk '{print $NF}')
if [[ "$pub_from_cert" != "$pub_from_key" ]]; then
  say "ERROR: cert/key public key mismatch (cert=$pub_from_cert key=$pub_from_key) - abort"
  exit 2
fi

# Check SAN contains llm.kxpms.cn
if ! openssl x509 -in "$TMP_DIR/fullchain.pem" -noout -text 2>/dev/null \
     | grep -q "DNS:llm.kxpms.cn"; then
  say "ERROR: SAN does not contain llm.kxpms.cn - abort (likely wrong cert)"
  exit 3
fi

# Diff vs current - only swap if changed
old_sum=$(sha256sum "$DST_DIR/fullchain.pem" 2>/dev/null | awk '{print $1}' || echo none)
new_sum=$(sha256sum "$TMP_DIR/fullchain.pem" | awk '{print $1}')
if [[ "$old_sum" == "$new_sum" ]]; then
  say "no change (sha256=$new_sum), skip reload"
  exit 0
fi

say "cert changed: $old_sum -> $new_sum"
install -m 0600 "$TMP_DIR/fullchain.pem" "$DST_DIR/fullchain.pem"
install -m 0600 "$TMP_DIR/privkey.pem"   "$DST_DIR/privkey.pem"
chmod 0600 "$DST_DIR"/*.pem
say "wrote new cert to ${DST_DIR}"

# Validate nginx config and reload
if nginx -t >> "$LOG" 2>&1; then
  nginx -s reload >> "$LOG" 2>&1 && say "nginx reloaded"
else
  say "ERROR: nginx -t failed; cert NOT applied via reload"
  exit 4
fi

say "== done =="