# Phase 1 执行指南：数据库迁移与种子数据导入

**执行日期**: 2026-08-07  
**预计时间**: 3 工作日  
**优先级**: P0（必须完成）

---

## 📋 前置条件检查

### 1. 环境准备

```bash
# 检查 PostgreSQL 连接
psql --version
# 预期: PostgreSQL 17.x

# 设置数据库连接（根据实际环境选择）
export DB_URL="postgres://kxuser:kxpass@127.0.0.1:5432/llm_gateway?sslmode=disable"

# 测试连接
psql $DB_URL -c "SELECT version();"
```

### 2. 文件清单

确认以下文件存在：

```bash
✅ sql/migrations/075-omnifree-schema.sql       (15KB)
✅ sql/migrations/075-omnifree-schema.down.sql  (1.6KB)
✅ docs/omnifree/seed/free_resource_catalog.json (9.2KB)
✅ docs/omnifree/seed/auto_combo_templates.json  (4.0KB)
✅ docs/omnifree/seed/keyless_providers.json     (1.3KB)
✅ cmd/seed-free-resources/main.go               (10.6KB)
✅ scripts/omnifree/deploy.sh                    (4.7KB)
✅ scripts/omnifree/healthcheck.sh               (5.2KB)
```

---

## 🚀 执行步骤

### Step 1: 数据库迁移（15分钟）

#### 1.1 备份当前数据库（可选但推荐）

```bash
# 备份整个数据库
pg_dump $DB_URL > backup_before_omnifree_$(date +%Y%m%d_%H%M%S).sql

# 或仅备份相关表（如果已有）
pg_dump $DB_URL \
  -t provider_catalog \
  -t credentials \
  -t model_offers \
  > backup_provider_credentials_$(date +%Y%m%d_%H%M%S).sql
```

#### 1.2 执行迁移

```bash
# 执行迁移脚本
psql $DB_URL -f sql/migrations/075-omnifree-schema.sql

# 预期输出：
# CREATE TABLE
# CREATE INDEX
# CREATE INDEX
# ...
# COMMENT
```

#### 1.3 验证表创建

```bash
# 检查新表是否创建成功
psql $DB_URL -c "
  SELECT table_name, 
         pg_size_pretty(pg_total_relation_size(quote_ident(table_name)::regclass)) as size
  FROM information_schema.tables 
  WHERE table_schema = 'public' 
    AND (table_name LIKE 'free_%' 
         OR table_name LIKE 'auto_combo%' 
         OR table_name = 'keyless_providers')
  ORDER BY table_name;
"

# 预期输出：
# table_name                | size
# --------------------------|------
# auto_combo_templates      | 8192 bytes
# free_quota_tracker        | 8192 bytes
# free_resource_catalog     | 8192 bytes
# keyless_providers         | 8192 bytes
```

#### 1.4 验证索引

```bash
psql $DB_URL -c "
  SELECT schemaname, tablename, indexname 
  FROM pg_indexes 
  WHERE tablename IN ('free_resource_catalog', 'free_quota_tracker', 'auto_combo_templates', 'keyless_providers')
  ORDER BY tablename, indexname;
"

# 预期输出：应看到约 20 个索引
```

---

### Step 2: 编译种子导入工具（5分钟）

```bash
# 进入工具目录
cd cmd/seed-free-resources

# 安装依赖（如果需要）
go mod download

# 编译
go build -o seed-free-resources main.go

# 验证编译成功
./seed-free-resources --help

# 预期输出：
# Usage of ./seed-free-resources:
#   --db-url string
#         Database connection URL (required)
#   --catalog string
#         Path to free_resource_catalog.json (required)
#   --templates string
#         Path to auto_combo_templates.json (required)
#   --keyless string
#         Path to keyless_providers.json (required)
#   --tenant-id int
#         Tenant ID (default: 1)
```

---

### Step 3: 导入种子数据（10分钟）

#### 3.1 导入免费资源目录（15个）

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2

