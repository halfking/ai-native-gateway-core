// Package streaming — audio_service.go
//
// 音频端点（/v1/audio/transcriptions、/v1/audio/speech、/v1/mcp）共享的
// 服务核：候选解析、上游适配（multipart 透传 + chat-audio 桥接）、鉴权。
//
// 为什么按两种传输形态适配（2026-10-03 小米上游实测）：
//
//	形态 A（multipart 透传）  POST {base}/audio/transcriptions —— OpenAI
//	                          官方/兼容（groq、openrouter、智谱 /v4）走这条。
//	形态 B（chat-audio 桥接） POST {base}/chat/completions，音频以
//	                          input_audio base64 块承载 —— 小米 MiMo 只支持
//	                          这条：/v1/audio/transcriptions 在
//	                          token-plan-cn.xiaomimimo.com 上是 404，而
//	                          chat+input_audio 转写完全可用。
//
// 两种形态共用 provider.GetCandidatesByModality(model, "audio") 的候选集，
// 逐候选 failover（与 embeddings handler 同款循环，不经过 URSM 权威视图
// ——音频端点自带逐候选重试，且探针对音频模型的形态适配是另一条修复线，
// 两层互不阻塞）。
package streaming

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/provider"
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

const (
	// maxAudioUploadBytes 与 chat 请求体上限一致（32MiB）。OpenAI 官方
	// /audio/transcriptions 是 25MB，这里放宽到 chat 同级以容纳 base64
	// 膨胀前的原始文件；上游各自的更严限制由上游 4xx 透出。
	maxAudioUploadBytes = 32 << 20

	// maxAudioResponseBytes 上限：TTS 长文本合成的 WAV 可以到几十 MB。
	maxAudioResponseBytes = 96 << 20

	// audioModalityRequest 是音频端点向候选解析器声明的模态过滤值。
	audioModalityRequest = "audio"
)

// Transport 表示一次音频请求实际使用的上游传输形态。响应头
// X-Gw-Audio-Transport 会带上它，客户端（如 openpocket 的探测缓存）
// 可以据此区分两种形态而不用探测。
const (
	AudioTransportTranscriptions = "transcriptions"
	AudioTransportChatAudio      = "chat-audio"
)

// AudioProviderResolver mirrors embeddingProviderResolver — kept separate so
// the two features can evolve their resolver contracts independently.
type AudioProviderResolver interface {
	GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error)
}

// AudioKeyVerifier is the data-plane key check shared by all audio endpoints.
type AudioKeyVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, rawKey string) (*authentication.KeyInfo, error)
	CheckBudget(ctx context.Context, keyID int) error
}

// AudioService is the shared core behind the audio HTTP handlers.
type AudioService struct {
	provider    AudioProviderResolver
	upstream    *upstream.Client
	keyVerifier AudioKeyVerifier
	rateLimiter ratelimit.RPMLimiter
}

func NewAudioService(resolver AudioProviderResolver, upstreamClient *upstream.Client) *AudioService {
	return &AudioService{provider: resolver, upstream: upstreamClient}
}

// SetAuth wires the data-plane key verifier and shared rate limiter.
func (s *AudioService) SetAuth(keyVerifier *authentication.KeyVerifier, rateLimiter ratelimit.RPMLimiter) {
	s.keyVerifier = keyVerifier
	s.rateLimiter = rateLimiter
}

// TranscribeRequest is one transcription call, transport-agnostic.
type TranscribeRequest struct {
	Model          string
	Language       string
	Prompt         string
	ResponseFormat string // json | text | verbose_json | srt | vtt
	Temperature    string
	Stream         bool
	File           []byte
	Filename       string
	ContentType    string

	TenantID string
	Profile  string
}

// TranscribeResult carries the normalized outcome of one transcription.
type TranscribeResult struct {
	Text            string
	Transport       string
	UpstreamModel   string
	DurationSeconds *float64
	// RelayBody 非空时（multipart 透传形态）直接作为响应体；此时 Text
	// 已从 RelayBody 解析出来用于流式聚合与日志。
	RelayBody       []byte
	RelayIsSSE      bool
	RelayIsTextOnly bool
}

