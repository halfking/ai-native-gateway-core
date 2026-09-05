## Agent 1 — IR/Session/Protocol audit

Scope: commits since 2026-08-31 06:00 +0800 that touch `internal/ir/`,
`domains/session/v2/`, `domains/transformation/`, `adapter/unified/`, and
`cmd/gateway/main.go` (protocol/dispatch paths only), plus the always-critical
IR parser/serializer/stream/types files.

Recent activity in scope (18 commits):
- `0dacb1d22` feat(bg): Redis token-bucket leader election for MV refresh
- `bfc556ea9` 修正自检的参数 修正大模型上下文超找处理的bug (context-overflow handler)
- `be6ca0987` feat(session): session turn digests
- `f49c06ff8` fix(session): ClientType parameter in session aggregator mocks
- `e441940cd` docs(handoff): 632 MV shipped
- `a17ccd8a6` fix(streaming): WriteFrame goroutine semaphore
- `48dd51fa7` fix(gateway): self-calls follow own listen port
- `16068c099` fix(gateway): mv refresher traffic-only blue-green
- `7b748269e` fix(migration): tenant_id type in MV creation
- `3505cb49e` fix(admin): MV audit fixes
- `24da16e1c` feat(admin): routing analytics MV
- `5ca8d0005` fix(audit): P1-5 IR top-level json tags
- `d9b289256` fix(audit): attachment cleanup P0
- `493f7123a` fix(ursm): require PostgreSQL for Shadow mode
- `9ca6ab96c` fix(audit): isolated-PG verify + attachment cleanup
- `72e84edcb` fix(audit): 24h audit follow-up (P1 storage/embeddata/deadcode)
- `3b3497935` fix(audit): installer/outbox/reader/cache gaps (turn_reader lastN fix)

Audit goals A–G evaluated against current code; findings below.

---

## P0

### P0-1. Fabricated Gemini tool-use ID collides on parallel calls to the same function
- **file:line**: `internal/ir/parse_gemini.go:216` (also `parse_gemini_stream.go:189`)
- **call chain**:
  1. Inbound Gemini generateContent request with two `parts[].functionCall` items both named `lookup`
  2. `parseGeminiContents` loops over parts; for each `FunctionCall` it sets `ToolUse.ID = "gemini_call_" + fc.Name` (`parse_gemini.go:216`)
  3. Same loop path runs on stream chunks (`parse_gemini_stream.go:189`); same deterministic synthesis
  4. Both tool_use blocks (and any matching function_response) carry the *same* synthetic id
  5. Upstream serializer cross-walks `ToolUseID` ↔ `ToolCallID`; two calls collapse onto one pair and the second is silently mis-paired
- **evidence**:
  ```go
  // parse_gemini.go:215-218
  ToolUse: &ToolUse{
      ID:    "gemini_call_" + fc.Name, // name-based -> collisions
      Name:  fc.Name,
      Input: fc.Args,
  },
  ```
  Test `integration_roundtrip_test.go:477-478` confirms the id is treated as canonical (both ir1 and ir2 share `gemini_call_lookup`); there is no per-call disambiguator (no index, no counter, no hash of `Input`).
- **reproduction (input -> expected -> actual)**:
  - input: `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"k":"A"}}},{"functionCall":{"name":"lookup","args":{"k":"B"}}}]},{"role":"function","parts":[{"functionResponse":{"name":"lookup","response":{"k":"A"}},{"functionResponse":{"name":"lookup","response":{"k":"B"}}}]}]}`
  - expected: two distinct `tool_use.id` (e.g. `gemini_call_lookup_0` / `gemini_call_lookup_1`) and a matching `tool_result.tool_use_id` for each
  - actual: both `tool_use.id` and both `tool_result.tool_use_id` are `gemini_call_lookup`; `validateToolCallIntegrity` (`serialize_openai.go:513`) reports orphans or mis-pairs and may reject the request
- **test suggestion**:
  - file: `internal/ir/parse_gemini_test.go`
  - test name: `TestParseGemini_ParallelFunctionCalls_ProduceUniqueIDs`
  - assertion: parse a request with two `functionCall` parts sharing the same `name` but different `args`; assert `ir.Messages[*].Content[*].ToolUse.ID` are distinct (e.g. contain an index suffix). Repeat for the streaming path with two `candidates[0].content.parts[].functionCall` events.

---

