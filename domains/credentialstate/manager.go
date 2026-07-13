// DEPRECATED: This file will be moved to _to-be-deprecated/routing-old/credentialstate/
// Replaced by: domains/ursm/manager.go
// Migration date: 2026-07-03
// Status: 等待 Router/Executor 适配 URSM 完成后迁移
// DO NOT use this package in new code. Use domains/ursm instead.

package credentialstate

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
	"github.com/redis/go-redis/v9"
)

// Manager 凭据状态管理器 - 统一管理所有探测结果和状态更新
type Manager struct {
	memCache      *sync.Map
	redisClient   *redis.Client
	db            *pgxpool.Pool
	memCacheTTL   time.Duration
	redisCacheTTL time.Duration
	staleTTL      time.Duration
	batchWriter   *BatchWriter

	// 探测器提交函数（函数注入，避免循环依赖）
	credProbeV2Submitter func(credID int)
	modelProbeSubmitter  func(ctx context.Context, credID int, model string) error

	// 2026-07-13: 错误触发的主动探测提交函数（bg.ActiveProbeWorker.Submit）。
	// 连续失败 ≥2 次时立即调用，让 ActiveProbeWorker 直接探测上游并把
	// 探测结果写入 request_logs（自动出现在实时请求流）。
	activeProbeSubmitter func(credID int, model string, parentReqID string)

	// 2026-07-13: 触发主动探测的连续失败阈值（默认 2，配置来自 settings.error_probe.consecutive_threshold）
	activeProbeThreshold int

	// 2026-07-13 (BUG #2 fix): pending reprobe timers. Holding a reference to the
	// *time.Timer returned by time.AfterFunc lets Stop() cancel a scheduled
	// credProbeV2 reprobe before it fires — otherwise a pending backoff timer
	// can survive a graceful shutdown and fire into a stopped/dead DB, or worse
	// race with a freshly-restarted process. Each entry holds the timer plus
	// the (credID, model) it was scheduled for so duplicates can be deduped.
	pendingTimersMu sync.Mutex
	pendingTimers   map[string]*time.Timer // key = fmt.Sprintf("%d:%s", credID, model)

	// Phase 2: 模型热度追踪器（可选，nil 时禁用热度感知探测）
	popularityTracker *ModelPopularityTracker

	// 2026-07-03: 候选缓存失效函数（函数注入，避免循环依赖）
	invalidateCandidateCache func()
}

// CacheEntry 缓存条目
type CacheEntry struct {
	State     *State
	ExpiresAt time.Time
}

// NewManager 创建状态管理器
func NewManager(db *pgxpool.Pool, redisClient *redis.Client) *Manager {
	m := &Manager{
		memCache:      &sync.Map{},
		redisClient:   redisClient,
		db:            db,
		memCacheTTL:   10 * time.Second,
		redisCacheTTL: 5 * time.Minute,
		staleTTL:      2 * time.Minute,
		pendingTimers: make(map[string]*time.Timer),
	}
	m.batchWriter = NewBatchWriter(db, 5*time.Second, 100)
	return m
}

// Start 启动管理器
func (m *Manager) Start(ctx context.Context) {
	m.batchWriter.Start(ctx)
	slog.Info("credential state manager started",
		"mem_cache_ttl", m.memCacheTTL,
		"redis_cache_ttl", m.redisCacheTTL,
		"stale_ttl", m.staleTTL)
}

// Stop 停止管理器
func (m *Manager) Stop() {
	m.batchWriter.Stop()
	// 2026-07-13 (BUG #2 fix): cancel any in-flight tiered reprobe timers
	// so a graceful shutdown does not race with a delayed SubmitFastProbe.
	// Stop() on a fired timer is a no-op, so this is safe at any phase.
	m.pendingTimersMu.Lock()
	for k, t := range m.pendingTimers {
		if t != nil {
			t.Stop()
		}
		delete(m.pendingTimers, k)
	}
	m.pendingTimersMu.Unlock()
}

// SetProbeSubmitter 设置快速探测提交函数（避免循环依赖）
func (m *Manager) SetProbeSubmitter(credFn func(int), modelFn func(context.Context, int, string) error) {
	m.credProbeV2Submitter = credFn
	m.modelProbeSubmitter = modelFn
}

