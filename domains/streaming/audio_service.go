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
	"time"

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

	// maxAudioRequestDuration 约束单次音频调用（转写/合成）的总时长。
	// 上游读体没有服务端 deadline（gateway 的 WriteTimeout=0，upstream
	// client 只有 ResponseHeaderTimeout），一个 trickling 上游可无限期
	// 钉住 handler 与已缓冲的音频内存；客户端断连能中止（r.Context()
	// 取消传播到上游请求），但 MCP 客户端常不设超时。10 分钟按
	// 30s 音频转写 + 慢 TTS 留足余量（39 轮 2026-10-03 审计 P2-3）。
	maxAudioRequestDuration = 10 * time.Minute

	// maxConcurrentAudioOps 限制同时进行的音频转写/合成调用数。音频路径
	// 在内存里同时持有原始音频（≤32MiB）+ base64 放大副本（4/3×）+
	// 重打包体（透传形态再一份），最坏单请求 ~240MiB；限流是 RPM 语义、
	// 分钟窗内可突发，无闸门时并发突发直接打爆内存（39 轮 P2-2）。
	// 8 路并发 × 最坏 ~240MiB ≈ 1.9GiB 峰值预算，超出返回 429/503
	// 由上层映射。探针流量也走这里，容量满时探针红牌是正确语义。
	maxConcurrentAudioOps = 8
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
	// sem 限制并发音频调用（maxConcurrentAudioOps），构造期一次性建立。
	sem chan struct{}
}

func NewAudioService(resolver AudioProviderResolver, upstreamClient *upstream.Client) *AudioService {
	return &AudioService{
		provider: resolver,
		upstream: upstreamClient,
		sem:      make(chan struct{}, maxConcurrentAudioOps),
	}
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
	// IgnoredParams 列出客户端传了、但这条上游形态兑现不了的参数
	// （chat-audio 桥接下 prompt/temperature 无处安放、language 归一后
	// 落空）。handler 走 X-Gw-Audio-Ignored-Params 响应头回执，流式走
	// transcript.ignored_params 事件。静默丢弃会让调用方以为提示词已经
	// 参与识别——领域词汇提示被吞掉时，转写结果只是「看起来不准」。
	IgnoredParams []string
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
	// IgnoredParams 列出客户端传了、但这条上游形态无法兑现的参数
	// （当前只有 TTS speed——上游 chat 协议没有语速旋钮，2026-10-04 实测
	// 传了也不生效）。handler 用 X-Gw-Audio-Ignored-Params 响应头回执，
	// 免得客户端以为语速已生效。
	IgnoredParams []string
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

// xiaomiASRAudioFormats 是 MiMo ASR 接受的 input_audio.format 白名单。
//
// 2026-10-04 直连实测（token-plan-cn）：非白名单值被上游 400 拒收，错误体
// 原样给出 "input_audio.format must be one of: wav, mp3. Got: m4a"；官方
// 文档也只列 mp3（audio/mpeg、audio/mp3）与 wav（audio/wav）。
var xiaomiASRAudioFormats = map[string]bool{"wav": true, "mp3": true}

// audioFileFormat 从文件名/Content-Type 推断 input_audio.format。
// 白名单之外的扩展名原样透传；chat-audio 桥接形态（小米）会在发送前被
// 前置校验拦成本地 400（见 transcribeViaChatAudio），multipart 透传形态
// 则由上游错误明示。
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
	ctx, cancel := context.WithTimeout(ctx, maxAudioRequestDuration)
	defer cancel()
	if err := s.acquireAudioSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseAudioSlot()
	candidates, _, err := s.provider.GetCandidatesByModality(ctx, req.Model, req.Profile, req.TenantID, audioModalityRequest)
	if err != nil {
		return nil, newAudioNoProviderError("resolve candidates: %s", err)
	}
	usable := audioCandidateSelection(candidates)
	if len(usable) == 0 {
		return nil, newAudioNoProviderError("no audio provider available for model %q", req.Model)
	}
	format := audioFileFormat(req.Filename, req.ContentType)

	// 39 轮（P1-1）：一旦向客户端发出过转写增量，当前候选的流中失败必须
	// 直接上抛——换候选重跑会让客户端收到两段拼接的增量流，且截断文本
	// 可能被误当成功结果。emitted 闩锁由包装的 emit 记录。
	emitted := false
	emit := emitDelta
	if emitDelta != nil {
		emit = func(delta string) {
			emitted = true
			emitDelta(delta)
		}
	}

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
				res, err = s.transcribeViaChatAudio(ctx, cand, req, format, emit)
			} else {
				res, err = s.transcribeViaMultipart(ctx, cand, req, emit)
			}
			if err == nil {
				return res, nil
			}
			lastErr = err
			if emitted {
				// 部分增量已交付，重试会产生重复输出；错误交给 handler
				// 以 SSE error 事件（或 502 envelope）如实透出。
				return nil, err
			}
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

