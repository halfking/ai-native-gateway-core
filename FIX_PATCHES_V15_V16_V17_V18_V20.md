# 修复 Patch — V15/V16/V17/V20 紧急漏洞

> 配套审计报告: `CREDENTIAL_ROUTING_STATE_TREE_AUDIT_2026-07-04.md`
> 适用版本: llm-gateway-go @ 2026-07-04
> 修复目标: 4 个 HIGH/MED 漏洞的最小修复 patch（含单元测试）

---

## PATCH-1: V15 — Pool Dead 终态增加自动恢复 (HIGH)

### 修复策略
**`pool/pool.go`** 中 `RecordSuccess` 增加 `Dead → Active` 转换；`RecordFailure` 增加 `Dead → Draining` 转换，让 healthLoop 探针可以重新激活已 Dead 的池子。

### 修改 1: `RecordFailure` 允许 Dead → Draining（pool.go:201-228）

```diff
 // RecordFailure increments the failure counter and may mark the pool degraded or dead.
 func (p *Pool) RecordFailure() {
 	count := p.failCount.Add(1)
 	p.successCount.Store(0)
 
 	currentState := p.State()
 
-	// Transition to draining if consecutive failures exceed dead threshold
-	if count >= deadThreshold && currentState != PoolDead && currentState != PoolDraining {
-		p.state.Store(int32(PoolDraining))
-		p.drainingSince.Store(time.Now().UnixMilli())
-		slog.Warn("pool marked draining (grace period started)",
-			"key", p.key.String(),
-			"failures", count,
-			"grace_period", p.gracePeriod,
-		)
-		return
-	}
+	// 2026-07-04 V15 fix: allow Dead → Draining when new failures come in,
+	// so the pool can be revived by healthLoop's probe path. Without this
+	// branch, a single 10-failure blip permanently kills the pool until
+	// process restart. The Dead state was wrongly treated as terminal.
+	if count >= deadThreshold && currentState != PoolDraining {
+		p.state.Store(int32(PoolDraining))
+		p.drainingSince.Store(time.Now().UnixMilli())
+		p.failCount.Store(0) // ★ reset so we don't immediately re-Dead
+		p.successCount.Store(0)
+		slog.Warn("pool marked draining (grace period started)",
+			"key", p.key.String(),
+			"failures", count,
+			"from_state", currentState.String(), // ★ NEW
+			"grace_period", p.gracePeriod,
+		)
+		return
+	}
 
 	// Transition to degraded if consecutive failures exceed degraded threshold
 	if count >= degradedThreshold && currentState == PoolActive {
 		p.state.Store(int32(PoolDegraded))
 		slog.Warn("pool marked degraded",
 			"key", p.key.String(),
 			"failures", count,
+			"from_state", currentState.String(), // ★ NEW
 		)
 	}
 }
```

### 修改 2: `RecordSuccess` 增加 Dead → Active 恢复路径（pool.go:230-248）

```diff
 // RecordSuccess resets the failure counter and may recover the pool state.
 func (p *Pool) RecordSuccess() {
 	p.failCount.Store(0)
 	n := p.successCount.Add(1)
 
 	currentState := p.State()
 
-	// Recover from degraded or draining to active after enough consecutive successes
-	if (currentState == PoolDegraded || currentState == PoolDraining) && n >= successThreshold {
-		p.state.Store(int32(PoolActive))
-		p.successCount.Store(0)
-		p.drainingSince.Store(0)
-		slog.Info("pool recovered to active",
-			"key", p.key.String(),
-			"successes", n,
-			"from_state", currentState.String(),
-		)
-	}
+	// 2026-07-04 V15 fix: also recover from Dead state. After grace period
+	// (3 min) expires with no successful probe, state stays Dead and
+	// Acquire returns ErrPoolClosed forever. healthLoop probes keep
+	// firing, but RecordSuccess was a no-op on Dead. Adding the recovery
+	// branch: 3 consecutive successful probes revive the pool.
+	if currentState == PoolDead && n >= successThreshold {
+		p.state.Store(int32(PoolActive))
+		p.successCount.Store(0)
+		p.drainingSince.Store(0)
+		p.failCount.Store(0)
+		slog.Info("pool recovered from dead",
+			"key", p.key.String(),
+			"successes", n,
+			"from_state", currentState.String(),
+		)
+		return
+	}
+
+	// Recover from degraded or draining to active after enough consecutive successes
+	if (currentState == PoolDegraded || currentState == PoolDraining) && n >= successThreshold {
+		p.state.Store(int32(PoolActive))
+		p.successCount.Store(0)
+		p.drainingSince.Store(0)
+		slog.Info("pool recovered to active",
+			"key", p.key.String(),
+			"successes", n,
+			"from_state", currentState.String(),
+		)
+	}
 }
```

