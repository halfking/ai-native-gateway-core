package sanitize

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

func TestSanitizeCompressionCache_MatchesMockSupplierWithoutOriginalPII(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	sanitizer, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := NewSanitizeInputMiddleware(sanitizer, rdb, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	cache := compression.NewSessionCache(nil, nil)
	compressor := compression.NewSessionCompressor(compression.SessionCompressorDeps{Cache: cache})
	var supplierBody []byte
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplierBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read supplier body failed: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(supplier.Close)

	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalBody, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read sanitized body failed: %v", readErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		result := compressor.Prepare(r.Context(), finalBody, "tenant-sani", "gw_sani01", "openai", 0, false)
		if commitErr := compressor.CommitFinal(r.Context(), "tenant-sani", "gw_sani01", finalBody, result); commitErr != nil {
			t.Errorf("commit final cache failed: %v", commitErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, supplier.URL, bytes.NewReader(finalBody))
		if reqErr != nil {
			t.Errorf("create supplier request failed: %v", reqErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp, callErr := http.DefaultClient.Do(req)
		if callErr != nil {
			t.Errorf("call supplier failed: %v", callErr)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = resp.Body.Close()
		w.WriteHeader(http.StatusOK)
	}))

	originalPII := "13800138000"
	original := `{"model":"mock","messages":[{"role":"user","content":"phone ` + originalPII + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(original))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", "gw_sani01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler status = %d, body=%s", rec.Code, rec.Body.String())
	}

	state, cachedBody, err := cache.GetOrLoad(ctx, "tenant-sani", "gw_sani01")
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.AuditedAt == 0 {
		t.Fatalf("cache missing audited state: %+v", state)
	}
	if !bytes.Equal(cachedBody, supplierBody) {
		t.Fatalf("cache/supplier mismatch:\n cache=%s\n supplier=%s", cachedBody, supplierBody)
	}
	if bytes.Contains(supplierBody, []byte(originalPII)) || bytes.Contains(cachedBody, []byte(originalPII)) {
		t.Fatalf("original PII leaked: cache=%s supplier=%s", cachedBody, supplierBody)
	}
	if !bytes.Contains(supplierBody, []byte("{SENSITIVE:phone:")) {
		t.Fatalf("supplier did not receive sanitize placeholder: %s", supplierBody)
	}
}
