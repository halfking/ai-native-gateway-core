# IR Fidelity Audit

## Changes

- Preserve unknown Anthropic top-level request fields in `InternalRequest.Extensions`.
- Restore same-protocol Anthropic extensions without leaking them across protocols.
- Preserve unknown OpenAI content blocks and Anthropic document blocks during round trips.
- Add OpenAI `parallel_tool_calls` parsing and serialization.
- Add regression tests for extensions, multimodal blocks, and `parallel_tool_calls`.

## Verification

- `go test ./...`
- `go build ./...`
- `go vet ./...`
