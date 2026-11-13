# 多模态 modality 批判式审计与 inline 媒体计量根修（2026-11-14）

> 审计对象：gpt-6 系列图片支持修复（161e29507 本侧 + 9e4bf88c1 并行续修）。
> 结论：上轮修复只打开了**三道闸中的第一道**，且修在错误的推断层；本轮补齐写侧与第二道闸，并兑现缺失的真实验证腿。

## 一、审计定案

### A1（P1）写侧复发风险：上轮把修复加在了读侧展示层
- 上轮改的是 `catalog/display.go` 的 `inferModalityFromName`——那是**租户目录展示**的推断（`EffectiveModality`）。
- 请求时刻的模态门读的是 **DB 存储值**（`provider/modality_mismatch.go` 的 `modalityMismatchSQL` 直接取 `mc.modality`），不经展示层。
- 新行种值的唯一写侧权威是 `modelname.InferModality`（`discovery/discovery.go:751/954`、`discovery/canonical_match.go:166`、`bg/node_probe.go:3719`）——它的规则表**只有 gpt-5 没有 gpt-6** ⇒ 新发现的 gpt-6* 行仍会被种成 text（modality 列粘滞），错误复发。
- 红基线实证：`InferModality("gpt-6-astra") == "text"`（修复前）。
- **修复**：`modelname/modality_defaults.go` 增 `{"gpt-6", "multimodal", 1}`（挨 gpt-5，带事故注释）+ `modality_defaults_test.go` 5 条 gpt-6 用例 + gpt-4 纯文本回归护栏。变异（摘规则）→ 断言级红 → 还原绿。
- 注：`catalog/display.go` 与 `modelname` 是两份平行推断实现（既有设计），两边现已同口径（gpt-4o/gpt-5/gpt-6/minimax-m3）。

### A2（P1）第二道闸：直发带图请求必 422 `context_budget_uncountable`（与 modality、窗口均无关）
- 真实请求复现（211 生产，key id=255，2026-11-13 17:17）：模态门放行后 `executor failed: context_budget_exceeded: model gpt-6-astra: context_budget_uncountable: media or remote context requires a trusted token bound (actual=0 limit=0)`。
- 根因链：`internal/contextbudget/inspect.go` 把 `image_url`（含 `data:` URL）一律标 external；`budget.go` 对 external 且无 `ExternalInputKnown` 直接 Uncountable；而 **`ExternalInputTokens`/`ExternalInputKnown` 全仓没有任何生产填充点** ⇒ 任何直发带图/带音频请求全模型必 422。gpt-4o（multimodal、window=128000）同样过不了这道闸。
- 大会话（ZCode）能过是因为 r1052 的 Reduce 层**剥掉**媒体块后 body 无媒体——绕过而非修复。
- **辨析**：budget.go 自述的安全不变量是「unaccounted external references (**remote state the gateway cannot see**) fail closed」。`data:` URL / 内联 base64 的字节网关**逐字节可见**，校准估计器已有 blob 分支（≥4KiB 无空白串 bytes/5）可计量——把它与 remote 引用一起 fail-closed 是分类错误，不是安全语义。
- **修复**：`inspect.go` 重写为 `classifyExternal` 三分类（none / inline / remote）：
  - inline（可计量）：data: URL、Anthropic `source.type=base64`、Gemini `inlineData`、OpenAI `input_audio.data`、`document` base64。
  - remote（维持 fail-closed）：http(s) 媒体 URL、`file_id`、`fileData`、`previous_response_id`/`conversation`/`cachedContent`/`cache_id`、provider prompt 模板、`redacted_thinking`/`item_reference`/`encrypted_content`、web_search 族 tools、顶层 ad-hoc `images`/`attachments`/`files`、assistant `audio:{id}`。
  - `budget.go` 两处 Uncountable 短路改为 `info.remote`；inline 走校准估计器（blob 分支），已知窗口下照常执法、未知窗口 advisory。
  - 保守方向说明：blob 估计对照片高估 ~200 倍（bytes/5 vs 视觉 token 实价），只可能更严不可能漏；微小图片（<4KiB）走 chars/3.5。诚实边界：极小 PNG 可编码大像素图（纯色 10000×10000），其视觉 token 成本会低估——容量终审归上游（2026-11-10 既定哲学：上游 context_length 错误走恢复链）。