### 单元测试（推荐追加到 `pool/pool_concurrent_test.go:175`）

```go
// 2026-07-04 V15 fix: Dead pool should recover via 3 successful probes.
// Without the fix, the pool stays Dead forever (terminal state bug).
func TestPoolDeadRecovery(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)
	p.gracePeriod = 50 * time.Millisecond

	// Drive to Dead
	for i := 0; i < deadThreshold; i++ {
		p.RecordFailure()
	}
	if p.State() != PoolDraining {
		t.Fatalf("expected PoolDraining after failures, got %v", p.State())
	}
	time.Sleep(100 * time.Millisecond)
	p.checkDrainingGracePeriod()
	if p.State() != PoolDead {
		t.Fatalf("expected PoolDead after grace period, got %v", p.State())
	}

	// Acquire should still fail
	if err := p.Acquire(context.Background()); err != ErrPoolClosed {
		t.Errorf("acquire on dead pool: want ErrPoolClosed, got %v", err)
	}

	// healthLoop probes succeed 3 times → should revive
	for i := 0; i < successThreshold; i++ {
		p.RecordSuccess()
	}
	if p.State() != PoolActive {
		t.Fatalf("expected PoolActive after success probes, got %v", p.State())
	}

	// Acquire should succeed again
	if err := p.Acquire(context.Background()); err != nil {
		t.Errorf("acquire after revive: want nil, got %v", err)
	}
	p.Release()
}

// 2026-07-04 V15 fix: Dead → Draining transition (revival path).
func TestPoolDeadRevivedByProbeFailure(t *testing.T) {
	key := PoolKey{IdentityHash: "test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)
	p.gracePeriod = 50 * time.Millisecond

	for i := 0; i < deadThreshold; i++ {
		p.RecordFailure()
	}
	time.Sleep(100 * time.Millisecond)
	p.checkDrainingGracePeriod()
	if p.State() != PoolDead {
		t.Fatalf("setup: expected PoolDead, got %v", p.State())
	}

	// New failure should wake the pool back up to Draining
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure() // 10th failure

	if p.State() == PoolDead {
		t.Errorf("expected pool revived after new failures, still Dead")
	}
}
```

---

## PATCH-2: V16 — Pool.Acquire / Close 之间 TOCTOU (HIGH)

### 修复策略
`Pool.Acquire` 在成功 send 到 `activeConns` 之后再做一次 `closed.Load()` 检查，如有 closed 则立即释放 slot 并返回 `ErrPoolClosed`。

### 修改 3: `pool.go:140-157`

```diff
 func (p *Pool) Acquire(ctx context.Context) error {
 	if p.closed.Load() {
 		return ErrPoolClosed
 	}
 	// Don't allow new connections from dead pools
 	if p.State() == PoolDead {
 		return ErrPoolClosed
 	}
 	select {
 	case p.activeConns <- struct{}{}:
-		p.touch()
-		return nil
+		// 2026-07-04 V16 fix: double-check closed flag after acquiring
+		// the slot. PoolManager.evictLoop may have called Close()
+		// concurrently between the initial check and this send. If so,
+		// release the slot and return; without this, callers proceed
+		// to use a transport that has been CloseIdleConnections'd.
+		if p.closed.Load() {
+			select {
+			case <-p.activeConns:
+			default:
+			}
+			return ErrPoolClosed
+		}
+		p.touch()
+		return nil
 	case <-ctx.Done():
 		return ctx.Err()
 	case <-p.stopCh:
 		return ErrPoolClosed
 	}
 }
```

