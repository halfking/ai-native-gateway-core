// Package gateway: capabilities endpoint (GW-0.2).
//
// GET /api/v2/capabilities returns the gateway's current capability list per
// docs/全面优化v1/API-DETAILS.md §5. Feature labels mirror the audit matrix in
// docs/全面优化v1/README.md §二 and must reflect source-code facts only:
// current (proven by source), partial (foundation exists, not closed-loop),
// planned (target capability). The endpoint is unauthenticated — it exposes
// no provider/credential data (unlike /metrics) and is intended for
// cross-service capability discovery.
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
)

// defaultPrimaryPort is the documented primary gateway port (cmd/gateway).
// Used as fallback when the configured listen address cannot be parsed.
const defaultPrimaryPort = 8781

// gatewayV2DemoPort is the parallel demo entry (cmd/gateway-v2), reported so
// consumers can distinguish it from the production primary port.
const gatewayV2DemoPort = 8782

// CapabilitiesResponse is the GW-0.2 response contract.
type CapabilitiesResponse struct {
	Service  string            `json:"service"`
	Version  string            `json:"version"`
	Status   string            `json:"status"`
	Features map[string]string `json:"features"`
	Ports    map[string]int    `json:"ports"`
}

// NewCapabilitiesHandler builds the GET /api/v2/capabilities handler.
//
// version comes from the version.json SSOT (see version.go Version()).
// listenAddr is the configured primary listen address (cfg.Listen), used to
// report the actual primary port instead of the hardcoded default.
func NewCapabilitiesHandler(version, listenAddr string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CapabilitiesResponse{
			Service: "llm-gateway-go",
			Version: version,
			Status:  "native",
			Features: map[string]string{
				"data_plane":     "current",
				"sticky_session": "current",
				"tenant_quota":   "current",
				// Gateway-to-SM outbox delivery is wired but remains opt-in; deployed
				// HMAC delivery, consumer idempotency, and ownership reconciliation are not closed-loop.
				"durable_outbox":       "partial",
				"plugin_runtime":       "partial",
				"webhook_subscription": "implemented",
			},
			Ports: map[string]int{
				"primary":         parseListenPort(listenAddr),
				"gateway_v2_demo": gatewayV2DemoPort,
			},
		})
	})
}

// parseListenPort extracts the TCP port from a listen address. Unparseable or
// empty values fall back to defaultPrimaryPort so the endpoint never reports
// port 0.
func parseListenPort(listenAddr string) int {
	if listenAddr == "" {
		return defaultPrimaryPort
	}
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return defaultPrimaryPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return defaultPrimaryPort
	}
	return port
}
