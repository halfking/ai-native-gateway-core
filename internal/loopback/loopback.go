// Package loopback resolves this gateway instance's own loopback base URL.
//
// Several in-process features call the gateway itself over HTTP (auto
// session title/summary generation, credential & node self-checks, the
// system monitor's gateway round). Those self-calls used to hard-code
// http://127.0.0.1:8781, which broke on every blue-green generation where
// the active instance serves on :8782 — auto titles, for example, failed
// with "connection refused" until the next deploy flipped the port back.
// The instance's own listen address is the authoritative source of the
// port (the blue-green canary units inject LLM_GATEWAY_LISTEN per
// instance), so it is resolved here instead of assuming :8781.
package loopback

import (
	"net"
	"os"
	"strconv"
	"strings"
)

// defaultPort is the historic listen port, kept as the fallback so that
// env-less setups (local dev, tests) keep behaving exactly as before.
const defaultPort = "8781"

// GatewayBase returns the loopback base URL ("http://127.0.0.1:<port>")
// of THIS gateway instance, without a path suffix.
//
// Resolution order:
//  1. the port of LLM_GATEWAY_LISTEN (":8782", "0.0.0.0:8782",
//     "127.0.0.1:8782", "[::]:8782" all yield http://127.0.0.1:8782)
//  2. http://127.0.0.1:8781
//
// Feature-level overrides (LLM_GATEWAY_ENDPOINT, *_BASE_URL) are checked
// at the call sites and take precedence over this value.
func GatewayBase() string {
	listen := strings.TrimSpace(os.Getenv("LLM_GATEWAY_LISTEN"))
	if listen != "" {
		if _, port, err := net.SplitHostPort(listen); err == nil {
			if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65536 {
				return "http://127.0.0.1:" + port
			}
		}
	}
	return "http://127.0.0.1:" + defaultPort
}
