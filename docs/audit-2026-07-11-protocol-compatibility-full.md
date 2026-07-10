# Protocol Compatibility Full Audit Report

**Date:** 2026-07-11  
**Scope:** llm-gateway-go 协议转换层 — 8厂商兼容性全量审计  
**Author:** OpenCode Agent  
**Status:** 🔴 P0 风险已识别

---

## 执行摘要

### 八厂商状态表

| 厂商 | 协议类型 | Extensions支持 | Streaming完整性 | 私有字段泄露风险 | 综合评级 |
|------|---------|---------------|----------------|-----------------|---------|
| OpenAI | OpenAI-Chat | ✅ 原生 | ✅ 完整 | 🟢 低 | ✅ A |
| Anthropic | Anthropic-Messages | ✅ 原生 | ✅ 完整 | 🟢 低 | ✅ A |
| Google Gemini | OpenAI-Chat | ⚠️ 依赖IR | 🔴 缺失 | 🟡 中 | 🟡 C |
| 智谱GLM | OpenAI-Chat | ⚠️ 依赖IR | 🔴 缺失 | 🟡 中 | 🟡 C |
| MiniMax | OpenAI-Chat | ⚠️ 依赖IR | 🔴 缺失 | 🔴 高 | 🔴 D |
| DeepSeek | OpenAI-Chat | ⚠️ 依赖IR | 🟢 部分 | 🟡 中 | 🟢 B |
| Doubao | OpenAI-Chat | ⚠️ 依赖IR | 🟢 部分 | 🟡 中 | 🟢 B |
| Ollama | OpenAI-Chat | ⚠️ 依赖IR | ✅ 完整 | 🟢 低 | 🟢 B+ |

### P0 风险点

🔴 **风险1：Extensions 默认关闭导致信息丢失**
- 环境变量 `LLM_GATEWAY_IR_CONVERTER` 未设置时，6家厂商的私有字段全部丢失
- 影响：tool_calls、function_calling、多模态能力不可用

🔴 **风险2：Streaming 转换缺失**
- Gemini/GLM/MiniMax 三家厂商的 SSE 流未实现协议转换
- 影响：客户端收到格式错误的流事件，导致解析失败

🔴 **风险3：私有字段泄露到下游**
- MiniMax 的 `bot_setting`、GLM 的 `retrieval` 等字段未过滤
- 影响：下游厂商可能拒绝请求或产生未定义行为

---

## 第一部分：架构现状

### 1.1 三层 IR 架构

当前系统采用三层中间表示（IR）架构：

```
Client Protocol → Parse → IR → Serialize → Upstream Protocol
   (OpenAI)                                    (Anthropic)
   (Anthropic)                                 (OpenAI)
```

**核心模块：**
- `internal/ir/parse_openai.go` - OpenAI 请求解析
- `internal/ir/parse_anthropic.go` - Anthropic 请求解析
- `internal/ir/serialize_openai.go` - OpenAI 请求序列化
- `internal/ir/serialize_anthropic.go` - Anthropic 请求序列化
- `internal/ir/response.go` - 响应转换（8个方向）

**特性开关：**
```bash
LLM_GATEWAY_IR_CONVERTER=true   # 启用 IR 转换（推荐）
LLM_GATEWAY_IR_CONVERTER=false  # 回退到 legacy callbacks（默认，风险高）
```

### 1.2 Extensions 机制

IR 层通过 `Extensions` 字段保留私有字段：

```go
type InternalRequest struct {
    Model       string
    Messages    []Message
    Temperature *float64
    MaxTokens   *int
    Extensions  map[string]interface{}  // 🔑 私有字段存储
}
```

**支持的私有字段示例：**
- MiniMax: `bot_setting`, `reply_constraints`
- GLM: `retrieval`, `web_search`
- Gemini: `safetySettings`, `generationConfig`
- Doubao: `plugins`, `bot_id`

⚠️ **当前问题：** `LLM_GATEWAY_IR_CONVERTER` 未设置时，所有私有字段被丢弃。

---

## 第二部分：八厂商详查


### 2.1 OpenAI（A级）

