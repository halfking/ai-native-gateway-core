# LLM Gateway 验证清单

**用途**: 在生产环境或配置了真实凭据的测试环境中使用此清单验证系统功能。

---

## ✅ 基础功能验证

### 1. Telemetry 完整性
```bash
# 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}],"max_tokens":5}'

# 验证 request_logs
psql -c "
SELECT request_id, client_model, credential_id, request_status, success
FROM request_logs 
WHERE ts > now() - interval '5 minutes'
ORDER BY ts DESC LIMIT 5;
"

# 验证 request_wal
psql -c "
SELECT request_id, status, stage, client_model
FROM request_wal 
WHERE created_at > now() - interval '5 minutes'
ORDER BY created_at DESC LIMIT 5;
"
```

**期望**:
- [ ] request_logs 有记录且 request_id 非空
- [ ] request_wal 有对应记录
- [ ] credential_id 非空（如果成功路由）
- [ ] 无重复 request_id

---

## 🔄 路由切换验证

### 2. Tier 1 → Tier 2 Fallback

```sql
-- 步骤1: 查看当前路由配置
SELECT 
    cmb.id,
    c.label AS credential,
    pm.raw_model_name AS model,
    cmb.routing_tier AS tier,
    cmb.available
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'gpt-4'
ORDER BY cmb.routing_tier;

-- 步骤2: 禁用 tier 1 凭据
UPDATE credential_model_bindings 
SET available = false, unavailable_at = now()
WHERE routing_tier = 1 AND provider_model_id = (
    SELECT id FROM provider_models WHERE raw_model_name = 'gpt-4' LIMIT 1
);

-- 步骤3: 触发路由刷新
NOTIFY auto_route_refresh, 'tier1_disabled';
```

```bash
# 步骤4: 发送10个请求
for i in {1..10}; do
  curl -s -X POST http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer YOUR_API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"tier test"}],"max_tokens":5}' &
done
wait
sleep 5
```

```sql
-- 步骤5: 验证路由决策
SELECT 
    client_model,
    COUNT(*) AS total_requests,
    COUNT(DISTINCT credential_id) AS unique_credentials,
    MIN(credential_id) AS credential_used
FROM request_logs 
WHERE ts > now() - interval '2 minutes' AND client_model = 'gpt-4'
GROUP BY client_model;

-- 期望: credential_used 应该是 tier 2 的 credential_id

-- 步骤6: 恢复 tier 1 凭据
UPDATE credential_model_bindings 
SET available = true, unavailable_at = NULL
WHERE routing_tier = 1;
```

**期望**:
- [ ] 所有请求路由到 tier 2 凭据
- [ ] routing_decision_log.tier = 2
- [ ] 无 no_candidate 错误

---

## 🔐 指纹管理验证

### 3. Identity Hash 绑定

```bash
# 使用相同的 identity_hash 发送多个请求
IDENTITY="test-user-123"

for i in {1..5}; do
  curl -s -X POST http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer YOUR_API_KEY" \
    -H "X-Identity-Hash: $IDENTITY" \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"fp test '$i'"}],"max_tokens":5}' &
done
wait
sleep 5
```

```sql
-- 验证同一 identity_hash 是否使用相同凭据
SELECT 
    identity_hash,
    COUNT(*) AS requests,
    COUNT(DISTINCT credential_id) AS unique_credentials,
    ARRAY_AGG(DISTINCT credential_id) AS credential_ids
FROM request_logs 
WHERE identity_hash IS NOT NULL 
  AND ts > now() - interval '2 minutes'
GROUP BY identity_hash;

-- 期望: unique_credentials = 1 (同一用户使用同一凭据)
```

**期望**:
- [ ] 同一 identity_hash 的请求使用相同的 credential_id
- [ ] fingerprint slot 正确分配
- [ ] 无 slot 耗尽错误

---

## 🚀 并发 Slot 验证

### 4. Slot 抢占测试

