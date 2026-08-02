#!/bin/bash
# LLM Gateway 部署脚本

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# The canonical deployment runner uses this command as its source of truth
# for target contracts. Keep the legacy build/deploy commands below intact,
# but make target selection explicit and fail closed for production.
source "$SCRIPT_DIR/deploy-lib/targets.sh"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

echo_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

echo_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查环境
check_environment() {
    echo_info "Checking environment..."
    
    # 检查 Go
    if ! command -v go &> /dev/null; then
        echo_error "Go is not installed"
        exit 1
    fi
    
    # 检查 Docker
    if ! command -v docker &> /dev/null; then
        echo_warn "Docker is not installed (optional)"
    fi
    
    echo_info "Environment check passed"
}

# 编译
build() {
    echo_info "Building..."
    
    go build -mod=mod -o bin/llm-gateway ./cmd/server
    
    if [ $? -eq 0 ]; then
        echo_info "Build succeeded"
    else
        echo_error "Build failed"
        exit 1
    fi
}

# 测试
test() {
    echo_info "Running tests..."
    
    go test -mod=mod -v ./...
    
    if [ $? -eq 0 ]; then
        echo_info "Tests passed"
    else
        echo_error "Tests failed"
        exit 1
    fi
}

# 部署
deploy() {
    ENV=$(target_resolve_alias "${1:-245}")

    case "$ENV" in
        245|test|preprod)
            ENV="245"
            ;;
        154|prod|production)
            if [ "${LLM_GATEWAY_EXPLICIT_PROD:-false}" != "true" ]; then
                echo_error "生产目标 154 必须显式设置 LLM_GATEWAY_EXPLICIT_PROD=true，并使用 scripts/deploy-154.sh"
                exit 64
            fi
            ENV="154"
            ;;
        *)
            echo_error "不支持的部署目标: $ENV（仅 245；154 需显式使用 scripts/deploy-154.sh）"
            exit 64
            ;;
    esac
    
    echo_info "Deploying to $ENV..."
    
    # 停止旧服务
    if [ -f "llm-gateway.pid" ]; then
        OLD_PID=$(cat llm-gateway.pid)
        if kill -0 $OLD_PID 2>/dev/null; then
            echo_info "Stopping old service (PID: $OLD_PID)..."
            kill $OLD_PID
            sleep 2
        fi
    fi
    
    # 启动新服务
    echo_info "Starting new service..."
    nohup ./bin/llm-gateway > logs/llm-gateway.log 2>&1 &
    echo $! > llm-gateway.pid
    
    # 健康检查
    sleep 3
    if health_check; then
        echo_info "Deployment succeeded"
    else
        echo_error "Deployment failed"
        rollback
        exit 1
    fi
}

# Canonical target plan consumed by the deployment gate. The optional
# --json is accepted for compatibility with the runner and does not change
# the output because the contract is already JSON.
plan() {
    local target
    target=$(target_resolve_alias "${1:-}")
    case "$target" in
        245|154|186|252|kaixuan-1|kaixuan-2|kaixuan-3)
            printf 'target: %s\n' "$target"
            target_contract "$target"
            ;;
        *)
            echo_error "用法: $0 plan <245|154|186|252|kaixuan-1|kaixuan-2|kaixuan-3> [--json]"
            exit 64
            ;;
    esac
}

# 健康检查
health_check() {
    echo_info "Performing health check..."
    
    for i in {1..10}; do
        if curl -s http://localhost:8080/health > /dev/null; then
            echo_info "Health check passed"
            return 0
        fi
        sleep 1
    done
    
    echo_error "Health check failed"
    return 1
}

# 回滚
rollback() {
    local target
    target=$(target_resolve_alias "${1:-245}")
    local rollback_policy
    rollback_policy=$(target_field "$target" rollback_policy)
    if [[ "$rollback_policy" == "runbook" ]]; then
        echo_error "target $target rollback is runbook-only; use the documented rollback runbook"
        return 64
    fi
    echo_warn "Rolling back target $target..."
    
    # 停止当前服务
    if [ -f "llm-gateway.pid" ]; then
        PID=$(cat llm-gateway.pid)
        if kill -0 $PID 2>/dev/null; then
            kill $PID
        fi
    fi
    
    # 恢复备份
    if [ -f "bin/llm-gateway.backup" ]; then
        cp bin/llm-gateway.backup bin/llm-gateway
        deploy
    fi
    
    echo_warn "Rollback completed"
}

# 主函数
main() {
    COMMAND=${1:-deploy}
    
    case $COMMAND in
        plan)
            plan "${2:-}"
            ;;
        check)
            check_environment
            ;;
        build)
            build
            ;;
        test)
            test
            ;;
        deploy)
            target=$(target_resolve_alias "${2:-245}")
            target_check_actionable "$target" deploy
            if [[ "$target" == "154" && "${LLM_GATEWAY_EXPLICIT_PROD:-false}" != "true" ]]; then
                echo_error "生产目标 154 必须显式设置 LLM_GATEWAY_EXPLICIT_PROD=true，并使用 scripts/deploy-154.sh"
                exit 64
            fi
            check_environment
            build
            test
            deploy "$target"
            ;;
        rollback)
            rollback "${2:-245}"
            ;;
        health)
            health_check
            ;;
        *)
            echo "Usage: $0 {plan|check|build|test|deploy|rollback|health} [target]"
            exit 1
            ;;
    esac
}

main "$@"
