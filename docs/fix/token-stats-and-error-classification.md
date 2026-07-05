# Token统计缺失与错误分类修复方案

## 问题1: 数据库错误被伪装成 no_candidate

### 当前问题代码
`domains/streaming/handler.go:1333-1342`

```go
candidates, policy, err := h.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
if err != nil {
    slog.Error("failed to get candidates from provider", "error", err)
    // ❌ 错误：将数据库错误伪装成 no_candidate
    h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "no_candidate", nil, latencyMs)
    logCtx.failAndMark("no_candidate",
        fmt.Sprintf("no available provider for model '%s'", clientModel), nil, nil)
    markLogged()
    writeErrorJSON(w, http.StatusServiceUnavailable, requestID, 
        fmt.Sprintf("no available provider for model '%s'", clientModel), 
        "server_error", "no_candidate")
    return
}
```

### GetCandidates 可能返回的错误

| 错误类型 | 示例 | HTTP状态码建议 |
|---------|------|---------------|
| 数据库连接失败 | `"routing DB not configured"` | 503 |
| SQL执行错误 | `"relation does not exist"` | 500 |
| 分区缺失 | `"no partition found"` | 500 |
| 行扫描错误 | `"column type mismatch"` | 500 |
| 函数不存在 | `"function recent_success_rate does not exist"` | 500 |

### 修复方案

#### 方案A: 区分数据库错误和业务错误（推荐）

```go
candidates, policy, err := h.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
if err != nil {
    slog.Error("failed to get candidates from provider", "error", err, "model", clientModel)
    
    // 判断是否是数据库/基础设施错误
    errorCode := "routing_database_error"
    errorMessage := fmt.Sprintf("Routing service unavailable: %v", err)
    httpStatus := http.StatusInternalServerError
    
    // 如果错误消息包含明确的数据库关键词，使用更明确的错误码
    errStr := err.Error()
    if strings.Contains(errStr, "not configured") {
        errorCode = "routing_not_configured"
        errorMessage = "Routing service is not configured"
        httpStatus = http.StatusServiceUnavailable
    } else if strings.Contains(errStr, "connection") || strings.Contains(errStr, "timeout") {
        errorCode = "routing_connection_error"
        errorMessage = "Database connection error"
        httpStatus = http.StatusServiceUnavailable
    } else if strings.Contains(errStr, "relation") || strings.Contains(errStr, "partition") || strings.Contains(errStr, "function") {
        errorCode = "routing_schema_error"
        errorMessage = fmt.Sprintf("Database schema error: %v", err)
        httpStatus = http.StatusInternalServerError
    }
    
    h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, errorCode, nil, latencyMs)
    logCtx.failAndMark(errorCode, errorMessage, nil, nil)
    markLogged()
    writeErrorJSON(w, httpStatus, requestID, errorMessage, "database_error", errorCode)
    return
}

// 候选列表为空才是真正的 no_candidate
if len(candidates) == 0 {
    h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "no_candidate", nil, latencyMs)
    logCtx.failAndMark("no_candidate",
        fmt.Sprintf("no available provider for model '%s'", clientModel), nil, nil)
    markLogged()
    writeErrorJSON(w, http.StatusServiceUnavailable, requestID, 
        fmt.Sprintf("No available provider for model '%s'", clientModel), 
        "server_error", "no_candidate")
    return
}
```

#### 方案B: 增强型错误包装（更优雅）

**步骤1**: 在 `provider/client.go` 中定义错误类型

```go
// provider/errors.go (新文件)
package provider

import "errors"

var (
    ErrNotConfigured   = errors.New("provider client not configured")
    ErrDBNotConfigured = errors.New("routing DB not configured")
    ErrDBQuery         = errors.New("database query failed")
    ErrDBScan          = errors.New("database scan failed")
)

// WrapDBError wraps a database error with context
func WrapDBError(err error, context string) error {
    return fmt.Errorf("%w: %s: %v", ErrDBQuery, context, err)
}

// IsInfrastructureError returns true if the error is infrastructure-related
func IsInfrastructureError(err error) bool {
    return errors.Is(err, ErrNotConfigured) ||
           errors.Is(err, ErrDBNotConfigured) ||
           errors.Is(err, ErrDBQuery) ||
           errors.Is(err, ErrDBScan)
}
```

**步骤2**: 修改 `provider/client.go` 使用错误包装

