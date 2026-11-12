package context

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// stubReader 是 memory.Reader 的测试替身。
type stubReader struct {
	disabled  bool
	hits      []memory.Memory
	err       error
	lastTopK  int
	lastUser  string
	lastQuery string
}

func (s *stubReader) Disabled() bool { return s.disabled }

func (s *stubReader) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	s.lastUser = userID
	s.lastQuery = query
	s.lastTopK = topK
	if s.err != nil {
		return nil, s.err
	}
	return s.hits, nil
}

func (s *stubReader) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	return s.Search(ctx, userID, query, topK)
}

// TestManagerNilReaderDegradesGracefully 钉住无记忆后端时优雅降级而非报错。
func TestManagerNilReaderDegradesGracefully(t *testing.T) {
	m := NewManager(nil, DefaultConfig())

	history, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{TaskType: "qa"})
	if err != nil {
		t.Fatalf("无 reader 时不应报错: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("无 reader 时历史应为空，实际 %d 条", len(history))
	}
}

// TestManagerDisabledReaderDegradesGracefully 钉住后端被禁用时降级。
func TestManagerDisabledReaderDegradesGracefully(t *testing.T) {
	m := NewManager(&stubReader{disabled: true}, DefaultConfig())

	history, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{TaskType: "qa"})
	if err != nil {
		t.Fatalf("reader disabled 时不应报错: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("reader disabled 时历史应为空，实际 %d 条", len(history))
	}
}

// TestManagerPropagatesSearchError 钉住后端失败时错误可见（区分"查不到"与"查失败"）。
func TestManagerPropagatesSearchError(t *testing.T) {
	boom := errors.New("backend down")
	m := NewManager(&stubReader{err: boom}, DefaultConfig())

	_, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{TaskType: "qa"})
	if err == nil {
		t.Fatal("后端失败必须报错")
	}
	if !errors.Is(err, boom) {
		t.Errorf("错误应可用 errors.Is 检出: %v", err)
	}
}

// TestManagerFiltersByThreshold 钉住相似度阈值过滤生效。
func TestManagerFiltersByThreshold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SimilarityThreshold = 0.5

	m := NewManager(&stubReader{
		hits: []memory.Memory{
			{Text: "高相关", Score: 0.9},
			{Text: "低相关", Score: 0.1},
			{Text: "刚过线", Score: 0.6},
		},
	}, cfg)

	history, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{TaskType: "qa"})
	if err != nil {
		t.Fatalf("GetRelevantHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("阈值 0.5 应保留 2 条，实际 %d 条: %v", len(history), history)
	}
	for _, h := range history {
		if h == "低相关" {
			t.Errorf("低分记忆应被过滤: %v", history)
		}
	}
}

// TestManagerSortsByScore 钉住高分优先。
func TestManagerSortsByScore(t *testing.T) {
	m := NewManager(&stubReader{
		hits: []memory.Memory{
			{Text: "低", Score: 0.2},
			{Text: "高", Score: 0.9},
			{Text: "中", Score: 0.5},
		},
	}, DefaultConfig())

	history, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{TaskType: "qa"})
	if err != nil {
		t.Fatalf("GetRelevantHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("应返回 3 条，实际 %d", len(history))
	}
	if history[0] != "高" {
		t.Errorf("应按分数降序，首条应为\"高\"，实际 %q", history[0])
	}
}

// TestManagerRespectsTokenBudget 钉住 token 预算真的在裁剪（不是摆设）。
func TestManagerRespectsTokenBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxContextTokens = 20 // 只够放一小段

	m := NewManager(&stubReader{
		hits: []memory.Memory{
			{Text: strings.Repeat("甲", 100), Score: 0.9},
			{Text: strings.Repeat("乙", 100), Score: 0.8},
		},
	}, cfg)

	history, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{TaskType: "qa"})
	if err != nil {
		t.Fatalf("GetRelevantHistory: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("超预算的片段应被裁掉，实际保留 %d 条: %v", len(history), history)
	}
}

