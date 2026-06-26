# minimax-m3 路由问题 - 最终报告

**日期**: 2026-06-26  
**问题**: minimax-m3 请求返回 no_candidate 错误  
**状态**: ✅ 配置完成，⚠️ 测试环境无法完全验证

---

## ✅ 已完成的配置

### 1. Provider Models (3个)
```sql
INSERT INTO provider_models (provider_id, raw_model_name, outbound_model_name, canonical_id)
VALUES 
    (14, 'minimax-m3', 'MiniMax-M3', 5),           -- minimax
    (67, 'minimax-m3', 'MiniMax-M3', 5),           -- minimax-anthropic
    (18, 'minimax-m3', 'minimaxai/minimax-m3', 5); -- nvidia
```
✅ 已验证：3 条记录

### 2. Credential Model Bindings (2个)
```sql
-- minimax-test-1 → minimax-m3
-- minimax-test-2 → minimax-m3
```
✅ 已验证：2 条绑定

### 3. Credential Model Index (26条)
- 覆盖前后2小时的5分钟时间桶
- 包含所有必需字段（success_rate, p95_latency_ms, score_smart等）
✅ 已验证：26 条记录

### 4. Canonical ID 修复
**关键修复**：发现 canonical_id 不一致问题并修复
- model_aliases 原来指向 5704
- models_canonical 中实际id是 5
- ✅ 已更新所有表使用正确的 canonical_id=5

---

## 🔍 根本问题分析

### Gateway 路由索引的两个机制

#### 1. Rollup（自动生成）
```go
// bg/auto_index_refresher.go
// 从最近5分钟的 request_logs 生成索引
FROM request_logs rl
WHERE rl.ts >= NOW() - INTERVAL '5 minutes'
  AND rl.credential_id IS NOT NULL  // 需要成功的请求
```

**问题**：测试环境没有成功的 request_logs → rollup 返回 0 行

#### 2. Refresh（从现有索引加载）
```go
// autoroute/index.go  
// 从现有的 credential_model_index 读取
FROM credential_model_index cmi
JOIN latest_bucket lb ...
JOIN credentials cr ...
LEFT JOIN models_canonical mc ...
```

**验证**：我们手动执行此查询返回 2 行数据
**但**：gateway 启动时的 rollup 仍然返回 0 行

### 循环依赖问题

```
需要成功的 request_logs (credential_id 非空)
    ↓
rollup 生成 credential_model_index
    ↓
gateway Refresh 加载索引到内存
    ↓
路由请求并选择 credential
    ↓
调用上游 API (需要解密密钥)
    ↓
生成成功的 request_logs
```

在测试环境：
- ❌ 无法解密凭据
- ❌ 无法调用上游
- ❌ 无法生成成功的 request_logs
- ❌ rollup 永远返回 0
- ❌ 循环无法启动

---

## 🎯 生产环境操作指南

### 前提条件
- ✅ 生产环境有真实的加密密钥
- ✅ 有活跃的 minimax/nvidia/minimax-anthropic credentials

### 步骤1: 验证配置已同步

```sql
-- 在生产数据库执行

-- 1. 检查 provider_models
SELECT pm.id, p.code, pm.raw_model_name, pm.outbound_model_name, pm.canonical_id
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name = 'minimax-m3';
-- 应该返回 3 行，canonical_id=5

-- 2. 检查 credential_model_bindings
SELECT COUNT(*) 
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3';
-- 应该返回 N 行（N = 活跃凭据数）

-- 3. 检查 canonical_id 一致性
SELECT 
    'model_aliases' AS source, canonical_id 
FROM model_aliases WHERE raw_name = 'minimax-m3'
UNION ALL
SELECT 'provider_models', canonical_id 
FROM provider_models WHERE raw_model_name = 'minimax-m3' LIMIT 1;
-- 两行应该都是 5
```

### 步骤2: 首次请求测试

```bash
# 发送测试请求
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer PROD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "测试"}],
    "max_tokens": 10
  }'
```

**预期结果**：
- 如果配置正确：200 或上游 API 错误（非 503 no_candidate）
- 如果仍然 no_candidate：继续步骤3

### 步骤3: 如果仍然 no_candidate

```sql
-- 检查路由决策
SELECT 
    request_id,
    model,
    chosen_credential_id,
    tier,
    candidates_tried,
    latency_ms
FROM routing_decision_log
WHERE model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 3;
```

**诊断**：
- `candidates_tried = 0`: 索引为空，继续步骤4
- `candidates_tried > 0` 但 `chosen_credential_id` 为空: 凭据不可用，检查凭据状态

### 步骤4: 手动填充索引（应急方案）

如果 rollup 因为没有历史 request_logs 而无法生成索引：

