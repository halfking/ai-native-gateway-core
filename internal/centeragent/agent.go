package centeragent

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/center"
)

// StartupConfig wires per-node registration and heartbeat into the shared
// gateway_instances table (252 data plane). All canonical nodes (local,
// 245, 154) that share LLM_GATEWAY_DATABASE_URL will appear in ops overview.
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

// MaybeStart registers this gateway node and begins periodic heartbeats.
func MaybeStart(ctx context.Context, cfg StartupConfig) {
	if cfg.Pool == nil {
		return
	}
	if strings.EqualFold(os.Getenv("OPS_CENTER_AGENT_DISABLED"), "1") ||
		strings.EqualFold(os.Getenv("OPS_CENTER_AGENT_DISABLED"), "true") {
		slog.Info("center agent disabled (OPS_CENTER_AGENT_DISABLED)")
		return
	}

	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = strings.TrimSpace(os.Getenv("OPS_NODE_REGION"))
	}
	if region == "" {
		region = "unknown"
	}

	instanceID, err := resolveInstanceID(cfg.InstanceID, dataDir)
	if err != nil {
		slog.Warn("center agent disabled (instance id)", "error", err)
		return
	}

	version := strings.TrimSpace(cfg.Version)
	if version == "" {
		version = "dev"
	}
	startedAt := cfg.StartTime
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	buildSeq := cfg.BuildSeq
	if buildSeq <= 0 {
		buildSeq = 1
	}

	hostname, _ := os.Hostname()
	ip := firstNonLoopbackIP()

	store := center.NewPgxStore(cfg.Pool)
	instance := &center.InstanceInfo{
		InstanceID:   instanceID,
		Hostname:     hostname,
		IPAddress:    ip,
		Region:       region,
		Version:      version,
		BuildSeq:     buildSeq,
		Status:       center.StatusOnline,
		StartedAt:    startedAt,
		InstanceType: "gateway",
		DeploymentID: region,
	}

	if err := store.RegisterInstance(ctx, instance); err != nil {
		slog.Error("center agent register failed", "error", err, "instance_id", instanceID)
		return
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}

	agent := &Agent{
		store:      store,
		instanceID: instanceID,
		startedAt:  startedAt,
		interval:   interval,
	}

	go agent.run(ctx)
	slog.Info("center agent started",
		"instance_id", instanceID,
		"region", region,
		"hostname", hostname,
		"interval", interval,
	)
}

// Agent emits local DB heartbeats for ops visibility.
type Agent struct {
	store      *center.PgxStore
	instanceID string
	startedAt  time.Time
	interval   time.Duration
}

func (a *Agent) run(ctx context.Context) {
	a.beat(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("center agent stopped", "instance_id", a.instanceID)
			return
		case <-ticker.C:
			a.beat(ctx)
		}
	}
}

func (a *Agent) beat(ctx context.Context) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	payload := &center.HeartbeatPayload{
		UptimeSecs:   int64(time.Since(a.startedAt).Seconds()),
		GoVersion:    runtime.Version(),
		NumGoroutine: runtime.NumGoroutine(),
		AllocMB:      float64(mem.Alloc) / (1024 * 1024),
		TotalAllocMB: float64(mem.TotalAlloc) / (1024 * 1024),
		SysMB:        float64(mem.Sys) / (1024 * 1024),
		CPUCores:     runtime.NumCPU(),
	}

	beatCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := a.store.RecordHeartbeat(beatCtx, a.instanceID, payload); err != nil {
		slog.Warn("center agent heartbeat failed", "error", err, "instance_id", a.instanceID)
	}
}

func resolveInstanceID(explicit, dataDir string) (string, error) {
	if id := strings.TrimSpace(explicit); id != "" {
		return id, nil
	}
	if id := strings.TrimSpace(os.Getenv("OPS_INSTANCE_ID")); id != "" {
		return id, nil
	}
	return getOrCreateInstanceID(dataDir)
}

func defaultDataDir() string {
	if v := strings.TrimSpace(os.Getenv("OPS_DATA_DIR")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "kx-gateway")
	}
	return "/var/lib/kx-gateway"
}

func getOrCreateInstanceID(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "instance.id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	}
	id := uuid.NewString()
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func firstNonLoopbackIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			return ip.String()
		}
	}
	return ""
}
