package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

// sensenovaFirstChunk is the byte-for-byte first frame that Xcode's Coding
// Assistant choked on on 2026-10-01 15:37:25 (request
// eb0ffdebbb81203f1c3e7eb1d38cf30c, upstream token.sensenova.cn/v1).
// Captured from llm.kxpms.cn with an Xcode user-agent.
const sensenovaFirstChunk = `data: {"id":"8f8c35e900f04f95a6580b07dce55193","created":1790840245,"model":"glm-5.2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":""}],"request_id":"8f8c35e900f04f95a6580b07dce55193"}` + "\n"

// TestNormalizeStreamChunk_EmptyFinishReasonBecomesNull pins the OpenAI spec
// rule that a mid-stream chunk carries finish_reason: null, never "". Zhipu and
// every reseller that forwards its payload verbatim (SenseNova) send "", which
// strict decoders reject — Xcode dropped the whole response because of it.
func TestNormalizeStreamChunk_EmptyFinishReasonBecomesNull(t *testing.T) {
	n := NewNormalizer()
	out := n.NormalizeChunk([]byte(sensenovaFirstChunk), true)

	if !strings.Contains(string(out), `"finish_reason":null`) {
		t.Fatalf("empty finish_reason was not normalized to null:\n%s", out)
	}
	if strings.Contains(string(out), `"finish_reason":""`) {
		t.Fatalf("empty-string finish_reason still on the wire:\n%s", out)
	}
	if !strings.Contains(string(out), `"content":""`) {
		t.Fatalf("delta content was altered:\n%s", out)
	}
}

// TestNormalizeStreamChunk_NullFinishReasonStaysNull guards against the fix
// double-encoding an already-correct frame.
func TestNormalizeStreamChunk_NullFinishReasonStaysNull(t *testing.T) {
	in := `data: {"id":"x","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n"
	out := NewNormalizer().NormalizeChunk([]byte(in), true)
	if !strings.Contains(string(out), `"finish_reason":null`) {
		t.Fatalf("null finish_reason changed:\n%s", out)
	}
}

// TestNormalizeStreamChunk_RealFinishReasonSurvives makes sure the "" branch
// does not swallow the terminal reason.
func TestNormalizeStreamChunk_RealFinishReasonSurvives(t *testing.T) {
	in := `data: {"id":"x","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}` + "\n"
	out := NewNormalizer().NormalizeChunk([]byte(in), true)
	if !strings.Contains(string(out), `"finish_reason":"stop"`) {
		t.Fatalf("terminal finish_reason lost:\n%s", out)
	}
}

// TestNormalizeStreamChunk_VendorMappingStillApplies proves the new branch
// composes with the pre-existing vendor reason mapping.
func TestNormalizeStreamChunk_VendorMappingStillApplies(t *testing.T) {
	in := `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"LENGTH"}]}` + "\n"
	out := NewNormalizer().NormalizeChunk([]byte(in), true)
	if !strings.Contains(string(out), `"finish_reason":"length"`) {
		t.Fatalf("LENGTH was not mapped to length:\n%s", out)
	}
}

// TestStripSensenovaFieldsBody_StripsResellerTags covers the second half of the
// Xcode failure: the sensenova catalog code had no sanitizer at all, so the
// bare top-level `request_id` Zhipu emits reached the client verbatim.
func TestStripSensenovaFieldsBody_StripsResellerTags(t *testing.T) {
	payload := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(sensenovaFirstChunk, "data: "), "\n"))
	out := string(StripSensenovaFieldsBody([]byte(payload)))

	if strings.Contains(out, `"request_id"`) {
		t.Fatalf("bare request_id survived sensenova sanitization:\n%s", out)
	}
	if !strings.Contains(out, `"object":"chat.completion.chunk"`) {
		t.Fatalf("sanitizer dropped spec fields:\n%s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("sanitized payload is not valid JSON: %v\n%s", err, out)
	}
}

