# LLM Gateway 请求处理完整流程图（含异常处理）

## 概览

本文档描述了从客户端请求到达网关，到最终响应返回的完整流程，包括所有关键的异常处理路径。

---

## 完整流程图

```mermaid
graph TB
    Start[客户端发起请求] --> ServerReceive[HTTP Server 接收请求]

    ServerReceive --> Recovery[Recovery Middleware]
    Recovery -->|panic捕获| PanicHandle[记录panic日志<br/>返回500]
    PanicHandle --> End[结束]

    Recovery --> CORS[CORS Middleware]
    CORS --> ReqID[RequestID Middleware<br/>生成/提取RequestID]

    ReqID --> Auth[Auth Middleware]
    Auth -->|鉴权失败| AuthFail[返回401/403<br/>记录审计日志]
    AuthFail --> End

    Auth --> RateLimit[Rate Limit Middleware<br/>per-tenant/per-key]
    RateLimit -->|限流触发| RateLimitFail[返回429<br/>Too Many Requests]
    RateLimitFail --> End

    RateLimit --> ParseReq[解析请求体<br/>提取model/stream/messages]
    ParseReq -->|解析失败| ParseFail[返回400<br/>Invalid Request]
    ParseFail --> End

    ParseReq --> ValidateReq[验证请求<br/>model存在性/参数合法性]
    ValidateReq -->|验证失败| ValidateFail[返回400<br/>记录validation错误]
    ValidateFail --> End

    ValidateReq --> RouteModel[Model路由<br/>选择Provider]
    RouteModel -->|模型不支持| ModelNotFound[返回404<br/>Model Not Found]
    ModelNotFound --> End

    RouteModel --> ResourceGate[资源门控<br/>Fast-Path检查]
    ResourceGate -->|资源不足| ResourceDeny[返回503<br/>Service Unavailable]
    ResourceDeny --> AuditLog1[尝试记录审计日志]
    AuditLog1 -->|DB失败| AuditFallback1[记录本地日志<br/>Prometheus指标]
    AuditLog1 --> End
    AuditFallback1 --> End

    ResourceGate --> CreateSession[创建会话<br/>生成SessionID]
    CreateSession -->|DB写入失败| SessionCreateFail[降级处理<br/>仅内存记录]
    SessionCreateFail --> SessionMemOnly[标记session为降级模式]

    CreateSession --> PrepareUpstream[准备上游请求]
    SessionMemOnly --> PrepareUpstream

    PrepareUpstream --> SelectProvider[选择Provider实例<br/>负载均衡]
    SelectProvider -->|无可用实例| NoProvider[返回503<br/>No Available Provider]
    NoProvider --> UpdateSession1[更新Session状态<br/>标记为failed]
    UpdateSession1 --> End

    SelectProvider --> BuildUpstreamReq[构建上游请求<br/>转换格式/添加headers]

    BuildUpstreamReq --> IsStream{是否流式请求?}

    %% 非流式路径
    IsStream -->|否| NonStreamReq[发起HTTP请求<br/>设置超时]
    NonStreamReq -->|网络错误| NetworkErr1[记录错误<br/>尝试重试]
    NetworkErr1 --> RetryLogic1{是否可重试?}
    RetryLogic1 -->|是| SelectProvider
    RetryLogic1 -->|否| ReturnErr1[返回502<br/>Bad Gateway]
    ReturnErr1 --> UpdateSession2[更新Session<br/>记录错误信息]
    UpdateSession2 --> End

    NonStreamReq -->|超时| TimeoutErr1[记录超时<br/>返回504]
    TimeoutErr1 --> UpdateSession3[更新Session]
    UpdateSession3 --> End

    NonStreamReq --> ParseResp1[解析上游响应]
    ParseResp1 -->|Provider错误| ProviderErr1[解析错误码<br/>4xx/5xx]
    ProviderErr1 --> RetryLogic2{是否可重试?}
    RetryLogic2 -->|是| SelectProvider
    RetryLogic2 -->|否| ReturnProviderErr[返回Provider错误<br/>保持原始状态码]
    ReturnProviderErr --> UpdateSession4[更新Session]
    UpdateSession4 --> AuditLog2[记录审计日志]
    AuditLog2 --> End

    ParseResp1 --> CalcTokens1[计算Token使用量<br/>输入/输出tokens]
    CalcTokens1 --> UpdateSession5[更新Session<br/>completed状态]
    UpdateSession5 -->|DB失败| UpdateFallback1[仅更新缓存<br/>异步重试]
    UpdateSession5 --> AuditLog3[记录审计日志]
    UpdateFallback1 --> AuditLog3
    AuditLog3 -->|DB失败| MetricOnly1[仅记录Prometheus]
    AuditLog3 --> ReturnResp1[返回响应给客户端]
    MetricOnly1 --> ReturnResp1
    ReturnResp1 --> End

    %% 流式路径
    IsStream -->|是| StreamReq[发起SSE连接<br/>建立双向流]
    StreamReq -->|连接失败| StreamConnErr[记录错误<br/>尝试重试]
    StreamConnErr --> RetryLogic3{是否可重试?}
    RetryLogic3 -->|是| SelectProvider
    RetryLogic3 -->|否| ReturnErr2[返回502]
    ReturnErr2 --> UpdateSession6[更新Session]
    UpdateSession6 --> End

    StreamReq --> StreamLoop[流式循环处理]
    StreamLoop --> ReadChunk[读取SSE chunk]

    ReadChunk -->|读取错误| ReadErr{错误类型?}
    ReadErr -->|EOF| StreamComplete[流正常结束]
    ReadErr -->|网络中断| StreamNetErr[记录中断<br/>检查恢复条件]
    StreamNetErr --> Recovery1{是否可恢复?}
    Recovery1 -->|是<br/>L0-L4恢复| StreamRecover[重建连接<br/>继续传输]
    StreamRecover --> StreamLoop
    Recovery1 -->|否| StreamFail[标记流失败<br/>返回错误chunk]
    StreamFail --> UpdateSession7[更新Session<br/>partial失败]
    UpdateSession7 --> End

    ReadChunk -->|超时| StreamTimeout[发送心跳<br/>保持连接]
    StreamTimeout --> TimeoutCheck{心跳次数?}
    TimeoutCheck -->|超过限制| StreamAbort[中断流<br/>返回超时]
    StreamAbort --> UpdateSession8[更新Session]
    UpdateSession8 --> End
    TimeoutCheck -->|继续| StreamLoop

    ReadChunk --> ParseChunk[解析SSE数据]
    ParseChunk -->|解析失败| ParseChunkErr[记录解析错误<br/>跳过该chunk]
    ParseChunkErr --> StreamLoop

    ParseChunk --> IsData{是否data事件?}
    IsData -->|否<br/>comment/ping| StreamLoop
    IsData -->|是| ExtractDelta[提取delta内容]

    ExtractDelta --> IsError{是否错误chunk?}
    IsError -->|是| ProviderStreamErr[Provider返回错误]
    ProviderStreamErr --> FirstByteSent1{首字节已发送?}
    FirstByteSent1 -->|是| ErrorInline[内联错误到流中<br/>继续输出]
    ErrorInline --> StreamLoop
    FirstByteSent1 -->|否| ReturnErr3[返回完整错误<br/>中断流]
    ReturnErr3 --> UpdateSession9[更新Session]
    UpdateSession9 --> End

    IsError -->|否| AccumTokens[累积token计数<br/>更新usage]
    AccumTokens --> WriteChunk[写入响应流<br/>flush给客户端]

    WriteChunk -->|客户端断开| ClientDisconnect[检测断开<br/>中止上游]
    ClientDisconnect --> CleanupStream[清理资源<br/>关闭连接]
    CleanupStream --> UpdateSession10[更新Session<br/>标记interrupted]
    UpdateSession10 --> End

    WriteChunk --> MarkFirstByte{首字节?}
    MarkFirstByte -->|是| RecordFirstByte[记录TTFB指标<br/>更新Session]
    MarkFirstByte --> StreamLoop
    RecordFirstByte --> StreamLoop

    StreamComplete --> CalcTokens2[计算最终token<br/>汇总usage]
    CalcTokens2 --> SendDone[发送[DONE]标记]
    SendDone --> UpdateSession11[更新Session<br/>completed状态]
    UpdateSession11 -->|DB失败| UpdateFallback2[异步重试<br/>标记延迟写入]
    UpdateSession11 --> AuditLog4[记录审计日志]
    UpdateFallback2 --> AuditLog4
    AuditLog4 -->|DB失败| MetricOnly2[仅Prometheus]
    AuditLog4 --> CloseStream[关闭流连接]
    MetricOnly2 --> CloseStream
    CloseStream --> End

    %% 全局超时保护
    ServerReceive -.->|请求超时| GlobalTimeout[全局超时保护<br/>context cancel]
    GlobalTimeout --> AbortReq[中止所有操作]
    AbortReq --> CleanupAll[清理资源<br/>关闭连接]
    CleanupAll --> UpdateSession12[更新Session<br/>timeout状态]
    UpdateSession12 --> End

    %% Graceful Shutdown
    ServerReceive -.->|收到SIGTERM| GracefulShutdown[Graceful Shutdown]
    GracefulShutdown --> StopAccept[停止接受新请求]
    StopAccept --> WaitInFlight[等待进行中请求<br/>最多30s]
    WaitInFlight --> ForceClose[强制关闭剩余连接]
    ForceClose --> Exit[进程退出]
```

