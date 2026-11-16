// Package extpoints defines extension point interfaces for the plugin system.
// 
// The 16-Phase Pipeline provides a structured execution model where plugins can
// hook into specific phases of request processing. Each phase represents a distinct
// point in the request/response lifecycle.
package extpoints

import (
	"context"
	"time"
)

// Phase represents a specific point in the request processing pipeline.
type Phase string

const (
	// Authentication & Authorization Phases
	PhasePreAuth  Phase = "pre_auth"   // Before authentication
	PhaseAuth     Phase = "auth"       // Authentication execution
	PhasePostAuth Phase = "post_auth"  // After authentication

	// Routing Phases
	PhasePreRoute  Phase = "pre_route"  // Before routing decision
	PhaseRoute     Phase = "route"      // Routing execution
	PhasePostRoute Phase = "post_route" // After routing decision

	// Upstream Communication Phases
	PhasePreUpstream  Phase = "pre_upstream"  // Before upstream call
	PhaseUpstream     Phase = "upstream"      // Upstream execution
	PhasePostUpstream Phase = "post_upstream" // After upstream call

	// Response Phases
	PhasePreResponse  Phase = "pre_response"  // Before response processing
	PhaseResponse     Phase = "response"      // Response processing
	PhasePostResponse Phase = "post_response" // After response processing

	// Logging & Audit Phases
	PhasePreLog  Phase = "pre_log"  // Before logging
	PhaseLog     Phase = "log"      // Logging execution
	PhasePostLog Phase = "post_log" // After logging

	// Error Handling Phase
	PhaseError Phase = "error" // Error handling (invoked on any phase failure)
)

// AllPhases returns all phases in execution order (excluding PhaseError).
func AllPhases() []Phase {
	return []Phase{
		PhasePreAuth,
		PhaseAuth,
		PhasePostAuth,
		PhasePreRoute,
		PhaseRoute,
		PhasePostRoute,
		PhasePreUpstream,
		PhaseUpstream,
		PhasePostUpstream,
		PhasePreResponse,
		PhaseResponse,
		PhasePostResponse,
		PhasePreLog,
		PhaseLog,
		PhasePostLog,
	}
}

// IsValid checks if a phase is valid.
func (p Phase) IsValid() bool {
	switch p {
	case PhasePreAuth, PhaseAuth, PhasePostAuth,
		PhasePreRoute, PhaseRoute, PhasePostRoute,
		PhasePreUpstream, PhaseUpstream, PhasePostUpstream,
		PhasePreResponse, PhaseResponse, PhasePostResponse,
		PhasePreLog, PhaseLog, PhasePostLog,
		PhaseError:
		return true
	default:
		return false
	}
}

// String returns the string representation of the phase.
func (p Phase) String() string {
	return string(p)
}

// Priority defines the execution priority for plugins within a phase.
// Lower values execute first.
type Priority int

const (
	PriorityHighest Priority = 0
	PriorityHigh    Priority = 100
	PriorityNormal  Priority = 500
	PriorityLow     Priority = 900
	PriorityLowest  Priority = 1000
)

// ExecutionContext provides context for plugin execution.
type ExecutionContext struct {
	// Request context
	Context context.Context

	// Phase being executed
	Phase Phase

	// Plugin ID
	PluginID string

	// Shared state across plugins (read/write)
	State map[string]interface{}

	// Request metadata
	RequestID   string
	RequestTime time.Time

	// Timeout for this execution
	Timeout time.Duration
}

// ExecutionResult represents the result of a plugin execution.
type ExecutionResult struct {
	// Success indicates if the plugin executed successfully
	Success bool

	// Error contains any error that occurred
	Error error

	// Modified indicates if the plugin modified the request/response
	Modified bool

	// Latency is the execution duration
	Latency time.Duration

	// Metadata for logging and debugging
	Metadata map[string]interface{}
}

// PluginCapability defines what a plugin can do.
type PluginCapability string

const (
	// Request capabilities
	CapabilityObserveRequest  PluginCapability = "request.observe"
	CapabilityMutateRequest   PluginCapability = "request.mutate"
	CapabilityBlockRequest    PluginCapability = "request.block"

	// Response capabilities
	CapabilityObserveResponse PluginCapability = "response.observe"
	CapabilityMutateResponse  PluginCapability = "response.mutate"
	CapabilityBlockResponse   PluginCapability = "response.block"

	// Authentication capabilities
	CapabilityAuthenticate PluginCapability = "auth.authenticate"
	CapabilityAuthorize    PluginCapability = "auth.authorize"

	// Routing capabilities
	CapabilityRoute       PluginCapability = "route.decide"
	CapabilityLoadBalance PluginCapability = "route.load_balance"

	// Logging capabilities
	CapabilityAuditLog  PluginCapability = "log.audit"
	CapabilityMetrics   PluginCapability = "log.metrics"
	CapabilityTracing   PluginCapability = "log.tracing"

	// Error handling
	CapabilityErrorHandle PluginCapability = "error.handle"
)

// PluginMetadata contains metadata about a plugin's extension point registration.
type PluginMetadata struct {
	// Plugin identification
	PluginID string
	Version  string

	// Extension point configuration
	Phase        Phase
	Priority     Priority
	Capabilities []PluginCapability

	// Execution constraints
	Timeout         time.Duration
	MaxConcurrency  int
	FailurePolicy   FailurePolicy

	// Scope filters (empty means all)
	TenantScope []string // Only execute for these tenants
	ModelScope  []string // Only execute for these models
	RouteScope  []string // Only execute for these routes
}

// FailurePolicy defines how the pipeline handles plugin failures.
type FailurePolicy string

const (
	// FailureOpen continues pipeline execution on plugin failure
	FailureOpen FailurePolicy = "fail_open"

	// FailureClosed stops pipeline execution on plugin failure
	FailureClosed FailurePolicy = "fail_closed"

	// FailureLog logs the error but continues execution
	FailureLog FailurePolicy = "fail_log"

	// FailureRetry retries the plugin execution
	FailureRetry FailurePolicy = "fail_retry"
)

// CallbackMode defines how a plugin is invoked.
type CallbackMode string

const (
	// Synchronous execution (blocks pipeline)
	CallbackSync CallbackMode = "sync"

	// Asynchronous execution (non-blocking)
	CallbackAsync CallbackMode = "async"

	// Fire-and-forget (no result tracking)
	CallbackFireAndForget CallbackMode = "fire_and_forget"
)
