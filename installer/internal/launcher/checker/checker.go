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
	// CurrentVersion returns the live current version. Use a provider so
	// the checker re-checks against the post-Apply version within one
	// daemon lifetime (audit C9). If nil, treated as always "".
	CurrentVersion func() string
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

	// trigger is pulsed by CheckNow to force an immediate tick outside
	// the regular interval (audit I1: the UI's "立即检查" button was a
	// no-op before; now it triggers a real poll). Guarded by triggerMu
	// (created lazily on first Start so pre-Start CheckNow doesn't block).
	triggerMu sync.Mutex
	trigger   chan struct{}
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
	c.triggerMu.Lock()
	c.trigger = make(chan struct{}, 1)
	c.triggerMu.Unlock()
	c.wg.Add(1)
	go c.loop(ctx)
}

// Stop waits for the loop to exit.
func (c *Checker) Stop() { c.wg.Wait() }

// CheckNow requests an immediate poll (non-blocking; a check already in
// flight is not duplicated). Returns false if the loop isn't running.
func (c *Checker) CheckNow() bool {
	c.triggerMu.Lock()
	ch := c.trigger
	c.triggerMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- struct{}{}:
		return true
	default:
		return true // one already pending; that's fine
	}
}

func (c *Checker) loop(ctx context.Context) {
	defer c.wg.Done()
	c.triggerMu.Lock()
	ch := c.trigger
	c.triggerMu.Unlock()
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
		case <-ch:
			// Manual trigger: tick now and reset the interval timer so the
			// next regular tick is a full interval away (no hammering).
			c.tick(ctx)
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(c.cfg.Interval)
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
	curVer := ""
	if c.cfg.CurrentVersion != nil {
		curVer = c.cfg.CurrentVersion()
	}
	if rel.Version == curVer {
		return // already on this version
	}
	slog.Info("checker found new version", "current", curVer, "new", rel.Version)
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