// acquireAudioSlot 占用一个并发音频槽位；ctx 取消（客户端断连/整体
// deadline）时让位返回错误而不是排队堆积。nil 信号量（零值构造的
// service，仅测试形态）直接放行。
func (s *AudioService) acquireAudioSlot(ctx context.Context) error {
	if s.sem == nil {
		return nil
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("audio capacity wait aborted: %w", ctx.Err())
	}
}

// releaseAudioSlot 与 acquireAudioSlot 配对；独立成方法让 defer 释放
// 不依赖调用方持有同一分支。
func (s *AudioService) releaseAudioSlot() {
	if s.sem != nil {
		<-s.sem
	}
}

// audioTransportError 表示「该端点形态在上游不存在」，应该换另一种传输
// 形态重试而不是换候选。小米的 /audio/transcriptions 回 openresty 404
// HTML；有些兼容层回 405；415 形态不支持也归入。
type audioTransportError struct{ status int }

func (e *audioTransportError) Error() string {
	return fmt.Sprintf("transport endpoint missing (http %d)", e.status)
}

func isAudioTransportFallbackErr(err error) bool {
	te, ok := err.(*audioTransportError)
	return ok && (te.status == http.StatusNotFound || te.status == http.StatusMethodNotAllowed || te.status == http.StatusUnsupportedMediaType)
}

// audioUpstreamStatusError 承载上游 4xx/5xx 的状态码与（截断后的）响应体，
// 让 handler 能把「客户端请求被上游拒绝」映射成 4xx 而不是一律 502。
//
// 2026-10-04 小米实测：unsupported input_audio.format、Unknown voice、
// asr_options.language 非法值等都是上游 400 错误体（"Param Incorrect"）。
// 这些是**调用方参数问题**，回报 502 会让客户端（openpocket 的探测缓存、
// 监控的 5xx 告警）误判成「网关故障」。
type audioUpstreamStatusError struct {
	status int
	body   string
}

func (e *audioUpstreamStatusError) Error() string {
	body := strings.TrimSpace(e.body)
	if body == "" {
		return fmt.Sprintf("upstream http %d", e.status)
	}
	return fmt.Sprintf("upstream http %d: %s", e.status, body)
}

