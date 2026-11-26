// Package streaming — audio_converse.go
//
// POST /v1/audio/converse —— 级联语音会话端点（2026-11-26 语音流式轮）。
//
// 把既有三段能力串成一次调用，多轮语音对话不再要求调用方自行编排三次
// HTTP 往返：
//
//	语音/文本输入 → ASR（Transcribe，音频模态候选）
//	             → LLM（环回 /v1/chat/completions，text 模态路由）
//	             → TTS（Synthesize，音频模态候选；tts_model 缺省跳过）
//
// 复用而非重写的边界（与 refine/analyze 同一原则——拒绝在音频面重新
// 实现一遍 chat 执行器）：
//
//   - ASR/TTS 步直接调 AudioService.Transcribe / Synthesize：候选解析、
//     形态回落（whisper-inference / chat-audio / multipart）、失败台账、
//     计费（chargeTranscription 时长计费 / Synthesize 计费）、并发闸全部
//     现成生效，本层零新增。
//   - LLM 步经 loopbackChatCompletions 转发调用方 Authorization（refine/
//     analyze 的 callLoopbackChat 同款）：chat 面现成的路由/预算/审计/
//     故障转移全量生效，成本归属调用方 key。
//
// 鉴权：AudioService.authenticate 同一口径（Bearer sk-* → Verify →
// RPM → key 预算 → 租户月度预算闸）。装配上跟随 refine/analyze **不包**
// KeyConcurrentGate——环回 chat 步携带同一把调用方 key 已计一次并发，
// 外层再包=双计；直连打爆本端点最终仍被环回 chat 步的同 key 限流拦住
// （authenticateTransform 的同款论证）。
//
// 流式边界（一期取舍，docs/04-implementation/deliverables 有完整说明）：
//
//   - ASR：桥接形态（chat-audio/speech_to_text）逐词 emitDelta →
//     transcript.text.delta 真流式；whisper-inference 透传形态无上游
//     SSE → 转写完成后单个 delta 兜底（与 /v1/audio/transcriptions
//     stream=true 的既有语义一致）。
//   - LLM：环回调用为非流式（loopbackChatCompletions v1 契约），回复
//     全文以单个 reply.text.delta + reply.text.done 发出——SSE 事件序
//     稳定，调用方增量消费逻辑不需要感知「这段回复是一次到的」。
//   - TTS：Synthesize 非流式，整段音频 base64 后以单个 response.audio
//     事件携带。
//
// 请求/响应契约见 serveConverse 与 docs 交付文档；错误信封走
// writeAudioError 同一分流（no_provider 503 / capacity 503 / 上游 4xx
// 分流 / rate_limit 429）。
package streaming

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// maxConverseBodyBytes 限制请求体。语音输入经 base64（4/3 放大）走 JSON，
// 与音频面的 32MiB 原始音频上限同口径：32MiB × 4/3 ≈ 43MiB，向上取整留
// history 文本余量。
const maxConverseBodyBytes = 48 << 20

// maxConverseHistoryTurns 限制 history 消息数。语音会话是转写文本进 LLM，
// 128 轮 × 每轮典型几百 token 远在 chat 面上下文预算之内；更长的会话由
// 调用方自行摘要折叠（refine/analyze 的滚动摘要可复用）。
const maxConverseHistoryTurns = 128

// maxConverseDuration 约束单次会话总时长。三段串行：ASR(10min 上限) +
// 环回 LLM(transform 120s 上限) + TTS(10min 上限) 都在各自面有独立超时，
// 这里给整体一个 12min 的硬顶，防止 trickle 上游把 handler 钉死。
const maxConverseDuration = 12 * time.Minute

// converseAudioInput 是本轮语音输入，input_audio 块同形（base64 data +
// format），与 chat 面 parseOpenAIInputAudioBlock 的调用方心智一致。
type converseAudioInput struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// converseMessage 是 history 里的一条消息（chat 面同形，只收 role/content，
// 语音会话历史是纯文本——音频历史回放不是本端点的职责）。
type converseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// converseRequest 是 POST /v1/audio/converse 的请求体。
type converseRequest struct {
	// ASRModel 可选：空 = 候选解析按 "" 路由（audio 模态默认候选）。
	ASRModel string `json:"asr_model"`
	// ChatModel 必填：环回 chat 面的路由模型名。
	ChatModel string `json:"chat_model"`
	// TTSModel 可选：空 = 跳过 TTS，只回文本。
	TTSModel string            `json:"tts_model"`
	Voice    string            `json:"voice"`
	History  []converseMessage `json:"history"`
	// Audio 与 Text 二选一：Audio 优先（都给时忽略 Text）。
	Audio *converseAudioInput `json:"audio"`
	Text  string              `json:"text"`
	// SystemPrompt 可选：透传给 LLM 的 system 消息。
	SystemPrompt string `json:"system_prompt"`
	Stream       bool   `json:"stream"`
	// Language 可选：透传给 ASR（whisper language 提示）。
	Language string `json:"language"`
}

