// Package supervisor manages plugin process lifecycle.
//
// Supervisor starts, stops, and monitors plugin processes with health checks,
// automatic restart on failure, and resource limits via cgroups.
package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
)

// Supervisor manages plugin processes.
type Supervisor struct {
	mu        sync.RWMutex
	processes map[string]*ManagedProcess

	// Configuration
	restartPolicy       RestartPolicy
	healthCheckInterval time.Duration

	// Dependencies
	eventBus EventBus
}

// ManagedProcess represents a managed plugin process.
type ManagedProcess struct {
	manifest *discovery.Manifest
	cmd      *exec.Cmd
	state    *ProcessState

	// Health check
	healthDone chan struct{}

	// waitDone is closed by monitorProcess (the single owner of cmd.Wait)
	// once Wait has returned. Stop waits on it instead of calling
	// cmd.Wait itself: exec.Cmd.Wait is documented to return an error if
	// called concurrently or twice (2026-11-14 audit D3).
	waitDone chan struct{}
	// stoppedManually marks a Stop-initiated termination so monitorProcess
	// records a graceful stop instead of a crash for the unavoidable
	// "signal: terminated" Wait error.
	stoppedManually bool

	// Resource limiter
	limiter *ResourceLimiter
}

// ProcessState tracks the state of a plugin process.
type ProcessState struct {
	PluginID     string
	Version      string
	PID          int
	Status       ProcessStatus
	StartTime    time.Time
	RestartCount int
	LastError    string
	Health       *HealthStatus
}

// ProcessStatus indicates the current status of a plugin process.
type ProcessStatus string

const (
	StatusStarting ProcessStatus = "starting"
	StatusRunning  ProcessStatus = "running"
	StatusStopping ProcessStatus = "stopping"
	StatusStopped  ProcessStatus = "stopped"
	StatusCrashed  ProcessStatus = "crashed"
)

// HealthStatus represents the health check result.
type HealthStatus struct {
	Healthy   bool
	Message   string
	CheckedAt time.Time
	Latency   time.Duration
	Metrics   map[string]float64
}

// RestartPolicy defines when to restart a failed plugin.
type RestartPolicy struct {
	Mode              RestartMode
	BackoffInitial    time.Duration
	BackoffMax        time.Duration
	BackoffMultiplier float64
	MaxRestartCount   int
}

// RestartMode defines restart behavior.
type RestartMode string

const (
	RestartAlways    RestartMode = "always"
	RestartOnFailure RestartMode = "on_failure"
	RestartNever     RestartMode = "never"
)

// EventBus defines event publishing interface.
type EventBus interface {
	Publish(ctx context.Context, event Event) error
}

// Event represents a supervisor event.
type Event struct {
	Type     EventType
	PluginID string
	Error    error
	Data     map[string]interface{}
}

// EventType indicates the type of event.
type EventType string

const (
	EventPluginStarted   EventType = "plugin.started"
	EventPluginStopped   EventType = "plugin.stopped"
	EventPluginCrashed   EventType = "plugin.crashed"
	EventPluginRestarted EventType = "plugin.restarted"
)

// NewSupervisor creates a new Supervisor.
func NewSupervisor(eventBus EventBus) *Supervisor {
	return &Supervisor{
		processes:           make(map[string]*ManagedProcess),
		restartPolicy:       DefaultRestartPolicy(),
		healthCheckInterval: 10 * time.Second,
		eventBus:            eventBus,
	}
}

// DefaultRestartPolicy returns the default restart policy.
func DefaultRestartPolicy() RestartPolicy {
	return RestartPolicy{
		Mode:              RestartOnFailure,
		BackoffInitial:    1 * time.Second,
		BackoffMax:        60 * time.Second,
		BackoffMultiplier: 2.0,
		MaxRestartCount:   10,
	}
}

// Start starts a plugin process.
func (s *Supervisor) Start(ctx context.Context, manifest *discovery.Manifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pluginID := manifest.Metadata.Name

	// Check if already running
	if _, exists := s.processes[pluginID]; exists {
		return fmt.Errorf("plugin %s already running", pluginID)
	}

	// Create managed process
	mp, err := s.createProcess(manifest)
	if err != nil {
		return fmt.Errorf("failed to create process: %w", err)
	}

	// Start the process
	if err := mp.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	mp.state.PID = mp.cmd.Process.Pid
	mp.state.Version = manifest.Metadata.Version
	mp.state.Status = StatusRunning
	mp.state.StartTime = time.Now()

	s.processes[pluginID] = mp

	// Start health check
	go s.runHealthCheck(pluginID, mp)

	// Monitor process exit
	go s.monitorProcess(pluginID, mp)

	// Publish event
	s.eventBus.Publish(ctx, Event{
		Type:     EventPluginStarted,
		PluginID: pluginID,
		Data: map[string]interface{}{
			"pid":        mp.state.PID,
			"start_time": mp.state.StartTime,
		},
	})

	fmt.Printf("INFO: plugin started: %s (PID %d)\n", pluginID, mp.state.PID)
	return nil
}

