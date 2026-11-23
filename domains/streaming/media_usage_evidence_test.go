package streaming

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

func TestProviderMediaEvidenceFromHandlers(t *testing.T) {
	for _, tc := range []struct {
		name, handler, body, contentType string
		status                           int
		inputUS, billedUS, imgs, videoUS int64
	}{
		{"large base64 image", "image", `{"data":[{"b64_json":"` + strings.Repeat("A", maxModalityEvidenceBody) + `"},{"url":"https://fixture.invalid/image"}],"usage":{"input_tokens":7}}`, "application/json", 200, -1, -1, 2, -1},
		{"zero images", "image", `{"data":[]}`, "application/json", 200, -1, -1, 0, -1},
		{"partial image list", "image", `{"data":[{"url":"https://fixture.invalid/image"},{}]}`, "application/json", 200, -1, -1, -1, -1},
		{"accepted image job", "image", `{"data":[]}`, "application/json", 202, -1, -1, -1, -1},
		{"video completed", "video", `{"id":"v","object":"video","status":"completed","seconds":"4.125"}`, "application/json", 200, -1, -1, -1, 4_125_000},
		{"video queued", "video", `{"id":"v","object":"video","status":"queued","seconds":"4.125"}`, "application/json", 200, -1, -1, -1, -1},
		{"audio duration and billed duration", "multipart", `{"text":"hello","duration":0.5,"usage":{"type":"duration","seconds":1}}`, "application/json", 200, 500_000, 1_000_000, -1, -1},
		{"audio terminal usage", "multipart", "data: {\"type\":\"transcript.text.done\",\"text\":\"hello\",\"usage\":{\"type\":\"duration\",\"seconds\":0.0001}}\n\n", "text/event-stream", 200, -1, 100, -1, -1},
		{"audio interrupted", "multipart", "data: {\"type\":\"transcript.text.delta\",\"usage\":{\"type\":\"duration\",\"seconds\":1}}\n\n", "text/event-stream", 200, -1, -1, -1, -1},
		{"minimax finish", "minimax", "data: {\"delta\":\"hello\",\"finish\":true,\"duration\":1.25}\n\n", "text/event-stream", 200, 1_250_000, -1, -1, -1},
		{"minimax no terminal frame", "minimax", "data: {\"delta\":\"hello\",\"duration\":1.25}\n\n", "text/event-stream", 200, -1, -1, -1, -1},
		{"chat audio complete", "chat", "data: {\"usage\":{\"seconds\":1.234567}}\n\ndata: [DONE]\n\n", "text/event-stream", 200, -1, 1_234_567, -1, -1},
		{"chat audio no sentinel", "chat", "data: {\"usage\":{\"seconds\":1.234567}}\n\n", "text/event-stream", 200, -1, -1, -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			candidate := provider.Candidate{ProviderID: 17, CredentialID: 19, BaseURL: server.URL, RawModel: "m"}
			ctx, client, store := t.Context(), upstream.NewWithRetries(0), newMemoryAttemptStore()
			req := TranscribeRequest{TenantID: "t", RequestID: "r", Filename: "fixture.wav", File: []byte("fixture")}
			var err error
			switch tc.handler {
			case "image":
				h := NewImagesGenerationsHandler(nil, client)
				h.SetAttemptRecorder(store)
				_, _, _, err = h.proxyGenerations(ctx, candidate, []byte(`{"n":9}`), "t", "r")
			case "video":
				h := NewVideosGenerationsHandler(nil, client)
				h.SetAttemptRecorder(store)
				_, _, _, err = h.proxyGenerations(ctx, candidate, []byte(`{"seconds":99}`), "t", "r")
			case "multipart":
				h := NewAudioService(nil, client)
				h.SetAttemptRecorder(store)
				_, err = h.transcribeViaMultipart(ctx, candidate, req, nil)
			case "minimax":
				h := NewAudioService(nil, client)
				h.SetAttemptRecorder(store)
				_, err = h.transcribeViaSpeechToText(ctx, candidate, req, "wav", nil)
			case "chat":
				h := NewAudioService(nil, client)
				h.SetAttemptRecorder(store)
				req.Stream = true
				_, err = h.transcribeViaChatAudio(ctx, candidate, req, "wav", func(string) {})
			}
			if err != nil || len(store.attempts) != 1 {
				t.Fatalf("attempts=%d err=%v", len(store.attempts), err)
			}
			var media reconciliation.MediaUsageEvidence
			for id := range store.attempts {
				usage := store.usage[id]
				if tc.name == "large base64 image" {
					assertMediaQuantity(t, usage.InputTokens, 7)
				}
				media = usage.Media
			}
			assertMediaQuantity(t, media.AudioInputDurationUS, tc.inputUS)
			assertMediaQuantity(t, media.AudioBilledDurationUS, tc.billedUS)
			assertMediaQuantity(t, media.GeneratedImages, tc.imgs)
			assertMediaQuantity(t, media.VideoOutputDurationUS, tc.videoUS)
		})
	}
}

func TestPCMOutputDurationRequiresVerifiedFrames(t *testing.T) {
	wav := make([]byte, 44+16)
	copy(wav, "RIFF")
	copy(wav[8:], "WAVEfmt ")
	copy(wav[36:], "data")
	le := binary.LittleEndian
	le.PutUint32(wav[4:], uint32(len(wav)-8))
	le.PutUint32(wav[16:], 16)
	le.PutUint16(wav[20:], 1)
	le.PutUint16(wav[22:], 1)
	le.PutUint32(wav[24:], 8_000)
	le.PutUint32(wav[28:], 16_000)
	le.PutUint16(wav[32:], 2)
	le.PutUint16(wav[34:], 16)
	le.PutUint32(wav[40:], 16)
	assertMediaQuantity(t, pcmWAVOutputUsage(wav).AudioOutputDurationUS, 1_000)
	returned := append([]byte(nil), wav...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(returned)
	}))
	defer server.Close()
	store := newMemoryAttemptStore()
	h := NewAudioService(nil, upstream.NewWithRetries(0))
	h.SetAttemptRecorder(store)
	_, err := h.synthesizeViaSpeech(t.Context(), provider.Candidate{ProviderID: 2, CredentialID: 3, BaseURL: server.URL, RawModel: "voice"},
		SynthesizeRequest{Input: "fixture", RequestID: "r", TenantID: "t"})
	if err != nil || len(store.usage) != 1 {
		t.Fatalf("recorded usage=%d err=%v", len(store.usage), err)
	}
	for _, usage := range store.usage {
		assertMediaQuantity(t, usage.Media.AudioOutputDurationUS, 1_000)
		assertMediaQuantity(t, usage.Media.AudioBilledDurationUS, -1)
	}
	assertMediaQuantity(t, pcmWAVOutputUsage(wav[:len(wav)-1]).AudioOutputDurationUS, -1)
	le.PutUint32(wav[28:], 8_000)
	assertMediaQuantity(t, pcmWAVOutputUsage(wav).AudioOutputDurationUS, -1)
}

func assertMediaQuantity(t *testing.T, got *int64, want int64) {
	t.Helper()
	if want == -1 && got == nil || got != nil && *got == want {
		return
	}
	if got == nil {
		t.Fatalf("quantity missing, expected %d", want)
	}
	t.Fatalf("quantity=%d, expected %d (-1 means missing)", *got, want)
}
