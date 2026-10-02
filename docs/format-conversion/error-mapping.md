# 错误码跨协议映射规则

## 概述

不同协议的错误码体系不同，网关需要把厂商错误统一为 IR 错误（`errorsx.UpstreamError`），然后再按客户端方言序列化为客户端期望的错误格式。

## errorsx.Kind 统一分类

```go
const (
    KindUnknown           Kind = ""
    KindClient            Kind = "client_error"           // 4xx，客户端错误
    KindAuth              Kind = "auth_error"             // 401/403
    KindPermission        Kind = "permission_error"       // 403
    KindNotFound          Kind = "not_found_error"        // 404
    KindRateLimit         Kind = "rate_limit_error"       // 429
    KindContextLength     Kind = "context_length_error"   // 上下文超限
    KindContentFilter     Kind = "content_filter_error"   // 内容过滤
    KindNetwork           Kind = "network_error"          // 网络错误
    KindUpstream          Kind = "upstream_error"         // 5xx，上游错误
    KindOverloaded        Kind = "overloaded_error"       // 529/503，服务过载
    KindTimeout           Kind = "timeout_error"          // 超时
    KindBilling           Kind = "billing_error"          // 余额不足
)
```

## 各厂商错误码 → errorsx.Kind 映射

### OpenAI

| OpenAI `type` | HTTP | errorsx.Kind |
|---------------|------|--------------|
| `invalid_request_error` | 400 | KindClient |
| `authentication_error` | 401 | KindAuth |
| `permission_error` | 403 | KindPermission |
| `not_found_error` | 404 | KindNotFound |
| `rate_limit_error` | 429 | KindRateLimit |
| `api_error` | 500/503 | KindUpstream |
| `overloaded_error` | 529 | KindOverloaded |

### Anthropic

| Anthropic `type` | HTTP | errorsx.Kind |
|------------------|------|--------------|
| `invalid_request_error` | 400 | KindClient |
| `authentication_error` | 401 | KindAuth |
| `permission_error` | 403 | KindPermission |
| `not_found_error` | 404 | KindNotFound |
| `rate_limit_error` | 429 | KindRateLimit |
| `api_error` | 500/529 | KindUpstream |
| `overloaded_error` | 529 | KindOverloaded |

### Gemini

| Gemini `status` | HTTP | errorsx.Kind |
|-----------------|------|--------------|
| `INVALID_ARGUMENT` | 400 | KindClient |
| `UNAUTHENTICATED` | 401 | KindAuth |
| `PERMISSION_DENIED` | 403 | KindPermission |
| `NOT_FOUND` | 404 | KindNotFound |
| `RESOURCE_EXHAUSTED` | 429 | KindRateLimit |
| `INTERNAL` | 500 | KindUpstream |
| `UNAVAILABLE` | 503 | KindOverloaded |
| `DEADLINE_EXCEEDED` | 504 | KindTimeout |

### DeepSeek

| DeepSeek `code` | errorsx.Kind |
|-----------------|--------------|
| `invalid_request_error` | KindClient |
| `authentication_error` | KindAuth |
| `rate_limit_error` | KindRateLimit |
| `insufficient_balance` | KindBilling |
| `server_error` | KindUpstream |

### GLM（智谱）

| GLM code | 错误描述 | errorsx.Kind |
|----------|----------|--------------|
| 1001 | 认证失败 | KindAuth |
| 1002 | 余额不足 | KindBilling |
| 1214 | 上下文过长 | KindContextLength |
| 1300 | 系统错误 | KindUpstream |
| `network_error` (finish_reason) | 网络异常 | KindNetwork |
| `sensitive` (finish_reason) | 内容敏感 | KindContentFilter |
| `model_context_window_exceeded` (finish_reason) | 上下文超限 | KindContextLength |

### MiniMax

| MiniMax `base_resp.status_code` | errorsx.Kind |
|---------------------------------|--------------|
| 0 | 成功（不放错误） |
| 1002 | KindRateLimit |
| 1003 | KindClient |
| 1004 | KindRateLimit |
| 1008 | KindBilling |
| 1027 | KindContextLength |
| 1039 | KindContextLength |
| 2056 | KindOverloaded |

