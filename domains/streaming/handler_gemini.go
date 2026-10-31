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
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/auth"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
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
	// jsonArray switches the wire framing to the vendor's default streaming
	// shape: a chunked JSON array (elements joined by ",\n", wrapped in
	// "[" / "]") instead of SSE "data: " frames. The vendor serves this
	// shape for :streamGenerateContent when "alt=sse" is absent
	// (docs/vendor-formats/gemini.md §流式响应（JSON 数组）); both official
	// google-genai SDKs always send alt=sse, so this path only serves
	// hand-rolled native clients that follow the vendor default.
	jsonArray bool
	// arrayStarted / arrayClosed track whether the "[" opener and "]"
	// closer have been written, so error terminals and finish() always
	// leave a parseable JSON body behind.
	arrayStarted bool
	arrayClosed  bool
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

// newGeminiJSONArrayStreamWriter returns a writer that frames the Gemini
// stream as the vendor's default chunked JSON array instead of SSE. Used
// when the inbound request omits "alt=sse".
func newGeminiJSONArrayStreamWriter(w http.ResponseWriter) *geminiStreamWriter {
	gw := newGeminiStreamWriter(w)
	gw.jsonArray = true
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
//
// In array mode the body must stay a parseable JSON array end to end, so the
// error is emitted as the only element (opener and closer written here when
// the failure lands before the first data element) and the array is closed
// immediately — no further elements follow a terminal error.
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
	if w.jsonArray {
		if !w.arrayStarted {
			w.arrayStarted = true
			w.arrayClosed = true
			_, _ = w.ResponseWriter.Write(append(append(append([]byte("["), body...), '\n'), ']'))
		} else if !w.arrayClosed {
			w.arrayClosed = true
			_, _ = w.ResponseWriter.Write(append(append([]byte(",\n"), body...), '\n', ']'))
		}
		if w.flusher != nil {
			w.flusher.Flush()
		}
		return
	}
	_, _ = w.ResponseWriter.Write(append(append([]byte("data: "), body...), '\n', '\n'))
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *geminiStreamWriter) WriteHeader(status int) {
	w.status = status
	if w.jsonArray {
		// ChatHandler unconditionally stamps text/event-stream on its
		// synthetic chat request, overwriting whatever ServeHTTP pre-set —
		// the header decision therefore has to be made here, at the moment
		// the response head is actually committed.
		w.Header().Set("Content-Type", "application/json")
	}
	w.ResponseWriter.WriteHeader(status)
}

// writeArrayElement appends one JSON object as the next element of the
// vendor-default chunked JSON array, writing the "[" opener before the
// first element and a ",\n" separator before each subsequent one.
func (w *geminiStreamWriter) writeArrayElement(obj []byte) error {
	if w.failed {
		return nil
	}
	if !w.arrayStarted {
		w.arrayStarted = true
		if _, err := w.ResponseWriter.Write(append([]byte("["), obj...)); err != nil {
			return err
		}
		return nil
	}
	_, err := w.ResponseWriter.Write(append([]byte(",\n"), obj...))
	return err
}

