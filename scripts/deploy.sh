#!/bin/bash
# LLM Gateway 部署脚本

set -e

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
    ENV=${1:-production}
    
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
    echo_warn "Rolling back..."
    
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
            check_environment
            build
            test
            deploy ${2:-production}
            ;;
        rollback)
            rollback
            ;;
        health)
            health_check
            ;;
        *)
            echo "Usage: $0 {check|build|test|deploy|rollback|health}"
            exit 1
            ;;
    esac
}

main "$@"
