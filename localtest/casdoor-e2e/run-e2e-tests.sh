#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# casdoor-e2e 八条判据完整测试（2026-11-27 L135 闭环真机验证）
#
# 前置：start-e2e.sh + migrate.sh 已跑；网关已在 tmux 会话 casdoor-e2e-gw
#       以 gateway.env 起在 :18781；casdoor 容器 healthy 在 :18000
#
# 判据：
#   C1  回归门：native 登录 + /api/auth/me 200（既有行为不回归）
#   C2  providers 列表含 casdoor（GET /api/admin/plugin-providers）
#   C3  PUT 热切换 → casdoor，响应 message 含 "no restart required"
#   C4  casdoor 登录成功且 token_source="re-signed"（断点①：换发本地 JWT）
#   C5  C4 的 token 打 /api/auth/me 200 不断链（issuer 白名单放行）
#   C6  身份物化：users 行存在 + auth_id_mapping.metadata.users_id 关联正确
#   C7  二次登录不重复物化（users 行数不变、映射同一条、users_id 不变）
#   C8  撤销纪元隔离：改 casdoor 物化用户的本地密码 ⇒ 旧 token 401、
#       casdoor 侧其它用户不受影响；随后 PUT 回切 native（回退判据）
# ─────────────────────────────────────────────────────────────────────
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

GW="http://127.0.0.1:18781"
CD="http://127.0.0.1:18000"
PASS=0; FAIL=0
say()  { printf '\n== %s ==\n' "$*"; }
ok_()  { echo "  ✓ $*"; PASS=$((PASS+1)); }
bad_() { echo "  ✗ $*"; FAIL=$((FAIL+1)); }
jq_get() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1" 2>/dev/null; }

PW="$(cat runtime/pg_password)"
PG_PW="$(cat runtime/pg_password)"
export "PGPASSWORD=$PG_PW"
q() { psql -h 127.0.0.1 -p 15433 -U gateway_e2e -d gateway_e2e -tAc "$1"; }

# captcha 自动解题登录驱动（bash 无法解 PNG ⇒ 委托 python 解题器；
# 网关闸门 armed 后一切登录强制 captcha —— r10 连跑翻红的根因）
captcha_login() { # $1=user $2=password_file → token 写 stdout
  python3 - "$1" "$2" <<'PYC'
import json, base64, sys, urllib.request, urllib.error
sys.path.insert(0, '/tmp')
from captcha_solver import solve_png
user = sys.argv[1]
secret_value = open(sys.argv[2]).read().strip()
for _ in range(4):
    d = json.load(urllib.request.urlopen('http://127.0.0.1:18781/api/auth/captcha'))
    png = base64.b64decode(d['image'].split(',',1)[-1])
    res = solve_png(png, '/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/admin/auth_captcha.go')
    if not res:
        continue
    payload = {"user": user, "pw": secret_value, "cid": d['captcha_id'], "cc": res[0]}
    pw_val = payload["pw"]
    body = json.dumps({"username": payload["user"], "password": pw_val, "captcha_id": payload["cid"], "captcha_code": payload["cc"]}).encode()
    req = urllib.request.Request('http://127.0.0.1:18781/api/auth/token', data=body, headers={'Content-Type':'application/json'})
    try:
        print(json.load(urllib.request.urlopen(req))['access_token'])
        sys.exit(0)
    except urllib.error.HTTPError:
        continue
sys.exit(1)
PYC
}

ADMIN_PW="$(echo YjlmZGZkZmI1YTQyZGVmNjc1MTQ0Mzg0 | base64 -d)"
USER_PW="$(cat runtime/casdoor_user_password)"

say "C0 前置健康"
curl -sf -m 5 "$GW/healthz" >/dev/null && ok_ "网关 healthz" || bad_ "网关 healthz"
curl -sf -m 5 "$CD/api/health" >/dev/null && ok_ "casdoor health" || bad_ "casdoor health"
# ★ 2026-11-27 e2e 验证轮重排：先读 runtime/admin_password_new ——
#   存在 ⇒ admin 已完成首登改密，直接 NEW 登录（重跑零失败计数，
#   不烧 captcha/限流闸门）；不存在 ⇒ 走 SEED 首登改密流程（生产语义）。
ADMIN_PW_NEW="$(cat runtime/admin_password_new 2>/dev/null || true)"
NATIVE_TOKEN=""
if [ -n "$ADMIN_PW_NEW" ]; then
  NATIVE_TOKEN=$(curl -sf -m 8 -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PW_NEW\"}" | jq_get "d['access_token']") || true
  if [ -z "$NATIVE_TOKEN" ] || [ "$NATIVE_TOKEN" = "None" ]; then
    # 闸门 armed（IP 已有失败计数）⇒ captcha 强制 ⇒ 走解题驱动
    NATIVE_TOKEN="$(captcha_login admin "$PWD/runtime/admin_password_new" 2>/dev/null || true)"
  fi
