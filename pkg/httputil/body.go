// Package httputil provides HTTP utility functions for the LLM Gateway.
package httputil

import (
	"io"
	"log/slog"
)

// DrainAndClose drains the response body before closing it to enable
// HTTP/1.1 connection reuse. Without draining, the connection may be
// closed instead of returned to the pool, reducing connection efficiency.
//
// This function limits the drain to 64KB to prevent unbounded memory
// consumption from large response bodies. If the body is larger, the
// connection may still be closed, but this is acceptable for error paths.
//
// Usage:
//
//	resp, err := http.DefaultClient.Do(req)
//	if err != nil {
//	    return err
//	}
//	defer DrainAndClose(resp.Body)
//
// The function is safe to call on nil readers (no-op) and always closes
// the body even if draining fails.
func DrainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	// Drain up to 64KB to enable connection reuse. LimitReader ensures
	// we don't consume unbounded memory on large error responses.
	_, drainErr := io.Copy(io.Discard, io.LimitReader(body, 64*1024))
	if drainErr != nil {
		// Log at debug level — drain errors are not critical since we're
		// likely on an error path already. The connection may be closed
		// instead of reused, but this is acceptable.
		slog.Debug("httputil: failed to drain response body",
			"error", drainErr)
	}
	// Always close, even if drain failed.
	if closeErr := body.Close(); closeErr != nil {
		slog.Debug("httputil: failed to close response body",
			"error", closeErr)
	}
}

// DrainAndCloseUnlimited drains the entire response body before closing it.
// Use this variant when you know the body is small (e.g., success responses
// from well-behaved APIs) and want to maximize connection reuse.
//
// For error paths or untrusted responses, prefer DrainAndClose which limits
// the drain to 64KB.
func DrainAndCloseUnlimited(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, drainErr := io.Copy(io.Discard, body)
	if drainErr != nil {
		slog.Debug("httputil: failed to drain response body (unlimited)",
			"error", drainErr)
	}
	if closeErr := body.Close(); closeErr != nil {
		slog.Debug("httputil: failed to close response body",
			"error", closeErr)
	}
}
