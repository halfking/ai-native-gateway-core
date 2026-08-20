package orchestration

import (
	"context"
	"fmt"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// CoreOrchestrator is the decision/action boundary for plugin-driven work.
// It authorizes a binding before enqueueing an action; it does not execute
// providers, tools, handoffs, deployments or other external side effects.
type CoreOrchestrator struct {
	loader    *PluginLoader
	scheduler *Scheduler
}

func NewCoreOrchestrator(loader *PluginLoader, scheduler *Scheduler) *CoreOrchestrator {
	return &CoreOrchestrator{loader: loader, scheduler: scheduler}
}

func (o *CoreOrchestrator) LoadPlugin(ctx context.Context, manifest *pluginruntime.Manifest) error {
	if o == nil || o.loader == nil {
		return fmt.Errorf("load plugin failed: orchestrator loader unavailable (plugin_id=%s)", pluginID(manifest))
	}
	return o.loader.Load(ctx, manifest)
}

func (o *CoreOrchestrator) Schedule(ctx context.Context, action Action) error {
	if o == nil || o.loader == nil || o.scheduler == nil {
		return fmt.Errorf("schedule action failed: orchestrator unavailable (action_id=%s)", action.ID)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("schedule action failed: context canceled (action_id=%s): %w", action.ID, err)
	}
	if err := o.loader.Authorize(action.BindingID, action.Capability, action.TenantID, action.Model); err != nil {
		return err
	}
	if err := o.scheduler.Enqueue(action); err != nil {
		return fmt.Errorf("schedule action failed: %w (action_id=%s)", err, action.ID)
	}
	return nil
}

func (o *CoreOrchestrator) Claim(runID, workerID string) (ActionClaim, error) {
	if o == nil || o.scheduler == nil {
		return ActionClaim{}, fmt.Errorf("claim action failed: scheduler unavailable (run_id=%s)", runID)
	}
	return o.scheduler.Claim(runID, workerID)
}

func (o *CoreOrchestrator) Complete(leaseID string, cause error) error {
	if o == nil || o.scheduler == nil {
		return fmt.Errorf("complete action failed: scheduler unavailable (lease_id=%s)", leaseID)
	}
	return o.scheduler.Complete(leaseID, cause)
}
