# OmniFree 数据层验证指南

**验证目标**: 确保迁移、seed、RLS、配额查询功能正常工作  
**执行环境**: 测试数据库  
**前置条件**: 已轮换泄露凭据

---

## 第一步：环境准备

### 1.1 轮换泄露凭据（紧急）

```sql
-- 连接到 PostgreSQL
psql -h 172.16.2.210 -U postgres -d llm_gateway

-- 轮换密码
ALTER USER kxuser WITH PASSWORD '<生成的新强密码>';

-- 验证
\du kxuser
```

### 1.2 设置环境变量

```bash
# 使用新密码
export OMNIFREE_DATABASE_URL='postgres://kxuser:<新密码>@172.16.2.210:5432/llm_gateway?sslmode=disable'

# 验证连接
psql "$OMNIFREE_DATABASE_URL" -c "SELECT version();"
```

---

## 第二步：执行数据库迁移

### 2.1 执行迁移

```bash
# 进入项目根目录
cd /path/to/llm-gateway-go-2

# 执行迁移（带事务和错误停止）
psql "$OMNIFREE_DATABASE_URL" -v ON_ERROR_STOP=1 \
  -f sql/migrations/075-omnifree-schema.sql
```

**预期输出**：
```
BEGIN
CREATE TABLE
CREATE INDEX
...
CREATE POLICY
COMMIT
NOTICE: ✅ OmniFree 数据模型迁移完成！
```

### 2.2 验证迁移结果

```bash
# 检查表是否创建
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT tablename FROM pg_tables 
WHERE schemaname = 'public' 
  AND tablename IN ('free_resource_catalog', 'free_quota_tracker', 
                    'auto_combo_templates', 'keyless_providers')
ORDER BY tablename;
"
```

**预期输出**：4 张表
```
        tablename        
-------------------------
 auto_combo_templates
 free_quota_tracker
 free_resource_catalog
 keyless_providers
```

### 2.3 验证 RLS 策略

```bash
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT schemaname, tablename, policyname 
FROM pg_policies 
WHERE tablename LIKE 'free_%' OR tablename LIKE '%combo%' OR tablename LIKE 'keyless%'
ORDER BY tablename, policyname;
"
```

**预期输出**：每张表至少 1 个 policy

---

## 第三步：导入 seed 数据

### 3.1 编译导入工具

```bash
go build -o /tmp/seed-free-resources ./cmd/seed-free-resources
```

### 3.2 执行导入

```bash
/tmp/seed-free-resources \
  --db-url="$OMNIFREE_DATABASE_URL" \
  --tenant-id="default" \
  --catalog=docs/omnifree/seed/free_resource_catalog.json \
  --templates=docs/omnifree/seed/auto_combo_templates.json \
  --keyless=docs/omnifree/seed/keyless_providers.json
```

**预期输出**：
```
✅ 数据库连接成功
✅ 已导入 15 个免费资源
✅ 已导入 6 个 Auto Combo 模板
✅ 已导入 3 个 Keyless 提供商
```

### 3.3 验证导入结果

```bash
# 验证资源数量
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT 
  (SELECT COUNT(*) FROM free_resource_catalog) AS resources,
  (SELECT COUNT(*) FROM auto_combo_templates) AS templates,
  (SELECT COUNT(*) FROM keyless_providers) AS keyless;
"
```

**预期输出**：
```
 resources | templates | keyless 
-----------+-----------+---------
        15 |         6 |       3
```

### 3.4 验证配额总量

```bash
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT 
  ROUND(SUM(monthly_tokens)::numeric / 1000000000, 2) AS monthly_gb,
  ROUND(SUM(daily_tokens)::numeric / 1000000, 2) AS daily_mb,
  COUNT(*) FILTER (WHERE tos_verdict = 'ok') AS tos_ok,
  COUNT(*) FILTER (WHERE free_type = 'keyless') AS keyless_count
FROM free_resource_catalog 
WHERE enabled = TRUE;
"
```

**预期输出**（近似）：
```
 monthly_gb | daily_mb | tos_ok | keyless_count 
------------+----------+--------+---------------
       1.18 |    15.75 |     11 |             3
```

---

## 第四步：功能验证

### 4.1 测试 Resolver（内置模板）

```bash
# 编译测试
go test ./domains/autocombo -v -run TestResolver_BuiltinTemplates
```

**预期输出**：
```
=== RUN   TestResolver_BuiltinTemplates
=== RUN   TestResolver_BuiltinTemplates/auto/free
=== RUN   TestResolver_BuiltinTemplates/auto/best-free
=== RUN   TestResolver_BuiltinTemplates/auto/coding:free
=== RUN   TestResolver_BuiltinTemplates/auto/reasoning:free
=== RUN   TestResolver_BuiltinTemplates/auto/fast:free
=== RUN   TestResolver_BuiltinTemplates/auto/creative:free
--- PASS: TestResolver_BuiltinTemplates (0.XXs)
PASS
```

### 4.2 测试租户隔离

```bash
# 设置租户上下文
psql "$OMNIFREE_DATABASE_URL" -c "
SET app.current_tenant = 'tenant-a';
SELECT COUNT(*) FROM free_resource_catalog;
"

# 切换租户
psql "$OMNIFREE_DATABASE_URL" -c "
SET app.current_tenant = 'tenant-b';
SELECT COUNT(*) FROM free_resource_catalog;
"
```

