#!/bin/bash
# ============================================================================
# 会话压缩测试 - Mock Supplier 启动脚本
# 启动 15 个 mock supplier 实例用于压缩测试
# ============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
MOCK_SCRIPT="$SCRIPT_DIR/mock_supplier.py"

# 颜色输出
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# PID 文件目录
PID_DIR="/tmp/llm-gateway-compression-mocks"
mkdir -p "$PID_DIR"

# 日志目录
LOG_DIR="/tmp/llm-gateway-compression-logs"
mkdir -p "$LOG_DIR"

# 检查 Python
if ! command -v python3 &> /dev/null; then
    echo -e "${RED}错误: 未找到 python3${NC}"
    exit 1
fi

# 停止所有 mock
stop_all() {
    echo -e "${YELLOW}停止所有 compression mock suppliers...${NC}"
    for port in {19200..19214}; do
        PID_FILE="$PID_DIR/mock-$port.pid"
        if [ -f "$PID_FILE" ]; then
            PID=$(cat "$PID_FILE")
            if kill -0 "$PID" 2>/dev/null; then
                kill "$PID" 2>/dev/null || true
                echo "  ✓ 已停止 :$port (PID: $PID)"
            fi
            rm -f "$PID_FILE"
        fi
    done
    echo -e "${GREEN}所有 mock 已停止${NC}"
}

# 启动单个 mock
start_mock() {
    local port=$1
    local model=$2
    local context_window=$3
    local group=$4
    
    PID_FILE="$PID_DIR/mock-$port.pid"
    LOG_FILE="$LOG_DIR/mock-$port.log"
    
    # 检查是否已运行
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
            echo "  ⚠ :$port 已在运行 (PID: $PID)"
            return
        fi
    fi
    
    # 启动 mock
    python3 "$MOCK_SCRIPT" \
        --port "$port" \
        --state healthy \
        --group "$group" \
        --instance "$((port - 19200))" \
        > "$LOG_FILE" 2>&1 &
    
    local pid=$!
    echo "$pid" > "$PID_FILE"
    
    # 等待启动
    sleep 0.5
    if kill -0 "$pid" 2>/dev/null; then
        echo -e "  ${GREEN}✓${NC} :$port (PID: $pid, Group: $group, Model: $model)"
    else
        echo -e "  ${RED}✗${NC} :$port 启动失败，查看日志: $LOG_FILE"
        rm -f "$PID_FILE"
    fi
}

# 健康检查
health_check() {
    echo -e "${YELLOW}健康检查...${NC}"
    local failed=0
    
    for port in {19200..19214}; do
        if curl -s -f "http://localhost:$port/healthz" > /dev/null 2>&1; then
            echo -e "  ${GREEN}✓${NC} :$port OK"
        else
            echo -e "  ${RED}✗${NC} :$port FAIL"
            ((failed++))
        fi
    done
    
    if [ $failed -eq 0 ]; then
        echo -e "${GREEN}所有 mock 健康正常${NC}"
        return 0
    else
        echo -e "${RED}有 $failed 个 mock 不健康${NC}"
        return 1
    fi
}

# 显示状态
show_status() {
    echo -e "${YELLOW}Mock Supplier 状态:${NC}"
    echo "----------------------------------------"
    printf "%-8s %-10s %-10s %-20s\n" "端口" "状态" "PID" "Group"
    echo "----------------------------------------"
    
    for port in {19200..19214}; do
        PID_FILE="$PID_DIR/mock-$port.pid"
        if [ -f "$PID_FILE" ]; then
            PID=$(cat "$PID_FILE")
            if kill -0 "$PID" 2>/dev/null; then
                local group=""
                if [ $port -lt 19205 ]; then
                    group="A-direct"
                elif [ $port -lt 19210 ]; then
                    group="B-relay"
                else
                    group="C-claude"
                fi
                echo -e "  $port  ${GREEN}运行中${NC}    $PID    $group"
            else
                echo -e "  $port  ${RED}已停止${NC}    -       -"
                rm -f "$PID_FILE"
            fi
        else
            echo -e "  $port  ${RED}未启动${NC}    -       -"
        fi
    done
    echo "----------------------------------------"
}

# 主逻辑
case "${1:-start}" in
    start)
        echo -e "${GREEN}启动会话压缩测试 Mock Suppliers${NC}"
        echo "========================================"
        
        # A组: 直连 (19200-19204)
        echo -e "\n${YELLOW}A组 - 直连 OpenAI (compression=smart)${NC}"
        for port in {19200..19204}; do
            start_mock "$port" "loadtest-gpt-128k" 128000 "A-direct"
        done
        
        # B组: 中转 (19205-19209)
        echo -e "\n${YELLOW}B组 - 中转节点 (compression=off)${NC}"
        for port in {19205..19209}; do
            start_mock "$port" "loadtest-gpt-128k" 128000 "B-relay"
        done
        
        # C组: Claude (19210-19214)
        echo -e "\n${YELLOW}C组 - Claude (compression=smart)${NC}"
        for port in {19210..19214}; do
            start_mock "$port" "loadtest-claude-200k" 200000 "C-claude"
        done
        
        echo -e "\n${GREEN}所有 mock 启动完成${NC}"
        echo "========================================"
        echo "日志目录: $LOG_DIR"
        echo "PID 目录: $PID_DIR"
        echo ""
        echo "使用以下命令："
        echo "  $0 status    - 查看状态"
        echo "  $0 health    - 健康检查"
        echo "  $0 stop      - 停止所有"
        echo "  $0 restart   - 重启所有"
        ;;
        
    stop)
        stop_all
        ;;
        
    restart)
        stop_all
        sleep 1
        $0 start
        ;;
        
    status)
        show_status
        ;;
        
    health)
        health_check
        ;;
        
    *)
        echo "用法: $0 {start|stop|restart|status|health}"
        exit 1
        ;;
esac
