// Package orchestrator runs the blue-green state machine.
//
// State flow: NOTIFIED → PREPARING → PREPARED → ACTIVATING → DRAINING → DONE
//                       ↘ FAILED   ↘  FAILED        ↘ FAILED
//
// The human approval gate is the PREPARED state — nothing crosses
// ACTIVATING without an explicit Apply call.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/backend"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
)

// Migrator abstracts `gateway migrate` invocation (orchestrator stays decoupled
// from subprocess execution — daemon injects the real implementation).
type Migrator interface {
	Migrate(ctx context.Context) error
}

// ActiveSwitcher abstracts proxy.SwitchActive.
type ActiveSwitcher interface {
	SwitchActive(addr string)
}

// Config configures the orchestrator.
type Config struct {
	Store          *store.Store
	Backend        backend.Backend
	Migrator       Migrator
	ActiveSwitcher ActiveSwitcher
	GatewayBinary  string        // systemd backend uses (compose ignores)
	CurrentVersion string
	CurrentAddr    string
	HealthRetries  int           // spec: 5
	HealthInterval time.Duration // spec: 1s
	RetainDuration time.Duration // spec: 1h (blue retained post-drain)
}

type Orchestrator struct {
	cfg Config
}

func New(cfg Config) *Orchestrator {
	if cfg.HealthRetries == 0 {
		cfg.HealthRetries = 5
	}
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = 1 * time.Second
	}
	if cfg.RetainDuration == 0 {
		cfg.RetainDuration = 1 * time.Hour
	}
	return &Orchestrator{cfg: cfg}
}

// Prepare runs: Stage green → Migrate → Health gate → PREPARED.
// On any failure: cleans up green, plan marked FAILED.
func (o *Orchestrator) Prepare(ctx context.Context, current, target store.Release) (*store.Plan, error) {
	plan := &store.Plan{
		ID:         newPlanID(),
		CreatedAt:  time.Now().UTC(),
		State:      store.StatePreparing,
		Current:    current,
		Target:     target,
		BlueAddr:   o.cfg.CurrentAddr,
		ActiveAddr: o.cfg.CurrentAddr,
		History:    []store.StateEvent{{State: store.StatePreparing, At: time.Now().UTC()}},
	}
	o.save(plan)

	// 1. Stage green
	greenAddr, err := o.cfg.Backend.Stage(ctx, toBackendRelease(target))
	if err != nil {
		o.fail(plan, fmt.Sprintf("stage: %v", err))
		return plan, fmt.Errorf("stage: %w", err)
	}
	plan.GreenAddr = greenAddr
	o.save(plan)

	// 2. Migrate (forward-compatible, runs while blue still serves)
	if err := o.cfg.Migrator.Migrate(ctx); err != nil {
		o.cleanup(ctx, greenAddr)
		o.fail(plan, fmt.Sprintf("migrate: %v", err))
		return plan, fmt.Errorf("migrate: %w", err)
	}

	// 3. Health gate
	if err := o.waitHealth(ctx, greenAddr); err != nil {
		o.cleanup(ctx, greenAddr)
		o.fail(plan, fmt.Sprintf("health: %v", err))
		return plan, fmt.Errorf("health gate: %w", err)
	}

	// 4. PREPARED — pause for human approval
	plan.State = store.StatePrepared
	plan.History = append(plan.History, store.StateEvent{State: store.StatePrepared, At: time.Now().UTC()})
	o.save(plan)
	return plan, nil
}

// Apply: PREPARED → ACTIVATING → DRAINING → DONE.
// Switches proxy to green, drains blue, schedules retained remove.
func (o *Orchestrator) Apply(ctx context.Context, planID string) error {
	plan, err := o.cfg.Store.Load(planID)
	if err != nil {
		return err
	}
	if plan.State != store.StatePrepared {
		return fmt.Errorf("plan not PREPARED (state=%s)", plan.State)
	}

	plan.State = store.StateActivating
	plan.History = append(plan.History, store.StateEvent{State: store.StateActivating, At: time.Now().UTC()})
	o.save(plan)

	// Switch active to green (atomic in proxy).
	o.cfg.ActiveSwitcher.SwitchActive(plan.GreenAddr)
	plan.ActiveAddr = plan.GreenAddr

	// Drain blue (Gateway graceful shutdown handles in-flight).
	plan.State = store.StateDraining
	plan.History = append(plan.History, store.StateEvent{State: store.StateDraining, At: time.Now().UTC()})
	o.save(plan)
	if err := o.cfg.Backend.Drain(ctx, plan.BlueAddr); err != nil {
		// Drain failure: traffic already on green (correct), but blue not clean.
		// Mark FAILED so operator sees, but don't revert.
		o.fail(plan, fmt.Sprintf("drain blue: %v", err))
		return fmt.Errorf("drain blue: %w", err)
	}

	plan.State = store.StateDone
	plan.History = append(plan.History, store.StateEvent{State: store.StateDone, At: time.Now().UTC()})
	o.save(plan)

	// Schedule retained remove of blue (spec: 1h after drain).
	go o.scheduleRetainedRemove(plan)
	return nil
}

// Rollback: switch back to blue, mark ROLLED_BACK, remove green.
func (o *Orchestrator) Rollback(ctx context.Context, planID string) error {
	plan, err := o.cfg.Store.Load(planID)
	if err != nil {
		return err
	}
	o.cfg.ActiveSwitcher.SwitchActive(plan.BlueAddr)
	plan.ActiveAddr = plan.BlueAddr
	plan.State = store.StateRolledBack
	plan.History = append(plan.History, store.StateEvent{State: store.StateRolledBack, At: time.Now().UTC()})
	o.save(plan)
	if plan.GreenAddr != "" {
		o.cfg.Backend.Remove(ctx, plan.GreenAddr)
	}
	return nil
}

// waitHealth polls Health until success or retries exhausted.
func (o *Orchestrator) waitHealth(ctx context.Context, addr string) error {
	var lastErr error
	for i := 0; i < o.cfg.HealthRetries; i++ {
		if err := o.cfg.Backend.Health(ctx, addr); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.cfg.HealthInterval):
		}
	}
	return fmt.Errorf("health check failed after %d retries: %w", o.cfg.HealthRetries, lastErr)
}

func (o *Orchestrator) cleanup(ctx context.Context, greenAddr string) {
	if greenAddr == "" {
		return
	}
	if err := o.cfg.Backend.Remove(ctx, greenAddr); err != nil {
		slog.Warn("green cleanup failed", "addr", greenAddr, "err", err)
	}
}

func (o *Orchestrator) fail(plan *store.Plan, msg string) {
	plan.State = store.StateFailed
	plan.Error = msg
	plan.History = append(plan.History, store.StateEvent{
		State: store.StateFailed, At: time.Now().UTC(), Note: msg,
	})
	o.save(plan)
}

func (o *Orchestrator) save(plan *store.Plan) {
	if err := o.cfg.Store.Save(plan); err != nil {
		slog.Warn("store save failed", "plan", plan.ID, "err", err)
	}
}

func (o *Orchestrator) scheduleRetainedRemove(plan *store.Plan) {
	time.Sleep(o.cfg.RetainDuration)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := o.cfg.Backend.Remove(ctx, plan.BlueAddr); err != nil {
		slog.Warn("retained remove failed", "addr", plan.BlueAddr, "err", err)
	}
}

func toBackendRelease(r store.Release) backend.Release {
	return backend.Release{Version: r.Version, DownloadURL: r.DownloadURL, SHA256: r.SHA256}
}

func newPlanID() string {
	return fmt.Sprintf("plan-%d", time.Now().UnixNano())
}