package upgrader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// DeviceProof identifies an activated instance to Maintain's P3 API.
// All three values must be present before an instance-specific request is sent.
type DeviceProof struct {
	InstanceID   string
	LicenseKey   string
	HardwareHash string
}

func (p DeviceProof) valid() bool {
	return strings.TrimSpace(p.InstanceID) != "" &&
		strings.TrimSpace(p.LicenseKey) != "" &&
		strings.TrimSpace(p.HardwareHash) != ""
}

// Client is an HTTP client for Maintain distribution and P3 upgrade APIs.
type Client struct {
	baseURL    string
	httpClient *http.Client
	proof      DeviceProof
}

// NewClient creates a client without instance proof. It can still perform
// anonymous distribution version checks, but cannot obtain policy hints or use P3 task APIs.
func NewClient(baseURL string) *Client {
	return NewClientWithProof(baseURL, DeviceProof{})
}

// NewClientWithProof creates a client that uses proof for instance-specific requests.
func NewClientWithProof(baseURL string, proof DeviceProof) *Client {
	return NewClientWithHTTPClient(baseURL, proof, nil)
}

// NewClientWithHTTPClient creates a proof-aware client using httpClient when supplied.
func NewClientWithHTTPClient(baseURL string, proof DeviceProof, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		proof:      proof,
	}
}

// CheckUpdateResponse is the legacy response shape used by existing callers.
type CheckUpdateResponse struct {
	HasUpdate bool     `json:"has_update"`
	Release   *Release `json:"release,omitempty"`
}

// Release contains the artifact selected for the local platform.
type Release struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Changelog   string `json:"changelog"`
	Mandatory   bool   `json:"mandatory"`
}

// DistributionVersionCheck mirrors GET /maintain-api/distribution/version-check.
type DistributionVersionCheck struct {
	UpdateAvailable          bool                   `json:"update_available"`
	LatestVersion            string                 `json:"latest_version"`
	CurrentVersion           string                 `json:"current_version"`
	Channel                  string                 `json:"channel"`
	Mandatory                bool                   `json:"mandatory"`
	MinRequired              string                 `json:"min_required,omitempty"`
	AutoUpgrade              bool                   `json:"auto_upgrade"`
	UpgradePolicyID          string                 `json:"upgrade_policy_id,omitempty"`
	EstimatedDowntimeMinutes int                    `json:"estimated_downtime_minutes,omitempty"`
	TargetRelease            *DistributionRelease   `json:"target_release,omitempty"`
	TargetArtifacts          []DistributionArtifact `json:"target_artifacts,omitempty"`
}

type DistributionRelease struct {
	Version     string `json:"version"`
	BuildSeq    int    `json:"build_seq"`
	Channel     string `json:"channel"`
	ReleaseDate string `json:"release_date,omitempty"`
	NotesURL    string `json:"notes_url,omitempty"`
}

type DistributionArtifact struct {
	Platform     string `json:"platform"`
	Arch         string `json:"arch"`
	Edition      string `json:"edition"`
	ArtifactName string `json:"artifact_name"`
	SizeBytes    int64  `json:"size_bytes"`
	// Filename and Size retain the pre-P4.4 exported fields for source compatibility.
	// New Maintain responses use ArtifactName and SizeBytes.
	Filename   string `json:"filename,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256"`
	StorageURI string `json:"storage_uri"`
}

// DistributionCheckResult preserves P4.4 policy hints alongside the legacy release mapping.
type DistributionCheckResult struct {
	CheckUpdateResponse
	AutoUpgrade              bool
	UpgradePolicyID          string
	EstimatedDowntimeMinutes int
	VersionCheck             DistributionVersionCheck
}

// CheckUpdateDistribution calls Maintain's distribution version-check endpoint.
// It remains anonymous when proof is unavailable; with valid proof, it includes
// the matching instance_id query value and P3 device-proof headers.
func (c *Client) CheckUpdateDistribution(ctx context.Context, currentVersion, channel, platform, arch string) (*CheckUpdateResponse, error) {
	result, err := c.CheckDistribution(ctx, currentVersion, channel, platform, arch)
	if err != nil {
		return nil, err
	}
	return &result.CheckUpdateResponse, nil
}

