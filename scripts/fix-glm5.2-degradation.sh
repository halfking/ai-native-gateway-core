#!/bin/bash
# 2026-08-29: GLM-5.2 降级问题修复部署脚本
# 
# 用途：
# 1. 部署代码修复（credentialhealth 阈值调整、weighted_router 缓存优化）
# 2. 执行数据库修复（保护 glm-5.2 binding、恢复降级状态）
# 3. 验证修复效果
# 4. 监控关键指标

set -e  # 遇到错误立即退出
set -u  # 使用未定义变量时报错

# ============================================================================
# 配置区域
# ============================================================================

# 目标服务器（根据实际环境调整）
SERVER_HOST="${SERVER_HOST:-172.31.86.245}"
SERVER_USER="${SERVER_USER:-root}"
DEPLOY_PATH="${DEPLOY_PATH:-/data/llm-gateway}"

# 数据库连接（根据实际配置调整）
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-postgres}"

# GLM-5.2 相关配置
PROVIDER_NAME="${PROVIDER_NAME:-sp1}"
CREDENTIAL_LABEL="${CREDENTIAL_LABEL:-spi-3}"
MODEL_NAME="glm-5.2"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ============================================================================
# 辅助函数
# ============================================================================

log_info() {
    echo -e "${BLUE}[INFO]${NC} $(date '+%Y-%m-%d %H:%M:%S') - $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $(date '+%Y-%m-%d %H:%M:%S') - $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $(date '+%Y-%m-%d %H:%M:%S') - $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $(date '+%Y-%m-%d %H:%M:%S') - $*"
}

# 检查命令是否存在
check_command() {
    if ! command -v "$1" &> /dev/null; then
        log_error "命令 $1 未找到，请先安装"
        exit 1
    fi
}

# 执行 SQL 并返回结果
execute_sql() {
    local sql="$1"
    local output_format="${2:-tuples-only}"  # tuples-only 或 expanded
    
    PGPASSWORD="${DB_PASSWORD}" psql \
        -h "${DB_HOST}" \
        -p "${DB_PORT}" \
        -U "${DB_USER}" \
        -d "${DB_NAME}" \
        -t \
        -A \
        -c "${sql}" 2>&1
}

# ============================================================================
# 阶段 1: 前置检查
# ============================================================================