### P0-2. `lastN <= 0` silently coerces to 10 in `TurnReader.LoadChain` — changes caller semantics
- **file:line**: `domains/session/v2/turn_reader.go:68-70`
- **call chain**:
  1. `domains/hooks/compression/session_cache.go:624` calls `c.turnReader.LoadChain(ctx, tenantID, gwSessionID, 10)` — passes 10 as default
  2. `domains/session/v2/outbound_builder.go:63` calls `b.reader.LoadChain(ctx, tenantID, sessionID, lastN)`; docstring `outbound_builder.go:45` says `lastN: 加载最近 N 轮（0 = 全部）` — **the public contract is `0` = all**
  3. `LoadChain` then silently rewrites `lastN <= 0 → 10` (`turn_reader.go:68-70`), so a caller asking for `0` (meaning "all") actually gets the last 10 turns
- **evidence**:
  ```go
  // turn_reader.go:64-70
  func (r *TurnReader) LoadChain(ctx context.Context, tenantID, sessionID string, lastN int) ([]Message, error) {
      ...
      if lastN <= 0 {
          lastN = 10
      }
  ```
  Combined with `outbound_builder.go:45` doc and `outbound_builder.go:177` ("use tokenest helper"), the contract drift is real: the doc says `0` means "all" but the implementation says `0` means "10".
- **reproduction (input -> expected -> actual)**:
  - input: `LoadChain(ctx, "t", "s", 0)` against a session with 47 turns
  - expected (per `outbound_builder.go:45`): all 47 turns returned, each turn's request+response stitched
  - actual: only the last 10 turns returned (slice clamped at `turns[len(turns)-10:]`); a 37-turn slice of conversation is silently dropped from the cold-start cache
- **test suggestion**:
  - file: `domains/session/v2/turn_reader_test.go`
  - test name: `TestTurnReader_LoadChain_LastNZeroReturnsAllTurns`
  - assertion: seed a fixture with 47 turns; call `LoadChain(ctx, "t", "s", 0)`; require `len(msgs)` equals the cumulative message count across all 47 turns, *not* 10×(some-N).
  - companion regression: `TestOutboundBuilder_BuildFromDeltas_LastNZero_ContractRoundTrip` asserting the same at the builder level.

---

## P1

### P1-1. InternalRequest `Class` and `DueAt` are intentionally JSON-skipped, but downstream writers still consult them by reflection — risk of divergence
- **file:line**: `internal/ir/types.go:201,204` (tag `-`); also referenced in `serialize_openai.go:182`, `serialize_anthropic.go:240-294`, `serialize_responses.go:59-170`
- **call chain**:
  1. `internal/ir/types.go:201` `Class RequestClass \`json:"-"\`` — explicit non-marshaling
  2. `serialize_openai.go:182` `restoreExtensions(out, req, ProtocolOpenAIChat)` then `serialize_openai.go:197` `json.Marshal(out)` never touches `req.Class`/`req.DueAt`
  3. The session writer (`domains/session/v2/session_writer_v2.go`) reads `req.Class`/`req.DueAt` via reflection when stamping the session row; if the IR is round-tripped via Postgres JSONB without those fields, downstream reload loses the scheduling marker
- **evidence**: the IR top-level json tags added in `5ca8d0005` deliberately skipped `Class` and `DueAt` (per the commit message). However, the IR is persisted to `session_bodies_*` columns as JSON via `safeJSONMarshal` (`bodies_writer.go:204`); on a reload (cold start) the reader cannot rebuild `req.Class` from the JSON row. That is fine *for transport* but breaks `domains/session/v2/session_writer_v2.go` if it tries to recover scheduling metadata from a reloaded IR.
- **reproduction (input -> expected -> actual)**:
  - input: a scheduled request (`Class=scheduled, DueAt=T+5m`) routed, IR written to `session_bodies_hot`
  - expected on cold start: reloading `req` from JSON row yields the same `Class`/`DueAt`
  - actual: `Class`/`DueAt` are zero-value; the session aggregator stamps the row as immediate (no reschedule row)
- **test suggestion**:
  - file: `internal/ir/ir_json_trip_test.go` (already added in `5ca8d0005`)
  - test name: `TestInternalRequest_RoundTrip_ClassAndDueAtPreserved`
  - assertion: marshal/unmarshal an `InternalRequest{Class:scheduled, DueAt:T}`, verify the in-memory struct preserves both. If non-preservation is the chosen contract, add an explicit `TestClassIsNotPersisted_DocumentedBehavior` test with a comment pointer to `domains/session/v2/session_writer_v2.go` stating that scheduling must be re-read from `session_turns.scheduled_at`, not from the IR row.

