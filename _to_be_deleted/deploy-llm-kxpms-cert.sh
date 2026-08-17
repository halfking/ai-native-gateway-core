#!/bin/bash
# deploy-llm-kxpms-cert.sh — 在 252 上承接 llm.kxpms.cn 域名 + 证书 + 自动续期
#
# 用途：
#   把 llm.kxpms.cn 的 SSL 终止迁到 252（NPS 所在服务器），并通过 certbot
#   在 252 上自动续期，154 仅作 DNS-flip 兜底。
#
# 架构（部署后）:
#   - 252 是主 ingress (DNS 当前 → 115.29.212.252)，nginx kxpms-on-252.conf
#     listen 9443 server_name llm.kxpms.cn，证书读 certbot 路径：
#       /etc/letsencrypt/live/kxpms.cn/{fullchain.pem, privkey.pem}
#   - 252 :80 server block 含 ACME challenge handler (location /.well-known/
#     acme-challenge/ { root /var/www/certbot; }) — 与 *.itestu.cn 既有
#     certbot-managed 范式一致，proven pattern。
#   - certbot-renew.timer (systemd) 每 12h 跑一次 certbot renew --noninteractive
#     自动续期所有受管证书（含 kxpms.cn）。renew_before_expiry = 30 days。
#   - certbot deploy hook /etc/letsencrypt/renewal-hooks/deploy/sync-to-154.sh
#     每次续期成功后自动 scp 新证书到 154、reload 两边 nginx。
#   - 154 是 DNS-flip 兜底：原 cert + nginx 配置不动（仅依赖 deploy hook 同步
#     新 cert 到 /etc/letsencrypt/live/kxpms.cn/）。
#
# 用法：
#   ./deploy-llm-kxpms-cert.sh               # 全流程（含 certbot 部署）
#   ./deploy-llm-kxpms-cert.sh --sync-only  # 只做"sync-to-154"动作（修复 154 证书丢失）
#   ./deploy-llm-kxpms-cert.sh --nginx-only # 只改 nginx 配置（证书已就位）
#   ./deploy-llm-kxpms-cert.sh --rollback   # 回滚 252 nginx 到变更前
#   ./deploy-llm-kxpms-cert.sh --renew      # 强制跑一次 certbot renew（实战用得少）
#
# 依赖：
#   - sshpass（SSHPASS 环境变量，或 ssh key）
#   - ssh / scp
#   - 252 已安装 certbot 1.22.0（含 webroot authenticator）
#   - 252 的 certbot 账户 (LE 注册) 已存在（默认沿用 /etc/letsencrypt/accounts/）
#
# 重要：
#   - 若 252 /etc/letsencrypt/live/kxpms.cn 已存在（含上一次手动同步过来的
#     fullchain.pem），certbot 会拒绝覆写。本脚本会先备份并清理冲突。
#   - 变更前自动备份 nginx 配置: .bak-pre-llm-cert-<ts>
#
# 关键经验 (2026-07-12 部署踩坑):
#   1. certbot 默认 webroot_map 在 renew --force-renewal 时不会自动写；
#      第一次 certonly 后要在 /etc/letsencrypt/renewal/kxpms.cn.conf 手动加
#        [[webroot_map]]
#        kxpms.cn = /var/www/certbot
#        llm.kxpms.cn = /var/www/certbot
#      否则报 "renewal config file {} is missing a required file reference"
#   2. 252 nginx :80 显式 server { server_name llm.kxpms.cn; } 块内
#      location /.well-known/acme-challenge/ 必须放在 location / { return 301 } 之前。
#      这次用本机 webroot 模式（proven pattern），不跨服务器 proxy。
#   3. openssl pkey 没 -modulus（EC key），校验公钥配对要用
#        openssl x509 -noout -pubkey | openssl md5
#        openssl pkey -pubout | openssl md5
#   4. scp 默认 BatchMode=yes 拒未知 host，先 ssh-keyscan。
#   5. nginx 9443 是 proxy_protocol，单独 test 要带 PROXY header；正常从 stream.d 进。
#   6. certbot 续期后必须 reload 两边 nginx。cert 写盘 ≠ nginx 已加载。

set -euo pipefail

# ── 配置 ────────────────────────────────────────────────────────────────
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
HOST_252="${HOST_252:-115.29.212.252}"
HOST_154="${HOST_154:-47.97.111.154}"
PORT="${PORT:-25022}"
USER_REMOTE="${USER_REMOTE:-root}"

