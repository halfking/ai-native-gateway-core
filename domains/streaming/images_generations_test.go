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
