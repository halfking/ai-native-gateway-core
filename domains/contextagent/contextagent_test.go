package contextagent

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

// ── 假实现 ────────────────────────────────────────────────────────────────

// fakeRows 是 Rows 的假实现（按行吐出预置的值）。
type fakeRows struct {
	rows [][]any
	i    int
	err  error
}

func (f *fakeRows) Next() bool {
	if f.i >= len(f.rows) {
		return false
	}
	f.i++
	return true
}

func (f *fakeRows) Scan(dest ...any) error {
	if f.i-1 >= len(f.rows) {
		return context.Canceled
	}
	row := f.rows[f.i-1]
	for i := range dest {
		if i >= len(row) {
			continue
		}
		switch d := dest[i].(type) {
		case *int:
			if v, ok := row[i].(int); ok {
				*d = v
			}
		case *[]byte:
			if v, ok := row[i].([]byte); ok {
				*d = v
			}
		case *string:
			if v, ok := row[i].(string); ok {
				*d = v
			}
		}
	}
	return nil
}

func (f *fakeRows) Err() error { return f.err }
func (f *fakeRows) Close()     {}

// fakeQueryer 按调用顺序吐出预置的行组，并记录每次执行的 SQL。
type fakeQueryer struct {
	groups []*fakeRows
	calls  int
	sqls   []string
}

func (f *fakeQueryer) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	f.sqls = append(f.sqls, sql)
	if f.calls >= len(f.groups) {
		return &fakeRows{}, nil
	}
	g := f.groups[f.calls]
	f.calls++
	return g, nil
}

// fakeStepLoader 是步骤明细加载器的假实现。
type fakeStepLoader struct {
	details map[int][]map[string]any
	err     error
}

func (f *fakeStepLoader) LoadSteps(_ context.Context, _ Identity, steps []int) ([]StepDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []StepDetail
	for _, s := range steps {
		msgs, ok := f.details[s]
		if !ok {
			continue
		}
		out = append(out, StepDetail{StepIndex: s, Messages: msgs, Tokens: EstimateTokens(messagesText(msgs))})
	}
	return out, nil
}

// fakeIndexLoader 是索引层加载器的假实现。
type fakeIndexLoader struct {
	entries []IndexEntry
	err     error
}

func (f *fakeIndexLoader) LoadIndex(_ context.Context, _ Identity) ([]IndexEntry, error) {
	return f.entries, f.err
}

// fakeLongTerm 是长期记忆加载器的假实现。
type fakeLongTerm struct {
	items []memory.Memory
	err   error
}

func (f *fakeLongTerm) Recall(_ context.Context, _, _ string, _ int) ([]memory.Memory, error) {
	return f.items, f.err
}

// ── 身份识别 ──────────────────────────────────────────────────────────────

func TestRecognize_ReadsProjectTaskSessionStep(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(HeaderProjectID, "proj-alpha")
	r.Header.Set(HeaderTaskID, "task-42")
	r.Header.Set(HeaderStepIndex, "7")
	r.Header.Set(HeaderStepID, "step-abc")
	r.Header.Set(HeaderClientType, "zcode")
	r.Header.Set("X-Session-Id", "sess-12345678")

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	id := Recognize(r, body, IdentityFacts{
		TenantID:        "tenant-1",
		APIKeyID:        9,
		ApplicationCode: "codex",
		RequestID:       "req-1",
		ClientProtocol:  "openai-chat",
	})

	if id.Project != "proj-alpha" {
		t.Fatalf("project: got %q", id.Project)
	}
	if id.Task != "task-42" {
		t.Fatalf("task: got %q", id.Task)
	}
	if id.StepIndex != 7 {
		t.Fatalf("step index: got %d", id.StepIndex)
	}
	if id.StepID != "step-abc" {
		t.Fatalf("step id: got %q", id.StepID)
	}
	if id.Session == "" {
		t.Fatal("session must be resolved")
	}
	if id.ClientType != "zcode" {
		t.Fatalf("client type: got %q", id.ClientType)
	}
	if id.TenantID != "tenant-1" || id.APIKeyID != 9 {
		t.Fatalf("tenant/key mismatch: %+v", id)
	}
}

func TestRecognize_ProjectFallsBackToApplicationCode(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	id := Recognize(r, []byte(`{"messages":[]}`), IdentityFacts{
		TenantID:        "t",
		ApplicationCode: "codex",
	})
	if id.Project != "app:codex" {
		t.Fatalf("project fallback: got %q want app:codex", id.Project)
	}
}

