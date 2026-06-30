#!/bin/bash
set -e

# Phase 2.x 实际请求反馈集成 - Kubernetes 部署脚本
# 基于 deploy_phase2_k8s_upload.sh 改进

SERVER="184"
NAMESPACE="pms-test"
DEPLOYMENT="llm-gateway-go-deployment"
IMAGE_NAME="kx-llm-gateway-go"
GIT_SHA=$(git rev-parse --short HEAD)
IMAGE_TAG="gitsha-${GIT_SHA}"
REMOTE_BUILD_DIR="/opt/kx-memora-build/services/llm-gateway-go"

echo "=== Phase 2.x 实际请求反馈集成 - Kubernetes 部署 ==="
echo "Git SHA: ${GIT_SHA}"
echo "镜像标签: ${IMAGE_TAG}"
echo ""

# 1. 打包代码
echo "1. 打包本地代码..."
TEMP_TARBALL="/tmp/llm-gateway-go-${GIT_SHA}.tar.gz"
tar czf ${TEMP_TARBALL} \
    --exclude='.git' \
    --exclude='node_modules' \
    --exclude='*.log' \
    --exclude='coverage' \
    --exclude='.DS_Store' \
    .
echo "✓ 打包完成: $(ls -lh ${TEMP_TARBALL} | awk '{print $5}')"
echo ""

# 2. 上传到服务器
echo "2. 上传到184服务器..."
scp ${TEMP_TARBALL} ${SERVER}:/tmp/
echo "✓ 上传完成"
echo ""

# 3. 解压并构建镜像
echo "3. 在184服务器构建镜像..."
ssh $SERVER bash << REMOTE_BUILD
set -e

echo "  → 备份当前代码..."
if [ -d ${REMOTE_BUILD_DIR}.bak-phase2x ]; then
    rm -rf ${REMOTE_BUILD_DIR}.bak-phase2x
fi
cp -a ${REMOTE_BUILD_DIR} ${REMOTE_BUILD_DIR}.bak-phase2x

echo "  → 解压新代码..."
cd ${REMOTE_BUILD_DIR}
tar xzf /tmp/llm-gateway-go-${GIT_SHA}.tar.gz

echo "  → 确保使用正确的基础镜像..."
sed -i 's/kx-base:go-vue-alpine-slim-runtime/kx-base:go-vue-amd64/g' Dockerfile

echo "  → 构建 Docker 镜像..."
docker build --build-arg GIT_SHA=${GIT_SHA} -t ${IMAGE_NAME}:${IMAGE_TAG} -t ${IMAGE_NAME}:phase2x-latest .

echo "  ✓ 镜像构建成功"
docker images | grep ${IMAGE_NAME} | head -3

# 清理临时文件
rm -f /tmp/llm-gateway-go-${GIT_SHA}.tar.gz

REMOTE_BUILD

# 本地清理
rm -f ${TEMP_TARBALL}

echo "✓ 镜像构建完成"
echo ""

# 4. 更新 Deployment
echo "4. 更新 Deployment..."
ssh $SERVER bash << 'REMOTE_DEPLOY'
set -e

echo "  → 导出当前 deployment 配置..."
kubectl get deployment llm-gateway-go-deployment -n pms-test -o yaml > /tmp/llm-gateway-deployment-phase2x.yaml

echo "  → 修改镜像..."
sed -i "s|image: kx-llm-gateway-go:.*|image: kx-llm-gateway-go:gitsha-0d5aec70|" /tmp/llm-gateway-deployment-phase2x.yaml
sed -i 's|imagePullPolicy: Never|imagePullPolicy: IfNotPresent|' /tmp/llm-gateway-deployment-phase2x.yaml

echo "  → 应用配置..."
kubectl apply -f /tmp/llm-gateway-deployment-phase2x.yaml

echo "  → 等待滚动更新..."
kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=5m

echo "  ✓ 滚动更新完成"

REMOTE_DEPLOY

echo "✓ 部署完成"
echo ""

# 5. 验证部署
echo "5. 验证部署..."
ssh $SERVER bash << 'REMOTE_VERIFY'
set -e

echo "  → Pod 状态:"
kubectl get pods -n pms-test -l app=llm-gateway-go

echo ""
echo "  → 等待新 Pod 就绪..."
sleep 15

POD=$(kubectl get pods -n pms-test -l app=llm-gateway-go --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')
echo "  运行中的 Pod: ${POD}"

echo ""
echo "  → 验证镜像版本:"
kubectl get pod ${POD} -n pms-test -o jsonpath='{.spec.containers[0].image}'
echo ""

echo ""
echo "  → 检查关键日志:"
kubectl logs -n pms-test ${POD} --tail=100 | grep -E "credential state observer|postgres connected|gateway listening" | head -10

REMOTE_VERIFY

echo ""
echo "=== Phase 2.x 部署成功 ==="
echo ""
echo "📋 验证清单:"
echo ""
echo "1. 查看完整日志:"
echo "   ssh 184 'kubectl logs -n pms-test -l app=llm-gateway-go -f | grep -E \"StateObserver|UpdateOn\"'"
echo ""
echo "2. 模拟成功请求（观察 UpdateOnSuccess 调用）:"
echo "   curl -X POST http://184:31781/v1/chat/completions ..."
echo ""
echo "3. 模拟失败请求（观察 UpdateOnFailure 调用）:"
echo "   # 使用无效凭据或模型"
echo ""
echo "4. 模拟用户取消（验证不计入统计）:"
echo "   curl -X POST http://184:31781/v1/chat/completions ... & sleep 1 && kill \$!"
echo ""
echo "5. 检查数据库 credential_states 表:"
echo "   kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c 'SELECT * FROM credential_states ORDER BY last_updated_at DESC LIMIT 10;'"
echo ""
echo "6. 回滚（如需要）:"
echo "   ssh 184 'kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test'"