// converseTranscript 是响应里的转写段。
type converseTranscript struct {
	Text      string `json:"text"`
	Transport string `json:"transport,omitempty"`
}

// converseReply 是响应里的 LLM 段。
type converseReply struct {
	Content string `json:"content"`
	Model   string `json:"model,omitempty"`
}

// converseAudioOut 是响应里的合成音频段（base64）。
type converseAudioOut struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// converseResult 是非流式响应体 / SSE 终态的公共载荷。
type converseResult struct {
	RequestID  string              `json:"request_id"`
	Transcript *converseTranscript `json:"transcript,omitempty"`
	Reply      *converseReply      `json:"reply"`
	Audio      *converseAudioOut   `json:"audio,omitempty"`
}

// ConverseService 是级联编排核。svc 提供 ASR/TTS 与鉴权；环回 chat 复用
// 包级 loopbackChatCompletions。
type ConverseService struct {
	svc *AudioService
	hc  *http.Client
	// baseURL 覆盖环回基址（同 AudioTransformService.baseURL 语义）。
	baseURL string
	// maxTokens 环回 chat 的输出预算（同 transform 的 reasoning 教训）。
	maxTokens int
}

// NewConverseService wires the cascade orchestrator onto the shared audio
// service.
func NewConverseService(svc *AudioService) *ConverseService {
	cs := &ConverseService{
		svc:       svc,
		hc:        &http.Client{Timeout: maxConverseDuration},
		maxTokens: 8192,
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_AUDIO_TRANSFORM_LLM_BASE")); v != "" {
		cs.baseURL = strings.TrimRight(v, "/")
	}
	return cs
}

// ConverseHandler serves POST /v1/audio/converse.
type ConverseHandler struct {
	cs *ConverseService
}

func NewConverseHandler(cs *ConverseService) *ConverseHandler {
	return &ConverseHandler{cs: cs}
}