// TestRecognize_NamespaceMatchesWriteSideFormula —— 读侧命名空间必须与
// **写侧**（domains/streaming/executors 的三行公式）逐字节一致，
// 否则 Memora 召回恒空。
//
// ★ 审计轮把这条门从「手工构造 Identity 比字符串格式」改成
//
//	「拿真请求走真派生，再与写侧公式对拍」——
//	旧门只比格式，永远发现不了「头名用错 / 前缀漏了 / api_key_id 恒 0」
//	这三处发散。
func TestRecognize_NamespaceMatchesWriteSideFormula(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Task-Id", "task-42")
	const cred = "sk-write-side-credential"
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	id := Recognize(r, body, IdentityFacts{
		TenantID:        "tenant-1",
		ModelCredential: cred,
	})

	// 写侧口径：apiKeyID = hash(凭据明文)；taskID = memory.TaskID；
	// userID = memory.UserID(tenant, apiKeyID, taskID)。
	writeAPIKeyID := memory.APIKeyIDFromToken(cred)
	writeTaskID := memory.TaskID(r, body, writeAPIKeyID)
	writeUserID := memory.UserID("tenant-1", writeAPIKeyID, writeTaskID)

	if writeUserID == "" {
		t.Fatal("write-side namespace must be derivable in this scenario")
	}
	if got := id.Namespace(); got != writeUserID {
		t.Fatalf("namespace diverges from write side: got %q want %q", got, writeUserID)
	}
	// 逐段对拍（防止有人「恰好相等」但分段不同）。
	want := "t:tenant-1:k:" + strconv.Itoa(writeAPIKeyID) + ":task-42"
	if got := id.Namespace(); got != want {
		t.Fatalf("namespace shape: got %q want %q", got, want)
	}
	if id.MemoryTaskID != writeTaskID {
		t.Fatalf("memory task id: got %q want %q", id.MemoryTaskID, writeTaskID)
	}
	if id.MemoryAPIKeyID != writeAPIKeyID {
		t.Fatalf("memory api key id: got %d want %d", id.MemoryAPIKeyID, writeAPIKeyID)
	}
}

// TestRecognize_NoModelCredentialMeansNoNamespace —— 没有模型面凭据时
// 命名空间必须**为空**（fail-closed），而不是拿「猜出来的」分量去查。
//
// ★ 旧实现会用 APIKeyID(恒 0) + X-Gw-Task-Id 拼一个看起来合法的串，
//
//	结果必然 0 命中，却让读数像「记忆是空的」。
func TestRecognize_NoModelCredentialMeansNoNamespace(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(HeaderTaskID, "task-42")
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	id := Recognize(r, body, IdentityFacts{TenantID: "tenant-1"})
	if got := id.Namespace(); got != "" {
		t.Fatalf("namespace must be empty without a model credential, got %q", got)
	}
	if id.MemoryAPIKeyID != 0 {
		t.Fatalf("memory api key id must stay 0, got %d", id.MemoryAPIKeyID)
	}
}

// TestRecognize_NamespaceUsesWriteSideTaskVocabulary —— 任务分量必须走
// **写侧词表**（X-Task-Id 原值 / X-Session-Id → "s:" 前缀）。
func TestRecognize_NamespaceUsesWriteSideTaskVocabulary(t *testing.T) {
	const cred = "sk-vocab-credential"

	// X-Task-Id ⇒ 原值，无前缀。
	r1, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r1.Header.Set("X-Task-Id", "T-42")
	id1 := Recognize(r1, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), IdentityFacts{
		TenantID: "acme", ModelCredential: cred,
	})
	if !strings.HasSuffix(id1.Namespace(), ":T-42") {
		t.Fatalf("X-Task-Id must map to the raw value: %q", id1.Namespace())
	}

	// X-Session-Id ⇒ "s:" 前缀。
	r2, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r2.Header.Set("X-Session-Id", "S-7")
	id2 := Recognize(r2, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), IdentityFacts{
		TenantID: "acme", ModelCredential: cred,
	})
	if !strings.HasSuffix(id2.Namespace(), ":s:S-7") {
		t.Fatalf("X-Session-Id must map to the s: vocabulary: %q", id2.Namespace())
	}
}

// ── 计划 ──────────────────────────────────────────────────────────────────

