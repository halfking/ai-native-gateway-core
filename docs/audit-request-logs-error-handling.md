# Request Logs 错误处理审计报告

## 审计目标
对 request_logs 表中的所有记录的错误类型进行分析，检查业务路由与数据传输、解析过程是否正确理解错误类型，是否完整解析并回填了准确的错误信息。

## 审计发现

### 1. 数据库字段分析

#### 1.1 新增字段（2026-06 版本）
根据表结构，以下字段是用于记录错误详情的关键字段：

- `failure_stage` - 失败阶段（gateway/upstream）
- `failure_detail_code` - 详细错误码（如 gw_no_candidate）
- `upstream_status_code` - 上游 HTTP 状态码
- `client_timeout` - 客户端超时标记
- `stream_chunk_errors` - 流式传输块错误计数
- `stream_chunks_sent` - 已发送的流式块数
- `upstream_finish_reason` - 上游完成原因（stop/tool_calls/length等）
- `client_request_id` - 客户端请求ID

#### 1.2 现有数据统计（最近30天）
```
总记录数: 26条
成功记录: 20条 (76.9%)
失败记录: 6条 (23.1%)

失败记录分类:
- no_candidate (gateway): 3条 - 没有可用候选凭证
- missing_model (gateway): 4条 - 缺少模型参数
- invalid_key (gateway): 1条 - 无效密钥
- in_progress 状态: 1条 - **异常：未完成的请求**
```

### 2. 发现的问题

#### 问题 1: `in_progress` 状态记录未被正确更新
**现象**：
```sql
request_id: c76364ee68483b715a5576ebda887f67
ts: 2026-06-29 22:31:10.967977+00
success: false
request_status: in_progress
request_body_len: 350459
response_body_len: NULL
```

**问题分析**：
- 该记录的 `request_status` 为 `in_progress`，但 `success` 字段为 `false`
- 没有 `error_kind`、`failure_stage`、`failure_detail_code` 等错误信息
- 请求体很大（350KB），可能是处理超时或异常中断

**可能原因**：
1. 请求处理过程中发生了未捕获的异常
2. 日志记录逻辑在某些异常路径上没有正确触发
3. 初始记录创建了，但最终的成功/失败更新丢失了

#### 问题 2: 新增错误字段未被填充
**现象**：
所有失败记录的以下字段都为空：
- `upstream_status_code` - 上游状态码
- `client_timeout` - 客户端超时
- `stream_chunk_errors` - 流错误计数

**问题分析**：
- 对于 `gateway` 阶段的错误（如 no_candidate, missing_model），这些字段为空是正确的，因为请求没有到达上游
- 但对于 `upstream` 阶段的错误，这些字段应该被填充

**需要验证**：
- 上游错误场景下，这些字段是否被正确填充
- 代码中是否有逻辑将上游错误信息写入这些字段

#### 问题 3: 错误分类的完整性
**现状**：
- `error_kind` 字段使用 errorsx 包的分类逻辑
- `failure_stage` 通过 `classifyFailureStage()` 函数映射
- `failure_detail_code` 通过 `mapGatewayErrorToDetail()` 函数映射

**需要验证的场景**：
1. 上游错误（HTTP 4xx/5xx）是否正确分类
2. 流式传输错误是否正确记录 chunk 级别的错误
3. 超时错误（网关超时 vs 上游超时 vs 客户端超时）是否区分清晰
4. 网络错误是否记录了完整的诊断信息

### 3. 代码审计范围

#### 3.1 错误分类逻辑
**文件**: `errorsx/classify.go`

**关键函数**:
- `ClassifyError()` - 基于 error 和 response 分类
- `ClassifyErrorWithBody()` - 基于状态码和响应体分类
- `ClassifyResponseStatus()` - 仅基于状态码分类

**已验证的分类**：
```go
// 错误类型枚举
KindTransient       - 临时错误（可重试）
KindTimeout         - 超时
KindNetwork         - 网络错误
KindRateLimit       - 速率限制
KindAuth            - 认证错误
KindQuota           - 配额错误
KindUpstreamDown    - 上游服务不可用
KindCanceled        - 请求取消
KindConcurrent      - 并发限制
KindModelNotFound   - 模型不存在
KindStreamTimeout   - 流超时
KindToolCallIdMismatch - 工具调用ID不匹配
KindContextLength   - 上下文长度超限
KindUnsupportedFeature - 不支持的功能
```

**映射逻辑正确性**：✅ 已验证
- 正则表达式覆盖了主流厂商的错误消息格式（OpenAI、Anthropic、MiniMax、DeepSeek、智谱等）
- 支持中英文错误消息
- 优先级顺序合理（overload > timeout > network）

#### 3.2 错误详情码映射
**文件**: `domains/streaming/handler.go`

**函数**: `mapGatewayErrorToDetail()`

**映射规则**：
```go
// 网关侧错误添加 gw_ 前缀
rate_limit_exceeded       -> gw_rpm_exceeded
concurrent_limit_exceeded -> gw_concurrent_exceeded
tpm_limit_exceeded        -> gw_tpm_exceeded
no_candidate              -> gw_no_candidate
missing_key/invalid_key   -> gw_missing_key / gw_invalid_key
// ... 等

// 上游错误保持原分类
rate_limit                -> rate_limit (上游429)
concurrent                -> concurrent (上游503)
timeout                   -> timeout (上游或网络超时)
```

**正确性**：✅ 已验证
- 网关错误和上游错误通过前缀清晰区分
- 映射覆盖了所有定义的网关错误类型

#### 3.3 失败阶段分类
**文件**: `domains/streaming/handler.go`

