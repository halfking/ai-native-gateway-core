// Package backend abstracts deploy backends for the launcher's blue-green
// orchestration. The orchestrator (see orchestrator package) only depends
// on the Backend interface.
package backend

import "context"

// Release describes a version to deploy.
//
// Compose backend uses Image (container image tag).
// systemd backend (Phase 2) uses DownloadURL + SHA256.
type Release struct {
	Version     string
	Image       string // container image tag for compose
	DownloadURL string // binary URL for systemd
	SHA256      string
}

// Backend is the deploy-backend abstraction.
//
// Activate (proxy switch) is NOT a backend op — it's a launcher-internal
// proxy change (see proxy package).
type Backend interface {
	// Name identifies the backend ("compose" | "systemd").
	Name() string

	// Stage brings up a new (green) instance without affecting the
	// currently-active (blue) instance. Returns the green instance's
	// local address (e.g. "127.0.0.1:8783").
	Stage(ctx context.Context, rel Release) (greenAddr string, err error)

	// Health probes the instance at addr (GET /healthz).
	Health(ctx context.Context, addr string) error

	// Drain signals the instance at addr to stop gracefully and waits
	// for it to finish in-flight work.
	Drain(ctx context.Context, addr string) error

	// Remove stops and cleans up the instance permanently.
	Remove(ctx context.Context, addr string) error
}
