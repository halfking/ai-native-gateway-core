package streaming

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// SubAgentHeader is the header clients use to report sub-agent statuses
// for the current goal session. The gateway treats the report as advisory
// metadata: it backs the CompletionDetector sub-agent gate (do not declare
// "task done" while sub-agents are still running) and feeds the
// goal-mode continue payload (sub_agents_pending field).
//
// Wire format: a JSON array of {id, status} objects. Status values are
// opaque to the gateway; only "completed" is recognized as terminal today.
// Unknown statuses are counted as pending (fail-safe: don't declare done).
//
// Example:
//
//	X-Gw-Sub-Agents: [{"id":"agent-1","status":"completed"},{"id":"agent-2","status":"running"}]
const SubAgentHeader = "X-Gw-Sub-Agents"

// SubAgentStatus enumerates the gateway-recognized terminal status. Any
// other value (including empty / unknown) is treated as in-flight.
const (
	SubAgentStatusCompleted = "completed"
)

// SubAgentReport is one row in the X-Gw-Sub-Agents payload.
type SubAgentReport struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// SubAgentSnapshot is the gateway-side aggregate after parsing the header.
type SubAgentSnapshot struct {
	Total     int
	Completed int
	Pending   int
}

// parseSubAgentsHeader decodes the X-Gw-Sub-Agents value into a snapshot.
// Returns an empty snapshot when the header is missing, malformed, or
// parses to an empty list. Logging is best-effort — the parser must not
// block the request path on a malformed header.
func parseSubAgentsHeader(raw string) SubAgentSnapshot {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SubAgentSnapshot{}
	}
	var rows []SubAgentReport
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		slog.Debug("sub_agents_header_parse_failed", "error", err)
		return SubAgentSnapshot{}
	}
	out := SubAgentSnapshot{Total: len(rows)}
	for _, row := range rows {
		if strings.EqualFold(row.Status, SubAgentStatusCompleted) {
			out.Completed++
		} else {
			out.Pending++
		}
	}
	return out
}
