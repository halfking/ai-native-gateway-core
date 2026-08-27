package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMonitorRoutesRegisteredWithoutRedis verifies that credential monitor
// routes (especially /api/credentials/monitor-summary) are registered even
// when Redis is not available. This is a regression test for the 404 error
// when h.redisClient is nil.
func TestMonitorRoutesRegisteredWithoutRedis(t *testing.T) {
	// Create a handler without Redis (redisClient = nil)
	h := &Handler{db: nil, redisClient: nil}
	
	// Create monitor handlers with nil Redis
	monitorH := NewCredentialMonitorHandlers(h, nil, nil)
	
	// Register routes on a test mux
	mux := http.NewServeMux()
	wrap := func(fn http.HandlerFunc) http.HandlerFunc { return fn }
	monitorH.RegisterMonitorRoutes(mux, wrap)
	
	// Test that routes are registered by checking the handler exists
	// We use a non-GET method that returns 405 instead of executing the handler
	routes := map[string]string{
		"/api/credentials/monitor-summary":         http.MethodGet,
		"/api/credentials/sliding-window":          http.MethodGet,
		"/api/credentials/promote":                 http.MethodPost,
		"/api/credentials/demote":                  http.MethodPost,
		"/api/credentials/set-concurrency-auto":    http.MethodPost,
		"/api/credentials/model-toggle":            http.MethodPost,
		"/api/credentials/model-history":           http.MethodGet,
		"/api/credentials/decisions":               http.MethodGet,
		"/api/credentials/clear-manual-disabled":   http.MethodPost,
		"/api/credentials/set-manual-disabled":     http.MethodPost,
	}
	
	for route, validMethod := range routes {
		t.Run(route, func(t *testing.T) {
			// Use an invalid method to avoid executing the actual handler logic
			// which would panic on nil DB. A registered route returns 405,
			// an unregistered route returns 404.
			var testMethod string
			if validMethod == http.MethodGet {
				testMethod = http.MethodPost
			} else {
				testMethod = http.MethodGet
			}
			
			req := httptest.NewRequest(testMethod, route, nil)
			rr := httptest.NewRecorder()
			
			// Call the mux to see if it routes
			mux.ServeHTTP(rr, req)
			
			// A 404 means the route wasn't registered
			// A 405 means the route IS registered but wrong method
			if rr.Code == http.StatusNotFound {
				t.Errorf("route %s returned 404 - route not registered (Redis was nil)", route)
			}
		})
	}
}
