// Package upstream provides an HTTP client wrapper for upstream LLM provider
// calls with configurable timeouts, retry, and error classification.
package upstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

const (
	maxRetries     = 2
	retryBaseDelay = 500 * time.Millisecond
	connectTimeout = 10 * time.Second
	// The stream executor owns the 30s first-byte product policy. The transport
	// must allow slower reasoning models to reach their headers first.
	defaultHeaderTimeout = 120 * time.Second
	maxErrorBodyBytes    = 4096
)

type ErrorKind = errorsx.ErrorKind

var (
	KindTransient    = errorsx.KindTransient
	KindTimeout      = errorsx.KindTimeout
	KindNetwork      = errorsx.KindNetwork
	KindRateLimit    = errorsx.KindRateLimit
	KindAuth         = errorsx.KindAuth
	KindQuota        = errorsx.KindQuota
	KindUpstreamDown = errorsx.KindUpstreamDown
)

type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
	// 2026-06-23 P0 audit: capture upstream response body (capped at 4KB)
	// so transient/5xx errors include the vendor's actual error message
	// in request_logs.response_preview instead of being recorded as an
	// empty preview. Without this, "error_kind=transient" rows are
	// diagnostically useless — operators cannot tell whether the cause
	// is vendor-side rate limiting, auth, or a network blip.
	Body []byte
	// StatusCode is the upstream HTTP status code when the response
	// was readable (0 for pure network errors like connection reset).
	StatusCode int
}