// CheckDistribution performs a version check and returns P4.4 policy hints when authorized.
func (c *Client) CheckDistribution(ctx context.Context, currentVersion, channel, platform, arch string) (*DistributionCheckResult, error) {
	if platform == "" {
		platform = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}

	endpoint, err := c.endpoint("/maintain-api/distribution/version-check", url.Values{
		"channel":  {channel},
		"current":  {currentVersion},
		"platform": {platform},
		"arch":     {arch},
	})
	if err != nil {
		return nil, err
	}
	if c.proof.valid() {
		query := endpoint.Query()
		query.Set("instance_id", c.proof.InstanceID)
		endpoint.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.applyProof(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return &DistributionCheckResult{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}

	var v DistributionVersionCheck
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	result := &DistributionCheckResult{
		CheckUpdateResponse:      CheckUpdateResponse{HasUpdate: v.UpdateAvailable},
		AutoUpgrade:              v.AutoUpgrade,
		UpgradePolicyID:          v.UpgradePolicyID,
		EstimatedDowntimeMinutes: v.EstimatedDowntimeMinutes,
		VersionCheck:             v,
	}
	if v.UpdateAvailable {
		artifact := selectArtifact(v.TargetArtifacts, platform, arch)
		if artifact == nil {
			return nil, fmt.Errorf("no artifact matching platform=%s arch=%s in response (have %d candidates)", platform, arch, len(v.TargetArtifacts))
		}
		result.Release = &Release{
			Version:     v.LatestVersion,
			DownloadURL: artifact.StorageURI,
			SHA256:      artifact.SHA256,
			Mandatory:   v.Mandatory,
		}
	}
	return result, nil
}

func selectArtifact(artifacts []DistributionArtifact, platform, arch string) *DistributionArtifact {
	for i := range artifacts {
		if artifacts[i].Platform == platform && artifacts[i].Arch == arch {
			return &artifacts[i]
		}
	}
	return nil
}

// UpgradeTask is a task atomically claimed by Maintain's P3 poll endpoint.
type UpgradeTask struct {
	TaskID         int64  `json:"task_id"`
	ToVersion      string `json:"to_version"`
	PreCheck       bool   `json:"pre_check"`
	DryRun         bool   `json:"dry_run"`
	DownloadURL    string `json:"download_url"`
	DownloadHint   string `json:"download_hint"`
	ScriptURL      string `json:"script_url"`
	ChecksumSHA256 string `json:"checksum_sha256"`
}

// PollUpgradeTask returns nil, nil when the instance has no claimable task.
// A 204 is normal, including while a sequential batch is blocked by an earlier task.
func (c *Client) PollUpgradeTask(ctx context.Context) (*UpgradeTask, error) {
	if err := c.requireProof(); err != nil {
		return nil, err
	}
	endpoint, err := c.endpoint("/maintain-api/upgrade/poll", nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create poll request: %w", err)
	}
	c.applyProof(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll upgrade task: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	var task UpgradeTask
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return nil, fmt.Errorf("decode poll response: %w", err)
	}
	if task.TaskID == 0 || task.ToVersion == "" {
		return nil, fmt.Errorf("decode poll response: missing task_id or to_version")
	}
	return &task, nil
}

// DownloadTicket authorizes a single package download from Maintain.
type DownloadTicket struct {
	RequestID string    `json:"request_id"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	FileName  string    `json:"file_name"`
}

type downloadTicketRequest struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
}

// CreateDownloadTicket obtains the ticket for the task version and local platform.
func (c *Client) CreateDownloadTicket(ctx context.Context, version, platform, arch string) (*DownloadTicket, error) {
	endpoint, err := c.endpoint("/maintain-api/downloads/ticket", nil)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(downloadTicketRequest{Version: version, Platform: platform, Arch: arch})
	if err != nil {
		return nil, fmt.Errorf("marshal ticket request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create ticket request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request download ticket: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	var ticket DownloadTicket
	if err := json.NewDecoder(resp.Body).Decode(&ticket); err != nil {
		return nil, fmt.Errorf("decode ticket response: %w", err)
	}
	if ticket.RequestID == "" || ticket.URL == "" {
		return nil, fmt.Errorf("decode ticket response: missing request_id or url")
	}
	return &ticket, nil
}

// UpgradeProgress reports a non-terminal P3 task stage.
type UpgradeProgress struct {
	Status   string `json:"status"`
	Stage    string `json:"stage"`
	Progress int    `json:"progress"`
	Message  string `json:"message,omitempty"`
}

// ReportUpgradeProgress records download, install, switch, or test progress.
func (c *Client) ReportUpgradeProgress(ctx context.Context, taskID int64, progress UpgradeProgress) error {
	if err := c.requireProof(); err != nil {
		return err
	}
	if taskID <= 0 {
		return fmt.Errorf("task_id must be positive")
	}
	if progress.Progress < 0 || progress.Progress > 100 {
		return fmt.Errorf("progress must be between 0 and 100")
	}
	switch progress.Status {
	case "downloading", "installing", "testing":
	default:
		return fmt.Errorf("invalid task progress status %q", progress.Status)
	}
	return c.postTask(ctx, taskID, "progress", progress)
}

// UpgradeResult records the final P3 task outcome.
type UpgradeResult struct {
	Status          string   `json:"status"`
	Error           string   `json:"error,omitempty"`
	DurationSeconds int64    `json:"duration_seconds,omitempty"`
	Logs            []string `json:"logs,omitempty"`
}

// ReportUpgradeResult records completed, failed, or rolled_back. Repeating the
// same result is safe because Maintain treats it as idempotent.
func (c *Client) ReportUpgradeResult(ctx context.Context, taskID int64, result UpgradeResult) error {
	if err := c.requireProof(); err != nil {
		return err
	}
	if taskID <= 0 {
		return fmt.Errorf("task_id must be positive")
	}
	switch result.Status {
	case "completed", "failed", "rolled_back":
	default:
		return fmt.Errorf("invalid task result status %q", result.Status)
	}
	return c.postTask(ctx, taskID, "result", result)
}

func (c *Client) postTask(ctx context.Context, taskID int64, action string, payload any) error {
	endpoint, err := c.endpoint(path.Join("/maintain-api/upgrade/tasks", strconv.FormatInt(taskID, 10), action), nil)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal task %s: %w", action, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create task %s request: %w", action, err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyProof(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("report task %s: %w", action, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return responseError(resp)
	}
	return nil
}

// CheckUpdate checks the legacy authority endpoint for backward compatibility.
func (c *Client) CheckUpdate(ctx context.Context, currentVersion, channel string) (*CheckUpdateResponse, error) {
	endpoint, err := c.endpoint("/api/v1/updates/latest", url.Values{
		"channel":         {channel},
		"current_version": {currentVersion},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return &CheckUpdateResponse{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	var result CheckUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// ReportUpdateRequest is retained for the legacy authority API.
type ReportUpdateRequest struct {
	InstanceID string `json:"instance_id"`
	Version    string `json:"version"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

func (c *Client) ReportUpdate(ctx context.Context, req *ReportUpdateRequest) error {
	return c.postLegacy(ctx, "/api/v1/updates/report", req)
}

// ReportRollbackRequest is retained for the legacy authority API.
type ReportRollbackRequest struct {
	InstanceID string `json:"instance_id"`
	ToVersion  string `json:"to_version"`
	Reason     string `json:"reason"`
}

func (c *Client) ReportRollback(ctx context.Context, req *ReportRollbackRequest) error {
	return c.postLegacy(ctx, "/api/v1/updates/rollback", req)
}

func (c *Client) postLegacy(ctx context.Context, requestPath string, payload any) error {
	endpoint, err := c.endpoint(requestPath, nil)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return responseError(resp)
	}
	return nil
}

func (c *Client) endpoint(requestPath string, query url.Values) (*url.URL, error) {
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	endpoint.Path = path.Join(endpoint.Path, requestPath)
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}
	return endpoint, nil
}

func (c *Client) applyProof(req *http.Request) {
	if !c.proof.valid() {
		return
	}
	req.Header.Set("X-Instance-ID", c.proof.InstanceID)
	req.Header.Set("X-License-Key", c.proof.LicenseKey)
	req.Header.Set("X-Hardware-Hash", c.proof.HardwareHash)
}

func (c *Client) requireProof() error {
	if !c.proof.valid() {
		return fmt.Errorf("instance device proof is required")
	}
	return nil
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
}
