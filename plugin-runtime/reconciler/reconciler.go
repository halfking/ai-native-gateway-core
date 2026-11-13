// Package reconciler implements the plugin state reconciliation loop.
//
// Reconciler periodically checks the desired state (from registry) against
// actual state (from supervisor) and automatically fixes drift:
// - Crashed plugins are restarted
// - Version mismatches trigger upgrades
// - Continuous crashes trigger automatic disable
package reconciler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

// Reconciler coordinates plugin state between desired and actual.
type Reconciler interface {
	// Start begins the reconciliation loop.
	Start(ctx context.Context) error

	// Stop halts the reconciliation loop.
	Stop() error

	// Reconcile performs a single reconciliation cycle (manual trigger).
	Reconcile(ctx context.Context) error
}

// Config configures the Reconciler behavior.
type Config struct {
	// Interval between reconciliation cycles (default: 30s).
	Interval time.Duration

	// MaxCrashCount before a plugin is disabled (default: 10).
	MaxCrashCount int

	// CrashResetWindow for resetting crash counters (default: 1 hour).
	CrashResetWindow time.Duration
}

// PluginState represents the state of a plugin at a point in time.
type PluginState struct {
	PluginID      string
	Name          string
	Version       string
	DesiredState  State
	ActualState   State
	CrashCount    int
	LastCrashTime time.Time
}

// State represents the lifecycle state of a plugin.
type State string

const (
	StatePending  State = "pending"  // Waiting to start
	StateStarting State = "starting" // Starting up
	StateRunning  State = "running"  // Running normally
	StateStopping State = "stopping" // Shutting down
	StateStopped  State = "stopped"  // Stopped
	StateFailed   State = "failed"   // Failed/crashed
	StateDisabled State = "disabled" // Disabled (won't restart)
)

// Action represents a reconciliation action to take.
type Action string

const (
	ActionStart   Action = "start"   // Start the plugin
	ActionStop    Action = "stop"    // Stop the plugin
	ActionRestart Action = "restart" // Restart the plugin
	ActionUpgrade Action = "upgrade" // Upgrade the plugin
	ActionDisable Action = "disable" // Disable the plugin
	ActionNone    Action = "none"    // No action needed
)

// ReconcileAction describes an action to perform during reconciliation.
type ReconcileAction struct {
	PluginID   string
	Action     Action
	Reason     string
	OldVersion string // for upgrades
	NewVersion string // for upgrades
}

// SupervisorInterface defines the interface we need from Supervisor.
type SupervisorInterface interface {
	Start(ctx context.Context, manifest *discovery.Manifest) error
	Stop(ctx context.Context, pluginID string) error
	Restart(ctx context.Context, pluginID string) error
	StateOf(pluginID string) (*supervisor.ProcessState, error)
	ListAll() []*supervisor.ProcessState
}

// PluginRegistry defines the interface for plugin registration and metadata.
type PluginRegistry interface {
	List(ctx context.Context, filter RegistryFilter) ([]*RegistryPlugin, error)
	UpdateVersion(ctx context.Context, pluginID string, version string) error
	UpdateStatus(ctx context.Context, pluginID string, status PluginStatus) error
	GetManifest(ctx context.Context, pluginID string) (*discovery.Manifest, error)
}

// RegistryPlugin represents a plugin in the registry.
type RegistryPlugin struct {
	ID      string
	Name    string
	Version string
	Enabled bool
	Status  PluginStatus
}

// PluginStatus represents the administrative status of a plugin.
type PluginStatus string

const (
	StatusEnabled  PluginStatus = "enabled"
	StatusDisabled PluginStatus = "disabled"
)

// RegistryFilter filters plugins in the registry.
type RegistryFilter struct {
	EnabledOnly bool
}

// FilterEnabled returns a filter for enabled plugins only.
func FilterEnabled() RegistryFilter {
	return RegistryFilter{EnabledOnly: true}
}

// reconciler implements the Reconciler interface.
type reconciler struct {
	config       *Config
	supervisor   SupervisorInterface
	registry     PluginRegistry
	ticker       *time.Ticker
	stopCh       chan struct{}
	crashTracker *CrashTracker
}

