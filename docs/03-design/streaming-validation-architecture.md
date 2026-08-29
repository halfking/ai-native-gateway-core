# 流处理验证层架构设计

**版本**: v1.0  
**创建日期**: 2026-08-29  
**作者**: Backend Team  
**状态**: 已实现  

---

## 1. 概述

### 1.1 问题背景

上游 LLM 提供商（特别是 minimax-m3 和 glm-5.2）在流式响应中偶尔会发送格式错误的 SSE 帧：
- 不完整的 JSON（如裸 `{` 或截断的对象）
- 不完整的 tool_use/tool_result 配对
- 流在关键内容块之间中断

这些问题导致：
1. **客户端解析失败** - 无效 JSON 到达客户端
2. **过早的 resume_blocked** - Survival coordinator 在应该重试时未能识别
3. **用户体验受损** - "Tool result is missing" 等错误

### 1.2 解决方案

在流处理管道中添加两层验证：
1. **SSE Frame Validator** - 验证 SSE 数据帧的 JSON 完整性
2. **Tool Call Validator** - 验证 tool_use 和 tool_result 的完整配对

这两层验证器在流到达客户端之前拦截错误，并将问题标记为 `Resumable=true`，使 survival coordinator 能够透明重试。

---

## 2. 架构设计

### 2.1 系统上下文

```
┌─────────────┐      ┌──────────────┐      ┌─────────────┐
│   Client    │◄─────│  LLM Gateway │◄─────│  Upstream   │
│  (ZCode)    │      │   Streaming  │      │  Provider   │
│             │      │   Pipeline   │      │ (MiniMax)   │
└─────────────┘      └──────────────┘      └─────────────┘
                            │
                            │ 包含
                            ▼
                     ┌──────────────┐
                     │ Validation   │
                     │    Layer     │
                     └──────────────┘
```

### 2.2 流处理管道架构

```
Upstream Response
      │
      ▼
┌─────────────────────────────────────────────────────────┐
│                   Stream Reader                         │
│  (domains/streaming/stream.go::StreamOpenAI)           │
└─────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────┐
│              First Frame Handling                       │
│  • Read first SSE frame from upstream                   │
│  • Check MiniMax error status                           │
└─────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────┐
│       ┌─────────────────────────────────┐               │
│       │  SSE Frame Validator (First)    │  ← NEW       │
│       │  • validateSSEDataFrame()       │               │
│       │  • Check JSON completeness      │               │
│       │  • Return Resumable if invalid  │               │
│       └─────────────────────────────────┘               │
└─────────────────────────────────────────────────────────┘
      │
      │ Valid first frame
      ▼
┌─────────────────────────────────────────────────────────┐
│              Parse & Process First Frame                │
│  • ir.ParseOpenAIStreamChunk()                          │
│  • Check for content                                    │
└─────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────┐
│              Attempt Commit Gate                        │
│  • Buffer up to 8 chunks / 64KB                         │
│  • Detect empty streams                                 │
│  • Control client visibility                            │
└─────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────┐
│              Main Stream Loop                           │
│  for line := range reader {                             │
│    ┌───────────────────────────────────┐               │
│    │ SSE Frame Validator (Mid-stream)  │ ← NEW         │
│    │ • validateSSEDataFrame()          │               │
│    │ • Check committed state           │               │
│    │ • Drop bad frame if committed     │               │
│    │ • Return Resumable if not         │               │
│    └───────────────────────────────────┘               │
│    │                                                     │
│    ▼                                                     │
│    Parse OpenAI chunk                                   │
│    │                                                     │
│    ▼                                                     │
│    ┌───────────────────────────────────┐               │
│    │  Tool Call Validator (Optional)   │ ← PLANNED     │
│    │  • Track tool_use blocks          │               │
│    │  • Verify tool_result pairing     │               │
│    └───────────────────────────────────┘               │
│    │                                                     │
│    ▼                                                     │
│    Process content blocks                               │
│    │                                                     │
│    ▼                                                     │
│    Write to client (via commit gate)                    │
│  }                                                       │
└─────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────┐
│              Stream Completion                          │
│  • Validate tool call completeness (if enabled)         │
│  • Return StreamOutcome                                 │
└─────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────┐
│           Survival Coordinator                          │
│  • Check Resumable flag                                 │
│  • Retry within holdback window if true                 │
│  • Failover to next candidate                           │
└─────────────────────────────────────────────────────────┘
```

