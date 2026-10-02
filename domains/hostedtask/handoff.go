// hostedtask/handoff.go — 召回移交包（§3.3 ④ 轻量快照路径，R65 P1 子集）。
//
// 轻量路径不走 ACC v3 transfer 状态机、不动 ACC 权威状态：非终态任务由
// RecallTask 在同事务内做 cancelled 终态抢占后快照。完整 P1 路径还含线层
// 导出（request_logs turns 摘要）、续层指针（pi native session id 四元索引）
// 与知层（Memora scope_chain + context-manifest + compress 引用）。
package hostedtask

// StructuredHandoffPacket 是召回时交付给调用方的结构化移交包（§3.3 ④）。
type StructuredHandoffPacket struct {
	Goal              string         `json:"goal"`
	CurrentResult     map[string]any `json:"current_result"`
	DoneWhen          string         `json:"done_when,omitempty"`
	Blockers          []string       `json:"blockers"`
	NextOwner         string         `json:"next_owner"`
	RequiresInputFrom []string       `json:"requires_input_from"`
	ArtifactRefs      []any          `json:"artifact_refs"`
}

// buildHandoffPacket 从任务投影组装 §3.3 ④ 移交包（纯函数；RecallTask 以
// 抢占后的最终行调用，保证包与事件台账一致）。currentResult 直接取
// hosted_tasks.result 快照；blockers/requiresInputFrom 按状态机最小推导
// （needs_review → 人工对账；failed → 尽力带 error 详情）。
func buildHandoffPacket(t *Task) StructuredHandoffPacket {
	result := t.Result
	if result == nil {
		result = map[string]any{}
	}
	packet := StructuredHandoffPacket{
		Goal:              t.Goal,
		CurrentResult:     result,
		DoneWhen:          t.DoneWhen,
		Blockers:          []string{},
		NextOwner:         "recall_caller",
		RequiresInputFrom: []string{},
		ArtifactRefs:      []any{},
	}
	switch t.Status {
	case StatusNeedsReview:
		packet.Blockers = append(packet.Blockers,
			"needs_review: outcome unverifiable; manual reconciliation required")
		packet.RequiresInputFrom = append(packet.RequiresInputFrom, "human")
	case StatusFailed:
		if msg, _ := t.Result["error"].(string); msg != "" {
			packet.Blockers = append(packet.Blockers, "failed: "+msg)
		} else {
			packet.Blockers = append(packet.Blockers, "failed: no error detail recorded")
		}
	case StatusCancelled, StatusExpired:
		packet.Blockers = append(packet.Blockers, string(t.Status)+": task did not run to completion")
	}
	// artifact_refs：result 权威优先，回落创建时 context.artifacts。
	if refs, ok := t.Result["artifact_refs"].([]any); ok && len(refs) > 0 {
		packet.ArtifactRefs = refs
	} else if refs, ok := t.Context["artifacts"].([]any); ok && len(refs) > 0 {
		packet.ArtifactRefs = refs
	}
	return packet
}
