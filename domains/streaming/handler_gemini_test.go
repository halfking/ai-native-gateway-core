package streaming

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

type geminiRequestLoggerStub struct {
	create func(context.Context, *telemetry.InitialRequest) error
}

func (s *geminiRequestLoggerStub) CreateInitial(ctx context.Context, req *telemetry.InitialRequest) error {
	return s.create(ctx, req)
}

type geminiFlushWriter struct {
	header  http.Header
	status  int
	body    strings.Builder
	flushes int
}

func (w *geminiFlushWriter) Header() http.Header { return w.header }

func (w *geminiFlushWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *geminiFlushWriter) WriteHeader(status int) { w.status = status }

func (w *geminiFlushWriter) Flush() { w.flushes++ }

// init installs a discard logger for test runs.
func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestExtractGeminiPath verifies URL path parsing for Gemini endpoints.
func TestExtractGeminiPath(t *testing.T) {
	cases := []struct {
		path       string
		wantModel  string
		wantAction string
		wantOK     bool
	}{
		{"/v1beta/models/gemini-2.5-pro:generateContent", "gemini-2.5-pro", "generateContent", true},
		{"/v1beta/models/gemini-2.0-flash:streamGenerateContent", "gemini-2.0-flash", "streamGenerateContent", true},
		{"/v1/models/gemini-pro:generateContent", "gemini-pro", "generateContent", true},
		// Wrong action
		{"/v1beta/models/gemini-pro:countTokens", "", "", false},
		// Missing action
		{"/v1beta/models/gemini-pro", "", "", false},
		// Wrong path
		{"/v1/chat/completions", "", "", false},
		{"/healthz", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			model, action, ok := extractGeminiPath(tc.path)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
				return
			}
			if !ok {
				return
			}
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
			if action != tc.wantAction {
				t.Errorf("action = %q, want %q", action, tc.wantAction)
			}
		})
	}
}

// TestGeminiStatusFor_HTTPStatusCodeMapping verifies HTTP→Gemini status names.
func TestGeminiStatusFor_HTTPStatusCodeMapping(t *testing.T) {
	cases := map[int]string{
		400: "INVALID_ARGUMENT",
		401: "UNAUTHENTICATED",
		403: "PERMISSION_DENIED",
		404: "NOT_FOUND",
		429: "RESOURCE_EXHAUSTED",
		500: "INTERNAL",
		502: "INTERNAL",
		200: "UNKNOWN",
	}
	for httpStatus, wantGemini := range cases {
		if got := geminiStatusFor(httpStatus); got != wantGemini {
			t.Errorf("geminiStatusFor(%d) = %q, want %q", httpStatus, got, wantGemini)
		}
	}
}

