// Package summary - summarizer_coverage_test.go
//
// Coverage-gap tests for the summary package. The existing summarizer_test.go
// only exercises SummaryDimensionTechnical and DimensionForTaskType. This file
// adds the missing branches for the remaining five dimensions and the
// SummarizeAll / error paths.

package summary

import (
	"context"
	"errors"
	"testing"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// alwaysSucceedClient returns a fixed response for every model.
type alwaysSucceedClient struct{ response string }

func (c *alwaysSucceedClient) Complete(_ context.Context, _ string, opts ...CompletionOption) (string, error) {
	cfg := CompletionConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Model == "" {
		return "", errors.New("no model")
	}
	return c.response, nil
}

// alwaysFailClient always returns an error.
type alwaysFailClient struct{}

func (c *alwaysFailClient) Complete(_ context.Context, _ string, _ ...CompletionOption) (string, error) {
	return "", errors.New("client error")
}

// partialClient succeeds for the first N calls, fails after.
type partialClient struct {
	calls     int
	succeedTo int // succeed for dims with index < succeedTo
}

func (p *partialClient) Complete(_ context.Context, _ string, opts ...CompletionOption) (string, error) {
	cfg := CompletionConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	p.calls++
	if p.calls <= p.succeedTo {
		return "summary for " + cfg.Model, nil
	}
	return "", errors.New("partial failure")
}

// ─────────────────────────────────────────────────────────────────────────────
// SummarizeAll
// ─────────────────────────────────────────────────────────────────────────────

func TestSummarizeAll_AllSucceed(t *testing.T) {
	client := &alwaysSucceedClient{response: "ok"}
	s := NewSummarizer(client)

	results, err := s.SummarizeAll(context.Background(), "some conversation")
	if err != nil {
		t.Fatalf("SummarizeAll: unexpected error: %v", err)
	}
	all := appconfig.AllSummaryDimensions()
	if len(results) != len(all) {
		t.Fatalf("results = %d dimensions, want %d", len(results), len(all))
	}
	for _, dim := range all {
		if results[dim] == "" {
			t.Errorf("dimension %s: empty result", dim)
		}
	}
}

func TestSummarizeAll_TotalFailure(t *testing.T) {
	s := NewSummarizer(&alwaysFailClient{})
	_, err := s.SummarizeAll(context.Background(), "some conversation")
	if err == nil {
		t.Fatal("SummarizeAll with always-failing client: expected error, got nil")
	}
}

func TestSummarizeAll_PartialFailure_ReturnsPartialAndError(t *testing.T) {
	// Succeed only for the first 2 out of 6 dimensions.
	s := NewSummarizer(&partialClient{succeedTo: 2})
	results, err := s.SummarizeAll(context.Background(), "conv")
	if err == nil {
		t.Fatal("expected partial error, got nil")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 partial results, got %d", len(results))
	}
}

func TestSummarizeAll_NilSummarizer(t *testing.T) {
	var s *Summarizer
	_, err := s.SummarizeAll(context.Background(), "conv")
	if err == nil {
		t.Fatal("expected error from nil Summarizer, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BuildPrompt – all six dimensions
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildPrompt_AllDimensions(t *testing.T) {
	cases := []struct {
		dim     appconfig.SummaryDimension
		keyword string // expected prefix fragment
	}{
		{appconfig.SummaryDimensionProject, "项目上下文"},
		{appconfig.SummaryDimensionKeywords, "关键词"},
		{appconfig.SummaryDimensionTasks, "任务状态"},
		{appconfig.SummaryDimensionDecisions, "关键决策"},
		{appconfig.SummaryDimensionProblems, "问题与解决方案"},
		{appconfig.SummaryDimensionTechnical, "技术细节"},
	}
	for _, tc := range cases {
		prompt := BuildPrompt(tc.dim, "conversation text")
		if len(prompt) == 0 {
			t.Errorf("BuildPrompt(%s): empty prompt", tc.dim)
		}
		if !contains(prompt, tc.keyword) {
			t.Errorf("BuildPrompt(%s): expected keyword %q in prompt %q", tc.dim, tc.keyword, prompt[:min(80, len(prompt))])
		}
		if !contains(prompt, "conversation text") {
			t.Errorf("BuildPrompt(%s): conversation text missing from prompt", tc.dim)
		}
	}
}

func TestBuildPrompt_UnknownDimension(t *testing.T) {
	prompt := BuildPrompt("unknown_dim", "my convo")
	if len(prompt) == 0 {
		t.Fatal("BuildPrompt(unknown): empty prompt")
	}
	if !contains(prompt, "my convo") {
		t.Error("BuildPrompt(unknown): conversation text missing")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SystemPromptForDimension – all six dimensions
// ─────────────────────────────────────────────────────────────────────────────

func TestSystemPromptForDimension_AllDimensions(t *testing.T) {
	dims := appconfig.AllSummaryDimensions()
	for _, dim := range dims {
		sp := SystemPromptForDimension(dim)
		if sp == "" {
			t.Errorf("SystemPromptForDimension(%s): empty", dim)
		}
	}
}

func TestSystemPromptForDimension_Unknown(t *testing.T) {
	sp := SystemPromptForDimension("unknown")
	if sp == "" {
		t.Error("SystemPromptForDimension(unknown): expected fallback, got empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MaxTokensForDimension
// ─────────────────────────────────────────────────────────────────────────────

func TestMaxTokensForDimension_AllDimensions(t *testing.T) {
	cases := map[appconfig.SummaryDimension]int{
		appconfig.SummaryDimensionDecisions:  1024,
		appconfig.SummaryDimensionTechnical:  1024,
		appconfig.SummaryDimensionProject:    768,
		appconfig.SummaryDimensionTasks:      768,
		appconfig.SummaryDimensionProblems:   768,
		appconfig.SummaryDimensionKeywords:   256,
	}
	for dim, want := range cases {
		if got := MaxTokensForDimension(dim); got != want {
			t.Errorf("MaxTokensForDimension(%s) = %d, want %d", dim, got, want)
		}
	}
}

func TestMaxTokensForDimension_Unknown(t *testing.T) {
	got := MaxTokensForDimension("unknown")
	if got <= 0 {
		t.Errorf("MaxTokensForDimension(unknown) = %d, want > 0", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TemperatureForDimension
// ─────────────────────────────────────────────────────────────────────────────

func TestTemperatureForDimension_AllDimensions(t *testing.T) {
	cases := map[appconfig.SummaryDimension]float64{
		appconfig.SummaryDimensionKeywords:  0.0,
		appconfig.SummaryDimensionDecisions: 0.1,
		appconfig.SummaryDimensionTechnical: 0.1,
		appconfig.SummaryDimensionProject:   0.2,
		appconfig.SummaryDimensionTasks:     0.2,
		appconfig.SummaryDimensionProblems:  0.2,
	}
	for dim, want := range cases {
		if got := TemperatureForDimension(dim); got != want {
			t.Errorf("TemperatureForDimension(%s) = %v, want %v", dim, got, want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Summarize – nil client and empty output paths
// ─────────────────────────────────────────────────────────────────────────────

func TestSummarize_NilClient(t *testing.T) {
	s := NewSummarizer(nil)
	_, err := s.Summarize(context.Background(), appconfig.SummaryDimensionProject, "conv")
	if err == nil {
		t.Fatal("expected error from nil LLMClient, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
