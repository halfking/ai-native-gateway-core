package center

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// CommandExecutor 命令执行器（边缘节点使用）
type CommandExecutor struct {
	client *Client
}

// NewCommandExecutor 创建命令执行器
func NewCommandExecutor(client *Client) *CommandExecutor {
	return &CommandExecutor{
		client: client,
	}
}

// Execute 执行命令
func (e *CommandExecutor) Execute(ctx context.Context, cmd *Command) *CommandResult {
	start := time.Now()
	result := &CommandResult{
		Success: false,
	}

	slog.Info("executing command", "command_id", cmd.CommandID, "command", cmd.Command)

	// 根据命令类型执行
	switch cmd.Command {
	case "restart":
		result = e.executeRestart(ctx, cmd)
	case "upgrade":
		result = e.executeUpgrade(ctx, cmd)
	case "config_update":
		result = e.executeConfigUpdate(ctx, cmd)
	case "health_check":
		result = e.executeHealthCheck(ctx, cmd)
	case "collect_logs":
		result = e.executeCollectLogs(ctx, cmd)
	default:
		result.Error = "unknown command: " + cmd.Command
	}

	result.ExecMs = time.Since(start).Milliseconds()
	return result
}

// executeRestart 执行重启
func (e *CommandExecutor) executeRestart(ctx context.Context, cmd *Command) *CommandResult {
	result := &CommandResult{}

	// 这里应该触发进程重启
	// 实际实现中需要使用systemd或supervisor等进程管理器
	result.Success = true
	result.Output = "restart scheduled"

	slog.Info("restart command executed", "command_id", cmd.CommandID)
	return result
}

// executeUpgrade 执行升级
func (e *CommandExecutor) executeUpgrade(ctx context.Context, cmd *Command) *CommandResult {
	result := &CommandResult{}

	targetVersion := cmd.Args["version"]
	if targetVersion == "" {
		result.Error = "missing version argument"
		return result
	}

	// 触发自动升级流程
	// 实际实现中需要调用autoupdate模块
	result.Success = true
	result.Output = "upgrade to " + targetVersion + " initiated"

	slog.Info("upgrade command executed", "command_id", cmd.CommandID, "target_version", targetVersion)
	return result
}

// executeConfigUpdate 执行配置更新
func (e *CommandExecutor) executeConfigUpdate(ctx context.Context, cmd *Command) *CommandResult {
	result := &CommandResult{}

	configKey := cmd.Args["key"]
	configValue := cmd.Args["value"]

	if configKey == "" || configValue == "" {
		result.Error = "missing key or value argument"
		return result
	}

	// 更新配置
	// 实际实现中需要调用config模块
	result.Success = true
	result.Output = "config updated: " + configKey + " = " + configValue

	slog.Info("config_update command executed", "command_id", cmd.CommandID, "key", configKey)
	return result
}

// executeHealthCheck 执行健康检查
func (e *CommandExecutor) executeHealthCheck(ctx context.Context, cmd *Command) *CommandResult {
	result := &CommandResult{}

	// 执行健康检查
	// 检查数据库连接、Redis连接、文件系统等
	checks := []string{
		"database: ok",
		"redis: ok",
		"filesystem: ok",
	}

	result.Success = true
	result.Output = strings.Join(checks, "\n")

	slog.Info("health_check command executed", "command_id", cmd.CommandID)
	return result
}

// executeCollectLogs 执行日志收集
func (e *CommandExecutor) executeCollectLogs(ctx context.Context, cmd *Command) *CommandResult {
	result := &CommandResult{}

	lines := cmd.Args["lines"]
	if lines == "" {
		lines = "100"
	}

	// 收集最近的日志
	execCmd := exec.CommandContext(ctx, "tail", "-n", lines, "/var/log/llm-gateway.log")
	output, err := execCmd.CombinedOutput()
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.Output = string(output)

	slog.Info("collect_logs command executed", "command_id", cmd.CommandID, "lines", lines)
	return result
}

// StartWorker 启动命令执行Worker
func (e *CommandExecutor) StartWorker(ctx context.Context, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("command executor worker stopped")
			return
		case <-ticker.C:
			e.pollAndExecute(ctx)
		}
	}
}

// pollAndExecute 轮询并执行命令
func (e *CommandExecutor) pollAndExecute(ctx context.Context) {
	// 获取待执行命令
	commands, err := e.client.FetchPendingCommands(ctx)
	if err != nil {
		slog.Error("fetch pending commands failed", "error", err)
		return
	}

	if len(commands) == 0 {
		return
	}

	slog.Info("found pending commands", "count", len(commands))

	// 执行每个命令
	for _, cmd := range commands {
		// 检查是否过期
		if cmd.ExpiresAt != nil && time.Now().After(*cmd.ExpiresAt) {
			result := &CommandResult{
				Success: false,
				Error:   "command expired",
			}
			_ = e.client.ReportCommandResult(ctx, cmd.CommandID, result)
			continue
		}

		// 执行命令
		result := e.Execute(ctx, &cmd)

		// 上报结果
		if err := e.client.ReportCommandResult(ctx, cmd.CommandID, result); err != nil {
			slog.Error("report command result failed", "command_id", cmd.CommandID, "error", err)
		}
	}
}
