#!/bin/bash
# LLM Gateway 本地部署脚本
# 用途: 快速设置本地开发/测试环境

set -e

# ============================================
# 配置
# ============================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_FILE="/tmp/llm-gateway.log"
PID_FILE="/tmp/llm-gateway.pid"
ENV_FILE="/tmp/llm-gateway-test.env"

# 数据库配置 (默认值)
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5434}"
DB_NAME="${DB_NAME:-redclaw}"
DB_USER="${DB_USER:-redclaw}"
DB_PASSWORD="${DB_PASSWORD:-redclaw}"

# Redis 配置
REDIS_HOST="${REDIS_HOST:-localhost}"
REDIS_PORT="${REDIS_PORT:-6379}"

# 服务配置
SERVICE_PORT="${SERVICE_PORT:-8781}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin123}"

# ============================================
# 颜色输出
# ============================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

success() {
    echo -e "${GREEN}[✓]${NC} $1"
}

warning() {
    echo -e "${YELLOW}[⚠]${NC} $1"
}

error() {
    echo -e "${RED}[✗]${NC} $1"
}

# ============================================
# 函数
# ============================================

check_requirements() {
    info "检查依赖..."
    
    # 检查 Go
    if ! command -v go &> /dev/null; then
        error "Go 未安装，请先安装 Go 1.21+"
        exit 1
    fi
    success "Go $(go version | awk '{print $3}')"
    
    # 检查 psql
    if ! command -v psql &> /dev/null; then
        warning "psql 未安装，数据库操作可能失败"
    else
        success "PostgreSQL client $(psql --version | awk '{print $3}')"
    fi
    
    # 检查 Docker (可选)
    if command -v docker &> /dev/null; then
        success "Docker $(docker --version | awk '{print $3}' | sed 's/,//')"
    else
        warning "Docker 未安装，无法自动启动数据库"
    fi
}

check_database() {
    info "检查数据库连接..."
    
    if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1" &> /dev/null; then
        success "数据库连接成功: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
        return 0
    else
        error "数据库连接失败"
        warning "请确保 PostgreSQL 正在运行:"
        echo "  docker ps | grep postgres"
        echo "或设置环境变量:"
        echo "  export DB_HOST=your_host DB_PORT=5432 DB_NAME=your_db DB_USER=your_user DB_PASSWORD=your_pass"
        return 1
    fi
}

run_migrations() {
    info "运行数据库迁移..."
    
    local migration_dir="$PROJECT_ROOT/sql/migrations/startup"
    
    if [ ! -d "$migration_dir" ]; then
        error "迁移目录不存在: $migration_dir"
        return 1
    fi
    
    # 检查关键迁移文件
    local critical_migrations=(
        "001_users_table.sql"
        "006_tenants_table.sql"
        "360_session_module_executions.sql"
        "361_dashboard_access_events.sql"
    )
    
    local all_exist=true
    for migration in "${critical_migrations[@]}"; do
        if [ ! -f "$migration_dir/$migration" ]; then
            warning "迁移文件不存在: $migration"
            all_exist=false
        fi
    done
    
    if [ "$all_exist" = false ]; then
        error "部分关键迁移文件缺失"
        return 1
    fi
    
    # 执行迁移 (仅执行我们新增的表)
    info "执行 360_session_module_executions.sql..."
    if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -f "$migration_dir/360_session_module_executions.sql" &> /tmp/migration_360.log; then
        success "360 迁移完成"
    else
        warning "360 迁移可能已执行或失败，查看日志: /tmp/migration_360.log"
    fi
    
    info "执行 361_dashboard_access_events.sql..."
    if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -f "$migration_dir/361_dashboard_access_events.sql" &> /tmp/migration_361.log; then
        success "361 迁移完成"
    else
        warning "361 迁移可能已执行或失败，查看日志: /tmp/migration_361.log"
    fi
    
    # 验证表创建
    local table_count=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -t -c "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public' AND (tablename LIKE '%session_module%' OR tablename LIKE '%dashboard%');" 2>/dev/null || echo "0")
    
    if [ "$table_count" -ge 8 ]; then
        success "数据库表验证通过 ($table_count 个表)"
    else
        warning "表数量不足，预期至少8个，实际: $table_count"
    fi
}

generate_env_file() {
    info "生成环境配置文件..."
    
    cat > "$ENV_FILE" << EOF
# LLM Gateway 本地测试环境配置
# 自动生成于: $(date)

# 数据库配置
export LLM_GATEWAY_DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"

# Redis 配置
export LLM_GATEWAY_REDIS_ADDR="${REDIS_HOST}:${REDIS_PORT}"
export LLM_GATEWAY_REDIS_PASSWORD=""

# 服务配置
export LLM_GATEWAY_LISTEN=":${SERVICE_PORT}"
export LLM_GATEWAY_SECRET_KEY="test-secret-key-for-local-development-only-$(date +%s)"
export LLM_GATEWAY_ADMIN_PASSWORD="${ADMIN_PASSWORD}"

# 开发模式
export LLM_GATEWAY_CORS_ORIGINS="*"
export LLM_GATEWAY_ENV="development"
export LLM_GATEWAY_LOG_LEVEL="info"

# 可选: 启用 V2 API
# export LLM_GATEWAY_V2_ENABLED="true"
EOF

    success "环境配置已生成: $ENV_FILE"
}

