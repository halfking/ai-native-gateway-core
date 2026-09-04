package ir

import (
	"encoding/json"
	"testing"
)

// FuzzIRParsersNeverPanic exercises the public request/document parsers with
// arbitrary bytes. Invalid input may be rejected; it must not crash the process.
func FuzzIRParsersNeverPanic(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"claude-3-5-sonnet","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		[]byte(`{"model":"gpt-4o","input":"hi"}`),
		[]byte(`{"model":"gpt-4o","messages":null,"unknown":{"value":1}}`),
		[]byte(``),
		[]byte(`null`),
		[]byte(`{"model":`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		parsers := []func([]byte) (*InternalRequest, error){
			ParseOpenAI,
			ParseAnthropic,
			ParseGemini,
			ParseResponses,
			DecodeRequestDocument,
		}
		for _, parse := range parsers {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("parser panicked for %q: %v", data, recovered)
					}
				}()

				req, err := parse(data)
				if err != nil || req == nil {
					return
				}

				// A successful parse must remain serializable as JSON through the
				// document codec, even when protocol serialization is not supported.
				document, err := EncodeRequestDocument(req)
				if err != nil {
					t.Fatalf("successful parse cannot be encoded as request document: %v", err)
				}
				if !json.Valid(document) {
					t.Fatalf("request document is not valid JSON: %q", document)
				}
			}()
		}
	})
}

// FuzzIRStreamParsersNeverPanic covers the stream parser entry points. The
// event type is intentionally fixed for Anthropic because it is supplied by
// the SSE envelope rather than the event payload.
func FuzzIRStreamParsersNeverPanic(f *testing.F) {
	f.Add(`data: {"id":"chatcmpl-1","choices":[]}`)
	f.Add(`data: {"candidates":[]}`)
	f.Add(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)
	f.Add(`data: [DONE]`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, line string) {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("OpenAI stream parser panicked for %q: %v", line, recovered)
				}
			}()
			_, _ = ParseOpenAIStreamChunk(line)
		}()

		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Gemini stream parser panicked for %q: %v", line, recovered)
				}
			}()
			_, _ = ParseGeminiStreamChunk(line)
		}()

		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Anthropic stream parser panicked for %q: %v", line, recovered)
				}
			}()
			_, _ = ParseAnthropicStreamEvent("content_block_delta", []byte(line))
		}()
	})
}
