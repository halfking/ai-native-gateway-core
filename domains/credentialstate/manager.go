// Package credentialstate - 凭据状态管理器（LEGACY）
//
// ⚠️ LEGACY: 此包已被 domains/ursm/v2 替代，但保留用于回退路径。
//
// 演进历史:
//   - v1.0: 初版内存缓存（10s TTL）
//   - v2.0: 添加 Redis 后端（双层缓存）
//   - 2026-07: 被 domains/ursm/v2 替代（Redis 单一权威源）
//
// Phase 3 (2026-07-25) 状态:
//   - ✅ 已被 LegacyStateBackend 封装（state_backend.go）
//   - ✅ 仍用于 URSM v2 authoritative 模式的 fail-open 回退
//   - ✅ 保留回滚能力（URSM_V2_MODE=off 一键切换）
//
// 未来计划:
//   - v3.0 (ETA: 2026-Q4): 完全移除（URSM v2 充分验证后）
//
// 维护原则:
//   - 接受 bug 修复（回退路径必须可用）
//   - 不接受新功能（不再添加依赖）
//   - 新代码请使用 StateBackend 接口（state_backend.go）
//
// 替代关系:
//   - StateBackend.IsAvailable() // 由 LegacyStateBackend 包装此包
//   - domains/ursm/v2.Manager.FilterAndScore() // authoritative 模式
//   - domains/ursm/v2.Manager.Plan() // canary 模式
//
// 回退测试:
//   URSM_V2_MODE=off 确保此包路径正常工作

package credentialstate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
	"github.com/redis/go-redis/v9"
)

// onNoCandidatesFanoutLimit returns the optional per-request fan-out cap.
// Zero or an invalid value means all candidates are dispatched; the probe
// worker still deduplicates pairs and applies its own bounded drain.
func onNoCandidatesFanoutLimit() int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("LLM_GATEWAY_NO_CANDIDATE_PROBE_FANOUT")))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

const noCandidatesDebounceWindow = 5 * time.Second

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
	activeProbeSubmitter func(credID int, model string, tenantID string, parentReqID string)

	// 2026-07-13: 触发主动探测的连续失败阈值（默认 2，配置来自 settings.error_probe.consecutive_threshold）
	activeProbeThreshold int

	// 2026-07-13 (BUG #2 fix): pending reprobe timers. Holding a reference to the
	// *time.Timer returned by time.AfterFunc lets Stop() cancel a scheduled
	// credProbeV2 reprobe before it fires — otherwise a pending backoff timer
	// can survive a graceful shutdown and fire into a stopped/dead DB, or worse
	// race with a freshly-restarted process. Pending work is credential-level
	// because CredentialProbeV2 probes a credential rather than a model.
	pendingTimersMu sync.Mutex
	pendingTimers   map[string]*pendingProbeTimer // key = credential ID
	nextTimerGen    uint64
	stopped         bool
	callbacks       sync.WaitGroup

	// Phase 2: 模型热度追踪器（可选，nil 时禁用热度感知探测）
	popularityTracker *ModelPopularityTracker

	// Candidate cache invalidator scoped to a credential. Per-state
	// transitions must not flush unrelated models and tenants back to the DB.
	invalidateCandidateCache func(credentialID int)

	noCandidatesMu         sync.Mutex
	noCandidatesDispatched map[string]time.Time
}

// CacheEntry 缓存条目
type CacheEntry struct {
	State     *State
	ExpiresAt time.Time
}

type pendingProbeTimer struct {
	timer *time.Timer
	gen   uint64
	dueAt time.Time
}

