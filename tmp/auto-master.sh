#!/bin/bash
# 任务自动化总控脚本
# 用途：自动执行所有可以安全执行的检查和准备工作

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

echo ""
echo "========================================"
echo "  价格计费修复 - 自动化任务执行"
echo "========================================"
echo ""
echo "本脚本将自动执行以下任务："
echo "  ✓ 代码编译验证"
echo "  ✓ 迁移文件完整性检查"
echo "  ✓ 生成部署检查清单"
echo "  ⚠ 数据库迁移（需手动确认）"
echo "  ⚠ API部署验证（需配置）"
echo ""
echo "按回车键开始..."
read -r
echo ""

# 任务1: 代码编译验证
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}任务 1/4: 代码编译验证${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

cd "$(dirname "$SCRIPT_DIR")"
echo "工作目录: $(pwd)"
echo ""

echo "编译 maas 包..."
if go build ./maas 2>&1 | grep -v "warning:"; then
    echo -e "${GREEN}✓${NC} maas 编译成功"
else
    echo -e "${RED}✗${NC} maas 编译失败"
    exit 1
fi

echo "编译 domains/streaming 包..."
if go build ./domains/streaming 2>&1 | grep -v "warning:"; then
    echo -e "${GREEN}✓${NC} domains/streaming 编译成功"
else
    echo -e "${RED}✗${NC} domains/streaming 编译失败"
    exit 1
fi

echo "编译 admin 包..."
if go build ./admin 2>&1 | grep -v "warning:"; then
    echo -e "${GREEN}✓${NC} admin 编译成功"
else
    echo -e "${RED}✗${NC} admin 编译失败"
    exit 1
fi

echo ""
echo -e "${GREEN}✓ 任务1完成: 所有代码编译通过${NC}"
echo ""
sleep 1

# 任务2: 迁移文件验证
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}任务 2/4: 迁移文件完整性检查${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

if [[ -f "$SCRIPT_DIR/auto-verify-migrations.sh" ]]; then
    bash "$SCRIPT_DIR/auto-verify-migrations.sh"
else
    echo -e "${RED}✗${NC} 验证脚本不存在"
    exit 1
fi

echo ""
echo -e "${GREEN}✓ 任务2完成: 迁移文件验证通过${NC}"
echo ""
sleep 1

# 任务3: 生成部署检查清单
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}任务 3/4: 生成部署检查清单${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

CHECKLIST_FILE="$SCRIPT_DIR/deployment-checklist-$(date +%Y%m%d_%H%M%S).txt"

cat > "$CHECKLIST_FILE" << 'EOF'
部署前检查清单
========================================

□ 开发侧准备
  ☑ 代码编译通过
  ☑ 迁移文件完整（1000/1001/1002）
  ☑ 测试用例已编写
  ☑ 文档已完善

□ DBA准备（任务2）
  □ 已阅读迁移文件
  □ 已备份数据库
  □ 已在dev环境测试迁移
  □ 已准备回滚方案
  
  执行步骤：
  1. 连接dev数据库
     psql -h dev-db-host -U admin -d llm_gateway_dev
  
  2. 应用迁移（按顺序）
     \i sql/migrations/startup/1000_billing_status_and_credit_limit.sql
     \i sql/migrations/startup/1001_bill_payment_tracking.sql
     \i sql/migrations/startup/1002_billing_retry_queue.sql
  
  3. 验证
     \d tenant_credit_wallets
     \d maas_monthly_bills
     \d billing_precheck_log
     \d billing_retry_queue
  
  4. 测试查询
     SELECT billing_status, credit_limit_cents FROM tenant_credit_wallets LIMIT 1;

□ 运维准备（任务3）
  □ 已获取最新代码
  □ 已在dev环境编译
  □ 已准备回滚二进制
  
  执行步骤：
  1. 编译
     go build -o llm-gateway ./cmd/gateway
  
  2. 部署到dev
     scp llm-gateway dev-server:/opt/llm-gateway/
     ssh dev-server 'sudo systemctl restart llm-gateway'
  
  3. 检查服务
     ssh dev-server 'sudo systemctl status llm-gateway'
     ssh dev-server 'curl http://localhost:8080/api/system/version'
  
  4. 验证API（需要管理员token）
     curl "http://dev-server:8080/api/admin/bills?limit=10" \
       -H "Authorization: Bearer $ADMIN_TOKEN"

□ 数据分析准备（任务4）
  □ 已获取生产只读权限
  □ 已准备验证SQL
  
  执行步骤：
  1. 连接生产只读副本
     psql -h prod-readonly-host -U analyst -d llm_gateway_prod
  
  2. 执行验证查询（见 P0-4 文档）
  
  3. 填写验证报告

□ 灰度发布（推荐）
  □ 先部署到1台dev服务器
  □ 观察24小时无问题
  □ 再部署到staging
  □ 观察3天无问题
  □ 最后部署到production

□ 监控指标
  □ billing_precheck_rejected_total 计数器
  □ 402 错误率
  □ 账单API响应时间
  □ 数据库查询性能

□ 回滚准备
  □ 保留旧版本二进制
  □ 准备迁移回滚脚本
  □ 确认回滚步骤

风险点：
⚠️  P0-1可能导致用户请求被阻断（402错误）
⚠️  迁移可能锁表（建议非高峰执行）
⚠️  新API需要super_admin权限

应急联系：
- 技术问题: 查看对应文档
- 紧急问题: 联系架构师

========================================
检查完成时间: ___________
检查人签名: ___________
========================================
EOF

echo -e "${GREEN}✓${NC} 检查清单已生成: $CHECKLIST_FILE"
echo ""
cat "$CHECKLIST_FILE"
echo ""
echo -e "${GREEN}✓ 任务3完成: 部署检查清单已生成${NC}"
echo ""
sleep 1

# 任务4: 生成快速参考
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}任务 4/4: 生成快速参考卡片${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

QUICKREF_FILE="$SCRIPT_DIR/quick-reference.txt"

cat > "$QUICKREF_FILE" << 'EOF'
快速参考卡片
========================================

📦 迁移文件位置
sql/migrations/startup/1000_billing_status_and_credit_limit.sql
sql/migrations/startup/1001_bill_payment_tracking.sql
sql/migrations/startup/1002_billing_retry_queue.sql

🔧 编译命令
go build -o llm-gateway ./cmd/gateway

🚀 部署命令（dev）
scp llm-gateway dev-server:/opt/llm-gateway/
ssh dev-server 'sudo systemctl restart llm-gateway'

✅ 验证命令
# 检查服务状态
systemctl status llm-gateway

# 检查版本
curl http://localhost:8080/api/system/version

# 测试账单API
curl "http://localhost:8080/api/admin/bills?limit=10" \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 检查数据库字段
psql -c "\d tenant_credit_wallets"

🔙 回滚命令
# 迁移回滚
psql < sql/migrations/startup/1002_billing_retry_queue.down.sql
psql < sql/migrations/startup/1001_bill_payment_tracking.down.sql
psql < sql/migrations/startup/1000_billing_status_and_credit_limit.down.sql

# 服务回滚
ssh dev-server 'sudo systemctl stop llm-gateway'
scp old-binary dev-server:/opt/llm-gateway/llm-gateway
ssh dev-server 'sudo systemctl start llm-gateway'

📊 监控指标
- billing_precheck_rejected_total
- http_requests_total{path="/api/admin/bills"}
- 402 错误率

📞 文档位置
- 交接包: docs/handoff/2026-11-26-p0-tasks-handoff.md
- 状态报告: tmp/handoff-status-report.md
- 检查清单: tmp/deployment-checklist-*.txt

========================================
EOF

echo -e "${GREEN}✓${NC} 快速参考已生成: $QUICKREF_FILE"
echo ""
cat "$QUICKREF_FILE"
echo ""
echo -e "${GREEN}✓ 任务4完成: 快速参考卡片已生成${NC}"
echo ""

# 总结
echo ""
echo "========================================"
echo -e "${GREEN}✓ 所有自动化任务已完成${NC}"
echo "========================================"
echo ""
echo "生成的文件："
echo "  1. $CHECKLIST_FILE"
echo "  2. $QUICKREF_FILE"
echo "  3. tmp/auto-verify-migrations.sh"
echo "  4. tmp/auto-test-api.sh"
echo "  5. tmp/auto-deploy.sh"
echo ""
echo "下一步操作："
echo "  1. 阅读部署检查清单"
echo "  2. DBA执行数据库迁移"
echo "  3. 运维执行服务部署"
echo "  4. 数据分析执行数据验证"
echo ""
echo "⚠️  注意：生产环境操作需要相应权限和人工审核"
echo ""