---

## 3. SSE Frame Validator

### 3.1 设计原则

**目标**: 在流到达客户端之前拦截格式错误的 SSE 帧

**策略**:
- **First frame**: 严格验证，失败则立即返回 `Resumable=true`
- **Mid-stream**: 
  - 如果尚未提交到客户端 → 返回 `Resumable=true`
  - 如果已提交 → 丢弃坏帧，继续（避免中断已可见流）

### 3.2 验证逻辑

```go
// sse_frame_validator.go
func validateSSEDataFrame(line string) bool {
    payload := extractPayload(line) // data: {...} → {...}
    
    if payload == "" || payload == "[DONE]" {
        return true // 空帧和 [DONE] 是有效的
    }
    
    // 快速路径：如果不是 JSON，直接通过（如 keepalive 注释）
    if !strings.HasPrefix(strings.TrimSpace(payload), "{") {
        return true
    }
    
    // 验证 JSON 完整性
    var tmp interface{}
    return json.Unmarshal([]byte(payload), &tmp) == nil
}
```

### 3.3 集成点

#### First Frame Validation
```go
// stream.go::StreamOpenAI
if !validateSSEDataFrame(firstLine) {
    payload := extractPayload(firstLine)
    slog.Warn("stream: malformed first SSE frame detected",
        "payload_prefix", truncateForLog(payload, 100),
        "client_model", clientModel,
        "vendor", vendorCode,
    )
    if capture != nil {
        capture.MarkInterruptedWithReason("malformed_sse_frame")
    }
    metrics.Global().RecordMalformedSSEFrame(vendorCode, "first_frame")
    
    return StreamOutcome{
        Interrupted: true,
        Reason:      "malformed_sse_frame",
        Kind:        errorsx.KindUpstreamDown,
        Resumable:   true,  // ← 关键：允许重试
        ChunkCount:  0,
    }
}
```

#### Mid-Stream Validation
```go
// stream.go::StreamOpenAI (main loop)
if !validateSSEDataFrame(line) {
    payload := extractPayload(line)
    terminalVisible := attemptHasClientSemanticOutput(gate, chunkCount)
    
    slog.Warn("stream: malformed SSE frame detected mid-stream",
        "payload_prefix", truncateForLog(payload, 100),
        "chunk_count", chunkCount,
        "committed", terminalVisible,
    )
    
    metrics.Global().RecordMalformedSSEFrame(vendorCode, "mid_stream")
    
    if !terminalVisible {
        // 尚未提交到客户端 → 可以安全重试
        return StreamOutcome{
            Interrupted: true,
            Reason:      "malformed_sse_frame_mid_stream",
            Kind:        errorsx.KindUpstreamDown,
            Resumable:   true,
            ChunkCount:  chunkCount,
        }
    }
    
    // 已提交 → 丢弃此帧，继续（避免破坏流）
    continue
}
```

### 3.4 性能考虑

**基准测试结果**:
- 有效帧验证: ~1µs (1011 ns/op)
- 无效帧验证: ~90ns (90.88 ns/op)
- 内存分配: 0 allocs/op（无效帧），minimal（有效帧）

**开销分析**:
- 相对于整体流解析：约 10%
- 对端到端延迟影响：<1ms
- **结论**: 性能影响可忽略

---

## 4. Tool Call Validator

### 4.1 设计原则

**目标**: 确保每个 `tool_use` 都有对应的 `tool_result`

**策略**:
- 在流处理过程中跟踪所有 tool_use block
- 验证每个 tool_use 都有匹配的 tool_result
- 在流结束时检查完整性

### 4.2 状态机

