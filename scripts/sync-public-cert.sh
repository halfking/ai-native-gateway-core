#!/usr/bin/env bash
# Pull the public-trusted (Let's Encrypt) cert for DL_DOMAIN from the edge host
# (252, where certbot renews it) into the local caddy PKI dir, then reload caddy
# if anything changed. Designed to run from cron daily; safe to run by hand.
#
# ## 与 setup-caddy.sh 的关系
#
# 本脚本只搬运/校验证书文件并 reload 运行中的 caddy（admin endpoint
# localhost:2019），不重写 Caddyfile。:443 站点块由
# `DL_PUBLIC_TLS=1 bash scripts/setup-caddy.sh` 生成并指向
# pki/<domain>.le.{crt,key}——与重跑 setup-caddy.sh（会重启 caddy）相比，
# 续期日的 reload 不中断现有连接。
#
# ## 用法
#
#     bash scripts/sync-public-cert.sh            # pull + reload if changed
#     bash scripts/sync-public-cert.sh --check    # only print validity, no write
#
# 环境变量（带默认值）：
#   CERT_SRC_HOST=root@115.29.212.252   边缘机（certbot 所在）
#   CERT_SRC_PORT=25022                 边缘机 SSH 端口
#   CERT_SRC_DIR=/etc/letsencrypt/live/<domain>
#   DL_DOMAIN=llm.itestu.cn
#   DL_CADDY_ROOT=~/kaixuan/caddy       与 setup-caddy.sh 一致
#
# ## 安全性
#
# 1. 下载后先做三项校验（可解析 / 域名匹配 SAN / 未过期 / 私钥配对），
#    任一不过即丢弃并保留原证书；
# 2. 只有内容变化时才落盘 + reload（cmp 幂等）；
# 3. 私钥 0600，仅落在本机 pki 目录。
set -euo pipefail

DL_DOMAIN="${DL_DOMAIN:-llm.itestu.cn}"
CERT_SRC_HOST="${CERT_SRC_HOST:-root@115.29.212.252}"
CERT_SRC_PORT="${CERT_SRC_PORT:-25022}"
CERT_SRC_DIR="${CERT_SRC_DIR:-/etc/letsencrypt/live/${DL_DOMAIN}}"
DL_CADDY_ROOT="${DL_CADDY_ROOT:-$HOME/kaixuan/caddy}"
DL_CADDY_HOME="${DL_CADDY_HOME:-$HOME/sdk/caddy}"
DL_PKI_DIR="${DL_PKI_DIR:-$DL_CADDY_ROOT/pki}"
DL_LOG_DIR="${DL_LOG_DIR:-$DL_CADDY_ROOT/logs}"
DST_CRT="$DL_PKI_DIR/${DL_DOMAIN}.le.crt"
DST_KEY="$DL_PKI_DIR/${DL_DOMAIN}.le.key"

log() { printf '[sync-public-cert] %s\n' "$*"; }
die() { printf '[sync-public-cert] error: %s\n' "$*" >&2; exit 1; }

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
need_cmd ssh
need_cmd openssl

CHECK_ONLY=0
[[ "${1:-}" == "--check" ]] && CHECK_ONLY=1

mkdir -p "$DL_PKI_DIR" "$DL_LOG_DIR"
TMP_DIR=$(mktemp -d -t llm-cert-sync.XXXXXX)
trap 'rm -rf "$TMP_DIR"' EXIT

fetch() {
  ssh -p "$CERT_SRC_PORT" -o ConnectTimeout=10 -o BatchMode=yes \
    -o StrictHostKeyChecking=accept-new \
    "$CERT_SRC_HOST" "cat '$CERT_SRC_DIR/$1'" >"$TMP_DIR/$1" \
    || die "fetch $1 from ${CERT_SRC_HOST}:${CERT_SRC_DIR} failed"
}

log "pulling ${DL_DOMAIN} cert from ${CERT_SRC_HOST}:${CERT_SRC_PORT}${CERT_SRC_DIR}"
fetch fullchain.pem
fetch privkey.pem

# ── 校验：可解析 → SAN 匹配 → 未过期（≥7 天）→ 私钥配对 ──
openssl x509 -in "$TMP_DIR/fullchain.pem" -noout >/dev/null 2>&1 \
  || die "downloaded cert is not parseable"
# LibreSSL (macOS 自带) 不支持 -ext subjectAltName，统一走 -text 解析
openssl x509 -in "$TMP_DIR/fullchain.pem" -noout -text 2>/dev/null \
  | grep -A1 "Subject Alternative Name" | grep -q "DNS.*${DL_DOMAIN}" \
  || die "downloaded cert SAN does not cover ${DL_DOMAIN}"
openssl x509 -in "$TMP_DIR/fullchain.pem" -noout -checkend 604800 >/dev/null 2>&1 \
  || die "downloaded cert expires within 7 days — refusing to install (investigate certbot on ${CERT_SRC_HOST})"
cert_pub=$(openssl x509 -in "$TMP_DIR/fullchain.pem" -noout -pubkey 2>/dev/null | openssl sha256)
key_pub=$(openssl pkey -in "$TMP_DIR/privkey.pem" -pubout 2>/dev/null | openssl sha256)
[[ "$cert_pub" == "$key_pub" ]] || die "cert/key mismatch (public key hashes differ)"

if [[ "$CHECK_ONLY" == "1" ]]; then
  openssl x509 -in "$TMP_DIR/fullchain.pem" -noout -subject -issuer -dates
  log "check-only mode: nothing written"
  exit 0
fi

if [[ -f "$DST_CRT" && -f "$DST_KEY" ]] \
  && cmp -s "$TMP_DIR/fullchain.pem" "$DST_CRT" \
  && cmp -s "$TMP_DIR/privkey.pem" "$DST_KEY"; then
  log "cert unchanged; nothing to do"
  exit 0
fi

install -m 0644 "$TMP_DIR/fullchain.pem" "$DST_CRT"
install -m 0600 "$TMP_DIR/privkey.pem" "$DST_KEY"
log "installed $DST_CRT (expiry: $(openssl x509 -in "$DST_CRT" -noout -enddate | cut -d= -f2))"

if [[ -x "$DL_CADDY_HOME/caddy" ]] && pgrep -f "caddy run" >/dev/null 2>&1; then
  "$DL_CADDY_HOME/caddy" reload --config "$DL_CADDY_ROOT/Caddyfile" \
    && log "caddy reloaded" \
    || die "caddy reload failed — cert installed but runtime still serves the old one; fix Caddyfile (DL_PUBLIC_TLS=1 setup-caddy.sh) and restart"
else
  log "caddy not running (or binary missing) — cert installed; start caddy to serve it"
fi
