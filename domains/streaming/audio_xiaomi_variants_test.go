// Package streaming — audio_xiaomi_variants_test.go
//
// 小米 MiMo 音频面的行为锁定（2026-10-04 直连实测 + 网关端到端复验）。
//
// 这一组测试守的是「上游契约」而不是「我们怎么写代码」——每个断言背后
// 都有一次真实上游调用：
//
//	asr_options.language  严格白名单 zh/en/auto（zh-CN/ja/ZH 一律 400）
//	input_audio.format    只收 wav/mp3（m4a 400，错误体自带清单）
//	TTS 三个变体          请求形状互不相同，见各 Test 注释
//	上游 400              是调用方参数问题，回 502 会让客户端误判网关故障
//
// 判据刻意分两层：纯函数层（映射表/形状拼装）用表驱动穷举，上游报文层
// （真的发出的 JSON 长什么样）用 httptest 桩断言。桩的请求体形状必须照抄
// 实测报文，否则测的是一个不存在的契约。
package streaming

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// ── 1. asr_options.language 归一 ────────────────────────────────────────

func TestNormalizeASRLanguageForCandidate(t *testing.T) {
	xiaomi := provider.Candidate{CatalogCode: "xiaomi"}
	other := provider.Candidate{CatalogCode: "zhipu"}

	cases := []struct {
		in         string
		wantXiaomi string // 桥接形态（catalog=xiaomi）下的透传值
		wantOther  string // multipart 透传形态下按 OpenAI 契约原样透传
	}{
		{"", "", ""},
		{"zh", "zh", "zh"},
		{"en", "en", "en"},
		{"auto", "auto", "auto"},
		// 上游实测 400 的写法，必须被归一到合法值而不是原样透传。
		{"zh-CN", "zh", "zh-CN"},
		{"zh_CN", "zh", "zh_CN"},
		{"en-US", "en", "en-US"},
		{"ZH", "zh", "ZH"},
		{"  En  ", "en", "En"},
		{"zho", "zh", "zho"},
		{"eng", "en", "eng"},
		// 无法映射：桥接形态必须**省略**（回落到上游自动识别），
		// 原样透传会把能成功的请求变成 400。
		{"ja", "", "ja"},
		{"ko", "", "ko"},
		{"fr-CA", "", "fr-CA"},
		// 地区后缀能剥掉的基础语种仍应归一。
		{"zh-Hans-CN", "zh", "zh-Hans-CN"},
		{"auto-4", "auto", "auto-4"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.in), func(t *testing.T) {
			if got := normalizeASRLanguageForCandidate(xiaomi, tc.in); got != tc.wantXiaomi {
				t.Errorf("xiaomi bridge language %q -> %q, want %q", tc.in, got, tc.wantXiaomi)
			}
			if got := normalizeASRLanguageForCandidate(other, tc.in); got != tc.wantOther {
				t.Errorf("passthrough language %q -> %q, want %q", tc.in, got, tc.wantOther)
			}
		})
	}
}

// bridgeASRPayload 抓 chat-audio 桥接实际发出的请求体。
func bridgeASRPayload(t *testing.T, catalogCode, filename, language string) map[string]any {
	t.Helper()
	var captured map[string]any
	svc := newTestAudioService(t, catalogCode, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Errorf("bridge body not json: %v (%s)", err, string(raw)[:min(200, len(raw))])
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), filename, "mimo-v2.5-asr", language)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	return captured
}

func asrOptionsOf(t *testing.T, payload map[string]any) (map[string]any, bool) {
	t.Helper()
	v, ok := payload["asr_options"]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("asr_options must be an object, got %T", v)
	}
	return m, true
}

func TestChatAudioBridgeForwardsMappedLanguageAsAsrOptions(t *testing.T) {
	// 修好的核心行为：language 不再被丢弃，映射后进 asr_options.language。
	payload := bridgeASRPayload(t, "xiaomi", "a.wav", "zh-CN")
	opts, ok := asrOptionsOf(t, payload)
	if !ok {
		t.Fatalf("language=zh-CN must be forwarded as asr_options, body=%v", payload)
	}
	if got := opts["language"]; got != "zh" {
		t.Errorf("asr_options.language = %v, want \"zh\"", got)
	}
}