func (h *ConverseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if h.cs == nil || h.cs.svc == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Audio converse service unavailable", "server_error", "service_unavailable")
		return
	}

	// 数据面鉴权与 /v1/audio/* 同口径（含 RPM 与租户预算闸）。RPM 在此
	// 计一次，环回 chat 步携带同一把 key 会再计一次——一次逻辑调用两次
	// RPM 的取舍与 MCP 面一致（见 authenticateTransform 注释），语音会话
	// 的调用频率远低于周期 analyze，双计可接受且装配上免包 gate。
	keyInfo, ok := h.cs.svc.authenticate(w, r, requestID)
	if !ok {
		return
	}
	if keyInfo != nil {
		r = r.WithContext(withModalityAttemptKey(r.Context(), keyInfo))
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConverseBodyBytes))
	if err != nil {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, requestID, "Request body too large", "invalid_request_error", "request_too_large")
		return
	}
	var req converseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "invalid JSON body: "+err.Error(), "invalid_request_error", "invalid_audio_request")
		return
	}
	if strings.TrimSpace(req.ChatModel) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "chat_model is required", "invalid_request_error", "invalid_model")
		return
	}
	hasAudio := req.Audio != nil && strings.TrimSpace(req.Audio.Data) != ""
	hasText := strings.TrimSpace(req.Text) != ""
	if !hasAudio && !hasText {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "either audio.data or text is required", "invalid_request_error", "invalid_input")
		return
	}
	if len(req.History) > maxConverseHistoryTurns {
		writeErrorJSON(w, http.StatusBadRequest, requestID,
			fmt.Sprintf("history exceeds %d messages", maxConverseHistoryTurns),
			"invalid_request_error", "invalid_history")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxConverseDuration)
	defer cancel()

	// ── 段一：ASR（audio 输入时）────────────────────────────────────
	var transcript *converseTranscript
	if hasAudio {
		format := strings.ToLower(strings.TrimSpace(req.Audio.Format))
		if format == "" {
			format = "wav"
		}
		audioBytes, decErr := base64.StdEncoding.DecodeString(req.Audio.Data)
		if decErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, requestID, "audio.data is not valid base64", "invalid_request_error", "invalid_audio_request")
			return
		}
		tr := TranscribeRequest{
			Model:       req.ASRModel,
			Language:    req.Language,
			File:        audioBytes,
			Filename:    "input." + format,
			ContentType: "audio/" + format,
			RequestID:   requestID,
		}
		if keyInfo != nil {
			tr.TenantID = keyInfo.TenantID
			if keyInfo.DefaultClientProfile != nil {
				tr.Profile = *keyInfo.DefaultClientProfile
			}
		}
		res, terr := h.cs.svc.Transcribe(ctx, tr, nil)
		if terr != nil {
			writeAudioError(w, requestID, terr)
			return
		}
		h.cs.svc.chargeTranscription(ctx, keyInfo, requestID, tr.Model, res.DurationSeconds)
		if strings.TrimSpace(res.Text) == "" {
			writeErrorJSON(w, http.StatusBadRequest, requestID, "transcription produced empty text", "invalid_request_error", "empty_transcript")
			return
		}
		transcript = &converseTranscript{Text: res.Text, Transport: res.Transport}
	}

	// ── 段二：环回 LLM ────────────────────────────────────────────────
	userTurn := req.Text
	if hasAudio {
		userTurn = transcript.Text
	}
	replyContent, replyModel, lerr := h.cs.converseChat(ctx, r, requestID, &req, userTurn)
	if lerr != nil {
		writeAudioError(w, requestID, lerr)
		return
	}
	reply := &converseReply{Content: replyContent, Model: replyModel}

	// ── 段三：TTS（tts_model 给了才做）───────────────────────────────
	var audioOut *converseAudioOut
	if strings.TrimSpace(req.TTSModel) != "" {
		syn := SynthesizeRequest{
			Model:     req.TTSModel,
			Input:     replyContent,
			Voice:     req.Voice,
			RequestID: requestID,
		}
		if keyInfo != nil {
			syn.TenantID = keyInfo.TenantID
			if keyInfo.DefaultClientProfile != nil {
				syn.Profile = *keyInfo.DefaultClientProfile
			}
		}
		res, serr := h.cs.svc.Synthesize(ctx, syn)
		if serr != nil {
			writeAudioError(w, requestID, serr)
			return
		}
		audioOut = &converseAudioOut{
			Data:   base64.StdEncoding.EncodeToString(res.Audio),
			Format: converseAudioFormat(res.ContentType),
		}
	}

	out := converseResult{RequestID: requestID, Transcript: transcript, Reply: reply, Audio: audioOut}

	if req.Stream {
		h.serveStream(w, r, requestID, &out)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// converseChat 组装 history + 本轮输入并环回 chat 面。返回 content 与实际
// 上游模型名。
func (cs *ConverseService) converseChat(ctx context.Context, r *http.Request, requestID string, req *converseRequest, userTurn string) (string, string, error) {
	endpoint := cs.baseURL
	if endpoint == "" {
		scheme := "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
			scheme = "https"
		} else if r.TLS != nil {
			scheme = "https"
		}
		host := r.Host
		if host == "" {
			return "", "", newAudioClientInputError("cannot derive loopback endpoint: request has no Host")
		}
		endpoint = scheme + "://" + host
	}
	messages := make([]map[string]string, 0, len(req.History)+2)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.SystemPrompt})
	}
	for _, m := range req.History {
		role := strings.TrimSpace(m.Role)
		if role != "user" && role != "assistant" && role != "system" {
			return "", "", newAudioClientInputError("history role must be user/assistant/system, got %q", m.Role)
		}
		if strings.TrimSpace(m.Content) == "" {
			return "", "", newAudioClientInputError("history message content must be non-empty")
		}
		messages = append(messages, map[string]string{"role": role, "content": m.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": userTurn})
	payload := map[string]any{
		"model":      req.ChatModel,
		"messages":   messages,
		"stream":     false,
		"max_tokens": cs.maxTokens,
	}
	return loopbackChatCompletions(ctx, cs.hc, endpoint, r.Header.Get("Authorization"), "audio_converse", requestID, payload)
}

// serveStream 以 SSE 发出最终结果。事件序固定：
//
//	（如有转写）transcript.transport → transcript.text.done
//	reply.text.delta → reply.text.done
//	（如有合成）response.audio
//	data: [DONE]
//
// 一期环回 chat 是非流式调用，reply 增量天然只有一段；事件名与增量消费
// 逻辑对齐 OpenAI/既有 transcript.* 口径，后续二期把环回换流式时只在
// emitDelta 处多发几个 reply.text.delta，事件契约不变。
func (h *ConverseHandler) serveStream(w http.ResponseWriter, r *http.Request, requestID string, out *converseResult) {
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

	if out.Transcript != nil {
		if out.Transcript.Transport != "" {
			writeEvent("transcript.transport", map[string]string{"type": "transcript.transport", "transport": out.Transcript.Transport})
		}
		writeEvent("transcript.text.done", map[string]string{"type": "transcript.text.done", "text": out.Transcript.Text})
	}
	if out.Reply != nil && out.Reply.Content != "" {
		writeEvent("reply.text.delta", map[string]string{"type": "reply.text.delta", "delta": out.Reply.Content})
		writeEvent("reply.text.done", map[string]string{"type": "reply.text.done", "text": out.Reply.Content})
	}
	if out.Audio != nil {
		writeEvent("response.audio", map[string]any{"type": "response.audio", "audio": out.Audio})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// converseAudioFormat 从上游 Content-Type 推导音频格式名（X-Gw-Audio-*
// 与 macos-say 垫片的 wav/m4a 口径）。
func converseAudioFormat(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.Contains(ct, "wav"):
		return "wav"
	case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
		return "mp3"
	case strings.Contains(ct, "m4a"), strings.Contains(ct, "mp4"):
		return "m4a"
	case strings.Contains(ct, "opus"):
		return "opus"
	case strings.Contains(ct, "flac"):
		return "flac"
	case strings.Contains(ct, "pcm"):
		return "pcm"
	default:
		return "wav"
	}
}
