# Swimlane Display Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Optimize the real-time request flow dashboard by fixing data jumping, reversing display direction (newest on LEFT), optimizing Redis storage, and adding page visibility support.

**Architecture:** 6-phase implementation covering backend Redis optimization with slim tile format, display direction reversal (RIGHT→LEFT timeline), data jumping fixes with timestamp tolerance, page visibility optimization, case-insensitive model filtering, and frontend component refactoring.

**Tech Stack:** Go (backend), Redis, Vue 3 + TypeScript (frontend), SSE (Server-Sent Events)

**Design Spec:** `docs/superpowers/specs/2026-07-26-swimlane-optimization-design.md`

---

## File Structure Overview

### Backend (Go)
- **Modify**: `admin/live_stream_redis_store.go` - Add slim tile format, change tile ordering
- **Modify**: `admin/live_stream_sse.go` - Add timestamp tolerance, debug logging
- **Create**: `admin/live_stream_redis_store_test.go` - Tests for slim format and ordering

### Frontend (Vue/TypeScript)
- **Modify**: `web/src/components/SwimLane.vue` - Reverse display direction, update animations
- **Modify**: `web/src/components/LiveRequestStreamV2.vue` - Add case-insensitive filtering
- **Modify**: `web/src/composables/liveStreamStore.ts` - Add page visibility optimization
- **Create**: `web/src/components/SwimLaneTrack.vue` - Dedicated swimlane track component
- **Create**: `web/src/components/SwimLaneTrack.test.ts` - Component tests

---

## Phase 1: Redis Storage Optimization

### Task 1.1: Add Slim Tile Format Structures

**Files:**
- Modify: `admin/live_stream_redis_store.go:41-56`

- [ ] **Step 1: Add slim tile struct after LiveStreamTile**

```go
// LiveStreamTileSlim is a lightweight version of LiveStreamTile for Redis queue storage.
// Full details remain in the request detail hash; this minimal format reduces memory by ~90%.
type LiveStreamTileSlim struct {
	RequestID string  `json:"rid"`
	Timestamp int64   `json:"ts"`           // Unix milliseconds
	Status    string  `json:"st"`           // success, failure, in_progress, idle
	ErrorKind *string `json:"ek,omitempty"` // 5xx, 4xx, timeout, etc.
	IsProbe   bool    `json:"p,omitempty"`  // true if this is a probe request
}
```

- [ ] **Step 2: Add serialization helper functions**

```go
// marshalTileSlim converts a LiveStreamTile to slim JSON format.
func marshalTileSlim(tile LiveStreamTile) (string, error) {
	ts, err := time.Parse(time.RFC3339, tile.Timestamp)
	if err != nil {
		return "", fmt.Errorf("invalid timestamp: %w", err)
	}
	
	slim := LiveStreamTileSlim{
		RequestID: tile.RequestID,
		Timestamp: ts.UnixMilli(),
		Status:    tile.Status,
		ErrorKind: tile.ErrorKind,
		IsProbe:   tile.IsProbe,
	}
	
	data, err := json.Marshal(slim)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// unmarshalTileSlim converts slim JSON back to LiveStreamTile.
func unmarshalTileSlim(data string) (LiveStreamTile, error) {
	var slim LiveStreamTileSlim
	if err := json.Unmarshal([]byte(data), &slim); err != nil {
		return LiveStreamTile{}, err
	}
	
	ts := time.UnixMilli(slim.Timestamp).UTC()
	
	return LiveStreamTile{
		RequestID: slim.RequestID,
		Timestamp: ts.Format(time.RFC3339),
		Status:    slim.Status,
		ErrorKind: slim.ErrorKind,
		IsProbe:   slim.IsProbe,
		// Other fields loaded from detail hash when needed
	}, nil
}
```

- [ ] **Step 3: Verify code compiles**

Run: `cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2 && go build ./admin/...`
Expected: No compilation errors

- [ ] **Step 4: Commit structures**

```bash
git add admin/live_stream_redis_store.go
git commit -m "feat(admin): add slim tile format for Redis storage optimization"
```

---

### Task 1.2: Write Tests for Slim Tile Format

**Files:**
- Create: `admin/live_stream_redis_store_test.go`

- [ ] **Step 1: Create test file with slim format tests**

```go
package admin

import (
	"testing"
	"time"
	
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	require.NoError(t, err)
	
	// Verify size is small (under 100 bytes)
	assert.Less(t, len(data), 100, "slim format should be under 100 bytes")
	
	// Deserialize back
	decoded, err := unmarshalTileSlim(data)
	require.NoError(t, err)
	
	// Verify key fields preserved
	assert.Equal(t, tile.RequestID, decoded.RequestID)
	assert.Equal(t, tile.Status, decoded.Status)
	assert.Equal(t, tile.IsProbe, decoded.IsProbe)
	assert.NotNil(t, decoded.ErrorKind)
	assert.Equal(t, *tile.ErrorKind, *decoded.ErrorKind)
	
	// Verify timestamp (allow 1ms tolerance for rounding)
	origTs, _ := time.Parse(time.RFC3339, tile.Timestamp)
	decodedTs, _ := time.Parse(time.RFC3339, decoded.Timestamp)
	assert.InDelta(t, origTs.UnixMilli(), decodedTs.UnixMilli(), 1)
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
	
	// Verify >80% reduction
	reduction := float64(len(fullData)-len(slimData)) / float64(len(fullData))
	assert.Greater(t, reduction, 0.8, "should achieve >80% size reduction")
	
	t.Logf("Full size: %d bytes, Slim size: %d bytes, Reduction: %.1f%%",
		len(fullData), len(slimData), reduction*100)
}

func intPtr(v int) *int       { return &v }
func float64Ptr(v float64) *float64 { return &v }
```

