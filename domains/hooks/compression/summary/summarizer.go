package summary

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
)

// LLMClient defines the small completion surface the summarizer needs.
type LLMClient interface {
	Complete(ctx context.Context, prompt string, opts ...CompletionOption) (string, error)
}

// CompletionOption applies one completion parameter to a CompletionConfig.
type CompletionOption func(*CompletionConfig)

// CompletionConfig captures the options passed to the backing LLM client.
type CompletionConfig struct {
	Model        string
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
}

// WithModel sets the request model.
func WithModel(model string) CompletionOption {
	return func(cfg *CompletionConfig) { cfg.Model = model }
}

// WithMaxTokens sets the response token budget.
func WithMaxTokens(maxTokens int) CompletionOption {
	return func(cfg *CompletionConfig) { cfg.MaxTokens = maxTokens }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(temperature float64) CompletionOption {
	return func(cfg *CompletionConfig) { cfg.Temperature = temperature }
}

// WithSystemPrompt sets the system prompt.
func WithSystemPrompt(systemPrompt string) CompletionOption {
	return func(cfg *CompletionConfig) { cfg.SystemPrompt = systemPrompt }
}

// Summarizer orchestrates multi-dimensional summaries and model fallback.
type Summarizer struct {
	client LLMClient
}

// NewSummarizer builds a summarizer around an LLM client.
func NewSummarizer(client LLMClient) *Summarizer {
	return &Summarizer{client: client}
}

// Summarize generates a summary for one dimension, trying every configured
// model in order until one succeeds.
func (s *Summarizer) Summarize(ctx context.Context, dim appconfig.SummaryDimension, conversation string) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("summary: llm client unavailable")
	}
	cfg := ResolveModelConfig(dim)
	if len(cfg.Models) == 0 {
		return "", errors.New("summary: no models configured")
	}

	prompt := BuildPrompt(dim, conversation)
	for _, model := range cfg.Models {
		out, err := s.client.Complete(ctx, prompt,
			WithModel(model),
			WithMaxTokens(MaxTokensForDimension(dim)),
			WithTemperature(TemperatureForDimension(dim)),
			WithSystemPrompt(SystemPromptForDimension(dim)),
		)
		if err != nil {
			continue
		}
		out = strings.TrimSpace(out)
		if out != "" {
			return out, nil
		}
	}
	return "", fmt.Errorf("summary: all models failed for %s (%s)", dim, cfg.Source)
}

// SummarizeAll generates the six canonical summary dimensions in stable order.
func (s *Summarizer) SummarizeAll(ctx context.Context, conversation string) (map[appconfig.SummaryDimension]string, error) {
	if s == nil {
		return nil, errors.New("summary: summarizer unavailable")
	}
	results := make(map[appconfig.SummaryDimension]string, len(appconfig.AllSummaryDimensions()))
	var errs []string
	for _, dim := range appconfig.AllSummaryDimensions() {
		text, err := s.Summarize(ctx, dim, conversation)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", dim, err))
			continue
		}
		results[dim] = text
	}
	if len(results) == 0 && len(errs) > 0 {
		sort.Strings(errs)
		return nil, errors.New(strings.Join(errs, "; "))
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return results, fmt.Errorf("partial summary: %s", strings.Join(errs, "; "))
	}
	return results, nil
}

// DimensionForTaskType maps a task type to the most relevant summary
// dimension. The mapping is intentionally conservative so existing request
// flows remain stable while still allowing per-dimension model selection.
func DimensionForTaskType(taskType string) appconfig.SummaryDimension {
	switch {
	case strings.HasPrefix(taskType, "code_"):
		return appconfig.SummaryDimensionTechnical
	case strings.HasPrefix(taskType, "data_"):
		return appconfig.SummaryDimensionTechnical
	case taskType == "deployment" || taskType == "devops" || taskType == "infra":
		return appconfig.SummaryDimensionTasks
	default:
		return appconfig.SummaryDimensionProject
	}
}

// BuildPrompt constructs the user prompt for one summary dimension.
func BuildPrompt(dim appconfig.SummaryDimension, conversation string) string {
	switch dim {
	case appconfig.SummaryDimensionProject:
		return "请总结以下对话中的项目上下文：目标、范围、技术栈、关键约束、当前进展。\n\n" + conversation
	case appconfig.SummaryDimensionKeywords:
		return "请从以下对话中提取关键词，输出精炼短语列表，避免重复。\n\n" + conversation
	case appconfig.SummaryDimensionTasks:
		return "请总结以下对话中的任务状态：已完成、进行中、待办、阻塞项、下一步。\n\n" + conversation
	case appconfig.SummaryDimensionDecisions:
		return "请总结以下对话中的关键决策：结论、备选方案、选择理由、负责人。\n\n" + conversation
	case appconfig.SummaryDimensionProblems:
		return "请总结以下对话中的问题与解决方案：症状、根因、修复方式、未解决项。\n\n" + conversation
	case appconfig.SummaryDimensionTechnical:
		return "请总结以下对话中的技术细节：文件路径、API、命令、配置、代码片段、数值。\n\n" + conversation
	default:
		return "请总结以下对话内容。\n\n" + conversation
	}
}

// SystemPromptForDimension returns a concise system prompt for one summary
// dimension.
func SystemPromptForDimension(dim appconfig.SummaryDimension) string {
	switch dim {
	case appconfig.SummaryDimensionProject:
		return "你是项目上下文总结器，重点保留项目目标、范围和背景。"
	case appconfig.SummaryDimensionKeywords:
		return "你是关键词提取器，输出高信息密度的关键词。"
	case appconfig.SummaryDimensionTasks:
		return "你是任务追踪总结器，重点保留状态、阻塞项和下一步。"
	case appconfig.SummaryDimensionDecisions:
		return "你是决策记录总结器，重点保留决策、理由和影响。"
	case appconfig.SummaryDimensionProblems:
		return "你是问题总结器，重点保留问题、根因和修复结果。"
	case appconfig.SummaryDimensionTechnical:
		return "你是技术细节总结器，重点保留代码、命令、配置和精确数值。"
	default:
		return "你是一个专业的对话总结器。"
	}
}

// MaxTokensForDimension returns the token budget for one summary dimension.
func MaxTokensForDimension(dim appconfig.SummaryDimension) int {
	switch dim {
	case appconfig.SummaryDimensionDecisions, appconfig.SummaryDimensionTechnical:
		return 1024
	case appconfig.SummaryDimensionProject, appconfig.SummaryDimensionTasks, appconfig.SummaryDimensionProblems:
		return 768
	case appconfig.SummaryDimensionKeywords:
		return 256
	default:
		return 512
	}
}

// TemperatureForDimension returns the sampling temperature for one summary
// dimension.
func TemperatureForDimension(dim appconfig.SummaryDimension) float64 {
	switch dim {
	case appconfig.SummaryDimensionKeywords:
		return 0.0
	case appconfig.SummaryDimensionDecisions, appconfig.SummaryDimensionTechnical:
		return 0.1
	default:
		return 0.2
	}
}