func TestChatAudioBridgeOmitsUnmappableLanguage(t *testing.T) {
	// 反向对照：无法映射的语种必须**不带** asr_options。
	// 原样透传会让小米 400（实测 "asr_options.language must be one of: zh, en, auto. Got: ja"），
	// 也就是把今天能成功的请求改坏。
	payload := bridgeASRPayload(t, "xiaomi", "a.wav", "ja")
	if _, ok := asrOptionsOf(t, payload); ok {
		t.Fatalf("unmappable language must be omitted, not forwarded: %v", payload)
	}
}

func TestChatAudioBridgeOmitsAsrOptionsWhenLanguageEmpty(t *testing.T) {
	payload := bridgeASRPayload(t, "xiaomi", "a.wav", "")
	if _, ok := asrOptionsOf(t, payload); ok {
		t.Fatalf("empty language must not produce asr_options: %v", payload)
	}
}

// ── 2. input_audio.format 前置校验 ───────────────────────────────────────

func TestChatAudioBridgeRejectsUnsupportedFormatWithoutCallingUpstream(t *testing.T) {
	// 2026-10-04 实测：小米只收 wav/mp3，m4a 上游 400 且错误体自带清单。
	// 网关必须**本地**拦掉：否则要先 base64 膨胀 4/3 传完整个音频才换来 400。
	var hits int32
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"must not happen"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.m4a", "mimo-v2.5-asr", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported format status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not supported") {
		t.Errorf("body should name the problem: %s", rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("upstream must not be called for an unsupported format, got %d calls", got)
	}
}

func TestChatAudioBridgeAcceptsWavAndMp3(t *testing.T) {
	// 正向对照：判据必须锚在真实上游调用上，否则「不发请求」也能全绿。
	for _, fn := range []string{"a.wav", "a.mp3"} {
		var hits int32
		svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		})
		h := NewAudioTranscriptionsHandler(svc)
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
		buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), fn, "mimo-v2.5-asr", "")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", fn, rec.Code, rec.Body.String())
		}
		if got := atomic.LoadInt32(&hits); got != 1 {
			t.Errorf("%s: upstream calls = %d, want 1", fn, got)
		}
	}
}

