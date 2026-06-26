# minimax-m3 路由问题诊断报告

**问题**: 请求 minimax-m3 模型时返回 no_candidate 错误  
**环境**: llmgateway.internal.example.com (生产环境)  
**诊断日期**: 2026-06-26  
**状态**: ❌ 配置缺失

---

## 🔍 问题诊断

### 根本原因

**minimax-m3 缺少 provider_models 映射**，导致路由链断裂：

```
客户端请求: minimax-m3
    ↓
model_aliases: ✅ 存在 (id=249, canonical_id=5704)
    ↓
provider_models: ❌ 不存在 (minimax provider 没有 minimax-m3)
    ↓
credential_model_bindings: ❌ 无法建立绑定
    ↓
路由结果: no_candidate 错误
```

### 当前配置状态

#### ✅ model_aliases (正常)
```sql
id=249, raw_name='minimax-m3', canonical_id=5704, status='active'
```

#### ❌ provider_models (缺失)
```sql
-- minimax (provider_id=14) 只有这些模型：
- abab6.5-chat   (raw_model_name, outbound_model_name)
- abab6.5s-chat  (raw_model_name, outbound_model_name)

-- 缺少：
- minimax-m3 (❌ 不存在)
```

#### ❌ credential_model_bindings (无法绑定)
```sql
-- 无法为 minimax-m3 建立 credential 绑定
-- 因为 provider_models 中不存在此模型
```

---

## 🔧 修复方案

### 方案 A: 标准修复（推荐）

假设 `minimax-m3` 对应上游 API 的 `abab7-chat`：

```sql
-- 连接到生产数据库
-- psql -h production-db -U kxuser -d llm_gateway

BEGIN;

-- 1. 添加 provider_model 映射
INSERT INTO provider_models (provider_id, raw_model_name, outbound_model_name, canonical_id)
VALUES (
    14,              -- minimax provider_id
    'minimax-m3',    -- 客户端请求的名称
    'abab7-chat',    -- 上游 API 的实际模型名 (需要确认!)
    5704             -- canonical_id (从 model_aliases)
)
ON CONFLICT DO NOTHING;

-- 2. 为所有活跃的 minimax 凭据添加 minimax-m3 绑定
INSERT INTO credential_model_bindings (credential_id, provider_model_id, routing_tier, weight, available)
SELECT 
    c.id AS credential_id,
    pm.id AS provider_model_id,
    1 AS routing_tier,        -- 默认 tier 1
    100 AS weight,
    true AS available
FROM credentials c
CROSS JOIN provider_models pm
WHERE c.provider_id = 14                    -- minimax
  AND c.status = 'active'
  AND c.availability_state = 'ready'
  AND pm.raw_model_name = 'minimax-m3'
  AND pm.provider_id = 14
ON CONFLICT (credential_id, provider_model_id) DO NOTHING;

-- 3. 验证添加结果
SELECT 
    'provider_models' AS table_name,
    COUNT(*) AS added
FROM provider_models
WHERE raw_model_name = 'minimax-m3';

SELECT 
    'credential_model_bindings' AS table_name,
    COUNT(*) AS added
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3';

-- 4. 触发路由刷新
NOTIFY auto_route_refresh, 'minimax_m3_added';

COMMIT;
```

### 方案 B: 如果不确定 outbound_model_name

如果不确定 minimax-m3 对应的上游模型名，可以：

1. **查看 MiniMax 官方文档**
   - https://api.minimax.chat/ (或官方 API 文档)
   - 确认 m3 模型的正式名称

2. **联系 MiniMax 技术支持**
   - 询问 "minimax-m3" 对应的 API 模型名称

3. **测试验证**
   ```bash
   # 使用真实凭据测试不同的模型名称
   curl https://api.minimax.chat/v1/chat/completions \
     -H "Authorization: Bearer YOUR_KEY" \
     -d '{"model":"abab7-chat","messages":[...]}'
   ```

### 方案 C: 如果 minimax-m3 已弃用

如果 minimax-m3 是旧名称，应该：

```sql
-- 更新 model_aliases，将 minimax-m3 映射到现有模型
UPDATE model_aliases
SET canonical_id = (
    SELECT canonical_id 
    FROM provider_models 
    WHERE raw_model_name = 'abab6.5-chat' 
      AND provider_id = 14
    LIMIT 1
)
WHERE raw_name = 'minimax-m3';
```

---

## ⚠️ 注意事项

### 1. 确认 outbound_model_name

**关键**: 必须确认 `minimax-m3` 在 MiniMax API 中的正确名称！

可能的名称：
- `abab7-chat` (最可能，根据命名规律)
- `minimax-m3` (如果 API 直接支持)
- `abab6.5s-chat` (如果 m3 是 6.5s 的别名)

### 2. 验证凭据

确保有活跃的 minimax 凭据：

```sql
SELECT 
    id,
    label,
    status,
    availability_state,
    circuit_state
FROM credentials
WHERE provider_id = 14  -- minimax
  AND status = 'active'
  AND availability_state = 'ready';
```

### 3. 测试验证

添加配置后，测试请求：

```bash
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "test"}],
    "max_tokens": 10
  }'
```

### 4. 监控路由决策

```sql
-- 查看 minimax-m3 的路由决策
SELECT 
    request_id,
    model,
    chosen_credential_id,
    tier,
    candidates_tried
FROM routing_decision_log
WHERE model = 'minimax-m3'
  AND ts > now() - interval '10 minutes'
ORDER BY ts DESC
LIMIT 10;
```

---

## 📋 执行检查清单

修复前检查：
- [ ] 确认 minimax-m3 的正确 outbound_model_name
- [ ] 确认有活跃的 minimax credentials
- [ ] 备份当前配置

执行修复：
- [ ] 添加 provider_model
- [ ] 添加 credential_model_bindings
- [ ] 触发路由刷新 (NOTIFY)
- [ ] 验证添加结果

修复后验证：
- [ ] 查询 v_routable_credential_models 确认可路由
- [ ] 发送测试请求
- [ ] 检查 routing_decision_log
- [ ] 检查 request_logs 中的 credential_id

---

## 🔍 诊断命令

### 快速检查路由状态

```sql
-- 检查 minimax-m3 是否可路由
SELECT 
    credential_label,
    raw_model_name,
    canonical_id,
    is_routable,
    unavailable_reason
FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3'
   OR canonical_id = 5704;

-- 应该返回至少1行，is_routable = true
```

### 检查路由链完整性

```sql
-- 完整的路由链检查
SELECT 
    'model_aliases' AS step,
    COUNT(*) AS count
FROM model_aliases
WHERE raw_name = 'minimax-m3'

UNION ALL

SELECT 
    'provider_models',
    COUNT(*)
FROM provider_models
WHERE raw_model_name = 'minimax-m3'

UNION ALL

SELECT 
    'credential_model_bindings',
    COUNT(*)
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3';

-- 每一步都应该 > 0
```

---

## 📞 需要确认的信息

1. **MiniMax API 中 m3 模型的正式名称是什么？**
   - abab7-chat?
   - minimax-m3?
   - 其他？

2. **生产环境中是否有活跃的 minimax credentials？**

3. **是否需要为不同的 credential 配置不同的 routing_tier？**

---

**诊断完成时间**: 2026-06-26  
**下一步**: 确认 outbound_model_name 后执行修复 SQL
