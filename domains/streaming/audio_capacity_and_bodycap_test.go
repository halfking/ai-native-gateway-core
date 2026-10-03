// Package streaming — audio_capacity_and_bodycap_test.go
//
// R40 审计钉测：
//   - R1：/v1/audio/transcriptions 必须有请求体总字节上限（此前
//     ParseMultipartForm 的 32MiB 只是内存阈值，超限部分静默落盘临时
//     文件，总量无上限；speech/mcp 两面均有 MaxBytesReader，唯此面漏包）。
//   - R2：音频并发闸耗尽必须报 503 audio_capacity（此前裸 error 落到
//     502 upstream_error，把网关自身饱和伪装成上游故障）。
package streaming

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// TestAudioCapacityErrorIsTypedAndMapped：闸满 + ctx 取消 → typed
// audioCapacityError；writeAudioError 面上映射 503/audio_capacity。
func TestAudioCapacityErrorIsTypedAndMapped(t *testing.T) {
	svc := NewAudioService(&fakeAudioResolver{candidates: nil}, upstream.New())
	// 占满全部 8 个并发槽。
	for i := 0; i < maxConcurrentAudioOps; i++ {
		svc.sem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := svc.Transcribe(ctx, TranscribeRequest{
		Model: "mimo-v2.5-asr", File: tinyWAVForTest(),
		Filename: "a.wav", ContentType: "audio/wav",
	}, nil)
	var capErr *audioCapacityError
	if !errors.As(err, &capErr) {
		t.Fatalf("gate-exhausted Transcribe err = %v, want *audioCapacityError", err)
	}

	// handler 面映射：503 + audio_capacity（不再 502 upstream_error）。
	rec := httptest.NewRecorder()
	writeAudioError(rec, "req-cap", capErr)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("writeAudioError(capacity) status = %d, want 503", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("audio_capacity")) {
		t.Errorf("writeAudioError(capacity) body lacks audio_capacity code: %s", rec.Body.String())
	}
}

// TestTranscriptionsBodyCapRejectsOversize：超过 body 上限（32MiB 文件
// + 1MiB 开销余量）的 multipart 请求 → 413 request_too_large，而不是
// 被完整读入内存/落盘后再判定。
func TestTranscriptionsBodyCapRejectsOversize(t *testing.T) {
	h := NewAudioTranscriptionsHandler(NewAudioService(&fakeAudioResolver{}, upstream.New()))

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "huge.wav")
	if err != nil {
		t.Fatal(err)
	}
	// 33.5MiB：文件级上限是 32MiB，body 上限 33MiB——这里放一个既超
	// body 上限也超文件上限的载荷，验证 body cap 先在解析阶段拒绝。
	if _, err := fw.Write(bytes.Repeat([]byte{0}, 33<<20|(512<<10))); err != nil {
		t.Fatal(err)
	}
	_ = mw.WriteField("model", "mimo-v2.5-asr")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body status = %d, want 413 (body=%s)", rec.Code, truncateForLog(rec.Body.String(), 200))
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("request_too_large")) {
		t.Errorf("oversize body code want request_too_large, got: %s", truncateForLog(rec.Body.String(), 200))
	}
}

// TestTranscriptionsBodyCapAllowsNormalFile：上限内的合法 multipart 请求
// 仍可正常解析（回归保护：MaxBytesReader 不能误伤正常路径）。
func TestTranscriptionsBodyCapAllowsNormalFile(t *testing.T) {
	// 只验证解析层：用一个可达的 fake 上游让请求走完 parse 阶段。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"seconds":1}}`))
	}))
	defer srv.Close()
	cand := provider.Candidate{
		CredentialID: 1, ProviderID: 1, BaseURL: srv.URL,
		Protocol: "openai-completions", CatalogCode: "xiaomi",
		RawModel: "mimo-v2.5-asr", Routable: true, APIKey: "sk-test",
	}
	cand.AvailabilityState = "ready"
	h := NewAudioTranscriptionsHandler(NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New()))

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.wav")
	if _, err := fw.Write(tinyWAVForTest()); err != nil {
		t.Fatal(err)
	}
	_ = mw.WriteField("model", "mimo-v2.5-asr")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("normal-size body wrongly rejected as 413 (body=%s)", truncateForLog(rec.Body.String(), 200))
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("normal-size body status = %d, want 200 (body=%s)", rec.Code, truncateForLog(rec.Body.String(), 200))
	}
}