**预期行为**：
- tenant-a 看到 tenant-a 的数据
- tenant-b 看到 tenant-b 的数据或 0 行（如果未导入）

### 4.3 测试配额窗口计算

```bash
go test ./domains/freeresource -v -run TestWindowBoundaries
```

**预期输出**：
```
=== RUN   TestWindowBoundaries
=== RUN   TestWindowBoundaries/day-1_mid-day
=== RUN   TestWindowBoundaries/month-1_mid-month
--- PASS: TestWindowBoundaries (0.XXs)
PASS
```

### 4.4 查询免费资源目录

```bash
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT 
  provider_code,
  model_id,
  free_type,
  monthly_tokens,
  tos_verdict,
  enabled
FROM free_resource_catalog
WHERE tenant_id = 'default' AND enabled = TRUE
ORDER BY monthly_tokens DESC
LIMIT 5;
"
```

**预期输出**：显示前 5 个免费资源

### 4.5 查询 Auto Combo 模板

```bash
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT 
  combo_name,
  variant,
  enabled,
  max_candidates
FROM auto_combo_templates
WHERE tenant_id = 'default' AND enabled = TRUE;
"
```

**预期输出**：6 个模板（auto/free, auto/best-free, 等）

---

## 第五步：压力测试（可选）

### 5.1 批量插入配额记录

```bash
psql "$OMNIFREE_DATABASE_URL" -c "
INSERT INTO free_quota_tracker (
  credential_id, provider_code, model_id, window_type,
  window_start, window_end, request_count, token_count,
  tenant_id, updated_at
)
SELECT 
  1, 'openai', 'gpt-3.5-turbo', 'day-1',
  date_trunc('day', now()), date_trunc('day', now()) + interval '1 day',
  generate_series * 10, generate_series * 1000,
  'default', now()
FROM generate_series(1, 10);
"

# 验证
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT COUNT(*), SUM(token_count) 
FROM free_quota_tracker 
WHERE tenant_id = 'default';
"
```

### 5.2 测试配额预检性能

```bash
# 使用 EXPLAIN ANALYZE
psql "$OMNIFREE_DATABASE_URL" -c "
EXPLAIN ANALYZE
SELECT 
  request_count, 
  token_count,
  CASE WHEN request_count >= 900 THEN true ELSE false END AS is_exhausted
FROM free_quota_tracker
WHERE credential_id = 1
  AND window_type = 'day-1'
  AND window_end >= now()
  AND tenant_id = 'default';
"
```

**检查**：执行时间应 < 10ms

---

## 第六步：回滚测试（可选）

### 6.1 执行回滚

```bash
# 备份当前数据（可选）
pg_dump "$OMNIFREE_DATABASE_URL" \
  -t free_resource_catalog \
  -t free_quota_tracker \
  -t auto_combo_templates \
  -t keyless_providers \
  > /tmp/omnifree_backup.sql

# 执行回滚
psql "$OMNIFREE_DATABASE_URL" -v ON_ERROR_STOP=1 \
  -f sql/migrations/075-omnifree-schema.down.sql
```

### 6.2 验证回滚

```bash
# 检查表是否删除
psql "$OMNIFREE_DATABASE_URL" -c "
SELECT tablename FROM pg_tables 
WHERE schemaname = 'public' 
  AND tablename IN ('free_resource_catalog', 'free_quota_tracker', 
                    'auto_combo_templates', 'keyless_providers');
"
```

**预期输出**：0 行（表已删除）

### 6.3 重新迁移（如需要）

```bash
psql "$OMNIFREE_DATABASE_URL" -v ON_ERROR_STOP=1 \
  -f sql/migrations/075-omnifree-schema.sql
```

---

## 验证清单

- [ ] 1. 凭据已轮换
- [ ] 2. 数据库连接正常
- [ ] 3. 迁移执行成功（4 张表 + 索引 + RLS）
- [ ] 4. seed 导入成功（15 资源 + 6 模板 + 3 keyless）
- [ ] 5. 配额总量正确（~1.18B monthly tokens）
- [ ] 6. 内置模板测试通过
- [ ] 7. 租户隔离生效
- [ ] 8. 配额窗口计算正确
- [ ] 9. 查询性能 < 10ms
- [ ] 10. 回滚测试通过（可选）

---

## 常见问题

### Q1: 迁移报错 "column does not exist"
**A**: 检查 credentials 表是否有 `is_free_tier` 列，迁移会自动添加

### Q2: seed 导入报错 "unsupported type []string"
**A**: 确保使用修复后的代码（commit d156788f 之后）

### Q3: RLS 阻止了查询
**A**: 设置 `SET app.current_tenant = 'default'` 或使用 super_admin 角色

### Q4: 配额总量与预期不符
**A**: 检查 seed 文件版本，当前为 1.18B monthly tokens

---

## 验证通过标准

✅ **数据层验证通过** 需满足：
1. 所有迁移成功执行
2. seed 数据完整导入
3. RLS 隔离生效
4. 查询性能正常
5. 单元测试通过

达到此标准后，即可开始应用层集成。

---

**验证指南版本**: 1.0  
**最后更新**: 2026-08-07  
**下一步**: 验证通过后，按 `HANDOFF.md` 实施应用层集成
