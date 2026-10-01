package compression

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSummaryGuardFailureSelectsMechanicalFallback(t *testing.T) {
	messages := []map[string]any{}
	for i := 0; i < 10; i++ {
		messages = append(messages, map[string]any{"role": "user", "content": strings.Repeat("historical context ", 300)}, map[string]any{"role": "assistant", "content": strings.Repeat("historical context ", 300)})
	}
	body, _ := json.Marshal(map[string]any{"messages": messages})
	ctx := WithGeneratedTextGuard(context.Background(), func(context.Context, string) (string, error) { return "", errors.New("guard unavailable") })
	result := NewRecoveryCoordinator(RecoveryDeps{Summarizer: func(context.Context, []byte, string) (string, bool) { return "unchecked-summary", true }}).Recover(ctx, body, "openai", 5000, "tenant", "session", 0)
	if !result.ShouldRetry || result.Strategy != "smart_window_mechanical" || strings.Contains(string(result.NewBody), "unchecked-summary") {
		t.Fatalf("unsafe fallback: %+v", result)
	}
}

func TestMemoDoesNotCrossSanitizeGeneration(t *testing.T) {
	memo := newResultMemoWithStore(newFakeMemoStore(), time.Minute)
	parts := testMemoParts()
	parts.SanitizeGeneration = "old"
	body := []byte(`{"messages":[{"role":"user","content":"{SENSITIVE:phone:1}"}]}`)
	if err := memo.Set(context.Background(), parts, body, fullMemoValue()); err != nil {
		t.Fatal(err)
	}
	if value, err := memo.Get(context.Background(), parts, body); err != nil || value == nil {
		t.Fatal("same-generation memo miss")
	}
	parts.SanitizeGeneration = "new"
	if value, err := memo.Get(context.Background(), parts, body); err != nil || value != nil {
		t.Fatal("memo reused after mapping replacement")
	}
}

func TestV2RecoveryDoesNotCrossSanitizeGeneration(t *testing.T) {
	body := makeBodyAny(makeMsg("system", "sys"), makeMsg("user", strings.Repeat("old ", 100)), makeMsg("assistant", strings.Repeat("old ", 100)), makeMsg("user", "latest"))
	rc := NewRecoveryCoordinator(RecoveryDeps{V2Meta: stubV2RecoveryMeta{meta: map[string]any{
		"sanitize_map_generation": "old", "cut_marker": map[string]interface{}{"created_at": float64(time.Now().Unix()), "source_msg_count": 4, "system_msg_count": 1, "cut_index": 2, "strategy": "mechanical_trim"},
	}}, V2Builder: stubV2RecoveryBuilder{body: []byte(`[{"role":"system","content":"sys"},{"role":"user","content":"latest"}]`)}})
	ctx := WithSanitizeInfo(context.Background(), SanitizeInfo{MapGeneration: "old"})
	same := rc.Recover(ctx, body, "openai", 1000, "tenant", "session", 0)
	if same.Strategy != "incremental_v2_metadata" {
		t.Fatalf("matching generation did not reuse valid metadata: %+v", same)
	}
	ctx = WithSanitizeInfo(context.Background(), SanitizeInfo{MapGeneration: "new"})
	changed := rc.Recover(ctx, body, "openai", 1000, "tenant", "session", 0)
	if changed.Strategy == "incremental_v2_metadata" {
		t.Fatal("V2 reused stale mapping generation")
	}
}

func TestCurrentGuardRejectsPreviouslyCachedGeneratedSecret(t *testing.T) {
	memo := newResultMemoWithStore(newFakeMemoStore(), time.Minute)
	parts := testMemoParts()
	body := []byte(`{"messages":[{"role":"user","content":"ordinary request"}]}`)
	value := fullMemoValue()
	value.CompressedBody = []byte(`{"messages":[{"role":"user","content":"[smm_v1:old] password=UncheckedSecret"}]}`)
	if err := memo.Set(context.Background(), parts, body, value); err != nil {
		t.Fatal(err)
	}
	ctx := WithGeneratedTextGuard(context.Background(), func(ctx context.Context, text string) (string, error) {
		return strings.ReplaceAll(text, "UncheckedSecret", "[REDACTED]"), nil
	})
	if got, err := memo.Get(ctx, parts, body); err != nil || got != nil {
		t.Fatal("old unchecked memo reused")
	}
	if cachedBodyPassesGuard(ctx, value.CompressedBody) {
		t.Fatal("old unsafe session body accepted")
	}
	if !cachedBodyPassesGuard(ctx, body) {
		t.Fatal("safe cache rejected")
	}
	if !strings.HasPrefix(memoKey(parts, body), "compression:memo:v2:") {
		t.Fatal("legacy memo namespace reused")
	}
	ctx = WithGeneratedTextGuard(context.Background(), func(context.Context, string) (string, error) { return "", errors.New("unavailable") })
	if cachedBodyPassesGuard(ctx, body) {
		t.Fatal("cache check failure failed open")
	}
}

func TestCompressionSourceSnapshotRedisRoundTrip(t *testing.T) {
	original := &SessionState{CompressionSourceSnapshot: MessageSnapshot{Hash: "assembled", MessageCount: 3, TokenEstimate: 55}}
	encoded := encodeSessionStateFields(original)
	fields := make(map[string]string)
	for i := 0; i < len(encoded); i += 2 {
		key, ok := encoded[i].(string)
		value, v := encoded[i+1].(string)
		if ok && v {
			fields[key] = value
		}
	}
	var decoded SessionState
	if err := decodeSessionStateFields(fields, &decoded); err != nil || decoded.CompressionSourceSnapshot != original.CompressionSourceSnapshot {
		t.Fatalf("snapshot lost in Redis: %v", err)
	}
}

func TestSessionMetadataResetSurvivesRedisColdRead(t *testing.T) {
	backend := newFakeRedis()
	cache := NewSessionCache(backend, nil)
	ctx := context.Background()
	old := &SessionState{SchemaVersion: 1, HasCutMarker: true, CutCreatedAt: 123, CutSourceMsgs: 20, CutIndex: 10, CutStrategy: "mechanical_trim", SanitizeStats: SanitizeStats{PlaceholderCount: 1, SanitizedAt: 123}, SanitizeMapGeneration: "old", SanitizeMapRef: "old-ref", SanitizedSnapshot: MessageSnapshot{Hash: "old"}, CompressionSourceSnapshot: MessageSnapshot{Hash: "old"}, SanitizeMessageRefs: []SanitizedMessageRef{{RawHash: "old"}}}
	if err := cache.Set(ctx, "tenant", "gw_statclear01", old, []byte(`{"messages":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, "tenant", "gw_statclear01", &SessionState{SchemaVersion: 1}, []byte(`{"messages":[]}`)); err != nil {
		t.Fatal(err)
	}
	fresh := NewSessionCache(backend, nil)
	got, _, err := fresh.GetOrLoad(ctx, "tenant", "gw_statclear01")
	if err != nil || got == nil || got.HasCutMarker || got.CutIndex != 0 || got.CutSourceMsgs != 0 || got.CutStrategy != "" || got.SanitizeStats != (SanitizeStats{}) || got.SanitizeMapGeneration != "" || got.SanitizeMapRef != "" || !got.SanitizedSnapshot.IsZero() || !got.CompressionSourceSnapshot.IsZero() || len(got.SanitizeMessageRefs) > 0 {
		t.Fatalf("L2 revived reset metadata: %+v %v", got, err)
	}
}
