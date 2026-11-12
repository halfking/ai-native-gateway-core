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
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

type fakeImageResolver struct {
	candidates   []provider.Candidate
	lastModality string
}

func (f *fakeImageResolver) GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error) {
	f.lastModality = modality
	return f.candidates, nil, nil
}

func TestImagesGenerations_missingModel(t *testing.T) {
	h := NewImagesGenerationsHandler(&fakeImageResolver{}, upstream.NewWithRetries(0))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"hi"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestImagesGenerations_noCandidates(t *testing.T) {
	resolver := &fakeImageResolver{candidates: nil}
	h := NewImagesGenerationsHandler(resolver, upstream.NewWithRetries(0))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"dall-e-3","prompt":"cat"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if resolver.lastModality != imageModalityRequest {
		t.Fatalf("modality=%q want image", resolver.lastModality)
	}
}

func TestImagesGenerations_proxySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/images/generations") {
			t.Fatalf("path=%s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "dall-e-3" {
			t.Fatalf("model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://img.example/1.png"}]}`)
	}))
	t.Cleanup(srv.Close)

	resolver := &fakeImageResolver{candidates: []provider.Candidate{{
		CredentialID: 1,
		ProviderID:   1,
		BaseURL:      srv.URL + "/v1",
		APIKey:       "sk-test",
		RawModel:     "dall-e-3",
		Routable:     true,
	}}}
	h := NewImagesGenerationsHandler(resolver, upstream.NewWithRetries(0))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"dall-e-3","prompt":"cat"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Image-Transport"); got != "images-generations" {
		t.Fatalf("transport=%q", got)
	}
}

// ── 2026-11-13 脱敏域逐条审计 P2 收口：media 面端到端钉测 ─────────────────
// 此前 /v1/images/generations 完全绕过 SmartSaniGuard：prompt 明文出上游、
// 响应占位符无从还原。本钉测经真实 SanitizeInputMiddleware + 真实
// SanitizeRestoreInterceptor 走完整 ServeHTTP：
//   ① 上游收到的 prompt 必须是占位符（不是原始手机号）；
//   ② 上游把占位符回显进 revised_prompt 时，客户端必须拿到原文。

func TestImagesGenerationsSanitizeRestoreEndToEnd(t *testing.T) {
	san, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(san, nil, 0)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	restore, err := sanitize.NewSanitizeRestoreInterceptor(san, nil, 0)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	var upstreamPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		upstreamPrompt, _ = body["prompt"].(string)
		// 上游把收到的 prompt 原样回显进 revised_prompt（OpenAI 行为）。
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"revised_prompt":"`+
			strings.ReplaceAll(upstreamPrompt, `"`, `\"`)+`","url":"https://img.example/1.png"}]}`)
	}))
	t.Cleanup(srv.Close)

	resolver := &fakeImageResolver{candidates: []provider.Candidate{{
		CredentialID: 1,
		ProviderID:   1,
		BaseURL:      srv.URL + "/v1",
		APIKey:       "sk-test",
		RawModel:     "dall-e-3",
		Routable:     true,
	}}}
	h := NewImagesGenerationsHandler(resolver, upstream.NewWithRetries(0))
	h.SetSanitizeGuard(mw.Wrap, restore)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"dall-e-3","prompt":"画一张包含 13800138000 的海报"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(upstreamPrompt, "13800138000") {
		t.Fatalf("raw sensitive value leaked to upstream: %q", upstreamPrompt)
	}
	if !strings.Contains(upstreamPrompt, "{SENSITIVE:phone:") {
		t.Fatalf("prompt was not sanitized before dispatch: %q", upstreamPrompt)
	}
	var resp struct {
		Data []struct {
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len=%d", len(resp.Data))
	}
	if !strings.Contains(resp.Data[0].RevisedPrompt, "13800138000") {
		t.Fatalf("placeholder was NOT restored for client: %q", resp.Data[0].RevisedPrompt)
	}
	if strings.Contains(rec.Body.String(), "{SENSITIVE:phone:") {
		t.Fatalf("internal placeholder marker leaked to client: %s", rec.Body.String())
	}
}

// fail-closed：上游回显 gateway 占位符、但本请求映射缺席（占位符是伪造的）
// ⇒ 未知标记必须打码，不得把内部标记透给客户端。
func TestImagesGenerationsRestoreMasksForgedMarker(t *testing.T) {
	san, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(san, nil, 0)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	restore, err := sanitize.NewSanitizeRestoreInterceptor(san, nil, 0)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 上游「伪造」了一个本请求从未产生过的占位符。
		_, _ = io.WriteString(w, `{"created":1,"data":[{"revised_prompt":"forged {SENSITIVE:phone:42}"}]}`)
	}))
	t.Cleanup(srv.Close)

	resolver := &fakeImageResolver{candidates: []provider.Candidate{{
		CredentialID: 1, ProviderID: 1, BaseURL: srv.URL + "/v1", APIKey: "sk-test",
		RawModel: "dall-e-3", Routable: true,
	}}}
	h := NewImagesGenerationsHandler(resolver, upstream.NewWithRetries(0))
	h.SetSanitizeGuard(mw.Wrap, restore)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"dall-e-3","prompt":"cat"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "{SENSITIVE:phone:42}") {
		t.Fatalf("forged placeholder leaked to client: %s", rec.Body.String())
	}
}
