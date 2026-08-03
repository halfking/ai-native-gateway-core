package compression

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	summarymodel "github.com/kaixuan/llm-gateway-go/domains/hooks/compression/summary"
)

type summaryClientAdapter struct {
	deps    *Dependencies
	profile string
}

func newSummaryClientAdapter(deps *Dependencies, profile string) summarymodel.LLMClient {
	return &summaryClientAdapter{deps: deps, profile: profile}
}

func (a *summaryClientAdapter) Complete(ctx context.Context, prompt string, opts ...summarymodel.CompletionOption) (string, error) {
	if a == nil || a.deps == nil || a.deps.Provider == nil || !a.deps.Provider.Enabled() {
		return "", errors.New("summary client unavailable")
	}

	cfg := summarymodel.CompletionConfig{MaxTokens: 512, Temperature: 0.2}
	for _, opt := range opts {
		opt(&cfg)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return "", errors.New("summary model empty")
	}

	candidates, err := a.deps.Provider.GetCandidates(ctx, cfg.Model, a.profile)
	if err != nil {
		return "", err
	}
	for i := range candidates {
		cand := candidates[i]
		if !cand.Available || cand.ContextWindow == nil || *cand.ContextWindow < defaultCompactionMinWindow {
			continue
		}
		out, callErr := completeSummaryCandidate(ctx, &cand, prompt, cfg)
		if callErr != nil {
			slog.Debug("summary: candidate failed", "model", cfg.Model, "raw_model", cand.RawModel, "error", callErr)
			continue
		}
		out = strings.TrimSpace(out)
		if out != "" {
			return out, nil
		}
	}
	return "", fmt.Errorf("summary: all candidates failed for model %s", cfg.Model)
}

func completeSummaryCandidate(ctx context.Context, cand *ProviderCandidate, prompt string, cfg summarymodel.CompletionConfig) (string, error) {
	if cand == nil {
		return "", errors.New("candidate nil")
	}
	model := strings.TrimSpace(cand.RawModel)
	if model == "" {
		model = strings.TrimSpace(cfg.Model)
	}
	if model == "" {
		return "", errors.New("candidate model empty")
	}
	cfg.Model = model
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 512
	}
	if strings.EqualFold(cand.Protocol, "anthropic-messages") {
		return invokeAnthropicCompletion(ctx, cand, prompt, cfg)
	}
	return invokeOpenAICompletion(ctx, cand, prompt, cfg)
}

func invokeOpenAICompletion(ctx context.Context, cand *ProviderCandidate, prompt string, cfg summarymodel.CompletionConfig) (string, error) {
	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": cfg.SystemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": prompt})
	payload, _ := json.Marshal(map[string]any{
		"model":       cfg.Model,
		"max_tokens":  cfg.MaxTokens,
		"temperature": cfg.Temperature,
		"stream":      false,
		"messages":    messages,
	})

	req, err := buildCompactionRequest(ctx, cand, payload, false)
	if err != nil {
		return "", err
	}
	resp, err := defaultHTTPDoer(req)
	if err != nil {
		return "", err
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxCompactionBody)))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("summary upstream %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", errors.New("summary upstream returned empty content")
	}
	return parsed.Choices[0].Message.Content, nil
}

func invokeAnthropicCompletion(ctx context.Context, cand *ProviderCandidate, prompt string, cfg summarymodel.CompletionConfig) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"model":       cfg.Model,
		"max_tokens":  cfg.MaxTokens,
		"temperature": cfg.Temperature,
		"system":      cfg.SystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})

	req, err := buildCompactionRequest(ctx, cand, payload, true)
	if err != nil {
		return "", err
	}
	resp, err := defaultHTTPDoer(req)
	if err != nil {
		return "", err
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxCompactionBody)))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("summary upstream %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	var out strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" && block.Text != "" {
			out.WriteString(block.Text)
		}
	}
	if out.Len() == 0 {
		return "", errors.New("summary upstream returned empty content")
	}
	return out.String(), nil
}
