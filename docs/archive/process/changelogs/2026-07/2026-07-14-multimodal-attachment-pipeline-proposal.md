# 2026-07-14 — 多模态附件 HTTP 直传 + request_logs 只存 URL + 详情 UI 优化方案

> 本文档对应 ticket `multimodal-attachment-pipeline`，定位为「多轮优化
> 拼图」，不是单次落地。所有改动按 7 个 Phase 切分，每个 Phase 是
> 可独立 review / revert 的 PR。

## 0. 现状速览（与今天代码对齐）

| 维度 | 现状 | 来源 |
|---|---|---|
| `files.kxpms.cn` 客户端 | **不存在**（0 处代码） | 全仓搜索 0 命中 |
| 已有 attachments 提取 | ✅ 已实现，但只覆盖 `openai-completions` 路径 | `domains/streaming/handler.go:1031` 调用 `attachmentExtractor.ExtractFromOpenAIBody`；`messages.go` 路径未调用 `ExtractFromAnthropicBody` |
| `request_logs.request_body` | **包含 base64 原文** | `sql/schema/01-schema.sql:1593` |
| `request_logs.attachments` | JSONB，**仅元数据**，不含字节 | `sql/migrations/startup/325_request_attachments.sql:16-25` |
| 附件落盘位置 | 本地 FS：`./data/attachments/YYYY/MM/req_<id>/<sha>.<ext>` | `domains/attachments/storage.go:241` |
| 列表附件角标 R3 | ✅ 已实现 | `web/src/views/RequestLogsView.vue:1285-1290` |
| 详情附件 Tab R4 | ✅ 已实现 | `RequestLogsView.vue:1396-1404` |
| 图片大图预览 | ✅ 已实现（lightbox） | `RequestLogsView.vue:1527-1534` |
| 视频/音频播放 | ❌ 没有，走 `<a download>` | `isImageAttachment` 判定 only |
| PDF/音频 inline 预览 | ❌ 没有 |  |
| 多模态数据丢失路径 | 已知 ≥ 5 条活跃 + ≥ 4 条边缘 | 见 §3 |

---

## 1. 与 6 条需求的逐条差距

### R1 ─ 多模态文件需先 HTTP 直传 `files.kxpms.cn`，URL 入 request_logs
- **现状 0%**：无 `files.kxpms.cn` 客户端，全仓 grep 0 命中。`domains/attachments/storage.go` 写本地 FS，`storage_backend_oss.go` / `storage_backend_s3.go` 是 build-tag 实现的 OSS/S3 后端，也不是 kxpms。
- **期望 100%**：每个 `data:` URI → kxpms 上传 → URL 替换 → 转发 + 入库全部以 URL 形态。

### R2 ─ request_logs 原则上不存多模态字节，只存 URL
- **现状 0%**：`request_logs.request_body jsonb` 完整存了原 body（含 base64）；`attachments` JSONB 存的是路径 + SHA256 + 200 字符 `original_url`（含 data URI 前缀）。**结果**：磁盘上与数据库里两份字节存在。
- **期望 100%**：落在 DB 里的 image_url 全部是 `https://files.kxpms.cn/<id>` 形式的 URL；data URI 上传后即丢弃原 base64，只在 attachments 表里保留 URL 指针 + 元数据。

### R3 ─ 列表显示附件标志 + 数量
- **现状 100%**：实现见 `RequestLogsView.vue:1285-1290`，后端 `admin/logs.go:163` 通过 `COALESCE(jsonb_array_length(rl.attachments), 0)` 计算 `attachment_count`。
- **期望 100%**：**无需改动**，可保留兼容。Phase 5 顺手把 `attachment_count` 计算方式从 `attachments` 列迁到 `attachment_urls` 列（见 §2）。

### R4 ─ 详情页"响应内容"右侧加"附件"Tab，多个 tabpage
- **现状 100%**：见 `RequestLogsView.vue:1384, 1392, 1396-1404`，tab 顺序为「请求消息 → 转发消息 → 响应内容 → 📎 附件 (N)」。
- **期望 100%**：无需新增 tab 容器，仅在点击 tab 时显示 `AttachmentList` 组件即可。