### P1-2. `tool_choice:"any"` -> OpenAI serialization correctness verified, but Anthropic `tool_choice` acceptance never validated end-to-end
- **file:line**: `internal/ir/serialize_anthropic.go:84-86` (calls `serializeAnthropicToolChoice`); `internal/ir/serialize_anthropic.go:412` / `:583` (calls `GetProviderFieldConfig(targetProvider, modelName).ToolResultIDField`)
- **call chain**:
  1. An inbound Anthropic request carries `tool_choice: {type:"any"}`
  2. `parseAnthropicToolChoice` (`parse_anthropic.go:571`) preserves it on the IR as `Type:"any"`
  3. Serializing to OpenAI: `serialize_openai.go:494` maps `"any"->"required"` (correct)
  4. Serializing to Anthropic: `serializeAnthropic.go:84` keeps the literal `{type:"any"}` — Anthropic actually accepts this on the wire, but the **same IR** serialized with `TargetProvider="minimax"` still writes `tool_use_id` vs `tool_call_id` correctly (`provider_field_mapping.go:30-38`); however, no regression test exists for the `nvidia + minimax-model` routing branch (`provider_field_mapping.go:60-71`)
- **evidence**: no test in `provider_field_mapping_test.go` covers `catalogCode="nvidia"`, `modelName="minimaxai/foo"` returning `tool_call_id`. The unit tests in that file (per `git grep`) only cover direct MiniMax.
- **reproduction (input -> expected -> actual)**:
  - input: `GetProviderFieldConfig("nvidia", "minimaxai/claude-haiku-4-5")`
  - expected: `ProviderFieldConfig{ToolResultIDField:"tool_call_id"}`
  - actual: not covered by automated tests (likely correct, but no guard)
- **test suggestion**:
  - file: `internal/ir/provider_field_mapping_test.go`
  - test name: `TestGetProviderFieldConfig_NVIDIA_RoutesMiniMaxModels`
  - assertion: pass `("nvidia","minimaxai/anything")`, `("nvidia","minimax-anything")`, `("nvidia","minimax")` and expect `ToolResultIDField == "tool_call_id"`; pass `("nvidia","meta/llama-3")` and expect `"tool_use_id"`. Same matrix for `UsesToolCallID` (`provider_field_mapping.go:82`).

### P1-3. SSE resume via `ApprovalResumeHandler` exists, but there is no end-to-end test for "interrupted mid-tool-call" -> "resumed without orphan IDs"
- **file:line**: `cmd/gateway/approval_integration.go:45-72`; `domains/streaming/tool_call_validator_integration_test.go:65-128` (interrupt detection); `domains/session/v2/turn_writer.go` (turn append on resume)
- **call chain**:
  1. Upstream Anthropic SSE emits `content_block_start{type:tool_use, id:"toolu_01"}` + partial `input_json_delta`
  2. Connection dies -> `attempt_commit_gate` classifies as `incomplete_tool_call_interrupted` (`tool_call_validator_integration_test.go:126-128`)
  3. `ApprovalResumeHandler` (`cmd/gateway/approval_integration.go:45`) is invoked on the human-approved retry path; `NewPendingStoreResponder` re-emits from `pending.Store`
  4. The pending store's snapshot was taken *before* `content_block_stop` for the tool_use block — but no test asserts that re-emission includes the partial arguments AND that the tool_use_id is the same as the prior attempt
- **evidence**: `tool_call_validator_integration_test.go:107-128` exercises only the *classification*, not the *resume* path. `cmd/gateway/approval_integration.go` wires the resume but no integration test connects the two.
- **reproduction (input -> expected -> actual)**:
  - input: stream interrupted at `partial_json="{\"loc\":\"San Fra"` for `toolu_01`; client resumes via approval
  - expected: resumed request carries `[{"type":"tool_use","id":"toolu_01","name":"lookup","input":{"loc":"San Fra"}}]` and validation passes (id matches, args well-formed partial)
  - actual (to verify on staging): unknown — `audit-restore-legacy-sessions` covers session resume but not this approval-replay path
- **test suggestion**:
  - file: `domains/streaming/tool_call_validator_resume_test.go` (new)
  - test name: `TestApprovalResumeHandler_PreservesToolUseIDAcrossInterruption`
  - assertion: build an interrupted stream snapshot with partial JSON for `toolu_01`; invoke `ApprovalResumeHandler` (with a `pending.Store` mock + recording `ChatHandler`); assert the resumed upstream call carries the same `tool_use.id` and the accumulated `input` JSON up to the interruption point.

