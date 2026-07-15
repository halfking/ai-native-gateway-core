package opsreporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/center"
)

const (
	defaultCollectURL = "https://llm.kxpms.cn"
	defaultInterval   = 60 * time.Second
)

// Config wires HTTP-based ops telemetry to the central collector (llm.kxpms.cn).
type Config struct {
	CollectURL   string
	Region       string
	Version      string
	BuildSeq     int
	InstanceID   string
	DataDir      string
	LicenseKey   string
	AdminUser    string
	AdminEmail   string
	StartTime    time.Time
	Interval     time.Duration
	HTTPClient   *http.Client
}

// MaybeStart registers this node via central API and emits periodic heartbeats.
func MaybeStart(ctx context.Context, cfg Config) {
	if strings.EqualFold(os.Getenv("OPS_CENTER_AGENT_DISABLED"), "1") ||
		strings.EqualFold(os.Getenv("OPS_CENTER_AGENT_DISABLED"), "true") {
		slog.Info("ops reporter disabled (OPS_CENTER_AGENT_DISABLED)")
		return
	}

	collectURL := resolveCollectURL(cfg.CollectURL)
	if collectURL == "" {
		slog.Info("ops reporter disabled (OPS_COLLECT_URL unset)")
		return
	}

	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = strings.TrimSpace(os.Getenv("OPS_NODE_REGION"))
	}
	if region == "" {
		region = "unknown"
	}

	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	instanceID, err := resolveInstanceID(cfg.InstanceID, dataDir)
	if err != nil {
		slog.Warn("ops reporter disabled (instance id)", "error", err)
		return
	}

	licenseKey := strings.TrimSpace(cfg.LicenseKey)
	if licenseKey == "" {
		licenseKey = strings.TrimSpace(os.Getenv("OPS_COLLECT_LICENSE_KEY"))
	}
	adminUser := strings.TrimSpace(cfg.AdminUser)
	if adminUser == "" {
		adminUser = strings.TrimSpace(os.Getenv("LLM_GATEWAY_ADMIN_USER"))
	}
	if licenseKey == "" || adminUser == "" {
		slog.Warn("ops reporter disabled (OPS_COLLECT_LICENSE_KEY + admin user required for registration)")
		return
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	r := &Reporter{
		baseURL:    strings.TrimRight(collectURL, "/"),
		region:     region,
		version:    cfg.Version,
		buildSeq:   cfg.BuildSeq,
		instanceID: instanceID,
		dataDir:    dataDir,
		licenseKey: licenseKey,
		adminUser:  adminUser,
		adminEmail: strings.TrimSpace(cfg.AdminEmail),
		startedAt:  cfg.StartTime,
		interval:   cfg.Interval,
		client:     client,
	}
	if r.startedAt.IsZero() {
		r.startedAt = time.Now()
	}
	if r.interval <= 0 {
		r.interval = defaultInterval
	}

	go r.run(ctx)
	slog.Info("ops reporter started", "collect_url", r.baseURL, "region", region, "instance_id", instanceID)
}

// Reporter sends register + heartbeat to central API.
type Reporter struct {
	baseURL      string
	region       string
	version      string
	buildSeq     int
	instanceID   string
	dataDir      string
	licenseKey   string
	adminUser    string
	adminEmail   string
	startedAt    time.Time
	interval     time.Duration
	client       *http.Client
	instanceToken string
}

func (r *Reporter) run(ctx context.Context) {
	if err := r.ensureRegistered(ctx); err != nil {
		slog.Error("ops reporter register failed", "error", err)
		return
	}
	r.beat(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.beat(ctx); err != nil {
				slog.Warn("ops heartbeat failed", "error", err)
				if regErr := r.ensureRegistered(ctx); regErr != nil {
					slog.Warn("ops re-register failed", "error", regErr)
				}
			}
		}
	}
}

func (r *Reporter) ensureRegistered(ctx context.Context) error {
	if tok := r.readToken(); tok != "" {
		r.instanceToken = tok
		return nil
	}
	body := map[string]any{
		"instance_id": r.instanceID,
		"region":      r.region,
		"hostname":    hostname(),
		"ip_address":  firstIP(),
		"version":     r.version,
		"build_seq":   r.buildSeq,
		"license_key": r.licenseKey,
		"admin_user":  r.adminUser,
		"admin_email": r.adminEmail,
	}
	var resp struct {
		InstanceToken string `json:"instance_token"`
	}
	if err := r.postJSON(ctx, "/api/v1/ops/nodes/register", body, "", &resp); err != nil {
		return err
	}
	if resp.InstanceToken == "" {
		return fmt.Errorf("empty instance_token")
	}
	r.instanceToken = resp.InstanceToken
	return r.writeToken(resp.InstanceToken)
}

func (r *Reporter) beat(ctx context.Context) error {
	if r.instanceToken == "" {
		return fmt.Errorf("not registered")
	}
	payload := center.HeartbeatPayload{
		UptimeSecs: int64(time.Since(r.startedAt).Seconds()),
	}
	return r.postJSON(ctx, "/api/v1/ops/nodes/heartbeat", payload, r.instanceToken, nil)
}

func (r *Reporter) postJSON(ctx context.Context, path string, body any, bearer string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reporter) tokenPath() string {
	return filepath.Join(r.dataDir, "ops.instance.token")
}

func (r *Reporter) readToken() string {
	data, err := os.ReadFile(r.tokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (r *Reporter) writeToken(token string) error {
	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(r.tokenPath(), []byte(token), 0o600)
}

func resolveCollectURL(explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("OPS_COLLECT_URL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("LICENSE_AUTHORITY_URL")); v != "" {
		return v
	}
	return defaultCollectURL
}

func resolveInstanceID(explicit, dataDir string) (string, error) {
	if id := strings.TrimSpace(explicit); id != "" {
		return id, nil
	}
	if id := strings.TrimSpace(os.Getenv("OPS_INSTANCE_ID")); id != "" {
		return id, nil
	}
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

func defaultDataDir() string {
	if v := strings.TrimSpace(os.Getenv("OPS_DATA_DIR")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "kx-gateway")
	}
	return "/var/lib/kx-gateway"
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func firstIP() string {
	// lightweight; ops register also stores hostname for display
	return ""
}