func TestNonBridgeCandidateDoesNotPreValidateFormat(t *testing.T) {
	// 透传形态（multipart）可能支持 webm/m4a，本地白名单不能误伤它。
	var hits int32
	svc := newTestAudioService(t, "zhipu", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.Contains(r.URL.Path, "/audio/transcriptions") {
			_, _ = w.Write([]byte(`{"text":"passthrough ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"bridge"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.m4a", "mimo-v2.5-asr", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Error("non-bridge candidate must still reach an upstream")
	}
}

// ── 3. 上游 4xx 的状态码语义 ─────────────────────────────────────────────

func upstreamErrorCase(t *testing.T, catalogCode string, upstreamStatus, wantStatus int, wantCode string) {
	t.Helper()
	svc := newTestAudioService(t, catalogCode, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstreamStatus)
		_, _ = w.Write([]byte(`{"error":{"code":"400","message":"Param Incorrect","param":"input_audio.format must be one of: wav, mp3. Got: m4a"}}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("upstream %d -> gateway %d, want %d; body=%s", upstreamStatus, rec.Code, wantStatus, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), wantCode) {
		t.Errorf("body should carry code %q: %s", wantCode, rec.Body.String())
	}
}

func TestUpstreamClientFaultMapsToBadRequest(t *testing.T) {
	// 上游 400 = 调用方参数问题。回报 502 会让 openpocket 的探测缓存与
	// 5xx 告警误判成「网关故障」。
	upstreamErrorCase(t, "other", http.StatusBadRequest, http.StatusBadRequest, "upstream_rejected_request")
}

func TestUpstreamUnauthorizedStaysBadGateway(t *testing.T) {
	// 上游 401/403 是**网关自己那把 key** 的问题，不是调用方的，必须留在 5xx。
	upstreamErrorCase(t, "other", http.StatusUnauthorized, http.StatusBadGateway, "upstream_error")
}

func TestUpstreamRateLimitMapsToTooManyRequests(t *testing.T) {
	upstreamErrorCase(t, "other", http.StatusTooManyRequests, http.StatusTooManyRequests, "upstream_rate_limited")
}

func TestUpstreamServerErrorStaysBadGateway(t *testing.T) {
	upstreamErrorCase(t, "other,providerx", http.StatusInternalServerError, http.StatusBadGateway, "upstream_error")
}

// ── 4. TTS 三个变体的请求形状 ────────────────────────────────────────────

// newTTSAudioService 让 resolver 返回指定的 RawModel（决定 TTS 变体）。
func newTTSAudioService(t *testing.T, rawModel string, handler http.HandlerFunc) *AudioService {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cand := provider.Candidate{
		CredentialID: 1, ProviderID: 1, BaseURL: srv.URL,
		Protocol: "openai-completions", CatalogCode: "xiaomi",
		RawModel: rawModel, Routable: true, APIKey: "sk-test",
	}
	cand.AvailabilityState = "ready"
	return NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())
}

func ttsBridgePayload(t *testing.T, rawModel, body string) (map[string]any, int, *httptest.ResponseRecorder) {
	t.Helper()
	var captured map[string]any
	svc := newTTSAudioService(t, rawModel, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Errorf("tts bridge body not json: %v (%s)", err, string(raw)[:min(200, len(raw))])
		}
		fake := base64.StdEncoding.EncodeToString([]byte("RIFFfake"))
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"audio":{"data":%q}}}]}`, fake)))
	})
	h := NewAudioSpeechHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return captured, rec.Code, rec
}

func ttsAudioField(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	v, ok := payload["audio"]
	if !ok {
		t.Fatalf("audio object missing: %v", payload)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("audio must be an object, got %T", v)
	}
	return m
}

func ttsMessages(t *testing.T, payload map[string]any) []map[string]string {
	t.Helper()
	raw, _ := json.Marshal(payload["messages"])
	var out []map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("messages not an array of {role,content}: %v", err)
	}
	return out
}

func TestTTSBuiltinKeepsAssistantMessageAndNormalizedVoice(t *testing.T) {
	payload, code, rec := ttsBridgePayload(t, "mimo-v2.5-tts", `{"model":"mimo-v2.5-tts","input":"你好","voice":"alloy"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, rec.Body.String())
	}
	msgs := ttsMessages(t, payload)
	if len(msgs) != 1 || msgs[0]["role"] != "assistant" || msgs[0]["content"] != "你好" {
		t.Errorf("builtin tts messages = %+v", msgs)
	}
	if got := ttsAudioField(t, payload)["voice"]; got != "mimo_default" {
		t.Errorf("voice = %v, want mimo_default", got)
	}
}

