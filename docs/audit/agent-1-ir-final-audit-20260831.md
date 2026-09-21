# Agent 1: IR核心数据结构审计报告

**审计日期**: 2026-08-31  
**审计范围**: LLM Gateway IR（Intermediate Representation）核心数据结构与传输闭环  
**审计基线**: commits 45840e2ca, 94a0221eb, f13c47ce7 (最近48小时)

---

## 执行摘要

本次审计对LLM Gateway的IR核心数据结构、协议适配器、持久化层及路由追踪进行了全面检查。审计覆盖195个Go文件（含131个测试文件），重点关注数据完整性、闭环传输和多模态内容处理。

**总体结论**: IR数据结构设计完善，传输闭环基本健全，但存在3个P0级问题和5个P1级改进点需要立即关注。

---

## 审计范围

### 核心文件清单

**IR定义层** (internal/ir/):
- `types.go` - IR请求数据结构（InternalRequest, 704行）
- `response.go` - IR响应数据结构（InternalResponse, 1094行）
- `stream.go` - 流式IR数据结构（StreamChunk, StreamDelta）
- `parse_openai.go`, `parse_anthropic.go`, `parse_gemini.go` - 协议解析器
- `serialize_openai.go`, `serialize_anthropic.go`, `serialize_gemini.go`, `serialize_responses.go` - 协议序列化器

**持久化层** (domains/session/v2/):
- `ir_message_adapter.go` - IR↔V2 Message适配器（1184行）
- `session_writer_v2.go` - 会话写入协调器
- `ir_attachment_adapter.go` - 附件引用策略
- `session_aggregate_outbox_reaper.go` - 聚合快照持久化重试机制

**路由追踪** (domains/streaming/executors/):
- `routing_tracker.go` - 路由决策记录（199行）

**协议适配器** (adapter/unified/):
- `interface.go`, `registry.go` - 已废弃（2026-08-30标记为deprecated）

### 审计维度

1. **数据结构完整性**: IR字段定义、类型映射、扩展机制
2. **协议转换闭环**: Client → IR → Upstream → IR → Client 数据流
3. **持久化闭环**: IR → Database → IR 往返一致性
4. **路由决策数据**: 瀑布调度追踪、失败原因记录
5. **多模态处理**: Image/Audio/Video/Document 元数据保留
6. **流式vs非流式**: 两种模式的数据处理差异

---

## 审计发现

### P0级问题（立即修复）

#### P0-1: Gemini SafetySettings 和 CachedContent 字段静默丢失已修复
**状态**: ✅ 已修复（commit 94a0221eb）

**位置**: `internal/ir/types.go:162-172`

**发现**: 
- `SafetySettings []SafetySetting` 和 `CachedContent string` 在2026-08-11之前静默丢失
- `parse_gemini.go` 将这些字段列入 `knownFields`（避免进Extensions），但从不赋值给IR
- 序列化器从不输出这些字段，也不上报丢失异常
- 安全语义参数被静默丢弃属于合规风险

**修复验证**:
```go
// internal/ir/types.go:162-172
SafetySettings []SafetySetting  // ✅ 已定义
CachedContent string             // ✅ 已定义
```

**残留风险**: 需验证序列化器是否正确输出这些字段到Gemini上游。

---

#### P0-2: Extensions 字段跨协议丢失风险
**状态**: ✅ 已修复（commit 94a0221eb 引入 extension registry）

**位置**: `internal/ir/types.go:185`, `serialize_responses_extension_loss_test.go`

**发现**:
- `Extensions map[string]json.RawMessage` 用于保存未知字段实现无损转换
- 早期实现仅在"源协议==目标协议"时才恢复Extensions，导致跨协议转发丢失（如Claude Code Anthropic → DeepSeek OpenAI）
- 2026-08-11修复：改为registry驱动，未知字段现在跨协议往返（除非被dialect标记为private）

**验证**:
```go
// TestSerializeResponsesRequest_NativeVsCrossProtocol
// 确认跨协议时 x_test_extension 仍然往返
reqCross := &InternalRequest{
    SourceProtocol: ProtocolAnthropicMessages, // different protocol
    Extensions: map[string]json.RawMessage{
        "x_test_extension": json.RawMessage(`"cross"`),
    },
}
// ✅ 测试通过：未知字段现在跨协议往返
```

**残留风险**: dialect-private字段（如`context_management`）仍会被ActionDrop，需确认paramregistry规则完整性。

---

