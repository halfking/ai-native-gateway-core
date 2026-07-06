package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLiveStreamRedisStore_RecordAndReplay(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	req1 := LiveRequest{
		RequestID:     "req-1",
		Ts:            time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}
	req2 := LiveRequest{
		RequestID:     "req-2",
		Ts:            time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "claude-3-5-sonnet",
		ModelCategory: "anthropic",
		ProviderCode:  "anthropic",
		Status:        "failure",
	}

	if err := store.Record(ctx, req1); err != nil {
		t.Fatalf("Record req1: %v", err)
	}
	if err := store.Record(ctx, req2); err != nil {
		t.Fatalf("Record req2: %v", err)
	}

	// Replay (super admin, no tenant filter)
	items, err := store.Replay(ctx, "", true, 50)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// Should be ASC order (oldest first)
	if items[0].RequestID != "req-1" {
		t.Errorf("first item should be req-1, got %s", items[0].RequestID)
	}
	if items[1].RequestID != "req-2" {
		t.Errorf("second item should be req-2, got %s", items[1].RequestID)
	}

	// Replay with tenant filter
	items, err = store.Replay(ctx, "tenant-a", false, 50)
	if err != nil {
		t.Fatalf("Replay tenant-a: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items for tenant-a, got %d", len(items))
	}

	// Replay with wrong tenant
	items, err = store.Replay(ctx, "tenant-b", false, 50)
	if err != nil {
		t.Fatalf("Replay tenant-b: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items for tenant-b, got %d", len(items))
	}
}

func TestLiveStreamRedisStore_IdleMarker(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := store.RecordIdleMarker(ctx, now); err != nil {
		t.Fatalf("RecordIdleMarker: %v", err)
	}

	items, err := store.Replay(ctx, "", true, 50)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 idle marker, got %d items", len(items))
	}
	if items[0].Type != "idle_marker" {
		t.Errorf("expected idle_marker type, got %s", items[0].Type)
	}
	if items[0].RequestID == "" {
		t.Fatal("idle marker should have a stable request_id for frontend keys")
	}

	s := BuildLiveStreamSnapshot(items)
	if len(s.Dimensions["vendor"]) != 1 || s.Dimensions["vendor"][0].ID != "__idle__" {
		t.Fatalf("idle marker should be represented in vendor dimensions, got %#v", s.Dimensions["vendor"])
	}
	if s.Summary.Total != 0 {
		t.Fatalf("idle marker should not count as a real request summary, got %#v", s.Summary)
	}
}

func TestLiveStreamRedisStore_NilClient(t *testing.T) {
	store := NewLiveStreamRedisStore(nil)
	ctx := context.Background()

	// Should not panic, all operations are no-ops
	if err := store.Record(ctx, LiveRequest{RequestID: "test"}); err != nil {
		t.Errorf("Record with nil client should return nil, got %v", err)
	}
	if err := store.RecordIdleMarker(ctx, time.Now()); err != nil {
		t.Errorf("RecordIdleMarker with nil client should return nil, got %v", err)
	}
	items, err := store.Replay(ctx, "", true, 50)
	if err != nil {
		t.Errorf("Replay with nil client should return nil error, got %v", err)
	}
	if items != nil {
		t.Errorf("Replay with nil client should return nil slice, got %v", items)
	}
}

