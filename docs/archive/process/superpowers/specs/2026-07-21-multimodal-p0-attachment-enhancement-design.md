# 多模态 P0 + 附件增强修复设计

**日期**：2026-07-21
**范围**：Responses 模态检测、Anthropic 媒体边界、附件元数据增强、多模态 usage/billing、provider modality schema 一致性

## 1. 目标

在不进行全量 Media IR 重构、也不进行 data URI/URL 改写的前提下，修复当前多模态请求链路中已确认的高优先级断点：

- Responses API 的 `input[]` 能正确参与模态识别和协议转换；
- OpenAI/Anthropic/Gemini/Responses 的媒体附件可以被统一扫描并持久化元数据；
- Anthropic legacy/IR 转换不会生成只有 `type` 字段的无效媒体块；
- 已有 image/audio/video/reasoning usage 能进入 request log、telemetry 和 MaAS 多模态计费；
- provider modality 的 migration、baseline schema、查询逻辑和测试保持一致。

## 2. 非目标

本轮不做以下改动：

- 不实现外部文件服务、signed URL 或 data URI → URL rewrite；
- 不重构为完整的通用 Media IR/Capability Set；
- 不新增音频秒数、视频秒数、页数等独立资费维度；
- 不为所有厂商新增原生 endpoint executor；
- 不改变与本轮无关的 legacy 文本协议行为。

## 3. 设计

### 3.1 请求能力检测

新增共享的请求能力扫描逻辑，兼容：

- OpenAI/Anthropic `messages[].content[]`；
- Gemini `contents[].parts[]`；
- OpenAI Responses `input[]` 及其 `content[]`；
- `tool_result.content` 中的嵌套媒体。

扫描结果至少包含：

```text
has_text
has_image
has_audio
has_video
has_document
primary_modality
```

`primary_modality` 保持现有路由兼容规则：`video > audio > vision > text`。新扫描器不替换现有 SQL 语义，只统一所有入口的检测输入，避免 Responses 原始 `input[]` 被误判为 text。

### 3.2 Responses 转换

在 `convertResponsesToChatBody` 路径中显式识别：

- `input_text`；
- `input_image`；
- `input_audio`；
- `input_file`；
- 已有 function call/function output。

可表达的媒体转换为 Chat content block；无法无损表达的类型返回明确的转换错误，不再作为普通 user message 静默降级。保留顶层 `modalities` 和 `audio` 配置。

### 3.3 Anthropic 转换

对 IR serializer 和 legacy converter 分别处理：

- `image`：保留 base64/url source；
- `document`：保留 base64/url source；
- `audio/video`：根据目标协议支持情况明确转换；若 Anthropic Messages 无对应标准块，则返回 `unsupported_modality`，不发送半成品 block；
- mixed content：保留 text 与媒体 sibling 的顺序。

本轮不假设 Anthropic 原生支持 OpenAI `input_audio` 或 `video_url`，因此不把 OpenAI 私有形状直接伪装成 Anthropic 标准形状。

### 3.4 附件提取与存储

扩展现有 `domains/attachments`：

- OpenAI：`image_url` data URI、`input_audio`、`file/input_file`；
- Anthropic：image/document base64 source；
- Gemini：`inlineData`；
- Responses：`input_image`、`input_audio`、`input_file` 中的 data URI/base64。

统一生成附件元数据：

```text
attachment_type
content_type
size_bytes
hash
message_index
block_index
source_kind
status
error_code
```

保存策略仍为 best-effort：存储失败不阻断上游请求，不修改实际转发 body。对声明 MIME 与 magic bytes 不一致的内容记录失败或校验状态，避免仅凭扩展名判断内容类型。保持现有大小上限和后端抽象，不在本轮引入 URL rewrite。

### 3.5 Usage 与 billing

扩展 stream capture、response body extraction 和 request log 写入，使以下字段能够贯通：

- prompt/completion；
- cache read/write；
- reasoning；
- image/audio/video；
- provider-specific tokens。

主计费收尾统一调用 `ChargeRequestMultimodal`。如果供应商只返回 aggregate token，则只使用 aggregate input/output rate，不自行拆分媒体 token；如果供应商返回 audio/video seconds，本轮仅保存 raw/provider usage 或 telemetry，不转换成 token 扣费。

### 3.6 Provider modality schema

先以仓库实际 schema 为准完成核对：

- 若 `provider_models.modality` migration、baseline schema、view 和查询均存在，则统一补齐查询及测试；
- 若缺少 migration/schema，则先补齐可部署 migration 与 baseline，再让路由查询使用 `COALESCE(provider_models.modality, models_canonical.modality, 'text')`；
- 保留 canonical modality 作为 fallback；
- 不将文档中的未落地字段直接写入查询。

## 4. 错误与兼容策略

- 附件存储错误：继续 fail-open，记录结构化错误；
- 转换无法无损表达：返回结构化 `unsupported_modality`，避免静默丢数据；
- usage 维度缺失：按零处理，不影响文本请求；
- aggregate 与 detail 同时出现：不重复计费，优先保留原始 usage 并使用明确的归一化规则；
- 旧请求和纯文本请求保持现有路由和计费行为。

## 5. 测试计划

### 单元测试

- Responses `input_text/input_image/input_audio/input_file` 转 Chat；
- Responses 模态检测和混合媒体优先级；
- Anthropic mixed content 保序；
- Anthropic 不支持媒体的明确错误；
- OpenAI/Anthropic/Gemini/Responses 附件提取；
- MIME/magic bytes/大小边界；
- stream capture 和 response body 的多模态 usage；
- `ChargeRequestMultimodal` 主路径参数。

### 集成/回归测试

- OpenAI Chat → Anthropic image/document；
- Gemini native → IR → OpenAI/Anthropic candidate；
- Responses audio/file 路由到正确 modality；
- 多模态 usage 写入 request log 并触发 MaAS 计费；
- provider modality migration/schema/query 一致性。

### 验证命令

```text
go test ./domains/attachments/...
go test ./domains/streaming/...
go test ./internal/ir/...
go test ./maas/...
go test ./...
```

如果全量测试已有与本轮无关的失败，只记录原始失败，不修改无关代码。