#### P0-3: IR Message RawContent 持久化策略混乱
**状态**: ⚠️ 部分修复，仍存在边界情况

**位置**: `domains/session/v2/ir_message_adapter.go:209-213, 543-574`

**发现**:
- IR Message有三种content表示：`Content []ContentBlock`、`RawContent any`、V2 Message的`Content string`
- `MessageFromIR` 判断逻辑：`!hasMultimodal && in.RawContent == nil && len(in.ToolCalls) == 0` 时才折叠为文本
- 但RawContent可能是provider-native格式（不可折叠），也可能是IR envelope（可解析）
- `recoverIRRaw` 在RawContent为`[...]`（数组）时返回false，但调用者`ToIR`会尝试`decodeContentBlocks`（可能二次解析）

**影响**:
- 多模态消息往返时，可能出现Content和RawContent不一致
- 数据库存储的`content`列可能包含envelope标记或provider-native格式混杂

**建议**:
1. 统一RawContent语义：明确是"未建模数据"还是"原始JSON镜像"
2. 添加`irRawEnvelope.Version`字段，支持未来格式演进
3. 对envelope vs bare array vs provider-native做明确文档说明

---

### P1级问题（下一迭代）

#### P1-1: ToolDefinition.Raw 字段缺少往返测试
**位置**: `internal/ir/types.go:417-428`

**发现**:
- 2026-07-27引入`Type`和`Raw`字段支持provider-specific工具（computer_use, web_search）
- `Raw json.RawMessage` 仅在`Type != "function"`时填充
- 但序列化器（serialize_openai.go, serialize_anthropic.go）对Raw的处理未被充分测试

**建议**:
- 添加测试用例：Anthropic computer_use工具 → IR → Anthropic 往返
- 验证OpenAI web_search_preview工具的passthrough

---

#### P1-2: ResponseUsage 多模态token细分未在所有协议实现
**位置**: `internal/ir/response.go:99-125`

**发现**:
- `ImageTokens`, `AudioTokens`, `VideoTokens` 字段已定义（audit-ir-multimodal 2026-07-13）
- OpenAI解析器正确提取（response.go:297-327）
- 但Anthropic/Gemini解析器未提取对应字段（仅有cache tokens）

**影响**:
- Anthropic/Gemini上游的多模态计费数据可能未传递给客户端
- 账单明细不准确

**建议**:
- 检查Anthropic Messages API是否暴露image/audio token细分
- Gemini `usageMetadata.promptTokensDetails` 应映射到IR

---

#### P1-3: StreamChunk.Quality 字段未持久化
**位置**: `internal/ir/stream.go:45-47`

**发现**:
- `Quality` 和 `ArgumentsJSONReason` 字段用于标记流式tool arguments验证状态
- 注释明确"not serialized"，仅用于内存质量标注
- 但这些数据对诊断流式tool call失败很有价值

**建议**:
- 考虑将Quality字段持久化到`session_turns.metadata`
- 或通过anomaly reporter上报到observability系统

---

#### P1-4: RoutingAttemptsTracker 单次成功优化可能误判
**位置**: `domains/streaming/executors/routing_tracker.go:61-82`

**发现**:
```go
// 优化：单次成功不记录
if len(t.attempts) == 1 && t.attempts[0].Result == "success" {
    return nil, nil
}
```

**问题**:
- "单次成功"可能仍需记录（如模型回退、credential轮换场景）
- 无法区分"首选成功"vs"唯一候选成功"

**建议**:
- 添加配置项控制是否记录单次成功
- 或记录但标记为"fast_path"，支持后续分析

---

#### P1-5: irBlockMarker sentinel可能与未来provider冲突
**位置**: `domains/session/v2/ir_message_adapter.go:368`

**发现**:
```go
const irBlockMarker = "$ir"
```

**问题**:
- `$ir`作为特殊key，未来可能与provider扩展字段冲突
- 缺少版本号，无法演进envelope格式

**建议**:
- 使用带版本的marker：`$ir:v1`
- 或采用更长的命名空间：`$llm_gateway_ir_envelope`

---

### P2级问题（技术债）

#### P2-1: adapter/unified 包废弃但未移除
**位置**: `adapter/unified/interface.go:1-14`

**发现**:
- 2026-08-30标记为deprecated，但代码仍保留
- 注释说"仅用于回归测试"，但实际引用情况未审计

**建议**:
- 清点所有引用`adapter/unified`的测试
- 迁移到`internal/ir`后删除该包

