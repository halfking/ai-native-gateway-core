package extpoints

import (
	"net/http"
	"time"
)

// Response represents a response from the upstream or gateway.
type Response struct {
	// HTTP response details
	StatusCode int
	Headers    http.Header
	Body       []byte

	// Content metadata
	ContentType   string
	ContentLength int64

	// Timing information
	UpstreamTime time.Duration
	TotalTime    time.Duration

	// Error information (if any)
	Error      error
	ErrorPhase Phase

	// Upstream information
	UpstreamHost string
	UpstreamAddr string

	// Custom attributes (set by plugins)
	Attributes map[string]interface{}
}

// ResponseInterceptor defines the interface for plugins that intercept responses.
// These plugins execute in the post-upstream, pre-response, or response phases.
type ResponseInterceptor interface {
	PhaseExecutor

	// InterceptResponse processes the response from upstream.
	// Returns the modified response and whether it was changed.
	InterceptResponse(ctx *ExecutionContext, req *Request, resp *Response) (*Response, bool, error)
}

// ResponseTransformer defines the interface for plugins that transform responses.
type ResponseTransformer interface {
	PhaseExecutor

	// TransformResponse modifies the response before sending to client.
	// Returns the transformed response.
	TransformResponse(ctx *ExecutionContext, req *Request, resp *Response) (*Response, error)
}

// ResponseValidator defines the interface for plugins that validate responses.
type ResponseValidator interface {
	PhaseExecutor

	// ValidateResponse checks if the response is valid.
	// Returns nil if valid, error otherwise.
	ValidateResponse(ctx *ExecutionContext, resp *Response) error
}

// ResponseFilter defines the interface for plugins that can block responses.
type ResponseFilter interface {
	PhaseExecutor

	// ShouldAllow determines if the response should be sent to the client.
	// Returns true to allow, false to block.
	ShouldAllow(ctx *ExecutionContext, resp *Response) (bool, string, error)
}

// ResponseEnricher defines the interface for plugins that enrich responses
// with additional headers or metadata.
type ResponseEnricher interface {
	PhaseExecutor

	// EnrichResponse adds data to the response without modifying core content.
	// Returns the enriched response.
	EnrichResponse(ctx *ExecutionContext, req *Request, resp *Response) (*Response, error)
}

// ResponseCache defines the interface for caching plugins.
type ResponseCache interface {
	PhaseExecutor

	// Get retrieves a cached response for the request.
	// Returns the cached response and true if found.
	Get(ctx *ExecutionContext, req *Request) (*Response, bool, error)

	// Set stores a response in the cache.
	Set(ctx *ExecutionContext, req *Request, resp *Response, ttl time.Duration) error

	// Invalidate removes cached responses matching the pattern.
	Invalidate(ctx *ExecutionContext, pattern string) error
}

// ResponseBodyTransformer defines the interface for plugins that transform response bodies.
type ResponseBodyTransformer interface {
	PhaseExecutor

	// TransformBody modifies the response body.
	// Returns the transformed body and content type.
	TransformBody(ctx *ExecutionContext, body []byte, contentType string) ([]byte, string, error)
}

// ResponseHeaderManipulator defines the interface for plugins that modify response headers.
type ResponseHeaderManipulator interface {
	PhaseExecutor

	// ManipulateHeaders adds, modifies, or removes response headers.
	// Returns the modified headers.
	ManipulateHeaders(ctx *ExecutionContext, headers http.Header) (http.Header, error)
}

// ErrorHandler defines the interface for error handling plugins.
// These plugins execute in the PhaseError when any phase fails.
type ErrorHandler interface {
	PhaseExecutor

	// HandleError processes an error that occurred during pipeline execution.
	// Returns a response to send to the client, or nil to use default error handling.
	HandleError(ctx *ExecutionContext, req *Request, err error, phase Phase) (*Response, error)
}

// ErrorTransformer defines the interface for plugins that transform error responses.
type ErrorTransformer interface {
	PhaseExecutor

	// TransformError converts an error into a formatted response.
	// Returns the error response.
	TransformError(ctx *ExecutionContext, req *Request, err error) (*Response, error)
}

// ContentCompressor defines the interface for response compression plugins.
type ContentCompressor interface {
	PhaseExecutor

	// Compress compresses the response body based on Accept-Encoding.
	// Returns the compressed body and encoding used.
	Compress(ctx *ExecutionContext, body []byte, acceptEncoding string) ([]byte, string, error)
}

// ContentDecompressor defines the interface for request decompression plugins.
type ContentDecompressor interface {
	PhaseExecutor

	// Decompress decompresses the request body based on Content-Encoding.
	// Returns the decompressed body.
	Decompress(ctx *ExecutionContext, body []byte, contentEncoding string) ([]byte, error)
}

// SecurityHeaderInjector defines the interface for security header plugins.
type SecurityHeaderInjector interface {
	PhaseExecutor

	// InjectSecurityHeaders adds security-related headers to the response.
	// Returns headers to add.
	InjectSecurityHeaders(ctx *ExecutionContext, req *Request, resp *Response) (http.Header, error)
}

// ContentSanitizer defines the interface for content sanitization plugins.
type ContentSanitizer interface {
	PhaseExecutor

	// SanitizeContent removes or masks sensitive data from the response.
	// Returns the sanitized body.
	SanitizeContent(ctx *ExecutionContext, body []byte, contentType string) ([]byte, error)
}

// ResponseModifier defines the interface for plugins that can completely
// replace the response (e.g., for mocking, testing, or synthetic responses).
type ResponseModifier interface {
	PhaseExecutor

	// ModifyResponse can completely replace the response.
	// Returns the new response and true if modified.
	ModifyResponse(ctx *ExecutionContext, req *Request, resp *Response) (*Response, bool, error)
}

// StreamInterceptor defines the interface for plugins that intercept streaming responses.
type StreamInterceptor interface {
	PhaseExecutor

	// InterceptStream processes a chunk of streaming response data.
	// Returns the modified chunk and whether it was changed.
	InterceptStream(ctx *ExecutionContext, chunk []byte) ([]byte, bool, error)

	// OnStreamStart is called when streaming begins.
	OnStreamStart(ctx *ExecutionContext, req *Request) error

	// OnStreamEnd is called when streaming completes.
	OnStreamEnd(ctx *ExecutionContext, req *Request, totalBytes int64) error
}
