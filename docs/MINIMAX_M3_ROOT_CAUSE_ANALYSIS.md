# minimax-m3 路由问题 - 根本原因分析

**日期**: 2026-06-26  
**状态**: ✅ 配置完成，❌ 测试环境无法验证（缺少加密密钥）

---

## 🎯 根本原因

经过深入调试，发现问题的完整链路：

### 数据流

```
1. 客户端请求: model="minimax-m3" ✅
   ↓
2. provider.GetCandidates() 查询数据库 ✅
   ↓  
3. fetchCandidatesDB() 返回 6 个候选 ✅
   ↓
4. enrichWithAPIKeys() 尝试解密 API key ❌
   ↓
5. RevealAPIKey() 失败（无加密密钥） ❌
   ↓
6. 所有候选被过滤掉 ❌
   ↓
7. routing_decision_log: candidates_tried = 0 ❌
   ↓
8. 返回错误: "no available provider" ❌
```

### 关键代码

```go
// provider/client.go:990-998
apiKey, err := c.RevealAPIKey(ctx, cand.ProviderID, cand.CredentialID)
if err != nil {
    slog.Warn("failed to reveal api key", ...)
    continue  // ❌ 跳过这个候选
}
```

### 日志证据

```json
{"level":"INFO","msg":"provider.GetCandidates: fetchCandidatesDB returned","candidate_count":6}
{"level":"INFO","msg":"provider.GetCandidates: final result","count_after_enrich":0}
{"level":"WARN","msg":"failed to reveal api key","error":"credential reveal not configured"}
```

---

## ✅ 已完成的配置

### 1. Provider Models (3个)
```sql
SELECT * FROM provider_models WHERE raw_model_name = 'minimax-m3';
```
| id | provider_id | provider | raw_model_name | outbound_model_name | canonical_id |
|----|-------------|----------|----------------|---------------------|--------------|
| 1 | 14 | minimax | minimax-m3 | MiniMax-M3 | 5 |
| 2 | 67 | minimax-anthropic | minimax-m3 | MiniMax-M3 | 5 |
| 3 | 18 | nvidia | minimax-m3 | minimaxai/minimax-m3 | 5 |

✅ **验证通过**

### 2. Credential Model Bindings (2个)
```sql
SELECT * FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3';
```
| credential_id | provider_model_id | available | routing_tier |
|---------------|-------------------|-----------|--------------|
| 1001 | 1 | true | 1 |
| 1002 | 2 | true | 1 |

✅ **验证通过**

### 3. Credential Model Index (98条)
```sql
SELECT COUNT(*) FROM credential_model_index WHERE raw_model = 'minimax-m3';
-- 结果: 98 rows (覆盖多个时间桶)
```
✅ **验证通过**

### 4. Model Offers View (2个)
```sql
SELECT * FROM model_offers WHERE raw_model_name = 'minimax-m3';
```
| id | credential_id | raw_model_name | outbound_model_name | available |
|----|---------------|----------------|---------------------|-----------|
| 1 | 1001 | minimax-m3 | MiniMax-M3 | true |
| 2 | 1002 | minimax-m3 | MiniMax-M3 | true |

✅ **验证通过**

### 5. v_routable_credential_models (2个)
```sql
SELECT * FROM v_routable_credential_models WHERE raw_model_name = 'minimax-m3';
```
| credential_id | is_routable | unavailable_reason |
|---------------|-------------|-------------------|
| 1001 | true | (null) |
| 1002 | true | (null) |

✅ **验证通过**

### 6. Models Canonical
```sql
SELECT * FROM models_canonical WHERE id = 5;
```
| id | canonical_name | status |
|----|----------------|--------|
| 5 | minimax-m3 | active |

✅ **验证通过**

### 7. Model Aliases
```sql
SELECT * FROM model_aliases WHERE raw_name = 'minimax-m3';
```
| id | raw_name | canonical_id | status |
|----|----------|--------------|--------|
| 249 | minimax-m3 | 5 | active |

✅ **验证通过**

---

## 🔍 调试过程发现

### autoroute.Index (内存索引)
```json
{"msg":"autoroute.Index.Refresh: query completed",
 "total_candidates":14,
 "sample_models":["...","minimax-m3","minimax-m3",...]}
```
✅ minimax-m3 已加载到内存

