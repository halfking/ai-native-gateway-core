// Package streaming — audio_endpoints_test.go
//
// 音频端点的行为锁定测试：
//  1. multipart 契约解析（openpocket 探测的字段组合）；
//  2. chat-audio 桥接（小米形态）非流式 + SSE 流式事件序；
//  3. multipart 透传 404 → chat 桥接回落的候选内 fallback；
//  4. TTS 桥接（assistant 消息 + voice 归一）；
//  5. MCP JSON-RPC 最小面（initialize/tools/list/tools/call）。
package streaming

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// fakeAudioResolver 返回固定候选，绕过 DB。
type fakeAudioResolver struct {
	candidates []provider.Candidate
}

func (f *fakeAudioResolver) GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error) {
	return f.candidates, nil, nil
}

// newTestAudioService 挂一个 httptest 上游并返回 service。
// upstream.New() 的代理解析器对 127.0.0.1 直连，httptest 上游可达。
func newTestAudioService(t *testing.T, catalogCode string, upstreamHandler http.HandlerFunc) *AudioService {
	t.Helper()
	srv := httptest.NewServer(upstreamHandler)
	t.Cleanup(srv.Close)
	cand := provider.Candidate{
		CredentialID: 1,
		ProviderID:   1,
		BaseURL:      srv.URL,
		Protocol:     "openai-completions",
		CatalogCode:  catalogCode,
		RawModel:     "mimo-v2.5-asr",
		Routable:     true,
		APIKey:       "sk-test",
	}
	cand.AvailabilityState = "ready"
	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())
	return svc
}

func TestTranscriptionsChatAudioBridgeNonStream(t *testing.T) {
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "input_audio") {
			t.Errorf("chat-audio bridge must carry input_audio, got %s", string(body)[:min(200, len(body))])
		}
		if !strings.Contains(r.URL.Path, "/chat/completions") {
			t.Errorf("xiaomi candidate must hit chat/completions, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"今天我们开会"}}],"usage":{"seconds":9}}`))
	})
	h := NewAudioTranscriptionsHandler(svc)

	wav := tinyWAVForTest()
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, wav, "test.wav", "mimo-v2.5-asr", "zh")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not json: %v", err)
	}
	if out.Text != "今天我们开会" {
		t.Fatalf("text = %q", out.Text)
	}
	if got := rec.Header().Get("X-Gw-Audio-Transport"); got != AudioTransportChatAudio {
		t.Fatalf("transport = %q", got)
	}
	if got := rec.Header().Get("X-Gw-Audio-Seconds"); got != "9" {
		t.Fatalf("audio seconds = %q", got)
	}
}

func TestTranscriptionsMultipartFallbackToChatAudio(t *testing.T) {
	// 非 xiaomi 候选：先打 /audio/transcriptions，上游 404 后回落 chat 桥接。
	svc := newTestAudioService(t, "zhipu", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/audio/transcriptions") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fallback 文本"}}]}`))
	})
	h := NewAudioTranscriptionsHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", "zh")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fallback 文本") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Audio-Transport"); got != AudioTransportChatAudio {
		t.Fatalf("transport = %q", got)
	}
}

func TestTranscriptionsMultipartRelayVerbatim(t *testing.T) {
	svc := newTestAudioService(t, "zhipu", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/audio/transcriptions") {
			// 校验透传的关键字段
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Errorf("upstream multipart parse: %v", err)
			}
			if r.FormValue("model") != "mimo-v2.5-asr" {
				t.Errorf("relayed model = %q", r.FormValue("model"))
			}
			if r.FormValue("language") != "zh" {
				t.Errorf("relayed language = %q", r.FormValue("language"))
			}
			_, _ = w.Write([]byte(`{"text":"relay 文本","segments":[]}`))
			return
		}
		t.Errorf("should not hit chat for zhipu candidate")
	})
	h := NewAudioTranscriptionsHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBody(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr", "zh")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"relay 文本"`) {
		t.Fatalf("relay body = %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Audio-Transport"); got != AudioTransportTranscriptions {
		t.Fatalf("transport = %q", got)
	}
}

