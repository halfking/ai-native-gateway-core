// Package instanceid resolves the stable identity of a running gateway
// instance.
//
// The identity answers "which machine produced this row / owns this queue
// lease". Two very different layers need the same answer:
//
//   - cmd/gateway's dispatch wiring (queue backend, governor backend) —
//     package main, which no other package can import;
//   - domains/session/v2's turn writer, which stamps it into
//     session_turns.host (migration 885) so that "which machine wrote this
//     turn" is answerable from the row itself.
//
// Putting the resolution here (instead of duplicating it) is what keeps the
// two answers equal. The precedence is pinned by
// TestResolvePrefersExplicitEnvOverHostname.
//
// Resolution order (2026-10-20):
//
//  1. LLM_GATEWAY_INSTANCE_ID — explicit, operator-provided identity;
//  2. os.Hostname() — the machine;
//  3. "llm-gateway" — last resort, so the value is never empty.
package instanceid

import (
	"os"
	"strings"
)

// EnvInstanceID is the environment variable that overrides the identity.
const EnvInstanceID = "LLM_GATEWAY_INSTANCE_ID"

// Fallback is used when neither the environment nor the hostname yields a
// value. It is a constant (not a timestamp) on purpose: the identity must be
// stable across restarts, otherwise every restart would look like a new
// machine in the session family's host column.
const Fallback = "llm-gateway"

// Resolve returns the stable identity of this gateway instance.
func Resolve() string {
	if configured := strings.TrimSpace(os.Getenv(EnvInstanceID)); configured != "" {
		return configured
	}
	if hostname, err := os.Hostname(); err == nil {
		if trimmed := strings.TrimSpace(hostname); trimmed != "" {
			return trimmed
		}
	}
	return Fallback
}