func TestLiveRequestRedisPayload_OnlyObservationFields(t *testing.T) {
	errKind := "upstream_5xx"
	latency := 123
	req := LiveRequest{
		Type:         "request",
		RequestID:    "req-compact",
		Ts:           time.Now().UTC().Format(time.RFC3339),
		TenantID:     "tenant-a",
		GwSessionID:  "gw-session-id-only",
		Model:        "gpt-4o",
		ProviderCode: "openai",
		Status:       "failure",
		LatencyMs:    &latency,
		ErrorKind:    &errKind,
	}

	data, err := marshalLiveRequestRedisPayload(req)
	if err != nil {
		t.Fatalf("marshalLiveRequestRedisPayload: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		t.Fatalf("unmarshal raw payload: %v", err)
	}
	allowed := map[string]struct{}{
		"type": {}, "request_id": {}, "ts": {}, "tenant_id": {}, "gw_session_id": {},
		"model": {}, "model_category": {}, "provider_code": {}, "status": {},
		"latency_ms": {}, "prompt_tokens": {}, "completion_tokens": {}, "total_tokens": {},
		"cost_usd": {}, "error_kind": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("redis payload contains non-observation field %q in %s", key, data)
		}
	}
	for _, forbidden := range []string{"messages", "request_body", "response_body", "outbound_body", "attachments", "session"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("redis payload must not contain %q", forbidden)
		}
	}
}

func TestLiveStreamRedisStore_DimensionQueues(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	req := LiveRequest{
		RequestID:     "req-dim",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai-official",
		Status:        "success",
	}

	if err := store.Record(ctx, req); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Verify dimension keys exist
	vendorKey := "llmgw:live:dim:vendor:openai"
	count, err := rdb.ZCard(ctx, vendorKey).Result()
	if err != nil {
		t.Fatalf("ZCard vendor: %v", err)
	}
	if count != 1 {
		t.Errorf("vendor queue should have 1 item, got %d", count)
	}

	providerKey := "llmgw:live:dim:provider:openai-official"
	count, err = rdb.ZCard(ctx, providerKey).Result()
	if err != nil {
		t.Fatalf("ZCard provider: %v", err)
	}
	if count != 1 {
		t.Errorf("provider queue should have 1 item, got %d", count)
	}

	modelKey := "llmgw:live:dim:model:gpt-4o"
	count, err = rdb.ZCard(ctx, modelKey).Result()
	if err != nil {
		t.Fatalf("ZCard model: %v", err)
	}
	if count != 1 {
		t.Errorf("model queue should have 1 item, got %d", count)
	}
}

func TestLiveStreamRedisStore_DimensionQueuesKeepSmallRawDimensions(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		vendor := "vendor-" + string(rune('a'+i))
		req := LiveRequest{
			RequestID:     string(rune('a' + i)),
			Ts:            time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			TenantID:      "tenant-a",
			Model:         "model-" + string(rune('a'+i)),
			ModelCategory: vendor,
			ProviderCode:  "provider-" + string(rune('a'+i)),
			Status:        "success",
		}
		if err := store.Record(ctx, req); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	for i := 0; i < 7; i++ {
		vendor := "vendor-" + string(rune('a'+i))
		globalKey := "llmgw:live:dim:vendor:" + vendor
		tenantKey := tenantLiveStreamKey("tenant-a", "dim:vendor:"+vendor)
		for _, key := range []string{globalKey, tenantKey} {
			count, err := rdb.ZCard(ctx, key).Result()
			if err != nil {
				t.Fatalf("ZCard %s: %v", key, err)
			}
			if count != 1 {
				t.Fatalf("expected raw dimension key %s to retain 1 item, got %d", key, count)
			}
		}
	}
	if count, err := rdb.ZCard(ctx, tenantLiveStreamKey("tenant-a", "dim:vendor:__others__")).Result(); err != nil {
		t.Fatalf("ZCard synthetic others: %v", err)
	} else if count != 0 {
		t.Fatalf("redis should not persist synthetic others as a raw dimension queue, got %d", count)
	}
}