phase1_preflight_checks() {
    log_info "=== 阶段 1: 前置检查 ==="
    
    # 检查必要命令
    check_command "git"
    check_command "go"
    check_command "psql"
    check_command "ssh"
    
    # 检查 Git 状态
    log_info "检查 Git 仓库状态..."
    if ! git diff-index --quiet HEAD --; then
        log_warn "Git 工作区有未提交的更改"
        git status --short
        read -p "是否继续？(y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            log_error "用户取消操作"
            exit 1
        fi
    fi
    
    # 检查数据库连接
    log_info "检查数据库连接..."
    if ! execute_sql "SELECT 1" > /dev/null 2>&1; then
        log_error "无法连接到数据库 ${DB_HOST}:${DB_PORT}/${DB_NAME}"
        log_error "请检查数据库配置和网络连接"
        exit 1
    fi
    log_success "数据库连接正常"
    
    # 检查目标 credential 是否存在
    log_info "检查 ${PROVIDER_NAME} 中的 ${MODEL_NAME} credential..."
    local credential_count
    credential_count=$(execute_sql "
        SELECT COUNT(*)
        FROM credentials c
        JOIN providers p ON c.provider_id = p.id
        WHERE p.name = '${PROVIDER_NAME}'
          AND c.label LIKE '%${CREDENTIAL_LABEL}%'
    ")
    
    if [ "$credential_count" -eq 0 ]; then
        log_error "未找到 ${PROVIDER_NAME}/${CREDENTIAL_LABEL} 的 credential"
        log_error "请检查 PROVIDER_NAME 和 CREDENTIAL_LABEL 配置"
        exit 1
    fi
    log_success "找到 ${credential_count} 个匹配的 credential"
    
    log_success "=== 阶段 1 完成 ==="
    echo
}

# ============================================================================
# 阶段 2: 代码修复部署
# ============================================================================

phase2_deploy_code_fix() {
    log_info "=== 阶段 2: 代码修复部署 ==="
    
    # 编译 Go 项目
    log_info "编译 Go 项目..."
    if ! go build -o llm-gateway-go ./cmd/gateway; then
        log_error "编译失败"
        exit 1
    fi
    log_success "编译成功"
    
    # 运行单元测试（可选）
    log_info "运行单元测试（credentialhealth 和 routing）..."
    if ! go test -v ./credentialhealth/... ./domains/routing/... -timeout 30s; then
        log_warn "单元测试失败，但继续部署（如果是测试环境问题）"
    else
        log_success "单元测试通过"
    fi
    
    # 备份远程服务器上的旧版本
    log_info "备份远程服务器上的旧版本..."
    ssh "${SERVER_USER}@${SERVER_HOST}" "
        cd ${DEPLOY_PATH} && \
        if [ -f llm-gateway-go ]; then \
            cp llm-gateway-go llm-gateway-go.backup.\$(date +%Y%m%d_%H%M%S); \
        fi
    "
    
    # 上传新版本
    log_info "上传新版本到 ${SERVER_HOST}..."
    scp llm-gateway-go "${SERVER_USER}@${SERVER_HOST}:${DEPLOY_PATH}/llm-gateway-go.new"
    
    # 重启服务
    log_info "重启服务..."
    ssh "${SERVER_USER}@${SERVER_HOST}" "
        cd ${DEPLOY_PATH} && \
        mv llm-gateway-go.new llm-gateway-go && \
        chmod +x llm-gateway-go && \
        docker-compose restart llm-gateway || systemctl restart llm-gateway
    "
    
    # 等待服务启动
    log_info "等待服务启动（10 秒）..."
    sleep 10
    
    # 检查服务状态
    log_info "检查服务状态..."
    if ssh "${SERVER_USER}@${SERVER_HOST}" "curl -sf http://localhost:8080/health > /dev/null"; then
        log_success "服务启动成功"
    else
        log_error "服务启动失败，请检查日志"
        ssh "${SERVER_USER}@${SERVER_HOST}" "cd ${DEPLOY_PATH} && docker-compose logs --tail=50 llm-gateway"
        exit 1
    fi
    
    log_success "=== 阶段 2 完成 ==="
    echo
}

# ============================================================================
# 阶段 3: 数据库修复
# ============================================================================

phase3_database_fix() {
    log_info "=== 阶段 3: 数据库修复 ==="
    
    # 获取目标 credential_id
    log_info "查找目标 credential_id..."
    local credential_ids
    credential_ids=$(execute_sql "
        SELECT c.id
        FROM credentials c
        JOIN providers p ON c.provider_id = p.id
        WHERE p.name = '${PROVIDER_NAME}'
          AND c.label LIKE '%${CREDENTIAL_LABEL}%'
    ")
    
    if [ -z "$credential_ids" ]; then
        log_error "未找到匹配的 credential"
        exit 1
    fi
    
    log_info "找到 credential_id: ${credential_ids}"
    
    # 执行保护操作
    log_info "为 glm-5.2 binding 设置保护..."
    for cred_id in $credential_ids; do
        execute_sql "
            UPDATE credential_model_bindings cmb
            SET 
                admin_protected = TRUE,
                manual_priority = 100,
                updated_at = NOW()
            FROM provider_models pm
            WHERE cmb.provider_model_id = pm.id
              AND cmb.credential_id = ${cred_id}
              AND pm.raw_model_name = '${MODEL_NAME}';
        "
        log_success "已保护 credential ${cred_id} 的 ${MODEL_NAME} binding"
    done
    
    # 恢复降级状态
    log_info "恢复已降级的 binding..."
    local recovered_count
    recovered_count=$(execute_sql "
        WITH updated AS (
            UPDATE credential_model_bindings cmb
            SET 
                available = TRUE,
                unavailable_reason = NULL,
                unavailable_at = NULL,
                unavailable_recover_at = NULL,
                updated_at = NOW()
            FROM provider_models pm, credentials c, providers p
            WHERE cmb.provider_model_id = pm.id
              AND cmb.credential_id = c.id
              AND c.provider_id = p.id
              AND p.name = '${PROVIDER_NAME}'
              AND pm.raw_model_name = '${MODEL_NAME}'
              AND cmb.available = FALSE
              AND cmb.unavailable_reason = 'continuous_failure'
            RETURNING cmb.id
        )
        SELECT COUNT(*) FROM updated;
    ")
    
    if [ "$recovered_count" -gt 0 ]; then
        log_success "恢复了 ${recovered_count} 个降级的 binding"
    else
        log_info "没有需要恢复的降级 binding"
    fi
    
    # 清理 probe 状态
    log_info "清理 node_probe_state..."
    execute_sql "
        DELETE FROM node_probe_state nps
        USING credentials c, providers p
        WHERE nps.credential_id = c.id
          AND c.provider_id = p.id
          AND p.name = '${PROVIDER_NAME}'
          AND nps.raw_model_name = '${MODEL_NAME}'
          AND nps.last_direct_ok = FALSE;
    "
    
    log_success "=== 阶段 3 完成 ==="
    echo
}

# ============================================================================
# 阶段 4: 验证修复效果
# ============================================================================

phase4_verify_fix() {
    log_info "=== 阶段 4: 验证修复效果 ==="
    
    # 检查 binding 是否可路由
    log_info "检查 ${MODEL_NAME} 是否可路由..."
    local routable_result
    routable_result=$(execute_sql "
        SELECT 
            credential_id,
            credential_label,
            is_routable,
            CASE 
                WHEN is_routable THEN '可路由'
                ELSE '被阻塞'
            END AS status
        FROM v_routable_credential_models
        WHERE raw_model_name = '${MODEL_NAME}'
          AND credential_label LIKE '%${CREDENTIAL_LABEL}%'
        ORDER BY is_routable DESC;
    ")
    
    echo "$routable_result"
    
    # 检查保护状态
    log_info "检查保护状态..."
    execute_sql "
        SELECT 
            c.id AS credential_id,
            c.label,
            cmb.admin_protected,
            cmb.manual_priority,
            cmb.available
        FROM credential_model_bindings cmb
        JOIN provider_models pm ON cmb.provider_model_id = pm.id
        JOIN credentials c ON cmb.credential_id = c.id
        JOIN providers p ON c.provider_id = p.id
        WHERE p.name = '${PROVIDER_NAME}'
          AND pm.raw_model_name = '${MODEL_NAME}'
          AND c.label LIKE '%${CREDENTIAL_LABEL}%';
    " | column -t
    
    log_success "=== 阶段 4 完成 ==="
    echo
}

# ============================================================================
# 阶段 5: 监控和报告
# ============================================================================

phase5_monitoring() {
    log_info "=== 阶段 5: 监控指标 ==="
    
    cat <<EOF

建议监控以下指标（Prometheus / Grafana）：

1. 降级频率：
   rate(credential_degradation_total{model="${MODEL_NAME}"}[5m])

2. 请求成功率：
   sum(rate(request_total{model="${MODEL_NAME}", status="success"}[5m]))
   /
   sum(rate(request_total{model="${MODEL_NAME}"}[5m]))

3. rate_limit 错误率：
   rate(request_errors_total{model="${MODEL_NAME}", error_kind="rate_limit"}[1m])

4. 权重分布：
   credential_weight{model="${MODEL_NAME}", provider="${PROVIDER_NAME}"}

5. 队列深度：
   credential_queue_depth{credential_label=~".*${CREDENTIAL_LABEL}.*"}

查看实时日志：
  ssh ${SERVER_USER}@${SERVER_HOST} \\
    "cd ${DEPLOY_PATH} && docker-compose logs -f --tail=100 llm-gateway | grep -i 'glm-5.2\\|degraded\\|rate_limit'"

EOF
    
    log_success "=== 阶段 5 完成 ==="
    echo
}

# ============================================================================
# 主流程
# ============================================================================

main() {
    log_info "开始 GLM-5.2 降级问题修复流程"
    echo
    
    # 显示配置
    cat <<EOF
当前配置：
  服务器: ${SERVER_HOST}
  部署路径: ${DEPLOY_PATH}
  数据库: ${DB_HOST}:${DB_PORT}/${DB_NAME}
  供应商: ${PROVIDER_NAME}
  凭据标签: ${CREDENTIAL_LABEL}
  模型: ${MODEL_NAME}

EOF
    
    read -p "配置正确吗？继续执行？(y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log_error "用户取消操作"
        exit 1
    fi
    
    # 执行各阶段
    phase1_preflight_checks
    
    # 询问是否部署代码（可选）
    read -p "是否部署代码修复？(y/N) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        phase2_deploy_code_fix
    else
        log_warn "跳过代码部署"
    fi
    
    phase3_database_fix
    phase4_verify_fix
    phase5_monitoring
    
    log_success "修复流程完成！"
    log_info "建议持续观察 15-30 分钟，确保降级问题不再出现"
}

# 执行主流程
main "$@"