func TestMakePlan_ProtectsTailAndIndex(t *testing.T) {
	plan := MakePlan(PlanInput{
		ContextWindow:  128_000,
		CurrentTokens:  1_000,
		SearchQuery:    "查询",
		AvailableSteps: []int{1, 2, 3},
		StepTokens:     map[int]int{1: 100, 2: 100, 3: 100},
		IndexTokens:    500,
	})
	if plan.BudgetFor(LayerTail) <= 0 {
		t.Fatal("tail must get a budget")
	}
	if plan.BudgetFor(LayerIndex) <= 0 {
		t.Fatal("index must get a budget")
	}
	if plan.BudgetFor(LayerIndex) < 500 {
		t.Fatalf("index budget too small: %d", plan.BudgetFor(LayerIndex))
	}
	if len(plan.LoadSteps) != 3 {
		t.Fatalf("expected 3 steps loaded, got %v", plan.LoadSteps)
	}
	total := plan.BudgetFor(LayerTail) + plan.BudgetFor(LayerIndex) + plan.BudgetFor(LayerStepDetail) + plan.BudgetFor(LayerLongTerm)
	if total > plan.Budget {
		t.Fatalf("allocation exceeds budget: %d > %d (%s)", total, plan.Budget, plan.Describe())
	}
}

// TestMakePlan_SkipsStepsThatDoNotFit —— 装不下的步骤必须留给「按需加载」，
// 不能把预算吃穿。
func TestMakePlan_SkipsStepsThatDoNotFit(t *testing.T) {
	plan := MakePlan(PlanInput{
		ContextWindow:  32_768,
		CurrentTokens:  0,
		AvailableSteps: []int{1, 2},
		StepTokens:     map[int]int{1: 5_000_000, 2: 10},
		IndexTokens:    100,
	})
	for _, s := range plan.LoadSteps {
		if s == 1 {
			t.Fatal("an oversized step must not be eagerly loaded")
		}
	}
	if len(plan.LoadSteps) != 1 || plan.LoadSteps[0] != 2 {
		t.Fatalf("expected only step 2 loaded, got %v", plan.LoadSteps)
	}
}

// ── 装配 ──────────────────────────────────────────────────────────────────

func buildChatBody(t *testing.T, msgs []map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"model":    "gpt-x",
		"stream":   true,
		"messages": msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestAssemble_IndexIsPlacedAtTheTop —— 用户的核心要求：执行摘要索引在**最上层**。
func TestAssemble_IndexIsPlacedAtTheTop(t *testing.T) {
	body := buildChatBody(t, []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "first user"},
		{"role": "assistant", "content": "a1"},
		{"role": "user", "content": "u2"},
	})
	out, ok := Assemble(body, []LayerContent{{
		Layer: LayerIndex,
		Text:  RenderIndex([]IndexEntry{{StepIndex: 1, Goal: "改鉴权重试", Result: "已在 auth/client.go 落地"}}),
	}})
	if !ok {
		t.Fatal("assemble returned false")
	}
	var probe struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.Messages) < 3 {
		t.Fatalf("too few messages: %d", len(probe.Messages))
	}
	if role := roleOf(t, probe.Messages[0]); role != "system" {
		t.Fatalf("messages[0] must stay the system message, got %q", role)
	}
	idxMsg := string(probe.Messages[1])
	if !strings.Contains(idxMsg, IndexHeader) {
		t.Fatalf("messages[1] must be the index block, got %s", idxMsg)
	}
	if !strings.Contains(idxMsg, "auth/client.go") {
		t.Fatal("index must keep the exact identifier")
	}
	// 首条 user（B-track）必须被钉住。
	if !strings.Contains(string(probe.Messages[2]), "first user") {
		t.Fatalf("the first user message must be pinned, got %s", probe.Messages[2])
	}
	// 其它顶层键必须原样保留。
	if probe.Model != "gpt-x" || !probe.Stream {
		t.Fatalf("top-level keys must be preserved: %+v", probe)
	}
}

// TestAssemble_KeepsRecentTail —— 「注意力放在最近的任务」⇒ 尾部不能被压掉。
func TestAssemble_KeepsRecentTail(t *testing.T) {
	body := buildChatBody(t, []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "first user"},
		{"role": "assistant", "content": "a1"},
		{"role": "user", "content": "the most recent question"},
	})
	out, ok := Assemble(body, []LayerContent{{Layer: LayerIndex, Text: IndexHeader}})
	if !ok {
		t.Fatal("assemble returned false")
	}
	if !strings.Contains(string(out), "the most recent question") {
		t.Fatal("the most recent user message must survive assembly")
	}
}

