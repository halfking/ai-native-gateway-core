# 格式转换审计与方案

本目录是 llm-gateway-go 的客户端、统一 IR、原厂协议和多模态格式转换文档 SSOT。

## 文档导航

| 文档 | 内容 |
|---|---|
| [01-系统总览与数据流.md](./01-系统总览与数据流.md) | 客户端协议、IR、原厂协议和响应回转换链路 |
| [02-客户端协议.md](./02-客户端协议.md) | OpenAI Chat、OpenAI Responses、Anthropic Messages、Gemini native 的入站形状 |
| [03-原厂协议适配.md](./03-原厂协议适配.md) | OpenAI、Anthropic、Gemini、DeepSeek、GLM、MiniMax、Qwen、Ollama、Doubao 的适配边界 |
| [04-多模态转换.md](./04-多模态转换.md) | image、audio、video、document、file URI 和未知字段保留规则 |
| [05-审计矩阵.md](./05-审计矩阵.md) | 代码、测试、原厂标准和当前结论逐项对照 |
| [06-模拟测试与发布门禁.md](./06-模拟测试与发布门禁.md) | fixture、round-trip、流式和回归测试要求 |
| [07-问题清单与修正记录.md](./07-问题清单与修正记录.md) | 本轮确认的问题、修复和后续事项 |
| [08-MiniMax-M3-审计与统一标准.md](./08-MiniMax-M3-审计与统一标准.md) | MiniMax-M3 协议路径、会话压缩、工具、多模态和 154/252 证据 |

## 状态定义

- `verified`: 代码和测试均有直接证据，且格式符合原厂标准。
- `partial`: 主路径已实现，但存在模型、endpoint 或字段子集限制。
- `unsupported`: 明确没有实现，不能由兼容协议名称推断支持。
- `unverified`: 缺少一手资料或真实脱敏 fixture，不进入“已支持”结论。

## 核心原则

1. 客户端协议与原厂协议分离；OpenAI Chat 和 Responses 不共用请求格式。
2. 先解析到统一 IR，再按目标原厂协议序列化；不能用字符串替换代替结构化转换。
3. 标准字段必须保留；供应商私有字段只能在同厂同协议方向恢复，不能跨厂泄漏。
4. 多模态能力按输入/输出能力和 model offer 判断，不从厂商名称推断。
5. 工具调用必须保持 call ID、工具名和结果的关联；工具轮次不能被压缩或转换拆散。
6. 未知字段和未知流式事件必须保留或产生明确 loss report。

## 本轮审计结论

现有系统已经包含 OpenAI、Anthropic、Gemini native 的 IR parser/serializer，以及 Responses 和厂商兼容层桥接。历史文档中“Gemini 原生 adapter 未实现”的结论已过时，应以本目录和代码为准。

本轮修正了 Gemini native serializer 对 OpenAI 风格 `Message.ToolCalls` 和 `tool` 消息的遗漏，补充了并行工具调用与工具结果的模拟测试。原生 endpoint 的模型能力差异、Usage 细项和计费单位仍须按 provider profile 与真实 fixture 继续核验。
