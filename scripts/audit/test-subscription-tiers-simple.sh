#!/usr/bin/env bash
# test-subscription-tiers-simple.sh — 简化版订阅层级测试
# 用于验证核心逻辑：JWT 生成 + API 调用

set -euo pipefail

BASE_URL="${1:-http://127.0.0.1:8781}"

echo "[1/3] 生成测试 JWT..."
TOKEN=$(python3 - <<'PYEOF'
import base64
import hashlib
import hmac
import json
import time
import os

def _b64(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()

SECRET = os.getenv("LLM_GATEWAY_SECRET_KEY", "dev-secret-key-change-in-production")

header = _b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
now = int(time.time())
payload = _b64(json.dumps({
    "user_id": 1,
    "tenant_id": "default",
    "username": "admin",
    "role": "super_admin",
    "exp": now + 1800,
    "iat": now,
    "iss": "llm-gateway",
    "aud": ["llm-gateway-api"]
}, separators=(",", ":")).encode())

sig = _b64(hmac.new(SECRET.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
print(f"{header}.{payload}.{sig}")
PYEOF
)

echo "✓ JWT 生成成功 (前8字符): ${TOKEN:0:8}..."

echo "[2/3] 测试后端 API..."
API_URL="${BASE_URL}/api/admin/tiers"
HTTP_CODE=$(curl -s -o /tmp/tiers_response.json -w "%{http_code}" \
  -H "Cookie: llmgw_session=$TOKEN" \
  "$API_URL")

echo "HTTP Status: $HTTP_CODE"

if [[ "$HTTP_CODE" == "200" ]]; then
  echo "✓ API 返回成功"
  cat /tmp/tiers_response.json | python3 -m json.tool 2>/dev/null || cat /tmp/tiers_response.json
elif [[ "$HTTP_CODE" == "404" ]]; then
  echo "⚠ API 端点未实现（404）- 这是预期的，需要后端实现"
else
  echo "✗ API 返回错误: $HTTP_CODE"
  cat /tmp/tiers_response.json 2>/dev/null || true
fi

echo "[3/3] 前端路由检查..."
PAGE_URL="${BASE_URL}/admin/subscription-tiers"
PAGE_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  -H "Cookie: llmgw_session=$TOKEN" \
  "$PAGE_URL")

echo "页面状态: $PAGE_CODE"

if [[ "$PAGE_CODE" == "200" ]]; then
  echo "✓ 前端页面可访问"
else
  echo "✗ 前端页面返回: $PAGE_CODE"
fi

echo ""
echo "总结:"
echo "- JWT 生成: ✓"
echo "- API 端点: $([[ "$HTTP_CODE" == "200" ]] && echo "✓" || echo "⚠ 未实现")"
echo "- 前端路由: $([[ "$PAGE_CODE" == "200" ]] && echo "✓" || echo "✗")"
