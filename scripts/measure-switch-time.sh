#!/bin/bash

##############################################################################
# 切换时间精确测量脚本
# 用途：精确测量蓝绿切换的各个阶段耗时
##############################################################################

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
NGINX_CONF="/opt/homebrew/etc/nginx/conf.d/upstream.conf"
BLUE_PORT=8081
GREEN_PORT=8082
NGINX_PORT=18080
HEALTH_ENDPOINT="http://localhost:18080/health"

# 时间戳函数（纳秒级）
timestamp_ns() {
    python3 -c 'import time; print(int(time.time() * 1000000000))'
}

# 时间戳函数（毫秒）
timestamp_ms() {
    python3 -c 'import time; print(int(time.time() * 1000))'
}

# 计算时间差（毫秒）
time_diff_ms() {
    local start=$1
    local end=$2
    echo $((end - start))
}

# 获取当前活跃端口
get_active_port() {
    if [ ! -f "$NGINX_CONF" ]; then
        echo "8081"  # 默认蓝色
        return
    fi
    
    if grep -q "server 127.0.0.1:8081" "$NGINX_CONF"; then
        echo "8081"
    else
        echo "8082"
    fi
}

# 获取目标端口
get_target_port() {
    local current=$(get_active_port)
    if [ "$current" = "8081" ]; then
        echo "8082"
    else
        echo "8081"
    fi
}

# 检查服务是否就绪
check_service_ready() {
    local port=$1
    local max_attempts=30
    local attempt=0
    
    while [ $attempt -lt $max_attempts ]; do
        if curl -sf "http://localhost:${port}/health" > /dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
        attempt=$((attempt + 1))
    done
    return 1
}

# 测量 Nginx reload 时间
measure_nginx_reload() {
    echo -e "${BLUE}[测量] Nginx reload 时间...${NC}"
    
    local start=$(timestamp_ms)
    nginx -s reload 2>&1
    local end=$(timestamp_ms)
    
    local reload_time=$(time_diff_ms $start $end)
    echo -e "${GREEN}✓ Nginx reload 耗时: ${reload_time}ms${NC}"
    echo "$reload_time"
}