// roleOf 取一条消息的角色（测试辅助）。
func roleOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	return probe.Role
}

// ── 记忆工具：识别 ────────────────────────────────────────────────────────

func TestDetectMemoryToolCalls_OpenAI(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
		{"id":"c1","type":"function","function":{"name":"load_step_detail","arguments":"{\"step_index\":7}"}},
		{"id":"c2","type":"function","function":{"name":"some_other_tool","arguments":"{}"}}
	]}}]}`)
	calls := DetectMemoryToolCalls(body, "openai-chat")
	if len(calls) != 1 {
		t.Fatalf("expected only the memory tool to be detected, got %d", len(calls))
	}
	if calls[0].Name != ToolLoadStepDetail || calls[0].ID != "c1" {
		t.Fatalf("unexpected call: %+v", calls[0])
	}
}

func TestDetectMemoryToolCalls_Anthropic(t *testing.T) {
	body := []byte(`{"content":[{"type":"tool_use","id":"tu1","name":"search_memory","input":{"query":"登录超时"}}]}`)
	calls := DetectMemoryToolCalls(body, "anthropic-messages")
	if len(calls) != 1 || calls[0].Name != ToolSearchMemory {
		t.Fatalf("unexpected calls: %+v", calls)
	}
}

// TestDetectMemoryToolCalls_IgnoresNonMemoryTools —— 非记忆工具必须原样透传给
// 客户端，网关不能吞掉。
func TestDetectMemoryToolCalls_IgnoresNonMemoryTools(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[
		{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}
	]}}]}`)
	if calls := DetectMemoryToolCalls(body, "openai-chat"); len(calls) != 0 {
		t.Fatalf("non-memory tools must not be captured, got %+v", calls)
	}
}

// ── 记忆工具：执行 ────────────────────────────────────────────────────────

func TestServeMemoryTool_LoadStepDetail(t *testing.T) {
	loader := &fakeStepLoader{details: map[int][]map[string]any{
		7: {{"role": "user", "content": "goal"}, {"role": "assistant", "content": "detail of step 7"}},
	}}
	res := ServeMemoryTool(context.Background(), ToolDeps{
		Identity:   Identity{Session: "s1"},
		StepLoader: loader,
	}, ToolCall{ID: "c1", Name: ToolLoadStepDetail, Arguments: json.RawMessage(`{"step_index":7}`)})

	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "detail of step 7") {
		t.Fatalf("content missing the step detail: %q", res.Content)
	}
	if len(res.Loaded) != 1 || res.Loaded[0] != "step:7" {
		t.Fatalf("provenance missing: %+v", res.Loaded)
	}
}

func TestServeMemoryTool_LoadStepDetailNotFound(t *testing.T) {
	res := ServeMemoryTool(context.Background(), ToolDeps{
		Identity:   Identity{Session: "s1"},
		StepLoader: &fakeStepLoader{},
	}, ToolCall{ID: "c1", Name: ToolLoadStepDetail, Arguments: json.RawMessage(`{"step_index":99}`)})
	if res.Error == "" {
		t.Fatal("a missing step must be reported as an error, not silently empty")
	}
	if !strings.Contains(res.Content, "99") {
		t.Fatalf("the model must be told which step was missing: %q", res.Content)
	}
}

func TestServeMemoryTool_SearchMemory(t *testing.T) {
	lt := &fakeLongTerm{items: []memory.Memory{
		{ID: "m1", Text: "鉴权重试最多 5 次"},
		{ID: "m2", Text: "探针窗口对齐 60s"},
	}}
	res := ServeMemoryTool(context.Background(), ToolDeps{
		Identity: Identity{
			TenantID:        "t",
			MemoryNamespace: "t:t:k:1:tk",
			MemoryTaskID:    "tk",
			MemoryAPIKeyID:  1,
		},
		LongTerm: lt,
	}, ToolCall{ID: "c2", Name: ToolSearchMemory, Arguments: json.RawMessage(`{"query":"鉴权","top_k":2}`)})

	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "鉴权重试最多 5 次") {
		t.Fatalf("content missing recalled memory: %q", res.Content)
	}
	if len(res.Loaded) != 2 {
		t.Fatalf("expected 2 provenance entries, got %+v", res.Loaded)
	}
}

