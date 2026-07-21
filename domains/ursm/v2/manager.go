// Package v2 facade. Manager is the wiring point for URSM v2 in the
// executor: it composes the v2 store, recovery gate, and rollout
// controller, and exposes the minimal surface the executor needs to
// make a sidecar write when a request succeeds or fails.
package v2

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/resource"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/rollout"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Dependencies wires the v2 facade. Redis is required; the resource
// pools and Logger are optional. Config is filled with DefaultConfig()
// when RedisKeyPrefix is empty.
type Dependencies struct {
	Redis  *redis.Client
	FP     resource.FP
	Conc   resource.Concurrency
	RPM    resource.RPM
	Config Config
	Logger Logger
}

// Logger is the minimal surface v2 needs. A no-op logger is used when
// Logger is nil.
type Logger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// Manager is the URSM v2 facade. It is safe to leave unconfigured
// (URSMv2 == nil on Executor): the executor sidecar guards each call
// on `e.URSMv2 != nil`.
type Manager struct {
	cfg      Config
	store    *store.Store
	recovery *recovery.Manager
	rollout  *rollout.Controller
	fp       resource.FP
	conc     resource.Concurrency
	rpm      resource.RPM
	log      Logger
}

// New constructs a Manager. If d.Config.RedisKeyPrefix is empty the
// facade falls back to DefaultConfig() — the caller still retains
// whatever Mode / Canary settings they passed in via the same struct
// only when the prefix is non-empty; otherwise the entire default
// config is used. (This is the T8 spec behavior; we'll revisit the
// "field-by-field defaults" question in a later task.)
func New(d Dependencies) *Manager {
	if d.Config.RedisKeyPrefix == "" {
		d.Config = DefaultConfig()
	}
	cfg := d.Config
	log := d.Logger
	if log == nil {
		log = nopLogger{}
	}
	return &Manager{
		cfg:      cfg,
		store:    store.New(d.Redis),
		recovery: recovery.New(d.Redis, cfg.RedisKeyPrefix),
		rollout: rollout.New(rollout.Config{
			Mode:          cfg.Mode,
			CanaryPercent: cfg.CanaryPercent,
			CanaryTenants: cfg.CanaryTenants,
			CanaryModels:  cfg.CanaryModels,
		}),
		fp:   d.FP,
		conc: d.Conc,
		rpm:  d.RPM,
		log:  log,
	}
}

// Mode returns the active rollout mode.
func (m *Manager) Mode() api.RolloutMode {
	if m == nil {
		return api.ModeOff
	}
	return m.rollout.Mode()
}

// ShouldUseV2 delegates to the rollout controller.
func (m *Manager) ShouldUseV2(tenant, model, requestID string) bool {
	if m == nil {
		return false
	}
	return m.rollout.ShouldUseV2(tenant, model, requestID)
}

// Ready returns the v2 recovery gate state. False means the v2
// pipeline is not authoritative yet; callers should treat v2 as off.
func (m *Manager) Ready(ctx context.Context) bool {
	if m == nil {
		return false
	}
	return m.recovery.Ready(ctx)
}

// SetReady toggles the v2 recovery gate. Used by recovery / boot flows
// and exercised by the manager tests.
func (m *Manager) SetReady(ctx context.Context, ready bool) error {
	if m == nil {
		return nil
	}
	return m.recovery.SetReady(ctx, ready)
}

// RecordRequest is the executor sidecar entry point. It is a no-op
// when:
//
//   - the receiver is nil (defensive — protects against bad wiring),
//   - the store is not configured,
//   - the rollout controller decides this request is not on the v2
//     path (ModeOff always; ModeShadow by design; ModeCanary
//     unless the canary hash / tenant / model matches),
//
// On the success/failure path the call is forwarded to the v2 store
// via the RecordRequest Lua script. The context is detached
// (context.WithoutCancel) so a cancelled request context does not
// abort the sidecar write; the configured RecordTimeoutMs bounds the
// wait.
//
// The reducer package is wired in Task 9 — kept out of T8 to keep
// this change focused on the sidecar write.
func (m *Manager) RecordRequest(ctx context.Context, ev api.RequestOutcome) error {
	if m == nil || m.store == nil {
		return nil
	}
	if !m.rollout.ShouldUseV2(ev.TenantID, ev.RawModel, ev.RequestID) {
		return nil
	}
	timeout := time.Duration(m.cfg.RecordTimeoutMs) * time.Millisecond
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if _, err := m.store.RecordRequest(rctx,
		store.NodeKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel),
		store.WindowKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel, "1m"),
		store.WindowKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel, "5m"),
		store.WindowKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel, "30m"),
		store.RecordOutcome{
			Success:      ev.Success,
			ErrorKind:    ev.ErrorKind,
			NowMs:        time.Now().UnixMilli(),
			LatencyMs:    ev.LatencyMs,
			RequestID:    ev.RequestID,
			NodeTTL:      m.cfg.NodeTTL,
			Window5mTTL:  m.cfg.Window5mTTL,
			Window30mTTL: m.cfg.Window30mTTL,
			AdminHold:    false,
		}); err != nil {
		m.log.Warn("ursm.v2: record failed", "error", err, "cid", ev.CredentialID)
		return fmt.Errorf("ursm.v2: record: %w", err)
	}
	return nil
}
