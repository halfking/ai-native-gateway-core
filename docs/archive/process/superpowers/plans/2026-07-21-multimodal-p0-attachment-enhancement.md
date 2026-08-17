# Multimodal P0 + Attachment Enhancement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the confirmed multimodal routing, conversion, attachment metadata, usage, and billing gaps without URL rewriting or a full Media IR rewrite.

**Architecture:** Keep the existing IR and protocol adapters. Add a shared request-shape modality detector that understands `messages`, Gemini `contents`, and Responses `input`; extend existing attachment extraction with a generic media candidate model while preserving best-effort, no-rewrite behavior; thread existing multimodal usage fields through capture, request logs, telemetry, and `ChargeRequestMultimodal`.

**Tech Stack:** Go, JSON/`json.RawMessage`, PostgreSQL migrations/baseline SQL, existing `testify` tests, `go test`.

---

## Current implementation map and invariant

Before every task, inspect the listed current files and run the narrow test command for that package. Do not assume the prior audit is current. Preserve these invariants:

- no data URI → URL rewrite;
- attachment storage remains best-effort and fail-open;
- pure-text routing and legacy four-token billing remain behaviorally compatible;
- unsupported cross-protocol media must fail explicitly, never serialize as a block containing only `type`;
- do not modify unrelated working-tree changes.

## Task 1: Normalize request modality detection

**Files:**
- Create: `internal/ir/request_capability.go`
- Test: `internal/ir/request_capability_test.go`
- Modify: `domains/streaming/modality_detect.go`
- Test: `domains/streaming/modality_detect_test.go`

- [ ] **Step 1: Re-check current detectors and tests**

Read `domains/streaming/modality_detect.go`, `domains/streaming/modality_detect_test.go`, `internal/ir/detect.go`, and current Responses tests. Run:

```bash
go test ./domains/streaming -run 'Modality|CandidateModality|Responses' -count=1
```

Expected: current tests pass or existing failures are recorded before edits.

- [ ] **Step 2: Write failing capability tests**

Add tests for a capability result containing `HasImage`, `HasAudio`, `HasVideo`, `HasDocument`, and `PrimaryModality` for:

```json
{"input":[{"type":"message","role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}}]}]}
```

and mixed `input_image + input_file`, Gemini `inlineData`, Anthropic nested `tool_result.content`, and empty/plain text requests.

- [ ] **Step 3: Implement shared JSON capability scanning**

Implement an exported or package-internal function in `internal/ir/request_capability.go` that walks `messages`, `contents`, and `input`, recursively inspecting `content`, `parts`, `source`, `file`, `input_file`, `inlineData`, and `fileData`. Return the full set plus `PrimaryModality` using `video > audio > image > text`; map document-only requests to `text` for backward-compatible routing until SQL gains document capability.

- [ ] **Step 4: Delegate streaming detection to the shared scanner**

Change `detectRequestModality` to call the shared scanner rather than maintaining a second JSON walker. Preserve its existing return values and mixed-media priority.

- [ ] **Step 5: Run focused tests**

```bash
go test ./internal/ir -run 'RequestCapability|Gemini|ParseOpenAI' -count=1
go test ./domains/streaming -run 'Modality|CandidateModality' -count=1
```

Expected: PASS.

## Task 2: Make Responses media conversion explicit

**Files:**
- Modify: `domains/streaming/responses.go`
- Test: `domains/streaming/responses_audio_test.go`
- Test: `domains/streaming/responses_test.go`

- [ ] **Step 1: Re-check current conversion behavior**

Read `responsesRequestBody`, `convertResponsesToChatBody`, `convertResponsesInputItem`, and all current Responses audio tests. Run:

```bash
go test ./domains/streaming -run 'Responses' -count=1
```

- [ ] **Step 2: Add failing conversion tests**

Add tests asserting that Responses `input` content blocks become Chat blocks:

