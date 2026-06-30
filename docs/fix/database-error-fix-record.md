# 数据库错误伪装问题修复记录

**修复日期**: 2026-06-30  
**问题**: 数据库连接/查询错误被伪装成 `no_candidate` 业务错误  
**修复人员**: AI Agent (Claude Opus 4)

---

## 修改文件清单

### 1. `domains/streaming/handler.go` ✅

**位置**: Line 1333-1341  
**修改类型**: 错误分类逻辑

**修改前**:
```go
candidates, policy, err := h.provider.GetCandidates(...)
if err != nil {
    slog.Error("failed to get candidates from provider", "error", err)
    h.emitFailedDecisionLog(..., "no_candidate", ...)  // ❌ 伪装
    writeErrorJSON(w, 503, "no available provider", "no_candidate")
}
```

**修改后**:
```go
candidates, policy, err := h.provider.GetCandidates(...)
if err != nil {
    slog.Error("failed to get candidates from provider", "error", err, "model", clientModel)
    
    // ✅ 根据错误内容分类
    errorCode := "routing_database_error"
    errorMessage := fmt.Sprintf("Routing service error: %v", err)
    httpStatus := http.StatusInternalServerError
    
    errStr := err.Error()
    if strings.Contains(errStr, "not configured") {
        errorCode = "routing_not_configured"
        httpStatus = http.StatusServiceUnavailable
    } else if strings.Contains(errStr, "connection") || strings.Contains(errStr, "timeout") {
        errorCode = "routing_connection_error"
        httpStatus = http.StatusServiceUnavailable
    } else if strings.Contains(errStr, "relation") || strings.Contains(errStr, "partition") || 
        strings.Contains(errStr, "function") || strings.Contains(errStr, "does not exist") {
        errorCode = "routing_schema_error"
    }
    
    h.emitFailedDecisionLog(..., errorCode, ...)  // ✅ 真实错误码
    writeErrorJSON(w, httpStatus, errorMessage, "database_error", errorCode)
}
```

---

### 2. `domains/streaming/responses.go` ✅

**位置**: Line 334-343  
**修改类型**: 错误分类逻辑 + 导入 strings 包

**添加导入**:
```go
import (
    // ... 其他导入
    "strings"  // ✅ 新增
    // ...
)
```

**修改前**:
```go
candidates, policy, candErr := h.chatHandler.provider.GetCandidates(...)
if candErr != nil || len(candidates) == 0 {  // ❌ 混在一起
    attemptErrCode = "no_candidate"
    writeResponsesError(w, 503, "No available provider", "no_candidate")
}
```

**修改后**:
```go
candidates, policy, candErr := h.chatHandler.provider.GetCandidates(...)
if candErr != nil {
    // ✅ 先处理数据库错误
    slog.Error("failed to get candidates from provider", "error", candErr, "model", clientModel)
    
    attemptErrCode = "routing_database_error"
    // ... 错误分类逻辑（同 handler.go）
    
    writeResponsesError(w, httpStatus, attemptErrMsg, "database_error", attemptErrCode)
    return
}
if len(candidates) == 0 {
    // ✅ 再处理真实的 no_candidate
    attemptErrCode = "no_candidate"
    writeResponsesError(w, 503, "No available provider", "no_candidate")
}
```

---

### 3. `domains/streaming/messages.go` ✅

**位置**: Line 391-400  
**修改类型**: 错误分类逻辑（strings 已导入）

**修改前**:
```go
candidates, policy, candErr := h.chatHandler.provider.GetCandidates(...)
if candErr != nil || len(candidates) == 0 {  // ❌ 混在一起
    attemptErrCode = "no_candidate"
    writeAnthropicError(w, 503, "overloaded_error", "No available provider")
}
```

**修改后**:
```go
candidates, policy, candErr := h.chatHandler.provider.GetCandidates(...)
if candErr != nil {
    // ✅ 先处理数据库错误
    slog.Error("failed to get candidates from provider", "error", candErr, "model", clientModel)
    
    attemptErrCode = "routing_database_error"
    // ... 错误分类逻辑（同 handler.go）
    
    writeAnthropicError(w, httpStatus, "api_error", attemptErrMsg)
    return
}
if len(candidates) == 0 {
    // ✅ 再处理真实的 no_candidate
    attemptErrCode = "no_candidate"
    writeAnthropicError(w, 503, "overloaded_error", "No available provider")
}
```

---

## 新增错误码

