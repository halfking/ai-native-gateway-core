// Package compression - marker_idempotency_test.go
//
// 验证重复压缩marker的幂等性 (审计遗留 #6)
//
// 测试场景:
//   A. 压缩后的会话再次压缩 (marker嵌套)
//   B. marker消息重复出现
//   C. 多轮压缩后marker累积
//
// 验证目标:
//   - isSummaryMarkerMsg 正确识别所有marker变体
//   - diff对齐不会把marker内容当成普通消息
//   - marker不参与LCS hash索引
//   - 重复压缩不会产生marker嵌套或泄漏
package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────────────────────
// Scenario A: 压缩后的会话再次压缩 (marker嵌套风险)
// ────────────────────────────────────────────────────────────────────────────────

// TestMarkerIdempotency_NestedCompression drives the REAL production
// rebuild path (extractOpenAI → RebuildOpenAIAfterSummary →
// injectOpenAISummaryMarker) across two compression rounds, rather than
// hand-rolling a marker with injectMarkerAtFront. This is what actually
// exercises the bug fixed in retain.go: extractOpenAI's FirstUser pin must
// skip an existing smm_v1 marker, or round 2's rebuild pins round 1's
// marker as B-track and nests it alongside the fresh round-2 marker.
func TestMarkerIdempotency_NestedCompression(t *testing.T) {
	// Round 1: original conversation, first LLM summary compresses it.
	body1 := makeBody([]map[string]string{
		userMsg("turn1 user"),
		assistantMsg("turn1 assistant"),
		userMsg("turn2 user"),
		assistantMsg("turn2 assistant"),
		userMsg("turn3 user"),
		assistantMsg("turn3 assistant"),
	})
	ret1, err := extractOpenAI(body1)
	if err != nil {
		t.Fatalf("extractOpenAI round1: %v", err)
	}
	rebuilt1, ok := RebuildOpenAIAfterSummary(body1, "Summary of turn1-2", ret1, 2)
	if !ok {
		t.Fatalf("rebuild round1 failed")
	}
	marker1, marked1 := injectOpenAISummaryMarker(rebuilt1)
	if marker1 == "" || marked1 == nil {
		t.Fatalf("inject marker1 failed")
	}

	// 验证点1: marked1应该只含1个marker
	msgs1, _ := myExtractMessages(marked1)
	markerCount1 := 0
	for _, m := range msgs1 {
		if isSummaryMarkerMsg(m) {
			markerCount1++
		}
	}
	if markerCount1 != 1 {
		t.Fatalf("expected exactly 1 marker after round1, got %d", markerCount1)
	}

	// Round 2: client appends more turns on top of the round-1 compressed
	// body (which still contains marker1 as messages[0]). A second window
	// trigger fires another LLM summary over this body.
	msgs1Raw, _ := extractMessages(marked1)
	extra := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"turn4 user"}`),
		json.RawMessage(`{"role":"assistant","content":"turn4 assistant"}`),
		json.RawMessage(`{"role":"user","content":"turn5 user"}`),
		json.RawMessage(`{"role":"assistant","content":"turn5 assistant"}`),
	}
	allMsgs := append(msgs1Raw, extra...)
	newMsgsJSON, _ := json.Marshal(allMsgs)
	body2, ok := spliceBodyMessages(marked1, newMsgsJSON)
	if !ok {
		t.Fatalf("splice body2 failed")
	}

	ret2, err := extractOpenAI(body2)
	if err != nil {
		t.Fatalf("extractOpenAI round2: %v", err)
	}
	// The fix under test: FirstUser must be the real "turn1 user" message,
	// not marker1 (which also has role=user and sits at index 0).
	if strings.Contains(stringValue(ret2.FirstUser), CompactionMarkerPrefix) {
		t.Fatal("extractOpenAI pinned the round-1 marker as FirstUser — marker will nest")
	}

	rebuilt2, ok := RebuildOpenAIAfterSummary(body2, "Summary of turn1-4", ret2, 2)
	if !ok {
		t.Fatalf("rebuild round2 failed")
	}
	marker2, marked2 := injectOpenAISummaryMarker(rebuilt2)
	if marker2 == "" || marked2 == nil {
		t.Fatalf("inject marker2 failed")
	}

	// 验证点2: marked2应该只有marker2（marker1不应该被误钉住并重新出现）
	msgs2, _ := myExtractMessages(marked2)
	markerCount2 := 0
	hasMarker1 := false
	hasMarker2 := false
	for _, m := range msgs2 {
		if isSummaryMarkerMsg(m) {
			markerCount2++
			var msg struct {
				Content string `json:"content"`
			}
			json.Unmarshal(m, &msg)
			if strings.Contains(msg.Content, "Summary of turn1-2") {
				hasMarker1 = true
			}
			if strings.Contains(msg.Content, "Summary of turn1-4") {
				hasMarker2 = true
			}
		}
	}

	if markerCount2 > 1 {
		t.Errorf("marker nesting detected: expected 1 marker after round2, got %d", markerCount2)
	}
	if hasMarker1 && hasMarker2 {
		t.Error("both marker1 and marker2 present - marker replacement failed")
	}
	if !hasMarker2 {
		t.Error("marker2 not found - latest marker injection failed")
	}
	_ = marker1
}

// ────────────────────────────────────────────────────────────────────────────────
// Scenario B: marker消息重复出现 (client误发或缓存污染)
// ────────────────────────────────────────────────────────────────────────────────

func TestMarkerIdempotency_DuplicateMarkers(t *testing.T) {
	marker := BuildSummaryMarker("Summary content")

	// 构造包含重复marker的lastOutbound (异常场景)
	lastWithDuplicates := makeBody([]map[string]string{
		summaryMsg("Summary content"), // marker1
		userMsg("recent q1"),
		assistantMsg("recent a1"),
		summaryMsg("Summary content"), // marker2 (duplicate)
		userMsg("recent q2"),
		assistantMsg("recent a2"),
	})

	// 客户端追加新消息
	client := makeBody([]map[string]string{
		userMsg("recent q1"),
		assistantMsg("recent a1"),
		userMsg("recent q2"),
		assistantMsg("recent a2"),
		userMsg("new turn"),
	})

	state := &SessionState{SchemaVersion: 1, SummaryMarker: marker}
	res, err := BuildOutboundMessages(client, state, lastWithDuplicates, "openai")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// 验证: 即使lastOutbound有重复marker，outbound应该去重或保留唯一marker
	msgs, _ := myExtractMessages(res.Body)
	markerCount := 0
	for _, m := range msgs {
		if isSummaryMarkerMsg(m) {
			markerCount++
		}
	}

	// 期望: diff算法应该保留所有lastOutbound的marker（因为marker不参与LCS）
	// 但业务上应该避免重复marker累积
	if markerCount > 2 {
		t.Errorf("excessive markers: expected ≤2 (from lastOutbound), got %d", markerCount)
	}

	t.Logf("marker count in output: %d (input had 2 duplicate markers)", markerCount)
}

// ────────────────────────────────────────────────────────────────────────────────
// Scenario C: isSummaryMarkerMsg 识别各种marker格式
// ────────────────────────────────────────────────────────────────────────────────

func TestIsSummaryMarkerMsg_Variants(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		wantTrue bool
	}{
		{
			name:     "plain string marker",
			msg:      `{"role":"assistant","content":"[smm_v1:abc123]\nSummary text"}`,
			wantTrue: true,
		},
		{
			name:     "array content with text part",
			msg:      `{"role":"assistant","content":[{"type":"text","text":"[smm_v1:def456]\nSummary"}]}`,
			wantTrue: true,
		},
		{
			name:     "array content marker not in first text",
			msg:      `{"role":"assistant","content":[{"type":"image_url"},{"type":"text","text":"[smm_v1:xyz]\nSummary"}]}`,
			wantTrue: true, // isSummaryMarkerMsg检查第一个type=="text"的part（跳过非text类型）
		},
		{
			name:     "marker embedded in middle",
			msg:      `{"role":"assistant","content":"Some text [smm_v1:789] more"}`,
			wantTrue: false, // 必须是前缀
		},
		{
			name:     "no marker",
			msg:      `{"role":"user","content":"Normal message"}`,
			wantTrue: false,
		},
		{
			name:     "marker in nested array",
			msg:      `{"role":"assistant","content":[{"type":"text","text":"Normal"},{"type":"text","text":"[smm_v1:nested]"}]}`,
			wantTrue: false, // 只检查第一个text part
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSummaryMarkerMsg(json.RawMessage(tt.msg))
			if got != tt.wantTrue {
				t.Errorf("isSummaryMarkerMsg() = %v, want %v", got, tt.wantTrue)
			}
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────────
// Scenario D: marker不参与LCS hash (验证marker不会影响diff对齐)
// ────────────────────────────────────────────────────────────────────────────────

func TestMarkerIdempotency_NotInLCSIndex(t *testing.T) {
	marker := BuildSummaryMarker("Summary")

	// lastOutbound: marker + 2条消息
	last := makeBody([]map[string]string{
		summaryMsg("Summary"),
		userMsg("q1"),
		assistantMsg("a1"),
	})

	// 客户端发送原始历史（不含marker）+ 新消息
	client := makeBody([]map[string]string{
		userMsg("q1"),
		assistantMsg("a1"),
		userMsg("q2"), // new
	})

	state := &SessionState{SchemaVersion: 1, SummaryMarker: marker}
	res, err := BuildOutboundMessages(client, state, last, "openai")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// 验证: diff应该找到q1/a1的LCS，delta=1 (q2)
	if res.DeltaCount != 1 {
		t.Errorf("DeltaCount = %d, want 1 (only q2 is new)", res.DeltaCount)
	}

	// 验证: outbound应该是 marker + q1 + a1 + q2
	msgs, _ := myExtractMessages(res.Body)
	if len(msgs) != 4 {
		t.Errorf("outbound message count = %d, want 4", len(msgs))
	}

	// 验证: 第一条是marker
	if !isSummaryMarkerMsg(msgs[0]) {
		t.Error("first message should be marker")
	}
}

// ────────────────────────────────────────────────────────────────────────────────
// Scenario E: 多轮压缩后marker累积 (验证不会无限增长)
// ────────────────────────────────────────────────────────────────────────────────

// TestMarkerIdempotency_MultiRoundAccumulation drives 10 rounds of the REAL
// rebuild path (extractOpenAI → RebuildOpenAIAfterSummary →
// injectOpenAISummaryMarker), each round appending two new turns onto the
// previous round's marked body, then triggering another summary. Without
// the retain.go fix, every round's FirstUser pin would grab the previous
// marker and the marker count would grow unbounded.
func TestMarkerIdempotency_MultiRoundAccumulation(t *testing.T) {
	body := makeBody([]map[string]string{
		userMsg("turn0 user"),
		assistantMsg("turn0 assistant"),
	})

	for round := 1; round <= 10; round++ {
		ret, err := extractOpenAI(body)
		if err != nil {
			t.Fatalf("round %d extractOpenAI error: %v", round, err)
		}
		rebuilt, ok := RebuildOpenAIAfterSummary(body, "Summary round "+string(rune('0'+round)), ret, 2)
		if !ok {
			t.Fatalf("round %d rebuild failed", round)
		}
		marker, marked := injectOpenAISummaryMarker(rebuilt)
		if marker == "" || marked == nil {
			t.Fatalf("round %d inject marker failed", round)
		}

		// Append two new turns for the next round to summarise.
		msgsRaw, _ := extractMessages(marked)
		extra := []json.RawMessage{
			json.RawMessage(`{"role":"user","content":"turn` + string(rune('0'+round)) + `+1 user"}`),
			json.RawMessage(`{"role":"assistant","content":"turn` + string(rune('0'+round)) + `+1 assistant"}`),
		}
		allMsgs := append(msgsRaw, extra...)
		newMsgsJSON, _ := json.Marshal(allMsgs)
		nextBody, ok := spliceBodyMessages(marked, newMsgsJSON)
		if !ok {
			t.Fatalf("round %d splice failed", round)
		}
		body = nextBody
	}

	// 验证: 10轮后，body应该只有1个marker（最新的）
	msgs, _ := myExtractMessages(body)
	markerCount := 0
	for _, m := range msgs {
		if isSummaryMarkerMsg(m) {
			markerCount++
		}
	}

	// 关键: marker不应该累积到10个
	if markerCount != 1 {
		t.Errorf("marker accumulation detected: got %d markers after 10 rounds, expected exactly 1", markerCount)
	}

	t.Logf("after 10 rounds of compression: %d markers, %d total messages", markerCount, len(msgs))
}

// ────────────────────────────────────────────────────────────────────────────────
// Helper functions
// ────────────────────────────────────────────────────────────────────────────────

// injectMarkerAtFront 在messages数组首部注入marker消息
func injectMarkerAtFront(body []byte, marker string) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	msgs, ok := req["messages"].([]any)
	if !ok {
		return body
	}

	markerMsg := map[string]string{
		"role":    "assistant",
		"content": marker + "\nSummary content here",
	}

	// 在首部插入marker
	newMsgs := append([]any{markerMsg}, msgs...)
	req["messages"] = newMsgs

	result, _ := json.Marshal(req)
	return result
}

// myExtractMessages 从body中提取messages数组 (避免与diff.go的extractMessages冲突)
func myExtractMessages(body []byte) ([]json.RawMessage, error) {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return req.Messages, nil
}