- `input_text` → `{type:"text", text:...}`;
- `input_image` with `image_url` or `file_id` → image-compatible block/document reference;
- `input_audio` → `{type:"input_audio", input_audio:{data,format}}`;
- `input_file` with `file_data`, `file_id`, or `filename` → `{type:"file", file:{...}}`;
- unsupported item shape returns an explicit conversion error instead of a generic empty user message.

- [ ] **Step 3: Change conversion API to return errors**

Change `convertResponsesToChatBody` to return `(map[string]any, error)` and update its only production caller and tests. Keep function-call/function-output handling unchanged. Add a small helper for Responses content blocks so unknown media is rejected with an error containing `unsupported_modality`.

- [ ] **Step 4: Route Responses using original capability result**

In `ResponsesHandler.ServeHTTP`, call the shared capability detector on `bodyBytes` before candidate resolution and pass its primary modality into the existing resolver path. Do not rely on `messages[]` after conversion.

- [ ] **Step 5: Run focused tests**

```bash
go test ./domains/streaming -run 'Responses' -count=1
```

Expected: PASS, including audio/file routing fixtures.

## Task 3: Harden Anthropic media conversion boundaries

**Files:**
- Modify: `internal/ir/serialize_anthropic.go`
- Test: `internal/ir/serialize_anthropic_test.go`
- Modify: `domains/transformation/anthropic/chat_to_anthropic.go`
- Test: `domains/transformation/anthropic/chat_to_anthropic_multimodal_test.go`
- Modify: `domains/streaming/messages.go`
- Test: `domains/streaming/anthropic_bridge_multimodal_test.go`

- [ ] **Step 1: Re-check current serializers and tests**

Read the Anthropic IR serializer switch, legacy Chat→Anthropic converter, Anthropic→Chat block converter, and current multimodal tests. Run:

```bash
go test ./internal/ir -run 'Anthropic|Multimodal|Image' -count=1
go test ./domains/transformation/anthropic -run 'Multimodal|Anthropic' -count=1
go test ./domains/streaming -run 'Anthropic.*Multimodal' -count=1
```

- [ ] **Step 2: Add failing tests for unsupported media and document preservation**

Assert that:

- document blocks preserve source type, MIME, data/URL, title/context;
- text + image/document sibling blocks preserve order;
- audio/video sent toward Anthropic produce a typed `unsupported_modality` error rather than `{"type":"audio"}` or `{"type":"video"}`;
- legacy OpenAI→Anthropic conversion preserves text/image and explicitly rejects input audio/video/file shapes it cannot represent.

- [ ] **Step 3: Add typed conversion error**

Introduce or reuse a small error type/code that callers can classify as `unsupported_modality`. Change IR Anthropic serialization to return an error when an `audio`/`video`/unrepresentable block has no valid Anthropic representation. Do not change valid image/document output.

- [ ] **Step 4: Update legacy converters**

Make `ConvertChatRequestToAnthropic` return the typed error for unsupported input media and add explicit document handling where the target shape is valid. Ensure `convertBlockMessage` keeps text and supported image/document siblings instead of falling through to an incomplete block.

- [ ] **Step 5: Run focused tests**

```bash
go test ./internal/ir -run 'Anthropic|Multimodal|Image' -count=1
go test ./domains/transformation/anthropic -run 'Multimodal|Anthropic' -count=1
go test ./domains/streaming -run 'Anthropic.*Multimodal' -count=1
```

Expected: PASS.

## Task 4: Extend attachment extraction and content validation

**Files:**
- Modify: `domains/attachments/extractor.go`
- Modify: `domains/attachments/storage.go`
- Modify: `domains/attachments/detect_content_type.go`
- Test: `domains/attachments/extractor_test.go`
- Test: `domains/attachments/storage_test.go`
- Test: `domains/attachments/detect_content_type_test.go`

- [ ] **Step 1: Re-check attachment initialization and call sites**

Read `domains/attachments/extractor.go`, `storage.go`, all storage backends, and the OpenAI/Anthropic call sites in `handler.go` and `messages.go`. Run:

```bash
go test ./domains/attachments/... -count=1
```

- [ ] **Step 2: Add failing extraction tests**

Add fixtures for OpenAI/Responses `input_audio`, `file/input_file`, Gemini `inlineData`, and Anthropic `document` base64. Assert metadata includes media type, decoded size, hash, source kind, message/block coordinates, and failure status without changing the request body.

- [ ] **Step 3: Introduce a generic attachment candidate model**

Keep existing public image methods for compatibility, but add an internal scanner that returns candidates with `Type`, `ContentType`, `DataURI/base64 payload`, `SourceKind`, `MessageIndex`, and `BlockIndex`. Route OpenAI, Anthropic, Gemini, and Responses body scanners through it.

- [ ] **Step 4: Add MIME and magic-byte validation**

Add a bounded content sniff step for common PNG/JPEG/GIF/WebP/PDF/MP3/WAV/MP4 signatures. Preserve declared MIME when valid; record `mime_mismatch` in `ErrorCode` and do not block forwarding. Keep the current max-size enforcement and backend abstraction.

- [ ] **Step 5: Keep storage fail-open and body unchanged**

Ensure extraction failures append metadata and return normally. Do not introduce URL rewriting or external uploader calls. If the existing storage method requires a data URI, add a generic storage entry point that accepts decoded bytes and content type while retaining `SaveBase64Image` as a compatibility wrapper.

- [ ] **Step 6: Run focused tests**

```bash
go test ./domains/attachments/... -count=1
```

Expected: PASS.

## Task 5: Thread multimodal usage through capture and billing

**Files:**
- Modify: `domains/hooks/audit/audit.go`
- Modify: `domains/streaming/handler.go`
- Modify: `domains/streaming/usage.go`
- Modify: `domains/hooks/observability/telemetry/client.go` only if current update signatures require it
- Test: `domains/hooks/audit/audit_test.go`
- Test: `domains/streaming/usage_doubao_test.go` or new usage tests
- Test: `domains/streaming/handler_test.go` or the nearest existing charge-path test

- [ ] **Step 1: Re-check current usage fields and charge path**

Read `UsageData`, `StreamCapture`, `SummaryAsMap`, `extractTokensFromResponseBody`, request-log assignment, and the MaAS call. Run:

```bash
go test ./domains/hooks/audit/... ./domains/streaming/... ./maas/... -run 'Usage|Audit|Charge|Multimodal' -count=1
```

- [ ] **Step 2: Add failing capture tests**

Assert `SummaryAsMap` exposes non-zero `reasoning_tokens`, `image_tokens`, `audio_tokens`, `video_tokens`, and `provider_tokens` when populated by a stream or response usage parser.

- [ ] **Step 3: Extend usage extraction without double counting**

Parse image/audio/video details from existing OpenAI/Gemini usage structures and preserve raw/provider fields. When only aggregate prompt/completion exists, leave modality-specific counters nil. Do not infer audio/video tokens from bytes or seconds.

- [ ] **Step 4: Populate request-log multimodal fields**

Copy the new summary values and non-stream response values into the existing request-log fields. Preserve zero/nil fallback semantics so estimator behavior for providers without usage remains unchanged.

- [ ] **Step 5: Switch the main MaAS charge call**

Build `maas.TokenUsage` from request-log prompt/completion/cache/image/audio/video fields and call `ChargeRequestMultimodal`. Keep `ChargeRequest` only for unchanged callers. Do not charge reasoning/provider tokens unless the existing rate model explicitly supports them.

- [ ] **Step 6: Run focused tests**

```bash
go test ./domains/hooks/audit/... ./domains/streaming/... ./maas/... -run 'Usage|Audit|Charge|Multimodal' -count=1
```

Expected: PASS.

## Task 6: Reconcile provider modality schema and routing query