func TestGeminiStreamWriter_ConvertsFlushedChunks(t *testing.T) {
	w := &geminiFlushWriter{header: make(http.Header)}
	gw := newGeminiStreamWriter(w)

	first := `data: {"id":"chunk-1","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Hi"}}]}` + "\n\n"
	second := `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n"
	if _, err := gw.Write([]byte(first[:len(first)/2])); err != nil {
		t.Fatalf("first partial write: %v", err)
	}
	if w.body.Len() != 0 {
		t.Fatal("partial SSE line was written before its newline")
	}
	if _, err := gw.Write([]byte(first[len(first)/2:])); err != nil {
		t.Fatalf("first complete write: %v", err)
	}
	if !strings.Contains(w.body.String(), `"text":"Hi"`) {
		t.Fatalf("converted Gemini chunk missing text: %s", w.body.String())
	}
	if _, err := gw.Write([]byte(second)); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if !strings.Contains(w.body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("converted Gemini chunk missing finish reason: %s", w.body.String())
	}
	gw.Flush()
	if w.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", w.flushes)
	}
	if _, err := gw.Write([]byte(": keep-alive\n\ndata: [DONE]\n\ndata: [DONE]\n")); err != nil {
		t.Fatalf("transport write: %v", err)
	}
	if got := strings.Count(w.body.String(), ": keep-alive"); got != 1 {
		t.Fatalf("heartbeat count = %d, want 1", got)
	}
	if got := strings.Count(w.body.String(), "data: [DONE]"); got != 0 {
		t.Fatalf("Gemini stream leaked %d OpenAI DONE sentinels", got)
	}
}

func TestGeminiSyntheticRequestPropagatesOriginalContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	original := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:streamGenerateContent", nil).WithContext(ctx)
	synthetic := newGeminiSyntheticRequest(original, []byte(`{"stream":true}`))

	cancel()
	if synthetic.Context().Err() != context.Canceled {
		t.Fatalf("synthetic context error = %v, want context.Canceled", synthetic.Context().Err())
	}
	if got := synthetic.Header.Get("X-Gw-Client-Protocol"); got != ir.ProtocolGeminiGenerate {
		t.Fatalf("client protocol = %q", got)
	}
}

func TestGeminiStreamWriter_PreservesErrorResponses(t *testing.T) {
	w := &geminiFlushWriter{header: make(http.Header)}
	gw := newGeminiStreamWriter(w)
	gw.WriteHeader(http.StatusBadGateway)
	payload := []byte(`{"error":{"message":"upstream unavailable"}}`)
	if _, err := gw.Write(payload); err != nil {
		t.Fatalf("error write: %v", err)
	}
	if got := w.body.String(); got != string(payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
}

// TestGeminiHandler_URLPathNotMatching verifies 404 on malformed URLs.
func TestGeminiHandler_URLPathNotMatching(t *testing.T) {
	h := &GeminiHandler{chatHandler: &ChatHandler{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "URL path") {
		t.Errorf("body doesn't mention URL path: %s", body)
	}
}

func TestGeminiHandler_InvalidJSONBodyCreatesEarlyWALAndPropagatesGeneratedID(t *testing.T) {
	var got *telemetry.InitialRequest
	logger := &geminiRequestLoggerStub{create: func(_ context.Context, req *telemetry.InitialRequest) error {
		got = req
		return nil
	}}
	chat := &ChatHandler{}
	chat.SetRequestLogger(nil)
	h := &GeminiHandler{chatHandler: chat, requestLogger: logger}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent",
		strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	gotHeader := req.Header.Get("X-Request-Id")
	h.ServeHTTP(rec, req)

	if got == nil || got.RequestID == "" || got.SessionID == "" || !got.Provisional {
		t.Fatalf("early WAL request = %#v", got)
	}
	if req.Header.Get("X-Request-Id") != got.RequestID {
		t.Fatalf("request header ID = %q, WAL ID = %q", req.Header.Get("X-Request-Id"), got.RequestID)
	}
	if gotHeader != "" {
		t.Fatalf("test request unexpectedly had header: %q", gotHeader)
	}
}

func TestGeminiHandler_InvalidJSONBodyWithRequestIDCreatesEarlyWAL(t *testing.T) {
	var got *telemetry.InitialRequest
	logger := &geminiRequestLoggerStub{create: func(_ context.Context, req *telemetry.InitialRequest) error {
		got = req
		return nil
	}}
	h := &GeminiHandler{chatHandler: &ChatHandler{}, requestLogger: logger}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent",
		strings.NewReader(`not json`))
	req.Header.Set("X-Request-Id", "gemini-req")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got == nil || got.RequestID != "gemini-req" || got.SessionID == "" || !got.Provisional {
		t.Fatalf("early WAL request = %#v", got)
	}
}

func TestGeminiHandler_InvalidJSONBody(t *testing.T) {
	h := &GeminiHandler{chatHandler: &ChatHandler{}}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent",
		strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestSerializeGeminiResponse_RoundTrip verifies IR → Gemini JSON output.
func TestSerializeGeminiResponse_RoundTrip(t *testing.T) {
	t.Run("text content", func(t *testing.T) {
		irResp := &ir.InternalResponse{
			ID:      "msg_001",
			Model:   "gemini-2.5-pro",
			Created: 1700000000,
			Role:    "assistant",
			Content: []ir.ResponseContentBlock{
				{Type: "text", Text: "Hello Gemini client"},
			},
			FinishReason: "stop",
			Usage: ir.ResponseUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}

		out, err := ir.SerializeGeminiResponse(irResp, "gemini-2.5-pro")
		if err != nil {
			t.Fatalf("SerializeGeminiResponse: %v", err)
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}

		candidates, ok := result["candidates"].([]any)
		if !ok || len(candidates) != 1 {
			t.Fatalf("candidates malformed")
		}
		cand := candidates[0].(map[string]any)

		content := cand["content"].(map[string]any)
		parts := content["parts"].([]any)
		if len(parts) != 1 {
			t.Fatalf("parts = %d, want 1", len(parts))
		}
		part := parts[0].(map[string]any)
		if part["text"] != "Hello Gemini client" {
			t.Errorf("text = %v, want \"Hello Gemini client\"", part["text"])
		}

		if cand["finishReason"] != "STOP" {
			t.Errorf("finishReason = %v, want STOP", cand["finishReason"])
		}

		if content["role"] != "model" {
			t.Errorf("role = %v, want model", content["role"])
		}

		usage := result["usageMetadata"].(map[string]any)
		if int(usage["promptTokenCount"].(float64)) != 100 {
			t.Errorf("promptTokenCount = %v", usage["promptTokenCount"])
		}
		if int(usage["candidatesTokenCount"].(float64)) != 50 {
			t.Errorf("candidatesTokenCount = %v", usage["candidatesTokenCount"])
		}
		if int(usage["totalTokenCount"].(float64)) != 150 {
			t.Errorf("totalTokenCount = %v", usage["totalTokenCount"])
		}
		if result["modelVersion"] != "gemini-2.5-pro" {
			t.Errorf("modelVersion = %v", result["modelVersion"])
		}
	})
}

// TestSerializeGeminiResponse_WithMultimodalUsage verifies multimodal
// usage (image/audio tokens) propagation through Gemini response.
func TestSerializeGeminiResponse_WithMultimodalUsage(t *testing.T) {
	img := 300
	audio := 100

	irResp := &ir.InternalResponse{
		Model: "gemini-2.5-pro",
		Content: []ir.ResponseContentBlock{
			{Type: "text", Text: "I see an image"},
		},
		FinishReason: "stop",
		Usage: ir.ResponseUsage{
			PromptTokens:     1500,
			CompletionTokens: 50,
			TotalTokens:      1550,
			ImageTokens:      &img,
			AudioTokens:      &audio,
		},
	}

	out, err := ir.SerializeGeminiResponse(irResp, "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	var result map[string]any
	json.Unmarshal(out, &result)

	usage := result["usageMetadata"].(map[string]any)
	details, ok := usage["promptTokensDetails"].([]any)
	if !ok {
		t.Fatal("promptTokensDetails missing")
	}
	hasImage := false
	hasAudio := false
	for _, d := range details {
		m := d.(map[string]any)
		switch m["modality"] {
		case "IMAGE":
			hasImage = int(m["tokenCount"].(float64)) == 300
		case "AUDIO":
			hasAudio = int(m["tokenCount"].(float64)) == 100
		}
	}
	if !hasImage || !hasAudio {
		t.Errorf("missing IMAGE or AUDIO modality breakdown: %+v", details)
	}
}

// TestSerializeGeminiResponse_Nil verifies graceful handling of nil input.
func TestSerializeGeminiResponse_Nil(t *testing.T) {
	_, err := ir.SerializeGeminiResponse(nil, "model")
	if err == nil {
		t.Error("expected error for nil response")
	}
}

// TestMapFinishReasonToGemini verifies finish reason mapping.
func TestMapFinishReasonToGemini(t *testing.T) {
	// Build an IR response with each finish reason, serialize, and check
	// the Gemini candidate.finishReason mapping.
	cases := map[string]string{
		"stop":           "STOP",
		"length":         "MAX_TOKENS",
		"content_filter": "SAFETY",
		"tool_calls":     "STOP",
		"unknown":        "STOP",
	}
	for in, want := range cases {
		irResp := &ir.InternalResponse{
			FinishReason: in,
			Content:      []ir.ResponseContentBlock{{Type: "text", Text: "x"}},
		}
		out, err := ir.SerializeGeminiResponse(irResp, "")
		if err != nil {
			t.Fatalf("Serialize(%q): %v", in, err)
		}
		var result map[string]any
		_ = json.Unmarshal(out, &result)
		cands := result["candidates"].([]any)
		cand := cands[0].(map[string]any)
		if cand["finishReason"] != want {
			t.Errorf("FinishReason %q → %v, want %q", in, cand["finishReason"], want)
		}
	}
}

// TestGeminiStreamWriter_PendingCapFailClosed verifies the unified pending
// byte cap: a frame larger than maxPendingBytes triggers a Gemini-native
// error frame and marks the stream failed, and subsequent writes are
// swallowed rather than appended after the error.
func TestGeminiStreamWriter_PendingCapFailClosed(t *testing.T) {
	fw := &geminiFlushWriter{header: http.Header{}}
	w := &geminiStreamWriter{ResponseWriter: fw, maxPendingBytes: 32}

	// A single oversized pseudo-SSE frame (no newline) exceeds the cap.
	big := strings.Repeat("x", 64)
	n, err := w.Write([]byte(big))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(big) {
		t.Fatalf("Write returned n=%d, want %d", n, len(big))
	}
	if !w.failed {
		t.Fatal("writer should be failed after exceeding pending cap")
	}
	if w.pending != nil {
		t.Fatalf("failClosed must release the pending buffer, got %d bytes retained", len(w.pending))
	}

	// The client must have received a Gemini-native error frame.
	out := fw.body.String()
	if !strings.Contains(out, "\"error\"") || !strings.Contains(out, "pending byte limit") {
		t.Fatalf("expected Gemini-native error frame, got %q", out)
	}

	// Subsequent writes are swallowed and add no new frames.
	before := fw.body.Len()
	_, _ = w.Write([]byte("data: should-be-swallowed\n\n"))
	if fw.body.Len() != before {
		t.Fatalf("post-fail writes should be swallowed; body grew unexpectedly")
	}
}

// TestGeminiStreamWriter_WithinCapNotFailed verifies that frames within the
// cap do not trip the fail-closed path.
func TestGeminiStreamWriter_WithinCapNotFailed(t *testing.T) {
	fw := &geminiFlushWriter{header: http.Header{}}
	w := &geminiStreamWriter{ResponseWriter: fw, maxPendingBytes: maxGeminiPendingBytes}

	if _, err := w.Write([]byte("data: small\n\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.failed {
		t.Fatal("writer must not be failed for a frame within the cap")
	}
}

// TestGeminiMultiCandidateCollapsesToFirst asserts the documented product
// decision: a Gemini response with multiple candidates collapses to
// candidate[0] through the IR round-trip the GeminiHandler depends on.
// Extending the IR to carry N candidates is future work and out of scope.
func TestGeminiMultiCandidateCollapsesToFirst(t *testing.T) {
	body := `{"candidates":[` +
		`{"content":{"parts":[{"text":"first"}],"role":"model"}},` +
		`{"content":{"parts":[{"text":"second"}],"role":"model"}}` +
		`],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`

	irResp, err := ir.ParseGeminiResponse([]byte(body))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	out, err := ir.SerializeGeminiResponse(irResp, "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}

	var parsed struct {
		Candidates []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if len(parsed.Candidates) != 1 {
		t.Fatalf("expected exactly 1 candidate after collapse, got %d: %s", len(parsed.Candidates), out)
	}
	content, _ := parsed.Candidates[0]["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) == 0 {
		t.Fatalf("candidate[0] has no parts: %v", parsed.Candidates[0])
	}
	part0, _ := parts[0].(map[string]any)
	if part0["text"] != "first" {
		t.Fatalf("expected surviving candidate[0] text 'first', got %v", part0["text"])
	}
}
