#!/usr/bin/env bash
# =============================================================================
# register-local-provider.sh — 一键注册本地模型供应商到网关
#
# 流程：
#   1. 用 modelctl.sh ensure 自动拉起本地推理服务
#   2. 登录网关管理 API 获取 token
#   3. POST /api/providers 创建 kind='local' 供应商（免 key 凭据自动生成）
#   4. 触发模型发现（provider refresh）
#   5. 输出注册结果（provider_id / credential_id / discovered models）
#
# 用法：
#   ./register-local-provider.sh <local-ollama|local-mlx-lm|local-mlx-dspark|...> \
#       [gateway_base_url] [override_base_url]
#
# 示例：
#   # 网关本机直跑：
#   ./register-local-provider.sh local-mlx-lm http://127.0.0.1:8782
#   # 网关跑在 Docker（容器内访问宿主机服务需 host.docker.internal）：
#   ./register-local-provider.sh local-mlx-lm http://127.0.0.1:8782 \
#       http://host.docker.internal:8080/v1
# =============================================================================
set -euo pipefail

CATALOG_CODE="${1:?用法: $0 <local-ollama|local-mlx-lm|local-mlx-dspark|local-llamacpp|local-lmstudio|local-vllm> [gateway_url] [base_url_override]}"
GW="${2:-http://127.0.0.1:8782}"
BASE_URL_OVERRIDE="${3:-}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# 托管类型 → modelctl 类型名
case "$CATALOG_CODE" in
  local-ollama)     CTL=ollama;  PORT=11434 ;;
  local-mlx-lm)     CTL=mlx-lm;  PORT=8080 ;;
  local-mlx-dspark) CTL=dspark;  PORT=8081 ;;
  local-llamacpp)   CTL=llamacpp;PORT=8082 ;;
  local-lmstudio)   CTL=lmstudio;PORT=1234 ;;
  local-vllm)       CTL=vllm;    PORT=8000 ;;
  *) echo "✗ 未知本地目录代码: $CATALOG_CODE"; exit 1 ;;
esac

# 1. 自动拉起本地服务（lmstudio/vllm/llamacpp 只探活不代启）
echo "── 1/4 确保本地服务运行: $CTL :$PORT"
if ! bash "$SCRIPT_DIR/modelctl.sh" ensure "$CTL" "$PORT"; then
  echo "✗ 本地服务未就绪，中止注册"; exit 1
fi

# 2. 登录网关
echo "── 2/4 登录网关 $GW"
: "${LLM_GATEWAY_ADMIN_USER:=admin}"
: "${LLM_GATEWAY_ADMIN_PASSWORD:?需要 LLM_GATEWAY_ADMIN_PASSWORD 环境变量}"
TOKEN=$(curl -sS -m 10 -X POST "$GW/api/auth/token" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$LLM_GATEWAY_ADMIN_USER\",\"password\":\"$LLM_GATEWAY_ADMIN_PASSWORD\"}" \
  | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("token") or d.get("access_token") or "")')
[ -n "$TOKEN" ] || { echo "✗ 登录失败"; exit 1; }

# 3. 创建本地供应商（base_url 可覆盖；凭据自动生成，无需传 api_key）
echo "── 3/4 创建本地供应商 $CATALOG_CODE"
BODY="{\"catalog_code\":\"$CATALOG_CODE\""
if [ -n "$BASE_URL_OVERRIDE" ]; then BODY="$BODY,\"base_url\":\"$BASE_URL_OVERRIDE\""; fi
BODY="$BODY}"
CREATE_RESP=$(curl -sS -m 15 -X POST "$GW/api/providers" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$BODY")
echo "$CREATE_RESP" | python3 -m json.tool
PROVIDER_ID=$(echo "$CREATE_RESP" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",0))')
CRED_ID=$(echo "$CREATE_RESP" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("credential_id",0))')
[ "$PROVIDER_ID" != "0" ] || { echo "✗ 供应商创建失败（可能已存在）"; }

# 4. 触发模型发现
echo "── 4/4 触发模型发现 (provider_id=$PROVIDER_ID)"
curl -sS -m 60 -X POST "$GW/api/providers/$PROVIDER_ID/refresh-models" \
  -H "Authorization: Bearer $TOKEN" | head -c 500; echo
sleep 3
curl -sS -m 15 "$GW/api/providers/$PROVIDER_ID/models" \
  -H "Authorization: Bearer $TOKEN" | head -c 800; echo

echo
echo "✅ 注册完成: provider_id=$PROVIDER_ID credential_id=$CRED_ID"
echo "   验证: curl -s $GW/v1/models | jq '.data[].id' | grep -i <模型名>"