func TestTTSVoiceDesignOmitsVoiceAndSendsUserDescription(t *testing.T) {
	// 2026-10-04 实测：voicedesign 变体
	//   - 带 audio.voice       → 400 "audio.voice is not supported for voice design model"
	//   - 缺 user 消息内容      → 400 "user message content must not be empty..."
	//   - user(描述) + assistant(文本) + 无 voice → 200
	payload, code, rec := ttsBridgePayload(t, "mimo-v2.5-tts-voicedesign",
		`{"model":"mimo-v2.5-tts-voicedesign","input":"今天天气不错。","voice":"温柔清亮的女声，语速偏慢"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, rec.Body.String())
	}
	if _, has := ttsAudioField(t, payload)["voice"]; has {
		t.Errorf("voicedesign must NOT carry audio.voice (upstream 400): %v", payload["audio"])
	}
	msgs := ttsMessages(t, payload)
	if len(msgs) != 2 || msgs[0]["role"] != "user" || msgs[0]["content"] != "温柔清亮的女声，语速偏慢" {
		t.Fatalf("voicedesign messages must be [user(desc), assistant(text)], got %+v", msgs)
	}
	if msgs[1]["role"] != "assistant" || msgs[1]["content"] != "今天天气不错。" {
		t.Errorf("voicedesign assistant message wrong: %+v", msgs[1])
	}
}

func TestTTSVoiceDesignDefaultsDescriptionWhenVoiceEmpty(t *testing.T) {
	// 上游要求 user 消息非空；客户端没给音色描述时也要能出声音。
	payload, code, rec := ttsBridgePayload(t, "mimo-v2.5-tts-voicedesign",
		`{"model":"mimo-v2.5-tts-voicedesign","input":"今天天气不错。"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, rec.Body.String())
	}
	msgs := ttsMessages(t, payload)
	if len(msgs) != 2 || strings.TrimSpace(msgs[0]["content"]) == "" {
		t.Fatalf("voicedesign needs a non-empty user message, got %+v", msgs)
	}
}

func TestTTSVoiceCloneRequiresSampleDataURL(t *testing.T) {
	// 2026-10-04 实测：voiceclone 变体 audio.voice 必须是样本 DataURL，
	// 传音色名 → 400 "audio.voice must be a DataURL for voice clone model"。
	// 网关要在本地给出能照着改的说明（400），而不是让上游回 opaque 400。
	var hits int32
	svc := newTTSAudioService(t, "mimo-v2.5-tts-voiceclone", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"UklGRg=="}}}]}`))
	})
	h := NewAudioSpeechHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech",
		strings.NewReader(`{"model":"mimo-v2.5-tts-voiceclone","input":"你好","voice":"茉莉"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data URL") {
		t.Errorf("body should tell the caller to pass a sample data URL: %s", rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("upstream must not be called when the sample is missing, got %d calls", got)
	}
}

func TestTTSVoiceCloneForwardsSampleDataURL(t *testing.T) {
	sample := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(tinyWAVForTest())
	body := fmt.Sprintf(`{"model":"mimo-v2.5-tts-voiceclone","input":"你好","voice":%q}`, sample)
	payload, code, rec := ttsBridgePayload(t, "mimo-v2.5-tts-voiceclone", body)
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, rec.Body.String())
	}
	if got := ttsAudioField(t, payload)["voice"]; got != sample {
		t.Errorf("voiceclone sample must be forwarded verbatim, got %v", got)
	}
}

// ── 5. speed 的如实回执 ─────────────────────────────────────────────────

func TestTTSSpeedIsDisclosedAsIgnored(t *testing.T) {
	// 2026-10-04 实测：小米 chat 协议没有语速旋钮，同一文本带不带 speed
	// 得到逐字节相同的音频。静默忽略会让调用方以为语速已生效。
	_, code, rec := ttsBridgePayload(t, "mimo-v2.5-tts",
		`{"model":"mimo-v2.5-tts","input":"你好","voice":"茉莉","speed":1.5}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Audio-Ignored-Params"); got != "speed" {
		t.Errorf("X-Gw-Audio-Ignored-Params = %q, want \"speed\"", got)
	}
}

func TestTTSNoSpeedMeansNoIgnoredHeader(t *testing.T) {
	// 防恒绿对照：没有 speed 时这个头不能出现。
	_, code, rec := ttsBridgePayload(t, "mimo-v2.5-tts",
		`{"model":"mimo-v2.5-tts","input":"你好","voice":"茉莉"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Audio-Ignored-Params"); got != "" {
		t.Errorf("X-Gw-Audio-Ignored-Params = %q, want empty", got)
	}
}

// ── 6. MCP 的错误码分流 ─────────────────────────────────────────────────

func TestMCPClientFaultUsesInvalidParams(t *testing.T) {
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"400","message":"Param Incorrect"}}`))
	})
	h := NewAudioMCPHandler(svc)
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"transcribe_audio","arguments":{"audio_base64":%q,"format":"wav","model":"mimo-v2.5-asr"}}}`,
		base64.StdEncoding.EncodeToString(tinyWAVForTest()))
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), fmt.Sprintf("%d", mcpErrParams)) {
		t.Fatalf("client fault must be -%d (invalid params), got: %s", mcpErrParams, rec.Body.String())
	}
}

