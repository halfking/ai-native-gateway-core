// main_dispatch_bodycap_test.go — P1-20 dispatch body cap.
//
// 2026-08-26 (P1-20 fix): the dispatch path used to silently truncate
// requests at 32 MiB via io.LimitReader, then hand the truncated
// bytes to chatHandler — meaning a 33 MiB payload was processed as a
// corrupted 32 MiB one. The fix swaps the silent LimitReader for
// http.MaxBytesReader so an oversize body returns a *http.MaxBytesError
// that the caller surfaces as HTTP 413.
//
//go:build dispatch_bodycap_test
// +build dispatch_bodycap_test

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDispatchRequestBody_RejectsOversize(t *testing.T) {
	// Body just past the cap (MaxDispatchBodyBytes + 1024 bytes).
	oversize := strings.Repeat("a", MaxDispatchBodyBytes+1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(oversize))

	_, _, _, err := dispatchRequestBody(req)
	if err == nil {
		t.Fatalf("dispatchRequestBody must return error for oversize body")
	}
	var mbErr *http.MaxBytesError
	if !asMaxBytesError(err, &mbErr) {
		t.Fatalf("expected *http.MaxBytesError, got %T: %v", err, err)
	}
}

func TestDispatchRequestBody_AcceptsUnderCap(t *testing.T) {
	body := `{"model":"gpt-4","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	model, stream, raw, err := dispatchRequestBody(req)
	if err != nil {
		t.Fatalf("under-cap body must succeed: %v", err)
	}
	if model != "gpt-4" {
		t.Fatalf("model sniff failed: got %q", model)
	}
	if !stream {
		t.Fatalf("stream sniff failed: got false")
	}
	if string(raw) != body {
		t.Fatalf("rawBody round-trip mismatch")
	}
	// Body must be restored for chatHandler to re-read.
	if req.Body == nil {
		t.Fatalf("r.Body must be restored after sniff")
	}
}

func TestDispatchRequestBody_EmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	model, stream, raw, err := dispatchRequestBody(req)
	if err != nil {
		t.Fatalf("empty body must succeed: %v", err)
	}
	if model != "" || stream || raw != nil {
		t.Fatalf("empty body must yield zero values, got model=%q stream=%v raw=%v", model, stream, raw)
	}
}

// asMaxBytesError is a small wrapper so the test does not need to
// import errors at the file level.
func asMaxBytesError(err error, target **http.MaxBytesError) bool {
	for cur := err; cur != nil; {
		if mb, ok := cur.(*http.MaxBytesError); ok {
			*target = mb
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}
