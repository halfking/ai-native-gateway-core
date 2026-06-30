#!/bin/bash
set -e

# Phase 2 热度追踪功能 - Kubernetes 部署脚本
# 适用于184测试环境 (k3s)

SERVER="184"
NAMESPACE="pms-test"
DEPLOYMENT="llm-gateway-go-deployment"
IMAGE_NAME="kx-llm-gateway-go"
GIT_SHA=$(git rev-parse --short HEAD)
IMAGE_TAG="gitsha-${GIT_SHA}"

echo "=== Phase 2 热度追踪 - Kubernetes 部署 ==="
echo "Git SHA: ${GIT_SHA}"
echo "镜像标签: ${IMAGE_TAG}"
echo ""

# 1. 在184服务器上构建镜像
echo "1. 在184服务器构建新镜像..."
ssh $SERVER bash << REMOTE_BUILD
set -e
cd /opt/kx-memora-build/services/llm-gateway-go

echo "  → 拉取最新代码..."
git fetch origin
git checkout main
git pull origin main

CURRENT_SHA=\$(git rev-parse --short HEAD)
echo "  → 当前 commit: \${CURRENT_SHA}"

if [ "\${CURRENT_SHA}" != "${GIT_SHA}" ]; then
    echo "  ✗ 服务器代码不是最新的！"
    echo "    本地: ${GIT_SHA}"
    echo "    服务器: \${CURRENT_SHA}"
    exit 1
fi

echo "  → 构建 Docker 镜像..."
docker build -t ${IMAGE_NAME}:${IMAGE_TAG} -t ${IMAGE_NAME}:latest .

echo "  ✓ 镜像构建成功"
docker images | grep ${IMAGE_NAME} | head -3

REMOTE_BUILD

echo "✓ 镜像构建完成"
echo ""

# 2. 添加热度追踪环境变量
echo "2. 配置热度追踪环境变量（默认禁用）..."
ssh $SERVER bash << REMOTE_CONFIG
set -e

# 检查是否已有该环境变量
if kubectl get deployment ${DEPLOYMENT} -n ${NAMESPACE} -o yaml | grep -q "LLM_GATEWAY_ENABLE_POPULARITY_TRACKING"; then
    echo "  ✓ 环境变量已存在"
else
    echo "  → 添加环境变量..."
    kubectl set env deployment/${DEPLOYMENT} -n ${NAMESPACE} \
        LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false
    echo "  ✓ 环境变量已添加"
fi

REMOTE_CONFIG

echo ""

# 3. 更新 Deployment 镜像
echo "3. 更新 Deployment 镜像..."
ssh $SERVER bash << REMOTE_DEPLOY
set -e

echo "  → 更新镜像到 ${IMAGE_TAG}..."
kubectl set image deployment/${DEPLOYMENT} -n ${NAMESPACE} \
    llm-gateway-go=${IMAGE_NAME}:${IMAGE_TAG}

echo "  → 等待滚动更新..."
kubectl rollout status deployment/${DEPLOYMENT} -n ${NAMESPACE} --timeout=5m

echo "  ✓ 滚动更新完成"

REMOTE_DEPLOY

echo "✓ 部署完成"
echo ""

# 4. 验证部署
echo "4. 验证部署..."
ssh $SERVER bash << REMOTE_VERIFY
set -e

echo "  → 检查 Pod 状态..."
kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go

echo ""
echo "  → 检查启动日志..."
POD=\$(kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}')
kubectl logs -n ${NAMESPACE} \${POD} --tail=50 | grep -E "credential state manager|postgres connected|started" | tail -10

REMOTE_VERIFY

echo ""
echo "=== 部署验证清单 ==="
echo ""
echo "1. 查看实时日志:"
echo "   ssh 184 'kubectl logs -n ${NAMESPACE} -l app=llm-gateway-go -f'"
echo ""
echo "2. 检查热度追踪配置:"
echo "   ssh 184 'kubectl get deployment ${DEPLOYMENT} -n ${NAMESPACE} -o yaml | grep POPULARITY_TRACKING'"
echo ""
echo "3. 启用热度追踪（可选）:"
echo "   ssh 184 'kubectl set env deployment/${DEPLOYMENT} -n ${NAMESPACE} LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true'"
echo ""
echo "4. 数据库索引准备:"
echo "   ssh 184"
echo "   kubectl exec -n ${NAMESPACE} -it \$(kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-pg -o jsonpath='{.items[0].metadata.name}') -- psql -U llm_gateway"
echo "   CREATE INDEX CONCURRENTLY idx_request_logs_created_at_model ON request_logs (created_at DESC, client_model) WHERE client_model IS NOT NULL;"
echo ""
echo "5. 回滚（如需要）:"
echo "   ssh 184 'kubectl rollout undo deployment/${DEPLOYMENT} -n ${NAMESPACE}'"
