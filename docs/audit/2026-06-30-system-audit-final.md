# LLM Gateway 系统深度审计报告（最终版）
**日期**: 2026-06-30  
**审计范围**: Token统计完整性、错误处理、数据传输解析  
**服务器**: 14.103.174.71 (71服务器)  
**数据库**: llm_gateway @ llm-gateway-pg-71-replica:5432

---

## 执行摘要

通过三轮深度审计，最终确认了系统的真实问题。

### 核心发现

| 优先级 | 问题 | 状态 | 根因 |
|--------|------|------|------|
| **P0-1** | request_wal 6月分区缺失 | ✅ **已修复** | 只有7月分区，6月30日写入失败 |
| **P0-2** | Token统计缺失（60%失败请求） | ⚠️ **待修复** | 网关层失败时未调用EstimateTokens |
| **P1-1** | minimax-m2.7-quickspeed 路由失败 | ⚠️ 待修复 | 模型别名不存在 |
| **P1-2** | minimax-m3 空响应 | ⚠️ 待诊断 | 上游返回0 token |

---

## 问题1: request_wal分区缺失 (P0-1) ✅ 已修复

### 现象
```
WARN request_logger: CreateInitial failed
error="ERROR: no partition of relation \"request_wal\" found for row (SQLSTATE 23514)"
```

### 根因
- 当前日期: **2026-06-30**
- request_wal 只有 `2026_07` 分区，缺少 `2026_06` 分区
- request_logs 有 `default` 分区作为fallback，所以正常

### 修复
```sql
CREATE TABLE request_wal_2026_06 PARTITION OF request_wal
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');
```

**执行时间**: 2026-06-30 07:40 UTC  
**验证**: 错误日志停止 ✅

---

## 问题2: Token统计缺失 (P0-2) ⚠️ 核心问题

### 数据分析

#### 整体统计（过去24小时）
```
总请求: 192
有token统计: 171 (89.1%)
缺失token统计: 21 (10.9%)
```

#### 按成功/失败分类
```
成功请求:   157条, 100% 有token统计 ✅
失败请求:    35条,  40% 有token统计 ❌
```

#### 按错误类型分类

| 错误类型 | 失败阶段 | 总数 | 有Token | 覆盖率 |
|---------|---------|------|---------|--------|
| empty_response | upstream | 14 | 14 | **100%** ✅ |
| no_candidate | gateway | 9 | 0 | **0%** ❌ |
| missing_model | gateway | 4 | 0 | **0%** ❌ |
| canceled | upstream | 3 | 0 | **0%** ❌ |
| transient | upstream | 2 | 0 | **0%** ❌ |
| provider_error | upstream | 1 | 0 | **0%** ❌ |
| invalid_key | gateway | 1 | 0 | **0%** ❌ |

### 根因分析

#### 为什么empty_response有token？
- 请求到达上游提供商
- 上游返回了 `usage` 字段（即使 `completion_tokens=0`）
- 代码正常解析并写入数据库

#### 为什么其他错误没有token？

**1. 网关层错误 (gateway stage)**
- `no_candidate`, `missing_model`, `invalid_key`
- 请求在网关层被拦截，未发送到上游
- 代码路径: `domains/streaming/handler.go:1336-1348`

```go
// line 1336
h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 
    0, nil, nil, "no_candidate", nil, latencyMs)
```

**问题**: `emitFailedDecisionLog` 函数**没有传递 request_body**，也**没有调用 EstimateTokens**。

**2. 上游错误但未返回usage (upstream stage)**
- `canceled`, `transient`, `provider_error`
- 请求已发送，但上游返回错误（没有usage字段）
- 或者客户端取消（连接断开）

### 代码位置

#### DecisionLogEntry 结构体（支持token字段）
`domains/hooks/observability/telemetry/client.go:L140-L172`

```go
type DecisionLogEntry struct {
    RequestID        string  `json:"request_id"`
    // ...
    PromptTokens     *int    `json:"prompt_tokens,omitempty"`      // ✅ 字段存在
    CompletionTokens *int    `json:"completion_tokens,omitempty"`  // ✅ 字段存在
    RequestBytes     *int    `json:"request_bytes,omitempty"`
    ResponseBytes    *int    `json:"response_bytes,omitempty"`
    // ...
}
```

#### emitFailedDecisionLog 函数（未设置token）
`domains/streaming/handler.go:L2938-L2985`

