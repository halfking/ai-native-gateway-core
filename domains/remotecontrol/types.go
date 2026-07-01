// Package remotecontrol 实现远程控制系统。
//
// 核心能力：
//   - 远程指令：暂停、恢复、终止、检查会话
//   - 飞书指令集成：通过飞书机器人发送指令
//   - 权限验证：确保操作人员有相应权限
//   - 审计追踪：完整记录所有远程操作
//
// 设计原则：
//   - 安全第一：严格的权限验证
//   - 审计完整：所有操作都有日志
//   - 易于扩展：支持添加新的指令类型
package remotecontrol

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/sessionstate"
)

// CommandType 指令类型
type CommandType string

const (
	// CommandTypePause 暂停会话
	CommandTypePause CommandType = "pause"

	// CommandTypeResume 恢复会话
	CommandTypeResume CommandType = "resume"

	// CommandTypeTerminate 终止会话
	CommandTypeTerminate CommandType = "terminate"

	// CommandTypeInspect 检查会话状态
	CommandTypeInspect CommandType = "inspect"

	// CommandTypeModify 修改会话参数
	CommandTypeModify CommandType = "modify"

	// CommandTypeStatus 查询状态
	CommandTypeStatus CommandType = "status"

	// CommandTypeCancel 取消操作
	CommandTypeCancel CommandType = "cancel"
)

// CommandStatus 指令状态
type CommandStatus string

const (
	// CommandStatusPending 待执行
	CommandStatusPending CommandStatus = "pending"

	// CommandStatusExecuting 执行中
	CommandStatusExecuting CommandStatus = "executing"

	// CommandStatusCompleted 已完成
	CommandStatusCompleted CommandStatus = "completed"

	// CommandStatusFailed 执行失败
	CommandStatusFailed CommandStatus = "failed"

	// CommandStatusCanceled 已取消
	CommandStatusCanceled CommandStatus = "canceled"
)

// RemoteCommand 远程指令
type RemoteCommand struct {
	// ID 指令唯一标识
	ID string

	// Type 指令类型
	Type CommandType

	// SessionID 会话ID
	SessionID string

	// TenantID 租户ID
	TenantID string

	// IssuerID 操作人ID
	IssuerID string

	// IssuerName 操作人名称
	IssuerName string

	// Parameters 指令参数
	Parameters map[string]any

	// Status 执行状态
	Status CommandStatus

	// Result 执行结果
	Result map[string]any

	// Error 错误信息
	Error string

	// CreatedAt 创建时间
	CreatedAt time.Time

	// ExecutedAt 执行时间
	ExecutedAt *time.Time

	// CompletedAt 完成时间
	CompletedAt *time.Time
}

// CommandExecutor 指令执行器接口
type CommandExecutor interface {
	// Execute 执行指令
	Execute(ctx context.Context, cmd *RemoteCommand) error
}

// SessionManager 会话管理器接口
type SessionManager interface {
	// GetSession 获取会话
	GetSession(ctx context.Context, sessionID string) (*domain.PipelineRequest, error)

	// PauseSession 暂停会话
	PauseSession(ctx context.Context, sessionID string) error

	// ResumeSession 恢复会话
	ResumeSession(ctx context.Context, sessionID string) error

	// TerminateSession 终止会话
	TerminateSession(ctx context.Context, sessionID, reason string) error

	// GetSessionState 获取会话状态
	GetSessionState(ctx context.Context, sessionID string) (*sessionstate.SessionStateMachine, error)
}

// AuthorizationChecker 权限检查器接口
type AuthorizationChecker interface {
	// CanExecute 检查是否可以执行指令
	CanExecute(issuerID string, cmdType CommandType, tenantID string) bool

	// GetUserRole 获取用户角色
	GetUserRole(issuerID string, tenantID string) (string, error)
}

// AuditLogger 审计日志记录器接口
type AuditLogger interface {
	// LogCommand 记录指令
	LogCommand(ctx context.Context, cmd *RemoteCommand) error
}

// CommandFilter 指令查询过滤器
type CommandFilter struct {
	// TenantID 租户ID
	TenantID string

	// SessionID 会话ID
	SessionID string

	// IssuerID 操作人ID
	IssuerID string

	// Type 指令类型
	Type CommandType

	// Status 指令状态
	Status CommandStatus

	// StartTime 开始时间
	StartTime *time.Time

	// EndTime 结束时间
	EndTime *time.Time

	// Limit 返回数量限制
	Limit int

	// Offset 偏移量
	Offset int
}

// SessionInspection 会话检查结果
type SessionInspection struct {
	// SessionID 会话ID
	SessionID string

	// TenantID 租户ID
	TenantID string

	// CurrentState 当前状态
	CurrentState sessionstate.SessionState

	// CurrentPhase 当前阶段
	CurrentPhase sessionstate.SessionPhase

	// StateHistory 状态历史
	StateHistory []sessionstate.StateChange

	// Metrics 指标
	Metrics sessionstate.SessionMetrics

	// Metadata 元数据
	Metadata map[string]any

	// InspectedAt 检查时间
	InspectedAt time.Time
}

// CommandResult 指令执行结果
type CommandResult struct {
	// Success 是否成功
	Success bool

	// Message 结果消息
	Message string

	// Data 结果数据
	Data map[string]any

	// Error 错误信息
	Error string
}

// Permission 权限定义
type Permission string

const (
	// PermissionPauseSession 暂停会话权限
	PermissionPauseSession Permission = "pause_session"

	// PermissionResumeSession 恢复会话权限
	PermissionResumeSession Permission = "resume_session"

	// PermissionTerminateSession 终止会话权限
	PermissionTerminateSession Permission = "terminate_session"

	// PermissionInspectSession 检查会话权限
	PermissionInspectSession Permission = "inspect_session"

	// PermissionModifySession 修改会话权限
	PermissionModifySession Permission = "modify_session"
)

// Role 角色定义
type Role string

const (
	// RoleSuperAdmin 超级管理员
	RoleSuperAdmin Role = "super_admin"

	// RoleAdmin 管理员
	RoleAdmin Role = "admin"

	// RoleOperator 运维人员
	RoleOperator Role = "operator"

	// RoleViewer 查看者
	RoleViewer Role = "viewer"
)

// RolePermissions 角色权限映射
var RolePermissions = map[Role][]Permission{
	RoleSuperAdmin: {
		PermissionPauseSession,
		PermissionResumeSession,
		PermissionTerminateSession,
		PermissionInspectSession,
		PermissionModifySession,
	},
	RoleAdmin: {
		PermissionPauseSession,
		PermissionResumeSession,
		PermissionInspectSession,
	},
	RoleOperator: {
		PermissionPauseSession,
		PermissionResumeSession,
		PermissionInspectSession,
	},
	RoleViewer: {
		PermissionInspectSession,
	},
}

// CommandTypeToPermission 指令类型到权限的映射
var CommandTypeToPermission = map[CommandType]Permission{
	CommandTypePause:     PermissionPauseSession,
	CommandTypeResume:    PermissionResumeSession,
	CommandTypeTerminate: PermissionTerminateSession,
	CommandTypeInspect:   PermissionInspectSession,
	CommandTypeModify:    PermissionModifySession,
	CommandTypeStatus:    PermissionInspectSession,
}

// HasPermission 检查角色是否有权限
func HasPermission(role Role, permission Permission) bool {
	permissions, ok := RolePermissions[role]
	if !ok {
		return false
	}

	for _, p := range permissions {
		if p == permission {
			return true
		}
	}

	return false
}
