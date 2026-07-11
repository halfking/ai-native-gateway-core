#!/bin/bash
# deploy-llm-kxpms-cert.sh — 在 252 上承接 llm.kxpms.cn 域名 + 证书
#
# 用途：
#   把 llm.kxpms.cn 的 SSL 终止从 154 迁到 252（NPS 所在服务器），并保持
#   154 上的证书为兜底，使得 DNS 可以随时在两台机器之间切换。
#
# 架构：
#   - 252 nginx (kxpms-on-252.conf) listen 9443, server_name llm.kxpms.cn,
#     证书路径：/etc/nps/conf/certs/hosts/llm.kxpms.cn/{fullchain.pem,privkey.pem}
#   - 154 certbot 自动续期 letsencrypt kxpms.cn 证书（含 llm.kxpms.cn SAN）
#   - 252 每日 03:17 cron 调用 sync-llm-kxpms-cert.sh 从 154 拉新证书
#   - DNS：当前指向 252 (115.29.212.252)，需要时可切回 154 (47.97.111.154)
#
# 用法：
#   ./deploy-llm-kxpms-cert.sh                # 全流程：建目录→拷证书→改 nginx→reload→验证
#   ./deploy-llm-kxpms-cert.sh --sync-only   # 只跑证书同步（修复证书过期/损坏）
#   ./deploy-llm-kxpms-cert.sh --nginx-only  # 只更新 nginx 配置（证书已就位）
#   ./deploy-llm-kxpms-cert.sh --rollback    # 回滚 nginx 到变更前
#
# 依赖：
#   - sshpass（SSHPASS 环境变量，或 ssh key）
#   - ssh / scp
#
# 重要：
#   - 154 侧的 nginx 配置不动；154 仍然在 :443 服务 llm.kxpms.cn，作为 DNS 切换兜底
#   - 252 侧 SNI 路由（stream.d/sni-proxy.conf）已有 llm.kxpms.cn → itestu_nginx_backend(:9443)
#   - 变更前会自动备份：/etc/nginx/conf.d/kxpms-on-252.conf.bak-pre-llm-cert-<ts>
#
# 关键经验 (2026-07-11 部署踩坑):
#   1. openssl pkey 没有 -modulus flag（EC 私钥），校验公钥要用 pubkey fingerprint
#   2. scp -o BatchMode=yes 默认要求 known_hosts，先 ssh-keyscan 一次
#   3. nginx 9443 监听器要求 proxy_protocol，从 stream.d 进来；9443 单独测试要带 proxy header
#   4. 同一 kxpms.cn cert 在 252 的 SAN 不含 llm.kxpms.cn，必须用 154 那张（含 SAN）

set -euo pipefail

# ── 配置 ────────────────────────────────────────────────────────────────
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
HOST_252="${HOST_252:-115.29.212.252}"
HOST_154="${HOST_154:-47.97.111.154}"
PORT="${PORT:-25022}"
USER_REMOTE="${USER_REMOTE:-root}"

CERT_DIR_252="/etc/nps/conf/certs/hosts/llm.kxpms.cn"
CERT_DIR_154="/etc/letsencrypt/live/kxpms.cn"
NGINX_CONF_252="/etc/nginx/conf.d/kxpms-on-252.conf"
SYNC_SCRIPT_252="/usr/local/bin/sync-llm-kxpms-cert.sh"
SSH_KEY_252="/root/.ssh/cert-sync-252-154"
KNOWN_HOSTS_252="/root/.ssh/known_hosts"
SYNC_LOG_252="/var/log/sync-llm-kxpms-cert.log"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

# ── 参数 ────────────────────────────────────────────────────────────────
SYNC_ONLY=0
NGINX_ONLY=0
ROLLBACK=0
while [[ $# -gt 0 ]]; do
  case $1 in
    --sync-only)  SYNC_ONLY=1; shift ;;
    --nginx-only) NGINX_ONLY=1; shift ;;
    --rollback)   ROLLBACK=1; shift ;;
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

