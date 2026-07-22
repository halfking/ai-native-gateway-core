// Package orchestrator runs the blue-green state machine.
//
// State flow: NOTIFIED → PREPARING → PREPARED → ACTIVATING → DRAINING → DONE
//
//	↘ FAILED   ↘  FAILED        ↘ FAILED
//
// The human approval gate is the PREPARED state — nothing crosses
// ACTIVATING without an explicit Apply call.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/backend"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
)

// Migrator abstracts `gateway migrate` invocation (orchestrator stays decoupled
// from subprocess execution — daemon injects the real implementation).
type Migrator interface {
	Migrate(ctx context.Context) error
}

// ActiveSwitcher abstracts the proxy's active-target management.
// Apply uses SwitchActive to redirect traffic; Prepare uses ActiveAddr
// to read the *current* live target as BlueAddr (audit I7: previously
// CurrentAddr was frozen at orchestrator construction, so a second
// Prepare within one daemon lifetime would set BlueAddr to the
// original blue even though traffic was on green after an Apply).
type ActiveSwitcher interface {
	SwitchActive(addr string)
	ActiveAddr() string
}

// Config configures the orchestrator.
type Config struct {
	Store          *store.Store
	Backend        backend.Backend
	Migrator       Migrator
	ActiveSwitcher ActiveSwitcher
	HealthRetries  int           // spec: 5
	HealthInterval time.Duration // spec: 1s
	RetainDuration time.Duration // spec: 1h (blue retained post-drain)
}

type Orchestrator struct {
	cfg  Config
	done chan struct{}

	// opMu serializes Prepare/Apply/Rollback (I2). Without it, two
	// concurrent Prepare calls would both try to stage a green container
	// on the same port, or a Rollback could race an in-flight Apply.
	opMu sync.Mutex

	// retainedCancel tracks per-plan retained-remove goroutines so Rollback
	// can cancel a pending remove before it kills the blue we just switched
	// back to (I5). Guarded by retainedMu.
	retainedMu     sync.Mutex
	retainedCancel map[string]chan struct{}
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
	return &Orchestrator{
		cfg:            cfg,
		done:           make(chan struct{}),
		retainedCancel: make(map[string]chan struct{}),
	}
}

// Stop signals all background goroutines (scheduleRetainedRemove) to exit.
func (o *Orchestrator) Stop() {
	close(o.done)
}

// Prepare transitions an existing NOTIFIED plan through PREPARING → PREPARED.
// Steps: Stage green → Migrate → Health gate.
// On any failure: cleans up green, plan marked FAILED.
//
// Audit C4: previously Prepare took (current, target) and constructed a
// brand-new plan, orphaning the NOTIFIED plan in the store. Now it loads
// the existing plan by ID and transitions it in place — matching the
// spec state machine (NOTIFIED → PREPARING → PREPARED on one object).
//
// Audit I7: BlueAddr is read live from ActiveSwitcher rather than a
// Config.CurrentAddr frozen at construction, so a second Prepare within
// one daemon lifetime correctly treats the current active as blue.
func (o *Orchestrator) Prepare(ctx context.Context, planID string) (*store.Plan, error) {
	o.opMu.Lock()
	defer o.opMu.Unlock()
	plan, err := o.cfg.Store.Load(planID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", planID, err)
	}
	if plan.State != store.StateNotified && plan.State != store.StateFailed {
		return plan, fmt.Errorf("prepare: plan must be NOTIFIED or FAILED (got %s)", plan.State)
	}
	// Snapshot the live active addr as BlueAddr for this cycle.
	blueAddr := o.cfg.ActiveSwitcher.ActiveAddr()
	plan.BlueAddr = blueAddr
	plan.ActiveAddr = blueAddr
	plan.State = store.StatePreparing
	plan.Error = ""
	plan.History = append(plan.History, store.StateEvent{State: store.StatePreparing, At: time.Now().UTC()})
	o.save(plan)

	// 1. Stage green
	greenAddr, err := o.cfg.Backend.Stage(ctx, toBackendRelease(plan.Target))
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
	o.opMu.Lock()
	defer o.opMu.Unlock()
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

	// Switch active to green (atomic in proxy). Persist so daemon restart
	// restores the proxy target instead of reverting to drained blue (C1).
	o.cfg.ActiveSwitcher.SwitchActive(plan.GreenAddr)
	plan.ActiveAddr = plan.GreenAddr
	o.persistActive(plan.GreenAddr, plan.Target.Version)

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
	plan.Audit = appendAudit(plan.Audit, store.AuditEntry{Action: "apply", At: time.Now().UTC()})
	o.save(plan)

	// Schedule retained remove of blue (spec: 1h after drain).
	// Tracked per-plan so Rollback can cancel it (I5).
	o.scheduleRetainedRemove(plan)
	return nil
}

