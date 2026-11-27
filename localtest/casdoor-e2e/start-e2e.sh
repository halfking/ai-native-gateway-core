#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# casdoor-e2e 本地联调一键启动（方案 B：全本地隔离）
#
# 产出：
#   1. runtime/ 密钥（casdoor RSA4096 JWT 证书 + 各密码/secret，gitignore 保护）
#   2. casdoor/init_data.json（org=kaixuan-e2e / app=llm-gateway-e2e / user=gwuser，
#      password grant 勾选；证书块内嵌公私钥）
#   3. casdoor/app.conf（从模板渲染）
#   4. gateway.env（本地网关的 CASDOOR_* + JWT 链 env，网关直接 source）
#   5. docker compose 起 PG×2 + casdoor
#
# 用法：
#   ./start-e2e.sh            # 生成密钥与配置并起栈（幂等：已有 runtime 则复用）
#   ./start-e2e.sh --regen    # 重新生成全部密钥与配置（栈不停，需 migrate + 重起 casdoor）
# ─────────────────────────────────────────────────────────────────────
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
RUNTIME="$SCRIPT_DIR/runtime"
mkdir -p "$RUNTIME" casdoor/logs

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }
err()  { echo -e "${RED}✗ $*${NC}" >&2; }

REGEN=0
[ "${1:-}" = "--regen" ] && REGEN=1

# ── 1. 密钥与密码 ──
if [ ! -f "$RUNTIME/jwt_public.pem" ] || [ "$REGEN" = "1" ]; then
  info "生成 casdoor JWT RSA-4096 证书..."
  openssl genrsa -out "$RUNTIME/jwt_private.pem" 4096 2>/dev/null
  openssl rsa -in "$RUNTIME/jwt_private.pem" -pubout -out "$RUNTIME/jwt_public.pem" 2>/dev/null
  chmod 600 "$RUNTIME/jwt_private.pem"
  ok "证书已生成: runtime/jwt_{public,private}.pem"
else
  ok "证书已存在（复用）: runtime/jwt_{public,private}.pem"
fi

ensure_secret() { # $1=file $2=length(bytes)
  local f="$1" n="${2:-24}"
  if [ ! -s "$f" ] || [ "$REGEN" = "1" ]; then
    openssl rand -hex "$n" | tr -d '\n' > "$f"; chmod 600 "$f"
  fi
}
ensure_secret "$RUNTIME/pg_password" 16
ensure_secret "$RUNTIME/casdoor_db_password" 16
ensure_secret "$RUNTIME/casdoor_client_secret" 24
ensure_secret "$RUNTIME/casdoor_user_password" 12
ensure_secret "$RUNTIME/admin_password" 12
ensure_secret "$RUNTIME/jwt_secret" 32
ensure_secret "$RUNTIME/cek" 16
ok "密码/secret 就绪（runtime/*.hex 文件）"

PG_PASSWORD=$(cat "$RUNTIME/pg_password")
CASDOOR_DB_PASSWORD=$(cat "$RUNTIME/casdoor_db_password")
CLIENT_SECRET=$(cat "$RUNTIME/casdoor_client_secret")
USER_PASSWORD=$(cat "$RUNTIME/casdoor_user_password")
ADMIN_PASSWORD=$(cat "$RUNTIME/admin_password")
JWT_SECRET=$(cat "$RUNTIME/jwt_secret")
CEK=$(cat "$RUNTIME/cek")

# ── 2. init_data.json（bcrypt 用户密码 + 证书块内嵌 PEM）──
info "生成 casdoor/init_data.json..."
python3 - "$RUNTIME" "$USER_PASSWORD" <<'PYEOF'
import json, sys

runtime, user_pw = sys.argv[1], sys.argv[2]
pub = open(f"{runtime}/jwt_public.pem").read().strip()
priv = open(f"{runtime}/jwt_private.pem").read().strip()

