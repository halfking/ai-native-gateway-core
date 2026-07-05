#!/bin/bash
# deploy-184.sh — 标准化184环境部署脚本（统一入口）
#
# 模式:
#   (默认)           标准 10 步部署（check → version → build → push → K8s → health → report）
#   --with-migration 部署 + DB migration + build_seq 提交
#   --after-local-test 本地测试 → 部署 + migration（端到端）
#   --columnar       columnar 增量二进制构建部署
#   --quick          旧版 sshpass 快速部署（已废弃，仅保留参考）
#
# 用法:
#   ./deploy-184.sh                              # 标准部署
#   ./deploy-184.sh --with-migration             # 含 migration
#   ./deploy-184.sh --after-local-test           # 本地验证后部署
#   SKIP_LOCAL_TEST=1 ./deploy-184.sh -m         # 含 migration，跳过本地测试（仅 --after-local-test 模式）
#   ./deploy-184.sh --columnar                   # columnar 增量部署

set -euo pipefail

# ==================== 配置区 ====================
SERVER="root@14.103.112.184"
SERVER_IP="14.103.112.184"
SSH_PORT="25022"
NAMESPACE="pms-test"
DEPLOYMENT="llm-gateway-go-deployment"
IMAGE_NAME="kx-llm-gateway-go"
REGISTRY_INTERNAL="registry.kxpms.cn"
REGISTRY_LOCAL="127.0.0.1:5000"
HEALTH_ENDPOINT="http://localhost:30080/health"
OLD_IMAGE_DAYS=30
REPO_DIR="${LLM_GATEWAY_REPO:-$(cd "$(dirname "$0")" && pwd)}"

# ==================== 颜色输出 ====================
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BLUE='\033[0;34m'; NC='\033[0m'
log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[✓]${NC} $1"; }
log_warn()    { echo -e "${YELLOW}[⚠]${NC} $1"; }
log_error()   { echo -e "${RED}[✗]${NC} $1"; }
log_step()    { echo ""; echo -e "${GREEN}========================================${NC}"; echo -e "${GREEN}$1${NC}"; echo -e "${GREEN}========================================${NC}"; }

# ==================== 参数解析 ====================
MODE="standard"
SKIP_LOCAL_TEST="${SKIP_LOCAL_TEST:-0}"
SKIP_BUILD_SEQ_COMMIT="${SKIP_BUILD_SEQ_COMMIT:-0}"
RECORD_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --with-migration|-m)  MODE="with-migration"; shift ;;
    --after-local-test|-l) MODE="after-local-test"; shift ;;
    --columnar|-c)        MODE="columnar"; shift ;;
    --quick|-q)           MODE="quick"; shift ;;
    --record|-r)          MODE="record"; shift ;;
    --help|-h)            echo "用法: $0 [--with-migration|--after-local-test|--columnar|--record]"; exit 0 ;;
    *)                    log_error "未知参数: $1"; exit 1 ;;
  esac
done

cd "$REPO_DIR"

# ==================== 快速模式（废弃）====================
quick_deploy() {
  log_warn "quick-deploy 模式已废弃（含硬编码密码，安全风险）"
  log_info "请使用标准模式: ./deploy-184.sh [--with-migration]"
  exit 1
}

