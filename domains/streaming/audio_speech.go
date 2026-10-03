// Package streaming — audio_speech.go
//
// POST /v1/audio/speech —— OpenAI 兼容的语音合成（TTS）端点。
//
// 请求 JSON {model, input, voice, response_format, speed}；响应是
// audio/* 二进制（小米 chat-audio 桥接返回 24kHz16bit WAV）。
// X-Gw-Audio-Transport 响应头标记实际传输形态。
//
// 小米桥接的音色集合（mimo_default/冰糖/茉莉/苏打/白桦/Mia/Chloe/
// Milo/Dean）之外的 voice 会归一到 mimo_default；客户端传 OpenAI 标准
// 音色（alloy 等）时不必换配置即可出声。
package streaming

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// AudioSpeechHandler proxies OpenAI-compatible TTS requests.
type AudioSpeechHandler struct {
	svc *AudioService
}

func NewAudioSpeechHandler(svc *AudioService) *AudioSpeechHandler {
	return &AudioSpeechHandler{svc: svc}
}

func (h *AudioSpeechHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Audio speech service unavailable", "server_error", "service_unavailable")
		return
	}
	keyInfo, ok := h.svc.authenticate(w, r, requestID)
	if !ok {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, requestID, "Request body too large", "invalid_request_error", "request_too_large")
		return
	}
	var payload struct {
		Model          string  `json:"model"`
		Input          string  `json:"input"`
		Voice          string  `json:"voice"`
		ResponseFormat string  `json:"response_format"`
		Speed          float64 `json:"speed"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "Invalid JSON body", "invalid_request_error", "invalid_json")
		return
	}
	if strings.TrimSpace(payload.Model) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "model is required", "invalid_request_error", "invalid_model")
		return
	}
	if strings.TrimSpace(payload.Input) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "input must be a non-empty string", "invalid_request_error", "invalid_input")
		return
	}
	speed := ""
	if payload.Speed > 0 {
		speed = jsonNumberString(payload.Speed)
	}

	req := SynthesizeRequest{
		Model:          payload.Model,
		Input:          payload.Input,
		Voice:          payload.Voice,
		ResponseFormat: payload.ResponseFormat,
		Speed:          speed,
	}
	if keyInfo != nil {
		req.TenantID = keyInfo.TenantID
		if keyInfo.DefaultClientProfile != nil {
			req.Profile = *keyInfo.DefaultClientProfile
		}
	}
	req.RequestID = requestID

	res, serr := h.svc.Synthesize(r.Context(), req)
	if serr != nil {
		writeAudioError(w, requestID, serr)
		return
	}
	w.Header().Set("Content-Type", res.ContentType)
	w.Header().Set("X-Gw-Audio-Transport", res.Transport)
	w.Header().Set("X-Gw-Upstream-Model", res.UpstreamModel)
	// 客户端传了但这条上游形态兑现不了的参数要如实回执（当前只有 speed：
	// 小米 chat 协议无语速旋钮，实测带不带 speed 得到逐字节相同的音频）。
	// 静默忽略会让调用方以为语速已生效。
	if len(res.IgnoredParams) > 0 {
		w.Header().Set("X-Gw-Audio-Ignored-Params", strings.Join(res.IgnoredParams, ","))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Audio)
}

// jsonNumberString renders a float without scientific notation for the
// speed field relay.
func jsonNumberString(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