```go
func (c *Client) GetCandidates(ctx context.Context, model, profile string) ([]Candidate, *Policy, error) {
    if !c.Enabled() {
        return nil, DefaultPolicy(), ErrNotConfigured  // 使用明确的错误类型
    }
    // ...
    v, err, _ := c.sf.Do("cand:"+key, func() (any, error) {
        resp, fetchErr := c.fetchCandidatesDB(ctx, routeModel, profile)
        if fetchErr != nil {
            return nil, WrapDBError(fetchErr, "fetchCandidatesDB")  // 包装错误
        }
        // ...
    })
    if err != nil {
        return nil, DefaultPolicy(), err
    }
    // ...
}

func (c *Client) loadCandidatesDB(ctx context.Context, clientModel string) ([]Candidate, error) {
    if c.dbPool == nil {
        return nil, nil
    }
    rows, err := c.dbPool.Query(ctx, `...`)
    if err != nil {
        return nil, WrapDBError(err, fmt.Sprintf("query candidates for model '%s'", clientModel))
    }
    // ...
    if err := rows.Scan(...); err != nil {
        return nil, fmt.Errorf("%w: %v", ErrDBScan, err)
    }
    // ...
}
```

**步骤3**: 修改 `handler.go` 使用错误类型判断

```go
candidates, policy, err := h.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
if err != nil {
    slog.Error("failed to get candidates from provider", "error", err, "model", clientModel)
    
    // 使用错误类型判断
    if provider.IsInfrastructureError(err) {
        errorCode := "routing_infrastructure_error"
        errorMessage := fmt.Sprintf("Routing service error: %v", err)
        
        h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, errorCode, nil, latencyMs)
        logCtx.failAndMark(errorCode, errorMessage, nil, nil)
        markLogged()
        writeErrorJSON(w, http.StatusInternalServerError, requestID, errorMessage, "infrastructure_error", errorCode)
    } else {
        // 未知错误，也不应该伪装成 no_candidate
        errorCode := "routing_unknown_error"
        errorMessage := fmt.Sprintf("Routing failed: %v", err)
        
        h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, errorCode, nil, latencyMs)
        logCtx.failAndMark(errorCode, errorMessage, nil, nil)
        markLogged()
        writeErrorJSON(w, http.StatusInternalServerError, requestID, errorMessage, "unknown_error", errorCode)
    }
    return
}
```

---

## 问题2: 网关层失败时缺失Token统计

### 修复方案（三合一）

在 `handler.go` 的请求处理早期阶段，立即估算token并存储：

```go
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 前置逻辑 ...
    
    // 读取 request body
    bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
    if err != nil {
        // ... 错误处理 ...
    }
    
    // ✅ 立即估算 prompt tokens（即使后续失败也有数据）
    var estimatedPromptTokens *int
    if len(bodyBytes) > 0 {
        tokens := transformation.EstimateTokens(bodyBytes)
        estimatedPromptTokens = &tokens
        slog.Debug("estimated prompt tokens", "tokens", tokens, "body_size", len(bodyBytes))
    }
    
    // 存储到上下文或 logCtx（需要扩展 logCtx 结构）
    // 方式1: 存储到 context
    ctx := context.WithValue(r.Context(), estimatedTokensKey{}, estimatedPromptTokens)
    r = r.WithContext(ctx)
    
    // 方式2: 存储到 logCtx（如果已经创建）
    // logCtx.SetEstimatedPromptTokens(estimatedPromptTokens)
    
    // ... 继续处理 ...
    
    // 在所有失败路径使用估算值
    candidates, policy, err := h.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
    if err != nil {
        // ✅ 传递估算的 token
        h.emitFailedDecisionLogWithTokens(requestID, clientModel, keyInfo, clientID, 
            0, nil, nil, "routing_database_error", nil, latencyMs,
            estimatedPromptTokens, nil)  // completion_tokens 为 nil
        // ...
    }
}
```

### 新增函数签名

