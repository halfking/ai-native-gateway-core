package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// GeminiHandler serves Google's native Gemini generateContent API endpoints.
//
// audit-gateway-gemini (2026-07-13): Routes client requests from
//   - POST /v1beta/models/{model}:generateContent
//   - POST /v1beta/models/{model}:streamGenerateContent
//   - POST /v1/models/{model}:generateContent       (newer path)
//   - POST /v1/models/{model}:streamGenerateContent (newer path)
//
// through the IR layer. The Gemini-native body is parsed, converted to
// OpenAI Chat IR, then dispatched through the existing ChatHandler. The
// upstream response is converted back to Gemini format.
//
// This gives us full Gemini-native client compatibility without requiring
// a brand-new executor — credentials, sticky sessions, retries, audit,
// telemetry all reuse the OpenAI infrastructure via IR translation.
//
// Multi-candidate decision (audit 2026-08-29): Gemini may return multiple
// `candidates` in a single response/stream, but the IR layer deliberately
// supports exactly ONE candidate (index 0). ParseGeminiResponse /
// ParseGeminiStreamChunk read only Candidates[0], and SerializeGeminiResponse
// emits a single-element `candidates` array. This is an explicit, documented
// product decision for this audit: extra candidates are collapsed (rejected)
// to candidate[0] rather than interleaved. Extending the IR to carry N
// candidates (and the gateway's semantic-cache / audit / token accounting to
// cope with them) is future product work and is OUT OF SCOPE here. The
// contract test TestGeminiMultiCandidateCollapsesToFirst asserts this.
type GeminiHandler struct {
	chatHandler   *ChatHandler
	requestLogger interface {
		CreateInitial(context.Context, *telemetry.InitialRequest) error
	}
}

type geminiStreamWriter struct {
	http.ResponseWriter
	flusher http.Flusher
	pending []byte
	status  int
	done    bool
	// maxPendingBytes bounds the in-flight buffer between SSE newlines.
	// A single Gemini frame that exceeds it triggers failClosed instead of
	// growing the buffer without bound (a stalled/unbounded upstream line
	// would otherwise exhaust memory before the next flush).
	maxPendingBytes int
	// failed marks the stream as terminated by a fail-closed error so
	// subsequent Write calls are swallowed rather than emitting more frames.
	failed bool
}

// maxGeminiPendingBytes is the cap on the geminiStreamWriter pending buffer.
// It mirrors the project-wide sseMaxLineBytes default (16 MiB) so a single
// oversized Gemini frame is rejected with a Gemini-native error rather than
// buffered indefinitely.
const maxGeminiPendingBytes = 16 << 20

func newGeminiStreamWriter(w http.ResponseWriter) *geminiStreamWriter {
	gw := &geminiStreamWriter{ResponseWriter: w, maxPendingBytes: maxGeminiPendingBytes}
	gw.flusher, _ = w.(http.Flusher)
	return gw
}

func (w *geminiStreamWriter) Write(p []byte) (int, error) {
	if w.status >= http.StatusBadRequest {
		return w.ResponseWriter.Write(p)
	}
	// Once fail-closed, swallow further writes so we don't emit frames after
	// a Gemini-native error.
	if w.failed {
		return len(p), nil
	}
	// Bound the pending buffer: a single frame larger than the cap cannot be
	// processed line-by-line, so fail closed with a Gemini-native error.
	if len(w.pending)+len(p) > w.maxPendingBytes {
		w.failClosed("stream frame exceeded pending byte limit")
		return len(p), nil
	}
	w.pending = append(w.pending, p...)
	for {
		lineEnd := bytes.IndexByte(w.pending, '\n')
		if lineEnd < 0 {
			break
		}
		line := bytes.TrimSpace(w.pending[:lineEnd])
		w.pending = w.pending[lineEnd+1:]
		if len(line) == 0 {
			continue
		}
		if err := w.writeChunk(line); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// failClosed emits a Gemini-native error frame to the client and marks the
// stream as failed. It is intentionally terminal: no further frames are
// written by Write. The error shape matches writeGeminiError so a native
// Gemini client observes a consistent {"error":{...}} payload.
func (w *geminiStreamWriter) failClosed(message string) {
	if w.failed {
		return
	}
	w.failed = true
	// Release the accumulated pending buffer immediately: the stream is
	// terminal and holding up to maxGeminiPendingBytes beyond this point
	// only inflates RSS for the life of the request.
	w.pending = nil
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    http.StatusInternalServerError,
			"message": message,
			"status":  geminiStatusFor(http.StatusInternalServerError),
		},
	})
	_, _ = w.ResponseWriter.Write(append(append([]byte("data: "), body...), '\n', '\n'))
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *geminiStreamWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *geminiStreamWriter) writeChunk(line []byte) error {
	if bytes.Equal(line, []byte("data: [DONE]")) {
		// [DONE] is an OpenAI chat sentinel, not a Gemini protocol frame.
		w.done = true
		return nil
	}
	if bytes.HasPrefix(line, []byte(":")) {
		// SSE comments are protocol-neutral transport frames. Preserve them
		// verbatim so native Gemini streams receive the shared heartbeat.
		_, err := w.ResponseWriter.Write(append(append([]byte{}, line...), '\n', '\n'))
		return err
	}
	chunk, err := ir.ParseOpenAIStreamChunk(string(line))
	if err != nil {
		// ChatHandler can emit a non-SSE error after stream setup. Preserve it
		// so the caller receives the original diagnostic rather than a dropped chunk.
		_, writeErr := w.ResponseWriter.Write(append(append([]byte{}, line...), '\n'))
		if writeErr != nil {
			return writeErr
		}
		return nil
	}
	if output := chunk.SerializeGemini(); output != "" {
		_, err = w.ResponseWriter.Write([]byte(output))
	}
	return err
}

