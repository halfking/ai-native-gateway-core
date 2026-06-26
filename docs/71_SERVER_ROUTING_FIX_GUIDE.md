# 71 服务器路由和请求记录问题 - 完整修复指南

**日期**: 2026-06-26  
**服务器**: 71 (llmgateway.internal.example.com)  
**问题**: 请求无法记录 + 路由失败 + empty_response 过多

---

## 📋 问题概述

### 报告的问题

1. **71服务器上的请求无法记录** - request_logs 表没有新记录
2. **通过 llmgateway.internal.example.com/v1 发起的请求无法正确路由** - 返回 no_candidate 错误
3. **路由层无法正确匹配凭据** - 虽然有可用凭据（如 minimax-m3）
4. **184 数据库的 request_logs 中大量 empty_response** - 误判导致

---

## 🔍 根因分析

### 问题 1 & 2 & 3: 路由失败

**可能的原因**（按优先级排序）：

#### A. 路由索引为空或过期
```
credential_model_index 表没有数据 
→ Gateway 的 autoroute.Index.Refresh() 返回空列表
→ provider.GetCandidates() 返回 0 个候选
→ 路由器找不到可用凭据
→ 返回 no_candidate 错误
→ 请求失败，request_logs 记录失败状态
```

**诊断方法**：
```sql
SELECT COUNT(*) FROM credential_model_index 
WHERE raw_model = 'minimax-m3' 
  AND bucket > now() - interval '10 minutes';
-- 如果返回 0，说明索引为空
```

#### B. Canonical ID 不一致
```
model_aliases.canonical_id = 5704
provider_models.canonical_id = 5
→ SQL 查询中的 JOIN 条件无法匹配
→ loadCandidatesDB 返回 0 行
→ no_candidate
```

**诊断方法**：
```sql
SELECT 'model_aliases' AS source, canonical_id 
FROM model_aliases WHERE raw_name = 'minimax-m3'
UNION ALL
SELECT 'provider_models', canonical_id 
FROM provider_models WHERE raw_model_name = 'minimax-m3' LIMIT 1;
-- 如果两个值不同，说明不一致
```

#### C. 凭据状态不可用
```
credentials.availability_state != 'ready'
或 credentials.lifecycle_status != 'active'
或 credential_model_bindings.available = false
→ v_routable_credential_models.is_routable = false
→ loadCandidatesDB 的 WHERE 子句过滤掉
→ no_candidate
```

**诊断方法**：
```sql
SELECT credential_id, is_routable, unavailable_reason
FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3';
-- 检查 is_routable 是否为 true
```

#### D. Gateway 缓存未刷新
```
数据库已修复，但 Gateway 仍使用旧缓存
→ provider.Client.candCache 仍然是空的或过期的
→ 需要等待 30 秒缓存过期，或重启 Gateway
```

---

### 问题 4: empty_response 过多

**根因**（已在 commit 78de1295 修复）：

`relay/handler.go::detectEmptyStreamResponse` 的第 4 个检查条件是 dead code：

```go
// Line 1749: 调用 detectEmptyStreamResponse
isEmpty := detectEmptyStreamResponse(m, reqLog)

// Line 1774: 之后才填充 UpstreamFinishReason
if v, ok := m["upstream_finish_reason"].(string); ok && v != "" {
    reqLog.UpstreamFinishReason = strPtr(v)
}
```

因为在调用点 `reqLog.UpstreamFinishReason` 还是 `nil`，导致以下合法响应被误判为 empty_response：

- Tool-call 响应（只有 tool_calls，没有 text content）
- 良性的 `eof_without_done` + 空 delta chunks
- 推理模型 `length` 截断无 reasoning 文本
- Anthropic stream 的 tool_calls 路径没有调用 `ObserveChunk`

**修复方案**：直接读取 `m["upstream_finish_reason"]`（已由 SummaryAsMap 填充），而不是读取 `reqLog.UpstreamFinishReason`。

---

## 🔧 修复步骤

### 前置准备

1. **获取数据库访问权限**
   ```bash
   export DB_HOST=<184-ip>
   export DB_PORT=5432
   export DB_USER=kxuser
   export DB_NAME=llm_gateway
   export DB_PASSWORD=<password>
   ```

