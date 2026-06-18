package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/pending"
)

// adminWithPendingStore builds a Handler with the given store
// pre-wired. All other fields are nil — the endpoints we test
// here only touch the pending store.
func adminWithPendingStore(store *pending.Store) *Handler {
	h := &Handler{}
	h.SetPendingStore(store)
	return h
}

// TestPendingList_NilStoreReturns503 pins the graceful-degrade
// contract: when the operator has disabled the pending cache
// (e.g. no Redis), the admin endpoints return 503 with a clear
// error code rather than 500.
func TestPendingList_NilStoreReturns503(t *testing.T) {
	h := adminWithPendingStore(nil)
	r := httptest.NewRequest("GET", "/api/admin/pending-responses", nil)
	w := httptest.NewRecorder()
	h.handlePendingList(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	if !contains(w.Body.String(), "PENDING_STORE_UNAVAILABLE") {
		t.Errorf("body: got %q", w.Body.String())
	}
}

func TestPendingDetail_NilStoreReturns503(t *testing.T) {
	h := adminWithPendingStore(nil)
	r := httptest.NewRequest("GET", "/api/admin/pending-responses/sess-x", nil)
	w := httptest.NewRecorder()
	h.handlePendingDetail(w, r, "sess-x")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

func TestPendingDelete_NilStoreReturns503(t *testing.T) {
	h := adminWithPendingStore(nil)
	r := httptest.NewRequest("DELETE", "/api/admin/pending-responses/sess-x", nil)
	w := httptest.NewRecorder()
	h.handlePendingDelete(w, r, "sess-x")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

func TestPendingStats_NilStoreReturns503(t *testing.T) {
	h := adminWithPendingStore(nil)
	r := httptest.NewRequest("GET", "/api/admin/pending-responses/stats", nil)
	w := httptest.NewRecorder()
	h.handlePendingStats(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

// TestPendingSubrouter_DispatchesByMethod pins the
// detail-vs-delete dispatch. Both methods share a single
// subrouter path; a regression that drops the switch would
// either 405 everything or 404 the detail.
func TestPendingSubrouter_DispatchesByMethod(t *testing.T) {
	store := pending.NewStore(nil, 0)
	h := adminWithPendingStore(store)
	// GET on /sess-x → handlePendingDetail. With nil store
	// inside the real Store, the call returns 503; we only
	// assert the subrouter routed to detail (not 404 / 405).
	r := httptest.NewRequest("GET", "/api/admin/pending-responses/sess-x", nil)
	w := httptest.NewRecorder()
	h.handlePendingSubrouter(w, r)
	if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
		t.Fatalf("GET on subrouter should route to detail (got %d)", w.Code)
	}
	// DELETE on /sess-x → handlePendingDelete.
	r = httptest.NewRequest("DELETE", "/api/admin/pending-responses/sess-y", nil)
	w = httptest.NewRecorder()
	h.handlePendingSubrouter(w, r)
	if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
		t.Fatalf("DELETE on subrouter should route to delete (got %d)", w.Code)
	}
	// POST on /sess-x → 405.
	r = httptest.NewRequest("POST", "/api/admin/pending-responses/sess-z", nil)
	w = httptest.NewRecorder()
	h.handlePendingSubrouter(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: got %d, want 405", w.Code)
	}
}

// TestPendingSubrouter_RejectsSubPath guards the future-proofing
// against a client trying to hit /api/admin/pending-responses/
// foo/bar. We 404 immediately rather than passing garbage into
// the detail/delete handlers.
func TestPendingSubrouter_RejectsSubPath(t *testing.T) {
	h := adminWithPendingStore(nil)
	r := httptest.NewRequest("GET", "/api/admin/pending-responses/foo/bar", nil)
	// Rewrite path: httptest's URL is "/api/admin/pending-responses/
	// foo/bar" but the TrimPrefix + IndexByte / handling must
	// catch the nested slash. We use the raw path.
	w := httptest.NewRecorder()
	h.handlePendingSubrouter(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("nested path: got %d, want 404", w.Code)
	}
}

// TestPageBounds_PinsDefaultsAndCaps: the page-bounds parser
// must clamp limit to [1..500] and offset to [0..∞). Garbage
// values fall back to safe defaults (50 / 0).
func TestPageBounds_PinsDefaultsAndCaps(t *testing.T) {
	cases := []struct {
		name                string
		query               string
		wantLimit, wantOff  int
	}{
		{"no query", "", 50, 0},
		{"limit=10", "limit=10", 10, 0},
		{"limit=1000 (cap)", "limit=1000", 500, 0}, // capped at 500
		{"limit=0 (default)", "limit=0", 50, 0},
		{"limit=-1 (default)", "limit=-1", 50, 0},
		{"limit=abc (default)", "limit=abc", 50, 0},
		{"offset=10", "offset=10", 50, 10},
		{"offset=-1 (default)", "offset=-1", 50, 0},
		{"both", "limit=20&offset=5", 20, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/admin/pending-responses"
			if tc.query != "" {
				url += "?" + tc.query
			}
			r := httptest.NewRequest("GET", url, nil)
			limit, offset := pageBounds(r)
			if limit != tc.wantLimit {
				t.Errorf("limit: got %d, want %d", limit, tc.wantLimit)
			}
			if offset != tc.wantOff {
				t.Errorf("offset: got %d, want %d", offset, tc.wantOff)
			}
		})
	}
}

// TestWriteErrorJSON_PinsShape: the admin error response
// contract. Clients key on {"error":{"message":...,"code":...}}.
func TestWriteErrorJSON_PinsShape(t *testing.T) {
	w := httptest.NewRecorder()
	writeErrorJSON(w, http.StatusBadRequest, "bad", "BAD")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code: got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object: got %T", body["error"])
	}
	if errObj["message"] != "bad" {
		t.Errorf("message: got %v", errObj["message"])
	}
	if errObj["code"] != "BAD" {
		t.Errorf("code: got %v", errObj["code"])
	}
}

// TestGetPendingStore_NilHandlerSafe pins the nil-receiver
// safety. The auth middleware may construct Handler without
// going through NewHandler; we must not panic on a missing
// store.
func TestGetPendingStore_NilHandlerSafe(t *testing.T) {
	var h *Handler
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil Handler panicked: %v", r)
		}
	}()
	if got := h.getPendingStore(); got != nil {
		t.Errorf("nil Handler should return nil store, got %v", got)
	}
}

// TestPendingStoreAdapter_NilStore pins the adapter's graceful
// contract. Mirrors the design — calling list() with a nil
// store is an error, not a panic.
func TestPendingStoreAdapter_NilStore(t *testing.T) {
	var a *pendingStoreAdapter
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil adapter panicked: %v", r)
		}
	}()
	_, err := a.list(pendingListContext{})
	if err == nil {
		t.Fatal("expected error from nil adapter")
	}
	if !errors.Is(err, errors.New("")) && err.Error() == "" {
		t.Fatalf("got err=%v", err)
	}
}

// contains is a tiny helper to keep the test file import-clean
// (avoids pulling strings just for one Contains call).
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