func (e *Error) Error() string { return fmt.Sprintf("[%s] %s: %v", e.Kind, e.Message, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Client wraps http.Client with upstream-specific configuration.
type Client struct {
	hc         *http.Client
	maxRetries int
	baseDelay  time.Duration
	proxy      *ProxyResolver
}

// New creates a new upstream client with sensible defaults. The proxy
// behaviour is controlled by a ProxyResolver that decides per-host whether
// to use HTTP_PROXY or go direct (see NewProxyResolver).
func New() *Client {
	return NewWithRetries(maxRetries)
}

// NewWithRetries creates an upstream client with the given number of
// internal retries. The chat / anthropic path (via Executor) should pass
// 0 here so retries are owned by the routing layer (which can switch
// credentials, update health state, and skip client-bug kinds); other
// callers (e.g. embeddings) can keep the old default of 2 retries.
//
// OPT-4 (2026-07-12): previously all callers shared maxRetries=2,
// resulting in 2 (upstream) × N (per-credential) × M (candidates) up to
// 6N×M upstream dials for a single failed request. With internal retries
// = 0 the routing executor's candidate loop owns retry decisions
// entirely, which keeps the worst case at N×M (still bounded by the
// number of candidates, not by retry amplification).
func NewWithRetries(maxRetries int) *Client {
	proxy := NewProxyResolver()
	if maxRetries < 0 {
		maxRetries = 0
	}
	return &Client{
		hc: &http.Client{
			Transport: &http.Transport{
				Proxy:                 proxy.ProxyFunc(),
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: responseHeaderTimeout(),
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
				DialContext: (&net.Dialer{
					Timeout:   connectTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:        128,
				MaxIdleConnsPerHost: 32,
			},
		},
		maxRetries: maxRetries,
		baseDelay:  retryBaseDelay,
		proxy:      proxy,
	}
}

func responseHeaderTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv("LLM_GATEWAY_RESPONSE_HEADER_TIMEOUT"))
	if value == "" {
		return defaultHeaderTimeout
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration
	}
	return defaultHeaderTimeout
}

// ProxyStatus returns a snapshot of the proxy resolver state.
func (c *Client) ProxyStatus() map[string]any {
	if c.proxy == nil {
		return map[string]any{"healthy": false, "proxy": ""}
	}
	return c.proxy.Status()
}

// Proxy returns the underlying ProxyResolver so other handlers (e.g. healthz)
// can read its state. May return nil if the client was constructed without a
// resolver.
func (c *Client) Proxy() *ProxyResolver {
	return c.proxy
}

// Stop releases the background probe goroutine.
func (c *Client) Stop() {
	if c.hc != nil {
		c.hc.CloseIdleConnections()
	}
	if c.proxy != nil {
		c.proxy.Stop()
	}
}

// Do sends an HTTP request with retry and error classification.
// It does NOT close the response body on success — caller must do that.
// On retryable errors after exhausting retries, the response body IS closed.
func (c *Client) Do(req *http.Request) (*http.Response, *Error) {
	var (
		resp *http.Response
		uErr *Error
	)
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, &Error{Kind: KindTransient, Message: "rewind body failed", Err: err}
				}
				req.Body = body
			}
			delay := c.baseDelay * (1 << (attempt - 1))
			slog.Debug("upstream retry", "attempt", attempt, "delay_ms", delay.Milliseconds())
			select {
			case <-req.Context().Done():
				return nil, &Error{Kind: KindTimeout, Message: "context cancelled", Err: req.Context().Err()}
			case <-time.After(delay):
			}
		}

		var doErr error
		resp, doErr = c.hc.Do(req)
		if doErr == nil && resp.StatusCode < 500 {
			return resp, nil
		}

		kind := errorsx.ClassifyError(doErr, resp)
		if !errorsx.IsRetryable(kind) {
			msg := ""
			var bodyBytes []byte
			statusCode := 0
			if doErr != nil {
				msg = doErr.Error()
			} else if resp != nil {
				statusCode = resp.StatusCode
				// 2026-06-23 P0: capture upstream body (4KB cap) so transient
				// errors have a diagnostic message in request_logs.
				body := captureErrorBody(resp, true)
				bodyBytes = body
				msg = strings.TrimSpace(string(body))
				if msg == "" {
					msg = fmt.Sprintf("HTTP %d (empty body)", resp.StatusCode)
				}
			}
			return resp, &Error{Kind: kind, Message: msg, Err: doErr, Body: bodyBytes, StatusCode: statusCode}
		}
		// Retryable error — capture body from this attempt too so the
		// final "retry exhausted" Error carries the diagnostic message.
		var bodyBytes []byte
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
			body := captureErrorBody(resp, attempt == c.maxRetries)
			bodyBytes = body
		}
		if doErr != nil {
			uErr = &Error{Kind: kind, Message: doErr.Error(), Err: doErr, Body: bodyBytes, StatusCode: statusCode}
		} else if len(bodyBytes) > 0 {
			msg := strings.TrimSpace(string(bodyBytes))
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d (empty body)", statusCode)
			}
			uErr = &Error{Kind: kind, Message: msg, Err: doErr, Body: bodyBytes, StatusCode: statusCode}
		} else {
			uErr = &Error{Kind: kind, Message: "retry exhausted", Err: doErr, Body: bodyBytes, StatusCode: statusCode}
		}
	}
	return resp, uErr
}

// captureErrorBody consumes an error response once and restores a readable
// body so callers can still inspect or relay the response after Do returns.
func captureErrorBody(resp *http.Response, restore bool) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	//nolint:errcheck // best-effort close
	resp.Body.Close()
	if restore {
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return body
}

// BuildUpstreamRequest creates an HTTP request to the upstream LLM provider.
func BuildUpstreamRequest(
	ctx context.Context,
	baseURL string,
	apiKey string,
	model string,
	body io.Reader,
	stream bool,
	id *identity.ClientIdentity,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamurl.ChatCompletionsURL(baseURL), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	// Rule 20 §7: X-Virtual-* headers are server-injected, delete client residue first
	virtualHeaders := []string{"X-Virtual-Client-Id", "X-Virtual-IP", "X-Virtual-MAC"}
	for _, h := range virtualHeaders {
		req.Header.Del(h)
	}
	if id != nil {
		req.Header.Set("X-Virtual-Client-Id", id.VirtualClientID)
		req.Header.Set("X-Virtual-IP", id.VirtualIP)
		req.Header.Set("X-Virtual-MAC", id.VirtualMAC)
	}
	return req, nil
}
