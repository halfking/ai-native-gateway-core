package extpoints

import (
	"context"
)

// ExtensionPoint is the base interface that all plugin extension points must implement.
// It provides common lifecycle and metadata operations.
type ExtensionPoint interface {
	// Metadata returns the plugin's metadata for this extension point.
	Metadata() *PluginMetadata

	// Initialize is called when the plugin is loaded.
	// It allows the plugin to perform setup operations.
	Initialize(ctx context.Context) error

	// Shutdown is called when the plugin is being unloaded.
	// It allows the plugin to perform cleanup operations.
	Shutdown(ctx context.Context) error

	// HealthCheck reports the health status of the plugin.
	// Returns nil if healthy, error otherwise.
	HealthCheck(ctx context.Context) error
}

// PhaseExecutor defines the interface for plugins that execute in specific phases.
// All phase-specific interfaces (RequestTransformer, ResponseInterceptor, etc.)
// should embed this interface.
type PhaseExecutor interface {
	ExtensionPoint

	// Execute is called when the plugin's phase is reached in the pipeline.
	// The ExecutionContext provides access to request/response data and shared state.
	Execute(ctx *ExecutionContext) (*ExecutionResult, error)
}

// ConditionalExecutor allows plugins to decide whether they should execute
// based on the current context.
type ConditionalExecutor interface {
	// ShouldExecute determines if the plugin should execute for the given context.
	// Return true to execute, false to skip.
	ShouldExecute(ctx *ExecutionContext) bool
}

// StatefulExecutor allows plugins to maintain state across executions.
type StatefulExecutor interface {
	// GetState retrieves the plugin's current state.
	GetState(ctx context.Context) (map[string]interface{}, error)

	// SetState updates the plugin's state.
	SetState(ctx context.Context, state map[string]interface{}) error
}

// ConfigurableExecutor allows plugins to be dynamically reconfigured.
type ConfigurableExecutor interface {
	// Configure applies new configuration to the plugin.
	// This is called when the gateway configuration changes.
	Configure(ctx context.Context, config map[string]interface{}) error

	// GetConfig retrieves the plugin's current configuration.
	GetConfig(ctx context.Context) (map[string]interface{}, error)
}

// MetricsReporter allows plugins to report custom metrics.
type MetricsReporter interface {
	// ReportMetrics returns metrics for this plugin.
	// The returned map should contain metric names and values.
	ReportMetrics(ctx context.Context) (map[string]float64, error)
}

// BaseExtensionPoint provides a default implementation of ExtensionPoint.
// Plugins can embed this to get default behavior and only override what they need.
type BaseExtensionPoint struct {
	metadata *PluginMetadata
}

// NewBaseExtensionPoint creates a new BaseExtensionPoint with the given metadata.
func NewBaseExtensionPoint(metadata *PluginMetadata) *BaseExtensionPoint {
	return &BaseExtensionPoint{
		metadata: metadata,
	}
}

// Metadata returns the plugin's metadata.
func (b *BaseExtensionPoint) Metadata() *PluginMetadata {
	return b.metadata
}

// Initialize performs default initialization (no-op).
func (b *BaseExtensionPoint) Initialize(ctx context.Context) error {
	return nil
}

// Shutdown performs default shutdown (no-op).
func (b *BaseExtensionPoint) Shutdown(ctx context.Context) error {
	return nil
}

// HealthCheck performs default health check (always healthy).
func (b *BaseExtensionPoint) HealthCheck(ctx context.Context) error {
	return nil
}

// ExtensionPointRegistry manages registered extension points.
// This is used by the ExecutionEngine to discover and invoke plugins.
type ExtensionPointRegistry interface {
	// Register registers a plugin's extension point.
	Register(phase Phase, plugin ExtensionPoint) error

	// Unregister removes a plugin's extension point.
	Unregister(phase Phase, pluginID string) error

	// Get retrieves all extension points for a phase, ordered by priority.
	Get(phase Phase) []ExtensionPoint

	// GetByID retrieves a specific plugin's extension point.
	GetByID(pluginID string) (ExtensionPoint, error)

	// List returns all registered extension points.
	List() map[Phase][]ExtensionPoint
}