// NewManager 创建状态管理器
func NewManager(db *pgxpool.Pool, redisClient *redis.Client) *Manager {
	m := &Manager{
		memCache:               &sync.Map{},
		redisClient:            redisClient,
		db:                     db,
		memCacheTTL:            10 * time.Second,
		redisCacheTTL:          5 * time.Minute,
		staleTTL:               2 * time.Minute,
		pendingTimers:          make(map[string]*pendingProbeTimer),
		noCandidatesDispatched: make(map[string]time.Time),
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
	m.pendingTimersMu.Lock()
	m.stopped = true
	for k, pending := range m.pendingTimers {
		if pending != nil && pending.timer != nil {
			pending.timer.Stop()
		}
		delete(m.pendingTimers, k)
	}
	m.pendingTimersMu.Unlock()
	m.callbacks.Wait()
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
func (m *Manager) SetActiveProbeSubmitter(fn func(credID int, model string, tenantID string, parentReqID string), threshold int) {
	m.activeProbeSubmitter = fn
	if threshold > 0 {
		m.activeProbeThreshold = threshold
	} else if m.activeProbeThreshold <= 0 {
		m.activeProbeThreshold = 2
	}
}

// SetInvalidateCandidateCache sets the credential-scoped candidate cache
// invalidator. Broad provider or admin mutations continue to use the separate
// global invalidator in provider.
func (m *Manager) SetInvalidateCandidateCache(fn func(credentialID int)) {
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
//
// 探测触发时使用 requestID 的来源租户，使 request_logs 中的探测行归属
// 到正确的租户，而不是先前硬编码的 "system"。
//
// 2026-07-14: billingMode 参数。当 billingMode=="free" 且 errKind 是 transient
// （timeout/network/rate_limit/upstream_down/stream_timeout）时，不进入 cooling
// （不设 Available=false），让该凭据留在路由池里，仅靠 RecentSuccessRate 软降权
// 排到健康凭据之后。产品原则：50% 成功率的免费凭据"有总比没有强"。
// 永久错误（auth/model_not_found/quota_permanent）对免费凭据仍硬剔。
func (m *Manager) UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID, tenantID, billingMode string) {
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
			// 2026-07-14: 免费凭据首次出现（无历史状态）时，Available 初始化为
			// true。否则 Go 零值 false 会让一次 transient 失败就把它判为不可用
			// （IsAvailable 读 !state.Available），违背"免费凭据 transient 不硬剔"
			// 的原则。付费凭据默认 false（保持历史行为：一次失败即不可用，靠
			// success/probe 恢复）。
			Available: billingMode == "free",
		}
	}

	now := time.Now()
	state.LastFailureAt = &now
	state.LastUpdatedAt = now
	state.ConsecutiveFails++
	state.LastError = string(errKind)
	state.Source = "request"

	// 2026-07-14: 免费凭据 transient 失败——确保 Available 保持 true。
	// 即使之前因别的原因（如历史 cooling 翻 false）处于 false，只要这次是
	// transient 且凭据免费，就把它留在路由池里（软降权）。永久错误不在此列，
	// 下面 permanent 分支会正常翻 false。
	isTransient := errKind == errorsx.KindRateLimit ||
		errKind == errorsx.KindUpstreamDown ||
		errKind == errorsx.KindTimeout ||
		errKind == errorsx.KindStreamTimeout

	if billingMode == "free" && isTransient {
		state.Available = true
	}

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
	//   - credProbeV2（按 30s/2m/5m 分级延迟后立即探测）作为兜底
	// A timeout/network failure must start probing after the first confirmed
	// failure. Waiting for a second request is too late for streaming clients:
	// they commonly cancel while the upstream is still waiting for headers.
	probeImmediately := errKind == errorsx.KindNetwork ||
		errKind == errorsx.KindTimeout ||
		errKind == errorsx.KindUpstreamDown ||
		errKind == errorsx.KindStreamTimeout ||
		errKind == errorsx.KindTransient
	if m.activeProbeSubmitter != nil &&
		(probeImmediately || state.ConsecutiveFails >= m.activeProbeThreshold) {
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
			m.activeProbeSubmitter(credID, model, tenantID, requestID)
		}
	}

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
			m.invalidateCandidateCache(credID)
		}

	} else if isTransient && state.ConsecutiveFails >= 3 {
		// 2026-07-14: 免费凭据（billing_mode=free）对 transient 错误容忍。
		// 不进入 cooling（不设 Available=false / RecoverAt、不失效候选缓存、
		// 不调度 cooling 探测），让该凭据留在路由池里。不稳定性靠
		// RecentSuccessRate → router loadScore quality 分（权重 0.2）软降权，
		// 排到健康凭据之后。产品原则：50% 成功率的免费凭据"有总比没有强"。
		// 注意：上面的 ConsecutiveFails++ / LastError 仍执行，观测与软降权
		// 信号不丢；永久错误分支也不受影响（免费凭据坏 key 仍硬剔）。
		if billingMode == "free" {
			slog.Info("credstate: free credential transient failures tolerated (soft demote only)",
				"credential_id", credID,
				"model", model,
				"error_kind", errKind,
				"consecutive_fails", state.ConsecutiveFails)
			// 跳过 cooling：state.Available 保持原值（未翻 false），
			// 不设 RecoverAt，不 invalidateCandidateCache，不 scheduleCredProbe。
			// 探测恢复仍由其它路径（stale TTL / active probe）驱动。
		} else {
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
				m.invalidateCandidateCache(credID)
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
		} // end non-free cooling
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
			m.invalidateCandidateCache(state.CredentialID)
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

// OnNoCandidates (2026-07-14) fans out per-candidate probes when the
// router reports zero available nodes. The previous code path emitted
// only a failed request_logs row (kind="no_candidate") and stopped
// there, leaving operators with no follow-up investigation artifact
// beyond the per-request breakdown. This is the missing link that
// produced the 2026-07-14 minimax-m3 incident where every request for
// ~30 minutes returned "no available nodes" with no probe history.
//
// Behaviour:
//   - Filter out credential_id == 0 (placeholder / phantom rows).
//   - Dedup (credID, model) pairs so the active_probe worker can apply
//     its own dedup map without us double-firing.
//   - Cap the fan-out at OnNoCandidatesFanoutLimit (8) so a request with
//     30 candidates does not flood the worker queue.
//   - Submit one (credID, model) pair per call to activeProbeSubmitter,
//     reusing the existing ActiveProbeWorker.Submit signature so the
//     probe appears in the live request stream and request_logs with
//     task_type='probe_triggered'.
//
// Safe when no activeProbeSubmitter is wired: emits a debug log and
// returns without touching any state. Safe to call concurrently — the
// underlying Submit is goroutine-safe.
func (m *Manager) OnNoCandidates(ctx context.Context, sig NoCandidatesSignal) {
	if m == nil {
		return
	}
	if m.activeProbeSubmitter == nil {
		slog.Debug("credstate: OnNoCandidates received but no active_probe submitter wired",
			"client_model", sig.ClientModel,
			"request_id", sig.RequestID,
			"candidates", len(sig.Candidates))
		return
	}
	if len(sig.Candidates) == 0 {
		return
	}
	debounceKey := sig.TenantID + "|" + strings.ToLower(strings.TrimSpace(sig.ClientModel))
	now := time.Now()
	m.noCandidatesMu.Lock()
	if last, ok := m.noCandidatesDispatched[debounceKey]; ok && now.Sub(last) < noCandidatesDebounceWindow {
		m.noCandidatesMu.Unlock()
		slog.Debug("credstate: OnNoCandidates debounced", "client_model", sig.ClientModel, "tenant_id", sig.TenantID)
		return
	}
	m.noCandidatesDispatched[debounceKey] = now
	for key, at := range m.noCandidatesDispatched {
		if now.Sub(at) > 2*noCandidatesDebounceWindow {
			delete(m.noCandidatesDispatched, key)
		}
	}
	m.noCandidatesMu.Unlock()

	// Dedup (credID, model) — different credentials can serve the same
	// outbound model; only the first wins.
	seen := make(map[string]struct{}, len(sig.Candidates))
	dispatched := 0
	fanoutLimit := onNoCandidatesFanoutLimit()

	for _, c := range sig.Candidates {
		if c.CredentialID == 0 || c.RawModel == "" {
			continue
		}
		k := fmt.Sprintf("%d|%s", c.CredentialID, c.RawModel)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}

		// Mirror UpdateOnFailure's flash-protection: if this pair was
		// just touched by a successful request, the no-candidates
		// signal is almost certainly stale (race between router
		// snapshot and the live state manager). Skip to avoid
		// spamming probes on a credential that just worked.
		cacheK := m.cacheKey(c.CredentialID, c.RawModel)
		if cached, ok := m.getFromMemCache(cacheK); ok && cached.LastSuccessAt != nil {
			if time.Since(*cached.LastSuccessAt) < 2*time.Second {
				slog.Debug("credstate: OnNoCandidates skipping probe — flash-protection",
					"credential_id", c.CredentialID,
					"model", c.RawModel,
					"last_success_at", cached.LastSuccessAt.Format(time.RFC3339Nano),
				)
				continue
			}
		}

		m.activeProbeSubmitter(c.CredentialID, c.RawModel, sig.TenantID, sig.RequestID)
		dispatched++
		if fanoutLimit > 0 && dispatched >= fanoutLimit {
			slog.Info("credstate: OnNoCandidates fan-out cap reached",
				"client_model", sig.ClientModel,
				"request_id", sig.RequestID,
				"cap", fanoutLimit,
				"remaining_candidates", len(sig.Candidates)-dispatched,
			)
			break
		}

	}

	if dispatched > 0 {
		slog.Info("credstate: OnNoCandidates dispatched active_probe",
			"client_model", sig.ClientModel,
			"tenant_id", sig.TenantID,
			"request_id", sig.RequestID,
			"probes_dispatched", dispatched,
			"candidates_considered", len(sig.Candidates),
		)
	}
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

// scheduleCredProbe enqueues one delayed credential-level probe, honoring the
// tiered backoff. CredentialProbeV2 probes a credential (not a model), so
// pending work is deduplicated by credential ID. If a later failure requests
// a later due time, the existing earlier probe is retained.
//
// 2026-07-13 (BUG #2 fix): this is the implementation that the previous
// `m.credProbeV2Submitter(credID)` call site silently bypassed. Calling
// the submitter immediately defeated the documented "分级回退" tiered
// retry and effectively made every transient-failure reprobe wait for an
// unintended additional five-minute delay.
//
// Behaviour:
//   - If no submitter is wired or backoff <= 0, fires immediately
//     (preserves the legacy "best-effort" semantics for tests).
//   - A pending earlier probe for the same credential is retained; a new
//     request only replaces it when it would run sooner.
//   - Stores the *time.Timer on the manager so Stop() can cancel it.
func (m *Manager) scheduleCredProbe(credID int, model string, backoff time.Duration) {
	if m == nil || m.credProbeV2Submitter == nil {
		return
	}
	key := fmt.Sprintf("%d", credID)
	dueAt := time.Now().Add(backoff)

	m.pendingTimersMu.Lock()
	if m.stopped {
		m.pendingTimersMu.Unlock()
		return
	}
	if prev, ok := m.pendingTimers[key]; ok && prev != nil {
		if !prev.dueAt.IsZero() && !dueAt.Before(prev.dueAt) {
			m.pendingTimersMu.Unlock()
			return
		}
		if prev.timer != nil {
			prev.timer.Stop()
		}
	}
	m.nextTimerGen++
	generation := m.nextTimerGen
	pending := &pendingProbeTimer{gen: generation, dueAt: dueAt}
	m.pendingTimers[key] = pending
	m.pendingTimersMu.Unlock()

	fire := func() {
		m.pendingTimersMu.Lock()
		current, ok := m.pendingTimers[key]
		if !ok || current != pending || current.gen != generation || m.stopped {
			m.pendingTimersMu.Unlock()
			return
		}
		delete(m.pendingTimers, key)
		m.callbacks.Add(1)
		submitter := m.credProbeV2Submitter
		m.pendingTimersMu.Unlock()
		defer m.callbacks.Done()
		if submitter != nil {
			submitter(credID)
		}
	}

	if backoff <= 0 {
		// The callback still goes through the same generation/stopped checks.
		go fire()
		return
	}

	m.pendingTimersMu.Lock()
	if current, ok := m.pendingTimers[key]; !ok || current != pending || m.stopped {
		m.pendingTimersMu.Unlock()
		return
	}
	pending.timer = time.AfterFunc(backoff, fire)
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
