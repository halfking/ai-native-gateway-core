#!/bin/bash
# V3.2 会话优化集成验证脚本
# 用途：验证后端 API、SSE 和前端组件集成
# 创建时间：2026-08-14
# 依赖：本地 gateway 运行（localhost:8080）、web dev server（localhost:5173）

set -e

echo "=========================================="
echo "V3.2 会话优化集成验证"
echo "=========================================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 计数器
PASS=0
FAIL=0
SKIP=0

# 测试结果记录
test_result() {
    local name="$1"
    local status="$2"
    local message="$3"
    
    if [ "$status" = "PASS" ]; then
        echo -e "${GREEN}✅ PASS${NC} - $name"
        ((PASS++))
    elif [ "$status" = "FAIL" ]; then
        echo -e "${RED}❌ FAIL${NC} - $name: $message"
        ((FAIL++))
    else
        echo -e "${YELLOW}⚠️  SKIP${NC} - $name: $message"
        ((SKIP++))
    fi
}

echo "第 1 步：检查服务可达性"
echo "----------------------------------------"

# 检查后端
if curl -s -f http://localhost:8080/healthz > /dev/null 2>&1; then
    test_result "后端服务 (localhost:8080)" "PASS"
    BACKEND_UP=1
else
    test_result "后端服务 (localhost:8080)" "FAIL" "服务不可达，请先启动 gateway"
    BACKEND_UP=0
fi

# 检查前端
if curl -s -f http://localhost:5173 > /dev/null 2>&1; then
    test_result "前端服务 (localhost:5173)" "PASS"
    FRONTEND_UP=1
else
    test_result "前端服务 (localhost:5173)" "SKIP" "前端未运行，跳过 UI 测试"
    FRONTEND_UP=0
fi

echo ""
echo "第 2 步：验证 V3.2 后端 API 端点"
echo "----------------------------------------"

if [ "$BACKEND_UP" -eq 1 ]; then
    # 需要认证的端点，这里只检查路由是否注册（401/403 表示路由存在）
    
    # 节点操作 API
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8080/api/admin/providers/test-provider-id/test-now)
    if [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ] || [ "$HTTP_CODE" = "400" ]; then
        test_result "POST /api/admin/providers/{id}/test-now" "PASS"
    else
        test_result "POST /api/admin/providers/{id}/test-now" "FAIL" "HTTP $HTTP_CODE (期望 401/403/400)"
    fi
    
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PATCH http://localhost:8080/api/admin/providers/test-provider-id/enable)
    if [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ] || [ "$HTTP_CODE" = "400" ]; then
        test_result "PATCH /api/admin/providers/{id}/enable" "PASS"
    else
        test_result "PATCH /api/admin/providers/{id}/enable" "FAIL" "HTTP $HTTP_CODE (期望 401/403/400)"
    fi
    
    # 状态变更历史
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/api/admin/requests/test-request-id/transitions)
    if [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ] || [ "$HTTP_CODE" = "404" ]; then
        test_result "GET /api/admin/requests/{id}/transitions" "PASS"
    else
        test_result "GET /api/admin/requests/{id}/transitions" "FAIL" "HTTP $HTTP_CODE (期望 401/403/404)"
    fi
    
    # 在线会话 API
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/api/admin/sessions/online)
    if [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ]; then
        test_result "GET /api/admin/sessions/online" "PASS"
    else
        test_result "GET /api/admin/sessions/online" "FAIL" "HTTP $HTTP_CODE (期望 401/403)"
    fi
    
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/api/admin/sessions/test-session-id/timeline)
    if [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ] || [ "$HTTP_CODE" = "404" ]; then
        test_result "GET /api/admin/sessions/{id}/timeline" "PASS"
    else
        test_result "GET /api/admin/sessions/{id}/timeline" "FAIL" "HTTP $HTTP_CODE (期望 401/403/404)"
    fi
else
    test_result "V3.2 API 端点" "SKIP" "后端未运行"
fi

echo ""
echo "第 3 步：验证 SSE 端点和消息类型"
echo "----------------------------------------"

if [ "$BACKEND_UP" -eq 1 ]; then
    # 检查 SSE 端点（会返回 401，但端点存在）
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/api/admin/live-stream)
    if [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ]; then
        test_result "SSE 端点 /api/admin/live-stream" "PASS"
    else
        test_result "SSE 端点 /api/admin/live-stream" "FAIL" "HTTP $HTTP_CODE (期望 401/403)"
    fi
    
    # 注意：真实的 SSE 消息类型验证需要认证后连接，这里只是验证端点存在
    echo "  ℹ️  SSE 消息类型 (queue_snapshot, node_update) 需要认证后验证"
else
    test_result "SSE 端点" "SKIP" "后端未运行"
fi

echo ""
echo "第 4 步：前端组件存在性检查"
echo "----------------------------------------"

if [ -f "web/src/components/QueuePerspectivePanel.vue" ]; then
    test_result "QueuePerspectivePanel.vue" "PASS"
else
    test_result "QueuePerspectivePanel.vue" "FAIL" "组件文件不存在"
fi

if [ -f "web/src/components/NodeStatusMatrix.vue" ]; then
    test_result "NodeStatusMatrix.vue" "PASS"
else
    test_result "NodeStatusMatrix.vue" "FAIL" "组件文件不存在"
fi

if [ -f "web/src/components/LiveRequestStreamV2.vue" ]; then
    test_result "LiveRequestStreamV2.vue" "PASS"
else
    test_result "LiveRequestStreamV2.vue" "FAIL" "组件文件不存在"
fi

if [ -f "web/src/components/SessionStatsPanel.vue" ]; then
    test_result "SessionStatsPanel.vue" "PASS"
else
    test_result "SessionStatsPanel.vue" "FAIL" "组件文件不存在"
fi

echo ""
echo "第 5 步：Migration 文件存在性检查"
echo "----------------------------------------"

if [ -f "sql/migrations/startup/510_request_type.sql" ]; then
    test_result "Migration 510 (request_type)" "PASS"
else
    test_result "Migration 510 (request_type)" "FAIL" "SQL 文件不存在"
fi

if [ -f "sql/migrations/startup/511_state_transitions_table.sql" ]; then
    test_result "Migration 511 (state_transitions)" "PASS"
else
    test_result "Migration 511 (state_transitions)" "FAIL" "SQL 文件不存在"
fi

if [ -f "sql/migrations/startup/513_schema_unification_and_session_turns_dual_write.sql" ]; then
    test_result "Migration 513 (schema unification)" "PASS"
else
    test_result "Migration 513 (schema unification)" "FAIL" "SQL 文件不存在"
fi

echo ""
echo "=========================================="
echo "测试结果汇总"
echo "=========================================="
echo -e "${GREEN}✅ PASS: $PASS${NC}"
echo -e "${RED}❌ FAIL: $FAIL${NC}"
echo -e "${YELLOW}⚠️  SKIP: $SKIP${NC}"
echo ""

if [ "$FAIL" -gt 0 ]; then
    echo "❌ 集成验证失败，请修复上述问题"
    exit 1
elif [ "$SKIP" -gt 5 ]; then
    echo "⚠️  跳过项过多，请确保服务正常运行后重新验证"
    exit 2
else
    echo "✅ 基础集成验证通过"
    echo ""
    echo "下一步："
    echo "1. 使用认证 token 连接 SSE 端点验证 queue_snapshot/node_update 消息"
    echo "2. 使用 browser-use/playwright 验证前端 UI 交互"
    echo "3. 执行完整的端到端测试场景"
    exit 0
fi
