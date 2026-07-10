package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
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

	// Seed a real request so the main + vendor lanes register activity.
	seedTs := time.Now().UTC()
	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-seed",
		Ts:            seedTs.Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}); err != nil {
		t.Fatalf("Record seed: %v", err)
	}

	// Force every activity key into the past so lanes are considered idle.
	staleUnix := seedTs.Unix() - idleThresholdSeconds - 5
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	emitTs := time.Now().UTC()
	if err := store.ScanAndRecordIdleMarkers(ctx, emitTs); err != nil {
		t.Fatalf("ScanAndRecordIdleMarkers: %v", err)
	}

	// The vendor lane should now carry an idle marker that reuses the
	// vendor identity (openai), NOT a synthetic __idle__ lane.
	items, err := store.Replay(ctx, "", true, 50)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	foundIdle := false
	for _, it := range items {
		if it.Type == "idle_marker" {
			foundIdle = true
			if it.RequestID == "" {
				t.Fatal("idle marker should have a stable request_id for frontend keys")
			}
		}
	}
	if !foundIdle {
		t.Fatalf("expected at least one idle marker after idle threshold, got %#v", items)
	}

	s := BuildLiveStreamSnapshot(items)
	if s.Summary.Total != 1 {
		t.Fatalf("idle markers should not count as real request summary, got %#v (items=%#v)", s.Summary, items)
	}
}

