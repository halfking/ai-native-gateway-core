// Package circuit implements a circuit breaker state machine for upstream
// provider connections, with per-error-kind cooling policies and half-open probing.
//
// States:
//
//	CLOSED       — Normal operation, requests pass through
//	OPEN         — Cooling period active, requests are rejected
//	HALF_OPEN    — Probe period, one request allowed to test recovery
//	QUARANTINED  — Permanent failure (Auth/Quota), manual recovery only
package credential

import (
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// State represents the circuit breaker state.
type State int32

const (
	StateClosed      State = 0
	StateOpen        State = 1
	StateHalfOpen    State = 2
	StateQuarantined State = 3
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	case StateQuarantined:
		return "quarantined"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// ErrorKind — shared error classification from errorsx package
// ---------------------------------------------------------------------------

type ErrorKind = errorsx.ErrorKind

var (
	KindTransient          = errorsx.KindTransient
	KindTimeout            = errorsx.KindTimeout
	KindNetwork            = errorsx.KindNetwork
	KindRateLimit          = errorsx.KindRateLimit
	KindAuth               = errorsx.KindAuth
	KindQuota              = errorsx.KindQuota
	KindUpstreamDown       = errorsx.KindUpstreamDown
	KindUpstreamOverloaded = errorsx.KindUpstreamOverloaded
	KindStreamTimeout      = errorsx.KindStreamTimeout
	KindConcurrent         = errorsx.KindConcurrent
)

const (
	autoRecoveryFailureThreshold        int32 = 2 // 从 3 降到 2，更激进地降级不稳定凭据（如 NVIDIA NIM）
	exponentialRecoveryFailureThreshold int32 = 2
	permanentRecoveryFailureThreshold   int32 = 2

	// halfOpenProbeTimeout is how long a half-open probe may hold its single
	// slot before it is auto-released. Guards against a leaked probe slot
	// (Allow() consumed but no Record*/ReleaseProbe ever called, e.g. a probe
	// blocked by the concurrency/RPM limiter) permanently wedging the breaker
	// in HALF_OPEN. Set longer than a realistic single upstream call so a
	// genuinely slow probe is not force-released.
	halfOpenProbeTimeout = 5 * time.Minute
)

// ---------------------------------------------------------------------------
// CoolingPolicy — per-error-kind recovery strategy
// ---------------------------------------------------------------------------

// CoolingPolicy defines how a circuit recovers from an error.
type CoolingPolicy struct {
	InitialCooling time.Duration // First cooling period
	MaxCooling     time.Duration // Maximum cooling period (exponential backoff cap)
	RecoveryType   RecoveryType  // How the circuit recovers
	ShrinkFactor   float64       // Multiplier for concurrency limiter (0=no shrink)
}

// RecoveryType describes the recovery strategy.
type RecoveryType int

const (
	RecoveryAuto        RecoveryType = iota // Auto-recover after cooling period
	RecoveryExponential                     // Exponential backoff cooling
	RecoveryPermanent                       // Quarantine, manual recovery only
)

// Default cooling policies per error kind.
var defaultPolicies = map[ErrorKind]CoolingPolicy{
	KindTransient: {InitialCooling: 60 * time.Second, MaxCooling: 60 * time.Second, RecoveryType: RecoveryAuto, ShrinkFactor: 0},
	KindTimeout:   {InitialCooling: 60 * time.Second, MaxCooling: 60 * time.Second, RecoveryType: RecoveryAuto, ShrinkFactor: 0},
	KindNetwork:   {InitialCooling: 60 * time.Second, MaxCooling: 60 * time.Second, RecoveryType: RecoveryAuto, ShrinkFactor: 0},
	// 2026-07-24: RateLimit 从 15分钟 改为 2分钟
	// 15分钟对多轮对话场景太长，2分钟足够让上游限流恢复
	KindRateLimit: {InitialCooling: 120 * time.Second, MaxCooling: 120 * time.Second, RecoveryType: RecoveryExponential, ShrinkFactor: 0.7},
	// 2026-07-22 fix (BUG #1): KindAuth used to be RecoveryPermanent
	// (quarantine, manual recovery only). That combined with BUG #2
	// (writer.go writing availability_recover_at=NULL) meant a single
	// 401/403 from an upstream permanently disabled a credential for
	// 15+ hours until admin intervention. With writer.go:218 now
	// writing a 15-minute recover_at AND bg/credential_recovery.go:56
	// including 'auth_failed' in its whitelist, the recovery ticker
	// can flip the credential back to ready; the in-memory breaker
	// now uses exponential backoff (15min → 30min → 60min → 2h → 4h →
	// 24h cap) so a flapping credential (admin rotated the apikey but
	// the new one is also bad) degrades gracefully instead of being
	// pinned forever. After 24h of cooling the credential is
	// effectively permanent-by-time, matching the old UX.
	// 2026-07-24: Auth InitialCooling 从 15分钟 改为 5分钟，减少多轮对话中的长时间中断
	KindAuth:         {InitialCooling: 5 * time.Minute, MaxCooling: 24 * time.Hour, RecoveryType: RecoveryExponential, ShrinkFactor: 0.5},
	KindQuota:        {InitialCooling: 0, MaxCooling: 0, RecoveryType: RecoveryPermanent, ShrinkFactor: 0},
	KindUpstreamDown: {InitialCooling: 30 * time.Second, MaxCooling: 1800 * time.Second, RecoveryType: RecoveryExponential, ShrinkFactor: 0.5},
	// 2026-08-08: overload-shaped 5xx recovers far faster than a real
	// outage, so the exponential ceiling stops at 5 min instead of
	// KindUpstreamDown's 30 min. Keeping it exponential (not RecoveryAuto)
	// still punishes a relay that stays overloaded across many rounds.
	errorsx.KindUpstreamOverloaded: {InitialCooling: 30 * time.Second, MaxCooling: 5 * time.Minute, RecoveryType: RecoveryExponential, ShrinkFactor: 0.5},
	KindStreamTimeout:              {InitialCooling: 30 * time.Second, MaxCooling: 30 * time.Second, RecoveryType: RecoveryAuto, ShrinkFactor: 0},
	// 2026-07-24: Concurrent 从 5分钟 改为 2分钟，减少多轮对话中的长时间中断
	errorsx.KindConcurrent: {InitialCooling: 2 * time.Minute, MaxCooling: 2 * time.Minute, RecoveryType: RecoveryAuto, ShrinkFactor: 0.5},
}

// ---------------------------------------------------------------------------
// Breaker
// ---------------------------------------------------------------------------

// Breaker is a single circuit breaker instance, keyed by provider+credential.
type Breaker struct {
	key            string
	state          atomic.Int32
	failCount      atomic.Int32
	consecutive    atomic.Int32
	halfOpenProbes atomic.Int32
	coolingPolicy  CoolingPolicy

	mu             sync.Mutex
	lastFailureAt  time.Time
	openSince      time.Time // when the circuit was opened
	coolingExpires time.Time // when cooling ends
	coolingCycle   int       // current exponential backoff cycle
	nextProbeAt    time.Time // when half-open probe is allowed
	lastErrorKind  ErrorKind
}

// New creates a new circuit breaker for the given provider/credential.
func New(providerID, credentialID int) *Breaker {
	return NewWithPolicy(providerID, credentialID, CoolingPolicy{})
}

// NewWithPolicy creates a circuit breaker with a custom default policy.
// If policy is zero-valued, KindTransient policy is used as the default.
func NewWithPolicy(providerID, credentialID int, defaultPolicy CoolingPolicy) *Breaker {
	if defaultPolicy.InitialCooling == 0 && defaultPolicy.RecoveryType == 0 {
		defaultPolicy = defaultPolicies[KindTransient]
	}
	return &Breaker{
		key:           fmt.Sprintf("%d/%d", providerID, credentialID),
		coolingPolicy: defaultPolicy,
	}
}

// Key returns the breaker's identifier.
func (b *Breaker) Key() string { return b.key }

// State returns the current circuit state.
func (b *Breaker) State() State { return State(b.state.Load()) }

// ConsecutiveFailures returns the consecutive failure count.
func (b *Breaker) ConsecutiveFailures() int { return int(b.consecutive.Load()) }

// Allow checks whether a request should be allowed through the credential.
func (b *Breaker) Allow() bool {
	switch b.State() {
	case StateClosed:
		return true
	case StateQuarantined:
		return false
	case StateOpen:
		if b.tryTransitionToHalfOpen() {
			return b.claimProbe()
		}
		return false
	case StateHalfOpen:
		return b.claimProbe()
	default:
		return false
	}
}

// claimProbe attempts to claim the single half-open probe slot. Returns true
// only if this caller is admitted as the probe. If a previously-held probe has
// exceeded halfOpenProbeTimeout without any Record*/ReleaseProbe call (i.e. it
// leaked — the request that consumed Allow() exited without recording a
// result), the slot is force-reclaimed so the breaker cannot stay wedged in
// HALF_OPEN indefinitely (2026-07-03 incident: probe blocked by the limiter at
// executor.go AcquireAll-fail path, breaker stuck HALF_OPEN for hours until
// process restart).
//
// The whole claim is serialized under b.mu: a lock-free fast path (e.g.
// halfOpenProbes.Add(1)==1) would race the timeout-reclaim path — two
// concurrent Allow() calls right after an OPEN→HALF_OPEN transition could both
// be admitted as "the" probe (one via the fast path, one via a slow-path
// timeout check that fires against tryTransitionToHalfOpen's fresh
// nextProbeAt=time.Now()), admitting two probes to a degraded credential.
func (b *Breaker) claimProbe() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.State() != StateHalfOpen {
		return false
	}
	now := time.Now()
	if b.halfOpenProbes.Load() == 0 || now.After(b.nextProbeAt) {
		b.halfOpenProbes.Store(1)
		b.nextProbeAt = now.Add(halfOpenProbeTimeout)
		return true
	}
	return false
}

// PeekAllowed reports whether a request would currently be allowed through
// WITHOUT consuming the half-open probe slot. Read-only: safe to call from
// pre-checks that must not steal the probe from the real attempt that follows
// (e.g. the sync-retry allCircuitOpen pre-scan).
func (b *Breaker) PeekAllowed() bool {
	switch b.State() {
	case StateClosed:
		return true
	case StateQuarantined:
		return false
	case StateHalfOpen:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.halfOpenProbes.Load() == 0 || time.Now().After(b.nextProbeAt)
	case StateOpen:
		b.mu.Lock()
		defer b.mu.Unlock()
		return time.Now().After(b.coolingExpires)
	default:
		return false
	}
}

// ReleaseProbe releases the half-open probe slot without recording a result.
// Called when a request consumed the probe via Allow() but exits before
// RecordSuccess/RecordFailure — e.g. the limiter rejected it, or an
// early-continue skip path (model-not-found / client-bug / content-filter /
// stream-interrupt) decided the error is not a credential-health signal.
// Releasing the slot lets the next request probe the credential again instead
// of wedging the breaker in HALF_OPEN.
func (b *Breaker) ReleaseProbe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.State() != StateHalfOpen {
		return
	}
	if b.halfOpenProbes.Load() > 0 {
		b.halfOpenProbes.Store(0)
		b.nextProbeAt = time.Time{}
	}
}

