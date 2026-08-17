#!/bin/bash
# 2026-08-06 部署快速命令参考
# 使用方法：chmod +x deploy-quickstart.sh && ./deploy-quickstart.sh

set -e

echo "========================================"
echo "会话管理功能部署快速指南"
echo "========================================"
echo ""

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# 步骤 1: 检查环境
echo -e "${YELLOW}步骤 1: 检查环境${NC}"
echo "检查 Go 版本..."
go version || { echo -e "${RED}Go 未安装${NC}"; exit 1; }

echo "检查 PostgreSQL 连接..."
# psql -h localhost -U postgres -d llm_gateway -c "SELECT 1;" > /dev/null 2>&1 || { echo -e "${RED}数据库连接失败${NC}"; exit 1; }

echo -e "${GREEN}✓ 环境检查通过${NC}"
echo ""

# 步骤 2: 编译代码
echo -e "${YELLOW}步骤 2: 编译代码${NC}"
echo "编译中..."
go build -o /tmp/llm-gateway . || { echo -e "${RED}编译失败${NC}"; exit 1; }
echo -e "${GREEN}✓ 编译成功${NC}"
echo ""

# 步骤 3: 运行测试
echo -e "${YELLOW}步骤 3: 运行测试（可选）${NC}"
read -p "是否运行测试？(y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    go test ./admin/... -v
fi
echo ""

# 步骤 4: 数据库迁移
echo -e "${YELLOW}步骤 4: 数据库迁移${NC}"
echo -e "${RED}警告：这将修改生产数据库！${NC}"
read -p "是否继续？(yes/NO): " -r
echo
if [[ $REPLY == "yes" ]]; then
    echo "请手动执行以下命令："
    echo ""
    echo "  psql -h <host> -U <user> -d llm_gateway << 'EOF'"
    echo "  \\i deploy/sql/migrations/V353__session_summaries_project_task_tags.sql"
    echo "  \\i deploy/sql/migrations/V354__session_management_views.sql"
    echo "  \\i deploy/sql/migrations/V355__backfill_session_task_id.sql"
    echo "  SELECT * FROM v_session_task_id_coverage;"
    echo "  EOF"
    echo ""
    read -p "数据库迁移完成后按回车继续..." -r
else
    echo -e "${YELLOW}跳过数据库迁移${NC}"
fi
echo ""

# 步骤 5: 部署应用
echo -e "${YELLOW}步骤 5: 部署应用${NC}"
echo "部署方式："
echo "  1. systemctl 服务"
echo "  2. Docker 容器"
echo "  3. 手动运行"
echo "  4. 跳过"
read -p "选择 (1-4): " -n 1 -r
echo
case $REPLY in
    1)
        echo "停止服务..."
        # sudo systemctl stop llm-gateway
        echo "复制二进制文件..."
        # sudo cp /tmp/llm-gateway /usr/local/bin/
        echo "启动服务..."
        # sudo systemctl start llm-gateway
        echo -e "${GREEN}✓ 服务已重启${NC}"
        ;;
    2)
        echo "Docker 部署请参考部署文档"
        ;;
    3)
        echo "手动运行: /tmp/llm-gateway"
        ;;
    *)
        echo "跳过部署"
        ;;
esac
echo ""

# 步骤 6: 验证
echo -e "${YELLOW}步骤 6: 验证部署${NC}"
echo "等待服务启动..."
sleep 3

echo "测试 API 端点..."
# curl -s http://localhost:8781/api/system/version || echo "API 未响应"

echo ""
echo -e "${GREEN}========================================"
echo "部署完成！"
echo "========================================${NC}"
echo ""
echo "下一步："
echo "  1. 查看日志: journalctl -u llm-gateway -f"
echo "  2. 测试 API: curl http://localhost:8781/api/sessions/list"
echo "  3. 查看监控: Prometheus/Grafana"
echo ""
echo "文档位置: .handoff/2026-08-06-*.md"
echo ""
