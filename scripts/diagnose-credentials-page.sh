#!/usr/bin/env bash
# diagnose-credentials-page.sh
# 诊断 https://llmgo.kxpms.cn/routing-v2/credentials 页面无数据问题

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_LOCAL="$ROOT_DIR/.env.local"

# 加载环境变量
if [ -f "$ENV_LOCAL" ]; then
  set -a; . "$ENV_LOCAL"; set +a
fi

SSH_HOST="${INTERNAL_PUBLIC_IP:-14.103.112.184}"
SSH_PORT="${SSH_PORT_184:-25022}"
SSH_KEY="${SSH_KEY_184_PATH:-$HOME/.ssh/56_id_rsa}"
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=10"

echo "=========================================="
echo "诊断 /routing-v2/credentials 无数据问题"
echo "=========================================="
echo ""

# 1. 检查数据库数据
echo "1️⃣  检查数据库中的 credentials 数据..."
CRED_COUNT=$(ssh -i "$SSH_KEY" -p "$SSH_PORT" $SSH_OPTS "root@$SSH_HOST" \
  "kubectl exec -n pms-test deployment/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -tAc \"SELECT COUNT(*) FROM credentials WHERE lifecycle_status != 'retired';\"")
echo "   ✓ credentials 表: $CRED_COUNT 条记录"

MODEL_OFFERS_COUNT=$(ssh -i "$SSH_KEY" -p "$SSH_PORT" $SSH_OPTS "root@$SSH_HOST" \
  "kubectl exec -n pms-test deployment/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -tAc \"SELECT COUNT(*) FROM model_offers;\"")
echo "   ✓ model_offers 表: $MODEL_OFFERS_COUNT 条记录"

# 2. 检查用户角色
echo ""
echo "2️⃣  检查用户角色配置..."
ssh -i "$SSH_KEY" -p "$SSH_PORT" $SSH_OPTS "root@$SSH_HOST" \
  "kubectl exec -n pms-test deployment/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -c \"SELECT id, username, role, enabled FROM users ORDER BY id;\""

# 3. 检查 API 端点配置
echo ""
echo "3️⃣  检查 API 端点权限要求..."
echo "   /api/credentials/monitor-summary 需要 super_admin 角色"
echo "   前端路由 /routing-v2/credentials 标记为 requiresSuper: true"

# 4. 检查服务状态
echo ""
echo "4️⃣  检查 llm-gateway-go 服务状态..."
GATEWAY_RUNNING=$(ssh -i "$SSH_KEY" -p "$SSH_PORT" $SSH_OPTS "root@$SSH_HOST" \
  "kubectl get pods -n pms-test -l app=llm-gateway-go --no-headers | grep -c Running || echo 0")
echo "   ✓ Gateway pods running: $GATEWAY_RUNNING"

# 5. 诊断结果
echo ""
echo "=========================================="
echo "诊断结果与建议"
echo "=========================================="
echo ""
echo "🔍 问题原因："
echo "   /api/credentials/monitor-summary 端点要求 super_admin 角色"
echo "   普通 tenant_admin 用户访问会收到 403 Forbidden"
echo ""
echo "✅ 解决方案："
echo ""
echo "方案 1: 使用 super_admin 账号登录"
echo "   用户名: admin"
echo "   需要在 https://llmgo.kxpms.cn/login 使用 super_admin 账号登录"
echo ""
echo "方案 2: 修改权限配置（如果需要让 tenant_admin 也能访问）"
echo "   修改文件: admin/handler.go"
echo "   将 RegisterMonitorRoutes(mux, h.superAdmin) 改为 RegisterMonitorRoutes(mux, h.admin)"
echo "   然后重新部署"
echo ""
echo "方案 3: 提升现有用户权限"
echo "   运行以下命令将某个用户提升为 super_admin："
echo "   ssh -i $SSH_KEY -p $SSH_PORT root@$SSH_HOST \\"
echo "     \"kubectl exec -n pms-test deployment/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -c \\\"UPDATE users SET role='super_admin' WHERE username='YOUR_USERNAME';\\\"\""
echo ""
echo "=========================================="
