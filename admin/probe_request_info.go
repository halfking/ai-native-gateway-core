// admin/probe_request_info.go — derive probe metadata from a
// telemetry.RequestLogEntry for the live-stream swim lane.
//
// 2026-07-13: bg.ActiveProbeWorker writes probe rows with:
//
//	task_type       = "probe_triggered"
//	task_type_chosen = "probe_direct"
//	quality_flags    = ["probe","direct",...]
//	auto_decision    = JSON with "probe_attempt": N
//
// LiveRequestFromTelemetry calls extractProbeInfo to populate
// IsProbe / ProbeOrigin / ProbeAttempt on the live-stream row.
package admin

import (
	"encoding/json"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// probeRequestInfo is the small subset of probe metadata the live-stream
// dashboard needs to render the 🛡️ Probe pill + the "仅探测" filter.
type probeRequestInfo struct {
	IsProbe      bool
	ProbeOrigin  string
	ProbeAttempt int
}

// extractProbeInfo reads a telemetry.RequestLogEntry and returns the
// probe metadata to surface on the live-stream row. Returns the zero
// value when the entry is not a probe row, so callers can blindly
// assign the result without nil checks.
func extractProbeInfo(entry *telemetry.RequestLogEntry) probeRequestInfo {
	if entry == nil {
		return probeRequestInfo{}
	}
	if entry.TaskType == nil || *entry.TaskType != "probe_triggered" {
		return probeRequestInfo{}
	}

	origin := "direct"
	if entry.TaskTypeChosen != nil {
		switch *entry.TaskTypeChosen {
		case "probe_direct":
			origin = "direct"
		case "probe_gateway":
			origin = "gateway"
		case "probe_scheduled":
			origin = "scheduled"
		}
	}

	attempt := 0
	// Attempt 1: parse from AutoDecision JSON (preferred; typed)
	if entry.AutoDecision != nil && *entry.AutoDecision != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(*entry.AutoDecision), &meta); err == nil {
			if v, ok := meta["probe_attempt"].(float64); ok {
				attempt = int(v)
			} else if v, ok := meta["probe_attempt"].(int); ok {
				attempt = v
			}
		}
	}
	// Attempt 2: parse from quality_flags "attempt_N" (fallback for
	// older rows written before the auto_decision JSONB was added).
	if attempt == 0 && entry.QualityFlags != nil {
		for _, f := range entry.QualityFlags {
			if len(f) > 8 && f[:8] == "attempt_" {
				if n, err := strconv.Atoi(f[8:]); err == nil && n > attempt {
					attempt = n
				}
			}
		}
	}

	return probeRequestInfo{
		IsProbe:      true,
		ProbeOrigin:  origin,
		ProbeAttempt: attempt,
	}
}
