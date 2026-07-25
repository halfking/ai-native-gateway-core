# Swimlane Display Optimization Design

**Date**: 2026-07-26  
**Author**: System Design  
**Status**: Approved

## Executive Summary

This design addresses critical issues in the real-time request flow dashboard:
1. Visual data jumping/flickering in swimlanes
2. High Redis memory usage from storing full request payloads
3. Timeline display direction (RIGHT→LEFT: newest on left)
4. Case-insensitive model filtering
5. Page visibility optimization to reduce system load

## Current Architecture Analysis

### Backend (Go)
- **Redis Storage**: `admin/live_stream_redis_store.go`
  - Stores full JSON payloads (~500 bytes/tile) in sorted sets
  - Keys: `llmgw:live:dim:{dimension}:{key}`
  - Score: Unix milliseconds timestamp
  - Limit: 20 tiles per lane

- **SSE Hub**: `admin/live_stream_sse.go`
  - Computes deltas by comparing snapshots
  - Broadcasts via Server-Sent Events
  - Caches snapshots per tenant scope

### Frontend (Vue 3 + TypeScript)
- **Store**: `web/src/composables/liveStreamStore.ts`
  - Manages SSE connection
  - Stores global snapshot state

- **Swimlane Component**: `web/src/components/SwimLane.vue`
  - Displays tiles using Vue TransitionGroup
  - Current: Shows newest on RIGHT (last N items)
  - Uses `tile.request_id` as animation key

### Identified Issues

#### Issue 1: Data Jumping
**Root Causes:**
- Vue TransitionGroup re-renders entire array when order changes
- `lanesChanged()` comparison is too strict (timestamp precision)
- Snapshot refresh overwrites delta updates

#### Issue 2: High Memory Usage
- Redis stores ~500KB for typical dashboard (50 lanes × 20 tiles × 500 bytes)
- Full payloads include fields not needed for display

#### Issue 3: Incorrect Display Direction
- Current: Newest on RIGHT (data fills left→right)
- Required: Newest on LEFT (data fills right→left)

#### Issue 4: Case-Sensitive Filtering
- Model filter uses exact string match
- "gpt-4o" and "GPT-4o" treated as different models

#### Issue 5: No Page Visibility Optimization
- Updates continue when tab is hidden
- Wastes CPU/memory on invisible dashboard

---

## Design Solution

### 1. Redis Storage Optimization

**Objective**: Reduce memory usage by 90%

**Implementation**: Slim tile format storing only display-critical fields

```go
// New slim tile format (admin/live_stream_redis_store.go)
type LiveStreamTileSlim struct {
    RequestID   string  `json:"rid"`           // ~20 bytes
    Timestamp   int64   `json:"ts"`            // 8 bytes  
    Status      string  `json:"st"`            // ~10 bytes
    ErrorKind   *string `json:"ek,omitempty"` // ~15 bytes (optional)
    IsProbe     bool    `json:"p,omitempty"`   // 1 byte (optional)
    // Total: ~54 bytes (vs ~500 bytes current)
}
```

**Storage Strategy**:
- Queue stores slim tiles only (for display)
- Detail hash stores full payload (for drawer/detail view)
- Keys remain unchanged for backward compatibility

**Benefits**:
- Memory reduction: 500KB → 50KB (90% savings)
- Faster Redis operations (smaller payloads)
- Network bandwidth savings on SSE push

---

### 2. Display Direction Reversal (RIGHT→LEFT)

**Objective**: Show newest requests on the LEFT, oldest pushed out from RIGHT

**Visual Timeline**:
```
[Newest] ← [Newer] ← [Older] ← [Oldest]
  LEFT                            RIGHT
  
When new request arrives:
[NEW] ← [Newest] ← [Newer] ← [Older] (oldest pushed out)
```

**Backend Changes** (`admin/live_stream_redis_store.go`):

```go
// Current: returns last N tiles (newest at end)
func lastTiles(items []LiveStreamTile, limit int) []LiveStreamTile {
    if limit <= 0 || len(items) <= limit {
        return items
    }
    return items[len(items)-limit:]
}

// New: returns first N tiles (newest at start)
func firstTiles(items []LiveStreamTile, limit int) []LiveStreamTile {
    if limit <= 0 || len(items) <= limit {
        return items
    }
    return items[:limit]
}
```