// SetActiveProbeSubmitter (2026-07-13) 注册错误触发的主动探测提交器。
// 当 UpdateOnFailure 累计到 consecutive_fails >= activeProbeThreshold 时
// 立即调用 fn，fn 由 bg.ActiveProbeWorker.Submit 实现。
//
// threshold 传 0 或负数使用默认值 2。
func (m *Manager) SetActiveProbeSubmitter(fn func(credID int, model string, parentReqID string), threshold int) {
	m.activeProbeSubmitter = fn
	if threshold > 0 {
		m.activeProbeThreshold = threshold
	} else if m.activeProbeThreshold <= 0 {
		m.activeProbeThreshold = 2
	}
}

// SetInvalidateCandidateCache 设置候选缓存失效函数（避免循环依赖）
// 2026-07-03: Added to fix bug #8 - UpdateOnFailure must invalidate candidate cache
func (m *Manager) SetInvalidateCandidateCache(fn func()) {
	m.invalidateCandidateCache = fn
}

// UpdateOnSuccess 请求成功时更新状态
func (m *Manager) UpdateOnSuccess(ctx context.Context, credID int, model string, latencyMs int, requestID string) {
	key := m.cacheKey(credID, model)

	state, _ := m.getFromMemCache(key)
	if state == nil {
		state = &State{
			CredentialID: credID,
			Model:        model,
		}
	}

	now := time.Now()
	state.Available = true
	state.HealthStatus = "healthy"
	state.LastSuccessAt = &now
	state.LastUpdatedAt = now
	state.ConsecutiveFails = 0

	// 移动平均延迟
	if state.AvgLatencyMs == 0 {
		state.AvgLatencyMs = latencyMs
	} else {
		state.AvgLatencyMs = (state.AvgLatencyMs*3 + latencyMs) / 4
	}

	state.Source = "request"

	m.setToMemCache(key, state)
	go func() {
		redisCtx, cancel := runctx.DetachedTimeout(ctx, 3*time.Second)
		defer cancel()
		m.setToRedis(redisCtx, key, state)
	}()

	avail := true
	m.batchWriter.Add(StateUpdate{
		CredentialID:  credID,
		Model:         model,
		Available:     &avail,
		LatencyMs:     &latencyMs,
		LastSuccessAt: &now,
		UpdatedAt:     now,
	})

	slog.Debug("credstate: success updated",
		"credential_id", credID,
		"model", model,
		"latency_ms", latencyMs)
}

