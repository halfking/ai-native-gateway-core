# Gemini 结构化 tool result 保真 — 交接文档

**项目**: llm-gateway-go（IR 协议转换层）
**日期**: 2026-08-16
**负责人**: ZCode (GLM-5.2, Z.ai)
**状态**: 核心功能完成并合并 `main` ✅；剩余为评估性扩展任务

---

## 1. 背景与目标

Gemini 的 `functionResponse.response` 是一个**任意 JSON 值**（对象/数组/标量/null），
而 IR 层原本只把它压成纯文本塞进 `ToolResult.Content`，导致：

- 同协议 Gemini→Gemini 中继时，结构化结果退化为 `{"result": "<json 字符串>"}` 文本包装；
- 会话持久化往返后，原生结构彻底丢失。

目标：让 Gemini 原生结构化响应在 **解析 → IR → 序列化 → 会话持久化 → 中继**
全链路中保持原生 JSON 形状，同时不影响 OpenAI/Anthropic 等跨协议的既有文本语义。

---

## 2. 已完成工作（已合并 origin/main）

| Commit | 说明 |
|--------|------|
| `b6c905c46` | `fix(gemini): preserve structured function responses` — IR 载体 + 解析/序列化 + 值矩阵测试 |
| `87ed88d40` | `fix(ir): persist native Gemini response across session round-trip` — 持久化保真修复 |
| `8a84665d8` | `test(session): cover Gemini structured response relay round-trip` — 完整中继链路测试 |

### 关键设计

- **IR 载体**：`internal/ir/types.go` 的 `ToolResult.GeminiResponse json.RawMessage`，
  标签 `json:"gemini_response,omitempty"`。
  - `nil` = 线缆缺失 `response`（序列化时走 `{"result": "<text>"}` 回退）；
  - 非 nil 字节（含字面量 `null`） = 显式携带的响应，序列化时原样写入 `functionResponse.response`。
- **解析**（`parse_gemini.go:226`）：仅当 `fr.Response != nil` 时拷贝原始 JSON；
  始终保留文本 `Content` 块以兼容跨协议。
- **序列化**（`serialize_gemini.go:344`）：`GeminiResponse != nil` 时直接作为 `response`
  值；否则回退 `{"result": extractTextFromContent(...)}`。
- **持久化**：会话适配器 `domains/session/v2/ir_message_adapter.go` 经 `json.Marshal`
  往返 `ToolResult`，`json` 标签保证 `GeminiResponse` 不丢失（这是 `87ed88d40` 修复的核心）。

### 测试覆盖

- `internal/ir/gemini_test.go`：值矩阵（object/array/string/number/boolean/null/empty/omitted）+ 旧文本回退。
- `internal/ir/integration_roundtrip_test.go`：同协议 round-trip 稳定。
- `internal/ir/serialize_end_to_end_test.go`：跨协议（OpenAI/Anthropic）不暴露 `GeminiResponse`。
- `domains/session/v2/ir_message_adapter_lossless_test.go`：持久化往返保留 + 完整中继链路端到端。

---

## 3. 审计结论（2026-08-16）

采用多子代理并行模式规划（实际因网络受限改为本地只读审计），重点核查：

1. **nil vs 显式 null 语义** — 一致。`parse` 仅在 `Response != nil` 写入；`serialize`
   用 `!= nil` 判定，字面 `null` 原样输出。端到端测试已断言。
2. **跨协议不泄露** — OpenAI/Anthropic 序列化器只读 `Content` 文本，不引用
   `GeminiResponse`；该字段仅在 `ToolResult` 整体 `json.Marshal`（会话持久化）时出现，
   协议序列化器全为手写 `map[string]any`，不会带上 `gemini_response` 键。
3. **持久化往返一致** — `87ed88d40` 修复后，两个 `session/v2` 测试覆盖。
4. **panic / 无效 JSON 风险** — `json.RawMessage` 由 `encoding/json` 直接写，
   `null` 字节合法；`nil` 时走回退不触碰该字段。无风险。
