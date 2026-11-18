# 错误处理增强 - 实施指南

> **版本：** P0 Phase 1  
> **日期：** 2026-11-18  
> **状态：** 实施中

## 概述

本指南说明如何将现有的错误处理代码迁移到增强版本，以支持：
- 错误来源标识（`source`: gateway/provider）
- 上游错误信息保留（`upstream_error`）
- 重试信息暴露（`retry_info`）
- 响应头增强（`X-Gateway-*`）

## 新增文件

### 1. 核心实现

```
domains/streaming/
├── error_response_enhanced.go              # 增强的错误响应结构和函数
├── error_response_integration_examples.go  # 集成示例
└── error_response_enhanced_test.go         # 单元测试
```

### 2. 关键类型

```go
// UpstreamErrorInfo - 上游错误详情
type UpstreamErrorInfo struct {
    StatusCode int                    `json:"status_code"`
    Type       string                 `json:"type,omitempty"`
    Message    string                 `json:"message,omitempty"`
    Code       string                 `json:"code,omitempty"`
    Body       string                 `json:"body,omitempty"`
    Details    map[string]interface{} `json:"details,omitempty"`
}

// RetryInfo - 重试信息
type RetryInfo struct {
    Retryable    bool `json:"retryable"`
    RetryAfter   int  `json:"retry_after,omitempty"`
    AttemptsMade int  `json:"attempts_made"`
    MaxAttempts  int  `json:"max_attempts"`
}

// EnhancedErrorResponse - 增强的错误响应
type EnhancedErrorResponse struct {
    Error struct {
        Type          string             `json:"type"`
        Message       string             `json:"message"`
        Code          string             `json:"code"`
        Source        string             `json:"source"`
        ProviderID    *int               `json:"provider_id,omitempty"`
        ProviderName  string             `json:"provider_name,omitempty"`
        UpstreamError *UpstreamErrorInfo `json:"upstream_error,omitempty"`
        RetryInfo     *RetryInfo         `json:"retry_info,omitempty"`
    } `json:"error"`
    RequestID string `json:"request_id,omitempty"`
}
```

### 3. 核心函数

```go
// 主函数
func writeErrorJSONEnhanced(
    w http.ResponseWriter,
    status int,
    requestID string,
    gatewayMsg string,
    errType string,
    code string,
    source string,
    upstreamErr *UpstreamErrorInfo,
    providerID *int,
    providerName string,
    retryInfo *RetryInfo,
)

// 辅助函数
func extractUpstreamErrorInfo(execErr error, respBody []byte, statusCode int) *UpstreamErrorInfo
func buildRetryInfoFromExecuteError(execErr *executors.ExecuteError, maxAttempts int) *RetryInfo
func determineErrorSource(execErr error) string
```

## 迁移步骤

### 阶段1：渐进式迁移（推荐）

#### 步骤1：识别关键错误处理点

使用以下命令找到所有`writeErrorJSON`调用点：

```bash
cd llm-gateway-go-integrate/domains/streaming
grep -n "writeErrorJSON\|writeErrorJSONWithDebug\|writeErrorJSONCtx" handler.go | head -50
```

#### 步骤2：按优先级迁移

**优先级1（P0）：ExecuteError处理**

迁移前：
```go
if execErr != nil {
    writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
        "Service unavailable", "server_error", "service_unavailable")
    return
}
```

迁移后：
```go
if execErr != nil {
    h.handleExecuteErrorEnhanced(w, r, requestID, execErr, candidates)
    return
}
```

**优先级2（P0）：路由错误处理**

迁移前：
```go
if routingErr != nil {
    class := classifyRoutingError(routingErr)
    writeErrorJSON(w, class.httpStatus, requestID, class.message, "server_error", class.code)
    return
}
```

迁移后：
```go
if routingErr != nil {
    h.handleRoutingErrorEnhanced(w, requestID, routingErr)
    return
}
```

**优先级3（P1）：鉴权错误处理**

迁移前：
```go
if authErr != nil {
    writeErrorJSON(w, http.StatusUnauthorized, requestID,
        "Authentication failed", "authentication_error", "invalid_api_key")
    return
}
```

迁移后：
```go
if authErr != nil {
    h.handleAuthErrorEnhanced(w, requestID, authErr)
    return
}
```

### 阶段2：批量迁移

对于大量相似的调用点，可以使用包装函数：

```go
// 向后兼容包装器
func (h *ChatHandler) writeErrorJSONSmart(
    w http.ResponseWriter,
    status int,
    requestID string,
    msg string,
    errType string,
    code string,
    execErr error,
    candidates []provider.Candidate,
) {
    source := "gateway"
    var upstreamErr *UpstreamErrorInfo
    var providerID *int
    var providerName string
    var retryInfo *RetryInfo
    
    // 尝试提取ExecuteError信息
    var ee *executors.ExecuteError
    if errors.As(execErr, &ee) {
        source = "provider"
        
        if len(ee.Attempts) > 0 {
            lastAttempt := ee.Attempts[len(ee.Attempts)-1]
            if ee.Trace != nil && ee.Trace.UpstreamResponseBody != nil {
                upstreamErr = extractUpstreamErrorInfo(
                    execErr,
                    ee.Trace.UpstreamResponseBody,
                    lastAttempt.HTTPStatus,
                )
            }
            providerID = &lastAttempt.ProviderID
            providerName = findProviderName(lastAttempt.ProviderID, candidates)
        }
        
        retryInfo = buildRetryInfoFromExecuteError(ee, len(candidates))
    }
    
    writeErrorJSONEnhanced(
        w, status, requestID, msg, errType, code,
        source, upstreamErr, providerID, providerName, retryInfo,
    )
}
```