func TestServeMemoryTool_SearchMemoryWithoutNamespace(t *testing.T) {
	res := ServeMemoryTool(context.Background(), ToolDeps{
		Identity: Identity{},
		LongTerm: &fakeLongTerm{},
	}, ToolCall{ID: "c2", Name: ToolSearchMemory, Arguments: json.RawMessage(`{"query":"x"}`)})
	if res.Error == "" {
		t.Fatal("missing namespace must be an explicit error (never a silent empty result)")
	}
}

// ── 工具声明注入 ──────────────────────────────────────────────────────────

func TestInjectToolSpecs_IsIdempotent(t *testing.T) {
	body := buildChatBody(t, []map[string]any{{"role": "user", "content": "hi"}})
	out, ok := InjectToolSpecs(body, "openai-chat")
	if !ok {
		t.Fatal("first injection must succeed")
	}
	again, ok := InjectToolSpecs(out, "openai-chat")
	if ok {
		t.Fatal("second injection must report no change")
	}
	if string(again) != string(out) {
		t.Fatal("second injection must not change the body")
	}
	var probe struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.Tools) != 2 {
		t.Fatalf("expected 2 memory tools, got %d", len(probe.Tools))
	}
}

// ── 工具结果回填 ──────────────────────────────────────────────────────────

func TestAppendToolResults_OpenAI(t *testing.T) {
	reqBody := buildChatBody(t, []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "hi"},
	})
	respBody := []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
		{"id":"c1","type":"function","function":{"name":"load_step_detail","arguments":"{\"step_index\":3}"}}
	]}}]}`)
	results := []ToolResult{{ToolCallID: "c1", Name: ToolLoadStepDetail, Content: "the detail"}}

	out, ok := AppendToolResults(reqBody, respBody, results, "openai-chat")
	if !ok {
		t.Fatal("append failed")
	}
	var probe struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.Messages) != 4 {
		t.Fatalf("expected system+user+assistant+tool, got %d", len(probe.Messages))
	}
	if probe.Messages[2]["role"] != "assistant" {
		t.Fatalf("messages[2] must be the assistant tool-call message: %+v", probe.Messages[2])
	}
	if probe.Messages[3]["role"] != "tool" || probe.Messages[3]["tool_call_id"] != "c1" {
		t.Fatalf("messages[3] must be the tool result: %+v", probe.Messages[3])
	}
	if probe.Messages[3]["content"] != "the detail" {
		t.Fatalf("tool result content mismatch: %+v", probe.Messages[3])
	}
}

// ── 智能体主流程 ──────────────────────────────────────────────────────────

// TestAgentProcess_NoMemoryToolCall_Passthrough —— 没有记忆工具调用时，
// 网关不得改变响应（语义等价于透明代理）。
func TestAgentProcess_NoMemoryToolCall_Passthrough(t *testing.T) {
	clientBody := buildChatBody(t, []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "hi"},
	})
	upstream := []byte(`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)

	var submitted [][]byte
	agent := &Agent{
		Submitter: SubmitterFunc(func(_ context.Context, body []byte) ([]byte, error) {
			submitted = append(submitted, append([]byte(nil), body...))
			return upstream, nil
		}),
		Recorder: NewMemoryRecorder(),
	}

	out, err := agent.Process(context.Background(), Request{
		RequestID: "req-1",
		Identity:  Identity{Session: "s1"},
		Body:      clientBody,
		Protocol:  "openai-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Rounds != 1 {
		t.Fatalf("expected 1 round, got %d", out.Rounds)
	}
	if string(out.ResponseBody) != string(upstream) {
		t.Fatal("response must be passed through unchanged")
	}
	if len(out.ServedToolCalls) != 0 {
		t.Fatalf("no tool should have been served, got %+v", out.ServedToolCalls)
	}
	if out.Record == nil || string(out.Record.RestoreClientRequest()) != string(clientBody) {
		t.Fatal("the original client request must be recorded byte-exactly")
	}
	if string(out.Record.RestoreUpstreamResponse()) != string(upstream) {
		t.Fatal("the original upstream response must be recorded byte-exactly")
	}
}

