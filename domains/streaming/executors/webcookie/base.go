// Package webcookie provides the framework for browser-reverse-engineered
// ("web-cookie") free LLM providers — e.g. deepseek-web, chatgpt-web,
// gemini-web. These providers expose a free chat UI in the browser but no
// official free API; OmniRoute wraps them by reverse-engineering the browser's
// HTTP/WS/SockJS calls with cookie/session injection and TLS fingerprinting.
//
// This package is the Go analogue of OmniRoute's open-sse/executors/*-web.ts
// adapters. Because each site's protocol must be captured individually (and
// maintained against anti-bot changes), this package provides only the shared
// framework; concrete adapters are implemented per-provider.
//
// Status (2026-08-10): framework + deepseek-web skeleton. The other 27
// web-cookie providers in free_resource_catalog are present as catalog rows
// (enabled=false) and documented in docs/omnifree/web-cookie-providers.md as
// the implementation backlog.
package webcookie

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// Session holds an authenticated browser session for a web-cookie provider:
// the cookies, refresh timestamp, and a lazily-initialized http.Client with a
// cookie jar. One Session per (provider, account, tenant).
type Session struct {
	ProviderCode string
	AccountLabel string
	// TenantID scopes the session to a tenant. SessionManager keys include this
	// field so two tenants sharing the same providerCode+accountLabel never
	// accidentally share cookies (C1, audit round 2). Required on Put; Get
	// returns nil when the caller supplies a different tenant.
	TenantID    string
	Cookies     []*http.Cookie
	RefreshedAt time.Time
	ExpiresAt   time.Time // zero = no expiry

	mu     sync.Mutex
	client *http.Client
}

// Client returns an http.Client bound to this session's cookie jar. The client
// is reused across requests; transport customization (e.g. uTLS fingerprinting)
// is applied by the concrete adapter via TransportFactory.
func (s *Session) Client(transportFactory func() http.RoundTripper) *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client
	}
	jar, _ := cookiejar.New(nil) // nil PublicSuffixList is fine for controlled clients
	if len(s.Cookies) > 0 {
		// cookiejar needs a URL scope; use the known https URL per provider.
		if scope := s.scopeURL(); scope.Host != "" {
			jar.SetCookies(scope, s.Cookies)
		}
	}
	transport := http.RoundTripper(http.DefaultTransport)
	if transportFactory != nil {
		if rt := transportFactory(); rt != nil {
			transport = rt
		}
	}
	c := &http.Client{
		Jar:       jar,
		Timeout:   120 * time.Second,
		Transport: transport,
	}
	s.client = c
	return c
}

func (s *Session) scopeURL() *url.URL {
	return &url.URL{Scheme: "https", Host: hostForProvider(s.ProviderCode)}
}

// hostForProvider maps a web-cookie provider code to its browser domain. This
// is the minimal mapping; concrete adapters own their full URL construction.
func hostForProvider(code string) string {
	switch code {
	case "deepseek-web":
		return "chat.deepseek.com"
	case "chatgpt-web":
		return "chatgpt.com"
	case "gemini-web":
		return "gemini.google.com"
	case "claude-web":
		return "claude.ai"
	case "grok-web":
		return "grok.com"
	case "copilot-web":
		return "copilot.microsoft.com"
	case "kimi-web":
		return "kimi.moonshot.cn"
	case "doubao-web":
		return "www.doubao.com"
	case "qwen-web":
		return "chat.qwen.ai"
	case "yuanbao-web":
		return "yuanbao.tencent.com"
	case "perplexity-web":
		return "www.perplexity.ai"
	case "huggingchat":
		return "huggingface.co"
	case "lmarena":
		return "lmarena.ai"
	case "poe-web":
		return "poe.com"
	case "v0-vercel-web":
		return "v0.dev"
	case "blackbox-web":
		return "www.blackbox.ai"
	case "muse-spark-web":
		return "www.meta.ai"
	case "t3-web":
		return "t3.chat"
	case "hailuo-web":
		return "hailuo.com"
	case "zai-web":
		return "chat.z.ai"
	case "venice-web":
		return "venice.ai"
	case "notion-web":
		return "www.notion.so"
	case "inner-ai":
		return "inner.ai"
	case "adapta-web":
		return "adapta.org"
	case "microsoft-designer-web":
		return "designer.microsoft.com"
	case "copilot-m365-web":
		return "microsoft365.com"
	default:
		return "" // unknown provider: concrete adapter must define a cookie scope
	}
}

