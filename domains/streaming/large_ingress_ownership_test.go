package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessionid"
)

type largeIngressOwnerStore struct {
	*stubSessionGetter
	ensureErr error
}

func (s *largeIngressOwnerStore) EnsureV2WithID(_ context.Context, id string, keyID int, tenant, _, _ string) (*session.Session, bool, error) {
	if s.ensureErr != nil {
		return nil, false, s.ensureErr
	}
	if si, ok := s.got[id]; ok {
		return si, false, nil
	}
	si := &session.Session{SessionID: id, APIKeyID: keyID, TenantID: tenant}
	if s.got == nil {
		s.got = make(map[string]*session.Session)
	}
	s.got[id] = si
	return si, true, nil
}

func (s *largeIngressOwnerStore) BindAPIKey(_ context.Context, id string, keyID int, tenant string) error {
	si := s.got[id]
	if si == nil || si.APIKeyID != 0 || si.TenantID != "" && si.TenantID != tenant {
		return errors.New("orphan cannot be adopted")
	}
	si.APIKeyID, si.TenantID = keyID, tenant
	return nil
}

func TestLargeIngressChecksOwnershipBeforeBindingArchive(t *testing.T) {
	for _, tc := range []struct {
		name, header, bodyID, ownerTenant string
		ownerKey, status                  int
	}{
		{"header foreign key", "gw_victim", "", "tenant-1", 42, 403},
		{"body foreign key", "gw_attacker", "gw_victim", "tenant-1", 42, 403},
		{"foreign tenant same key", "gw_victim", "", "tenant-2", 99, 403},
		{"foreign orphan", "gw_victim", "", "tenant-2", 0, 403},
		{"same tenant orphan", "gw_victim", "", "tenant-1", 0, 200},
		{"unscoped orphan", "gw_victim", "", "", 0, 200},
		{"owned body overrides header", "gw_other", "gw_victim", "tenant-1", 99, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := largebody.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			getter := &largeIngressOwnerStore{stubSessionGetter: &stubSessionGetter{got: map[string]*session.Session{
				"gw_victim": {SessionID: "gw_victim", APIKeyID: tc.ownerKey, TenantID: tc.ownerTenant},
			}}}
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
			h.sessionGetter = getter
			body := map[string]any{"model": "m", "messages": []any{map[string]string{"role": "user", "content": "owner archive proof"}}}
			if tc.bodyID != "" {
				body["session_id"] = tc.bodyID
			}
			encoded, _ := json.Marshal(body)
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				ref, ok := LargeRequestArchive(r.Context())
				if !ok || ref.SessionID != "gw_victim" {
					t.Fatalf("wrong final archive: %+v", ref)
				}
				if _, err := store.Lookup(r.Context(), ref.Identity); err != nil {
					t.Fatal(err)
				}
				w.WriteHeader(200)
			})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(encoded)))
			r.Header.Set("Authorization", "Bearer test")
			r.Header.Set("X-Gw-Session-Id", tc.header)
			w := httptest.NewRecorder()
			NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(next).ServeHTTP(w, r)
			if w.Code != tc.status || called != (tc.status == 200) {
				t.Fatalf("status=%d next=%v: %s", w.Code, called, w.Body.String())
			}
			if tc.status == 403 {
				assertStagedLargeArchive(t, store, w, string(encoded))
			}
		})
	}
}