fi
if [ -n "$NATIVE_TOKEN" ] && [ "$NATIVE_TOKEN" != "None" ]; then
  ok_ "admin 新密码直登（幂等重跑）"
else
  NATIVE_TOKEN=$(curl -sf -m 8 -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PW\"}" | jq_get "d['access_token']") || true
  if [ -z "$NATIVE_TOKEN" ] || [ "$NATIVE_TOKEN" = "None" ]; then
    NATIVE_TOKEN="$(captcha_login admin "$PWD/gateway.env.seed_pw" 2>/dev/null || true)"
  fi
  [ -n "$NATIVE_TOKEN" ] && [ "$NATIVE_TOKEN" != "None" ] && ok_ "管理员首登拿 token" || bad_ "管理员首登失败"
  # seed 管理员首登带 must_change_password 门（admin API 全 403，仅 me/change-password/logout 白名单）
  # ⇒ 先改密解除门，再用新密码重新登录拿干净 token（生产语义，测试顺着走）
  if [ -z "$ADMIN_PW_NEW" ]; then ADMIN_PW_NEW="E2eAdmin#Rolled1"; printf '%s' "$ADMIN_PW_NEW" > runtime/admin_password_new; chmod 600 runtime/admin_password_new; fi
  MCP=$(curl -s -o /tmp/c0mcp.json -w '%{http_code}' -m 8 -X PUT "$GW/api/auth/change-password" \
    -H "Authorization: Bearer $NATIVE_TOKEN" -H 'Content-Type: application/json' \
    -d "{\"old_password\":\"$ADMIN_PW\",\"new_password\":\"$ADMIN_PW_NEW\"}")
  if [ "$MCP" = "200" ]; then ok_ "首登改密解除 must_change_password 门"
  else bad_ "改密失败 => $MCP $(cat /tmp/c0mcp.json | head -c 120)"; fi
  NATIVE_TOKEN=$(curl -sf -m 8 -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PW_NEW\"}" | jq_get "d['access_token']")
  [ -n "$NATIVE_TOKEN" ] && [ "$NATIVE_TOKEN" != "None" ] && ok_ "新密码重新登录拿干净 token" || bad_ "改密后登录失败"
fi
AUTH="Authorization: Bearer $NATIVE_TOKEN"

say "C1 回归门：/api/auth/me"
CODE=$(curl -s -o /tmp/c1.json -w '%{http_code}' -m 8 "$GW/api/auth/me" -H "$AUTH")
[ "$CODE" = "200" ] && ok_ "/api/auth/me 200 (native 会话)" || bad_ "/api/auth/me => $CODE"

say "C2 providers 含 casdoor"
PROV=$(curl -s -m 8 "$GW/api/admin/plugin-providers" -H "$AUTH")
echo "$PROV" | grep -q casdoor && ok_ "plugin-providers 列出 casdoor" || bad_ "providers 无 casdoor: $(echo "$PROV" | head -c 120)"

say "C3 PUT 热切换 → casdoor"
PUT=$(curl -s -m 8 -X PUT "$GW/api/admin/plugin-config" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"auth_provider":"casdoor"}')
echo "$PUT" | grep -q "no restart required" && ok_ "PUT message=no restart required" || bad_ "PUT 响应: $(echo "$PUT" | head -c 200)"

say "C4 casdoor 登录 → re-signed"
LOGIN=$(curl -s -m 15 -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
  -d "{\"username\":\"gwuser\",\"password\":\"$USER_PW\"}")
TS=$(echo "$LOGIN" | jq_get "d['token_source']")
CT=$(echo "$LOGIN" | jq_get "d['access_token']")
[ "$TS" = "re-signed" ] && ok_ "token_source=re-signed (断点①)" || bad_ "token_source=$TS resp=$(echo "$LOGIN" | head -c 200)"
[ -n "$CT" ] && [ "$CT" != "None" ] && ok_ "拿到换发 JWT" || bad_ "无 access_token"

say "C5 换发 token 会话不断链"
CODE=$(curl -s -o /tmp/c5.json -w '%{http_code}' -m 8 "$GW/api/auth/me" -H "Authorization: Bearer $CT")
[ "$CODE" = "200" ] && ok_ "/api/auth/me 200 (换发 JWT)" || bad_ "/api/auth/me => $CODE"

say "C6 身份物化 + metadata 关联"
UN=$(q "SELECT username FROM users WHERE username='gwuser'")
GWUID=$(q "SELECT id FROM users WHERE username='gwuser'")
MID=$(q "SELECT internal_id FROM auth_id_mapping WHERE provider='casdoor' AND external_id=(SELECT external_id FROM auth_id_mapping WHERE username='gwuser' AND provider='casdoor' LIMIT 1) LIMIT 1")
META=$(q "SELECT metadata->>'users_id' FROM auth_id_mapping WHERE username='gwuser' AND provider='casdoor' LIMIT 1")
[ -n "$UN" ] && ok_ "users 行物化 (id=$GWUID)" || bad_ "users 无 gwuser"
[ "$META" = "$GWUID" ] && [ -n "$META" ] && ok_ "metadata.users_id=$META == users.id" || bad_ "metadata.users_id=$META vs users.id=$GWUID"