- [ ] **Step 2: Run tests**

Run: `go test ./admin -run TestSlimTile -v`
Expected: PASS with size reduction >80%

- [ ] **Step 3: Commit tests**

```bash
git add admin/live_stream_redis_store_test.go
git commit -m "test(admin): add tests for slim tile format"
```

---

## Phase 2: Display Direction Reversal (RIGHT→LEFT Timeline)

### Task 2.1: Change Backend Tile Ordering

**Files:**
- Modify: `admin/live_stream_redis_store.go:928-933`

- [ ] **Step 1: Rename lastTiles to firstTiles and change logic**

Find the `lastTiles` function around line 928 and replace it:

```go
// firstTiles returns the first N tiles from items (newest tiles, for RIGHT→LEFT display).
// Backend stores tiles in DESC timestamp order in Redis ZSET, so first N = newest N.
func firstTiles(items []LiveStreamTile, limit int) []LiveStreamTile {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	// Return first N instead of last N
	return items[:limit]
}
```

- [ ] **Step 2: Update all callers of lastTiles**

Find `buildLiveStreamLanes` around line 712 and update:

```go
lanes = append(lanes, LiveStreamLane{
	ID:        key,
	Name:      key,
	Dimension: dimension,
	Requests:  firstTiles(grouped[key], liveStreamLaneLimit), // Changed from lastTiles
	Stats:     stats[key],
	IsOthers:  false,
})
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./admin/...`
Expected: No compilation errors

- [ ] **Step 4: Commit backend ordering change**

```bash
git add admin/live_stream_redis_store.go
git commit -m "feat(admin): reverse tile ordering for RIGHT→LEFT timeline display"
```

---

### Task 2.2: Add Backend Tests for Tile Ordering

**Files:**
- Modify: `admin/live_stream_redis_store_test.go`

- [ ] **Step 1: Add test for firstTiles function**

```go
func TestFirstTiles(t *testing.T) {
	tiles := []LiveStreamTile{
		{RequestID: "newest", Timestamp: "2026-07-26T12:03:00Z"},
		{RequestID: "newer", Timestamp: "2026-07-26T12:02:00Z"},
		{RequestID: "older", Timestamp: "2026-07-26T12:01:00Z"},
		{RequestID: "oldest", Timestamp: "2026-07-26T12:00:00Z"},
	}
	
	t.Run("returns all when limit >= length", func(t *testing.T) {
		result := firstTiles(tiles, 10)
		assert.Equal(t, 4, len(result))
		assert.Equal(t, "newest", result[0].RequestID)
	})
	
	t.Run("returns first N when limit < length", func(t *testing.T) {
		result := firstTiles(tiles, 2)
		assert.Equal(t, 2, len(result))
		assert.Equal(t, "newest", result[0].RequestID)
		assert.Equal(t, "newer", result[1].RequestID)
	})
	
	t.Run("returns all when limit is 0", func(t *testing.T) {
		result := firstTiles(tiles, 0)
		assert.Equal(t, 4, len(result))
	})
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./admin -run TestFirstTiles -v`
Expected: PASS

- [ ] **Step 3: Commit tests**

```bash
git add admin/live_stream_redis_store_test.go
git commit -m "test(admin): add tests for firstTiles ordering"
```

---

### Task 2.3: Update Frontend Display Direction

**Files:**
- Modify: `web/src/components/SwimLane.vue:125-135`

- [ ] **Step 1: Update visibleRequests computation**

Find the `visibleRequests` computed property around line 127 and update:

```typescript
// Display first N tiles (backend sends newest first)
// No reversal needed - backend order matches display order (newest on left)
const visibleRequests = computed(() => {
  const requests = props.lane.requests
  const max = maxVisibleTiles.value
  if (requests.length <= max) return [...requests]
  // Take first N (newest) instead of last N
  return requests.slice(0, max)
})
```

- [ ] **Step 2: Update comment for renderedRequests**

Update the comment around line 135:

```typescript
// Backend sends tiles in DESC order (newest first).
// Display left→right: newest on LEFT, oldest on RIGHT.
const renderedRequests = computed<RequestTileType[]>(() => visibleRequests.value)
```

- [ ] **Step 3: Verify TypeScript compilation**

Run: `cd web && npm run type-check`
Expected: No type errors

- [ ] **Step 4: Commit frontend display logic**