### R5 ─ 附件列表：信息 + 缩略图 + 直载按钮 + 大图/原生播放器
- **现状 ~75%**：图片缩略图 + lightbox + 下载完成；视频/音频/PDF **没有 inline 预览**，点击 video 走 `<a download>`（文件被下载到本地，不能"调用默认播放器"）。
- **期望 100%**：
  - `image/*`（含 SVG）：`<img>` + lightbox（已有）
  - `video/*`：`<video :src="url" controls preload="metadata">` 直接播放
  - `audio/*`：`<audio :src="url" controls preload="metadata">`
  - `application/pdf`：`<iframe :src="url">` 或浏览器原生预览
  - 其它：`fileExt` 图标 + 直载（已实现）

### R6 ─ 详情页应能展示请求带的图片/文件，可下载、可巡查
- **现状 70%**：附件本身可下载；**`request_body` 完整 base64 仍在前端 JSON 树里可达**（意味着 multi-MB JSON 走线）。在没有 URL 重写的当下是必然结果。
- **期望 100%**：当 Phase 2（kxpms URL 替换）落地后，前端可完全不下载 base64 字节，只在「请求消息」标签内用 markdown/text 显示"图片（[查看](url)）"链接 → 点击打开新窗口预览。`request_body` jsonb 列由 30TiB 缩到几 MB。

---

## 2. 目标架构（Phase 全景）

```
                 ┌───────────────────────────────────────────────────────────────┐
   Client  ──▶   │ 1. POST /v1/chat/completions (含 data:image/...;base64,...) │
                 │ 2. body reads, attachExtractor.ExtractFromXxxBody          │
                 │ 3. NEW: uploader.UploadForRequest(bodyBytes)                │
                 │    ├─ scan all data: URIs                                    │
                 │    ├─ dedup by sha256 → local hash cache                     │
                 │    ├─ POST files.kxpms.cn/api/v1/upload (multipart)          │
                 │    ├─ return { url, cdn_url }                                │
                 │    └─ rewrite bodyBytes: data: URI → URL, 记录 attachment_urls │
                 │ 4. prepareRequestBody (MergeConsecutiveMessages, etc.)       │
                 │ 5. forward(upstreamBody) → upstream LLM                      │
                 │ 6. attachments → request_logs.attachments (含 url 字段)      │
                 │ 7. attachment_urls → request_logs.attachment_urls            │
                 └───────────────────────────────────────────────────────────────┘

前端:
    列表:   📎 N   ← 直接读 attachment_urls 数组长度，不需要重新扫描 attachments
    详情:
      - 请求消息 tab:  文本保持原样；image_url 块渲染成 <img :src="url"> 链接
      - 📎 附件 tab:    <AttachmentList :items="attachments" />  ← 新组件
                         ├─ <img> / <video> / <audio> / <iframe> / fileExt
                         └─ 直载按钮 → window.open(url + '?dl=1') 或 <a download>
      - 直载 / 缩略 / 放大 / 默认播放器
```

---

## 3. 多模态数据丢失清单（dedupConsecutive 修复之外）

> 这些不是文档"理论上的边界条件"，都是今天实际会命中的活跃路径。

### Tier 1 — 默认 Legacy 链路上仍存在的丢图路径

| # | 文件:行 | 问题 | 影响 |
|---|---|---|---|
| L1 | `domains/transformation/anthropic/chat_to_anthropic.go:99-107` | OpenAI `data:image/...` 一律判为 `source.type="url"`，Anthropic 实体会拒 | Q3（OpenAI→Anthropic）路径全部 base64 图均下行错误 |
| L2 | `domains/streaming/anthropic_bridge.go:920-931` | 与 L1 同病的流式版本 | Live SSE OpenAI→Anthropic 同等损坏 |
| L3 | `domains/transformation/anthropic/anthropic_to_chat_request.go:203-218` | Anthropic `case "image"` 只生成 `[Image: <url>]` 或 `[Image: base64 data]` 字符串 | Q2（Anthropic→OpenAI）丢图 |
| L4 | `domains/streaming/messages.go:692-701` | `convertBlockMessage` 遇到 `image` 块立刻 return，**丢弃前后所有 sibling text/tool_use** | Q2 Anthropic `[text, image]` 多模态消息丢上下文 |
| L5 | `domains/hooks/compression/strip.go:286-349` | `stripThinkingBlocks` 在 thinking-块存在时一并删除 `image_url` / `input_audio` | 长会话在 smart 压缩后失图 |
| L6 | `domains/transformation/sanitizer.go:647-672, 569, 582, 595` | `extractContentText` / `collapseToolHistory` 只看 `type=="text"`，折叠 tool 历史时丢图 | Q3 上 tool-use 模型把图像 tool result 当文本传播 |