NGINX_CONF_252="/etc/nginx/conf.d/kxpms-on-252.conf"
SYNC_HOOK_252="/etc/letsencrypt/renewal-hooks/deploy/sync-to-154.sh"
SSH_KEY_252="/root/.ssh/cert-sync-252-154"
KNOWN_HOSTS_252="/root/.ssh/known_hosts"
CERT_WEBROOT_252="/var/www/certbot"
CERT_RENEWAL_252="/etc/letsencrypt/renewal/kxpms.cn.conf"
CERT_LIVE_DIR_252="/etc/letsencrypt/live/kxpms.cn"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

# ── 参数 ────────────────────────────────────────────────────────────────
SYNC_ONLY=0
NGINX_ONLY=0
ROLLBACK=0
RENEW_ONLY=0
while [[ $# -gt 0 ]]; do
  case $1 in
    --sync-only)  SYNC_ONLY=1; shift ;;
    --nginx-only) NGINX_ONLY=1; shift ;;
    --rollback)   ROLLBACK=1; shift ;;
    --renew)      RENEW_ONLY=1; shift ;;
    -h|--help)
      grep -E "^# (用途|用法|依赖|重要|关键经验)" "$0" | sed 's/^# //'
      exit 0 ;;
    *) echo "unknown arg: $1"; exit 2 ;;
  esac
done

# ── 辅助 ────────────────────────────────────────────────────────────────
log()  { printf "${BLUE}[%s]${NC} %s\n" "$(date +%H:%M:%S)" "$*"; }
ok()   { printf "${GREEN}[%s] OK${NC}  %s\n" "$(date +%H:%M:%S)" "$*"; }
warn() { printf "${YELLOW}[%s] WARN${NC} %s\n" "$(date +%H:%M:%S)" "$*"; }
err()  { printf "${RED}[%s] ERR${NC} %s\n" "$(date +%H:%M:%S)" "$*" >&2; }

ssh_252() { ssh -p "$PORT" "${USER_REMOTE}@${HOST_252}" "$@"; }
ssh_154() { ssh -p "$PORT" "${USER_REMOTE}@${HOST_154}" "$@"; }
scp_to_252() { scp -P "$PORT" "$1" "${USER_REMOTE}@${HOST_252}:$2"; }
scp_to_154() { scp -P "$PORT" "$1" "${USER_REMOTE}@${HOST_154}:$2"; }

# ── 主逻辑 ──────────────────────────────────────────────────────────────

rollback_nginx() {
  log "查找最近的备份..."
  local bak
  bak=$(ssh_252 "ls -t /etc/nginx/conf.d/kxpms-on-252.conf.bak-pre-llm-cert-* 2>/dev/null | head -1")
  if [[ -z "$bak" ]]; then err "找不到备份"; return 1; fi
  log "回滚到 $bak"
  ssh_252 "cp '$bak' '$NGINX_CONF_252' && nginx -t && nginx -s reload"
  ok "nginx 已回滚"
}
[[ $ROLLBACK -eq 1 ]] && { rollback_nginx; exit 0; }

step_precondition() {
  log "前置检查"
  ssh_252 "echo '252 OK'"
  ssh_154 "echo '154 OK'"
  ok "两台机器可达"
}

step_install_ssh_key() {
  log "[1/8] 252 配置专用 SSH key + 154 装公钥"
  ssh_252 "
if [[ ! -f '$SSH_KEY_252' ]]; then
  ssh-keygen -t ed25519 -f '$SSH_KEY_252' -N '' -C 'cert-sync-252-154@20260711' >/dev/null
  echo 'GENERATED_KEY'
fi
ssh-keyscan -p $PORT $HOST_154 >> '$KNOWN_HOSTS_252' 2>/dev/null
chmod 0600 '$SSH_KEY_252' '$KNOWN_HOSTS_252'"
  local pub
  pub=$(ssh_252 "cat '$SSH_KEY_252.pub'")
  ssh_154 "
cp -a /root/.ssh/authorized_keys /root/.ssh/authorized_keys.bak-pre-252-sync-\$(date +%Y%m%d-%H%M%S) 2>/dev/null || true
grep -q 'cert-sync-252-154' /root/.ssh/authorized_keys 2>/dev/null || {
  echo '# cert-sync-252-154 - syncs kxpms.cn cert to 154 after renew' >> /root/.ssh/authorized_keys
  echo '$pub' >> /root/.ssh/authorized_keys
  echo 'PUBKEY_INSTALLED'
}"
  ok "SSH key 已就绪（双向）"
}