```bash
git add web/src/components/SwimLane.vue
git commit -m "feat(web): update swimlane to display newest tiles on left"
```

---

### Task 2.4: Update CSS Animations for LEFT Entry

**Files:**
- Modify: `web/src/components/SwimLane.vue:300-end` (style section)

- [ ] **Step 1: Update animation CSS**

Find the `<style scoped>` section and update the transition classes:

```vue
<style scoped>
/* ... existing styles ... */

/* Animation: new tiles slide in from LEFT */
.swim-tile-enter-active {
  transition: all 0.3s ease;
}

.swim-tile-enter-from {
  opacity: 0;
  transform: translateX(-20px); /* Changed from translateX(20px) */
}

.swim-tile-leave-active {
  transition: all 0.3s ease;
  position: absolute;
}

.swim-tile-leave-to {
  opacity: 0;
  transform: translateX(20px); /* Old tiles slide out RIGHT */
}

/* Existing tiles shift RIGHT when new tile arrives on LEFT */
.swim-tile-move {
  transition: transform 0.3s ease;
}
</style>
```

- [ ] **Step 2: Test animation in browser**

Run: `cd web && npm run dev`
Open: http://localhost:5173/admin/dashboard
Verify: New tiles slide in from left

- [ ] **Step 3: Commit animation changes**

```bash
git add web/src/components/SwimLane.vue
git commit -m "feat(web): reverse swimlane animations for LEFT→RIGHT flow"
```

---

## Phase 3: Data Jumping Fixes

### Task 3.1: Add Timestamp Tolerance to Delta Comparison

**Files:**
- Modify: `admin/live_stream_redis_store.go:1241-1261`

- [ ] **Step 1: Add timestamp comparison helper**

Add after the `lanesChanged` function:

```go
// absTimeDiff returns the absolute duration between two timestamps.
func absTimeDiff(a, b time.Time) time.Duration {
	d := a.Sub(b)
	if d < 0 {
		return -d
	}
	return d
}

// timestampsEqual checks if two RFC3339 timestamps are equal within tolerance.
// Tolerance accounts for clock skew and serialization precision loss.
const timestampToleranceMs = 100

func timestampsEqual(ts1, ts2 string) bool {
	t1, err1 := time.Parse(time.RFC3339, ts1)
	t2, err2 := time.Parse(time.RFC3339, ts2)
	
	if err1 != nil || err2 != nil {
		// If either parse fails, fall back to string comparison
		return ts1 == ts2
	}
	
	return absTimeDiff(t1, t2) <= timestampToleranceMs*time.Millisecond
}
```

- [ ] **Step 2: Update lanesChanged to use timestamp tolerance**

Find the `lanesChanged` function around line 1241 and update the timestamp comparison:

```go
func lanesChanged(old, new []LiveStreamLane) bool {
	if len(old) != len(new) {
		return true
	}
	for i := range old {
		if old[i].ID != new[i].ID || old[i].IsOthers != new[i].IsOthers ||
			old[i].Stats != new[i].Stats || len(old[i].Requests) != len(new[i].Requests) {
			return true
		}
		for j := range old[i].Requests {
			if old[i].Requests[j].RequestID != new[i].Requests[j].RequestID {
				return true
			}
			if old[i].Requests[j].Status != new[i].Requests[j].Status {
				return true
			}
			// Use tolerance-based comparison instead of exact string match
			if !timestampsEqual(old[i].Requests[j].Timestamp, new[i].Requests[j].Timestamp) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./admin/...`
Expected: No errors

- [ ] **Step 4: Commit timestamp tolerance**

```bash
git add admin/live_stream_redis_store.go
git commit -m "feat(admin): add timestamp tolerance to prevent spurious delta updates"
```

---

### Task 3.2: Add Tests for Timestamp Tolerance

**Files:**
- Modify: `admin/live_stream_redis_store_test.go`

- [ ] **Step 1: Add timestamp comparison tests**

```go
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
		new := []LiveStreamLane{baseLane}
		new[0].Requests[0].Timestamp = "2026-07-26T12:00:00.050Z" // 50ms diff
		
		changed := lanesChanged(old, new)
		assert.False(t, changed, "should not detect change for 50ms timestamp difference")
	})
	
	t.Run("change detected for timestamps outside tolerance", func(t *testing.T) {
		old := []LiveStreamLane{baseLane}
		new := []LiveStreamLane{baseLane}
		new[0].Requests[0].Timestamp = "2026-07-26T12:00:00.200Z" // 200ms diff
		
		changed := lanesChanged(old, new)
		assert.True(t, changed, "should detect change for 200ms timestamp difference")
	})
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./admin -run TestTimestamps -v`
Expected: All tests PASS

- [ ] **Step 3: Commit tests**

```bash
git add admin/live_stream_redis_store_test.go
git commit -m "test(admin): add tests for timestamp tolerance"
```

---

### Task 3.3: Add Debug Logging to Snapshot Refresh

**Files:**
- Modify: `admin/live_stream_sse.go:584-639`

- [ ] **Step 1: Enhance needsFullRefresh with detailed logging**

Find the `needsFullRefresh` function and add logging:

```go
func needsFullRefresh(cached, fresh *LiveStreamSnapshot) bool {
	if cached == nil {
		slog.Debug("snapshot refresh: first push (no cached snapshot)")
		return true
	}

	// Judgment 1: Total count changed > 20%
	oldTotal := cached.Summary.Total
	newTotal := fresh.Summary.Total
	if oldTotal > 0 {
		diff := float64(absInt(newTotal - oldTotal))
		threshold := float64(oldTotal) * 0.2
		if diff > threshold {
			slog.Info("snapshot refresh triggered: total count changed significantly",
				"old_total", oldTotal,
				"new_total", newTotal,
				"diff_pct", fmt.Sprintf("%.1f%%", diff/float64(oldTotal)*100),
				"threshold_pct", "20%")
			return true
		}
	}

	// Judgment 2: Lane count changed (any dimension)
	for _, dim := range []string{"vendor", "provider", "model"} {
		oldLanes := cached.Dimensions[dim]
		newLanes := fresh.Dimensions[dim]
		if len(oldLanes) != len(newLanes) {
			slog.Info("snapshot refresh triggered: lane count changed",
				"dimension", dim,
				"old_count", len(oldLanes),
				"new_count", len(newLanes))
			return true
		}
	}

	// Judgment 3: Top 5 lane order changed (any dimension)
	for _, dim := range []string{"vendor", "provider", "model"} {
		oldLanes := cached.Dimensions[dim]
		newLanes := fresh.Dimensions[dim]
		topN := 5
		if len(oldLanes) < topN {
			topN = len(oldLanes)
		}
		if len(newLanes) < topN {
			topN = len(newLanes)
		}

		for i := 0; i < topN; i++ {
			if oldLanes[i].ID != newLanes[i].ID {
				slog.Info("snapshot refresh triggered: top lane order changed",
					"dimension", dim,
					"position", i,
					"old_lane", oldLanes[i].ID,
					"new_lane", newLanes[i].ID)
				return true
			}
		}
	}

	// No significant change detected
	slog.Debug("snapshot refresh skipped: no significant changes detected",
		"old_total", oldTotal,
		"new_total", newTotal,
		"lane_counts", map[string]int{
			"vendor":   len(cached.Dimensions["vendor"]),
			"provider": len(cached.Dimensions["provider"]),
			"model":    len(cached.Dimensions["model"]),
		})
	return false
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./admin/...`
Expected: No errors

- [ ] **Step 3: Commit logging enhancements**

```bash
git add admin/live_stream_sse.go
git commit -m "feat(admin): add debug logging to snapshot refresh logic"
```

---

## Phase 4: Page Visibility Optimization

### Task 4.1: Add Page Visibility Listener

**Files:**
- Modify: `web/src/composables/liveStreamStore.ts:118-200`

- [ ] **Step 1: Add visibility state after liveStreamState**

```typescript
// Page visibility state
const visibilityState = reactive({
  isVisible: typeof document !== 'undefined' ? !document.hidden : true,
  lastVisibleAt: Date.now(),
})
```

- [ ] **Step 2: Add visibility change handler**

```typescript
// Track page visibility changes
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    const wasHidden = !visibilityState.isVisible
    visibilityState.isVisible = !document.hidden
    
    if (!document.hidden && wasHidden) {
      // Page became visible - trigger full refresh
      const hiddenDuration = Date.now() - visibilityState.lastVisibleAt
      console.log(`[LiveStream] Page visible after ${Math.round(hiddenDuration / 1000)}s, refreshing snapshot`)
      
      // Mark that we need a full refresh on next event
      needsFullRefresh = true
    } else if (document.hidden) {
      visibilityState.lastVisibleAt = Date.now()
      console.log('[LiveStream] Page hidden, pausing updates')
    }
  })
}

let needsFullRefresh = false
```

- [ ] **Step 3: Verify TypeScript compilation**

Run: `cd web && npm run type-check`
Expected: No errors

- [ ] **Step 4: Commit visibility state**

```bash
git add web/src/composables/liveStreamStore.ts
git commit -m "feat(web): add page visibility state tracking"
```

---

### Task 4.2: Implement Update Pause When Hidden

**Files:**
- Modify: `web/src/composables/liveStreamStore.ts:200-300`

- [ ] **Step 1: Find handleMessage function and add visibility check**

Look for where SSE messages are processed and add visibility guard:

```typescript
// In the openConnection function, update message handler
eventSource.onmessage = (event: MessageEvent) => {
  try {
    const envelope: LiveStreamEnvelope = JSON.parse(event.data)
    
    // Skip updates when page is hidden (keep connection alive)
    if (!visibilityState.isVisible) {
      console.debug('[LiveStream] Message received but page hidden, skipping update')
      return
    }
    
    // Handle full refresh after page becomes visible
    if (needsFullRefresh && envelope.type === 'snapshot_refresh') {
      console.log('[LiveStream] Applying full snapshot after visibility change')
      needsFullRefresh = false
    }
    
    // ... rest of existing message handling ...
    liveStreamState.lastEventAt = Date.now()
    
    if (envelope.type === 'initial_data') {
      // ... existing initial_data handling ...
    }
    
    // ... rest of message handling ...
  } catch (err) {
    console.error('[LiveStream] Failed to parse SSE message:', err)
  }
}
```