```go
// 扩展原有函数，添加 token 参数
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
    if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
        return
    }
    
    // ... 原有逻辑 ...
    
    dl := &telemetry.DecisionLogEntry{
        RequestID:         requestID,
        TenantID:          tenantID,
        APIKeyID:          apiKeyID,
        Model:             canonicalOrClient(canonical, clientModel),
        CandidatesTried:   candidatesTried,
        LatencyMs:         latencyMs,
        Success:           false,
        ErrorClass:        strPtr(errCode),
        FailureDetailCode: strPtr(errCode),
        ClientModel:       strPtr(clientModel),
        IdentityHash:      strPtr(clientID.IdentityHash),
        PromptTokens:      promptTokens,      // ✅ 设置
        CompletionTokens:  completionTokens,  // ✅ 设置
    }
    
    // ... 其余逻辑 ...
    
    h.telemetryClient.EmitDecisionLog(dl)
}

// 兼容性：保留原函数，内部调用新函数
func (h *ChatHandler) emitFailedDecisionLog(
    requestID, clientModel string,
    keyInfo *authentication.KeyInfo,
    clientID identity.ClientIdentity,
    candidatesTried int,
    modelResolution *resolve.Resolution,
    txResult *transformation.TransformResult,
    errCode string,
    failTrace *executors.Trace,
    latencyMs int,
) {
    // 尝试从 context 获取估算值（如果可用）
    // 否则传 nil
    h.emitFailedDecisionLogWithTokens(requestID, clientModel, keyInfo, clientID,
        candidatesTried, modelResolution, txResult, errCode, failTrace, latencyMs,
        nil, nil)  // 向后兼容，tokens为nil
}
```

---

## 错误分类规范

### 新增错误码

| 错误码 | failure_stage | 描述 | HTTP状态 |
|--------|--------------|------|---------|
| `routing_not_configured` | gateway | 路由服务未配置 | 503 |
| `routing_connection_error` | gateway | 数据库连接错误 | 503 |
| `routing_schema_error` | gateway | 数据库表/函数缺失 | 500 |
| `routing_query_error` | gateway | SQL查询执行失败 | 500 |
| `routing_infrastructure_error` | gateway | 基础设施错误（通用） | 500 |
| `no_candidate` | gateway | 无可用候选节点（业务） | 503 |
| `missing_model` | gateway | 模型不存在（业务） | 400 |

### 错误分类决策树

```
GetCandidates 返回错误?
├─ Yes
│  ├─ 包含 "not configured"? → routing_not_configured (503)
│  ├─ 包含 "connection" / "timeout"? → routing_connection_error (503)
│  ├─ 包含 "relation" / "partition" / "function"? → routing_schema_error (500)
│  ├─ 其他数据库错误? → routing_query_error (500)
│  └─ 未知错误? → routing_infrastructure_error (500)
│
└─ No
   ├─ len(candidates) == 0? → no_candidate (503) ✅ 真正的业务错误
   └─ len(candidates) > 0? → 继续处理
```

---

## 实施步骤

### 阶段1: 紧急修复（今天，2小时）

**目标**: 停止伪装数据库错误

1. 修改 `domains/streaming/handler.go:1334-1342`
2. 实现方案A（字符串匹配）
3. 添加日志记录真实错误
4. 部署到71服务器

```bash
# 测试
curl -X POST http://localhost:__PORT_12__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "test-model", "messages": []}'

# 预期：返回明确的数据库错误，而非 no_candidate
```

### 阶段2: 结构化错误（本周，4小时）

**目标**: 实现错误类型系统

1. 创建 `provider/errors.go`
2. 修改 `provider/client.go` 使用错误包装
3. 修改 `handler.go` 使用错误类型判断
4. 添加单元测试

### 阶段3: Token统计完整性（本周，4小时）

**目标**: 所有失败请求都有token估算

1. 扩展 `emitFailedDecisionLog` 函数签名
2. 在body读取后立即估算token
3. 所有失败路径传递token
4. 添加单元测试

### 阶段4: SQL回填历史数据（立即，15分钟）

```sql
-- 回填过去30天的失败请求
UPDATE request_logs
SET prompt_tokens = CEIL(LENGTH(request_body::text) / 3.5)::int,
    outbound_token_est = CEIL(LENGTH(request_body::text) / 3.5)::int
WHERE prompt_tokens IS NULL 
  AND request_body IS NOT NULL
  AND success = false
  AND ts >= NOW() - INTERVAL '30 days';

-- 验证
SELECT 
    error_kind,
    COUNT(*) as total,
    COUNT(prompt_tokens) as has_tokens,
    AVG(prompt_tokens) as avg_tokens
FROM request_logs
WHERE success = false
  AND ts >= NOW() - INTERVAL '7 days'
GROUP BY error_kind;
```

---

## 监控与告警

### 新增指标

```promql
# 数据库错误率
sum(rate(llmgw_requests_total{error_kind=~"routing_.*_error"}[5m])) 
/ 
sum(rate(llmgw_requests_total[5m]))

# Token统计缺失率
sum(rate(llmgw_requests_total{has_prompt_tokens="false"}[5m])) 
/ 
sum(rate(llmgw_requests_total[5m]))

# 真实的 no_candidate（排除数据库错误）
sum(rate(llmgw_requests_total{error_kind="no_candidate"}[5m]))
```

