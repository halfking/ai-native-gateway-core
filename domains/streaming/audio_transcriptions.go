// Package streaming — audio_transcriptions.go
//
// POST /v1/audio/transcriptions —— OpenAI 兼容的录音转写端点。
//
// 这是 2026-10-03 音频端点轮的对客户端承诺面：openpocket（及其它
// OpenAI 兼容客户端）的云端转写发现逻辑按「multipart file/model/
// language + 响应 {text}」探测这个端点，404 即判定「网关未提供转写
// 端点」后回退外部 ASR。本端点落地后：
//
//	非流式  POST multipart → 逐候选 failover → JSON {text} / text/plain
//	流式    stream=true   → SSE transcript.text.delta / transcript.text.done
//
// 事件名与 OpenAI gpt-4o-transcribe 流式、智谱 glm-asr SSE 保持一致，
// openpocket 的 TransportSSE 消费端无需新适配。
package streaming

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AudioTranscriptionsHandler proxies OpenAI-compatible transcription
// requests (multipart in, JSON/text/SSE out).
type AudioTranscriptionsHandler struct {
	svc *AudioService
}

func NewAudioTranscriptionsHandler(svc *AudioService) *AudioTranscriptionsHandler {
	return &AudioTranscriptionsHandler{svc: svc}
}

func (h *AudioTranscriptionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
	}
	w.Header().Set("X-Request-Id", requestID)

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErrorJSON(w, http.StatusMethodNotAllowed, requestID, "Method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}
	if h.svc == nil || h.svc.provider == nil || h.svc.upstream == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Audio transcription service unavailable", "server_error", "service_unavailable")
		return
	}
	keyInfo, ok := h.svc.authenticate(w, r, requestID)
	if !ok {
		return
	}

	req, err := parseTranscriptionsMultipart(w, r)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, err.Error(), "invalid_request_error", err.code)
		return
	}
	if keyInfo != nil {
		req.TenantID = keyInfo.TenantID
		if keyInfo.DefaultClientProfile != nil {
			req.Profile = *keyInfo.DefaultClientProfile
		}
	}

	if req.Stream {
		h.serveStream(w, r, requestID, req)
		return
	}

	res, terr := h.svc.Transcribe(r.Context(), *req, nil)
	if terr != nil {
		writeAudioError(w, requestID, terr)
		return
	}
	writeTranscriptionResult(w, requestID, req.Model, res, req.ResponseFormat)
}

