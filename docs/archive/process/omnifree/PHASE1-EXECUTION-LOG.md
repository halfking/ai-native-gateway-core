# Phase 1 执行日志

**执行时间**: 2026-08-07  
**执行人**: ZCode AI Agent  
**目标环境**: 阿里云 252 (172.16.2.210:5432)  
**数据库**: llm_gateway

---

## 执行前准备

### 1. 备份命令（强烈建议先执行）

```bash
# 设置数据库连接（需要提供正确的密码）
export DB_252="postgres://kxuser:YOUR_PASSWORD@172.16.2.210:5432/llm_gateway?sslmode=disable"

# 备份整个数据库
pg_dump $DB_252 > backup_252_before_omnifree_$(date +%Y%m%d_%H%M%S).sql

# 或仅备份相关表（如果已存在）
pg_dump $DB_252 \
  -t provider_catalog \
  -t credentials \
  -t model_offers \
  > backup_252_related_tables_$(date +%Y%m%d_%H%M%S).sql
```

---

## Phase 1 执行步骤

### Step 1: 数据库迁移（预计15分钟）

```bash
# 设置数据库连接
export DB_252="postgres://kxuser:YOUR_PASSWORD@172.16.2.210:5432/llm_gateway?sslmode=disable"

# 测试连接
psql $DB_252 -c "SELECT version();"

# 执行迁移脚本
psql $DB_252 -f sql/migrations/075-omnifree-schema.sql

# 验证表创建
psql $DB_252 -c "
  SELECT table_name 
  FROM information_schema.tables 
  WHERE table_schema = 'public' 
    AND (table_name LIKE 'free_%' 
         OR table_name LIKE 'auto_combo%' 
         OR table_name = 'keyless_providers')
  ORDER BY table_name;
"

# 预期输出4张表:
# - auto_combo_templates
# - free_quota_tracker
# - free_resource_catalog
# - keyless_providers
```

### Step 2: 编译种子导入工具（预计5分钟）

```bash
cd cmd/seed-free-resources

# 编译
go build -o seed-free-resources main.go

# 验证
./seed-free-resources --help

# 预期输出: Usage 信息
```

### Step 3: 导入种子数据（预计10分钟）

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2

# 导入数据
./cmd/seed-free-resources/seed-free-resources \
  --db-url "$DB_252" \
  --catalog docs/omnifree/seed/free_resource_catalog.json \
  --templates docs/omnifree/seed/auto_combo_templates.json \
  --keyless docs/omnifree/seed/keyless_providers.json \
  --tenant-id 1

# 预期输出:
# ✅ Imported 15 free resources
# ✅ Imported 6 auto combo templates
# ✅ Imported 3 keyless providers
# 🎉 Seed data import completed successfully!
```

### Step 4: 验证数据（预计5分钟）

```bash
# 验证免费资源数量
psql $DB_252 -c "
  SELECT 
    COUNT(*) as total_resources,
    COUNT(*) FILTER (WHERE enabled = TRUE) as enabled_resources,
    COUNT(DISTINCT provider_code) as provider_count
  FROM free_resource_catalog;
"
# 预期: total_resources=15, enabled_resources=15, provider_count=10

# 验证配额总量
psql $DB_252 -c "
  SELECT 
    SUM(monthly_tokens) FILTER (WHERE free_type='recurring-monthly') / 1000000000.0 as monthly_b,
    SUM(daily_tokens) FILTER (WHERE free_type='recurring-daily') / 1000000.0 as daily_m,
    COUNT(*) FILTER (WHERE free_type='keyless') as keyless_count
  FROM free_resource_catalog
  WHERE enabled=TRUE;
"
# 预期: monthly_b≈1.27, daily_m≈16.4, keyless_count=1

# 验证 ToS 分布
psql $DB_252 -c "
  SELECT tos_verdict, COUNT(*) 
  FROM free_resource_catalog 
  WHERE enabled=TRUE 
  GROUP BY tos_verdict 
  ORDER BY COUNT(*) DESC;