**协议类型：** OpenAI Chat Completions  
**转换路径：** Q1 passthrough  
**Extensions：** ✅ 原生支持  
**Streaming：** ✅ 完整

**评估：**
- ✅ 无需协议转换
- ✅ 所有字段原生传递
- ✅ SSE 流格式标准

### 2.2 Anthropic（A级）

**协议类型：** Anthropic Messages  
**转换路径：** Q4 passthrough  
**Extensions：** ✅ 原生支持  
**Streaming：** ✅ 完整

**评估：**
- ✅ 无需协议转换
- ✅ 所有字段原生传递
- ✅ SSE 流格式标准

### 2.3 Google Gemini（C级）

**协议类型：** OpenAI Chat（模拟）  
**转换路径：** Q1 → IR → Gemini API  
**Extensions：** ⚠️ 依赖 IR  
**Streaming：** 🔴 缺失

**私有字段：**
```json
{
  "safetySettings": [...],
  "generationConfig": {
    "candidateCount": 1,
    "stopSequences": [...]
  }
}
```

**问题：**
1. 🔴 IR 未开启时，`safetySettings` 丢失
2. 🔴 Streaming 响应未实现 SSE 转换
3. 🟡 `candidateCount > 1` 时需特殊处理

### 2.4 智谱GLM（C级）

**协议类型：** OpenAI Chat（模拟）  
**转换路径：** Q1 → IR → GLM API  
**Extensions：** ⚠️ 依赖 IR  
**Streaming：** 🔴 缺失

**私有字段：**
```json
{
  "retrieval": {
    "enable": true,
    "search_query": "..."
  },
  "web_search": {
    "enable": true
  }
}
```

**问题：**
1. 🔴 IR 未开启时，知识库检索功能失效
2. 🔴 Streaming 响应未实现转换
3. 🟡 `web_search` 字段可能泄露到下游

### 2.5 MiniMax（D级）

**协议类型：** OpenAI Chat（模拟）  
**转换路径：** Q1 → IR → MiniMax API  
**Extensions：** ⚠️ 依赖 IR  
**Streaming：** 🔴 缺失

**私有字段：**
```json
{
  "bot_setting": [
    {
      "bot_name": "MM智能助理",
      "content": "你是一个专业助手"
    }
  ],
  "reply_constraints": {
    "sender_type": "BOT",
    "sender_name": "MM智能助理"
  }
}
```

**问题：**
1. 🔴 IR 未开启时，角色设定完全失效
2. 🔴 Streaming 响应未实现转换
3. 🔴 `bot_setting` 字段泄露风险高（包含敏感配置）
4. 🟡 `reply_constraints` 可能导致下游解析错误

### 2.6 DeepSeek（B级）

**协议类型：** OpenAI Chat（高兼容）  
**转换路径：** Q1 passthrough（部分 IR）  
**Extensions：** ⚠️ 依赖 IR  
**Streaming：** 🟢 部分支持

**私有字段：**
```json
{
  "frequency_penalty": 0.5,
  "presence_penalty": 0.5
}
```

**评估：**
- 🟢 基础功能无需 IR
- ⚠️ 高级参数需要 IR
- 🟢 Streaming 基本兼容

### 2.7 Doubao/字节（B级）

**协议类型：** OpenAI Chat（高兼容）  
**转换路径：** Q1 passthrough（部分 IR）  
**Extensions：** ⚠️ 依赖 IR  
**Streaming：** 🟢 部分支持

**私有字段：**
```json
{
  "plugins": ["web_search", "calculator"],
  "bot_id": "7389..."
}
```

**评估：**
- 🟢 基础功能无需 IR
- ⚠️ 插件系统需要 IR
- 🟢 Streaming 基本兼容

### 2.8 Ollama（B+级）

**协议类型：** OpenAI Chat（完全兼容）  
**转换路径：** Q1 passthrough  
**Extensions：** ✅ 无私有字段  
**Streaming：** ✅ 完整

**评估：**
- ✅ 完全兼容 OpenAI 协议
- ✅ 无需 Extensions
- ✅ Streaming 标准实现

---

## 第三部分：信息丢失风险分析

