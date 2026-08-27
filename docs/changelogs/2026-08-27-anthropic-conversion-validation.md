# Anthropic Conversion Validation

## Summary

The 2026-08-27 request audit for `claude-sonnet-5` showed that the persisted request and outbound bodies retained the same 495600-byte payload, so the USRM v2 body store was not truncating the request. The audit also identified silent-loss paths in the OpenAI Chat to Anthropic legacy converter.

## Changes

- Preserve system messages whose content is an array of text blocks.
- Reject unsupported content block types instead of dropping them.
- Reject image blocks without a usable URL.
- Reject missing tool-call function objects.
- Reject malformed tool arguments and valid JSON values that are not objects.
- Add regression tests for malformed content and tool shapes.
- Persist client protocol, upstream protocol, and conversion status as explicit request-log metadata.

## Evidence

- 154 request detail endpoint returned HTTP 200 for the audited request.
- 252 `request_logs_bodies_hot` contained equal 495600-byte request and outbound bodies.
- The request had two messages, an approximately 455 KB system prompt, an 82-byte user prompt, and 11 tools.
- Related package tests pass: `domains/transformation/anthropic`, `internal/ir`, `domains/session/v2`, and `domains/ursm/v2/...`.

## Remaining Risk

The repository-wide test command is currently blocked by pre-existing compile and test failures in unrelated packages. The conversion fix must still pass the 245 deployment gate before any promotion to 154.

## Audit Follow-up

The follow-up audit confirmed that the branch also contains concurrent session changes in routing, summary, live-stream, and credential modules. Those changes were preserved and are listed by `git diff main...HEAD`; they were not reverted or folded into the conversion diagnosis. A merge-path regression test now verifies that protocol metadata survives request-log entry aggregation.
