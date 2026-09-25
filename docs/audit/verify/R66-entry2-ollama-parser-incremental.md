# R66 Entry #2 — Audit Verify Report

**Subject:** R66 audit entry #2 — Ollama parser incremental correctness
**Original claim:** "Ollama's native NDJSON `message.content` is incremental (delta), surfaced via `StreamDelta`, not via the unused `StreamChunk.CumulativeContent`."
**Verifier:** standalone audit-verify agent (Rule 50 / R66)
**Date:** 2026-09-25

---

## TL;DR — Verdict

**R66 entry #2 is TRUTHFUL. All sub-claims verified against code and tests. No regression.**

| Sub-claim                                                                                  | Status   | Evidence |
|---------------------------------------------------------------------------------------------|----------|----------|
| (a) `message.content` → `StreamDelta.Content` (per-frame delta, `DeltaType="text"`)         | ✅ TRUE  | [parse_ollama_stream.go:114-127](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream.go#L114-L127) |
| (b) `message.thinking` → `StreamDelta.ReasoningContent` (`DeltaType="reasoning"`)           | ✅ TRUE  | [parse_ollama_stream.go:129-137](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream.go#L129-L137) |
| (c) `StreamChunk.CumulativeContent` stays empty (RESERVED per R52)                          | ✅ TRUE  | [stream.go:63-66](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/stream.go#L63-L66) + test lines 41-43, 77-79, 117-119 |
| (d) Done chunk uses top-level `done: true` (NOT `[DONE]` sentinel)                          | ✅ TRUE  | [parse_ollama_stream.go:140-156](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream.go#L140-L156) + test line 177-194 |
| (e) `prompt_eval_count`/`eval_count` → `StreamUsage` (PromptTokens/CompletionTokens/Total)  | ✅ TRUE  | [parse_ollama_stream.go:148-154](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream.go#L148-L154) + test lines 137-142, 192-194 |
| (f) `ProtocolOllamaChat = "ollama-chat"` constant registered                                | ✅ TRUE  | [types.go:42](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/types.go#L42) |
| (g) Multi-frame incremental contract pinned by test                                         | ✅ TRUE  | [parse_ollama_stream_test.go:151-173](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream_test.go#L151-L173) |

---

## Upstream protocol cross-check (WebFetch 2026-09-25)

[WebFetch of upstream api.md](https://github.com/ollama/ollama/blob/main/docs/api.md#generate-a-chat-completion) confirms:

1. `message.content` per NDJSON frame is **delta bytes only** — Ollama streams incremental token fragments, not cumulative snapshots. Confirmed by reading Ollama's "Generate a chat completion" streaming example.
2. `message.thinking` (Ollama 0.5+ DeepSeek-R1 family) carries per-frame thinking delta — separate channel from `content`.
3. Final frame carries `"done": true` plus `done_reason`, `prompt_eval_count`, `eval_count` — no `[DONE]` sentinel (SSE-style), so the parser correctly relies on top-level `done` boolean.

R66 audit's WebFetch claim is **consistent with upstream reality**.

---

## Field-by-field cross-check

### `StreamDelta` struct ([stream.go:114-131](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/stream.go#L114-L131))

```go
type StreamDelta struct {
    Role            string
    Content         string   // ← per-frame text delta (R66 entry#2 channel)
    ReasoningContent string   // ← per-frame thinking delta (R66 entry#2 channel)
    ToolCalls       []ToolCall
    DeltaType       string   // ← "text" / "reasoning" / "tool_call"
}
```

✅ All four fields used by Ollama parser. **No unused fields** — consistent with the audit claim.

### `StreamChunk.CumulativeContent` (R52 RESERVED)

Test file pins the contract three times:

- **Line 41-43** (delta case): `chunk.CumulativeContent != ""` → test fails with "(incremental contract — full text is reconstructed by concatenating Delta.Content)"
- **Line 77-79** (reasoning case): same invariant on reasoning-only frames
- **Line 117-119** (done+content same-line): terminal-line content also surfaces as delta, not cumulative

The audit's claim that `CumulativeContent` is "unused / RESERVED" matches the test corpus. **R52 invariant upheld.**

### `ProtocolOllamaChat` registration

[types.go:26-44](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/types.go#L26-L44) declares:

```go
const (
    ProtocolOpenAIChat        = "openai-chat"
    ProtocolAnthropicMessages = "anthropic-messages"
    ProtocolOpenAIResponses   = "openai-responses"
    ProtocolOllamaChat        = "ollama-chat"
)
```

✅ Constant exists. The protocol is wired (further wiring is verified separately — see [response_protocols.go](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/response_protocols.go) for SSE/serializer dispatch, which is out of R66 entry#2 scope).

### Edge cases (in scope of R66 entry#2 "No regression")

- **Interim no-op frame** (`{"done": false, "message": {"content": ""}}`): parser returns `(nil, nil)` per [parse_ollama_stream.go:67-69](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream.go#L67-L69) + test `TestParseOllamaStreamChunk_InterimNoopFrame` (line 85-94). Correctly suppresses zero-Typed chunks.
- **Done + content same line** (`done: true` with `message.content`): parser emits **two chunks** — delta first, then Done. Test `TestParseOllamaStreamChunk_DoneWithContent` (line 100-143) pins this. ✅ No chunk-type overwrite bug.
- **Pure done line** (no content): exactly one Done chunk. Test line 177-194. ✅
- **Error frame** (`{"error": "..."}`): maps to `ChunkTypeError`. Test line 197-213. ✅
- **Empty line**: returns error. Test line 215-222. ✅
- **`done_reason` lifecycle signals** (`load`, `unload`): collapsed to `"stop"` so a model reload isn't mistaken for content-filter stop. Test line 226-242. ✅

---

## Additional finding (not in R66 entry#2 — informational only)

The parser function comment ([parse_ollama_stream.go:61-67](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream.go#L61-L67)) states:

> "Returns `(nil, nil)` for lines that carry neither error, content, thinking, nor done (e.g. Ollama's interim ping frames)"

This refers to **valid JSON frames with no surfaceable content**, not blank NDJSON lines. Blank lines (`""`, `"   \n"`) correctly return an error per [parse_ollama_stream.go:67-69](file:///Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/internal/ir/parse_ollama_stream.go#L67-L69) and test line 215-222. The contract is internally consistent — no contradiction to flag.

---

## Audit verdict

R66 entry #2 is **truthful** in every sub-claim:
- Incremental content surfaces via `StreamDelta.Content` (per-frame), not via `StreamChunk.CumulativeContent`.
- Reasoning surfaces via `StreamDelta.ReasoningContent`.
- Done uses top-level `done: true` (no `[DONE]` sentinel).
- Usage maps from `prompt_eval_count`/`eval_count`.
- Protocol constant `ProtocolOllamaChat = "ollama-chat"` exists.

**No regression. No drift. R52 invariant upheld.**

Recommend: **No follow-up action required for R66 entry #2.** Move to next pending R66 audit entry (or whichever the user prioritises next).

---

## Provenance

- **Files inspected:** `internal/ir/parse_ollama_stream.go`, `internal/ir/parse_ollama_stream_test.go`, `internal/ir/stream.go`, `internal/ir/types.go`, `internal/ir/response_protocols.go`, `internal/ir/serialize_ollama.go`.
- **External evidence:** WebFetch of Ollama upstream `docs/api.md` (Generate a chat completion section) on 2026-09-25.
- **No code modified.** This is a read-only verification.

---

## Post-audit note（2026-09-25 R66 批判式审计）

本报告随原提交落库时，该提交 message 声称了三项本 diff 不包含的"健化"
（expectThinkingDone/handleBareContent 桥、chunkLines→handleBuf 复用、
stream.go RecycleIR 缓冲回收）及三条对应测试。逐项 grep 核实均不存在，
属提交信息与实际改动不符；真实交付面即本报告所述：契约反转（累计→增量）
+ 测试断言翻面 + TestParseOllamaStreamChunk_MultiFrameIncremental 端到端
钉桩 + stream.go 字段注释勘误。

处置：两提交均未推送，安全 amend 将 message 重写为实际交付面（无远端
影响）；同步修正 serialize_ollama.go §3 注释中 thinking "(cumulative)"
的过时表述（流式为逐帧增量，非流式为单值），代码行为零变更。全部 ollama
测试实跑复绿（go test ./internal/ir/... -run Ollama → PASS）。