func TestLiveStreamRedisStore_StatusTransitionReplacesRequestID(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()
	start := LiveRequest{
		RequestID:     "req-transition",
		Ts:            time.Now().UTC().Add(-time.Second).Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "in_progress",
	}
	done := start
	done.Ts = time.Now().UTC().Format(time.RFC3339)
	done.Status = "success"

	if err := store.Record(ctx, start); err != nil {
		t.Fatalf("Record start: %v", err)
	}
	if err := store.Record(ctx, done); err != nil {
		t.Fatalf("Record done: %v", err)
	}

	for key, want := range map[string]int64{
		tenantLiveStreamKey("tenant-a", "main"):               1,
		tenantLiveStreamKey("tenant-a", "status:in_progress"): 0,
		tenantLiveStreamKey("tenant-a", "status:success"):     1,
		liveStreamStatPrefix + "in_progress":                  0,
		liveStreamStatPrefix + "success":                      1,
	} {
		got, err := rdb.ZCard(ctx, key).Result()
		if err != nil {
			t.Fatalf("ZCard %s: %v", key, err)
		}
		if got != want {
			t.Fatalf("ZCard %s = %d, want %d", key, got, want)
		}
	}

	items, err := store.Replay(ctx, "tenant-a", false, 10)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one request after transition, got %#v", items)
	}
	if items[0].RequestID != "req-transition" || items[0].Status != "success" {
		t.Fatalf("expected latest success state, got %#v", items[0])
	}
	ss := BuildLiveStreamSnapshot(items)
	if ss.Summary.Total != 1 || ss.Summary.Success != 1 || ss.Summary.InProgress != 0 {
		t.Fatalf("unexpected snapshot summary after transition: %#v", ss.Summary)
	}
}

func TestLiveStreamRedisStore_IdleMarkerWritesTenantDimensionQueues(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()
	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-1",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.RecordIdleMarker(ctx, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("RecordIdleMarker: %v", err)
	}

	for _, key := range []string{
		tenantLiveStreamKey("tenant-a", "status:idle"),
		tenantLiveStreamKey("tenant-a", "dim:vendor:__idle__"),
		tenantLiveStreamKey("tenant-a", "dim:provider:系统心跳"),
		tenantLiveStreamKey("tenant-a", "dim:model:空闲"),
	} {
		count, err := rdb.ZCard(ctx, key).Result()
		if err != nil {
			t.Fatalf("ZCard %s: %v", key, err)
		}
		if count != 1 {
			t.Fatalf("expected idle queue %s to contain 1 marker, got %d", key, count)
		}
	}

	items, err := store.Replay(ctx, "tenant-a", false, 10)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	ss := BuildLiveStreamSnapshot(items)
	if len(ss.DetailDimensions["vendor"]) != 2 {
		t.Fatalf("detail dimensions should include request vendor and idle vendor, got %#v", ss.DetailDimensions["vendor"])
	}
}

func TestComputeDelta_ReturnsAllLanesWhenOldIsNil(t *testing.T) {
	snapshot := &LiveStreamSnapshot{
		Summary:       LiveStreamStats{Total: 5, Success: 3, Failure: 2},
		Dimensions:    map[string][]LiveStreamLane{"vendor": {{ID: "openai"}}, "provider": {}, "model": {}},
		StatusLegends: []LiveStreamLegendItem{{Key: "success", Name: "success", Count: 3}},
	}
	delta := ComputeDelta(nil, snapshot)
	if delta.Summary != snapshot.Summary {
		t.Fatalf("expected full summary, got %#v", delta.Summary)
	}
	if len(delta.ChangedLanes["vendor"]) != 1 {
		t.Fatalf("expected all vendor lanes, got %#v", delta.ChangedLanes)
	}
}

func TestComputeDelta_OmitsUnchangedDimensions(t *testing.T) {
	lane := []LiveStreamLane{{ID: "openai", Stats: LiveStreamStats{Total: 3, Success: 2, Failure: 1}}}
	old := &LiveStreamSnapshot{
		Summary:    LiveStreamStats{Total: 3},
		Dimensions: map[string][]LiveStreamLane{"vendor": lane, "provider": nil, "model": nil},
	}
	unchanged := &LiveStreamSnapshot{
		Summary:    LiveStreamStats{Total: 4},
		Dimensions: map[string][]LiveStreamLane{"vendor": lane, "provider": nil, "model": nil},
	}
	delta := ComputeDelta(old, unchanged)
	if delta.Summary.Total != 4 {
		t.Fatalf("expected updated summary, got %#v", delta.Summary)
	}
	if _, ok := delta.ChangedLanes["vendor"]; ok {
		t.Fatalf("expected vendor to be omitted when unchanged, got %#v", delta.ChangedLanes)
	}
}