// Adapter is the interface every concrete web-cookie provider implements.
// Each adapter owns its site-specific protocol (HTTP/SockJS/WS), auth flow,
// and response parsing. The framework provides session management and the
// http.Client; the adapter provides the protocol.
type Adapter interface {
	// ProviderCode returns the provider this adapter handles (e.g. "deepseek-web").
	ProviderCode() string

	// Login authenticates a session, populating Cookies. Called when a session
	// is first needed or after its cookies expire. The credentials come from
	// the credential's Metadata (e.g. browser cookie dump, username/password).
	Login(ctx context.Context, accountLabel string, creds map[string]string) (*Session, error)

	// RefreshSession refreshes an expiring session (re-fetches CSRF tokens,
	// rotates short-lived cookies). Return the same session refreshed.
	RefreshSession(ctx context.Context, s *Session) (*Session, error)

	// Chat sends a chat-completion-shaped request via the site's protocol and
	// returns the raw response for the executor to stream/parse. isStream
	// indicates whether the client requested SSE.
	Chat(ctx context.Context, s *Session, cand provider.Candidate, body []byte, isStream bool) (*http.Response, error)

	// ParseStream converts the site's proprietary stream format (SockJS frames,
	// JSON lines, etc.) into OpenAI-style SSE chunks the gateway can forward.
	// Writes converted chunks to w.
	ParseStream(w http.ResponseWriter, resp *http.Response) error
}

// Registry holds all registered web-cookie adapters, keyed by provider code.
// The executor dispatch (executor.go switch cand.Protocol → "webcookie") looks
// up the adapter here, then uses SessionManager to get/create a session.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry creates an empty adapter registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register adds an adapter. Idempotent; later registrations replace earlier.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.ProviderCode()] = a
}

// Get returns the adapter for a provider code, or nil if unimplemented.
func (r *Registry) Get(code string) Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adapters[code]
}

// Codes returns all registered provider codes (for /v1/models and admin UI).
func (r *Registry) Codes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	codes := make([]string, 0, len(r.adapters))
	for c := range r.adapters {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}

// DefaultRegistry is the package-level registry the executor consults.
// Concrete adapters register themselves in init() or via explicit wiring.
var DefaultRegistry = NewRegistry()

// SessionManager owns the pool of authenticated sessions per
// (provider, account, tenant). Sessions are loaded from the webcookie_sessions
// DB table (migration 077) and refreshed lazily when they expire.
//
// TenantID MUST be supplied on Get/Put. Tenant collisions are explicit: two
// tenants sharing providerCode+accountLabel are kept in separate Session
// objects so cookies never leak across tenants. Pre-existing single-tenant
// callers passing "" continue to work (their sessions share the same "" bucket).
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session // key: tenantID + "\x00" + providerCode + "\x00" + accountLabel
}

// NewSessionManager creates an empty session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{sessions: make(map[string]*Session)}
}

func sessionKey(tenantID, providerCode, accountLabel string) string {
	return tenantID + "\x00" + providerCode + "\x00" + accountLabel
}

// Get returns an existing non-expired session for the given tenant/provider/
// account tuple, or nil if none.
func (m *SessionManager) Get(providerCode, accountLabel, tenantID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionKey(tenantID, providerCode, accountLabel)]
	if !ok || s == nil {
		return nil
	}
	if !s.ExpiresAt.IsZero() && time.Now().After(s.ExpiresAt) {
		delete(m.sessions, sessionKey(tenantID, providerCode, accountLabel))
		return nil
	}
	return s
}

// Put stores a session, replacing any prior session for the same
// (tenant, provider, account) tuple. s.TenantID is the authoritative tenant
// key — empty string puts into the shared "" bucket.
func (m *SessionManager) Put(s *Session) {
	if s == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sessionKey(s.TenantID, s.ProviderCode, s.AccountLabel)] = s
}

// DefaultSessionManager is the package-level session pool.
var DefaultSessionManager = NewSessionManager()
