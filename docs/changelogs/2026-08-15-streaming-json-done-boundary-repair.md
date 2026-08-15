# Streaming JSON DONE Boundary Repair

## Problem

An upstream stream on 154 returned a valid OpenAI completion chunk immediately
followed by `[DONE].` in the same SSE data line. JSON consumers received one
invalid string and failed with a non-whitespace-character-after-JSON error.

## Changes

- Split only a complete JSON payload followed by `[DONE]` or `[DONE].` into
  separate `data:` frames.
- Preserve final unterminated SSE lines when `bufio.Reader` returns them with
  `io.EOF`.
- Apply the same OpenAI frame repair before JSON parsing in the OpenAI to
  Anthropic and OpenAI to Responses stream converters.
- Preserve the final unterminated event in Anthropic passthrough streams.
- Disable new digest-only request-log body writes, including historical false
  overrides, so audit records retain complete request and response JSON.

## Verification

- `go test ./domains/streaming -count=1`
- `go test ./domains/hooks/observability/telemetry -run 'Test(UpdateRequestLog|RequestBodiesSummaryEnabled|Sanitize)' -count=1`
- `go test ./...`
- `go vet ./...`
- `go build ./...`

## Rollback

Revert the resulting commit. The change is limited to stream framing and
telemetry body persistence; no schema or deployment configuration changes.
