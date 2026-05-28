// Package upstream provides an HTTP client wrapper for upstream LLM provider
// calls with configurable timeouts, retry, and error classification.
package upstream

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
)

const (
	maxRetries      = 2
	retryBaseDelay  = 500 * time.Millisecond
	defaultTimeout  = 120 * time.Second
	connectTimeout  = 10 * time.Second
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
}

func (e *Error) Error() string { return fmt.Sprintf("[%s] %s: %v", e.Kind, e.Message, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Client wraps http.Client with upstream-specific configuration.
type Client struct {
	hc          *http.Client
	maxRetries  int
	baseDelay   time.Duration
}

// New creates a new upstream client with sensible defaults.
func New() *Client {
	return &Client{
		hc: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				IdleConnTimeout: 90 * time.Second,
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
			if doErr != nil {
				msg = doErr.Error()
			} else if resp != nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				msg = strings.TrimSpace(string(body))
			}
			return resp, &Error{Kind: kind, Message: msg, Err: doErr}
		}
		if resp != nil {
			resp.Body.Close()
		}
		uErr = &Error{Kind: kind, Message: "retry exhausted", Err: doErr}
	}
	return resp, uErr
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
	base := strings.TrimRight(baseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	// Forward identity labels as upstream headers (not the raw fingerprint)
	if id != nil {
		req.Header.Set("X-Virtual-Client-Id", id.VirtualClientID)
		req.Header.Set("X-Virtual-IP", id.VirtualIP)
		req.Header.Set("X-Virtual-MAC", id.VirtualMAC)
	}
	return req, nil
}
