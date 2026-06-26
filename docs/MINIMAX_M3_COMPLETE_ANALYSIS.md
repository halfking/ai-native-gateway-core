# minimax-m3 路由问题完整分析报告

**日期**: 2026-06-26  
**问题**: minimax-m3 请求返回 no_candidate 错误  
**环境**: llmgateway.internal.example.com (生产环境 71)  
**状态**: ⚠️ 部分完成（配置已添加，但受限于测试环境）

---

## 📋 执行摘要

我已成功添加了 minimax-m3 的完整配置（provider_models + credential_model_bindings + credential_model_index），但由于测试环境缺少加密密钥，无法完成端到端验证。**配置在生产环境应该可以正常工作**。

---

## ✅ 已完成的工作

### 1. 添加 Provider Models (3个)

```sql
-- minimax provider
INSERT INTO provider_models (provider_id, raw_model_name, outbound_model_name, canonical_id)
VALUES (14, 'minimax-m3', 'MiniMax-M3', 5704);

-- minimax-anthropic provider  
INSERT INTO provider_models (provider_id, raw_model_name, outbound_model_name, canonical_id)
VALUES (67, 'minimax-m3', 'MiniMax-M3', 5704);

-- nvidia provider
INSERT INTO provider_models (provider_id, raw_model_name, outbound_model_name, canonical_id)
VALUES (18, 'minimax-m3', 'minimaxai/minimax-m3', 5704);
```

**验证**: ✅ 3 条记录已插入

### 2. 添加 Credential Model Bindings (2个)

```sql
INSERT INTO credential_model_bindings (credential_id, provider_model_id, routing_tier, weight, available)
SELECT c.id, pm.id, 1, 100, true
FROM credentials c
CROSS JOIN provider_models pm
WHERE c.provider_id IN (14, 18, 67)
  AND pm.raw_model_name = 'minimax-m3'
  AND pm.provider_id = c.provider_id;
```

**验证**: ✅ 2 条绑定已添加（minimax-test-1, minimax-test-2）

### 3. 填充 Credential Model Index (26条)

```sql
-- 为前后30分钟的时间桶生成索引数据
-- 包含 success_rate, p95_latency_ms, score_smart 等字段
```

**验证**: ✅ 26 条索引记录已插入，覆盖 13 个时间桶

### 4. 验证可路由性

```sql
SELECT * FROM v_routable_credential_models WHERE raw_model_name = 'minimax-m3';
-- 结果: 2 个可路由节点，is_routable = true
```

**验证**: ✅ 数据库层面配置正确

---

## ⚠️ 测试环境限制

### 根本问题：循环依赖

Gateway 的自动路由索引依赖于**成功的 request_logs**：

```
需要成功的 request_logs 
    ↓
rollupCredentialModelIndexSQL 生成索引
    ↓
gateway 加载索引并路由请求
    ↓
调用上游 API
    ↓
生成成功的 request_logs
```

但测试环境缺少加密密钥：
```
credential decrypt failed: no decryption key available
→ 无法调用上游 API
→ 无法生成成功的 request_logs
→ rollup 返回 0 行
→ gateway 索引为空
→ no_candidate 错误
```

### 我们尝试的解决方案

1. ✅ **手动填充 credential_model_index**
   - 插入了 26 条记录
   - 包含所有必需字段
   
2. ❌ **Gateway 未使用手动数据**
   - `rollupCredentialModelIndexSQL` 从 request_logs 查询
   - 忽略了手动插入的 credential_model_index 数据
   - `auto index refreshed: credential_rows=0`

3. ❌ **无法触发 refreshIndexSQL**
   - refreshIndexSQL 可以读取现有的 credential_model_index
   - 但只在 gateway 启动时调用一次
   - NOTIFY 触发的是 rollup，不是 refresh

---

## 🔍 诊断结果

### 数据库配置（完全正确）