func (w *geminiStreamWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *geminiStreamWriter) finish() error {
	if w.failed {
		return nil
	}
	if len(bytes.TrimSpace(w.pending)) == 0 {
		return nil
	}
	line := bytes.TrimSpace(w.pending)
	w.pending = nil
	return w.writeChunk(line)
}

// geminiModelPathRe matches the Gemini model suffix in URL paths.
var geminiModelPathRe = regexp.MustCompile(`/(?:v1beta|v1)/models/([^:/]+)(?::(generateContent|streamGenerateContent))`)

// NewGeminiHandler constructs a Gemini handler that delegates to the
// existing ChatHandler after IR translation.
func NewGeminiHandler(ch *ChatHandler) *GeminiHandler {
	h := &GeminiHandler{chatHandler: ch}
	if ch != nil {
		h.requestLogger = ch.requestLogger
	}
	return h
}

func newGeminiSyntheticRequest(original *http.Request, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if original != nil {
		req = req.WithContext(original.Context())
		req.Header = original.Header.Clone()
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Gw-Client-Protocol", ir.ProtocolGeminiGenerate)
	return req
}

// ServeHTTP routes a Gemini-native request through the IR translation
// pipeline and back to Gemini-native response format.
func (h *GeminiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w, r, journeyWriter := beginRequestJourney(w, r, h.chatHandler)
	defer finishRequestJourney(r, journeyWriter)
	r = markExplicitStreamSession(r)
	// Step 4 audit fix (2026-07-28): per-request IR scope so anomaly dedup
	// is bounded to this request (SerializeOpenAI at step 6 and
	// SerializeGeminiResponse at the non-stream tail) rather than the
	// process-global map.
	scope, cleanup := ir.WithIRScope(nil)
	defer cleanup()
	_ = scope

	requestIdentity := initializeRequestIdentity(r)
	requestID := requestIdentity.RequestID
	provisionalSessionID := requestIdentity.SessionID
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("X-Gw-Session-Id", provisionalSessionID)
	if h.requestLogger != nil {
		if err := h.requestLogger.CreateInitial(r.Context(), &telemetry.InitialRequest{
			RequestID: requestID, TenantID: "default", SessionID: provisionalSessionID, Provisional: true,
		}); err != nil {
			slog.Warn("gemini_handler: early WAL create failed", "request_id", requestID, "error", err)
		}
	}

	// Step 1: Extract model name and action from URL path
	model, action, ok := extractGeminiPath(r.URL.Path)
	if !ok {
		writeGeminiError(w, http.StatusNotFound, "URL path must match /v{1beta,1}/models/{model}:(generateContent|streamGenerateContent)")
		return
	}

	// Step 2: Read the request body (Gemini format)
	bodyBytes, err := readRequestBody(r.Context(), r.Body, maxBodySize)
	if err != nil {
		status := http.StatusBadRequest
		message := "failed to read body"
		if len(bodyBytes) > maxBodySize {
			status = http.StatusRequestEntityTooLarge
			message = "request body exceeds gateway limit"
		} else if errors.Is(err, context.DeadlineExceeded) {
			message = "request body read timed out"
		}
		writeGeminiError(w, status, message)
		return
	}
	_ = r.Body.Close()

	// Step 3: Parse Gemini body → IR
	irReq, err := ir.ParseGemini(bodyBytes)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest,
			fmt.Sprintf("invalid Gemini request body: %v", err))
		return
	}

	// Step 4: Inject model name from URL if body doesn't have one
	if irReq.Model == "" {
		irReq.Model = model
	}
	resolveRequestJourney(r, "", model, irReq.Model)

	// Step 5: Mark streaming intent on the IR (URL action wins over body)
	wantStream := action == "streamGenerateContent" || irReq.Stream

	// Step 6: Convert IR → OpenAI Chat body via IR layer
	irReq.SourceProtocol = ir.ProtocolGeminiGenerate // mark origin
	openaiBody, err := ir.SerializeOpenAI(irReq)
	if err != nil {
		writeGeminiError(w, http.StatusInternalServerError,
			fmt.Sprintf("IR → OpenAI serialization failed: %v", err))
		return
	}
	// Force stream flag on OpenAI body when streaming is requested
	if wantStream && !irReq.Stream {
		var openaiMap map[string]any
		_ = json.Unmarshal(openaiBody, &openaiMap)
		openaiMap["stream"] = true
		openaiBody, _ = json.Marshal(openaiMap)
	}

	// Step 7: Build a synthetic /v1/chat/completions request that the
	// existing ChatHandler will recognize and dispatch through the OpenAI
	// executor (with all its infrastructure: credentials, sticky, audit).
	synthReq := newGeminiSyntheticRequest(r, openaiBody)

	// Step 8: Stream through a real ResponseWriter so ChatHandler flushes
	// reach the Gemini client as soon as each upstream chunk is available.
	if wantStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		streamWriter := newGeminiStreamWriter(w)
		h.chatHandler.ServeHTTP(streamWriter, synthReq)
		if err := streamWriter.finish(); err != nil {
			slog.Warn("gemini_handler: failed to flush final stream chunk", "err", err)
		}
		return
	}

	// Non-streaming requests still use a recorder so the complete response can
	// be converted through the response IR before it is written to the client.
	rec := httptest.NewRecorder()
	h.chatHandler.ServeHTTP(rec, synthReq)
	respStatus := rec.Code
	respHeader := rec.Header()
	respBody := rec.Body.Bytes()
	for k, vals := range respHeader {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Content-Encoding") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if respStatus >= 400 || len(respBody) == 0 {
		w.WriteHeader(respStatus)
		_, _ = w.Write(respBody)
		return
	}
	irResp, err := ir.ParseOpenAIResponse(respBody)
	if err != nil {
		slog.Warn("gemini_handler: failed to parse OpenAI response as IR",
			"err", err, "status", respStatus)
		w.WriteHeader(respStatus)
		_, _ = w.Write(respBody)
		return
	}
	{
		// Non-stream: serialize full Gemini response
		geminiRespBytes, err := ir.SerializeGeminiResponse(irResp, clientModel(irReq))
		if err != nil {
			slog.Warn("gemini_handler: IR → Gemini serialization failed, forwarding raw",
				"err", err)
			w.WriteHeader(respStatus)
			_, _ = w.Write(respBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(respStatus)
		_, _ = w.Write(geminiRespBytes)
	}
}

// extractGeminiPath parses a URL path like /v1beta/models/{m}:generateContent.
func extractGeminiPath(p string) (model string, action string, ok bool) {
	m := geminiModelPathRe.FindStringSubmatch(p)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// writeGeminiError writes a Gemini-native error response.
//
// Gemini error shape:
//
//	{ "error": { "code": <int>, "message": "<msg>", "status": "<StatusName>" } }
func writeGeminiError(w http.ResponseWriter, status int, msg string) {
	geminiStatus := geminiStatusFor(status)
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": msg,
			"status":  geminiStatus,
		},
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// geminiStatusFor maps HTTP status codes to Gemini-native status names.
func geminiStatusFor(httpStatus int) string {
	switch httpStatus {
	case 400:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 500, 502, 503, 504:
		return "INTERNAL"
	default:
		if httpStatus >= 400 && httpStatus < 500 {
			return "FAILED_PRECONDITION"
		}
		if httpStatus >= 500 {
			return "INTERNAL"
		}
		return "UNKNOWN"
	}
}

// clientModel returns the model name in Gemini-resolved form.
func clientModel(req *ir.InternalRequest) string {
	if req == nil || req.Model == "" {
		return "unknown"
	}
	return req.Model
}
