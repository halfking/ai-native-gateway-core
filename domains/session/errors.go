package session

import "errors"

// 审批相关错误定义
var (
	// ErrApprovalPending 表示审批请求正在进行中
	// 当会话进入审批流程时返回此错误，通知 handler 暂停处理
	ErrApprovalPending = errors.New("approval request is pending")

	// ErrApprovalTimeout 表示审批请求超时
	// 当审批在规定时间内未完成时返回此错误
	ErrApprovalTimeout = errors.New("approval request timeout")

	// ErrApprovalRejected 表示审批请求被拒绝
	// 当审批人拒绝审批请求时返回此错误
	ErrApprovalRejected = errors.New("approval request rejected")
)