### Mistral

| Mistral `code` | errorsx.Kind |
|----------------|--------------|
| `invalid_request_error` | KindClient |
| `authentication_error` | KindAuth |
| `rate_limit_exceeded` | KindRateLimit |
| `insufficient_balance` | KindBilling |
| `server_error` | KindUpstream |

## 错误响应序列化

### 客户端期望 OpenAI 格式

```json
{
  "error": {
    "message": "Rate limit exceeded",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded",
    "param": null
  }
}
```

### 客户端期望 Anthropic 格式

```json
{
  "type": "error",
  "error": {
    "type": "rate_limit_error",
    "message": "Rate limit exceeded"
  }
}
```

### 客户端期望 Gemini 格式

```json
{
  "error": {
    "code": 429,
    "message": "Rate limit exceeded",
    "status": "RESOURCE_EXHAUSTED"
  }
}
```

## 错误传播流程

```
Upstream HTTP error
    ↓
errorsx.UpstreamError{Kind, Message, StatusCode}
    ↓
HTTP status code 设置（基于 Kind）
    ↓
SerializeXxxError → client native format
    ↓
HTTP response
```

### HTTP 状态码映射

⚠️ **2026-09-21 审计说明**：实际 `HTTPStatusForKind` 实现是粗粒度的，仅区分：

| 类别 | ErrorKind | HTTP Status |
|------|-----------|-------------|
| 速率/配额 | KindRateLimit, KindQuota, KindQuotaPeriodic, KindQuotaBalance, KindQuotaPermanent | 429 |
| 容量耗尽 | KindConcurrent, KindUpstreamOverloaded | 503 |
| 超时 | KindTimeout, KindStreamTimeout | 504 |
| 网络/上游 | KindNetwork, KindUpstreamDown | 502 |
| 默认（其他全部） | KindAuth, KindAuthRevoked, KindModelNotFound, KindModelDeprecated, KindContentFilter, KindContextLength, KindCanceled, KindClientBug, KindToolCallIdMismatch, KindUnsupportedFeature, KindEmptyResponse, KindConversion, KindUpstreamContextLoss, KindNoAvailableChannel, KindCircuitOpen, KindFpSlotSaturated, KindTransient | **502** |

**设计意图**：网关作为中继代理，把"上游问题"统一呈现为 `502 Bad Gateway`。客户端只需要知道"网关上游出错"，由日志/告警/重试策略决定进一步动作。这样避免暴露上游内部状态码分类。

**理想精细化映射（未来 P3 优化方向）**：

| errorsx.Kind | 理想 HTTP Status | 备注 |
|--------------|------------------|------|
| KindAuth | 401 | 客户端可重新登录 |
| KindAuthRevoked | 401 | 同上 |
| KindModelNotFound | 404 | 客户端可切换模型 |
| KindModelDeprecated | 410 | 客户端可永久切换 |
| KindContentFilter | 400 | 客户端可改写 prompt |
| KindContextLength | 400 | 客户端可缩减上下文 |
| KindClientBug | 400 | 客户端代码 bug |
| KindToolCallIdMismatch | 400 | 客户端代码 bug |
| KindUnsupportedFeature | 501 | 上游不支持该特性 |
| KindCanceled | 499 | nginx 风格，客户端主动断连 |
| KindConversion | 500 | 网关自身 bug |
| KindNoAvailableChannel | 503 | 上游无通道可用 |
| KindCircuitOpen | 503 | 网关熔断 |
| KindFpSlotSaturated | 503 | 凭据槽饱和 |

⚠️ 注意：`errorsx/error_mapping_doctest_test.go` 已经把这些差异作为"漂移"显式记录，未来谁真要细化映射必须同时改测试和文档。

## 流式错误

流式响应中的错误需要特殊处理：

1. **立即中断流**：不能再下发 chunk。
2. **下发错误事件**：
   - OpenAI SSE: `data: {"error": {...}}` 后立即 `data: [DONE]`
   - Anthropic SSE: `event: error` + `data: {"type":"error","error":{...}}`
   - Gemini SSE: `data: {"error": {...}}`
3. **关闭 HTTP 连接**：客户端不再等待后续 chunk。