```go
func (h *ChatHandler) emitFailedDecisionLog(requestID, clientModel string, 
    keyInfo *authentication.KeyInfo, clientID identity.ClientIdentity, 
    candidatesTried int, modelResolution *resolve.Resolution, 
    txResult *transformation.TransformResult, errCode string, 
    failTrace *executors.Trace, latencyMs int) {
    
    dl := &telemetry.DecisionLogEntry{
        RequestID:       requestID,
        Model:           canonicalOrClient(canonical, clientModel),
        Success:         false,
        ErrorClass:      strPtr(errCode),
        // ❌ 没有设置 PromptTokens
        // ❌ 没有设置 CompletionTokens
        // ❌ 没有设置 RequestBytes
        // ❌ 没有设置 ResponseBytes
    }
    h.telemetryClient.EmitDecisionLog(dl)
}
```

#### EstimateTokens 函数（未被调用）
`domains/transformation/ctx_compress.go:L448-L470`

```go
func EstimateTokens(bodyBytes []byte) int {
    // 估算逻辑：字符数 / 3.5
    // 但网关层失败时未调用此函数
}
```

### 影响范围

#### 成本分析影响
- 21条请求（10.9%）缺失token统计
- 无法计算这些请求的实际成本
- 特别是 `no_candidate` 错误（9次），客户端重试会导致更多成本

#### 性能分析影响
- 无法分析失败请求的输入大小
- 无法判断是否因输入过大导致失败

#### 用户体验影响
- 用户看到503错误但没有计费信息
- 可能引起计费争议

---

## 修复方案: Token统计缺失 (P0-2)

### 方案1: 在网关层失败时估算prompt_tokens（推荐）

#### 修改位置
`domains/streaming/handler.go:L1336` 和 `L1344`

#### 修改前
```go
h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 
    0, nil, nil, "no_candidate", nil, int(time.Since(startTime).Milliseconds()))
```

#### 修改后
```go
// 估算 prompt tokens
var estimatedPromptTokens *int
if bodyBytes != nil && len(bodyBytes) > 0 {
    tokens := transformation.EstimateTokens(bodyBytes)
    estimatedPromptTokens = &tokens
}

h.emitFailedDecisionLogWithTokens(requestID, clientModel, keyInfo, clientID, 
    0, nil, nil, "no_candidate", nil, 
    int(time.Since(startTime).Milliseconds()),
    estimatedPromptTokens, nil) // completion_tokens 为 nil
```

#### 新增函数签名
```go
func (h *ChatHandler) emitFailedDecisionLogWithTokens(
    requestID, clientModel string, 
    keyInfo *authentication.KeyInfo, 
    clientID identity.ClientIdentity, 
    candidatesTried int, 
    modelResolution *resolve.Resolution, 
    txResult *transformation.TransformResult, 
    errCode string, 
    failTrace *executors.Trace, 
    latencyMs int,
    promptTokens *int,      // 新增
    completionTokens *int,  // 新增
) {
    // ... 原有逻辑 ...
    dl := &telemetry.DecisionLogEntry{
        // ... 原有字段 ...
        PromptTokens:     promptTokens,      // 设置
        CompletionTokens: completionTokens,  // 设置
    }
    h.telemetryClient.EmitDecisionLog(dl)
}
```

### 方案2: 使用 outbound_token_est 字段回填

#### SQL回填脚本
```sql
-- 回填 prompt_tokens（基于 request_body 估算）
UPDATE request_logs
SET prompt_tokens = (
    CASE 
        WHEN request_body IS NOT NULL 
        THEN CEIL(LENGTH(request_body::text) / 3.5)::int
        ELSE NULL
    END
)
WHERE prompt_tokens IS NULL 
  AND request_body IS NOT NULL
  AND ts >= '2026-06-01';

-- 验证
SELECT 
    COUNT(*) as updated_count,
    AVG(prompt_tokens) as avg_tokens
FROM request_logs
WHERE prompt_tokens IS NOT NULL 
  AND success = false
  AND ts >= NOW() - INTERVAL '24 hours';
```

#### 优点
- 立即修复历史数据
- 不需要修改代码

#### 缺点
- 估算不精确（实际token可能有10-20%误差）
- 需要定期执行（新的失败请求仍会缺失）

### 方案3: 在 request_body 写入时立即估算（最佳）

#### 修改位置
`domains/streaming/handler.go` 中读取 `request_body` 后

```go
// 读取 request body
bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
// ...

// 立即估算 token（即使后续失败也有数据）
estimatedTokens := transformation.EstimateTokens(bodyBytes)

// 存储到 context 或 logCtx
logCtx.SetEstimatedPromptTokens(estimatedTokens)
```

#### 在失败时使用估算值
```go
// 网关层失败
h.emitFailedDecisionLog(..., logCtx.EstimatedPromptTokens())
```

#### 优点
- 一次性解决，所有失败路径都有token估算
- 估算逻辑集中，易于维护

#### 缺点
- 需要修改 `logCtx` 结构体
- 工作量稍大

---

## 修复优先级建议

### 立即执行（今天）