### 3.1 Extensions 默认关闭的影响

| 厂商 | 丢失功能 | 业务影响 | 风险等级 |
|------|---------|---------|---------|
| Gemini | 安全过滤配置 | 内容可能被误拦截 | 🟡 中 |
| GLM | 知识库检索 | RAG 功能失效 | 🔴 高 |
| MiniMax | 角色设定 | 对话质量下降 | 🔴 高 |
| DeepSeek | 参数微调 | 生成质量降低 | 🟡 中 |
| Doubao | 插件调用 | 工具能力失效 | 🔴 高 |

**统计：**
- 🔴 高风险厂商：3家（GLM、MiniMax、Doubao）
- 🟡 中等风险：2家（Gemini、DeepSeek）
- 影响用户数：约 60%（假设厂商流量均匀分布）

### 3.2 Streaming 缺失矩阵

| 厂商 | Non-Stream | Stream | 问题描述 |
|------|-----------|--------|---------|
| OpenAI | ✅ | ✅ | 完整 |
| Anthropic | ✅ | ✅ | 完整 |
| Gemini | ✅ | 🔴 | SSE 事件格式错误 |
| GLM | ✅ | 🔴 | 无转换，客户端解析失败 |
| MiniMax | ✅ | 🔴 | 无转换，客户端解析失败 |
| DeepSeek | ✅ | 🟡 | 部分兼容，边缘情况失败 |
| Doubao | ✅ | 🟡 | 部分兼容，边缘情况失败 |
| Ollama | ✅ | ✅ | 完整 |

**现象：**
```
# Gemini Streaming 错误示例
data: {"candidates":[{"content":{"parts":[...]}}]}

# 期望格式（OpenAI SSE）
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[...]}
```

### 3.3 私有字段泄露矩阵

| 源厂商 | 私有字段 | 目标厂商 | 泄露后果 |
|--------|---------|---------|---------|
| MiniMax | `bot_setting` | OpenAI | 400 Bad Request |
| GLM | `retrieval` | Anthropic | 字段被忽略或错误 |
| Doubao | `plugins` | Gemini | 可能触发未定义行为 |
| Gemini | `safetySettings` | GLM | 字段被忽略 |

**实测案例：**
```bash
# MiniMax → OpenAI 转发（未过滤）
curl -X POST https://api.openai.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [...],
    "bot_setting": [...]  # ❌ OpenAI 拒绝
  }'

# 返回
{"error":{"message":"Invalid parameter: bot_setting","type":"invalid_request_error"}}
```

---

## 第四部分：开闭原则审计

### 4.1 硬编码违反点

| # | 位置 | 硬编码内容 | 违反原则 | 风险 |
|---|------|-----------|---------|------|
| 1 | `cmd/gateway/main.go:372` | `ChatToAnthropic` 回调 | 开闭原则 | 新增厂商需改代码 |
| 2 | `cmd/gateway/main.go:401` | `AnthropicToChatResponse` 回调 | 开闭原则 | 新增厂商需改代码 |
| 3 | `internal/ir/parse_openai.go:45` | `if model == "gpt-4"` | 策略模式缺失 | 模型判断逻辑分散 |
| 4 | `domains/streaming/chat_executor.go:123` | 协议判断 switch | 多态缺失 | 新增协议需改 switch |
| 5 | `internal/ir/extensions.go:0` | ❌ 不存在 | 扩展点缺失 | 无法动态注册厂商 |

**示例：添加新厂商（Cohere）需要改动的地方**

1. `cmd/gateway/main.go` - 新增回调函数
2. `internal/ir/parse_openai.go` - 新增解析逻辑
3. `internal/ir/serialize_openai.go` - 新增序列化逻辑
4. `domains/streaming/chat_executor.go` - 新增 switch case
5. `internal/models/provider.go` - 新增 provider 常量

**预估工作量：** 5个文件 × 平均30行 = 150行改动

### 4.2 推荐改进：插件化架构

