// Package httputil provides HTTP utility functions for the LLM Gateway.
package httputil

import (
	"errors"
	"io"
	"log/slog"
)

const DefaultBodyPrefixLimit = 64 << 10

// ReadPrefixAndDrain captures at most limit bytes from body, drains the rest,
// and closes body. It returns any read, drain, or close error. EOF while the
// body is shorter than limit is normal and is not returned as an error.
func ReadPrefixAndDrain(body io.ReadCloser, limit int) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	if limit < 0 {
		limit = 0
	}

	prefix, readErr := io.ReadAll(io.LimitReader(body, int64(limit)))
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	_, drainErr := io.Copy(io.Discard, body)
	closeErr := body.Close()
	return prefix, errors.Join(readErr, drainErr, closeErr)
}

// DrainAndClose drains the response body before closing it to enable HTTP/1.1
// connection reuse. It captures and discards at most DefaultBodyPrefixLimit
// bytes, then drains the remaining tail before closing. The body is always
// closed, and errors are logged at debug level.
func DrainAndClose(body io.ReadCloser) {
	_, err := ReadPrefixAndDrain(body, DefaultBodyPrefixLimit)
	if err != nil {
		slog.Debug("httputil: failed to drain or close response body", "error", err)
	}
}

// DrainAndCloseUnlimited drains the entire response body before closing it.
// It is retained for callers that explicitly want unlimited draining.
func DrainAndCloseUnlimited(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, drainErr := io.Copy(io.Discard, body)
	closeErr := body.Close()
	if err := errors.Join(drainErr, closeErr); err != nil {
		slog.Debug("httputil: failed to drain or close response body (unlimited)", "error", err)
	}
}