---

## 关键异常处理点详解

### 1. **Panic Recovery**
- **位置**: Recovery Middleware (最外层)
- **处理**: 捕获所有panic，记录完整堆栈，返回500
- **指标**: `panic_recovered_total`
- **审计**: 记录到error日志和Prometheus

### 2. **鉴权失败**
- **位置**: Auth Middleware
- **处理**:
  - API Key无效/过期 → 401
  - 权限不足 → 403
  - 记录审计日志（包含失败原因）
- **指标**: `auth_failed_total{reason}`

### 3. **限流触发**
- **位置**: Rate Limit Middleware
- **维度**: per-tenant, per-key, per-model
- **处理**: 返回429，带Retry-After header
- **指标**: `rate_limit_exceeded_total{dimension}`

### 4. **资源门控拒绝**
- **位置**: Fast-Path资源检查
- **条件**:
  - 并发请求数超限
  - 内存使用过高
  - Provider不可用
- **处理**: 返回503，尝试记录审计（可能降级）
- **指标**: `resource_gate_rejected_total{reason}`

### 5. **DB/Redis失败降级**
- **场景**:
  - Session创建失败 → 仅内存记录
  - Session更新失败 → 异步重试队列
  - 审计写入失败 → 本地日志 + Prometheus
- **策略**:
  - 优先保证请求成功
  - 异步补偿持久化
  - 告警阈值监控

