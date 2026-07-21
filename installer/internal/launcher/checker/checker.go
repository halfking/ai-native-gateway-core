// Package checker periodically polls the master for new Gateway versions.
// On new version found, fires OnUpdate callback. NEVER auto-prepares or
// auto-applies — that's the human gate.
//
// Loop pattern mirrors plugin-runtime/health_loop.go.
package checker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// FoundRelease describes a new version discovered by the checker.
type FoundRelease struct {
	Version     string
	DownloadURL string
	SHA256      string
	Changelog   string
	Image       string // compose image tag (optional, set by master)
}

// Source abstracts the version-discovery mechanism (real impl: MasterHTTPSource).
type Source interface {
	Check(ctx context.Context) (*FoundRelease, error)
}

// Config configures the checker loop.
type Config struct {
	CurrentVersion string
	MasterURL      string
	Channel        string
	Interval       time.Duration // default 1h
	Jitter         time.Duration // default Interval/4
	Source         Source        // if nil, masterSource stub
	OnUpdate       func(*FoundRelease)
}

// Checker runs the periodic poll loop.
type Checker struct {
	cfg Config
	wg  sync.WaitGroup
}

func New(cfg Config) *Checker {
	if cfg.Interval == 0 {
		cfg.Interval = 1 * time.Hour
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = cfg.Interval / 4
	}
	return &Checker{cfg: cfg}
}

// Start launches the background poll loop. Cancel ctx to stop.
func (c *Checker) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.loop(ctx)
}

// Stop waits for the loop to exit.
func (c *Checker) Stop() { c.wg.Wait() }

func (c *Checker) loop(ctx context.Context) {
	defer c.wg.Done()
	// Wait first interval before initial tick (avoid hammering master at startup).
	t := time.NewTimer(c.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx)
			t.Reset(c.cfg.Interval + jitter(c.cfg.Jitter))
		}
	}
}

func (c *Checker) tick(ctx context.Context) {
	src := c.cfg.Source
	if src == nil {
		// Default stub returns nil — daemon (Task 10) provides real Source.
		return
	}
	rel, err := src.Check(ctx)
	if err != nil {
		slog.Debug("checker source error (will retry)", "err", err)
		return
	}
	if rel == nil {
		return // no update
	}
	if rel.Version == c.cfg.CurrentVersion {
		return // already on this version
	}
	slog.Info("checker found new version", "current", c.cfg.CurrentVersion, "new", rel.Version)
	if c.cfg.OnUpdate != nil {
		c.cfg.OnUpdate(rel)
	}
}

// jitter returns a random duration in [-max/2, +max/2].
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(time.Now().UnixNano()%int64(max)) - max/2
}