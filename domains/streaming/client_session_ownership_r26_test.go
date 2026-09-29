package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// r26SessionStore is an in-memory sessionGetter double for the
// client-bring-session ownership contract.
type r26SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session.Session
	binds    []string
}

func newR26SessionStore(seed map[string]*session.Session) *r26SessionStore {
	if seed == nil {
		seed = map[string]*session.Session{}
	}
	return &r26SessionStore{sessions: seed}
}

func (s *r26SessionStore) Get(_ context.Context, id string) (*session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if si, ok := s.sessions[id]; ok {
		return si, nil
	}
	return nil, session.ErrSessionNotFound
}

func (s *r26SessionStore) Touch(context.Context, string) error { return nil }

func (s *r26SessionStore) CreateV2(_ context.Context, apiKeyID int, tenantID, _, _ string) (*session.Session, error) {
	return &session.Session{SessionID: "gw_created", APIKeyID: apiKeyID, TenantID: tenantID}, nil
}

func (s *r26SessionStore) BindAPIKey(_ context.Context, sessionID string, apiKeyID int, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binds = append(s.binds, sessionID)
	if si, ok := s.sessions[sessionID]; ok {
		si.APIKeyID = apiKeyID
		si.TenantID = tenantID
	}
	return nil
}

func (s *r26SessionStore) bindCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.binds)
}

const r26VictimSession = "gw_r26_victim"

// R24-B (V3-A02, round 26): a client-supplied session id that belongs to
// another api key/tenant must be rejected with 403 before any turn is
// created, on both native lanes. Before the fix the foreign sessionInfo
// flowed unchecked into applyResolvedGatewaySession and logCtx.
func TestNativeClientSessionCrossKeyRejected403(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
		wrap func(*ChatHandler) http.Handler
		err  string
	}{
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			wrap: func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) },
			err:  "permission_error",
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"m","input":"hi"}`,
			wrap: func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) },
			err:  "session_forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newR26SessionStore(map[string]*session.Session{
				r26VictimSession: {SessionID: r26VictimSession, APIKeyID: 42, TenantID: "tenant-1"},
			})
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{
				ID: 99, TenantID: "tenant-2", ApplicationID: 7,
			}})
			h.provider = durableEndpointResolver{}
			h.executor = &executors.Executor{}
			h.sessionGetter = store

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", r26VictimSession)
			rec := httptest.NewRecorder()
			tc.wrap(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.err) ||
				!strings.Contains(rec.Body.String(), "session not owned by this api key") {
				t.Fatalf("body missing ownership rejection marker %q: %s", tc.err, rec.Body.String())
			}
			if store.bindCount() != 0 {
				t.Fatal("cross-key request must not bind the session to the attacker key")
			}
		})
	}
}

// Matching owner and orphan sessions keep working: the owner match is
// adopted, and an orphan (api_key_id = 0) is bound to the first claiming
// key exactly like the chat path.
func TestNativeClientSessionOwnerMatchAndOrphanBind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session *session.Session
	}{
		{"owner match", &session.Session{SessionID: r26VictimSession, APIKeyID: 42, TenantID: "tenant-1"}},
		{"orphan binds", &session.Session{SessionID: r26VictimSession, APIKeyID: 0, TenantID: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Capture before the request: BindAPIKey mutates the stored
			// session in place, and tc.session points at the same object.
			originalAPIKeyID := tc.session.APIKeyID
			store := newR26SessionStore(map[string]*session.Session{r26VictimSession: tc.session})
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{
				ID: 42, TenantID: "tenant-1", ApplicationID: 7,
			}})
			h.provider = durableEndpointResolver{}
			h.executor = &executors.Executor{}
			h.sessionGetter = store

			req := httptest.NewRequest(http.MethodPost, "/v1/messages",
				strings.NewReader(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", r26VictimSession)
			rec := httptest.NewRecorder()
			NewMessagesHandler(h).ServeHTTP(rec, req)

			// The request proceeds past session resolution; whatever the
			// executor-less downstream answers, it must NOT be a 403.
			if rec.Code == http.StatusForbidden {
				t.Fatalf("legitimate session rejected: %s", rec.Body.String())
			}
			wantBinds := 0
			if originalAPIKeyID == 0 {
				wantBinds = 1
			}
			if store.bindCount() != wantBinds {
				t.Fatalf("BindAPIKey calls = %d, want %d", store.bindCount(), wantBinds)
			}
		})
	}
}