"
# 预期: ok=12, caution=2, ambiguous=1

# 验证 Auto Combo 模板
psql $DB_252 -c "
  SELECT combo_name, variant, enabled 
  FROM auto_combo_templates 
  ORDER BY combo_name;
"
# 预期: 6行

# 验证 Keyless 提供商
psql $DB_252 -c "
  SELECT provider_code, enabled, allowlist_in_auto_combo 
  FROM keyless_providers;
"
# 预期: 3行
```

### Step 5: RLS 策略验证（预计5分钟）

```bash
psql $DB_252 <<EOF
-- 设置租户上下文
SET app.current_tenant_id = '1';

-- 应该能看到数据
SELECT COUNT(*) as count_tenant_1 FROM free_resource_catalog;

-- 切换到不存在的租户
SET app.current_tenant_id = '999';

-- 应该看不到数据
SELECT COUNT(*) as count_tenant_999 FROM free_resource_catalog;

-- 重置
RESET app.current_tenant_id;
EOF

# 预期: count_tenant_1 > 0, count_tenant_999 = 0
```

---

## 回滚方案（如果出现问题）

```bash
# 设置数据库连接
export DB_252="postgres://kxuser:YOUR_PASSWORD@172.16.2.210:5432/llm_gateway?sslmode=disable"

# 方案1: 使用回滚脚本（推荐）
psql $DB_252 -f sql/migrations/075-omnifree-schema.down.sql

# 方案2: 从备份恢复
psql $DB_252 < backup_252_before_omnifree_YYYYMMDD_HHMMSS.sql

# 方案3: 手动删除表
psql $DB_252 <<EOF
DROP TABLE IF EXISTS free_quota_tracker CASCADE;
DROP TABLE IF EXISTS free_resource_catalog CASCADE;
DROP TABLE IF EXISTS auto_combo_templates CASCADE;
DROP TABLE IF EXISTS keyless_providers CASCADE;

-- 恢复扩展的列
ALTER TABLE provider_catalog DROP COLUMN IF EXISTS has_free_tier;
ALTER TABLE provider_catalog DROP COLUMN IF EXISTS free_tier_notes;
ALTER TABLE provider_catalog DROP COLUMN IF EXISTS official_free_docs_url;

ALTER TABLE credentials DROP COLUMN IF EXISTS is_free_tier;
ALTER TABLE credentials DROP COLUMN IF EXISTS free_quota_window_type;
ALTER TABLE credentials DROP COLUMN IF EXISTS free_quota_limit;

ALTER TABLE model_offers DROP COLUMN IF EXISTS is_free_model;
ALTER TABLE model_offers DROP COLUMN IF EXISTS free_resource_id;
EOF
```

---

## 验收标准

Phase 1 完成后，以下所有项应为 ✅：

- [ ] 4张新表创建成功
- [ ] 15个免费资源导入成功
- [ ] 6个 Auto Combo 模板导入成功
- [ ] 3个 Keyless 提供商导入成功
- [ ] RLS 策略验证通过
- [ ] 配额总量符合预期（~1.27B tokens/月）
- [ ] ToS 分布正确

---

## 执行记录

**开始时间**: ___________  
**结束时间**: ___________  
**执行状态**: [ ] 成功 [ ] 失败 [ ] 部分成功  

**遇到的问题**:
___________________________________________
___________________________________________

**解决方案**:
___________________________________________
___________________________________________

---

## 下一步

Phase 1 完成后：
1. [ ] 填写执行记录
2. [ ] 通知团队成员
3. [ ] 准备启动 Phase 2（配额追踪实现）
4. [ ] 参考文档: `docs/omnifree/02-QUOTA-TRACKING.md`

---

**执行人签名**: ___________  
**复审人签名**: ___________  
**日期**: ___________