```go
// 理想架构
type ProviderPlugin interface {
    Name() string
    ParseRequest([]byte) (*IR, error)
    SerializeRequest(*IR) ([]byte, error)
    ParseResponse([]byte) (*IR, error)
    SerializeResponse(*IR) ([]byte, error)
    StreamTransform(io.Reader) (io.Reader, error)
}

// 注册机制
func RegisterProvider(name string, plugin ProviderPlugin) {
    providerRegistry[name] = plugin
}

// 使用
func init() {
    RegisterProvider("cohere", &CoherePlugin{})
    RegisterProvider("mistral", &MistralPlugin{})
}
```

**收益：**
- ✅ 新增厂商无需改动核心代码
- ✅ 测试隔离性强
- ✅ 支持热加载（可选）

---

## 第五部分：可观测性设计

### 5.1 新增字段建议

**请求维度：**
```go
type RequestLog struct {
    // 现有字段...
    
    // 新增
    IREnabled           bool              `json:"ir_enabled"`
    ExtensionsPreserved bool              `json:"extensions_preserved"`
    ExtensionKeys       []string          `json:"extension_keys"`
    ProtocolConversion  string            `json:"protocol_conversion"` // "Q1", "Q2", "Q3", "Q4"
    StreamingMode       string            `json:"streaming_mode"`      // "none", "sse", "chunked"
}
```

**响应维度：**
```go
type ResponseLog struct {
    // 现有字段...
    
    // 新增
    UpstreamProtocol    string  `json:"upstream_protocol"`
    ClientProtocol      string  `json:"client_protocol"`
    ConversionLatencyMS float64 `json:"conversion_latency_ms"`
    ExtensionsDropped   int     `json:"extensions_dropped"`
}
```

### 5.2 采集点设计

```go
// cmd/gateway/main.go
func (e *Executor) Execute(ctx context.Context, req *Request) error {
    start := time.Now()
    
    // 采集点1：协议识别
    metrics.RecordProtocolPair(req.ClientProtocol, req.UpstreamProtocol)
    
    // 采集点2：Extensions 状态
    if e.IR != nil {
        ir, _ := e.IR.ParseRequest(req.Body)
        metrics.RecordExtensions(len(ir.Extensions), req.Provider)
    }
    
    // 采集点3：转换耗时
    convStart := time.Now()
    convertedBody, _ := e.ConvertRequest(req)
    metrics.RecordConversionLatency(time.Since(convStart), req.Provider)
    
    // 采集点4：流式模式
    if req.Stream {
        metrics.RecordStreamingMode(req.ClientProtocol, req.UpstreamProtocol)
    }
    
    // ...
}
```

### 5.3 Grafana 面板设计

**Panel 1: 协议转换矩阵（Heatmap）**
```promql
sum by (client_protocol, upstream_protocol) (
    rate(llm_gateway_protocol_conversion_total[5m])
)
```

**Panel 2: Extensions 保留率（Gauge）**
```promql
sum(llm_gateway_extensions_preserved_total) 
/ 
sum(llm_gateway_extensions_total) * 100
```

**Panel 3: Streaming 错误率（Graph）**
```promql
sum by (provider) (
    rate(llm_gateway_streaming_errors_total[5m])
) 
/ 
sum by (provider) (
    rate(llm_gateway_streaming_requests_total[5m])
) * 100
```

---

## 第六部分：改进方案

### 6.1 P0 任务（立即执行，1周内）

| # | 任务 | 负责人 | 工期 | 验收标准 |
|---|------|--------|------|---------|
| P0-1 | 默认启用 IR | @backend | 1天 | `LLM_GATEWAY_IR_CONVERTER=true` 写入 k8s configmap |
| P0-2 | Gemini Streaming 转换 | @backend | 2天 | 通过 `TestGeminiStreamingConversion` |
| P0-3 | GLM Streaming 转换 | @backend | 2天 | 通过 `TestGLMStreamingConversion` |
| P0-4 | MiniMax 字段过滤 | @backend | 1天 | `bot_setting` 不泄露到下游 |
| P0-5 | 监控面板上线 | @sre | 1天 | Grafana 显示协议转换指标 |

**预估总工期：** 5个工作日（可并行）

### 6.2 P1 任务（2周内）

