package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

type nativeSessionGetSpy struct{ calls int }

type racingClaimGetter struct {
	mu        sync.Mutex
	arrivals  int
	barrier   chan struct{}
	owner     *session.Session
	ensureErr error
}

func (s *racingClaimGetter) Get(context.Context, string) (*session.Session, error) {
	if s.barrier != nil {
		s.mu.Lock()
		s.arrivals++
		if s.arrivals == 2 {
			close(s.barrier)
		}
		s.mu.Unlock()
		<-s.barrier
	}
	return nil, session.ErrSessionNotFound
}

func (s *racingClaimGetter) EnsureV2WithID(_ context.Context, id string, keyID int, tenant, _, _ string) (*session.Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ensureErr != nil {
		return nil, false, s.ensureErr
	}
	if s.owner == nil {
		s.owner = &session.Session{SessionID: id, APIKeyID: keyID, TenantID: tenant}
		return s.owner, true, nil
	}
	return s.owner, false, nil
}

func (*racingClaimGetter) Touch(context.Context, string) error { return nil }
func (*racingClaimGetter) CreateV2(context.Context, int, string, string, string) (*session.Session, error) {
	return nil, errors.New("unexpected CreateV2")
}
func (s *racingClaimGetter) BindAPIKey(_ context.Context, id string, keyID int, tenant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == nil || s.owner.SessionID != id || s.owner.APIKeyID != 0 ||
		s.owner.TenantID != "" && s.owner.TenantID != tenant {
		return errors.New("orphan already bound or tenant mismatch")
	}
	s.owner.APIKeyID = keyID
	s.owner.TenantID = tenant
	return nil
}

func TestUnknownClientSessionClaimBindsOnlyMatchingOrphan(t *testing.T) {
	for _, tc := range []struct {
		name, tenant string
		wantAllowed  bool
	}{
		{"same tenant", "tenant-1", true},
		{"empty tenant", "", true},
		{"foreign tenant", "tenant-2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getter := &racingClaimGetter{owner: &session.Session{SessionID: "gw_orphan_claim", TenantID: tc.tenant}}
			id, owned, err := normalizeAndRegisterClientSession(
				httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "gw_orphan_claim", getter,
				&authentication.KeyInfo{ID: 42, TenantID: "tenant-1"})
			if tc.wantAllowed {
				if err != nil || id != "gw_orphan_claim" || owned == nil || owned.APIKeyID != 42 || owned.TenantID != "tenant-1" {
					t.Fatalf("orphan claim failed: id=%q owned=%+v err=%v", id, owned, err)
				}
			} else if !errors.Is(err, errClientSessionForbidden) || owned != nil {
				t.Fatalf("foreign orphan was claimed: owned=%+v err=%v", owned, err)
			}
		})
	}
}

func TestUnknownClientSessionClaimRaceAcceptsOnlyAtomicWinner(t *testing.T) {
	getter := &racingClaimGetter{barrier: make(chan struct{})}
	keys := []*authentication.KeyInfo{{ID: 41, TenantID: "tenant-1"}, {ID: 42, TenantID: "tenant-2"}}
	type outcome struct {
		keyID int
		id    string
		info  *session.Session
		err   error
	}
	results := make(chan outcome, len(keys))
	for _, key := range keys {
		go func(key *authentication.KeyInfo) {
			id, info, err := normalizeAndRegisterClientSession(
				httptest.NewRequest(http.MethodPost, "/v1/messages", nil), "gw_racing", getter, key)
			results <- outcome{keyID: key.ID, id: id, info: info, err: err}
		}(key)
	}
	winners, forbidden := 0, 0
	for range keys {
		got := <-results
		switch {
		case got.err == nil && got.info != nil && got.info.APIKeyID == got.keyID && got.id == "gw_racing":
			winners++
		case errors.Is(got.err, errClientSessionForbidden) && got.info == nil:
			forbidden++
		default:
			t.Fatalf("unsafe concurrent claim result: %+v", got)
		}
	}
	if winners != 1 || forbidden != 1 {
		t.Fatalf("race winners=%d forbidden=%d", winners, forbidden)
	}
}

