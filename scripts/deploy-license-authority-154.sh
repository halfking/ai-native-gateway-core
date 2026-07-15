#!/usr/bin/env bash
# Deploy license-authority to 154 and wire nginx /api/v1 → :8443
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

SSH_PORT="${LLM_GATEWAY_SSH_PORT:-25022}"
SSH_HOST="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"
SSH_KEY_FILE="${SSH_KEY_FILE:-}"
for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
  [[ -f "$k" ]] && SSH_KEY_FILE="$k" && break
done
SSH=(ssh -i "$SSH_KEY_FILE" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=25 -o ServerAliveInterval=10)
SCP=(scp -i "$SSH_KEY_FILE" -P "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)

BIN="/tmp/license-authority.linux.amd64"
REMOTE_DIR="/opt/license-authority"
ENV_FILE="/etc/license-authority/env"
NGINX_CONF="/etc/nginx/conf.d/llm-kxpms-cn.conf"
MARKER="# --- license-authority /api/v1 (managed) ---"

log() { echo "[deploy-la-154] $*"; }

log "编译 license-authority (linux/amd64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$BIN" ./cmd/license-authority
ls -lh "$BIN"

log "上传二进制与 systemd unit..."
"${SSH[@]}" "$SSH_HOST" "mkdir -p '$REMOTE_DIR' /etc/license-authority /var/lib/kx-gateway/license-authority"
"${SCP[@]}" "$BIN" "$SSH_HOST:$REMOTE_DIR/license-authority"
"${SCP[@]}" "$ROOT/deploy/systemd/license-authority.service" "$SSH_HOST:/etc/systemd/system/license-authority.service"
"${SSH[@]}" "$SSH_HOST" "chmod +x '$REMOTE_DIR/license-authority'"

log "写入 /etc/license-authority/env（首次生成密钥，后续保留）..."
"${SSH[@]}" "$SSH_HOST" "python3 - <<'PY'
import base64, os, secrets
from pathlib import Path

gw = Path('/etc/llm-gateway-go/env')
la = Path('/etc/license-authority/env')

def read_env(p):
    d = {}
    if p.exists():
        for ln in p.read_text().splitlines():
            if '=' in ln and not ln.strip().startswith('#'):
                k, v = ln.split('=', 1)
                d[k] = v
    return d

g = read_env(gw)
existing = read_env(la)

def need(k, gen=None):
    if existing.get(k):
        return existing[k]
    if g.get(k):
        return g[k]
    return gen() if gen else ''

db = g.get('LLM_GATEWAY_DATABASE_URL', '')
if not db:
    raise SystemExit('LLM_GATEWAY_DATABASE_URL missing in gateway env')

jwt = need('LICENSE_JWT_SECRET', lambda: g.get('LLM_GATEWAY_SECRET_KEY') or secrets.token_urlsafe(48))
if len(jwt) < 32:
    jwt = secrets.token_urlsafe(48)

admin = need('LICENSE_AUTHORITY_ADMIN_TOKEN', lambda: secrets.token_urlsafe(48))
if len(admin) < 32:
    admin = secrets.token_urlsafe(48)

aes = need('LICENSE_AES_KEY')
if not aes:
    aes = base64.b64encode(os.urandom(32)).decode().rstrip('=')

redis = need('LICENSE_AUTHORITY_REDIS_URL', lambda: g.get('LLM_GATEWAY_REDIS_URL', ''))

out = [
    '# /etc/license-authority/env — managed by deploy-license-authority-154.sh',
    'LICENSE_AUTHORITY_LISTEN=0.0.0.0:8443',
    'LICENSE_AUTHORITY_DATABASE_URL=' + db,
    'LICENSE_AUTHORITY_DATA_DIR=/var/lib/kx-gateway/license-authority',
    'LICENSE_AUTHORITY_LOG_LEVEL=info',
    'LICENSE_JWT_SECRET=' + jwt,
    'LICENSE_AUTHORITY_ADMIN_TOKEN=' + admin,
    'LICENSE_AES_KEY=' + aes,
]
if redis:
    out.append('LICENSE_AUTHORITY_REDIS_URL=' + redis)

la.parent.mkdir(parents=True, exist_ok=True)
la.write_text('\\n'.join(out) + '\\n')
os.chmod(str(la), 0o600)
print('env written keys:', len(out))
PY"

log "配置 nginx /api/v1 → license-authority..."
"${SSH[@]}" "$SSH_HOST" 'python3 - <<'"'"'PY'"'"'
from pathlib import Path
conf = Path("/etc/nginx/conf.d/llm-kxpms-cn.conf")
text = conf.read_text()
marker = "# --- license-authority /api/v1 (managed) ---"
upstream_snippet = """upstream license_authority {
    server 127.0.0.1:8443 max_fails=3 fail_timeout=10s;
    keepalive 8;
}
"""
location_block = marker + """
    location ^~ /api/v1/ {
        proxy_pass http://license_authority;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 30s;
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;
        proxy_buffering off;
    }
"""
if "upstream license_authority" not in text:
    anchor = "upstream llm_local {"
    idx = text.find(anchor)
    if idx < 0:
        raise SystemExit("upstream llm_local not found")
    end = text.find("}", idx)
    end = text.find("\n", end) + 1
    text = text[:end] + "\n" + upstream_snippet + text[end:]
if marker not in text:
    anchor = "    # Default: everything else"
    idx = text.find(anchor)
    if idx < 0:
        raise SystemExit("nginx anchor not found")
    text = text[:idx] + location_block + "\n\n" + text[idx:]
bak = conf.with_suffix(conf.suffix + ".bak.la-" + __import__("datetime").datetime.now().strftime("%Y%m%d-%H%M%S"))
bak.write_text(conf.read_text())
conf.write_text(text)
print("nginx patched, backup:", bak)
PY'

log "配置 252 nginx /api/v1 → 154:8443（公网 DNS 入口）..."
HOST_252="${LLM_GATEWAY_252_SSH:-root@115.29.212.252}"
"${SSH[@]}" "$HOST_252" 'python3 - <<'"'"'PY'"'"'
from pathlib import Path
conf = Path("/etc/nginx/conf.d/kxpms-on-252.conf")
text = conf.read_text()
marker = "# --- license-authority /api/v1 (managed) ---"
upstream = """
upstream kxpms_license_authority {
    server 172.16.2.209:8443 max_fails=3 fail_timeout=10s;
    keepalive 8;
}
"""
loc = marker + """
    location ^~ /api/v1/ {
        proxy_pass http://kxpms_license_authority;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 30s;
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;
        proxy_buffering off;
    }

"""
if "kxpms_license_authority" not in text:
    anchor = "upstream kxpms_llm_backend {"
    idx = text.find(anchor)
    end = text.find("}", idx)
    end = text.find("\n", end) + 1
    text = text[:end] + upstream + text[end:]
if marker not in text:
    anchor = "    location / {"
    idx = text.find(anchor, text.find("server_name llm.kxpms.cn"))
    if idx < 0:
        raise SystemExit("llm server block anchor not found")
    text = text[:idx] + loc + text[idx:]
bak = conf.with_suffix(conf.suffix + ".bak.la-" + __import__("datetime").datetime.now().strftime("%Y%m%d-%H%M%S"))
bak.write_text(conf.read_text())
conf.write_text(text)
print("252 nginx patched, backup:", bak)
PY'
"${SSH[@]}" "$HOST_252" "nginx -t && systemctl reload nginx"

log "启动 license-authority + reload nginx..."
"${SSH[@]}" "$SSH_HOST" "systemctl daemon-reload && systemctl enable license-authority.service && systemctl restart license-authority.service && sleep 3 && systemctl is-active license-authority.service && nginx -t && systemctl reload nginx"

log "冒烟验证..."
"${SSH[@]}" "$SSH_HOST" "curl -fsS http://127.0.0.1:8443/api/v1/healthz && echo && curl -sS -o /dev/null -w 'register=%{http_code}\\n' -X POST http://127.0.0.1:8443/api/v1/ops/nodes/register -H 'Content-Type: application/json' -d '{}'"

log "重启 gateway 以重试 ops reporter 注册..."
"${SSH[@]}" "$SSH_HOST" "systemctl restart llm-gateway-go.service && sleep 4 && journalctl -u llm-gateway-go --since '1 min ago' --no-pager | grep -iE 'ops reporter' | tail -5"

log "✅ license-authority 154 部署完成"
