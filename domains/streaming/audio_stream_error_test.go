// Package streaming — audio_stream_error_test.go
//
// 39 轮（2026-10-03 12h 审计）钉测：
//  1. P1-1：chat-audio 桥接流中的上游错误帧必须让调用失败，而不是把
//     截断文本当成功返回；且部分增量已发出后 Transcribe 不得换候选重跑
//     （否则客户端收到两段拼接的转写流）。
//  2. P2-2：并发闸——第 maxConcurrentAudioOps+1 个音频调用在槽位释放前
//     不得进入上游请求。
//  3. P2-4：TTS 桥接的 Content-Type 按 RIFF 魔数纠偏（WAV 字节不再标成
//     客户端请求的 mp3）。
//  4. P3-4：音色归一返回集合的规范小写键。
package streaming

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// TestRelayChatAudioSSEErrorFrame 单元级：错误帧在增量之后到达时，
// relayChatAudioSSE 必须返回 error，携带上游错误消息。
func TestRelayChatAudioSSEErrorFrame(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"部分\"}}]}\n\n" +
		"data: {\"error\":{\"message\":\"audio too short\",\"type\":\"invalid_request_error\"}}\n\n" +
		"data: [DONE]\n\n"
	var got []string
	text, _, err := relayChatAudioSSE(context.Background(), strings.NewReader(stream), func(d string) {
		got = append(got, d)
	})
	if err == nil {
		t.Fatalf("error frame must fail the relay, got success text=%q", text)
	}
	if !strings.Contains(err.Error(), "audio too short") {
		t.Fatalf("error must carry upstream message, got %q", err.Error())
	}
	if len(got) != 1 || got[0] != "部分" {
		t.Fatalf("deltas emitted before error frame = %v", got)
	}
}

