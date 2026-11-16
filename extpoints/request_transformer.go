package extpoints

import (
	"net/http"
)

// Request represents an incoming request to the gateway.
type Request struct {
	// HTTP request details
	Method  string
	Path    string
	Headers http.Header
	Body    []byte

	// Query parameters
	QueryParams map[string][]string

	// Routing information
	Route      string
	TargetHost string
	TargetPath string

	// Authentication context
	UserID   string
	TenantID string
	Roles    []string

	// Model information (for LLM requests)
	Model    string
	Provider string

	// Request metadata
	RequestID string
	ClientIP  string
	UserAgent string

	// Custom attributes (set by plugins)
	Attributes map[string]interface{}
}

// RequestTransformer defines the interface for plugins that transform requests.
// These plugins execute in the pre-route, route, or pre-upstream phases.
type RequestTransformer interface {
	PhaseExecutor

	// TransformRequest modifies the request before it's sent upstream.
	// Returns the modified request and whether it was changed.
	TransformRequest(ctx *ExecutionContext, req *Request) (*Request, bool, error)
}

// RequestValidator defines the interface for plugins that validate requests.
type RequestValidator interface {
	PhaseExecutor

	// ValidateRequest checks if the request is valid.
	// Returns nil if valid, error otherwise.
	ValidateRequest(ctx *ExecutionContext, req *Request) error
}

// RequestEnricher defines the interface for plugins that enrich requests
// with additional data (e.g., user context, metadata).
type RequestEnricher interface {
	PhaseExecutor

	// EnrichRequest adds data to the request without modifying core fields.
	// Returns the enriched request.
	EnrichRequest(ctx *ExecutionContext, req *Request) (*Request, error)
}

// RequestRouter defines the interface for plugins that make routing decisions.
type RequestRouter interface {
	PhaseExecutor

	// Route determines where the request should be sent.
	// Returns the target host and path.
	Route(ctx *ExecutionContext, req *Request) (targetHost, targetPath string, err error)
}

// RequestFilter defines the interface for plugins that can block requests.
type RequestFilter interface {
	PhaseExecutor

	// ShouldAllow determines if the request should be allowed.
	// Returns true to allow, false to block.
	ShouldAllow(ctx *ExecutionContext, req *Request) (bool, string, error)
}

// RateLimiter defines the interface for rate limiting plugins.
type RateLimiter interface {
	PhaseExecutor

	// CheckLimit checks if the request exceeds rate limits.
	// Returns true if within limits, false if exceeded.
	CheckLimit(ctx *ExecutionContext, req *Request) (allowed bool, retryAfter int, err error)
}

// RequestRewriter defines the interface for plugins that rewrite request URLs.
type RequestRewriter interface {
	PhaseExecutor

	// RewriteURL rewrites the request URL according to plugin logic.
	// Returns the rewritten path and query parameters.
	RewriteURL(ctx *ExecutionContext, req *Request) (newPath string, newQuery map[string][]string, err error)
}

// HeaderManipulator defines the interface for plugins that modify headers.
type HeaderManipulator interface {
	PhaseExecutor

	// ManipulateHeaders adds, modifies, or removes request headers.
	// Returns the modified headers.
	ManipulateHeaders(ctx *ExecutionContext, headers http.Header) (http.Header, error)
}

// BodyTransformer defines the interface for plugins that transform request bodies.
type BodyTransformer interface {
	PhaseExecutor

	// TransformBody modifies the request body.
	// Returns the transformed body and content type.
	TransformBody(ctx *ExecutionContext, body []byte, contentType string) ([]byte, string, error)
}

// AuthenticationProvider defines the interface for authentication plugins.
type AuthenticationProvider interface {
	PhaseExecutor

	// Authenticate verifies the request's authentication credentials.
	// Returns the authenticated user ID and tenant ID.
	Authenticate(ctx *ExecutionContext, req *Request) (userID, tenantID string, err error)
}

// AuthorizationProvider defines the interface for authorization plugins.
type AuthorizationProvider interface {
	PhaseExecutor

	// Authorize checks if the authenticated user is authorized to perform the request.
	// Returns true if authorized, false otherwise.
	Authorize(ctx *ExecutionContext, req *Request, userID, tenantID string) (bool, string, error)
}

// LoadBalancer defines the interface for load balancing plugins.
type LoadBalancer interface {
	PhaseExecutor

	// SelectUpstream chooses which upstream server to send the request to.
	// Returns the selected upstream URL.
	SelectUpstream(ctx *ExecutionContext, req *Request, upstreams []string) (string, error)
}

// CircuitBreaker defines the interface for circuit breaker plugins.
type CircuitBreaker interface {
	PhaseExecutor

	// ShouldAttempt determines if the request should be attempted based on
	// circuit breaker state.
	ShouldAttempt(ctx *ExecutionContext, upstream string) (bool, error)

	// RecordSuccess records a successful upstream call.
	RecordSuccess(ctx *ExecutionContext, upstream string) error

	// RecordFailure records a failed upstream call.
	RecordFailure(ctx *ExecutionContext, upstream string, err error) error
}

// RetryPolicy defines the interface for retry policy plugins.
type RetryPolicy interface {
	PhaseExecutor

	// ShouldRetry determines if a failed request should be retried.
	// Returns true if retry should be attempted, along with retry delay.
	ShouldRetry(ctx *ExecutionContext, req *Request, attempt int, err error) (bool, int, error)
}
