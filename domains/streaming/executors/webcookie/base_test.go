package webcookie

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// stubAdapter is a minimal Adapter for Registry/SessionManager tests.
type stubAdapter struct{ code string }

func (a *stubAdapter) ProviderCode() string { return a.code }
func (a *stubAdapter) Login(_ context.Context, _ string, _ map[string]string) (*Session, error) {
	return nil, errors.New("stub: not implemented")
}
func (a *stubAdapter) RefreshSession(_ context.Context, s *Session) (*Session, error) {
	return s, nil
}
func (a *stubAdapter) Chat(_ context.Context, _ *Session, _ provider.Candidate, _ []byte, _ bool) (*http.Response, error) {
	return nil, errors.New("stub: not implemented")
}
func (a *stubAdapter) ParseStream(_ http.ResponseWriter, _ *http.Response) error {
	return errors.New("stub: not implemented")
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	if a := r.Get("nope"); a != nil {
		t.Fatal("empty registry should return nil for unknown code")
	}
	a1 := &stubAdapter{code: "alpha"}
	a2 := &stubAdapter{code: "beta"}
	r.Register(a1)
	r.Register(a2)
	if r.Get("alpha") != a1 {
		t.Fatal("Get(alpha) did not return registered adapter")
	}
	if r.Get("beta") != a2 {
		t.Fatal("Get(beta) did not return registered adapter")
	}
	codes := r.Codes()
	if len(codes) != 2 {
		t.Fatalf("Codes() = %v, want 2 entries", codes)
	}
	if codes[0] != "alpha" || codes[1] != "beta" {
		t.Fatalf("Codes() = %v, want sorted [alpha beta]", codes)
	}
}

func TestRegistry_RegisterReplaces(t *testing.T) {
	r := NewRegistry()
	old := &stubAdapter{code: "gamma"}
	r.Register(old)
	newA := &stubAdapter{code: "gamma"}
	r.Register(newA)
	if r.Get("gamma") != newA {
		t.Fatal("re-registering a code should replace the prior adapter")
	}
	if len(r.Codes()) != 1 {
		t.Fatalf("after replace, Codes() = %v, want 1 entry", r.Codes())
	}
}

func TestSessionManager_PutGetExpiry(t *testing.T) {
	m := NewSessionManager()
	s := &Session{
		ProviderCode: "deepseek-web",
		AccountLabel: "acct1",
		Cookies:      []*http.Cookie{{Name: "token", Value: "abc"}},
	}
	m.Put(s)
	got := m.Get("deepseek-web", "acct1")
	if got != s {
		t.Fatal("Get after Put returned a different session")
	}
	if m.Get("deepseek-web", "nonexistent") != nil {
		t.Fatal("Get for nonexistent account should return nil")
	}
}

func TestSessionManager_ExpiryEviction(t *testing.T) {
	m := NewSessionManager()
	expired := &Session{
		ProviderCode: "x-web",
		AccountLabel: "acct",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // already expired
	}
	m.Put(expired)
	if m.Get("x-web", "acct") != nil {
		t.Fatal("expired session should be evicted on Get")
	}
}

func TestSessionManager_NilPutNoop(t *testing.T) {
	m := NewSessionManager()
	m.Put(nil) // must not panic
}

func TestSessionClient_DefaultTransportForNilFactory(t *testing.T) {
	s := &Session{
		ProviderCode: "deepseek-web",
		AccountLabel: "acct",
		Cookies:      []*http.Cookie{{Name: "token", Value: "abc"}},
	}
	client := s.Client(nil)
	if client == nil {
		t.Fatal("Client(nil) returned nil")
	}
	if client.Transport == nil {
		t.Fatal("Client(nil) should use a default transport")
	}
	if got := client.Jar.Cookies(&url.URL{Scheme: "https", Host: "chat.deepseek.com"}); len(got) != 1 || got[0].Name != "token" {
		t.Fatalf("cookie jar not initialized for provider scope: %#v", got)
	}
}

func TestSessionClient_NilReturningFactoryUsesDefaultTransport(t *testing.T) {
	s := &Session{ProviderCode: "deepseek-web", AccountLabel: "acct"}
	client := s.Client(func() http.RoundTripper { return nil })
	if client == nil || client.Transport == nil {
		t.Fatalf("nil-returning factory should fall back to default transport: %#v", client)
	}
}

func TestHostForProvider(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"deepseek-web", "chat.deepseek.com"},
		{"chatgpt-web", "chatgpt.com"},
		{"gemini-web", "gemini.google.com"},
		{"claude-web", "claude.ai"},
		{"unknown-web", ""}, // unknown providers must define an explicit cookie scope
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			if got := hostForProvider(tt.code); got != tt.want {
				t.Errorf("hostForProvider(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

// TestDeepSeekWebAdapter_SkeletonNotImplemented verifies the skeleton adapter
// is registered and returns ErrNotImplemented (so the framework reports
// "adapter exists, pending capture" rather than "no adapter").
func TestDeepSeekWebAdapter_SkeletonNotImplemented(t *testing.T) {
	a := DefaultRegistry.Get("deepseek-web")
	if a == nil {
		t.Fatal("deepseek-web adapter not registered in DefaultRegistry")
	}
	if a.ProviderCode() != "deepseek-web" {
		t.Errorf("ProviderCode() = %q, want deepseek-web", a.ProviderCode())
	}
	if _, err := a.Login(context.Background(), "x", nil); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Login on skeleton should return ErrNotImplemented, got %v", err)
	}
	if _, err := a.Chat(context.Background(), &Session{}, provider.Candidate{}, nil, false); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Chat on skeleton should return ErrNotImplemented, got %v", err)
	}
	if err := a.ParseStream(httptest.NewRecorder(), nil); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ParseStream on skeleton should return ErrNotImplemented, got %v", err)
	}
}
