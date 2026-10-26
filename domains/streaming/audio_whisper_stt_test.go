// Package streaming — audio_whisper_stt_test.go
//
// whisper.cpp /inference 传输形态的行为锁定（2026-10-26 本机 whisper-server
// 1.9.5 实测形状）：
//   - whisper-cpp / whisper-cpp-large 候选钉死 /inference，不空跑
//     /audio/transcriptions 的 404；
//   - multipart file/model/language/prompt/response_format 上行，JSON {text} 下行；
//   - verbose_json 回落 json（该上游没有 verbose 方言）；
//   - 其它 catalog code（含云端 whisper-* 模型名的 custom 候选）不受影响。
package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

func TestPreferWhisperInferenceMatchesOnlyWhisperCatalogCodes(t *testing.T) {
	for _, code := range []string{"whisper-cpp", "whisper-cpp-large", "Whisper-CPP", " whisper-cpp "} {
		if !preferWhisperInference(provider.Candidate{CatalogCode: code}) {
			t.Errorf("code %q should prefer whisper inference", code)
		}
	}
	// 关键负例：云端供应商也有 whisper-* 模型名，但 catalog_code 不是
	// whisper-cpp——绝不能按模型名猜方言。
	for _, code := range []string{"", "custom", "minimax", "groq", "ollama"} {
		if preferWhisperInference(provider.Candidate{CatalogCode: code, RawModel: "whisper-large-v3"}) {
			t.Errorf("code %q must NOT prefer whisper inference (host speaks OpenAI dialect)", code)
		}
	}
}

func TestWhisperInferenceNonStream(t *testing.T) {
	var hitPath string
	svc := newSpeechToTextTestService(t, "whisper-cpp", "whisper-large-v3-turbo", func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("request not multipart: %v", err)
			return
		}
		if got := r.FormValue("model"); got != "whisper-large-v3-turbo" {
			t.Errorf("model form = %q", got)
		}
		if got := r.FormValue("language"); got != "zh" {
			t.Errorf("language form = %q, want zh", got)
		}
		if got := r.FormValue("response_format"); got != "json" {
			t.Errorf("response_format = %q, want json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"今天下午3点\n我们在252服务器上\n排查网关出口流量异常问题"}`))
	})
	res, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "whisper-large-v3-turbo", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
		Language: "zh",
	}, nil)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if hitPath != "/inference" {
		t.Fatalf("upstream path = %q, want /inference", hitPath)
	}
	if res.Text == "" || res.Transport != AudioTransportWhisperInference {
		t.Fatalf("text=%q transport=%q", res.Text, res.Transport)
	}
	if res.UpstreamModel != "whisper-large-v3-turbo" {
		t.Fatalf("upstream model = %q", res.UpstreamModel)
	}
}

func TestWhisperInferenceVerboseJsonFallsBackToJSON(t *testing.T) {
	var gotFormat string
	svc := newSpeechToTextTestService(t, "whisper-cpp-large", "whisper-large-v3", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		gotFormat = r.FormValue("response_format")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	})
	res, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "whisper-large-v3", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
		ResponseFormat: "verbose_json",
	}, nil)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if gotFormat != "json" {
		t.Fatalf("response_format sent = %q, want json fallback (upstream has no verbose dialect)", gotFormat)
	}
	if res.Text != "ok" {
		t.Fatalf("text = %q", res.Text)
	}
}

// TestWhisperSkipsTranscriptionsEndpoint：钉 /inference 的候选不应先向
// /audio/transcriptions 打一发注定 404 的空往返（与 MiniMax 同款断言）。
func TestWhisperSkipsTranscriptionsEndpoint(t *testing.T) {
	var paths []string
	svc := newSpeechToTextTestService(t, "whisper-cpp", "whisper-large-v3-turbo", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/inference" {
			_, _ = w.Write([]byte(`{"text":"hi"}`))
			return
		}
		http.NotFound(w, r)
	})
	if _, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "whisper-large-v3-turbo", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
	}, nil); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/inference" {
		t.Fatalf("upstream paths = %v, want exactly [/inference]", paths)
	}
}

// TestWhisperUpstreamErrorSurfaces：上游 4xx（如音频不是 16kHz WAV）必须
// 原样透出，不能被静默吞掉。
func TestWhisperUpstreamErrorSurfaces(t *testing.T) {
	svc := newSpeechToTextTestService(t, "whisper-cpp", "whisper-large-v3-turbo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid WAV file: sample rate must be 16000"}`))
	})
	_, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "whisper-large-v3-turbo", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
	}, nil)
	if err == nil {
		t.Fatal("expected upstream 400 to surface")
	}
	if up, ok := err.(interface{ StatusCode() int }); !ok || up.StatusCode() != http.StatusBadRequest {
		t.Fatalf("error should carry upstream status 400, got %v", err)
	}
}

// TestWhisperBaseURL不带V1：transcribeViaWhisperInference 直接拼 {base}/inference，
// 注册约定 base_url 不带 /v1——若运营误带 /v1，路径必须原样呈现（不静默剥离）。
func TestWhisperBaseURLPathJoining(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		_, _ = w.Write([]byte(`{"text":"x"}`))
	}))
	defer srv.Close()
	cand := provider.Candidate{BaseURL: srv.URL, CatalogCode: "whisper-cpp", RawModel: "m", Protocol: "openai-completions", Routable: true, APIKey: "sk-test", AvailabilityState: "ready"}
	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())
	if _, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "m", File: tinyWAVForTest(), Filename: "a.wav", ContentType: "audio/wav",
	}, nil); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if hitPath != "/inference" {
		t.Fatalf("path = %q, want /inference (base must be registered WITHOUT /v1)", hitPath)
	}
}