func (s *Supervisor) createProcess(manifest *discovery.Manifest) (*ManagedProcess, error) {
	// Create command
	cmd := exec.Command(manifest.Spec.Executable, manifest.Spec.Args...)

	// Set working directory
	if manifest.Spec.WorkDir != "" {
		cmd.Dir = manifest.Spec.WorkDir
	}

	// Set environment variables. 2026-11-14 audit D15: inherit the parent
	// environment first — a bare append(cmd.Env, ...) leaves the child with
	// only the manifest env (no PATH/HOME), breaking most binaries.
	cmd.Env = append(os.Environ(), formatEnv(manifest.Spec.Env)...)

	// Create resource limiter
	limiter, err := NewResourceLimiter(manifest.Metadata.Name, manifest.Spec.Resources)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource limiter: %w", err)
	}

	// Apply resource limits
	if err := limiter.Apply(cmd); err != nil {
		return nil, fmt.Errorf("failed to apply resource limits: %w", err)
	}

	mp := &ManagedProcess{
		manifest:   manifest,
		cmd:        cmd,
		limiter:    limiter,
		healthDone: make(chan struct{}),
		waitDone:   make(chan struct{}),
		state: &ProcessState{
			PluginID: manifest.Metadata.Name,
			Status:   StatusStarting,
		},
	}

	return mp, nil
}

func (s *Supervisor) monitorProcess(pluginID string, mp *ManagedProcess) {
	// monitorProcess is the SINGLE owner of cmd.Wait (exec.Cmd.Wait must be
	// called exactly once; a concurrent Wait from Stop returns
	// "exec: Wait was already called" — 2026-11-14 audit D3). Stop signals
	// its intent via stoppedManually and waits on waitDone.
	err := mp.cmd.Wait()

	s.mu.Lock()

	// Stop health check
	close(mp.healthDone)

	// A Stop-initiated SIGTERM surfaces as Wait error "signal: terminated";
	// it is a graceful stop by design, not a crash (2026-11-14 audit D3).
	gracefulStop := mp.stoppedManually

	// Update state
	if err != nil && !gracefulStop {
		mp.state.Status = StatusCrashed
		mp.state.LastError = err.Error()
		fmt.Printf("ERROR: plugin crashed: %s (error: %v)\n", pluginID, err)

		s.eventBus.Publish(context.Background(), Event{
			Type:     EventPluginCrashed,
			PluginID: pluginID,
			Error:    err,
		})
	} else {
		mp.state.Status = StatusStopped
		if gracefulStop {
			mp.state.LastError = ""
		} else if err != nil {
			mp.state.LastError = err.Error()
		}
		fmt.Printf("INFO: plugin stopped: %s\n", pluginID)

		s.eventBus.Publish(context.Background(), Event{
			Type:     EventPluginStopped,
			PluginID: pluginID,
		})
	}

	// Decide whether to restart. 2026-11-14 audit D1: the dead entry must be
	// removed from the map BEFORE the restart attempt, otherwise s.Start
	// rejects with "already running" forever — the crash-restart loop never
	// succeeds and the stale entry blocks manual Start/Restart too.
	shouldRestart := s.shouldRestart(mp, err) && !gracefulStop
	if shouldRestart {
		mp.state.RestartCount++
		backoff := s.calculateBackoff(mp.state.RestartCount)
		manifest := mp.manifest
		// Remove the dead entry while still under the lock so concurrent
		// Start/Stop observe a consistent "not running" state.
		delete(s.processes, pluginID)
		close(mp.waitDone)
		s.mu.Unlock()

		fmt.Printf("INFO: restarting plugin %s in %v (attempt %d)\n",
			pluginID, backoff, mp.state.RestartCount)

		// Backoff sleep must NOT hold the lock: s.mu guards Start/Stop/
		// ListAll/StateOf, and a 60s capped sleep under the lock stalls the
		// whole supervisor (2026-11-14 audit D4).
		time.Sleep(backoff)

		ctx := context.Background()
		if err := s.Start(ctx, manifest); err != nil {
			fmt.Printf("ERROR: failed to restart plugin %s: %v\n", pluginID, err)
		}
	} else {
		// Remove from processes map
		delete(s.processes, pluginID)
		close(mp.waitDone)
		s.mu.Unlock()
	}
}