- [ ] **Step 2: Test visibility behavior in browser**

Run: `cd web && npm run dev`
Steps:
1. Open dashboard
2. Wait for data to load
3. Switch to another tab (hide page)
4. Wait 30 seconds
5. Switch back to dashboard tab
Expected: Console shows "Page hidden" then "Page visible, refreshing snapshot"

- [ ] **Step 3: Commit pause logic**

```bash
git add web/src/composables/liveStreamStore.ts
git commit -m "feat(web): pause updates when page is hidden"
```

---

### Task 4.3: Add Snapshot Refresh on Resume

**Files:**
- Modify: `web/src/composables/useLiveStream.ts` (if exists, otherwise modify store directly)

- [ ] **Step 1: Add manual refresh function to store**

Add to `liveStreamStore.ts`:

```typescript
// Request full snapshot refresh from backend
export function requestSnapshotRefresh() {
  if (liveStreamState.connection !== 'open') {
    console.warn('[LiveStream] Cannot refresh: connection not open')
    return
  }
  
  // Trigger backend snapshot push by marking refresh needed
  // Backend will send snapshot_refresh on next periodic check
  needsFullRefresh = true
  console.log('[LiveStream] Snapshot refresh requested')
}
```

- [ ] **Step 2: Call refresh when page becomes visible**

Update the visibility change handler:

```typescript
if (!document.hidden && wasHidden) {
  const hiddenDuration = Date.now() - visibilityState.lastVisibleAt
  console.log(`[LiveStream] Page visible after ${Math.round(hiddenDuration / 1000)}s, refreshing snapshot`)
  
  // Request fresh snapshot
  needsFullRefresh = true
  requestSnapshotRefresh()
}
```

- [ ] **Step 3: Verify TypeScript compilation**

Run: `cd web && npm run type-check`
Expected: No errors

- [ ] **Step 4: Commit refresh logic**

```bash
git add web/src/composables/liveStreamStore.ts
git commit -m "feat(web): refresh snapshot when page becomes visible"
```

---

## Phase 5: Case-Insensitive Model Filtering

### Task 5.1: Normalize Model Filter Values

**Files:**
- Modify: `web/src/components/LiveRequestStreamV2.vue:370-390`

- [ ] **Step 1: Add normalized model filter computed property**

Find where `modelFilter` is defined and add normalization:

```typescript
// Normalize model filter for case-insensitive comparison
const normalizedModelFilter = computed(() => 
  Array.from(modelFilter.value).map(m => m.toLowerCase().trim())
)
```

- [ ] **Step 2: Update filteredLanes to use normalized comparison**

Find the `filteredLanes` computed property around line 323 and update:

```typescript
const filteredLanes = computed(() => {
  return lanes.value
    .map(lane => ({
      ...lane,
      requests: lane.requests.filter(r => {
        // Request type filter
        const requestType = r.is_probe === true ? 'probe' : 'business'
        if (!requestTypeFilter.value.has(requestType)) {
          return false
        }
        
        // Status filter
        if (statusFilter.value.size > 0 && (!r.status || !statusFilter.value.has(r.status as LiveStatus))) {
          return false
        }
        
        // Model filter (case-insensitive)
        if (normalizedModelFilter.value.length > 0) {
          const stdModel = standardModelName(r.model).toLowerCase().trim()
          if (!stdModel || !normalizedModelFilter.value.includes(stdModel)) {
            return false
          }
        }
        
        // Provider filter
        if (providerFilter.value.size > 0 && (!r.provider || !providerFilter.value.has(r.provider))) {
          return false
        }
        
        // Vendor filter
        if (vendorFilter.value.size > 0 && (!r.vendor || !vendorFilter.value.has(r.vendor as LiveModelCategory))) {
          return false
        }
        
        return true
      }),
    }))
    .filter(lane => lane.requests.length > 0)
})
```

- [ ] **Step 3: Update availableModels to sort case-insensitively**

Find `availableModels` computed property and update:

```typescript
const availableModels = computed(() => {
  const models = new Set<string>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      const name = standardModelName(req.model)
      if (name && name !== '[空闲]') {
        models.add(name) // Keep original case for display
      }
    }
  }
  // Sort case-insensitively using locale compare
  return Array.from(models).sort((a, b) => 
    a.localeCompare(b, 'zh-CN', { sensitivity: 'base' })
  )
})
```

- [ ] **Step 4: Test in browser**

Run: `cd web && npm run dev`
Steps:
1. Add filter for "gpt-4o"
2. Verify "GPT-4o", "GPT-4O", "gpt-4o" all match
3. Check dropdown shows models in case-insensitive alphabetical order

- [ ] **Step 5: Commit filtering changes**

```bash
git add web/src/components/LiveRequestStreamV2.vue
git commit -m "feat(web): add case-insensitive model filtering"
```

---