// SynthesizeRequest is one TTS call.
type SynthesizeRequest struct {
	Model          string
	Input          string
	Voice          string
	ResponseFormat string
	Speed          string

	TenantID string
	Profile  string
}

// SynthesizeResult is the synthesized audio payload.
type SynthesizeResult struct {
	Audio          []byte
	ContentType    string
	Transport      string
	UpstreamModel  string
	TranscriptHint string
}

// audioCandidateSelection 是候选过滤后的可用列表：与 embeddings 同款
// 门槛（Anthropic 协议无音频面、无 key / 不可用的候选跳过）。
func audioCandidateSelection(candidates []provider.Candidate) []provider.Candidate {
	usable := make([]provider.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if !c.IsAvailable() || c.APIKey == "" {
			continue
		}
		if c.Protocol == providercatalog.ProtocolAnthropicMessages {
			continue
		}
		usable = append(usable, c)
	}
	return usable
}

// preferChatAudioBridge 报告该候选是否应直接走 chat-audio 桥接（跳过
// multipart 透传的 404 往返）。小米实测其 /v1/audio/transcriptions 不存在；
// 已知集合外的新供应商先试标准路径、404/405 再回落桥接，零成本兼容。
func preferChatAudioBridge(c provider.Candidate) bool {
	return strings.EqualFold(strings.TrimSpace(c.CatalogCode), "xiaomi")
}

// audioFileFormat 从文件名/Content-Type 推断 input_audio.format。
// 小米仅接受 wav|mp3，其它格式原样透传、由上游错误明示。
func audioFileFormat(filename, contentType string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(filename), "."))
	switch ext {
	case "wav", "mp3", "webm", "ogg", "oga", "m4a", "mp4", "flac", "aac", "opus":
		return ext
	}
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		if frag := strings.TrimPrefix(mt, "audio/"); frag != mt && frag != "" {
			return strings.ToLower(frag)
		}
	}
	return "wav"
}