func TestLiveStreamRedisStore_NilClient(t *testing.T) {
	store := NewLiveStreamRedisStore(nil)
	ctx := context.Background()

	// Should not panic, all operations are no-ops
	if err := store.Record(ctx, LiveRequest{RequestID: "test"}); err != nil {
		t.Errorf("Record with nil client should return nil, got %v", err)
	}
	if err := store.ScanAndRecordIdleMarkers(ctx, time.Now()); err != nil {
		t.Errorf("ScanAndRecordIdleMarkers with nil client should return nil, got %v", err)
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

func TestLiveStreamRedisStore_IdleMarkerWritesMainQueue(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()
	seedTs := time.Now().UTC()
	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-1",
		Ts:            seedTs.Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Force activity keys stale so the vendor/provider/model/main lanes emit idle markers.
	staleUnix := seedTs.Unix() - idleThresholdSeconds - 5
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	if err := store.ScanAndRecordIdleMarkers(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("ScanAndRecordIdleMarkers: %v", err)
	}

	// Idle markers MUST land in the main queue (the only queue Replay reads),
	// otherwise they were invisible dead data. Verify the tenant-scoped main
	// queue now holds req-1 plus the idle markers.
	tenantMain := tenantLiveStreamKey("tenant-a", "main")
	members, err := rdb.ZRange(ctx, tenantMain, 0, -1).Result()
	if err != nil {
		t.Fatalf("ZRange tenant main: %v", err)
	}
	// req-1 + one idle marker per idle dimension (vendor, provider, model, main).
	if len(members) < 2 {
		t.Fatalf("expected tenant main queue to contain req-1 + idle markers, got %d: %#v", len(members), members)
	}

	// Resolve every idle marker from the tenant main queue and assert each
	// carries ONLY its own dimension's identity.
	var vendorIdle, providerIdle, modelIdle *LiveRequest
	for _, m := range members {
		if m == "req-1" {
			continue
		}
		detail, gErr := rdb.Get(ctx, liveStreamRequestDetailKey("tenant-a", m)).Result()
		if gErr != nil {
			continue
		}
		req, uErr := unmarshalLiveRequestRedisPayload(detail)
		if uErr != nil {
			continue
		}
		if req.Type != "idle_marker" {
			continue
		}
		switch {
		case req.ModelCategory == "openai" && req.ProviderCode == "" && req.Model == "":
			vendorIdle = &req
		case req.ProviderCode == "openai" && req.ModelCategory == "" && req.Model == "":
			providerIdle = &req
		case req.Model == "gpt-4o" && req.ModelCategory == "" && req.ProviderCode == "":
			modelIdle = &req
		}
	}
	if vendorIdle == nil {
		t.Fatalf("expected a vendor-scoped idle marker (ModelCategory=openai only), got members %#v", members)
	}
	if providerIdle == nil {
		t.Fatalf("expected a provider-scoped idle marker (ProviderCode=openai only), got members %#v", members)
	}
	if modelIdle == nil {
		t.Fatalf("expected a model-scoped idle marker (Model=gpt-4o only), got members %#v", members)
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
	// After removing Others aggregation, all 7 vendors should be returned
	if len(lanes) != 7 {
		t.Fatalf("expected all 7 vendor lanes (no Others aggregation), got %d", len(lanes))
	}
	// Verify no synthetic others lane exists
	for _, lane := range lanes {
		if lane.ID == "__others__" || lane.IsOthers {
			t.Fatalf("should not have synthetic others lane after removing aggregation: %#v", lane)
		}
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

func TestLiveStreamSSEHub_EvictStaleCachedSnapshots(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	hub.cfg.CachedSnapshotTTL = 50 * time.Millisecond

	// Seed two tenants with fresh and stale entries.
	hub.cachedSnapshotMu.Lock()
	hub.cachedSnapshot["tenant-fresh"] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: time.Now(),
	}
	hub.cachedSnapshot["tenant-stale"] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: time.Now().Add(-1 * time.Hour),
	}
	hub.cachedSnapshotMu.Unlock()

	hub.evictStaleCachedSnapshots()

	hub.cachedSnapshotMu.RLock()
	defer hub.cachedSnapshotMu.RUnlock()
	if _, ok := hub.cachedSnapshot["tenant-fresh"]; !ok {
		t.Fatal("fresh entry should not be evicted")
	}
	if _, ok := hub.cachedSnapshot["tenant-stale"]; ok {
		t.Fatal("stale entry should be evicted")
	}
}

// TestLiveStreamSSEHub_ConfigDefaults verifies that LiveStreamConfig{}
// (zero-value) and partial overrides resolve to safe defaults and that
// cleanup interval follows TTL when not explicitly set.
//
// Added 2026-07-09 alongside the live-stream-cache-evict-stall fix
// (computeScopeDelta enter-and-refresh + tunable TTL via env).
func TestLiveStreamSSEHub_ConfigDefaults(t *testing.T) {
	t.Run("zero_value_yields_10min_defaults", func(t *testing.T) {
		hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
		if hub.cfg.CachedSnapshotTTL != 10*time.Minute {
			t.Fatalf("expected CachedSnapshotTTL=10m, got %s", hub.cfg.CachedSnapshotTTL)
		}
		if hub.cfg.CachedSnapshotCleanupInterval != 10*time.Minute {
			t.Fatalf("expected CachedSnapshotCleanupInterval=10m when zero, got %s",
				hub.cfg.CachedSnapshotCleanupInterval)
		}
	})

	t.Run("ttl_override_only_follows_cleanup_interval", func(t *testing.T) {
		hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
			CachedSnapshotTTL: 5 * time.Minute,
		})
		if hub.cfg.CachedSnapshotTTL != 5*time.Minute {
			t.Fatalf("expected TTL=5m, got %s", hub.cfg.CachedSnapshotTTL)
		}
		// cleanup interval unconfigured → defaults to TTL → 5min, NOT 10min
		if hub.cfg.CachedSnapshotCleanupInterval != 5*time.Minute {
			t.Fatalf("cleanup interval should follow TTL=5m, got %s",
				hub.cfg.CachedSnapshotCleanupInterval)
		}
	})

	t.Run("cleanup_interval_independently_overridable", func(t *testing.T) {
		hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
			CachedSnapshotTTL:             30 * time.Minute,
			CachedSnapshotCleanupInterval: 1 * time.Minute,
		})
		if hub.cfg.CachedSnapshotTTL != 30*time.Minute {
			t.Fatalf("expected TTL=30m, got %s", hub.cfg.CachedSnapshotTTL)
		}
		if hub.cfg.CachedSnapshotCleanupInterval != 1*time.Minute {
			t.Fatalf("expected cleanup=1m, got %s",
				hub.cfg.CachedSnapshotCleanupInterval)
		}
	})
}

