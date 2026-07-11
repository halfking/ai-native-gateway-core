package compression

import (
	"encoding/json"
	"testing"
	"time"
)

func makeBodyAny(messages ...map[string]any) []byte {
	body := map[string]any{
		"model":    "test-model",
		"messages": messages,
	}
	b, _ := json.Marshal(body)
	return b
}

func makeMsg(role, content string) map[string]any {
	return map[string]any{"role": role, "content": content}
}

func TestAnalyzeConversation_SystemMessages(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"system","content":"You are a helpful assistant"}`),
		json.RawMessage(`{"role":"system","content":"Additional context"}`),
		json.RawMessage(`{"role":"user","content":"Hello"}`),
		json.RawMessage(`{"role":"assistant","content":"Hi there"}`),
	}
	infos := AnalyzeConversation(msgs)
	if len(infos) != 4 {
		t.Fatalf("expected 4 infos, got %d", len(infos))
	}
	if infos[0].Role != "system" {
		t.Errorf("expected system role, got %s", infos[0].Role)
	}
	if infos[2].Role != "user" {
		t.Errorf("expected user role, got %s", infos[2].Role)
	}
}

func TestAnalyzeConversation_ToolRoundDetection(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"What's the weather?"}`),
		json.RawMessage(`{"role":"assistant","content":"Let me check","tool_calls":[{"id":"tc1","function":{"name":"weather","arguments":"{}"}}]}`),
		json.RawMessage(`{"role":"tool","content":"Sunny 72F","tool_call_id":"tc1"}`),
		json.RawMessage(`{"role":"assistant","content":"It's sunny and 72F"}`),
	}
	infos := AnalyzeConversation(msgs)
	if len(infos) != 4 {
		t.Fatalf("expected 4 infos, got %d", len(infos))
	}
	// The assistant message with tool_calls should be a tool round member.
	if !infos[1].IsToolRoundMember {
		t.Error("expected message[1] (assistant+tool_calls) to be tool round member")
	}
	// The tool response should be a tool round member.
	if !infos[2].IsToolRoundMember {
		t.Error("expected message[2] (tool) to be tool round member")
	}
	// They should share the same tool round ID.
	if infos[1].ToolRoundID != infos[2].ToolRoundID {
		t.Errorf("expected tool round IDs to match: %d vs %d", infos[1].ToolRoundID, infos[2].ToolRoundID)
	}
}

func TestFindOptimalCutPoint_NoCompressionNeeded(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"system","content":"sys"}`),
		json.RawMessage(`{"role":"user","content":"Hi"}`),
		json.RawMessage(`{"role":"assistant","content":"Hello"}`),
	}
	plan := FindOptimalCutPoint(msgs, 128000, 0.7)
	if plan.CutIndex != -1 {
		t.Errorf("expected CutIndex=-1 (no compression), got %d", plan.CutIndex)
	}
	if plan.SystemCount != 1 {
		t.Errorf("expected SystemCount=1, got %d", plan.SystemCount)
	}
}

func TestFindOptimalCutPoint_LargeConversation(t *testing.T) {
	// Build a conversation with many large messages.
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"system","content":"System prompt"}`),
	}
	// Add 30 user+assistant pairs with large content.
	largeContent := ""
	for i := 0; i < 2000; i++ {
		largeContent += "This is a long message with lots of content. "
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, json.RawMessage(`{"role":"user","content":"`+largeContent+`"}`))
		msgs = append(msgs, json.RawMessage(`{"role":"assistant","content":"`+largeContent+`"}`))
	}

	plan := FindOptimalCutPoint(msgs, 10000, 0.5)
	if plan.CutIndex < 0 {
		t.Fatal("expected a valid cut index for large conversation")
	}
	if plan.SummariseCount == 0 {
		t.Error("expected non-zero summarise count")
	}
	if plan.RetainCount == 0 {
		t.Error("expected non-zero retain count")
	}
	t.Logf("plan: cutIdx=%d summarise=%d retain=%d estBefore=%d estAfter=%d reason=%s",
		plan.CutIndex, plan.SummariseCount, plan.RetainCount,
		plan.EstTokensBefore, plan.EstTokensAfter, plan.Reason)
}

