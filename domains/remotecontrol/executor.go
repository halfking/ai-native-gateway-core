package remotecontrol

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// DefaultCommandExecutor 默认指令执行器
//
// 职责：
//   - 执行远程指令
//   - 权限验证
//   - 审计记录
type DefaultCommandExecutor struct {
	sessionMgr  SessionManager
	authChecker AuthorizationChecker
	auditLogger AuditLogger
}

// NewDefaultCommandExecutor 创建默认指令执行器
func NewDefaultCommandExecutor(
	sessionMgr SessionManager,
	authChecker AuthorizationChecker,
	auditLogger AuditLogger,
) *DefaultCommandExecutor {
	return &DefaultCommandExecutor{
		sessionMgr:  sessionMgr,
		authChecker: authChecker,
		auditLogger: auditLogger,
	}
}

// Execute 执行指令
func (e *DefaultCommandExecutor) Execute(ctx context.Context, cmd *RemoteCommand) error {
	if cmd == nil {
		return fmt.Errorf("command is nil")
	}

	// 生成ID
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}

	// 设置初始状态
	cmd.Status = CommandStatusExecuting
	cmd.CreatedAt = time.Now()
	now := time.Now()
	cmd.ExecutedAt = &now

	// 1. 权限检查
	if !e.authChecker.CanExecute(cmd.IssuerID, cmd.Type, cmd.TenantID) {
		cmd.Status = CommandStatusFailed
		cmd.Error = "permission denied"
		e.logCommand(ctx, cmd)
		return fmt.Errorf("permission denied for user %s to execute %s", cmd.IssuerID, cmd.Type)
	}

	// 2. 审计记录
	e.logCommand(ctx, cmd)

	slog.Info("executing remote command",
		"command_id", cmd.ID,
		"type", cmd.Type,
		"session_id", cmd.SessionID,
		"issuer", cmd.IssuerName)

	// 3. 执行指令
	var err error
	switch cmd.Type {
	case CommandTypePause:
		err = e.pauseSession(ctx, cmd)
	case CommandTypeResume:
		err = e.resumeSession(ctx, cmd)
	case CommandTypeTerminate:
		err = e.terminateSession(ctx, cmd)
	case CommandTypeInspect:
		err = e.inspectSession(ctx, cmd)
	case CommandTypeModify:
		err = e.modifySession(ctx, cmd)
	case CommandTypeStatus:
		err = e.statusSession(ctx, cmd)
	default:
		err = fmt.Errorf("unknown command type: %s", cmd.Type)
	}

	// 4. 更新指令状态
	completedAt := time.Now()
	cmd.CompletedAt = &completedAt

	if err != nil {
		cmd.Status = CommandStatusFailed
		cmd.Error = err.Error()

		slog.Error("command execution failed",
			"command_id", cmd.ID,
			"type", cmd.Type,
			"error", err)
	} else {
		cmd.Status = CommandStatusCompleted

		slog.Info("command execution completed",
			"command_id", cmd.ID,
			"type", cmd.Type,
			"duration_ms", completedAt.Sub(*cmd.ExecutedAt).Milliseconds())
	}

	// 5. 记录最终状态
	e.logCommand(ctx, cmd)

	return err
}

// pauseSession 暂停会话
func (e *DefaultCommandExecutor) pauseSession(ctx context.Context, cmd *RemoteCommand) error {
	if cmd.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	// 暂停会话
	if err := e.sessionMgr.PauseSession(ctx, cmd.SessionID); err != nil {
		return fmt.Errorf("failed to pause session: %w", err)
	}

	// 获取会话状态
	state, err := e.sessionMgr.GetSessionState(ctx, cmd.SessionID)
	if err != nil {
		slog.Warn("failed to get session state after pause", "error", err)
	} else {
		cmd.Result = map[string]any{
			"session_id":    cmd.SessionID,
			"current_state": state.GetState(),
			"message":       "session paused successfully",
		}
	}

	return nil
}

// resumeSession 恢复会话
func (e *DefaultCommandExecutor) resumeSession(ctx context.Context, cmd *RemoteCommand) error {
	if cmd.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	// 恢复会话
	if err := e.sessionMgr.ResumeSession(ctx, cmd.SessionID); err != nil {
		return fmt.Errorf("failed to resume session: %w", err)
	}

	// 获取会话状态
	state, err := e.sessionMgr.GetSessionState(ctx, cmd.SessionID)
	if err != nil {
		slog.Warn("failed to get session state after resume", "error", err)
	} else {
		cmd.Result = map[string]any{
			"session_id":    cmd.SessionID,
			"current_state": state.GetState(),
			"message":       "session resumed successfully",
		}
	}

	return nil
}

// terminateSession 终止会话
func (e *DefaultCommandExecutor) terminateSession(ctx context.Context, cmd *RemoteCommand) error {
	if cmd.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	// 获取终止原因
	reason := "terminated by operator"
	if r, ok := cmd.Parameters["reason"].(string); ok && r != "" {
		reason = r
	}

	// 终止会话
	if err := e.sessionMgr.TerminateSession(ctx, cmd.SessionID, reason); err != nil {
		return fmt.Errorf("failed to terminate session: %w", err)
	}

	cmd.Result = map[string]any{
		"session_id": cmd.SessionID,
		"reason":     reason,
		"message":    "session terminated successfully",
	}

	return nil
}

