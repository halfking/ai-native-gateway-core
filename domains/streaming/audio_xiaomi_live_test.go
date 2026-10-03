// Package streaming — audio_xiaomi_live_test.go
//
// 小米 MiMo 音频面的**实网上游**验证（opt-in）。
//
// 为什么要有这一层：上面 audio_xiaomi_variants_test.go 的桩只证明「我们按
// 声称的契约发报文」，不证明**上游真的这么收**。这一组用真实 key 打真实
// endpoint，把两者钉在一起：
//
//	LLM_GATEWAY_LIVE_XIAOMI_KEY   tp-/ttp- 开头的 Token Plan key
//	LLM_GATEWAY_LIVE_XIAOMI_BASE  可选，默认 https://token-plan-cn.xiaomimimo.com/v1
//
// 未设置 key 时全部 SKIP（默认 `go test` 不产生任何外部流量与费用）。
//
// 关键设计是**差分对照**：对同一个「上游会拒收的值」，既直接打上游（断言
// 4xx），又经网关 handler 打（断言 2xx）。只断言网关成功的话，网关把值
// 丢掉也能过——那正是本文件要防的回归（见 normalizeASRLanguageForCandidate）。
package streaming

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

const (
	liveDefaultBase = "https://token-plan-cn.xiaomimimo.com/v1"
	// liveUnmappableLanguage 会被小米 asr_options.language 严格校验拒收。
	// 用它做差分对照的「负」一侧。
	liveUnmappableLanguage = "zh-CN"
	liveMappedLanguage     = "zh"
)

func liveBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_LIVE_XIAOMI_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return liveDefaultBase
}

// liveXiaomiService 构造一个指向真实上游的 AudioService（候选 = 小米）。
func liveXiaomiService(t *testing.T, rawModel string) *AudioService {
	t.Helper()
	cand := provider.Candidate{
		CredentialID: 1, ProviderID: 1,
		BaseURL:  liveBaseURL(),
		Protocol: "openai-completions", CatalogCode: "xiaomi",
		RawModel: rawModel, Routable: true,
		APIKey: strings.TrimSpace(os.Getenv("LLM_GATEWAY_LIVE_XIAOMI_KEY")),
	}
	cand.AvailabilityState = "ready"
	return NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())
}

// liveSilenceWAV 造 1s 24kHz16bit 单声道静音 WAV（不发外部文件依赖）。
func liveSilenceWAV() []byte {
	const rate, secs, bits, ch = 24000, 1, 16, 1
	dataLen := rate * secs * ch * bits / 8
	b := make([]byte, 44+dataLen)
	copy(b[0:4], "RIFF")
	le32ForTest(b[4:], uint32(36+dataLen))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	le32ForTest(b[16:], 16)
	le16ForTest(b[20:], 1)
	le16ForTest(b[22:], ch)
	le32ForTest(b[24:], rate)
	le32ForTest(b[28:], uint32(rate*ch*bits/8))
	le16ForTest(b[32:], ch*bits/8)
	le16ForTest(b[34:], bits)
	copy(b[36:40], "data")
	le32ForTest(b[40:], uint32(dataLen))
	return b
}

// liveDirectASR 直连上游发一次 chat+input_audio，透传 asr_options。
func liveDirectASR(t *testing.T, language string) (int, []byte) {
	t.Helper()
	payload := map[string]any{
		"model": "mimo-v2.5-asr",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type": "input_audio",
				"input_audio": map[string]string{
					"data":   base64.StdEncoding.EncodeToString(liveSilenceWAV()),
					"format": "wav",
				},
			}},
		}},
	}
	if language != "" {
		payload["asr_options"] = map[string]string{"language": language}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, liveBaseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build direct request: %v", err)
	}
	req.Header.Set("api-key", strings.TrimSpace(os.Getenv("LLM_GATEWAY_LIVE_XIAOMI_KEY")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("direct upstream call: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw
}

func skipUnlessLiveXiaomi(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("LLM_GATEWAY_LIVE_XIAOMI_KEY")) == "" {
		t.Skip("set LLM_GATEWAY_LIVE_XIAOMI_KEY to run live MiMo audio tests (consumes upstream quota)")
	}
}

// TestLiveXiaomiASRLanguageDifferential 是本文件的核心：同一个
// 「上游拒收的语种写法」，直连 4xx、经网关 2xx。
func TestLiveXiaomiASRLanguageDifferential(t *testing.T) {
	skipUnlessLiveXiaomi(t)

	// 负对照：asr_options.language 的严格白名单是真实存在的。
	stDirect, body := liveDirectASR(t, liveUnmappableLanguage)
	if stDirect != http.StatusBadRequest {
		t.Fatalf("control: upstream must reject asr_options.language=%q with 400, got %d: %s",
			liveUnmappableLanguage, stDirect, firstLine(string(body)))
	}
	t.Logf("control ok: upstream rejects %q -> 400 (%s)", liveUnmappableLanguage, firstLine(string(body)))

	// 正对照：合法值确实 200（否则上面的 400 可能只是别的原因）。
	if st, b := liveDirectASR(t, liveMappedLanguage); st != http.StatusOK {
		t.Fatalf("control: asr_options.language=%q must be accepted, got %d: %s", liveMappedLanguage, st, firstLine(string(b)))
	}

	// 主体：经网关 handler 用同一种「上游拒收」的写法，必须 200。
	svc := liveXiaomiService(t, "mimo-v2.5-asr")
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, liveSilenceWAV(), "a.wav", "mimo-v2.5-asr", liveUnmappableLanguage)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("gateway must normalize %q before forwarding, got %d: %s",
			liveUnmappableLanguage, rec.Code, firstLine(rec.Body.String()))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("gateway response not json: %v (%s)", err, firstLine(rec.Body.String()))
	}
	if out.Text == "" {
		t.Error("gateway transcription text is empty")
	}
	if got := rec.Header().Get("X-Gw-Audio-Transport"); got != AudioTransportChatAudio {
		t.Errorf("transport = %q, want %q", got, AudioTransportChatAudio)
	}
	t.Logf("gateway ok: language=%q -> asr_options.language=%q, text=%q", liveUnmappableLanguage, liveMappedLanguage, out.Text)
}

