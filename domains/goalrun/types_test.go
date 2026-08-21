package goalrun

import (
	"errors"
	"testing"
	"time"
)

// TestCanTransition：状态迁移矩阵（设计 13 §6.3）。
func TestCanTransition(t *testing.T) {
	tests := []struct {
		from Status
		to   Status
		want bool
	}{
		// 合法初始化流程
		{StatusCreated, StatusQueued, true},
		{StatusQueued, StatusRunning, true},

		// 合法等待状态
		{StatusRunning, StatusWaitingTool, true},
		{StatusRunning, StatusWaitingInput, true},
		{StatusRunning, StatusWaitingHandoff, true},
		{StatusRunning, StatusAuditing, true},

		// 合法恢复流程
		{StatusWaitingTool, StatusQueued, true},
		{StatusWaitingInput, StatusQueued, true},
		{StatusWaitingHandoff, StatusRestoring, true},
		{StatusRestoring, StatusQueued, true},

		// 合法终态迁移
		{StatusRunning, StatusCompleted, true},
		{StatusRunning, StatusFailed, true},
		{StatusRunning, StatusCanceled, true},
		{StatusRunning, StatusExpired, true},
		{StatusRunning, StatusManualRequired, true},
		{StatusRunning, StatusResumeSafetyBlocked, true},

		{StatusQueued, StatusCompleted, true},
		{StatusQueued, StatusCanceled, true},
		{StatusWaitingTool, StatusExpired, true},
		{StatusAuditing, StatusCompleted, true},

		// 非法：终态不可回到 runnable
		{StatusCompleted, StatusQueued, false},
		{StatusCompleted, StatusRunning, false},
		{StatusFailed, StatusRunning, false},
		{StatusCanceled, StatusQueued, false},
		{StatusExpired, StatusRunning, false},
		{StatusManualRequired, StatusQueued, false},

		// 非法：resume_safety_blocked 是吸态（只允许未来人工迁出）
		{StatusResumeSafetyBlocked, StatusQueued, false},
		{StatusResumeSafetyBlocked, StatusRunning, false},

		// 非法：跳过中间状态
		{StatusCreated, StatusRunning, false},
		{StatusQueued, StatusWaitingTool, false},
		{StatusWaitingHandoff, StatusQueued, false}, // 必须先 restoring

		// 相同状态总是合法（幂等）
		{StatusQueued, StatusQueued, true},
		{StatusRunning, StatusRunning, true},
		{StatusCompleted, StatusCompleted, true},
	}

	for _, tt := range tests {
		got := CanTransition(tt.from, tt.to)
		if got != tt.want {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

// TestIsTerminalStatus：终态检测（设计 13 §6.3）。
func TestIsTerminalStatus(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusCompleted, true},
		{StatusFailed, true},
		{StatusCanceled, true},
		{StatusExpired, true},
		{StatusManualRequired, true},
		// resume_safety_blocked 不是终态，但同样不可自动恢复
		{StatusResumeSafetyBlocked, false},
		// 非终态
		{StatusCreated, false},
		{StatusQueued, false},
		{StatusRunning, false},
		{StatusWaitingTool, false},
		{StatusWaitingInput, false},
		{StatusWaitingHandoff, false},
		{StatusAuditing, false},
		{StatusRestoring, false},
	}

	for _, tt := range tests {
		got := IsTerminalStatus(tt.status)
		if got != tt.want {
			t.Errorf("IsTerminalStatus(%s) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// TestNewGoalRunInput_Validate：必填字段校验。
func TestNewGoalRunInput_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*NewGoalRunInput)
		wantErr error
	}{
		{"valid", func(i *NewGoalRunInput) {}, nil},
		{"missing tenant_id", func(i *NewGoalRunInput) { i.TenantID = "" }, ErrTenantIDRequired},
		{"missing api_key_id", func(i *NewGoalRunInput) { i.APIKeyID = "" }, ErrAPIKeyIDRequired},
		{"missing root_session_id", func(i *NewGoalRunInput) { i.RootSessionID = "" }, ErrRootSessionIDRequired},
		{"missing root_request_id", func(i *NewGoalRunInput) { i.RootRequestID = "" }, ErrRootRequestIDRequired},
		{"missing instruction_hash", func(i *NewGoalRunInput) { i.InstructionHash = "" }, ErrInstructionHashRequired},
		{"missing deadline_at", func(i *NewGoalRunInput) { i.DeadlineAt = time.Time{} }, ErrDeadlineRequired},
		{"missing lease_owner", func(i *NewGoalRunInput) { i.LeaseOwner = "" }, ErrLeaseOwnerRequired},
		{"missing lease_until", func(i *NewGoalRunInput) { i.LeaseUntil = time.Time{} }, ErrLeaseUntilRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := NewGoalRunInput{
				TenantID:        "tenant-1",
				APIKeyID:        "key-1",
				RootSessionID:   "sess-1",
				RootRequestID:   "req-1",
				InstructionHash: "hash-abc",
				DeadlineAt:      time.Now().Add(24 * time.Hour),
				LeaseOwner:      "gw-1",
				LeaseUntil:      time.Now().Add(time.Hour),
			}
			tt.mutate(&input)
			err := input.Validate()
			if (err == nil) != (tt.wantErr == nil) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
			if err != nil && tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