然后批量替换：
```bash
# 查找并替换（需要人工确认）
find . -name "*.go" -exec sed -i.bak 's/writeErrorJSON(/h.writeErrorJSONSmart(/g' {} \;
```

## 测试验证

### 1. 单元测试

运行新增的测试：

```bash
cd domains/streaming
go test -v -run TestWriteErrorJSONEnhanced
go test -v -run TestExtractUpstreamErrorInfo
```

### 2. 集成测试

创建测试场景验证响应格式：

```go
func TestEndToEnd_EnhancedErrorResponse(t *testing.T) {
    // 设置测试环境
    handler := setupTestHandler()
    
    // 模拟供应商返回429错误
    mockProvider := &MockProvider{
        Response: &http.Response{
            StatusCode: 429,
            Body: io.NopCloser(strings.NewReader(`{
                "error": {
                    "message": "Rate limit exceeded for model gpt-4",
                    "type": "rate_limit_error",
                    "code": "rate_limit_exceeded"
                }
            }`)),
        },
    }
    
    // 发送请求
    req := httptest.NewRequest("POST", "/v1/chat/completions", requestBody)
    w := httptest.NewRecorder()
    
    handler.ServeHTTP(w, req)
    
    // 验证响应
    var resp EnhancedErrorResponse
    json.Unmarshal(w.Body.Bytes(), &resp)
    
    assert.Equal(t, "provider", resp.Error.Source)
    assert.NotNil(t, resp.Error.UpstreamError)
    assert.Equal(t, 429, resp.Error.UpstreamError.StatusCode)
    assert.Equal(t, "Rate limit exceeded for model gpt-4", resp.Error.UpstreamError.Message)
    assert.NotNil(t, resp.Error.RetryInfo)
    assert.True(t, resp.Error.RetryInfo.Retryable)
}
```

### 3. 回归测试

确保旧客户端仍能正常工作：

```go
func TestBackwardCompatibility_OldClient(t *testing.T) {
    w := httptest.NewRecorder()
    
    writeErrorJSONEnhanced(
        w, http.StatusBadRequest, "req-123",
        "Bad request", "invalid_request_error", "invalid_param",
        "gateway", nil, nil, "", nil,
    )
    
    var resp map[string]interface{}
    json.Unmarshal(w.Body.Bytes(), &resp)
    
    // 旧客户端只读取基本字段
    errorObj := resp["error"].(map[string]interface{})
    assert.Equal(t, "invalid_request_error", errorObj["type"])
    assert.Equal(t, "Bad request", errorObj["message"])
    assert.Equal(t, "invalid_param", errorObj["code"])
    
    // 新字段存在但旧客户端可以忽略
    assert.Equal(t, "gateway", errorObj["source"])
}
```

## 响应格式对比

### 旧格式（保持向后兼容）

```json
{
  "error": {
    "type": "server_error",
    "message": "Service unavailable",
    "code": "service_unavailable"
  },
  "request_id": "req-123"
}
```

### 新格式（增强）

```json
{
  "error": {
    "type": "rate_limit_error",
    "message": "Rate limit exceeded",
    "code": "rate_limit",
    "source": "provider",
    "provider_id": 7,
    "provider_name": "openai",
    "upstream_error": {
      "status_code": 429,
      "type": "rate_limit_error",
      "message": "Rate limit exceeded for model gpt-4. Retry after 60 seconds.",
      "code": "rate_limit_exceeded",
      "body": "{\"error\":{\"message\":\"Rate limit exceeded\",\"type\":\"rate_limit_error\"}}"
    },
    "retry_info": {
      "retryable": true,
      "retry_after": 60,
      "attempts_made": 2,
      "max_attempts": 3
    }
  },
  "request_id": "req-456"
}
```

### 响应头（新增）

```
X-Request-Id: req-456
X-Gateway-Failover: true
X-Gateway-Attempts: 2
```

## 迁移检查清单

- [ ] 新文件已添加到项目
- [ ] 单元测试通过
- [ ] 集成测试通过
- [ ] 回归测试通过
- [ ] ExecuteError处理已迁移
- [ ] 路由错误处理已迁移
- [ ] 鉴权错误处理已迁移
- [ ] 响应头正确设置
- [ ] 上游错误信息正确提取
- [ ] 重试信息正确构建
- [ ] 文档已更新
- [ ] API文档已更新

## 常见问题

### Q1: 如何确定错误来源（gateway vs provider）？

**A:** 使用`determineErrorSource`函数，或手动判断：
- 路由错误、鉴权错误、配置错误 → `"gateway"`
- ExecuteError（Tried > 0）→ `"provider"`

### Q2: 上游错误信息为空怎么办？

**A:** 这是正常的，某些错误（如网络超时）没有上游响应体。此时`upstream_error`字段为`null`。

### Q3: 如何处理多次尝试的情况？

**A:** 使用最后一次尝试的错误信息作为`upstream_error`，完整历史记录在会话诊断日志中。

### Q4: 旧客户端会受影响吗？

**A:** 不会。新字段（`source`, `upstream_error`, `retry_info`）是可选的，旧客户端可以忽略它们。

### Q5: 性能影响如何？

**A:** 最小影响：
- 错误提取：1-2ms
- JSON序列化：<1ms
- 响应体截断：O(1)

## 下一步

完成P0迁移后，继续P1任务：
1. 添加`routing_history`字段
2. 优化脱敏逻辑
3. 添加监控指标
4. 配置告警规则

## 参考

- [错误处理指南](./error-handling-guide.md)
- [审计报告](./error-handling-audit-report.html)
- [会话诊断日志](./error-handling-guide.md#四会话诊断日志)

---

**维护者：** Platform Team  
**更新日期：** 2026-11-18