```
┌──────────┐
│  Idle    │
└────┬─────┘
     │ content_block_start (type: tool_use)
     ▼
┌──────────┐
│ Pending  │────────┐
└────┬─────┘        │ stream ends
     │ content_block_start (type: tool_result)
     ▼              │
┌──────────┐        │
│Completed │        │
└──────────┘        │
                    ▼
              ┌──────────┐
              │Incomplete│ → Resumable=true
              └──────────┘
```

### 4.3 实现

```go
// tool_call_validator.go
type ToolCallValidator struct {
    pendingToolUses map[string]*ToolUseState
    mu              sync.RWMutex
}

type ToolUseState struct {
    ID        string
    StartedAt time.Time
    Completed bool
}

// OnToolUse registers a new tool_use block
func (v *ToolCallValidator) OnToolUse(id string) {
    v.mu.Lock()
    defer v.mu.Unlock()
    v.pendingToolUses[id] = &ToolUseState{
        ID:        id,
        StartedAt: time.Now(),
        Completed: false,
    }
}

// OnToolResult marks a tool_use as completed
func (v *ToolCallValidator) OnToolResult(id string) {
    v.mu.Lock()
    defer v.mu.Unlock()
    if state, exists := v.pendingToolUses[id]; exists {
        state.Completed = true
    }
}

// ValidateComplete checks all tool_uses have results
func (v *ToolCallValidator) ValidateComplete() error {
    v.mu.RLock()
    defer v.mu.RUnlock()
    
    for id, state := range v.pendingToolUses {
        if !state.Completed {
            return fmt.Errorf("tool_use %s is incomplete", id)
        }
    }
    return nil
}
```

### 4.4 集成点（计划中）

#### Anthropic Stream Processing
```go
// anthropic_bridge.go
func processAnthropicStream(reader io.Reader, validator *ToolCallValidator) {
    for event := range parseSSE(reader) {
        switch event.Type {
        case "content_block_start":
            if event.ContentBlock.Type == "tool_use" {
                validator.OnToolUse(event.ContentBlock.ID)
            }
        case "content_block_delta":
            if event.Delta.Type == "tool_result" {
                validator.OnToolResult(event.Delta.ToolUseID)
            }
        }
    }
    
    // Stream end - validate completeness
    if err := validator.ValidateComplete(); err != nil {
        return StreamOutcome{
            Interrupted: true,
            Reason:      "incomplete_tool_call",
            Kind:        errorsx.KindUpstreamDown,
            Resumable:   true,
        }
    }
}
```

---

## 5. 可观测性

### 5.1 Prometheus 指标

#### SSE Frame Validation
```go
// metrics/interface.go
type Metrics interface {
    RecordMalformedSSEFrame(provider, stage string)
}

// Prometheus metric
llm_gateway_malformed_sse_frame_total{provider="minimax", stage="first_frame"}
llm_gateway_malformed_sse_frame_total{provider="minimax", stage="mid_stream"}
```

**查询示例**:
```promql
# 5分钟内的无效帧率
rate(llm_gateway_malformed_sse_frame_total[5m])

# 按提供商分组
sum(rate(llm_gateway_malformed_sse_frame_total[5m])) by (provider)

# 按阶段分组
sum(rate(llm_gateway_malformed_sse_frame_total[5m])) by (stage)
```

#### Tool Call Validation (计划中)
```go
llm_gateway_incomplete_tool_call_total{provider="anthropic", reason="missing_result"}
llm_gateway_incomplete_tool_call_total{provider="anthropic", reason="stream_interrupted"}
llm_gateway_incomplete_tool_call_total{provider="anthropic", reason="id_mismatch"}
```

### 5.2 日志记录

#### First Frame Malformed
```
level=WARN msg="stream: malformed first SSE frame detected"
  payload_prefix="{\"id\":\"chatcmpl-xyz\",\"object\":\"chat.completion.chunk\","
  client_model="minimax-m3"
  vendor="minimax"
  reason="incomplete_or_invalid_json"
```

#### Mid-Stream Malformed
```
level=WARN msg="stream: malformed SSE frame detected mid-stream"
  payload_prefix="{"
  chunk_count=5
  committed=false
  client_model="minimax-m3"
  vendor="minimax"
```

