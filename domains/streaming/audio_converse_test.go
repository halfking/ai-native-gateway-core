// Package streaming — audio_converse_test.go
//
// /v1/audio/converse 级联语音会话端点的行为锁定测试（2026-11-26 语音流式轮）：
//  1. 全链路（ASR chat-audio 桥接 → 环回 LLM → TTS 桥接）非流式 JSON 契约；
//  2. stream=true 的 SSE 事件序（transcript.* → reply.* → response.audio → [DONE]）；
//  3. 纯文本输入（跳过 ASR）与 tts_model 缺省（跳过 TTS）的裁剪语义；
//  4. 参数校验（缺 chat_model / 无输入 / base64 非法 / history 越界）；
//  5. 转写空文本拒绝进 LLM。
//
// 环回 LLM 的打点：converseChat 的环回目标由请求 Host 推导 ⇒ 测试经一个
// 反代 httptest.Server 发请求（前端 → handler → 同一 server 的
// /v1/chat/completions 桩），X-Request-Id 由 handler 生成，chat 桩按
// X-Gateway-Internal-Purpose=audio_converse 识别自己的调用。
package streaming

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// providerCandidateForTest 造一个指向测试上游的 xiaomi 候选（chat-audio
// 桥接判据按 catalog_code=xiaomi 命中，ASR/TTS 两段共用）。
func providerCandidateForTest(baseURL string) provider.Candidate {
	cand := provider.Candidate{
		CredentialID: 1,
		ProviderID:   1,
		BaseURL:      baseURL,
		Protocol:     "openai-completions",
		CatalogCode:  "xiaomi",
		RawModel:     "mimo-v2.5-asr",
		Routable:     true,
		APIKey:       "sk-test-upstream",
	}
	cand.AvailabilityState = "ready"
	return cand
}

// mustReadAll 限长读 body（测试专用）。
func mustReadAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// converseFixture 装一个含 ASR/TTS 上游 + 环回 chat 桩的三合一测试环境。
// upstreamHandler 处理音频面调用（新 TestServer），chatStubs 处理环回
// /v1/chat/completions（前端 server，handler 经 Host 推导环回到这里）。
type converseFixture struct {
	front *httptest.Server // 请求入口 + 环回 chat 桩
	svc   *AudioService
}

func newConverseFixture(t *testing.T, chatHandler http.HandlerFunc, upstreamHandler http.HandlerFunc) *converseFixture {
	t.Helper()
	audioUpstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(audioUpstream.Close)

	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{providerCandidateForTest(audioUpstream.URL)}}, upstream.NewWithRetries(0))

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			chatHandler(w, r)
			return
		}
		// 其余路径反代到 handler（挂在 front 自己身上的 mux 外层不可行——
		// handler 需要 front 的 Host 做环回推导，所以这里显式反代）。
		target, _ := url.Parse(audioUpstream.URL)
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	return &converseFixture{front: front, svc: svc}
}

