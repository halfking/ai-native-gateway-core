package streaming

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

type stubSessionGetter struct {
	got            map[string]*session.Session
	created        []*session.Session
	createErr      error
	getErr         error
	lastCreateTask string
}

func (s *stubSessionGetter) Get(ctx context.Context, id string) (*session.Session, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if sess, ok := s.got[id]; ok {
		return sess, nil
	}
	return nil, session.ErrSessionNotFound
}

func (s *stubSessionGetter) Touch(ctx context.Context, id string) error { return nil }

func (s *stubSessionGetter) CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*session.Session, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.lastCreateTask = taskID
	created := &session.Session{SessionID: generateSystemSessionID(), APIKeyID: apiKeyID, TenantID: tenantID, TaskID: taskID, Namespace: "gw"}
	s.created = append(s.created, created)
	if s.got == nil {
		s.got = map[string]*session.Session{}
	}
	s.got[created.SessionID] = created
	return created, nil
}

func (s *stubSessionGetter) BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error {
	return nil
}

type stubRecentSessionFinder struct {
	sessionID string
	err       error
	called    bool
	window    time.Duration
	identity  string
}

func (s *stubRecentSessionFinder) FindRecentGatewaySession(ctx context.Context, tenantID, identityHash string, apiKeyID int, since time.Duration) (string, error) {
	s.called = true
	s.window = since
	s.identity = identityHash
	if s.err != nil {
		return "", s.err
	}
	return s.sessionID, nil
}

func TestCountRequestMessages(t *testing.T) {
	if got := countRequestMessages([]byte(`{"messages":[{"role":"user","content":"a"}]}`)); got != 1 {
		t.Fatalf("countRequestMessages() = %d, want 1", got)
	}
	if got := countRequestMessages([]byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`)); got != 2 {
		t.Fatalf("countRequestMessages() = %d, want 2", got)
	}
	if got := countRequestMessages([]byte(`{"model":"x"}`)); got != 0 {
		t.Fatalf("countRequestMessages() = %d, want 0", got)
	}
}

func TestAssignGatewaySession_CreatesForSingleMessage(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	getter := &stubSessionGetter{}
	h.SetSessionGetter(getter)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySession(context.Background(), []byte(`{"messages":[{"role":"user","content":"hello"}]}`), r, keyInfo, "", nil, "roocode")
	if err != nil {
		t.Fatalf("assignGatewaySession() error = %v", err)
	}
	if !assignment.AutoCreated {
		t.Fatal("expected AutoCreated=true")
	}
	if assignment.SessionID == "" || assignment.SessionInfo == nil {
		t.Fatal("expected created session info")
	}
	if len(getter.created) != 1 {
		t.Fatalf("created sessions = %d, want 1", len(getter.created))
	}
}

func TestAssignGatewaySession_ReusesRecentGatewaySession(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	getter := &stubSessionGetter{got: map[string]*session.Session{
		"gw_existing": {SessionID: "gw_existing", APIKeyID: 11, TenantID: "tenant-a", TaskID: "task-1", Namespace: "gw"},
	}}
	h.SetSessionGetter(getter)
	finder := &stubRecentSessionFinder{sessionID: "gw_existing"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "device-1")
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySessionWithFinder(context.Background(), []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`), r, keyInfo, "", nil, "roocode", finder)
	if err != nil {
		t.Fatalf("assignGatewaySessionWithFinder() error = %v", err)
	}
	if !assignment.Resumed || !assignment.FromRecent {
		t.Fatalf("expected recent resume, got %+v", assignment)
	}
	if assignment.SessionID != "gw_existing" {
		t.Fatalf("session_id = %q, want gw_existing", assignment.SessionID)
	}
	if len(getter.created) != 0 {
		t.Fatalf("created sessions = %d, want 0", len(getter.created))
	}
	if !finder.called {
		t.Fatal("expected finder to be called")
	}
	if finder.window != session.LastSystemSessionTTL {
		t.Fatalf("window = %v, want %v", finder.window, session.LastSystemSessionTTL)
	}
}

