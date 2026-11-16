// Package demo provides example plugin implementations for testing the ExecutionEngine.
package demo

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/extpoints"
)

// HeaderInjectorPlugin is a demo plugin that adds custom headers to requests.
// It implements the RequestTransformer interface.
type HeaderInjectorPlugin struct {
	*extpoints.BaseExtensionPoint
}

// NewHeaderInjectorPlugin creates a new header injector plugin.
func NewHeaderInjectorPlugin() *HeaderInjectorPlugin {
	return &HeaderInjectorPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(&extpoints.PluginMetadata{
			PluginID:       "demo-header-injector",
			Version:        "1.0.0",
			Phase:          extpoints.PhasePreRoute,
			Priority:       extpoints.PriorityNormal,
			Timeout:        2 * time.Second,
			FailurePolicy:  extpoints.FailureOpen,
			Capabilities:   []extpoints.PluginCapability{extpoints.CapabilityMutateRequest},
		}),
	}
}

// Execute implements PhaseExecutor interface.
func (p *HeaderInjectorPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	// In a real implementation, this would get the request from context
	// For demo purposes, we simulate the transformation
	
	fmt.Printf("[HeaderInjector] Adding custom headers in phase %s\n", ctx.Phase)
	
	// Simulate adding headers
	if ctx.State == nil {
		ctx.State = make(map[string]interface{})
	}
	ctx.State["headers_added"] = []string{"X-Gateway-Version", "X-Request-ID"}
	
	return &extpoints.ExecutionResult{
		Success:  true,
		Modified: true,
		Latency:  10 * time.Millisecond,
		Metadata: map[string]interface{}{
			"headers_added": 2,
		},
	}, nil
}

// TransformRequest implements RequestTransformer interface.
func (p *HeaderInjectorPlugin) TransformRequest(ctx *extpoints.ExecutionContext, req *extpoints.Request) (*extpoints.Request, bool, error) {
	if req.Headers == nil {
		req.Headers = make(http.Header)
	}
	
	// Add custom headers
	req.Headers.Set("X-Gateway-Version", "1.0.0")
	req.Headers.Set("X-Request-ID", req.RequestID)
	req.Headers.Set("X-Plugin-Processed", "demo-header-injector")
	
	fmt.Printf("[HeaderInjector] Transformed request: added 3 headers\n")
	
	return req, true, nil
}

// AuthenticationPlugin is a demo plugin that validates authentication.
type AuthenticationPlugin struct {
	*extpoints.BaseExtensionPoint
}

// NewAuthenticationPlugin creates a new authentication plugin.
func NewAuthenticationPlugin() *AuthenticationPlugin {
	return &AuthenticationPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(&extpoints.PluginMetadata{
			PluginID:       "demo-authenticator",
			Version:        "1.0.0",
			Phase:          extpoints.PhaseAuth,
			Priority:       extpoints.PriorityHigh, // Run early
			Timeout:        3 * time.Second,
			FailurePolicy:  extpoints.FailureClosed, // Block on auth failure
			Capabilities:   []extpoints.PluginCapability{extpoints.CapabilityAuthenticate},
		}),
	}
}

// Execute implements PhaseExecutor interface.
func (p *AuthenticationPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	fmt.Printf("[Authenticator] Validating authentication in phase %s\n", ctx.Phase)
	
	// Simulate authentication check
	if ctx.State == nil {
		ctx.State = make(map[string]interface{})
	}
	ctx.State["authenticated"] = true
	ctx.State["user_id"] = "demo-user-123"
	
	return &extpoints.ExecutionResult{
		Success:  true,
		Modified: false,
		Latency:  5 * time.Millisecond,
		Metadata: map[string]interface{}{
			"user_id": "demo-user-123",
		},
	}, nil
}