func assertStagedLargeArchive(t *testing.T, store *largebody.Store, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	requestID := w.Header().Get("X-Gw-Archive-Id")
	if requestID == "" {
		t.Fatal("rejected request lost its recovery reference")
	}
	var ref largebody.Ref
	err := filepath.WalkDir(store.RootPath(), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == "ref.json" && filepath.Base(filepath.Dir(path)) == requestID {
			b, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(b, &ref) != nil {
				t.Fatalf("invalid staged record: %v", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.RequestID != requestID || ref.SessionID == "gw_victim" || ref.SessionID == "gw_attacker" {
		t.Fatalf("rejection wrote into client session: %+v", ref)
	}
	got, err := store.ReadRange(context.Background(), ref, largebody.Span{Length: ref.Size}, 4096)
	if err != nil || string(got) != want {
		t.Fatalf("staging recovery failed: %v", err)
	}
}

func TestLargeIngressOwnershipStoreFailureRetainsStaging(t *testing.T) {
	for _, claim := range []bool{false, true} {
		t.Run(map[bool]string{false: "lookup", true: "claim"}[claim], func(t *testing.T) {
			store, err := largebody.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			getter := &largeIngressOwnerStore{stubSessionGetter: &stubSessionGetter{}}
			if claim {
				getter.ensureErr = errors.New("store offline")
			} else {
				getter.getErr = errors.New("store offline")
			}
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
			h.sessionGetter = getter
			body := `{"session_id":"gw_victim","model":"m","messages":[{"role":"user","content":"hello"}]}`
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer test")
			w := httptest.NewRecorder()
			NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("store failure forwarded") })).ServeHTTP(w, r)
			if w.Code != 503 {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			assertStagedLargeArchive(t, store, w, body)
		})
	}
}

func TestLargeIngressFallbackRewritesBodyAndHeaderTogether(t *testing.T) {
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
	h.sessionGetter = &stubSessionGetter{}
	body := `{"gwSessionId":"gw_unclaimed","session_id":"gw_unclaimed","messages":[{"role":"user","content":"hello"}]}`
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ref, _ := LargeRequestArchive(r.Context())
		sid, _ := sessionid.Resolve(r.Header, b)
		if sid == "gw_unclaimed" || sid != ref.SessionID || sid != r.Header.Get("X-Gw-Session-Id") {
			t.Fatalf("fallback diverged: body=%s ref=%+v header=%s", b, ref, r.Header.Get("X-Gw-Session-Id"))
		}
		_, si, err := normalizeAndRegisterClientSession(r, sid, h.sessionGetter, &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"})
		if err != nil || si == nil || si.SessionID != sid {
			t.Fatalf("fallback not stable at inner owner gate: %+v %v", si, err)
		}
		w.WriteHeader(200)
	})).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
}

func TestLargeIngressNoClientSessionPreservesRecentAssignment(t *testing.T) {
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
	h.sessionGetter = &largeIngressOwnerStore{stubSessionGetter: &stubSessionGetter{got: map[string]*session.Session{
		"gw_recent": {SessionID: "gw_recent", APIKeyID: 99, TenantID: "tenant-1"},
	}}}
	index, _ := newTestLastSystemSessionIndex(t)
	h.SetSessionRouting(index, nil)
	if err := index.Set(context.Background(), 99, &session.LastSystemSessionEntry{SessionID: "gw_recent", DeviceSeed: "device"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"task"},{"role":"assistant","content":"history"},{"role":"user","content":"continue"}]}`))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("X-Device-Seed", "device")
	w := httptest.NewRecorder()
	NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref, _ := LargeRequestArchive(r.Context())
		if ref.SessionID != "gw_recent" || r.Header.Get("X-Gw-Session-Id") != "gw_recent" {
			t.Fatalf("staging id shadowed recent assignment: %+v", ref)
		}
		if _, err := store.Lookup(r.Context(), ref.Identity); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(200)
	})).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
}

func TestLargeIngressWithoutOwnershipAuthorityUsesFreshSession(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key, getter bool
	}{
		{"no getter", true, false}, {"no key authority", false, true}, {"local mode", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := largebody.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			if tc.key {
				h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
			}
			if tc.getter {
				h.sessionGetter = &stubSessionGetter{got: map[string]*session.Session{"gw_victim": {SessionID: "gw_victim", APIKeyID: 42, TenantID: "tenant-1"}}}
			}
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"session_id":"gw_victim","messages":[{"role":"user","content":"hello"}]}`))
			r.Header.Set("Authorization", "Bearer test")
			r.Header.Set("X-Gw-Session-Id", "gw_victim")
			w := httptest.NewRecorder()
			NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				sid, _ := sessionid.Resolve(r.Header, body)
				ref, _ := LargeRequestArchive(r.Context())
				if sid == "gw_victim" || sid != ref.SessionID || sid != r.Header.Get("X-Gw-Session-Id") {
					t.Fatalf("unverified client identity bound: body=%s ref=%+v", body, ref)
				}
				if _, err := store.Lookup(r.Context(), ref.Identity); err != nil {
					t.Fatal(err)
				}
				w.WriteHeader(200)
			})).ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestLargeIngressCanonicalizesConflictingHighestPriorityAliases(t *testing.T) {
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
	h.sessionGetter = &largeIngressOwnerStore{stubSessionGetter: &stubSessionGetter{got: map[string]*session.Session{
		"gw_owned_A": {SessionID: "gw_owned_A", APIKeyID: 99, TenantID: "tenant-1"}, "gw_owned_B": {SessionID: "gw_owned_B", APIKeyID: 99, TenantID: "tenant-1"},
	}}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"gwSessionId":"gw_owned_A","gw_session_id":"gw_owned_B","messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ref, _ := LargeRequestArchive(r.Context())
		for range 256 {
			sid, _ := sessionid.Resolve(r.Header, body)
			if sid != ref.SessionID || sid != r.Header.Get("X-Gw-Session-Id") {
				t.Fatalf("identity drift: body=%s ref=%+v", body, ref)
			}
		}
		w.WriteHeader(200)
	})).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
}
