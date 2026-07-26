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

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.aggregate(ctx)
		}
	}
}

func (a *ProfileAggregator) aggregate(ctx context.Context) {
	aggregateCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Aggregate yesterday's data
	yesterday := time.Now().AddDate(0, 0, -1)

	if err := a.aggregator.AggregateDailyProfiles(aggregateCtx, yesterday); err != nil {
		slog.Error("provider profile aggregation failed", "error", err, "date", yesterday.Format("2006-01-02"))
		return
	}

	slog.Info("provider profile aggregation completed", "date", yesterday.Format("2006-01-02"))
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
