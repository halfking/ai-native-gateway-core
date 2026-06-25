package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubVerifier struct {
	enabled bool
	verify  func(ctx context.Context, rawKey string) (KeyInfo, error)
}

func (s *stubVerifier) Enabled() bool { return s.enabled }
func (s *stubVerifier) Verify(ctx context.Context, rawKey string) (KeyInfo, error) {
	return s.verify(ctx, rawKey)
}

func TestNewHandler(t *testing.T) {
	mgr := &Manager{}
	h := NewHandler(mgr)
	if h == nil || h.manager != mgr {
		t.Fatal("NewHandler did not set manager")
	}
}

func TestHandler_SetAuth(t *testing.T) {
	h := NewHandler(nil)
	kv := &stubVerifier{enabled: true}
	h.SetAuth(kv)
	if h.keyVerifier != kv {
		t.Fatal("SetAuth did not install verifier")
	}
}

func TestHandler_SetPendingStore(t *testing.T) {
	h := NewHandler(nil)
	// 调用 SetPendingStore 确保方法可达
	_ = h.SetPendingStore
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		auth   string
		apiKey string
		want   string
	}{
		{"empty", "", "", ""},
		{"bearer upper", "Bearer sk-abc", "", "sk-abc"},
		{"bearer lower", "bearer sk-abc", "", "sk-abc"},
		{"x-api-key", "", "sk-xyz", "sk-xyz"},
		{"auth wins", "Bearer sk-a", "sk-b", "sk-a"},
		{"other scheme", "Basic abc", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "/", nil)
			if tt.auth != "" {
				r.Header.Set("Authorization", tt.auth)
			}
			if tt.apiKey != "" {
				r.Header.Set("x-api-key", tt.apiKey)
			}
			if got := extractBearerToken(r); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandler_authenticate_Disabled(t *testing.T) {
	h := NewHandler(nil)
	h.SetAuth(&stubVerifier{enabled: false})
	r, _ := http.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	ctx, ok := h.authenticate(w, r)
	if !ok {
		t.Fatal("disabled verifier should allow through")
	}
	if ctx == nil {
		t.Fatal("ctx should not be nil")
	}
}

func TestHandler_authenticate_MissingKey(t *testing.T) {
	h := NewHandler(nil)
	h.SetAuth(&stubVerifier{enabled: true})
	r, _ := http.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	_, ok := h.authenticate(w, r)
	if ok {
		t.Fatal("missing key should be rejected")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandler_authenticate_InvalidKey(t *testing.T) {
	h := NewHandler(nil)
	h.SetAuth(&stubVerifier{
		enabled: true,
		verify: func(ctx context.Context, rawKey string) (KeyInfo, error) {
			return KeyInfo{}, &InvalidKeyError{Message: "bad"}
		},
	})
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer sk-x")
	w := httptest.NewRecorder()
	_, ok := h.authenticate(w, r)
	if ok {
		t.Fatal("invalid key should be rejected")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandler_authenticate_OtherError(t *testing.T) {
	h := NewHandler(nil)
	h.SetAuth(&stubVerifier{
		enabled: true,
		verify: func(ctx context.Context, rawKey string) (KeyInfo, error) {
			return KeyInfo{}, errors.New("redis down")
		},
	})
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer sk-x")
	w := httptest.NewRecorder()
	_, ok := h.authenticate(w, r)
	if ok {
		t.Fatal("non-InvalidKey error should be rejected")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandler_authenticate_Success(t *testing.T) {
	h := NewHandler(nil)
	h.SetAuth(&stubVerifier{
		enabled: true,
		verify: func(ctx context.Context, rawKey string) (KeyInfo, error) {
			return KeyInfo{ID: 42, TenantID: "tenant-z"}, nil
		},
	})
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer sk-x")
	w := httptest.NewRecorder()
	ctx, ok := h.authenticate(w, r)
	if !ok {
		t.Fatal("valid key should pass")
	}
	if GetAPIKeyIDFromContext(ctx) != 42 {
		t.Fatalf("api key id = %d, want 42", GetAPIKeyIDFromContext(ctx))
	}
	if GetTenantIDFromContext(ctx) != "tenant-z" {
		t.Fatalf("tenant id = %q, want tenant-z", GetTenantIDFromContext(ctx))
	}
}

func TestHandler_ServeHTTP_UnknownPath(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("GET", "/v1/unknown", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandler_ServeHTTP_MethodNotAllowed(t *testing.T) {
	h := NewHandler(nil)
	// /v1/sessions/{id} with PUT → 405 from default switch case
	r, _ := http.NewRequest("PUT", "/v1/sessions/abc", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandler_getPendingResponse_NilStore(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandler_getPendingResponse_NotFound(t *testing.T) {
	h := NewHandler(nil) // nil manager
	h.SetPendingStore(&alwaysMissStore{})
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandler_getPendingResponse_InProgress(t *testing.T) {
	h := NewHandler(nil)
	h.SetPendingStore(&fixedStore{entry: &PendingEntry{
		SessionID: "s1", RequestID: "r1", Status: "in_progress",
	}})
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "in_progress" {
		t.Fatalf("status field = %v, want in_progress", body["status"])
	}
}

func TestHandler_getPendingResponse_Failed(t *testing.T) {
	h := NewHandler(nil)
	h.SetPendingStore(&fixedStore{entry: &PendingEntry{
		SessionID: "s1", RequestID: "r1", Status: "failed", ErrorMessage: "boom",
	}})
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHandler_getPendingResponse_Completed(t *testing.T) {
	h := NewHandler(nil)
	h.SetPendingStore(&fixedStore{entry: &PendingEntry{
		SessionID: "s1", RequestID: "r1", Status: "completed", Body: "data: {}\n\n", ContentType: "text/event-stream",
	}})
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("X-Gw-Pending-Replay") != "true" {
		t.Fatal("X-Gw-Pending-Replay header missing")
	}
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	if w.Body.String() != "data: {}\n\n" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestHandler_getPendingResponse_CompletedEmptyBody(t *testing.T) {
	h := NewHandler(nil)
	h.SetPendingStore(&fixedStore{entry: &PendingEntry{
		SessionID: "s1", RequestID: "r1", Status: "completed", Body: "",
	}})
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandler_getPendingResponse_UnknownStatus(t *testing.T) {
	h := NewHandler(nil)
	h.SetPendingStore(&fixedStore{entry: &PendingEntry{
		SessionID: "s1", RequestID: "r1", Status: "weird",
	}})
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandler_getPendingResponse_StoreError(t *testing.T) {
	h := NewHandler(nil)
	h.SetPendingStore(&errorStore{err: errors.New("boom")})
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandler_getPendingResponse_TenantMismatch(t *testing.T) {
	h := NewHandler(nil)
	h.SetPendingStore(&fixedStore{entry: &PendingEntry{
		SessionID: "s1", RequestID: "r1", Status: "completed", Body: "x", TenantID: "tenant-other",
	}})
	// 注入 tenant
	r, _ := http.NewRequest("GET", "/v1/sessions/s1/pending-response", nil)
	r = r.WithContext(SetTenantID(r.Context(), "tenant-A"))
	w := httptest.NewRecorder()
	h.getPendingResponse(w, r, "s1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (tenant isolation)", w.Code)
	}
}

func TestHandler_ServeHTTP_PendingResponseBadMethod(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("POST", "/v1/sessions/s1/pending-response", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandler_ServeHTTP_EmptySessionID(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("GET", "/v1/sessions/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandler_ServeHTTP_MissingIDInPending(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("GET", "/v1/sessions//pending-response", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandler_ServeHTTP_MigrateBadMethod(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("PUT", "/v1/sessions/migrate", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// --- Stubs ---

type alwaysMissStore struct{}

func (alwaysMissStore) Get(ctx context.Context, sid, rid string) (*PendingEntry, bool, error) {
	return nil, false, nil
}
func (alwaysMissStore) GetLatest(ctx context.Context, sid string) (*PendingEntry, string, bool, error) {
	return nil, "", false, nil
}

type fixedStore struct {
	entry *PendingEntry
}

func (f *fixedStore) Get(ctx context.Context, sid, rid string) (*PendingEntry, bool, error) {
	return f.entry, true, nil
}
func (f *fixedStore) GetLatest(ctx context.Context, sid string) (*PendingEntry, string, bool, error) {
	return f.entry, "r1", true, nil
}

type errorStore struct{ err error }

func (e *errorStore) Get(ctx context.Context, sid, rid string) (*PendingEntry, bool, error) {
	return nil, false, e.err
}
func (e *errorStore) GetLatest(ctx context.Context, sid string) (*PendingEntry, string, bool, error) {
	return nil, "", false, e.err
}

func TestHandler_CreateSession_InvalidMethod(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("PUT", "/v1/sessions", nil)
	w := httptest.NewRecorder()
	h.CreateSession(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_GetSessionByID_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	h := NewHandler(mgr)
	r, _ := http.NewRequest("GET", "/v1/sessions/nonexistent", nil)
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.GetSessionByID(w, r, "nonexistent")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_GetSessionByID_Expired(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	// 手动把 expires_at 改成过去
	_ = mgr.redis.HSet(ctx, "session:"+sess.SessionID, map[string]any{
		"expires_at": "2020-01-01T00:00:00Z",
	})
	h := NewHandler(mgr)
	r, _ := http.NewRequest("GET", "/v1/sessions/"+sess.SessionID, nil)
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.GetSessionByID(w, r, sess.SessionID)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", w.Code)
	}
}

func TestHandler_GetSessionByID_Forbidden(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 99, "t", "d")
	h := NewHandler(mgr)
	r, _ := http.NewRequest("GET", "/v1/sessions/"+sess.SessionID, nil)
	// 调用方 apiKeyID=1，与 session.APIKeyID=99 不一致
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.GetSessionByID(w, r, sess.SessionID)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandler_GetSessionByID_OK(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	h := NewHandler(mgr)
	r, _ := http.NewRequest("GET", "/v1/sessions/"+sess.SessionID, nil)
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.GetSessionByID(w, r, sess.SessionID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestHandler_DeleteSessionByID_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	h := NewHandler(mgr)
	r, _ := http.NewRequest("DELETE", "/v1/sessions/nope", nil)
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.DeleteSessionByID(w, r, "nope")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_DeleteSessionByID_OK(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	h := NewHandler(mgr)
	r, _ := http.NewRequest("DELETE", "/v1/sessions/"+sess.SessionID, nil)
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.DeleteSessionByID(w, r, sess.SessionID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestHandler_DeleteSessionByID_Forbidden(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 99, "t", "d")
	h := NewHandler(mgr)
	r, _ := http.NewRequest("DELETE", "/v1/sessions/"+sess.SessionID, nil)
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.DeleteSessionByID(w, r, sess.SessionID)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandler_MigrateSession_BadBody(t *testing.T) {
	h := NewHandler(nil)
	body := strings.NewReader("not json")
	r, _ := http.NewRequest("POST", "/v1/sessions/migrate", body)
	w := httptest.NewRecorder()
	h.MigrateSession(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_MigrateSession_MissingSessionID(t *testing.T) {
	h := NewHandler(nil)
	body := strings.NewReader(`{}`)
	r, _ := http.NewRequest("POST", "/v1/sessions/migrate", body)
	w := httptest.NewRecorder()
	h.MigrateSession(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_MigrateSession_MissingDeviceSeed(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	h := NewHandler(mgr)
	body := strings.NewReader(`{"session_id":"` + sess.SessionID + `"}`)
	r, _ := http.NewRequest("POST", "/v1/sessions/migrate", body)
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.MigrateSession(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_MigrateSession_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	h := NewHandler(mgr)
	body := strings.NewReader(`{"session_id":"nope"}`)
	r, _ := http.NewRequest("POST", "/v1/sessions/migrate", body)
	r.Header.Set("X-Device-Seed", "dev1")
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.MigrateSession(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_MigrateSession_Forbidden(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 99, "t", "d")
	h := NewHandler(mgr)
	body := strings.NewReader(`{"session_id":"` + sess.SessionID + `"}`)
	r, _ := http.NewRequest("POST", "/v1/sessions/migrate", body)
	r.Header.Set("X-Device-Seed", "dev1")
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.MigrateSession(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_MigrateSession_OK(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d-1")
	h := NewHandler(mgr)
	body := strings.NewReader(`{"session_id":"` + sess.SessionID + `"}`)
	r, _ := http.NewRequest("POST", "/v1/sessions/migrate", body)
	r.Header.Set("X-Device-Seed", "d-2")
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	w := httptest.NewRecorder()
	h.MigrateSession(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestHandler_CreateSession_InvalidMethodDirectCall(t *testing.T) {
	h := NewHandler(nil)
	r, _ := http.NewRequest("GET", "/v1/sessions", nil)
	w := httptest.NewRecorder()
	h.CreateSession(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandler_CreateSession_DeviceSeedFromMachineID(t *testing.T) {
	mgr, _ := newTestManager(t)
	h := NewHandler(mgr)
	body := strings.NewReader("{}")
	r, _ := http.NewRequest("POST", "/v1/sessions", body)
	r.Header.Set("X-Machine-Id", "mach-x")
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	r = r.WithContext(SetTenantID(r.Context(), "t"))
	w := httptest.NewRecorder()
	h.CreateSession(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
}
