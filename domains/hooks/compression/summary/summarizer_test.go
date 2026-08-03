package summary

import (
	"context"
	"errors"
	"testing"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/settings"
)

type fakeLLMClient struct {
	calls []summaryCall
}

type summaryCall struct {
	prompt string
	cfg    CompletionConfig
}

func (f *fakeLLMClient) Complete(ctx context.Context, prompt string, opts ...CompletionOption) (string, error) {
	cfg := CompletionConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	f.calls = append(f.calls, summaryCall{prompt: prompt, cfg: cfg})
	if cfg.Model == "bad-model" {
		return "", errors.New("bad model")
	}
	return "summary from " + cfg.Model, nil
}

func TestSummarizerTriesConfiguredModelsInOrder(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: map[string][]byte{
		"summary_models.technical_details": jsonString(t, "bad-model, good-model"),
	}})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerSummaryTestSpecs(registry)
	settings.Global = registry

	client := &fakeLLMClient{}
	s := NewSummarizer(client)
	got, err := s.Summarize(context.Background(), appconfig.SummaryDimensionTechnical, "conversation")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "summary from good-model" {
		t.Fatalf("summary = %q", got)
	}
	if len(client.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(client.calls))
	}
	if client.calls[0].cfg.Model != "bad-model" || client.calls[1].cfg.Model != "good-model" {
		t.Fatalf("call models = %#v", client.calls)
	}
	if client.calls[1].cfg.MaxTokens != 1024 {
		t.Fatalf("technical max tokens = %d, want 1024", client.calls[1].cfg.MaxTokens)
	}
	if client.calls[1].cfg.Temperature != 0.1 {
		t.Fatalf("technical temperature = %v, want 0.1", client.calls[1].cfg.Temperature)
	}
}

func TestDimensionForTaskType(t *testing.T) {
	cases := []struct {
		taskType string
		want     appconfig.SummaryDimension
	}{
		{taskType: "code_debug", want: appconfig.SummaryDimensionTechnical},
		{taskType: "data_analysis", want: appconfig.SummaryDimensionTechnical},
		{taskType: "deployment", want: appconfig.SummaryDimensionTasks},
		{taskType: "", want: appconfig.SummaryDimensionProject},
	}
	for _, tc := range cases {
		if got := DimensionForTaskType(tc.taskType); got != tc.want {
			t.Fatalf("DimensionForTaskType(%q) = %q, want %q", tc.taskType, got, tc.want)
		}
	}
}