### Tier 2 — Anthropic 入站路径根本不会落盘

| # | 文件:行 | 问题 |
|---|---|---|
| S1 | `domains/streaming/messages.go` 全文 | **找不到** `ExtractFromAnthropicBody` 的调用点。`extractor.go:117` 实现了但没人调 |
| S2 | `domains/streaming/messages.go:653-722` `convertBlockMessage` | 同 L4 |

### Tier 3 — 边界/边缘丢图

| # | 文件:行 | 问题 |
|---|---|---|
| E1 | `domains/streaming/handler.go:51` + `messages.go:214` | `maxBodySize = 128 MiB`；多图 high-res 请求直接 413 |
| E2 | `domains/streaming/handler.go:4365-4384` | `extractFirstUserMessage` 只导文本，首条图文消息审计 hook 永远 Pass，prompt injection 留口 |
| E3 | `domains/attachments/storage.go:282` | 单图 > 20 MB 即报错（已落地 best-effort，但客户端会看到上游也炸） |
| E4 | `bg/storage_retention_worker.go:36` + `admin/data_lifecycle_attachments.go` | 30 天只删盘不删库 / 只删库不删盘，附件元数据与文件双向脱钩 |

---

## 4. 优化方案：7 个 Phase

> 每个 Phase 都是独立可 review / revert 的 PR；schema migration 都在
> `sql/migrations/startup/` 下，n+1 编号按已有 326~390 顺延。

### Phase 1 ── 修复 active 数据丢失（L1/L2/L3/L4）

目标：**今天就丢图的所有活跃路径 100% 修好**，不依赖 kxpms 上线。

| 改动 | 文件:行 | 内容 |
|---|---|---|
| L1 修 | `chat_to_anthropic.go:99-107` | 增加 `parseDataURI(url)` helper；识别 `data:image/...;base64,XXX` 时改写为 `{type:"image", source:{type:"base64", media_type:"image/png", data:"<base64 part>"}}`；HTTP URL 走原 `type:"url"` 分支 |
| L2 修 | `anthropic_bridge.go:920-931` | 同 L1 |
| L3 修 | `anthropic_to_chat_request.go:203-218` | base64 → `data:image/...` URI（再由 Phase 2 后续替换成 kxpms URL）；URL → 保留原 URL 不退化 |
| L4 修 | `messages.go:692-701` | 改成"先累加 textParts，再返回带 [text..., image_part(s)] 的数组 content"，text 不能再丢 |

> 测试：5 个跨协议用例 + 1 个 lossless regression 双向测试。

### Phase 2 ── files.kxpms.cn Uploader 服务端接入

#### 2.1 新模块
```
domains/attachments/uploader/
├── uploader.go            // interface Uploader + MultiBackend
├── files_kxpms.go         // FilesKxpmsBackend
├── local_passthrough.go   // LocalBackend (dev/test, 等价于今天的 storage)
├── hashcache.go           // sha256 -> {url, cdn_url, expires_at} 内存 LRU
├── config.go              // 从 env / DB config table 读 base_url/auth/timeout
└── uploader_test.go       // 用 httptest.NewServer mock 上游
```

接口：
```go
type UploadRequest struct {
    Bytes       []byte         // 已经 base64-decoded 的图片二进制
    ContentType string
    Filename    string         // 推断: image-2026-07-14T103045Z-<sha8>.png
    SHA256Hex   string
    Source      string         // "client" / "rewritten"
    TenantID    string
}

type UploadResult struct {
    URL       string          // https://files.kxpms.cn/<id>
    CDNURL    string          // https://cdn.kxpms.cn/<id>
    FileID    string
    Size      int
    ExpiresAt time.Time
}

type Uploader interface {
    Upload(ctx context.Context, req UploadRequest) (*UploadResult, error)
    Resolve(ctx context.Context, fileID string) (*UploadResult, error)
}
```