Update `buildLiveStreamLanes()`:
```go
lanes = append(lanes, LiveStreamLane{
    // ...
    Requests: firstTiles(grouped[key], liveStreamLaneLimit), // Changed
})
```

**Frontend Changes** (`web/src/components/SwimLane.vue`):

```typescript
// Current: slice from end, show left→right
const visibleRequests = computed(() => {
  const requests = props.lane.requests
  const max = maxVisibleTiles.value
  if (requests.length <= max) return [...requests]
  return requests.slice(requests.length - max) // Last N
})

// New: keep backend order (newest first), display left→right
const visibleRequests = computed(() => {
  const requests = props.lane.requests
  const max = maxVisibleTiles.value
  if (requests.length <= max) return [...requests]
  return requests.slice(0, max) // First N
})
```

**CSS Animation Update**:
```vue
<style>
/* Current: tiles enter from right */
.swim-tile-enter-from {
  opacity: 0;
  transform: translateX(20px);
}

/* New: tiles enter from left */
.swim-tile-enter-from {
  opacity: 0;
  transform: translateX(-20px);
}

/* Existing tiles shift right when new tile arrives */
.swim-tile-move {
  transition: transform 0.3s ease;
}
</style>
```

---

### 3. Data Jumping Fixes

**Objective**: Eliminate visual flickering in swimlanes

#### Fix A: Stable Vue Keys
Ensure `tile.request_id` is truly unique and stable:

```typescript
// SwimLane.vue - verify key stability
<RequestTile
  v-for="tile in renderedRequests"
  :key="`${tile.request_id}-${groupBy}`"  // Namespace by dimension
  :tile="tile"
/>
```

#### Fix B: Delta Comparison Tolerance

```go
// admin/live_stream_redis_store.go
func lanesChanged(old, new []LiveStreamLane) bool {
    // ... existing checks ...
    
    for j := range old[i].Requests {
        // Add timestamp tolerance (±100ms for clock skew)
        oldTs, _ := time.Parse(time.RFC3339, old[i].Requests[j].Timestamp)
        newTs, _ := time.Parse(time.RFC3339, new[i].Requests[j].Timestamp)
        if absTimeDiff(oldTs, newTs) > 100*time.Millisecond {
            return true
        }
    }
    return false
}

func absTimeDiff(a, b time.Time) time.Duration {
    d := a.Sub(b)
    if d < 0 {
        return -d
    }
    return d
}
```

#### Fix C: Verify Snapshot Refresh Logic
Confirm `needsFullRefresh()` is working correctly:

```go
// admin/live_stream_sse.go - add debug logging
func needsFullRefresh(cached, fresh *LiveStreamSnapshot) bool {
    if cached == nil {
        slog.Debug("snapshot refresh: first push")
        return true
    }
    
    // Check total count change > 20%
    oldTotal := cached.Summary.Total
    newTotal := fresh.Summary.Total
    if oldTotal > 0 {
        diff := float64(absInt(newTotal - oldTotal))
        threshold := float64(oldTotal) * 0.2
        if diff > threshold {
            slog.Debug("snapshot refresh: total changed",
                "old", oldTotal, "new", newTotal, "diff_pct", diff/float64(oldTotal)*100)
            return true
        }
    }
    
    // ... rest of checks ...
    
    slog.Debug("snapshot refresh: skipped, no significant changes")
    return false
}
```

---

### 4. Page Visibility Optimization

**Objective**: Pause updates when page is hidden, refresh on focus

**Implementation** (`web/src/composables/liveStreamStore.ts`):

