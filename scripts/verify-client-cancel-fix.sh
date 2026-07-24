#!/bin/bash
# 验证客户端取消问题修复效果
# 创建时间: 2026-07-24
# 用途: 检查 NPS/Nginx 配置和最近的客户端取消事件

set -e

echo "=========================================="
echo "客户端取消问题修复验证脚本"
echo "=========================================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 1. 检查 NPS 配置
echo "1. 检查 NPS 配置 (服务器 252)..."
echo "-------------------------------------------"
NPS_TIMEOUT=$(ssh 252 "grep 'disconnect_timeout' /etc/nps/conf/nps.conf | grep -v '#' | awk -F'=' '{print \$2}' | tr -d ' '")
if [ "$NPS_TIMEOUT" = "8640" ]; then
    echo -e "${GREEN}✓${NC} NPS disconnect_timeout = 8640 (12小时)"
else
    echo -e "${RED}✗${NC} NPS disconnect_timeout 配置异常: $NPS_TIMEOUT"
fi

NPS_STATUS=$(ssh 252 "sudo systemctl is-active nps")
if [ "$NPS_STATUS" = "active" ]; then
    echo -e "${GREEN}✓${NC} NPS 服务运行正常"
    NPS_PID=$(ssh 252 "ps aux | grep 'nps service' | grep -v grep | awk '{print \$2}'")
    echo "  PID: $NPS_PID"
else
    echo -e "${RED}✗${NC} NPS 服务状态异常: $NPS_STATUS"
fi
echo ""

# 2. 检查 Nginx 配置
echo "2. 检查 Nginx 配置 (服务器 252)..."
echo "-------------------------------------------"
NGINX_READ_TIMEOUT=$(ssh 252 "grep 'proxy_read_timeout' /etc/nginx/conf.d/kxpms-on-252.conf | grep 'location /api/v1/' -A 5 | grep proxy_read_timeout | awk '{print \$2}' | tr -d ';'")
if [ "$NGINX_READ_TIMEOUT" = "300s" ]; then
    echo -e "${GREEN}✓${NC} Nginx proxy_read_timeout = 300s"
else
    echo -e "${YELLOW}⚠${NC} Nginx proxy_read_timeout = $NGINX_READ_TIMEOUT (预期 300s)"
fi

NGINX_STATUS=$(ssh 252 "sudo systemctl is-active nginx")
if [ "$NGINX_STATUS" = "active" ]; then
    echo -e "${GREEN}✓${NC} Nginx 服务运行正常"
else
    echo -e "${RED}✗${NC} Nginx 服务状态异常: $NGINX_STATUS"
fi
echo ""

# 3. 查询最近的客户端取消事件
echo "3. 查询最近 1 小时的客户端取消事件..."
echo "-------------------------------------------"
RECENT_CANCELS=$(ssh 154 "psql -U llmgateway -d llmgateway -t -c \"
SELECT COUNT(*) 
FROM context_attrs 
WHERE origin_stage LIKE 'probe-client_cancel-%'
  AND created_at > now() - interval '1 hour';
\"" 2>/dev/null | tr -d ' ')

if [ -z "$RECENT_CANCELS" ]; then
    echo -e "${YELLOW}⚠${NC} 无法连接数据库，跳过此检查"
else
    if [ "$RECENT_CANCELS" = "0" ]; then
        echo -e "${GREEN}✓${NC} 最近 1 小时无客户端取消事件"
    else
        echo -e "${YELLOW}⚠${NC} 最近 1 小时有 $RECENT_CANCELS 个客户端取消事件"
        echo ""
        echo "最近 5 个客户端取消事件详情:"
        ssh 154 "psql -U llmgateway -d llmgateway -c \"
SELECT 
    request_id,
    api_key_id,
    latency_ms,
    SUBSTRING(request_preview, 1, 100) as preview,
    created_at
FROM context_attrs 
WHERE origin_stage LIKE 'probe-client_cancel-%'
  AND created_at > now() - interval '1 hour'
ORDER BY created_at DESC 
LIMIT 5;
\"" 2>/dev/null
    fi
fi
echo ""

# 4. 检查 Gateway 代码版本
echo "4. 检查 Gateway 代码版本..."
echo "-------------------------------------------"
CURRENT_COMMIT=$(git log --oneline -1 | awk '{print $1}')
FIX_COMMIT="d7da957d"

if git log --oneline | head -20 | grep -q "$FIX_COMMIT"; then
    echo -e "${GREEN}✓${NC} 包含修复提交 $FIX_COMMIT"
    echo "  当前提交: $CURRENT_COMMIT"
else
    echo -e "${RED}✗${NC} 未找到修复提交 $FIX_COMMIT"
fi
echo ""

# 5. 测试建议
echo "=========================================="
echo "测试建议"
echo "=========================================="
echo "1. 使用 VSCode Copilot 连接 llm.kxpms.cn"
echo "2. 发起一个长时间推理请求 (如 o1-preview)"
echo "3. 观察是否会在 60 秒左右自动断开"
echo ""
echo "预期结果:"
echo "  - 请求应该能够持续超过 60 秒"
echo "  - 如果仍然断开，检查 latency_ms 字段是否接近 300000 (5分钟)"
echo ""
echo "监控命令:"
echo "  ssh 154 \"tail -f /var/log/llm-gateway/gateway.log | grep client_cancel\""
echo ""

echo "=========================================="
echo "验证完成"
echo "=========================================="