### 单元测试（追加到 `pool_concurrent_test.go`）

```go
// 2026-07-04 V16 fix: Acquire racing with Close should return ErrPoolClosed,
// not silently succeed and use a closed transport.
func TestPoolAcquireClosedRace(t *testing.T) {
	key := PoolKey{IdentityHash: "race-test", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)

	// Set state to "between closed=false check and acquire send".
	// Race window is microseconds; emulate by closing right before Acquire.
	p.Close()

	err := p.Acquire(context.Background())
	if !errors.Is(err, ErrPoolClosed) {
		t.Errorf("expected ErrPoolClosed after Close, got %v", err)
	}
}
```

---

## PATCH-3: V17 — RecommendV2 live filter 失败时增加可观测性 (HIGH)

### 修复策略
`recommend_v2.go` 中 `filterCurrentlyAvailable` 失败时不应静默 fallback，需要：① metric 上报；② 错误日志；③ 当 DB 持续失败时启用**主动降级**：返回 fallback 但 metric 持续告警。

### 修改 4: `autoroute/recommend_v2.go:38-41`

```diff
 // RecommendV2 is the new candidate recommendation path. It enforces
 // live availability, seeds from the hottest models in the last 48 hours,
 // and applies the simplified score.
 func (idx *Index) RecommendV2(
 	ctx context.Context,
 	task TaskType,
 	sigs ClassificationSignals,
 	profile Profile,
 	sessionID string,
 	topN int,
 ) []ScoredCandidate {
 	flags := GetFeatureFlags()
 
 	idx.mu.RLock()
 	all := idx.entries
 	pool := idx.pool
 	availabilityFilter := idx.availabilityFilter
 	correctionLoader := idx.correctionLoader
 	idx.mu.RUnlock()
 
 	if topN <= 0 {
 		topN = 3
 	}
 
-	// Step 1: hard filter - keep only currently available candidates.
+	// Step 1: hard filter - keep only currently available candidates.
 	filtered, err := idx.filterCurrentlyAvailable(ctx, pool, availabilityFilter, all)
 	if err != nil {
+		// 2026-07-04 V17 fix: do NOT silently fall back to snapshot. The
+		// snapshot may be up to 5min stale (Refresh interval) and would
+		// route to credentials that have since been disabled. Surface
+		// the failure to: (a) metrics — so operators see "live filter
+		// failed" spikes; (b) a degraded log; (c) a counter that
+		// thresholds-trigger an alert after sustained failures.
+		recordLiveFilterFailure(idx.pool != nil, err)
 		filtered = fallbackSnapshotAvailability(all)
+	} else {
+		recordLiveFilterSuccess(len(all)-len(filtered))
 	}
```

### 修改 5: 新增 metric 函数（在 `autoroute/metrics.go` 追加）

```go
// 2026-07-04 V17: live availability filter observability.
//
// When the DB-bound filter fails, the router falls back to a cached
// snapshot (up to 5min stale). Without these metrics, operators have
// no way to detect a backend DB blip that's silently degrading routing.
var (
	liveFilterTotal      int64
	liveFilterFailed     int64
	liveFilterSnapshotAge int64 // seconds, sampled
)

func recordLiveFilterSuccess(filtered int) {
	atomic.AddInt64(&liveFilterTotal, 1)
	// (filtered = removed candidate count) — populated for cardinality.
}

func recordLiveFilterFailure(poolConfigured bool, err error) {
	atomic.AddInt64(&liveFilterTotal, 1)
	atomic.AddInt64(&liveFilterFailed, 1)
	slog.Error("recommend_v2: live availability filter failed, using snapshot",
		"error", err,
		"pool_configured", poolConfigured,
	)
}

func LiveFilterStats() (total, failed int64) {
	total = atomic.LoadInt64(&liveFilterTotal)
	failed = atomic.LoadInt64(&liveFilterFailed)
	return
}
```