#### 2.2 调用点插入
- `domains/streaming/handler.go:1030-1041` 在 `attachExtractor.ExtractFromOpenAIBody` **之后**插一段 `rewriteDataURIsToURLs(bodyBytes, uploader)`；返回 `upstreamBody`。下游以 `upstreamBody` 为准发送。
- 同时新增 `domains/streaming/messages.go` 内对 Anthropic body 的同位置调用。**S1 顺便修**：默认路径与 OpenAI 对齐。

#### 2.3 失败策略
- 上传失败 → 写 warning，**不阻塞请求**（与现有 attachment extractor 策略一致），但保留 base64 转发给上游（fail-open）。

#### 2.4 配置
- Env: `FILES_KXPMS_BASE_URL`, `FILES_KXPMS_TIMEOUT_SEC`, `FILES_KXPMS_ENABLED`
- DB: `attachment_uploader_config` 表（仅 super_admin 可改）；token 走 SOPS 加密（沿用 `secret/` 既有路径）

#### 2.5 测试
- 单测：mock files.kxpms.cn 推送 3 种 case（pic 1KB, 20MB edge, 网络 5xx 重试）
- 集成：现有 `extractor_test.go` 加 `_with_uploader.go` 跑全链路

### Phase 3 ── request_logs 入库形态切到 URL

#### 3.1 新 migration `391_attachment_urls.sql`
```sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS attachment_urls JSONB;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS attachment_url_rewritten BOOLEAN DEFAULT FALSE;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS upstream_body JSONB;

CREATE INDEX IF NOT EXISTS idx_request_logs_attachment_urls_gin
    ON request_logs USING GIN (attachment_urls);
CREATE INDEX IF NOT EXISTS idx_request_logs_attachment_url_rewritten
    ON request_logs (ts DESC) WHERE attachment_url_rewritten = true;
```

`attachment_urls JSONB` 形如：
```json
[{
  "msg_idx": 0,
  "block_idx": 1,
  "content_type": "image/png",
  "size_bytes": 23842,
  "sha256": "abc...",
  "url": "https://files.kxpms.cn/3f91...",
  "cdn_url": "https://cdn.kxpms.cn/3f91...",
  "original_kind": "data_uri" | "http_url"
}]
```

#### 3.2 入库策略
- `request_logs.request_body` 仍保留客户端原文（用于审计 + 入境文本还原）；
- `upstream_body` 写**真正发出的 body**（URL 替换后）。由此能对比"R1 改写"的偏差；
- `attachment_url_rewritten = TRUE` 标记此次 R1 实际做了替换；
- `attachments` 列继续保留历史，**只用作 UI 直载回落到本地盘**的 fallback；当 `attachment_urls` 命中，本地直载不再必要。

#### 3.3 列表接口（迁移兼容）
- `admin/logs.go:163` 当前的 `COALESCE(jsonb_array_length(rl.attachments),0)` 计算 `attachment_count` 不动；
- 但新增 `attachment_count_v2 = COALESCE(jsonb_array_length(rl.attachment_urls),0)`，前端 `RequestLogRow` 取 `v2 ?? v1` 回退。

### Phase 4 ── 前端 UI 升级（满足 R5 / R6 全量）

#### 4.1 新组件
- `web/src/components/attachments/AttachmentList.vue` — 抽离 grid 卡片，避免在 `RequestLogsView.vue` / `RequestLogDrawer.vue` 里复制粘贴。
- `web/src/components/attachments/AttachmentPreview.vue` — 渲染 `<video>` / `<audio>` / `<iframe>` / `<img>` 四类预览；点击缩略图仍 emit `open-lightbox`。
- `web/src/components/attachments/AttachmentLightbox.vue` — 现有 lightbox 抽离（`<Teleport to="body">`），支持 video/audio 当前帧显示。
- `web/src/components/attachments/AttachmentQuickBar.vue` — 在 "请求消息" tab 里出现，列「本条消息携带的 N 个附件」徽章 → 跳到 📎 附件 tab。