// tryTransitionToHalfOpen checks if the cooling period has expired and
// transitions from OPEN to HALF_OPEN if so. Only returns true when this
// goroutine actually performed the OPEN→HALF_OPEN transition.
func (b *Breaker) tryTransitionToHalfOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.State() != StateOpen {
		return false
	}

	if time.Now().After(b.coolingExpires) {
		b.state.Store(int32(StateHalfOpen))
		b.halfOpenProbes.Store(0)
		b.nextProbeAt = time.Now()
		slog.Info("circuit half-open",
			"key", b.key,
			"cooling_cycle", b.coolingCycle,
		)
		return true
	}
	return false
}

// 瞬时错误族：同族错误共享连续失败计数（2026-07-09 修复 NVIDIA NIM 连续失败不降级问题）
// NVIDIA NIM 等不稳定凭据可能返回多种瞬时错误（Timeout/Network/Transient），但本质都是"不可用"。
// 如果每次错误类型切换就重置计数，会导致永远达不到降级阈值。
var transientFamily = map[ErrorKind]bool{
	KindTransient:     true,
	KindTimeout:       true,
	KindNetwork:       true,
	KindStreamTimeout: true,
}

// RecordFailure records a failure and transitions the circuit state.
func (b *Breaker) RecordFailure(kind ErrorKind) {
	// 2026-07-03 P0 fix: client bugs (tool_call_id_mismatch, invalid_request_format,
	// etc.) should not trigger circuit breaker state changes. These are client-side
	// issues, not credential/upstream health problems.
	if errorsx.IsClientBug(errorsx.ErrorKind(kind)) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	policy, ok := defaultPolicies[kind]
	if !ok {
		policy = b.coolingPolicy
	}
	// 2026-07-09: 同族错误不重置连续失败计数
	lastIsTransient := transientFamily[b.lastErrorKind]
	nowIsTransient := transientFamily[kind]
	if b.lastErrorKind != "" && b.lastErrorKind != kind && !(lastIsTransient && nowIsTransient) {
		b.consecutive.Store(0)
		if policy.RecoveryType == RecoveryExponential {
			b.coolingCycle = 0
		}
	}

	b.lastFailureAt = now
	b.lastErrorKind = kind
	consecutive := b.consecutive.Add(1)
	b.failCount.Add(1)

	threshold := failureConfirmationThreshold(policy)
	if b.State() == StateHalfOpen {
		threshold = 1
	}
	if consecutive < threshold {
		slog.Warn("circuit failure pending confirmation",
			"key", b.key,
			"error_kind", kind,
			"consecutive", consecutive,
			"threshold", threshold,
		)
		return
	}

	switch policy.RecoveryType {
	case RecoveryPermanent:
		b.state.Store(int32(StateQuarantined))
		b.coolingCycle = 0
		slog.Warn("circuit quarantined",
			"key", b.key,
			"error_kind", kind,
		)

	case RecoveryAuto, RecoveryExponential:
		b.state.Store(int32(StateOpen))
		b.halfOpenProbes.Store(0)
		b.openSince = now

		if policy.RecoveryType == RecoveryExponential {
			b.coolingCycle++
			cooling := policy.InitialCooling * time.Duration(math.Pow(2, float64(b.coolingCycle-1)))
			if cooling > policy.MaxCooling {
				cooling = policy.MaxCooling
			}
			b.coolingExpires = now.Add(cooling)
			if b.coolingCycle >= 5 {
				slog.Warn("circuit repeated cooling cycles",
					"key", b.key,
					"cycle", b.coolingCycle,
					"cooling", cooling,
					"error_kind", kind,
				)
			}
		} else {
			b.coolingExpires = now.Add(policy.InitialCooling)
			// Escalate: 3 consecutive transient/timeout/network → use exponential policy.
			// 2026-08-08 P0 Fix: do NOT escalate upstream_overloaded here. Overloaded
			// is a supplier-side short-lived condition that already gets its own
			// RecoveryExponential path (KindUpstreamOverloaded policy). Locking the
			// circuit for 30 minutes because the supplier is briefly over capacity
			// is the wrong response — and was the direct cause of every Claude/GPT
			// request failing for half an hour after a single overload blip.
			switch kind {
			case KindTransient, KindTimeout, KindNetwork, KindStreamTimeout:
				if consecutive >= autoRecoveryFailureThreshold {
					escalated := defaultPolicies[KindUpstreamDown]
					b.coolingExpires = now.Add(escalated.InitialCooling)
					slog.Warn("circuit escalated to exponential cooling",
						"key", b.key,
						"consecutive", consecutive,
						"error_kind", kind,
					)
				}
			}
		}

		slog.Warn("circuit opened",
			"key", b.key,
			"error_kind", kind,
			"cooling_until", b.coolingExpires.Format(time.RFC3339),
			"cycle", b.coolingCycle,
		)
	}
}