# SSH 到远程（优先 SSHPASS 环境变量，否则用 ssh key）
ssh_remote() {
  local host="$1"; shift
  if [[ -n "${SSHPASS:-}" ]]; then
    sshpass -e ssh \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -p "$PORT" "${USER_REMOTE}@${host}" "$@"
  else
    ssh \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -p "$PORT" "${USER_REMOTE}@${host}" "$@"
  fi
}

scp_remote_to_local() {
  local host="$1"; shift
  local src="$1"; shift
  local dst="$1"; shift
  if [[ -n "${SSHPASS:-}" ]]; then
    sshpass -e scp \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -P "$PORT" "${USER_REMOTE}@${host}:${src}" "$dst"
  else
    scp \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -P "$PORT" "${USER_REMOTE}@${host}:${src}" "$dst"
  fi
}

# ── 步骤 ────────────────────────────────────────────────────────────────

rollback_nginx() {
  log "查找最近的备份..."
  local bak
  bak=$(ssh_remote "$HOST_252" "ls -t /etc/nginx/conf.d/kxpms-on-252.conf.bak-pre-llm-cert-* 2>/dev/null | head -1")
  if [[ -z "$bak" ]]; then
    err "找不到备份，无法回滚"
    return 1
  fi
  log "回滚到 $bak"
  ssh_remote "$HOST_252" "cp '$bak' '$NGINX_CONF_252' && nginx -t && nginx -s reload"
  ok "nginx 已回滚"
}

if [[ $ROLLBACK -eq 1 ]]; then
  rollback_nginx
  exit 0
fi

step_check_precondition() {
  log "前置检查: 154 端口可达 + 252 端口可达"
  ssh_remote "$HOST_154" "echo '154 OK'"
  ssh_remote "$HOST_252" "echo '252 OK'"
  ok "两台机器 SSH 可达"
}

step_install_cert_dir() {
  log "[1/5] 在 252 创建 NPS 证书目录"
  ssh_remote "$HOST_252" "mkdir -p '$CERT_DIR_252' && ls -ld '$CERT_DIR_252'"
  ok "目录已就绪: $CERT_DIR_252"
}

step_sync_cert() {
  log "[2/5] 从 154 拉证书到本地临时目录"
  local tmp; tmp=$(mktemp -d)
  trap "rm -rf $tmp" EXIT
  scp_remote_to_local "$HOST_154" "$CERT_DIR_154/fullchain.pem" "$tmp/fullchain.pem"
  scp_remote_to_local "$HOST_154" "$CERT_DIR_154/privkey.pem"   "$tmp/privkey.pem"

  log "校验证书/私钥配对 (pubkey fingerprint)"
  local fc; fc=$(openssl x509 -in "$tmp/fullchain.pem" -noout -pubkey 2>/dev/null | openssl md5 | awk '{print $NF}')
  local fk; fk=$(openssl pkey -in "$tmp/privkey.pem" -pubout 2>/dev/null | openssl md5 | awk '{print $NF}')
  [[ "$fc" == "$fk" ]] || { err "公钥指纹不一致: cert=$fc key=$fk"; return 1; }
  ok "公钥指纹匹配: $fc"

  log "校验 SAN 含 llm.kxpms.cn"
  if ! openssl x509 -in "$tmp/fullchain.pem" -noout -text 2>/dev/null | grep -q "DNS:llm.kxpms.cn"; then
    err "证书 SAN 不含 llm.kxpms.cn — 拉错证书了？"
    return 1
  fi
  ok "SAN 校验通过"

  log "推到 252 的 NPS 证书目录"
  if [[ -n "${SSHPASS:-}" ]]; then
    sshpass -e scp \
      -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -P "$PORT" "$tmp/fullchain.pem" "${USER_REMOTE}@${HOST_252}:$CERT_DIR_252/fullchain.pem"
    sshpass -e scp \
      -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -P "$PORT" "$tmp/privkey.pem"   "${USER_REMOTE}@${HOST_252}:$CERT_DIR_252/privkey.pem"
  else
    scp \
      -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -P "$PORT" "$tmp/fullchain.pem" "${USER_REMOTE}@${HOST_252}:$CERT_DIR_252/fullchain.pem"
    scp \
      -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -P "$PORT" "$tmp/privkey.pem"   "${USER_REMOTE}@${HOST_252}:$CERT_DIR_252/privkey.pem"
  fi
  ssh_remote "$HOST_252" "chmod 0600 '$CERT_DIR_252'/*.pem && ls -la '$CERT_DIR_252'"
  ok "证书已落地: $CERT_DIR_252"
}

