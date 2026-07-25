#!/bin/bash
# 部署验证脚本 - 154 和 245 服务器
# 日期: 2026-07-26
# 功能: 部署新版本 gateway 并验证压力感知路由功能

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 服务器配置
SERVERS=("154" "245")
BINARY_PATH="/opt/llm-gateway-go/gateway"
BACKUP_SUFFIX=".backup-$(date +%Y%m%d-%H%M%S)"

# 本地编译产物
LOCAL_BINARY="./gateway-new"

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}部署验证脚本${NC}"
echo -e "${GREEN}========================================${NC}"

# 步骤 1: 验证本地编译产物
echo -e "\n${YELLOW}步骤 1: 验证本地编译产物${NC}"
if [ ! -f "$LOCAL_BINARY" ]; then
    echo -e "${RED}错误: 未找到编译产物 $LOCAL_BINARY${NC}"
    exit 1
fi
echo -e "${GREEN}✓ 本地编译产物存在${NC}"
ls -lh "$LOCAL_BINARY"

# 步骤 2: 备份和部署到服务器
for SERVER in "${SERVERS[@]}"; do
    echo -e "\n${YELLOW}========================================${NC}"
    echo -e "${YELLOW}处理服务器: $SERVER${NC}"
    echo -e "${YELLOW}========================================${NC}"
    
    # 2.1 检查服务器连接
    echo -e "\n${YELLOW}2.1 检查服务器连接${NC}"
    if ! ssh "$SERVER" "hostname" &>/dev/null; then
        echo -e "${RED}✗ 无法连接到服务器 $SERVER${NC}"
        continue
    fi
    echo -e "${GREEN}✓ 服务器 $SERVER 连接成功${NC}"
    
    # 2.2 备份当前版本
    echo -e "\n${YELLOW}2.2 备份当前版本${NC}"
    ssh "$SERVER" "cp -f $BINARY_PATH ${BINARY_PATH}${BACKUP_SUFFIX}" || {
        echo -e "${RED}✗ 备份失败${NC}"
        continue
    }
    echo -e "${GREEN}✓ 备份完成: ${BINARY_PATH}${BACKUP_SUFFIX}${NC}"
    
    # 2.3 上传新版本
    echo -e "\n${YELLOW}2.3 上传新版本${NC}"
    scp "$LOCAL_BINARY" "$SERVER:${BINARY_PATH}.new" || {
        echo -e "${RED}✗ 上传失败${NC}"
        continue
    }
    echo -e "${GREEN}✓ 上传完成${NC}"
    
    # 2.4 检查当前运行状态
    echo -e "\n${YELLOW}2.4 检查当前运行状态${NC}"
    ssh "$SERVER" "ps aux | grep -E '[/]opt/llm-gateway-go/.*gateway' | head -3"
    
    # 2.5 询问是否重启（手动确认）
    echo -e "\n${YELLOW}新版本已上传到: ${BINARY_PATH}.new${NC}"
    echo -e "${YELLOW}备份位于: ${BINARY_PATH}${BACKUP_SUFFIX}${NC}"
    echo -e "\n${GREEN}服务器 $SERVER 准备完成${NC}"
done

# 步骤 3: 手动重启指南
echo -e "\n${YELLOW}========================================${NC}"
echo -e "${YELLOW}步骤 3: 手动重启指南${NC}"
echo -e "${YELLOW}========================================${NC}"

cat << 'EOF'

请在每个服务器上手动执行以下命令来重启服务：

服务器 154:
  ssh 154
  cd /opt/llm-gateway-go
  mv gateway.new gateway
  # 查找并停止旧进程
  ps aux | grep '[/]opt/llm-gateway-go/llm-gateway-go'
  kill <PID>  # 替换为实际 PID
  # 启动新版本（根据实际启动方式）
  nohup ./gateway > gateway.log 2>&1 &
  # 或使用 systemd
  systemctl restart llm-gateway

服务器 245:
  ssh 245
  cd /opt/llm-gateway-go
  mv gateway.new gateway
  # 查找并停止旧进程
  ps aux | grep '[/]opt/llm-gateway-go/gateway'
  kill <PID>  # 替换为实际 PID
  # 启动新版本
  nohup ./gateway > gateway.log 2>&1 &

启用压力感知路由（在启动前设置环境变量）:
  export PRESSURE_AWARE_ROUTING=true
  ./gateway

或修改 systemd 配置:
  [Service]
  Environment="PRESSURE_AWARE_ROUTING=true"

EOF

# 步骤 4: 验证命令
echo -e "\n${YELLOW}========================================${NC}"
echo -e "${YELLOW}步骤 4: 部署后验证命令${NC}"
echo -e "${YELLOW}========================================${NC}"

cat << 'EOF'

验证服务启动:
  ssh 245 "ps aux | grep gateway"
  ssh 154 "ps aux | grep gateway"

验证压力感知路由状态:
  curl http://245:8781/metrics | grep llmgw_pressure_aware_routing_enabled
  curl http://154:8781/metrics | grep llmgw_pressure_aware_routing_enabled
  # 预期输出: llmgw_pressure_aware_routing_enabled 1 (如果启用)

检查压力查询失败率:
  curl http://245:8781/metrics | grep llmgw_pressure_query_failures_total
  curl http://154:8781/metrics | grep llmgw_pressure_query_failures_total

查看日志:
  ssh 245 "tail -f /opt/llm-gateway-go/gateway.log"
  ssh 154 "tail -f /opt/llm-gateway-go/*.log"

回滚（如果需要）:
  ssh 245 "cd /opt/llm-gateway-go && cp gateway.backup-* gateway && systemctl restart llm-gateway"

EOF

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}部署准备完成！${NC}"
echo -e "${GREEN}========================================${NC}"
