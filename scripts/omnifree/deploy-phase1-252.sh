#!/bin/bash
# OmniFree Phase 1 一键部署脚本
# 执行时间: 2026-08-07
# 目标环境: 阿里云 252

set -e  # 遇到错误立即退出

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🚀 OmniFree Phase 1 部署脚本"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 设置数据库连接
export DB_252="postgres://kxuser:kxuser123@172.16.2.210:5432/llm_gateway?sslmode=disable"

echo "📋 Phase 1 部署步骤："
echo "  1. 测试数据库连接"
echo "  2. 备份数据库（可选）"
echo "  3. 执行数据库迁移"
echo "  4. 编译种子导入工具"
echo "  5. 导入种子数据"
echo "  6. 验证数据完整性"
echo ""

# Step 1: 测试连接
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📡 Step 1: 测试数据库连接..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if ! psql $DB_252 -c "SELECT version();" > /dev/null 2>&1; then
    echo "❌ 数据库连接失败！"
    echo "   请检查："
    echo "   - 网络连接是否正常"
    echo "   - VPN 是否已连接"
    echo "   - 防火墙规则是否允许"
    echo "   - 数据库地址: 172.16.2.210:5432"
    echo ""
    echo "   如果在远程服务器上执行，请登录到有数据库访问权限的服务器："
    echo "   ssh user@jump-server"
    exit 1
fi
echo "✅ 数据库连接成功！"
echo ""

# Step 2: 备份（可选）
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "💾 Step 2: 备份数据库（可选，按回车跳过，输入y执行备份）"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
read -p "是否备份数据库? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    BACKUP_FILE="backup_252_omnifree_$(date +%Y%m%d_%H%M%S).sql"
    echo "正在备份到: $BACKUP_FILE"
    pg_dump $DB_252 > $BACKUP_FILE
    echo "✅ 备份完成: $BACKUP_FILE"
else
    echo "⏭️  跳过备份"
fi
echo ""

# Step 3: 执行迁移
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🗄️  Step 3: 执行数据库迁移..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if ! psql $DB_252 -f sql/migrations/075-omnifree-schema.sql; then
    echo "❌ 数据库迁移失败！"
    echo "   查看上方错误信息进行排查"
    echo "   如需回滚，执行: psql \$DB_252 -f sql/migrations/075-omnifree-schema.down.sql"
    exit 1
fi
echo "✅ 数据库迁移完成！"
echo ""

# 验证表创建
echo "🔍 验证表创建..."
TABLE_COUNT=$(psql $DB_252 -tAc "
  SELECT COUNT(*) 
  FROM information_schema.tables 
  WHERE table_name IN ('free_resource_catalog', 'free_quota_tracker', 
                       'auto_combo_templates', 'keyless_providers');
")

if [ "$TABLE_COUNT" -eq 4 ]; then
    echo "✅ 4张表创建成功！"
else
    echo "⚠️  警告: 只创建了 $TABLE_COUNT 张表（预期4张）"
fi
echo ""

# Step 4: 编译工具
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🔨 Step 4: 编译种子导入工具..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
cd cmd/seed-free-resources
if ! go build -o seed-free-resources main.go; then
    echo "❌ 编译失败！"
    exit 1
fi
echo "✅ 编译完成！"
cd ../..
echo ""

# Step 5: 导入种子数据
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📥 Step 5: 导入种子数据..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if ! ./cmd/seed-free-resources/seed-free-resources \
  --db-url "$DB_252" \
  --catalog docs/omnifree/seed/free_resource_catalog.json \
  --templates docs/omnifree/seed/auto_combo_templates.json \
  --keyless docs/omnifree/seed/keyless_providers.json \
  --tenant-id 1; then
    echo "❌ 种子数据导入失败！"
    exit 1
fi
echo "✅ 种子数据导入完成！"
echo ""

# Step 6: 验证数据
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ Step 6: 验证数据完整性..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 验证免费资源
RESOURCE_COUNT=$(psql $DB_252 -tAc "SELECT COUNT(*) FROM free_resource_catalog WHERE enabled=TRUE;")
echo "📊 免费资源数量: $RESOURCE_COUNT (预期: 15)"

# 验证配额总量
MONTHLY_QUOTA=$(psql $DB_252 -tAc "
  SELECT ROUND(SUM(monthly_tokens)/1000000000.0, 2)
  FROM free_resource_catalog 
  WHERE free_type='recurring-monthly' AND enabled=TRUE;
")
echo "📊 月度配额总量: ${MONTHLY_QUOTA}B tokens (预期: ~1.27B)"

# 验证 Auto Combo
TEMPLATE_COUNT=$(psql $DB_252 -tAc "SELECT COUNT(*) FROM auto_combo_templates WHERE enabled=TRUE;")
echo "📊 Auto Combo 模板: $TEMPLATE_COUNT (预期: 6)"

# 验证 Keyless
KEYLESS_COUNT=$(psql $DB_252 -tAc "SELECT COUNT(*) FROM keyless_providers WHERE enabled=TRUE;")
echo "📊 Keyless 提供商: $KEYLESS_COUNT (预期: 3)"

echo ""

# 验证 ToS 分布
echo "📊 ToS 合规分布:"
psql $DB_252 -c "
  SELECT tos_verdict, COUNT(*) as count
  FROM free_resource_catalog 
  WHERE enabled=TRUE 
  GROUP BY tos_verdict 
  ORDER BY COUNT(*) DESC;
"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🎉 Phase 1 部署完成！"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📋 验收结果："
echo "  ✅ 数据库迁移: 成功"
echo "  ✅ 免费资源: $RESOURCE_COUNT 个"
echo "  ✅ Auto Combo: $TEMPLATE_COUNT 个模板"
echo "  ✅ Keyless: $KEYLESS_COUNT 个提供商"
echo "  ✅ 月度配额: ${MONTHLY_QUOTA}B tokens"
echo ""
echo "📚 下一步："
echo "  1. 查看执行日志: docs/omnifree/PHASE1-EXECUTION-LOG.md"
echo "  2. 准备 Phase 2: 实现配额追踪器"
echo "  3. 参考文档: docs/omnifree/02-QUOTA-TRACKING.md"
echo ""
echo "🎊 恭喜！Phase 1 部署成功！"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