// NewReconciler creates a new Reconciler with the given configuration.
func NewReconciler(config *Config, supervisor SupervisorInterface, registry PluginRegistry) Reconciler {
	// Apply defaults
	if config.Interval == 0 {
		config.Interval = 30 * time.Second
	}
	if config.MaxCrashCount == 0 {
		config.MaxCrashCount = 10
	}
	if config.CrashResetWindow == 0 {
		config.CrashResetWindow = 1 * time.Hour
	}

	return &reconciler{
		config:       config,
		supervisor:   supervisor,
		registry:     registry,
		stopCh:       make(chan struct{}),
		crashTracker: NewCrashTracker(config.CrashResetWindow),
	}
}

// Start begins the reconciliation loop.
func (r *reconciler) Start(ctx context.Context) error {
	r.ticker = time.NewTicker(r.config.Interval)

	// Perform initial reconciliation immediately
	if err := r.Reconcile(ctx); err != nil {
		log.Printf("ERROR: Initial reconciliation failed: %v", err)
	}

	// Start periodic reconciliation
	go func() {
		for {
			select {
			case <-r.ticker.C:
				if err := r.Reconcile(ctx); err != nil {
					log.Printf("ERROR: Reconciliation failed: %v", err)
				}
			case <-r.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	log.Println("INFO: Reconciler started")
	return nil
}

// Stop halts the reconciliation loop.
func (r *reconciler) Stop() error {
	if r.ticker != nil {
		r.ticker.Stop()
	}
	close(r.stopCh)
	log.Println("INFO: Reconciler stopped")
	return nil
}

// Reconcile performs a single reconciliation cycle.
func (r *reconciler) Reconcile(ctx context.Context) error {
	// 1. Get desired states from registry
	desiredStates, err := r.getDesiredStates(ctx)
	if err != nil {
		return fmt.Errorf("get desired states: %w", err)
	}

	// 2. Get actual states from supervisor
	actualStates, err := r.getActualStates(ctx)
	if err != nil {
		return fmt.Errorf("get actual states: %w", err)
	}

	// 3. Compute reconciliation actions
	actions := r.computeActions(desiredStates, actualStates)

	// 4. Execute actions
	for _, action := range actions {
		if err := r.executeAction(ctx, action); err != nil {
			log.Printf("ERROR: Execute action %s for plugin %s failed: %v",
				action.Action, action.PluginID, err)
			// Continue with other actions
		}
	}

	return nil
}

// getDesiredStates retrieves the desired state for all enabled plugins.
func (r *reconciler) getDesiredStates(ctx context.Context) (map[string]*PluginState, error) {
	// Get all enabled plugins from registry
	plugins, err := r.registry.List(ctx, FilterEnabled())
	if err != nil {
		return nil, err
	}

	states := make(map[string]*PluginState)
	for _, plugin := range plugins {
		states[plugin.ID] = &PluginState{
			PluginID:     plugin.ID,
			Name:         plugin.Name,
			Version:      plugin.Version,
			DesiredState: StateRunning, // All enabled plugins should be running
		}
	}

	return states, nil
}

// getActualStates retrieves the actual state of all running plugins.
func (r *reconciler) getActualStates(ctx context.Context) (map[string]*PluginState, error) {
	// Get all processes from supervisor
	processes := r.supervisor.ListAll()

	states := make(map[string]*PluginState)
	for _, proc := range processes {
		states[proc.PluginID] = &PluginState{
			PluginID:    proc.PluginID,
			ActualState: mapProcessStatus(proc.Status),
			CrashCount:  r.crashTracker.GetCount(proc.PluginID),
		}
	}

	return states, nil
}

// computeActions determines what actions to take based on state comparison.
func (r *reconciler) computeActions(desired, actual map[string]*PluginState) []*ReconcileAction {
	var actions []*ReconcileAction

	// Check desired plugins
	for pluginID, desiredState := range desired {
		actualState, exists := actual[pluginID]

		if !exists {
			// Plugin should be running but isn't → start it
			actions = append(actions, &ReconcileAction{
				PluginID: pluginID,
				Action:   ActionStart,
				Reason:   "Plugin not running but desired",
			})
			continue
		}

		// Check crash count
		if actualState.CrashCount >= r.config.MaxCrashCount {
			// Too many crashes → disable
			actions = append(actions, &ReconcileAction{
				PluginID: pluginID,
				Action:   ActionDisable,
				Reason:   fmt.Sprintf("Crash count exceeded: %d", actualState.CrashCount),
			})
			continue
		}

		// Check state
		switch actualState.ActualState {
		case StateFailed:
			// Plugin failed → restart
			actions = append(actions, &ReconcileAction{
				PluginID: pluginID,
				Action:   ActionRestart,
				Reason:   "Plugin in failed state",
			})

			// Record crash
			r.crashTracker.RecordCrash(pluginID)

		case StateStopped:
			// Plugin stopped but should be running → start
			actions = append(actions, &ReconcileAction{
				PluginID: pluginID,
				Action:   ActionStart,
				Reason:   "Plugin stopped but desired running",
			})

		case StateRunning:
			// Check version mismatch
			if desiredState.Version != "" && actualState.Version != "" &&
				actualState.Version != desiredState.Version {
				// Version mismatch → upgrade
				actions = append(actions, &ReconcileAction{
					PluginID:   pluginID,
					Action:     ActionUpgrade,
					Reason:     fmt.Sprintf("Version mismatch: %s -> %s", actualState.Version, desiredState.Version),
					OldVersion: actualState.Version,
					NewVersion: desiredState.Version,
				})
			}
		}
	}

	// Check for plugins that shouldn't be running
	for pluginID, actualState := range actual {
		if _, exists := desired[pluginID]; !exists {
			// Plugin running but not desired → stop
			if actualState.ActualState == StateRunning {
				actions = append(actions, &ReconcileAction{
					PluginID: pluginID,
					Action:   ActionStop,
					Reason:   "Plugin running but not desired",
				})
			}
		}
	}

	return actions
}

// executeAction performs the specified reconciliation action.
func (r *reconciler) executeAction(ctx context.Context, action *ReconcileAction) error {
	log.Printf("INFO: Executing action %s for plugin %s: %s",
		action.Action, action.PluginID, action.Reason)

	switch action.Action {
	case ActionStart:
		// Get manifest from registry
		manifest, err := r.registry.GetManifest(ctx, action.PluginID)
		if err != nil {
			return fmt.Errorf("get manifest: %w", err)
		}
		return r.supervisor.Start(ctx, manifest)

	case ActionStop:
		return r.supervisor.Stop(ctx, action.PluginID)

	case ActionRestart:
		return r.supervisor.Restart(ctx, action.PluginID)

	case ActionUpgrade:
		// Stop old version
		if err := r.supervisor.Stop(ctx, action.PluginID); err != nil {
			return fmt.Errorf("stop old version: %w", err)
		}

		// Update version in registry
		if err := r.registry.UpdateVersion(ctx, action.PluginID, action.NewVersion); err != nil {
			return fmt.Errorf("update version: %w", err)
		}

		// Wait briefly for clean shutdown
		time.Sleep(1 * time.Second)

		// Start new version
		manifest, err := r.registry.GetManifest(ctx, action.PluginID)
		if err != nil {
			return fmt.Errorf("get manifest: %w", err)
		}
		return r.supervisor.Start(ctx, manifest)

	case ActionDisable:
		// Stop the plugin
		if err := r.supervisor.Stop(ctx, action.PluginID); err != nil {
			log.Printf("WARN: Stop plugin failed (will continue to disable): %v", err)
		}
		// Update status in registry
		return r.registry.UpdateStatus(ctx, action.PluginID, StatusDisabled)

	default:
		return fmt.Errorf("unknown action: %s", action.Action)
	}
}

// mapProcessStatus converts supervisor.ProcessStatus to reconciler.State.
func mapProcessStatus(status supervisor.ProcessStatus) State {
	switch status {
	case supervisor.StatusStarting:
		return StateStarting
	case supervisor.StatusRunning:
		return StateRunning
	case supervisor.StatusStopping:
		return StateStopping
	case supervisor.StatusStopped:
		return StateStopped
	case supervisor.StatusCrashed:
		return StateFailed
	default:
		return StatePending
	}
}
