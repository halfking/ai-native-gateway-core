#!/bin/bash
# 诊断 db_empty 问题的脚本
# 用于检查路由计划表和凭据状态

set -euo pipefail

echo "=========================================="
echo "154 服务器 db_empty 问题诊断"
echo "时间: $(date)"
echo "=========================================="

# 1. 检查最近的 db_empty 日志
echo ""
echo "=== 1. 最近的 db_empty 错误 ==="
ssh llm-154 "journalctl -u llm-gateway-go --since '1 day ago' --no-pager | grep -i 'db_empty' | tail -10" || echo "✅ 最近 24 小时没有 db_empty 错误"

# 2. 检查当前路由计划状态
echo ""
echo "=== 2. 当前路由计划状态 ==="
ssh llm-154 "journalctl -u llm-gateway-go --since '5 minutes ago' --no-pager | grep 'routing_resolve' | grep -E 'gpt-5.6-(luna|terra|sol)|glm-5.2|claude' | tail -10"

# 3. 检查 candidates_count = 0 的情况
echo ""
echo "=== 3. 零候选数的路由 ==="
ssh llm-154 "journalctl -u llm-gateway-go --since '1 hour ago' --no-pager | grep 'candidates_count\":0' | wc -l" | xargs -I {} echo "最近 1 小时: {} 次"

# 4. 检查 URSM v2 状态
echo ""
echo "=== 4. URSM v2 状态 ==="
ssh llm-154 "journalctl -u llm-gateway-go --since '1 day ago' --no-pager | grep 'ursm.v2 manager constructed' | tail -1"

# 5. 检查 Redis 连接
echo ""
echo "=== 5. Redis 状态 ==="
ssh llm-154 "redis-cli -h 172.16.2.210 -p 6379 ping 2>&1 || echo '❌ Redis 不可达'"

# 6. 检查服务运行时间
echo ""
echo "=== 6. 服务运行时间 ==="
ssh llm-154 "systemctl show llm-gateway-go | grep ActiveEnterTimestamp"

# 7. 检查内存使用
echo ""
echo "=== 7. 内存使用 ==="
ssh llm-154 "systemctl show llm-gateway-go | grep MemoryCurrent"

# 8. 检查最近的错误
echo ""
echo "=== 8. 最近的 WARN/ERROR 日志 ==="
ssh llm-154 "journalctl -u llm-gateway-go --since '10 minutes ago' --no-pager | grep -E 'level.*(WARN|ERROR)' | tail -5" || echo "✅ 最近 10 分钟没有 WARN/ERROR"

echo ""
echo "=========================================="
echo "诊断完成"
echo "=========================================="
