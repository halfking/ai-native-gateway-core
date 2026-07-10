package center

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Client 中心节点客户端（边缘节点使用）
type Client struct {
	serverURL  string
	instanceID string
	version    string
	buildSeq   int
	store      Store

	mu            sync.RWMutex
	lastHeartbeat time.Time
	status        string
}

// NewClient 创建中心节点客户端
func NewClient(serverURL, instanceID, version string, buildSeq int, store Store) *Client {
	return &Client{
		serverURL:  serverURL,
		instanceID: instanceID,
		version:    version,
		buildSeq:   buildSeq,
		store:      store,
		status:     StatusOnline,
	}
}

// Register 注册到中心节点
func (c *Client) Register(ctx context.Context, hostname, ipAddress, region string) error {
	instance := &InstanceInfo{
		InstanceID: c.instanceID,
		Hostname:   hostname,
		IPAddress:  ipAddress,
		Region:     region,
		Version:    c.version,
		BuildSeq:   c.buildSeq,
		Status:     StatusOnline,
		StartedAt:  time.Now(),
	}

	if err := c.store.RegisterInstance(ctx, instance); err != nil {
		slog.Error("register instance failed", "error", err)
		return err
	}

	slog.Info("instance registered", "instance_id", c.instanceID, "version", c.version)
	return nil
}

// SendHeartbeat 发送心跳
func (c *Client) SendHeartbeat(ctx context.Context, payload *HeartbeatPayload) error {
	if err := c.store.RecordHeartbeat(ctx, c.instanceID, payload); err != nil {
		slog.Error("record heartbeat failed", "error", err)
		return err
	}

	c.mu.Lock()
	c.lastHeartbeat = time.Now()
	c.mu.Unlock()

	return nil
}

// SendStatusReport 发送状态报告
func (c *Client) SendStatusReport(ctx context.Context, payload *StatusReportPayload) error {
	if err := c.store.RecordStatusReport(ctx, c.instanceID, payload); err != nil {
		slog.Error("record status report failed", "error", err)
		return err
	}

	return nil
}

// FetchPendingCommands 获取待执行命令
func (c *Client) FetchPendingCommands(ctx context.Context) ([]Command, error) {
	commands, err := c.store.ListPendingCommands(ctx, c.instanceID)
	if err != nil {
		slog.Error("fetch pending commands failed", "error", err)
		return nil, err
	}

	return commands, nil
}

// ReportCommandResult 上报命令执行结果
func (c *Client) ReportCommandResult(ctx context.Context, commandID string, result *CommandResult) error {
	status := CommandStatusExecuted
	if !result.Success {
		status = CommandStatusFailed
	}

	if err := c.store.UpdateCommandStatus(ctx, commandID, status, result); err != nil {
		slog.Error("report command result failed", "error", err)
		return err
	}

	return nil
}

// GetStatus 获取客户端状态
func (c *Client) GetStatus() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// SetStatus 设置客户端状态
func (c *Client) SetStatus(status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
}

// StartHeartbeatWorker 启动心跳Worker
func (c *Client) StartHeartbeatWorker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("heartbeat worker stopped")
			return
		case <-ticker.C:
			c.sendHeartbeat(ctx)
		}
	}
}

// sendHeartbeat 内部心跳发送
func (c *Client) sendHeartbeat(ctx context.Context) {
	// 构建心跳载荷
	payload := &HeartbeatPayload{
		UptimeSecs:   int64(time.Since(time.Now().Add(-24 * time.Hour)).Seconds()),
		GoVersion:    "go1.21",
		NumGoroutine: 100,
		AllocMB:      512.0,
		TotalAllocMB: 1024.0,
		SysMB:        2048.0,
		CPUCores:     8,
	}

	if err := c.SendHeartbeat(ctx, payload); err != nil {
		slog.Error("send heartbeat failed", "error", err)
	}
}
