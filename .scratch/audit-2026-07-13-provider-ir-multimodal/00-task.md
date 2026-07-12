# Audit: Provider IR 多模态 & 计费完整性

**状态**: completed
**优先级**: P0
**创建日期**: 2026-07-13
**完成日期**: 2026-07-13
**关联文档**: `docs/IR格式优化/10-Provider-IR-Multimodal-Audit-2026-07-13.md` (Doubao 增补后)

## 任务目标

针对 9 个 LLM 供应商（OpenAI / Anthropic / Gemini / GLM / MiniMax / DeepSeek / Ollama / Qwen / Doubao），逐一审计 IR（internal/ir）在以下维度的实际覆盖与缺失：

1. 文本请求模式：Chat Completions、Messages、generateContent、/api/chat 等 wire format 转换是否完整
2. 输出格式：流式（SSE/NDJSON）与非流式 response 解析是否覆盖原厂全部字段
3. 多模态请求：image / audio / video / document / file 各自的解析与序列化支持
4. 多模态 usage：reasoning / cache / image / audio / video / document 各类 metric 提取
5. 多模态计费：rate component 是否支持 per_token / per_image / per_second / per_request 等单位
6. capability / 路由：vendor 能力描述与路由过滤是否能匹配多模态请求

## 阶段

1. ✅ 代码现状扫描（types.go / parse_*.go / serialize_*.go / response.go / usage.go / billing）
2. ✅ 官方资料核对（OpenAI / Anthropic / Gemini / DeepSeek / GLM / Qwen / MiniMax / Ollama / Doubao docs）
3. ✅ 形成审计报告（`docs/IR格式优化/10-Provider-IR-Multimodal-Audit-2026-07-13.md`）
4. ✅ 输出 P0/P1/P2 任务清单 + 代码改动建议
5. ✅ 追加 Doubao / 火山方舟官方入口与 `volcengine-coding` 聚合入口匹配审计
6. ⏳ 提交 PR + 更新 CHANGELOG

## 关键发现摘要

1. **多模态 IR 覆盖极薄**：仅 `ImageSource` 一类内容块；audio / video / document / file 全部缺失
2. **UsageData 字段不全**：缺少 reasoning_tokens、audio_tokens、image_tokens、video_tokens、document_tokens
3. **billing 按 token 线性**：MaAS 计费公式只支持 4 类 token × 单价，多模态单位无表达
4. **多模态能力路由缺失**：models_canonical.modality 单值枚举，无法表达 image+audio
5. **Gemini 原生 / Ollama 原生 adapter 未实现**：现状是 fallback 到 OpenAI Chat 兼容层，丢失 inlineData/fileData/eval_count
6. **流式解析存在差异**：Anthropic SSE 多 event 累加；Gemini SSE GenerateContentResponse / Ollama NDJSON 未实现
7. **DeepSeek cache hit/miss 维度分离**：当前 UsageData 把所有 cache 都计入 CacheReadTokens，但 DeepSeek hit 与 miss 价不同
8. **Ollama 计费语义错误**：Ollama 是本地 GPU，per-token 计费语义错误；应该用 deployment-scoped rate card
9. **Anthropic message content 下的 document 块丢失**：仅 system 数组下识别，message content 下走 default 分支丢失
10. **OpenAI input_audio / output_audio 完全未支持**：Chat audio / Responses audio 全部缺失
11. **Doubao 入口存在 provider family 边界**：`doubao` 是官方目录，`volcengine-coding` 是多厂商聚合，不能共用 Doubao stripper、usage extractor 或 rate card
12. **Doubao 原生能力未建模**：火山方舟文档、视频、音频、文件、Responses、上下文缓存和深度思考尚未进入统一 IR

## 报告主要章节

- §1 执行摘要（含风险等级表）
- §2 厂商逐项审计（OpenAI / Anthropic / Gemini / DeepSeek / GLM / MiniMax / Ollama / Qwen / Doubao）
  - 每厂商：当前 IR 覆盖、官方资料核对表、关键风险
- §3 多模态 IR 扩展方案（ContentPart / MediaPart / AssetRef 详细设计）
- §4 多模态计费方案（UsageMetric、Rate Component、idempotency、防重复计费）
- §5 实施任务清单（P0/P1/P2）
- §6 验证矩阵（fixture 要求、计费验证用例、必跑测试、发布指标）
- §7 相关文档导航

## 关联 commit

- `a6edd018f` fix(ir): 补充 parse/serialize Extensions 逻辑 (audit-10)
- `10674ee11` fix(ir): 修复厂商自定义参数丢失问题 (audit-10)
- `b7d613b03` fix(ir): use ir.ParseOpenAI to handle string/array content correctly
- `80d332cb7` fix(streaming): wire GLM/DeepSeek/Doubao strippers into non-stream response path
- `3222003c8` fix(streaming): preserve OpenAI-compatible and billing-critical fields in vendor strippers

## 下一步建议

按 P0 → P1 → P2 顺序执行：
1. **P0**（1-2 周）：UsageData 扩展 + Reasoning 计费；input_audio / output_audio；Anthropic message document；Usage Normalizer 注册
2. **P1**（2-4 周）：MediaPart/AssetRef 类型；Capability 表 + 路由；Gemini 原生 Adapter；Ollama 原生 Adapter；Rate Component 表
3. **P2**（1-2 月）：Qwen 原生 adapter；Responses API 原生 Parse；MiniMax voice/video；Invoice reconciliation；LossReport 持久化

每个阶段都需保持审计-10 的测试门禁和发布指标，新增改动需 CHANGELOG 同步更新。