func (w *geminiStreamWriter) writeChunk(line []byte) error {
	if bytes.Equal(line, []byte("data: [DONE]")) {
		// [DONE] is an OpenAI chat sentinel, not a Gemini protocol frame.
		w.done = true
		return nil
	}
	if bytes.HasPrefix(line, []byte(":")) {
		if w.jsonArray {
			// SSE comments cannot be carried by a JSON array body — the
			// vendor's default streaming shape has no comment/keepalive
			// channel. Drop them instead of corrupting the array; a stalled
			// upstream is simply silent in this shape (same as the vendor).
			return nil
		}
		// SSE comments are protocol-neutral transport frames. Preserve them
		// verbatim so native Gemini streams receive the shared heartbeat.
		_, err := w.ResponseWriter.Write(append(append([]byte{}, line...), '\n', '\n'))
		return err
	}
	// R26-U1 (2026-09-29): an OpenAI chat error payload ({"error":...})
	// mid-stream — most notably the output-policy block terminal emitted by
	// the interceptor chain — is a shape a Gemini client cannot parse (and
	// it round-trips ParseOpenAIStreamChunk without error, so the check
	// must come before chunk parsing). Re-emit it as a Gemini-native
	// terminal error frame instead of forwarding an alien protocol line.
	if w.reemitChatErrorFrameAsGemini(line) {
		return nil
	}
	chunk, err := ir.ParseOpenAIStreamChunk(string(line))
	if err != nil {
		// ChatHandler can emit a non-SSE error after stream setup. Preserve it
		// so the caller receives the original diagnostic rather than a dropped chunk.
		if w.jsonArray {
			// The diagnostic is only embeddable when it is a JSON object;
			// anything else would corrupt the array, so fail closed with a
			// well-formed error element instead of forwarding raw bytes.
			if trimmed := bytes.TrimSpace(line); json.Valid(trimmed) {
				return w.writeArrayElement(trimmed)
			}
			w.failClosed("unparsable stream frame from upstream")
			return nil
		}
		_, writeErr := w.ResponseWriter.Write(append(append([]byte{}, line...), '\n'))
		if writeErr != nil {
			return writeErr
		}
		return nil
	}
	output := chunk.SerializeGemini()
	if output == "" {
		return nil
	}
	if w.jsonArray {
		// SerializeGemini emits the SSE frame "data: {…}\n\n"; the array shape
		// reuses that one body implementation and re-frames it as an element.
		obj := bytes.TrimSpace(bytes.TrimPrefix([]byte(output), []byte("data: ")))
		return w.writeArrayElement(obj)
	}
	_, err = w.ResponseWriter.Write([]byte(output))
	return err
}

// reemitChatErrorFrameAsGemini converts an OpenAI chat SSE error line
// (data: {"error":{"message":...}} with no choices) into a Gemini-native
// terminal frame via failClosed. It reports whether the line was consumed.
func (w *geminiStreamWriter) reemitChatErrorFrameAsGemini(line []byte) bool {
	const dataPrefix = "data: "
	if !bytes.HasPrefix(line, []byte(dataPrefix)) {
		return false
	}
	var payload struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(bytes.TrimSpace(line[len(dataPrefix):]), &payload) != nil ||
		payload.Error == nil || len(payload.Choices) > 0 {
		return false
	}
	message := payload.Error.Message
	if message == "" {
		message = "upstream stream error"
	}
	w.failClosed(message)
	return true
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
	if line := bytes.TrimSpace(w.pending); len(line) > 0 {
		w.pending = nil
		if err := w.writeChunk(line); err != nil {
			return err
		}
	}
	if w.jsonArray && !w.failed {
		if !w.arrayStarted {
			// A stream that produced no elements at all still owes the
			// client a parseable body: the vendor's zero-chunk shape is an
			// empty array.
			w.arrayStarted = true
			w.arrayClosed = true
			_, err := w.ResponseWriter.Write([]byte("[]"))
			return err
		}
		if !w.arrayClosed {
			w.arrayClosed = true
			_, err := w.ResponseWriter.Write([]byte("\n]"))
			return err
		}
	}
	return nil
}

// geminiModelPathRe matches the Gemini model suffix in URL paths.
//
// ★ 动作集合含 countTokens（2026-10-29 Gemini countTokens 轮）：厂商原生
// 客户端在发 generateContent 之前会先探一次 token 计数（google-genai SDK 的
// models.count_tokens）⇒ 不认这个动作 ⇒ 原生客户端拿到 404 ⇒ 上下文管理
// 整条链断在第一步。
//
// ★ 三个动作共用同一个路径形状 `{model}:{action}` ⇒ **必须由同一个解析器
// 认全**，否则「解析面」与「凭据收口面」（internal/auth.IsGeminiNativePath，
// 锚在同一组动作上）会各认一半 ⇒ 一个能认证、另一个 404。
var geminiModelPathRe = regexp.MustCompile(`/(?:v1beta|v1)/models/([^:/]+)(?::(generateContent|streamGenerateContent|countTokens))`)

