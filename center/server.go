package center

import (
	"context"
	"log/slog"
	"time"
)

// Server 中心节点服务端
type Server struct {
	store Store

	instances map[string]*InstanceInfo
}

// NewServer 创建中心节点服务端
func NewServer(store Store) *Server {
	return &Server{
		store:     store,
		instances: make(map[string]*InstanceInfo),
	}
}

// GetInstance 获取实例信息
func (s *Server) GetInstance(ctx context.Context, instanceID string) (*InstanceInfo, error) {
	return s.store.GetInstance(ctx, instanceID)
}

// ListInstances 列出所有实例
func (s *Server) ListInstances(ctx context.Context, status string, offset, limit int) ([]InstanceInfo, int, error) {
	return s.store.ListInstances(ctx, status, offset, limit)
}

// IssueCommand 下发命令
func (s *Server) IssueCommand(ctx context.Context, instanceID, command string, args map[string]string, issuedBy string) (*Command, error) {
	cmd := &Command{
		CommandID:  generateCommandID(),
		InstanceID: instanceID,
		Command:    command,
		Args:       args,
		Status:     CommandStatusPending,
		IssuedAt:   time.Now(),
		IssuedBy:   issuedBy,
	}

	if err := s.store.CreateCommand(ctx, cmd); err != nil {
		slog.Error("create command failed", "error", err)
		return nil, err
	}

	slog.Info("command issued", "command_id", cmd.CommandID, "instance_id", instanceID, "command", command)
	return cmd, nil
}

// GetCommandStatus 查询命令状态
func (s *Server) GetCommandStatus(ctx context.Context, commandID string) (*Command, error) {
	return s.store.GetCommand(ctx, commandID)
}

// GetInstanceHealth 获取实例健康状态
func (s *Server) GetInstanceHealth(ctx context.Context, instanceID string) (string, error) {
	lastHeartbeat, err := s.store.GetLastHeartbeat(ctx, instanceID)
	if err != nil {
		return StatusOffline, err
	}

	// 超过2分钟未心跳视为离线
	if time.Since(lastHeartbeat) > 2*time.Minute {
		return StatusOffline, nil
	}

	// 超过30秒未心跳视为降级
	if time.Since(lastHeartbeat) > 30*time.Second {
		return StatusDegraded, nil
	}

	return StatusOnline, nil
}

// GetInstanceMetrics 获取实例指标
func (s *Server) GetInstanceMetrics(ctx context.Context, instanceID string, since time.Time, limit int) ([]HeartbeatRecord, error) {
	return s.store.GetHeartbeatHistory(ctx, instanceID, since, limit)
}

// MonitorInstances 监控实例状态（后台任务）
func (s *Server) MonitorInstances(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("instance monitor stopped")
			return
		case <-ticker.C:
			s.checkInstanceHealth(ctx)
		}
	}
}

// checkInstanceHealth 检查所有实例健康状态
func (s *Server) checkInstanceHealth(ctx context.Context) {
	instances, _, err := s.store.ListInstances(ctx, "", 0, 1000)
	if err != nil {
		slog.Error("list instances failed", "error", err)
		return
	}

	for _, instance := range instances {
		health, err := s.GetInstanceHealth(ctx, instance.InstanceID)
		if err != nil {
			continue
		}

		// 如果状态变化，更新数据库
		if health != instance.Status {
			if err := s.store.UpdateInstanceStatus(ctx, instance.InstanceID, health); err != nil {
				slog.Error("update instance status failed", "instance_id", instance.InstanceID, "error", err)
			} else {
				slog.Info("instance status changed",
					"instance_id", instance.InstanceID,
					"old_status", instance.Status,
					"new_status", health,
				)
			}
		}
	}
}

// GetDashboardStats 获取仪表盘统计
func (s *Server) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	instances, total, err := s.store.ListInstances(ctx, "", 0, 1000)
	if err != nil {
		return nil, err
	}

	stats := &DashboardStats{
		TotalInstances: total,
	}

	for _, instance := range instances {
		switch instance.Status {
		case StatusOnline:
			stats.OnlineInstances++
		case StatusOffline:
			stats.OfflineInstances++
		case StatusDegraded:
			stats.DegradedInstances++
		}
	}

	return stats, nil
}

// DashboardStats 仪表盘统计
type DashboardStats struct {
	TotalInstances    int `json:"total_instances"`
	OnlineInstances   int `json:"online_count"`
	OfflineInstances  int `json:"offline_count"`
	DegradedInstances int `json:"degraded_count"`
}

// generateCommandID 生成命令ID
func generateCommandID() string {
	return time.Now().Format("20060102150405") + "-" + randomString(8)
}

// randomString 生成随机字符串
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[i%len(letters)]
	}
	return string(b)
}
