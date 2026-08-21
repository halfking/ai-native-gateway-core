package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlidingWindowBatch_RoutesRegistered(t *testing.T) {
	mux := http.NewServeMux()
	m := &CredentialMonitorHandlers{h: &Handler{}}
	m.RegisterMonitorRoutes(mux, identityWrap)

	req, _ := http.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", nil)
	_, pattern := mux.Handler(req)
	if pattern == "" {
		t.Fatal("route /api/credentials/sliding-window/batch is not registered")
	}
}

func TestSlidingWindowBatch_Validation(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}

	t.Run("method not allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/credentials/sliding-window/batch", nil)
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("got %d want 405", rec.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader("{"))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400", rec.Code)
		}
	})

	t.Run("items required when null", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(`{"minutes":5}`))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400 body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("empty items ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(`{"items":[]}`))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["count"] != float64(0) {
			t.Fatalf("count=%v", resp["count"])
		}
	})

	t.Run("too many items", func(t *testing.T) {
		items := make([]map[string]any, slidingWindowBatchMaxItems+1)
		for i := range items {
			items[i] = map[string]any{"credential_id": i + 1, "model": "m"}
		}
		body, _ := json.Marshal(map[string]any{"items": items})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", bytes.NewReader(body))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400 body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "at most") {
			t.Fatalf("expected at-most message, got %s", rec.Body.String())
		}
	})

	t.Run("minutes too large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch",
			strings.NewReader(`{"minutes":2000,"items":[{"credential_id":1,"model":"m"}]}`))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400", rec.Code)
		}
	})
}

func TestSlidingWindowBatch_PerItemErrorsWithoutDB(t *testing.T) {
	// No redis / no db: request_logs fallback fails for valid pairs; missing
	// fields are rejected before load. Batch must still return 200 with
	// per-item error strings (not a whole-batch 500).
	m := &CredentialMonitorHandlers{h: &Handler{}}
	body := `{"minutes":5,"items":[
		{"credential_id":0,"model":"m"},
		{"credential_id":7,"model":""},
		{"credential_id":7,"model":"claude-sonnet-5"},
		{"credential_id":8,"model":"glm-5.1"}
	]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(body))
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Count   int `json:"count"`
		Results []struct {
			CredentialID int            `json:"credential_id"`
			Model        string         `json:"model"`
			Stats        map[string]any `json:"stats"`
			Error        string         `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 4 || len(resp.Results) != 4 {
		t.Fatalf("count=%d results=%d", resp.Count, len(resp.Results))
	}
	if resp.Results[0].Error == "" || resp.Results[1].Error == "" {
		t.Fatalf("expected validation errors on first two items: %+v", resp.Results[:2])
	}
	for i := 2; i < 4; i++ {
		if resp.Results[i].Error == "" && resp.Results[i].Stats == nil {
			t.Fatalf("item %d: expected error or stats, got %+v", i, resp.Results[i])
		}
		// Without DB, load fails loudly per item.
		if resp.Results[i].Error == "" {
			t.Fatalf("item %d: expected load error without DB, got stats=%v", i, resp.Results[i].Stats)
		}
	}
}

func TestSlidingWindowBatch_MaxItemsBoundary(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	items := make([]map[string]any, slidingWindowBatchMaxItems)
	for i := range items {
		items[i] = map[string]any{"credential_id": i + 1, "model": fmt.Sprintf("m-%d", i)}
	}
	body, _ := json.Marshal(map[string]any{"minutes": 5, "items": items})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", bytes.NewReader(body))
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["count"] != float64(slidingWindowBatchMaxItems) {
		t.Fatalf("count=%v", resp["count"])
	}
}
