// e2e_alignment_test.go - Functional verification tests for O-1/O-2/O-3
// (2026-08-09 post-audit verification)
//
// 验证关键功能：AlignmentMap 构建 → 缓存编解码（AuditedAt + algn）
// → Memo 存取 → updateCache 集成。使用真实场景数据验证三种压缩路径。

package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────────────────
// Test Fixtures
// ────────────────────────────────────────────────────────────────────────────

// buildRealisticClientBody 构造真实场景的客户端请求 body（n 条消息）。
func buildRealisticClientBody(n int) []byte {
	msgs := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		content := fmt.Sprintf("User turn %d: please help with task", i)
		if i%2 == 1 {
			role = "assistant"
			content = fmt.Sprintf("Assistant response %d: I can help you with that", i)
		}
		msgs = append(msgs, map[string]any{"role": role, "content": content})
	}
	b, _ := json.Marshal(map[string]any{"messages": msgs})
	return b
}

// buildCompressedBody 构造压缩后 body（摘要 + 保留消息）。
func buildCompressedBodyWithSummary(summaryText string, retainedStart int, total int) []byte {
	msgs := []map[string]any{
		{"role": "assistant", "content": summaryText}, // 摘要在第一个 assistant 位置
	}
	for i := retainedStart; i < total; i++ {
		role := "user"
		content := fmt.Sprintf("User turn %d: please help with task", i)
		if i%2 == 1 {
			role = "assistant"
			content = fmt.Sprintf("Assistant response %d: I can help you with that", i)
		}
		msgs = append(msgs, map[string]any{"role": role, "content": content})
	}
	b, _ := json.Marshal(map[string]any{"messages": msgs})
	return b
}

// ────────────────────────────────────────────────────────────────────────────
// Scenario 1: LLM Summary - AlignmentMap 构建验证（真实场景数据）
// ────────────────────────────────────────────────────────────────────────────

func TestFunctional_AlignmentMap_LLMSummaryScenario(t *testing.T) {
	// 场景：15 条原始消息 → LLM 摘要前 10 条 → 保留后 5 条
	before := buildRealisticClientBody(15)
	after := buildCompressedBodyWithSummary("[SUMMARY] Earlier conversation summarized", 10, 15)
	summaryIdx := 0 // 摘要在 after[0]

	align := buildAlignmentMap(before, after, summaryIdx)

	// 验证 1：长度 = 15（每条原始消息一个条目）
	if len(align) != 15 {
		t.Fatalf("AlignmentMap len = %d, want 15", len(align))
	}

	// 验证 2：前 10 条被折叠进摘要（CompressedInto=0）
	for i := 0; i < 10; i++ {
		a := align[i]
		if !a.IsCompressed {
			t.Fatalf("orig %d should be compressed into summary", i)
		}
		if a.CompressedInto != 0 {
			t.Fatalf("orig %d want CompressedInto=0 (summary), got %d", i, a.CompressedInto)
		}
		if a.Hash == "" {
			t.Fatalf("orig %d should have content hash", i)
		}
	}

	// 验证 3：后 5 条 1:1 保留（CompressedIndex=1..5，after[1..5]）
	for i := 10; i < 15; i++ {
		a := align[i]
		if a.IsCompressed {
			t.Fatalf("orig %d should be retained 1:1 (not compressed)", i)
		}
		expectedAfterIdx := (i - 10) + 1 // after[1..5]
		if a.CompressedIndex != expectedAfterIdx {
			t.Fatalf("orig %d want CompressedIndex=%d, got %d", i, expectedAfterIdx, a.CompressedIndex)
		}
		if a.Hash == "" {
			t.Fatalf("orig %d should have content hash", i)
		}
	}

	t.Logf("✅ Scenario 1 PASS: LLM summary, folded=%d, retained=%d", 10, 5)
}

// ────────────────────────────────────────────────────────────────────────────
// Scenario 2: Mechanical Trim - AlignmentMap dropped=-1
// ────────────────────────────────────────────────────────────────────────────