init_data = {
    "organizations": [{
        "owner": "admin", "name": "kaixuan-e2e", "displayName": "Kaixuan E2E",
        "websiteUrl": "http://127.0.0.1:18000", "favicon": "", "logo": "", "logoDark": "",
        "defaultAvatar": "https://cdn.casdoor.com/avatar/default-avatar.png",
        "passwordType": "bcrypt", "passwordSalt": "", "passwordOptions": [],
        "defaultPassword": "", "masterPassword": "", "initScore": 0,
        "disableSignin": False, "enableSoftDeletion": False, "isProfilePublic": True,
        "defaultApplication": "", "defaultLanguage": "", "languages": ["en", "zh"],
        "countryCodes": ["CN", "US", "SG"],
        "accountItems": [
            {"name": "Organization", "visible": True, "viewRule": "Public", "modifyRule": "Admin"},
            {"name": "Display name", "visible": True, "viewRule": "Public", "modifyRule": "Self"},
            {"name": "Email", "visible": True, "viewRule": "Public", "modifyRule": "Self"},
        ],
        "tags": [], "themeData": None,
    }],
    "applications": [{
        "owner": "admin", "name": "llm-gateway-e2e", "displayName": "LLM Gateway E2E",
        "description": "Local e2e test application for llm-gateway-go casdoor mode",
        "organization": "kaixuan-e2e", "cert": "e2e-cert",
        "clientId": "e2e-client-id", "clientSecret": open(f"{runtime}/casdoor_client_secret").read().strip(),
        "enablePassword": True, "enableSignUp": False, "enableSigninSession": False,
        "enableCodeSignin": False, "enableWebAuthn": False, "enableAutoSignin": False,
        "disableSignin": False,
        "grantTypes": ["authorization_code", "password", "client_credentials", "token", "id_token", "refresh_token"],
        "redirectUris": ["http://127.0.0.1:18781/callback"],
        "homepageUrl": "http://127.0.0.1:18781",
        "expireInHours": 168, "failedSigninLimit": 12, "failedSigninFrozenTime": 5,
        "signinMethods": [
            {"name": "Password", "displayName": "Password", "rule": "All"},
        ],
        "signupItems": [
            {"name": "ID", "visible": True, "required": True, "prompted": False, "rule": "Random"},
            {"name": "Username", "visible": True, "required": True, "prompted": False, "rule": "None"},
            {"name": "Display name", "visible": True, "required": True, "prompted": False, "rule": "None"},
            {"name": "Password", "visible": True, "required": True, "prompted": False, "rule": "None"},
            {"name": "Email", "visible": True, "required": False, "prompted": False, "rule": "Normal"},
        ],
        "providers": [], "tokenFields": [], "tokenFormat": "", "favicon": "", "logo": "",
        "footerHtml": "", "themeData": None, "title": "",
    }],
    "users": [{
        "owner": "kaixuan-e2e", "name": "gwuser", "type": "normal-user",
        "displayName": "E2E Gateway User", "password": user_pw,
        "email": "gwuser@e2e.local", "phone": "", "countryCode": "",
        "address": [], "addresses": [], "affiliation": "", "avatar": "", "groups": [],
        "isAdmin": True, "isGlobalAdmin": False, "isForbidden": False, "isDeleted": False,
        "createdIp": "", "ranking": 1, "score": 0, "tag": "", "signupApplication": "llm-gateway-e2e",
    }],
    "certs": [{
        "owner": "admin", "name": "e2e-cert", "displayName": "E2E JWT Cert",
        "type": "x509", "cryptoAlgorithm": "RS256", "bitSize": 4096, "scope": "JWT",
        "expireInYears": 20,
        # 注意：casdoor 上游 ParseJwtSigningKey 对 RSA 直接 ParseRSAPrivateKeyFromPEM，
        # cert.PrivateKey/Certificate 字段必须是裸 PEM 字符串（不做 base64，154 在役形态不适用本地 init_data 灌库路径）
        "certificate": pub,
        "privateKey": priv,
    }],
    "models": [], "adapters": [], "enforcers": [], "roles": [],
    "permissions": [], "providers": [], "ldaps": [], "syncers": [], "tokens": [],
    "webhooks": [], "groups": [], "plans": [], "pricings": [], "menus": [],
    "departments": [], "posts": [], "payments": [], "products": [], "resources": [],
}
with open("casdoor/init_data.json", "w") as f:
    json.dump(init_data, f, ensure_ascii=False, indent=1)