// inspectSession 检查会话
func (e *DefaultCommandExecutor) inspectSession(ctx context.Context, cmd *RemoteCommand) error {
	if cmd.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	// 获取会话状态
	state, err := e.sessionMgr.GetSessionState(ctx, cmd.SessionID)
	if err != nil {
		return fmt.Errorf("failed to get session state: %w", err)
	}

	// 构建检查结果
	snapshot := state.GetSnapshot()

	cmd.Result = map[string]any{
		"session_id":    cmd.SessionID,
		"current_state": snapshot.CurrentState,
		"current_phase": snapshot.CurrentPhase,
		"state_history": snapshot.History,
		"metrics":       snapshot.Metrics,
		"metadata":      snapshot.Metadata,
		"created_at":    snapshot.CreatedAt,
		"inspected_at":  time.Now(),
	}

	return nil
}

// modifySession 修改会话
func (e *DefaultCommandExecutor) modifySession(ctx context.Context, cmd *RemoteCommand) error {
	if cmd.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	// 获取会话
	session, err := e.sessionMgr.GetSession(ctx, cmd.SessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// 修改会话参数（从Parameters中读取）
	modified := make(map[string]any)

	// 示例：修改元数据
	if metadata, ok := cmd.Parameters["metadata"].(map[string]any); ok {
		for key, value := range metadata {
			if session.Metadata == nil {
				session.Metadata = make(map[string]any)
			}
			session.Metadata[key] = value
			modified[key] = value
		}
	}

	cmd.Result = map[string]any{
		"session_id": cmd.SessionID,
		"modified":   modified,
		"message":    "session modified successfully",
	}

	return nil
}

// statusSession 查询会话状态
func (e *DefaultCommandExecutor) statusSession(ctx context.Context, cmd *RemoteCommand) error {
	if cmd.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	// 获取会话状态
	state, err := e.sessionMgr.GetSessionState(ctx, cmd.SessionID)
	if err != nil {
		return fmt.Errorf("failed to get session state: %w", err)
	}

	cmd.Result = map[string]any{
		"session_id":    cmd.SessionID,
		"current_state": state.GetState(),
		"current_phase": state.GetPhase(),
		"is_terminal":   state.IsTerminal(),
	}

	return nil
}

// logCommand 记录指令
func (e *DefaultCommandExecutor) logCommand(ctx context.Context, cmd *RemoteCommand) {
	if e.auditLogger == nil {
		return
	}

	if err := e.auditLogger.LogCommand(ctx, cmd); err != nil {
		slog.Error("failed to log command", "command_id", cmd.ID, "error", err)
	}
}

// SimpleAuthorizationChecker 简单权限检查器
type SimpleAuthorizationChecker struct {
	userRoles map[string]map[string]Role // userID -> tenantID -> Role
}

// NewSimpleAuthorizationChecker 创建简单权限检查器
func NewSimpleAuthorizationChecker() *SimpleAuthorizationChecker {
	return &SimpleAuthorizationChecker{
		userRoles: make(map[string]map[string]Role),
	}
}

// SetUserRole 设置用户角色
func (c *SimpleAuthorizationChecker) SetUserRole(userID, tenantID string, role Role) {
	if c.userRoles[userID] == nil {
		c.userRoles[userID] = make(map[string]Role)
	}
	c.userRoles[userID][tenantID] = role
}

// CanExecute 检查是否可以执行指令
func (c *SimpleAuthorizationChecker) CanExecute(issuerID string, cmdType CommandType, tenantID string) bool {
	// 获取用户角色
	role, err := c.GetUserRole(issuerID, tenantID)
	if err != nil {
		return false
	}

	// 获取需要的权限
	permission, ok := CommandTypeToPermission[cmdType]
	if !ok {
		return false
	}

	// 检查角色是否有权限
	return HasPermission(Role(role), permission)
}

// GetUserRole 获取用户角色
func (c *SimpleAuthorizationChecker) GetUserRole(issuerID string, tenantID string) (string, error) {
	if tenantRoles, ok := c.userRoles[issuerID]; ok {
		if role, ok := tenantRoles[tenantID]; ok {
			return string(role), nil
		}
	}

	return "", fmt.Errorf("user role not found")
}

// InMemoryAuditLogger 内存审计日志（用于测试）
type InMemoryAuditLogger struct {
	commands []*RemoteCommand
}

// NewInMemoryAuditLogger 创建内存审计日志
func NewInMemoryAuditLogger() *InMemoryAuditLogger {
	return &InMemoryAuditLogger{
		commands: make([]*RemoteCommand, 0),
	}
}

// LogCommand 记录指令
func (l *InMemoryAuditLogger) LogCommand(ctx context.Context, cmd *RemoteCommand) error {
	l.commands = append(l.commands, cmd)
	return nil
}

// GetCommands 获取所有指令（用于测试）
func (l *InMemoryAuditLogger) GetCommands() []*RemoteCommand {
	return l.commands
}