func failureConfirmationThreshold(policy CoolingPolicy) int32 {
	switch policy.RecoveryType {
	case RecoveryPermanent:
		return permanentRecoveryFailureThreshold
	case RecoveryExponential:
		return exponentialRecoveryFailureThreshold
	default:
		return autoRecoveryFailureThreshold
	}
}

// RecordSuccess records a success and transitions the circuit to CLOSED.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	prev := b.State()
	b.state.Store(int32(StateClosed))
	b.consecutive.Store(0)
	b.coolingCycle = 0
	b.halfOpenProbes.Store(0)
	b.lastFailureAt = time.Time{}

	if prev != StateClosed {
		slog.Info("circuit closed",
			"key", b.key,
		)
	}
}

// Reset resets the breaker to CLOSED state (admin/manual recovery).
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Store(int32(StateClosed))
	b.consecutive.Store(0)
	b.failCount.Store(0)
	b.coolingCycle = 0
	b.halfOpenProbes.Store(0)
	b.lastFailureAt = time.Time{}
	b.lastErrorKind = ""
	slog.Info("circuit reset", "key", b.key)
}

// Stats returns diagnostic information about the breaker.
func (b *Breaker) Stats() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()

	stats := map[string]any{
		"key":                  b.key,
		"state":                b.State().String(),
		"consecutive_failures": b.consecutive.Load(),
		"total_failures":       b.failCount.Load(),
		"cooling_cycle":        b.coolingCycle,
	}
	if !b.lastFailureAt.IsZero() {
		stats["last_failure_at"] = b.lastFailureAt.Format(time.RFC3339)
		stats["last_error_kind"] = string(b.lastErrorKind)
	}
	if !b.openSince.IsZero() {
		stats["open_since"] = b.openSince.Format(time.RFC3339)
	}
	if !b.coolingExpires.IsZero() {
		stats["cooling_expires"] = b.coolingExpires.Format(time.RFC3339)
	}
	return stats
}