2. **获取 API Key（用于测试）**
   ```bash
   export API_KEY=<your-api-key>
   ```

---

### 步骤 1: 运行诊断脚本

```bash
cd /path/to/llm-gateway-go-2
./scripts/test_71_routing.sh
```

这个脚本会检查：
- 健康检查是否通过
- minimax-m3 的配置是否正确
- canonical_id 是否一致
- credential_model_bindings 状态
- credential_model_index 是否有数据
- 凭据状态
- 最近的 request_logs

**预期输出**：脚本会明确指出哪些地方有问题。

---

### 步骤 2: 运行修复脚本

```bash
./scripts/fix_71_routing_complete.sh
```

这个脚本会：
1. 诊断所有配置问题
2. 自动修复 canonical_id 不一致
3. 初始化路由索引（如果为空）
4. 验证修复结果
5. 测试实际请求
6. 检查 empty_response 统计

**交互式执行**：脚本会在每个步骤后暂停，让你检查输出。

---

### 步骤 3: 验证修复结果

#### 3.1 检查路由索引

```sql
SELECT 
    COUNT(*) AS total_records,
    COUNT(DISTINCT credential_id) AS unique_credentials,
    MAX(bucket) AS latest_bucket
FROM credential_model_index
WHERE raw_model = 'minimax-m3';
```

**期望结果**：至少有 1 条记录，`latest_bucket` 在最近 10 分钟内。

#### 3.2 发送测试请求

```bash
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 10
  }'
```

**期望结果**：
- HTTP 200 + 正常响应
- 或上游 API 错误（非 503 no_candidate）

#### 3.3 检查 request_logs

```sql
SELECT 
    ts,
    request_id,
    client_model,
    credential_id,
    request_status,
    success
FROM request_logs
WHERE ts > now() - interval '5 minutes'
  AND client_model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 5;
```

**期望结果**：有新记录，`credential_id` 不为 NULL。

---

### 步骤 4: 处理 empty_response 问题

empty_response 的修复已在代码中完成（commit 78de1295），但需要重新部署。

#### 4.1 检查当前代码版本

```bash
cd /path/to/llm-gateway-go-2
git log --oneline -1
```

确认最新提交包含 `78de1295` 或更新。

#### 4.2 重新编译

```bash
go build -o /tmp/gateway-new ./cmd/gateway
```

#### 4.3 部署到 71 服务器

```bash
# 停止当前 Gateway
systemctl stop llm-gateway
# 或 docker stop gateway

# 备份旧版本
cp /path/to/gateway /path/to/gateway.backup

# 部署新版本
scp /tmp/gateway-new 71:/path/to/gateway

# 启动新版本
systemctl start llm-gateway
# 或 docker start gateway
```

#### 4.4 验证 empty_response 减少

等待 1 小时后：

```sql
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE error_kind = 'empty_response') AS empty_response_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE error_kind = 'empty_response') / NULLIF(COUNT(*), 0), 2) AS percentage
FROM request_logs
WHERE ts > now() - interval '1 hour';
```

**期望结果**：empty_response 比例应该从 ~30% 降到 <5%。

---

## 🔍 故障排查

### 问题 A: 修复后仍然 no_candidate

**可能原因**：Gateway 缓存未刷新

**解决方案**：
1. 等待 30 秒（缓存 TTL）
2. 或重启 Gateway：`systemctl restart llm-gateway`

---

### 问题 B: credential_reveal_failed

**可能原因**：Gateway 无法解密凭据

**解决方案**：
```bash
# 检查 Gateway 的加密密钥配置
echo $CREDENTIAL_ENCRYPTION_KEY
echo $SECRET_KEY

# 检查 Gateway 日志
docker logs gateway 2>&1 | grep -i "credential.*key"
```

如果密钥缺失或错误，更新环境变量并重启。

---

### 问题 C: 路由索引初始化后仍为空

**可能原因**：没有可用的凭据

**诊断**：
```sql
SELECT 
    c.id,
    p.code,
    c.availability_state,
    c.lifecycle_status,
    c.status
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE p.code IN ('minimax', 'nvidia', 'minimax-anthropic')
  AND c.status != 'disabled';
```