| 错误码 | HTTP状态 | 描述 | 示例错误 |
|--------|---------|------|---------|
| `routing_not_configured` | 503 | 路由服务未配置 | "provider client not configured" |
| `routing_connection_error` | 503 | 数据库连接失败 | "connection refused" |
| `routing_schema_error` | 500 | 数据库Schema问题 | "relation does not exist", "no partition found" |
| `routing_database_error` | 500 | 通用数据库错误 | 其他SQL错误 |
| `no_candidate` | 503 | 真实的无候选节点 | 查询成功但返回0条 |

---

## 错误分类逻辑

```
GetCandidates() 返回 error != nil?
│
├─ Yes → 基础设施错误
│   ├─ 包含 "not configured" → routing_not_configured (503)
│   ├─ 包含 "connection" / "timeout" → routing_connection_error (503)
│   ├─ 包含 "relation" / "partition" / "function" / "does not exist" → routing_schema_error (500)
│   └─ 其他 → routing_database_error (500)
│
└─ No → 继续检查
    ├─ len(candidates) == 0 → no_candidate (503) ✅ 真实的业务错误
    └─ len(candidates) > 0 → 正常处理
```

---

## 影响分析

### 对现有系统的影响

| 方面 | 影响 | 风险等级 |
|-----|------|---------|
| API 兼容性 | ✅ 向后兼容（新增错误码，不删除旧码） | 低 |
| 日志记录 | ✅ 更详细的错误信息 | 无 |
| 监控告警 | ⚠️ 需要添加新错误码的监控 | 低 |
| 客户端影响 | ⚠️ 可能需要更新错误处理逻辑 | 低 |

### 预期效果

**修复前**:
```
用户看到: HTTP 503 "no available provider for model 'gpt-4'"
实际原因: 数据库连接失败
运维行动: 检查模型配置、provider状态 ❌ 方向错误
排查时间: 30分钟+
```

**修复后**:
```
用户看到: HTTP 503 "Database connection error"
实际原因: 数据库连接失败
运维行动: 检查数据库连接、网络 ✅ 方向正确
排查时间: 5分钟
```

---

## 编译验证

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go build -o /tmp/llm-gateway-test ./cmd/gateway
```

**结果**: ✅ 编译成功，无错误

---

## 部署建议

### 部署步骤

1. **备份当前版本**
   ```bash
   docker commit llm-gateway-go llm-gateway-go:backup-20260630
   ```

2. **构建新镜像**
   ```bash
   docker build -t kx-llm-gateway-go:error-fix-20260630 .
   ```

3. **灰度测试** (可选)
   - 在测试环境部署
   - 模拟数据库连接失败
   - 验证返回的是 `routing_connection_error` 而非 `no_candidate`

4. **生产部署**
   ```bash
   docker stop llm-gateway-go
   docker run -d --name llm-gateway-go \
     --env-file /path/to/env \
     kx-llm-gateway-go:error-fix-20260630
   ```

5. **验证监控**
   - 观察新错误码 `routing_*_error` 的出现频率
   - 确认 `no_candidate` 错误数量是否下降

### 回滚方案

如果出现问题：
```bash
docker stop llm-gateway-go
docker start llm-gateway-go-backup
```

或使用备份镜像：
```bash
docker run -d --name llm-gateway-go \
  --env-file /path/to/env \
  llm-gateway-go:backup-20260630
```

---

## 测试计划

### 单元测试（待补充）

```go
// domains/streaming/handler_test.go

func TestGetCandidates_DatabaseConnectionError(t *testing.T) {
    mockProvider := &MockProvider{
        getCandidatesErr: errors.New("connection refused"),
    }
    handler := &ChatHandler{provider: mockProvider}
    
    req := httptest.NewRequest("POST", "/v1/chat/completions", body)
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    
    assert.Equal(t, 503, w.Code)
    
    var resp map[string]any
    json.Unmarshal(w.Body.Bytes(), &resp)
    
    errorCode := resp["error"].(map[string]any)["code"].(string)
    assert.Equal(t, "routing_connection_error", errorCode)
    assert.NotEqual(t, "no_candidate", errorCode)
}

func TestGetCandidates_SchemaError(t *testing.T) {
    mockProvider := &MockProvider{
        getCandidatesErr: errors.New("relation \"request_logs\" does not exist"),
    }
    handler := &ChatHandler{provider: mockProvider}
    
    req := httptest.NewRequest("POST", "/v1/chat/completions", body)
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    
    assert.Equal(t, 500, w.Code)
    
    var resp map[string]any
    json.Unmarshal(w.Body.Bytes(), &resp)
    
    errorCode := resp["error"].(map[string]any)["code"].(string)
    assert.Equal(t, "routing_schema_error", errorCode)
}