// UpdateOnFailure 请求失败时更新状态（含闪断保护）
func (m *Manager) UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID string) {
	// 2026-07-01: 过滤不应计入凭据错误统计的情况
	// 1. 用户取消（KindCanceled）：用户主动取消请求，不是凭据问题
	// 2. 客户端错误（IsClientBug）：包括 model_not_found, tool_call_id_mismatch, unsupported_feature
	//    这些是客户端使用问题，不应惩罚凭据
	if errKind == errorsx.KindCanceled || errorsx.IsClientBug(errKind) {
		return
	}

	key := m.cacheKey(credID, model)

	state, _ := m.getFromMemCache(key)
	if state == nil {
		state = &State{
			CredentialID: credID,
			Model:        model,
		}
	}

	now := time.Now()
	state.LastFailureAt = &now
	state.LastUpdatedAt = now
	state.ConsecutiveFails++
	state.LastError = string(errKind)
	state.Source = "request"

	// 智能探测策略：
	// 1. 临时故障（429/503/timeout）：连续失败 >= 3 → 30秒后验证，间隔递增 (30s → 2m → 5m)
	// 2. 永久故障（auth/quota/model_not_found）：连续失败 >= 2 → 标记 broken，探测间隔 15分钟
	// 3. 闪断保护：2秒内有成功 → 不触发探测
	//
	// 2026-07-13: 错误触发的主动探测 (active_probe)。
	// 当连续失败 >= activeProbeThreshold（默认 2）时立即调用 activeProbeSubmitter，
	// bg.ActiveProbeWorker 会按 5s → 30s → 2m → 5m → 15m backoff 链直连上游探测，
	// 每轮结果写 request_logs，自动出现在实时请求流。
	// active_probe 与下面的"永久/临时分级探测"并行：
	//   - active_probe 优先用于快速隔离"上游 vs gateway"问题，0s 延迟
	//   - credProbeV2（5min 延迟）作为兜底
	if m.activeProbeSubmitter != nil && state.ConsecutiveFails >= m.activeProbeThreshold {
		// 闪断保护：2秒内有成功 → 不触发探测，避免误判瞬时网络抖动
		if state.LastSuccessAt == nil || now.Sub(*state.LastSuccessAt) > 2*time.Second {
			slog.Info("credstate: triggering active_probe",
				"credential_id", credID,
				"model", model,
				"error_kind", errKind,
				"consecutive_fails", state.ConsecutiveFails,
				"threshold", m.activeProbeThreshold,
				"parent_request_id", requestID,
			)
			m.activeProbeSubmitter(credID, model, requestID)
		}
	}

	isTransient := errKind == errorsx.KindRateLimit ||
		errKind == errorsx.KindUpstreamDown ||
		errKind == errorsx.KindTimeout ||
		errKind == errorsx.KindStreamTimeout

	isPermanent := errKind == errorsx.KindAuth ||
		errKind == errorsx.KindAuthRevoked ||
		errKind == errorsx.KindModelNotFound ||
		errKind == errorsx.KindQuotaPermanent

	if isPermanent && state.ConsecutiveFails >= 2 {
		// 永久故障：快速标记为 broken，降低探测频率（15分钟）
		state.Available = false
		nextRetry := now.Add(15 * time.Minute)
		state.RecoverAt = &nextRetry

		slog.Warn("credstate: permanent failure detected",
			"credential_id", credID,
			"model", model,
			"error_kind", errKind,
			"consecutive_fails", state.ConsecutiveFails,
			"next_retry", nextRetry)

		// 2026-07-03: Bug #8 fix - invalidate candidate cache when marking unavailable
		// Without this, the router sees stale candidate list for 30s (cache TTL)
		if m.invalidateCandidateCache != nil {
			m.invalidateCandidateCache()
		}

	} else if isTransient && state.ConsecutiveFails >= 3 {
		// 临时故障（429/503/timeout/stream_timeout）：连续失败 >= 3。
		//
		// 2026-07-09 修正（问题2 - NVIDIA NIM 流式无反馈长时间未熔断）：
		// 之前此处只调度探测，凭据仍保持 Available=true 继续接收请求，
		// 导致流式无反馈（first_byte_timeout / stream_timeout，归为
		// KindStreamTimeout）的凭据长时间挂在路由池里，前端持续无响应。
		//
		// 现在：连续 3 次临时故障立即把凭据置为 cooling（Available=false，
		// RecoverAt = now + 5min），并失效候选缓存，让路由在下一次候选
		// 解析时立即剔除该凭据。探测恢复（UpdateFromProbe）会在凭据真正
		// 恢复后把 Available 翻回 true。
		const transientCooling = 5 * time.Minute
		state.Available = false
		nextRetry := now.Add(transientCooling)
		state.RecoverAt = &nextRetry

		slog.Warn("credstate: transient failure threshold reached, credential cooling",
			"credential_id", credID,
			"model", model,
			"error_kind", errKind,
			"consecutive_fails", state.ConsecutiveFails,
			"cooling_seconds", int(transientCooling.Seconds()),
			"recover_at", nextRetry)

		if m.invalidateCandidateCache != nil {
			m.invalidateCandidateCache()
		}

		// 递增退避探测 (30s → 2m → 5m)：探测用于在 cooling 期间提前发现
		// 凭据恢复，探测成功后 UpdateFromProbe 会立即恢复路由。
		//
		// 2026-07-13 (BUG #2 fix): the previous code computed `backoff`
		// (30s / 2m / 5m depending on the consecutive_failures count) but
		// then fired credProbeV2Submitter immediately — the backoff value
		// was unused. credProbeV2 itself adds its own 5-minute delay, so
		// the effective schedule collapsed to "always 5 minutes" regardless
		// of how many consecutive failures had accumulated. This made the
		// "分级回退" tiered retry a documentation-only feature.
		//
		// After the fix we actually honor the backoff via time.AfterFunc.
		// The timer is keyed by (credID, model) and stored on the manager
		// so Stop() can cancel a pending reprobe at shutdown.
		var backoff time.Duration
		switch {
		case state.ConsecutiveFails <= 3:
			backoff = 30 * time.Second
		case state.ConsecutiveFails <= 5:
			backoff = 2 * time.Minute
		default:
			backoff = 5 * time.Minute
		}

		if state.LastSuccessAt == nil || now.Sub(*state.LastSuccessAt) > 2*time.Second {
			slog.Info("credstate: transient failure, scheduling reprobe",
				"credential_id", credID,
				"model", model,
				"consecutive_fails", state.ConsecutiveFails,
				"backoff", backoff)

			m.scheduleCredProbe(credID, model, backoff)
		}
	}

	m.setToMemCache(key, state)
	go func() {
		redisCtx, cancel := runctx.DetachedTimeout(ctx, 3*time.Second)
		defer cancel()
		m.setToRedis(redisCtx, key, state)
	}()

	errStr := string(errKind)
	m.batchWriter.Add(StateUpdate{
		CredentialID:  credID,
		Model:         model,
		LastFailureAt: &now,
		LastError:     &errStr,
		UpdatedAt:     now,
	})

	slog.Debug("credstate: failure updated",
		"credential_id", credID,
		"model", model,
		"error_kind", errKind,
		"consecutive_fails", state.ConsecutiveFails)
}