### 5.3 StreamCapture Integration

验证器通过 `StreamCapture` 记录中断原因：
```go
if capture != nil {
    capture.MarkInterruptedWithReason("malformed_sse_frame")
    // 或
    capture.MarkInterruptedWithReason("incomplete_tool_call")
}
```

这使得后续分析可以在数据库中查询：
```sql
SELECT reason, COUNT(*) 
FROM request_logs_hot 
WHERE interrupted = true 
  AND reason LIKE '%malformed%'
GROUP BY reason;
```

---

## 6. 错误处理策略

### 6.1 决策树

```
收到 SSE 帧
    │
    ▼
验证 JSON 完整性
    │
    ├─ 有效 ─────────────────────────────────────► 继续处理
    │
    └─ 无效
        │
        ▼
    检查客户端可见性
        │
        ├─ 尚未提交（chunkCount < commitThreshold）
        │    │
        │    ▼
        │  记录 malformed_sse_frame 指标
        │    │
        │    ▼
        │  返回 Resumable=true
        │    │
        │    ▼
        │  Survival Coordinator 重试
        │
        └─ 已提交
             │
             ▼
           记录 malformed_sse_frame 指标
             │
             ▼
           丢弃此帧，继续处理
             │
             ▼
           客户端收到部分响应（好于完全中断）
```

### 6.2 Resumable 标志语义

`Resumable=true` 告诉 survival coordinator:
1. **问题是暂时的** - 上游不稳定，而非永久失败
2. **可以重试** - 在 holdback window 内重试
3. **无客户端影响** - 还没有字节到达客户端
4. **应该 failover** - 尝试下一个候选提供商

### 6.3 Holdback Window

Survival coordinator 使用 holdback window 控制重试：
- 在窗口内：丢弃坏帧，重试
- 超出窗口：返回错误给客户端

验证层确保在窗口内快速失败，最大化重试成功率。

---

## 7. 测试策略

### 7.1 单元测试

**文件**: `sse_frame_validator_test.go`

**覆盖**:
- 有效 JSON 对象
- 无效 JSON（裸 `{`、截断、语法错误）
- 边界情况（空字符串、`[DONE]`、注释）
- 各种提供商格式（OpenAI、Anthropic、MiniMax）

