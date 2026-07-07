#!/usr/bin/env bash
# ====================================================================
# 184 轻量健康探测 — 由 cron 每 5 分钟调用，部署在 184 宿主机
# ====================================================================
# 探测内容：
#   1. 网关 /healthz（HTTP 200 + status=ok）
#   2. 发 1 个测试请求验证路由可用（检查 _mock_identity 或 choices）
#   3. 抽样检查 3 个 mock 的 /healthz
# 失败时输出 [ALERT]，便于日志聚合告警
# ====================================================================
set -uo pipefail

LOG_PREFIX="[health-probe $(date '+%Y-%m-%d %H:%M:%S')]"
GW_DEPLOY="llm-gateway-go-deployment"
GW_POD_CMD="kubectl -n pms-test exec \$(kubectl -n pms-test get pods -l app=llm-gateway-go -o name | head -1) --"

# 从 Pod 环境读取 admin key（不硬编码）
ADMIN_KEY=$(eval "$GW_POD_CMD printenv LLM_GATEWAY_ADMIN_API_KEY" 2>/dev/null || echo "")

# 1. 网关健康检查
health=$(eval "$GW_POD_CMD curl -sS --max-time 5 http://localhost:8781/healthz 2>/dev/null" | grep -o '"status":"ok"' || echo "")
if [ -n "$health" ]; then
    echo "$LOG_PREFIX [OK] gateway /healthz"
else
    echo "$LOG_PREFIX [ALERT] gateway /healthz FAILED"
fi

# 2. 路由测试（发 1 个请求，验证能路由到 mock）
if [ -n "$ADMIN_KEY" ]; then
    route_resp=$(eval "$GW_POD_CMD curl -sS --max-time 15 http://localhost:8781/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $ADMIN_KEY' \
        -H 'X-Gw-Session-Id: cron-health-probe' \
        -d '{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"health probe\"}]}' 2>/dev/null" || echo "")
    if echo "$route_resp" | grep -q '_mock_identity'; then
        mock_id=$(echo "$route_resp" | grep -o '_mock_identity":"[^"]*"' | head -1)
        echo "$LOG_PREFIX [OK] route OK ($mock_id)"
    elif echo "$route_resp" | grep -q '"choices"'; then
        echo "$LOG_PREFIX [OK] route OK (non-mock provider, still healthy)"
    else
        echo "$LOG_PREFIX [ALERT] route FAILED: $(echo "$route_resp" | head -c 150)"
    fi
else
    echo "$LOG_PREFIX [WARN] could not read ADMIN_API_KEY, skip route test"
fi

# 3. 抽样检查 mock（3 个，用 Pod 可达的宿主机 IP）
mock_ok=0
for port in 19080 19085 19091; do
    if curl -sS --max-time 2 http://172.31.0.4:$port/healthz >/dev/null 2>&1; then
        mock_ok=$((mock_ok + 1))
    fi
done
echo "$LOG_PREFIX [INFO] mock health: $mock_ok/3"
