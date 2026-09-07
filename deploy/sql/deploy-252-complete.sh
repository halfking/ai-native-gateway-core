#!/bin/bash
# 在252服务器上执行此脚本
# SSH连接: ssh -p 25022 192.168.1.252
#
# 2026-09-07: 252 与本机一样，容器内唯一的登录角色是 llm_gateway，
# 不是 postgres。脚本里所有 psql 调用统一走 PG_USER 模板，与
# configs/env-252.sh 对齐。

set -e
PG_USER="${LLM_GATEWAY_PG_USER:-llm_gateway}"

echo "=========================================="
echo "供应商画像系统 - 252服务器部署"
echo "=========================================="
echo ""

# 1. 拉取最新代码
echo "步骤 1: 拉取最新代码..."
cd /path/to/llm-gateway-go  # 请修改为实际路径
git pull origin main
echo "✓ 代码已更新"
echo ""

# 2. 执行数据库迁移
echo "步骤 2: 执行数据库迁移..."
psql -h localhost -U "$PG_USER" -d llm_gateway \
  -f deploy/sql/migrations/2026-07-26-provider-profile-system.sql

if [ $? -eq 0 ]; then
    echo "✓ 数据库迁移成功"
else
    echo "✗ 数据库迁移失败"
    exit 1
fi
echo ""

# 3. 验证表创建
echo "步骤 3: 验证表创建..."
TABLE_COUNT=$(psql -h localhost -U "$PG_USER" -d llm_gateway -t -c \
  "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'provider_profile%';")
TABLE_COUNT=$(echo $TABLE_COUNT | xargs)
echo "创建了 $TABLE_COUNT 个表（期望: 5）"

if [ "$TABLE_COUNT" -ge "5" ]; then
    echo "✓ 表创建验证通过"
else
    echo "✗ 表数量不足"
fi
echo ""

# 4. 验证索引创建
echo "步骤 4: 验证索引创建..."
INDEX_COUNT=$(psql -h localhost -U "$PG_USER" -d llm_gateway -t -c \
  "SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname LIKE '%provider_profile%';")
INDEX_COUNT=$(echo $INDEX_COUNT | xargs)
echo "创建了 $INDEX_COUNT 个索引（期望: 10+）"
echo ""

# 5. 验证 credentials 表扩展
echo "步骤 5: 验证 credentials 表扩展..."
AUTO_COLS=$(psql -h localhost -U "$PG_USER" -d llm_gateway -t -c \
  "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='credentials' AND column_name LIKE 'auto_%';")
AUTO_COLS=$(echo $AUTO_COLS | xargs)
echo "添加了 $AUTO_COLS 个自动处理字段（期望: 4）"
echo ""

# 6. 检查配置
echo "步骤 6: 检查/添加配置..."
psql -h localhost -U "$PG_USER" -d llm_gateway <<EOF
-- 确保 settings 表存在
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 插入或更新配置
INSERT INTO settings (key, value, updated_at)
VALUES 
  ('provider_profile.enabled', 'true', NOW()),
  ('provider_profile.collection_interval', '7200', NOW()),
  ('provider_profile.aggregation_interval', '86400', NOW()),
  ('provider_profile.cleanup_interval', '604800', NOW())
ON CONFLICT (key) DO UPDATE 
  SET value = EXCLUDED.value, updated_at = NOW();

-- 显示当前配置
SELECT key, value FROM settings WHERE key LIKE 'provider_profile%' ORDER BY key;
EOF
echo "✓ 配置已设置"
echo ""

# 7. 重启网关
echo "步骤 7: 重启网关服务..."
echo "请手动执行以下命令之一："
echo ""
echo "  # 使用 systemd"
echo "  sudo systemctl restart llm-gateway"
echo ""
echo "  # 或查找并优雅重启"
echo "  ps aux | grep llm-gateway"
echo "  kill -TERM <pid>"
echo ""

echo "=========================================="
echo "部署完成！"
echo "=========================================="
echo ""
echo "下一步："
echo "  1. 重启网关服务"
echo "  2. 查看日志: tail -f /var/log/llm-gateway/gateway.log | grep 'provider profile'"
echo "  3. 等待2小时后检查数据: SELECT COUNT(*) FROM provider_profile_metrics;"
echo ""
