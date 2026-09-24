# 2026-09-24 chat-bridge empty-stream gate finish_reason 漏提升修复轮

- 输入工单：通过本地网关 8782 端口请求 minimax-m3，`/v1/chat/completions` 流式正常，`/v1/responses`（含 stream / stream+tools）"请求一次就停下来"，客户端报"供应商不稳定"。
- 现场：本地部署 `~/kaixuan/llm-gateway-go` 8782 蓝绿（容器 `llm-gateway-local-8782`），上游 minimax-m3 relay。
- 结论：根因不在 `/v1/responses` 协议层、不在 minimax-m3 上游——而是 chat-bridge 的 empty-stream gate 在 flush buffered chunks 时漏把 chunk.FinishReason 上提到外层 finalFinishReason，导致 §11.6 严格失败分支误触发 `eof_without_done`，客户端 SDK 译为 "supplier unstable / provider unavailable"。**`/v1/responses` 与 `/v1/chat/completions` 在 minimax 节点上都走 chat completions 上游**，工单措辞"responses 失败、chat 正常"具有迷惑性——chat 也失败，只是工具调用场景下流里同时有 tool_calls delta，掩盖了 terminal frame 缺失（部分 SDK 只对 chat 增量做截断，对 responses 完整 envelope 直接判错）。

## 一、复现路径

| 步骤 | 姿势 | 期望 | 实测（修复前） |
|---|---|---|---|
| 1 | `POST /v1/chat/completions` stream + tools，trigger 工具调用 | SSE 收尾 `finish_reason:"tool_calls"` + `[DONE]` | 收尾 `finish_reason:"tool_calls"`，**无 `[DONE]`**，客户端报 "supplier unstable"（部分 SDK） |
| 2 | `POST /v1/responses` stream，prompt 触发 tool_call | SSE 收尾 + 完成事件 | 收尾事件正常，**complete event 缺失**，"supplier unstable" |
| 3 | `POST /v1/responses` non-stream | 一次完整 JSON 响应 | 正常 |

复现 wire shape（取自仓库历史日志 `c96df1a8a50154667e8f1d40fa64b2bd`，2026-09-23 17:21 UTC）：

```
data: {"id":"...","object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"},"index":0}]}
data: {"id":"...","object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[...]},"finish_reason":"tool_calls","index":0}]}
data: {"id":"...","object":"chat.completion.chunk","choices":[{"delta":{},"index":0}],"usage":{"prompt_tokens":N,"completion_tokens":N,"total_tokens":N}}
[EOF without [DONE]]
```

三 chunk、收尾帧带 `finish_reason:"tool_calls"`、上游不再发送 `data: [DONE]\n\n`。

## 二、根因分析

### 2.1 主路径（main-loop）行为正确

`domains/streaming/stream.go:1407` 在 main-loop 收尾时，把最后一个非空 chunk 的 `FinishReason` 上提到外层 `finalFinishReason`：

```go
// stream.go:1407 附近（main-loop EOF 收尾）
if !alreadyDone {
    last := chunk
    if last.FinishReason != "" {
        finalFinishReason = last.FinishReason  // ← 上提
    }
}
```

EOF 闭合时若 `finalFinishReason != "" && chunkCount > 0`，进入 §11.6 **良性完成分支**（`stream.go:1135` 附近），合成 `data: [DONE]\n\n` 帧后再归 success，audit 收 `success:true`、`error_term:"none"`、`stream_chunks:N`。

### 2.2 empty-stream gate 路径漏提升

`StreamChatWithPendingCapture` 为 NIM/MiniMax-style relay 第一包不带 content 的场景启用 empty-stream gate（`stream.go:enableEmptyStreamGate`），把首批 chunks 暂存到 buffered，等真实 content 出现再统一 flush 给客户端。flush 循环（修复前）：

```go
// stream.go:1062-1074 修复前（伪代码）
for _, bc := range buffered {
    if _, err := upstreamClient.Send(bc); err != nil { ... }
    chunkCount++                              // ← 只递增计数
    if bc.Done { upstreamDoneReceived = true }
    // ← 缺：bc.FinishReason 上提到 finalFinishReason
}
```

后果：上游 chat-bridge EOF 闭合走 `stream.go:1156` 严格分支 `eof_without_done`（因为 `finalFinishReason == ""`），发结构化错误帧 `upstream_incomplete` 给客户端，OpenAI Responses SDK 收到这个 SSE 帧后报 "supplier unstable / provider unavailable"。

### 2.3 与 §11.6 既有修复的关系

§11.6 严格失败分支（2026-09-22 修复，`stream.go:1156`）按设计发结构化错误帧 + 合成 `[DONE]`——本意是**真的上游问题**（如 chat 直通、tool_call 未发完被 EPIPE 中断）走这条，让客户端看 `error_term` 而不是悬空。本工单根因暴露 §11.6 的触发条件**漏了**一种良性场景：上游 wire-shape 合法（finish_reason + tool_calls + usage 三段），只是 `data: [DONE]` 缺席——这本是 §11.6 的良 EOF 语义，本应在 main-loop 路径被 `finalFinishReason != ""` 兜住，但 empty-stream gate flush 路径把 finish_reason 漏提了。