#### 4.2 API 客户端扩展 (`web/src/api/logs.ts`)
```ts
export interface AttachmentURL {
  msg_idx: number;
  block_idx: number;
  content_type: string;
  size_bytes: number;
  sha256: string;
  url: string;           // 永远 https://files.kxpms.cn/<id>
  cdn_url?: string;
  original_kind: 'data_uri' | 'http_url';
}
export interface RequestLogDetail extends RequestLogRow {
  request_body: any;
  response_body: any;
  attachments?: AttachmentMeta[];     // 保留 - 本地 FS 直载 (向后兼容)
  attachment_urls?: AttachmentURL[];  // 新增 - 文件服务 URL
  upstream_body?: any;                // 新增 - 真正发出的 body
}
```

#### 4.3 i18n keys（沿用现有 locale）
- `requests.detail_extra.previewVideo` / `previewAudio` / `previewPdf` / `openInNewTab`
- `requests.detail_extra.quickBarTitle`

#### 4.4 交互细节
- 图片：`<img>` + `<a :href="url" target="_blank">打开原图</a>`
- 视频：`<video controls preload="metadata" :src="url" />` —— 浏览器原生 controls 已默认全屏按钮
- 音频：同上
- PDF：`<iframe :src="url" sandbox="allow-same-origin" />`，不支持时显示「[下载 PDF](url)」
- 直载按钮：`<a :href="url + '?dl=1'" download :title="filename">直载</a>`（files.kxpms.cn 端开发 `?dl=1` 加 `Content-Disposition: attachment`）

### Phase 5 ── 旧 attachments 路径做"安全降级"

- 修改 `admin/logs.go:163` 计算逻辑：`attachment_count` 取 `attachment_urls` 长度 → 这样列表角标即反映「被 kxpms 上传过的图」，与详情 tab 一致。
- 本地 FS 路径仍然保留（数据生命周期页 / 文件系统维护页继续走老路径）；Phase 5 默认先放出 kxpms，**保留双写**六个月做"对比"统计。
- 删除 `bg/storage_retention_worker.go` 中只删盘不删库的逻辑、删除 `admin/data_lifecycle_attachments.go` 中只清库不删盘的逻辑，确保 DB 与 FS 双写双删。**E4 顺便修**。

### Phase 6 ── Anthropic 入站补齐 + 5xx 兜底

| 改动 | 文件:行 | 内容 |
|---|---|---|
| S1 修 | `domains/streaming/messages.go` 读 body 后 | 调 `attachmentExtractor.ExtractFromAnthropicBody`，把 base64 图片存到本地 FS（仍保留）并喂给 uploader |
| L5 修 | `domains/hooks/compression/strip.go:286-349` | smart 压缩时把 image_url 用 §3.1 attachment_urls 替换历史里的图片 → 摘要仍可引用 `https://files.kxpms.cn/<id>`；下一步轮次再考虑多模态摘要 |
| L6 修 | `domains/transformation/sanitizer.go:647-672` | `extractContentText` 在 `type!=text` 但有 `image_url`/`input_audio` 时返回 `[图片 N]` 占位，**不丢图元数据** |
| E1 修 | `domains/streaming/handler.go:51` + `messages.go:214` | `maxBodySize` 从 128MiB 提到 1GiB；或根据 `attachment_urls` 数量做 N×20MiB 软限制 |
| E2 修 | `domains/streaming/handler.go:4365-4384` | `extractFirstUserMessage` 改为"在多模态情况下，从 attachment_urls 提取一个 OCR stub"（Phase 8 + OCR 路径） |

### Phase 7 ── Admin 入口与回滚能力

- 新增 admin 入口 `web/src/views/data-lifecycle/AttachmentUploadConfig.vue`：可看 `attachment_uploader_config.base_url`、开关、`enabled`、最近 24h 上传 QPS/失败率、单测按钮（POST 1×1 PNG）。
- 新增 admin 日志列 `request_logs.upload_failed_count` + `request_logs.upload_failed_reason`：uploader 失败但仍 fallback 转发时记录，运营可一键"重试上传"。
- 后端埋点：`go-metrics` 暴露 histogram `attachment_upload_seconds{result=ok|retry|fail}`、`attachment_url_rewrite_count`。

---

## 5. 风险与边界

