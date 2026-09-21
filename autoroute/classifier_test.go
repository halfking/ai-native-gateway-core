package autoroute

import (
	"context"
	"encoding/json"
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
		ToolCount:      5,
		HasToolResults: true,
		LastUserPrompt: "find the file and replace the value",
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
		EstimatedTokens: 20_000, // below long_context threshold so code path runs
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Should still classify as code because last user has "function"
	if res.Primary != TaskCode {
		t.Fatalf("expected TaskCode, got %s", res.Primary)
	}
}

// ── 新增测试（需求 #1：编程任务不被长上下文截胡）──────────────────────

func TestHeuristicClassifier_LongContextCodingTask_ShouldBeCode(t *testing.T) {
	// RED: 当前实现会因为 tokens>50000 直接返回 long_context，
	// 根本不检查 code 关键词。修复后应该先检查 code。
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt:  "请帮我重构这段 Python 代码，并添加单元测试",
		EstimatedTokens: 60_000, // 超过 long_context threshold (50000)
		HasCodeBlock:    true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Errorf("expected TaskCode for long coding request, got %s (reason=%s)", res.Primary, res.Reason)
	}
	if res.Confidence < 0.85 {
		t.Errorf("expected high confidence for strong code signal, got %.2f", res.Confidence)
	}
}

