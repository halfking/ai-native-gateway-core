#!/bin/bash
# 检查 Redis 中的 route_node 状态

echo "========================================="
echo "🔍 检查 Redis 中的 RouteNodeStore 状态"
echo "========================================="
echo ""

# 检查 Redis 是否运行
if ! command -v redis-cli &> /dev/null; then
    echo "❌ redis-cli 未安装"
    exit 1
fi

echo "1️⃣  检查 Redis 连接..."
if redis-cli ping > /dev/null 2>&1; then
    echo "✅ Redis 连接成功"
else
    echo "❌ Redis 连接失败或未启动"
    echo "   尝试启动: brew services start redis"
    exit 1
fi
echo ""

echo "2️⃣  查找 minimax-m3 的 route_node 键..."
# Redis key 格式: route_node:<credID>:<model>
KEYS=$(redis-cli KEYS "route_node:*:minimax-m3" 2>/dev/null)
if [ -z "$KEYS" ]; then
    echo "✅ 没有找到 minimax-m3 的 route_node 记录"
    echo "   这意味着该节点从未被标记为不可用"
else
    echo "⚠️  找到以下 route_node 记录:"
    echo "$KEYS"
    echo ""
    for key in $KEYS; do
        echo "键: $key"
        redis-cli GET "$key" | python3 -m json.tool 2>/dev/null || redis-cli GET "$key"
        echo ""
    done
fi
echo ""

echo "3️⃣  查找所有 minimax 相关的 route_node 键..."
ALL_MINIMAX=$(redis-cli KEYS "route_node:*:minimax*" 2>/dev/null)
if [ -z "$ALL_MINIMAX" ]; then
    echo "✅ 没有找到任何 minimax 相关的 route_node 记录"
else
    echo "找到以下记录:"
    for key in $ALL_MINIMAX; do
        echo "  - $key"
    done
fi
echo ""

echo "4️⃣  查找 credential 10 的所有 route_node 记录..."
CRED_10=$(redis-cli KEYS "route_node:10:*" 2>/dev/null)
if [ -z "$CRED_10" ]; then
    echo "✅ Credential 10 没有任何 route_node 记录"
else
    echo "找到以下记录:"
    for key in $CRED_10; do
        echo "  键: $key"
        VALUE=$(redis-cli GET "$key")
        echo "  值: $VALUE" | head -c 200
        echo ""
    done
fi
echo ""

echo "========================================="
echo "📊 诊断结论"
echo "========================================="
echo ""
echo "如果没有找到 route_node:10:minimax-m3 键："
echo "  → minimax-m3 从未被标记为不可用"
echo "  → RouteNodeStore.IsUsable() 会返回 true（默认可用）"
echo "  → 数据库层面是可路由的"
echo ""
echo "如果找到了该键且包含 Disabled=true："
echo "  → 需要清除该键或等待冷却时间过期"
echo "  → 清除命令: redis-cli DEL route_node:10:minimax-m3"
echo ""

