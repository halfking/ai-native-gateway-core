#!/bin/bash
# deploy-184.sh - 标准化184环境部署脚本
# 用途: 自动化完成从代码提交到K8s部署的完整流程
# 版本号原则: 从 git 仓库最近的 tag 获取

set -euo pipefail

# ==================== 配置区 ====================
SERVER="root@14.103.112.184"
SSH_PORT="25022"
NAMESPACE="pms-test"
DEPLOYMENT="llm-gateway-go-deployment"
IMAGE_NAME="kx-llm-gateway-go"
REGISTRY_INTERNAL="registry.kxpms.cn"
REGISTRY_LOCAL="127.0.0.1:5000"
HEALTH_ENDPOINT="http://localhost:30080/health"
OLD_IMAGE_DAYS=30

# ==================== 颜色输出 ====================
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[✓]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[⚠]${NC} $1"
}

log_error() {
    echo -e "${RED}[✗]${NC} $1"
}

log_step() {
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}$1${NC}"
    echo -e "${GREEN}========================================${NC}"
}

# ==================== 步骤1: 检查未提交改动 ====================
check_uncommitted_changes() {
    log_step "步骤 1/8: 检查未提交改动"
    
    if ! git diff-index --quiet HEAD --; then
        log_warn "检测到未提交的改动:"
        git status --short
        echo ""
        read -p "是否提交这些改动? (y/n): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            git add .
            read -p "请输入提交信息: " COMMIT_MSG
            git commit -m "${COMMIT_MSG}"
            log_success "改动已提交"
        else
            log_warn "跳过提交，继续部署（改动不会包含在镜像中）"
        fi
    else
        log_success "工作区干净，无未提交改动"
    fi
}

# ==================== 步骤2: 获取版本信息 ====================
get_version_info() {
    log_step "步骤 2/8: 获取版本信息"
    
    # 从 git 获取最近的 tag 作为版本号
    GIT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    log_info "Git Tag: ${GIT_TAG}"
    
    # 获取当前提交的短 SHA
    GIT_SHA=$(git rev-parse --short=8 HEAD)
    log_info "Git SHA: ${GIT_SHA}"
    
    # 获取构建日期
    BUILD_DATE=$(date +%Y%m%d)
    log_info "Build Date: ${BUILD_DATE}"
    
    # 读取并递增构建序号（使用 build_seq 文件以保持一致性）
    if [[ -f "build_seq" ]]; then
        BUILD_SEQ=$(cat build_seq)
    else
        BUILD_SEQ=0
        log_warn "build_seq 文件不存在，初始化为 0"
    fi
    
    NEW_BUILD_SEQ=$((BUILD_SEQ + 1))
    echo "${NEW_BUILD_SEQ}" > build_seq
    log_info "Build Seq: ${BUILD_SEQ} -> ${NEW_BUILD_SEQ}"
    
    # 生成完整镜像标签
    IMAGE_TAG="${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${NEW_BUILD_SEQ}"
    
    # 生成 version.json 文件
    cat > version.json <<EOF
{
  "version": "${GIT_TAG}",
  "git_tag": "${GIT_TAG}",
  "git_sha": "${GIT_SHA}",
  "build_seq": ${NEW_BUILD_SEQ},
  "build_date": "${BUILD_DATE}",
  "module": "llm-gateway-go"
}
EOF
    
    log_success "版本信息:"
    echo "  Git Tag:    ${GIT_TAG}"
    echo "  Git SHA:    ${GIT_SHA}"
    echo "  Build Seq:  ${NEW_BUILD_SEQ}"
    echo "  Build Date: ${BUILD_DATE}"
    echo "  Image Tag:  ${IMAGE_TAG}"
}

# ==================== 步骤3: 构建Docker镜像 ====================
build_docker_image() {
    log_step "步骤 3/8: 构建Docker镜像"
    
    log_info "开始构建镜像 ${IMAGE_NAME}:${IMAGE_TAG}..."
    
    docker build \
        --build-arg GIT_TAG="${GIT_TAG}" \
        --build-arg GIT_SHA="${GIT_SHA}" \
        --build-arg BUILD_SEQ="${NEW_BUILD_SEQ}" \
        --build-arg BUILD_DATE="${BUILD_DATE}" \
        -t ${IMAGE_NAME}:${IMAGE_TAG} \
        -t ${IMAGE_NAME}:latest \
        .
    
    log_success "镜像构建完成"
    docker images | grep ${IMAGE_NAME} | head -3
}