func conversePost(t *testing.T, f *converseFixture, payload string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewConverseHandler(NewConverseService(f.svc))
	req := httptest.NewRequest(http.MethodPost, f.front.URL+"/v1/audio/converse", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// fakeChatStubs 返回 chat 环回桩：校验 model 与 messages 里含转写文本，
// 回固定 LLM 答案。
func fakeChatStubs(t *testing.T, wantModel, wantUserContains, reply string, seen *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Gateway-Internal-Purpose") != "audio_converse" {
			t.Errorf("loopback chat purpose = %q", r.Header.Get("X-Gateway-Internal-Purpose"))
		}
		var parsed struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		body := mustReadAll(t, r.Body)
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Errorf("chat stub body parse: %v", err)
		}
		if parsed.Model != wantModel {
			t.Errorf("chat model = %q, want %q", parsed.Model, wantModel)
		}
		if wantUserContains != "" {
			found := false
			for _, m := range parsed.Messages {
				if m.Role == "user" && strings.Contains(m.Content, wantUserContains) {
					found = true
				}
			}
			if !found {
				t.Errorf("chat messages must carry user turn containing %q, got %+v", wantUserContains, parsed.Messages)
			}
		}
		*seen = append(*seen, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q}}],"model":"qwen3:8b"}`, reply)))
	}
}

func TestConverseFullCascadeNonStream(t *testing.T) {
	// 音频上游同时接住 ASR（chat-audio 桥接：input_audio 块）与 TTS
	// （assistant 消息 + modalities）。catalog_code=xiaomi ⇒ 两段都走
	// chat-audio 桥接（candidate 的 RawModel 固定 mimo-v2.5-asr）。
	var chatCalls []string
	f := newConverseFixture(t,
		fakeChatStubs(t, "qwen3:8b", "你好呀", "我很好，谢谢。", &chatCalls),
		func(w http.ResponseWriter, r *http.Request) {
			body := mustReadAll(t, r.Body)
			var parsed struct {
				Messages []struct {
					Role    string `json:"role"`
					Content []struct {
						Type       string `json:"type"`
						InputAudio *struct {
							Data   string `json:"data"`
							Format string `json:"format"`
						} `json:"input_audio"`
					} `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal([]byte(body), &parsed)
			isASR := false
			isTTS := false
			for _, m := range parsed.Messages {
				for _, c := range m.Content {
					if c.InputAudio != nil {
						isASR = true
					}
				}
				if m.Role == "assistant" {
					isTTS = true
				}
			}
			if isASR {
				// 桥接转写非流式（converse 编排传 emitDelta=nil）：单 JSON。
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"你好呀"}}],"usage":{"seconds":1.5}}`))
				return
			}
			if isTTS {
				fake := base64.StdEncoding.EncodeToString([]byte("RIFFfake-wav"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"audio":{"data":%q,"transcript":"我很好"}}}]}`, fake)))
				return
			}
			t.Errorf("audio upstream got neither ASR nor TTS shape: %s", body[:min(200, len(body))])
		})

	audio := base64.StdEncoding.EncodeToString([]byte("fake-wav-bytes"))
	payload := fmt.Sprintf(`{"asr_model":"whisper-large-v3-turbo","chat_model":"qwen3:8b","tts_model":"tts-xiaomi","voice":"alloy","audio":{"data":%q,"format":"wav"},"system_prompt":"你是助手","history":[{"role":"user","content":"早"},{"role":"assistant","content":"早！有事？"}]}`, audio)
	rec := conversePost(t, f, payload)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var out converseResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if out.Transcript == nil || out.Transcript.Text != "你好呀" {
		t.Errorf("transcript = %+v, want text 你好呀", out.Transcript)
	}
	if out.Transcript != nil && out.Transcript.Transport != AudioTransportChatAudio {
		t.Errorf("transport = %q, want %q", out.Transcript.Transport, AudioTransportChatAudio)
	}
	if out.Reply == nil || out.Reply.Content != "我很好，谢谢。" {
		t.Errorf("reply = %+v", out.Reply)
	}
	if out.Audio == nil {
		t.Fatalf("audio missing")
	}
	raw, err := base64.StdEncoding.DecodeString(out.Audio.Data)
	if err != nil || string(raw) != "RIFFfake-wav" {
		t.Errorf("audio round-trip broken: %q err %v", out.Audio.Data, err)
	}
	if out.Audio.Format != "wav" {
		t.Errorf("audio format = %q, want wav", out.Audio.Format)
	}
	if len(chatCalls) != 1 {
		t.Fatalf("loopback chat calls = %d, want 1", len(chatCalls))
	}
	if !strings.Contains(chatCalls[0], "早") {
		t.Errorf("chat payload must carry history, got %s", chatCalls[0])
	}
}

func TestConverseTextOnlyNoTTS(t *testing.T) {
	var chatCalls []string
	f := newConverseFixture(t,
		fakeChatStubs(t, "qwen3:8b", "今天天气", "晴天。", &chatCalls),
		func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("audio upstream must not be called for text-only/no-tts request")
		})
	payload := `{"chat_model":"qwen3:8b","text":"今天天气怎么样"}`
	rec := conversePost(t, f, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var out converseResult
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Transcript != nil {
		t.Errorf("transcript must be absent for text input, got %+v", out.Transcript)
	}
	if out.Audio != nil {
		t.Errorf("audio must be absent without tts_model, got %+v", out.Audio)
	}
	if out.Reply == nil || out.Reply.Content != "晴天。" {
		t.Errorf("reply = %+v", out.Reply)
	}
}

