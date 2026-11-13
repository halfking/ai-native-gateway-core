#!/usr/bin/env bash
# F3 智谱 trial 配额归因验证脚本
# 
# 功能：自动连接 211 生产库并执行验证 SQL
# 
# 使用方法：
#   ./scripts/verify-f3-quota-attribution.sh [--via-mesh]
# 
# 选项：
#   --via-mesh    强制使用 netbird mesh 中继（外网环境）
#   （无参数）    自动探测：优先内网直连，失败时回落 mesh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SQL_FILE="${SCRIPT_DIR}/verify-f3-quota-attribution.sql"

# 默认 SSH 目标
SSH_TARGET="211"
USE_MESH=false

# 解析参数
for arg in "$@"; do
    case "$arg" in
        --via-mesh)
            USE_MESH=true
            SSH_TARGET="211-mesh"
            ;;
        *)
            echo "未知参数: $arg"
            echo "使用方法: $0 [--via-mesh]"
            exit 1
            ;;
    esac
done

# 自动探测（如果没有显式指定 --via-mesh）
if [ "$USE_MESH" = false ]; then
    echo "🔍 自动探测网络连通性..."
    if timeout 3 ping -c 1 192.168.31.211 &>/dev/null; then
        echo "✅ 内网直连可用，使用 211"
        SSH_TARGET="211"
    else
        echo "⚠️  内网不通，回落 mesh 中继"
        SSH_TARGET="211-mesh"
    fi
fi

echo "📡 连接目标: $SSH_TARGET"
echo "📄 SQL 文件: $SQL_FILE"
echo ""

# 检查 SQL 文件是否存在
if [ ! -f "$SQL_FILE" ]; then
    echo "❌ 错误: SQL 文件不存在: $SQL_FILE"
    exit 1
fi

# 执行远程 SQL
echo "🚀 执行验证查询..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 将 SQL 文件内容通过 stdin 传递，避免文件传输
ssh "$SSH_TARGET" "docker exec -i acc-pg17 psql -U postgres -d llm_gateway" < "$SQL_FILE"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ 验证完成！"
echo ""
echo "📊 结果解读："
echo "  • 查询 1: before_deploy 应为 transient/request_rejected，after_deploy 应为 upstream_quota_permanent"
echo "  • 查询 2: 显示详细时间线，用于精确定位部署时间点"
echo "  • 查询 3: 验证字段完整性（upstream_status_code=400, client_status_code=502/429）"
echo "  • 查询 4: 确认最近 24 小时无新的误归因"
echo "  • 查询 5: 采样代表性记录，人工复核"