// clientFault 报告该上游错误是否应作为「调用方请求有问题」回报。
// 401/403 是网关自己那把上游 key 的问题（不是调用方的），429 是配额，
// 两者都不能算调用方错误。
func (e *audioUpstreamStatusError) clientFault() bool {
	switch e.status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// audioClientInputError 标记「调用方参数问题」——网关在**本地**就能判定，
// 不需要问上游（前置校验、或与上游契约对不上的调用方式）。
//
// 用类型而不是错误字符串前缀做判定：前缀会被无关改动冲掉，而这里每多一
// 种判定就多一处静默退化成 502 的地方。
type audioClientInputError struct{ msg string }

func (e *audioClientInputError) Error() string { return e.msg }

func newAudioClientInputError(format string, args ...any) error {
	return &audioClientInputError{msg: fmt.Sprintf(format, args...)}
}

// audioNoProviderError 标记「该模型解析不出任何可用音频候选」。它与上游
// 无关（请求还没出门），语义上既不是调用方参数错误也不是网关故障，所以
// 单独一类：handler 映射成 503 no_provider（openpocket 的 isNoProvider
// 识别口径）。
//
// 2026-10-04 审计补型：此前 handler 靠 strings.Contains(msg, "no audio
// provider available") / "resolve candidates" 反推这个语义——错误文本是
// 我们自己拼的所以今天碰巧成立，但它排在 typed 上游错误检查**之前**，一旦
// 上游 4xx 错误体（供应商可控文本）恰好含这两个子串，调用方参数错误就会被
// 误判成 503。与 audioUpstreamStatusError / audioClientInputError 同一原则：
// 语义判定走类型，不走错误字符串。
type audioNoProviderError struct{ msg string }

func (e *audioNoProviderError) Error() string { return e.msg }

func newAudioNoProviderError(format string, args ...any) error {
	return &audioNoProviderError{msg: fmt.Sprintf(format, args...)}
}

// newAudioUpstreamError 从已限长读取的上游错误体构造 typed 错误。
// body 已由调用方用 readLimitedResponse 限长；这里再截一次是防御性的
// （readLimitedResponse 超限会返回 error 而不是部分数据）。
func newAudioUpstreamError(status int, body []byte) error {
	raw := strings.TrimSpace(string(body))
	if len(raw) > 8<<10 {
		raw = raw[:8<<10]
	}
	return &audioUpstreamStatusError{status: status, body: raw}
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
		return nil, newAudioUpstreamError(resp.StatusCode, body)
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

// xiaomiASRLanguageAliases 把 OpenAI 的 language 字段（ISO-639-1，客户端
// 常见带地区/大写/下划线变体）映射到 asr_options.language 的合法取值。
//
// 2026-10-04 直连实测（token-plan-cn）：asr_options.language 是**严格**白
// 名单，只有 zh / en / auto；zh-CN、en-US、zh_CN、ja、ko、ZH、auto-4 一律
// 400 "asr_options.language must be one of: zh, en, auto. Got: X"。
//
// 因此无法映射时必须**省略该键**（回落到上游自动识别语种），绝不能原样
// 透传——原样透传会把今天能成功的请求变成 400。MiMo ASR 自称「中英双语 +
// 多方言、无需语言标签、自动识别」，所以省略并不是降级。
var xiaomiASRLanguageAliases = map[string]string{
	"auto": "auto", "detect": "auto", "自动": "auto",
	"zh": "zh", "zho": "zh", "chi": "zh", "cmn": "zh", "chs": "zh", "cht": "zh",
	"chinese": "zh", "mandarin": "zh", "中文": "zh", "汉语": "zh",
	"en": "en", "eng": "en", "english": "en", "英文": "en",
}

// normalizeASRLanguageForCandidate 把 language 归一到 chat-audio 桥接形态
// 可透传的值；返回空串表示「不透传 language」。
//
// 非 chat-audio 桥接（multipart 透传形态）时返回原值——那条路径按 OpenAI
// 契约把 language 原样交给上游，由上游自己校验。
func normalizeASRLanguageForCandidate(cand provider.Candidate, language string) string {
	v := strings.TrimSpace(language)
	if v == "" {
		return ""
	}
	if !preferChatAudioBridge(cand) {
		return v
	}
	// 先整体小写、下划线归一为连字符，再剥掉地区/变体后缀（zh-Hans-CN → zh）。
	key := strings.ToLower(strings.ReplaceAll(v, "_", "-"))
	if mapped, ok := xiaomiASRLanguageAliases[key]; ok {
		return mapped
	}
	if base, _, found := strings.Cut(key, "-"); found {
		if mapped, ok := xiaomiASRLanguageAliases[base]; ok {
			return mapped
		}
	}
	return ""
}

// ignoredChatAudioASRParams 报告 chat-audio 桥接形态「收下但没送出去」的
// 转写参数。三条各自的依据：
//
//	prompt      —— 上游拒收 text part（400 "ASR request must not include
//	              text parts"），官方 ASR 文档的请求体也只有 messages /
//	              model / asr_options / stream 四个键，没有等价物；
//	temperature —— 同上，这条上游没有采样温度旋钮；
//	language    —— 只有「传了但归一后落空」才算丢弃。映射成功的
//	              （zh-CN→zh）真的进了 asr_options，不该被记成未生效；
//	              没传也不算。
//
// 透传形态（multipart）三项都按 OpenAI 契约原样上行，不产生回执。
func ignoredChatAudioASRParams(cand provider.Candidate, req TranscribeRequest) []string {
	if !preferChatAudioBridge(cand) {
		return nil
	}
	var ignored []string
	if strings.TrimSpace(req.Prompt) != "" {
		ignored = append(ignored, "prompt")
	}
	if strings.TrimSpace(req.Temperature) != "" {
		ignored = append(ignored, "temperature")
	}
	if lang := strings.TrimSpace(req.Language); lang != "" && normalizeASRLanguageForCandidate(cand, lang) == "" {
		ignored = append(ignored, "language")
	}
	return ignored
}

// transcribeViaChatAudio bridges the audio into a chat/completions call with
// an input_audio content block (Xiaomi MiMo shape, verified 2026-10-03).
//
// 桥接的实测约束（小米，2026-10-04 复测）：
//   - 不能带 text part（上游 400 "ASR request must not include text parts"）
//     → prompt 无法透传；
//   - language 可以透传，但走 asr_options.language 且是严格白名单，
//     见 normalizeASRLanguageForCandidate；
//   - format 仅 wav|mp3，桥接前本地校验（见 xiaomiASRAudioFormats）。
func (s *AudioService) transcribeViaChatAudio(ctx context.Context, cand provider.Candidate, req TranscribeRequest, format string, emitDelta func(string)) (*TranscribeResult, error) {
	if preferChatAudioBridge(cand) && !xiaomiASRAudioFormats[format] {
		// 本地拦掉：否则要先把最多 32MiB 音频 base64 膨胀 4/3 再传一整轮
		// 才换来上游一句 400。multipart 透传形态不受此限制（上游可能收
		// webm/m4a），所以只对已知桥接供应商前置校验。
		return nil, newAudioClientInputError("audio format %q is not supported by the chat-audio bridge (supported: mp3, wav)", format)
	}
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
	if lang := normalizeASRLanguageForCandidate(cand, req.Language); lang != "" {
		payload["asr_options"] = map[string]string{"language": lang}
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
		return nil, newAudioUpstreamError(resp.StatusCode, raw)
	}

	res := &TranscribeResult{Transport: AudioTransportChatAudio, UpstreamModel: cand.RawModel, IgnoredParams: ignoredChatAudioASRParams(cand, req)}
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
		return nil, newAudioClientInputError("input must be a non-empty string")
	}
	ctx, cancel := context.WithTimeout(ctx, maxAudioRequestDuration)
	defer cancel()
	if err := s.acquireAudioSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseAudioSlot()
	candidates, _, err := s.provider.GetCandidatesByModality(ctx, req.Model, req.Profile, req.TenantID, audioModalityRequest)
	if err != nil {
		return nil, newAudioNoProviderError("resolve candidates: %s", err)
	}
	usable := audioCandidateSelection(candidates)
	if len(usable) == 0 {
		return nil, newAudioNoProviderError("no audio provider available for model %q", req.Model)
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
		return nil, newAudioUpstreamError(resp.StatusCode, raw)
	}
	audio, err := readLimitedResponse(resp.Body, maxAudioResponseBytes)
	if err != nil {
		return nil, err
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.Contains(ct, "application/json") {
		// 上游没标（或错标 JSON）时按请求格式推断；RIFF 魔数优先——
		// 实际字节形态比客户端期望值可信（39 轮 P2-4 同款纠偏）。
		ct = "audio/" + audioTTSEffectiveFormat(req.ResponseFormat)
		if isWAVBytes(audio) {
			ct = "audio/wav"
		}
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
// （空值让上游走默认）。命中集合时返回 map 键的规范小写形式——上游若按
// 大小写敏感匹配，原始大小写（如 "Mia"）仍会 400（39 轮 P3-4）。
func normalizeTTSVoiceForCandidate(cand provider.Candidate, voice string) string {
	v := strings.TrimSpace(voice)
	if preferChatAudioBridge(cand) {
		lv := strings.ToLower(v)
		if v == "" || !xiaomiVoices[lv] {
			return "mimo_default"
		}
		return lv
	}
	return v
}

// audioTTSEffectiveFormat 归一 TTS response_format；默认 wav（小米桥接
// 返回 24kHz16bit WAV；OpenAI 默认 mp3，但显式 wav 两边都合法）。
//
// 这条是**透传形态**（OpenAI 兼容 /audio/speech）的口径，含 OpenAI 全量
// 容器；chat-audio 桥接形态的更窄口径见 normalizeTTSFormatForCandidate。
// 两者不能合并：合成 OpenAI 兼容上游时 opus/aac/flac 是合法的。
func audioTTSEffectiveFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "mp3", "opus", "aac", "flac", "wav", "pcm":
		return strings.ToLower(strings.TrimSpace(f))
	default:
		return "wav"
	}
}

// xiaomiTTSFormats 是 MiMo TTS 的 audio.format 合法集合。
//
// 官方《Speech Synthesis (MiMo-TTS Series) - OpenAI API Compatibility》
// （mimo.mi.com → llms.txt → api/audio/tts.md）只列 wav / mp3 / pcm /
// pcm16，并写明 pcm 与 pcm16 等价（都按 pcm16 处理）。
//
// 这比 OpenAI /audio/speech **窄**：OpenAI 还收 opus / aac / flac。
var xiaomiTTSFormats = map[string]string{
	"wav": "wav", "mp3": "mp3", "pcm": "pcm", "pcm16": "pcm",
}

// ttsFormatsOpenAIOnly 是「OpenAI 合法但 MiMo 不收」的三种容器。它们是
// 客户端按 OpenAI 契约发出的**合法请求**，撞上小米只能失败——本地 400
// 比白跑一轮再换一句上游 400 更好（与 ASR 侧 m4a 的前置校验同款处置）。
//
// 刻意只拦这三个、不拦无法识别的乱值：后者在透传形态下一直是回落 wav
// 的既有行为，改动它超出「上游不收这个格式」这个问题本身。
var ttsFormatsOpenAIOnly = map[string]bool{"opus": true, "aac": true, "flac": true}

// normalizeTTSFormatForCandidate 把 response_format 归一到该候选能兑现的
// 容器。透传形态走 OpenAI 全量口径（audioTTSEffectiveFormat）；桥接形态
// 额外做两件事——
//
//	pcm16 → pcm（官方别名，此前会被当成乱值回落成 wav，输出格式与客户端
//	         预期不符且无人告知）；
//	opus/aac/flac → 本地 400（官方清单里没有）。
func normalizeTTSFormatForCandidate(cand provider.Candidate, format string) (string, error) {
	eff := audioTTSEffectiveFormat(format)
	if !preferChatAudioBridge(cand) {
		return eff, nil
	}
	key := strings.ToLower(strings.TrimSpace(format))
	if key == "" {
		return "wav", nil
	}
	if mapped, ok := xiaomiTTSFormats[key]; ok {
		return mapped, nil
	}
	if ttsFormatsOpenAIOnly[key] {
		return "", newAudioClientInputError(
			"response_format %q is valid for OpenAI /audio/speech but not supported by this upstream (supported: wav, mp3, pcm, pcm16)",
			key)
	}
	return eff, nil
}

// xiaomiTTSMode 分类 MiMo TTS 家族——三个变体要的请求形状互不相同
// （2026-10-04 逐个直连实测），按同一个形状发必然有变体 400。
type xiaomiTTSMode int

const (
	// xiaomiTTSBuiltin（mimo-v2.5-tts）：assistant 消息承载文本 + 内置音色名。
	xiaomiTTSBuiltin xiaomiTTSMode = iota
	// xiaomiTTSVoiceDesign（-voicedesign）：必须带 user 消息描述音色，
	// 且**不能**带 audio.voice（上游 400 "audio.voice is not supported for
	// voice design model"；缺 user 消息则 400 "user message content must
	// not be empty for voice design model"）。
	xiaomiTTSVoiceDesign
	// xiaomiTTSVoiceClone（-voiceclone）：audio.voice 必须是音频样本的
	// DataURL（上游 400 "audio.voice must be a DataURL for voice clone
	// model"），且只收 mp3/wav 样本。
	xiaomiTTSVoiceClone
)

// xiaomiTTSModeForModel 按上游模型名判定 TTS 变体。
func xiaomiTTSModeForModel(model string) xiaomiTTSMode {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "voicedesign") || strings.Contains(m, "voice-design"):
		return xiaomiTTSVoiceDesign
	case strings.Contains(m, "voiceclone") || strings.Contains(m, "voice-clone"):
		return xiaomiTTSVoiceClone
	default:
		return xiaomiTTSBuiltin
	}
}

