package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

// RuntimeMetrics is the allowlisted operational aggregate payload.
type RuntimeMetrics struct {
	InstanceID     string    `json:"instance_id"`
	Version        string    `json:"version"`
	LicenseKeyHash string    `json:"license_key_hash"`
	Timestamp      time.Time `json:"timestamp"`

	UptimeSecs  int64   `json:"uptime_secs"`
	CPUUsagePct float64 `json:"cpu_usage_pct"`
	MemUsedMB   int64   `json:"mem_used_mb"`
	MemTotalMB  int64   `json:"mem_total_mb"`
	DiskUsedGB  int64   `json:"disk_used_gb"`
	DiskTotalGB int64   `json:"disk_total_gb"`
	DBSizeMB    int64   `json:"db_size_mb"`

	CurrentConcurrency int     `json:"current_concurrency"`
	Last5MinTPS        float64 `json:"last_5min_tps"`
	Last5MinP50Ms      float64 `json:"last_5min_p50_ms"`
	Last5MinP99Ms      float64 `json:"last_5min_p99_ms"`
	Last5MinSuccessPct float64 `json:"last_5min_success_pct"`

	ModelUsage  map[string]int64 `json:"model_usage,omitempty"`
	TenantCount int              `json:"tenant_count"`

	LicenseType          string `json:"license_type,omitempty"`
	LicenseExpiresInDays int    `json:"license_expires_in_days,omitempty"`
}

// TrafficSnapshot carries aggregated request metrics without request bodies.
type TrafficSnapshot struct {
	CurrentConcurrency int
	Last5MinTPS        float64
	Last5MinP50Ms      float64
	Last5MinP99Ms      float64
	Last5MinSuccessPct float64
	ModelUsage         map[string]int64
	TenantCount        int
}

// TrafficReader supplies aggregated traffic metrics.
type TrafficReader interface {
	Snapshot(ctx context.Context) (TrafficSnapshot, error)
}

// PrefReader returns whether the customer opted in to runtime telemetry.
type PrefReader interface {
	Enabled(ctx context.Context) (bool, error)
}

// DiskUsageReader optionally supplies disk usage without pulling in extra deps.
type DiskUsageReader interface {
	DiskUsageGB(ctx context.Context) (usedGB, totalGB int64, err error)
}

// DBSizeReader returns database size in megabytes without reading content.
type DBSizeReader interface {
	DatabaseSizeMB(ctx context.Context) (int64, error)
}

// LicenseInfoReader supplies non-sensitive license metadata.
type LicenseInfoReader interface {
	LicenseType(ctx context.Context) (string, error)
	ExpiresInDays(ctx context.Context) (int, error)
}

// Config holds collector identity and cadence.
type Config struct {
	InstanceID     string
	Version        string
	LicenseKeyHash string
	StartTime      time.Time
	Interval       time.Duration
	ReportURL      string
}

// HashLicenseKey returns the first 8 bytes of SHA-256 as 16 hex chars.
func HashLicenseKey(licenseKey string) string {
	if licenseKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(licenseKey))
	return hex.EncodeToString(sum[:8])
}

// BuildRuntimeMetrics assembles allowlisted metrics from system and traffic sources.
func BuildRuntimeMetrics(cfg Config, traffic TrafficSnapshot, dbSizeMB int64, disk DiskUsageReader, licenseType string, expiresInDays int) *RuntimeMetrics {
	m := &RuntimeMetrics{
		InstanceID:     cfg.InstanceID,
		Version:        cfg.Version,
		LicenseKeyHash: cfg.LicenseKeyHash,
		Timestamp:      time.Now().UTC(),
		UptimeSecs:     int64(time.Since(cfg.StartTime).Seconds()),
		DBSizeMB:       dbSizeMB,
		CurrentConcurrency: traffic.CurrentConcurrency,
		Last5MinTPS:        sanitizeFloat(traffic.Last5MinTPS),
		Last5MinP50Ms:      sanitizeFloat(traffic.Last5MinP50Ms),
		Last5MinP99Ms:      sanitizeFloat(traffic.Last5MinP99Ms),
		Last5MinSuccessPct:  sanitizeFloat(traffic.Last5MinSuccessPct),
		ModelUsage:         traffic.ModelUsage,
		TenantCount:        traffic.TenantCount,
		LicenseType:        licenseType,
		LicenseExpiresInDays: expiresInDays,
	}
	applySystemMetrics(m)
	if disk != nil {
		if used, total, err := disk.DiskUsageGB(context.Background()); err == nil {
			m.DiskUsedGB = used
			m.DiskTotalGB = total
		}
	}
	return m
}

func applySystemMetrics(m *RuntimeMetrics) {
	if cpuPct, err := cpu.Percent(0, false); err == nil && len(cpuPct) > 0 {
		m.CPUUsagePct = sanitizeFloat(cpuPct[0])
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		m.MemUsedMB = int64(vm.Used) / 1024 / 1024
		m.MemTotalMB = int64(vm.Total) / 1024 / 1024
	}
}

// sanitizeFloat replaces NaN and Inf with 0.0 to prevent JSON marshal errors.
// json.Marshal accepts NaN/Inf but produces invalid JSON that fails Unmarshal.
func sanitizeFloat(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0.0
	}
	return v
}
