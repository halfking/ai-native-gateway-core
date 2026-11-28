# 计费系统数据库迁移指南

## 迁移文件说明

### 001_create_billing_tables.sql
创建分层计费系统所需的所有数据库表：
- `billing_configs`: 计费配置表
- `api_key_billing_map`: API Key映射表
- `billing_plans`: 套餐定义表
- `booster_packs`: 增值包表

### 001_rollback.sql
回滚001迁移的所有更改

## 执行迁移

### 方法1：直接执行SQL
```bash
# 执行迁移
psql -h <host> -U <user> -d <database> -f migrations/001_create_billing_tables.sql

# 回滚迁移
psql -h <host> -U <user> -d <database> -f migrations/001_rollback.sql
```

### 方法2：使用golang-migrate
```bash
# 安装工具
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# 执行迁移
migrate -database "postgresql://user:pass@host:5432/dbname?sslmode=disable" \
        -path ./migrations up

# 回滚一个版本
migrate -database "postgresql://user:pass@host:5432/dbname?sslmode=disable" \
        -path ./migrations down 1
```

## 验证迁移

```sql
-- 检查表是否创建成功
SELECT table_name FROM information_schema.tables 
WHERE table_schema = 'public' 
AND table_name IN ('billing_configs', 'api_key_billing_map', 'billing_plans', 'booster_packs');

-- 检查索引
SELECT indexname, tablename FROM pg_indexes 
WHERE schemaname = 'public' 
AND tablename LIKE 'billing_%' OR tablename LIKE 'booster_%';

-- 检查默认套餐数据
SELECT plan_id, plan_name, monthly_quota, base_price_cents 
FROM billing_plans 
WHERE is_active = true;
```

## 注意事项

1. **备份数据库**：执行迁移前务必备份
2. **测试环境验证**：先在测试环境执行并验证
3. **权限检查**：确保数据库用户有CREATE TABLE权限
4. **依赖检查**：如果api_keys表不存在，需要先创建或修改外键约束

## 迁移后配置

迁移完成后，需要：
1. 插入初始计费配置（通过Admin API）
2. 配置定时任务重置小时统计
3. 配置定时任务处理增值包过期
