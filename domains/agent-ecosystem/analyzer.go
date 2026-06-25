package agentecosystem

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// BehaviorAnalyzer 行为分析器（线程安全）。
//
// 用法：
//   - 调用 Record 上报一条 Behavior
//   - 调用 GetStats 获取某个 agent 的聚合统计
//   - 调用 GetBehaviors 获取原始记录
type BehaviorAnalyzer struct {
	mu      sync.RWMutex
	records map[string][]*Behavior // agentID -> behaviors（按 RecordedAt 升序）
}

// NewBehaviorAnalyzer 构造一个空的 BehaviorAnalyzer。
func NewBehaviorAnalyzer() *BehaviorAnalyzer {
	return &BehaviorAnalyzer{records: make(map[string][]*Behavior)}
}

// Record 记录一条行为。
func (a *BehaviorAnalyzer) Record(b *Behavior) error {
	if b == nil {
		return errors.New("agentecosystem: nil behavior")
	}
	if b.AgentID == "" {
		return errors.New("agentecosystem: behavior AgentID required")
	}
	if b.RecordedAt.IsZero() {
		b.RecordedAt = time.Now()
	}
	a.mu.Lock()
	a.records[b.AgentID] = append(a.records[b.AgentID], b)
	a.mu.Unlock()
	return nil
}

// GetBehaviors 获取某个 agent 的全部行为记录（深拷贝副本，避免外部修改污染内部状态）。
func (a *BehaviorAnalyzer) GetBehaviors(agentID string) []*Behavior {
	a.mu.RLock()
	defer a.mu.RUnlock()
	src := a.records[agentID]
	if len(src) == 0 {
		return nil
	}
	out := make([]*Behavior, len(src))
	for i, b := range src {
		cp := *b
		if b.Metadata != nil {
			cp.Metadata = make(map[string]any, len(b.Metadata))
			for k, v := range b.Metadata {
				cp.Metadata[k] = v
			}
		}
		out[i] = &cp
	}
	return out
}

// GetStats 获取 agent 行为统计。
// 当无记录时返回仅含 AgentID 的零值 Stats。
func (a *BehaviorAnalyzer) GetStats(agentID string) *Stats {
	a.mu.RLock()
	defer a.mu.RUnlock()

	behaviors := a.records[agentID]
	if len(behaviors) == 0 {
		return &Stats{AgentID: agentID}
	}

	stats := &Stats{AgentID: agentID, TotalCount: len(behaviors)}
	var totalLatency int64
	var firstAt, lastAt time.Time
	for i, b := range behaviors {
		if b.Success {
			stats.SuccessCount++
		} else {
			stats.FailureCount++
		}
		totalLatency += b.LatencyMs
		if i == 0 || b.RecordedAt.Before(firstAt) {
			firstAt = b.RecordedAt
		}
		if i == 0 || b.RecordedAt.After(lastAt) {
			lastAt = b.RecordedAt
		}
	}
	stats.AvgLatencyMs = totalLatency / int64(len(behaviors))
	stats.FirstAt = firstAt
	stats.LastAt = lastAt
	return stats
}

// Reset 清空某个 agent 的记录。
func (a *BehaviorAnalyzer) Reset(agentID string) {
	a.mu.Lock()
	delete(a.records, agentID)
	a.mu.Unlock()
}

// Stats 行为聚合统计。
type Stats struct {
	AgentID      string
	TotalCount   int
	SuccessCount int
	FailureCount int
	AvgLatencyMs int64
	FirstAt      time.Time
	LastAt       time.Time
}

// SuccessRate 成功率（0.0 - 1.0）。
func (s *Stats) SuccessRate() float64 {
	if s == nil || s.TotalCount == 0 {
		return 0
	}
	return float64(s.SuccessCount) / float64(s.TotalCount)
}

// FailureRate 失败率（0.0 - 1.0）。
func (s *Stats) FailureRate() float64 {
	if s == nil || s.TotalCount == 0 {
		return 0
	}
	return float64(s.FailureCount) / float64(s.TotalCount)
}

// TopFailingActions 返回失败次数最多的 N 个 action（用于行为画像）。
func (a *BehaviorAnalyzer) TopFailingActions(agentID string, n int) []ActionCount {
	a.mu.RLock()
	defer a.mu.RUnlock()
	counts := make(map[string]int)
	for _, b := range a.records[agentID] {
		if !b.Success {
			counts[b.Action]++
		}
	}
	out := make([]ActionCount, 0, len(counts))
	for action, c := range counts {
		out = append(out, ActionCount{Action: action, Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// ActionCount action 失败次数。
type ActionCount struct {
	Action string
	Count  int
}
