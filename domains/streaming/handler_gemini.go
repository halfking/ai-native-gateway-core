package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

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
type GeminiHandler struct {
	chatHandler *ChatHandler
}

// geminiModelPathRe matches the Gemini model suffix in URL paths.
var geminiModelPathRe = regexp.MustCompile(`/(?:v1beta|v1)/models/([^:/]+)(?::(generateContent|streamGenerateContent))`)

// NewGeminiHandler constructs a Gemini handler that delegates to the
// existing ChatHandler after IR translation.
func NewGeminiHandler(ch *ChatHandler) *GeminiHandler {
	return &GeminiHandler{chatHandler: ch}
}

// ServeHTTP routes a Gemini-native request through the IR translation
// pipeline and back to Gemini-native response format.
func (h *GeminiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Step 1: Extract model name and action from URL path
	model, action, ok := extractGeminiPath(r.URL.Path)
	if !ok {
		writeGeminiError(w, http.StatusNotFound, "URL path must match /v{1beta,1}/models/{model}:(generateContent|streamGenerateContent)")
		return
	}

	// Step 2: Read the request body (Gemini format)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "failed to read body")
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
	synthReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader(openaiBody))
	synthReq.Header = r.Header.Clone()
	if synthReq.Header.Get("Content-Type") == "" {
		synthReq.Header.Set("Content-Type", "application/json")
	}
	// Mark origin so downstream observability can distinguish Gemini-via-IR
	synthReq.Header.Set("X-Gw-Client-Protocol", ir.ProtocolGeminiGenerate)

	// Step 8: Dispatch to ChatHandler and capture response
	rec := httptest.NewRecorder()
	h.chatHandler.ServeHTTP(rec, synthReq)

	// Step 9: Apply upstream status + headers, then transform body
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
		// Error/empty: forward as-is
		w.WriteHeader(respStatus)
		_, _ = w.Write(respBody)
		return
	}

	// Step 10: Convert OpenAI response → IR
	irResp, err := ir.ParseOpenAIResponse(respBody)
	if err != nil {
		slog.Warn("gemini_handler: failed to parse OpenAI response as IR",
			"err", err, "status", respStatus)
		w.WriteHeader(respStatus)
		_, _ = w.Write(respBody)
		return
	}

	// Step 11: Streaming vs non-streaming output
	if wantStream {
		// OpenAI SSE → IR StreamChunk → Gemini SSE
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		// Scan each OpenAI SSE line, convert to Gemini SSE via IR chunk
		for _, raw := range bytes.Split(respBody, []byte("\n")) {
			line := bytes.TrimSpace(raw)
			if len(line) == 0 {
				continue
			}
			// Strip "data: " prefix
			payload := line
			if bytes.HasPrefix(payload, []byte("data:")) {
				payload = bytes.TrimPrefix(payload, []byte("data:"))
				payload = bytes.TrimSpace(payload)
			}
			if bytes.Equal(payload, []byte("[DONE]")) {
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
				continue
			}
			// Parse OpenAI chunk → IR chunk
			chunk, err := ir.ParseOpenAIStreamChunk(string(line))
			if err != nil {
				continue
			}
			// Serialize IR chunk → Gemini SSE
			geminiLine := chunk.SerializeGemini()
			if geminiLine == "" {
				continue
			}
			_, _ = w.Write([]byte(geminiLine))
			if flusher != nil {
				flusher.Flush()
			}
		}
	} else {
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
