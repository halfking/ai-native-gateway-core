package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	// ErrNoAvailableCredential 表示没有可用凭据
	ErrNoAvailableCredential = errors.New("no available credential")
)

// Scheduler 是凭据调度器接口
type Scheduler interface {
	// Select 选择一个凭据
	Select(ctx context.Context) (*Credential, error)

	// Release 释放凭据
	Release(cred *Credential)

	// UpdateWeight 更新凭据权重
	UpdateWeight(credID int, weight int)

	// Metrics 返回调度统计
	Metrics() SchedulerMetrics
}

// Credential 是凭据对象
type Credential struct {
	ID         int
	ProviderID int
	APIKey     string
	Quota      int // 每分钟配额
}

// SchedulerMetrics 是调度统计
type SchedulerMetrics struct {
	TotalSelections int64
	PerCredential   map[int]int64 // credID → 选择次数
}

// WRRScheduler 是 Weighted Round-Robin 调度器
type WRRScheduler struct {
	credentials []*WeightedCredential
	mu          sync.Mutex

	totalSelections int64
	perCredential   map[int]int64
}

// WeightedCredential 是带权重的凭据
type WeightedCredential struct {
	Credential      *Credential
	Weight          int // 配置权重 (固定)
	CurrentWeight   int // 动态权重 (调度时更新)
	EffectiveWeight int // 有效权重 (根据健康状况调整)
}

// NewWRRScheduler 创建 WRR 调度器
func NewWRRScheduler(credentials []*Credential) *WRRScheduler {
	weighted := make([]*WeightedCredential, len(credentials))
	for i, cred := range credentials {
		weighted[i] = &WeightedCredential{
			Credential:      cred,
			Weight:          cred.Quota, // 权重初始为配额
			CurrentWeight:   0,
			EffectiveWeight: cred.Quota,
		}
	}

	return &WRRScheduler{
		credentials:   weighted,
		perCredential: make(map[int]int64),
	}
}

// Select 选择一个凭据 (Smooth WRR 算法)
func (s *WRRScheduler) Select(ctx context.Context) (*Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.credentials) == 0 {
		return nil, ErrNoAvailableCredential
	}

	// Smooth WRR 算法
	best := s.selectBest()
	if best == nil {
		return nil, ErrNoAvailableCredential
	}

	// 统计
	atomic.AddInt64(&s.totalSelections, 1)
	s.perCredential[best.Credential.ID]++

	return best.Credential, nil
}

// selectBest 使用 Smooth WRR 算法选择最佳凭据
func (s *WRRScheduler) selectBest() *WeightedCredential {
	var best *WeightedCredential
	total := 0

	// 1. 计算 total，更新 current_weight
	for _, cred := range s.credentials {
		cred.CurrentWeight += cred.EffectiveWeight
		total += cred.EffectiveWeight

		if best == nil || cred.CurrentWeight > best.CurrentWeight {
			best = cred
		}
	}

	// 2. best.current_weight -= total
	if best != nil && total > 0 {
		best.CurrentWeight -= total
	}

	return best
}

// Release 释放凭据
func (s *WRRScheduler) Release(cred *Credential) {
	// 目前无需特殊处理
	// 如果需要追踪"正在使用"状态，可在此实现
}

// UpdateWeight 更新凭据权重
func (s *WRRScheduler) UpdateWeight(credID int, weight int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, cred := range s.credentials {
		if cred.Credential.ID == credID {
			cred.EffectiveWeight = weight
			return
		}
	}
}

// Metrics 返回调度统计
func (s *WRRScheduler) Metrics() SchedulerMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 复制 map 避免并发问题
	perCredCopy := make(map[int]int64, len(s.perCredential))
	for k, v := range s.perCredential {
		perCredCopy[k] = v
	}

	return SchedulerMetrics{
		TotalSelections: atomic.LoadInt64(&s.totalSelections),
		PerCredential:   perCredCopy,
	}
}