// Transcribe executes the transcription across candidates with failover.
// emitDelta (non-nil only for streaming callers) receives incremental text.
func (s *AudioService) Transcribe(ctx context.Context, req TranscribeRequest, emitDelta func(string)) (*TranscribeResult, error) {
	if s == nil || s.provider == nil || s.upstream == nil {
		return nil, fmt.Errorf("audio service not configured")
	}
	candidates, _, err := s.provider.GetCandidatesByModality(ctx, req.Model, req.Profile, req.TenantID, audioModalityRequest)
	if err != nil {
		return nil, fmt.Errorf("resolve candidates: %w", err)
	}
	usable := audioCandidateSelection(candidates)
	if len(usable) == 0 {
		return nil, fmt.Errorf("no audio provider available for model %q", req.Model)
	}
	format := audioFileFormat(req.Filename, req.ContentType)

	var lastErr error
	for _, cand := range usable {
		// 桥接优先（小米）或透传优先（其余），另一形态按 404/405/415 回落。
		order := []string{AudioTransportTranscriptions, AudioTransportChatAudio}
		if preferChatAudioBridge(cand) {
			order = []string{AudioTransportChatAudio}
		}
		for _, transport := range order {
			var res *TranscribeResult
			var err error
			if transport == AudioTransportChatAudio {
				res, err = s.transcribeViaChatAudio(ctx, cand, req, format, emitDelta)
			} else {
				res, err = s.transcribeViaMultipart(ctx, cand, req, emitDelta)
			}
			if err == nil {
				return res, nil
			}
			lastErr = err
			if !isAudioTransportFallbackErr(err) {
				// 非「形态不存在」类错误（429/5xx/网络/内容拒绝）→ 换候选。
				break
			}
			// 404/405/415：该候选没有这种端点形态，试下一形态/候选。
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all audio providers failed")
	}
	return nil, lastErr
}

// isAudioTransportFallbackErr 报告错误是否属于「该端点形态在上游不存在」，
// 应该换另一种传输形态重试而不是换候选。小米的 /audio/transcriptions
// 回 openresty 404 HTML；有些兼容层回 405；415 形态不支持也归入。
type audioTransportError struct{ status int }

func (e *audioTransportError) Error() string {
	return fmt.Sprintf("transport endpoint missing (http %d)", e.status)
}

func isAudioTransportFallbackErr(err error) bool {
	te, ok := err.(*audioTransportError)
	return ok && (te.status == http.StatusNotFound || te.status == http.StatusMethodNotAllowed || te.status == http.StatusUnsupportedMediaType)
}

// transcribeViaMultipart relays the multipart body to the upstream's
// OpenAI-compatible /audio/transcriptions endpoint.
func (s *AudioService) transcribeViaMultipart(ctx context.Context, cand provider.Candidate, req TranscribeRequest, emitDelta func(string)) (*TranscribeResult, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", req.Filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(req.File); err != nil {
		return nil, err
	}
	_ = mw.WriteField("model", cand.RawModel)
	if req.Language != "" {
		_ = mw.WriteField("language", req.Language)
	}
	if req.Prompt != "" {
		_ = mw.WriteField("prompt", req.Prompt)
	}
	if req.ResponseFormat != "" {
		_ = mw.WriteField("response_format", req.ResponseFormat)
	}
	if req.Temperature != "" {
		_ = mw.WriteField("temperature", req.Temperature)
	}
	if req.Stream {
		// stream 与 srt/vtt 等 response_format 在 OpenAI 上互斥，仅在
		// 未显式指定格式或格式为 json 时携带。
		if req.ResponseFormat == "" || req.ResponseFormat == "json" {
			_ = mw.WriteField("stream", "true")
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(cand.BaseURL, "/") + "/audio/transcriptions"
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Authorization", "Bearer "+cand.APIKey)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upReq.Header.Set("X-Gateway-Internal-Purpose", "audio_transcription")

	resp, upErr := s.upstream.Do(upReq)
	if upErr != nil {
		return nil, upErr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusUnsupportedMediaType {
		return nil, &audioTransportError{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readLimitedResponse(resp.Body, 8<<10)
		return nil, fmt.Errorf("upstream http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := readLimitedResponse(resp.Body, maxAudioResponseBytes)
	if err != nil {
		return nil, err
	}
	ct := resp.Header.Get("Content-Type")
	res := &TranscribeResult{
		Transport:     AudioTransportTranscriptions,
		UpstreamModel: cand.RawModel,
		RelayBody:     body,
	}
	if strings.Contains(ct, "text/event-stream") {
		res.RelayIsSSE = true
		// 透传形态的 SSE 交给 handler 原样转发；这里不做逐事件解析。
		return res, nil
	}
	if req.ResponseFormat == "text" || req.ResponseFormat == "srt" || req.ResponseFormat == "vtt" {
		res.RelayIsTextOnly = true
		res.Text = string(body)
		return res, nil
	}
	var parsed struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if jerr := json.Unmarshal(body, &parsed); jerr == nil {
		res.Text = parsed.Text
		if parsed.Model != "" {
			res.UpstreamModel = parsed.Model
		}
	}
	return res, nil
}

// transcribeViaChatAudio bridges the audio into a chat/completions call with
// an input_audio content block (Xiaomi MiMo shape, verified 2026-10-03).
//
// 桥接的两个实测约束（小米）：
//   - 不能带 text part（上游 400 "must not include text parts"）→ language/
//     prompt 无法透传，文档明示在该形态下被忽略；
//   - format 仅 wav|mp3，其它格式由上游 400 明示。
func (s *AudioService) transcribeViaChatAudio(ctx context.Context, cand provider.Candidate, req TranscribeRequest, format string, emitDelta func(string)) (*TranscribeResult, error) {
	payload := map[string]any{
		"model": cand.RawModel,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type":        "input_audio",
				"input_audio": map[string]string{"data": base64.StdEncoding.EncodeToString(req.File), "format": format},
			}},
		}},
	}
	streaming := req.Stream && emitDelta != nil
	if streaming {
		payload["stream"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		upstreamurl.Build(cand.BaseURL, upstreamurl.EpChatCompletions), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Authorization", "Bearer "+cand.APIKey)
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Accept", "application/json")
	upReq.Header.Set("X-Gateway-Internal-Purpose", "audio_transcription")

	resp, upErr := s.upstream.Do(upReq)
	if upErr != nil {
		return nil, upErr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, &audioTransportError{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := readLimitedResponse(resp.Body, 8<<10)
		return nil, fmt.Errorf("upstream http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	res := &TranscribeResult{Transport: AudioTransportChatAudio, UpstreamModel: cand.RawModel}
	if streaming {
		text, seconds, serr := relayChatAudioSSE(ctx, resp.Body, emitDelta)
		if serr != nil {
			return nil, serr
		}
		res.Text = text
		res.DurationSeconds = seconds
		return res, nil
	}
	raw, err := readLimitedResponse(resp.Body, maxAudioResponseBytes)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Seconds float64 `json:"seconds"`
		} `json:"usage"`
	}
	if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
		return nil, fmt.Errorf("chat-audio bridge response is not JSON: %w", jerr)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("chat-audio bridge response has no choices")
	}
	res.Text = parsed.Choices[0].Message.Content
	if parsed.Usage.Seconds > 0 {
		res.DurationSeconds = &parsed.Usage.Seconds
	}
	return res, nil
}

// Synthesize executes TTS across candidates with failover.
func (s *AudioService) Synthesize(ctx context.Context, req SynthesizeRequest) (*SynthesizeResult, error) {
	if s == nil || s.provider == nil || s.upstream == nil {
		return nil, fmt.Errorf("audio service not configured")
	}
	if strings.TrimSpace(req.Input) == "" {
		return nil, fmt.Errorf("input must be a non-empty string")
	}
	candidates, _, err := s.provider.GetCandidatesByModality(ctx, req.Model, req.Profile, req.TenantID, audioModalityRequest)
	if err != nil {
		return nil, fmt.Errorf("resolve candidates: %w", err)
	}
	usable := audioCandidateSelection(candidates)
	if len(usable) == 0 {
		return nil, fmt.Errorf("no audio provider available for model %q", req.Model)
	}

	var lastErr error
	for _, cand := range usable {
		order := []string{AudioTransportTranscriptions, AudioTransportChatAudio}
		if preferChatAudioBridge(cand) {
			order = []string{AudioTransportChatAudio}
		}
		for _, transport := range order {
			var res *SynthesizeResult
			var err error
			if transport == AudioTransportChatAudio {
				res, err = s.synthesizeViaChatAudio(ctx, cand, req)
			} else {
				res, err = s.synthesizeViaSpeech(ctx, cand, req)
			}
			if err == nil {
				return res, nil
			}
			lastErr = err
			if !isAudioTransportFallbackErr(err) {
				break
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all audio providers failed")
	}
	return nil, lastErr
}

// synthesizeViaSpeech relays to the upstream's OpenAI-compatible
// POST /audio/speech endpoint.
func (s *AudioService) synthesizeViaSpeech(ctx context.Context, cand provider.Candidate, req SynthesizeRequest) (*SynthesizeResult, error) {
	payload := map[string]any{
		"model":           cand.RawModel,
		"input":           req.Input,
		"response_format": audioTTSEffectiveFormat(req.ResponseFormat),
	}
	if v := normalizeTTSVoiceForCandidate(cand, req.Voice); v != "" {
		payload["voice"] = v
	}
	if req.Speed != "" {
		payload["speed"] = req.Speed
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cand.BaseURL, "/")+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Authorization", "Bearer "+cand.APIKey)
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("X-Gateway-Internal-Purpose", "audio_speech")

	resp, upErr := s.upstream.Do(upReq)
	if upErr != nil {
		return nil, upErr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusUnsupportedMediaType {
		return nil, &audioTransportError{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := readLimitedResponse(resp.Body, 8<<10)
		return nil, fmt.Errorf("upstream http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	audio, err := readLimitedResponse(resp.Body, maxAudioResponseBytes)
	if err != nil {
		return nil, err
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.Contains(ct, "application/json") {
		ct = "audio/" + audioTTSEffectiveFormat(req.ResponseFormat)
	}
	return &SynthesizeResult{Audio: audio, ContentType: ct, Transport: AudioTransportTranscriptions, UpstreamModel: cand.RawModel}, nil
}

// xiaomiVoices 是小米 TTS 的合法音色集合（上游 400 "Unknown voice" 的错误
// 体里原样给出的清单，2026-10-03 实测）。之外的 voice（如 OpenAI 的
// alloy 系）会被小米 400 拒收，归一到默认音色而不是让请求失败。
var xiaomiVoices = map[string]bool{
	"mimo_default": true, "冰糖": true, "茉莉": true, "苏打": true, "白桦": true,
	"mia": true, "chloe": true, "milo": true, "dean": true,
}

// normalizeTTSVoiceForCandidate 把客户端音色归一到候选供应商可接受的值。
// 空值/未知值 → 该供应商默认（小米 mimo_default）；透传形态下保持原值
// （空值让上游走默认）。
func normalizeTTSVoiceForCandidate(cand provider.Candidate, voice string) string {
	v := strings.TrimSpace(voice)
	if preferChatAudioBridge(cand) {
		if v == "" || !xiaomiVoices[strings.ToLower(v)] {
			return "mimo_default"
		}
		return v
	}
	return v
}

// audioTTSEffectiveFormat 归一 TTS response_format；默认 wav（小米桥接
// 返回 24kHz16bit WAV；OpenAI 默认 mp3，但显式 wav 两边都合法）。
func audioTTSEffectiveFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "mp3", "opus", "aac", "flac", "wav", "pcm":
		return strings.ToLower(strings.TrimSpace(f))
	default:
		return "wav"
	}
}

// synthesizeViaChatAudio bridges TTS through chat/completions with
// modalities ["text","audio"]（小米 MiMo 形态，2026-10-03 实测）：
//
//   - 文本必须放 assistant 角色消息（上游 400 "messages must contain an
//     assistant role for TTS model"）；
//   - audio.voice 可省略（走默认音色）；带非法音色名会 400，因此先归一；
//   - 响应 choices[0].message.audio.data 是 base64 音频（WAV 24kHz）。
func (s *AudioService) synthesizeViaChatAudio(ctx context.Context, cand provider.Candidate, req SynthesizeRequest) (*SynthesizeResult, error) {
	format := audioTTSEffectiveFormat(req.ResponseFormat)
	payload := map[string]any{
		"model":      cand.RawModel,
		"modalities": []string{"text", "audio"},
		"audio":      map[string]string{"voice": normalizeTTSVoiceForCandidate(cand, req.Voice), "format": format},
		"messages":   []map[string]any{{"role": "assistant", "content": req.Input}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		upstreamurl.Build(cand.BaseURL, upstreamurl.EpChatCompletions), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Authorization", "Bearer "+cand.APIKey)
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("X-Gateway-Internal-Purpose", "audio_speech")

	resp, upErr := s.upstream.Do(upReq)
	if upErr != nil {
		return nil, upErr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, &audioTransportError{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := readLimitedResponse(resp.Body, 8<<10)
		return nil, fmt.Errorf("upstream http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	raw, err := readLimitedResponse(resp.Body, maxAudioResponseBytes)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Audio struct {
					Data       string `json:"data"`
					Transcript string `json:"transcript"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
		return nil, fmt.Errorf("chat-audio TTS response is not JSON: %w", jerr)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Audio.Data == "" {
		return nil, fmt.Errorf("chat-audio TTS response carries no audio data")
	}
	audio, derr := base64.StdEncoding.DecodeString(parsed.Choices[0].Message.Audio.Data)
	if derr != nil {
		return nil, fmt.Errorf("chat-audio TTS audio payload is not base64: %w", derr)
	}
	return &SynthesizeResult{
		Audio:          audio,
		ContentType:    "audio/" + format,
		Transport:      AudioTransportChatAudio,
		UpstreamModel:  cand.RawModel,
		TranscriptHint: parsed.Choices[0].Message.Audio.Transcript,
	}, nil
}

// relayChatAudioSSE reads the upstream chat SSE stream, forwarding each
// delta.content chunk through emitDelta and returning the aggregated text.
// 事件形状（OpenAI 转写流式口径，与智谱 glm-asr SSE 同名）由 handler 落盘：
//
//	event: transcript.text.delta  {"type":"transcript.text.delta","delta":...}
func relayChatAudioSSE(ctx context.Context, body io.Reader, emitDelta func(string)) (string, *float64, error) {
	var aggregated strings.Builder
	var seconds *float64
	reader := newSSELineReader(ctx, body)
	for reader.Next() {
		event, data, ok := reader.Event()
		if !ok {
			continue
		}
		_ = event
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				Seconds float64 `json:"seconds"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage.Seconds > 0 {
			v := chunk.Usage.Seconds
			seconds = &v
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				aggregated.WriteString(ch.Delta.Content)
				emitDelta(ch.Delta.Content)
			}
		}
	}
	if err := reader.Err(); err != nil {
		return aggregated.String(), seconds, err
	}
	return aggregated.String(), seconds, nil
}

// authenticate 复刻 embeddings 的数据面鉴权链（key 校验 → 节流 → 限流 →
// 预算）。三个音频面（transcriptions/speech/mcp）共用。
func (s *AudioService) authenticate(w http.ResponseWriter, r *http.Request, requestID string) (*authentication.KeyInfo, bool) {
	if s.keyVerifier == nil || !s.keyVerifier.Enabled() {
		return nil, true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Missing API key", "authentication_error", "missing_key")
		return nil, false
	}
	keyInfo, err := s.keyVerifier.Verify(r.Context(), rawKey)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Invalid or expired API key", "authentication_error", "invalid_key")
		return nil, false
	}
	if keyInfo.Status == "throttled" {
		ratelimit.MarkGatewaySharedKeyRateLimit(w)
		writeErrorJSON(w, http.StatusTooManyRequests, requestID, "API key throttled", "rate_limit_error", "key_throttled")
		return nil, false
	}
	if outcome := checkGatewayRateLimit(r.Context(), keyInfo, s.rateLimiter, nil); !outcome.Skipped {
		writeRateLimitHeaders(w, outcome)
		if outcome.Blocked {
			ratelimit.MarkGatewaySharedKeyRateLimit(w)
			writeErrorJSON(w, http.StatusTooManyRequests, requestID, "Rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
			return nil, false
		}
	}
	if err := s.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); err != nil {
		if _, exceeded := err.(*authentication.BudgetExceededError); exceeded {
			writeErrorJSON(w, http.StatusPaymentRequired, requestID, "Budget exhausted", "insufficient_quota", "budget_exhausted")
			return nil, false
		}
		slog.Error("audio budget check failed, failing closed",
			"request_id", requestID,
			"key_id", keyInfo.ID,
			"error", err)
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Budget check temporarily unavailable", "server_error", "budget_unavailable")
		return nil, false
	}
	return keyInfo, true
}

// sseLineReader 是一个极简 SSE 事件读取器（event:/data: 行、空行分块），
// 供桥接流式转写消费上游 chat SSE。streaming 包内已有多个面向 chat 的
// SSE 解析器（带 [DONE]/注释/keepalive 语义差异），这里只需要最小子集，
// 独立实现避免耦合。
type sseLineReader struct {
	sc    *bufio.Scanner
	curEv string
	curDa string
	has   bool
	done  bool
	err   error
}

func newSSELineReader(ctx context.Context, body io.Reader) *sseLineReader {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	return &sseLineReader{sc: sc}
}

// Next 读取下一个事件块（空行分隔），返回是否还有事件。
func (r *sseLineReader) Next() bool {
	r.curEv, r.curDa, r.has = "", "", false
	for r.sc.Scan() {
		line := strings.TrimRight(r.sc.Text(), "\r")
		switch {
		case line == "":
			if r.has {
				return true
			}
		case strings.HasPrefix(line, ":"):
			// SSE 注释/keepalive，跳过
		case strings.HasPrefix(line, "event:"):
			r.curEv = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if r.curDa == "" {
				r.curDa = d
			} else {
				r.curDa += "\n" + d
			}
			r.has = true
		}
	}
	if err := r.sc.Err(); err != nil {
		r.err = err
		return false
	}
	// 流结束：末尾没有空行收尾的残余块仍然交付。
	r.done = true
	return r.has
}

// Event 返回当前事件名与 data（ok=false 表示这个块没有 data 行）。
func (r *sseLineReader) Event() (event, data string, ok bool) {
	return r.curEv, r.curDa, r.has
}

// Err 返回底层读取错误。
func (r *sseLineReader) Err() error { return r.err }