### provider.GetCandidates() 流程
```json
{"msg":"provider.GetCandidates called","original_model":"minimax-m3"}
{"msg":"provider.GetCandidates: fetchCandidatesDB returned","candidate_count":6}
{"msg":"provider.GetCandidates: final result","count_after_enrich":0}
```
❌ enrichWithAPIKeys 过滤掉所有候选

### enrichWithAPIKeys 失败原因
```json
{"level":"WARN","msg":"failed to reveal api key",
 "credential_id":1001,
 "error":"credential reveal not configured (no DB, keyring, or fernet key)"}
```
❌ 无法解密 API key

---

## 🚫 测试环境限制

### 为什么测试环境无法完全验证？

1. **缺少加密密钥**
   - Gateway 需要 fernet key 或 keyring 来解密 credentials.secret_ciphertext
   - 测试环境未配置加密密钥

2. **RevealAPIKey() 必须成功**
   - enrichWithAPIKeys() 会为每个候选调用 RevealAPIKey()
   - 失败的候选会被跳过（continue）
   - 结果：所有候选被过滤

3. **无法打破循环依赖**
   - 需要解密 → 调用上游 → 生成 request_logs
   - 但无法解密 → 无法调用上游
   - 测试环境陷入死锁

### 尝试的解决方案

1. ✅ 添加 provider_models
2. ✅ 添加 credential_model_bindings  
3. ✅ 手动填充 credential_model_index
4. ✅ 验证所有数据库配置
5. ❌ 无法绕过 API key 解密

---

## 🎯 生产环境预期

在**生产环境**（有加密密钥）中，流程应该是：

```
1. provider.GetCandidates() ✅
   ↓
2. fetchCandidatesDB() 返回候选 ✅
   ↓
3. enrichWithAPIKeys() ✅
   ↓
4. RevealAPIKey() 成功解密 ✅
   ↓
5. 候选被保留 ✅
   ↓
6. routing_decision_log: candidates_tried > 0 ✅
   ↓
7. 路由成功，调用上游 API ✅
```

---

## 📋 生产环境验证步骤

### 步骤 1: 验证配置同步

```sql
-- 1.1 检查 provider_models
SELECT COUNT(*) FROM provider_models WHERE raw_model_name = 'minimax-m3';
-- 期望: 3

-- 1.2 检查 model_offers
SELECT COUNT(*) FROM model_offers WHERE raw_model_name = 'minimax-m3';
-- 期望: N (N = 活跃 minimax 凭据数)

-- 1.3 检查 v_routable_credential_models
SELECT COUNT(*) FROM v_routable_credential_models 
WHERE raw_model_name = 'minimax-m3' AND is_routable = true;
-- 期望: N (同上)
```

### 步骤 2: 测试请求

```bash
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
- ✅ 200 OK 或上游 API 错误（非 503 no_candidate）
- ✅ routing_decision_log.candidates_tried > 0
- ✅ routing_decision_log.chosen_credential_id 非空

### 步骤 3: 检查路由决策

```sql
SELECT 
    request_id,
    model,
    chosen_credential_id,
    chosen_provider_id,
    tier,
    candidates_tried
