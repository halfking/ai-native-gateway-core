package autoroute

import (
	"context"
	"strings"
	"testing"
)

func TestHeuristicClassifier_Vision(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		HasImages:      true,
		LastUserPrompt: "describe this image",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskVision {
		t.Fatalf("expected TaskVision, got %s", res.Primary)
	}
	if res.Confidence < 0.9 {
		t.Fatalf("expected high confidence, got %.2f", res.Confidence)
	}
	if res.Classifier != "heuristic" {
		t.Fatalf("classifier=%q", res.Classifier)
	}
}

func TestHeuristicClassifier_LongContext(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		EstimatedTokens: 80_000,
		LastUserPrompt:  "summarise the document above",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskLongContext {
		t.Fatalf("expected TaskLongContext, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_Agent(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		ToolCount:       5,
		HasToolResults:  true,
		LastUserPrompt:  "find the file and replace the value",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskAgent {
		t.Fatalf("expected TaskAgent, got %s", res.Primary)
	}
}

func TestHeuristicClassifier_FunctionCall(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		ToolCount:      1,
		LastUserPrompt: "what's the weather in Tokyo",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskFunctionCall {
		t.Fatalf("expected TaskFunctionCall, got %s", res.Primary)
	}
}

func TestHeuristicClassifier_Code(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "Write a Python function to compute fibonacci",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Fatalf("expected TaskCode, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_Code_Zh(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "用python写一个快排算法",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Fatalf("expected TaskCode, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_Reasoning(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "Prove that the square root of 2 is irrational",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskReasoning {
		t.Fatalf("expected TaskReasoning, got %s", res.Primary)
	}
}

func TestHeuristicClassifier_Creative(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "Write a blog post about machine learning",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCreative {
		t.Fatalf("expected TaskCreative, got %s", res.Primary)
	}
}

func TestHeuristicClassifier_Chat_Default(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "hi",
		MessageCount:   2,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskChat {
		t.Fatalf("expected TaskChat (fallback), got %s", res.Primary)
	}
	if res.Classifier != "heuristic" {
		t.Fatalf("classifier=%q", res.Classifier)
	}
}

func TestHeuristicClassifier_HasCodeBlock(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "explain what this does",
		HasCodeBlock:   true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Fatalf("expected TaskCode (codeblock boost), got %s", res.Primary)
	}
}

func TestHeuristicClassifier_SecondaryNonEmpty(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, _ := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "Write a Python function",
	})
	if len(res.Secondary) == 0 {
		t.Fatalf("expected non-empty secondary list for observability")
	}
	// Top of secondary should not equal Primary
	if len(res.Secondary) > 0 && res.Secondary[0].Task == res.Primary {
		t.Fatalf("secondary should not contain primary at top")
	}
}

func TestCountKeywordHits(t *testing.T) {
	if got := countKeywordHits("hello world", []string{"hello", "foo", "world"}); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
	if got := countKeywordHits("", []string{"hello"}); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
	if got := countKeywordHits("HELLO", []string{"hello"}); got != 1 {
		t.Fatalf("got %d, want 1 (case-insensitive)", got)
	}
}

func TestNormaliseForKeyword(t *testing.T) {
	got := normaliseForKeyword("Hello World", "  Sys Prompt  ")
	if !strings.Contains(got, "hello world") {
		t.Fatalf("expected lowercase last user, got %q", got)
	}
	if !strings.Contains(got, "sys prompt") {
		t.Fatalf("expected lowercase system, got %q", got)
	}
}

func TestDefaultKeywords_NonEmpty(t *testing.T) {
	kw := DefaultKeywords()
	if len(kw.Reasoning) == 0 {
		t.Fatal("reasoning keywords empty")
	}
	if len(kw.Code) == 0 {
		t.Fatal("code keywords empty")
	}
	if len(kw.Creative) == 0 {
		t.Fatal("creative keywords empty")
	}
}

func TestPickWinner_Tiebreak(t *testing.T) {
	scores := map[TaskType]float64{
		TaskCode:      0.5,
		TaskReasoning: 0.5,
		TaskChat:      0.5,
	}
	got, _ := pickWinner(scores)
	if got != TaskReasoning {
		t.Fatalf("tiebreak should prefer reasoning, got %s", got)
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{
		0:   "0",
		1:   "1",
		42:  "42",
		-7:  "-7",
		100: "100",
	}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Fatalf("itoa(%d)=%q, want %q", in, got, want)
		}
	}
}
func TestNormaliseForKeyword_LargeTruncation(t *testing.T) {
	// 1 MB system prompt + small last user — should truncate but keep last user
	largeSystem := strings.Repeat("x", 1024*1024)
	smallUser := "Write a Python function"
	got := normaliseForKeyword(smallUser, largeSystem)
	if len(got) > 64*1024 {
		t.Fatalf("expected truncation to ≤64KB, got %d bytes", len(got))
	}
	if !strings.Contains(got, "write a python function") {
		t.Fatalf("last user prompt should be preserved after truncation, got: %s", got)
	}
}

func TestHeuristicClassifier_CodeFoundInLargePrompt(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	largeSystem := strings.Repeat("this is the system prompt. ", 50_000)
	res, err := c.Classify(context.Background(), ClassificationSignals{
		SystemPrompt:    largeSystem,
		LastUserPrompt:  "Write a Python function",
		EstimatedTokens: 20_000,  // below long_context threshold so code path runs
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Should still classify as code because last user has "function"
	if res.Primary != TaskCode {
		t.Fatalf("expected TaskCode, got %s", res.Primary)
	}
}