```typescript
// Add visibility state
const visibilityState = reactive({
  isVisible: !document.hidden,
  pendingUpdates: [] as LiveStreamEnvelope[],
})

// Track visibility changes
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    const wasHidden = !visibilityState.isVisible
    visibilityState.isVisible = !document.hidden
    
    if (!document.hidden && wasHidden) {
      // Page became visible - refresh full snapshot
      console.log('[LiveStream] Page visible, refreshing snapshot')
      refetchFullSnapshot()
      visibilityState.pendingUpdates = []
    } else if (document.hidden) {
      console.log('[LiveStream] Page hidden, pausing updates')
    }
  })
}

// Modified event handler
function handleMessage(event: MessageEvent) {
  if (!visibilityState.isVisible) {
    // Page hidden - don't apply updates, keep SSE connection alive
    return
  }
  
  // ... normal update logic ...
}

// New function to refetch snapshot
async function refetchFullSnapshot() {
  try {
    const endpoint = buildUrl(customEndpoint || ENDPOINT)
    const response = await fetch(endpoint.replace('/live-stream', '/live-stream/snapshot'))
    const snapshot = await response.json()
    liveStreamState.snapshot = snapshot
  } catch (err) {
    console.error('[LiveStream] Failed to refetch snapshot:', err)
  }
}
```

**Benefits**:
- Reduces CPU usage when tab hidden
- Prevents memory accumulation
- Fresh data on tab focus
- SSE connection remains alive (avoids reconnection cost)

---

### 5. Case-Insensitive Model Filtering

**Objective**: Match models regardless of case ("gpt-4o" = "GPT-4o" = "GPT-4O")

**Backend** (`admin/live_stream_redis_store.go`):
Already implemented via `normalizeModelKey()` - verify it's applied consistently:

```go
// Ensure normalization in queue key generation
modelKey := normalizeModelKey(emptyAs(req.CanonicalName, req.Model))
if modelKey != "" && modelKey != "unknown" {
    keys = append(keys,
        liveStreamDimPrefix+"model:"+modelKey,
        tenantLiveStreamKey(tenantID, "dim:model:"+modelKey),
    )
}

// Ensure normalization in dimension key resolution
case "model":
    if req.CanonicalName != "" {
        return normalizeModelKey(req.CanonicalName)
    }
    if req.Model == "" {
        return ""
    }
    return normalizeModelKey(req.Model)
```

**Frontend** (`web/src/components/LiveRequestStreamV2.vue`):

```typescript
// Normalize filter values
const normalizedModelFilter = computed(() => 
  Array.from(modelFilter.value).map(m => m.toLowerCase().trim())
)

// Apply normalized comparison in filter
const filteredLanes = computed(() => {
  return lanes.value
    .map(lane => ({
      ...lane,
      requests: lane.requests.filter(r => {
        // Model filter (case-insensitive)
        if (normalizedModelFilter.value.length > 0) {
          const stdModel = standardModelName(r.model).toLowerCase().trim()
          if (!normalizedModelFilter.value.includes(stdModel)) {
            return false
          }
        }
        
        // ... other filters ...
        return true
      }),
    }))
    .filter(lane => lane.requests.length > 0)
})

// Update available models list (normalize for display)
const availableModels = computed(() => {
  const models = new Set<string>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      const name = standardModelName(req.model)
      if (name && name !== '[空闲]') {
        models.add(name) // Store original case for display
      }
    }
  }
  // Sort case-insensitively
  return Array.from(models).sort((a, b) => 
    a.localeCompare(b, 'zh-CN', { sensitivity: 'base' })
  )
})
```

---

### 6. Dedicated Swimlane Component

**Objective**: Isolate swimlane rendering logic for better testing and maintainability

**New Component**: `web/src/components/SwimLaneTrack.vue`

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
}>()

const emit = defineEmits<{
  tileClick: [requestId: string]
}>()

// Display first N tiles (newest on left)
const visibleTiles = computed(() => {
  const max = props.maxVisible
  if (props.tiles.length <= max) return props.tiles
  return props.tiles.slice(0, max)
})

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
        @click="handleTileClick"
      />
    </TransitionGroup>
  </div>
</template>

<style scoped>
.swim-lane-track {
  display: flex;
  overflow-x: auto;
  padding: 8px 0;
}