func TestMCPUnsupportedFormatUsesInvalidParams(t *testing.T) {
	var hits int32
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	})
	h := NewAudioMCPHandler(svc)
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"transcribe_audio","arguments":{"audio_base64":%q,"format":"m4a","model":"mimo-v2.5-asr"}}}`,
		base64.StdEncoding.EncodeToString(tinyWAVForTest()))
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), fmt.Sprintf("%d", mcpErrParams)) {
		t.Fatalf("unsupported format must be -%d, got: %s", mcpErrParams, rec.Body.String())
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("upstream must not be called, got %d", hits)
	}
}

// ── 7. no_provider 语义的类型化判定（2026-10-04 审计补） ─────────────────
//
// writeAudioError 曾用 strings.Contains(msg, "resolve candidates") 反推
// 「无候选」语义，且排在 typed 上游错误检查之前——上游 4xx 错误体是供应商
// 可控文本，一旦恰好含同样子串，调用方参数错误就被误判成 503 no_provider。
// 这两条测试把「子串不能劫持语义」与「真实无候选仍回 503」都钉住。

func TestUpstreamBodyContainingNoProviderSubstringsIsNotMisrouted(t *testing.T) {
	// 上游 400 错误体故意带上 no_provider 的两个旧判定子串：修复前这里
	// 回 503 no_provider（字符串匹配抢先命中），修复后必须仍是 400
	// upstream_rejected_request。
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"400","message":"Param Incorrect: cannot resolve candidates for model; no audio provider available"}}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("upstream 400 with hostile body -> gateway %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_rejected_request") {
		t.Errorf("body should carry upstream_rejected_request: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no_provider") {
		t.Errorf("hostile upstream body must not trigger no_provider: %s", rec.Body.String())
	}
}

func TestNoCandidatesMapsTo503NoProvider(t *testing.T) {
	// resolver 返回空候选列表：锁定 typed audioNoProviderError 仍映射成
	// 503 no_provider（openpocket 的 isNoProvider 识别口径不能退化）。
	ran := false
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		ran = true
		_, _ = w.Write([]byte(`{}`))
	})
	svc.provider = &fakeAudioResolver{candidates: nil}
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if ran {
		t.Errorf("no candidates must fail before any upstream call")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no candidates -> gateway %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no_provider") {
		t.Errorf("body should carry no_provider: %s", rec.Body.String())
	}
}

func TestTTSNoCandidatesMapsTo503NoProvider(t *testing.T) {
	// TTS 面同款：Synthesize 的「无候选」也要走 typed 判定回 503。
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	svc.provider = &fakeAudioResolver{candidates: nil}
	h := NewAudioSpeechHandler(svc)
	payload := `{"model":"mimo-v2.5-tts","input":"你好"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("tts no candidates -> gateway %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no_provider") {
		t.Errorf("body should carry no_provider: %s", rec.Body.String())
	}
}