// xiaomiDefaultVoiceDesignPrompt 是 voicedesign 变体在调用方没有给音色
// 描述时使用的兜底描述（上游要求 user 消息非空）。
const xiaomiDefaultVoiceDesignPrompt = "自然清晰的中性声音，语速适中，吐字清楚。"

// buildChatAudioTTSPayload 按变体拼出 chat/completions 的 TTS 请求体。
// req.Voice 对 voicedesign 变体被当作**音色设计描述**（OpenAI 的 voice
// 字段在这条上游上正好可以承载一句自然语言描述），对 voiceclone 变体被
// 当作样本 DataURL 透传。
//
// 第二个返回值是被**替换**掉的客户端参数（音色被换成默认音色）——
// 调用方要求的是一个音色、拿到的是另一个，这是必须如实回执的静默改写，
// 与 speed「收下但不生效」同类。mode 在这里判定一次，同时决定载荷形状
// 与回执内容，避免两处各自算一遍变体而漂移。
func buildChatAudioTTSPayload(cand provider.Candidate, req SynthesizeRequest, format string) (map[string]any, []string, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		format = "wav"
	}
	mode := xiaomiTTSBuiltin
	if preferChatAudioBridge(cand) {
		mode = xiaomiTTSModeForModel(cand.RawModel)
	}
	var ignored []string
	audio := map[string]string{"format": format}
	messages := []map[string]string{{"role": "assistant", "content": req.Input}}
	switch mode {
	case xiaomiTTSVoiceDesign:
		// voice 字段在这里是「音色设计描述」；为空用兜底描述。
		desc := strings.TrimSpace(req.Voice)
		if desc == "" {
			desc = xiaomiDefaultVoiceDesignPrompt
		}
		messages = append([]map[string]string{{"role": "user", "content": desc}}, messages...)
	case xiaomiTTSVoiceClone:
		// 样本必须是 DataURL；音色名在这里没有意义，早失败给调用方一个
		// 能照着改的说明，而不是让上游回一句 opaque 的 400。
		sample := strings.TrimSpace(req.Voice)
		if !strings.HasPrefix(strings.ToLower(sample), "data:") {
			return nil, nil, newAudioClientInputError(
				"model %q clones a voice from an audio sample: pass the sample as a data URL in voice (e.g. data:audio/wav;base64,...), not a voice name",
				cand.RawModel)
		}
		if mediatype, ok := dataURLMediaType(sample); ok && !voiceCloneSampleFormats[mediatype] {
			return nil, nil, newAudioClientInputError(
				"model %q accepts only wav or mp3 voice samples, got media type %q (supported: audio/wav, audio/mpeg, audio/mp3)",
				cand.RawModel, mediatype)
		}
		audio["voice"] = sample
	default:
		if v := normalizeTTSVoiceForCandidate(cand, req.Voice); v != "" {
			audio["voice"] = v
		}
		// 客户端点名了音色、我们换成了默认音色 —— 拿到的音频与请求的不是
		// 一个人，必须回执（此前静默替换：voice=alloy 与 voice=茉莉 拿到
		// 的是同一个声音，调用方无从分辨）。
		if asked := strings.TrimSpace(req.Voice); asked != "" {
			if _, ok := xiaomiVoices[strings.ToLower(asked)]; !ok {
				ignored = append(ignored, "voice")
			}
		}
	}
	return map[string]any{
		"model":      cand.RawModel,
		"modalities": []string{"text", "audio"},
		"audio":      audio,
		"messages":   messages,
	}, ignored, nil
}