func TestUnknownClientSessionClaimFailureStopsAllProtocolDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		serve            func(*ChatHandler) http.Handler
	}{
		{"chat", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, func(h *ChatHandler) http.Handler { return h }},
		{"messages", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"m","input":"hi"}`, func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	} {
		for _, failure := range []string{"foreign winner", "store error"} {
			t.Run(tc.name+"/"+failure, func(t *testing.T) {
				getter := &racingClaimGetter{}
				wantStatus, wantCode := http.StatusForbidden, "session_forbidden"
				if failure == "foreign winner" {
					getter.owner = &session.Session{SessionID: "gw_racing", APIKeyID: 7, TenantID: "tenant-2"}
				} else {
					getter.ensureErr = errors.New("redis unavailable")
					wantStatus, wantCode = http.StatusServiceUnavailable, "session_unavailable"
				}
				h := NewChatHandler(nil, nil, nil, nil, nil, nil)
				if tc.name == "chat" {
					h.executor = &executors.Executor{}
					h.provider = durableEndpointResolver{}
				}
				h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}})
				h.SetSessionGetter(getter)
				var failures []*telemetry.RequestLogEntry
				h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { failures = append(failures, entry) })
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Authorization", "Bearer sk-test")
				req.Header.Set("X-Gw-Session-Id", "gw_racing")
				rec := httptest.NewRecorder()
				tc.serve(h).ServeHTTP(rec, req)
				if rec.Code != wantStatus || len(failures) != 1 || failures[0].ErrorKind == nil || *failures[0].ErrorKind != wantCode {
					t.Fatalf("claim failure dispatched or lost audit: status=%d body=%q audit=%+v", rec.Code, rec.Body.String(), failures)
				}
			})
		}
	}
}

func TestUnknownClientSessionWithoutAtomicEnsurerUsesFreshID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	id, info, err := normalizeAndRegisterClientSession(req, "gw_client_selected", &stubSessionGetter{},
		&authentication.KeyInfo{ID: 42, TenantID: "tenant-1"})
	if err != nil || info != nil || id == "gw_client_selected" || !strings.HasPrefix(id, "gw_") {
		t.Fatalf("unowned client id was trusted without atomic ensurer: id=%q info=%+v err=%v", id, info, err)
	}
}

func (s *nativeSessionGetSpy) Get(context.Context, string) (*session.Session, error) {
	s.calls++
	return nil, errClientSessionForbidden
}

func TestNativeSessionLookupRejectsOtherAPIKeyOrTenantBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		storedKey        int
		storedTenant     string
		serve            func(*ChatHandler) http.Handler
	}{
		{"messages other key", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, 7, "tenant-1", func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"messages other tenant", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, 42, "tenant-2", func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses other key", "/v1/responses", `{"model":"m","input":"hi"}`, 7, "tenant-1", func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
		{"responses other tenant", "/v1/responses", `{"model":"m","input":"hi"}`, 42, "tenant-2", func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}})
			h.SetSessionGetter(&stubSessionGetter{got: map[string]*session.Session{
				"gw_stolen": {SessionID: "gw_stolen", APIKeyID: tc.storedKey, TenantID: tc.storedTenant},
			}})
			var failures []*telemetry.RequestLogEntry
			h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { failures = append(failures, entry) })
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", "gw_stolen")
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || len(failures) != 1 ||
				failures[0].ErrorKind == nil || *failures[0].ErrorKind != "session_forbidden" {
				t.Fatalf("cross-owner session was not rejected: status=%d body=%q audit=%+v", rec.Code, rec.Body.String(), failures)
			}
		})
	}
}

func TestNativeSessionLookupBindsOnlySameTenantOrphan(t *testing.T) {
	key := &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}
	for _, tc := range []struct {
		name, tenant string
		allowed      bool
	}{
		{"same tenant orphan", "tenant-1", true},
		{"unknown tenant orphan", "", true},
		{"foreign tenant orphan", "tenant-2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getter := &stubSessionGetter{got: map[string]*session.Session{
				"gw_orphan": {SessionID: "gw_orphan", APIKeyID: 0, TenantID: tc.tenant},
			}}
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			id, info, err := normalizeAndRegisterClientSession(req, "gw_orphan", getter, key)
			if tc.allowed {
				if err != nil || id != "gw_orphan" || info == nil || info.APIKeyID != key.ID || info.TenantID != key.TenantID {
					t.Fatalf("valid orphan was not bound: id=%q info=%+v err=%v", id, info, err)
				}
			} else if err != errClientSessionForbidden || info != nil {
				t.Fatalf("foreign tenant orphan reused: info=%+v err=%v", info, err)
			}
		})
	}
	// A missing key is never a reason to query historical session state.
	spy := &nativeSessionGetSpy{}
	if _, info, err := normalizeAndRegisterClientSession(httptest.NewRequest(http.MethodPost, "/v1/messages", nil), "gw_unknown", spy, nil); info != nil || err != nil || spy.calls != 0 {
		t.Fatalf("missing key consulted session getter: info=%+v err=%v calls=%d", info, err, spy.calls)
	}
}

func TestNativeSessionLookupAcceptsCurrentKeyAndTenant(t *testing.T) {
	key := &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}
	stored := &session.Session{SessionID: "gw_owned", APIKeyID: 42, TenantID: "tenant-1"}
	getter := &stubSessionGetter{got: map[string]*session.Session{"gw_owned": stored}}
	id, info, err := normalizeAndRegisterClientSession(
		httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "gw_owned", getter, key)
	if err != nil || id != "gw_owned" || info != stored {
		t.Fatalf("current-key session was not reused: id=%q info=%+v err=%v", id, info, err)
	}
}

func TestChatSessionLookupRejectsSameKeyOtherTenant(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = &executors.Executor{}
	h.provider = durableEndpointResolver{}
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}})
	h.SetSessionGetter(&stubSessionGetter{got: map[string]*session.Session{
		"gw_stolen": {SessionID: "gw_stolen", APIKeyID: 42, TenantID: "tenant-2"},
	}})
	var failures []*telemetry.RequestLogEntry
	h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { failures = append(failures, entry) })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("X-Gw-Session-Id", "gw_stolen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(failures) != 1 ||
		failures[0].ErrorKind == nil || *failures[0].ErrorKind != "session_forbidden" {
		t.Fatalf("cross-tenant Chat session was not rejected: status=%d body=%q audit=%+v", rec.Code, rec.Body.String(), failures)
	}
}

func TestSessionLookupFailureStopsAllProtocolDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		serve            func(*ChatHandler) http.Handler
	}{
		{"chat", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, func(h *ChatHandler) http.Handler { return h }},
		{"messages", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"m","input":"hi"}`, func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			if tc.name == "chat" {
				h.executor = &executors.Executor{}
				h.provider = durableEndpointResolver{}
			}
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}})
			h.SetSessionGetter(&stubSessionGetter{getErr: errors.New("store unavailable")})
			var failures []*telemetry.RequestLogEntry
			h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { failures = append(failures, entry) })
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", "gw_known")
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable || len(failures) != 1 ||
				failures[0].ErrorKind == nil || *failures[0].ErrorKind != "session_unavailable" {
				t.Fatalf("lookup fault dispatched or lost failure audit: status=%d body=%q audit=%+v", rec.Code, rec.Body.String(), failures)
			}
		})
	}
}