# ==================== 步骤4: 推送镜像 ====================
push_docker_image() {
    log_step "步骤 4/8: 推送镜像到Registry"
    
    # 推送到内部 registry
    log_info "推送到内部 registry: ${REGISTRY_INTERNAL}"
    docker tag ${IMAGE_NAME}:${IMAGE_TAG} ${REGISTRY_INTERNAL}/${IMAGE_NAME}:${IMAGE_TAG}
    docker push ${REGISTRY_INTERNAL}/${IMAGE_NAME}:${IMAGE_TAG}
    log_success "已推送到 ${REGISTRY_INTERNAL}"
    
    # 推送到 184 本地 registry
    log_info "推送到184本地 registry: ${REGISTRY_LOCAL}"
    docker tag ${IMAGE_NAME}:${IMAGE_TAG} ${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG}
    docker push ${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG}
    log_success "已推送到 ${REGISTRY_LOCAL}"
}

# ==================== 步骤5: 更新K8s部署 ====================
update_k8s_deployment() {
    log_step "步骤 5/8: 更新Kubernetes部署"
    
    log_info "更新 deployment 镜像..."
    ssh -p ${SSH_PORT} ${SERVER} "kubectl set image deployment/${DEPLOYMENT} \
        llm-gateway-go=${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG} \
        -n ${NAMESPACE}"
    
    log_info "等待 rolling update 完成..."
    ssh -p ${SSH_PORT} ${SERVER} "kubectl rollout status deployment/${DEPLOYMENT} -n ${NAMESPACE} --timeout=5m"
    
    log_success "Kubernetes 部署更新完成"
}

# ==================== 步骤6: 健康检查 ====================
health_check() {
    log_step "步骤 6/8: 健康检查"
    
    log_info "等待服务就绪..."
    sleep 10
    
    # 检查 Pod 状态
    log_info "Pod 状态:"
    ssh -p ${SSH_PORT} ${SERVER} "kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go"
    
    # 健康检查
    log_info "健康检查:"
    HEALTH_RESPONSE=$(ssh -p ${SSH_PORT} ${SERVER} "curl -s ${HEALTH_ENDPOINT}" || echo "{}")
    echo "${HEALTH_RESPONSE}" | jq '.' 2>/dev/null || echo "${HEALTH_RESPONSE}"
    
    # 验证版本
    log_info "验证容器内版本信息..."
    POD_NAME=$(ssh -p ${SSH_PORT} ${SERVER} "kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'")
    log_info "Pod: ${POD_NAME}"
    
    # 验证 VERSION 文件
    VERSION_IN_POD=$(ssh -p ${SSH_PORT} ${SERVER} "kubectl exec -n ${NAMESPACE} ${POD_NAME} -- cat /opt/llm-gateway-go/VERSION 2>/dev/null || cat /.VERSION 2>/dev/null")
    log_info "容器内版本: ${VERSION_IN_POD}"
    
    if [[ "${VERSION_IN_POD}" == "${IMAGE_TAG}" ]]; then
        log_success "版本验证通过"
    else
        log_warn "版本不匹配: 期望 ${IMAGE_TAG}, 实际 ${VERSION_IN_POD}"
    fi
}