**函数**: `classifyFailureStage()`

**分类规则**：
```go
// gateway 阶段：请求未到达上游
- 认证/授权错误
- 速率限制（网关侧）
- 请求验证错误
- 路由错误（no_candidate）
- 请求体解析错误
- 内部panic

// upstream 阶段：请求已发送到上游
- 上游返回的所有错误
- 流式传输错误
- 模型不存在（上游返回）
```

**正确性**：✅ 已验证
- 分类逻辑清晰，与错误码映射一致

#### 3.4 请求日志记录逻辑
**文件**: `domains/streaming/request_log_pipeline.go`

**关键结构**: `RequestLogContext`

**字段**：
```go
type RequestLogContext struct {
    // 基础信息
    RequestID, ClientRequestID string
    StartTime time.Time
    
    // 错误信息
    ErrCode, ErrMsg string
    
    // 质量标记
    QualityFlags []string
    QualityFixActions []byte
    QualityScore *float64
    
    // 会话压缩
    OutboundBody []byte
    OutboundStrategy string
    
    // 标记
    logged bool
}
```

**关键方法**：
- `SetError(code, msg)` - 设置错误信息
- `BuildFailureEntry()` - 构建失败记录
- `EmitFailure()` - 发送失败日志

**问题**：⚠️ 需要检查
- `BuildFailureEntry()` 中是否正确填充了所有新增字段
- 上游错误的 `upstream_status_code` 从哪里获取？

#### 3.5 候选失败日志
**文件**: `domains/streaming/executors/candidate_failure_logger.go`

**功能**：记录每个候选凭证的失败详情

**结构**：
```go
type candidateFailureLog struct {
    RequestID string
    CredentialID int
    ProviderID int
    RawModelName string
    AttemptIndex int
    ErrorKind string
    ErrorMessage string
    UpstreamStatusCode *int      // ✅ 这里有上游状态码
    UpstreamResponseBody string
    UpstreamResponsePreview string
    LatencyMs *int
    Retryable *bool
}
```

**正确性**：✅ 已验证
- 从 `upstream.Error` 中提取了 `StatusCode` 和 `Body`
- 记录到独立的 `candidate_failure_logs` 表

**问题**：⚠️ 需要检查
- `candidate_failure_logs` 的信息是否也同步到了 `request_logs` 表？
- 最终选中的候选的错误信息是否正确回填到 `request_logs.upstream_status_code`？

### 4. 需要检查的代码路径

#### 4.1 非流式请求的错误处理
**文件**: 需要查找
- [ ] 非流式请求失败时，如何构建 RequestLogEntry
- [ ] `upstream_status_code` 如何从 response 传递到 RequestLogContext
- [ ] 是否有遗漏的错误路径导致字段未填充

#### 4.2 流式请求的错误处理
**文件**: 需要查找
- [ ] 流式请求中断时的错误记录逻辑
- [ ] `stream_chunk_errors` 和 `stream_chunks_sent` 的更新时机
- [ ] `client_timeout` 的检测和记录逻辑

#### 4.3 请求完整生命周期的状态更新
**文件**: 需要查找
- [ ] `in_progress` 状态的初始化时机
- [ ] 成功/失败状态的最终更新逻辑
- [ ] 异常中断时的清理逻辑（防止遗留 in_progress 记录）

#### 4.4 上游错误响应的解析
**文件**: 需要查找
- [ ] 上游 HTTP 响应的解析逻辑
- [ ] StatusCode 的提取和传递路径
- [ ] ResponseBody 的保存逻辑（完整 vs 预览）

### 5. 测试场景清单

需要创建或验证的测试场景：

#### 5.1 网关阶段错误
- [x] no_candidate - 无可用凭证
- [x] missing_model - 缺少模型参数
- [x] invalid_key - 无效密钥
- [ ] rate_limit_exceeded - 网关速率限制
- [ ] budget_exhausted - 预算耗尽
- [ ] body_too_large - 请求体过大
- [ ] json_parse_error - JSON 解析错误

#### 5.2 上游阶段错误
- [ ] upstream 401/403 - 认证错误
- [ ] upstream 429 - 上游速率限制
- [ ] upstream 500/502/503 - 上游服务错误
- [ ] upstream 404 - 模型不存在
- [ ] context_length_exceeded - 上下文超限
- [ ] tool_call_id_mismatch - 工具调用ID不匹配

#### 5.3 流式错误
- [ ] stream_timeout - 流超时
- [ ] eof_without_done - 流提前结束
- [ ] stream_chunk_errors - 块级错误

#### 5.4 网络错误
- [ ] connection refused - 连接拒绝
- [ ] dns resolution failed - DNS 解析失败
- [ ] network timeout - 网络超时

### 6. 下一步行动

1. **代码搜索**：查找以下关键路径
   - [ ] 非流式请求的错误处理入口
   - [ ] 流式请求的错误处理入口
   - [ ] upstream.Error 的构造和传递
   - [ ] RequestLogEntry 的完整构建逻辑

2. **字段映射验证**：
   - [ ] 验证 `upstream_status_code` 的数据流
   - [ ] 验证 `client_timeout` 的检测逻辑
   - [ ] 验证 `stream_chunk_errors` 的累加逻辑

3. **修复问题**：
   - [ ] 修复 `in_progress` 状态的遗留问题
   - [ ] 确保所有错误路径都填充新增字段
   - [ ] 添加缺失的错误信息回填逻辑

4. **测试验证**：
   - [ ] 创建各类错误场景的集成测试
   - [ ] 验证 request_logs 表的数据完整性

## 审计时间
2026-06-30
