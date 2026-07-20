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
