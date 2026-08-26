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
	"github.com/stretchr/testify/assert"
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

	if err := store.Record(ctx, req1, ""); err != nil {
		t.Fatalf("Record req1: %v", err)
	}
	if err := store.Record(ctx, req2, ""); err != nil {
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
	}, ""); err != nil {
		t.Fatalf("Record seed: %v", err)
	}

	// Force every activity key into the past so lanes are considered idle.
	staleUnix := seedTs.Unix() - int64(LiveStreamLaneRetention.Seconds()) - 5
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	emitTs := time.Now().UTC()
	if err := store.ScanAndRecordIdleMarkers(ctx, emitTs, LiveStreamLaneRetention); err != nil {
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
	vendorLanes := s.Dimensions["vendor"]
	var openaiLane *LiveStreamLane
	for i := range vendorLanes {
		if vendorLanes[i].ID == "openai" {
			openaiLane = &vendorLanes[i]
			break
		}
	}
	if openaiLane == nil {
		t.Fatalf("expected openai vendor lane with idle tile, lanes=%#v", vendorLanes)
	}
	hasIdleTile := false
	for _, tile := range openaiLane.Requests {
		if tile.Status == "idle" {
			hasIdleTile = true
			break
		}
	}
	if !hasIdleTile {
		t.Fatalf("expected idle tile inside openai vendor lane, requests=%#v", openaiLane.Requests)
	}
}

func TestLiveStreamQueueKeepLimit(t *testing.T) {
	if got := liveStreamQueueKeepLimit(liveStreamMainKey); got != liveStreamReplayLimit {
		t.Fatalf("main queue keep=%d want %d", got, liveStreamReplayLimit)
	}
	if got := liveStreamQueueKeepLimit("llmgw:live:tenant:default:main"); got != liveStreamReplayLimit {
		t.Fatalf("tenant main keep=%d want %d", got, liveStreamReplayLimit)
	}
	if got := liveStreamQueueKeepLimit(liveStreamDimPrefix + "vendor:openai"); got != LiveStreamLaneVisibleLimit {
		t.Fatalf("dimension queue keep=%d want %d", got, LiveStreamLaneVisibleLimit)
	}
}

// TestIdleMarkerUsesScanTimeAsTs verifies the 2026-07-28 fix: the idle
// marker's Ts (and ZSet score) is the scan time, not the anchor
// "lastActivity+threshold". This is what makes the "update idle time on
// every tick" requirement observable at the Redis level (a ZADD with
// the same member but a fresh score is a real write that refreshes the
// key TTL and reorders the marker in the lane).
//
// 2026-07-28: This test replaces the previous TestIdleMarkerAnchorsAtSilenceStart
// which asserted the buggy "freeze at silence start" behaviour. The
// bug report requires idle duration to grow between ticks, which is
// only achievable if the Ts advances on each tick.
func TestIdleMarkerUsesScanTimeAsTs(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	store := NewLiveStreamRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx := context.Background()

	lastActivity := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	threshold := 5 * time.Minute
	mr.Set("llmgw:live:activity:global:vendor:openai", fmt.Sprintf("%d", lastActivity.Unix()))

	emitTs := lastActivity.Add(threshold + time.Minute)
	if err := store.ScanAndRecordIdleMarkers(ctx, emitTs, threshold); err != nil {
		t.Fatalf("ScanAndRecordIdleMarkers: %v", err)
	}

	items, err := store.Replay(ctx, "", true, 50)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var idle *LiveRequest
	for i := range items {
		if items[i].Type == "idle_marker" {
			idle = &items[i]
			break
		}
	}
	if idle == nil {
		t.Fatalf("expected idle marker, got %#v", items)
	}
	// 2026-07-28 fix: Ts is now the scan time (emitTs), not the
	// anchor at silence start (lastActivity+threshold). This is what
	// makes each idle tick a real ZADD (different score than the
	// previous tick) so the key TTL is refreshed and the displayed
	// idle duration is "time since the most recent heartbeat".
	wantTs := emitTs.UTC().Format(time.RFC3339)
	if idle.Ts != wantTs {
		t.Fatalf("idle ts=%q want %q (scan time, so each tick is a real ZADD)", idle.Ts, wantTs)
	}
	// Sanity: must NOT be the old anchored value.
	oldAnchoredTs := lastActivity.Add(threshold).UTC().Format(time.RFC3339)
	if idle.Ts == oldAnchoredTs {
		t.Fatalf("idle ts=%q must not be the legacy anchored-at-silence-start value", idle.Ts)
	}
}

func TestLiveStreamRedisStore_TrimDimensionQueueToVisibleLimit(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	store := NewLiveStreamRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx := context.Background()
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	total := LiveStreamLaneVisibleLimit + 5
	for i := 0; i < total; i++ {
		req := LiveRequest{
			RequestID:     fmt.Sprintf("req-%03d", i),
			Ts:            base.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339),
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		}
		if err := store.Record(ctx, req, ""); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	key := liveStreamDimPrefix + "vendor:openai"
	n, err := store.rdb.ZCard(ctx, key).Result()
	if err != nil {
		t.Fatalf("ZCard: %v", err)
	}
	if n != int64(LiveStreamLaneVisibleLimit) {
		t.Fatalf("dimension queue len=%d want %d", n, LiveStreamLaneVisibleLimit)
	}
	oldest, err := store.rdb.ZRange(ctx, key, 0, 0).Result()
	if err != nil {
		t.Fatalf("ZRange: %v", err)
	}
	// Dimension queues store slim tile JSON members, so compare the decoded
	// request id rather than the raw member.
	wantOldest := fmt.Sprintf("req-%03d", total-LiveStreamLaneVisibleLimit)
	if len(oldest) == 0 || requestIDFromDimensionQueueMember(oldest[0]) != wantOldest {
		t.Fatalf("oldest member=%v want %q (first 5 trimmed)", oldest, wantOldest)
	}
}

func TestLiveRequestTile_ProbeFields(t *testing.T) {
	tile := liveRequestTile(LiveRequest{
		RequestID:    "probe-1",
		Ts:           "2026-07-14T12:00:00Z",
		Model:        "gpt-4o",
		ProviderCode: "openai",
		Status:       "success",
		IsProbe:      true,
		ProbeOrigin:  "gateway",
		ProbeAttempt: 2,
	})
	if !tile.IsProbe {
		t.Fatalf("expected IsProbe=true, got %#v", tile)
	}
	if tile.ProbeOrigin != "gateway" {
		t.Fatalf("expected ProbeOrigin=gateway, got %q", tile.ProbeOrigin)
	}
	if tile.ProbeAttempt != 2 {
		t.Fatalf("expected ProbeAttempt=2, got %d", tile.ProbeAttempt)
	}
}

func TestLiveStreamRedisStore_NilClient(t *testing.T) {
	store := NewLiveStreamRedisStore(nil)
	ctx := context.Background()

	// Should not panic, all operations are no-ops
	if err := store.Record(ctx, LiveRequest{RequestID: "test"}, ""); err != nil {
		t.Errorf("Record with nil client should return nil, got %v", err)
	}
	if err := store.ScanAndRecordIdleMarkers(ctx, time.Now(), LiveStreamLaneRetention); err != nil {
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

func TestDimensionQueueKeyInfoBuildsActivityKeyScope(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
		ok   bool
	}{
		{name: "global provider", key: liveStreamDimPrefix + "provider:MiniMax", want: liveStreamActivityKey("", "provider", "MiniMax"), ok: true},
		{name: "tenant model", key: "llmgw:live:tenant:default:dim:model:minimax-m3", want: liveStreamActivityKey("default", "model", "minimax-m3"), ok: true},
		{name: "invalid dimension", key: liveStreamDimPrefix + "status:success", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := dimensionQueueKeyInfo(tt.key)
			if ok != tt.ok {
				t.Fatalf("ok=%v want %v (%#v)", ok, tt.ok, info)
			}
			if ok {
				got := liveStreamActivityKey(info.tenantID, info.dimension, info.dimensionKey)
				if got != tt.want {
					t.Fatalf("activity key=%q want %q", got, tt.want)
				}
			}
		})
	}
}