---

## P2

### P2-1. `internal/ir/response.go:48` `Usage ResponseUsage` has no JSON `omitempty` — empty usage always marshals as `{"usage":{...}}` with zeros, inflating body size and risking client rejection
- file:line: `internal/ir/response.go:48` and `internal/ir/stream.go:28`
- note: client SDKs (OpenAI Python) crash on `usage.completion_tokens=0` for tool_use responses that never write `output_tokens`. Worth measuring in staging before changing the tag.

### P2-2. `internal/ir/response.go:25` `Created int64` overflow risk on platforms where clock returns nanoseconds — minor; only relevant if upstream streams a non-Unix timestamp (none observed)

### P2-3. `internal/ir/serialize_anthropic.go:252-262` reports OpenAI-only-field losses but emits *no body change* when `service_tier="priority"` arrives from an OpenAI client; Anthropic has no service tier, but `Priority` is silently dropped. Already audited, but no end-to-end test for `service_tier + Anthropic`.

### P2-4. `internal/ir/serialize_anthropic.go:412` `GetProviderFieldConfig(targetProvider, modelName).ToolResultIDField` is called once per tool role message; cache it on the caller side. Trivial perf, mentioned for completeness.

### P2-5. `domains/session/v2/session_aggregator.go:222-224` `total_tokens = EXCLUDED.total_tokens + public.sessions.total_tokens` — accumulator uses `int`; safe today (max `2^31-1`) but a 100k-turn session will overflow. Should bind to `bigint`.

### P2-6. `domains/session/v2/bodies_writer.go:204-216` `safeJSONMarshal` rejects "NaN/Inf floats" by design, but the parser path (`Message.UnmarshalJSON`) doesn't reject such values; if a NaN ever enters the DB column via the parser, the next writer panics on `safeJSONMarshal` returning `errors.New("marshal JSON produced invalid output")` — and the rejection bubbles up as `insert bodies: …` causing the whole turn commit to abort.

### P2-7. `internal/ir/parse_anthropic.go:286-293` reads `tool_use_id` first then `tool_call_id` (MiniMax compat) but only at the message level; the *content block* path in `parseAnthropicContentBlocks` (`parse_anthropic.go:335-340`) does the same; coverage is consistent, but a separate `parseAnthropicContentBlock` single-item helper at `parse_anthropic.go:431-434` does the same fall-through — three duplicate lookup sites. Worth a single `extractToolResultID(map[string]any) string` helper.

### P2-8. `internal/ir/serialize_responses.go:565` `SanitizeResponsesFunctionName` does not preserve case; a tool named `MyTool_v2` round-trips as `MyTool_v2` (good), but if a downstream provider treats names case-insensitively, collisions are possible. Out-of-scope for IR; flagged for cross-provider testing.

---

## Already-fixed (skipped)

These items were addressed in commits since 2026-08-31 06:00 and require no further work in this audit:

- **IR top-level json tags** — `5ca8d0005` (`internal/ir/{types,response,stream}.go`): all exported fields now have explicit `json:` tags. Round-tripping via `encoding/json` is safe. New tests in `internal/ir/ir_json_trip_test.go`.
- **Turn reader unified view** — `3b3497935` (`domains/session/v2/turn_reader.go:39-77`): both `LoadLatestOutbound` and `LoadChain` now read `public.session_bodies_unified` (hot + monthly partitions), fixing the legacy/`session_bodies` schema drift that would have left cold-start caches reading pre-partition data only.
- **Session aggregator idempotency mocks** — `f49c06ff8`: test mocks carry `ClientType`; not a runtime concern.
- **Session aggregator upsert claim** — `72e84edcb` (in `bodies_writer.go`) plus `e441940cd` docs: aggregate claim semantics hardened in `session_aggregator.go:140-197`; covered by `session_aggregate_outbox_reaper_integration_test.go` (added in `9ca6ab96c`).
- **WriteFrame semaphore** — `a17ccd8a6` (`domains/streaming/...`): caps goroutine count at `2*capacity`; `ErrWriteSlotsExhausted` returned on overflow, admin projection still updates. Not in IR/session path but referenced from `cmd/gateway/main.go` wiring.

---

## Verification gaps

Items that the audit cannot conclude without staging validation; mark "需 staging 验证":