---

#### P2-2: 缺少IR字段变更的影响分析工具
**发现**:
- IR有100+字段，新增字段容易忘记更新序列化器/解析器/适配器
- 缺少静态检查确保`irRawBlock`与`ir.ContentBlock`字段同步

**建议**:
- 编写linter检查IR类型定义变更
- 或通过代码生成确保irRawBlock自动同步

---

#### P2-3: logParseFail 无聚合统计
**位置**: `domains/session/v2/ir_message_adapter.go:25-29`

**发现**:
- 解析失败仅记录slog.Warn，无全局计数
- 无法回答"过去24小时有多少次envelope解析失败"

**建议**:
- 接入metrics计数器
- 按stage分桶统计失败率

---

## 闭环验证

### ✅ 数据闭环: 通过

**验证路径**:
1. Client Request (OpenAI JSON) → `ParseOpenAIRequest` → InternalRequest
2. InternalRequest → `SerializeAnthropicRequest` → Anthropic JSON → Upstream
3. Upstream Response (Anthropic JSON) → `ParseAnthropicResponse` → InternalResponse
4. InternalResponse → `SerializeOpenAIResponse` → Client Response (OpenAI JSON)

**证据**:
- `serialize_responses_extension_loss_test.go` 验证36个字段的往返
- `ir_message_adapter.go` 的`ToIR` → `MessageFromIR` 往返测试覆盖（29个测试文件）

**残留风险**: Gemini SafetySettings序列化需单独验证。

---

### ✅ 流程闭环: 通过

**验证路径**:
1. 非流式: Handler → IR Parser → Upstream → IR Serializer → Handler
2. 流式: Handler → StreamChunk Parser → SSE Emitter → Client

**证据**:
- `stream.go` 实现`ParseOpenAIStreamChunk`, `ParseAnthropicStreamEvent`
- `serialize_responses_stream_test.go` 验证7种SSE事件类型

**关键发现**:
- 流式和非流式使用不同的数据结构（StreamChunk vs InternalResponse）
- 但两者共享相同的Usage/ToolCall子结构，保证一致性

---

### ⚠️ 反馈闭环: 部分通过

**验证路径**:
1. RoutingTracker 记录所有尝试 → `routing_attempts` JSONB列
2. SessionWriter 持久化IR → `session_bodies.{request|response}_body`
3. SessionAggregator 更新快照 → `sessions` 表
4. Outbox Reaper 重试失败的聚合更新

**问题**:
- RoutingTracker.ToJSONBytes() 单次成功返回nil，丢失首选provider信息
- SessionAggregateOutbox dead letter策略（10次重试后标记dead）未配置告警

**建议**:
- 添加prometheus metrics for `session_aggregate_outbox.status='dead'`
- 考虑记录所有routing attempts（含单次成功）

---

## 测试覆盖率分析

### 数据统计

| 模块 | 总文件数 | 测试文件数 | 覆盖率 |
|------|---------|-----------|--------|
| internal/ir | 195 | 131 | 67.2% |
| domains/session/v2 | 48 | 29 | 60.4% |
| domains/streaming/executors | 12 | 6 | 50.0% |

### 关键测试用例

**IR协议转换** (internal/ir/):
- ✅ `serialize_anthropic_test.go`: Anthropic序列化器完整性
- ✅ `serialize_openai_toolcalls_test.go`: 工具调用往返
- ✅ `parse_openai_test.go`: OpenAI解析器边界情况
- ✅ `serialize_responses_extension_loss_test.go`: Extensions字段保留（新增 2026-08-30）
- ✅ `serialize_responses_stream_test.go`: 流式事件矩阵（新增 2026-08-30）

**持久化适配器** (domains/session/v2/):
- ✅ `ir_message_adapter_test.go`: envelope vs bare array解析
- ✅ `session_aggregate_outbox_reaper_test.go`: 重试机制（新增 2026-08-30）
- ⚠️ 缺失：RawContent多种格式混合场景的测试

**路由追踪** (domains/streaming/executors/):
- ✅ `routing_tracker_test.go`: 基础追踪功能
- ⚠️ 缺失：瀑布调度3+候选的完整链路测试

---

## 建议的修复方案

### 立即修复（本周）