func TestLiveStreamRedisStore_ActivityKeysUseDimensionIndexes(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	if err := store.Record(ctx, LiveRequest{
		RequestID: "indexed-1", Ts: time.Now().UTC().Format(time.RFC3339), TenantID: "default",
		Model: "minimax-m3", ModelCategory: "minimax", ProviderCode: "MiniMax", Status: "success",
	}, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	keys, err := store.activityKeysFromDimensionIndexes(ctx)
	if err != nil {
		t.Fatalf("activityKeysFromDimensionIndexes: %v", err)
	}
	want := map[string]bool{
		liveStreamActivityKey("", "vendor", "minimax"):        false,
		liveStreamActivityKey("", "provider", "MiniMax"):      false,
		liveStreamActivityKey("", "model", "minimax-m3"):      false,
		liveStreamActivityKey("default", "vendor", "minimax"): false,
	}
	for _, key := range keys {
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing indexed activity key %q", key)
		}
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
		"cost_usd": {}, "error_kind": {}, "client_profile": {}, "identity_hash": {}, "credits_charged": {},
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

	if err := store.Record(ctx, req, ""); err != nil {
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
		if err := store.Record(ctx, req, ""); err != nil {
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

	if err := store.Record(ctx, start, ""); err != nil {
		t.Fatalf("Record start: %v", err)
	}
	if err := store.Record(ctx, done, ""); err != nil {
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

func TestLiveStreamRedisStore_StatusTransitionDoesNotMoveTimestampBackward(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()
	base := time.Now().UTC()
	start := LiveRequest{
		RequestID:     "req-backward-ts",
		Ts:            base.Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "in_progress",
	}
	staleDone := start
	staleDone.Ts = base.Add(-12 * time.Minute).Format(time.RFC3339)
	staleDone.Status = "success"

	if err := store.Record(ctx, start, ""); err != nil {
		t.Fatalf("Record start: %v", err)
	}
	if err := store.Record(ctx, staleDone, ""); err != nil {
		t.Fatalf("Record staleDone: %v", err)
	}

	items, err := store.Replay(ctx, "tenant-a", false, 10)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one request after stale transition, got %#v", items)
	}
	if items[0].Status != "success" {
		t.Fatalf("expected success state, got %#v", items[0])
	}
	if items[0].Ts != start.Ts {
		t.Fatalf("timestamp moved backward: got %q want %q", items[0].Ts, start.Ts)
	}

	snap := BuildLiveStreamSnapshot(items)
	vendor := snap.Dimensions["vendor"]
	if len(vendor) != 1 || len(vendor[0].Requests) != 1 {
		t.Fatalf("unexpected vendor lanes: %#v", vendor)
	}
	if vendor[0].Requests[0].Timestamp != start.Ts {
		t.Fatalf("lane timestamp moved backward: got %q want %q", vendor[0].Requests[0].Timestamp, start.Ts)
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
	}, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Force activity keys stale so the vendor/provider/model/main lanes emit idle markers.
	staleUnix := seedTs.Unix() - int64(LiveStreamLaneRetention.Seconds()) - 5
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	if err := store.ScanAndRecordIdleMarkers(ctx, time.Now().UTC(), LiveStreamLaneRetention); err != nil {
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
	// req-1 + one idle marker per idle dimension (vendor, provider, model).
	// "main" activity keys are skipped — they never render in a swim lane.
	if len(members) < 4 {
		t.Fatalf("expected tenant main queue to contain req-1 + 3 idle markers, got %d: %#v", len(members), members)
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
	for _, idle := range []*LiveRequest{vendorIdle, providerIdle, modelIdle} {
		if idle.ErrorKind == nil || *idle.ErrorKind != idleMarkerErrorKind {
			t.Fatalf("expected error_kind=%q on idle marker, got %#v", idleMarkerErrorKind, idle)
		}
	}
	// Global-scope idle markers must not land in the tenant main queue.
	for _, m := range members {
		if strings.HasPrefix(m, "idle-global-") {
			t.Fatalf("global idle marker %q should not be in tenant main queue", m)
		}
	}
}

func TestComputeScopeDelta_FallsBackToReplayWhenDimensionSnapshotEmpty(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{RedisClient: rdb, InitialReplayLimit: 50})
	ctx := context.Background()
	req := LiveRequest{
		RequestID:     "req-replay-fallback",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}
	if err := hub.store.Record(ctx, req, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Simulate a local/no-dimension-scan environment: remove dimension queues so
	// SnapshotFromDimensionQueues returns empty, but keep the main queue intact.
	for _, key := range []string{
		liveStreamDimPrefix + "vendor:openai",
		liveStreamDimPrefix + "provider:openai",
		liveStreamDimPrefix + "model:gpt-4o",
		tenantLiveStreamKey("tenant-a", "dim:vendor:openai"),
		tenantLiveStreamKey("tenant-a", "dim:provider:openai"),
		tenantLiveStreamKey("tenant-a", "dim:model:gpt-4o"),
	} {
		mr.Del(key)
	}

	delta := hub.computeScopeDelta(ctx, "tenant-a", false)
	if delta == nil {
		t.Fatal("expected delta from replay fallback")
	}
	if delta.Summary.Total != 1 {
		t.Fatalf("summary.total=%d want 1", delta.Summary.Total)
	}
	if got := len(delta.ChangedLanes["vendor"]); got != 1 {
		t.Fatalf("vendor lanes=%d want 1", got)
	}
	if got := delta.ChangedLanes["vendor"][0].Requests[0].RequestID; got != req.RequestID {
		t.Fatalf("vendor request_id=%q want %q", got, req.RequestID)
	}
}

// TestIdleMarkerQueueKeys_ScopeRouting verifies the 2026-07-28 fix:
// idle markers are mirror-written to the dimension queue as well as
// the main queue, so the production reader (SnapshotFromDimensionQueues)
// can surface them. The previous implementation only wrote to the
// main queue, which meant idle markers were invisible whenever any
// dim queue had any data — the "idle tile suddenly missing" bug.
func TestIdleMarkerQueueKeys_ScopeRouting(t *testing.T) {
	t.Run("global scope writes to main + global dim", func(t *testing.T) {
		keys := idleMarkerQueueKeys("", "vendor", "openai")
		mustContain := map[string]bool{
			liveStreamMainKey:                     false,
			liveStreamDimPrefix + "vendor:openai": false,
		}
		for _, k := range keys {
			if _, ok := mustContain[k]; ok {
				mustContain[k] = true
			}
		}
		for k, seen := range mustContain {
			if !seen {
				t.Fatalf("global idle queues=%#v missing %q", keys, k)
			}
		}
		// Must NOT leak into a tenant main queue for global scope.
		for _, k := range keys {
			if strings.HasPrefix(k, "llmgw:live:tenant:") {
				t.Fatalf("global idle queue %q must not be tenant-scoped", k)
			}
		}
	})

	t.Run("tenant scope writes to tenant main + tenant dim only", func(t *testing.T) {
		keys := idleMarkerQueueKeys("tenant-a", "vendor", "openai")
		mustContain := map[string]bool{
			tenantLiveStreamKey("tenant-a", "main"):              false,
			tenantLiveStreamKey("tenant-a", "dim:vendor:openai"): false,
		}
		for _, k := range keys {
			if _, ok := mustContain[k]; ok {
				mustContain[k] = true
			}
			if k == liveStreamDimPrefix+"vendor:openai" {
				t.Fatalf("tenant idle marker must not be mirrored to global dim queue: %q", keys)
			}
		}
		for k, seen := range mustContain {
			if !seen {
				t.Fatalf("tenant idle queues=%#v missing %q", keys, k)
			}
		}
		if len(keys) != len(mustContain) {
			t.Fatalf("tenant idle queues=%#v want exactly tenant main + tenant dim", keys)
		}
	})

	t.Run("empty dimension key falls back to main queue only", func(t *testing.T) {
		// The ScanAndRecordIdleMarkers filter already skips dimension=="main",
		// but a defensive empty-key call must not produce a malformed dim
		// key like "llmgw:live:dim:vendor:".
		keys := idleMarkerQueueKeys("", "vendor", "")
		if len(keys) != 1 || keys[0] != liveStreamMainKey {
			t.Fatalf("empty-dim global idle queues=%#v want [%q]", keys, liveStreamMainKey)
		}
		keys = idleMarkerQueueKeys("tenant-a", "vendor", "")
		want := tenantLiveStreamKey("tenant-a", "main")
		if len(keys) != 1 || keys[0] != want {
			t.Fatalf("empty-dim tenant idle queues=%#v want [%q]", keys, want)
		}
	})

	t.Run("unsafe characters in dimension key are escaped", func(t *testing.T) {
		// dim values with ':' or '/' would produce an ambiguous key
		// that downstream parsers could split incorrectly. The mirror
		// must apply the same safeKey transform that idleMarkerRequestID
		// uses, so the dim key matches the RequestID the dashboard
		// expects to see.
		keys := idleMarkerQueueKeys("", "model", "gpt:4o/abc")
		wantDim := liveStreamDimPrefix + "model:gpt_4o_abc"
		found := false
		for _, k := range keys {
			if k == wantDim {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("idle queues=%#v missing escaped dim key %q", keys, wantDim)
		}
		// None of the keys should still contain the unsafe colon-slash.
		for _, k := range keys {
			if strings.Contains(k, "gpt:4o/") {
				t.Fatalf("idle queue %q still contains unsafe characters", k)
			}
		}
	})
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
	if s.Summary.Total != 3 || s.Summary.Success != 1 || s.Summary.Failure != 1 || s.Summary.InProgress != 1 || s.Summary.RateLimited != 0 {
		t.Fatalf("unexpected summary: %#v", s.Summary)
	}
	if len(s.Dimensions["vendor"]) != 2 {
		t.Fatalf("expected 2 vendor lanes, got %d", len(s.Dimensions["vendor"]))
	}
	if s.Dimensions["vendor"][0].ID != "anthropic" {
		t.Fatalf("first vendor lane should be anthropic (stable sort), got %s", s.Dimensions["vendor"][0].ID)
	}
	if s.Dimensions["vendor"][1].ID != "openai" {
		t.Fatalf("second vendor lane should be openai, got %s", s.Dimensions["vendor"][1].ID)
	}
	if s.Dimensions["vendor"][1].Stats.Total != 2 {
		t.Fatalf("openai lane total should be 2, got %d", s.Dimensions["vendor"][1].Stats.Total)
	}
	if len(s.Dimensions["provider"][0].Requests) == 0 {
		t.Fatal("provider lane should include render-ready requests")
	}
}

func TestBuildLiveStreamSnapshot_ClientCancelProbeDoesNotCountAsProviderFailure(t *testing.T) {
	stage := "probe"
	errorKind := "client_cancel"
	items := []LiveRequest{
		{RequestID: "real", Ts: "2026-08-16T00:00:01Z", Model: "claude", ModelCategory: "anthropic", ProviderCode: "zhima", Status: "success"},
		{RequestID: "probe-client_cancel-1", Ts: "2026-08-16T00:00:02Z", Model: "claude", ModelCategory: "anthropic", ProviderCode: "zhima", Status: "failure", FailureStage: &stage, ErrorKind: &errorKind},
	}

	snapshot := BuildLiveStreamSnapshot(items)

	if snapshot.Summary.Total != 1 || snapshot.Summary.Success != 1 || snapshot.Summary.Failure != 0 {
		t.Fatalf("client cancel probe affected summary stats: %#v", snapshot.Summary)
	}
	lane := snapshot.Dimensions["provider"][0]
	if lane.Stats.Total != 1 || lane.Stats.Failure != 0 {
		t.Fatalf("client cancel probe affected provider stats: %#v", lane.Stats)
	}
	if len(lane.Requests) != 2 {
		t.Fatalf("diagnostic probe must remain visible, requests=%d", len(lane.Requests))
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
		if err := store.Record(ctx, req, ""); err != nil {
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
	t.Run("zero_value_yields_4h_defaults", func(t *testing.T) {
		hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
		if hub.cfg.CachedSnapshotTTL != LiveStreamLaneRetention {
			t.Fatalf("expected CachedSnapshotTTL=4h, got %s", hub.cfg.CachedSnapshotTTL)
		}
		if hub.cfg.CachedSnapshotCleanupInterval != LiveStreamLaneRetention {
			t.Fatalf("expected CachedSnapshotCleanupInterval=4h when zero, got %s",
				hub.cfg.CachedSnapshotCleanupInterval)
		}
		if hub.cfg.IdleThreshold != LiveStreamIdleThreshold {
			t.Fatalf("expected IdleThreshold=5m, got %s", hub.cfg.IdleThreshold)
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
	scope := newLiveStreamScope("tenant-active", false)
	hub.cachedSnapshotMu.Lock()
	hub.cachedSnapshot[scope.cacheKey] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: staleBefore,
	}
	hub.cachedSnapshotMu.Unlock()

	beforeHits := atomic.LoadInt64(&hub.cachedSnapshotBaselinePresent)
	beforeMisses := atomic.LoadInt64(&hub.cachedSnapshotBaselineAbsent)
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
	entry := hub.cachedSnapshot[scope.cacheKey]
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
	if got := atomic.LoadInt64(&hub.cachedSnapshotBaselinePresent); got != beforeHits+1 {
		t.Errorf("expected cachedSnapshotBaselinePresent++ (was %d, now %d)", beforeHits, got)
	}
	if got := atomic.LoadInt64(&hub.cachedSnapshotBaselineAbsent); got != beforeMisses {
		t.Errorf("cachedSnapshotBaselineAbsent should not increment when baseline exists (was %d, now %d)", beforeMisses, got)
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
	newScope := newLiveStreamScope("tenant-new", false)
	_, exists := hub.cachedSnapshot[newScope.cacheKey]
	hub.cachedSnapshotMu.RUnlock()
	if exists {
		t.Fatal("computeScopeDelta should NOT create empty entries for unseen tenants")
	}
	if got := atomic.LoadInt64(&hub.cachedSnapshotBaselineAbsent); got != beforeMisses+1 {
		t.Errorf("expected miss++ (was %d, now %d)", beforeMisses, got)
	}

	// (4) After multiple computeScopeDelta calls refreshing lastAccessed,
	//     evictStaleCachedSnapshots must NOT remove the active tenant
	//     even though it was "about to expire" at seed time.
	hub.evictStaleCachedSnapshots()
	hub.cachedSnapshotMu.RLock()
	_, stillThere := hub.cachedSnapshot[scope.cacheKey]
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
		name       string
		key        string
		wantTenant string
		wantDim    string
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

func TestBuildLiveStreamLanes_StableAlphabeticalOrder(t *testing.T) {
	items := []LiveRequest{
		{RequestID: "r1", ModelCategory: "openai", Status: "success"},
		{RequestID: "r2", ModelCategory: "openai", Status: "success"},
		{RequestID: "r3", ModelCategory: "anthropic", Status: "success"},
	}
	lanes, _, _ := buildLiveStreamLanes("vendor", items)
	if len(lanes) < 2 {
		t.Fatalf("expected at least 2 lanes, got %d", len(lanes))
	}
	// openai has higher Total but anthropic must come first (alphabetical).
	if lanes[0].ID != "anthropic" {
		t.Fatalf("expected anthropic first (stable sort), got %q then %q", lanes[0].ID, lanes[1].ID)
	}
	if lanes[1].ID != "openai" {
		t.Fatalf("expected openai second, got %q", lanes[1].ID)
	}
	if lanes[1].Stats.Total <= lanes[0].Stats.Total {
		t.Fatalf("openai should still have higher Total in stats, anthropic=%d openai=%d",
			lanes[0].Stats.Total, lanes[1].Stats.Total)
	}
}

// 2026-08-27: the lane ordering contract is ASC FIFO (oldest left → newest
// right), flipped deliberately by 730cbaef8. SwimLaneTrack paints index 0
// leftmost and lastTiles() caps each lane by keeping the tail (newest N).
// buildLiveStreamLanes therefore sorts each lane itself rather than
// inheriting the caller's order, so the dimension-queue path and the
// main-queue replay path agree.
func TestBuildLiveStreamLanes_LaneRequestsAreASC(t *testing.T) {
	// Deliberately shuffled: the lane builder must not depend on the
	// caller pre-sorting its input.
	items := []LiveRequest{
		{RequestID: "r3", Ts: "2026-07-20T00:00:02Z", Model: "gpt-4o", ModelCategory: "openai", ProviderCode: "openai", Status: "success"},
		{RequestID: "r1", Ts: "2026-07-20T00:00:00Z", Model: "gpt-4o", ModelCategory: "openai", ProviderCode: "openai", Status: "success"},
		{RequestID: "r4", Ts: "2026-07-20T00:00:03Z", Model: "gpt-4o", ModelCategory: "openai", ProviderCode: "openai", Status: "failure"},
		{RequestID: "r2", Ts: "2026-07-20T00:00:01Z", Model: "gpt-4o", ModelCategory: "openai", ProviderCode: "openai", Status: "success"},
	}
	lanes, _, _ := buildLiveStreamLanes("vendor", items)
	if len(lanes) != 1 {
		t.Fatalf("expected 1 lane, got %d", len(lanes))
	}
	requests := lanes[0].Requests
	if len(requests) != 4 {
		t.Fatalf("expected 4 tiles in lane, got %d", len(requests))
	}
	for i := 1; i < len(requests); i++ {
		if requests[i-1].Timestamp > requests[i].Timestamp {
			t.Fatalf("lane %q not ASC at idx %d: prev=%q curr=%q",
				lanes[0].ID, i, requests[i-1].Timestamp, requests[i].Timestamp)
		}
	}
	// ASC FIFO: the oldest request is painted leftmost, newest rightmost.
	if requests[0].RequestID != "r1" {
		t.Fatalf("expected oldest request (r1) at head, got %q", requests[0].RequestID)
	}
	if requests[len(requests)-1].RequestID != "r4" {
		t.Fatalf("expected newest request (r4) at tail, got %q", requests[len(requests)-1].RequestID)
	}
}

// 2026-08-27: under the ASC FIFO contract lastTiles() keeps items[len-N:],
// i.e. the newest N tiles — the cap must drop the OLDEST so a busy lane
// keeps showing fresh traffic instead of freezing on its first tiles.
func TestBuildLiveStreamLanes_LaneCapKeepsNewest(t *testing.T) {
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	total := liveStreamLaneLimit + 5
	items := make([]LiveRequest, 0, total)
	for i := 0; i < total; i++ {
		items = append(items, LiveRequest{
			RequestID:     fmt.Sprintf("req-%03d", i),
			Ts:            base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		})
	}

	lanes, _, _ := buildLiveStreamLanes("vendor", items)
	if len(lanes) != 1 {
		t.Fatalf("expected 1 lane, got %d", len(lanes))
	}
	requests := lanes[0].Requests
	if len(requests) != liveStreamLaneLimit {
		t.Fatalf("lane tiles=%d want %d", len(requests), liveStreamLaneLimit)
	}

	newest := fmt.Sprintf("req-%03d", total-1)
	if requests[len(requests)-1].RequestID != newest {
		t.Fatalf("newest tile=%q want %q", requests[len(requests)-1].RequestID, newest)
	}
	for _, tile := range requests {
		if tile.RequestID == "req-000" {
			t.Fatal("oldest request survived the cap; newest tiles were dropped instead")
		}
	}
}

// 2026-07-20: lanesChanged must NOT report "changed" when only the struct
// pointers differ but request_id + status + ts are identical. The previous
// implementation compared LiveStreamTile values with `!=` (pointer
// compare), so liveRequestTile's "always-fresh struct" output caused the
// delta cache to push the full lane every snapshot — the bandwidth and
// Vue remount waste that surfaced as "swim lane flicker".
func TestLanesChanged_DetectsActualChanges(t *testing.T) {
	tileA := LiveStreamTile{RequestID: "r1", Status: "success", Timestamp: "2026-07-20T00:00:00Z"}
	tileB := LiveStreamTile{RequestID: "r2", Status: "success", Timestamp: "2026-07-20T00:00:01Z"}
	tileAUpdated := LiveStreamTile{RequestID: "r1", Status: "success", Timestamp: "2026-07-20T00:00:00Z"}

	old := []LiveStreamLane{{ID: "openai", Requests: []LiveStreamTile{tileA}, Stats: LiveStreamStats{Total: 1, Success: 1}}}
	newSame := []LiveStreamLane{{ID: "openai", Requests: []LiveStreamTile{tileAUpdated}, Stats: LiveStreamStats{Total: 1, Success: 1}}}
	if lanesChanged(old, newSame) {
		t.Error("lanesChanged returned true for an identical-content pair; the new check must ignore pointer inequality")
	}

	// Status change must trigger a change.
	tileAStatusChanged := LiveStreamTile{RequestID: "r1", Status: "failure", Timestamp: "2026-07-20T00:00:00Z"}
	newStatusDiff := []LiveStreamLane{{ID: "openai", Requests: []LiveStreamTile{tileAStatusChanged}, Stats: LiveStreamStats{Total: 1, Failure: 1}}}
	if !lanesChanged(old, newStatusDiff) {
		t.Error("lanesChanged returned false despite status change; must trigger on Status diff")
	}

	// Timestamp change must trigger a change.
	tileATimeChanged := LiveStreamTile{RequestID: "r1", Status: "success", Timestamp: "2026-07-20T00:00:05Z"}
	newTimeDiff := []LiveStreamLane{{ID: "openai", Requests: []LiveStreamTile{tileATimeChanged}, Stats: LiveStreamStats{Total: 1, Success: 1}}}
	if !lanesChanged(old, newTimeDiff) {
		t.Error("lanesChanged returned false despite ts change; must trigger on Timestamp diff")
	}

	// Different request_id set must trigger.
	newIDDiff := []LiveStreamLane{{ID: "openai", Requests: []LiveStreamTile{tileB}, Stats: LiveStreamStats{Total: 1, Success: 1}}}
	if !lanesChanged(old, newIDDiff) {
		t.Error("lanesChanged returned false despite request_id change; must trigger on id diff")
	}

	// Length mismatch must trigger.
	newLenDiff := []LiveStreamLane{{ID: "openai", Requests: []LiveStreamTile{tileA, tileB}, Stats: LiveStreamStats{Total: 2, Success: 2}}}
	if !lanesChanged(old, newLenDiff) {
		t.Error("lanesChanged returned false despite length mismatch")
	}
}

// Regression guard for deterministic lane order end-to-end through the
// dimension-queue path. Redis SCAN key order is not stable across calls,
// so without an explicit sort the same underlying records produced
// different lane.requests on consecutive snapshots — the swim-lane
// "rolling"/jumping symptom.
//
// 2026-07-26: the asserted contract is now ASC (oldest first) to match
// what the dashboard renders and what lastTiles() truncates against.
func TestSnapshotFromDimensionQueues_RequestsAreASC(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	// Seed 5 requests out of chronological order. Record() writes to all
	// dimension queues (vendor/provider/model). SnapshotFromDimensionQueues
	// now loads every dimKey member verbatim, while BuildLiveStreamSnapshot
	// dedupes Summary/lanes in the next stage.
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	insertOrder := []int{3, 0, 4, 1, 2} // shuffled
	for _, i := range insertOrder {
		req := LiveRequest{
			RequestID:     "req-" + string(rune('a'+i)),
			Ts:            base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			TenantID:      "default",
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		}
		if err := store.Record(ctx, req, ""); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	for _, id := range []string{"req-z", "req-y"} {
		if err := store.Record(ctx, LiveRequest{
			RequestID:     id,
			Ts:            base.Add(2 * time.Second).Format(time.RFC3339),
			TenantID:      "default",
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		}, ""); err != nil {
			t.Fatalf("Record %s: %v", id, err)
		}
	}

	snap, err := store.SnapshotFromDimensionQueues(ctx, "default", false)
	if err != nil {
		t.Fatalf("SnapshotFromDimensionQueues: %v", err)
	}
	if snap == nil || snap.Summary.Total == 0 {
		t.Fatal("expected non-empty snapshot")
	}

	for _, dim := range []string{"vendor", "provider", "model"} {
		for _, lane := range snap.Dimensions[dim] {
			for j := 1; j < len(lane.Requests); j++ {
				if lane.Requests[j-1].Timestamp > lane.Requests[j].Timestamp {
					t.Fatalf("%s lane %q not ASC at idx %d: prev=%q curr=%q",
						dim, lane.ID, j,
						lane.Requests[j-1].Timestamp, lane.Requests[j].Timestamp)
				}
			}
			for j := 1; j < len(lane.Requests); j++ {
				if lane.Requests[j-1].Timestamp == lane.Requests[j].Timestamp &&
					lane.Requests[j-1].RequestID > lane.Requests[j].RequestID {
					t.Fatalf("%s lane %q tie-break is not stable at idx %d: prev=%q curr=%q",
						dim, lane.ID, j,
						lane.Requests[j-1].RequestID, lane.Requests[j].RequestID)
				}
			}
		}
	}
}

func TestSnapshotFromDimensionQueues_ReadsSlimTileMembers(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	defer mr.Close()

	store := NewLiveStreamRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	req := LiveRequest{
		RequestID:     "req-slim-1",
		Ts:            "2026-07-26T04:30:00Z",
		TenantID:      "default",
		Model:         "claude-sonnet-5",
		ModelCategory: "anthropic",
		ProviderCode:  "apiclaude",
		Status:        "success",
	}
	if err := store.Record(ctx, req, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	snap, err := store.SnapshotFromDimensionQueues(ctx, "default", false)
	if err != nil {
		t.Fatalf("SnapshotFromDimensionQueues: %v", err)
	}
	if snap == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if snap.Summary.Total != 1 {
		t.Fatalf("summary total=%d want 1", snap.Summary.Total)
	}

	vendorLanes := snap.Dimensions["vendor"]
	if len(vendorLanes) != 1 {
		t.Fatalf("vendor lanes len=%d want 1", len(vendorLanes))
	}
	if vendorLanes[0].ID != "anthropic" {
		t.Fatalf("vendor lane id=%q want anthropic", vendorLanes[0].ID)
	}
	if got := vendorLanes[0].Requests[0].RequestID; got != "req-slim-1" {
		t.Fatalf("vendor tile request_id=%q want req-slim-1", got)
	}

	providerLanes := snap.Dimensions["provider"]
	if len(providerLanes) != 1 {
		t.Fatalf("provider lanes len=%d want 1", len(providerLanes))
	}
	if providerLanes[0].ID != "apiclaude" {
		t.Fatalf("provider lane id=%q want apiclaude", providerLanes[0].ID)
	}
}

// 2026-07-20: The dimension-queue snapshot path no longer dedupes across
// dimKeys before building the snapshot. This regression test ensures the
// later-stage per-lane and Summary dedupe keep counts stable: one request
// should appear once in vendor, once in provider, once in model, but
// Summary.Total must still be 1.
func TestBuildLiveStreamSnapshot_DedupesSummaryAndLaneMembers(t *testing.T) {
	req := LiveRequest{
		RequestID:     "req-1",
		Ts:            "2026-07-20T12:00:00Z",
		TenantID:      "default",
		Model:         "glm-5.2",
		CanonicalName: "glm-5.2",
		ModelCategory: "zhipu ai",
		ProviderCode:  "普联",
		Status:        "success",
	}
	// Simulate SnapshotFromDimensionQueues loading the same request once per
	// dimKey (vendor/provider/model). The snapshot builder must count it once
	// in Summary while keeping exactly one tile in each lane.
	items := []LiveRequest{req, req, req}
	snap := BuildLiveStreamSnapshot(items)
	if snap.Summary.Total != 1 {
		t.Fatalf("summary.total=%d want 1", snap.Summary.Total)
	}
	if snap.Summary.Success != 1 {
		t.Fatalf("summary.success=%d want 1", snap.Summary.Success)
	}
	if got := len(snap.Dimensions["vendor"]); got != 1 {
		t.Fatalf("vendor lanes=%d want 1", got)
	}
	if got := len(snap.Dimensions["provider"]); got != 1 {
		t.Fatalf("provider lanes=%d want 1", got)
	}
	if got := len(snap.Dimensions["model"]); got != 1 {
		t.Fatalf("model lanes=%d want 1", got)
	}
	for _, dim := range []string{"vendor", "provider", "model"} {
		lane := snap.Dimensions[dim][0]
		if got := len(lane.Requests); got != 1 {
			t.Fatalf("%s lane requests=%d want 1", dim, got)
		}
		if lane.Requests[0].RequestID != req.RequestID {
			t.Fatalf("%s lane request_id=%q want %q", dim, lane.Requests[0].RequestID, req.RequestID)
		}
	}
}

func TestSlimTileFormat(t *testing.T) {
	now := time.Now().UTC()
	errorKind := "5xx"

	tile := LiveStreamTile{
		RequestID: "req-test-123",
		Timestamp: now.Format(time.RFC3339),
		Status:    "success",
		ErrorKind: &errorKind,
		IsProbe:   true,
	}

	// Serialize to slim format
	data, err := marshalTileSlim(tile)
	if err != nil {
		t.Fatalf("marshalTileSlim: %v", err)
	}

	// Verify size is small (under 100 bytes)
	if len(data) >= 100 {
		t.Fatalf("slim format should be under 100 bytes, got %d", len(data))
	}

	// Deserialize back
	decoded, err := unmarshalTileSlim(data)
	if err != nil {
		t.Fatalf("unmarshalTileSlim: %v", err)
	}

	// Verify key fields preserved
	if decoded.RequestID != tile.RequestID {
		t.Errorf("RequestID mismatch: got %q want %q", decoded.RequestID, tile.RequestID)
	}
	if decoded.Status != tile.Status {
		t.Errorf("Status mismatch: got %q want %q", decoded.Status, tile.Status)
	}
	if decoded.IsProbe != tile.IsProbe {
		t.Errorf("IsProbe mismatch: got %v want %v", decoded.IsProbe, tile.IsProbe)
	}
	if decoded.ErrorKind == nil {
		t.Fatal("ErrorKind should not be nil")
	}
	if *decoded.ErrorKind != *tile.ErrorKind {
		t.Errorf("ErrorKind mismatch: got %q want %q", *decoded.ErrorKind, *tile.ErrorKind)
	}

	// Verify timestamp (allow 1ms tolerance for rounding)
	origTs, _ := time.Parse(time.RFC3339, tile.Timestamp)
	decodedTs, _ := time.Parse(time.RFC3339, decoded.Timestamp)
	delta := origTs.UnixMilli() - decodedTs.UnixMilli()
	if delta < -1 || delta > 1 {
		t.Errorf("Timestamp delta %dms exceeds tolerance", delta)
	}
}

func TestSlimTileFormatSizeReduction(t *testing.T) {
	errorKind := "5xx"
	tile := LiveStreamTile{
		RequestID:        "req-test-456",
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		Model:            "gpt-4o",
		Vendor:           "openai",
		Provider:         "openai-official",
		Status:           "failure",
		ErrorKind:        &errorKind,
		LatencyMs:        intPtr(1234),
		CostUSD:          float64Ptr(0.05),
		PromptTokens:     intPtr(100),
		CompletionTokens: intPtr(200),
		IsProbe:          false,
	}

	// Full format (current)
	fullData, _ := json.Marshal(tile)

	// Slim format (new)
	slimData, _ := marshalTileSlim(tile)

	// Log actual sizes
	reduction := float64(len(fullData)-len(slimData)) / float64(len(fullData))
	t.Logf("Full size: %d bytes, Slim size: %d bytes, Reduction: %.1f%%",
		len(fullData), len(slimData), reduction*100)

	// Verify >70% reduction (realistic based on actual field count)
	if reduction <= 0.7 {
		t.Fatalf("expected >70%% size reduction, got %.1f%%", reduction*100)
	}
}

func float64Ptr(v float64) *float64 { return &v }

// lastTiles caps a lane whose items are sorted ASC (oldest first), so the
// kept tail is the newest N — the FIFO contract from 730cbaef8.
func TestLastTiles(t *testing.T) {
	tiles := []LiveStreamTile{
		{RequestID: "oldest", Timestamp: "2026-07-26T12:00:00Z"},
		{RequestID: "older", Timestamp: "2026-07-26T12:01:00Z"},
		{RequestID: "newer", Timestamp: "2026-07-26T12:02:00Z"},
		{RequestID: "newest", Timestamp: "2026-07-26T12:03:00Z"},
	}

	t.Run("returns all when limit >= length", func(t *testing.T) {
		result := lastTiles(tiles, 10)
		assert.Equal(t, 4, len(result))
		assert.Equal(t, "oldest", result[0].RequestID)
	})

	t.Run("keeps newest N when limit < length", func(t *testing.T) {
		result := lastTiles(tiles, 2)
		assert.Equal(t, 2, len(result))
		assert.Equal(t, "newer", result[0].RequestID)
		assert.Equal(t, "newest", result[1].RequestID)
	})

	t.Run("returns all when limit is 0", func(t *testing.T) {
		result := lastTiles(tiles, 0)
		assert.Equal(t, 4, len(result))
	})
}

func TestTimestampsEqual(t *testing.T) {
	base := "2026-07-26T12:00:00.000Z"

	t.Run("exact match", func(t *testing.T) {
		assert.True(t, timestampsEqual(base, base))
	})

	t.Run("within tolerance (50ms)", func(t *testing.T) {
		ts1 := "2026-07-26T12:00:00.000Z"
		ts2 := "2026-07-26T12:00:00.050Z"
		assert.True(t, timestampsEqual(ts1, ts2))
	})

	t.Run("within tolerance (100ms)", func(t *testing.T) {
		ts1 := "2026-07-26T12:00:00.000Z"
		ts2 := "2026-07-26T12:00:00.100Z"
		assert.True(t, timestampsEqual(ts1, ts2))
	})

	t.Run("outside tolerance (150ms)", func(t *testing.T) {
		ts1 := "2026-07-26T12:00:00.000Z"
		ts2 := "2026-07-26T12:00:00.150Z"
		assert.False(t, timestampsEqual(ts1, ts2))
	})

	t.Run("invalid timestamps fall back to string comparison", func(t *testing.T) {
		assert.True(t, timestampsEqual("invalid", "invalid"))
		assert.False(t, timestampsEqual("invalid1", "invalid2"))
	})
}

func TestLanesChangedWithTimestampTolerance(t *testing.T) {
	baseLane := LiveStreamLane{
		ID:       "test",
		Name:     "test",
		Stats:    LiveStreamStats{Total: 1},
		IsOthers: false,
		Requests: []LiveStreamTile{
			{
				RequestID: "req-1",
				Timestamp: "2026-07-26T12:00:00.000Z",
				Status:    "success",
			},
		},
	}

	t.Run("no change detected for timestamps within tolerance", func(t *testing.T) {
		old := []LiveStreamLane{baseLane}
		newLane := baseLane
		newLane.Requests = []LiveStreamTile{
			{
				RequestID: "req-1",
				Timestamp: "2026-07-26T12:00:00.050Z", // 50ms diff
				Status:    "success",
			},
		}
		new := []LiveStreamLane{newLane}

		changed := lanesChanged(old, new)
		assert.False(t, changed, "should not detect change for 50ms timestamp difference")
	})

	t.Run("change detected for timestamps outside tolerance", func(t *testing.T) {
		old := []LiveStreamLane{baseLane}
		newLane := baseLane
		newLane.Requests = []LiveStreamTile{
			{
				RequestID: "req-1",
				Timestamp: "2026-07-26T12:00:00.200Z", // 200ms diff
				Status:    "success",
			},
		}
		new := []LiveStreamLane{newLane}

		changed := lanesChanged(old, new)
		assert.True(t, changed, "should detect change for 200ms timestamp difference")
	})
}

// TestIdleMarker_VisibleInDimensionQueueSnapshot is the regression test for
// the "idle tile missing from dashboard" bug. Before the 2026-07-28 fix,
// ScanAndRecordIdleMarkers only wrote idle markers to the main queue, but
// the production reader (SnapshotFromDimensionQueues) reads only dimension
// queues, so the marker was never visible whenever any dim queue had data.
//
// This test sets up a lane with real requests, runs the idle scan, then
// reads via the production path and asserts the idle marker surfaces.
func TestIdleMarker_VisibleInDimensionQueueSnapshot(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	// Seed a real request so a dim queue exists (this is the precondition
	// that hid the bug: with ANY dim data, SnapshotFromDimensionQueues
	// would never fall back to Replay()).
	seedTs := time.Now().UTC().Add(-30 * time.Minute)
	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-seed",
		Ts:            seedTs.Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}, ""); err != nil {
		t.Fatalf("Record seed: %v", err)
	}

	// Force every activity key into the past so the lane is considered idle.
	staleUnix := seedTs.Unix() - int64(LiveStreamLaneRetention.Seconds()) - 5
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	if err := store.ScanAndRecordIdleMarkers(ctx, time.Now().UTC(), LiveStreamLaneRetention); err != nil {
		t.Fatalf("ScanAndRecordIdleMarkers: %v", err)
	}

	// The bug was: the production reader (SnapshotFromDimensionQueues)
	// never saw the idle marker because it only reads dim queues. The
	// fix mirror-writes to dim queues, so this assertion must now pass.
	snap, err := store.SnapshotFromDimensionQueues(ctx, "default", false)
	if err != nil {
		t.Fatalf("SnapshotFromDimensionQueues: %v", err)
	}
	if snap == nil {
		t.Fatal("expected snapshot, got nil")
	}

	// Find the openai vendor lane.
	var openaiLane *LiveStreamLane
	for i := range snap.Dimensions["vendor"] {
		if snap.Dimensions["vendor"][i].ID == "openai" {
			openaiLane = &snap.Dimensions["vendor"][i]
			break
		}
	}
	if openaiLane == nil {
		t.Fatalf("expected openai vendor lane, got %#v", snap.Dimensions["vendor"])
	}

	// The openai lane must now contain an idle tile alongside the seed request.
	hasIdle := false
	for _, tile := range openaiLane.Requests {
		if tile.Status == "idle" {
			hasIdle = true
			break
		}
	}
	if !hasIdle {
		t.Fatalf("idle marker should be visible to the production dim-queue reader; got tiles=%#v", openaiLane.Requests)
	}
}

// TestBuildLiveStreamSnapshot_DedupesIdleMarkersPerLane verifies the contract
// that a rendered lane contains at most one idle tile. Global and tenant
// markers have different stable RequestIDs, and legacy global-dim data can
// leave both in the input snapshot; lane-level dedupe must still collapse
// them to the newest idle marker.
func TestBuildLiveStreamSnapshot_DedupesIdleMarkersPerLane(t *testing.T) {
	oldIdle := LiveRequest{
		Type:          "idle_marker",
		RequestID:     "idle-t-tenant-a-vendor-openai",
		Ts:            "2026-07-28T10:00:00Z",
		TenantID:      "tenant-a",
		Status:        "idle",
		ModelCategory: "openai",
	}
	newIdle := oldIdle
	newIdle.RequestID = "idle-global-vendor-openai"
	newIdle.Ts = "2026-07-28T10:05:00Z"
	newIdle.TenantID = ""
	newRequest := LiveRequest{
		RequestID:     "req-new",
		Ts:            "2026-07-28T10:06:00Z",
		TenantID:      "tenant-a",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}

	snap := BuildLiveStreamSnapshot([]LiveRequest{oldIdle, newIdle, newRequest})
	var lane *LiveStreamLane
	for i := range snap.Dimensions["vendor"] {
		if snap.Dimensions["vendor"][i].ID == "openai" {
			lane = &snap.Dimensions["vendor"][i]
			break
		}
	}
	if lane == nil {
		t.Fatalf("expected openai lane, got %#v", snap.Dimensions["vendor"])
	}

	if len(lane.Requests) != 2 {
		t.Fatalf("expected one normal tile plus one idle tile, got %#v", lane.Requests)
	}
	// ASC FIFO: the idle marker (10:05) precedes the newer request (10:06).
	if lane.Requests[0].RequestID != newIdle.RequestID || lane.Requests[0].Status != "idle" {
		t.Fatalf("expected newest idle marker first under ASC ordering, got %#v", lane.Requests)
	}
	if lane.Requests[1].RequestID != "req-new" {
		t.Fatalf("new normal request should be the newest (rightmost) tile, got %#v", lane.Requests)
	}
	for _, tile := range lane.Requests {
		if tile.Status == "idle" && tile.RequestID == oldIdle.RequestID {
			t.Fatalf("stale duplicate idle marker survived: %#v", lane.Requests)
		}
	}
}

// 不加入新记录" — the "update existing idle, do not add new" requirement.
// A lane that has been idle for 3 consecutive ticks must carry exactly
// ONE idle marker in Redis, never three (the ZADD is idempotent because
// the RequestID is stable per (scope, dimension, dimKey)).
func TestIdleMarker_StableRequestIdAcrossTicks(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	// Seed a request so a lane exists.
	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-1",
		Ts:            time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Force EVERY activity key (global + tenant) into the past so the
	// scanner treats the lane as idle regardless of which scope it picks
	// up. Record() writes both global and tenant activity keys, so we
	// must stale them all. The threshold the scanner checks against is
	// LiveStreamLaneRetention (4h), so the activity key must be older
	// than that to register as idle.
	staleUnix := time.Now().Add(-LiveStreamLaneRetention - time.Minute).Unix()
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	// Three consecutive idle ticks, separated by simulated time advance.
	var idleRequestIDs []string
	for tick := 0; tick < 3; tick++ {
		ts := time.Now().UTC().Add(time.Duration(tick) * time.Minute)
		if err := store.ScanAndRecordIdleMarkers(ctx, ts, LiveStreamLaneRetention); err != nil {
			t.Fatalf("tick %d ScanAndRecordIdleMarkers: %v", tick, err)
		}
		// Inspect the tenant dim queue directly (the request was
		// recorded with tenantID="default" so the activity scanner
		// produces tenant-scoped markers). The mirror write also
		// populates the global dim queue, so we check both.
		tenantDimKey := tenantLiveStreamKey("default", "dim:vendor:openai")
		members, err := rdb.ZRange(ctx, tenantDimKey, 0, -1).Result()
		if err != nil {
			t.Fatalf("tick %d ZRange: %v", tick, err)
		}
		var idleCount int
		for _, m := range members {
			// For tenant-scoped markers, the ZSet member is the bare
			// request_id (not slim-tile JSON) because idleMarkerQueueKeys
			// ZADDs marker.RequestID directly. For real requests, the
			// member IS slim-tile JSON; filter on prefix.
			if strings.HasPrefix(m, "idle-") {
				idleCount++
				idleRequestIDs = append(idleRequestIDs, m)
			}
		}
		if idleCount != 1 {
			t.Fatalf("tick %d: expected exactly 1 idle marker in dim queue, got %d (members=%#v)", tick, idleCount, members)
		}
	}

	// All 3 ticks must have produced the SAME RequestID (stable identity).
	if idleRequestIDs[0] != idleRequestIDs[1] || idleRequestIDs[1] != idleRequestIDs[2] {
		t.Fatalf("idle RequestID must be stable across ticks; got %q, %q, %q", idleRequestIDs[0], idleRequestIDs[1], idleRequestIDs[2])
	}
}

// TestIdleMarker_PushedRightByNewRequest verifies "后续的正常记录推到最左侧，
// 并挤出去" — a new real request has a higher score than the idle marker
// (since real requests use the request's own ts and idle uses scan time,
// both equal to "now", but the new request's ZADD happens AFTER the idle
// tick, so the real request's score is ≥ idle's). In the DESC lane ordering
// the new request goes leftmost and the idle tile gets pushed right.
func TestIdleMarker_PushedRightByNewRequest(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	// Seed an old request, then make the lane go idle.
	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-1",
		Ts:            time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Force EVERY activity key into the past. The scanner compares
	// against LiveStreamLaneRetention (4h), so the activity must be
	// older than that.
	staleUnix := time.Now().Add(-LiveStreamLaneRetention - time.Minute).Unix()
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	idleScanTs := time.Now().UTC()
	if err := store.ScanAndRecordIdleMarkers(ctx, idleScanTs, LiveStreamLaneRetention); err != nil {
		t.Fatalf("ScanAndRecordIdleMarkers: %v", err)
	}

	// Now a new real request arrives AFTER the idle tick. We pass a Ts
	// strictly in the future of the idle scan, so the new request's
	// score is GUARANTEED to be greater than the idle marker's score.
	// (Using time.Now() in tests is racy — by the time ScanAndRecordIdleMarkers
	// finishes, the clock may have advanced past the new request's Ts.)
	futureTs := time.Now().UTC().Add(time.Hour)
	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-2",
		Ts:            futureTs.Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}, ""); err != nil {
		t.Fatalf("Record req-2: %v", err)
	}

	// Tenant-scoped dim queue: this is what the production reader sees
	// for tenant="default". The ZSet has three members — req-1 + req-2
	// as slim tile JSON (because Record() encodes real requests that
	// way) and the idle marker as bare request_id.
	dimKey := tenantLiveStreamKey("default", "dim:vendor:openai")
	members, err := rdb.ZRevRangeWithScores(ctx, dimKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("ZRevRangeWithScores: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 members, got %d (%#v)", len(members), members)
	}
	// members[0] is leftmost (highest score), members[1] is middle,
	// members[2] is rightmost. req-2 (future Ts) must be leftmost and
	// the idle marker must be to the LEFT of req-1 (because its score
	// was the scan time, which is more recent than req-1's 30-min-old
	// Ts). So the order is: req-2 (leftmost), idle (middle), req-1
	// (rightmost).
	if requestIDFromDimensionQueueMember(members[0].Member.(string)) != "req-2" {
		t.Fatalf("new request should be leftmost; got %q (members=%#v)", members[0].Member, members)
	}
	if requestIDFromDimensionQueueMember(members[1].Member.(string)) != idleMarkerRequestID("default", "vendor", "openai") {
		t.Fatalf("middle member should be idle marker; got %q (members=%#v)", members[1].Member, members)
	}
	if requestIDFromDimensionQueueMember(members[2].Member.(string)) != "req-1" {
		t.Fatalf("rightmost member should be the older req-1; got %q (members=%#v)", members[2].Member, members)
	}
	// Score order: req-2 > idle > req-1.
	if !(members[0].Score > members[1].Score && members[1].Score > members[2].Score) {
		t.Fatalf("expected strict score order: req-2 > idle > req-1, got %v %v %v", members[0].Score, members[1].Score, members[2].Score)
	}
}

// TestIdleMarker_RefreshesTsOnEachTick verifies "更新其空闲时间" — between
// two ticks, the idle marker's Ts in the detail hash advances. This is
// the user-visible "空闲 X 分钟" counter: it resets to 0 on each tick and
// then counts up until the next tick, so the operator always sees an
// accurate "time since last heartbeat".
func TestIdleMarker_RefreshesTsOnEachTick(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-1",
		Ts:            time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Force EVERY activity key into the past. The scanner compares
	// against LiveStreamLaneRetention (4h), so the activity must be
	// older than that.
	staleUnix := time.Now().Add(-LiveStreamLaneRetention - time.Minute).Unix()
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	firstTs := time.Now().UTC()
	if err := store.ScanAndRecordIdleMarkers(ctx, firstTs, LiveStreamLaneRetention); err != nil {
		t.Fatalf("first ScanAndRecordIdleMarkers: %v", err)
	}
	// Tenant-scoped RequestID is "idle-t-default-vendor-openai".
	idleRequestID := idleMarkerRequestID("default", "vendor", "openai")
	firstDetail, err := rdb.Get(ctx, liveStreamRequestDetailKey("default", idleRequestID)).Result()
	if err != nil {
		t.Fatalf("Get first detail: %v", err)
	}
	var firstReq LiveRequest
	if err := json.Unmarshal([]byte(firstDetail), &firstReq); err != nil {
		t.Fatalf("Unmarshal first detail: %v", err)
	}
	if firstReq.Ts != firstTs.UTC().Format(time.RFC3339) {
		t.Fatalf("first idle Ts=%q want %q", firstReq.Ts, firstTs.UTC().Format(time.RFC3339))
	}

	// Second tick, 1 second later (simulated).
	mr.FastForward(1 * time.Second)
	secondTs := time.Now().UTC()
	if err := store.ScanAndRecordIdleMarkers(ctx, secondTs, LiveStreamLaneRetention); err != nil {
		t.Fatalf("second ScanAndRecordIdleMarkers: %v", err)
	}
	secondDetail, err := rdb.Get(ctx, liveStreamRequestDetailKey("default", idleRequestID)).Result()
	if err != nil {
		t.Fatalf("Get second detail: %v", err)
	}
	var secondReq LiveRequest
	if err := json.Unmarshal([]byte(secondDetail), &secondReq); err != nil {
		t.Fatalf("Unmarshal second detail: %v", err)
	}
	if secondReq.Ts != secondTs.UTC().Format(time.RFC3339) {
		t.Fatalf("second idle Ts=%q want %q (must advance between ticks)", secondReq.Ts, secondTs.UTC().Format(time.RFC3339))
	}
	if secondReq.RequestID != firstReq.RequestID {
		t.Fatalf("RequestID must stay stable across ticks; got %q then %q", firstReq.RequestID, secondReq.RequestID)
	}
}

// TestIdleMarker_BothMainAndDimQueueUpdated verifies the dual-write fix:
// after a single tick, the idle marker is present in BOTH the main queue
// (so Replay() / Snapshot() see it) AND the dimension queue (so the
// production reader SnapshotFromDimensionQueues sees it). The detail
// hash payload is consistent across both.
func TestIdleMarker_BothMainAndDimQueueUpdated(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	if err := store.Record(ctx, LiveRequest{
		RequestID:     "req-1",
		Ts:            time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Force EVERY activity key into the past. The scanner compares
	// against LiveStreamLaneRetention (4h), so the activity must be
	// older than that.
	staleUnix := time.Now().Add(-LiveStreamLaneRetention - time.Minute).Unix()
	for _, k := range mr.Keys() {
		if strings.HasPrefix(k, liveStreamActivityPrefix) {
			mr.Set(k, fmt.Sprintf("%d", staleUnix))
		}
	}

	if err := store.ScanAndRecordIdleMarkers(ctx, time.Now().UTC(), LiveStreamLaneRetention); err != nil {
		t.Fatalf("ScanAndRecordIdleMarkers: %v", err)
	}

	// Tenant-scoped RequestID.
	idleRequestID := idleMarkerRequestID("default", "vendor", "openai")
	tenantMainKey := tenantLiveStreamKey("default", "main")
	tenantDimKey := tenantLiveStreamKey("default", "dim:vendor:openai")

	// Tenant main queue must contain the idle marker (for Replay() / Snapshot()).
	mainMembers, err := rdb.ZRange(ctx, tenantMainKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("ZRange tenant main: %v", err)
	}
	foundInMain := false
	for _, m := range mainMembers {
		if m == idleRequestID {
			foundInMain = true
			break
		}
	}
	if !foundInMain {
		t.Fatalf("idle marker %q missing from tenant main queue %q (members=%#v)", idleRequestID, tenantMainKey, mainMembers)
	}

	// Tenant dim queue must ALSO contain the idle marker (for
	// SnapshotFromDimensionQueues — the production reader).
	dimMembers, err := rdb.ZRange(ctx, tenantDimKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("ZRange tenant dim: %v", err)
	}
	foundInDim := false
	for _, m := range dimMembers {
		if m == idleRequestID {
			foundInDim = true
			break
		}
	}
	if !foundInDim {
		t.Fatalf("idle marker %q missing from tenant dim queue %q (members=%#v) — production reader would not see it", idleRequestID, tenantDimKey, dimMembers)
	}

	// Score must be the same in both queues (the marker's "logical time").
	mainScore, err := rdb.ZScore(ctx, tenantMainKey, idleRequestID).Result()
	if err != nil {
		t.Fatalf("ZScore tenant main: %v", err)
	}
	dimScore, err := rdb.ZScore(ctx, tenantDimKey, idleRequestID).Result()
	if err != nil {
		t.Fatalf("ZScore tenant dim: %v", err)
	}
	if mainScore != dimScore {
		t.Fatalf("idle marker score mismatch: main=%v dim=%v (must be identical so lane ordering agrees)", mainScore, dimScore)
	}

	// Detail hash must be readable from both global and tenant keys.
	globalDetail, err := rdb.Get(ctx, liveStreamGlobalRequestDetailKey(idleRequestID)).Result()
	if err != nil {
		t.Fatalf("Get global detail: %v", err)
	}
	var req LiveRequest
	if err := json.Unmarshal([]byte(globalDetail), &req); err != nil {
		t.Fatalf("Unmarshal global detail: %v", err)
	}
	if req.Type != "idle_marker" {
		t.Fatalf("detail type=%q want idle_marker", req.Type)
	}
	if req.ModelCategory != "openai" {
		t.Fatalf("detail ModelCategory=%q want openai (vendor identity preserved)", req.ModelCategory)
	}
}

// TestComputeScopeDelta_DropsDegradedSnapshot verifies 方案A: when a scope
// already has a populated cached baseline, a clearly degraded incoming
// snapshot (total far below 40% of the cached baseline — the signature of a
// Redis read timeout that skipped most dimension queues) must be dropped
// rather than pushed to clients, so swim-lane counts do not jump 20↔3.
func TestComputeScopeDelta_DropsDegradedSnapshot(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{RedisClient: rdb, InitialReplayLimit: 200})
	// Disable the 2s snapshot throttle (2026-08-25): this test exercises the
	// degraded-snapshot guard, which must run on the immediate second read —
	// the throttle would just replay the cached baseline delta instead.
	hub.SetSnapshotMinInterval(0)
	ctx := context.Background()

	// Seed a healthy baseline into Redis: 100 openai requests → snapshot total=100.
	for i := 0; i < 100; i++ {
		req := LiveRequest{
			RequestID:     fmt.Sprintf("req-baseline-%d", i),
			Ts:            time.Now().UTC().Format(time.RFC3339),
			TenantID:      "tenant-deg",
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		}
		if err := hub.store.Record(ctx, req, ""); err != nil {
			t.Fatalf("Record baseline %d: %v", i, err)
		}
	}
	// First read populates the cached baseline.
	delta0 := hub.computeScopeDelta(ctx, "tenant-deg", false)
	if delta0 == nil {
		t.Fatal("first delta must populate baseline")
	}
	cachedTotal := delta0.Summary.Total
	if cachedTotal < degradedSnapshotMinBaseline {
		t.Fatalf("baseline total=%d < min %d, test seed too small", cachedTotal, degradedSnapshotMinBaseline)
	}
	skipsBefore := atomic.LoadInt64(&hub.cachedSnapshotDegradedSkips)

	// Wipe most dimension queues to simulate a timeout-degraded read that only
	// retains a tiny fraction of the lanes. We delete all vendor/provider/model
	// queues then re-insert just ONE vendor lane with 2 members, so the next
	// SnapshotFromDimensionQueues returns total≈2 (well under 40% of baseline).
	for _, key := range mr.Keys() {
		if strings.Contains(key, ":dim:") {
			mr.Del(key)
		}
	}
	for i := 0; i < 2; i++ {
		req := LiveRequest{
			RequestID:     fmt.Sprintf("req-degraded-%d", i),
			Ts:            time.Now().UTC().Format(time.RFC3339),
			TenantID:      "tenant-deg",
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		}
		if err := hub.store.Record(ctx, req, ""); err != nil {
			t.Fatalf("Record degraded %d: %v", i, err)
		}
	}

	// The degraded read must be dropped, not pushed.
	deltaDeg := hub.computeScopeDelta(ctx, "tenant-deg", false)
	if deltaDeg != nil {
		t.Fatalf("degraded snapshot must be dropped, got delta total=%d", deltaDeg.Summary.Total)
	}
	skipsAfter := atomic.LoadInt64(&hub.cachedSnapshotDegradedSkips)
	if skipsAfter != skipsBefore+1 {
		t.Fatalf("degraded skip counter: before=%d after=%d, want +1", skipsBefore, skipsAfter)
	}

	// The cached baseline must be UNCHANGED (degraded read must not overwrite it).
	hub.cachedSnapshotMu.RLock()
	entry := hub.cachedSnapshot["scope:tenant:tenant-deg"]
	hub.cachedSnapshotMu.RUnlock()
	if entry == nil || entry.snapshot == nil {
		t.Fatal("cached baseline must be preserved after degraded read")
	}
	if entry.snapshot.Summary.Total != cachedTotal {
		t.Fatalf("cached baseline overwritten: got total=%d want %d", entry.snapshot.Summary.Total, cachedTotal)
	}
}

// TestComputeScopeDelta_AcceptsNonDegradedSnapshot verifies 方案A does NOT
// misfire when traffic legitimately drops but stays above the 40% threshold,
// or when there is no baseline yet (cold start).
func TestComputeScopeDelta_AcceptsNonDegradedSnapshot(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{RedisClient: rdb, InitialReplayLimit: 200})
	ctx := context.Background()

	// Cold start: no baseline → a small snapshot must NOT be dropped.
	for i := 0; i < 3; i++ {
		req := LiveRequest{
			RequestID:     fmt.Sprintf("req-cold-%d", i),
			Ts:            time.Now().UTC().Format(time.RFC3339),
			TenantID:      "tenant-cold",
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		}
		if err := hub.store.Record(ctx, req, ""); err != nil {
			t.Fatalf("Record cold %d: %v", i, err)
		}
	}
	if d := hub.computeScopeDelta(ctx, "tenant-cold", false); d == nil {
		t.Fatal("cold-start small snapshot must not be dropped")
	}

	// Moderate drop (above 40% threshold): baseline 60 → next 30 (50%) must pass.
	for i := 0; i < 60; i++ {
		req := LiveRequest{
			RequestID:     fmt.Sprintf("req-mod-%d", i),
			Ts:            time.Now().UTC().Format(time.RFC3339),
			TenantID:      "tenant-mod",
			Model:         "gpt-4o",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        "success",
		}
		if err := hub.store.Record(ctx, req, ""); err != nil {
			t.Fatalf("Record mod %d: %v", i, err)
		}
	}
	if d := hub.computeScopeDelta(ctx, "tenant-mod", false); d == nil {
		t.Fatal("moderate baseline must populate")
	}
}

// TestLiveStreamDimIndex_PopulatedAndRead verifies 方案C: Record() registers
// its dimension queue keys into the scope index SET, and discoverDimensionQueues
// reads them via SMEMBERS (source=index) instead of SCANning the keyspace.
func TestLiveStreamDimIndex_PopulatedAndRead(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	// Record one request: it should land in vendor/provider/model dim queues
	// AND register those queues in both the global and tenant index SETs.
	req := LiveRequest{
		RequestID:     "req-idx-1",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "tenant-idx",
		Model:         "gpt-4o",
		CanonicalName: "gpt-4o",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}
	if err := store.Record(ctx, req, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Tenant index must contain the tenant-scoped dim keys.
	tenantMembers, err := rdb.SMembers(ctx, liveStreamDimIndexKey("tenant-idx", false)).Result()
	if err != nil {
		t.Fatalf("SMembers tenant index: %v", err)
	}
	wantTenantDim := []string{
		tenantLiveStreamKey("tenant-idx", "dim:vendor:openai"),
		tenantLiveStreamKey("tenant-idx", "dim:provider:openai"),
		tenantLiveStreamKey("tenant-idx", "dim:model:gpt-4o"),
	}
	for _, want := range wantTenantDim {
		if !sliceContainsString(tenantMembers, want) {
			t.Errorf("tenant index missing dim key %q; got %v", want, tenantMembers)
		}
	}

	// Global index must contain the global dim keys.
	globalMembers, err := rdb.SMembers(ctx, liveStreamDimIndexKey("", true)).Result()
	if err != nil {
		t.Fatalf("SMembers global index: %v", err)
	}
	wantGlobalDim := []string{
		liveStreamDimPrefix + "vendor:openai",
		liveStreamDimPrefix + "provider:openai",
		liveStreamDimPrefix + "model:gpt-4o",
	}
	for _, want := range wantGlobalDim {
		if !sliceContainsString(globalMembers, want) {
			t.Errorf("global index missing dim key %q; got %v", want, globalMembers)
		}
	}

	// discoverDimensionQueues for the tenant scope must return exactly the 3
	// tenant dim keys (post-sort) via the index path.
	keys, err := store.discoverDimensionQueues(ctx, "tenant-idx", false)
	if err != nil {
		t.Fatalf("discoverDimensionQueues: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("tenant discover returned %d keys, want 3: %v", len(keys), keys)
	}
	for _, want := range wantTenantDim {
		if !sliceContainsString(keys, want) {
			t.Errorf("discoverDimensionQueues missing key %q; got %v", want, keys)
		}
	}

	// Super scope must return the 3 global dim keys.
	superKeys, err := store.discoverDimensionQueues(ctx, "", true)
	if err != nil {
		t.Fatalf("discoverDimensionQueues super: %v", err)
	}
	if len(superKeys) != 3 {
		t.Fatalf("super discover returned %d keys, want 3: %v", len(superKeys), superKeys)
	}
}

// sliceContainsString reports whether vals contains s. Small helper kept local
// to the live-stream tests to avoid pulling in a broader slice util.
func sliceContainsString(vals []string, s string) bool {
	for _, v := range vals {
		if v == s {
			return true
		}
	}
	return false
}

// TestLiveStreamDimIndex_FallbackToScan verifies 方案C degrades gracefully:
// when the index SET is empty/absent (cold start, expiry), discoverDimensionQueues
// falls back to SCAN so no lane is lost.
func TestLiveStreamDimIndex_FallbackToScan(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	// Record so the dim queues exist in Redis but then DELETE the index SET,
	// simulating an expired/absent index (pre-migration cold start).
	req := LiveRequest{
		RequestID:     "req-fb-1",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "tenant-fb",
		Model:         "claude-3",
		CanonicalName: "claude-3",
		ModelCategory: "anthropic",
		ProviderCode:  "anthropic",
		Status:        "success",
	}
	if err := store.Record(ctx, req, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	rdb.Del(ctx, liveStreamDimIndexKey("tenant-fb", false))

	// discoverDimensionQueues must still find the 3 dim keys via SCAN fallback.
	keys, err := store.discoverDimensionQueues(ctx, "tenant-fb", false)
	if err != nil {
		t.Fatalf("discoverDimensionQueues: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("fallback returned %d keys, want 3: %v", len(keys), keys)
	}
}

// TestIsDimensionQueueKey covers the index filter that decides which queue
// keys are registered and which SET members are kept.
func TestIsDimensionQueueKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{liveStreamDimPrefix + "vendor:openai", true},
		{liveStreamDimPrefix + "provider:MiniMax", true},
		{liveStreamDimPrefix + "model:gpt-4o", true},
		{tenantLiveStreamKey("t1", "dim:vendor:openai"), true},
		{tenantLiveStreamKey("t1", "dim:model:claude-3"), true},
		{liveStreamMainKey, false},
		{tenantLiveStreamKey("t1", "main"), false},
		{liveStreamStatPrefix + "success", false},
		{liveStreamDimIndexPrefix + "global", false}, // index SET itself is not a lane
		{"llmgw:live:req:abc", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isDimensionQueueKey(c.key); got != c.want {
			t.Errorf("isDimensionQueueKey(%q)=%v want %v", c.key, got, c.want)
		}
	}
}
