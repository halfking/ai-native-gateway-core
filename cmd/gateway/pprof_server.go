package main

import (
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

// newPprofServer creates an explicitly enabled, loopback-only diagnostic
// server. Keeping it off the public mux prevents the SPA catch-all and avoids
// exposing profiles on the gateway's data-plane listener.
func newPprofServer(addr string) (*http.Server, bool) {
	if !isLoopbackListenAddr(addr) {
		return nil, false
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		mux.Handle("/debug/pprof/"+name, pprof.Handler(name))
	}
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}, true
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
