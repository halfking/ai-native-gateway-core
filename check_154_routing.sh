#!/bin/bash
# 检查 154 服务器路由节点状态

echo "=== 1. 检查服务状态 ==="
ssh root@8.136.114.154 -p 25022 "systemctl status llm-gateway-go | head -20"

echo -e "\n=== 2. 检查最近的 'No available provider' 错误 ==="
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go --since '1 hour ago' | grep -i 'no available provider' | tail -10"

echo -e "\n=== 3. 检查 URSM v2 Ready 状态 ==="
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go --since '1 hour ago' | grep -i 'not ready' | tail -10"

echo -e "\n=== 4. 检查候选节点过滤日志 ==="
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go --since '1 hour ago' | grep -i 'all candidates unavailable' | tail -10"

echo -e "\n=== 5. 检查 Redis 连接状态 ==="
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go --since '1 hour ago' | grep -iE '(redis|connection)' | tail -10"

echo -e "\n=== 6. 检查冷却状态 ==="
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go --since '1 hour ago' | grep -i 'cooling' | tail -10"

echo -e "\n=== 7. 检查 glm-5.2 相关日志 ==="
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go --since '1 hour ago' | grep -i 'glm-5.2' | tail -10"