```sql
-- 查看凭据的 slot 限制
SELECT 
    id AS credential_id,
    label,
    concurrency_limit,
    fp_slot_limit,
    active_sessions
FROM credentials
WHERE id = YOUR_CREDENTIAL_ID;
```

```bash
# 发送超过 fp_slot_limit 数量的并发请求
# 假设 fp_slot_limit = 3
IDENTITY="test-user-456"

for i in {1..10}; do
  curl -s -X POST http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer YOUR_API_KEY" \
    -H "X-Identity-Hash: $IDENTITY" \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"slot test '$i'"}],"stream":true,"max_tokens":100}' &
done
sleep 2  # 让请求并发执行
```

```sql
-- 检查 active_sessions 和失败情况
SELECT 
    credential_id,
    active_sessions,
    pressure_ratio
FROM credential_model_index
WHERE credential_id = YOUR_CREDENTIAL_ID
  AND bucket = date_trunc('hour', now());

-- 检查是否有请求因 slot 不足失败
SELECT 
    COUNT(*) AS total,
    COUNT(CASE WHEN error_kind = 'rate_limited' THEN 1 END) AS rate_limited_count
FROM request_logs
WHERE credential_id = YOUR_CREDENTIAL_ID
  AND ts > now() - interval '2 minutes';
```

**期望**:
- [ ] active_sessions 最多达到 fp_slot_limit
- [ ] 超额请求正确失败或排队
- [ ] pressure_ratio 正确计算

---

## 👥 多会话验证

### 5. 多会话并发

```bash
# 同一用户，多个会话
IDENTITY="test-user-789"

for session in {1..3}; do
  for req in {1..5}; do
    curl -s -X POST http://localhost:8080/v1/chat/completions \
      -H "Authorization: Bearer YOUR_API_KEY" \
      -H "X-Identity-Hash: $IDENTITY" \
      -H "X-Session-ID: session-$session" \
      -H "Content-Type: application/json" \
      -d '{"model":"gpt-4","messages":[{"role":"user","content":"session test"}],"max_tokens":5}' &
  done
done
wait
sleep 5
```

```sql
-- 验证会话隔离
SELECT 
    identity_hash,
    gw_session_id,
    COUNT(*) AS requests,
    COUNT(DISTINCT credential_id) AS unique_credentials
FROM request_logs
WHERE identity_hash = 'test-user-789'
  AND ts > now() - interval '2 minutes'
GROUP BY identity_hash, gw_session_id;
```

**期望**:
- [ ] 每个 gw_session_id 独立记录
- [ ] 同一 identity_hash 的不同会话可能使用不同凭据（取决于策略）
- [ ] 会话数据正确隔离

---

## 🔄 会话内模型变更验证

### 6. 会话内切换模型

```bash
SESSION_ID="persistent-session-123"
IDENTITY="test-user-999"

# 第一次请求：gpt-4
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "X-Identity-Hash: $IDENTITY" \
  -H "X-Session-ID: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"first"}],"max_tokens":5}'

sleep 2

# 第二次请求：gpt-3.5-turbo
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "X-Identity-Hash: $IDENTITY" \
  -H "X-Session-ID: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"second"}],"max_tokens":5}'

sleep 2

# 第三次请求：回到 gpt-4
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "X-Identity-Hash: $IDENTITY" \
  -H "X-Session-ID: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"third"}],"max_tokens":5}'
```

```sql
-- 验证会话内模型变更
SELECT 
    request_id,
    ts,
    gw_session_id,
    client_model,
    credential_id,
    success
FROM request_logs
WHERE gw_session_id = 'persistent-session-123'
ORDER BY ts;
```

**期望**:
- [ ] gw_session_id 相同
- [ ] client_model 正确记录变更
- [ ] 每个模型请求都成功路由
- [ ] credential_id 可能不同（不同模型）

---

## 📊 高并发压测

### 7. 300 并发请求