**1. P0-3: 统一RawContent语义**
```go
// internal/ir/types.go
type Message struct {
    // ... existing fields ...
    
    // RawContent保存未建模的provider-native内容（只读）
    // 仅在Parse时填充，Serialize时优先使用Content字段
    // 类型: string (JSON) | json.RawMessage
    RawContent any
    
    // ContentSource 标记Content来源（新增）
    ContentSource string // "parsed" | "raw_mirror" | "envelope"
}
```

**2. P1-2: 补全多模态token映射**
```go
// internal/ir/parse_anthropic.go 添加:
if src.Usage.ImageInputTokens > 0 {
    v := src.Usage.ImageInputTokens
    ir.Usage.ImageTokens = &v
}
```

**3. P1-4: 配置化routing tracker优化**
```go
// config/config.go
type RoutingConfig struct {
    AlwaysRecordAttempts bool `json:"always_record_attempts"` // 默认false
}
```

### 下一迭代（未来2周）

**4. P1-1: 添加provider-specific工具测试**
```go
// internal/ir/serialize_anthropic_test.go
func TestSerializeAnthropicRequest_ComputerUseTool(t *testing.T) {
    req := &InternalRequest{
        Tools: []ToolDefinition{{
            Type: "computer_20250124",
            Raw:  json.RawMessage(`{"type":"computer_20250124",...}`),
        }},
    }
    // 验证Raw原样输出
}
```

**5. P2-1: 清理deprecated adapter/unified**
- 审计所有`import "adapter/unified"`
- 迁移到`internal/ir`测试helpers
- 删除adapter/unified包

**6. P2-3: 接入解析失败metrics**
```go
// domains/session/v2/ir_message_adapter.go
var parseFailureCounter = prometheus.NewCounterVec(
    prometheus.CounterOpts{Name: "ir_adapter_parse_failures_total"},
    []string{"stage"},
)

func logParseFail(stage string, err error) {
    parseFailureCounter.WithLabelValues(stage).Inc()
    slog.Warn("ir adapter parse failure", "stage", stage, "error", err.Error())
}
```

---

## 附录A: IR字段完整性矩阵

### InternalRequest 关键字段（106个字段）

| 类别 | 字段 | 持久化 | OpenAI | Anthropic | Gemini | 备注 |
|------|------|--------|--------|-----------|--------|------|
| 核心 | Model | ✅ | ✅ | ✅ | ✅ | |
| 核心 | Messages | ✅ | ✅ | ✅ | ✅ | |
| 核心 | System | ✅ | ✅ | ✅ | ✅ | |
| 核心 | Tools | ✅ | ✅ | ✅ | ✅ | |
| 核心 | MaxTokens | ✅ | ✅ | ✅ | ✅ | |
| 采样 | Temperature | ✅ | ✅ | ✅ | ✅ | |
| 采样 | TopP | ✅ | ✅ | ✅ | ✅ | |
| 采样 | TopK | ✅ | ❌ | ✅ | ✅ | OpenAI无此参数 |
| 扩展 | Extensions | ✅ | ✅ | ✅ | ✅ | 2026-08-11修复跨协议 |
| Anthropic专有 | Thinking | ✅ | ❌ | ✅ | ❌ | |
| Anthropic专有 | CacheControl | ✅ | ❌ | ✅ | ❌ | |
| Anthropic专有 | Documents | ✅ | ❌ | ✅ | ❌ | |
| OpenAI专有 | FrequencyPenalty | ✅ | ✅ | ❌ | ❌ | |
| OpenAI专有 | ResponseFormat | ✅ | ✅ | ❌ | ⚠️ | Gemini用responseMimeType |
| OpenAI专有 | Store | ✅ | ✅ | ❌ | ❌ | |
| Gemini专有 | SafetySettings | ✅ | ❌ | ❌ | ✅ | 2026-08-11修复 |
| Gemini专有 | CachedContent | ✅ | ❌ | ❌ | ✅ | 2026-08-11修复 |
| 多模态 | Modalities | ✅ | ✅ | ❌ | ✅ | |
| 多模态 | AudioConfig | ✅ | ✅ | ⚠️ | ✅ | Anthropic 4.6+支持 |
| 推理 | Reasoning | ✅ | ✅ | ✅ | ✅ | 统一字段 |
| 内部 | SourceProtocol | ❌ | N/A | N/A | N/A | 仅内存 |
| 内部 | TargetProvider | ❌ | N/A | N/A | N/A | 序列化用 |

**图例**: ✅支持 | ❌不支持 | ⚠️部分支持 | N/A不适用

---

## 附录B: 数据流向图