./cmd/seed-free-resources/seed-free-resources \
  --db-url "$DB_URL" \
  --catalog docs/omnifree/seed/free_resource_catalog.json \
  --templates docs/omnifree/seed/auto_combo_templates.json \
  --keyless docs/omnifree/seed/keyless_providers.json \
  --tenant-id 1

# 预期输出：
# ✅ Imported 15 free resources
# ✅ Imported 6 auto combo templates
# ✅ Imported 3 keyless providers
# 🎉 Seed data import completed successfully!
```

#### 3.2 验证导入结果

```bash
# 检查免费资源数量
psql $DB_URL -c "
  SELECT COUNT(*) as total_resources,
         COUNT(*) FILTER (WHERE enabled = TRUE) as enabled_resources,
         COUNT(DISTINCT provider_code) as provider_count
  FROM free_resource_catalog;
"

# 预期输出：
# total_resources | enabled_resources | provider_count
# ----------------|-------------------|-----------------
#              15 |                15 |             10

# 检查配额总量
psql $DB_URL -c "
  SELECT 
    SUM(monthly_tokens) FILTER (WHERE free_type = 'recurring-monthly') / 1000000000.0 as monthly_tokens_b,
    SUM(daily_tokens) FILTER (WHERE free_type = 'recurring-daily') / 1000000.0 as daily_tokens_m,
    COUNT(*) FILTER (WHERE free_type = 'keyless') as keyless_count
  FROM free_resource_catalog
  WHERE enabled = TRUE;
"

# 预期输出：
# monthly_tokens_b | daily_tokens_m | keyless_count
# -----------------|----------------|---------------
#             1.27 |          16.40 |             1

# 检查 ToS 分布
psql $DB_URL -c "
  SELECT tos_verdict, COUNT(*) 
  FROM free_resource_catalog 
  WHERE enabled = TRUE 
  GROUP BY tos_verdict 
  ORDER BY COUNT(*) DESC;
"

# 预期输出：
# tos_verdict | count
# ------------|-------
# ok          |    12
# caution     |     2
# ambiguous   |     1

# 检查 Auto Combo 模板
psql $DB_URL -c "
  SELECT combo_name, variant, enabled 
  FROM auto_combo_templates 
  ORDER BY combo_name;
"

# 预期输出：6 行（auto/free, auto/best-free, auto/coding:free, ...）

# 检查 Keyless 提供商
psql $DB_URL -c "
  SELECT provider_code, enabled, allowlist_in_auto_combo 
  FROM keyless_providers;
"

# 预期输出：3 行（opencode, duckduckgo-web, felo）
```

---

### Step 4: 验证 RLS 策略（5分钟）

```bash
# 测试 RLS 隔离
psql $DB_URL <<EOF
-- 设置租户上下文
SET app.current_tenant_id = '1';

-- 应该能看到数据
SELECT COUNT(*) FROM free_resource_catalog;

-- 切换到不存在的租户
SET app.current_tenant_id = '999';

-- 应该看不到数据
SELECT COUNT(*) FROM free_resource_catalog;

-- 重置
RESET app.current_tenant_id;
EOF

# 预期：tenant_id=1 时能看到数据，tenant_id=999 时看不到
```

---

### Step 5: 运行健康检查（5分钟）

```bash
# 设置必要的环境变量
export DB_URL="postgres://kxuser:kxpass@127.0.0.1:5432/llm_gateway?sslmode=disable"

# 运行健康检查脚本
./scripts/omnifree/healthcheck.sh