// TestLiveStreamSSEHub_ComputeScopeDelta_RefreshesAccessOnEmpty is the
// regression test for the live-stream-cache-evict-stall fix.
//
// Before the fix, an empty snapshot returned from Redis (Summary.Total==0)
// caused computeScopeDelta to early-return *without* touching lastAccessed.
// Over a 10-minute idle window, evictStaleCachedSnapshots would then
// remove the entry, and the next non-empty replay would "look empty"
// from the dashboard's perspective → the user-reported
// "queues disappear, come back on refresh" symptom.
//
// After the fix, lastAccessed is refreshed on every entry, regardless of
// snapshot outcome, so an actively subscribed tenant's cache cannot be
// starved by transient empty reads.
func TestLiveStreamSSEHub_ComputeScopeDelta_RefreshesAccessOnEmpty(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
		RedisClient: rdb,
	})
	hub.cfg.CachedSnapshotTTL = 10 * time.Minute

	// Seed a stale-soon entry: lastAccessed = 9 minutes ago. Without
	// the fix, one more evict tick (10min) would delete this entry.
	staleBefore := time.Now().Add(-9 * time.Minute)
	hub.cachedSnapshotMu.Lock()
	hub.cachedSnapshot["tenant-active"] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: staleBefore,
	}
	hub.cachedSnapshotMu.Unlock()

	beforeHits := atomic.LoadInt64(&hub.cachedSnapshotHits)
	beforeMisses := atomic.LoadInt64(&hub.cachedSnapshotMisses)
	beforeEmptySkips := atomic.LoadInt64(&hub.cachedSnapshotEmptySkips)
	beforeEvictions := atomic.LoadInt64(&hub.cachedSnapshotEvictions)

	// Trigger computeScopeDelta against the tenant. miniredis has no
	// data → Snapshot returns non-nil with Summary.Total==0 → early
	// return path exercised.
	delta := hub.computeScopeDelta(context.Background(), "tenant-active", false)
	if delta != nil {
		t.Fatalf("expected nil delta on empty snapshot, got %v", delta)
	}

	// (1) lastAccessed must be refreshed even though delta is nil.
	hub.cachedSnapshotMu.RLock()
	entry := hub.cachedSnapshot["tenant-active"]
	hub.cachedSnapshotMu.RUnlock()
	if entry == nil {
		t.Fatal("entry should still exist after empty snapshot")
	}
	if !entry.lastAccessed.After(staleBefore) {
		t.Fatalf("lastAccessed should be refreshed; was %v now %v",
			staleBefore, entry.lastAccessed)
	}
	if elapsed := time.Since(entry.lastAccessed); elapsed > 2*time.Second {
		t.Fatalf("lastAccessed not refreshed to recent time (now - lastAccessed = %v)", elapsed)
	}

	// (2) counter increments match the touched-on-entry semantics.
	if got := atomic.LoadInt64(&hub.cachedSnapshotHits); got != beforeHits+1 {
		t.Errorf("expected cachedSnapshotHits++ (was %d, now %d)", beforeHits, got)
	}
	if got := atomic.LoadInt64(&hub.cachedSnapshotMisses); got != beforeMisses {
		t.Errorf("cachedSnapshotMisses should not increment on hit (was %d, now %d)", beforeMisses, got)
	}
	if got := atomic.LoadInt64(&hub.cachedSnapshotEmptySkips); got != beforeEmptySkips+1 {
		t.Errorf("expected cachedSnapshotEmptySkips++ (was %d, now %d)", beforeEmptySkips, got)
	}
	if got := atomic.LoadInt64(&hub.cachedSnapshotEvictions); got != beforeEvictions {
		t.Errorf("evictions should not change in computeScopeDelta (was %d, now %d)",
			beforeEvictions, got)
	}

	// (3) Same call on a tenant with NO existing entry → miss counter,
	//     no entry created (intentional: avoid unbounded growth).
	delta = hub.computeScopeDelta(context.Background(), "tenant-new", false)
	if delta != nil {
		t.Fatalf("expected nil delta for new tenant, got %v", delta)
	}
	hub.cachedSnapshotMu.RLock()
	_, exists := hub.cachedSnapshot["tenant-new"]
	hub.cachedSnapshotMu.RUnlock()
	if exists {
		t.Fatal("computeScopeDelta should NOT create empty entries for unseen tenants")
	}
	if got := atomic.LoadInt64(&hub.cachedSnapshotMisses); got != beforeMisses+1 {
		t.Errorf("expected miss++ (was %d, now %d)", beforeMisses, got)
	}

	// (4) After multiple computeScopeDelta calls refreshing lastAccessed,
	//     evictStaleCachedSnapshots must NOT remove the active tenant
	//     even though it was "about to expire" at seed time.
	hub.evictStaleCachedSnapshots()
	hub.cachedSnapshotMu.RLock()
	_, stillThere := hub.cachedSnapshot["tenant-active"]
	hub.cachedSnapshotMu.RUnlock()
	if !stillThere {
		t.Fatal("active tenant cache should survive evict after refresh-on-enter fix")
	}
	if got := atomic.LoadInt64(&hub.cachedSnapshotEvictions); got != beforeEvictions {
		t.Errorf("evictions should still be unchanged (was %d, now %d)",
			beforeEvictions, got)
	}
}