func TestTranscriptionsChatAudioStreamSSE(t *testing.T) {
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"今天\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"开会\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"seconds\":4}}\n\n" +
			"data: [DONE]\n\n"))
	})
	h := NewAudioTranscriptionsHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	buildTranscriptionsMultipartBodyWithFields(t, req, tinyWAVForTest(), "a.wav", "mimo-v2.5-asr",
		map[string]string{"stream": "true"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{
		"event: transcript.text.delta",
		`"delta":"今天"`,
		`"delta":"开会"`,
		"event: transcript.text.done",
		`"text":"今天开会"`,
		"data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q:\n%s", want, body)
		}
	}
}

func TestSpeechChatAudioTTSBridge(t *testing.T) {
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed struct {
			Model      string   `json:"model"`
			Modalities []string `json:"modalities"`
			Audio      struct {
				Voice  string `json:"voice"`
				Format string `json:"format"`
			} `json:"audio"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Errorf("tts bridge body parse: %v", err)
		}
		if parsed.Model != "mimo-v2.5-asr" { // resolver 返回的 RawModel
			t.Errorf("tts model = %q", parsed.Model)
		}
		if len(parsed.Modalities) != 2 || parsed.Modalities[0] != "text" || parsed.Modalities[1] != "audio" {
			t.Errorf("tts bridge must set modalities [text audio], got %+v", parsed.Modalities)
		}
		if parsed.Audio.Voice != "mimo_default" {
			t.Errorf("unknown voice must normalize to mimo_default, got %q", parsed.Audio.Voice)
		}
		if len(parsed.Messages) == 0 || parsed.Messages[0].Role != "assistant" {
			t.Errorf("tts text must be an assistant message, got %+v", parsed.Messages)
		}
		fake := base64.StdEncoding.EncodeToString([]byte("RIFFfake"))
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"audio":{"data":%q,"transcript":"你好"}}}]}`, fake)))
	})
	h := NewAudioSpeechHandler(svc)

	payload := `{"model":"mimo-v2.5-tts","input":"你好","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "RIFFfake" {
		t.Fatalf("audio payload = %q", rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Audio-Transport"); got != AudioTransportChatAudio {
		t.Fatalf("transport = %q", got)
	}
}

func TestMCPToolsCallTranscribe(t *testing.T) {
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"mcp 转写文本"}}]}`))
	})
	h := NewAudioMCPHandler(svc)

	payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"transcribe_audio","arguments":{"audio_base64":%q,"format":"wav","model":"mimo-v2.5-asr"}}}`,
		base64.StdEncoding.EncodeToString(tinyWAVForTest()))
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mcp 转写文本") {
		t.Fatalf("tools/call body = %s", rec.Body.String())
	}
}

func TestTranscriptionsParseErrors(t *testing.T) {
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {})
	h := NewAudioTranscriptionsHandler(svc)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty body", "", "invalid multipart"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

// TestMCPEarlyErrorsUseJsonRPCEnvelope —— R89-ES（228 号）守卫：
//
// MCP streamable HTTP 2025-06-18 规范要求：协议级错误（空 body / batch 请求 /
// JSON 解析失败）必须以 HTTP 200 + JSON-RPC envelope `{jsonrpc:"2.0", error:{code:-32700/-32600,...}, id:null}`
// 的形式返回，而不是 HTTP 4xx + OpenAI envelope。
//
// 当前实现把 4 处 early-error 都用 `writeErrorJSON` 走 OpenAI envelope，
// 让本门转红 —— 这是预期行为：门的目的不是「现在绿」，而是**钉住「未来修法后必须满足」的契约**。
//
// 修复者把 4 处 early-error 改成 JSON-RPC envelope 后，本门自动转绿；
// 4 处全部覆盖，不豁免（避免「挑一条通过就当过了」的失明）。
func TestMCPEarlyErrorsUseJsonRPCEnvelope(t *testing.T) {
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {})
	h := NewAudioMCPHandler(svc)

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantHTTP int // expected transport-level HTTP status (per spec)
	}{
		{
			name:     "empty body -> Parse error -32700",
			body:     "",
			wantCode: -32700,
			wantHTTP: http.StatusOK, // JSON-RPC 协议级错误 → HTTP 200
		},
		{
			name:     "batch request -> Invalid Request -32600",
			body:     `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`,
			wantCode: -32600,
			wantHTTP: http.StatusOK,
		},
		{
			name:     "invalid JSON -> Parse error -32700",
			body:     `{"jsonrpc":`,
			wantCode: -32700,
			wantHTTP: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantHTTP {
				t.Errorf("status = %d, want %d (transport-level should be 200 for JSON-RPC protocol errors)",
					rec.Code, tc.wantHTTP)
			}
			var env struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				// OpenAI envelope fields — must NOT appear
				Type string `json:"type"`
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("response not valid JSON-RPC envelope: %v\nbody=%s", err, rec.Body.String())
			}
			if env.JSONRPC != "2.0" {
				t.Errorf("jsonrpc field = %q, want \"2.0\" (current impl emits OpenAI envelope without this field)", env.JSONRPC)
			}
			if env.Error == nil {
				t.Fatalf("error field missing; current impl emits OpenAI envelope with `type`/`code` instead of `error.code`\nbody=%s", rec.Body.String())
			}
			if env.Error.Code != tc.wantCode {
				t.Errorf("error.code = %d, want %d", env.Error.Code, tc.wantCode)
			}
			if env.Type != "" || env.Code != "" {
				t.Errorf("OpenAI envelope fields leaked: type=%q code=%q (should be absent in JSON-RPC envelope)", env.Type, env.Code)
			}
			// 2026-10-04：本守卫原为**无条件** t.Log，措辞断言「当前实现违反
			// 协议契约」——修好之后它照样打印同样的话，变成一条会误导人的
			// 绿测日志。改为只在真违反时说话。
			if rec.Code != tc.wantHTTP {
				t.Logf("228 号 守卫命中: %s 当前实现违反 MCP streamable HTTP 2025-06-18 协议契约"+
					"（HTTP %d + 非 JSON-RPC envelope）；需切换为 HTTP 200 + JSON-RPC envelope {code:%d}",
					tc.name, rec.Code, tc.wantCode)
			} else {
				t.Logf("228 号 守卫: %s 已符合协议契约（HTTP 200 + JSON-RPC envelope {code:%d}）",
					tc.name, tc.wantCode)
			}
		})
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func tinyWAVForTest() []byte {
	// 44 字节 WAV 头 + 800 字节静音（8kHz 8bit 100ms）
	b := make([]byte, 44+800)
	copy(b[0:4], "RIFF")
	le32ForTest(b[4:], 36+800)
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	le32ForTest(b[16:], 16)
	le16ForTest(b[20:], 1)
	le16ForTest(b[22:], 1)
	le32ForTest(b[24:], 8000)
	le32ForTest(b[28:], 8000)
	le16ForTest(b[32:], 1)
	le16ForTest(b[34:], 8)
	copy(b[36:40], "data")
	le32ForTest(b[40:], 800)
	return b
}

func le32ForTest(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func le16ForTest(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func buildTranscriptionsMultipartBody(t *testing.T, req *http.Request, wav []byte, filename, model, language string) {
	fields := map[string]string{"language": language, "model": model}
	buildTranscriptionsMultipartBodyWithFields(t, req, wav, filename, model, fields)
}

func buildTranscriptionsMultipartBodyWithFields(t *testing.T, req *http.Request, wav []byte, filename, model string, fields map[string]string) {
	t.Helper()
	var buf strings.Builder
	boundary := "gwaudiotest"
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"" + filename + "\"\r\n")
	buf.WriteString("Content-Type: audio/wav\r\n\r\n")
	filePart := buf.String() + string(wav) + "\r\n"
	var tail strings.Builder
	writePartTo(&tail, "model", model)
	for k, v := range fields {
		if k == "model" {
			continue
		}
		writePartTo(&tail, k, v)
	}
	tail.WriteString("--" + boundary + "--\r\n")

	full := filePart + tail.String()
	req.Body = io.NopCloser(strings.NewReader(full))
	req.ContentLength = int64(len(full))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
}

func writePartTo(buf *strings.Builder, name, value string) {
	buf.WriteString("--gwaudiotest\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\n")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}
