# IR 格式优化方案

**版本**: v2.0
**日期**: 2026-07-12
**状态**: 审计完成，待评审实施

## 文档目标

本目录是 llm-gateway-go 的 IR 协议与多模态优化方案 SSOT。方案覆盖 OpenAI、Anthropic、Gemini、DeepSeek、GLM、Qwen、MiniMax、Ollama、Doubao，并将请求转换、流式事件、模型能力、usage、供应商成本和租户计费作为独立问题处理。

## 文档导航

| 文档 | 内容 |
|------|------|
| [01-现状审计与修正.md](./01-现状审计与修正.md) | 对原方案和现有实现的审计、错误修正、可信结论 |
| [02-目标架构.md](./02-目标架构.md) | Protocol Adapter、统一 IR、媒体、能力、usage、计费设计 |
| [03-实施与迁移计划.md](./03-实施与迁移计划.md) | 分阶段任务、数据库迁移、回滚和验收门禁 |
| [04-协议与验证矩阵.md](./04-协议与验证矩阵.md) | 各协议支持范围、测试矩阵、原厂资料索引 |
| [05-Phase-A-Extensions实施结果.md](./05-Phase-A-Extensions实施结果.md) | 方向隔离方案 A/B 的代码改动与测试证据 |
| [06-Provider-Profile审计与收敛.md](./06-Provider-Profile审计与收敛.md) | 供应商字段审计、P0 误删修复与 profile 前置条件 |
| [10-Provider-IR-Multimodal-Audit-2026-07-13.md](./10-Provider-IR-Multimodal-Audit-2026-07-13.md) | 9 厂商 IR 多模态、usage、计费和 Doubao/火山方舟 provider family 审计 |

## 核心决策

1. 不再把所有供应商都视为 OpenAI 或 Anthropic 的字段变体；按协议注册 Adapter。
2. OpenAI Chat Completions 与 Responses 是两个独立协议，不复用同一个请求 parser。
3. 使用统一 `MediaPart` 表示 image/audio/video/document；PDF 是 MIME 类型，不是独立结构。
4. 模型能力使用输入/输出能力集合；旧 `modality` 仅作为派生展示字段。
5. usage 使用可扩展指标集合；原厂 usage 优先，估算值必须标记来源，不参与重复计费。
6. 供应商成本和租户 credits 保持两套 rate card，但共用同一组 usage metric。
7. 不增加固定的 image/audio/video/file 八列；采用带单位和生效区间的版本化费率组件。
8. `credit_ledger.entry_type` 保持 `consume`；明细写入请求级 charge breakdown。
9. 流式转换保留未知事件，并用有状态 assembler 维护 output item/content part 生命周期。
10. 先修复 Extensions 方向和 Responses 输入损坏，再扩展原生 Gemini/Ollama。

## 范围边界

本方案覆盖：

- 文本、图像、音频、视频、文档输入的协议表达与转换。
- 文本、reasoning、工具调用和可选多模态输出的响应表达。
- SSE、NDJSON 等流式传输的语义归一化。
- 模型/offer 能力声明、路由校验、usage 归一化和可审计计费。

本方案暂不覆盖：

- Realtime/WebSocket 会话协议。
- 图像生成、语音合成、转录等独立产品 API 的统一化。
- 供应商 Files API 的文件生命周期托管；第一阶段只支持已有 file ID/URI 引用。
- 在网关内持久化 OpenAI `previous_response_id` 指向的响应状态。

## 完成定义

- 所有协议通过 Adapter 注册，不支持的协议 fail closed。
- 同协议往返不丢标准字段；跨协议转换输出明确的 loss report。
- 多模态请求只路由到满足全部输入能力的 model offer。
- usage 和 charge 可按 request ID 重放，重复写入不会重复扣费。
- `request_logs_hot`、父表、分区、union view 和 promotion 流程保持列兼容。
- 单元、契约、fixture replay、真实上游 canary 和前端浏览器验证均通过。
