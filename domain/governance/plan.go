package governance

import "time"

// ToolCallSpec 单个客户端 tool 调用规格。
type ToolCallSpec struct {
	Name          string
	Args          map[string]any
	RequireResult bool
	TimeoutMs     int
}

// ToolExecutionPlan 客户端 tool 执行计划。
//
// 由 domains/interception.ToolOrchestrator 产出；治理引擎在 Decision.Kind =
// Suspend && WaitFor=Tool 时挂起会话等待结果。
type ToolExecutionPlan struct {
	Tools     []ToolCallSpec
	MustAll   bool
	Timeout   time.Duration
	OnFailure string
}

// DecisionStep 决策追踪步骤。
//
// 用于决策可观测与回放；每个 verdict/decision 在产生时记录一条。
type DecisionStep struct {
	Stage      string
	PluginName string
	Verdict    string
	Decision   DecisionKind
	LatencyMs  int64
	At         time.Time
}