func TestGetCandidates_RealNoCandidate(t *testing.T) {
    mockProvider := &MockProvider{
        getCandidatesErr: nil,           // ✅ 无数据库错误
        candidates:       []Candidate{}, // ✅ 但返回空列表
    }
    handler := &ChatHandler{provider: mockProvider}
    
    req := httptest.NewRequest("POST", "/v1/chat/completions", body)
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    
    assert.Equal(t, 503, w.Code)
    
    var resp map[string]any
    json.Unmarshal(w.Body.Bytes(), &resp)
    
    errorCode := resp["error"].(map[string]any)["code"].(string)
    assert.Equal(t, "no_candidate", errorCode)  // ✅ 这才是真正的 no_candidate
}
```

### 集成测试

**测试1: 数据库连接失败**
```bash
# 停止数据库
docker stop llm-gateway-pg-71-replica

# 发送请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "Hi"}]}'

# 预期响应:
# {
#   "error": {
#     "message": "Database connection error",
#     "type": "database_error",
#     "code": "routing_connection_error"
#   }
# }

# 恢复数据库
docker start llm-gateway-pg-71-replica
```

**测试2: Schema错误（模拟分区缺失）**
```bash
# 删除8月分区（当前是6月，不会用到）
docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway \
  -c "DROP TABLE IF EXISTS request_wal_2026_08"

# 正常请求（不会触发，因为当前是6月）
# 需要等到8月或手动修改系统时间测试
```

**测试3: 真实的 no_candidate**
```bash
# 请求不存在的模型（但数据库正常）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{"model": "non-existent-model-12345", "messages": [{"role": "user", "content": "Hi"}]}'

# 预期响应:
# {
#   "error": {
#     "message": "No available provider for model 'non-existent-model-12345'",
#     "type": "server_error",
#     "code": "no_candidate"
#   }
# }
```

---

## 监控指标更新

### Prometheus指标

```promql
# 新增: 数据库错误率
sum(rate(llmgw_requests_total{error_kind=~"routing_.*_error"}[5m]))
/
sum(rate(llmgw_requests_total[5m]))

# 修正: 真实的 no_candidate（排除数据库错误）
sum(rate(llmgw_requests_total{error_kind="no_candidate"}[5m]))

# 按错误类型分组
sum by (error_kind) (rate(llmgw_requests_total{error_kind=~"routing_.*|no_candidate"}[5m]))
```

### 告警规则

```yaml
groups:
  - name: llm_gateway_routing_errors
    rules:
      # 数据库连接错误（立即告警）
      - alert: RoutingDatabaseConnectionError
        expr: |
          sum(rate(llmgw_requests_total{error_kind="routing_connection_error"}[5m])) > 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "路由数据库连接失败"
          description: "检测到数据库连接错误，所有路由请求将失败"
      
      # Schema错误（立即告警）
      - alert: RoutingDatabaseSchemaError
        expr: |
          sum(rate(llmgw_requests_total{error_kind="routing_schema_error"}[5m])) > 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "路由数据库Schema错误"
          description: "可能是表/分区/函数缺失，需立即检查"
      
      # 真实的 no_candidate（业务告警）
      - alert: HighNoCandidateRate
        expr: |
          (
            sum(rate(llmgw_requests_total{error_kind="no_candidate"}[5m]))
            /
            sum(rate(llmgw_requests_total[5m]))
          ) > 0.1
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "无候选节点错误率超过10%"
          description: "可能是模型配置、凭据健康度、或网络问题"
```

---

## 文档更新

需要同步更新以下文档：

1. **API文档** - 新增错误码说明
2. **运维手册** - 错误排查流程
3. **监控手册** - 新增告警规则
4. **变更日志** - 记录本次修改

---

## 总结

### 修改统计

- 修改文件: 3个
- 新增代码行: ~60行
- 删除代码行: ~15行
- 新增错误码: 4个
- 编译状态: ✅ 成功
- 测试状态: ⏳ 待补充单元测试

### 关键改进

1. **✅ 透明化错误**: 数据库错误不再伪装
2. **✅ 精准分类**: 5种错误类型，各有对应HTTP状态码
3. **✅ 快速定位**: 运维人员可立即识别问题类型
4. **✅ 向后兼容**: 不破坏现有API契约

### 下一步

1. 补充单元测试
2. 部署到测试环境验证
3. 添加监控告警规则
4. 观察1周后部署到生产环境

---

**修复完成时间**: 2026-06-30 19:00 CST  
**预计部署时间**: 2026-07-01 (经测试验证后)
