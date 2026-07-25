# LLM Gateway 数据格式与转换优化

## 问题背景

在生产环境中发现了以下问题：

1. **消息丢失问题**：有时第一次发送给模型时，请求数据丢失（日志中也无记录），第二次才正常
2. **工具调用丢失**：模型返回的响应中应该包含 tool_calls，但实际数据中丢失了

## 根因分析

通过深度代码审查和架构分析，识别出以下潜在问题：

### 1. 日志记录不足
- **问题**：IR 转换层（`internal/ir/` 和 `domains/transformation/`）缺少详细的结构化日志
- **影响**：无法追踪数据在转换过程中的变化，难以定位丢失点
- **位置**：
  - `domains/transformation/ir_transport.go` 仅有基本的错误日志
  - `internal/ir/parse_*.go` 和 `serialize_*.go` 完全没有日志

### 2. 异常处理缺失
- **问题**：转换错误时没有记录原始数据，无法复现问题
- **影响**：错误发生后无法回溯，只能等待下次复现
- **位置**：所有 `ir.ParseXXX()` 和 `ir.SerializeXXX()` 函数

### 3. 并发竞争问题（潜在）
- **问题**：Session 统计字段（`TotalPromptTokens`, `TotalCompletionTokens` 等）没有使用原子操作
- **影响**：并发请求可能导致计数丢失或错误
- **位置**：`domains/session/session.go:60-62`

### 4. 缺少语义分析
- **问题**：无法检测响应是否语义完整（例如：说要调用工具但实际没有 tool_calls）
- **影响**：工具调用丢失时没有告警机制

## 解决方案

### 1. 原始数据日志记录器 (RawDataLogger)

**文件**：`internal/logging/raw_data_logger.go`

**功能**：
- 记录转换前后的完整原始数据（客户端请求、上游请求、上游响应、客户端响应）
- 回转日志文件，单文件最大 200MB
- 保留最近 5 个日志文件
- JSON Lines 格式，每条日志包含：
  - `timestamp`: 时间戳
  - `request_id`: 请求ID（用于关联）
  - `direction`: 数据方向（client_request, upstream_request, upstream_response, client_response）
  - `protocol`: 协议类型（openai-chat, anthropic-messages 等）
  - `data_size`: 数据大小
  - `raw_data`: 原始数据（截断到 50KB）
  - `conversion_step`: 转换步骤（pre_parse, post_serialize 等）
  - `error`: 错误信息（如有）

**使用场景**：
- 调试消息丢失问题：对比 client_request 和 upstream_request，找出转换差异
- 调试工具调用丢失：对比 upstream_response 和 client_response，检查 tool_calls 字段
- 复现转换错误：保存了完整的输入数据

**配置**：
```bash
export LLM_GATEWAY_RAW_LOG_ENABLED=true
export LLM_GATEWAY_RAW_LOG_DIR=/var/log/llm-gateway/raw_data
export LLM_GATEWAY_RAW_LOG_MAX_SIZE=209715200  # 200MB
```

### 2. 语义分析器 (SemanticAnalyzer)

**文件**：`internal/ir/semantic_analyzer.go`

**功能**：
- 检测响应是否语义完整
- 识别以下异常模式：
  1. **工具调用意图短语**：响应包含"正在调用"、"let me use"等词汇，但没有 tool_calls
  2. **文本截断**：未闭合的引号、括号，或不完整的句子
  3. **finish_reason 不一致**：finish_reason 是 "stop" 但内容暗示未完成
  4. **空响应或极短响应**：内容 <10 字符且 finish_reason 不是 "length" 或 "content_filter"

**返回结果**：
- `IsIncomplete`: 是否不完整
- `Reason`: 不完整的原因
- `Confidence`: 置信度 (0.0-1.0)
- `SuspectedMissingTools`: 是否疑似工具调用丢失
- `Indicators`: 触发的指标列表

**对比原始日志**：
- `CompareWithRawLog()` 函数可对比原始上游响应和 IR 响应
- 如果原始数据有 tool_calls 但 IR 没有，判定为转换丢失

### 3. 异常报告器 (AnomalyReporter)

**文件**：`internal/logging/anomaly_reporter.go`

**功能**：
- 向外部端点报告格式转换异常
- 支持三种异常类型：
  1. `tool_calls_missing`: 工具调用丢失
  2. `conversion_error`: 转换错误
  3. `semantic_incomplete`: 语义不完整

