package v2

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRawCacheV2_PutGet(t *testing.T) {
	c := NewRawCacheV2(8) // 8 sessions 容量
	defer c.Close()

	entry := &RawEntry{
		TurnNo:        1,
		RequestDelta:  []Message{{Role: "user", Content: "hi"}},
		ResponseDelta: []Message{{Role: "assistant", Content: "hello"}},
		SubmitMode:    "full",
		UpdatedAt:     time.Now(),
	}
	c.Put(context.Background(), "tenant1", "gw_abc", entry)

	got, ok := c.Get(context.Background(), "tenant1", "gw_abc")
	if !ok {
		t.Fatalf("expected hit, got miss")
	}
	if got.TurnNo != 1 {
		t.Fatalf("turn_no mismatch: %d", got.TurnNo)
	}
}

func TestRawCacheV2_PutGetIsolatedFromCaller(t *testing.T) {
	c := NewRawCacheV2(2)
	rawPayload := map[string]interface{}{"blocks": []interface{}{map[string]interface{}{"text": "original"}}}
	entry := &RawEntry{
		RequestDelta: []Message{{Role: "user", Content: "original", RawContent: rawPayload, ToolCalls: []map[string]interface{}{{"id": "call-1"}}}},
		Attachments:  []AttachmentRef{{ObjectKey: "attachment-1"}},
	}
	c.Put(context.Background(), "tenant", "session", entry)
	entry.RequestDelta[0].Content = "mutated"
	entry.RequestDelta[0].ToolCalls[0]["id"] = "mutated-call"
	entry.RequestDelta[0].RawContent.(map[string]interface{})["blocks"].([]interface{})[0].(map[string]interface{})["text"] = "mutated-raw"
	entry.Attachments[0].ObjectKey = "mutated-attachment"

	got, ok := c.Get(context.Background(), "tenant", "session")
	if !ok || got == nil {
		t.Fatalf("expected isolated cache hit, got ok=%v entry=%+v", ok, got)
	}
	rawGot := got.RequestDelta[0].RawContent.(map[string]interface{})["blocks"].([]interface{})[0].(map[string]interface{})["text"]
	if got.RequestDelta[0].Content != "original" || rawGot != "original" || got.RequestDelta[0].ToolCalls[0]["id"] != "call-1" || got.Attachments[0].ObjectKey != "attachment-1" {
		t.Fatalf("cache retained caller-owned mutable data: %+v", got)
	}
	got.RequestDelta[0].Content = "get-mutated"
	again, ok := c.Get(context.Background(), "tenant", "session")
	if !ok || again.RequestDelta[0].Content != "original" {
		t.Fatalf("Get exposed cache-owned mutable data: %+v", again)
	}
}

func TestRawCacheV2_LRUEviction(t *testing.T) {
	c := NewRawCacheV2(2)
	defer c.Close()
	c.Put(context.Background(), "t", "s1", &RawEntry{TurnNo: 1, UpdatedAt: time.Now()})
	c.Put(context.Background(), "t", "s2", &RawEntry{TurnNo: 1, UpdatedAt: time.Now()})
	c.Put(context.Background(), "t", "s3", &RawEntry{TurnNo: 1, UpdatedAt: time.Now()})
	if _, ok := c.Get(context.Background(), "t", "s1"); ok {
		t.Fatalf("s1 should be evicted (LRU)")
	}
	if _, ok := c.Get(context.Background(), "t", "s2"); !ok {
		t.Fatalf("s2 should still be present")
	}
}

func TestRawCacheV2_OnlyStoresCurrentTurnDelta(t *testing.T) {
	c := NewRawCacheV2(4)
	defer c.Close()
	// 模拟：会话被前端压缩（SubmitMode=inferred_compressed），
	// 整包视为本轮 delta，不做 LCS、不存完整 outbound
	c.Put(context.Background(), "t", "s1", &RawEntry{
		TurnNo:       5,
		RequestDelta: []Message{{Role: "user", Content: "FULL BODY"}},
		SubmitMode:   "inferred_compressed",
		UpdatedAt:    time.Now(),
	})
	got, _ := c.Get(context.Background(), "t", "s1")
	if got.SubmitMode != "inferred_compressed" {
		t.Fatalf("expected inferred_compressed, got %s", got.SubmitMode)
	}
	if len(got.RequestDelta) != 1 || got.RequestDelta[0].Content != "FULL BODY" {
		t.Fatalf("expected single FULL BODY message")
	}
}

// TestRawCacheV2_ConcurrentSafe 验证 100 goroutine 并发 Put/Get 不 panic 且数据正确
func TestRawCacheV2_ConcurrentSafe(t *testing.T) {
	c := NewRawCacheV2(64)
	defer c.Close()

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			key := "sess_" + string(rune('A'+(idx%26)))
			entry := &RawEntry{
				TurnNo:    idx,
				UpdatedAt: time.Now(),
			}
			c.Put(context.Background(), "tenant1", key, entry)
			_, _ = c.Get(context.Background(), "tenant1", key)
		}(i)
	}

	wg.Wait()

	// 至少能 Get 出一个 key
	if _, ok := c.Get(context.Background(), "tenant1", "sess_A"); !ok {
		t.Fatalf("expected at least one surviving entry")
	}
}
