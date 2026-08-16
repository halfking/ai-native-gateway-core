package handoff

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal" //nolint:depguard // handoff snapshots the existing Goal contract
)

const GoalStateVersion = 1

// GoalState is the versioned state transferred to a new Goal session.
type GoalState struct {
	Version           int       `json:"version"`
	SourceSessionID   string    `json:"source_session_id"`
	TenantID          string    `json:"tenant_id"`
	State             string    `json:"state"`
	CostMode          string    `json:"cost_mode,omitempty"`
	RetryCount        int       `json:"retry_count"`
	DecisionCount     int       `json:"decision_count"`
	AutoContinueCount int       `json:"auto_continue_count"`
	RepeatCount       int       `json:"repeat_count"`
	ModelSwitchCount  int       `json:"model_switch_count"`
	CurrentModel      string    `json:"current_model,omitempty"`
	TokensUsed        int       `json:"tokens_used"`
	MessageCount      int       `json:"message_count"`
	TaskDescription   string    `json:"task_description,omitempty"`
	AuditCompleted    bool      `json:"audit_completed"`
	CompletedSteps    []string  `json:"completed_steps,omitempty"`
	RemainingWork     string    `json:"remaining_work,omitempty"`
	CapturedAt        time.Time `json:"captured_at"`
}

// GoalStateInput supplies request-local fields not held by GoalStore.
type GoalStateInput struct {
	TenantID       string
	SessionID      string
	CostMode       string
	TokensUsed     int
	MessageCount   int
	CompletedSteps []string
	RemainingWork  string
}

// GoalStateSerializer snapshots and restores Goal sessions.
type GoalStateSerializer interface {
	Serialize(ctx context.Context, input GoalStateInput) (*GoalState, error)
	Restore(ctx context.Context, newSessionID string, state *GoalState) error
}

// MemoryGoalStateSerializer uses GoalStore for session state and keeps no
// durable proposal data. The transport owns the in-memory proposal binding.
type MemoryGoalStateSerializer struct {
	store goal.GoalStore
	now   func() time.Time
}

// NewMemoryGoalStateSerializer creates a serializer backed by the supplied
// GoalStore. Proposal persistence remains owned by the handoff transport.
func NewMemoryGoalStateSerializer(store goal.GoalStore) *MemoryGoalStateSerializer {
	return &MemoryGoalStateSerializer{store: store, now: time.Now}
}

func (s *MemoryGoalStateSerializer) Serialize(ctx context.Context, input GoalStateInput) (*GoalState, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("goal state store is unavailable")
	}
	if input.SessionID == "" {
		return nil, fmt.Errorf("goal source session is required")
	}
	session, err := s.store.GetSession(ctx, input.TenantID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("get goal session %q: %w", input.SessionID, err)
	}
	if session == nil {
		return nil, nil
	}
	return &GoalState{
		Version:           GoalStateVersion,
		SourceSessionID:   session.SessionID,
		TenantID:          session.TenantID,
		State:             string(session.State),
		CostMode:          input.CostMode,
		RetryCount:        session.RetryCount,
		DecisionCount:     session.DecisionCount,
		AutoContinueCount: session.AutoContinueCount,
		RepeatCount:       session.RepeatCount,
		ModelSwitchCount:  session.ModelSwitchCount,
		CurrentModel:      session.CurrentModel,
		TokensUsed:        input.TokensUsed,
		MessageCount:      input.MessageCount,
		TaskDescription:   session.OriginalGoal,
		AuditCompleted:    len(session.AuditResult) > 0,
		CompletedSteps:    append([]string(nil), input.CompletedSteps...),
		RemainingWork:     input.RemainingWork,
		CapturedAt:        s.now().UTC(),
	}, nil
}

func (s *MemoryGoalStateSerializer) Restore(ctx context.Context, newSessionID string, state *GoalState) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("goal state store is unavailable")
	}
	if newSessionID == "" || state == nil || state.SourceSessionID == "" {
		return fmt.Errorf("goal restore requires target session and state")
	}
	if state.Version != GoalStateVersion {
		return fmt.Errorf("unsupported goal state version %d", state.Version)
	}
	if existing, err := s.store.GetSession(ctx, state.TenantID, newSessionID); err != nil {
		return fmt.Errorf("get target goal session %q: %w", newSessionID, err)
	} else if existing != nil {
		if existing.TenantID == state.TenantID && existing.OriginalGoal == state.TaskDescription && existing.State == goal.StateActive &&
			existing.RetryCount == 0 && existing.DecisionCount == 0 && existing.AutoContinueCount == 0 &&
			existing.RepeatCount == 0 && existing.ModelSwitchCount == 0 && existing.CurrentModel == state.CurrentModel {
			return nil
		}
		return fmt.Errorf("target goal session %q conflicts with handoff state", newSessionID)
	}
	now := s.now().UTC()
	return s.store.CreateSession(ctx, &goal.Session{
		SessionID:      newSessionID,
		TenantID:       state.TenantID,
		State:          goal.StateActive,
		OriginalGoal:   state.TaskDescription,
		CurrentModel:   state.CurrentModel,
		LastActivityAt: now,
		CreatedAt:      now,
	})
}
