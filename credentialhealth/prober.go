package credentialhealth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// CredentialProber probes credential health with lightweight requests.
// Used before marking degraded and during recovery checks.
type CredentialProber interface {
	// ProbeCredential sends a lightweight test request to verify credential health.
	// Returns success=true if credential responds normally.
	ProbeCredential(ctx context.Context, credentialID int, model string) ProbeResult
}

// ProbeResult contains probe outcome.
type ProbeResult struct {
	Success   bool
	ErrorKind errorsx.ErrorKind
	Latency   time.Duration
	Detail    string
}

// NoopProber always returns success (for testing or when probe is disabled).
type NoopProber struct{}

func (NoopProber) ProbeCredential(ctx context.Context, credentialID int, model string) ProbeResult {
	return ProbeResult{Success: true}
}

// DBProber checks recent call history in credential_health_calls (Redis/DB).
// If there's a successful call within last 30s, consider credential healthy.
// This is a lightweight probe that doesn't make actual upstream requests.
type DBProber struct {
	recorder *Recorder
	window   time.Duration // default 30s
}

// NewDBProber creates a DB-based prober.
func NewDBProber(recorder *Recorder, window time.Duration) *DBProber {
	if window == 0 {
		window = 30 * time.Second
	}
	return &DBProber{
		recorder: recorder,
		window:   window,
	}
}

func (p *DBProber) ProbeCredential(ctx context.Context, credentialID int, model string) ProbeResult {
	if p.recorder == nil {
		return ProbeResult{Success: false, Detail: "recorder is nil"}
	}

	// Get recent calls (since window ago)
	since := time.Now().Add(-p.window)
	entries, err := p.recorder.GetRecent(ctx, credentialID, model, since)
	if err != nil {
		slog.Warn("prober: failed to get recent calls",
			"credential_id", credentialID,
			"model", model,
			"error", err)
		return ProbeResult{Success: false, Detail: fmt.Sprintf("get calls failed: %v", err)}
	}

	// If no recent calls, cannot determine health → fail probe (require actual test)
	if len(entries) == 0 {
		return ProbeResult{Success: false, Detail: "no recent calls"}
	}

	// Check if there's any successful call in recent window
	for _, e := range entries {
		if e.Success {
			slog.Info("prober: found recent success, credential healthy",
				"credential_id", credentialID,
				"model", model,
				"recent_calls", len(entries))
			return ProbeResult{
				Success: true,
				Latency: time.Duration(e.LatencyMs) * time.Millisecond,
			}
		}
	}

	// All recent calls failed
	var lastErrorKind errorsx.ErrorKind
	if len(entries) > 0 && entries[0].ErrorKind != "" {
		lastErrorKind = errorsx.ErrorKind(entries[0].ErrorKind)
	}

	slog.Info("prober: all recent calls failed, credential unhealthy",
		"credential_id", credentialID,
		"model", model,
		"recent_calls", len(entries),
		"last_error_kind", lastErrorKind)

	return ProbeResult{
		Success:   false,
		ErrorKind: lastErrorKind,
		Detail:    fmt.Sprintf("all %d recent calls failed", len(entries)),
	}
}
