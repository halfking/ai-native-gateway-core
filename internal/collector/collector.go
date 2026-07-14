package collector

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Reporter sends validated runtime metrics to the authority ingest endpoint.
type Reporter interface {
	Report(ctx context.Context, payload []byte) error
}

// Collector orchestrates opt-in guarded periodic collection.
type Collector struct {
	cfg         Config
	pref        PrefReader
	traffic     TrafficReader
	dbSize      DBSizeReader
	licenseInfo LicenseInfoReader
	reporter    Reporter
}

// NewCollector wires dependencies for periodic runtime collection.
func NewCollector(cfg Config, pref PrefReader, traffic TrafficReader, dbSize DBSizeReader, licenseInfo LicenseInfoReader, reporter Reporter) *Collector {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	return &Collector{
		cfg:         cfg,
		pref:        pref,
		traffic:     traffic,
		dbSize:      dbSize,
		licenseInfo: licenseInfo,
		reporter:    reporter,
	}
}

// Run blocks until ctx is cancelled, collecting on each interval tick.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.collectOnce(ctx); err != nil {
				slog.Warn("runtime collect failed", "error", err)
			}
		}
	}
}

func (c *Collector) collectOnce(ctx context.Context) error {
	if c.pref != nil {
		enabled, err := c.pref.Enabled(ctx)
		if err != nil {
			return err
		}
		if !enabled {
			return nil
		}
	}

	var traffic TrafficSnapshot
	if c.traffic != nil {
		snap, err := c.traffic.Snapshot(ctx)
		if err != nil {
			return err
		}
		traffic = snap
	}

	var dbSize int64
	if c.dbSize != nil {
		size, err := c.dbSize.DatabaseSizeMB(ctx)
		if err == nil {
			dbSize = size
		}
	}

	licenseType := ""
	expiresIn := 0
	if c.licenseInfo != nil {
		if t, err := c.licenseInfo.LicenseType(ctx); err == nil {
			licenseType = t
		}
		if days, err := c.licenseInfo.ExpiresInDays(ctx); err == nil {
			expiresIn = days
		}
	}

	metrics := BuildRuntimeMetrics(c.cfg, traffic, dbSize, nil, licenseType, expiresIn)
	body, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	if err := ValidatePayload(body); err != nil {
		return err
	}
	if c.reporter == nil {
		return nil
	}
	return c.reporter.Report(ctx, body)
}
