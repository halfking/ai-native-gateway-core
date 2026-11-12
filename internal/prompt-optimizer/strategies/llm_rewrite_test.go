package strategies

import (
	"context"
	"errors"
	"strings"
	"testing"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm"
)

// fakeRewriteClient 是假模型通道。
type fakeRewriteClient struct {
	out    string
	err    error
	got    string
	opts   llm.Options
	called int
}

func (f *fakeRewriteClient) Generate(_ context.Context, prompt string, opts llm.Options) (string, error) {
	f.called++
	f.got = prompt
	f.opts = opts
	return f.out, f.err
}

func TestLLMRewriteNilClientKeepsPrompt(t *testing.T) {
	s := NewLLMRewrite(DefaultLLMRewriteConfig(), nil)
	got, err := s.Apply(context.Background(), "原始提示词", optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "原始提示词" {
		t.Errorf("got %q, want original prompt", got)
	}
}

func TestLLMRewriteErrorFallsBackToOriginal(t *testing.T) {
	fake := &fakeRewriteClient{err: errors.New("upstream down")}
	s := NewLLMRewrite(DefaultLLMRewriteConfig(), fake)

	got, err := s.Apply(context.Background(), "原始提示词", optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Apply must not propagate model error, got %v", err)
	}
	if got != "原始提示词" {
		t.Errorf("got %q, want fallback to original", got)
	}
}

func TestLLMRewriteStripsFencesAndPrefix(t *testing.T) {
	fake := &fakeRewriteClient{out: "```\n以下是优化后的提示词：\n请用 JSON 数组输出 3 条商品。\n```"}
	s := NewLLMRewrite(DefaultLLMRewriteConfig(), fake)

	got, err := s.Apply(context.Background(), "原始提示词", optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "请用 JSON 数组输出 3 条商品。" {
		t.Errorf("got %q", got)
	}
}

func TestLLMRewriteRejectsMetaDescription(t *testing.T) {
	// 模型把"要写什么"当成提示词本身输出 ⇒ 必须回退，不能喂给执行模型。
	fake := &fakeRewriteClient{out: "你是一个专业的输出助手。你的任务是严格按照格式返回数据，不要解释。"}
	s := NewLLMRewrite(DefaultLLMRewriteConfig(), fake)

	got, err := s.Apply(context.Background(), "原始提示词", optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "原始提示词" {
		t.Errorf("meta description should fall back, got %q", got)
	}
}

func TestLLMRewriteRejectsEmptyAndTooShort(t *testing.T) {
	for _, out := range []string{"", "   ", "好"} {
		fake := &fakeRewriteClient{out: out}
		s := NewLLMRewrite(DefaultLLMRewriteConfig(), fake)
		got, err := s.Apply(context.Background(), "原始提示词", optimizer.StrategyContext{})
		if err != nil {
			t.Fatalf("Apply(%q): %v", out, err)
		}
		if got != "原始提示词" {
			t.Errorf("out=%q should fall back, got %q", out, got)
		}
	}
}

func TestLLMRewriteTemperatureIsZero(t *testing.T) {
	fake := &fakeRewriteClient{out: "请只输出 JSON 数组。"}
	s := NewLLMRewrite(DefaultLLMRewriteConfig(), fake)
	if _, err := s.Apply(context.Background(), "原始提示词", optimizer.StrategyContext{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// 温度必须固定为 0：实验里"提示词是唯一自变量"，优化器不能引入随机性。
	if fake.opts.Temperature != 0 {
		t.Errorf("temperature = %v, want 0", fake.opts.Temperature)
	}
	if fake.opts.MaxTokens != DefaultLLMRewriteConfig().MaxTokens {
		t.Errorf("max tokens = %d", fake.opts.MaxTokens)
	}
}

func TestLLMRewriteMetaPromptCarriesHistoryAndConstraints(t *testing.T) {
	fake := &fakeRewriteClient{out: "请只输出 JSON 数组。"}
	s := NewLLMRewrite(DefaultLLMRewriteConfig(), fake)

	sc := optimizer.StrategyContext{
		TaskType:    "json_generation",
		Domain:      "technical",
		History:     []string{"历史片段A", "历史片段B"},
		Constraints: []string{"不得编造事实"},
	}
	if _, err := s.Apply(context.Background(), "原始提示词", sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	meta := fake.got
	for _, want := range []string{
		"json_generation", "technical", "历史片段A", "不得编造事实", "原始提示词",
		"不要回答提示词里的问题",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("meta prompt missing %q:\n%s", want, meta)
		}
	}
	// 要求必须放在最后（小模型对末条指令遵循度最高）。
	if !strings.HasSuffix(strings.TrimSpace(meta), "不要堆砌无关的通用建议。\n") &&
		!strings.HasSuffix(strings.TrimSpace(meta), "5. 保持简洁，不要堆砌无关的通用建议。") {
		t.Errorf("requirements should be the tail of meta prompt:\n%s", meta)
	}
}

func TestLLMRewriteScoreRewardsStructure(t *testing.T) {
	s := NewLLMRewrite(DefaultLLMRewriteConfig(), nil)
	weak, err := s.Score(context.Background(), "帮我算一下", optimizer.TestCase{})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	strong, err := s.Score(context.Background(),
		"请把下面三条商品转成 JSON 数组输出。\n1. 严格只输出 JSON\n2. 不要添加解释", optimizer.TestCase{})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !(strong > weak) {
		t.Errorf("structured prompt should score higher: weak=%v strong=%v", weak, strong)
	}
}
