package centeragent

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/opsreporter"
)

// StartupConfig wires per-node registration and heartbeat.
// Default path: HTTP → OPS_COLLECT_URL (https://llm.kxpms.cn).
// Legacy direct-DB path only when OPS_COLLECT_DIRECT_DB=1.
type StartupConfig struct {
	Pool         *pgxpool.Pool
	Version      string
	BuildSeq     int
	StartTime    time.Time
	Region       string
	InstanceID   string
	DataDir      string
	AuthorityURL string
	Interval     time.Duration
}

// MaybeStart begins ops telemetry. HTTP API aggregation is the default.
func MaybeStart(ctx context.Context, cfg StartupConfig) {
	if strings.EqualFold(os.Getenv("OPS_CENTER_AGENT_DISABLED"), "1") ||
		strings.EqualFold(os.Getenv("OPS_CENTER_AGENT_DISABLED"), "true") {
		slog.Info("center agent disabled (OPS_CENTER_AGENT_DISABLED)")
		return
	}

	if directDBEnabled() {
		maybeStartDirectDB(ctx, cfg)
		return
	}

	opsreporter.MaybeStart(ctx, opsreporter.Config{
		CollectURL: firstNonEmpty(cfg.AuthorityURL, os.Getenv("OPS_COLLECT_URL")),
		Region:     cfg.Region,
		Version:    cfg.Version,
		BuildSeq:   cfg.BuildSeq,
		StartTime:  cfg.StartTime,
		InstanceID: cfg.InstanceID,
		DataDir:    cfg.DataDir,
		Interval:   cfg.Interval,
		LicenseKey: os.Getenv("OPS_COLLECT_LICENSE_KEY"),
		AdminUser:  os.Getenv("LLM_GATEWAY_ADMIN_USER"),
		AdminEmail: os.Getenv("LLM_GATEWAY_ADMIN_EMAIL"),
	})
}

func directDBEnabled() bool {
	v := strings.TrimSpace(os.Getenv("OPS_COLLECT_DIRECT_DB"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
