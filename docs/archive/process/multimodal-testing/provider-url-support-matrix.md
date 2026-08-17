# 供应商附件 URL 支持矩阵（MM-2 预研草稿）

**日期**: 2026-08-15
**状态**: 草稿（预研输出，未改供应商调用代码）
**上游**: `docs/修订0811/19-整体优化实施方案-2026-08-15.md` 轨道 MM
**代码对齐**: `domains/session/v2/attachment_reference_strategy.go`（GetProviderCapability 的 `SupportsHTTPSURL`）

---

## 1. 背景与用途

MM-1a 之后，入站 base64 附件在 IR 侧已被替换为网关 URL 引用（`domains/attachments/ir_transformer.go`）。
出站渲染（MM-1b，outbound_render 接线）按 `SelectReferenceMode` 决定给上游发什么：

- provider 支持服务端拉取 URL → 直接发网关 URL（省上行带宽，doc 19 验收目标"base64 不再重复上行"）；
- provider 不支持 URL → **回退**：网关从附件存储取回内容，重新内联为 data URI 发给上游。

本矩阵回答"哪些 provider 能走 URL、哪些必须回退"，是 MM-2 回退实现与 MM-1 默认开启门禁
（doc 19 §4："MM-2 供应商 URL 支持矩阵覆盖主流 provider，其余走回退"）的决策输入。

## 2. 支持矩阵

结论列：✅ = 支持服务端拉取公网 URL；❌ = 不支持（必须回退 data URI）；⚠️ = 待真机确认。

| Provider | URL 拉取 | 证据 / 来源 | 现有代码一致性 |
|---|---|---|---|
| openai | ✅ | `image_url.url` 官方支持 HTTP URL 与 data URI 两种（官方 Vision 文档）；E2E T-02(URL)/T-03(base64) 已备好用例 | `SupportsHTTPSURL: true, PreferredMode: gateway_url` ✅ 一致 |
| anthropic | ✅ | Messages `image.source.type="url"` 与 document URL source 官方支持；base64 source 为传统主路径（官方文档） | `SupportsHTTPSURL: true, PreferredMode: data` ✅ 一致（URL 留给大文件路径） |
| gemini / google | ❌ | 不接受任意 HTTPS URL；仅接受 Files API 的 `fileData.fileUri` 或 inline base64（官方文档）。网关 URL 对 Gemini 无效，必须回退 inline | `SupportsHTTPSURL: false, PreferredMode: provider_file` ✅ 一致 |
| glm / zhipu | ✅ | GLM-4V 系列 `image_url.url` 同时支持公网 URL 与 base64（docs.bigmodel.cn/cn/guide/models/vlm/glm-4v-plus-0111 等） | `SupportsHTTPSURL: true, PreferredMode: gateway_url` ✅ 一致 |
| qwen / dashscope | ✅ | qwen-vl 系列 `image_url` 支持公网 URL（DashScope 服务端拉取）与 base64（官方百炼文档） | `SupportsHTTPSURL: true, PreferredMode: gateway_url` ✅ 一致 |
| doubao / volcengine | ✅ | Ark 视觉模型 `image_url.url` 支持公网 URL 与 base64（火山方舟文档） | `SupportsHTTPSURL: true, PreferredMode: gateway_url` ✅ 一致 |
| minimax | ✅⚠️ | Chat Completions 为 OpenAI 兼容 `image_url`，M3/H3/Hailuo 级模型支持图片输入，URL 与 base64 均接受（platform.minimaxi.com 文档）；但社区反馈存在 URL 未稳定取回的案例 → 建议保留 data 回退开关 | `SupportsHTTPSURL: true, PreferredMode: data` ✅ 一致（保守首选 data） |
| deepseek | ⚠️ | 官方 Chat API 当前无公开视觉模型（VL 系列未在 chat 端点开放）；URL 拉取能力未经真机确认 | 代码已保守降级 `SupportsHTTPSURL: false, PreferredMode: data`（MM-1a 提交内落地）；真机确认支持后可升回 true |
| ollama | ❌ | 本地推理，无服务端取回能力，必须 inline | `SupportsHTTPSURL: false, PreferredMode: data` ✅ 一致 |
| 未知 provider | ❌（默认） | 保守默认，强制回退 | default 分支 `SupportsHTTPSURL: false` ✅ 一致 |

**结论**：主流可路由 provider（openai / anthropic / glm / qwen / doubao / minimax）均覆盖 URL 路径；
gemini 与 ollama 走 Files API / inline 回退。满足 doc 19 的 MM-1 默认开启门禁条件。

## 3. 对 MM-2 回退实现的输入（不实现，仅记录设计约束）

1. **回退触发点**：`SelectReferenceMode` 返回 `RefModeDataURI` 但 IR 侧已是 URL 引用时，
   网关需取回内容重新内联。触发条件 = `!capability.SupportsHTTPSURL`（gemini/ollama/未知）。
2. **取回来源**：优先网关自身附件存储（`Storage.LoadAttachment(relPath)`，hash 路径已在
   `AttachmentMetadata.Path`），不发起外网请求；仅当引用是外部 URL（用户原始 http url 未落盘）时才
   需要 HTTP fetch —— 这部分是 MM-2 的 `download/fetch` 实现缺口（doc 19 现状依据）。
3. **fetch 安全约束**（实现时必须带）：目的地址 SSRF 校验（禁内网/环回）、大小上限复用
   `attachments.DefaultMaxSize`（20MB）、超时（建议 ≤10s）、Content-Type 白名单
   （复用 `attachment_reference_strategy.SupportedMIMETypes`）。
4. **重试语义**：回退失败不得吞掉原请求错误；附件缺失时按 best-effort 保留原引用并记
   `store_failed` 状态（与 extractor 语义一致）。

## 4. 待办移交

- [ ] MM-2 实现时：真机验证 `deepseek` 视觉/URL 能力；如支持则把 `SupportsHTTPSURL` 升回 true（矩阵测试同步）。
- [ ] MM-2 实现时：minimax URL 拉取稳定性加 E2E 用例（URL 失败自动降 base64）。
- [ ] MM-4：`scripts/multimodal-e2e` 增加网关 URL 模式用例（T-02 变体：客户端只发网关 URL，验证 provider 拉取）。
- [ ] anthropic document URL 的 100MB 上限（`MaxURLBytes`）与 Files API 优先级在 MM-1b 渲染时复核。

## 5. 参考来源

- 智谱 GLM-4V 系列（URL + Base64 双示例）: https://docs.bigmodel.cn/cn/guide/models/vlm/glm-4v-plus-0111
- MiniMax Chat Completions（OpenAI 兼容 image_url）: https://platform.minimaxi.com/docs/api-reference/text-chat-openai
- MiniMax Messages/模型模态说明（M 系列多模态，旧 abab/M2 系列纯文本）: https://platform.minimaxi.com/docs/api-reference/text-chat-anthropic
- 本仓库 E2E 框架与用例现状: `docs/multimodal-testing/00-test-plan.md`、`scripts/multimodal-e2e/`（T-02..T-15）