```bash
#!/bin/bash
echo "=== 高并发压测 ==="
START=$(date +%s%N)

for i in {1..300}; do
  MODELS=("gpt-4" "gpt-3.5-turbo" "claude-3-5-sonnet-20241022")
  MODEL=${MODELS[$((RANDOM % 3))]}
  
  curl -s -X POST http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer YOUR_API_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"test $i\"}],\"max_tokens\":5}" \
    -o /dev/null -w "%{http_code}\n" &
  
  if (( i % 50 == 0 )); then sleep 0.1; fi
done
wait

END=$(date +%s%N)
ELAPSED=$(( (END - START) / 1000000 ))
echo "✅ 300 并发完成，耗时: ${ELAPSED}ms"

sleep 10
```

```sql
-- 验证结果
SELECT 
    COUNT(*) AS total_requests,
    COUNT(DISTINCT request_id) AS unique_requests,
    COUNT(CASE WHEN success = true THEN 1 END) AS success_count,
    COUNT(CASE WHEN success = false THEN 1 END) AS failure_count,
    ROUND(AVG(latency_ms)) AS avg_latency_ms
FROM request_logs
WHERE ts > now() - interval '5 minutes';

-- 检查无重复
SELECT 
    CASE WHEN COUNT(*) = COUNT(DISTINCT request_id) THEN 'PASS' ELSE 'FAIL' END AS no_duplicates
FROM request_logs
WHERE ts > now() - interval '5 minutes';

-- 检查模型分布
SELECT 
    client_model,
    COUNT(*) AS count,
    ROUND(COUNT(*) * 100.0 / SUM(COUNT(*)) OVER(), 2) AS percentage
FROM request_logs
WHERE ts > now() - interval '5 minutes'
GROUP BY client_model;
```

**期望**:
- [ ] total_requests = unique_requests = 300
- [ ] success_count > 0（至少部分成功）
- [ ] 无重复 request_id
- [ ] 3个模型均匀分布（约33%）
- [ ] avg_latency_ms < 5000

---

## 🔍 故障排查

### 常见问题检查

```sql
-- 1. 检查 credential_model_index 是否有数据
SELECT COUNT(*) FROM credential_model_index 
WHERE bucket >= date_trunc('hour', now() - interval '1 hour');

-- 2. 检查可路由节点
SELECT COUNT(*) FROM v_routable_credential_models WHERE is_routable = true;

-- 3. 检查 providers 状态
SELECT id, code, enabled, manual_disabled FROM providers;

-- 4. 检查 credentials 状态
SELECT 
    id, label, availability_state, circuit_state, 
    concurrency_limit, fp_slot_limit, active_sessions
FROM credentials;

-- 5. 检查最近的路由决策
SELECT 
    request_id, model, chosen_credential_id, tier, 
    candidates_tried, latency_ms
FROM routing_decision_log
WHERE ts > now() - interval '10 minutes'
ORDER BY ts DESC
LIMIT 10;

-- 6. 检查失败原因分布
SELECT 
    error_kind,
    failure_stage,
    failure_detail_code,
    COUNT(*) AS count
FROM request_logs
WHERE success = false
  AND ts > now() - interval '1 hour'
GROUP BY error_kind, failure_stage, failure_detail_code
ORDER BY count DESC;
```

---

## 📝 验证报告模板

```markdown
# 验证报告 - [日期]

## 环境
- Gateway 版本: [version]
- Database: [postgres version]
- 测试环境: [production/staging/test]

## 基础功能 ✅/❌
- [ ] Telemetry 完整性
- [ ] request_logs 无重复
- [ ] request_wal 覆盖率 > 95%

## 路由功能 ✅/❌
- [ ] Tier 1 → Tier 2 fallback
- [ ] 指纹管理（identity_hash 绑定）
- [ ] Slot 抢占（fp_slot_limit）

## 并发功能 ✅/❌
- [ ] 多会话并发
- [ ] 会话内模型变更
- [ ] 300 并发压测通过

## 问题记录
[列出发现的问题]

## 建议
[列出改进建议]
```

---

**最后更新**: 2026-06-26  
**维护者**: LLM Gateway Team