**报告内容**：
```json
{
  "timestamp": "2026-07-26T10:30:00Z",
  "request_id": "req_123456",
  "anomaly_type": "tool_calls_missing",
  "source_protocol": "openai-chat",
  "target_protocol": "anthropic-messages",
  "conversion_step": "parse_or_serialize",
  "raw_input_sample": "...",
  "raw_output_sample": "...",
  "error_message": "",
  "analysis": {
    "suspected_cause": "IR conversion dropped tool_calls field",
    "missing_tool_calls": "{...}",
    "recommended_action": "Check IR ParseXXX/SerializeXXX functions",
    "upstream_has_tools": "true",
    "downstream_has_tools": "false"
  },
  "confidence": 0.9
}
```

**异步发送**：
- 批量发送（最多 10 条/批）
- 不阻塞主流程
- 队列最大 100 条

**配置**：
```bash
export LLM_GATEWAY_ANOMALY_REPORTER_ENABLED=true
export LLM_GATEWAY_ANOMALY_ENDPOINT=https://llmgo.kxpms.cn/format-anomalies
```

### 4. 增强的 IR 转换层

**文件**：`domains/transformation/ir_transport.go`

**改进**：

#### 4.1 请求转换 (Convert)
```go
// 原代码（无日志）
internalReq, err := parseRequest(tc.ClientProtocol, tc.BodyBytes)
if err != nil {
    return nil, fmt.Errorf("ir_transport: parse %s: %w", tc.ClientProtocol, err)
}

// 新代码（带详细日志和错误处理）
slog.DebugContext(ctx, "ir_transport: parsing request",
    "request_id", requestID,
    "from_protocol", tc.ClientProtocol,
    "body_size", len(tc.BodyBytes))

internalReq, err := parseRequest(tc.ClientProtocol, tc.BodyBytes)
if err != nil {
    // 1. 记录详细错误日志
    slog.ErrorContext(ctx, "ir_transport: parse request failed",
        "request_id", requestID,
        "protocol", tc.ClientProtocol,
        "body_size", len(tc.BodyBytes),
        "err", err)
    
    // 2. 记录原始数据到日志文件
    if t.rawLogger != nil {
        t.rawLogger.LogConversionError(requestID, tc.ClientProtocol, 
            "client_request", "parse", tc.BodyBytes, err)
    }
    
    // 3. 报告异常到外部端点
    if t.anomalyReporter != nil {
        t.anomalyReporter.ReportConversionError(ctx, requestID, 
            tc.ClientProtocol, tc.UpstreamProtocol, 
            "parse_request", tc.BodyBytes, err)
    }
    
    return nil, fmt.Errorf("ir_transport: parse %s: %w", tc.ClientProtocol, err)
}

// 4. 记录成功信息（包含关键字段）
slog.InfoContext(ctx, "ir_transport: parsed request successfully",
    "request_id", requestID,
    "protocol", tc.ClientProtocol,
    "message_count", len(internalReq.Messages),
    "tool_count", len(internalReq.Tools),
    "has_system", internalReq.System != nil,
    "stream", internalReq.Stream,
    "parse_duration_ms", parseElapsed.Milliseconds())
```

#### 4.2 响应转换 (ConvertResponse)
```go
// 新增语义分析
if t.semanticAnalyzer != nil && !hasToolCalls {
    analysis := t.semanticAnalyzer.AnalyzeResponse(internalResp)
    if analysis.IsIncomplete {
        slog.WarnContext(ctx, "ir_transport: semantic analysis detected incomplete response",
            "request_id", requestID,
            "reason", analysis.Reason,
            "confidence", analysis.Confidence,
            "suspected_missing_tools", analysis.SuspectedMissingTools)
        
        // 对比原始日志
        if analysis.SuspectedMissingTools {
            hasLoss, missingData := ir.CompareWithRawLog(upstreamBody, internalResp)
            if hasLoss {
                slog.ErrorContext(ctx, "ir_transport: TOOL_CALLS_LOST during conversion",
                    "request_id", requestID,
                    "missing_tool_calls", missingData)
                
                // 报告严重异常
                t.anomalyReporter.ReportToolCallsMissing(...)
            }
        }
    }
}
```

### 5. 初始化代码

**文件**：`cmd/gateway/logging_init.go`

**使用方式**：

在 `cmd/gateway/main.go` 中：

```go
// 原代码
irTransport := transformation.NewIRTransport()

// 新代码
irTransport := initEnhancedIRTransport()
```

`initEnhancedIRTransport()` 会自动初始化：
1. RawDataLogger
2. AnomalyReporter  
3. SemanticAnalyzer
4. 带完整功能的 IRTransport

## 部署指南

### 1. 环境变量配置