// TestTranscribeStreamErrorNoFailover 集成级：第一个候选（xiaomi 桥接）已
// 发出增量后遇流中错误 → Transcribe 必须直接失败，不得重试第二个健康候选。
func TestTranscribeStreamErrorNoFailover(t *testing.T) {
	var secondHits int32
	var mu sync.Mutex
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		secondHits++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"text":"second candidate text"}`))
	}))
	defer second.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" +
			"data: {\"error\":{\"message\":\"internal_error_midstream\"}}\n\n"))
	}))
	defer first.Close()

	mkCand := func(url, catalog, raw string) provider.Candidate {
		c := provider.Candidate{
			CredentialID: 1, ProviderID: 1,
			BaseURL: url, Protocol: "openai-completions",
			CatalogCode: catalog, RawModel: raw,
			Routable: true, APIKey: "sk-test",
		}
		c.AvailabilityState = "ready"
		return c
	}
	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{
		mkCand(first.URL, "xiaomi", "mimo-v2.5-asr"),
		mkCand(second.URL, "zhipu", "glm-asr"),
	}}, upstream.New())

	var emitted []string
	_, err := svc.Transcribe(context.Background(), TranscribeRequest{
		Model: "mimo-v2.5-asr", Stream: true,
		File: tinyWAVForTest(), Filename: "a.wav",
	}, func(delta string) { emitted = append(emitted, delta) })
	if err == nil {
		t.Fatalf("mid-stream error after deltas must surface, got success")
	}
	if !strings.Contains(err.Error(), "internal_error_midstream") {
		t.Fatalf("error must carry upstream frame message, got %q", err.Error())
	}
	mu.Lock()
	hits := secondHits
	mu.Unlock()
	if hits != 0 {
		t.Fatalf("failover after delivered deltas is forbidden, second candidate hit %d times", hits)
	}
	if len(emitted) != 1 || emitted[0] != "partial" {
		t.Fatalf("emitted deltas = %v, want [partial]", emitted)
	}
}

// TestAudioConcurrencyGate P2-2：占满并发槽后，下一个 Transcribe 必须等待
// （在观察窗口内没有发出上游请求），释放后放行。
func TestAudioConcurrencyGate(t *testing.T) {
	var upstreamEnter = make(chan struct{}, 16)
	releaseFirst := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamEnter <- struct{}{}
		<-releaseFirst // 首个请求挂住，占住槽位
		_, _ = w.Write([]byte(`{"text":"done"}`))
	}))
	defer srv.Close()
	defer func() { close(releaseFirst) }()

	cand := provider.Candidate{
		CredentialID: 1, ProviderID: 1,
		BaseURL: srv.URL, Protocol: "openai-completions",
		CatalogCode: "zhipu", RawModel: "glm-asr",
		Routable: true, APIKey: "sk-test",
	}
	cand.AvailabilityState = "ready"
	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())

	// 占满全部槽位（每个阻塞在上游）。
	for i := 0; i < maxConcurrentAudioOps; i++ {
		go svc.Transcribe(context.Background(), TranscribeRequest{
			Model: "glm-asr", File: tinyWAVForTest(), Filename: "a.wav",
		}, nil)
	}
	// 等待 maxConcurrentAudioOps 个请求全部进入上游。
	for i := 0; i < maxConcurrentAudioOps; i++ {
		select {
		case <-upstreamEnter:
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d did not reach upstream within 5s", i)
		}
	}

	// 第 9 个请求：必须卡在闸门（上游不再有新进入），且短超时 ctx 让它
	// 以容量等待错误退出而不是无限阻塞。
	gatedCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := svc.Transcribe(gatedCtx, TranscribeRequest{
		Model: "glm-asr", File: tinyWAVForTest(), Filename: "a.wav",
	}, nil)
	if err == nil {
		t.Fatalf("gated request must not succeed while slots are full")
	}
	select {
	case <-upstreamEnter:
		t.Fatalf("gated request must not reach upstream while slots are full")
	default:
	}
	if !strings.Contains(err.Error(), "audio capacity") && gatedCtx.Err() == nil {
		t.Fatalf("gated failure must come from the capacity gate, got %v", err)
	}
}

// TestSynthesizeBridgeWAVContentTypeSniff P2-4：桥接回 WAV 字节而客户端
// 请求 mp3 时，Content-Type 必须钉回 audio/wav。
func TestSynthesizeBridgeWAVContentTypeSniff(t *testing.T) {
	wav := append([]byte("RIFF"), make([]byte, 4)...)
	wav = append(wav, []byte("WAVEfmt ")...)
	svc := newTestAudioService(t, "xiaomi", func(w http.ResponseWriter, r *http.Request) {
		fake := base64.StdEncoding.EncodeToString(wav)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"audio":{"data":%q,"transcript":"你好"}}}]}`, fake)))
	})
	h := NewAudioSpeechHandler(svc)

	payload := `{"model":"mimo-v2.5-tts","input":"你好","voice":"Mia","response_format":"mp3"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("RIFF/WAVE bytes must be labeled audio/wav even when mp3 requested, got %q", got)
	}
}

// TestNormalizeTTSVoiceCanonicalCase P3-4（2026-10-05 修正断言方向）：
// 匹配保持大小写不敏感，但回传必须保留客户端原值——小米上游音色
// 大小写敏感（Mia/Chloe/Milo/Dean 首字母大写），早先「归一到小写键」
// 的断言把 Mia 写成 mia，上游 400 "Unknown voice: mia"，英文音色全族
// 不可用（真实音频回环验证抓到）。中文音色无大小写问题不受影响。
func TestNormalizeTTSVoiceCanonicalCase(t *testing.T) {
	cand := provider.Candidate{CatalogCode: "xiaomi"}
	if got := normalizeTTSVoiceForCandidate(cand, "Mia"); got != "Mia" {
		t.Fatalf("Mia must relay canonical %q, got %q", "Mia", got)
	}
	if got := normalizeTTSVoiceForCandidate(cand, "mia"); got != "Mia" {
		t.Fatalf("lowercase client input must relay canonical %q, got %q", "Mia", got)
	}
	if got := normalizeTTSVoiceForCandidate(cand, "MIMO_DEFAULT"); got != "mimo_default" {
		t.Fatalf("must relay canonical %q, got %q", "mimo_default", got)
	}
	if got := normalizeTTSVoiceForCandidate(cand, "冰糖"); got != "冰糖" {
		t.Fatalf("chinese voice must pass through, got %q", got)
	}
	if got := normalizeTTSVoiceForCandidate(cand, "alloy"); got != "mimo_default" {
		t.Fatalf("unknown voice must fall back to mimo_default, got %q", got)
	}
}