# ==================== 步骤7: 清理过期镜像 ====================
cleanup_old_images() {
    log_step "步骤 7/8: 清理过期镜像"
    
    log_info "在184服务器上清理过期镜像（>${OLD_IMAGE_DAYS}天）..."
    
    ssh -p ${SSH_PORT} ${SERVER} bash <<'REMOTE_SCRIPT'
set -e

ARCHIVE_DIR="/opt/ready-to-delete"
mkdir -p ${ARCHIVE_DIR}

echo "清理 Docker dangling 镜像..."
docker image prune -f

echo "查找过期的 kx-llm-gateway-go 镜像..."
OLD_IMAGES=$(docker images kx-llm-gateway-go --format "{{.Repository}}:{{.Tag}}" | grep -v "latest" | tail -n +6 || true)

if [[ -n "${OLD_IMAGES}" ]]; then
    echo "${OLD_IMAGES}" | while read img; do
        echo "  标记删除: ${img}"
        echo "${img}" >> ${ARCHIVE_DIR}/deleted-images-$(date +%Y%m%d).log
        docker rmi ${img} 2>/dev/null || true
    done
    echo "过期镜像已删除"
else
    echo "没有过期镜像需要清理"
fi

echo "清理完成"
ls -lh ${ARCHIVE_DIR}/ | tail -5 || true
REMOTE_SCRIPT
    
    log_success "过期镜像清理完成"
}

# ==================== 步骤8: 生成部署报告 ====================
generate_report() {
    log_step "步骤 8/8: 生成部署报告"
    
    REPORT_FILE="deployment-report-$(date +%Y%m%d-%H%M%S).md"
    
    cat > ${REPORT_FILE} <<EOF
# 184环境部署报告

## 部署信息
- **部署时间**: $(date '+%Y-%m-%d %H:%M:%S')
- **部署环境**: 184 (14.103.112.184)
- **命名空间**: ${NAMESPACE}
- **部署名称**: ${DEPLOYMENT}
- **操作人员**: $(whoami)

## 版本信息
- **Git Tag**: ${GIT_TAG}
- **Git SHA**: ${GIT_SHA}
- **Build Seq**: ${NEW_BUILD_SEQ}
- **Build Date**: ${BUILD_DATE}
- **镜像标签**: ${IMAGE_TAG}

## 镜像信息
- **镜像名称**: ${IMAGE_NAME}:${IMAGE_TAG}
- **内部Registry**: ${REGISTRY_INTERNAL}/${IMAGE_NAME}:${IMAGE_TAG}
- **本地Registry**: ${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG}

## Git提交信息
\`\`\`
$(git log -1 --oneline)
\`\`\`

**最近3次提交**:
\`\`\`
$(git log -3 --oneline)
\`\`\`

## 健康检查结果
\`\`\`json
$(ssh -p ${SSH_PORT} ${SERVER} "curl -s ${HEALTH_ENDPOINT}" 2>/dev/null | jq '.' || echo "{}")
\`\`\`

## Pod状态
\`\`\`
$(ssh -p ${SSH_PORT} ${SERVER} "kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go" 2>/dev/null)
\`\`\`

## 验证命令
- 查看日志: \`ssh -p ${SSH_PORT} ${SERVER} "kubectl logs -n ${NAMESPACE} -l app=llm-gateway-go -f"\`
- 查看Pod: \`ssh -p ${SSH_PORT} ${SERVER} "kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go"\`
- 健康检查: \`ssh -p ${SSH_PORT} ${SERVER} "curl -s ${HEALTH_ENDPOINT} | jq ."\`

## 回滚命令（如需要）
\`\`\`bash
ssh -p ${SSH_PORT} ${SERVER} "kubectl rollout undo deployment/${DEPLOYMENT} -n ${NAMESPACE}"
\`\`\`

---
*报告生成时间: $(date '+%Y-%m-%d %H:%M:%S')*
EOF
    
    log_success "部署报告已生成: ${REPORT_FILE}"
    echo ""
    cat ${REPORT_FILE}
}

# ==================== 主流程 ====================
main() {
    echo ""
    log_step "开始部署到184环境"
    
    check_uncommitted_changes
    get_version_info
    build_docker_image
    push_docker_image
    update_k8s_deployment
    health_check
    cleanup_old_images
    generate_report
    
    echo ""
    log_success "=========================================="
    log_success "部署完成！"
    log_success "=========================================="
    echo ""
    log_info "快速验证命令:"
    echo "  curl http://14.103.112.184:30080/health | jq ."
    echo "  ssh -p ${SSH_PORT} ${SERVER} 'kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go'"
    echo "  ssh -p ${SSH_PORT} ${SERVER} 'kubectl logs -n ${NAMESPACE} -l app=llm-gateway-go --tail=50'"
    echo ""
}

# 执行主流程
main