// NewGeminiHandler constructs a Gemini handler that delegates to the
// existing ChatHandler after IR translation.
func NewGeminiHandler(ch *ChatHandler) *GeminiHandler {
	h := &GeminiHandler{chatHandler: ch}
	if ch != nil {
		h.requestLogger = ch.requestLogger
	}
	return h
}

// geminiWantsSSE reports whether the inbound request explicitly selected
// the vendor's SSE streaming shape via "alt=sse". Everything else (no alt
// parameter, alt=json) falls back to the vendor's default chunked JSON
// array shape.
func geminiWantsSSE(r *http.Request) bool {
	if r == nil {
		return false
	}
	return r.URL.Query().Get("alt") == "sse"
}

func newGeminiSyntheticRequest(original *http.Request, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if original != nil {
		req = req.WithContext(original.Context())
		req.Header = original.Header.Clone()
		normalizeGeminiCredential(req, original)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Gw-Client-Protocol", ir.ProtocolGeminiGenerate)
	return req
}

// normalizeGeminiCredential converts the Gemini-native credential forms into
// the canonical `Authorization: Bearer` shape on the synthetic request.
//
// ★ 2026-10-26（Gemini 原生认证修复轮）：Gemini 原生客户端按厂方契约用
// `x-goog-api-key` 头或 `?key=` query 传凭据（docs/vendor-formats/gemini.md
// §HTTP 形态、docs/供应商协议优化.md §8.3、docs/QUICK_REFERENCE.md
// §Gemini协议）。而 Gemini handler 把请求转成**合成的
// /v1/chat/completions** 请求再交给 ChatHandler ⇒ 合成请求的下游全是
// OpenAI 形状的代码（只认 Bearer / x-api-key）。
//
// ⇒ 在**边界上一次性归一**：凭据形态在入口转成 Bearer，下游一律不用
// 知道 Google 的头名 ⇒ 不会出现「入口认了、下游不认」的半修状态。
//
// ★ 为什么不在合成请求里保留 `?key=`：合成请求的 URL 是新建的
// （httptest.NewRequest），query 根本带不过来；而把 key 留在 URL 上
// 会让它进入内部请求的 URL 字段 ⇒ 有落进日志的风险。归一成头之后
// 凭据只存在于 Authorization 头里。
//
// ★★ 只归一**本轮新增的两种形态**（x-goog-api-key / ?key=）：
// `x-api-key`（Anthropic）本来就能被下游 extractBearerToken 读到
// （合成请求克隆了原始头）⇒ 把它改写成 Authorization 会**顺带改变**
// 另一条既有路径的行为（无 verifier 模式下 handoff 会把 Authorization
// 里的客户端凭据转给摘要上游，见 trustedHandoffUpstreamAPIKey）
// ⇒ ⇒ 那属于本轮范围之外，本轮**不动**。
func normalizeGeminiCredential(synth, original *http.Request) {
	if synth == nil || original == nil {
		return
	}
	key, source := auth.ExtractClientKey(original)
	if key == "" {
		return
	}
	if source != auth.SourceGoogAPIKey && source != auth.SourceQueryKey {
		// Bearer / x-api-key：合成请求已克隆原始头，无需改写。
		return
	}
	synth.Header.Set("Authorization", "Bearer "+key)
}

// attachGeminiSourceReasoning stashes the inbound reasoning intent into the
// request context (R45 Gemini thinking 专项).
//
// Step 6 below serializes the IR to the OpenAI wire form strictly BEFORE
// routing, so req.TargetProvider is necessarily empty there and
// applyThinkingToOpenAIChat cannot render the budget-shaped Reasoning intent
// (generationConfig.thinkingConfig → Reasoning{Type:"enabled",BudgetTokens})
// into the synthetic body. Without this carrier the executor's re-parse can
// never recover the intent and it is silently dropped for every non-Gemini
// target — the P5 defect class, Gemini-inbound remnant. The executor restores
// it via ir.RestoreSourceReasoning at the point where the target dialect is
// known (finalizeOpenAIUpstreamBody). Requests without thinkingConfig are
// returned unchanged (no context mutation on the common path).
func attachGeminiSourceReasoning(r *http.Request, irReq *ir.InternalRequest) *http.Request {
	if r == nil || irReq == nil {
		return r
	}
	src := ir.SourceReasoning{
		Reasoning:      irReq.Reasoning,
		Thinking:       irReq.Thinking,
		SourceProtocol: ir.ProtocolGeminiGenerate,
	}
	if !src.HasReasoningIntent() {
		return r
	}
	return r.WithContext(ir.WithSourceReasoning(r.Context(), src))
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
		writeGeminiError(w, http.StatusNotFound, "URL path must match /v{1beta,1}/models/{model}:(generateContent|streamGenerateContent|countTokens)")
		return
	}

	// Step 1.5: :countTokens 是**另一个动作**，不进生成链路 ——
	// 它不调上游、不产生内容，只回答「这段提示词有多少 token」。
	// ★ 分派必须在这里（读 body 之前），因为它与生成路径的 body 解析、
	// 鉴权、闸门全部不同。
	if action == "countTokens" {
		h.serveCountTokens(w, r, model)
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

	// Step 4.5 (R45 Gemini thinking 专项): 把入向推理意图（thinkingConfig →
	// ir.Reasoning budget 形）挂进请求 context。详见 attachGeminiSourceReasoning。
	r = attachGeminiSourceReasoning(r, irReq)

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
		// Vendor dialect: "alt=sse" selects SSE framing; the default (no
		// alt parameter) is the vendor's chunked JSON array. Both official
		// google-genai SDKs always request alt=sse, so the array path only
		// serves native clients that follow the vendor default.
		sseMode := geminiWantsSSE(r)
		if sseMode {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		streamWriter := newGeminiStreamWriter(w)
		if !sseMode {
			streamWriter = newGeminiJSONArrayStreamWriter(w)
		}
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
		// R26-U1: the recorder body here is the chat handler's OpenAI-shaped
		// error (e.g. the R25-A output-policy 403) — re-emit it in
		// Gemini-native error shape instead of leaking an alien protocol
		// body to a native Gemini client.
		var openAIError struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if respStatus >= 400 && json.Unmarshal(respBody, &openAIError) == nil && openAIError.Error != nil {
			message := openAIError.Error.Message
			if message == "" {
				message = fmt.Sprintf("upstream error (HTTP %d)", respStatus)
			}
			writeGeminiError(w, respStatus, message)
			return
		}
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

// ── :countTokens（2026-10-29 Gemini countTokens 轮）────────────────────

// serveCountTokens answers POST /v{1beta,1}/models/{model}:countTokens.
//
// # 口径：为什么是「估算」，以及估算的是**哪一份**文本
//
// ★★ 本仓**没有**任何供应商分词器 ⇒ 网关给不出厂商那个精确数字 ⇒
// **任何**答案都是估算 ⇒ 于是唯一的问题是「这个估算指的是什么」。
//
// ★★★ 本实现估的是**网关真正会发出去的那份文本**，不是客户端原始请求：
//
//  1. Gemini 原生请求在本仓会被 IR 归一（ir.ParseGemini → ir.SerializeOpenAI）
//     才进主链路（handler_gemini.go 的 Step 3 / Step 6）；
//  2. 上游看到的是**归一之后**的那份，不是客户端写的那份；
//  3. ⇒ 所以「客户端原文的 token 数」与「网关实际会被计费/被拒的那个 token 数」
//     **不是同一个数** ⇒ 回答前者会答非所问。
//
// ★ ⇒ 因此估算走的是**与生成路径同一份归一**：同一个 ir.ParseGemini +
// ir.SerializeOpenAI ⇒ ★★ **两个 Gemini 面对同一段提示词给出的口径一致**
// ⇒ 客户端的「预检数字」与「网关实际发出的内容」不会各说各话。
//
// ★ 为什么**不转发上游让厂商算**（2026-10-27 那一轮的登记理由是「唯一诚实的
// 实现是转发上游」）—— ★★★ 本轮把这个理由推翻了，三条：
//
//  1. ★ **转发算的是另一份请求**：厂商的 :countTokens 会对**客户端原文**
//     （Gemini 形状）计数，而网关发出去的是**归一后**的那份 ⇒ 转发得到的
//     数字描述的是**一个不会发生的请求** ⇒ 它不是更诚实，是**另一种不诚实**。
//  2. ★ **转发并非普遍可用**：只有 gemini-generate 协议的上游才有 :countTokens；
//     而本仓的 Gemini 入向可以被**任意**上游服务（IR 翻译到 OpenAI/Anthropic/
//     Ollama）⇒ 对这类上游「转发」根本无处可转。
//  3. ★ **转发只在一种情况下更准**：上游协议恰好是 gemini-generate 时 ⇒
//     那一支的精确值由**下一轮**单独做（本轮登记，见审计档），本轮不混进来。
//
// ★ 与 Anthropic 的 /v1/messages/count_tokens 的差别（那一条是仓内先例）：
// Anthropic 那边估算的是**原始 Anthropic 体**，本条估的是**归一后的体**；
// 而 Anthropic 之所以必须估原文，是因为 **SSE 无法回填 message_start**
// （Anthropic 客户端只在流首拿得到输入 token）⇒ 那个数字是它**唯一**能看到的
// 数字 ⇒ 两个面必须一致。★ Gemini 的响应**本来就带精确的
// usageMetadata.promptTokenCount** ⇒ 预检数字与事后用量之间的漂移
// **不是同一类失败** ⇒ ⇒ 那条「一致性」理由**不迁移** ⇒ 本条可以取
// 「与网关实际发送一致」这个更强的口径。
//
// ★ RPM：**消耗**（与同一条路由上的生成动作同一把尺，checkGatewayRateLimit）。
//
// ★ 为什么本地计算面也要限流：refine/analyze/summarize 之所以「刻意不消耗」
// （见 authenticateNoRPM），是因为它们的 LLM 步骤**经环回 chat 面携带同一把
// 调用方 key**，那一跳已计过一次 ⇒ 再计即双计。而本面**不产生任何上游调用**
// ⇒ 不存在「已计过一次」的那一跳 ⇒ 按路由上其他动作计一次，是唯一自洽的选择。
// ★ 而仓内已把 Anthropic 的 /v1/messages/count_tokens 登记成 P2 缺口
// （「无 RPM 限流」）⇒ 新端点不应再制造同一个缺口的第二个实例。
func (h *GeminiHandler) serveCountTokens(w http.ResponseWriter, r *http.Request, model string) {
	if r.Method != http.MethodPost {
		writeGeminiError(w, http.StatusMethodNotAllowed, "method not allowed: :countTokens requires POST")
		return
	}

	// 鉴权 + 预算闸（复用包级共享实现，错误信封换成 Gemini 形状）。
	keyInfo, ok := h.authenticateGeminiNative(w, r)
	if !ok {
		return
	}

	// ★ RPM 闸：**与同一条路由上的生成动作同一把尺**（checkGatewayRateLimit）。
	//
	// ★ 为什么本地计算面也要限流：仓内对 Anthropic 的 /v1/messages/count_tokens
	// 登记过一条 P2 缺口（「无 RPM 限流；单请求可读至 128MB 且无频率上限 ⇒
	// 待裁决是否补」——见 internal/ingressguard 的登记表）。⇒ ★ **新端点不应
	// 再制造同一个缺口的第二个实例**：本面要读整个 body 并做一次解析 + 估算，
	// 是网关自己的算力开销 ⇒ 有频率上限才是对的。
	//
	// ★ 与 summarize/transform 的「刻意不消耗 RPM」不同：那三个面的 LLM 步骤
	// **经环回 chat 面携带同一把调用方 key**，那一跳已计一次 ⇒ 再计即双计；
	// 而本面**不产生任何上游调用** ⇒ 不存在「已计一次」的那一跳 ⇒ 按路由上
	// 其他动作的口径计一次，是唯一自洽的选择。
	rlOutcome := checkGatewayRateLimit(r.Context(), keyInfo, h.chatHandler.rateLimiter, notifyRateLimitWait(w, false))
	if !rlOutcome.Skipped {
		writeRateLimitHeaders(w, rlOutcome)
		if rlOutcome.Blocked {
			recordGatewayRateLimitRejection(rlOutcome)
			ratelimit.MarkGatewaySharedKeyRateLimit(w)
			writeGeminiError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
	}

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

	// 与生成路径**同一份**归一：ParseGemini → SerializeOpenAI。
	irReq, err := ir.ParseGemini(bodyBytes)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, fmt.Sprintf("invalid Gemini request body: %v", err))
		return
	}
	if irReq.Model == "" {
		irReq.Model = model
	}
	openaiBody, err := ir.SerializeOpenAI(irReq)
	if err != nil {
		writeGeminiError(w, http.StatusInternalServerError,
			fmt.Sprintf("IR → OpenAI serialization failed: %v", err))
		return
	}

	// ★ 复用仓内既有的 OpenAI 形状估算器（domains/streaming/usage_estimator.go
	// 的 estimatePromptTokens：CJK 1 字≈1 token、其余 4 字≈1 token、
	// 图片块 765）⇒ **不写第四个 token 估算器**。
	total := estimatePromptTokens(openaiBody)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// ★ 厂商响应形状：{"totalTokens": N}（docs/vendor-formats/gemini.md 的
	// 模型列表同款命名法）。
	_, _ = w.Write([]byte(`{"totalTokens":` + strconv.Itoa(total) + `}`))
}

// authenticateGeminiNative 校验 Gemini 原生入向的凭据，并把失败写成
// **厂商原生形状**的错误信封。
//
// ★ 判定复用包级共享实现 authenticateNoRPMWith（校验 key + 键状态 + 预算闸，
// 不消耗 RPM）⇒ 与 refine/analyze/summarize 与 ChatHandler 判得同一把尺。
//
// ★ lite 模式（DB 校验器不可用）的静态键回退：与 ChatHandler 的静态数据面
// 键**同一份判定**（staticDataPlaneKeyMatches）⇒ 否则同一个部署里
// 「generateContent 要静态键、:countTokens 不要」会是一个真实的鉴权洞。
//
// ★ 返回的 keyInfo 供 RPM 闸使用（checkGatewayRateLimit 需要它算配额）；
// lite 模式下它为 nil，而该闸对 nil keyInfo 是 Skipped（与 ChatHandler 同）。
func (h *GeminiHandler) authenticateGeminiNative(w http.ResponseWriter, r *http.Request) (*authentication.KeyInfo, bool) {
	var kv SummarizeKeyVerifier
	var staticKey string
	if h != nil && h.chatHandler != nil {
		kv = h.chatHandler.keyVerifier
		staticKey = h.chatHandler.staticDataPlaneKey
	}

	// ★ 校验器可用时：共享实现负责 key + 键状态 + 预算闸。
	if kv != nil && kv.Enabled() {
		return authenticateNoRPMWith(kv, w, r, "", func(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
			writeGeminiError(w, status, msg)
		})
	}

	// ★ lite 模式：校验器不可用 ⇒ 退到静态数据面键（与 ChatHandler 同款）。
	// 两边都没有 ⇒ 放行（无凭据体系可校验；全局 AuthMiddleware 仍是第一道闸）。
	if staticKey == "" {
		return nil, true
	}
	rawKey := extractBearerToken(r)
	if !staticDataPlaneKeyMatches(rawKey, staticKey) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeGeminiError(w, http.StatusUnauthorized, "invalid api key")
		return nil, false
	}
	return nil, true
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