FROM routing_decision_log
WHERE model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 5;
```

**期望**：
- `candidates_tried > 0`
- `chosen_credential_id` 非空

### 步骤 4: 检查 API key 解密

查看 gateway 日志：
```bash
grep "failed to reveal api key" /path/to/gateway.log | grep minimax
```

**期望**：无输出（没有解密失败）

---

## 🔧 如果生产环境仍有问题

### 场景 A: candidates_tried = 0

**诊断**：
```sql
-- 检查 model_offers
SELECT * FROM model_offers WHERE raw_model_name = 'minimax-m3';
```

**可能原因**：
1. model_offers 为空 → 检查 provider_models 和 credential_model_bindings
2. v_routable_credential_models.is_routable = false → 检查凭据状态
3. API key 解密失败 → 检查 gateway 日志

**解决**：
- 如果是配置问题，执行本文档的配置 SQL
- 如果是解密问题，检查 gateway 的加密配置

### 场景 B: candidates_tried > 0 但 chosen_credential_id 为空

**诊断**：
```sql
SELECT * FROM credentials WHERE id IN (
    SELECT credential_id FROM model_offers WHERE raw_model_name = 'minimax-m3'
);
```

**可能原因**：
- 所有候选都不可用（availability_state, circuit_state 等）
- 所有候选被路由过滤器排除

### 场景 C: 上游 API 错误

**诊断**：检查 request_logs.error_kind

**可能原因**：
- outbound_model_name 不正确
- 凭据无效或过期

**解决**：
```sql
-- 更新正确的上游模型名
UPDATE provider_models
SET outbound_model_name = '正确的API模型名'
WHERE raw_model_name = 'minimax-m3' AND provider_id = X;
```

---

## 💡 架构改进建议

### 1. 引导模式（Bootstrap Mode）

当环境变量 `LLM_GATEWAY_BOOTSTRAP_MODE=true` 时：
- enrichWithAPIKeys() 跳过 API key 验证
- 使用假的 API key (如 "bootstrap-key")
- 允许测试环境验证路由逻辑

### 2. 测试凭据支持

在 credentials 表添加 `is_test` 字段：
- 测试凭据使用明文 API key
- 生产凭据使用加密 API key
- enrichWithAPIKeys() 根据类型选择解密方式

### 3. Redis 共享路由状态

**当前问题**：
- autoroute.Index 存储在进程内存
- 多个 gateway 实例无法共享索引
- 每个实例独立维护索引

**建议方案**：
```go
// autoroute/index_redis.go
type RedisIndex struct {
    client *redis.Client
    localCache *Index // 本地缓存
}

func (idx *RedisIndex) Refresh(ctx context.Context) error {
    // 1. 从 DB 查询最新数据
    // 2. 序列化并写入 Redis (key: "autoroute:index", TTL: 10min)
    // 3. 其他实例通过 Redis PUBSUB 收到通知
    // 4. 所有实例从 Redis 加载最新索引
}
```

**优势**：
- 多机共享同一份索引
- 任一实例 Refresh 后，所有实例同步更新
- 降低数据库查询压力

### 4. 降级策略

当 enrichWithAPIKeys() 全部失败时：
- 不要直接返回空数组
- 记录警告日志
- 返回原始候选（不含 API key）
- 让路由器决定如何处理

---

## 📊 监控指标

### 关键指标

1. **API key 解密成功率**
```sql
-- 从日志统计
SELECT 
    DATE_TRUNC('hour', ts) AS hour,
    COUNT(*) AS total_attempts,
    COUNT(*) FILTER (WHERE error IS NULL) AS success_count
FROM api_key_reveal_log
WHERE ts > now() - interval '24 hours'
GROUP BY hour;
```

2. **enrichWithAPIKeys 过滤率**
```
候选过滤率 = (fetchDB返回数 - enrich后数) / fetchDB返回数
```

3. **minimax-m3 路由成功率**
```sql
SELECT 
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE candidates_tried > 0) AS routed,
    ROUND(100.0 * COUNT(*) FILTER (WHERE candidates_tried > 0) / COUNT(*), 2) AS success_rate_pct
FROM routing_decision_log
WHERE model = 'minimax-m3'
  AND ts > now() - interval '1 hour';
```

---

## 📝 总结

### 配置状态
✅ **所有数据库配置完成且验证通过**
- provider_models: 3 条
- credential_model_bindings: 2 条  
- credential_model_index: 98 条
- model_offers: 2 条
- v_routable_credential_models: 2 条可路由

### 测试状态
❌ **测试环境无法完全验证**
- 原因：缺少加密密钥
- 阻塞点：enrichWithAPIKeys() 过滤所有候选
- 影响：candidates_tried = 0

### 生产环境预期
✅ **应该正常工作**
- 条件：有加密密钥可以解密 API key
- 流程：完整路由链路已打通
- 结果：minimax-m3 请求应该成功路由

### 下一步
1. 在生产环境测试 minimax-m3 请求
2. 验证 candidates_tried > 0
3. 如有问题，按故障排查清单诊断
4. 考虑实施架构改进建议

---

**完成时间**: 2026-06-26 15:56  
**调试会话**: 完整追踪从数据库到 enrichWithAPIKeys 的完整链路  
**根本原因**: 测试环境缺少加密密钥，无法解密 API key
