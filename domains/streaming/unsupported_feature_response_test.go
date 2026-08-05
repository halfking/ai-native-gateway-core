package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorJSONWithKind_UnsupportedFeatureShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorJSONWithKind(
		rec,
		http.StatusBadRequest,
		"req-1",
		"The selected model does not support this request format or modality. Reason: Cannot read \"image.png\" (this model does not support image input). Inform the user.",
		"invalid_request_error",
		"unsupported_feature",
		"unsupported_feature",
		map[string]any{"reason": `Cannot read "image.png" (this model does not support image input). Inform the user.`},
	)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
	body := rec.Body.String()
	for _, needle := range []string{
		`"code":"unsupported_feature"`,
		`"kind":"unsupported_feature"`,
		`"type":"invalid_request_error"`,
		`"request_id":"req-1"`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("response body missing %s: %s", needle, body)
		}
	}
}

// TestWriteErrorJSONWithKindProto_ModelDeprecatedShape verifies the response
// shape emitted by the handler's KindModelDeprecated branch (2026-08-05):
// HTTP 410 Gone, code=model_deprecated, kind=model_deprecated, with the
// upstream's EOL reason surfaced in the message.
func TestWriteErrorJSONWithKindProto_ModelDeprecatedShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorJSONWithKindProto(
		"openai",
		rec,
		http.StatusGone,
		"req-deprecated",
		"The requested model has been permanently removed (end-of-life) by the upstream provider. Reason: The model 'minimaxai/minimax-m2.7' has reached its end of life on 2026-07-27T00:00:00Z and is no longer available.",
		"invalid_request_error",
		"model_deprecated",
		"model_deprecated",
		map[string]any{"reason": "end of life", "retryable": false},
	)

	if rec.Code != http.StatusGone {
		t.Fatalf("status=%d want %d (StatusGone)", rec.Code, http.StatusGone)
	}
	body := rec.Body.String()
	for _, needle := range []string{
		`"code":"model_deprecated"`,
		`"kind":"model_deprecated"`,
		`"type":"invalid_request_error"`,
		`"request_id":"req-deprecated"`,
		`end of life`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("response body missing %s: %s", needle, body)
		}
	}
}
