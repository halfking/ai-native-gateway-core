// Package streaming — audio_minimax_stt_test.go
//
// MiniMax speech_to_text 传输形态的行为锁定（2026-10-06 直连实测形状）：
//   - minimax 候选钉死 /speech_to_text，不空跑 /audio/transcriptions 的 404；
//   - 非流式 JSON {text,duration,trace_id} → Text + DurationSeconds；
//   - 流式私有 SSE {index,delta,finish} 帧 → 逐帧 delta 下发 + 全文拼接；
//   - stream 与 srt/vtt 互斥：response_format 非空时不携带 stream；
//   - zhipu 候选行为不变（仍 multipart 透传优先）。
package streaming

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// newSpeechToTextTestService 与 newTestAudioService 同构，但候选模型名
// 与 catalog_code 可指定（minimax 的上游 raw 名是 asr-1.0）。
func newSpeechToTextTestService(t *testing.T, catalogCode, rawModel string, upstreamHandler http.HandlerFunc) *AudioService {
	t.Helper()
	srv := httptest.NewServer(upstreamHandler)
	t.Cleanup(srv.Close)
	cand := provider.Candidate{
		CredentialID:      1,
		ProviderID:        1,
		BaseURL:           srv.URL,
		Protocol:          "openai-completions",
		CatalogCode:       catalogCode,
		RawModel:          rawModel,
		Routable:          true,
		APIKey:            "sk-test",
		AvailabilityState: "ready",
	}
	return NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())
}

func minimaxTestCandidate(baseURL string) provider.Candidate {
	return provider.Candidate{
		CredentialID:      1,
		ProviderID:        1,
		BaseURL:           baseURL,
		Protocol:          "openai-completions",
		CatalogCode:       "minimax",
		RawModel:          "asr-1.0",
		Routable:          true,
		APIKey:            "sk-test",
		AvailabilityState: "ready",
	}
}

func TestSpeechToTextNonStream(t *testing.T) {
	var hitPath string
	svc := newSpeechToTextTestService(t, "minimax", "asr-1.0", func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("request not multipart: %v", err)
		}
		if got := r.FormValue("model"); got != "asr-1.0" {
			t.Errorf("model form = %q, want asr-1.0", got)
		}
		if r.FormValue("stream") != "" {
			t.Errorf("non-stream request must not carry stream=true, got %q", r.FormValue("stream"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"各位好，今天开会","duration":9.44,"trace_id":"abc"}`))
	})
	res, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "minimax-asr-1.0", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
	}, nil)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if hitPath != "/speech_to_text" {
		t.Fatalf("upstream path = %q, want /speech_to_text", hitPath)
	}
	if res.Text != "各位好，今天开会" {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Transport != AudioTransportSpeechToText {
		t.Fatalf("transport = %q", res.Transport)
	}
	if res.DurationSeconds == nil || *res.DurationSeconds != 9.44 {
		t.Fatalf("duration = %v, want 9.44", res.DurationSeconds)
	}
	if res.UpstreamModel != "asr-1.0" {
		t.Fatalf("upstream model = %q", res.UpstreamModel)
	}
}

func TestSpeechToTextStreamingSSE(t *testing.T) {
	svc := newSpeechToTextTestService(t, "minimax", "asr-1.0", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
		}
		if r.FormValue("stream") != "true" {
			t.Errorf("stream form = %q, want true", r.FormValue("stream"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"index\":0,\"delta\":\"各\",\"finish\":false}\n\n" +
			"data: {\"index\":1,\"delta\":\"位好，今天开会\",\"finish\":false}\n\n" +
			"data: {\"index\":2,\"delta\":\"。\",\"finish\":true,\"duration\":4.6}\n\n"))
	})
	var deltas []string
	res, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "minimax-asr-1.0", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav", Stream: true,
	}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if res.Text != "各位好，今天开会。" {
		t.Fatalf("text = %q", res.Text)
	}
	if strings.Join(deltas, "|") != "各|位好，今天开会|。" {
		t.Fatalf("deltas = %v", deltas)
	}
	if res.DurationSeconds == nil || *res.DurationSeconds != 4.6 {
		t.Fatalf("duration = %v, want 4.6 from terminal frame", res.DurationSeconds)
	}
}

func TestSpeechToTextStreamMutuallyExclusiveWithSRT(t *testing.T) {
	svc := newSpeechToTextTestService(t, "minimax", "asr-1.0", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("stream") != "" {
			t.Errorf("stream must be omitted when response_format is srt, got %q", r.FormValue("stream"))
		}
		_, _ = w.Write([]byte(`{"text":"x","duration":1}`))
	})
	if _, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "minimax-asr-1.0", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
		Stream: true, ResponseFormat: "srt",
	}, nil); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
}

func TestMinimaxSkipsTranscriptionsEndpoint(t *testing.T) {
	// minimax 候选不许先打 /audio/transcriptions（实测 404 的空往返）。
	svc := newSpeechToTextTestService(t, "minimax", "asr-1.0", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/audio/transcriptions") {
			t.Errorf("minimax candidate must not hit standard path, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"text":"ok","duration":1}`))
	})
	if _, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "minimax-asr-1.0", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
	}, nil); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
}

func TestZhipuCandidateKeepsMultipartFirst(t *testing.T) {
	// zhipu 候选的行为不得被 minimax 分支改变：仍先打标准透传路径。
	var paths []string
	svc := newSpeechToTextTestService(t, "zhipu", "glm-asr", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.URL.Path, "/audio/transcriptions") {
			_, _ = io.WriteString(w, `{"text":"透传直返"}`)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	res, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "glm-asr", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
	}, nil)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if len(paths) != 1 || !strings.Contains(paths[0], "/audio/transcriptions") {
		t.Fatalf("paths = %v, want single /audio/transcriptions hit", paths)
	}
	if res.Text != "透传直返" || res.Transport != AudioTransportTranscriptions {
		t.Fatalf("res = %+v", res)
	}
}

func TestPreferSpeechToTextMatchesOnlyMinimax(t *testing.T) {
	if !preferSpeechToText(provider.Candidate{CatalogCode: "minimax"}) {
		t.Fatal("minimax must prefer speech_to_text")
	}
	if preferSpeechToText(provider.Candidate{CatalogCode: " xiaomi "}) {
		t.Fatal("xiaomi must not prefer speech_to_text")
	}
	if preferSpeechToText(provider.Candidate{}) {
		t.Fatal("empty catalog must not prefer speech_to_text")
	}
}

// MCP 面在注入 transform 后 tools/list 必须出现四个工具（锁定工具面扩张）。
func TestMCPToolsListIncludesTransformTools(t *testing.T) {
	svc := NewAudioService(&fakeAudioResolver{}, upstream.New())
	h := NewAudioMCPHandler(svc)
	h.SetTransformService(NewAudioTransformService(svc))

	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("tools/list not json: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range out.Result.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"transcribe_audio", "synthesize_speech", "refine_transcription", "analyze_transcription"} {
		if !names[want] {
			t.Fatalf("tools/list missing %q, got %v", want, names)
		}
	}
}

// 未注入 transform 时 tools/list 不得声明新工具（旧部署零行为变化）。
func TestMCPToolsListWithoutTransformOmitsTransformTools(t *testing.T) {
	h := NewAudioMCPHandler(NewAudioService(&fakeAudioResolver{}, upstream.New()))
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "refine_transcription") || strings.Contains(body, "analyze_transcription") {
		t.Fatalf("transform tools must be absent without injection: %s", body)
	}
}
