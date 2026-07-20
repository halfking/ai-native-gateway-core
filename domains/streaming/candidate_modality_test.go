package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

type modalityResolverSpy struct {
	lastModel    string
	lastProfile  string
	lastTenantID string
	lastModality string
}

func (s *modalityResolverSpy) Enabled() bool { return true }

func (s *modalityResolverSpy) GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error) {
	return s.GetCandidatesByModality(ctx, model, profile, tenantID, "text")
}

func (s *modalityResolverSpy) GetCandidatesByModality(_ context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error) {
	s.lastModel = model
	s.lastProfile = profile
	s.lastTenantID = tenantID
	s.lastModality = modality
	return nil, provider.DefaultPolicy(), nil
}

func (s *modalityResolverSpy) ModelKnown(context.Context, string) bool { return true }

func TestResolveCandidatesForRequest_UsesVisionModalityForImageInput(t *testing.T) {
	resolver := &modalityResolverSpy{}
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`)

	_, _, modality, err := resolveCandidatesForRequest(context.Background(), resolver, "gpt-4o", "test-profile", "tenant-a", body)
	if err != nil {
		t.Fatalf("resolveCandidatesForRequest: %v", err)
	}
	if modality != "vision" {
		t.Fatalf("modality=%q want vision", modality)
	}
	if resolver.lastModality != "vision" {
		t.Fatalf("resolver modality=%q want vision", resolver.lastModality)
	}
	if resolver.lastModel != "gpt-4o" || resolver.lastProfile != "test-profile" || resolver.lastTenantID != "tenant-a" {
		t.Fatalf("resolver args not forwarded: model=%q profile=%q tenant=%q", resolver.lastModel, resolver.lastProfile, resolver.lastTenantID)
	}
}

func TestResolveCandidatesForRequest_UsesTextModalityForPlainText(t *testing.T) {
	resolver := &modalityResolverSpy{}
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)

	_, _, modality, err := resolveCandidatesForRequest(context.Background(), resolver, "gpt-4o", "", "default", body)
	if err != nil {
		t.Fatalf("resolveCandidatesForRequest: %v", err)
	}
	if modality != "text" {
		t.Fatalf("modality=%q want text", modality)
	}
	if resolver.lastModality != "text" {
		t.Fatalf("resolver modality=%q want text", resolver.lastModality)
	}
}