say "C7 二次登录不重复物化"
UID2=$(q "SELECT count(*) FROM users WHERE username='gwuser'")
MAP2=$(q "SELECT count(*) FROM auth_id_mapping WHERE username='gwuser' AND provider='casdoor'")
LOGIN2=$(curl -s -m 15 -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
  -d "{\"username\":\"gwuser\",\"password\":\"$USER_PW\"}")
CT2=$(echo "$LOGIN2" | jq_get "d['access_token']")
UID3=$(q "SELECT count(*) FROM users WHERE username='gwuser'")
MAP3=$(q "SELECT count(*) FROM auth_id_mapping WHERE username='gwuser' AND provider='casdoor'")
META3=$(q "SELECT metadata->>'users_id' FROM auth_id_mapping WHERE username='gwuser' AND provider='casdoor' LIMIT 1")
[ "$UID2" = "1" ] && [ "$UID3" = "1" ] && ok_ "users 行数恒 1" || bad_ "users 行数 $UID2→$UID3"
[ "$MAP2" = "1" ] && [ "$MAP3" = "1" ] && ok_ "映射恒 1 条" || bad_ "映射 $MAP2→$MAP3"
[ "$META3" = "$GWUID" ] && ok_ "users_id 稳定" || bad_ "users_id $META → $META3"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 8 "$GW/api/auth/me" -H "Authorization: Bearer $CT2")
[ "$CODE" = "200" ] && ok_ "二次换发 token 也能用" || bad_ "二次 token /me => $CODE"

say "C8 撤销纪元隔离 + 回退 native"
# ★ 2026-11-27 e2e 验证轮重写：物化行密码是随机 32B bcrypt（user_provisioner.go
#   「永不可本地登录」设计）⇒ self change-password 的 old_password 比对结构性
#   不可能成功。撤销判据改走 admin 强制重置 PUT /api/users/{id}/password
#   （admin/users.go 同样推进 revokeUserTokensForPasswordChange 纪元，
#   user.reset_password 审计）——这正是真实场景「管理员响应凭据泄露」。
OLD_CT="$CT"
GWUID=$(q "SELECT id FROM users WHERE username='gwuser'")
CPC=$(curl -s -o /tmp/c8cp.json -w '%{http_code}' -m 8 -X PUT "$GW/api/users/$GWUID/password" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"password\":\"$(echo RTJlUm9sbG92ZXIjMjAyNg== | base64 -d)\"}")
[ "$CPC" = "200" ] && ok_ "admin 强制重置 200" || bad_ "reset => $CPC $(cat /tmp/c8cp.json | head -c 120)"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 8 "$GW/api/auth/me" -H "Authorization: Bearer $OLD_CT")
[ "$CODE" = "401" ] && ok_ "旧 token 已撤销 (401, 撤销纪元对物化用户生效)" || bad_ "旧 token /me => $CODE (期望 401)"
# 撤销隔离：admin 自己的会话不受 gwuser 纪元推进影响
ACODE=$(curl -s -o /dev/null -w '%{http_code}' -m 8 "$GW/api/auth/me" -H "$AUTH")
[ "$ACODE" = "200" ] && ok_ "admin 会话不受影响 (隔离成立)" || bad_ "admin /me => $ACODE (期望 200)"
# 回退 native：PUT 回切 + admin（native 管理员）登录仍可用
PUT2=$(curl -s -m 8 -X PUT "$GW/api/admin/plugin-config" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"auth_provider":"native"}')
echo "$PUT2" | grep -q "no restart required" && ok_ "回退 PUT 成功" || bad_ "回退 PUT: $(echo "$PUT2" | head -c 160)"
RELOGIN=$(curl -s -m 15 -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PW_NEW\"}")
TS2=$(echo "$RELOGIN" | jq_get "d['token_source']")
if [ "$TS2" = "provider" ]; then ok_ "回切后 native 登录仍可用 (token_source=provider 本地签发)"
else bad_ "回切后 admin 登录 token_source=$TS2 resp=$(echo "$RELOGIN" | head -c 160)"; fi
# 回切后 casdoor 用户不再能登录（gwuser 的凭据改由 native 表校验，随机 bcrypt
# 永不可命中 —— 这就是「物化行不可本地登录」的实测面）
NLOGIN=$(curl -s -o /dev/null -w '%{http_code}' -m 15 -X POST "$GW/api/auth/token" -H 'Content-Type: application/json' \
  -d "{\"username\":\"gwuser\",\"password\":\"$(cat runtime/casdoor_user_password)\"}")
[ "$NLOGIN" = "401" ] && ok_ "回切后 gwuser casdoor 口令被 native 拒绝 (401, 不可本地登录)" || bad_ "回切后 gwuser => $NLOGIN (期望 401)"

say "总结"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = "0" ] && echo "ALL GREEN" || echo "HAS RED"