# 预期输出：
# 🔍 OmniFree 健康检查
# ✅ 已启用免费资源: 15
# ✅ 当日可用配额窗口: 0 (正常，尚未产生追踪记录)
# ✅ 已启用 Auto Combo 模板: 6
# ✅ Keyless 提供商: 3
# 🎉 健康检查通过!
```

---

## ✅ 验收标准

Phase 1 完成后，以下所有检查项应为 ✅：

### 数据库表
- [x] `free_resource_catalog` 表创建成功
- [x] `free_quota_tracker` 表创建成功
- [x] `auto_combo_templates` 表创建成功
- [x] `keyless_providers` 表创建成功

### 索引与约束
- [x] 所有表的主键索引创建
- [x] 所有表的外键约束创建
- [x] 所有表的 CHECK 约束生效
- [x] 所有表的唯一约束生效

### RLS 策略
- [x] 所有表启用 RLS
- [x] tenant_id 隔离生效

### 种子数据
- [x] 15 个免费资源导入成功
- [x] 6 个 Auto Combo 模板导入成功
- [x] 3 个 Keyless 提供商导入成功
- [x] 总配额 ~1.27B tokens/月

### 功能验证
- [x] 去重查询正常工作
- [x] ToS 分级正确
- [x] 健康检查脚本通过

---

## 🐛 故障排除

### 问题1：迁移脚本执行失败

**症状**: `ERROR: relation "xxx" already exists`

**解决**:
```bash
# 检查是否有遗留表
psql $DB_URL -c "\dt free_*"

# 如果需要重新迁移，先回滚
psql $DB_URL -f sql/migrations/075-omnifree-schema.down.sql

# 再重新执行
psql $DB_URL -f sql/migrations/075-omnifree-schema.sql
```

### 问题2：种子数据导入失败

**症状**: `ERROR: duplicate key value violates unique constraint`

**解决**:
```bash
# 种子导入工具使用 ON CONFLICT DO UPDATE，应该不会失败
# 如果失败，检查 JSON 文件格式
cat docs/omnifree/seed/free_resource_catalog.json | jq '.' > /dev/null
echo $?  # 应该返回 0

# 如果需要重新导入，先清空
psql $DB_URL -c "
  DELETE FROM free_resource_catalog WHERE tenant_id = 1;
  DELETE FROM auto_combo_templates WHERE tenant_id = 1;
  DELETE FROM keyless_providers WHERE tenant_id = 1;
"
```

### 问题3：RLS 测试失败

**症状**: 切换租户后仍能看到数据

**解决**:
```bash
# 检查 RLS 是否启用
psql $DB_URL -c "
  SELECT tablename, rowsecurity 
  FROM pg_tables 
  WHERE tablename LIKE 'free_%' 
     OR tablename LIKE 'auto_combo%' 
     OR tablename = 'keyless_providers';
"

# 应该显示 rowsecurity = true
# 如果为 false，重新执行迁移脚本
```

---

## 📊 Phase 1 完成报告

完成后填写：

```
Phase 1 执行报告
==================

执行日期: ___________
执行人: ___________
数据库环境: [ ] 本地 [ ] 开发 [ ] 测试
数据库 URL: ___________

执行结果:
- 迁移脚本执行: [ ] 成功 [ ] 失败（原因: _______）
- 种子数据导入: [ ] 成功 [ ] 失败（原因: _______）
- RLS 策略验证: [ ] 通过 [ ] 失败（原因: _______）
- 健康检查: [ ] 通过 [ ] 失败（原因: _______）

数据统计:
- 免费资源数量: _______（预期: 15）
- Auto Combo 模板: _______（预期: 6）
- Keyless 提供商: _______（预期: 3）
- 月度配额总量: _______ B tokens（预期: ~1.27B）

问题记录:
___________________________________________
___________________________________________

下一步:
[ ] 进入 Phase 2: 实现配额追踪器
```

---

## 🎯 下一步：Phase 2

Phase 1 完成并验收通过后，立即启动 Phase 2：

**目标**: 实现本地配额追踪 + 429 校准  
**时间**: Day 4-8（5 工作日）  
**文档**: 参考 `docs/omnifree/02-QUOTA-TRACKING.md`

---

**📞 支持**: 如遇到问题，查阅 `docs/omnifree/00-AUDIT-AND-IMPLEMENTATION.md`
