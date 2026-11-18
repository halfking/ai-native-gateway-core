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

// ResponseMetricsPlugin is a demo plugin that collects response metrics.
// It runs in PhasePostUpstream to analyze the response from upstream LLM.
type ResponseMetricsPlugin struct {
	*extpoints.BaseExtensionPoint
	metrics map[string]interface{}
}

// NewResponseMetricsPlugin creates a new response metrics plugin.
func NewResponseMetricsPlugin() *ResponseMetricsPlugin {
	return &ResponseMetricsPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(&extpoints.PluginMetadata{
			PluginID:       "demo-response-metrics",
			Version:        "1.0.0",
			Phase:          extpoints.PhasePostUpstream,
			Priority:       extpoints.PriorityNormal,
			Timeout:        2 * time.Second,
			FailurePolicy:  extpoints.FailureOpen,
			Capabilities:   []extpoints.PluginCapability{extpoints.CapabilityMutateResponse},
		}),
		metrics: make(map[string]interface{}),
	}
}

// Execute implements PhaseExecutor interface.
func (p *ResponseMetricsPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	fmt.Printf("[ResponseMetrics] Collecting metrics in phase %s\n", ctx.Phase)
	
	// Extract response info from State (set by upstream call)
	var responseSize int64
	var responseTime time.Duration
	
	if ctx.State != nil {
		if size, ok := ctx.State["response_size"].(int64); ok {
			responseSize = size
		}
		if rt, ok := ctx.State["response_time"].(time.Duration); ok {
			responseTime = rt
		}
	}
	
	// Record metrics
	p.metrics["response_size"] = responseSize
	p.metrics["response_time_ms"] = responseTime.Milliseconds()
	p.metrics["timestamp"] = ctx.RequestTime
	
	fmt.Printf("[ResponseMetrics] Recorded: size=%d bytes, time=%dms\n", 
		responseSize, responseTime.Milliseconds())
	
	return &extpoints.ExecutionResult{
		Success:  true,
		Modified: false,
		Latency:  1 * time.Millisecond,
		Metadata: map[string]interface{}{
			"metrics_collected": len(p.metrics),
			"response_size":     responseSize,
			"response_time_ms":  responseTime.Milliseconds(),
		},
	}, nil
}

// InterceptResponse implements ResponseInterceptor interface.
func (p *ResponseMetricsPlugin) InterceptResponse(ctx *extpoints.ExecutionContext, resp *extpoints.Response) (*extpoints.Response, bool, error) {
	// Calculate response size
	responseSize := int64(len(resp.Body))
	
	// Record in metrics
	p.metrics["status_code"] = resp.StatusCode
	p.metrics["response_size"] = responseSize
	p.metrics["headers_count"] = len(resp.Headers)
	
	// Add metric headers to response
	if resp.Headers == nil {
		resp.Headers = make(http.Header)
	}
	resp.Headers.Set("X-Response-Size", fmt.Sprintf("%d", responseSize))
	resp.Headers.Set("X-Plugin-Processed", "demo-response-metrics")
	
	fmt.Printf("[ResponseMetrics] Intercepted response: status=%d, size=%d bytes\n", 
		resp.StatusCode, responseSize)
	
	return resp, true, nil
}

// GetMetrics returns collected metrics (for testing).
func (p *ResponseMetricsPlugin) GetMetrics() map[string]interface{} {
	return p.metrics
}

// RequestModifierPlugin is a demo plugin that modifies requests before upstream call.
// It runs in PhasePreUpstream to add custom parameters or modify the request.
type RequestModifierPlugin struct {
	*extpoints.BaseExtensionPoint
	modifications map[string]interface{}
}

// NewRequestModifierPlugin creates a new request modifier plugin.
func NewRequestModifierPlugin() *RequestModifierPlugin {
	return &RequestModifierPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(&extpoints.PluginMetadata{
			PluginID:       "demo-request-modifier",
			Version:        "1.0.0",
			Phase:          extpoints.PhasePreUpstream,
			Priority:       extpoints.PriorityNormal,
			Timeout:        2 * time.Second,
			FailurePolicy:  extpoints.FailureOpen,
			Capabilities:   []extpoints.PluginCapability{extpoints.CapabilityMutateRequest},
		}),
		modifications: make(map[string]interface{}),
	}
}

// Execute implements PhaseExecutor interface.
func (p *RequestModifierPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	fmt.Printf("[RequestModifier] Modifying request in phase %s\n", ctx.Phase)
	
	// Track modifications
	modCount := 0
	
	// Example: Add custom parameters to State
	if ctx.State != nil {
		// Add temperature override
		ctx.State["temperature_override"] = 0.7
		modCount++
		
		// Add max_tokens
		ctx.State["max_tokens_override"] = 2000
		modCount++
		
		// Add system prompt prefix
		ctx.State["system_prompt_prefix"] = "You are a helpful assistant. "
		modCount++
	}
	
	// Record modifications
	p.modifications["temperature_override"] = 0.7
	p.modifications["max_tokens_override"] = 2000
	p.modifications["modification_count"] = modCount
	
	fmt.Printf("[RequestModifier] Applied %d modifications\n", modCount)
	
	return &extpoints.ExecutionResult{
		Success:  true,
		Modified: true,
		Latency:  1 * time.Millisecond,
		Metadata: map[string]interface{}{
			"modifications_applied": modCount,
			"temperature_override":  0.7,
			"max_tokens_override":   2000,
		},
	}, nil
}

// TransformRequest implements RequestTransformer interface.
func (p *RequestModifierPlugin) TransformRequest(ctx *extpoints.ExecutionContext, req *extpoints.Request) (*extpoints.Request, bool, error) {
	// Modify request attributes
	if req.Attributes == nil {
		req.Attributes = make(map[string]interface{})
	}
	
	// Add custom headers
	if req.Headers == nil {
		req.Headers = make(http.Header)
	}
	req.Headers.Set("X-Modified-By", "demo-request-modifier")
	req.Headers.Set("X-Modification-Time", time.Now().Format(time.RFC3339))
	
	// Add to attributes
	req.Attributes["modified_by_plugin"] = "demo-request-modifier"
	req.Attributes["temperature_override"] = 0.7
	req.Attributes["max_tokens_override"] = 2000
	
	fmt.Printf("[RequestModifier] Transformed request: added custom attributes and headers\n")
	
	return req, true, nil
}

// GetModifications returns applied modifications (for testing).
func (p *RequestModifierPlugin) GetModifications() map[string]interface{} {
	return p.modifications
}