print("  init_data.json written (org=kaixuan-e2e app=llm-gateway-e2e user=gwuser cert=e2e-cert)")
PYEOF
ok "init_data.json 就绪"

# ── 3. app.conf ──
sed "s/@PG_PASSWORD@/$CASDOOR_DB_PASSWORD/" casdoor/app.conf.template > casdoor/app.conf
chmod 600 casdoor/app.conf
ok "casdoor/app.conf 已渲染（含 casdoor 库密码，gitignore 保护）"

# ── 4. gateway.env（网关 source 用）──
cat > gateway.env <<ENVFILE
# 本地网关联调 env（source 本文件后起网关；凭证文件在 runtime/，gitignore 保护）
export DATABASE_URL="postgres://gateway_e2e:${PG_PASSWORD}@127.0.0.1:15433/gateway_e2e?sslmode=disable"
export LLM_GATEWAY_LISTEN=":18781"
export LLM_GATEWAY_SECRET_KEY="${JWT_SECRET}"
export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="${CEK}"
export LLM_GATEWAY_REDIS_ADDR="127.0.0.1:18799"
export LLM_GATEWAY_SESSIONS_V2_ENABLED="true"
export LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE="true"
export LLM_GATEWAY_CORS_ORIGINS="*"
export LLM_GATEWAY_SEED_ADMIN_PASSWORD="${ADMIN_PASSWORD}"
# ★ 2026-11-27 e2e 验证轮：刻意**不**设 AUTH_PROVIDER —— loadPluginConfig 的
#   优先级是 env > plugin_config 表 > 默认，设了会把表里的选择静默压掉
#   （「表是唯一事实源」被启动 env 打回 ⇒ PUT 热切换的口径在重启后失真）。
#   provider 选择只落 plugin_config 表（默认 native，C3 判据经 PUT 切 casdoor）。
export CASDOOR_ENDPOINT="http://127.0.0.1:18000"
export CASDOOR_CLIENT_ID="e2e-client-id"
export CASDOOR_CLIENT_SECRET="${CLIENT_SECRET}"
export CASDOOR_ORGANIZATION="kaixuan-e2e"
export CASDOOR_APPLICATION="llm-gateway-e2e"
export CASDOOR_TLS_SKIP_VERIFY=""
export CASDOOR_CERTIFICATE="$(cat "$RUNTIME/jwt_public.pem")"
ENVFILE
chmod 600 gateway.env
ok "gateway.env 就绪"

# migrate.sh 直接读 runtime/pg_password（密码 SSOT），无需中间 env 文件
ok "gateway.env 就绪"

# ── 5. 起栈 ──
export PG_PASSWORD CASDOOR_DB_PASSWORD
info "docker compose up..."
docker compose up -d
ok "栈已启动: PG 127.0.0.1:15433 / casdoor http://127.0.0.1:18000"

echo
echo "下一步:"
echo "  1. ./migrate.sh                 # 重建 gateway_e2e 库 + 全量迁移"
echo "  2. docker logs -f casdoor-e2e   # 看 casdoor 初始化"
echo "  3. curl -s http://127.0.0.1:18000/.well-known/openid-configuration | head -c 200"
echo "  4. set -a; source gateway.env; set +a; go build -o runtime/gateway ./cmd/gateway && ./runtime/gateway"
