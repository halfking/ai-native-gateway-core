# LLM Gateway 错误处理指南

> **文档版本：** v1.0  
> **更新日期：** 2026-11-18  
> **适用范围：** llm-gateway-go-integrate

## 目录

- [一、错误处理架构](#一错误处理架构)
- [二、错误分类体系](#二错误分类体系)
- [三、自动路由切换机制](#三自动路由切换机制)
- [四、会话诊断日志](#四会话诊断日志)
- [五、故障排查流程](#五故障排查流程)
- [六、常见问题与解决方案](#六常见问题与解决方案)

---

## 一、错误处理架构

### 1.1 整体流程

```
客户端请求
    ↓
[鉴权层] → 401/403 错误
    ↓
[路由层] → routing_* 错误
    ↓
[执行层] → 供应商调用
    ↓
[错误分类器] ClassifyResponseStatus
    ↓
[决策引擎] DecideNextAction
    ↓
├─ ActionSwitchNode → 切换到备用节点
├─ ActionWaitRecovery → 等待恢复
├─ ActionRetrySameNode → 同节点重试
└─ ActionFailTerminal → 返回客户端
```

### 1.2 核心组件

**errorsx.ClassifyResponseStatus**
- 位置：`llm-gateway-go-integrate/errorsx/classify.go`
- 功能：将HTTP状态码和响应体分类为20+种ErrorKind
- 输入：HTTP状态码、响应体、供应商信息
- 输出：ErrorKind、重试建议、等待时间

**errorsx.DecideNextAction**
- 位置：`llm-gateway-go-integrate/errorsx/action_policy.go`
- 功能：根据ErrorKind决定下一步动作
- 输入：DecisionContext（包含Kind、历史、剩余尝试次数）
- 输出：ActionDecision（动作类型、是否记录失败、重试延迟）

**writeErrorJSON系列函数**
- 位置：`llm-gateway-go-integrate/domains/streaming/handler.go`
- 功能：将错误写入HTTP响应
- 当前问题：使用硬编码消息，未保留供应商原始错误

---

## 二、错误分类体系

### 2.1 ErrorKind 完整列表

| ErrorKind | 含义 | HTTP状态 | 重试策略 | 记录到节点健康 |
|-----------|------|----------|---------|---------------|
| `KindTransient` | 临时故障 | 503 | 同节点重试3次 | ✅ |
| `KindRateLimit` | 节点限流（429） | 429 | 等待后重试 | ✅ |
| `KindRateLimitObserved` | 上游说限流（400+限流信息） | 400 | 换节点 | ❌ |
| `KindRequestRejected` | 请求侧错误（4xx无明确归因） | 400 | 换节点 | ❌ |
| `KindModelUnavailable` | 模型不可用（裸404） | 404 | 跨凭据重试 | ✅ 绑定级 |
| `KindAuth` | 凭据认证失败 | 401 | 换凭据 | ✅ |
| `KindQuota` | 配额耗尽 | 402 | 等待恢复 | ✅ |
| `KindUpstreamDown` | 上游服务不可用 | 503 | 等待恢复 | ✅ |
| `KindUpstreamOverloaded` | 上游过载 | 502 | 等待恢复（短） | ✅ |
| `KindModelNotFound` | 模型未找到（终态） | 404 | ❌ 不重试 | ❌ |
| `KindContentFilter` | 内容过滤拦截 | 400 | ❌ 不重试 | ❌ |
| `KindContextLength` | 上下文长度超限 | 400 | trim后重试1次 | ❌ |
| `KindToolCallIdMismatch` | 工具调用ID不匹配 | 400 | ❌ 不重试 | ❌ |
| `KindEmptyResponse` | 空响应 | 200 | 换节点 | ✅ 软降级 |
| `KindCanceled` | 客户端取消 | 499 | ❌ 不重试 | ❌ |

### 2.2 错误分类逻辑

**HTTP 4xx 分类规则：**

```go
// 1. 优先匹配具体错误类型
if isAuthError(body) {
    return KindAuth
}
if isQuotaError(body) {
    return KindQuota
}
if isRateLimitError(body) && status == 429 {
    return KindRateLimit
}
if isRateLimitError(body) && status != 429 {
    return KindRateLimitObserved  // 状态码与body不一致
}
if isContentFilterError(body) {
    return KindContentFilter
}
if isContextLengthError(body) {
    return KindContextLength
}

// 2. 状态码兜底
switch status {
case 404:
    if hasModelDeprecationSignal(body) {
        return KindModelDeprecated
    }
    if body == "" || isGeneric404(body) {
        return KindModelUnavailable  // 裸404
    }
    return KindModelNotFound  // 有错误体的404
case 400, 413, 414, 422:
    return KindRequestRejected  // 无明确归因的请求错误
default:
    return KindTransient  // 兜底
}
```

**HTTP 5xx 分类规则：**

```go
switch status {
case 502:
    if isOverloadError(body) {
        return KindUpstreamOverloaded
    }
    return KindUpstreamDown
case 503:
    if isConcurrencyError(body) {
        return KindConcurrent
    }
    if isNoAvailableChannelError(body) {
        return KindNoAvailableChannel
    }
    return KindUpstreamDown
default:
    return KindTransient
}
```

---

## 三、自动路由切换机制

### 3.1 决策树

```
DecideNextAction(ctx DecisionContext)
    │
    ├─ 历史校验失败？ → ActionFailClosed
    │
    ├─ 客户端取消？ → ActionClientCanceled
    │
    ├─ 终态错误？ → ActionFailTerminal
    │   (KindModelNotFound, KindContentFilter, KindModelDeprecated等)
    │
    ├─ 可重试错误？
    │   ├─ 同节点预算未用完？ → ActionRetrySameNode
    │   └─ 有备用节点？ → ActionSwitchNode
    │       └─ 无备用节点？ → ActionFailTerminal
    │
    ├─ 请求侧错误？ → ActionSwitchNode
    │   (KindRequestRejected, KindRateLimitObserved, KindModelUnavailable)
    │   └─ 无备用节点？ → ActionFailTerminal
    │
    ├─ 凭据/配额错误？ → ActionWaitRecovery
    │   (KindAuth, KindQuota, KindRateLimit, KindUpstreamDown)
    │
    └─ 未映射错误？ → ActionFailClosed
```

### 3.2 关键参数

**同节点重试预算：**
- 默认：每个节点3次尝试
- 适用：KindTransient、KindTimeout、KindNetwork、KindEmptyResponse

**跨节点切换条件：**
- 有备用候选节点（HasAlternateNode=true）
- 剩余尝试次数 > 0（RemainingAttempts > 0）
- 错误类型支持切换（非终态）

**等待恢复延迟：**
```go
KindRateLimit:        3分钟
KindQuota:            1小时
KindUpstreamDown:     30分钟
KindUpstreamOverloaded: 5分钟
```

### 3.3 节点健康状态

**状态记录条件：**
```go
ActionDecision {
    RecordProviderFailure: bool      // 记录供应商级失败
    RecordCredentialFailure: bool    // 记录凭据级失败
    RecordCredentialState: bool      // 写入node_probe_state
}
```

**不记录的情况：**
- 请求侧错误（KindRequestRejected、KindRateLimitObserved）
- 客户端错误（KindClientBug、KindToolCallIdMismatch）
- 客户端取消（KindCanceled）

---

## 四、会话诊断日志

### 4.1 设计目标

为每个请求会话维护一个完整的诊断日志，记录：
- 路由决策过程
- 每次尝试的结果
- 错误分类和处理决策
- 自动切换历史

### 4.2 数据结构

```go
// SessionDiagnosticLog 会话诊断日志
type SessionDiagnosticLog struct {
    RequestID      string                 `json:"request_id"`
    SessionID      string                 `json:"session_id,omitempty"`
    ClientModel    string                 `json:"client_model"`
    TenantID       string                 `json:"tenant_id"`
    StartTime      time.Time              `json:"start_time"`
    EndTime        time.Time              `json:"end_time"`
    FinalStatus    int                    `json:"final_status"`
    Success        bool                   `json:"success"`
    
    // 路由信息
    RoutingDecision RoutingDecision       `json:"routing_decision"`
    
    // 执行历史
    Attempts       []AttemptRecord        `json:"attempts"`
    
    // 最终结果
    FinalError     *ErrorRecord           `json:"final_error,omitempty"`
    
    // 统计
    TotalAttempts  int                    `json:"total_attempts"`
    Switches       int                    `json:"switches"`
    Retries        int                    `json:"retries"`
    TotalDuration  int64                  `json:"total_duration_ms"`
}

// RoutingDecision 路由决策信息
type RoutingDecision struct {
    Candidates     []CandidateInfo        `json:"candidates"`
    SelectedIndex  int                    `json:"selected_index"`
    SelectionTime  time.Time              `json:"selection_time"`
    RoutingLatency int64                  `json:"routing_latency_ms"`
}

// AttemptRecord 单次尝试记录
type AttemptRecord struct {
    AttemptNo      int                    `json:"attempt_no"`
    Provider       ProviderInfo           `json:"provider"`
    StartTime      time.Time              `json:"start_time"`
    EndTime        time.Time              `json:"end_time"`
    Duration       int64                  `json:"duration_ms"`
    
    // 请求信息
    UpstreamURL    string                 `json:"upstream_url"`
    UpstreamMethod string                 `json:"upstream_method"`
    
    // 响应信息
    StatusCode     int                    `json:"status_code"`
    Success        bool                   `json:"success"`
    
    // 错误信息
    Error          *ErrorRecord           `json:"error,omitempty"`
    
    // 决策信息
    ErrorKind      string                 `json:"error_kind,omitempty"`
    NextAction     string                 `json:"next_action,omitempty"`
    ActionReason   string                 `json:"action_reason,omitempty"`
}

// ProviderInfo 供应商信息
type ProviderInfo struct {
    ProviderID     int                    `json:"provider_id"`
    ProviderName   string                 `json:"provider_name"`
    CredentialID   string                 `json:"credential_id"`
    Model          string                 `json:"model"`
    CatalogCode    string                 `json:"catalog_code,omitempty"`
}

// ErrorRecord 错误详情
type ErrorRecord struct {
    Source         string                 `json:"source"` // "gateway" | "provider"
    Type           string                 `json:"type"`
    Message        string                 `json:"message"`
    Code           string                 `json:"code"`
    
    // 上游错误（如果有）
    UpstreamError  *UpstreamErrorInfo     `json:"upstream_error,omitempty"`
}

// UpstreamErrorInfo 上游错误详情
type UpstreamErrorInfo struct {
    StatusCode     int                    `json:"status_code"`
    Type           string                 `json:"type,omitempty"`
    Message        string                 `json:"message,omitempty"`
    Code           string                 `json:"code,omitempty"`
    Body           string                 `json:"body,omitempty"` // 完整响应体（截断到4KB）
}
```

### 4.3 存储方案

**方案A：数据库存储（推荐）**

新增表：`request_diagnostic_logs`

```sql
CREATE TABLE request_diagnostic_logs (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL UNIQUE,
    session_id VARCHAR(255),
    tenant_id VARCHAR(255),
    client_model VARCHAR(255),
    
    start_time TIMESTAMP NOT NULL,
    end_time TIMESTAMP NOT NULL,
    final_status INT NOT NULL,
    success BOOLEAN NOT NULL,
    
    -- JSON字段存储详细信息
    routing_decision JSONB,
    attempts JSONB NOT NULL,
    final_error JSONB,
    
    -- 统计字段（用于查询和聚合）
    total_attempts INT NOT NULL,
    switches INT NOT NULL DEFAULT 0,
    retries INT NOT NULL DEFAULT 0,
    total_duration_ms BIGINT NOT NULL,
    
    created_at TIMESTAMP DEFAULT NOW(),
    
    INDEX idx_request_id (request_id),
    INDEX idx_session_id (session_id),
    INDEX idx_tenant_id (tenant_id),
    INDEX idx_start_time (start_time),
    INDEX idx_success (success),
    INDEX idx_final_status (final_status)
);

-- 分区策略：按时间分区，每天一个分区
CREATE TABLE request_diagnostic_logs_y2026m11d18 
    PARTITION OF request_diagnostic_logs
    FOR VALUES FROM ('2026-11-18 00:00:00') TO ('2026-11-19 00:00:00');

-- 保留策略：保留30天
```

**方案B：Redis缓存 + 异步落库**

1. 请求进行中：存储在Redis `diagnostic:req:{request_id}`，TTL=1小时
2. 请求完成后：异步写入PostgreSQL
3. 实时查询：优先查Redis，回退到DB

### 4.4 使用方式

**1. 初始化日志：**

```go
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    requestID := generateRequestID()
    
    // 初始化诊断日志
    diagLog := diagnostics.NewSessionLog(requestID)
    diagLog.ClientModel = getClientModel(r)
    diagLog.TenantID = getTenantID(r)
    diagLog.StartTime = time.Now()
    
    // 存入context
    ctx := context.WithValue(r.Context(), diagnosticsKey, diagLog)
    r = r.WithContext(ctx)
    
    // ... 处理请求 ...
    
    // 请求结束时保存
    defer func() {
        diagLog.EndTime = time.Now()
        diagLog.TotalDuration = diagLog.EndTime.Sub(diagLog.StartTime).Milliseconds()
        h.diagnosticLogger.Save(ctx, diagLog)
    }()
}
```

**2. 记录路由决策：**

```go
candidates, err := h.resolver.GetCandidates(ctx, model, profile)
diagLog := diagnostics.FromContext(ctx)
diagLog.RecordRoutingDecision(candidates, selectedIndex, routingLatency)
```

**3. 记录尝试：**

```go
attemptNo := 1
for _, candidate := range candidates {
    attempt := diagLog.StartAttempt(attemptNo, candidate)
    
    resp, err := h.executor.Execute(ctx, candidate, req)
    
    attempt.EndTime = time.Now()
    attempt.Duration = attempt.EndTime.Sub(attempt.StartTime).Milliseconds()
    attempt.StatusCode = resp.StatusCode
    attempt.Success = err == nil
    
    if err != nil {
        // 记录错误详情
        attempt.RecordError(err, resp)
        
        // 记录决策
        kind := errorsx.ClassifyResponseStatus(resp.StatusCode, resp.Body)
        decision := errorsx.DecideNextAction(...)
        attempt.ErrorKind = string(kind)
        attempt.NextAction = string(decision.Action)
        attempt.ActionReason = decision.ReasonCode
    }
    
    diagLog.AddAttempt(attempt)
    attemptNo++
    
    if err == nil || decision.Action == ActionFailTerminal {
        break
    }
}
```

**4. 查询日志：**

```go
// 通过request_id查询
log, err := diagLogger.Get(ctx, requestID)

// 通过session_id查询最近N条
logs, err := diagLogger.ListBySession(ctx, sessionID, limit)

// 查询失败请求
failedLogs, err := diagLogger.ListFailed(ctx, startTime, endTime, limit)
```

### 4.5 API接口

**GET /internal/diagnostics/request/:request_id**

返回指定请求的完整诊断日志。

**GET /internal/diagnostics/session/:session_id**

返回指定会话的所有请求诊断日志。

**GET /internal/diagnostics/failures**

查询参数：
- `start_time`: 开始时间
- `end_time`: 结束时间
- `tenant_id`: 租户ID（可选）
- `model`: 模型（可选）
- `status_code`: HTTP状态码（可选）
- `limit`: 返回数量

---

## 五、故障排查流程

### 5.1 快速诊断

**步骤1：获取request_id**

从客户端错误响应中获取：
```json
{
  "error": {...},
  "request_id": "req-abc-123"
}
```

或从响应头获取：
```
X-Request-Id: req-abc-123
```

**步骤2：查询诊断日志**

```bash
curl "https://gateway.example.com/internal/diagnostics/request/req-abc-123"
```

**步骤3：分析失败原因**

检查字段：
- `attempts[].error_kind`: 错误类型
- `attempts[].next_action`: 采取的动作
- `attempts[].error.upstream_error`: 上游原始错误
- `switches`: 切换次数（0表示未切换）
- `retries`: 重试次数

### 5.2 常见故障模式

**模式1：所有候选都失败**

```json
{
  "total_attempts": 3,
  "switches": 2,
  "success": false,
  "attempts": [
    {"provider_id": 7, "error_kind": "rate_limit", "next_action": "switch_node"},
    {"provider_id": 8, "error_kind": "auth", "next_action": "switch_node"},
    {"provider_id": 9, "error_kind": "quota", "next_action": "fail_terminal"}
  ]
}
```

**分析：**
- Provider 7：限流
- Provider 8：认证失败
- Provider 9：配额耗尽
- 无可用节点，最终失败

**模式2：循环重试**

```json
{
  "total_attempts": 10,
  "switches": 9,
  "attempts": [
    {"provider_id": 7, "error_kind": "transient"},
    {"provider_id": 8, "error_kind": "transient"},
    {"provider_id": 7, "error_kind": "transient"},  // 循环
    {"provider_id": 8, "error_kind": "transient"},
    ...
  ]
}
```

**分析：**
- 历史追踪机制失效
- 检查DecisionHistory是否正确传递

**模式3：错误分类错误**

```json
{
  "attempts": [{
    "status_code": 400,
    "error_kind": "transient",  // ❌ 应该是request_rejected
    "next_action": "retry_same_node",
    "upstream_error": {
      "message": "Invalid parameter: temperature must be between 0 and 2"
    }
  }]
}
```

**分析：**
- 分类器未正确识别400错误
- 导致无效重试

---

## 六、常见问题与解决方案

### 6.1 上游错误信息丢失

**问题：**
客户端看到的是网关通用消息，而非供应商原始错误。

**现状：**
```go
writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
    "Rate limit exceeded", "server_error", "rate_limit")
```

客户端收到：
```json
{
  "error": {
    "type": "server_error",
    "message": "Rate limit exceeded",
    "code": "rate_limit"
  }
}
```

**解决方案：**

使用增强的错误写入函数：

```go
// 从executor错误中提取上游信息
var execErr *executors.ExecuteError
if errors.As(err, &execErr) && execErr.UpstreamError != nil {
    writeErrorJSONEnhanced(
        w,
        http.StatusTooManyRequests,
        requestID,
        "Rate limit exceeded",
        "rate_limit_error",
        "rate_limit",
        "provider",  // 来源
        execErr.UpstreamError,
        &execErr.ProviderID,
        execErr.ProviderName,
        &RetryInfo{
            Retryable:    true,
            RetryAfter:   60,
            AttemptsMade: attemptCount,
            MaxAttempts:  maxAttempts,
        },
    )
}
```

客户端收到：
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
      "code": "rate_limit_exceeded"
    },
    "retry_info": {
      "retryable": true,
      "retry_after": 60,
      "attempts_made": 2,
      "max_attempts": 3
    }
  },
  "request_id": "req-abc-123"
}
```

### 6.2 错误来源不明确

**问题：**
无法判断错误是网关自身问题还是供应商问题。

**解决方案：**

在所有错误响应中添加`source`字段：
- `"gateway"`: 网关层错误（鉴权、路由、配置等）
- `"provider"`: 供应商错误（上游API返回的错误）

### 6.3 自动切换不可见

**问题：**
客户端不知道发生了failover。

**解决方案：**

1. 添加响应头：
```
X-Gateway-Attempts: 3
X-Gateway-Provider-Chain: 7,8,9
X-Gateway-Final-Provider: 9
X-Gateway-Failover: true
```

2. 在错误响应中包含`routing_history`（参见HTML报告）

### 6.4 日志查询缓慢

**问题：**
诊断日志表过大，查询缓慢。

**解决方案：**

1. 使用分区表（按天分区）
2. 建立合适的索引
3. 定期归档旧数据（>30天）
4. 使用Redis缓存热数据

---

## 附录

### A. 相关文件

- 错误分类：`llm-gateway-go-integrate/errorsx/classify.go`
- 决策引擎：`llm-gateway-go-integrate/errorsx/action_policy.go`
- 路由错误：`llm-gateway-go-integrate/domains/streaming/routing_errors.go`
- 错误写入：`llm-gateway-go-integrate/domains/streaming/handler.go`
- 完整审计报告：`error-handling-audit-report.html`

### B. 监控指标

建议添加以下Prometheus指标：

```
gateway_error_total{kind, source, provider}
gateway_failover_total{reason}
gateway_retry_total{action}
gateway_diagnostic_log_writes_total
gateway_diagnostic_log_query_duration_seconds
```

### C. 参考资料

- [错误处理审计报告 (HTML)](../../../error-handling-audit-report.html)
- [会话诊断日志设计文档](./session-diagnostic-logging.md)
- [errorsx包设计文档](../design/errorsx-design.md)

---

**文档维护者：** Platform Team  
**问题反馈：** [提交Issue](https://github.com/yourorg/llm-gateway/issues)
