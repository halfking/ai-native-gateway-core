# Gemini Stream SSE Fix

## Problem

The Gemini native handler captured the complete ChatHandler response in an
`httptest.ResponseRecorder` before converting it. ChatHandler could flush
OpenAI SSE chunks internally, but those flushes were invisible to the Gemini
client until the upstream stream ended.

## Fix

Streaming Gemini requests now use a real `http.ResponseWriter` wrapper. The
wrapper buffers incomplete lines, converts each complete OpenAI SSE chunk
through the IR stream serializer, and forwards `Flush` calls to the client.
Duplicate `[DONE]` markers are collapsed to one terminal event. Responses
returned after an HTTP error status bypass conversion so the original
diagnostic body is preserved.

## Verification

- `go test ./domains/streaming ./internal/ir -count=1`
- `go vet ./domains/streaming ./internal/ir`
- `go build ./cmd/gateway`
- `go run scripts/audit-ir-e2e/main.go`