| 风险 | 缓解 |
|---|---|
| kxpms 不可用时段 | 失败→ fallback base64，不阻断（Phase 2.3）。配置开关关掉即关闭整个 rewrite |
| kxpms 上传慢 → 长请求 | 单图上传超时默认 5s（总超时 30s 并发限 5 张；并发取 Min(imageCount, 5)），整体上限按 env 可调 |
| 既有直存到 DB 的 base64 怎么办 | Phase 5 双写 6 个月，前端默认走新 URL；Phase 7+ 提供一次性 backfill 脚本（按 ts 范围 backfill 一批） |
| IR transport 路径 | IR (`internal/ir/parse_openai.go:347`) 也扫描 base64，需确保 uploader 在 IR 之前跑（设计放在 `handler.go:1030` 是早于 routing 的） |
| `kxpms.cn` 内部鉴权 | 沿用 `manifests/secrets/` 的 SOPS 加密 + `secret.Get("FILES_KXPMS_TOKEN")` |
| 上游 LLM 拒收 URL 形式图片 | kxpms URL 应当 allowlist；某些模型只接受 base64 → 此场景回退路径不变 |
| Lightbox 体积超过 1MB | 直传分流：`<img :src=thumbnail>` (kxpms 端支持 `?w=300`) + lightbox 时切原图；可作 Phase 8+ |

---

## 6. 落地次序 & 验收

| Phase | 估时 | 落地次序 | 依赖 |
|---|---|---|---|
| **Phase 1** | 2–3d | **本 PR 链优先** | 无 |
| **Phase 2** | 5–7d | 第 2 个 PR | 无（kxpms 端需预开通） |
| **Phase 3** | 1d | 同 Phase 2 PR | migration 391 |
| **Phase 4** | 3d | 同 Phase 2 PR | 后端接口稳定 |
| **Phase 5** | 0.5d | 第 3 个 PR | Phase 2 已上线一周 |
| **Phase 6** | 3d | 第 4 个 PR | Phase 3 上线 |
| **Phase 7** | 2d | 第 5 个 PR | Phase 2 上线 |

每 Phase 验收按本规范要求：

- 单测覆盖（`go test ./domains/attachments/...` Phase 2+100%；Phase 4 前端组件 spec）
- `go test ./...` 全绿；`golangci-lint run` 0 issues
- PR 关联 changelog：`docs/changelogs/<date>-multimodal-pipeline-phaseN.md`
- 前端走 `browser-use` 实战 video-level 验证列表 + 详情 + 4 种 MIME 预览（满足 rule 11 §6 browser-use 强制实测）

---

## 7. 验证清单（必须满足）

- [ ] Phase 1 后：在 `cmd/gateway/main_pipeline.go` 写一次 Playwright + curl，minimax-m3 端同时提交 OpenAI 客户端 / Anthropic 客户端两种含图请求，验证直连等价性
- [ ] Phase 2 后：mock files.kxpms.cn 服务器跑端到端，反向确认 attachment_urls column 与 attachments column 都正确写入
- [ ] Phase 4 后：browser-use 录 video 验证 4 类 MIME（image / video / audio / pdf），并截图 `ui-verify-attachment-{kind}-{ts}.png` 入档
- [ ] Phase 5 后：列表 / 详情 / 数据生命周期三处 UI 与 DB double-write 联动无误

---

## 8. 与既有 ticket 的关系

- 与 `2026-07-13-request-log-save-fail-fix`：attachments JSONB 写入是它
  落地的范本，迁移 391 沿用其命名 + index 习惯
- 与 `2026-07-13-data-lifecycle-radio-and-dedup`：storage admin UI 与
  Phase 7 共用入口
- 与今天修的 `dedupConsecutive`（多模态 base64 修复）：本方案在
  Layer 2 不与之耦合；Layer 1 (Phase 1) 是它之后的横向拓展

---

## 9. 一句话执行版本

> **Phase 1 先动手 3d**（堵漏改写模型间转换，避免 image_url 仍以错误形态发出）；
> **Phase 2+3+4 一个 2 周 sprint 内合并发布**（kxpms 接入 + URL 入库 + UI 升级）；
> **Phase 6+7 一个 1 周 sprint**（补全路径 + admin 入口）。
> Phase 5 双写 / Phase 8+ 优化点（OCR 占位 / thumbnail）按需排期。
