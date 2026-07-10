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
	InstanceID    string    `json:"instance_id"`
	Hostname      string    `json:"hostname"`
	IPAddress     string    `json:"ip_address"`
	Region        string    `json:"region,omitempty"`
	Version       string    `json:"version"`
	BuildSeq      int       `json:"build_seq"`
	StartedAt     time.Time `json:"started_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Status        string    `json:"status"`
}

const (
	StatusOnline   = "online"
	StatusOffline  = "offline"
	StatusDegraded = "degraded"
)