step_update_nginx_252() {
  log "[2/8] 更新 252 nginx kxpms-on-252.conf"
  ssh_252 "cp '$NGINX_CONF_252' '$NGINX_CONF_252.bak-pre-llm-cert-\$(date +%Y%m%d-%H%M%S)'"
  local py
  py=$(cat <<'PYEOF'
import sys
path = "/etc/nginx/conf.d/kxpms-on-252.conf"
with open(path, "r") as f:
    content = f.read()

# 1) llm.kxpms.cn server block — switch cert to certbot-managed path
old = """server {
    include /etc/nginx/conf.d/00-security-hardening.snippet;
    autoindex off;
    listen 9443 ssl http2 proxy_protocol;
    set_real_ip_from 127.0.0.1;
    real_ip_header proxy_protocol;
    server_name llm.kxpms.cn;

    # Cert sourced from 154 (letsencrypt kxpms.cn SAN includes llm.kxpms.cn)
    # Copied 2026-07-11 to /etc/nps/conf/certs/hosts/llm.kxpms.cn/ so NPS can manage renewal.
    # 154 retains a copy so DNS can be flipped back to 154 anytime.
    ssl_certificate     /etc/nps/conf/certs/hosts/llm.kxpms.cn/fullchain.pem;
    ssl_certificate_key /etc/nps/conf/certs/hosts/llm.kxpms.cn/privkey.pem;"""

new = """server {
    include /etc/nginx/conf.d/00-security-hardening.snippet;
    autoindex off;
    listen 9443 ssl http2 proxy_protocol;
    set_real_ip_from 127.0.0.1;
    real_ip_header proxy_protocol;
    server_name llm.kxpms.cn;

    # Cert managed by certbot on 252 at /etc/letsencrypt/live/kxpms.cn/
    # (auto-renewed via certbot-renew.timer; SAN includes llm.kxpms.cn).
    # 154 keeps its own mirror via /etc/letsencrypt/renewal-hooks/deploy/sync-to-154.sh.
    ssl_certificate     /etc/letsencrypt/live/kxpms.cn/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/kxpms.cn/privkey.pem;"""

if old in content:
    content = content.replace(old, new)
    print("llm.kxpms.cn block: switched to certbot path")
elif "/etc/letsencrypt/live/kxpms.cn/fullchain.pem" in content and "llm.kxpms.cn" in content.split("ssl_certificate     /etc/letsencrypt/live/kxpms.cn/fullchain.pem;")[0][-1500:]:
    print("SKIP: llm.kxpms.cn already uses certbot path")
else:
    print("WARN: llm.kxpms.cn block not in expected form - inspect manually")

with open(path, "w") as f:
    f.write(content)
PYEOF
  )
  ssh_252 "python3 -c '$py'"
  ssh_252 "nginx -t && nginx -s reload"
  ok "nginx 配置已更新（certbot path）"
}

step_install_sync_hook() {
  log "[3/8] 部署 sync-to-154 deploy hook 到 252"
  local hook_body
  hook_body=$(cat "$REPO_DIR/scripts/sync-to-154.sh")
  ssh_252 "cat > '$SYNC_HOOK_252' <<'HOOKEOF'
$hook_body
HOOKEOF
chmod +x '$SYNC_HOOK_252'"
  ok "deploy hook 落地: $SYNC_HOOK_252"
}

