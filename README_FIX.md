# 路由问题修复指南

## 问题症状
使用 llm-gateway-go 代理的任何模型都找不到可用的节点。

## 快速修复（推荐）

### 方法 1: 自动诊断和修复
```bash
# 在项目根目录运行
./diagnose_and_fix.sh
```

这个脚本会：
1. ✅ 检查数据库连接
2. ✅ 检查 `v_routable_credential_models` 视图是否存在
3. ✅ 分析可路由节点数量和原因
4. ✅ 提供自动修复选项

### 方法 2: 手动执行 SQL 修复
```bash
# 如果你知道数据库可以连接
psql -d llm_gateway -f fix_routing_issue.sql
```

## 问题根本原因

在 `provider/client.go:696` 行的 SQL 查询中：
```sql
LEFT JOIN v_routable_credential_models v
   ON v.credential_id = mo.credential_id
  AND v.raw_model_name = mo.raw_model_name
WHERE ...
  AND v.is_routable = TRUE  -- 这里！
```

**问题分析：**

1. **视图不存在** → LEFT JOIN 返回 NULL → `NULL = TRUE` 为 FALSE → 所有节点被过滤
2. **视图存在但所有记录都是 `is_routable = FALSE`** → 所有节点被过滤
3. **底层表状态错误** → credentials/providers/model_offers 被标记为不可用

## 详细诊断步骤

如果自动脚本无法运行（数据库连接问题等），请手动执行以下步骤：

### 1. 检查视图是否存在
```sql
-- 连接数据库
psql -d llm_gateway

-- 检查视图
\dv v_routable_credential_models

-- 如果返回 "Did not find any relation named..."，说明视图不存在
```

### 2. 如果视图不存在，创建它
```sql
-- 执行修复 SQL
\i fix_routing_issue.sql
```

### 3. 如果视图存在，检查数据
```sql
-- 查看可路由节点数量
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_routable = TRUE) as routable,
    COUNT(*) FILTER (WHERE is_routable = FALSE) as not_routable
FROM v_routable_credential_models
WHERE tenant_id = 'default';
```

如果 `routable` 为 0，继续：

```sql
-- 查看不可路由的原因
SELECT 
    unavailable_reason,
    COUNT(*) as count
FROM v_routable_credential_models
WHERE tenant_id = 'default' AND is_routable = FALSE
GROUP BY unavailable_reason
ORDER BY count DESC;
```

### 4. 根据原因修复

#### 原因: provider_disabled 或 provider_manual_disabled
```sql
UPDATE providers
SET enabled = TRUE, manual_disabled = FALSE
WHERE tenant_id = 'default';
```

#### 原因: availability_* 或 credential_status_*
```sql
UPDATE credentials
SET 
    availability_state = 'ready',
    availability_recover_at = NULL,
    quota_state = 'ok',
    quota_recover_at = NULL,
    lifecycle_status = 'active',
    status = 'active',
    circuit_state = 'closed',
    consecutive_failures = 0,
    cooling_until = NULL
WHERE tenant_id = 'default';
```

#### 原因: offer_unavailable
```sql
UPDATE model_offers mo
SET 
    available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL
FROM credentials c
WHERE mo.credential_id = c.id
AND c.tenant_id = 'default'
AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%';
```

### 5. 验证修复
```sql
-- 应该看到可路由节点 > 0
SELECT COUNT(*) as routable_nodes
FROM v_routable_credential_models
WHERE tenant_id = 'default' AND is_routable = TRUE;

-- 查看一些样本数据
SELECT provider_id, credential_id, raw_model_name, is_routable
FROM v_routable_credential_models
WHERE tenant_id = 'default' AND is_routable = TRUE
LIMIT 10;
```

## 修复后的步骤

1. **重启应用**以清除缓存：
   ```bash
   # 如果使用 systemd
   sudo systemctl restart llm-gateway
   
   # 或者直接重启进程
   pkill -f llm-gateway
   ./llm-gateway
   ```

2. **测试路由**：
   ```bash
   # 使用 API 测试
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer YOUR_API_KEY" \
     -d '{
       "model": "gpt-4",
       "messages": [{"role": "user", "content": "Hello"}]
     }'
   ```

3. **检查日志**以确认节点被正确选择

## 预防措施

1. **监控可路由节点数量**：
   ```sql
   -- 添加到监控系统
   SELECT COUNT(*) FROM v_routable_credential_models 
   WHERE tenant_id = 'default' AND is_routable = TRUE;
   ```

2. **定期检查视图**：
   ```bash
   # 加入到健康检查
   psql -d llm_gateway -c "SELECT COUNT(*) FROM v_routable_credential_models WHERE is_routable = TRUE;"
   ```

3. **审查状态更新逻辑**：
   - 检查 `credentialstate/writer.go` 中的错误处理
   - 确保不会因为单个模型失败而标记整个凭据不可用

## 相关文件

- `fix_routing_issue.sql` - 视图创建和修复 SQL
- `diagnose_and_fix.sh` - 自动诊断和修复脚本
- `diagnose_routing.md` - 详细诊断指南
- `provider/client.go:617-756` - loadCandidatesDB 函数（问题源头）
- `routing/router.go:108-133` - filterByRouteNodeHealth 函数
- `credentialstate/writer.go` - 凭据状态更新逻辑

## 需要帮助？

如果问题仍然存在，请检查：

1. **数据库中是否有任何凭据和模型**：
   ```sql
   SELECT COUNT(*) FROM providers WHERE tenant_id = 'default';
   SELECT COUNT(*) FROM credentials WHERE tenant_id = 'default';
   SELECT COUNT(*) FROM model_offers;
   ```
   如果都是 0，需要先添加 provider 和 credential 数据。

2. **应用日志**中是否有详细的错误信息：
   ```bash
   # 查找路由相关日志
   grep -i "routing\|candidate\|routable" logs/app.log
   ```

3. **Redis 连接**（如果使用了 RouteNodeStore）：
   ```bash
   redis-cli ping
   ```