func TestHeuristicClassifier_PlanModeCoding_ShouldBeCode(t *testing.T) {
	// RED: 计划模式 prompt 通常很长，容易被 long_context 截胡
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		SystemPrompt: "You are a coding assistant. Use plan mode: first create an implementation plan, then write code step by step.",
		LastUserPrompt: "实现一个完整的用户认证系统，包括注册、登录、JWT、权限管理。" +
			"先制定详细的实现计划，列出所有模块和接口，然后逐步实现每个模块。",
		EstimatedTokens: 8_000,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Errorf("expected TaskCode for plan mode coding, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_IDEClientFingerprint_Cursor(t *testing.T) {
	// RED: 当前没有 ClientType 字段，需要先加到 ClassificationSignals
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt:  "帮我分析这个函数的时间复杂度", // 没有明确 code 关键词
		EstimatedTokens: 3_000,
		ClientType:      "cursor", // IDE 指纹
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Errorf("expected TaskCode for Cursor IDE client, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_IDEClientFingerprint_ClaudeCode(t *testing.T) {
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "review my implementation and suggest improvements",
		ClientType:     "claude-code",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Errorf("expected TaskCode for claude-code IDE client, got %s", res.Primary)
	}
}

func TestHeuristicClassifier_StackTraceError_ShouldBeCode(t *testing.T) {
	// 用户粘贴错误堆栈，应该被识别为编程任务（新 pattern + code 关键词）
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: `遇到了这个错误：
Traceback (most recent call last):
  File "app.py", line 42, in process_data
    result = compute(x, y)
TypeError: unsupported operand type(s) for +: 'int' and 'str'

帮我修复这段代码`,
		EstimatedTokens: 1_200,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Errorf("expected TaskCode for stack trace error, got %s (reason: %s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_PureDocumentSummary_ShouldBeLongContext(t *testing.T) {
	// GREEN: 纯文档总结，没有编程信号，应该仍然是 long_context
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt:  "请总结这份会议纪要的要点",
		EstimatedTokens: 80_000,
		HasCodeBlock:    false,
		ClientType:      "", // 非 IDE
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskLongContext {
		t.Errorf("expected TaskLongContext for pure document summary, got %s", res.Primary)
	}
}

// ── 新增测试（V6：接入 code_audit / intent_classification / planning）────────

func TestHeuristicClassifier_CodeAudit(t *testing.T) {
	// 显式安全审计请求 → TaskCodeAudit（不再是死代码）
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "请对这段代码做安全审计，检查是否有漏洞和注入风险",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCodeAudit {
		t.Errorf("expected TaskCodeAudit for security audit, got %s (reason=%s)", res.Primary, res.Reason)
	}
	if res.Confidence < 0.8 {
		t.Errorf("expected high confidence for explicit audit, got %.2f", res.Confidence)
	}
}

func TestHeuristicClassifier_CodeAudit_OrdinaryReviewStaysCode(t *testing.T) {
	// 收窄后：普通 "review my code" 不应误判为 code_audit，仍是 TaskCode
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "please review my code and suggest improvements",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Errorf("expected TaskCode for ordinary review, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_IntentClassification(t *testing.T) {
	// 意图分类请求 → TaskIntentClassification（不再是死代码，也不再误判为 chat）
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "请对这条用户消息进行意图分类，判断是咨询、投诉还是建议",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskIntentClassification {
		t.Errorf("expected TaskIntentClassification, got %s (reason=%s)", res.Primary, res.Reason)
	}
	if res.Confidence < 0.8 {
		t.Errorf("expected high confidence, got %.2f", res.Confidence)
	}
}

func TestHeuristicClassifier_Planning_PurePlan(t *testing.T) {
	// 纯方案/任务拆解请求 → TaskPlanning（高智商任务，用 opus/sonnet）
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "帮我写一份微服务架构方案，拆解各模块任务和技术选型",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskPlanning {
		t.Errorf("expected TaskPlanning for pure plan, got %s (reason=%s)", res.Primary, res.Reason)
	}
	if res.Confidence < 0.8 {
		t.Errorf("expected high confidence, got %.2f", res.Confidence)
	}
}

func TestHeuristicClassifier_Planning_WithImplement_StillCode(t *testing.T) {
	// 不回归："先制定计划然后实现" 仍是 TaskCode（strongCodingSignal 优先于 planning）
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "实现一个用户认证系统，先制定详细的计划，然后逐步实现",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskCode {
		t.Errorf("expected TaskCode for plan-then-implement, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_Planning_EnglishDesignDoc(t *testing.T) {
	// English design doc / task breakdown → TaskPlanning
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "Write a technical design doc for the new billing system, break down the work into phases",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary != TaskPlanning {
		t.Errorf("expected TaskPlanning for design doc, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

func TestHeuristicClassifier_Intent_NotChat(t *testing.T) {
	// 收窄后：包含"判断"但非意图分类的普通对话，不应误判为 intent_classification
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	res, err := c.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "帮我判断一下这个决定对不对",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Primary == TaskIntentClassification {
		t.Errorf("ordinary '判断' must not trigger intent_classification, got %s (reason=%s)", res.Primary, res.Reason)
	}
}

// ── 中文编程任务 pattern 补丁（盲区修复）─────────────────────────────

func TestHeuristicClassifier_ZhCodingTask_AlgorithmName(t *testing.T) {
	// 盲区："写一个快速排序" 无"算法/函数"字面，关键词层不命中。
	// 修复：pattern 层匹配"动词 + 编程对象"。
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	cases := []string{
		"用 Python 写一个快速排序",
		"写一个红黑树的插入",
		"用 go 写一个 LRU 缓存",
		"写一个线程池",
	}
	for _, prompt := range cases {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: prompt})
		if err != nil {
			t.Fatalf("err on %q: %v", prompt, err)
		}
		if res.Primary != TaskCode {
			t.Errorf("expected TaskCode for %q, got %s (reason=%s)", prompt, res.Primary, res.Reason)
		}
	}
}

func TestHeuristicClassifier_ZhCodingTask_LanguageVerb(t *testing.T) {
	// 盲区："用 React 做表单组件""用 SQL 查询" 不命中关键词。
	// 修复：pattern 层匹配"语言/框架名 + 动作动词"。
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	cases := []string{
		"帮我用 React 做一个表单组件",
		"用 SQL 查询最近七天的订单",
		"用 Java 实现一个简单的区块链",
		"写个脚本批量重命名文件",
	}
	for _, prompt := range cases {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: prompt})
		if err != nil {
			t.Fatalf("err on %q: %v", prompt, err)
		}
		if res.Primary != TaskCode {
			t.Errorf("expected TaskCode for %q, got %s (reason=%s)", prompt, res.Primary, res.Reason)
		}
	}
}

func TestHeuristicClassifier_ZhCodingPattern_NoFalsePositive(t *testing.T) {
	// code pattern 补丁绝不能误伤 creative/planning：含"写一个"但对象是
	// 故事/博客/方案，必须保持原归类。
	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	cases := []struct {
		prompt string
		want   TaskType
	}{
		{"写一个关于太空探索的故事", TaskCreative},
		{"写一篇关于 AI 的博客文章", TaskCreative},
		{"帮我写一份微服务架构方案，拆解各模块任务", TaskPlanning},
	}
	for _, tc := range cases {
		res, err := c.Classify(context.Background(), ClassificationSignals{LastUserPrompt: tc.prompt})
		if err != nil {
			t.Fatalf("err on %q: %v", tc.prompt, err)
		}
		if res.Primary != tc.want {
			t.Errorf("expected %s for %q, got %s (reason=%s) — code pattern over-triggered",
				tc.want, tc.prompt, res.Primary, res.Reason)
		}
	}
}



// TestClassificationSignalsString verifies that String() method does not leak
// sensitive prompt content into logs.
func TestClassificationSignalsString(t *testing.T) {
	sensitivePrompt := "My credit card is 1234-5678-9012-3456"
	sensitiveSystem := "Internal company secret: project codename alpha"

	sigs := ClassificationSignals{
		SystemPrompt:    sensitiveSystem,
		LastUserPrompt:  sensitivePrompt,
		MessageCount:    5,
		EstimatedTokens: 2000,
		ToolCount:       2,
		HasImages:       true,
		Language:        "en",
		HasCodeBlock:    false,
		HasToolResults:  true,
		ClientType:      "cursor",
	}

	output := sigs.String()

	// Verify no sensitive content in output
	if strings.Contains(output, "credit card") {
		t.Error("String() output contains sensitive content: 'credit card'")
	}
	if strings.Contains(output, "1234-5678") {
		t.Error("String() output contains sensitive content: card number")
	}
	if strings.Contains(output, "company secret") {
		t.Error("String() output contains sensitive content: 'company secret'")
	}
	if strings.Contains(output, "codename") {
		t.Error("String() output contains sensitive content: 'codename'")
	}

	// Verify it contains length information instead
	if !strings.Contains(output, "SystemPrompt:") {
		t.Error("String() output should contain 'SystemPrompt:' label")
	}
	if !strings.Contains(output, "chars") {
		t.Error("String() output should contain 'chars' unit for lengths")
	}

	// Verify it contains non-sensitive metadata
	if !strings.Contains(output, "MessageCount:5") {
		t.Error("String() output should contain MessageCount")
	}
	if !strings.Contains(output, "ClientType:cursor") {
		t.Error("String() output should contain ClientType")
	}
}

// TestClassificationSignalsMarshalJSON verifies that MarshalJSON() does not
// leak sensitive prompt content into JSON logs or telemetry.
func TestClassificationSignalsMarshalJSON(t *testing.T) {
	sensitivePrompt := "Please help me hack into this system"
	sensitiveSystem := "You are a security expert with access to classified data"

	sigs := ClassificationSignals{
		SystemPrompt:    sensitiveSystem,
		LastUserPrompt:  sensitivePrompt,
		MessageCount:    3,
		EstimatedTokens: 1500,
		ToolCount:       1,
		HasImages:       false,
		Language:        "en",
		HasCodeBlock:    true,
		HasToolResults:  false,
		ClientType:      "vscode",
	}

	jsonBytes, err := json.Marshal(sigs)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	jsonStr := string(jsonBytes)

	// Verify no sensitive content in JSON output
	if strings.Contains(jsonStr, "hack") {
		t.Error("JSON output contains sensitive content: 'hack'")
	}
	if strings.Contains(jsonStr, "classified") {
		t.Error("JSON output contains sensitive content: 'classified'")
	}
	if strings.Contains(jsonStr, "security expert") {
		t.Error("JSON output contains sensitive content: 'security expert'")
	}

	// Verify it contains length fields instead
	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("Failed to parse JSON output: %v", err)
	}

	// Check expected fields exist
	expectedFields := []string{
		"system_prompt_len", "last_user_len", "message_count",
		"estimated_tokens", "tool_count", "has_images", "language",
		"has_code_block", "has_tool_results", "client_type",
	}
	for _, field := range expectedFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("JSON output missing expected field: %s", field)
		}
	}

	// Verify length values match
	if int(parsed["system_prompt_len"].(float64)) != len(sensitiveSystem) {
		t.Errorf("system_prompt_len=%v, want %d", parsed["system_prompt_len"], len(sensitiveSystem))
	}
	if int(parsed["last_user_len"].(float64)) != len(sensitivePrompt) {
		t.Errorf("last_user_len=%v, want %d", parsed["last_user_len"], len(sensitivePrompt))
	}

	// Verify metadata values preserved
	if int(parsed["message_count"].(float64)) != 3 {
		t.Errorf("message_count=%v, want 3", parsed["message_count"])
	}
	if parsed["client_type"].(string) != "vscode" {
		t.Errorf("client_type=%v, want vscode", parsed["client_type"])
	}
}

// TestClassificationSignalsNoContent verifies that both String() and
// MarshalJSON() work correctly with empty content.
func TestClassificationSignalsNoContent(t *testing.T) {
	sigs := ClassificationSignals{
		SystemPrompt:    "",
		LastUserPrompt:  "",
		MessageCount:    1,
		EstimatedTokens: 0,
		ToolCount:       0,
		HasImages:       false,
		Language:        "en",
		HasCodeBlock:    false,
		HasToolResults:  false,
		ClientType:      "",
	}

	// Test String()
	output := sigs.String()
	if !strings.Contains(output, "0 chars") {
		t.Error("String() should show '0 chars' for empty prompts")
	}

	// Test MarshalJSON()
	jsonBytes, err := json.Marshal(sigs)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("Failed to parse JSON output: %v", err)
	}

	if int(parsed["system_prompt_len"].(float64)) != 0 {
		t.Errorf("system_prompt_len should be 0, got %v", parsed["system_prompt_len"])
	}
	if int(parsed["last_user_len"].(float64)) != 0 {
		t.Errorf("last_user_len should be 0, got %v", parsed["last_user_len"])
	}
}
