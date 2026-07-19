package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

func newTestHandler(t *testing.T) (*TelemetryFallbackBufferHandler, *dbdegradation.RingBuffer, *[]dbdegradation.BackupRecord) {
	t.Helper()
	rb := dbdegradation.NewRingBuffer(10)
	var replayed []dbdegradation.BackupRecord
	h := NewTelemetryFallbackBufferHandler(rb, func(ctx context.Context, rec dbdegradation.BackupRecord) error {
		replayed = append(replayed, rec)
		return nil
	})
	return h, rb, &replayed
}

func doReq(h http.Handler, method, path string, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandler_Stats_Empty(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := doReq(h, "GET", "/internal/telemetry/fallback-buffer/stats", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var s dbdegradation.RingBufferStats
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Capacity != 10 {
		t.Errorf("capacity = %d, want 10", s.Capacity)
	}
	if s.Size != 0 {
		t.Errorf("size = %d, want 0", s.Size)
	}
}

func TestHandler_Stats_AfterWrites(t *testing.T) {
	h, rb, _ := newTestHandler(t)
	for i := 0; i < 3; i++ {
		_ = rb.WriteRequestLog(context.Background(), "k", "p")
	}
	w := doReq(h, "GET", "/internal/telemetry/fallback-buffer/stats", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var s dbdegradation.RingBufferStats
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Size != 3 || s.Writes != 3 {
		t.Errorf("size=%d writes=%d, want 3/3", s.Size, s.Writes)
	}
}

func TestHandler_Dump(t *testing.T) {
	h, rb, _ := newTestHandler(t)
	for i := 0; i < 3; i++ {
		_ = rb.WriteRequestLog(context.Background(), "k", "p")
	}
	w := doReq(h, "GET", "/internal/telemetry/fallback-buffer/dump", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Count   int                          `json:"count"`
		Records []dbdegradation.BackupRecord `json:"records"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Count != 3 || len(resp.Records) != 3 {
		t.Errorf("count=%d records=%d, want 3/3", resp.Count, len(resp.Records))
	}
}

func TestHandler_Clear_RequiresConfirm(t *testing.T) {
	h, rb, _ := newTestHandler(t)
	_ = rb.WriteRequestLog(context.Background(), "k", "p")

	// Missing confirm
	w := doReq(h, "POST", "/internal/telemetry/fallback-buffer/clear", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("no-confirm status = %d, want 400", w.Code)
	}
	if rb.Stats().Size != 1 {
		t.Errorf("after no-confirm size = %d, want 1 (unchanged)", rb.Stats().Size)
	}

	// Wrong method
	w = doReq(h, "GET", "/internal/telemetry/fallback-buffer/clear", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET clear status = %d, want 405", w.Code)
	}

	// Confirm=true
	w = doReq(h, "POST", "/internal/telemetry/fallback-buffer/clear", `{"confirm":true}`)
	if w.Code != http.StatusOK {
		t.Errorf("confirm status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if rb.Stats().Size != 0 {
		t.Errorf("after clear size = %d, want 0", rb.Stats().Size)
	}
}

func TestHandler_Replay_All(t *testing.T) {
	h, rb, replayed := newTestHandler(t)
	for i := 0; i < 5; i++ {
		_ = rb.WriteRequestLog(context.Background(), "k", "p")
	}
	w := doReq(h, "POST", "/internal/telemetry/fallback-buffer/replay", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Replayed int `json:"replayed"`
		Failed   int `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Replayed != 5 || resp.Failed != 0 {
		t.Errorf("replayed=%d failed=%d, want 5/0", resp.Replayed, resp.Failed)
	}
	if len(*replayed) != 5 {
		t.Errorf("replayed slice len = %d, want 5", len(*replayed))
	}
	if rb.Stats().Size != 0 {
		t.Errorf("after replay size = %d, want 0", rb.Stats().Size)
	}
}

func TestHandler_Replay_Limit(t *testing.T) {
	h, rb, replayed := newTestHandler(t)
	for i := 0; i < 10; i++ {
		_ = rb.WriteRequestLog(context.Background(), "k", "p")
	}
	w := doReq(h, "POST", "/internal/telemetry/fallback-buffer/replay", `{"limit":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Replayed int `json:"replayed"`
		Failed   int `json:"failed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Replayed != 3 {
		t.Errorf("replayed = %d, want 3 (limited)", resp.Replayed)
	}
	if len(*replayed) != 3 {
		t.Errorf("replayed slice len = %d, want 3", len(*replayed))
	}
	if rb.Stats().Size != 7 {
		t.Errorf("after limited replay size = %d, want 7", rb.Stats().Size)
	}
}

func TestHandler_Replay_FailureCounts(t *testing.T) {
	rb := dbdegradation.NewRingBuffer(5)
	for i := 0; i < 5; i++ {
		_ = rb.WriteRequestLog(context.Background(), "k", "p")
	}
	// Custom replay that fails on the 2nd record.
	var calls int
	h := NewTelemetryFallbackBufferHandler(rb, func(ctx context.Context, rec dbdegradation.BackupRecord) error {
		calls++
		if calls == 2 {
			return errors.New("boom")
		}
		return nil
	})
	w := doReq(h, "POST", "/internal/telemetry/fallback-buffer/replay", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Replayed int `json:"replayed"`
		Failed   int `json:"failed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Replayed != 4 || resp.Failed != 1 {
		t.Errorf("replayed=%d failed=%d, want 4/1", resp.Replayed, resp.Failed)
	}
	if !strings.Contains(w.Body.String(), `"failed":1`) {
		t.Errorf("response missing failed count: %s", w.Body.String())
	}
}

func TestHandler_UnknownPath_Returns404(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := doReq(h, "GET", "/internal/telemetry/fallback-buffer/unknown", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", w.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h, _, _ := newTestHandler(t)
	// POST on dump is wrong method
	w := doReq(h, "POST", "/internal/telemetry/fallback-buffer/dump", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST dump status = %d, want 405", w.Code)
	}
	// GET on replay is wrong method
	w = doReq(h, "GET", "/internal/telemetry/fallback-buffer/replay", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET replay status = %d, want 405", w.Code)
	}
}