func (s *Supervisor) shouldRestart(mp *ManagedProcess, err error) bool {
	// Check restart policy
	switch s.restartPolicy.Mode {
	case RestartNever:
		return false
	case RestartAlways:
		return mp.state.RestartCount < s.restartPolicy.MaxRestartCount
	case RestartOnFailure:
		return err != nil && mp.state.RestartCount < s.restartPolicy.MaxRestartCount
	}
	return false
}

func (s *Supervisor) calculateBackoff(restartCount int) time.Duration {
	backoff := float64(s.restartPolicy.BackoffInitial)
	for i := 1; i < restartCount; i++ {
		backoff *= s.restartPolicy.BackoffMultiplier
	}

	duration := time.Duration(backoff)
	if duration > s.restartPolicy.BackoffMax {
		return s.restartPolicy.BackoffMax
	}
	return duration
}

func (s *Supervisor) runHealthCheck(pluginID string, mp *ManagedProcess) {
	ticker := time.NewTicker(s.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			health, err := s.HealthCheck(ctx, pluginID)
			cancel()

			if err != nil {
				fmt.Printf("WARN: health check failed for %s: %v\n", pluginID, err)
				health = &HealthStatus{
					Healthy:   false,
					Message:   err.Error(),
					CheckedAt: time.Now(),
				}
			}

			s.mu.Lock()
			mp.state.Health = health
			s.mu.Unlock()

		case <-mp.healthDone:
			return
		}
	}
}

// Stop gracefully stops a plugin process.
func (s *Supervisor) Stop(ctx context.Context, pluginID string) error {
	s.mu.Lock()
	mp, exists := s.processes[pluginID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("plugin %s not found", pluginID)
	}
	mp.state.Status = StatusStopping
	mp.stoppedManually = true
	s.mu.Unlock()

	// Send SIGTERM
	if err := mp.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM: %w", err)
	}

	// Wait for graceful shutdown (10 seconds). 2026-11-14 audit D3: do NOT
	// call cmd.Wait here — monitorProcess is the single Wait owner and a
	// concurrent Wait returns "exec: Wait was already called". Wait on the
	// waitDone signal instead; the timeout arm force-kills (the resulting
	// Wait return is still consumed by monitorProcess).
	select {
	case <-time.After(10 * time.Second):
		// Timeout, force kill
		fmt.Printf("WARN: plugin %s did not stop gracefully, killing\n", pluginID)
		return mp.cmd.Process.Kill()

	case <-mp.waitDone:
		fmt.Printf("INFO: plugin %s stopped gracefully\n", pluginID)
		return nil
	}
}

// Restart restarts a plugin.
func (s *Supervisor) Restart(ctx context.Context, pluginID string) error {
	s.mu.RLock()
	mp, exists := s.processes[pluginID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("plugin %s not found", pluginID)
	}

	manifest := mp.manifest

	if err := s.Stop(ctx, pluginID); err != nil {
		return fmt.Errorf("failed to stop plugin: %w", err)
	}

	time.Sleep(1 * time.Second)

	return s.Start(ctx, manifest)
}

// StateOf returns the current state of a plugin.
func (s *Supervisor) StateOf(pluginID string) (*ProcessState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mp, exists := s.processes[pluginID]
	if !exists {
		return nil, fmt.Errorf("plugin %s not found", pluginID)
	}

	return mp.state, nil
}

// ListAll returns all managed processes.
func (s *Supervisor) ListAll() []*ProcessState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	states := make([]*ProcessState, 0, len(s.processes))
	for _, mp := range s.processes {
		states = append(states, mp.state)
	}
	return states
}

// HealthCheck performs a health check on a plugin.
func (s *Supervisor) HealthCheck(ctx context.Context, pluginID string) (*HealthStatus, error) {
	// TODO: Implement actual health check via IPC
	// For now, just check if process is running
	s.mu.RLock()
	mp, exists := s.processes[pluginID]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("plugin not found")
	}

	// Check if process is still alive
	select {
	case <-mp.waitDone:
		return &HealthStatus{
			Healthy:   false,
			Message:   "process exited",
			CheckedAt: time.Now(),
		}, nil
	default:
	}

	return &HealthStatus{
		Healthy:   true,
		Message:   "ok",
		CheckedAt: time.Now(),
		Latency:   0,
	}, nil
}

// Helper functions

func formatEnv(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}
