package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

type fakeVideoResolver struct {
	candidates   []provider.Candidate
	lastModality string
}

func (f *fakeVideoResolver) GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error) {
	f.lastModality = modality
	return f.candidates, nil, nil
}

func TestVideosGenerations_noCandidates(t *testing.T) {
	resolver := &fakeVideoResolver{}
	h := NewVideosGenerationsHandler(resolver, upstream.New())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", strings.NewReader(`{"model":"sora","prompt":"clip"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if resolver.lastModality != videoModalityRequest {
		t.Fatalf("modality=%q", resolver.lastModality)
	}
}
