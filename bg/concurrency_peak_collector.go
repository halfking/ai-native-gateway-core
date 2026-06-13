// Package bg provides background workers for llm-gateway-go.
package bg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// credModelPeakKey is the composite key for tracking peak concurrency
// per credential-model pair.
type credModelPeakKey struct {
	CredID int64
	Model  string
}

// peakCounter holds the live current count and the in-window peak value
// for a single credential-model pair.
type peakCounter struct {
	current atomic.Int64
	peak    atomic.Int64
}

// peakSample aggregates peak statistics between two DB flushes.
type peakSample struct {
	peak       int64
	sumCurrent int64
	samples    int64
}

// ConcurrencyPeakCollector samples live concurrency per credential-model
// pair and writes per-minute peak/avg values to credential_model_peak_1m.
//
// Sampling interval: 30s (collects current value)
// Flushing interval: 60s (writes to DB)
// Retention: managed by TimescaleDB retention policy on credential_model_peak_1m.
type ConcurrencyPeakCollector struct {
	db *pgxpool.Pool

	// counters is the live in-memory store of (current, peak) per key.
	counters sync.Map // map[credModelPeakKey]*peakCounter

	// mu protects pending only.
	mu      sync.Mutex
	pending map[credModelPeakKey]*peakSample

	cancel context.CancelFunc
	done   chan struct{}
}

// NewConcurrencyPeakCollector creates a new collector.
func NewConcurrencyPeakCollector(db *pgxpool.Pool) *ConcurrencyPeakCollector {
	return &ConcurrencyPeakCollector{
		db:      db,
		pending: make(map[credModelPeakKey]*peakSample),
		done:    make(chan struct{}),
	}
}

// Start spawns the background sampling and flushing goroutine.
func (c *ConcurrencyPeakCollector) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	go c.run(cctx)
	slog.Info("concurrency peak collector started",
		"sample_interval", "30s",
		"flush_interval", "60s",
	)
}

// Stop terminates the background goroutine and waits for it to finish.
func (c *ConcurrencyPeakCollector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

// Acquire records that one concurrent request has started for the given
// credential-model pair. Safe to call concurrently.
func (c *ConcurrencyPeakCollector) Acquire(credID int64, model string) {
	if c == nil {
		return
	}
	key := credModelPeakKey{CredID: credID, Model: model}
	v, _ := c.counters.LoadOrStore(key, &peakCounter{})
	counter := v.(*peakCounter)
	cur := counter.current.Add(1)
	// CAS-loop to update peak if cur is higher.
	for {
		old := counter.peak.Load()
		if cur <= old {
			return
		}
		if counter.peak.CompareAndSwap(old, cur) {
			return
		}
	}
}

// Release records that one concurrent request has finished for the given
// credential-model pair. Safe to call concurrently.
func (c *ConcurrencyPeakCollector) Release(credID int64, model string) {
	if c == nil {
		return
	}
	key := credModelPeakKey{CredID: credID, Model: model}
	if v, ok := c.counters.Load(key); ok {
		v.(*peakCounter).current.Add(-1)
	}
}

// Record records a sampled current value into the pending buffer.
// Called by the background sampler every 30s.
func (c *ConcurrencyPeakCollector) Record(credID int64, model string, current int64) {
	if c == nil {
		return
	}
	key := credModelPeakKey{CredID: credID, Model: model}

	c.mu.Lock()
	defer c.mu.Unlock()
	sample, ok := c.pending[key]
	if !ok {
		sample = &peakSample{}
		c.pending[key] = sample
	}
	sample.sumCurrent += current
	sample.samples++
	if current > sample.peak {
		sample.peak = current
	}
}

// drainPending returns and clears the pending buffer.
func (c *ConcurrencyPeakCollector) drainPending() map[credModelPeakKey]*peakSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	peaks := c.pending
	c.pending = make(map[credModelPeakKey]*peakSample)
	return peaks
}

// run is the background loop: sample every 30s, flush every 60s.
func (c *ConcurrencyPeakCollector) run(ctx context.Context) {
	defer close(c.done)
	sampleTicker := time.NewTicker(30 * time.Second)
	flushTicker := time.NewTicker(60 * time.Second)
	defer sampleTicker.Stop()
	defer flushTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Best-effort final flush.
			c.flush(context.Background())
			return
		case <-sampleTicker.C:
			c.sample()
		case <-flushTicker.C:
			c.flush(ctx)
		}
	}
}

// sample walks all live counters and records their current value into pending.
func (c *ConcurrencyPeakCollector) sample() {
	c.counters.Range(func(key, value any) bool {
		k := key.(credModelPeakKey)
		counter := value.(*peakCounter)
		current := counter.current.Load()
		c.Record(k.CredID, k.Model, current)
		// Reset peak for next sampling window. The peak for the previous
		// window is already captured in pending.
		counter.peak.Store(current)
		return true
	})
}

// flush writes accumulated samples to the database.
func (c *ConcurrencyPeakCollector) flush(ctx context.Context) {
	peaks := c.drainPending()
	if len(peaks) == 0 {
		return
	}

	bucket := time.Now().UTC().Truncate(time.Minute)

	// Batch INSERT with ON CONFLICT.
	// We use a transaction so all rows commit or none.
	tx, err := c.db.Begin(ctx)
	if err != nil {
		slog.Error("peak flush: begin tx failed", "error", err, "count", len(peaks))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows := 0
	for key, sample := range peaks {
		avg := 0.0
		if sample.samples > 0 {
			avg = float64(sample.sumCurrent) / float64(sample.samples)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO credential_model_peak_1m
				(bucket, credential_id, raw_model, peak_concurrent, avg_concurrent, sample_count)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (bucket, credential_id, raw_model) DO UPDATE SET
				peak_concurrent = GREATEST(credential_model_peak_1m.peak_concurrent, EXCLUDED.peak_concurrent),
				avg_concurrent  = (credential_model_peak_1m.avg_concurrent + EXCLUDED.avg_concurrent) / 2.0,
				sample_count    = credential_model_peak_1m.sample_count + EXCLUDED.sample_count
		`, bucket, key.CredID, key.Model, sample.peak, avg, sample.samples)
		if err != nil {
			slog.Error("peak flush: insert failed", "error", err,
				"credential_id", key.CredID, "model", key.Model)
			continue
		}
		rows++
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("peak flush: commit failed", "error", err, "rows", rows)
		return
	}
	slog.Debug("peak samples flushed",
		"count", rows, "bucket", bucket.Format(time.RFC3339),
	)
}

// Stats returns a snapshot of collector statistics (for admin API).
func (c *ConcurrencyPeakCollector) Stats() map[string]interface{} {
	if c == nil {
		return nil
	}
	activeKeys := 0
	c.counters.Range(func(_, _ any) bool {
		activeKeys++
		return true
	})
	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	return map[string]interface{}{
		"active_keys":     activeKeys,
		"pending_samples": pending,
		"sample_interval": "30s",
		"flush_interval":  "60s",
	}
}

// GetLiveConcurrent returns the current concurrency for a specific
// credential-model pair. Returns 0 if not tracked.
func (c *ConcurrencyPeakCollector) GetLiveConcurrent(credID int64, model string) int64 {
	if c == nil {
		return 0
	}
	key := credModelPeakKey{CredID: credID, Model: model}
	if v, ok := c.counters.Load(key); ok {
		return v.(*peakCounter).current.Load()
	}
	return 0
}
