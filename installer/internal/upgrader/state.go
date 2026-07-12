package upgrader

// UpgradeState 升级状态枚举（与 autoupdate/types.go 对齐）
type UpgradeState string

const (
	StatePending         UpgradeState = "pending"
	StateDownloading     UpgradeState = "downloading"
	StateVerifying       UpgradeState = "verifying"
	StateReadyToRestart  UpgradeState = "ready_to_restart"
	StateBackingUp       UpgradeState = "backing_up"
	StateUpgrading       UpgradeState = "upgrading"
	StateSuccess         UpgradeState = "success"
	StateFailed          UpgradeState = "failed"
	StateRolledBack      UpgradeState = "rolled_back"
	StateRollingUpdate   UpgradeState = "rolling_update_started"
	StateRollingProgress UpgradeState = "rolling_update_progress"
	StateRollingComplete UpgradeState = "rolling_update_complete"
)

// UpgradeTransition 升级状态转换定义
type UpgradeTransition struct {
	From []UpgradeState
	To   UpgradeState
}

// ValidTransitions 合法的升级状态转换表
var ValidTransitions = []UpgradeTransition{
	{From: []UpgradeState{StatePending}, To: StateDownloading},
	{From: []UpgradeState{StateDownloading}, To: StateVerifying},
	{From: []UpgradeState{StateDownloading}, To: StateFailed},
	{From: []UpgradeState{StateVerifying}, To: StateReadyToRestart},
	{From: []UpgradeState{StateVerifying}, To: StateFailed},
	{From: []UpgradeState{StateReadyToRestart}, To: StateBackingUp},
	{From: []UpgradeState{StateBackingUp}, To: StateUpgrading},
	{From: []UpgradeState{StateBackingUp}, To: StateFailed},
	{From: []UpgradeState{StateUpgrading}, To: StateSuccess},
	{From: []UpgradeState{StateUpgrading}, To: StateRolledBack},
	{From: []UpgradeState{StateUpgrading}, To: StateFailed},
	{From: []UpgradeState{StateRolledBack}, To: StateUpgrading},
	{From: []UpgradeState{StateFailed}, To: StateDownloading},
}

// RollbackState 回滚状态枚举
type RollbackState string

const (
	RollbackPending    RollbackState = "pending"
	RollbackInProgress RollbackState = "in_progress"
	RollbackComplete   RollbackState = "rolled_back"
	RollbackFailed     RollbackState = "failed"
)

// IsValidTransition 检查状态转换是否合法
func IsValidTransition(current UpgradeState, next UpgradeState) bool {
	for _, t := range ValidTransitions {
		for _, from := range t.From {
			if from == current && t.To == next {
				return true
			}
		}
	}
	return false
}

// TerminalStates 终态列表
var TerminalStates = []UpgradeState{
	StateSuccess,
	StateRolledBack,
}