step_update_nginx() {
  log "[3/5] 修改 252 nginx kxpms-on-252.conf 的 llm.kxpms.cn 块"
  local remote_py
  remote_py=$(cat <<'PYEOF'
import sys
path = "/etc/nginx/conf.d/kxpms-on-252.conf"
with open(path, "r") as f:
    content = f.read()

old = """server {
    include /etc/nginx/conf.d/00-security-hardening.snippet;
    autoindex off;
    listen 9443 ssl http2 proxy_protocol;
    set_real_ip_from 127.0.0.1;
    real_ip_header proxy_protocol;
    server_name llm.kxpms.cn;

    ssl_certificate     /etc/letsencrypt/live/kxpms.cn/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/kxpms.cn/privkey.pem;"""

new = """server {
    include /etc/nginx/conf.d/00-security-hardening.snippet;
    autoindex off;
    listen 9443 ssl http2 proxy_protocol;
    set_real_ip_from 127.0.0.1;
    real_ip_header proxy_protocol;
    server_name llm.kxpms.cn;

    # Cert sourced from 154 (letsencrypt kxpms.cn SAN includes llm.kxpms.cn).
    # Copied to /etc/nps/conf/certs/hosts/llm.kxpms.cn/ — NPS owns renewal flow.
    # 154 retains a copy so DNS can be flipped back to 154 anytime.
    ssl_certificate     /etc/nps/conf/certs/hosts/llm.kxpms.cn/fullchain.pem;
    ssl_certificate_key /etc/nps/conf/certs/hosts/llm.kxpms.cn/privkey.pem;"""

if old not in content:
    print("ERROR: llm.kxpms.cn block not found - abort (file may have changed)")
    sys.exit(1)
content = content.replace(old, new)

old_note = """# Note: llm.kxpms.cn uses shared kxpms.cn cert (currently no llm.kxpms.cn SAN)
#       Once DNS for llm.kxpms.cn → 252, run:
#         certbot certonly --nginx -d kxpms.cn -d llm.kxpms.cn --expand"""

new_note = """# Cert topology (2026-07-11):
#   - llm.kxpms.cn: cert copied from 154 → /etc/nps/conf/certs/hosts/llm.kxpms.cn/
#                   (letsencrypt cert with llm.kxpms.cn SAN).
#                   NPS owns renewal of this host cert.
#                   Both 154 and 252 can serve llm.kxpms.cn with valid cert → flip DNS at will.
#   - other *.kxpms.cn: shared /etc/letsencrypt/live/kxpms.cn cert (no llm.kxpms.cn SAN)."""

if old_note in content:
    content = content.replace(old_note, new_note)

with open(path, "w") as f:
    f.write(content)
print("OK")
PYEOF
)
  ssh_remote "$HOST_252" "cp '$NGINX_CONF_252' '$NGINX_CONF_252.bak-pre-llm-cert-\$(date +%Y%m%d-%H%M%S)' && python3 -c '$remote_py'"
  ok "nginx 配置已更新"
}