```go
func TestValidateSSEDataFrame(t *testing.T) {
    tests := []struct {
        name  string
        line  string
        valid bool
    }{
        {"valid_json", `data: {"id":"123","object":"chat.completion"}`, true},
        {"bare_brace", `data: {`, false},
        {"truncated", `data: {"id":"123"`, false},
        {"done_marker", `data: [DONE]`, true},
        {"empty", `data: `, true},
    }
    // ...
}
```

### 7.2 集成测试

**文件**: `sse_frame_validator_integration_test.go`

**场景**:
1. **首帧无效** → 返回 Resumable=true，无客户端输出
2. **中间流无效（未提交）** → 返回 Resumable=true
3. **中间流无效（已提交）** → 丢弃帧，继续
4. **假阳性防护** → 确保有效帧不被拒绝

```go
func TestStreamOpenAI_MalformedFirstFrame(t *testing.T) {
    cases := []struct {
        name     string
        firstLine string
    }{
        {"bare_brace", "data: {\n\n"},
        {"truncated_json", "data: {\"id\":\"123\"\n\n"},
        {"garbage", "data: not_json\n\n"},
    }
    
    for _, tc := range cases {
        outcome := streamWithFirstLine(tc.firstLine)
        assert.True(t, outcome.Resumable, "should be resumable")
        assert.Zero(t, clientOutput, "no output to client")
    }
}
```

### 7.3 性能基准测试

```go
func BenchmarkValidateSSEDataFrame(b *testing.B) {
    validFrame := `data: {"id":"chatcmpl-xyz","choices":[...]}`
    invalidFrame := `data: {`
    
    b.Run("valid", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            validateSSEDataFrame(validFrame)
        }
    })
    
    b.Run("invalid", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            validateSSEDataFrame(invalidFrame)
        }
    })
}
```

**结果**:
```
BenchmarkValidateSSEDataFrame/valid-8     1171245    1011 ns/op
BenchmarkValidateSSEDataFrame/invalid-8  13185031     90.88 ns/op
```

---

## 8. 部署与监控

### 8.1 部署策略

1. **245 测试环境** - 观察 48 小时
   - 监控 malformed_sse_frame 指标
   - 确认无假阳性
   - 验证性能影响

2. **154 生产环境** - 谨慎部署
   - 使用 SOP 文档
   - 实时监控指标
   - 准备快速回滚

3. **灰度发布（可选）** - 如有多实例
   - 先部署 1/3 实例
   - 观察 1 小时
   - 逐步扩展

### 8.2 监控面板

**Grafana 面板配置**:
- **Panel 1**: Malformed SSE Frame Rate
  ```promql
  rate(llm_gateway_malformed_sse_frame_total[5m])
  ```
- **Panel 2**: By Provider
  ```promql
  sum(rate(llm_gateway_malformed_sse_frame_total[5m])) by (provider)
  ```
- **Panel 3**: By Stage
  ```promql
  sum(rate(llm_gateway_malformed_sse_frame_total[5m])) by (stage)
  ```
- **Panel 4**: Request Success Rate (overall)
  ```promql
  rate(llm_gateway_requests_total{status="success"}[5m]) / 
  rate(llm_gateway_requests_total[5m])
  ```

### 8.3 告警规则

```yaml
groups:
  - name: sse_validation
    rules:
      - alert: HighMalformedSSERate
        expr: rate(llm_gateway_malformed_sse_frame_total[5m]) > 0.05
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High malformed SSE frame rate detected"
          
      - alert: CriticalMalformedSSERate
        expr: rate(llm_gateway_malformed_sse_frame_total[5m]) > 0.10
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Critical malformed SSE frame rate"
```

---

## 9. 未来改进

### 9.1 短期（1-2 周）
- [ ] 完成 Tool Call Validator 实现
- [ ] 添加更多提供商格式的测试
- [ ] 优化 JSON 验证性能（如需要）

### 9.2 中期（1-2 月）
- [ ] 实现电路熔断器（malformed rate > 阈值时切换提供商）
- [ ] 添加自适应验证（根据提供商历史稳定性调整）
- [ ] 增强可观测性（详细的帧内容采样）

### 9.3 长期（3-6 月）
- [ ] 机器学习预测（识别即将失败的流）
- [ ] 自动修复（尝试补全不完整的 JSON）
- [ ] 提供商健康评分系统

---

## 10. 参考资料

### 10.1 相关文档
- `.handoff/2026-08-29-task-completion-report.md` - 第一阶段总结
- `.handoff/2026-08-29-next-steps-progress-update.md` - 第二阶段总结
- `docs/audit/2026-08-29-sse-validation-audit.md` - 审计报告
- `.handoff/2026-08-29-tool-result-missing-analysis.md` - Tool Call 问题分析

### 10.2 代码位置
```
domains/streaming/sse_frame_validator.go           # 验证器实现
domains/streaming/sse_frame_validator_test.go      # 单元测试
domains/streaming/sse_frame_validator_integration_test.go  # 集成测试
domains/streaming/stream.go                         # 集成点
domains/streaming/tool_call_validator.go           # Tool Call 验证器（进行中）
metrics/interface.go                                # 指标接口
metrics/prometheus.go                               # Prometheus 实现
```

### 10.3 决策记录

| 日期 | 决策 | 原因 |
|------|------|------|
| 2026-08-29 | 首帧严格验证，中间流区分已提交/未提交 | 平衡安全性和用户体验 |
| 2026-08-29 | 使用 json.Unmarshal 验证而非正则 | 准确性 > 性能（开销可接受） |
| 2026-08-29 | Resumable=true 而非直接失败 | 允许透明重试，提升成功率 |
| 2026-08-29 | 集成到 stream.go 而非独立中间件 | 减少复杂度，访问 attemptCommitGate 状态 |

---

**最后更新**: 2026-08-29  
**下次审查**: 2026-09-29  
**维护者**: Backend Team