// UpdateFromProbe 探测结果更新状态（权威来源）
func (m *Manager) UpdateFromProbe(ctx context.Context, state *State) {
	key := m.cacheKey(state.CredentialID, state.Model)

	// 2026-07-04 Bug #8 fix (part 2): invalidate candidate cache when
	// probe flips Available from false → true. Without this, router sees
	// stale candidate list (without the newly-recovered credential) for
	// up to 30s (cache TTL). This complements the UpdateOnFailure fix
	// (part 1) which invalidates on true → false transition.
	oldState, _ := m.getFromMemCache(key)
	if oldState != nil && !oldState.Available && state.Available {
		if m.invalidateCandidateCache != nil {
			m.invalidateCandidateCache()
		}
		slog.Info("credstate: probe recovered credential, invalidated candidate cache",
			"credential_id", state.CredentialID,
			"model", state.Model,
		)
	}

	m.setToMemCache(key, state)
	go func() {
		redisCtx, cancel := runctx.DetachedTimeout(ctx, 3*time.Second)
		defer cancel()
		m.setToRedis(redisCtx, key, state)
	}()

	slog.Debug("credstate: probe result updated",
		"credential_id", state.CredentialID,
		"model", state.Model,
		"available", state.Available,
		"source", state.Source)
}

// GetState 查询状态（三层缓存：内存 → Redis → DB）
func (m *Manager) GetState(ctx context.Context, credID int, model string) (*State, error) {
	key := m.cacheKey(credID, model)

	// L1: 内存缓存
	if state, ok := m.getFromMemCache(key); ok {
		return state, nil
	}

	// L2: Redis缓存
	if state, err := m.getFromRedis(ctx, key); err == nil && state != nil {
		m.setToMemCache(key, state)
		return state, nil
	}

	// L3: DB查询（最后手段）
	state, err := m.getFromDB(ctx, credID, model)
	if err != nil {
		return nil, err
	}

	if state != nil {
		m.setToMemCache(key, state)
		go m.setToRedis(ctx, key, state)
	}

	return state, nil
}

// IsAvailable 快速判断是否可用
func (m *Manager) IsAvailable(ctx context.Context, credID int, model string) (bool, string) {
	state, err := m.GetState(ctx, credID, model)
	if err != nil {
		return true, "" // 查询失败时回退到原有逻辑
	}
	if state == nil {
		return true, ""
	}
	if !state.Available {
		return false, state.LastError
	}
	return true, ""
}

// GetStaleStates 获取过期状态（用于触发自动 ping）
func (m *Manager) GetStaleStates(staleTTL time.Duration) []*State {
	var stale []*State
	now := time.Now()

	m.memCache.Range(func(k, v interface{}) bool {
		entry := v.(*CacheEntry)
		if now.Sub(entry.State.LastUpdatedAt) > staleTTL {
			stale = append(stale, entry.State)
		}
		return true
	})

	return stale
}