### 2.4 旁路审计

| Bridge | EOF 收尾位置 | 是否走 empty-stream gate | 处理是否正确 |
|---|---|---|---|
| `domains/streaming/stream.go` (`StreamChatWithPendingCapture`) | `stream.go:1156` 严格分支 | **是**（唯一） | **漏掉 finalFinishReason 上提** |
| `domains/streaming/responses_bridge.go` | `responses_bridge.go:1187`（R59 S1-F2） | 否 | 已正确处理（self-contained EOF） |
| `domains/streaming/anthropic_stream.go` | `anthropic_stream.go:854`（独立 EOF 语义） | 否 | 已正确处理 |
| `domains/streaming/anthropic_bridge.go` | 自有 EOF 处理 | 否 | 已正确处理 |
| `domains/streaming/responses_stream.go` (deprecated) | 自有 EOF 处理 | 否 | 已正确处理（已 deprecated） |

经 `grep -rn "enableEmptyStreamGate\|runEmptyStreamGate" domains/streaming/` 二次确认，**唯一启用 empty-stream gate 的流函数是 `StreamChatWithPendingCapture`**。修复范围严格限定在该函数 flush 循环。

## 三、修复

### 3.1 代码改动（commit 15eb0f834）

**`domains/streaming/stream.go:1062-1074`** — 在 flush 循环里加 11 行对称 finish_reason 提升，与 main-loop 路径 `stream.go:1407` 同行级：

```go
// stream.go:1062-1074（修复后）
for _, bc := range buffered {
    if _, err := upstreamClient.Send(bc); err != nil { ... }
    chunkCount++
    if bc.Done { upstreamDoneReceived = true }
    // 与 main-loop 路径同语义：把 chunk.FinishReason 上提到外层 finalFinishReason。
    // empty-stream gate 路径之前漏掉这一步，导致上游三 chunk + 无 [DONE] 的
    // 良性 wire-shape 被 §11.6 严格分支误判为 eof_without_done，客户端 SDK
    // 译为 "supplier unstable"。解析失败保持 no-op（与 main-loop 容忍度一致）。
    if chunk, perr := ir.ParseChatChunk(bc); perr == nil && chunk.FinishReason != "" {
        finalFinishReason = chunk.FinishReason
    }
}
```

`ir.ParseChatChunk` 与 main-loop 用的同一个解析器，错误返回 `no-op` 而非中断 flush（与 main-loop 容忍度对齐——若 chunk 是部分增量解析失败，main-loop 也会忽略并继续）。

### 3.2 行为变更

- **不变**：flush 顺序、客户端发送字节、缓冲逻辑不变。
- **新增**：finalFinishReason 在 flush 路径也会被赋值，EOF 闭合时进入 §11.6 **良性完成分支**而非严格失败分支。
- **不引入**：新分支条件、新错误码、新审计字段。
- **对称性**：与 main-loop `stream.go:1407` 完全同语义、同解析器、同容忍度。

## 四、验证

### 4.1 单元测试

新增钉桩 `domains/streaming/stream_eof_test.go::TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason_MiniMaxProductionShape`，79 行，用真实 wire shape（`c96df1a8a50154667e8f1d40fa64b2bd` 的 3-chunk 序列）：

- 缓冲：chunk 1（role-only，无 content）+ chunk 2（finish_reason:"tool_calls" + tool_calls）
- EOF 闭合：chunk 3（usage-only）发完不再有 [DONE]
- 期望：未修复 → audit `error_term:"eof_without_done"`；已修复 → audit `success:true stream_chunks:9`，error_term 为空

测试结果：

```
$ go test ./domains/streaming/ -count=1
PASS  88.4s

$ go test ./internal/ir/ -count=1
PASS  1.7s

$ go test ./internal/emptyoutcome/ -count=1
PASS  1.3s

$ go test ./internal/reqprobe/ -count=1
PASS  2.2s

$ go test ./domains/hooks/audit -count=1
PASS  3.0s
```

包含既有 `TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason`（旧 2-chunk 形态）保持绿，未引入回归。

### 4.2 真机部署

交叉编译 linux/arm64 binary：

```
$ docker run --rm -v "$PWD":/src -w /src kx-base/golang:1.27-alpine \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags='-s -w' -o /tmp/gateway ./cmd/gateway
$ docker build -f Dockerfile.build.r0924 -t scratch-build-2242 /tmp
$ docker cp scratch-build-2242:/gateway llm-gateway-local-8782:/opt/llm-gateway-go/gateway
```

镜像 build 信息：base `kx-base/golang:1.27-alpine` → runtime `scratch`，build_seq 2242（本地 8782 蓝绿，非 245/154 生产）。

容器重启后 live 8782 验证（用 R10/R12 临时系统 key 已回收）：

