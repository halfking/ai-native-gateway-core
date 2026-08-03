package compression

import (
	"context"
	"errors"
	"testing"
)

type fakeSummaryProvider struct {
	models []string
}

func (f *fakeSummaryProvider) Enabled() bool { return true }

func (f *fakeSummaryProvider) GetCandidates(ctx context.Context, model, profile string) ([]ProviderCandidate, error) {
	f.models = append(f.models, model)
	if model == "missing-model" {
		return nil, errors.New("missing model")
	}
	return []ProviderCandidate{{RawModel: model, BaseURL: "https://example.invalid", APIKey: "key", Available: true}}, nil
}

func TestSummaryClientAdapterFallsBackAcrossModels(t *testing.T) {
	provider := &fakeSummaryProvider{}
	client := newSummaryClientAdapter(&Dependencies{Provider: provider}, "")

	got, err := client.Complete(context.Background(), "prompt")
	if err == nil || got != "" {
		t.Fatalf("missing model should fail without configured model, got (%q, %v)", got, err)
	}
}