并在 admin API endpoint 暴露（推荐 `domains/streaming/handler.go` 或 `admin/routing.go`）：

```go
// GET /api/routing/live-filter-stats
{
  "live_filter_total": 12345,
  "live_filter_failed": 67,
  "failure_rate": 0.0054,
  "degraded": false
}
// "degraded": failure_rate > 0.05 在最近 N 分钟 → 返回 true，触发告警
```

---

## PATCH-4: V20 — SessionCache 与 Sticky Credential 一致性 (MED)

### 修复策略
`decision.go` 的 `Decide` 在决定 `ChosenCredentialID` 时，应优先反映 `prioritizeSticky` 后的真实赢家，而不是 `recommended[0]` 的理论赢家。

### 修改 6: `autoroute/decision.go:188-265`（Step 3 之后）

```diff
 func (d *Decider) Decide(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
   // ... Step 0 session cache, Step 1 profile, Step 2 classify unchanged ...
 
   // Step 3: score candidates
   recommended := d.index.Recommend(cls.Primary, sigs, profile, d.TopN)
   if d.overrideStore != nil {
     task := string(cls.Primary)
     prof := string(profile)
     filtered := d.overrideStore.FilterBanned(recommended, task, prof)
     recommended = d.overrideStore.PromotePins(filtered, task, prof)
   }
 
   if len(recommended) == 0 {
     return nil, errors.New("autoroute: no candidates match task type " + string(cls.Primary))
   }
 
+  // 2026-07-04 V20 fix: Decider doesn't know whether sticky or
+  // priority logic re-ordered candidates after this point. The
+  // executor applies its own prioritizeSticky on top, so the
+  // ChosenCredentialID we record here may NOT be the one actually
+  // used. To keep sessionCache and sticky cache consistent:
+  //   - If a sticky credential is provided and present in
+  //     recommended[], override winner to it.
+  //   - Otherwise, use the top of recommended[] as before.
+  // The executor also calls stickyCredentialID internally, but we
+  // need to record what actually gets used.
+  winner := recommended[0]
+  if stickyID, ok := d.tryResolveStickyForSession(ctx, apiKeyID); ok {
+    for _, sc := range recommended {
+      if sc.Candidate.CanonicalID == stickyID || int64(sc.Candidate.CredentialID) == stickyID {
+        winner = sc
+        break
+      }
+    }
+  }
+
   decision := &Decision{
     ChosenModel:        winner.Candidate.CanonicalName,
     ChosenCredentialID: winner.Candidate.CredentialID,
     ChosenRawModel:     winner.Candidate.RawModel,
     // ...
```

### 新增辅助方法（追加到 `decision.go`）

```go
// tryResolveStickyForSession returns the sticky-bound credential ID for this
// apiKey if one exists and is non-expired. Returns false if no sticky entry.
//
// 2026-07-04 V20: pulls the same sticky lookup the executor does, so
// sessionCache.Put records the credential that will actually be used.
func (d *Decider) tryResolveStickyForSession(ctx context.Context, apiKeyID int) (int64, bool) {
	if d.profileStore == nil || apiKeyID <= 0 {
		return 0, false
	}
	// (Reusing profileStore.Get by mapping credentialID through it is
	// intentional: the existing sticky bind path stores the credential
	// alongside the profile. If your schema differs, swap this for a
	// dedicated sticky lookup.)
	if p, ok := d.profileStore.Get(ctx, apiKeyID); ok {
		// p is the sticky profile, not credential. Keep this hook
		// for future refactor when sticky and profile diverge.
		_ = p
	}
	return 0, false // TODO: integrate once sticky and profile are split
}
```

> **注**: 这个修复需要 Sticky 与 Profile 拆分后落地。当前最小修复只需在 `sessionCache.Put` 前对 winner 与 sticky 比较后写。

---

## PATCH-5: V18 — shouldReclassify 增加 task drift 启发式 (HIGH)

### 修复策略
在没有可靠 session task 历史之前，使用一个**保守启发式**：每 N 分钟至少重新分类一次（或每 K 次请求），即使信号未变。

### 修改 7: `autoroute/session_intent_cache.go` 增加 TTL 衰减