**Files:**
- Inspect/Modify: `sql/migrations/startup/` with the next repository migration version
- Modify: `deploy/sql/objects/tables/provider_models.sql`
- Modify: `provider/client.go`
- Modify: `domains/streaming/models.go` if its query exposes modality
- Test: `provider/client_*modality*_test.go` or new focused SQL/query test
- Modify: `docs/changelogs/2026-07-15-modality-routing.md` only after schema truth is established

- [ ] **Step 1: Re-check schema sources before editing**

Search startup migrations, deploy migrations, baseline tables, views, and current production-schema assumptions:

```bash
rg -n 'provider_models.*modality|ALTER TABLE provider_models|COALESCE\(pm.modality|COALESCE\(mc.modality' sql deploy provider domains --glob '*.sql' --glob '*.go'
```

Record whether the column exists in each source. Do not change Go SQL until the migration/baseline decision is clear.

- [ ] **Step 2: Add failing schema/query coverage**

Add a schema assertion or SQL fixture showing provider-level modality overrides canonical modality, with canonical fallback when provider modality is NULL. Cover `vision`, `audio`, `video`, `multimodal`, and text behavior.

- [ ] **Step 3: Add migration and baseline only if absent**

If absent, add `provider_models.modality` with the repository's normal allowed values/default/check style, add the corresponding down migration, and update the baseline table definition. If already present in another authoritative migration, do not duplicate it; instead align all generated/deploy SQL sources.

- [ ] **Step 4: Update routing and candidate projection**

Use `COALESCE(pm.modality, mc.modality, 'text')` only after the column is deployable. Ensure video/multimodal inclusion semantics match the documented capability policy and candidate JSON exposes the effective modality.

- [ ] **Step 5: Run schema/query-focused checks**

```bash
go test ./provider/... ./domains/streaming/... -run 'Modality|Candidate' -count=1
git diff --check
```

Expected: PASS and no SQL formatting errors.

## Task 7: Synchronize documentation and add end-to-end regression coverage

**Files:**
- Modify: `docs/IR格式优化/04-协议与验证矩阵.md`
- Modify: `docs/IR格式优化/10-Provider-IR-Multimodal-Audit-2026-07-13.md`
- Modify: `docs/changelogs/2026-07-14-multimodal-attachment-pipeline-proposal.md`
- Modify: `docs/changelogs/2026-07-15-modality-routing.md`
- Create: `docs/multimodal/README.md`
- Test: existing end-to-end fixtures under `internal/ir/` and `domains/streaming/`

- [ ] **Step 1: Re-check documentation claims against final code**

Search each document for claims about Gemini, Anthropic extraction, provider modality, and billing. Update only claims disproven by the final implementation.

- [ ] **Step 2: Add end-to-end fixture coverage**

Add or extend fixtures for:

```text
Responses input_audio → route audio → Chat block
Gemini inlineData video → route video → IR media block
OpenAI image + text → Anthropic image + text
attachment extraction → metadata only, original body unchanged
usage details → request log fields → multimodal charge input
```

- [ ] **Step 3: Run package and repository tests**

```bash
go test ./domains/attachments/... ./domains/streaming/... ./internal/ir/... ./maas/... ./provider/...
go test ./...
git diff --check
```

Expected: affected packages pass; full suite either passes or reports pre-existing unrelated failures.

- [ ] **Step 4: Review final diff for scope**

Confirm no URL rewrite, external uploader, unrelated refactor, generated binary, or accidental modification of pre-existing user changes. Record any test failures in the final handoff.

## Verification checklist

- [ ] Every task began with a fresh implementation check.
- [ ] Responses `input[]` drives routing modality.
- [ ] Unsupported Anthropic media fails explicitly.
- [ ] Image/audio/video/document attachments produce metadata without body rewrite.
- [ ] MIME mismatch is observable and fail-open.
- [ ] Stream/non-stream multimodal usage reaches request logs.
- [ ] Main MaAS path calls `ChargeRequestMultimodal`.
- [ ] Provider modality schema and routing query agree.
- [ ] Docs no longer claim implemented features are merely planned.
- [ ] Focused tests and full test results are recorded.