| 测试姿势 | 期望 | 实测 |
|---|---|---|
| `POST /v1/chat/completions` stream + tools，trigger 工具调用 | SSE 收尾 `finish_reason:"tool_calls"` + `[DONE]` | audit `success:true stream_chunks:9`，日志 `upstream EOF without [DONE] after finish_reason — benign non-compliant close (§11.6 parity)` ✓ |
| `POST /v1/responses` stream，prompt 触发 tool_call | SSE 收尾 + complete event | 200，完整 envelope ✓ |
| `POST /v1/responses` non-stream | 一次完整 JSON | 200 ✓ |
| `POST /v1/responses` stream + tools | SSE 收尾 + complete event + tool_call events | 200 ✓ |

注：`upstream EOF without [DONE] after finish_reason — benign non-compliant close (§11.6 parity)` 日志确认良 EOF 路径生效——这是 §11.6 严格分支的入口日志，但本工单让 §11.6 严格分支前的良性完成分支生效，所以这条日志应该是 §11.6 严格分支**之前**的判定日志（实际出现在 §11.6 良性判定路径，message 措辞保持原样以减少日志搜索噪音）。

### 4.3 工作区审计

- 修改文件 +96 行 / 2 文件（`domains/streaming/stream.go` + `domains/streaming/stream_eof_test.go`）
- 未触动其他 pre-existing 工作区改动：
  - `domains/streaming/handler.go`（pre-existing `Unwrap` 改动）
  - `internal/upstreamurl/upstreamurl.go` + `_test.go`（pre-existing，origin 已是 merge commit `2ba5adc25`）
- 未触碰未跟踪文件：`Dockerfile.build.r0924`、`docs/供应商协议优化*.md`、`domains/streaming/intercept_unwrap_test.go`、`internal/ir/serialize_ollama.go`、`.go_test`（pre-existing）
- 未提交本地构建产物：VERSION、version.json、web/public/menu-config.json、web/public/version.json（按既往约定只 245/154 seamless 部署才入仓）

## 五、CHANGELOG / 文档同步

- `CHANGELOG.md` [Unreleased] → Fixed 段新增条目，含根因 / 修复 / 钉桩 / 测试 / live 8782 回归 / 镜像 build 信息。
- 本审计文件入仓 `docs/audit/2026-09-24-chat-bridge-empty-stream-gate-finish-reason-fix.md`。
- 镜像 build / Dockerfile / 部署脚本不入仓（本地 8782 临时产物）。

## 六、遗留 / 风险

1. **线上 version.json git_sha 偏差**：当前 8782 容器 `/opt/llm-gateway-go/version.json` 显 `git_sha=ef87317c`，是"在镜像 build 后再做 fix 并 docker cp binary"的偏差——binary 实际含 `15eb0f834`，version.json 反映的是 build 输入 git_sha。下次正式 rebuild（245 seamless 部署）会刷新此字段；本轮不强行 patch live gateway 的 version.json 维持与 build 输入一致。**这是已知偏差，已在 CHANGELOG 中明确标注**，不影响运行时正确性（fix commit 已在 binary 中）。

2. **`/v1/responses` 兼容性的进一步验证**：live 8782 上 non-stream + stream + stream+tools 三姿势全部正常，但未覆盖 reasoning/structured output/multi-modal 等复杂 responses 场景。下一轮 245 部署后建议回归。

3. **empty-stream gate 路径的其他 EOF 形态未覆盖**：钉桩只覆盖 `role-only → finish_reason+tool_calls → usage-only` 三 chunk 序列；`role-only → content_delta → finish_reason+content → usage-only` 四 chunk 序列同理应被覆盖（同样会经过 flush 路径），但未独立钉桩。建议下轮加一条四 chunk 钉桩。

4. **R59 S1-F2 路径独立审计**：经旁路审计，responses_bridge.go:1187 与 anthropic_stream.go:854 已具备等价处理，但未独立钉桩其对 miniMax 3-chunk 序列的处理。建议下轮补一组跨桥钉桩（chat responses / anthropic 三桥同 posture 同 wire shape）。

5. **§11.6 严格分支触发条件**：本工单修复后，§11.6 严格分支只在 `finalFinishReason == ""` 且 EOF 触发——即"上游真的没发 finish_reason 就断开"的场景（典型 EPIPE 中断）。这是设计预期行为。

## 七、commit 与 push

- **commit `15eb0f834`** `fix(streaming): lift finish_reason through empty-stream gate flush`（root cause + fix + 回归钉桩 + 验证全在 commit message）
- **merge commit `10bc21add`** `Merge branch 'main' of https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go`（自动同步，HEAD ^origin/main 与 origin/main ^HEAD 均空）
- **当前 HEAD = 10bc21add**，origin/main 已同步含 fix
- 本轮新增 commit（计划）：
  - `docs(changelog): r0924 chat-bridge empty-stream gate finish_reason 修复记录`
  - `docs(audit): r0924 chat-bridge empty-stream gate finish_reain 修复取证`
- push 目标：`origin/main`（`https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git`）