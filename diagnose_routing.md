# 路由问题诊断和修复指南

## 问题描述
所有模型都找不到可用的节点，怀疑路由策略无法正确判断可用的凭据节点。

## 根本原因分析

在 `provider/client.go:696` 行，loadCandidatesDB 的 SQL 查询中有这个关键过滤条件：

```sql
AND v.is_routable = TRUE
```

这个条件要求 `v_routable_credential_models` 视图必须返回 `is_routable = TRUE`。

**问题出现的可能原因：**

1. **视图不存在** - LEFT JOIN 导致 v.is_routable 为 NULL，`NULL = TRUE` 返回 FALSE
2. **视图存在但所有记录的 is_routable 都是 FALSE** - 所有节点被过滤
3. **视图逻辑错误** - 判断条件过于严格

## 诊断步骤

### 1. 检查视图是否存在

```sql
-- 连接数据库
psql -d llm_gateway

-- 检查视图
\dv v_routable_credential_models

-- 或者用 SQL
SELECT table_name, table_type 
FROM information_schema.tables 
WHERE table_name = 'v_routable_credential_models';
```

### 2. 如果视图存在，检查数据

```sql
-- 查看总记录数和可路由数量
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_routable = TRUE) as routable,
    COUNT(*) FILTER (WHERE is_routable = FALSE) as not_routable,
    COUNT(*) FILTER (WHERE is_routable IS NULL) as null_routable
FROM v_routable_credential_models
WHERE tenant_id = 'default';

-- 查看不可路由的原因
SELECT 
    unavailable_reason,
    COUNT(*) as count
FROM v_routable_credential_models
WHERE tenant_id = 'default' AND is_routable = FALSE
GROUP BY unavailable_reason
ORDER BY count DESC;
```

### 3. 检查底层表的数据

```sql
-- 检查 providers 表
SELECT 
    id, 
    display_name, 
    enabled, 
    COALESCE(manual_disabled, FALSE) as manual_disabled
FROM providers 
WHERE tenant_id = 'default' 
ORDER BY id;

-- 检查 credentials 表
SELECT 
    id,
    provider_id,
    label,
    status,
    lifecycle_status,
    availability_state,
    quota_state,
    COALESCE(manual_disabled, FALSE) as manual_disabled
FROM credentials
WHERE tenant_id = 'default'
ORDER BY provider_id, id;

-- 检查 model_offers 表
SELECT 
    credential_id,
    raw_model_name,
    available,
    unavailable_reason,
    COALESCE(admin_protected, FALSE) as admin_protected
FROM model_offers
WHERE credential_id IN (SELECT id FROM credentials WHERE tenant_id = 'default')
LIMIT 20;
```

## 快速修复方案

### 方案A：如果视图不存在，执行修复SQL

```bash
cd __DEV_HOME__/workspace/llm-gateway-go-2
psql -d llm_gateway -f fix_routing_issue.sql
```

### 方案B：手动恢复所有凭据和模型的可用性

如果所有节点的 is_routable 都是 FALSE，可能是因为之前的错误导致所有节点被标记为不可用。

```sql
-- 1. 恢复所有 credentials 的状态
UPDATE credentials
SET 
    availability_state = 'ready',
    availability_recover_at = NULL,
    quota_state = 'ok',
    quota_recover_at = NULL,
    lifecycle_status = 'active',
    status = 'active',
    manual_disabled = FALSE,
    circuit_state = 'closed',
    consecutive_failures = 0,
    cooling_until = NULL,
    state_reason_code = NULL,
    state_reason_detail = NULL,
    state_updated_at = now()
WHERE tenant_id = 'default'
AND lifecycle_status != 'archived';

-- 2. 恢复所有 model_offers 的可用性
UPDATE model_offers mo
SET 
    available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL,
    unavailable_recover_at = NULL,
    admin_protected = FALSE
FROM credentials c
WHERE mo.credential_id = c.id
AND c.tenant_id = 'default'
AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%';

-- 3. 恢复所有 credential_model_bindings 的可用性（如果表存在）
UPDATE credential_model_bindings cmb
SET 
    available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL,
    unavailable_recover_at = NULL,
    admin_protected = FALSE
FROM credentials c
WHERE cmb.credential_id = c.id
AND c.tenant_id = 'default'
AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%';

-- 4. 启用所有 providers
UPDATE providers
SET 
    enabled = TRUE,
    manual_disabled = FALSE
WHERE tenant_id = 'default';

-- 5. 使缓存失效（如果使用了缓存）
-- 这通常需要重启应用或调用缓存清理API
```

### 方案C：临时绕过视图检查（仅用于紧急情况）

如果需要立即恢复服务，可以临时修改代码绕过视图检查：

在 `provider/client.go` 第 696 行，注释掉或修改过滤条件：

```go
// 临时禁用视图检查
// AND v.is_routable = TRUE
```

**警告：这只是临时方案，会跳过重要的健康检查！**

## 验证修复

执行修复后，运行以下命令验证：

```sql
-- 1. 检查可路由节点数量
SELECT COUNT(*) 
FROM v_routable_credential_models 
WHERE tenant_id = 'default' 
AND is_routable = TRUE;

-- 2. 测试一个具体的模型
SELECT 
    provider_id,
    credential_id,
    raw_model_name,
    is_routable,
    unavailable_reason
FROM v_routable_credential_models
WHERE tenant_id = 'default'
AND raw_model_name ILIKE '%gpt-4%'  -- 替换为你要测试的模型
LIMIT 10;
```

## 预防措施

1. **定期检查视图状态**：添加监控检查 is_routable = TRUE 的节点数量
2. **审查状态更新逻辑**：确保 `credentialstate/writer.go` 中的状态更新不会错误地标记健康节点
3. **添加日志**：在路由失败时记录详细的过滤原因
4. **数据库迁移**：确保 v_routable_credential_models 视图在数据库初始化时正确创建

## 相关文件

- `provider/client.go:617-756` - loadCandidatesDB 函数
- `routing/router.go:108-133` - filterByRouteNodeHealth 函数
- `credentialstate/writer.go` - 凭据状态更新逻辑
- `admin/providers.go:553` - 视图使用示例
