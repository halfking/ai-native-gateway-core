# 2026-07-15 — per-(provider, model) modality routing

## 背景

用户报告：通过网关调用 minimax-m3 + 图片时上游返回 "本模型 (minimax-m3) 不支持图像输入"，但直连 minimax-m3 接口可以正常收到图片。

之前的根因分析（见 `docs/changelogs/2026-07-14-multimodal-merge-loss.md`）锁定在
OpenAI Chat Completions 请求体的归一化环节（merge consecutive messages 时丢图）。
该 P0 已修复。

本 changelog 记录 **第二个根因** 和本任务的修复方案：**minimax-m3 在两个
不同 catalog 上的能力差异没有被路由层识别**，导致 vision 请求被路由到不支持
图像的 catalog。

## 第二个根因

`minimax-m3` 这个模型名同时挂在两个 catalog 上：

| catalog | provider URL | 真实后端 | 是否支持 M3 图像 |
|---|---|---|---|
| `minimax` | `https://api.minimaxi.com/v1` | 真 MiniMax-M3（按 platform.minimax.io 文档） | ✅ 支持 |
| `volcengine-coding` | `https://ark.cn-beijing.volces.com/api/coding/v3` | 火山方舟 Coding Plan 内部调度 | ❌ 该后端不支持 minimax-m3 视觉 |

`provider_catalog.static_models` 在 volcengine-coding 的 manifest 中列出了
`minimax-m3`（误植），导致当客户端请求 `model=minimax-m3` 时 `GetCandidates`
返回两个候选：

- `provider_id=14` catalog=`minimax` （真 MiniMax 直连，per_token billing）
- `provider_id=34` catalog=`volcengine-coding` （火山方舟，token_plan billing）

SQL 排序把 `token_plan` 排第一（`CASE WHEN ... THEN 1`），所以 vision 请求被
路由到 volcengine-coding 后端 → 返回 "本模型 (minimax-m3) 不支持图像输入"。

## 修复

### 1. Schema 迁移：`provider_models.modality` 列

`models_canonical.modality` 字段已经存在（CHECK: text/vision/audio/multimodal/
embedding），但只能给模型设置单一 modality。不能表达 "minimax-m3 在 minimax
catalog 是 multimodal，在 volcengine-coding catalog 是 text-only"。

新增 `provider_models.modality` 列（per-(provider, raw_model_name) 粒度），覆盖
`models_canonical.modality`。SQL 用 `COALESCE(pm.modality, mc.modality, 'text')`。

文件：`deploy/sql/migrations/2026-07-15-modality-routing.sql` + `.down.sql`

### 2. Backfill

- volcengine-coding / volcano-tokenplan / volcano-normal catalog 下所有
  `provider_models` 默认 `modality='text'`（保守，逐个验证前不算 multimodal）
- volcengine-coding 上的 minimax-m2.x 和 minimax-m3 显式 `text`
- minimax catalog 上的 minimax-m3 显式 `multimodal`（直连 MiniMax-M3 官方文档
  支持 image_url + video_url）
- `models_canonical.minimax-m3.modality = 'multimodal'`
- `models_canonical.minimax-m2.x.modality = 'text'`（官方文档明确 M2.x 不支持
  image/video）

### 3. 路由 SQL 包含语义（provider/client.go:860）

修改前的 WHERE 子句：
```sql
AND ($3 = '' OR COALESCE(mc.modality, 'text') = $3)
```

修改后：
```sql
AND (
    $3 = ''
    OR $3 = 'text'                                -- 任何 provider 都能处理纯文本
    OR COALESCE(pm.modality, mc.modality, 'text') = $3
    OR ($3 = 'vision' AND COALESCE(pm.modality, mc.modality, 'text') = 'multimodal')
    OR ($3 = 'audio'  AND COALESCE(pm.modality, mc.modality, 'text') = 'multimodal')
)
```

包含语义：
- 纯文本 → 不限制 modality（任何 provider 都能处理）
- vision 请求 → 接受 vision / multimodal 的 provider
- audio 请求 → 接受 audio / multimodal 的 provider
- 单一 modality（video / embedding / multimodal）→ 精确匹配