如果所有凭据都 `availability_state != 'ready'`，需要修复凭据状态：

```sql
UPDATE credentials
SET availability_state = 'ready'
WHERE id = <credential_id>;
```

---

### 问题 D: empty_response 仍然很多

**可能原因**：
1. 代码未部署最新版本
2. 确实是上游返回空响应（Provider 18 NVIDIA NIM 已知有 ~13% 真实空响应率）

**诊断**：
```sql
-- 按 provider 分组
SELECT 
    p.code AS provider,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE rl.error_kind = 'empty_response') AS empty_resp,
    ROUND(100.0 * COUNT(*) FILTER (WHERE rl.error_kind = 'empty_response') / COUNT(*), 2) AS pct
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE rl.ts > now() - interval '1 hour'
GROUP BY p.code
ORDER BY empty_resp DESC;
```

如果 NVIDIA (provider 18) 的空响应率高，这是已知问题，非 Gateway bug。

---

## 📊 监控指标

### 关键 SQL 查询

#### 1. 路由成功率
```sql
SELECT 
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE request_status != 'no_candidate') AS routed,
    ROUND(100.0 * COUNT(*) FILTER (WHERE request_status != 'no_candidate') / COUNT(*), 2) AS success_rate
FROM request_logs
WHERE ts > now() - interval '1 hour'
  AND client_model = 'minimax-m3';
```

#### 2. 索引健康度
```sql
SELECT 
    raw_model,
    COUNT(DISTINCT credential_id) AS credentials,
    MAX(bucket) AS latest_bucket,
    CASE 
        WHEN MAX(bucket) > now() - interval '10 minutes' THEN 'fresh'
        WHEN MAX(bucket) > now() - interval '1 hour' THEN 'stale'
        ELSE 'expired'
    END AS status
FROM credential_model_index
GROUP BY raw_model
ORDER BY latest_bucket DESC
LIMIT 20;
```

#### 3. 请求成功率
```sql
SELECT 
    client_model,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE success) AS success,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success) / COUNT(*), 2) AS success_rate
FROM request_logs
WHERE ts > now() - interval '1 hour'
GROUP BY client_model
ORDER BY total DESC
LIMIT 10;
```

---

## 📝 预防措施

### 1. 自动化路由索引初始化

在 Gateway 启动时，如果 `credential_model_index` 为空，自动从 `credential_model_bindings` 初始化。

### 2. Canonical ID 一致性检查

添加数据库约束或定期检查脚本，确保 `model_aliases`、`provider_models`、`models_canonical` 的 canonical_id 一致。

### 3. 监控告警

设置告警：
- 当路由索引最新时间 > 15 分钟时告警
- 当 no_candidate 比例 > 10% 时告警
- 当 empty_response 比例突然上升时告警

---

## 📚 相关文档

- [MINIMAX_M3_FINAL_REPORT.md](./MINIMAX_M3_FINAL_REPORT.md) - minimax-m3 配置完整报告
- [2026-06-26-empty-response-misclassification-fix.md](./2026-06-26-empty-response-misclassification-fix.md) - empty_response 修复详情
- [REQUEST_LOGS_DIAGNOSIS_FINAL.md](./REQUEST_LOGS_DIAGNOSIS_FINAL.md) - request_logs 前端显示问题

---

## ✅ 检查清单

在认为问题已解决前，确认以下所有项：

- [ ] `credential_model_index` 有 minimax-m3 的记录
- [ ] 索引的 `bucket` 时间在最近 10 分钟内
- [ ] canonical_id 在所有表中一致
- [ ] `v_routable_credential_models` 显示至少 1 个 is_routable=true
- [ ] 测试请求返回 200 或上游错误（非 503 no_candidate）
- [ ] `request_logs` 有新记录且 `credential_id` 不为 NULL
- [ ] Gateway 日志显示 "provider.GetCandidates" 返回 count > 0
- [ ] empty_response 比例 < 10%
- [ ] 最新代码已部署（包含 commit 78de1295）

---

**最后更新**: 2026-06-26  
**维护者**: LLM Gateway Team
