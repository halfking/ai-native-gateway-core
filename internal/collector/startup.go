package collector

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/licensing"
)

// StartupConfig wires the runtime collector on a customer gateway node.
type StartupConfig struct {
	Pool         *pgxpool.Pool
	DataDir      string
	AuthorityURL string
	Version      string
	StartTime    time.Time
	Interval     time.Duration
}

// MaybeStart launches the runtime collector when authority URL and instance token exist.
func MaybeStart(ctx context.Context, cfg StartupConfig) {
	if cfg.Pool == nil {
		return
	}
	authorityURL := strings.TrimRight(strings.TrimSpace(cfg.AuthorityURL), "/")
	if authorityURL == "" {
		slog.Info("runtime collector disabled (LICENSE_AUTHORITY_URL not set)")
		return
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/kx-gateway"
	}
	tokenPath := filepath.Join(dataDir, "instance.token")
	token, err := ReadCredentialFile(tokenPath)
	if err != nil || token == "" {
		slog.Info("runtime collector disabled (instance token missing)", "path", tokenPath)
		return
	}
	instanceID, err := InstanceIDFromToken(token)
	if err != nil || instanceID == "" {
		slog.Warn("runtime collector disabled (unable to parse instance token)", "error", err)
		return
	}

	licenseKeyHash := os.Getenv("KX_LICENSE_KEY_HASH")
	if licenseKeyHash == "" {
		licenseKeyHash = HashLicenseKey(os.Getenv("KX_LICENSE_KEY"))
	}

	startedAt := cfg.StartTime
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	version := cfg.Version
	if version == "" {
		version = "dev"
	}

	prefStore := licensing.NewPgxStore(cfg.Pool)
	fingerprintFn := func() (string, error) {
		fp, fpErr := licensing.GenerateFingerprint()
		if fpErr != nil {
			return "", fpErr
		}
		return fp.Hash(), nil
	}

	// 三合一增强: 双写 Reporter (Authority + 本地 PG)
	httpReporter := &HTTPReporter{ReportURL: authorityURL + "/api/v1/collect/runtime", Token: token}
	localReporter := &LocalDBReporter{
		Pool:       cfg.Pool,
		InstanceID: instanceID,
	}
	multiReporter := NewMultiReporter(httpReporter, localReporter)

	c := NewCollector(Config{
		InstanceID:     instanceID,
		Version:        version,
		LicenseKeyHash: licenseKeyHash,
		StartTime:      startedAt,
		Interval:       interval,
		ReportURL:      authorityURL + "/api/v1/collect/runtime",
	}, &PgTelemetryPrefReader{Store: prefStore, Hash: fingerprintFn},
		&PgTrafficReader{Pool: cfg.Pool},
		&PgDBSizeReader{Pool: cfg.Pool},
		&PgLicenseInfoReader{Store: prefStore, Hash: fingerprintFn},
		multiReporter,
	)

	go c.Run(ctx)
	slog.Info("runtime collector started",
		"instance_id", instanceID,
		"interval", interval,
		"report_url", authorityURL+"/api/v1/collect/runtime",
	)
}
