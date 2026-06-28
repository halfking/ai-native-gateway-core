package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/redis/go-redis/v9"
)

// TestWriteCacheStatePromSnapshotInfo verifies the snapshot_info
// helper emits the expected HELP / TYPE headers and one info line
// per CacheStateEntry.
func TestWriteCacheStatePromSnapshotInfo(t *testing.T) {
	now := time.Now()
	entries := []CacheStateEntry{
		{
			CredentialID:         11,
			RawModel:             "glm-5.2",
			State:                "healthy_confirmed",
			Available:            true,
			ConsecutiveSuccesses: 3,
			ConsecutiveFailures:  0,
			Source:               "model_probe",
			UpdatedAt:            &now,
		},
	}

	w := httptest.NewRecorder()
	writeCacheStateProm(w, entries)

	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", got)
	}
	body := w.Body.String()
	for _, want := range []string{
		"# HELP llmgw_availability_cache_snapshot_info",
		"# TYPE llmgw_availability_cache_snapshot_info gauge",
		`llmgw_availability_cache_snapshot_info{credential_id="11",raw_model="glm-5.2",state="healthy_confirmed",source="model_probe"} 1`,
		`llmgw_availability_cache_available{credential_id="11",raw_model="glm-5.2"} 1`,
		`llmgw_availability_cache_consecutive_successes{credential_id="11",raw_model="glm-5.2"} 3`,
		`llmgw_availability_cache_consecutive_failures{credential_id="11",raw_model="glm-5.2"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestWriteCacheStatePromEmpty(t *testing.T) {
	w := httptest.NewRecorder()
	writeCacheStateProm(w, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "# HELP llmgw_availability_cache_snapshot_info") {
		t.Fatalf("expected HELP header, got %q", body)
	}
	if strings.Contains(body, "llmgw_availability_cache_snapshot_info{") {
		t.Fatalf("empty list should emit no info line, got %q", body)
	}
}

func TestWriteCacheStatePromUnavailableEntry(t *testing.T) {
	entries := []CacheStateEntry{
		{
			CredentialID: 7,
			RawModel:     "minimax-m3",
			State:        "broken_confirmed",
			Available:    false,
			Source:       "model_probe",
		},
	}
	w := httptest.NewRecorder()
	writeCacheStateProm(w, entries)
	body := w.Body.String()
	if !strings.Contains(body, `llmgw_availability_cache_available{credential_id="7",raw_model="minimax-m3"} 0`) {
		t.Fatalf("missing available=0 line: %s", body)
	}
}

// TestHandleProbeCacheStateFormatProm end-to-end: hit the endpoint
// with ?format=prom and verify the response body is exposition text.
func TestHandleProbeCacheStateFormatProm(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := client.HSet(ctx, "llmgw:avail:11:glm-5.2", map[string]any{
		"state":                 "healthy_confirmed",
		"consecutive_successes": "3",
	}).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reader := bg.NewModelAvailabilityReader(client)

	h := &Handler{availabilityReader: reader, redisClient: client}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/admin/probe/cache-state?credential_id=11&model=glm-5.2&format=prom", nil)
	h.handleProbeCacheState(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "# HELP llmgw_availability_cache_snapshot_info") {
		t.Fatalf("missing HELP header, body=%s", body)
	}
	if !strings.Contains(body, `state="healthy_confirmed"`) {
		t.Fatalf("expected state label, body=%s", body)
	}
}

// TestHandleProbeCacheStateFormatDefault still returns JSON when no
// format is specified.  Regression guard for the format=prom branch
// being unreachable in the default path.
func TestHandleProbeCacheStateFormatDefault(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := client.HSet(ctx, "llmgw:avail:11:glm-5.2", map[string]any{
		"state": "healthy_confirmed",
	}).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reader := bg.NewModelAvailabilityReader(client)
	h := &Handler{availabilityReader: reader, redisClient: client}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/admin/probe/cache-state?credential_id="+strconv.Itoa(11)+"&model=glm-5.2", nil)
	h.handleProbeCacheState(w, r)

	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json", got)
	}
}

// TestHandleProbeCacheStateEmptyRedis guards against the failure mode
// that originally made /probe-health render zero rows: the scan path
// must NOT 500 when Redis has no availability keys yet (cold deploy /
// after FLUSHDB). The response must be a well-formed JSON payload
// with count=0 and an empty entries array.
func TestHandleProbeCacheStateEmptyRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	reader := bg.NewModelAvailabilityReader(client)
	h := &Handler{availabilityReader: reader, redisClient: client}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/probe/cache-state", nil)
	h.handleProbeCacheState(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"count":0`) {
		t.Fatalf(`expected "count":0 in body, got %s`, body)
	}
	if !strings.Contains(body, `"entries":[]`) {
		t.Fatalf(`expected "entries":[] in body, got %s`, body)
	}
}