### 6. **Provider错误重试**
- **可重试错误**:
  - 网络超时/连接失败
  - 429 (Provider限流)
  - 503 (Provider不可用)
  - 5xx 服务端错误
- **不可重试**:
  - 4xx 客户端错误（除429）
  - 认证错误
  - 模型不存在
- **策略**:
  - 最多3次重试
  - 指数退避 (100ms, 200ms, 400ms)
  - 切换Provider实例

### 7. **流式特殊处理**
#### 7.1 首字节前错误
- **行为**: 返回完整错误响应（JSON格式）
- **状态码**: 保持Provider原始错误码

#### 7.2 首字节后错误
- **行为**: 内联错误到SSE流中
- **格式**:
  ```
  data: {"error": {"message": "...", "type": "...", "code": "..."}}

  data: [DONE]
  ```
- **原因**: HTTP状态码已发送（200），无法修改

#### 7.3 流中断恢复 (L0-L4)
- **L0**: 客户端主动断开 → 立即中止
- **L1**: 网络抖动 → 重连同一实例
- **L2**: 实例故障 → 切换实例，重放已发送内容
- **L3**: 全链路故障 → 等待恢复，心跳保持
- **L4**: 超时放弃 → 标记partial失败

#### 7.4 心跳机制
- **触发**: 10秒内无数据
- **内容**: `: keep-alive\n\n`（SSE comment；客户端必须忽略，不按 `data` JSON 解析）
- **限制**: 最多6次（总计60秒），超过则中断

### 8. **客户端断开检测**
- **检测点**:
  - WriteChunk返回错误
  - Context.Done()触发
- **处理**:
  - 立即中止上游请求
  - 关闭Provider连接
  - 更新Session为interrupted
  - 记录已消耗token

### 9. **全局超时保护**
- **超时时间**:
  - 非流式: 30秒
  - 流式: 300秒
- **触发**: Context deadline exceeded
- **处理**:
  - 取消所有子操作
  - 关闭所有连接
  - 返回504 Gateway Timeout
  - 强制更新Session

### 10. **Graceful Shutdown**
- **信号**: SIGTERM / SIGINT
- **流程**:
  1. 停止接受新请求 (立即)
  2. 等待进行中请求完成（HTTP drain 最多23秒；随后 worker/依赖关闭阶段最多5秒）
  3. 强制关闭剩余 HTTP 连接
  4. 刷新所有缓冲区
  5. 关闭DB/Redis连接
  6. 进程退出
- **保护**:
  - 流式请求优先保护（延长等待）
  - 审计日志强制刷盘
  - 未完成Session标记为interrupted

---

## 关键数据流