func TestComputeDelta_IncludesChangedDimension(t *testing.T) {
	old := &LiveStreamSnapshot{
		Summary:    LiveStreamStats{Total: 3},
		Dimensions: map[string][]LiveStreamLane{"vendor": {{ID: "openai", Stats: LiveStreamStats{Total: 3}}}, "provider": nil, "model": nil},
	}
	updated := &LiveStreamSnapshot{
		Summary:    LiveStreamStats{Total: 4},
		Dimensions: map[string][]LiveStreamLane{"vendor": {{ID: "openai", Stats: LiveStreamStats{Total: 4}}}, "provider": nil, "model": nil},
	}
	delta := ComputeDelta(old, updated)
	if _, ok := delta.ChangedLanes["vendor"]; !ok {
		t.Fatalf("expected vendor to be included when changed, got %#v", delta.ChangedLanes)
	}
}

func TestComputeDelta_TenantIsolation(t *testing.T) {
	tenantA := &LiveStreamSnapshot{
		Summary:    LiveStreamStats{Total: 3, Success: 2, Failure: 1},
		Dimensions: map[string][]LiveStreamLane{"vendor": {{ID: "openai", Stats: LiveStreamStats{Total: 3}}}, "provider": nil, "model": nil},
	}
	tenantB := &LiveStreamSnapshot{
		Summary:    LiveStreamStats{Total: 5, Success: 4, Failure: 1},
		Dimensions: map[string][]LiveStreamLane{"vendor": {{ID: "anthropic", Stats: LiveStreamStats{Total: 5}}}, "provider": nil, "model": nil},
	}
	// Delta from nil (first time) should return full tenantA
	deltaA := ComputeDelta(nil, tenantA)
	if deltaA.Summary.Total != 3 {
		t.Fatalf("expected tenantA summary, got %#v", deltaA.Summary)
	}
	// Delta from tenantA to tenantB should return full tenantB (different tenants)
	deltaB := ComputeDelta(tenantA, tenantB)
	if deltaB.Summary.Total != 5 {
		t.Fatalf("expected tenantB summary, got %#v", deltaB.Summary)
	}
	if len(deltaB.ChangedLanes["vendor"]) != 1 || deltaB.ChangedLanes["vendor"][0].ID != "anthropic" {
		t.Fatalf("expected anthropic vendor, got %#v", deltaB.ChangedLanes)
	}
}

func TestBuildLiveStreamSnapshot_ServerSideAggregation(t *testing.T) {
	items := []LiveRequest{
		{RequestID: "1", Ts: "2026-07-06T00:00:01Z", TenantID: "t1", Model: "gpt-4o", ModelCategory: "openai", ProviderCode: "openai", Status: "success"},
		{RequestID: "2", Ts: "2026-07-06T00:00:02Z", TenantID: "t1", Model: "gpt-4o", ModelCategory: "openai", ProviderCode: "openai", Status: "failure"},
		{RequestID: "3", Ts: "2026-07-06T00:00:03Z", TenantID: "t1", Model: "claude", ModelCategory: "anthropic", ProviderCode: "anthropic", Status: "in_progress"},
	}

	s := BuildLiveStreamSnapshot(items)
	if s.Summary.Total != 3 || s.Summary.Success != 1 || s.Summary.Failure != 1 || s.Summary.InProgress != 1 {
		t.Fatalf("unexpected summary: %#v", s.Summary)
	}
	if len(s.Dimensions["vendor"]) != 2 {
		t.Fatalf("expected 2 vendor lanes, got %d", len(s.Dimensions["vendor"]))
	}
	if s.Dimensions["vendor"][0].ID != "openai" {
		t.Fatalf("top vendor should be openai, got %s", s.Dimensions["vendor"][0].ID)
	}
	if s.Dimensions["vendor"][0].Stats.Total != 2 {
		t.Fatalf("openai lane total should be 2, got %d", s.Dimensions["vendor"][0].Stats.Total)
	}
	if len(s.Dimensions["provider"][0].Requests) == 0 {
		t.Fatal("provider lane should include render-ready requests")
	}
}