func TestFunctional_AlignmentMap_MechanicalTrimScenario(t *testing.T) {
	// 场景：20 条原始消息 → 机械裁剪保留最后 6 条
	before := buildRealisticClientBody(20)
	after := buildRealisticClientBody(20) // 复制后只取最后 6 条
	var afterMsgs []map[string]any
	_ = json.Unmarshal(before, &struct {
		Messages *[]map[string]any `json:"messages"`
	}{Messages: &afterMsgs})
	afterMsgs = afterMsgs[14:] // 保留 index 14..19（最后 6 条）
	after, _ = json.Marshal(map[string]any{"messages": afterMsgs})

	align := buildAlignmentMap(before, after, -1) // summaryIdx=-1 表示无摘要

	// 验证 1：长度 = 20
	if len(align) != 20 {
		t.Fatalf("AlignmentMap len = %d, want 20", len(align))
	}

	// 验证 2：前 14 条被丢弃（CompressedInto=-1）
	for i := 0; i < 14; i++ {
		a := align[i]
		if !a.IsCompressed || a.CompressedInto != -1 {
			t.Fatalf("orig %d should be dropped (CompressedInto=-1), got compressed=%v into=%d",
				i, a.IsCompressed, a.CompressedInto)
		}
	}

	// 验证 3：后 6 条 1:1 保留
	for i := 14; i < 20; i++ {
		a := align[i]
		if a.IsCompressed {
			t.Fatalf("orig %d should be retained 1:1", i)
		}
		expectedAfterIdx := i - 14
		if a.CompressedIndex != expectedAfterIdx {
			t.Fatalf("orig %d want CompressedIndex=%d, got %d", i, expectedAfterIdx, a.CompressedIndex)
		}
	}

	t.Logf("✅ Scenario 2 PASS: Mechanical trim, dropped=%d, retained=%d", 14, 6)
}

// ────────────────────────────────────────────────────────────────────────────
// Scenario 3: Cache Persistence - AuditedAt + AlignmentMap 编解码
// ────────────────────────────────────────────────────────────────────────────

func TestFunctional_CachePersistence_AuditedAtAndAlignmentMap(t *testing.T) {
	// 构造真实 SessionState（带 AlignmentMap + AuditedAt）
	now := time.Now().Unix()
	st := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: "hash-abc123",
		MsgCount:         12,
		TokenEstimate:    450,
		SummaryMarker:    "smm_v1:def456",
		AuditedAt:        now,
		AlignmentMap: []AlignmentInfo{
			{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: true, CompressedInto: 0, Hash: "h0"},
			{OriginalIndex: 1, CompressedIndex: 0, IsCompressed: true, CompressedInto: 0, Hash: "h1"},
			{OriginalIndex: 2, CompressedIndex: 1, IsCompressed: false, CompressedInto: -1, Hash: "h2"},
		},
	}

	// 编码 → Redis hash fields
	fields := encodeSessionStateFields(st)
	fieldsMap := fieldsToMap(fields)

	// 验证 1：aud_at 字段存在
	if audAt, ok := fieldsMap["aud_at"]; !ok || audAt == "0" {
		t.Fatalf("aud_at field missing or zero: %v", fieldsMap)
	}

	// 验证 2：algn 字段存在且为 JSON
	algnJSON, ok := fieldsMap["algn"]
	if !ok {
		t.Fatal("algn field missing")
	}
	var algnDecoded []AlignmentInfo
	if err := json.Unmarshal([]byte(algnJSON), &algnDecoded); err != nil {
		t.Fatalf("algn field not valid JSON: %v", err)
	}
	if len(algnDecoded) != 3 {
		t.Fatalf("algn decoded len = %d, want 3", len(algnDecoded))
	}

	// 解码 → SessionState
	decoded := &SessionState{}
	if err := decodeSessionStateFields(fieldsMap, decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// 验证 3：AuditedAt 正确恢复
	if decoded.AuditedAt != now {
		t.Fatalf("AuditedAt mismatch: got %d want %d", decoded.AuditedAt, now)
	}

	// 验证 4：AlignmentMap 正确恢复
	if !reflect.DeepEqual(decoded.AlignmentMap, st.AlignmentMap) {
		t.Fatalf("AlignmentMap mismatch:\n got  %+v\n want %+v", decoded.AlignmentMap, st.AlignmentMap)
	}

	t.Logf("✅ Scenario 3 PASS: AuditedAt=%d, AlignmentMap len=%d, round-trip OK", decoded.AuditedAt, len(decoded.AlignmentMap))
}