func TestAssignGatewaySession_CreatesWhenRecentLookupMisses(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	getter := &stubSessionGetter{}
	h.SetSessionGetter(getter)
	finder := &stubRecentSessionFinder{}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySessionWithFinder(context.Background(), []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`), r, keyInfo, "", nil, "roocode", finder)
	if err != nil {
		t.Fatalf("assignGatewaySessionWithFinder() error = %v", err)
	}
	if !assignment.AutoCreated {
		t.Fatal("expected AutoCreated=true")
	}
	if len(getter.created) != 1 {
		t.Fatalf("created sessions = %d, want 1", len(getter.created))
	}
}

func TestAssignGatewaySession_PropagatesFinderError(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	getter := &stubSessionGetter{}
	h.SetSessionGetter(getter)
	finder := &stubRecentSessionFinder{err: errors.New("db down")}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	_, err := h.assignGatewaySessionWithFinder(context.Background(), []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`), r, keyInfo, "", nil, "roocode", finder)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureSessionID_NoKeyInfoReturnsGwPrefixedID(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	got := h.ensureSessionID(context.Background(), r, nil)
	if got == "" {
		t.Fatal("ensureSessionID() returned empty when keyInfo is nil")
	}
	if !strings.HasPrefix(got, "gw_") {
		t.Fatalf("ensureSessionID() = %q, want gw_ prefix", got)
	}
}

func TestEnsureSessionID_HonorsClientHeader(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_client_provided")

	got := h.ensureSessionID(context.Background(), r, nil)
	if got != "gw_client_provided" {
		t.Fatalf("ensureSessionID() = %q, want gw_client_provided (client header)", got)
	}
}

func TestEnsureSessionID_RejectsNonGwHeader(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "not-a-gw-id")
	r.Header.Set("X-Session-Id", "also-not-gw")

	got := h.ensureSessionID(context.Background(), r, nil)
	if !strings.HasPrefix(got, "gw_") {
		t.Fatalf("ensureSessionID() = %q, want gw_ prefix (non-gw_ headers should be replaced)", got)
	}
}

func TestEnsureSessionID_NilSessionGetterFallsBackToSystemID(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	got := h.ensureSessionID(context.Background(), r, keyInfo)
	if !strings.HasPrefix(got, "gw_") {
		t.Fatalf("ensureSessionID() = %q, want gw_ prefix when sessionGetter=nil", got)
	}
}

func TestEnsureSessionID_WithKeyInfoAndGetterCallsCreateV2(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	getter := &stubSessionGetter{}
	h.SetSessionGetter(getter)
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	got := h.ensureSessionID(context.Background(), r, keyInfo)
	if got == "" {
		t.Fatal("ensureSessionID() returned empty")
	}
	if !strings.HasPrefix(got, "gw_") {
		t.Fatalf("ensureSessionID() = %q, want gw_ prefix", got)
	}
	if len(getter.created) != 1 {
		t.Fatalf("CreateV2 called %d times, want 1", len(getter.created))
	}
}

func TestAssignGatewaySession_HonorsReuseWindow(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	getter := &stubSessionGetter{got: map[string]*session.Session{
		"gw_recent": {SessionID: "gw_recent", APIKeyID: 11, TenantID: "tenant-a", TaskID: "task-1", Namespace: "gw"},
	}}
	h.SetSessionGetter(getter)
	h.SetSessionReuseWindow(123 * time.Second)
	finder := &stubRecentSessionFinder{sessionID: "gw_recent"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySessionWithFinder(context.Background(), []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`), r, keyInfo, "", nil, "roocode", finder)
	if err != nil {
		t.Fatalf("assignGatewaySessionWithFinder() error = %v", err)
	}
	if !assignment.FromRecent {
		t.Fatal("expected FromRecent=true")
	}
	if finder.window != 123*time.Second {
		t.Fatalf("finder.window = %v, want 123s (configurable)", finder.window)
	}
}

func TestParseSessionReuseWindow(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "empty defaults to 5 minutes", raw: "", want: 5 * time.Minute},
		{name: "10m parses", raw: "10m", want: 10 * time.Minute},
		{name: "30s parses", raw: "30s", want: 30 * time.Second},
		{name: "1h parses", raw: "1h", want: 1 * time.Hour},
		{name: "garbage falls back to 5m", raw: "garbage", want: 5 * time.Minute},
		{name: "negative falls back to 5m", raw: "-1m", want: 5 * time.Minute},
		{name: "zero falls back to 5m", raw: "0s", want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_SESSION_REUSE_WINDOW", tt.raw)
			got := parseSessionReuseWindow()
			if got != tt.want {
				t.Fatalf("parseSessionReuseWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}
