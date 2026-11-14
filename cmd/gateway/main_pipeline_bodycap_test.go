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