build_service() {
    info "编译服务..."
    
    cd "$PROJECT_ROOT"
    
    if go build -o llm-gateway ./cmd/gateway; then
        local size=$(ls -lh llm-gateway | awk '{print $5}')
        success "编译成功 (大小: $size)"
    else
        error "编译失败"
        return 1
    fi
}

stop_service() {
    if [ -f "$PID_FILE" ]; then
        local pid=$(cat "$PID_FILE")
        if ps -p "$pid" &> /dev/null; then
            info "停止服务 (PID: $pid)..."
            kill "$pid" 2>/dev/null || true
            sleep 2
            
            # 强制杀掉
            if ps -p "$pid" &> /dev/null; then
                kill -9 "$pid" 2>/dev/null || true
            fi
            success "服务已停止"
        fi
        rm -f "$PID_FILE"
    fi
}

start_service() {
    info "启动服务..."
    
    cd "$PROJECT_ROOT"
    
    # 加载环境变量
    source "$ENV_FILE"
    
    # 启动服务
    nohup ./llm-gateway > "$LOG_FILE" 2>&1 &
    local pid=$!
    echo "$pid" > "$PID_FILE"
    
    sleep 3
    
    # 验证服务启动
    if ps -p "$pid" &> /dev/null; then
        success "服务启动成功 (PID: $pid)"
        
        # 检查健康状态
        if curl -s "http://localhost:${SERVICE_PORT}/healthz" | grep -q "ok"; then
            success "健康检查通过"
        else
            warning "健康检查失败，查看日志: $LOG_FILE"
        fi
    else
        error "服务启动失败，查看日志: $LOG_FILE"
        return 1
    fi
}

show_status() {
    echo ""
    echo "============================================"
    echo "🚀 LLM Gateway 本地部署完成"
    echo "============================================"
    echo ""
    echo "服务信息:"
    echo "  前端:      http://localhost:${SERVICE_PORT}/"
    echo "  健康检查:  http://localhost:${SERVICE_PORT}/healthz"
    echo "  Prometheus: http://localhost:${SERVICE_PORT}/metrics"
    echo "  Dashboard: http://localhost:${SERVICE_PORT}/api/admin/dashboard/*"
    echo ""
    echo "登录信息:"
    echo "  用户名:    admin"
    echo "  密码:      ${ADMIN_PASSWORD}"
    echo ""
    echo "服务管理:"
    echo "  查看日志:  tail -f $LOG_FILE"
    echo "  停止服务:  $0 stop"
    echo "  重启服务:  $0 restart"
    echo "  查看状态:  $0 status"
    echo ""
    echo "数据库:"
    echo "  连接串:    postgres://${DB_USER}:***@${DB_HOST}:${DB_PORT}/${DB_NAME}"
    echo "  访问:      PGPASSWORD=${DB_PASSWORD} psql -h ${DB_HOST} -p ${DB_PORT} -U ${DB_USER} -d ${DB_NAME}"
    echo ""
}

check_service_status() {
    if [ -f "$PID_FILE" ]; then
        local pid=$(cat "$PID_FILE")
        if ps -p "$pid" &> /dev/null; then
            success "服务运行中 (PID: $pid)"
            
            # 显示端口监听
            if command -v lsof &> /dev/null; then
                local port_info=$(lsof -nP -iTCP:${SERVICE_PORT} -sTCP:LISTEN 2>/dev/null || echo "")
                if [ -n "$port_info" ]; then
                    success "监听端口: ${SERVICE_PORT}"
                fi
            fi
            
            # 测试健康检查
            if curl -s "http://localhost:${SERVICE_PORT}/healthz" | grep -q "ok"; then
                success "健康检查: 通过"
            else
                warning "健康检查: 失败"
            fi
            
            return 0
        else
            warning "PID 文件存在但进程未运行"
            return 1
        fi
    else
        warning "服务未运行"
        return 1
    fi
}

# ============================================
# 主流程
# ============================================

case "${1:-deploy}" in
    deploy)
        echo "============================================"
        echo "LLM Gateway 本地部署脚本"
        echo "============================================"
        echo ""
        
        check_requirements
        
        if ! check_database; then
            error "数据库检查失败，退出"
            exit 1
        fi
        
        run_migrations
        generate_env_file
        build_service
        stop_service
        start_service
        show_status
        ;;
        
    stop)
        stop_service
        ;;
        
    start)
        if [ ! -f "$PROJECT_ROOT/llm-gateway" ]; then
            error "可执行文件不存在，请先运行: $0 deploy"
            exit 1
        fi
        start_service
        ;;
        
    restart)
        stop_service
        sleep 2
        start_service
        ;;
        
    status)
        check_service_status
        ;;
        
    logs)
        if [ -f "$LOG_FILE" ]; then
            tail -f "$LOG_FILE"
        else
            error "日志文件不存在: $LOG_FILE"
            exit 1
        fi
        ;;
        
    *)
        echo "用法: $0 {deploy|start|stop|restart|status|logs}"
        echo ""
        echo "命令:"
        echo "  deploy   - 完整部署 (构建+迁移+启动)"
        echo "  start    - 启动服务"
        echo "  stop     - 停止服务"
        echo "  restart  - 重启服务"
        echo "  status   - 查看状态"
        echo "  logs     - 查看日志"
        echo ""
        echo "环境变量 (可选):"
        echo "  DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD"
        echo "  REDIS_HOST, REDIS_PORT"
        echo "  SERVICE_PORT, ADMIN_PASSWORD"
        exit 1
        ;;
esac
