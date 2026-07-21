package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
)

// Proxy is the Gateway entry reverse proxy. Holds an atomic pointer to the
// active backend address. /launcher/* paths route to the local API handler;
// everything else is forwarded to the active backend.
//
// Activation in blue-green: SwitchActive atomically rebuilds the reverse
// proxy Director. Established connections (HTTP keep-alive) aren't cut —
// the switch only affects new connection targets.
type Proxy struct {
	activeAddr atomic.Pointer[string]
	apiHandler atomic.Pointer[http.Handler]
	rp         atomic.Pointer[httputil.ReverseProxy]
}

// New creates a proxy with initial active target at activeAddr ("host:port").
func New(activeAddr string) *Proxy {
	p := &Proxy{}
	p.activeAddr.Store(&activeAddr)
	p.SetAPIHandler(http.NotFoundHandler())
	p.rebuildReverseProxy()
	return p
}

// SetAPIHandler injects the local /launcher/* handler (api package).
func (p *Proxy) SetAPIHandler(h http.Handler) {
	p.apiHandler.Store(&h)
}

// ActiveAddr returns the current active backend address.
func (p *Proxy) ActiveAddr() string {
	addr := p.activeAddr.Load()
	if addr == nil {
		return ""
	}
	return *addr
}

// SwitchActive atomically changes the active backend address.
// Used for blue-green activation. Doesn't cut existing connections.
func (p *Proxy) SwitchActive(addr string) {
	slog.Info("proxy switch active", "from", p.ActiveAddr(), "to", addr)
	p.activeAddr.Store(&addr)
	p.rebuildReverseProxy()
}

// rebuildReverseProxy constructs a new ReverseProxy targeting the current
// active address. Called after SwitchActive so future requests go to the
// new target. Old proxy is GC'd once no references remain.
func (p *Proxy) rebuildReverseProxy() {
	target := &url.URL{Scheme: "http", Host: p.ActiveAddr()}
	rp := httputil.NewSingleHostReverseProxy(target)
	// Custom ErrorHandler so 502 from a down backend doesn't crash the daemon
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Warn("proxy upstream error", "addr", p.ActiveAddr(), "err", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	p.rp.Store(rp)
}

// ServeHTTP implements http.Handler. Routes /launcher/* to local API,
// otherwise forwards to the active backend.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/launcher/") {
		h := p.apiHandler.Load()
		if h != nil {
			(*h).ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	rp := p.rp.Load()
	if rp != nil {
		(*rp).ServeHTTP(w, r)
	} else {
		http.Error(w, "proxy not initialized", http.StatusInternalServerError)
	}
}