// TestLiveStreamSSEHub_EvictStaleAfterRefactor verifies the new eviction
// counter increments by exactly the number of removed entries, and that
// the fresh entry (refreshed during test) survives.
//
// Companion to TestLiveStreamSSEHub_EvictStaleCachedSnapshots; the
// original assertion (fresh/stale semantics) still holds.
func TestLiveStreamSSEHub_EvictStaleAfterRefactor(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	hub.cfg.CachedSnapshotTTL = 50 * time.Millisecond

	hub.cachedSnapshotMu.Lock()
	hub.cachedSnapshot["fresh"] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: time.Now(),
	}
	hub.cachedSnapshot["stale-a"] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: time.Now().Add(-1 * time.Hour),
	}
	hub.cachedSnapshot["stale-b"] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: time.Now().Add(-2 * time.Hour),
	}
	hub.cachedSnapshotMu.Unlock()

	before := atomic.LoadInt64(&hub.cachedSnapshotEvictions)
	hub.evictStaleCachedSnapshots()
	after := atomic.LoadInt64(&hub.cachedSnapshotEvictions)

	if after-before != 2 {
		t.Fatalf("expected evictions to grow by 2 (was %d, now %d)", before, after)
	}

	hub.cachedSnapshotMu.RLock()
	defer hub.cachedSnapshotMu.RUnlock()
	if _, ok := hub.cachedSnapshot["fresh"]; !ok {
		t.Fatal("fresh entry should survive")
	}
	if _, ok := hub.cachedSnapshot["stale-a"]; ok {
		t.Fatal("stale-a should be evicted")
	}
	if _, ok := hub.cachedSnapshot["stale-b"]; ok {
		t.Fatal("stale-b should be evicted")
	}
}

// TestParseActivityKey is a regression test for the scope-parsing bug
// where a tenant-scoped activity key was mis-decoded (the "tenant:"
// prefix check was applied to a segment that never carried the prefix,
// dropping the tenantID and emitting idle markers into the wrong scope).
func TestParseActivityKey(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		wantTenant string
		wantDim  string
		wantDimKey string
	}{
		{"global vendor", liveStreamActivityKey("", "vendor", "openai"), "", "vendor", "openai"},
		{"tenant vendor", liveStreamActivityKey("tenant-a", "vendor", "openai"), "tenant-a", "vendor", "openai"},
		{"global main", liveStreamActivityKey("", "main", ""), "", "main", ""},
		{"tenant main", liveStreamActivityKey("tenant-a", "main", ""), "tenant-a", "main", ""},
		{"tenant dim key with colon", liveStreamActivityKey("tenant-a", "provider", "acme:co"), "tenant-a", "provider", "acme:co"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, ok := parseActivityKey(c.key)
			if !ok {
				t.Fatalf("parseActivityKey(%q) returned ok=false", c.key)
			}
			if info.tenantID != c.wantTenant {
				t.Errorf("tenantID = %q, want %q", info.tenantID, c.wantTenant)
			}
			if info.dimension != c.wantDim {
				t.Errorf("dimension = %q, want %q", info.dimension, c.wantDim)
			}
			if info.dimensionKey != c.wantDimKey {
				t.Errorf("dimensionKey = %q, want %q", info.dimensionKey, c.wantDimKey)
			}
		})
	}
}