// dataURLMediaType 从 data URL 里取 media type；取不到（无 media type 或
// 形态异常）时返回 ok=false，交给上游判断而不是本地瞎猜。
func dataURLMediaType(dataURL string) (string, bool) {
	rest := dataURL[len("data:"):]
	idx := strings.IndexByte(rest, ',')
	if idx < 0 {
		return "", false
	}
	mt := rest[:idx]
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	mt = strings.ToLower(strings.TrimSpace(mt))
	if mt == "" {
		return "", false
	}
	return mt, true
}

// voiceCloneSampleFormats 是 voiceclone 变体接受的样本容器（官方 TTS 文档：
// 「only supports passing in audio sample files in mp3 and wav formats」）。
// wav 的标准类型是 audio/wav，实践中 audio/x-wav 也常见。
var voiceCloneSampleFormats = map[string]bool{
	"audio/wav": true, "audio/x-wav": true, "audio/wave": true,
	"audio/mpeg": true, "audio/mp3": true,
}

// synthesizeViaChatAudio bridges TTS through chat/completions with
// modalities ["text","audio"]（小米 MiMo 形态，2026-10-03/04 实测 + 官方
// 文档复核 2026-10-04）：
//
//   - 文本必须放 assistant 角色消息（上游 400 "messages must contain an
//     assistant role for TTS model"）；
//   - audio.format 只收 wav/mp3/pcm/pcm16（官方清单），比 OpenAI 窄，
//     见 normalizeTTSFormatForCandidate；
//   - 内置音色之外的名字先归一到 mimo_default（上游 400 "Unknown voice"，
//     错误体里自带合法清单），并回执 voice 被替换；
//   - voicedesign / voiceclone 两个变体的形状要求不同，见 buildChatAudioTTSPayload；
//   - 响应 choices[0].message.audio.data 是 base64 音频（WAV 24kHz）。
func (s *AudioService) synthesizeViaChatAudio(ctx context.Context, cand provider.Candidate, req SynthesizeRequest) (*SynthesizeResult, error) {
	format, ferr := normalizeTTSFormatForCandidate(cand, req.ResponseFormat)
	if ferr != nil {
		return nil, ferr
	}
	payload, voiceIgnored, perr := buildChatAudioTTSPayload(cand, req, format)
	if perr != nil {
		return nil, perr
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
		return nil, newAudioUpstreamError(resp.StatusCode, raw)
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
	// 39 轮（P2-4）：桥接实测固定回 24kHz16bit WAV；客户端请求的 format
	// 若与实际字节不符（上游不认该格式仍回 WAV），Content-Type 按客户端
	// 期望值标注会让下游按 audio/mp3 等解码失败。按 RIFF 魔数把 WAV 字节
	// 钉回 audio/wav。
	ct := "audio/" + format
	if isWAVBytes(audio) {
		ct = "audio/wav"
	}
	return &SynthesizeResult{
		Audio:          audio,
		ContentType:    ct,
		Transport:      AudioTransportChatAudio,
		UpstreamModel:  cand.RawModel,
		TranscriptHint: parsed.Choices[0].Message.Audio.Transcript,
		IgnoredParams:  ignoredChatAudioTTSParams(req, voiceIgnored),
	}, nil
}

// ignoredChatAudioTTSParams 汇总 chat-audio 形态「收下但没按字面兑现」的
// 参数。2026-10-04 实测 + 官方文档复核：
//
//	speed —— 小米 chat 协议没有语速旋钮，带不带 speed 得到逐字节相同的
//	         音频（base64 长度一致）；
//	voice —— 客户端点名了音色而这条上游没有，我们换成 mimo_default。
//	         拿到的声音与请求的不是同一个人，比 speed 更需要回执。
//
// 两个来源分开传进来（speed 来自请求本身，voice 来自载荷拼装），避免
// 在这里重算一次 TTS 变体——变体判定与载荷形状必须是同一个事实源。
func ignoredChatAudioTTSParams(req SynthesizeRequest, voiceIgnored []string) []string {
	ignored := append([]string(nil), voiceIgnored...)
	if strings.TrimSpace(req.Speed) != "" {
		ignored = append(ignored, "speed")
	}
	return ignored
}

// isWAVBytes 按 RIFF/WAVE 魔数嗅探音频字节（12 字节头：RIFF + 4 字节长度 +
// WAVE）。只用于 Content-Type 纠偏，不做完整 WAV 校验。
func isWAVBytes(b []byte) bool {
	return len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE"
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
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
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
		// 39 轮（P1-1）：上游流中错误帧（data: {"error":{...}}）此前落进
		// 只声明 Choices/Usage 的结构体——未知字段被忽略、choices 为空、
		// 整帧静默蒸发，函数把截断文本当成功返回。显式检出并上抛；此时
		// 部分增量可能已发出，Transcribe 的 emitted 闩锁会阻止换候选重跑。
		if chunk.Error != nil {
			detail := chunk.Error.Message
			if detail == "" {
				detail = chunk.Error.Type
			}
			if detail == "" {
				detail = chunk.Error.Code
			}
			if detail == "" {
				detail = "unknown upstream stream error frame"
			}
			return aggregated.String(), seconds, fmt.Errorf("upstream stream error: %s", detail)
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
