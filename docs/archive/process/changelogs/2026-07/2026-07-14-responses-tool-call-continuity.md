# Responses API tool-call continuity

## Change

The `/v1/responses` adapter now translates Responses API `function_call` and
`function_call_output` input items into Chat Completions messages while keeping
the same call ID. This preserves the assistant tool-call to tool-result
relationship for upstream providers such as gpt-5.6-luna.

## Verification

- `go test ./domains/streaming ./internal/ir ./domains/transformation`
- `go test ./...`