step_install_sync_script() {
  log "[4/5] 部署 sync-llm-kxpms-cert.sh 到 252 + 设置 cron"
  local sync_body
  sync_body=$(cat "$REPO_DIR/scripts/sync-llm-kxpms-cert.sh")
  ssh_remote "$HOST_252" "cat > '$SYNC_SCRIPT_252' <<'EOF_REMOTE_SCRIPT'
$sync_body
EOF_REMOTE_SCRIPT
chmod +x '$SYNC_SCRIPT_252'"
  # Pre-populate known_hosts + generate ssh key if missing
  ssh_remote "$HOST_252" "
if [[ ! -f '$SSH_KEY_252' ]]; then
  ssh-keygen -t ed25519 -f '$SSH_KEY_252' -N '' -C 'cert-sync-252-154' >/dev/null
  echo 'GENERATED_KEY'
fi
ssh-keyscan -p $PORT $HOST_154 >> '$KNOWN_HOSTS_252' 2>/dev/null
chmod 0600 '$SSH_KEY_252' '$KNOWN_HOSTS_252'
"
  # Install pub key on 154
  local pub; pub=$(ssh_remote "$HOST_252" "cat '$SSH_KEY_252.pub'")
  ssh_remote "$HOST_154" "
cp -a /root/.ssh/authorized_keys /root/.ssh/authorized_keys.bak-pre-252-sync-\$(date +%Y%m%d-%H%M%S) 2>/dev/null || true
grep -q 'cert-sync-252-154' /root/.ssh/authorized_keys 2>/dev/null || {
  echo '# cert-sync-252-154 - syncs llm.kxpms.cn cert to 252 NPS dir' >> /root/.ssh/authorized_keys
  echo '$pub' >> /root/.ssh/authorized_keys
  echo 'PUBKEY_INSTALLED'
}
"
  # Add cron
  ssh_remote "$HOST_252" "(crontab -l 2>/dev/null | grep -v sync-llm-kxpms-cert.sh; echo '17 3 * * * $SYNC_SCRIPT_252 >> $SYNC_LOG_252 2>&1') | crontab -"
  ok "sync 脚本 + cron 已部署 (每天 03:17)"
}

step_verify() {
  log "[5/5] 验证 nginx reload + 双端 HTTPS 联通"
  ssh_remote "$HOST_252" "nginx -t && nginx -s reload"
  ok "nginx reload 成功"

  log "验证 252 直连..."
  local out_252
  out_252=$(curl -sS --resolve "llm.kxpms.cn:443:$HOST_252" --connect-timeout 8 --max-time 15 \
              -H "Authorization: Bearer sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9" \
              https://llm.kxpms.cn/v1/models)
  echo "$out_252" | head -c 200
  echo "..."

  log "验证 154 直连 (DNS 切换兜底)..."
  local out_154
  out_154=$(curl -sS --resolve "llm.kxpms.cn:443:$HOST_154" --connect-timeout 8 --max-time 15 \
              -H "Authorization: Bearer sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9" \
              https://llm.kxpms.cn/v1/models)
  echo "$out_154" | head -c 200
  echo "..."

  if [[ ${#out_252} -gt 100 && ${#out_154} -gt 100 ]]; then
    ok "252 + 154 双端均返回 models 列表 — DNS 可随时切换"
  else
    err "验证失败: 252=${#out_252}b 154=${#out_154}b"
    return 1
  fi
}

# ── 主流程 ──────────────────────────────────────────────────────────────

if [[ $NGINX_ONLY -eq 1 ]]; then
  step_check_precondition
  step_update_nginx
  step_verify
elif [[ $SYNC_ONLY -eq 1 ]]; then
  step_check_precondition
  step_sync_cert
  ssh_remote "$HOST_252" "$SYNC_SCRIPT_252" 2>&1 | tail -5
else
  step_check_precondition
  step_install_cert_dir
  step_sync_cert
  step_update_nginx
  step_install_sync_script
  step_verify
fi

echo
ok "==== 部署完成 ===="
echo "  • 证书位置:   252:$CERT_DIR_252"
echo "  • 同步脚本:   252:$SYNC_SCRIPT_252 (cron 03:17 daily)"
echo "  • nginx 备份: 252:$NGINX_CONF_252.bak-pre-llm-cert-*"
echo "  • 当前 DNS:   请确认 llm.kxpms.cn → $HOST_252（切回时改 $HOST_154）"