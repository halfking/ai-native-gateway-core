package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// messagesBody builds an OpenAI-shaped body with n alternating user/assistant
// messages whose content is "msg-<i>". Deterministic content lets retained
// messages share an identical msgHash between before/after bodies.
func messagesBody(n int) []byte {
	msgs := make([]map[string]string, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]string{"role": role, "content": fmt.Sprintf("msg-%d", i)})
	}
	b, err := json.Marshal(map[string]any{"messages": msgs})
	if err != nil {
		panic(err)
	}
	return b
}

// TestBuildAlignmentMap_LLMSummary covers the summary path: history messages
// folded into the injected summary (after[0]) are marked IsCompressed with
// CompressedInto=0, while retained messages keep a 1:1 mapping.
func TestBuildAlignmentMap_LLMSummary(t *testing.T) {
	before := messagesBody(15)
	afterMsgs := []map[string]string{{"role": "assistant", "content": "[summary] folded history"}}
	for i := 10; i < 15; i++ {
		afterMsgs = append(afterMsgs, map[string]string{"role": roleFor(i), "content": fmt.Sprintf("msg-%d", i)})
	}
	after, err := json.Marshal(map[string]any{"messages": afterMsgs})
	if err != nil {
		t.Fatal(err)
	}

	align := buildAlignmentMap(before, after, 0)
	if len(align) != 15 {
		t.Fatalf("want 15 entries (one per original message), got %d", len(align))
	}

	for i := 0; i < 10; i++ {
		a := align[i]
		if a.TargetKind != "summary" || a.TargetSpace != "messages" {
			t.Fatalf("orig %d target = %s/%s, want summary/messages", i, a.TargetKind, a.TargetSpace)
		}
		if !a.IsCompressed {
			t.Fatalf("orig %d should be folded into summary", i)
		}
		if a.CompressedIndex != 0 || a.CompressedInto != 0 {
			t.Fatalf("orig %d want CompressedIndex/Into=0 (summary), got %d/%d",
				i, a.CompressedIndex, a.CompressedInto)
		}
	}
	// Retained messages: orig 10..14 → after positions 1..5.
	for i, want := range map[int]int{10: 1, 11: 2, 12: 3, 13: 4, 14: 5} {
		a := align[i]
		if a.IsCompressed {
			t.Fatalf("orig %d should be retained 1:1", i)
		}
		if a.CompressedIndex != want || a.CompressedInto != -1 {
			t.Fatalf("orig %d want CompressedIndex=%d Into=-1, got %d/%d",
				i, want, a.CompressedIndex, a.CompressedInto)
		}
		if a.Hash == "" {
			t.Fatalf("orig %d should carry a content hash", i)
		}
	}
}

// TestBuildAlignmentMap_MechanicalTrim covers the degraded/trim path: dropped
// messages are IsCompressed=true with CompressedIndex/Into=-1 (no single
// replacement), retained tail messages map 1:1.
func TestBuildAlignmentMap_MechanicalTrim(t *testing.T) {
	before := messagesBody(10)
	after := messagesBody(10)[:0] // rebuild below to keep only the tail 4
	_ = after

	// Keep only the last 4 messages (indices 6..9).
	var msgs []map[string]string
	for i := 6; i < 10; i++ {
		msgs = append(msgs, map[string]string{"role": roleFor(i), "content": fmt.Sprintf("msg-%d", i)})
	}
	after, err := json.Marshal(map[string]any{"messages": msgs})
	if err != nil {
		t.Fatal(err)
	}

	align := buildAlignmentMap(before, after, -1)
	if len(align) != 10 {
		t.Fatalf("want 10 entries, got %d", len(align))
	}
	for i := 0; i < 6; i++ {
		a := align[i]
		if a.TargetKind != "dropped" || a.TargetSpace != "none" {
			t.Fatalf("orig %d target = %s/%s, want dropped/none", i, a.TargetKind, a.TargetSpace)
		}
		if !a.IsCompressed || a.CompressedIndex != -1 || a.CompressedInto != -1 {
			t.Fatalf("orig %d want dropped (-1/-1, compressed), got compressed=%v idx=%d into=%d",
				i, a.IsCompressed, a.CompressedIndex, a.CompressedInto)
		}
	}
	for i, want := range map[int]int{6: 0, 7: 1, 8: 2, 9: 3} {
		a := align[i]
		if a.IsCompressed || a.CompressedIndex != want {
			t.Fatalf("orig %d want retained at after[%d], got compressed=%v idx=%d",
				i, want, a.IsCompressed, a.CompressedIndex)
		}
	}
}

