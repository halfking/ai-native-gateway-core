package main

import (
	"os"
	"strings"
	"testing"
)

// TestDispatchWaterfallByRequestRouteWired guards the request-detail waterfall
// path. The frontend lazily requests this URL for ?tab=waterfall; leaving the
// handler unregistered makes the request fall through to an unrelated route
// and the SPA treats the resulting 401 as an auth loss.
func TestDispatchWaterfallByRequestRouteWired(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	const route = `mux.HandleFunc("/api/admin/dispatch/waterfall/request/", wrapAdmin(handleDispatchWaterfallByRequest))`
	if !strings.Contains(text, route) {
		t.Fatalf("main.go: missing authenticated by-request waterfall route %q", route)
	}
}