// ---------------------------------------------------------------------------
// Manager — global registry of circuit breakers
// ---------------------------------------------------------------------------

// Manager manages all circuit breakers keyed by (provider, credential).
type Manager struct {
	mu       sync.RWMutex
	breakers map[string]*Breaker
}

// NewManager creates a new circuit breaker manager.
func NewManager() *Manager {
	return &Manager{breakers: make(map[string]*Breaker)}
}

// GetOrCreate returns the breaker for the given provider/credential.
func (m *Manager) GetOrCreate(providerID, credentialID int) *Breaker {
	key := fmt.Sprintf("%d/%d", providerID, credentialID)

	m.mu.RLock()
	b, ok := m.breakers[key]
	m.mu.RUnlock()
	if ok {
		return b
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if b, ok = m.breakers[key]; ok {
		return b
	}
	b = New(providerID, credentialID)
	m.breakers[key] = b
	return b
}

// Get returns the breaker for the given provider/credential, or nil.
func (m *Manager) Get(providerID, credentialID int) *Breaker {
	key := fmt.Sprintf("%d/%d", providerID, credentialID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.breakers[key]
}

// ReleaseProbe releases the half-open probe slot for the given
// provider/credential without recording a result. See Breaker.ReleaseProbe.
func (m *Manager) ReleaseProbe(providerID, credentialID int) {
	if b := m.Get(providerID, credentialID); b != nil {
		b.ReleaseProbe()
	}
}

// PeekAllowed reports whether a request would currently be allowed through
// WITHOUT consuming the half-open probe slot. Read-only pre-check; the caller
// must still call Allow() on the real attempt. See Breaker.PeekAllowed.
func (m *Manager) PeekAllowed(providerID, credentialID int) bool {
	if b := m.Get(providerID, credentialID); b != nil {
		return b.PeekAllowed()
	}
	return true
}

// Stats returns diagnostic information for all breakers.
func (m *Manager) Stats() []map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]map[string]any, 0, len(m.breakers))
	for _, b := range m.breakers {
		result = append(result, b.Stats())
	}
	return result
}

