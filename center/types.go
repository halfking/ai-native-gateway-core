package center

import "time"

type MessageType string

const (
	MsgHeartbeat     MessageType = "heartbeat"
	MsgStatusReport  MessageType = "status_report"
	MsgMetricReport  MessageType = "metric_report"
	MsgEventAlert    MessageType = "event_alert"
	MsgCommand       MessageType = "command"
	MsgCommandResult MessageType = "command_result"
	MsgUpgradeNotify MessageType = "upgrade_notify"
)

type Envelope struct {
	ID         string      `json:"id"`
	Type       MessageType `json:"type"`
	InstanceID string      `json:"instance_id"`
	TenantID   string      `json:"tenant_id"`
	Timestamp  time.Time   `json:"timestamp"`
	Version    string      `json:"version"`
	Payload    []byte      `json:"payload"`
	Signature  string      `json:"signature,omitempty"`
}

type HeartbeatPayload struct {
	UptimeSecs   int64   `json:"uptime_secs"`
	GoVersion    string  `json:"go_version"`
	NumGoroutine int     `json:"num_goroutine"`
	AllocMB      float64 `json:"alloc_mb"`
	TotalAllocMB float64 `json:"total_alloc_mb"`
	SysMB        float64 `json:"sys_mb"`
	CPUCores     int     `json:"cpu_cores"`
}

type StatusReportPayload struct {
	State          string  `json:"state"`
	ActiveLicenses int     `json:"active_licenses"`
	ActiveDevices  int     `json:"active_devices"`
	RequestsTotal  int64   `json:"requests_total"`
	RequestsOk     int64   `json:"requests_ok"`
	RequestsErr    int64   `json:"requests_err"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	P99LatencyMs   float64 `json:"p99_latency_ms"`
}

type MetricReportPayload struct {
	Metrics []MetricDataPoint `json:"metrics"`
}

type MetricDataPoint struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Timestamp time.Time         `json:"timestamp"`
}

type CommandPayload struct {
	CommandID string            `json:"command_id"`
	Command   string            `json:"command"`
	Args      map[string]string `json:"args,omitempty"`
	IssuedAt  time.Time         `json:"issued_at"`
	IssuedBy  string            `json:"issued_by"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}

type CommandResultPayload struct {
	CommandID string `json:"command_id"`
	Success   bool   `json:"success"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
	ExecMs    int64  `json:"exec_ms"`
}

type InstanceInfo struct {
	InstanceID     string    `json:"instance_id"`
	Hostname       string    `json:"hostname"`
	IPAddress      string    `json:"ip_address"`
	Region         string    `json:"region,omitempty"`
	Version        string    `json:"version"`
	BuildSeq       int       `json:"build_seq"`
	StartedAt      time.Time `json:"started_at"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	Status         string    `json:"status"`
	InstanceType   string    `json:"instance_type,omitempty"`
	DeploymentID   string    `json:"deployment_id,omitempty"`
	ReplicaCount   int       `json:"replica_count,omitempty"`
	LicenseKeyHash string    `json:"license_key_hash,omitempty"`
	HardwareHash   string    `json:"hardware_hash,omitempty"`
	PublicKey      string    `json:"public_key,omitempty"`
	InstanceToken  string    `json:"instance_token,omitempty"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
}

const (
	StatusOnline   = "online"
	StatusOffline  = "offline"
	StatusDegraded = "degraded"
)

// OpsAlert is a derived operational alert (no separate rules engine yet).
type OpsAlert struct {
	ID         string    `json:"id"`
	Severity   string    `json:"severity"`
	Title      string    `json:"title"`
	Message    string    `json:"message"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
	InstanceID string    `json:"instance_id,omitempty"`
	DetectedAt time.Time `json:"detected_at"`
}

// RuntimeMetricsSummary 实例性能汇总（用于 Ops Overview）
type RuntimeMetricsSummary struct {
	InstanceID string             `json:"instance_id"`
	Hostname   string             `json:"hostname"`
	Region     string             `json:"region"`
	Version    string             `json:"version"`
	Status     string             `json:"status"`
	AvgCPU     float64            `json:"avg_cpu_pct"`
	AvgMemPct  float64            `json:"avg_mem_pct"`
	AvgTPS     float64            `json:"avg_tps"`
	MaxP99     int                `json:"max_p99_ms"`
	TopModels  map[string]int64   `json:"top_models"`
	LastUpdate *time.Time         `json:"last_update"`
}