| # | 任务 | 负责人 | 工期 | 验收标准 |
|---|------|--------|------|---------|
| P1-1 | MiniMax Streaming 转换 | @backend | 2天 | 通过端到端测试 |
| P1-2 | Extensions 白名单机制 | @backend | 3天 | 配置文件控制哪些字段可穿透 |
| P1-3 | 协议转换日志增强 | @backend | 2天 | 日志包含 `protocol_conversion` 字段 |
| P1-4 | 自动化回归测试 | @qa | 3天 | 8厂商 × 2模式 = 16个测试用例 |
| P1-5 | 告警规则配置 | @sre | 1天 | Extensions丢失率 > 5% 触发告警 |

**预估总工期：** 7个工作日（可并行）

### 6.3 P2 任务（1月内）

| # | 任务 | 负责人 | 工期 | 验收标准 |
|---|------|--------|------|---------|
| P2-1 | 插件化架构重构 | @backend | 10天 | 新增厂商无需改核心代码 |
| P2-2 | 动态协议注册 | @backend | 5天 | 运行时加载 provider plugin |
| P2-3 | 性能基准测试 | @qa | 3天 | 转换开销 < 5ms (p99) |
| P2-4 | 文档完善 | @tech-writer | 3天 | 厂商接入指南 |
| P2-5 | Legacy 代码清理 | @backend | 5天 | 删除 `_to-be-deprecated/` 目录 |

**预估总工期：** 20个工作日

---

## 第七部分：实施时间表（Gantt）

```
Week 1
├── P0-1: IR默认启用 ████████ (1d)
├── P0-2: Gemini Stream ████████████████ (2d)
├── P0-3: GLM Stream    ████████████████ (2d, parallel)
├── P0-4: MiniMax过滤  ████████ (1d)
└── P0-5: 监控面板     ████████ (1d)

Week 2
├── P1-1: MiniMax Stream ████████████████ (2d)
├── P1-2: Extensions白名单 ████████████████████████ (3d)
├── P1-3: 日志增强       ████████████████ (2d, parallel)
├── P1-4: 回归测试       ████████████████████████ (3d)
└── P1-5: 告警规则       ████████ (1d)

Week 3-4
├── P2-1: 插件化重构     ██████████████████████████████████████████████████ (10d)
├── P2-2: 动态注册       ████████████████████████ (5d, depends on P2-1)
├── P2-3: 性能测试       ████████████████ (3d)
├── P2-4: 文档           ████████████████ (3d, parallel)
└── P2-5: 代码清理       ████████████████████████ (5d, depends on P2-1)
```

**里程碑：**
- ✅ Week 1 结束：P0 风险全部解决
- ✅ Week 2 结束：监控覆盖率 100%
- ✅ Week 4 结束：架构重构完成，新增厂商成本降低 80%

---

## 附录

### A. 测试用例清单

```bash
# P0 回归测试
go test ./internal/ir -run TestGeminiStreamingConversion
go test ./internal/ir -run TestGLMStreamingConversion
go test ./internal/ir -run TestMinimaxFieldFiltering

# P1 端到端测试
go test ./e2e -run TestProtocolConversionMatrix

# P2 性能测试
go test -bench=BenchmarkIRConversion ./internal/ir
```

### B. 环境变量参考

```bash
# 启用 IR 转换（必须）
export LLM_GATEWAY_IR_CONVERTER=true

# Extensions 白名单（可选）
export LLM_GATEWAY_EXTENSIONS_WHITELIST="bot_setting,retrieval,plugins"

# 调试模式
export LLM_GATEWAY_DEBUG_PROTOCOL=true
```

### C. 相关文档

- [Protocol Conversion Matrix (2026-06-29)](./2026-06-29-protocol-conversion-matrix.md)
- [IR Architecture Audit (2026-06-23)](./IR-ARCHITECTURE-AUDIT-2026-06-23.md)
- [Three Layer Architecture Audit (2026-06-23)](./2026-06-23-three-layer-architecture-audit.md)

---

**文档版本：** v1.0  
**最后更新：** 2026-07-11  
**下次审计：** 2026-08-11 (P2任务完成后)