// Rollback: switch back to blue, mark ROLLED_BACK, remove green.
// Only valid when state is DONE or FAILED (I4): rolling back a NOTIFIED
// plan (no green staged) or PREPARING plan is a confusing no-op.
func (o *Orchestrator) Rollback(ctx context.Context, planID string) error {
	o.opMu.Lock()
	defer o.opMu.Unlock()
	plan, err := o.cfg.Store.Load(planID)
	if err != nil {
		return err
	}
	// Allowed states:
	//   PREPARED — cancel a staged green without applying (audit I6).
	//   DONE/FAILED — revert traffic to blue + remove green/retained.
	// NOTIFIED/PREPARING have no usable green to clean, so reject.
	if plan.State != store.StateDone && plan.State != store.StateFailed && plan.State != store.StatePrepared {
		return fmt.Errorf("rollback only valid for PREPARED/DONE/FAILED plans (state=%s)", plan.State)
	}
	// Cancel any pending retained-remove so it doesn't kill blue after
	// we switch back to it (I5).
	o.cancelRetainedRemove(planID)

	o.cfg.ActiveSwitcher.SwitchActive(plan.BlueAddr)
	plan.ActiveAddr = plan.BlueAddr
	o.persistActive(plan.BlueAddr, plan.Current.Version)
	plan.State = store.StateRolledBack
	plan.History = append(plan.History, store.StateEvent{State: store.StateRolledBack, At: time.Now().UTC()})
	plan.Audit = appendAudit(plan.Audit, store.AuditEntry{Action: "rollback", At: time.Now().UTC()})
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

// persistActive writes active.json so a daemon restart restores the proxy
// target instead of reverting to a drained/stopped instance (C1).
func (o *Orchestrator) persistActive(addr, version string) {
	if err := o.cfg.Store.SaveActive(&store.ActivePointer{Addr: addr, Version: version}); err != nil {
		slog.Warn("persist active failed", "addr", addr, "err", err)
	}
}

// scheduleRetainedRemove removes blue after RetainDuration. Tracks the
// per-plan cancel channel so Rollback can cancel it (I5).
func (o *Orchestrator) scheduleRetainedRemove(plan *store.Plan) {
	cancel := make(chan struct{})
	o.retainedMu.Lock()
	o.retainedCancel[plan.ID] = cancel
	o.retainedMu.Unlock()

	go func() {
		select {
		case <-o.done:
			return
		case <-cancel:
			slog.Info("retained remove cancelled (rollback)", "plan", plan.ID)
			return
		case <-time.After(o.cfg.RetainDuration):
		}
		ctx, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel2()
		if err := o.cfg.Backend.Remove(ctx, plan.BlueAddr); err != nil {
			slog.Warn("retained remove failed", "addr", plan.BlueAddr, "err", err)
		}
		o.retainedMu.Lock()
		delete(o.retainedCancel, plan.ID)
		o.retainedMu.Unlock()
	}()
}

// cancelRetainedRemove cancels a pending retained-remove for a plan.
func (o *Orchestrator) cancelRetainedRemove(planID string) {
	o.retainedMu.Lock()
	defer o.retainedMu.Unlock()
	if c, ok := o.retainedCancel[planID]; ok {
		close(c)
		delete(o.retainedCancel, planID)
	}
}

// appendAudit returns the slice with entry appended, allocating if nil.
// (Audit entries record who/when/what per spec §8.3 — audit I3: previously
// store.AuditEntry existed but was never populated.)
func appendAudit(entries []store.AuditEntry, entry store.AuditEntry) []store.AuditEntry {
	return append(entries, entry)
}

func toBackendRelease(r store.Release) backend.Release {
	return backend.Release{
		Version:     r.Version,
		Image:       r.Image,
		DownloadURL: r.DownloadURL,
		SHA256:      r.SHA256,
	}
}