5. **跨包路径** — transformation 中继、streaming 均通过 `Serialize*` 搬运，
   Gemini→Gemini 同协议走 `GeminiResponse`，跨协议走文本，符合设计。

**结论：无需要修正的代码缺陷。** 测试覆盖充分，无 flaky/非确定性断言
（值比较用 `reflect.DeepEqual` 或 `assert.JSONEq`，不依赖 map 顺序）。

---

## 4. 当前仓库状态（交接时点）

- `HEAD/origin/main` = `5b6ec115a`（已包含全部 3 个 Gemini 提交）。
- 工作区存在 **与本次任务无关** 的他人未提交改动，请勿提交/修改/丢弃：
  - `VERSION`, `version.json`, `web/public/menu-config.json`, `web/public/version.json`
  - `docs/db-changelog.md`
- 现有 git stash（8+ 个）与 linked worktree 均保留，未清理。

---

## 5. 剩余任务（建议后续方向）

1. **OpenAI/Anthropic 原生结构化 tool result 评估**
   目前跨协议 Gemini→OpenAI/Anthropic 仍走纯文本 `Content`。若这些 provider
   也有原生结构化响应语义，可按同类模式（`RawMessage` 载体 + 序列化器优先读取）扩展。
   注意：OpenAI `tool` 消息的 `content`  historically 为字符串，需先确认上游是否接受结构化。

2. **持久化字段历史兼容文档**
   `gemini_response` 为 `omitempty` 新字段，旧会话行恢复时为 `nil` 并走文本回退
   （符合预期）。建议在 `docs/` 补一条 ADR/注释说明，避免未来误判为数据丢失。

3. **（可选）流式 tool result 保真**
   本任务覆盖请求侧（`functionResponse`）。若 Gemini 流式也回传结构化 tool 结果，
   需同步核查 `response.go` 的 `SerializeGeminiResponse` 路径（当前仅处理文本 content）。

---

## 6. 新会话可复制提示词（多子代理并行模式）

> 复制以下内容到新会话即可继续。建议用 2–3 个并行子代理分工：
> - 子代理 A：调研 OpenAI/Anthropic 上游是否支持结构化 tool result（读官方 spec + 仓库现有 serializer）。
> - 子代理 B：起草 `gemini_response` 持久化字段的 ADR/兼容文档。
> - 子代理 C（主代理）：汇总、设计 IR 扩展方案、写测试、rebase 推送。

```
请读取本会话上下文，继续 llm-gateway-go 的 IR 工具结果保真工作。
当前 main/origin/main 为 5b6ec115a（已含 Gemini 结构化响应 3 个提交：
b6c905c46, 87ed88d40, 8a84665d8）。

工作区含与本次任务无关的他人未提交改动
（VERSION, version.json, web/public/*, docs/db-changelog.md），请勿提交、修改或丢弃。
现有 git stash（8+ 个）与 linked worktree 均保留。

已完成：Gemini native functionResponse.response 结构化无损全链路
（解析→IR.GeminiResponse→序列化原样输出→会话持久化→中继），含全值矩阵、
跨协议隔离、会话往返、完整中继链路测试。审计结论：无代码缺陷。

剩余任务（评估性，非阻塞）：
(1) 评估 OpenAI/Anthropic 是否需同类原生结构化 tool result 保留（先确认上游 spec）；
(2) 为 gemini_response 持久化字段补历史兼容 ADR 文档；
(3)（可选）流式 tool result 保真（核查 response.go 的 SerializeGeminiResponse）。

执行约束：
- 只改 internal/ir 与 domains/session/v2 内与 Gemini 任务相关的文件；
- 精准提交单一/最小文件集合，rebase 到最新 origin/main 后推送；
- 不清理现有 stash 与 linked worktree，不触碰工作区中他人改动；
- 每个改动配定向测试 + 全仓定向 go test；使用 gofmt、git diff --check。
- 建议多子代理并行：A 调研上游 spec，B 起草 ADR 文档，C 汇总设计+实现+推送。
```
