package licensing

// License-health API endpoints (Phase 3B-1).
//
// Why this file
// -------------
// Phase 3C introduced a package-level DaemonHealth singleton and a
// graceful-enforcement FailureMarker. Until now, both lived entirely
// inside Go memory — ops teams had no way to see them through HTTP.
// This file adds two read-only endpoints that the OpsOverviewView.vue
// (and any external monitoring) can poll:
//
//   GET /api/system/license/health
//     → DaemonHealth snapshot serialized as JSON
//     → includes last-refresh outcome, consecutive-failure count,
//        recent-failures ring buffer (so dashboards can graph rate)
//
//   GET /api/system/license/status
//     → FailureMarker (grace state) plus a computed
//        "grace_remaining_seconds" field so the UI can show a
//        countdown timer when license verification is broken
//     → in_grace bool flips true when the marker is within the grace
//        window, so alerts route correctly
//
// Both endpoints are ALLOWED in restricted mode (the path-prefix
// check is `/api/system/license/...`). No authentication — these
// return only metadata about the licensing subsystem itself, no
// secrets.
//
// Operational endpoint set
// ------------------------
//
//   /api/system/license/health   — token-refresh daemon outcomes
//   /api/system/license/status   — graceful-enforcement state
//   /api/system/license          — existing license management (admin auth)
//   /api/system/license/...      — existing offline activation flow

import (
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
)

// LicenseHealthHandler exposes the licensing subsystem's runtime
// health and grace state via HTTP. It holds no I/O dependencies —
// DaemonHealth is a singleton, FailureMarker is on-disk under
// dataDir. Kept as a separate type from AdminHandler so the wiring
// path is minimal (no Store / Crypto dependencies to mock).
type LicenseHealthHandler struct {
	// DataDir is the directory where FailureMarker is stored. If
	// empty the grace endpoints return zero-value (healthy) data.
	DataDir string

	// DefaultGraceDuration is the matching grace window for
	// computing grace_remaining_seconds when no explicit policy
	// is in effect. Defaults to DefaultGracePeriod (24h) if zero.
	DefaultGraceDuration time.Duration
}

// NewLicenseHealthHandler returns a handler bound to dataDir.
// Sensible default for GraceDuration; pass 0 to use
// DefaultGracePeriod.
func NewLicenseHealthHandler(dataDir string, defaultGrace time.Duration) *LicenseHealthHandler {
	if defaultGrace <= 0 {
		defaultGrace = DefaultGracePeriod
	}
	return &LicenseHealthHandler{
		DataDir:              dataDir,
		DefaultGraceDuration: defaultGrace,
	}
}

// RegisterHealthRoutes attaches the two endpoints to the supplied
// echo group (typically /api/system/license).
func (h *LicenseHealthHandler) RegisterHealthRoutes(g *echo.Group) {
	g.GET("/health", h.GetHealth)
	g.GET("/status", h.GetStatus)
}