// serveStream handles stream=true: SSE with transcript.text.delta /
// transcript.text.done events (OpenAI + zhipu-compatible naming).
func (h *AudioTranscriptionsHandler) serveStream(w http.ResponseWriter, r *http.Request, requestID string, req *TranscribeRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrorJSON(w, http.StatusInternalServerError, requestID, "Streaming unsupported by connection", "server_error", "stream_unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ": gateway request %s\n\n", requestID)
	flusher.Flush()

	writeEvent := func(event string, payload any) {
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(data))
		flusher.Flush()
	}

	// 一次 Transcribe 调用完成所有工作：
	//   - 桥接形态 → emitDelta 逐词回调（真流式）；
	//   - 透传形态（上游原生 SSE）→ RelayBody 整段写出。当前透传读取
	//     是「收完再转发」的伪流式（客户端语义不变：同样的 SSE 事件序
	//     列），智谱 30 秒限长的现实下这是 v1 的有意取舍；不做两次调用
	//     ——那会双倍消耗上游音频秒数。
	//
	// 39 轮（P2-1）：X-Gw-Audio-Transport 响应头在本流式路径上从未生效
	// ——WriteHeader(200) 已在流首发出，之后再 w.Header().Set 进不了已
	// 发送的响应头（Go net/http 语义；httptest.ResponseRecorder 不快照
	// 头所以测不出）。transport 信息改走 transcript.transport 事件：
	// SSE 消费者按事件名分发，未知事件名按规范忽略，openpocket 的
	// delta/done 消费不受影响。非流式路径的响应头（writeTranscriptionResult
	// 在 WriteHeader 之前设置）不受影响。
	res, err := h.svc.Transcribe(r.Context(), *req, func(delta string) {
		writeEvent("transcript.text.delta", map[string]string{"type": "transcript.text.delta", "delta": delta})
	})
	if err != nil {
		writeEvent("error", map[string]string{"type": "error", "message": sanitizeAudioErrorMessage(err.Error())})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	writeEvent("transcript.transport", map[string]string{"type": "transcript.transport", "transport": res.Transport})
	// 同 P2-1：流式路径的响应头已随 WriteHeader(200) 发出，IgnoredParams
	// 只能走事件（与 transport 同理）。未知事件名按 SSE 规范忽略。
	if len(res.IgnoredParams) > 0 {
		writeEvent("transcript.ignored_params", map[string]any{
			"type":    "transcript.ignored_params",
			"ignored": res.IgnoredParams,
		})
	}
	if res.RelayIsSSE && len(res.RelayBody) > 0 {
		_, _ = w.Write(res.RelayBody)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	writeEvent("transcript.text.done", map[string]string{"type": "transcript.text.done", "text": res.Text})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// writeTranscriptionResult renders the non-stream response per
// response_format: json/verbose_json → JSON object; text/srt/vtt → 透传文本。
func writeTranscriptionResult(w http.ResponseWriter, requestID, clientModel string, res *TranscribeResult, responseFormat string) {
	w.Header().Set("X-Gw-Audio-Transport", res.Transport)
	w.Header().Set("X-Gw-Upstream-Model", res.UpstreamModel)
	// 客户端传了但这条上游形态兑现不了的参数（chat-audio 下 prompt/
	// temperature/language）如实回执。必须在 WriteHeader 之前设置。
	if len(res.IgnoredParams) > 0 {
		w.Header().Set("X-Gw-Audio-Ignored-Params", strings.Join(res.IgnoredParams, ","))
	}
	if res.DurationSeconds != nil {
		w.Header().Set("X-Gw-Audio-Seconds", fmt.Sprintf("%g", *res.DurationSeconds))
	}
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "text", "srt", "vtt":
		// 透传形态已经有上游原文（RelayBody）；桥接形态用聚合文本。
		body := res.Text
		if len(res.RelayBody) > 0 && res.RelayIsTextOnly {
			body = string(res.RelayBody)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		return
	}
	// json / verbose_json：透传形态优先回上游原文（保留 segments/时间戳），
	// 桥接形态生成最小 JSON。
	if len(res.RelayBody) > 0 && !res.RelayIsTextOnly {
		ct := "application/json"
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(res.RelayBody)
		return
	}
	out := map[string]any{
		"text":  res.Text,
		"model": clientModel,
	}
	if res.DurationSeconds != nil {
		out["duration"] = *res.DurationSeconds
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// transcriptionsParseError carries a machine-readable code for the 400
// family（openpocket 探测把 4xx 带模型信息视为「端点存在」的证据）。
type transcriptionsParseError struct {
	msg  string
	code string
}

func (e *transcriptionsParseError) Error() string { return e.msg }

// parseTranscriptionsMultipart 解析 OpenAI 兼容的转写请求：
//
//	file            (required)  音频文件
//	model           (required)  模型名（canonical）
//	language        (optional)  ISO-639-1；chat-audio 桥接形态下透传为
//	                           asr_options.language（上游只认 zh/en/auto，
//	                           见 normalizeASRLanguageForCandidate 的
//	                           归一与丢弃口径）
//	prompt          (optional)  仅 multipart 透传形态生效（小米拒收 text part）
//	response_format(optional)  json（默认）| text | verbose_json | srt | vtt
//	temperature     (optional)
//	stream          (optional)  true → SSE
func parseTranscriptionsMultipart(w http.ResponseWriter, r *http.Request) (*TranscribeRequest, *transcriptionsParseError) {
	if err := r.ParseMultipartForm(maxAudioUploadBytes); err != nil {
		return nil, &transcriptionsParseError{msg: "invalid multipart form: " + err.Error(), code: "invalid_multipart"}
	}
	if r.MultipartForm == nil {
		return nil, &transcriptionsParseError{msg: "multipart form body is required", code: "invalid_multipart"}
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, &transcriptionsParseError{msg: "file field is required", code: "missing_file"}
	}
	defer func() { _ = file.Close() }()
	audio, err := io.ReadAll(io.LimitReader(file, maxAudioUploadBytes+1))
	if err != nil {
		return nil, &transcriptionsParseError{msg: "read file field failed: " + err.Error(), code: "read_failed"}
	}
	if int64(len(audio)) > maxAudioUploadBytes {
		return nil, &transcriptionsParseError{msg: fmt.Sprintf("audio file exceeds %d bytes", maxAudioUploadBytes), code: "file_too_large"}
	}
	if len(audio) == 0 {
		return nil, &transcriptionsParseError{msg: "audio file is empty", code: "empty_file"}
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		return nil, &transcriptionsParseError{msg: "model field is required", code: "invalid_model"}
	}
	contentType := ""
	if header != nil {
		contentType = header.Header.Get("Content-Type")
	}
	req := &TranscribeRequest{
		Model:          model,
		Language:       strings.TrimSpace(r.FormValue("language")),
		Prompt:         r.FormValue("prompt"),
		ResponseFormat: strings.ToLower(strings.TrimSpace(r.FormValue("response_format"))),
		Temperature:    strings.TrimSpace(r.FormValue("temperature")),
		Stream:         strings.EqualFold(strings.TrimSpace(r.FormValue("stream")), "true") || strings.TrimSpace(r.FormValue("stream")) == "1",
		File:           audio,
		Filename:       "audio",
		ContentType:    contentType,
	}
	if header != nil && header.Filename != "" {
		req.Filename = header.Filename
	}
	switch req.ResponseFormat {
	case "", "json", "text", "verbose_json", "srt", "vtt":
	default:
		return nil, &transcriptionsParseError{msg: "response_format must be one of json, text, verbose_json, srt, vtt", code: "invalid_response_format"}
	}
	return req, nil
}

// writeAudioError 把转写/合成链路的错误映射到网关错误信封。
//
// 「无候选」给 503 no_provider（openpocket 的 isNoProvider 识别口径），按
// audioNoProviderError 类型判定——错误文本是我们自己拼的，但用子串反推
// 语义意味着上游 4xx 错误体（供应商可控）一旦恰好含同样子串就会被误分。
// 上游 4xx 按语义分流（2026-10-04 小米实测引入）：
//
//	400/413/422 → 400 调用方请求问题（unsupported input_audio.format、
//	              Unknown voice、asr_options.language 非法值等）
//	429        → 429 上游配额
//	401/403    → 502 网关自己那把上游 key 的问题，不是调用方的
//	其余 5xx   → 502
//
// 之前一律 502，客户端（openpocket 的探测缓存、5xx 告警）会把「参数写错」
// 误判成「网关故障」。
func writeAudioError(w http.ResponseWriter, requestID string, err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	var noProvErr *audioNoProviderError
	if errors.As(err, &noProvErr) {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "No audio provider available for model", "server_error", "no_provider")
		return
	}
	var upErr *audioUpstreamStatusError
	if errors.As(err, &upErr) {
		switch {
		case upErr.clientFault():
			writeErrorJSON(w, http.StatusBadRequest, requestID,
				"Audio request rejected by upstream: "+sanitizeAudioErrorMessage(upErr.Error()),
				"invalid_request_error", "upstream_rejected_request")
			return
		case upErr.status == http.StatusTooManyRequests:
			writeErrorJSON(w, http.StatusTooManyRequests, requestID,
				"Upstream audio provider rate limited: "+sanitizeAudioErrorMessage(upErr.Error()),
				"rate_limit_error", "upstream_rate_limited")
			return
		}
	}
	// 桥接前置校验（不支持的音频格式 / voiceclone 缺样本）也是调用方问题。
	if isAudioClientInputErr(err) {
		writeErrorJSON(w, http.StatusBadRequest, requestID, sanitizeAudioErrorMessage(err.Error()),
			"invalid_request_error", "invalid_audio_request")
		return
	}
	writeErrorJSON(w, http.StatusBadGateway, requestID, "Audio upstream failed: "+sanitizeAudioErrorMessage(msg), "server_error", "upstream_error")
}

// isAudioClientInputErr 报告错误是否是本地前置校验判定的调用方问题。
func isAudioClientInputErr(err error) bool {
	var cie *audioClientInputError
	return errors.As(err, &cie)
}

// sanitizeAudioErrorMessage 截断并清洗上游错误文本（供应商体里可能带
// HTML 或超长堆栈），仅保留首行。
func sanitizeAudioErrorMessage(msg string) string {
	if idx := strings.IndexAny(msg, "\n\r"); idx >= 0 {
		msg = msg[:idx]
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