.swim-lane-track__tiles {
  display: flex;
  gap: var(--tile-gap, 6px);
  flex-direction: row;
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

/* Existing tiles shift right */
.swim-tile-move {
  transition: transform 0.3s ease;
}
</style>
```

**Update Parent** (`SwimLane.vue`):

```vue
<template>
  <div class="swim-lane">
    <div class="swim-lane__label">
      <!-- ... existing label code ... -->
    </div>
    
    <SwimLaneTrack
      :tiles="lane.requests"
      :mode="laneMode"
      :group-by="groupBy"
      :max-visible="maxVisibleTiles"
      @tile-click="handleTileClick"
    />
  </div>
</template>

<script setup lang="ts">
import SwimLaneTrack from './SwimLaneTrack.vue'
// ... rest of script ...
</script>
```

---

## Implementation Phases

### Phase 1: Redis Optimization (Backend)
**Duration**: 2-3 hours

**Tasks**:
1. Create `LiveStreamTileSlim` struct
2. Add `marshalTileSlim()` / `unmarshalTileSlim()` functions
3. Update `Record()` to store both slim queue + detail hash
4. Update `Replay()` to use slim tiles
5. Maintain backward compatibility (read old format if slim missing)

**Files**:
- `admin/live_stream_redis_store.go`

**Testing**:
- Unit tests for slim serialization
- Integration test: verify detail drawer still works
- Memory profiling: confirm 90% reduction

---

### Phase 2: Display Direction Change
**Duration**: 2-3 hours

**Tasks**:
1. Backend: Change `lastTiles()` to `firstTiles()`
2. Backend: Update all callers
3. Frontend: Update `visibleRequests` computation
4. Frontend: Reverse CSS animations
5. Test with real data

**Files**:
- `admin/live_stream_redis_store.go`
- `web/src/components/SwimLane.vue`

**Testing**:
- Visual verification: newest on left
- Animation verification: slides in from left
- Edge case: single tile, full lane, empty lane

---

### Phase 3: Data Jumping Fixes
**Duration**: 3-4 hours

**Tasks**:
1. Add timestamp tolerance to `lanesChanged()`
2. Add debug logging to `needsFullRefresh()`
3. Update Vue keys with dimension namespace
4. Load test with high-frequency updates

**Files**:
- `admin/live_stream_redis_store.go`
- `admin/live_stream_sse.go`
- `web/src/components/SwimLane.vue`

**Testing**:
- 100 req/sec for 5 minutes → no jumping
- Rapid dimension switching → smooth
- Snapshot refresh → verify skipped when not needed

---

### Phase 4: Page Visibility
**Duration**: 2 hours

**Tasks**:
1. Add visibility listener
2. Implement pause/resume logic
3. Add snapshot refetch endpoint (if needed)
4. Test tab switching behavior

**Files**:
- `web/src/composables/liveStreamStore.ts`
- `admin/live_stream_sse.go` (add snapshot endpoint)

**Testing**:
- Tab hidden → updates paused
- Tab visible → fresh snapshot loaded
- SSE connection stays alive

---

### Phase 5: Model Filtering
**Duration**: 1-2 hours

**Tasks**:
1. Normalize filter values
2. Case-insensitive comparison
3. Update UI to show normalized models
4. Test edge cases

**Files**:
- `web/src/components/LiveRequestStreamV2.vue`

**Testing**:
- Filter "gpt-4o" matches "GPT-4o", "GPT-4O"
- UI shows original case
- Empty filter shows all

---

### Phase 6: Component Refactoring
**Duration**: 2-3 hours

**Tasks**:
1. Create `SwimLaneTrack.vue` component
2. Extract rendering logic
3. Update parent component
4. Add unit tests

**Files**:
- `web/src/components/SwimLaneTrack.vue` (new)
- `web/src/components/SwimLane.vue`
- `web/src/components/RequestTile.test.ts` (update)

**Testing**:
- Unit tests for SwimLaneTrack
- Integration tests with parent
- Visual regression tests

---

## Verification & Testing

### Automated Tests

**Backend**:
```go
// admin/live_stream_redis_store_test.go
func TestSlimTileFormat(t *testing.T) {
    tile := LiveStreamTileSlim{
        RequestID: "req-123",
        Timestamp: time.Now().UnixMilli(),
        Status:    "success",
    }
    
    data := marshalTileSlim(tile)
    assert.Less(t, len(data), 100) // Under 100 bytes
    
    decoded := unmarshalTileSlim(data)
    assert.Equal(t, tile.RequestID, decoded.RequestID)
}

func TestFirstTiles(t *testing.T) {
    tiles := []LiveStreamTile{
        {RequestID: "old"},
        {RequestID: "new"},
    }
    
    result := firstTiles(tiles, 1)
    assert.Equal(t, "old", result[0].RequestID)
}
```

**Frontend**:
```typescript
// web/src/components/SwimLaneTrack.test.ts
describe('SwimLaneTrack', () => {
  it('displays newest tiles on left', () => {
    const tiles = [
      { request_id: 'new', timestamp: '2026-07-26T12:01:00Z' },
      { request_id: 'old', timestamp: '2026-07-26T12:00:00Z' },
    ]
    
    const wrapper = mount(SwimLaneTrack, {
      props: { tiles, maxVisible: 10 }
    })
    
    const rendered = wrapper.findAllComponents(RequestTile)
    expect(rendered[0].props('tile').request_id).toBe('new')
  })
  
  it('limits visible tiles to maxVisible', () => {
    const tiles = Array.from({ length: 30 }, (_, i) => ({
      request_id: `req-${i}`,
      timestamp: new Date().toISOString(),
    }))
    
    const wrapper = mount(SwimLaneTrack, {
      props: { tiles, maxVisible: 20 }
    })
    
    expect(wrapper.findAllComponents(RequestTile)).toHaveLength(20)
  })
})
```

### Manual Testing Checklist

- [ ] Deploy to local environment
- [ ] Generate 100 test requests
- [ ] Verify newest appears on LEFT
- [ ] Switch dimensions (vendor → provider → model)
- [ ] Verify no visual jumping
- [ ] Enable "gpt-4o" filter, verify matches "GPT-4O"
- [ ] Hide tab for 1 minute, show tab → verify fresh data
- [ ] Check Redis memory: `INFO memory` before/after
- [ ] Load test: 100 req/sec for 5 minutes
- [ ] Check browser DevTools: no memory leaks

### Deployment to 245

**Steps**:
1. Build frontend: `cd web && npm run build`
2. Build backend: `go build -o llm-gateway cmd/gateway/main.go`
3. Deploy to 245: `scp llm-gateway user@245:/path/to/deploy`
4. Restart service: `ssh user@245 'systemctl restart llmgw'`
5. Verify dashboard at `http://245/admin/dashboard`

---

## Rollback Plan

If issues are discovered in production:

1. **Phase 1-2 (Redis + Display)**: 
   - Revert commits
   - Old format still readable (backward compat)
   
2. **Phase 3-6 (Frontend)**:
   - Deploy previous frontend build
   - Backend remains compatible

**Database migrations**: None required (Redis only)

---

## Success Metrics

- Redis memory usage: **< 100KB** (from ~500KB)
- Visual jumping: **Zero** occurrences in 5-minute test
- Page visibility: **90%** reduction in hidden-tab CPU usage
- Model filtering: **100%** case-insensitive match rate
- Animation smoothness: **60 FPS** during tile transitions

---

## Risks & Mitigations

**Risk 1**: Slim format breaks detail drawer
- **Mitigation**: Keep full payload in detail hash
- **Test**: Click tile → drawer opens with full data

**Risk 2**: Display direction confuses users
- **Mitigation**: Add visual indicators (arrow showing time flow)
- **Test**: User study with 3-5 testers

**Risk 3**: Page visibility causes data loss
- **Mitigation**: SSE stays connected, refetch on resume
- **Test**: Leave tab hidden for 1 hour, verify fresh data on return

**Risk 4**: Backward compatibility breaks old clients
- **Mitigation**: Backend reads both formats
- **Test**: Deploy backend first, then frontend (phased rollout)

---

## Future Enhancements

1. **Virtual scrolling**: For lanes with >100 tiles
2. **Tile tooltips**: Show full details on hover
3. **Keyboard navigation**: Arrow keys to navigate tiles
4. **Export functionality**: Download lane data as CSV
5. **Custom time range**: Filter by timestamp range

---

## References

- Original issue: 实时请求流数据跳变优化
- Redis documentation: https://redis.io/docs/data-types/sorted-sets/
- Vue TransitionGroup: https://vuejs.org/guide/built-ins/transition-group.html
- Page Visibility API: https://developer.mozilla.org/en-US/docs/Web/API/Page_Visibility_API

---

**Design Approved**: 2026-07-26  
**Ready for Implementation**: Yes
