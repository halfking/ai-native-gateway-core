// Package bg — Provider Profile Background Workers
//
// This file contains background workers for the provider profile system:
//   - ProfileCollector: runs lightweight metric collection every 2 hours
//   - ProfileAggregator: runs daily profile aggregation every day at 4am
//   - ProfileCleaner: runs old metrics cleanup every Sunday at 5am
//
// Integration:
//  1. Call InitProviderProfile(db) in cmd/gateway/main.go after dbConn is ready
//  2. Call collector.Start(), aggregator.Start(), cleaner.Start() to begin workers
//  3. Call Stop() on each worker during graceful shutdown
//
// Design:
//   - Each worker runs in its own goroutine with a ticker
//   - Failures are logged but don't crash the process
//   - Workers can be stopped gracefully via Stop()
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ProfileCollector runs lightweight metric collection periodically
type ProfileCollector struct {
	collector *providerprofile.LightweightCollector
	interval  time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewProfileCollector creates a new profile collector.
//
// fernetKey/keyring decrypt credentials.secret_ciphertext for the network
// latency probe (GET /v1/models). These are the same keys derived in
// cmd/gateway/main.go via secret.FernetKeyFromSecret / secret.KeyringFromEnv
// and passed through initProviderProfile.
func NewProfileCollector(db *pgxpool.Pool, interval time.Duration, fernetKey []byte, keyring *secret.Keyring) *ProfileCollector {
	metricsStore := providerprofile.NewPGMetricsStore(db)
	networkProber := providerprofile.NewGatewayNetworkProber(db, fernetKey, keyring)
	requestAnalyzer := providerprofile.NewGatewayRequestAnalyzer(db)
	scaleProvider := providerprofile.NewGatewayScaleProvider(db)
	credentialLister := providerprofile.NewGatewayCredentialLister(db)

	collector := providerprofile.NewLightweightCollector(
		metricsStore,
		networkProber,
		requestAnalyzer,
		scaleProvider,
		credentialLister,
	)

	return &ProfileCollector{
		collector: collector,
		interval:  interval,
		done:      make(chan struct{}),
	}
}

// Start begins the collection loop
func (c *ProfileCollector) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	go c.run(ctx)
	slog.Info("provider profile collector started", "interval", c.interval)
}

// Stop gracefully stops the collector
func (c *ProfileCollector) Stop() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		slog.Info("provider profile collector stopped")
	}
}

func (c *ProfileCollector) run(ctx context.Context) {
	defer close(c.done)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Run once immediately on start
	c.collect(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

func (c *ProfileCollector) collect(ctx context.Context) {
	collectCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := c.collector.CollectMetrics(collectCtx); err != nil {
		slog.Error("provider profile collection failed", "error", err)
		return
	}

	slog.Info("provider profile collection completed")
}

// ProfileAggregator runs daily profile aggregation
type ProfileAggregator struct {
	aggregator *providerprofile.DailyAggregator
	interval   time.Duration
	cancel     context.CancelFunc
	done       chan struct{}
}

// NewProfileAggregator creates a new profile aggregator
func NewProfileAggregator(db *pgxpool.Pool, interval time.Duration) *ProfileAggregator {
	metricsStore := providerprofile.NewPGMetricsStore(db)
	profileStore := providerprofile.NewPGProfileStore(db)
	scorer := providerprofile.NewDefaultScorer()
	weights := providerprofile.DefaultWeights()
	credentialLister := providerprofile.NewGatewayCredentialLister(db)

	aggregator := providerprofile.NewDailyAggregator(
		metricsStore,
		profileStore,
		scorer,
		weights,
		credentialLister,
	)

	return &ProfileAggregator{
		aggregator: aggregator,
		interval:   interval,
		done:       make(chan struct{}),
	}
}

// Start begins the aggregation loop
func (a *ProfileAggregator) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	go a.run(ctx)
	slog.Info("provider profile aggregator started", "interval", a.interval)
}

// Stop gracefully stops the aggregator
func (a *ProfileAggregator) Stop() {
	if a.cancel != nil {
		a.cancel()
		<-a.done
		slog.Info("provider profile aggregator stopped")
	}
}

func (a *ProfileAggregator) run(ctx context.Context) {
	defer close(a.done)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	// Run once on start for TODAY so a freshly-enabled system populates
	// provider_profile_daily without waiting a full day. Without this, the table
	// stays empty until the first 24h ticker fires (aggregating yesterday),
	// which means the alert engine has no data to evaluate on day 1.
	a.aggregateAt(ctx, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.aggregate(ctx) // normal daily cadence: aggregate yesterday
		}
	}
}

// aggregateAt aggregates the daily profiles for a specific date.
func (a *ProfileAggregator) aggregateAt(ctx context.Context, date time.Time) {
	aggregateCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := a.aggregator.AggregateDailyProfiles(aggregateCtx, date); err != nil {
		slog.Error("provider profile aggregation failed", "error", err, "date", date.Format("2006-01-02"))
		return
	}

	slog.Info("provider profile aggregation completed", "date", date.Format("2006-01-02"))
}

func (a *ProfileAggregator) aggregate(ctx context.Context) {
	// Aggregate yesterday's data on each tick (normal daily cadence).
	a.aggregateAt(ctx, time.Now().AddDate(0, 0, -1))
}

