package orchestration

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

type ActionState string

const (
	ActionPending    ActionState = "pending"
	ActionRunning    ActionState = "running"
	ActionCompleted  ActionState = "completed"
	ActionFailed     ActionState = "failed"
	ActionSuspended  ActionState = "suspended"
	ActionDeadLetter ActionState = "dead_letter"
)

var (
	ErrRunHasActiveAction = fmt.Errorf("run has active action")
	ErrLeaseLost          = fmt.Errorf("action lease lost")
	ErrActionNotFound     = fmt.Errorf("action not found")
	ErrNoActionAvailable  = fmt.Errorf("no action available")
	ErrSequenceRegression = fmt.Errorf("action sequence must increase")
)

type Action struct {
	ID            string
	RunID         string
	Sequence      int64
	BindingID     string
	Capability    string
	TenantID      string
	Model         string
	FailurePolicy pluginruntime.FailurePolicy
	State         ActionState
	LeaseID       string
	WorkerID      string
	LeaseUntil    time.Time
	LastError     string
}

type ActionClaim struct {
	Action  Action
	LeaseID string
}

type SchedulerConfig struct {
	LeaseTTL           time.Duration
	BindingConcurrency map[string]int
	Now                func() time.Time
}

type Scheduler struct {
	mu            sync.Mutex
	cfg           SchedulerConfig
	actions       map[string]*Action
	activeRun     map[string]string
	activeBinding map[string]int
	leaseSeq      atomic.Uint64
}

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Scheduler{cfg: cfg, actions: make(map[string]*Action), activeRun: make(map[string]string), activeBinding: make(map[string]int)}
}

func (s *Scheduler) Enqueue(action Action) error {
	if s == nil {
		return fmt.Errorf("enqueue action failed: scheduler unavailable (action_id=%s)", action.ID)
	}
	if action.ID == "" || action.RunID == "" || action.BindingID == "" || action.Sequence <= 0 {
		return fmt.Errorf("enqueue action failed: required identity missing (action_id=%s, run_id=%s)", action.ID, action.RunID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.actions[action.ID]; exists {
		return fmt.Errorf("enqueue action failed: duplicate action (action_id=%s)", action.ID)
	}
	if currentID := s.activeRun[action.RunID]; currentID != "" {
		current := s.actions[currentID]
		if current != nil && action.Sequence <= current.Sequence {
			return ErrSequenceRegression
		}
		if current != nil && current.State != ActionCompleted && current.State != ActionFailed && current.State != ActionSuspended && current.State != ActionDeadLetter {
			return ErrRunHasActiveAction
		}
	}
	action.State = ActionPending
	s.actions[action.ID] = &action
	s.activeRun[action.RunID] = action.ID
	return nil
}

func (s *Scheduler) Claim(runID, workerID string) (ActionClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	actionID := s.activeRun[runID]
	action := s.actions[actionID]
	if action == nil {
		return ActionClaim{}, ErrNoActionAvailable
	}
	now := s.cfg.Now()
	if action.State == ActionRunning && now.Before(action.LeaseUntil) {
		return ActionClaim{}, ErrNoActionAvailable
	}
	if action.State != ActionPending && action.State != ActionRunning {
		return ActionClaim{}, ErrNoActionAvailable
	}
	if action.State == ActionRunning && s.activeBinding[action.BindingID] > 0 {
		s.activeBinding[action.BindingID]--
	}
	if limit := s.cfg.BindingConcurrency[action.BindingID]; limit > 0 && s.activeBinding[action.BindingID] >= limit {
		return ActionClaim{}, ErrNoActionAvailable
	}
	leaseID := fmt.Sprintf("lease-%d", s.leaseSeq.Add(1))
	action.State = ActionRunning
	action.WorkerID = workerID
	action.LeaseID = leaseID
	action.LeaseUntil = now.Add(s.cfg.LeaseTTL)
	s.activeBinding[action.BindingID]++
	return ActionClaim{Action: *action, LeaseID: leaseID}, nil
}

func (s *Scheduler) Complete(leaseID string, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var action *Action
	for _, candidate := range s.actions {
		if candidate.LeaseID == leaseID {
			action = candidate
			break
		}
	}
	if action == nil || action.State != ActionRunning {
		return ErrLeaseLost
	}
	if !s.cfg.Now().Before(action.LeaseUntil) {
		return ErrLeaseLost
	}
	if s.activeBinding[action.BindingID] > 0 {
		s.activeBinding[action.BindingID]--
	}
	action.LeaseID = ""
	action.LeaseUntil = time.Time{}
	if cause == nil {
		action.State = ActionCompleted
		return nil
	}
	action.LastError = cause.Error()
	switch action.FailurePolicy {
	case pluginruntime.FailureSuspend:
		action.State = ActionSuspended
	case pluginruntime.FailureRetryDLQ:
		action.State = ActionDeadLetter
	default:
		action.State = ActionFailed
	}
	return nil
}

func (s *Scheduler) Get(actionID string) (Action, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	action, ok := s.actions[actionID]
	if !ok {
		return Action{}, false
	}
	return *action, true
}