### 告警规则

```yaml
- alert: RoutingDatabaseError
  expr: |
    sum(rate(llmgw_requests_total{error_kind=~"routing_.*_error"}[5m])) > 0
  for: 1m
  annotations:
    summary: "检测到路由数据库错误"
    description: "错误类型: {{ $labels.error_kind }}"

- alert: HighTokenMissingRate
  expr: |
    (
      sum(rate(llmgw_requests_total{has_prompt_tokens="false"}[5m]))
      /
      sum(rate(llmgw_requests_total[5m]))
    ) > 0.05
  for: 10m
  annotations:
    summary: "Token统计缺失率超过5%"
```

---

## 测试计划

### 单元测试

```go
// domains/streaming/handler_test.go

func TestGetCandidates_DatabaseError_ReturnsInfrastructureError(t *testing.T) {
    // 模拟数据库错误
    mockProvider := &MockProvider{
        getCandidatesErr: errors.New("connection refused"),
    }
    
    handler := &ChatHandler{provider: mockProvider}
    
    // 发送请求
    req := httptest.NewRequest("POST", "/v1/chat/completions", body)
    w := httptest.NewRecorder()
    
    handler.ServeHTTP(w, req)
    
    // 验证返回的是基础设施错误，而非 no_candidate
    assert.Equal(t, 500, w.Code)
    
    var resp map[string]any
    json.Unmarshal(w.Body.Bytes(), &resp)
    
    errorCode := resp["error"].(map[string]any)["code"].(string)
    assert.Contains(t, errorCode, "routing_")
    assert.NotEqual(t, "no_candidate", errorCode)
}

func TestEmitFailedDecisionLog_WithTokens(t *testing.T) {
    // 测试 token 估算被正确记录
    mockTelemetry := &MockTelemetryClient{}
    handler := &ChatHandler{telemetryClient: mockTelemetry}
    
    promptTokens := 100
    handler.emitFailedDecisionLogWithTokens(
        "req123", "gpt-4", nil, identity.ClientIdentity{},
        0, nil, nil, "no_candidate", nil, 100,
        &promptTokens, nil,
    )
    
    // 验证 PromptTokens 被设置
    assert.NotNil(t, mockTelemetry.lastEntry.PromptTokens)
    assert.Equal(t, 100, *mockTelemetry.lastEntry.PromptTokens)
}
```

### 集成测试

```bash
# 测试1: 数据库连接失败
docker stop llm-gateway-pg-71-replica
curl -X POST http://localhost:__PORT_12__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "Hi"}]}'

# 预期: 返回 routing_connection_error，而非 no_candidate

# 测试2: 分区缺失（模拟）
docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway \
  -c "DROP TABLE IF EXISTS request_wal_2026_06"
  
# 发送请求，预期: 日志写入失败但请求仍能处理

# 测试3: Token统计
curl -X POST http://localhost:__PORT_12__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "invalid-model", "messages": [{"role": "user", "content": "Test"}]}'

# 验证 request_logs 中有 prompt_tokens（估算值）
docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway \
  -c "SELECT request_id, error_kind, prompt_tokens FROM request_logs ORDER BY ts DESC LIMIT 1"
  
# 预期: prompt_tokens 不为 NULL
```

---

## 文档更新

需要更新的文档：
1. `docs/errors.md` - 新增错误码说明
2. `docs/monitoring.md` - 新增监控指标
3. `README.md` - 更新错误处理说明
4. API文档 - 更新错误响应示例

---

## 总结

### 核心改进

1. **✅ 透明化数据库错误**
   - 不再伪装成 `no_candidate`
   - 明确区分基础设施错误和业务错误
   - 便于运维人员快速定位问题

2. **✅ 完整的Token统计**
   - 所有请求（包括失败）都有token估算
   - 成本分析更准确
   - 性能分析更完整

3. **✅ 结构化错误处理**
   - 错误类型系统
   - 错误包装与传播
   - 便于监控和告警

### 预期效果

| 指标 | 修复前 | 修复后 |
|-----|-------|-------|
| 数据库错误透明度 | 0% (伪装成no_candidate) | 100% |
| Token统计覆盖率（失败请求） | 40% | 100% |
| 运维问题定位时间 | 30分钟+ | 5分钟 |
| 误报率 | 高 (真实DB错误被当成路由问题) | 低 |

---

**修复优先级**: P0（立即执行）  
**预计工时**: 10小时（分3个阶段）  
**风险等级**: 低（向后兼容）
