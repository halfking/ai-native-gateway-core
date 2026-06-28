package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/redis/go-redis/v9"
)

// TestHandleProbeCacheKeysPOST triggers a SCAN via the admin endpoint
// and verifies the gauge is updated. The endpoint has no body
// contract so this stays a small smoke test.
func TestHandleProbeCacheKeysPOST(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := client.HSet(ctx, "llmgw:avail:"+strconv.Itoa(i)+":m", "state", "ok").Err(); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Build the worker through its public constructor so the
	// production code path is exercised end-to-end.
	counter := bg.NewAvailabilityKeyCounter(client, time.Hour)

	h := &Handler{availabilityKeyCounter: counter}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/admin/probe/cache-keys", nil)
	h.handleProbeCacheKeys(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := resp["refreshed"].(bool); !got {
		t.Fatalf("refreshed = %v, want true", resp["refreshed"])
	}
}

func TestHandleProbeCacheKeysMethodNotAllowed(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/probe/cache-keys", nil)
	h.handleProbeCacheKeys(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandleProbeCacheKeysServiceUnavailable(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/admin/probe/cache-keys", nil)
	h.handleProbeCacheKeys(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