# 模拟完整切换流程并测量
simulate_full_switch() {
    echo -e "${BLUE}================================${NC}"
    echo -e "${BLUE}开始完整切换流程测量${NC}"
    echo -e "${BLUE}================================${NC}"
    
    local current_port=$(get_active_port)
    local target_port=$(get_target_port)
    local current_color=$([ "$current_port" = "8081" ] && echo "蓝色" || echo "绿色")
    local target_color=$([ "$target_port" = "8081" ] && echo "蓝色" || echo "绿色")
    
    echo -e "当前活跃环境: ${GREEN}${current_color} (${current_port})${NC}"
    echo -e "目标切换环境: ${YELLOW}${target_color} (${target_port})${NC}"
    echo ""
    
    # 阶段1: 检查目标服务是否运行
    echo -e "${BLUE}[阶段1] 检查目标服务状态...${NC}"
    local stage1_start=$(timestamp_ms)
    
    if ! check_service_ready "$target_port"; then
        echo -e "${RED}✗ 目标服务 (${target_port}) 未就绪${NC}"
        return 1
    fi
    
    local stage1_end=$(timestamp_ms)
    local stage1_time=$(time_diff_ms $stage1_start $stage1_end)
    echo -e "${GREEN}✓ 目标服务就绪检查完成: ${stage1_time}ms${NC}"
    echo ""
    
    # 阶段2: 预热目标服务
    echo -e "${BLUE}[阶段2] 预热目标服务...${NC}"
    local stage2_start=$(timestamp_ms)
    
    # 并发预热请求
    for i in {1..5}; do
        curl -sf "http://localhost:${target_port}/health" > /dev/null 2>&1 &
    done
    wait
    
    local stage2_end=$(timestamp_ms)
    local stage2_time=$(time_diff_ms $stage2_start $stage2_end)
    echo -e "${GREEN}✓ 预热完成: ${stage2_time}ms${NC}"
    echo ""
    
    # 阶段3: 修改 Nginx 配置
    echo -e "${BLUE}[阶段3] 修改 Nginx 配置...${NC}"
    local stage3_start=$(timestamp_ms)
    
    # 备份当前配置
    cp "$NGINX_CONF" "${NGINX_CONF}.backup"
    
    # 生成新配置
    cat > "$NGINX_CONF" << EOF
upstream llm_gateway {
    server 127.0.0.1:${target_port};
}
EOF
    
    local stage3_end=$(timestamp_ms)
    local stage3_time=$(time_diff_ms $stage3_start $stage3_end)
    echo -e "${GREEN}✓ 配置文件修改完成: ${stage3_time}ms${NC}"
    echo ""
    
    # 阶段4: Nginx reload（关键步骤）
    echo -e "${BLUE}[阶段4] Nginx reload（原子切换）...${NC}"
    local stage4_start=$(timestamp_ms)
    
    nginx -s reload 2>&1
    
    local stage4_end=$(timestamp_ms)
    local stage4_time=$(time_diff_ms $stage4_start $stage4_end)
    echo -e "${GREEN}✓ Nginx reload 完成: ${stage4_time}ms${NC}"
    echo ""
    
    # 阶段5: 验证切换成功
    echo -e "${BLUE}[阶段5] 验证切换结果...${NC}"
    local stage5_start=$(timestamp_ms)
    
    sleep 0.2  # 等待 reload 生效
    
    # 验证新服务是否接管流量
    local verify_success=true
    for i in {1..3}; do
        if ! curl -sf "$HEALTH_ENDPOINT" > /dev/null 2>&1; then
            verify_success=false
            break
        fi
    done
    
    local stage5_end=$(timestamp_ms)
    local stage5_time=$(time_diff_ms $stage5_start $stage5_end)
    
    if [ "$verify_success" = true ]; then
        echo -e "${GREEN}✓ 切换验证成功: ${stage5_time}ms${NC}"
    else
        echo -e "${RED}✗ 切换验证失败: ${stage5_time}ms${NC}"
        # 回滚
        mv "${NGINX_CONF}.backup" "$NGINX_CONF"
        nginx -s reload
        return 1
    fi
    echo ""
    
    # 总结
    local total_time=$((stage1_time + stage2_time + stage3_time + stage4_time + stage5_time))
    local critical_path=$((stage3_time + stage4_time))
    
    echo -e "${BLUE}================================${NC}"
    echo -e "${BLUE}切换时间分析${NC}"
    echo -e "${BLUE}================================${NC}"
    echo -e "阶段1 (健康检查):     ${stage1_time}ms"
    echo -e "阶段2 (预热):         ${stage2_time}ms"
    echo -e "阶段3 (修改配置):     ${stage3_time}ms"
    echo -e "阶段4 (Nginx reload): ${GREEN}${stage4_time}ms${NC} ${YELLOW}← 关键路径${NC}"
    echo -e "阶段5 (验证):         ${stage5_time}ms"
    echo -e "${BLUE}--------------------------------${NC}"
    echo -e "总耗时:               ${total_time}ms"
    echo -e "关键路径耗时:         ${GREEN}${critical_path}ms${NC}"
    echo -e "${BLUE}================================${NC}"
    
    if [ $critical_path -le 2000 ]; then
        echo -e "${GREEN}✓ 切换时间满足要求（< 2秒）${NC}"
    else
        echo -e "${YELLOW}⚠ 切换时间超过目标（2秒）${NC}"
    fi
    
    # 清理备份
    rm -f "${NGINX_CONF}.backup"
    
    return 0
}

# 持续测量切换过程中的可用性
measure_availability_during_switch() {
    echo -e "${BLUE}[测量] 切换过程中的服务可用性...${NC}"
    
    local test_duration=10  # 测试持续时间（秒）
    local success_count=0
    local fail_count=0
    local total_count=0
    
    local end_time=$(($(date +%s) + test_duration))
    
    while [ $(date +%s) -lt $end_time ]; do
        if curl -sf -m 1 "$HEALTH_ENDPOINT" > /dev/null 2>&1; then
            success_count=$((success_count + 1))
        else
            fail_count=$((fail_count + 1))
        fi
        total_count=$((total_count + 1))
        sleep 0.05  # 50ms 间隔
    done
    
    local success_rate=$(python3 -c "print(f'{$success_count / $total_count * 100:.2f}')")
    
    echo -e "${BLUE}================================${NC}"
    echo -e "测试时长: ${test_duration}秒"
    echo -e "总请求数: ${total_count}"
    echo -e "成功数: ${GREEN}${success_count}${NC}"
    echo -e "失败数: ${RED}${fail_count}${NC}"
    echo -e "成功率: ${GREEN}${success_rate}%${NC}"
    echo -e "${BLUE}================================${NC}"
}

# 主函数
main() {
    echo -e "${BLUE}================================${NC}"
    echo -e "${BLUE}蓝绿部署切换时间精确测量${NC}"
    echo -e "${BLUE}================================${NC}"
    echo ""
    
    # 检查 Nginx 是否运行
    if ! pgrep -x nginx > /dev/null; then
        echo -e "${RED}✗ Nginx 未运行，请先启动 Nginx${NC}"
        exit 1
    fi
    
    # 检查当前服务状态
    local current_port=$(get_active_port)
    if ! check_service_ready "$current_port"; then
        echo -e "${RED}✗ 当前服务 (${current_port}) 未运行${NC}"
        exit 1
    fi
    
    # 执行测量
    simulate_full_switch
    
    echo ""
    echo -e "${GREEN}✓ 测量完成${NC}"
}

# 如果直接运行脚本
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