```bash
# 原始数据日志
export LLM_GATEWAY_RAW_LOG_ENABLED=true
export LLM_GATEWAY_RAW_LOG_DIR=/var/log/llm-gateway/raw_data
export LLM_GATEWAY_RAW_LOG_MAX_SIZE=209715200  # 200MB

# 异常报告
export LLM_GATEWAY_ANOMALY_REPORTER_ENABLED=true
export LLM_GATEWAY_ANOMALY_ENDPOINT=https://llmgo.kxpms.cn/format-anomalies

# 语义分析
export LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED=true
```

### 2. 日志目录权限

```bash
sudo mkdir -p /var/log/llm-gateway/raw_data
sudo chown llm-gateway:llm-gateway /var/log/llm-gateway/raw_data
sudo chmod 755 /var/log/llm-gateway/raw_data
```

### 3. 日志轮转配置

创建 `/etc/logrotate.d/llm-gateway-raw`:

```
/var/log/llm-gateway/raw_data/*.jsonl {
    daily
    rotate 7
    compress
    delaycompress
    notifempty
    missingok
    create 0644 llm-gateway llm-gateway
}
```

### 4. 监控告警

建议设置以下告警：

1. **工具调用丢失告警**：
   - 条件：`anomaly_type == "tool_calls_missing"`
   - 级别：P1（严重）
   - 通知：立即

2. **转换错误率告警**：
   - 条件：最近 5 分钟转换错误 > 10 次
   - 级别：P2（重要）
   - 通知：15 分钟内

3. **日志文件大小告警**：
   - 条件：原始日志目录 > 5GB
   - 级别：P3（提示）
   - 通知：每日汇总

## 故障排查指南

### 场景1：消息丢失（第一次无数据，第二次正常）

**排查步骤**：

1. 查看原始日志文件：
```bash
cd /var/log/llm-gateway/raw_data
tail -f raw_data_*.jsonl | grep "request_id.*<问题请求ID>"
```

2. 检查是否有 `client_request` 记录：
   - 如果有：说明请求到达了网关
   - 如果没有：问题在网关之前（负载均衡、反向代理等）

3. 检查 `upstream_request` 记录：
   - 对比 `client_request` 和 `upstream_request` 的 `data_size`
   - 如果 `upstream_request` 的 `data_size` 为 0 或明显偏小，说明转换过程丢失数据

4. 查看结构化日志：
```bash
journalctl -u llm-gateway | grep "request_id.*<问题请求ID>" | grep "parse request"
```

5. 检查是否有异常报告：
```bash
curl https://llmgo.kxpms.cn/format-anomalies?request_id=<问题请求ID>
```

### 场景2：工具调用丢失

**排查步骤**：

1. 查看原始上游响应：
```bash
cat /var/log/llm-gateway/raw_data/raw_data_*.jsonl | \
  jq -r 'select(.request_id == "<问题请求ID>" and .direction == "upstream_response") | .raw_data'
```

2. 检查原始响应是否包含 tool_calls：
```bash
# OpenAI 格式
echo "$raw_data" | jq '.choices[0].message.tool_calls'

# Anthropic 格式
echo "$raw_data" | jq '.content[] | select(.type == "tool_use")'
```

3. 查看客户端响应：
```bash
cat /var/log/llm-gateway/raw_data/raw_data_*.jsonl | \
  jq -r 'select(.request_id == "<问题请求ID>" and .direction == "client_response") | .raw_data'
```

4. 对比差异，如果原始响应有 tool_calls 但客户端响应没有：
   - 查看语义分析日志：
```bash
journalctl -u llm-gateway | grep "semantic analysis detected incomplete response" | grep "<问题请求ID>"
```

5. 查看异常报告：
```bash
curl https://llmgo.kxpms.cn/format-anomalies?anomaly_type=tool_calls_missing&request_id=<问题请求ID>
```

### 场景3：转换错误

**排查步骤**：

1. 查看错误日志：
```bash
journalctl -u llm-gateway | grep "parse.*failed\|serialize.*failed" | grep "<问题请求ID>"
```

2. 查看原始数据：
```bash
cat /var/log/llm-gateway/raw_data/raw_data_*.jsonl | \
  jq 'select(.request_id == "<问题请求ID>" and .error != null)'
```

3. 分析错误类型：
   - JSON 解析错误：检查原始数据格式
   - 字段缺失错误：检查协议兼容性
   - 类型转换错误：检查字段类型映射

4. 复现问题：
```bash
# 提取原始输入
raw_input=$(cat /var/log/llm-gateway/raw_data/raw_data_*.jsonl | \
  jq -r 'select(.request_id == "<问题请求ID>" and .conversion_step == "pre_parse") | .raw_data')

# 本地测试
echo "$raw_input" | go run ./cmd/test-ir-parse/main.go --protocol openai-chat
```

## 性能影响评估

### 1. 原始日志记录