func TestBuildLiveStreamSnapshot_TopNOthers(t *testing.T) {
	items := make([]LiveRequest, 0, 7)
	for i := 0; i < 7; i++ {
		items = append(items, LiveRequest{
			RequestID:     string(rune('a' + i)),
			Ts:            time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			TenantID:      "t1",
			Model:         "m",
			ModelCategory: "vendor-" + string(rune('a'+i)),
			ProviderCode:  "p",
			Status:        "success",
		})
	}

	s := BuildLiveStreamSnapshot(items)
	lanes := s.Dimensions["vendor"]
	if len(lanes) != 6 {
		t.Fatalf("expected 5 top lanes + others, got %d", len(lanes))
	}
	last := lanes[len(lanes)-1]
	if last.ID != "__others__" || !last.IsOthers || last.Stats.Total != 2 {
		t.Fatalf("unexpected others lane: %#v", last)
	}
	if len(s.DetailDimensions["vendor"]) != 7 {
		t.Fatalf("detail dimensions must retain all 7 vendors, got %d", len(s.DetailDimensions["vendor"]))
	}
	for _, lane := range s.DetailDimensions["vendor"] {
		if lane.ID == "__others__" || lane.IsOthers {
			t.Fatalf("detail dimensions must not contain synthetic others lane: %#v", lane)
		}
	}
}

func TestBuildLiveStreamSnapshot_NoOthersWhenFiveOrFewer(t *testing.T) {
	items := make([]LiveRequest, 0, 5)
	for i := 0; i < 5; i++ {
		items = append(items, LiveRequest{
			RequestID:     string(rune('a' + i)),
			Ts:            time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			TenantID:      "t1",
			Model:         "m",
			ModelCategory: "vendor-" + string(rune('a'+i)),
			ProviderCode:  "p",
			Status:        "success",
		})
	}

	s := BuildLiveStreamSnapshot(items)
	lanes := s.Dimensions["vendor"]
	if len(lanes) != 5 {
		t.Fatalf("expected exactly 5 lanes and no others, got %d", len(lanes))
	}
	for _, lane := range lanes {
		if lane.ID == "__others__" || lane.IsOthers {
			t.Fatalf("did not expect others lane when dimension count <= 5: %#v", lane)
		}
	}
	if len(s.DetailDimensions["vendor"]) != 5 {
		t.Fatalf("detail dimensions should contain 5 raw vendors, got %d", len(s.DetailDimensions["vendor"]))
	}
}

func TestLiveStreamRedisStore_TenantScopedReplay(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()
	for _, req := range []LiveRequest{
		{RequestID: "a", Ts: time.Now().UTC().Format(time.RFC3339), TenantID: "tenant-a", Model: "gpt", ModelCategory: "openai", ProviderCode: "openai", Status: "success"},
		{RequestID: "b", Ts: time.Now().UTC().Format(time.RFC3339), TenantID: "tenant-b", Model: "claude", ModelCategory: "anthropic", ProviderCode: "anthropic", Status: "success"},
	} {
		if err := store.Record(ctx, req); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	items, err := store.Replay(ctx, "tenant-a", false, 10)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(items) != 1 || items[0].RequestID != "a" {
		t.Fatalf("tenant-a replay should only see request a, got %#v", items)
	}

	ss, err := store.Snapshot(ctx, "tenant-b", false, 10)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if ss.Summary.Total != 1 || ss.Dimensions["vendor"][0].ID != "anthropic" {
		t.Fatalf("tenant-b snapshot should be isolated, got %#v", ss)
	}
}
