// Package upstream provides an HTTP client wrapper for upstream LLM provider
// calls with configurable timeouts, retry, and error classification.
package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default domains whose requests should NEVER go through an HTTP proxy.
// These are Chinese mainland providers or local infra that must be reached directly.
var defaultDomesticDomains = []string{
	"api.minimax.chat",
	"api.minimaxi.com",
	"api.deepseek.com",
	"api.moonshot.cn",
	"api.scnet.cn",
	"api.coze.cn",
	"dashscope.aliyuncs.com",
	"open.bigmodel.cn",
	"spark-api-open.xf-yun.com",
	"hunyuan.tencent.com",
	"qianfan.baidubce.com",
	"api.lkeap.cloud.tencent.com",
	"aip.baidubce.com",
	"mg-new.evolai.cn",
	"llm.kxpms.cn",
	"kxpms.cn",
	"localhost",
	"127.0.0.1",
}

// ProxyResolver decides whether to use an HTTP proxy for a given upstream host.
//
// On startup it performs a quick health check on the HTTP_PROXY/HTTPS_PROXY
// configured in the environment. If the proxy is unreachable the resolver
// transparently disables proxying for the lifetime of the process (logged once).
// A background goroutine keeps probing the proxy on a fixed interval so that
// when the proxy comes back online the resolver re-enables it automatically
// without a process restart.
// At request time, hosts that match the "domestic" allow-list always bypass
// the proxy, even if the proxy is healthy.
type ProxyResolver struct {
	mu sync.RWMutex

	proxyURL      *url.URL
	proxyEnabled  atomic.Bool
	domesticHosts map[string]bool
	checkDone     bool
	healthTimeout time.Duration

	probeInterval time.Duration
	probeStop     chan struct{}
}

// NewProxyResolver reads HTTP_PROXY/HTTPS_PROXY from the environment and
// returns a resolver. It also kicks off an async health check; until the
// check completes the resolver still uses the proxy (if set) for unknown
// hosts but skips it for the domestic allow-list.
//
// A background goroutine will continue to probe the proxy every
// probeInterval (30s by default) so that the resolver can automatically
// re-enable the proxy once it becomes reachable again.
func NewProxyResolver(extraDomesticHosts ...string) *ProxyResolver {
	r := &ProxyResolver{
		domesticHosts: make(map[string]bool, len(defaultDomesticDomains)+len(extraDomesticHosts)),
		healthTimeout: 5 * time.Second,
		probeInterval: 30 * time.Second,
		probeStop:     make(chan struct{}),
	}
	for _, h := range defaultDomesticDomains {
		r.domesticHosts[strings.ToLower(h)] = true
	}
	for _, h := range extraDomesticHosts {
		r.domesticHosts[strings.ToLower(h)] = true
	}

	rawProxy := firstNonEmpty(
		os.Getenv("HTTPS_PROXY"),
		os.Getenv("https_proxy"),
		os.Getenv("HTTP_PROXY"),
		os.Getenv("http_proxy"),
	)
	if rawProxy == "" {
		slog.Info("proxy resolver: no HTTP_PROXY/HTTPS_PROXY configured; all traffic is direct")
		r.proxyEnabled.Store(false)
		r.checkDone = true
		return r
	}

	u, err := url.Parse(rawProxy)
	if err != nil || u.Host == "" {
		slog.Warn("proxy resolver: invalid proxy URL; disabling", "raw", rawProxy, "error", err)
		r.proxyEnabled.Store(false)
		r.checkDone = true
		return r
	}
	r.proxyURL = u
	r.proxyEnabled.Store(true)

	go r.healthCheck()
	go r.probeLoop()
	return r
}

// Stop releases the background probe goroutine. Call from main shutdown.
func (r *ProxyResolver) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probeStop != nil {
		select {
		case <-r.probeStop:
		default:
			close(r.probeStop)
		}
	}
}

func (r *ProxyResolver) probeLoop() {
	t := time.NewTicker(r.probeInterval)
	defer t.Stop()
	for {
		select {
		case <-r.probeStop:
			return
		case <-t.C:
			r.healthCheck()
		}
	}
}

func (r *ProxyResolver) healthCheck() {
	r.mu.Lock()
	r.checkDone = true
	r.mu.Unlock()

	host := r.proxyURL.Host
	if r.proxyURL.Port() == "" {
		host = net.JoinHostPort(r.proxyURL.Hostname(), "1080")
	}
	dctx, cancel := context.WithTimeout(context.Background(), r.healthTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dctx, "tcp", host)
	if err != nil {
		if r.proxyEnabled.Load() {
			slog.Warn("proxy resolver: proxy health check FAILED — disabling HTTP proxy for this process (will retry periodically)",
				"proxy", r.proxyURL.String(),
				"host", host,
				"error", err,
			)
		}
		r.proxyEnabled.Store(false)
		return
	}
	_ = conn.Close()
	if !r.proxyEnabled.Load() {
		slog.Info("proxy resolver: proxy health check RECOVERED — re-enabling HTTP proxy",
			"proxy", r.proxyURL.String(),
		)
	}
	r.proxyEnabled.Store(true)
}

// Resolve returns the proxy URL to use for a given request, or nil for direct.
//
//   - Domestic hosts always return nil (direct connect).
//   - Non-domestic hosts return the configured proxy URL when healthy.
//   - When the proxy is unhealthy, non-domestic hosts also fall back to nil.
func (r *ProxyResolver) Resolve(reqURL *url.URL) *url.URL {
	if reqURL == nil {
		return nil
	}
	host := strings.ToLower(reqURL.Hostname())
	if r.isDomestic(host) {
		return nil
	}
	if r.proxyURL == nil {
		return nil
	}
	if !r.proxyEnabled.Load() {
		return nil
	}
	return r.proxyURL
}

// IsDomestic reports whether a host matches the domestic allow-list.
func (r *ProxyResolver) IsDomestic(host string) bool {
	return r.isDomestic(strings.ToLower(host))
}

func (r *ProxyResolver) isDomestic(host string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.domesticHosts[host] {
		return true
	}
	// strip leading host label (e.g. "integration.api.x.com" → "api.x.com")
	if i := strings.Index(host, "."); i > 0 {
		if r.domesticHosts[host[i+1:]] {
			return true
		}
		// also try matching the registrable suffix for CN providers
		for suffix := range r.domesticHosts {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		}
	}
	return false
}

// ProxyFunc returns an http.Proxy function backed by this resolver.
func (r *ProxyResolver) ProxyFunc() func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		u := r.Resolve(req.URL)
		if u == nil {
			return nil, nil
		}
		return u, nil
	}
}

// Status returns a diagnostic snapshot of the resolver state.
func (r *ProxyResolver) Status() map[string]any {
	r.mu.RLock()
	done := r.checkDone
	r.mu.RUnlock()
	out := map[string]any{
		"healthy":     r.proxyEnabled.Load(),
		"health_done": done,
		"domestic":    sortedKeys(r.domesticHosts),
	}
	if r.proxyURL != nil {
		out["proxy"] = r.proxyURL.String()
	} else {
		out["proxy"] = ""
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// simple sort
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// ensure compile error surfaces early if fmt is unused
var _ = fmt.Sprintf