**CPU 影响**：
- JSON 序列化：~0.1ms/请求
- 文件写入（带 Sync）：~0.5ms/请求
- **总计**：~0.6ms/请求（<1% CPU on 4-core）

**磁盘 I/O**：
- 每请求 ~5KB（请求） + ~10KB（响应）= 15KB
- 1000 req/s → 15MB/s 写入
- 200MB 日志文件 → ~13 秒轮转一次（高负载）

**建议**：
- 生产环境建议使用 SSD
- 可配置为异步写入（牺牲数据完整性换取性能）

### 2. 语义分析

**CPU 影响**：
- 文本分析：~0.2ms/响应
- 仅在没有 tool_calls 时执行
- **影响**：<0.5% CPU

### 3. 异常报告

**网络影响**：
- 异步批量发送，不阻塞主流程
- 队列满时丢弃旧数据
- **影响**：几乎无

## 测试验证

### 单元测试

```bash
# 测试原始日志记录器
go test ./internal/logging -v -run TestRawDataLogger

# 测试语义分析器
go test ./internal/ir -v -run TestSemanticAnalyzer

# 测试异常报告器
go test ./internal/logging -v -run TestAnomalyReporter
```

### 集成测试

```bash
# 启动测试环境
export LLM_GATEWAY_RAW_LOG_ENABLED=true
export LLM_GATEWAY_RAW_LOG_DIR=./test_logs
export LLM_GATEWAY_ANOMALY_REPORTER_ENABLED=true
export LLM_GATEWAY_ANOMALY_ENDPOINT=http://localhost:8080/test-anomalies

go run ./cmd/gateway/main.go

# 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 检查日志
ls -lh ./test_logs/
cat ./test_logs/raw_data_*.jsonl | jq .
```

## 后续优化建议

### 1. 异步日志写入

当前实现是同步写入（带 Sync），可改为：
```go
type AsyncRawDataLogger struct {
    queue chan RawDataEntry
    // ...
}
```

### 2. 采样记录

高负载下可启用采样（例如：每 10 个请求记录 1 个）：
```go
if rand.Intn(10) == 0 {
    rawLogger.LogClientRequest(...)
}
```

### 3. 结构化存储

可将原始日志写入时序数据库（ClickHouse、InfluxDB）以支持高效查询。

### 4. 实时监控面板

基于异常报告数据构建实时监控面板，展示：
- 工具调用丢失趋势
- 转换错误率
- 语义不完整检测

## 附录

### A. 日志格式示例

**client_request (客户端请求)**:
```json
{
  "timestamp": "2026-07-26T10:30:00.123Z",
  "request_id": "req_abc123",
  "direction": "client_request",
  "protocol": "openai-chat",
  "data_size": 456,
  "raw_data": "{\"model\":\"gpt-4\",\"messages\":[...]}",
  "headers": {
    "Content-Type": "application/json",
    "X-Request-ID": "req_abc123"
  },
  "conversion_step": "pre_parse"
}
```

**upstream_response (上游响应)**:
```json
{
  "timestamp": "2026-07-26T10:30:01.234Z",
  "request_id": "req_abc123",
  "direction": "upstream_response",
  "protocol": "anthropic-messages",
  "data_size": 1234,
  "raw_data": "{\"id\":\"msg_xyz\",\"content\":[{\"type\":\"tool_use\",...}]}",
  "conversion_step": "pre_parse"
}
```

### B. 环境变量完整列表

| 变量名 | 默认值 | 说明 |
|--------|--------|------|
| `LLM_GATEWAY_RAW_LOG_ENABLED` | `true` | 是否启用原始日志 |
| `LLM_GATEWAY_RAW_LOG_DIR` | `./logs/raw_data` | 日志目录 |
| `LLM_GATEWAY_RAW_LOG_MAX_SIZE` | `209715200` | 单文件最大字节数 |
| `LLM_GATEWAY_ANOMALY_REPORTER_ENABLED` | `true` | 是否启用异常报告 |
| `LLM_GATEWAY_ANOMALY_ENDPOINT` | `https://llmgo.kxpms.cn/format-anomalies` | 异常端点 URL |
| `LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED` | `true` | 是否启用语义分析 |

### C. 相关文件清单

**新增文件**：
- `internal/logging/raw_data_logger.go` - 原始数据日志记录器
- `internal/logging/anomaly_reporter.go` - 异常报告器
- `internal/ir/semantic_analyzer.go` - 语义分析器
- `cmd/gateway/logging_init.go` - 初始化代码

**修改文件**：
- `domains/transformation/ir_transport.go` - 增强的 IR 转换层

**总代码行数**：~1200 行（不含注释）