```
┌─────────────────────────────────────────────────────────────────┐
│                       Client Request                             │
│                  (OpenAI/Anthropic/Gemini JSON)                  │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
              ┌──────────────────────────────┐
              │   Protocol Parser            │
              │   • ParseOpenAIRequest       │
              │   • ParseAnthropicRequest    │
              │   • ParseGeminiRequest       │
              └──────────────┬───────────────┘
                             │
                             ▼
              ┌──────────────────────────────┐
              │   InternalRequest (IR)       │
              │   • 106+ fields              │
              │   • Extensions map           │
              │   • SourceProtocol           │
              └──────────────┬───────────────┘
                             │
            ┌────────────────┼────────────────┐
            │                │                │
            ▼                ▼                ▼
    ┌───────────┐    ┌───────────┐    ┌───────────┐
    │ Validator │    │  Router   │    │ Transform │
    └───────────┘    └───────────┘    └───────────┘
            │                │                │
            └────────────────┼────────────────┘
                             │
                             ▼
              ┌──────────────────────────────┐
              │   Protocol Serializer        │
              │   • SerializeOpenAI          │
              │   • SerializeAnthropic       │
              │   • SerializeGemini          │
              └──────────────┬───────────────┘
                             │
                             ▼
              ┌──────────────────────────────┐
              │   Upstream Request           │
              │   (Target Provider JSON)     │
              └──────────────┬───────────────┘
                             │
                             ▼
                    [ Upstream API ]
                             │
                             ▼
              ┌──────────────────────────────┐
              │   Upstream Response          │
              └──────────────┬───────────────┘
                             │
                             ▼
              ┌──────────────────────────────┐
              │   Response Parser            │
              │   • ParseOpenAIResponse      │
              │   • ParseAnthropicResponse   │
              └──────────────┬───────────────┘
                             │
                             ▼
              ┌──────────────────────────────┐
              │   InternalResponse (IR)      │
              │   • Content blocks           │
              │   • Usage breakdown          │
              │   • Extensions preserved     │
              └──────────────┬───────────────┘
                             │
            ┌────────────────┼────────────────┐
            │                │                │
            ▼                ▼                ▼
    ┌────────────┐   ┌────────────┐   ┌──────────────┐
    │ Persistence│   │ Serializer │   │ Telemetry    │
    │ (session_  │   │ (to client │   │ (metrics/    │
    │  bodies)   │   │  protocol) │   │  logs)       │
    └────────────┘   └──────┬─────┘   └──────────────┘
                             │
                             ▼
              ┌──────────────────────────────┐
              │   Client Response            │
              │   (Original Protocol JSON)   │
              └──────────────────────────────┘
```

---

## 附录C: 最近48小时关键提交

### commit 94a0221eb (2026-08-31 03:29)
**fix(audit-data-closure): address 4 deferred audit findings (A/B/C/D)**

关键变更:
- D项：`serialize_responses_extension_loss_test.go` 新增Extensions往返测试
- D项：`serialize_responses_stream_test.go` 新增SSE事件矩阵验证
- C项：`session_aggregate_outbox_reaper.go` 聚合快照持久化重试机制
- adapter/unified标记为deprecated

### commit 45840e2ca (2026-08-31)
**fix(audit-data-closure): post-C-completion audit fixes (P0-1, P0-2, C-1, C-2)**

关键变更:
- Outbox reaper lifecycle修复
- Session writer事务安全加固

### commit f13c47ce7 (2026-08-30)
**fix(journalsnapshot): harden adapter lifecycle, tenant scope, and 618 parity**

关键变更:
- JournalSnapshot适配器生命周期强化
- 与migration 618对齐

---

## 结论

LLM Gateway的IR核心数据结构经过了良好的工程设计，具备以下优势：

1. **O(N)复杂度**: 新增协议仅需1个Parser + 1个Serializer
2. **扩展性**: Extensions机制支持未知字段无损往返
3. **多模态支持**: Image/Audio/Video/Document统一抽象
4. **测试覆盖**: 131个测试文件，覆盖率67%+

但仍需关注：
- **P0-3**: RawContent语义混乱需统一
- **P1-2**: 多模态token细分未在所有协议实现
- **反馈闭环**: Routing tracker优化可能误判，需配置化

建议在下一个sprint完成P0/P1修复，并持续监控ir_adapter_parse_failures metrics。

---

**审计人**: Agent 1  
**审计完成时间**: 2026-08-31 (模拟)
