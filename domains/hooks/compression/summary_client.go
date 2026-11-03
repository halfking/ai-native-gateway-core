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
	deps     *Dependencies
	profile  string
	tenantID string
}

func newSummaryClientAdapter(deps *Dependencies, profile, tenantID string) summarymodel.LLMClient {
	return &summaryClientAdapter{deps: deps, profile: profile, tenantID: tenantID}
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

	candidates, err := getSummaryCandidates(ctx, a.deps.Provider, cfg.Model, a.profile, a.tenantID)
	if err != nil {
		// ★ F-1（2026-11-04）：候选解析失败（含模态门拒绝：provider 过滤不了
		// 模态、或该模型在 text 模态下没有候选）⇒ 摘要降级（上层跳过摘要或退
		// 机械裁剪）。这里必须留一条可观测日志，不得静默——否则压缩质量降级
		// 无从归因（与下方 T12 的"error swallowed"是同一教训）。
		slog.Warn("summary: resolve candidates failed",
			"model", cfg.Model,
			"tenant_id", a.tenantID,
			"error", err,
		)
		return "", err
	}
	// T13 (audit 2026-08-27 §6.4): 累积所有候选错误，把首个 wrap 进 final error
	// 而不是丢给固定字符串"all candidates failed"，让上层能分辨失败原因。
	var firstErr error
	failedCount := 0
	for i := range candidates {
		cand := candidates[i]
		if !cand.Available {
			continue
		}
		if cand.ContextWindow != nil && *cand.ContextWindow < defaultCompactionMinWindow {
			continue
		}
		out, callErr := completeSummaryCandidate(ctx, &cand, prompt, cfg)
		if callErr == nil {
			out, callErr = guardGeneratedText(ctx, out)
		}
		if callErr != nil {
			// T12 (audit 2026-08-27 §6.4): 原 slog.Debug 4,588次/日 error swallowed
			// → 提到 WARN 让日志检索/告警触达；同时记录失败累计便于诊断。
			failedCount++
			if firstErr == nil {
				firstErr = callErr
			}
			slog.Warn("summary: candidate failed",
				"model", cfg.Model,
				"raw_model", cand.RawModel,
				"error", callErr,
				"failed_count", failedCount,
				"total_candidates", len(candidates),
			)
			continue
		}
		out = strings.TrimSpace(out)
		if out != "" {
			return out, nil
		}
	}
	if firstErr != nil {
		return "", fmt.Errorf("summary: all %d candidates failed for model %s: first error: %w", failedCount, cfg.Model, firstErr)
	}
	return "", fmt.Errorf("summary: all candidates failed for model %s", cfg.Model)
}

// getSummaryCandidates resolves the candidate set for one summary model.
//
// ★ 2026-11-04（F-1，40bbe7a54 同族第二处）：摘要请求是**纯文本的 chat 形态
// 调用** ⇒ 候选解析固定走 text 模态门。此前这里走无模态过滤的
// GetCandidates/GetCandidatesForTenant ⇒ 登记为 embedding 的绑定会被发 chat
// 形状请求 ⇒ 上游 400 ⇒ 摘要静默失败。tenantID 继续透传（租户路由策略仍生效，
// 只是现在从 GetCandidatesByModality 的参数走）。
func getSummaryCandidates(ctx context.Context, provider ProviderClient, model, profile, tenantID string) ([]ProviderCandidate, error) {
	return resolveCandidatesByModality(ctx, provider, model, profile, tenantID, modalityText)
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