// TriggerPing 触发单个凭据+模型的探测
func (m *Manager) TriggerPing(ctx context.Context, credID int, model string) {
	if m.modelProbeSubmitter != nil {
		go func() {
			probeCtx, cancel := runctx.DetachedTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := m.modelProbeSubmitter(probeCtx, credID, model); err != nil {
				slog.Warn("credstate: trigger ping failed",
					"credential_id", credID,
					"model", model,
					"error", err)
			}
		}()
	}
}

func (m *Manager) cacheKey(credID int, model string) string {
	return fmt.Sprintf("%d:%s", credID, model)
}

// scheduleCredProbe enqueues a delayed credProbeV2 submission for the
// (credID, model) pair, honoring the tiered backoff (30s / 2m / 5m).
//
// 2026-07-13 (BUG #2 fix): this is the implementation that the previous
// `m.credProbeV2Submitter(credID)` call site silently bypassed. Calling
// the submitter immediately defeated the documented "分级回退" tiered
// retry and effectively made every transient-failure reprobe fixed at
// credProbeV2's own 5-minute fastReprobeDelay.
//
// Behaviour:
//   - If no submitter is wired or backoff <= 0, fires immediately
//     (preserves the legacy "best-effort" semantics for tests).
//   - If the same (credID, model) already has a pending timer, replaces
//     it (the latest failure should always own the schedule — older
//     timers are stale).
//   - If backoff is zero, fires synchronously via goroutine to match the
//     call-site's existing fire-and-forget style.
//   - Stores the *time.Timer on the manager so Stop() can cancel it.
func (m *Manager) scheduleCredProbe(credID int, model string, backoff time.Duration) {
	if m == nil || m.credProbeV2Submitter == nil {
		return
	}
	key := fmt.Sprintf("%d:%s", credID, model)

	// Replace any prior pending timer for this (cred, model) pair so the
	// newest failure controls the schedule. Stop() on an already-fired
	// timer returns false but is otherwise a safe no-op.
	m.pendingTimersMu.Lock()
	if prev, ok := m.pendingTimers[key]; ok && prev != nil {
		prev.Stop()
		delete(m.pendingTimers, key)
	}
	m.pendingTimersMu.Unlock()

	fire := func() {
		// Clear our slot before firing so subsequent failures can re-arm
		// without racing with the in-flight submission.
		m.pendingTimersMu.Lock()
		delete(m.pendingTimers, key)
		m.pendingTimersMu.Unlock()
		m.credProbeV2Submitter(credID)
	}

	if backoff <= 0 {
		go fire()
		return
	}

	timer := time.AfterFunc(backoff, fire)
	m.pendingTimersMu.Lock()
	m.pendingTimers[key] = timer
	m.pendingTimersMu.Unlock()
}

// Enabled 是否启用
func (m *Manager) Enabled() bool {
	return m != nil && m.db != nil
}

// EnablePopularityTracking 启用模型热度追踪（Phase 2 特性）
//
// 启用后，Manager 会基于 request_logs 统计模型调用频率，
// 动态调整探测间隔：
//   - 高频模型（>100 req/h）：10秒探测
//   - 中频模型（10-100 req/h）：2分钟探测
//   - 低频模型（<10 req/h）：10分钟探测
//
// 必须在 Start() 之前调用。
func (m *Manager) EnablePopularityTracking() {
	if m.db == nil {
		slog.Warn("credstate: cannot enable popularity tracking without database")
		return
	}
	m.popularityTracker = NewModelPopularityTracker(m.db)
	slog.Info("credstate: popularity tracking enabled")
}

// GetRecommendedProbeInterval 返回模型的推荐探测间隔（基于热度）
//
// 如果未启用热度追踪，返回默认值 5 分钟。
func (m *Manager) GetRecommendedProbeInterval(model string) time.Duration {
	if m.popularityTracker == nil {
		return 5 * time.Minute // 默认：中等间隔
	}
	return m.popularityTracker.GetProbeInterval(model)
}