// ────────────────────────────────────────────────────────────────────────────
// Scenario 4: Memo Storage - AlignmentMap 存取一致性
// ────────────────────────────────────────────────────────────────────────────

func TestFunctional_MemoStorage_AlignmentMapConsistency(t *testing.T) {
	// 构造 MemoValue（带 AlignmentMap）
	alignOrig := []AlignmentInfo{
		{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: true, CompressedInto: 0, Hash: "h0"},
		{OriginalIndex: 1, CompressedIndex: 1, IsCompressed: false, CompressedInto: -1, Hash: "h1"},
	}
	alignJSON, _ := json.Marshal(alignOrig)

	memo := &MemoValue{
		CompressedBody:       []byte(`{"messages":[{"role":"assistant","content":"summary"}]}`),
		Strategy:             "summary",
		SummaryMarker:        "smm_v1:test",
		CompressedPrefixHash: "prefix-hash-def",
		AlignmentMap:         json.RawMessage(alignJSON),
		CachedAt:             time.Now(),
	}

	// 序列化 → JSON
	memoJSON, err := json.Marshal(memo)
	if err != nil {
		t.Fatalf("marshal memo: %v", err)
	}

	// 反序列化 → MemoValue
	var memoRestored MemoValue
	if err := json.Unmarshal(memoJSON, &memoRestored); err != nil {
		t.Fatalf("unmarshal memo: %v", err)
	}

	// 验证：AlignmentMap 正确恢复
	var alignRestored []AlignmentInfo
	if err := json.Unmarshal(memoRestored.AlignmentMap, &alignRestored); err != nil {
		t.Fatalf("unmarshal AlignmentMap from memo: %v", err)
	}
	if !reflect.DeepEqual(alignRestored, alignOrig) {
		t.Fatalf("AlignmentMap from memo mismatch:\n got  %+v\n want %+v", alignRestored, alignOrig)
	}

	t.Logf("✅ Scenario 4 PASS: Memo AlignmentMap round-trip OK, len=%d", len(alignRestored))
}

// ────────────────────────────────────────────────────────────────────────────
// Scenario 5: UpdateCache Integration - AuditedAt stamping
// ────────────────────────────────────────────────────────────────────────────

func TestFunctional_UpdateCache_AuditedAtStamping(t *testing.T) {
	// 使用 captureBackend 验证 updateCache 写入的 AuditedAt
	cap := &captureBackend{}
	cache := NewSessionCache(cap, nil)
	sc := &SessionCompressor{deps: SessionCompressorDeps{Cache: cache}}

	res := &PrepareResult{
		MsgCount:      8,
		TokenEst:      300,
		SummaryMarker: "smm_v1:test",
		AlignmentMap: []AlignmentInfo{
			{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: false, CompressedInto: -1, Hash: "h0"},
		},
	}
	outbound := []byte(`{"messages":[{"role":"user","content":"test"}]}`)

	// 调用 updateCache
	sc.updateCache(context.Background(), "tenant-test", "gw-test-01", nil, outbound, res, false, false)

	// 读取捕获的 Redis fields
	cap.mu.Lock()
	fields := cap.fields
	cap.mu.Unlock()

	if len(fields) == 0 {
		t.Fatal("updateCache did not write to cache")
	}

	// 解码 SessionState
	st := &SessionState{}
	if err := decodeSessionStateFields(fields, st); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// 验证：AuditedAt 已打上且合理（最近 10 秒内）
	now := time.Now().Unix()
	if st.AuditedAt == 0 {
		t.Fatal("O-1: AuditedAt must be >0 after updateCache")
	}
	if st.AuditedAt < now-10 || st.AuditedAt > now+10 {
		t.Fatalf("AuditedAt=%d out of reasonable range (now=%d)", st.AuditedAt, now)
	}

	// 验证：AlignmentMap 持久化
	if len(st.AlignmentMap) != 1 {
		t.Fatalf("AlignmentMap len = %d, want 1", len(st.AlignmentMap))
	}

	t.Logf("✅ Scenario 5 PASS: updateCache stamped AuditedAt=%d (within 10s), AlignmentMap persisted", st.AuditedAt)
}
