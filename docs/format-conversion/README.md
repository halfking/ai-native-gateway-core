# 双向转换逻辑说明文档

本文档描述网关在 **客户端 → 网关 → 厂商 → 网关 → 客户端** 路径上的所有转换逻辑。

## 核心原则

1. **IR（Internal Representation）中性**：内部数据结构不偏向任何协议。
2. **解析阶段无损**：Parse 阶段必须保留所有原始信息（包括未知字段、reasoning、annotations）。
3. **序列化阶段按目标方言**：Serialize 阶段按目标厂商的方言规则输出。
4. **方言继承**：私有方言（如 DeepSeek、Qwen）继承 OpenAI Chat 协议，只扩展私有字段。
5. **流式必须分段**：流式响应按 chunk 转换，不能缓存完整响应。

## 转换流程图

```
                    入站请求                       出站请求
                       ↓                             ↑
┌─────────────────────────────────────────────────────────────┐
│  ParseXxx                                                  SerializeYyy
│  ↓                                                          ↑
│  InternalRequest ───────────────────────────────→ InternalRequest
│         │                                                    │
│         ├── paramguard (inbound)  ──→  InternalRequest      │
│         ├── URSM v2 路由           ──→  InternalRequest      │
│         ├── reasoncap / reasonnorm ──→  InternalRequest      │
│         ├── sanitizer              ──→  InternalRequest      │
│         └── paramguard (outbound)  ──→  InternalRequest      │
│                                                             │
└─────────────────────────────────────────────────────────────┘
                       ↓                             ↑
                ParseXxxResponse              SerializeYyyResponse
                    ↓                                ↑
            InternalResponse                InternalResponse
                       ↓                             ↑
                    入站响应                       出站响应
```

## 文件清单

| 文件 | 内容 |
|------|------|
| [ir-design.md](ir-design.md) | IR 数据结构设计原则与字段映射 |
| [parse-pipeline.md](parse-pipeline.md) | 入站解析流水线的执行顺序 |
| [serialize-pipeline.md](serialize-pipeline.md) | 出站序列化流水线的执行顺序 |
| [dialect-resolution.md](dialect-resolution.md) | 如何根据 provider catalog code + protocol 决定方言 |
| [streaming-conversion.md](streaming-conversion.md) | 流式响应的双向转换细节 |
| [error-mapping.md](error-mapping.md) | 错误码跨协议映射规则 |
| [tool-conversion.md](tool-conversion.md) | 工具调用的跨协议转换 |
| [reasoning-thinking-conversion.md](reasoning-thinking-conversion.md) | 推理/思考内容的跨协议转换 |

## 关键路径（URSM v2 角度）

URSM v2 的职责是路由决策（健康度、容量、负载均衡、优先级、Provider 选择），它**不**直接处理协议转换。协议转换发生在路由**之前**（Parse）和**之后**（Serialize）。

```
HTTP Request
    ↓
ParseOpenAI / ParseAnthropic / ParseResponses
    ↓
InternalRequest (IR)
    ↓
URSM v2 路由
    ↓
选定 provider candidate
    ↓
paramguard.Apply(bodyBytes, dialect)
    ↓
SerializeOpenAI / SerializeAnthropic / SerializeResponses / SerializeGemini
    ↓
HTTP Request (vendor native)
    ↓
HTTP Response (vendor native)
    ↓
ParseOpenAIResponse / ParseAnthropicResponse / ParseGeminiResponse
    ↓
InternalResponse (IR)
    ↓
SerializeOpenAIResponse / SerializeAnthropicResponse / SerializeResponsesResponse
    ↓
HTTP Response (client native)
```

## 不变量

1. **Parse 不可丢失信息**：客户端发的所有字段（含未知字段）必须能在 IR 中找到。
2. **Serialize 必须按方言**：同一份 IR 在不同方言下必须输出不同的 body。
3. **流式不可丢失顺序**：流式 chunk 的顺序必须在转换中保持。
4. **错误必须分类**：跨协议错误必须映射到统一的 `errorsx.Kind`。
5. **Token 必须统计**：usage 字段在转换中必须保留或估算。