step_setup_certbot() {
  log "[4/8] 配置 252 certbot 账户 + kxpms.cn 续期"
  # 1. 清理旧 live 目录（如果有手动同步的旧 fullchain 会冲突）
  ssh_252 "
if [[ -d '$CERT_LIVE_DIR_252' ]]; then
  cp -a '$CERT_LIVE_DIR_252' '$CERT_LIVE_DIR_252.bak-pre-certbot-\$(date +%Y%m%d-%H%M%S)'
fi
rm -rf '$CERT_LIVE_DIR_252' /etc/letsencrypt/renewal/kxpms.cn.conf /etc/letsencrypt/archive/kxpms.cn"
  # 2. 注册证书（首跑 certbot）
  ssh_252 "certbot certonly --webroot -w '$CERT_WEBROOT_252' --cert-name kxpms.cn -d kxpms.cn -d llm.kxpms.cn" 2>&1 | tail -5
  # 3. 注入 webroot_map
  ssh_252 "
python3 -c '
import re
path = \"/etc/letsencrypt/renewal/kxpms.cn.conf\"
with open(path, \"r\") as f:
    content = f.read()
old = \"[[webroot_map]]\\n\"
new = \"[[webroot_map]]\\nkxpms.cn = /var/www/certbot\\nllm.kxpms.cn = /var/www/certbot\\n\"
if old in content and \"kxpms.cn = /var/www/certbot\" not in content:
    content = content.replace(old, new, 1)
    with open(path, \"w\") as f:
        f.write(content)
    print(\"webroot_map entries added\")
else:
    print(\"webroot_map already has entries\")
'"
  ok "certbot renewal config 就绪"
}

step_enable_timer() {
  log "[5/8] 启用 certbot-renew.timer"
  ssh_252 "
systemctl is-enabled certbot-renew.timer >/dev/null 2>&1 || systemctl enable certbot-renew.timer
systemctl restart certbot-renew.timer
systemctl is-active certbot-renew.timer && echo TIMER_ACTIVE"
  ok "certbot-renew.timer active"
}

step_remove_legacy_cron() {
  log "[6/8] 移除旧的 154→252 daily cron（已由 certbot 自动续期 + deploy hook 取代）"
  ssh_252 "
(crontab -l 2>/dev/null | grep -v sync-llm-kxpms-cert.sh) | crontab -
# Also cleanup old sync script + backup files
rm -f /usr/local/bin/sync-llm-kxpms-cert.sh /var/log/sync-llm-kxpms-cert.log 2>&1
rm -rf /etc/nps/conf/certs/hosts/llm.kxpms.cn 2>&1
echo OLD_CRON_REMOVED"
  ok "legacy sync 已退役"
}

step_initial_sync() {
  log "[7/8] 首次同步证书到 154（把 certbot 在 252 跑出来的 cert 推到 154）"
  ssh_252 "$SYNC_HOOK_252" 2>&1 | tail -5
  ok "首 sync 完成"
}

step_verify() {
  log "[8/8] 验证"
  curl -sS --resolve "llm.kxpms.cn:443:$HOST_252" --connect-timeout 8 --max-time 20 \
       -o /dev/null -w "  252 HTTPS: %{http_code} cert=%{ssl_verify_result}\n" \
       https://llm.kxpms.cn/v1/models
  curl -sS --resolve "llm.kxpms.cn:443:$HOST_154" --connect-timeout 8 --max-time 20 \
       -o /dev/null -w "  154 HTTPS: %{http_code} cert=%{ssl_verify_result}\n" \
       https://llm.kxpms.cn/v1/models
  ok "双端 HTTPS 均验证通过"
  echo
  log "Certbot 状态："
  ssh_252 "certbot certificates 2>/dev/null | grep -A 6 kxpms.cn | head -8" 2>&1
  log "Timer 状态："
  ssh_252 "systemctl list-timers certbot-renew.timer 2>&1 | tail -3" 2>&1
}

step_renew_now() {
  log "强制立即续期以验证 deploy hook chain"
  ssh_252 "certbot renew --force-renewal --cert-name kxpms.cn 2>&1 | tail -10" 2>&1
  log "deploy hook log:"
  ssh_252 "tail -10 /var/log/sync-to-154.log 2>&1" 2>&1
}

# ── 主流程 ──────────────────────────────────────────────────────────────

if [[ $NGINX_ONLY -eq 1 ]]; then
  step_precondition
  step_update_nginx_252
  step_verify
elif [[ $SYNC_ONLY -eq 1 ]]; then
  step_precondition
  step_initial_sync
elif [[ $RENEW_ONLY -eq 1 ]]; then
  step_precondition
  step_renew_now
else
  step_precondition
  step_install_ssh_key
  step_update_nginx_252
  step_install_sync_hook
  step_setup_certbot
  step_enable_timer
  step_remove_legacy_cron
  step_initial_sync
  step_verify
fi

echo
ok "==== 部署完成 ===="
echo "  • cert (auto-renewed): 252:$CERT_LIVE_DIR_252"
echo "  • certbot renewal config: $CERT_RENEWAL_252"
echo "  • deploy hook: $SYNC_HOOK_252"
echo "  • timer: certbot-renew.timer (systemd, 12h cadence)"
echo "  • nginx: 252:$NGINX_CONF_252  154:/etc/nginx/conf.d/llm-kxpms-cn.conf (untouched)"
echo "  • DNS: $(dig +short llm.kxpms.cn @223.5.5.5 2>/dev/null)  <- 切回时改 $HOST_154"