func TestLiveXiaomiASRStream(t *testing.T) {
	skipUnlessLiveXiaomi(t)
	svc := liveXiaomiService(t, "mimo-v2.5-asr")
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBodyWithFields(t, req, liveSilenceWAV(), "a.wav", "mimo-v2.5-asr",
		map[string]string{"stream": "true"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, firstLine(rec.Body.String()))
	}
	body := rec.Body.String()
	for _, want := range []string{"transcript.text.done", "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE missing %q", want)
		}
	}
}

func TestLiveXiaomiUnsupportedFormatIsRejectedLocally(t *testing.T) {
	skipUnlessLiveXiaomi(t)
	// 本地前置校验：不能打上游。用一个不可路由的 base 证明它没打——
	// 若代码真发了请求，net.Dial 到不可达主机必然报错而不是 400。
	cand := provider.Candidate{
		CredentialID: 1, ProviderID: 1,
		BaseURL:  "http://127.0.0.1:1/v1", // 永远连不上
		Protocol: "openai-completions", CatalogCode: "xiaomi",
		RawModel: "mimo-v2.5-asr", Routable: true, APIKey: "sk-unused",
	}
	cand.AvailabilityState = "ready"
	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())
	h := NewAudioTranscriptionsHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, liveSilenceWAV(), "a.m4a", "mimo-v2.5-asr", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("m4a must be rejected locally with 400, got %d: %s", rec.Code, firstLine(rec.Body.String()))
	}
}

func TestLiveXiaomiTTSBuiltin(t *testing.T) {
	skipUnlessLiveXiaomi(t)
	svc := liveXiaomiService(t, "mimo-v2.5-tts")
	h := NewAudioSpeechHandler(svc)
	body := `{"model":"mimo-v2.5-tts","input":"网关语音合成测试。","voice":"茉莉","speed":1.5}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, firstLine(rec.Body.String()))
	}
	if !isWAVBytes(rec.Body.Bytes()) {
		t.Errorf("expected WAV bytes, got magic %x", rec.Body.Bytes()[:min(4, rec.Body.Len())])
	}
	if got := rec.Header().Get("X-Gw-Audio-Ignored-Params"); got != "speed" {
		t.Errorf("X-Gw-Audio-Ignored-Params = %q, want \"speed\"", got)
	}
}

func TestLiveXiaomiTTSVoiceDesign(t *testing.T) {
	skipUnlessLiveXiaomi(t)
	// 这条在修复前必然 400：voicedesign 不接受 audio.voice，且要求 user 描述。
	svc := liveXiaomiService(t, "mimo-v2.5-tts-voicedesign")
	h := NewAudioSpeechHandler(svc)
	body := `{"model":"mimo-v2.5-tts-voicedesign","input":"音色设计测试。","voice":"温柔清亮的女声，语速适中"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("voicedesign must work after fix, got %d: %s", rec.Code, firstLine(rec.Body.String()))
	}
	if !isWAVBytes(rec.Body.Bytes()) {
		t.Errorf("expected WAV bytes, got magic %x", rec.Body.Bytes()[:min(4, rec.Body.Len())])
	}
}

func TestLiveXiaomiTTSVoiceCloneRejectsVoiceName(t *testing.T) {
	skipUnlessLiveXiaomi(t)
	// 传音色名（而不是样本 DataURL）必须在本地 400，且不打上游。
	svc := liveXiaomiService(t, "mimo-v2.5-tts-voiceclone")
	h := NewAudioSpeechHandler(svc)
	body := `{"model":"mimo-v2.5-tts-voiceclone","input":"克隆测试。","voice":"茉莉"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("voiceclone with a voice name must be 400, got %d: %s", rec.Code, firstLine(rec.Body.String()))
	}
}

func TestLiveXiaomiTTSVoiceCloneWithSample(t *testing.T) {
	skipUnlessLiveXiaomi(t)
	sample := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(liveSilenceWAV())
	svc := liveXiaomiService(t, "mimo-v2.5-tts-voiceclone")
	h := NewAudioSpeechHandler(svc)
	body := fmt.Sprintf(`{"model":"mimo-v2.5-tts-voiceclone","input":"克隆测试。","voice":%q}`, sample)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("voiceclone with a sample must be 200, got %d: %s", rec.Code, firstLine(rec.Body.String()))
	}
	if !isWAVBytes(rec.Body.Bytes()) {
		t.Errorf("expected WAV bytes, got magic %x", rec.Body.Bytes()[:min(4, rec.Body.Len())])
	}
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return s[:i]
	}
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}
