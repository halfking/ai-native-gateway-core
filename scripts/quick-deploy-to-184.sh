#!/bin/bash
# =====================================================================
# quick-deploy-to-184.sh — 快速部署到184服务器的一键脚本
#
# 功能：
# 1. 自动构建镜像（包含版本管理）
# 2. 导出并上传镜像到184服务器
# 3. 在184上加载镜像到本地registry
# 4. 更新K8s deployment
# 5. 验证部署状态
#
# 用法: 
#   ./scripts/quick-deploy-to-184.sh
#
# 环境要求:
# - Docker已安装
# - sshpass已安装
# - 本地有deploy/build-image.sh和scripts/bump-version.sh
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# 服务器配置
SERVER_IP="14.103.112.184"
SERVER_PORT="25022"
SERVER_USER="root"
SERVER_PASS='<SSH_PASSWORD_REDACTED>'
K8S_NAMESPACE="pms-test"
DEPLOYMENT_NAME="llm-gateway-go-deployment"
CONTAINER_NAME="llm-gateway-go"

echo "=========================================="
echo "🚀 llm-gateway-go 快速部署到 184"
echo "=========================================="
echo ""

# ── 步骤1: 构建镜像 ────────────────────────────────────────────────
echo "📦 [1/5] 构建 Docker 镜像..."
cd "${PROJECT_ROOT}"
./deploy/build-image.sh

# 读取版本号
VERSION=$(cat VERSION | tr -d '\n')
echo "✅ 构建完成，版本: ${VERSION}"
echo ""

# ── 步骤2: 导出镜像 ────────────────────────────────────────────────
echo "📤 [2/5] 导出镜像..."
IMAGE_NAME="kx-llm-gateway-go:${VERSION}"
TEMP_FILE="/tmp/kx-llm-gateway-go-${VERSION}.tar.gz"

docker save "${IMAGE_NAME}" | gzip > "${TEMP_FILE}"
echo "✅ 镜像已导出到: ${TEMP_FILE}"
ls -lh "${TEMP_FILE}"
echo ""

# ── 步骤3: 上传到184 ───────────────────────────────────────────────
echo "🌐 [3/5] 上传镜像到 184 服务器..."
export SSHPASS="${SERVER_PASS}"
sshpass -e scp -P "${SERVER_PORT}" -o StrictHostKeyChecking=no \
  "${TEMP_FILE}" \
  "${SERVER_USER}@${SERVER_IP}:/tmp/"
echo "✅ 上传完成"
echo ""

# ── 步骤4: 在184上部署 ─────────────────────────────────────────────
echo "🔧 [4/5] 在 184 上加载镜像并更新 K8s..."
sshpass -e ssh -p "${SERVER_PORT}" -o StrictHostKeyChecking=no \
  "${SERVER_USER}@${SERVER_IP}" <<EOF
set -e

echo "  → 加载镜像到 Docker..."
docker load < /tmp/kx-llm-gateway-go-${VERSION}.tar.gz

echo "  → 推送到本地 registry..."
docker tag kx-llm-gateway-go:${VERSION} 127.0.0.1:5000/kx-llm-gateway-go:${VERSION}
docker push 127.0.0.1:5000/kx-llm-gateway-go:${VERSION}

echo "  → 更新 K8s deployment..."
kubectl set image deployment/${DEPLOYMENT_NAME} \
  ${CONTAINER_NAME}=127.0.0.1:5000/kx-llm-gateway-go:${VERSION} \
  -n ${K8S_NAMESPACE}

echo "  → 等待 rollout 完成..."
kubectl rollout status deployment/${DEPLOYMENT_NAME} -n ${K8S_NAMESPACE} --timeout=120s

echo "  → 清理临时文件..."
rm -f /tmp/kx-llm-gateway-go-${VERSION}.tar.gz
EOF
echo "✅ K8s 部署完成"
echo ""

# ── 步骤5: 验证部署 ────────────────────────────────────────────────
echo "✅ [5/5] 验证部署..."
sshpass -e ssh -p "${SERVER_PORT}" -o StrictHostKeyChecking=no \
  "${SERVER_USER}@${SERVER_IP}" <<EOF
echo "  → Pod 状态:"
kubectl get pods -n ${K8S_NAMESPACE} | grep llm-gateway-go

echo ""
echo "  → 镜像版本:"
kubectl get deployment/${DEPLOYMENT_NAME} -n ${K8S_NAMESPACE} -o jsonpath='{.spec.template.spec.containers[0].image}'
echo ""

echo ""
echo "  → 最近日志:"
kubectl logs -n ${K8S_NAMESPACE} deployment/${DEPLOYMENT_NAME} --tail=10
EOF

echo ""
echo "=========================================="
echo "🎉 部署完成！"
echo "=========================================="
echo ""
echo "版本: ${VERSION}"
echo "服务: https://llmgo.kxpms.cn"
echo ""
echo "验证命令:"
echo "  curl -s https://llmgo.kxpms.cn/healthz"
echo ""
echo "查看日志:"
echo "  ssh -p 25022 root@14.103.112.184 'kubectl logs -n ${K8S_NAMESPACE} deployment/${DEPLOYMENT_NAME} --tail=50'"
echo ""

# 清理本地临时文件
rm -f "${TEMP_FILE}"
