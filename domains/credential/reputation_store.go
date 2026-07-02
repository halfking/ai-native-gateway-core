// Package credential - Reputation storage interfaces and in-memory implementation.
package credential

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// TimeseriesRow 每日时序指标（与 provider_reputation_timeseries 表一一对应）
type TimeseriesRow struct {
	ID               int64
	ProviderID       string
	Model            string
	Date             time.Time // UTC 00:00 截断
	ReliabilityScore float64
	AvgLatencyMs     float64
	ErrorRate        float64
	RequestCount     int64
	SuccessCount     int64
	BanditAlpha      float64
	BanditBeta       float64
	SuccessRate      float64
	RateLimitErrors  int
	QuotaErrors      int
	AuthErrors       int
	TimeoutErrors    int
	NetworkErrors    int
	OtherErrors      int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ReputationStore 信誉存储接口
type ReputationStore interface {
	// GetTimeseries 获取 (provider, model) 的时序数据，按日期升序
	GetTimeseries(ctx context.Context, providerID, model string, days int) ([]TimeseriesRow, error)

	// SaveTimeseries 保存/覆盖每日指标（同 (provider, model, date) 唯一）
	SaveTimeseries(ctx context.Context, row *TimeseriesRow) error

	// ListProviderModelPairs 列出已存在的 (provider, model) 对（用于扫描所有供应商）
	ListProviderModelPairs(ctx context.Context) ([]ProviderModelKey, error)

	// GetRecentIncidents 获取最近事件（按时间倒序）
	GetRecentIncidents(ctx context.Context, providerID string, model string, days int) ([]Incident, error)

	// GetUnresolvedIncidents 获取未解决事件
	GetUnresolvedIncidents(ctx context.Context, providerID string, model string) ([]Incident, error)

	// RecordIncident 记录事件；返回带 ID 的事件
	RecordIncident(ctx context.Context, incident *Incident) error

	// ResolveIncident 标记事件已解决
	ResolveIncident(ctx context.Context, incidentID int64, resolutionNotes string) error

	// RecordIncidentIfNotExists 在指定窗口内已存在同 (provider, model, type) 的未解决事件时跳过；用于去重
	RecordIncidentIfNotExists(ctx context.Context, incident *Incident, dedupeWindow time.Duration) (created bool, err error)
}

// ProviderModelKey (provider, model) 对
type ProviderModelKey struct {
	ProviderID string
	Model      string
}

// ErrReputationNotFound 未找到
var ErrReputationNotFound = errors.New("credential: reputation not found")

// ---------------------------------------------------------------------------
// InMemoryReputationStore — 线程安全的内存实现（用于测试 / 本地开发）
// ---------------------------------------------------------------------------

// InMemoryReputationStore 内存存储
type InMemoryReputationStore struct {
	mu         sync.RWMutex
	timeseries map[string]*TimeseriesRow // key = providerID|model|date(YYYY-MM-DD)
	incidents  map[int64]*Incident       // incident.ID -> Incident
	nextTS     int64
	nextInc    int64
	now        func() time.Time // 测试时可注入
}

// NewInMemoryReputationStore 创建内存存储
func NewInMemoryReputationStore() *InMemoryReputationStore {
	return &InMemoryReputationStore{
		timeseries: make(map[string]*TimeseriesRow),
		incidents:  make(map[int64]*Incident),
		now:        time.Now,
	}
}

func tsKey(providerID, model string, date time.Time) string {
	return providerID + "|" + model + "|" + date.UTC().Format("2006-01-02")
}

// GetTimeseries 获取时序数据
func (s *InMemoryReputationStore) GetTimeseries(ctx context.Context, providerID, model string, days int) ([]TimeseriesRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if days <= 0 {
		days = 7
	}
	cutoff := s.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -days+1)

	out := make([]TimeseriesRow, 0, days)
	for _, row := range s.timeseries {
		if row.ProviderID != providerID || row.Model != model {
			continue
		}
		if row.Date.Before(cutoff) {
			continue
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// SaveTimeseries 保存每日指标
func (s *InMemoryReputationStore) SaveTimeseries(ctx context.Context, row *TimeseriesRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if row == nil || row.ProviderID == "" || row.Model == "" {
		return errors.New("credential: SaveTimeseries requires provider_id and model")
	}
	row.Date = row.Date.UTC().Truncate(24 * time.Hour)
	if row.CreatedAt.IsZero() {
		row.CreatedAt = s.now()
	}
	row.UpdatedAt = s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	key := tsKey(row.ProviderID, row.Model, row.Date)
	if existing, ok := s.timeseries[key]; ok {
		row.ID = existing.ID
		row.CreatedAt = existing.CreatedAt
	} else {
		s.nextTS++
		row.ID = s.nextTS
	}
	cp := *row
	s.timeseries[key] = &cp
	return nil
}

// ListProviderModelPairs 列出所有 (provider, model) 对
func (s *InMemoryReputationStore) ListProviderModelPairs(ctx context.Context) ([]ProviderModelKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[ProviderModelKey]struct{})
	for _, row := range s.timeseries {
		k := ProviderModelKey{ProviderID: row.ProviderID, Model: row.Model}
		seen[k] = struct{}{}
	}
	out := make([]ProviderModelKey, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderID != out[j].ProviderID {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

// GetRecentIncidents 获取最近事件
func (s *InMemoryReputationStore) GetRecentIncidents(ctx context.Context, providerID string, model string, days int) ([]Incident, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if days <= 0 {
		days = 30
	}
	cutoff := s.now().AddDate(0, 0, -days)

	out := make([]Incident, 0)
	for _, inc := range s.incidents {
		if providerID != "" && inc.ProviderID != providerID {
			continue
		}
		if model != "" && inc.Model != model {
			continue
		}
		if inc.StartedAt.Before(cutoff) {
			continue
		}
		out = append(out, *inc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// GetUnresolvedIncidents 获取未解决事件
func (s *InMemoryReputationStore) GetUnresolvedIncidents(ctx context.Context, providerID string, model string) ([]Incident, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Incident, 0)
	for _, inc := range s.incidents {
		if inc.Resolved {
			continue
		}
		if providerID != "" && inc.ProviderID != providerID {
			continue
		}
		if model != "" && inc.Model != model {
			continue
		}
		out = append(out, *inc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// RecordIncident 记录事件
func (s *InMemoryReputationStore) RecordIncident(ctx context.Context, incident *Incident) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if incident == nil || incident.ProviderID == "" || incident.Type == "" {
		return errors.New("credential: RecordIncident requires provider_id and type")
	}
	if incident.StartedAt.IsZero() {
		incident.StartedAt = s.now()
	}
	if incident.CreatedAt.IsZero() {
		incident.CreatedAt = s.now()
	}
	incident.UpdatedAt = s.now()
	if incident.EndedAt != nil {
		incident.Duration = incident.EndedAt.Sub(incident.StartedAt)
		incident.DurationSeconds = int(incident.Duration.Seconds())
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextInc++
	incident.ID = s.nextInc
	cp := *incident
	s.incidents[incident.ID] = &cp
	return nil
}

// ResolveIncident 标记事件已解决
func (s *InMemoryReputationStore) ResolveIncident(ctx context.Context, incidentID int64, resolutionNotes string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	inc, ok := s.incidents[incidentID]
	if !ok {
		return ErrReputationNotFound
	}
	inc.Resolved = true
	inc.ResolutionNotes = resolutionNotes
	inc.UpdatedAt = s.now()
	if inc.EndedAt == nil {
		ended := s.now()
		inc.EndedAt = &ended
		inc.Duration = ended.Sub(inc.StartedAt)
		inc.DurationSeconds = int(inc.Duration.Seconds())
	}
	return nil
}

// RecordIncidentIfNotExists 在窗口内去重
func (s *InMemoryReputationStore) RecordIncidentIfNotExists(ctx context.Context, incident *Incident, dedupeWindow time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if dedupeWindow <= 0 {
		dedupeWindow = 1 * time.Hour
	}
	cutoff := s.now().Add(-dedupeWindow)
	for _, inc := range s.incidents {
		if inc.ProviderID == incident.ProviderID &&
			inc.Model == incident.Model &&
			inc.Type == incident.Type &&
			!inc.Resolved &&
			inc.StartedAt.After(cutoff) {
			return false, nil
		}
	}
	if incident.StartedAt.IsZero() {
		incident.StartedAt = s.now()
	}
	if incident.CreatedAt.IsZero() {
		incident.CreatedAt = s.now()
	}
	incident.UpdatedAt = s.now()
	s.nextInc++
	incident.ID = s.nextInc
	cp := *incident
	s.incidents[incident.ID] = &cp
	return true, nil
}