**1. 方案2: SQL回填历史数据** (15分钟)
```bash
# 在71服务器执行
docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway << 'EOSQL'
UPDATE request_logs
SET prompt_tokens = CEIL(LENGTH(request_body::text) / 3.5)::int
WHERE prompt_tokens IS NULL 
  AND request_body IS NOT NULL
  AND success = false
  AND ts >= '2026-06-01';
EOSQL
```

### 本周完成（P0）

**2. 方案3: 在读取body时立即估算** (4小时)
- 修改 `handler.go` 中的 body 读取逻辑
- 添加 `logCtx.SetEstimatedPromptTokens()`
- 在所有失败路径使用估算值
- 添加单元测试

### 本月完成（P1）

**3. 添加更精确的token计数器** (8小时)
- 集成 tiktoken 或类似库
- 针对不同模型使用不同的tokenizer
- 替换粗略估算（len/3.5）

---

## 问题3: minimax-m2.7-quickspeed 路由失败 (P1-1)

### 现象
9次 `no_candidate` 错误，全部来自 `minimax-m2.7-quickspeed`

### 根因
数据库中的模型名:
- `MiniMax-M2.7-highspeed` ✅
- `MiniMax-M2.7` ✅
- `minimax-m2.7` ✅

客户端请求的模型名:
- `minimax-m2.7-quickspeed` ❌ (不存在)

### 修复
```sql
-- 查找 canonical_id
SELECT id FROM models_canonical WHERE lower(canonical_name) = 'minimax-m2.7';
-- 假设返回 id = 15

-- 添加别名
INSERT INTO model_aliases (raw_name, canonical_id, status)
VALUES ('minimax-m2.7-quickspeed', 15, 'active')
ON CONFLICT (raw_name, canonical_id) DO NOTHING;
```

### 验证
```bash
./scripts/diagnose-routing.sh minimax-m2.7-quickspeed
```

---

## 问题4: minimax-m3 空响应 (P1-2)

### 现象
14次 `empty_response` 错误

### 详细数据
```
request_id: 5255b2d0934ea31119c175de1dc389d6
outbound_model: minimaxai/minimax-m3
credential_id: 19
prompt_tokens: 71979  ← 输入很大（72K tokens）
completion_tokens: 0  ← 输出为0
stream_chunk_count: 3 ← 收到3个chunk
stream_chunks_sent: 0 ← 但未发送给客户端
```

### 可能原因

1. **输入超过有效上下文窗口**
   - 72K tokens 可能超过模型的实际处理能力
   - 即使模型声称支持128K，实际可能有限制

2. **上游质量过滤**
   - 上游返回了内容，但被质量检查过滤掉
   - 检查 `quality_flags` 字段

3. **上游真实返回空**
   - 模型本身输出为空（极少见）

### 诊断步骤
```bash
# 1. 检查凭据19的健康状态
docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway \
  -c "SELECT id, availability_state, circuit_state, consecutive_failures, lifecycle_status 
      FROM credentials WHERE id = 19"

# 2. 检查上下文窗口配置
docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway \
  -c "SELECT canonical_name, context_window 
      FROM models_canonical 
      WHERE lower(canonical_name) LIKE '%minimax-m3%'"

# 3. 检查最近的成功请求的token数
docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway \
  -c "SELECT request_id, prompt_tokens, completion_tokens 
      FROM request_logs 
      WHERE client_model = 'minimax-m3' 
        AND success = true 
        AND ts >= NOW() - INTERVAL '7 days'
      ORDER BY prompt_tokens DESC
      LIMIT 10"
```

---

## 其他发现

### 1. 缓存使用率偏低
```
总请求: 192
使用缓存: 7 (3.6%)
缓存读取: 1008 tokens
```

**建议**: 检查缓存配置，提高命中率

### 2. 流式vs非流式统计
```
request_mode = 'chat': 192条 (100%)
其他模式: 0条
```

**结论**: 所有请求都是chat模式，流式处理

### 3. request_body完整性
```
成功请求: 100% 有 request_body (157/157)
失败请求: 100% 有 request_body (35/35)
```

**结论**: request_body 记录完整 ✅

### 4. response_body完整性
```
成功请求: 100% 有 response_body (157/157)
失败请求: 40% 有 response_body (14/35)
```

**分析**: 只有 `empty_response` 错误有response_body（因为上游返回了）

---

## 监控建议

### 新增告警

```yaml
# 1. Token统计缺失率
- alert: TokenStatisticsMissingRate
  expr: |
    (
      count(rate(llmgw_requests_total{prompt_tokens=""}[5m]))
      /
      count(rate(llmgw_requests_total[5m]))
    ) > 0.05
  for: 10m
  annotations:
    summary: "超过5%的请求缺失token统计"

# 2. 网关层失败率
- alert: HighGatewayFailureRate
  expr: |
    (
      sum(rate(llmgw_requests_total{failure_stage="gateway"}[5m]))
      /
      sum(rate(llmgw_requests_total[5m]))
    ) > 0.1
  for: 5m
  annotations:
    summary: "网关层失败率超过10%"

# 3. empty_response 错误率
- alert: HighEmptyResponseRate
  expr: |
    rate(llmgw_requests_total{error_kind="empty_response"}[5m]) > 0.1
  for: 10m
  annotations:
    summary: "empty_response错误率过高"
```