// LicenseHealthResponse is the JSON shape returned by GetHealth.
// It mirrors DaemonHealth but is a flat serialisable struct so HTTP
// callers don't have to deal with the embedded mutex.
type LicenseHealthResponse struct {
	Healthy          bool      `json:"healthy"`
	StartedAt        time.Time `json:"started_at"`
	LastCycleAt      time.Time `json:"last_cycle_at,omitempty"`
	LastSuccessAt    time.Time `json:"last_success_at,omitempty"`
	LastErrorAt      time.Time `json:"last_error_at,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	TotalCycles      int64     `json:"total_cycles"`
	TotalSuccesses   int64     `json:"total_successes"`
	TotalFailures    int64     `json:"total_failures"`
	ConsecutiveFails int       `json:"consecutive_fails"`

	// RecentFailures is a bounded list (max 8) of timestamps — useful
	// for graphing failure streaks in dashboards.
	RecentFailures []time.Time `json:"recent_failures,omitempty"`

	// Stale is true when the daemon hasn't run a cycle in 2× the
	// expected refresh interval. Computed server-side so dashboards
	// don't need to know the cadence.
	//
	// Convention: stale = no cycle in the last 2h without a prior
	// successful one. Tunable via REFRESH_INTERVAL_SECONDS env var.
	Stale bool `json:"stale"`
}

// GetHealth returns the DaemonHealth snapshot. Returns a
// zero-value (Healthy: true) response if the daemon has never
// run — this lets dashboards render "no data yet" without
// flashing red.
func (h *LicenseHealthHandler) GetHealth(c echo.Context) error {
	snap := GetDaemonHealth()

	interval := 1 * time.Hour // typical daemon refresh interval
	if s := os.Getenv("REFRESH_INTERVAL_SECONDS"); s != "" {
		// Allow override for testing / non-default cadences.
		if d, err := time.ParseDuration(s + "s"); err == nil && d > 0 {
			interval = d
		}
	}
	// "Stale" if last cycle > 2× interval ago AND total cycles > 0
	// (never-run is intentionally not stale — no signal to alarm on).
	stale := !snap.LastCycleAt.IsZero() &&
		snap.TotalCycles > 0 &&
		time.Since(snap.LastCycleAt) > 2*interval

	healthy := snap.Healthy() && !stale

	resp := LicenseHealthResponse{
		Healthy:          healthy,
		StartedAt:        snap.StartedAt,
		LastCycleAt:      snap.LastCycleAt,
		LastSuccessAt:    snap.LastSuccessAt,
		LastErrorAt:      snap.LastErrorAt,
		LastError:        snap.LastError,
		TotalCycles:      snap.TotalCycles,
		TotalSuccesses:   snap.TotalSuccesses,
		TotalFailures:    snap.TotalFailures,
		ConsecutiveFails: snap.ConsecutiveFails,
		RecentFailures:   snap.RecentFailures,
		Stale:            stale,
	}
	return c.JSON(http.StatusOK, resp)
}

// LicenseStatusResponse is the JSON shape returned by GetStatus.
// Includes the raw FailureMarker plus derived fields that the
// frontend can render directly without re-implementing grace
// arithmetic.
type LicenseStatusResponse struct {
	// Mode is one of: "normal", "in_grace", "restricted".
	// "restricted" is currently unreachable through this endpoint
	// (the v2 middleware already blocks non-license paths) but
	// included for symmetry.
	Mode string `json:"mode"`

	// GraceConfigured is the grace window the system would apply
	// (DefaultGracePeriod unless operator-configured).
	// Serialised as seconds (a top-level duration field is awkward
	// in JSON — clients can multiply by time.Second).
	GraceConfiguredSeconds int64 `json:"grace_configured_seconds"`

	// InGrace is true when the failure marker exists and is within
	// the configured grace window. False when healthy or when grace
	// has expired and the system has fallen into restricted mode.
	InGrace bool `json:"in_grace"`

	// GraceRemainingSeconds is the time until grace expires, or 0
	// when not in grace.
	GraceRemainingSeconds int64 `json:"grace_remaining_seconds"`

	// Marker is the raw FailureMarker (or nil if no marker).
	Marker *FailureMarker `json:"marker,omitempty"`

	// NoGraceHonored reports whether LICENSE_NO_GRACE=1 is set —
	// useful for ops to confirm their incident override took effect
	// without shell access.
	NoGraceHonored bool `json:"no_grace_honored"`
}

// GetStatus returns the grace state for ops dashboards. Reads the
// failure marker from disk and computes grace elapsed vs configured
// duration.
func (h *LicenseHealthHandler) GetStatus(c echo.Context) error {
	resp := LicenseStatusResponse{
		GraceConfiguredSeconds: int64(h.DefaultGraceDuration / time.Second),
		NoGraceHonored:         os.Getenv("LICENSE_NO_GRACE") == "1",
	}

	if h.DataDir == "" {
		// No data dir → assume healthy (no marker can exist).
		resp.Mode = "normal"
		return c.JSON(http.StatusOK, resp)
	}

	marker, err := ReadLicenseFailure(h.DataDir)
	if err != nil || marker.FailedAt.IsZero() {
		// No marker on disk → service is healthy.
		resp.Mode = "normal"
		return c.JSON(http.StatusOK, resp)
	}

	resp.Marker = &marker

	elapsed := time.Since(marker.FailedAt)
	remaining := h.DefaultGraceDuration - elapsed
	if elapsed < h.DefaultGraceDuration && remaining > 0 {
		resp.Mode = "in_grace"
		resp.InGrace = true
		resp.GraceRemainingSeconds = int64(remaining / time.Second)
	} else {
		resp.Mode = "restricted"
	}

	return c.JSON(http.StatusOK, resp)
}