// Authenticate implements AuthenticationProvider interface.
func (p *AuthenticationPlugin) Authenticate(ctx *extpoints.ExecutionContext, req *extpoints.Request) (userID, tenantID string, err error) {
	// Check for auth token
	authHeader := req.Headers.Get("Authorization")
	if authHeader == "" {
		return "", "", fmt.Errorf("missing authorization header")
	}
	
	// Demo: extract user from token (simplified)
	userID = "demo-user-123"
	tenantID = "demo-tenant-456"
	
	fmt.Printf("[Authenticator] Authenticated user %s, tenant %s\n", userID, tenantID)
	
	return userID, tenantID, nil
}

// LoggingPlugin is a demo plugin that logs requests (async).
type LoggingPlugin struct {
	*extpoints.BaseExtensionPoint
	events []string
}

// NewLoggingPlugin creates a new logging plugin.
func NewLoggingPlugin() *LoggingPlugin {
	return &LoggingPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(&extpoints.PluginMetadata{
			PluginID:       "demo-logger",
			Version:        "1.0.0",
			Phase:          extpoints.PhaseLog,
			Priority:       extpoints.PriorityNormal,
			Timeout:        1 * time.Second,
			FailurePolicy:  extpoints.FailureLog, // Don't block on log failure
			Capabilities:   []extpoints.PluginCapability{extpoints.CapabilityAuditLog},
		}),
		events: make([]string, 0),
	}
}

// Execute implements PhaseExecutor interface.
func (p *LoggingPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	fmt.Printf("[Logger] Logging request in phase %s\n", ctx.Phase)
	
	// Log event
	event := fmt.Sprintf("request_id=%s phase=%s", ctx.RequestID, ctx.Phase)
	p.events = append(p.events, event)
	
	return &extpoints.ExecutionResult{
		Success:  true,
		Modified: false,
		Latency:  2 * time.Millisecond,
		Metadata: map[string]interface{}{
			"events_logged": len(p.events),
		},
	}, nil
}

// LogEvent implements AuditLogger interface.
func (p *LoggingPlugin) LogEvent(ctx *extpoints.ExecutionContext, event *extpoints.AuditEvent) error {
	fmt.Printf("[Logger] Audit event: type=%s request_id=%s\n", event.EventType, event.RequestID)
	
	// In a real implementation, this would write to a log aggregator
	p.events = append(p.events, event.EventType)
	
	return nil
}

// Flush implements AuditLogger interface.
func (p *LoggingPlugin) Flush() error {
	fmt.Printf("[Logger] Flushing %d events\n", len(p.events))
	return nil
}

// GetEvents returns logged events (for testing).
func (p *LoggingPlugin) GetEvents() []string {
	return p.events
}

// HealthCheckPlugin is a demo plugin that implements health reporting.
// This simulates what ai-session-manager would implement.
type HealthCheckPlugin struct {
	*extpoints.BaseExtensionPoint
	healthy bool
}

// NewHealthCheckPlugin creates a new health check plugin.
func NewHealthCheckPlugin() *HealthCheckPlugin {
	return &HealthCheckPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(&extpoints.PluginMetadata{
			PluginID:       "demo-health-checker",
			Version:        "1.0.0",
			Phase:          extpoints.PhasePreAuth,
			Priority:       extpoints.PriorityHighest, // Run first
			Timeout:        1 * time.Second,
			FailurePolicy:  extpoints.FailureOpen,
		}),
		healthy: true,
	}
}

// Execute implements PhaseExecutor interface.
func (p *HealthCheckPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	return &extpoints.ExecutionResult{
		Success:  true,
		Modified: false,
	}, nil
}

// HealthCheck implements ExtensionPoint interface.
func (p *HealthCheckPlugin) HealthCheck(ctx context.Context) error {
	if !p.healthy {
		return fmt.Errorf("plugin unhealthy")
	}
	return nil
}

// SetHealthy sets the health status (for testing).
func (p *HealthCheckPlugin) SetHealthy(healthy bool) {
	p.healthy = healthy
}