### 仪表板指标

```promql
# Token统计覆盖率（按错误类型）
sum by (error_kind) (
  rate(llmgw_requests_has_tokens_total[5m])
) 
/ 
sum by (error_kind) (
  rate(llmgw_requests_total[5m])
)

# 失败请求的平均输入token数
avg(llmgw_request_prompt_tokens{success="false"})

# 网关层vs上游层失败比例
sum by (failure_stage) (rate(llmgw_requests_total{success="false"}[5m]))
```

---

## 执行清单

### ✅ 已完成
- [x] 修复 request_wal 6月分区缺失
- [x] 确认 token统计缺失的根因
- [x] 分析所有错误类型的token覆盖率

### ⚡ 今天必须完成

- [ ] SQL回填历史数据的 prompt_tokens
- [ ] 添加 minimax-m2.7-quickspeed 别名
- [ ] 诊断 minimax-m3 空响应问题

### 📅 本周完成

- [ ] 实现方案3：在读取body时立即估算token
- [ ] 添加单元测试验证token估算逻辑
- [ ] 创建分区自动创建函数

### 📆 本月完成

- [ ] 集成 tiktoken 库，提高token估算精度
- [ ] 优化缓存使用率
- [ ] 实现空响应自动重试逻辑

---

## 审计总结

### 审计历程

1. **第一轮审计**: 查错数据库（crm vs llm_gateway），误判表不存在
2. **第二轮审计**: 发现分区缺失问题，修复 request_wal
3. **第三轮审计**: 深度分析token统计，找到根因

### 核心发现

**Token统计的真相**:
- ✅ 字段存在于数据库和结构体
- ✅ 成功请求100%有token统计
- ❌ 失败请求60%缺失token统计
- ❌ 根因：网关层失败时未调用EstimateTokens

### 关键教训

1. **数据库名称很重要**: 不同容器可能连接不同数据库
2. **分区vs表缺失**: 错误信息相似，需仔细区分
3. **代码路径分析**: 失败路径和成功路径的token统计逻辑不同
4. **数据验证**: 用实际数据验证假设（192条记录，171条有token）

---

## 附录：SQL诊断脚本合集

### A. Token统计分析
```sql
-- 按错误类型统计token覆盖率
SELECT 
    error_kind,
    failure_stage,
    COUNT(*) as total,
    COUNT(prompt_tokens) as has_tokens,
    ROUND(COUNT(prompt_tokens) * 100.0 / COUNT(*), 2) as coverage_pct
FROM request_logs
WHERE ts >= NOW() - INTERVAL '24 hours'
  AND success = false
GROUP BY error_kind, failure_stage
ORDER BY total DESC;
```

### B. 缺失token的请求详情
```sql
SELECT 
    request_id,
    ts,
    client_model,
    error_kind,
    failure_stage,
    CASE WHEN request_body IS NOT NULL 
         THEN 'has_body' ELSE 'no_body' END as body_status,
    LENGTH(request_body::text) as body_size
FROM request_logs
WHERE ts >= NOW() - INTERVAL '24 hours'
  AND prompt_tokens IS NULL
ORDER BY ts DESC
LIMIT 20;
```

### C. 回填token估算
```sql
-- 回填（估算公式: 字符数 / 3.5）
UPDATE request_logs
SET prompt_tokens = CEIL(LENGTH(request_body::text) / 3.5)::int
WHERE prompt_tokens IS NULL 
  AND request_body IS NOT NULL
  AND success = false
  AND ts >= '2026-06-01';

-- 验证回填结果
SELECT 
    COUNT(*) as total_backfilled,
    MIN(prompt_tokens) as min_tokens,
    MAX(prompt_tokens) as max_tokens,
    AVG(prompt_tokens) as avg_tokens,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY prompt_tokens) as median_tokens
FROM request_logs
WHERE success = false
  AND ts >= NOW() - INTERVAL '24 hours';
```

---

## 审计人员
- AI Agent (Claude Opus 4)
- 第一轮审计: 2026-06-30 14:00-15:30 CST (数据库错误)
- 第二轮审计: 2026-06-30 15:30-16:30 CST (分区修复)
- 第三轮审计: 2026-06-30 16:30-18:00 CST (token深度分析)
- 服务器: 14.103.174.71 (71)
- 数据库: llm_gateway @ llm-gateway-pg-71-replica