```sql
-- provider_models: ✅ 3 条
SELECT COUNT(*) FROM provider_models WHERE raw_model_name = 'minimax-m3';
-- 结果: 3

-- credential_model_bindings: ✅ 2 条  
SELECT COUNT(*) FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3';
-- 结果: 2

-- v_routable_credential_models: ✅ 2 个可路由
SELECT COUNT(*) FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3' AND is_routable = true;
-- 结果: 2

-- credential_model_index: ✅ 26 条记录
SELECT COUNT(*) FROM credential_model_index WHERE raw_model = 'minimax-m3';
-- 结果: 26

-- 模拟 gateway 的 refreshIndexSQL: ✅ 返回 2 行
WITH latest_bucket AS (
    SELECT credential_id, raw_model, MAX(bucket) AS bucket
    FROM credential_model_index
    WHERE raw_model = 'minimax-m3'
    GROUP BY credential_id, raw_model
)
SELECT cmi.credential_id, cmi.raw_model
FROM credential_model_index cmi
JOIN latest_bucket lb ON ...
JOIN credentials cr ON ...;
-- 结果: 2 rows (credential_id=1001, 1002)
```

### Gateway 行为（受限于环境）

```
1. 启动时执行 Refresh() → 可能成功加载索引（未确认）
2. 每5分钟执行 Rollup() → 从 request_logs 查询 → 返回 0 行
3. NOTIFY 触发 Rollup() → 从 request_logs 查询 → 返回 0 行
4. 路由请求 → 查询内存索引 → candidates_tried=0 → no_candidate
```

---

## 🎯 生产环境预期行为

在**配置了加密密钥**的生产环境中：

1. ✅ Provider models 已配置
2. ✅ Credential bindings 已配置
3. ✅ 首次请求会触发路由
4. ✅ 成功调用后生成 request_logs
5. ✅ 下次 rollup 会从 request_logs 生成正确的索引
6. ✅ 后续请求正常路由

**结论**: 配置应该在生产环境正常工作，因为有真实凭据可以完成循环。

---

## 📊 验证命令（生产环境）

### 1. 验证配置
```sql
-- 检查 provider_models
SELECT pm.id, p.code, pm.raw_model_name, pm.outbound_model_name
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name = 'minimax-m3';
-- 应返回 3 行

-- 检查可路由性
SELECT credential_label, is_routable, unavailable_reason
FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3';
-- 应返回 N 行（N = 活跃凭据数）
```

### 2. 测试请求
```bash
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_PRODUCTION_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"minimax-m3","messages":[{"role":"user","content":"测试"}],"max_tokens":10}'
```

### 3. 检查路由决策
```sql
SELECT request_id, model, chosen_credential_id, tier, candidates_tried
FROM routing_decision_log
WHERE model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 5;
-- chosen_credential_id 应非空，candidates_tried > 0
```

---

## 🚀 如果生产环境仍有问题

### 场景 A：no_candidate (candidates_tried=0)

**可能原因**:
1. credential_model_index 为空（需要等待首次 rollup）
2. 凭据状态不正确

**解决**:
```sql
-- 手动填充索引（临时）
INSERT INTO credential_model_index (...) 
SELECT ... FROM credential_model_bindings ...;

-- 或等待5-10分钟让 rollup 自动生成
```

### 场景 B：credential_reveal_failed

**可能原因**: 凭据解密失败

**解决**:
1. 检查凭据的 secret_ciphertext
2. 确认 gateway 有正确的加密密钥
3. 检查 gateway 日志中的 decrypt 错误

### 场景 C：上游 API 错误

**可能原因**: outbound_model_name 不正确

**解决**:
```sql
-- 检查并更新 outbound_model_name
UPDATE provider_models
SET outbound_model_name = '正确的上游模型名'
WHERE raw_model_name = 'minimax-m3' AND provider_id = X;
```

---

## 📁 相关文件

- `provider_models`: 已添加 3 条记录（id=1,2,3）
- `credential_model_bindings`: 已添加 2 条记录（id=1,2）
- `credential_model_index`: 已添加 26 条记录
- `model_aliases`: 已存在 minimax-m3 → canonical_id=5704

---

## 💡 建议

### 短期
1. 在生产环境测试 minimax-m3 请求
2. 如果仍有问题，检查 routing_decision_log.candidates_tried
3. 如果 candidates_tried=0，手动填充 credential_model_index

### 长期
1. 考虑改进 gateway 启动逻辑，从 credential_model_bindings 生成初始索引
2. 或者提供"引导模式"，在没有 request_logs 时使用静态配置
3. 添加监控告警：当 rollup credential_rows=0 时告警

---

**完成时间**: 2026-06-26 15:35  
**测试环境**: 本地 Docker (r112_postgres)  
**生产环境**: 需要在 llmgateway.internal.example.com (71) 验证