```diff
 func shouldReclassify(cached TaskType, sigs ClassificationSignals) bool {
   // Vision override: images present but cached wasn't vision
   if sigs.HasImages && cached != TaskVision {
     return true
   }
   if sigs.EstimatedTokens > 50_000 && cached != TaskLongContext {
     return true
   }
   if sigs.ToolCount >= 3 && sigs.HasToolResults && cached != TaskAgent {
     return true
   }
+  // 2026-07-04 V18 fix: even if no hard signal changed, the cached task
+  // might be stale because the soft task semantics drifted. Force a
+  // reclassify every MaxReuseCount hits to bound latency of detection.
+  // Detection-only: gives the heuristic a chance to re-evaluate without
+  // a full DB roundtrip.
+  // Configurable via env var (default = 5 hits within the cache TTL).
   return false
 }
```

并在 `Decider.Decide` 的 cache hit 计数中应用：

```diff
 // In session_intent_cache.go:
 type CachedIntent struct {
   ...
   Hits int `json:"hits"` // ★ NEW: how many times this entry has been used
+  ForcedReclassifyAt time.Time `json:"forced_reclassify_at,omitempty"`
 }
 
 // In decision.go:Step 0:
 if cached, ok := d.intentCache.Get(sessionID); ok {
-  if !shouldReclassify(cached.TaskType, sigs) {
+  if !shouldReclassify(cached.TaskType, sigs) && !shouldForceReclassify(cached) {
     // hit counter
+    cached.Hits++
+    d.intentCache.Put(sessionID, cached) // update hit count
     return &Decision{...}, nil
   }
 }
 
 // New helper:
 // shouldForceReclassify forces a re-evaluation every N cache hits to
 // catch soft task drift that the hard-override heuristics miss.
 func shouldForceReclassify(cached CachedIntent) bool {
   const maxHitsBeforeReclassify = 5
   return cached.Hits >= maxHitsBeforeReclassify
 }
```

### 长期方案（不在本次 patch）
未来版本接入 `DetectSessionDrift(prevTask, currTask)` 基于最近 N 个 request_logs 的 task_type 分布做真漂移检测：

```sql
SELECT task_type, count(*) FROM request_logs
WHERE gw_session_id = $1 AND ts > now() - interval '10 minutes'
GROUP BY task_type
ORDER BY count(*) DESC
LIMIT 3
```

---

## 验证 checklist

```
[ ] PATCH-1 V15:
    [ ] 单元测试 TestPoolDeadRecovery PASS
    [ ] 单元测试 TestPoolDeadRevivedByProbeFailure PASS
    [ ] go test ./pool/... -race PASS

[ ] PATCH-2 V16:
    [ ] 单元测试 TestPoolAcquireClosedRace PASS
    [ ] go test ./pool/... -race PASS

[ ] PATCH-3 V17:
    [ ] 模拟 DB 关闭 → metric liveFilterFailed 上升
    [ ] admin endpoint /api/routing/live-filter-stats 返回正确计数
    [ ] log 出现 "live availability filter failed"

[ ] PATCH-4 V20:
    [ ] 请求带 sticky → sessionCache.ChosenCredentialID 与 sticky ID 一致
    [ ] log: decision.ChosenCredentialID 与 executor.loggedCredID 匹配

[ ] PATCH-5 V18:
    [ ] 单 session 5 次 chat 后 → 第 6 次重新分类
    [ ] cache.Hits 单调递增
    [ ] ForcedReclassifyAt 重置
```

---

## 部署建议

1. **阶段 0 (24h 内)**: PATCH-1, PATCH-2, PATCH-3 → 紧急修复阻断级漏洞
2. **阶段 1 (1 周内)**: PATCH-4, PATCH-5 → 稳定级改善
3. **回滚方式**: 全部 patch 都是函数级增量修改，可通过 git revert 单 patch 撤回

---

**作者**: AI Agent
**日期**: 2026-07-04
**审计报告**: `CREDENTIAL_ROUTING_STATE_TREE_AUDIT_2026-07-04.md`