// ProfileCleaner runs old metrics cleanup
type ProfileCleaner struct {
	metricsStore *providerprofile.PGMetricsStore
	interval     time.Duration
	cancel       context.CancelFunc
	done         chan struct{}
}

// NewProfileCleaner creates a new profile cleaner
func NewProfileCleaner(db *pgxpool.Pool, interval time.Duration) *ProfileCleaner {
	return &ProfileCleaner{
		metricsStore: providerprofile.NewPGMetricsStore(db),
		interval:     interval,
		done:         make(chan struct{}),
	}
}

// Start begins the cleanup loop
func (c *ProfileCleaner) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	go c.run(ctx)
	slog.Info("provider profile cleaner started", "interval", c.interval)
}

// Stop gracefully stops the cleaner
func (c *ProfileCleaner) Stop() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		slog.Info("provider profile cleaner stopped")
	}
}

func (c *ProfileCleaner) run(ctx context.Context) {
	defer close(c.done)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cleanup(ctx)
		}
	}
}

func (c *ProfileCleaner) cleanup(ctx context.Context) {
	cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	deleted, err := c.metricsStore.CleanupOldMetrics(cleanupCtx)
	if err != nil {
		slog.Error("provider profile cleanup failed", "error", err)
		return
	}

	slog.Info("provider profile cleanup completed", "deleted_rows", deleted)
}

// ProfileAlertWorker runs daily profile alert evaluation after the aggregator.
// Profile alerts are advisory only; model-level probe state owns automatic
// binding degradation and recovery.
type ProfileAlertWorker struct {
	engine   *providerprofile.AlertEngine
	lister   *providerprofile.GatewayCredentialLister
	db       *pgxpool.Pool
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// Engine returns the underlying AlertEngine so callers (e.g. main.go) can
// inject an alert handler via SetAlertHandler after construction. Returns nil
// before the worker is constructed.
func (w *ProfileAlertWorker) Engine() *providerprofile.AlertEngine {
	if w == nil {
		return nil
	}
	return w.engine
}

// NewProfileAlertWorker creates the alert worker.
func NewProfileAlertWorker(db *pgxpool.Pool, interval time.Duration) *ProfileAlertWorker {
	profileStore := providerprofile.NewPGProfileStore(db)
	profileSource := providerprofile.NewPGProfileSource(profileStore)
	alertStore := providerprofile.NewPGAlertStore(db)
	actor := providerprofile.NewPGCredentialActor(db)
	lister := providerprofile.NewGatewayCredentialLister(db)
	engine := providerprofile.NewAlertEngine(profileSource, alertStore, actor, providerprofile.DefaultAlertConfig())
	return &ProfileAlertWorker{engine: engine, lister: lister, db: db, interval: interval, done: make(chan struct{})}
}

// Start begins the alert loop.
func (w *ProfileAlertWorker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.run(ctx)
	slog.Info("provider profile alert worker started", "interval", w.interval)
}

// Stop gracefully stops.
func (w *ProfileAlertWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		slog.Info("provider profile alert worker stopped")
	}
}

func (w *ProfileAlertWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Delay the first run a few minutes so the aggregator's start-of-day run
	// has a chance to populate provider_profile_daily before we evaluate.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	w.evaluate(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.evaluate(ctx)
		}
	}
}

func (w *ProfileAlertWorker) evaluate(ctx context.Context) {
	evalCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Active credentials — candidates for auto-disable.
	activeIDs, err := w.lister.ListActiveCredentials(evalCtx)
	if err != nil {
		slog.Error("provider profile alert: list active credentials failed", "error", err)
		return
	}
	// Currently auto-disabled credentials — candidates for auto-enable (recovery).
	// These are NOT in ListActiveCredentials (which filters lifecycle_status='active').
	disabledIDs, err := w.listAutoDisabledCredentials(evalCtx)
	if err != nil {
		slog.Warn("provider profile alert: list auto-disabled credentials failed", "error", err)
	} else {
		activeIDs = append(activeIDs, disabledIDs...)
	}

	var disabled, enabled, alerted int
	for _, credID := range activeIDs {
		res, err := w.engine.EvaluateCredential(evalCtx, credID)
		if err != nil {
			slog.Warn("provider profile alert: evaluate failed", "credential_id", credID, "error", err)
			continue
		}
		switch res.Action {
		case "disabled":
			disabled++
		case "enabled":
			enabled++
		}
		alerted += len(res.Alerts)
	}
	slog.Info("provider profile alert evaluation completed",
		"credentials_evaluated", len(activeIDs), "disabled", disabled, "enabled", enabled, "alerts_emitted", alerted)
}

// listAutoDisabledCredentials returns legacy credential ids that were disabled
// by the retired credential-level profile action. They remain visible for
// advisory reporting, but this worker no longer changes their lifecycle.
func (w *ProfileAlertWorker) listAutoDisabledCredentials(ctx context.Context) ([]int64, error) {
	rows, err := w.db.Query(ctx, `
		SELECT id FROM credentials
		WHERE lifecycle_status = 'disabled'
		  AND manual_disabled = false
		  AND auto_disabled_at IS NOT NULL
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query auto-disabled credentials: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan credential id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