// ── 8. 官方文档复核后的修正（2026-10-04 批判审计） ──────────────────────
//
// 依据：官方《Speech Synthesis (MiMo-TTS Series) - OpenAI API Compatibility》
// 的 audio.format 只列 wav / mp3 / pcm / pcm16，比 OpenAI /audio/speech 窄
// （OpenAI 还收 opus/aac/flac）。此前的归一口径 audioTTSEffectiveFormat 是
// OpenAI 全量集合，于是客户端按 OpenAI 契约请求 opus 时被原样发给小米，
// 白跑一轮再换一句上游 400。修复前用 opus 实测确认过这个行为（上游确实
// 收到 audio.format="opus"）。
func TestTTSBridgeRejectsOpenAIOnlyFormatsLocally(t *testing.T) {
	for _, format := range []string{"opus", "aac", "flac"} {
		t.Run(format, func(t *testing.T) {
			var hits int32
			svc := newTTSAudioService(t, "mimo-v2.5-tts", func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"UklGRg=="}}}]}`))
			})
			h := NewAudioSpeechHandler(svc)
			payload := fmt.Sprintf(`{"model":"mimo-v2.5-tts","input":"你好","response_format":%q}`, format)
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("format=%s -> gateway %d, want 400 (MiMo does not accept it); body=%s", format, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "invalid_audio_request") {
				t.Errorf("body should carry invalid_audio_request: %s", rec.Body.String())
			}
			if atomic.LoadInt32(&hits) != 0 {
				t.Errorf("format=%s must be rejected before calling upstream, got %d calls", format, hits)
			}
		})
	}
}

func TestTTSBridgeAcceptsOfficialFormatsAndPCM16Alias(t *testing.T) {
	// 官方 pcm 与 pcm16 等价。此前 pcm16 不在归一表里，会被当成乱值
	// 回落成 wav —— 客户端要裸 PCM、拿到 WAV，且无人告知。
	cases := map[string]string{"wav": "wav", "mp3": "mp3", "pcm": "pcm", "pcm16": "pcm"}
	for asked, want := range cases {
		t.Run(asked, func(t *testing.T) {
			var sent string
			svc := newTTSAudioService(t, "mimo-v2.5-tts", func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var p struct {
					Audio map[string]string `json:"audio"`
				}
				_ = json.Unmarshal(body, &p)
				sent = p.Audio["format"]
				_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"UklGRg=="}}}]}`))
			})
			h := NewAudioSpeechHandler(svc)
			payload := fmt.Sprintf(`{"model":"mimo-v2.5-tts","input":"你好","response_format":%q}`, asked)
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("format=%s -> %d; body=%s", asked, rec.Code, rec.Body.String())
			}
			if sent != want {
				t.Errorf("upstream audio.format = %q, want %q", sent, want)
			}
		})
	}
}