### Task 5.2: Verify Backend Model Normalization

**Files:**
- Verify: `admin/live_stream_redis_store.go:188-198` (normalizeModelKey function)

- [ ] **Step 1: Review normalizeModelKey implementation**

Verify the function exists and is used consistently:

```go
// Should already exist around line 188
func normalizeModelKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = collapseWhitespace(s)
	return strings.ToLower(s)
}
```

- [ ] **Step 2: Check it's used in dimension key generation**

Verify around line 467 in `liveRequestQueueKeys`:

```go
modelKey := normalizeModelKey(emptyAs(req.CanonicalName, req.Model))
if modelKey != "" && modelKey != "unknown" {
    keys = append(keys,
        liveStreamDimPrefix+"model:"+modelKey,
        tenantLiveStreamKey(tenantID, "dim:model:"+modelKey),
    )
}
```

- [ ] **Step 3: Check it's used in liveStreamDimensionKey**

Verify around line 774 and 787:

```go
case "model":
    if req.CanonicalName != "" {
        return normalizeModelKey(req.CanonicalName)
    }
    if req.Model == "" {
        return ""
    }
    return normalizeModelKey(req.Model)
```

- [ ] **Step 4: If all checks pass, document verification**

```bash
git commit --allow-empty -m "docs: verify backend model normalization is consistent"
```

---

## Phase 6: Component Refactoring

### Task 6.1: Create SwimLaneTrack Component

**Files:**
- Create: `web/src/components/SwimLaneTrack.vue`

- [ ] **Step 1: Create new component file**

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { RequestTile, SwimLaneMode } from '../types/swimlane'
import RequestTile from './RequestTile.vue'

const props = defineProps<{
  tiles: RequestTile[]
  mode: SwimLaneMode
  groupBy: string
  maxVisible: number
  selectedLegends?: Set<string>
}>()

const emit = defineEmits<{
  tileClick: [requestId: string]
}>()

// Display first N tiles (backend sends newest first)
const visibleTiles = computed(() => {
  const max = props.maxVisible
  if (props.tiles.length <= max) return props.tiles
  return props.tiles.slice(0, max)
})

function isTileHighlighted(tile: RequestTile): boolean {
  if (!props.selectedLegends || props.selectedLegends.size === 0) return false
  const key = tile[props.groupBy as keyof RequestTile] as string
  return props.selectedLegends.has(key)
}

function isTileDimmed(tile: RequestTile): boolean {
  if (!props.selectedLegends || props.selectedLegends.size === 0) return false
  const key = tile[props.groupBy as keyof RequestTile] as string
  return !props.selectedLegends.has(key)
}

function handleTileClick(requestId: string) {
  emit('tileClick', requestId)
}
</script>

<template>
  <div class="swim-lane-track">
    <TransitionGroup 
      name="swim-tile" 
      tag="div" 
      class="swim-lane-track__tiles"
      :class="{ 'swim-lane-track__tiles--small': mode === 'small' }"
    >
      <RequestTile
        v-for="tile in visibleTiles"
        :key="`${tile.request_id}-${groupBy}`"
        :tile="tile"
        :group-by="groupBy"
        :mode="mode"
        :is-highlighted="isTileHighlighted(tile)"
        :is-dimmed="isTileDimmed(tile)"
        @click="handleTileClick"
      />
    </TransitionGroup>
  </div>
</template>

<style scoped>
.swim-lane-track {
  display: flex;
  overflow-x: hidden;
  min-width: 0;
}

.swim-lane-track__tiles {
  display: flex;
  gap: var(--tile-gap, 6px);
  flex-direction: row;
  min-width: 0;
}

.swim-lane-track__tiles--small {
  gap: var(--tile-gap, 4px);
}

/* Animation: new tiles slide in from LEFT */
.swim-tile-enter-active {
  transition: all 0.3s ease;
}

.swim-tile-enter-from {
  opacity: 0;
  transform: translateX(-20px);
}

.swim-tile-leave-active {
  transition: all 0.3s ease;
  position: absolute;
}

.swim-tile-leave-to {
  opacity: 0;
  transform: translateX(20px);
}