### RequestID传递链路
```
Client Header (X-Request-ID)
  ↓
Middleware提取/生成
  ↓
Context传递
  ↓
Session记录
  ↓
上游请求 Header
  ↓
审计日志
  ↓
响应 Header
```

### Locale传递链路
```
Client Header (Accept-Language / X-Locale)
  ↓
解析并标准化 (en/zh/ja/...)
  ↓
Context存储
  ↓
错误消息本地化
  ↓
响应 Header (Content-Language)
```

### Auth信息传递
```
API Key (Header)
  ↓
Auth Middleware验证
  ↓
提取 tenant_id, user_id, permissions
  ↓
Context传递
  ↓
资源门控检查
  ↓
Session关联
  ↓
审计日志记录
```

### Token计数流
```
请求: 计算输入tokens (tiktoken)
  ↓
流式: 累积每个chunk的delta
  ↓
非流式: 从response.usage提取
  ↓
汇总: input_tokens + output_tokens
  ↓
Session更新
  ↓
审计记录
  ↓
计费系统 (异步)
```

---

## 可观测性埋点

### 指标 (Prometheus)
```
# 请求层
http_requests_total{method, path, status}
http_request_duration_seconds{method, path}

# 认证
auth_attempts_total{result}
auth_duration_seconds

# 限流
rate_limit_checks_total{result}
rate_limit_current{tenant, key}

# 资源门控
resource_gate_decisions_total{result, reason}
concurrent_requests{tenant, model}

# Provider
provider_requests_total{provider, model, status}
provider_request_duration_seconds{provider, model}
provider_ttfb_seconds{provider, model}  # 流式首字节

# Session
session_created_total{result}
session_updated_total{result}
session_duration_seconds{model, status}

# Token
tokens_consumed_total{tenant, model, type}  # type: input/output

# 错误
errors_total{type, component}
panics_recovered_total

# DB/Redis
db_operations_total{operation, result}
db_operation_duration_seconds{operation}
redis_operations_total{operation, result}
```

### 日志关键字段
```json
{
  "timestamp": "2026-09-02T10:30:45.123Z",
  "level": "info/warn/error",
  "request_id": "req_abc123",
  "session_id": "ses_xyz789",
  "tenant_id": "tenant_001",
  "user_id": "user_456",
  "api_key_id": "key_789",
  "model": "gpt-4",
  "provider": "openai",
  "provider_instance": "openai-001",
  "is_stream": true,
  "duration_ms": 1234,
  "status_code": 200,
  "input_tokens": 100,
  "output_tokens": 150,
  "error": "...",
  "error_type": "timeout/network/provider/...",
  "retry_count": 1,
  "ttfb_ms": 234  // 流式
}
```

---

## 异常场景优先级

### P0 - 立即告警
1. Panic rate > 0.1%
2. Auth service down
3. DB connection lost (持续>5分钟)
4. 所有Provider不可用
5. 资源门控拒绝率 > 20%

### P1 - 关注监控
1. Session更新失败率 > 5%
2. 审计日志写入失败率 > 10%
3. Provider错误率 > 10%
4. 流式中断恢复失败率 > 5%
5. 限流触发率 > 30%

### P2 - 趋势观察
1. 平均TTFB上升
2. Token计数偏差
3. 重试率上升
4. Graceful shutdown等待时间
5. 客户端断开率变化

---

## 改进建议

### 短期 (1-2周)
1. **增强心跳标识**: 在心跳chunk中添加明确的`type: "heartbeat"`字段
2. **客户端断开优化**: 更快速的检测和资源清理
3. **审计降级告警**: DB失败时的降级路径增加明确告警

### 中期 (1个月)
1. **per-tenant budget**: 根据租户历史负载动态调整资源配额
2. **流式恢复增强**: 支持客户端主动请求恢复点
3. **错误聚合改进**: 首字节后错误的更优雅处理

### 长期 (2-3个月)
1. **分布式追踪**: 集成OpenTelemetry完整链路追踪
2. **智能重试**: 基于错误类型和Provider历史表现的动态重试策略
3. **预测性限流**: 基于负载预测的主动限流

---

## 附录: 状态机

### Session状态转换
```
[created] → [processing] → [completed]
           ↓             ↘
      [interrupted]    [failed]
                        ↓
                   [partial] (流式)
```

### Provider实例状态
```
[healthy] ⇄ [degraded] → [unhealthy] → [removed]
   ↑                                       ↓
   └───────────[recovery check]────────────┘
```

---

**文档版本**: v1.0
**最后更新**: 2026-09-02
**维护者**: Gateway Team