// TestAgentProcess_MemoryToolCall_Resubmits —— 核心流程：
// 客户端请求 → 提交 LLM → LLM 发出记忆工具调用 → 网关自己加载记忆 →
// 重新提交 → 客户端拿到的是**不含记忆工具调用**的最终回答。
func TestAgentProcess_MemoryToolCall_Resubmits(t *testing.T) {
	clientBody := buildChatBody(t, []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "步7 的细节是什么"},
	})
	toolResp := []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
		{"id":"c1","type":"function","function":{"name":"load_step_detail","arguments":"{\"step_index\":7}"}}
	]}}]}`)
	finalResp := []byte(`{"choices":[{"message":{"role":"assistant","content":"步7 改了 internal/auth/client.go"}}]}`)

	loader := &fakeStepLoader{details: map[int][]map[string]any{
		7: {{"role": "assistant", "content": "step 7 body: internal/auth/client.go"}},
	}}

	var submitted [][]byte
	agent := &Agent{
		StepLoader: loader,
		Submitter: SubmitterFunc(func(_ context.Context, body []byte) ([]byte, error) {
			submitted = append(submitted, append([]byte(nil), body...))
			if len(submitted) == 1 {
				return toolResp, nil
			}
			return finalResp, nil
		}),
		Recorder:    NewMemoryRecorder(),
		InjectTools: true,
	}

	out, err := agent.Process(context.Background(), Request{
		RequestID: "req-2",
		Identity:  Identity{Session: "s1", Task: "t1"},
		Body:      clientBody,
		Protocol:  "openai-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Rounds != 2 {
		t.Fatalf("expected 2 rounds, got %d", out.Rounds)
	}
	if len(out.ServedToolCalls) != 1 {
		t.Fatalf("expected 1 served tool call, got %+v", out.ServedToolCalls)
	}
	if out.ServedToolCalls[0].Name != ToolLoadStepDetail {
		t.Fatalf("unexpected served call: %+v", out.ServedToolCalls[0])
	}
	// 客户端拿到的最终响应里**没有**记忆工具调用。
	if strings.Contains(string(out.ResponseBody), ToolLoadStepDetail) {
		t.Fatalf("the final response must not contain a memory tool call: %s", out.ResponseBody)
	}
	if !strings.Contains(string(out.ResponseBody), "internal/auth/client.go") {
		t.Fatalf("the final response must carry the answer: %s", out.ResponseBody)
	}
	// 第二次提交的请求体里必须带上了工具结果。
	if len(submitted) != 2 {
		t.Fatalf("expected 2 submissions, got %d", len(submitted))
	}
	if !strings.Contains(string(submitted[1]), "step 7 body") {
		t.Fatalf("the resubmitted body must carry the tool result: %s", submitted[1])
	}
	// 记录里必须有原始请求与原始响应。
	if string(out.Record.RestoreClientRequest()) != string(clientBody) {
		t.Fatal("original client request must be byte-exact")
	}
	if string(out.Record.RestoreUpstreamResponse()) != string(toolResp) {
		t.Fatal("original upstream response must be byte-exact")
	}
	if string(out.Record.RestoreFinalResponse()) != string(finalResp) {
		t.Fatal("final response must be byte-exact")
	}
}

// TestAgentProcess_ToolLoopIsBounded —— 回环必须有上界，不能无限问下去。
func TestAgentProcess_ToolLoopIsBounded(t *testing.T) {
	clientBody := buildChatBody(t, []map[string]any{{"role": "user", "content": "hi"}})
	toolResp := []byte(`{"choices":[{"message":{"tool_calls":[
		{"id":"c1","type":"function","function":{"name":"search_memory","arguments":"{\"query\":\"x\"}"}}
	]}}]}`)

	calls := 0
	agent := &Agent{
		LongTerm:  &fakeLongTerm{},
		MaxRounds: 3,
		Submitter: SubmitterFunc(func(_ context.Context, _ []byte) ([]byte, error) {
			calls++
			return toolResp, nil
		}),
	}
	out, err := agent.Process(context.Background(), Request{
		RequestID: "req-3",
		Identity:  Identity{Session: "s1", Task: "t1"},
		Body:      clientBody,
		Protocol:  "openai-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("the loop must stop at MaxRounds, got %d submissions", calls)
	}
	if out.Rounds != 3 {
		t.Fatalf("expected 3 rounds, got %d", out.Rounds)
	}
}

// TestAgentProcess_LoadFailureDoesNotBlock —— 记忆加载失败**不得**阻断请求。
func TestAgentProcess_LoadFailureDoesNotBlock(t *testing.T) {
	clientBody := buildChatBody(t, []map[string]any{{"role": "user", "content": "hi"}})
	upstream := []byte(`{"choices":[{"message":{"content":"ok"}}]}`)

	agent := &Agent{
		IndexLoader: &fakeIndexLoader{err: context.DeadlineExceeded},
		Submitter: SubmitterFunc(func(_ context.Context, _ []byte) ([]byte, error) {
			return upstream, nil
		}),
	}
	out, err := agent.Process(context.Background(), Request{
		RequestID: "req-4",
		Identity:  Identity{Session: "s1"},
		Body:      clientBody,
		Protocol:  "openai-chat",
	})
	if err != nil {
		t.Fatalf("a loader failure must not fail the request: %v", err)
	}
	if string(out.ResponseBody) != string(upstream) {
		t.Fatal("response must still be returned")
	}
}

// ── PG 加载器（用假 Queryer 验证 SQL 与拼装） ─────────────────────────────

func TestPGStepDetailLoader_DedupsAndOrders(t *testing.T) {
	reqDelta := []byte(`[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"}]`)
	respDelta := []byte(`[{"role":"assistant","content":"a1"},{"role":"assistant","content":"a2"}]`)
	q := &fakeQueryer{groups: []*fakeRows{{rows: [][]any{
		{1, reqDelta, respDelta},
	}}}}

	loader := NewPGStepDetailLoader(q)
	details, err := loader.LoadSteps(context.Background(), Identity{TenantID: "t", Session: "s1"}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	msgs := details[0].Messages
	if len(msgs) != 3 {
		t.Fatalf("the duplicated assistant message must be removed: %+v", msgs)
	}
	if msgTextOf(msgs[0]) != "q1" || msgTextOf(msgs[2]) != "a2" {
		t.Fatalf("unexpected message order/content: %+v", msgs)
	}
}

// msgTextOf 取消息文本（测试辅助）。
func msgTextOf(m map[string]any) string {
	s, _ := m["content"].(string)
	return s
}

func TestPGIndexLoader_ReadsRows(t *testing.T) {
	q := &fakeQueryer{groups: []*fakeRows{{rows: [][]any{
		{1, "goal one", "result one"},
		{2, "goal two", "result two"},
	}}}}
	loader := NewPGIndexLoader(q)
	entries, err := loader.LoadIndex(context.Background(), Identity{TenantID: "t", Session: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].StepIndex != 1 || entries[1].Goal != "goal two" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

// ── 记录与还原 ────────────────────────────────────────────────────────────

// TestFileRecorder_RoundTripIsByteExact —— 「原样还原」的核心不变量。
func TestFileRecorder_RoundTripIsByteExact(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewFileRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"model":"m","messages":[{"role":"user","content":"中文原文"}],"x":1}`)
	origResp := []byte(`{"choices":[{"message":{"content":"答复"}}]}`)
	final := []byte(`{"choices":[{"message":{"content":"最终答复"}}]}`)

	in := &Record{
		RequestID:            "req-abc",
		OriginalRequestBody:  original,
		OriginalResponseBody: origResp,
		FinalResponseBody:    final,
	}
	if err := rec.Save(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	out, err := rec.Load(context.Background(), "req-abc")
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("record must be loadable")
	}
	if string(out.RestoreClientRequest()) != string(original) {
		t.Fatalf("client request not restored byte-exactly:\n got %s\nwant %s", out.RestoreClientRequest(), original)
	}
	if string(out.RestoreUpstreamResponse()) != string(origResp) {
		t.Fatal("upstream response not restored byte-exactly")
	}
	if string(out.RestoreFinalResponse()) != string(final) {
		t.Fatal("final response not restored byte-exactly")
	}
}

// TestFileRecorder_LoadMissingReturnsNil —— 不存在的记录不是错误。
func TestFileRecorder_LoadMissingReturnsNil(t *testing.T) {
	rec, err := NewFileRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := rec.Load(context.Background(), "nope")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("missing record must return (nil, nil)")
	}
}

// TestSafeFileName_RejectsPathTraversal —— request_id 来自外部，
// 不能让它穿越目录。
func TestSafeFileName_RejectsPathTraversal(t *testing.T) {
	got := safeFileName("../../etc/passwd")
	if strings.Contains(got, "/") || strings.Contains(got, "..") {
		t.Fatalf("unsafe file name: %q", got)
	}
}