// ResetAll resets all breakers to CLOSED state.
func (m *Manager) ResetAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.breakers {
		b.Reset()
	}
}

// RecordFailure records a failure on the appropriate breaker.
func (m *Manager) RecordFailure(providerID, credentialID int, kind ErrorKind) {
	b := m.GetOrCreate(providerID, credentialID)
	b.RecordFailure(kind)
}

// RecordSuccess records a success on the appropriate breaker.
func (m *Manager) RecordSuccess(providerID, credentialID int) {
	b := m.GetOrCreate(providerID, credentialID)
	b.RecordSuccess()
}

// Allow checks if a request should be allowed through the credential.
func (m *Manager) Allow(providerID, credentialID int) bool {
	b := m.GetOrCreate(providerID, credentialID)
	return b.Allow()
}

// ProbeCheck performs a half-open probe: if the circuit is HALF_OPEN,
// it returns true. The caller should make a lightweight probe request
// and then call RecordSuccess/RecordFailure.
func (m *Manager) ProbeCheck(providerID, credentialID int) bool {
	b := m.GetOrCreate(providerID, credentialID)
	state := b.State()
	if state == StateHalfOpen {
		return true
	}
	// Also allow if it transitioned from OPEN to HALF_OPEN concurrently
	if state == StateOpen && b.Allow() {
		return b.State() == StateHalfOpen
	}
	return false
}

// CloseProbe completes a half-open probe by recording the result.
func (m *Manager) CloseProbe(providerID, credentialID int, success bool, kind ErrorKind) {
	b := m.GetOrCreate(providerID, credentialID)
	if success {
		b.RecordSuccess()
	} else {
		b.RecordFailure(kind)
	}
}
