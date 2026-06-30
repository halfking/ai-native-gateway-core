#!/bin/bash
set -e

# Phase 2 热度追踪功能 - Kubernetes 部署脚本（上传模式）
# 适用于184测试环境 (k3s)

SERVER="184"
NAMESPACE="pms-test"
DEPLOYMENT="llm-gateway-go-deployment"
IMAGE_NAME="kx-llm-gateway-go"
GIT_SHA=$(git rev-parse --short HEAD)
IMAGE_TAG="gitsha-${GIT_SHA}"
REMOTE_BUILD_DIR="/opt/kx-memora-build/services/llm-gateway-go"

echo "=== Phase 2 热度追踪 - Kubernetes 部署 (上传模式) ==="
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
if [ -d ${REMOTE_BUILD_DIR}.bak ]; then
    rm -rf ${REMOTE_BUILD_DIR}.bak
fi
cp -a ${REMOTE_BUILD_DIR} ${REMOTE_BUILD_DIR}.bak

echo "  → 解压新代码..."
cd ${REMOTE_BUILD_DIR}
tar xzf /tmp/llm-gateway-go-${GIT_SHA}.tar.gz

echo "  → 构建 Docker 镜像..."
docker build -t ${IMAGE_NAME}:${IMAGE_TAG} -t ${IMAGE_NAME}:latest .

echo "  ✓ 镜像构建成功"
docker images | grep ${IMAGE_NAME} | head -3

# 清理临时文件
rm -f /tmp/llm-gateway-go-${GIT_SHA}.tar.gz

REMOTE_BUILD

# 本地清理
rm -f ${TEMP_TARBALL}

echo "✓ 镜像构建完成"
echo ""

# 4. 检查并添加热度追踪环境变量
echo "4. 配置热度追踪环境变量..."
ssh $SERVER bash << 'REMOTE_CONFIG'
set -e

if kubectl get deployment llm-gateway-go-deployment -n pms-test -o yaml | grep -q "LLM_GATEWAY_ENABLE_POPULARITY_TRACKING"; then
    echo "  ✓ 环境变量已存在"
    kubectl get deployment llm-gateway-go-deployment -n pms-test -o yaml | grep -A1 "POPULARITY_TRACKING"
else
    echo "  → 添加环境变量（默认禁用）..."
    kubectl set env deployment/llm-gateway-go-deployment -n pms-test \
        LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false
    echo "  ✓ 环境变量已添加"
fi

REMOTE_CONFIG

echo ""

# 5. 更新 Deployment 镜像
echo "5. 更新 Deployment 镜像..."
ssh $SERVER bash << REMOTE_DEPLOY
set -e

echo "  → 当前镜像:"
kubectl get deployment ${DEPLOYMENT} -n ${NAMESPACE} -o jsonpath='{.spec.template.spec.containers[0].image}'
echo ""

echo "  → 更新到: ${IMAGE_NAME}:${IMAGE_TAG}"
kubectl set image deployment/${DEPLOYMENT} -n ${NAMESPACE} \
    llm-gateway-go=${IMAGE_NAME}:${IMAGE_TAG}

echo "  → 等待滚动更新..."
kubectl rollout status deployment/${DEPLOYMENT} -n ${NAMESPACE} --timeout=5m

echo "  ✓ 滚动更新完成"

REMOTE_DEPLOY

echo "✓ 部署完成"
echo ""

# 6. 验证部署
echo "6. 验证部署..."
ssh $SERVER bash << 'REMOTE_VERIFY'
set -e

echo "  → Pod 状态:"
kubectl get pods -n pms-test -l app=llm-gateway-go

echo ""
echo "  → 等待新 Pod 就绪..."
sleep 10

echo ""
echo "  → 检查启动日志（搜索 credential state manager）:"
POD=$(kubectl get pods -n pms-test -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}')
echo "  Pod: ${POD}"

kubectl logs -n pms-test ${POD} --tail=100 | grep -E "credential state manager|popularity" || echo "  (未找到热度追踪相关日志，这是正常的，因为默认禁用)"

REMOTE_VERIFY

echo ""
echo "=== 部署成功 ==="
echo ""
echo "📋 下一步操作:"
echo ""
echo "1. 查看完整日志:"
echo "   ssh 184 'kubectl logs -n pms-test -l app=llm-gateway-go --tail=200 | grep -E \"credential state|postgres\"'"
echo ""
echo "2. 查看环境变量配置:"
echo "   ssh 184 'kubectl get deployment llm-gateway-go-deployment -n pms-test -o yaml | grep -A2 POPULARITY'"
echo ""
echo "3. 准备数据库索引（必须）:"
echo "   参考 sql/phase2_db_setup.sql"
echo ""
echo "4. 启用热度追踪（可选，需先完成索引）:"
echo "   ssh 184 'kubectl set env deployment/llm-gateway-go-deployment -n pms-test LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true'"
echo ""
echo "5. 观察热度追踪日志（启用后5分钟）:"
echo "   ssh 184 'kubectl logs -n pms-test -l app=llm-gateway-go -f | grep popularity'"
echo ""
echo "6. 回滚（如需要）:"
echo "   ssh 184 'kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test'"