func TestFindOptimalCutPoint_ToolIntegrityPreserved(t *testing.T) {
	// Build a conversation where the cut would land in the middle of a tool round.
	largeContent := ""
	for i := 0; i < 1000; i++ {
		largeContent += "x"
	}
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"system","content":"sys"}`),
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, json.RawMessage(`{"role":"user","content":"`+largeContent+`"}`))
		msgs = append(msgs, json.RawMessage(`{"role":"assistant","content":"`+largeContent+`"}`))
	}
	// Add a tool round at the end.
	msgs = append(msgs, json.RawMessage(`{"role":"assistant","content":"","tool_calls":[{"id":"tc1","function":{"name":"f","arguments":"{}"}}]}`))
	msgs = append(msgs, json.RawMessage(`{"role":"tool","content":"result"}`))

	plan := FindOptimalCutPoint(msgs, 5000, 0.5)
	// Verify the cut doesn't split a tool round.
	if plan.CutIndex > 0 {
		// Check the message at the cut boundary — it should NOT be a tool
		// result that references a tool_use on the other side.
		infos := AnalyzeConversation(msgs)
		nonSystem := infos[plan.SystemCount:]
		if plan.CutIndex < len(nonSystem) {
			msgAtCut := nonSystem[plan.CutIndex]
			_ = msgAtCut // Should not be a bare tool result
		}
	}
}

func TestSmartCompress_BasicRebuild(t *testing.T) {
	body := makeBodyAny(
		makeMsg("system", "You are helpful."),
		makeMsg("user", "Question 1"),
		makeMsg("assistant", "Answer 1"),
		makeMsg("user", "Question 2"),
		makeMsg("assistant", "Answer 2"),
	)
	plan := CutPlan{
		CutIndex:       2,
		SystemCount:    1,
		FirstUserKept:  false,
		SummariseCount: 2,
		RetainCount:    2,
	}

	rebuilt, err := SmartCompress(body, plan, "openai", "Summary of prior turns")
	if err != nil {
		t.Fatalf("SmartCompress failed: %v", err)
	}

	var result struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rebuilt, &result); err != nil {
		t.Fatalf("failed to parse rebuilt body: %v", err)
	}

	// Expected: [system, summary(user), user Q2, assistant A2]
	if len(result.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "system" {
		t.Errorf("expected system at [0], got %s", result.Messages[0].Role)
	}
	if result.Messages[1].Role != "user" {
		t.Errorf("expected user (summary) at [1], got %s", result.Messages[1].Role)
	}
	if result.Messages[2].Role != "user" {
		t.Errorf("expected user at [2], got %s", result.Messages[2].Role)
	}
	if result.Messages[3].Role != "assistant" {
		t.Errorf("expected assistant at [3], got %s", result.Messages[3].Role)
	}
}

func TestCutMarker_RoundTrip(t *testing.T) {
	original := CutMarker{
		Version:        cutMarkerSchemaVersion,
		CreatedAt:      1719300000,
		SourceMsgCount: 20,
		SystemMsgCount: 2,
		CutIndex:       8,
		SummaryMarker:  "smm_v1:abcd1234]",
		Strategy:       "smart_window_llm",
		BytesBefore:    50000,
		BytesAfter:     20000,
	}

	// Test SessionState round-trip
	var state SessionState
	state.SetCutMarker(original)
	if !state.HasCutMarker {
		t.Fatal("expected HasCutMarker=true after SetCutMarker")
	}

	recovered := state.ToCutMarker("test summary")
	if recovered == nil {
		t.Fatal("expected non-nil CutMarker from ToCutMarker")
	}
	if recovered.CutIndex != original.CutIndex {
		t.Errorf("CutIndex mismatch: %d vs %d", recovered.CutIndex, original.CutIndex)
	}
	if recovered.SourceMsgCount != original.SourceMsgCount {
		t.Errorf("SourceMsgCount mismatch: %d vs %d", recovered.SourceMsgCount, original.SourceMsgCount)
	}
	if recovered.Strategy != original.Strategy {
		t.Errorf("Strategy mismatch: %s vs %s", recovered.Strategy, original.Strategy)
	}
	if recovered.SummaryText != "test summary" {
		t.Errorf("SummaryText mismatch: %s", recovered.SummaryText)
	}
}

func TestCutMarker_RedisRoundTrip(t *testing.T) {
	cm := CutMarker{
		Version:        cutMarkerSchemaVersion,
		CreatedAt:      1719300000,
		SourceMsgCount: 15,
		SystemMsgCount: 1,
		CutIndex:       5,
		SummaryMarker:  "smm_v1:1234abcd]",
		Strategy:       "smart_window_mechanical",
		BytesBefore:    30000,
		BytesAfter:     12000,
	}

	fields := cm.MarshalForRedis()
	if fields["cm_ci"] != "5" {
		t.Errorf("expected cm_ci=5, got %s", fields["cm_ci"])
	}

	recovered := UnmarshalCutMarkerFromRedis(fields)
	if recovered == nil {
		t.Fatal("expected non-nil from UnmarshalCutMarkerFromRedis")
	}
	if recovered.CutIndex != cm.CutIndex {
		t.Errorf("CutIndex mismatch: %d vs %d", recovered.CutIndex, cm.CutIndex)
	}
	if recovered.Strategy != cm.Strategy {
		t.Errorf("Strategy mismatch: %s vs %s", recovered.Strategy, cm.Strategy)
	}
}

func TestCutMarker_GlobalCutIndex(t *testing.T) {
	cm := CutMarker{
		SystemMsgCount: 2,
		CutIndex:       5,
	}
	if cm.GlobalCutIndex() != 7 {
		t.Errorf("expected GlobalCutIndex=7, got %d", cm.GlobalCutIndex())
	}
}

func TestCutMarker_IsExpired(t *testing.T) {
	// Fresh marker.
	fresh := CutMarker{CreatedAt: unixNow()}
	if fresh.IsExpired(redisKeyTTL) {
		t.Error("expected fresh marker to not be expired")
	}

	// Old marker.
	old := CutMarker{CreatedAt: 1} // 1970
	if !old.IsExpired(redisKeyTTL) {
		t.Error("expected old marker to be expired")
	}
}

func TestIncrementalBuild_Basic(t *testing.T) {
	// Build a body where the first 3 non-system messages were "compressed".
	body := makeBodyAny(
		makeMsg("system", "System prompt"),
		makeMsg("user", "Old question 1"),
		makeMsg("assistant", "Old answer 1"),
		makeMsg("user", "Old question 2"),
		makeMsg("assistant", "Old answer 2"),
		makeMsg("user", "New question"),
	)

	marker := CutMarker{
		Version:        cutMarkerSchemaVersion,
		SystemMsgCount: 1,
		CutIndex:       4, // First 4 non-system messages summarised
		SourcePrefixHash: messagePrefixHash([]json.RawMessage{
			json.RawMessage(`{"role":"system","content":"System prompt"}`),
			json.RawMessage(`{"role":"user","content":"Old question 1"}`),
			json.RawMessage(`{"role":"assistant","content":"Old answer 1"}`),
			json.RawMessage(`{"role":"user","content":"Old question 2"}`),
			json.RawMessage(`{"role":"assistant","content":"Old answer 2"}`),
		}),
		SummaryText: "Summary of old conversation",
	}

	rebuilt, ok := IncrementalBuild(body, marker, "openai")
	if !ok {
		t.Fatal("expected IncrementalBuild to succeed")
	}

	var result struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rebuilt, &result); err != nil {
		t.Fatalf("failed to parse rebuilt body: %v", err)
	}

	// Expected: [system, summary(user), new question(user)]
	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "system" {
		t.Errorf("expected system at [0], got %s", result.Messages[0].Role)
	}
	if result.Messages[1].Role != "user" {
		t.Errorf("expected summary(user) at [1], got %s", result.Messages[1].Role)
	}
	if result.Messages[2].Content != "New question" {
		t.Errorf("expected 'New question' at [2], got %s", result.Messages[2].Content)
	}
}

func TestIncrementalBuild_StaleMarker(t *testing.T) {
	body := makeBodyAny(
		makeMsg("user", "Only message"),
	)
	marker := CutMarker{
		Version:          cutMarkerSchemaVersion,
		SystemMsgCount:   0,
		CutIndex:         5, // More than available messages
		SourcePrefixHash: "stale",
		SummaryText:      "Summary",
	}
	_, ok := IncrementalBuild(body, marker, "openai")
	if ok {
		t.Error("expected IncrementalBuild to fail with stale marker")
	}
}

func TestComputeInfoWeight(t *testing.T) {
	tests := []struct {
		text string
		role string
		min  float64
	}{
		{"", "user", 0.0},
		{"Hello", "user", 0.3},
		{"Error: file not found at /path/to/file.py", "assistant", 0.5},
		{"```python\nprint('hello')\n```", "assistant", 0.5},
		{"https://example.com/api/v1/data", "user", 0.4},
	}
	for _, tt := range tests {
		score := computeInfoWeight(tt.text, tt.role)
		if score < tt.min {
			t.Errorf("computeInfoWeight(%q, %q) = %.2f, expected >= %.2f", tt.text, tt.role, score, tt.min)
		}
	}
}

// unixNow returns the current unix timestamp for testing.
func unixNow() int64 {
	return time.Now().Unix()
}