### 4. Handler 检测 modality 并传递给路由

新增 `domains/streaming/modality_detect.go::detectRequestModality(bodyBytes)`
—— 解析请求体里的 image / audio / video blocks，决定 routing modality。

支持的形态：
- OpenAI Chat: `{type:"image_url"|"input_audio"|"video_url"|"file"|"input_file"}`
- Anthropic Messages: `{type:"image"|"audio"|"video", source:{type, media_type, data|url}}`
- Gemini native: `{parts:[{inlineData:{mimeType:"image/..."}}, ...]}`
- 嵌套在 tool_result.content 数组里的 media block
- data URI 形式的 image_url

优先级：video > audio > vision > text。混合 modality 返回最高优先级，
SQL 过滤会自动接受 multimodal provider。

修改 `domains/streaming/handler.go:1428` 和 `messages.go:412`：

```go
// 之前：
candidates, policy, err := h.provider.GetCandidates(...)

// 之后：
requestModality := detectRequestModality(bodyBytes)
candidates, policy, err := h.provider.GetCandidatesByModality(..., requestModality)
```

`providerResolver` interface 和 `routingProviderResolver`（cmd/gateway）接口
都加了 `GetCandidatesByModality` 方法。

### 5. Candidate struct 加 Modality 字段

`provider.Candidate.Modality` 新增字段（`pm.modality` COALESCE 后的结果），
用于诊断和后续可能的日志分析。

## 影响范围

- **修复**：vision/audio/video 请求不再被路由到 volcengine-coding catalog
  下的 text-only 候选，避免 "不支持图像输入" 误报。
- **回归保护**：纯文本请求完全不变（modality="" 或 "text" 时 SQL 不过滤），
  任何历史路由行为都保持。
- **范围**：所有走 chat / messages endpoint 的请求都覆盖。

## 测试

新增 `domains/streaming/modality_detect_test.go`：22 个 case 覆盖：

| 场景 | 期望 modality |
|---|---|
| OpenAI Chat 纯文本 / array 只含 text | text |
| OpenAI image_url data URI / http URL | vision |
| Anthropic image base64 / url | vision |
| OpenAI input_audio | audio |
| Anthropic audio block | audio |
| OpenAI video_url | video |
| Anthropic video block | video |
| Gemini inlineData image/png | vision |
| Gemini inlineData audio/wav | audio |
| Gemini inlineData video/mp4 | video |
| image + audio 混合 | audio（优先级） |
| image + video 混合 | video |
| audio + video 混合 | video |
| OpenAI file block image MIME | vision |
| OpenAI file block pdf MIME | text（不假定 image） |
| 空 body / 无效 JSON | text |

`go test ./domains/streaming/...` 全 PASS（22/22）。
`go test ./...` 全 PASS（包内 + 全仓库）。
`go vet ./...` clean。

## 验证步骤

1. **数据库**：执行 `deploy/sql/migrations/2026-07-15-modality-routing.sql`，
   验证块会自动输出 volcengine-coding 行数、models_canonical.minimax-m3=
   multimodal、volcengine-coding 下的 minimax-m3=text。
2. **网关**：部署包含本任务的 Go 二进制，重启 gateway。
3. **端到端**：客户端发 `model=minimax-m3` + image 请求到网关，应被路由到
   provider_id=14（minimax 直连）而非 provider_id=34（volcano-tokenplan）。
   检查 `request_logs.upstream_provider_id` 列确认。

## 下一步建议

- 给 volcengine-coding 下的每个模型单独标注真实 modality（当前全部 text 偏保守）。
  火山方舟 Coding Plan 实际支持 GLM-4V、Doubao Seed 1.6 等视觉模型。
- 监控：发现 "no_candidate" 错误与 modality 相关的比例，作为新模型发现的输入。
- 把 `detectRequestModality` 提取到 internal/ir 包以便未来其他入口（Responses API）
  复用。