func TestBuildAlignmentMap_AnthropicSystemSummary(t *testing.T) {
	before := []byte(`{"system":"old system","messages":[{"role":"user","content":"old"},{"role":"assistant","content":"old answer"},{"role":"user","content":"latest"}]}`)
	after, err := json.Marshal(map[string]any{
		"system":   AnthropicSystemSummaryPrefix + "prior turns",
		"messages": []map[string]string{{"role": "user", "content": "latest"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	align := buildAlignmentMapForProtocol(before, after, -1, "anthropic-messages")
	if len(align) != 3 {
		t.Fatalf("want 3 entries, got %d", len(align))
	}
	for i := 0; i < 2; i++ {
		if align[i].TargetKind != "summary" || align[i].TargetSpace != "top_level_system" || align[i].CompressedIndex != -1 {
			t.Fatalf("orig %d system summary target = %+v", i, align[i])
		}
	}
	if align[2].TargetKind != "retained" || align[2].TargetSpace != "messages" || align[2].CompressedIndex != 0 {
		t.Fatalf("latest message target = %+v", align[2])
	}
}

func TestBuildAlignmentMap_InvalidSummaryIndexFailsSafe(t *testing.T) {
	before := messagesBody(2)
	after := messagesBody(2)
	align := buildAlignmentMap(before, after, 99)
	for _, item := range align {
		if item.TargetKind == "summary" || item.CompressedIndex == 99 {
			t.Fatalf("out-of-range summary index leaked into alignment: %+v", align)
		}
	}
}

// original body has no parseable messages.
func TestBuildAlignmentMap_EmptyInput(t *testing.T) {
	if got := buildAlignmentMap(nil, []byte(`{"messages":[]}`), -1); got != nil {
		t.Fatalf("nil before body must yield nil map, got %+v", got)
	}
	if got := buildAlignmentMap([]byte(`not-json`), nil, 0); got != nil {
		t.Fatalf("unparseable before body must yield nil map, got %+v", got)
	}
	if got := buildAlignmentMap([]byte(`{"messages":[]}`), messagesBody(3), -1); got != nil {
		t.Fatalf("empty before messages must yield nil map, got %+v", got)
	}
}

// TestFirstAssistantIndex verifies the summary-index lookup used to align
// folded messages with the injected smm_v1 marker.
func TestFirstAssistantIndex(t *testing.T) {
	if got := firstAssistantIndex([]byte(`{"messages":[{"role":"user","content":"a"}]}`)); got != -1 {
		t.Fatalf("no assistant message should return -1, got %d", got)
	}
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"a"},` +
		`{"role":"assistant","content":"b"},` +
		`{"role":"assistant","content":"c"}]}`)
	if got := firstAssistantIndex(body); got != 1 {
		t.Fatalf("want first assistant index 1, got %d", got)
	}
	if got := firstAssistantIndex([]byte(`not-json`)); got != -1 {
		t.Fatalf("unparseable body should return -1, got %d", got)
	}
}

// TestSessionState_AlignmentMapRoundTrip covers the v7 Redis hash encoding:
// the "algn" JSON array and the "aud_at" stamp survive
// encodeSessionStateFields → decodeSessionStateFields unchanged, and a
// corrupt "algn" value degrades to nil instead of failing the decode.
func TestSessionState_AlignmentMapRoundTrip(t *testing.T) {
	st := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: "loh-abc",
		MsgCount:         3,
		TokenEstimate:    120,
		SummaryMarker:    "smm_v1:abc",
		AuditedAt:        1750000000,
		AlignmentMap: []AlignmentInfo{
			{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: true, CompressedInto: 0, Hash: "h0", Occurrence: 0, TargetKind: "summary", TargetSpace: "messages"},
			{OriginalIndex: 1, CompressedIndex: 1, IsCompressed: false, CompressedInto: -1, Hash: "h1", Occurrence: 0, TargetKind: "retained", TargetSpace: "messages"},
		},
	}
	fields := encodeSessionStateFields(st)

	got := &SessionState{}
	if err := decodeSessionStateFields(fieldsToMap(fields), got); err != nil {
		t.Fatal(err)
	}
	if got.AuditedAt != st.AuditedAt {
		t.Fatalf("aud_at round-trip mismatch: got %d want %d", got.AuditedAt, st.AuditedAt)
	}
	if !reflect.DeepEqual(got.AlignmentMap, st.AlignmentMap) {
		t.Fatalf("algn round-trip mismatch:\n got  %+v\n want %+v", got.AlignmentMap, st.AlignmentMap)
	}

	// Corrupt "algn" → nil, not an error.
	bad := map[string]string{"algn": "{{{not-json"}
	gotBad := &SessionState{}
	if err := decodeSessionStateFields(bad, gotBad); err != nil {
		t.Fatalf("corrupt algn must not fail decode: %v", err)
	}
	if gotBad.AlignmentMap != nil {
		t.Fatalf("corrupt algn should degrade to nil, got %+v", gotBad.AlignmentMap)
	}
}

// captureBackend records the last HSet field set so a test can decode the
// exact SessionState that updateCache persisted through SessionCache.Set.
type captureBackend struct {
	mu     sync.Mutex
	fields map[string]string
}

func (c *captureBackend) HSet(_ context.Context, _ string, values ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fields = make(map[string]string, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		k, _ := values[i].(string)
		v, _ := values[i+1].(string)
		c.fields[k] = v
	}
	return nil
}
func (c *captureBackend) HGetAll(_ context.Context, _ string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (c *captureBackend) Expire(_ context.Context, _ string, _ time.Duration) error { return nil }
func (c *captureBackend) Del(_ context.Context, _ string) error                     { return nil }

// TestUpdateCache_StampsAuditedAt_PersistsAlignmentMap exercises O-1 + O-2
// end to end through updateCache → SessionCache.Set → saveToRedis: the
// persisted state carries AuditedAt>0 (audit pipeline passed) and the
// AlignmentMap verbatim.
func TestUpdateCache_StampsAuditedAt_PersistsAlignmentMap(t *testing.T) {
	cap := &captureBackend{}
	cache := NewSessionCache(cap, nil)
	sc := &SessionCompressor{deps: SessionCompressorDeps{Cache: cache}}

	am := []AlignmentInfo{
		{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: true, CompressedInto: 0, Hash: "h0"},
		{OriginalIndex: 1, CompressedIndex: 1, IsCompressed: false, CompressedInto: -1, Hash: "h1"},
	}
	res := &PrepareResult{
		MsgCount:            2,
		TokenEst:            80,
		SummaryMarker:       "smm_v1:abc",
		CompressionStrategy: "summary",
		AlignmentMap:        am,
	}
	outbound := []byte(`{"messages":[{"role":"assistant","content":"summary"},{"role":"user","content":"tail"}]}`)

	sc.updateCache(context.Background(), "t1", "gw_algn01", nil, outbound, res, true, false)

	cap.mu.Lock()
	fields := make(map[string]string, len(cap.fields))
	for k, v := range cap.fields {
		fields[k] = v
	}
	cap.mu.Unlock()

	if len(fields) == 0 {
		t.Fatal("no HSet fields captured — cache write did not happen")
	}

	st := &SessionState{}
	if err := decodeSessionStateFields(fields, st); err != nil {
		t.Fatal(err)
	}
	if st.AuditedAt == 0 {
		t.Fatal("O-1: AuditedAt must be stamped >0 on cache write")
	}
	if !reflect.DeepEqual(st.AlignmentMap, am) {
		t.Fatalf("O-2: AlignmentMap mismatch:\n got  %+v\n want %+v", st.AlignmentMap, am)
	}
	if st.MsgCount != 2 || st.TokenEstimate != 80 {
		t.Fatalf("unexpected state fields: mc=%d te=%d", st.MsgCount, st.TokenEstimate)
	}
	if st.LastCompressedAt == 0 {
		t.Fatal("LastCompressedAt should be stamped")
	}
}

func TestBuildAlignmentMap_DuplicateMessagesPreserveOccurrenceOrder(t *testing.T) {
	before := []byte(`{"messages":[{"role":"user","content":"same"},{"role":"user","content":"same"}]}`)
	after := []byte(`{"messages":[{"role":"user","content":"same"},{"role":"user","content":"same"}]}`)

	align := buildAlignmentMap(before, after, -1)
	if len(align) != 2 {
		t.Fatalf("want two alignment entries, got %d", len(align))
	}
	if align[0].IsCompressed || align[0].CompressedIndex != 0 {
		t.Fatalf("first duplicate mapped incorrectly: %+v", align[0])
	}
	if align[1].IsCompressed || align[1].CompressedIndex != 1 {
		t.Fatalf("second duplicate mapped incorrectly: %+v", align[1])
	}
}

func TestBuildAlignmentMap_RecordsDuplicateOccurrences(t *testing.T) {
	before := []byte(`{"messages":[{"role":"user","content":"same"},{"role":"user","content":"same"}]}`)
	after := []byte(`{"messages":[{"role":"user","content":"same"}]}`)
	align := buildAlignmentMap(before, after, -1)
	if len(align) != 2 || align[0].Occurrence != 0 || align[1].Occurrence != 1 {
		t.Fatalf("occurrences = %+v", align)
	}
	if align[0].TargetKind != "retained" || align[1].TargetKind != "dropped" {
		t.Fatalf("target kinds = %+v", align)
	}
}

func roleFor(i int) string {
	if i%2 == 1 {
		return "assistant"
	}
	return "user"
}