// TestManagerUsesConfiguredUserID 钉住按配置的 userID 检索（记忆按用户隔离）。
func TestManagerUsesConfiguredUserID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UserID = "user-42"

	reader := &stubReader{hits: []memory.Memory{{Text: "x", Score: 1}}}
	m := NewManager(reader, cfg)

	if _, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{TaskType: "qa"}); err != nil {
		t.Fatalf("GetRelevantHistory: %v", err)
	}
	if reader.lastUser != "user-42" {
		t.Errorf("应使用配置的 UserID，实际 %q", reader.lastUser)
	}
}

// TestManagerQueryIncludesTaskType 钉住检索 query 带任务类型。
func TestManagerQueryIncludesTaskType(t *testing.T) {
	reader := &stubReader{hits: []memory.Memory{{Text: "x", Score: 1}}}
	m := NewManager(reader, DefaultConfig())

	if _, err := m.GetRelevantHistory(context.Background(), &optimizer.Intent{
		TaskType: "translation",
		Domain:   "technical",
	}); err != nil {
		t.Fatalf("GetRelevantHistory: %v", err)
	}
	if !strings.Contains(reader.lastQuery, "translation") {
		t.Errorf("query 应含任务类型，实际 %q", reader.lastQuery)
	}
	if !strings.Contains(reader.lastQuery, "technical") {
		t.Errorf("query 应含领域，实际 %q", reader.lastQuery)
	}
}

// TestDomainConstraintsAreDomainSpecific 钉住领域约束差异化。
func TestDomainConstraintsAreDomainSpecific(t *testing.T) {
	medical := DomainConstraints("medical")
	legal := DomainConstraints("legal")

	if len(medical) == 0 || len(legal) == 0 {
		t.Fatal("领域约束不应为空")
	}
	if strings.Join(medical, "|") == strings.Join(legal, "|") {
		t.Error("不同领域的约束应不同")
	}
	if !strings.Contains(strings.Join(medical, "|"), "医疗") &&
		!strings.Contains(strings.Join(medical, "|"), "诊断") {
		t.Errorf("医疗约束应含医疗语义: %v", medical)
	}
}

// TestWindowEnforcesBudget 钉住窗口预算。
func TestWindowEnforcesBudget(t *testing.T) {
	w := NewWindow(10)

	if !w.Add("short") {
		t.Fatal("短文本应放得下")
	}
	if w.Add(strings.Repeat("x", 1000)) {
		t.Fatal("超预算文本应被拒绝")
	}
	if w.Used() > w.MaxTokens() {
		t.Fatalf("已用 %d 超过上限 %d", w.Used(), w.MaxTokens())
	}
}

// TestWindowEmptyTextFree 钉住空文本不消耗预算。
func TestWindowEmptyTextFree(t *testing.T) {
	w := NewWindow(10)
	if !w.Add("") {
		t.Fatal("空文本应被接受")
	}
	if w.Used() != 0 {
		t.Fatalf("空文本不应消耗预算，实际用了 %d", w.Used())
	}
}

// TestEstimateTokensHandlesChinese 钉住中文 token 估算不低估。
func TestEstimateTokensHandlesChinese(t *testing.T) {
	chinese := strings.Repeat("中", 100)
	english := strings.Repeat("a", 100)

	if EstimateTokens(chinese) < 100 {
		t.Errorf("中文估算应 ≥ 字符数，实际 %d", EstimateTokens(chinese))
	}
	if EstimateTokens(english) > 40 {
		t.Errorf("英文估算应远小于字符数，实际 %d", EstimateTokens(english))
	}
	if EstimateTokens("") != 0 {
		t.Error("空串应为 0 token")
	}
}

// TestTruncateKeepsWithinBudget 钉住截断后仍在预算内。
func TestTruncate(t *testing.T) {
	text := strings.Repeat("中文内容", 200)
	got := Truncate(text, 20)

	if got == text {
		t.Fatal("超预算文本应被截断")
	}
	if EstimateTokens(got) > 20 {
		t.Errorf("截断后 token 数 %d 超过预算 20", EstimateTokens(got))
	}
	if !strings.HasSuffix(got, "...\n") {
		t.Errorf("截断应带省略标记，实际结尾 %q", got)
	}
}