- 钉测：`TestInlineMediaIsMeteredNotUncountable`（6 协议形状 + blob 增长同形对比 + inline+remote 混合仍 Uncountable）；`TestUncountedExternalContextFailsClosed` 改判 2 例（document base64 / inlineData 移出）+ 新增 2 remote 形状（Anthropic source.url、input_image.file_id）。变异（分类退回旧 external 形状）→ 红 → 还原绿。

### A3（P0→已闭）验证腿缺失
- 上轮从未发过真实带图请求（`test-gpt6-image.sh` 用占位 key 且从未执行）。本轮闭环：真库铸临时 key（id=255，HMAC-SHA256 `LLM_GATEWAY_SECRET_KEY`，application 18/default）→ `https://llm.itestu.cn/v1/chat/completions` model=gpt-6-astra 带 1x1 PNG data URL：
  - 修复前（线上 3045）：**422** `context_budget_uncountable`（红复现）
  - 修复后（本轮部署）：**200**（绿验收，见 §四）→ key 撤销。

### A4 一次性 SQL 脚本治理
- `scripts/fix-gpt6-modality.sql`（161e29507）已被 `scripts/fix-multimodal-models-comprehensive.sql`（9e4bf88c1，并行会话）覆盖。不删除（并行侧已归档落库），改为头部标注取代关系与官方路径。
- 官方可复现路径 = `cmd/tools/backfill-modality`（按现行规则回填、只升级、尊重 `modality_source` 闸门、幂等）。

## 二、验证读数

| 项 | 命令/路径 | 结果 |
|---|---|---|
| modelname 门 | `go test ./modelname/` | ok（5 gpt-6 用例 + 护栏） |
| 变异 M1 | 摘 `{"gpt-6"}` 规则 | 红（gpt-6.1-terra 断言）→ 还原绿 |
| contextbudget 包 | `go test ./internal/contextbudget/` | ok |
| 变异 M2 | classifyExternal 退回旧 external 形状 | 红（inline 必 Uncountable）→ 还原绿 |
| sessioncontext（r1052 媒体面） | `go test ./internal/sessioncontext/` | ok |
| compression / executors / streaming / catalog | 全量 | ok（streaming 99s） |
| build / vet / gofmt | `go build ./...` 等 | 干净 |
| 真实带图请求（211） | 铸 key → POST data-URL 1x1 PNG → 撤 key | 修复前 422 → 修复后 200 |

## 三、遗留登记（裁决面）

1. **backfill-modality 全量回填不执行**：211 dry-run 12 候选（o1-mini/o1-preview/o1-pro/o3-pro/o3-mini-high/o4-mini-high/gpt-4-turbo-preview→vision；phi-4-multimodal-instruct→multimodal；tts-1→audio；embed-qa-4/embed-v3/text-embedding-001→embedding）。其中 `o1-mini`/`o3-mini-high` 按原厂文档疑似**纯文本**推理模型，现行 `{"o1-"/"o3-"/"o4-"→vision}` 规则（2026-08-09 作者）对其精度存疑 ⇒ 全量执行会写 2 个存疑值进路由面。**裁决归 owner**：核实 o1-mini/o3-mini vision 语义后修订规则表，再跑 `backfill-modality`。
2. **gpt-5/gpt-6 全系列 `context_window=NULL`**：inline 媒体在未知窗口下 advisory 放行（不阻断），大载荷仍受 `DefaultMaxJSONBytes` 字节帽与上游终审约束。给 gpt-6 系列补窗口值=数据补录（原厂窗口数归 owner/运营）。
3. **modality 双实现**（`catalog/display.go` 推断 vs `modelname.InferModality`）维持平行——本轮对齐 gpt-6，结构性收敛（单源）另案。
4. 154/245 共享 252 库：60 行 modality 已由 9e4bf88c1 修复在位；本轮代码（写侧规则 + inline 计量）随下一班列车上 154/245。

## 四、211 部署与活体验证

- 部署：蓝绿切流，版本 `2.5.9-<commit>-20261114-211`，readyz OK，公网版本身份对拍。
- 活体：key id=255 → gpt-6-astra 带图 200；随后 `UPDATE api_keys SET enabled=false, status='revoked'`。
- 对照：gpt-6-astra 纯文本请求 200（无回归）。