# ==================== 标准部署步骤 ====================
check_uncommitted_changes() {
    log_step "步骤 1/10: 检查未提交改动"
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

get_version_info() {
    log_step "步骤 2/10: 获取版本信息"
    GIT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    GIT_SHA=$(git rev-parse --short=8 HEAD)
    BUILD_DATE=$(date +%Y%m%d)

    if [[ -f "build_seq" ]]; then
        BUILD_SEQ=$(cat build_seq)
    else
        BUILD_SEQ=0
        log_warn "build_seq 文件不存在，初始化为 0"
    fi
    NEW_BUILD_SEQ=$((BUILD_SEQ + 1))
    echo "${NEW_BUILD_SEQ}" > build_seq
    IMAGE_TAG="${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${NEW_BUILD_SEQ}"

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

    if [[ "$MODE" == "record" ]]; then
      log_info "初始化部署记录目录..."
      if BUILD_SEQ="$NEW_BUILD_SEQ" bash "$REPO_DIR/scripts/init-deploy-record.sh"; then
        SEQ_PAD=$(printf "%03d" "$NEW_BUILD_SEQ")
        RECORD_DIR="$REPO_DIR/deploy/r${SEQ_PAD}-${BUILD_DATE}"
        log_success "部署记录目录: $RECORD_DIR"
      else
        log_warn "init-deploy-record.sh 失败，跳过记录"
      fi
    fi
}

build_docker_image() {
    log_step "步骤 3/10: 构建Docker镜像"
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

push_docker_image() {
    log_step "步骤 4/10: 推送镜像到Registry"
    log_info "推送到内部 registry: ${REGISTRY_INTERNAL}"
    docker tag ${IMAGE_NAME}:${IMAGE_TAG} ${REGISTRY_INTERNAL}/${IMAGE_NAME}:${IMAGE_TAG}
    docker push ${REGISTRY_INTERNAL}/${IMAGE_NAME}:${IMAGE_TAG}

    log_info "推送到184本地 registry: ${REGISTRY_LOCAL}"
    docker tag ${IMAGE_NAME}:${IMAGE_TAG} ${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG}
    docker push ${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG}
}

show_deploying_page() {
    log_step "步骤 5/10: 显示部署中页面"
    DEPLOY_MSG="${DEPLOY_MSG:-系统升级与优化}"
    DEPLOY_ETA="${DEPLOY_ETA:-约 1-2 分钟}"

    if [[ ! -f web/dist/index-deploying.html ]]; then
        log_warn "web/dist/index-deploying.html 不存在，跳过"
        return 0
    fi

    ssh -p ${SSH_PORT} ${SERVER} bash <<'BACKUP_HTML'
set -e
REMOTE_WEB="/opt/llm-gateway-go/web"
BAK_FILE="${REMOTE_WEB}/index.html.bak.$(date +%s)"
if [[ -f "${REMOTE_WEB}/index.html" ]]; then
    cp "${REMOTE_WEB}/index.html" "${BAK_FILE}"
    echo "Backup: ${BAK_FILE}"
fi
BACKUP_HTML

    scp -P ${SSH_PORT} web/dist/index-deploying.html \
      ${SERVER}:/opt/llm-gateway-go/web/index.html
    log_success "部署中页面已显示 (msg=${DEPLOY_MSG}, eta=${DEPLOY_ETA})"
}

update_k8s_deployment() {
    log_step "步骤 6/10: 更新Kubernetes部署"
    ssh -p ${SSH_PORT} ${SERVER} "kubectl set image deployment/${DEPLOYMENT} \
        llm-gateway-go=${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG} -n ${NAMESPACE}"
    ssh -p ${SSH_PORT} ${SERVER} "kubectl rollout status deployment/${DEPLOYMENT} -n ${NAMESPACE} --timeout=5m"
    log_success "Kubernetes 部署更新完成"
}

restore_normal_page() {
    log_step "步骤 7/10: 恢复页面"
    ssh -p ${SSH_PORT} ${SERVER} bash <<'RESTORE_HTML'
set -e
REMOTE_WEB="/opt/llm-gateway-go/web"
BAK_FILE=$(ls -t $REMOTE_WEB/index.html.bak.* 2>/dev/null | head -1)
if [[ -n "$BAK_FILE" ]]; then
    cp "$BAK_FILE" "$REMOTE_WEB/index.html"
    echo "Restored from: $BAK_FILE"
    rm -f "$BAK_FILE"
else
    echo "No backup found, keeping deploying page"
fi
RESTORE_HTML
    log_success "页面已恢复"
}

health_check() {
    log_step "步骤 8/10: 健康检查"
    sleep 10
    log_info "Pod 状态:"
    ssh -p ${SSH_PORT} ${SERVER} "kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go"
    log_info "健康检查:"
    HEALTH_RESPONSE=$(ssh -p ${SSH_PORT} ${SERVER} "curl -s ${HEALTH_ENDPOINT}" || echo "{}")
    echo "${HEALTH_RESPONSE}" | jq '.' 2>/dev/null || echo "${HEALTH_RESPONSE}"
    log_info "验证容器内版本信息..."
    POD_NAME=$(ssh -p ${SSH_PORT} ${SERVER} "kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'")
    log_info "Pod: ${POD_NAME}"
    VERSION_IN_POD=$(ssh -p ${SSH_PORT} ${SERVER} "kubectl exec -n ${NAMESPACE} ${POD_NAME} -- cat /opt/llm-gateway-go/VERSION 2>/dev/null || cat /.VERSION 2>/dev/null")
    if [[ "${VERSION_IN_POD}" == "${IMAGE_TAG}" ]]; then
        log_success "版本验证通过"
    else
        log_warn "版本不匹配: 期望 ${IMAGE_TAG}, 实际 ${VERSION_IN_POD}"
    fi
}

cleanup_old_images() {
    log_step "步骤 9/10: 清理过期镜像"
    ssh -p ${SSH_PORT} ${SERVER} bash <<'REMOTE_SCRIPT'
set -e
ARCHIVE_DIR="/opt/ready-to-delete"
mkdir -p ${ARCHIVE_DIR}
docker image prune -f
OLD_IMAGES=$(docker images kx-llm-gateway-go --format "{{.Repository}}:{{.Tag}}" | grep -v "latest" | tail -n +6 || true)
if [[ -n "${OLD_IMAGES}" ]]; then
    echo "${OLD_IMAGES}" | while read img; do
        echo "  标记删除: ${img}"
        echo "${img}" >> ${ARCHIVE_DIR}/deleted-images-$(date +%Y%m%d).log
        docker rmi ${img} 2>/dev/null || true
    done
fi
REMOTE_SCRIPT
    log_success "过期镜像清理完成"
}

generate_report() {
    log_step "步骤 10/10: 生成部署报告"
    REPORT_FILE="deployment-report-$(date +%Y%m%d-%H%M%S).md"
    if [[ -n "$RECORD_DIR" ]]; then
      REPORT_FILE="$RECORD_DIR/deploy-report.md"
      # 更新 README.md 状态
      if [[ -f "$RECORD_DIR/README.md" ]]; then
        sed -i '' "s/状态.*/状态 | **✅ 已部署** |/" "$RECORD_DIR/README.md" 2>/dev/null || true
      fi
      # 写镜像标签到 artifacts
      echo "${REGISTRY_INTERNAL}/${IMAGE_NAME}:${IMAGE_TAG}" > "$RECORD_DIR/artifacts/docker-image.txt"
    fi
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

## 回滚命令（如需要）
\`\`\`bash
ssh -p ${SSH_PORT} ${SERVER} "kubectl rollout undo deployment/${DEPLOYMENT} -n ${NAMESPACE}"
\`\`\`

---
*报告生成时间: $(date '+%Y-%m-%d %H:%M:%S')*
EOF
    log_success "部署报告已生成: ${REPORT_FILE}"
    cat ${REPORT_FILE}
}

# ==================== DB Migration 阶段 ====================
run_db_migration() {
    log_step "阶段: 执行 DB migration"
    RUN_MIG_SCRIPT="$HOME/.agents/skills/deploy-184/scripts/run-migrations.sh"
    if [[ -x "$RUN_MIG_SCRIPT" ]]; then
        bash "$RUN_MIG_SCRIPT" || { log_error "DB migration 失败"; exit 1; }
    else
        log_info "run-migrations.sh 不存在，跳过 migration"
    fi
    log_success "DB migration 完成"
}

restart_pod() {
    log_step "阶段: 重启 Pod 使新 schema 生效"
    ssh -p ${SSH_PORT} ${SERVER} \
      "kubectl rollout restart deployment/llm-gateway-go-deployment -n pms-test"
    ssh -p ${SSH_PORT} ${SERVER} \
      "kubectl rollout status deployment/llm-gateway-go-deployment -n pms-test --timeout=3m"
    log_success "Pod 重启完成"
}

commit_build_seq() {
    log_step "阶段: 提交 build_seq"
    if [[ "$SKIP_BUILD_SEQ_COMMIT" != "1" ]]; then
        CHANGED=$(git status --short | grep -E "build_seq|version.json" | wc -l | tr -d ' ')
        if [[ ${CHANGED} -gt 0 ]]; then
            NEW_BUILD_SEQ=$(cat build_seq 2>/dev/null || echo "unknown")
            git add build_seq version.json
            git commit -m "chore: bump build_seq to ${NEW_BUILD_SEQ} after 184 deploy"
            git push
            log_success "build_seq 已提交"
        else
            log_info "build_seq 无改动"
        fi
    else
        log_info "跳过 build_seq 提交"
    fi
}

# ==================== 部署后验证（record 模式）====================
run_record_verify() {
    log_step "阶段: 部署后验证"
    local verify_args="--env 184"
    if [[ -n "$RECORD_DIR" ]]; then
      verify_args="$verify_args --record $RECORD_DIR"
    fi
    if "$REPO_DIR/deploy/verify.sh" $verify_args; then
        log_success "部署后验证全部通过"
        if [[ -n "$RECORD_DIR" ]]; then
          echo "验证通过" > "$RECORD_DIR/verify/result.txt"
        fi
    else
        log_error "部署后验证有失败项"
        if [[ -n "$RECORD_DIR" ]]; then
          echo "验证失败" > "$RECORD_DIR/verify/result.txt"
        fi
        exit 1
    fi
}

# ==================== Columnar 增量部署 ====================
columnar_deploy() {
    log_step "Columnar 增量部署到 184"

    [[ -f llm-gateway-go-linux-amd64 ]] || { log_error "Binary (llm-gateway-go-linux-amd64) 缺失"; exit 1; }
    [[ -f VERSION ]] || { log_error "VERSION 文件缺失"; exit 1; }

    SSH_KEY="$HOME/.ssh/56_id_rsa"
    SSH_CMD="-p $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"
    SCP_CMD="-P $SSH_PORT -i $SSH_KEY -o StrictHostKeyChecking=no -o BatchMode=yes"
    LOCAL_TAG="columnar-$(date +%Y%m%d-%H%M%S)"

    log_info "1. 准备远程构建目录..."
    ssh $SSH_CMD "root@$SERVER_IP" bash -s <<'REMOTE'
set -e
rm -rf /tmp/columnar-deploy && mkdir -p /tmp/columnar-deploy/build
REMOTE

    log_info "2. 推送 binary + Dockerfile.incremental..."
    scp $SCP_CMD llm-gateway-go-linux-amd64 "root@$SERVER_IP":/tmp/columnar-deploy/build/llm-gateway-go
    scp $SCP_CMD VERSION .deploy_seq "root@$SERVER_IP":/tmp/columnar-deploy/build/ 2>/dev/null || true
    scp $SCP_CMD Dockerfile.incremental "root@$SERVER_IP":/tmp/columnar-deploy/build/

    log_info "3. 在 184 上构建镜像..."
    ssh $SSH_CMD "root@$SERVER_IP" bash <<REMOTE
set -e
cd /tmp/columnar-deploy/build
docker build \
    --build-arg BASE_IMAGE=kx-llm-gateway-go:latest \
    -t ${REGISTRY_LOCAL}/kx-llm-gateway-go:${LOCAL_TAG} \
    -t ${REGISTRY_LOCAL}/kx-llm-gateway-go:columnar-latest \
    -f Dockerfile.incremental .
docker push ${REGISTRY_LOCAL}/kx-llm-gateway-go:${LOCAL_TAG}
kubectl -n ${NAMESPACE} set image deployment/${DEPLOYMENT} \
    llm-gateway-go=${REGISTRY_LOCAL}/kx-llm-gateway-go:${LOCAL_TAG}
kubectl -n ${NAMESPACE} rollout status deployment/${DEPLOYMENT} --timeout=2m
REMOTE

    log_info "4. 验证..."
    sleep 5
    ssh $SSH_CMD "root@$SERVER_IP" bash <<'REMOTE'
set -e
POD=$(kubectl -n pms-test get pods -l app=llm-gateway-go \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')
echo "Pod: $POD"
echo "Image: $(kubectl -n pms-test get pod $POD -o jsonpath='{.spec.containers[0].image}')"
kubectl -n pms-test logs "$POD" --tail=200 2>/dev/null | grep -E "columnar" || echo "(no columnar log lines yet)"
REMOTE

    log_success "Columnar 部署完成"
}

# ==================== 本地验证阶段 ====================
run_local_test() {
    log_step "阶段: 本地验证"
    VERIFY_SCRIPT="$HOME/.agents/skills/local-deploy-test/scripts/verify-all.sh"
    if [[ ! -x "$VERIFY_SCRIPT" ]]; then
        log_error "本地验证脚本不存在: $VERIFY_SCRIPT"
        exit 1
    fi
    SKIP_COLUMNAR=1 bash "$VERIFY_SCRIPT" || {
        log_error "本地验证失败，阻止部署到 184"
        exit 1
    }
    log_success "本地验证通过"
}

# ==================== 标准部署流程 ====================
standard_deploy_flow() {
    check_uncommitted_changes
    get_version_info
    build_docker_image
    push_docker_image
    show_deploying_page
    update_k8s_deployment
    restore_normal_page
    health_check
    cleanup_old_images
    generate_report
}

# ==================== 主流程 ====================
main() {
    START_TIME=$(date +%s)

    case "$MODE" in
        quick)
            quick_deploy
            ;;
        columnar)
            columnar_deploy
            ;;
        after-local-test)
            log_step "端到端部署: 本地测试 → 184 部署（含 migration）"
            if [[ "$SKIP_LOCAL_TEST" != "1" ]]; then
                run_local_test
            else
                log_info "SKIP_LOCAL_TEST=1，跳过本地测试"
            fi
            standard_deploy_flow
            run_db_migration
            restart_pod
            commit_build_seq
            ;;
        with-migration)
            log_step "184 部署（含 DB migration）"
            standard_deploy_flow
            run_db_migration
            restart_pod
            commit_build_seq
            ;;
        record)
            log_step "记录模式部署: 标准部署 + migration + 验证"
            standard_deploy_flow
            run_db_migration
            restart_pod
            run_record_verify
            commit_build_seq
            ;;
        standard)
            standard_deploy_flow
            ;;
    esac

    END_TIME=$(date +%s)
    ELAPSED=$((END_TIME - START_TIME))

    echo ""
    log_success "=========================================="
    log_success "部署完成！耗时 ${ELAPSED}s"
    log_success "=========================================="
    echo ""
    log_info "快速验证命令:"
    echo "  curl http://14.103.112.184:30080/health | jq ."
    echo "  ssh -p ${SSH_PORT} ${SERVER} 'kubectl get pods -n ${NAMESPACE} -l app=llm-gateway-go'"
    echo "  ssh -p ${SSH_PORT} ${SERVER} 'kubectl logs -n ${NAMESPACE} -l app=llm-gateway-go --tail=50'"
    echo ""
}

main