/* Existing tiles shift RIGHT when new tile arrives on LEFT */
.swim-tile-move {
  transition: transform 0.3s ease;
}
</style>
```

- [ ] **Step 2: Verify TypeScript compilation**

Run: `cd web && npm run type-check`
Expected: No errors

- [ ] **Step 3: Commit new component**

```bash
git add web/src/components/SwimLaneTrack.vue
git commit -m "feat(web): create dedicated SwimLaneTrack component"
```

---

### Task 6.2: Integrate SwimLaneTrack into SwimLane

**Files:**
- Modify: `web/src/components/SwimLane.vue`

- [ ] **Step 1: Import SwimLaneTrack component**

Add import at top of script section:

```typescript
import SwimLaneTrack from './SwimLaneTrack.vue'
```

- [ ] **Step 2: Remove old rendering logic from SwimLane.vue**

Remove or comment out:
- `visibleRequests` computed property (lines ~127-132)
- `renderedRequests` computed property (lines ~135)
- `isTileHighlighted` function
- `isTileDimmed` function

- [ ] **Step 3: Replace track template section**

Find the template section with `swim-lane__track` and replace:

```vue
<template>
  <div class="swim-lane">
    <div class="swim-lane__label">
      <!-- Keep existing label code -->
      <div class="swim-lane__name">{{ displayName }}</div>
      <div class="swim-lane__stats">
        <!-- Keep existing stats -->
      </div>
    </div>
    
    <!-- Replace old track with new component -->
    <div
      class="swim-lane__track"
      :class="{ 'swim-lane__track--small': isSmall }"
      :style="{ '--tile-w': TILE_WIDTH + 'px', '--tile-gap': TILE_GAP + 'px' }"
      ref="trackRef"
    >
      <SwimLaneTrack
        :tiles="lane.requests"
        :mode="laneMode"
        :group-by="groupBy"
        :max-visible="maxVisibleTiles"
        :selected-legends="selectedLegends"
        @tile-click="handleTileClick"
      />
    </div>
  </div>
</template>
```

- [ ] **Step 4: Remove old TransitionGroup and tile rendering from style**

Keep container styles, remove tile-specific animation styles (moved to SwimLaneTrack)

- [ ] **Step 5: Test in browser**

Run: `cd web && npm run dev`
Verify: Swimlanes render correctly with new component

- [ ] **Step 6: Commit integration**

```bash
git add web/src/components/SwimLane.vue
git commit -m "refactor(web): integrate SwimLaneTrack component into SwimLane"
```

---

### Task 6.3: Add Unit Tests for SwimLaneTrack

**Files:**
- Create: `web/src/components/SwimLaneTrack.test.ts`

- [ ] **Step 1: Create test file**

```typescript
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import SwimLaneTrack from './SwimLaneTrack.vue'
import RequestTile from './RequestTile.vue'
import type { RequestTile as RequestTileType } from '../types/swimlane'

describe('SwimLaneTrack', () => {
  const createTile = (id: string, timestamp: string): RequestTileType => ({
    request_id: id,
    timestamp,
    model: 'gpt-4o',
    vendor: 'openai',
    provider: 'openai-official',
    status: 'success',
  })

  it('displays tiles in correct order (newest on left)', () => {
    const tiles = [
      createTile('new', '2026-07-26T12:02:00Z'),
      createTile('old', '2026-07-26T12:01:00Z'),
    ]

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'large',
        groupBy: 'vendor',
        maxVisible: 10,
      },
    })

    const renderedTiles = wrapper.findAllComponents(RequestTile)
    expect(renderedTiles).toHaveLength(2)
    expect(renderedTiles[0].props('tile').request_id).toBe('new')
    expect(renderedTiles[1].props('tile').request_id).toBe('old')
  })

  it('limits displayed tiles to maxVisible', () => {
    const tiles = Array.from({ length: 30 }, (_, i) =>
      createTile(`req-${i}`, new Date(Date.now() - i * 1000).toISOString())
    )

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'small',
        groupBy: 'model',
        maxVisible: 20,
      },
    })

    const renderedTiles = wrapper.findAllComponents(RequestTile)
    expect(renderedTiles).toHaveLength(20)
    // First tile should be the first in array (newest)
    expect(renderedTiles[0].props('tile').request_id).toBe('req-0')
  })

  it('shows all tiles when count <= maxVisible', () => {
    const tiles = [
      createTile('req-1', '2026-07-26T12:01:00Z'),
      createTile('req-2', '2026-07-26T12:02:00Z'),
    ]

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'large',
        groupBy: 'provider',
        maxVisible: 10,
      },
    })

    const renderedTiles = wrapper.findAllComponents(RequestTile)
    expect(renderedTiles).toHaveLength(2)
  })

  it('emits tileClick event when tile is clicked', async () => {
    const tiles = [createTile('req-123', '2026-07-26T12:00:00Z')]

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'large',
        groupBy: 'vendor',
        maxVisible: 10,
      },
    })

    const tile = wrapper.findComponent(RequestTile)
    await tile.trigger('click')

    expect(wrapper.emitted('tileClick')).toBeTruthy()
    expect(wrapper.emitted('tileClick')![0]).toEqual(['req-123'])
  })

  it('applies correct mode class', () => {
    const tiles = [createTile('req-1', '2026-07-26T12:00:00Z')]

    const wrapperSmall = mount(SwimLaneTrack, {
      props: { tiles, mode: 'small', groupBy: 'vendor', maxVisible: 10 },
    })

    const wrapperLarge = mount(SwimLaneTrack, {
      props: { tiles, mode: 'large', groupBy: 'vendor', maxVisible: 10 },
    })

    expect(wrapperSmall.find('.swim-lane-track__tiles--small').exists()).toBe(true)
    expect(wrapperLarge.find('.swim-lane-track__tiles--small').exists()).toBe(false)
  })
})
```

- [ ] **Step 2: Run tests**

Run: `cd web && npm run test -- SwimLaneTrack`
Expected: All tests PASS

- [ ] **Step 3: Commit tests**

```bash
git add web/src/components/SwimLaneTrack.test.ts
git commit -m "test(web): add unit tests for SwimLaneTrack component"
```

---

## Integration Testing & Verification

### Task 7.1: Local Integration Testing

**Files:**
- None (testing only)

- [ ] **Step 1: Build frontend**

Run: `cd web && npm run build`
Expected: Build succeeds with no errors

- [ ] **Step 2: Start backend**

Run: `go run cmd/gateway/main.go`
Expected: Server starts on port 8080

- [ ] **Step 3: Open dashboard in browser**

Navigate to: `http://localhost:8080/admin/dashboard`