// TestSensenovaFrameIsClientSafe is the end-to-end shape assertion: a frame that
// went through the real egress chain (vendor sanitizer, then the stream
// normalizer) must be free of every non-spec field and carry finish_reason as
// JSON null. The sanitizer is invoked through stripChunkFieldsForVendor because
// that is the only production entry point — it unwraps the `data: ` prefix
// before handing the payload to the stripper.
func TestSensenovaFrameIsClientSafe(t *testing.T) {
	line, code, _ := stripChunkFieldsForVendor(sensenovaFirstChunk, "sensenova", StripSensenovaFieldsBody)
	if code != 0 {
		t.Fatalf("unexpected vendor error code %d", code)
	}
	out := string(NewNormalizer().NormalizeChunk([]byte(line), true))
	if !strings.HasPrefix(out, "data: ") {
		t.Fatalf("normalizer dropped the SSE prefix:\n%s", out)
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(out, "data: "), "\n"))

	var chunk struct {
		RequestID *string `json:"request_id"`
		Object    string  `json:"object"`
		Choices   []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &chunk); err != nil {
		t.Fatalf("decoded frame is not valid JSON: %v\n%s", err, body)
	}
	if chunk.RequestID != nil {
		t.Fatalf("request_id still present: %q", *chunk.RequestID)
	}
	if chunk.Object != "chat.completion.chunk" {
		t.Fatalf("object = %q", chunk.Object)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(chunk.Choices))
	}
	// Strict OpenAI clients decode this as an optional enum; null is the only
	// legal "not finished yet" value.
	if chunk.Choices[0].FinishReason != nil {
		t.Fatalf("finish_reason = %q, want JSON null", *chunk.Choices[0].FinishReason)
	}
}

// TestSensenovaFrameIsClientSafe_EmptyCatalogCode is the shape of the real
// 2026-10-01 production path. The request was served by providers row
// `sensenova-jack` (id 33089), whose catalog_code is EMPTY, so
// cmd/gateway/main.go's catalog switch never selects a stripFn and the frame
// relies entirely on top-level field inference. A fix that only registered a
// "sensenova" case would have looked correct in review and changed nothing in
// production — this test is the regression guard for that.
func TestSensenovaFrameIsClientSafe_EmptyCatalogCode(t *testing.T) {
	line, code, _ := stripChunkFieldsForVendor(sensenovaFirstChunk, "", nil)
	if code != 0 {
		t.Fatalf("unexpected vendor error code %d", code)
	}
	out := string(NewNormalizer().NormalizeChunk([]byte(line), true))
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(out, "data: "), "\n"))

	var chunk struct {
		RequestID *string `json:"request_id"`
		Choices   []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &chunk); err != nil {
		t.Fatalf("decoded frame is not valid JSON: %v\n%s", err, body)
	}
	if chunk.RequestID != nil {
		t.Fatalf("request_id survived inference-only sanitization: %q", *chunk.RequestID)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].FinishReason != nil {
		t.Fatalf("finish_reason is not JSON null: %s", body)
	}
}

// TestZhipuResponseBodyStillKeepsRequestID pins the divergence between the two
// Zhipu surfaces. The response body keeps `request_id` (an existing contract,
// see strip_realworld_test.go); the streaming chunk drops it. These two field
// lists look like duplicates and will eventually be "unified" by someone — this
// test is what makes that unification fail loudly instead of silently regressing
// either Xcode compatibility or the documented response contract.
func TestZhipuResponseBodyStillKeepsRequestID(t *testing.T) {
	body := `{"id":"glm-1","object":"chat.completion","model":"glm-4-plus","request_id":"public-req","zhipu_request_id":"internal","choices":[]}`
	out := string(StripZhipuFieldsBody([]byte(body)))
	if !strings.Contains(out, `"request_id":"public-req"`) {
		t.Fatalf("response body lost its public request_id:\n%s", out)
	}
	if strings.Contains(out, "zhipu_request_id") {
		t.Fatalf("response body kept the internal zhipu_request_id:\n%s", out)
	}

	chunkOut := string(StripZhipuStreamChunkFieldsBody([]byte(strings.TrimSpace(strings.TrimPrefix(sensenovaFirstChunk, "data: ")))))
	if strings.Contains(chunkOut, `"request_id"`) {
		t.Fatalf("stream chunk kept request_id:\n%s", chunkOut)
	}
}

// TestResolveStreamVendor_BareRequestIDIsZhipuFamily pins the inference that
// makes an unregistered catalog code safe: a bare request_id now resolves to the
// Zhipu policy instead of silently passing through.
func TestResolveStreamVendor_BareRequestIDIsZhipuFamily(t *testing.T) {
	payload := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(sensenovaFirstChunk, "data: "), "\n"))
	vendor, _ := resolveStreamVendor(payload, "", nil)
	if vendor != "zhipu" {
		t.Fatalf("vendor = %q, want zhipu", vendor)
	}
}

// TestResolveStreamVendor_VendorRequestIDWins guards the ordering: a Doubao
// payload also carries a bare request_id, and must keep the Doubao policy.
func TestResolveStreamVendor_VendorRequestIDWins(t *testing.T) {
	payload := `{"doubao_request_id":"d","request_id":"r","choices":[]}`
	vendor, _ := resolveStreamVendor(payload, "", nil)
	if vendor != "doubao" {
		t.Fatalf("vendor = %q, want doubao", vendor)
	}
}