func TestConverseSSEEventOrder(t *testing.T) {
	var chatCalls []string
	f := newConverseFixture(t,
		fakeChatStubs(t, "qwen3:8b", "语音问题", "语音回答", &chatCalls),
		func(w http.ResponseWriter, r *http.Request) {
			// ASR 段（input_audio 块）回非流式 JSON 转写；TTS 段（assistant
			// 消息）回 chat-audio 音频。
			body := mustReadAll(t, r.Body)
			if strings.Contains(body, "input_audio") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"语音问题"}}]}`))
				return
			}
			fake := base64.StdEncoding.EncodeToString([]byte("RIFF"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"audio":{"data":%q,"transcript":"t"}}}]}`, fake)))
		})
	audio := base64.StdEncoding.EncodeToString([]byte("fake"))
	payload := fmt.Sprintf(`{"chat_model":"qwen3:8b","tts_model":"tts-xiaomi","audio":{"data":%q,"format":"wav"},"stream":true}`, audio)
	rec := conversePost(t, f, payload)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	idxTransport := strings.Index(body, "event: transcript.transport")
	idxTranscript := strings.Index(body, "event: transcript.text.done")
	idxDelta := strings.Index(body, "event: reply.text.delta")
	idxDone := strings.Index(body, "event: reply.text.done")
	idxAudio := strings.Index(body, "event: response.audio")
	idxEnd := strings.Index(body, "data: [DONE]")
	for name, idx := range map[string]int{
		"transcript.transport": idxTransport, "transcript.text.done": idxTranscript,
		"reply.text.delta": idxDelta, "reply.text.done": idxDone,
		"response.audio": idxAudio, "[DONE]": idxEnd,
	} {
		if idx < 0 {
			t.Fatalf("SSE missing event %s, body:\n%s", name, body)
		}
	}
	if !(idxTransport < idxTranscript && idxTranscript < idxDelta && idxDelta < idxDone && idxDone < idxAudio && idxAudio < idxEnd) {
		t.Errorf("SSE event order broken: transport=%d transcript=%d delta=%d done=%d audio=%d end=%d",
			idxTransport, idxTranscript, idxDelta, idxDone, idxAudio, idxEnd)
	}
	if !strings.Contains(body, "语音回答") {
		t.Errorf("reply delta must carry LLM content")
	}
}

func TestConverseValidationErrors(t *testing.T) {
	var chatCalls []string
	f := newConverseFixture(t,
		fakeChatStubs(t, "qwen3:8b", "", "never", &chatCalls),
		func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("audio upstream must not be reached on validation failure")
		})
	cases := []struct {
		name    string
		payload string
		code    int
	}{
		{"missing_chat_model", `{"text":"hi"}`, http.StatusBadRequest},
		{"no_input", `{"chat_model":"qwen3:8b"}`, http.StatusBadRequest},
		{"bad_base64", `{"chat_model":"qwen3:8b","audio":{"data":"!!!not-base64!!!","format":"wav"}}`, http.StatusBadRequest},
		{"history_too_long", `{"chat_model":"qwen3:8b","text":"hi","history":[` + strings.Repeat(`{"role":"user","content":"x"},`, maxConverseHistoryTurns) + `{"role":"user","content":"y"}]}`, http.StatusBadRequest},
		{"bad_history_role", `{"chat_model":"qwen3:8b","text":"hi","history":[{"role":"tool","content":"x"}]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := conversePost(t, f, tc.payload)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("error envelope not JSON: %v", err)
			}
			if env.Error.Code == "" {
				t.Errorf("error envelope missing code")
			}
		})
	}
	if len(chatCalls) != 0 {
		t.Errorf("loopback chat must not be called on validation failures, got %d", len(chatCalls))
	}
}

func TestConverseEmptyTranscriptRejected(t *testing.T) {
	var chatCalls []string
	f := newConverseFixture(t,
		fakeChatStubs(t, "qwen3:8b", "", "never", &chatCalls),
		func(w http.ResponseWriter, r *http.Request) {
			// chat-audio ASR 桥接返回空转写（非流式 JSON）。
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
		})
	audio := base64.StdEncoding.EncodeToString([]byte("silence"))
	payload := fmt.Sprintf(`{"chat_model":"qwen3:8b","audio":{"data":%q,"format":"wav"}}`, audio)
	rec := conversePost(t, f, payload)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "empty_transcript") {
		t.Errorf("error code = %s", rec.Body.String())
	}
	if len(chatCalls) != 0 {
		t.Errorf("empty transcript must not reach LLM, got %d chat calls", len(chatCalls))
	}
}
