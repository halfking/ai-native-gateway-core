package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

func TestDispatchRequestBody_KnownLengthCaps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int64
		limit int64
	}{
		{"ingress_bypass", (32 << 20) + 1, 32 << 20},
		{"at_dispatch_maximum", MaxDispatchBodyBytes, 32 << 20},
		{"dispatch_maximum", MaxDispatchBodyBytes + 1, MaxDispatchBodyBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
			req.ContentLength = tc.size
			_, _, _, err := dispatchRequestBody(req)
			var mbErr *http.MaxBytesError
			if !errors.As(err, &mbErr) || mbErr.Limit != tc.limit {
				t.Errorf("size %d: error=%v, want MaxBytesError limit=%d", tc.size, err, tc.limit)
			}

			fallbackCalled := false
			h := v2DispatchHandler(&v2DispatchDeps{Pipeline: pipeline.NewRequestPipeline()}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
			req.ContentLength = tc.size
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge || fallbackCalled {
				t.Fatalf("status=%d fallback=%v; want 413 without fallback", rec.Code, fallbackCalled)
			}
			if !strings.Contains(rec.Body.String(), fmt.Sprintf("exceeds %d bytes", tc.limit)) {
				t.Fatalf("response must report effective limit %d: %s", tc.limit, rec.Body.String())
			}
		})
	}
}

// D-A2 (2026-11-14 audit relay): chunked bodies carry
// ContentLength == -1, which blinded both static ContentLength
// pre-checks — a non-archived chunked body used to slip past the
// 32 MiB ingress gate and get fully buffered (a Content-Length twin
// would have been 413'd). The MaxBytesReader wrap now enforces the
// same effective limit the static checks apply.
func TestDispatchRequestBody_ChunkedNonArchivedCaps(t *testing.T) {
	// 32 MiB + 1 KiB chunked body, no archive in context: must hit the
	// 32 MiB gate (not the 100 MiB dispatch maximum the old wrap used).
	oversize := strings.Repeat("a", (32<<20)+1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(oversize))
	req.ContentLength = -1 // chunked
	_, _, _, err := dispatchRequestBody(req)
	var mbErr *http.MaxBytesError
	if !errors.As(err, &mbErr) || mbErr.Limit != 32<<20 {
		t.Fatalf("chunked oversize: error=%v, want MaxBytesError limit=%d", err, int64(32<<20))
	}

	fallbackCalled := false
	h := v2DispatchHandler(&v2DispatchDeps{Pipeline: pipeline.NewRequestPipeline()}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(oversize))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || fallbackCalled {
		t.Fatalf("status=%d fallback=%v; want 413 without fallback", rec.Code, fallbackCalled)
	}
	if !strings.Contains(rec.Body.String(), fmt.Sprintf("exceeds %d bytes", 32<<20)) {
		t.Fatalf("response must report effective limit %d: %s", int64(32<<20), rec.Body.String())
	}
}

func TestDispatchRequestBody_ChunkedUnderGateSucceeds(t *testing.T) {
	// Normal chunked traffic (ContentLength == -1) below the 32 MiB
	// gate must keep flowing: sniff works, body round-trips.
	body := `{"model":"gpt-4","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	model, stream, raw, err := dispatchRequestBody(req)
	if err != nil {
		t.Fatalf("chunked under-gate body must succeed: %v", err)
	}
	if model != "gpt-4" || !stream || string(raw) != body {
		t.Fatalf("chunked sniff broken: model=%q stream=%v rawLen=%d", model, stream, len(raw))
	}
}
