package goalrun

import "errors"

// Store 错误哨兵。
var (
	// ErrLeaseLost 更新影响 0 行：租约已过期/被抢占或 GoalRun 已越过状态边界。
	ErrLeaseLost = errors.New("goalrun: lease lost or run transitioned (0 rows)")
	// ErrDuplicateGoalRun 同 (tenant_id, root_goal_id) 或 (tenant_id, root_request_id) 已存在。
	ErrDuplicateGoalRun = errors.New("goalrun: goal run already exists")
	// ErrVersionConflict expected_version 不匹配（CAS 冲突）。
	ErrVersionConflict = errors.New("goalrun: version conflict")
	// ErrInvalidTransition 状态迁移非法。
	ErrInvalidTransition = errors.New("goalrun: invalid status transition")
	// ErrSequenceConflict step sequence 已存在。
	ErrSequenceConflict = errors.New("goalrun: step sequence conflict")
	// ErrActionNotFound action 不存在。
	ErrActionNotFound = errors.New("goalrun: action not found")
	// ErrGoalRunNotFound GoalRun 不存在。
	ErrGoalRunNotFound = errors.New("goalrun: goal run not found")
)

// Validation 错误。
var (
	ErrTenantIDRequired         = errors.New("goalrun: TenantID required")
	ErrAPIKeyIDRequired         = errors.New("goalrun: APIKeyID required")
	ErrRootSessionIDRequired    = errors.New("goalrun: RootSessionID required")
	ErrRootRequestIDRequired    = errors.New("goalrun: RootRequestID required")
	ErrInstructionHashRequired  = errors.New("goalrun: InstructionHash required")
	ErrDeadlineRequired         = errors.New("goalrun: DeadlineAt required")
	ErrLeaseOwnerRequired       = errors.New("goalrun: LeaseOwner required")
	ErrLeaseUntilRequired       = errors.New("goalrun: LeaseUntil required")
	ErrGoalRunIDRequired        = errors.New("goalrun: GoalRunID required")
	ErrRequestIDRequired        = errors.New("goalrun: RequestID required")
	ErrActionTypeRequired       = errors.New("goalrun: ActionType required")
	ErrIdempotencyKeyRequired   = errors.New("goalrun: IdempotencyKey required")
)