- [ ] **Step 4: Verify newest-on-left display**

Action: Generate some test requests
Verify: Newest requests appear on LEFT of each swimlane

- [ ] **Step 5: Verify no data jumping**

Action: Let dashboard run for 5 minutes with incoming requests
Verify: No visual flickering or jumping in swimlanes

- [ ] **Step 6: Verify page visibility optimization**

Actions:
1. Switch to another tab (hide dashboard)
2. Wait 1 minute
3. Switch back to dashboard tab
Verify: Console shows "Page hidden" then "Page visible, refreshing"
Verify: Fresh data appears after return

- [ ] **Step 7: Verify case-insensitive model filter**

Actions:
1. Click "模型" filter button
2. Select "gpt-4o" (or any model)
3. Verify requests with "GPT-4o", "GPT-4O", "gpt-4o" all appear
4. Clear filter
Verify: All model variants matched

- [ ] **Step 8: Verify animations**

Action: Watch new requests arrive
Verify: New tiles slide in smoothly from LEFT
Verify: Old tiles slide out to RIGHT when limit exceeded

- [ ] **Step 9: Check Redis memory usage**

Run: `redis-cli INFO memory | grep used_memory_human`
Before: Record value
After 1000 requests: Record value
Verify: Memory usage significantly lower than before optimization

- [ ] **Step 10: Document test results**

Create: `docs/test-results-swimlane-optimization.md` with findings

```bash
git add docs/test-results-swimlane-optimization.md
git commit -m "docs: add local integration test results"
```

---

### Task 7.2: Deploy to 245 and Production Testing

**Files:**
- None (deployment only)

- [ ] **Step 1: Build production assets**

```bash
cd web
npm run build
cd ..
go build -o llm-gateway cmd/gateway/main.go
```

- [ ] **Step 2: Deploy to 245**

```bash
scp llm-gateway user@245:/path/to/deploy/
scp -r web/dist user@245:/path/to/deploy/web/
```

- [ ] **Step 3: Restart service on 245**

```bash
ssh user@245 'systemctl restart llmgw'
```

- [ ] **Step 4: Verify service is running**

```bash
ssh user@245 'systemctl status llmgw'
```
Expected: Active (running)

- [ ] **Step 5: Access dashboard on 245**

Navigate to: `http://245/admin/dashboard`
Verify: Dashboard loads successfully

- [ ] **Step 6: Run production smoke tests**

Test same scenarios as local integration:
- Newest on left
- No jumping
- Page visibility
- Model filtering
- Smooth animations

- [ ] **Step 7: Monitor for 30 minutes**

Watch for:
- Memory leaks
- Connection issues
- Performance degradation
- Error logs

- [ ] **Step 8: Document production deployment**

```bash
git commit --allow-empty -m "deploy: swimlane optimization to production (245)"
git push origin main
```

---

## Rollback Procedures

### If Issues Found in Production

**Rollback Steps:**

1. **Identify the issue**
   - Check error logs: `ssh user@245 'journalctl -u llmgw -n 100'`
   - Check browser console errors
   - Check Redis memory usage

2. **Revert to previous commit**
   ```bash
   git revert HEAD~N  # N = number of commits to revert
   go build -o llm-gateway cmd/gateway/main.go
   cd web && npm run build && cd ..
   ```

3. **Redeploy previous version**
   ```bash
   scp llm-gateway user@245:/path/to/deploy/
   scp -r web/dist user@245:/path/to/deploy/web/
   ssh user@245 'systemctl restart llmgw'
   ```

4. **Verify rollback successful**
   - Dashboard loads
   - No errors in logs
   - Normal operation resumed

5. **Document issue and rollback**
   ```bash
   git commit -m "rollback: swimlane optimization due to [issue description]"
   git push origin main
   ```

---

## Success Criteria Checklist

- [ ] Redis memory usage < 100KB (down from ~500KB)
- [ ] Zero visual jumping in 5-minute observation
- [ ] Newest tiles appear on LEFT of swimlanes
- [ ] Page visibility reduces hidden-tab CPU to near-zero
- [ ] Model filter matches case-insensitively (gpt-4o = GPT-4O)
- [ ] Animations smooth at 60 FPS
- [ ] All unit tests pass
- [ ] All integration tests pass
- [ ] Production deployment successful
- [ ] No errors in logs after 30 minutes of production use

---

**Plan Complete**

Total estimated time: 12-16 hours across 6 phases
Files modified: 8
Files created: 3
Tests added: 50+