```sql
-- 临时填充 credential_model_index
INSERT INTO credential_model_index (
    bucket,
    credential_id,
    raw_model,
    canonical_id,
    active_sessions,
    concurrency_limit,
    success_rate,
    p95_latency_ms,
    score_smart,
    score_speed_first,
    score_cost_first,
    pressure_ratio
)
SELECT 
    to_timestamp(floor((extract('epoch' from now()) / 300 )) * 300) AS bucket,
    cmb.credential_id,
    pm.raw_model_name AS raw_model,
    pm.canonical_id,
    0 AS active_sessions,
    c.concurrency_limit,
    0.95 AS success_rate,
    500 AS p95_latency_ms,
    100.0 AS score_smart,
    100.0 AS score_speed_first,
    100.0 AS score_cost_first,
    0.0 AS pressure_ratio
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.available = true
  AND c.availability_state = 'ready'
  AND pm.raw_model_name = 'minimax-m3'
ON CONFLICT (bucket, credential_id, raw_model) DO NOTHING;

-- 触发 gateway 刷新
NOTIFY auto_route_refresh, 'minimax_m3_manual';
```

等待 10 秒后重新测试。

### 步骤5: 验证成功

```sql
-- 检查成功的请求
SELECT 
    request_id,
    client_model,
    credential_id,
    request_status,
    success
FROM request_logs
WHERE client_model = 'minimax-m3'
  AND success = true
ORDER BY ts DESC
LIMIT 5;

-- 一旦有成功的请求，后续的 rollup 会自动维护索引
```

---

## 🔧 故障排查清单

### 问题A: no_candidate (candidates_tried=0)

**原因**: 路由索引为空

**解决**:
1. 检查 credential_model_index 是否有数据
2. 如果没有，执行步骤4的手动填充
3. 或等待有成功请求后的下一次 rollup（每5分钟）

### 问题B: no_candidate (candidates_tried>0)

**原因**: 有候选但都不可用

**解决**:
```sql
SELECT 
    credential_id,
    availability_state,
    circuit_state,
    status
FROM credentials
WHERE provider_id IN (14, 18, 67)  -- minimax providers
  AND id IN (SELECT DISTINCT credential_id FROM credential_model_bindings cmb 
             JOIN provider_models pm ON pm.id = cmb.provider_model_id 
             WHERE pm.raw_model_name = 'minimax-m3');
```

检查凭据状态并修复。

### 问题C: credential_reveal_failed

**原因**: 无法解密凭据

**解决**:
1. 检查 gateway 是否配置了加密密钥
2. 检查凭据的 secret_ciphertext 是否正确
3. 查看 gateway 日志中的具体错误

### 问题D: 上游 API 错误

**原因**: outbound_model_name 不正确

**解决**:
```sql
-- 更新正确的上游模型名
UPDATE provider_models
SET outbound_model_name = '正确的API模型名'
WHERE raw_model_name = 'minimax-m3' AND provider_id = X;

-- 例如：
-- MiniMax API: 'MiniMax-M3'
-- NVIDIA NIM API: 'minimaxai/minimax-m3'
```

---

## 📊 监控建议

### 关键指标

1. **路由成功率**
```sql
SELECT 
    COUNT(*) AS total,
    COUNT(CASE WHEN chosen_credential_id IS NOT NULL THEN 1 END) AS routed,
    ROUND(100.0 * COUNT(CASE WHEN chosen_credential_id IS NOT NULL THEN 1 END) / COUNT(*), 2) AS success_rate
FROM routing_decision_log
WHERE model = 'minimax-m3'
  AND ts > now() - interval '1 hour';
```

2. **索引健康度**
```sql
SELECT 
    COUNT(DISTINCT credential_id) AS credentials,
    COUNT(*) AS total_rows,
    MAX(bucket) AS latest_bucket
FROM credential_model_index
WHERE raw_model = 'minimax-m3';
```

3. **请求成功率**
```sql
SELECT 
    COUNT(*) AS total,
    COUNT(CASE WHEN success THEN 1 END) AS success,
    ROUND(100.0 * COUNT(CASE WHEN success THEN 1 END) / COUNT(*), 2) AS success_rate
FROM request_logs
WHERE client_model = 'minimax-m3'
  AND ts > now() - interval '1 hour';
```

---

## 💡 长期改进建议

### 1. 引导模式
Gateway 启动时如果 credential_model_index 为空，自动从 credential_model_bindings 生成初始索引。

### 2. 索引持久化
将 Refresh() 加载的索引也写入 credential_model_index，避免依赖 rollup。

### 3. 降级策略
当 rollup 连续失败时，使用 v_routable_credential_models 视图作为降级方案。

### 4. 监控告警
- 当 rollup credential_rows=0 持续 >10分钟时告警
- 当 routing candidates_tried=0 比例 >50% 时告警

---

## 📝 总结

### 测试环境
- ✅ 所有配置已正确添加
- ✅ 数据库查询验证通过
- ❌ 无法端到端验证（缺少加密密钥）

### 生产环境预期
- ✅ 首次请求应该成功（或上游错误，非 no_candidate）
- ✅ 成功请求会生成 request_logs
- ✅ 后续 rollup 会自动维护索引
- ✅ 后续请求正常工作

### 如果生产环境仍有问题
1. 执行步骤1-3 的诊断
2. 如果 candidates_tried=0，执行步骤4 手动填充
3. 验证凭据状态和加密密钥配置
4. 检查上游 API 模型名称是否正确

---

**完成时间**: 2026-06-26 15:45  
**配置状态**: ✅ 完成  
**验证状态**: ⚠️ 需要生产环境验证  
**预期结果**: 应该在生产环境正常工作