1. **P1-1 (Class/DueAt JSON-skip)**: needs confirmation that `domains/session/v2/session_writer_v2.go` reads `Class` from in-memory IR *before* persistence (not after a JSON round-trip). The current code path appears correct, but no test covers a reload-then-rewrite sequence.
2. **P1-2 (NVIDIA->MiniMax routing)**: unit-test gap noted; verifying in staging requires hitting NVIDIA's relay for `minimaxai/*` and inspecting the upstream wire shape.
3. **P1-3 (interrupted-mid-tool-call resume)**: no staging evidence that the resume path preserves `tool_use.id` exactly. Recommend a 5-minute chaos drill: kill the upstream connection at 70% of an `input_json_delta` stream and verify the resumed attempt reuses the same id.
4. **P2-5 (total_tokens overflow)**: needs DBA confirmation that `sessions.total_tokens` is `bigint`, not `int`. The Go type is `int` (`session_aggregator.go:311`), which is platform-dependent; PostgreSQL DDL should match.
5. **P2-6 (NaN/Inf in DB columns)**: needs a probe that injects a NaN into a session row and confirms the writer fails fast with a clear error rather than corrupting the table.

---

## Summary table

| ID | Severity | File:Line | Summary | Test suggestion |
|----|----------|-----------|---------|-----------------|
| P0-1 | P0 | `internal/ir/parse_gemini.go:216`; `parse_gemini_stream.go:189` | Gemini tool_use IDs fabricated from function name only - parallel calls collide | `TestParseGemini_ParallelFunctionCalls_ProduceUniqueIDs` in `internal/ir/parse_gemini_test.go` |
| P0-2 | P0 | `domains/session/v2/turn_reader.go:68-70` | `LoadChain(0)` silently coerces to 10, contradicting `outbound_builder.go:45` doc | `TestTurnReader_LoadChain_LastNZeroReturnsAllTurns` in `domains/session/v2/turn_reader_test.go` |
| P1-1 | P1 | `internal/ir/types.go:201,204` | `Class`/`DueAt` json-skipped - break if IR is round-tripped via JSONB | `TestInternalRequest_RoundTrip_ClassAndDueAtPreserved` (or explicit non-persistence test) in `internal/ir/ir_json_trip_test.go` |
| P1-2 | P1 | `internal/ir/provider_field_mapping.go:60-71` | NVIDIA->MiniMax model-name routing lacks test coverage | `TestGetProviderFieldConfig_NVIDIA_RoutesMiniMaxModels` in `internal/ir/provider_field_mapping_test.go` |
| P1-3 | P1 | `cmd/gateway/approval_integration.go:45-72`; `domains/streaming/tool_call_validator_integration_test.go:107-128` | No end-to-end test that approval-resume preserves tool_use.id across interruption | `TestApprovalResumeHandler_PreservesToolUseIDAcrossInterruption` in `domains/streaming/tool_call_validator_resume_test.go` (new) |
| P2-1 | P2 | `internal/ir/response.go:48`; `internal/ir/stream.go:28` | Usage struct lacks `omitempty` - empty usage always emitted | 1-line: tag `json:"usage,omitempty"` (measure client impact first) |
| P2-2 | P2 | `internal/ir/response.go:25` | `Created int64` - minor overflow risk on bad upstream timestamps | 1-line: clamp at parse time |
| P2-3 | P2 | `internal/ir/serialize_anthropic.go:252-262` | OpenAI-only losses reported but no body change for `service_tier` on Anthropic | 1-line: end-to-end test |
| P2-4 | P2 | `internal/ir/serialize_anthropic.go:412,583,934` | `GetProviderFieldConfig` called per-message - cache miss pattern | 1-line: hoist call |
| P2-5 | P2 | `domains/session/v2/session_aggregator.go:222-224,311` | `total_tokens int` will overflow at 2^31 - schema DDL needs `bigint` | 1-line: confirm DBA schema |
| P2-6 | P2 | `domains/session/v2/bodies_writer.go:204-216`; `UnmarshalJSON` at `Message.UnmarshalJSON` | NaN/Inf in JSONB row aborts next writer; no symmetric guard on parse | 1-line: probe NaN injection in staging |
| P2-7 | P2 | `internal/ir/parse_anthropic.go:286-293,335-340,431-434` | Three duplicate `tool_use_id/tool_call_id` fallback sites | 1-line: extract helper |
| P2-8 | P2 | `internal/ir/serialize_responses.go:565` | `SanitizeResponsesFunctionName` case-preserving; downstream case-insensitive collisions possible | 1-line: cross-provider probe |
