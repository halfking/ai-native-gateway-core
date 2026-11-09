package mcp

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// AuditLog 是审计日志：内存环形缓冲 + 可选 JSONL 落盘。
//
// 环形缓冲保留最近 N 条（默认 200，与既有 tools/mcp-gateway 的审计环一致），
// 供管理端点快速查看；JSONL 落盘用于长期留存与 SIEM 导出。
type AuditLog struct {
	mu sync.RWMutex

	ring    []AuditEvent
	maxSize int

	fileMu sync.Mutex
	file   *os.File
	writer *bufio.Writer
}

// NewAuditLog 构造审计日志。
//
// filePath 为空表示只保留内存环形缓冲（不落盘）。
func NewAuditLog(maxSize int, filePath string) (*AuditLog, error) {
	if maxSize <= 0 {
		maxSize = 200
	}
	l := &AuditLog{maxSize: maxSize}
	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		l.file = f
		l.writer = bufio.NewWriter(f)
	}
	return l, nil
}

// Record 记录一条审计事件。
func (l *AuditLog) Record(ev AuditEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	l.mu.Lock()
	l.ring = append(l.ring, ev)
	if len(l.ring) > l.maxSize {
		l.ring = l.ring[len(l.ring)-l.maxSize:]
	}
	l.mu.Unlock()

	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if l.writer == nil {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = l.writer.Write(raw)
	_ = l.writer.WriteByte('\n')
	// 审计落盘是 best-effort：写失败不打断数据面（与仓内 shadow-write 语义一致）。
	_ = l.writer.Flush()
}

// Recent 返回最近 n 条事件（按时间倒序）。
func (l *AuditLog) Recent(n int) []AuditEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if n <= 0 || n > len(l.ring) {
		n = len(l.ring)
	}
	out := make([]AuditEvent, 0, n)
	for i := len(l.ring) - 1; i >= len(l.ring)-n; i-- {
		out = append(out, l.ring[i])
	}
	return out
}

// Query 按条件过滤审计事件（user/tool/key/adapter/kind），按时间倒序。
func (l *AuditLog) Query(filter AuditFilter) []AuditEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]AuditEvent, 0, len(l.ring))
	for i := len(l.ring) - 1; i >= 0; i-- {
		ev := l.ring[i]
		if filter.TenantID != "" && ev.TenantID != filter.TenantID {
			continue
		}
		if filter.UserID != "" && ev.UserID != filter.UserID {
			continue
		}
		if filter.KeyID != "" && ev.KeyID != filter.KeyID {
			continue
		}
		if filter.Tool != "" && ev.Tool != filter.Tool {
			continue
		}
		if filter.Adapter != "" && ev.Adapter != filter.Adapter {
			continue
		}
		if filter.Kind != "" && ev.Kind != filter.Kind {
			continue
		}
		if filter.Outcome != "" && ev.Outcome != filter.Outcome {
			continue
		}
		if !filter.Since.IsZero() && ev.At.Before(filter.Since) {
			continue
		}
		out = append(out, ev)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out
}

// AuditFilter 是审计查询条件。
type AuditFilter struct {
	TenantID string
	UserID   string
	KeyID    string
	Tool     string
	Adapter  string
	Kind     string
	Outcome  string
	Since    time.Time
	Limit    int
}

// ExportJSONL 导出审计为 JSONL（SIEM 兼容），返回导出条数与字节数。
//
// 导出内容只含审计字段本身，不含任何凭据明文。
func (l *AuditLog) ExportJSONL(w *bufio.Writer) (int, error) {
	return l.ExportFilteredJSONL(w, AuditFilter{})
}

// ExportFilteredJSONL exports only the authorized slice of the audit log.
func (l *AuditLog) ExportFilteredJSONL(w *bufio.Writer, filter AuditFilter) (int, error) {
	events := l.Query(filter)
	count := 0
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			return count, err
		}
		if _, err := w.Write(raw); err != nil {
			return count, err
		}
		if err := w.WriteByte('\n'); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// Stats 返回审计概览（按结果与类别计数）。
func (l *AuditLog) Stats() map[string]any {
	return l.FilteredStats(AuditFilter{})
}

// FilteredStats keeps aggregate counts within the same boundary as event rows.
func (l *AuditLog) FilteredStats(filter AuditFilter) map[string]any {
	filter.Limit = 0
	events := l.Query(filter)
	byOutcome := map[string]int{}
	byKind := map[string]int{}
	tools := map[string]int{}
	for _, ev := range events {
		byOutcome[ev.Outcome]++
		byKind[ev.Kind]++
		if ev.Tool != "" {
			tools[ev.Tool]++
		}
	}
	return map[string]any{
		"total":      len(events),
		"by_outcome": byOutcome,
		"by_kind":    byKind,
		"by_tool":    tools,
	}
}

// Close 关闭落盘句柄。
func (l *AuditLog) Close() error {
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if l.writer != nil {
		_ = l.writer.Flush()
	}
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// SortedCounts 返回按计数倒序的字典视图（面板用，避免 JSON key 无序）。
func SortedCounts(m map[string]int) []struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
} {
	out := make([]struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}, 0, len(m))
	for k, v := range m {
		out = append(out, struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