func TestNonBridgeCandidateStillAcceptsOpus(t *testing.T) {
	// 反向守卫：opus/aac/flac 在 OpenAI 兼容上游上是合法的，收窄只能作用
	// 在桥接形态。若这条转红，说明有人把 audioTTSEffectiveFormat 的
	 // 全量口径改窄了，会打断所有非小米 TTS 供应商。
	var sent string
	svc := newTestAudioService(t, "zhipu", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p struct {
			ResponseFormat string `json:"response_format"`
		}
		_ = json.Unmarshal(body, &p)
		sent = p.ResponseFormat
		_, _ = w.Write([]byte("binary-audio"))
	})
	h := NewAudioSpeechHandler(svc)
	payload := `{"model":"some-tts","input":"你好","response_format":"opus"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("passthrough candidate with opus -> %d; body=%s", rec.Code, rec.Body.String())
	}
	if sent != "opus" {
		t.Errorf("passthrough upstream response_format = %q, want opus (OpenAI contract)", sent)
	}
}

func TestVoiceCloneRejectsNonWavMP3Sample(t *testing.T) {
	// 官方：voiceclone 只收 mp3/wav 样本。此前只校验 data: 前缀，m4a 样本
	// 能过本地校验再被上游 400。
	var hits int32
	svc := newTTSAudioService(t, "mimo-v2.5-tts-voiceclone", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"UklGRg=="}}}]}`))
	})
	h := NewAudioSpeechHandler(svc)
	payload := `{"model":"mimo-v2.5-tts-voiceclone","input":"你好","voice":"data:audio/m4a;base64,QUJD"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("m4a voice sample -> %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("unsupported sample format must not reach upstream, got %d calls", hits)
	}
}

func TestVoiceCloneAcceptsWavSampleDataURL(t *testing.T) {
	var hits int32
	svc := newTTSAudioService(t, "mimo-v2.5-tts-voiceclone", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"UklGRg=="}}}]}`))
	})
	h := NewAudioSpeechHandler(svc)
	payload := `{"model":"mimo-v2.5-tts-voiceclone","input":"你好","voice":"data:audio/wav;base64,QUJD"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wav voice sample -> %d; body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("wav sample must reach upstream exactly once, got %d", hits)
	}
}

func TestTTSVoiceSubstitutionIsDisclosed(t *testing.T) {
	// 客户端点名 alloy（OpenAI 音色），这条上游没有 → 我们换成 mimo_default。
	// 拿到的声音与请求的不是同一个人，此前静默替换。
	svc := newTTSAudioService(t, "mimo-v2.5-tts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"UklGRg=="}}}]}`))
	})
	h := NewAudioSpeechHandler(svc)
	payload := `{"model":"mimo-v2.5-tts","input":"你好","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Audio-Ignored-Params"); !strings.Contains(got, "voice") {
		t.Errorf("substituted voice must be disclosed, header = %q", got)
	}
}

func TestTTSKnownVoiceIsNotReportedAsIgnored(t *testing.T) {
	svc := newTTSAudioService(t, "mimo-v2.5-tts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"UklGRg=="}}}]}`))
	})
	h := NewAudioSpeechHandler(svc)
	payload := `{"model":"mimo-v2.5-tts","input":"你好","voice":"茉莉"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Gw-Audio-Ignored-Params"); got != "" {
		t.Errorf("known voice must not be reported as ignored, header = %q", got)
	}
}

func TestASRBridgeDisclosesDroppedPromptAndTemperature(t *testing.T) {
	// prompt 是领域词汇提示，被吞掉时转写结果只是「看起来不准」，调用方
	// 无从判断是模型能力问题还是提示词没生效。
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"文本"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBodyWithFields(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", map[string]string{
		"language": "zh", "prompt": "以下是人名：韩梅梅", "temperature": "0.2",
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	got := rec.Header().Get("X-Gw-Audio-Ignored-Params")
	for _, want := range []string{"prompt", "temperature"} {
		if !strings.Contains(got, want) {
			t.Errorf("header %q must disclose %q", got, want)
		}
	}
	if strings.Contains(got, "language") {
		t.Errorf("language=zh is mapped into asr_options and must NOT be reported: %q", got)
	}
}

func TestASRBridgeDisclosesUnmappableLanguage(t *testing.T) {
	// language=ja 归一后落空（省略该键回落到上游自动识别）——这是丢弃，
	// 与「映射成功」要分得开，否则回执本身就是谎。
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"文本"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", "ja")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Gw-Audio-Ignored-Params"); !strings.Contains(got, "language") {
		t.Errorf("unmappable language must be disclosed, header = %q", got)
	}
}

func TestASRPassthroughDoesNotDiscloseIgnoredParams(t *testing.T) {
	// 透传形态三项都按 OpenAI 契约原样上行，没有丢弃可回执。
	svc := newTestAudioService(t, "zhipu", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/audio/transcriptions") {
			_, _ = w.Write([]byte(`{"text":"文本"}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"文本"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBodyWithFields(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", map[string]string{
		"language": "ja", "prompt": "术语表", "temperature": "0.2",
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Gw-Audio-Ignored-Params"); got != "" {
		t.Errorf("passthrough transport drops nothing, header = %q", got)
	}
}

func TestASRStreamDisclosesIgnoredParamsViaEvent(t *testing.T) {
	// 流式路径的响应头已随 WriteHeader(200) 发出，进不去——沿用
	// transcript.transport 的既有处置，走 SSE 事件。
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"文本"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBodyWithFields(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", map[string]string{
		"language": "zh", "prompt": "术语表", "stream": "true",
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "transcript.ignored_params") {
		t.Fatalf("stream path must disclose ignored params via event; body=%s", body)
	}
	if !strings.Contains(body, "prompt") {
		t.Errorf("event payload must name prompt; body=%s", body)
	}
}
