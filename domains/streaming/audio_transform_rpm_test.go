// Package streaming — audio_transform_rpm_test.go
//
// transform 端点的鉴权/限流行为锁定（2026-10-07 完善轮）：
//   - /v1/audio/refine|analyze 的鉴权走 authenticateTransform——校验 key
//     与预算，但**不消耗 RPM**（LLM 步骤环回 chat 面已计一次，双计会把
//     周期 analyze 打成 429）；
//   - 环回 chat 503 no_candidate 时，HTTP 面映射 503 no_provider，
//     MCP 面透出 alternatives 建议清单。
package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// countingLimiter 记录 CheckRPM 调用次数的假限流器。
type countingLimiter struct {
	checks int
	inner  ratelimit.RPMLimiter
}

func (l *countingLimiter) CheckRPM(keyID int, limit int) bool {
	l.checks++
	return l.inner.CheckRPM(keyID, limit)
}

func (l *countingLimiter) RPMStatus(keyID int, limit int) (int, int) {
	return l.inner.RPMStatus(keyID, limit)
}

// fakeTransformVerifier 是最小 AudioKeyVerifier：任意 sk-* 放行。
type fakeTransformVerifier struct {
	budgetChecks int
}

func (v *fakeTransformVerifier) Enabled() bool { return true }
func (v *fakeTransformVerifier) Verify(_ context.Context, rawKey string) (*authentication.KeyInfo, error) {
	if strings.HasPrefix(rawKey, "sk-") {
		return &authentication.KeyInfo{ID: 77, TenantID: "default"}, nil
	}
	return nil, &authentication.InvalidKeyError{Message: "bad key"}
}
func (v *fakeTransformVerifier) CheckBudget(_ context.Context, _ int) error {
	v.budgetChecks++
	return nil
}

func newRpmTestTransform(t *testing.T) (*AudioTransformHandler, *countingLimiter, *fakeTransformVerifier, *httptest.Server) {
	t.Helper()
	svc := NewAudioService(&fakeAudioResolver{}, upstream.New())
	lim := ratelimit.NewSlidingWindowLimiter()
	t.Cleanup(lim.Stop)
	cl := &countingLimiter{inner: lim}
	ver := &fakeTransformVerifier{}
	svc.SetAuth(nil, cl) // keyVerifier 走接口前先置 nil，下面替换
	svc.keyVerifier = ver
	ts := NewAudioTransformService(svc)
	h := NewAudioTransformHandler(ts)

	chatMux := http.NewServeMux()
	chatMux.Handle("/v1/audio/refine", h)
	chatMux.Handle("/v1/audio/analyze", h)
	chatMux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"stub","choices":[{"message":{"content":"{\"refined\":\"精修结果文本\"}"}}]}`))
	})
	srv := httptest.NewServer(chatMux)
	t.Cleanup(srv.Close)
	return h, cl, ver, srv
}

func TestTransformEndpointDoesNotConsumeRPM(t *testing.T) {
	_, cl, ver, srv := newRpmTestTransform(t)
	resp, body := transformPost(t, srv.URL, "/v1/audio/refine", `{"model":"m","text":"原文"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	// 鉴权发生过（预算检查 1 次），但限流器一次都没被问。
	if ver.budgetChecks != 1 {
		t.Fatalf("budget checks = %d, want 1", ver.budgetChecks)
	}
	if cl.checks != 0 {
		t.Fatalf("RPM consumed %d times by transform endpoint, want 0 (loopback chat owns the single count)", cl.checks)
	}
}

func TestTransformLoopback503MapsToNoProviderWithAlternatives(t *testing.T) {
	svc := NewAudioService(&fakeAudioResolver{}, upstream.New())
	ts := NewAudioTransformService(svc)
	h := NewAudioTransformHandler(ts)
	mux := http.NewServeMux()
	mux.Handle("/v1/audio/refine", h)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"no_candidate","message":"No candidates available","alternatives":{"requested_model":"glm-4.7","task_type":"code","alternatives":[{"model":"kimi-k3"},{"model":"deepseek-v4-pro"},{"model":"minimax-text-01"}]}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, body := transformPost(t, srv.URL, "/v1/audio/refine", `{"model":"glm-4.7","text":"原文"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"no_provider"`) {
		t.Fatalf("error code should be no_provider, got %s", body)
	}
}

func TestExtractAlternativeModels(t *testing.T) {
	body := []byte(`{"error":{"alternatives":{"alternatives":[{"model":"kimi-k3"},{"model":"deepseek-v4-pro"}]}}}`)
	alts := extractAlternativeModels(body)
	if len(alts) != 2 || alts[0] != "kimi-k3" || alts[1] != "deepseek-v4-pro" {
		t.Fatalf("alts = %v", alts)
	}
	if alts := extractAlternativeModels([]byte(`not json`)); alts != nil {
		t.Fatalf("garbage body must give nil, got %v", alts)
	}
}
